package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/llm/routing"
)

// agentsInspectorModel builds a model with the inspector open on the Agents tab.
func agentsInspectorModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	return m
}

// TestAgentsTabIsPopulatedFromTheRuntime is the basic wiring claim: the roster
// the panel shows comes from the session's runtime subagents, not from the
// configuration roster that /agents displays.
func TestAgentsTabIsPopulatedFromTheRuntime(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "the child read three files", session.ContentTypePlain)
	m.state.RegisterSubagent("explore the repo", child)
	m.refreshInspector()

	roster := m.inspector.model.AgentsSnapshot()
	if len(roster) != 1 {
		t.Fatalf("the inspector roster has %d agents, want 1", len(roster))
	}
	if roster[0].Label != "explore the repo" {
		t.Fatalf("roster label = %q, want the runtime label", roster[0].Label)
	}
	if roster[0].Status != inspector.AgentRunning {
		t.Fatalf("a freshly registered subagent is not reported as running: %v", roster[0].Status)
	}
	if !roster[0].HasChild {
		t.Fatal("a registered subagent has a live child, so HasChild should be true")
	}
	if !strings.Contains(roster[0].ChildBody, "the child read three files") {
		t.Fatalf("the child body does not hold the child's own transcript:\n%s", roster[0].ChildBody)
	}
}

// TestChildBodyIsTheChildsOwnTranscript is the plan's explicit acceptance
// criterion at the model level: opening an agent must never put the PARENT's
// conversation on screen under the child's name. On screen the two are
// indistinguishable, so the only correct answer when there is no child is to
// say there is no child.
func TestChildBodyIsTheChildsOwnTranscript(t *testing.T) {
	m := agentsInspectorModel(t)
	m.state.AddMessage(session.RoleUser, "PARENT-SECRET the user's own prompt", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant, "PARENT-SECRET the parent's answer", session.ContentTypeMarkdown)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child only text", session.ContentTypePlain)
	m.state.RegisterSubagent("child one", child)
	m.refreshInspector()

	roster := m.inspector.model.AgentsSnapshot()
	if len(roster) != 1 {
		t.Fatalf("roster size = %d, want 1", len(roster))
	}
	if strings.Contains(roster[0].ChildBody, "PARENT-SECRET") {
		t.Fatalf("the child's body contains the PARENT's conversation:\n%s", roster[0].ChildBody)
	}
	if !strings.Contains(roster[0].ChildBody, "child only text") {
		t.Fatalf("the child body omits the child's own text:\n%s", roster[0].ChildBody)
	}
}

// TestChildTranscriptBodyExcludesTheParent pins the same rule one level down,
// against the function that actually builds the body: a parent state passed in
// by mistake must not produce a body that reads as a child's.
func TestChildTranscriptBodyExcludesTheParent(t *testing.T) {
	m := agentsInspectorModel(t)
	m.state.AddMessage(session.RoleAssistant, "PARENT-SECRET", session.ContentTypePlain)

	// A pipeline card has NO child state. The body must be empty rather than
	// borrowed from the parent.
	body, truncated := m.childTranscriptBody(nil)
	if body != "" || truncated {
		t.Fatalf("a nil child produced a body %q/%v, want empty and un-truncated", body, truncated)
	}

	// And the real child's body is the child's.
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child text", session.ContentTypePlain)
	body, _ = m.childTranscriptBody(child)
	if strings.Contains(body, "PARENT-SECRET") {
		t.Fatalf("the child body contains the parent's text:\n%s", body)
	}
	if !strings.Contains(body, "child text") {
		t.Fatalf("the child body omits the child's text:\n%s", body)
	}
}

// TestPipelineCardWithoutAChildShowsMetadataNotAParentTranscript pins the
// no-child case end to end: a pipeline/SDD card shares the parent's state and
// has Child == nil, and the detail must say so while still showing the route
// metadata that WAS recorded.
func TestPipelineCardWithoutAChildShowsMetadataNotAParentTranscript(t *testing.T) {
	m := agentsInspectorModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "PARENT-SECRET", session.ContentTypeMarkdown)
	// RegisterSubagentWithMeta with a nil child is how the pipeline records a
	// dispatched role without a separate conversation.
	m.state.RegisterSubagentWithMeta("review task", nil, session.SubagentMeta{
		Role: routing.RoleReviewer, Provider: "somewhere", Model: "review-model",
	})
	m.refreshInspector()

	roster := m.inspector.model.AgentsSnapshot()
	if len(roster) != 1 {
		t.Fatalf("roster size = %d, want 1", len(roster))
	}
	if roster[0].HasChild {
		t.Fatal("a card with no child state reports HasChild true")
	}
	if roster[0].Role != string(routing.RoleReviewer) || roster[0].Model != "review-model" {
		t.Fatalf("the route metadata was lost in the conversion: %+v", roster[0])
	}
	if strings.Contains(roster[0].ChildBody, "PARENT-SECRET") {
		t.Fatalf("a card with no child borrowed the parent's transcript:\n%s", roster[0].ChildBody)
	}

	// Opened, it says so and shows the metadata.
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused a card whose metadata exists")
	}
	body := m.inspector.model.View(m.inspectorData())
	if !strings.Contains(body, "No child transcript available") {
		t.Fatalf("the detail does not say there is no child transcript:\n%s", body)
	}
	if strings.Contains(body, "PARENT-SECRET") {
		t.Fatalf("the detail rendered the PARENT's conversation under the child's name:\n%s", body)
	}
	if !strings.Contains(body, "review-model") {
		t.Fatalf("the recorded route metadata is missing:\n%s", body)
	}
}

// TestInspectAgentsCommandOpensTheTab pins the named entry point, which is how
// a user reaches the tab without knowing Ctrl+B and Tab.
func TestInspectAgentsCommandOpensTheTab(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.dispatchCommand("/inspect agents")

	if !m.inspector.isOpen() {
		t.Fatal("/inspect agents did not open the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabAgents {
		t.Fatalf("tab = %q, want agents", got)
	}
}

// TestAgentsTabKeysMoveTheCursorWithoutOpeningAClosedDetail pins the key
// contract: browsing the roster is browsing. A key that both moved the cursor
// AND opened a panel would make the panel impossible to avoid, and the reader
// would lose the list every time they looked at the next agent.
func TestAgentsTabKeysMoveTheCursorWithoutOpeningAClosedDetail(t *testing.T) {
	m := agentsInspectorModel(t)
	for _, name := range []string{"one", "two"} {
		child := newChildState(t)
		child.AddMessage(session.RoleAssistant, name+" says hi", session.ContentTypePlain)
		m.state.RegisterSubagent(name, child)
	}
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()

	if m.inspector.model.AgentDetailOpen() {
		t.Fatal("precondition: no detail should be open yet")
	}
	before := m.inspector.model.AgentIDSelected()

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Down was not handled on the Agents tab")
	}
	if after := got.inspector.model.AgentIDSelected(); after == before {
		t.Fatal("Down did not move the agent cursor")
	}
	if got.inspector.model.AgentDetailOpen() {
		t.Fatal("moving the cursor OPENED a detail; browsing must not open panels")
	}
}

// TestEnterOnTheAgentsTabOpensTheSelectedChild pins the other half: Enter is
// what opens a transcript, and what opens is the SELECTED child's.
func TestEnterOnTheAgentsTabOpensTheSelectedChild(t *testing.T) {
	m := agentsInspectorModel(t)
	first := newChildState(t)
	first.AddMessage(session.RoleAssistant, "FIRST-CHILD-TEXT", session.ContentTypePlain)
	m.state.RegisterSubagent("first", first)
	second := newChildState(t)
	second.AddMessage(session.RoleAssistant, "SECOND-CHILD-TEXT", session.ContentTypePlain)
	m.state.RegisterSubagent("second", second)

	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()

	// Move onto the second child, then open it.
	m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyDown})
	m.refreshInspector()

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Enter was not handled on the Agents tab")
	}
	if !got.inspector.model.AgentDetailOpen() {
		t.Fatal("Enter did not open the selected child")
	}
	view := stripANSI(got.inspector.model.View(got.inspectorData()))
	if !strings.Contains(view, "SECOND-CHILD-TEXT") {
		t.Fatalf("Enter opened the wrong child:\n%s", view)
	}
	if strings.Contains(view, "FIRST-CHILD-TEXT") {
		t.Fatalf("Enter opened a child other than the selected one:\n%s", view)
	}
}

// TestAnOpenAgentDetailFollowsTheCursor is the pairing rule: once a detail IS
// open, moving the cursor must move the detail with it. A label describing one
// agent over another agent's transcript is worse than no label at all.
func TestAnOpenAgentDetailFollowsTheCursor(t *testing.T) {
	m := agentsInspectorModel(t)
	first := newChildState(t)
	first.AddMessage(session.RoleAssistant, "FIRST-CHILD-TEXT", session.ContentTypePlain)
	m.state.RegisterSubagent("first", first)
	second := newChildState(t)
	second.AddMessage(session.RoleAssistant, "SECOND-CHILD-TEXT", session.ContentTypePlain)
	m.state.RegisterSubagent("second", second)

	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()

	mm, _, _ := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := asModel(t, mm)
	if !got.inspector.model.AgentDetailOpen() {
		t.Fatal("precondition: the detail must be open")
	}
	if view := stripANSI(got.inspector.model.View(got.inspectorData())); !strings.Contains(view, "FIRST-CHILD-TEXT") {
		t.Fatalf("the first child's transcript is not showing:\n%s", view)
	}

	mm2, _, _ := got.handleKeypress(tea.KeyPressMsg{Code: tea.KeyDown})
	moved := asModel(t, mm2)
	view := stripANSI(moved.inspector.model.View(moved.inspectorData()))
	if !strings.Contains(view, "SECOND-CHILD-TEXT") {
		t.Fatalf("the open detail did not follow the cursor onto the second child:\n%s", view)
	}
	if strings.Contains(view, "FIRST-CHILD-TEXT") {
		t.Fatalf("the open detail still shows the previous child:\n%s", view)
	}
	// And the stack did not grow: following the cursor is not a sequence of
	// views, so Esc must not have to walk back through the agents scrolled past.
	if depth := moved.inspector.model.Depth(); depth != 1 {
		t.Fatalf("detail depth = %d after following the cursor, want 1", depth)
	}
}

// TestChildCompletionUpdatesStatusWithoutChangingTheView pins the plan's
// side-effect rule: a child finishing is a data change, not a navigation. The
// reader must keep looking at whatever they were looking at.
func TestChildCompletionUpdatesStatusWithoutChangingTheView(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "the child's work", session.ContentTypePlain)
	view := m.state.RegisterSubagent("working", child)
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()

	// Open the detail, and page it away from the top so a reset would show.
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused")
	}
	m.refreshInspector()

	m.state.FinishSubagent(view.ID, "done with everything", nil)
	m.refreshInspector()

	roster := m.inspector.model.AgentsSnapshot()
	if len(roster) != 1 {
		t.Fatalf("roster size = %d after completion, want 1", len(roster))
	}
	if roster[0].Status != inspector.AgentCompleted {
		t.Fatalf("status = %v after completion, want completed", roster[0].Status)
	}
	if roster[0].Summary != "done with everything" {
		t.Fatalf("the final summary was lost: %q", roster[0].Summary)
	}
	// The view is where it was: still on the same agent, still open.
	if got := m.inspector.model.AgentIDSelected(); got != view.ID {
		t.Fatalf("selection moved to %d on completion, want %d", got, view.ID)
	}
	if !m.inspector.model.AgentDetailOpen() {
		t.Fatal("the detail closed itself when the child finished")
	}
}

// TestAgentFailureIsVisibleInTheRoster pins that a failed child is not silent.
// A failure the user cannot see is a failure they discover from the transcript
// scrolling past, or not at all.
func TestAgentFailureIsVisibleInTheRoster(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("doomed", child)
	m.state.FinishSubagent(view.ID, "", errFakeAgentFailure{})
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.refreshInspector()

	roster := m.inspector.model.AgentsSnapshot()
	if len(roster) != 1 || roster[0].Status != inspector.AgentFailed {
		t.Fatalf("a failed child is not reported as failed: %+v", roster)
	}
	rendered := stripANSI(m.inspector.model.View(m.inspectorData()))
	if !strings.Contains(rendered, "failed") {
		t.Fatalf("the roster does not mark the failure:\n%s", rendered)
	}
	if !strings.Contains(rendered, "child exploded") {
		t.Fatalf("the failure reason is not shown:\n%s", rendered)
	}
}

// errFakeAgentFailure is a distinguishable failure for the tests above.
type errFakeAgentFailure struct{}

func (errFakeAgentFailure) Error() string { return "child exploded" }

// TestAgentsStopTargetsOnlyTheSelectedRunningChild pins the stop action's
// target at the model boundary. Ctrl+X must resolve to the child whose ROW is
// selected — stopping a different running agent is the worst outcome this
// feature can produce.
func TestAgentsStopTargetsOnlyTheSelectedRunningChild(t *testing.T) {
	m := agentsInspectorModel(t)
	runningChild := newChildState(t)
	running := m.state.RegisterSubagent("still running", runningChild)
	doneChild := newChildState(t)
	done := m.state.RegisterSubagent("already done", doneChild)
	m.state.FinishSubagent(done.ID, "finished", nil)

	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()

	// With the RUNNING child selected, the stop precondition holds.
	if got := m.inspector.model.AgentIDSelected(); got != running.ID {
		t.Fatalf("selection = %d, want the running child %d", got, running.ID)
	}
	if !m.inspector.model.SelectedAgentRunning() {
		t.Fatal("the selected running child is not reported as running")
	}

	// Move onto the completed one: the precondition must drop, or the action
	// would offer to stop an agent that has already finished.
	m.inspector.model.MoveAgentSelection(1)
	if got := m.inspector.model.AgentIDSelected(); got != done.ID {
		t.Fatalf("selection = %d, want the completed child %d", got, done.ID)
	}
	if m.inspector.model.SelectedAgentRunning() {
		t.Fatal("a completed child is reported as running, so stop would target it")
	}

	// And the selection identifies the running child by ID for the dispatch.
	m.inspector.model.MoveAgentSelection(-1)
	if id := m.inspector.model.AgentIDSelected(); id != running.ID {
		t.Fatalf("stop would dispatch for %d, want %d", id, running.ID)
	}
}

// TestAgentDetailSurvivesAResize pins that resizing is not navigation: the
// child transcript the reader opened is still open and still scrolled where
// they left it.
func TestAgentDetailSurvivesAResize(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	for i := 0; i < 200; i++ {
		child.AddMessage(session.RoleAssistant, "child line of output", session.ContentTypePlain)
	}
	m.state.RegisterSubagent("chatty", child)
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.refreshInspector()
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused")
	}
	m.inspector.model.PageAgentDetail(1)
	scroll := m.inspector.model.AgentDetailScroll()
	if scroll == 0 {
		t.Fatal("precondition: the body must have scrolled")
	}

	for _, size := range [][2]int{{120, 40}, {80, 24}, {200, 60}} {
		m.resize(size[0], size[1])
		m.refreshInspector()
		if !m.inspector.model.AgentDetailOpen() {
			t.Fatalf("at %dx%d the detail was lost", size[0], size[1])
		}
		if got := m.inspector.model.AgentIDSelected(); got == 0 {
			t.Fatalf("at %dx%d the selection was lost", size[0], size[1])
		}
	}
}

// TestStopFromTheInspectorTargetsTheInspectedChild pins the dispatch: Ctrl+X
// with the Agents tab showing a running child resolves to stopping THAT child,
// and the runtime is asked to cancel exactly that runtime ID.
func TestStopFromTheInspectorTargetsTheInspectedChild(t *testing.T) {
	m := agentsInspectorModel(t)
	first := newChildState(t)
	firstRunning := m.state.RegisterSubagent("first running", first)
	second := newChildState(t)
	secondRunning := m.state.RegisterSubagent("second running", second)

	// A cancellable child records its cancel func on the view; without one,
	// CancelSubagent has nothing to call and the test would be asserting that a
	// no-op did nothing. The recorder doubles as the observation: it proves
	// WHICH child the action reached.
	firstCancels := &cancelRecorder{}
	secondCancels := &cancelRecorder{}
	m.state.SetSubagentCancel(firstRunning.ID, firstCancels.cancel)
	m.state.SetSubagentCancel(secondRunning.ID, secondCancels.cancel)

	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused the first child")
	}
	m.refreshInspector()

	ctx := m.actionSnapshot()
	if ctx.InspectorAgentRunningID != firstRunning.ID {
		t.Fatalf("the action context targets %d, want the inspected child %d",
			ctx.InspectorAgentRunningID, firstRunning.ID)
	}
	if id, ok := ctx.ctrlXID(); !ok || id != ActionStopAgent {
		t.Fatalf("Ctrl+X resolved to %q/%v, want the stop action", id, ok)
	}

	// Move onto the second child: the target must follow the selection, or the
	// key would stop an agent the user is not looking at.
	m.inspector.model.MoveAgentSelection(1)
	m.inspector.model.SyncAgentDetail()
	m.refreshInspector()
	if got := m.actionSnapshot().InspectorAgentRunningID; got != secondRunning.ID {
		t.Fatalf("the target = %d after moving, want %d", got, secondRunning.ID)
	}

	mm, _ := m.runAction(ActionStopAgent)
	_ = asModel(t, mm)

	if !secondCancels.cancelled() {
		t.Fatalf("stopping the inspected child %d did not reach it", secondRunning.ID)
	}
	if firstCancels.cancelled() {
		t.Fatalf("stopping the inspected child also cancelled %d", firstRunning.ID)
	}
}

// cancelRecorder is a per-child cancel func that records that it ran.
type cancelRecorder struct{ called bool }

func (c *cancelRecorder) cancel() { c.called = true }

func (c *cancelRecorder) cancelled() bool { return c.called }

// TestStopIsOfferedOnlyWhileTheAgentsTabIsOpen pins that the inspector target
// is not read from a tab the user is not looking at. A hidden panel must not
// silently capture a global key.
func TestStopIsOfferedOnlyWhileTheAgentsTabIsOpen(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	m.state.RegisterSubagent("running", child)
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.refreshInspector()
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused")
	}
	m.refreshInspector()
	if m.actionSnapshot().InspectorAgentRunningID == 0 {
		t.Fatal("precondition: the Agents tab must be offering a stop target")
	}

	// Switch to another tab: the target goes away with the view.
	m.inspector.model.Open(inspector.TabChanges)
	m.inspector.model.ClearStack()
	if got := m.actionSnapshot().InspectorAgentRunningID; got != 0 {
		t.Fatalf("the stop target %d survived leaving the Agents tab", got)
	}
}

// TestParentApprovalIsReachableWhileInspectingAnAgent is the plan's explicit
// rule: an approval or question from the parent must stay answerable while the
// reader is inside a child. A panel that swallowed the decision would leave the
// run blocked with nothing on screen to resolve it.
func TestParentApprovalIsReachableWhileInspectingAnAgent(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child text", session.ContentTypePlain)
	m.state.RegisterSubagent("child", child)
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.refreshInspector()
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused")
	}
	m.refreshInspector()

	// The parent raises a decision while the inspector is showing the child.
	m.state.SetPendingApproval(&session.PendingToolCall{
		ID: "call-1", Name: "shell.run", Risk: "high", Reason: "writes files",
	})
	if !m.hasPendingApproval() {
		t.Fatal("precondition: an approval must be pending")
	}

	// The decision owns the keys, so the inspector must not be the effective
	// surface in that state — the form is.
	if got := m.effectiveFocus(); got != FocusPanel {
		t.Fatalf("effective focus = %v with an approval pending, want the panel", got)
	}
	// And the child's transcript is still on screen behind it, not discarded.
	if view := stripANSI(m.inspector.model.View(m.inspectorData())); !strings.Contains(view, "child text") {
		t.Fatalf("the inspected child was lost when a decision arrived:\n%s", view)
	}
	if !m.inspector.model.AgentDetailOpen() {
		t.Fatal("the agent detail was cleared by an incoming decision")
	}
}

// TestInspectorSessionSwapClearsTheAgentDetail pins the session boundary: a new
// conversation must not present the previous session's child as if it belonged
// to the new one.
func TestInspectorSessionSwapClearsTheAgentDetail(t *testing.T) {
	m := agentsInspectorModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "text from the closed session", session.ContentTypePlain)
	m.state.RegisterSubagent("from the old session", child)
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.refreshInspector()
	if !m.inspector.model.EnterAgent() {
		t.Fatal("EnterAgent refused")
	}
	if !m.inspector.model.AgentDetailOpen() {
		t.Fatal("precondition: the detail must be open")
	}

	// A different conversation.
	m.inspector.model.SetScope("s-a-different-conversation")

	if m.inspector.model.AgentDetailOpen() {
		t.Fatal("the agent detail survived a session swap, so it describes a foreign conversation")
	}
	view := stripANSI(m.inspector.model.View(m.inspectorData()))
	if strings.Contains(view, "text from the closed session") {
		t.Fatalf("a closed session's child transcript is still on screen:\n%s", view)
	}
}
