package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/contextpack"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
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
	}, nil, nil)

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
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil)
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
	msg := BuildSystemPrompt(RolePlanner, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, "You are a planner") {
		t.Error("planner role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: answer, final") {
		t.Errorf("planner allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptImplementerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, "You are an implementer") {
		t.Error("implementer role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, patch, final") {
		t.Errorf("implementer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptDescribesPatchFormat(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, "<<<<<<< SEARCH") {
		t.Error("system prompt missing search/replace patch marker <<<<<<< SEARCH")
	}
	if !strings.Contains(content, ">>>>>>> REPLACE") {
		t.Error("system prompt missing search/replace patch marker >>>>>>> REPLACE")
	}
}

func TestBuildSystemPromptContainsActionExamples(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil)
	content := msg.Content

	for _, want := range []string{
		`"type": "answer"`,
		`"type": "tool_call"`,
		`"type": "patch"`,
		`"type": "final"`,
		"<<<<<<< SEARCH",
		">>>>>>> REPLACE",
		"Do not use unified diff syntax",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("system prompt missing expected content %q\n%s", want, content)
		}
	}
}

func TestBuildSystemPromptImplementerIncludesPatchExample(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, `"type": "patch"`) {
		t.Error("implementer role example missing patch action")
	}
	if !strings.Contains(content, "File:") {
		t.Error("implementer role patch example missing File: header")
	}
}

func TestBuildSystemPromptTesterHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleTester, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, "You are a tester") {
		t.Error("tester role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("tester allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptReviewerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleReviewer, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, "You are a reviewer") {
		t.Error("reviewer role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("reviewer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptUnknownRoleFallsBackToGeneral(t *testing.T) {
	msg := BuildSystemPrompt(AgentRole("nonexistent"), dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, "You are the general agent") {
		t.Error("unknown role should fall back to general agent addendum")
	}
	if !strings.Contains(content, "Allowed actions for this role: answer, tool_call, patch, final") {
		t.Error("unknown role should fall back to general agent allowed actions")
	}
}

func TestBuildSystemPromptEachRoleHasAllowedActions(t *testing.T) {
	expected := map[AgentRole]string{
		RoleGeneral:     "Allowed actions for this role: answer, tool_call, patch, final",
		RolePlanner:     "Allowed actions for this role: answer, final",
		RoleImplementer: "Allowed actions for this role: tool_call, patch, final",
		RoleTester:      "Allowed actions for this role: tool_call, final",
		RoleReviewer:    "Allowed actions for this role: tool_call, final",
	}

	for role, want := range expected {
		msg := BuildSystemPrompt(role, dummyTools(), nil, nil)
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

func TestBuildSystemPromptDescribesParallelActionsArray(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil)
	content := msg.Content

	if !strings.Contains(content, `"actions"`) {
		t.Error("system prompt missing parallel actions array description")
	}
	if !strings.Contains(content, "parallel read-only work") {
		t.Error("system prompt missing parallel read-only guidance")
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

func TestBuildSystemPromptIncludesAvailableSkills(t *testing.T) {
	idx := skills.NewIndex()
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil)
	content := msg.Content

	if !strings.Contains(content, "Available Skills") {
		t.Fatal("system prompt should contain 'Available Skills' section placeholder")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging workflow"})
	idx.Set("deploy", skills.Skill{Name: "deploy", Description: "Deployment workflows"})

	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil)
	content := msg.Content

	if !strings.Contains(content, "`debug`") {
		t.Fatal("system prompt should list debug skill")
	}
	if !strings.Contains(content, "`deploy`") {
		t.Fatal("system prompt should list deploy skill")
	}
	if !strings.Contains(content, "Debugging workflow") {
		t.Fatal("system prompt should include skill descriptions")
	}
	if !strings.Contains(content, "skill.load") {
		t.Fatal("system prompt should mention skill.load")
	}
}

func TestBuildSystemPromptWithActiveSkills(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging workflow"})

	active := []string{"debug"}
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, active)
	content := msg.Content

	if !strings.Contains(content, "Active Skills") {
		t.Fatal("system prompt should show 'Active Skills' when skills are loaded")
	}
	if !strings.Contains(content, "`debug`") {
		t.Fatal("system prompt should list active skill name")
	}
	if strings.Contains(content, "skill.load") {
		t.Fatal("system prompt should NOT mention skill.load when skills are active")
	}
}

func TestBuildSystemPromptNoSkills(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, nil, nil, nil)
	content := msg.Content

	if !strings.Contains(content, "No skills are available") {
		t.Fatal("system prompt should note no skills when index is nil")
	}
}

func TestBuildSystemPromptEmptySkillIndex(t *testing.T) {
	idx := skills.NewIndex()
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil)
	content := msg.Content

	if !strings.Contains(content, "No skills are available") {
		t.Fatal("system prompt should note no skills when index is empty")
	}
}

func TestBuildSystemPromptRepoScoutRole(t *testing.T) {
	msg := BuildSystemPrompt(RoleRepoScout, nil, nil, nil)
	if !strings.Contains(msg.Content, "repo scout") {
		t.Fatalf("repo scout system prompt missing role focus:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "tool_call, final") {
		t.Fatalf("repo scout system prompt missing allowed actions:\n%s", msg.Content)
	}
}
