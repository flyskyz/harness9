// Package engine 实现了 harness9 的核心 agent loop — 驱动
// Two-Stage ReAct（Thinking → Action → Observation）循环的编排层。
//
// # Two-Stage ReAct 设计理念
//
// 传统 ReAct 循环在每个 Turn 中执行一次 LLM 调用，让模型同时完成推理和行动。
// 这在复杂任务中容易出现"未经深思的冲动行为"——模型在充分理解问题之前就急于调用工具。
//
// harness9 引入 Two-Stage ReAct，将每个 Turn 拆分为两个阶段：
//
//	Phase 1 — Thinking（慢思考）：剥夺所有工具，迫使模型在没有行动能力的情况下
//	           进行纯粹的推理、分析和规划。因为没有工具可用，模型必须充分理解
//	           问题、拆解任务、制定策略，而不仅仅是"试一试"。
//
//	Phase 2 — Action（行动）：恢复完整工具列表，模型基于 Phase 1 的思考结果
//	           采取有针对性的行动。此时模型已经"想清楚了"，工具调用更精准高效。
//
// # 上下文一致性保证
//
// 每个 Turn 最终只向 contextHistory 注入一条 assistant 消息。Thinking 的思考内容
// 与 Action 的行动内容会被合并为同一条消息，避免连续 assistant 消息导致的 API
// 兼容性问题（Anthropic Messages API 要求 user/assistant 严格交替）。
//
// # 引擎职责
//
//   - 维护跨 Turn 的对话上下文 (Context History)
//   - 在每个 Turn 中编排 Thinking → Action 两阶段 LLM 调用
//   - 通过 Registry 接口路由工具调用
//   - 将工具执行结果 (Observation) 回注上下文供下一轮使用
//   - 检测终止条件（模型不再发起 ToolCall）
package engine

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

// Option 是 AgentEngine 的函数选项，用于在构造时配置非必需参数。
type Option func(*AgentEngine)

// WithThinking 控制是否启用两阶段 Thinking-Action 模式。默认开启（true）。
// 关闭后退化为标准单阶段 ReAct，每个 Turn 只进行一次 LLM 调用。
func WithThinking(enabled bool) Option {
	return func(e *AgentEngine) {
		e.enableThinking = enabled
	}
}

// WithMaxTurns 设置单次 Run 允许的最大 Turn 数。n <= 0 表示不限制。
func WithMaxTurns(n int) Option {
	return func(e *AgentEngine) {
		e.maxTurns = n
	}
}

// WithToolTimeout 设置单个工具执行的超时时间。0 表示使用 context 原始截止时间。
func WithToolTimeout(d time.Duration) Option {
	return func(e *AgentEngine) {
		e.toolTimeout = d
	}
}

// WithMaxConcurrentTools 设置同一 Turn 内最大并发工具数。n <= 0 表示不限制。
// 用于防止过多的并发工具调用压垮下游服务（如 API 限频、磁盘 IO 瓶颈）。
func WithMaxConcurrentTools(n int) Option {
	return func(e *AgentEngine) {
		e.maxConcurrentTools = n
	}
}

// AgentEngine 是 harness9 agent loop 的核心编排器。它将 LLM Provider（"大脑"）
// 与 Tool Registry（"双手"）组合在一起，执行多轮 Two-Stage ReAct 循环直到任务完成。
//
// 当 enableThinking 为 true（默认）时，每个 Turn 由两次 LLM 调用组成：
//
//	Thinking 调用（tools=nil）→ Action 调用（tools=availableTools）
//
// 两次调用的结果会合并为一条 assistant 消息注入上下文，保证 API 兼容性。
//
// 当 enableThinking 为 false 时，退化为标准单阶段 ReAct：
//
//	Action 调用（tools=availableTools）
//
// 所有字段均为未导出，构造后不可变。通过 NewAgentEngine + Option 完成配置。
type AgentEngine struct {
	// provider LLM 后端，负责生成 assistant 响应（推理文本和/或工具调用请求）。
	provider provider.LLMProvider

	// registry 工具注册表，负责将 ToolCall 解析为具体执行并返回结果。
	registry tools.Registry

	// workDir agent 操作的工作区绝对路径，注入到 system prompt 中使 LLM 了解其工作上下文。
	workDir string

	// enableThinking 控制是否启用两阶段 Thinking-Action 模式。
	enableThinking bool

	// maxTurns 单次 Run 允许的最大 Turn 数。0 表示不限制。
	// 防止模型陷入无限循环，消耗过多 token。
	maxTurns int

	// toolTimeout 单个工具执行的超时时间。0 表示使用传入 context 的原始截止时间。
	// 超时后工具执行会被取消，结果标记为 IsError。
	toolTimeout time.Duration

	// maxConcurrentTools 同一 Turn 内最大并发工具数。n <= 0 表示不限制。
	maxConcurrentTools int
}

// NewAgentEngine 使用给定的 Provider、Registry 和工作目录创建新的 AgentEngine。
// 通过 Option 函数可配置 Thinking、MaxTurns、ToolTimeout 等可选参数。
//
// 默认值：
//   - enableThinking = true（开启 Two-Stage ReAct，项目核心卖点）
//   - maxTurns       = 50
//   - toolTimeout    = 60s
//
// 参数:
//   - p:       LLM Provider 实现（如 OpenAI、Anthropic 的适配器）
//   - r:       Tool Registry 实现（管理工具的注册与执行）
//   - workDir: 工作区绝对路径，注入 system prompt
//   - opts:    可选配置（WithThinking, WithMaxTurns, WithToolTimeout 等）
func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, opts ...Option) *AgentEngine {
	e := &AgentEngine{
		provider:       p,
		registry:       r,
		workDir:        workDir,
		enableThinking: true,
		maxTurns:       50,
		toolTimeout:    60 * time.Second,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// phase 标识 Two-Stage Turn 内 LLM 调用所属的阶段。
type phase int

const (
	phaseThinking phase = iota // Phase 1：剥夺工具的慢思考
	phaseAction                // Phase 2：恢复工具的精准行动；单阶段模式下也走 phaseAction
)

// emitter 封装了阻塞模式 Run 与流式模式 RunStream 在 "输出侧" 的全部差异：
//
//   - generate     如何执行一次 LLM 调用（阻塞 Generate 还是流式 GenerateStream）
//   - phaseDone    阶段完成后如何展示文本（阻塞打印到 stdout，流式无需重复 — 已通过 delta 发送）
//   - toolStart    工具开始执行时的副作用（仅日志 vs 日志 + EventToolStart）
//   - toolDone     工具完成时的副作用（仅日志 vs 日志 + EventToolResult）
//
// generate / phaseDone 仅在 runLoop 主 goroutine 中调用，无并发；
// toolStart / toolDone 在 per-tool goroutine 中并发调用，实现方需自行保证安全。
type emitter struct {
	generate  func(ctx context.Context, ph phase, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error)
	phaseDone func(ph phase, turn int, content string)
	toolStart func(turn int, tc schema.ToolCall)
	toolDone  func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration)
}

// Run 执行单个用户 prompt 的阻塞式主循环。文本通过 stdout 输出，错误直接返回。
//
//  1. 使用 system prompt（含 workDir）和用户初始消息初始化对话上下文
//  2. 进入 Two-Stage ReAct 循环（runLoop 内部）：
//     a. [Phase 1] 若启用 Thinking，先以空工具列表调用 LLM
//     b. [Phase 2] 以完整工具列表调用 LLM
//     c. 合并 Thinking + Action 为单条 assistant 消息
//     d. 若无 ToolCall → 任务完成
//     e. 否则并发执行所有 ToolCall（独立超时）
//     f. 将工具结果作为 Observation 注入上下文
//  3. 重复直至自然终止 / MaxTurns 超限 / context 取消
func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	em := emitter{
		generate: func(ctx context.Context, _ phase, _ int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
			return e.provider.Generate(ctx, history, tools)
		},
		phaseDone: func(ph phase, _ int, content string) {
			if content == "" {
				return
			}
			switch ph {
			case phaseThinking:
				fmt.Printf("[thinking] %s\n", content)
			case phaseAction:
				fmt.Printf("[assistant] %s\n", content)
			}
		},
		toolStart: func(turn int, tc schema.ToolCall) {
			log.Print(logfmt.FormatToolStart("engine", turn, tc))
		},
		toolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
			log.Print(logfmt.FormatToolDone("engine", turn, tc, result, d))
		},
	}
	return e.runLoop(ctx, userPrompt, "engine", em)
}

// runLoop 是 Run 与 RunStream 共享的主循环内核。通过 emitter 参数注入输出侧差异，
// 自身只负责 ReAct 循环编排：上下文初始化、Turn 计数、终止条件、Two-Stage 调度、
// 并发工具执行、Observation 注入。
//
// 参数：
//   - logPrefix:  日志前缀（"engine" 或 "engine-stream"），用于区分两条路径的日志
//   - em:         输出侧差异封装
func (e *AgentEngine) runLoop(ctx context.Context, userPrompt string, logPrefix string, em emitter) error {
	log.Printf("[%s] 启动 | workdir=%s thinking=%v maxTurns=%d toolTimeout=%v maxConcurrent=%d",
		logPrefix, e.workDir, e.enableThinking, e.maxTurns, e.toolTimeout, e.maxConcurrentTools)

	// 初始化对话上下文：注入 system prompt（含工作区路径）定义 agent 身份和能力，
	// 然后附上用户任务描述。
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: fmt.Sprintf(
				"You are harness9, an expert coding assistant. "+
					"You have full access to tools in the workspace. "+
					"Your working directory is: %s",
				e.workDir,
			),
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0
	overallStart := time.Now()

	for {
		turnCount++

		// --- 安全阀：防止无限循环 ---
		if e.maxTurns > 0 && turnCount > e.maxTurns {
			return fmt.Errorf("已达最大 Turn 数 (%d)，循环终止", e.maxTurns)
		}

		// 检查 context 是否已取消（支持超时和手动中断）
		select {
		case <-ctx.Done():
			return fmt.Errorf("context 已取消: %w", ctx.Err())
		default:
		}

		turnStart := time.Now()
		availableTools := e.registry.GetAvailableTools()
		log.Printf("[%s] ======== Turn %d ======== | history=%d  tools=%d  thinking=%v",
			logPrefix, turnCount, len(contextHistory), len(availableTools), e.enableThinking)

		llmStart := time.Now()
		responseMsg, err := e.runTurn(ctx, turnCount, contextHistory, availableTools, logPrefix, em)
		if err != nil {
			return err
		}
		llmDuration := time.Since(llmStart)

		contextHistory = append(contextHistory, *responseMsg)

		// --- 终止条件检测 ---
		if len(responseMsg.ToolCalls) == 0 {
			log.Printf("[%s] Turn %d | 任务完成，模型未请求工具调用 | llm=%s total=%s",
				logPrefix, turnCount, llmDuration, time.Since(turnStart))
			break
		}

		// --- ToolCall 阶段（并发执行，带独立超时） ---
		toolStart := time.Now()
		results := e.executeTools(ctx, turnCount, responseMsg.ToolCalls, logPrefix, em)
		toolDuration := time.Since(toolStart)

		// --- Observation 阶段 ---
		for i, toolCall := range responseMsg.ToolCalls {
			contextHistory = append(contextHistory, schema.Message{
				Role:       schema.RoleUser,
				Content:    results[i].Output,
				ToolCallID: toolCall.ID,
			})
		}

		log.Printf("[%s] Turn %d | Observation 注入完成 | history=%d | llm=%s tools=%s total=%s",
			logPrefix, turnCount, len(contextHistory), llmDuration, toolDuration, time.Since(turnStart))
	}

	log.Printf("[%s] 循环结束 | 总Turns=%d | total_time=%s", logPrefix, turnCount, time.Since(overallStart))
	return nil
}

// runTurn 执行一个完整的 Turn，根据 enableThinking 选择两阶段或单阶段路径。
// 返回合并后的单条 assistant 消息（避免连续 assistant 消息违反 Anthropic API 约束）。
func (e *AgentEngine) runTurn(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition, logPrefix string, em emitter) (*schema.Message, error) {
	if !e.enableThinking {
		log.Printf("[%s] Turn %d | Action (tools=%d)", logPrefix, turn, len(tools))
		msg, err := em.generate(ctx, phaseAction, turn, history, tools)
		if err != nil {
			return nil, fmt.Errorf("模型生成失败 (turn %d): %w", turn, err)
		}
		em.phaseDone(phaseAction, turn, msg.Content)
		return msg, nil
	}

	// ============================================================
	// Phase 1: Thinking（慢思考与规划）
	// ============================================================
	// 通过传入 nil 剥夺所有工具。LLM 没有行动能力，被迫进行纯推理。
	log.Printf("[%s] Turn %d | Phase 1: Thinking (tools=none)", logPrefix, turn)
	thinkResp, err := em.generate(ctx, phaseThinking, turn, history, nil)
	if err != nil {
		log.Printf("[%s] Turn %d | Thinking 阶段生成失败: %v", logPrefix, turn, err)
		return nil, fmt.Errorf("thinking 阶段生成失败 (turn %d): %w", turn, err)
	}
	// 防御性清除：确保 Thinking 响应不含 ToolCalls，防止 LLM 不遵守指令时污染 Phase 2 上下文。
	thinkResp.ToolCalls = nil
	if thinkResp.Content != "" {
		log.Printf("[%s] Turn %d | Phase 1 完成 | 思考长度=%d chars", logPrefix, turn, len(thinkResp.Content))
	} else {
		log.Printf("[%s] Turn %d | Phase 1 完成 | 思考为空", logPrefix, turn)
	}
	em.phaseDone(phaseThinking, turn, thinkResp.Content)

	// ============================================================
	// Phase 2: Action（行动与工具调用）
	// ============================================================
	// 构建 Phase 2 临时上下文：主 history + Phase 1 思考；仅本次 Generate 调用使用，
	// 不持久化到主 contextHistory。最终通过 joinContent 合并为单条 assistant 消息。
	phase2History := make([]schema.Message, len(history), len(history)+1)
	copy(phase2History, history)
	phase2History = append(phase2History, *thinkResp)

	log.Printf("[%s] Turn %d | Phase 2: Action (tools=%d)", logPrefix, turn, len(tools))
	actionResp, err := em.generate(ctx, phaseAction, turn, phase2History, tools)
	if err != nil {
		log.Printf("[%s] Turn %d | Action 阶段生成失败: %v", logPrefix, turn, err)
		return nil, fmt.Errorf("action 阶段生成失败 (turn %d): %w", turn, err)
	}
	em.phaseDone(phaseAction, turn, actionResp.Content)

	log.Printf("[%s] Turn %d | Two-Stage 合并完成 | thinking=%d chars action=%d chars toolCalls=%d",
		logPrefix, turn, len(thinkResp.Content), len(actionResp.Content), len(actionResp.ToolCalls))

	return &schema.Message{
		Role:      schema.RoleAssistant,
		Content:   joinContent(thinkResp.Content, actionResp.Content),
		ToolCalls: actionResp.ToolCalls,
	}, nil
}

// executeTools 并发执行所有工具调用，每个工具带有独立的超时控制。
// 通过预分配切片 + 索引写入保证结果顺序与 ToolCalls 一致。
//
// 并行工具调用的前提：经过 RLHF 微调的现代 LLM 在同一 Turn 内并行下发多个工具调用时，
// 必然假设这些调用互不依赖；若存在依赖，模型会主动拆分为多个 Turn。
func (e *AgentEngine) executeTools(ctx context.Context, turn int, toolCalls []schema.ToolCall, logPrefix string, em emitter) []schema.ToolResult {
	log.Printf("[%s] Turn %d | 并行执行 %d 个工具调用 (maxConcurrent=%d)", logPrefix, turn, len(toolCalls), e.maxConcurrentTools)

	results := make([]schema.ToolResult, len(toolCalls))
	var wg sync.WaitGroup

	// 信号量：限制并发工具数，防止下游过载（API 限频、磁盘 IO 瓶颈）。
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

			// 为每个工具创建带独立超时的子 context，超时仅影响当前工具。
			toolCtx := ctx
			var cancel context.CancelFunc
			if e.toolTimeout > 0 {
				toolCtx, cancel = context.WithTimeout(ctx, e.toolTimeout)
				defer cancel()
			}

			em.toolStart(turn, tc)

			toolStart := time.Now()
			results[idx] = e.registry.Execute(toolCtx, tc)
			toolDuration := time.Since(toolStart)

			em.toolDone(turn, tc, results[idx], toolDuration)
		}(i, toolCall)
	}

	wg.Wait()
	return results
}

// joinContent 将 Phase 1 的思考内容与 Phase 2 的行动内容合并为单段文本。
// 避免在上下文中出现连续的 assistant 消息。
func joinContent(thinking, action string) string {
	switch {
	case thinking == "" && action == "":
		return ""
	case thinking == "":
		return action
	case action == "":
		return thinking
	default:
		return thinking + "\n\n" + action
	}
}
