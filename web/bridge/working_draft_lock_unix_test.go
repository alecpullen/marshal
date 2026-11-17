//go:build linux || darwin

package bridge

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkingDraftStoreReclaimsLockFromDeadOwner proves a store left locked by a
// process that is gone reopens. Recovering what a crash interrupted is this
// store's reason to exist, and refusing to start would silently disable that
// recovery until somebody deleted a file from the state directory by hand.
func TestWorkingDraftStoreReclaimsLockFromDeadOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "working-drafts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	owner := workingDraftLockContent(exitedProcessPID(t), []byte(strings.Repeat("a", 32)))
	if err := os.WriteFile(filepath.Join(dir, workingDraftLockName), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}

	s, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
	if err != nil {
		t.Fatalf("open over a dead owner's lock: %v", err)
	}
	defer s.Close()
	if _, err := s.Create("/projects/alpha", "req-1", WorkingDraftContent{Title: "t", Plan: "p"}); err != nil {
		t.Fatalf("create after reclaiming the lock: %v", err)
	}
}

// TestWorkingDraftStoreKeepsLockItCannotProveStale is the fail-closed side of
// the same rule: a live owner, an opaque token written by an older bridge, and a
// half-written lock are all kept, because taking a lock a live store holds would
// let two writers share one journal.
func TestWorkingDraftStoreKeepsLockItCannotProveStale(t *testing.T) {
	for name, content := range map[string]string{
		"live owner":       workingDraftLockContent(os.Getpid(), []byte(strings.Repeat("b", 32))),
		"opaque token":     strings.Repeat("c", 32),
		"half written":     workingDraftLockPIDPrefix + "1\n",
		"unparsable owner": workingDraftLockPIDPrefix + "not-a-pid\n" + strings.Repeat("d", workingDraftLockTokenLength) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "working-drafts")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, workingDraftLockName), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := OpenWorkingDraftStore(dir, workingDraftTestClock())
			if err == nil {
				_ = s.Close()
				t.Fatal("a lock that cannot be shown stale was reclaimed")
			}
			if !errors.Is(err, ErrWorkingDraftUnavailable) {
				t.Fatalf("open: %v", err)
			}
		})
	}
}

// exitedProcessPID starts a process, waits for it, and returns its PID, so a
// test can present an ownership lock whose writer is provably gone.
func exitedProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for helper process: %v", err)
	}
	return pid
}
