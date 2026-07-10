package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// meKeyTarget is the small interface meKey needs from a map editor: only
// the ability to accept a tea.Msg (which a KeyPressMsg is). Both
// *mapEditor and *mapIntEditor satisfy it.
type meKeyTarget interface {
	Update(tea.Msg) tea.Cmd
}

// meKey drives a map editor with a sequence of named keys. It mirrors the
// keyMsg helper used at the Model level but works directly against a
// mapEditor / mapIntEditor for unit testing the widget in isolation.
func meKey(m meKeyTarget, keys ...string) {
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
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		m.Update(msg)
	}
}

func TestMapEditorAdd(t *testing.T) {
	v := map[string]string{}
	m := newMapEditor("Commands", &v)
	m.Focus(true)
	meKey(m, "a", "g", "o", "enter", "g", "o", " ", "v", "e", "t", "enter")
	if v["go"] != "go vet" {
		t.Fatalf("map = %v, want go->go vet", v)
	}
}

func TestMapEditorEditValue(t *testing.T) {
	v := map[string]string{"go": "go vet ./..."}
	m := newMapEditor("Commands", &v)
	m.Focus(true)
	meKey(m, "enter", "g", "o", " ", "b", "u", "i", "l", "d", "enter")
	if v["go"] != "go build" {
		t.Fatalf("value = %q, want go build", v["go"])
	}
}

func TestMapEditorDelete(t *testing.T) {
	v := map[string]string{"go": "x", "py": "y"}
	m := newMapEditor("Commands", &v)
	m.Focus(true)
	meKey(m, "d")
	if _, ok := v["go"]; ok {
		t.Fatal("d should delete the focused row")
	}
}

func TestMapIntEditorAdd(t *testing.T) {
	v := map[string]int{}
	m := newMapIntEditor("Tool iters", &v)
	m.Focus(true)
	meKey(m, "a", "t", "e", "s", "t", "e", "r", "enter", "9", "enter")
	if v["tester"] != 9 {
		t.Fatalf("map = %v, want tester->9", v)
	}
}
