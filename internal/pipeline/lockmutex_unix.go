//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package pipeline

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// flockExclusive blocks until f holds an exclusive advisory lock on the whole
// file. It is the critical section that makes the run-lock takeover exclusive:
// the lock is held for the whole read → park → verify → claim → verify
// sequence, so concurrent takers are serialised by the kernel instead of
// racing each other's renames. The kernel drops it when the descriptor closes,
// so a taker that dies inside the section cannot wedge it.
func flockExclusive(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}

// funlock releases flockExclusive's lock. Unlocking explicitly is
// belt-and-braces: close(2) drops every flock the process holds on the file,
// but being explicit keeps ownership release independent of descriptor
// lifetime.
func funlock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil && !errors.Is(err, unix.EBADF) {
		return err
	}
	return nil
}
