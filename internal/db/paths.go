package db

import (
	"os"
	"path/filepath"
)

// Path returns the Marshal database path for a working directory. When
// the directory sits inside a git repository the database anchors at the
// repository root, so a session opened from any subdirectory shares one
// project database.
func Path(workingDir string) string {
	return filepath.Join(dbRoot(workingDir), ".marshal", "marshal.db")
}

// dbRoot mirrors repo.Root: the walk is duplicated here because
// internal/repo imports internal/db, so calling repo.Root would create an
// import cycle. Non-git directories yield dir unchanged.
func dbRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	cur := abs
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return dir
		}
		cur = parent
	}
}
