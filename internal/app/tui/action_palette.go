package tui

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/picker"
)

// actionPaletteCommand is the pickerCommand value that marks the docked panel
// as the action palette. It matches the /actions command name so the two entry
// points share one surface, but the pick path never round-trips through the
// dispatch table: PickedMsg routes straight to runAction, so the palette can
// never recurse through /actions.
const actionPaletteCommand = "actions"

// actionPaletteItems renders the resolved action list as picker rows. Every
// action is listed, including the ones the current context cannot run: an
// unavailable action's row explains why instead of vanishing, which is how a
// user discovers that the operation exists but needs a different state.
//
// The picker's own fuzzy filter matches the group, label, and detail, so
// typing part of a reason ("queued") finds the action just as well as typing
// its name.
func actionPaletteItems(actions []Action) []picker.Item {
	items := make([]picker.Item, 0, len(actions))
	for _, a := range actions {
		badge := ""
		if a.KeyHint != "" {
			badge = a.KeyHint
		}
		if a.Disabled {
			// The badge is the only cell a picker row renders as an
			// unmissable tag, and "unavailable" is exactly the distinction
			// the list must not blur.
			badge = "unavailable"
		}
		items = append(items, picker.Item{
			Group:  actionGroup(a),
			Label:  a.Label,
			Detail: a.Detail(),
			Badge:  badge,
			Value:  string(a.ID),
		})
	}
	return items
}

// actionGroup buckets the palette listing so the urgent operations sort at
// the top. A user reaching for the palette is usually mid-run and looking
// for a control, not browsing.
func actionGroup(a Action) string {
	switch a.Priority {
	case actionPriorityEssential:
		return "Now"
	case actionPriorityLikely:
		return "While running"
	default:
		return "Panels and modes"
	}
}

// openActionPalette opens the palette docked above the composer.
//
// The palette is a dock panel rather than its own overlay, so opening it does
// not touch the composer model, its cursor, or its condensed paste
// attachments: the draft is exactly what it was when the palette closes, and
// Esc returns the user to it.
func (m *Model) openActionPalette() {
	ctx := m.actionSnapshot()
	m.openPicker(actionPaletteCommand, "Actions", "Enter runs · Esc returns to the prompt",
		actionPaletteItems(resolveActions(ctx)), "")
	m.refreshViewport()
}

// runPaletteAction executes the action the palette selected, closing the
// palette first so a panel-opening action replaces it rather than stacking
// behind it.
func (m *Model) runPaletteAction(id ActionID) (tea.Model, tea.Cmd) {
	m.dock.CloseNow()
	m.pickerCommand = ""
	return m.runAction(id)
}
