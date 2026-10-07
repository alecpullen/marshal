package snapshot

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
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

// serviceTrackedPaths lists the paths in a published snapshot, using Git itself
// against the generation that retains the hash. It is the new-path equivalent
// of the old `svc.runGitOut("ls-tree", …)`: the assertion is about what Git
// reads out of the STORE, not about Marshal's own bookkeeping.
func serviceTrackedPaths(t *testing.T, svc *Service, hash string) []string {
	t.Helper()
	m, err := NewManager(svc.DataDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cat, err := m.Catalog(svc.Workspace())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	dir := generationHolding(t, m, cat, hash)
	out, err := m.gitOutput(context.Background(), dir, "ls-tree", "-r", "--name-only", "-z", hash)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	return splitNULPaths(string(out))
}

// splitNULPaths splits a NUL-delimited path list, dropping the trailing empty
// element. NUL delimiters are used because a path may legitimately contain a
// newline, which a newline-delimited listing would split in two.
func splitNULPaths(s string) []string {
	s = strings.TrimSuffix(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// generationHolding finds the generation directory whose refs name hash.
func generationHolding(t *testing.T, m *Manager, cat *Catalog, hash string) string {
	t.Helper()
	man, err := cat.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if man == nil {
		t.Fatal("workspace has no manifest")
	}
	for i := range man.Generations {
		id := man.Generations[i].ID
		exists, err := cat.RefExists(context.Background(), id, hash)
		if err != nil {
			continue
		}
		if !exists {
			continue
		}
		dir, err := cat.GenerationDir(id)
		if err != nil {
			t.Fatalf("GenerationDir: %v", err)
		}
		return dir
	}
	t.Fatalf("no generation retains snapshot %s", hash)
	return ""
}

func requireServiceGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// A capture through the Service publishes a real snapshot and returns its hash.
func TestService_TrackReturnsHash(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if !validObjectHash(hash) {
		t.Fatalf("Track returned %q, which is not an object id", hash)
	}
}

// Diff compares the snapshot against the live work tree and reports the change.
func TestService_DiffShowsChange(t *testing.T) {
	requireServiceGit(t)

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
	if !strings.Contains(diff, "-hello") || !strings.Contains(diff, "+world") {
		t.Fatalf("diff missing expected content: %s", diff)
	}
}

// Restore puts the snapshot's content back and leaves extra files alone.
func TestService_RestoreReverts(t *testing.T) {
	requireServiceGit(t)

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
	// A file the snapshot never saw. Restoring must not delete it: the snapshot
	// records the paths it captured, not the absence of every other path.
	extra := filepath.Join(dir, "extra.txt")
	if err := os.WriteFile(extra, []byte("keep me"), 0644); err != nil {
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
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("Restore removed a file the snapshot never contained: %v", err)
	}
}

// MaxFileBytes still excludes a statically oversized file, and the exclusion is
// asserted through Git reading the published snapshot.
func TestService_ExcludesLargeFile(t *testing.T) {
	requireServiceGit(t)

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

	files := serviceTrackedPaths(t, svc, hash)
	joined := strings.Join(files, "\n")
	if strings.Contains(joined, "large.bin") {
		t.Fatalf("large.bin should not be in snapshot, got: %v", files)
	}
	if !strings.Contains(joined, "small.txt") {
		t.Fatalf("small.txt should be in snapshot, got: %v", files)
	}
}

// A snapshot must never change the project's own Git state.
func TestService_DoesNotTouchProjectGit(t *testing.T) {
	requireServiceGit(t)

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

// Gitignored files never enter a snapshot, however large or small.
func TestService_ExcludesGitignoredFiles(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".marshal\n/marshal\n*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "marshal.db"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marshal"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.log"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(t.TempDir(), dir, 50, []string{}, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	files := serviceTrackedPaths(t, svc, hash)
	joined := strings.Join(files, "\n")
	for _, ignored := range []string{".marshal/marshal.db", "marshal", "app.log"} {
		if strings.Contains(joined, ignored) {
			t.Fatalf("%s should not be in snapshot, got: %v", ignored, files)
		}
	}
	if !strings.Contains(joined, "tracked.txt") {
		t.Fatalf("tracked.txt should be in snapshot, got: %v", files)
	}
}

// Prune reclaims the generation whose snapshots have all expired, and the hash
// then stops being retained.
func TestService_Prune(t *testing.T) {
	requireServiceGit(t)

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

	// The expired generation is gone whole, and the hash is no longer
	// retained: looking it up must now fail rather than produce a diff.
	if _, err := svc.Diff(context.Background(), hash); err == nil {
		t.Fatal("Diff succeeded for a hash whose generation was reclaimed")
	}
	// No generation directory survives, which is what makes the bytes actually
	// free: a retained directory would keep the objects on disk.
	entries, err := os.ReadDir(filepath.Join(svc.StoreDir(), generationsDirName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read generations dir: %v", err)
	}
	for _, e := range entries {
		t.Fatalf("prune left generation directory %s behind", e.Name())
	}
}

// A disabled service captures nothing AND creates nothing: an empty store left
// behind by a disabled session is storage the user never asked for.
func TestService_DisabledCreatesNothing(t *testing.T) {
	requireServiceGit(t)

	dataDir := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := New(dataDir, dir, 2_000_000, []string{}, testLogger())
	svc.enabled = false

	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("disabled Track: %v", err)
	}
	if hash != "" {
		t.Fatalf("disabled Track returned a hash %q", hash)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, snapshotsDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a disabled service created the snapshot store: %v", err)
	}
}

// Diff and Restore must reject a hash that is not an object id rather than
// handing the text to Git as a revision expression.
func TestService_RejectsNonHashIdentifiers(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	svc := New(t.TempDir(), dir, 2_000_000, []string{}, testLogger())

	for _, bad := range []string{"", "HEAD", "HEAD~1", "--help", "refs/snapshots/x"} {
		if _, err := svc.Diff(context.Background(), bad); err == nil {
			t.Errorf("Diff(%q) succeeded; hash text must never become a revision expression", bad)
		}
		if err := svc.Restore(context.Background(), bad); err == nil {
			t.Errorf("Restore(%q) succeeded; hash text must never become a revision expression", bad)
		}
	}
}

// storeFingerprint returns a stable fingerprint of every file under the managed
// snapshots root. The diff must leave it byte-identical: a native Git write
// (an index update, a ref, a loose object) inside the store is exactly what the
// design forbids, and a mtime-only change would hide it.
func storeFingerprint(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			fmt.Fprintf(&b, "d %s\n", rel)
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		sum := "<symlink:" + rel + ">"
		if info.Mode().IsRegular() {
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			h := sha1.Sum(data)
			sum = hex.EncodeToString(h[:])
		}
		fmt.Fprintf(&b, "f %s %d %s\n", rel, info.Size(), sum)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("fingerprint %s: %v", root, err)
	}
	return b.String()
}

// The diff must be strictly READ-ONLY with respect to the managed store. The
// manager encodes the comparison index itself into a temporary file OUTSIDE the
// store, so no native Git writer touches the generation, and the encoded index
// must not be rewritten by the diff invocation either (GIT_OPTIONAL_LOCKS=0).
func TestService_DiffWritesNothingInTheStore(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(t.TempDir(), dir, 2_000_000, nil, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := storeFingerprint(t, filepath.Join(svc.DataDir(), snapshotsDirName))
	diff, err := svc.Diff(context.Background(), hash)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+world") {
		t.Fatalf("diff missing the change:\n%s", diff)
	}
	after := storeFingerprint(t, filepath.Join(svc.DataDir(), snapshotsDirName))
	if before != after {
		t.Fatal("Diff wrote something inside the managed snapshots store")
	}
}

// The encoded index is correct WITHOUT Git writing it: it is a version-2 index
// the manager builds from the snapshot's own tree entries, and Git reads it
// without rewriting it. This pins the format against the encoder and the
// reservation that must not drift from it.
func TestEncodeReadOnlyIndexMatchesGitAndIsStable(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := New(t.TempDir(), dir, 2_000_000, nil, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	m, err := NewManager(svc.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	cat, err := m.CatalogForRoot(svc.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Release() }()

	loc, err := m.LookupSnapshotOn(context.Background(), cat, hash)
	if err != nil {
		t.Fatalf("LookupSnapshotOn: %v", err)
	}
	scratch, err := m.encodeReadOnlyIndex(context.Background(), loc)
	if err != nil {
		t.Fatalf("encodeReadOnlyIndex: %v", err)
	}
	defer scratch.remove()

	encoded, err := os.ReadFile(scratch.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	// The reservation must be at least the encoded size and no more than one
	// allocation unit above it.
	if int64(len(encoded)) > scratch.bytes || scratch.bytes-int64(len(encoded)) >= DefaultAllocationUnitBytes {
		t.Fatalf("reservation %d does not bound the %d-byte index", scratch.bytes, len(encoded))
	}
	// Git must accept the manager-written index.
	out, errOut, _, err := m.gitRunEnv(context.Background(), loc.GitDir, svc.WorkTree(), nil, scratch.env(), defaultMaxCommandOutput,
		"ls-files")
	if err != nil {
		t.Fatalf("git ls-files: %v%s", err, gitOutputSuffix(errOut))
	}
	listed := string(out)
	for _, want := range []string{"a.txt", "sub/b.txt"} {
		if !strings.Contains(listed, want) {
			t.Errorf("git did not read %q out of the manager-written index:\n%s", want, listed)
		}
	}

	// The DIFF itself must leave its index byte-identical. It runs on a freshly
	// encoded index, because the `ls-files` probe above may legitimately refresh
	// stat data (a Git read path, not the diff under test).
	fresh, err := m.encodeReadOnlyIndex(context.Background(), loc)
	if err != nil {
		t.Fatalf("encodeReadOnlyIndex (fresh): %v", err)
	}
	defer fresh.remove()
	before, err := os.ReadFile(fresh.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	_, diffErrOut, _, err := m.gitRunEnv(context.Background(), loc.GitDir, svc.WorkTree(), nil, fresh.env(), defaultMaxCommandOutput,
		"diff-index", "--patch", "--no-ext-diff", "--no-textconv", loc.Hash)
	if err != nil {
		t.Fatalf("git diff-index: %v%s", err, gitOutputSuffix(diffErrOut))
	}
	after, err := os.ReadFile(fresh.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the read-only diff rewrote the manager-encoded index")
	}
}

// Restore is scoped to the snapshot's own paths and never follows a destination
// symlink out of the workspace.
func TestService_RestoreLeavesExtraFilesAndRefusesSymlinkDestination(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(t.TempDir(), dir, 2_000_000, nil, testLogger())
	hash, err := svc.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}

	// A file the snapshot never saw is left alone.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(dir, "extra.txt")
	if err := os.WriteFile(extra, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.Restore(context.Background(), hash); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "hello" {
		t.Fatalf("restored %q, want hello", data)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("Restore removed a file the snapshot never contained: %v", err)
	}

	// A symlink planted AT a snapshot path must be refused, not written
	// through: following it could write outside the workspace.
	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "a.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err = svc.Restore(context.Background(), hash)
	if !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("Restore over a symlink = %v, want ErrSymlinkEscape", err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "untouched" {
		t.Fatalf("restore wrote through a symlink: victim is %q", data)
	}
}

// Rooted must keep working: it is what the runtime actually wires up, and it
// resolves a service per active root.
func TestRootedTrackDiffRestoreThroughNewPath(t *testing.T) {
	requireServiceGit(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewRooted(t.TempDir(), dir, func() string { return dir }, 2_000_000, nil, testLogger())
	if !r.Enabled() {
		t.Fatal("Rooted reports disabled with git available")
	}
	hash, err := r.Track(context.Background())
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.WriteFile(path, []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}
	diff, err := r.Diff(context.Background(), hash)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+world") {
		t.Fatalf("diff missing the change:\n%s", diff)
	}
	if err := r.Restore(context.Background(), hash); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("restored content = %q, want %q", data, "hello")
	}
}
