// Package memory 提供 harness9 的短期记忆管理：会话历史持久化与上下文压缩。
package memory

import "github.com/harness9/internal/schema"

// Compactor 在将历史消息注入 LLM 上下文前进行裁剪，防止超出上下文窗口。
// 接口设计允许后续扩展 TokenBudgetCompactor、LLMSummarizationCompactor 等策略。
type Compactor interface {
	Compact(msgs []schema.Message) []schema.Message
}

// SlidingWindowCompactor 保留最近 MaxMessages 条消息（System Prompt 固定在首位）。
// MaxMessages 含 system 消息本身；0 或负数时使用默认值 100。
type SlidingWindowCompactor struct {
	MaxMessages int
}

// Compact 对 msgs 进行滑动窗口裁剪，返回裁剪后的切片。
//
// 边界修正：若窗口第一条消息是工具执行结果（ToolCallID != ""），
// 向前回溯直到找到配对的 assistant 工具请求消息，保证上下文完整。
func (c *SlidingWindowCompactor) Compact(msgs []schema.Message) []schema.Message {
	if len(msgs) == 0 || msgs[0].Role != schema.RoleSystem {
		return msgs
	}

	max := c.MaxMessages
	if max <= 0 {
		max = 100
	}
	if max < 2 {
		max = 2 // must hold at least system + one turn
	}
	if len(msgs) <= max {
		return msgs
	}

	// startIdx 为窗口中第一条非 system 消息的索引（msgs[0] 始终是 system）
	startIdx := len(msgs) - max + 1

	// 边界修正：回溯孤立的 Observation 消息
	for startIdx > 1 && msgs[startIdx].ToolCallID != "" {
		startIdx--
	}

	result := make([]schema.Message, 0, len(msgs)-startIdx+1)
	result = append(result, msgs[0]) // system 始终保留
	result = append(result, msgs[startIdx:]...)
	return result
}

// TokenBudgetCompactor 基于 token 预算（字符数÷4 估算）而非消息条数进行上下文裁剪。
// 这是 harness9 推荐的默认 Compactor，能感知实际 token 用量，适配不同模型的上下文窗口。
//
// 压缩策略：
//  1. token 总数 ≤ MaxTokens 时直接返回（无需压缩）
//  2. 从中间删除旧消息，保留 system 消息和最近 MinTailMessages 条消息
//  3. 压缩后执行双向孤立消息修复（HermesAgent 模式）
type TokenBudgetCompactor struct {
	// MaxTokens 是压缩目标上限（tokens 估算值）。通常设为模型 context window 的 80%。
	MaxTokens int
	// MinTailMessages 是无论预算如何都必须保留的最近非 system 消息数量（默认 6）。
	MinTailMessages int
}

// NewTokenBudgetCompactor 创建针对指定 context window 大小的 TokenBudgetCompactor。
// MaxTokens 自动设为 contextWindow 的 80%。
func NewTokenBudgetCompactor(contextWindow int) *TokenBudgetCompactor {
	return &TokenBudgetCompactor{
		MaxTokens:       contextWindow * 80 / 100,
		MinTailMessages: 6,
	}
}

// Compact 压缩消息历史至 MaxTokens 预算内，始终保留 system 消息和最近 MinTailMessages 条消息。
func (c *TokenBudgetCompactor) Compact(msgs []schema.Message) []schema.Message {
	if len(msgs) == 0 {
		return msgs
	}
	if EstimateTokens(msgs) <= c.maxTokens() {
		return msgs
	}
	// 必须以 system 消息开头
	if msgs[0].Role != schema.RoleSystem {
		return msgs
	}

	minTail := c.minTail()
	rest := msgs[1:] // non-system messages

	if len(rest) <= minTail {
		return msgs
	}

	tail := rest[len(rest)-minTail:]

	// Shrink head one message at a time until budget is satisfied or head is empty.
	for headEnd := len(rest) - minTail; headEnd > 0; headEnd-- {
		candidate := make([]schema.Message, 0, 1+headEnd+minTail)
		candidate = append(candidate, msgs[0])
		candidate = append(candidate, rest[:headEnd]...)
		candidate = append(candidate, tail...)
		if EstimateTokens(candidate) <= c.maxTokens() {
			return repairOrphanedToolPairs(candidate)
		}
	}

	// Head fully removed — system + tail only.
	result := make([]schema.Message, 0, 1+len(tail))
	result = append(result, msgs[0])
	result = append(result, tail...)
	return repairOrphanedToolPairs(result)
}

func (c *TokenBudgetCompactor) maxTokens() int {
	if c.MaxTokens <= 0 {
		return 160_000 // 200K * 80% conservative default
	}
	return c.MaxTokens
}

func (c *TokenBudgetCompactor) minTail() int {
	if c.MinTailMessages <= 0 {
		return 6
	}
	return c.MinTailMessages
}

// repairOrphanedToolPairs executes bidirectional tool-pair integrity repair after compaction.
//  1. Removes orphaned user tool_result messages with no matching assistant tool_call
//  2. Inserts stub user tool_result for any assistant tool_call missing its response
//
// Required for Anthropic Messages API compliance: tool_call / tool_result must appear in pairs.
func repairOrphanedToolPairs(msgs []schema.Message) []schema.Message {
	// Collect IDs of all tool_calls issued by assistant.
	calledIDs := make(map[string]bool)
	for _, m := range msgs {
		if m.Role == schema.RoleAssistant {
			for _, tc := range m.ToolCalls {
				calledIDs[tc.ID] = true
			}
		}
	}
	// Collect IDs that already have a tool_result.
	resultIDs := make(map[string]bool)
	for _, m := range msgs {
		if m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}

	result := make([]schema.Message, 0, len(msgs))
	for _, m := range msgs {
		// Drop orphaned tool_result (no matching tool_call).
		if m.ToolCallID != "" && !calledIDs[m.ToolCallID] {
			continue
		}
		result = append(result, m)
		// Insert stub for tool_call missing its result.
		if m.Role == schema.RoleAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if !resultIDs[tc.ID] {
					result = append(result, schema.Message{
						Role:       schema.RoleUser,
						Content:    "[tool result unavailable: context was compacted]",
						ToolCallID: tc.ID,
					})
				}
			}
		}
	}
	return result
}
