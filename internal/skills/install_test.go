package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestInstallGitRejectsTraversalBundleName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	// A bundle directory whose name fails ValidName must be skipped by
	// installGit. On Unix a backslash is a legal filename character but
	// ValidName rejects it (it is a path separator on Windows), so it is
	// a real invalid bundle name that os.ReadDir can surface.
	src := t.TempDir()
	runGitTest(t, src, "init")
	runGitTest(t, src, "config", "user.email", "test@example.com")
	runGitTest(t, src, "config", "user.name", "Test")

	// Legitimate bundle that should be installed.
	goodDir := filepath.Join(src, "skills", "good")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "SKILL.md"), []byte("# Good\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Invalid-named bundle that must be skipped.
	badDir := filepath.Join(src, "skills", `bad\name`)
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("# Bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, src, "add", "-A")
	runGitTest(t, src, "commit", "-m", "initial")

	target := t.TempDir()
	if _, err := installGit(context.Background(), src, target, ""); err != nil {
		t.Fatalf("installGit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(target, "good", "SKILL.md")); err != nil {
		t.Fatalf("legitimate bundle not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, `bad\name`)); err == nil {
		t.Fatalf("invalid-named bundle was installed into target: %v", err)
	}
}

// A repo that lays its bundles out for several harnesses at once (the
// Runpod plugin repo uses plugins/<owner>/skills/<name>/SKILL.md) must
// still have every bundle discovered.
func TestInstallGitDiscoversNestedBundles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	src := t.TempDir()
	runGitTest(t, src, "init")
	runGitTest(t, src, "config", "user.email", "test@example.com")
	runGitTest(t, src, "config", "user.name", "Test")

	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(src, "plugins", "runpod", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: d\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A SKILL.md inside node_modules must not be discovered.
	noise := filepath.Join(src, "node_modules", "pkg")
	if err := os.MkdirAll(noise, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noise, "SKILL.md"), []byte("# noise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, src, "add", "-A")
	runGitTest(t, src, "commit", "-m", "initial")

	target := t.TempDir()
	if _, err := installGit(context.Background(), src, target, ""); err != nil {
		t.Fatalf("installGit: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(target, name, "SKILL.md")); err != nil {
			t.Errorf("bundle %q not installed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "pkg")); err == nil {
		t.Error("node_modules bundle was installed")
	}
}

// A clone with no discoverable bundle must fail with a clear message rather
// than installing nothing silently.
func TestInstallGitNoBundlesErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	src := t.TempDir()
	runGitTest(t, src, "init")
	runGitTest(t, src, "config", "user.email", "test@example.com")
	runGitTest(t, src, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# nothing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, src, "add", "-A")
	runGitTest(t, src, "commit", "-m", "initial")

	_, err := installGit(context.Background(), src, t.TempDir(), "")
	if err == nil {
		t.Fatal("expected an error for a clone with no bundles")
	}
	if !strings.Contains(err.Error(), "no SKILL.md bundles") {
		t.Errorf("error = %v, want it to mention no SKILL.md bundles", err)
	}
}

func TestInstallSingleFileRejectsInvalidName(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "source.md")
	if err := os.WriteFile(src, []byte("# test"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An invalid name containing a path separator should be rejected.
	_, err := installSingleFile(src, t.TempDir(), "../escape")
	if err == nil {
		t.Fatal("expected error for invalid skill name with path separator")
	}
}

func TestLooksLikeGitURL(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"github:owner/repo", true},
		{"git@github.com:owner/repo.git", true},
		{"https://github.com/owner/repo", true},
		{"https://github.com/owner/repo.git", true},
		{"http://example.com/repo", true},
		{"https://gitlab.com/owner/repo.git", true},
		{"./local/path/SKILL.md", false},
		{"/tmp/skill-bundle", false},
		{"SKILL.md", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			if got := looksLikeGitURL(tt.source); got != tt.want {
				t.Errorf("looksLikeGitURL(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}
