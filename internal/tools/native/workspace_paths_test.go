package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// jsonArgs encodes a tool-call argument object for invokeTool.
func jsonArgs(m map[string]string) (string, error) {
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// containedRoot returns a symlink-resolved temp dir, so the paths the
// resolvers return compare equal to the ones a test spells.
func containedRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return root
}

// worktreeSession puts the session in a real worktree of project and returns
// the active root's path.
func worktreeSession(t *testing.T, project, branch string) (string, *session.State) {
	t.Helper()
	worktree := filepath.Join(project, ".marshal", "worktrees", branch)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", worktree, err)
	}
	state := session.New(config.Default(), project, time.Unix(100, 0), session.Persistence{})
	state.SetWorkspace(session.Workspace{ProjectRoot: project, ActiveRoot: worktree, Branch: branch})
	return worktree, state
}

// An absolute spelling of a contained path is a writable target without System
// access, and it is the same canonical target the relative spelling names.
func TestAbsoluteContainedWrite(t *testing.T) {
	root := containedRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ts := newToolSetForTest(t, Options{WorkspaceRoot: root})
	if ts.systemAccess() {
		t.Fatal("fixture must not have system access")
	}

	abs := filepath.Join(root, "sub", "written.txt")
	fromAbsolute, err := ts.resolveToolPath(abs, false)
	if err != nil {
		t.Fatalf("absolute resolution: %v", err)
	}
	fromRelative, err := ts.resolveToolPath(filepath.Join("sub", "written.txt"), false)
	if err != nil {
		t.Fatalf("relative resolution: %v", err)
	}
	if fromAbsolute != fromRelative {
		t.Fatalf("absolute %q and relative %q disagree", fromAbsolute, fromRelative)
	}
	if fromAbsolute != abs {
		t.Fatalf("canonical target = %q, want %q", fromAbsolute, abs)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	args, err := jsonArgs(map[string]string{"path": abs, "content": "absolute body\n"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", args); err != nil {
		t.Fatalf("file.write by absolute path: %v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "absolute body\n" {
		t.Fatalf("content = %q, want the written body", string(data))
	}

	// The registry hook hands policy and previews the same target.
	tool, ok := reg.Lookup("file.write")
	if !ok {
		t.Fatal("file.write not registered")
	}
	if tool.ResolvePath == nil {
		t.Fatal("file.write must carry a path resolver")
	}
	hooked, err := tool.ResolvePath(context.Background(), abs, true)
	if err != nil {
		t.Fatalf("tool ResolvePath: %v", err)
	}
	if hooked != abs {
		t.Fatalf("tool ResolvePath = %q, want %q", hooked, abs)
	}
}

// A path that repeats the worktree the session is standing in joins into a
// nested duplicate tree. The write is refused with the useful relative
// suggestion, and nothing is created.
func TestSingleWorktreePrefixIsDiagnosed(t *testing.T) {
	project := containedRoot(t)
	worktree, state := worktreeSession(t, project, "a")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: worktree,
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	args, err := jsonArgs(map[string]string{
		"path":    ".marshal/worktrees/a/new.txt",
		"content": "nested\n",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = invokeTool(t, reg, "file.write", args)
	if err == nil {
		t.Fatal("a repeated worktree prefix was written instead of diagnosed")
	}
	if !strings.Contains(err.Error(), "duplicated worktree segment") {
		t.Fatalf("error should explain the duplicated segment, got: %v", err)
	}
	if !strings.Contains(err.Error(), "new.txt") {
		t.Fatalf("error should suggest the relative path, got: %v", err)
	}

	nestedPath, statErr := os.Stat(filepath.Join(worktree, ".marshal", "worktrees", "a", "new.txt"))
	if !os.IsNotExist(statErr) {
		t.Fatalf("nested duplicate was created: %v (%v)", nestedPath, statErr)
	}
}

// The diagnostic yields to reality: a workspace that genuinely nests that
// layout stays addressable, and the write lands where the caller spelled it
// rather than being rewritten.
func TestLiteralNestedWorktreePathSurvives(t *testing.T) {
	project := containedRoot(t)
	worktree, state := worktreeSession(t, project, "a")

	nested := filepath.Join(worktree, ".marshal", "worktrees", "a")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	literal := filepath.Join(nested, "note.md")
	writeFile(t, literal, "old body\n")

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: worktree,
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	patch := "File: .marshal/worktrees/a/note.md\n<<<<<<< SEARCH\nold body\n=======\nnew body\n>>>>>>> REPLACE"
	args, err := jsonArgs(map[string]string{"patch": patch})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write_patch", args); err != nil {
		t.Fatalf("a genuinely nested path was rejected: %v", err)
	}

	data, err := os.ReadFile(literal)
	if err != nil {
		t.Fatalf("read literal nested file: %v", err)
	}
	if string(data) != "new body\n" {
		t.Fatalf("literal nested file = %q, want the edit", string(data))
	}
	if _, statErr := os.Stat(filepath.Join(worktree, "note.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the request was rewritten to the collapsed path: %v", statErr)
	}
}

// A scoped view judges the target the tool would really write. An absolute
// spelling, an alias traversal or a traversal out of the declared allowlist
// is refused even though the tool itself would permit the path — while the
// in-scope spellings still work.
func TestScopedViewsCannotEscapeWithAbsolutePath(t *testing.T) {
	root := containedRoot(t)
	artifacts := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		NamedRoots:    map[string]string{"@run": artifacts},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	artifactView := registry.ArtifactWriterView(reg, "@run")
	artifactCases := []struct {
		path  string
		allow bool
	}{
		{"@run/verdict.md", true},
		{filepath.Join(artifacts, "absolute-verdict.md"), true},
		{"@run/../app.go", false},
		{filepath.Join(root, "app.go"), false},
	}
	for _, tc := range artifactCases {
		args, err := jsonArgs(map[string]string{"path": tc.path, "content": "body\n"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := invokeTool(t, artifactView, "file.write", args); (err == nil) != tc.allow {
			t.Errorf("artifact view on %q: err = %v, want allowed=%v", tc.path, err, tc.allow)
		}
	}

	fallbackView := registry.FallbackWriterView(reg, []string{"internal/foo"})
	fallbackCases := []struct {
		path  string
		allow bool
	}{
		{"internal/foo/x.go", true},
		{filepath.Join(root, "internal", "foo", "y.go"), true},
		{"internal/foo/../other/z.go", false},
		{"internal/other/z.go", false},
		{filepath.Join(root, "internal", "other", "z.go"), false},
	}
	for _, tc := range fallbackCases {
		args, err := jsonArgs(map[string]string{"path": tc.path, "content": "body\n"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := invokeTool(t, fallbackView, "file.write", args); (err == nil) != tc.allow {
			t.Errorf("fallback view on %q: err = %v, want allowed=%v", tc.path, err, tc.allow)
		}
	}
}
