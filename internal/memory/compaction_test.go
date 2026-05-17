package memory_test

import (
	"testing"

	"github.com/harness9/internal/memory"
	"github.com/harness9/internal/schema"
)

func msgs(roles ...string) []schema.Message {
	result := make([]schema.Message, len(roles))
	for i, r := range roles {
		result[i] = schema.Message{Role: schema.Role(r), Content: r + "_content"}
	}
	return result
}

func msgWithToolCallID(id string) schema.Message {
	return schema.Message{Role: schema.RoleUser, ToolCallID: id, Content: "obs"}
}

func msgAssistantWithTool() schema.Message {
	return schema.Message{
		Role:      schema.RoleAssistant,
		ToolCalls: []schema.ToolCall{{ID: "tc1", Name: "bash"}},
	}
}

func TestSlidingWindow_NoCompaction(t *testing.T) {
	c := &memory.SlidingWindowCompactor{MaxMessages: 10}
	input := msgs("system", "user", "assistant")
	got := c.Compact(input)
	if len(got) != 3 {
		t.Fatalf("want 3 msgs, got %d", len(got))
	}
}

func TestSlidingWindow_BasicCompaction(t *testing.T) {
	// [system, u1, a1, u2, a2, u3] MaxMessages=4
	// startIdx = 6-4+1 = 3 → msgs[3]=u2 (no tool_call_id) → keep
	// result: [system, u2, a2, u3]
	input := []schema.Message{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleAssistant, Content: "a1"},
		{Role: schema.RoleUser, Content: "u2"},
		{Role: schema.RoleAssistant, Content: "a2"},
		{Role: schema.RoleUser, Content: "u3"},
	}
	c := &memory.SlidingWindowCompactor{MaxMessages: 4}
	got := c.Compact(input)
	if len(got) != 4 {
		t.Fatalf("want 4 msgs, got %d: %+v", len(got), got)
	}
	if got[0].Role != schema.RoleSystem {
		t.Error("first msg must be system")
	}
	if got[1].Content != "u2" {
		t.Errorf("want u2, got %q", got[1].Content)
	}
}

func TestSlidingWindow_BacktrackOrphanObservation(t *testing.T) {
	// [system, u1, assistant(tool_calls), obs(tool_call_id=tc1), a2, u3] MaxMessages=4
	// startIdx = 6-4+1 = 3 → msgs[3]=obs has ToolCallID → backtrack
	// startIdx=2 → assistant has tool_calls, ToolCallID="" → stop
	// result: [system, assistant(tool_calls), obs, a2, u3] = 5 msgs
	input := []schema.Message{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "u1"},
		msgAssistantWithTool(),
		msgWithToolCallID("tc1"),
		{Role: schema.RoleAssistant, Content: "a2"},
		{Role: schema.RoleUser, Content: "u3"},
	}
	c := &memory.SlidingWindowCompactor{MaxMessages: 4}
	got := c.Compact(input)
	if len(got) != 5 {
		t.Fatalf("want 5 msgs (backtracked), got %d: %+v", len(got), got)
	}
	if got[0].Role != schema.RoleSystem {
		t.Error("first msg must be system")
	}
	if len(got[1].ToolCalls) == 0 {
		t.Error("second msg must be assistant with tool_calls")
	}
	if got[2].ToolCallID != "tc1" {
		t.Error("third msg must be the observation")
	}
}

func TestSlidingWindow_DefaultMaxMessages(t *testing.T) {
	// MaxMessages=0 should use default 100
	c := &memory.SlidingWindowCompactor{MaxMessages: 0}
	input := msgs("system", "user", "assistant")
	got := c.Compact(input)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}

func TestSlidingWindow_MaxMessagesOne(t *testing.T) {
	// MaxMessages=1 means only system fits; treated as min 2
	c := &memory.SlidingWindowCompactor{MaxMessages: 1}
	input := []schema.Message{
		{Role: schema.RoleSystem, Content: "sys"},
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleAssistant, Content: "a1"},
	}
	got := c.Compact(input) // must not panic
	if got[0].Role != schema.RoleSystem {
		t.Error("first msg must be system")
	}
}

func TestSlidingWindow_EmptyInput(t *testing.T) {
	c := &memory.SlidingWindowCompactor{MaxMessages: 5}
	got := c.Compact(nil)
	if len(got) != 0 {
		t.Errorf("want 0, got %d", len(got))
	}
}

func TestSlidingWindow_NoSystemMessage(t *testing.T) {
	c := &memory.SlidingWindowCompactor{MaxMessages: 2}
	input := []schema.Message{
		{Role: schema.RoleUser, Content: "u1"},
		{Role: schema.RoleUser, Content: "u2"},
		{Role: schema.RoleUser, Content: "u3"},
	}
	// No system message: should return unchanged
	got := c.Compact(input)
	if len(got) != 3 {
		t.Errorf("want 3 (unchanged), got %d", len(got))
	}
}
