package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
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

func TestMouseClickActiveToolExpandsPerToolCall(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	started := time.Now()
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "sleep 999", StartedAt: started})
	m.lastTranscriptHash = 0
	m.refreshViewport()

	// Locate the active-tool region.
	var region clickRegion
	found := false
	for _, r := range m.clickRegions {
		if r.target.isActiveTool {
			region, found = r, true
			break
		}
	}
	if !found {
		t.Fatal("expected a click region for the active tool call")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, updated)

	key := activeToolKeyFor(session.ActiveToolCall{Name: "shell.run", StartedAt: started})
	if !mm.activeToolIsExpanded(key) {
		t.Fatal("expected the click to expand the active tool call")
	}

	// A repaint (hash invalidation + rebuild) must keep the override.
	mm.lastTranscriptHash = 0
	mm.refreshViewport()
	if !mm.activeToolIsExpanded(key) {
		t.Fatal("expected the override to survive a refreshViewport repaint")
	}

	// A different StartedAt (new tool call) collapses back.
	key2 := activeToolKeyFor(session.ActiveToolCall{Name: "shell.run", StartedAt: started.Add(time.Second)})
	if mm.activeToolIsExpanded(key2) {
		t.Fatal("expected a different tool call to be collapsed")
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

func TestNowBarClickDrillsIntoAgent(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()

	plan := m.nowBarPlan()
	top, _, ok := m.nowBarBand(plan)
	if !ok {
		t.Fatal("expected a now bar band")
	}
	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top + plan.agentRowStart}); !handled {
		t.Fatal("a click on an agent row must be handled")
	}
	if len(m.viewStack) != 1 {
		t.Fatalf("expected to drill into the subagent, viewStack=%d", len(m.viewStack))
	}
}

// Rows above the first agent (the progress/turn row) are not agents;
// clicking them is consumed but must not drill.
func TestNowBarClickOnProgressRowDoesNothing(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.busy = true
	m.turnStartedAt = m.now().Add(-time.Second)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()

	plan := m.nowBarPlan()
	if plan.agentRowStart != 1 {
		t.Fatalf("agentRowStart = %d, want 1 under a turn row", plan.agentRowStart)
	}
	top, _, _ := m.nowBarBand(plan)
	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top}); !handled {
		t.Fatal("a click inside the bar must be consumed")
	}
	if len(m.viewStack) != 0 {
		t.Fatal("clicking the turn row must not drill in")
	}
}

// The band sits directly below the transcript viewport.
func TestNowBarBandSitsBelowViewport(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent("reviewer", child)
	m.jobs = []native.JobInfo{runningJob(1, "go test ./...", time.Second)}
	m.refreshViewport()
	top, _, ok := m.nowBarBand(m.nowBarPlan())
	if !ok {
		t.Fatal("expected a band")
	}
	if want := m.scrollHintRows() + m.breadcrumbRows() + m.viewport.Height(); top != want {
		t.Fatalf("band top = %d, want %d", top, want)
	}
}

func TestNoNowBarNoBand(t *testing.T) {
	m := newTestModel(t)
	if _, _, ok := m.nowBarBand(m.nowBarPlan()); ok {
		t.Fatal("nothing live means no band")
	}
}
