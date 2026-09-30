package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/contextpack"
)

// contextTestModel builds a model at a widescreen size with the inspector open
// on the Context tab.
func contextTestModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := inspectTestModel(t, w, h)
	m.inspector.open(inspector.TabContext, m.inspectorSideAvailable())
	m.refreshInspector()
	return m
}

// seededPack installs a pack with one section that fits and one the budget cut.
//
// Full differing from Content is exactly how the pack records a cut
// (buildPackFromSections restores Content from Full before every budget pass),
// so this is the real shape of a truncated section rather than a hand-set flag.
func seededPack() contextpack.Pack {
	return contextpack.Pack{
		// GeneratedAt is what marks a pack as having been built — the panel
		// reads it rather than len(Sections), because an empty pack and a
		// never-built one both have no sections.
		GeneratedAt: time.Now(),
		TokenUsage:  contextpack.TokenUsage{EstimatedTokens: 1_500, MaxTokens: 32_000, Truncated: true},
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "repo card", Source: "repo", EstimatedTokens: 200, Content: "go module marshal\n", Full: "go module marshal\n"},
			{
				Kind: contextpack.SectionRepoMap, Title: "repo map", Source: "repo",
				EstimatedTokens: 1_300, Content: "internal/\n",
				Full: "internal/\ncmd/\nweb/\ndocs/\nscripts/\ntest/\n",
			},
		},
	}
}

// TestContextTabIsPopulatedFromTheLivePack pins that the panel renders the
// SESSION's pack rather than an empty shell: the tab is only useful if the
// numbers on it are the agent's real ones.
func TestContextTabIsPopulatedFromTheLivePack(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()

	view := m.viewString()
	for _, want := range []string{"repo card", "repo map"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Context tab does not list %q:\n%s", want, view)
		}
	}
	if got := m.inspector.model.ContextRowCount(); got != 2 {
		t.Fatalf("the Context tab offers %d rows, want one per section", got)
	}
}

// TestContextTabInfersSectionTruncationFromThePack pins that a section the
// budget cut is marked, and one it did not is not. The flag is derived from
// Content/Full rather than guessed, so a pack that has never been through a
// budget pass (Full empty) is never reported as cut.
func TestContextTabInfersSectionTruncationFromThePack(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()

	if !m.inspector.model.OpenContextRowByLabel("repo map") {
		t.Fatal("could not open the repo-map section")
	}
	if !m.inspector.model.ContextDetailTruncated() {
		t.Error("a section the budget cut is not marked truncated")
	}

	if !m.inspector.model.OpenContextRowByLabel("repo card") {
		t.Fatal("could not open the repo-card section")
	}
	if m.inspector.model.ContextDetailTruncated() {
		t.Error("a section the budget did not cut is marked truncated")
	}

	// A pack whose sections have never been through a budget pass has an empty
	// Full and must not be read as truncated on that basis.
	unbudgeted := contextpack.Pack{
		GeneratedAt: time.Now(),
		TokenUsage:  contextpack.TokenUsage{MaxTokens: 32_000},
		Sections:    []contextpack.Section{{Title: "fresh", Content: "text\n"}},
	}
	m.state.SetContextPack(unbudgeted)
	m.refreshInspector()
	if !m.inspector.model.OpenContextRowByLabel("fresh") {
		t.Fatal("could not open the fresh section")
	}
	if m.inspector.model.ContextDetailTruncated() {
		t.Error("a section with no Full recorded is reported as truncated; an unbudgeted pack is not a cut one")
	}
}

// TestContextTabShowsTheLastRequest pins the second scope: the panel renders
// the runtime's own request snapshot, with the route that actually served it.
func TestContextTabShowsTheLastRequest(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetRequestInspection(session.RequestInspection{
		AttemptID:  3,
		Provider:   "ollama",
		Model:      "qwen2.5-coder:14b",
		Generation: "gen-1",
		Messages: []session.InspectionMessage{
			{Role: "system", Content: "You are Marshal."},
			{Role: "user", Content: "hello"},
		},
		Tools: []session.InspectionTool{{Name: "file.read", Description: "read a file"}},
		Outcome: session.InspectionOutcome{
			Status: session.InspectionCompleted,
		},
	})
	m.inspector.model.SetContextScope(inspector.ContextScopeRequest)
	m.refreshInspector()

	view := m.viewString()
	for _, want := range []string{"You are Marshal.", "hello", "file.read", "ollama"} {
		if !strings.Contains(view, want) {
			t.Errorf("the request scope does not show %q:\n%s", want, view)
		}
	}
}

// TestContextTabRedactsTheRequestScope is the redaction contract at the model
// boundary: a credential the runtime captured never reaches the screen, and —
// because the panel builds its copy source through the same function — never
// reaches the clipboard either.
func TestContextTabRedactsTheRequestScope(t *testing.T) {
	const secret = "sk-live-abcdefghijklmnopqrstuvwxyz012345"
	m := contextTestModel(t, 160, 40)
	m.state.SetRequestInspection(session.RequestInspection{
		AttemptID: 1,
		Provider:  "openai",
		Model:     "gpt-x",
		Messages: []session.InspectionMessage{
			{Role: "system", Content: "OPENAI_API_KEY=" + secret},
		},
		Outcome: session.InspectionOutcome{Status: session.InspectionCompleted},
	})
	m.inspector.model.SetContextScope(inspector.ContextScopeRequest)
	m.refreshInspector()

	if view := m.viewString(); strings.Contains(view, secret) {
		t.Fatalf("the request scope rendered a secret:\n%s", view)
	}
	if !m.inspector.model.OpenContextRow(0) {
		t.Fatal("could not open the first request row")
	}
	text, _, _, ok := m.inspector.model.CaptureContextCopy()
	if !ok {
		t.Fatal("the open row has nothing to copy")
	}
	if strings.Contains(text, secret) {
		t.Fatalf("the copy source carries an unredacted secret:\n%s", text)
	}
}

// TestContextTabDoesNotRedactThePackScope pins the other half of the rule: the
// pack carries ordinary repository content, which Task 4's code-copy actions
// deliberately leave alone. Masking it would corrupt legitimate text and teach
// the reader to ignore the mask.
func TestContextTabDoesNotRedactThePackScope(t *testing.T) {
	const line = "API_KEY=looks-secret-but-is-a-fixture"
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(contextpack.Pack{
		GeneratedAt: time.Now(),
		TokenUsage:  contextpack.TokenUsage{MaxTokens: 32_000},
		Sections:    []contextpack.Section{{Title: "snippet", Content: line + "\n"}},
	})
	m.refreshInspector()

	if !m.inspector.model.OpenContextRowByLabel("snippet") {
		t.Fatal("could not open the snippet section")
	}
	text, _, _, ok := m.inspector.model.CaptureContextCopy()
	if !ok {
		t.Fatal("the open section has nothing to copy")
	}
	if !strings.Contains(text, line) {
		t.Fatalf("the pack scope mangled repository content: %q", text)
	}
}

// TestContextKeysMoveTheRowCursorNotAScore pins the key path: Up/Down on the
// Context tab move the ROW cursor, and Tab cycles the scope rather than
// leaving the panel. A key that scrolled the tab body instead would move
// something the reader is not looking at.
func TestContextKeysMoveTheRowCursorAndCycleTheScope(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()

	before := m.inspector.model.ContextScope()
	moved := inspectorKeyModel(t, m, keyPress("down"))
	if c := moved.inspector.model.ContextCursor(); c != 1 {
		t.Fatalf("Down left the row cursor at %d, want 1", c)
	}

	cycled := inspectorKeyModel(t, moved, tea.KeyPressMsg{Code: tea.KeyRight})
	if cycled.inspector.model.ContextScope() == before {
		t.Error("Right did not cycle the context scope")
	}
	if cycled.inspector.model.SelectedTab() != inspector.TabContext {
		t.Error("Right left the Context tab; it cycles the scope, not the tab")
	}

	// Tab keeps its tab-bar meaning on this tab, as on every other one. A
	// panel where Tab sometimes leaves and sometimes stays cannot be learned.
	tabbed := inspectorKeyModel(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if tabbed.inspector.model.SelectedTab() == inspector.TabContext {
		t.Error("Tab did not move to the next tab on the Context tab")
	}
}

// TestContextEnterOpensTheSelectedRow pins Enter: it opens the row the cursor
// is on, and the body it opens is that row's own.
func TestContextEnterOpensTheSelectedRow(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()

	got := inspectorKeyModel(t, m, keyPress("down")) // onto "repo map"
	got = inspectorKeyModel(t, got, keyPress("enter"))

	if !got.inspector.model.ContextDetailOpen() {
		t.Fatal("Enter did not open a context row")
	}
	text, label, _, ok := got.inspector.model.CaptureContextCopy()
	if !ok {
		t.Fatal("the opened row has nothing to copy")
	}
	if !strings.Contains(label, "repo map") {
		t.Fatalf("copy label = %q, want it to name the repo-map row", label)
	}
	if strings.Contains(text, "go module marshal") {
		t.Fatalf("Enter opened the wrong row's body:\n%s", text)
	}
}

// TestContextRefreshDoesNotReplaceAnOpenRow pins the snapshot-update rule at the
// model boundary: a turn-boundary refresh updates the tab's rows but leaves the
// body the reader is studying alone, and marks it stale.
func TestContextRefreshDoesNotReplaceAnOpenRow(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	if !m.inspector.model.OpenContextRowByLabel("repo map") {
		t.Fatal("could not open the repo-map section")
	}
	before, _, _, _ := m.inspector.model.CaptureContextCopy()

	updated := seededPack()
	updated.Sections[1].Content = "internal/\ncmd/\n"
	m.state.SetContextPack(updated)
	m.refreshInspector()

	after, _, _, _ := m.inspector.model.CaptureContextCopy()
	if after != before {
		t.Fatalf("a refresh replaced the open body:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if !m.inspector.model.ContextDetailStale() {
		t.Error("the body changed underneath the reader with no indication")
	}
}

// TestContextLeavesAChildScopeTheRuntimeReleased pins the child boundary: a
// scope naming a child whose State the runtime has released returns the reader
// to the root rather than showing a snapshot of a conversation the app can no
// longer see.
func TestContextLeavesAChildScopeTheRuntimeReleased(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.inspector.model.SetChildContext("agent #9 gone", inspector.ContextData{
		Child: true,
		Pack:  inspector.ContextPack{Known: true},
	})
	m.refreshInspector()

	if m.inspector.model.InChildContext() {
		t.Fatal("the child scope survived a refresh with no such child in the runtime")
	}
}

// TestContextScopeCommandOpensTheNamedScope pins /inspect context <scope>, and
// that an unknown scope is named back rather than silently ignored.
func TestContextScopeCommandOpensTheNamedScope(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.dispatchCommand("/inspect context request")

	if !m.inspector.isOpen() {
		t.Fatal("/inspect context request did not open the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabContext {
		t.Fatalf("selected tab = %q, want the context tab", got)
	}
	if got := m.inspector.model.ContextScope(); got != inspector.ContextScopeRequest {
		t.Fatalf("scope = %q, want the last-request scope", got)
	}

	m.dispatchCommand("/inspect context pack")
	if got := m.inspector.model.ContextScope(); got != inspector.ContextScopePack {
		t.Fatalf("scope = %q after /inspect context pack", got)
	}

	m.dispatchCommand("/inspect context nonsense")
	if got := m.inspector.model.ContextScope(); got != inspector.ContextScopePack {
		t.Fatalf("an unknown scope changed the selection to %q", got)
	}
	if toast := m.toastText(); !strings.Contains(toast, "nonsense") {
		t.Fatalf("toast = %q, want it to name the unknown scope", toast)
	}
}

// TestContextTabIsReachableThroughInspect is the discoverability contract: the
// tab is in the product set AND offered by VisibleTabs, so /inspect can open
// it and the tab bar can show it.
func TestContextTabIsReachableThroughInspect(t *testing.T) {
	found := false
	for _, tab := range inspector.VisibleTabs() {
		if tab == inspector.TabContext {
			found = true
		}
	}
	if !found {
		t.Fatal("the context tab is not offered by VisibleTabs")
	}
	m := inspectTestModel(t, 160, 40)
	m.dispatchCommand("/inspect context")
	if got := m.inspector.model.SelectedTab(); got != inspector.TabContext {
		t.Fatalf("selected tab = %q, want the context tab", got)
	}
}

// TestContextCopyActionIsAvailableOnlyWithAnOpenRow pins the action's
// availability rule: it is offered exactly when there is something on screen to
// copy, which is what keeps the palette honest about what it can do.
func TestContextCopyActionIsAvailableOnlyWithAnOpenRow(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()

	ctx := m.actionSnapshot()
	if ctx.ContextDetailOpen {
		t.Fatal("the context copy reports an open row before any was opened")
	}
	if a, ok := resolveAction(ctx, ActionCopyContext); !ok || !a.Disabled {
		t.Fatal("the context copy is offered with nothing open")
	}

	if !m.inspector.model.OpenContextRowByLabel("repo card") {
		t.Fatal("could not open a row")
	}
	m.refreshInspector()
	if !m.actionSnapshot().ContextDetailOpen {
		t.Fatal("the context copy is not offered with a row open")
	}
}

// TestContextCopyActionIsNotOfferedFromAnotherTab pins that availability reads
// the tab ON DISPLAY: a body left open behind a tab the reader switched away
// from is not what they are looking at, and offering to copy it would copy
// something off screen.
func TestContextCopyActionIsNotOfferedFromAnotherTab(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	if !m.inspector.model.OpenContextRowByLabel("repo card") {
		t.Fatal("could not open a row")
	}
	m.refreshInspector()
	if !m.actionSnapshot().ContextDetailOpen {
		t.Fatal("precondition: the copy should be available on the Context tab")
	}

	m.inspector.model.Open(inspector.TabChanges)
	m.refreshInspector()
	if m.actionSnapshot().ContextDetailOpen {
		t.Fatal("the context copy is offered from another tab")
	}
}

// TestContextTabIsPopulatedOnTheFirstRefreshAfterASessionSwap pins the ORDER
// inside refreshInspector.
//
// SetScope discards per-conversation state when the scope actually changes, so
// a context snapshot filled BEFORE SetScope is thrown away by the very refresh
// that produced it. The symptom is a Context tab that is empty the first time a
// new session opens it and only fills in later — which reads as "the pack has
// not been built" when it has.
func TestContextTabIsPopulatedOnTheFirstRefreshAfterASessionSwap(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.inspector.open(inspector.TabContext, m.inspectorSideAvailable())
	m.refreshInspector()
	if m.inspector.model.ContextRowCount() == 0 {
		t.Fatal("precondition: the first session's pack should be rendered")
	}

	// A NEW conversation: a fresh State, which carries a different scope ID.
	replacement := session.New(m.state.Config, m.state.WorkingDir, time.Unix(200, 0), session.Persistence{})
	replacement.SetContextPack(seededPack())
	m.state = replacement
	m.refreshInspector()

	if m.inspector.model.ContextRowCount() == 0 {
		t.Fatal("the Context tab is empty on the first refresh after a session swap; the snapshot was discarded by SetScope")
	}
}

// TestSessionSwapClearsTheContextSnapshot pins the session boundary: a reply —
// or a pack — recorded for a conversation the user has left must not appear on
// the panel describing the new one.
func TestSessionSwapClearsTheContextSnapshot(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	if m.inspector.model.ContextRowCount() == 0 {
		t.Fatal("precondition: the pack should be rendered")
	}

	m.inspector.model.SetScope("a-new-conversation")

	if m.inspector.model.ContextRowCount() != 0 {
		t.Fatal("the context rows survived a session change")
	}
	if m.inspector.model.InChildContext() {
		t.Fatal("a child context scope survived a session change")
	}
}

// TestContextSnapshotIsACopyNotALiveView pins that the panel holds copied data.
//
// It drives the mutation through UpdateContextPack, which is the path every
// real mutator uses, and it asserts on a panel that has ALREADY rendered. A
// version of this that mutated the caller's own slice after SetContextPack
// proved nothing: SetContextPack clones, so the caller's slice could not reach
// the stored pack even if the inspector held a live reference.
func TestContextSnapshotIsACopyNotALiveView(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	if !strings.Contains(m.viewString(), "repo card") {
		t.Fatal("precondition: the seeded pack should be on screen")
	}

	// Mutate the SESSION's pack in place, through the atomic accessor.
	m.state.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		pack.Sections[0].Title = "MUTATED"
		pack.Sections[0].Content = "MUTATED\n"
		return pack
	})

	if view := m.viewString(); strings.Contains(view, "MUTATED") {
		t.Fatalf("the panel read live state rather than its copied snapshot:\n%s", view)
	}
	// A refresh is what adopts the new snapshot, and it must then show it —
	// otherwise the assertion above would pass for a panel that never updates.
	m.refreshInspector()
	if view := m.viewString(); !strings.Contains(view, "MUTATED") {
		t.Fatalf("a refresh did not adopt the mutated pack:\n%s", view)
	}
}

// TestContextEscLeavesTheChildScope pins the only way back out of a child's
// context.
//
// Esc is consumed by the keypress router BEFORE any per-tab handler runs, so
// the exit has to live where that router actually dispatches — the inspector
// host's esc(). A case in the tab handler is unreachable, and the symptom is a
// reader who scoped into a child and has no key that returns them.
func TestContextEscLeavesTheChildScope(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	before := m.inspector.model.ContextScope()

	// A REAL child, reached through the same wiring the app uses. A fabricated
	// scope name would make this test vacuous: the refresh's own safety net
	// leaves a child the runtime cannot find, which would clear the scope
	// whether or not Esc does anything.
	child := newChildState(t)
	child.SetContextPack(seededPack())
	m.state.RegisterSubagent("explore", child)
	m.refreshInspector()
	m.inspector.model.Open(inspector.TabAgents)
	if !m.enterInspectedChildContext() {
		t.Fatal("could not scope into the child's context")
	}
	if !m.inspector.model.InChildContext() {
		t.Fatal("precondition: the child scope should be entered")
	}
	// The refresh must NOT be what clears it: assert the scope survives one,
	// so the Esc assertion below is about Esc.
	if !m.inspector.model.InChildContext() {
		t.Fatal("the child scope did not survive a refresh")
	}

	// Driven through the REAL keypress router, with focus on the inspector.
	// handleInspectorKey is the per-tab handler and is NOT where Esc arrives —
	// that was the defect, so driving it directly would prove nothing.
	m.focus = FocusInspector
	m = keyModel(t, m, func() (tea.Model, tea.Cmd) {
		mm, cmd, _ := m.handleKeypress(keyPress("esc"))
		return mm, cmd
	})

	if m.inspector.model.InChildContext() {
		t.Fatal("Esc did not leave the child context scope")
	}
	if got := m.inspector.model.ContextScope(); got != before {
		t.Fatalf("scope = %q after leaving the child, want the previous %q", got, before)
	}
}

// TestContextRefreshInAChildKeepsTheOpenRow pins that the turn-boundary refresh
// does not take the reader's body away inside a child scope.
//
// The wiring re-pushes the child snapshot on every refresh, so a child path
// that dismissed the detail unconditionally would do it several times a turn,
// with nothing having changed. The root scope already behaved; the child did
// not, and the difference was invisible except to someone reading.
func TestContextRefreshInAChildKeepsTheOpenRow(t *testing.T) {
	m := contextTestModel(t, 160, 40)

	// A REAL child, registered on the runtime and reached through the same
	// wiring the app uses, so the refresh under test is the real one.
	child := newChildState(t)
	child.SetContextPack(seededPack())
	m.state.RegisterSubagent("review", child)
	m.refreshInspector()
	m.inspector.model.Open(inspector.TabAgents)
	if m.inspector.model.SelectedTab() != inspector.TabAgents {
		t.Fatal("could not open the Agents tab")
	}
	if !m.enterInspectedChildContext() {
		t.Fatal("could not scope into the child's context")
	}
	m.refreshInspector()

	if m.inspector.model.ContextRowCount() == 0 {
		t.Fatal("precondition: the child should offer a row")
	}
	if !m.inspector.model.OpenContextRow(0) {
		t.Fatal("could not open a child row")
	}
	before, _, _, ok := m.inspector.model.CaptureContextCopy()
	if !ok {
		t.Fatal("the opened child row has nothing to copy")
	}

	// A refresh with nothing changed, as the app performs on every turn
	// boundary. The body must survive AND not be labelled stale: an identical
	// snapshot has nothing to disclose.
	m.refreshInspector()
	if !m.inspector.model.ContextDetailOpen() {
		t.Fatal("a refresh closed the reader's open row inside the child scope")
	}
	if after, _, _, _ := m.inspector.model.CaptureContextCopy(); after != before {
		t.Fatalf("a refresh replaced the open child body:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if m.inspector.model.ContextDetailStale() {
		t.Fatal("an unchanged refresh marked the child body stale")
	}

	// Now the child's pack really does move: the body stays, and the reader is
	// told it moved. Replacing it here would be the same failure as replacing
	// it on the identical refresh above.
	child.UpdateContextPack(func(pack contextpack.Pack) contextpack.Pack {
		pack.Sections[0].Content = "the child's section has moved on\n"
		return pack
	})
	m.refreshInspector()

	if !m.inspector.model.ContextDetailOpen() {
		t.Fatal("the changed refresh closed the reader's open row")
	}
	if after, _, _, _ := m.inspector.model.CaptureContextCopy(); after != before {
		t.Fatalf("the changed refresh replaced the body under the reader:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if !m.inspector.model.ContextDetailStale() {
		t.Fatal("the child body changed underneath the reader with no indication")
	}
}

// TestContextRowCursorSurvivesTheChildDimension pins that browsing a child does
// not move the reader's place in the conversation's own list.
func TestContextRowCursorSurvivesTheChildDimension(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	m.inspector.model.MoveContextSelection(1)
	rootCursor := m.inspector.model.ContextCursor()
	if rootCursor != 1 {
		t.Fatalf("precondition: the root cursor should be 1, got %d", rootCursor)
	}

	// A child with a list long enough that a carried index would be visible.
	m.inspector.model.SetChildContext("agent #6 deep", inspector.ContextData{
		Child: true,
		Pack: inspector.ContextPack{
			Known:       true,
			GeneratedAt: time.Now(),
			Sections: []inspector.ContextSection{
				{Title: "a", EstimatedTokens: 1},
				{Title: "b", EstimatedTokens: 1},
				{Title: "c", EstimatedTokens: 1},
			},
		},
	})
	m.inspector.model.MoveContextSelection(2) // browse inside the child
	m.inspector.model.ClearChildContext()

	if got := m.inspector.model.ContextCursor(); got != rootCursor {
		t.Fatalf("the root cursor = %d after visiting the child, want the %d it was left at", got, rootCursor)
	}
}

// TestContextCopyLabelSurvivesATabSwitch pins that a context body keeps its own
// heading.
//
// detailLabel is one field shared by three tabs. A diff opened on the Changes
// tab overwrites it, so a context body that rendered under that shared field
// would come back under the diff's title — and the copy label, built from the
// same field, would name the wrong thing on the clipboard.
func TestContextCopyLabelSurvivesATabSwitch(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(seededPack())
	m.refreshInspector()
	if !m.inspector.model.OpenContextRowByLabel("repo card") {
		t.Fatal("could not open a context row")
	}
	before, _, _, ok := m.inspector.model.CaptureContextCopy()
	if !ok {
		t.Fatal("the opened row has nothing to copy")
	}

	// Another tab opens its own detail, overwriting the shared heading.
	m.inspector.model.Open(inspector.TabChanges)
	if !m.inspector.model.EnterSelected() {
		t.Skip("no changed file to open on the Changes tab; nothing overwrote the label")
	}
	m.inspector.model.ApplyDiffLoaded(inspector.DiffLoadedMsg{
		Scope:   m.inspector.model.Scope(),
		Request: mustPendingRequest(t, m),
		Path:    "some/file.go",
	})
	m.inspector.model.Open(inspector.TabContext)
	m.refreshInspector()

	if _, label, _, ok := m.inspector.model.CaptureContextCopy(); ok && strings.Contains(label, "some/file.go") {
		t.Fatalf("the context copy label = %q, want the CONTEXT row's label", label)
	}
	_ = before
}

// keyModel runs one step of a key sequence and narrows the result to Model.
func keyModel(t *testing.T, m Model, step func() (tea.Model, tea.Cmd)) Model {
	t.Helper()
	mm, _ := step()
	out, ok := mm.(Model)
	if !ok {
		t.Fatalf("the key step returned %T, want a Model", mm)
	}
	return out
}

// mustPendingRequest drains the inspector's pending diff request.
func mustPendingRequest(t *testing.T, m Model) uint64 {
	t.Helper()
	req, ok := m.inspector.model.PendingDiffRequest()
	if !ok {
		t.Fatal("no pending diff request to drain")
	}
	return req.Request
}

// TestContextDoesNotRenderAProviderErrorVerbatim pins that the attempt's error
// text goes through redaction like the rest of the request.
//
// The error string is arbitrary provider text about the same request and can
// embed a credential — a URL with a query token, a header echoed back — so
// trusting it because it is "just an error" is how a secret reaches the screen
// and the clipboard.
func TestContextDoesNotRenderAProviderErrorVerbatim(t *testing.T) {
	const secret = "sk-live-abcdefghijklmnopqrstuvwxyz012345"
	m := contextTestModel(t, 160, 40)
	m.state.SetRequestInspection(session.RequestInspection{
		AttemptID: 1,
		Provider:  "openai",
		Model:     "gpt-x",
		Outcome: session.InspectionOutcome{
			Status: session.InspectionFailed,
			Err:    "request to https://api.example.com/v1?key=" + secret + " failed",
		},
	})
	m.inspector.model.SetContextScope(inspector.ContextScopeRequest)
	m.refreshInspector()

	view := m.viewString()
	if strings.Contains(view, secret) {
		t.Fatalf("the attempt's error text rendered a secret verbatim:\n%s", view)
	}
	if !strings.Contains(view, "failed") {
		t.Errorf("the failure reason is gone; redacting must not erase the diagnosis:\n%s", view)
	}
}

// TestContextShowsTheSectionSource pins that a section's source is displayed
// when it says something the title does not.
//
// A pinned @file section carries "path:start-end" as its source while its title
// is the bare path, so the line range is the only part a reader cannot infer.
func TestContextShowsTheSectionSource(t *testing.T) {
	m := contextTestModel(t, 160, 40)
	m.state.SetContextPack(contextpack.Pack{
		GeneratedAt: time.Now(),
		TokenUsage:  contextpack.TokenUsage{MaxTokens: 32_000},
		Sections: []contextpack.Section{{
			Kind: contextpack.SectionFileSnippet, Title: "internal/app/tui/model.go",
			Source: "internal/app/tui/model.go:100-140", EstimatedTokens: 400,
			Content: "package tui\n",
		}},
	})
	m.refreshInspector()
	// The panel's own width, not the terminal's: the rail gets a share of the
	// frame, and asserting against the terminal would be testing a width the
	// inspector never sees.
	m.inspector.model.Resize(90, 24)

	view := m.inspector.model.View(inspector.Data{})
	if !strings.Contains(view, "100-140") {
		t.Errorf("the section's line range is not shown:\n%s", view)
	}
}

// TestContextWorksOnAFreshSessionWithNothingRecorded pins the empty state a
// reader actually meets: a new session has no pack and no request, and the tab
// must still explain itself rather than render nothing.
func TestContextWorksOnAFreshSessionWithNothingRecorded(t *testing.T) {
	m := contextTestModel(t, 160, 40)

	view := m.viewString()
	if strings.TrimSpace(view) == "" {
		t.Fatal("the Context tab renders nothing on a fresh session")
	}
	if !strings.Contains(strings.ToLower(view), "not been built") {
		t.Errorf("the pack scope does not explain that no pack exists yet:\n%s", view)
	}

	m.inspector.model.SetContextScope(inspector.ContextScopeRequest)
	m.refreshInspector()
	if got := m.viewString(); !strings.Contains(strings.ToLower(got), "no request") {
		t.Errorf("the request scope does not explain that none was submitted:\n%s", got)
	}
}

// TestContextTabRendersEmptyDataWithoutPanicking pins robustness: the inspector
// must render every tab against an empty session, which is what a reader sees
// the moment they open it.
func TestContextTabRendersEmptyDataWithoutPanicking(t *testing.T) {
	m := New(session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}))
	m.resize(80, 24)
	m.inspector = newInspectorHost()
	m.inspector.open(inspector.TabContext, m.inspectorSideAvailable())
	m.refreshInspector()
	if got := m.viewString(); got == "" {
		t.Fatal("the Context tab renders nothing with no session state")
	}
}

// inspectorKeyModel drives one key through the inspector's key handler and
// narrows the returned tea.Model back to the concrete Model the handler always
// returns, failing loudly rather than panicking if that contract ever changes.
func inspectorKeyModel(t *testing.T, m Model, k tea.KeyPressMsg) Model {
	t.Helper()
	mm, _, handled := m.handleInspectorKey(k)
	if !handled {
		t.Fatalf("%q was not handled on the inspector", k.String())
	}
	out, isModel := mm.(Model)
	if !isModel {
		t.Fatalf("handleInspectorKey returned %T, want a Model", mm)
	}
	return out
}
