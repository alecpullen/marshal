package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/contextpack"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
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
	}, nil, nil, false)

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
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
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
	msg := BuildSystemPrompt(RolePlanner, dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "You are a planner") {
		t.Error("planner role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: answer, final") {
		t.Errorf("planner allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptImplementerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "You are an implementer") {
		t.Error("implementer role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, patch, final") {
		t.Errorf("implementer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptDescribesPatchFormat(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "<<<<<<< SEARCH") {
		t.Error("system prompt missing search/replace patch marker <<<<<<< SEARCH")
	}
	if !strings.Contains(content, ">>>>>>> REPLACE") {
		t.Error("system prompt missing search/replace patch marker >>>>>>> REPLACE")
	}
}

func TestBuildSystemPromptContainsActionExamples(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
	content := msg.Content

	for _, want := range []string{
		`"type": "answer"`,
		`"type": "tool_call"`,
		`"type": "patch"`,
		`"type": "final"`,
		`"type": "ask_user"`,
		"<<<<<<< SEARCH",
		">>>>>>> REPLACE",
		"Do not use unified diff syntax",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("system prompt missing expected content %q\n%s", want, content)
		}
	}
}

func TestBuildSystemPromptNativeModeOmitsJSONEnvelopeScaffolding(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, dummyTools(), nil, nil, true)
	content := msg.Content

	for _, want := range []string{
		"You are Marshal, a local-first coding assistant",
		"Prefer small, verifiable changes",
		"You are an implementer",
		"Available tools:",
		"file.read",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("native prompt missing expected content %q\n%s", want, content)
		}
	}

	for _, unwanted := range []string{
		"Respond with exactly one JSON object",
		`"rationale"`,
		`"action"`,
		`"actions"`,
		"<<<<<<< SEARCH",
		">>>>>>> REPLACE",
		"Do not use unified diff syntax",
		"Allowed actions for this role: tool_call, patch, final",
	} {
		if strings.Contains(content, unwanted) {
			t.Errorf("native prompt contains JSON-envelope scaffolding %q\n%s", unwanted, content)
		}
	}

	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("native implementer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptImplementerIncludesPatchExample(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, `"type": "patch"`) {
		t.Error("implementer role example missing patch action")
	}
	if !strings.Contains(content, "File:") {
		t.Error("implementer role patch example missing File: header")
	}
}

func TestBuildSystemPromptTesterHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleTester, dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "You are a tester") {
		t.Error("tester role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("tester allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptReviewerHasCorrectAllowedActions(t *testing.T) {
	msg := BuildSystemPrompt(RoleReviewer, dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "You are a reviewer") {
		t.Error("reviewer role focus missing")
	}
	if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
		t.Errorf("reviewer allowed actions incorrect; got:\n%s", content)
	}
}

func TestBuildSystemPromptUnknownRoleFallsBackToGeneral(t *testing.T) {
	msg := BuildSystemPrompt(AgentRole("nonexistent"), dummyTools(), nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "You are the general agent") {
		t.Error("unknown role should fall back to general agent addendum")
	}
	if !strings.Contains(content, "Allowed actions for this role: answer, tool_call, patch, final, ask_user") {
		t.Error("unknown role should fall back to general agent allowed actions")
	}
}

func TestBuildSystemPromptEachRoleHasAllowedActions(t *testing.T) {
	expected := map[AgentRole]string{
		RoleGeneral:     "Allowed actions for this role: answer, tool_call, patch, final, ask_user",
		RolePlanner:     "Allowed actions for this role: answer, final",
		RoleImplementer: "Allowed actions for this role: tool_call, patch, final",
		RoleTester:      "Allowed actions for this role: tool_call, final",
		RoleReviewer:    "Allowed actions for this role: tool_call, final",
	}

	for role, want := range expected {
		msg := BuildSystemPrompt(role, dummyTools(), nil, nil, false)
		content := msg.Content

		if !strings.Contains(content, want) {
			t.Errorf("role %q missing allowed actions line %q\n%s", role, want, content)
		}
	}
}

func TestBuildSystemPromptNativeModeOmitsPatchFromAllowedActions(t *testing.T) {
	expected := map[AgentRole]string{
		RoleGeneral:     "Allowed actions for this role: answer, tool_call, final, ask_user",
		RolePlanner:     "Allowed actions for this role: answer, final",
		RoleImplementer: "Allowed actions for this role: tool_call, final",
		RoleTester:      "Allowed actions for this role: tool_call, final",
		RoleReviewer:    "Allowed actions for this role: tool_call, final",
	}

	for role, want := range expected {
		msg := BuildSystemPrompt(role, dummyTools(), nil, nil, true)
		content := msg.Content

		if !strings.Contains(content, want) {
			t.Errorf("native role %q missing allowed actions line %q\n%s", role, want, content)
		}
		idx := strings.Index(content, "Allowed actions for this role: ")
		if idx == -1 {
			t.Errorf("native role %q missing allowed actions line\n%s", role, content)
			continue
		}
		actionsLine := content[idx:]
		if nl := strings.Index(actionsLine, "\n"); nl != -1 {
			actionsLine = actionsLine[:nl]
		}
		if strings.Contains(actionsLine, "patch") {
			t.Errorf("native role %q should not list patch in allowed actions line %q", role, actionsLine)
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
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
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
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil, false)
	content := msg.Content

	if !strings.Contains(content, "Available Skills") {
		t.Fatal("system prompt should contain 'Available Skills' section placeholder")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging workflow"})
	idx.Set("deploy", skills.Skill{Name: "deploy", Description: "Deployment workflows"})

	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil, false)
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
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, active, false)
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
	msg := BuildSystemPrompt(RoleGeneral, nil, nil, nil, false)
	content := msg.Content

	if !strings.Contains(content, "No skills are available") {
		t.Fatal("system prompt should note no skills when index is nil")
	}
}

func TestBuildSystemPromptEmptySkillIndex(t *testing.T) {
	idx := skills.NewIndex()
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil, false)
	content := msg.Content

	if !strings.Contains(content, "No skills are available") {
		t.Fatal("system prompt should note no skills when index is empty")
	}
}

func TestBuildSystemPromptRepoScoutRole(t *testing.T) {
	msg := BuildSystemPrompt(RoleRepoScout, nil, nil, nil, false)
	if !strings.Contains(msg.Content, "repo scout") {
		t.Fatalf("repo scout system prompt missing role focus:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "tool_call, final") {
		t.Fatalf("repo scout system prompt missing allowed actions:\n%s", msg.Content)
	}
}

func TestSDDRoleAddenda(t *testing.T) {
	for _, role := range []AgentRole{RoleSDDImplementer, RoleSDDReviewer, RoleSDDBranchReviewer} {
		msg := BuildSystemPrompt(role, dummyTools(), nil, nil, false)
		if !strings.Contains(msg.Content, "SDD") {
			t.Errorf("role %s: system prompt missing SDD context", role)
		}
		addendum, ok := roleAddenda[role]
		if !ok {
			t.Errorf("role %s: no addendum in roleAddenda map", role)
			continue
		}
		if len(addendum.allowedActions) == 0 {
			t.Errorf("role %s: addendum has no allowed actions", role)
		}
	}
}

func TestModeDirectivePlan(t *testing.T) {
	d := modeDirective(policy.ModePlan)
	if !strings.Contains(d, "plan mode") {
		t.Errorf("plan directive %q should mention 'plan mode'", d)
	}
	if !strings.Contains(d, "numbered plan") {
		t.Errorf("plan directive %q should mention 'numbered plan'", d)
	}
}

func TestPlanModeDirectiveAsksQuestionsInSingleCall(t *testing.T) {
	d := modeDirective(policy.ModePlan)
	if !strings.Contains(d, "single question.ask") {
		t.Fatalf("plan directive should require a single question.ask call; got:\n%s", d)
	}
	if !strings.Contains(d, "numbered plan") {
		t.Fatalf("plan directive should require a numbered plan; got:\n%s", d)
	}
	if !strings.Contains(d, "one round") {
		t.Fatalf("plan directive should limit clarifications to one round; got:\n%s", d)
	}
}

func TestModeDirectiveDefault(t *testing.T) {
	d := modeDirective(policy.ModeDefault)
	if !strings.Contains(d, "default mode") {
		t.Errorf("default directive %q should mention 'default mode'", d)
	}
	if !strings.Contains(d, "mode.request") {
		t.Errorf("default directive %q should mention mode.request", d)
	}
}

func TestModeDirectiveCopilot(t *testing.T) {
	d := modeDirective(policy.ModeCopilot)
	if !strings.Contains(d, "copilot mode") {
		t.Errorf("copilot directive %q should mention 'copilot mode'", d)
	}
	if !strings.Contains(d, "auto-approved") {
		t.Errorf("copilot directive %q should mention auto-approved", d)
	}
}

func TestModeDirectiveAuto(t *testing.T) {
	d := modeDirective(policy.ModeAuto)
	if !strings.Contains(d, "auto mode") {
		t.Errorf("auto directive %q should mention 'auto mode'", d)
	}
	if !strings.Contains(d, "cannot ask the user") {
		t.Errorf("auto directive %q should mention cannot ask the user", d)
	}
}

func TestBuildSystemPromptWithModeDirectives(t *testing.T) {
	tests := []struct {
		name    string
		mode    policy.ApprovalMode
		want    string // substring the content must contain
		notWant string // substring the content must NOT contain (empty = skip)
	}{
		{
			name:    "plan mode",
			mode:    policy.ModePlan,
			want:    "plan mode",
			notWant: "mode.request: Ask the user to switch",
		},
		{
			name:    "default mode",
			mode:    policy.ModeDefault,
			want:    "default mode",
			notWant: "",
		},
		{
			name:    "edit mode",
			mode:    policy.ModeEdit,
			want:    "edit mode",
			notWant: "mode.request: Ask the user to switch",
		},
		{
			name:    "copilot mode",
			mode:    policy.ModeCopilot,
			want:    "copilot mode",
			notWant: "mode.request: Ask the user to switch",
		},
		{
			name:    "auto mode",
			mode:    policy.ModeAuto,
			want:    "auto mode",
			notWant: "mode.request: Ask the user to switch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := BuildSystemPromptWithMode(RoleGeneral, dummyTools(), nil, nil, nil, false, tt.mode)
			content := msg.Content

			if !strings.Contains(content, tt.want) {
				t.Errorf("BuildSystemPromptWithMode(%s) content missing %q\n%s", tt.mode, tt.want, content)
			}
			if tt.notWant != "" && strings.Contains(content, tt.notWant) {
				t.Errorf("BuildSystemPromptWithMode(%s) content contains unexpected %q\n%s", tt.mode, tt.notWant, content)
			}
		})
	}
}

func TestBuildSystemPromptWithModeDefaultAdvertisesModeRequest(t *testing.T) {
	msg := BuildSystemPromptWithMode(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeDefault)
	content := msg.Content

	if !strings.Contains(content, "mode.request: Ask the user to switch") {
		t.Errorf("default mode prompt should advertise mode.request tool\n%s", content)
	}
}

func TestBuildSystemPromptWithAddendum(t *testing.T) {
	msg := BuildSystemPromptWithAddendum(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeEdit, "Be extra careful with diffs.")
	if !strings.Contains(msg.Content, "Be extra careful with diffs.") {
		t.Fatalf("addendum missing from prompt:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, baseRules) {
		t.Fatalf("base rules dropped when addendum present")
	}
}

func TestBaseRulesEncourageEarlyFinal(t *testing.T) {
	for _, want := range []string{
		"only to obtain facts",
		"produce a final answer",
		"Stop after validation",
	} {
		if !strings.Contains(baseRules, want) {
			t.Errorf("baseRules missing %q", want)
		}
	}
}

// The existing ask_user example ("archive or delete") is a narrow binary
// fork. Live testing (Kimi's kimi-for-coding, "improve the retry behavior
// in the provider layer") showed a model treating a broad, open-ended
// request as having one "obvious" interpretation and just implementing it,
// without surfacing that several materially different directions existed.
// A second example matching that exact shape of ambiguity — broad scope,
// multiple valid directions, not a binary fork — should make the existing
// "ask if a decision would materially change the outcome" rule easier for
// a model to generalize to open-ended requests, not just yes/no forks.
func TestBuildSystemPromptContainsBroadScopeAskUserExample(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
	content := msg.Content
	for _, want := range []string{
		"broad", "materially different",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("system prompt missing broad-scope ask_user guidance (expected to mention %q)\n%s", want, content)
		}
	}
}

// Native mode gets zero ask_user examples today (nativeOutputFormat has no
// examples block at all, unlike the JSON path's baseOutputFormat) even
// though ask_user is registered as a real tool for RoleGeneral in native
// mode too. Add at least one example so native-mode models have the same
// concrete guidance the JSON path already has.
func TestBuildSystemPromptNativeModeIncludesAskUserExample(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, true)
	content := msg.Content
	if !strings.Contains(content, "ask_user") {
		t.Errorf("native-mode system prompt has no ask_user guidance at all\n%s", content)
	}
	if !strings.Contains(content, "materially different") {
		t.Errorf("native-mode system prompt missing broad-scope ask_user guidance\n%s", content)
	}
}

// RoleSubtask's prompt used to claim "You MUST NOT attempt to write, modify,
// patch, or run arbitrary commands" — but SubtaskScopeView deliberately keeps
// shell.run and file.write_patch available (see
// TestSubtaskScopeViewFiltersTools's "implementation tools must be visible
// to child"), so that claim was false. The only restriction that is
// actually enforced (and actually needs to be, since a subtask's child
// session has no user who could ever answer a question) is question.ask.
// The prompt must describe reality, not an aspirational restriction nobody
// enforces.
func TestBuildSystemPromptSubtaskDescribesActualCapabilities(t *testing.T) {
	msg := BuildSystemPrompt(RoleSubtask, dummyTools(), nil, nil, false)
	content := msg.Content

	for _, unwanted := range []string{
		"MUST NOT attempt to write, modify, patch, or run arbitrary commands",
		"read-only subtask",
	} {
		if strings.Contains(content, unwanted) {
			t.Errorf("subtask prompt still claims a restriction that isn't enforced: %q\n%s", unwanted, content)
		}
	}
	if !strings.Contains(content, "question.ask") && !strings.Contains(content, "prompt the user") {
		t.Errorf("subtask prompt lost the one restriction that IS real (cannot ask the user)\n%s", content)
	}
}

// todoAddendum ("Use todo.write for any user request with 3 or more
// steps...") used to be appended unconditionally regardless of role. But
// swarm's read-only-scoped roles (planner, scouts, reviewer — ScopeReadOnly)
// and the tester (ScopeTester) are built from registry.ReadOnlyView /
// TesterView, neither of which includes todo.write (RiskWorkspaceWrite).
// Telling those roles to use a tool that isn't in their actual tool list
// wastes an iteration on an "unknown tool" error at best. The addendum must
// only appear when todo.write is actually available.
func TestTodoAddendumOmittedWhenToolNotAvailable(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
	if strings.Contains(msg.Content, "todo.write") {
		t.Errorf("prompt references todo.write but it is not in the tool list\n%s", msg.Content)
	}
}

func TestTodoAddendumIncludedWhenToolAvailable(t *testing.T) {
	tools := append(dummyTools(), registry.Tool{
		Name: "todo.write", Risk: registry.RiskWorkspaceWrite, Description: "Track task progress.",
	})
	msg := BuildSystemPrompt(RoleGeneral, tools, nil, nil, false)
	if !strings.Contains(msg.Content, "Use todo.write for any user request") {
		t.Errorf("prompt missing todoAddendum even though todo.write is available\n%s", msg.Content)
	}
}

// ModeAuto's directive ("You cannot ask the user questions") is composed
// into the SAME prompt as the ask_user few-shot examples in
// baseOutputFormat/nativeOutputFormat, which appear later in the text. A
// model reading top-to-bottom sees "you cannot ask" immediately followed,
// further down, by worked examples of calling ask_user — a visible
// contradiction. The auto-mode directive must explicitly override those
// examples rather than silently coexist with them.
func TestBuildSystemPromptAutoModeOverridesAskUserExamples(t *testing.T) {
	for _, native := range []bool{false, true} {
		msg := BuildSystemPromptWithMode(RoleGeneral, dummyTools(), nil, nil, nil, native, policy.ModeAuto)
		content := msg.Content
		if !strings.Contains(content, "cannot ask the user") {
			t.Fatalf("native=%v: auto mode prompt missing the cannot-ask directive\n%s", native, content)
		}
		if !strings.Contains(content, "ask_user") || !strings.Contains(content, "do not apply") {
			t.Errorf("native=%v: auto mode prompt does not explicitly override the ask_user examples shown later in the prompt\n%s", native, content)
		}
	}
}

// Live evidence (Kimi's kimi-for-coding-highspeed against the real
// marshal source tree): 7 of 17 tool failures in one turn were file.read
// calls against plausible-sounding but nonexistent paths guessed from Go
// naming conventions (e.g. "internal/llm/routing/preset.go" when no such
// file exists anywhere in the repo) -- pure wasted iterations, since no
// amount of fuzzy-matching in file.read's error can suggest a file that
// isn't there. The rule set must explicitly discourage guessing a path
// before verifying it exists.
func TestBuildTruncationMessageNamesToolsAndReason(t *testing.T) {
	msg := BuildTruncationMessage([]string{"file.write_patch"})
	if msg.Role != schema.RoleUser {
		t.Fatalf("Role = %q, want %q — the JSON-action path has no tool-call IDs to answer", msg.Role, schema.RoleUser)
	}
	if !strings.Contains(msg.Content, "file.write_patch") {
		t.Fatalf("content must name the refused tool: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "truncated") {
		t.Fatalf("content must explain the truncation: %q", msg.Content)
	}
}

func TestBuildSystemPromptDiscouragesGuessingFilePaths(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
	content := msg.Content
	if !strings.Contains(content, "guessed path") && !strings.Contains(content, "guessing a path") {
		t.Errorf("prompt missing guidance against guessing file paths before verifying they exist\n%s", content)
	}
}
