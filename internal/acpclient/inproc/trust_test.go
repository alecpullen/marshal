package inproc

import (
	"context"
	"reflect"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/trust"
)

// TestDecideTrustPassesTheDecision pins the conversion: each of the three wire
// decisions reaches the runtime's trustDecide as the matching trust.Decision.
func TestDecideTrustPassesTheDecision(t *testing.T) {
	dir := t.TempDir()
	for _, decision := range []acpwire.TrustDecision{
		acpwire.TrustPermanent,
		acpwire.TrustSession,
		acpwire.TrustDecline,
	} {
		var got []trust.Decision
		h, _ := New(Deps{
			State:       newState(t, "s1"),
			TrustPrompt: dir,
			DecideTrust: func(d trust.Decision) { got = append(got, d) },
		})
		if err := h.DecideTrust(context.Background(), dir, decision); err != nil {
			t.Fatalf("DecideTrust(%q): %v", decision, err)
		}
		want := []trust.Decision{trust.Decision(decision)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("DecideTrust(%q) recorded %v, want %v", decision, got, want)
		}
	}
}

// TestDecideTrustRefusesWhatIsNotPending pins the three refusals: with nothing
// pending, for another directory, with a decision the seam does not carry, and
// without a recorder, DecideTrust fails and calls nothing.
func TestDecideTrustRefusesWhatIsNotPending(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()

	calls := 0
	decide := func(trust.Decision) { calls++ }

	tests := []struct {
		name     string
		deps     Deps
		dir      string
		decision acpwire.TrustDecision
		want     string
	}{
		{
			name:     "no prompt",
			deps:     Deps{State: newState(t, "s1"), DecideTrust: decide},
			dir:      dir,
			decision: acpwire.TrustPermanent,
			want:     "inproc: no trust decision is pending",
		},
		{
			name:     "another directory",
			deps:     Deps{State: newState(t, "s1"), TrustPrompt: dir, DecideTrust: decide},
			dir:      other,
			decision: acpwire.TrustPermanent,
			want:     "inproc: no trust decision is pending for " + other,
		},
		{
			name:     "unknown decision",
			deps:     Deps{State: newState(t, "s1"), TrustPrompt: dir, DecideTrust: decide},
			dir:      dir,
			decision: acpwire.TrustDecision("maybe"),
			want:     `inproc: unknown trust decision "maybe"`,
		},
		{
			name:     "nil DecideTrust",
			deps:     Deps{State: newState(t, "s1"), TrustPrompt: dir},
			dir:      dir,
			decision: acpwire.TrustPermanent,
			want:     "inproc: no trust decision is pending",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := calls
			h, _ := New(tt.deps)
			err := h.DecideTrust(context.Background(), tt.dir, tt.decision)
			if err == nil {
				t.Fatalf("DecideTrust: no error, want %q", tt.want)
			}
			if got := err.Error(); got != tt.want {
				t.Errorf("DecideTrust error = %q, want %q", got, tt.want)
			}
			if calls != before {
				t.Errorf("DecideTrust called the recorder %d times, want none", calls-before)
			}
		})
	}
}

// TestTrustPromptIsTheDeps pins the read: the host reports the prompt it was
// built with, and "" when it was built with none.
func TestTrustPromptIsTheDeps(t *testing.T) {
	dir := t.TempDir()

	withPrompt, _ := New(Deps{State: newState(t, "s1"), TrustPrompt: dir})
	if got := withPrompt.TrustPrompt(); got != dir {
		t.Errorf("TrustPrompt() = %q, want %q", got, dir)
	}

	without, _ := New(Deps{State: newState(t, "s1")})
	if got := without.TrustPrompt(); got != "" {
		t.Errorf("TrustPrompt() = %q, want \"\"", got)
	}
}

// TestToolsReadsTheRuntime pins both halves of the read: without Deps.Tools the
// session reports no registry, and with it the session reports the runtime's
// names, copied so a caller cannot change what the next read returns. A closed
// session keeps answering, as Status does.
func TestToolsReadsTheRuntime(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})
	if names, ok := sess.Tools(); ok {
		t.Errorf("Tools() = %v, true without Deps.Tools; want false", names)
	}

	names := []string{"file.edit", "file.read", "shell.run"}
	h, sess := New(Deps{
		State: newState(t, "s1"),
		Tools: func() ([]string, bool) { return names, true },
	})
	got, ok := sess.Tools()
	if !ok {
		t.Fatalf("Tools() reported no registry, want the runtime's names")
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("Tools() = %v, want %v", got, names)
	}
	got[0] = "changed"
	if next, _ := sess.Tools(); !reflect.DeepEqual(next, names) {
		t.Errorf("Tools() after the caller changed its result = %v, want %v", next, names)
	}

	// A closed session keeps answering with its final value (C4).
	h.current.close()
	if after, ok := sess.Tools(); !ok || !reflect.DeepEqual(after, names) {
		t.Errorf("Tools() on a closed session = %v, %v; want %v, true", after, ok, names)
	}
}
