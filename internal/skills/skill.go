// Package skills loads user-defined prompt extensions from TOML files.
//
// Each skill can augment the system prompt for the executor and critic roles,
// and is activated via a slash-command trigger (e.g. "/test").
//
// Example skill file (~/.config/marshal/skills/test.toml):
//
//	name        = "test"
//	description = "Write or update tests"
//	trigger     = "/test"
//
//	[executor]
//	system_extra = """
//	Focus on writing comprehensive tests. Use table-driven tests in Go.
//	Cover edge cases and error paths. Do not modify non-test files unless
//	absolutely necessary.
//	"""
//
//	[critic]
//	system_extra = """
//	PASS only if the new or modified tests actually exercise the described
//	behaviour and would catch real regressions.
//	"""
package skills

// SkillLayer holds the additional system-prompt text for one model role.
type SkillLayer struct {
	// SystemExtra is appended to the role's base system prompt when the
	// skill is active.
	SystemExtra string `toml:"system_extra"`
}

// Skill is a named prompt extension loaded from a TOML file.
type Skill struct {
	Name        string     `toml:"name"`
	Description string     `toml:"description"`
	// Trigger is the slash command that activates this skill, e.g. "/test".
	Trigger     string     `toml:"trigger"`
	Executor    SkillLayer `toml:"executor"`
	Critic      SkillLayer `toml:"critic"`
}
