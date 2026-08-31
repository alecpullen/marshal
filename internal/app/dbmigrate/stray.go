package dbmigrate

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/repo"
)

// AdoptStrayProjectDB repairs databases created by subdirectory launches
// before project-root anchoring: when the repository-root database does
// not exist but a stray <root>/<subdir>/.marshal/marshal.db does, the
// stray database is moved to the root and the stray .marshal directory is
// removed. One directory level deep, best-effort: any error is logged and
// swallowed so startup never fails because of cleanup.
//
// A config.toml in the stray directory is left in place — it predates the
// root anchoring and the trust system still gates it by hash; only the
// database (sessions, history, symbols, knowledge) is adopted.
func AdoptStrayProjectDB(workingDir string, log *slog.Logger) error {
	root := repo.Root(workingDir)
	rootDB := filepath.Join(root, ".marshal", "marshal.db")
	if _, err := os.Stat(rootDB); err == nil {
		return nil // root DB already exists; nothing to adopt
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		strayDir := filepath.Join(root, entry.Name(), ".marshal")
		strayDB := filepath.Join(strayDir, "marshal.db")
		if _, err := os.Stat(strayDB); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(rootDB), 0o755); err != nil {
			return err
		}
		if err := os.Rename(strayDB, rootDB); err != nil {
			return err
		}
		// Remove the stray .marshal dir when it holds nothing but
		// machine-local state; keep it if the user stored anything else.
		if leftovers, lerr := os.ReadDir(strayDir); lerr == nil && len(leftovers) == 0 {
			_ = os.Remove(strayDir)
		}
		log.Info("adopted session database from subdirectory launch",
			"from", strayDB, "to", rootDB)
		return nil // one stray is enough; re-runs handle the rest
	}
	return nil
}
