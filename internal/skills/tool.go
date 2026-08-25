package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func RegisterTool(reg *registry.Registry, idx *Index, state *session.State) {
	reg.Register(registry.Tool{
		Name:        "skill.load",
		Description: "Load a skill into the agent's context by name. The system prompt lists available skills. Call this when a skill's expertise is relevant to the task. Calling it for a skill that is already active re-sends its full text, which is how you recover a skill whose body has aged out of context.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Name of the skill to load"}},"required":["name"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   false,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return handleSkillLoad(call, idx, state)
		},
	})
	reg.Register(registry.Tool{
		Name:        "skill.unload",
		Description: "Drop a loaded skill from the agent's context by name, so its body stops consuming context on every turn. Call this when you are done with a skill's procedure.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Name of the skill to unload"}},"required":["name"],"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   false,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Name == "" {
				return registry.ToolResult{}, fmt.Errorf("missing required argument: name")
			}
			if err := NewSkillLoader(idx, state).Unload(args.Name); err != nil {
				return registry.ToolResult{}, err
			}
			return registry.ToolResult{
				Summary: fmt.Sprintf("Skill %q unloaded.", args.Name),
			}, nil
		},
	})
}

// LoadSkillIntoSession loads a skill by name into the session state,
// posting a compact transcript tag. It checks that the skill exists, is
// not already active, and enforces skills.max_active against explicitly
// loaded skills.
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

// SkillLoader centralizes skill-loading logic.
//
// Skill bodies are emitted as their own system messages on the wire (see
// agent.Runner.appendSkillBodies) and never occupy context-pack budget,
// so the loader deliberately does not budget-check them against the
// pack. skills.max_active bounds how many load.
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

// Load resolves and injects a skill into the session. When quiet is true, no
// ContentTypeSkill transcript tag is posted (used for autoloaded skills).
//
// Loading an already-active skill is a re-fetch, not an error: bodies age
// out of the wire (internal/agent/route.go appendSkillBodies), so asking
// again is how the model gets the full text back.
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
		sl.state.ResetSkillBodyAge(name)
		return nil
	}

	sl.evictForBudget()

	sl.state.AddMessage(session.RoleSystem, WrapBody(skill), session.ContentTypeSkillBody)
	if !quiet {
		sl.state.AddMessage(session.RoleSystem, skill.Name, session.ContentTypeSkill)
	}
	sl.state.ActivateSkill(skill.Name)
	return nil
}

// Unload deactivates a skill so its body stops being written to the wire.
func (sl *SkillLoader) Unload(name string) error {
	if !sl.state.DeactivateSkill(name) {
		return fmt.Errorf("skill %q is not active", name)
	}
	sl.state.AddMessage(session.RoleSystem, name, session.ContentTypeSkill)
	return nil
}

// evictForBudget drops least-recently-activated skills until there is room
// for one more under skills.max_active. Autoloaded skills are user-configured
// always-on and are never evicted (nor counted). 0 means unlimited.
//
// This replaces the old behaviour of refusing the new skill: refusing left
// the model stuck with whatever it happened to load first, with no way to
// change the set.
func (sl *SkillLoader) evictForBudget() {
	max := sl.state.Config.Skills.MaxActive
	if max <= 0 {
		return
	}
	pinned := make(map[string]bool, len(sl.state.Config.Skills.Autoload))
	for _, n := range sl.state.Config.Skills.Autoload {
		pinned[n] = true
	}
	for {
		evictable := make([]string, 0, max)
		for _, n := range sl.state.ActiveSkillsByAge() {
			if !pinned[n] {
				evictable = append(evictable, n)
			}
		}
		if len(evictable) < max {
			return
		}
		sl.state.DeactivateSkill(evictable[0])
	}
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
