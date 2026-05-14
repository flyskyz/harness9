package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/provider/providertest"
	"github.com/harness9/internal/skills"
	"github.com/harness9/internal/tools"
)

func newTestEngine(t *testing.T) *engine.AgentEngine {
	t.Helper()
	return engine.NewAgentEngine(providertest.NewMock(), tools.NewRegistry(), t.TempDir())
}

// TestRunCLI_ExitCommand 验证输入 "exit" 时 runCLI 正常返回，不阻塞。
func TestRunCLI_ExitCommand(t *testing.T) {
	eng := newTestEngine(t)
	input := strings.NewReader("exit\n")
	runCLI(context.Background(), eng, input, nil)
}

// TestRunCLI_QuitCommand 验证输入 "quit" 时 runCLI 正常返回。
func TestRunCLI_QuitCommand(t *testing.T) {
	eng := newTestEngine(t)
	input := strings.NewReader("quit\n")
	runCLI(context.Background(), eng, input, nil)
}

// TestRunCLI_EOFExits 验证 stdin 关闭（EOF）时 runCLI 正常返回。
func TestRunCLI_EOFExits(t *testing.T) {
	eng := newTestEngine(t)
	input := strings.NewReader("") // 空输入 → 立即 EOF
	runCLI(context.Background(), eng, input, nil)
}

// TestRunCLI_ContextCancel 验证 ctx 取消时 runCLI 正常返回。
func TestRunCLI_ContextCancel(t *testing.T) {
	eng := newTestEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	input := strings.NewReader("some input\n")
	runCLI(ctx, eng, input, nil)
}

// TestRunCLI_UnknownSkill 验证 /nonexistent 输入时 runCLI 正常返回，不 panic。
func TestRunCLI_UnknownSkill(t *testing.T) {
	eng := newTestEngine(t)
	idx, err := skills.LoadSkills(t.TempDir()) // 空 Index
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("/nonexistent\nexit\n")
	runCLI(context.Background(), eng, input, idx)
}

// TestResolvePrompt_PlainInput 验证普通输入原样透传。
func TestResolvePrompt_PlainInput(t *testing.T) {
	prompt, ok := resolvePrompt("hello world", nil)
	if !ok {
		t.Error("plain input should return ok=true")
	}
	if prompt != "hello world" {
		t.Errorf("got %q, want %q", prompt, "hello world")
	}
}

// TestResolvePrompt_NilIndex 验证 idx 为 nil 时斜杠命令作为普通输入透传。
func TestResolvePrompt_NilIndex(t *testing.T) {
	prompt, ok := resolvePrompt("/some-skill", nil)
	if !ok {
		t.Error("nil idx slash input should return ok=true")
	}
	if prompt != "/some-skill" {
		t.Errorf("got %q, want %q", prompt, "/some-skill")
	}
}

// TestResolvePrompt_SkillOnly 验证 /skill-name 返回 skill body。
func TestResolvePrompt_SkillOnly(t *testing.T) {
	idx := makeTestIndex(t, "my-skill", "Skill body content.")
	prompt, ok := resolvePrompt("/my-skill", idx)
	if !ok {
		t.Error("expected ok=true")
	}
	if prompt != "Skill body content." {
		t.Errorf("got %q, want %q", prompt, "Skill body content.")
	}
}

// TestResolvePrompt_SkillWithExtra 验证 /skill-name extra text 正确拼接。
func TestResolvePrompt_SkillWithExtra(t *testing.T) {
	idx := makeTestIndex(t, "my-skill", "Skill body content.")
	prompt, ok := resolvePrompt("/my-skill 清理 main.go", idx)
	if !ok {
		t.Error("expected ok=true")
	}
	want := "Skill body content.\n\n清理 main.go"
	if prompt != want {
		t.Errorf("got %q, want %q", prompt, want)
	}
}

// TestResolvePrompt_UnknownSkill 验证不存在的技能返回 ok=false。
func TestResolvePrompt_UnknownSkill(t *testing.T) {
	idx, _ := skills.LoadSkills(t.TempDir())
	_, ok := resolvePrompt("/nonexistent", idx)
	if ok {
		t.Error("unknown skill should return ok=false")
	}
}

// makeTestIndex 在临时目录创建子目录结构的 skill 并返回 Index。
func makeTestIndex(t *testing.T, name, body string) *skills.Index {
	t.Helper()
	dir := t.TempDir()
	subDir := filepath.Join(dir, name)
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: Test skill\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(subDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	idx, err := skills.LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}
