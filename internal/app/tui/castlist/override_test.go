package castlist

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/llm/routing"
)

// panelWithRows creates a *Panel with the given rows.
func panelWithRows(t *testing.T, rows ...Row) *Panel {
	t.Helper()
	p := New("test", rows, nil, "agent")
	return p
}

// The cursor must skip the strategy and verify-gate rows: they are not
// roles and have nothing to override.
func TestCursorSkipsNonRoleRows(t *testing.T) {
	p := panelWithRows(t,
		Row{Title: "strategy"},
		Row{Title: "scout", Role: routing.AgentRole("scout")},
		Row{Title: "verify gate"},
		Row{Title: "reviewer", Role: routing.AgentRole("reviewer")},
	)
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		seen[p.rows[p.cursor].Title] = true
		p.moveCursor(1)
	}
	for _, bad := range []string{"strategy", "verify gate"} {
		if seen[bad] {
			t.Errorf("cursor landed on non-role row %q", bad)
		}
	}
}

func TestOverrideRecordedPerRole(t *testing.T) {
	p := panelWithRows(t, Row{Title: "scout", Role: routing.AgentRole("scout")})
	p.setOverride(routing.AgentRole("scout"), "cheap-preset")
	if got := p.Overrides()[routing.AgentRole("scout")]; got != "cheap-preset" {
		t.Fatalf("override = %q, want cheap-preset", got)
	}
}

// The sentinel clears rather than storing a literal.
func TestOverrideUnsetClearsIt(t *testing.T) {
	p := panelWithRows(t, Row{Title: "scout", Role: routing.AgentRole("scout")})
	p.setOverride(routing.AgentRole("scout"), "cheap-preset")
	p.setOverride(routing.AgentRole("scout"), "")
	if _, still := p.Overrides()[routing.AgentRole("scout")]; still {
		t.Fatal("clearing an override must remove the entry, not store an empty string")
	}
}

func TestOverriddenRowIsMarked(t *testing.T) {
	p := panelWithRows(t, Row{Title: "scout", Role: routing.AgentRole("scout"), Detail: "sonnet-5"})
	p.setOverride(routing.AgentRole("scout"), "opus-5")
	r := p.rows[0]
	if r.Badge == "" {
		t.Error("an overridden row needs a marker")
	}
	if r.Detail == "sonnet-5" {
		t.Error("an overridden row must show the override in Detail")
	}
}

// A push update (e.g. SetVerifyRow) must not move the cursor out from
// under the user.
func TestRowUpdateDoesNotClobberCursor(t *testing.T) {
	p := panelWithRows(t,
		Row{Title: "scout", Role: routing.AgentRole("scout")},
		Row{Title: "reviewer", Role: routing.AgentRole("reviewer")},
	)
	p.moveCursor(1)
	before := p.rows[p.cursor].Title
	p.SetVerifyRow("verify gate on")
	if p.rows[p.cursor].Title != before {
		t.Fatalf("cursor moved from %q to %q on a row update", before, p.rows[p.cursor].Title)
	}
}

// A resolution failure blocks Enter rather than starting a run that will
// fail at dispatch.
func TestResolutionErrorBlocksStart(t *testing.T) {
	p := panelWithRows(t, Row{Title: "scout", Role: routing.AgentRole("scout")})
	p.setRowErr(routing.AgentRole("scout"), "unknown preset \"nope\"")
	if !p.blocked() {
		t.Fatal("a row error must block Enter")
	}
}

// Production-shape castlist: all rows are role rows (the strategy row is
// rendered separately in View, not in p.rows). Enter must emit StartMsg
// even when the cursor is on a role row — "o" opens the picker, not Enter.
func TestProductionShapeCastlistCanStart(t *testing.T) {
	p := panelWithRows(t,
		Row{Title: "scout", Role: routing.AgentRole("scout"), Detail: "ollama/qwen3"},
		Row{Title: "implementer", Role: routing.AgentRole("implementer"), Detail: "ollama/qwen3"},
		Row{Title: "reviewer", Role: routing.AgentRole("reviewer"), Detail: "ollama/qwen3"},
	)
	// Cursor starts on a role row (scout).
	if p.rows[p.cursor].Role == "" {
		t.Fatal("cursor must start on a role row in production shape")
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter must emit StartMsg even when cursor is on a role row")
	}
	msg := cmd()
	start, ok := msg.(StartMsg)
	if !ok {
		t.Fatalf("msg = %T, want StartMsg", msg)
	}
	if start.Strategy != "agent" {
		t.Errorf("Strategy = %q, want agent", start.Strategy)
	}
}

// Clearing an override must restore the row's original Detail and Badge,
// not leave the stale override values.
func TestClearOverrideRestoresOriginalDetail(t *testing.T) {
	p := panelWithRows(t, Row{
		Title:  "scout",
		Role:   routing.AgentRole("scout"),
		Detail: "ollama/qwen3",
		Badge:  "routed",
	})
	p.setOverride(routing.AgentRole("scout"), "opus-5")
	if p.rows[0].Detail != "opus-5" {
		t.Fatalf("after override, Detail = %q, want opus-5", p.rows[0].Detail)
	}
	if p.rows[0].Badge != "override" {
		t.Fatalf("after override, Badge = %q, want override", p.rows[0].Badge)
	}
	// Clear the override.
	p.setOverride(routing.AgentRole("scout"), "")
	if p.rows[0].Detail != "ollama/qwen3" {
		t.Fatalf("after clear, Detail = %q, want ollama/qwen3", p.rows[0].Detail)
	}
	if p.rows[0].Badge != "routed" {
		t.Fatalf("after clear, Badge = %q, want routed", p.rows[0].Badge)
	}
}
