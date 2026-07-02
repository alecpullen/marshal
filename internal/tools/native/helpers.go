package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"marshal/internal/tools/registry"
)

const truncationMarker = "\n[output truncated]"

func decodeArgs[T any](tool registry.Tool, raw json.RawMessage) (T, error) {
	var zero T
	if err := registry.ValidateArgs(tool, raw); err != nil {
		return zero, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	return zero, nil
}

func resolveWorkspacePath(root string, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}

	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return root, nil
	}

	full := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return full, nil
}

func workspaceRel(root string, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return filepath.ToSlash(rel), nil
}

func limitOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + truncationMarker
}
