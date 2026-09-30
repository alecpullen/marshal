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
