//go:build windows

package worktree

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// platformTargetLocking reports whether this platform has a verified advisory
// lock binding. LockFileEx is verified on Windows, so it is.
const platformTargetLocking = true

// tryTargetLock attempts a non-blocking LockFileEx over the whole file: shared
// for an ordinary root user and exclusive for a lifecycle mutation. A lock
// another handle holds surfaces as ERROR_LOCK_VIOLATION, which means "wait and
// retry", not "failed".
func tryTargetLock(f *os.File, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 0xFFFFFFFF, 0xFFFFFFFF, ol)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

// unlockTargetFile unlocks the whole-file range tryTargetLock locked.
func unlockTargetFile(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 0xFFFFFFFF, 0xFFFFFFFF, ol)
	if err != nil && !errors.Is(err, windows.ERROR_NOT_LOCKED) {
		return err
	}
	return nil
}
