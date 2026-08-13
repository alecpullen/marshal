package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	Force bool       `json:"force,omitempty"`
}

func (t *toolSet) todoWriteTool() registry.Tool {
	tool := registry.Tool{
		Name:        "todo.write",
		Description: "Replace the entire session todo list. Use for any task with 3+ steps or multiple requirements; mark items completed immediately, never batch-complete at the end.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"],"additionalProperties":false}},"force":{"type":"boolean","description":"When true, allows dropping unfinished todos from the list without marking them completed first. Use when the user explicitly requests dropping or reorganizing items."}},"required":["todos"],"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[todoWriteArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		for i, item := range args.Todos {
			// registry.ValidateArgs only checks that the arguments are a
			// JSON object — the declared schema's "required" is not
			// enforced — so content has to be checked here, the same way
			// status already is. Without this a model that names the field
			// anything other than "content" writes a list of empty items
			// and the todo panel draws glyphs with no text next to them.
			if strings.TrimSpace(item.Content) == "" {
				return registry.ToolResult{}, fmt.Errorf("todo %d has empty content; each item needs a non-empty %q string describing the task", i+1, "content")
			}
			switch item.Status {
			case TodoPending, TodoInProgress, TodoCompleted:
			default:
				return registry.ToolResult{}, fmt.Errorf("invalid todo status %q; use pending|in_progress|completed", item.Status)
			}
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("todo store not available")
		}
		store := TodoStore(t.sessionState)

		// The drop guard is a safety net against accidental loss of
		// unfinished work. It is skipped when the caller explicitly sets
		// force=true (e.g. the user asked to drop or reorganize items).
		if !args.Force {
			var dropped []string
			newContents := map[string]int{}
			for _, item := range args.Todos {
				newContents[strings.TrimSpace(item.Content)]++
			}
			for _, old := range store.Todos() {
				if old.Status == TodoCompleted {
					continue
				}
				key := strings.TrimSpace(old.Content)
				if newContents[key] == 0 {
					dropped = append(dropped, old.Content)
					continue
				}
				newContents[key]--
			}
			if len(dropped) > 0 {
				return registry.ToolResult{}, fmt.Errorf("refusing to drop %d unfinished todo(s): %s; include them in the new list, mark them completed first, or set force=true to override", len(dropped), strings.Join(dropped, "; "))
			}
		}

		if err := store.SetTodos(args.Todos); err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("tasks updated · %d items", len(args.Todos)),
			Content: fmt.Sprintf("%d todo(s) recorded", len(args.Todos)),
		}, nil
	}
	return tool
}
