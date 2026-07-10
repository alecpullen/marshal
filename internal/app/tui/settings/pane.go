package settings

import tea "charm.land/bubbletea/v2"

// sectionPane renders and edits one config section in the right pane.
type sectionPane interface {
	Init() tea.Cmd
	Update(tea.Msg) (sectionPane, tea.Cmd)
	View(width int) string
	SetWidth(int)
	// HasInnerFocus reports whether the pane has an open sub-form or
	// inline edit that Esc should close instead of closing the overlay.
	HasInnerFocus() bool
	// CloseInner discards the deepest open sub-form or inline edit.
	CloseInner()
	FocusedFieldTitle() string
}

// staticPane is a read-only placeholder pane (also used for hints).
type staticPane struct{ text string }

func (p *staticPane) Init() tea.Cmd                         { return nil }
func (p *staticPane) Update(tea.Msg) (sectionPane, tea.Cmd) { return p, nil }
func (p *staticPane) View(width int) string                 { return p.text }
func (p *staticPane) SetWidth(int)                          {}
func (p *staticPane) HasInnerFocus() bool                   { return false }
func (p *staticPane) CloseInner()                           {}
func (p *staticPane) FocusedFieldTitle() string             { return "" }
