package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/tools/patch"
)

// PreviewPatchDiff dry-runs a raw search/replace patch proposal against the
// files currently on disk and returns the combined unified diff, without
// writing anything. Runner calls this before showing an approval prompt for
// file.write_patch so the TUI's Diff panel has something to render while the
// user is still deciding — the real apply-and-backup happens later, inside
// the file.write_patch tool handler itself, once the user approves.
func PreviewPatchDiff(workspaceRoot string, patchText string) (string, error) {
	patches, err := patch.Parse(patchText)
	if err != nil {
		return "", err
	}
	if len(patches) == 0 {
		return "", fmt.Errorf("no valid patches found in proposal")
	}

	var diffs []string
	for _, fp := range patches {
		path, err := safeWorkspacePath(workspaceRoot, fp.Path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read file %s: %w", fp.Path, err)
		}
		ok, err := patch.ValidatePatch(string(data), fp)
		if !ok || err != nil {
			return "", fmt.Errorf("patch validation failed for %s: %v", fp.Path, err)
		}
		diff, err := patch.GenerateDiff(fp.Path, string(data), fp)
		if err != nil {
			return "", err
		}
		diffs = append(diffs, diff)
	}
	return strings.Join(diffs, "\n\n"), nil
}

// safeWorkspacePath mirrors the workspace path-safety rules used by
// internal/tools/native (relative paths only, no ".." traversal). It is
// duplicated here rather than imported because native's resolver is
// unexported and this package must not import internal/tools/native.
func safeWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative: %s", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace root: %s", rel)
	}
	return filepath.Join(root, cleaned), nil
}
