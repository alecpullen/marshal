package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
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

// TestStackCarriesNoRevision pins that the service keeps no revisions: the
// agent's mirror numbers its snapshots, and the service's Stack is always 0.
// The rest of the projection's assertions live in the conformance harness,
// which reads both arms (V1).
func TestStackCarriesNoRevision(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)

	_, sess := New(Deps{State: state})
	if got := sess.Stack(0, false); got.Rev != 0 {
		t.Errorf("Rev = %d, want 0: the service keeps no revisions", got.Rev)
	}
}

// stackCallNode finds the tool node the snapshot carries for the finished call
// callID.
func stackCallNode(t *testing.T, snap acpwire.StackSnapshot, callID string) acpwire.WireNode {
	t.Helper()
	for _, n := range snap.Nodes {
		if n.Kind != acpwire.KindTool || n.Tool == nil {
			continue
		}
		for _, c := range n.Tool.Calls {
			if c.CallID == callID {
				return n
			}
		}
	}
	t.Fatalf("no tool node carries call %q", callID)
	return acpwire.WireNode{}
}

// recordCall runs one finished tool call as its own step on st.
func recordCall(st *session.State, name, callID string, at time.Time) {
	beat := st.BeginStep(session.Actor{Model: "qwen3-coder"})
	st.LogToolCall(registry.AuditEvent{
		Timestamp: at, ToolName: name, StepID: beat, ToolCallID: callID,
		ResultSummary: "ok",
	})
	st.EndStep(beat)
}

// TestStackReusesTheMainMemo pins that the session's own transcript is read
// through a memo: an unchanged node comes back as it was, and a call made since
// the last read is in the tree the next one returns.
func TestStackReusesTheMainMemo(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	recordCall(state, "file.read", "c1", fixed)

	_, sess := New(Deps{State: state})
	first := stackCallNode(t, sess.Stack(0, true), "c1")
	if first.Version == "" {
		t.Fatalf("node %q carries no version; want one the memo may keep", first.ID)
	}
	second := stackCallNode(t, sess.Stack(0, true), "c1")
	if first.Tool != second.Tool {
		t.Errorf("Stack(0, true) projected unchanged node %q afresh; want the memo's reuse", first.ID)
	}

	recordCall(state, "shell", "c2", fixed.Add(time.Second))
	stackCallNode(t, sess.Stack(0, true), "c2")
}

// TestStackReusesTheSubagentMemo pins that a drilled subagent's transcript is
// read through its own memo: two reads of the same child, unchanged, hand back
// the node the first read projected.
func TestStackReusesTheSubagentMemo(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	parent := stackState(t, "s1", fixed)
	child := stackState(t, "child", fixed)
	child.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	recordCall(child, "file.read", "c1", fixed)
	view := parent.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: parent})
	first := stackCallNode(t, sess.Stack(view.ID, true), "c1")
	if first.Version == "" {
		t.Fatalf("node %q carries no version; want one the memo may keep", first.ID)
	}
	second := stackCallNode(t, sess.Stack(view.ID, true), "c1")
	if first.Tool != second.Tool {
		t.Errorf("Stack(%d, true) projected unchanged node %q afresh; want the memo's reuse", view.ID, first.ID)
	}
}

// TestStackResetsTheSubagentMemoForAnotherChild pins that the subagent memo
// serves the child read last: reading another child and then the first again
// still returns each child's own tree, as a fresh projection of it builds.
func TestStackResetsTheSubagentMemoForAnotherChild(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	parent := stackState(t, "s1", fixed)
	one := stackState(t, "child-one", fixed)
	one.AddMessage(session.RoleUser, "one says hi", session.ContentTypePlain)
	two := stackState(t, "child-two", fixed)
	two.AddMessage(session.RoleUser, "two says hi", session.ContentTypePlain)
	first := parent.RegisterSubagent("scout", one)
	second := parent.RegisterSubagent("scout", two)

	_, sess := New(Deps{State: parent})
	readsChild := func(id int64, src *session.State) {
		t.Helper()
		got := sess.Stack(id, false)
		if got.SubagentID != id {
			t.Errorf("SubagentID = %d, want %d", got.SubagentID, id)
		}
		want := viewmodel.ProjectState(src, false, src.Now()).Tree
		if !reflect.DeepEqual(got.Roots, want.Roots) {
			t.Errorf("Stack(%d) Roots =\n%v\nwant\n%v", id, got.Roots, want.Roots)
		}
		if !reflect.DeepEqual(got.Nodes, want.Nodes) {
			t.Errorf("Stack(%d) Nodes =\n%+v\nwant\n%+v", id, got.Nodes, want.Nodes)
		}
	}
	readsChild(first.ID, one)
	readsChild(second.ID, two)
	readsChild(first.ID, one)
}

// TestReleaseSubagentDropsItsMemo pins that leaving a drill-in forgets the
// subagent's projection.
func TestReleaseSubagentDropsItsMemo(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	child := stackState(t, "child", fixed)
	child.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	view := state.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: state})
	sess.Stack(view.ID, false)
	sess.stackMu.Lock()
	kept := sess.subMemoID
	sess.stackMu.Unlock()
	if kept != view.ID {
		t.Fatalf("subMemoID = %d, want %d", kept, view.ID)
	}

	if err := sess.ReleaseSubagent(context.Background(), view.ID); err != nil {
		t.Fatalf("ReleaseSubagent(%d) = %v, want nil", view.ID, err)
	}
	sess.stackMu.Lock()
	after := sess.subMemoID
	sess.stackMu.Unlock()
	if after != 0 {
		t.Errorf("subMemoID = %d after the release, want 0", after)
	}
}

// TestReleaseSubagentOfAnotherChildKeepsTheMemo pins that a release names one
// subagent: another child's memo survives it.
func TestReleaseSubagentOfAnotherChildKeepsTheMemo(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	child := stackState(t, "child", fixed)
	child.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	view := state.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: state})
	sess.Stack(view.ID, false)

	if err := sess.ReleaseSubagent(context.Background(), view.ID+1); err != nil {
		t.Fatalf("ReleaseSubagent = %v, want nil", err)
	}
	sess.stackMu.Lock()
	after := sess.subMemoID
	sess.stackMu.Unlock()
	if after != view.ID {
		t.Errorf("subMemoID = %d after releasing another child, want %d", after, view.ID)
	}
}

// TestReleaseSubagentOnAClosedSessionFails pins the closed-session contract
// the other methods share.
func TestReleaseSubagentOnAClosedSessionFails(t *testing.T) {
	state := stackState(t, "s1", time.Unix(1_700_000_000, 0))
	_, sess := New(Deps{State: state})
	sess.close()
	if err := sess.ReleaseSubagent(context.Background(), 1); err != errSessionClosed {
		t.Errorf("ReleaseSubagent on a closed session = %v, want errSessionClosed", err)
	}
}
