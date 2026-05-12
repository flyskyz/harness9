// Package imchannel 定义了 IM 平台的统一适配接口。
//
// IMChannel 是对不同 IM 平台（飞书、Slack、钉钉等）的抽象，
// 通过统一的接口隔离平台差异，使上层编排逻辑（Server）无需感知具体平台细节。
//
// # 核心设计
//
// IMChannel 负责连接管理和消息收发；Session 负责单次用户消息触发的
// Agent 执行过程中的 IM 侧进度展示。两者共同构成 IM 集成层的公共契约。
package imchannel

import (
	"context"
	"time"

	"github.com/harness9/internal/schema"
)

// MessageHandler 是用户消息到达时的回调签名。
// ctx 来自 IMChannel 的连接上下文，handler 不应长时间阻塞——
// 耗时操作应在独立 goroutine 中执行。
type MessageHandler func(ctx context.Context, msg IncomingMessage)

// IncomingMessage 代表从 IM 平台收到的一条用户消息。
type IncomingMessage struct {
	// ChatID 会话标识符（飞书的 chat_id，Slack 的 channel 等）。
	ChatID string

	// SenderID 发送者的平台用户标识（飞书 open_id 等）。
	SenderID string

	// Text 消息的纯文本内容。
	Text string

	// MessageID 平台消息唯一标识，用于回复线程或关联进度消息。
	MessageID string
}

// IMChannel 是 IM 平台的统一适配接口。
// 不同平台各自实现此接口，上层 Server 编排层依赖此接口而非具体实现。
type IMChannel interface {
	// Start 建立与 IM 平台的连接并开始接收消息，阻塞直到 ctx 取消。
	// 实现应支持自动重连，在 ctx 取消时优雅退出。
	Start(ctx context.Context) error

	// SetMessageHandler 注册用户消息到达时的回调。
	// 必须在 Start 之前调用，否则消息可能在处理器注册前到达。
	SetMessageHandler(handler MessageHandler)

	// NewSession 为一条入站消息创建独立的交互会话。
	// 每条用户消息对应一个 Session，Session 负责该次交互中所有 IM 消息的发送与更新。
	NewSession(chatID, messageID string) Session
}

// Session 代表一条用户消息触发的 Agent 执行上下文的"IM 侧视图"。
// 每个 Session 独立管理该次交互中占位消息、进度更新和最终回复的 IM 操作。
type Session interface {
	// NotifyThinking 发送"思考中"占位消息（Agent 开始处理时调用）。
	NotifyThinking(ctx context.Context) error

	// NotifyToolStart 推送工具开始执行的进度，在 EventToolStart 事件时调用。
	NotifyToolStart(ctx context.Context, tc schema.ToolCall) error

	// NotifyToolDone 推送工具执行完成的进度，d 为该工具的实际执行耗时。
	NotifyToolDone(ctx context.Context, tc schema.ToolCall, result schema.ToolResult, d time.Duration) error

	// SendReply 发送 Agent 的最终回复（成功或错误均通过此方法）。
	// 调用方保证 text 非空；若 agent 静默完成，编排层负责提供兜底文本（"✅ 任务完成"）。
	SendReply(ctx context.Context, text string) error
}
