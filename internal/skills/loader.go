package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

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
