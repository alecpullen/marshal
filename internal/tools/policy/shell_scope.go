package policy

import (
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/pathutil"
)

// resolveShellScopes fills each operation's targets relative to its captured
// directory, applying any `git -C` override.
func (a *shellAnalyzer) resolveShellScopes() {
	for i := range a.ops {
		op := &a.ops[i]
		dir := a.in.Dir
		if len(op.Directories) > 0 {
			dir = op.Directories[0]
		}
		if gitDir, ok := gitDirectory(op, dir); ok {
			dir = gitDir
			op.Directories = []string{dir}
		}
		a.resolveTargets(op, dir)
	}
}

// cdTarget returns the cwd a `cd` operation leaves behind.
func cdTarget(op *ShellOperation, dir string) (string, bool) {
	if op.Executable.Value != "cd" || len(op.Operands) == 0 {
		return "", false
	}
	target := op.Operands[0]
	if !target.Known {
		return "", false
	}
	next := target.Value
	if !filepath.IsAbs(next) {
		next = filepath.Join(dir, next)
	}
	return filepath.Clean(next), true
}

// resolveTargets classifies every known operand of one operation.
func (a *shellAnalyzer) resolveTargets(op *ShellOperation, dir string) {
	for _, operand := range op.Operands {
		if !operand.Known {
			op.Targets = append(op.Targets, ShellTarget{Word: operand, Scope: ShellScopeUnknown, Effect: ShellUnknown})
			continue
		}
		path := operand.Value
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		scope, resolved := classifyShellPath(path, a.in)
		op.Targets = append(op.Targets, ShellTarget{Word: operand, Path: resolved, Scope: scope})
	}
}

// classifyShellPath resolves a path and classifies it against the workspace
// and read roots. A dangling symlink is unknown; a path that does not exist
// yet is still classifiable lexically against the workspace root.
func classifyShellPath(path string, in ShellInput) (ShellScope, string) {
	// A symlink whose target does not exist cannot be classified safely.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(path); err != nil {
			return ShellScopeUnknown, path
		}
	}
	resolved, err := pathutil.ResolveAbsolute(path)
	if err != nil {
		// The path does not exist yet (a file about to be created, or a
		// fixture root): classify it lexically rather than as unknown.
		resolved = filepath.Clean(path)
	}
	if in.WorkspaceRoot != "" {
		root, rerr := pathutil.ResolveAbsolute(in.WorkspaceRoot)
		if rerr != nil {
			root = filepath.Clean(in.WorkspaceRoot)
		}
		if isWithinRoot(resolved, root) {
			return ShellScopeWorkspace, resolved
		}
	}
	// A scratch root is ordinary working space the session may read and
	// write: the temporary directory it stages build output in, and its own
	// scratch area. It is checked before the read roots because it is the
	// more specific answer, and because a scratch root inside the project
	// checkout would otherwise be classified as the read-only reference.
	//
	// The home directory is never scratch space, wherever it happens to
	// live: a temporary directory that happens to contain it must not
	// become a way to reach a credential. A path the workspace or a read
	// root already covers is unaffected — only the scratch grant is
	// withheld.
	homeRoot := ""
	if in.HomeDir != "" {
		homeRoot, _ = pathutil.ResolveAbsolute(in.HomeDir)
		if homeRoot == "" {
			homeRoot = filepath.Clean(in.HomeDir)
		}
	}
	for _, r := range in.ScratchRoots {
		if r == "" {
			continue
		}
		root, rerr := pathutil.ResolveAbsolute(r)
		if rerr != nil {
			root = filepath.Clean(r)
		}
		if !isWithinRoot(resolved, root) {
			continue
		}
		// The root itself is not its own working space, and an operand that
		// names a pattern reaches past the exact area the grant was given for:
		// `rm -rf <temp>/*` is not the session's staging work, and a pattern
		// cannot be judged against the home-directory carve-out below.
		if resolved == root || hasGlobMeta(path) {
			continue
		}
		if homeRoot != "" && isWithinRoot(resolved, homeRoot) {
			continue
		}
		return ShellScopeScratch, resolved
	}
	for _, r := range in.ReadRoots {
		if r == "" {
			continue
		}
		root, rerr := pathutil.ResolveAbsolute(r)
		if rerr != nil {
			root = filepath.Clean(r)
		}
		if !isWithinRoot(resolved, root) {
			continue
		}
		// A scratch root is narrower than a read root: it is withheld from the
		// home directory above, and a root listed here too must not launder the
		// same path back in as a plain read root.
		if homeRoot != "" && withinAnyRoot(resolved, in.ScratchRoots) && isWithinRoot(resolved, homeRoot) {
			continue
		}
		return ShellScopeReadRoot, resolved
	}
	return ShellScopeExternal, resolved
}

// hasGlobMeta reports whether a path names a pattern rather than one file.
func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

// withinAnyRoot reports whether an already-resolved path lies inside one of
// the given roots.
func withinAnyRoot(path string, roots []string) bool {
	for _, r := range roots {
		if r == "" {
			continue
		}
		root, err := pathutil.ResolveAbsolute(r)
		if err != nil {
			root = filepath.Clean(r)
		}
		if isWithinRoot(path, root) {
			return true
		}
	}
	return false
}

// isWithinRoot reports whether path is root or a descendant of it.
func isWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// gitDirectory returns the directory a `git -C <dir>` chain targets, if any.
func gitDirectory(op *ShellOperation, dir string) (string, bool) {
	if op.Executable.Value != "git" {
		return "", false
	}
	argv := op.Argv
	for i := 1; i < len(argv); i++ {
		if argv[i].Value == "-C" && i+1 < len(argv) {
			next := argv[i+1].Value
			if !filepath.IsAbs(next) {
				next = filepath.Join(dir, next)
			}
			return filepath.Clean(next), true
		}
	}
	return "", false
}
