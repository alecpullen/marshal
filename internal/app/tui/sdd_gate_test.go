package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestSDDGatePromptRendersSpecApproval(t *testing.T) {
	g := session.SDDGate{Kind: "spec_approval", Reason: "spec.md ready for review at .marshal/sdd/spec.md"}
	prompt := sddGatePrompt(g)
	if !strings.Contains(prompt, "spec.md") || !strings.Contains(prompt, "approve") {
		t.Errorf("spec gate prompt missing path/approve: %q", prompt)
	}
}

func TestSDDGatePromptRendersEscalation(t *testing.T) {
	g := session.SDDGate{Kind: "escalation", TaskID: "T2", Reason: "reviewer FAILED 3x"}
	prompt := sddGatePrompt(g)
	if !strings.Contains(prompt, "T2") || !strings.Contains(prompt, "Escalation") {
		t.Errorf("escalation gate prompt: %q", prompt)
	}
}
