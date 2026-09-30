package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/tui/inspector"
)

// inspectTestModel builds a model at a given size with the real command
// registry wired, so /inspect reaches the TUI effect table rather than being
// refused as an unknown command.
func inspectTestModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(w, h)
	return m
}

// visibleTabSet is the offered tab set as a lookup, so the tests below can
// partition AllTabs into "reachable" and "not yet implemented" without
// hardcoding either list.
func visibleTabSet() map[inspector.Tab]bool {
	out := map[inspector.Tab]bool{}
	for _, tab := range inspector.VisibleTabs() {
		out[tab] = true
	}
	return out
}

// TestInspectCommandOpensInspector pins the bare command: it opens the
// inspector on its current tab, beside the conversation when there is room.
// A successful open is visible on screen, so it must not also narrate itself
// into the transcript or the toast.
func TestInspectCommandOpensInspector(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	if !m.inspectorSideAvailable() {
		t.Fatal("precondition: the side column must be available at 160x40")
	}
	before := len(m.state.Messages())

	m.dispatchCommand("/inspect")

	if !m.inspector.isOpen() {
		t.Fatal("/inspect did not open the inspector")
	}
	if m.inspector.placement() != inspectorSide {
		t.Fatalf("placement = %v, want side", m.inspector.placement())
	}
	if got, want := m.inspector.model.SelectedTab(), inspector.VisibleTabs()[0]; got != want {
		t.Fatalf("selected tab = %q, want the default %q", got, want)
	}
	if got := len(m.state.Messages()); got != before {
		t.Fatalf("transcript grew from %d to %d messages on a successful open", before, got)
	}
	if toast := m.toastText(); toast != "" {
		t.Fatalf("toast = %q, want no feedback on a successful open", toast)
	}
}

// TestInspectCommandIsIdempotent pins that a second bare /inspect returns the
// user to the inspector they already have rather than resetting it.
func TestInspectCommandIsIdempotent(t *testing.T) {
	m := inspectTestModel(t, 160, 40)

	m.dispatchCommand("/inspect")
	first := m.inspector.model.SelectedTab()
	m.dispatchCommand("/inspect")

	if !m.inspector.isOpen() {
		t.Fatal("the second /inspect closed the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != first {
		t.Fatalf("selected tab = %q after a repeat /inspect, want %q", got, first)
	}
}

// TestInspectCommandOpensEveryVisibleTab drives the command from
// inspector.VisibleTabs() rather than a hardcoded list: a tab that becomes
// available in a later task must be reachable through /inspect the moment it
// lands, with no change here.
func TestInspectCommandOpensEveryVisibleTab(t *testing.T) {
	for _, tab := range inspector.VisibleTabs() {
		t.Run(string(tab), func(t *testing.T) {
			m := inspectTestModel(t, 160, 40)

			m.dispatchCommand("/inspect " + string(tab))

			if !m.inspector.isOpen() {
				t.Fatalf("/inspect %s did not open the inspector", tab)
			}
			if got := m.inspector.model.SelectedTab(); got != tab {
				t.Fatalf("selected tab = %q, want %q", got, tab)
			}
			if toast := m.toastText(); toast != "" {
				t.Fatalf("toast = %q, want no feedback on a successful open", toast)
			}
		})
	}
}

// TestInspectCommandCloseClosesInspector pins the explicit close.
func TestInspectCommandCloseClosesInspector(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.dispatchCommand("/inspect")
	if !m.inspector.isOpen() {
		t.Fatal("precondition: the inspector must be open")
	}

	m.dispatchCommand("/inspect close")

	if m.inspector.isOpen() {
		t.Fatal("/inspect close left the inspector open")
	}
	if m.inspector.placement() != inspectorClosed {
		t.Fatalf("placement = %v, want closed", m.inspector.placement())
	}
}

// TestInspectCommandReportsTabsThatAreNotAvailableYet is the important
// refusal: a tab that exists in the product set but has no implementation
// must not open, and must not be silently ignored either. The user asked for
// a specific view and has to be told it is not there yet, by name.
func TestInspectCommandReportsTabsThatAreNotAvailableYet(t *testing.T) {
	visible := visibleTabSet()
	checked := 0
	for _, tab := range inspector.AllTabs() {
		if visible[tab] {
			continue
		}
		checked++
		t.Run(string(tab), func(t *testing.T) {
			m := inspectTestModel(t, 160, 40)

			m.dispatchCommand("/inspect " + string(tab))

			if m.inspector.isOpen() {
				t.Fatalf("/inspect %s opened a view that has no implementation", tab)
			}
			toast := m.toastText()
			if !strings.Contains(toast, string(tab)) {
				t.Fatalf("toast = %q, want it to name the %s view", toast, tab)
			}
			if !strings.Contains(toast, "not available") {
				t.Fatalf("toast = %q, want it to say the view is not available yet", toast)
			}
			for _, v := range inspector.VisibleTabs() {
				if !strings.Contains(toast, string(v)) {
					t.Fatalf("toast = %q, want it to list the available view %q", toast, v)
				}
			}
		})
	}
	if checked == 0 {
		// Every tab is implemented, so the refusal path is unreachable and
		// there is nothing left to assert. Skipping (rather than failing)
		// keeps the later task that makes the full set available from having
		// to delete this test.
		t.Skip("every tab is visible; the not-available-yet path is unreachable")
	}
}

// TestInspectCommandReportsUnknownTab pins the typo case: an unknown name is
// a different answer from a not-yet-implemented one, and it lists what the
// user can actually ask for.
func TestInspectCommandReportsUnknownTab(t *testing.T) {
	m := inspectTestModel(t, 160, 40)

	m.dispatchCommand("/inspect nonsense")

	if m.inspector.isOpen() {
		t.Fatal("/inspect nonsense opened the inspector")
	}
	toast := m.toastText()
	if !strings.Contains(toast, "nonsense") {
		t.Fatalf("toast = %q, want it to name the unknown view", toast)
	}
	for _, v := range inspector.VisibleTabs() {
		if !strings.Contains(toast, string(v)) {
			t.Fatalf("toast = %q, want it to list the available view %q", toast, v)
		}
	}
}

// TestInspectCommandOpensBelowTheSideThreshold is the plan's explicit rule:
// an explicit open works even when the terminal has no room for a second
// column. It falls back to the dock rather than refusing.
func TestInspectCommandOpensBelowTheSideThreshold(t *testing.T) {
	m := inspectTestModel(t, 80, 24)
	if m.inspectorSideAvailable() {
		t.Fatal("precondition: the side column must be unavailable at 80x24")
	}

	m.dispatchCommand("/inspect")

	if !m.inspector.isOpen() {
		t.Fatal("/inspect refused below the side threshold; an explicit open must still work")
	}
	if m.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want dock below the threshold", m.inspector.placement())
	}
}

// TestInspectCommandOpensNamedTabBelowTheSideThreshold covers the same rule
// for an explicit tab, which is the form the command is most likely to be
// typed in.
func TestInspectCommandOpensNamedTabBelowTheSideThreshold(t *testing.T) {
	m := inspectTestModel(t, 80, 24)
	if m.inspectorSideAvailable() {
		t.Fatal("precondition: the side column must be unavailable at 80x24")
	}
	tab := inspector.VisibleTabs()[0]

	m.dispatchCommand("/inspect " + string(tab))

	if !m.inspector.isOpen() {
		t.Fatalf("/inspect %s refused below the side threshold", tab)
	}
	if got := m.inspector.model.SelectedTab(); got != tab {
		t.Fatalf("selected tab = %q, want %q", got, tab)
	}
	if m.inspector.placement() != inspectorDock {
		t.Fatalf("placement = %v, want dock below the threshold", m.inspector.placement())
	}
}

// TestInspectCommandWithoutHostReportsRatherThanPanics pins the defensive
// branch: a build with no inspector host must answer the command, not crash
// on a nil dereference.
func TestInspectCommandWithoutHostReportsRatherThanPanics(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.inspector = nil

	m.dispatchCommand("/inspect")

	msgs := m.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a message explaining the inspector is unavailable")
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "inspector") {
		t.Fatalf("message = %q, want it to name the inspector", last.Content)
	}
}

// TestInspectCommandReportsUnavailableTabBelowTheThreshold pins that the
// refusal is reported wherever the inspector would have rendered: a narrow
// terminal must not turn "not available yet" into silence.
func TestInspectCommandReportsUnavailableTabBelowTheThreshold(t *testing.T) {
	visible := visibleTabSet()
	var missing inspector.Tab
	for _, tab := range inspector.AllTabs() {
		if !visible[tab] {
			missing = tab
			break
		}
	}
	if missing == "" {
		t.Skip("every tab is visible; the not-available-yet path is unreachable")
	}

	m := inspectTestModel(t, 80, 24)
	m.dispatchCommand("/inspect " + string(missing))

	if m.inspector.isOpen() {
		t.Fatalf("/inspect %s opened a view that has no implementation", missing)
	}
	if toast := m.toastText(); !strings.Contains(toast, string(missing)) {
		t.Fatalf("toast = %q, want it to name the %s view", toast, missing)
	}
}
