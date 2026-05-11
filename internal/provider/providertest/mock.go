// Package providertest 提供 LLMProvider 的测试基础设施（test infrastructure）。
//
// 本包对生产二进制不可见 — 仅在测试编译单元中被引用 — 因此可以放心承载
// 仅用于集成测试和早期开发的桩实现（Stub），不会污染发行版本。
//
// 典型使用：
//
//	import "github.com/harness9/internal/provider/providertest"
//
//	mock := providertest.NewMock()
//	eng := engine.NewAgentEngine(mock, registry, workDir)
//	err := eng.Run(ctx, "test prompt")
package providertest

import (
	"context"
	"sync/atomic"

	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
)

// mockProvider 是 LLMProvider 的确定性桩实现，模拟启用 Two-Stage ReAct 的完整对话：
//
//	Thinking 调用 (tools=nil)        → 模型进行深度思考
//	Action 调用 1 (tools=[bash])    → 模型发出 bash 工具调用
//	Action 调用 2 (tools=[bash])    → 模型返回最终文本回复，agent loop 终止
//
// 线程安全：使用 atomic.Int32 管理内部状态，支持并发调用。
// 每个测试用例应通过 NewMock 创建独立实例，避免 turn 计数器在测试间泄漏。
type mockProvider struct {
	// turn 记录非 Thinking 模式下的调用次数。
	// Thinking 阶段的调用（tools=nil）不计入 turn。
	turn atomic.Int32
}

// Generate 实现 LLMProvider 接口的阻塞式调用，委托给 simulateResponse。
func (m *mockProvider) Generate(_ context.Context, _ []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	return m.simulateResponse(tools), nil
}

// GenerateStream 实现 LLMProvider 接口的流式调用。
// 将 simulateResponse 的结果拆分为 StreamChunk 序列通过 channel 发送：
//   - 文本内容 → StreamChunkTextDelta（一次性发送全部文本）
//   - 工具调用 → StreamChunkToolCallStart + StreamChunkToolCallDelta
//   - 结束     → StreamChunkDone（含完整 Message）
//
// 简化的流式模拟：不逐 token 发送，因为测试关心的是事件类型和顺序，而非粒度。
func (m *mockProvider) GenerateStream(ctx context.Context, _ []schema.Message, tools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	msg := m.simulateResponse(tools)

	ch := make(chan schema.StreamChunk)
	go func() {
		defer close(ch)

		// send 内联了 ctx 感知的发送逻辑，避免对 provider 内部 helper 的依赖。
		send := func(chunk schema.StreamChunk) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- chunk:
				return true
			}
		}

		if msg.Content != "" {
			if !send(schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: msg.Content}) {
				return
			}
		}

		for i, tc := range msg.ToolCalls {
			if !send(schema.StreamChunk{
				Type: schema.StreamChunkToolCallStart,
				ToolCall: &schema.ToolCallDelta{
					Index: i,
					ID:    tc.ID,
					Name:  tc.Name,
				},
			}) {
				return
			}
			if !send(schema.StreamChunk{
				Type: schema.StreamChunkToolCallDelta,
				ToolCall: &schema.ToolCallDelta{
					Index:     i,
					Arguments: tc.Arguments,
				},
			}) {
				return
			}
		}

		send(schema.StreamChunk{Type: schema.StreamChunkDone, Message: msg})
	}()
	return ch, nil
}

// simulateResponse 根据 tools 参数和内部 turn 计数器生成确定性响应。
// 行为：
//   - tools 为空（Thinking 阶段）：返回深度思考文本
//   - tools 非空，turn==1：返回 bash 工具调用请求
//   - tools 非空，turn>1：返回纯文本最终回复（触发 loop 终止）
func (m *mockProvider) simulateResponse(tools []schema.ToolDefinition) *schema.Message {
	if len(tools) == 0 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "【深度思考】目标是检查文件。我不能直接盲猜，我需要先调用 bash 工具执行 ls 命令，看看当前目录下有什么，然后再做定夺。",
		}
	}

	t := m.turn.Add(1)
	if t == 1 {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "我要执行我刚才规划的步骤了。",
			ToolCalls: []schema.ToolCall{
				{ID: "call_123", Name: "bash", Arguments: []byte(`{"command": "ls -la"}`)},
			},
		}
	}

	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "根据工具返回的结果，我看到了 main.go，任务圆满完成！",
	}
}

// NewMock 构造并返回一个新的 mockProvider 实例。
// turn 计数器从 0 开始，确保首次 Action 阶段调用触发 ToolCall 响应。
// 每个测试用例都应独立创建实例，避免状态污染。
func NewMock() provider.LLMProvider {
	return &mockProvider{}
}
