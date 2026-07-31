package policy

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

// TestMatchRule_SessionExactMatch pins S-01: session-approved commands and
// config allow/confirm lists must match the full normalized command exactly.
// Prefix matching would let a later `go test ./... && curl ...` inherit the
// approval granted to `go test ./...`.
func TestMatchRule_SessionExactMatch(t *testing.T) {
	// Use a zero config so the test is not affected by default allow rules.
	cfg := config.Config{}
	pe := NewEngine(&cfg, []string{"go test ./..."})

	tests := []struct {
		cmd  string
		want Decision
	}{
		{"go test ./...", DecisionAllow},
		{"go  test   ./...", DecisionAllow}, // normalization collapses whitespace
		{"go test ./... && echo leaked", DecisionConfirm},
		{"go test ./..", DecisionConfirm},
		{"go test", DecisionConfirm},
	}

	for _, tc := range tests {
		dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": tc.cmd})
		if err != nil {
			t.Fatalf("Evaluate(%q) error: %v", tc.cmd, err)
		}
		if dec != tc.want {
			t.Errorf("Evaluate(%q) = %v, want %v; reason=%q", tc.cmd, dec, tc.want, reason)
		}
	}
}

// TestMatchRule_ConfigAllowExactMatch pins S-01 for the config allow list.
func TestMatchRule_ConfigAllowExactMatch(t *testing.T) {
	cfg := config.Config{}
	cfg.Tools.Shell.Allow.Commands = []string{"go test ./..."}
	pe := NewEngine(&cfg, nil)

	dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "go test ./... && echo leaked"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionConfirm {
		t.Errorf("got %v, want Confirm; reason=%q", dec, reason)
	}
}

// TestMatchRule_ConfigConfirmExactMatch pins S-01 for the config confirm list.
func TestMatchRule_ConfigConfirmExactMatch(t *testing.T) {
	cfg := config.Config{}
	cfg.Tools.Shell.Confirm.Commands = []string{"go get github.com/foo/bar@latest"}
	pe := NewEngine(&cfg, nil)

	// A command that starts with the confirm rule but adds a suffix must not
	// match; it should fall through to the default secure confirm.
	dec, reason, err := pe.Evaluate("shell.run", map[string]interface{}{"command": "go get github.com/foo/bar@latest && echo leaked"})
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if dec != DecisionConfirm {
		t.Errorf("got %v, want Confirm; reason=%q", dec, reason)
	}
	if strings.Contains(reason, "config confirm rule") {
		t.Errorf("reason %q should not claim a config confirm rule matched", reason)
	}
}
