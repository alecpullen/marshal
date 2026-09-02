package config

import (
	"os"
	"path/filepath"
)

// marshalDirIgnore lists the machine-local state that must never be
// committed even when .marshal/config.toml is shared. config.toml is
// deliberately absent: it is the committable project layer.
const marshalDirIgnore = `marshal.db
marshal.db-*
marshal.log
tool-results/
pipeline/
worktrees/
plugins/
plugins-lock.json
skills/
`

// EnsureMarshalDirIgnored writes .marshal/.gitignore excluding
// machine-local state, creating .marshal/ if needed. Idempotent: an
// existing file is left untouched so user additions survive. Best-effort
// by contract — callers log failures and continue.
func EnsureMarshalDirIgnored(workingDir string) error {
	dir := filepath.Join(workingDir, ".marshal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(marshalDirIgnore), 0o644)
}
