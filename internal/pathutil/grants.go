package pathutil

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrOutsideRoots is returned by ResolveGrantedPath when the resolved target
// lies outside every root the requested operation is permitted to touch. It is
// distinct from a malformed path: the target resolved fine, it simply is not
// granted. Tool layers translate it into their own escape sentinel.
var ErrOutsideRoots = errors.New("path is outside the permitted roots")

// RootGrant permits one root for one or both operations. Path is the root a
// target is resolved under; Alias, when set (e.g. "@run"), is an extra prefix
// the caller may use to address that same root. Read and Write say which
// operations the grant covers, so a project checkout a worktree may consult is
// a read grant and never an implicit write target.
type RootGrant struct {
	Path  string
	Alias string
	Read  bool
	Write bool
}

// ResolveGrantedPath resolves ONE target for ONE operation and returns the
// canonical, symlink-resolved absolute path.
//
// A path that starts with a granted alias is resolved from that alias's root;
// every other relative path is resolved from primary. The request is joined
// exactly once — a leading ".." is part of the request, not a rejection — and
// the single resulting target is then judged against the roots the operation
// grants. An absolute path resolves from itself. Because symlinks (on the
// target and on every root) are resolved BEFORE that judgement, a new file
// beneath a symlinked parent is checked against the root its bytes would
// really land in.
//
// primary is readable and writable. A grant is only usable for the operation
// whose flag it sets. On an ungranted target ErrOutsideRoots is returned,
// wrapped with the roots that were consulted.
func ResolveGrantedPath(primary string, grants []RootGrant, path string, write bool) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if primary == "" {
		return "", errors.New("primary root is required")
	}

	base := primary
	rel := path
	aliased := false
	for _, g := range grants {
		if g.Alias == "" || g.Path == "" {
			continue
		}
		slash := filepath.ToSlash(path)
		if slash != g.Alias && !strings.HasPrefix(slash, g.Alias+"/") {
			continue
		}
		base = g.Path
		rel = strings.TrimPrefix(slash, g.Alias)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = "."
		}
		aliased = true
		break
	}
	if !aliased && strings.HasPrefix(path, "@") {
		return "", fmt.Errorf("unknown named alias in path %q", path)
	}

	target := filepath.FromSlash(rel)
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	// Resolve first, judge second: nearest-existing-ancestor handling and
	// symlink resolution both belong to the target, not to the request.
	resolved, err := ResolveAbsolute(target)
	if err != nil {
		return "", err
	}

	roots := permittedRoots(primary, grants, write)
	for _, root := range roots {
		canonical, err := ResolveAbsolute(root)
		if err != nil {
			continue
		}
		if pathWithin(canonical, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: path %q resolves outside all allowed roots [%s]",
		ErrOutsideRoots, path, strings.Join(roots, ", "))
}

// permittedRoots lists the roots one operation may reach, primary first. It is
// also the answer an escape error reports, so it keeps the caller's own
// spelling of each root.
func permittedRoots(primary string, grants []RootGrant, write bool) []string {
	roots := make([]string, 0, len(grants)+1)
	seen := make(map[string]bool, len(grants)+1)
	add := func(root string) {
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	add(primary)
	for _, g := range grants {
		if write && !g.Write {
			continue
		}
		if !write && !g.Read {
			continue
		}
		add(g.Path)
	}
	return roots
}

// pathWithin reports whether the already symlink-resolved target lies inside
// the already symlink-resolved root. A filepath.Rel failure is not contained,
// so an unjudgeable pair fails closed.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
