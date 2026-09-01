package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
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
	// DropUnfinished is the opt-in escape hatch for auto-carry. False
	// (absent) keeps the default: unfinished items missing from the
	// submitted list are carried over with their status.
	DropUnfinished bool `json:"drop_unfinished"`
}

// TodoWriteTool builds the todo.write tool bound to the given session
// state. Subagent factories use it to rebind the tool to a child's own
// session so its task list never overwrites the parent's.
func TodoWriteTool(state *session.State) registry.Tool {
	return newTodoWriteTool(state)
}

func (t *toolSet) todoWriteTool() registry.Tool {
	return newTodoWriteTool(t.sessionState)
}

func newTodoWriteTool(state *session.State) registry.Tool {
	tool := registry.Tool{
		Name:        "todo.write",
		Description: "Replace the session todo list. Use for any task with 3+ steps or multiple requirements; mark items completed immediately, never batch-complete at the end. Unfinished items omitted from the new list are kept automatically (carried over); completed items may be dropped. Pass \"drop_unfinished\": true only when the carried list is corrupted or stale and you need to replace it wholesale: the submitted list becomes the whole list, even if that drops unfinished items you left out.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"],"additionalProperties":false}},"drop_unfinished":{"type":"boolean","description":"When true, skip auto-carry: the submitted list becomes the whole list and unfinished items omitted from it are dropped. Default false keeps auto-carry."}},"required":["todos"],"additionalProperties":false}`),
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
			// status already is.
			if strings.TrimSpace(item.Content) == "" {
				return registry.ToolResult{}, fmt.Errorf("todo %d has empty content; each item needs a non-empty %q string describing the task", i+1, "content")
			}
			switch item.Status {
			case TodoPending, TodoInProgress, TodoCompleted:
			default:
				return registry.ToolResult{}, fmt.Errorf("invalid todo status %q; use pending|in_progress|completed", item.Status)
			}
		}
		if state == nil {
			return registry.ToolResult{}, fmt.Errorf("todo store not available")
		}
		store := TodoStore(state)

		// Auto-carry: unfinished items missing from the submitted list are
		// kept (appended with their status) instead of erroring. The old
		// drop-guard fired during ordinary reorganisation and forced a
		// retry loop with the model.
		submitted := map[string]bool{}
		for _, item := range args.Todos {
			submitted[strings.TrimSpace(item.Content)] = true
		}
		oldTodos := store.Todos()
		var carried []string
		if !args.DropUnfinished {
			for _, old := range oldTodos {
				if old.Status == TodoCompleted {
					continue
				}
				if !submitted[strings.TrimSpace(old.Content)] {
					carried = append(carried, old.Content)
					args.Todos = append(args.Todos, TodoItem{Content: old.Content, Status: old.Status})
				}
			}
		}

		if err := store.SetTodos(args.Todos); err != nil {
			return registry.ToolResult{}, err
		}
		result := registry.ToolResult{
			Summary: fmt.Sprintf("tasks updated · %d items", len(args.Todos)),
			Content: fmt.Sprintf("%d todo(s) recorded", len(args.Todos)),
		}
		if len(carried) > 0 {
			result.Content += fmt.Sprintf("; carried over %d unfinished item(s): %s", len(carried), strings.Join(carried, "; "))
		}
		if args.DropUnfinished {
			var dropped []string
			for _, old := range oldTodos {
				if old.Status == TodoCompleted {
					continue
				}
				if !submitted[strings.TrimSpace(old.Content)] {
					dropped = append(dropped, old.Content)
				}
			}
			if len(dropped) > 0 {
				result.Content += fmt.Sprintf("; dropped %d unfinished item(s) per drop_unfinished: %s", len(dropped), strings.Join(dropped, "; "))
			}
		}
		return result, nil
	}
	return tool
}
