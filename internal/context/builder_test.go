package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harness9/internal/skills"
)

func TestBuild_BasePromptOnly(t *testing.T) {
	dir := t.TempDir()
	// skills 目录不存在 → 空 Index
	idx, _ := skills.LoadSkills(filepath.Join(dir, "skills"))

	b := NewPromptBuilder(dir, idx)
	prompt := b.Build()

	if !strings.Contains(prompt, "harness9") {
		t.Error("prompt should contain 'harness9'")
	}
	if !strings.Contains(prompt, dir) {
		t.Error("prompt should contain workDir")
	}
	if strings.Contains(prompt, "项目规范") {
		t.Error("prompt should not contain AGENTS.md section when file absent")
	}
	if strings.Contains(prompt, "可用 Skills") {
		t.Error("prompt should not contain skills section when index is empty")
	}
}

func TestBuild_WithAgentsMd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Project Guide\n\nAlways write tests first."), 0644); err != nil {
		t.Fatal(err)
	}
	idx, _ := skills.LoadSkills(filepath.Join(dir, "skills"))

	b := NewPromptBuilder(dir, idx)
	prompt := b.Build()

	if !strings.Contains(prompt, "项目规范") {
		t.Error("prompt should contain AGENTS.md section header")
	}
	if !strings.Contains(prompt, "Always write tests first.") {
		t.Error("prompt should contain AGENTS.md content")
	}
}

func TestBuild_WithSkills(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	if err := os.Mkdir(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillSubDir := filepath.Join(skillsDir, "go-refactor")
	if err := os.Mkdir(skillSubDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillContent := "---\nname: go-refactor\ndescription: Go refactoring guide\n---\n\nAlways run go vet first."
	if err := os.WriteFile(filepath.Join(skillSubDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := skills.LoadSkills(skillsDir)
	if err != nil {
		t.Fatal(err)
	}

	b := NewPromptBuilder(dir, idx)
	prompt := b.Build()

	if !strings.Contains(prompt, "可用 Skills") {
		t.Error("prompt should contain skills section header")
	}
	if !strings.Contains(prompt, "go-refactor: Go refactoring guide") {
		t.Error("prompt should contain skill index entry")
	}
	// Progressive Disclosure：skill 全文不能出现在 System Prompt 中
	if strings.Contains(prompt, "Always run go vet first.") {
		t.Error("prompt must NOT contain skill body content (progressive disclosure violated)")
	}
}

func TestBuild_NilSkillsIndex(t *testing.T) {
	dir := t.TempDir()
	b := NewPromptBuilder(dir, nil)
	prompt := b.Build()
	if !strings.Contains(prompt, "harness9") {
		t.Error("prompt should contain 'harness9' even with nil skills index")
	}
}
