package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/contextpack"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

func dummyTools() []registry.Tool {
	return []registry.Tool{
		{Name: "file.read", Risk: registry.RiskReadOnly, Description: "Read a file."},
		{Name: "shell.run", Risk: registry.RiskCommand, Description: "Run a shell command."},
	}
}

func TestBuildSystemPromptListsTools(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, []registry.Tool{
		{Name: "file.read", Description: "Read a workspace file.", Risk: registry.RiskReadOnly},
		{Name: "shell.run", Description: "Run a shell command.", Risk: registry.RiskCommand},
	})

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

func TestBuildSystemPromptContainsBaseSections(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools())
	content := msg.Content

	if msg.Role != schema.RoleSystem {
		t.Fatalf("Role = %q, want %q", msg.Role, schema.RoleSystem)
	}

	for _, want := range []string{
		"You are Marshal, a local-first coding assistant",
		"You receive a context pack with each turn",
		"Prefer small, verifiable changes",
		"Respond with exactly one JSON object",
		"Available tools:",
		"file.read",
		"shell.run",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing expected section %q\n%s", want, content)
		}
	}
}

func TestBuildSystemPromptPlannerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RolePlanner, dummyTools())
	content := msg.Content

	if !strings.Contains(content, "You are a planner") {
		t.Error("planner role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: answer, final") {
		t.Errorf("planner allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptImplementerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, dummyTools())
	content := msg.Content

	if !strings.Contains(content, "You are an implementer") {
		t.Error("implementer role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("implementer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptTesterHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleTester, dummyTools())
	content := msg.Content

	if !strings.Contains(content, "You are a tester") {
		t.Error("tester role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("tester allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptReviewerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleReviewer, dummyTools())
	content := msg.Content

	if !strings.Contains(content, "You are a reviewer") {
		t.Error("reviewer role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("reviewer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptUnknownRoleFallsBackToGeneral(t *testing.T) {
	msg := BuildSystemPrompt(AgentRole("nonexistent"), dummyTools())
	content := msg.Content

	if !strings.Contains(content, "You are the general agent") {
		t.Error("unknown role should fall back to general agent addendum")
	}
	if !strings.Contains(content, "Allowed actions for this role: answer, tool_call, final") {
		t.Error("unknown role should fall back to general agent allowed actions")
	}
}

func TestBuildSystemPromptEachRoleHasAllowedActions(t *testing.T) {
	expected := map[AgentRole]string{
		RoleGeneral:     "Allowed actions for this role: answer, tool_call, final",
		RolePlanner:     "Allowed actions for this role: answer, final",
		RoleImplementer: "Allowed actions for this role: tool_call, final",
		RoleTester:      "Allowed actions for this role: tool_call, final",
		RoleReviewer:    "Allowed actions for this role: tool_call, final",
	}

	for role, want := range expected {
		msg := BuildSystemPrompt(role, dummyTools())
		content := msg.Content

		if !strings.Contains(content, want) {
			t.Errorf("role %q missing allowed actions line %q\n%s", role, want, content)
		}
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

func TestBuildContextPackMessageReturnsFalseForEmptyPack(t *testing.T) {
	msg, ok := BuildContextPackMessage(contextpack.Pack{})
	if ok {
		t.Fatalf("ok = true, want false")
	}
	if msg.Content != "" {
		t.Fatalf("msg.Content = %q, want empty", msg.Content)
	}
}

func TestBuildContextPackMessageRendersPack(t *testing.T) {
	msg, ok := BuildContextPackMessage(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if msg.Role != schema.RoleUser {
		t.Fatalf("Role = %q, want %q", msg.Role, schema.RoleUser)
	}
	if !strings.Contains(msg.Content, "Project context pack:") || !strings.Contains(msg.Content, "Project: marshal") {
		t.Fatalf("context message missing rendered pack:\n%s", msg.Content)
	}
}
