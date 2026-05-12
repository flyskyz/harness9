// Package feishu 实现了基于飞书（Lark）平台的 IMChannel 适配器。
//
// 采用飞书 WebSocket 长连接模式接收事件，无需公网 IP 或内网穿透。
// 仅处理私聊（chat_type=p2p）的文本消息（message_type=text）。
//
// SDK: github.com/larksuite/oapi-sdk-go/v3
package feishu

import (
	"context"
	"encoding/json"
	"log"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/harness9/internal/imchannel"
)

// Channel 是 IMChannel 接口的飞书实现，通过 WebSocket 长连接接收事件并调用飞书 API 发送消息。
type Channel struct {
	appID     string
	appSecret string
	client    *lark.Client
	handler   imchannel.MessageHandler
}

// NewChannel 创建飞书 Channel，使用给定的 App ID 和 App Secret 初始化 API 客户端。
func NewChannel(appID, appSecret string) *Channel {
	return &Channel{
		appID:     appID,
		appSecret: appSecret,
		client:    lark.NewClient(appID, appSecret),
	}
}

// SetMessageHandler 注册用户消息到达时的回调，必须在 Start 之前调用。
func (c *Channel) SetMessageHandler(handler imchannel.MessageHandler) {
	c.handler = handler
}

// NewSession 为一条入站消息创建对应的飞书 Session。
func (c *Channel) NewSession(chatID, messageID string) imchannel.Session {
	return &Session{
		client: c.client,
		chatID: chatID,
	}
}

// Start 建立飞书 WebSocket 长连接并开始接收事件，阻塞直到 ctx 取消。
// 支持自动重连，连接中断后会自动恢复。
func (c *Channel) Start(ctx context.Context) error {
	d := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			return c.handleEvent(ctx, event)
		})

	wsClient := ws.NewClient(
		c.appID,
		c.appSecret,
		ws.WithEventHandler(d),
		ws.WithLogLevel(larkcore.LogLevelInfo),
		ws.WithAutoReconnect(true),
	)
	return wsClient.Start(ctx)
}

// handleEvent 处理飞书消息接收事件，过滤非私聊和非文本消息。
func (c *Channel) handleEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if c.handler == nil {
		return nil
	}
	if event.Event == nil || event.Event.Message == nil {
		return nil
	}
	msg := event.Event.Message

	// 仅处理私聊消息（p2p），忽略群聊
	if msg.ChatType == nil || *msg.ChatType != "p2p" {
		return nil
	}
	// 仅处理文本消息
	if msg.MessageType == nil || *msg.MessageType != "text" {
		log.Printf("[feishu] 忽略非文本消息: type=%v", msg.MessageType)
		return nil
	}
	if msg.Content == nil {
		return nil
	}

	// 解析文本内容（格式：{"text":"..."}）
	var textMsg struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &textMsg); err != nil || textMsg.Text == "" {
		return nil
	}

	chatID := derefStr(msg.ChatId)
	messageID := derefStr(msg.MessageId)

	senderID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = derefStr(event.Event.Sender.SenderId.OpenId)
	}

	log.Printf("[feishu] 收到私聊消息 | chatID=%s senderID=%s msgID=%s", chatID, senderID, messageID)

	c.handler(ctx, imchannel.IncomingMessage{
		ChatID:    chatID,
		SenderID:  senderID,
		Text:      textMsg.Text,
		MessageID: messageID,
	})
	return nil
}

// derefStr 安全解引用 *string，nil 时返回空字符串。
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
