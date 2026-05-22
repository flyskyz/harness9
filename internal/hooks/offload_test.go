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
	dir := t.TempDir()
	h := hooks.NewOffloadHook(dir, "sess1", hooks.WithThreshold(100))
	tc := schema.ToolCall{Name: "bash", ID: "tc1"}
	result := schema.ToolResult{ToolCallID: "tc1", Output: "short output"}

	got := h.AfterExecute(context.Background(), tc, result)
	if got.Output != "short output" {
		t.Errorf("output should be unchanged below threshold, got %q", got.Output)
	}
	// No file should be created
	entries, _ := os.ReadDir(filepath.Join(dir, "sess1"))
	if len(entries) != 0 {
		t.Error("no file should be written below threshold")
	}
}

func TestOffloadHook_AboveThreshold_WritesFile(t *testing.T) {
	dir := t.TempDir()
	h := hooks.NewOffloadHook(dir, "sess1", hooks.WithThreshold(10), hooks.WithPreviewLines(2))

	large := strings.Repeat("line\n", 50) // 250 chars, well above threshold=10
	tc := schema.ToolCall{Name: "bash", ID: "tc2"}
	result := schema.ToolResult{ToolCallID: "tc2", Output: large}

	got := h.AfterExecute(context.Background(), tc, result)

	// Output should mention the file path
	expectedPath := filepath.Join(dir, "sess1", "tc2.txt")
	if !strings.Contains(got.Output, expectedPath) {
		t.Errorf("output should contain file path %q, got %q", expectedPath, got.Output)
	}

	// File should exist with original content
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("offload file not created: %v", err)
	}
	if string(content) != large {
		t.Error("offload file content should match original output")
	}

	// Output should contain preview (2 lines)
	if !strings.Contains(got.Output, "预览") {
		t.Error("output should contain preview marker")
	}
}

func TestOffloadHook_ExcludedTools_NoOffload(t *testing.T) {
	dir := t.TempDir()
	h := hooks.NewOffloadHook(dir, "sess1", hooks.WithThreshold(1))

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
	// Use a non-existent root that can't be created (file instead of dir)
	f, err := os.CreateTemp("", "not-a-dir")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	// Use file path as base dir — MkdirAll will fail because it's a file
	h := hooks.NewOffloadHook(f.Name(), "sess1", hooks.WithThreshold(1))
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
