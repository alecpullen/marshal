// internal/app/tui/inspector_dock_keys_test.go — the docked inspector must be
// reachable through the PRODUCTION View/Update path.
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/commands"
)

// TestDockPlacedInspectorOwnsTheKeysThroughViewAndUpdate is the regression for
// the review's Critical 1.
//
// The dock slot used to be claimed from viewString, which View reaches through a
// VALUE receiver: the claim landed on a copy that was discarded the moment the
// frame was returned, so the CANONICAL model never held the panel. Below the side
// threshold — where the inspector falls back to the dock — that made /inspect and
// Ctrl+B draw a panel that no key and no click could drive: dock.IsOpen() stayed
// false, panelOwnsKeys() stayed false, key routing fell through to the composer,
// and availableFocusTargets never offered FocusInspector because the rail is
// disabled there.
//
// The passing TestDockPlacedInspectorOccupiesTheDockAndRenders masked the bug
// because it called viewString() directly on a pointer receiver. This test drives
// the production entry points instead, and asserts the two properties that were
// actually false: the canonical model has the dock claimed, and a modal surface
// owns the keys.
func TestDockPlacedInspectorOwnsTheKeysThroughViewAndUpdate(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)
	m.state.AddMessageFinal(session.RoleAssistant, "an answer", session.ContentTypeMarkdown)

	if m.inspectorSideAvailable() {
		t.Fatal("precondition: the side reports available at 80x24, so this is not the dock case")
	}

	// Open it the way a user does: the Ctrl+B key, through Update.
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	got := asModel(t, mm)

	if !got.inspector.isOpen() {
		t.Fatal("Ctrl+B did not open the inspector")
	}
	if got.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want the dock fallback below the side threshold",
			got.inspector.placement())
	}

	// THE assertions. Both were false before the fix, and both are what makes the
	// panel reachable rather than merely visible.
	if got.dock.Panel() != dock.Panel(got.inspector.adapter) {
		t.Fatal("the canonical model does not hold the inspector in the dock slot: " +
			"no key or click can reach a panel the model thinks is not open")
	}
	if !got.panelOwnsKeys() {
		t.Fatal("panelOwnsKeys() is false with the inspector docked and drawn: " +
			"key routing falls through to the composer behind the panel")
	}
	if focus := got.effectiveFocus(); focus != FocusPanel {
		t.Fatalf("effectiveFocus() = %v with a docked panel open, want FocusPanel", focus)
	}

	// And it renders. A claim without a render would be the mirror-image failure.
	//
	// The discriminator is the adapter's OWN output, compared as a substring of
	// the frame: any literal would pin this test to whichever tab's data happens
	// to be populated in the fixture, and the property under test is "the panel
	// the model claims is the panel that was drawn", not "the Overview says REPO".
	frame := stripANSI(got.View().Content)
	panel := stripANSI(got.inspector.adapter.View(got.leftWidth, dock.MaxRows(got.height)))
	if panel == "" {
		t.Fatal("the docked inspector rendered nothing at all")
	}
	if !strings.Contains(frame, strings.TrimSpace(panel)) {
		t.Fatalf("the frame does not contain the panel the model claims to hold.\nframe:\n%s", frame)
	}

	// Closing through the same production path must release the slot, or the next
	// key would be sent to a panel that is gone.
	mm, _ = got.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	closed := asModel(t, mm)
	if closed.dock.Panel() == dock.Panel(closed.inspector.adapter) {
		t.Fatal("the dock slot was not released when the inspector closed")
	}
	if closed.panelOwnsKeys() {
		t.Fatal("panelOwnsKeys() is true after the docked inspector closed")
	}
}

// TestDockSlotIsClaimedByThePlacementChangeNotTheRender pins the mechanism rather
// than the symptom: the claim must happen on the message that changed the
// placement, so the VERY NEXT message is already routed to the panel.
//
// A fix that claimed the slot lazily — on the following render, or on some later
// unrelated message — would pass the test above (which renders in between) while
// still losing the first keystroke the user types.
func TestDockSlotIsClaimedByThePlacementChangeNotTheRender(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)

	// Open via the command dispatch path, with NO render in between.
	mm, _ := m.dispatchCommand("/inspect overview")
	got := asModel(t, mm)

	if !got.inspector.isOpen() {
		t.Fatal("/inspect did not open the inspector")
	}
	if got.dock.Panel() != dock.Panel(got.inspector.adapter) {
		t.Fatal("the dock slot is not claimed on the message that opened the inspector: " +
			"the first keypress after opening would be routed to the composer")
	}
}

// dockedInspectorModel builds a model whose inspector is DOCK-placed — the
// placement a terminal below the rail threshold falls back to, and the one where
// the dock is the only slot the inspector can have.
func dockedInspectorModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)
	return m
}

// TestEnterOnADockedChangesTabOpensTheDiff is the regression for the review's
// D2, reader-visible half.
//
// The dock branch of Update diverted EVERY keypress to the dock adapter, whose
// keymap has up/down/pgup/pgdn/home/end/tab/esc and nothing else. handleInspectorKey
// — which is where the tab contract lives — was therefore unreachable in the dock
// placement, so Enter on a selected changed file opened nothing. Below the rail
// threshold, where the inspector falls back to the dock, there was no placement
// at all in which the keyboard could open a diff.
//
// The key is driven through the production Update path, because the bug was in
// that routing: calling handleInspectorKey directly is what the old code could
// never do.
func TestEnterOnADockedChangesTabOpensTheDiff(t *testing.T) {
	dir, head := railFixtureRepo(t)
	m := dockedInspectorModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: dir, ActiveRoot: dir})
	m.railBaseRef = head
	// Open FIRST: the changed-files read is gated on something needing it
	// (railEnabled OR the inspector rendering), and in this model the rail is
	// disabled by construction — that is what forces the dock placement.
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshRailChanged()
	m.refreshInspector()

	if m.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want the dock: this test is about the dock placement",
			m.inspector.placement())
	}
	// Claim the slot the way the message that changed the placement does, so the
	// key is routed against the same state a real session has.
	m.syncDock()
	if !m.inspectorDockOwnsSlot() {
		t.Fatal("the inspector does not hold the dock slot, so this is not the dock-key case")
	}

	selectChangedPath(t, &m, "a.go")

	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := asModel(t, mm)

	if !got.inspector.model.HasDiff() {
		t.Fatal("Enter on a docked Changes tab did not open the selected file")
	}
	if cmd == nil {
		t.Fatal("Enter produced no read command, so no patch would ever be fetched")
	}
	msg := cmd()
	loaded, ok := msg.(inspector.DiffLoadedMsg)
	if !ok {
		t.Fatalf("Enter's command produced %T, want inspector.DiffLoadedMsg", msg)
	}
	if loaded.Path != "a.go" {
		t.Fatalf("Enter read %q, want a.go", loaded.Path)
	}

	// And the patch reaches the panel through the same production path.
	//
	// The assertion is on the panel's own loaded state rather than on a
	// substring of the render: the dock is a short panel, so a diff's body
	// scrolls out of view and a substring test would be measuring the dock's
	// height instead of whether Enter worked.
	mm, _ = got.Update(msg)
	after := asModel(t, mm)
	patch, _, _, ok := after.inspector.model.CapturedPatch()
	if !ok {
		t.Fatal("the delivered diff never reached the docked panel")
	}
	if !strings.Contains(patch, "func A() {}") {
		t.Fatalf("the docked panel holds the wrong patch:\n%s", patch)
	}
	if view := stripANSI(after.inspector.model.View(after.inspectorData())); !strings.Contains(view, "a.go") {
		t.Fatalf("the open diff's header is not on the docked panel:\n%s", view)
	}
}

// TestDockedInspectorTabKeysReachTheTabAndNotTheAdapter pins the other half of
// D2 with a key the adapter has NO binding for.
//
// Up/Down would not do: the adapter's own keymap also moves the cursor (through
// moveCursor), so a test on them passes with or without the fix and proves
// nothing. Left/Right on the Context tab is the discriminating case — cycling the
// scope is a binding that exists ONLY in handleInspectorKey, and the adapter
// would forward it to moveContextSelection's list, which is empty, doing nothing.
func TestDockedInspectorTabKeysReachTheTabAndNotTheAdapter(t *testing.T) {
	m := dockedInspectorModel(t)
	m.inspector.open(inspector.TabContext, m.inspectorSideAvailable())
	m.refreshInspector()
	m.syncDock()
	if !m.inspectorDockOwnsSlot() {
		t.Fatal("the inspector does not hold the dock slot, so this is not the dock-key case")
	}

	// The pack scope is the default; the request scope is the other one. Nothing
	// has been recorded for either, which does not matter: the selection is the
	// thing under test, and no request has to exist for the scope to move.
	if got := m.inspector.model.ContextScope(); got != inspector.ContextScopePack {
		t.Fatalf("precondition: scope = %q, want the default pack scope", got)
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got := asModel(t, mm)

	if got.inspector.model.ContextScope() == inspector.ContextScopePack {
		t.Fatal("Right on a docked Context tab did not cycle the scope: the tab's own " +
			"keys never reached handleInspectorKey, so the dock placement had no binding " +
			"for the scope at all")
	}

	// And back, so the key is a two-way binding rather than a one-shot.
	mm, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	back := asModel(t, mm)
	if back.inspector.model.ContextScope() != inspector.ContextScopePack {
		t.Fatalf("Left did not cycle the scope back; got %q", back.inspector.model.ContextScope())
	}
}

// TestCtrlXStopsTheAgentFromTheDockedInspector is the regression for the
// review's D1.
//
// The footer advertises Ctrl+X whenever stopping the inspected agent is
// available — and the state it exists for is precisely a docked Agents tab
// showing a running child. The dock branch swallowed the key, so the one control
// the footer was pointing at did nothing, while the key that closed the panel
// (Ctrl+B) had been carefully exempted. The two are now one mechanism.
//
// The assertion is on the child the runtime was asked to cancel, not on a return
// value: "handled" is what the old code got wrong, and it would pass on a
// no-op.
func TestCtrlXStopsTheAgentFromTheDockedInspector(t *testing.T) {
	m := dockedInspectorModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child text", session.ContentTypePlain)
	running := m.state.RegisterSubagent("running child", child)
	cancels := &cancelRecorder{}
	m.state.SetSubagentCancel(running.ID, cancels.cancel)

	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.refreshInspector()
	m.syncDock()
	if !m.inspectorDockOwnsSlot() {
		t.Fatal("the inspector does not hold the dock slot, so this is not the dock-key case")
	}

	// The detail is opened through the inspector's own API rather than by
	// pressing Enter: this test is about Ctrl+X, and driving the setup through
	// another dock key would make the two failures indistinguishable.
	if !m.inspector.model.EnterAgent() {
		t.Fatal("precondition: EnterAgent refused the running child")
	}
	m.refreshInspector()
	if id, ok := m.actionSnapshot().ctrlXID(); !ok || id != ActionStopAgent {
		t.Fatalf("precondition: Ctrl+X resolves to %q/%v with a docked running Agents selection",
			id, ok)
	}

	mm, _ := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	after := asModel(t, mm)

	if !cancels.cancelled() {
		t.Fatal("Ctrl+X did not reach the running child while the inspector held the dock: " +
			"the footer advertises this key in exactly this state")
	}
	// The panel is still up: stopping an agent must not also close the view the
	// user is stopping it from.
	if !after.inspectorDockOwnsSlot() {
		t.Fatal("Ctrl+X closed the docked inspector instead of stopping the agent")
	}
}

// TestDockedInspectorGlobalKeySetIsExplicit pins WHICH keys survive the dock, as
// a table.
//
// The set answers the review's D1 question — "resolve the action catalog's global
// keys; check the catalog for what else is global" — and a table is the right
// shape for it because the answer has to be reviewable: a key accidentally added
// to handleDockGlobalKey makes a panel key unreachable, and a key accidentally
// missing leaves the footer advertising something dead. Both show up here as a
// row that moved.
//
// The effect is asserted, not the routing: each row names an observable
// consequence, so a key wired to the wrong action fails rather than passing on
// "something happened".
func TestDockedInspectorGlobalKeySetIsExplicit(t *testing.T) {
	cases := []struct {
		key  string
		msg  tea.KeyPressMsg
		want func(t *testing.T, m Model)
	}{
		{
			key: "ctrl+b",
			msg: tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl},
			want: func(t *testing.T, m Model) {
				t.Helper()
				if m.inspector.isOpen() {
					t.Fatal("Ctrl+B did not close the docked inspector")
				}
			},
		},
		{
			key: "ctrl+s",
			msg: tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl},
			want: func(t *testing.T, m Model) {
				t.Helper()
				if m.mouseOverride != MouseRelease {
					t.Fatalf("mouseOverride = %v, want the capture toggled off", m.mouseOverride)
				}
			},
		},
		{
			key: "ctrl+r",
			msg: tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl},
			want: func(t *testing.T, m Model) {
				t.Helper()
				// Ctrl+R is arm-then-confirm, and the arming step is the
				// observable effect of the first press.
				if !m.rollbackArmed {
					t.Fatal("Ctrl+R did not arm the rollback")
				}
			},
		},
		{
			key: "f6",
			msg: tea.KeyPressMsg{Code: tea.KeyF6},
			want: func(t *testing.T, m Model) {
				t.Helper()
				// The rail is disabled in this model, so the only cycle stops are
				// the composer and the conversation. What matters is that the key
				// reached cycleFocus at all instead of being swallowed: the focus
				// moves off FocusInspector onto an available target.
				if got := m.focus; got != FocusComposer && got != FocusConversation {
					t.Fatalf("focus = %v after F6, want an available non-modal target", got)
				}
			},
		},
		{
			key: "f2",
			msg: tea.KeyPressMsg{Code: tea.KeyF2},
			want: func(t *testing.T, m Model) {
				t.Helper()
				// F2 IS routed through the catalog, and the catalog refuses it
				// while a panel owns the keys ("resolve the open panel or
				// decision first"). The assertion is the CATALOG's verdict
				// reaching the user, not the palette opening: a key that
				// resolved to a disabled action and said so is the honest
				// outcome, and before the fix the key did nothing and said
				// nothing.
				if m.pickerCommand == actionPaletteCommand {
					t.Fatal("the palette opened over a panel that owns the keys")
				}
				if toast := m.toastText(); !strings.Contains(toast, "Action palette") {
					t.Fatalf("toast = %q, want the catalog's reason for refusing the palette", toast)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			m := dockedInspectorModel(t)
			m.state.AddMessage(session.RoleAssistant, "an answer", session.ContentTypeMarkdown)
			// A backup has to exist or Ctrl+R's action is unavailable and the
			// arming step would never run — the row would then pass while
			// testing the unavailability path instead of the binding.
			m.state.StoreBackup([]session.BackupFile{{Path: "internal/app/tui/x.go"}})
			m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
			m.refreshInspector()
			m.syncDock()
			if !m.inspectorDockOwnsSlot() {
				t.Fatal("the inspector does not hold the dock slot")
			}

			mm, _ := m.Update(tc.msg)
			tc.want(t, asModel(t, mm))
		})
	}
}

// TestDockedInspectorPanelKeysStillReachThePanel is the negative half of the same
// table: a key the panel owns must NOT be intercepted as global, or the exemption
// would have quietly become "the inspector loses its own keymap when docked".
func TestDockedInspectorPanelKeysStillReachThePanel(t *testing.T) {
	m := dockedInspectorModel(t)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshInspector()
	m.syncDock()
	if !m.inspectorDockOwnsSlot() {
		t.Fatal("the inspector does not hold the dock slot")
	}
	before := m.inspector.model.SelectedTab()

	// Tab belongs to the panel: it cycles the tab bar, and it is one of the
	// bindings handleInspectorKey deliberately leaves to the adapter.
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got := asModel(t, mm)

	if got.inspector.model.SelectedTab() == before {
		t.Fatalf("Tab did not advance the tab bar from %q: a panel key was intercepted", before)
	}
	if !got.inspectorDockOwnsSlot() {
		t.Fatal("Tab evicted the inspector from the dock slot")
	}
}

// TestDockGlobalKeysDoNotLeakToAnotherPanel pins the boundary of the exemption.
// The inspector's own adapter is the ONLY panel that gets the global keys: a
// different panel in the slot keeps it exclusively, exactly as the "the open
// panel owns every key" contract promises for every one of them.
func TestDockGlobalKeysDoNotLeakToAnotherPanel(t *testing.T) {
	m := dockedInspectorModel(t)
	child := newChildState(t)
	child.AddMessage(session.RoleAssistant, "child text", session.ContentTypePlain)
	running := m.state.RegisterSubagent("running child", child)
	cancels := &cancelRecorder{}
	m.state.SetSubagentCancel(running.ID, cancels.cancel)

	// A competing panel owns the slot, so the inspector is not dock-placed at all.
	m.openDocPanel(&commands.Doc{Title: "occupant", Rows: []commands.Row{{Text: "row"}}})
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.syncDock()

	if m.inspectorDockOwnsSlot() {
		t.Fatal("precondition: the inspector should not own the slot")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if cancels.cancelled() {
		t.Fatal("Ctrl+X reached the runtime while another panel owned the dock slot")
	}
}

// TestFrameDockRectMatchesTheRenderedDock is the regression for the review's
// Important 1.
//
// frame.go claims "rendering and pointer routing cannot disagree about a row".
// It did not hold: dock.Host.rows is set only by dock.View, which ran only inside
// viewString, so the canonical model's dockRows() was 0 for every open panel. The
// frame's Dock rectangle therefore stayed empty and computeFrame measured a
// Transcript rectangle extending over the rows where the panel was drawn — so
// contentLineForClick resolved a click on the panel to a transcript line that was
// not on screen.
func TestFrameDockRectMatchesTheRenderedDock(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)
	m.state.AddMessageFinal(session.RoleAssistant, "an answer", session.ContentTypeMarkdown)

	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	if m.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want dock below the threshold", m.inspector.placement())
	}
	// The canonical model, with no View call: this is the state a pointer event
	// is routed against.
	m.refreshViewport()

	if dockRows := m.dockRows(); dockRows <= 0 {
		t.Fatal("dockRows() is 0 while a panel is open: the frame cannot know the panel's height")
	}
	f := m.frameRect()
	if f.Dock.Empty() {
		t.Fatalf("the canonical frame's Dock rectangle is empty while a panel is open: %+v", f)
	}
	// The rendered height, checked against the rectangle. View is what records the
	// true height, so rendering first makes the two comparable.
	m.viewString()
	if got := m.dockHeight(); got != f.Dock.Height {
		t.Fatalf("the frame's Dock rectangle is %d rows but the panel rendered %d",
			f.Dock.Height, got)
	}
	if m.dock.Rows() != f.Dock.Height {
		t.Fatalf("the dock host rendered %d rows but the frame claims %d",
			m.dock.Rows(), f.Dock.Height)
	}
	// The rectangles must not overlap: a click in the dock must not resolve to a
	// transcript line.
	if !f.Transcript.Empty() && f.Transcript.Bottom() > f.Dock.Y {
		t.Fatalf("the Transcript rectangle reaches row %d but the Dock starts at %d",
			f.Transcript.Bottom(), f.Dock.Y)
	}
	// And a click inside the dock must be declined by the transcript router.
	if line, ok := m.contentLineForClick(0, f.Dock.Y); ok {
		t.Fatalf("a click on the dock's first row resolved to transcript line %d", line)
	}
}
