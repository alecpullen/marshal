package service

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/interaction"
)

// These tests pin what an unclassified error means at the service, which the
// conformance harness cannot assert: F-SEC-37 makes such an error opaque over
// the wire, so the shared cases assert only that the call fails with no agent
// code. The identity below is the in-process contract a caller switching on
// errors.Is sees, and it is the half the conformance arm cannot carry (P3 plan
// 6b).

// TestReviewRefusesARangeWithABase pins that the refusal is the service's own
// sentinel, so a caller can detect it rather than matching text.
func TestReviewRefusesARangeWithABase(t *testing.T) {
	_, sess := New(Deps{
		State:  newState(t, "s1"),
		Review: func(context.Context, acpwire.ReviewParams) error { return nil },
	})

	if err := sess.Review(context.Background(), acpwire.ReviewParams{Range: "a..b", Base: "main"}); !errors.Is(err, errRangeAndBase) {
		t.Fatalf("Review with a range and a base = %v, want errRangeAndBase", err)
	}
}

// TestPromptPassesUnclassifiedErrorsThrough pins the other half of decision 4:
// an error the agent did not classify reaches the caller as the same value.
func TestPromptPassesUnclassifiedErrorsThrough(t *testing.T) {
	fake := newFakeRunner()
	plain := errors.New("boom")
	fake.err = plain
	_, sess := New(Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }})

	if _, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"}); err != plain {
		t.Fatalf("Prompt err = %v, want the same error value back", err)
	}
}

// TestSetInteractionReturnsTheSettersError pins that a refused interactive
// change is the runtime's error, unchanged, rather than a coded refusal.
func TestSetInteractionReturnsTheSettersError(t *testing.T) {
	refused := errors.New("interaction refused")
	_, sess := New(Deps{
		State: newState(t, "s1"),
		SetInteraction: func(interaction.Patch) (interaction.State, error) {
			return interaction.State{}, refused
		},
	})

	mode := "act"
	if _, err := sess.SetInteraction(context.Background(), acpwire.InteractionPatch{Mode: &mode}); !errors.Is(err, refused) {
		t.Fatalf("SetInteraction err = %v, want %v", err, refused)
	}
}

// TestAnUnclassifiedWorkErrorComesBackUnchanged pins the other half of the
// stale-cursor rule: only a stale cursor is coded, and every other failure is
// the work service's own error.
func TestAnUnclassifiedWorkErrorComesBackUnchanged(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeWork{pageErr: boom}
	_, sess := New(Deps{State: newState(t, "s1"), Work: f})

	if _, err := sess.ListWork(context.Background(), acpwire.WorkQuery{View: "history"}); !errors.Is(err, boom) {
		t.Fatalf("ListWork err = %v, want the work service's error unchanged", err)
	}
}
