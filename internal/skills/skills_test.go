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
