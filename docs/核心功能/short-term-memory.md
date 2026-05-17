# Short-Term Memory 短期记忆实现原理

## 1. 背景与目标

harness9 的 `runLoop` 原本将 `contextHistory` 声明为局部变量，每次 `Run()` 调用全新初始化，会话间完全无法延续。用户在 TUI 中每条输入对 Agent 而言都是全新任务，无法基于前轮对话继续工作。

Short-Term Memory 模块解决了以下问题：

- **会话历史跨 `Run()` 调用持久化**：对话记录写入 SQLite，进程重启后仍可恢复
- **上下文压缩**：防止历史消息无限增长超出 LLM 上下文窗口
- **TUI 会话管理**：`/new` 新建会话，`/resume` 切换历史会话，状态栏实时显示会话信息

---

## 2. 架构总览

```
┌──────────────────────────────────────────────────────────────────┐
│                          AgentEngine                              │
│                                                                  │
│   WithSession(sess)  ──►  session memory.Session                 │
│   WithCompactor(c)   ──►  compactor memory.Compactor             │
│                                                                  │
│   runLoop():                                                     │
│     loadHistoryWith()  ← 从 Session 加载 + 注入 system prompt      │
│     applyCompactionWith()  ← 每次 LLM 调用前裁剪                  │
│     saveHistoryWith()  ← 结束后写回新增消息                        │
└──────────────┬───────────────────────────────────────────────────┘
               │
               ▼
┌──────────────────────────────────────────────────────────────────┐
│                       internal/memory/                            │
│                                                                  │
│  Session (interface)          Manager                            │
│  ├── GetMessages(limit)       ├── NewSession()   → SQLiteSession │
│  ├── AddMessages(msgs)        ├── OpenSession(id)→ SQLiteSession │
│  ├── PopMessage()             ├── ListSessions() → []SessionInfo │
│  └── Clear()                 └── DeleteSession()                │
│                                                                  │
│  SQLiteSession (主实现)        MemorySession (测试用)             │
│  ├── WAL 模式 SQLite           └── sync.Mutex + []Message        │
│  ├── 事务性 AddMessages                                           │
│  └── tool_calls JSON 序列化   Compactor (interface)              │
│                               └── SlidingWindowCompactor         │
└──────────────────────────────────────────────────────────────────┘
               │
               ▼
     ~/.harness9/sessions.db  (SQLite 持久化文件)
```

---

## 3. 包结构

```
internal/memory/
├── session.go          # Session 接口 + SessionInfo 类型定义
├── manager.go          # Manager：SQLite 连接持有者 + 会话 CRUD
├── sqlite_session.go   # SQLiteSession：SQLite 持久化实现
├── mem_session.go      # MemorySession：纯内存实现（测试用）
└── compaction.go       # Compactor 接口 + SlidingWindowCompactor
```

---

## 4. 核心接口

### 4.1 Session 接口

```go
// Session 管理单个会话的消息历史。
// 接口定义在 memory 包（使用者侧），SQLiteSession / MemorySession 实现。
type Session interface {
    SessionID() string
    // GetMessages 返回历史消息；limit=0 返回全部，limit>0 返回最近 limit 条。
    GetMessages(ctx context.Context, limit int) ([]schema.Message, error)
    // AddMessages 追加新消息到会话历史。
    AddMessages(ctx context.Context, msgs []schema.Message) error
    // PopMessage 删除并返回最新一条消息（undo 用）；无消息时返回 nil, nil。
    PopMessage(ctx context.Context) (*schema.Message, error)
    // Clear 清空会话历史。
    Clear(ctx context.Context) error
}
```

### 4.2 Compactor 接口

```go
// Compactor 在将历史消息注入 LLM 上下文前进行裁剪，防止超出上下文窗口。
type Compactor interface {
    Compact(msgs []schema.Message) []schema.Message
}
```

### 4.3 Manager

```go
type Manager struct{ db *sql.DB }

func NewManager(dbPath string) (*Manager, error)
func (m *Manager) NewSession(ctx context.Context) (Session, error)
func (m *Manager) OpenSession(ctx context.Context, id string) (Session, error)
func (m *Manager) ListSessions(ctx context.Context) ([]SessionInfo, error)
func (m *Manager) DeleteSession(ctx context.Context, id string) error
func (m *Manager) Close() error
```

`Manager` 是整个进程的单一 SQLite 连接持有者，所有 `SQLiteSession` 共享同一个 `*sql.DB`。

---

## 5. SQLite Schema

持久化路径：`~/.harness9/sessions.db`

```sql
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    PRIMARY KEY,   -- UUID v4
    created_at INTEGER NOT NULL,      -- Unix timestamp（秒）
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT    NOT NULL,
    role         TEXT    NOT NULL,    -- 'system'|'user'|'assistant'
    content      TEXT    NOT NULL,
    tool_calls   TEXT,                -- JSON，仅 assistant 消息有值
    tool_call_id TEXT,                -- 仅 Observation（user）消息有值
    created_at   INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id);
```

**关键设计决策：**

- **WAL 模式**：`journal_mode=WAL` 允许并发读写，适合 TUI + 引擎同时访问
- **`tool_calls` 列**：存储 `[]schema.ToolCall` 的 JSON 序列化，读取时反序列化，与现有类型完全对齐
- **System Prompt 不持久化**：system 消息每次由 `loadHistoryWith` 重新注入，不写入 DB，保持 prompt 可随配置更新而变化
- **ON DELETE CASCADE**：删除会话时自动清理所有关联消息

---

## 6. SQLiteSession 实现要点

### 6.1 GetMessages

```go
// limit=0：全量升序查询
SELECT role, content, tool_calls, tool_call_id
FROM messages WHERE session_id = ?
ORDER BY id ASC

// limit>0：先 DESC LIMIT，再内存反转，获得"最近 N 条按时间升序"
SELECT ... ORDER BY id DESC LIMIT ?
// → 内存反转 → 升序
```

### 6.2 AddMessages（事务）

单次 `AddMessages` 将批量消息和 `sessions.updated_at` 更新封装在同一个事务中，确保原子性：

```
BEGIN TX
  INSERT INTO messages ...（多条）
  UPDATE sessions SET updated_at = ? WHERE id = ?
COMMIT
```

### 6.3 PopMessage（原子删除）

为防止数据丢失，先在事务内反序列化 `tool_calls`，再执行删除：

```
BEGIN TX
  SELECT ... ORDER BY id DESC LIMIT 1
  反序列化 tool_calls（失败则 ROLLBACK，消息不丢失）
  DELETE FROM messages WHERE id = ?
COMMIT
```

### 6.4 NULL 值处理

`tool_calls` 和 `tool_call_id` 均使用 `sql.NullString`：仅 assistant 消息有 `tool_calls`，仅 Observation（user）消息有 `tool_call_id`。

---

## 7. SlidingWindowCompactor 算法

配置：`MaxMessages int`（默认 100，含 system prompt）

```
输入：msgs = [system, msg1, msg2, ..., msgN]

1. 若 len(msgs) ≤ MaxMessages，直接返回原切片（无需压缩）
2. 计算窗口起点：startIdx = len(msgs) - MaxMessages + 1
3. 【边界修正】向前回溯孤立的 Observation：
   while startIdx > 1 AND msgs[startIdx].ToolCallID != "" {
       startIdx--
   }
   // 确保窗口第一条消息不是孤立的工具结果（Observation），
   // 否则 LLM API 无法将其与工具调用请求关联。
4. 返回：[msgs[0]] + msgs[startIdx:]
```

**示意（MaxMessages=4）**：

```
原始：[system][user:提问][assistant:tool_calls=read][user:tool_call_id=x][assistant:回答][user:继续]
步骤2：startIdx = 6-4+1 = 3 → msgs[3]=Observation（ToolCallID≠""）
步骤3：回溯 → startIdx=2 → msgs[2]=assistant（有 tool_calls）→ 停止
结果：[system][assistant:read][user:tool_call_id=x][assistant:回答][user:继续]  （5条）
```

**为何要回溯孤立 Observation？**

LLM API（尤其是 Anthropic）要求工具执行结果（`ToolCallID != ""`）必须能找到配对的工具调用请求（`ToolCalls`）。若滑动窗口把 Observation 保留但截断了其对应的 `tool_calls` 消息，API 调用会报错。回溯操作以少量额外消息换取上下文完整性。

---

## 8. AgentEngine 集成

### 8.1 新增 Option 与方法

```go
// internal/engine/agent_loop.go

type AgentEngine struct {
    // ...现有字段
    mu        sync.RWMutex     // 保护下面两个字段，防止与 TUI goroutine 竞争
    session   memory.Session   // 可选，nil 表示无持久化
    compactor memory.Compactor // 可选，nil 表示不压缩
}

func WithSession(s memory.Session) Option
func WithCompactor(c memory.Compactor) Option

// SetSession 替换当前会话，供 TUI /new、/resume 切换时调用。
// 线程安全：通过 sync.RWMutex 保护。
func (e *AgentEngine) SetSession(s memory.Session)
```

### 8.2 runLoop 改动（三处）

```go
func (e *AgentEngine) runLoop(ctx context.Context, userPrompt string, ...) error {
    // 在循环开始时快照，避免与 TUI goroutine 的 SetSession 产生竞争
    e.mu.RLock()
    sess := e.session
    comp := e.compactor
    e.mu.RUnlock()

    // ① 启动：从 Session 加载历史 + 注入当前用户输入
    contextHistory, startLen := e.loadHistoryWith(ctx, userPrompt, sess)

    for {
        // ② 每轮 LLM 调用前：应用压缩策略
        responseMsg, err := em.generate(ctx, turnCount,
            e.applyCompactionWith(comp, contextHistory), availableTools)
        // ...
    }

    // ③ 结束：将本次新增消息写回 Session
    e.saveHistoryWith(ctx, sess, contextHistory, startLen)
}
```

**私有辅助方法语义：**

| 方法 | session=nil 时 | 说明 |
|------|---------------|------|
| `loadHistoryWith` | 创建全新 `[system, user]` | 退化为原有行为 |
| `applyCompactionWith` | 原样返回 msgs | 不压缩 |
| `saveHistoryWith` | no-op | 失败仅打 warning，不中断 |

**system prompt 不持久化的设计原因：**

`startLen` 在注入 system 消息之后、追加用户输入之前记录。`saveHistoryWith` 保存 `msgs[startLen:]`，即 `[user_prompt, assistant_response, observations...]`，system 消息始终被跳过。这样 system prompt 可随配置（`PromptBuilder`、`AGENTS.md`）更新而变化，不受历史数据约束。

### 8.3 并发安全

`SetSession` 由 TUI goroutine 调用（用户输入 `/new`/`/resume`），`runLoop` 由引擎 goroutine 调用。两者通过 `sync.RWMutex` 保护：

```go
// TUI goroutine（写）
func (e *AgentEngine) SetSession(s memory.Session) {
    e.mu.Lock()
    e.session = s
    e.mu.Unlock()
}

// 引擎 goroutine（读，在 runLoop 开始时快照）
e.mu.RLock()
sess := e.session
e.mu.RUnlock()
```

`runLoop` 内部只操作本地快照 `sess`，不再读取 `e.session`，彻底消除竞争。

---

## 9. TUI 集成

### 9.1 tuiModel 新增字段

```go
type tuiModel struct {
    // ...现有字段

    // Session 管理
    manager         *memory.Manager   // 共享 SQLite 连接，整个进程唯一实例
    session         memory.Session    // 当前活跃会话
    sessionID       string            // UUID 前 8 位，用于状态栏显示
    sessionMsgCount int               // 当前会话消息条数

    // /resume 选择模式
    resumeSelecting bool
    resumeSessions  []memory.SessionInfo
}
```

### 9.2 斜杠命令

| 命令 | 行为 |
|------|------|
| `/new` | 调用 `manager.NewSession()`，替换 `session`，调用 `eng.SetSession()`，重置 `sessionMsgCount`，状态栏刷新 |
| `/resume` | 调用 `manager.ListSessions()`，展示最近 10 条会话列表，进入序号选择模式 |

`/resume` 交互流：

```
可用会话（3 条）：
  [1] f3a2c1b0  2026-05-17 14:30  23 条消息
  [2] 9c1b77a2  2026-05-16 09:15  41 条消息
  [3] 8d4f2e01  2026-05-15 21:00  7 条消息
输入序号选择（非数字 Enter 取消）：
```

`/resume` 进入"选择模式"（`resumeSelecting = true`），下一次 `KeyEnter` 被 `handleResumeSelection` 拦截，`strconv.Atoi` 解析序号，非数字输入则取消。

### 9.3 状态栏

```
[harness9] gpt-4o-mini  workdir: /your/project  │  session: f3a2c1b0  msgs: 23
```

`sessionMsgCount` 在每次 `EventDone` 事件后通过 `msgCountMsg tea.Cmd` 异步刷新：

```go
// EventDone 分支
return m, tea.Batch(textinput.Blink, m.refreshMsgCount())

// refreshMsgCount 返回的 tea.Cmd
func (m tuiModel) refreshMsgCount() tea.Cmd {
    sess := m.session
    return func() tea.Msg {
        msgs, _ := sess.GetMessages(context.Background(), 0)
        return msgCountMsg(len(msgs))
    }
}

// Update switch
case msgCountMsg:
    m.sessionMsgCount = int(msg)
```

### 9.4 main.go 初始化

```go
// 初始化 Memory Manager 和首个 Session
homeDir, _ := os.UserHomeDir()
mgr, err := memory.NewManager(filepath.Join(homeDir, ".harness9", "sessions.db"))
defer mgr.Close()

sess, err := mgr.NewSession(ctx)

eng := engine.NewAgentEngine(llm, registry, workDir,
    engine.WithPromptBuilder(promptBuilder),
    engine.WithSession(sess),
    engine.WithCompactor(&memory.SlidingWindowCompactor{MaxMessages: 100}),
)
```

---

## 10. 设计决策总结

| 决策 | 原因 |
|------|------|
| **SQLite 而非内存存储** | 进程重启后会话可恢复，`/resume` 功能依赖持久化 |
| **纯 Go SQLite 驱动（modernc.org/sqlite）** | 无 CGo 依赖，与 harness9 零 CGo 目标一致，交叉编译友好 |
| **System Prompt 不持久化** | Prompt 可随配置（PromptBuilder/AGENTS.md）更新而变化，不应被历史数据锁定 |
| **Compactor 接口可插拔** | 首期 SlidingWindowCompactor，后续可扩展 TokenBudgetCompactor、LLM摘要压缩等 |
| **孤立 Observation 回溯** | 保证 LLM API 收到完整的工具调用上下文，避免 API 报错 |
| **sync.RWMutex 快照** | runLoop 在循环开始时一次性快照 session/compactor，避免与 TUI 的 SetSession 产生数据竞争 |
| **失败 warning 不中断** | saveHistoryWith 失败不应影响主流程；持久化是增强功能，不是核心依赖 |
| **msgCountMsg 异步刷新** | 避免在 Bubbletea Update 中做同步 I/O，符合 Elm 架构的无副作用 Update 原则 |

---

## 11. 不在本期范围内

| 功能 | 优先级 | 说明 |
|------|--------|------|
| LLM-based 摘要压缩 | P3 | 需要额外 LLM 调用，成本较高 |
| Token Budget 压缩 | P2 | 需要 token 估算能力（tiktoken 等） |
| FTS5 全文会话搜索 | P3 | `/search` 命令，搜索历史对话 |
| TTL 自动过期清理 | P3 | 定期清除旧会话，控制磁盘占用 |
| CLI 模式 session 支持 | P3 | CLI 当前为无状态 REPL，暂不改动 |
