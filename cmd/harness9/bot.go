package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/imchannel"
	"github.com/harness9/internal/logfmt"
	"github.com/harness9/internal/schema"
)

// Server 是 IMChannel 与 AgentEngine 之间的编排层。
//
// 职责：
//   - 监听 IMChannel 的入站消息
//   - 为每条消息启动独立的 Agent 执行循环（goroutine）
//   - 将 RunStream 事件流映射到 Session 进度推送方法
//
// 每条消息独立处理，无跨消息状态共享。
type Server struct {
	channel imchannel.IMChannel
	eng     *engine.AgentEngine
}

// NewServer 创建 Server，将 IMChannel 与 AgentEngine 组合在一起。
func NewServer(ch imchannel.IMChannel, eng *engine.AgentEngine) *Server {
	return &Server{channel: ch, eng: eng}
}

// Start 注册消息处理器并启动 IMChannel 长连接（阻塞直到 ctx 取消）。
func (s *Server) Start(ctx context.Context) error {
	s.channel.SetMessageHandler(func(_ context.Context, msg imchannel.IncomingMessage) {
		// 每条消息使用从 server ctx 派生的独立子 context，超时 5 分钟。
		msgCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		go func() {
			defer cancel()
			s.handleMessage(msgCtx, msg)
		}()
	})
	return s.channel.Start(ctx)
}

// handleMessage 处理单条用户消息：启动 Agent 循环，将事件流翻译为 Session 进度推送。
func (s *Server) handleMessage(ctx context.Context, msg imchannel.IncomingMessage) {
	log.Print(logfmt.FormatMsg("server", fmt.Sprintf("处理消息 │ chatID=%s text=%.50s", msg.ChatID, msg.Text)))

	session := s.channel.NewSession(msg.ChatID, msg.MessageID)

	if err := session.NotifyThinking(ctx); err != nil {
		log.Print(logfmt.FormatMsg("server", fmt.Sprintf("NotifyThinking 失败: %v", err)))
	}

	stream, err := s.eng.RunStream(ctx, msg.Text)
	if err != nil {
		log.Print(logfmt.FormatMsg("server", fmt.Sprintf("RunStream 启动失败: %v", err)))
		if replyErr := session.SendReply(ctx, fmt.Sprintf("❌ 启动失败：%v", err)); replyErr != nil {
			log.Print(logfmt.FormatMsg("server", fmt.Sprintf("SendReply 失败: %v", replyErr)))
		}
		return
	}

	// toolCalls / toolStartTimes 记录工具元信息，用于在 EventToolResult 时还原工具名和计算耗时。
	toolCalls := make(map[string]schema.ToolCall)
	toolStartTimes := make(map[string]time.Time)

	// reply 累积当前 Turn 的 Action 文本。遇到工具调用时重置，确保只保留最终回复。
	var reply strings.Builder

	for evt := range stream {
		switch evt.Type {
		case engine.EventActionDelta:
			if text, ok := evt.Data.(string); ok {
				reply.WriteString(text)
			}

		case engine.EventToolStart:
			reply.Reset() // 本 Turn 有工具调用，Action 文本不是最终回复
			if tc, ok := evt.Data.(schema.ToolCall); ok {
				toolCalls[tc.ID] = tc
				toolStartTimes[tc.ID] = time.Now()
				if err := session.NotifyToolStart(ctx, tc); err != nil {
					log.Print(logfmt.FormatMsg("server", fmt.Sprintf("NotifyToolStart 失败 (tool=%s): %v", tc.Name, err)))
				}
			}

		case engine.EventToolResult:
			if result, ok := evt.Data.(schema.ToolResult); ok {
				tc := toolCalls[result.ToolCallID]
				var d time.Duration
				if startTime, found := toolStartTimes[result.ToolCallID]; found {
					d = time.Since(startTime)
				}
				if err := session.NotifyToolDone(ctx, tc, result, d); err != nil {
					log.Print(logfmt.FormatMsg("server", fmt.Sprintf("NotifyToolDone 失败 (tool=%s): %v", tc.Name, err)))
				}
			}

		case engine.EventDone:
			finalText := reply.String()
			if finalText == "" {
				finalText = "✅ 任务完成"
			}
			if err := session.SendReply(ctx, finalText); err != nil {
				log.Print(logfmt.FormatMsg("server", fmt.Sprintf("SendReply 失败: %v", err)))
			}

		case engine.EventError:
			errMsg := fmt.Sprintf("%v", evt.Data)
			log.Print(logfmt.FormatMsg("server", fmt.Sprintf("Agent 执行错误: %s", errMsg)))
			if err := session.SendReply(ctx, "❌ "+errMsg); err != nil {
				log.Print(logfmt.FormatMsg("server", fmt.Sprintf("SendReply 失败: %v", err)))
			}
		}
	}
}
