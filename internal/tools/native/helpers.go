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

// resolveNamedRoot resolves paths that may use a named alias prefix (e.g.
// "@run/task-1-brief.md"). If the path does not start with a known alias,
// it falls through to resolveWorkspacePathMulti for normal resolution.
// Unknown aliases are rejected; traversal outside an alias root is rejected.
func resolveNamedRoot(namedRoots map[string]string, root string, additionalRoots []string, rel string) (string, error) {
	for alias, aliasRoot := range namedRoots {
		prefix := alias + "/"
		if rel == alias {
			return aliasRoot, nil
		}
		if strings.HasPrefix(rel, prefix) {
			sub := rel[len(prefix):]
			return resolveWorkspacePathMulti(aliasRoot, nil, sub)
		}
	}
	if strings.HasPrefix(rel, "@") {
		return "", fmt.Errorf("unknown named alias in path %q", rel)
	}
	return resolveWorkspacePathMulti(root, additionalRoots, rel)
}

func limitOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + truncationMarker
}
