package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/harness9/internal/engine"
	"github.com/harness9/internal/imchannel"
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
		// 每条消息使用从 server ctx 派生的独立子 context，
		// 超时设为 5 分钟，server 关闭时所有进行中的处理也会随之取消。
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
	log.Printf("[server] 处理消息 | chatID=%s text=%.50s", msg.ChatID, msg.Text)

	session := s.channel.NewSession(msg.ChatID, msg.MessageID)

	if err := session.NotifyThinking(ctx); err != nil {
		log.Printf("[server] NotifyThinking 失败: %v", err)
	}

	stream, err := s.eng.RunStream(ctx, msg.Text)
	if err != nil {
		_ = session.SendReply(ctx, fmt.Sprintf("❌ 启动失败：%v", err))
		return
	}

	// toolCalls 和 toolStartTimes 记录每个工具调用的元信息，
	// 用于在 EventToolResult 到达时还原工具名称和计算耗时。
	// 这两个 map 仅在当前 goroutine 中访问，无需加锁。
	toolCalls := make(map[string]schema.ToolCall)
	toolStartTimes := make(map[string]time.Time)

	// reply 累积 Action 阶段的文本（最终回复）。
	// 每当本 Turn 出现工具调用时重置，确保只保留无工具调用的最后一个 Action 响应。
	var reply strings.Builder
	// lastThinking 保存最近一轮 Thinking 阶段的文本。
	// Two-Stage ReAct 中，模型有时将完整回答放在 Thinking 阶段，Action 阶段返回空内容。
	var lastThinking strings.Builder
	// lastThinkingTurn 记录当前累积的是哪一轮的思考文本，Turn 变化时重置缓冲区。
	var lastThinkingTurn int
	// thinkingFlushed 标记当前 Turn 的思考内容是否已推送至进度消息。
	// 每轮 Turn 开始时重置，避免同一 Turn 的思考内容重复推送。
	var thinkingFlushed bool

	// flushThinking 将当前 Turn 的思考内容推送到进度消息（每轮最多调用一次）。
	// 在思考→工具调用（EventToolStart）和思考→完成（EventDone）两处触发。
	flushThinking := func() {
		if thinkingFlushed || lastThinking.Len() == 0 {
			return
		}
		if err := session.UpdateThinkingContent(ctx, lastThinking.String()); err != nil {
			log.Printf("[server] UpdateThinkingContent 失败: %v", err)
		}
		thinkingFlushed = true
	}

	for evt := range stream {
		switch evt.Type {
		case engine.EventThinkingDelta:
			// 每轮 Thinking 开始时重置，只保留最近一轮的思考内容。
			if evt.Turn != lastThinkingTurn {
				lastThinking.Reset()
				lastThinkingTurn = evt.Turn
				thinkingFlushed = false
			}
			if text, ok := evt.Data.(string); ok {
				lastThinking.WriteString(text)
			}

		case engine.EventActionDelta:
			if text, ok := evt.Data.(string); ok {
				reply.WriteString(text)
			}

		case engine.EventToolStart:
			// Thinking 阶段已完成，先将思考内容推送到进度消息，再追加工具调用行。
			flushThinking()
			// 本 Turn 有工具调用，该轮 Action 文本不是最终回复，重置。
			reply.Reset()
			if tc, ok := evt.Data.(schema.ToolCall); ok {
				toolCalls[tc.ID] = tc
				toolStartTimes[tc.ID] = time.Now()
				if err := session.NotifyToolStart(ctx, tc); err != nil {
					log.Printf("[server] NotifyToolStart 失败 (tool=%s): %v", tc.Name, err)
				}
			}

		case engine.EventToolResult:
			if result, ok := evt.Data.(schema.ToolResult); ok {
				tc := toolCalls[result.ToolCallID]
				d := time.Since(toolStartTimes[result.ToolCallID])
				if err := session.NotifyToolDone(ctx, tc, result, d); err != nil {
					log.Printf("[server] NotifyToolDone 失败 (tool=%s): %v", tc.Name, err)
				}
			}

		case engine.EventDone:
			finalText := reply.String()
			if finalText != "" {
				// Action 有文本：先推送思考内容（如有），再发送 Action 文本作为最终回复。
				flushThinking()
				if err := session.SendReply(ctx, finalText); err != nil {
					log.Printf("[server] SendReply 失败: %v", err)
				}
			} else {
				// Action 为空：thinking 本身就是最终回复（Two-Stage ReAct 的退化情况），
				// 直接发送不再额外推送 UpdateThinkingContent，避免同一内容出现两次。
				if err := session.SendReply(ctx, lastThinking.String()); err != nil {
					log.Printf("[server] SendReply 失败: %v", err)
				}
			}

		case engine.EventError:
			errMsg := fmt.Sprintf("%v", evt.Data)
			log.Printf("[server] Agent 执行错误: %s", errMsg)
			if err := session.SendReply(ctx, "❌ "+errMsg); err != nil {
				log.Printf("[server] SendReply(error) 失败: %v", err)
			}
		}
	}
}
