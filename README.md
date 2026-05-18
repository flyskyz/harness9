# harness9

> 轻量级、功能完备、生产可用的 Go Agent Harness 框架

---

## 为什么选择 harness9？

大多数 Agent 框架要么过于臃肿（满屏抽象层、数百个依赖），要么过于简陋（仅能跑个 demo）。harness9 走中间路线：**最小抽象、完整功能、生产就绪**。

```
核心设计理念:

  简洁 — 最小化抽象层，代码直白易读
  完备 — 覆盖 Agent 运行所需的全部核心模块
  生产可用 — 错误恢复、上下文管理、超时控制等生产级特性
```

---

## 架构总览

```
                    ┌─────────────────────┐
                    │       Engine        │
                    │   (MainLoop 核心)    │
                    └──┬───┬───┬───┬─────┘
                       │   │   │   │
          ┌────────────┘   │   │   └────────────┐
          ▼                ▼   ▼                 ▼
    ┌──────────┐   ┌──────────┐   ┌──────────────────┐   ┌──────────┐
    │ Provider │   │ Schema   │   │   Tool Registry  │   │  Memory  │
    │ (模型接口) │   │ (类型层)  │   │  (工具注册/调用)  │   │ (记忆层)  │
    └──────────┘   └──────────┘   └──────────────────┘   └──────────┘
          │                               │                     │
    ┌─────┴────┐                    ┌────┴────┐         ┌──────┴──────┐
    ▼          ▼                    ▼         ▼         ▼             ▼
┌────────┐ ┌────────┐         ┌────────┐ ┌────────┐ ┌────────┐ ┌──────────┐
│ OpenAI │ │Anthropic│        │  bash  │ │read /  │ │Session │ │Compactor │
│Provider│ │Provider │        │  Tool  │ │write   │ │Manager │ │(滑动窗口) │
└────────┘ └────────┘         └────────┘ └────────┘ └────────┘ └──────────┘
```

**Engine** 驱动 Agent 主循环：**推理 → 工具调用 → 观察 → 继续推理**

---

## 核心特性

### 全屏 TUI（交互式终端默认模式）

在交互式终端中直接运行，自动进入全屏 TUI 模式：

```
$ go run ./cmd/harness9
```

**WelcomeBanner 欢迎页**（首屏）：

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

**对话页**（首次 Enter 后切换）：

```
  ▶ You: 帮我分析 main.go 里的 bug

  ◆ harness9:
    好的，我先读取文件...
    ✓ read_file(main.go) — 234ms
    发现第 42 行存在空指针解引用问题

  ⠼ 思考中...  bash(go test ./...)  [3.2s]
  model: gpt-4o-mini  │  mode: Default  │  ~/myproject
  › _
  enter 发送  / 技能命令  ↑↓ 滚动  ctrl+c 退出
```

- 流式输出逐 token 追加，实时显示推理过程
- 工具执行期间 spinner 动画 + 耗时计数
- Ctrl-C 中断正在运行的 Agent，再按一次退出 TUI
- 通过管道或 CI 调用时自动退回 CLI REPL 模式

详见 [TUI 交互界面实现原理](docs/核心功能/tui.md)。

### 交互式 CLI（管道 / CI 模式）

非 TTY 环境（管道、脚本、CI）自动退回 CLI REPL：

```
$ go run ./cmd/harness9
harness9 │ 输入 "exit" 或按 Ctrl-C 退出

harness9> 帮我分析 internal/engine/agent_loop.go 的结构
harness9> 列出目录下所有 Go 文件并统计行数
harness9> exit
再见！
```

详见 [CLI 使用指南](docs/核心功能/cli.md)。

### Agent Skills（按需加载的领域知识）

在项目根目录的 `skills/` 下放置子目录，每个子目录包含一个 `SKILL.md` 文件。Agent 启动时感知索引，按需加载全文。遵循 **Progressive Disclosure** 原则，System Prompt 始终精简：

```
your-project/
├── skills/
│   ├── refactor-guide/
│   │   └── SKILL.md         # 重构规范
│   └── testing-standards/
│       └── SKILL.md         # 测试标准
└── AGENTS.md                # 项目级规范（自动注入 System Prompt）
```

CLI 模式下支持斜杠命令直接激活技能，绕过 LLM 判断步骤：

```
harness9> /refactor-guide
harness9> /refactor-guide 清理 internal/engine/agent_loop.go
```

详见 [Agent Skills 设计原理](docs/核心功能/agent-skills.md)。

### 标准 ReAct 循环

每个 Turn 执行一次 LLM 调用（携带完整工具列表），工具结果作为 Observation 注入上下文，驱动下一轮推理：

```
Turn N:
  LLM(messages, tools) → Action（含工具调用时）
  → 并发执行工具 → Observation 注入 → Turn N+1

自然终止：模型不再发起工具调用 → 输出最终回复
```

详见 [Agent Loop 核心实现原理](docs/核心功能/agent-loop.md)。

### 并发工具执行

同一 Turn 中的多个工具调用**并发执行**，每个工具拥有独立超时控制：

```go
// 每个工具独立超时，互不影响
toolCtx, cancel = context.WithTimeout(ctx, e.toolTimeout)
results[idx] = e.registry.Execute(toolCtx, tc)
```

### 流式输出

支持 `Run`（阻塞）和 `RunStream`（流式）双模式，共享同一引擎配置：

```go
// 阻塞式
err := eng.Run(ctx, prompt)

// 流式：逐 token 消费
stream, _ := eng.RunStream(ctx, prompt)
for evt := range stream {
    switch evt.Type {
    case engine.EventActionDelta:
        fmt.Print(evt.Data.(string))
    case engine.EventDone:
        return
    }
}
```

### Short-Term Memory（会话历史持久化）

每次对话的消息历史自动持久化到 SQLite（`~/.harness9/sessions.db`），重启后可从中断处继续：

```
$ harness9
session: a1b2c3d4  msgs: 12         ← 状态栏实时显示当前会话 ID 和消息数

harness9> /new       # 开启全新会话
harness9> /resume    # 列出历史会话并选择恢复
```

**SlidingWindowCompactor** 在上下文超出窗口限制时自动压缩，始终保留系统提示和最新 N 条消息，并回溯对齐孤立的工具调用观察，避免 LLM API 报错。

详见 [Short-Term Memory 技术方案](docs/核心功能/short-term-memory.md)。

### 自愈能力

工具执行失败时，错误信息原样回传给 LLM，触发自动重试：

```go
// IsError=true 时，LLM 能看到错误原因并尝试自愈
ToolResult{ToolCallID: id, Output: "command not found: foo", IsError: true}
```

详见 [Tool Calling 工具调用系统](docs/核心功能/tool-calling.md)。

---

## 安装

### 一键安装（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/ZhangShenao/harness9/master/scripts/install.sh | bash
```

安装完成后验证：

```bash
harness9 --version
# harness9 v0.1.0
```

### 配置 API Key

```bash
export OPENAI_API_KEY="sk-..."

# 可选：切换模型
export LLM_MODEL="openai/gpt-4o"

# 可选：使用 OpenRouter 或其他兼容 API
# export OPENAI_BASE_URL="https://openrouter.ai/api/v1"
```

建议将 `export` 命令写入 `~/.zshrc` 或 `~/.bashrc`，避免每次重新设置。

### 使用

```bash
cd /your/project   # 进入你的项目目录
harness9           # 启动（自动以当前目录为 Agent 工作沙箱）
```

### 从源码构建（开发者）

需要 Go 1.25+：

```bash
git clone https://github.com/ZhangShenao/harness9
cd harness9
go run ./cmd/harness9
```

---

## 快速开始

### 环境要求

- OpenAI 或兼容 API Key（OpenRouter、Azure 等）

### 配置说明

harness9 启动时优先读取**工作目录（work_dir）**下的 `.env` 文件加载环境变量。  
如果同名变量已通过 `export` 或 shell 配置文件手动设置，**手动设置的值始终优先**，`.env` 文件不会覆盖它。

**优先级：** `export VAR=value`（手动设置）> `work_dir/.env` 文件

`.env` 文件示例：

```env
OPENAI_API_KEY=sk-...

# 可选：切换模型
LLM_MODEL=openai/gpt-4o-mini

# 可选：使用 OpenRouter 或其他兼容 API
# OPENAI_BASE_URL=https://openrouter.ai/api/v1
```

> **提示：** 将 `export OPENAI_API_KEY="sk-..."` 写入 `~/.zshrc` 或 `~/.bashrc` 即可跨项目全局生效，无需在每个项目目录放置 `.env`。

### 添加 Project Guidelines

在项目根目录放置 `AGENTS.md`，启动时自动注入 System Prompt：

```markdown
# 我的项目规范

## 技术栈
- Go 1.25、PostgreSQL 16

## 编码规范
- 所有函数必须有注释
- 禁止直接操作数据库，必须通过 Repository 层
```

### 添加 Skills

在 `skills/<name>/SKILL.md` 路径创建技能文件：

```bash
mkdir -p skills/refactor-guide
```

```markdown
# skills/refactor-guide/SKILL.md
---
name: refactor-guide
description: Use when refactoring Go code — explains team conventions
---

# 重构规范

1. 先运行 go vet，修复所有 warning
2. 保持函数不超过 50 行
...
```

CLI 模式下可用 `/refactor-guide` 直接激活该技能。

### 测试

```bash
go test ./...
go test -cover ./...
```

### 最小示例

```go
package main

import (
    "context"
    "log"

    "github.com/harness9/internal/engine"
    "github.com/harness9/internal/provider"
    "github.com/harness9/internal/tools"
)

func main() {
    workDir := "/your/project"

    p, err := provider.NewOpenAIProvider("gpt-4o-mini")
    if err != nil {
        log.Fatalf("init provider: %v", err)
    }

    registry := tools.NewRegistry()
    registry.Register(tools.NewBashTool(workDir))
    registry.Register(tools.NewReadFileTool(workDir))
    registry.Register(tools.NewWriteFileTool(workDir))
    registry.Register(tools.NewEditFileTool(workDir))

    eng := engine.NewAgentEngine(p, registry, workDir)

    if err := eng.Run(context.Background(), "帮我列出当前目录下的所有 Go 文件"); err != nil {
        log.Fatalf("run: %v", err)
    }
}
```

---

## 核心模块

| 模块 | 说明 | 状态 |
|------|------|:----:|
| **TUI** | 全屏 TUI（Bubbletea）：WelcomeBanner + 对话页双 Phase、流式输出、Spinner 动词轮换、Tab 补全、滚动 | ✅ |
| **Engine** | 标准 ReAct 主循环，阻塞 + 流式双模式 | ✅ |
| **Context** | System Prompt 结构化组装（基础 + AGENTS.md + Skills 索引） | ✅ |
| **Skills** | Skills 解析、索引、按需加载（`use_skill` 工具） | ✅ |
| **Provider** | LLM 统一接口，OpenAI / Anthropic 适配器 | ✅ |
| **Schema** | 跨组件共享的核心数据类型 | ✅ |
| **Tools** | 工具注册表 + 内置工具（bash / read_file / write_file / edit_file） | ✅ |
| **Env** | 零依赖 `.env` 配置加载器 | ✅ |

---

## 项目结构

```
harness9/
├── cmd/
│   └── harness9/
│       ├── main.go                  # 程序入口：TUI（TTY）/ CLI（管道）
│       ├── tui.go                   # TUI 核心：tuiModel struct、样式变量、Init、RunTUI
│       ├── tui_update.go            # Update 逻辑：事件处理、键盘、滚动、Tab 补全、Markdown 渲染
│       ├── tui_view.go              # View 渲染：renderConversation/ToolProgress/StatusBar/Input/Footer
│       ├── tui_banner.go            # WelcomeBanner：HARNESS9 ASCII Art + bannerContent()
│       ├── tui_test.go              # TUI Update 逻辑单元测试（45 个用例）
│       └── cli.go                   # 交互式 CLI REPL 实现
├── internal/
│   ├── engine/
│   │   ├── agent_loop.go            # 共享 runLoop + 阻塞式 Run + PromptBuilder 接口
│   │   ├── agent_loop_test.go       # 主循环单元测试
│   │   ├── stream.go                # 流式入口 RunStream + engine.Event 事件类型
│   │   └── stream_test.go           # 流式接口单元测试
│   ├── context/
│   │   ├── builder.go               # DefaultPromptBuilder（组装 System Prompt）
│   │   └── builder_test.go
│   ├── skills/
│   │   ├── skill.go                 # Skill 数据结构 + frontmatter 解析
│   │   ├── index.go                 # Index：Summary() + GetFullContent()
│   │   ├── loader.go                # LoadSkills(dir) → *Index
│   │   ├── use_skill_tool.go        # use_skill 工具（实现 tools.BaseTool）
│   │   └── skills_test.go           # Skills 系统单元测试（20 个测试用例）
│   ├── provider/
│   │   ├── interface.go             # LLMProvider 接口定义
│   │   ├── openai.go                # OpenAI 兼容 API 适配器
│   │   ├── anthropic.go             # Anthropic 兼容 API 适配器
│   │   ├── tool_call_accumulator.go # 流式工具调用累积器
│   │   └── providertest/mock.go     # 测试用确定性 Mock Provider
│   ├── schema/
│   │   ├── message.go               # Message、ToolCall、ToolResult、ToolDefinition
│   │   └── stream.go                # StreamChunk（Provider 层流式类型）
│   ├── tools/
│   │   ├── base.go                  # BaseTool 接口
│   │   ├── registry.go              # 工具注册中心
│   │   ├── safe_path.go             # 路径沙箱校验（防 Path Traversal）
│   │   ├── path_locker.go           # 路径级 RWMutex（并发文件操作保护）
│   │   ├── bash.go                  # bash 工具
│   │   ├── read_file.go             # read_file 工具
│   │   ├── write_file.go            # write_file 工具
│   │   └── edit_file.go             # edit_file 工具（多级模糊匹配）
│   ├── env/
│   │   ├── env.go                   # 零依赖 .env 加载器
│   │   └── env_test.go
│   └── logfmt/
│       ├── format.go                # 块状日志格式化
│       └── format_test.go
├── docs/
│   └── 核心功能/
│       ├── cli.md                   # CLI 使用指南（本文档）
│       ├── agent-skills.md          # Agent Skills 设计原理
│       ├── tui.md                   # TUI 交互界面实现原理
│       ├── agent-loop.md            # Agent Loop 核心实现原理
│       └── tool-calling.md          # Tool Calling 工具调用系统详解
├── skills/                          # 示例 Skills（可复制到你的项目中使用）
│   ├── go-coding-standards/
│   │   └── SKILL.md
│   ├── debugging-guide/
│   │   └── SKILL.md
│   └── architecture-overview/
│       └── SKILL.md
├── knowledge/                       # AI 技术动态知识库（采集 → 分析 → 文章）
├── .env.example                     # 环境变量配置模板
├── go.mod
├── AGENTS.md                        # 项目开发规范与上下文
└── CLAUDE.md -> AGENTS.md
```

---

## 文档索引

| 文档 | 内容 |
|------|------|
| [TUI 交互界面实现原理](docs/核心功能/tui.md) | Bubbletea 架构、布局、事件流、键盘交互、Context 传播 |
| [CLI 使用指南](docs/核心功能/cli.md) | 启动、环境变量、AGENTS.md、Skills 配置、常见问题 |
| [Agent Skills 设计原理](docs/核心功能/agent-skills.md) | Progressive Disclosure、frontmatter 规范、use_skill 工具 |
| [Agent Loop 核心实现原理](docs/核心功能/agent-loop.md) | 标准 ReAct 设计原理、PromptBuilder、流式架构 |
| [Tool Calling 工具调用系统](docs/核心功能/tool-calling.md) | 工具接口、并发模型、内置工具详解、扩展指南 |
| [Short-Term Memory 技术方案](docs/核心功能/short-term-memory.md) | SQLiteSession、SlidingWindowCompactor、TUI 集成、并发安全设计 |
| [AGENTS.md](AGENTS.md) | 项目开发规范、编码标准、架构决策 |

---

## 对标框架

| 框架 | 优势 | 与 harness9 的差异 |
|------|------|-------------------|
| Claude Agent SDK | 深度 Anthropic 集成 | 官方 SDK，仅支持 Anthropic；harness9 多 Provider，Go 原生 |
| OpenAI Agents SDK | 官方支持，生态完整 | Python 框架，Handoffs 多 Agent 机制；harness9 为 Go 原生单 Agent |
| OpenHarness | 显式循环设计清晰 | Python；harness9 Go 原生，含 IM 渠道适配层 |
| OpenCode | Plan/Build Agent 切换 | TypeScript；harness9 标准 ReAct，Go 原生，更轻量 |

---

## License

MIT
