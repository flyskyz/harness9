package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/schema"
)

// eventMsg 将 engine.Event 包装为 tea.Msg，供 Bubbletea 的 Update 分发。
type eventMsg engine.Event

// readNextEvent 返回一个 tea.Cmd，该 Cmd 阻塞直到 ch 中有一个 Event，
// 然后以 eventMsg 形式递交给 Update。ch 关闭时递交 EventDone。
func readNextEvent(ch <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return eventMsg{Type: engine.EventDone}
		}
		return eventMsg(evt)
	}
}

// Update 实现 tea.Model——处理所有消息。
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.MouseMsg:
		// 鼠标滚轮滚动（需 tea.WithMouseCellMotion() 启用）
		if msg.Action == tea.MouseActionPress {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m = m.scrollBy(-3)
			case tea.MouseButtonWheelDown:
				m = m.scrollBy(3)
			}
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			if m.running {
				m.cancelFn()
				return m, nil
			}
			return m, tea.Quit
		case tea.KeyPgUp, tea.KeyCtrlUp:
			scrollH := m.scrollHeight()
			m = m.scrollBy(-(scrollH / 2))
			return m, nil
		case tea.KeyPgDown, tea.KeyCtrlDown:
			scrollH := m.scrollHeight()
			m = m.scrollBy(scrollH / 2)
			return m, nil
		case tea.KeyEnd:
			m.viewTop = -1
			return m, nil
		case tea.KeyTab:
			if !m.running {
				m = m.cycleCompletion()
				m.completionHint = m.buildCompletionHint()
			}
			return m, nil
		case tea.KeyEnter:
			if m.running {
				return m, nil
			}
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				return m, nil
			}
			m.phase = phaseChat
			m.input.Reset()
			// 清除补全状态
			m.typedPrefix = ""
			m.completions = nil
			m.completionHint = ""

			// 显示用户消息
			m.lines = append(m.lines, userMsgStyle.Render("▶ You: ")+raw)

			// 处理斜杠命令 / 普通输入
			prompt, ok := resolvePrompt(raw, m.skillsIndex)
			if !ok {
				name := strings.TrimPrefix(strings.SplitN(raw, " ", 2)[0], "/")
				m.lines = append(m.lines, errorStyle.Render("  ✗ 技能未找到: "+name))
				m.input.Focus()
				return m, textinput.Blink
			}
			if strings.HasPrefix(raw, "/") && m.skillsIndex != nil {
				name := strings.TrimPrefix(strings.SplitN(raw, " ", 2)[0], "/")
				m.lines = append(m.lines, skillStyle.Render("  ◎ 技能已加载: "+name))
			}

			// 开启 assistant 回复区域
			m.lines = append(m.lines, assistantStyle.Render("◆ harness9:"), "")
			m.pendingReplyStart = len(m.lines) - 1 // 指向末尾的空字符串
			m.pendingReply = ""

			ctx, cancel := context.WithCancel(m.outerCtx)
			m.cancelFn = cancel
			m.running = true
			if m.eng == nil {
				m.input.Focus()
				return m, textinput.Blink
			}
			m.input.Blur()
			ch, err := m.eng.RunStream(ctx, prompt)
			if err != nil {
				m.lines = append(m.lines, errorStyle.Render("❌ "+err.Error()))
				m.running = false
				cancel()
				m.input.Focus()
				return m, textinput.Blink
			}
			m.eventCh = ch
			return m, readNextEvent(ch)
		}

	case eventMsg:
		return m.handleEvent(engine.Event(msg))

	case spinner.TickMsg:
		if m.running && m.currentTool != "" {
			m.tickCount++
			if m.tickCount%30 == 0 {
				m.verbIdx = (m.verbIdx + 1) % len(spinnerVerbs)
			}
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	if !m.running {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		// 非 Tab 的按键重置补全循环，重新计算提示
		if _, isKey := msg.(tea.KeyMsg); isKey {
			m.typedPrefix = ""
			m.completions = nil
		}
		m.completionHint = m.buildCompletionHint()
		return m, cmd
	}
	return m, nil
}

// handleEvent 处理单个 engine.Event，返回更新后的模型和下一个 tea.Cmd。
func (m tuiModel) handleEvent(evt engine.Event) (tea.Model, tea.Cmd) {
	switch evt.Type {
	case engine.EventActionDelta:
		delta, _ := evt.Data.(string)
		m.pendingReply += delta
		// 原始文本回写到 lines，等工具边界时用 glamour 统一渲染
		rawLines := strings.Split(m.pendingReply, "\n")
		m.lines = append(m.lines[:m.pendingReplyStart], rawLines...)
		return m, readNextEvent(m.eventCh)

	case engine.EventToolStart:
		// 工具启动前先渲染当前累积的文本块
		m = m.flushPendingReply()
		tc, _ := evt.Data.(schema.ToolCall)
		m.currentTool = tc.Name
		m.toolStart = time.Now()
		m.toolArgs = tc.Arguments
		return m, tea.Batch(readNextEvent(m.eventCh), tea.Cmd(m.spinner.Tick))

	case engine.EventToolResult:
		result, _ := evt.Data.(schema.ToolResult)
		elapsed := time.Since(m.toolStart).Round(time.Millisecond)
		var line string
		if result.IsError {
			line = toolErrStyle.Render(fmt.Sprintf("  ✗ %s", m.currentTool)) + dimStyle.Render(fmt.Sprintf(" — %s", elapsed))
		} else {
			line = toolOKStyle.Render(fmt.Sprintf("  ✓ %s", m.currentTool)) + dimStyle.Render(fmt.Sprintf(" — %s", elapsed))
		}
		m.lines = append(m.lines, line)
		m.pendingReplyStart = len(m.lines) // 下一个回复文本块从这里开始
		m.currentTool = ""
		m.toolArgs = nil
		return m, readNextEvent(m.eventCh)

	case engine.EventDone:
		m = m.flushPendingReply()
		if m.cancelFn != nil {
			m.cancelFn()
		}
		m.running = false
		m.currentTool = ""
		m.toolArgs = nil
		// 纯工具执行无文字回复时，补充完成标记
		if len(m.lines) > 0 && m.lines[len(m.lines)-1] == "" {
			m.lines[len(m.lines)-1] = doneStyle.Render("  ✅ 任务完成")
		}
		m.input.Focus()
		return m, textinput.Blink

	case engine.EventError:
		errMsg, _ := evt.Data.(string)
		// 丢弃未渲染的原始流式文本
		m.lines = m.lines[:m.pendingReplyStart]
		m.pendingReply = ""
		if m.cancelFn != nil {
			m.cancelFn()
		}
		m.running = false
		m.currentTool = ""
		m.lines = append(m.lines, errorStyle.Render("❌ "+errMsg))
		m.input.Focus()
		return m, textinput.Blink
	}

	return m, readNextEvent(m.eventCh)
}

// scrollHeight 返回对话区域可显示的行数。
// 运行中且有活跃工具时额外保留 1 行给 ToolProgress。
func (m tuiModel) scrollHeight() int {
	reserved := 3 // StatusBar + PromptInput + Footer
	if m.running && m.currentTool != "" {
		reserved = 4 // + ToolProgress
	}
	h := m.height - reserved
	if h < 1 {
		h = 1
	}
	return h
}

// scrollBy 将视口向上（delta<0）或向下（delta>0）移动 delta 行。
// viewTop=-1 表示自动跟随底部；到达底部时自动切回 -1。
func (m tuiModel) scrollBy(delta int) tuiModel {
	scrollH := m.scrollHeight()
	total := len(m.lines)
	if total <= scrollH {
		return m // 内容不足一屏，无需滚动
	}
	if m.viewTop < 0 {
		// 从自动模式进入手动模式：以当前底部位置为起点
		m.viewTop = total - scrollH
	}
	m.viewTop += delta
	if m.viewTop <= 0 {
		m.viewTop = 0
	}
	if m.viewTop >= total-scrollH {
		m.viewTop = -1 // 回到底部自动模式
	}
	return m
}

// cycleCompletion 处理 Tab 键：首次进入补全模式，或在匹配列表中循环切换。
func (m tuiModel) cycleCompletion() tuiModel {
	raw := m.input.Value()
	if !strings.HasPrefix(raw, "/") || m.skillsIndex == nil {
		return m
	}
	prefix := strings.TrimPrefix(strings.SplitN(raw, " ", 2)[0], "/")

	if m.typedPrefix == "" {
		// 首次按 Tab：以当前输入作为前缀，初始化补全列表
		var matches []string
		for _, n := range m.skillsIndex.Names() {
			if strings.HasPrefix(n, prefix) {
				matches = append(matches, n)
			}
		}
		if len(matches) == 0 {
			return m
		}
		m.typedPrefix = prefix
		m.completions = matches
		m.completionIdx = 0
	} else if len(m.completions) > 0 {
		// 已在补全模式：循环到下一个
		m.completionIdx = (m.completionIdx + 1) % len(m.completions)
	}

	if len(m.completions) > 0 {
		m.input.SetValue("/" + m.completions[m.completionIdx])
		m.input.CursorEnd()
	}
	return m
}

// buildCompletionHint 根据当前输入生成状态栏的补全提示文字。
// 空输入或非斜杠命令时返回 ""。
func (m tuiModel) buildCompletionHint() string {
	raw := m.input.Value()
	if !strings.HasPrefix(raw, "/") || m.skillsIndex == nil {
		return ""
	}
	prefix := strings.TrimPrefix(strings.SplitN(raw, " ", 2)[0], "/")

	// 正在补全循环中：展示已缓存列表；否则实时计算匹配
	var names []string
	if m.typedPrefix != "" && len(m.completions) > 0 {
		names = m.completions
	} else {
		for _, n := range m.skillsIndex.Names() {
			if strings.HasPrefix(n, prefix) {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		return ""
	}

	parts := make([]string, len(names))
	for i, n := range names {
		if m.typedPrefix != "" && i == m.completionIdx {
			parts[i] = skillStyle.Render("/" + n) // 当前选中项高亮
		} else {
			parts[i] = dimStyle.Render("/" + n)
		}
	}
	return "  ↹  " + strings.Join(parts, "   ")
}

// flushPendingReply 将 pendingReply 用 glamour 渲染并替换 lines 中的原始文本。
func (m tuiModel) flushPendingReply() tuiModel {
	if m.pendingReply == "" {
		return m
	}
	if m.pendingReplyStart > len(m.lines) {
		m.pendingReplyStart = len(m.lines)
	}
	rendered := renderMD(m.pendingReply, m.width)
	lines := splitLines(rendered)
	m.lines = append(m.lines[:m.pendingReplyStart], lines...)
	m.pendingReply = ""
	m.pendingReplyStart = len(m.lines)
	return m
}

// renderMD 通过 glamour 将 Markdown 文本渲染为终端 ANSI 格式。
// 降级策略：任何错误均原样返回原文。
//
// 故意不使用 glamour.WithAutoStyle()：该选项会发送 OSC 11 终端颜色查询，
// 终端将响应写回 stdin，Bubbletea 的 textinput 会将其误判为用户输入，
// 导致输入框出现乱码（如 ]11;rgb:.../[35;1R）。改用固定 "dark" 样式规避此问题。
func renderMD(text string, width int) string {
	if width <= 4 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width-4),
	)
	if err != nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	return strings.TrimRight(out, "\n")
}

// splitLines 按换行符分割，去除末尾空行后返回切片。
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// summarizeTool 根据工具名对参数进行智能截断摘要，用于 ToolProgress 展示。
func summarizeTool(name string, args json.RawMessage) string {
	switch name {
	case "bash":
		var v struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &v); err != nil || v.Command == "" {
			return ""
		}
		cmd := strings.ReplaceAll(v.Command, "\n", " ↵ ")
		if len([]rune(cmd)) > 120 {
			return string([]rune(cmd)[:120]) + "…"
		}
		return cmd
	case "read_file", "write_file", "edit_file":
		var v struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &v); err != nil || v.Path == "" {
			return ""
		}
		return filepath.Base(v.Path)
	default:
		if len(args) == 0 {
			return ""
		}
		s := string(args)
		runes := []rune(s)
		if len(runes) > 80 {
			return string(runes[:80]) + "…"
		}
		return s
	}
}
