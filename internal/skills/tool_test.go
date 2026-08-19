package skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/tools/registry"
)

func newTestState() *session.State {
	return session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
}

func TestSkillLoadToolSuccess(t *testing.T) {
	idx := NewIndex()
	idx.skills["debug"] = Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Risk:        "read_only",
		Body:        "# Debug\n\nReproduce, isolate, fix.\n",
	}

	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, ok := reg.Lookup("skill.load")
	if !ok {
		t.Fatal("skill.load tool not registered")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("Risk = %s, want read_only", tool.Risk)
	}
	if tool.Cacheable {
		t.Fatal("skill.load should not be cacheable")
	}

	result, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_1",
		Name: "skill.load",
		Args: []byte(`{"name": "debug"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.Summary == "" {
		t.Fatal("expected summary in result")
	}

	if !state.HasActiveSkill("debug") {
		t.Fatal("expected debug to be active after load")
	}

	msgs := state.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages length = %d, want 2 (hidden body + visible tag)", len(msgs))
	}
	if msgs[0].Role != session.RoleSystem {
		t.Fatalf("Message role = %q, want system", msgs[0].Role)
	}
	if msgs[0].ContentType != session.ContentTypeSkillBody {
		t.Fatalf("Body content type = %q, want skill_body", msgs[0].ContentType)
	}
	if msgs[1].ContentType != session.ContentTypeSkill || msgs[1].Content != "debug" {
		t.Fatalf("Tag message = (%q, %q), want (skill, debug)", msgs[1].ContentType, msgs[1].Content)
	}
	// Content should be wrapped in a procedure marker.
	if !strings.Contains(msgs[0].Content, "procedure to follow") {
		t.Fatalf("Message content missing procedure marker:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "```") {
		t.Fatalf("Message content missing fenced block:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "# Debug\n\nReproduce, isolate, fix.") {
		t.Fatalf("Message content missing skill body:\n%s", msgs[0].Content)
	}
}

func TestSkillLoadToolUnknownName(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_1",
		Name: "skill.load",
		Args: []byte(`{"name": "nonexistent"}`),
	})
	if err == nil {
		t.Fatal("expected error for unknown skill name")
	}
}

func TestSkillLoadToolAlreadyActive(t *testing.T) {
	idx := NewIndex()
	idx.skills["debug"] = Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Body:        "# Debug\n\nBody.\n",
	}

	state := newTestState()
	state.ActivateSkill("debug")

	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_2",
		Name: "skill.load",
		Args: []byte(`{"name": "debug"}`),
	})
	if err == nil {
		t.Fatal("expected error for already-active skill")
	}
}

func TestSkillLoadToolContextBudgetExceeded(t *testing.T) {
	idx := NewIndex()
	idx.skills["large"] = Skill{
		Name:        "large",
		Description: "A very large skill",
		Body:        "This skill has a body that exceeds the context budget. " + string(make([]byte, 50000)),
	}

	state := newTestState()
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal"},
		},
		TokenUsage: contextpack.TokenUsage{
			MaxTokens:       12000,
			EstimatedTokens: 11990,
		},
	})

	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_3",
		Name: "skill.load",
		Args: []byte(`{"name": "large"}`),
	})
	if err == nil {
		t.Fatal("expected error for budget exceeded")
	}
}

func TestSkillLoadToolInvalidArgs(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_4",
		Name: "skill.load",
		Args: []byte(`not json`),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON args")
	}
}

func TestSkillBodyIsWrappedAsReference(t *testing.T) {
	idx := NewIndex()
	idx.skills["test-skill"] = Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Body:        "# Test\n\nThis is the skill body.\n",
	}

	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_ref",
		Name: "skill.load",
		Args: []byte(`{"name": "test-skill"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	msgs := state.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages length = %d, want 2 (hidden body + visible tag)", len(msgs))
	}

	content := msgs[0].Content
	// Must contain a fenced block (triple backticks).
	if !strings.Contains(content, "```") {
		t.Fatalf("expected fenced block (triple backticks) in content:\n%s", content)
	}
	// Must contain a "procedure to follow" marker.
	if !strings.Contains(content, "procedure to follow") {
		t.Fatalf("expected 'procedure to follow' marker in content:\n%s", content)
	}
	// Must contain the skill name.
	if !strings.Contains(content, "test-skill") {
		t.Fatalf("expected skill name in content:\n%s", content)
	}
	// Must contain the original body text.
	if !strings.Contains(content, "# Test\n\nThis is the skill body.") {
		t.Fatalf("expected original skill body in content:\n%s", content)
	}
}

func TestSkillLoadToolMissingNameArg(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_5",
		Name: "skill.load",
		Args: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for missing name arg")
	}
}

func TestSkillLoadBudgetEnforced(t *testing.T) {
	idx := NewIndex()
	for _, name := range []string{"one", "two", "three"} {
		idx.skills[name] = Skill{Name: name, Description: "d", Body: "body " + name}
	}
	state := newTestState()
	state.Config.Skills.MaxActive = 2

	if err := LoadSkillIntoSession(idx, state, "one"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if err := LoadSkillIntoSession(idx, state, "two"); err != nil {
		t.Fatalf("second load: %v", err)
	}
	err := LoadSkillIntoSession(idx, state, "three")
	if err == nil || !strings.Contains(err.Error(), "skill budget reached") {
		t.Fatalf("third load should hit the budget, got %v", err)
	}
	if state.HasActiveSkill("three") {
		t.Fatal("refused skill must not become active")
	}
}

func TestSkillLoadBudgetIgnoresAutoloaded(t *testing.T) {
	idx := NewIndex()
	for _, name := range []string{"auto", "one"} {
		idx.skills[name] = Skill{Name: name, Description: "d", Body: "body " + name}
	}
	state := newTestState()
	state.Config.Skills.MaxActive = 1
	state.Config.Skills.Autoload = []string{"auto"}

	if err := LoadSkillIntoSessionQuiet(idx, state, "auto"); err != nil {
		t.Fatalf("autoload: %v", err)
	}
	// The autoloaded skill must not consume the explicit-load budget.
	if err := LoadSkillIntoSession(idx, state, "one"); err != nil {
		t.Fatalf("explicit load under budget: %v", err)
	}
}

func TestSkillLoaderRejectsOnFreshBudgetCheck(t *testing.T) {
	idx := NewIndex()
	// Two skills, each small enough to pass individually, but together
	// they exceed a tight budget.
	body := strings.Repeat("x", 4000) // ~1000 tokens each
	idx.skills["big1"] = Skill{Name: "big1", Description: "d", Body: body}
	idx.skills["big2"] = Skill{Name: "big2", Description: "d", Body: body}

	state := newTestState()
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal"},
		},
		TokenUsage: contextpack.TokenUsage{
			MaxTokens:       12000,
			EstimatedTokens: 10500, // leaves ~1500 tokens — enough for one skill but not two
		},
	})

	// First load succeeds.
	if err := LoadSkillIntoSession(idx, state, "big1"); err != nil {
		t.Fatalf("first load should succeed: %v", err)
	}
	// Second load must fail — the fresh budget check accounts for big1's
	// body tokens already injected, not just the stale context pack.
	err := LoadSkillIntoSession(idx, state, "big2")
	if err == nil {
		t.Fatal("second load should fail with fresh budget check, but succeeded")
	}
	if !strings.Contains(err.Error(), "context budget") {
		t.Fatalf("expected budget error, got: %v", err)
	}
}

func TestSkillLoadBudgetZeroUnlimited(t *testing.T) {
	idx := NewIndex()
	for _, name := range []string{"a", "b", "c", "d"} {
		idx.skills[name] = Skill{Name: name, Description: "d", Body: "body " + name}
	}
	state := newTestState()
	state.Config.Skills.MaxActive = 0
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := LoadSkillIntoSession(idx, state, name); err != nil {
			t.Fatalf("unlimited load %s: %v", name, err)
		}
	}
}
