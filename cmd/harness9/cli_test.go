package main

import (
	"context"
	"strings"
	"testing"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/provider/providertest"
	"github.com/harness9/internal/tools"
)

func newTestEngine(t *testing.T) *engine.AgentEngine {
	t.Helper()
	return engine.NewAgentEngine(providertest.NewMock(), tools.NewRegistry(), t.TempDir())
}

// TestRunCLI_ExitCommand 验证输入 "exit" 时 runCLI 正常返回，不阻塞。
func TestRunCLI_ExitCommand(t *testing.T) {
	eng := newTestEngine(t)
	input := strings.NewReader("exit\n")
	runCLI(context.Background(), eng, input)
}

// TestRunCLI_QuitCommand 验证输入 "quit" 时 runCLI 正常返回。
func TestRunCLI_QuitCommand(t *testing.T) {
	eng := newTestEngine(t)
	input := strings.NewReader("quit\n")
	runCLI(context.Background(), eng, input)
}

// TestRunCLI_EOFExits 验证 stdin 关闭（EOF）时 runCLI 正常返回。
func TestRunCLI_EOFExits(t *testing.T) {
	eng := newTestEngine(t)
	input := strings.NewReader("") // 空输入 → 立即 EOF
	runCLI(context.Background(), eng, input)
}

// TestRunCLI_ContextCancel 验证 ctx 取消时 runCLI 正常返回。
func TestRunCLI_ContextCancel(t *testing.T) {
	eng := newTestEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	input := strings.NewReader("some input\n")
	runCLI(ctx, eng, input)
}
