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
	todoEnabled bool
}

// NewPromptBuilder 创建绑定到指定工作目录和 Skills Index 的 PromptBuilder。
// skillsIndex 为 nil 时，跳过 skills 段落注入。
func NewPromptBuilder(workDir string, idx *skills.Index) *DefaultPromptBuilder {
	return &DefaultPromptBuilder{workDir: workDir, skillsIndex: idx}
}

// WithTodoEnabled 在 system prompt 中添加 todo_write 工具的使用指引。
// 仅在 todo_write 已注册时调用。
func (b *DefaultPromptBuilder) WithTodoEnabled(enabled bool) *DefaultPromptBuilder {
	b.todoEnabled = enabled
	return b
}

// Build 组装并返回完整的 System Prompt 字符串。
func (b *DefaultPromptBuilder) Build() string {
	var parts []string

	// 1. 基础 prompt
	parts = append(parts, fmt.Sprintf(
		"你的名字是 harness9。请始终以 \"harness9\" 自称 — 不要使用 \"AI 助手\"、\"语言模型\" 或任何其他通称。\n\n"+
			"harness9 是一个通用 AI Agent，可完全访问用户的计算机。\n\n"+
			"能力：\n"+
			"- 执行 Shell 命令：运行程序、管理进程、安装软件包、与操作系统交互\n"+
			"- 读取、写入和编辑文件系统中的文件\n"+
			"- 将多个工具串联使用，自主完成复杂的多步骤任务\n\n"+
			"工作目录：%s\n\n"+
			"工作准则：\n"+
			"- 先调查后行动：优先读取文件并运行诊断命令\n"+
			"- 小步可验证地推进：每次重要操作后检查结果\n"+
			"- 命令失败时，诊断根本原因而非猜测\n"+
			"- 优先局部修改而非整体重写；保持现有风格和约定\n"+
			"- 任务描述模糊时，选择最合理的解释后直接推进",
		b.workDir,
	))

	// 2. AGENTS.md（不存在或为空时静默跳过）
	agentsPath := filepath.Join(b.workDir, "AGENTS.md")
	if data, err := os.ReadFile(agentsPath); err == nil && len(data) > 0 {
		parts = append(parts, "## 项目规范（AGENTS.md）\n\n"+string(data))
	}

	// 3. Skills 索引（空 Index 或 nil 时跳过整块）
	if b.skillsIndex != nil && !b.skillsIndex.IsEmpty() {
		parts = append(parts,
			"## 可用 Skills\n\n"+
				"需要时使用 `use_skill` 工具加载任意 Skill 的完整内容。\n\n"+
				b.skillsIndex.Summary(),
		)
	}

	// 4. Todo 工具使用指引（仅在 todo_write 已注册时注入）
	if b.todoEnabled {
		parts = append(parts,
			"## 任务管理\n\n"+
				"使用 `todo_write` 工具追踪复杂任务的执行进度：\n"+
				"- 任务包含 3 个或以上独立步骤时，开始前先调用此工具记录任务列表\n"+
				"- 开始某步骤时，将对应条目状态更新为 `in_progress`\n"+
				"- 完成每个步骤后，立即将状态更新为 `completed`\n"+
				"- Todo 列表在对话上下文中持久保留 — 请保持准确",
		)
	}

	return strings.Join(parts, "\n\n")
}
