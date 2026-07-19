package db

import "path/filepath"

// Path returns the Marshal database path for a working directory.
func Path(workingDir string) string {
	return filepath.Join(workingDir, ".marshal", "marshal.db")
}
