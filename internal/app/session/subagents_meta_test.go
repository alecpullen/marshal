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

func TestRegisterSubagentDefaultsMeta(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	v := state.RegisterSubagent("task 1", child)
	if v.Fallback {
		t.Error("Fallback should default to false")
	}
}
