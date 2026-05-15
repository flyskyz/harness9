# TUI 重构设计文档：对标 OpenHarness 架构

> 日期：2026-05-15  
> 状态：已审批，待实现  
> 参考：`docs/技术调研/TUI-调研报告.md` Section 2.2 & 5.6

---

## 1. 目标

将 harness9 TUI 的架构对标 OpenHarness，在不引入未有功能的前提下：

1. 引入显式 **Phase 状态机**（welcome → chat）
2. 新增 **WelcomeBanner**（含 HARNESS9 框线字体 ASCII Art）
3. 将 StatusBar / ToolProgress / Footer **职责分离**
4. 新增 **Spinner 动词轮换**（6 个中文动词，3s 周期）
5. 新增 **工具参数智能摘要**（按工具类型截断）
6. 按职责将 `tui.go`（560 行）**拆分为 4 个文件**

---

## 2. 布局设计

### 2.1 Phase: welcome（启动画面）

```
  ╦ ╦  ╔╦╗  ╔═╗  ╔╗╦  ╔══  ╔══  ╔══  ╔═╗
  ╠═╣  ╠╩╣  ╠╦╝  ║╚╗  ╠═   ╚═╗  ╚═╗  ╚═╣   ← 亮青色（color "81"）
  ╩ ╩  ╚ ╝  ╩╗   ╩ ╩  ╚══  ══╝  ══╝    ╝

  harness9  ·  An AI-powered coding agent      ← "harness9" 黄色粗体，其余暗灰
  /skill 加载技能  │  Tab 补全  │  Ctrl+C 退出

  ──────────────────────────────────────────── ← 暗色分隔线
  model: claude-sonnet-4-6  │  mode: Default   ← StatusBar（常驻）
  > _                                          ← PromptInput
  enter 发送  / 技能命令  ↑↓ 历史  ctrl+c 退出  ← Footer（常驻）
```

### 2.2 Phase: chat（对话模式，Banner 消失）

```
  ▶ You: 帮我分析 main.go 的 bug
  ◆ harness9:
    好的，我先读取文件...                        ← ConversationView（弹性高度，可滚动）
    ✓ read_file(main.go) — 234ms
    发现第 42 行存在空指针...

  ⠼ 分析中...  bash(go test ./...)  [3.2s]    ← ToolProgress（仅 running 时显示，1 行）
  model: claude-sonnet-4-6  │  mode: Default   ← StatusBar（常驻，1 行）
  > _                                          ← PromptInput（1 行）
  enter 发送  / 技能命令  ↑↓ 滚动  ctrl+c 退出  ← Footer（常驻，1 行）
```

### 2.3 高度分配

| 区域 | 高度 | 显示条件 |
|------|------|---------|
| ConversationView | `height - 3`（idle）/ `height - 4`（running） | phaseChat |
| ToolProgress | 1 行 | phaseChat && running && currentTool != "" |
| StatusBar | 1 行（常驻） | 始终 |
| PromptInput | 1 行（常驻） | 始终 |
| Footer | 1 行（常驻） | 始终 |

---

## 3. 数据模型变更

### 3.1 新增类型

```go
type tuiPhase int

const (
    phaseWelcome tuiPhase = iota
    phaseChat
)
```

### 3.2 tuiModel 字段变更

**新增：**

```go
phase     tuiPhase  // 当前阶段
verbIdx   int       // spinner 动词轮换索引（0-5）
tickCount int       // tick 计数，每 30 次（≈3s）递增 verbIdx
```

**删除：**

```go
// statusLine string  ← 删除
// 原来三合一的 statusLine（工具进度 / 滚动提示 / 补全提示）
// 各职责由独立渲染函数接管，不再需要中间状态字符串
```

**保留不变：**
- `workDir`、`modelName`（StatusBar 展示）
- `width`、`height`（终端尺寸）
- `lines []string`、`viewTop int`（Scrollback + 滚动）
- `spinner`、`input`（Bubbles 组件）
- `currentTool`、`toolStart`（工具耗时跟踪）
- `pendingReply`、`pendingReplyStart`（Markdown 流式渲染）
- `typedPrefix`、`completions`、`completionIdx`、`completionHint`（Tab 补全）
- `outerCtx`、`eng`、`skillsIndex`、`eventCh`、`cancelFn`、`running`（运行时）

### 3.3 Spinner 动词表

```go
var spinnerVerbs = []string{
    "思考中", "分析中", "处理中", "推理中", "计算中", "评估中",
}
```

每 30 次 `spinner.TickMsg`（100ms × 30 = 3s）轮换一次。

### 3.4 Phase 切换规则

| 事件 | Phase 变化 |
|------|-----------|
| 用户第一次按 Enter 提交非空输入 | `phaseWelcome → phaseChat` |
| 之后的所有交互 | 保持 `phaseChat` |

---

## 4. 文件结构

现有 `tui.go`（560 行）按职责拆分为 4 个文件，同属 `package main`：

```
cmd/harness9/
├── tui.go           ← tuiModel struct、newTUIModel、Init、RunTUI 入口
│                       约 120 行
├── tui_update.go    ← Update()、handleEvent()、scrollBy()、
│                       cycleCompletion()、buildCompletionHint()、
│                       flushPendingReply()、summarizeTool()
│                       约 230 行
├── tui_view.go      ← View() + 6 个子渲染器：
│                       renderBanner()、renderConversation()、
│                       renderToolProgress()、renderStatusBar()、
│                       renderInput()、renderFooter()
│                       约 190 行
├── tui_banner.go    ← HARNESS9 ASCII Art 常量 + bannerContent()
│                       约 50 行
└── tui_test.go      ← 保留现有 37 个测试 + 新增测试
                        约 340 行
```

### View() 调用链

```
View()
  ├─ phase == phaseWelcome
  │    renderBanner()
  │      ├─ bannerContent()     — ASCII Art + 副标题 + 帮助行 + 分隔线
  │      ├─ renderStatusBar()
  │      ├─ renderInput()
  │      └─ renderFooter()
  │
  └─ phase == phaseChat
       ├─ renderConversation()  — Scrollback 区（弹性高度）
       ├─ renderToolProgress()  — 条件渲染（running && currentTool != ""）
       ├─ renderStatusBar()
       ├─ renderInput()
       └─ renderFooter()
```

---

## 5. 新增功能规格

### 5.1 WelcomeBanner ASCII Art

字体风格：3 行框线字体（Unicode 盒形字符 ╔═╗║╚╝╠╣╦╩）

```
╦ ╦  ╔╦╗  ╔═╗  ╔╗╦  ╔══  ╔══  ╔══  ╔═╗
╠═╣  ╠╩╣  ╠╦╝  ║╚╗  ╠═   ╚═╗  ╚═╗  ╚═╣
╩ ╩  ╚ ╝  ╩╗   ╩ ╩  ╚══  ══╝  ══╝    ╝
```

字符设计：

| 字符 | Row 1 | Row 2 | Row 3 | 设计要点 |
|------|-------|-------|-------|---------|
| H | `╦ ╦` | `╠═╣` | `╩ ╩` | 双柱 + 横梁 |
| A | `╔╦╗` | `╠╩╣` | `╚ ╝` | ╦ 作顶峰，╩ 作横撇 |
| R | `╔═╗` | `╠╦╝` | `╩╗ ` | P 形顶 + 右斜腿 |
| N | `╔╗╦` | `║╚╗` | `╩ ╩` | 双柱 + 对角折线 |
| E | `╔══` | `╠═ ` | `╚══` | 三横梁，中横略短 |
| S | `╔══` | `╚═╗` | `══╝` | Z 型反转曲线 |
| 9 | `╔═╗` | `╚═╣` | `  ╝` | 圆圈 + 右侧延伸尾 |

颜色：`lipgloss.Color("81")`（亮青 #5fd7ff），宽度居中

Banner 副标题结构：

```
  harness9  ·  An AI-powered coding agent
  /skill 加载技能  │  Tab 补全  │  Ctrl+C 退出
  ────────────────────────────────────────
```

- `harness9`：`lipgloss.Color("226")` 黄色粗体
- `·  An AI-powered coding agent`：`lipgloss.Color("240")` 暗灰
- 快捷键提示：`/skill`、`Tab`、`Ctrl+C` 用亮青色，描述文字暗灰
- 分隔线：`strings.Repeat("─", width-4)`，暗灰色

### 5.2 StatusBar（常驻）

格式：`model: <modelName>  │  mode: Default  │  <workDir>`

```go
// 样式：标签暗灰，值亮青
"model: " + cyanStyle.Render(m.modelName) +
dimStyle.Render("  │  mode: Default  │  ") +
cyanStyle.Render(shortPath(m.workDir))
```

`shortPath` 将绝对路径缩短为 `~/<dir>` 格式（`strings.Replace(p, homeDir, "~", 1)`）。

### 5.3 ToolProgress（运行时，仅 phaseChat）

格式：`<spinner> <verb>...  <toolName>(<summary>)  [<elapsed>]`

示例：`⠼ 分析中...  bash(go test ./...)  [3.2s]`

颜色：
- spinner：黄色（`lipgloss.Color("226")`）
- 动词 `分析中...`：黄色
- 工具名 + 参数：`lipgloss.Color("11")`
- 耗时 `[3.2s]`：暗灰

Spinner 动词轮换逻辑：

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
```

### 5.4 工具参数智能摘要（summarizeTool）

```go
func summarizeTool(name string, args json.RawMessage) string
```

| 工具名 | 摘要策略 |
|--------|---------|
| `bash` | 提取 `command` 字段，截取前 120 字符，超出加 `…` |
| `read_file` / `write_file` / `edit_file` | 提取 `path` 字段，只显示文件名（`filepath.Base`） |
| 其他工具 | `args` JSON 截取前 80 字符，超出加 `…` |
| 解析失败 | 降级为空字符串（只显示工具名） |

示例：
- `bash(go test ./... 2>&1 ↵ head...)` → `bash(go test ./...)`
- `read_file(main.go)`
- `edit_file(tui.go)`

### 5.5 Footer（常驻）

```
enter 发送  / 技能命令  ↑↓ 滚动  ctrl+c 退出
```

- 手动滚动时：`↑↓ 滚动  end 回底部 (xx%)`（替换 `↑↓ 滚动` 部分）
- 快捷键（`enter`、`/`、`↑↓`、`ctrl+c`、`end`）：亮青色
- 描述文字（`发送`、`技能命令` 等）：暗灰色

---

## 6. 配色汇总

| 元素 | lipgloss 颜色 | 说明 |
|------|-------------|------|
| ASCII Art | `"81"` | 亮青 #5fd7ff |
| 品牌名 `harness9` | `"226"` Bold | 黄色粗体 |
| 副标题 / 帮助描述 | `"240"` | 暗灰 |
| Footer 快捷键 | `"81"` | 亮青 |
| Footer 描述 | `"240"` | 暗灰 |
| StatusBar 值（model 名、workdir） | `"81"` | 亮青 |
| StatusBar 标签（`model:` `│` `mode:`） | `"240"` | 暗灰 |
| ToolProgress 动词 + spinner | `"226"` | 黄色 |
| ToolProgress 工具名 + 参数 | `"11"` | 黄色（终端标准黄） |
| ToolProgress 耗时 | `"240"` | 暗灰 |
| 分隔线 | `"237"` | 深灰 |
| 用户消息前缀 `▶ You:` | `"12"` Bold | 蓝色粗体（保留现有） |
| Assistant 前缀 `◆ harness9:` | `"10"` Bold | 绿色粗体（保留现有） |
| 工具成功 `✓` | `"10"` | 绿色 |
| 工具失败 `✗` | `"9"` | 红色 |
| 错误 `❌` | `"9"` | 红色 |
| 技能激活 `◎` | `"14"` | 青色 |

---

## 7. 测试策略

### 7.1 保留现有 37 个测试（不破坏现有行为）

所有现有测试函数保持不变，仅在 `newTestModel()` 中确保 `phase` 初始值为 `phaseWelcome`，不影响测试逻辑。

### 7.2 新增测试

| 测试函数 | 验证内容 |
|---------|---------|
| `TestPhaseTransition_WelcomeToChat` | 首次 Enter 后 phase 变为 phaseChat |
| `TestPhaseStaysChat_AfterFirstEnter` | 第二次 Enter 后 phase 保持 phaseChat |
| `TestSpinnerVerbRotation` | tickCount % 30 == 0 时 verbIdx 递增，循环回绕 |
| `TestSummarizeTool_Bash` | bash 命令截取前 120 字符 |
| `TestSummarizeTool_ReadFile` | read_file 只显示文件名 |
| `TestSummarizeTool_Other` | 未知工具 JSON 截取前 80 字符 |
| `TestSummarizeTool_InvalidArgs` | 解析失败降级为空字符串 |

---

## 8. 不在本次范围内

以下 OpenHarness 功能**不实现**（harness9 尚无对应基础设施）：

- Plan Mode 切换
- 权限确认 Modal 弹框
- Token 计数统计（需要 provider 返回 usage 信息）
- Swarm 多 Agent 面板
- OHJSON 跨进程协议（单进程无需）
- 历史会话列表

---

## 9. 迁移影响

- `RunTUI` 函数签名**不变**，main.go 无需修改
- 现有 `tui_test.go` 的 37 个测试**全部通过**（不破坏现有行为）
- `tui.go` 拆分后原文件删除，由 4 个新文件替代
- `statusLine` 字段删除后，所有引用点（`handleEvent`、`spinner.TickMsg`）均移入渲染函数
