package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

func TestIsExpandedFollowsGlobalDefaultUntilOverridden(t *testing.T) {
	m := newTestModel(t)
	// A real transcript item, so the key under test is the one the app
	// actually produces rather than a hand-built literal.
	m.state.LogThinking(session.ThinkingEntry{Text: "why", StartedAt: time.Unix(100, 0)})
	key := testItemKey(t, m, session.KindThinking, 0)

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
	m.state.LogThinking(session.ThinkingEntry{Text: "why", StartedAt: time.Unix(100, 0)})
	key := testItemKey(t, m, session.KindThinking, 0)
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

func TestCtrlGClearsActiveToolOverrides(t *testing.T) {
	m := newTestModel(t)
	keyA := activeToolKey{startedAt: time.Unix(500, 0), name: "shell.run"}
	keyB := activeToolKey{startedAt: time.Unix(501, 0), name: "file.read"}
	m.toggleActiveToolExpanded(keyA)
	m.toggleActiveToolExpanded(keyB)
	if !m.activeToolIsExpanded(keyA) || !m.activeToolIsExpanded(keyB) {
		t.Fatal("precondition: both overrides should be set")
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+g was not handled")
	}
	mm := asModel(t, updated)

	if len(mm.activeToolExpanded) != 0 {
		t.Fatalf("activeToolExpanded = %v, want cleared", mm.activeToolExpanded)
	}
	if mm.activeToolIsExpanded(keyA) || mm.activeToolIsExpanded(keyB) {
		t.Fatal("expected all active-tool overrides to be cleared")
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

	m.toggleItemExpanded(testItemKey(t, m, session.KindThinking, 0))
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

func TestItemKeyForGroupUsesFirstMember(t *testing.T) {
	key := itemKeyForGroup([]string{"audit:1", "audit:2"})
	want := itemKey{viewID: "audit:1", kind: session.KindAudit}
	if key != want {
		t.Fatalf("itemKeyForGroup = %+v, want %+v", key, want)
	}
}

// A group's key must not move as the run grows: the first member is the one
// thing a growing run cannot change.
func TestItemKeyForGroupStableAsRunGrows(t *testing.T) {
	short := itemKeyForGroup([]string{"audit:1", "audit:2"})
	long := itemKeyForGroup([]string{"audit:1", "audit:2", "audit:3"})
	if short != long {
		t.Fatalf("group key moved as the run grew: %+v -> %+v", short, long)
	}
}

// An empty member list has no identity to derive; the key must not claim one.
func TestItemKeyForGroupEmpty(t *testing.T) {
	key := itemKeyForGroup(nil)
	if key.viewID != "" {
		t.Fatalf("empty group key carries viewID %q, want empty", key.viewID)
	}
}

// testItemKey returns the identity key of the n-th transcript item of a kind,
// read from the LIVE transcript rather than hand-built. Hand-building a key is
// how a test stops testing the identity rule and starts asserting on a literal
// — and under the old (timestamp, kind) identity, a hand-built key is exactly
// the collision the test was supposed to catch.
func testItemKey(t *testing.T, m Model, kind session.TranscriptKind, n int) itemKey {
	t.Helper()
	seen := 0
	for _, item := range m.state.Transcript() {
		if item.Kind != kind {
			continue
		}
		if seen == n {
			return itemKeyFor(&item)
		}
		seen++
	}
	t.Fatalf("no %v item at index %d in the transcript", kind, n)
	return itemKey{}
}

// Two same-timestamp thinking entries produce DIFFERENT keys. Under the old
// (timestamp, kind) identity they collided, which is what made expanding one
// block expand both.
func TestItemKeysSeparateIdenticalTimestamps(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(900, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "first", StartedAt: at})
	m.state.LogThinking(session.ThinkingEntry{Text: "second", StartedAt: at})

	a := testItemKey(t, m, session.KindThinking, 0)
	b := testItemKey(t, m, session.KindThinking, 1)
	if a == b {
		t.Fatalf("both thinking items produced the same key %+v", a)
	}

	// And the expand state is genuinely independent: expanding one must not
	// expand the other.
	m.toggleItemExpanded(a)
	if !m.isExpanded(a) {
		t.Fatal("toggling a must expand it")
	}
	if m.isExpanded(b) {
		t.Fatal("expanding one thought expanded the other: the keys still collide")
	}
}
