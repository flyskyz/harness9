// Package engine 实现了 harness9 的核心 agent loop — 标准 ReAct 循环编排层。
//
// 每个 Turn：LLM 调用（携带完整工具列表）→ 工具执行（如有）→ Observation 注入 → 下一 Turn。
// 通过 emitter 抽象支持阻塞（Run）和流式（RunStream）两种输出模式，共享同一主循环内核。
package engine

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/memory"
	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

// Option 是 AgentEngine 的函数选项。
type Option func(*AgentEngine)

// WithMaxTurns 设置单次 Run 允许的最大 Turn 数，0 或负数表示不限制。
func WithMaxTurns(n int) Option {
	return func(e *AgentEngine) { e.maxTurns = n }
}

// WithToolTimeout 设置单个工具执行的超时时间，0 表示使用 context 原始截止时间。
func WithToolTimeout(d time.Duration) Option {
	return func(e *AgentEngine) { e.toolTimeout = d }
}

// WithMaxConcurrentTools 设置同一 Turn 内最大并发工具数，0 或负数表示不限制。
func WithMaxConcurrentTools(n int) Option {
	return func(e *AgentEngine) { e.maxConcurrentTools = n }
}

// WithContextWindow 设置模型的最大 context window（tokens），用于 TUI token 使用率展示。
// 通常通过 provider.GetModelLimits(modelName).ContextTokens 获取。
func WithContextWindow(tokens int) Option {
	return func(e *AgentEngine) { e.contextWindow = tokens }
}

// PromptBuilder 构造 Agent 的 system prompt。
// 接口定义在 engine 包（使用者侧），由 internal/context 包实现。
// 引擎通过此接口与 Context Engineering 模块解耦。
type PromptBuilder interface {
	Build() string
}

// WithPromptBuilder 设置自定义 PromptBuilder。未设置时使用内置默认文案。
func WithPromptBuilder(pb PromptBuilder) Option {
	return func(e *AgentEngine) { e.promptBuilder = pb }
}

// WithSession 绑定 Session，使 runLoop 在启动时加载历史、结束时保存新消息。
func WithSession(s memory.Session) Option {
	return func(e *AgentEngine) { e.session = s }
}

// WithCompactor 绑定上下文压缩策略，在每次 LLM 调用前裁剪历史消息。
func WithCompactor(c memory.Compactor) Option {
	return func(e *AgentEngine) { e.compactor = c }
}

// WithPlanMode 设置 Agent 的初始执行模式。
func WithPlanMode(mode planning.PlanMode) Option {
	return func(e *AgentEngine) { e.planMode = mode }
}

// WithTodoStore 绑定 TodoStore，使引擎在 runLoop 生命周期中加载/保存任务列表。
func WithTodoStore(s *planning.TodoStore) Option {
	return func(e *AgentEngine) { e.todoStore = s }
}

// SetSession 替换当前绑定的 Session，供 TUI /new、/resume 命令切换会话时调用。
// 线程安全：可从任意 goroutine 调用（如 TUI goroutine）。
func (e *AgentEngine) SetSession(s memory.Session) {
	e.mu.Lock()
	e.session = s
	e.mu.Unlock()
}

// SetPlanMode 线程安全地更新当前执行模式。TUI Shift+Tab 调用此方法。
func (e *AgentEngine) SetPlanMode(mode planning.PlanMode) {
	e.mu.Lock()
	e.planMode = mode
	e.mu.Unlock()
}

// AgentEngine 是 harness9 agent loop 的核心编排器，将 LLM Provider（"大脑"）
// 与 Tool Registry（"双手"）组合在一起，执行多轮 ReAct 循环直到任务完成。
type AgentEngine struct {
	provider           provider.LLMProvider
	registry           tools.Registry
	workDir            string
	maxTurns           int
	toolTimeout        time.Duration
	maxConcurrentTools int
	contextWindow      int // 模型 context window（tokens），用于 TUI 展示，0 表示未知
	promptBuilder      PromptBuilder
	mu                 sync.RWMutex        // protects session and compactor
	session            memory.Session      // 可选，nil 表示无持久化
	compactor          memory.Compactor    // 可选，nil 表示不压缩
	planMode           planning.PlanMode   // 当前执行模式，影响工具过滤
	todoStore          *planning.TodoStore // 可选，nil 表示无 planning
}

// NewAgentEngine 创建新的 AgentEngine。默认值：maxTurns=50, toolTimeout=60s。
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, opts ...Option) *AgentEngine {
	e := &AgentEngine{
		provider:    p,
		registry:    r,
		workDir:     workDir,
		maxTurns:    50,
		toolTimeout: 60 * time.Second,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// emitter 封装 Run 与 RunStream 在"输出侧"的差异：
//   - generate:     如何执行一次 LLM 调用并处理输出（阻塞打印 stdout vs 流式发事件）
//   - toolStart:    工具开始时的副作用（仅日志 vs 日志 + EventToolStart）
//   - toolDone:     工具完成时的副作用（仅日志 vs 日志 + EventToolResult）
//   - tokenUpdate:  报告 token 用量（仅日志 vs 日志 + EventTokenUpdate）
//   - compaction:   上下文压缩时报告压缩详情（仅日志 vs 日志 + EventCompaction）
//
// toolStart / toolDone 在 per-tool goroutine 中并发调用，实现方需自行保证安全。
type emitter struct {
	// generate 执行一次 LLM 调用，返回响应 Message 和实际 token 用量（可能为 nil）。
	generate  func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error)
	toolStart func(turn int, tc schema.ToolCall)
	toolDone  func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration)
	// tokenUpdate 报告当前 context 的 token 用量。
	// 在 LLM 调用前以估算值调用；调用后若有实际用量则以实际值再次调用。
	// tokens = token 数；window = 模型 context window（0 表示未知）。
	tokenUpdate func(tokens, window int)
	// compaction 在上下文发生有效压缩时调用（token 数减少 > 5%）。
	compaction func(data CompactionData)
}

// Run 执行单个用户 prompt 的阻塞式主循环，文本输出到 stdout。
func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	em := emitter{
		generate: func(ctx context.Context, _ int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			msg, usage, err := e.provider.Generate(ctx, history, tools)
			if err != nil {
				return nil, nil, err
			}
			if msg.Content != "" {
				fmt.Printf("[assistant] %s\n", msg.Content)
			}
			return msg, usage, nil
		},
		toolStart: func(turn int, tc schema.ToolCall) {
			log.Print(logfmt.FormatToolStart("engine", turn, tc))
		},
		toolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
			log.Print(logfmt.FormatToolDone("engine", turn, tc, result, d))
		},
		tokenUpdate: func(tokens, window int) {
			log.Print(logfmt.FormatMsg("engine", fmt.Sprintf("context tokens: ~%s", memory.FormatTokenCount(tokens))))
		},
		compaction: func(data CompactionData) {
			log.Print(logfmt.FormatMsg("engine", fmt.Sprintf(
				"context compacted: %s → %s tokens (%d → %d msgs)",
				memory.FormatTokenCount(data.TokensBefore),
				memory.FormatTokenCount(data.TokensAfter),
				data.MsgsBefore, data.MsgsAfter,
			)))
		},
	}
	return e.runLoop(ctx, userPrompt, "engine", em)
}

// runLoop 是 Run 与 RunStream 共享的主循环内核。
func (e *AgentEngine) runLoop(ctx context.Context, userPrompt string, logPrefix string, em emitter) error {
	log.Print(logfmt.FormatLoopStart(logPrefix, e.workDir, e.maxTurns, e.toolTimeout, e.maxConcurrentTools))

	// 在循环开始时快照 session 和 compactor，避免与 TUI goroutine 的 SetSession 产生数据竞争。
	e.mu.RLock()
	sess := e.session
	comp := e.compactor
	planMode := e.planMode
	todoStore := e.todoStore
	e.mu.RUnlock()

	// 启动时从 Session 恢复 TodoStore 状态（跨会话续接未完成任务）。
	if sess != nil && todoStore != nil {
		if todos, err := sess.GetTodos(ctx); err != nil {
			log.Print(logfmt.FormatMsg(logPrefix, fmt.Sprintf("加载 todos 失败: %v", err)))
		} else {
			todoStore.Write(todos)
		}
	}

	// Plan Mode：注入规划行为约束（write_file/edit_file 已由 filterReadOnlyTools 在工具层硬性过滤，
	// 此处只补充 bash 只读限制和 todo_write 输出要求等无法在工具层表达的行为规则）。
	if planMode == planning.PlanModePlan {
		userPrompt = "分析以下请求，用 todo_write 输出一份可直接执行的实现计划，然后用纯文字简述计划后停止。\n" +
			"todo 项要求：每条对应一个具体的实现动作（例如：创建某文件、实现某函数、运行某命令），\n" +
			"而非高层规划描述（禁止写\"需求澄清\"、\"方案设计\"之类无法直接执行的条目）。\n" +
			"如需了解当前代码库，可使用 read_file 或 bash（只读命令：ls、cat、find、grep）。\n" +
			"不要创建文件、执行 build/install 或做任何实际修改。\n\n" +
			userPrompt
	}

	contextHistory, startLen := e.loadHistoryWith(ctx, userPrompt, sess)

	// 结束时将 TodoStore 持久化到 Session（write-replace）。
	defer func() {
		if sess != nil && todoStore != nil {
			if err := sess.SaveTodos(ctx, todoStore.Read()); err != nil {
				log.Print(logfmt.FormatMsg(logPrefix, fmt.Sprintf("保存 todos 失败: %v", err)))
			}
		}
	}()

	turnCount := 0
	overallStart := time.Now()

	for {
		turnCount++

		if e.maxTurns > 0 && turnCount > e.maxTurns {
			return fmt.Errorf("已达最大 Turn 数 (%d)，循环终止", e.maxTurns)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("context 已取消: %w", ctx.Err())
		default:
		}

		availableTools := e.registry.GetAvailableTools()
		if planMode == planning.PlanModePlan {
			availableTools = filterReadOnlyTools(availableTools)
		}
		toolTokens := memory.EstimateToolTokens(availableTools)

		// Preflight token check: estimate tokens before and after compaction.
		msgTokensBefore := memory.EstimateTokens(contextHistory)
		compactedHistory := e.applyCompactionWith(comp, contextHistory)
		msgTokensAfter := memory.EstimateTokens(compactedHistory)
		totalTokens := msgTokensAfter + toolTokens

		// Emit EventCompaction if compaction reduced tokens by > 5%.
		if comp != nil && msgTokensAfter < int(float64(msgTokensBefore)*0.95) {
			em.compaction(CompactionData{
				TokensBefore: msgTokensBefore + toolTokens,
				TokensAfter:  totalTokens,
				MsgsBefore:   len(contextHistory),
				MsgsAfter:    len(compactedHistory),
			})
		}

		// Report current context token usage to TUI / CLI.
		em.tokenUpdate(totalTokens, e.contextWindow)

		turnStart := time.Now()
		log.Print(logfmt.FormatTurnStart(logPrefix, turnCount, len(compactedHistory), len(availableTools)))

		llmStart := time.Now()
		responseMsg, usage, err := em.generate(ctx, turnCount, compactedHistory, availableTools)
		if err != nil {
			return fmt.Errorf("模型生成失败 (turn %d): %w", turnCount, err)
		}
		llmDuration := time.Since(llmStart)

		// 用实际 API 返回的 token 用量更新显示，替代之前的估算值。
		if usage != nil && usage.InputTokens > 0 {
			em.tokenUpdate(usage.InputTokens, e.contextWindow)
		}

		contextHistory = append(contextHistory, *responseMsg)

		if len(responseMsg.ToolCalls) == 0 {
			log.Print(logfmt.FormatTurnDone(logPrefix, turnCount, llmDuration, time.Since(overallStart)))
			break
		}

		toolStart := time.Now()
		results := e.executeTools(ctx, turnCount, responseMsg.ToolCalls, logPrefix, em)
		toolDuration := time.Since(toolStart)

		for i, toolCall := range responseMsg.ToolCalls {
			contextHistory = append(contextHistory, schema.Message{
				Role:       schema.RoleUser,
				Content:    results[i].Output,
				ToolCallID: toolCall.ID,
			})
		}

		log.Print(logfmt.FormatObservation(logPrefix, turnCount, len(contextHistory), llmDuration, toolDuration, time.Since(turnStart)))
	}

	e.saveHistoryWith(ctx, sess, contextHistory, startLen)
	log.Print(logfmt.FormatLoopEnd(logPrefix, turnCount, time.Since(overallStart)))
	return nil
}

// buildSystemPrompt 返回 system prompt 字符串。
// 若设置了 PromptBuilder 则委托给它，否则回退到内置默认文案。
func (e *AgentEngine) buildSystemPrompt() string {
	if e.promptBuilder != nil {
		return e.promptBuilder.Build()
	}
	return fmt.Sprintf(`Your name is harness9. Always refer to yourself as "harness9" — never as "AI assistant", "language model", or any other generic term.

harness9 is a general-purpose AI agent with full access to the user's computer.

Capabilities:
- Run shell commands to execute programs, manage processes, install packages, and interact with the OS
- Read, write, and edit files across the filesystem
- Chain multiple tools together to complete complex, multi-step tasks autonomously

Working directory: %s

Guidelines:
- Investigate before acting: read files and run diagnostic commands first
- Work in small verifiable steps; check results after each significant action
- When a command fails, diagnose the root cause rather than guessing
- Prefer targeted edits over full rewrites; preserve existing style and conventions
- If a task is ambiguous, choose the most reasonable interpretation and proceed`, e.workDir)
}

// loadHistoryWith 从 sess 加载历史消息，注入 system prompt 和当前用户输入。
// sess 为 nil 时退化为原有行为（全新 contextHistory）。
// 返回完整历史切片和新消息的起始索引（用于 saveHistoryWith）。
func (e *AgentEngine) loadHistoryWith(ctx context.Context, userPrompt string, sess memory.Session) ([]schema.Message, int) {
	var history []schema.Message
	if sess != nil {
		msgs, err := sess.GetMessages(ctx, 0)
		if err != nil {
			log.Print(logfmt.FormatMsg("engine", fmt.Sprintf("加载会话历史失败: %v", err)))
		} else {
			history = msgs
		}
	}
	// system prompt 不持久化到 DB，每次调用时重新注入
	if len(history) == 0 || history[0].Role != schema.RoleSystem {
		history = append([]schema.Message{{Role: schema.RoleSystem, Content: e.buildSystemPrompt()}}, history...)
	}
	startLen := len(history) // 新消息从此处开始；system prompt 不计入持久化范围
	history = append(history, schema.Message{Role: schema.RoleUser, Content: userPrompt})
	return history, startLen
}

// applyCompactionWith 对消息列表应用压缩策略。comp 为 nil 时原样返回。
func (e *AgentEngine) applyCompactionWith(comp memory.Compactor, msgs []schema.Message) []schema.Message {
	if comp == nil {
		return msgs
	}
	return comp.Compact(msgs)
}

// saveHistoryWith 将本次 Run 新增的消息（msgs[startLen:]）写回 sess。
// sess 为 nil 时为 no-op；失败仅打 warning 日志，不中断主流程。
func (e *AgentEngine) saveHistoryWith(ctx context.Context, sess memory.Session, msgs []schema.Message, startLen int) {
	if sess == nil || startLen >= len(msgs) {
		return
	}
	newMsgs := msgs[startLen:]
	if err := sess.AddMessages(ctx, newMsgs); err != nil {
		log.Print(logfmt.FormatMsg("engine", fmt.Sprintf("保存会话历史失败: %v", err)))
	}
}

// planModeWhitelist 是 Plan Mode 下允许 LLM 调用的工具名称集合。
// 包含 todo_write：Plan Mode 的核心目标是让 LLM 通过 todo_write 输出结构化计划。
var planModeWhitelist = map[string]bool{
	"read_file":  true,
	"bash":       true,
	"use_skill":  true,
	"todo_write": true,
}

// filterReadOnlyTools 返回 tools 中属于 planModeWhitelist 的子集。
func filterReadOnlyTools(tools []schema.ToolDefinition) []schema.ToolDefinition {
	var result []schema.ToolDefinition
	for _, t := range tools {
		if planModeWhitelist[t.Name] {
			result = append(result, t)
		}
	}
	return result
}

// executeTools 并发执行所有工具调用，每个工具带有独立的超时控制。
// 通过预分配切片 + 索引写入保证结果顺序与 ToolCalls 一致。
func (e *AgentEngine) executeTools(ctx context.Context, turn int, toolCalls []schema.ToolCall, logPrefix string, em emitter) []schema.ToolResult {
	log.Print(logfmt.FormatParallelTools(logPrefix, turn, len(toolCalls), e.maxConcurrentTools))

	results := make([]schema.ToolResult, len(toolCalls))
	var wg sync.WaitGroup

	var sem chan struct{}
	if e.maxConcurrentTools > 0 {
		sem = make(chan struct{}, e.maxConcurrentTools)
	}

	for i, toolCall := range toolCalls {
		wg.Add(1)
		go func(idx int, tc schema.ToolCall) {
			defer wg.Done()

			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}

			toolCtx := ctx
			var cancel context.CancelFunc
			if e.toolTimeout > 0 {
				toolCtx, cancel = context.WithTimeout(ctx, e.toolTimeout)
				defer cancel()
			}

			em.toolStart(turn, tc)

			start := time.Now()
			results[idx] = e.registry.Execute(toolCtx, tc)
			em.toolDone(turn, tc, results[idx], time.Since(start))
		}(i, toolCall)
	}

	wg.Wait()
	return results
}
