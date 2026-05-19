// Package schema — 流式数据类型。
// 本文件定义 Provider 层的流式增量类型，用于 GenerateStream 方法通过 channel 逐 chunk 传递 LLM 响应增量。
package schema

// StreamChunkType 枚举了 LLM 流式响应的增量 chunk 类型。
type StreamChunkType string

const (
	// StreamChunkTextDelta 文本增量（token-by-token），引擎层映射为 EventThinkingDelta 或 EventActionDelta。
	StreamChunkTextDelta StreamChunkType = "text_delta"

	// StreamChunkDone 流式响应结束，Message 字段携带完整的响应 Message（含 ToolCalls）。
	StreamChunkDone StreamChunkType = "done"

	// StreamChunkError 流式过程发生错误，Error 字段包含错误描述。
	StreamChunkError StreamChunkType = "error"
)

// StreamChunk 是 LLM 流式响应的单个增量单元。
//
//	Type == text_delta → Delta 有效
//	Type == done       → Message、Usage 有效
//	Type == error      → Error 有效
type StreamChunk struct {
	Type    StreamChunkType `json:"type"`
	Delta   string          `json:"delta,omitempty"`
	Message *Message        `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
	// Usage 在 StreamChunkDone 中由 Provider 填充，包含本次调用的实际 token 用量。
	Usage *Usage `json:"usage,omitempty"`
}
