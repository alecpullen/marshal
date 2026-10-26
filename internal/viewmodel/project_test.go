package viewmodel

import (
	"reflect"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

// projTestState is an empty session state with no persistence, so the
// projection tests read only what they set.
func projTestState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), "/repo", at(0), session.Persistence{})
}

func TestSnapshotOfIsBusyWhileWorkRuns(t *testing.T) {
	now := at(5)
	if snap := SnapshotOf(projTestState(t), false, now); snap.Busy {
		t.Fatal("an idle state is not busy")
	}

	call := projTestState(t)
	call.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", ToolCallID: "c1", StartedAt: at(1)})
	if snap := SnapshotOf(call, false, now); !snap.Busy {
		t.Fatal("a running call makes the snapshot busy")
	}

	thinking := projTestState(t)
	thinking.BeginStreaming()
	thinking.AppendThinking("Reading the parser.")
	if snap := SnapshotOf(thinking, false, now); !snap.Busy {
		t.Fatal("streaming reasoning makes the snapshot busy")
	}

	if snap := SnapshotOf(projTestState(t), true, now); !snap.Busy {
		t.Fatal("a running turn makes the snapshot busy")
	}
}

func TestSnapshotOfCarriesTheState(t *testing.T) {
	st := projTestState(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	st.AddNarration(st.BeginStep(session.Actor{}), "Reading the parser.")
	st.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", ToolCallID: "c1", StartedAt: at(1)})
	st.BeginStreaming()
	st.AppendThinking("Consider the grammar.")
	if err := st.SetTodos(todos(db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"})); err != nil {
		t.Fatal(err)
	}
	now := at(9)
	snap := SnapshotOf(st, true, now)
	if !reflect.DeepEqual(snap.Items, st.Transcript()) {
		t.Fatalf("items = %+v", snap.Items)
	}
	if !reflect.DeepEqual(snap.Steps, st.Steps()) {
		t.Fatalf("steps = %+v", snap.Steps)
	}
	if !reflect.DeepEqual(snap.Todos, st.Todos()) {
		t.Fatalf("todos = %+v", snap.Todos)
	}
	if !reflect.DeepEqual(snap.ActiveTools, st.ActiveToolCalls()) {
		t.Fatalf("activeTools = %+v", snap.ActiveTools)
	}
	if !reflect.DeepEqual(snap.InProgress, st.InProgress()) {
		t.Fatalf("inProgress = %+v", snap.InProgress)
	}
	if !snap.Now.Equal(now) {
		t.Fatalf("now = %v, want %v", snap.Now, now)
	}
}

// The band rides along as the tree's last node and stays out of Roots, so a
// transcript walk never sees it but a client receives it like any other node.
func TestProjectStateAppendsTheBandAfterTheTree(t *testing.T) {
	st := projTestState(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	st.AddNarration(st.BeginStep(session.Actor{}), "Reading the parser.")
	if err := st.SetTodos(todos(
		db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"},
		db.TodoItem{ID: "q", Content: "Tests", Status: "pending"},
	)); err != nil {
		t.Fatal(err)
	}
	now := at(10)
	proj := ProjectState(st, true, now)

	snap := SnapshotOf(st, true, now)
	turns := Build(snap)
	want := Project(turns, st.Workspace().ActiveRoot)
	if !reflect.DeepEqual(proj.Tree.Roots, want.Roots) {
		t.Fatalf("roots = %v, want %v", proj.Tree.Roots, want.Roots)
	}
	if len(proj.Tree.Nodes) != len(want.Nodes)+1 {
		t.Fatalf("nodes = %d, want %d plus the band", len(proj.Tree.Nodes), len(want.Nodes))
	}
	if !reflect.DeepEqual(proj.Tree.Nodes[:len(want.Nodes)], want.Nodes) {
		t.Fatalf("nodes = %+v, want %+v then the band", proj.Tree.Nodes, want.Nodes)
	}
	last := proj.Tree.Nodes[len(proj.Tree.Nodes)-1]
	if last.ID != acpwire.BandID || last.Kind != acpwire.KindBand {
		t.Fatalf("last node = %+v, want the band", last)
	}
	for _, r := range proj.Tree.Roots {
		if r == acpwire.BandID {
			t.Fatalf("the band is in roots: %v", proj.Tree.Roots)
		}
	}
	if band := BandOf(snap, turns); !reflect.DeepEqual(proj.Band, band) {
		t.Fatalf("band = %+v, want %+v", proj.Band, band)
	}
	if !reflect.DeepEqual(proj.Tree.Nodes[len(proj.Tree.Nodes)-1], ProjectBand(proj.Band)) {
		t.Fatalf("last node = %+v, want ProjectBand(Band)", last)
	}
}
