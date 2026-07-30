package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeNestedIgnoredLargeFile builds the tree shape that broke snapshots in
// practice: a subdirectory carrying its own .gitignore that ignores a
// directory holding an oversized file. Git honors .gitignore at every level;
// anything that reads only the root .gitignore does not.
func writeNestedIgnoredLargeFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, ".kilo")
	if err := os.MkdirAll(filepath.Join(nested, "node_modules", "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".gitignore"), []byte("node_modules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(nested, "node_modules", "pkg", "bundle.js")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 4096)), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestTrackSucceedsWithNestedGitignoredLargeFile is the regression gate for
// silently broken rollback protection. largeFileExcludes emitted a
// ":(exclude)<path>" pathspec for a file git considers ignored, and git add
// exits 1 when a pathspec names an ignored path — so every pre-write snapshot
// failed, leaving nothing to roll back to.
func TestTrackSucceedsWithNestedGitignoredLargeFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := writeNestedIgnoredLargeFile(t)

	// maxFile below the large file's size, so it becomes an exclude candidate.
	svc := New(t.TempDir(), dir, 1024, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty snapshot hash")
	}
}

// TestLargeFileExcludesSkipsGitIgnoredPaths pins the root cause directly: the
// exclude list must never name a path git ignores, whatever ignore file the
// rule came from.
func TestLargeFileExcludesSkipsGitIgnoredPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := writeNestedIgnoredLargeFile(t)

	svc := New(t.TempDir(), dir, 1024, []string{}, testLogger())
	// ensureRepo/refreshExclude must run first: the shadow repo is what
	// answers ignore questions.
	if err := svc.ensureRepo(context.Background()); err != nil {
		t.Fatalf("ensureRepo: %v", err)
	}
	if err := svc.refreshExclude(context.Background()); err != nil {
		t.Fatalf("refreshExclude: %v", err)
	}

	excludes, err := svc.largeFileExcludes(context.Background())
	if err != nil {
		t.Fatalf("largeFileExcludes: %v", err)
	}
	for _, ex := range excludes {
		if strings.Contains(ex, "node_modules") {
			t.Errorf("exclude list names a git-ignored path %q; git add will reject it", ex)
		}
	}
}

// TestGitOutputSuffixSurfacesCause pins that git's own message reaches the
// error. Callers log these at Warn while raw output is Debug-only, so a bare
// "exit status 1" is undiagnosable.
func TestGitOutputSuffixSurfacesCause(t *testing.T) {
	if got := gitOutputSuffix(nil); got != "" {
		t.Errorf("gitOutputSuffix(nil) = %q, want empty", got)
	}
	got := gitOutputSuffix([]byte("The following paths are ignored:\n.kilo/node_modules\n"))
	if !strings.Contains(got, "paths are ignored") {
		t.Errorf("gitOutputSuffix() = %q, want git's message", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("gitOutputSuffix() = %q, want newlines flattened for one-line logs", got)
	}
	long := gitOutputSuffix([]byte(strings.Repeat("z", 500)))
	if len(long) > 320 {
		t.Errorf("gitOutputSuffix() len = %d, want capped", len(long))
	}
}
