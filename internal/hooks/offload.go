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
// 文件写入 {workDir}/.harness9/tool_results/{sessionID}/{toolCallID}.txt，
// 摘要中展示相对于 workDir 的相对路径，确保 LLM 可通过 read_file 工具读回。
type OffloadHook struct {
	workDir      string // Agent 工作区根目录，offload 文件写入其 .harness9 子目录
	sessionID    string
	threshold    int
	previewLines int
}

// NewOffloadHook 创建写入 workDir/.harness9/tool_results/sessionID/ 的 OffloadHook。
func NewOffloadHook(workDir, sessionID string, opts ...OffloadOption) *OffloadHook {
	h := &OffloadHook{
		workDir:      workDir,
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
// 文件路径在 workDir 内，摘要中展示相对路径，LLM 可直接用 read_file 工具读取。
// 写入失败时 fail-open：原样返回原始结果，不中断 agent loop。
func (h *OffloadHook) AfterExecute(_ context.Context, tc schema.ToolCall, result schema.ToolResult) schema.ToolResult {
	if offloadExcluded[tc.Name] {
		return result
	}
	originalOutput := result.Output
	if len(originalOutput) <= h.threshold {
		return result
	}
	// tc.ID 为空时无法生成稳定文件名，跳过 offload
	if tc.ID == "" {
		return result
	}

	dir := filepath.Join(h.workDir, ".harness9", "tool_results", h.sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return result
	}
	absPath := filepath.Join(dir, tc.ID+".txt")
	if err := os.WriteFile(absPath, []byte(originalOutput), 0600); err != nil {
		return result
	}

	// 展示相对于 workDir 的路径，使 LLM 可直接传给 read_file（沙箱内可访问）
	relPath, err := filepath.Rel(h.workDir, absPath)
	if err != nil {
		relPath = absPath
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
		relPath, totalLines, len(originalOutput), previewEnd, preview,
	)
	return result
}
