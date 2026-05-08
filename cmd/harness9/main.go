package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/env"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

func main() {
	// 绑定工作路径
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("[main] 获取工作目录失败: %v", err)
	}

	// 加载环境变量
	if err := env.Load(filepath.Join(workDir, ".env")); err != nil {
		log.Fatalf("[main] 加载环境配置失败: %v", err)
	}

	// 指定 LLMProvider
	llm, err := provider.NewOpenAIProvider("openai/gpt-5.4-mini")
	if err != nil {
		log.Fatalf("[main] 创建 Provider 失败: %v", err)
	}

	// 创建ToolRegistry并注册Tools
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	// 创建Agent Engine，并关闭慢思考模式
	eng := engine.NewAgentEngine(llm, registry, workDir, false)

	prompt := `请帮我并行执行以下 3 个独立任务（你可以在同一轮调用多个工具）：
1. 读取 test_fixtures/greeting.txt 的内容
2. 读取 test_fixtures/info.txt 的内容
3. 运行命令：echo "hello from parallel world" && date

请一次性发出所有工具调用，不要分批执行。`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	useStream := len(os.Args) > 1 && os.Args[1] == "stream"

	if useStream {
		fmt.Println("=== 流式调用模式 ===")
		runStream(ctx, eng, prompt)
	} else {
		fmt.Println("=== 阻塞式调用模式 ===")
		runBlocking(ctx, eng, prompt)
	}
}

func runBlocking(ctx context.Context, eng *engine.AgentEngine, prompt string) {
	if err := eng.Run(ctx, prompt); err != nil {
		log.Fatalf("[main] 引擎异常退出: %v", err)
	}
}

func runStream(ctx context.Context, eng *engine.AgentEngine, prompt string) {
	stream, err := eng.RunStream(ctx, prompt)
	if err != nil {
		log.Fatalf("[main] RunStream 启动失败: %v", err)
	}

	for evt := range stream {
		switch evt.Type {
		case engine.EventThinkingDelta:
			fmt.Print(evt.Data.(string))
		case engine.EventActionDelta:
			fmt.Print(evt.Data.(string))
		case engine.EventToolStart:
			if tc, ok := evt.Data.(schema.ToolCall); ok {
				fmt.Printf("\n[tool-start] %s (%s)\n", tc.Name, tc.ID)
			}
		case engine.EventToolResult:
			if tr, ok := evt.Data.(schema.ToolResult); ok {
				fmt.Printf("\n[tool-result] %s\n", truncStr(tr.Output, 200))
			}
		case engine.EventDone:
			fmt.Println("\n[done]")
		case engine.EventError:
			fmt.Printf("\n[error] %v\n", evt.Data)
		}
	}
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
