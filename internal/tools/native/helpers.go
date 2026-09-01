package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/tools/registry"
)

const truncationMarker = "\n[output truncated]"

// pathDescription returns a JSON-encoded string literal for a path
// parameter's schema description, with a sentence about this toolset's
// alias prefixes appended when any are configured.
//
// Without this, a worker handed "@run/task-1-brief.md" reads a schema that
// says paths are workspace-relative, concludes the prefix is a placeholder,
// and goes hunting the filesystem for a directory named "run" instead of
// passing the path through verbatim.
func (t *toolSet) pathDescription(base string) string {
	encoded, err := json.Marshal(base + t.namedRootHint())
	if err != nil {
		return `"` + base + `"`
	}
	return string(encoded)
}

// namedRootHint describes the alias prefixes this toolset resolves, or ""
// when none are configured.
func (t *toolSet) namedRootHint() string {
	if len(t.namedRoots) == 0 {
		return ""
	}
	aliases := make([]string, 0, len(t.namedRoots))
	for alias := range t.namedRoots {
		aliases = append(aliases, `"`+alias+`/"`)
	}
	sort.Strings(aliases)
	return fmt.Sprintf(" A path starting with %s is a real, resolvable path"+
		" pointing at a provided directory outside the workspace: pass it"+
		" through verbatim, and do not strip the prefix or search the"+
		" workspace for a directory of that name.",
		strings.Join(aliases, " or "))
}

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

// resolveWorkspacePath resolves a relative path against the workspace root,
// verifying that the resolved path is contained within the root (including
// through symlinks). It rejects absolute paths and upward traversal.
func resolveWorkspacePath(root string, rel string) (string, error) {
	return SafeResolve(root, rel)
}

// resolveWorkspacePathMulti resolves a relative path against the primary
// root and any additional roots. Write semantics: absolute paths are
// always rejected. See resolveWorkspacePathMultiMode for the read-tool
// variant that accepts absolute paths contained in a root.
func resolveWorkspacePathMulti(root string, additionalRoots []string, rel string) (string, error) {
	return resolveWorkspacePathMultiMode(root, additionalRoots, rel, false)
}

// resolveWorkspacePathMultiRead is the read-tool variant of
// resolveWorkspacePathMulti: an absolute path is accepted when it
// resolves — symlinks included — inside the primary root or any
// additional root. An absolute path contained in no root is rejected
// with an error naming the allowed roots.
func resolveWorkspacePathMultiRead(root string, additionalRoots []string, rel string) (string, error) {
	return resolveWorkspacePathMultiMode(root, additionalRoots, rel, true)
}

func resolveWorkspacePathMultiMode(root string, additionalRoots []string, rel string, allowAbsolute bool) (string, error) {
	cleaned := filepath.Clean(rel)

	// Collect all roots: primary first, then additional.
	roots := make([]string, 1+len(additionalRoots))
	roots[0] = root
	copy(roots[1:], additionalRoots)

	if allowAbsolute && filepath.IsAbs(rel) {
		// Absolute path: accept only if it is contained within one of
		// the roots. resolveAbsolute (saferesolve.go) is the single
		// source of truth for symlink containment; per-root failures
		// (missing root, symlink escape) are collapsed into one error
		// that names the allowed roots so the model can retry.
		for _, r := range roots {
			absRoot, err := filepath.Abs(r)
			if err != nil {
				continue
			}
			resolvedRoot, err := filepath.EvalSymlinks(absRoot)
			if err != nil {
				continue
			}
			if resolved, err := resolveAbsolute(resolvedRoot, cleaned); err == nil {
				return resolved, nil
			}
		}
		return "", fmt.Errorf("%w: absolute path %q resolves outside all allowed roots [%s]",
			ErrPathEscapes, rel, strings.Join(roots, ", "))
	}

	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}
	if cleaned == "." {
		return root, nil
	}

	// First pass: lexical check. Pick the first root under which the
	// path is lexically contained. This intentionally allows `..` at the
	// start of rel — a path like `../siblingroot/file` is valid when
	// `siblingroot` is in additionalRoots. The symlink check below
	// will catch symlink-based escapes that the lexical check would miss.
	var lastLexical error
	for _, r := range roots {
		full := filepath.Join(r, cleaned)
		relToRoot, err := filepath.Rel(r, full)
		if err != nil {
			lastLexical = err
			continue
		}
		if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			lastLexical = fmt.Errorf("path %q escapes root %q", rel, r)
			continue
		}
		// Lexically contained. Now verify symlink containment via the
		// single source of truth (resolveAbsolute).
		absRoot, err := filepath.Abs(r)
		if err != nil {
			return "", err
		}
		resolvedRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return "", fmt.Errorf("resolve root %q: %w", absRoot, err)
		}
		return resolveAbsolute(resolvedRoot, full)
	}
	if lastLexical != nil {
		return "", lastLexical
	}
	return "", fmt.Errorf("path %q escapes workspace", rel)
}

func workspaceRel(root string, abs string) (string, error) {
	// Resolve symlinks in root so the rel computation matches the path
	// returned by resolveWorkspacePath (which is also symlink-resolved).
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	rel, err := filepath.Rel(resolvedRoot, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return filepath.ToSlash(rel), nil
}

// effectiveAdditionalRoots returns the configured additional roots, plus the
// project root when a worktree is active (i.e. the active root differs from
// the project root). This keeps the project root reachable from inside a
// worktree: an agent isolated in /project/.marshal/worktrees/feat-x can still
// read ../docs/architecture.md because the project root is an allowed root.
// additionalRoots is set once at construction and never updated, so the
// project root must be injected dynamically at resolution time.
func (t *toolSet) effectiveAdditionalRoots() []string {
	roots := t.additionalRoots
	if ws := t.wsState(); ws != nil {
		w := ws.Workspace()
		if w.ActiveRoot != "" && w.ProjectRoot != "" && w.ActiveRoot != w.ProjectRoot {
			roots = append(roots, w.ProjectRoot)
		}
	}
	return roots
}

// resolveNamedRoot resolves paths that may use a named alias prefix (e.g.
// "@run/task-1-brief.md") with write semantics: absolute paths are
// rejected. See resolveWorkspacePathMultiMode.
func resolveNamedRoot(namedRoots map[string]string, root string, additionalRoots []string, rel string) (string, error) {
	return resolveNamedRootMode(namedRoots, root, additionalRoots, rel, false)
}

// resolveNamedRootRead is the read-tool variant: the alias path gets the
// same absolute-path leniency as ordinary reads, because the alias root
// is itself an allowed root.
func resolveNamedRootRead(namedRoots map[string]string, root string, additionalRoots []string, rel string) (string, error) {
	return resolveNamedRootMode(namedRoots, root, additionalRoots, rel, true)
}

func resolveNamedRootMode(namedRoots map[string]string, root string, additionalRoots []string, rel string, allowAbsolute bool) (string, error) {
	for alias, aliasRoot := range namedRoots {
		prefix := alias + "/"
		if rel == alias {
			return aliasRoot, nil
		}
		if strings.HasPrefix(rel, prefix) {
			sub := rel[len(prefix):]
			return resolveWorkspacePathMultiMode(aliasRoot, nil, sub, allowAbsolute)
		}
	}
	if strings.HasPrefix(rel, "@") {
		return "", fmt.Errorf("unknown named alias in path %q", rel)
	}
	if allowAbsolute && filepath.IsAbs(rel) {
		// An absolute path in read mode may also live under an alias root,
		// which is itself an allowed root. Try each alias root before
		// falling through to the ordinary multi-root check.
		for _, aliasRoot := range namedRoots {
			if resolved, err := resolveWorkspacePathMultiMode(aliasRoot, nil, rel, true); err == nil {
				return resolved, nil
			}
		}
	}
	return resolveWorkspacePathMultiMode(root, additionalRoots, rel, allowAbsolute)
}

func limitOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + truncationMarker
}
