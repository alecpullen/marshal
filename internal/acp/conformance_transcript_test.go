package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// TestTranscriptConformance runs every transcript, row-detail, callers and
// sheet case against both arms of the seam: the service itself, and the
// client's mirror of it over a pipe. The same answers from both is V1.
//
// Every case builds its whole state before open, as plan 3b's cases do: the
// pipe arm's first read is what the agent sends then.
func TestTranscriptConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) seam)
	}{
		{name: "the stack is the shared projection", run: stackIsTheSharedProjection},
		{name: "the stack builds with the busy flag it is given", run: stackBuildsWithTheBusyFlagItIsGiven},
		{name: "a subagent's stack reads its transcript", run: stackOfASubagentReadsItsTranscript},
		{name: "an unknown subagent's stack is empty", run: stackOfAnUnknownSubagentIsEmpty},
		{name: "the stack reads the state's clock", run: stackReadsTheStatesClock},
		{name: "a silent change reaches the mirror", run: transcriptChangesReachTheMirror},
		{name: "a call's detail", run: stackNodeReturnsACallsDetail},
		{name: "a subagent's detail", run: stackNodeReadsASubagentsTranscript},
		{name: "an unknown node or subagent fails", run: stackNodeFailsForAnUnknownNodeOrSubagent},
		{name: "the callers ask the finder", run: callersAsksTheFinderForTheResolvedSymbol},
		{name: "no callers without a lookup", run: callersIsNilWithoutALookup},
		{name: "the session's activity", run: sheetCarriesTheSessionsActivity},
		{name: "a subagent's activity", run: sheetReadsASubagentsOwnActivity},
		{name: "a nested subagent", run: sheetFindsANestedSubagent},
		{name: "an unknown subagent reads as the session", run: sheetReadsAnUnknownSubagentAsTheSession},
		{name: "a refresh rebases onto HEAD", run: refreshSheetRebasesOntoHead},
		{name: "a refresh reads the turns, totals and repo counts", run: refreshSheetReadsTurnsTotalsAndRepoCounts},
		{name: "a released subagent reads the same", run: releasedSubagentReadsTheSame},
	}
	for _, arm := range seamArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// TestStackBusyFlipBudget holds R10's 10ms budget for the busy flip over the
// pipe on the 2,000-call session (P3 plan 4b, decision 2).
func TestStackBusyFlipBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	withoutMemoCheck(t)
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	sess := openPipeSeamTB(t, service.Deps{State: st})

	// The first read subscribes the connection and sends the first flag; only
	// the flips are measured.
	sess.Stack(0, false)

	// Prove a flip round-trips before timing them: a flip the agent never
	// received sends no patch, so the mirror would answer byte-identical. (The
	// session has 200 finished turns, so receipts alone prove nothing.)
	idle := sess.Stack(0, false)
	sess.Stack(0, true)
	busy := sess.Stack(0, true)
	if reflect.DeepEqual(idle, busy) {
		t.Fatal("the flip did not change the projection: the mirror saw no patch")
	}
	sess.Stack(0, false)

	const runs = 20
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		sess.Stack(0, i%2 == 0)
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	// Plan 4c measured one flush of this session at 4.3ms; a flip is that
	// flush, its call and the mirror's read.
	t.Logf("busy flip: median %v over %d flips (budget 10ms; plan 4c's flush: 4.3ms)", median, runs)
	if median >= 10*time.Millisecond {
		t.Fatalf("busy flip median = %v, budget 10ms (R10): do not loosen it", median)
	}
}

// stackState builds a state the way the service's stack tests do, on a frozen
// clock, so both arms project the same tree.
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

// sameJSON compares two values by their JSON encodings, as plan 4a's mirror
// tests do: the wire does not keep a nil slice apart from an empty one, and
// nothing renders the difference.
func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal %s: %v", what, err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want %s: %v", what, err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("%s =\n%s\nwant\n%s", what, gotJSON, wantJSON)
	}
}

// stackIsTheSharedProjection pins that Stack serves the projection the TUI
// renders, at the state's clock: the same roots and nodes
// viewmodel.ProjectState builds, band node last.
func stackIsTheSharedProjection(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
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

	sess := open(t, service.Deps{State: state})
	got := sess.Stack(0, false)

	if got.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "s1")
	}
	if got.SubagentID != 0 {
		t.Errorf("SubagentID = %d, want 0", got.SubagentID)
	}
	want := viewmodel.ProjectState(state, false, state.Now()).Tree
	sameJSON(t, "Roots", got.Roots, want.Roots)
	sameJSON(t, "Nodes", got.Nodes, want.Nodes)
	if len(got.Nodes) == 0 || got.Nodes[len(got.Nodes)-1].Kind != acpwire.KindBand {
		t.Errorf("last node = %+v, want the band", got.Nodes)
	}
}

// transcriptChangesReachTheMirror pins R4 for the eight state calls that
// change what the projection reads and publish no update of their own: each
// publishes transcript_changed, so the agent re-projects and the client's next
// read shows the change (P3 plan 8).
//
// Each row reads once, makes its change directly on the state, proves the
// change moved the projection (a row whose change was invisible could pass
// without any push), then makes a call that changes nothing and reads again.
func transcriptChangesReachTheMirror(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fixed := time.Unix(1_700_000_000, 0)
	root := t.TempDir()
	const elsewhere = "/elsewhere-wt"

	// build makes two turns, one shell call inside the second root: moving the
	// workspace elides its "cd" prefix, so the projection moves with it.
	build := func(t *testing.T) (*session.State, func(string) int64) {
		t.Helper()
		st := stackState(t, "s1", fixed)
		st.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: root})
		st.AddMessage(session.RoleUser, "turn one", session.ContentTypePlain)
		beat := st.BeginStep(session.Actor{Model: "qwen3-coder"})
		st.LogToolCall(registry.AuditEvent{
			Timestamp: fixed, ToolName: "shell.run", StepID: beat, ToolCallID: "c1",
			Args:          json.RawMessage(`{"command":"cd ` + elsewhere + ` && go test ./..."}`),
			ResultSummary: "ok",
		})
		st.EndStep(beat)
		st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypeMarkdown)
		st.AddMessage(session.RoleUser, "turn two", session.ContentTypePlain)
		beat2 := st.BeginStep(session.Actor{Model: "qwen3-coder"})
		st.AddNarration(beat2, "Thinking it over.")
		st.EndStep(beat2)
		st.AddMessageFinal(session.RoleAssistant, "done again", session.ContentTypeMarkdown)
		idOf := func(content string) int64 {
			t.Helper()
			for _, m := range st.Messages() {
				if m.Content == content {
					return m.ID
				}
			}
			t.Fatalf("no message %q", content)
			return 0
		}
		return st, idOf
	}

	rows := []struct {
		name    string
		prepare func(t *testing.T, st *session.State, idOf func(string) int64)
		change  func(t *testing.T, st *session.State, idOf func(string) int64)
	}{
		{
			name:   "rewind",
			change: func(t *testing.T, st *session.State, idOf func(string) int64) { st.Rewind(idOf("turn two")) },
		},
		{
			name:    "switch branch",
			prepare: func(t *testing.T, st *session.State, idOf func(string) int64) { st.Rewind(idOf("turn two")) },
			change: func(t *testing.T, st *session.State, idOf func(string) int64) {
				branches := st.Branches()
				st.SwitchBranch(branches[len(branches)-1])
			},
		},
		{
			name:   "clear messages",
			change: func(t *testing.T, st *session.State, idOf func(string) int64) { st.ClearMessages() },
		},
		{
			name: "log thinking",
			change: func(t *testing.T, st *session.State, idOf func(string) int64) {
				st.LogThinking(session.ThinkingEntry{Text: "weighing it"})
			},
		},
		{
			name: "set todos",
			change: func(t *testing.T, st *session.State, idOf func(string) int64) {
				if err := st.SetTodos([]db.TodoItem{{ID: "t1", Content: "a", Status: "pending"}}); err != nil {
					t.Fatalf("SetTodos: %v", err)
				}
			},
		},
		{
			name: "add a run event",
			change: func(t *testing.T, st *session.State, idOf func(string) int64) {
				st.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, Title: "abc123"})
			},
		},
		{
			name: "clear the run events",
			prepare: func(t *testing.T, st *session.State, idOf func(string) int64) {
				st.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, Title: "abc123"})
			},
			change: func(t *testing.T, st *session.State, idOf func(string) int64) { st.ClearRunEvents() },
		},
		{
			name: "move the workspace",
			change: func(t *testing.T, st *session.State, idOf func(string) int64) {
				st.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: elsewhere})
			},
		},
	}

	ctx := context.Background()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			st, idOf := build(t)
			// The row's setup runs before the first read, so its own change is
			// the one the read below must reflect: "switch branch back" and
			// "clear the run events" restore what the setup left.
			if row.prepare != nil {
				row.prepare(t, st, idOf)
			}
			sess := open(t, service.Deps{State: st})
			first := sess.Stack(0, false)

			row.change(t, st, idOf)

			want := viewmodel.ProjectState(st, false, st.Now()).Tree
			sameEncoded := func(a, b any) bool {
				aJSON, err := json.Marshal(a)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				bJSON, err := json.Marshal(b)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				return bytes.Equal(aJSON, bJSON)
			}
			if sameEncoded(first.Roots, want.Roots) && sameEncoded(first.Nodes, want.Nodes) {
				t.Fatal("the change did not move the projection: the row cannot prove its push")
			}

			// A call that changes nothing: its barrier (on the pipe arm) is what
			// delivers the patch the change published.
			if err := sess.ClearNotice(ctx, ""); err != nil {
				t.Fatalf("ClearNotice: %v", err)
			}
			got := sess.Stack(0, false)
			sameJSON(t, "Roots", got.Roots, want.Roots)
			sameJSON(t, "Nodes", got.Nodes, want.Nodes)
		})
	}
}

// stackBuildsWithTheBusyFlagItIsGiven pins that the flag decides the live
// beat and the turn's receipt, whatever subagentID names: a child registered as
// running is still projected not busy when the caller says so.
func stackBuildsWithTheBusyFlagItIsGiven(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
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

	sess := open(t, service.Deps{State: state})
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

// stackOfASubagentReadsItsTranscript pins that a subagent ID reads the
// child's transcript, not the session's, and keeps the session's identity.
func stackOfASubagentReadsItsTranscript(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "parent says hi", session.ContentTypePlain)

	child := stackState(t, "child", fixed)
	child.AddMessage(session.RoleUser, "child says hi", session.ContentTypePlain)
	view := state.RegisterSubagent("scout", child)

	sess := open(t, service.Deps{State: state})
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

// stackOfAnUnknownSubagentIsEmpty pins that an ID that names no child the
// session keeps reads as an empty snapshot carrying that ID.
func stackOfAnUnknownSubagentIsEmpty(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	sess := open(t, service.Deps{State: state})

	// The session's own stack answers first. On the pipe arm a refused read and
	// a missing handler are both an empty snapshot, so without this the empty
	// read below would pass with session/stack deleted.
	if own := sess.Stack(0, false); len(own.Nodes) == 0 {
		t.Fatalf("the session's own stack = %+v, want nodes", own)
	}

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

// stackReadsTheStatesClock pins that Stack builds at the state's clock: on
// a frozen clock in 2023 a stale list is not stale, where the wall clock would
// long since have made it so.
func stackReadsTheStatesClock(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
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

	sess := open(t, service.Deps{State: state})
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
	// Over the wire the client reads its mirror, and a band that goes stale
	// with time alone pushes nothing: flip the busy flag so the agent
	// re-projects at the state's new clock. Both arms then read the same tree.
	sess.Stack(0, false)
	if got := band(); !got.Stale {
		t.Errorf("band is not stale after TodoStaleAfter plus a minute; want stale")
	}
}

// stackNodeReturnsACallsDetail pins that a call's row detail carries what the
// projection caps: the whole output, the beat it belongs to and the step.
func stackNodeReturnsACallsDetail(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	st := seamState(t, "s1")
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := st.BeginStep(session.Actor{Model: "qwen3"})
	long := strings.Repeat("x", acpwire.WireTextCap*3)
	ev := registry.AuditEvent{
		Timestamp: time.Unix(2, 0), ToolName: "shell.run", StepID: step,
		ToolCallID: "c1", ResultContent: long,
	}
	st.LogToolCall(ev)

	sess := open(t, service.Deps{State: st})
	res, err := sess.StackNode(context.Background(), viewmodel.ToolKey(ev), 0)
	if err != nil {
		t.Fatalf("StackNode(%q) = %v", viewmodel.ToolKey(ev), err)
	}
	if len(res.Detail.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(res.Detail.Calls))
	}
	if got := res.Detail.Calls[0].Output; got != long {
		t.Fatalf("output = %d bytes, want the whole %d-byte output", len(got), len(long))
	}
	if want := fmt.Sprintf("step:%d", step); res.Node.Parent != want {
		t.Fatalf("node parent = %q, want the beat %q", res.Node.Parent, want)
	}
	if res.Detail.Step == nil {
		t.Fatal("the call row carries no step")
	}
	if res.Detail.Step.ID != int64(step) {
		t.Fatalf("step id = %d, want %d", res.Detail.Step.ID, step)
	}
}

// stackNodeReadsASubagentsTranscript pins that a subagent's node is read from
// the child's transcript, and that the session's own holds no such node.
func stackNodeReadsASubagentsTranscript(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	parent := seamState(t, "parent")

	child := seamState(t, "child")
	child.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := child.BeginStep(session.Actor{Model: "qwen3"})
	ev := registry.AuditEvent{
		Timestamp: time.Unix(20, 0), ToolName: "shell.run", StepID: step,
		ToolCallID: "c1", ResultSummary: "ok",
	}
	child.LogToolCall(ev)
	view := parent.RegisterSubagent("scout", child)

	sess := open(t, service.Deps{State: parent})
	key := viewmodel.ToolKey(ev)
	res, err := sess.StackNode(context.Background(), key, view.ID)
	if err != nil {
		t.Fatalf("StackNode(%q, %d) = %v", key, view.ID, err)
	}
	if len(res.Detail.Calls) != 1 || res.Detail.Calls[0].CallID != "c1" {
		t.Fatalf("calls = %+v, want the child's c1", res.Detail.Calls)
	}
	if _, err := sess.StackNode(context.Background(), key, 0); err == nil {
		t.Fatalf("StackNode(%q, 0) = nil error, want a miss: the parent holds no such node", key)
	}
}

// stackNodeFailsForAnUnknownNodeOrSubagent pins the two misses.
func stackNodeFailsForAnUnknownNodeOrSubagent(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	st := seamState(t, "s1")
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := st.BeginStep(session.Actor{Model: "qwen3"})
	ev := registry.AuditEvent{Timestamp: time.Unix(2, 0), ToolName: "shell.run",
		StepID: step, ToolCallID: "c1", ResultSummary: "ok"}
	st.LogToolCall(ev)

	sess := open(t, service.Deps{State: st})
	// A known node answers first: a deleted session/stack_node would otherwise
	// make both misses below pass as method-not-found.
	key := viewmodel.ToolKey(ev)
	if _, err := sess.StackNode(context.Background(), key, 0); err != nil {
		t.Fatalf("StackNode(%q, 0) = %v, want the known node", key, err)
	}
	if _, err := sess.StackNode(context.Background(), "tool:999:c9", 0); err == nil {
		t.Fatal(`StackNode("tool:999:c9", 0) = nil error, want a miss`)
	}
	if _, err := sess.StackNode(context.Background(), "tool:999:c9", 999999); err == nil {
		t.Fatal("StackNode with subagent 999999 = nil error, want a miss")
	}
}

// fakeFinder records each lookup and answers refs and ok.
type fakeFinder struct {
	refs   []string
	ok     bool
	calls  int
	asked  []string // "file:line:col" of each lookup
	failIf func()
}

// References is one lookup of a symbol's references.
func (f *fakeFinder) References(ctx context.Context, path string, line, col int) ([]string, bool) {
	if f.failIf != nil {
		f.failIf()
	}
	f.calls++
	f.asked = append(f.asked, fmt.Sprintf("%s:%d:%d", path, line, col))
	return f.refs, f.ok
}

// editEvent is the service's editEvent: a file.write_patch of transcript.go
// with the resolved symbol renderSubagentCard at line 584, column 5. Here it
// carries a step, so its row has a node key.
func editEvent(step session.StepID) registry.AuditEvent {
	return registry.AuditEvent{
		Timestamp:    time.Unix(10, 0),
		ToolName:     "file.write_patch",
		StepID:       step,
		ToolCallID:   "c1",
		FilesChanged: []string{"transcript.go"},
		Symbols: []registry.SymbolRef{{
			File: "transcript.go", Name: "renderSubagentCard",
			Kind: "function", Line: 584, Col: 5, Resolved: true,
		}},
	}
}

// logEdit logs the event mk builds for a fresh state's open step, and returns
// the state and the event it logged.
func logEdit(t *testing.T, mk func(session.StepID) registry.AuditEvent) (*session.State, registry.AuditEvent) {
	t.Helper()
	st := seamState(t, "s1")
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	ev := mk(st.BeginStep(session.Actor{Model: "qwen3"}))
	st.LogToolCall(ev)
	return st, ev
}

// callersAsksTheFinderForTheResolvedSymbol pins that the refs are the
// finder's, asked for the symbol the call changed.
func callersAsksTheFinderForTheResolvedSymbol(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	st, ev := logEdit(t, editEvent)
	f := &fakeFinder{refs: []string{"a.go:10"}, ok: true}

	sess := open(t, service.Deps{State: st, ReferenceFinder: f})
	got, err := sess.Callers(context.Background(), viewmodel.ToolKey(ev))
	if err != nil {
		t.Fatalf("Callers(%q) = %v", viewmodel.ToolKey(ev), err)
	}
	if len(got) != 1 || got[0] != "a.go:10" {
		t.Fatalf("callers = %v, want [a.go:10]", got)
	}
	if len(f.asked) != 1 || f.asked[0] != "transcript.go:584:5" {
		t.Fatalf("asked = %v, want one lookup of transcript.go:584:5", f.asked)
	}
}

// callersIsNilWithoutALookup pins every reason a row reads no callers: the
// finder answers nothing, the symbol is unresolved, the tool changed no
// symbol, the node is unknown, or there is no finder at all.
func callersIsNilWithoutALookup(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	cases := []struct {
		name   string
		edit   func(session.StepID) registry.AuditEvent
		key    func(registry.AuditEvent) string
		finder func(*fakeFinder) service.ReferenceFinder
	}{
		{
			name: "an unresolved symbol",
			edit: func(step session.StepID) registry.AuditEvent {
				ev := editEvent(step)
				ev.Symbols[0].Resolved = false
				return ev
			},
			key:    viewmodel.ToolKey,
			finder: func(f *fakeFinder) service.ReferenceFinder { return f },
		},
		{
			name: "a file.read call",
			edit: func(step session.StepID) registry.AuditEvent {
				ev := editEvent(step)
				ev.ToolName = "file.read"
				return ev
			},
			key:    viewmodel.ToolKey,
			finder: func(f *fakeFinder) service.ReferenceFinder { return f },
		},
		{
			name:   "an unknown node ID",
			edit:   editEvent,
			key:    func(registry.AuditEvent) string { return "tool:999:c9" },
			finder: func(f *fakeFinder) service.ReferenceFinder { return f },
		},
		{
			name:   "an adapter with no finder",
			edit:   editEvent,
			key:    viewmodel.ToolKey,
			finder: func(*fakeFinder) service.ReferenceFinder { return nil },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ev := logEdit(t, tc.edit)
			f := &fakeFinder{refs: []string{"a.go:10"}, ok: true}

			sess := open(t, service.Deps{State: st, ReferenceFinder: tc.finder(f)})
			got, err := sess.Callers(context.Background(), tc.key(ev))
			if err != nil {
				t.Fatalf("Callers = %v, want nil", err)
			}
			if got != nil {
				t.Fatalf("callers = %v, want nil", got)
			}
			if f.calls != 0 {
				t.Fatalf("the finder was asked %d times, want none", f.calls)
			}
		})
	}

	t.Run("a finder that is not ready", func(t *testing.T) {
		st, ev := logEdit(t, editEvent)
		f := &fakeFinder{ok: false}

		sess := open(t, service.Deps{State: st, ReferenceFinder: f})
		got, err := sess.Callers(context.Background(), viewmodel.ToolKey(ev))
		if err != nil {
			t.Fatalf("Callers = %v, want nil", err)
		}
		if got != nil {
			t.Fatalf("callers = %v, want nil", got)
		}
	})
}

// sheetCarriesTheSessionsActivity pins every in-memory value the sheet
// reads: the tool-call log, the files it touched, the rules, the skills, the
// budget, the loaded tools, the sandbox and the context pack.
func sheetCarriesTheSessionsActivity(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(10, 0),
		ToolName:  "file.read",
		Args:      json.RawMessage(`{"path":"a.go"}`),
		Duration:  10 * time.Millisecond,
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp:    time.Unix(20, 0),
		ToolName:     "file.write_patch",
		FilesChanged: []string{"b.go"},
		Duration:     30 * time.Millisecond,
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(30, 0),
		ToolName:  "shell.run",
		Error:     "exit 1",
		Duration:  50 * time.Millisecond,
	})
	state.AddSessionRule("go test")
	state.ActivateSkill("tdd")
	state.SetToolBudget(session.ToolBudget{Used: 3, Max: 100})
	state.AddLoadedToolNames([]string{"web.fetch"})
	state.SetSandboxInfo(session.SandboxInfo{Backend: "restricted"})
	state.Config.Tools.Shell.AllowNetwork = true
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 3400},
		Sections: []contextpack.Section{
			{Title: "repo map", EstimatedTokens: 2400},
			{Title: "todos"},
		},
	})

	sess := open(t, service.Deps{State: state})
	got := sess.Sheet(0)
	want := acpwire.SessionSheet{
		StartedAt: 100000,
		Context: acpwire.SheetContext{
			MaxTokens:       12000,
			EstimatedTokens: 3400,
			Sections: []acpwire.SheetContextSection{
				{Title: "repo map", EstimatedTokens: 2400},
				{Title: "todos"},
			},
		},
		Tools: []acpwire.SheetTool{
			{Name: "file.read", Calls: 1, SlowestMs: 10},
			{Name: "file.write_patch", Calls: 1, SlowestMs: 30},
			{Name: "shell.run", Calls: 1, Errors: 1, SlowestMs: 50},
		},
		Files: []acpwire.SheetFile{
			{Path: "b.go", Edits: 1, LastAt: 20000},
			{Path: "a.go", Reads: 1, LastAt: 10000},
		},
		Rules:       []string{"go test"},
		Skills:      []string{"tdd"},
		ToolBudget:  acpwire.SheetToolBudget{Used: 3, Max: 100},
		LoadedTools: 1,
		Sandbox:     acpwire.SheetSandbox{Backend: "restricted", AllowNetwork: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sheet(0) =\n%+v\nwant\n%+v", got, want)
	}
}

// sheetReadsASubagentsOwnActivity pins that a subagent's ID reads the
// child's activity, not the parent's, and that only the session's own sheet
// carries a context pack.
func sheetReadsASubagentsOwnActivity(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	parent := seamState(t, "parent")
	parent.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "parent-probe"})
	parent.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 100},
		Sections:   []contextpack.Section{{Title: "repo map", EstimatedTokens: 100}},
	})

	child := session.New(config.Default(), t.TempDir(), time.Unix(200, 0), session.Persistence{SessionID: "child"})
	child.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(20, 0), ToolName: "child-probe"})
	child.AddSessionRule("child rule")
	view := parent.RegisterSubagent("scout", child)

	sess := open(t, service.Deps{State: parent})
	got := sess.Sheet(view.ID)

	if got.SubagentID != view.ID {
		t.Errorf("SubagentID = %d, want the view's %d", got.SubagentID, view.ID)
	}
	if want := []acpwire.SheetTool{{Name: "child-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("Tools = %+v, want only the child's %+v", got.Tools, want)
	}
	if want := []string{"child rule"}; !reflect.DeepEqual(got.Rules, want) {
		t.Errorf("Rules = %v, want %v", got.Rules, want)
	}
	if got.StartedAt != 200000 {
		t.Errorf("StartedAt = %d, want the child's 200000", got.StartedAt)
	}
	if !reflect.DeepEqual(got.Context, acpwire.SheetContext{}) {
		t.Errorf("Context = %+v, want empty for a subagent", got.Context)
	}
}

// sheetFindsANestedSubagent pins that the search descends into a child's
// own subagents.
func sheetFindsANestedSubagent(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	parent := seamState(t, "parent")
	child := seamState(t, "child")
	parent.RegisterSubagent("scout", child)

	grandchild := seamState(t, "grandchild")
	grandchild.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(30, 0), ToolName: "grandchild-probe"})
	grandView := child.RegisterSubagent("scout-of-scout", grandchild)

	sess := open(t, service.Deps{State: parent})
	got := sess.Sheet(grandView.ID)

	if got.SubagentID != grandView.ID {
		t.Errorf("SubagentID = %d, want the grandchild's %d", got.SubagentID, grandView.ID)
	}
	if want := []acpwire.SheetTool{{Name: "grandchild-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
		t.Errorf("Tools = %+v, want the grandchild's %+v", got.Tools, want)
	}
}

// sheetReadsAnUnknownSubagentAsTheSession pins that an ID naming no child
// session, and a card registered with no child, both read as the session's own
// sheet.
func sheetReadsAnUnknownSubagentAsTheSession(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	parent := seamState(t, "parent")
	parent.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(10, 0), ToolName: "parent-probe"})
	card := parent.RegisterSubagent("pipeline", nil)

	sess := open(t, service.Deps{State: parent})
	for _, id := range []int64{999999, card.ID} {
		got := sess.Sheet(id)
		if got.SubagentID != 0 {
			t.Errorf("Sheet(%d).SubagentID = %d, want 0", id, got.SubagentID)
		}
		if want := []acpwire.SheetTool{{Name: "parent-probe", Calls: 1}}; !reflect.DeepEqual(got.Tools, want) {
			t.Errorf("Sheet(%d).Tools = %+v, want the parent's %+v", id, got.Tools, want)
		}
	}
}

// refreshSheetRebasesOntoHead pins that a Rebase reads HEAD and the changed
// files against it, and that the status line's count follows.
func refreshSheetRebasesOntoHead(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	requireSheetGit(t)
	repo := sheetRepo(t)
	state := session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
	sess := open(t, service.Deps{State: state})

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	want := []acpwire.SheetChangedFile{{Path: "a.txt", Status: "M", Added: 1}}
	if got := sess.Sheet(0).Changed; !reflect.DeepEqual(got, want) {
		t.Errorf("Changed = %+v, want %+v", got, want)
	}
	if got := sess.Status().ChangedFiles; got != 1 {
		t.Errorf("Status().ChangedFiles = %d, want 1", got)
	}

	runGit(t, repo, "commit", "-qam", "change")
	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Rebase: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	if got := sess.Sheet(0).Changed; len(got) != 0 {
		t.Errorf("Changed after the commit = %+v, want none", got)
	}
	if got := sess.Status().ChangedFiles; got != 0 {
		t.Errorf("Status().ChangedFiles after the commit = %d, want 0", got)
	}
}

// refreshSheetReadsTurnsTotalsAndRepoCounts pins the Stats refresh: the
// recent turn metrics, the session's usage totals and the repo's counts.
func refreshSheetReadsTurnsTotalsAndRepoCounts(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("s1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, row := range []db.TurnMetricsRow{
		{ProjectID: projectID, SessionID: "s1", StartedAt: time.Unix(1000, 0).UTC(), PromptTokens: 100, CompletionTokens: 20, CacheReadTokens: 50},
		{ProjectID: projectID, SessionID: "s1", StartedAt: time.Unix(2000, 0).UTC(), PromptTokens: 200, CompletionTokens: 40},
	} {
		if _, err := database.InsertTurnMetrics(row); err != nil {
			t.Fatalf("InsertTurnMetrics: %v", err)
		}
	}
	indexedAt := time.Unix(3000, 0).UTC()
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "a.go", Language: "go", Hash: "h1", LastIndexedAt: indexedAt},
		{Path: "b.go", Language: "go", Hash: "h2", LastIndexedAt: indexedAt},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	if err := database.SaveSymbols(projectID, []db.Symbol{
		{FilePath: "a.go", Kind: "function", Name: "A"},
		{FilePath: "a.go", Kind: "function", Name: "B"},
		{FilePath: "b.go", Kind: "type", Name: "C"},
	}); err != nil {
		t.Fatalf("SaveSymbols: %v", err)
	}

	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{DB: database, SessionID: "s1"})
	sess := open(t, service.Deps{State: state, ProjectID: projectID})

	if err := sess.RefreshSheet(context.Background(), acpwire.SheetRefresh{Stats: true}); err != nil {
		t.Fatalf("RefreshSheet: %v", err)
	}
	got := sess.Sheet(0)
	wantTurns := []acpwire.SheetTurn{
		{StartedAt: 2000000, PromptTokens: 200, CompletionTokens: 40},
		{StartedAt: 1000000, PromptTokens: 100, CompletionTokens: 20, CacheReadTokens: 50},
	}
	if !reflect.DeepEqual(got.Turns, wantTurns) {
		t.Errorf("Turns = %+v, want %+v", got.Turns, wantTurns)
	}
	wantTotals := acpwire.SheetTotals{Turns: 2, PromptTokens: 300, CompletionTokens: 60, CacheReadTokens: 50}
	if got.Totals != wantTotals {
		t.Errorf("Totals = %+v, want %+v", got.Totals, wantTotals)
	}
	if want := (acpwire.SheetRepo{Files: 2, Symbols: 3}); got.Repo != want {
		t.Errorf("Repo = %+v, want %+v", got.Repo, want)
	}
}

// releasedSubagentReadsTheSame pins that leaving a drill-in changes no
// answer: the reads after the release equal the reads before it.
func releasedSubagentReadsTheSame(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fixed := time.Unix(1_700_000_000, 0)
	state := stackState(t, "s1", fixed)
	state.AddMessage(session.RoleUser, "parent says hi", session.ContentTypePlain)

	child := stackState(t, "child", fixed)
	child.AddMessage(session.RoleUser, "child says hi", session.ContentTypePlain)
	recordCall(child, "file.read", "c1", fixed)
	view := state.RegisterSubagent("scout", child)

	sess := open(t, service.Deps{State: state})
	ctx := context.Background()

	beforeStack := sess.Stack(view.ID, false)
	beforeSheet := sess.Sheet(view.ID)

	if err := sess.ReleaseSubagent(ctx, view.ID); err != nil {
		t.Fatalf("ReleaseSubagent(%d) = %v, want nil", view.ID, err)
	}

	afterStack := sess.Stack(view.ID, false)
	afterSheet := sess.Sheet(view.ID)

	if afterStack.SubagentID != beforeStack.SubagentID {
		t.Errorf("SubagentID after the release = %d, want %d", afterStack.SubagentID, beforeStack.SubagentID)
	}
	sameJSON(t, "Roots after the release", afterStack.Roots, beforeStack.Roots)
	sameJSON(t, "Nodes after the release", afterStack.Nodes, beforeStack.Nodes)
	if !reflect.DeepEqual(afterSheet, beforeSheet) {
		t.Errorf("Sheet(%d) after the release =\n%+v\nwant\n%+v", view.ID, afterSheet, beforeSheet)
	}
}
