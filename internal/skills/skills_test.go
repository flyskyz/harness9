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
