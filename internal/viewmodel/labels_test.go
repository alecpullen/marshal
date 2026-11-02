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

func TestNoLabelForDroppedTodo(t *testing.T) {
	dropped := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Old work."), audit("file.read", 1, "a", 2)},
		Steps: []session.Step{stepFor(1, "gone", 1, 2)},
		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "pending"}, db.TodoItem{ID: "y", Content: "Y", Status: "pending"}),
	})
	if got := labelsOf(dropped); len(got) != 0 {
		t.Fatalf("dropped todo labelled: %v", got)
	}
}

// A one-item list is a plan like any other: its beat carries the 1/1 tag,
// once, however many beats work on it.
func TestOneItemListLabelsItsFirstBeat(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Only item."), audit("file.read", 1, "a", 2),
			narration(3, 2, 3, "Still on it."), audit("file.read", 2, "b", 4),
		},
		Steps: []session.Step{stepFor(1, "x", 1, 2), stepFor(2, "x", 3, 4)},
		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "in_progress"}),
	})
	if got := labelsOf(nodes); strings.Join(got, ",") != "step:1=x" {
		t.Fatalf("labels = %v\n%s", got, shape(nodes))
	}
	if l := nodes[0].Children[1].Step.Todo; l.Index != 1 || l.Total != 1 {
		t.Fatalf("label = %+v", l)
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
			todoWrite(1, "w", 3, `{"todos":[{"id":"t1","content":"A","status":"completed"},{"id":"t2","content":"B","status":"completed"}]}`),
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

// Labels do not depend on how long the list is or was: rewriting the list
// shorter or longer never unlabels finished work, and a turn that only ever
// saw one item is labelled too.
func TestLabelsDoNotDependOnListLength(t *testing.T) {
	steps := []session.Step{stepFor(1, "t1", 1, 3)}
	items := []session.TranscriptItem{
		userMsg(1, 0),
		narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2),
		todoWrite(1, "w", 3, `{"todos":[{"id":"t1","content":"A","status":"completed"},{"id":"t2","content":"B","status":"pending"}]}`),
	}
	one := todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"})
	two := todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"})
	for name, list := range map[string][]db.TodoItem{"one": one, "two": two} {
		if got := labelsOf(Build(Snapshot{Items: items, Steps: steps, Todos: list})); len(got) != 1 {
			t.Fatalf("%s-item list: labels = %v", name, got)
		}
	}
	flat := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2)}
	if got := labelsOf(Build(Snapshot{Items: flat, Steps: steps, Todos: one})); len(got) != 1 {
		t.Fatalf("a turn that saw one item is labelled: %v", got)
	}
}

// The tag reads the plan in effect, not the list as it stands now, so a list
// rewritten later cannot change a row that already settled.
func TestTagReadsThePlanNotTheCurrentList(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."), audit("file.read", 1, "r", 2),
			todoWrite(1, "w", 3, `{"todos":[{"id":"a","content":"A","status":"in_progress"},{"id":"b","content":"B","status":"pending"},{"id":"c","content":"C","status":"pending"}]}`),
			narration(3, 2, 5, "Doing A."), audit("file.read", 2, "r2", 6),
		},
		Steps: []session.Step{stepFor(1, "", 1, 4), stepFor(2, "a", 5, 7)},
		Todos: todos(
			db.TodoItem{ID: "a", Content: "A", Status: "in_progress"},
			db.TodoItem{ID: "b", Content: "B", Status: "pending"},
			db.TodoItem{ID: "c", Content: "C", Status: "pending"},
			db.TodoItem{ID: "d", Content: "D", Status: "pending"},
		),
	})
	l := findNode(nodes, "step:2").Step.Todo
	if l == nil || l.ID != "a" || l.Index != 1 || l.Total != 3 || l.Content != "A" {
		t.Fatalf("label = %+v, want the plan's 1/3 A", l)
	}
}

// The same steps with the todo dropped from the current list: the tag is
// unchanged, because it never read that list.
func TestTagSurvivesDroppingItsTodo(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."), audit("file.read", 1, "r", 2),
			todoWrite(1, "w", 3, `{"todos":[{"id":"a","content":"A","status":"in_progress"},{"id":"b","content":"B","status":"pending"},{"id":"c","content":"C","status":"pending"}]}`),
			narration(3, 2, 5, "Doing A."), audit("file.read", 2, "r2", 6),
		},
		Steps: []session.Step{stepFor(1, "", 1, 4), stepFor(2, "a", 5, 7)},
		Todos: todos(
			db.TodoItem{ID: "b", Content: "B", Status: "pending"},
			db.TodoItem{ID: "c", Content: "C", Status: "pending"},
		),
	})
	l := findNode(nodes, "step:2").Step.Todo
	if l == nil || l.ID != "a" || l.Index != 1 || l.Total != 3 || l.Content != "A" {
		t.Fatalf("label = %+v, want the plan's 1/3 A", l)
	}
}

// A narrated beat whose only call is a write is re-bound to the todo the same
// actor's next step was bound to, and the next step merges into it.
func TestWriteOnlyBeatTakesTheNextStepsTodo(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Moving on."),
			todoWrite(1, "w", 2, `{"todos":[{"id":"a","content":"A","status":"completed"},{"id":"b","content":"B","status":"in_progress"}]}`),
			audit("file.read", 2, "r", 4),
		},
		Steps: []session.Step{stepFor(1, "", 1, 3), stepFor(2, "b", 4, 6)},
		Todos: todos(
			db.TodoItem{ID: "a", Content: "A", Status: "completed"},
			db.TodoItem{ID: "b", Content: "B", Status: "in_progress"},
		),
	})
	if got := shape(nodes); strings.Count(got, "step:") != 1 {
		t.Fatalf("the narrationless step merges into the beat:\n%s", got)
	}
	beat := nodes[0].Children[1]
	if beat.Step.TodoID != "b" {
		t.Fatalf("beat todo = %q, want the next step's binding", beat.Step.TodoID)
	}
	if l := beat.Step.Todo; l == nil || l.ID != "b" || l.Index != 2 || l.Total != 2 || l.Content != "B" {
		t.Fatalf("label = %+v, want the plan's 2/2 B", l)
	}
}

// With no next step to take its binding from, a write-only beat keeps the
// binding it was stored with.
func TestWriteOnlyBeatKeepsItsBindingWithoutANextStep(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Moving on."),
			todoWrite(1, "w", 2, `{"todos":[{"id":"a","content":"A","status":"in_progress"}]}`),
		},
		Steps: []session.Step{stepFor(1, "a", 1, 3)},
		Todos: todos(db.TodoItem{ID: "a", Content: "A", Status: "in_progress"}),
	})
	if beat := nodes[0].Children[1]; beat.Step.TodoID != "a" {
		t.Fatalf("beat todo = %q, want its stored binding", beat.Step.TodoID)
	}
}

// The receipt counts what the turn's plan lines moved to completed, so a later
// write that drops the completed item cannot change a settled receipt.
func TestReceiptCountsThePlansCompletions(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Doing A."), audit("file.read", 1, "r", 2),
			todoWrite(1, "w1", 3, `{"todos":[{"id":"a","content":"A","status":"completed"},{"id":"b","content":"B","status":"in_progress"}]}`),
			final(3, 4),
			userMsg(5, 10),
			narration(6, 2, 11, "Doing B."), audit("file.read", 2, "r2", 12),
			todoWrite(2, "w2", 13, `{"todos":[{"id":"b","content":"B","status":"completed"}]}`),
			final(7, 14),
		},
		Steps: []session.Step{stepFor(1, "a", 1, 3), stepFor(2, "b", 11, 13)},
		Todos: todos(db.TodoItem{ID: "b", Content: "B", Status: "completed"}),
	})
	var rc *ReceiptInfo
	for _, n := range nodes[0].Children {
		if n.Kind == KindReceipt {
			rc = n.Receipt
		}
	}
	if rc == nil || rc.Tasks != 1 {
		t.Fatalf("turn 1 receipt = %+v, want one task", rc)
	}
}

func TestDisplayToolName(t *testing.T) {
	cases := map[string]string{
		"file.read": "Read file",
		"agent.run": "Run subagent",
		// agent.await is multipurpose (subagents, jobs, watches): the row
		// describes the action, not the target class.
		"agent.await":      "Waiting…",
		"file.write_patch": "Edit file",
		"shell.run":        "Run command",
		// Unknown tools still fall through to the title-case fallback.
		"custom.box": "Custom box",
	}
	for name, want := range cases {
		if got := DisplayToolName(name); got != want {
			t.Errorf("DisplayToolName(%q) = %q, want %q", name, got, want)
		}
	}
}
