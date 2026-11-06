package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// fakeFinder records each lookup and answers refs and ok.
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

// editEvent is tui/callers_test.go's editEvent: a file.write_patch of
// transcript.go with the resolved symbol renderSubagentCard at line 584,
// column 5. Here it carries a step, so its row has a node key.
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
	st := newState(t, "s1")
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	ev := mk(st.BeginStep(session.Actor{Model: "qwen3"}))
	st.LogToolCall(ev)
	return st, ev
}

func TestCallersAsksTheFinderForTheResolvedSymbol(t *testing.T) {
	st, ev := logEdit(t, editEvent)
	f := &fakeFinder{refs: []string{"a.go:10"}, ok: true}

	_, sess := New(Deps{State: st, ReferenceFinder: f})
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

func TestCallersIsNilWithoutALookup(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(session.StepID) registry.AuditEvent
		key    func(registry.AuditEvent) string
		finder func(*fakeFinder) ReferenceFinder
	}{
		{
			name: "an unresolved symbol",
			edit: func(step session.StepID) registry.AuditEvent {
				ev := editEvent(step)
				ev.Symbols[0].Resolved = false
				return ev
			},
			key:    viewmodel.ToolKey,
			finder: func(f *fakeFinder) ReferenceFinder { return f },
		},
		{
			name: "a file.read call",
			edit: func(step session.StepID) registry.AuditEvent {
				ev := editEvent(step)
				ev.ToolName = "file.read"
				return ev
			},
			key:    viewmodel.ToolKey,
			finder: func(f *fakeFinder) ReferenceFinder { return f },
		},
		{
			name:   "an unknown node ID",
			edit:   editEvent,
			key:    func(registry.AuditEvent) string { return "tool:999:c9" },
			finder: func(f *fakeFinder) ReferenceFinder { return f },
		},
		{
			name:   "an adapter with no finder",
			edit:   editEvent,
			key:    viewmodel.ToolKey,
			finder: func(*fakeFinder) ReferenceFinder { return nil },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ev := logEdit(t, tc.edit)
			f := &fakeFinder{refs: []string{"a.go:10"}, ok: true}

			_, sess := New(Deps{State: st, ReferenceFinder: tc.finder(f)})
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

		_, sess := New(Deps{State: st, ReferenceFinder: f})
		got, err := sess.Callers(context.Background(), viewmodel.ToolKey(ev))
		if err != nil {
			t.Fatalf("Callers = %v, want nil", err)
		}
		if got != nil {
			t.Fatalf("callers = %v, want nil", got)
		}
	})
}

func TestCallersOnAReplacedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1"), ReferenceFinder: &fakeFinder{ok: true}})
	h.swap(newState(t, "s2"))

	if _, err := old.Callers(context.Background(), "tool:1:c1"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("Callers on a closed session = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
}
