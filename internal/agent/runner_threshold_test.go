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

// A modern model can advertise a max output larger than 12.5% of its
// window (kimi-for-coding-highspeed: 256000 window, 262144 max output).
// Subtracting it whole drove the budget negative, tripped
// minDerivedTurnTokens, and fell back to the 60k safety net.
func TestEffectiveTurnThreshold_CapsOutputReserve(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	// reserve = min(262144, 256000/8=32000) = 32000
	// 0.85 * 256000 = 217600; 217600 - 32000 = 185600
	got, fb := r.effectiveTurnThreshold(256000, 262144, 0)
	if fb {
		t.Fatalf("expected non-fallback path, got fallback")
	}
	if got != 185600 {
		t.Fatalf("got %d, want 185600", got)
	}
}

// NewRunner must not pre-seed the ceiling: a seeded value is
// indistinguishable from an explicit user setting, which made the
// window-derived branch unreachable in production.
func TestNewRunnerLeavesTurnCeilingUnset(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	if r.MaxTurnContextTokens != 0 {
		t.Fatalf("NewRunner seeded MaxTurnContextTokens = %d, want 0 (0 = derive from window)", r.MaxTurnContextTokens)
	}
}

func TestThresholdSource(t *testing.T) {
	cases := []struct {
		window, configured int
		want               string
	}{
		{256000, 50000, "configured"},
		{256000, 0, "derived"},
		{0, 0, "fallback"},
	}
	for _, tc := range cases {
		if got := thresholdSource(tc.window, tc.configured); got != tc.want {
			t.Fatalf("thresholdSource(%d, %d) = %q, want %q", tc.window, tc.configured, got, tc.want)
		}
	}
}
