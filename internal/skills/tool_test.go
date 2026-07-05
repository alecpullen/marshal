package skills

import (
	"context"
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
	if len(msgs) != 1 {
		t.Fatalf("Messages length = %d, want 1", len(msgs))
	}
	if msgs[0].Role != session.RoleSystem {
		t.Fatalf("Message role = %q, want system", msgs[0].Role)
	}
	if msgs[0].Content != "# Debug\n\nReproduce, isolate, fix.\n" {
		t.Fatalf("Message content = %q", msgs[0].Content)
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
