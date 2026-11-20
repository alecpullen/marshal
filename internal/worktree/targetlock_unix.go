//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package worktree

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// platformTargetLocking reports whether this platform has a verified advisory
// lock binding. The alternative is failing closed, never an unlocked success.
const platformTargetLocking = true

// tryTargetLock attempts a non-blocking flock: shared for an ordinary root user
// and exclusive for a lifecycle mutation. It returns (false, nil) when another
// open file description holds an incompatible lock, which is the signal to wait
// and retry rather than an error.
func tryTargetLock(f *os.File, exclusive bool) (bool, error) {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return false, err
}

// unlockTargetFile explicitly unlocks and then closes. Unlocking is
// belt-and-braces: close(2) drops every flock the process holds on the file,
// but being explicit keeps release independent of descriptor-lifetime details.
func unlockTargetFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil && !errors.Is(err, unix.EBADF) {
		return err
	}
	return nil
}
