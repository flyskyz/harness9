// Package planning 实现 harness9 的规划模块：TodoStore（任务列表）和 PlanMode（执行模式）。
package planning

import (
	"fmt"
	"strings"
	"sync"
)

// TodoStatus 表示单个任务条目（todo item）的生命周期状态。
// 状态转换约束由 todo_write 工具（tools 包）负责执行，TodoStore 本身不做校验。
//
// 合法的状态转换路径：
//
//	pending ──► in_progress ──► completed
//	   │              │
//	   └──────────────┴──► cancelled
type TodoStatus string

const (
	// TodoPending 表示任务尚未开始。初始创建时的默认状态。
	TodoPending TodoStatus = "pending"
	// TodoInProgress 表示任务正在执行中。
	// LLM 在开始实际工具调用前应先将任务标记为此状态。
	TodoInProgress TodoStatus = "in_progress"
	// TodoCompleted 表示任务已完成，对应有实际产出（文件创建、命令执行等）。
	// 防作弊校验（todo_write）确保此状态不能被批量伪造。
	TodoCompleted TodoStatus = "completed"
	// TodoCancelled 表示任务已取消，不再执行。
	// 取消的任务不能直接标记为 completed，必须先恢复为 pending 或 in_progress。
	TodoCancelled TodoStatus = "cancelled"
)

// TodoItem 是单个任务条目，包含唯一标识、内容描述和当前状态。
// ID 由 LLM 自行分配，用于在全量替换时识别条目历史状态（防作弊校验的依据）。
type TodoItem struct {
	ID      string     `json:"id"`      // 任务的唯一标识符，LLM 自行分配
	Content string     `json:"content"` // 任务内容描述，应对应一个具体可执行动作
	Status  TodoStatus `json:"status"`  // 当前状态（pending/in_progress/completed/cancelled）
}

// TodoStore 是线程安全的会话内任务列表，采用全量替换（atomic replace）语义。
//
// 设计决策——全量替换 vs 增量更新：
// LLM 每次调用 todo_write 时输出完整的当前清单（而非增量指令），
// 全量替换与这种输出形式完全匹配，同时避免了增量 API 的状态一致性问题。
//
// 并发安全：内部使用 sync.RWMutex 保护 items 切片，
// Read 允许多读并发，Write 排他。所有方法均可从任意 goroutine 安全调用。
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// NewTodoStore 创建空的 TodoStore，无任何初始任务。
func NewTodoStore() *TodoStore {
	return &TodoStore{}
}

// Write 原子性全量替换任务列表，返回替换后的列表副本。
// 先将入参复制到内部 slice，再返回内部 slice 的第二份副本：
// 双重复制确保调用方、内部存储与入参三者各自独立，互不影响。
func (s *TodoStore) Write(items []TodoItem) []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 第一次 copy：内部存储与入参 items 解耦，防止调用方后续修改 items 影响内部状态。
	s.items = make([]TodoItem, len(items))
	copy(s.items, items)
	// 第二次 copy（通过 s.copy()）：返回值与内部存储解耦，防止调用方修改返回值影响 TodoStore。
	return s.copy()
}

// Read 返回当前任务列表的副本（线程安全）。
func (s *TodoStore) Read() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.copy()
}

// copy 返回 s.items 的副本。调用方必须持有读锁或写锁后才能调用此方法。
// 空列表时返回 nil（而非空切片），与 json.Marshal 的行为兼容（nil → "null"，[]{} → "[]"）。
// 注意：TodoItem 是值类型（无指针字段），浅拷贝即为完整独立副本。
func (s *TodoStore) copy() []TodoItem {
	if len(s.items) == 0 {
		return nil
	}
	result := make([]TodoItem, len(s.items))
	copy(result, s.items)
	return result
}

// FormatForInjection 将 pending 和 in_progress 状态的任务格式化为纯文本，
// 供 SummarizationCompactor 在上下文压缩后注入摘要消息末尾，
// 防止 LLM 在长对话压缩后遗忘尚未完成的任务。
//
// 实现了 memory.TodoInjector 接口（接口定义在 memory 包，遵循"接口定义在使用者侧"原则）。
// 无活跃任务（全部已完成或已取消）时返回空字符串，调用方应跳过注入。
//
// 输出格式示例：
//
//	[ ] 实现 handler/user.go
//	[>] 配置数据库连接
//	[ ] 添加路由注册
func (s *TodoStore) FormatForInjection() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var lines []string
	for _, item := range s.items {
		if item.Status == TodoPending || item.Status == TodoInProgress {
			// [ ] 表示 pending（待开始），[>] 表示 in_progress（进行中）
			prefix := "[ ]"
			if item.Status == TodoInProgress {
				prefix = "[>]"
			}
			lines = append(lines, fmt.Sprintf("%s %s", prefix, item.Content))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// ActiveCount 返回 (active, total) 两个计数：
//   - active：pending 和 in_progress 状态的任务数（即尚未完成的任务）
//   - total：TodoStore 中的全部任务数
//
// TUI 续跑逻辑（autoExecuting）使用此方法判断是否仍有待执行的任务。
func (s *TodoStore) ActiveCount() (active, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total = len(s.items)
	for _, item := range s.items {
		if item.Status == TodoPending || item.Status == TodoInProgress {
			active++
		}
	}
	return
}
