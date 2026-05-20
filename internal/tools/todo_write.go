package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/schema"
)

// TodoWriteTool 允许 LLM 维护当前会话的任务列表。
// 传入 todos 时全量替换；省略 todos 时读取当前列表。
type TodoWriteTool struct {
	store *planning.TodoStore
}

// NewTodoWriteTool 创建绑定到指定 TodoStore 的工具实例。
func NewTodoWriteTool(store *planning.TodoStore) *TodoWriteTool {
	return &TodoWriteTool{store: store}
}

func (t *TodoWriteTool) Name() string { return "todo_write" }

func (t *TodoWriteTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: "todo_write",
		Description: "维护当前会话的任务清单。" +
			"提供 todos 数组时全量替换（atomic replace）；省略 todos 时读取当前列表。\n" +
			"当任务涉及 3 个或以上独立步骤时，在开始前调用此工具记录任务列表，" +
			"并在每完成一步后立即更新对应条目的 status 为 in_progress 或 completed。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"todos": map[string]interface{}{
					"type":        "array",
					"description": "完整的任务列表（全量替换）。省略此字段则仅读取当前列表。",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":      map[string]interface{}{"type": "string"},
							"content": map[string]interface{}{"type": "string"},
							"status": map[string]interface{}{
								"type": "string",
								"enum": []string{"pending", "in_progress", "completed", "cancelled"},
							},
						},
						"required": []string{"id", "content", "status"},
					},
				},
			},
		},
	}
}

type todoWriteArgs struct {
	Todos []planning.TodoItem `json:"todos"`
}

func (t *TodoWriteTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input todoWriteArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	var current []planning.TodoItem
	if len(input.Todos) > 0 {
		// 批量作弊防护规则：
		//   - pending/new → completed：单次调用最多允许 1 个（LLM 完成工作后可跳过 in_progress）
		//   - cancelled → completed：始终拒绝（必须先恢复为 pending/in_progress）
		//   - in_progress/completed → completed：始终允许
		prev := t.store.Read()
		prevStatus := make(map[string]planning.TodoStatus, len(prev))
		for _, item := range prev {
			prevStatus[item.ID] = item.Status
		}
		var directCompletions int
		for _, item := range input.Todos {
			if item.Status != planning.TodoCompleted {
				continue
			}
			prior, exists := prevStatus[item.ID]
			if !exists || prior == planning.TodoPending {
				// 新条目或 pending → completed：计入直接完成数，超过 1 个则拒绝。
				directCompletions++
				continue
			}
			if prior == planning.TodoCancelled {
				// cancelled → completed 明确拒绝：取消的任务必须先恢复为 pending 或 in_progress。
				return "", fmt.Errorf(
					"任务 %q 已取消，不能直接标记为 completed；如需重新执行，请先将其恢复为 pending 或 in_progress。",
					item.ID)
			}
			// prior == in_progress 或 completed → 合法路径，不计入。
		}
		if directCompletions > 1 {
			return "", fmt.Errorf(
				"不允许在一次调用中将 %d 个任务直接标记为 completed（未经 in_progress）。"+
					"请逐一处理：每次仅完成一项实际工作后更新该条目状态。",
				directCompletions)
		}
		current = t.store.Write(input.Todos)
	} else {
		current = t.store.Read()
	}

	if current == nil {
		current = []planning.TodoItem{}
	}

	b, err := json.Marshal(current)
	if err != nil {
		return "", fmt.Errorf("序列化任务列表失败: %w", err)
	}
	return string(b), nil
}
