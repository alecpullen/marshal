package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// marshalIgnoreEntry is the line written into .gitignore.
const marshalIgnoreEntry = ".marshal/"

// EnsureMarshalIgnored appends .marshal/ to the repository's .gitignore
// when it is not already covered. Marshal writes its project database,
// logs, and config into .marshal/, and a committed config.toml would leak
// whatever a user pasted into it.
//
// Not a git repository is not an error: there is nothing to ignore.
func EnsureMarshalIgnored(workingDir string) error {
	if _, err := os.Stat(filepath.Join(workingDir, ".git")); err != nil {
		return nil
	}

	path := filepath.Join(workingDir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case ".marshal", ".marshal/", "/.marshal", "/.marshal/":
			return nil
		}
	}

	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += marshalIgnoreEntry + "\n"

	// Preserve the original file's permissions across the atomic rename so a
	// pre-existing .gitignore with non-default mode (e.g. 0600) is not reset
	// to 0644. New files default to 0644.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".gitignore.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp .gitignore: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp .gitignore: %w", err)
	}
	// Flush to disk before the rename so a crash cannot leave a zero-length
	// or partially-written .gitignore in place of the original.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp .gitignore: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp .gitignore: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod temp .gitignore: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename .gitignore: %w", err)
	}
	return nil
}
