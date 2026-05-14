package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/schema"
)

// newTestModel 返回适合单元测试的 tuiModel（nil engine/skills，固定尺寸）。
func newTestModel() tuiModel {
	m := newTUIModel(nil, nil, "/tmp/test", "test-model")
	m.width = 80
	m.height = 24
	return m
}

// applyUpdate 调用 m.Update(msg)，返回更新后的 tuiModel。
// 丢弃返回的 tea.Cmd（单元测试中不执行 Cmd）。
func applyUpdate(m tuiModel, msg tea.Msg) tuiModel {
	updated, _ := m.Update(msg)
	return updated.(tuiModel)
}

func TestEventActionDelta_AppendsToLastLine(t *testing.T) {
	m := newTestModel()
	m.lines = []string{""}
	m.running = true

	m = applyUpdate(m, eventMsg{Type: engine.EventActionDelta, Data: "hello"})
	if got := m.lines[len(m.lines)-1]; got != "hello" {
		t.Errorf("first delta: got %q, want %q", got, "hello")
	}

	m = applyUpdate(m, eventMsg{Type: engine.EventActionDelta, Data: " world"})
	if got := m.lines[len(m.lines)-1]; got != "hello world" {
		t.Errorf("second delta: got %q, want %q", got, "hello world")
	}
}

func TestEventActionDelta_InitializesEmptyLines(t *testing.T) {
	m := newTestModel()
	m.lines = nil
	m.running = true

	m = applyUpdate(m, eventMsg{Type: engine.EventActionDelta, Data: "hi"})
	if len(m.lines) == 0 {
		t.Fatal("lines should not be empty after delta on nil slice")
	}
	if got := m.lines[len(m.lines)-1]; got != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestEventToolStart_SetsCurrentTool(t *testing.T) {
	m := newTestModel()
	m.running = true

	tc := schema.ToolCall{Name: "bash", ID: "1"}
	m = applyUpdate(m, eventMsg{Type: engine.EventToolStart, Data: tc})

	if m.currentTool != "bash" {
		t.Errorf("currentTool = %q, want %q", m.currentTool, "bash")
	}
	if m.toolStart.IsZero() {
		t.Error("toolStart should be set when tool starts")
	}
}

func TestEventToolResult_ClearsCurrentToolAndAppendsLine(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.currentTool = "bash"
	m.toolStart = time.Now().Add(-100 * time.Millisecond)
	m.lines = []string{}

	result := schema.ToolResult{Output: "ok", IsError: false}
	m = applyUpdate(m, eventMsg{Type: engine.EventToolResult, Data: result})

	if m.currentTool != "" {
		t.Errorf("currentTool should be cleared, got %q", m.currentTool)
	}
	if m.statusLine != "" {
		t.Errorf("statusLine should be cleared, got %q", m.statusLine)
	}
	if len(m.lines) == 0 {
		t.Error("completion line should be appended to scrollback")
	}
	if !strings.Contains(m.lines[len(m.lines)-1], "bash") {
		t.Errorf("completion line should mention tool name, got %q", m.lines[len(m.lines)-1])
	}
}

func TestEventToolResult_ErrorMark(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.currentTool = "bash"
	m.toolStart = time.Now()
	m.lines = []string{}

	result := schema.ToolResult{Output: "failed", IsError: true}
	m = applyUpdate(m, eventMsg{Type: engine.EventToolResult, Data: result})

	if len(m.lines) == 0 {
		t.Fatal("completion line should be appended")
	}
	if !strings.Contains(m.lines[len(m.lines)-1], "✗") {
		t.Errorf("error result should use ✗, got %q", m.lines[len(m.lines)-1])
	}
}

func TestEventDone_ResetsRunningState(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.currentTool = "bash"
	m.statusLine = "⠼ bash [1s]"
	m.lines = []string{""}
	var cancelled bool
	m.cancelFn = func() { cancelled = true }

	m = applyUpdate(m, eventMsg{Type: engine.EventDone})

	if m.running {
		t.Error("running should be false after EventDone")
	}
	if m.currentTool != "" {
		t.Errorf("currentTool should be cleared, got %q", m.currentTool)
	}
	if m.statusLine != "" {
		t.Errorf("statusLine should be cleared, got %q", m.statusLine)
	}
	if cancelled {
		t.Error("EventDone should NOT call cancelFn")
	}
}

func TestEventError_SetsStatusLineAndResetsRunning(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.currentTool = "bash"

	m = applyUpdate(m, eventMsg{Type: engine.EventError, Data: "context cancelled"})

	if m.running {
		t.Error("running should be false after EventError")
	}
	if !strings.Contains(m.statusLine, "context cancelled") {
		t.Errorf("statusLine should contain error message, got %q", m.statusLine)
	}
	if m.currentTool != "" {
		t.Errorf("currentTool should be cleared, got %q", m.currentTool)
	}
}
