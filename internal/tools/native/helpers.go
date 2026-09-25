package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

	// The request is ONE path, so exactly one root claims it: the first root
	// under which it is lexically contained. This intentionally allows `..`
	// at the start of rel — a path like `../siblingroot/file` is valid when
	// `siblingroot` is in additionalRoots.
	var lastLexical error
	var chosenRoot string
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
		chosenRoot = r
		break
	}
	if chosenRoot == "" {
		if lastLexical != nil {
			return "", lastLexical
		}
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}

	absRoot, err := filepath.Abs(chosenRoot)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", absRoot, err)
	}

	// Resolve the target ONCE, then decide containment.
	resolved, err := resolveTarget(filepath.Join(chosenRoot, cleaned))
	if err != nil {
		return "", err
	}
	if containsPath(resolvedRoot, resolved) {
		return resolved, nil
	}

	// A symlink took the target outside the root that owns this path. A
	// different root may still contain that SAME target — a worktree's
	// seeded .docs-archive is lexically inside the worktree but resolves
	// into the project root — so weigh the resolved target against the
	// remaining roots.
	//
	// It must be the resolved target that is weighed, never a re-join of
	// the request to another root: re-joining fabricates a different file
	// that merely shares the name, so a worktree link to an external file
	// would silently answer from a same-named path in the project root (and
	// write there too, inside what should be an isolated checkout).
	for _, r := range roots {
		if r == chosenRoot {
			continue
		}
		rootAbs, aerr := filepath.Abs(r)
		if aerr != nil {
			continue
		}
		rootResolved, rerr := filepath.EvalSymlinks(rootAbs)
		if rerr != nil {
			continue
		}
		if containsPath(rootResolved, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: path %q resolves outside root", ErrPathEscapes, resolved)
}

// resolveSystemPath resolves an absolute path for a system-access session.
// It is the system-mode counterpart to resolveAbsolute: containment is
// deliberately not checked, but the path is still symlink-resolved so
// callers get a canonical absolute path. A path that does not exist yet
// resolves through its nearest existing ancestor, matching SafeResolve's
// new-file behavior.
func resolveSystemPath(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", abs, err)
		}
		resolved, err = resolveUpThenDown(abs)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", abs, err)
		}
	}
	return resolved, nil
}

// resolveReadToolPath resolves a path for a read-side tool other than the
// file tools (repo.search, csv.inspect, json.query). With system access on it
// accepts absolute paths anywhere on the filesystem, matching the file tools
// and the system-access prompt directive. Without the flag the prior
// containment behavior is preserved exactly.
func (t *toolSet) resolveReadToolPath(rel string) (string, error) {
	if t.systemAccess() {
		return t.resolveToolPath(rel, true)
	}
	// These tools take a path FILTER, not a workspace path, so they keep the
	// write-side containment rules (absolute paths rejected). They do share
	// the file tools' ~ expansion and doubled-prefix diagnostic, so an agent
	// that reads a path with file.read and then filters with repo.search gets
	// the same answer from both instead of a colder not-found.
	expanded, err := expandHomeDir(rel)
	if err != nil {
		return "", err
	}
	rel = expanded
	if derr := t.doubledWorktreeError(rel); derr != nil {
		return "", derr
	}
	return resolveNamedRoot(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), rel)
}

// doubledWorktreeError returns the diagnostic for a mistaken repeated
// .marshal/worktrees/<branch>/ prefix, or nil when the path is not doubled or
// the literal path really exists. Shared by every path entry point so the file
// tools and the other read tools answer alike.
func (t *toolSet) doubledWorktreeError(rel string) error {
	suggestion, ok := detectDoubledWorktree(rel)
	if !ok {
		return nil
	}
	// The suggestion only fires when the literal path is not real, so a
	// workspace that genuinely nests that layout (a doc tree describing the
	// on-disk structure, say) stays readable instead of being rejected.
	probe := rel
	if !filepath.IsAbs(probe) {
		probe = filepath.Join(t.activeRoot(), rel)
	}
	// Lstat, not Stat: a dangling symlink is still a real entry at the
	// literal path, so it must not be second-guessed as a typo.
	if _, err := os.Lstat(probe); err == nil {
		return nil
	}
	return fmt.Errorf("%w: path %q contains a duplicated worktree segment; relative paths resolve from the current worktree root — did you mean %q?", ErrPathEscapes, rel, suggestion)
}

// resolveToolPath is the single entry point for file-tool path resolution.
// With system access on, an absolute path resolves anywhere on the
// filesystem; otherwise the existing containment rules apply unchanged.
// Relative paths behave identically in both modes. read selects the
// read-tool variant, which additionally accepts absolute paths contained in
// an allowed root even without system access.
func (t *toolSet) resolveToolPath(rel string, read bool) (string, error) {
	// Propagate rather than swallow: a failure here means ~ could not be
	// expanded, and silently treating "~/x" as a relative path named "~"
	// hides the real cause behind a confusing not-found.
	expanded, err := expandHomeDir(rel)
	if err != nil {
		return "", err
	}
	rel = expanded
	if t.systemAccess() && filepath.IsAbs(rel) {
		return resolveSystemPath(filepath.Clean(rel))
	}
	// A repeated .marshal/worktrees/<branch>/ segment is a mistaken prefix,
	// not a real path. This diagnostic previously lived only in SafeResolve,
	// which file.* does not route through — they go via the named-root and
	// multi-root resolvers below — so they answered with a bare not-found
	// and the model had no way to learn it had prefixed the root itself.
	//
	if derr := t.doubledWorktreeError(rel); derr != nil {
		return "", derr
	}
	var resolved string
	if read {
		resolved, err = resolveNamedRootRead(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), rel)
	} else {
		resolved, err = resolveNamedRoot(t.namedRoots, t.activeRoot(), t.effectiveAdditionalRoots(), rel)
	}
	if err != nil {
		if errors.Is(err, ErrPathEscapes) {
			// Read-side only: Marshal-owned paths under $HOME are readable
			// without a workspace override. Writes never take this branch.
			// The target is resolved BEFORE the allowlist decision and the
			// resolved path is what gets returned, so a symlink planted
			// inside an allowlisted directory cannot pivot the read
			// elsewhere on disk.
			if read && filepath.IsAbs(rel) {
				if target, rerr := resolveSystemPath(filepath.Clean(rel)); rerr == nil &&
					IsReadAllowedOutsideWorkspace(target) {
					return target, nil
				}
			}
			return "", fmt.Errorf("%w; Marshal-owned paths readable outside the workspace: %s",
				err, strings.Join(readAllowedUnderHome, ", "))
		}
		return "", err
	}
	return resolved, nil
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
