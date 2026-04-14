// Package types provides shared types used across multiple packages.
// This package exists to avoid circular dependencies.
package types

// Skill represents a project-local or user-global skill addition.
// Skills are loaded from TOML files and appended to system prompts.
type Skill struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	SystemPromptAdditions string `toml:"system_prompt_additions"`
}
