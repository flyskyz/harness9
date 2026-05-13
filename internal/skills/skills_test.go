package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter_Valid(t *testing.T) {
	content := "---\nname: my-skill\ndescription: A test skill\ntrigger: refactor\n---\n\nBody here."
	name, desc, trig, body := parseFrontmatter(content)
	if name != "my-skill" {
		t.Errorf("name: got %q, want %q", name, "my-skill")
	}
	if desc != "A test skill" {
		t.Errorf("description: got %q, want %q", desc, "A test skill")
	}
	if trig != "refactor" {
		t.Errorf("trigger: got %q, want %q", trig, "refactor")
	}
	if body != "Body here." {
		t.Errorf("body: got %q, want %q", body, "Body here.")
	}
}

func TestParseFrontmatter_QuotedValues(t *testing.T) {
	content := "---\nname: \"quoted-skill\"\ndescription: \"Quoted description\"\n---\n\nBody"
	name, desc, _, body := parseFrontmatter(content)
	if name != "quoted-skill" {
		t.Errorf("name: got %q, want %q", name, "quoted-skill")
	}
	if desc != "Quoted description" {
		t.Errorf("description: got %q, want %q", desc, "Quoted description")
	}
	if body != "Body" {
		t.Errorf("body: got %q, want %q", body, "Body")
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := "Just a plain body"
	name, desc, trig, body := parseFrontmatter(content)
	if name != "" || desc != "" || trig != "" {
		t.Error("expected empty name/desc/trig for content without frontmatter")
	}
	if body != content {
		t.Errorf("body should equal full content: got %q", body)
	}
}

func TestParseFrontmatter_MissingClosingDelimiter(t *testing.T) {
	content := "---\nname: my-skill\n\nNo closing delimiter"
	name, _, _, body := parseFrontmatter(content)
	if name != "" {
		t.Errorf("name: got %q, want empty when closing delimiter missing", name)
	}
	if body != content {
		t.Errorf("body should equal full content: got %q", body)
	}
}

func TestParseFrontmatter_NoTrigger(t *testing.T) {
	content := "---\nname: my-skill\ndescription: A skill\n---\n\nBody"
	name, desc, trig, body := parseFrontmatter(content)
	if name != "my-skill" || desc != "A skill" {
		t.Error("expected name and description to parse")
	}
	if trig != "" {
		t.Errorf("trigger: got %q, want empty string", trig)
	}
	if body != "Body" {
		t.Errorf("body: got %q, want %q", body, "Body")
	}
}

// 以下变量仅为让编译器不报错（后续任务会用到这些导入）
var _ = context.Background
var _ = json.RawMessage(nil)
var _ = os.TempDir
var _ = filepath.Join
var _ = strings.Contains

// --- Index tests ---

func TestIndex_IsEmpty(t *testing.T) {
	empty := &Index{}
	if !empty.IsEmpty() {
		t.Error("new Index should be empty")
	}
	nonEmpty := &Index{skills: []Skill{{Name: "a", Description: "A"}}}
	if nonEmpty.IsEmpty() {
		t.Error("Index with skills should not be empty")
	}
}

func TestIndex_Summary_Empty(t *testing.T) {
	idx := &Index{}
	if idx.Summary() != "" {
		t.Error("empty index Summary() should return empty string")
	}
}

func TestIndex_Summary_WithSkills(t *testing.T) {
	idx := &Index{skills: []Skill{
		{Name: "skill-a", Description: "Desc A"},
		{Name: "skill-b", Description: "Desc B"},
	}}
	got := idx.Summary()
	if !strings.Contains(got, "skill-a: Desc A") {
		t.Errorf("summary missing skill-a entry: %q", got)
	}
	if !strings.Contains(got, "skill-b: Desc B") {
		t.Errorf("summary missing skill-b entry: %q", got)
	}
}

func TestIndex_GetFullContent_NotFound(t *testing.T) {
	idx := &Index{}
	_, err := idx.GetFullContent("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention skill name: %v", err)
	}
}

func TestIndex_GetFullContent_Found(t *testing.T) {
	f, err := os.CreateTemp("", "skill-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("---\nname: test-skill\ndescription: Test\n---\n\nSkill body content."); err != nil {
		t.Fatal(err)
	}
	f.Close()

	idx := &Index{skills: []Skill{{Name: "test-skill", Description: "Test", filePath: f.Name()}}}
	body, err := idx.GetFullContent("test-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Skill body content." {
		t.Errorf("body: got %q, want %q", body, "Skill body content.")
	}
}

// --- LoadSkills tests ---

// writeSkillFile 在指定路径写入 skill 文件内容的测试辅助函数。
func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkills_NonExistentDir(t *testing.T) {
	idx, err := LoadSkills("/nonexistent/path/to/skills")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !idx.IsEmpty() {
		t.Error("expected empty index for nonexistent directory")
	}
}

func TestLoadSkills_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	idx, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !idx.IsEmpty() {
		t.Error("expected empty index for empty directory")
	}
}

func TestLoadSkills_ValidSkills(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, filepath.Join(dir, "skill-a.md"),
		"---\nname: skill-a\ndescription: Skill A desc\n---\n\nBody A")
	writeSkillFile(t, filepath.Join(dir, "skill-b.md"),
		"---\nname: skill-b\ndescription: Skill B desc\n---\n\nBody B")

	idx, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.IsEmpty() {
		t.Fatal("expected non-empty index")
	}
	summary := idx.Summary()
	if !strings.Contains(summary, "skill-a: Skill A desc") {
		t.Errorf("summary missing skill-a: %q", summary)
	}
	if !strings.Contains(summary, "skill-b: Skill B desc") {
		t.Errorf("summary missing skill-b: %q", summary)
	}
}

func TestLoadSkills_SkipsInvalidFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, filepath.Join(dir, "no-desc.md"),
		"---\nname: missing-desc\n---\n\nBody")
	writeSkillFile(t, filepath.Join(dir, "valid.md"),
		"---\nname: valid-skill\ndescription: Valid skill\n---\n\nBody")

	idx, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	summary := idx.Summary()
	if strings.Contains(summary, "missing-desc") {
		t.Error("skill missing description should be skipped")
	}
	if !strings.Contains(summary, "valid-skill") {
		t.Error("valid skill should be loaded")
	}
}

func TestLoadSkills_SkipsNonMdFiles(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, filepath.Join(dir, "not-skill.txt"),
		"---\nname: txt-skill\ndescription: Text file\n---\n\nBody")
	writeSkillFile(t, filepath.Join(dir, "real.md"),
		"---\nname: real-skill\ndescription: Real skill\n---\n\nBody")

	idx, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(idx.Summary(), "txt-skill") {
		t.Error("non-.md files should be skipped")
	}
	if !strings.Contains(idx.Summary(), "real-skill") {
		t.Error(".md skill should be loaded")
	}
}

// --- UseSkillTool tests ---

func TestUseSkillTool_Name(t *testing.T) {
	tool := NewUseSkillTool(&Index{})
	if tool.Name() != "use_skill" {
		t.Errorf("Name(): got %q, want %q", tool.Name(), "use_skill")
	}
}

func TestUseSkillTool_Execute_InvalidArgs(t *testing.T) {
	tool := NewUseSkillTool(&Index{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON args")
	}
}

func TestUseSkillTool_Execute_EmptyName(t *testing.T) {
	tool := NewUseSkillTool(&Index{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"skill_name":""}`))
	if err == nil {
		t.Error("expected error for empty skill_name")
	}
}

func TestUseSkillTool_Execute_NotFound(t *testing.T) {
	tool := NewUseSkillTool(&Index{})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"skill_name":"missing"}`))
	if err == nil {
		t.Error("expected error for missing skill")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should mention skill name: %v", err)
	}
}

func TestUseSkillTool_Execute_Found(t *testing.T) {
	f, err := os.CreateTemp("", "skill-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("---\nname: test\ndescription: Test\n---\n\nSkill body content."); err != nil {
		t.Fatal(err)
	}
	f.Close()

	idx := &Index{skills: []Skill{{Name: "test", Description: "Test", filePath: f.Name()}}}
	tool := NewUseSkillTool(idx)
	body, err := tool.Execute(context.Background(), json.RawMessage(`{"skill_name":"test"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "Skill body content." {
		t.Errorf("body: got %q, want %q", body, "Skill body content.")
	}
}
