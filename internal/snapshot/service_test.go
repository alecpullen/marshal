package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestService_TrackReturnsHash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
}

func TestService_DiffShowsChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := svc.Diff(context.Background(), hash)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !contains(diff, "-hello") || !contains(diff, "+world") {
		t.Fatalf("diff missing expected content: %s", diff)
	}
}

func TestService_RestoreReverts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := svc.Restore(context.Background(), hash); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestService_ExcludesLargeFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.bin"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 50, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	files, err := svc.runGitOut(context.Background(), "ls-tree", "-r", "--name-only", hash)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	if contains(files, "large.bin") {
		t.Fatalf("large.bin should not be in snapshot, got: %s", files)
	}
	if !contains(files, "small.txt") {
		t.Fatalf("small.txt should be in snapshot, got: %s", files)
	}
}

func TestService_DoesNotTouchProjectGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// init project repo
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.name", "Test").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "add", "-A").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-m", "init").Run(); err != nil {
		t.Fatal(err)
	}

	statusBefore, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())
	if _, err := svc.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}

	statusAfter, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(statusBefore) != string(statusAfter) {
		t.Fatalf("project git status changed after snapshot: before=%q after=%q", statusBefore, statusAfter)
	}
}

func TestService_Prune(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Wait so the snapshot commit is older than the prune cutoff.
	time.Sleep(1100 * time.Millisecond)

	if err := svc.Prune(context.Background(), 0); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	refs, err := svc.runGitOut(context.Background(), "for-each-ref", "--format=%(refname)", "refs/snapshots/")
	if err != nil {
		t.Fatalf("for-each-ref: %v", err)
	}
	if contains(refs, hash) {
		t.Fatal("expected old snapshot ref to be pruned")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func init() {
	_ = time.Now
}
