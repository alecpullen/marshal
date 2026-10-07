//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// processTree is a Unix process group. The child is started with Setpgid so it
// becomes the leader of a fresh group; every descendant it spawns inherits that
// group unless it deliberately changes it. Killing the group's negative pgid
// therefore reaches children and grandchildren, which is why this is race-free
// on Unix: the group exists from fork.
type processTree struct {
	// mu guards pgid: it is written by the goroutine that started the child
	// and read by whichever goroutine releases ownership.
	mu   sync.Mutex
	pgid int
}

// newProcessTree arranges for cmd to run in its own process group.
func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Preserve any attributes the caller already set.
	cmd.SysProcAttr.Setpgid = true
	return &processTree{}, nil
}

// afterStart records the group id. Because Setpgid was set, the child's process
// group id equals its pid.
func (t *processTree) afterStart(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid child pid %d", pid)
	}
	t.mu.Lock()
	t.pgid = pid
	t.mu.Unlock()
	return nil
}

// group returns the recorded process group id, or 0 when none was recorded.
func (t *processTree) group() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pgid
}

// kill signals SIGKILL to the entire process group. A group that is already
// gone is not an error.
func (t *processTree) kill() error {
	pgid := t.group()
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// waitGone blocks until no process remains in the group. It is what lets a
// caller conclude that no writer survives.
func (t *processTree) waitGone() error {
	pgid := t.group()
	if pgid <= 0 {
		return nil
	}
	deadline := time.Now().Add(processTreeKillTimeout)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("probe process group %d: %w", pgid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process group %d still has live members after %s", pgid, processTreeKillTimeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// close releases the group handle. On Unix there is nothing to release; the
// group id is a plain value.
func (t *processTree) close() {}
