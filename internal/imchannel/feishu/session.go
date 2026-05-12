package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/harness9/internal/schema"
)

// Session 是 imchannel.Session 接口的飞书实现。
//
// 进度展示策略：每个生命周期事件发送一条独立文本消息，无需 Patch API。
// 飞书 Patch API 仅支持交互式卡片（msg_type=interactive），不适用于纯文本消息。
//
// 消息序列示例：
//
//	🤔 思考中...
//	🔧 调用工具：bash
//	✅ bash（123ms）
//	（最终回复文本）
//
// 并发安全：Session 本身无可变状态，sendText 的每次调用均为独立 HTTP 请求，
// 多 goroutine 并发调用不同方法是安全的。消息顺序由飞书服务端接收顺序决定。
type Session struct {
	client *lark.Client
	chatID string
}

// NotifyThinking 发送"思考中"占位消息，表示 Agent 开始处理。
func (s *Session) NotifyThinking(ctx context.Context) error {
	return s.sendText(ctx, "🤔 思考中...")
}

// NotifyToolStart 发送工具调用开始消息。
func (s *Session) NotifyToolStart(ctx context.Context, tc schema.ToolCall) error {
	return s.sendText(ctx, fmt.Sprintf("🔧 调用工具：%s", tc.Name))
}

// NotifyToolDone 发送工具调用完成消息（成功或失败）。
func (s *Session) NotifyToolDone(ctx context.Context, tc schema.ToolCall, result schema.ToolResult, d time.Duration) error {
	icon := "✅"
	if result.IsError {
		icon = "❌"
	}
	return s.sendText(ctx, fmt.Sprintf("%s %s（%dms）", icon, tc.Name, d.Milliseconds()))
}

// SendReply 发送 Agent 最终回复。
// 调用方（Server）保证 text 不为空；兜底逻辑在编排层（server.go）处理。
func (s *Session) SendReply(ctx context.Context, text string) error {
	return s.sendText(ctx, text)
}

// sendText 发送一条纯文本飞书消息到当前会话。
func (s *Session) sendText(ctx context.Context, text string) error {
	content := buildTextContent(text)
	resp, err := s.client.Im.Message.Create(ctx,
		larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				ReceiveId(s.chatID).
				Content(content).
				Build()).
			Build())
	if err != nil {
		return fmt.Errorf("feishu sendText: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu sendText: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// buildTextContent 将纯文本序列化为飞书文本消息的 Content JSON（{"text":"..."}）。
func buildTextContent(text string) string {
	b, _ := json.Marshal(map[string]string{"text": text})
	return string(b)
}

// truncateRunes 按 Unicode 字符数截断字符串，超出时追加省略号。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
