package mcpauth

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Display must satisfy the oauth.Display contract shape used by
// oauth.Engine.Authorize: a one-shot ShowURL and a ctx-aware Wait.
func TestDisplayShowURLRecordsURL(t *testing.T) {
	d := NewDisplay()
	if got := d.URL(); got != "" {
		t.Fatalf("URL() before ShowURL = %q, want empty", got)
	}
	d.ShowURL("https://auth.example.com/authorize?x=1")
	if got := d.URL(); got != "https://auth.example.com/authorize?x=1" {
		t.Errorf("URL() = %q, want the shown url", got)
	}
}

func TestDisplayWaitReturnsOnCancel(t *testing.T) {
	d := NewDisplay()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Wait(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Wait returned %v before cancellation", err)
	case <-time.After(20 * time.Millisecond):
		// still waiting, as expected
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}

// Panel must satisfy the dock.Panel contract and claim its own tick messages
// so the dock routes them without the model needing to name them.
func TestPanelOwnsTickMsg(t *testing.T) {
	p := NewPanel("github", NewDisplay())
	if !p.OwnsMsg(TickMsg{}) {
		t.Error("OwnsMsg(TickMsg) = false, want true")
	}
	if p.OwnsMsg(AuthDoneMsg{}) {
		t.Error("OwnsMsg(AuthDoneMsg) = true; completion must reach the model, not the panel")
	}
}

func TestPanelEscCancels(t *testing.T) {
	p := NewPanel("github", NewDisplay())
	cancelled := false
	p.SetCancel(func() { cancelled = true })
	p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled {
		t.Error("Esc did not invoke the cancel func")
	}
}
