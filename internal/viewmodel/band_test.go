package viewmodel

import (
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
)

func TestBandProgressNextAndStale(t *testing.T) {
	items := []session.TranscriptItem{userMsg(1, 0), todoWrite(1, "w", 1, `{"todos":[]}`)}
	var steps []session.Step
	for i := 1; i <= 9; i++ {
		steps = append(steps, stepOf(session.StepID(i), 1, i*40, i*40+5))
		items = append(items, narration(int64(10+i), session.StepID(i), i*40, "Still on it."))
	}
	for i := range steps {
		steps[i].TodoID = "b"
	}
	s := Snapshot{Items: items, Steps: steps, Busy: true, Now: at(400),
		Todos: todos(db.TodoItem{ID: "a", Content: "A", Status: "completed"}, db.TodoItem{ID: "b", Content: "B", Status: "in_progress"},
			db.TodoItem{ID: "c", Content: "C", Status: "pending"}, db.TodoItem{ID: "d", Content: "D", Status: "pending"})}
	b := BandOf(s, Build(s))
	if !b.Todos || b.Done != 1 || b.Total != 4 || b.Active != "B" || len(b.Next) != 2 || b.Next[0] != "C" {
		t.Fatalf("band = %+v", b)
	}
	if !b.Stale {
		t.Fatalf("399s and 9 responses since the write must be stale: %+v", b)
	}
}

func TestBandEntitiesAreRunningCards(t *testing.T) {
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "Task 1", Status: session.SubagentRunning, StartedAt: at(2)}}
	s := Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), sub, jobStart(1, "j", 3, "job-4")},
		Steps: []session.Step{stepOf(1, 1, 1, 0)}, Busy: true, Now: at(10)}
	b := BandOf(s, Build(s))
	labels := map[string]bool{}
	for _, e := range b.Entities {
		labels[e.Label] = true
	}
	if len(b.Entities) != 2 || !labels["#3 Task 1"] || !labels["job-4"] {
		t.Fatalf("entities = %+v", b.Entities)
	}
}
