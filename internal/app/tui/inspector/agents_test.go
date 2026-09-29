package inspector

import (
	"strings"
	"testing"
)

// agentsFixture builds a roster with the given IDs, all completed, so a test
// can talk about identity without repeating field noise.
func agentsFixture(ids ...int64) []Agent {
	out := make([]Agent, 0, len(ids))
	for _, id := range ids {
		out = append(out, Agent{
			ID:      id,
			Label:   "task-" + itoa(id),
			Status:  AgentCompleted,
			Role:    "implementer",
			Model:   "some-model",
			Elapsed: "12s",
			Summary: "done",
		})
	}
	return out
}

// itoa avoids pulling strconv into the test file for one call.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- selection identity -------------------------------------------------

// TestAgentsSelectionIsKeyedByIDNotIndex is the rule that makes a status change
// safe to observe: an agent that finishes keeps its row's selection. An
// index-based cursor would silently move to whichever agent landed in the same
// slot, and the reader would be studying the wrong child.
func TestAgentsSelectionIsKeyedByIDNotIndex(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2, 3))

	m.MoveAgentSelection(1) // onto agent 2
	if got := m.AgentIDSelected(); got != 2 {
		t.Fatalf("selected id = %d, want 2", got)
	}

	// The FIRST agent finishes, which is the ordinary case: statuses change
	// under a reader constantly. The roster is replaced with the same order.
	next := agentsFixture(1, 2, 3)
	next[0].Status = AgentRunning
	m.SetAgents(next)

	if got := m.AgentIDSelected(); got != 2 {
		t.Fatalf("selected id = %d after a sibling changed status, want 2", got)
	}
	if m.AgentSelectionVanished() {
		t.Fatal("a status change was reported as the selection vanishing")
	}
}

// TestAgentsSelectionSurvivesAReorder pins that the roster's ORDER is not the
// identity. Registration order is not guaranteed to be stable across a refresh,
// and a cursor that followed the index would move with it.
func TestAgentsSelectionSurvivesAReorder(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2, 3))
	m.MoveAgentSelection(2) // onto agent 3

	reordered := []Agent{
		{ID: 3, Label: "task-3", Status: AgentCompleted},
		{ID: 1, Label: "task-1", Status: AgentCompleted},
		{ID: 2, Label: "task-2", Status: AgentCompleted},
	}
	m.SetAgents(reordered)

	if got := m.AgentIDSelected(); got != 3 {
		t.Fatalf("selected id = %d after a reorder, want 3", got)
	}
	// And the cursor index followed the ID, not the position.
	if _, ok := m.SelectedAgent(); !ok {
		t.Fatal("nothing selected after a reorder")
	}
	sel, _ := m.SelectedAgent()
	if sel.Label != "task-3" {
		t.Fatalf("selected %q, want task-3", sel.Label)
	}
}

// TestAgentsVanishedSelectionClampsAndIsLabelled pins the other half: when the
// selected agent is GONE, the panel must land on a neighbour and say so. Saying
// nothing would present another agent's transcript as the one the reader was
// following.
func TestAgentsVanishedSelectionClampsAndIsLabelled(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2, 3))
	m.MoveAgentSelection(2) // agent 3
	if !m.EnterAgent() {
		t.Fatal("EnterAgent refused a selection that exists")
	}

	// Agent 3 leaves the in-memory roster entirely.
	m.SetAgents(agentsFixture(1, 2))

	if !m.AgentSelectionVanished() {
		t.Fatal("the selection vanished and nothing said so")
	}
	id := m.AgentIDSelected()
	if id == 0 {
		t.Fatal("no neighbour was selected after the selection vanished")
	}
	if id == 3 {
		t.Fatal("the vanished agent is still reported as selected")
	}
	view := m.viewAgents()
	if !strings.Contains(strings.ToLower(view), "no longer") {
		t.Fatalf("the roster does not say the selected agent is gone:\n%s", view)
	}

	// Moving onto a real row clears the label: the reader is now on an agent
	// that exists.
	m.MoveAgentSelection(-1)
	if m.AgentSelectionVanished() {
		t.Fatal("the vanished label survived moving onto a surviving agent")
	}
}

// --- empty roster -------------------------------------------------------

// TestAgentsEmptyRosterSaysSo is the distinction between "no agents have run"
// and "the panel is broken": an empty string reads as the latter.
func TestAgentsEmptyRosterSaysSo(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(nil)

	view := m.viewAgents()
	if strings.TrimSpace(view) == "" {
		t.Fatal("an empty roster rendered nothing, which is indistinguishable from a broken panel")
	}
	lower := strings.ToLower(view)
	if !strings.Contains(lower, "no ") {
		t.Fatalf("the empty state does not say there are no agents:\n%s", view)
	}
	if m.EnterAgent() {
		t.Fatal("EnterAgent reported work with an empty roster, so a key handler would swallow Enter")
	}
	if _, ok := m.SelectedAgent(); ok {
		t.Fatal("an agent is reported as selected with an empty roster")
	}
	if m.SelectedAgentRunning() {
		t.Fatal("an agent is reported as running with an empty roster")
	}
}

// --- distinct states ----------------------------------------------------

// TestAgentsStatesAreDistinguishable pins that running, completed and failed do
// not render the same. A roster where "failed" looks like "done" is one where
// a failure is discovered from somewhere else entirely.
func TestAgentsStatesAreDistinguishable(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{
		{ID: 1, Label: "running-one", Status: AgentRunning, CurrentTool: "file.read"},
		{ID: 2, Label: "done-one", Status: AgentCompleted, Summary: "all good"},
		{ID: 3, Label: "failed-one", Status: AgentFailed, Error: "provider refused the request"},
	})

	view := m.viewAgents()
	for _, want := range []string{"running-one", "done-one", "failed-one"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the roster omits %q:\n%s", want, view)
		}
	}
	// The failure text is the whole point of the failed state; it must be
	// reachable without opening anything.
	if !strings.Contains(view, "provider refused the request") {
		t.Fatalf("the failed agent's error is not shown:\n%s", view)
	}
	// And the running agent's current tool is live information worth showing.
	if !strings.Contains(view, "file.read") {
		t.Fatalf("the running agent's current tool is not shown:\n%s", view)
	}

	// The three rows must not all look alike: at least the status markers
	// differ, which is what a reader scans for.
	lines := strings.Split(stripANSIForTest(view), "\n")
	distinct := map[string]bool{}
	for _, line := range lines {
		for _, name := range []string{"running-one", "done-one", "failed-one"} {
			if strings.Contains(line, name) {
				distinct[statusPartOfLine(line, name)] = true
			}
		}
	}
	if len(distinct) < 3 {
		t.Fatalf("the three statuses render indistinguishable rows (%d distinct shapes):\n%s", len(distinct), view)
	}
}

// statusPartOfLine returns the portion of a row before the agent's label, which
// is where the status marker lives.
func statusPartOfLine(line, label string) string {
	if i := strings.Index(line, label); i >= 0 {
		return line[:i]
	}
	return line
}

// TestAgentsRosterIsNamedAsTheRuntimeRoster pins the distinction the plan calls
// out: /agents is the CONFIGURATION roster (what each role routes to), and this
// tab is the RUNTIME roster (what actually ran). Two surfaces with one name is
// how a user looks in the wrong one.
func TestAgentsRosterIsNamedAsTheRuntimeRoster(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1))

	view := strings.ToLower(m.viewAgents())
	if !strings.Contains(view, "runtime") {
		t.Fatalf("the roster does not identify itself as the runtime roster:\n%s", m.viewAgents())
	}
}

// --- detail: the child transcript ---------------------------------------

// TestAgentsDetailShowsTheChildConversation pins the happy path: an agent with
// a child body opens that body.
func TestAgentsDetailShowsTheChildConversation(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{{
		ID: 1, Label: "child-one", Status: AgentCompleted, HasChild: true,
		ChildBody: "child says: I read three files\n",
	}})
	m.AgentDetailTop()

	if !m.EnterAgent() {
		t.Fatal("EnterAgent refused an agent that has a child")
	}
	if !m.AgentDetailOpen() {
		t.Fatal("the detail did not open")
	}
	if !strings.Contains(m.detail.Content(), "I read three files") {
		t.Fatalf("the child body is not in the detail:\n%s", m.detail.Content())
	}
}

// TestAgentsDetailNeverPresentsTheParentAsTheChild is the plan's explicit
// acceptance criterion. When there is no child state, the panel must say so and
// show the pipeline metadata it does have — never a body that is actually the
// parent's, because a reader cannot tell the difference on screen.
func TestAgentsDetailNeverPresentsTheParentAsTheChild(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{{
		ID: 7, Label: "pipeline-card", Status: AgentCompleted,
		Role: "reviewer", Model: "review-model", Provider: "somewhere",
		Elapsed: "1m 4s", Summary: "reviewed the diff",
		HasChild: false,
	}})
	m.AgentDetailTop()

	if !m.EnterAgent() {
		t.Fatal("EnterAgent refused an agent whose metadata exists")
	}
	body := m.detail.Content()
	if !strings.Contains(body, "No child transcript available") {
		t.Fatalf("the detail does not state that no child transcript exists:\n%s", body)
	}
	// But the pipeline metadata it DOES have must still be reachable: "no
	// child" is not "nothing to see".
	for _, want := range []string{"reviewer", "review-model", "somewhere", "reviewed the diff"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the pipeline metadata %q is missing from the detail:\n%s", want, body)
		}
	}
}

// TestAgentsDetailDisclosesATruncatedChildBody pins that a bounded child body
// is labelled as bounded. The detail view already distinguishes the two kinds
// of "more"; this pins that the caller actually sets the flag.
func TestAgentsDetailDisclosesATruncatedChildBody(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{{
		ID: 1, Label: "big-child", Status: AgentCompleted,
		HasChild: true, ChildBody: "a very long conversation\n", ChildTruncated: true,
	}})
	if !m.EnterAgent() {
		t.Fatal("EnterAgent refused the agent")
	}
	if !m.detail.Truncated() {
		t.Fatal("a truncated child body reached the detail without its truncation flag")
	}
	rendered := m.detail.View("")
	if !strings.Contains(strings.ToLower(rendered), "incomplete") {
		t.Fatalf("the truncated child body is not disclosed:\n%s", rendered)
	}
}

// TestEnteringTheSameAgentTwiceDoesNotDeepenTheStack pins that re-entering is
// idempotent. The detail stack is history: a second level for the same view
// means Esc needs two presses to leave something one press entered.
func TestEnteringTheSameAgentTwiceDoesNotDeepenTheStack(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2))
	m.AgentDetailTop()

	if !m.EnterAgent() {
		t.Fatal("first EnterAgent refused")
	}
	first := m.Depth()
	if !m.EnterAgent() {
		t.Fatal("second EnterAgent refused; re-opening a view must work")
	}
	if m.Depth() != first {
		t.Fatalf("depth = %d after re-entering the same agent, want %d", m.Depth(), first)
	}

	// A DIFFERENT agent is a real second level, so the stack is not frozen.
	m.MoveAgentSelection(1)
	m.EnterAgent()
	if m.Depth() <= first {
		t.Fatalf("opening a different agent did not deepen the stack (depth %d)", m.Depth())
	}
}

// TestAgentsDetailScrollsIndependentlyOfTheRoster pins that the two pieces of
// navigation are separate: scrolling the child transcript must not move the
// roster cursor, and moving the cursor must not scroll the transcript.
func TestAgentsDetailScrollsIndependentlyOfTheRoster(t *testing.T) {
	big := strings.Repeat("a line of the child transcript\n", 400)
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{
		{ID: 1, Label: "one", Status: AgentCompleted, HasChild: true, ChildBody: big},
		{ID: 2, Label: "two", Status: AgentCompleted, HasChild: true, ChildBody: big},
	})
	if !m.EnterAgent() {
		t.Fatal("EnterAgent refused the agent")
	}
	m.Resize(80, 10) // measure the body so paging has a height

	if before := m.AgentDetailScroll(); before != 0 {
		t.Fatalf("a freshly opened detail started at line %d, want the top", before)
	}
	m.PageAgentDetail(1)
	if m.AgentDetailScroll() == 0 {
		t.Fatal("paging the child transcript did not move it")
	}
	if got := m.AgentIDSelected(); got != 1 {
		t.Fatalf("paging the transcript moved the roster cursor to %d", got)
	}

	// And the reverse.
	scroll := m.AgentDetailScroll()
	m.MoveAgentSelection(1)
	if got := m.AgentDetailScroll(); got != scroll {
		t.Fatalf("moving the roster cursor scrolled the transcript (%d -> %d)", scroll, got)
	}
	if got := m.AgentIDSelected(); got != 2 {
		t.Fatalf("selected id = %d after moving, want 2", got)
	}

	m.AgentDetailBottom()
	if m.AgentDetailScroll() == 0 {
		t.Fatal("AgentDetailBottom did not move the body")
	}
	m.AgentDetailTop()
	if m.AgentDetailScroll() != 0 {
		t.Fatalf("AgentDetailTop left the body at line %d", m.AgentDetailScroll())
	}
}

// --- the stop action's precondition -------------------------------------

// TestSelectedAgentRunningIsTrueOnlyForTheSelectedRunningChild pins the
// precondition the stop action reads. Binding stop to "any agent is running"
// is how a user stops an agent they were not looking at.
func TestSelectedAgentRunningIsTrueOnlyForTheSelectedRunningChild(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{
		{ID: 1, Label: "still-working", Status: AgentRunning, HasChild: true},
		{ID: 2, Label: "finished", Status: AgentCompleted, HasChild: true},
	})

	// The cursor starts on the running one.
	if !m.SelectedAgentRunning() {
		t.Fatal("the selected running agent is not reported as running")
	}
	m.MoveAgentSelection(1) // onto the completed one
	if m.SelectedAgentRunning() {
		t.Fatal("a completed agent is reported as running, so stop would target it")
	}

	// And with nothing selected at all.
	m.SetAgents(nil)
	if m.SelectedAgentRunning() {
		t.Fatal("the stop action is enabled with an empty roster")
	}
}

// TestAgentsStopIsOfferedForARunningChildWithoutAChildState pins the pipeline
// case: a card with Child == nil can still be a live, cancellable agent. The
// stop precondition must not depend on a transcript existing.
func TestAgentsStopIsOfferedForARunningChildWithoutAChildState(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents([]Agent{{ID: 9, Label: "live-card", Status: AgentRunning, HasChild: false}})

	if !m.SelectedAgentRunning() {
		t.Fatal("a running agent with no child state is not reported as running")
	}
	if m.AgentIDSelected() != 9 {
		t.Fatalf("selected id = %d, want 9", m.AgentIDSelected())
	}
}

// --- copying the input --------------------------------------------------

// TestAgentsRosterIsCopiedNotAliased pins the ownership rule: the inspector
// holds a COPY of what it was handed. Holding the caller's slice would let a
// later mutation change what is on screen without a refresh, which is the kind
// of bug that only shows up under concurrency.
func TestAgentsRosterIsCopiedNotAliased(t *testing.T) {
	in := agentsFixture(1, 2)
	m := New()
	m.Resize(80, 20)
	m.SetAgents(in)

	in[0].Label = "MUTATED"
	if got := m.AgentsSnapshot(); got[0].Label == "MUTATED" {
		t.Fatal("the roster aliases the caller's slice")
	}

	// And the snapshot handed out is the caller's to keep.
	out := m.AgentsSnapshot()
	out[0].Label = "ALSO MUTATED"
	if m.AgentsSnapshot()[0].Label == "ALSO MUTATED" {
		t.Fatal("AgentsSnapshot handed out the panel's own slice")
	}
}

// --- resize -------------------------------------------------------------

// TestAgentsResizeLosesNoState pins that resizing is not navigation: the
// selection, the vanished label and the detail survive it.
func TestAgentsResizeLosesNoState(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2, 3))
	m.MoveAgentSelection(1)
	m.EnterAgent()

	for _, size := range [][2]int{{200, 60}, {80, 24}, {120, 40}, {80, 20}} {
		m.Resize(size[0], size[1])
		if got := m.AgentIDSelected(); got != 2 {
			t.Fatalf("at %dx%d the selection became %d, want 2", size[0], size[1], got)
		}
		if !m.AgentDetailOpen() {
			t.Fatalf("at %dx%d the detail was lost", size[0], size[1])
		}
	}
}

// --- bounds -------------------------------------------------------------

// TestAgentsRowsNeverExceedThePanelWidth pins that an author-supplied label or
// summary cannot produce a line wider than the panel it is rendered into. A row
// that overflows wraps and pushes the panel's own chrome off the bottom.
func TestAgentsRowsNeverExceedThePanelWidth(t *testing.T) {
	long := strings.Repeat("very-long-label-", 40)
	m := New()
	m.Resize(60, 20)
	m.SetAgents([]Agent{{
		ID: 1, Label: long, Status: AgentRunning, CurrentTool: long,
		Role: string(long), Model: long, Provider: long,
	}})

	view := stripANSIForTest(m.viewAgents())
	for i, line := range strings.Split(view, "\n") {
		if w := lineWidth(line); w > 60 {
			t.Fatalf("line %d is %d cells wide, want <= 60:\n%s", i, w, line)
		}
	}
}

// lineWidth counts the visible cells of a line, ignoring nothing: the test
// strips ANSI first, so what is left is what the terminal draws.
func lineWidth(s string) int {
	return len([]rune(s))
}

// TestAgentsRosterHasACursorMarker pins that the reader can see which row is
// selected. A roster with a hidden cursor makes Enter unpredictable.
func TestAgentsRosterHasACursorMarker(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2))

	view := stripANSIForTest(m.viewAgents())
	if !strings.Contains(view, "▸") {
		t.Fatalf("the roster marks no row as selected:\n%s", view)
	}
	// Exactly one row carries the marker.
	if n := strings.Count(view, "▸"); n != 1 {
		t.Fatalf("%d rows carry the cursor marker, want exactly 1:\n%s", n, view)
	}
}

// TestAgentsSelectionStartsOnTheFirstRow pins the default: with nothing
// selected yet, Enter must do something predictable rather than nothing.
func TestAgentsSelectionStartsOnTheFirstRow(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1, 2, 3))

	if got := m.AgentIDSelected(); got != 1 {
		t.Fatalf("selected id = %d on a fresh roster, want the first row", got)
	}
	// Running off either end clamps rather than wrapping or clearing.
	m.MoveAgentSelection(-10)
	if got := m.AgentIDSelected(); got != 1 {
		t.Fatalf("moving far up selected %d, want a clamp to 1", got)
	}
	m.MoveAgentSelection(10)
	if got := m.AgentIDSelected(); got != 3 {
		t.Fatalf("moving far down selected %d, want a clamp to 3", got)
	}
}

// TestEnterAgentFallsThroughWhenNothingIsSelected pins the false return that
// lets a key handler try a different Enter meaning. Swallowing Enter with
// nothing to open is how a key stops working for no visible reason.
func TestEnterAgentFallsThroughWhenNothingIsSelected(t *testing.T) {
	m := New()
	m.Resize(80, 20)

	if m.EnterAgent() {
		t.Fatal("EnterAgent claimed to open something with an empty roster")
	}
	if m.AgentDetailOpen() {
		t.Fatal("a detail opened with an empty roster")
	}
}

// TestSetScopeResetsTheAgentDetail pins the session boundary: the detail stack
// belongs to a conversation, and a new conversation must not inherit a panel
// showing a child that no longer exists.
func TestSetScopeResetsTheAgentDetail(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.SetAgents(agentsFixture(1))
	m.EnterAgent()

	m.SetScope("a-new-conversation")

	// Whatever the reset does, it must not leave the panel claiming to show a
	// child from the previous conversation with no way to tell.
	if m.AgentDetailOpen() {
		t.Fatal("the agent detail survived a session change, so it describes a foreign conversation")
	}
}
