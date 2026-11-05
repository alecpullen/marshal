package rpc

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"marshal/internal/acpwire"
)

// startStacks creates a mirror bound to a fresh piped connection whose
// notifications it handles.
func startStacks(t *testing.T) (*Stacks, *fake) {
	t.Helper()
	stacks := NewStacks()
	f := start(t, Handlers{Notify: stacks.Notify})
	stacks.Bind(f.conn)
	return stacks, f
}

// stackRead is one Stack read's outcome, delivered by readStack.
type stackRead struct {
	snap acpwire.StackSnapshot
	err  error
}

// readStack starts one Stack read on its own goroutine.
func readStack(s *Stacks, sessionID string, subagentID int64) <-chan stackRead {
	out := make(chan stackRead, 1)
	go func() {
		snap, err := s.Stack(context.Background(), sessionID, subagentID)
		out <- stackRead{snap: snap, err: err}
	}()
	return out
}

// patchFrame builds the session/update notification carrying one stack_patch.
func patchFrame(t *testing.T, sessionID string, patch acpwire.StackPatch) string {
	t.Helper()
	patch.Kind = acpwire.UpdateStackPatch
	return updateFrame(t, sessionID, patch)
}

// updateFrame builds the session/update notification carrying one update.
func updateFrame(t *testing.T, sessionID string, update any) string {
	t.Helper()
	body, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  acpwire.MethodSessionUpdate,
		"params":  acpwire.SessionUpdate{SessionID: sessionID, Update: body},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	return string(frame)
}

// resultFrame builds a successful response to the request with the given raw
// ID.
func resultFrame(t *testing.T, id string, result any) string {
	t.Helper()
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return `{"jsonrpc":"2.0","id":` + id + `,"result":` + string(body) + `}`
}

// turnSnapshot is one turn root holding the given step children.
func turnSnapshot(sessionID string, subagentID int64, rev uint64, children ...string) acpwire.StackSnapshot {
	nodes := []acpwire.WireNode{{ID: "turn:1", Kind: acpwire.KindTurn, Children: children}}
	for _, child := range children {
		nodes = append(nodes, acpwire.WireNode{ID: child, Kind: acpwire.KindStep, Parent: "turn:1"})
	}
	return acpwire.StackSnapshot{
		SessionID:  sessionID,
		SubagentID: subagentID,
		Rev:        rev,
		Roots:      []string{"turn:1"},
		Nodes:      nodes,
	}
}

// pingConn round-trips one call, so the read goroutine has dispatched every
// frame the agent wrote before it. It fails when the client's next frame is
// not the ping, which catches a read that went back to the agent.
func pingConn(t *testing.T, f *fake) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- f.conn.Call(context.Background(), "test/ping", nil, nil) }()
	req := f.recv(t)
	if got := rawString(t, req["method"]); got != "test/ping" {
		t.Fatalf("client's next frame = %q, want test/ping", got)
	}
	f.send(t, resultFrame(t, rawString(t, req["id"]), struct{}{}))
	if err := <-errCh; err != nil {
		t.Fatalf("ping call: %v", err)
	}
}

// readOnce performs one read that the agent answers with snap and returns it,
// leaving the session's Changed channel drained.
func readOnce(t *testing.T, f *fake, s *Stacks, sessionID string, subagentID int64, snap acpwire.StackSnapshot) acpwire.StackSnapshot {
	t.Helper()
	reads := readStack(s, sessionID, subagentID)
	req := f.recv(t)
	if got := rawString(t, req["method"]); got != acpwire.MethodStack {
		t.Fatalf("first read method = %q, want %q", got, acpwire.MethodStack)
	}
	var params acpwire.StackParams
	if err := json.Unmarshal(req["params"], &params); err != nil {
		t.Fatalf("decode stack params: %v", err)
	}
	if params.SessionID != sessionID || params.SubagentID != subagentID {
		t.Fatalf("stack params = %+v, want session %q and subagent %d", params, sessionID, subagentID)
	}
	f.send(t, resultFrame(t, rawString(t, req["id"]), snap))
	read := <-reads
	if read.err != nil {
		t.Fatalf("read stack: %v", read.err)
	}
	drainChanged(t, s.Changed(sessionID))
	return read.snap
}

// waitStack polls the mirror, without I/O, until the stack reaches rev.
func waitStack(t *testing.T, s *Stacks, sessionID string, subagentID int64, rev uint64) acpwire.StackSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		snap, err := s.Stack(context.Background(), sessionID, subagentID)
		if err != nil {
			t.Fatalf("read stack: %v", err)
		}
		if snap.Rev == rev {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("mirror stopped at revision %d, want %d", snap.Rev, rev)
		}
		time.Sleep(time.Millisecond)
	}
}

// drainChanged drops every pending Changed signal.
func drainChanged(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	for {
		select {
		case <-changed:
		default:
			return
		}
	}
}

// wantChanged waits for one Changed signal.
func wantChanged(t *testing.T, changed <-chan struct{}) {
	t.Helper()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("no Changed signal within a second")
	}
}

// checkSnapshot verifies a read's revision, roots and node IDs in order.
func checkSnapshot(t *testing.T, snap acpwire.StackSnapshot, rev uint64, roots, ids []string) {
	t.Helper()
	if snap.Rev != rev {
		t.Fatalf("snapshot revision = %d, want %d", snap.Rev, rev)
	}
	if !slices.Equal(snap.Roots, roots) {
		t.Fatalf("snapshot roots = %v, want %v", snap.Roots, roots)
	}
	if got := nodeIDs(snap.Nodes); !slices.Equal(got, ids) {
		t.Fatalf("snapshot node order = %v, want %v", got, ids)
	}
}

// nodeIDs is the IDs of nodes, in their order.
func nodeIDs(nodes []acpwire.WireNode) []string {
	out := make([]string, len(nodes))
	for i, node := range nodes {
		out[i] = node.ID
	}
	return out
}

// TestStacksFirstReadFetchesThenServesFromTheMirror checks that the first read
// sends one session/stack and returns its snapshot, and that a second read
// sends nothing.
func TestStacksFirstReadFetchesThenServesFromTheMirror(t *testing.T) {
	stacks, f := startStacks(t)

	reads := readStack(stacks, "s1", 0)
	req := f.recv(t)
	if got := rawString(t, req["method"]); got != acpwire.MethodStack {
		t.Fatalf("first read method = %q, want %q", got, acpwire.MethodStack)
	}
	var params acpwire.StackParams
	if err := json.Unmarshal(req["params"], &params); err != nil {
		t.Fatalf("decode stack params: %v", err)
	}
	if params.SessionID != "s1" || params.SubagentID != 0 {
		t.Fatalf("stack params = %+v, want session s1 and subagent 0", params)
	}
	f.send(t, resultFrame(t, rawString(t, req["id"]), turnSnapshot("s1", 0, 3, "step:1")))

	read := <-reads
	if read.err != nil {
		t.Fatalf("first read: %v", read.err)
	}
	checkSnapshot(t, read.snap, 3, []string{"turn:1"}, []string{"turn:1", "step:1"})

	// The second read is served from the mirror, so the client's next frame is
	// the ping's, not another session/stack.
	second, err := stacks.Stack(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	checkSnapshot(t, second, 3, []string{"turn:1"}, []string{"turn:1", "step:1"})
	pingConn(t, f)
}

// TestStacksApplyChainedPatches checks that patches whose base is the mirror's
// revision apply in order and move the revision on.
func TestStacksApplyChainedPatches(t *testing.T) {
	stacks, f := startStacks(t)
	first := readOnce(t, f, stacks, "s1", 0, turnSnapshot("s1", 0, 1, "step:1"))
	checkSnapshot(t, first, 1, []string{"turn:1"}, []string{"turn:1", "step:1"})

	changed := stacks.Changed("s1")
	drainChanged(t, changed)

	// 1→2 upserts the turn with a new child and adds that child.
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     2,
		BaseRev: 1,
		Roots:   []string{"turn:1"},
		Upsert: []acpwire.WireNode{
			{ID: "turn:1", Kind: acpwire.KindTurn, Children: []string{"step:1", "step:2"}},
			{ID: "step:2", Kind: acpwire.KindStep, Parent: "turn:1"},
		},
	}))
	// 2→3 removes it again.
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     3,
		BaseRev: 2,
		Roots:   []string{"turn:1"},
		Remove:  []string{"step:2"},
	}))

	got := waitStack(t, stacks, "s1", 0, 3)
	checkSnapshot(t, got, 3, []string{"turn:1"}, []string{"turn:1", "step:1"})
	wantChanged(t, changed)
}

// TestStacksDropDuplicatePatches checks that a patch at or below the mirror's
// revision changes nothing.
func TestStacksDropDuplicatePatches(t *testing.T) {
	stacks, f := startStacks(t)
	readOnce(t, f, stacks, "s1", 0, turnSnapshot("s1", 0, 2, "step:1"))

	changed := stacks.Changed("s1")
	drainChanged(t, changed)

	// A repeat of the patch that produced revision 2, and one from before it.
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     2,
		BaseRev: 1,
		Roots:   []string{"turn:1"},
		Upsert:  []acpwire.WireNode{{ID: "step:9", Kind: acpwire.KindStep, Parent: "turn:1"}},
	}))
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     1,
		BaseRev: 0,
		Roots:   []string{"stale"},
		Upsert:  []acpwire.WireNode{{ID: "step:8", Kind: acpwire.KindStep}},
	}))
	// Neither patch starts a fetch or reaches the agent.
	pingConn(t, f)

	got := waitStack(t, stacks, "s1", 0, 2)
	checkSnapshot(t, got, 2, []string{"turn:1"}, []string{"turn:1", "step:1"})
	select {
	case <-changed:
		t.Fatal("a duplicate patch signalled Changed")
	default:
	}
}

// TestStacksRefetchOnAGap checks that a patch the mirror cannot chain starts
// one session/stack fetch, that a later patch is queued during it, and that the
// mirror ends with both changes.
func TestStacksRefetchOnAGap(t *testing.T) {
	stacks, f := startStacks(t)
	readOnce(t, f, stacks, "s1", 0, turnSnapshot("s1", 0, 2, "step:1"))

	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     6,
		BaseRev: 5,
		Roots:   []string{"turn:1"},
		Upsert:  []acpwire.WireNode{{ID: "step:5", Kind: acpwire.KindStep, Parent: "turn:1"}},
	}))

	req := f.recv(t)
	if got := rawString(t, req["method"]); got != acpwire.MethodStack {
		t.Fatalf("gap fetch method = %q, want %q", got, acpwire.MethodStack)
	}
	// A second patch arrives while the fetch is in flight and is queued.
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     10,
		BaseRev: 9,
		Roots:   []string{"turn:1"},
		Upsert:  []acpwire.WireNode{{ID: "step:9", Kind: acpwire.KindStep, Parent: "turn:1"}},
	}))
	// The agent answers with revision 9, which already carries the 5→6 change.
	f.send(t, resultFrame(t, rawString(t, req["id"]), turnSnapshot("s1", 0, 9, "step:1", "step:5")))

	got := waitStack(t, stacks, "s1", 0, 10)
	checkSnapshot(t, got, 10, []string{"turn:1"}, []string{"turn:1", "step:1", "step:5", "step:9"})
	// The queued patch chained, so no second fetch went out.
	pingConn(t, f)
}

// TestStacksQueueDuringTheFirstFetch checks that a patch arriving before the
// first read is answered is queued, and that the read still returns what the
// fetch returned.
func TestStacksQueueDuringTheFirstFetch(t *testing.T) {
	stacks, f := startStacks(t)

	reads := readStack(stacks, "s1", 0)
	req := f.recv(t)
	if got := rawString(t, req["method"]); got != acpwire.MethodStack {
		t.Fatalf("first read method = %q, want %q", got, acpwire.MethodStack)
	}
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     5,
		BaseRev: 4,
		Roots:   []string{"turn:1"},
		Upsert:  []acpwire.WireNode{{ID: "step:4", Kind: acpwire.KindStep, Parent: "turn:1"}},
	}))
	f.send(t, resultFrame(t, rawString(t, req["id"]), turnSnapshot("s1", 0, 4, "step:1")))

	read := <-reads
	if read.err != nil {
		t.Fatalf("first read: %v", read.err)
	}
	// The read answers with the fetched snapshot...
	checkSnapshot(t, read.snap, 4, []string{"turn:1"}, []string{"turn:1", "step:1"})
	// ...and the queued patch has moved the mirror on.
	got := waitStack(t, stacks, "s1", 0, 5)
	checkSnapshot(t, got, 5, []string{"turn:1"}, []string{"turn:1", "step:1", "step:4"})
}

// TestStacksReadOrderFollowsRootsAndChildren checks that a read lists the tree
// depth-first from its roots, then every node the walk did not reach.
func TestStacksReadOrderFollowsRootsAndChildren(t *testing.T) {
	stacks, f := startStacks(t)
	readOnce(t, f, stacks, "s1", 0, acpwire.StackSnapshot{
		SessionID: "s1",
		Rev:       1,
		Roots:     []string{"turn:1"},
		Nodes: []acpwire.WireNode{
			{ID: "turn:1", Kind: acpwire.KindTurn, Children: []string{"step:1", "step:3"}},
			{ID: "step:1", Kind: acpwire.KindStep, Parent: "turn:1"},
			{ID: "step:3", Kind: acpwire.KindStep, Parent: "turn:1"},
			{ID: acpwire.BandID, Kind: acpwire.KindBand},
		},
	})

	// The new child lands in the middle of the turn's children, after the
	// band in first-arrival order.
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		Rev:     2,
		BaseRev: 1,
		Roots:   []string{"turn:1"},
		Upsert: []acpwire.WireNode{
			{ID: "turn:1", Kind: acpwire.KindTurn, Children: []string{"step:1", "step:2", "step:3"}},
			{ID: "step:2", Kind: acpwire.KindStep, Parent: "turn:1"},
		},
	}))

	got := waitStack(t, stacks, "s1", 0, 2)
	checkSnapshot(t, got, 2, []string{"turn:1"},
		[]string{"turn:1", "step:1", "step:2", "step:3", acpwire.BandID})
}

// TestStacksReleaseUnsubscribes checks that releasing a subagent unsubscribes
// it with its params, drops its entry, and that the main transcript cannot be
// released.
func TestStacksReleaseUnsubscribes(t *testing.T) {
	stacks, f := startStacks(t)
	readOnce(t, f, stacks, "s1", 1, turnSnapshot("s1", 1, 1, "step:1"))

	errCh := make(chan error, 1)
	go func() { errCh <- stacks.Release(context.Background(), "s1", 1) }()

	req := f.recv(t)
	if got := rawString(t, req["method"]); got != acpwire.MethodStackUnsubscribe {
		t.Fatalf("release method = %q, want %q", got, acpwire.MethodStackUnsubscribe)
	}
	var params acpwire.StackParams
	if err := json.Unmarshal(req["params"], &params); err != nil {
		t.Fatalf("decode unsubscribe params: %v", err)
	}
	if params.SessionID != "s1" || params.SubagentID != 1 {
		t.Fatalf("unsubscribe params = %+v, want session s1 and subagent 1", params)
	}
	f.send(t, resultFrame(t, rawString(t, req["id"]), struct{}{}))
	if err := <-errCh; err != nil {
		t.Fatalf("release: %v", err)
	}

	// The entry is gone, so the next read fetches the stack again.
	reads := readStack(stacks, "s1", 1)
	req = f.recv(t)
	if got := rawString(t, req["method"]); got != acpwire.MethodStack {
		t.Fatalf("read after release method = %q, want %q", got, acpwire.MethodStack)
	}
	f.send(t, resultFrame(t, rawString(t, req["id"]), turnSnapshot("s1", 1, 2, "step:1")))
	if read := <-reads; read.err != nil || read.snap.Rev != 2 {
		t.Fatalf("read after release = %+v, %v, want the re-fetched snapshot at revision 2", read.snap, read.err)
	}

	// The main transcript cannot be released.
	if err := stacks.Release(context.Background(), "s1", 0); err == nil ||
		err.Error() != "rpc: the main transcript cannot be released" {
		t.Fatalf("release of subagent 0 = %v, want the pinned error", err)
	}
}

// TestStacksIgnoreOtherUpdates checks that a notification carrying another
// update kind, and a patch for a stack nobody read, change nothing.
func TestStacksIgnoreOtherUpdates(t *testing.T) {
	stacks, f := startStacks(t)
	readOnce(t, f, stacks, "s1", 0, turnSnapshot("s1", 0, 1, "step:1"))

	changed := stacks.Changed("s1")
	drainChanged(t, changed)

	// Another update kind, a patch for a subagent stack nobody read, and one
	// for a session nobody read.
	f.send(t, updateFrame(t, "s1", map[string]any{"kind": "agent_message_chunk", "content": "hello"}))
	f.send(t, patchFrame(t, "s1", acpwire.StackPatch{
		SubagentID: 7,
		Rev:        2,
		BaseRev:    1,
		Roots:      []string{"turn:9"},
		Upsert:     []acpwire.WireNode{{ID: "turn:9", Kind: acpwire.KindTurn}},
	}))
	f.send(t, patchFrame(t, "s9", acpwire.StackPatch{
		Rev:     2,
		BaseRev: 1,
		Roots:   []string{"turn:9"},
		Upsert:  []acpwire.WireNode{{ID: "turn:9", Kind: acpwire.KindTurn}},
	}))
	// The ping proves all three were dispatched; it also fails if any of them
	// started a fetch.
	pingConn(t, f)

	got := waitStack(t, stacks, "s1", 0, 1)
	checkSnapshot(t, got, 1, []string{"turn:1"}, []string{"turn:1", "step:1"})
	select {
	case <-changed:
		t.Fatal("an ignored update signalled Changed")
	default:
	}
}
