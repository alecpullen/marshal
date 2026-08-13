package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
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
	if res.Summary != "tasks updated · 3 items" {
		t.Fatalf("summary = %q, want tasks updated · 3 items", res.Summary)
	}
	got := state.Todos()
	if len(got) != 3 || got[1].Status != "in_progress" {
		t.Fatalf("todos = %+v", got)
	}
}

// registry.ValidateArgs does not enforce the declared schema, so a model
// that names the field anything but "content" used to write a list of empty
// items — the todo panel then drew bare glyphs with no text. The handler
// must reject that outright so the model gets a correctable error instead.
func TestTodoWriteRejectsMisnamedContentField(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"task": "read spec", "status": "completed"},
			{"task": "write plan", "status": "in_progress"},
		},
	})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected an error for items with no content field")
	}
	if !strings.Contains(err.Error(), "content") {
		t.Fatalf("error = %v, want it to name the %q field so the model can correct itself", err, "content")
	}
	if got := state.Todos(); len(got) != 0 {
		t.Fatalf("todos = %+v, want the rejected list not to be stored", got)
	}
}

func TestTodoWriteRejectsBlankContent(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "real task", "status": "in_progress"},
			{"content": "   ", "status": "pending"},
		},
	})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected an error for whitespace-only content")
	}
}

func TestTodoWriteAllowsMultipleInProgress(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "a", "status": "in_progress"},
			{"content": "b", "status": "in_progress"},
		},
	})
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: args}); err != nil {
		t.Fatalf("expected multiple in_progress to be allowed: %v", err)
	}
	got := state.Todos()
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
}

func TestTodoWriteRejectsDroppingUnfinishedItems(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	// Seed an existing list with one unfinished item.
	if err := state.SetTodos([]db.TodoItem{
		{Content: "keep me", Status: TodoPending},
		{Content: "done", Status: TodoCompleted},
	}); err != nil {
		t.Fatalf("seed todos: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "done", "status": "completed"},
		},
	})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error when dropping unfinished todo")
	}
	if !strings.Contains(err.Error(), "force=true") {
		t.Fatalf("error should mention the force=true override option: %v", err)
	}
	if got := state.Todos(); len(got) != 2 {
		t.Fatalf("state should not have been modified, got %d items", len(got))
	}
}

func TestTodoWriteForceDropsUnfinishedItems(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	if err := state.SetTodos([]db.TodoItem{
		{Content: "keep me", Status: TodoPending},
		{Content: "done", Status: TodoCompleted},
	}); err != nil {
		t.Fatalf("seed todos: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "done", "status": "completed"},
			{"content": "new task", "status": "in_progress"},
		},
		"force": true,
	})
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: args}); err != nil {
		t.Fatalf("force=true should allow dropping: %v", err)
	}
	got := state.Todos()
	if len(got) != 2 {
		t.Fatalf("expected 2 items (done + new), got %d", len(got))
	}
	if got[0].Content != "done" || got[1].Content != "new task" {
		t.Fatalf("unexpected todos: %+v", got)
	}
}

func TestTodoWriteAllowsCompletingUnfinishedItem(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	if err := state.SetTodos([]db.TodoItem{{Content: "keep me", Status: TodoPending}}); err != nil {
		t.Fatalf("seed todos: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "keep me", "status": "completed"},
		},
	})
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: args}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := state.Todos()
	if len(got) != 1 || got[0].Status != TodoCompleted {
		t.Fatalf("expected one completed item, got %+v", got)
	}
}

func TestTodoWriteAllowsKeepingUnfinishedItem(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.todoWriteTool()

	if err := state.SetTodos([]db.TodoItem{{Content: "keep me", Status: TodoInProgress}}); err != nil {
		t.Fatalf("seed todos: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"todos": []map[string]string{
			{"content": "keep me", "status": "in_progress"},
			{"content": "new", "status": "pending"},
		},
	})
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: args}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := state.Todos(); len(got) != 2 {
		t.Fatalf("expected two items, got %d", len(got))
	}
}
