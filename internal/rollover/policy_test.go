package rollover

import "testing"

func TestDecide_ContextPercent_FiresAtThreshold(t *testing.T) {
	// 50% of a 10000-token window = 5000 tokens.
	p := Policy{Mode: PolicyContextPercent, ContextPercent: 50, TurnCount: 0}
	s := Signal{Tokens: 5000, ContextWindow: 10000}
	if !Decide(p, s) {
		t.Error("expected Decide to fire at 50% threshold")
	}
}

func TestDecide_ContextPercent_FiresAboveThreshold(t *testing.T) {
	p := Policy{Mode: PolicyContextPercent, ContextPercent: 50, TurnCount: 0}
	s := Signal{Tokens: 7500, ContextWindow: 10000}
	if !Decide(p, s) {
		t.Error("expected Decide to fire above 50% threshold")
	}
}

func TestDecide_ContextPercent_DoesNotFireBelowThreshold(t *testing.T) {
	p := Policy{Mode: PolicyContextPercent, ContextPercent: 50, TurnCount: 0}
	s := Signal{Tokens: 4000, ContextWindow: 10000}
	if Decide(p, s) {
		t.Error("expected Decide not to fire below 50% threshold")
	}
}

func TestDecide_ContextPercent_UnknownWindow(t *testing.T) {
	// Unknown window (ContextWindow == 0) disables the percentage rule.
	p := Policy{Mode: PolicyContextPercent, ContextPercent: 50, TurnCount: 0}
	s := Signal{Tokens: 50000, ContextWindow: 0}
	if Decide(p, s) {
		t.Error("expected Decide not to fire with unknown window")
	}
}

func TestDecide_ContextPercent_UnknownWindow_TurnBackstopFires(t *testing.T) {
	// Turn-count backstop still fires even with unknown window.
	p := Policy{Mode: PolicyContextPercent, ContextPercent: 50, TurnCount: 10}
	s := Signal{Tokens: 50000, ContextWindow: 0, TurnsInGeneration: 10}
	if !Decide(p, s) {
		t.Error("expected Decide to fire via turn backstop with unknown window")
	}
}

func TestDecide_ContextPercent_TurnBackstopFires(t *testing.T) {
	// Turn-count backstop fires even when context percent hasn't been reached.
	p := Policy{Mode: PolicyContextPercent, ContextPercent: 80, TurnCount: 5}
	s := Signal{Tokens: 1000, ContextWindow: 10000, TurnsInGeneration: 5}
	if !Decide(p, s) {
		t.Error("expected Decide to fire via turn backstop")
	}
}

func TestDecide_TurnCount_FiresAtThreshold(t *testing.T) {
	p := Policy{Mode: PolicyTurnCount, TurnCount: 10}
	s := Signal{TurnsInGeneration: 10}
	if !Decide(p, s) {
		t.Error("expected Decide to fire at turn count threshold")
	}
}

func TestDecide_TurnCount_FiresAboveThreshold(t *testing.T) {
	p := Policy{Mode: PolicyTurnCount, TurnCount: 10}
	s := Signal{TurnsInGeneration: 15}
	if !Decide(p, s) {
		t.Error("expected Decide to fire above turn count threshold")
	}
}

func TestDecide_TurnCount_DoesNotFireBelowThreshold(t *testing.T) {
	p := Policy{Mode: PolicyTurnCount, TurnCount: 10}
	s := Signal{TurnsInGeneration: 5}
	if Decide(p, s) {
		t.Error("expected Decide not to fire below turn count threshold")
	}
}

func TestDecide_TurnCount_ZeroTurnCountNeverFires(t *testing.T) {
	p := Policy{Mode: PolicyTurnCount, TurnCount: 0}
	s := Signal{TurnsInGeneration: 100}
	if Decide(p, s) {
		t.Error("expected Decide not to fire with zero turn count")
	}
}

func TestDecide_CallerCheckpoint_FiresWhenRequested(t *testing.T) {
	p := Policy{Mode: PolicyCallerCheckpoint, TurnCount: 0}
	s := Signal{Requested: true}
	if !Decide(p, s) {
		t.Error("expected Decide to fire when requested")
	}
}

func TestDecide_CallerCheckpoint_DoesNotFireWhenNotRequested(t *testing.T) {
	p := Policy{Mode: PolicyCallerCheckpoint, TurnCount: 0}
	s := Signal{Requested: false}
	if Decide(p, s) {
		t.Error("expected Decide not to fire when not requested")
	}
}

func TestDecide_CallerCheckpoint_FiresAtTurnBackstop(t *testing.T) {
	p := Policy{Mode: PolicyCallerCheckpoint, TurnCount: 10}
	s := Signal{TurnsInGeneration: 10, Requested: false}
	if !Decide(p, s) {
		t.Error("expected Decide to fire at turn backstop")
	}
}

func TestDecide_CallerCheckpoint_FiresWhenRequestedAndBelowBackstop(t *testing.T) {
	p := Policy{Mode: PolicyCallerCheckpoint, TurnCount: 10}
	s := Signal{TurnsInGeneration: 3, Requested: true}
	if !Decide(p, s) {
		t.Error("expected Decide to fire when requested even below backstop")
	}
}

func TestDecide_UnknownPolicyNeverFires(t *testing.T) {
	p := Policy{Mode: "unknown_mode"}
	s := Signal{Tokens: 999999, ContextWindow: 1000, TurnsInGeneration: 999, Requested: true}
	if Decide(p, s) {
		t.Error("expected Decide never to fire for unknown policy")
	}
}

func TestDecide_EmptyModeNeverFires(t *testing.T) {
	p := Policy{Mode: ""}
	s := Signal{Tokens: 999999, ContextWindow: 1000, TurnsInGeneration: 999, Requested: true}
	if Decide(p, s) {
		t.Error("expected Decide never to fire for empty mode")
	}
}
