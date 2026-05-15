// Command harness9 是 harness9 框架的主入口。
//
// 默认以交互式 CLI REPL 模式运行；传入 --feishu 标志则启动飞书 Bot 服务。
//
// 环境变量（可通过 .env 文件或系统环境变量提供）：
//
//	OPENAI_API_KEY     LLM Provider API Key（必填）
//	WORK_DIR           Agent 工具的沙箱根目录（默认：进程工作目录）
//	OPENAI_BASE_URL    自定义 OpenAI 兼容 API 地址（可选）
//	LLM_MODEL          模型名称（默认：openai/gpt-4o-mini）
//
// 飞书模式额外需要：
//
//	FEISHU_APP_ID      飞书应用 App ID
//	FEISHU_APP_SECRET  飞书应用 App Secret
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
	"github.com/harness9/internal/imchannel/feishu"
	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/skills"
	"github.com/harness9/internal/tools"
)

// version 由 goreleaser ldflags 在发布构建时注入；本地开发构建显示 "dev"。
var version = "dev"

func main() {
	versionMode := flag.Bool("version", false, "打印版本号并退出")
	feishuMode := flag.Bool("feishu", false, "启动飞书 Bot 模式（需配置 FEISHU_APP_ID / FEISHU_APP_SECRET）")
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

	workDir := os.Getenv("WORK_DIR")
	if workDir == "" {
		workDir = cwd
	}

	// 加载 Skills（workdir/skills/，目录不存在时静默返回空 Index）
	skillsIndex, err := skills.LoadSkills(filepath.Join(workDir, "skills"))
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("加载 skills 失败: %v", err)))
	}

	// 构建 System Prompt（基础 prompt + AGENTS.md + skills 索引）
	promptBuilder := harctx.NewPromptBuilder(workDir, skillsIndex)

	modelName := os.Getenv("LLM_MODEL")
	if modelName == "" {
		modelName = "openai/gpt-4o-mini"
	}
	llm, err := provider.NewOpenAIProvider(modelName)
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("创建 Provider 失败: %v", err)))
	}

	registry := tools.NewRegistry()
	for _, tool := range []tools.BaseTool{
		tools.NewReadFileTool(workDir),
		tools.NewWriteFileTool(workDir),
		tools.NewBashTool(workDir),
		tools.NewEditFileTool(workDir),
		skills.NewUseSkillTool(skillsIndex),
	} {
		if err := registry.Register(tool); err != nil {
			log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("注册工具 %s 失败: %v", tool.Name(), err)))
		}
	}

	eng := engine.NewAgentEngine(llm, registry, workDir,
		engine.WithPromptBuilder(promptBuilder),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *feishuMode {
		appID := os.Getenv("FEISHU_APP_ID")
		appSecret := os.Getenv("FEISHU_APP_SECRET")
		if appID == "" || appSecret == "" {
			log.Fatal(logfmt.FormatMsg("main", "缺少飞书配置：FEISHU_APP_ID 或 FEISHU_APP_SECRET 未设置"))
		}
		log.Print(logfmt.FormatMsg("main", fmt.Sprintf("harness9 飞书 Bot 启动 │ workDir=%s appID=%s", workDir, appID)))
		ch := feishu.NewChannel(appID, appSecret)
		srv := NewServer(ch, eng)
		if err := srv.Start(ctx); err != nil {
			log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("Server 退出: %v", err)))
		}
	} else if term.IsTerminal(os.Stdin.Fd()) {
		log.Print(logfmt.FormatMsg("main", fmt.Sprintf("harness9 TUI 启动 │ workDir=%s", workDir)))
		if err := RunTUI(ctx, eng, skillsIndex, workDir, modelName); err != nil {
			log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("TUI 退出: %v", err)))
		}
	} else {
		log.Print(logfmt.FormatMsg("main", fmt.Sprintf("harness9 CLI 启动 │ workDir=%s", workDir)))
		RunCLI(ctx, eng, skillsIndex)
	}
	log.Print(logfmt.FormatMsg("main", "harness9 正常退出"))
}
