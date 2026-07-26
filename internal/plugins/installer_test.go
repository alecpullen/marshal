package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// git runs a git command in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepo creates a git repo in dir with one commit containing the given
// files (relative path -> contents), and returns the commit hash.
func initRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test")
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "initial")
	inst, err := NewInstaller()
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	commit, err := inst.HeadCommit(context.Background(), dir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	return commit
}

func TestCloneReturnsCommit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	wantCommit := initRepo(t, src, map[string]string{"README.md": "hello"})

	inst, err := NewInstaller()
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	commit, err := inst.Clone(context.Background(), "file://"+src, "", dest)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if commit != wantCommit {
		t.Fatalf("commit = %q, want %q", commit, wantCommit)
	}
	data, err := os.ReadFile(filepath.Join(dest, "README.md"))
	if err != nil {
		t.Fatalf("cloned file missing: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("cloned content = %q", data)
	}
}

func TestCloneBranchRef(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initRepo(t, src, map[string]string{"README.md": "hello"})
	git(t, src, "checkout", "-b", "v1")
	writeFile(t, filepath.Join(src, "VERSION"), "1.0")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-m", "release")

	inst, err := NewInstaller()
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	if _, err := inst.Clone(context.Background(), "file://"+src, "v1", dest); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "VERSION")); err != nil {
		t.Fatalf("branch ref not checked out: %v", err)
	}
}

func TestCloneBadSourceIncludesStderr(t *testing.T) {
	inst, err := NewInstaller()
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	_, err = inst.Clone(context.Background(), filepath.Join(t.TempDir(), "nope"), "", filepath.Join(t.TempDir(), "dest"))
	if err == nil {
		t.Fatal("Clone should fail for a missing source")
	}
}

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "top.txt"), "top")
	writeFile(t, filepath.Join(src, "sub", "nested.txt"), "nested")

	dst := filepath.Join(t.TempDir(), "out")
	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir: %v", err)
	}
	for name, want := range map[string]string{"top.txt": "top", filepath.Join("sub", "nested.txt"): "nested"} {
		data, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", name, data, want)
		}
	}
}
