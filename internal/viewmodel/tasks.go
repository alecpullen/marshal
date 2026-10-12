package viewmodel

import (
	"encoding/json"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

const todoWriteTool = "todo.write"

// MinTaskTodos is how long a todo list has to be before it drives the
// transcript. A one-item list is no plan to follow: its work reads better as
// plain narrated steps, so tasks, queue and task counts only appear for
// lists of two or more.
const MinTaskTodos = 2

func todoListed(todos []db.TodoItem, id string) bool {
	for _, t := range todos {
		if t.ID == id {
			return true
		}
	}
	return false
}

// todoWriteLen is how many items a todo.write call wrote.
func todoWriteLen(ev registry.AuditEvent) int {
	var args struct {
		Todos []db.TodoItem `json:"todos"`
	}
	if json.Unmarshal(ev.Args, &args) != nil {
		return 0
	}
	return len(args.Todos)
}

func taskMode(todos []db.TodoItem) bool { return len(todos) >= MinTaskTodos }

// ReceiptInfo is the one-line summary closing a finished turn.
type ReceiptInfo struct {
	Duration time.Duration
	Tasks    int
	Steps    int
	Tools    int
	Files    int
	Usage    string
	Salvaged bool
}

// IsShellFamily reports whether a tool's subject is a command line.
func IsShellFamily(name string) bool { return name == "shell.run" || name == "test.run" }

// EventFailed is the one definition of a failed call: an error, a denial, or
// a non-zero exit. The task failure rule and the step glyph both use it.
func EventFailed(ev registry.AuditEvent) bool {
	return ev.Error != "" || ev.Approval == registry.ApprovalDenied ||
		(ev.CommandExitCode != nil && *ev.CommandExitCode != 0)
}

// effectiveTodoID is the task a step renders under. A step is bound to the
// task active when it started, so a step that narrates and then marks the
// next todo in progress with todo.write would sit under the previous task.
// When todo.write is its only call it is re-bound to the todo that write set
// in progress. This affects rendering only; the stored binding is unchanged.
func effectiveTodoID(info *StepInfo, noOtherCalls bool, todos []db.TodoItem) string {
	if len(info.TodoWrites) > 0 && noOtherCalls && len(info.Narration) > 0 {
		if id := todoSetInProgress(info.TodoWrites[len(info.TodoWrites)-1], todos); id != "" {
			return id
		}
	}
	return info.Step.TodoID
}

// todoSetInProgress reads a todo.write's arguments and returns the ID of the
// item it marked in_progress.
//
// An ID the model sent wins. Otherwise the item is matched by text, and when
// several todos share that text the one started closest to the write is the
// one it just set in progress: a later todo with the same words must not pull
// older steps under itself.
func todoSetInProgress(ev registry.AuditEvent, todos []db.TodoItem) string {
	var args struct {
		Todos []db.TodoItem `json:"todos"`
	}
	if json.Unmarshal(ev.Args, &args) != nil {
		return ""
	}
	for _, it := range args.Todos {
		if it.Status != "in_progress" {
			continue
		}
		if it.ID != "" {
			for _, t := range todos {
				if t.ID == it.ID {
					return t.ID
				}
			}
		}
		want := strings.ToLower(strings.TrimSpace(it.Content))
		best, bestGap := "", time.Duration(1<<62)
		for _, t := range todos {
			if strings.ToLower(strings.TrimSpace(t.Content)) != want {
				continue
			}
			gap := time.Duration(1 << 61) // never started: only wins if alone
			if !t.StartedAt.IsZero() && !ev.Timestamp.IsZero() {
				gap = t.StartedAt.Sub(ev.Timestamp)
				if gap < 0 {
					gap = -gap
				}
			}
			if best == "" || gap < bestGap {
				best, bestGap = t.ID, gap
			}
		}
		return best
	}
	return ""
}

// receipt builds the closing summary for a finished turn, or nil when the
// turn has no final answer or no steps.
func receipt(turnKey string, nodes []*Node, userMsg *session.Message, s Snapshot) *Node {
	var final *session.Message
	for _, n := range nodes {
		if n.Kind == KindFinal && n.Item != nil && n.Item.Message != nil {
			final = n.Item.Message
		}
	}
	if final == nil {
		return nil
	}
	r := &ReceiptInfo{Usage: final.Usage, Salvaged: final.Salvaged}
	if userMsg != nil && !final.CreatedAt.IsZero() && !userMsg.CreatedAt.IsZero() {
		r.Duration = final.CreatedAt.Sub(userMsg.CreatedAt)
	}
	files := map[string]bool{}
	taskIDs := map[string]bool{}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch {
			case n.Kind == KindStep && n.Step != nil:
				r.Steps++
				if n.Step.TodoID != "" {
					taskIDs[n.Step.TodoID] = true
				}
			case n.Kind == KindTool:
				for _, ev := range n.Tools {
					r.Tools++
					for _, f := range ev.FilesChanged {
						files[f] = true
					}
				}
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	if r.Steps == 0 {
		return nil
	}
	r.Files = len(files)
	for _, td := range s.Todos {
		if !taskMode(s.Todos) || !taskIDs[td.ID] || td.CompletedAt.IsZero() {
			continue
		}
		if userMsg != nil && td.CompletedAt.Before(userMsg.CreatedAt) {
			continue
		}
		if !final.CreatedAt.IsZero() && td.CompletedAt.After(final.CreatedAt) {
			continue
		}
		r.Tasks++
	}
	n := &Node{ID: NodeID{KindReceipt, "receipt:" + turnKey}, Kind: KindReceipt, Receipt: r}
	b := newHash()
	b.f("rc|%d|%d|%d|%d|%d|%s|%v|", r.Duration, r.Tasks, r.Steps, r.Tools, r.Files, r.Usage, r.Salvaged)
	n.Version = b.sum()
	return n
}
