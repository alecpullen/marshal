package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// busyStackState builds a session on a frozen clock with one finished turn, so
// the agent's own rule projects it idle and the turn carries a receipt: the
// flag a client flips is then visible as that receipt's absence.
func busyStackState(t *testing.T, id string) *session.State {
	t.Helper()
	fixed := time.Unix(1_700_000_000, 0)
	st := session.New(config.Default(), t.TempDir(), fixed, session.Persistence{SessionID: id},
		session.WithClock(func() time.Time { return fixed }))
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	beat := st.BeginStep(session.Actor{Model: "qwen3-coder"})
	st.AddNarration(beat, "Running the tests.")
	st.EndStep(beat)
	st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypeMarkdown)
	return st
}

// snapshotHasKind reports whether any node of snap has kind.
func snapshotHasKind(snap acpwire.StackSnapshot, kind string) bool {
	for _, n := range snap.Nodes {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

// flipBusy sends session/stack_busy for the test session.
func flipBusy(t *testing.T, a *stackAgent, busy bool) {
	t.Helper()
	if err := a.conn.Call(context.Background(), acpwire.MethodStackBusy,
		acpwire.StackBusyParams{SessionID: stackWireSession, Busy: busy}, nil); err != nil {
		t.Fatalf("session/stack_busy(%v): %v", busy, err)
	}
}

// TestStackBusySetsTheFlagTheStacksAreProjectedWith pins R2 for the flip: the
// patches the handler sends precede its response, so a mirror read straight
// after the call already carries the new flag, with no settle in between.
func TestStackBusySetsTheFlagTheStacksAreProjectedWith(t *testing.T) {
	a := startStackAgent(t, busyStackState(t, stackWireSession))

	first := readStack(t, a, 0)
	if !snapshotHasKind(first, acpwire.KindReceipt) {
		t.Fatalf("the first read has no receipt; want the finished turn's")
	}

	flipBusy(t, a, true)
	if got := readStack(t, a, 0); snapshotHasKind(got, acpwire.KindReceipt) {
		t.Errorf("after stack_busy{true} the mirror still carries a receipt")
	}

	flipBusy(t, a, false)
	if got := readStack(t, a, 0); !snapshotHasKind(got, acpwire.KindReceipt) {
		t.Errorf("after stack_busy{false} the mirror carries no receipt; want the finished turn's")
	}
}

// TestStackBusyReachesAFinishedSubagent pins that a flip clears a settled
// subagent projector: one whose subagent has stopped is skipped from then on,
// so the flag would otherwise never reach its tree.
func TestStackBusyReachesAFinishedSubagent(t *testing.T) {
	st := busyStackState(t, stackWireSession)
	child := busyStackState(t, "child")
	view := st.RegisterSubagent("scout", child)
	st.FinishSubagent(view.ID, "done", nil)

	a := startStackAgent(t, st)
	if got := readStack(t, a, view.ID); !snapshotHasKind(got, acpwire.KindReceipt) {
		t.Fatalf("the child's first read has no receipt; want the finished turn's")
	}

	// Let the idle path settle the projector, as it does once the child stops.
	a.m.flushChildStacks(stackWireSession, st)
	p := a.m.stackFor(stackKey(stackWireSession, view.ID))
	p.mu.Lock()
	final := p.final
	p.mu.Unlock()
	if !final {
		t.Fatalf("the child's projector is not final after the child stopped")
	}

	flipBusy(t, a, true)
	if got := readStack(t, a, view.ID); snapshotHasKind(got, acpwire.KindReceipt) {
		t.Errorf("the flag reached no finished child: the mirror still carries a receipt")
	}
}

// TestTheSameBusyFlagSendsNoPatch pins that only a change re-projects.
func TestTheSameBusyFlagSendsNoPatch(t *testing.T) {
	a := startStackAgent(t, busyStackState(t, stackWireSession))
	readStack(t, a, 0)

	flipBusy(t, a, true)
	settle(t, a.conn)
	_, afterFlip := a.counts()
	if afterFlip == 0 {
		t.Fatalf("the flip sent no patch")
	}

	flipBusy(t, a, true)
	settle(t, a.conn)
	if _, afterSame := a.counts(); afterSame != afterFlip {
		t.Errorf("a repeated flag sent %d patches, want none", afterSame-afterFlip)
	}
}

// TestStackBusyRefusesBadParams pins the two rejections: no session ID is
// invalid params, an unknown session is the unknown-session error.
func TestStackBusyRefusesBadParams(t *testing.T) {
	a := startStackAgent(t, busyStackState(t, stackWireSession))

	if _, err := a.m.StackBusy(context.Background(), json.RawMessage(`{"busy":true}`)); err == nil ||
		err.Error() != "session/stack_busy requires sessionId" {
		t.Errorf("session/stack_busy without a session id = %v, want the requires-sessionId error", err)
	}
	if _, err := a.m.StackBusy(context.Background(), json.RawMessage(`{"sessionId":"nope","busy":true}`)); err == nil ||
		err.Error() != "unknown session: nope" {
		t.Errorf("session/stack_busy for an unknown session = %v, want the unknown-session error", err)
	}
}
