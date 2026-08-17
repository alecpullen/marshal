package plugins

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"marshal/internal/skills"
)

// scanSkills reads skills/<name>/SKILL.md bundles under dir. A missing
// skills/ directory yields nil, nil; malformed skills are skipped.
func scanSkills(dir string) ([]skills.Skill, error) {
	skillsDir := filepath.Join(dir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
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

// LoadedPlugin pairs a verified plugin's lockfile name with its scanned
// contents.
type LoadedPlugin struct {
	Name     string
	Contents Contents
}

// LoadStore verifies every plugin recorded in the lockfile at lockPath
// against its on-disk contents in storeDir and returns the valid plugins
// with their scanned contents. It is deliberately forgiving: a missing
// store is a no-op, and per-plugin problems (tampered contents, missing
// files, malformed lockfile) are logged as warnings and skipped so
// startup never aborts because of plugin state. Callers load the global
// store first, then the project store, so later entries win precedence.
func LoadStore(storeDir, lockPath string, logger *slog.Logger) ([]LoadedPlugin, error) {
	if _, err := os.Stat(storeDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat plugin store %s: %w", storeDir, err)
	}

	lf, err := ReadLockfile(lockPath)
	if err != nil {
		logger.Warn("skipping plugin store: lockfile unreadable", "path", lockPath, "error", err)
		return nil, nil
	}

	var loaded []LoadedPlugin
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
		contents, err := ScanPlugin(dir)
		if err != nil {
			logger.Error("skipping plugin: scan failed", "plugin", entry.Name, "error", err)
			continue
		}
		loaded = append(loaded, LoadedPlugin{Name: entry.Name, Contents: contents})
	}
	return loaded, nil
}
