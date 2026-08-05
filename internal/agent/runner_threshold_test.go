package agent

import (
	"testing"

	"marshal/internal/llm/routing"
)

func TestEffectiveTurnThreshold_TracksWindowWhenUnset(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	// 0.85 * 128000 - 4096 = 104704
	got, fb := r.effectiveTurnThreshold(128000, 4096, 0)
	if fb {
		t.Fatalf("expected non-fallback path")
	}
	if got != 104704 {
		t.Fatalf("got %d, want 104704", got)
	}
}

func TestEffectiveTurnThreshold_ExplicitConfigIsHardCeiling(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	got, fb := r.effectiveTurnThreshold(200000, 8192, 50000)
	if fb {
		t.Fatalf("configured should not trigger fallback")
	}
	if got != 50000 {
		t.Fatalf("got %d, want 50000", got)
	}
}

func TestEffectiveTurnThreshold_UnknownWindowFallsBack(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	got, fb := r.effectiveTurnThreshold(0, 0, 0)
	if !fb {
		t.Fatalf("expected fallback flag")
	}
	if got != DefaultMaxTurnContextTokens {
		t.Fatalf("got %d, want %d", got, DefaultMaxTurnContextTokens)
	}
}

// TestThresholdNotStickyAcrossTurns asserts D1's headline fix: resolveRoute
// must NOT mutate r.MaxTurnContextTokens (the sticky bit). Calling it on a
// 32k model then a 200k model with the same configured ceiling leaves the
// configured ceiling intact AND lets the per-turn threshold derive from each
// model's window independently.
func TestThresholdNotStickyAcrossTurns(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	r.MaxTurnContextTokens = 0 // no user ceiling — derive every time

	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "small", Model: "small-32k", ContextWindow: 32000, MaxOutputTokens: 2048},
	}}
	_, _, routeSmall := r.resolveRoute(&Task{Class: ClassQuestion})
	small, _ := r.effectiveTurnThreshold(routeSmall.Window, routeSmall.MaxOutput, r.MaxTurnContextTokens)

	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "large", Model: "large-200k", ContextWindow: 200000, MaxOutputTokens: 4096},
	}}
	_, _, routeLarge := r.resolveRoute(&Task{Class: ClassQuestion})
	large, _ := r.effectiveTurnThreshold(routeLarge.Window, routeLarge.MaxOutput, r.MaxTurnContextTokens)

	if small <= 0 {
		t.Fatalf("small model threshold = %d, want > 0", small)
	}
	if large <= 100000 {
		t.Fatalf("large model threshold = %d, want > 100000 (the 32k window must not poison the 200k turn)", large)
	}
}
