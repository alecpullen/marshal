package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func analyzeScope(t *testing.T, cmd, root string) ShellAnalysis {
	t.Helper()
	a, err := AnalyzeShell(ShellInput{Command: cmd, WorkspaceRoot: root, Dir: root})
	if err != nil {
		t.Fatalf("AnalyzeShell(%q): %v", cmd, err)
	}
	return a
}

func opDirs(a ShellAnalysis, name string) []string {
	var out []string
	for _, op := range a.Operations {
		if op.Executable.Value == name && len(op.Directories) > 0 {
			out = append(out, op.Directories[0])
		}
	}
	return out
}

func TestShellScopeCdSuccessFailureAndIsolation(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// && consumes success: rm runs in sub.
	a := analyzeScope(t, "cd sub && rm x", root)
	if dirs := opDirs(a, "rm"); len(dirs) != 1 || dirs[0] != sub {
		t.Fatalf("cd && rm dirs = %v, want [%s]", dirs, sub)
	}

	// || consumes failure: rm runs in the original root.
	a = analyzeScope(t, "cd sub || rm x", root)
	if dirs := opDirs(a, "rm"); len(dirs) != 1 || dirs[0] != root {
		t.Fatalf("cd || rm dirs = %v, want [%s]", dirs, root)
	}

	// A subshell's cd does not leak to the parent.
	a = analyzeScope(t, "(cd sub; rm x); rm y", root)
	dirs := opDirs(a, "rm")
	if len(dirs) != 2 || dirs[0] != sub || dirs[1] != root {
		t.Fatalf("subshell rm dirs = %v, want [%s %s]", dirs, sub, root)
	}
}

func TestShellScopeGitDirectoryOptions(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	a := analyzeScope(t, "git -C other status", root)
	if dirs := opDirs(a, "git"); len(dirs) != 1 || dirs[0] != other {
		t.Fatalf("git -C dirs = %v, want [%s]", dirs, other)
	}
}

func TestShellScopeActiveLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := AnalyzeShell(ShellInput{Command: "rm x", WorkspaceRoot: wt, Dir: wt})
	if err != nil {
		t.Fatalf("AnalyzeShell: %v", err)
	}
	found := false
	for _, op := range a.Operations {
		if op.Executable.Value != "rm" {
			continue
		}
		for _, target := range op.Targets {
			if target.Scope == ShellScopeWorkspace {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("active worktree target not classified as workspace: %+v", a.Operations)
	}
}

func TestShellScopeSymlinkAndMissingDescendant(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	a := analyzeScope(t, "rm link/x", root)
	scope := targetScope(a, "rm")
	if scope != ShellScopeExternal {
		t.Fatalf("symlink target scope = %q, want external", scope)
	}

	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	a = analyzeScope(t, "rm dangling", root)
	if scope := targetScope(a, "rm"); scope != ShellScopeUnknown {
		t.Fatalf("dangling symlink scope = %q, want unknown", scope)
	}
}

func TestShellScopeMixedAndUnknownTargets(t *testing.T) {
	root := t.TempDir()
	a := analyzeScope(t, "cp inside $UNKNOWN", root)
	var scopes []ShellScope
	for _, op := range a.Operations {
		if op.Executable.Value != "cp" {
			continue
		}
		for _, target := range op.Targets {
			scopes = append(scopes, target.Scope)
		}
	}
	if len(scopes) != 2 {
		t.Fatalf("cp targets = %v, want 2", scopes)
	}
	if scopes[0] != ShellScopeWorkspace || scopes[1] != ShellScopeUnknown {
		t.Fatalf("cp scopes = %v, want [workspace unknown]", scopes)
	}
}

func TestShellScopeReadExceptionsNeverGrantWrites(t *testing.T) {
	root := t.TempDir()
	readRoot := t.TempDir()
	a, err := AnalyzeShell(ShellInput{
		Command:       "cat " + filepath.Join(readRoot, "x"),
		WorkspaceRoot: root,
		Dir:           root,
		ReadRoots:     []string{readRoot},
	})
	if err != nil {
		t.Fatalf("AnalyzeShell: %v", err)
	}
	if scope := targetScope(a, "cat"); scope != ShellScopeReadRoot {
		t.Fatalf("read-root target scope = %q, want read_root", scope)
	}
}

func targetScope(a ShellAnalysis, name string) ShellScope {
	for _, op := range a.Operations {
		if op.Executable.Value != name {
			continue
		}
		for _, target := range op.Targets {
			return target.Scope
		}
	}
	return ShellScopeNone
}
