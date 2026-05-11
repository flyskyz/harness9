// Package engine — 流式输出支持。
//
// 本文件提供 RunStream 流式接口，是 agent_loop.go 中 Run 阻塞接口的流式对应。
// RunStream 复用 runLoop 共享内核，通过 emitter 注入"输出侧"差异：
// 将 LLM 文本增量、工具进度等以语义化 Event 通过 Go channel 推送给消费者。
//
// # 双层 channel 转换
//
//	Provider.GenerateStream() → chan StreamChunk → streamGenerate → chan Event → 客户端
//
// Provider 层产出底层 token 级增量（StreamChunk），引擎层将其转化为面向客户端的语义
// 事件（Event）。客户端只需关心业务含义，不必感知 SDK 差异。
//
// # 与阻塞模式的关系
//
// Run 与 RunStream 共享：
//   - runLoop 主循环骨架（Turn 计数、终止条件、Two-Stage 编排、并发工具执行）
//   - 工具执行日志格式化（formatToolStartLog / formatToolDoneLog）
//
// 仅在 emitter 回调中体现差异：
//   - generate:   阻塞调用 Provider.Generate vs 流式调用 GenerateStream 并转发 delta
//   - phaseDone:  阻塞打印到 stdout vs 流式无需重复（delta 已发送）
//   - toolStart:  仅日志 vs 日志 + EventToolStart
//   - toolDone:   仅日志 vs 日志 + EventToolResult
package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/schema"
)

// EventType 枚举了引擎面向客户端的流式事件类型。
// 与 Provider 层的 StreamChunkType 不同，Event 是经过引擎语义化处理的事件：
// 引擎根据当前 phase 把 text_delta 映射为 EventThinkingDelta / EventActionDelta；
// 引擎在工具执行前后发送 EventToolStart 和 EventToolResult。
type EventType string

const (
	// EventThinkingDelta 表示 Thinking 阶段的文本增量。
	// 仅在 enableThinking == true 时产生。Data 类型为 string（token 文本）。
	EventThinkingDelta EventType = "thinking_delta"

	// EventActionDelta 表示 Action 阶段的文本增量。
	// 无论是否启用 Thinking 模式，Action 阶段的文本输出都会触发此事件。
	// Data 类型为 string（token 文本）。
	EventActionDelta EventType = "action_delta"

	// EventToolStart 表示引擎开始执行一个工具调用。
	// 在引擎通过 Registry 分发工具执行时触发（而非 LLM 流式输出工具调用请求时）。
	// Data 类型为 schema.ToolCall（含 Name、ID、Arguments）。
	EventToolStart EventType = "tool_start"

	// EventToolResult 表示一个工具执行完成。
	// 每个并发执行的工具完成后都会独立触发此事件，顺序不固定。
	// Data 类型为 schema.ToolResult（含 Output、IsError）。
	EventToolResult EventType = "tool_result"

	// EventDone 表示 agent loop 正常结束（模型不再请求工具调用）。
	EventDone EventType = "done"

	// EventError 表示 agent loop 中发生了错误。
	// 可能的原因：MaxTurns 超限、context 取消、Provider 流式错误等。
	// Data 类型为 string（错误描述）。
	EventError EventType = "error"
)

// Event 是引擎面向客户端的流式事件单元。RunStream 返回 <-chan Event，
// 客户端从 channel 中读取事件实现实时交互。
//
// 典型消费方式：
//
//	for evt := range stream {
//	    switch evt.Type {
//	    case engine.EventActionDelta:
//	        fmt.Print(evt.Data.(string))
//	    case engine.EventToolResult:
//	        result := evt.Data.(schema.ToolResult)
//	        fmt.Println(result.Output)
//	    case engine.EventDone:
//	        // 循环结束
//	    case engine.EventError:
//	        log.Fatal(evt.Data.(string))
//	    }
//	}
type Event struct {
	// Type 事件类型，决定 Data 字段的实际类型。
	Type EventType `json:"type"`

	// Turn 当前事件所属的 Turn 编号（从 1 开始）。
	Turn int `json:"turn,omitempty"`

	// Data 事件载荷，类型随 Type 变化：
	//   - EventThinkingDelta / EventActionDelta → string（token 文本）
	//   - EventToolStart   → schema.ToolCall
	//   - EventToolResult  → schema.ToolResult
	//   - EventDone        → nil
	//   - EventError       → string
	Data any `json:"data,omitempty"`
}

// sendEvent 向 Event channel 发送事件，同时感知 context 取消。
// 用于循环内部的事件发送，避免在 context 已取消时无谓地阻塞在 channel 写入上。
// 返回 false 表示 context 已取消，调用方应立即退出 goroutine。
//
// 注意：终止事件（EventDone / EventError）应使用直接 ch <- 而非本函数，
// 因为 context 取消时本函数会丢弃事件，但终止事件需要确保消费者收到。
func sendEvent(ctx context.Context, ch chan<- Event, evt Event) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- evt:
		return true
	}
}

// RunStream 是 Run 的流式对应方法，通过 Go channel 逐事件输出 agent loop 的运行状态。
//
// 与 Run 的核心区别：
//   - Run 调用 provider.Generate（阻塞），RunStream 调用 provider.GenerateStream（流式逐 token）
//   - Run 通过 fmt.Printf 直接输出文本，RunStream 通过 Event channel 输出
//   - Run 返回 error，RunStream 通过 EventError 事件报告错误
//
// 内部启动独立 goroutine 运行共享 runLoop，返回只读 channel 供消费者读取。
// channel 在循环结束（正常 / 异常 / context 取消）后自动关闭。
// 配置（MaxTurns、ToolTimeout 等）与 Run 完全一致，两种模式共享同一个 AgentEngine 实例。
func (e *AgentEngine) RunStream(ctx context.Context, userPrompt string) (<-chan Event, error) {
	ch := make(chan Event)

	go func() {
		defer close(ch)

		em := emitter{
			generate: func(ctx context.Context, ph phase, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
				return e.streamGenerate(ctx, ch, ph, turn, history, tools)
			},
			phaseDone: func(_ phase, _ int, _ string) {
				// 流式模式：文本已通过 EventThinkingDelta / EventActionDelta 逐 token 发送，
				// 阶段结束时无需重复输出。
			},
			toolStart: func(turn int, tc schema.ToolCall) {
				log.Print(logfmt.FormatToolStart("engine-stream", turn, tc))
				sendEvent(ctx, ch, Event{Type: EventToolStart, Turn: turn, Data: tc})
			},
			toolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
				log.Print(logfmt.FormatToolDone("engine-stream", turn, tc, result, d))
				sendEvent(ctx, ch, Event{Type: EventToolResult, Turn: turn, Data: result})
			},
		}

		// 运行共享主循环，把返回值翻译成终止事件。
		// 注意：终止事件使用直接 ch <- 而非 sendEvent，以保证 context 已取消时
		// 消费者仍能收到错误信息（消费者 goroutine 不受 ctx 影响，仍在 range 读取）。
		if err := e.runLoop(ctx, userPrompt, "engine-stream", em); err != nil {
			ch <- Event{Type: EventError, Data: err.Error()}
			return
		}
		ch <- Event{Type: EventDone}
	}()

	return ch, nil
}

// streamGenerate 是 RunStream 用于驱动 Provider.GenerateStream 的桥接器。
// 它将底层 StreamChunk 逐个读取并转换为面向客户端的语义 Event，最终返回
// 累积完成的完整 Message 供 runLoop 注入到对话上下文。
//
// 工作流程：
//  1. 调用 provider.GenerateStream 获取 <-chan StreamChunk
//  2. 根据 phase 决定文本 delta 应发送的事件类型（thinking_delta / action_delta）
//  3. 逐 chunk 读取：
//     - text_delta → 转发为相应的 EventXxxDelta
//     - tool_call_start / tool_call_delta → 忽略（工具事件在 executeTools 中发送）
//     - done → 提取完整 Message
//     - error → 返回 error，由 runLoop 翻译成 EventError
//
// 返回 error 时，runLoop 会包装该 error 并最终通过 EventError 报告给消费者。
func (e *AgentEngine) streamGenerate(ctx context.Context, ch chan<- Event, ph phase, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	stream, err := e.provider.GenerateStream(ctx, history, tools)
	if err != nil {
		return nil, err
	}

	// 根据 phase 选择文本增量事件的具体类型，让消费者能区分"思考流"和"行动流"。
	deltaType := EventActionDelta
	if ph == phaseThinking {
		deltaType = EventThinkingDelta
	}

	var msg *schema.Message
	for chunk := range stream {
		switch chunk.Type {
		case schema.StreamChunkTextDelta:
			if !sendEvent(ctx, ch, Event{Type: deltaType, Turn: turn, Data: chunk.Delta}) {
				// context 已取消，返回错误让 runLoop 走错误路径退出
				return nil, ctx.Err()
			}
		case schema.StreamChunkToolCallStart, schema.StreamChunkToolCallDelta:
			// 工具调用请求已到达 Provider 流，但实际执行在 executeTools 中进行。
			// EventToolStart / EventToolResult 在那里统一发送，避免重复语义。
		case schema.StreamChunkDone:
			msg = chunk.Message
		case schema.StreamChunkError:
			return nil, fmt.Errorf("%s", chunk.Error)
		}
	}

	// 防御性检查：Provider 必须在流结束前发送 StreamChunkDone。
	if msg == nil {
		return nil, fmt.Errorf("provider stream ended without done chunk")
	}
	return msg, nil
}
