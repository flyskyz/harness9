// Package memory provides short-term memory management for harness9: session history persistence and context compaction.
package memory

import (
	"encoding/json"
	"fmt"

	"github.com/harness9/internal/schema"
)

// charsPerToken is the character-to-token ratio used for estimation.
// This matches HermesAgent, DeepAgents, and OpenCode's approach:
// simple, dependency-free, and slightly conservative.
const charsPerToken = 4

// EstimateTokens estimates the total token count for a message slice
// using character count / 4. Includes content, tool call arguments, and tool call IDs.
func EstimateTokens(msgs []schema.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
		for _, tc := range m.ToolCalls {
			total += len(tc.ID) + len(tc.Name) + len(tc.Arguments)
		}
		total += len(m.ToolCallID)
	}
	return total / charsPerToken
}

// EstimateToolTokens estimates token usage for a slice of tool definitions.
// Tool schemas can consume 20-30K+ tokens with many tools — this must be
// included in preflight token checks.
func EstimateToolTokens(tools []schema.ToolDefinition) int {
	total := 0
	for _, t := range tools {
		total += len(t.Name) + len(t.Description)
		if t.InputSchema != nil {
			if b, err := json.Marshal(t.InputSchema); err == nil {
				total += len(b)
			}
			// marshalling failure silently → conservative estimate (0 for schema)
		}
	}
	return total / charsPerToken
}

// FormatTokenCount formats a token count as a human-readable string.
// Examples: 45200 → "45.2K", 1200000 → "1.2M", 500 → "500"
func FormatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
