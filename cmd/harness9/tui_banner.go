package main

import (
	"strings"
)

// asciiArt 是用 3 行框线字符绘制的 HARNESS9 标题。
// 字符宽度：H/A/R/N/E/S/S/9 各 3 列，字符间 2 空格，共 38 字符宽。
const asciiArt = `╦ ╦  ╔╦╗  ╔═╗  ╔╗╦  ╔══  ╔══  ╔══  ╔═╗
╠═╣  ╠╩╣  ╠╦╝  ║╚╗  ╠═   ╚═╗  ╚═╗  ╚═╣
╩ ╩  ╚ ╝  ╩╗   ╩ ╩  ╚══  ══╝  ══╝    ╝`

// bannerContent 返回欢迎页的完整 Banner 内容（ASCII Art + 副标题 + 快捷键提示 + 分隔线）。
// width 为终端宽度，用于居中 ASCII Art 和计算分隔线长度。
func bannerContent(width int) string {
	// 居中 ASCII Art（以第一行的 rune 宽度为基准）
	artLines := strings.Split(asciiArt, "\n")
	artWidth := len([]rune(artLines[0]))
	padding := (width - artWidth) / 2
	if padding < 0 {
		padding = 0
	}
	pad := strings.Repeat(" ", padding)

	var centeredArt []string
	for _, line := range artLines {
		centeredArt = append(centeredArt, pad+cyanStyle.Render(line))
	}

	subtitle := "  " + brandStyle.Render("harness9") +
		dimStyle.Render("  ·  An AI-powered coding agent")

	helpLine := "  " + cyanStyle.Render("/skill") + dimStyle.Render(" 加载技能  │  ") +
		cyanStyle.Render("Tab") + dimStyle.Render(" 补全  │  ") +
		cyanStyle.Render("Ctrl+C") + dimStyle.Render(" 退出")

	w := width - 4
	if w < 10 {
		w = 10
	}
	sep := "  " + sepStyle.Render(strings.Repeat("─", w))

	parts := []string{
		"",
		strings.Join(centeredArt, "\n"),
		"",
		subtitle,
		helpLine,
		"",
		sep,
	}
	return strings.Join(parts, "\n")
}
