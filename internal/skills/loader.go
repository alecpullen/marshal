package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ScopedSkill pairs a skill with the scope it was loaded from.
type ScopedSkill struct {
	Skill Skill
	Scope string // "global" or "project"
}

// scanSkillDir walks a directory and returns the paths of all skill files:
// top-level .md files and SKILL.md files inside subdirectories. Returns an
// empty slice (not an error) if the directory does not exist.
func scanSkillDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			bundlePath := filepath.Join(dir, entry.Name(), BundleFileName)
			if _, err := os.Stat(bundlePath); err != nil {
				continue
			}
			paths = append(paths, bundlePath)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	return paths, nil
}

// ListScopes returns skills from both scopes without merging; the caller can
// decide how to handle name collisions.
func ListScopes(globalDir, projectDir string) ([]ScopedSkill, error) {
	var out []ScopedSkill
	for _, pair := range []struct{ dir, scope string }{
		{globalDir, "global"},
		{projectDir, "project"},
	} {
		paths, err := scanSkillDir(pair.dir)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			skill, err := parseSkillFile(p)
			if err != nil {
				continue
			}
			out = append(out, ScopedSkill{Skill: skill, Scope: pair.scope})
		}
	}
	return out, nil
}

func parseSkillFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	return Parse(string(raw))
}

func LoadSkills(globalDir, projectDir string) (*Index, error) {
	idx := NewIndex()

	if err := loadBuiltIns(idx); err != nil {
		return nil, err
	}

	if err := loadFromDir(idx, globalDir, slog.Default()); err != nil {
		return nil, fmt.Errorf("load global skills from %s: %w", globalDir, err)
	}
	if err := loadFromDir(idx, projectDir, slog.Default()); err != nil {
		return nil, fmt.Errorf("load project skills from %s: %w", projectDir, err)
	}

	return idx, nil
}

func loadFromDir(idx *Index, dir string, logger *slog.Logger) error {
	paths, err := scanSkillDir(dir)
	if err != nil {
		return err
	}
	for _, p := range paths {
		loadSkillFile(idx, p, logger)
	}
	return nil
}

func loadSkillFile(idx *Index, path string, logger *slog.Logger) {
	raw, err := os.ReadFile(path)
	if err != nil {
		logger.Warn("failed to read skill file", "path", path, "error", err)
		return
	}

	skill, err := parseFrontmatter(string(raw))
	if err != nil {
		logger.Warn("skipping invalid skill file", "path", path, "error", err)
		return
	}

	idx.skills[skill.Name] = skill
}
