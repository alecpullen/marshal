package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/session/sessiontest"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// stackWireSession is the session ID these tests serve.
const stackWireSession = "s1"

// The 2,000-call session R10's budgets are measured on.
const (
	largeStackTurns = sessiontest.LargeTurns
	largeStackCalls = sessiontest.LargeCalls
)

// stackAgent is a marshal-capable TurnManager served over a pipe, with a
// client-side mirror bound to the same connection.
type stackAgent struct {
	m      *TurnManager
	mirror *rpc.Stacks
	conn   *rpc.Conn
	st     *session.State

	mu      sync.Mutex
	fetches int
	patches int
}

// startStackAgent serves st through startTestAgent, as the real host serves a
// connection, and returns the mirror the client side reads.
func startStackAgent(t *testing.T, st *session.State) *stackAgent {
	t.Helper()
	a := &stackAgent{st: st, mirror: rpc.NewStacks()}
	client := startTestAgent(t, a.install)
	a.connect(t, client)
	return a
}

// startStackAgentBench is startStackAgent for a benchmark, whose *testing.B
// startTestAgent's *testing.T signature does not accept.
func startStackAgentBench(b *testing.B, st *session.State) *stackAgent {
	b.Helper()
	a := &stackAgent{st: st, mirror: rpc.NewStacks()}
	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	srv.MaxLineBytes = rpc.MaxFrameBytes
	a.install(srv)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	b.Cleanup(func() {
		cancel()
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			b.Error("the server did not stop")
		}
	})
	a.connect(b, client)
	return a
}

// install registers the stack methods on srv and builds the manager they
// serve, exactly as registerHandlers does for a marshal client.
func (a *stackAgent) install(srv *Server) {
	// The runtime carries an event broker, as the real host's does.
	broker := pubsub.NewBroker[session.Event]()
	a.m = NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{State: a.st, Events: broker}, id == stackWireSession
		},
		Notify:  srv.Notify,
		Marshal: func() bool { return true },
	})
	srv.Handle(acpwire.MethodStack, func(ctx context.Context, params json.RawMessage) (any, error) {
		a.mu.Lock()
		a.fetches++
		a.mu.Unlock()
		return a.m.Stack(ctx, params)
	})
	srv.Handle(acpwire.MethodStackUnsubscribe, a.m.StackUnsubscribe)
	srv.Handle(acpwire.MethodStackBusy, a.m.StackBusy)
	srv.Handle("test/ping", func(ctx context.Context, params json.RawMessage) (any, error) {
		return struct{}{}, nil
	})
}

// connect builds the client connection whose notifications feed the mirror.
func (a *stackAgent) connect(tb testing.TB, client rpc.Transport) {
	tb.Helper()
	a.conn = rpc.NewConn(client, rpc.Handlers{Notify: a.notify})
	a.mirror.Bind(a.conn)
	tb.Cleanup(func() { _ = a.conn.Close() })
}

// notify is one Handlers.Notify: it counts stack patches and applies
// everything to the mirror, as a client's handler chain would.
func (a *stackAgent) notify(method string, params json.RawMessage) {
	if method == acpwire.MethodSessionUpdate {
		var update acpwire.SessionUpdate
		if err := json.Unmarshal(params, &update); err == nil {
			var kind struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(update.Update, &kind); err == nil && kind.Kind == acpwire.UpdateStackPatch {
				a.mu.Lock()
				a.patches++
				a.mu.Unlock()
			}
		}
	}
	a.mirror.Notify(method, params)
}

// counts reports the session/stack fetches served and the stack patches sent.
func (a *stackAgent) counts() (fetches, patches int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fetches, a.patches
}

// settle waits until every frame the agent sent before the call is applied:
// the connection notifies on its read goroutine, so the ping's response cannot
// overtake the patches that preceded it.
func settle(t *testing.T, conn *rpc.Conn) {
	t.Helper()
	if err := conn.Call(context.Background(), "test/ping", nil, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
}

// readStack reads one stack through the mirror, subscribing the connection on
// a first read.
func readStack(tb testing.TB, a *stackAgent, subagentID int64) acpwire.StackSnapshot {
	tb.Helper()
	snap, err := a.mirror.Stack(context.Background(), stackWireSession, subagentID)
	if err != nil {
		tb.Fatalf("mirror read: %v", err)
	}
	return snap
}

// sameAsProjector checks that the mirror holds exactly what the projector last
// sent: the same nodes, node for node and in the projector's order, with the
// same roots and revision.
func sameAsProjector(t *testing.T, a *stackAgent, subagentID int64, st *session.State, busy bool) {
	t.Helper()
	p := a.m.stackFor(stackKey(stackWireSession, subagentID))
	p.mu.Lock()
	rev := p.rev
	sent := make(map[string][]byte, len(p.sent))
	for id, encoded := range p.sent {
		sent[id] = encoded
	}
	order := append([]string(nil), p.sentOrder...)
	roots := append([]string(nil), p.sentRoots...)
	p.mu.Unlock()

	snap := readStack(t, a, subagentID)
	if snap.Rev != rev {
		t.Fatalf("mirror rev = %d, projector rev = %d", snap.Rev, rev)
	}
	if len(snap.Nodes) != len(order) {
		t.Fatalf("mirror has %d nodes, projector sent %d", len(snap.Nodes), len(order))
	}
	for i, id := range order {
		if snap.Nodes[i].ID != id {
			t.Fatalf("node %d = %q, projector order has %q", i, snap.Nodes[i].ID, id)
		}
		got, err := json.Marshal(snap.Nodes[i])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, sent[id]) {
			t.Fatalf("node %q = %s, projector sent %s", id, got, sent[id])
		}
	}
	if len(snap.Roots) != len(roots) {
		t.Fatalf("mirror roots = %v, projector sent %v", snap.Roots, roots)
	}
	for i, root := range roots {
		if snap.Roots[i] != root {
			t.Fatalf("mirror roots = %v, projector sent %v", snap.Roots, roots)
		}
	}
}

// flushAndCheck flushes, waits for the patches to land, and compares the mirror
// with the projector.
func flushAndCheck(t *testing.T, a *stackAgent, st *session.State, busy bool) {
	t.Helper()
	a.m.flushStack(stackWireSession, st, busy)
	settle(t, a.conn)
	sameAsProjector(t, a, 0, st, busy)
}

func TestStackMirrorConvergesThroughATurn(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := startStackAgent(t, st)

	st.AddMessage(session.RoleUser, "first prompt", session.ContentTypePlain)
	readStack(t, a, 0)
	sameAsProjector(t, a, 0, st, true)

	step := st.BeginStep(session.Actor{})
	flushAndCheck(t, a, st, true)

	for _, call := range []string{"c1", "c2", "c3"} {
		st.LogToolCall(registry.AuditEvent{
			ToolName:      "shell.run",
			StepID:        step,
			ToolCallID:    call,
			Args:          json.RawMessage(`{"command":"echo ` + call + `"}`),
			ResultContent: "ok",
		})
	}
	flushAndCheck(t, a, st, true)

	st.AddNarration(step, "three calls done")
	flushAndCheck(t, a, st, true)

	st.EndStep(step)
	flushAndCheck(t, a, st, false)

	st.AddMessageFinal(session.RoleAssistant, "all done", session.ContentTypePlain)
	flushAndCheck(t, a, st, false)

	st.AddMessage(session.RoleUser, "second prompt", session.ContentTypePlain)
	flushAndCheck(t, a, st, true)

	// The dirty flush and its band wake carry the same tree.
	p := a.m.stackFor(stackKey(stackWireSession, 0))
	p.mu.Lock()
	p.wakeAt = time.Now().Add(-time.Second)
	p.mu.Unlock()
	a.m.markStackDirty(stackWireSession)
	st.AddNarration(step, "a late beat")
	a.m.flushDirtyStack(stackWireSession, st)
	settle(t, a.conn)
	sameAsProjector(t, a, 0, st, true)
}

// freshAndCheck flushes, waits for the patches to land, and compares the mirror
// node for node with a fresh projection of the state: not with what the
// projector says it sent, which a stale memo would keep agreeing with.
func freshAndCheck(t *testing.T, a *stackAgent, st *session.State, busy bool) {
	t.Helper()
	a.m.flushStack(stackWireSession, st, busy)
	settle(t, a.conn)
	fresh := viewmodel.ProjectState(st, busy, time.Now()).Tree
	snap := readStack(t, a, 0)
	byID := make(map[string]acpwire.WireNode, len(fresh.Nodes))
	for _, n := range fresh.Nodes {
		byID[n.ID] = n
	}
	if len(snap.Nodes) != len(fresh.Nodes) {
		t.Fatalf("mirror has %d nodes, a fresh projection has %d", len(snap.Nodes), len(fresh.Nodes))
	}
	for _, n := range snap.Nodes {
		if n.ID == acpwire.BandID {
			continue // the band is not the memo's and its stale age moves
		}
		f, ok := byID[n.ID]
		if !ok {
			t.Fatalf("mirror holds node %q, which a fresh projection does not", n.ID)
		}
		got, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("node %q = %s, a fresh projection has %s", n.ID, got, want)
		}
	}
}

// TestFlushedPatchesMatchAFreshProjection walks the same turn as
// TestStackMirrorConvergesThroughATurn and compares what the client holds with
// a fresh projection at every step, so a memo that kept a stale encoding
// cannot hide behind the projector's own record of what it sent.
func TestFlushedPatchesMatchAFreshProjection(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := startStackAgent(t, st)

	st.AddMessage(session.RoleUser, "first prompt", session.ContentTypePlain)
	readStack(t, a, 0)
	freshAndCheck(t, a, st, true)

	step := st.BeginStep(session.Actor{})
	freshAndCheck(t, a, st, true)

	for _, call := range []string{"c1", "c2", "c3"} {
		st.LogToolCall(registry.AuditEvent{
			ToolName:      "shell.run",
			StepID:        step,
			ToolCallID:    call,
			Args:          json.RawMessage(`{"command":"echo ` + call + `"}`),
			ResultContent: "ok",
		})
	}
	freshAndCheck(t, a, st, true)

	st.AddNarration(step, "three calls done")
	freshAndCheck(t, a, st, true)

	st.EndStep(step)
	freshAndCheck(t, a, st, false)

	st.AddMessageFinal(session.RoleAssistant, "all done", session.ContentTypePlain)
	freshAndCheck(t, a, st, false)

	st.AddMessage(session.RoleUser, "second prompt", session.ContentTypePlain)
	freshAndCheck(t, a, st, true)
}

func TestStackMirrorRecoversFromALostPatch(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := startStackAgent(t, st)

	st.AddMessage(session.RoleUser, "first prompt", session.ContentTypePlain)
	readStack(t, a, 0)
	sameAsProjector(t, a, 0, st, true)

	// A flush the client never hears: the projector advances without notifying.
	st.AddMessage(session.RoleUser, "silent", session.ContentTypePlain)
	p := a.m.stackFor(stackKey(stackWireSession, 0))
	p.mu.Lock()
	if _, changed := p.diff(p.memo.ProjectState(st, false, time.Now()).Tree); !changed {
		p.mu.Unlock()
		t.Fatal("the lost change produced no patch")
	}
	p.mu.Unlock()

	// The next flush is a gap for the mirror, which refetches.
	st.AddMessage(session.RoleUser, "after the gap", session.ContentTypePlain)
	a.m.flushStack(stackWireSession, st, false)
	settle(t, a.conn)

	waitFor(t, "the mirror to refetch after a gap", func() bool {
		p.mu.Lock()
		rev := p.rev
		p.mu.Unlock()
		snap, err := a.mirror.Stack(context.Background(), stackWireSession, 0)
		return err == nil && snap.Rev == rev
	})
	sameAsProjector(t, a, 0, st, false)
}

func TestStackMirrorFollowsASubagent(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := startStackAgent(t, st)
	st.AddMessage(session.RoleUser, "parent", session.ContentTypePlain)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	child.AddMessage(session.RoleUser, "child prompt", session.ContentTypePlain)
	v := st.RegisterSubagent("worker", child)

	readStack(t, a, v.ID)
	child.AddMessage(session.RoleUser, "child second", session.ContentTypePlain)
	a.m.flushChildStacks(stackWireSession, st)
	settle(t, a.conn)
	sameAsProjector(t, a, v.ID, child, false)

	if err := a.mirror.Release(context.Background(), stackWireSession, v.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	_, patchesBefore := a.counts()
	fetchesBefore, _ := a.counts()

	child.AddMessage(session.RoleUser, "child third", session.ContentTypePlain)
	a.m.flushChildStacks(stackWireSession, st)
	settle(t, a.conn)
	if _, patches := a.counts(); patches != patchesBefore {
		t.Fatalf("released subagent patches = %d, want %d", patches, patchesBefore)
	}

	// The mirror forgot the stack, so the next read fetches it again.
	readStack(t, a, v.ID)
	if fetches, _ := a.counts(); fetches != fetchesBefore+1 {
		t.Fatalf("fetches after release = %d, want %d", fetches, fetchesBefore+1)
	}
}

// largeStackSession builds the 2,000-call session: 200 turns, each a prompt,
// one step with ten shell.run calls, and a final reply.
func largeStackSession(tb testing.TB) *session.State {
	tb.Helper()
	return sessiontest.LargeSession(tb)
}

// logLargeStackShape records the fixture's size once, for the implementation
// record (P3 R10).
func logLargeStackShape(tb testing.TB, st *session.State) {
	tb.Helper()
	tree := viewmodel.ProjectState(st, false, time.Now()).Tree
	snap := acpwire.StackSnapshot{
		SessionID: stackWireSession,
		Rev:       1,
		Roots:     tree.Roots,
		Nodes:     tree.Nodes,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Logf("large session: %d calls, %d nodes, snapshot %d bytes",
		largeStackTurns*largeStackCalls, len(tree.Nodes), len(data))
}

// tenToolNodes returns ten tool nodes of snap, the nodes a ten-node patch
// carries.
func tenToolNodes(tb testing.TB, snap acpwire.StackSnapshot) []acpwire.WireNode {
	tb.Helper()
	nodes := make([]acpwire.WireNode, 0, 10)
	for i := len(snap.Nodes) - 1; i >= 0 && len(nodes) < 10; i-- {
		if snap.Nodes[i].Kind == acpwire.KindTool {
			nodes = append(nodes, snap.Nodes[i])
		}
	}
	if len(nodes) < 10 {
		tb.Fatalf("snapshot has %d tool nodes, want 10", len(nodes))
	}
	return nodes
}

// stackPatchFrame is one session/update stack_patch frame, as the agent sends
// it: the envelope with the update left raw.
func stackPatchFrame(tb testing.TB, rev, baseRev uint64, roots []string, nodes []acpwire.WireNode) json.RawMessage {
	tb.Helper()
	update, err := json.Marshal(acpwire.StackPatch{
		Kind:    acpwire.UpdateStackPatch,
		Rev:     rev,
		BaseRev: baseRev,
		Roots:   roots,
		Upsert:  nodes,
	})
	if err != nil {
		tb.Fatal(err)
	}
	frame, err := json.Marshal(acpwire.SessionUpdate{SessionID: stackWireSession, Update: update})
	if err != nil {
		tb.Fatal(err)
	}
	return frame
}

// medianDuration is the median of the runs, which R10's budgets are stated
// against.
func medianDuration(runs []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// BenchmarkStackPatchApply measures applying a ten-node patch through the
// mirror, decoding included, with each iteration chaining the revision.
func BenchmarkStackPatchApply(b *testing.B) {
	st := largeStackSession(b)
	logLargeStackShape(b, st)
	a := startStackAgentBench(b, st)
	snap := readStack(b, a, 0)
	nodes := tenToolNodes(b, snap)

	// Build the frames first: the budget is the apply, not the encode.
	frames := make([]json.RawMessage, b.N)
	rev := snap.Rev
	for i := range frames {
		frames[i] = stackPatchFrame(b, rev+1, rev, snap.Roots, nodes)
		rev++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.mirror.Notify(acpwire.MethodSessionUpdate, frames[i])
	}
}

// BenchmarkStackRoundTrip measures a fresh mirror's read of the large session,
// from the request to the installed snapshot, over the pipe.
func BenchmarkStackRoundTrip(b *testing.B) {
	withoutMemoCheck(b)
	st := largeStackSession(b)
	logLargeStackShape(b, st)
	a := startStackAgentBench(b, st)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mirror := rpc.NewStacks()
		mirror.Bind(a.conn)
		if _, err := mirror.Stack(context.Background(), stackWireSession, 0); err != nil {
			b.Fatalf("mirror read: %v", err)
		}
	}
}

// BenchmarkStackFlushLargeSession measures one flush of the large session after
// one new tool call, on the agent alone. It has no budget; it decides whether
// R4's interval is affordable (plan 4b).
func BenchmarkStackFlushLargeSession(b *testing.B) {
	withoutMemoCheck(b)
	st := largeStackSession(b)
	logLargeStackShape(b, st)
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{State: st}, id == stackWireSession
		},
		Notify:  func(string, any) error { return nil },
		Marshal: func() bool { return true },
	})
	if _, err := m.Stack(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		b.Fatal(err)
	}
	step := st.BeginStep(session.Actor{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st.LogToolCall(registry.AuditEvent{
			ToolName:      "shell.run",
			StepID:        step,
			ToolCallID:    fmt.Sprintf("bench-%d", i),
			Args:          json.RawMessage(`{"command":"echo bench"}`),
			ResultContent: "ok",
		})
		b.StartTimer()
		m.flushStack(stackWireSession, st, true)
	}
}

// addOneCall adds one tool call to a large session's step with the timer
// stopped, so a benchmark measures the projection and not the fixture.
func addOneCall(b *testing.B, st *session.State, step session.StepID, i int) {
	b.Helper()
	b.StopTimer()
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		StepID:        step,
		ToolCallID:    fmt.Sprintf("bench-%d", i),
		Args:          json.RawMessage(`{"command":"echo bench"}`),
		ResultContent: "ok",
	})
	b.StartTimer()
}

// BenchmarkProjectStateLargeSession measures the package-level projection the
// TUI's in-process Stack made before this plan: every node built and projected
// afresh on each call (P3 plan 4c).
func BenchmarkProjectStateLargeSession(b *testing.B) {
	withoutMemoCheck(b)
	st := largeStackSession(b)
	logLargeStackShape(b, st)
	step := st.BeginStep(session.Actor{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addOneCall(b, st, step, i)
		_ = viewmodel.ProjectState(st, true, time.Now())
	}
}

// BenchmarkWireMemoLargeSession measures the same projection through one memo,
// warmed by a projection before the timer starts, so the reuse of every
// unchanged node is what the loop spends nothing on (P3 plan 4c).
func BenchmarkWireMemoLargeSession(b *testing.B) {
	withoutMemoCheck(b)
	st := largeStackSession(b)
	logLargeStackShape(b, st)
	var m viewmodel.WireMemo
	_ = m.ProjectState(st, true, time.Now())
	step := st.BeginStep(session.Actor{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addOneCall(b, st, step, i)
		_ = m.ProjectState(st, true, time.Now())
	}
}

// TestStackPatchApplyBudget holds R10's 2ms budget for applying a ten-node
// patch, decoding included (P3 plan 4a).
func TestStackPatchApplyBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	a := startStackAgent(t, st)
	snap := readStack(t, a, 0)
	nodes := tenToolNodes(t, snap)

	const runs = 20
	frames := make([]json.RawMessage, runs)
	rev := snap.Rev
	for i := range frames {
		frames[i] = stackPatchFrame(t, rev+1, rev, snap.Roots, nodes)
		rev++
	}

	durations := make([]time.Duration, 0, runs)
	for _, frame := range frames {
		start := time.Now()
		a.mirror.Notify(acpwire.MethodSessionUpdate, frame)
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	t.Logf("patch apply: median %v over %d runs (budget 2ms)", median, runs)
	if median >= 2*time.Millisecond {
		t.Fatalf("patch apply median = %v, budget 2ms (R10): do not loosen it", median)
	}
}

// TestStackRoundTripBudget holds R10's 50ms budget for a full session/stack
// round trip to an applied mirror over the pipe (P3 plan 4a). The first read
// activates the projector and encodes every node once, as every read did
// before the memo (P3 plan 4c), so it is budgeted on its own: the median over
// the rest would let a pathological first read through on 29 warm samples.
func TestStackRoundTripBudget(t *testing.T) {
	withoutMemoCheck(t)
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	a := startStackAgent(t, st)

	const runs = 30
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		mirror := rpc.NewStacks()
		mirror.Bind(a.conn)
		start := time.Now()
		if _, err := mirror.Stack(context.Background(), stackWireSession, 0); err != nil {
			t.Fatalf("mirror read: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	cold := durations[0]
	t.Logf("round trip: median %v over %d runs, first read %v (budget 50ms)", median, runs, cold)
	if median >= 50*time.Millisecond {
		t.Fatalf("round trip median = %v, budget 50ms (R10): do not loosen it", median)
	}
	if cold >= 50*time.Millisecond {
		t.Fatalf("first read, projector activation and every node's first encode = %v, budget 50ms (R10): the memo must not hide it", cold)
	}
}
