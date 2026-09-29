package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/app/tui/sidepanel"
	"marshal/internal/commands"
)

// inspectorSizes are the three terminal sizes the plan names as acceptance
// sizes: a small terminal, a comfortable one, and a widescreen one.
var inspectorSizes = [][2]int{{80, 24}, {120, 40}, {200, 60}}

// inspectorModel builds a model with the rail enabled and a conversation, at a
// given size.
func inspectorModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(w, h)
	m.state.AddMessage(session.RoleUser, "what changed?", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant, "here is the answer", session.ContentTypeMarkdown)
	m.refreshViewport()
	m.refreshInspector()
	return m
}

// TestDraftSurvivesOpeningTheInspectorAtEverySize is the plan's headline
// acceptance case: the same inspector is reachable at 80x24, 120x40 and 200x60
// WITHOUT losing typed input. A read-only panel that costs the user their
// draft is one they learn not to open.
func TestDraftSurvivesOpeningTheInspectorAtEverySize(t *testing.T) {
	const draft = "half-written prompt"

	for _, size := range inspectorSizes {
		w, h := size[0], size[1]
		t.Run(sizeName(w, h), func(t *testing.T) {
			m := inspectorModel(t, w, h)
			m.input.SetValue(draft)

			// Ctrl+B, the documented key.
			mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
			got := asModel(t, mm)
			if !handled {
				t.Fatal("Ctrl+B was not handled")
			}
			if !got.inspector.isOpen() {
				t.Fatalf("Ctrl+B did not open the inspector at %dx%d", w, h)
			}
			if got.input.Value() != draft {
				t.Fatalf("draft = %q after opening the inspector, want %q", got.input.Value(), draft)
			}

			// And the draft survives a resize while it is open, which is the
			// combination the plan warns about.
			got.resize(w, h)
			if got.input.Value() != draft {
				t.Fatalf("draft = %q after a resize, want %q", got.input.Value(), draft)
			}
		})
	}
}

// TestInspectorStateIsReachableAcrossEverySize pins that the same inspector
// state survives moving between the three sizes: tab, scroll, filter and the
// detail stack are all still there.
func TestInspectorStateIsReachableAcrossEverySize(t *testing.T) {
	m := inspectorModel(t, inspectorSizes[0][0], inspectorSizes[0][1])
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	m.inspector.model.SetState(inspector.TabOverview, inspector.TabState{Scroll: 6, Filter: "app"})

	for i := 0; i < 12; i++ {
		w, h := inspectorSizes[i%len(inspectorSizes)][0], inspectorSizes[i%len(inspectorSizes)][1]
		m.resize(w, h)
		m.refreshInspector()

		if !m.inspector.isOpen() {
			t.Fatalf("iteration %d at %dx%d: the inspector became unreachable", i, w, h)
		}
		if got := m.inspector.model.State(inspector.TabOverview); got.Scroll != 6 || got.Filter != "app" {
			t.Fatalf("iteration %d at %dx%d: state = %+v, want scroll 6 filter app", i, w, h, got)
		}
		if m.input.Value() != "" {
			// The draft was never set here, but a resize must not type into
			// the composer either.
			t.Fatalf("iteration %d at %dx%d: composer gained %q", i, w, h, m.input.Value())
		}
	}
}

// TestInspectorOpensBelowTheThresholdViaTheCommand pins the plan's explicit
// rule: an explicit open works below the side threshold, and with initial
// side-panel visibility off.
func TestInspectorOpensBelowTheThresholdViaTheCommand(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24) // below the rail threshold
	m.refreshInspector()

	if m.inspectorSideAvailable() {
		t.Fatalf("the side reports available at 80x24; the threshold moved")
	}

	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	if !m.inspector.isOpen() {
		t.Fatal("an explicit open was refused below the threshold")
	}
	if m.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want the dock fallback", m.inspector.placement())
	}
	if m.input.Value() != "" {
		t.Fatalf("opening the inspector below the threshold typed %q into the composer", m.input.Value())
	}
}

func sizeName(w, h int) string { return fmt.Sprintf("%dx%d", w, h) }

// TestInspectorKeysScrollWhenItOwnsFocus pins that the inspector is a real
// focused surface: with focus on it, the scroll keys move the inspector rather
// than the transcript behind it. A focus target that receives keys and does
// nothing is a focus marker that lies.
func TestInspectorKeysScrollWhenItOwnsFocus(t *testing.T) {
	// A SHORT inspector: the Overview has to overflow its own column, or
	// scrolling correctly clamps at zero and the test would be asserting that
	// a working clamp is broken.
	m := inspectorModel(t, 200, 60)
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	if r := m.frameRect().Inspector; !r.Empty() {
		m.inspector.resize(r.Width, 6)
	}
	m.setFocus(FocusInspector)

	before := m.inspector.model.State(inspector.TabOverview).Scroll
	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyDown})
	got := asModel(t, mm)

	if !handled {
		t.Fatal("the down key was not handled while the inspector owned focus")
	}
	after := got.inspector.model.State(inspector.TabOverview).Scroll
	if after <= before {
		t.Fatalf("inspector scroll = %d after Down, want it advanced past %d", after, before)
	}
	if got.viewport.YOffset() != m.viewport.YOffset() {
		t.Fatalf("the transcript scrolled (%d -> %d) while the inspector owned focus",
			m.viewport.YOffset(), got.viewport.YOffset())
	}
}

// TestInspectorScrollClampsRatherThanScrollingForever pins the other half: a
// short Overview must NOT register a scroll at all. An unbounded scroll would
// let the user scroll into empty space and conclude the panel is broken.
func TestInspectorScrollClampsRatherThanScrollingForever(t *testing.T) {
	m := inspectorModel(t, 200, 60)
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	if r := m.frameRect().Inspector; !r.Empty() {
		m.inspector.resize(r.Width, 6)
	}
	m.inspector.model.SetState(inspector.TabOverview, inspector.TabState{Scroll: 10_000})
	m.refreshInspector()

	view := stripANSI(m.inspector.model.View(m.inspectorData()))
	if view == "" {
		t.Fatal("scrolled far past the end and the inspector rendered nothing")
	}
}

// TestDockPlacedInspectorOccupiesTheDockAndRenders pins the dock placement end
// to end: below the side threshold the inspector must take the dock slot, render
// there, and give the slot back when it closes. A placement that is recorded but
// never rendered is a panel the user cannot see.
func TestDockPlacedInspectorOccupiesTheDockAndRenders(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)
	m.state.AddMessageFinal(session.RoleAssistant, "an answer", session.ContentTypeMarkdown)
	m.refreshInspector()

	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	if m.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want dock below the threshold", m.inspector.placement())
	}

	// Rendering installs it in the slot.
	view := stripANSI(m.viewString())
	if m.dock.Panel() != dock.Panel(m.inspector.adapter) {
		t.Fatal("a dock-placed inspector did not claim the dock slot when the frame was rendered")
	}
	if m.frameRect().Dock.Empty() {
		t.Fatal("the dock rectangle is empty while the inspector claims the dock slot")
	}
	// The discriminator is a section heading the Overview renders from the
	// rail's data. The rail itself is DISABLED in this model
	// (SidePanel.Enabled = false), so this text can only have come from the
	// inspector — which is exactly the claim being tested.
	if !strings.Contains(view, "REPO") {
		t.Fatalf("the dock-placed inspector rendered no recognizable section:\n%s", view)
	}

	// Closing gives the slot back, so the next panel can have it.
	m.inspector.close()
	m.viewString()
	if m.dock.Panel() == dock.Panel(m.inspector.adapter) {
		t.Fatal("the dock slot was not released when the inspector closed")
	}
	if m.dock.IsOpen() {
		t.Fatal("the dock is still open after the inspector closed")
	}
}

// TestDockPlacementDoesNotEvictAnotherPanel pins the suspension rule at the
// dock: a panel that already owns the slot must not be replaced by the
// inspector, because that would discard whatever the user was in the middle of.
func TestDockPlacementDoesNotEvictAnotherPanel(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = false
	m.resize(80, 24)

	// A competing panel takes the slot first.
	m.openDocPanel(&commands.Doc{Title: "occupant", Rows: []commands.Row{{Text: "row"}}})
	if m.dock.Panel() == nil {
		t.Fatal("precondition: a panel must be in the dock")
	}
	occupant := m.dock.Panel()

	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	m.viewString()

	if m.dock.Panel() != occupant {
		t.Fatal("the inspector evicted the panel that already owned the dock slot")
	}
}

// TestInspectorEscBacksOutDepthBeforeLeavingFocus pins the Esc ordering that
// makes one key press do one thing. With the body expanded, the first Esc must
// leave that placement — not the focus move, and not the whole panel.
func TestInspectorEscBacksOutDepthBeforeLeavingFocus(t *testing.T) {
	m := inspectorModel(t, 120, 40)
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	m.inspector.expandBody()

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Esc was not handled while body-expanded")
	}
	if got.inspector.replacesBodyOnly() {
		t.Fatal("Esc did not leave body-expanded")
	}
	if !got.inspector.isOpen() {
		t.Fatal("Esc closed the inspector instead of leaving body-expanded")
	}
}

// TestInspectorEscPopsADetailBeforeLeavingFocus pins the other half of the
// ordering: with a detail open the first Esc pops it, and the inspector stays
// open.
func TestInspectorEscPopsADetailBeforeLeavingFocus(t *testing.T) {
	m := inspectorModel(t, 200, 60)
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	m.setFocus(FocusInspector)
	m.inspector.model.OpenTarget(inspector.Target{
		Kind: inspector.TargetChangedFile, Scope: "s1", ID: "internal/app/tui/view.go",
	})
	if m.inspector.model.Depth() != 1 {
		t.Fatalf("precondition: depth = %d, want 1", m.inspector.model.Depth())
	}

	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := asModel(t, mm)
	if !handled {
		t.Fatal("Esc was not handled with a detail open")
	}
	if got.inspector.model.Depth() != 0 {
		t.Fatalf("depth = %d after Esc, want the detail popped", got.inspector.model.Depth())
	}
	if !got.inspector.isOpen() {
		t.Fatal("Esc closed the whole inspector instead of popping one detail")
	}
}

// TestInspectorCloseMessageClosesIt pins the message the dock adapter emits, so
// a CloseMsg from the panel reaches the host rather than being dropped by the
// model's message switch.
func TestInspectorCloseMessageClosesIt(t *testing.T) {
	m := inspectorModel(t, 80, 24)
	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	if !m.inspector.isOpen() {
		t.Fatal("precondition: the inspector must be open")
	}

	mm, _ := m.Update(inspector.CloseMsg{})
	got := asModel(t, mm)
	if got.inspector.isOpen() {
		t.Fatal("inspector.CloseMsg did not close the inspector")
	}
	if got.input.Value() != "" {
		t.Fatalf("closing typed %q into the composer", got.input.Value())
	}
}

// TestInspectorReplacesTheRailRatherThanStackingWithIt pins that the two
// second-column surfaces never appear together. They render from the SAME data,
// so showing both would print every number twice while charging the
// conversation twice for the width.
//
// The probe is the changed-file path: it is rendered by both surfaces from the
// same list, so its occurrence count across the frame is exactly the evidence
// needed — one column means one occurrence.
func TestInspectorReplacesTheRailRatherThanStackingWithIt(t *testing.T) {
	m := inspectorModel(t, 200, 60)
	if !m.railEnabled() {
		t.Fatal("the rail is not enabled at 200x60; the test cannot observe the swap")
	}
	m.state.Config.TUI.SidePanel.Hidden = nil
	m.rebuildRail()
	m.railChanged = []sidepanel.ChangedFile{
		{Path: "internal/app/tui/markerfile.go", Status: 'M', Added: 3, Removed: 1},
	}
	m.refreshInspector()

	const probe = "markerfile.go"
	withoutInspector := stripANSI(m.viewString())
	railCount := strings.Count(withoutInspector, probe)
	if railCount == 0 {
		t.Fatalf("the rail did not render the changed file, so the swap cannot be observed:\n%s", withoutInspector)
	}
	if !m.inspector.isRendering() {
		// Open it now that the baseline is measured.
		m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	}
	if m.inspector.placement() != inspectorSide {
		t.Fatalf("placement = %v, want side at 200x60", m.inspector.placement())
	}
	withInspector := stripANSI(m.viewString())

	if got := strings.Count(withInspector, probe); got != railCount {
		t.Fatalf("the changed-file path appears %d times with the inspector open and %d with the rail alone: "+
			"the two surfaces are stacking instead of swapping", got, railCount)
	}
}

// TestInspectorRendersTheRailsData pins that the inspector shows the same
// numbers the rail does. They share one snapshot, so a disagreement here would
// mean the wiring handed the inspector a second, different one.
func TestInspectorRendersTheRailsData(t *testing.T) {
	m := inspectorModel(t, 200, 60)
	m.state.Config.TUI.SidePanel.Hidden = nil
	m.rebuildRail()
	m.railChanged = []sidepanel.ChangedFile{
		{Path: "internal/app/tui/sharedsnapshot.go", Status: 'M', Added: 5, Removed: 2},
	}
	m.refreshInspector()

	railView := stripANSI(m.rail.View(m.railData(), m.railWidth, m.frameRect().Body().Height))
	if !strings.Contains(railView, "sharedsnapshot.go") {
		t.Fatalf("the rail did not render the changed file:\n%s", railView)
	}

	m.inspector.open(inspector.TabOverview, m.inspectorSideAvailable())
	inspectorView := stripANSI(m.inspector.model.View(m.inspectorData()))
	if !strings.Contains(inspectorView, "sharedsnapshot.go") {
		t.Fatalf("the inspector did not render the changed file the rail showed:\n%s", inspectorView)
	}
}
