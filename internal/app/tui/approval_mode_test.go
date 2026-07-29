package tui

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
)

// The status line must report the mode the policy engine is actually
// enforcing. Hardcoding ModeDefault here made the TUI claim "default" while
// app.Run had wired the engine to auto from the same config value — so file
// edits were auto-approved while the UI said the agent was read-only.
func TestApprovalModeSeededFromConfig(t *testing.T) {
	for _, tc := range []struct {
		configured string
		want       policy.ApprovalMode
		wantLabel  string
	}{
		{"auto", policy.ModeAuto, "auto"},
		{"copilot", policy.ModeCopilot, "copilot"},
		{"edit", policy.ModeEdit, "edit"},
		{"plan", policy.ModePlan, "plan"},
		{"default", policy.ModeDefault, "default"},
		{"", policy.ModeDefault, "default"},
		{"nonsense", policy.ModeDefault, "default"},
	} {
		t.Run(tc.configured, func(t *testing.T) {
			cfg := config.Default()
			cfg.Agent.ApprovalMode = tc.configured
			state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
			m := New(state)
			if m.approvalMode != tc.want {
				t.Fatalf("approvalMode = %q, want %q", m.approvalMode, tc.want)
			}
			if got := m.modeSegment(); got != tc.wantLabel {
				t.Fatalf("modeSegment() = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}
