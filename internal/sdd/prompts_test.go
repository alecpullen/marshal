package sdd

import (
	"strings"
	"testing"
)

func TestOrchestratorSystemPromptContainsRules(t *testing.T) {
	p := OrchestratorSystemPrompt()
	for _, want := range []string{
		"drain iteration",
		"agent.run",
		"parallel",
		"do not edit source",
		"status:",
		"dispatched:",
		"merged:",
		"blocked_tasks:",
		"next_action:",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestOrchestratorSystemPromptUnderTokenBudget(t *testing.T) {
	p := OrchestratorSystemPrompt()
	// ~2-3K tokens; rough char estimate (4 chars/token). Must be under 16K chars.
	if len(p) > 16000 {
		t.Errorf("system prompt is %d chars, want < 16000 (~4K tokens)", len(p))
	}
}

func TestBuildDispatchPromptReferencesPaths(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	ws.Ensure()
	dumpPath := ws.Dir() + "/state-dump.json"
	p := BuildDispatchPrompt(ws, dumpPath)
	for _, want := range []string{
		"state-dump.json",
		"spec.md",
		"dag.json",
		"one drain iteration",
		"structured report",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("dispatch prompt missing %q", want)
		}
	}
}
