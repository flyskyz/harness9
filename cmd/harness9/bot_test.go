package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/imchannel"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

// ---- mock IMChannel ----

// mockChannel 是 IMChannel 的测试桩，直接调用注册的 handler 而不经过网络。
type mockChannel struct {
	handler imchannel.MessageHandler
}

func (c *mockChannel) SetMessageHandler(h imchannel.MessageHandler) { c.handler = h }

func (c *mockChannel) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (c *mockChannel) NewSession(chatID, messageID string) imchannel.Session {
	return newMockSession()
}

// ---- mock Session ----

// recordedCall 记录一次 Session 方法调用及其参数。
type recordedCall struct {
	method string
	args   []any
}

// mockSession 记录所有 Session 方法调用，供测试断言。
type mockSession struct {
	mu    sync.Mutex
	calls []recordedCall
}

func newMockSession() *mockSession { return &mockSession{} }

func (s *mockSession) record(method string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{method: method, args: args})
}

// callsOf 返回指定方法的所有调用记录（不加锁，仅在断言阶段调用）。
func (s *mockSession) callsOf(method string) []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedCall
	for _, c := range s.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (s *mockSession) NotifyThinking(ctx context.Context) error {
	s.record("NotifyThinking")
	return nil
}

func (s *mockSession) NotifyToolStart(ctx context.Context, tc schema.ToolCall) error {
	s.record("NotifyToolStart", tc)
	return nil
}

func (s *mockSession) NotifyToolDone(ctx context.Context, tc schema.ToolCall, result schema.ToolResult, d time.Duration) error {
	s.record("NotifyToolDone", tc, result, d)
	return nil
}

func (s *mockSession) SendReply(ctx context.Context, text string) error {
	s.record("SendReply", text)
	return nil
}

// ---- mock LLM Provider（确定性响应序列）----

// seqProvider 按序列依次返回预设响应。
type seqProvider struct {
	mu        sync.Mutex
	responses []*schema.Message
	idx       int
}

func (p *seqProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idx >= len(p.responses) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "done"}, nil
	}
	msg := p.responses[p.idx]
	p.idx++
	return msg, nil
}

func (p *seqProvider) GenerateStream(ctx context.Context, msgs []schema.Message, toolDefs []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	msg, err := p.Generate(ctx, msgs, toolDefs)
	if err != nil {
		return nil, err
	}
	ch := make(chan schema.StreamChunk, 4)
	go func() {
		defer close(ch)
		if msg.Content != "" {
			select {
			case <-ctx.Done():
				return
			case ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: msg.Content}:
			}
		}
		select {
		case <-ctx.Done():
		case ch <- schema.StreamChunk{Type: schema.StreamChunkDone, Message: msg}:
		}
	}()
	return ch, nil
}

// ---- mock Registry（静态工具列表，固定输出）----

type staticReg struct {
	toolDefs []schema.ToolDefinition
	output   string
}

func (r *staticReg) Register(_ tools.BaseTool) error { return nil }

func (r *staticReg) GetAvailableTools() []schema.ToolDefinition { return r.toolDefs }

func (r *staticReg) Execute(_ context.Context, tc schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{ToolCallID: tc.ID, Output: r.output}
}

// ---- 可植入 mockSession 的 mockChannel ----

// sessionCapturingChannel 允许测试从外部注入一个已知的 mockSession，
// 以便在 handleMessage 执行完毕后对其调用记录进行断言。
type sessionCapturingChannel struct {
	session *mockSession
}

func (c *sessionCapturingChannel) SetMessageHandler(h imchannel.MessageHandler) {}
func (c *sessionCapturingChannel) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
func (c *sessionCapturingChannel) NewSession(_, _ string) imchannel.Session { return c.session }

// ---- 辅助函数 ----

// buildEngine 构造一个使用给定 Provider 的 AgentEngine（工具超时 5s）。
func buildEngine(p provider.LLMProvider, reg tools.Registry) *engine.AgentEngine {
	return engine.NewAgentEngine(p, reg, "/test",
		engine.WithToolTimeout(5*time.Second),
	)
}

// runHandleMessage 同步执行 server.handleMessage，等待其返回。
func runHandleMessage(srv *Server, msg imchannel.IncomingMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.handleMessage(ctx, msg)
}

// ---- 测试用例 ----

// TestHandleMessage_NotifyThinkingCalledFirst 验证消息处理开始时立刻发送"思考中"占位消息。
func TestHandleMessage_NotifyThinkingCalledFirst(t *testing.T) {
	p := &seqProvider{
		responses: []*schema.Message{
			{Role: schema.RoleAssistant, Content: "hello"},
		},
	}
	reg := &staticReg{}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "hi"})

	notifyThinkingCalls := sess.callsOf("NotifyThinking")
	if len(notifyThinkingCalls) == 0 {
		t.Fatal("NotifyThinking 应在处理开始时被调用一次")
	}

	// NotifyThinking 应是第一个调用
	sess.mu.Lock()
	first := sess.calls[0].method
	sess.mu.Unlock()
	if first != "NotifyThinking" {
		t.Errorf("第一个调用应是 NotifyThinking，实际是 %s", first)
	}
}

// TestHandleMessage_SendReply_FinalText 验证 Agent 返回纯文本回复时，SendReply 被调用且携带正确内容。
func TestHandleMessage_SendReply_FinalText(t *testing.T) {
	const finalReply = "任务完成，结果如下"

	p := &seqProvider{
		responses: []*schema.Message{
			{Role: schema.RoleAssistant, Content: finalReply},
		},
	}
	reg := &staticReg{}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "do something"})

	replyCalls := sess.callsOf("SendReply")
	if len(replyCalls) == 0 {
		t.Fatal("期望 SendReply 被调用一次")
	}
	got := replyCalls[0].args[0].(string)
	if got != finalReply {
		t.Errorf("SendReply 文本应为 %q，实际为 %q", finalReply, got)
	}
}

// TestHandleMessage_ToolFlow_NotifyStartAndDone 验证工具调用流程：
// EventToolStart → NotifyToolStart，EventToolResult → NotifyToolDone。
func TestHandleMessage_ToolFlow_NotifyStartAndDone(t *testing.T) {
	p := &seqProvider{
		responses: []*schema.Message{
			// Turn 1: 发起工具调用
			{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: "tc1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)},
				},
			},
			// Turn 2: 最终回复
			{Role: schema.RoleAssistant, Content: "完成"},
		},
	}
	reg := &staticReg{
		toolDefs: []schema.ToolDefinition{{Name: "bash"}},
		output:   "main.go",
	}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "ls"})

	startCalls := sess.callsOf("NotifyToolStart")
	if len(startCalls) == 0 {
		t.Fatal("期望 NotifyToolStart 被调用一次")
	}
	startedTc := startCalls[0].args[0].(schema.ToolCall)
	if startedTc.Name != "bash" {
		t.Errorf("NotifyToolStart 工具名应为 bash，实际为 %s", startedTc.Name)
	}

	doneCalls := sess.callsOf("NotifyToolDone")
	if len(doneCalls) == 0 {
		t.Fatal("期望 NotifyToolDone 被调用一次")
	}
}

// TestHandleMessage_ToolResult_DurationNonNegative 验证工具耗时计算不会因 toolStartTimes 缺失而返回负值或异常大值。
func TestHandleMessage_ToolResult_DurationNonNegative(t *testing.T) {
	p := &seqProvider{
		responses: []*schema.Message{
			{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: "tc1", Name: "bash", Arguments: []byte(`{}`)},
				},
			},
			{Role: schema.RoleAssistant, Content: "ok"},
		},
	}
	reg := &staticReg{
		toolDefs: []schema.ToolDefinition{{Name: "bash"}},
		output:   "out",
	}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "test"})

	doneCalls := sess.callsOf("NotifyToolDone")
	if len(doneCalls) == 0 {
		t.Fatal("期望 NotifyToolDone 被调用一次")
	}
	// 耗时应在合理范围内（小于 60 秒），不应出现从 epoch 起算的天文数字
	d := doneCalls[0].args[2].(time.Duration)
	if d < 0 {
		t.Errorf("工具耗时不应为负值，实际: %v", d)
	}
	const maxReasonable = 60 * time.Second
	if d > maxReasonable {
		t.Errorf("工具耗时异常偏大（可能 toolStartTimes 计算错误），实际: %v", d)
	}
}

// TestHandleMessage_RunStreamError_SendsReply 验证 RunStream 启动失败时，
// 错误信息通过 SendReply 回传给用户。
func TestHandleMessage_RunStreamError_SendsReply(t *testing.T) {
	// 构造一个返回错误的 Provider
	errProvider := &errorProvider{err: fmt.Errorf("provider unavailable")}
	reg := &staticReg{}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	// 注意：RunStream 不会因 provider 构造失败而返回 error（仅在 goroutine 内返回），
	// 此测试验证 EventError 路径下 SendReply 被调用。
	srv := NewServer(ch, buildEngine(errProvider, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "test"})

	replyCalls := sess.callsOf("SendReply")
	if len(replyCalls) == 0 {
		t.Fatal("期望 SendReply 被调用（错误回复）")
	}
}

// TestHandleMessage_ContextCancellation_NoHang 验证 context 取消时 handleMessage 能正常退出，
// 不会无限阻塞。
func TestHandleMessage_ContextCancellation_NoHang(t *testing.T) {
	// 使用已取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &seqProvider{
		responses: []*schema.Message{
			{Role: schema.RoleAssistant, Content: "done"},
		},
	}
	reg := &staticReg{}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleMessage(ctx, imchannel.IncomingMessage{ChatID: "c1", Text: "test"})
	}()

	select {
	case <-done:
		// 正常退出
	case <-time.After(3 * time.Second):
		t.Fatal("handleMessage 在 context 取消时未能及时退出")
	}
}

// errorProvider 是一个始终返回错误的 LLM Provider，用于测试错误路径。
type errorProvider struct {
	err error
}

func (p *errorProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	return nil, p.err
}

func (p *errorProvider) GenerateStream(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk, 1)
	err := p.err
	go func() {
		defer close(ch)
		ch <- schema.StreamChunk{
			Type:  schema.StreamChunkError,
			Error: err.Error(),
		}
	}()
	return ch, nil
}

// TestHandleMessage_MultipleToolCalls_AllNotified 验证同一 Turn 中多个工具调用都分别触发
// NotifyToolStart 和 NotifyToolDone。
func TestHandleMessage_MultipleToolCalls_AllNotified(t *testing.T) {
	p := &seqProvider{
		responses: []*schema.Message{
			{
				Role: schema.RoleAssistant,
				ToolCalls: []schema.ToolCall{
					{ID: "tc1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)},
					{ID: "tc2", Name: "bash", Arguments: []byte(`{"command":"pwd"}`)},
				},
			},
			{Role: schema.RoleAssistant, Content: "完成"},
		},
	}
	reg := &staticReg{
		toolDefs: []schema.ToolDefinition{{Name: "bash"}},
		output:   "result",
	}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "two tools"})

	if len(sess.callsOf("NotifyToolStart")) != 2 {
		t.Errorf("期望 NotifyToolStart 被调用 2 次（每个工具各一次），实际 %d 次",
			len(sess.callsOf("NotifyToolStart")))
	}
	if len(sess.callsOf("NotifyToolDone")) != 2 {
		t.Errorf("期望 NotifyToolDone 被调用 2 次（每个工具各一次），实际 %d 次",
			len(sess.callsOf("NotifyToolDone")))
	}
}

// TestHandleMessage_EmptyReply_UsesFallback 验证 Action 阶段文本为空时，
// SendReply 收到兜底文本（"✅ 任务完成" 或 thinking 文本），而不是空字符串。
func TestHandleMessage_EmptyReply_UsesFallback(t *testing.T) {
	// Provider 返回空 Content 且无 ToolCalls，模拟模型静默完成
	p := &seqProvider{
		responses: []*schema.Message{
			{Role: schema.RoleAssistant, Content: ""},
		},
	}
	reg := &staticReg{}
	sess := newMockSession()
	ch := &sessionCapturingChannel{session: sess}
	srv := NewServer(ch, buildEngine(p, reg))

	runHandleMessage(srv, imchannel.IncomingMessage{ChatID: "c1", Text: "silent task"})

	replyCalls := sess.callsOf("SendReply")
	if len(replyCalls) == 0 {
		t.Fatal("期望 SendReply 被调用一次")
	}
	// 兜底文本不应为空（session.SendReply 内部将空字符串替换为 "✅ 任务完成"）
	got := replyCalls[0].args[0].(string)
	if got == "" {
		t.Error("SendReply 不应收到空字符串，期望兜底文本")
	}
}
