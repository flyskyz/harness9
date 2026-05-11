// Provider 层流式响应辅助：跨厂商共享的工具调用累积器。
//
// OpenAI 和 Anthropic 的流式 API 都会把单个工具调用的字段拆成多个 chunk 发送：
//
//	首个 chunk      — 携带 ID、Name（标识工具调用开始）
//	后续若干 chunk  — 携带参数 JSON 的部分片段（input_json_delta / function.arguments）
//
// 消费方需按 Index 把这些增量合并为完整的 schema.ToolCall。两个 Provider 此前各自
// 维护了功能完全相同的累积器（openaiToolCallAccumulator / anthropicToolCallAccumulator），
// 本文件抽出统一实现。
package provider

import (
	"strings"

	"github.com/harness9/internal/schema"
)

// toolCallAccumulator 缓存单个工具调用在流式过程中分片到达的字段。
// Arguments 使用 strings.Builder 增量拼接，避免反复内存分配。
type toolCallAccumulator struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// toolCallAccumulators 按 Index 组织的累积器集合，供 OpenAI/Anthropic 流式 Provider 复用。
// 同一流中可能同时存在多个并行工具调用（Parallel Tool Calling），各自通过 Index 隔离。
type toolCallAccumulators map[int]*toolCallAccumulator

// newToolCallAccumulators 创建空的累积器集合。
func newToolCallAccumulators() toolCallAccumulators {
	return make(toolCallAccumulators)
}

// get 返回 idx 对应的累积器，不存在时按需创建并写入集合。
func (a toolCallAccumulators) get(idx int) *toolCallAccumulator {
	if acc, ok := a[idx]; ok {
		return acc
	}
	acc := &toolCallAccumulator{index: idx}
	a[idx] = acc
	return acc
}

// start 在流式响应首次出现某工具调用时记录 ID 和 Name。
// 重复调用相同 idx 会覆盖旧值（理论上不会发生，由 Provider 保证只在 *_start 时调用）。
func (a toolCallAccumulators) start(idx int, id, name string) {
	acc := a.get(idx)
	acc.id = id
	acc.name = name
}

// appendArgs 把参数 JSON 的部分片段追加到 idx 对应累积器的缓冲区。
func (a toolCallAccumulators) appendArgs(idx int, partial string) {
	a.get(idx).args.WriteString(partial)
}

// finalize 按 Index 升序重组累积结果为 ToolCall 列表，供 StreamChunkDone 携带。
// 返回 nil 表示流中没有工具调用（不应作为错误处理）。
func (a toolCallAccumulators) finalize() []schema.ToolCall {
	if len(a) == 0 {
		return nil
	}
	result := make([]schema.ToolCall, 0, len(a))
	for i := 0; i < len(a); i++ {
		acc, ok := a[i]
		if !ok {
			continue
		}
		result = append(result, schema.ToolCall{
			ID:        acc.id,
			Name:      acc.name,
			Arguments: []byte(acc.args.String()),
		})
	}
	return result
}
