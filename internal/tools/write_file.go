// 内置工具：WriteFile（文件写入工具）。
//
// 提供受限工作区（Sandboxed Workspace）内的安全文件写入能力，关键安全机制：
//  1. 沙箱边界（Sandbox Boundary）：所有路径通过 safePath 校验，
//     拒绝试图通过 "../" 等方式逃逸出工作区的路径遍历攻击（Path Traversal Attack）。
//  2. 自动建目录（Auto-Mkdir）：父级目录不存在时使用 0755 权限自动创建，
//     避免 LLM 因 ENOENT 错误而频繁重试。
//  3. 覆盖写入（Overwrite Semantics）：与 os.WriteFile 一致，目标文件已存在时直接覆盖，
//     LLM 需在 prompt 层自行判断是否应先 read_file 检查内容再写入。
//  4. 原子写入（Atomic Write）：使用 write-to-tmp + rename 模式，
//     避免进程被 kill / 断电 / 中断时留下半写的目标文件。
//     详见 atomicWriteFile 文档。
//
// # Known Limitations
//
//   - 原子性仅限同文件系统（POSIX 约束）。tmp 文件强制与目标同 dir 创建
//     （os.CreateTemp(filepath.Dir(target), ...)）以避免跨 FS 退化成
//     copy+unlink。如果调用方显式把 memory/ 挂成独立 mount，该目录树
//     自身仍需在同一个 FS 上才能保证原子性。
//   - Windows 下 os.Rename 在目标已存在时返回错误（与 POSIX 不同），
//     atomic overwrite 语义在 Windows 上不成立。harness9 v1 不承诺 Windows
//     一等支持，相关测试用例使用 runtime.GOOS == "windows" 直接 skip。
//     正式支持 Windows 时需改走 MoveFileExW 的 MOVEFILE_REPLACE_EXISTING 标志。
//   - 并发多 writer 写同一文件时，tmp+rename 保证不产生半写文件，但最终内容
//     是某一方的完整内容（后 rename 者胜出），另一方的修改会丢。这是 "single
//     writer per file" 假设下的预期行为，业务层需通过 LockPath 避免。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/harness9/internal/schema"
)

// osCreateTemp / osRename / osFileSync 作为可替换的函数变量，目的是在单元
// 测试中注入失败分支（例如模拟 rename 失败以验证 tmp 清理 + 目标文件保持原状、
// 或模拟 fsync 失败以验证不会继续 rename 导致 "rename 了但内容 stale"）。
// 生产路径始终走标准库实现。
//
// 这个模式避免了引入完整的 filesystem abstraction 带来的复杂度，在测试尺度上
// 足够用。
var (
	osCreateTemp = os.CreateTemp
	osRename     = os.Rename
	osFileSync   = func(f *os.File) error { return f.Sync() }
)

// WriteFileTool 实现了 BaseTool 接口，提供受限工作区内的安全文件写入能力。
type WriteFileTool struct {
	// workDir 工具允许写入的根目录（Sandbox Boundary，沙箱边界），
	// 所有写入操作被限制在此目录树内。
	workDir string
}

// NewWriteFileTool 创建绑定到指定工作区的文件写入工具。
// workDir 会被 filepath.Clean 清洗，确保路径规范化（Path Normalization）。
func NewWriteFileTool(workDir string) *WriteFileTool {
	return &WriteFileTool{workDir: filepath.Clean(workDir)}
}

// Name 返回工具标识符 "write_file"。
func (t *WriteFileTool) Name() string {
	return "write_file"
}

// Definition 返回工具的元信息，包含描述和 JSON Schema 参数定义。
// LLM 会根据此定义决定何时调用该工具以及如何构造参数。
func (t *WriteFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "创建或覆盖写入一个文件，如果父级目录不存在会自动创建。请提供相对于工作区的相对路径。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要写入的文件路径，如 src/main.go",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "要写入的完整文件内容",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

// writeFileArgs 定义 write_file 工具的 JSON 参数结构（Argument Payload），
// 对应 LLM 在 ToolCall 中传递的 Arguments 载荷。
type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Execute 执行文件写入操作。流程如下：
//  1. 反序列化 JSON 参数，提取目标路径与文件内容
//  2. 通过 safePath 校验并解析为绝对路径（含沙箱边界检查）
//  3. 自动创建父级目录（若不存在）
//  4. 以 0644 权限原子写入文件（write-to-tmp + rename）
func (t *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input writeFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	// 沙箱边界校验：阻止路径遍历攻击（Path Traversal Attack）。
	// 与 read_file 复用同一份 safePath 实现，保持安全策略一致。
	fullPath, err := safePath(t.workDir, input.Path)
	if err != nil {
		return "", err
	}

	unlock := LockPath(fullPath)
	defer unlock()

	// 自动创建父级目录（Auto-Mkdir），避免 LLM 因父目录缺失而反复试错。
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", fmt.Errorf("创建父目录失败: %w", err)
	}

	// 原子写入（Atomic Write）：write-to-tmp + rename，
	// 进程被 kill 或磁盘满时不会留下半写的目标文件。
	if err := atomicWriteFile(fullPath, []byte(input.Content), 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	return fmt.Sprintf("成功将 %d 字节写入到文件: %s", len(input.Content), input.Path), nil
}

// atomicWriteFile 以原子方式将 data 写入 path：先写入同目录下的 tmp 文件，
// 成功后通过 os.Rename 原子替换目标。任一步骤失败都会清理 tmp 并保留目标
// 文件原样（如果存在）。
//
// fallbackPerm 是 target 不存在时新建文件所用的权限；target 已存在时会 Stat
// 原文件继承其 mode，不使用 fallbackPerm（否则覆盖写入会把用户之前 chmod 过
// 的权限静默还原为默认值）。
//
// 关键设计点:
//
//   - **tmp 必须与 target 在同一 dir**（os.CreateTemp(filepath.Dir(path), ...)）。
//     os.Rename 的原子性仅在同文件系统内保证；跨 FS 会退化为 copy+unlink，
//     进程被 kill 时可能留下部分 copy 完成的目标文件。
//
//   - **tmp 名字用 "." 开头**（形如 ".<basename>.tmp.<random>"）。进程中断
//     留下的 tmp 文件在常规 ls / 图形化文件浏览器中默认不可见，避免用户
//     误以为是项目残留文件或病毒。
//
//   - **权限继承（Permission Preservation）**：target 已存在时 Stat 拿原 mode
//     并 Chmod 到 tmp，不用 fallbackPerm 覆盖用户意图。target 不存在时才用
//     fallbackPerm。这一行为和 os.WriteFile 的语义保持一致。
//
//   - **fsync → Close → Chmod → Rename** 的顺序：
//     Sync 把内容刷盘（防止断电后"rename 生效但内容丢失"），
//     Close 释放 fd，
//     Chmod 确保目标权限在"落地"时就正确（避免中间出现错误权限窗口），
//     Rename 原子替换目标。
//
//   - **deferred cleanup 是无条件的**，只在成功 rename 后通过 committed flag
//     跳过。rename 成功后 tmpPath 不再存在，Remove 也不会报错，但显式跳过
//     可以避免在极少数情况下（例如另一进程快速创建了同名文件）误删。
//
// 参见 package-level Known Limitations 了解跨 FS 和 Windows 的限制。
func atomicWriteFile(path string, data []byte, fallbackPerm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// 权限继承：如果 target 已存在，用它的 mode；否则用 fallbackPerm。
	// 这和 os.WriteFile 覆盖行为一致（os.WriteFile 对已存在文件不改 mode）。
	finalPerm := fallbackPerm
	if st, err := os.Stat(path); err == nil {
		finalPerm = st.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("atomic: stat target %s: %w", path, err)
	}

	// "." 前缀让中断遗留的 tmp 文件默认隐藏，避免误解。
	// os.CreateTemp 会把 pattern 里的 "*" 替换为随机后缀，生成诸如
	// ".MEMORY.md.tmp.1234567890" 的完整文件名。
	tmp, err := osCreateTemp(dir, "."+base+".tmp.*")
	if err != nil {
		return fmt.Errorf("atomic: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	// committed 在成功 rename 后置 true；defer 里读到 false 就清理 tmp。
	// 这样无论中途哪一步 return error，tmp 都不会残留。
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic: write tmp %s: %w", tmpPath, err)
	}
	// Sync 在 Close 之前：把 page cache 刷到磁盘，防止断电时"rename 已生效
	// 但内容未真正持久化"的情况 —— 原子性的语义是"要么旧要么新"，不是
	// "要么旧要么未定义"。Sync 失败（NFS / 坏块设备常见）必须直接早退，
	// 不能继续 Rename，否则会得到"rename 完成但内容可能 stale"的结果。
	if err := osFileSync(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic: sync tmp %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomic: close tmp %s: %w", tmpPath, err)
	}

	// Chmod 在 Rename 前：避免目标文件短暂以 CreateTemp 默认的 0600 存在，
	// 再被改成预期的 mode（期间有可见性/权限错误窗口）。
	if err := os.Chmod(tmpPath, finalPerm); err != nil {
		return fmt.Errorf("atomic: chmod tmp %s: %w", tmpPath, err)
	}

	if err := osRename(tmpPath, path); err != nil {
		return fmt.Errorf("atomic: rename %s -> %s: %w", tmpPath, path, err)
	}
	committed = true
	return nil
}
