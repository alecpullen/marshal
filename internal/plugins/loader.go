package plugins

import (
	"fmt"
	"os"
	"path/filepath"

	"marshal/internal/skills"
)

// ScanBundle inspects the plugin directory at dir and returns the skills it
// provides. A valid phase-1 plugin is any directory with a skills/
// subdirectory containing <name>/SKILL.md bundles. Directories without a
// SKILL.md and malformed skill files are skipped silently (the caller
// surfaces counts); a missing skills/ directory is an error because it
// means the repo is not a marshal plugin.
func ScanBundle(dir string) ([]skills.Skill, error) {
	skillsDir := filepath.Join(dir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s contains no skills/ directory: not a marshal plugin", dir)
		}
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	var found []skills.Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(skillsDir, entry.Name(), skills.BundleFileName))
		if err != nil {
			continue
		}
		skill, err := skills.Parse(string(raw))
		if err != nil {
			continue
		}
		found = append(found, skill)
	}
	return found, nil
}
