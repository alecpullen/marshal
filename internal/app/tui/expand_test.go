package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/viewmodel"
)

func TestIsExpandedFollowsGlobalDefaultUntilOverridden(t *testing.T) {
	m := newTestModel(t)
	key := thinkID(time.Unix(100, 0))

	if m.isExpanded(key) {
		t.Fatal("expected collapsed by default (density starts at steps)")
	}

	m.density = densityFull
	if !m.isExpanded(key) {
		t.Fatal("expected expanded once the global default flips")
	}

	m.toggleExpanded(key)
	if m.isExpanded(key) {
		t.Fatal("expected the per-item override to win over the global default")
	}

	m.density = densitySteps
	if m.isExpanded(key) {
		t.Fatal("expected the per-item override (still false) to persist")
	}
}

func TestCtrlGClearsPerItemOverrides(t *testing.T) {
	m := newTestModel(t)
	key := thinkID(time.Unix(100, 0))
	m.toggleExpanded(key) // override to true (default false -> true)
	if !m.isExpanded(key) {
		t.Fatal("precondition: override should read expanded")
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	mm := asModel(t, updated)

	// the global level moved to full, and the override was cleared, so the
	// item now simply follows the (new) global default.
	if !mm.isExpanded(key) {
		t.Fatal("expected item to follow the flipped global default")
	}
	if len(mm.nodeDensity) != 0 {
		t.Fatalf("nodeDensity = %v, want cleared", mm.nodeDensity)
	}
}

func TestCtrlGClearsActiveToolOverrides(t *testing.T) {
	m := newTestModel(t)
	keyA := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:a"}
	keyB := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:b"}
	m.toggleExpanded(keyA)
	m.toggleExpanded(keyB)
	if !m.isToolExpanded(keyA, true) || !m.isToolExpanded(keyB, true) {
		t.Fatal("precondition: both overrides should be set")
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	mm := asModel(t, updated)

	if len(mm.nodeDensity) != 0 {
		t.Fatalf("nodeDensity = %v, want cleared", mm.nodeDensity)
	}
	// A running call ignores the global default: it stays collapsed.
	if mm.isToolExpanded(keyA, true) || mm.isToolExpanded(keyB, true) {
		t.Fatal("expected all active-tool overrides to be cleared")
	}
}

func TestRefreshViewportUsesPerItemExpandForThinking(t *testing.T) {
	m := newTestModel(t)
	ts1 := time.Unix(300, 0)
	ts2 := time.Unix(301, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "reasoning one", Duration: time.Second, StartedAt: ts1})
	m.state.LogThinking(session.ThinkingEntry{Text: "reasoning two", Duration: time.Second, StartedAt: ts2})
	m.invalidateTranscript()
	m.refreshViewport()

	content := m.viewport.GetContent()
	if strings.Contains(content, "reasoning one") || strings.Contains(content, "reasoning two") {
		t.Fatalf("expected both thinking blocks collapsed by default, got: %s", content)
	}

	m.toggleExpanded(thinkID(ts1))
	m.invalidateTranscript()
	m.refreshViewport()

	content = m.viewport.GetContent()
	if !strings.Contains(content, "reasoning one") {
		t.Fatal("expected the clicked item's reasoning to be visible")
	}
	if strings.Contains(content, "reasoning two") {
		t.Fatal("expected the other item to remain collapsed")
	}
}
