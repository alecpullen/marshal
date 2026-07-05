package settings

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
)

func TestBoolFieldToggle(t *testing.T) {
	cfg := config.Default()
	f := newBoolField("local-only", "Local only", &cfg.Privacy.RemoteProvidersAllowed, nil)
	if f.Value() != false {
		t.Fatalf("initial value = %v", f.Value())
	}
	f.Update(tea.KeyMsg{Type: tea.KeySpace})
	if f.Value() != true {
		t.Fatalf("toggled value = %v", f.Value())
	}
}

func TestIntFieldStoresValue(t *testing.T) {
	var got int
	f := newIntField("Max tool iterations", 8, func(v int) { got = v })
	if f.Value() != 8 {
		t.Fatalf("initial value = %d, want 8", f.Value())
	}
	f.Focus()
	f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if got != 82 {
		t.Fatalf("onChange value = %d, want 82", got)
	}
	if f.Value() != 82 {
		t.Fatalf("field value = %d, want 82", f.Value())
	}
}

func TestIntFieldRejectsNonDigits(t *testing.T) {
	f := newIntField("Max tool iterations", 8, nil)
	f.Focus()
	f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if f.Value() != 8 {
		t.Fatalf("value = %d after invalid input, want 8", f.Value())
	}
}
