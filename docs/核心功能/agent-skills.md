# Agent Skills 技术方案

## 1. 设计原理：Progressive Disclosure

Skills 系统遵循 **Progressive Disclosure（渐进式披露）** 原则：

- **启动时**：只将 skills 的 `name` + `description` 索引注入 System Prompt，不加载全文
- **运行时**：LLM 根据用户任务判断是否需要某个 skill，通过调用 `use_skill` 工具按需加载全文

这样避免了将所有 skill 全文一次性注入 System Prompt 导致的 Token 膨胀，保持上下文窗口的高效利用。

## 2. 目录结构

用户在 `workdir/skills/` 下放置 `.md` 文件，harness9 启动时自动加载：

```
{workdir}/
└── skills/
    ├── go-refactor.md    # 自定义 skill
    ├── testing-guide.md
    └── deploy-guide.md
```

## 3. Skill 文件格式

每个 skill 是一个标准 Markdown 文件，开头包含 YAML frontmatter：

```markdown
---
name: go-refactor
description: Use when refactoring Go code — team conventions and patterns
trigger: "refactor, clean up, restructure, simplify"
---

# Go 重构指南

## 重构前必做

1. 运行 `go vet ./...` 确认无静态分析错误
2. 运行 `go test ./...` 确认测试全部通过
3. 查看 git diff 确认修改范围
```

### frontmatter 字段说明

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | **是** | skill 唯一标识（供 `use_skill` 工具调用） |
| `description` | string | **是** | 简短描述，注入 System Prompt 索引，帮助 LLM 判断何时使用 |
| `trigger` | string | 否 | 触发关键词，仅作文档说明，不做自动匹配 |

## 4. System Prompt 注入效果

harness9 启动后，System Prompt 末尾会附加 skills 索引：

```
## Available Skills

Use the `use_skill` tool to load the full content of any skill when needed.

- go-refactor: Use when refactoring Go code — team conventions and patterns
- testing-guide: Use when writing or reviewing tests
- deploy-guide: Use when deploying to production
```

## 5. LLM 调用 use_skill 工具

LLM 在判断需要某个 skill 的完整内容时，会发起工具调用：

```json
{
  "name": "use_skill",
  "arguments": {
    "skill_name": "go-refactor"
  }
}
```

工具返回该 skill 文件 frontmatter 之后的完整 body 内容，LLM 随后基于此内容指导任务执行。

## 6. 模块实现

| 模块 | 文件 | 职责 |
|------|------|------|
| `skills.Skill` | `internal/skills/skill.go` | 数据结构 + frontmatter 解析 |
| `skills.Index` | `internal/skills/index.go` | 索引摘要 + 懒加载全文 |
| `skills.LoadSkills` | `internal/skills/loader.go` | 扫描目录构建 Index |
| `skills.UseSkillTool` | `internal/skills/use_skill_tool.go` | `use_skill` 工具实现 |
| `context.DefaultPromptBuilder` | `internal/context/builder.go` | 组装 System Prompt |

## 7. 错误处理

| 场景 | 行为 |
|------|------|
| `skills/` 目录不存在 | 返回空 Index，静默跳过 |
| skill 文件缺少 `name` 或 `description` | 跳过该文件，打印 warn 日志 |
| `use_skill` 调用不存在的 skill | 返回包含可用名称列表的错误信息，LLM 可自愈 |
| `AGENTS.md` 不存在 | PromptBuilder 跳过该段落 |

## 8. CLI 启动方式

```bash
# 默认 CLI 模式（交互式 REPL）
cd /your/project
harness9

# 飞书 Bot 模式（需配置 FEISHU_APP_ID / FEISHU_APP_SECRET）
harness9 --feishu
```
