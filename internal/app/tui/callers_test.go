package tui

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type fakeFinder struct {
	refs   []string
	ok     bool
	calls  int
	failIf func()
}

func (f *fakeFinder) References(ctx context.Context, path string, line, col int) ([]string, bool) {
	if f.failIf != nil {
		f.failIf()
	}
	f.calls++
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

func TestCallersLineRendersWhenResolved(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(editEvent(), false, []string{"a.go:10", "b.go:20"}, 100))
	if !strings.Contains(out, "callers") {
		t.Fatalf("expected a callers line:\n%s", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("callers line missing a reference:\n%s", out)
	}
}

func TestNoCallersLineWhenEmpty(t *testing.T) {
	out := stripANSI(renderCompletedToolCall(editEvent(), false, nil, 100))
	if strings.Contains(out, "callers") {
		t.Fatalf("empty result must render no callers line — '0 callers' is\nindistinguishable from an unindexed server:\n%s", out)
	}
}

// The renderer must never issue a query: it runs on every spinner tick.
func TestRendererNeverQueries(t *testing.T) {
	f := &fakeFinder{ok: true, failIf: func() { t.Fatal("renderCompletedToolCall issued an LSP query") }}
	m := newTestModel(t)
	m.refFinder = f
	_ = renderCompletedToolCall(editEvent(), false, nil, 100)
}

func TestUnresolvedSymbolIsNeverQueried(t *testing.T) {
	m := newTestModel(t)
	f := &fakeFinder{ok: true, refs: []string{"x.go:1"}}
	m.refFinder = f
	e := editEvent()
	e.Symbols[0].Resolved = false
	m.state.LogToolCall(e)
	if cmd := m.callerQueryCmds(); cmd != nil {
		cmd()
	}
	if f.calls != 0 {
		t.Fatal("an unresolved symbol position must never be queried — a guessed\nposition returns confidently wrong callers")
	}
}

func TestNegativeResultIsCachedAndNotRetried(t *testing.T) {
	m := newTestModel(t)
	f := &fakeFinder{ok: false}
	m.refFinder = f
	m.state.LogToolCall(editEvent())
	if cmd := m.callerQueryCmds(); cmd != nil {
		if msg := cmd(); msg != nil {
			m2, _ := m.Update(msg)
			m = m2.(Model)
		}
	}
	before := f.calls
	if cmd := m.callerQueryCmds(); cmd != nil {
		cmd()
	}
	if f.calls != before {
		t.Fatalf("a no-server result must not be retried: %d -> %d calls", before, f.calls)
	}
}

func TestNilFinderIsSafe(t *testing.T) {
	m := newTestModel(t)
	m.refFinder = nil
	m.state.LogToolCall(editEvent())
	if cmd := m.callerQueryCmds(); cmd != nil {
		t.Fatal("a nil finder must produce no commands")
	}
}

// Same rule as the notice banner and region offsets: state rendered into
// the transcript but living outside items must bust the viewport cache.
func TestCallersBustTheTranscriptHash(t *testing.T) {
	m := newTestModel(t)
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	before := m.viewport.GetContent()

	key := itemKey{}
	for _, item := range m.state.Transcript() {
		if item.Kind == session.KindAudit {
			key = itemKeyFor(&item)
		}
	}
	m.callers = map[itemKey][]string{key: {"a.go:10"}}
	m.refreshViewport()
	if m.viewport.GetContent() == before {
		t.Fatal("callers must be folded into transcriptHash, or results repaint nothing")
	}
}

func TestStaleCallersArePruned(t *testing.T) {
	m := newTestModel(t)
	gone := itemKey{}
	m.callers = map[itemKey][]string{gone: {"a.go:1"}}
	m.callersAsked = map[itemKey]bool{gone: true}
	m.state.LogToolCall(editEvent())
	m.refreshViewport()
	if _, still := m.callers[gone]; still {
		t.Fatal("callers for items no longer in the transcript must be pruned (rollback)")
	}
}
