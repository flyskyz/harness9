package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/harness9/internal/planning"
	"github.com/harness9/internal/tools"
)

func TestTodoWriteTool_Name(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)
	if tool.Name() != "todo_write" {
		t.Errorf("Name() = %q, want todo_write", tool.Name())
	}
}

func TestTodoWriteTool_Write(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	args, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "step one", "status": "pending"},
			{"id": "2", "content": "step two", "status": "in_progress"},
		},
	})

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should be JSON of the current list
	var got []planning.TodoItem
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result not valid JSON: %v — got %q", err, result)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d", len(got))
	}
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Errorf("unexpected items: %+v", got)
	}

	// Store should be updated
	stored := store.Read()
	if len(stored) != 2 {
		t.Fatalf("store has %d items, want 2", len(stored))
	}
}

func TestTodoWriteTool_Read_WhenNoTodos(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// Omit todos field → read current (empty) list
	args, _ := json.Marshal(map[string]interface{}{})
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// Should return "[]" for empty list
	var got []planning.TodoItem
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatalf("result not valid JSON: %v — got %q", err, result)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %+v", got)
	}
}

func TestTodoWriteTool_Write_Replaces(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	first, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "old", "status": "pending"},
		},
	})
	tool.Execute(context.Background(), first) //nolint:errcheck

	second, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "2", "content": "new", "status": "in_progress"},
		},
	})
	tool.Execute(context.Background(), second) //nolint:errcheck

	stored := store.Read()
	if len(stored) != 1 || stored[0].ID != "2" {
		t.Errorf("second Write should replace first: %+v", stored)
	}
}

func TestTodoWriteTool_InvalidJSON(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	_, err := tool.Execute(context.Background(), []byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// TestTodoWriteTool_BulkPendingToCompleted 验证批量 pending→completed（2 个以上）被拒绝。
// 单个任务直接 pending→completed 允许（LLM 实际完成工作但未经 in_progress 步骤），
// 但同时完成 2+ 个未开始的任务视为作弊行为。
func TestTodoWriteTool_BulkPendingToCompleted(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// 初始化：两个 pending 任务
	init, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "pending"},
			{"id": "2", "content": "task two", "status": "pending"},
		},
	})
	if _, err := tool.Execute(context.Background(), init); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 尝试在一次调用中将两个 pending 任务全部标记为 completed（批量作弊）
	cheat, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "completed"},
			{"id": "2", "content": "task two", "status": "completed"},
		},
	})
	_, err := tool.Execute(context.Background(), cheat)
	if err == nil {
		t.Error("expected error when bulk-completing 2 pending items, got nil")
	}

	// store 应保持未变
	stored := store.Read()
	for _, item := range stored {
		if item.Status == planning.TodoCompleted {
			t.Errorf("store should not have completed items after rejected write, got %+v", stored)
		}
	}
}

// TestTodoWriteTool_SinglePendingToCompleted 验证单个 pending→completed 允许通过。
// LLM 完成工作后可以直接标记为 completed，不强制要求经过 in_progress。
func TestTodoWriteTool_SinglePendingToCompleted(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// 初始化：一个 pending 任务
	init, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "pending"},
		},
	})
	if _, err := tool.Execute(context.Background(), init); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// 单个 pending → completed 应该允许（LLM 完成了实际工作）
	complete, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "completed"},
		},
	})
	if _, err := tool.Execute(context.Background(), complete); err != nil {
		t.Errorf("single pending→completed should be allowed, got error: %v", err)
	}
}

// TestTodoWriteTool_InProgressToCompleted 验证 in_progress→completed 允许通过。
func TestTodoWriteTool_InProgressToCompleted(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// 初始化：item1 in_progress
	init, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "in_progress"},
		},
	})
	if _, err := tool.Execute(context.Background(), init); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// in_progress → completed 合法
	complete, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "completed"},
		},
	})
	if _, err := tool.Execute(context.Background(), complete); err != nil {
		t.Errorf("in_progress→completed should be allowed, got error: %v", err)
	}
}

// TestTodoWriteTool_CancelledToCompleted 验证 cancelled→completed 始终被拒绝。
// cancelled 任务必须先恢复为 pending/in_progress 才能完成，不适用"单个允许"宽松规则。
func TestTodoWriteTool_CancelledToCompleted(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// 初始化：一个 cancelled 任务
	init, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "cancelled"},
		},
	})
	if _, err := tool.Execute(context.Background(), init); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// cancelled → completed 即使只有 1 个也应被拒绝
	args, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "completed"},
		},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Error("expected error when cancelled→completed, got nil")
	}
}

// TestTodoWriteTool_SingleDirectPlusInProgress 验证"1 个直接完成 + 1 个经 in_progress 完成"的
// 混合调用允许通过（directCompletions == 1，未超过阈值）。
func TestTodoWriteTool_SingleDirectPlusInProgress(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// 初始化：item1 pending，item2 in_progress
	init, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "pending"},
			{"id": "2", "content": "task two", "status": "in_progress"},
		},
	})
	if _, err := tool.Execute(context.Background(), init); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	// item1: pending→completed（1 个直接完成），item2: in_progress→completed（合法）
	// directCompletions == 1 → 应允许通过
	args, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "task one", "status": "completed"},
			{"id": "2", "content": "task two", "status": "completed"},
		},
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Errorf("1 direct + 1 in_progress completion should be allowed, got error: %v", err)
	}
}

// TestTodoWriteTool_BulkNewItemCompleted 验证批量新建 completed 条目（2 个以上）被拒绝。
// 单个新建直接 completed 允许（LLM 可能完成了工作再创建记录），
// 同时新建 2+ 个 completed 条目视为作弊。
func TestTodoWriteTool_BulkNewItemCompleted(t *testing.T) {
	store := planning.NewTodoStore()
	tool := tools.NewTodoWriteTool(store)

	// 同时创建 2 个已完成的全新条目 → 应被拒绝
	args, _ := json.Marshal(map[string]interface{}{
		"todos": []map[string]string{
			{"id": "1", "content": "brand new one", "status": "completed"},
			{"id": "2", "content": "brand new two", "status": "completed"},
		},
	})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Error("expected error when creating 2 new items as completed, got nil")
	}
}
