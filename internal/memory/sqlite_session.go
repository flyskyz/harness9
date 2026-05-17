package memory

import (
	"context"
	"database/sql"

	"github.com/harness9/internal/schema"
)

// SQLiteSession 是 Session 的 SQLite 持久化实现（完整方法将在 Task 5 中补充）。
type SQLiteSession struct {
	db        *sql.DB
	sessionID string
}

// SessionID 返回会话唯一标识符。
func (s *SQLiteSession) SessionID() string { return s.sessionID }

// GetMessages 返回最近 limit 条消息（Task 5 中实现完整逻辑）。
func (s *SQLiteSession) GetMessages(_ context.Context, _ int) ([]schema.Message, error) {
	return nil, nil
}

// AddMessages 追加消息到会话（Task 5 中实现完整逻辑）。
func (s *SQLiteSession) AddMessages(_ context.Context, _ []schema.Message) error { return nil }

// PopMessage 弹出最后一条消息（Task 5 中实现完整逻辑）。
func (s *SQLiteSession) PopMessage(_ context.Context) (*schema.Message, error) { return nil, nil }

// Clear 清空会话所有消息（Task 5 中实现完整逻辑）。
func (s *SQLiteSession) Clear(_ context.Context) error { return nil }
