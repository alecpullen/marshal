//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock attempts an exclusive, non-blocking flock. It returns (false, nil)
// when another open file description holds the lock, which is the signal to
// wait and retry rather than an error.
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

// releaseFlock explicitly unlocks and then closes. Unlocking is belt-and-braces:
// close(2) drops every flock the process holds on the file, but being explicit
// keeps ownership release independent of descriptor-lifetime details.
func releaseFlock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil && !errors.Is(err, unix.EBADF) {
		return err
	}
	return nil
}
