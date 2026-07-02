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
