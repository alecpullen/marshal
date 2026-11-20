package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/pathutil"
	"marshal/internal/tools/patch"
)

// PreviewPatchDiff dry-runs a raw search/replace patch proposal against the
// files currently on disk and returns the combined unified diff, without
// writing anything. Runner calls this before showing an approval prompt for
// file.write_patch so the TUI's Diff panel has something to render while the
// user is still deciding — the real apply-and-backup happens later, inside
// the file.write_patch tool handler itself, once the user approves.
func PreviewPatchDiff(workspaceRoot string, patchText string, system bool) (string, error) {
	patches, err := patch.Parse(patchText)
	if err != nil {
		return "", err
	}
	if len(patches) == 0 {
		return "", fmt.Errorf("no valid patches found in proposal")
	}
	return PreviewFilePatches(workspaceRoot, patches, system)
}

// PreviewFilePatches dry-runs already-parsed patches against the files
// currently on disk and returns the combined unified diff, writing nothing.
// It is the shared preview path for file.write_patch and file.edit, so a
// caller never has to round file.edit's structured fields through patch text
// just to render a diff.
//
// workspaceRoot is the explicit root a caller with no tool binding has. The
// runner resolves through the tool's own resolver instead — see
// PreviewResolvedFilePatches — so a worktree session previews the copy the
// tool would really edit.
func PreviewFilePatches(workspaceRoot string, patches []patch.FilePatch, system bool) (string, error) {
	return PreviewResolvedFilePatches(func(rel string) (string, error) {
		if system && filepath.IsAbs(rel) {
			// System access: absolute paths anywhere on the filesystem are
			// previewable. No containment check — the approval prompt is the
			// gate, and the diff is what the user decides on.
			abs, err := filepath.Abs(rel)
			if err != nil {
				return "", fmt.Errorf("resolve absolute path %s: %w", rel, err)
			}
			return filepath.Clean(abs), nil
		}
		return pathutil.SafeWorkspacePath(workspaceRoot, rel)
	}, patches)
}

// PreviewResolvedFilePatches dry-runs already-parsed patches against the
// files resolve names and returns the combined unified diff, writing nothing.
// resolve is the write tool's own path resolver (registry.Tool.ResolvePath
// bound for a write), so the preview reads the same target the tool would
// touch: the bound worktree copy, a named artifact alias, or a permitted
// absolute path. A path the resolver refuses fails the preview rather than
// falling back to a same-named file under the launch directory.
func PreviewResolvedFilePatches(resolve func(string) (string, error), patches []patch.FilePatch) (string, error) {
	var diffs []string
	for _, fp := range patches {
		path, err := resolve(fp.Path)
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
