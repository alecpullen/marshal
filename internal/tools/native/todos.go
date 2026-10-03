package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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
		Schema:      json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"],"additionalProperties":false}},"drop_unfinished":{"type":"boolean","description":"When true, skip auto-carry: the submitted list becomes the whole list and unfinished items omitted from it are dropped. Default false keeps auto-carry."}},"required":["todos"],"additionalProperties":false}`),
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

		oldTodos := store.Todos()
		now := todoNow()
		// Identity and timestamps are the store's, never the model's.
		for i := range args.Todos {
			args.Todos[i].StartedAt, args.Todos[i].CompletedAt = time.Time{}, time.Time{}
		}
		merged, matched := reconcileTodos(oldTodos, args.Todos, now, state.TodoIDFloor())

		// Auto-carry: unfinished items missing from the submitted list are
		// kept (appended with their status) instead of erroring. The old
		// drop-guard fired during ordinary reorganisation and forced a
		// retry loop with the model.
		var carried, dropped []string
		for i, old := range oldTodos {
			if matched[i] || old.Status == TodoCompleted {
				continue
			}
			if args.DropUnfinished {
				dropped = append(dropped, old.Content)
				continue
			}
			carried = append(carried, old.Content)
			merged = append(merged, old)
		}
		args.Todos = merged

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
		if len(dropped) > 0 {
			result.Content += fmt.Sprintf("; dropped %d unfinished item(s) per drop_unfinished: %s", len(dropped), strings.Join(dropped, "; "))
		}
		return result, nil
	}
	return tool
}

// todoNow is the clock for todo timestamps; tests replace it.
var todoNow = time.Now

// reconcileTodos matches a submitted list against the previous one so items
// keep their identity and timestamps across rewrites. An item matches, in
// order, by ID, by exact content, and by content equal after trimming and
// case-folding; anything else is new and gets the next "t<n>" ID. matched
// reports which previous items were claimed. Timestamps are set from status
// transitions: StartedAt on first entering in_progress, CompletedAt on entering
// completed (cleared again if the item leaves it).
func reconcileTodos(prev, next []TodoItem, now time.Time, floor int) ([]TodoItem, []bool) {
	matched := make([]bool, len(prev))
	claim := func(ok func(TodoItem) bool) int {
		for i := range prev {
			if !matched[i] && ok(prev[i]) {
				matched[i] = true
				return i
			}
		}
		return -1
	}
	// floor is the highest number any earlier ID in the session used, so an
	// ID freed by dropping a completed todo is never handed to a new one
	// (older steps still carry it).
	counter := floor
	for _, p := range prev {
		if n, err := strconv.Atoi(strings.TrimPrefix(p.ID, "t")); err == nil && strings.HasPrefix(p.ID, "t") && n > counter {
			counter = n
		}
	}
	out := make([]TodoItem, 0, len(next))
	for _, item := range next {
		idx := -1
		if item.ID != "" {
			idx = claim(func(p TodoItem) bool { return p.ID == item.ID })
		}
		if idx < 0 {
			idx = claim(func(p TodoItem) bool { return p.Content == item.Content })
		}
		if idx < 0 {
			want := strings.ToLower(strings.TrimSpace(item.Content))
			idx = claim(func(p TodoItem) bool { return strings.ToLower(strings.TrimSpace(p.Content)) == want })
		}
		var base TodoItem
		if idx >= 0 {
			base = prev[idx]
		}
		if base.ID == "" {
			// New, or a todo saved before IDs existed: it gets an identity now.
			counter++
			base.ID = "t" + strconv.Itoa(counter)
		}
		base.Content, base.Status = item.Content, item.Status
		switch item.Status {
		case TodoInProgress:
			if base.StartedAt.IsZero() {
				base.StartedAt = now
			}
			base.CompletedAt = time.Time{}
		case TodoCompleted:
			if base.CompletedAt.IsZero() {
				base.CompletedAt = now
			}
		default:
			base.CompletedAt = time.Time{}
		}
		out = append(out, base)
	}
	return out, matched
}
