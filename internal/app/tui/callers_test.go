package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

type fakeFinder struct {
	refs   []string
	ok     bool
	calls  int
	asked  []string // "file:line:col" of each lookup
	failIf func()
}

func (f *fakeFinder) References(ctx context.Context, path string, line, col int) ([]string, bool) {
	if f.failIf != nil {
		f.failIf()
	}
	f.calls++
	f.asked = append(f.asked, fmt.Sprintf("%s:%d:%d", path, line, col))
	return f.refs, f.ok
}

func editEvent() registry.AuditEvent {
	return registry.AuditEvent{
		ToolName:     "file.write_patch",
		FilesChanged: []string{"transcript.go"},
		Symbols: []registry.SymbolRef{{
			File: "transcript.go", Name: "renderSubagentCard",
			Kind: "function", Line: 584, Col: 5, Resolved: true,
		}},
	}
}

// callersModel builds a model over an adapter made from a state, with f as the
// session's reference finder. A nil f leaves the adapter with none, so the
// session answers every lookup with nil rather than a nil pointer arriving as a
// non-nil interface.
func callersModel(t *testing.T, f *fakeFinder) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	var rf inproc.ReferenceFinder
	if f != nil {
		rf = f
	}
	m := testModelWith(inproc.Deps{State: state, ReferenceFinder: rf},
		WithCommandRegistry(reg), WithHomeDir(t.TempDir()),
		WithAuthResolver(newMemAuth()), WithOpenBrowser(func(string) error { return nil }))
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// flaggedCallerKey is the row the wire flagged for a callers lookup.
func flaggedCallerKey(t *testing.T, m Model) viewmodel.NodeID {
	t.Helper()
	for id, w := range m.wire {
		if w.Tool != nil && w.Tool.Callers {
			return viewmodel.NodeID{Kind: viewmodel.KindTool, Key: id}
		}
	}
	t.Fatal("no row is flagged for a callers lookup")
	return viewmodel.NodeID{}
}

func TestCallersLineRendersWhenResolved(t *testing.T) {
	out := stripANSI(renderCallRow(editEvent(), false, []string{"a.go:10", "b.go:20"}, 100, ""))
	if !strings.Contains(out, "callers") {
		t.Fatalf("expected a callers line:\n%s", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("callers line missing a reference:\n%s", out)
	}
}

func TestNoCallersLineWhenEmpty(t *testing.T) {
	out := stripANSI(renderCallRow(editEvent(), false, nil, 100, ""))
	if strings.Contains(out, "callers") {
		t.Fatalf("empty result must render no callers line — '0 callers' is\nindistinguishable from an unindexed server:\n%s", out)
	}
}

// The renderer must never issue a query: it runs on every spinner tick.
func TestRendererNeverQueries(t *testing.T) {
	f := &fakeFinder{ok: true, failIf: func() { t.Fatal("refreshViewport issued a callers lookup") }}
	m := callersModel(t, f)
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	m.refreshViewport()
	if f.calls != 0 {
		t.Fatalf("a refresh issued %d lookups, want none", f.calls)
	}
}

func TestUnresolvedSymbolIsNeverQueried(t *testing.T) {
	f := &fakeFinder{ok: true, refs: []string{"x.go:1"}}
	m := callersModel(t, f)
	e := editEvent()
	e.Symbols[0].Resolved = false
	m.state.LogToolCall(e)
	m.refreshViewport()
	if cmd := m.callerQueryCmds(); cmd != nil {
		cmd()
	}
	if f.calls != 0 {
		t.Fatal("an unresolved symbol position must never be queried — a guessed\nposition returns confidently wrong callers")
	}
}

func TestNegativeResultIsCachedAndNotRetried(t *testing.T) {
	f := &fakeFinder{ok: false}
	m := callersModel(t, f)
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	cmd := m.callerQueryCmds()
	if cmd == nil {
		t.Fatal("a flagged row must be asked")
	}
	if msg := cmd(); msg != nil {
		m2, _ := m.Update(msg)
		m = m2.(Model)
	}
	before := f.calls
	if cmd := m.callerQueryCmds(); cmd != nil {
		cmd()
	}
	if f.calls != before {
		t.Fatalf("a no-server result must not be retried: %d -> %d calls", before, f.calls)
	}
}

func TestNoFinderLeavesNoCallersLine(t *testing.T) {
	m := callersModel(t, nil)
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	key := flaggedCallerKey(t, m)
	cmd := m.callerQueryCmds()
	if cmd == nil {
		t.Fatal("a flagged row still asks the session, which answers nil without a finder")
	}
	if msg := cmd(); msg != nil {
		m2, _ := m.Update(msg)
		m = m2.(Model)
	}
	vals, ok := m.callers[key]
	if !ok || vals != nil {
		t.Fatalf("no finder must store a nil answer for %v: %v (present %v)", key, vals, ok)
	}
	if strings.Contains(m.viewport.GetContent(), "callers") {
		t.Fatal("a nil answer must render no callers line")
	}
}

func TestCallersAreAskedOnceForAFlaggedRow(t *testing.T) {
	f := &fakeFinder{ok: true, refs: []string{"a.go:10"}}
	m := callersModel(t, f)
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	key := flaggedCallerKey(t, m)

	cmd := m.callerQueryCmds()
	if cmd == nil {
		t.Fatal("the flagged row was not asked")
	}
	if msg := cmd(); msg != nil {
		m2, _ := m.Update(msg)
		m = m2.(Model)
	}
	if got := m.callers[key]; len(got) != 1 || got[0] != "a.go:10" {
		t.Fatalf("callers = %v, want [a.go:10]", got)
	}
	if cmd := m.callerQueryCmds(); cmd != nil {
		t.Fatal("a row is asked once and its answer kept, a nil one included")
	}
}

func TestCallersWaitWhileDrilledIn(t *testing.T) {
	f := &fakeFinder{ok: true, refs: []string{"a.go:10"}}
	m := callersModel(t, f)
	child := session.New(config.Default(), t.TempDir(), time.Unix(200, 0), session.Persistence{})
	v := m.state.RegisterSubagent("scout", child)
	m.state.LogToolCall(editEvent())
	m.drillIntoSubagent(v)
	m.refreshViewport()
	if cmd := m.callerQueryCmds(); cmd != nil {
		t.Fatal("a lookup must wait while the view is drilled in: the index holds the child's transcript")
	}
	m.popDrill()
	m.refreshViewport()
	if cmd := m.callerQueryCmds(); cmd == nil {
		t.Fatal("the row must be asked on the first tick after the view returns")
	}
}

// Same rule as the notice banner and region offsets: state rendered into
// the transcript but living outside items must bust the viewport cache.
func TestCallersBustTheTranscriptHash(t *testing.T) {
	m := newTestModel(t)
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	before := m.viewport.GetContent()

	var key viewmodel.NodeID
	for _, item := range m.state.Transcript() {
		if item.Kind == session.KindAudit {
			key = viewmodel.ToolID(*item.Audit)
		}
	}
	m.callers = map[viewmodel.NodeID][]string{key: {"a.go:10"}}
	m.refreshViewport()
	if m.viewport.GetContent() == before {
		t.Fatal("callers must be folded into the content signature, or results repaint nothing")
	}
}

func TestStaleCallersArePruned(t *testing.T) {
	m := newTestModel(t)
	gone := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:gone"}
	m.callers = map[viewmodel.NodeID][]string{gone: {"a.go:1"}}
	m.callersAsked = map[viewmodel.NodeID]bool{gone: true}
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	if _, still := m.callers[gone]; still {
		t.Fatal("callers for items no longer in the transcript must be pruned (rollback)")
	}
}
