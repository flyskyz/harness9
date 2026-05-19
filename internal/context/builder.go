// Package context 实现 harness9 的上下文工程（Context Engineering）模块。
//
// DefaultPromptBuilder 按 Progressive Disclosure 原则组装 Agent 的 System Prompt：
//  1. harness9 基础 prompt（角色定义 + 工作目录声明）
//  2. workdir/AGENTS.md 内容（用户项目规范，不存在时静默跳过）
//  3. Skills 索引摘要（LLM 按需通过 use_skill 工具加载全文，空时跳过）
package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness9/internal/skills"
)

// DefaultPromptBuilder 实现了 engine.PromptBuilder 接口。
// Go 通过结构类型（Structural Typing）隐式满足接口，无需显式声明或 import engine 包。
type DefaultPromptBuilder struct {
	workDir     string
	skillsIndex *skills.Index
}

// NewPromptBuilder 创建绑定到指定工作目录和 Skills Index 的 PromptBuilder。
// skillsIndex 为 nil 时，跳过 skills 段落注入。
func NewPromptBuilder(workDir string, idx *skills.Index) *DefaultPromptBuilder {
	return &DefaultPromptBuilder{workDir: workDir, skillsIndex: idx}
}

// Build 组装并返回完整的 System Prompt 字符串。
func (b *DefaultPromptBuilder) Build() string {
	var parts []string

	// 1. 基础 prompt
	parts = append(parts, fmt.Sprintf(
		"Your name is harness9. Always refer to yourself as \"harness9\" — never as \"AI assistant\", \"language model\", or any other generic term.\n\n"+
			"harness9 is a general-purpose AI agent with full access to the user's computer.\n\n"+
			"Capabilities:\n"+
			"- Run shell commands to execute programs, manage processes, install packages, and interact with the OS\n"+
			"- Read, write, and edit files across the filesystem\n"+
			"- Chain multiple tools together to complete complex, multi-step tasks autonomously\n\n"+
			"Working directory: %s\n\n"+
			"Guidelines:\n"+
			"- Investigate before acting: read files and run diagnostic commands first\n"+
			"- Work in small verifiable steps; check results after each significant action\n"+
			"- When a command fails, diagnose the root cause rather than guessing\n"+
			"- Prefer targeted edits over full rewrites; preserve existing style and conventions\n"+
			"- If a task is ambiguous, choose the most reasonable interpretation and proceed",
		b.workDir,
	))

	// 2. AGENTS.md（不存在或为空时静默跳过）
	agentsPath := filepath.Join(b.workDir, "AGENTS.md")
	if data, err := os.ReadFile(agentsPath); err == nil && len(data) > 0 {
		parts = append(parts, "## Project Guidelines (AGENTS.md)\n\n"+string(data))
	}

	// 3. Skills 索引（空 Index 或 nil 时跳过整块）
	if b.skillsIndex != nil && !b.skillsIndex.IsEmpty() {
		parts = append(parts,
			"## Available Skills\n\n"+
				"Use the `use_skill` tool to load the full content of any skill when needed.\n\n"+
				b.skillsIndex.Summary(),
		)
	}

	return strings.Join(parts, "\n\n")
}
