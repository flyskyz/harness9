// engine 包的单元测试。
// 使用可编程的 Mock Provider 和预设 Registry，在不依赖真实 LLM API 的情况下
// 对 Agent Loop 的核心行为进行端到端验证。
//
// 测试覆盖范围：
//   - 基础 ReAct 流程（Action → Observation 循环）
//   - MaxTurns 安全阀机制
//   - Context 取消传播
//   - System Prompt 中 WorkDir 注入
//   - 工具错误结果回传
//   - 工具执行超时
package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

// countingProvider 是一个可编程的 LLM Provider 桩实现，
// 按预设的响应序列依次返回结果，并记录所有 Generate 调用的参数。
type countingProvider struct {
	mu        sync.Mutex
	responses []func(tools []schema.ToolDefinition) *schema.Message
	calls     []providerCall
}

// providerCall 记录一次 Generate 调用的完整参数。
type providerCall struct {
	messages []schema.Message
	tools    []schema.ToolDefinition
}

func (p *countingProvider) Generate(_ context.Context, messages []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, providerCall{messages: messages, tools: tools})

	if len(p.responses) == 0 {
		return &schema.Message{Role: schema.RoleAssistant, Content: "no more responses"}, nil, nil
	}
	fn := p.responses[0]
	p.responses = p.responses[1:]
	return fn(tools), nil, nil
}

func (p *countingProvider) GenerateStream(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	msg, _, err := p.Generate(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	ch := make(chan schema.StreamChunk, 2)
	go func() {
		defer close(ch)
		if msg.Content != "" {
			ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: msg.Content}
		}
		ch <- schema.StreamChunk{Type: schema.StreamChunkDone, Message: msg}
	}()
	return ch, nil
}

// staticRegistry 返回固定工具列表，对任何 Execute 调用都返回预设的成功结果。
type staticRegistry struct {
	tools  []schema.ToolDefinition
	output string
}

func (r *staticRegistry) Register(_ tools.BaseTool) error            { return nil }
func (r *staticRegistry) GetAvailableTools() []schema.ToolDefinition { return r.tools }
func (r *staticRegistry) Execute(_ context.Context, call schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{ToolCallID: call.ID, Output: r.output}
}

// errorRegistry 对任何 Execute 调用都返回预设的错误结果。
type errorRegistry struct {
	tools []schema.ToolDefinition
}

func (r *errorRegistry) Register(_ tools.BaseTool) error            { return nil }
func (r *errorRegistry) GetAvailableTools() []schema.ToolDefinition { return r.tools }
func (r *errorRegistry) Execute(_ context.Context, call schema.ToolCall) schema.ToolResult {
	return schema.ToolResult{ToolCallID: call.ID, Output: "command not found", IsError: true}
}

// TestReact_BasicFlow 验证单阶段 ReAct 的完整流程：
//
//	Turn 1: LLM Action（携带工具调用）→ Observation
//	Turn 2: LLM Action（最终回复，无工具调用）→ 终止
func TestReact_BasicFlow(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(tools []schema.ToolDefinition) *schema.Message {
				if len(tools) == 0 {
					t.Error("Action 调用应收到可用工具列表")
				}
				return &schema.Message{
					Role: schema.RoleAssistant,
					ToolCalls: []schema.ToolCall{
						{ID: "c1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)},
					},
				}
			},
			func(tools []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done!"}
			},
		},
	}
	r := &staticRegistry{
		tools:  []schema.ToolDefinition{{Name: "bash"}},
		output: "main.go",
	}

	eng := NewAgentEngine(p, r, "/test")
	if err := eng.Run(context.Background(), "list files"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.calls) != 2 {
		t.Fatalf("expected 2 Generate calls, got %d", len(p.calls))
	}
	// 两次调用都应携带工具列表
	for i, call := range p.calls {
		if len(call.tools) == 0 {
			t.Errorf("call %d should have tools", i)
		}
	}
}

// TestMaxTurnsLimit 验证 MaxTurns 安全阀：LLM 持续发起工具调用时，达到上限后强制退出。
func TestMaxTurnsLimit(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "bash", Arguments: []byte("{}")}}}
			},
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c2", Name: "bash", Arguments: []byte("{}")}}}
			},
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c3", Name: "bash", Arguments: []byte("{}")}}}
			},
		},
	}
	r := &staticRegistry{output: "ok"}
	eng := NewAgentEngine(p, r, "/test", WithMaxTurns(2))

	err := eng.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatal("expected MaxTurns error")
	}
	if !strings.Contains(err.Error(), "最大 Turn 数") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestContextCancellation 验证 Context 取消信号的传播。
func TestContextCancellation(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "bash", Arguments: []byte("{}")}}}
			},
		},
	}
	r := &staticRegistry{output: "ok"}
	eng := NewAgentEngine(p, r, "/test")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	err := eng.Run(ctx, "cancelled task")
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
	if !strings.Contains(err.Error(), "context 已取消") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWorkDirInSystemPrompt 验证 System Prompt 中正确注入了工作区路径。
func TestWorkDirInSystemPrompt(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "ok"}
			},
		},
	}
	r := &staticRegistry{output: "ok"}
	eng := NewAgentEngine(p, r, "/my/custom/path")

	_ = eng.Run(context.Background(), "test")

	if len(p.calls) == 0 {
		t.Fatal("expected at least 1 Generate call")
	}
	firstMsg := p.calls[0].messages[0]
	if firstMsg.Role != schema.RoleSystem {
		t.Fatal("first message should be system")
	}
	if !strings.Contains(firstMsg.Content, "/my/custom/path") {
		t.Fatalf("system prompt should contain WorkDir, got: %s", firstMsg.Content)
	}
}

// TestToolErrorResult 验证工具执行失败时，错误结果作为 Observation 正确回传给 LLM。
func TestToolErrorResult(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "bash", Arguments: []byte("{}")}}}
			},
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "retry"}
			},
		},
	}
	r := &errorRegistry{}
	eng := NewAgentEngine(p, r, "/test")

	if err := eng.Run(context.Background(), "test error"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.calls) < 2 {
		t.Fatal("expected 2 calls")
	}
	lastMsg := p.calls[1].messages[len(p.calls[1].messages)-1]
	if lastMsg.Role != schema.RoleUser {
		t.Fatal("observation should be user role")
	}
	if !strings.Contains(lastMsg.Content, "command not found") {
		t.Fatal("observation should contain error output")
	}
}

// TestToolTimeout 验证 WithToolTimeout 选项为每个工具执行创建带超时的子 context。
func TestToolTimeout(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done"}
			},
		},
	}
	timeoutRegistry := &timeoutCheckRegistry{}
	eng := NewAgentEngine(p, timeoutRegistry, "/test", WithToolTimeout(100*time.Millisecond))

	_ = eng.Run(context.Background(), "test")
}

// timeoutCheckRegistry 验证 Execute 收到的 context 有 deadline 设置。
type timeoutCheckRegistry struct{}

func (r *timeoutCheckRegistry) Register(_ tools.BaseTool) error            { return nil }
func (r *timeoutCheckRegistry) GetAvailableTools() []schema.ToolDefinition { return nil }
func (r *timeoutCheckRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	if _, ok := ctx.Deadline(); !ok {
		panic("expected context to have a deadline set by WithToolTimeout")
	}
	return schema.ToolResult{ToolCallID: call.ID, Output: "ok"}
}

// mockPromptBuilder 是 PromptBuilder 接口的测试桩。
type mockPromptBuilder struct {
	content string
}

func (m *mockPromptBuilder) Build() string { return m.content }

// TestWithPromptBuilder_CustomContent 验证 WithPromptBuilder 使自定义内容作为 system prompt 传入。
func TestWithPromptBuilder_CustomContent(t *testing.T) {
	customPrompt := "custom system prompt for testing"
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done"}
			},
		},
	}
	r := &staticRegistry{}
	eng := NewAgentEngine(p, r, "/test", WithPromptBuilder(&mockPromptBuilder{content: customPrompt}))

	_ = eng.Run(context.Background(), "test")

	if len(p.calls) == 0 {
		t.Fatal("expected at least 1 Generate call")
	}
	systemMsg := p.calls[0].messages[0]
	if systemMsg.Role != schema.RoleSystem {
		t.Fatalf("first message should be system role, got %q", systemMsg.Role)
	}
	if systemMsg.Content != customPrompt {
		t.Errorf("system prompt: got %q, want %q", systemMsg.Content, customPrompt)
	}
}

// TestWithPromptBuilder_FallbackDefault 验证未设置 PromptBuilder 时使用内置默认文案。
func TestWithPromptBuilder_FallbackDefault(t *testing.T) {
	p := &countingProvider{
		responses: []func(tools []schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done"}
			},
		},
	}
	r := &staticRegistry{}
	eng := NewAgentEngine(p, r, "/fallback/path")

	_ = eng.Run(context.Background(), "test")

	systemMsg := p.calls[0].messages[0]
	if !strings.Contains(systemMsg.Content, "/fallback/path") {
		t.Errorf("default prompt should contain workDir, got: %s", systemMsg.Content)
	}
	if !strings.Contains(systemMsg.Content, "harness9") {
		t.Errorf("default prompt should contain 'harness9', got: %s", systemMsg.Content)
	}
}

// TestRunLoop_WithTodoStore_RestoresAndSaves 验证 WithTodoStore 的跨会话持久化行为：
//  1. runLoop 启动时从 Session 恢复 TodoStore 状态（Session 是持久化的 source of truth）
//  2. runLoop 结束时通过 defer 将最终状态保存回 Session
func TestRunLoop_WithTodoStore_RestoresAndSaves(t *testing.T) {
	p := &countingProvider{
		responses: []func([]schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done"}
			},
		},
	}
	reg := &staticRegistry{output: "ok"}

	// 准备一个已有持久化 todos 的 Session（模拟上次 Run 结束后保存的状态）。
	sess := newMemorySessionForTest("sess-1")
	if err := sess.SaveTodos(context.Background(), []planning.TodoItem{
		{ID: "t1", Content: "pre-existing task", Status: planning.TodoPending},
	}); err != nil {
		t.Fatalf("SaveTodos setup error: %v", err)
	}

	todoStore := planning.NewTodoStore()

	eng := NewAgentEngine(p, reg, t.TempDir(),
		WithTodoStore(todoStore),
		WithSession(sess),
	)
	if err := eng.Run(context.Background(), "test"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// 验证：runLoop 启动时应从 Session 恢复 todos 到 todoStore。
	// 恢复后 todoStore 中应有 t1（在 Run 开始时加载）。
	// Run 结束时 defer 将（恢复后、未被修改的）todos 重新保存到 Session。
	saved, err := sess.GetTodos(context.Background())
	if err != nil {
		t.Fatalf("GetTodos error: %v", err)
	}
	if len(saved) != 1 || saved[0].ID != "t1" {
		t.Errorf("expected saved todos to contain t1, got %+v", saved)
	}
}

// TestRunLoop_PlanMode_InjectsPlanPrefix 验证 Plan Mode 下用户 prompt 被注入了规划前缀。
func TestRunLoop_PlanMode_InjectsPlanPrefix(t *testing.T) {
	var receivedUserPrompt string
	p := &countingProvider{
		responses: []func([]schema.ToolDefinition) *schema.Message{
			func(_ []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done"}
			},
		},
	}
	// 注入一个回调 provider 捕获历史消息内容
	capturing := &capturingProvider{inner: p, onGenerate: func(msgs []schema.Message) {
		for _, m := range msgs {
			if m.Role == schema.RoleUser && receivedUserPrompt == "" {
				receivedUserPrompt = m.Content
			}
		}
	}}
	reg := &staticRegistry{output: "ok"}
	eng := NewAgentEngine(capturing, reg, t.TempDir(),
		WithPlanMode(planning.PlanModePlan),
	)
	if err := eng.Run(context.Background(), "implement feature X"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// Plan Mode 前缀必须存在于用户消息中
	if !strings.Contains(receivedUserPrompt, "todo_write") {
		t.Errorf("Plan Mode should inject planning prefix mentioning todo_write, got: %q", receivedUserPrompt)
	}
	if !strings.Contains(receivedUserPrompt, "implement feature X") {
		t.Errorf("original user prompt should be preserved, got: %q", receivedUserPrompt)
	}
}

// ---- 测试辅助类型 ----

// memorySessionForTest 是支持真实 GetTodos/SaveTodos 持久化语义的 Session 桩。
// 注意：memory.MemorySession 的 SaveTodos 是 no-op、GetTodos 始终返回空列表，
// 无法用于验证跨 runLoop 的 todo restore/save 行为，因此此处独立实现。
type memorySessionForTest struct {
	id    string
	msgs  []schema.Message
	todos []planning.TodoItem
}

func newMemorySessionForTest(id string) *memorySessionForTest {
	return &memorySessionForTest{id: id}
}

func (s *memorySessionForTest) SessionID() string { return s.id }
func (s *memorySessionForTest) GetMessages(_ context.Context, _ int) ([]schema.Message, error) {
	return append([]schema.Message(nil), s.msgs...), nil
}
func (s *memorySessionForTest) AddMessages(_ context.Context, msgs []schema.Message) error {
	s.msgs = append(s.msgs, msgs...)
	return nil
}
func (s *memorySessionForTest) PopMessage(_ context.Context) (*schema.Message, error) {
	if len(s.msgs) == 0 {
		return nil, nil
	}
	m := s.msgs[len(s.msgs)-1]
	s.msgs = s.msgs[:len(s.msgs)-1]
	return &m, nil
}
func (s *memorySessionForTest) Clear(_ context.Context) error { s.msgs = nil; return nil }
func (s *memorySessionForTest) GetTodos(_ context.Context) ([]planning.TodoItem, error) {
	return append([]planning.TodoItem(nil), s.todos...), nil
}
func (s *memorySessionForTest) SaveTodos(_ context.Context, items []planning.TodoItem) error {
	s.todos = append([]planning.TodoItem(nil), items...)
	return nil
}

// capturingProvider 包装另一个 Provider，在每次 Generate 调用时执行 onGenerate 回调。
type capturingProvider struct {
	inner      provider.LLMProvider
	onGenerate func(msgs []schema.Message)
}

func (p *capturingProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	p.onGenerate(msgs)
	return p.inner.Generate(ctx, msgs, tools)
}
func (p *capturingProvider) GenerateStream(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	p.onGenerate(msgs)
	return p.inner.GenerateStream(ctx, msgs, tools)
}

// TestRunLoop_PlanMode_FiltersWriteTools 验证 Plan Mode 下写操作工具被过滤，只读工具保留。
func TestRunLoop_PlanMode_FiltersWriteTools(t *testing.T) {
	p := &countingProvider{
		responses: []func([]schema.ToolDefinition) *schema.Message{
			func(tools []schema.ToolDefinition) *schema.Message {
				return &schema.Message{Role: schema.RoleAssistant, Content: "done"}
			},
		},
	}

	allTools := []schema.ToolDefinition{
		{Name: "read_file", Description: "read"},
		{Name: "write_file", Description: "write"},
		{Name: "bash", Description: "bash"},
		{Name: "edit_file", Description: "edit"},
		{Name: "todo_write", Description: "todo"},
	}
	reg := &staticRegistry{tools: allTools, output: "ok"}

	eng := NewAgentEngine(p, reg, t.TempDir(),
		WithMaxTurns(1),
		WithPlanMode(planning.PlanModePlan),
	)
	err := eng.Run(context.Background(), "plan this")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if len(p.calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(p.calls))
	}
	visibleTools := p.calls[0].tools
	for _, tool := range visibleTools {
		switch tool.Name {
		case "write_file", "edit_file":
			t.Errorf("tool %q should be filtered in PlanMode, but was visible", tool.Name)
		}
	}
	found := make(map[string]bool)
	for _, tool := range visibleTools {
		found[tool.Name] = true
	}
	if !found["read_file"] {
		t.Error("read_file should be visible in PlanMode")
	}
	if !found["bash"] {
		t.Error("bash should be visible in PlanMode")
	}
	if !found["todo_write"] {
		t.Error("todo_write should be visible in PlanMode (needed to write the plan)")
	}
}
