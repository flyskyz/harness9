package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteFileTool_Name(t *testing.T) {
	tool := NewWriteFileTool("/tmp")
	if tool.Name() != "write_file" {
		t.Errorf("expected 'write_file', got %q", tool.Name())
	}
}

func TestWriteFileTool_Definition(t *testing.T) {
	tool := NewWriteFileTool("/tmp")
	def := tool.Definition()
	if def.Name != "write_file" {
		t.Errorf("definition name mismatch: %q", def.Name)
	}
	if def.Description == "" {
		t.Error("definition should have a description")
	}
	if def.InputSchema == nil {
		t.Error("definition should have an input schema")
	}
}

func TestWriteFileTool_Execute_Success(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"out.txt","content":"hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(dir + "/out.txt")
	if err != nil {
		t.Fatalf("file should exist after write: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file content mismatch: %q", data)
	}
}

func TestWriteFileTool_Execute_ReturnsFilePathAndByteCount(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"result.txt","content":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "result.txt") {
		t.Errorf("success message should mention file path, got: %q", out)
	}
	if !strings.Contains(out, "5") { // len("hello") == 5
		t.Errorf("success message should contain byte count 5, got: %q", out)
	}
}

func TestWriteFileTool_Execute_AutoMkdir(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"deep/nested/dir/file.txt","content":"nested"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(dir + "/deep/nested/dir/file.txt")
	if err != nil {
		t.Fatalf("nested file should exist: %v", err)
	}
	if string(data) != "nested" {
		t.Errorf("nested file content mismatch: %q", data)
	}
}

func TestWriteFileTool_Execute_Overwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/file.txt", []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewWriteFileTool(dir)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"file.txt","content":"overwritten"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(dir + "/file.txt")
	if err != nil {
		t.Fatalf("failed to read file after overwrite: %v", err)
	}
	if string(data) != "overwritten" {
		t.Errorf("expected overwritten content, got %q", data)
	}
}

func TestWriteFileTool_Execute_BadJSON(t *testing.T) {
	tool := NewWriteFileTool("/tmp")
	_, err := tool.Execute(context.Background(), json.RawMessage(`not_json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON args")
	}
}

// ---------------------------------------------------------------------------
// Atomic write semantics — tests below exercise atomicWriteFile directly and
// through WriteFileTool.Execute. They verify:
//   - 成功写入后没有 tmp 残留
//   - 权限继承（target 已存在时保留原 mode；不存在时用 fallback）
//   - rename 失败时 tmp 清理 + 目标文件保持原状
//   - tmp 文件创建在 target 同目录（跨 FS 原子性的前提）
//   - tmp 文件名以 "." 开头（默认隐藏）
//   - 并发多 writer 写同一路径不产生半写内容
// ---------------------------------------------------------------------------

// listTmpArtifacts 返回 dir 下所有 name 中含 ".tmp." 的文件，用于检测 tmp 残留。
func listTmpArtifacts(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWriteFileTool_Atomic_NoTmpResidueOnSuccess(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteFileTool(dir)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"ok.txt","content":"hello"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if residue := listTmpArtifacts(t, dir); len(residue) != 0 {
		t.Errorf("tmp residue after successful write: %v", residue)
	}
}

func TestAtomicWriteFile_PermissionPreservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes differ from POSIX; not enforced for v1")
	}

	tests := []struct {
		name         string
		existingMode os.FileMode // 0 表示 target 不存在
		fallback     os.FileMode
		wantMode     os.FileMode
	}{
		{
			name:         "target absent uses fallback 0644",
			existingMode: 0,
			fallback:     0644,
			wantMode:     0644,
		},
		{
			name:         "target absent uses fallback 0600",
			existingMode: 0,
			fallback:     0600,
			wantMode:     0600,
		},
		{
			name:         "target present 0644 preserved despite fallback 0600",
			existingMode: 0644,
			fallback:     0600,
			wantMode:     0644,
		},
		{
			name:         "target present 0755 preserved (e.g. script)",
			existingMode: 0755,
			fallback:     0644,
			wantMode:     0755,
		},
		{
			name:         "target present 0600 preserved",
			existingMode: 0600,
			fallback:     0644,
			wantMode:     0600,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target.txt")

			if tc.existingMode != 0 {
				if err := os.WriteFile(target, []byte("old"), tc.existingMode); err != nil {
					t.Fatal(err)
				}
				// WriteFile 受 umask 影响，显式 Chmod 确保初始 mode 精确。
				if err := os.Chmod(target, tc.existingMode); err != nil {
					t.Fatal(err)
				}
			}

			if err := atomicWriteFile(target, []byte("new"), tc.fallback); err != nil {
				t.Fatalf("atomicWriteFile: %v", err)
			}

			st, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != tc.wantMode {
				t.Errorf("mode = %v, want %v", st.Mode().Perm(), tc.wantMode)
			}

			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "new" {
				t.Errorf("content = %q, want %q", data, "new")
			}
		})
	}
}

func TestAtomicWriteFile_RenameFailure_LeavesOriginalAndCleansTmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rename-over-existing semantics differ on Windows")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatal(err)
	}

	origRename := osRename
	osRename = func(oldpath, newpath string) error {
		return errors.New("simulated rename failure")
	}
	defer func() { osRename = origRename }()

	err := atomicWriteFile(target, []byte("NEW"), 0644)
	if err == nil {
		t.Fatal("expected error from rename failure")
	}
	if !strings.Contains(err.Error(), "simulated rename failure") {
		t.Errorf("error should wrap simulated failure, got: %v", err)
	}

	// Original content must be intact.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("target should remain %q on rename failure, got %q", "ORIGINAL", got)
	}

	// Tmp must be cleaned up.
	if residue := listTmpArtifacts(t, dir); len(residue) != 0 {
		t.Errorf("tmp residue after failed rename: %v", residue)
	}
}

func TestAtomicWriteFile_WriteFailure_LeavesOriginalAndCleansTmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("write-failure injection uses read-only temp fd technique not portable to Windows")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatal(err)
	}

	// Inject a CreateTemp that opens the tmp file read-only, causing Write to fail.
	// Then fall through to the defer-cleanup path and verify target untouched + tmp gone.
	origCreateTemp := osCreateTemp
	osCreateTemp = func(d, pattern string) (*os.File, error) {
		f, err := origCreateTemp(d, pattern)
		if err != nil {
			return nil, err
		}
		name := f.Name()
		// Close original write fd and reopen read-only so Write will fail.
		_ = f.Close()
		return os.OpenFile(name, os.O_RDONLY, 0600)
	}
	defer func() { osCreateTemp = origCreateTemp }()

	err := atomicWriteFile(target, []byte("NEW"), 0644)
	if err == nil {
		t.Fatal("expected error from write failure")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("target should remain %q on write failure, got %q", "ORIGINAL", got)
	}

	if residue := listTmpArtifacts(t, dir); len(residue) != 0 {
		t.Errorf("tmp residue after failed write: %v", residue)
	}
}

func TestAtomicWriteFile_TmpInSameDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(subdir, "target.txt")

	var capturedDir string
	origCreateTemp := osCreateTemp
	osCreateTemp = func(d, pattern string) (*os.File, error) {
		capturedDir = d
		return origCreateTemp(d, pattern)
	}
	defer func() { osCreateTemp = origCreateTemp }()

	if err := atomicWriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	if capturedDir != subdir {
		t.Errorf("tmp dir = %q, want %q (target dir, for same-FS rename atomicity)", capturedDir, subdir)
	}
}

func TestAtomicWriteFile_DotPrefixHidesTmp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "visible.txt")

	var capturedTmpName string
	origCreateTemp := osCreateTemp
	osCreateTemp = func(d, pattern string) (*os.File, error) {
		f, err := origCreateTemp(d, pattern)
		if f != nil {
			capturedTmpName = filepath.Base(f.Name())
		}
		return f, err
	}
	defer func() { osCreateTemp = origCreateTemp }()

	if err := atomicWriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(capturedTmpName, ".") {
		t.Errorf("tmp name should start with '.' (hidden from default ls), got %q", capturedTmpName)
	}
	if !strings.Contains(capturedTmpName, ".tmp.") {
		t.Errorf("tmp name should contain '.tmp.' for pattern recognition, got %q", capturedTmpName)
	}
}

func TestAtomicWriteFile_Concurrent_NoCorruption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "concurrent.txt")

	// Two distinct contents large enough that a byte-level half-mix would be
	// trivially detectable. Each is 1024 bytes + a signature prefix.
	contentA := []byte("writer-A-" + strings.Repeat("a", 1024))
	contentB := []byte("writer-B-" + strings.Repeat("b", 1024))

	const iters = 100
	var wg sync.WaitGroup
	for i := 0; i < iters; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				_ = atomicWriteFile(target, contentA, 0644)
			} else {
				_ = atomicWriteFile(target, contentB, 0644)
			}
		}(i)
	}
	wg.Wait()

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	// Final content must be exactly one writer's full content — never a prefix,
	// never a blend.
	if !bytesEqual(got, contentA) && !bytesEqual(got, contentB) {
		prefix := got
		if len(prefix) > 64 {
			prefix = prefix[:64]
		}
		t.Errorf("corrupted final content: len=%d head=%q", len(got), prefix)
	}

	if residue := listTmpArtifacts(t, dir); len(residue) != 0 {
		t.Errorf("tmp residue after concurrent writes: %v", residue)
	}
}

func TestAtomicWriteFile_SyncFailure_LeavesOriginalAndCleansTmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fsync semantics differ on Windows; not enforced for v1")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatal(err)
	}

	// 注入 Sync 失败 (NFS / 坏块设备在生产中会撞到这个路径)。
	// 正确行为: 直接 return error, 不继续 Rename, 否则会得到
	// "rename 完成但内容可能 stale" 的假原子性结果。
	origSync := osFileSync
	osFileSync = func(f *os.File) error {
		return errors.New("simulated fsync failure (NFS / bad block)")
	}
	defer func() { osFileSync = origSync }()

	err := atomicWriteFile(target, []byte("NEW"), 0644)
	if err == nil {
		t.Fatal("expected error from sync failure")
	}
	if !strings.Contains(err.Error(), "simulated fsync failure") {
		t.Errorf("error should wrap simulated sync failure, got: %v", err)
	}
	// 关键: 错误消息应该是 sync path, 不是 rename path — 证明早退生效了
	if !strings.Contains(err.Error(), "sync") {
		t.Errorf("error should be from sync stage (early-exit, no rename), got: %v", err)
	}

	// 目标文件必须保持 ORIGINAL (rename 没执行)。
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("target should remain %q on sync failure (no rename should have happened), got %q", "ORIGINAL", got)
	}

	// tmp 必须清理。
	if residue := listTmpArtifacts(t, dir); len(residue) != 0 {
		t.Errorf("tmp residue after failed sync: %v", residue)
	}
}

func TestAtomicWriteFile_CreateTempFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatal(err)
	}

	origCreateTemp := osCreateTemp
	osCreateTemp = func(d, pattern string) (*os.File, error) {
		return nil, errors.New("simulated create temp failure")
	}
	defer func() { osCreateTemp = origCreateTemp }()

	err := atomicWriteFile(target, []byte("NEW"), 0644)
	if err == nil {
		t.Fatal("expected error from CreateTemp failure")
	}

	// Target must be unchanged; nothing to clean up because tmp never existed.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ORIGINAL" {
		t.Errorf("target should remain %q, got %q", "ORIGINAL", got)
	}
}

// bytesEqual is a tiny helper to avoid pulling "bytes" for one Equal call.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
