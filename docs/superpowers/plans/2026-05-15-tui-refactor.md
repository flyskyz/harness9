# TUI 重构实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 harness9 TUI 对标 OpenHarness 架构：引入 Phase 状态机、WelcomeBanner ASCII Art、职责分离的子渲染器、Spinner 动词轮换和工具参数摘要。

**Architecture:** 将现有 `tui.go`（560 行）拆分为 4 个文件（`tui.go` 结构体核心、`tui_update.go` 交互逻辑、`tui_view.go` 渲染层、`tui_banner.go` ASCII Art）；引入 `tuiPhase` 状态机区分欢迎页与对话页；删除 `statusLine` 字段，各职责由独立渲染函数接管。

**Tech Stack:** Go 1.25.3, bubbletea v1.3.10, lipgloss v1.1.1, bubbles v1.0.0, glamour v1.0.0

---

## 文件变更总览

| 操作 | 文件 | 说明 |
|------|------|------|
| Modify | `cmd/harness9/tui.go` | 新增类型/字段、删除 statusLine、更新样式 |
| Modify | `cmd/harness9/tui_test.go` | 修复 2 个已损坏测试 + 新增 7 个测试 |
| Create | `cmd/harness9/tui_banner.go` | ASCII Art 常量 + bannerContent() |
| Create | `cmd/harness9/tui_view.go` | View() + 6 个子渲染器 |
| Create | `cmd/harness9/tui_update.go` | Update/handleEvent/summarizeTool 等逻辑 |

---

## Task 1: 数据模型变更 + 修复编译

**Files:**
- Modify: `cmd/harness9/tui.go`
- Modify: `cmd/harness9/tui_test.go`

- [ ] **Step 1: 在 tui.go 中添加 tuiPhase 类型、spinnerVerbs 和新样式**

  在 `package main` 顶部（现有 `var (` 样式块之后）插入：

  ```go
  // tuiPhase 表示 TUI 的当前显示阶段。
  type tuiPhase int

  const (
      phaseWelcome tuiPhase = iota // 启动欢迎页
      phaseChat                    // 进入对话后
  )

  // spinnerVerbs 是工具运行中轮换显示的中文动词（每 30 次 tick ≈ 3s 切换一次）。
  var spinnerVerbs = []string{
      "思考中", "分析中", "处理中", "推理中", "计算中", "评估中",
  }
  ```

  同时将样式块中 `dimStyle` 的颜色从 `"8"` 改为 `"240"`（设计规范），并新增三个样式：

  ```go
  dimStyle = lipgloss.NewStyle().
          Foreground(lipgloss.Color("240"))   // 暗灰（由 "8" 改为 "240"）

  cyanStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))           // 亮青 #5fd7ff
  brandStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Bold(true) // 黄色粗体
  sepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))           // 深灰分隔线
  ```

  删除现有 `headerStyle`（将被 bannerContent() 替代）：

  ```go
  // 删除这 4 行：
  // headerStyle = lipgloss.NewStyle().
  //         Background(lipgloss.Color("237")).
  //         Foreground(lipgloss.Color("12")).
  //         Padding(0, 1)
  ```

- [ ] **Step 2: 更新 tuiModel struct——新增 4 个字段、删除 statusLine**

  将 tuiModel struct 中 `spinner / statusLine / input` 区域改为：

  ```go
  // Footer 组件
  spinner spinner.Model
  input   textinput.Model

  // Phase 状态机
  phase tuiPhase

  // Spinner 动词轮换
  verbIdx   int // 0-5，当前动词索引
  tickCount int // TickMsg 计数，每 30 次递增 verbIdx
  ```

  在 `currentTool string` / `toolStart time.Time` 下方新增：

  ```go
  toolArgs json.RawMessage // 当前工具的原始参数（用于摘要展示）
  ```

  **删除** `statusLine string` 字段（完整一行）。

- [ ] **Step 3: 在 tui.go 导入中新增 `encoding/json`**

  tui.go 的 import 块新增：

  ```go
  "encoding/json"
  ```

- [ ] **Step 4: 更新 newTUIModel——初始化 phase = phaseWelcome**

  在 return tuiModel{...} 中新增 `phase: phaseWelcome,`（其他字段不变）：

  ```go
  return tuiModel{
      workDir:     workDir,
      modelName:   modelName,
      phase:       phaseWelcome,
      spinner:     sp,
      input:       ti,
      outerCtx:    outerCtx,
      eng:         eng,
      skillsIndex: idx,
      viewTop:     -1,
  }
  ```

- [ ] **Step 5: 删除 tui.go 中所有 `m.statusLine` 赋值点**

  搜索 `m.statusLine`，定位到以下 6 处，全部删除赋值语句（仅删除该行）：

  - `handleEvent` EventToolStart：删除 `m.statusLine = toolRunStyle.Render(...)`
  - `handleEvent` EventToolResult：删除 `m.statusLine = ""`
  - `handleEvent` EventDone：删除 `m.statusLine = ""`
  - `handleEvent` EventError：将 `m.statusLine = errorStyle.Render("❌ " + errMsg)` **替换** 为追加到 scrollback：
    ```go
    m.lines = append(m.lines, errorStyle.Render("❌ "+errMsg))
    ```
  - `Update()` spinner.TickMsg 分支：删除 `m.statusLine = fmt.Sprintf(...)`
  - `View()` 中 `statusContent = m.statusLine`：暂时替换为 `statusContent = ""`（Task 5 将整体重写 View）

  同时在 EventToolStart 中，在 `m.toolStart = time.Now()` 之后新增：

  ```go
  tc, _ := evt.Data.(schema.ToolCall)
  m.currentTool = tc.Name
  m.toolArgs = tc.Arguments   // 新增
  m.toolStart = time.Now()
  ```

  在 EventToolResult 末尾（`m.currentTool = ""`之后）新增：

  ```go
  m.toolArgs = nil
  ```

  在 EventDone 末尾（`m.currentTool = ""`之后）新增：

  ```go
  m.toolArgs = nil
  ```

- [ ] **Step 6: 在 KeyEnter 处理中添加 nil guard 和 phase 切换**

  在 `raw := strings.TrimSpace(m.input.Value())` 之后、`m.input.Reset()` 之前，添加：

  ```go
  m.phase = phaseChat // 首次 Enter 即进入对话阶段
  ```

  在 `m.eventCh = ch` 之前（RunStream 调用前）添加 nil guard：

  ```go
  if m.eng == nil {
      m.input.Focus()
      return m, textinput.Blink
  }
  ```

- [ ] **Step 7: 修复 tui_test.go 中已损坏的两个测试**

  将 `TestEventDone_ResetsRunningState` 中的 `m.statusLine` 检查**删除**（statusLine 字段已移除，不再需要验证）：

  ```go
  // 删除这 4 行：
  // if m.statusLine != "" {
  //     t.Errorf("statusLine should be cleared, got %q", m.statusLine)
  // }
  ```

  将 `TestEventError_SetsStatusLineAndResetsRunning` 整体替换为（检查 scrollback 而非 statusLine）：

  ```go
  func TestEventError_AppendsToScrollbackAndResetsRunning(t *testing.T) {
      m := newTestModel()
      m.running = true
      m.currentTool = "bash"
      m.lines = []string{"partial text"}
      m.pendingReplyStart = 0

      m = applyUpdate(m, eventMsg{Type: engine.EventError, Data: "context cancelled"})

      if m.running {
          t.Error("running should be false after EventError")
      }
      if m.currentTool != "" {
          t.Errorf("currentTool should be cleared, got %q", m.currentTool)
      }
      if len(m.lines) == 0 {
          t.Fatal("error line should be appended to scrollback")
      }
      if !strings.Contains(m.lines[len(m.lines)-1], "context cancelled") {
          t.Errorf("last line should contain error message, got %q", m.lines[len(m.lines)-1])
      }
  }
  ```

- [ ] **Step 8: 验证编译通过**

  ```
  go build ./cmd/harness9/
  ```

  Expected: 无编译错误

- [ ] **Step 9: 运行测试，确认通过**

  ```
  go test -v ./cmd/harness9/
  ```

  Expected: 所有已有测试通过（注意：`TestEventError_SetsStatusLineAndResetsRunning` 已被重命名）

- [ ] **Step 10: Commit**

  ```bash
  git add cmd/harness9/tui.go cmd/harness9/tui_test.go
  git commit -m "refactor(tui): 引入 tuiPhase 状态机，删除 statusLine 字段，EventError 改写到 scrollback"
  ```

---

## Task 2: summarizeTool — TDD

**Files:**
- Modify: `cmd/harness9/tui_test.go`（先写测试）
- Modify: `cmd/harness9/tui.go`（再写实现）

- [ ] **Step 1: 在 tui_test.go 末尾写 4 个失败测试**

  ```go
  func TestSummarizeTool_Bash(t *testing.T) {
      args := json.RawMessage(`{"command":"go test ./... 2>&1 | head -20"}`)
      got := summarizeTool("bash", args)
      if got != "go test ./... 2>&1 | head -20" {
          t.Errorf("got %q", got)
      }
  }

  func TestSummarizeTool_Bash_Truncates(t *testing.T) {
      long := strings.Repeat("x", 130)
      args := json.RawMessage(`{"command":"` + long + `"}`)
      got := summarizeTool("bash", args)
      if len([]rune(got)) != 121 { // 120 chars + "…"
          t.Errorf("expected 121 runes (120 + ellipsis), got %d: %q", len([]rune(got)), got)
      }
      if !strings.HasSuffix(got, "…") {
          t.Errorf("expected ellipsis suffix, got %q", got)
      }
  }

  func TestSummarizeTool_ReadFile(t *testing.T) {
      args := json.RawMessage(`{"path":"/home/user/project/main.go"}`)
      got := summarizeTool("read_file", args)
      if got != "main.go" {
          t.Errorf("got %q, want %q", got, "main.go")
      }
  }

  func TestSummarizeTool_Other(t *testing.T) {
      args := json.RawMessage(`{"key":"value"}`)
      got := summarizeTool("custom_tool", args)
      if got != `{"key":"value"}` {
          t.Errorf("got %q", got)
      }
  }

  func TestSummarizeTool_InvalidArgs(t *testing.T) {
      args := json.RawMessage(`not-json`)
      got := summarizeTool("bash", args)
      if got != "" {
          t.Errorf("invalid args should return empty string, got %q", got)
      }
  }
  ```

  （注意：需要在文件顶部 import 中补充 `"encoding/json"`）

- [ ] **Step 2: 运行测试，确认失败**

  ```
  go test -v ./cmd/harness9/ -run TestSummarizeTool
  ```

  Expected: FAIL — `undefined: summarizeTool`

- [ ] **Step 3: 在 tui.go 末尾（RunTUI 之前）添加 summarizeTool 实现**

  ```go
  // summarizeTool 根据工具名对参数进行智能截断摘要，用于 ToolProgress 展示。
  func summarizeTool(name string, args json.RawMessage) string {
      switch name {
      case "bash":
          var v struct {
              Command string `json:"command"`
          }
          if err := json.Unmarshal(args, &v); err != nil || v.Command == "" {
              return ""
          }
          cmd := strings.ReplaceAll(v.Command, "\n", " ↵ ")
          if len([]rune(cmd)) > 120 {
              return string([]rune(cmd)[:120]) + "…"
          }
          return cmd
      case "read_file", "write_file", "edit_file":
          var v struct {
              Path string `json:"path"`
          }
          if err := json.Unmarshal(args, &v); err != nil || v.Path == "" {
              return ""
          }
          return filepath.Base(v.Path)
      default:
          if len(args) == 0 {
              return ""
          }
          s := string(args)
          runes := []rune(s)
          if len(runes) > 80 {
              return string(runes[:80]) + "…"
          }
          return s
      }
  }
  ```

  在 tui.go 导入中新增 `"path/filepath"`。

- [ ] **Step 4: 运行测试，确认全部通过**

  ```
  go test -v ./cmd/harness9/ -run TestSummarizeTool
  ```

  Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

  ```bash
  git add cmd/harness9/tui.go cmd/harness9/tui_test.go
  git commit -m "feat(tui): 添加 summarizeTool 工具参数摘要函数"
  ```

---

## Task 3: Phase 切换 + Spinner 动词轮换 — TDD

**Files:**
- Modify: `cmd/harness9/tui_test.go`（先写测试）
- Modify: `cmd/harness9/tui.go`（再写实现）

- [ ] **Step 1: 在 tui_test.go 末尾写 3 个失败测试**

  ```go
  func TestPhaseTransition_WelcomeToChat(t *testing.T) {
      m := newTestModel()
      if m.phase != phaseWelcome {
          t.Fatal("new model should start in phaseWelcome")
      }
      m.input.SetValue("hello world")

      m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})

      if m.phase != phaseChat {
          t.Errorf("phase should be phaseChat after first Enter, got %v", m.phase)
      }
  }

  func TestPhaseStaysChat_AfterFirstEnter(t *testing.T) {
      m := newTestModel()
      m.phase = phaseChat
      m.input.SetValue("second message")

      m = applyUpdate(m, tea.KeyMsg{Type: tea.KeyEnter})

      if m.phase != phaseChat {
          t.Errorf("phase should remain phaseChat, got %v", m.phase)
      }
  }

  func TestSpinnerVerbRotation(t *testing.T) {
      m := newTestModel()
      m.running = true
      m.currentTool = "bash"
      m.verbIdx = 0
      m.tickCount = 29

      m = applyUpdate(m, spinner.TickMsg{})

      if m.tickCount != 30 {
          t.Errorf("tickCount should be 30, got %d", m.tickCount)
      }
      if m.verbIdx != 1 {
          t.Errorf("verbIdx should advance to 1 after 30 ticks, got %d", m.verbIdx)
      }

      // 验证循环回绕
      m.verbIdx = 5
      m.tickCount = 59
      m = applyUpdate(m, spinner.TickMsg{})
      if m.verbIdx != 0 {
          t.Errorf("verbIdx should wrap to 0, got %d", m.verbIdx)
      }
  }
  ```

  在 tui_test.go 顶部 import 中补充 `"github.com/charmbracelet/bubbles/spinner"`。

- [ ] **Step 2: 运行测试，确认失败**

  ```
  go test -v ./cmd/harness9/ -run "TestPhase|TestSpinner"
  ```

  Expected: FAIL — `TestPhaseTransition_WelcomeToChat` fails（phase 不切换），`TestSpinnerVerbRotation` fails（tickCount 不递增）

- [ ] **Step 3: 更新 tui.go spinner.TickMsg 处理，加入 tickCount/verbIdx 逻辑**

  将现有 `case spinner.TickMsg:` 分支替换为：

  ```go
  case spinner.TickMsg:
      if m.running && m.currentTool != "" {
          m.tickCount++
          if m.tickCount%30 == 0 {
              m.verbIdx = (m.verbIdx + 1) % len(spinnerVerbs)
          }
          var cmd tea.Cmd
          m.spinner, cmd = m.spinner.Update(msg)
          return m, cmd
      }
      return m, nil
  ```

- [ ] **Step 4: 运行测试，确认全部通过**

  ```
  go test -v ./cmd/harness9/ -run "TestPhase|TestSpinner"
  ```

  Expected: PASS (3 tests)

- [ ] **Step 5: 更新 scrollHeight() 为动态计算**

  将现有 `scrollHeight()` 方法替换为：

  ```go
  // scrollHeight 返回对话区域可显示的行数。
  // 运行中且有活跃工具时额外保留 1 行给 ToolProgress。
  func (m tuiModel) scrollHeight() int {
      reserved := 3 // StatusBar + PromptInput + Footer
      if m.running && m.currentTool != "" {
          reserved = 4 // + ToolProgress
      }
      h := m.height - reserved
      if h < 1 {
          h = 1
      }
      return h
  }
  ```

- [ ] **Step 6: 运行全量测试，确认无回归**

  ```
  go test ./cmd/harness9/
  ```

  Expected: PASS

- [ ] **Step 7: Commit**

  ```bash
  git add cmd/harness9/tui.go cmd/harness9/tui_test.go
  git commit -m "feat(tui): Phase 状态机切换、Spinner 动词轮换、scrollHeight 动态化"
  ```

---

## Task 4: tui_banner.go — ASCII Art + bannerContent()

**Files:**
- Create: `cmd/harness9/tui_banner.go`

- [ ] **Step 1: 创建 tui_banner.go**

  ```go
  package main

  import (
      "strings"
  )

  // asciiArt 是用 3 行框线字符绘制的 HARNESS9 标题。
  // 字符宽度：H/A/R/N/E/S/9 各 3 列，字符间 2 空格，共 38 字符宽。
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
  ```

- [ ] **Step 2: 验证编译**

  ```
  go build ./cmd/harness9/
  ```

  Expected: 无错误

- [ ] **Step 3: 运行测试**

  ```
  go test ./cmd/harness9/
  ```

  Expected: PASS

- [ ] **Step 4: Commit**

  ```bash
  git add cmd/harness9/tui_banner.go
  git commit -m "feat(tui): 新增 tui_banner.go，HARNESS9 框线字体 ASCII Art"
  ```

---

## Task 5: tui_view.go — 重写 View() 和 6 个子渲染器

**Files:**
- Create: `cmd/harness9/tui_view.go`
- Modify: `cmd/harness9/tui.go`（删除旧 View() 函数）

- [ ] **Step 1: 创建 tui_view.go，包含全部渲染逻辑**

  ```go
  package main

  import (
      "fmt"
      "os"
      "strings"
      "time"

      "github.com/charmbracelet/lipgloss"
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

  // renderToolProgress 渲染工具执行进度行（仅 phaseChat 且 running && currentTool != "" 时显示）。
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

      verbStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
      toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

      return "  " +
          verbStyle.Render(m.spinner.View()+" "+verb+"...") +
          toolStyle.Render("  "+toolDisplay) +
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
      // 补全循环中：显示补全候选（completionHint 已由 buildCompletionHint 生成）
      if m.completionHint != "" {
          return m.completionHint
      }

      // 手动滚动中：替换滚动部分为位置百分比
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

      // 默认快捷键提示
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
          // 欢迎页：Banner + StatusBar + Input + Footer
          sb.WriteString(bannerContent(m.width))
          sb.WriteByte('\n')
          sb.WriteString(m.renderStatusBar())
          sb.WriteByte('\n')
          sb.WriteString(m.renderInput())
          sb.WriteByte('\n')
          sb.WriteString(m.renderFooter())
      } else {
          // 对话页：Conversation + [ToolProgress] + StatusBar + Input + Footer
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
  ```

- [ ] **Step 2: 删除 tui.go 中的旧 View() 函数**

  定位 tui.go 中的 `func (m tuiModel) View() string {`（约第 488 行），删除整个函数（到末尾的 `}`，约 57 行）。

- [ ] **Step 3: 验证编译**

  ```
  go build ./cmd/harness9/
  ```

  Expected: 无错误（statusBarStyle 在 tui.go 中已定义，tui_view.go 同属 package main）

- [ ] **Step 4: 运行测试**

  ```
  go test ./cmd/harness9/
  ```

  Expected: PASS（View() 现在在 tui_view.go，测试中不直接调用 View，不受影响）

- [ ] **Step 5: Commit**

  ```bash
  git add cmd/harness9/tui_view.go cmd/harness9/tui.go
  git commit -m "feat(tui): 新增 tui_view.go，View() 拆分为 6 个子渲染器"
  ```

---

## Task 6: tui_update.go — 抽离 Update 逻辑

**Files:**
- Create: `cmd/harness9/tui_update.go`
- Modify: `cmd/harness9/tui.go`（删除已移走的函数）

- [ ] **Step 1: 创建 tui_update.go，移入所有 Update 相关函数**

  将以下函数从 tui.go **剪切**到 tui_update.go（保持内容完全不变）：

  - `readNextEvent`
  - `Update`（含 `eventMsg` 类型定义）
  - `handleEvent`
  - `scrollBy`
  - `cycleCompletion`
  - `buildCompletionHint`
  - `flushPendingReply`
  - `renderMD`
  - `splitLines`
  - `summarizeTool`（Task 2 中添加到 tui.go，现在移过来）

  tui_update.go 文件头：

  ```go
  package main

  import (
      "context"
      "encoding/json"
      "fmt"
      "path/filepath"
      "strings"
      "time"

      "github.com/charmbracelet/bubbles/spinner"
      "github.com/charmbracelet/bubbles/textinput"
      tea "github.com/charmbracelet/bubbletea"
      "github.com/charmbracelet/glamour"

      "github.com/harness9/internal/engine"
      "github.com/harness9/internal/schema"
      "github.com/harness9/internal/skills"
  )

  // eventMsg 将 engine.Event 包装为 tea.Msg，供 Bubbletea 的 Update 分发。
  type eventMsg engine.Event
  ```

  （将 `eventMsg` 类型定义也从 tui.go 移到 tui_update.go）

- [ ] **Step 2: 删除 tui.go 中已移走的函数和类型**

  从 tui.go 中删除：
  - `type eventMsg engine.Event`
  - `func readNextEvent(...)`
  - `func (m tuiModel) Update(...)`
  - `func (m tuiModel) handleEvent(...)`
  - `func (m tuiModel) scrollBy(...)`
  - `func (m tuiModel) cycleCompletion(...)`
  - `func (m tuiModel) buildCompletionHint(...)`
  - `func (m tuiModel) flushPendingReply(...)`
  - `func renderMD(...)`
  - `func splitLines(...)`
  - `func summarizeTool(...)`

  同时清理 tui.go 的 import——移除不再使用的包（`context`、`encoding/json`、`fmt`、`path/filepath`、`strings`、`time`、所有 charmbracelet 包、engine/schema/skills）。

  tui.go 最终只保留：
  - package main + import（只需 `context`、`io`、`log`、`github.com/charmbracelet/bubbles/spinner`、`github.com/charmbracelet/bubbles/textinput`、`tea`、`lipgloss`、`engine`、`skills`）
  - `var (...)` 样式块
  - `type tuiPhase int` + 常量
  - `var spinnerVerbs`
  - `type tuiModel struct`
  - `func newTUIModel(...)`
  - `func (m tuiModel) Init() tea.Cmd`
  - `func RunTUI(...)`

- [ ] **Step 3: 验证编译**

  ```
  go build ./cmd/harness9/
  ```

  Expected: 无错误

- [ ] **Step 4: 运行全量测试**

  ```
  go test -v ./cmd/harness9/
  ```

  Expected: 全部 PASS（37 原有 + 7 新增 = 44 个测试）

- [ ] **Step 5: Commit**

  ```bash
  git add cmd/harness9/tui_update.go cmd/harness9/tui.go
  git commit -m "refactor(tui): 将 Update 逻辑抽离到 tui_update.go，tui.go 精简为 struct 核心"
  ```

---

## Task 7: 最终验证与收尾

**Files:** 只读检查，不修改代码

- [ ] **Step 1: 运行全量测试（含所有包）**

  ```
  go test ./...
  ```

  Expected: PASS

- [ ] **Step 2: 代码格式检查**

  ```
  gofmt -l ./cmd/harness9/
  ```

  Expected: 无输出（所有文件已格式化）

  如有输出，执行：

  ```
  gofmt -w ./cmd/harness9/
  ```

- [ ] **Step 3: go vet 检查**

  ```
  go vet ./cmd/harness9/
  ```

  Expected: 无警告

- [ ] **Step 4: 确认文件行数在预期范围内**

  ```bash
  wc -l cmd/harness9/tui.go cmd/harness9/tui_update.go cmd/harness9/tui_view.go cmd/harness9/tui_banner.go
  ```

  Expected:
  - `tui.go`: ≈ 110–130 行
  - `tui_update.go`: ≈ 200–250 行
  - `tui_view.go`: ≈ 170–200 行
  - `tui_banner.go`: ≈ 45–55 行

- [ ] **Step 5: Final commit（如有格式化修改）**

  ```bash
  git add -u
  git commit -m "style(tui): gofmt 格式化收尾"
  ```

  （若无修改则跳过此步）

- [ ] **Step 6: 合并到 master**

  ```bash
  git checkout master
  git merge tui --no-ff -m "feat(tui): 对标 OpenHarness 架构重构——Phase 状态机、ASCII Art Banner、职责分离渲染、Spinner 动词轮换"
  ```
