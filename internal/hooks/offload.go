package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness9/internal/schema"
)

const (
	defaultThreshold    = 10000
	defaultPreviewLines = 20
)

// offloadExcluded 中的工具永远不触发 offload，避免读写循环。
var offloadExcluded = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
}

// OffloadOption 配置 OffloadHook。
type OffloadOption func(*OffloadHook)

// WithThreshold 设置触发 offload 的字符数阈值（默认 10000）。
func WithThreshold(n int) OffloadOption {
	return func(h *OffloadHook) { h.threshold = n }
}

// WithPreviewLines 设置 context 中保留的预览行数（默认 20）。
func WithPreviewLines(n int) OffloadOption {
	return func(h *OffloadHook) { h.previewLines = n }
}

// OffloadHook 将超大工具输出写入文件系统，替换为摘要引用和预览内容。
// 使 LLM context 不因单次大输出爆炸，完整数据通过 read_file 可检索。
type OffloadHook struct {
	baseDir      string
	sessionID    string
	threshold    int
	previewLines int
}

// NewOffloadHook 创建写入 baseDir/sessionID/ 的 OffloadHook。
func NewOffloadHook(baseDir, sessionID string, opts ...OffloadOption) *OffloadHook {
	h := &OffloadHook{
		baseDir:      baseDir,
		sessionID:    sessionID,
		threshold:    defaultThreshold,
		previewLines: defaultPreviewLines,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// BeforeExecute 对 OffloadHook 是空操作。
func (h *OffloadHook) BeforeExecute(ctx context.Context, tc schema.ToolCall) (context.Context, error) {
	return ctx, nil
}

// AfterExecute 检测输出大小，超阈值时写入文件并替换 result.Output 为摘要引用。
// 写入失败时 fail-open：原样返回原始结果，不中断 agent loop。
func (h *OffloadHook) AfterExecute(_ context.Context, tc schema.ToolCall, result schema.ToolResult) schema.ToolResult {
	if offloadExcluded[tc.Name] {
		return result
	}
	originalOutput := result.Output
	if len(originalOutput) <= h.threshold {
		return result
	}

	dir := filepath.Join(h.baseDir, h.sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return result
	}
	path := filepath.Join(dir, tc.ID+".txt")
	if err := os.WriteFile(path, []byte(originalOutput), 0600); err != nil {
		return result
	}

	lines := strings.Split(originalOutput, "\n")
	totalLines := len(lines)
	previewEnd := h.previewLines
	if previewEnd > totalLines {
		previewEnd = totalLines
	}
	preview := strings.Join(lines[:previewEnd], "\n")

	result.Output = fmt.Sprintf(
		"[输出已保存至 %s，共 %d 行 / %d 字节。\n"+
			"可通过 read_file 工具配合 offset/limit 参数分页读取。\n\n"+
			"预览（前 %d 行）：\n%s\n...（已截断）]",
		path, totalLines, len(originalOutput), previewEnd, preview,
	)
	return result
}
