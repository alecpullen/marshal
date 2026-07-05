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

func TestIntFieldWithBoundsClampsValue(t *testing.T) {
	var got int
	f := newIntFieldWithBounds("Max tool iterations", 8, func(v int) { got = v }, 1, 100)
	f.Focus()
	// Typing '2' makes 82, which exceeds max 100? No — 82 < 100. Use max 50.
	f = newIntFieldWithBounds("Max tool iterations", 8, func(v int) { got = v }, 1, 50)
	f.Focus()
	f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if f.Value() != 50 {
		t.Fatalf("value = %d, want 50 (clamped to max)", f.Value())
	}
	if got != 50 {
		t.Fatalf("onChange value = %d, want 50", got)
	}
}
