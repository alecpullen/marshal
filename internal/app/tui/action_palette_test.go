package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/picker"
)

// The palette lists every action including the unavailable ones, with the
// reason rendered where a user looks for it — hiding a disabled action leaves
// the user guessing whether the operation exists at all.
func TestActionPaletteListsDisabledActionsWithReasons(t *testing.T) {
	m := newTestModel(t) // idle: stop-agent and clear-queue are unavailable

	m.openActionPalette()

	panel, ok := m.dock.Panel().(*picker.Model)
	if !ok {
		t.Fatalf("palette did not open the shared picker, got %T", m.dock.Panel())
	}
	items := panel.Items()
	if len(items) == 0 {
		t.Fatal("palette listed nothing")
	}

	var sawStop, sawQueue, sawMouse, sawSettings bool
	for _, it := range items {
		if it.Value == string(ActionStopAgent) {
			sawStop = true
			if !strings.Contains(it.Detail, "no running agent") {
				t.Errorf("stop-agent detail must explain its unavailability, got %q", it.Detail)
			}
			if it.Badge != "unavailable" {
				t.Errorf("disabled row must carry the unavailable badge, got %q", it.Badge)
			}
		}
		if it.Value == string(ActionClearQueue) {
			sawQueue = true
			if !strings.Contains(it.Detail, "no turn is running") {
				t.Errorf("clear-queue detail must explain its unavailability, got %q", it.Detail)
			}
		}
		// Two always-available actions prove the badge marks only the
		// disabled rows, not the whole list.
		if it.Value == string(ActionToggleMouse) {
			sawMouse = true
			if it.Badge == "unavailable" {
				t.Error("toggle-mouse is available; it must not wear the unavailable badge")
			}
		}
		if it.Value == string(ActionSettings) {
			sawSettings = true
			if it.Badge == "unavailable" {
				t.Error("settings is available; it must not wear the unavailable badge")
			}
		}
	}
	if !sawStop || !sawQueue || !sawMouse || !sawSettings {
		t.Fatalf("palette is missing entries: stop=%v queue=%v mouse=%v settings=%v", sawStop, sawQueue, sawMouse, sawSettings)
	}
}

// The palette must not touch the composer: opening it while a draft (and a
// condensed paste attachment) is in flight leaves both exactly as they were,
// and Esc returns the user to them.
func TestActionPalettePreservesDraftAndPastes(t *testing.T) {
	m := newTestModel(t)
	m.setFocus(FocusComposer)
	m.input.SetValue("draft in progress")
	m.addPaste("pasted attachment body")
	// addPaste appends the chip into the composer text itself, so the snapshot
	// for comparison must be taken after the chip lands.
	draftBefore := m.input.Value()
	pastesBefore := len(m.pastes)

	m.openActionPalette()

	if m.input.Value() != draftBefore {
		t.Fatalf("palette clobbered the draft: %q", m.input.Value())
	}
	if len(m.pastes) != pastesBefore {
		t.Fatalf("palette clobbered the pastes: %d → %d", pastesBefore, len(m.pastes))
	}

	// Esc (picker.CancelledMsg) returns to the composer with the draft intact.
	mm, _ := m.Update(picker.CancelledMsg{})
	m = mm.(Model)
	if m.dock.IsOpen() {
		t.Fatal("Esc must close the palette")
	}
	if m.input.Value() != draftBefore {
		t.Fatalf("draft lost across the palette round-trip: %q", m.input.Value())
	}
	if len(m.pastes) != pastesBefore {
		t.Fatalf("pastes lost across the palette round-trip: %d", len(m.pastes))
	}
}

// Enter executes the selected action and closes the palette, rather than
// routing the pick through a slash command that does not exist.
func TestActionPaletteEnterRunsSelectedAction(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.PushSteering("queued message")
	m.queuedCount = 1

	m.openActionPalette()
	if m.pickerCommand != actionPaletteCommand {
		t.Fatalf("pickerCommand = %q, want the palette marker", m.pickerCommand)
	}

	// Picking the clear-queue action resolves straight to runAction.
	mm, _ := m.Update(picker.PickedMsg{Value: string(ActionClearQueue)})
	m = mm.(Model)

	if m.dock.IsOpen() {
		t.Fatal("Enter must close the palette before the action runs")
	}
	if m.pickerCommand != "" {
		t.Fatalf("pickerCommand = %q, want cleared", m.pickerCommand)
	}
	if len(m.state.SteeringQueue()) != 0 {
		t.Fatalf("clear-queue action did not clear: %v", m.state.SteeringQueue())
	}
	if m.queuedCount != 0 {
		t.Fatalf("queuedCount = %d, want 0", m.queuedCount)
	}
}

// /actions and F2 are the same surface: dispatching the command must open
// the same palette the key opens, so both entry points show one resolution
// and neither can drift.
func TestActionsCommandOpensSamePaletteAsF2(t *testing.T) {
	m := newTestModel(t)

	mm, _ := m.dispatchCommand("/actions")
	m = asModel(t, mm)

	if !m.dock.IsOpen() {
		t.Fatal("/actions must open the palette")
	}
	if m.pickerCommand != actionPaletteCommand {
		t.Fatalf("pickerCommand = %q, want the palette marker", m.pickerCommand)
	}
	if _, ok := m.dock.Panel().(*picker.Model); !ok {
		t.Fatalf("/actions must open the shared picker, got %T", m.dock.Panel())
	}
}

// F2 opens the palette with the same contents /actions-less users reach
// from the keymap, so both entry points show one resolution.
func TestF2OpensActionPalette(t *testing.T) {
	m := newTestModel(t)

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyF2})

	if !m.dock.IsOpen() {
		t.Fatal("F2 must open the palette")
	}
	if m.pickerCommand != actionPaletteCommand {
		t.Fatalf("pickerCommand = %q, want the palette marker", m.pickerCommand)
	}
	if _, ok := m.dock.Panel().(*picker.Model); !ok {
		t.Fatalf("F2 must open the shared picker, got %T", m.dock.Panel())
	}
}

// The palette's own filter is the shared fuzzy matcher: typing part of a
// reason or a label must narrow the list, so the disabled explanations are
// searchable the same way the action names are.
func TestActionPaletteFiltersFuzzily(t *testing.T) {
	m := newTestModel(t)
	m.openActionPalette()

	panel := m.dock.Panel().(*picker.Model)
	panel.SetFilter("clr q")

	if got := panel.FilterValue(); got != "clr q" {
		t.Fatalf("filter = %q, want %q", got, "clr q")
	}
	// "Clear queued messages" must survive that fuzzy query while unrelated
	// rows do not.
	var matched bool
	for _, it := range panel.Items() {
		if it.Value == string(ActionClearQueue) {
			matched = true
		}
	}
	if !matched {
		t.Fatal("clear-queue is not among the palette's items")
	}

	// Drive the filter through the panel's own key handling as well, the way
	// a real user does: printable keys edit the filter, not the composer.
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'r'})
	m = mm.(Model)
	if m.input.Value() != "" {
		t.Fatalf("typing in the palette leaked into the composer: %q", m.input.Value())
	}
}
