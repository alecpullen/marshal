package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// stackNodeCounter is a session whose StackNode calls are counted, with the
// subagent ID of the last one. Every other method is the wrapped session's.
type stackNodeCounter struct {
	acpclient.Session
	calls        int
	lastSubagent int64
	fail         error // when set, StackNode counts the call and returns it
}

func (s *stackNodeCounter) StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error) {
	s.calls++
	s.lastSubagent = subagentID
	if s.fail != nil {
		return acpwire.StackNodeResult{}, s.fail
	}
	return s.Session.StackNode(ctx, nodeID, subagentID)
}

// runEventModel adds one run event to the model's transcript, refreshes the
// viewport, and returns the wire node ID and version the render drew.
func runEventModel(t *testing.T, m *Model) (string, string) {
	t.Helper()
	stateOf(m).AddRunEvent(session.RunEvent{
		Kind:  session.RunEventVerifyFailed,
		Title: "go test ./...",
		Body:  "verify failed: two tests failed",
	})
	m.refreshViewport()
	for id, w := range m.wire {
		if w.RunEvent == nil {
			continue
		}
		if w.Version == "" {
			t.Fatal("the run event's wire node carries no version")
		}
		return id, w.Version
	}
	t.Fatal("the wire holds no run event")
	return "", ""
}

// A render asks for detail without calling the session; the fetch is issued
// by the update that follows, and its answer is cached for the version drawn.
func TestWantedDetailIsFetchedOffTheRenderPath(t *testing.T) {
	m := newTestModel(t)
	key, version := runEventModel(t, &m)
	counter := &stackNodeCounter{Session: m.sess}
	m.sess = counter

	if d := m.wantDetail(key, version); d != nil {
		t.Fatal("a first want has no detail to draw yet")
	}
	if counter.calls != 0 {
		t.Fatalf("wantDetail issued %d StackNode calls, want none", counter.calls)
	}

	cmd := m.detailQueryCmds()
	if cmd == nil {
		t.Fatal("a recorded want must produce a fetch command")
	}
	if counter.calls != 0 {
		t.Fatalf("building the command issued %d StackNode calls, want none", counter.calls)
	}
	if second := m.detailQueryCmds(); second != nil {
		t.Fatal("a want is fetched once: the second call must have nothing to do")
	}

	msg := cmd()
	if msg == nil {
		t.Fatal("the fetch command produced no message")
	}
	if counter.calls != 1 {
		t.Fatalf("running the command made %d StackNode calls, want 1", counter.calls)
	}
	updated, _ := m.Update(msg)
	m = updated.(Model)

	d := m.wantDetail(key, version)
	if d == nil {
		t.Fatal("the answer must be cached for the version the render drew")
	}
	if d.Body != "verify failed: two tests failed" {
		t.Fatalf("cached detail body = %q, want the run event's whole body", d.Body)
	}
}

// A failed fetch is an answer too: it is kept for its version, so the row is
// not asked again until the node changes.
func TestAFailedDetailIsNotAskedAgain(t *testing.T) {
	m := newTestModel(t)
	key, version := runEventModel(t, &m)
	// The fetch fails while the node stays on screen: its answer is kept for
	// the version, so the row is not asked again until the node changes.
	m.sess = &stackNodeCounter{Session: m.sess, fail: errors.New("detail unavailable")}

	if d := m.wantDetail(key, version); d != nil {
		t.Fatal("a first want has no detail to draw yet")
	}
	cmd := m.detailQueryCmds()
	if cmd == nil {
		t.Fatal("a recorded want must produce a fetch command")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("the fetch command produced no message")
	}
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if d := m.wantDetail(key, version); d != nil {
		t.Fatal("a failed fetch must draw the wire text, not a detail")
	}
	if cmd := m.detailQueryCmds(); cmd != nil {
		t.Fatal("a failed fetch must not be asked again at the same version")
	}
	c, ok := m.details[key]
	if !ok || !c.failed {
		t.Fatalf("the failed entry must survive the refresh that stored it: %+v (present %v)", c, ok)
	}
}

// A node whose version moved is asked again: the old answer described a
// payload the row no longer draws.
func TestANewVersionIsAskedAgain(t *testing.T) {
	m := newTestModel(t)
	key, _ := runEventModel(t, &m)
	m.details = map[string]cachedDetail{key: {version: "a"}}

	if d := m.wantDetail(key, "b"); d != nil {
		t.Fatal("a cached entry for another version is not this version's detail")
	}
	if cmd := m.detailQueryCmds(); cmd == nil {
		t.Fatal("a new version must be asked again")
	}
}

// The update, not the render, sends the fetches a render asked for.
func TestUpdateSendsTheWantedFetches(t *testing.T) {
	m := newTestModel(t)
	key, version := runEventModel(t, &m)
	if d := m.wantDetail(key, version); d != nil {
		t.Fatal("a first want has no detail to draw yet")
	}

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m = drainCmds(m, cmd)

	if _, ok := m.details[key]; !ok {
		t.Fatal("the wanted fetch must be sent by the update and its answer stored")
	}
}

// The stored entry is an input the transcript draws from, so it must bust the
// viewport cache the way callers and notices do.
func TestAStoredDetailRepaintsItsRow(t *testing.T) {
	m := newTestModel(t)
	key, version := runEventModel(t, &m)
	before := m.lastTranscriptHash

	updated, _ := m.handleDetail(detailMsg{
		key:     key,
		version: version,
		res: acpwire.StackNodeResult{
			Node:   acpwire.WireNode{Version: version},
			Detail: acpwire.NodeDetail{Body: "verify failed: two tests failed"},
		},
	})
	if updated.lastTranscriptHash == before {
		t.Fatal("a stored detail must be folded into the content signature, or it repaints nothing")
	}
}

// Detail for a node the transcript dropped (a rollback, a cleared run) must
// not survive with its fetch state.
func TestDetailOfAGoneNodeIsPruned(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, Title: "a3f9e21"})
	m.details = map[string]cachedDetail{"run:gone": {version: "v"}}
	m.detailAsked = map[string]string{"run:gone": "v"}

	m.invalidateTranscript()
	m.refreshViewport()

	if _, still := m.details["run:gone"]; still {
		t.Fatal("detail for a node the wire no longer holds must be pruned")
	}
	if _, still := m.detailAsked["run:gone"]; still {
		t.Fatal("the in-flight version for a node the wire no longer holds must be pruned")
	}
}

// An answer that lands after its node left the tree must not be cached: the
// prune that would drop it only runs on the next content change, so the window
// between has to be closed by the answer itself.
func TestDetailOfAGoneNodeIsNotStored(t *testing.T) {
	m := newTestModel(t)
	// A live wire index, so the guard is exercised against a real one rather
	// than an empty map.
	runEventModel(t, &m)
	if d := m.wantDetail("run:gone", "v"); d != nil {
		t.Fatal("a first want has no detail to draw yet")
	}

	updated, _ := m.handleDetail(detailMsg{
		key:     "run:gone",
		version: "v",
		res: acpwire.StackNodeResult{
			Node:   acpwire.WireNode{Version: "v"},
			Detail: acpwire.NodeDetail{Body: "a body"},
		},
	})

	if _, ok := updated.details["run:gone"]; ok {
		t.Fatal("detail for a node the wire does not hold must not be cached")
	}
	if _, ok := updated.detailAsked["run:gone"]; ok {
		t.Fatal("the in-flight version of a node the wire does not hold must be dropped")
	}
}

// With no session there is nobody to ask, so the wants are dropped rather than
// held to fire against a later session's node IDs.
func TestWantsAreDroppedWithoutASession(t *testing.T) {
	m := newTestModel(t)
	key, version := runEventModel(t, &m)
	if d := m.wantDetail(key, version); d != nil {
		t.Fatal("a first want has no detail to draw yet")
	}

	m.sess = nil
	if cmd := m.detailQueryCmds(); cmd != nil {
		t.Fatal("a want with no session has nothing to send")
	}
	if len(m.detailWant) != 0 {
		t.Fatalf("%d wants survived with no session, want none", len(m.detailWant))
	}
}

// A want remembers which subagent's transcript holds the node, so the answer
// is read from the transcript the render drew.
func TestAWantRemembersItsSubagent(t *testing.T) {
	m := newTestModel(t)
	key, version := runEventModel(t, &m)
	m.viewStack = []acpwire.Subagent{{ID: 7}}
	counter := &stackNodeCounter{Session: m.sess}
	m.sess = counter

	if d := m.wantDetail(key, version); d != nil {
		t.Fatal("a first want has no detail to draw yet")
	}
	cmd := m.detailQueryCmds()
	if cmd == nil {
		t.Fatal("a recorded want must produce a fetch command")
	}
	cmd() // the fetch fails for a subagent with no child; the argument is the point
	if counter.lastSubagent != 7 {
		t.Fatalf("StackNode asked for subagent %d, want 7", counter.lastSubagent)
	}
}
