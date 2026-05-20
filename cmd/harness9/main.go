// Command harness9 是 harness9 框架的主入口。
//
// 在交互式终端（TTY）中运行时自动进入全屏 TUI 模式；
// 通过管道或 CI 调用时退回交互式 CLI REPL 模式。
// Agent 的工具沙箱根目录固定为启动时的进程工作目录（cwd）。
//
// 环境变量（可通过 .env 文件或系统环境变量提供）：
//
//	OPENAI_API_KEY     LLM Provider API Key（必填）
//	OPENAI_BASE_URL    自定义 OpenAI 兼容 API 地址（可选）
//	LLM_MODEL          模型名称（默认：openai/gpt-4o-mini）
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/charmbracelet/x/term"

	harctx "github.com/harness9/internal/context"
	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/env"
	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/memory"
	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/skills"
	"github.com/harness9/internal/tools"
)

// version 由 goreleaser ldflags 在发布构建时注入；本地开发构建显示 "dev"。
var version = "dev"

func main() {
	// upgrade 子命令在 flag 解析前处理，避免与 flag 系统冲突。
	if len(os.Args) > 1 && os.Args[1] == "upgrade" {
		if err := RunUpgrade(version); err != nil {
			fmt.Fprintf(os.Stderr, "升级失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	versionMode := flag.Bool("version", false, "打印版本号并退出")
	flag.Parse()

	if *versionMode {
		fmt.Println("harness9 " + version)
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("获取工作目录失败: %v", err)))
	}

	if err := env.Load(filepath.Join(cwd, ".env")); err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("加载环境配置失败: %v", err)))
	}

	workDir := cwd

	// 加载 Skills（workdir/skills/，目录不存在时静默返回空 Index）
	skillsIndex, err := skills.LoadSkills(filepath.Join(workDir, "skills"))
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("加载 skills 失败: %v", err)))
	}

	// 构建 System Prompt（基础 prompt + AGENTS.md + skills 索引）
	promptBuilder := harctx.NewPromptBuilder(workDir, skillsIndex).WithTodoEnabled(true)

	modelName := os.Getenv("LLM_MODEL")
	if modelName == "" {
		modelName = "openai/gpt-4o-mini"
	}
	llm, err := provider.NewOpenAIProvider(modelName)
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("创建 Provider 失败: %v", err)))
	}

	registry := tools.NewRegistry()
	todoStore := planning.NewTodoStore()
	for _, tool := range []tools.BaseTool{
		tools.NewReadFileTool(workDir),
		tools.NewWriteFileTool(workDir),
		tools.NewBashTool(workDir),
		tools.NewEditFileTool(workDir),
		skills.NewUseSkillTool(skillsIndex),
		tools.NewTodoWriteTool(todoStore),
	} {
		if err := registry.Register(tool); err != nil {
			log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("注册工具 %s 失败: %v", tool.Name(), err)))
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("获取 home 目录失败: %v", err)))
	}
	mgr, err := memory.NewManager(filepath.Join(homeDir, ".harness9", "sessions.db"))
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("初始化 Memory Manager 失败: %v", err)))
	}
	defer mgr.Close()

	sess, err := mgr.NewSession(ctx)
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("创建会话失败: %v", err)))
	}

	modelLimits := provider.GetModelLimits(modelName)
	// SummarizationCompactor 使用同一 LLM 生成摘要，内置 TokenBudgetCompactor 作为错误回退。
	compactor := memory.NewSummarizationCompactor(llm, modelLimits.ContextTokens,
		memory.WithTodoInjector(todoStore),
	)

	eng := engine.NewAgentEngine(llm, registry, workDir,
		engine.WithPromptBuilder(promptBuilder),
		engine.WithSession(sess),
		engine.WithCompactor(compactor),
		engine.WithContextWindow(modelLimits.ContextTokens),
		engine.WithTodoStore(todoStore),
	)

	if term.IsTerminal(os.Stdin.Fd()) {
		log.Print(logfmt.FormatMsg("main", fmt.Sprintf("harness9 TUI 启动 │ workDir=%s", workDir)))
		if err := RunTUI(ctx, eng, mgr, sess, skillsIndex, todoStore, workDir, modelName); err != nil {
			log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("TUI 退出: %v", err)))
		}
	} else {
		log.Print(logfmt.FormatMsg("main", fmt.Sprintf("harness9 CLI 启动 │ workDir=%s", workDir)))
		RunCLI(ctx, eng, skillsIndex)
	}
	log.Print(logfmt.FormatMsg("main", "harness9 正常退出"))
}
