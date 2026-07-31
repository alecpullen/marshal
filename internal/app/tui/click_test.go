package tui

import (
	"testing"
	"time"

	"marshal/internal/app/session"
)

func TestClickRegionsCoverThinkingAndAuditBlocks(t *testing.T) {
	m := newTestModel(t)
	ts1 := time.Unix(600, 0)
	ts2 := time.Unix(601, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "why I did this", Duration: time.Second, StartedAt: ts1})
	m.state.AddMessage(session.RoleUser, "hi", session.ContentTypePlain)
	_ = ts2
	m.lastTranscriptHash = 0
	m.refreshViewport()

	found := false
	for _, r := range m.clickRegions {
		if r.target.key == (itemKey{ts: ts1, kind: session.KindThinking}) {
			found = true
			if r.startLine < 0 || r.endLine <= r.startLine {
				t.Fatalf("invalid region for thinking block: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("expected a click region for the logged thinking item")
	}
}

func TestContentLineForClickRejectsOutsideViewport(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	m.refreshViewport()

	if _, ok := m.contentLineForClick(-1, 0); ok {
		t.Fatal("expected negative X to be rejected")
	}
	if _, ok := m.contentLineForClick(0, -1); ok {
		t.Fatal("expected negative Y to be rejected")
	}
	if _, ok := m.contentLineForClick(m.leftWidth+5, 0); ok {
		t.Fatal("expected X past leftWidth to be rejected")
	}
	if _, ok := m.contentLineForClick(0, m.viewport.Height()+5); ok {
		t.Fatal("expected Y past the viewport height to be rejected")
	}

	line, ok := m.contentLineForClick(0, 0)
	if !ok {
		t.Fatal("expected (0,0) to be inside the viewport")
	}
	if line != m.viewport.YOffset() {
		t.Fatalf("line = %d, want YOffset %d", line, m.viewport.YOffset())
	}
}

func TestRegionAtFindsContainingRegion(t *testing.T) {
	m := newTestModel(t)
	m.clickRegions = []clickRegion{
		{startLine: 0, endLine: 2, target: clickTarget{key: itemKey{ts: time.Unix(1, 0), kind: session.KindThinking}}},
		{startLine: 3, endLine: 5, target: clickTarget{isActiveTool: true}},
	}

	if _, ok := m.regionAt(2); ok {
		t.Fatal("line 2 is the separator between blocks and should not match")
	}
	target, ok := m.regionAt(4)
	if !ok || !target.isActiveTool {
		t.Fatalf("regionAt(4) = %+v, %v, want the active-tool region", target, ok)
	}
}
