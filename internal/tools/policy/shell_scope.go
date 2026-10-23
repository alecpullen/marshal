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
	for _, r := range in.ReadRoots {
		if r == "" {
			continue
		}
		root, rerr := pathutil.ResolveAbsolute(r)
		if rerr != nil {
			root = filepath.Clean(r)
		}
		if isWithinRoot(resolved, root) {
			return ShellScopeReadRoot, resolved
		}
	}
	return ShellScopeExternal, resolved
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
