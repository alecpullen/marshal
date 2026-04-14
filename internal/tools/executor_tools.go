package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sandboxPath resolves path relative to repoRoot and verifies the result
// stays within repoRoot (path traversal guard). Returns the absolute path.
// Absolute input paths are rejected — all paths must be relative to the repo root.
func sandboxPath(repoRoot, path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be relative to the repository root, not absolute", path)
	}
	abs := filepath.Clean(filepath.Join(repoRoot, path))
	cleanRoot := filepath.Clean(repoRoot)
	if abs != cleanRoot && !strings.HasPrefix(abs, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes repository root", path)
	}
	return abs, nil
}

func readFile(repoRoot, path string) (string, error) {
	abs, err := sandboxPath(repoRoot, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	return string(data), nil
}

func listDirectory(repoRoot, path string) (string, error) {
	abs, err := sandboxPath(repoRoot, path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("list_directory: %w", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			sb.WriteString(e.Name() + "/\n")
		} else {
			sb.WriteString(e.Name() + "\n")
		}
	}
	return sb.String(), nil
}

func searchCode(repoRoot, pattern, glob string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("search_code: invalid pattern %q: %w", pattern, err)
	}

	var sb strings.Builder
	matchCount := 0
	const maxMatches = 200

	err = filepath.Walk(repoRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			// Skip common non-source dirs
			if info.Name() == ".git" || info.Name() == "vendor" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		// Apply glob filter if provided
		if glob != "" {
			matched, err := filepath.Match(glob, info.Name())
			if err != nil || !matched {
				return nil
			}
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(repoRoot, p)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				sb.WriteString(fmt.Sprintf("%s:%d:%s\n", rel, i+1, line))
				matchCount++
				if matchCount >= maxMatches {
					sb.WriteString("... (truncated at 200 matches)\n")
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("search_code: walk error: %w", err)
	}
	return sb.String(), nil
}
