//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package index

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock attempts an exclusive, non-blocking flock. It returns (false, nil)
// when another open file description holds the lock.
func tryFlock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return false, err
}

// releaseFlock unlocks f. Closing f would drop the lock too; unlocking first
// keeps the release independent of descriptor lifetime.
func releaseFlock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil && !errors.Is(err, unix.EBADF) {
		return err
	}
	return nil
}
