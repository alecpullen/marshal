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

func TestStatePendingApprovalAndSessionRules(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))

	// Initially nil
	if got := state.PendingApproval(); got != nil {
		t.Fatalf("PendingApproval() = %v, want nil", got)
	}

	tc := &PendingToolCall{
		ID:           "123",
		Name:         "shell.run",
		Args:         `{"command": "go test"}`,
		Command:      "go test",
		Risk:         "command",
		Reason:       "test verification",
		ResponseChan: make(chan UserApprovalDecision, 1),
	}

	state.SetPendingApproval(tc)
	gotTc := state.PendingApproval()
	if gotTc == nil || gotTc.ID != "123" || gotTc.Command != "go test" {
		t.Fatalf("PendingApproval() = %#v, want %#v", gotTc, tc)
	}

	// Add session rule
	state.AddSessionRule("go test")
	rules := state.SessionRules()
	if len(rules) != 1 || rules[0] != "go test" {
		t.Fatalf("SessionRules() = %v, want ['go test']", rules)
	}
}

