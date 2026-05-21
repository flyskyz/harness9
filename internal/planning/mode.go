package planning

// PlanMode 表示 harness9 的执行模式。
//
// 枚举值通过 Shift+Tab 在 TUI 中循环切换；引擎在 runLoop 入口快照当前模式，
// 确保整轮推理过程中模式不变，不受 TUI goroutine 异步切换的影响。
type PlanMode int

const (
	// PlanModeDefault 是默认执行模式：LLM 可访问全部注册工具，直接执行任意操作。
	PlanModeDefault PlanMode = iota
	// PlanModePlan 是规划模式：filterReadOnlyTools 从工具列表移除 write_file / edit_file，
	// LLM 只能通过 read_file、bash（只读命令）探索代码库，通过 todo_write 输出结构化计划。
	// 这是硬约束（工具层），而非软约束（prompt 层），确保 LLM 无法绕过限制创建文件。
	PlanModePlan
	// PlanModeAutoEdit 是保留枚举值，当前行为与 PlanModeDefault 相同。
	// 预留给未来的"逐步确认编辑"模式（用户对每次文件修改手动批准）。
	PlanModeAutoEdit
)

// Next 返回 Shift+Tab 循环中的下一个模式：Default→Plan→AutoEdit→Default。
// 通过整数取模实现三值循环，新增模式时需同步更新取模数。
func (m PlanMode) Next() PlanMode {
	return (m + 1) % 3
}

// String 返回人类可读的模式名称，主要用于调试日志输出。
func (m PlanMode) String() string {
	switch m {
	case PlanModePlan:
		return "PLAN"
	case PlanModeAutoEdit:
		return "AUTO"
	default:
		return "DEFAULT"
	}
}

// Label 返回 TUI 状态栏展示的模式标签文本。
// PlanModeDefault 返回空字符串，不在状态栏显示额外标签（默认模式无需提示）。
// 非默认模式返回带括号的标签，与状态栏其他字段通过分隔符区分。
func (m PlanMode) Label() string {
	switch m {
	case PlanModePlan:
		return "[PLAN]"
	case PlanModeAutoEdit:
		return "[AUTO (未实现)]"
	default:
		return ""
	}
}
