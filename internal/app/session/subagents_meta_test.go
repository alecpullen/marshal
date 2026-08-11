package session

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestRegisterSubagentWithMeta(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	meta := SubagentMeta{
		Role:     routing.RoleImplementer,
		Provider: "ollama",
		Model:    "qwen2.5-coder",
		Fallback: true,
	}
	v := state.RegisterSubagentWithMeta("task 1", child, meta)
	if v.Role != routing.RoleImplementer {
		t.Errorf("Role = %q, want %q", v.Role, routing.RoleImplementer)
	}
	if v.Model != "qwen2.5-coder" {
		t.Errorf("Model = %q, want qwen2.5-coder", v.Model)
	}
	if !v.Fallback {
		t.Error("Fallback = false, want true")
	}
}

func TestAddSubagentTokensUpdatesView(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	state.AddSubagentTokens(v.ID, 150)
	got, ok := state.Subagent(v.ID)
	if !ok {
		t.Fatal("subagent not found")
	}
	if got.TokensUsed != 150 {
		t.Fatalf("TokensUsed = %d, want 150", got.TokensUsed)
	}
	state.AddSubagentTokens(v.ID, 75)
	got, _ = state.Subagent(v.ID)
	if got.TokensUsed != 225 {
		t.Fatalf("TokensUsed = %d, want 225", got.TokensUsed)
	}
}

func TestRegisterSubagentDefaultsMeta(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	if v.Fallback {
		t.Error("Fallback should default to false")
	}
}

func TestCancelSubagentInvokesStoredFunc(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)

	called := false
	state.SetSubagentCancel(v.ID, func() { called = true })
	if !state.CancelSubagent(v.ID) {
		t.Fatal("CancelSubagent returned false for running subagent with cancel")
	}
	if !called {
		t.Fatal("cancel function was not called")
	}
}

func TestCancelSubagentReturnsFalseForFinished(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	state.FinishSubagent(v.ID, "done", nil)

	called := false
	state.SetSubagentCancel(v.ID, func() { called = true })
	if state.CancelSubagent(v.ID) {
		t.Fatal("CancelSubagent returned true for finished subagent")
	}
	if called {
		t.Fatal("cancel function should not have been called for finished subagent")
	}
}

func TestCancelSubagentReturnsFalseForUnknown(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	if state.CancelSubagent(999) {
		t.Fatal("CancelSubagent returned true for unknown ID")
	}
}
