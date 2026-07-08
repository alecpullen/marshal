package native

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/tools/registry"
)

type fakeTodoStore struct {
	todos []TodoItem
}

func (f *fakeTodoStore) SetTodos(todos []TodoItem) error {
	f.todos = append([]TodoItem(nil), todos...)
	return nil
}

func (f *fakeTodoStore) Todos() []TodoItem {
	return append([]TodoItem(nil), f.todos...)
}

func TestTodoWriteReplacesList(t *testing.T) {
	store := &fakeTodoStore{}
	tools := &toolSet{sessionState: store}
	tool := tools.todoWriteTool()

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "read spec", "status": "completed"},
			{"content": "write plan", "status": "in_progress"},
			{"content": "implement", "status": "pending"},
		},
	})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.Summary != "todo list updated" {
		t.Fatalf("summary = %q, want todo list updated", res.Summary)
	}
	got := store.Todos()
	if len(got) != 3 || got[1].Status != "in_progress" {
		t.Fatalf("todos = %+v", got)
	}
}

func TestTodoWriteRejectsTwoInProgress(t *testing.T) {
	store := &fakeTodoStore{}
	tools := &toolSet{sessionState: store}
	tool := tools.todoWriteTool()

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "a", "status": "in_progress"},
			{"content": "b", "status": "in_progress"},
		},
	})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for two in_progress items")
	}
}
