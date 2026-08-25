package skills

import (
	_ "embed"
	"fmt"
)

//go:embed builtin/marshal-sdd-plan-authoring.md
var marshalSDDPlanAuthoring []byte

//go:embed builtin/marshal-executing-plans.md
var marshalExecutingPlans []byte

//go:embed builtin/brainstorming.md
var brainstormingSkill []byte

//go:embed builtin/test-driven-development.md
var testDrivenDevelopmentSkill []byte

//go:embed builtin/work-decomposition.md
var workDecompositionSkill []byte

//go:embed builtin/dispatching-parallel-agents.md
var dispatchingParallelAgentsSkill []byte

//go:embed builtin/systematic-debugging.md
var systematicDebuggingSkill []byte

//go:embed builtin/verification-before-completion.md
var verificationBeforeCompletionSkill []byte

// loadBuiltIns registers the skills embedded in the binary. They load before
// the global and project skill directories (see LoadSkills), and the index is
// keyed by name — so a user-installed skill of the same name overrides the
// built-in. That precedence is deliberate: an explicit install wins.
func loadBuiltIns(idx *Index) error {
	for name, raw := range map[string][]byte{
		"marshal-sdd-plan-authoring":     marshalSDDPlanAuthoring,
		"marshal-executing-plans":        marshalExecutingPlans,
		"brainstorming":                  brainstormingSkill,
		"test-driven-development":        testDrivenDevelopmentSkill,
		"work-decomposition":             workDecompositionSkill,
		"dispatching-parallel-agents":    dispatchingParallelAgentsSkill,
		"systematic-debugging":           systematicDebuggingSkill,
		"verification-before-completion": verificationBeforeCompletionSkill,
	} {
		skill, err := Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse built-in skill %q: %w", name, err)
		}
		idx.Set(skill.Name, skill)
	}
	return nil
}
