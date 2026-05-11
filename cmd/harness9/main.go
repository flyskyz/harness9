// Command harness9 是 harness9 框架的命令行入口。
//
// 使用方式：
//
//	# 阻塞模式（默认）
//	harness9 "请帮我列出当前目录下的所有 .go 文件"
//
//	# 流式模式
//	harness9 -stream "请帮我列出当前目录下的所有 .go 文件"
//
//	# 通过标准输入提供 prompt
//	echo "请帮我列出当前目录下的所有 .go 文件" | harness9
//	echo "请帮我列出当前目录下的所有 .go 文件" | harness9 -stream
//
// 程序读取当前工作目录作为沙箱根目录，并从 .env 加载 OPENAI_API_KEY / OPENAI_BASE_URL
// 等敏感配置。环境变量优先于 .env 文件，方便容器化部署。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/env"
	"github.com/harness9/internal/provider"
	"github.com/harness9/internal/schema"
	"github.com/harness9/internal/tools"
)

func main() {
	streamFlag := flag.Bool("stream", false, "使用流式输出模式（默认阻塞模式）")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [-stream] [prompt]\n\n"+
			"  prompt 可作为参数传入，也可通过 stdin 传入。\n"+
			"  示例:\n"+
			"    %s \"列出当前目录的 .go 文件\"\n"+
			"    %s -stream \"列出当前目录的 .go 文件\"\n"+
			"    echo \"列出当前目录的 .go 文件\" | %s\n",
			os.Args[0], os.Args[0], os.Args[0], os.Args[0])
	}
	flag.Parse()

	// 绑定工作路径
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("[main] 获取工作目录失败: %v", err)
	}

	// 加载环境变量
	if err := env.Load(filepath.Join(workDir, ".env")); err != nil {
		log.Fatalf("[main] 加载环境配置失败: %v", err)
	}

	// 解析 prompt：优先取命令行参数，其次读 stdin
	prompt, err := readPrompt(flag.Args())
	if err != nil {
		log.Fatalf("[main] 读取 prompt 失败: %v", err)
	}
	if prompt == "" {
		flag.Usage()
		os.Exit(2)
	}

	// 指定 LLMProvider
	llm, err := provider.NewOpenAIProvider("openai/gpt-5.4-mini")
	if err != nil {
		log.Fatalf("[main] 创建 Provider 失败: %v", err)
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
			log.Fatalf("[main] 注册工具 %s 失败: %v", tool.Name(), err)
		}
	}

	// 创建 Agent Engine（默认开启 Two-Stage ReAct）
	eng := engine.NewAgentEngine(llm, registry, workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if *streamFlag {
		fmt.Println("=== 流式调用模式 ===")
		runStream(ctx, eng, prompt)
	} else {
		fmt.Println("=== 阻塞式调用模式 ===")
		runBlocking(ctx, eng, prompt)
	}
}

// readPrompt 解析 prompt 来源：优先合并命令行参数，若为空则从 stdin 读取（在被管道驱动时）。
func readPrompt(args []string) (string, error) {
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " ")), nil
	}

	// 检测 stdin 是否被重定向（非 TTY），是则读取，否则返回空
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("stat stdin: %w", err)
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		// stdin 仍连接终端，未提供 prompt
		return "", nil
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
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
