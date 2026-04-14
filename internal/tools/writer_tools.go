package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func writeFile(repoRoot, path, content string) error {
	abs, err := sandboxPath(repoRoot, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("write_file: mkdir: %w", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write_file: %w", err)
	}
	return nil
}

// editFile replaces lines [startLine, endLine] (1-indexed, inclusive) with newContent.
func editFile(repoRoot, path string, startLine, endLine int, newContent string) error {
	abs, err := sandboxPath(repoRoot, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("edit_file: read: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	n := len(lines)

	if startLine < 1 || endLine < startLine || startLine > n {
		return fmt.Errorf("edit_file: line range %d-%d out of bounds (file has %d lines)", startLine, endLine, n)
	}
	if endLine > n {
		endLine = n
	}

	var result []string
	result = append(result, lines[:startLine-1]...)
	if newContent != "" {
		result = append(result, strings.Split(newContent, "\n")...)
	}
	result = append(result, lines[endLine:]...)

	if err := os.WriteFile(abs, []byte(strings.Join(result, "\n")), 0o644); err != nil {
		return fmt.Errorf("edit_file: write: %w", err)
	}
	return nil
}
