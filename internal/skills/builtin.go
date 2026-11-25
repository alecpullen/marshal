package skills

import (
	_ "embed"
)

//go:embed builtin/marshal-sdd-plan-authoring.md
var marshalSDDPlanAuthoring []byte

//go:embed builtin/marshal-executing-plans.md
var marshalExecutingPlans []byte

//go:embed builtin/marshal-writing-plans.md
var marshalWritingPlans []byte

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

//go:embed builtin/using-skills.md
var usingSkillsSkill []byte

//go:embed builtin/postmortem.md
var postmortemSkill []byte

//go:embed builtin/using-worktrees.md
var usingWorktreesSkill []byte

//go:embed builtin/progress-reporting.md
var progressReportingSkill []byte

// builtInSources is the embedded skill files by name. The name doubles as the
// built-in source's own identity for the entry.
func builtInSources() map[string][]byte {
	return map[string][]byte{
		"marshal-sdd-plan-authoring":     marshalSDDPlanAuthoring,
		"marshal-executing-plans":        marshalExecutingPlans,
		"marshal-writing-plans":          marshalWritingPlans,
		"brainstorming":                  brainstormingSkill,
		"test-driven-development":        testDrivenDevelopmentSkill,
		"work-decomposition":             workDecompositionSkill,
		"dispatching-parallel-agents":    dispatchingParallelAgentsSkill,
		"systematic-debugging":           systematicDebuggingSkill,
		"verification-before-completion": verificationBeforeCompletionSkill,
		"using-skills":                   usingSkillsSkill,
		"postmortem":                     postmortemSkill,
		"using-worktrees":                usingWorktreesSkill,
		"progress-reporting":             progressReportingSkill,
	}
}

// loadBuiltIns registers the skills embedded in the binary. The index is keyed
// by name, and resolution ranks the sources (see sourceRank in resolve.go), so
// a user-installed skill of the same name overrides the built-in. That
// precedence is deliberate: an explicit install wins.
func loadBuiltIns(idx *Index) error {
	candidates, err := builtInCandidates()
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		idx.Set(candidate.Skill.Name, candidate.Skill)
	}
	return nil
}
