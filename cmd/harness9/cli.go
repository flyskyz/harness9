package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/harness9/internal/engine"
)

// RunCLI 启动交互式 REPL，从 os.Stdin 读取用户输入。
// 直到 ctx 取消、用户输入 exit/quit 或 EOF 时返回。
func RunCLI(ctx context.Context, eng *engine.AgentEngine) {
	runCLI(ctx, eng, os.Stdin)
}

// runCLI 是 RunCLI 的可测试内核，允许注入任意 io.Reader 作为输入源。
func runCLI(ctx context.Context, eng *engine.AgentEngine, in io.Reader) {
	fmt.Println("harness9 │ 输入 \"exit\" 或按 Ctrl-C 退出")
	fmt.Println()

	reader := bufio.NewReader(in)
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n再见！")
			return
		default:
		}

		fmt.Print("harness9> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF 或管道关闭，正常退出
			fmt.Println("\n再见！")
			return
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			fmt.Println("再见！")
			return
		}

		if err := eng.Run(ctx, input); err != nil {
			if ctx.Err() != nil {
				fmt.Println("\n再见！")
				return
			}
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		}
	}
}
