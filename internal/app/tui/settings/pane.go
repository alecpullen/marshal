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

// firstFocuser is an optional interface implemented by panes that need to
// participate in "return to sidebar" decisions. When the pane is not at its
// first internal focus, sidebar-return keys (shift+tab, h, left) are
// forwarded into the pane instead of stealing focus back to the sidebar.
type firstFocuser interface{ AtFirstFocus() bool }
