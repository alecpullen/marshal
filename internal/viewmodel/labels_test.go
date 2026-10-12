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

func shellAudit(step session.StepID, callID string, s, exit int) session.TranscriptItem {
	it := audit("shell.run", step, callID, s)
	it.Audit.CommandExitCode = &exit
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
