package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/trustpanel"
	"marshal/internal/commands"
	"marshal/internal/trust"
)

func TestParseReviewArgs(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		model  string
		base   string
		rangeV string
		focus  []string
	}{
		{
			name:  "model only",
			args:  []string{"--model", "ollama/codellama", "main.go"},
			model: "ollama/codellama",
			focus: []string{"main.go"},
		},
		{
			name:  "model equals",
			args:  []string{"--model=ollama/codellama", "main.go"},
			model: "ollama/codellama",
			focus: []string{"main.go"},
		},
		{
			name:  "base only",
			args:  []string{"--base", "main", "src"},
			base:  "main",
			focus: []string{"src"},
		},
		{
			name:  "base equals",
			args:  []string{"--base=main", "src"},
			base:  "main",
			focus: []string{"src"},
		},
		{
			name:   "range only",
			args:   []string{"main...HEAD", "src"},
			rangeV: "main...HEAD",
			focus:  []string{"src"},
		},
		{
			name:   "all flags",
			args:   []string{"--model=ollama/codellama", "--base=main", "main...HEAD", "focus", "words"},
			model:  "ollama/codellama",
			base:   "main",
			rangeV: "main...HEAD",
			focus:  []string{"focus", "words"},
		},
		{
			name:  "no flags",
			args:  []string{"just", "focus"},
			focus: []string{"just", "focus"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, base, rangeV, remaining := parseReviewArgs(tc.args)
			if model != tc.model {
				t.Fatalf("model = %q, want %q", model, tc.model)
			}
			if base != tc.base {
				t.Fatalf("base = %q, want %q", base, tc.base)
			}
			if rangeV != tc.rangeV {
				t.Fatalf("range = %q, want %q", rangeV, tc.rangeV)
			}
			if !reflect.DeepEqual(remaining, tc.focus) {
				t.Fatalf("remaining = %v, want %v", remaining, tc.focus)
			}
		})
	}
}

func TestTrustCommandOpensTrustPanelWhenPending(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := New(state, WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"), WithCommandRegistry(reg))

	var decided trust.Decision
	m.trustDecide = func(d trust.Decision) { decided = d }

	m.dispatchCommand("/trust")
	if !m.dock.IsOpen() {
		t.Fatal("expected dock to be open after /trust with a pending decision")
	}
	if _, ok := m.dock.Panel().(*trustpanel.Panel); !ok {
		t.Fatalf("expected trustpanel, got %T", m.dock.Panel())
	}
	// Opening the panel must not itself trigger a decision.
	if decided != "" {
		t.Fatalf("expected no decision yet, got %q", decided)
	}
}

func TestTrustCommandFallsBackWhenNoPendingDecision(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := New(state, WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"), WithCommandRegistry(reg))

	// No trustDecide wired: the handler's docpanel opens, then the effect
	// must NOT open a trustpanel and must steer to the shell path.
	m.dispatchCommand("/trust")
	if _, ok := m.dock.Panel().(*docpanel.Panel); !ok {
		t.Fatalf("expected the handler's docpanel to stay docked, got %T", m.dock.Panel())
	}
	if _, ok := m.dock.Panel().(*trustpanel.Panel); ok {
		t.Fatal("expected no trustpanel when no decision is pending")
	}
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a system message steering to the shell path")
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "marshal --trust") {
		t.Fatalf("expected fallback message to mention marshal --trust, got %q", last.Content)
	}
}

func TestTrustCommandTrustedDoesNothingExtra(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	state.SetTrusted(true)
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := New(state, WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"), WithCommandRegistry(reg))

	m.dispatchCommand("/trust")
	if _, ok := m.dock.Panel().(*trustpanel.Panel); ok {
		t.Fatal("expected no trustpanel when already trusted")
	}
}
