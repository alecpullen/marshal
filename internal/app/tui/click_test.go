package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
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
	want := testItemKey(t, m, session.KindThinking, 0)
	for _, r := range m.clickRegions {
		if r.target.key == want {
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
		{startLine: 0, endLine: 2, target: clickTarget{key: itemKey{viewID: "thinking:1", kind: session.KindThinking}}},
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

	key := testItemKey(t, m, session.KindThinking, 0)
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

	key := testItemKey(t, m, session.KindThinking, 0)
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

	key := testItemKey(t, m, session.KindAudit, 0)
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

// TestMouseClickTodoPanelNeverHides verifies that repeated clicks on the
// todo panel toggle between expanded and collapsed and never enter the
// hidden state — a click should never make the panel vanish.
func TestMouseClickTodoPanelNeverHides(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	todos := make([]db.TodoItem, 0, 8)
	for i := 0; i < 8; i++ {
		todos = append(todos, db.TodoItem{Content: "todo item", Status: native.TodoPending})
	}
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	top, _, ok := m.todoPanelBand()
	if !ok {
		t.Fatal("expected a todo panel band after seeding todos")
	}

	// First click: expanded → collapsed.
	u1, _ := m.Update(tea.MouseClickMsg{X: 2, Y: top, Button: tea.MouseLeft})
	m1 := asModel(t, u1)
	if m1.todoPanelMode != todoPanelCollapsed {
		t.Fatalf("first click mode = %v, want collapsed", m1.todoPanelMode)
	}
	// The band moves when the panel collapses (viewport height changes);
	// recompute it for the next click.
	top1, _, ok1 := m1.todoPanelBand()
	if !ok1 {
		t.Fatal("expected a todo panel band after first click")
	}
	// Second click: collapsed → expanded (NOT hidden).
	u2, _ := m1.Update(tea.MouseClickMsg{X: 2, Y: top1, Button: tea.MouseLeft})
	m2 := asModel(t, u2)
	if m2.todoPanelMode != todoPanelExpanded {
		t.Fatalf("second click mode = %v, want expanded (never hidden)", m2.todoPanelMode)
	}
	// Third click: back to collapsed.
	top2, _, ok2 := m2.todoPanelBand()
	if !ok2 {
		t.Fatal("expected a todo panel band after second click")
	}
	u3, _ := m2.Update(tea.MouseClickMsg{X: 2, Y: top2, Button: tea.MouseLeft})
	m3 := asModel(t, u3)
	if m3.todoPanelMode != todoPanelCollapsed {
		t.Fatalf("third click mode = %v, want collapsed", m3.todoPanelMode)
	}
}

// A lane click is the mouse route to the running children. Task 14 changed where
// it goes — it opened one child's transcript directly, and now it opens the
// Agents tab that lists them all — so this asserts the destination rather than
// the drill.
//
// This replaces TestAgentLaneClickDrillsIn. The property worth keeping from it is
// the one it really guarded: the band announces itself as clickable and then does
// something when clicked.
func TestAgentLaneClickOpensTheAgentsTabViaTheClickRouter(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()

	top, _, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("expected an agent lane band")
	}
	if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top}); !handled {
		t.Fatal("a click on the lane must be handled")
	}
	if !m.inspector.isRendering() {
		t.Fatal("a lane click must open the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabAgents {
		t.Fatalf("the lane click opened %q, want the Agents tab", got)
	}
}

// EVERY row of the band opens the inspector, including the separator and the
// count.
//
// Before Task 14 those two rows were chrome and did nothing, while the agent
// rows below them drilled in. With one count row there is no "below them", so a
// reader who clicks the rule — an unsurprising thing to do — must not find the
// band half dead.
//
// This replaces TestAgentLaneClickOnChromeDoesNothing.
func TestAgentLaneClickOnEveryRowDoesSomething(t *testing.T) {
	for _, offset := range []int{0, 1} {
		m := newTestModel(t)
		child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
		m.state.RegisterSubagent("reviewer", child)
		m.refreshViewport()
		top, _, _ := m.agentLaneBand()

		if _, handled := m.handleAgentLaneClick(tea.MouseClickMsg{
			Button: tea.MouseLeft, X: 1, Y: top + offset,
		}); !handled {
			t.Fatalf("a click on lane row %d must be handled", offset)
		}
		if !m.inspector.isRendering() {
			t.Fatalf("a click on lane row %d must open the inspector", offset)
		}
	}
}

// The band must sit directly below the live strip (the job lane no longer
// exists as a separate stacked row).
func TestLaneBandSitsBelowLiveStrip(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m.state.RegisterSubagent("reviewer", child)
	m.jobs = []native.JobInfo{runningJob(1, "go test ./...", time.Second)}
	m.refreshViewport()
	top, _, ok := m.agentLaneBand()
	if !ok {
		t.Fatal("expected a band")
	}
	want := m.scrollHintRows() + m.breadcrumbRows() + m.viewport.Height() +
		m.turnSpinnerRows() + m.todoPanelRows() + m.liveStripRows()
	if top != want {
		t.Fatalf("band top = %d, want %d (lane must sit below the live strip)", top, want)
	}
}

func TestNoAgentLaneNoBand(t *testing.T) {
	m := newTestModel(t)
	if _, _, ok := m.agentLaneBand(); ok {
		t.Fatal("no running agents means no band")
	}
}
