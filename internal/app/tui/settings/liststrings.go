package settings

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// listStrings is an inline-editable list of strings bound to a slice in the
// working config. Commits mutate *items directly.
type listStrings struct {
	title   string
	items   *[]string
	cursor  int
	adding  bool
	editing bool
	input   textinput.Model
	focused bool
}

func newListStrings(title string, items *[]string) *listStrings {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	return &listStrings{title: title, items: items, input: ti}
}

func (l *listStrings) Focus(on bool) { l.focused = on }
func (l *listStrings) Focused() bool { return l.focused }
func (l *listStrings) Editing() bool { return l.adding || l.editing }

func (l *listStrings) CancelEdit() {
	l.adding = false
	l.editing = false
	l.input.Blur()
}

func (l *listStrings) clampCursor() {
	if l.cursor >= len(*l.items) {
		l.cursor = len(*l.items) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
}

func (l *listStrings) Label() string                       { return l.title }
func (l *listStrings) updateKey(k tea.KeyPressMsg) tea.Cmd { return l.Update(k) }

func (l *listStrings) Update(msg tea.KeyPressMsg) tea.Cmd {
	if l.Editing() {
		switch msg.String() {
		case "enter":
			val := strings.TrimSpace(l.input.Value())
			if val != "" {
				if l.adding {
					*l.items = append(*l.items, val)
					l.cursor = len(*l.items) - 1
				} else {
					(*l.items)[l.cursor] = val
				}
			}
			l.CancelEdit()
			return nil
		case "esc":
			l.CancelEdit()
			return nil
		}
		var cmd tea.Cmd
		l.input, cmd = l.input.Update(msg)
		return cmd
	}

	// Not editing: only consume keys when this list is the focused widget
	// inside its parent. Otherwise forward the key to siblings.
	if !l.focused {
		return nil
	}

	switch msg.String() {
	case "up", "k":
		l.cursor--
		l.clampCursor()
	case "down", "j":
		l.cursor++
		l.clampCursor()
	case "a":
		l.adding = true
		l.input.SetValue("")
		l.input.Focus()
	case "enter", "e":
		if len(*l.items) > 0 {
			l.editing = true
			l.input.SetValue((*l.items)[l.cursor])
			l.input.CursorEnd()
			l.input.Focus()
		}
	case "d":
		if len(*l.items) > 0 {
			*l.items = append((*l.items)[:l.cursor], (*l.items)[l.cursor+1:]...)
			l.clampCursor()
		}
	}
	return nil
}

func (l *listStrings) View(width int) string {
	var b strings.Builder
	b.WriteString(l.title + "\n")
	if len(*l.items) == 0 && !l.adding {
		b.WriteString("  (empty — press a to add)\n")
	}
	for i, item := range *l.items {
		marker := "  "
		if l.focused && i == l.cursor {
			marker = "▸ "
		}
		if l.editing && i == l.cursor {
			b.WriteString(marker + l.input.View() + "\n")
			continue
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, item))
	}
	if l.adding {
		b.WriteString("▸ " + l.input.View() + "\n")
	}
	if l.focused && !l.Editing() {
		b.WriteString("  a add · e edit · d delete\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
