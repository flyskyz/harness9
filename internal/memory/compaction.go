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
	max := c.MaxMessages
	if max <= 0 {
		max = 100
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
