package snapshot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootedSnapshotsFollowActiveRoot(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	worktree := t.TempDir()

	root := project
	r := NewRooted(dataDir, project, func() string { return root }, 1<<20, nil, slog.Default())
	ctx := context.Background()

	// Snapshot and restore at the project root.
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h1, err := r.Track(ctx)
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.Restore(ctx, h1); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "f.txt")); string(data) != "v1" {
		t.Fatalf("project f.txt = %q, want v1", data)
	}

	// Rebind to the worktree: snapshots and restores land there, and the
	// project root's files are untouched by worktree operations.
	root = worktree
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("w1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h2, err := r.Track(ctx)
	if err != nil {
		t.Fatalf("Track worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("w2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.Restore(ctx, h2); err != nil {
		t.Fatalf("Restore worktree: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(worktree, "f.txt")); string(data) != "w1" {
		t.Fatalf("worktree f.txt = %q, want w1", data)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "f.txt")); string(data) != "v1" {
		t.Fatalf("project f.txt = %q after worktree ops, want v1", data)
	}
}

// TestRootedSvcReusesServiceForSameRoot verifies that svc() returns the
// same *Service for the same active root, so concurrent Track calls share
// the same semaphore.
func TestRootedSvcReusesServiceForSameRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dataDir := t.TempDir()
	project := t.TempDir()

	r := NewRooted(dataDir, project, func() string { return project }, 1<<20, nil, testLogger())

	s1 := r.svc()
	s2 := r.svc()
	if s1 != s2 {
		t.Fatal("svc() should return the same *Service for the same root")
	}
}

// TestRootedSvcCreatesNewServiceForNewRoot verifies that svc() returns a
// different *Service when the active root changes (worktree rebind).
func TestRootedSvcCreatesNewServiceForNewRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dataDir := t.TempDir()
	project := t.TempDir()
	worktree := t.TempDir()

	root := project
	r := NewRooted(dataDir, project, func() string { return root }, 1<<20, nil, testLogger())

	s1 := r.svc()
	root = worktree
	s2 := r.svc()
	if s1 == s2 {
		t.Fatal("svc() should return a different *Service for a different root")
	}
	// First root's service should still be cached.
	s3 := r.svc()
	if s3 != s2 {
		t.Fatal("svc() should return cached *Service for worktree root")
	}
}

// recordingGit is a wrapper script that records every invocation and then
// delegates to the real git. It is the seam that proves exactly which commands
// a lifecycle path runs — in particular that shutdown runs no gc or repack.
func recordingGit(t *testing.T, logPath string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write recording git: %v", err)
	}
	return script
}

// loggedGitCommands returns every recorded invocation, one per line.
func loggedGitCommands(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read git log: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// assertNoHeavyGit fails if any recorded invocation runs a gc, repack, reflog
// expiry, or ref pruning SUBCOMMAND.
//
// The test is on the subcommand rather than on substring presence, because
// every managed invocation carries the `-c gc.auto=0` pin — which is the very
// configuration that stops Git from repacking on its own. The subcommand is the
// first argument that is neither a `-c name=value` pin nor a `--option`.
func assertNoHeavyGit(t *testing.T, commands []string) {
	t.Helper()
	banned := map[string]bool{"gc": true, "repack": true, "prune": true, "prune-packed": true}
	for _, c := range commands {
		sub := gitSubcommand(c)
		if banned[sub] {
			t.Errorf("recorded a heavy storage command %q (subcommand %q); storage maintenance must not repack", c, sub)
			continue
		}
		if sub == "reflog" && strings.Contains(c, "expire") {
			t.Errorf("recorded a reflog expiry %q; storage maintenance must not prune reachability", c)
		}
	}
}

// gitSubcommand extracts the git subcommand from a recorded argument list,
// skipping the `-c name=value` pins and `--option` arguments.
func gitSubcommand(c string) string {
	fields := strings.Fields(c)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-c":
			i++ // skip the value
		case strings.HasPrefix(f, "-c"), strings.HasPrefix(f, "--"):
			continue
		default:
			return f
		}
	}
	return ""
}

// A capture in the MAIN checkout and a capture in a bound WORKTREE, then a
// maintenance sweep from the worktree binding: the sweep must reconcile and
// expire BOTH stores, not just the active one. This is the whole-root behaviour
// that replaced the old main-checkout-only Prune.
func TestRootedMaintainsWorktreeStoresNotJustActiveRoot(t *testing.T) {
	requireServiceGit(t)

	dataDir := t.TempDir()
	project := t.TempDir()
	worktree := t.TempDir()
	root := project
	r := NewRooted(dataDir, project, func() string { return root }, 2_000_000, nil, testLogger())

	// Snapshot in the project root.
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("p1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Track(context.Background()); err != nil {
		t.Fatalf("project Track: %v", err)
	}

	// Rebind to the worktree and snapshot there too.
	root = worktree
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("w1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Track(context.Background()); err != nil {
		t.Fatalf("worktree Track: %v", err)
	}

	projectHash := WorkspaceHashFor(project)
	worktreeHash := WorkspaceHashFor(worktree)
	if projectHash == worktreeHash {
		t.Fatal("project and worktree hashed to the same store")
	}

	// Both stores exist before the sweep.
	for _, ws := range []string{projectHash, worktreeHash} {
		if _, err := os.Stat(filepath.Join(dataDir, snapshotsDirName, v2DirName, ws, manifestFileName)); err != nil {
			t.Fatalf("store %s missing before maintenance: %v", ws, err)
		}
	}

	time.Sleep(1100 * time.Millisecond)
	report, err := r.Maintain(context.Background(), 0)
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}

	// The sweep must have covered BOTH workspaces: reconciliation records each
	// versioned workspace it visited.
	seen := map[string]bool{}
	for _, ws := range report.Reconciled {
		seen[ws] = true
	}
	for _, ws := range []string{projectHash, worktreeHash} {
		if !seen[ws] {
			t.Fatalf("maintenance did not cover workspace %s; covered %v", ws, report.Reconciled)
		}
	}

	// And with retention 0 the expired generations in BOTH stores are gone:
	// the manifest survives, the generations directory does not.
	for _, ws := range []string{projectHash, worktreeHash} {
		genDir := filepath.Join(dataDir, snapshotsDirName, v2DirName, ws, generationsDirName)
		entries, err := os.ReadDir(genDir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read generations for %s: %v", ws, err)
		}
		for _, e := range entries {
			t.Errorf("maintenance left generation %s in workspace %s", e.Name(), ws)
		}
	}
}

// Rooted follows a session's active-root rebind for MAINTENANCE as well as for
// capture: the sweep that runs while the session is bound to a worktree must
// still cover the project root's store (and vice versa), because storage is
// bounded globally.
func TestRootedMaintenanceCoversBothRootsAcrossRebind(t *testing.T) {
	requireServiceGit(t)

	dataDir := t.TempDir()
	project := t.TempDir()
	worktree := t.TempDir()
	root := project
	r := NewRooted(dataDir, project, func() string { return root }, 2_000_000, nil, testLogger())

	// Capture in the project root, then rebind to the worktree and capture
	// there. The rebind is what makes the project store "inactive".
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("p1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Track(context.Background()); err != nil {
		t.Fatalf("project Track: %v", err)
	}
	root = worktree
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("w1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Track(context.Background()); err != nil {
		t.Fatalf("worktree Track: %v", err)
	}

	projectHash := WorkspaceHashFor(project)
	worktreeHash := WorkspaceHashFor(worktree)

	// The sweep runs while bound to the WORKTREE, so the project's store is not
	// the active root. Both must still be covered.
	report, err := r.Maintain(context.Background(), 7)
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	seen := map[string]bool{}
	for _, ws := range report.Reconciled {
		seen[ws] = true
	}
	if !seen[projectHash] {
		t.Fatalf("sweep bound to the worktree skipped the project store %s; covered %v", projectHash, report.Reconciled)
	}
	if !seen[worktreeHash] {
		t.Fatalf("sweep bound to the worktree skipped its own store %s; covered %v", worktreeHash, report.Reconciled)
	}

	// The per-operation surface still follows the ACTIVE root: an explicit
	// Service bound to the project root resolves the project's own snapshot,
	// exactly as the rebind contract promises. Maintenance covers everything;
	// rollback stays scoped to the store the session is working in.
	projSvc := New(dataDir, project, 2_000_000, nil, testLogger())
	if _, err := projSvc.LookupSnapshot(context.Background(), ""); !errors.Is(err, ErrInvalidObjectHash) {
		t.Fatalf("lookup with an empty hash = %v, want ErrInvalidObjectHash", err)
	}
}

// A workspace whose checkout no longer exists is still maintained: its manifest
// is the only record of where it came from, and its expired generations must be
// reclaimable like any other.
func TestRootedMaintainsInactiveStoreWhoseCheckoutIsGone(t *testing.T) {
	requireServiceGit(t)

	dataDir := t.TempDir()
	// The workspace is a directory we create and then DELETE, so its store is
	// the only surviving trace of it.
	gone := filepath.Join(t.TempDir(), "gone-workspace")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir()
	r := NewRooted(dataDir, live, func() string { return live }, 2_000_000, nil, testLogger())
	if err := os.WriteFile(filepath.Join(gone, "f.txt"), []byte("g1"), 0o644); err != nil {
		t.Fatal(err)
	}
	goneSvc := New(dataDir, gone, 2_000_000, nil, testLogger())
	if _, err := goneSvc.Track(context.Background()); err != nil {
		t.Fatalf("Track in the to-be-deleted workspace: %v", err)
	}
	goneHash := goneSvc.Workspace()
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)
	report, err := r.Maintain(context.Background(), 0)
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	covered := false
	for _, ws := range report.Reconciled {
		if ws == goneHash {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("maintenance skipped the store whose checkout is gone (%s); covered %v", goneHash, report.Reconciled)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, snapshotsDirName, v2DirName, goneHash, generationsDirName))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read generations: %v", err)
	}
	for _, e := range entries {
		t.Errorf("maintenance left generation %s in the inactive workspace", e.Name())
	}
	// The manifest itself survives: forgetting it would leave orphaned storage
	// with no way to know where it came from.
	if _, err := os.Stat(filepath.Join(dataDir, snapshotsDirName, v2DirName, goneHash, manifestFileName)); err != nil {
		t.Fatalf("maintenance removed the inactive workspace's manifest: %v", err)
	}
}

// CloseSnapshots performs NO storage maintenance: it runs no Git command at
// all, and in particular no gc or repack. That is the defect this task closes —
// the old shutdown path ran `git gc --prune=now`, which could not free anything
// and left 171 GiB of tmp_pack_* garbage behind when it was interrupted.
func TestRootedCloseSnapshotsRunsNoGit(t *testing.T) {
	requireServiceGit(t)

	dataDir := t.TempDir()
	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "git.log")
	recorder := recordingGit(t, logPath)

	r := NewRooted(dataDir, dir, func() string { return dir }, 2_000_000, nil, testLogger(),
		WithRootedGitBinary(recorder))
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Track(context.Background()); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// A maintenance sweep is allowed to run Git (read-only listings).
	if _, err := r.Maintain(context.Background(), 7); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	afterMaintain := loggedGitCommands(t, logPath)
	if len(afterMaintain) == 0 {
		t.Fatal("maintenance ran no Git at all; the recording seam is not wired")
	}
	assertNoHeavyGit(t, afterMaintain)

	// Close must run nothing at all.
	before := len(afterMaintain)
	if err := r.CloseSnapshots(context.Background()); err != nil {
		t.Fatalf("CloseSnapshots: %v", err)
	}
	after := loggedGitCommands(t, logPath)
	if len(after) != before {
		t.Fatalf("CloseSnapshots ran %d Git commands (%v); shutdown must do no storage work",
			len(after)-before, after[before:])
	}
}

// The maintenance sweep is a cancellation-respecting no-op when the snapshots
// root does not exist: a session that never captured must not bootstrap a store
// just to maintain it.
func TestRootedMaintainIsNoOpWithoutStoreRoot(t *testing.T) {
	requireServiceGit(t)

	dataDir := t.TempDir()
	dir := t.TempDir()
	r := NewRooted(dataDir, dir, func() string { return dir }, 2_000_000, nil, testLogger())

	report, err := r.Maintain(context.Background(), 7)
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if len(report.Reconciled) != 0 || len(report.Reclaimed) != 0 {
		t.Fatalf("maintenance of an absent store reported %+v, want an empty report", report)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, snapshotsDirName)); !os.IsNotExist(err) {
		t.Fatalf("maintenance created a store for a workspace that never captured: %v", err)
	}
	if r.HasActiveStore() {
		t.Fatal("HasActiveStore = true for a workspace with no store")
	}

	// A cancelled context is honoured, not ignored.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Maintain(ctx, 7); err == nil {
		t.Fatal("Maintain with a cancelled context returned no error")
	}
}
