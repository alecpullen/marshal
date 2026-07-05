package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/tools/registry"
)

func RegisterTool(reg *registry.Registry, idx *Index, state *session.State) {
	reg.Register(registry.Tool{
		Name:        "skill.load",
		Description: "Load a skill into the agent's context by name. The system prompt lists available skills. Call this when a skill's expertise is relevant to the task.",
		Schema:      json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string", "description": "Name of the skill to load"}}, "required": ["name"]}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   false,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return handleSkillLoad(call, idx, state)
		},
	})
}

func handleSkillLoad(call registry.ToolCall, idx *Index, state *session.State) (registry.ToolResult, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return registry.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" {
		return registry.ToolResult{}, fmt.Errorf("missing required argument: name")
	}

	skill, ok := idx.Load(args.Name)
	if !ok {
		available := idx.List()
		names := make([]string, len(available))
		for i, s := range available {
			names[i] = s.Name
		}
		return registry.ToolResult{}, fmt.Errorf("unknown skill %q. Available: %v", args.Name, names)
	}

	if state.HasActiveSkill(args.Name) {
		return registry.ToolResult{}, fmt.Errorf("skill %q is already active", args.Name)
	}

	pack := state.ContextPack()
	if !pack.IsEmpty() {
		estimatedBody := contextpack.EstimateTokens(skill.Body)
		remaining := pack.TokenUsage.MaxTokens - pack.TokenUsage.EstimatedTokens
		if estimatedBody > remaining {
			return registry.ToolResult{}, fmt.Errorf(
				"cannot load skill: body is ~%d tokens but only %d tokens remain in context budget",
				estimatedBody, remaining,
			)
		}
	}

	state.AddMessage(session.RoleSystem, skill.Body)
	state.ActivateSkill(skill.Name)

	return registry.ToolResult{
		Summary: fmt.Sprintf("Skill %q loaded into context (%d chars).", skill.Name, len(skill.Body)),
	}, nil
}
