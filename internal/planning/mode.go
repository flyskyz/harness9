package planning

// PlanMode 表示 harness9 的执行模式。
type PlanMode int

const (
	PlanModeDefault  PlanMode = iota // 完整工具访问（默认）
	PlanModePlan                     // 只读：过滤写操作工具，LLM 只能探索和分析
	PlanModeAutoEdit                 // 保留：自动确认编辑（未来扩展）
)

// Next 返回 Shift+Tab 循环中的下一个模式：Default→Plan→AutoEdit→Default。
func (m PlanMode) Next() PlanMode {
	return (m + 1) % 3
}

// String 返回可读名称，用于日志。
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

// Label 返回 TUI 状态栏显示的标签。Default 模式返回空字符串（不显示）。
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
