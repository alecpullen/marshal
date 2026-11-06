package service

import (
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// stackState builds a state the way the adapter's tests do, on a frozen clock.
func stackState(t *testing.T, id string, fixed time.Time) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), fixed, session.Persistence{SessionID: id},
		session.WithClock(func() time.Time { return fixed }))
}

// hasKind reports whether any node of nodes has kind.
func hasKind(nodes []acpwire.WireNode, kind string) bool {
	for _, n := range nodes {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// TestStackIsTheSharedProjection pins that Stack serves the projection the TUI
// renders, at the state's clock: the same roots and nodes viewmodel.ProjectState
// builds, band node last.
func TestStackIsTheSharedProjection(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)

	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	beat := state.BeginStep(session.Actor{Model: "qwen3-coder"})
	state.AddNarration(beat, "Reading the config.")
	state.LogToolCall(registry.AuditEvent{
		Timestamp: fixed, ToolName: "file.read", StepID: beat, ToolCallID: "c1",
		ResultSummary: "ok",
	})
	state.EndStep(beat)
	if err := state.SetTodos([]db.TodoItem{
		{ID: "t1", Content: "read", Status: "completed"},
		{ID: "t2", Content: "write", Status: "in_progress"},
	}); err != nil {
		t.Fatalf("SetTodos = %v, want nil", err)
	}

	_, sess := New(Deps{State: state})
	got := sess.Stack(0, false)

	if got.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "s1")
	}
	if got.SubagentID != 0 {
		t.Errorf("SubagentID = %d, want 0", got.SubagentID)
	}
	if got.Rev != 0 {
		t.Errorf("Rev = %d, want 0", got.Rev)
	}
	want := viewmodel.ProjectState(state, false, state.Now()).Tree
	if !reflect.DeepEqual(got.Roots, want.Roots) {
		t.Errorf("Roots =\n%v\nwant\n%v", got.Roots, want.Roots)
	}
	if !reflect.DeepEqual(got.Nodes, want.Nodes) {
		t.Errorf("Nodes =\n%+v\nwant\n%+v", got.Nodes, want.Nodes)
	}
	if len(got.Nodes) == 0 || got.Nodes[len(got.Nodes)-1].Kind != acpwire.KindBand {
		t.Errorf("last node = %+v, want the band", got.Nodes)
	}
}

// TestStackBuildsWithTheBusyFlagItIsGiven pins that the flag decides the live
// beat and the turn's receipt, whatever subagentID names: a child registered as
// running is still projected not busy when the caller says so.
func TestStackBuildsWithTheBusyFlagItIsGiven(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)

	fill := func(st *session.State) {
		st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
		beat := st.BeginStep(session.Actor{Model: "qwen3-coder"})
		st.AddNarration(beat, "Running the tests.")
		st.EndStep(beat)
		st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypeMarkdown)
	}
	fill(state)
	child := stackState(t, "child", fixed)
	fill(child)
	view := state.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: state})
	if got := sess.Stack(0, false); !hasKind(got.Nodes, acpwire.KindReceipt) {
		t.Errorf("Stack(0, false) has no receipt; want the finished turn's")
	}
	if got := sess.Stack(0, true); hasKind(got.Nodes, acpwire.KindReceipt) {
		t.Errorf("Stack(0, true) carries a receipt; want none while the turn runs")
	}
	if got := sess.Stack(view.ID, false); !hasKind(got.Nodes, acpwire.KindReceipt) {
		t.Errorf("Stack(%d, false) has no receipt; want the finished turn's", view.ID)
	}
	if got := sess.Stack(view.ID, true); hasKind(got.Nodes, acpwire.KindReceipt) {
		t.Errorf("Stack(%d, true) carries a receipt; want none while the turn runs", view.ID)
	}
}

// TestStackOfASubagentReadsItsTranscript pins that a subagent ID reads the
// child's transcript, not the session's, and keeps the session's identity.
func TestStackOfASubagentReadsItsTranscript(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "parent says hi", session.ContentTypePlain)

	child := stackState(t, "child", fixed)
	child.AddMessage(session.RoleUser, "child says hi", session.ContentTypePlain)
	view := state.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: state})
	got := sess.Stack(view.ID, false)
	if got.SubagentID != view.ID {
		t.Errorf("SubagentID = %d, want %d", got.SubagentID, view.ID)
	}
	if got.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "s1")
	}

	var texts []string
	for _, n := range got.Nodes {
		if n.Kind != acpwire.KindMessage || n.Message == nil {
			continue
		}
		texts = append(texts, n.Message.Content)
	}
	found := false
	for _, text := range texts {
		if text == "child says hi" {
			found = true
		}
		if text == "parent says hi" {
			t.Errorf("the child's stack holds the parent's message %q", text)
		}
	}
	if !found {
		t.Errorf("messages = %v, want the child's %q", texts, "child says hi")
	}
}

// TestStackOfAnUnknownSubagentIsEmpty pins that an ID that names no child the
// session keeps reads as an empty snapshot carrying that ID.
func TestStackOfAnUnknownSubagentIsEmpty(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	_, sess := New(Deps{State: state})

	got := sess.Stack(987654321, false)
	if got.SubagentID != 987654321 {
		t.Errorf("SubagentID = %d, want 987654321", got.SubagentID)
	}
	if len(got.Roots) != 0 {
		t.Errorf("Roots = %v, want none", got.Roots)
	}
	if len(got.Nodes) != 0 {
		t.Errorf("Nodes = %+v, want none", got.Nodes)
	}
}

// TestStackReadsTheStatesClock pins that Stack builds at the state's clock: on
// a frozen clock in 2023 a stale list is not stale, where the wall clock would
// long since have made it so.
func TestStackReadsTheStatesClock(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	state := session.New(config.Default(), t.TempDir(), clock, session.Persistence{SessionID: "s1"},
		session.WithClock(func() time.Time { return clock }))

	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	if err := state.SetTodos([]db.TodoItem{
		{ID: "t1", Content: "read", Status: "completed"},
		{ID: "t2", Content: "write", Status: "in_progress"},
	}); err != nil {
		t.Fatalf("SetTodos = %v, want nil", err)
	}
	// Eight responses by the list's owner, then the clock stays put.
	clock = clock.Add(time.Second)
	for i := 0; i < session.TodoStaleResponses; i++ {
		id := state.BeginStep(session.Actor{})
		state.EndStep(id)
	}

	_, sess := New(Deps{State: state})
	band := func() acpwire.WireBand {
		t.Helper()
		for _, n := range sess.Stack(0, true).Nodes {
			if n.Kind == acpwire.KindBand && n.Band != nil {
				return *n.Band
			}
		}
		t.Fatal("Stack(0, true) holds no band node")
		return acpwire.WireBand{}
	}

	if got := band(); got.Stale {
		t.Errorf("band is stale at the state's clock; want not stale (the list's last write is in 2023)")
	}

	clock = clock.Add(session.TodoStaleAfter + time.Minute)
	if got := band(); !got.Stale {
		t.Errorf("band is not stale after TodoStaleAfter plus a minute; want stale")
	}
}
