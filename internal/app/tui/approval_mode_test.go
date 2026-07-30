package tui

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/settings"
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

// The footer (m.approvalMode / modeSegment) and the /settings browser
// (m.state.Config.Agent.ApprovalMode) must stay in sync no matter which
// side changes the mode. Previously each mutation path updated only its
// own store, so the two disagreed.
func TestSettingsApprovalModeChangeUpdatesFooter(t *testing.T) {
	m := newTestModel(t)
	cfg := m.state.Config
	cfg.Agent.ApprovalMode = "auto"

	updated, _ := m.Update(settings.ChangedMsg{
		Cfg:      cfg,
		Receipts: []string{"agent.approval_mode = auto"},
	})
	m = updated.(Model)

	if m.approvalMode != policy.ModeAuto {
		t.Fatalf("approvalMode = %q, want %q after settings change", m.approvalMode, policy.ModeAuto)
	}
	if got := m.modeSegment(); got != "auto" {
		t.Fatalf("modeSegment() = %q, want %q after settings change", got, "auto")
	}
}

// setMode (Tab cycling and the /plan /default /edit /copilot /auto
// commands) is session-scoped, but the settings browser is built from
// m.state.Config — so the in-memory config must mirror the session mode
// or /settings shows a stale value.
func TestSetModeMirrorsSessionConfig(t *testing.T) {
	m := newTestModel(t)
	m.setMode("auto")

	if m.approvalMode != policy.ModeAuto {
		t.Fatalf("approvalMode = %q, want %q", m.approvalMode, policy.ModeAuto)
	}
	if got := m.state.Config.Agent.ApprovalMode; got != "auto" {
		t.Fatalf("state.Config.Agent.ApprovalMode = %q, want %q", got, "auto")
	}
}
