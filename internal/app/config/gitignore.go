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
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp .gitignore: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp .gitignore: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename .gitignore: %w", err)
	}
	return nil
}
