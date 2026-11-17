// internal/pipeline/containment.go
package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"

	"marshal/internal/pathutil"
)

// checkPlanPath validates a plan-supplied path lexically: it must be a
// non-empty, relative path that does not traverse above the worktree. It
// performs no filesystem access, so it can run at compile time, before a
// worktree exists.
//
// This is the first of the pipeline's two containment gates. It cannot catch
// a symlink escape — nothing on disk is consulted — so resolvePlanPath is
// still required at preflight and immediately before every mutation.
func checkPlanPath(rel string) error {
	if strings.TrimSpace(rel) == "" {
		return fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("path must be relative to the worktree: %s", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return fmt.Errorf("path must name a file inside the worktree: %s", rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the worktree: %s", rel)
	}
	return nil
}

// resolvePlanPath resolves a plan-supplied path against root and verifies
// that the symlink-resolved target stays inside root. It returns the resolved
// absolute path to use for the filesystem operation, so a write lands on the
// real target rather than through a symlink hop.
//
// root is made absolute before delegating: pathutil.ResolveWithinRoots
// compares an absolute root against the resolved target, so a relative root
// would report a legitimate path as an escape.
func resolvePlanPath(root, rel string) (string, error) {
	if err := checkPlanPath(rel); err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve worktree root %q: %w", root, err)
	}
	resolved, err := pathutil.ResolveWithinRoots(absRoot, nil, rel)
	if err != nil {
		return "", fmt.Errorf("path escapes the worktree: %w", err)
	}
	return resolved, nil
}
