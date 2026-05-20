// Package planning 实现 harness9 的规划模块：TodoStore（任务列表）和 PlanMode（执行模式）。
package planning

import (
	"fmt"
	"strings"
	"sync"
)

// TodoStatus 表示单个任务条目的状态。
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

// TodoItem 是单个任务条目。
type TodoItem struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

// TodoStore 是线程安全的会话内任务列表。
// 全量替换语义：每次 Write 用新列表原子替换整个旧列表。
type TodoStore struct {
	mu    sync.RWMutex
	items []TodoItem
}

// NewTodoStore 创建空的 TodoStore。
func NewTodoStore() *TodoStore {
	return &TodoStore{}
}

// Write 原子性全量替换任务列表，返回替换后的列表副本。
func (s *TodoStore) Write(items []TodoItem) []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make([]TodoItem, len(items))
	copy(s.items, items)
	return s.copy()
}

// Read 返回当前任务列表的副本（线程安全）。
func (s *TodoStore) Read() []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.copy()
}

// copy 返回 s.items 的浅拷贝，调用方须持有锁。
func (s *TodoStore) copy() []TodoItem {
	if len(s.items) == 0 {
		return nil
	}
	result := make([]TodoItem, len(s.items))
	copy(result, s.items)
	return result
}

// FormatForInjection 将 pending/in_progress 任务格式化为文本，
// 在上下文压缩后注入摘要，防止 LLM 遗忘未完成任务。
// 无活跃任务时返回空字符串。
func (s *TodoStore) FormatForInjection() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var lines []string
	for _, item := range s.items {
		if item.Status == TodoPending || item.Status == TodoInProgress {
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

// ActiveCount 返回 (active, total)：active = pending+in_progress 数量，total = 全部数量。
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
