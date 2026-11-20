package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/pathutil"
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

// resolveWorkspacePathMulti resolves one target against the primary root and
// any additional roots, following symlinks and judging the resolved target
// against the roots that operation may touch. Additional roots are read/write
// grants; resolveWorkspacePathMultiRead is the read-only caller shape.
func resolveWorkspacePathMulti(root string, additionalRoots []string, rel string) (string, error) {
	return resolveGrants(root, legacyGrants(nil, additionalRoots), rel, true)
}

// resolveWorkspacePathMultiRead is the read-tool variant of
// resolveWorkspacePathMulti: an absolute path is accepted when it resolves —
// symlinks included — inside the primary root or any additional root. An
// absolute path contained in no root is rejected with an error naming the
// allowed roots.
func resolveWorkspacePathMultiRead(root string, additionalRoots []string, rel string) (string, error) {
	return resolveGrants(root, legacyGrants(nil, additionalRoots), rel, false)
}

// resolveGrants is the one adapter from the shared resolver to this package:
// every legacy entry point funnels through it so they all report an escape as
// ErrPathEscapes with the roots that were consulted.
func resolveGrants(primary string, grants []pathutil.RootGrant, rel string, write bool) (string, error) {
	if primary == "" {
		return "", fmt.Errorf("%w: no workspace root is bound", ErrPathEscapes)
	}
	// A missing primary root must fail loudly. The shared resolver completes a
	// non-existing tail through its nearest existing ancestor — the right
	// answer for a new file in a live root, but the wrong one for a root that
	// has been removed underneath the session (a dispatch worktree torn down
	// while its tools were still bound to it), which would otherwise let the
	// write land in whatever directory now contains that path.
	if _, err := os.Stat(primary); err != nil {
		return "", fmt.Errorf("%w: workspace root %q is unavailable: %w", ErrPathEscapes, primary, err)
	}
	resolved, err := pathutil.ResolveGrantedPath(primary, grants, rel, write)
	if err != nil {
		if errors.Is(err, pathutil.ErrOutsideRoots) {
			return "", fmt.Errorf("%w: %w", ErrPathEscapes, err)
		}
		return "", err
	}
	return resolved, nil
}

// legacyGrants turns the explicit legacy roots into grants. An additional root
// and a named alias are read/write; a registration that wants a narrower
// permit supplies it through Options.PathGrants, which appendGrant(narrow)ed
// over these.
func legacyGrants(namedRoots map[string]string, additionalRoots []string) []pathutil.RootGrant {
	grants := make([]pathutil.RootGrant, 0, len(additionalRoots)+len(namedRoots))
	for _, root := range additionalRoots {
		grants = appendGrant(grants, pathutil.RootGrant{Path: root, Read: true, Write: true}, false)
	}
	aliases := sortedAliases(namedRoots)
	for _, alias := range aliases {
		grants = appendGrant(grants, pathutil.RootGrant{Path: namedRoots[alias], Alias: alias, Read: true, Write: true}, false)
	}
	return grants
}

// sortedAliases returns the alias prefixes longest first, so a more specific
// alias ("@run/notes") wins over its prefix ("@run") regardless of map order.
func sortedAliases(namedRoots map[string]string) []string {
	aliases := make([]string, 0, len(namedRoots))
	for alias := range namedRoots {
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool {
		if len(aliases[i]) != len(aliases[j]) {
			return len(aliases[i]) > len(aliases[j])
		}
		return aliases[i] < aliases[j]
	})
	return aliases
}

// appendGrant adds g unless an equivalent root or alias is already granted. A
// narrowing g replaces that existing grant instead, which is how a caller's
// operation-specific permit overrides the legacy read/write default.
func appendGrant(grants []pathutil.RootGrant, g pathutil.RootGrant, narrow bool) []pathutil.RootGrant {
	for i, existing := range grants {
		sameAlias := g.Alias != "" && existing.Alias == g.Alias
		samePath := g.Alias == "" && existing.Alias == "" && sameGrantPath(existing.Path, g.Path)
		if !sameAlias && !samePath {
			continue
		}
		if narrow {
			grants[i] = g
		}
		return grants
	}
	return append(grants, g)
}

// sameGrantPath compares two roots, preferring the literal spelling and
// falling back to the resolved one so a symlinked root is recognised as itself.
func sameGrantPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && resolvedA == resolvedB
}

// resolveSystemPath resolves an absolute path for a system-access session.
// It is the system-mode counterpart to the granted resolver: containment is
// deliberately not checked, but the path is still symlink-resolved so callers
// get a canonical absolute path. A path that does not exist yet resolves
// through its nearest existing ancestor, matching the new-file case.
func resolveSystemPath(abs string) (string, error) {
	return pathutil.ResolveAbsolute(abs)
}

// resolveReadToolPath resolves a path for a read-side tool other than the
// file tools (today: repo.search's path filter). It shares the file tools'
// rules exactly — ~ expansion, the doubled-prefix diagnostic and the granted
// roots — so an agent that reads a path with file.read and then filters with
// repo.search gets the same answer from both. An absolute spelling alone does
// not require System access; it still has to land inside a readable root.
func (t *toolSet) resolveReadToolPath(rel string) (string, error) {
	return t.resolveToolPath(rel, true)
}

// doubledWorktreeError returns the diagnostic for a mistaken repeated
// .marshal/worktrees/<branch>/ segment, or nil when the joined target is not
// doubled or the literal entry really exists.
func (t *toolSet) doubledWorktreeError(rel string) error {
	return doubledWorktreeError(t.activeRoot(), rel)
}

// resolveToolPath is the single entry point for file-tool path resolution.
// With system access on, an absolute path resolves anywhere on the filesystem;
// otherwise one target is resolved — from the alias root that names it, else
// from the active workspace root — and judged against the roots this
// operation is granted. read selects the read side, which also accepts
// absolute paths contained in a permitted root and the Marshal-owned
// directories under $HOME.
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
	if derr := t.doubledWorktreeError(rel); derr != nil {
		return "", derr
	}
	resolved, err := resolveGrants(t.activeRoot(), t.grantedRoots(), rel, !read)
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

// resolvePathFunc is the registry.Tool.ResolvePath hook for this toolset. It
// is the same resolution the handlers use, so policy target preparation, patch
// previews and retry hints judge exactly the file the tool would touch.
func (t *toolSet) resolvePathFunc(_ context.Context, path string, write bool) (string, error) {
	return t.resolveToolPath(path, !write)
}

// resolveReadOnlyPathFunc is the hook for a tool that only ever reads: the
// caller's operation flag cannot widen a read-only surface into a write one.
func (t *toolSet) resolveReadOnlyPathFunc(_ context.Context, path string, _ bool) (string, error) {
	return t.resolveToolPath(path, true)
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

// grantedRoots returns the roots this toolset resolves against. The primary
// root is implicit (it is passed separately and stays readable and writable);
// here the explicit additional roots and named aliases are read/write grants,
// the project root of an active worktree is a read-only reference, and the
// registration's own PathGrants narrow any of those for the same root or
// alias.
//
// additionalRoots and namedRoots are set once at construction and never
// updated, so the worktree's project root is injected dynamically at
// resolution time.
func (t *toolSet) grantedRoots() []pathutil.RootGrant {
	grants := legacyGrants(t.namedRoots, t.additionalRoots)
	if ws := t.wsState(); ws != nil {
		w := ws.Workspace()
		if w.ActiveRoot != "" && w.ProjectRoot != "" && w.ActiveRoot != w.ProjectRoot {
			// A project checkout a worktree session may consult is a
			// reference: readable, never an implicit write target.
			grants = appendGrant(grants, pathutil.RootGrant{Path: w.ProjectRoot, Read: true}, false)
		}
	}
	for _, g := range t.pathGrants {
		grants = appendGrant(grants, g, true)
	}
	return grants
}

// resolveNamedRoot resolves paths that may use a named alias prefix (e.g.
// "@run/task-1-brief.md"). Kept as an adapter over the shared resolver so the
// legacy callers and the tools migrate together.
func resolveNamedRoot(namedRoots map[string]string, root string, additionalRoots []string, rel string) (string, error) {
	return resolveGrants(root, legacyGrants(namedRoots, additionalRoots), rel, true)
}

// resolveNamedRootRead is the read-tool variant of resolveNamedRoot.
func resolveNamedRootRead(namedRoots map[string]string, root string, additionalRoots []string, rel string) (string, error) {
	return resolveGrants(root, legacyGrants(namedRoots, additionalRoots), rel, false)
}

func limitOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + truncationMarker
}
