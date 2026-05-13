package skills

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// LoadSkills 扫描 skillsDir 目录下所有 .md 文件并解析 frontmatter 构建 Index。
// 目录不存在时返回空 Index（不报错），保证零配置可运行。
// frontmatter 缺少必填字段（name 或 description）的文件被跳过并记录警告日志。
func LoadSkills(skillsDir string) (*Index, error) {
	entries, err := os.ReadDir(skillsDir)
	if os.IsNotExist(err) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 skills 目录失败: %w", err)
	}

	var loaded []Skill
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		filePath := filepath.Join(skillsDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("[skills] 跳过 %s: 读取失败: %v", entry.Name(), err)
			continue
		}
		name, desc, trigger, _ := parseFrontmatter(string(data))
		if name == "" || desc == "" {
			log.Printf("[skills] 跳过 %s: frontmatter 缺少 name 或 description", entry.Name())
			continue
		}
		loaded = append(loaded, Skill{
			Name:        name,
			Description: desc,
			Trigger:     trigger,
			filePath:    filePath,
		})
	}
	return &Index{skills: loaded}, nil
}
