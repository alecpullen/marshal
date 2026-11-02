package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/registry"
)

func TestClickRegionsCoverABeatThatOnlyReasoned(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "hi", session.ContentTypePlain)
	ts1 := time.Now()
	m.state.LogThinking(session.ThinkingEntry{Text: "why I did this", Duration: time.Second, StartedAt: ts1})
	m.invalidateTranscript()
	m.refreshViewport()

	r, ok := regionOf(&m, beatWithThinking(&m, m.state, ts1))
	if !ok {
		t.Fatal("expected a click region for the beat holding the reasoning")
	}
	if r.startLine < 0 || r.endLine <= r.startLine {
		t.Fatalf("invalid region for the beat: %+v", r)
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
	step := nodeID{Kind: acpwire.KindStep, Key: "step:1"}
	row := nodeID{Kind: acpwire.KindTool, Key: "tool:a"}
	other := nodeID{Kind: acpwire.KindStep, Key: "step:2"}
	m.nodeRegions = []nodeRegion{
		{startLine: 0, endLine: 6, target: clickTarget{node: step}},
		{startLine: 2, endLine: 4, target: clickTarget{node: row}},
		{startLine: 7, endLine: 9, target: clickTarget{node: other}},
	}

	if _, ok := m.regionAt(6); ok {
		t.Fatal("line 6 is the separator between blocks and should not match")
	}
	if target, ok := m.regionAt(3); !ok || target.node != row {
		t.Fatalf("regionAt(3) = %+v, %v, want the narrower row inside the step", target, ok)
	}
	if target, ok := m.regionAt(5); !ok || target.node != step {
		t.Fatalf("regionAt(5) = %+v, %v, want the step header region", target, ok)
	}
	if target, ok := m.regionAt(8); !ok || target.node != other {
		t.Fatalf("regionAt(8) = %+v, %v, want the second step", target, ok)
	}
}

// clickAt sends a left press and its release on one cell: a click. Clicks
// act on release, so a press alone can still become a drag.
func clickAt(t *testing.T, m tea.Model, x, y int) tea.Model {
	t.Helper()
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	updated, _ = updated.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return updated
}

func TestMouseClickOnABeatShowsItsReasoning(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	ts := time.Unix(700, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "click me", Duration: time.Second, StartedAt: ts})
	m.invalidateTranscript()
	m.refreshViewport()

	key := beatWithThinking(&m, m.state, ts)
	region, found := regionOf(&m, key)
	if !found {
		t.Fatal("expected a click region for the beat")
	}
	if strings.Contains(m.viewport.GetContent(), "click me") {
		t.Fatal("a collapsed beat shows its thinking time, not its reasoning")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated := clickAt(t, m, 1, y)
	mm := asModel(t, updated)

	if !mm.isExpanded(key) {
		t.Fatal("expected the click to expand the beat")
	}
	if !strings.Contains(mm.viewport.GetContent(), "click me") {
		t.Fatal("expected the reasoning text to be visible after the click")
	}
}

func TestMouseClickActiveToolExpandsPerToolCall(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	started := time.Now()
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "sleep 999", StartedAt: started, ToolCallID: "call_a"})
	m.invalidateTranscript()
	m.refreshViewport()

	key := nodeID{Kind: acpwire.KindTool, Key: "tool:call_a"}
	region, found := regionOf(&m, key)
	if !found {
		t.Fatal("expected a click region for the active tool call")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated := clickAt(t, m, 1, y)
	mm := asModel(t, updated)

	if !mm.isToolExpanded(key, true) {
		t.Fatal("expected the click to expand the active tool call")
	}

	// A repaint (hash invalidation + rebuild) must keep the override.
	mm.invalidateTranscript()
	mm.refreshViewport()
	if !mm.isToolExpanded(key, true) {
		t.Fatal("expected the override to survive a refreshViewport repaint")
	}

	// A different call collapses back.
	if mm.isToolExpanded(nodeID{Kind: acpwire.KindTool, Key: "tool:call_b"}, true) {
		t.Fatal("expected a different tool call to be collapsed")
	}
}

func TestMouseClickOutsideViewportIsNoop(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	ts := time.Unix(701, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "leave me collapsed", Duration: time.Second, StartedAt: ts})
	m.invalidateTranscript()
	m.refreshViewport()

	updated := clickAt(t, m, m.leftWidth+10, 0)
	mm := asModel(t, updated)

	key := thinkID(ts)
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
	m.invalidateTranscript()
	m.refreshViewport()

	key := toolIDAt(ts)
	region, found := regionOf(&m, key)
	if !found {
		t.Fatal("expected a click region for the failed tool call")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated := clickAt(t, m, 1, y)
	mm := asModel(t, updated)

	if !mm.isExpanded(key) {
		t.Fatal("expected the click to expand the failed tool call")
	}
	if !strings.Contains(mm.viewport.GetContent(), "error: boom") {
		t.Fatal("expected the failure detail to be visible after the click")
	}
}

func TestBoxSitsBelowTheViewport(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()
	if got := m.boxTop(); got != m.scrollHintRows()+m.breadcrumbRows()+m.viewport.Height() {
		t.Fatalf("box top = %d, want %d", got, m.scrollHintRows()+m.breadcrumbRows()+m.viewport.Height())
	}
}

func TestNoBoxWhenIdle(t *testing.T) {
	m := newTestModel(t)
	if got := m.boxRows(); got != 0 {
		t.Fatalf("boxRows() = %d, want no box when nothing is live", got)
	}
}

// A completion event is a row with state of its own: a click expands it in
// place rather than jumping anywhere.
func TestCompletionClickTogglesItsDisclosure(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "run the gate", session.ContentTypePlain)
	s1 := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(s1, "Starting the gate.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "shell.run", StepID: s1, ToolCallID: "j",
		Args: []byte(`{"command":"pytest -q","background":true}`), ResultSummary: session.JobStartPrefix + "job-1"})
	m.state.EndStep(s1)
	m.state.AddMessageFinal(session.RoleAssistant, "started", session.ContentTypePlain)
	m.state.AddJobExit(session.JobExit{ID: "job-1", Command: "pytest -q", ExitCode: 1})
	m.invalidateTranscript()
	m.refreshViewport()

	var region nodeRegion
	for _, r := range m.nodeRegions {
		if r.target.node.Kind == acpwire.KindCompletion {
			region = r
		}
	}
	if region.target.node.Key == "" {
		t.Fatalf("no completion region:\n%s", stripANSI(m.viewport.GetContent()))
	}
	if !strings.Contains(stripANSI(m.viewport.GetContent()), glyph.DisclosureCollapsed) {
		t.Fatalf("precondition: the event starts collapsed:\n%s", stripANSI(m.viewport.GetContent()))
	}

	top := m.scrollHintRows()
	updated := clickAt(t, m, 1, top+region.startLine-m.viewport.YOffset())
	mm := asModel(t, updated)
	if !strings.Contains(stripANSI(mm.viewport.GetContent()), glyph.DisclosureExpanded) {
		t.Fatalf("a click must expand the event:\n%s", stripANSI(mm.viewport.GetContent()))
	}
}
