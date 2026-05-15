package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
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

	tc := schema.ToolCall{Name: "bash", ID: "1", Arguments: json.RawMessage(`{"command":"ls"}`)}
	m = applyUpdate(m, eventMsg{Type: engine.EventToolStart, Data: tc})

	if m.currentTool != "bash" {
		t.Errorf("currentTool = %q, want %q", m.currentTool, "bash")
	}
	if m.toolStart.IsZero() {
		t.Error("toolStart should be set when tool starts")
	}
	if m.toolArgs == nil {
		t.Error("toolArgs should be set on EventToolStart")
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
	if !cancelled {
		t.Error("EventDone should call cancelFn to release context")
	}
}

func TestEventError_AppendsToScrollbackAndResetsRunning(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.currentTool = "bash"
	m.lines = []string{"partial text"}
	m.pendingReplyStart = 0

	m = applyUpdate(m, eventMsg{Type: engine.EventError, Data: "context cancelled"})

	if m.running {
		t.Error("running should be false after EventError")
	}
	if m.currentTool != "" {
		t.Errorf("currentTool should be cleared, got %q", m.currentTool)
	}
	if len(m.lines) == 0 {
		t.Fatal("error line should be appended to scrollback")
	}
	if !strings.Contains(m.lines[len(m.lines)-1], "context cancelled") {
		t.Errorf("last line should contain error message, got %q", m.lines[len(m.lines)-1])
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

func TestSummarizeTool_Bash(t *testing.T) {
	args := json.RawMessage(`{"command":"go test ./... 2>&1 | head -20"}`)
	got := summarizeTool("bash", args)
	if got != "go test ./... 2>&1 | head -20" {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeTool_Bash_Truncates(t *testing.T) {
	long := strings.Repeat("x", 130)
	args := json.RawMessage(`{"command":"` + long + `"}`)
	got := summarizeTool("bash", args)
	if len([]rune(got)) != 121 { // 120 chars + "…"
		t.Errorf("expected 121 runes (120 + ellipsis), got %d: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
}

func TestSummarizeTool_ReadFile(t *testing.T) {
	args := json.RawMessage(`{"path":"/home/user/project/main.go"}`)
	got := summarizeTool("read_file", args)
	if got != "main.go" {
		t.Errorf("got %q, want %q", got, "main.go")
	}
}

func TestSummarizeTool_Other(t *testing.T) {
	args := json.RawMessage(`{"key":"value"}`)
	got := summarizeTool("custom_tool", args)
	if got != `{"key":"value"}` {
		t.Errorf("got %q", got)
	}
}

func TestSummarizeTool_WriteFile(t *testing.T) {
	args := json.RawMessage(`{"path":"/home/user/project/utils.go","content":"package main"}`)
	got := summarizeTool("write_file", args)
	if got != "utils.go" {
		t.Errorf("got %q, want %q", got, "utils.go")
	}
}

func TestSummarizeTool_InvalidArgs(t *testing.T) {
	args := json.RawMessage(`not-json`)
	got := summarizeTool("bash", args)
	if got != "" {
		t.Errorf("invalid args should return empty string, got %q", got)
	}
}

func TestPhaseTransition_WelcomeToChat(t *testing.T) {
	m := newTestModel()
	if m.phase != phaseWelcome {
		t.Fatal("new model should start in phaseWelcome")
	}
	m.input.SetValue("hello world")

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.phase != phaseChat {
		t.Errorf("phase should be phaseChat after first Enter, got %v", m.phase)
	}
}

func TestPhaseStaysChat_AfterFirstEnter(t *testing.T) {
	m := newTestModel()
	m.phase = phaseChat
	m.input.SetValue("second message")

	m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.phase != phaseChat {
		t.Errorf("phase should remain phaseChat, got %v", m.phase)
	}
}

func TestSpinnerVerbRotation(t *testing.T) {
	m := newTestModel()
	m.running = true
	m.currentTool = "bash"
	m.verbIdx = 0
	m.tickCount = 29

	m = applyUpdate(m, spinner.TickMsg{})

	if m.tickCount != 30 {
		t.Errorf("tickCount should be 30, got %d", m.tickCount)
	}
	if m.verbIdx != 1 {
		t.Errorf("verbIdx should advance to 1 after 30 ticks, got %d", m.verbIdx)
	}

	// verify wraparound
	m.verbIdx = 5
	m.tickCount = 59
	m = applyUpdate(m, spinner.TickMsg{})
	if m.verbIdx != 0 {
		t.Errorf("verbIdx should wrap to 0, got %d", m.verbIdx)
	}
}

func TestScrollHeight_DynamicReservedLines(t *testing.T) {
	m := newTestModel() // height = 24
	if got := m.scrollHeight(); got != 21 {
		t.Errorf("idle: want 21 (24-3), got %d", got)
	}
	m.running = true
	m.currentTool = "bash"
	if got := m.scrollHeight(); got != 20 {
		t.Errorf("running with tool: want 20 (24-4), got %d", got)
	}
}
