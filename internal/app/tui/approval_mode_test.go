package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/settings"
	"marshal/internal/interaction"
)

// The status line must report the effective canonical tuple. The model seeds
// its presentation from the session's committed interaction state, not from a
// legacy approval-mode string.
func TestApprovalModeSeededFromCanonicalState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      interaction.Mode
		auto      bool
		want      string
		wantLabel string
	}{
		{"act", interaction.ModeAct, false, "act", "Act"},
		{"ask", interaction.ModeAsk, false, "ask", "Ask"},
		{"act auto", interaction.ModeAct, true, "act", "Act · Auto · System"},
		{"empty falls back ask", "", false, "ask", "Ask"},
		{"invalid falls back ask", "nonsense", false, "ask", "Ask"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Agent.Mode = tc.mode
			cfg.Agent.Auto = tc.auto
			state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
			m := testModel(t, state)
			if got := string(stateOf(m).InteractionState().Mode); got != tc.want {
				t.Fatalf("approvalMode = %q, want %q", got, tc.want)
			}
			if got := m.modeSegment(); got != tc.wantLabel {
				t.Fatalf("modeSegment() = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}

// The footer and the /settings browser must stay in sync no matter which side
// changes the configured tuple.
func TestSettingsApprovalModeChangeUpdatesFooter(t *testing.T) {
	m := newTestModel(t)
	cfg := stateOf(m).Config
	cfg.Agent.Mode = interaction.ModeAsk
	cfg.Agent.Auto = false

	updated, settle := m.Update(settings.ChangedMsg{
		Cfg:      cfg,
		Receipts: []string{"agent.mode = ask"},
	})
	m = updated.(Model)
	// The settings change's reload runs from the call queue, so the config is
	// applied when its result is handled.
	m = settleCalls(t, m, settle)

	if got := string(stateOf(m).InteractionState().Mode); got != "ask" {
		t.Fatalf("approvalMode = %q, want ask after settings change", got)
	}
	if got := m.modeSegment(); got != "Ask" {
		t.Fatalf("modeSegment() = %q, want Ask after settings change", got)
	}
}

// TestModeElevationHidesApprovalPanel pins the reported duplication: while the
// access panel owns the dock for a typed access request, the input area must
// NOT also render the normal approve/deny approval panel — the panel owns the
// decision UI.
func TestModeElevationHidesApprovalPanel(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Mode = interaction.ModeAsk
	f := newAccessTestFixture(t, 100, 30, cfg)
	f.register(t, session.AccessIntent{Editing: true, Reason: "The agent requests editing capability."})

	// A runtime tick is enough: no keypress is needed to open the panel.
	f.tick()
	if !f.m.accessPanelOwnsDock() {
		t.Fatalf("access panel did not open on a runtime tick; dock panel = %T", f.m.dock.Panel())
	}

	area := stripANSI(f.m.renderInputArea())
	for _, label := range []string{"allow", "deny"} {
		if strings.Contains(area, label) {
			t.Errorf("input area shows the approve/deny panel (%q) alongside the access panel:\n%s", label, area)
		}
	}
}
