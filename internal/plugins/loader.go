package plugins

import (
	"fmt"
	"log/slog"
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


// LoadStore verifies every plugin recorded in the lockfile at lockPath
// against its on-disk contents in storeDir and merges valid plugins'
// skills into idx. It is deliberately forgiving: a missing store is a
// no-op, and per-plugin problems (tampered contents, missing files,
// malformed lockfile) are logged as warnings and skipped so startup never
// aborts because of plugin state. Callers load the global store first,
// then the project store, so project plugins win name collisions.
func LoadStore(idx *skills.Index, storeDir, lockPath string, logger *slog.Logger) error {
	if _, err := os.Stat(storeDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat plugin store %s: %w", storeDir, err)
	}

	lf, err := ReadLockfile(lockPath)
	if err != nil {
		logger.Warn("skipping plugin store: lockfile unreadable", "path", lockPath, "error", err)
		return nil
	}

	for _, entry := range lf.Plugins {
		dir := filepath.Join(storeDir, entry.Name)
		hash, err := HashDir(dir)
		if err != nil {
			logger.Warn("skipping plugin: cannot read installed files", "plugin", entry.Name, "error", err)
			continue
		}
		if hash != entry.ContentHash {
			logger.Warn("skipping plugin: contents changed since install; run `marshal plugin update` to review and re-pin", "plugin", entry.Name)
			continue
		}
		pluginSkills, err := ScanBundle(dir)
		if err != nil {
			logger.Warn("skipping plugin", "plugin", entry.Name, "error", err)
			continue
		}
		for _, skill := range pluginSkills {
			idx.Set(skill.Name, skill)
		}
	}
	return nil
}
