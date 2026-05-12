// Package engine — 流式输出支持。
//
// RunStream 是 Run 的流式对应方法，复用 runLoop 共享内核，通过 emitter 注入输出侧差异：
// LLM 文本增量和工具进度通过 Go channel 以语义化 Event 推送给消费者。
//
// 数据流：Provider.GenerateStream → chan StreamChunk → streamGenerate → chan Event → 客户端
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
type EventType string

const (
	// EventActionDelta 表示 Action 阶段的文本增量（逐 token）。Data 类型为 string。
	EventActionDelta EventType = "action_delta"

	// EventToolStart 表示引擎开始执行一个工具调用。Data 类型为 schema.ToolCall。
	EventToolStart EventType = "tool_start"

	// EventToolResult 表示一个工具执行完成。Data 类型为 schema.ToolResult。
	EventToolResult EventType = "tool_result"

	// EventDone 表示 agent loop 正常结束。
	EventDone EventType = "done"

	// EventError 表示 agent loop 中发生了错误。Data 类型为 string（错误描述）。
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
//	    case engine.EventDone:
//	        return
//	    case engine.EventError:
//	        log.Fatal(evt.Data.(string))
//	    }
//	}
type Event struct {
	Type EventType `json:"type"`
	Turn int       `json:"turn,omitempty"`
	// Data 事件载荷，类型随 Type 变化：
	//   EventActionDelta → string, EventToolStart → schema.ToolCall,
	//   EventToolResult → schema.ToolResult, EventDone → nil, EventError → string
	Data any `json:"data,omitempty"`
}

// sendEvent 向 Event channel 发送事件，同时感知 context 取消。
// 返回 false 表示 context 已取消，调用方应立即退出。
// 终止事件（EventDone / EventError）应使用直接 ch <- 而非本函数，以确保消费者收到。
func sendEvent(ctx context.Context, ch chan<- Event, evt Event) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- evt:
		return true
	}
}

// RunStream 是 Run 的流式对应方法，通过 Go channel 逐事件输出 agent loop 的运行状态。
// 内部启动独立 goroutine 运行共享 runLoop，channel 在循环结束后自动关闭。
func (e *AgentEngine) RunStream(ctx context.Context, userPrompt string) (<-chan Event, error) {
	ch := make(chan Event)

	go func() {
		defer close(ch)

		em := emitter{
			generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
				return e.streamGenerate(ctx, ch, turn, history, tools)
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

		if err := e.runLoop(ctx, userPrompt, "engine-stream", em); err != nil {
			ch <- Event{Type: EventError, Data: err.Error()}
			return
		}
		ch <- Event{Type: EventDone}
	}()

	return ch, nil
}

// streamGenerate 驱动 Provider.GenerateStream，将 text_delta 转发为 EventActionDelta，
// 最终返回 StreamChunkDone 中的完整 Message 供 runLoop 注入对话上下文。
func (e *AgentEngine) streamGenerate(ctx context.Context, ch chan<- Event, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	stream, err := e.provider.GenerateStream(ctx, history, tools)
	if err != nil {
		return nil, err
	}

	var msg *schema.Message
	for chunk := range stream {
		switch chunk.Type {
		case schema.StreamChunkTextDelta:
			if !sendEvent(ctx, ch, Event{Type: EventActionDelta, Turn: turn, Data: chunk.Delta}) {
				return nil, ctx.Err()
			}
		case schema.StreamChunkDone:
			msg = chunk.Message
		case schema.StreamChunkError:
			return nil, fmt.Errorf("%s", chunk.Error)
		}
	}

	if msg == nil {
		return nil, fmt.Errorf("provider stream ended without done chunk")
	}
	return msg, nil
}
