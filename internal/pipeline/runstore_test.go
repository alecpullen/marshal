package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRunStoreManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	m := Manifest{
		SchemaVersion:  1,
		RunID:          "run-abc",
		PlanPath:       filepath.Join(dir, "plan.md"),
		PlanHash:       "sha256-hash",
		RepoRoot:       dir,
		TargetBranch:   "main",
		PipelineBranch: "pipeline/test-plan",
		BaseSHA:        "base123",
		WorktreePath:   filepath.Join(paths.Dir, "worktrees", "pipeline-test-plan"),
	}
	if err := rs.CreateManifest(m); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	got, err := rs.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if got.RunID != m.RunID || got.PlanHash != m.PlanHash {
		t.Errorf("manifest round-trip mismatch: got %+v, want %+v", got, m)
	}
}

func TestRunStoreManifestImmutable(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	m := Manifest{RunID: "run-1", PlanPath: "p.md"}
	if err := rs.CreateManifest(m); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	m2 := Manifest{RunID: "run-2", PlanPath: "p.md"}
	if err := rs.CreateManifest(m2); err == nil {
		t.Fatal("second CreateManifest should fail (immutable), got nil")
	}
}

func TestRunStoreManifestMissingIsError(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	if _, err := rs.Manifest(); err == nil {
		t.Fatal("Manifest on missing file should return error")
	}
}

func TestRunStoreCheckpointAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	c1 := Checkpoint{Seq: 1, RunID: "r1", Phase: PhaseImplementing, TaskN: 1}
	c2 := Checkpoint{Seq: 2, RunID: "r1", Phase: PhaseCommitting, TaskN: 1, HeadSHA: "abc123"}
	if err := rs.AppendCheckpoint(c1); err != nil {
		t.Fatalf("AppendCheckpoint 1: %v", err)
	}
	if err := rs.AppendCheckpoint(c2); err != nil {
		t.Fatalf("AppendCheckpoint 2: %v", err)
	}

	last, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !ok {
		t.Fatal("LastCheckpoint returned ok=false")
	}
	if last.Seq != 2 || last.HeadSHA != "abc123" {
		t.Errorf("last = %+v, want seq=2 head=abc123", last)
	}

	all, err := rs.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Replay returned %d, want 2", len(all))
	}
	if all[0].Seq != 1 || all[1].Seq != 2 {
		t.Errorf("replay order wrong: %d %d", all[0].Seq, all[1].Seq)
	}
}

func TestRunStorePartialFinalLineIgnored(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	c1 := Checkpoint{Seq: 1, Phase: PhaseImplementing}
	if err := rs.AppendCheckpoint(c1); err != nil {
		t.Fatalf("AppendCheckpoint: %v", err)
	}
	// Simulate a crash: append a partial line without newline.
	cpPath := rs.checkpointPath()
	f, _ := os.OpenFile(cpPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"seq":2,"phase":"committing"`) // no newline, incomplete JSON
	f.Close()

	last, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if !ok || last.Seq != 1 {
		t.Errorf("last = %+v ok=%v, want seq=1", last, ok)
	}
}

func TestRunStoreNoCheckpoints(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	_, ok, err := rs.LastCheckpoint()
	if err != nil {
		t.Fatalf("LastCheckpoint: %v", err)
	}
	if ok {
		t.Fatal("LastCheckpoint on empty store should return ok=false")
	}
	all, err := rs.Replay()
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("Replay on empty store returned %d, want 0", len(all))
	}
}

func TestRunStoreLockAcquireAndConcurrentRefusal(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	lock := RunLock{RunID: "r1", PID: 12345, Host: "test-host", AcquiredAt: "2026-08-08T12:00:00Z"}
	if err := rs.AcquireLock(lock); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	got, ok, err := rs.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if !ok || got.RunID != "r1" {
		t.Errorf("lock = %+v ok=%v, want r1", got, ok)
	}

	// Second acquire must fail.
	if err := rs.AcquireLock(RunLock{RunID: "r2", PID: 99999}); err == nil {
		t.Fatal("second AcquireLock should fail, got nil")
	}
}

func TestAtomicWriteSyncCleansUpTempOnError(t *testing.T) {
	dir := t.TempDir()
	// Target a path in a non-existent subdirectory so the temp file
	// creation fails (the parent dir doesn't exist).
	badPath := filepath.Join(dir, "nonexistent", "output.json")
	err := atomicWriteSync(badPath, []byte("{}"))
	if err == nil {
		t.Fatal("atomicWriteSync should fail for non-existent directory")
	}
	// Verify no .tmp file is left behind.
	tmpPath := badPath + ".tmp"
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("stale .tmp file left behind at %s", tmpPath)
	}
}

func TestAcquireLockAtomicNoTOCTOU(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)

	// Two goroutines attempt to acquire the lock simultaneously.
	errs := make(chan error, 2)
	go func() {
		errs <- rs.AcquireLock(RunLock{RunID: "r1", PID: 1, Host: "h", AcquiredAt: "now"})
	}()
	go func() {
		errs <- rs.AcquireLock(RunLock{RunID: "r2", PID: 2, Host: "h", AcquiredAt: "now"})
	}()

	err1 := <-errs
	err2 := <-errs

	// Exactly one should succeed, the other should fail.
	successes := 0
	for _, err := range []error{err1, err2} {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 success, got %d (err1=%v, err2=%v)", successes, err1, err2)
	}
}

func TestRunStoreLockRelease(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	if err := rs.AcquireLock(RunLock{RunID: "r1", PID: 1}); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := rs.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	if _, ok, _ := rs.Lock(); ok {
		t.Fatal("lock should be gone after release")
	}
	// Re-acquire after release should work.
	if err := rs.AcquireLock(RunLock{RunID: "r2", PID: 2}); err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
}

// AUD-07: concurrent takeovers of the same stale lock must produce exactly
// one new owner. A remove-then-create takeover lets a later taker delete the
// earlier taker's fresh lock, so two callers could both "succeed".
//
// The takers are released into the takeover's read-then-swap window together,
// so every one of them judges the same stale lock; that is the interleaving
// the assertion is about, and it is deterministic instead of timing-dependent.
func TestTakeoverLockIsAtomicUnderConcurrentAttempts(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	if err := rs.AcquireLock(RunLock{RunID: "stale", PID: 1, Host: "h", AcquiredAt: "old"}); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	const takers = 8
	var arrived sync.WaitGroup
	arrived.Add(takers)
	release := make(chan struct{})
	go func() {
		arrived.Wait()
		close(release)
	}()
	rs.takeoverSwapHook = func() {
		arrived.Done()
		<-release
	}

	errs := make([]error, takers)
	var finished sync.WaitGroup
	finished.Add(takers)
	for i := 0; i < takers; i++ {
		go func(i int) {
			defer finished.Done()
			errs[i] = rs.TakeoverLock(RunLock{
				RunID: fmt.Sprintf("taker-%d", i), PID: 100 + i, Host: "h", AcquiredAt: "new",
			})
		}(i)
	}
	finished.Wait()

	var winners []string
	for i, err := range errs {
		if err == nil {
			winners = append(winners, fmt.Sprintf("taker-%d", i))
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d takeovers succeeded, want exactly 1 (errors: %v)", len(winners), errs)
	}
	l, ok, lerr := rs.Lock()
	if lerr != nil || !ok {
		t.Fatalf("lock = %+v ok=%v err=%v, want the winner's lock to survive", l, ok, lerr)
	}
	if l.RunID != winners[0] {
		t.Errorf("surviving lock names %q, want the successful taker %q", l.RunID, winners[0])
	}
}

// AUD-07: when the lock changes between the read and the swap, the takeover
// must not destroy the lock it did not judge stale — it fails and leaves a
// lock file behind.
func TestTakeoverLockRestoresADisplacedLock(t *testing.T) {
	dir := t.TempDir()
	paths, err := NewPaths(dir, "test-plan")
	if err != nil {
		t.Fatalf("NewPaths: %v", err)
	}
	rs := NewRunStore(paths)
	if err := rs.AcquireLock(RunLock{RunID: "stale", PID: 1, Host: "h", AcquiredAt: "old"}); err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	// A competing owner replaces the lock in the window between the
	// takeover's read and its move-aside.
	rs.takeoverSwapHook = func() {
		content := []byte(`{"run_id":"live","pid":7,"host":"h","acquired_at":"new"}`)
		if err := os.WriteFile(rs.lockPath(), content, 0o644); err != nil {
			t.Errorf("write competing lock: %v", err)
		}
	}

	err = rs.TakeoverLock(RunLock{RunID: "me", PID: 2, Host: "h", AcquiredAt: "now"})
	if err == nil {
		t.Fatal("TakeoverLock should refuse a lock that changed under it")
	}
	l, ok, lerr := rs.Lock()
	if lerr != nil || !ok {
		t.Fatalf("lock = %+v ok=%v err=%v, want the displaced lock restored", l, ok, lerr)
	}
	if l.RunID != "live" {
		t.Errorf("lock names %q after the aborted takeover, want the competing owner \"live\"", l.RunID)
	}
}
