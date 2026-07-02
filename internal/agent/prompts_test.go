package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

func TestBuildSystemPromptListsTools(t *testing.T) {
	tools := []registry.Tool{
		{Name: "file.read", Description: "Read a workspace file.", Risk: registry.RiskReadOnly},
		{Name: "shell.run", Description: "Run a shell command.", Risk: registry.RiskCommand},
	}

	msg := BuildSystemPrompt(tools)

	if msg.Role != schema.RoleSystem {
		t.Fatalf("Role = %q, want %q", msg.Role, schema.RoleSystem)
	}
	if !strings.Contains(msg.Content, "file.read") || !strings.Contains(msg.Content, "shell.run") {
		t.Fatalf("system prompt missing tool names: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, "Marshal") {
		t.Fatalf("system prompt missing agent identity: %s", msg.Content)
	}
}

func TestBuildPlanningPromptIncludesGoal(t *testing.T) {
	msg := BuildPlanningPrompt("Fix the failing parser test")
	if msg.Role != schema.RoleUser {
		t.Fatalf("Role = %q, want %q", msg.Role, schema.RoleUser)
	}
	if !strings.Contains(msg.Content, "Fix the failing parser test") {
		t.Fatalf("planning prompt missing goal: %s", msg.Content)
	}
}

func TestBuildToolResultMessageIncludesSummaryAndContent(t *testing.T) {
	result := registry.ToolResult{Summary: "read 10 lines", Content: "package main"}
	msg := BuildToolResultMessage("file.read", result)

	if !strings.Contains(msg.Content, "file.read") {
		t.Fatalf("missing tool name: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, "read 10 lines") {
		t.Fatalf("missing summary: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, "package main") {
		t.Fatalf("missing content: %s", msg.Content)
	}
}

func TestBuildToolErrorMessageIncludesReason(t *testing.T) {
	msg := BuildToolErrorMessage("shell.run", "denied by policy: blocked command")
	if !strings.Contains(msg.Content, "shell.run") || !strings.Contains(msg.Content, "denied by policy") {
		t.Fatalf("tool error message = %q", msg.Content)
	}
}

func TestBuildCorrectionMessageIncludesErrorText(t *testing.T) {
	msg := BuildCorrectionMessage(errors.New("no JSON action object found"))
	if !strings.Contains(msg.Content, "no JSON action object found") {
		t.Fatalf("correction message = %q", msg.Content)
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(msg.Content), &decoded) == nil {
		t.Fatal("correction message should be plain instructive text, not JSON")
	}
}
