package native

import (
	"context"
	"encoding/json"
	"strings"
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
