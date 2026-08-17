package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
