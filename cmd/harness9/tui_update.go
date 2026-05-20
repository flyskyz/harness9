package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/memory"
	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/schema"
)

// execPrompt 是批准计划后首次触发执行的指令。
// 只包含行为引导，不声明权限（权限由 filterReadOnlyTools 在工具层硬性控制）。
const execPrompt = "按照 todo 清单逐项执行。规则：\n" +
	"1. 每开始一项前，用 todo_write 将其状态设为 in_progress\n" +
	"2. 用工具完成该项的实际工作——创建文件、写代码、运行命令等；" +
	"仅更新 todo_write 状态而不调用其他工具，不算完成该项\n" +
	"3. 确认实际产出后，用 todo_write 将其状态设为 completed\n" +
	"4. 不要输出进度摘要文字，立即处理下一项\n" +
	"全部完成后，用一句话汇报整体结果。"

// execContinuePrompt 是自动执行模式下 EventDone 后续跑的指令。
const execContinuePrompt = "继续处理 todo 清单中下一个 pending 或 in_progress 的任务项。" +
	"先用 todo_write 标记为 in_progress，然后用工具完成实际工作（写文件、执行命令等），" +
	"确认产出后标记为 completed，再处理下一项。" +
	"不要只更新状态而不做实际操作，不要输出进度摘要。"

// builtinCmds 是 TUI 内置的斜杠命令列表（不含 /），用于 Tab 补全和提示。
var builtinCmds = []struct {
	name string
	desc string
}{
	{"new", "开启新会话"},
	{"resume", "恢复历史会话"},
	{"plan", "进入规划模式分析任务"},
	{"exit", "退出 TUI"},
}

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
		// 审查对话框激活时：数字键 1-4 控制模式选择，其他键忽略
		if m.planReviewing {
			switch msg.String() {
			case "1": // 批准并自动执行
				m.planReviewing = false
				m.planMode = planning.PlanModeDefault
				m.input.Placeholder = "输入任务..."
				m.autoExecuting = true
				m.autoExecPrevDone = 0
				m.autoExecStuck = 0
				if m.eng != nil {
					m.eng.SetPlanMode(planning.PlanModeDefault)
				}
				m.lines = append(m.lines, dimStyle.Render("  ▶ 批准计划 — 自动执行中"))
				return m.dispatch(execPrompt)
			case "2": // 批准并逐步确认编辑
				m.planReviewing = false
				m.planMode = planning.PlanModeAutoEdit
				m.input.Placeholder = "输入任务..."
				m.autoExecuting = true
				m.autoExecPrevDone = 0
				m.autoExecStuck = 0
				if m.eng != nil {
					m.eng.SetPlanMode(planning.PlanModeAutoEdit)
				}
				m.lines = append(m.lines, dimStyle.Render("  ▶ 批准计划 — 逐步执行中"))
				return m.dispatch(execPrompt)
			case "3": // 继续修改计划（保持 Plan Mode）
				m.planReviewing = false
				m.input.Placeholder = "继续描述修改意见..."
				m.input.Focus()
				return m, textinput.Blink
			case "4": // 取消
				m.planReviewing = false
				m.planMode = planning.PlanModeDefault
				m.input.Placeholder = "输入任务..."
				if m.eng != nil {
					m.eng.SetPlanMode(planning.PlanModeDefault)
				}
				m.lines = append(m.lines, dimStyle.Render("  ✗ 已取消计划执行"))
				m.input.Focus()
				return m, textinput.Blink
			}
			return m, nil
		}
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			if m.running {
				m.autoExecuting = false
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
		case tea.KeyShiftTab:
			if !m.running {
				m.planMode = m.planMode.Next()
				if m.eng != nil {
					m.eng.SetPlanMode(m.planMode)
				}
			}
			return m, nil
		case tea.KeyEnter:
			if m.running {
				return m, nil
			}
			raw := strings.TrimSpace(m.input.Value())
			if m.resumeSelecting {
				return m.handleResumeSelection(raw)
			}
			if raw == "" {
				return m, nil
			}
			m.phase = phaseChat
			m.input.Reset()
			// 清除补全状态
			m.typedPrefix = ""
			m.completions = nil
			m.completionHint = ""

			// /exit 静默退出，不追加用户消息行
			if raw == "/exit" {
				return m, tea.Quit
			}

			// 显示用户消息
			m.lines = append(m.lines, userMsgStyle.Render("▶ You: ")+raw)

			// 处理其他内置命令
			if raw == "/new" {
				return m.handleNewSession()
			}
			if raw == "/resume" {
				return m.handleResumeList()
			}

			// /plan <task> — 进入 Plan Mode 并发送任务
			// 仅 "/plan"（无任务描述）时：激活 Plan Mode 并提示用户输入任务，不发送请求。
			if raw == "/plan" || strings.HasPrefix(raw, "/plan ") {
				task := strings.TrimSpace(strings.TrimPrefix(raw, "/plan"))
				m.planMode = planning.PlanModePlan
				if m.eng != nil {
					m.eng.SetPlanMode(planning.PlanModePlan)
				}
				if task == "" {
					m.lines = append(m.lines, dimStyle.Render("  [PLAN] 已进入规划模式 — 请输入要规划的任务"))
					m.input.Placeholder = "描述要规划的任务..."
					m.input.Reset()
					m.input.Focus()
					return m, textinput.Blink
				}
				raw = task
			}

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

			return m.dispatch(prompt)
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

		// 工具完成行：展示 tool_name(args摘要) — 耗时
		summary := summarizeTool(m.currentTool, m.toolArgs)
		display := m.currentTool
		if summary != "" {
			display = fmt.Sprintf("%s(%s)", m.currentTool, summary)
		}
		var line string
		if result.IsError {
			line = toolErrStyle.Render(fmt.Sprintf("  ✗ %s", display)) + dimStyle.Render(fmt.Sprintf(" — %s", elapsed))
		} else {
			line = toolOKStyle.Render(fmt.Sprintf("  ✓ %s", display)) + dimStyle.Render(fmt.Sprintf(" — %s", elapsed))
		}
		m.lines = append(m.lines, line)

		// todo_write 完成后，在工具行下方追加最新 todo 快照
		if m.currentTool == "todo_write" && !result.IsError && m.todoStore != nil {
			m = m.updateTodoBlock()
		}

		m.pendingReplyStart = len(m.lines)
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
		// Plan Mode 完成后展示审查对话框
		if m.planMode == planning.PlanModePlan {
			m.planReviewing = true
			return m, nil
		}
		// 自动执行模式：若仍有未完成 todo，检测进度后决定是否续跑
		if m.autoExecuting && m.todoStore != nil {
			items := m.todoStore.Read()
			var pending, done int
			for _, item := range items {
				switch item.Status {
				case planning.TodoPending, planning.TodoInProgress:
					pending++
				case planning.TodoCompleted:
					done++
				}
			}
			if pending > 0 {
				if done > m.autoExecPrevDone {
					// 有进度：重置停滞计数，继续执行
					m.autoExecStuck = 0
				} else {
					m.autoExecStuck++
				}
				if m.autoExecStuck < 3 {
					m.autoExecPrevDone = done
					return m.dispatch(execContinuePrompt)
				}
				// 连续 3 次无进度，放弃自动执行
				m.autoExecuting = false
				m.autoExecStuck = 0
				m.lines = append(m.lines, dimStyle.Render("  ⚠ 执行停滞，请手动描述下一步"))
			} else {
				// 所有 todo 已完成
				m.autoExecuting = false
			}
		}
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
		m.autoExecuting = false
		m.lines = append(m.lines, errorStyle.Render("❌ "+errMsg))
		m.input.Focus()
		return m, textinput.Blink

	case engine.EventTokenUpdate:
		data, _ := evt.Data.(engine.TokenUpdateData)
		m.contextTokens = data.EstimatedTokens
		if m.contextWindow == 0 && data.ContextWindow > 0 {
			m.contextWindow = data.ContextWindow
		}
		return m, readNextEvent(m.eventCh)

	case engine.EventCompaction:
		data, _ := evt.Data.(engine.CompactionData)
		line := dimStyle.Render(fmt.Sprintf("  ⚡ 上下文已压缩 — %s → %s tokens（%d → %d 条消息）",
			memory.FormatTokenCount(data.TokensBefore),
			memory.FormatTokenCount(data.TokensAfter),
			data.MsgsBefore,
			data.MsgsAfter,
		))
		m.lines = append(m.lines, line)
		return m, readNextEvent(m.eventCh)
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
// 补全顺序：内置命令优先，其次 Skills。
func (m tuiModel) cycleCompletion() tuiModel {
	raw := m.input.Value()
	if !strings.HasPrefix(raw, "/") || m.resumeSelecting {
		return m
	}
	prefix := strings.TrimPrefix(strings.SplitN(raw, " ", 2)[0], "/")

	if m.typedPrefix == "" {
		// 首次按 Tab：以当前输入作为前缀，初始化补全列表
		var matches []string
		for _, cmd := range builtinCmds {
			if strings.HasPrefix(cmd.name, prefix) {
				matches = append(matches, cmd.name)
			}
		}
		if m.skillsIndex != nil {
			for _, n := range m.skillsIndex.Names() {
				if strings.HasPrefix(n, prefix) {
					matches = append(matches, n)
				}
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
// 空输入、非斜杠命令、或处于 resume 选择模式时返回 ""。
// 内置命令附带描述，Skills 仅显示名称。
func (m tuiModel) buildCompletionHint() string {
	raw := m.input.Value()
	if !strings.HasPrefix(raw, "/") || m.resumeSelecting {
		return ""
	}
	prefix := strings.TrimPrefix(strings.SplitN(raw, " ", 2)[0], "/")

	type entry struct {
		name string
		desc string // 内置命令有描述，Skills 为空
	}

	// 正在补全循环中：从已缓存列表重建；否则实时匹配
	var entries []entry
	if m.typedPrefix != "" && len(m.completions) > 0 {
		// 用缓存列表还原 entry（desc 需重新查找）
		descOf := make(map[string]string, len(builtinCmds))
		for _, cmd := range builtinCmds {
			descOf[cmd.name] = cmd.desc
		}
		for _, n := range m.completions {
			entries = append(entries, entry{name: n, desc: descOf[n]})
		}
	} else {
		for _, cmd := range builtinCmds {
			if strings.HasPrefix(cmd.name, prefix) {
				entries = append(entries, entry{name: cmd.name, desc: cmd.desc})
			}
		}
		if m.skillsIndex != nil {
			for _, n := range m.skillsIndex.Names() {
				if strings.HasPrefix(n, prefix) {
					entries = append(entries, entry{name: n})
				}
			}
		}
	}
	if len(entries) == 0 {
		return ""
	}

	parts := make([]string, len(entries))
	for i, e := range entries {
		selected := m.typedPrefix != "" && i == m.completionIdx
		nameRendered := skillStyle.Render("/" + e.name)
		if !selected {
			nameRendered = dimStyle.Render("/" + e.name)
		}
		if e.desc != "" {
			parts[i] = nameRendered + " " + dimStyle.Render("("+e.desc+")")
		} else {
			parts[i] = nameRendered
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

// dispatch 以指定 prompt 启动一次 agent 推理流。
// 调用时 running 必须为 false；若已有推理在进行则静默返回，防止并发启动。
func (m tuiModel) dispatch(prompt string) (tuiModel, tea.Cmd) {
	if m.running {
		return m, nil
	}
	m.lines = append(m.lines, assistantStyle.Render("◆ harness9:"), "")
	m.pendingReplyStart = len(m.lines) - 1
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

// handleNewSession 创建新会话，替换引擎绑定，刷新状态栏。
func (m tuiModel) handleNewSession() (tea.Model, tea.Cmd) {
	if m.manager == nil {
		m.lines = append(m.lines, errorStyle.Render("  ✗ Memory Manager 未初始化"))
		m.input.Focus()
		return m, textinput.Blink
	}
	sess, err := m.manager.NewSession(m.outerCtx)
	if err != nil {
		m.lines = append(m.lines, errorStyle.Render("  ✗ 创建会话失败: "+err.Error()))
		m.input.Focus()
		return m, textinput.Blink
	}
	m.session = sess
	m.sessionID = sess.SessionID()
	if m.eng != nil {
		m.eng.SetSession(sess)
	}
	m.lines = append(m.lines, dimStyle.Render("  ✓ 新会话已创建: "+m.sessionID))
	m.input.Reset()
	m.input.Focus()
	return m, textinput.Blink
}

// handleResumeList 列出历史会话供用户选择。
func (m tuiModel) handleResumeList() (tea.Model, tea.Cmd) {
	if m.manager == nil {
		m.lines = append(m.lines, errorStyle.Render("  ✗ Memory Manager 未初始化"))
		m.input.Focus()
		return m, textinput.Blink
	}
	sessions, err := m.manager.ListSessions(m.outerCtx)
	if err != nil {
		m.lines = append(m.lines, errorStyle.Render("  ✗ 获取会话列表失败: "+err.Error()))
		m.input.Focus()
		return m, textinput.Blink
	}
	if len(sessions) == 0 {
		m.lines = append(m.lines, dimStyle.Render("  暂无历史会话"))
		m.input.Focus()
		return m, textinput.Blink
	}
	if len(sessions) > 10 {
		sessions = sessions[:10]
	}
	m.resumeSessions = sessions
	m.resumeSelecting = true

	m.lines = append(m.lines, dimStyle.Render(fmt.Sprintf("  可用会话（%d 条）：", len(sessions))))
	for i, s := range sessions {
		line := fmt.Sprintf("  [%d] %s  %s  %d 条消息",
			i+1,
			s.ID,
			s.UpdatedAt.Format("2006-01-02 15:04"),
			s.MsgCount,
		)
		m.lines = append(m.lines, dimStyle.Render(line))
	}
	m.lines = append(m.lines, dimStyle.Render("  输入序号选择（非数字 Enter 取消）："))

	m.input.Reset()
	m.input.Placeholder = "序号..."
	m.input.Focus()
	return m, textinput.Blink
}

// handleResumeSelection 处理用户在 resume 选择模式下的输入。
func (m tuiModel) handleResumeSelection(raw string) (tea.Model, tea.Cmd) {
	savedSessions := m.resumeSessions // 先保存，下面清空 m.resumeSessions
	m.resumeSelecting = false
	m.resumeSessions = nil
	m.input.Placeholder = "输入任务..."
	m.input.Reset()

	num, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || num < 1 || num > len(savedSessions) {
		m.lines = append(m.lines, dimStyle.Render("  已取消"))
		m.input.Focus()
		return m, textinput.Blink
	}

	info := savedSessions[num-1]
	sess, err := m.manager.OpenSession(m.outerCtx, info.ID)
	if err != nil {
		m.lines = append(m.lines, errorStyle.Render("  ✗ 加载会话失败: "+err.Error()))
		m.input.Focus()
		return m, textinput.Blink
	}
	m.session = sess
	m.sessionID = info.ID
	if m.eng != nil {
		m.eng.SetSession(sess)
	}
	m.lines = append(m.lines, dimStyle.Render(
		fmt.Sprintf("  ✓ 已切换到会话 %s（%d 条消息）", info.ID, info.MsgCount),
	))
	m.input.Focus()
	return m, textinput.Blink
}

// updateTodoBlock 在对话流末尾追加最新 todo 快照。
// 每次 todo_write 完成后调用，快照追加在工具完成行之后，呈现实时进度。
func (m tuiModel) updateTodoBlock() tuiModel {
	if m.todoStore == nil {
		return m
	}
	todoLines := m.renderTodoLines(m.todoStore.Read())
	if len(todoLines) == 0 {
		return m
	}
	m.lines = append(m.lines, todoLines...)
	return m
}
