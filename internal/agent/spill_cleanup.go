package agent

import (
	"os"
	"path/filepath"
)

// CleanupSpillFiles removes all .txt files from the spill directory. It is
// best-effort: a missing directory is not an error. Called at session end
// to prevent spill files from accumulating indefinitely in
// .marshal/tool-results/.
func CleanupSpillFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".txt" {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
	return nil
}
