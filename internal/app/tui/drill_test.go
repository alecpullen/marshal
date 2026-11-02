package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/help"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

func newChildState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

// newChildStateWithID is newChildState with the child's session ID set: a work
// item's reference names the child by that ID, not by the registered view.
func newChildStateWithID(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

func TestDrillSwapsTranscriptToChildSession(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleAssistant, "parent normal text", session.ContentTypePlain)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view.ID)
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

	m.drillIntoSubagent(view.ID)
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

func TestDrillIntoAnUnknownSubagentDoesNothing(t *testing.T) {
	m := newTestModel(t)
	if m.drillIntoSubagent(987654321) {
		t.Fatal("drilling into an ID Status does not name must report that nothing was pushed")
	}
	if len(m.viewStack) != 0 {
		t.Fatal("drilling into an ID Status does not name must not push the view stack")
	}
}

func TestWorkOriginDrillsIntoItsSubagent(t *testing.T) {
	m := newTestModel(t)
	child := newChildStateWithID(t, "child-1")
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)
	m.workSnapshot = acpwire.WorkSnapshot{Items: []acpwire.WorkItem{{
		TaskKey:   "work:agent",
		Reference: acpwire.WorkReference{ChildSessionID: "child-1"},
	}}}

	m, _ = m.jumpToWorkOrigin("work:agent")

	if len(m.viewStack) != 1 {
		t.Fatalf("viewStack = %d views, want one after jumping to a live child", len(m.viewStack))
	}
	if got := m.viewStack[0].ID; got != view.ID {
		t.Fatalf("drilled into %d, want the registered view %d", got, view.ID)
	}
}

func TestWorkOriginWithAGoneChildFallsThrough(t *testing.T) {
	m := newTestModel(t)
	m.workSnapshot = acpwire.WorkSnapshot{Items: []acpwire.WorkItem{{
		TaskKey:   "work:agent",
		Reference: acpwire.WorkReference{ChildSessionID: "gone"},
	}}}

	m, _ = m.jumpToWorkOrigin("work:agent")

	if len(m.viewStack) != 0 {
		t.Fatalf("viewStack = %d views, want none for a child the session released", len(m.viewStack))
	}
	if got := lastMessage(t, m); got != "The referenced transcript position is no longer available." {
		t.Fatalf("last message = %q, want the unavailable transcript text", got)
	}
}

// drillIntoSubagent reports whether it pushed, so a caller that reached it by a
// stale reference can fall through instead of silently doing nothing.
func TestDrillIntoASubagentReportsThePush(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child says hi", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore repo", child)

	if !m.drillIntoSubagent(view.ID) {
		t.Fatal("drilling into a registered subagent must report the push")
	}
	if len(m.viewStack) != 1 {
		t.Fatalf("viewStack = %v, want one level", m.viewStack)
	}
}

// A drilled view reads the child's facts from its drill: the root a completion
// row elides, the route a beat's model is compared against, and the reconnect
// notice. A child's state carries no sandbox info, so a drilled shell row shows
// none (decision 7).
func TestDrilledViewReadsTheChildsFacts(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.SetWorkspace(session.Workspace{ActiveRoot: "/repo/wt"})
	child.SetActiveRoute(session.RouteInfo{Active: true, Model: "m-child", Provider: "p-child"})
	child.SetActivity(session.Activity{Kind: session.ActivityReconnecting, Label: "retrying in 2s"})
	view := m.state.RegisterSubagent("explore repo", child)
	m.state.SetSandboxInfo(session.SandboxInfo{Backend: "restricted", NetworkIsolation: true})

	m.drillIntoSubagent(view.ID)
	facts := m.viewFacts()
	if facts.workdir != "/repo/wt" {
		t.Errorf("workdir = %q, want the child's active root", facts.workdir)
	}
	if facts.routeModel != "m-child" || facts.routeProvider != "p-child" {
		t.Errorf("route = %s @ %s, want the child's", facts.routeModel, facts.routeProvider)
	}
	if facts.reconnect != "retrying in 2s" {
		t.Errorf("reconnect = %q, want the child's label", facts.reconnect)
	}
	if facts.sandbox != (acpwire.Sandbox{}) {
		t.Errorf("sandbox = %+v, want none for a drilled child", facts.sandbox)
	}

	m.invalidateTranscript()
	m.refreshViewport()
	if plain := stripANSI(m.viewport.GetContent()); !strings.Contains(plain, "retrying in 2s") {
		t.Fatalf("the drilled transcript must show the child's reconnect notice, got:\n%s", plain)
	}
}

// The stack holds the view as it was at drill-in, but the facts follow the live
// view each refresh.
func TestDrilledFactsFollowTheLiveChild(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	child.SetWorkspace(session.Workspace{ActiveRoot: "/repo/wt"})
	view := m.state.RegisterSubagent("explore repo", child)

	m.drillIntoSubagent(view.ID)
	child.SetWorkspace(session.Workspace{ActiveRoot: "/repo/wt2"})
	if got := m.viewFacts().workdir; got != "/repo/wt2" {
		t.Fatalf("workdir = %q, want the live child's root", got)
	}
	if got := m.viewStack[0].Drill.ActiveRoot; got != "/repo/wt" {
		t.Fatalf("the stack's snapshot root = %q, want /repo/wt", got)
	}
}

func TestEscPopsDrillInsteadOfCancelling(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view.ID)

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

	// Two running children: only the drilled-in top may be stopped.
	other := newChildState(t)
	otherView := m.state.RegisterSubagent("other task", other)
	otherCancelled := false
	m.state.SetSubagentCancel(otherView.ID, func() { otherCancelled = true })

	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	cancelled := false
	m.state.SetSubagentCancel(view.ID, func() { cancelled = true })
	m.drillIntoSubagent(view.ID)

	if got := m.ctrlXAction(); got != ctrlXStopAgent {
		t.Fatalf("ctrlXAction = %v, want ctrlXStopAgent while drilled into a running subagent", got)
	}

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+x must be handled while drilled into a running subagent")
	}
	if !cancelled {
		t.Fatal("ctrl+x did not cancel the drilled subagent")
	}
	if otherCancelled {
		t.Fatal("ctrl+x cancelled the wrong subagent: only the drilled-in top is the target")
	}
	m = mm.(Model)
	if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "queued message" {
		t.Fatalf("ctrl+x while drilled must NOT clear the steering queue, queue = %v", q)
	}
	if m.flash != "" {
		t.Fatalf("a successful stop must not flash a failure, flash = %q", m.flash)
	}
}

func TestCtrlXUndrilledClearsSteeringQueue(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.PushSteering("queued message")

	// A running child exists but is not drilled into: the queue clear must not
	// stop it.
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	cancelled := false
	m.state.SetSubagentCancel(view.ID, func() { cancelled = true })

	if got := m.ctrlXAction(); got != ctrlXClearQueue {
		t.Fatalf("ctrlXAction = %v, want ctrlXClearQueue while busy and undrilled", got)
	}

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("ctrl+x must be handled while busy and undrilled")
	}
	if cancelled {
		t.Fatal("ctrl+x while undrilled must not stop a running subagent")
	}
	m = mm.(Model)
	if len(m.state.SteeringQueue()) != 0 {
		t.Fatal("ctrl+x while undrilled must clear the steering queue")
	}
}

// TestCtrlXFailedDrilledStopKeepsQueue: the view stack holds a snapshot, so a
// stop can fail. A refused stop consumes the key, leaves the queue alone and
// says so instead of claiming a stop that did not happen.
func TestCtrlXFailedDrilledStopKeepsQueue(t *testing.T) {
	const wantFlash = "Agent could not be stopped; queue unchanged"

	t.Run("finished child behind a stale running snapshot", func(t *testing.T) {
		m := newTestModel(t)
		m.busy = true

		child := newChildState(t)
		view := m.state.RegisterSubagent("explore repo", child)
		cancelled := false
		m.state.SetSubagentCancel(view.ID, func() { cancelled = true })
		// The snapshot says running, but the child settles before the key.
		m.drillIntoSubagent(view.ID)
		m.state.FinishSubagent(view.ID, "finished on its own", nil)
		m.state.PushSteering("queued message")

		if got := m.ctrlXAction(); got != ctrlXStopAgent {
			t.Fatalf("ctrlXAction = %v, want ctrlXStopAgent for the stale snapshot", got)
		}

		mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		if !handled {
			t.Fatal("a refused stop must still consume ctrl+x")
		}
		if cancelled {
			t.Fatal("a child that already finished must not be cancelled")
		}
		m = mm.(Model)
		if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "queued message" {
			t.Fatalf("a failed stop must leave the queue intact, queue = %v", q)
		}
		if m.flash != wantFlash {
			t.Fatalf("flash = %q, want %q", m.flash, wantFlash)
		}
	})

	t.Run("running child without a cancel handle", func(t *testing.T) {
		m := newTestModel(t)
		m.busy = true

		child := newChildState(t)
		view := m.state.RegisterSubagent("explore repo", child)
		// No SetSubagentCancel: the child is running but cannot be stopped.
		m.drillIntoSubagent(view.ID)
		m.state.PushSteering("queued message")

		mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		if !handled {
			t.Fatal("a refused stop must still consume ctrl+x")
		}
		m = mm.(Model)
		if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "queued message" {
			t.Fatalf("a failed stop must leave the queue intact, queue = %v", q)
		}
		if m.flash != wantFlash {
			t.Fatalf("flash = %q, want %q", m.flash, wantFlash)
		}
		if got := m.state.Subagents()[0].Status; got != session.SubagentRunning {
			t.Fatalf("subagent status = %v, want it still running", got)
		}
	})
}

// TestCtrlXCompletedDrillAndIdle: a completed snapshot is not a stop target, so
// Ctrl+X falls back to the busy queue action, while idle Ctrl+X does nothing.
func TestCtrlXCompletedDrillAndIdle(t *testing.T) {
	t.Run("completed snapshot clears the busy queue", func(t *testing.T) {
		m := newTestModel(t)
		m.busy = true

		child := newChildState(t)
		view := m.state.RegisterSubagent("explore repo", child)
		m.state.FinishSubagent(view.ID, "done", nil)
		completed := m.state.Subagents()[0]
		m.drillIntoSubagent(completed.ID)
		m.state.PushSteering("queued message")

		if got := m.ctrlXAction(); got != ctrlXClearQueue {
			t.Fatalf("ctrlXAction = %v, want ctrlXClearQueue for a completed snapshot", got)
		}

		mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		if !handled {
			t.Fatal("ctrl+x must be handled while busy")
		}
		m = mm.(Model)
		if len(m.state.SteeringQueue()) != 0 {
			t.Fatalf("a completed drill must use the busy queue action, queue = %v", m.state.SteeringQueue())
		}
		if m.flash != "" {
			t.Fatalf("a completed drill must not flash a stop failure, flash = %q", m.flash)
		}
	})

	t.Run("idle does not clear the queue", func(t *testing.T) {
		m := newTestModel(t)
		m.state.PushSteering("queued message")

		if got := m.ctrlXAction(); got != ctrlXNone {
			t.Fatalf("ctrlXAction = %v, want ctrlXNone while idle", got)
		}

		mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		if handled {
			t.Fatal("ctrl+x must fall through to the textarea while idle")
		}
		m = mm.(Model)
		if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "queued message" {
			t.Fatalf("idle ctrl+x must not clear the queue, queue = %v", q)
		}
	})
}

func TestBreadcrumbShowsPathWhileDrilled(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view.ID)

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
	m.drillIntoSubagent(view.ID)
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
	m.drillIntoSubagent(view.ID)
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
	m.drillIntoSubagent(view.ID)
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
	m.drillIntoSubagent(view.ID)

	frame := stripANSI(m.renderTranscriptFrame())
	if !strings.Contains(frame, "reviewer — review") {
		t.Fatalf("breadcrumb missing dispatched role, frame:\n%s", frame)
	}
}

func TestBreadcrumbOmitsRoleWhenUnset(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view.ID)

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

	m.drillIntoSubagent(view.ID)
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
	m.drillIntoSubagent(view.ID)

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
	m.drillIntoSubagent(view.ID)
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
	m.drillIntoSubagent(view.ID)
	m.invalidateTranscript()
	m.refreshViewport()

	// Verify we're drilled in.
	if m.breadcrumbRows() != 1 {
		t.Fatalf("breadcrumbRows = %d, want 1 while drilled in", m.breadcrumbRows())
	}

	// Find the click region for the child's beat that reasoned.
	key := beatWithThinking(&m, child, ts)
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

// ctrlXStubPanel is the smallest dock panel that owns the keyboard: the dock
// forwards every keypress to it, which is all these tests need.
type ctrlXStubPanel struct{}

func (ctrlXStubPanel) Update(tea.Msg) tea.Cmd { return nil }
func (ctrlXStubPanel) View(int, int) string   { return "" }
func (ctrlXStubPanel) Sizing() dock.Sizing    { return dock.Docked }

// ctrlXFrameCounts counts the Ctrl+X guidance a rendered frame displays: the
// key itself, and each of the two labels. A frame may show the pair once or
// not at all; total == stop+clear is what rules out a fragment.
func ctrlXFrameCounts(frame string) (total, stop, clear int) {
	return strings.Count(frame, "Ctrl+X"),
		strings.Count(frame, "Ctrl+X stop agent"),
		strings.Count(frame, "Ctrl+X clear queue")
}

// sendCtrlX drives the key through Model.Update, the way the terminal does, so
// browse routing, prompt ownership and dock ownership all apply.
func sendCtrlX(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	return asModel(t, updated)
}

// TestCtrlXDisplayedActionThroughModel: the composed frame and Model.Update
// must agree on what Ctrl+X does. The hint is shown at most once, names the
// action the key actually performs, and is never a fragment or a promise the
// key does not keep.
func TestCtrlXDisplayedActionThroughModel(t *testing.T) {
	// The busy hint cluster is long (81 cells with the queue pair and "? help"),
	// so it only fits beside the identity cluster on a genuinely wide row.
	// 80x24 exercises the shed path instead.
	geometries := []struct {
		name string
		w, h int
	}{
		{"wide", 200, 50},
		{"80x24", 80, 24},
	}
	cases := []struct {
		name string
		busy bool
		// drill is "", "running" or "done": whether the user is drilled into a
		// running, a completed, or no subagent when the key arrives.
		drill string
		// wantHint is the label the frame may display ("" for none).
		wantHint string
		// wantQueue is the steering queue length after the key.
		wantQueue int
		// wantCancel reports whether the drilled child must be stopped.
		wantCancel bool
	}{
		{name: "undrilled busy clears the queue", busy: true, wantHint: "clear queue"},
		{name: "running drill stops that agent", busy: true, drill: "running", wantHint: "stop agent", wantQueue: 1, wantCancel: true},
		{name: "completed drill clears the queue", busy: true, drill: "done", wantHint: "clear queue"},
		{name: "idle does nothing", wantQueue: 1},
	}

	for _, g := range geometries {
		for _, tc := range cases {
			t.Run(g.name+"/"+tc.name, func(t *testing.T) {
				m := newTestModel(t)
				m.resize(g.w, g.h)

				// Two live children: only the drilled-in one may ever be the
				// stop target.
				child := newChildState(t)
				view := m.state.RegisterSubagent("explore repo", child)
				cancelled := false
				m.state.SetSubagentCancel(view.ID, func() { cancelled = true })
				other := m.state.RegisterSubagent("other task", newChildState(t))
				otherCancelled := false
				m.state.SetSubagentCancel(other.ID, func() { otherCancelled = true })

				switch tc.drill {
				case "running":
					m.drillIntoSubagent(view.ID)
				case "done":
					m.state.FinishSubagent(view.ID, "done", nil)
					for _, v := range m.state.Subagents() {
						if v.ID == view.ID {
							m.drillIntoSubagent(v.ID)
						}
					}
				}

				m.busy = tc.busy
				m.state.PushSteering("queued message")
				m.refreshViewport()

				frame := stripANSI(m.viewString())
				total, stop, clear := ctrlXFrameCounts(frame)
				if total != stop+clear {
					t.Fatalf("frame left a Ctrl+X fragment:\n%s", frame)
				}
				if total > 1 {
					t.Fatalf("frame shows %d Ctrl+X hints, want at most 1:\n%s", total, frame)
				}
				if tc.wantHint == "" {
					if total != 0 {
						t.Fatalf("frame advertised Ctrl+X with no action:\n%s", frame)
					}
				} else if total == 1 {
					if got := map[string]int{"stop agent": stop, "clear queue": clear}[tc.wantHint]; got != 1 {
						t.Fatalf("displayed hint does not name %q:\n%s", tc.wantHint, frame)
					}
				} else if tc.busy && !strings.Contains(frame, "Ctrl+C stop") {
					// The hint may be shed for width, but the stop fallback
					// that replaces it must then be there.
					t.Fatalf("busy frame dropped Ctrl+X without the Ctrl+C stop fallback:\n%s", frame)
				}

				m = sendCtrlX(t, m)

				if otherCancelled {
					t.Fatal("ctrl+x stopped a subagent the user was not drilled into")
				}
				if cancelled != tc.wantCancel {
					t.Fatalf("drilled child cancelled = %v, want %v", cancelled, tc.wantCancel)
				}
				if q := len(m.state.SteeringQueue()); q != tc.wantQueue {
					t.Fatalf("steering queue = %d, want %d", q, tc.wantQueue)
				}
				// The displayed hint must name the action the key performed.
				if stop == 1 && cancelled != true {
					t.Fatal("frame advertised a stop the key did not perform")
				}
				if clear == 1 && len(m.state.SteeringQueue()) != 0 {
					t.Fatal("frame advertised a queue clear the key did not perform")
				}
			})
		}
	}

	// Browse mode declines Ctrl chords, so the key still reaches the global
	// handler — but the status row shows the browse cue, not the hint cluster.
	t.Run("browsing", func(t *testing.T) {
		for _, g := range geometries {
			m := newTestModel(t)
			m.resize(g.w, g.h)
			m.state.AddMessage(session.RoleAssistant, "hello", session.ContentTypePlain)
			m.busy = true
			m.state.PushSteering("queued message")
			m.refreshViewport()
			if !m.enterBrowse() {
				t.Fatal("could not enter browse mode")
			}

			frame := stripANSI(m.viewString())
			if total, _, _ := ctrlXFrameCounts(frame); total != 0 {
				t.Fatalf("browse frame advertised the global Ctrl+X hint:\n%s", frame)
			}
			if got := m.footerHints().CtrlXHint; got != help.CtrlXHintClearQueue {
				t.Fatalf("browse footer hint = %v, want the real clear-queue action", got)
			}

			m = sendCtrlX(t, m)
			if q := len(m.state.SteeringQueue()); q != 0 {
				t.Fatalf("ctrl+x in browse must reach the global handler, queue = %d", q)
			}
		}
	})

	// Resizing may shed the hint, but only whole: a fragment is never allowed
	// and the stop fallback replaces a shed pair.
	t.Run("resize", func(t *testing.T) {
		m := newTestModel(t)
		m.resize(200, 50)
		m.busy = true
		m.state.PushSteering("queued message")
		m.refreshViewport()
		if frame := stripANSI(m.viewString()); !strings.Contains(frame, "Ctrl+X clear queue") {
			t.Fatalf("wide frame lost the whole clear-queue hint:\n%s", frame)
		}

		// 80x24 is the minimum supported terminal; anything smaller renders the
		// too-small view instead of a frame.
		for _, size := range []struct{ w, h int }{{140, 40}, {120, 40}, {100, 30}, {80, 24}} {
			m.resize(size.w, size.h)
			m.refreshViewport()
			frame := stripANSI(m.viewString())
			total, stop, clear := ctrlXFrameCounts(frame)
			if total != stop+clear {
				t.Fatalf("%dx%d left a Ctrl+X fragment:\n%s", size.w, size.h, frame)
			}
			if total == 0 && !strings.Contains(frame, "Ctrl+C stop") {
				t.Fatalf("%dx%d dropped Ctrl+X without the Ctrl+C stop fallback:\n%s", size.w, size.h, frame)
			}
		}

		m.resize(200, 50)
		m.refreshViewport()
		if frame := stripANSI(m.viewString()); !strings.Contains(frame, "Ctrl+X clear queue") {
			t.Fatalf("widening again did not restore the whole hint:\n%s", frame)
		}
	})
}

// TestCtrlXStaleSubagentNeverClearsQueueThroughModel: the hint is rendered
// from a snapshot, so a child can settle between the render and the key. The
// key must then report itself and leave the queue alone, and a surface in
// front of the global handler must neither advertise the hint nor leak the key
// into the global action.
func TestCtrlXStaleSubagentNeverClearsQueueThroughModel(t *testing.T) {
	t.Run("stale running snapshot", func(t *testing.T) {
		m := newTestModel(t)
		// Wide enough for the busy cluster plus the identity cluster, so the
		// running-drill hint is actually displayed.
		m.resize(200, 50)
		m.busy = true

		child := newChildState(t)
		view := m.state.RegisterSubagent("explore repo", child)
		cancelled := false
		m.state.SetSubagentCancel(view.ID, func() { cancelled = true })
		m.drillIntoSubagent(view.ID)
		m.state.PushSteering("queued message")
		m.refreshViewport()

		if frame := stripANSI(m.viewString()); !strings.Contains(frame, "Ctrl+X stop agent") {
			t.Fatalf("a running drill must advertise the stop:\n%s", frame)
		}

		// The child settles on its own after the frame was drawn.
		m.state.FinishSubagent(view.ID, "finished on its own", nil)
		m = sendCtrlX(t, m)

		if cancelled {
			t.Fatal("a child that already finished must not be cancelled")
		}
		if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "queued message" {
			t.Fatalf("a refused stop must leave the queue intact, queue = %v", q)
		}
		if frame := stripANSI(m.viewString()); !strings.Contains(frame, "Agent could not be stopped; queue unchanged") {
			t.Fatalf("the refused stop did not report itself:\n%s", frame)
		}
	})

	t.Run("pending prompt owns the key", func(t *testing.T) {
		m := newTestModel(t)
		m.resize(140, 40)
		m.busy = true
		m.state.PushSteering("queued message")
		m.state.SetPendingQuestion(&session.PendingQuestion{
			Questions: []session.Question{{Question: "what?"}},
		})
		m.refreshViewport()

		if frame := stripANSI(m.viewString()); strings.Contains(frame, "Ctrl+X") {
			t.Fatalf("a pending prompt must not advertise the global hint:\n%s", frame)
		}
		m = sendCtrlX(t, m)
		if q := m.state.SteeringQueue(); len(q) != 1 {
			t.Fatalf("ctrl+x leaked into the global action under a prompt, queue = %v", q)
		}
	})

	t.Run("docked panel owns the key", func(t *testing.T) {
		m := newTestModel(t)
		m.resize(140, 40)
		m.busy = true
		m.state.PushSteering("queued message")
		m.dock.Open(ctrlXStubPanel{})
		m.refreshViewport()

		if frame := stripANSI(m.viewString()); strings.Contains(frame, "Ctrl+X") {
			t.Fatalf("an open dock must not advertise the global hint:\n%s", frame)
		}
		m = sendCtrlX(t, m)
		if q := m.state.SteeringQueue(); len(q) != 1 {
			t.Fatalf("ctrl+x leaked into the global action behind a dock, queue = %v", q)
		}
	})
}
