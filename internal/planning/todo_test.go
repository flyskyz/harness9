package planning_test

import (
	"sync"
	"testing"

	"github.com/harness9/internal/planning"
)

func TestTodoStore_WriteAndRead(t *testing.T) {
	s := planning.NewTodoStore()
	items := []planning.TodoItem{
		{ID: "1", Content: "task one", Status: planning.TodoPending},
		{ID: "2", Content: "task two", Status: planning.TodoInProgress},
	}
	got := s.Write(items)
	if len(got) != 2 {
		t.Fatalf("Write returned %d items, want 2", len(got))
	}
	read := s.Read()
	if len(read) != 2 {
		t.Fatalf("Read returned %d items, want 2", len(read))
	}
	if read[0].ID != "1" || read[1].ID != "2" {
		t.Errorf("unexpected items: %+v", read)
	}
}

func TestTodoStore_Read_IsCopy(t *testing.T) {
	s := planning.NewTodoStore()
	s.Write([]planning.TodoItem{{ID: "1", Content: "x", Status: planning.TodoPending}})
	got := s.Read()
	got[0].Content = "mutated"
	second := s.Read()
	if second[0].Content == "mutated" {
		t.Error("Read returned a reference, not a copy")
	}
}

func TestTodoStore_WriteReplaces(t *testing.T) {
	s := planning.NewTodoStore()
	s.Write([]planning.TodoItem{{ID: "1", Content: "old", Status: planning.TodoPending}})
	s.Write([]planning.TodoItem{{ID: "2", Content: "new", Status: planning.TodoPending}})
	got := s.Read()
	if len(got) != 1 || got[0].ID != "2" {
		t.Errorf("Write did not replace: %+v", got)
	}
}

func TestTodoStore_FormatForInjection_ActiveOnly(t *testing.T) {
	s := planning.NewTodoStore()
	s.Write([]planning.TodoItem{
		{ID: "1", Content: "done task", Status: planning.TodoCompleted},
		{ID: "2", Content: "active task", Status: planning.TodoInProgress},
		{ID: "3", Content: "pending task", Status: planning.TodoPending},
		{ID: "4", Content: "cancelled task", Status: planning.TodoCancelled},
	})
	out := s.FormatForInjection()
	if out == "" {
		t.Fatal("expected non-empty output when active tasks exist")
	}
	if contains(out, "done task") {
		t.Error("completed task should not appear in injection")
	}
	if contains(out, "cancelled task") {
		t.Error("cancelled task should not appear in injection")
	}
	if !contains(out, "active task") {
		t.Error("in_progress task should appear in injection")
	}
	if !contains(out, "pending task") {
		t.Error("pending task should appear in injection")
	}
}

func TestTodoStore_FormatForInjection_Empty(t *testing.T) {
	s := planning.NewTodoStore()
	if got := s.FormatForInjection(); got != "" {
		t.Errorf("empty store should return empty string, got %q", got)
	}
	// All completed → also empty
	s.Write([]planning.TodoItem{{ID: "1", Content: "done", Status: planning.TodoCompleted}})
	if got := s.FormatForInjection(); got != "" {
		t.Errorf("all-completed store should return empty string, got %q", got)
	}
}

func TestTodoStore_ActiveCount(t *testing.T) {
	s := planning.NewTodoStore()
	s.Write([]planning.TodoItem{
		{ID: "1", Content: "a", Status: planning.TodoCompleted},
		{ID: "2", Content: "b", Status: planning.TodoInProgress},
		{ID: "3", Content: "c", Status: planning.TodoPending},
	})
	active, total := s.ActiveCount()
	if active != 2 {
		t.Errorf("active want 2, got %d", active)
	}
	if total != 3 {
		t.Errorf("total want 3, got %d", total)
	}
}

func TestTodoStore_ConcurrentWrite(t *testing.T) {
	s := planning.NewTodoStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Write([]planning.TodoItem{{ID: "x", Content: "c", Status: planning.TodoPending}})
			s.Read()
		}(i)
	}
	wg.Wait()
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
