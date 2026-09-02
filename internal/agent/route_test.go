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
	window, maxOut := r.resolveModelLimits(routing.ModelPreset{
		Provider: "anthropic", Model: "claude-sonnet-4-5",
		ContextWindow: 111, MaxOutputTokens: 222,
	})
	if window != 111 || maxOut != 222 {
		t.Fatalf("got (%d, %d), want the preset's (111, 222)", window, maxOut)
	}
}

func TestResolveModelLimitsUsesProvidedTable(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"test-provider/model-with-cache-limits": {ContextWindow: 128000, MaxOutputTokens: 8192},
	})
	window, maxOutput := ResolveModelLimits(routing.ModelPreset{
		Provider: "test-provider",
		Model:    "model-with-cache-limits",
	}, &tbl, nil)
	if window != 128000 || maxOutput != 8192 {
		t.Fatalf("ResolveModelLimits() = (%d, %d), want (128000, 8192)", window, maxOutput)
	}
}

func TestResolveModelLimitsUsesTable(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"anthropic/claude-sonnet-4.5": {ContextWindow: 200000, MaxOutputTokens: 64000},
	})
	r := &Runner{LimitsTable: &tbl}
	window, maxOut := r.resolveModelLimits(routing.ModelPreset{
		Provider: "anthropic", Model: "claude-sonnet-4-5-20250929",
	})
	if window != 200000 || maxOut != 64000 {
		t.Fatalf("got (%d, %d), want (200000, 64000) via the variant match", window, maxOut)
	}
}

func TestResolveModelLimitsFallsBackToCatalog(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{})
	r := &Runner{LimitsTable: &tbl}
	window, _ := r.resolveModelLimits(routing.ModelPreset{
		Provider: "ollama", Model: "qwen2.5-coder:7b",
	})
	if window != 32768 {
		t.Fatalf("window = %d, want 32768 from the local catalog", window)
	}
}

func TestResolveModelLimitsNilTableDoesNotPanic(t *testing.T) {
	r := &Runner{}
	window, maxOut := r.resolveModelLimits(routing.ModelPreset{
		Provider: "openai", Model: "totally-made-up-xyz",
	})
	// Unknown models fall back to the catalog's conservative defaults.
	if window != 8192 || maxOut != 4096 {
		t.Fatalf("got (%d, %d), want (8192, 4096) for an unknown model", window, maxOut)
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
			window, output := (&Runner{}).resolveModelLimits(tt.preset)
			if window != tt.wantWindow || output != tt.wantOutput {
				t.Fatalf("resolveModelLimits() = (%d, %d), want (%d, %d)", window, output, tt.wantWindow, tt.wantOutput)
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

	window, maxOutput := target.resolveModelLimits(routing.ModelPreset{
		Provider: "test-provider",
		Model:    "model-with-cache-limits",
	})
	if window != 128000 || maxOutput != 8192 {
		t.Fatalf("resolveModelLimits after CopyFrom = (%d, %d), want (128000, 8192)", window, maxOutput)
	}
}
