package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	// Verify symlink containment: even if the path is lexically under root,
	// a symlink component could point outside.
	if err := verifySymlinkContainment(root, full); err != nil {
		return "", err
	}
	return full, nil
}

// resolveWorkspacePathMulti resolves a relative path against the primary
// root and any additional roots. The primary root is checked first; if the
// path is within it, the absolute path under the primary root is returned.
// If it escapes the primary root, each additional root is tried. A path
// that escapes ALL roots is rejected.
func resolveWorkspacePathMulti(root string, additionalRoots []string, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}

	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return root, nil
	}

	// Collect all roots: primary first, then additional.
	roots := make([]string, 1+len(additionalRoots))
	roots[0] = root
	copy(roots[1:], additionalRoots)

	for _, r := range roots {
		full := filepath.Join(r, cleaned)
		relative, err := filepath.Rel(r, full)
		if err != nil {
			continue
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			if err := verifySymlinkContainment(r, full); err != nil {
				return "", err
			}
			return full, nil
		}
	}

	return "", fmt.Errorf("path %q escapes workspace", rel)
}

// verifySymlinkContainment checks that full (which is already known to be
// lexically under root) does not escape root through symlinks. For paths
// that do not yet exist (new file case), it resolves the parent directory
// and appends the leaf component before checking containment.
func verifySymlinkContainment(root, full string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// Resolve symlinks in root itself first so that the containment
	// check compares real (resolved) paths on both sides.
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return fmt.Errorf("resolve root %q: %w", absRoot, err)
	}
	absRoot = resolvedRoot

	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("resolve %q: %w", full, err)
		}
		// New file: walk up until we find an existing directory,
		// resolve that, then append the non-existing tail.
		resolved, err = resolveUpThenDown(full)
		if err != nil {
			return fmt.Errorf("resolve %q: %w", full, err)
		}
	}
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", full, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path %q resolves outside root", ErrPathEscapes, full)
	}
	return nil
}

// resolveUpThenDown walks up from path until it finds a directory that exists,
// resolves that through symlinks, then appends the non-existing suffix back.
func resolveUpThenDown(path string) (string, error) {
	var tail []string
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing parent in %q", path)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			// Found existing component; append remaining tail.
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
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
