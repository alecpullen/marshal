package session

import (
	"context"
	"testing"
	"time"

	"marshal/internal/pubsub"
)

func TestSkillGateAppliesThresholdMatrix(t *testing.T) {
	state := newTestState()

	cases := []struct {
		name      string
		threshold int
		window    int
		want      SkillGateAction
	}{
		{"unknown window prompts", 131072, 0, SkillGatePrompt},
		{"large window proceeds", 131072, 262144, SkillGateProceed},
		{"window equal to threshold prompts", 131072, 131072, SkillGatePrompt},
		{"zero threshold disables", 0, 0, SkillGateProceed},
	}
	for _, tc := range cases {
		if got := state.SkillGateApplies("x", tc.threshold, tc.window); got != tc.want {
			t.Errorf("SkillGateApplies(%q, %d, %d) = %v, want %v", tc.name, tc.threshold, tc.window, got, tc.want)
		}
	}

	// Session-disabled override: gate proceeds even with a small window.
	state.SkillGateSetEnabled(false)
	if got := state.SkillGateApplies("x", 131072, 0); got != SkillGateProceed {
		t.Errorf("session-disabled gate: SkillGateApplies = %v, want Proceed", got)
	}
}

func TestSkillGateStickyDenyCycle(t *testing.T) {
	state := newTestState()
	const threshold = 131072
	const window = 0 // unknown window: gate fires

	// Deny 1: sticky deny active.
	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGateDenied {
		t.Fatalf("after 1st deny: got %v, want Denied", got)
	}
	// Deny 2: still sticky deny.
	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGateDenied {
		t.Fatalf("after 2nd deny: got %v, want Denied", got)
	}
	// Deny 3: n%3==0 → bounded re-prompt.
	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGatePrompt {
		t.Fatalf("after 3rd deny (n%%3==0): got %v, want Prompt", got)
	}
	// Deny 4: back to sticky deny.
	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGateDenied {
		t.Fatalf("after 4th deny: got %v, want Denied", got)
	}
	// Deny 5: still sticky deny.
	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGateDenied {
		t.Fatalf("after 5th deny: got %v, want Denied", got)
	}
	// Deny 6: n%3==0 → re-prompt again.
	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGatePrompt {
		t.Fatalf("after 6th deny (n%%3==0): got %v, want Prompt", got)
	}
}

func TestSkillGateAllowTransitions(t *testing.T) {
	state := newTestState()
	const threshold = 131072
	const window = 0

	// Per-skill allow: "x" proceeds, "y" still prompts.
	state.SkillGateRecordAllow("x", false)
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGateProceed {
		t.Errorf("after per-skill allow: SkillGateApplies(x) = %v, want Proceed", got)
	}
	if got := state.SkillGateApplies("y", threshold, window); got != SkillGatePrompt {
		t.Errorf("after per-skill allow: SkillGateApplies(y) = %v, want Prompt", got)
	}

	// Allow-all: everything proceeds and the gate is disabled.
	state.SkillGateRecordAllow("y", true)
	if got := state.SkillGateApplies("z", threshold, window); got != SkillGateProceed {
		t.Errorf("after allow-all: SkillGateApplies(z) = %v, want Proceed", got)
	}
	if state.SkillGateEnabled() {
		t.Error("SkillGateEnabled() = true after allow-all, want false")
	}

	// Allow clears the deny counter: deny twice, allow, then no denied
	// entry for "w" remains (the allow entry itself is expected).
	state.SkillGateRecordDeny("w")
	state.SkillGateRecordDeny("w")
	state.SkillGateRecordAllow("w", false)
	for _, d := range state.SkillGateDecisions() {
		if d.Skill == "w" && !d.Allowed {
			t.Errorf("deny counter resurfaced after allow: %+v", d)
		}
	}
}

func TestSkillGateSetEnabledTrueClearsDecisions(t *testing.T) {
	state := newTestState()
	state.SkillGateRecordAllow("a", false)
	state.SkillGateRecordDeny("b")
	state.SkillGateRecordDeny("b")

	state.SkillGateSetEnabled(true)
	if got := state.SkillGateDecisions(); len(got) != 0 {
		t.Errorf("SkillGateDecisions() after re-enable = %+v, want empty", got)
	}
	if !state.SkillGateEnabled() {
		t.Error("SkillGateEnabled() = false after SetEnabled(true), want true")
	}
}

func TestSkillGateDecisionsSortedUnion(t *testing.T) {
	state := newTestState()
	state.SkillGateRecordDeny("zeta")
	state.SkillGateRecordDeny("zeta")
	state.SkillGateRecordAllow("alpha", false)

	got := state.SkillGateDecisions()
	if len(got) != 2 {
		t.Fatalf("SkillGateDecisions() = %+v, want 2 entries", got)
	}
	if got[0].Skill != "alpha" || !got[0].Allowed || got[0].Count != 0 {
		t.Errorf("decisions[0] = %+v, want alpha allowed count 0", got[0])
	}
	if got[1].Skill != "zeta" || got[1].Allowed || got[1].Count != 2 {
		t.Errorf("decisions[1] = %+v, want zeta denied count 2", got[1])
	}
}

func TestSkillGateClearDecision(t *testing.T) {
	state := newTestState()
	const threshold = 131072
	const window = 0

	state.SkillGateRecordDeny("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGateDenied {
		t.Fatalf("after deny: got %v, want Denied", got)
	}

	state.SkillGateClearDecision("x")
	if got := state.SkillGateApplies("x", threshold, window); got != SkillGatePrompt {
		t.Errorf("after ClearDecision: SkillGateApplies = %v, want Prompt", got)
	}
}

func TestPendingSkillGateRespondOnceOnly(t *testing.T) {
	p := &PendingSkillGate{Skill: "demo"}
	// Buffered so the non-blocking send lands before the close; the
	// runner's real usage has a concurrent receiver, this mirrors the
	// delivered-then-closed contract deterministically.
	ch := make(chan SkillGateChoice, 1)
	p.ResponseChan = ch

	p.Respond(SkillGateAllowSkill)
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first value was delivered")
		}
		if got != SkillGateAllowSkill {
			t.Errorf("delivered %v, want SkillGateAllowSkill", got)
		}
	default:
		t.Fatal("first Respond did not deliver a value")
	}
	// The channel must be closed after the send.
	if _, ok := <-ch; ok {
		t.Error("channel not closed after Respond")
	}

	// Second call is a no-op: reading a closed channel yields the zero
	// value with ok=false, and no second value can appear.
	p.Respond(SkillGateDeny)
	select {
	case got, ok := <-ch:
		if ok {
			t.Errorf("second Respond delivered %v on a closed channel", got)
		}
	default:
		t.Error("second Respond changed channel state")
	}

	// An unbuffered channel with no receiver must not deadlock: the
	// non-blocking send drops the value and the close still unblocks
	// later readers with ok=false.
	p2 := &PendingSkillGate{Skill: "demo2"}
	p2.ResponseChan = make(chan SkillGateChoice)
	done := make(chan struct{})
	go func() {
		_, ok := <-p2.ResponseChan
		close(done)
		_ = ok
	}()
	p2.Respond(SkillGateAllowOnce)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Respond deadlocked on an unbuffered channel with no receiver")
	}
}

func TestSetPendingSkillGatePublishesEvent(t *testing.T) {
	state := newTestState()
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	state.SetEventBroker(broker)

	resp := make(chan SkillGateChoice)
	state.SetPendingSkillGate(&PendingSkillGate{
		Skill:        "demo",
		Description:  "a demo skill",
		Reason:       "small window",
		ResponseChan: resp,
	})
	select {
	case ev := <-ch:
		if ev.Type != EventPendingSkillGateChanged || ev.Payload.PendingSkillGate == nil {
			t.Fatalf("event = %+v", ev)
		}
		snap := ev.Payload.PendingSkillGate
		if snap.Skill != "demo" || snap.Description != "a demo skill" || snap.Reason != "small window" {
			t.Errorf("snapshot = %+v", snap)
		}
		if snap.ResponseChan != resp {
			t.Error("snapshot ResponseChan differs from the original")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for skill-gate set event")
	}

	state.SetPendingSkillGate(nil)
	select {
	case ev := <-ch:
		if ev.Type != EventPendingSkillGateChanged || ev.Payload.PendingSkillGate != nil {
			t.Fatalf("clear event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for skill-gate clear event")
	}
}

func TestResolvePendingForShutdownDeniesSkillGate(t *testing.T) {
	state := newTestState()

	// Buffered so the non-blocking deny send lands before the close.
	resp := make(chan SkillGateChoice, 1)
	state.SetPendingSkillGate(&PendingSkillGate{Skill: "demo", ResponseChan: resp})

	state.ResolvePendingForShutdown()

	if got := state.PendingSkillGate(); got != nil {
		t.Fatalf("PendingSkillGate() = %+v after shutdown resolve, want nil", got)
	}
	select {
	case got, ok := <-resp:
		if !ok {
			t.Fatal("channel closed without delivering a choice")
		}
		if got != SkillGateDeny {
			t.Errorf("delivered %v, want SkillGateDeny", got)
		}
	default:
		t.Fatal("shutdown did not deliver a deny choice")
	}
	// The channel must be closed after the send.
	if _, ok := <-resp; ok {
		t.Error("channel not closed after shutdown deny")
	}
}
