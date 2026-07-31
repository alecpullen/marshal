package tui

import (
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
