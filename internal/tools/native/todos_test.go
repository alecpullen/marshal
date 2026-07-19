package native

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestTodoWriteReplacesList(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
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
	got := state.Todos()
	if len(got) != 3 || got[1].Status != "in_progress" {
		t.Fatalf("todos = %+v", got)
	}
}

func TestTodoWriteRejectsTwoInProgress(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
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
