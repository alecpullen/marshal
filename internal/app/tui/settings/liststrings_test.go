package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func lsKey(l *listStrings, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		l.Update(msg)
	}
}

func TestListStringsAppend(t *testing.T) {
	items := []string{"one"}
	l := newListStrings("Ignore patterns", &items)
	l.Focus(true)
	lsKey(l, "a", "t", "w", "o", "enter")
	if len(items) != 2 || items[1] != "two" {
		t.Fatalf("items = %v, want [one two]", items)
	}
}

func TestListStringsEditInline(t *testing.T) {
	items := []string{"one", "two"}
	l := newListStrings("x", &items)
	l.Focus(true)
	lsKey(l, "down", "enter")
	if !l.Editing() {
		t.Fatal("enter should open inline edit")
	}
	lsKey(l, "!", "enter")
	if items[1] != "two!" {
		t.Fatalf("items[1] = %q, want two!", items[1])
	}
}

func TestListStringsEscCancelsEdit(t *testing.T) {
	items := []string{"one"}
	l := newListStrings("x", &items)
	l.Focus(true)
	lsKey(l, "enter", "z")
	l.CancelEdit()
	if l.Editing() || items[0] != "one" {
		t.Fatalf("cancel must discard the edit, items=%v", items)
	}
}

func TestListStringsDelete(t *testing.T) {
	items := []string{"one", "two"}
	l := newListStrings("x", &items)
	l.Focus(true)
	lsKey(l, "d")
	if len(items) != 1 || items[0] != "two" {
		t.Fatalf("items = %v, want [two]", items)
	}
}
