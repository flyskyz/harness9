package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/harness9/internal/memory"
	"github.com/harness9/internal/planning"
)

// accentStyle 返回当前执行模式对应的强调色样式（accent style）：
//   - PlanModeDefault → 青色（cyanStyle #81）
//   - PlanModePlan / PlanModeAutoEdit → 琥珀黄（planAccentStyle #220）
//
// 此方法被 renderStatusBar、renderFooter、renderTodoLines 统一调用，
// 确保颜色切换逻辑集中在单一位置，View 层无散落的 `if planMode ==` 判断。
func (m tuiModel) accentStyle() lipgloss.Style {
	if m.planMode != planning.PlanModeDefault {
		return planAccentStyle
	}
	return cyanStyle
}

// activeStatusBarStyle 返回当前模式下的状态栏背景样式：
//   - Default → 深灰底（statusBarStyle #235）
//   - Plan/AutoEdit → 深橙底（planStatusBarStyle #94），给用户明确的视觉信号
func (m tuiModel) activeStatusBarStyle() lipgloss.Style {
	if m.planMode != planning.PlanModeDefault {
		return planStatusBarStyle
	}
	return statusBarStyle
}

// shortPath 将绝对路径中的 $HOME 替换为 "~"。
func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return strings.Replace(p, home, "~", 1)
}

// renderTodoLines 将 TodoItem 列表渲染为结构化多行文本，追加到 Scrollback（m.lines）。
//
// 输出格式：标题行（图标 + "Tasks" + 进度统计 + 活跃任务数）+ 分隔线 + 各任务行。
// 每个任务行包含：序号、状态图标（✔/▶/○/⊘）和内容文本。
//
// 状态图标映射：
//   - in_progress → ▶（黄色，工具运行色）
//   - completed   → ✔（绿色）
//   - cancelled   → ⊘（灰色）
//   - pending     → ○（灰色）
//
// 颜色跟随当前 planMode 的 accentStyle（Plan Mode 下为琥珀色，其他为青色）。
func (m tuiModel) renderTodoLines(items []planning.TodoItem) []string {
	if len(items) == 0 {
		return nil
	}

	accent := m.accentStyle()

	// 统计完成数与进行中数
	var done, active int
	for _, item := range items {
		switch item.Status {
		case planning.TodoCompleted:
			done++
		case planning.TodoInProgress:
			active++
		}
	}

	lines := make([]string, 0, len(items)+3)

	// 标题行：图标 + "Tasks" + 进度 + 活跃数
	progress := accent.Render(fmt.Sprintf("%d/%d", done, len(items)))
	title := "  " + accent.Render("☰") + "  " +
		lipgloss.NewStyle().Bold(true).Render("Tasks") +
		dimStyle.Render("  ·  ") + progress
	if active > 0 {
		title += dimStyle.Render("  ·  ") + toolRunStyle.Render(fmt.Sprintf("%d active", active))
	}
	lines = append(lines, title)
	lines = append(lines, dimStyle.Render("  ──────────────────────────────────────"))

	// 各任务项
	for i, item := range items {
		num := dimStyle.Render(fmt.Sprintf("%2d.", i+1))
		var icon, content string
		switch item.Status {
		case planning.TodoInProgress:
			icon = toolRunStyle.Render("▶")
			content = toolRunStyle.Render(item.Content)
		case planning.TodoCompleted:
			icon = toolOKStyle.Render("✔")
			content = dimStyle.Render(item.Content)
		case planning.TodoCancelled:
			icon = dimStyle.Render("⊘")
			content = dimStyle.Render(item.Content)
		default: // pending
			icon = dimStyle.Render("○")
			content = item.Content
		}
		lines = append(lines, "  "+num+"  "+icon+"  "+content)
	}
	return lines
}

// renderConversation 渲染对话历史区（Scrollback）。
// scrollH 为可显示行数（由 scrollHeight() 计算）。
func (m tuiModel) renderConversation(scrollH int) string {
	var scrollLines []string
	if m.viewTop < 0 || len(m.lines) <= scrollH {
		if len(m.lines) >= scrollH {
			scrollLines = m.lines[len(m.lines)-scrollH:]
		} else {
			pad := make([]string, scrollH-len(m.lines))
			scrollLines = append(pad, m.lines...)
		}
	} else {
		start := m.viewTop
		end := start + scrollH
		if end > len(m.lines) {
			end = len(m.lines)
		}
		scrollLines = m.lines[start:end]
		if len(scrollLines) < scrollH {
			pad := make([]string, scrollH-len(scrollLines))
			scrollLines = append(pad, scrollLines...)
		}
	}
	return strings.Join(scrollLines, "\n")
}

// renderToolProgress 渲染工具执行进度行。
// 仅在 phaseChat && running && currentTool != "" 时调用。
func (m tuiModel) renderToolProgress() string {
	verb := spinnerVerbs[m.verbIdx]
	elapsed := time.Since(m.toolStart).Round(time.Millisecond)
	summary := summarizeTool(m.currentTool, m.toolArgs)

	var toolDisplay string
	if summary != "" {
		toolDisplay = fmt.Sprintf("%s(%s)", m.currentTool, summary)
	} else {
		toolDisplay = m.currentTool
	}

	return "  " +
		verbRunStyle.Render(m.spinner.View()+" "+verb+"...") +
		toolRunStyle.Render("  "+toolDisplay) +
		dimStyle.Render(fmt.Sprintf("  [%s]", elapsed))
}

// renderStatusBar 渲染常驻状态栏（model 名 + mode + workdir + session 信息）。
// 宽度充足时展示完整 session ID；窄终端（< 120 列）时截断为前 8 位加 "…"。
func (m tuiModel) renderStatusBar() string {
	accent := m.accentStyle()

	sessionInfo := ""
	if m.sessionID != "" {
		sid := m.sessionID
		if m.width < 120 && len(sid) > 8 {
			sid = sid[:8] + "…"
		}
		sessionInfo = dimStyle.Render("  │  session: ") + accent.Render(sid)

		if m.contextTokens > 0 {
			var tokenStr string
			if m.contextWindow > 0 {
				pct := m.contextTokens * 100 / m.contextWindow
				var tokenStyle lipgloss.Style
				switch {
				case pct >= 80:
					tokenStyle = tokenHighStyle
				case pct >= 50:
					tokenStyle = tokenWarnStyle
				default:
					tokenStyle = tokenOKStyle
				}
				tokenStr = tokenStyle.Render(
					memory.FormatTokenCount(m.contextTokens)+"/"+memory.FormatTokenCount(m.contextWindow),
				) + dimStyle.Render(fmt.Sprintf(" (%d%%)", pct))
			} else {
				tokenStr = accent.Render(memory.FormatTokenCount(m.contextTokens))
			}
			sessionInfo += dimStyle.Render("  ctx: ") + tokenStr
		}
	}
	modeLabel := m.planMode.Label()
	var modePart string
	if modeLabel != "" {
		modePart = dimStyle.Render("  │  ") + planModeLabelStyle.Render(modeLabel)
	}

	var tasksPart string
	if m.todoStore != nil {
		items := m.todoStore.Read()
		if len(items) > 0 {
			var completed int
			for _, item := range items {
				if item.Status == planning.TodoCompleted {
					completed++
				}
			}
			tasksPart = dimStyle.Render("  │  ") + accent.Render(fmt.Sprintf("%d/%d tasks", completed, len(items)))
		}
	}

	content := dimStyle.Render("  model: ") +
		accent.Render(m.modelName) +
		modePart +
		tasksPart +
		dimStyle.Render("  │  ") +
		accent.Render(shortPath(m.workDir)) +
		sessionInfo
	return m.activeStatusBarStyle().Width(m.width).Render(content)
}

// renderPlanReviewDialog 渲染 Plan Mode 完成后的审查选择对话框（带圆角边框）。
// 对话框在 planReviewing == true 时由 View() 插入到 StatusBar 之前，
// ↑↓ 移动光标，Enter 确认，Esc 取消。
func (m tuiModel) renderPlanReviewDialog() string {
	options := []string{
		"批准并自动执行",
		"批准并逐步确认编辑",
		"继续修改计划（保持 Plan Mode）",
		"取消",
	}

	var sb strings.Builder
	sb.WriteString(planModeLabelStyle.Render("Plan Mode 完成 — 选择下一步操作"))
	sb.WriteString("\n\n")
	for i, opt := range options {
		if i == m.planReviewCursor {
			sb.WriteString(planAccentStyle.Render("▶ ") + planReviewSelectedStyle.Render(opt))
		} else {
			sb.WriteString("  " + dimStyle.Render(opt))
		}
		if i < len(options)-1 {
			sb.WriteByte('\n')
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(dimStyle.Render("↑↓ 移动  Enter 确认  Esc 取消"))

	return planReviewBoxStyle.Render(sb.String())
}

// renderInput 渲染输入行。Plan Mode 下用琥珀色高亮 › 提示符。
func (m tuiModel) renderInput() string {
	if m.planMode != planning.PlanModeDefault {
		return "  " + planAccentStyle.Render("›") + " " + m.input.View()
	}
	return "  › " + m.input.View()
}

// renderFooter 渲染底部快捷键提示行。
// 优先级：补全提示 > 滚动位置提示 > 默认快捷键
func (m tuiModel) renderFooter() string {
	if m.completionHint != "" {
		return m.completionHint
	}

	accent := m.accentStyle()

	if m.viewTop >= 0 {
		scrollH := m.scrollHeight()
		maxTop := len(m.lines) - scrollH
		if maxTop < 1 {
			maxTop = 1
		}
		pct := m.viewTop * 100 / maxTop
		return "  " +
			accent.Render("enter") + dimStyle.Render(" 发送  ") +
			accent.Render("/") + dimStyle.Render(" 技能命令  ") +
			accent.Render("↑↓") + dimStyle.Render(" 滚动  ") +
			accent.Render("end") + dimStyle.Render(fmt.Sprintf(" 回底部 (%d%%)  ", pct)) +
			accent.Render("ctrl+c") + dimStyle.Render(" 退出")
	}

	return "  " +
		accent.Render("enter") + dimStyle.Render(" 发送  ") +
		accent.Render("/") + dimStyle.Render(" 技能命令  ") +
		accent.Render("↑↓") + dimStyle.Render(" 滚动  ") +
		accent.Render("ctrl+c") + dimStyle.Render(" 退出")
}

// View 实现 tea.Model——根据当前 phase 渲染完整 TUI 帧。
func (m tuiModel) View() string {
	if m.width == 0 {
		return ""
	}

	var sb strings.Builder

	if m.phase == phaseWelcome {
		sb.WriteString(bannerContent(m.width))
		sb.WriteByte('\n')
		sb.WriteString(m.renderStatusBar())
		sb.WriteByte('\n')
		sb.WriteString(m.renderInput())
		sb.WriteByte('\n')
		sb.WriteString(m.renderFooter())
	} else {
		scrollH := m.scrollHeight()
		sb.WriteString(m.renderConversation(scrollH))
		sb.WriteByte('\n')
		if m.running && m.currentTool != "" {
			sb.WriteString(m.renderToolProgress())
			sb.WriteByte('\n')
		}
		if m.planReviewing {
			sb.WriteString(m.renderPlanReviewDialog())
			sb.WriteByte('\n')
			sb.WriteString(m.renderStatusBar())
			return sb.String()
		}
		sb.WriteString(m.renderStatusBar())
		sb.WriteByte('\n')
		sb.WriteString(m.renderInput())
		sb.WriteByte('\n')
		sb.WriteString(m.renderFooter())
	}

	return sb.String()
}
