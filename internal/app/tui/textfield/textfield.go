// Package textfield wraps bubbles' textinput so that OS paste (tea.PasteMsg)
// works by default in every TUI text field. Handling paste becomes the
// default rather than something each component must remember to opt into.
package textfield

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
)

// Model embeds textinput.Model. Styling, masking, cursor, and blink behavior
// are all passthrough; the only difference is that Update accepts any
// tea.Msg, so a component routes both key presses and paste through a single
// call instead of writing a separate PasteMsg case.
type Model struct {
	textinput.Model
}

// New returns a new text field, equivalent to textinput.New().
func New() Model {
	return Model{Model: textinput.New()}
}

// Update forwards every message to the embedded text input. bubbles handles
// tea.PasteMsg and tea.KeyPressMsg natively; anything else (blink ticks,
// unknown messages) is passed through untouched so cursor behavior is
// unchanged.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	inner, cmd := m.Model.Update(msg)
	m.Model = inner
	return m, cmd
}
