package policy

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// TestMatchRule_SessionExactMatch pins S-01: session-approved commands and
// config allow/confirm lists must match the full normalized command exactly.
// Prefix matching would let a later `docker ps && curl ...` inherit the
// approval granted to `docker ps`.
func TestMatchRule_SessionExactMatch(t *testing.T) {
	// Use a zero config so the test is not affected by default allow rules.
	cfg := config.Config{}
	pe := NewEngine(&cfg, []string{"docker ps"})
	state := interaction.State{Mode: interaction.ModeAct, Revision: 1}

	tests := []struct {
		cmd  string
		want Decision
	}{
		{"docker ps", DecisionAllow},
		{"docker  ps", DecisionAllow}, // normalization collapses whitespace
		{"docker ps && echo leaked", DecisionConfirm},
		{"docker p", DecisionConfirm},
		{"docker", DecisionConfirm},
	}

	for _, tc := range tests {
		ev := evalShell(t, pe, state, tc.cmd)
		matched := false
		for _, reason := range ev.Reasons {
			matched = matched || reason.Code == ReasonExplicitAllow
		}
		if matched != (tc.want == DecisionAllow) {
			t.Errorf("%q: session match = %v", tc.cmd, matched)
		}
	}
}

// TestMatchRule_ConfigAllowExactMatch pins S-01 for the config allow list.
func TestMatchRule_ConfigAllowExactMatch(t *testing.T) {
	cfg := config.Config{}
	cfg.Tools.Shell.Allow.Commands = []string{"docker ps"}
	pe := NewEngine(&cfg, nil)

	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "docker ps && echo leaked")
	for _, reason := range ev.Reasons {
		if reason.Code == ReasonExplicitAllow {
			t.Errorf("config allow matched a suffix: %+v", ev)
		}
	}
}

// TestMatchRule_ConfigConfirmExactMatch pins S-01 for the config confirm list.
func TestMatchRule_ConfigConfirmExactMatch(t *testing.T) {
	cfg := config.Config{}
	cfg.Tools.Shell.Confirm.Commands = []string{"docker ps"}
	pe := NewEngine(&cfg, nil)

	// A command that starts with the confirm rule but adds a suffix must not
	// match; it should fall through to the default secure confirm.
	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "docker ps && echo leaked")
	if ev.Decision != DecisionAllow {
		t.Errorf("got %v, want Allow", ev.Decision)
	}
	for _, r := range ev.Reasons {
		if r.Code == ReasonExplicitConfirm && r.Rule != "" {
			t.Errorf("reason %+v should not claim a config confirm rule matched", r)
		}
	}
}
