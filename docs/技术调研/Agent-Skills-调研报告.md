# Agent Skills 功能调研报告

> 调研日期：2026-05-14
> 调研范围：DeepAgents、OpenHarness、OpenCode、OpenClaw、HermesAgent、Claude Agent SDK、OpenAI Agent SDK
> 目标：深度分析主流框架对 Agent Skills 的设计与实现，为 harness9 提供实现参考

> **⚠️ 信息来源说明**：本报告由后台调研 Agent 实时访问各框架 GitHub 仓库与官方文档后生成结构化摘要，再由主会话根据摘要落盘。部分代码示例（如 DeepAgents 的 `SkillsBackend` 接口、OpenCode 的 Effect 类型示例）系根据调研结论重建，用于阐释设计模式，并非直接复制自源码。如需核实原始代码，请参考各节中标注的仓库链接。

---

## 1. 调研背景

Agent Skills（技能）是近年 Agent Harness 框架中涌现的一类核心抽象，旨在解决如下问题：

- **Tools**（工具）负责执行具体动作（调用 API、读写文件），但无法封装"如何处理某类任务"的**高阶知识**
- **System Prompt** 虽可注入指令，但随项目增长会变得臃肿难维护
- **Skills** 填补了两者之间的空白：以声明式文件定义"处理某类任务的完整知识体"，按需加载注入上下文

本报告基于对上述 7 个框架的 GitHub 源码与官方文档的直接调研。

---

## 2. 各框架 Skills 实现分析

### 2.1 DeepAgents（LangChain）

**仓库**：https://github.com/langchain-ai/deepagents

**核心实现**：

DeepAgents 通过 `Backend` 接口抽象技能存储后端，实现了文件系统、内存、远程 HTTP 三种存储的统一访问：

```python
# skills/backend.py — 存储后端接口
class SkillsBackend(Protocol):
    async def list_skills(self) -> list[SkillMetadata]: ...
    async def load_skill(self, name: str) -> Skill: ...
```

**Skills 定义格式**（YAML frontmatter + Markdown）：

```markdown
---
name: code-review
description: Performs thorough code review with security and quality checks
triggers:
  - "review"
  - "check my code"
args:
  focus:
    type: string
    description: Review focus area (security/quality/performance)
    default: quality
---

# Code Review Skill

When performing a code review, follow these steps:
1. Check for security vulnerabilities (OWASP Top 10)
2. Verify error handling completeness
...
```

**加载机制**：
1. 启动时扫描 `.agents/skills/` 目录（可配置）
2. 解析每个子目录下的 `SKILL.md` 文件
3. 仅将 `name` + `description` 注入 System Prompt（渐进式披露）
4. LLM 判定技能适用后，完整正文按需加载

**YAML 解析**：最严格，强制符合 [AgentSkills 开放规范](https://agentskills.io/specification)，未通过 schema 验证的 Skills 文件会被拒绝加载并记录警告日志。

**调度机制**：双模——LLM 自主激活（意图匹配 `triggers` 字段）+ 用户显式 `/skill-name` 命令。

---

### 2.2 OpenHarness（HKUDS）

**仓库**：https://github.com/HKUDS/OpenHarness

**核心实现**：

OpenHarness 在 Skills 基础上增加了**插件体系**，每个 Skill 可声明所依赖的插件（Plugin），由框架负责在执行前安装或激活：

```markdown
---
name: web-research
description: Conducts comprehensive web research on any topic
plugins:
  - browser-use
  - serper-search
disable-model-invocation: false   # 控制 LLM 是否可自主触发此技能
---
```

**关键特性**：
- `disable-model-invocation: true` 字段可将某 Skill 设为"仅限用户显式触发"，防止 LLM 误激活
- Skill 正文支持 `{{variable}}` 模板变量替换，在加载时从运行时上下文注入
- 插件依赖在 Skill 加载时验证，缺失插件会降级为警告（不阻断启动）

**目录结构**：

```
.openharness/
└── skills/
    ├── web-research/
    │   ├── SKILL.md
    │   └── examples/           # 示例对话，帮助 LLM 理解激活时机
    └── data-analysis/
        ├── SKILL.md
        └── templates/          # Prompt 模板片段
```

---

### 2.3 OpenCode（Anomaly）

**仓库**：https://github.com/anomalyco/opencode

**核心实现**：

OpenCode 是少数支持**远程 URL 拉取技能**的框架，通过 `index.json` 清单文件实现技能分发：

```typescript
// skills/loader.ts — 远程技能加载
interface SkillIndex {
  version: string;
  skills: Array<{
    name: string;
    description: string;
    url: string;           // 远程 SKILL.md 的直接链接
    checksum: string;      // SHA256 校验，防止篡改
  }>;
}
```

**Effect 类型系统**：OpenCode 使用 [Effect-TS](https://effect.website/) 保证并发安全：

```typescript
// 技能加载是纯函数式的 Effect，可安全并发
const loadSkill = (name: string): Effect.Effect<Skill, SkillNotFoundError> =>
  pipe(
    findSkillFile(name),
    Effect.flatMap(parseSkillMarkdown),
    Effect.flatMap(validateSkillSchema)
  );
```

**加载路径优先级**（高到低）：
1. 项目本地：`<workDir>/.agents/skills/`
2. 用户级：`~/.agents/skills/`
3. 远程索引：配置文件中声明的 `skillRegistries[]` URL 列表

---

### 2.4 OpenClaw（OpenClaw）

**仓库**：https://github.com/openclaw/openclaw

**核心实现**：

OpenClaw 在 Skills 元数据中增加了**依赖声明**和**Agent 白名单**两大特性：

```markdown
---
name: deploy-service
description: Deploys a microservice to Kubernetes cluster
metadata:
  openclaw:
    dependencies:
      brew: [kubectl, helm]
      npm: []
      go: [sigs.k8s.io/kustomize/kustomize/v5@v5.3.0]
    allowed-agents:             # 只有列表内的 Agent 可使用此技能
      - devops-agent
      - platform-agent
    require-confirmation: true  # 执行前要求用户确认
---
```

**依赖安装流程**：
1. Skill 首次加载时检查依赖是否满足
2. 缺失依赖提示用户手动安装（不自动安装，避免权限问题）
3. 依赖满足后将 Skill 标记为 `ready`，否则标记为 `degraded`

**并发调度**：同一 Turn 内多个 Skill 的元数据注入是并发进行的；正文加载是串行的（避免 context 过长）。

---

### 2.5 HermesAgent（NousResearch）

**仓库**：https://github.com/NousResearch/hermes-agent

**核心实现**：

HermesAgent 在 Skills 实现上最关注**性能优化**，核心是**热重载不破坏 prefix cache**：

```python
# skills/registry.py — 热重载实现
class SkillRegistry:
    def reload(self, name: str) -> None:
        # 仅重载变化的技能，保持其他技能的 prefix cache 有效
        old_skill = self._skills.get(name)
        new_skill = self._load_from_disk(name)
        if old_skill and old_skill.content_hash == new_skill.content_hash:
            return  # 内容未变，跳过（保护 cache）
        self._skills[name] = new_skill
        self._invalidate_prompt_cache(name)  # 精准失效
```

**模板变量替换**：

```markdown
---
name: analyze-repo
---

You are analyzing the repository at `${HERMES_SKILL_DIR}/../..`.
Current date: ${CURRENT_DATE}
Working directory: ${WORK_DIR}
```

内置变量：`${HERMES_SKILL_DIR}`（技能文件所在目录）、`${WORK_DIR}`、`${CURRENT_DATE}`、`${AGENT_NAME}`。

**使用量追踪**：每次技能被激活时记录使用日志（技能名、激活时间、触发方式），用于分析哪些技能最有价值。

---

### 2.6 Claude Agent SDK（Anthropic）

**文档**：https://code.claude.com/docs/en/agent-sdk/overview

**核心实现**：

Claude Code 的 Skills 实现是渐进式披露（Progressive Disclosure）最完整的案例，分三层加载：

```
Layer 1（始终加载）：name + description → 注入 System Prompt 末尾
Layer 2（按需加载）：SKILL.md 正文 → 当 LLM 判定技能适用时注入
Layer 3（按需加载）：references/ + scripts/ → 技能执行过程中按需引用
```

**目录规范**：

```
.claude/
└── skills/                     # 项目级（优先级最高）
    └── <skill-name>/
        ├── SKILL.md             # 必需：技能定义
        ├── references/          # 可选：参考文档、规范文件
        └── scripts/             # 可选：辅助脚本

~/.claude/skills/               # 用户级（优先级次之）
```

**SKILL.md 编写规范**（Claude 文档推荐）：
- frontmatter `description` 字段用第三人称描述（"Use when..."），供 LLM 判断激活时机
- 正文使用命令式语气（"When X, do Y"）
- 避免在 SKILL.md 中重复 System Prompt 已有的内容

**Skill 调度实现**（`Skill` tool）：
```
用户消息 → PromptBuilder 注入技能列表 → LLM 决策 → 调用 Skill tool → 加载正文 → 继续执行
```

---

### 2.7 OpenAI Agent SDK

**文档**：https://developers.openai.com/api/docs/guides/agents

**Skills 等价机制**：

OpenAI Agent SDK 没有独立的 Skills 概念，通过以下机制实现等价能力：

| Skills 能力 | OpenAI 等价实现 |
|------------|----------------|
| 技能声明与描述 | Agent `instructions` 字段 |
| 技能分发与调度 | `Handoff`（将任务转交给专门 Agent）|
| 技能间协作 | Multi-Agent 编排（`Runner.run` with handoffs）|
| 外部知识注入 | MCP Server（提供动态上下文） |

```python
# OpenAI SDK 等价实现：用专门 Agent 替代 Skill
code_review_agent = Agent(
    name="code-reviewer",
    instructions="""You are an expert code reviewer. When reviewing code:
    1. Check for security vulnerabilities...
    2. Verify error handling...
    """,
    tools=[read_file, run_tests]
)

# 主 Agent 通过 handoff 将任务转交
main_agent = Agent(
    name="assistant",
    handoffs=[code_review_agent],
)
```

**评价**：OpenAI 方案的优点是简单统一（一切皆 Agent），缺点是粒度粗——独立 Agent 比轻量 Skill 消耗更多资源，且无法"组合"多个知识模块。

---

## 3. 对比分析

### 3.1 Skills 定义方式

| 框架 | 定义方式 | 格式 |
|------|---------|------|
| DeepAgents | `SKILL.md`（每个技能一个目录） | YAML frontmatter + Markdown |
| OpenHarness | `SKILL.md` + 插件声明 | YAML frontmatter + Markdown |
| OpenCode | `SKILL.md`（支持远程拉取） | YAML frontmatter + Markdown |
| OpenClaw | `SKILL.md` + 依赖/权限声明 | YAML frontmatter + Markdown |
| HermesAgent | `SKILL.md` + 模板变量 | YAML frontmatter + Markdown |
| Claude Code | `SKILL.md` 三层结构 | YAML frontmatter + Markdown |
| OpenAI SDK | Agent `instructions` 字段 | 纯字符串（无结构化格式）|

**结论**：`SKILL.md`（YAML frontmatter + Markdown）已成为事实标准，6/7 框架采用。

---

### 3.2 Skills 加载机制

```
加载路径优先级（共识）：
  项目本地（.agents/skills/ 或 .claude/skills/）
  > 用户级（~/.agents/skills/）
  > 全局内置
  > 远程注册表（仅 OpenCode 支持）

加载时机（共识）：
  - 元数据（name + description）：启动时全量加载，常驻内存
  - 正文内容：按需懒加载（LLM 判定技能适用后）
  - 辅助文件（references/scripts）：执行过程中按需读取
```

---

### 3.3 Skills 调度机制

| 触发方式 | 支持框架 | 实现细节 |
|---------|---------|---------|
| LLM 自主激活 | 全部（OpenAI 除外） | 技能描述作为 LLM 选择依据；`triggers` 字段辅助 |
| 用户显式命令 | 全部 | `/skill-name` 斜杠命令 |
| 规则匹配 | DeepAgents、OpenClaw | `triggers` 字段关键词匹配作为快速路径 |
| 禁止 LLM 激活 | OpenHarness | `disable-model-invocation: true` |
| 需用户确认 | OpenClaw | `require-confirmation: true` |

---

### 3.4 Skills 执行方式

**核心共识**：Skills 本质是**提示增强（Prompt Augmentation）**，不是独立可执行代码。

```
Skills 执行流程（标准模式）：
  1. SKILL.md 正文注入当前对话 context
  2. LLM 根据注入的指令，自主决定调用哪些 Tools 执行操作
  3. Tools 的执行结果作为 Observation 返回给 LLM
  4. LLM 根据技能指令评估结果，决定是否继续或输出最终答案
```

**与 Tools 的核心区别**：

| 维度 | Tools | Skills |
|------|-------|--------|
| 本质 | 可执行函数（代码） | 知识文档（提示） |
| 调用方 | LLM 通过 function call 调用 | 注入 context，LLM 遵循其中的指令 |
| 执行者 | 框架/运行时 | LLM（根据指令选择 Tools 执行） |
| 参数 | 强类型 JSON Schema | 自然语言描述 |
| 组合 | 工具间相互独立 | 一个 Skill 可以调用多个 Tools |
| 复用 | 按函数名调用 | 按文件路径加载 |
| 维护 | 修改代码 | 修改 Markdown 文档 |

---

## 4. 设计模式提炼

### 4.1 文件系统即注册表（Filesystem as Registry）

```
skills/
├── code-review/
│   └── SKILL.md
├── debug/
│   └── SKILL.md
└── deploy/
    ├── SKILL.md
    └── references/
        └── k8s-config.yaml
```

技能通过文件系统组织，无需显式注册，扫描目录即发现所有技能。

### 4.2 渐进式披露（Progressive Disclosure）

```
System Prompt = base_instructions + skill_catalog
               （仅包含 name + description，控制 token 消耗）

当 LLM 决定使用某技能时：
  → 加载 SKILL.md 正文，注入当前 context
  → LLM 在完整指令下执行操作
```

### 4.3 双触发模式（Dual Trigger）

```
用户输入 → 解析是否为 /skill-name 格式
  ├── 是 → 直接加载对应技能正文（不经过 LLM 判断）
  └── 否 → 交由 LLM 根据技能描述自主判断是否激活
```

### 4.4 精准缓存失效（Precise Cache Invalidation）

```
技能变更时：
  1. 计算新 SKILL.md 的 content hash
  2. 与旧 hash 对比
  3. 仅在内容真正变化时失效对应技能的 prefix cache
  4. 其他技能的 cache 保持有效
```

---

## 5. 对 harness9 实现 Skills 功能的建议

### 5.1 推荐架构

新建 `internal/skills/` 包，结构如下：

```go
internal/skills/
├── types.go       // Skill、SkillMetadata 类型定义
├── loader.go      // 文件系统扫描 + YAML 解析
├── registry.go    // 技能注册表（线程安全）
└── prompt.go      // SkillsPromptBuilder（实现 PromptBuilder 接口）
```

### 5.2 类型定义

```go
// types.go

// SkillMetadata 是技能的元数据，从 SKILL.md frontmatter 解析
type SkillMetadata struct {
    Name        string   `yaml:"name"`
    Description string   `yaml:"description"`
    Triggers    []string `yaml:"triggers,omitempty"`
}

// Skill 是完整的技能定义
type Skill struct {
    Metadata    SkillMetadata
    Content     string  // SKILL.md 正文（frontmatter 之后的 Markdown）
    ContentHash string  // SHA256，用于缓存失效判断
    SourcePath  string  // 文件路径，用于调试
}
```

### 5.3 加载机制

```go
// loader.go

// 扫描路径优先级（高到低）
var defaultSearchPaths = []string{
    ".agents/skills",   // 项目标准路径
    ".claude/skills",   // Claude 生态兼容路径
    "skills",           // 简化路径
}

// LoadSkills 扫描 workDir 下的所有技能目录
func LoadSkills(workDir string) ([]*Skill, error) {
    var skills []*Skill
    for _, rel := range defaultSearchPaths {
        dir := filepath.Join(workDir, rel)
        found, err := scanSkillDir(dir)
        if err != nil {
            continue // 目录不存在时跳过
        }
        skills = append(skills, found...)
    }
    return deduplicateByName(skills), nil // 高优先级路径的同名技能覆盖低优先级
}
```

### 5.4 PromptBuilder 集成

```go
// prompt.go

// SkillsPromptBuilder 在 System Prompt 末尾注入技能目录
type SkillsPromptBuilder struct {
    base     engine.PromptBuilder // 原有 PromptBuilder（装饰器模式）
    registry *Registry
}

func (b *SkillsPromptBuilder) BuildSystemPrompt(ctx context.Context) string {
    base := b.base.BuildSystemPrompt(ctx)
    catalog := b.buildSkillCatalog()
    if catalog == "" {
        return base
    }
    return base + "\n\n" + catalog
}

func (b *SkillsPromptBuilder) buildSkillCatalog() string {
    skills := b.registry.List()
    if len(skills) == 0 {
        return ""
    }
    var sb strings.Builder
    sb.WriteString("## Available Skills\n\n")
    for _, s := range skills {
        fmt.Fprintf(&sb, "- **%s**: %s\n", s.Metadata.Name, s.Metadata.Description)
    }
    sb.WriteString("\nUse the `invoke_skill` tool to activate a skill when it applies.")
    return sb.String()
}
```

### 5.5 斜杠命令支持（飞书 Bot / CLI REPL）

在 `cmd/harness9/bot.go` 的消息处理层解析斜杠命令：

```go
// 在 handleMessage 中检测 /skill-name 格式
if strings.HasPrefix(text, "/") {
    skillName := strings.TrimPrefix(text, "/")
    if skill := skillRegistry.Get(skillName); skill != nil {
        // 将技能正文作为用户 prompt 的前缀注入
        text = skill.Content + "\n\n" + text
    }
}
```

### 5.6 内置示例技能

在项目根目录提供 `skills/` 目录，内置 2-3 个示例技能，帮助用户理解格式：

```
skills/
├── code-review/
│   └── SKILL.md    # 代码审查技能
└── debug/
    └── SKILL.md    # 系统调试技能
```

### 5.7 实现优先级

| 阶段 | 任务 | 预计工作量 |
|------|------|---------|
| P0 | `types.go` + `loader.go`（文件系统扫描 + YAML 解析） | 1天 |
| P0 | `registry.go`（线程安全注册表） | 半天 |
| P0 | `SkillsPromptBuilder`（System Prompt 注入） | 半天 |
| P1 | 斜杠命令支持（`/skill-name`） | 半天 |
| P1 | 内置示例技能（code-review、debug） | 半天 |
| P2 | 热重载（文件变更监听） | 1天 |
| P2 | 技能正文按需懒加载（`invoke_skill` tool） | 1天 |

---

## 6. 结论

主流框架在 Agent Skills 设计上已形成高度共识：**以 `SKILL.md` 文件系统声明技能、以渐进式披露控制 token 消耗、以双触发模式兼顾灵活性与确定性**。

harness9 已有 `PromptBuilder` 接口和 `WithPromptBuilder` Option，天然适合通过装饰器模式接入 Skills 能力，无需改动 engine 核心逻辑。建议以 P0 任务为起点，在 2 天内完成核心实现，后续逐步迭代热重载和按需加载等高级特性。
