// Command harness9 是 harness9 框架的飞书 Bot 服务入口。
//
// 启动后通过飞书 WebSocket 长连接持续监听私聊消息，
// 每条消息触发一次独立的 AgentEngine 循环，执行结果回复到飞书。
//
// 必要的环境变量（可通过 .env 文件或系统环境变量提供）：
//
//	FEISHU_APP_ID      飞书应用 App ID
//	FEISHU_APP_SECRET  飞书应用 App Secret
//	OPENAI_API_KEY     LLM Provider API Key
//
// 可选环境变量：
//
//	WORK_DIR           Agent 工具的沙箱根目录（默认：进程工作目录）
//	OPENAI_BASE_URL    自定义 OpenAI 兼容 API 地址
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/env"
	"github.com/harness9/internal/imchannel/feishu"
	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/tools"
)

func main() {
	// 先取进程工作目录，用于定位 .env 文件
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("获取工作目录失败: %v", err)))
	}

	// 加载 .env（不存在时静默跳过，由系统环境变量提供配置）
	if err := env.Load(filepath.Join(cwd, ".env")); err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("加载环境配置失败: %v", err)))
	}

	// WORK_DIR 优先用 .env / 系统变量，未配置则回退到进程工作目录
	workDir := os.Getenv("WORK_DIR")
	if workDir == "" {
		workDir = cwd
	}

	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Fatal(logfmt.FormatMsg("main", "缺少飞书配置：FEISHU_APP_ID 或 FEISHU_APP_SECRET 未设置"))
	}

	// 指定 LLM Provider，模型名称通过 LLM_MODEL 环境变量配置，未设置时使用默认值。
	modelName := os.Getenv("LLM_MODEL")
	if modelName == "" {
		modelName = "openai/gpt-4o-mini"
	}
	llm, err := provider.NewOpenAIProvider(modelName)
	if err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("创建 Provider 失败: %v", err)))
	}

	// 创建 ToolRegistry 并注册内置工具
	registry := tools.NewRegistry()
	for _, tool := range []tools.BaseTool{
		tools.NewReadFileTool(workDir),
		tools.NewWriteFileTool(workDir),
		tools.NewBashTool(workDir),
		tools.NewEditFileTool(workDir),
	} {
		if err := registry.Register(tool); err != nil {
			log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("注册工具 %s 失败: %v", tool.Name(), err)))
		}
	}

	// 创建 AgentEngine
	eng := engine.NewAgentEngine(llm, registry, workDir)

	// 创建飞书 Channel 并组装 Server
	ch := feishu.NewChannel(appID, appSecret)
	srv := NewServer(ch, eng)

	// 监听系统信号，支持 Ctrl-C 优雅退出
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Print(logfmt.FormatMsg("main", fmt.Sprintf("harness9 飞书 Bot 启动 │ workDir=%s appID=%s", workDir, appID)))
	if err := srv.Start(ctx); err != nil {
		log.Fatal(logfmt.FormatMsg("main", fmt.Sprintf("Server 退出: %v", err)))
	}
	log.Print(logfmt.FormatMsg("main", "harness9 正常退出"))
}
