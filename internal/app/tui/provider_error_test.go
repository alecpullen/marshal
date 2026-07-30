package tui

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/connect"
)

// The provider-error banner must clear on every recovery path, not only on
// a successful turn. A successful /connect is the banner's own suggested
// remedy, so leaving the error set afterwards is contradictory.
func TestApplyConnectDoneSuccessClearsProviderError(t *testing.T) {
	m := newTestModel(t)
	m.state.SetProviderError(errors.New("provider request timed out"))

	m.applyConnectDone(connect.DoneMsg{Provider: "openai", Model: "gpt-4o"})

	if err := m.state.ProviderError(); err != nil {
		t.Fatalf("ProviderError() = %v, want nil after successful /connect", err)
	}
}

// A successful save+reload rebuilt the provider runtime, which proves
// provider construction works with the new config — the stale error must
// be cleared.
func TestPersistAndReloadSuccessClearsProviderError(t *testing.T) {
	m := newTestModel(t)
	m.configReloader = func(config.Config) error { return nil }
	m.state.SetProviderError(errors.New("provider request timed out"))

	saveErr, reloadErr := m.persistAndReload(m.state.Config)
	if saveErr != nil || reloadErr != nil {
		t.Fatalf("persistAndReload = (%v, %v), want (nil, nil)", saveErr, reloadErr)
	}
	if err := m.state.ProviderError(); err != nil {
		t.Fatalf("ProviderError() = %v, want nil after successful reload", err)
	}
}

// A failed reload proves nothing about the provider — the error banner
// must stay so the user keeps seeing why the agent is unavailable.
func TestPersistAndReloadFailureRetainsProviderError(t *testing.T) {
	m := newTestModel(t)
	m.configReloader = func(config.Config) error { return errors.New("cleanup failed") }
	m.state.SetProviderError(errors.New("provider request timed out"))

	_, reloadErr := m.persistAndReload(m.state.Config)
	if reloadErr == nil {
		t.Fatal("persistAndReload reloadErr = nil, want the reload error")
	}
	if err := m.state.ProviderError(); err == nil {
		t.Fatal("ProviderError() = nil, want the original error retained after failed reload")
	}
}

// When the TUI started without a runner (startup provider-build failure),
// a successful config reload rebuilt one in the runtime — the model must
// adopt it, or every submission keeps saying "Agent runner is not
// available." and the session can never recover.
func TestSuccessfulReloadAdoptsMissingRunner(t *testing.T) {
	m := newTestModel(t)
	if m.runner != nil {
		t.Fatal("test precondition: newTestModel must start without a runner")
	}
	runner := &testAgentRunner{}
	m.runnerSource = func() (context.Context, AgentRunner) {
		return context.Background(), runner
	}
	m.configReloader = func(config.Config) error { return nil }

	saveErr, reloadErr := m.persistAndReload(m.state.Config)
	if saveErr != nil || reloadErr != nil {
		t.Fatalf("persistAndReload = (%v, %v), want (nil, nil)", saveErr, reloadErr)
	}
	if m.runner == nil {
		t.Fatal("runner = nil after successful reload, want the rebuilt runner adopted")
	}
}

// An existing runner must never be replaced by the source: reload mutates
// it in place via CopyFrom, and swapping pointers would orphan in-flight
// cancellation wiring.
func TestSuccessfulReloadKeepsExistingRunner(t *testing.T) {
	m := newTestModel(t)
	existing := &testAgentRunner{}
	m.runner = existing
	m.runnerSource = func() (context.Context, AgentRunner) {
		return context.Background(), &testAgentRunner{}
	}
	m.configReloader = func(config.Config) error { return nil }

	if _, reloadErr := m.persistAndReload(m.state.Config); reloadErr != nil {
		t.Fatalf("persistAndReload reloadErr = %v, want nil", reloadErr)
	}
	if m.runner != AgentRunner(existing) {
		t.Fatal("runner was replaced despite already being set")
	}
}
