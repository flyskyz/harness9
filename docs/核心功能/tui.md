# TUI 交互界面实现原理

harness9 在交互式终端（TTY）下自动启动全屏 TUI 模式，使用 [Bubbletea](https://github.com/charmbracelet/bubbletea) 框架实现 Elm Architecture。

---

## 文件结构

TUI 按职责拆分为四个文件：

```
cmd/harness9/
├── tui.go          # tuiModel struct、包级样式变量、Init、RunTUI
├── tui_update.go   # Update 逻辑：事件处理、键盘、滚动、Tab 补全、Markdown 渲染
├── tui_view.go     # View 渲染：6 个子渲染器（Conversation/ToolProgress/StatusBar/Input/Footer）
├── tui_banner.go   # WelcomeBanner：HARNESS9 ASCII Art + bannerContent()
└── tui_test.go     # 45 个单元测试：直接注入 tea.Msg 验证 model 状态
```

---

## Phase 状态机

TUI 拥有两个 Phase，首次 Enter 触发从欢迎页切换到对话页：

```go
type tuiPhase int

const (
    phaseWelcome tuiPhase = iota  // 欢迎页（HARNESS9 ASCII Art）
    phaseChat                      // 对话页（Scrollback + 流式输出）
)
```

### phaseWelcome — 欢迎页布局

```
         ╦ ╦  ╔╦╗  ╔═╗  ╔╗╦  ╔══  ╔══  ╔══  ╔═╗
         ╠═╣  ╠╩╣  ╠╦╝  ║╚╗  ╠═   ╚═╗  ╚═╗  ╚═╣
         ╩ ╩  ╚ ╝  ╩╗   ╩ ╩  ╚══  ══╝  ══╝    ╝

  harness9  ·  An AI-powered coding agent
  /skill 加载技能  │  Tab 补全  │  Ctrl+C 退出
  ──────────────────────────────────────────────
  model: gpt-4o-mini  │  mode: Default  │  ~/myproject
  › 输入任务...
  enter 发送  / 技能命令  ↑↓ 滚动  ctrl+c 退出
```

### phaseChat — 对话页布局

```
  ▶ You: 帮我分析 main.go 里的 bug

  ◆ harness9:
    好的，我先读取文件...
    ✓ read_file(main.go) — 234ms
    发现第 42 行存在空指针解引用问题

  ⠼ 思考中...  bash(go test ./...)  [3.2s]    ← ToolProgress（仅运行时可见）
  model: gpt-4o-mini  │  mode: Default  │  ~/myproject  ← StatusBar
  › _                                                    ← Input
  enter 发送  / 技能命令  ↑↓ 滚动  ctrl+c 退出          ← Footer
```

| 区域 | 高度 | 职责 |
|------|------|------|
| Scrollback | 弹性（全部剩余行） | 历史消息追加输出；支持鼠标/键盘滚动 |
| ToolProgress | 1 行（仅运行中） | spinner 动词 + 工具名摘要 + 耗时 |
| StatusBar | 1 行 | model / mode / workdir 常驻信息 |
| Input | 1 行 | 单行文本输入框；Agent 运行时禁用 |
| Footer | 1 行 | 快捷键提示 / 滚动位置百分比 / Tab 补全提示 |

---

## WelcomeBanner：ASCII Art

`tui_banner.go` 中定义了三行框线字符组成的 HARNESS9 标题（字符宽度 38）：

```go
const asciiArt = `╦ ╦  ╔╦╗  ╔═╗  ╔╗╦  ╔══  ╔══  ╔══  ╔═╗
╠═╣  ╠╩╣  ╠╦╝  ║╚╗  ╠═   ╚═╗  ╚═╗  ╚═╣
╩ ╩  ╚ ╝  ╩╗   ╩ ╩  ╚══  ══╝  ══╝    ╝`
```

`bannerContent(width int)` 根据终端宽度居中渲染 ASCII Art，并在其下方追加副标题、快捷键提示和分隔线。

---

## 启动条件：TTY 自动检测

`main.go` 通过 `github.com/charmbracelet/x/term` 检测标准输入是否为交互式终端：

```go
if *feishuMode {
    // 飞书 Bot 模式
} else if term.IsTerminal(os.Stdin.Fd()) {
    // 交互式终端 → 启动 TUI
    RunTUI(ctx, eng, skillsIndex, workDir, modelName)
} else {
    // 管道 / CI 环境 → 退回 CLI REPL
    RunCLI(ctx, eng, skillsIndex)
}
```

---

## 日志隔离

`RunTUI` 入口处将 `log` 输出重定向到 `io.Discard`，防止引擎内部日志污染 AltScreen 输出：

```go
func RunTUI(...) error {
    origWriter := log.Writer()
    log.SetOutput(io.Discard)
    defer log.SetOutput(origWriter)
    // ...
}
```

---

## 数据流：engine.Event → Bubbletea Msg

engine.RunStream 返回 `<-chan engine.Event`，通过**链式 tea.Cmd** 桥接到 Bubbletea 消息循环：

```
engine.RunStream(ctx, prompt)
  └─ <-chan Event
       └─ readNextEvent(ch)    ← 阻塞读取一个 Event，返回 tea.Cmd
            └─ eventMsg        ← 包装为 Bubbletea Msg，触发 Update
                 └─ handleEvent() → 根据事件类型更新 model 状态
                      └─ readNextEvent(ch) ← 调度下一次读取（链式驱动）
```

```go
type eventMsg engine.Event

func readNextEvent(ch <-chan engine.Event) tea.Cmd {
    return func() tea.Msg {
        evt, ok := <-ch
        if !ok {
            return eventMsg{Type: engine.EventDone}
        }
        return eventMsg(evt)
    }
}
```

---

## 事件处理与高亮规则

| engine.Event | TUI 行为 | 样式 |
|---|---|---|
| `EventActionDelta` | delta 追加到 `pendingReply`，原始文本写入 scrollback | 普通文字 |
| `EventToolStart` | flush 渲染当前文本块；记录工具名、起始时间、工具参数；启动 spinner | 黄色工具进度行 |
| `EventToolResult` | 追加完成行（工具名 + 耗时）；清空 `currentTool` | 绿色 `✓` / 红色 `✗` |
| `EventDone` | flush 渲染最终文本块；`running=false`；重新激活输入框 | 粗体绿色 `✅ 任务完成` |
| `EventError` | 丢弃未渲染原始文本；`running=false`；追加红色错误行 | 红色 `❌` |

---

## Spinner 动词轮换

工具执行期间，ToolProgress 行展示随时间轮换的中文动词，增强等待反馈：

```go
var spinnerVerbs = []string{
    "思考中", "分析中", "处理中", "推理中", "计算中", "评估中",
}
```

Spinner 每 tick 触发一次 `spinner.TickMsg`，`tickCount` 累计到 30（约 3 秒）时 `verbIdx` 递增，6 个动词循环：

```go
case spinner.TickMsg:
    if m.running && m.currentTool != "" {
        m.tickCount++
        if m.tickCount%30 == 0 {
            m.verbIdx = (m.verbIdx + 1) % len(spinnerVerbs)
        }
        // ...
    }
```

---

## summarizeTool：工具参数摘要

`renderToolProgress` 调用 `summarizeTool` 将工具参数压缩为单行摘要，显示在工具名后的括号中：

```
⠼ 思考中...  bash(go test ./... 2>&1 | head -20)  [1.2s]
⠼ 分析中...  read_file(agent_loop.go)  [0.4s]
```

| 工具名 | 摘要逻辑 |
|--------|---------|
| `bash` | 提取 `command` 字段，截断至 120 字符 |
| `read_file` / `write_file` / `edit_file` | 提取 `path` 字段，取 `filepath.Base` |
| 其他工具 | JSON 原文截断至 80 字符 |
| 解析失败 | 返回空字符串（工具名不加括号）|

---

## View() 调用链

`View()` 根据 `phase` 选择渲染路径：

```go
func (m tuiModel) View() string {
    if m.phase == phaseWelcome {
        // bannerContent + StatusBar + Input + Footer
    } else {
        scrollH := m.scrollHeight()
        // renderConversation(scrollH)
        // [renderToolProgress()]  ← 仅 running && currentTool != ""
        // renderStatusBar()
        // renderInput()
        // renderFooter()
    }
}
```

### 动态 scrollHeight()

Scrollback 可用行数随运行状态动态调整：

```go
func (m tuiModel) scrollHeight() int {
    reserved := 3 // StatusBar + Input + Footer
    if m.running && m.currentTool != "" {
        reserved = 4 // 增加 ToolProgress 行
    }
    h := m.height - reserved
    if h < 1 { h = 1 }
    return h
}
```

---

## Markdown 渲染

### 流式渲染策略

LLM 文字输出（`EventActionDelta`）在 streaming 期间以原始文本追加展示；在**工具边界**（`EventToolStart`）和任务结束（`EventDone`）时，通过 [glamour](https://github.com/charmbracelet/glamour) 统一渲染整块文本：

```
EventActionDelta × N  →  pendingReply 累积原始文本
                              ↓
EventToolStart / EventDone  →  glamour.Render(pendingReply)
                              ↓
                         替换 lines[pendingReplyStart:]
```

### 关键字段

```go
pendingReply      string // 累积当前文本块的原始 Markdown
pendingReplyStart int    // pendingReply 对应 lines 中的起始行索引
```

### 避免终端颜色查询

故意不使用 `glamour.WithAutoStyle()`——该选项会发送 OSC 11 终端颜色查询，终端响应会被 Bubbletea 的 textinput 误判为用户输入，导致输入框乱码。改用固定 `"dark"` 样式：

```go
glamour.NewTermRenderer(
    glamour.WithStandardStyle("dark"),
    glamour.WithWordWrap(width-4),
)
```

---

## 键盘交互与滚动

### 全部按键

| 按键 | idle 状态 | Agent 运行中 |
|------|-----------|-------------|
| `Enter` | 发送输入，启动 Agent（首次触发 phaseWelcome→phaseChat） | 忽略 |
| `Tab` | 斜杠命令 Skill 补全循环 | 忽略 |
| `Ctrl-C` / `Ctrl-D` | 退出 TUI | 调用 `cancelFn()` 中断 Agent |
| 鼠标滚轮上 / `PgUp` / `↑` | 向上滚动 | 同左 |
| 鼠标滚轮下 / `PgDn` / `↓` | 向下滚动，到底回到 auto-scroll | 同左 |
| `End` | 强制跳回底部（auto-scroll） | — |

### 滚动实现

滚动状态用 `viewTop int` 表示：

- `viewTop = -1`：**auto-scroll 模式**，View() 始终展示 `lines` 末尾
- `viewTop ≥ 0`：**手动滚动模式**，View() 从该行索引开始展示

```go
func (m tuiModel) scrollBy(delta int) tuiModel {
    scrollH := m.scrollHeight()
    if m.viewTop < 0 {
        m.viewTop = len(m.lines) - scrollH // 从底部进入手动模式
    }
    m.viewTop += delta
    if m.viewTop >= len(m.lines)-scrollH {
        m.viewTop = -1 // 到达底部，回到 auto-scroll
    }
    return m
}
```

Footer 在手动滚动时显示当前位置百分比：

```
enter 发送  / 技能命令  ↑↓ 滚动  end 回底部 (42%)  ctrl+c 退出
```

---

## Slash 命令与 Tab 补全

### 识别流程

输入以 `/` 开头时，`resolvePrompt` 查找对应 Skill：

```
/skill-name [可选附加文本]
    ↓
skills.Index.GetFullContent("skill-name")
    ↓ 成功           ↓ 失败
  ◎ 技能已加载     ✗ 技能未找到: skill-name
  → Agent 运行       → 聚焦输入框，等待下次输入
```

### Tab 补全

1. 首次 Tab：以当前输入前缀匹配所有 Skill 名，缓存结果，补全第一个匹配项
2. 再次 Tab：在匹配列表中循环
3. 任意非 Tab 按键：退出补全循环

当前选中项显示在 Footer（青色），其余为灰色：

```
  ↹  /go-coding-standards   /go-lint-guide
```

---

## Context 传播

```
signal.NotifyContext(SIGINT/SIGTERM)  ← outerCtx（main.go）
  │
  ├─ tea.WithContext(outerCtx)        ← Bubbletea 程序级 context
  │    当 SIGTERM 到达时，Bubbletea 自动退出
  │
  └─ context.WithCancel(outerCtx)    ← 每次 Agent 运行派生子 context
       ├─ 存储于 m.cancelFn
       └─ Ctrl-C → cancelFn()        ← 取消当前 Agent，不退出 TUI
```

---

## 包级样式变量

所有 `lipgloss.Style` 在包级 `var` 块中定义，避免每帧 `View()` 调用时重复分配：

| 变量 | 颜色 | 用途 |
|------|------|------|
| `userMsgStyle` | Color "12"，Bold | 用户消息标签 |
| `assistantStyle` | Color "10"，Bold | Agent 回复标签 |
| `dimStyle` | Color "240" | 灰色辅助文字 |
| `errorStyle` | Color "9" | 错误消息 |
| `statusBarStyle` | Bg "235" / Fg "11" | StatusBar 背景 |
| `toolRunStyle` | Color "11" | 工具名（运行中，黄色） |
| `verbRunStyle` | Color "226" | Spinner + 动词（亮黄色） |
| `toolOKStyle` | Color "10" | 工具成功（绿色） |
| `toolErrStyle` | Color "9" | 工具失败（红色） |
| `doneStyle` | Color "10"，Bold | 任务完成（粗体绿色） |
| `skillStyle` | Color "14" | 技能激活（青色） |
| `cyanStyle` | Color "81" | 快捷键高亮 |
| `brandStyle` | Color "226"，Bold | harness9 品牌名 |
| `sepStyle` | Color "237" | 分隔线 |

---

## 技术依赖

| 库 | 版本 | 用途 |
|------|------|------|
| `github.com/charmbracelet/bubbletea` | v1.3.10 | Elm Architecture TUI 框架，AltScreen + 鼠标事件 |
| `github.com/charmbracelet/lipgloss` | v1.1.x | 终端样式与颜色 |
| `github.com/charmbracelet/bubbles` | v1.0.0 | spinner（工具进度）+ textinput（输入框） |
| `github.com/charmbracelet/glamour` | v1.0.0 | Markdown 渲染（代码块、加粗、列表等） |
| `github.com/charmbracelet/x/term` | 间接依赖 | TTY 检测（`term.IsTerminal`） |
