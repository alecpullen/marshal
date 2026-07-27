package textfield

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestUpdateHandlesPasteAndKeys(t *testing.T) {
	f := New()
	f.Focus()

	var cmd tea.Cmd
	f, cmd = f.Update(tea.PasteMsg{Content: "hello"})
	_ = cmd
	if got := f.Value(); got != "hello" {
		t.Fatalf("after paste, Value() = %q, want %q", got, "hello")
	}

	f, _ = f.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
	if got := f.Value(); got != "hello!" {
		t.Fatalf("after key, Value() = %q, want %q", got, "hello!")
	}
}

type tickMsg struct{}

func TestUpdateIgnoresUnknownMessages(t *testing.T) {
	f := New()
	f.Focus()
	f.SetValue("keep")
	f, _ = f.Update(tickMsg{})
	if got := f.Value(); got != "keep" {
		t.Fatalf("unknown message changed value: %q", got)
	}
}
