package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestSeedSymlinkModeCreatesSymlink(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, "node_modules/.keep", "x")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{{Path: "node_modules", Mode: "symlink"}},
	}, g, src, dst)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	link := filepath.Join(dst, "node_modules")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("node_modules is %v, want a symlink", info.Mode())
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	if want, err := filepath.EvalSymlinks(filepath.Join(src, "node_modules")); err != nil || resolved != want {
		t.Fatalf("link resolves to %q, want %q (err=%v)", resolved, want, err)
	}
}

func TestSeedCopyModeCopiesFileAndDirRecursively(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, ".env", "SECRET=1\n")
	writeSeedFile(t, src, "cache/sub/deep.bin", "bytes")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{
			{Path: ".env", Mode: "copy"},
			{Path: "cache", Mode: "copy"},
		},
	}, g, src, dst)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if got := readSeedFile(t, dst, ".env"); got != "SECRET=1\n" {
		t.Fatalf(".env = %q", got)
	}
	if got := readSeedFile(t, dst, "cache/sub/deep.bin"); got != "bytes" {
		t.Fatalf("cache/sub/deep.bin = %q", got)
	}
	// Copies are disposable material: fixed modes, not inherited ones.
	info, err := os.Stat(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf(".env mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestSeedSkipsTrackedPathWithWarning(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, "tracked.txt", "x")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return false, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{{Path: "tracked.txt", Mode: "copy"}},
	}, g, src, dst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "git-tracked") {
		t.Fatalf("warnings = %v, want one git-tracked warning", warnings)
	}
	if _, err := os.Lstat(filepath.Join(dst, "tracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("tracked path was seeded anyway: %v", err)
	}
}

func TestSeedRejectsNonLocalPathWithWarning(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) {
		t.Errorf("CheckIgnore called for a non-local path %q", path)
		return true, nil
	}

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{
			{Path: "/etc/passwd", Mode: "copy"},
			{Path: "../escape", Mode: "copy"},
		},
	}, g, src, dst)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want two rejections", warnings)
	}
}

func TestSeedMissingSourceSkippedSilently(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{{Path: "node_modules", Mode: "symlink"}},
	}, g, src, dst)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none for a missing source", warnings)
	}
}

func TestSeedExistingDestinationSkippedWithWarning(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, ".env", "from-project")
	writeSeedFile(t, dst, ".env", "already-here")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{{Path: ".env", Mode: "copy"}},
	}, g, src, dst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "already exists") {
		t.Fatalf("warnings = %v, want one already-exists warning", warnings)
	}
	if got := readSeedFile(t, dst, ".env"); got != "already-here" {
		t.Fatalf("existing destination was overwritten: %q", got)
	}
}

func TestSeedCheckIgnoreErrorBecomesWarning(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, ".env", "x")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return false, os.ErrPermission }

	warnings := SeedIntoWorktree(config.WorktreeConfig{
		Seed: []config.WorktreeSeed{{Path: ".env", Mode: "copy"}},
	}, g, src, dst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "check-ignore") {
		t.Fatalf("warnings = %v, want one check-ignore warning", warnings)
	}
}

func TestSeedDocsArchiveSymlinked(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, ".docs-archive/plan.md", "# plan\n")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{}, g, src, dst)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	link := filepath.Join(dst, ".docs-archive")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".docs-archive is %v, want a symlink", info.Mode())
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	if want, err := filepath.EvalSymlinks(filepath.Join(src, ".docs-archive")); err != nil || resolved != want {
		t.Fatalf("link resolves to %q, want %q (err=%v)", resolved, want, err)
	}
}

func TestSeedDocsArchiveSkippedWhenSourceMissing(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{}, g, src, dst)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none for a missing .docs-archive", warnings)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".docs-archive")); !os.IsNotExist(err) {
		t.Fatalf(".docs-archive was created anyway: %v", err)
	}
}

func TestSeedDocsArchiveNoClobber(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, ".docs-archive/plan.md", "# plan\n")
	writeSeedFile(t, dst, ".docs-archive/existing.md", "existing\n")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return true, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{}, g, src, dst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], ".docs-archive") {
		t.Fatalf("warnings = %v, want one .docs-archive warning", warnings)
	}
	info, err := os.Lstat(filepath.Join(dst, ".docs-archive"))
	if err != nil {
		t.Fatalf("lstat destination: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("existing destination replaced with a symlink: %v", info.Mode())
	}
	if !info.IsDir() {
		t.Fatalf("existing destination is %v, want a real directory", info.Mode())
	}
	if got := readSeedFile(t, dst, ".docs-archive/existing.md"); got != "existing\n" {
		t.Fatalf("existing destination was modified: %q", got)
	}
}

func TestSeedDocsArchiveSkippedWhenTracked(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeSeedFile(t, src, ".docs-archive/plan.md", "# plan\n")
	g := NewFakeGitOps()
	g.CheckIgnoreFunc = func(dir, path string) (bool, error) { return false, nil }

	warnings := SeedIntoWorktree(config.WorktreeConfig{}, g, src, dst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "git-tracked") {
		t.Fatalf("warnings = %v, want one git-tracked warning", warnings)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".docs-archive")); !os.IsNotExist(err) {
		t.Fatalf("tracked .docs-archive was seeded anyway: %v", err)
	}
}

func writeSeedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSeedFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
