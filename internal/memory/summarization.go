package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/harness9/internal/schema"
)

// Summarizer 抽象了摘要压缩所需的 LLM 调用能力。
// 接口定义在使用者侧（memory 包），任何实现了 Generate 方法的 provider 均满足此接口。
type Summarizer interface {
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error)
}

// TodoInjector 由 planning.TodoStore 实现，将活跃任务注入上下文压缩摘要。
// 定义在 memory 包（使用者侧），符合 Go 接口定义惯例。
type TodoInjector interface {
	FormatForInjection() string
}

const (
	// summaryMarker 用于标识摘要消息，支持在下次压缩时识别并增量更新。
	summaryMarker = "[Conversation Summary]"

	summarySystemPrompt = `You are a conversation summarizer. Produce a concise structured summary that preserves essential context for continuing the conversation. Output only the summary — no preamble, no explanation.`

	// summaryTemplate 用于首次摘要请求。
	summaryTemplate = "Summarize the following conversation into this structure:\n\n" +
		"**Goal:** What the user is trying to accomplish.\n" +
		"**Progress:** Key actions taken and their results.\n" +
		"**Key Decisions:** Important choices and rationale.\n" +
		"**Next Steps:** What was planned or pending when this segment ends.\n" +
		"**Critical Context:** Facts, file paths, variable names, or constraints the agent must remember.\n\n" +
		"Conversation:\n%s"

	// incrementalTemplate 用于在已有摘要的基础上进行增量更新。
	incrementalTemplate = "Update the existing summary by merging in new conversation content. " +
		"Output the merged summary in the same structure — no preamble.\n\n" +
		"<previous-summary>\n%s\n</previous-summary>\n\n" +
		"New conversation to merge:\n%s"
)

// SummarizationCompactor 使用 LLM 将旧消息压缩为结构化摘要，
// 在大幅减少 token 用量的同时保留任务关键上下文。
//
// 压缩策略：
//  1. token 估算值 ≤ MaxTokens → 直接返回（无需压缩）
//  2. 消息分割：system | head（旧消息） | tail（最近 MinTailMessages 条）
//  3. 调用 Provider 摘要 head → 单条摘要消息替代整个 head
//  4. Provider 调用失败 → 回退到 Fallback Compactor（默认 TokenBudgetCompactor）
//  5. 压缩后执行双向孤立工具对修复
//
// 增量更新：若 head 中已含摘要消息（以 summaryMarker 开头），
// 则向 LLM 提供旧摘要 + 新对话，请求合并更新，避免信息叠加丢失。
type SummarizationCompactor struct {
	Provider        Summarizer
	MaxTokens       int
	MinTailMessages int
	// Fallback 在 Provider 调用失败时使用。若为 nil，则创建同配置的 TokenBudgetCompactor。
	Fallback Compactor
	// TodoInjector 若非 nil，在每次摘要末尾注入活跃任务列表。
	TodoInjector TodoInjector
}

// CompactorOption 是 NewSummarizationCompactor 的函数选项。
type CompactorOption func(*SummarizationCompactor)

// WithTodoInjector 在摘要末尾注入活跃任务列表，防止 LLM 在上下文压缩后遗忘未完成任务。
func WithTodoInjector(ti TodoInjector) CompactorOption {
	return func(c *SummarizationCompactor) { c.TodoInjector = ti }
}

// NewSummarizationCompactor 创建针对指定 context window 大小的 SummarizationCompactor。
// MaxTokens 自动设为 contextWindow 的 80%；MinTailMessages 默认 6。
func NewSummarizationCompactor(p Summarizer, contextWindow int, opts ...CompactorOption) *SummarizationCompactor {
	c := &SummarizationCompactor{
		Provider:        p,
		MaxTokens:       contextWindow * 80 / 100,
		MinTailMessages: 6,
		Fallback:        NewTokenBudgetCompactor(contextWindow),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Compact 在 token 超出预算时调用 LLM 摘要旧消息，返回压缩后的消息列表。
func (c *SummarizationCompactor) Compact(msgs []schema.Message) []schema.Message {
	if EstimateTokens(msgs) <= c.maxTokens() {
		return msgs
	}
	if len(msgs) == 0 || msgs[0].Role != schema.RoleSystem {
		return msgs
	}

	minTail := c.minTail()
	rest := msgs[1:] // non-system messages

	if len(rest) <= minTail {
		return msgs
	}

	headEnd := len(rest) - minTail
	head := rest[:headEnd]
	tail := rest[headEnd:]

	summary, err := c.summarize(head)
	if err != nil {
		return c.fallback().Compact(msgs)
	}

	summaryContent := summaryMarker + "\n" + summary
	if c.TodoInjector != nil {
		if todoText := c.TodoInjector.FormatForInjection(); todoText != "" {
			summaryContent += "\n\n## Active Tasks\n" + todoText
		}
	}
	summaryMsg := schema.Message{
		Role:    schema.RoleUser,
		Content: summaryContent,
	}

	result := make([]schema.Message, 0, 2+len(tail))
	result = append(result, msgs[0])    // system 始终保留
	result = append(result, summaryMsg) // LLM 生成的摘要
	result = append(result, tail...)    // 最近消息原样保留

	return repairOrphanedToolPairs(result)
}

// summarize 调用 LLM 对 head 消息生成结构化摘要字符串。
// 若 head 中已包含上次摘要（summaryMarker），则执行增量更新。
func (c *SummarizationCompactor) summarize(head []schema.Message) (string, error) {
	if c.Provider == nil {
		return "", fmt.Errorf("SummarizationCompactor: Provider is nil")
	}
	var prevSummary string
	var lines []string

	for _, m := range head {
		// 检测上次摘要消息，提取内容用于增量更新，不将其加入对话文本。
		if strings.HasPrefix(m.Content, summaryMarker) {
			prevSummary = strings.TrimPrefix(m.Content, summaryMarker+"\n")
			continue
		}
		// 工具执行结果（Observation）
		if m.ToolCallID != "" {
			lines = append(lines, fmt.Sprintf("[tool_result %s]: %s", m.ToolCallID, m.Content))
			continue
		}
		// 文本内容
		if m.Content != "" {
			lines = append(lines, fmt.Sprintf("[%s]: %s", m.Role, m.Content))
		}
		// 工具调用请求
		for _, tc := range m.ToolCalls {
			lines = append(lines, fmt.Sprintf("[tool_call %s(%s)]: %s", tc.Name, tc.ID, string(tc.Arguments)))
		}
	}

	conversationText := strings.Join(lines, "\n")

	var userContent string
	if prevSummary != "" {
		userContent = fmt.Sprintf(incrementalTemplate, prevSummary, conversationText)
	} else {
		userContent = fmt.Sprintf(summaryTemplate, conversationText)
	}

	sysMsg := schema.Message{Role: schema.RoleSystem, Content: summarySystemPrompt}
	userMsg := schema.Message{Role: schema.RoleUser, Content: userContent}

	// 为摘要 LLM 调用设置独立超时（60 秒）。
	// Compact 接口不传递外层 context，因此此处无法感知外层取消；
	// 超时可防止摘要请求无限阻塞，确保 runLoop 在网络异常时能够回退到 Fallback 压缩器。
	summaryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, _, err := c.Provider.Generate(summaryCtx, []schema.Message{sysMsg, userMsg}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("summarizer returned nil message")
	}
	return resp.Content, nil
}

func (c *SummarizationCompactor) maxTokens() int {
	if c.MaxTokens <= 0 {
		return 160_000
	}
	return c.MaxTokens
}

func (c *SummarizationCompactor) minTail() int {
	if c.MinTailMessages <= 0 {
		return 6
	}
	return c.MinTailMessages
}

func (c *SummarizationCompactor) fallback() Compactor {
	if c.Fallback != nil {
		return c.Fallback
	}
	return &TokenBudgetCompactor{
		MaxTokens:       c.maxTokens(),
		MinTailMessages: c.minTail(),
	}
}
