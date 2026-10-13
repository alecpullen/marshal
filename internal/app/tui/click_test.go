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
	"marshal/internal/viewmodel"
)

func TestClickRegionsCoverThinkingAndAuditBlocks(t *testing.T) {
	m := newTestModel(t)
	// The reasoning belongs to the newest turn: an earlier turn's beat folds
	// to an outline row, which hides the thinking row it contains.
	m.state.AddMessage(session.RoleUser, "hi", session.ContentTypePlain)
	ts1 := time.Now()
	m.state.LogThinking(session.ThinkingEntry{Text: "why I did this", Duration: time.Second, StartedAt: ts1})
	m.invalidateTranscript()
	m.refreshViewport()

	found := false
	if r, ok := regionOf(&m, thinkID(ts1)); ok {
		found = true
		if r.startLine < 0 || r.endLine <= r.startLine {
			t.Fatalf("invalid region for thinking block: %+v", r)
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
	step := viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:1"}
	row := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:a"}
	other := viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:2"}
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

func TestMouseClickTogglesThinkingBlock(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	ts := time.Unix(700, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "click me", Duration: time.Second, StartedAt: ts})
	m.invalidateTranscript()
	m.refreshViewport()

	key := thinkID(ts)
	region, found := regionOf(&m, key)
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
	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "sleep 999", StartedAt: started, ToolCallID: "call_a"})
	m.invalidateTranscript()
	m.refreshViewport()

	key := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:call_a"}
	region, found := regionOf(&m, key)
	if !found {
		t.Fatal("expected a click region for the active tool call")
	}

	top := m.scrollHintRows()
	y := top + region.startLine - m.viewport.YOffset()
	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
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
	if mm.isToolExpanded(viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:call_b"}, true) {
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

	updated, _ := m.Update(tea.MouseClickMsg{X: m.leftWidth + 10, Y: 0, Button: tea.MouseLeft})
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

// A completion marker in a later turn points at a job card inside an earlier
// turn's folded beat. The card has no region while folded; clicking the
// marker opens the beat and lands on the card rather than doing nothing.
func TestMarkerClickOpensTheBeatHidingItsCard(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.foldTasks = true
	m.state.AddMessage(session.RoleUser, "run the gate", session.ContentTypePlain)
	s1 := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(s1, "Starting the gate.")
	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "shell.run", StepID: s1, ToolCallID: "j",
		Args: []byte(`{"command":"pytest -q","background":true}`), ResultSummary: session.JobStartPrefix + "job-1"})
	m.state.EndStep(s1)
	m.state.AddMessageFinal(session.RoleAssistant, "started", session.ContentTypePlain)
	m.state.AddMessage(session.RoleUser, "anything else?", session.ContentTypePlain)
	s2 := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(s2, "Waiting.")
	m.state.EndStep(s2)
	m.state.AddJobExit(session.JobExit{ID: "job-1", Command: "pytest -q", ExitCode: 1})
	m.invalidateTranscript()
	m.refreshViewport()

	var marker nodeRegion
	for _, r := range m.nodeRegions {
		if r.target.jumpTo.Key != "" {
			marker = r
		}
	}
	card := marker.target.jumpTo
	if card.Key == "" {
		t.Fatal("no completion marker region")
	}
	if _, ok := m.regionFor(card); ok {
		t.Fatal("precondition: the card's beat must be folded, leaving the card no region")
	}

	top := m.scrollHintRows()
	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: top + marker.startLine - m.viewport.YOffset(), Button: tea.MouseLeft})
	mm := asModel(t, updated)
	r, ok := mm.regionFor(card)
	if !ok {
		t.Fatal("clicking the marker must open the beat that hides its card")
	}
	if want := max(0, min(r.startLine, mm.viewport.TotalLineCount()-mm.viewport.Height())); mm.viewport.YOffset() != want {
		t.Fatalf("viewport at %d, card starts at %d", mm.viewport.YOffset(), r.startLine)
	}
}
