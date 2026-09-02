package agent

import (
	"context"
	"testing"

	"marshal/internal/llm/routing"
)

func TestEffectiveTurnThreshold_TracksWindowWhenUnset(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	// 0.85 * 128000 - 4096 = 104704
	got, fb, collapsed := r.effectiveTurnThreshold(128000, 4096, 0)
	if fb {
		t.Fatalf("expected non-fallback path")
	}
	if collapsed {
		t.Fatalf("expected non-collapsed path")
	}
	if got != 104704 {
		t.Fatalf("got %d, want 104704", got)
	}
}

func TestEffectiveTurnThreshold_ExplicitConfigIsHardCeiling(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	got, fb, _ := r.effectiveTurnThreshold(200000, 8192, 50000)
	if fb {
		t.Fatalf("configured should not trigger fallback")
	}
	if got != 50000 {
		t.Fatalf("got %d, want 50000", got)
	}
}

func TestEffectiveTurnThreshold_UnknownWindowFallsBack(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	got, fb, _ := r.effectiveTurnThreshold(0, 0, 0)
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
	_, _, routeSmall := r.resolveRoute(context.Background(), &Task{Class: ClassQuestion})
	small, _, _ := r.effectiveTurnThreshold(routeSmall.Window, routeSmall.MaxOutput, r.MaxTurnContextTokens)
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "large", Model: "large-200k", ContextWindow: 200000, MaxOutputTokens: 4096},
	}}
	_, _, routeLarge := r.resolveRoute(context.Background(), &Task{Class: ClassQuestion})
	large, _, _ := r.effectiveTurnThreshold(routeLarge.Window, routeLarge.MaxOutput, r.MaxTurnContextTokens)

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
	got, fb, _ := r.effectiveTurnThreshold(256000, 262144, 0)
	if fb {
		t.Fatalf("expected non-fallback path, got fallback")
	}
	if got != 185600 {
		t.Fatalf("got %d, want 185600", got)
	}
}

// A known tiny window (e.g. a 4096-token local model) must not collapse to
// the 60000 unknown-window safety net — that is 15× the real window and
// disables compaction exactly when it is needed most. The threshold clamps
// to a window-proportional value instead.
func TestEffectiveTurnThreshold_TinyWindowClampsToWindow(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	got, fb, collapsed := r.effectiveTurnThreshold(4096, 4096, 0)
	if fb {
		t.Fatalf("known window should not trigger the unknown-window fallback")
	}
	if !collapsed {
		t.Fatalf("expected the derivedCollapsed flag for a tiny window")
	}
	if got > 4096 {
		t.Fatalf("threshold = %d, want ≤ 4096 (the real window)", got)
	}
	// 0.85*4096 - min(4096, 512) = 2969, which is above window/2 — the
	// genuine derivation survives.
	if got != 2969 {
		t.Fatalf("threshold = %d, want 2969", got)
	}
}

// An even smaller window whose derivation falls below window/2 clamps to
// window/2 so output still has room.
func TestEffectiveTurnThreshold_MinuteWindowUsesHalfWindow(t *testing.T) {
	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	// 0.85*2048 - 256 = 1484 < 4000 → collapsed; 1484 > 2048/2 → keep 1484.
	got, _, collapsed := r.effectiveTurnThreshold(2048, 2048, 0)
	if !collapsed {
		t.Fatalf("expected the derivedCollapsed flag")
	}
	if got != 1484 {
		t.Fatalf("threshold = %d, want 1484", got)
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
		collapsed          bool
		want               string
	}{
		{256000, 50000, false, "configured"},
		{256000, 0, false, "derived"},
		{0, 0, false, "fallback"},
		// A small window whose derived value fell below minDerivedTurnTokens
		// and clamped to the window-proportional floor must be labeled a
		// fallback, not a derivation.
		{2000, 0, true, "fallback"},
	}
	for _, tc := range cases {
		if got := thresholdSource(tc.window, tc.configured, tc.collapsed); got != tc.want {
			t.Fatalf("thresholdSource(%d, %d, %v) = %q, want %q", tc.window, tc.configured, tc.collapsed, got, tc.want)
		}
	}
}

// Local routes get a bounded tool ceiling when the user has not configured
// one; remote routes keep the unlimited default; explicit config always wins.
func TestEffectiveMaxToolIterations(t *testing.T) {
	localRoute := routing.Route{Preset: routing.ModelPreset{Name: "local", Model: "m", LocalOnly: true}}
	remoteRoute := routing.Route{Preset: routing.ModelPreset{Name: "remote", Model: "m"}}

	r := NewRunner(nil, nil, nil, newTestState(t), "m")
	if got := r.effectiveMaxToolIterations(localRoute); got != LocalDefaultMaxToolIterations {
		t.Fatalf("local route = %d, want %d", got, LocalDefaultMaxToolIterations)
	}
	if got := r.effectiveMaxToolIterations(remoteRoute); got != 0 {
		t.Fatalf("remote route = %d, want 0 (unlimited)", got)
	}

	r.MaxToolIterations = 7
	if got := r.effectiveMaxToolIterations(localRoute); got != 7 {
		t.Fatalf("explicit config on local route = %d, want 7 (config wins)", got)
	}
	if got := r.effectiveMaxToolIterations(remoteRoute); got != 7 {
		t.Fatalf("explicit config on remote route = %d, want 7", got)
	}
}
