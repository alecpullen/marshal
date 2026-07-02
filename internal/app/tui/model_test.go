package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func TestEnterAppendsInputAndClearsPrompt(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].Role != session.RoleUser || messages[0].Content != "hello" {
		t.Fatalf("message = %#v", messages[0])
	}
	if model.input.Value() != "" {
		t.Fatalf("input = %q, want empty", model.input.Value())
	}
}

func TestEnterOnWhitespaceDoesNotAppendMessage(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if got := len(state.Messages()); got != 0 {
		t.Fatalf("len(messages) = %d, want 0", got)
	}
}

func TestQuitKeyRequestsShutdown(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("quit command is nil")
	}

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not shut down")
	}
}

func TestViewContainsExpectedPanels(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Marshal",
		"Status",
		"Transcript",
		"Streaming Output",
		"Command Palette",
		"Tool Log",
		"Diff",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestViewShowsProviderErrorWhenSet(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	state.SetProviderError(errors.New("dial tcp: connection refused"))
	view := model.View()

	if !strings.Contains(view, "Provider Error") {
		t.Fatalf("View() missing 'Provider Error' substring:\n%s", view)
	}
	if !strings.Contains(view, "connection refused") {
		t.Fatalf("View() missing 'connection refused' substring:\n%s", view)
	}
}

func TestViewOmitsProviderErrorSectionByDefault(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	model := New(state)

	view := model.View()

	if strings.Contains(view, "Provider Error") {
		t.Fatalf("View() should not contain 'Provider Error' when no error is set:\n%s", view)
	}
}

func TestTUIApprovalBannerAndKeypresses(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	
	respChan := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "456",
		Name:         "shell.run",
		Args:         `{"command":"go test"}`,
		Command:      "go test",
		Risk:         "command",
		Reason:       "run tests",
		ResponseChan: respChan,
	}
	state.SetPendingApproval(tc)

	model := New(state)

	// Check rendering of banner
	view := model.View()
	if !strings.Contains(view, "SECURITY APPROVAL REQUIRED") {
		t.Fatal("View() missing SECURITY APPROVAL REQUIRED banner")
	}
	if !strings.Contains(view, "go test") {
		t.Fatal("View() missing proposed command")
	}

	// 1. Test Deny Keypress 'd'
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	model = updated.(Model)
	
	select {
	case dec := <-respChan:
		if dec.Approved {
			t.Fatal("expected decision to be denied")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for deny response")
	}
	if state.PendingApproval() != nil {
		t.Fatal("expected pending approval to be cleared")
	}

	// Set up again for Enter key
	state.SetPendingApproval(tc)
	model = New(state)

	// 2. Test Approve Keypress 'enter'
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	
	select {
	case dec := <-respChan:
		if !dec.Approved || dec.Edited != "" {
			t.Fatal("expected decision to be approved and not edited")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for approve response")
	}

	// Set up again for Edit key
	state.SetPendingApproval(tc)
	model = New(state)

	// 3. Test Edit Keypress 'e'
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	model = updated.(Model)

	if !model.editingCommand {
		t.Fatal("expected model to enter editingCommand mode")
	}
	if model.input.Value() != "go test" {
		t.Fatalf("expected input value to be 'go test', got %q", model.input.Value())
	}

	// Simulate typing to edit command
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" -v")})
	model = updated.(Model)
	if model.input.Value() != "go test -v" {
		t.Fatalf("expected edited input value to be 'go test -v', got %q", model.input.Value())
	}

	// Press Enter to confirm edited command
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	select {
	case dec := <-respChan:
		if !dec.Approved || dec.Edited != "go test -v" {
			t.Fatalf("expected decision to be approved with edited command, got: %#v", dec)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for edited response")
	}
}

