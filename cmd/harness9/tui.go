package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/skills"
)

// package-level lipgloss 样式：在 View() 外定义，避免每帧重复分配。
var (
	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("12")).
			Bold(true)

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("11")).
			Padding(0, 1)

	// 工具执行阶段高亮
	toolRunStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))            // 黄色：运行中（工具名）
	verbRunStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))           // 黄色：spinner + 动词
	toolOKStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))            // 绿色：成功
	toolErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))             // 红色：失败
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true) // 绿色粗体：任务完成
	skillStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))            // 青色：技能激活
	cyanStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	brandStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true)
	sepStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
)

type tuiPhase int

const (
	phaseWelcome tuiPhase = iota
	phaseChat
)

var spinnerVerbs = []string{
	"思考中", "分析中", "处理中", "推理中", "计算中", "评估中",
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

	// 滚动状态：-1 表示自动跟随底部（auto-scroll），≥0 表示手动滚动到该行索引
	viewTop int

	// Footer 组件
	spinner spinner.Model
	input   textinput.Model

	// Phase 状态机
	phase tuiPhase

	// Spinner 动词轮换
	verbIdx   int // 0-5，当前动词索引
	tickCount int // TickMsg 计数，每 30 次递增 verbIdx

	// 当前工具跟踪（用于耗时展示）
	currentTool string
	toolStart   time.Time
	toolArgs    json.RawMessage

	// Markdown 流式渲染状态：
	// streaming 期间将 delta 追加到 pendingReply，
	// 在工具边界（EventToolStart / EventDone）统一调用 glamour 渲染，
	// 替换 lines[pendingReplyStart:] 中的原始文本。
	pendingReply      string
	pendingReplyStart int

	// Tab 斜杠命令补全状态
	typedPrefix    string   // 首次按 Tab 时的输入前缀（非空表示正在补全循环中）
	completions    []string // 与 typedPrefix 匹配的技能名列表
	completionIdx  int      // 当前循环位置
	completionHint string   // idle 时状态栏展示的补全提示

	// 运行时
	outerCtx    context.Context // 来自 main.go 的外部 context（SIGTERM 等信号）
	eng         *engine.AgentEngine
	skillsIndex *skills.Index
	eventCh     <-chan engine.Event
	cancelFn    context.CancelFunc
	running     bool
}

// newTUIModel 构造已初始化的 tuiModel：输入框聚焦，spinner 使用 Dot 样式。
func newTUIModel(eng *engine.AgentEngine, idx *skills.Index, outerCtx context.Context, workDir, modelName string) tuiModel {
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
		outerCtx:    outerCtx,
		eng:         eng,
		skillsIndex: idx,
		viewTop:     -1, // -1 = 自动跟随底部
		phase:       phaseWelcome,
	}
}

// Init 实现 tea.Model，启动输入框光标闪烁。
func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

// RunTUI 以 AltScreen 模式启动 Bubbletea 程序。
// 用户按 Ctrl-C/Ctrl-D（空闲时）退出后返回。
func RunTUI(ctx context.Context, eng *engine.AgentEngine, idx *skills.Index, workDir, modelName string) error {
	// TUI 独占终端，将内部日志重定向到静默，避免污染 AltScreen 输出。
	// 退出后恢复原 Writer，避免影响同进程其他逻辑（如测试框架）。
	origWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(origWriter)
	m := newTUIModel(eng, idx, ctx, workDir, modelName)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
