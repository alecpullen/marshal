package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestSDDGatePromptRendersTaskAndQuestion(t *testing.T) {
	g := session.SDDGate{TaskN: 3, Question: "user level or system level?"}
	prompt := sddGatePrompt(g)
	if !strings.Contains(prompt, "Task 3") || !strings.Contains(prompt, "user level") {
		t.Errorf("gate prompt missing task/question: %q", prompt)
	}
	if !strings.Contains(prompt, "Type your answer") {
		t.Errorf("gate prompt missing answer instruction: %q", prompt)
	}
}

func TestSDDGatePromptRendersEscalation(t *testing.T) {
	g := session.SDDGate{TaskN: 2, Question: "reviewer FAILED 3x"}
	prompt := sddGatePrompt(g)
	if !strings.Contains(prompt, "Task 2") || !strings.Contains(prompt, "FAILED") {
		t.Errorf("gate prompt missing task/question: %q", prompt)
	}
}
