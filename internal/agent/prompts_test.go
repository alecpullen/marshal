package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/routing"
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

func TestBuildSystemPromptIncludesToolArgHints(t *testing.T) {
	tools := []registry.Tool{{
		Name:        "file.read",
		Risk:        registry.RiskReadOnly,
		Description: "Read a file.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}}
	msg := BuildSystemPrompt(RoleGeneral, tools, nil, nil, false)
	want := "- file.read (read_only): Read a file. — args: path:string"
	if !strings.Contains(msg.Content, want) {
		t.Fatalf("system prompt missing arg hint %q", want)
	}
}

func TestBaseOutputFormatIncludesSearchFewShot(t *testing.T) {
	if !strings.Contains(baseOutputFormat, `"tool": "repo.search"`) {
		t.Fatal("baseOutputFormat missing repo.search few-shot")
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
		"You are Marshal, a local-friendly coding assistant",
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

func TestBaseRulesGuideQuestionToolUsage(t *testing.T) {
	if !strings.Contains(baseRules, "ask the user with question.ask") {
		t.Errorf("baseRules should mention question.ask:\n%s", baseRules)
	}
}

func TestBaseRulesOmitSkillLoadCarveOut(t *testing.T) {
	if strings.Contains(baseRules, "skill.load") {
		t.Errorf("baseRules should not mention skill.load (the skillDirective covers it):\n%s", baseRules)
	}
}

func TestBaseRulesOmitDestructiveCommandRule(t *testing.T) {
	if strings.Contains(baseRules, "Destructive or risky commands require explicit user approval") {
		t.Errorf("baseRules should not include the destructive-command rule (policy enforces it):\n%s", baseRules)
	}
}

func TestBaseRulesOmitSummariseRule(t *testing.T) {
	if strings.Contains(baseRules, "Summarise results clearly") {
		t.Errorf("baseRules should not include the generic 'Summarise results clearly' rule:\n%s", baseRules)
	}
}

func TestBaseRulesKeepShellWriteRule(t *testing.T) {
	for _, want := range []string{
		"file.write or file.write_patch",
		"never via shell redirection, heredocs, or tee",
	} {
		if !strings.Contains(baseRules, want) {
			t.Errorf("baseRules must keep the shell-write rule %q:\n%s", want, baseRules)
		}
	}
}

func TestBaseRulesCondensedAskUserRule(t *testing.T) {
	// The ask_user rule should be condensed to ~100 chars, not the 300+ char version
	if strings.Contains(baseRules, "Prefer question.ask when you have multiple related questions") {
		t.Errorf("baseRules ask_user rule should be condensed (no longer includes the options/multi-select guidance):\n%s", baseRules)
	}
	if !strings.Contains(baseRules, "ask the user with question.ask") {
		t.Errorf("baseRules must still mention question.ask:\n%s", baseRules)
	}
}

func TestBaseRulesMergedReadBeforeEdit(t *testing.T) {
	// "Never invent file contents" and "Do not read a guessed path" should
	// be merged into one rule, not two separate bullets. The old version
	// had them as two separate lines starting with "- Never invent" and
	// "- Do not read a guessed path". The merged version has them on one
	// line: "- Never invent file contents; read before editing. Do not
	// read a guessed path..."
	if strings.Contains(baseRules, "\n- Do not read a guessed path") {
		t.Errorf("baseRules should merge the read-before-edit and guessed-path rules (guessed-path should not be a separate bullet):\n%s", baseRules)
	}
	if !strings.Contains(baseRules, "Never invent file contents; read before editing. Do not read a guessed path") {
		t.Errorf("baseRules should contain the merged rule:\n%s", baseRules)
	}
}

func TestBaseRulesForbidShellFileWrites(t *testing.T) {
	for _, want := range []string{
		"file.write or file.write_patch",
		"never via shell redirection, heredocs, or tee",
	} {
		if !strings.Contains(baseRules, want) {
			t.Errorf("baseRules missing shell-write rule %q", want)
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

func TestNativePatchFormatIsTrimmed(t *testing.T) {
	// The trimmed format must contain the delimiter syntax and the
	// exact-match rule, but must NOT contain the full multi-line examples
	// or the unified-diff example.
	for _, want := range []string{
		"<<<<<<< SEARCH",
		">>>>>>> REPLACE",
		"must match the file content exactly",
		"prefer the file.write tool",
	} {
		if !strings.Contains(nativePatchFormat, want) {
			t.Errorf("nativePatchFormat missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"File: internal/app/config/types_test.go",
		"--- a/internal/app/config/types.go",
		"@@ -1,4 +1,5 @@",
	} {
		if strings.Contains(nativePatchFormat, unwanted) {
			t.Errorf("nativePatchFormat should not contain %q (example removed)", unwanted)
		}
	}
	if len(nativePatchFormat) > 600 {
		t.Errorf("nativePatchFormat is %d chars, should be under 600 (was 1708)", len(nativePatchFormat))
	}
}

func TestNativePatchFormatIncludesChainedExample(t *testing.T) {
	for _, want := range []string{
		"prefer the file.write tool",
		">>>>>>> REPLACE",
		"chain",
		"must match the file content exactly",
	} {
		if !strings.Contains(nativePatchFormat, want) {
			t.Errorf("nativePatchFormat missing %q", want)
		}
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
		"Unified diffs are also accepted but search/replace is preferred",
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
		"You are Marshal, a local-friendly coding assistant",
		"Prefer small, verifiable changes",
		"You are an implementer",
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

func TestNativeModeOmitsAvailableToolsProseList(t *testing.T) {
	tools := []registry.Tool{
		{Name: "file.read", Risk: registry.RiskReadOnly, Description: "Read a file."},
		{Name: "shell.run", Risk: registry.RiskCommand, Description: "Run a shell command."},
	}
	msg := BuildSystemPrompt(RoleGeneral, tools, nil, nil, true)
	content := msg.Content
	if strings.Contains(content, "Available tools:") {
		t.Errorf("native mode should not contain the Available tools prose list\n%s", content)
	}
	// Tool descriptions should not appear as prose in native mode
	if strings.Contains(content, "Read a file.") {
		t.Errorf("native mode should not include tool descriptions as prose\n%s", content)
	}
}

func TestNativeModeDefaultOmitsModeRequestLine(t *testing.T) {
	msg := BuildSystemPromptWithMode(RoleGeneral, dummyTools(), nil, nil, nil, true, policy.ModeDefault)
	content := msg.Content
	if strings.Contains(content, "mode.request: Ask the user to switch") {
		t.Errorf("native default mode should not manually append mode.request line (it's in the API payload)\n%s", content)
	}
}

func TestJSONModeStillIncludesAvailableToolsList(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, dummyTools(), nil, nil, false)
	content := msg.Content
	if !strings.Contains(content, "Available tools:") {
		t.Errorf("JSON mode must still include the Available tools prose list\n%s", content)
	}
}

func TestJSONModeDefaultStillIncludesModeRequestLine(t *testing.T) {
	msg := BuildSystemPromptWithMode(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeDefault)
	content := msg.Content
	if !strings.Contains(content, "mode.request: Ask the user to switch") {
		t.Errorf("JSON default mode must still include the mode.request line\n%s", content)
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

	if !strings.Contains(content, "Skills") {
		t.Fatal("system prompt should contain a Skills section placeholder")
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

	if !strings.Contains(content, "ACTIVE") {
		t.Fatal("system prompt should mark a loaded skill as active")
	}
	if !strings.Contains(content, "`debug`") {
		t.Fatal("system prompt should list active skill name")
	}
}

// An active skill must not hide the rest of the roster. Skill suites are
// interconnected — one skill routinely tells the model to load another —
// so dropping the list on the first load stranded every remaining skill.
func TestBuildSystemPromptKeepsRosterWhenSkillActive(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging workflow"})
	idx.Set("deploy", skills.Skill{Name: "deploy", Description: "Deployment workflows"})

	content := BuildSystemPrompt(RoleGeneral, nil, idx, []string{"debug"}, false).Content

	if !strings.Contains(content, "`deploy`") || !strings.Contains(content, "Deployment workflows") {
		t.Fatalf("inactive skills must stay listed while another is active:\n%s", content)
	}
	if !strings.Contains(content, "skill.load") {
		t.Fatal("system prompt should still explain skill.load while a skill is active")
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

func TestSDDPlanAuthorRolePrompt(t *testing.T) {
	msg := BuildSystemPrompt(RoleSDDPlanAuthor, dummyTools(), nil, nil, false)
	if !strings.Contains(msg.Content, "plan author") {
		t.Errorf("plan-author system prompt missing authoring language: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "approved design") {
		t.Errorf("plan-author system prompt missing approved-design handoff: %q", msg.Content)
	}
	// The role's allowed-actions line must not include ask_user, even though
	// the shared base rules mention it.
	allowed := "Allowed actions for this role:"
	idx := strings.Index(msg.Content, allowed)
	if idx < 0 {
		t.Fatalf("plan-author system prompt missing allowed-actions line")
	}
	line := msg.Content[idx:]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	if strings.Contains(line, "ask_user") {
		t.Errorf("plan-author allowed actions must exclude ask_user: %q", line)
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

func TestNativeOutputFormatOmitsAskUserParagraph(t *testing.T) {
	// The ask_user guidance is in baseRules; nativeOutputFormat should
	// not duplicate it.
	if strings.Contains(nativeOutputFormat, "ask_user") {
		t.Errorf("nativeOutputFormat should not contain ask_user guidance (it's in baseRules):\n%s", nativeOutputFormat)
	}
	if strings.Contains(nativeOutputFormat, "materially different") {
		t.Errorf("nativeOutputFormat should not contain the broad-scope ask_user paragraph:\n%s", nativeOutputFormat)
	}
}

func TestNativeOutputFormatKeepsJSONObjectRule(t *testing.T) {
	for _, want := range []string{
		"single valid JSON object",
		"Do not concatenate multiple JSON objects",
	} {
		if !strings.Contains(nativeOutputFormat, want) {
			t.Errorf("nativeOutputFormat missing JSON-object rule %q:\n%s", want, nativeOutputFormat)
		}
	}
}

func TestAutoModeDirectiveOmitsRetraction(t *testing.T) {
	d := modeDirective(policy.ModeAuto)
	if !strings.Contains(d, "cannot ask the user") {
		t.Errorf("auto directive must still say 'cannot ask the user':\n%s", d)
	}
	if strings.Contains(d, "do not apply") {
		t.Errorf("auto directive should not contain the retraction sentence:\n%s", d)
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

func TestBuildSystemPromptIncludesScratchpadAddendumForNativeTools(t *testing.T) {
	prompt := BuildSystemPromptWithAddendum(RoleGeneral, nil, nil, nil, nil, true, policy.ModeAuto, "", "", "")
	if !strings.Contains(prompt.Content, "scratchpad.write") {
		t.Fatal("expected scratchpad addendum when nativeTools is true")
	}
}

func TestBuildSystemPromptOmitsScratchpadAddendumForJSONTools(t *testing.T) {
	prompt := BuildSystemPromptWithAddendum(RoleGeneral, nil, nil, nil, nil, false, policy.ModeAuto, "", "", "")
	if strings.Contains(prompt.Content, "scratchpad.write") {
		t.Fatal("expected no scratchpad addendum when nativeTools is false")
	}
}

func TestBuildSystemPromptIncludesWorkingDir(t *testing.T) {
	wantDir := "/Users/alecpullen/projects/marshal"
	msg := BuildSystemPromptWithAddendum(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeEdit, "", wantDir, "")
	if !strings.Contains(msg.Content, wantDir) {
		t.Fatalf("system prompt missing working dir %q\n%s", wantDir, msg.Content)
	}
	if !strings.Contains(msg.Content, "shell.run executes with this directory as its cwd") {
		t.Errorf("system prompt missing cwd guidance\n%s", msg.Content)
	}
}

func TestBuildSystemPromptOmitsWorkingDirWhenEmpty(t *testing.T) {
	msg := BuildSystemPromptWithAddendum(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeEdit, "", "", "")
	if strings.Contains(msg.Content, "The workspace root is") {
		t.Errorf("empty workingDir should not inject workspace root line\n%s", msg.Content)
	}
}

func TestBuildSystemPromptNativePatchFormatCoversAuditFindings(t *testing.T) {
	tools := append(dummyTools(), registry.Tool{
		Name:        "file.write_patch",
		Risk:        registry.RiskWorkspaceWrite,
		Description: "Apply search/replace patch blocks to workspace files.",
	})
	msg := BuildSystemPromptWithAddendum(RoleGeneral, tools, nil, nil, nil, true, policy.ModeEdit, "", "/tmp/workspace", "")
	content := msg.Content

	for _, want := range []string{
		">>>>>>> REPLACE",
		"must match the file content exactly",
		"prefer the file.write tool",
		"chain",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("native system prompt missing audit guidance %q\n%s", want, content)
		}
	}
}

func TestBuildSystemPromptWithAddendum(t *testing.T) {
	msg := BuildSystemPromptWithAddendum(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeEdit, "Be extra careful with diffs.", "", "")
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

func TestBaseRulesNudgesReviewerSubagent(t *testing.T) {
	want := "dispatch a reviewer subagent with agent.run instead of reviewing inline"
	if !strings.Contains(baseRules, want) {
		t.Errorf("baseRules missing reviewer subagent nudge %q", want)
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

// ModeAuto's directive must tell the model it cannot ask the user
// questions, in both native and JSON modes.
func TestBuildSystemPromptAutoModeOverridesAskUserExamples(t *testing.T) {
	for _, native := range []bool{false, true} {
		msg := BuildSystemPromptWithMode(RoleGeneral, dummyTools(), nil, nil, nil, native, policy.ModeAuto)
		content := msg.Content
		if !strings.Contains(content, "cannot ask the user") {
			t.Fatalf("native=%v: auto mode prompt missing the cannot-ask directive\n%s", native, content)
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

func TestSkillDirectiveIsCondensed(t *testing.T) {
	for _, want := range []string{
		"skill.load",
		"YOUR job",
		"BEFORE acting",
	} {
		if !strings.Contains(skillDirective, want) {
			t.Errorf("skillDirective missing %q:\n%s", want, skillDirective)
		}
	}
	if len(skillDirective) > 400 {
		t.Errorf("skillDirective is %d chars, should be under 400 (was 1041)", len(skillDirective))
	}
}

func TestSkillDirectiveStillMentionsAgentRun(t *testing.T) {
	// The condensed directive should still mention agent.run for
	// skill-driven subagents.
	if !strings.Contains(skillDirective, "agent.run") {
		t.Errorf("skillDirective should still mention agent.run:\n%s", skillDirective)
	}
}

func TestSkillReminderDroppedFromPrompt(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging"})
	content := BuildSystemPrompt(RoleGeneral, nil, idx, nil, false).Content
	if strings.Contains(content, "Reminder: check the Skills list") {
		t.Errorf("skillReminder should not appear in the prompt:\n%s", content)
	}
}

// TestSkillDirectiveFavorsRelevance: the old directive said "When in doubt,
// load it", which drove misfit loads (brainstorming/writing-plans on an
// execute-existing-plan task — postmortem 2026-08-06). The directive must
// now push the other way: load only what directly applies.
func TestSkillDirectiveFavorsRelevance(t *testing.T) {
	if strings.Contains(skillDirective, "When in doubt, load it") {
		t.Fatal("directive still encourages speculative loads")
	}
	if !strings.Contains(skillDirective, "directly matches") {
		t.Fatal("directive should require a direct task match")
	}
}

func TestSkillDirectiveMentionsAgentRun(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "debugging", Body: "# Debug\n"})
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil, false)
	if !strings.Contains(msg.Content, "agent.run") {
		t.Fatalf("system prompt should tell the model to use agent.run for skill-driven subagents:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "dispatch or spawn a subagent") {
		t.Fatalf("system prompt should mention dispatching/spawning subagents:\n%s", msg.Content)
	}
}

func TestRenderAgentRosterEmpty(t *testing.T) {
	if got := RenderAgentRoster(config.Default()); got != "" {
		t.Fatalf("empty roster = %q, want empty", got)
	}
}

func TestRenderAgentRosterListsPresetsAndCustomAgents(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"reviewer": {Name: "reviewer", Preset: "openai/gpt-4o-mini", SystemPrompt: "Read-only reviewer"},
	}
	got := RenderAgentRoster(cfg)
	for _, want := range []string{
		"Custom agents:",
		"reviewer (openai/gpt-4o-mini)",
		"Read-only reviewer",
		"Model presets (valid provider/model pairs):",
		"openai/gpt-4o-mini",
		"model must be a provider/model pair; the provider must be configured",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("roster missing %q:\n%s", want, got)
		}
	}
}

// TestRenderAgentRosterWithDiscoveredLabelsProbedModels verifies that
// discovered (probed) models are listed separately from configured presets
// and do not leak into the "valid provider/model pairs" contract.
func TestRenderAgentRosterWithDiscoveredLabelsProbedModels(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	discovered := map[string][]schema.ModelInfo{
		"openai": {{ID: "gpt-5.6-luna"}, {ID: "gpt-4o-mini"}},
	}
	got := RenderAgentRosterWithDiscovered(cfg, discovered)
	for _, want := range []string{
		"Model presets (valid provider/model pairs):",
		"openai/gpt-4o-mini",
		"Also discovered from configured providers",
		"openai/gpt-5.6-luna",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("roster missing %q:\n%s", want, got)
		}
	}
	// The discovered model must NOT be presented as a preset line: the
	// "Model presets" section must contain only the configured preset.
	presetsIdx := strings.Index(got, "Model presets (valid provider/model pairs):")
	discoveredIdx := strings.Index(got, "Also discovered from configured providers")
	if presetsIdx < 0 || discoveredIdx < 0 {
		t.Fatalf("roster missing expected sections:\n%s", got)
	}
	if strings.Contains(got[presetsIdx:discoveredIdx], "gpt-5.6-luna") {
		t.Errorf("discovered model leaked into the presets section:\n%s", got)
	}
}

// TestRenderAgentRosterWithDiscoveredDeduplicates verifies a discovered
// model that is already a configured preset is not listed twice.
func TestRenderAgentRosterWithDiscoveredDeduplicates(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	discovered := map[string][]schema.ModelInfo{
		"openai": {{ID: "gpt-4o-mini"}, {ID: "gpt-5.6-luna"}},
	}
	got := RenderAgentRosterWithDiscovered(cfg, discovered)
	if n := strings.Count(got, "gpt-4o-mini"); n != 1 {
		t.Errorf("preset model listed %d times, want 1:\n%s", n, got)
	}
}

// TestRenderAgentRosterWithDiscoveredEmpty verifies no discovery section is
// emitted when nothing was probed — output identical to RenderAgentRoster.
func TestRenderAgentRosterWithDiscoveredEmpty(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	if got := RenderAgentRosterWithDiscovered(cfg, nil); got != RenderAgentRoster(cfg) {
		t.Errorf("nil discovered should match plain roster:\n%s", got)
	}
}

// TestRenderAgentRosterFooterIsHonest verifies the footer no longer claims
// the listed pairs are the only valid ones: any model on a configured
// provider is accepted by the resolver.
func TestRenderAgentRosterFooterIsHonest(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	got := RenderAgentRoster(cfg)
	if strings.Contains(got, "must name one of these") {
		t.Errorf("footer still overstates authority:\n%s", got)
	}
	for _, want := range []string{
		"model must be a provider/model pair; the provider must be configured",
		"listed presets are only what is configured locally — any model the provider serves is valid",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("footer missing %q:\n%s", want, got)
		}
	}
}

// TestDiscoveredModelsFromCache verifies the bridge from the on-disk
// modelcache to the roster's discovered map: only fresh, config-matching
// entries are included, keyed per provider.
func TestDiscoveredModelsFromCache(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "https://a/v1"}
	fresh := time.Now().Add(-time.Hour)
	stale := time.Now().Add(-72 * time.Hour) // beyond DefaultTTL of 24h
	if err := modelcache.Save(dir, modelcache.Cache{Providers: map[string]modelcache.Entry{
		"openai": {ConfigHash: modelcache.HashProvider(pc), Models: []schema.ModelInfo{{ID: "gpt-5.6-luna"}}, FetchedAt: fresh},
		"ollama": {ConfigHash: modelcache.HashProvider(config.ProviderConfig{Type: "ollama"}), Models: []schema.ModelInfo{{ID: "qwen3:8b"}}, FetchedAt: stale},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": pc,
		// ollama is not in cfg.Providers at all — its entry must be ignored.
	}
	got := DiscoveredModelsFromCache(cfg, dir, time.Now())
	if len(got["openai"]) != 1 || got["openai"][0].ID != "gpt-5.6-luna" {
		t.Errorf("openai discovery = %+v, want [gpt-5.6-luna]", got["openai"])
	}
	if _, ok := got["ollama"]; ok {
		t.Error("unconfigured provider must not be reported as discovered")
	}
}

// TestDiscoveredModelsFromCacheMissingDir verifies a missing/empty cache
// yields an empty map, never an error or nil-map panic.
func TestDiscoveredModelsFromCacheMissingDir(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {Type: "openai_compatible", BaseURL: "https://a/v1"},
	}
	got := DiscoveredModelsFromCache(cfg, filepath.Join(t.TempDir(), "nope"), time.Now())
	if len(got) != 0 {
		t.Errorf("missing cache should yield empty map, got %+v", got)
	}
}

func TestBuildSystemPromptIncludesRosterWhenAgentRunAvailable(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	tools := []registry.Tool{{Name: "agent.run", Risk: registry.RiskWorkspaceWrite, Description: "Dispatch a subagent."}}
	msg := BuildSystemPromptWithAddendum(RoleGeneral, tools, nil, nil, nil, false, policy.ModeEdit, "", "", RenderAgentRoster(cfg))
	if !strings.Contains(msg.Content, "## Agents and models") {
		t.Fatalf("system prompt missing roster section:\n%s", msg.Content)
	}
	if !strings.Contains(msg.Content, "openai/gpt-4o-mini") {
		t.Fatalf("roster missing preset pair:\n%s", msg.Content)
	}
}

func TestBuildSystemPromptOmitsRosterWithoutAgentRun(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/gpt-4o-mini": {Provider: "openai", Model: "gpt-4o-mini"},
	}
	msg := BuildSystemPromptWithAddendum(RoleGeneral, dummyTools(), nil, nil, nil, false, policy.ModeEdit, "", "", RenderAgentRoster(cfg))
	if strings.Contains(msg.Content, "## Agents and models") {
		t.Fatalf("roster should not appear without agent.run:\n%s", msg.Content)
	}
}

func TestPromptsNoLongerForbidUnifiedDiff(t *testing.T) {
	for _, s := range []string{baseOutputFormat, nativePatchFormat} {
		if strings.Contains(s, "Do not use unified diff") {
			t.Error("prompt still forbids unified diffs")
		}
	}
}

func TestBuildSystemPromptSkipsDeferredToolsInList(t *testing.T) {
	deferredTool := registry.Tool{
		Name:        "config.read",
		Risk:        registry.RiskReadOnly,
		Description: "Read the current Marshal configuration.",
		Deferred:    true,
	}
	msg := BuildSystemPromptWithDeferred(RoleGeneral, []registry.Tool{deferredTool}, []registry.Tool{deferredTool}, nil, nil, false)
	content := msg.Content

	availableIdx := strings.Index(content, "Available tools:\n")
	announceIdx := strings.Index(content, "Additional tools are available")
	if availableIdx < 0 || announceIdx < 0 {
		t.Fatalf("prompt missing expected sections:\n%s", content)
	}
	listSection := content[availableIdx:announceIdx]
	if strings.Contains(listSection, "config.read") {
		t.Fatalf("deferred tool should not appear in the Available tools list:\n%s", listSection)
	}
	if !strings.Contains(content[announceIdx:], "config.read") {
		t.Fatalf("deferred tool missing from the deferred announcement:\n%s", content[announceIdx:])
	}
}

func TestBuildSystemPromptLoadedDeferredToolMovesToAvailableList(t *testing.T) {
	loadedTool := registry.Tool{
		Name:        "config.read",
		Risk:        registry.RiskReadOnly,
		Description: "Read the current Marshal configuration.",
		Deferred:    true,
	}
	unloadedTool := registry.Tool{
		Name:        "config.providers.set",
		Risk:        registry.RiskWorkspaceWrite,
		Description: "Set a provider.",
		Deferred:    true,
	}
	// config.read is opted into via tools.select; config.providers.set is not.
	msg := BuildSystemPromptWithAddendum(RoleGeneral, []registry.Tool{loadedTool, unloadedTool}, []registry.Tool{loadedTool, unloadedTool}, nil, nil, false, policy.ModeEdit, "", "", "", "config.read")
	content := msg.Content

	availableIdx := strings.Index(content, "Available tools:\n")
	announceIdx := strings.Index(content, "Additional tools are available")
	if availableIdx < 0 || announceIdx < 0 {
		t.Fatalf("prompt missing expected sections:\n%s", content)
	}
	listSection := content[availableIdx:announceIdx]
	announceSection := content[announceIdx:]
	if !strings.Contains(listSection, "config.read") {
		t.Fatalf("loaded deferred tool should appear in the Available tools list:\n%s", listSection)
	}
	if strings.Contains(announceSection, "config.read") {
		t.Fatalf("loaded deferred tool should be excluded from the not-loaded announcement:\n%s", announceSection)
	}
	if !strings.Contains(announceSection, "config.providers.set") {
		t.Fatalf("unloaded deferred tool missing from the announcement:\n%s", announceSection)
	}
}

func TestSystemPromptOptionsMatchesWrappers(t *testing.T) {
	tools := []registry.Tool{
		{Name: "file.read", Risk: registry.RiskReadOnly, Description: "Read a file."},
		{Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite, Description: "Patch files."},
	}
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging"})

	// BuildSystemPromptWithAddendum is the superset wrapper — it exercises
	// every parameter. Compare it against a direct buildSystemPrompt call
	// with the equivalent SystemPromptOptions.
	want := BuildSystemPromptWithAddendum(
		RoleGeneral, tools, nil, idx, []string{"debug"},
		true, policy.ModeAuto, "extra addendum", "/tmp", "roster text", "config.read",
	)
	got := buildSystemPrompt(SystemPromptOptions{
		Role:         RoleGeneral,
		Tools:        tools,
		Deferred:     nil,
		SkillIndex:   idx,
		ActiveSkills: []string{"debug"},
		NativeTools:  true,
		Mode:         policy.ModeAuto,
		Addendum:     "extra addendum",
		WorkingDir:   "/tmp",
		Roster:       "roster text",
		LoadedNames:  []string{"config.read"},
	})
	if want.Content != got.Content {
		t.Fatalf("struct-based prompt differs from wrapper:\n--- want ---\n%s\n--- got ---\n%s", want.Content, got.Content)
	}
}
