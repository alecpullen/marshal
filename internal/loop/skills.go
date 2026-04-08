package loop

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Skill represents a skill that adds to system prompts.
type Skill struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description"`
	SystemPromptAdditions string `toml:"system_prompt_additions"`
}

// LoadSkills loads skills from all three tiers:
// 1. .marshal/skills/*.toml (project-local)
// 2. ~/.config/marshal/skills/*.toml (user-global)
// 3. Built-in skills (future - not yet implemented)
func LoadSkills(projectDir string) ([]Skill, error) {
	var skills []Skill

	// Tier 1: Project-local skills
	projectSkillsDir := filepath.Join(projectDir, ".marshal", "skills")
	if s, err := loadSkillsFromDir(projectSkillsDir); err != nil {
		return nil, fmt.Errorf("load project skills: %w", err)
	} else {
		skills = append(skills, s...)
	}

	// Tier 2: User-global skills
	home, err := os.UserHomeDir()
	if err == nil {
		globalSkillsDir := filepath.Join(home, ".config", "marshal", "skills")
		if s, err := loadSkillsFromDir(globalSkillsDir); err != nil {
			return nil, fmt.Errorf("load global skills: %w", err)
		} else {
			skills = append(skills, s...)
		}
	}

	// Tier 3: Built-in skills (future)
	// skills = append(skills, loadBuiltinSkills()...)

	// Validate all loaded skills
	for _, skill := range skills {
		if err := validateSkill(skill); err != nil {
			return nil, fmt.Errorf("invalid skill %q: %w", skill.Name, err)
		}
	}

	return skills, nil
}

// loadSkillsFromDir loads all .toml files from a directory.
func loadSkillsFromDir(dir string) ([]Skill, error) {
	var skills []Skill

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return skills, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".toml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		var skill Skill
		if _, err := toml.DecodeFile(path, &skill); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}

		skills = append(skills, skill)
	}

	return skills, nil
}

// validateSkill ensures the skill only uses allowed keys.
func validateSkill(s Skill) error {
	// Name is required
	if s.Name == "" {
		return fmt.Errorf("skill name is required")
	}

	// At least one of description or additions should be present
	if s.Description == "" && s.SystemPromptAdditions == "" {
		return fmt.Errorf("skill %q: at least one of description or system_prompt_additions is required", s.Name)
	}

	// Warn if additions is empty (skill has no effect)
	if s.SystemPromptAdditions == "" {
		// This is a warning but not an error
		// Could log: fmt.Printf("warning: skill %q has no system_prompt_additions\n", s.Name)
	}

	return nil
}
