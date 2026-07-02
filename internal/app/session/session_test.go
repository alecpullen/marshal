package session

import (
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
)

func TestStateAppendsMessagesInOrder(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))

	state.AddMessage(RoleSystem, "ready")
	state.AddMessage(RoleUser, "hello")

	messages := state.Messages()
	if len(messages) != 2 {
		t.Fatalf("len(Messages()) = %d, want 2", len(messages))
	}
	if messages[0].Role != RoleSystem || messages[0].Content != "ready" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != RoleUser || messages[1].Content != "hello" {
		t.Fatalf("second message = %#v", messages[1])
	}
}

func TestMessagesReturnsCopy(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))
	state.AddMessage(RoleUser, "hello")

	messages := state.Messages()
	messages[0].Content = "mutated"

	got := state.Messages()[0].Content
	if got != "hello" {
		t.Fatalf("stored message = %q, want hello", got)
	}
}

func TestShutdownCancelsState(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))
	state.Shutdown()

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not cancelled")
	}
}

func TestSetProviderErrorStoresAndRetrieves(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))

	testErr := errors.New("provider connection failed")
	state.SetProviderError(testErr)

	got := state.ProviderError()
	if !errors.Is(got, testErr) {
		t.Fatalf("ProviderError() = %v, want %v", got, testErr)
	}
}

func TestSetProviderErrorNilClearsExistingError(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))

	testErr := errors.New("provider connection failed")
	state.SetProviderError(testErr)

	state.SetProviderError(nil)

	got := state.ProviderError()
	if got != nil {
		t.Fatalf("ProviderError() = %v, want nil", got)
	}
}
