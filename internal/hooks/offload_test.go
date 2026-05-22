package hooks_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harness9/internal/hooks"
	"github.com/harness9/internal/schema"
)

func TestOffloadHook_BelowThreshold_NoOffload(t *testing.T) {
	workDir := t.TempDir()
	h := hooks.NewOffloadHook(workDir, "sess1", hooks.WithThreshold(100))
	tc := schema.ToolCall{Name: "bash", ID: "tc1"}
	result := schema.ToolResult{ToolCallID: "tc1", Output: "short output"}

	got := h.AfterExecute(context.Background(), tc, result)
	if got.Output != "short output" {
		t.Errorf("output should be unchanged below threshold, got %q", got.Output)
	}
	// No offload directory should be created
	entries, _ := os.ReadDir(filepath.Join(workDir, ".harness9", "tool_results", "sess1"))
	if len(entries) != 0 {
		t.Error("no file should be written below threshold")
	}
}

func TestOffloadHook_AboveThreshold_WritesFile(t *testing.T) {
	workDir := t.TempDir()
	h := hooks.NewOffloadHook(workDir, "sess1", hooks.WithThreshold(10), hooks.WithPreviewLines(2))

	large := strings.Repeat("line\n", 50) // 250 chars, well above threshold=10
	tc := schema.ToolCall{Name: "bash", ID: "tc2"}
	result := schema.ToolResult{ToolCallID: "tc2", Output: large}

	got := h.AfterExecute(context.Background(), tc, result)

	// File should exist at {workDir}/.harness9/tool_results/sess1/tc2.txt
	expectedFile := filepath.Join(workDir, ".harness9", "tool_results", "sess1", "tc2.txt")
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("offload file not created: %v", err)
	}
	if string(content) != large {
		t.Error("offload file content should match original output")
	}

	// Output should contain the relative path (accessible via read_file sandbox)
	expectedRelPath := filepath.Join(".harness9", "tool_results", "sess1", "tc2.txt")
	if !strings.Contains(got.Output, expectedRelPath) {
		t.Errorf("output should contain relative path %q, got %q", expectedRelPath, got.Output)
	}

	// Output should contain preview (2 lines)
	if !strings.Contains(got.Output, "预览") {
		t.Error("output should contain preview marker")
	}
}

func TestOffloadHook_EmptyToolCallID_NoOffload(t *testing.T) {
	workDir := t.TempDir()
	h := hooks.NewOffloadHook(workDir, "sess1", hooks.WithThreshold(1))

	large := strings.Repeat("x", 100)
	tc := schema.ToolCall{Name: "bash", ID: ""} // empty ID
	result := schema.ToolResult{Output: large}

	got := h.AfterExecute(context.Background(), tc, result)
	if got.Output != large {
		t.Error("empty tc.ID should skip offload and return original output unchanged")
	}
}

func TestOffloadHook_ExcludedTools_NoOffload(t *testing.T) {
	workDir := t.TempDir()
	h := hooks.NewOffloadHook(workDir, "sess1", hooks.WithThreshold(1))

	large := strings.Repeat("x", 100)
	for _, toolName := range []string{"read_file", "write_file", "edit_file"} {
		tc := schema.ToolCall{Name: toolName, ID: "tc-" + toolName}
		result := schema.ToolResult{Output: large}
		got := h.AfterExecute(context.Background(), tc, result)
		if got.Output != large {
			t.Errorf("tool %q should not be offloaded, output changed", toolName)
		}
	}
}

func TestOffloadHook_WriteFailure_ReturnsOriginal(t *testing.T) {
	workDir := t.TempDir()
	// Block MkdirAll by placing a file where the .harness9 directory would be
	harnessPath := filepath.Join(workDir, ".harness9")
	f, err := os.Create(harnessPath)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	h := hooks.NewOffloadHook(workDir, "sess1", hooks.WithThreshold(1))
	tc := schema.ToolCall{Name: "bash", ID: "tc-fail"}
	original := strings.Repeat("x", 100)
	result := schema.ToolResult{Output: original}

	got := h.AfterExecute(context.Background(), tc, result)
	if got.Output != original {
		t.Error("on write failure, original output should be returned unchanged")
	}
}

func TestOffloadHook_BeforeExecute_Noop(t *testing.T) {
	h := hooks.NewOffloadHook(t.TempDir(), "sess1")
	tc := schema.ToolCall{Name: "bash", ID: "tc-before"}
	ctx := context.Background()
	newCtx, err := h.BeforeExecute(ctx, tc)
	if err != nil {
		t.Fatalf("BeforeExecute should be no-op, got error: %v", err)
	}
	if newCtx != ctx {
		t.Error("BeforeExecute should return the same context")
	}
}
