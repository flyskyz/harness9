package memory_test

import (
	"testing"

	"github.com/harness9/internal/memory"
	"github.com/harness9/internal/schema"
)

func TestEstimateTokens_Empty(t *testing.T) {
	got := memory.EstimateTokens(nil)
	if got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestEstimateTokens_ContentOnly(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "abcd"},     // 4 chars → 1 token
		{Role: schema.RoleUser, Content: "abcdefgh"},   // 8 chars → 2 tokens
	}
	got := memory.EstimateTokens(msgs)
	if got != 3 {
		t.Fatalf("want 3, got %d", got)
	}
}

func TestEstimateTokens_WithToolCalls(t *testing.T) {
	msgs := []schema.Message{
		{
			Role: schema.RoleAssistant,
			ToolCalls: []schema.ToolCall{
				// "t1"=2 + "bash"=4 + `{"command":"ls"}`=16 = 22 chars → 5 tokens (int div)
				{ID: "t1", Name: "bash", Arguments: []byte(`{"command":"ls"}`)},
			},
		},
	}
	got := memory.EstimateTokens(msgs)
	if got != 5 {
		t.Fatalf("want 5, got %d", got)
	}
}

func TestEstimateTokens_WithToolCallID(t *testing.T) {
	msgs := []schema.Message{
		// "tcid"=4 + "result"=6 = 10 chars → 2 tokens
		{Role: schema.RoleUser, ToolCallID: "tcid", Content: "result"},
	}
	got := memory.EstimateTokens(msgs)
	if got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestEstimateToolTokens_Empty(t *testing.T) {
	got := memory.EstimateToolTokens(nil)
	if got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestEstimateToolTokens_Basic(t *testing.T) {
	tools := []schema.ToolDefinition{
		// "bash"=4 + "run a shell command"=19 = 23 chars → 5 tokens
		{Name: "bash", Description: "run a shell command"},
	}
	got := memory.EstimateToolTokens(tools)
	if got != 5 {
		t.Fatalf("want 5, got %d", got)
	}
}

func TestFormatTokenCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1.0K"},
		{45200, "45.2K"},
		{999999, "1000.0K"}, // %.1f rounds 999.999 → 1000.0
		{1000000, "1.0M"},
		{1200000, "1.2M"},
	}
	for _, tc := range cases {
		got := memory.FormatTokenCount(tc.n)
		if got != tc.want {
			t.Errorf("FormatTokenCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
