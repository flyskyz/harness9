package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/skills"
)

// package-level lipgloss 样式：在 View() 外定义，避免每帧重复分配。
var (
	headerStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("237")).
			Foreground(lipgloss.Color("12")).
			Padding(0, 1)

	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("12")).
			Bold(true)

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("11")).
			Padding(0, 1)
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

// tuiModel 是 harness9 TUI 的 Bubbletea Elm 模型。
type tuiModel struct {
	// 展示配置（构造时设置，后续不变）
	workDir   string
	modelName string

	// 终端尺寸（由 WindowSizeMsg 更新）
	width, height int

	// Scrollback：所有已渲染行，仅追加
	lines []string

	// Footer 组件
	spinner    spinner.Model
	statusLine string
	input      textinput.Model

	// 当前工具跟踪（用于耗时展示）
	currentTool string
	toolStart   time.Time

	// 运行时
	eng         *engine.AgentEngine
	skillsIndex *skills.Index
	eventCh     <-chan engine.Event
	cancelFn    context.CancelFunc
	running     bool
}

// newTUIModel 构造已初始化的 tuiModel：输入框聚焦，spinner 使用 Dot 样式。
func newTUIModel(eng *engine.AgentEngine, idx *skills.Index, workDir, modelName string) tuiModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

	ti := textinput.New()
	ti.Placeholder = "输入任务..."
	ti.CharLimit = 0
	ti.Focus()

	return tuiModel{
		workDir:     workDir,
		modelName:   modelName,
		spinner:     sp,
		input:       ti,
		eng:         eng,
		skillsIndex: idx,
	}
}

// Init 实现 tea.Model，启动输入框光标闪烁。
func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update 实现 tea.Model——处理所有消息。
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			if m.running {
				m.cancelFn()
				return m, nil
			}
			return m, tea.Quit
		case tea.KeyEnter:
			if m.running {
				return m, nil
			}
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				return m, nil
			}
			m.input.Reset()
			prompt, ok := resolvePrompt(raw, m.skillsIndex)
			if !ok {
				return m, nil
			}
			m.lines = append(m.lines,
				userMsgStyle.Render("▶ You: ")+raw,
				assistantStyle.Render("◆ harness9:"),
				"",
			)
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelFn = cancel
			m.running = true
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
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			elapsed := time.Since(m.toolStart).Round(time.Millisecond)
			m.statusLine = fmt.Sprintf("%s %s  [%s]", m.spinner.View(), m.currentTool, elapsed)
			return m, cmd
		}
		return m, nil
	}

	if !m.running {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleEvent 处理单个 engine.Event，返回更新后的模型和下一个 tea.Cmd。
func (m tuiModel) handleEvent(evt engine.Event) (tea.Model, tea.Cmd) {
	switch evt.Type {
	case engine.EventActionDelta:
		delta, _ := evt.Data.(string)
		if len(m.lines) == 0 {
			m.lines = append(m.lines, "")
		}
		m.lines[len(m.lines)-1] += delta
		return m, readNextEvent(m.eventCh)

	case engine.EventToolStart:
		tc, _ := evt.Data.(schema.ToolCall)
		m.currentTool = tc.Name
		m.toolStart = time.Now()
		m.statusLine = fmt.Sprintf("  %s...", tc.Name)
		return m, tea.Batch(readNextEvent(m.eventCh), tea.Cmd(m.spinner.Tick))

	case engine.EventToolResult:
		result, _ := evt.Data.(schema.ToolResult)
		elapsed := time.Since(m.toolStart).Round(time.Millisecond)
		mark := "✓"
		if result.IsError {
			mark = "✗"
		}
		line := dimStyle.Render(fmt.Sprintf("  %s %s — %s", mark, m.currentTool, elapsed))
		m.lines = append(m.lines, line)
		m.currentTool = ""
		m.statusLine = ""
		return m, readNextEvent(m.eventCh)

	case engine.EventDone:
		m.running = false
		m.currentTool = ""
		m.statusLine = ""
		if len(m.lines) > 0 && m.lines[len(m.lines)-1] == "" {
			m.lines[len(m.lines)-1] = dimStyle.Render("✅ 任务完成")
		}
		m.input.Focus()
		return m, textinput.Blink

	case engine.EventError:
		errMsg, _ := evt.Data.(string)
		m.running = false
		m.currentTool = ""
		m.statusLine = errorStyle.Render("❌ " + errMsg)
		m.input.Focus()
		return m, textinput.Blink
	}

	return m, readNextEvent(m.eventCh)
}

// View 实现 tea.Model——渲染完整 TUI 帧。
func (m tuiModel) View() string {
	if m.width == 0 {
		return ""
	}

	const reservedLines = 3 // header + statusbar + input
	scrollH := m.height - reservedLines
	if scrollH < 1 {
		scrollH = 1
	}

	// Header：logo + 模型名 + workdir
	header := headerStyle.Width(m.width).Render(
		fmt.Sprintf("⬡ harness9   %s · %s", m.modelName, m.workDir),
	)

	// Scrollback：展示最后 scrollH 行，不足时上方填充空行
	var scrollLines []string
	if len(m.lines) >= scrollH {
		scrollLines = m.lines[len(m.lines)-scrollH:]
	} else {
		pad := make([]string, scrollH-len(m.lines))
		scrollLines = append(pad, m.lines...)
	}
	scrollContent := strings.Join(scrollLines, "\n")

	// Status Bar（1 行）
	statusBar := statusBarStyle.Width(m.width).Render(m.statusLine)

	// Input（1 行）
	inputLine := "› " + m.input.View()

	return strings.Join([]string{header, scrollContent, statusBar, inputLine}, "\n")
}

// RunTUI 以 AltScreen 模式启动 Bubbletea 程序。
// 用户按 Ctrl-C/Ctrl-D（空闲时）退出后返回。
func RunTUI(ctx context.Context, eng *engine.AgentEngine, idx *skills.Index, workDir, modelName string) error {
	_ = ctx
	m := newTUIModel(eng, idx, workDir, modelName)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
