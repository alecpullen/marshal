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

// ListScopes returns skills from both scopes without merging; the caller can
// decide how to handle name collisions.
func ListScopes(globalDir, projectDir string) ([]ScopedSkill, error) {
	var out []ScopedSkill
	appendDir := func(dir, scope string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				bundlePath := filepath.Join(dir, entry.Name(), BundleFileName)
				if _, err := os.Stat(bundlePath); err != nil {
					continue
				}
				skill, err := parseSkillFile(bundlePath)
				if err != nil {
					continue
				}
				out = append(out, ScopedSkill{Skill: skill, Scope: scope})
				continue
			}
			if !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			skill, err := parseSkillFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			out = append(out, ScopedSkill{Skill: skill, Scope: scope})
		}
		return nil
	}
	if err := appendDir(globalDir, "global"); err != nil {
		return nil, err
	}
	if err := appendDir(projectDir, "project"); err != nil {
		return nil, err
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

	if err := loadFromDir(idx, globalDir, slog.Default()); err != nil {
		return nil, fmt.Errorf("load global skills from %s: %w", globalDir, err)
	}
	if err := loadFromDir(idx, projectDir, slog.Default()); err != nil {
		return nil, fmt.Errorf("load project skills from %s: %w", projectDir, err)
	}

	return idx, nil
}

func loadFromDir(idx *Index, dir string, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			bundlePath := filepath.Join(dir, entry.Name(), BundleFileName)
			if _, err := os.Stat(bundlePath); err != nil {
				continue
			}
			loadSkillFile(idx, bundlePath, logger)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		loadSkillFile(idx, filepath.Join(dir, entry.Name()), logger)
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
