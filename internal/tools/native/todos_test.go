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

// The drop-guard is gone: an unfinished item omitted from a new list is
// carried over automatically rather than refused. Completed items may be
// dropped freely.
func TestTodoWriteCarriesOmittedUnfinishedItem(t *testing.T) {
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
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("omitting an unfinished item should auto-carry, not error: %v", err)
	}
	if !strings.Contains(res.Content, "carried over 1 unfinished") {
		t.Fatalf("result should note the carry-over, got %q", res.Content)
	}
	got := state.Todos()
	if len(got) != 2 {
		t.Fatalf("expected 2 items (done + carried keep me), got %d", len(got))
	}
	contents := map[string]string{}
	for _, item := range got {
		contents[item.Content] = item.Status
	}
	if contents["keep me"] != TodoPending {
		t.Fatalf("carried item lost or status changed: %+v", contents)
	}
	if contents["done"] != TodoCompleted {
		t.Fatalf("completed item should be kept when submitted: %+v", contents)
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

func TestTodoWriteAutoCarriesUnfinishedItems(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	if err := state.SetTodos([]db.TodoItem{
		{Content: "old pending", Status: "pending"},
		{Content: "old in progress", Status: "in_progress"},
		{Content: "old done", Status: "completed"},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	tool := TodoWriteTool(state)
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "todo.write",
		Args: []byte(`{"todos":[{"content":"new task","status":"pending"}]}`),
	})
	if err != nil {
		t.Fatalf("todo.write returned error, want auto-carry success: %v", err)
	}
	got := state.Todos()
	// Completed items may be dropped; unfinished ones are carried.
	contents := map[string]string{}
	for _, item := range got {
		contents[item.Content] = item.Status
	}
	if _, ok := contents["old done"]; ok {
		t.Error("completed item should be droppable, was carried")
	}
	if contents["old pending"] != "pending" {
		t.Errorf("carried item lost or status changed: %v", contents)
	}
	if contents["old in progress"] != "in_progress" {
		t.Errorf("in-progress item lost or status changed: %v", contents)
	}
	if contents["new task"] != "pending" {
		t.Errorf("new item missing: %v", contents)
	}
	if !strings.Contains(res.Content, "carried over 2 unfinished") {
		t.Errorf("result should note the carry-over, got %q", res.Content)
	}
}

func TestTodoWriteSchemaHasNoForce(t *testing.T) {
	tool := TodoWriteTool(session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if strings.Contains(string(tool.Schema), "force") {
		t.Fatal("force parameter must be removed from the schema")
	}
}

func TestTodoWriteDropUnfinishedReplacesList(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	if err := state.SetTodos([]db.TodoItem{
		{Content: "old pending", Status: "pending"},
		{Content: "old in progress", Status: "in_progress"},
		{Content: "old done", Status: "completed"},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	tool := TodoWriteTool(state)
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "todo.write",
		Args: []byte(`{"todos":[{"content":"new task","status":"pending"}],"drop_unfinished":true}`),
	})
	if err != nil {
		t.Fatalf("todo.write returned error: %v", err)
	}
	got := state.Todos()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 item after drop, got %d: %+v", len(got), got)
	}
	if got[0].Content != "new task" || got[0].Status != "pending" {
		t.Fatalf("unexpected item after drop: %+v", got[0])
	}
	if !strings.Contains(res.Content, "dropped 2 unfinished") {
		t.Errorf("result should note the drop, got %q", res.Content)
	}
}

func TestTodoWriteDropUnfinishedEmptiesList(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	if err := state.SetTodos([]db.TodoItem{
		{Content: "old pending", Status: "pending"},
		{Content: "old in progress", Status: "in_progress"},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	tool := TodoWriteTool(state)
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "todo.write",
		Args: []byte(`{"todos":[],"drop_unfinished":true}`),
	})
	if err != nil {
		t.Fatalf("todo.write returned error: %v", err)
	}
	if got := state.Todos(); len(got) != 0 {
		t.Fatalf("expected empty list after drop, got %+v", got)
	}
	if res.Summary != "tasks updated · 0 items" {
		t.Errorf("summary = %q, want tasks updated · 0 items", res.Summary)
	}
}

func TestTodoWriteSchemaAdvertisesDropUnfinished(t *testing.T) {
	tool := TodoWriteTool(session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if !strings.Contains(string(tool.Schema), `"drop_unfinished"`) {
		t.Fatal("schema must advertise drop_unfinished")
	}
	if !strings.Contains(tool.Description, "drop_unfinished") {
		t.Fatal("description must document drop_unfinished")
	}
	if strings.Contains(string(tool.Schema), "mode") {
		t.Fatal("schema must not use a mode enum")
	}
}
