// 内置工具：EditFile（文件编辑工具 / File Editing Tool）。
//
// 提供受限工作区（Sandboxed Workspace）内的精确文本替换能力，
// 基于多级模糊匹配算法（Multi-Level Fuzzy Matching）实现：
//
//  1. 沙箱边界（Sandbox Boundary）：所有路径通过 safePath 校验，
//     拒绝路径遍历攻击（Path Traversal Attack）。
//  2. 多级匹配（Multi-Level Matching）：四级容错机制逐级降级，
//     精确匹配 → 换行符归一化 → 整体去空 → 逐行去缩进。
//  3. 唯一性校验（Uniqueness Guard）：所有级别均要求唯一匹配，
//     多匹配时返回明确错误，要求 LLM 提供更多上下文。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/harness9/internal/schema"
)

// EditFileTool 实现了 BaseTool 接口，提供受限工作区内的精确文本替换能力。
type EditFileTool struct {
	// workDir 沙箱边界（Sandbox Boundary），所有文件操作被限制在此目录内。
	workDir string
}

// NewEditFileTool 创建绑定到指定工作区的文件编辑工具。
// workDir 会被 filepath.Clean 清洗，确保路径规范化（Path Normalization）。
func NewEditFileTool(workDir string) *EditFileTool {
	return &EditFileTool{workDir: filepath.Clean(workDir)}
}

// Name 返回工具标识符 "edit_file"。
func (t *EditFileTool) Name() string {
	return "edit_file"
}

// Definition 返回工具的元信息，包含描述和 JSON Schema 参数定义。
// LLM 会根据此定义决定何时调用该工具以及如何构造参数。
func (t *EditFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "对现有文件进行局部的字符串替换。这比重写整个文件更安全、更快速。请提供足够的 source_text 上下文以确保匹配的唯一性。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要修改的文件路径，如 src/main.go",
				},
				"source_text": map[string]interface{}{
					"type":        "string",
					"description": "文件中原有的文本。必须包含足够的上下文，以确保在文件中的唯一性。",
				},
				"target_text": map[string]interface{}{
					"type":        "string",
					"description": "要替换成的新文本",
				},
			},
			"required": []string{"path", "source_text", "target_text"},
		},
	}
}

// editFileArgs 定义 edit_file 工具的 JSON 参数结构（Argument Payload），
// 对应 LLM 在 ToolCall 中传递的 Arguments 载荷。
type editFileArgs struct {
	Path       string `json:"path"`        // 目标文件路径
	SourceText string `json:"source_text"` // 待匹配的原始文本片段
	TargetText string `json:"target_text"` // 替换后的新文本
}

// Execute 执行文件编辑操作。流程如下：
//  1. 反序列化 JSON 参数，提取路径、源文本和目标文本
//  2. 通过 safePath 校验并解析为绝对路径（含沙箱边界检查）
//  3. 读取源文件内容
//  4. 通过 fuzzyReplace 多级模糊匹配执行替换
//  5. 将结果写回文件
func (t *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input editFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	fullPath, err := safePath(t.workDir, input.Path)
	if err != nil {
		return "", err
	}

	unlock := LockPath(fullPath)
	defer unlock()

	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("读取文件失败，请确认路径是否正确: %w", err)
	}
	originalContent := string(contentBytes)

	newContent, err := fuzzyReplace(originalContent, input.SourceText, input.TargetText)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("写回文件失败: %w", err)
	}

	return fmt.Sprintf("成功修改文件: %s", input.Path), nil
}

// fuzzyReplace 对文件内容执行多级模糊匹配替换（Multi-Level Fuzzy Matching）。
//
// 四级容错机制（Four-Level Fallback Pipeline）：
//
//	L1 — 精确匹配（Exact Match）：sourceText 在原始内容中精确出现一次，直接替换。
//	    替换使用原始 targetText 而非归一化版本，保留所有原始格式（含 \r\n）。
//	L2 — 换行符归一化（Line Ending Normalization）：
//	    将 \r\n 统一为 \n 后再匹配，兼容跨平台文件格式。
//	    替换后根据原始内容是否包含 \r\n 自动恢复换行风格。
//	L3 — 整体首尾去空（Trim Surrounding Whitespace）：
//	    去除 sourceText 两端的空白字符后匹配，容忍 LLM 产生的多余空白。
//	    若 trimmedSource 为空（全空白文本），跳过此步避免误匹配空行。
//	L4 — 逐行去缩进匹配（Line-by-Line Indent-Agnostic Matching）：
//	    逐行去除首尾空白后滑动窗口匹配，容忍缩进差异（空格 vs Tab）。
//
// 所有级别的匹配结果必须是唯一的（count == 1），多匹配或零匹配均返回明确错误。
func fuzzyReplace(originalContent, sourceText, targetText string) (string, error) {
	// 归一化 targetText 的换行符，避免替换后混用 \r\n 和 \n
	normalizedTarget := strings.ReplaceAll(targetText, "\r\n", "\n")

	// L1: 原始文本精确匹配（Exact Match）
	count := strings.Count(originalContent, sourceText)
	if count == 1 {
		return strings.Replace(originalContent, sourceText, targetText, 1), nil
	}
	if count > 1 {
		return "", fmt.Errorf("source_text 匹配到了 %d 处，请提供更多的上下文代码以确保唯一性", count)
	}

	// 对原始内容做换行符归一化，进入 L2-L4
	normalizedContent := strings.ReplaceAll(originalContent, "\r\n", "\n")
	normalizedSource := strings.ReplaceAll(sourceText, "\r\n", "\n")

	// 判断是否需要重建 \r\n（恢复原始换行风格）
	hasCRLF := strings.Contains(originalContent, "\r\n")

	// L2: 换行符归一化匹配（Line Ending Normalization Match）
	count = strings.Count(normalizedContent, normalizedSource)
	if count == 1 {
		result := strings.Replace(normalizedContent, normalizedSource, normalizedTarget, 1)
		if hasCRLF {
			result = strings.ReplaceAll(result, "\n", "\r\n")
		}
		return result, nil
	}

	// L3: 整体首尾去空匹配（Trimmed Match）
	trimmedSource := strings.TrimSpace(normalizedSource)
	if trimmedSource != "" {
		count = strings.Count(normalizedContent, trimmedSource)
		if count == 1 {
			result := strings.Replace(normalizedContent, trimmedSource, normalizedTarget, 1)
			if hasCRLF {
				result = strings.ReplaceAll(result, "\n", "\r\n")
			}
			return result, nil
		}
	}

	// L4: 逐行去缩进匹配（Line-by-Line Indent-Agnostic Matching）
	return lineByLineReplace(normalizedContent, normalizedSource, normalizedTarget, hasCRLF)
}

// lineByLineReplace 将文本按行切割，去除首尾空白后进行滑动窗口匹配（Sliding Window Matching）。
//
// 应用场景：LLM 提供的 source_text 与文件中实际内容的缩进不一致时，
// 通过逐行去缩进实现模糊匹配（Indent-Agnostic Matching）。
//
// 参数说明：
//   - content:   已归一化（\r\n → \n）的文件内容
//   - sourceText: 已归一化的待匹配文本（由 fuzzyReplace 传入 normalizedSource）
//   - targetText: 已归一化的替换文本（由 fuzzyReplace 传入 normalizedTarget）
//   - hasCRLF:   原始文件是否使用 \r\n 换行风格，用于匹配后恢复
//
// 匹配成功后，使用 targetText 替换整个匹配块（contentLines 中从 matchStartIndex
// 到 matchEndIndex 的范围），并保留原始换行风格。
func lineByLineReplace(content, sourceText, targetText string, hasCRLF bool) (string, error) {
	contentLines := strings.Split(content, "\n")
	sourceLines := strings.Split(strings.TrimSpace(sourceText), "\n")

	if len(sourceLines) == 0 || len(contentLines) < len(sourceLines) {
		return "", fmt.Errorf("在文件中未找到 source_text，请检查内容和缩进")
	}

	// 去除源文本每行的首尾空白
	for i := range sourceLines {
		sourceLines[i] = strings.TrimSpace(sourceLines[i])
	}

	matchCount := 0
	matchStartIndex := -1
	matchEndIndex := -1

	// 滑动窗口匹配
	for i := 0; i <= len(contentLines)-len(sourceLines); i++ {
		isMatch := true
		for j := 0; j < len(sourceLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != sourceLines[j] {
				isMatch = false
				break
			}
		}

		if isMatch {
			matchCount++
			matchStartIndex = i
			matchEndIndex = i + len(sourceLines)
		}
	}

	if matchCount == 0 {
		return "", fmt.Errorf("在文件中未找到 source_text，请检查内容和缩进")
	}
	if matchCount > 1 {
		return "", fmt.Errorf("模糊匹配到了 %d 处代码，请提供更多上下文以定位", matchCount)
	}

	var newContentLines []string
	newContentLines = append(newContentLines, contentLines[:matchStartIndex]...)
	newContentLines = append(newContentLines, targetText)
	newContentLines = append(newContentLines, contentLines[matchEndIndex:]...)

	result := strings.Join(newContentLines, "\n")
	if hasCRLF {
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}
	return result, nil
}
