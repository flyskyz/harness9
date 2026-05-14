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
    ┌──────────┐   ┌──────────┐   ┌──────────────────┐
    │ Provider │   │ Schema   │   │   Tool Registry  │
    │ (模型接口) │   │ (类型层)  │   │  (工具注册/调用)  │
    └──────────┘   └──────────┘   └──────────────────┘
          │                               │
    ┌─────┴────┐                    ┌────┴────┐
    ▼          ▼                    ▼         ▼
┌────────┐ ┌────────┐         ┌────────┐ ┌────────┐
│ OpenAI │ │Anthropic│        │  bash  │ │read /  │
│Provider│ │Provider │        │  Tool  │ │write   │
└────────┘ └────────┘         └────────┘ └────────┘
```

**Engine** 驱动 Agent 主循环：**推理 → 工具调用 → 观察 → 继续推理**

---

## 核心特性

### 全屏 TUI（交互式终端默认模式）

在交互式终端中直接运行，自动进入全屏 TUI 模式：

```
$ go run ./cmd/harness9
```

```
┌─────────────────────────────────────────────┐
│ ⬡ harness9   gpt-4o-mini · ~/myproject     │
├─────────────────────────────────────────────┤
│                                             │
│  ▶ You: 帮我分析 main.go 里的 bug           │
│                                             │
│  ◆ harness9:                               │
│    好的，我先读取文件...                    │
│    ✓ read_file(main.go) — 234ms            │
│    发现第 42 行存在空指针解引用问题         │
│                                             │
├─────────────────────────────────────────────┤
│  ⠼ bash(go test ./...)  [3.2s]             │
├─────────────────────────────────────────────┤
│  › _                                        │
└─────────────────────────────────────────────┘
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

在 `WORK_DIR/skills/` 下放置子目录，每个子目录包含一个 `SKILL.md` 文件。Agent 启动时感知索引，按需加载全文。遵循 **Progressive Disclosure** 原则，System Prompt 始终精简：

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

### 飞书 Bot 接入（可选）

通过 `--feishu` 标志启动飞书 Bot 模式，WebSocket 长连接接收消息，实时推送思考进度：

```bash
go run ./cmd/harness9 --feishu
```

```
🤔 思考中...
🔧 调用工具：bash
✅ bash（123ms）
<最终回复>
```

详见 [IM 渠道接入详解](docs/核心功能/im-channel.md)。

### 自愈能力

工具执行失败时，错误信息原样回传给 LLM，触发自动重试：

```go
// IsError=true 时，LLM 能看到错误原因并尝试自愈
ToolResult{ToolCallID: id, Output: "command not found: foo", IsError: true}
```

详见 [Tool Calling 工具调用系统](docs/核心功能/tool-calling.md)。

---

## 快速开始

### 环境要求

- Go 1.25+
- OpenAI 或兼容 API Key（OpenRouter、Azure 等）

### 安装 & 启动

```bash
# 克隆项目
git clone https://github.com/harness9/harness9
cd harness9

# 复制并填写 API 配置
cp .env.example .env
# 编辑 .env，至少填入 OPENAI_API_KEY

# 启动（交互式终端自动进入 TUI，无需飞书配置）
go run ./cmd/harness9
```

### 配置说明

`.env` 文件：

```env
OPENAI_API_KEY=sk-...

# 可选：指定 Agent 工作目录（默认为进程 cwd，通常无需设置）
# WORK_DIR=/Users/yourname/myproject

# 可选：切换模型
LLM_MODEL=openai/gpt-4o-mini

# 可选：使用 OpenRouter 或其他兼容 API
# OPENAI_BASE_URL=https://openrouter.ai/api/v1
```

### 添加 Project Guidelines

在 `WORK_DIR` 根目录放置 `AGENTS.md`，启动时自动注入 System Prompt：

```markdown
# 我的项目规范

## 技术栈
- Go 1.25、PostgreSQL 16

## 编码规范
- 所有函数必须有注释
- 禁止直接操作数据库，必须通过 Repository 层
```

### 添加 Skills

在 `WORK_DIR/skills/<name>/SKILL.md` 路径创建技能文件：

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

### 启动飞书 Bot（可选）

需额外配置飞书凭证：

```bash
# .env 中添加
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=xxx

# 启动
go run ./cmd/harness9 --feishu
```

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
| **TUI** | 全屏 TUI（Bubbletea）：流式输出 + spinner + 单行输入，TTY 自动检测 | ✅ |
| **Engine** | 标准 ReAct 主循环，阻塞 + 流式双模式 | ✅ |
| **Context** | System Prompt 结构化组装（基础 + AGENTS.md + Skills 索引） | ✅ |
| **Skills** | Skills 解析、索引、按需加载（`use_skill` 工具） | ✅ |
| **Provider** | LLM 统一接口，OpenAI / Anthropic 适配器 | ✅ |
| **Schema** | 跨组件共享的核心数据类型 | ✅ |
| **Tools** | 工具注册表 + 内置工具（bash / read_file / write_file / edit_file） | ✅ |
| **IMChannel** | IM 平台统一适配接口（IMChannel / Session 契约） | ✅ |
| **Feishu** | 飞书 WebSocket 长连接接入 + 进度消息推送 | ✅ |
| **Env** | 零依赖 `.env` 配置加载器 | ✅ |

---

## 项目结构

```
harness9/
├── cmd/
│   └── harness9/
│       ├── main.go                  # 程序入口：TUI（TTY）/ CLI（管道）/ 飞书 Bot（--feishu）
│       ├── tui.go                   # 全屏 TUI 实现（Bubbletea Elm Architecture）
│       ├── tui_test.go              # TUI Update 逻辑单元测试（37 个用例）
│       ├── cli.go                   # 交互式 CLI REPL 实现
│       ├── bot.go                   # Bot 编排层（IMChannel × AgentEngine，事件流映射）
│       └── bot_test.go              # Bot 事件映射单元测试
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
│   ├── imchannel/
│   │   ├── channel.go               # IMChannel / Session 接口定义
│   │   └── feishu/
│   │       ├── client.go            # 飞书 WebSocket 长连接适配器
│   │       ├── session.go           # 飞书 Session 实现（思考/工具进度/最终回复）
│   │       └── session_test.go      # 辅助函数单元测试
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
│       ├── tool-calling.md          # Tool Calling 工具调用系统详解
│       └── im-channel.md            # IM 渠道接入详解（飞书 Bot 实现原理）
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
| [IM 渠道接入详解](docs/核心功能/im-channel.md) | 飞书 Bot 实现原理、接口契约、事件映射规则 |
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
