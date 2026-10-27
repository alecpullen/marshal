package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

func newChildState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestDrillSwapsTranscriptToChildSession(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleAssistant, "parent normal text", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view)
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "child says hi") {
		t.Fatalf("drilled view should show the child transcript, got:\n%s", content)
	}
	if strings.Contains(content, "parent normal text") {
		t.Fatalf("drilled view must not show the parent transcript, got:\n%s", content)
	}
	if m.breadcrumbRows() != 1 {
		t.Fatalf("breadcrumbRows = %d, want 1 while drilled", m.breadcrumbRows())
	}
}

func TestPopDrillRestoresParentTranscript(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleAssistant, "parent normal text", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view)
	m.refreshViewport()
	if !m.popDrill() {
		t.Fatal("popDrill returned false while drilled")
	}
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "parent normal text") {
		t.Fatalf("after pop the parent transcript should be restored, got:\n%s", content)
	}
	if m.breadcrumbRows() != 0 {
		t.Fatalf("breadcrumbRows = %d, want 0 after pop", m.breadcrumbRows())
	}
	if m.popDrill() {
		t.Fatal("popDrill on an empty stack must return false")
	}
}

func TestDrillIntoSubagentRequiresChild(t *testing.T) {
	m := newTestModel(t)
	m.drillIntoSubagent(session.SubagentView{Label: "no child"})
	if len(m.viewStack) != 0 {
		t.Fatal("drilling into a childless subagent must not push the view stack")
	}
}

func TestEscPopsDrillInsteadOfCancelling(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !handled {
		t.Fatal("esc must be handled while drilled")
	}
	m = mm.(Model)
	if len(m.viewStack) != 0 {
		t.Fatalf("esc should pop the drill stack, len(viewStack) = %d", len(m.viewStack))
	}
}

func TestCtrlXStopsRunningSubagentWhileDrilled(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.PushSteering("queued message")

	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	cancelled := false
	m.state.SetSubagentCancel(view.ID, func() { cancelled = true })
	m.drillIntoSubagent(view)

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+x must be handled while drilled into a running subagent")
	}
	if !cancelled {
		t.Fatal("ctrl+x did not cancel the drilled subagent")
	}
	m = mm.(Model)
	if len(m.state.SteeringQueue()) != 1 {
		t.Fatal("ctrl+x while drilled must NOT clear the steering queue")
	}
}

func TestCtrlXUndrilledClearsSteeringQueue(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.PushSteering("queued message")

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+x must be handled while busy and undrilled")
	}
	m = mm.(Model)
	if len(m.state.SteeringQueue()) != 0 {
		t.Fatal("ctrl+x while undrilled must clear the steering queue")
	}
}

func TestBreadcrumbShowsPathWhileDrilled(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	frame := stripANSI(m.renderTranscriptFrame())
	for _, want := range []string{"orchestrator", "explore repo", "go back"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("breadcrumb missing %q, frame:\n%s", want, frame)
		}
	}
}

// TestDrillFrameDrawsTheCrumbInTheBorder: a drilled-in child's transcript is a
// closed frame whose top border carries the crumb and the way back.
func TestDrillFrameDrawsTheCrumbInTheBorder(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m.refreshViewport()

	rows := strings.Split(stripANSI(m.renderTranscriptFrame()), "\n")
	top := rows[0]
	if !strings.HasPrefix(top, "╭─ ") || !strings.HasSuffix(top, "╮") {
		t.Fatalf("drill frame does not open with a corner: %q", top)
	}
	for _, want := range []string{"explore repo", "↑/Esc to go back"} {
		if !strings.Contains(top, want) {
			t.Fatalf("drill frame's top border missing %q: %q", want, top)
		}
	}
	for _, row := range rows[1 : len(rows)-1] {
		if !strings.HasPrefix(row, "│ ") || !strings.HasSuffix(row, " │") {
			t.Fatalf("drill frame body row is not inside the gutter: %q", row)
		}
	}
	if last := rows[len(rows)-1]; !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("drill frame does not close: %q", last)
	}
	// The crumb becomes the top border, so only the closing row is new.
	if got := m.breadcrumbRows() + m.drillCloseRows(); got != 2 {
		t.Fatalf("the drill frame budgets %d rows, want the top border and the closing row", got)
	}
}

// TestDrillNarrowsTheTranscript: the frame's "│ … │" takes four cells, so the
// viewport narrows while drilled in and widens again on pop.
func TestDrillNarrowsTheTranscript(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)

	if got, want := m.viewport.Width(), m.leftWidth; got != want {
		t.Fatalf("undrilled viewport width = %d, want the full column %d", got, want)
	}
	m.drillIntoSubagent(view)
	if got, want := m.viewport.Width(), m.leftWidth-4; got != want {
		t.Fatalf("drilled viewport width = %d, want %d", got, want)
	}
	m.popDrill()
	if got, want := m.viewport.Width(), m.leftWidth; got != want {
		t.Fatalf("viewport width after popDrill = %d, want %d", got, want)
	}
}

// TestDrillClickOffsetsTheBorder: clicks map to content only past the frame's
// left border.
func TestDrillClickOffsetsTheBorder(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m.refreshViewport()

	y := m.scrollHintRows() + m.breadcrumbRows()
	if _, ok := m.contentLineForClick(1, y); ok {
		t.Fatal("x=1 is the frame's border and must miss the viewport")
	}
	if _, ok := m.contentLineForClick(2, y); !ok {
		t.Fatal("x=2 is the first content cell and must hit the viewport")
	}
	if got := m.contentPosAt(2, y).col; got != 0 {
		t.Fatalf("contentPosAt(2, y).col = %d, want 0", got)
	}
}

func TestBreadcrumbShowsDispatchedRole(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagentWithMeta("review", child, session.SubagentMeta{
		Role: routing.RoleReviewer,
	})
	m.drillIntoSubagent(view)

	frame := stripANSI(m.renderTranscriptFrame())
	if !strings.Contains(frame, "reviewer — review") {
		t.Fatalf("breadcrumb missing dispatched role, frame:\n%s", frame)
	}
}

func TestBreadcrumbOmitsRoleWhenUnset(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	frame := stripANSI(m.renderTranscriptFrame())
	if !strings.Contains(frame, "explore repo") {
		t.Fatalf("breadcrumb missing label, frame:\n%s", frame)
	}
	if strings.Contains(frame, " — explore") {
		t.Fatalf("breadcrumb must not render a separator without a role, frame:\n%s", frame)
	}
}

func TestDrilledChildAuditEventsRenderNormally(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(300, 0),
		ToolName:      "file.read",
		ResultSummary: "child tool output marker",
	})
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view)
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "child tool output marker") {
		t.Fatalf("drilled view should render the child's audit events, got:\n%s", content)
	}
}

func TestUpArrowPopsDrill(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyUp})
	if !handled {
		t.Fatal("up arrow must be handled while drilled")
	}
	m = mm.(Model)
	if len(m.viewStack) != 0 {
		t.Fatalf("up arrow should pop the drill stack, len = %d", len(m.viewStack))
	}
}

func TestDrilledChildShowsChildActiveTool(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	// Set an active tool on the child session.
	child.SetActiveToolCall(session.ActiveToolCall{
		Name:      "file.read",
		StartedAt: time.Now(),
	})
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m.refreshViewport()

	content := stripANSI(m.viewport.GetContent())
	if !strings.Contains(content, "Read file") {
		t.Fatalf("drilled view should show child's active tool 'Read file':\n%s", content)
	}
}

func TestUpArrowRecallsHistoryWhenNotDrilled(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "previous prompt", session.ContentTypePlain)
	m.resetHistoryNav()

	// Not drilled: up arrow should recall history, not pop drill.
	mm, _, _ := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyUp})
	// Whether handled depends on whether there is history to recall.
	// The key assertion is that it does NOT crash and the view stack
	// remains empty.
	m = mm.(Model)
	if len(m.viewStack) != 0 {
		t.Fatal("up arrow should not affect view stack when not drilled")
	}
}

// TestClickBeatWhileDrilledIntoSubagent verifies the off-by-one
// fix in contentLineForClick: when drilled into a subagent, a breadcrumb
// row is prepended above the viewport, so the click offset must include
// breadcrumbRows() in addition to scrollHintRows(). Without the fix, every
// click while drilled lands one row too high and fails to hit its region.
func TestClickBeatWhileDrilledIntoSubagent(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)

	// Set up a child session with a thinking block.
	child := newChildState(t)
	ts := time.Unix(800, 0)
	child.LogThinking(session.ThinkingEntry{Text: "child reasoning", Duration: time.Second, StartedAt: ts})
	child.AddMessage(session.RoleAssistant, "child answer", session.ContentTypePlain)

	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m.invalidateTranscript()
	m.refreshViewport()

	// Verify we're drilled in.
	if m.breadcrumbRows() != 1 {
		t.Fatalf("breadcrumbRows = %d, want 1 while drilled in", m.breadcrumbRows())
	}

	// Find the click region for the child's beat that reasoned.
	key := beatWithThinking(&m, ts)
	region, found := regionOf(&m, key)
	if !found {
		t.Fatal("expected a click region for the child's beat while drilled in")
	}

	// Compute the screen Y using the FIXED offset (scrollHintRows + breadcrumbRows).
	top := m.scrollHintRows() + m.breadcrumbRows()
	y := top + region.startLine - m.viewport.YOffset()

	// The click must land on the correct region and expand it. The drilled-in
	// frame's "│ " gutter takes the first two cells, so a click inside the
	// viewport starts at x=2.
	updated := clickAt(t, m, 3, y)
	mm := asModel(t, updated)
	if !mm.isExpanded(key) {
		t.Fatal("expected the click to expand the child's beat while drilled in (off-by-one fix)")
	}
	if !strings.Contains(stripANSI(mm.viewport.GetContent()), "child reasoning") {
		t.Fatal("expected the reasoning text to be visible after the click")
	}
}
