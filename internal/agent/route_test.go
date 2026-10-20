package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// TestResolveRouteMirrorsReasoningCapability pins the TUI contract: the
// route snapshot carries the same reasoning verdict the chat path gates on
// (chat.go), so display surfaces never advertise an effort value the wire
// would never send (e.g. native Ollama presets).
func TestResolveRouteMirrorsReasoningCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps schema.ProviderCapabilities
		want bool
	}{
		{"reasoning capable", schema.ProviderCapabilities{Reasoning: true}, true},
		{"effort dropped", schema.ProviderCapabilities{Reasoning: false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newTestState(t)
			p := &agenttest.ScriptedProvider{ProviderCaps: tc.caps}
			r := &Runner{
				State: state,
				Now:   func() time.Time { return time.Unix(100, 0) },
				RouteResolver: &staticResolver{
					route: routing.Route{
						Preset: routing.ModelPreset{Provider: "p", Model: "m", Thinking: "high"},
					},
					provider: p,
				},
			}
			r.resolveRoute(context.Background(), NewTask("understand this repo", time.Now()))
			got := state.ActiveRoute()
			if got.ReasoningCapable != tc.want {
				t.Fatalf("ReasoningCapable = %v, want %v (caps.Reasoning=%v)", got.ReasoningCapable, tc.want, tc.caps.Reasoning)
			}
			if got.Thinking != "high" {
				t.Fatalf("Thinking = %q, want high", got.Thinking)
			}
		})
	}
}

// TestResolveRouteRebuildsPackBudgetToWindow verifies that resolveRoute
// rebudgets the context pack from the resolved model window even when the
// route's explicit per-role context budget (MaxRepoContextTokens) is 0 —
// the default for every role. Previously the rebudget ran before the window
// was resolved and was skipped entirely at budget 0, so a pack seeded at
// the flat default stayed there forever.
func TestResolveRouteRebuildsPackBudgetToWindow(t *testing.T) {
	state := newTestState(t)
	// Seed a non-empty pack at the flat default budget, the state a fresh
	// repo scan leaves behind before any route has resolved a window.
	state.UpdateContextPack(func(p contextpack.Pack) contextpack.Pack {
		p.Sections = append(p.Sections, contextpack.Section{
			Kind:    contextpack.SectionRepoCard,
			Title:   "card",
			Content: "a repo card",
		})
		p.TokenUsage.MaxTokens = contextpack.DefaultMaxTokens
		return p
	})

	// The route resolves to a model with a known 200k context window. The
	// explicit per-role ContextBudget is left zero (its default), so only
	// the window-derived budget can apply.
	r := &Runner{
		State: state,
		Now:   func() time.Time { return time.Unix(100, 0) },
		RouteResolver: &staticResolver{
			route: routing.Route{
				Preset: routing.ModelPreset{
					Provider:        "test-provider",
					Model:           "big-model",
					ContextWindow:   200000,
					MaxOutputTokens: 8192,
				},
			},
		},
	}

	task := NewTask("understand this repo", time.Now())
	r.resolveRoute(context.Background(), task)

	got := state.ContextPack().TokenUsage.MaxTokens
	if want := contextpack.BudgetForWindow(200000); got != want {
		t.Fatalf("pack budget after resolveRoute = %d, want %d (BudgetForWindow(200000)); still at DefaultMaxTokens=%d",
			got, want, contextpack.DefaultMaxTokens)
	}
}

// TestResolveRoutePropagatesClampedWindow proves the clamp reaches every
// consumer for free: resolveRoute assigns the clamped pair to route.Window,
// so the pack budget, the turn threshold and the UI denominator all inherit
// it with no further edits.
func TestResolveRoutePropagatesClampedWindow(t *testing.T) {
	state := newTestState(t)
	state.UpdateContextPack(func(p contextpack.Pack) contextpack.Pack {
		p.Sections = append(p.Sections, contextpack.Section{
			Kind:    contextpack.SectionRepoCard,
			Title:   "card",
			Content: "a repo card",
		})
		p.TokenUsage.MaxTokens = contextpack.DefaultMaxTokens
		return p
	})

	r := &Runner{
		State: state,
		Now:   func() time.Time { return time.Unix(100, 0) },
		RouteResolver: &staticResolver{
			route: routing.Route{
				Preset: routing.ModelPreset{
					Name:            "ollama-cloud/deepseek-v4.1-flash:1017",
					Provider:        "ollama-cloud",
					Model:           "deepseek-v4.1-flash:1017",
					ContextWindow:   1048576,
					MaxOutputTokens: 131062,
				},
			},
		},
		ModelProbe: func(provider, model string) ModelProbe {
			return ModelProbe{Window: 65536, MaxOutput: 8192, OK: true}
		},
	}

	_, _, route := r.resolveRoute(context.Background(), NewTask("understand this repo", time.Now()))

	if route.Window != 65536 {
		t.Fatalf("route.Window = %d, want the clamped 65536", route.Window)
	}
	got := state.ContextPack().TokenUsage.MaxTokens
	if want := contextpack.BudgetForWindow(65536); got != want {
		t.Fatalf("pack budget after clamped resolveRoute = %d, want %d (BudgetForWindow(65536))", got, want)
	}
}

// TestResolveRouteBannersClampOncePerSession pins the guard: resolveRoute runs
// every turn and SetNotice replaces unconditionally, so an unguarded emit
// would re-stamp the banner each turn and make it undismissable.
func TestResolveRouteBannersClampOncePerSession(t *testing.T) {
	state := newTestState(t)
	r := &Runner{
		State: state,
		Now:   func() time.Time { return time.Unix(100, 0) },
		RouteResolver: &staticResolver{
			route: routing.Route{
				Preset: routing.ModelPreset{
					Name:            "ollama-cloud/deepseek-v4.1-flash:1017",
					Provider:        "ollama-cloud",
					Model:           "deepseek-v4.1-flash:1017",
					ContextWindow:   1048576,
					MaxOutputTokens: 131062,
				},
			},
		},
		ModelProbe: func(provider, model string) ModelProbe {
			return ModelProbe{Window: 65536, MaxOutput: 8192, OK: true}
		},
	}

	r.resolveRoute(context.Background(), NewTask("first", time.Now()))
	first, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after a clamping resolveRoute")
	}
	if first.Category != session.NoticeProvider {
		t.Fatalf("notice category = %v, want NoticeProvider", first.Category)
	}

	r.resolveRoute(context.Background(), NewTask("second", time.Now()))
	second, ok := state.Notice()
	if !ok {
		t.Fatal("notice disappeared on the second resolveRoute")
	}
	if !second.SetAt.Equal(first.SetAt) {
		t.Fatalf("notice re-stamped on the second turn (SetAt %v → %v); the clampNoticed guard is missing",
			first.SetAt, second.SetAt)
	}
}

func TestResolveRouteRebannersWhenClampChanges(t *testing.T) {
	state := newTestState(t)
	probeWindow := 65536
	r := &Runner{
		State: state,
		Now:   func() time.Time { return time.Unix(100, 0) },
		RouteResolver: &staticResolver{
			route: routing.Route{
				Preset: routing.ModelPreset{
					Name:            "ollama-cloud/deepseek-v4.1-flash:1017",
					Provider:        "ollama-cloud",
					Model:           "deepseek-v4.1-flash:1017",
					ContextWindow:   1048576,
					MaxOutputTokens: 131062,
				},
			},
		},
		ModelProbe: func(provider, model string) ModelProbe {
			return ModelProbe{Window: probeWindow, MaxOutput: 8192, OK: probeWindow > 0}
		},
	}

	r.resolveRoute(context.Background(), NewTask("first", time.Now()))
	first, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after the first clamping resolveRoute")
	}

	probeWindow = 32768
	r.resolveRoute(context.Background(), NewTask("second", time.Now()))
	second, ok := state.Notice()
	if !ok {
		t.Fatal("clamp notice disappeared after the clamp changed")
	}
	if !strings.Contains(second.Message, "32768") {
		t.Fatalf("notice did not re-banner for the new clamp: %q", second.Message)
	}
	if second.SetAt.Equal(first.SetAt) {
		t.Fatal("notice was not re-stamped when the clamp changed")
	}
}

func TestResolveRouteClearsStaleClampNotice(t *testing.T) {
	state := newTestState(t)
	probeWindow := 65536
	r := &Runner{
		State: state,
		Now:   func() time.Time { return time.Unix(100, 0) },
		RouteResolver: &staticResolver{
			route: routing.Route{
				Preset: routing.ModelPreset{
					Name:            "ollama-cloud/deepseek-v4.1-flash:1017",
					Provider:        "ollama-cloud",
					Model:           "deepseek-v4.1-flash:1017",
					ContextWindow:   1048576,
					MaxOutputTokens: 131062,
				},
			},
		},
		ModelProbe: func(provider, model string) ModelProbe {
			return ModelProbe{Window: probeWindow, MaxOutput: 8192, OK: probeWindow > 0}
		},
	}

	r.resolveRoute(context.Background(), NewTask("first", time.Now()))
	if _, ok := state.Notice(); !ok {
		t.Fatal("expected a clamp notice before the probe disappears")
	}

	probeWindow = 0
	r.resolveRoute(context.Background(), NewTask("second", time.Now()))
	if n, ok := state.Notice(); ok {
		t.Fatalf("stale clamp notice survived an unclamped route: %+v", n)
	}
}

func TestResolveRouteBannersOutputOnlyClamp(t *testing.T) {
	state := newTestState(t)
	r := &Runner{
		State: state,
		Now:   func() time.Time { return time.Unix(100, 0) },
		RouteResolver: &staticResolver{
			route: routing.Route{
				Preset: routing.ModelPreset{
					Name:            "provider/model",
					Provider:        "provider",
					Model:           "model",
					ContextWindow:   1048576,
					MaxOutputTokens: 131062,
				},
			},
		},
		ModelProbe: func(provider, model string) ModelProbe {
			return ModelProbe{Window: 1048576, MaxOutput: 8192, OK: true}
		},
	}

	r.resolveRoute(context.Background(), NewTask("first", time.Now()))
	n, ok := state.Notice()
	if !ok {
		t.Fatal("no notice for an output-only clamp")
	}
	if !strings.Contains(n.Message, "output tokens") {
		t.Fatalf("notice does not describe the output clamp: %q", n.Message)
	}
	if strings.Contains(n.Message, "context window") {
		t.Fatalf("output-only clamp notice claims a window change: %q", n.Message)
	}
}

func TestResolveModelLimitsPrefersPreset(t *testing.T) {
	r := &Runner{}
	lim := r.resolveModelLimits(routing.ModelPreset{
		Provider: "anthropic", Model: "claude-sonnet-4-5",
		ContextWindow: 111, MaxOutputTokens: 222,
	})
	if lim.Window != 111 || lim.MaxOutput != 222 {
		t.Fatalf("got (%d, %d), want the preset's (111, 222)", lim.Window, lim.MaxOutput)
	}
}

// TestResolveModelLimitsUsesRemoteDefaults pins the new first-time behavior:
// a preset with no explicit limits gets the documented 1M/128K remote pair
// instead of a third-party table lookup. This is the manual-test regression:
// a 1M model must not be resolved to a 64k table figure.
func TestResolveModelLimitsUsesRemoteDefaults(t *testing.T) {
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider: "openai", Model: "totally-made-up-xyz",
	}, ModelProbe{})
	if lim.Window != routing.DefaultRemoteContextWindow || lim.MaxOutput != routing.DefaultRemoteMaxOutputTokens {
		t.Fatalf("got (%d, %d), want the remote defaults (%d, %d)",
			lim.Window, lim.MaxOutput, routing.DefaultRemoteContextWindow, routing.DefaultRemoteMaxOutputTokens)
	}
	if lim.Clamped {
		t.Fatal("Clamped = true, want false when no provider reported limits")
	}
}

// TestResolveModelLimitsUsesLocalDefaults keeps local endpoints conservative:
// a pulled model's real window comes from the /api/show probe, not a default.
func TestResolveModelLimitsUsesLocalDefaults(t *testing.T) {
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider: "ollama", Model: "qwen2.5-coder:7b", LocalOnly: true,
	}, ModelProbe{})
	if lim.Window != routing.DefaultLocalContextWindow || lim.MaxOutput != routing.DefaultLocalMaxOutputTokens {
		t.Fatalf("got (%d, %d), want the local defaults (%d, %d)",
			lim.Window, lim.MaxOutput, routing.DefaultLocalContextWindow, routing.DefaultLocalMaxOutputTokens)
	}
}

// TestResolveModelLimitsClampsPresetToProbe is the reported bug as a unit
// test: a preset declaring a 1M window against a provider that reports 64k
// must resolve to 64k, not to the preset's claim.
func TestResolveModelLimitsClampsPresetToProbe(t *testing.T) {
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider:        "ollama-cloud",
		Model:           "deepseek-v4.1-flash:1017",
		ContextWindow:   1048576,
		MaxOutputTokens: 131062,
	}, ModelProbe{Window: 65536, MaxOutput: 8192, OK: true})

	if lim.Window != 65536 {
		t.Fatalf("Window = %d, want 65536 (the provider's report)", lim.Window)
	}
	if lim.MaxOutput != 8192 {
		t.Fatalf("MaxOutput = %d, want 8192 (the provider's report)", lim.MaxOutput)
	}
	if !lim.Clamped {
		t.Fatal("Clamped = false, want true")
	}
	if lim.ClampSource != "probe" {
		t.Fatalf("ClampSource = %q, want probe", lim.ClampSource)
	}
	if lim.PresetWindow != 1048576 {
		t.Fatalf("PresetWindow = %d, want the declared 1048576 retained", lim.PresetWindow)
	}
	if lim.ProbeWindow != 65536 {
		t.Fatalf("ProbeWindow = %d, want 65536", lim.ProbeWindow)
	}
}

// TestResolveModelLimitsNeverExpandsBeyondPreset pins the min() semantics: a
// deliberately conservative preset is never raised to a larger provider
// figure.
func TestResolveModelLimitsNeverExpandsBeyondPreset(t *testing.T) {
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider:        "p",
		Model:           "m",
		ContextWindow:   65536,
		MaxOutputTokens: 8192,
	}, ModelProbe{Window: 1048576, MaxOutput: 131062, OK: true})

	if lim.Window != 65536 || lim.MaxOutput != 8192 {
		t.Fatalf("got (%d, %d), want the preset's (65536, 8192)", lim.Window, lim.MaxOutput)
	}
	if lim.Clamped {
		t.Fatal("Clamped = true, want false: the clamp must never expand")
	}
}

// TestResolveModelLimitsProbeMissDoesNotClamp guards the hash-expiry path:
// modelcache.Lookup returns false after a config edit or 24h, and a miss must
// mean "no clamp", never a fuzzy one.
func TestResolveModelLimitsProbeMissDoesNotClamp(t *testing.T) {
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider:      "p",
		Model:         "m",
		ContextWindow: 1048576,
	}, ModelProbe{})

	if lim.Window != 1048576 {
		t.Fatalf("Window = %d, want the preset's 1048576", lim.Window)
	}
	if lim.Clamped {
		t.Fatal("Clamped = true, want false on a probe miss")
	}
}

// TestResolveModelLimitsProbeBeatsDefaults pins the source ranking: a
// provider-reported limit lowers the default pair, because the provider is
// the party that enforces it.
func TestResolveModelLimitsProbeBeatsDefaults(t *testing.T) {
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider: "p",
		Model:    "m",
	}, ModelProbe{Window: 65536, MaxOutput: 8192, OK: true})

	if lim.Window != 65536 {
		t.Fatalf("Window = %d, want 65536 from the probe", lim.Window)
	}
	if lim.MaxOutput != 8192 {
		t.Fatalf("MaxOutput = %d, want 8192 from the probe", lim.MaxOutput)
	}
	if !lim.Clamped {
		t.Fatal("Clamped = false, want true for a provider clamp")
	}
	if lim.ClampSource != "probe" {
		t.Fatalf("ClampSource = %q, want probe", lim.ClampSource)
	}
}

// TestClampedThresholdStaysPositive proves no clamp value can drive the
// derived threshold to zero and silently disable the turnThreshold > 0
// compaction branch. w == 1 is the documented degenerate case: int(0.85) == 0
// and the minDerivedTurnTokens floor is window/2 == 0. No real model has a
// one-token window, so this is a boundary assertion, not a runtime guard.
func TestClampedThresholdStaysPositive(t *testing.T) {
	r := &Runner{}
	for _, w := range []int{2, 3, 7, 8192, 65536} {
		got, _, _ := r.effectiveTurnThreshold(w, w/8, 0)
		if got <= 0 {
			t.Errorf("effectiveTurnThreshold(%d, %d, 0) = %d, want > 0", w, w/8, got)
		}
	}
	if got, _, _ := r.effectiveTurnThreshold(1, 0, 0); got != 0 {
		t.Errorf("effectiveTurnThreshold(1, 0, 0) = %d, want the documented degenerate 0", got)
	}
}

func TestResolveModelLimitsFillsUnsetFieldsIndependently(t *testing.T) {
	tests := []struct {
		name                   string
		preset                 routing.ModelPreset
		wantWindow, wantOutput int
	}{
		{
			name: "explicit context keeps automatic output resolvable",
			preset: routing.ModelPreset{
				Provider:      "ollama",
				Model:         "qwen2.5-coder:7b",
				ContextWindow: 64000,
				LocalOnly:     true,
			},
			wantWindow: 64000,
			wantOutput: routing.DefaultLocalMaxOutputTokens,
		},
		{
			name: "explicit output keeps automatic context resolvable",
			preset: routing.ModelPreset{
				Provider:        "ollama",
				Model:           "qwen2.5-coder:7b",
				MaxOutputTokens: 4096,
				LocalOnly:       true,
			},
			wantWindow: routing.DefaultLocalContextWindow,
			wantOutput: 4096,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := (&Runner{}).resolveModelLimits(tt.preset)
			if lim.Window != tt.wantWindow || lim.MaxOutput != tt.wantOutput {
				t.Fatalf("resolveModelLimits() = (%d, %d), want (%d, %d)", lim.Window, lim.MaxOutput, tt.wantWindow, tt.wantOutput)
			}
		})
	}
}

func TestCopyFromPreservesModelLimitResolution(t *testing.T) {
	source := &Runner{
		ModelProbe: func(provider, model string) ModelProbe {
			return ModelProbe{Window: 128000, MaxOutput: 8192, OK: true}
		},
	}
	target := &Runner{}
	target.CopyFrom(source)

	lim := target.resolveModelLimits(routing.ModelPreset{
		Provider: "test-provider",
		Model:    "model-with-probe-limits",
	})
	if lim.Window != 128000 || lim.MaxOutput != 8192 {
		t.Fatalf("resolveModelLimits after CopyFrom = (%d, %d), want (128000, 8192) via the copied probe", lim.Window, lim.MaxOutput)
	}
}
