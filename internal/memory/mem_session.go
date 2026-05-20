package memory

import (
	"context"
	"sync"

	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/schema"
)

// MemorySession 是 Session 的纯内存实现，仅用于测试。
type MemorySession struct {
	mu   sync.Mutex
	id   string
	msgs []schema.Message
}

// NewMemorySession 创建指定 ID 的内存会话。
func NewMemorySession(id string) *MemorySession {
	return &MemorySession{id: id}
}

func (s *MemorySession) SessionID() string { return s.id }

func (s *MemorySession) GetMessages(_ context.Context, limit int) ([]schema.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit >= len(s.msgs) {
		result := make([]schema.Message, len(s.msgs))
		copy(result, s.msgs)
		return result, nil
	}
	start := len(s.msgs) - limit
	result := make([]schema.Message, limit)
	copy(result, s.msgs[start:])
	return result, nil
}

func (s *MemorySession) AddMessages(_ context.Context, msgs []schema.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msgs...)
	return nil
}

func (s *MemorySession) PopMessage(_ context.Context) (*schema.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) == 0 {
		return nil, nil
	}
	msg := s.msgs[len(s.msgs)-1]
	s.msgs = s.msgs[:len(s.msgs)-1]
	return &msg, nil
}

func (s *MemorySession) Clear(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = nil
	return nil
}

// GetTodos 内存实现：始终返回空列表（无持久化）。
func (s *MemorySession) GetTodos(_ context.Context) ([]planning.TodoItem, error) {
	return nil, nil
}

// SaveTodos 内存实现：无操作（无持久化）。
func (s *MemorySession) SaveTodos(_ context.Context, _ []planning.TodoItem) error {
	return nil
}
