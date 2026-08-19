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
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Name of the skill to load"}},"required":["name"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   false,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return handleSkillLoad(call, idx, state)
		},
	})
}

// LoadSkillIntoSession loads a skill by name into the session state,
// posting a compact transcript tag. It checks that the skill exists, is not
// already active, fits within the context budget, and enforces
// skills.max_active against explicitly loaded skills.
//
// Two messages are added: the wrapped body (ContentTypeSkillBody), which
// feeds the model and persists but is hidden from the transcript, and a
// compact tag (ContentTypeSkill) so the transcript shows a one-line
// "skill.load: <name>" trace instead of the full body.
func LoadSkillIntoSession(idx *Index, state *session.State, name string) error {
	return NewSkillLoader(idx, state).Load(name, false)
}

// LoadSkillIntoSessionQuiet loads a skill by name into the session state
// without posting the ContentTypeSkill transcript tag. It is used for
// autoloaded skills so the transcript is not cluttered with automatic
// skill tags, while explicit skill.load tool calls still post the tag.
// Autoloaded skills are user-configured always-on and are exempt from the
// max_active budget.
func LoadSkillIntoSessionQuiet(idx *Index, state *session.State, name string) error {
	return NewSkillLoader(idx, state).Load(name, true)
}

// WrapBody renders a skill's body as the block that goes to the model:
// fenced, labelled with the skill name, and framed as a procedure to
// follow when it matches the current task. Both the session transcript
// copy and the agent's wire transcript use this so the two never drift.
func WrapBody(skill Skill) string {
	return "```\n# The following is a loaded skill: a procedure to follow when it matches the current task.\n" +
		"skill_name: " + skill.Name + "\n" +
		"---\n" +
		skill.Body + "\n```\n"
}

// SkillLoader centralizes skill-loading logic. It rebuilds the context
// budget estimate on each Load call so that multiple rapid loads within
// the same turn cannot exceed the budget by each checking against a stale
// context-pack snapshot.
//
// This is the single entry point for skill loading: the tool handler,
// LoadSkillIntoSession, LoadSkillIntoSessionQuiet, and the ACP handler
// all route through here. Future skill-loading extensions (hooks,
// permissions, telemetry, remote sources) belong on this struct.
type SkillLoader struct {
	idx   *Index
	state *session.State
}

// NewSkillLoader creates a SkillLoader for the given index and session state.
func NewSkillLoader(idx *Index, state *session.State) *SkillLoader {
	return &SkillLoader{idx: idx, state: state}
}

// Load resolves, deduplicates, budget-checks, and injects a skill into the
// session. When quiet is true, no ContentTypeSkill transcript tag is posted
// and the max_active budget is not enforced (for autoloaded skills).
func (sl *SkillLoader) Load(name string, quiet bool) error {
	skill, ok := sl.idx.Load(name)
	if !ok {
		available := sl.idx.List()
		names := make([]string, len(available))
		for i, s := range available {
			names[i] = s.Name
		}
		return fmt.Errorf("unknown skill %q. Available: %v", name, names)
	}

	if sl.state.HasActiveSkill(name) {
		return fmt.Errorf("skill %q is already active", name)
	}

	if !quiet {
		if err := sl.checkMaxActive(); err != nil {
			return err
		}
	}

	if err := sl.checkContextBudget(skill); err != nil {
		return err
	}

	sl.state.AddMessage(session.RoleSystem, WrapBody(skill), session.ContentTypeSkillBody)
	if !quiet {
		sl.state.AddMessage(session.RoleSystem, skill.Name, session.ContentTypeSkill)
	}
	sl.state.ActivateSkill(skill.Name)
	return nil
}

// FreshRemainingTokens estimates the remaining context budget by summing
// the context pack's estimated tokens plus the token cost of all currently
// active skill bodies, then subtracting from MaxTokens. This ensures
// consecutive Load calls see an accurate picture that accounts for skills
// already injected this turn.
func (sl *SkillLoader) FreshRemainingTokens() int {
	pack := sl.state.ContextPack()
	if pack.IsEmpty() {
		// No context pack set — skip budget check (matches prior behavior).
		return -1
	}
	used := pack.TokenUsage.EstimatedTokens
	for _, name := range sl.state.ActiveSkills() {
		if skill, ok := sl.idx.Load(name); ok {
			used += contextpack.EstimateTokens(skill.Body)
		}
	}
	return pack.TokenUsage.MaxTokens - used
}

// checkContextBudget verifies the skill body fits within the remaining
// context budget, accounting for all already-active skill bodies.
func (sl *SkillLoader) checkContextBudget(skill Skill) error {
	remaining := sl.FreshRemainingTokens()
	if remaining < 0 {
		return nil // no context pack set, skip check
	}
	estimatedBody := contextpack.EstimateTokens(skill.Body)
	if estimatedBody > remaining {
		return fmt.Errorf(
			"cannot load skill: body is ~%d tokens but only %d tokens remain in context budget",
			estimatedBody, remaining,
		)
	}
	return nil
}

// checkMaxActive enforces skills.max_active against explicitly loaded
// skills. Autoloaded skills are always-on and do not consume the budget.
// 0 means unlimited.
func (sl *SkillLoader) checkMaxActive() error {
	max := sl.state.Config.Skills.MaxActive
	if max <= 0 {
		return nil
	}
	autoloaded := make(map[string]bool, len(sl.state.Config.Skills.Autoload))
	for _, n := range sl.state.Config.Skills.Autoload {
		autoloaded[n] = true
	}
	active := 0
	for _, n := range sl.state.ActiveSkills() {
		if !autoloaded[n] {
			active++
		}
	}
	if active >= max {
		return fmt.Errorf("skill budget reached (%d active, max %d) — continue with the skills already loaded, or raise skills.max_active", active, max)
	}
	return nil
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
	if err := LoadSkillIntoSession(idx, state, args.Name); err != nil {
		return registry.ToolResult{}, err
	}
	return registry.ToolResult{
		Summary: fmt.Sprintf("Skill %q loaded into context.", args.Name),
	}, nil
}
