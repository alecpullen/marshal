package viewmodel

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
	// Work is the time the task's steps ran, the sum of their durations (a
	// still-open step counts up to now while the turn is running). It stops
	// at the end of a turn instead of running through the idle time before
	// the next prompt, which the todo's own timestamps would include.
	Work  time.Duration
	Tools int // tool calls, excluding todo.write
	Edits int // calls that changed files

	// UnresolvedFailure keeps a finished task open: its latest shell-family
	// call exited non-zero, or its last step had a failing tool row.
	UnresolvedFailure bool
	// FirstNarration is the first step's narration, the fallback title for a
	// dropped task.
	FirstNarration string
}

// QueueInfo is the todos a turn has not started working on, listed below the
// work in progress like the old todo list.
type QueueInfo struct {
	Items []QueueItem
}

// QueueItem is one waiting todo: position in the list, and whether the agent
// has already marked it in progress without a step having run under it yet.
type QueueItem struct {
	TodoID  string
	Content string
	Index   int // 1-based position in the current list
	Total   int
	Active  bool
}

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

// groupTasks wraps runs of consecutive steps that share a todo under task
// nodes. Anything else (steps with no task, final answers, notices) stays at
// turn level, so a pass-through between two steps of one task splits it into
// segments and the chronology stays honest.
func groupTasks(nodes []*Node, turnKey string, s Snapshot, lastTurn, wroteList bool) []*Node {
	out := make([]*Node, 0, len(nodes))
	var cur *Node
	segments := map[string]int{}
	for _, n := range nodes {
		id := ""
		if n.Kind == KindStep && n.Step != nil && !n.Step.Heuristic {
			id = n.Step.TodoID
			// A short list does not drive the transcript, but work under a
			// todo that has since left the list still gets its dropped header.
			if !wroteList && !taskMode(s.Todos) && todoListed(s.Todos, id) {
				id = ""
			}
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
			fillTask(n, s.Todos, s.Now, s.Busy && lastTurn)
			// An unfinished task's clock moves while the turn runs, between
			// steps as well as during them, so it must not be served from the
			// render cache.
			n.Live = s.Busy && lastTurn && n.Task.Status == "in_progress"
		}
	}
	return out
}

// DrivenByTodos reports whether a turn is working from the todo list: it has
// task blocks or a waiting list. The TUI pins the list above the transcript
// for such a turn; a turn that never touched the list does not get it, so
// leftovers from an earlier turn do not linger.
func DrivenByTodos(turn *Node) bool {
	for _, n := range turn.Children {
		if n.Kind == KindQueue || (n.Kind == KindTask && n.Task != nil && !n.Task.Dropped) {
			return true
		}
	}
	return false
}

// addQueue appends the waiting-todos list to the last turn when it is working
// from a todo list. The TUI shows these todos in the band pinned below the
// transcript rather than as rows, so only the latest turn carries one: earlier
// turns keep their folded task rows. Todos that already have a task in this
// turn are not listed twice; finished ones are done.
func addQueue(nodes []*Node, turnKey string, s Snapshot, todoWritten bool) []*Node {
	if !taskMode(s.Todos) {
		return nodes
	}
	started := map[string]bool{}
	for _, n := range nodes {
		if n.Kind == KindTask && n.Task != nil {
			started[n.Task.TodoID] = true
		}
	}
	if len(started) == 0 && !todoWritten {
		return nodes
	}
	q := &QueueInfo{}
	for i, td := range s.Todos {
		if td.Status == "completed" || started[td.ID] {
			continue
		}
		q.Items = append(q.Items, QueueItem{TodoID: td.ID, Content: td.Content, Index: i + 1, Total: len(s.Todos), Active: td.Status == "in_progress"})
	}
	if len(q.Items) == 0 {
		return nodes
	}
	node := &Node{ID: NodeID{KindQueue, "queue:" + turnKey}, Kind: KindQueue, Queue: q}
	b := newHash()
	for _, it := range q.Items {
		b.f("q|%s|%s|%d|%d|%v|", it.TodoID, it.Content, it.Index, it.Total, it.Active)
	}
	node.Version = b.sum()
	return append(nodes, node)
}

func fillTask(n *Node, todos []db.TodoItem, now time.Time, running bool) {
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
		if st.Step != nil && !st.Step.Step.StartedAt.IsZero() {
			end := st.Step.Step.EndedAt
			if end.IsZero() && running && !now.IsZero() {
				end = now
			}
			if end.After(st.Step.Step.StartedAt) {
				t.Work += end.Sub(st.Step.Step.StartedAt)
			}
		}
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
				if IsShellFamily(ev.ToolName) {
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
				if EventFailed(ev) {
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
	b.f("task|%s|%s|%s|%d|%d|%v|%d|%d|%d|%d|%d|%v|%d|%d|%d|", t.TodoID, t.Content, t.Status, t.Index, t.Total, t.Dropped,
		t.StartedAt.UnixNano(), t.CompletedAt.UnixNano(), t.Steps, t.Tools, t.Edits, t.UnresolvedFailure, len(t.FirstNarration), len(n.Children), int64(t.Work/time.Second))
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
