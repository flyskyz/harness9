# IM 渠道接入

## 1. 概述

harness9 通过 `imchannel` 包将 Agent 引擎接入即时通讯（IM）平台。当前实现支持**飞书（Lark）**私聊，采用 WebSocket 长连接接收消息，无需公网 IP 或内网穿透。

架构分三层：

```
┌─────────────────────────────────────────────────┐
│              cmd/harness9/server.go              │
│               Server 编排层                       │
│   IMChannel 事件 → AgentEngine.RunStream → Session│
└────────────┬────────────────────┬───────────────┘
             │                    │
┌────────────▼──────┐  ┌──────────▼──────────────┐
│  imchannel/       │  │  engine.AgentEngine       │
│  IMChannel 接口    │  │  RunStream → chan Event   │
│  Session 接口      │  └─────────────────────────┘
└────────────┬──────┘
             │ 飞书实现
┌────────────▼──────────────────────────────────┐
│  imchannel/feishu/                              │
│  Channel（WebSocket 接收）                       │
│  Session（独立文本消息推送进度）                   │
└───────────────────────────────────────────────┘
```

## 2. 核心接口设计（`internal/imchannel/channel.go`）

### IMChannel — IM 平台统一适配接口

```go
type IMChannel interface {
    // Start 建立长连接并阻塞直到 ctx 取消，支持自动重连。
    Start(ctx context.Context) error

    // SetMessageHandler 注册用户消息到达回调，必须在 Start 之前调用。
    SetMessageHandler(handler MessageHandler)

    // NewSession 为一条入站消息创建独立会话。
    NewSession(chatID, messageID string) Session
}
```

### Session — 单次交互的 IM 侧视图

| 方法 | 触发时机 | 说明 |
|------|---------|------|
| `NotifyThinking(ctx)` | Agent 开始处理 | 发送"思考中"占位消息 |
| `UpdateThinkingContent(ctx, text)` | Thinking 阶段结束 | 推送思考摘要（text 为空时跳过） |
| `NotifyToolStart(ctx, tc)` | `EventToolStart` 到达 | 告知用户工具开始执行 |
| `NotifyToolDone(ctx, tc, result, d)` | `EventToolResult` 到达 | 告知用户工具执行完成及耗时 |
| `SendReply(ctx, text)` | `EventDone` 到达 | 发送 Agent 最终回复（调用方保证 text 非空） |

**接口契约**：`SendReply` 的调用方（Server 编排层）负责提供非空文本，若 Agent 静默完成（reply 和 thinking 均为空），Server 填入兜底文本 `"✅ 任务完成"`，`Session` 实现无需处理空字符串。

### MessageHandler — 消息回调签名

```go
type MessageHandler func(ctx context.Context, msg IncomingMessage)
```

`IncomingMessage` 携带 `ChatID`、`SenderID`、`Text`、`MessageID`，供 Server 启动 Agent 循环和创建 Session 使用。

## 3. 飞书实现（`internal/imchannel/feishu/`）

### 3.1 Channel（`client.go`）

基于飞书官方 Go SDK（`github.com/larksuite/oapi-sdk-go/v3`）的 WebSocket 长连接模式。

**初始化**：

```go
ch := feishu.NewChannel(appID, appSecret)
```

**连接模型**：

```
飞书服务器
  │  WebSocket 长连接（飞书推）
  ▼
Channel.Start(ctx)
  │  事件分发（dispatcher）
  ▼
handleEvent()  ← 过滤：仅 p2p + text 类型
  │
  ▼
MessageHandler(ctx, IncomingMessage)
```

过滤规则：
- `chat_type != "p2p"` → 忽略（只处理私聊）
- `message_type != "text"` → 忽略（只处理文本）
- `content` 为空或 JSON 解析失败 → 忽略

### 3.2 Session（`session.go`）

进度展示策略：**每个生命周期事件发送一条独立文本消息**，不使用飞书 Patch API（后者仅支持交互式卡片）。

消息序列：

```
🤔 思考中...
💭 <思考摘要，最多 400 Unicode 字符>
🔧 调用工具：bash
✅ bash（123ms）
<最终回复文本>
```

若工具执行失败：

```
🔧 调用工具：bash
❌ bash（45ms）
<后续回复或错误说明>
```

**并发安全**：`Session` 本身无可变状态，`sendText` 每次均为独立 HTTP 请求，多 goroutine 同时调用不同方法（如并发工具执行期间的 `NotifyToolStart`）是安全的。消息到达用户侧的顺序由飞书服务端决定，不保证与发送顺序严格一致。

## 4. Server 编排层（`cmd/harness9/bot.go`）

Server 是 IMChannel 与 AgentEngine 之间的桥接层。

### 4.1 消息处理流程

```
IMChannel.MessageHandler 回调
  │
  ▼
Server.Start 启动独立 goroutine（超时 5 分钟）
  │
  ▼
Server.handleMessage(ctx, msg)
  │  1. NewSession → NotifyThinking
  │  2. RunStream → <-chan Event
  │  3. 事件循环（见下表）
  └──→ Session.SendReply（最终回复）
```

### 4.2 事件映射规则

| Event 类型 | Server 行为 | 说明 |
|---|---|---|
| `EventThinkingDelta` | 累积到 `lastThinking`，Turn 变化时重置 | 实现 Thinking 内容的跨 token 拼接 |
| `EventActionDelta` | 累积到 `reply` | 收集最终回复文本 |
| `EventToolStart` | `flushThinking()` + 重置 `reply` + 记录工具元信息 + `NotifyToolStart` | 先推送思考内容，再告知工具开始 |
| `EventToolResult` | 查询工具元信息，计算耗时（零值时返回 0），`NotifyToolDone` | 防御性处理缺失元信息 |
| `EventDone` | reply 非空 → `flushThinking` + `SendReply(reply)`；reply 为空 → `SendReply(thinking/兜底)` | Two-Stage ReAct 退化兜底 |
| `EventError` | `SendReply("❌ " + errMsg)` | 错误文本直接展示给用户 |

### 4.3 关键设计决策

**flushThinking 的幂等性**：

`flushThinking` 函数通过 `thinkingFlushed` 标记保证每轮 Turn 最多推送一次思考内容，防止在 `EventToolStart` 和 `EventDone` 都可能触发 `flushThinking` 的情况下重复发送。

**reply 的重置时机**：

当 `EventToolStart` 到达时，当前 Action 阶段的文本属于中间步骤而非最终回复，`reply` 被重置。只有不含 ToolCall 的 Action 文本才是最终回复，这与引擎的"无工具调用 → 终止"逻辑一致。

**Two-Stage ReAct 退化处理**：

在某些场景下（如任务非常简单），模型可能将完整回答放在 Thinking 阶段，Action 阶段返回空内容。此时 `EventDone` 到达时 `reply` 为空，Server 直接将 `lastThinking.String()` 作为最终回复发送，不再额外调用 `UpdateThinkingContent`，避免同一内容出现两次。

**per-message 超时隔离**：

每条消息的处理 goroutine 使用从 server ctx 派生的独立子 context，超时 5 分钟。服务器关闭时，所有进行中的 Agent 循环会通过 ctx 链路级联取消。

## 5. 环境变量配置

| 变量 | 必需 | 说明 |
|------|:---:|------|
| `FEISHU_APP_ID` | ✅ | 飞书应用 App ID（如 `cli_xxxxxxxxxxxxxxxx`） |
| `FEISHU_APP_SECRET` | ✅ | 飞书应用 App Secret |
| `OPENAI_API_KEY` | ✅ | LLM Provider API Key |
| `OPENAI_BASE_URL` | 可选 | 自定义 OpenAI 兼容端点（默认官方地址） |
| `LLM_MODEL` | 可选 | 模型名称（默认 `openai/gpt-4o-mini`） |
| `WORK_DIR` | 可选 | Agent 工具沙箱根目录（默认进程工作目录） |

配置通过 `.env` 文件或系统环境变量提供。系统环境变量优先于 `.env` 文件。

## 6. 扩展新 IM 平台

1. 在 `internal/imchannel/` 下创建新目录（如 `slack/`）
2. 实现 `IMChannel` 接口（`Start` + `SetMessageHandler` + `NewSession`）
3. 实现 `Session` 接口（`NotifyThinking` / `UpdateThinkingContent` / `NotifyToolStart` / `NotifyToolDone` / `SendReply`）
4. 在 `cmd/harness9/main.go` 中替换 `feishu.NewChannel(...)` 为新实现

Server 编排层（`server.go`）无需任何修改，它仅依赖 `IMChannel` 和 `Session` 接口。

## 7. 已知限制

| 限制 | 说明 |
|------|------|
| 仅支持飞书私聊 | 群聊消息被过滤，需要 @Bot 触发等群聊逻辑需另行实现 |
| 消息顺序不严格保证 | 并发工具执行期间的进度消息顺序取决于飞书服务端接收顺序 |
| 无会话记忆 | 每条用户消息触发独立 Agent 循环，无跨消息历史持久化 |
| 模型为硬编码配置 | 通过 `LLM_MODEL` 环境变量配置，所有飞书用户共用同一个模型 |
