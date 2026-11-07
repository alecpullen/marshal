package viewmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// memoClock is a clock the test drives, so every event in a sequence lands at
// a timestamp the test chose.
type memoClock struct{ now time.Time }

func (c *memoClock) get() time.Time { return c.now }

// next moves the clock a second on, before one event.
func (c *memoClock) next() { c.now = c.now.Add(time.Second) }

// memoTestState is projTestState on a clock the test advances one second per
// event.
func memoTestState(t *testing.T) (*session.State, *memoClock) {
	t.Helper()
	c := &memoClock{now: at(1)}
	st := session.New(config.Default(), "/repo", at(0), session.Persistence{}, session.WithClock(c.get))
	return st, c
}

// checkMemo compares m's projection of st with a fresh one: the whole tree,
// and every non-band node's encoding with the fresh node's JSON.
func checkMemo(t *testing.T, m *WireMemo, st *session.State, busy bool, now time.Time) Projection {
	t.Helper()
	got := m.ProjectState(st, busy, now)
	want := ProjectState(st, busy, now)
	if !reflect.DeepEqual(got.Tree, want.Tree) {
		t.Fatalf("memo tree differs from a fresh projection:\n got %+v\nwant %+v", got.Tree, want.Tree)
	}
	for _, n := range want.Tree.Nodes {
		if n.ID == acpwire.BandID {
			continue
		}
		data, ok := m.Encoded(n.ID)
		if !ok {
			t.Fatalf("no memo entry for node %q", n.ID)
		}
		fresh, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("marshal node %q: %v", n.ID, err)
		}
		if !bytes.Equal(data, fresh) {
			t.Fatalf("encoding of node %q = %s, want %s", n.ID, data, fresh)
		}
	}
	return got
}

// nodeByID returns the wire node with id.
func nodeByID(t *testing.T, tree acpwire.WireTree, id string) acpwire.WireNode {
	t.Helper()
	for _, w := range tree.Nodes {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("tree has no node %q", id)
	return acpwire.WireNode{}
}

// unreusable counts the nodes a memo must project afresh: a live one, or one
// with no version to compare. The band is not the memo's.
func unreusable(tree acpwire.WireTree) int {
	n := 0
	for _, w := range tree.Nodes {
		if w.ID == acpwire.BandID {
			continue
		}
		if w.Live || w.Version == "" {
			n++
		}
	}
	return n
}

// mustPanicNaming fails unless fn panics with a message naming want.
func mustPanicNaming(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("nothing panicked, want a panic naming %q", want)
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, want) {
			t.Fatalf("panic %q does not name %q", msg, want)
		}
	}()
	fn()
}

// A memo driven through a whole session — the payloads that change within a
// turn, the rows that settle, the band and the working directory — returns
// exactly what a fresh projection returns at every step.
func TestWireMemoMatchesAFreshProjection(t *testing.T) {
	st, clk := memoTestState(t)
	m := &WireMemo{}
	now := at(1000)

	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	checkMemo(t, m, st, true, now)

	// A step with three calls of one tool (a merged run) and an edit.
	clk.next()
	step := st.BeginStep(session.Actor{})
	for i := 0; i < 3; i++ {
		clk.next()
		st.LogToolCall(registry.AuditEvent{
			ToolName: "shell.run", StepID: step, ToolCallID: fmt.Sprintf("c%d", i),
			Args: json.RawMessage(`{"command":"cd /repo && go build ./..."}`), ResultSummary: "ok",
		})
	}
	clk.next()
	st.LogToolCall(registry.AuditEvent{
		ToolName: "file.edit", StepID: step, ToolCallID: "e1",
		Args: json.RawMessage(`{"path":"/repo/main.go"}`), ResultSummary: "edited main.go",
		FilesChanged: []string{"main.go"}, ResultContent: "+1 −1",
	})
	checkMemo(t, m, st, true, now)

	// An in-flight call, then streaming reasoning.
	clk.next()
	st.SetActiveToolCall(session.ActiveToolCall{
		Name: "shell.run", ToolCallID: "c4", StepID: step, StartedAt: clk.get(),
		Args: `{"command":"cd /repo && go test ./..."}`, Output: "running\n",
	})
	checkMemo(t, m, st, true, now)

	clk.next()
	st.BeginStreaming()
	st.AppendThinking("Considering the parser.")
	checkMemo(t, m, st, true, now)

	// The call settles.
	clk.next()
	st.ClearActiveToolCallID("c4")
	st.EndStreaming()
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: step, ToolCallID: "c4",
		Args: json.RawMessage(`{"command":"cd /repo && go test ./..."}`), ResultSummary: "ok",
	})
	checkMemo(t, m, st, true, now)

	// The final reply.
	clk.next()
	st.EndStep(step)
	st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
	checkMemo(t, m, st, false, now)

	// A second turn, with a todo write.
	clk.next()
	st.AddMessage(session.RoleUser, "again", session.ContentTypePlain)
	step2 := st.BeginStep(session.Actor{})
	clk.next()
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: step2, ToolCallID: "j1",
		Args: json.RawMessage(`{"command":"cd /repo && make watch"}`),
		// A background start, so the exit below has a row to pair with.
		ResultSummary: session.JobStartPrefix + "job-1",
	})
	clk.next()
	if err := st.SetTodos(todos(db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"})); err != nil {
		t.Fatal(err)
	}
	st.LogToolCall(registry.AuditEvent{
		ToolName: "todo.write", StepID: step2, ToolCallID: "t1",
		Args:          json.RawMessage(`{"todos":[{"id":"p","content":"Parser","status":"in_progress"}]}`),
		ResultSummary: "tasks updated · 1 items",
	})
	checkMemo(t, m, st, true, now)

	// A job completion.
	clk.next()
	if err := st.AddJobExit(session.JobExit{
		ID: "job-1", Command: "make watch", ExitCode: 0, At: clk.get(),
		Duration: 2 * time.Second, Output: "watching\n",
	}); err != nil {
		t.Fatal(err)
	}
	checkMemo(t, m, st, true, now)

	// A subagent card, running and then finished.
	clk.next()
	v := st.RegisterSubagent("worker", nil)
	checkMemo(t, m, st, true, now)
	clk.next()
	st.FinishSubagent(v.ID, "wrote the memo", nil)
	checkMemo(t, m, st, true, now)

	// The active root moves, so every call's elided subject changes while its
	// version does not.
	clk.next()
	st.SetWorkspace(session.Workspace{ActiveRoot: "/repo-wt"})
	checkMemo(t, m, st, true, now)
}

// A projection after one new call re-projects the turn nodes (which carry no
// version) and the few rows that changed, and reuses every other node as the
// very value it returned before.
func TestWireMemoReusesUnchangedNodes(t *testing.T) {
	st := projTestState(t)
	for turn := 0; turn < 10; turn++ {
		st.AddMessage(session.RoleUser, fmt.Sprintf("turn %d", turn), session.ContentTypePlain)
		step := st.BeginStep(session.Actor{})
		for call := 0; call < 10; call++ {
			id := fmt.Sprintf("t%02d-c%02d", turn, call)
			st.LogToolCall(registry.AuditEvent{
				ToolName: "shell.run", StepID: step, ToolCallID: id,
				Args:          json.RawMessage(fmt.Sprintf(`{"command":"echo %s"}`, id)),
				ResultContent: "ok",
			})
		}
		st.EndStep(step)
		st.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
	}
	m := &WireMemo{}
	now := at(1000)
	first := m.ProjectState(st, false, now)
	if want := len(first.Tree.Nodes) - 1; m.fresh != want {
		t.Fatalf("first projection projected %d nodes afresh, want all %d", m.fresh, want)
	}

	// The first turn's call row: a pointer the later projection must reuse.
	var firstCall acpwire.WireNode
	for _, w := range first.Tree.Nodes {
		if w.Kind == acpwire.KindTool {
			firstCall = w
			break
		}
	}
	if firstCall.Tool == nil {
		t.Fatal("the first projection has no call row")
	}

	// One new step with one call.
	step := st.BeginStep(session.Actor{})
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: step, ToolCallID: "new-1",
		Args: json.RawMessage(`{"command":"echo new"}`), ResultContent: "ok",
	})
	second := m.ProjectState(st, false, now)

	if limit := len(second.Turns) + 5; m.fresh > limit {
		t.Fatalf("second projection projected %d nodes afresh, want at most %d of %d", m.fresh, limit, len(second.Tree.Nodes))
	}
	if m.fresh*3 > len(second.Tree.Nodes) {
		t.Fatalf("second projection projected %d of %d nodes afresh, want far fewer", m.fresh, len(second.Tree.Nodes))
	}
	if again := nodeByID(t, second.Tree, firstCall.ID); again.Tool != firstCall.Tool {
		t.Fatalf("node %q was projected again: its Tool pointer changed", firstCall.ID)
	}
}

// Two projections of one busy state re-project what can change without a
// version change: the in-flight call and the turn nodes.
func TestWireMemoNeverReusesLiveOrUnversionedNodes(t *testing.T) {
	st, clk := memoTestState(t)
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	clk.next()
	step := st.BeginStep(session.Actor{})
	clk.next()
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: step, ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"go build ./..."}`), ResultSummary: "ok",
	})
	clk.next()
	live := session.ActiveToolCall{
		Name: "shell.run", ToolCallID: "c2", StepID: step, StartedAt: clk.get(),
		Args: `{"command":"go test ./..."}`,
	}
	st.SetActiveToolCall(live)

	m := &WireMemo{}
	now := at(1000)
	first := m.ProjectState(st, true, now)
	if want := len(first.Tree.Nodes) - 1; m.fresh != want {
		t.Fatalf("first projection projected %d nodes afresh, want all %d", m.fresh, want)
	}
	second := m.ProjectState(st, true, now)

	// Nothing changed between the calls, so only the nodes that can never be
	// reused — the live ones and those carrying no version — are fresh.
	if want := unreusable(second.Tree); m.fresh != want {
		t.Fatalf("second projection projected %d nodes afresh, want the %d live or unversioned ones", m.fresh, want)
	}
	for _, turn := range second.Turns {
		if n := nodeByID(t, second.Tree, turn.ID.Key); !n.Live && n.Version != "" {
			t.Fatalf("turn node %q has a version; the fixture needs unversioned turns", n.ID)
		}
	}
	liveID := ActiveToolID(live).Key
	if a, b := nodeByID(t, first.Tree, liveID), nodeByID(t, second.Tree, liveID); a.Tool == b.Tool {
		t.Fatalf("the in-flight call %q was reused", liveID)
	}
	// The settled call, unchanged, is not re-projected.
	settled := ToolID(registry.AuditEvent{StepID: step, ToolCallID: "c1"}).Key
	if a, b := nodeByID(t, first.Tree, settled), nodeByID(t, second.Tree, settled); a.Tool != b.Tool {
		t.Fatalf("the settled call %q was projected again", settled)
	}
}

// A beat projected while it was live is not reused once it settles: the wire
// form carries Live, and no version covers it, so a settled beat would keep
// the live flag (found by the TUI's live region, P3 plan 4c).
func TestWireMemoDoesNotReuseAnEntryCapturedWhileLive(t *testing.T) {
	st, clk := memoTestState(t)
	clk.next()
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	step := st.BeginStep(session.Actor{})
	clk.next()
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: step, ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"go build ./..."}`), ResultSummary: "ok",
	})
	clk.next()
	st.AddNarration(step, "building")

	m := &WireMemo{}
	now := at(1000)
	busy := checkMemo(t, m, st, true, now)
	beatID := ""
	for _, n := range busy.Tree.Nodes {
		if n.Kind != acpwire.KindStep {
			continue
		}
		if !n.Live {
			t.Fatalf("the open beat %q is not live; the fixture needs a live one", n.ID)
		}
		beatID = n.ID
		break
	}
	if beatID == "" {
		t.Fatal("the projection has no beat node")
	}

	// The turn stops with the state untouched: only the beat's Live moves,
	// and no version covers it.
	idle := checkMemo(t, m, st, false, now)
	was, is := nodeByID(t, busy.Tree, beatID), nodeByID(t, idle.Tree, beatID)
	if was.Version != is.Version || was.Version == "" {
		t.Fatalf("the beat's version moved from %q to %q; the fixture needs an unchanged one", was.Version, is.Version)
	}
	if is.Live {
		t.Fatalf("beat %q stayed live once the turn stopped", beatID)
	}
}

// The band is not the memo's, and an unknown ID has no entry.
func TestEncodedReportsUnknownIDs(t *testing.T) {
	st, clk := memoTestState(t)
	clk.next()
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m := &WireMemo{}
	proj := m.ProjectState(st, false, at(1000))

	if _, ok := m.Encoded(acpwire.BandID); ok {
		t.Fatalf("%q is the band, which the memo does not hold", acpwire.BandID)
	}
	if _, ok := m.Encoded("no-such-node"); ok {
		t.Fatal("an unknown ID reported an encoding")
	}
	if _, ok := m.Encoded(proj.Tree.Roots[0]); !ok {
		t.Fatal("the root turn reported no encoding")
	}
}

// The check catches a kept wire form that no longer matches a fresh
// projection, and names the node.
func TestTheCheckPanicsOnAStaleEntry(t *testing.T) {
	prev := CheckWireMemo.Load()
	CheckWireMemo.Store(true)
	t.Cleanup(func() { CheckWireMemo.Store(prev) })

	st, clk := memoTestState(t)
	clk.next()
	st.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	step := st.BeginStep(session.Actor{})
	clk.next()
	st.LogToolCall(registry.AuditEvent{
		ToolName: "shell.run", StepID: step, ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"go build ./..."}`), ResultSummary: "ok",
	})
	m := &WireMemo{}
	proj := m.ProjectState(st, false, at(1000))

	// A versioned row the memo kept, made stale behind its back.
	var stale string
	for _, w := range proj.Tree.Nodes {
		if w.Version != "" && !w.Live {
			stale = w.ID
			break
		}
	}
	if stale == "" {
		t.Fatal("the fixture has no versioned node to make stale")
	}
	m.entries[stale].node.Version = "stale"

	mustPanicNaming(t, stale, func() { m.ProjectState(st, false, at(1000)) })
}
