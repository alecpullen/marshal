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

// The runner nudges on its own response count, whatever its role. The band
// must count the same responses: the turn opener's, not only the general
// role's, and not a different actor's sharing the session.
func TestTodoClockCountsTheTurnOpenersResponses(t *testing.T) {
	var steps []session.Step
	for i := 1; i <= 4; i++ {
		st := stepOf(session.StepID(i), 1, i*10, i*10+5)
		st.Actor = session.Actor{Role: "architect", Label: "architect"}
		steps = append(steps, st)
	}
	other := stepOf(5, 1, 60, 65)
	other.Actor = session.Actor{Role: "implementer", Label: "implementer"}
	steps = append(steps, other)
	c := TodoClockOf(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0)}, Steps: steps})
	if c.Responses != 4 {
		t.Fatalf("responses = %d, want the 4 by the turn's opener", c.Responses)
	}
}

// An unfinished list is pinned only while a turn runs, in the band itself, so
// the web client hides it when idle exactly as the TUI does.
func TestBandHidesTodosWhenIdle(t *testing.T) {
	s := Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), todoWrite(1, "w", 1, `{"todos":[]}`), narration(2, 1, 1, "On A.")},
		Steps: []session.Step{stepOf(1, 1, 1, 2)}, Now: at(10),
		Todos: todos(db.TodoItem{ID: "a", Content: "A", Status: "in_progress"}, db.TodoItem{ID: "b", Content: "B", Status: "pending"})}
	steps := s.Steps
	steps[0].TodoID = "a"
	if b := BandOf(s, Build(s)); b.Todos {
		t.Fatalf("idle band shows todos: %+v", b)
	}
	s.Busy = true
	b := BandOf(s, Build(s))
	if !b.Todos {
		t.Fatalf("busy band hides todos: %+v", b)
	}
	if want := at(1).Add(session.TodoStaleAfter); !b.StaleAt.Equal(want) {
		t.Fatalf("StaleAt = %v, want %v (the write plus the stale age)", b.StaleAt, want)
	}
}

// The Tasks views show the whole list with each item's share of the
// transcript, idle included: the list outlives the band's own gating, so an
// unfinished one is still there to read once the turn ends.
func TestBandTasksCarryTheWholeListWhenIdle(t *testing.T) {
	s := Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), todoWrite(1, "w", 1, `{"todos":[]}`), narration(2, 1, 1, "On A.")},
		Steps: []session.Step{stepOf(1, 1, 1, 4)}, Now: at(10),
		Todos: todos(db.TodoItem{ID: "a", Content: "A\x07", Status: "in_progress", StartedAt: at(1)}, db.TodoItem{ID: "b", Content: "B", Status: "pending"})}
	s.Steps[0].TodoID = "a"
	turns := Build(s)
	var beat NodeID
	for _, n := range turns[len(turns)-1].Children {
		if n.Kind == KindStep {
			beat = n.ID
		}
	}
	b := BandOf(s, turns)
	if b.Todos || len(b.Tasks) != 2 {
		t.Fatalf("idle band: todos=%v tasks=%+v, want the list without the pinned rows", b.Todos, b.Tasks)
	}
	a := b.Tasks[0]
	if a.Content != "A" || a.Status != "in_progress" || !a.StartedAt.Equal(at(1)) {
		t.Fatalf("task a = %+v, want scrubbed content and its status and start", a)
	}
	if a.Steps != 1 || a.Work != at(4).Sub(at(1)) || beat == (NodeID{}) || a.First != beat {
		t.Fatalf("task a stat = %+v, want the one beat bound to it", a.TaskStat)
	}
	if b.Tasks[1].Steps != 0 || b.Tasks[1].First != (NodeID{}) {
		t.Fatalf("task b = %+v, want no beats yet", b.Tasks[1])
	}
	w := ProjectBand(b).Band
	if len(w.Tasks) != 2 || w.Tasks[0].Ref != a.First.Key || w.Tasks[0].WorkMs != 3000 || w.Tasks[1].Ref != "" {
		t.Fatalf("wire tasks = %+v", w.Tasks)
	}
}
