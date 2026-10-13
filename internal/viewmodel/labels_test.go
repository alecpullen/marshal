package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
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

func todos(items ...db.TodoItem) []db.TodoItem { return items }

func labelsOf(nodes []*Node) []string {
	var out []string
	for _, turn := range nodes {
		for _, n := range turn.Children {
			if n.Step != nil && n.Step.Todo != nil {
				out = append(out, n.ID.Key+"="+n.Step.Todo.ID)
			}
		}
	}
	return out
}

// The screenshot bug: Task 0 stuck in progress across many beats with
// subagent notices and messages between them. Exactly one label.
func TestStuckTodoGetsOneLabelAcrossInterruptions(t *testing.T) {
	sys := func(id int64, s int) session.TranscriptItem {
		return session.TranscriptItem{Timestamp: at(s), Kind: session.KindMessage,
			Message: &session.Message{ID: id, Role: session.RoleSystem, Content: "note", CreatedAt: at(s)}}
	}
	items := []session.TranscriptItem{userMsg(1, 0)}
	var steps []session.Step
	for i := 1; i <= 6; i++ {
		id := session.StepID(i)
		items = append(items, narration(int64(10+i), id, i*10, "Waiting for the next completion."), sys(int64(100+i), i*10+5))
		steps = append(steps, stepFor(id, "t0", i*10, i*10+4))
	}
	for i := range steps {
		steps[i].TurnMsgID = 1
	}
	nodes := Build(Snapshot{Items: items, Steps: steps,
		Todos: todos(db.TodoItem{ID: "t0", Content: "Task 0", Status: "in_progress"}, db.TodoItem{ID: "t1", Content: "Task 1", Status: "pending"})})
	if got := labelsOf(nodes); len(got) != 1 || got[0] != "step:1=t0" {
		t.Fatalf("labels = %v\n%s", got, shape(nodes))
	}
	if strings.Contains(shape(nodes), "task:") {
		t.Fatalf("no task nodes any more:\n%s", shape(nodes))
	}
}

func TestLabelOnEachTransition(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Parser first."), audit("file.read", 1, "a", 2),
			narration(3, 2, 3, "Tests now."), audit("file.read", 2, "b", 4),
			narration(4, 3, 5, "Back to parser."), audit("file.read", 3, "c", 6),
		},
		Steps: []session.Step{stepFor(1, "p", 1, 2), stepFor(2, "t", 3, 4), stepFor(3, "p", 5, 6)},
		Todos: todos(db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"}, db.TodoItem{ID: "t", Content: "Tests", Status: "pending"}),
	})
	want := []string{"step:1=p", "step:2=t", "step:3=p"}
	if got := labelsOf(nodes); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v", got)
	}
	l := nodes[0].Children[1].Step.Todo
	if l.Index != 1 || l.Total != 2 || l.Content != "Parser" {
		t.Fatalf("label = %+v", l)
	}
}

func TestNoLabelForDroppedTodoOrShortList(t *testing.T) {
	dropped := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Old work."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "gone", 1, 2)},
		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "pending"}, db.TodoItem{ID: "y", Content: "Y", Status: "pending"}),
	})
	short := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Only item."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "x", 1, 2)},
		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "in_progress"}),
	})
	if got := labelsOf(dropped); len(got) != 0 {
		t.Fatalf("dropped todo labelled: %v", got)
	}
	if got := labelsOf(short); len(got) != 0 {
		t.Fatalf("one-item list labelled: %v", got)
	}
}

func TestLabelResetsPerTurn(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Parser."), audit("file.read", 1, "a", 2),
			userMsg(5, 10), narration(6, 2, 11, "Still parser."), audit("file.read", 2, "b", 12),
		},
		Steps: []session.Step{func() session.Step { s := stepFor(1, "p", 1, 2); s.TurnMsgID = 1; return s }(),
			func() session.Step { s := stepFor(2, "p", 11, 12); s.TurnMsgID = 5; return s }()},
		Todos: todos(db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"}, db.TodoItem{ID: "q", Content: "Q", Status: "pending"}),
	})
	if got := labelsOf(nodes); len(got) != 2 {
		t.Fatalf("each turn's first bound beat is labelled: %v", got)
	}
}

func TestFoldKeepsNewestThreeAndRunningCards(t *testing.T) {
	items := []session.TranscriptItem{userMsg(1, 0)}
	var steps []session.Step
	for i := 1; i <= 6; i++ {
		id := session.StepID(i)
		items = append(items, narration(int64(10+i), id, i*10, "Beat."), audit("file.read", id, "r", i*10+1))
		steps = append(steps, stepOf(id, 1, i*10, i*10+2))
	}
	running := session.TranscriptItem{Timestamp: at(11), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "child", Status: session.SubagentRunning, StartedAt: at(11)}}
	items = append(items, running)
	nodes := Build(Snapshot{Items: items, Steps: steps, Busy: true, Now: at(100)})
	var folded []string
	for _, n := range nodes[0].Children {
		if n.Step != nil && n.Step.Folded {
			folded = append(folded, n.ID.Key)
		}
	}
	// step:1 holds the running card; 4..6 are the newest three.
	if strings.Join(folded, ",") != "step:2,step:3" {
		t.Fatalf("folded = %v\n%s", folded, shape(nodes))
	}
}

func TestEarlierTurnsFoldEveryFinishedBeat(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Old."), audit("file.read", 1, "a", 2), final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "New."), audit("file.read", 2, "b", 12),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 2), stepOf(2, 5, 11, 12)},
	})
	if !nodes[0].Children[1].Step.Folded || nodes[1].Children[1].Step.Folded {
		t.Fatalf("old turn folds, newest-3 of last turn do not:\n%s", shape(nodes))
	}
}

// DrivenByTodos is settled by the brief's code block, not its prose:
// n.Step.Todo != nil || n.Step.TodoID != "", so any beat bound to a non-empty
// TodoID drives the turn even when the todo is gone from the current list or
// the list is a one-item one. This test pins that ruling so the divergence
// from the prose cannot creep back in silently.
func TestDrivenByTodosFollowsTheCodeBlockNotTheProse(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		want bool
	}{
		{
			// A dropped todo gets no label, but the beat stays bound to it.
			name: "todo no longer in the list",
			snap: Snapshot{
				Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Old work."), audit("file.read", 1, "a", 2)},
				Steps: []session.Step{stepFor(1, "gone", 1, 2)},
				Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "pending"}, db.TodoItem{ID: "y", Content: "Y", Status: "pending"}),
			},
			want: true,
		},
		{
			// One item is too short for a label, but the binding still counts.
			name: "one-item list",
			snap: Snapshot{
				Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Only item."), audit("file.read", 1, "a", 2)},
				Steps: []session.Step{stepFor(1, "x", 1, 2)},
				Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "in_progress"}),
			},
			want: true,
		},
		{
			name: "labelled beat",
			snap: Snapshot{
				Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Parser."), audit("file.read", 1, "a", 2)},
				Steps: []session.Step{stepFor(1, "p", 1, 2)},
				Todos: todos(db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"}, db.TodoItem{ID: "q", Content: "Q", Status: "pending"}),
			},
			want: true,
		},
		{
			name: "no bound beats",
			snap: Snapshot{
				Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Unrelated."), audit("file.read", 1, "a", 2)},
				Steps: []session.Step{stepFor(1, "", 1, 2)},
				Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "pending"}, db.TodoItem{ID: "y", Content: "Y", Status: "pending"}),
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes := Build(c.snap)
			if got := DrivenByTodos(nodes[0]); got != c.want {
				t.Fatalf("DrivenByTodos = %v, want %v\n%s", got, c.want, shape(nodes))
			}
		})
	}
	// The label alone drives the turn, with no stored binding at all: this is
	// the n.Step.Todo != nil half of the predicate.
	labelled := &Node{Kind: KindTurn, Children: []*Node{
		{Kind: KindStep, Step: &StepInfo{Todo: &TodoLabel{ID: "p"}}},
	}}
	if !DrivenByTodos(labelled) {
		t.Fatal("a labelled beat with no binding must drive the turn")
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
}

// taskMode gates the transition labels now that task rows are gone: a turn
// counts as todo-driven from the longest list it wrote, not from the list as
// it stands, so a later rewrite to one item must not unlabel finished work.
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
	// its label instead of flattening.
	if got := labelsOf(Build(Snapshot{Items: items, Steps: steps, Todos: one})); len(got) == 0 {
		t.Fatal("rewriting the list to one item must not flatten a turn that worked from two")
	}
	// 1 -> 2: and it is the same with the longer list back.
	if got := labelsOf(Build(Snapshot{Items: items, Steps: steps, Todos: two})); len(got) == 0 {
		t.Fatal("two items drive the turn")
	}
	// A turn that only ever saw one item stays flat whatever the list does later.
	flat := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2)}
	if got := labelsOf(Build(Snapshot{Items: flat, Steps: steps, Todos: one})); len(got) != 0 {
		t.Fatalf("one item is not a plan: %v", got)
	}
}
