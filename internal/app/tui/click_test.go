package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
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

func TestMouseClickTogglesThinkingBlock(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	ts := time.Unix(700, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "click me", Duration: time.Second, StartedAt: ts})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	key := itemKey{ts: ts, kind: session.KindThinking}
	var region clickRegion
	found := false
	for _, r := range m.clickRegions {
		if r.target.key == key {
			region, found = r, true
		}
	}
	if !found {
		t.Fatal("expected a click region for the thinking block")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, updated)

	if !mm.isExpanded(key) {
		t.Fatal("expected the click to expand the thinking block")
	}
	if !strings.Contains(mm.viewport.GetContent(), "click me") {
		t.Fatal("expected the reasoning text to be visible after the click")
	}
}

func TestMouseClickOutsideViewportIsNoop(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	ts := time.Unix(701, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "leave me collapsed", Duration: time.Second, StartedAt: ts})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	updated, _ := m.Update(tea.MouseClickMsg{X: m.leftWidth + 10, Y: 0, Button: tea.MouseLeft})
	mm := asModel(t, updated)

	key := itemKey{ts: ts, kind: session.KindThinking}
	if mm.isExpanded(key) {
		t.Fatal("expected an out-of-bounds click to be a no-op")
	}
}

func TestMouseClickExpandsFailedToolCall(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	ts := time.Unix(702, 0)
	m.state.LogToolCall(registry.AuditEvent{
		Timestamp: ts,
		ToolName:  "shell.run",
		Error:     "boom",
		Args:      []byte(`{"command": "echo hi"}`),
	})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	key := itemKey{ts: ts, kind: session.KindAudit}
	var region clickRegion
	found := false
	for _, r := range m.clickRegions {
		if r.target.key == key {
			region, found = r, true
		}
	}
	if !found {
		t.Fatal("expected a click region for the failed tool call")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, updated)

	if !mm.isExpanded(key) {
		t.Fatal("expected the click to expand the failed tool call")
	}
	if !strings.Contains(mm.viewport.GetContent(), "error: boom") {
		t.Fatal("expected the failure detail to be visible after the click")
	}
}

func TestMouseClickTodoPanelCyclesMode(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	todos := make([]db.TodoItem, 0, 8)
	for i := 0; i < 8; i++ {
		status := native.TodoPending
		if i == 3 {
			status = native.TodoInProgress
		}
		todos = append(todos, db.TodoItem{Content: "todo item", Status: status})
	}
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if m.todoPanelMode != todoPanelExpanded {
		t.Fatalf("initial mode = %v, want expanded", m.todoPanelMode)
	}

	top, _, ok := m.todoPanelBand()
	if !ok {
		t.Fatal("expected a todo panel band after seeding todos")
	}

	updated, _ := m.Update(tea.MouseClickMsg{X: 2, Y: top, Button: tea.MouseLeft})
	mm := asModel(t, updated)
	if mm.todoPanelMode != todoPanelCollapsed {
		t.Fatalf("click in the todo band should advance to collapsed, got %v", mm.todoPanelMode)
	}

	// Control: a click just above the band (inside the viewport) must not
	// cycle the mode.
	ctrl, _ := m.Update(tea.MouseClickMsg{X: 2, Y: top - 1, Button: tea.MouseLeft})
	cc := asModel(t, ctrl)
	if cc.todoPanelMode != todoPanelExpanded {
		t.Fatalf("click above the band must not cycle, got %v", cc.todoPanelMode)
	}

	// Control: a click past the left column width must not cycle either.
	ctrl2, _ := m.Update(tea.MouseClickMsg{X: m.leftWidth + 5, Y: top, Button: tea.MouseLeft})
	cc2 := asModel(t, ctrl2)
	if cc2.todoPanelMode != todoPanelExpanded {
		t.Fatalf("click past leftWidth must not cycle, got %v", cc2.todoPanelMode)
	}
}
