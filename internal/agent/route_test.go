package agent

import (
	"context"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider/limits"
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

func TestResolveModelLimitsUsesProvidedTable(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"test-provider/model-with-cache-limits": {ContextWindow: 128000, MaxOutputTokens: 8192},
	})
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider: "test-provider",
		Model:    "model-with-cache-limits",
	}, &tbl, ModelProbe{}, nil)
	if lim.Window != 128000 || lim.MaxOutput != 8192 {
		t.Fatalf("ResolveModelLimits() = (%d, %d), want (128000, 8192)", lim.Window, lim.MaxOutput)
	}
}

func TestResolveModelLimitsUsesTable(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"anthropic/claude-sonnet-4.5": {ContextWindow: 200000, MaxOutputTokens: 64000},
	})
	r := &Runner{LimitsTable: &tbl}
	lim := r.resolveModelLimits(routing.ModelPreset{
		Provider: "anthropic", Model: "claude-sonnet-4-5-20250929",
	})
	if lim.Window != 200000 || lim.MaxOutput != 64000 {
		t.Fatalf("got (%d, %d), want (200000, 64000) via the variant match", lim.Window, lim.MaxOutput)
	}
}

func TestResolveModelLimitsFallsBackToCatalog(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{})
	r := &Runner{LimitsTable: &tbl}
	lim := r.resolveModelLimits(routing.ModelPreset{
		Provider: "ollama", Model: "qwen2.5-coder:7b",
	})
	if lim.Window != 32768 {
		t.Fatalf("window = %d, want 32768 from the local catalog", lim.Window)
	}
}

func TestResolveModelLimitsNilTableDoesNotPanic(t *testing.T) {
	r := &Runner{}
	lim := r.resolveModelLimits(routing.ModelPreset{
		Provider: "openai", Model: "totally-made-up-xyz",
	})
	// Unknown models fall back to the catalog's conservative defaults.
	if lim.Window != 8192 || lim.MaxOutput != 4096 {
		t.Fatalf("got (%d, %d), want (8192, 4096) for an unknown model", lim.Window, lim.MaxOutput)
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
	}, nil, ModelProbe{Window: 65536, MaxOutput: 8192, OK: true}, nil)

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
	}, nil, ModelProbe{Window: 1048576, MaxOutput: 131062, OK: true}, nil)

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
	}, nil, ModelProbe{}, nil)

	if lim.Window != 1048576 {
		t.Fatalf("Window = %d, want the preset's 1048576", lim.Window)
	}
	if lim.Clamped {
		t.Fatal("Clamped = true, want false on a probe miss")
	}
}

// TestResolveModelLimitsProbeBeatsTable pins the source ranking: the probe is
// a statement by the party enforcing the limit, the table is a cross-provider
// inference.
func TestResolveModelLimitsProbeBeatsTable(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"p/m": {ContextWindow: 1048576, MaxOutputTokens: 131062},
	})
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider:      "p",
		Model:         "m",
		ContextWindow: 1048576,
	}, &tbl, ModelProbe{Window: 65536, MaxOutput: 8192, OK: true}, nil)

	if lim.Window != 65536 {
		t.Fatalf("Window = %d, want 65536 from the probe", lim.Window)
	}
	if lim.ClampSource != "probe" {
		t.Fatalf("ClampSource = %q, want probe", lim.ClampSource)
	}
}

// TestResolveModelLimitsVariantMatchIsSilent pins the label that suppresses
// the user-facing banner: a MatchVariant hit is an inference across
// differently-named entries, not a provider statement.
func TestResolveModelLimitsVariantMatchIsSilent(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"anthropic/claude-sonnet-4.5": {ContextWindow: 200000},
	})
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider:      "anthropic",
		Model:         "claude-sonnet-4-5-20250929",
		ContextWindow: 400000,
	}, &tbl, ModelProbe{}, nil)

	if lim.Window != 200000 {
		t.Fatalf("Window = %d, want 200000 via the variant match", lim.Window)
	}
	if lim.ClampSource != "table-variant" {
		t.Fatalf("ClampSource = %q, want table-variant", lim.ClampSource)
	}
}

// TestResolveModelLimitsCatalogIsNeverClamped guards the failure mode that
// would cripple every unlisted model: catalog.Lookup returns conservative
// defaults (8192/4096) for unknown models, and clamping to a guess is wrong.
func TestResolveModelLimitsCatalogIsNeverClamped(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{})
	lim := ResolveModelLimits(routing.ModelPreset{
		Provider: "ollama",
		Model:    "qwen2.5-coder:7b",
	}, &tbl, ModelProbe{}, nil)

	if lim.Window != 32768 {
		t.Fatalf("Window = %d, want 32768 from the local catalog", lim.Window)
	}
	if lim.Clamped {
		t.Fatal("Clamped = true, want false: the catalog is never a clamp source")
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
			},
			wantWindow: 64000,
			wantOutput: 8192,
		},
		{
			name: "explicit output keeps automatic context resolvable",
			preset: routing.ModelPreset{
				Provider:        "ollama",
				Model:           "qwen2.5-coder:7b",
				MaxOutputTokens: 4096,
			},
			wantWindow: 32768,
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
	tbl := limits.NewTable(map[string]limits.Limit{
		"test-provider/model-with-cache-limits": {ContextWindow: 128000, MaxOutputTokens: 8192},
	})
	source := &Runner{LimitsTable: &tbl}
	target := &Runner{}
	target.CopyFrom(source)

	lim := target.resolveModelLimits(routing.ModelPreset{
		Provider: "test-provider",
		Model:    "model-with-cache-limits",
	})
	if lim.Window != 128000 || lim.MaxOutput != 8192 {
		t.Fatalf("resolveModelLimits after CopyFrom = (%d, %d), want (128000, 8192)", lim.Window, lim.MaxOutput)
	}
}
