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

	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}
