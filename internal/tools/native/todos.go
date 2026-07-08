package native

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// TodoItem is the tool-level alias for the persisted todo type.
type TodoItem = db.TodoItem

const (
	TodoPending    = "pending"
	TodoInProgress = "in_progress"
	TodoCompleted  = "completed"
)

type TodoStore interface {
	SetTodos([]TodoItem) error
	Todos() []TodoItem
}

type todoWriteArgs struct {
	Todos []TodoItem `json:"todos"`
}

func (t *toolSet) todoWriteTool() registry.Tool {
	tool := registry.Tool{
		Name:        "todo.write",
		Description: "Replace the entire session todo list. Use for any task with 3+ steps or multiple requirements; mark items completed immediately, never batch-complete at the end.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"]}}},"required":["todos"]}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[todoWriteArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		inProgress := 0
		for _, item := range args.Todos {
			switch item.Status {
			case TodoPending, TodoInProgress, TodoCompleted:
				if item.Status == TodoInProgress {
					inProgress++
				}
			default:
				return registry.ToolResult{}, fmt.Errorf("invalid todo status %q; use pending|in_progress|completed", item.Status)
			}
		}
		if inProgress > 1 {
			return registry.ToolResult{}, fmt.Errorf("at most one todo may be in_progress; got %d", inProgress)
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("todo store not available")
		}
		store, ok := t.sessionState.(TodoStore)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("session state does not support todos")
		}
		if err := store.SetTodos(args.Todos); err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: "todo list updated",
			Content: fmt.Sprintf("%d todo(s) recorded", len(args.Todos)),
		}, nil
	}
	return tool
}
