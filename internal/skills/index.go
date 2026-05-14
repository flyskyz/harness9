package skills

import (
	"fmt"
	"os"
	"strings"
)

// Index 是所有已加载 skills 的集合，提供索引摘要和按需全文读取能力。
type Index struct {
	skills []Skill
}

// IsEmpty 报告 Index 中是否不包含任何 skill。
func (idx *Index) IsEmpty() bool {
	return len(idx.skills) == 0
}

// Summary 返回 skills 索引的纯文本摘要，格式为每行 "- name: description\n"。
// 供 PromptBuilder 注入到 System Prompt，实现 Progressive Disclosure。
func (idx *Index) Summary() string {
	if idx.IsEmpty() {
		return ""
	}
	var sb strings.Builder
	for _, s := range idx.skills {
		fmt.Fprintf(&sb, "- %s: %s\n", s.Name, s.Description)
	}
	return sb.String()
}

// GetFullContent 按名称懒加载指定 skill 的全文内容（frontmatter 之后的 body）。
// skill 不存在时返回包含可用名称列表的可读错误信息，供 LLM 自愈。
func (idx *Index) GetFullContent(name string) (string, error) {
	for _, s := range idx.skills {
		if s.Name == name {
			data, err := os.ReadFile(s.filePath)
			if err != nil {
				return "", fmt.Errorf("读取 skill %q 失败: %w", name, err)
			}
			_, _, _, body := parseFrontmatter(string(data))
			return strings.TrimSpace(body), nil
		}
	}
	return "", fmt.Errorf("skill %q 不存在，可用技能: %s", name, idx.availableNames())
}

// Names 返回所有已加载技能的名称列表，供 TUI Tab 补全使用。
func (idx *Index) Names() []string {
	names := make([]string, len(idx.skills))
	for i, s := range idx.skills {
		names[i] = s.Name
	}
	return names
}

func (idx *Index) availableNames() string {
	return strings.Join(idx.Names(), ", ")
}
