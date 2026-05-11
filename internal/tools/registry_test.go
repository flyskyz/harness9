package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/harness9/internal/schema"
)

// testTool is a configurable BaseTool stub for registry tests.
type testTool struct {
	name   string
	output string
	err    error
}

func (t *testTool) Name() string { return t.name }

func (t *testTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: t.name, Description: "test tool"}
}

func (t *testTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return t.output, t.err
}

func TestNewRegistry_IsEmpty(t *testing.T) {
	r := NewRegistry()
	if defs := r.GetAvailableTools(); len(defs) != 0 {
		t.Fatalf("new registry should be empty, got %d tools", len(defs))
	}
}

func TestRegistry_Register_AddsDefinition(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&testTool{name: "mytool"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defs := r.GetAvailableTools()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(defs))
	}
	if defs[0].Name != "mytool" {
		t.Errorf("expected 'mytool', got %q", defs[0].Name)
	}
}

// TestRegistry_Register_DuplicateReturnsError 验证同名工具重复注册时：
//  1. 第二次 Register 返回 error
//  2. 原工具保持不变，未被新实现覆盖
func TestRegistry_Register_DuplicateReturnsError(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&testTool{name: "tool", output: "first"}); err != nil {
		t.Fatalf("first register should succeed: %v", err)
	}
	err := r.Register(&testTool{name: "tool", output: "second"})
	if err == nil {
		t.Fatal("expected error when registering duplicate tool name")
	}
	if !strings.Contains(err.Error(), "tool") {
		t.Errorf("error should mention conflicting name, got: %v", err)
	}

	// 原工具保持不变（first，而非 second）
	result := r.Execute(context.Background(), schema.ToolCall{
		ID: "1", Name: "tool", Arguments: json.RawMessage(`{}`),
	})
	if result.Output != "first" {
		t.Errorf("original tool should be preserved, expected 'first', got %q", result.Output)
	}
}

func TestRegistry_GetAvailableTools_MultipleTools(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"bash", "read_file", "write_file"} {
		if err := r.Register(&testTool{name: name}); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}
	if got := len(r.GetAvailableTools()); got != 3 {
		t.Fatalf("expected 3 tools, got %d", got)
	}
}

func TestRegistry_Execute_Success(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&testTool{name: "echo", output: "hello"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result := r.Execute(context.Background(), schema.ToolCall{
		ID: "call_1", Name: "echo", Arguments: json.RawMessage(`{}`),
	})

	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Output)
	}
	if result.Output != "hello" {
		t.Errorf("expected 'hello', got %q", result.Output)
	}
	if result.ToolCallID != "call_1" {
		t.Errorf("expected ToolCallID 'call_1', got %q", result.ToolCallID)
	}
}

func TestRegistry_Execute_ToolNotFound(t *testing.T) {
	r := NewRegistry()

	result := r.Execute(context.Background(), schema.ToolCall{
		ID: "call_1", Name: "ghost_tool",
	})

	if !result.IsError {
		t.Fatal("expected IsError=true for nonexistent tool")
	}
	if !strings.Contains(result.Output, "ghost_tool") {
		t.Errorf("error message should mention tool name, got: %s", result.Output)
	}
	if result.ToolCallID != "call_1" {
		t.Errorf("ToolCallID should be preserved, got %q", result.ToolCallID)
	}
}

func TestRegistry_Execute_ToolExecutionError(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&testTool{name: "broken", err: fmt.Errorf("disk full")}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result := r.Execute(context.Background(), schema.ToolCall{
		ID: "call_2", Name: "broken", Arguments: json.RawMessage(`{}`),
	})

	if !result.IsError {
		t.Fatal("expected IsError=true when tool.Execute returns error")
	}
	if !strings.Contains(result.Output, "disk full") {
		t.Errorf("error output should contain error message, got: %s", result.Output)
	}
}
