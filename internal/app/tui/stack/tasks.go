package stack

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

const todoWriteTool = "todo.write"

// TaskInfo is a task node's payload: one todo, as the steps that worked on it
// saw it. Content, status and position come from the current todo list.
type TaskInfo struct {
	TodoID      string
	Content     string
	Status      string
	Index       int // 1-based position in the current list; 0 when dropped
	Total       int
	Dropped     bool // the todo is no longer in the list
	StartedAt   time.Time
	CompletedAt time.Time

	Steps int
	Tools int // tool calls, excluding todo.write
	Edits int // calls that changed files

	// UnresolvedFailure keeps a finished task open: its latest shell-family
	// call exited non-zero, or its last step had a failing tool row.
	UnresolvedFailure bool
	// FirstNarration is the first step's narration, the fallback title for a
	// dropped task.
	FirstNarration string
}

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

func isShellFamily(name string) bool { return name == "shell.run" || name == "test.run" }

func eventFailed(ev registry.AuditEvent) bool {
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
		for _, t := range todos {
			if t.Content == it.Content {
				return t.ID
			}
		}
		want := strings.ToLower(strings.TrimSpace(it.Content))
		for _, t := range todos {
			if strings.ToLower(strings.TrimSpace(t.Content)) == want {
				return t.ID
			}
		}
	}
	return ""
}

// groupTasks wraps runs of consecutive steps that share a todo under task
// nodes. Anything else (steps with no task, final answers, notices) stays at
// turn level, so a pass-through between two steps of one task splits it into
// segments and the chronology stays honest.
func groupTasks(nodes []*Node, turnKey string, s Snapshot) []*Node {
	out := make([]*Node, 0, len(nodes))
	var cur *Node
	segments := map[string]int{}
	for _, n := range nodes {
		id := ""
		if n.Kind == KindStep && n.Step != nil && !n.Step.Heuristic {
			id = n.Step.TodoID
		}
		if id == "" {
			out = append(out, n)
			cur = nil
			continue
		}
		if cur != nil && cur.Task.TodoID == id {
			cur.Children = append(cur.Children, n)
			continue
		}
		segments[id]++
		cur = &Node{
			ID:   NodeID{KindTask, fmt.Sprintf("task:%s:%s:%d", turnKey, id, segments[id])},
			Kind: KindTask,
			Task: &TaskInfo{TodoID: id},
		}
		cur.Children = append(cur.Children, n)
		out = append(out, cur)
	}
	for _, n := range out {
		if n.Kind == KindTask {
			fillTask(n, s.Todos)
		}
	}
	return out
}

func fillTask(n *Node, todos []db.TodoItem) {
	t := n.Task
	t.Total = len(todos)
	t.Dropped = true
	for i, td := range todos {
		if td.ID == t.TodoID {
			t.Content, t.Status, t.Index = td.Content, td.Status, i+1
			t.StartedAt, t.CompletedAt = td.StartedAt, td.CompletedAt
			t.Dropped = false
			break
		}
	}
	var lastShell *registry.AuditEvent
	var last *Node
	for _, st := range n.Children {
		t.Steps++
		last = st
		if t.FirstNarration == "" && st.Step != nil && len(st.Step.Narration) > 0 {
			t.FirstNarration = st.Step.Narration[0].Content
		}
		for _, row := range st.Children {
			for i := range row.Tools {
				ev := row.Tools[i]
				t.Tools++
				if len(ev.FilesChanged) > 0 || isEditTool(ev.ToolName) {
					t.Edits++
				}
				if isShellFamily(ev.ToolName) {
					lastShell = &row.Tools[i]
				}
			}
		}
	}
	if lastShell != nil && lastShell.CommandExitCode != nil && *lastShell.CommandExitCode != 0 {
		t.UnresolvedFailure = true
	}
	if last != nil {
		for _, row := range last.Children {
			for _, ev := range row.Tools {
				if eventFailed(ev) {
					t.UnresolvedFailure = true
				}
			}
		}
	}
	n.Version = versionOfTask(n)
}

func isEditTool(name string) bool { return isDiffTool(name) }

func versionOfTask(n *Node) uint64 {
	t := n.Task
	b := newHash()
	b.f("task|%s|%s|%s|%d|%d|%v|%d|%d|%d|%d|%d|%v|%d|%d|", t.TodoID, t.Content, t.Status, t.Index, t.Total, t.Dropped,
		t.StartedAt.UnixNano(), t.CompletedAt.UnixNano(), t.Steps, t.Tools, t.Edits, t.UnresolvedFailure, len(t.FirstNarration), len(n.Children))
	for _, c := range n.Children {
		b.f("c|%s|%d|", c.ID.Key, c.Version)
	}
	return b.sum()
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
		if !taskIDs[td.ID] || td.CompletedAt.IsZero() {
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
