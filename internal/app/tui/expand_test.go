package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestIsExpandedFollowsGlobalDefaultUntilOverridden(t *testing.T) {
	m := newTestModel(t)
	key := itemKey{ts: time.Unix(100, 0), kind: session.KindThinking}

	if m.isExpanded(key) {
		t.Fatal("expected collapsed by default (detailExpanded starts false)")
	}

	m.detailExpanded = true
	if !m.isExpanded(key) {
		t.Fatal("expected expanded once the global default flips")
	}

	m.toggleItemExpanded(key)
	if m.isExpanded(key) {
		t.Fatal("expected the per-item override to win over the global default")
	}

	m.detailExpanded = false
	if m.isExpanded(key) {
		t.Fatal("expected the per-item override (still false) to persist")
	}
}

func TestCtrlGClearsPerItemOverrides(t *testing.T) {
	m := newTestModel(t)
	key := itemKey{ts: time.Unix(100, 0), kind: session.KindThinking}
	m.toggleItemExpanded(key) // override to true (default false -> true)
	if !m.isExpanded(key) {
		t.Fatal("precondition: override should read expanded")
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	mm := asModel(t, updated)

	// detailExpanded flipped true, and the override was cleared, so the
	// item now simply follows the (new) global default.
	if !mm.isExpanded(key) {
		t.Fatal("expected item to follow the flipped global default")
	}
	if len(mm.itemExpanded) != 0 {
		t.Fatalf("itemExpanded = %v, want cleared", mm.itemExpanded)
	}
}

func TestRefreshViewportUsesPerItemExpandForThinking(t *testing.T) {
	m := newTestModel(t)
	ts1 := time.Unix(300, 0)
	ts2 := time.Unix(301, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "reasoning one", Duration: time.Second, StartedAt: ts1})
	m.state.LogThinking(session.ThinkingEntry{Text: "reasoning two", Duration: time.Second, StartedAt: ts2})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	content := m.viewport.GetContent()
	if strings.Contains(content, "reasoning one") || strings.Contains(content, "reasoning two") {
		t.Fatalf("expected both thinking blocks collapsed by default, got: %s", content)
	}

	m.toggleItemExpanded(itemKey{ts: ts1, kind: session.KindThinking})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	content = m.viewport.GetContent()
	if !strings.Contains(content, "reasoning one") {
		t.Fatal("expected the clicked item's reasoning to be visible")
	}
	if strings.Contains(content, "reasoning two") {
		t.Fatal("expected the other item to remain collapsed")
	}
}

func TestItemKeyForGroupUsesFirstEvent(t *testing.T) {
	events := []registry.AuditEvent{
		{ToolName: "file.read", Timestamp: time.Unix(200, 0)},
		{ToolName: "file.read", Timestamp: time.Unix(201, 0)},
	}
	key := itemKeyForGroup(events)
	want := itemKey{ts: time.Unix(200, 0), kind: session.KindAudit}
	if key != want {
		t.Fatalf("itemKeyForGroup = %+v, want %+v", key, want)
	}
}
