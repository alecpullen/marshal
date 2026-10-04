package stack

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func stepFor(id session.StepID, todo string, start, end int) session.Step {
	st := stepOf(id, 1, start, end)
	st.TodoID = todo
	return st
}

func todoWrite(step session.StepID, callID string, s int, body string) session.TranscriptItem {
	it := audit("todo.write", step, callID, s)
	it.Audit.Args = json.RawMessage(body)
	return it
}

func shellAudit(step session.StepID, callID string, s, exit int) session.TranscriptItem {
	it := audit("shell.run", step, callID, s)
	it.Audit.CommandExitCode = &exit
	return it
}

func todos(items ...db.TodoItem) []db.TodoItem { return items }

func TestTasksGroupConsecutiveStepsAndHideTodoWrites(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Start with the parser."),
			todoWrite(1, "w1", 2, `{"todos":[{"content":"Parser","status":"in_progress"}]}`),
			narration(3, 2, 3, "Reading it."),
			audit("file.read", 2, "r", 4),
			narration(4, 3, 5, "Editing it."),
			audit("file.read", 3, "r", 6),
			final(9, 20),
		},
		Steps: []session.Step{stepFor(1, "", 1, 3), stepFor(2, "t1", 3, 5), stepFor(3, "t1", 5, 7)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "Parser", Status: "completed"}, db.TodoItem{ID: "t2", Content: "Tests", Status: "pending"}),
	})
	want := `turn:1
  msg:1
  task:turn:1:t1:1
    step:2
      tool:2:r
    step:3
      tool:3:r
  msg:9
`
	// Step 1 narrated and then only wrote the todo list: it re-binds to t1
	// for display, so it joins the task rather than standing alone.
	got := shape(nodes)
	if !strings.Contains(got, "task:turn:1:t1:1\n    step:1\n    step:2\n      tool:2:r\n    step:3") {
		t.Fatalf("shape:\n%s\nwant (modulo step 1 re-bound into the task):\n%s", got, want)
	}
	if strings.Contains(got, "todo") {
		t.Fatalf("todo.write must never render as a row:\n%s", got)
	}
	task := nodes[0].Children[1]
	if task.Kind != KindTask || task.Task.Index != 1 || task.Task.Total != 2 || task.Task.Steps != 3 || task.Task.Tools != 2 {
		t.Fatalf("task = %+v", task.Task)
	}
}

func TestTodoWriteOnlyStepWithoutNarrationIsOmitted(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			todoWrite(1, "w", 1, `{"todos":[{"content":"A","status":"in_progress"}]}`),
		},
		Steps: []session.Step{stepFor(1, "", 1, 2)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "in_progress"}),
	})
	if strings.Contains(shape(nodes), "step:") {
		t.Fatalf("a silent todo.write-only step must be omitted:\n%s", shape(nodes))
	}
}

func TestPassThroughSplitsATaskIntoSegments(t *testing.T) {
	notice := session.TranscriptItem{Timestamp: at(5), Kind: session.KindMessage,
		Message: &session.Message{ID: 7, Role: session.RoleSystem, Content: "rolled back", CreatedAt: at(5)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "One."), audit("file.read", 1, "a", 2),
			notice,
			narration(3, 2, 6, "Two."), audit("file.read", 2, "b", 7),
		},
		Steps: []session.Step{stepFor(1, "t1", 1, 3), stepFor(2, "t1", 6, 8)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "in_progress"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"}),
	})
	got := shape(nodes)
	if !strings.Contains(got, "task:turn:1:t1:1") || !strings.Contains(got, "task:turn:1:t1:2") {
		t.Fatalf("a pass-through between two steps must split the task:\n%s", got)
	}
}

func TestTasksWithoutIDsStayUngrouped(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Old."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "", 1, 3)},
		Todos: todos(db.TodoItem{Content: "legacy", Status: "pending"}),
	})
	if strings.Contains(shape(nodes), "task:") {
		t.Fatalf("pre-P3 sessions have no task headers:\n%s", shape(nodes))
	}
}

func TestDroppedTaskIsMarkedAndTitledFromNarration(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Cleaning up the cache. Then more."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "t9", 1, 3)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "Other", Status: "pending"}),
	})
	task := nodes[0].Children[1].Task
	if !task.Dropped || task.Index != 0 || !strings.HasPrefix(task.FirstNarration, "Cleaning up the cache") {
		t.Fatalf("dropped task = %+v", task)
	}
}

func TestUnresolvedFailure(t *testing.T) {
	cases := []struct {
		name  string
		items []session.TranscriptItem
		want  bool
	}{
		{"last shell exited 1", []session.TranscriptItem{shellAudit(1, "a", 2, 1)}, true},
		// The failure must be in an earlier step: any failed row in the last
		// step also keeps the task open.
		{"later shell passed", []session.TranscriptItem{shellAudit(1, "a", 2, 1), narration(8, 2, 3, "Fix."), shellAudit(2, "b", 4, 0)}, false},
		{"clean", []session.TranscriptItem{shellAudit(1, "a", 2, 0)}, false},
		{"last step has a failed non-shell row", func() []session.TranscriptItem {
			bad := audit("file.read", 1, "x", 2)
			bad.Audit.Error = "no such file"
			return []session.TranscriptItem{bad}
		}(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := append([]session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Run it.")}, c.items...)
			nodes := Build(Snapshot{
				Items: items,
				Steps: []session.Step{stepFor(1, "t1", 1, 9), stepFor(2, "t1", 10, 11)},
				Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"}),
			})
			if got := nodes[0].Children[1].Task.UnresolvedFailure; got != c.want {
				t.Fatalf("UnresolvedFailure = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReceiptArithmetic(t *testing.T) {
	edit := audit("file.write_patch", 1, "e", 2)
	edit.Audit.FilesChanged = []string{"a.go", "b.go"}
	edit2 := audit("file.write_patch", 2, "e2", 6)
	edit2.Audit.FilesChanged = []string{"b.go"}
	fin := final(9, 400)
	fin.Message.Usage = "212k tok"
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "A."), edit,
			todoWrite(1, "w", 3, `{"todos":[]}`),
			narration(3, 2, 5, "B."), edit2, audit("file.read", 2, "r", 7),
			fin,
		},
		Steps: []session.Step{stepFor(1, "t1", 1, 4), stepFor(2, "t2", 5, 8)},
		Todos: todos(
			db.TodoItem{ID: "t1", Content: "A", Status: "completed", CompletedAt: at(4)},
			db.TodoItem{ID: "t2", Content: "B", Status: "completed", CompletedAt: at(8)},
			db.TodoItem{ID: "t3", Content: "Old", Status: "completed", CompletedAt: at(-50)},
		),
	})
	var rc *ReceiptInfo
	for _, n := range nodes[0].Children {
		if n.Kind == KindReceipt {
			rc = n.Receipt
		}
	}
	if rc == nil {
		t.Fatal("a finished turn with steps gets a receipt")
	}
	if rc.Tasks != 2 || rc.Steps != 2 || rc.Tools != 3 || rc.Files != 2 || rc.Usage != "212k tok" || rc.Duration != 400*1e9 {
		t.Fatalf("receipt = %+v", rc)
	}
}

func TestNoReceiptWhileRunningOrWithoutSteps(t *testing.T) {
	has := func(nodes []*Node) bool {
		for _, n := range nodes[len(nodes)-1].Children {
			if n.Kind == KindReceipt {
				return true
			}
		}
		return false
	}
	running := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "A."), audit("file.read", 1, "a", 2), final(3, 5)},
		Steps: []session.Step{stepFor(1, "", 1, 3)}, Busy: true,
	})
	if has(running) {
		t.Error("no receipt while the turn is running")
	}
	bare := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), final(3, 5)}})
	if has(bare) {
		t.Error("no receipt for a turn with no steps")
	}
	_ = registry.AuditEvent{}
}

func queueOf(nodes []*Node) *QueueInfo {
	for _, n := range nodes[0].Children {
		if n.Kind == KindQueue {
			return n.Queue
		}
	}
	return nil
}

func TestQueueListsTodosWithoutATaskYet(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "t2", 1, 3)},
		Busy:  true,
		Todos: todos(
			db.TodoItem{ID: "t1", Content: "A", Status: "completed"},
			db.TodoItem{ID: "t2", Content: "B", Status: "in_progress"},
			db.TodoItem{ID: "t3", Content: "C", Status: "pending"},
			db.TodoItem{ID: "t4", Content: "D", Status: "pending"},
		),
	})
	q := queueOf(nodes)
	if q == nil || len(q.Items) != 2 || q.Items[0].TodoID != "t3" || q.Items[0].Index != 3 || q.Items[1].Total != 4 {
		t.Fatalf("queue should hold the two pending todos, got %+v", q)
	}
	kids := nodes[0].Children
	if kids[len(kids)-1].Kind != KindQueue {
		t.Fatalf("a running turn lists the queue last:\n%s", shape(nodes))
	}
}

func TestQueueSurvivesTheTurnEndingButOnlyOnTheLatestTurn(t *testing.T) {
	snap := Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2), final(3, 5)},
		Steps: []session.Step{stepFor(1, "t1", 1, 3)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "in_progress"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"}),
	}
	if q := queueOf(Build(snap)); q == nil || len(q.Items) != 1 || q.Items[0].TodoID != "t2" {
		t.Fatalf("a turn that ended with todos pending still lists them, got %+v", q)
	}
	// A later turn takes over as the latest: the earlier one keeps its task
	// rows and loses the list.
	snap.Items = append(snap.Items, userMsg(9, 20))
	nodes := Build(snap)
	for _, n := range nodes[0].Children {
		if n.Kind == KindQueue {
			t.Fatalf("only the latest turn carries the waiting list:\n%s", shape(nodes))
		}
	}
}

func TestTaskModeFollowsTheListsATurnWroteNotJustTheCurrentOne(t *testing.T) {
	steps := []session.Step{stepFor(1, "t1", 1, 3)}
	items := []session.TranscriptItem{
		userMsg(1, 0),
		narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2),
		todoWrite(1, "w", 3, `{"todos":[{"id":"t1","content":"A","status":"completed"},{"id":"t2","content":"B","status":"pending"}]}`),
	}
	one := todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"})
	two := todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"})

	// 2 -> 1: the list shrank after the turn wrote two items. The turn keeps
	// its task rows instead of flattening.
	if !strings.Contains(shape(Build(Snapshot{Items: items, Steps: steps, Todos: one})), "task:") {
		t.Fatal("rewriting the list to one item must not flatten a turn that worked from two")
	}
	// 1 -> 2: and it is the same with the longer list back.
	if !strings.Contains(shape(Build(Snapshot{Items: items, Steps: steps, Todos: two})), "task:") {
		t.Fatal("two items drive the turn")
	}
	// A turn that only ever saw one item stays flat whatever the list does later.
	flat := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2)}
	if strings.Contains(shape(Build(Snapshot{Items: flat, Steps: steps, Todos: one})), "task:") {
		t.Fatal("one item is not a plan")
	}
}

func TestQueueShowsOnceTheListIsWrittenEvenBeforeAnyStepRuns(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			todoWrite(1, "w", 1, `{"todos":[{"content":"A","status":"in_progress"}]}`),
		},
		Steps: []session.Step{stepFor(1, "", 1, 2)},
		Busy:  true,
		Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "in_progress"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"}),
	})
	q := queueOf(nodes)
	if q == nil || len(q.Items) != 2 || !q.Items[0].Active || q.Items[1].Active {
		t.Fatalf("the whole plan shows up front, the started todo marked active: %+v", q)
	}
}

func TestNoQueueForATurnThatNeverTouchedTheList(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Unrelated."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "", 1, 3)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "pending"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"}),
	})
	if queueOf(nodes) != nil || strings.Contains(shape(nodes), "task:") {
		t.Fatalf("leftover todos from an earlier turn must not decorate this one:\n%s", shape(nodes))
	}
}

func TestSingleTodoListFlowsWithoutTasksOrQueue(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2), final(3, 5)},
		Steps: []session.Step{stepFor(1, "t1", 1, 3)},
		Todos: todos(db.TodoItem{ID: "t1", Content: "A", Status: "in_progress"}),
	})
	if strings.Contains(shape(nodes), "task:") || queueOf(nodes) != nil {
		t.Fatalf("one todo is not a plan to follow:\n%s", shape(nodes))
	}
	for _, n := range nodes[0].Children {
		if n.Kind == KindReceipt && n.Receipt.Tasks != 0 {
			t.Fatalf("receipt counts no tasks for a one-item list: %+v", n.Receipt)
		}
	}
}
