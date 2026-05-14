package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/schema"
)

// newTestModel 返回适合单元测试的 tuiModel（nil engine/skills，固定尺寸）。
func newTestModel() tuiModel {
	m := newTUIModel(nil, nil, context.Background(), "/tmp/test", "test-model")
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
	if !cancelled {
		t.Error("EventDone should call cancelFn to release context")
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

func TestWindowSizeMsg_UpdatesDimensions(t *testing.T) {
	m := newTestModel()

	m = applyUpdate(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if m.width != 120 || m.height != 40 {
		t.Errorf("got %dx%d, want 120x40", m.width, m.height)
	}
}

func TestKeyCtrlC_WhenIdle_ReturnsQuitCmd(t *testing.T) {
	m := newTestModel()
	m.running = false

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a non-nil quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestKeyCtrlC_WhenRunning_CallsCancelFn(t *testing.T) {
	m := newTestModel()
	m.running = true
	var cancelled bool
	m.cancelFn = func() { cancelled = true }

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if !cancelled {
		t.Error("cancelFn should be called when Ctrl-C during agent run")
	}
	if !m.running {
		// running stays true until EventDone/EventError arrives from engine
		t.Error("running should remain true until engine confirms cancellation")
	}
}

func TestKeyEnter_EmptyInput_Ignored(t *testing.T) {
	m := newTestModel()
	m.running = false
	initialLines := len(m.lines)

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.lines) != initialLines {
		t.Error("empty Enter should not append to scrollback")
	}
}

func TestKeyEnter_WhenRunning_Ignored(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.input.SetValue("do something")
	initialLines := len(m.lines)

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.lines) != initialLines {
		t.Error("Enter while agent is running should be ignored")
	}
}

func TestKeyPgUp_EntersManualScroll(t *testing.T) {
	m := newTestModel()
	// 填充足够多的行触发滚动
	for i := 0; i < 30; i++ {
		m.lines = append(m.lines, "line")
	}

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyPgUp})

	if m.viewTop < 0 {
		t.Error("PgUp should enter manual scroll mode (viewTop >= 0)")
	}
}

func TestMouseWheelUp_ScrollsUp(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 30; i++ {
		m.lines = append(m.lines, "line")
	}

	m = applyUpdate(m, tea.MouseMsg{
		Button: tea.MouseButtonWheelUp,
		Action: tea.MouseActionPress,
	})

	if m.viewTop < 0 {
		t.Error("MouseWheelUp should enter manual scroll mode (viewTop >= 0)")
	}
}

func TestMouseWheelDown_AtBottom_NoChange(t *testing.T) {
	m := newTestModel()
	// viewTop=-1（底部），向下滚动不应改变状态
	m = applyUpdate(m, tea.MouseMsg{
		Button: tea.MouseButtonWheelDown,
		Action: tea.MouseActionPress,
	})

	if m.viewTop != -1 {
		t.Errorf("WheelDown at bottom should keep viewTop=-1, got %d", m.viewTop)
	}
}

func TestKeyEnd_ReturnsToAutoScroll(t *testing.T) {
	m := newTestModel()
	m.viewTop = 5 // 已在手动滚动

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnd})

	if m.viewTop != -1 {
		t.Errorf("End should reset to auto-scroll (viewTop=-1), got %d", m.viewTop)
	}
}

func TestKeyPgDown_AtBottom_StaysAutoScroll(t *testing.T) {
	m := newTestModel()
	// viewTop=-1 时按 PgDn 不应改变状态
	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyPgDown})

	if m.viewTop != -1 {
		t.Errorf("PgDn at auto-scroll bottom should keep viewTop=-1, got %d", m.viewTop)
	}
}
