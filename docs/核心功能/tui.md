# TUI 交互界面实现原理

harness9 在交互式终端（TTY）下自动启动全屏 TUI 模式，使用 [Bubbletea](https://github.com/charmbracelet/bubbletea) 框架实现 Elm Architecture 架构。

---

## 布局：分屏 Footer 架构

```
┌─────────────────────────────────────────┐
│ ⬡ harness9   gpt-4o-mini · ~/project   │  ← Header（固定 1 行）
├─────────────────────────────────────────┤
│                                         │
│  ▶ You: 帮我分析 main.go 里的 bug       │
│  ◎ 技能已加载: debugging-guide          │
│  ◆ harness9:                           │  ← Scrollback（弹性高度，可滚动）
│    好的，我先读取文件...                │
│    ✓ read_file(main.go) — 234ms        │
│    发现第 **42 行**存在空指针...        │
│                                         │
├─────────────────────────────────────────┤
│  ⠼ bash(go test ./...)  [3.2s]         │  ← StatusBar（固定 1 行）
├─────────────────────────────────────────┤
│  › _                                    │  ← Input（固定 1 行）
└─────────────────────────────────────────┘
```

| 区域 | 高度 | 职责 |
|------|------|------|
| Header | 1 行 | 展示 logo、模型名、workdir；不参与响应式重绘 |
| Scrollback | 剩余高度 | 历史消息追加输出；支持鼠标滚轮 / 键盘滚动查看历史 |
| StatusBar | 1 行 | 运行时：spinner + 工具名 + 耗时；滚动时：位置百分比；idle：Tab 补全提示 |
| Input | 1 行 | 单行文本输入框；Agent 运行时禁用，完成后重新激活 |

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

`go run ./cmd/harness9` 在 shell 中运行时自动进入 TUI；通过管道或脚本调用时退回 CLI，行为完全兼容。

---

## 日志隔离

`RunTUI` 入口处将 `log` 输出重定向到 `io.Discard`，防止引擎内部日志（`[engine-stream]`、`[skills]` 等）污染 AltScreen 输出：

```go
func RunTUI(...) error {
    log.SetOutput(io.Discard)
    // ...
}
```

需要调试时，可在启动前手动将 `log` 重定向到文件。

---

## 数据流：engine.Event → Bubbletea Msg

engine.RunStream 返回 `<-chan engine.Event`，需要桥接到 Bubbletea 的消息循环。核心机制是**链式 tea.Cmd**：

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

每个 `handleEvent` 调用的最后都返回 `readNextEvent(m.eventCh)`，形成自驱动链条，直到 EventDone 或 EventError 终止。

---

## 事件处理与高亮规则

| engine.Event | TUI 行为 | 样式 |
|---|---|---|
| `EventActionDelta` | delta 追加到 `pendingReply`，回写原始文本到 scrollback | 普通文字 |
| `EventToolStart` | flush 渲染当前文本块；记录工具名和起始时间；启动 spinner | 黄色 `⠿ toolName...` |
| `EventToolResult` | 追加完成行（含耗时）；更新 `pendingReplyStart` | 绿色 `✓` / 红色 `✗` |
| `EventDone` | flush 渲染最终文本块；`running=false`；重新激活输入框 | 粗体绿色 `✅ 任务完成` |
| `EventError` | 丢弃未渲染的原始文本；`running=false`；statusLine 显示错误 | 红色 `❌` |

Spinner 的 tick 由 `bubbles/spinner` 内置 `TickMsg` 独立驱动，与 engine Event 解耦，互不干扰。

---

## Markdown 渲染

### 流式渲染策略

LLM 的文字输出（`EventActionDelta`）在 streaming 期间以原始文本追加展示；在**工具边界**（`EventToolStart`）和任务结束（`EventDone`）时，通过 [glamour](https://github.com/charmbracelet/glamour) 统一渲染整块文本，替换 scrollback 中的原始内容。

```
EventActionDelta × N  →  pendingReply 累积原始文本
                              ↓
EventToolStart / EventDone  →  glamour.Render(pendingReply)
                              ↓
                         替换 lines[pendingReplyStart:]
```

这个"延迟渲染"策略避免了对每个 token 调用 glamour（性能损耗大），同时保证最终展示的代码块、加粗、列表等格式正确渲染。

### 关键实现字段

```go
pendingReply      string // 累积当前文本块的原始 Markdown
pendingReplyStart int    // pendingReply 对应 lines 中的起始行索引
```

### 避免终端颜色查询

故意不使用 `glamour.WithAutoStyle()`——该选项会发送 OSC 11 终端颜色查询，终端响应（`]11;rgb:...`、`[35;1R`）会写回 stdin，被 Bubbletea 的 textinput 误判为用户输入，导致输入框出现乱码。改用固定 `"dark"` 样式：

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
| `Enter` | 发送输入，启动 Agent | 忽略 |
| `Tab` | 斜杠命令 Skill 补全循环 | 忽略 |
| `Ctrl-C` / `Ctrl-D` | 退出 TUI | 调用 `cancelFn()` 中断 Agent |
| 鼠标滚轮上 / `Ctrl+Up` / `PgUp` | 向上滚动 | 同左 |
| 鼠标滚轮下 / `Ctrl+Down` / `PgDn` | 向下滚动，到底回到 auto-scroll | 同左 |
| `End` | 强制跳回底部（auto-scroll） | — |

### 滚动实现

滚动状态用 `viewTop int` 表示：

- `viewTop = -1`：**auto-scroll 模式**，View() 始终展示 `lines` 末尾
- `viewTop ≥ 0`：**手动滚动模式**，View() 从该行索引开始展示

```go
// scrollBy 将视口移动 delta 行（负数向上，正数向下）
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

新内容追加到 `lines` 末尾时，已滚动的用户（`viewTop ≥ 0`）视角保持不动；处于底部的用户（`viewTop = -1`）自动跟随。

**注意**：VS Code 集成终端会在进程级别拦截 PgUp/PgDn（用于终端自身的滚动缓冲区）。实际使用建议优先用**鼠标滚轮**或 **Ctrl+Up/Down**，这两种方式会正确传递给 TUI 进程。鼠标支持通过 `tea.WithMouseCellMotion()` 启用。

### 状态栏内容切换

```
运行中         →  ⠼ toolName  [Xs]       （工具进度）
手动滚动       →  ↑ PgUp/PgDn 滚动 · End 回到底部 (xx%)
idle，输入"/"  →  ↹  /skill-a   /skill-b  （Tab 补全提示）
idle，无输入   →  （空白）
```

---

## Slash 命令与 Tab 补全

### 识别流程

输入以 `/` 开头时，`resolvePrompt` 查找对应 Skill 并返回其全文作为 prompt：

```
/skill-name [可选附加文本]
    ↓
skills.Index.GetFullContent("skill-name")
    ↓ 成功           ↓ 失败
  ◎ 技能已加载     ✗ 技能未找到: skill-name
  → Agent 运行       → 聚焦输入框，等待下次输入
```

### Tab 补全

输入 `/` 后按 Tab 进入补全循环：

1. 首次 Tab：以当前输入前缀匹配所有 Skill 名，缓存结果，补全第一个匹配项
2. 再次 Tab：在匹配列表中循环
3. 任意非 Tab 按键：退出补全循环，根据新输入重新计算匹配

补全提示显示在 StatusBar：

```
  ↹  /go-coding-standards   /go-lint-guide
```

当前选中项高亮（青色），其余为灰色。

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

## 技术依赖

| 库 | 版本 | 用途 |
|------|------|------|
| `github.com/charmbracelet/bubbletea` | v1.3.10 | Elm Architecture TUI 框架，AltScreen + 鼠标事件 |
| `github.com/charmbracelet/lipgloss` | v1.1.x | 终端样式与颜色（Header / StatusBar / 阶段高亮） |
| `github.com/charmbracelet/bubbles` | v1.0.0 | spinner（工具进度）+ textinput（输入框） |
| `github.com/charmbracelet/glamour` | v1.0.0 | Markdown 渲染（代码块、加粗、列表等） |
| `github.com/charmbracelet/x/term` | 间接依赖 | TTY 检测（`term.IsTerminal`） |

---

## 文件位置

```
cmd/harness9/
├── tui.go        # RunTUI 入口 + tuiModel 定义 + Update/View/补全/滚动实现
└── tui_test.go   # 37 个单元测试：直接注入 tea.Msg 验证 model 状态变化
```

测试策略：直接调用 `tuiModel.Update()` 注入消息，验证 model 字段，不测试 `View()` 渲染字符串（脆弱且价值低）。
