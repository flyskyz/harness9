package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// shortPath 将绝对路径中的 $HOME 替换为 "~"。
func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return strings.Replace(p, home, "~", 1)
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

// renderStatusBar 渲染常驻状态栏（model 名 + mode + workdir）。
func (m tuiModel) renderStatusBar() string {
	content := dimStyle.Render("  model: ") +
		cyanStyle.Render(m.modelName) +
		dimStyle.Render("  │  mode: Default  │  ") +
		cyanStyle.Render(shortPath(m.workDir))
	return statusBarStyle.Width(m.width).Render(content)
}

// renderInput 渲染输入行。
func (m tuiModel) renderInput() string {
	return "  › " + m.input.View()
}

// renderFooter 渲染底部快捷键提示行。
// 优先级：补全提示 > 滚动位置提示 > 默认快捷键
func (m tuiModel) renderFooter() string {
	if m.completionHint != "" {
		return m.completionHint
	}

	if m.viewTop >= 0 {
		scrollH := m.scrollHeight()
		maxTop := len(m.lines) - scrollH
		if maxTop < 1 {
			maxTop = 1
		}
		pct := m.viewTop * 100 / maxTop
		return "  " +
			cyanStyle.Render("enter") + dimStyle.Render(" 发送  ") +
			cyanStyle.Render("/") + dimStyle.Render(" 技能命令  ") +
			cyanStyle.Render("↑↓") + dimStyle.Render(" 滚动  ") +
			cyanStyle.Render("end") + dimStyle.Render(fmt.Sprintf(" 回底部 (%d%%)  ", pct)) +
			cyanStyle.Render("ctrl+c") + dimStyle.Render(" 退出")
	}

	return "  " +
		cyanStyle.Render("enter") + dimStyle.Render(" 发送  ") +
		cyanStyle.Render("/") + dimStyle.Render(" 技能命令  ") +
		cyanStyle.Render("↑↓") + dimStyle.Render(" 滚动  ") +
		cyanStyle.Render("ctrl+c") + dimStyle.Render(" 退出")
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
		sb.WriteString(m.renderStatusBar())
		sb.WriteByte('\n')
		sb.WriteString(m.renderInput())
		sb.WriteByte('\n')
		sb.WriteString(m.renderFooter())
	}

	return sb.String()
}
