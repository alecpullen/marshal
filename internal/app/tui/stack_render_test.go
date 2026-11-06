package tui

import (
	"reflect"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/native"
)

// stackRecorder is a session whose Stack calls are recorded: how many, and
// the arguments of the last one. Every other method is the wrapped session's.
type stackRecorder struct {
	acpclient.Session
	calls        int
	lastSubagent int64
	lastBusy     bool
}

// Unwrap exposes the adapter this recorder wraps, so stateOf reaches the
// state behind it.
func (s *stackRecorder) Unwrap() acpclient.Session { return s.Session }

func (s *stackRecorder) Stack(subagentID int64, busy bool) acpwire.StackSnapshot {
	s.calls++
	s.lastSubagent = subagentID
	s.lastBusy = busy
	return s.Session.Stack(subagentID, busy)
}

// The viewport's tree is the session's stack, asked for with the TUI's own
// busy flag and the drilled-in child's ID.
func TestRefreshAsksTheSessionForItsStack(t *testing.T) {
	m := newTestModel(t)
	rec := &stackRecorder{Session: m.sess}
	m.sess = rec

	m.busy = true
	m.invalidateTranscript()
	m.refreshViewport()
	if rec.calls == 0 {
		t.Fatal("a refresh must ask the session for its stack")
	}
	if rec.lastSubagent != 0 || !rec.lastBusy {
		t.Fatalf("refresh asked for subagent %d busy %v, want 0 and true", rec.lastSubagent, rec.lastBusy)
	}

	child := session.New(stateOf(m).Config, t.TempDir(), m.now(), session.Persistence{})
	v := stateOf(m).RegisterSubagent("child", child)
	m.drillIntoSubagent(v.ID)
	m.invalidateTranscript()
	m.refreshViewport()
	if rec.lastSubagent != v.ID {
		t.Fatalf("drilled refresh asked for subagent %d, want %d", rec.lastSubagent, v.ID)
	}
}

// The band and the completion index come from the stack's own nodes, and the
// index leaves the band out.
func TestTheBandAndTheIndexComeFromTheStack(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	if err := stateOf(m).SetTodos([]native.TodoItem{
		{ID: "t1", Content: "first", Status: native.TodoInProgress},
		{ID: "t2", Content: "second", Status: native.TodoPending},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	step := stateOf(m).BeginStep(session.Actor{})
	stateOf(m).AddNarration(step, "Working the first item.")
	stateOf(m).EndStep(step)

	m.busy = false
	m.invalidateTranscript()
	m.refreshViewport()

	snap := m.sess.Stack(0, false)
	if want := bandOf(snap.Nodes); !reflect.DeepEqual(m.band, want) {
		t.Fatalf("band = %+v, want the stack's %+v", m.band, want)
	}
	if len(m.band.Tasks) == 0 {
		t.Fatal("the band must carry the whole task list")
	}
	nodes := map[string]bool{}
	for _, n := range snap.Nodes {
		nodes[n.ID] = true
	}
	for id, w := range m.wire {
		if w.Kind == acpwire.KindBand {
			t.Fatalf("the index holds the band node %q", id)
		}
		if !nodes[id] {
			t.Fatalf("index key %q is not a node of the stack", id)
		}
	}
}
