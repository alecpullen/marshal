//go:build windows

package pipeline

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// flockExclusive is the Windows fallback for platforms without flock: a
// blocking LockFileEx over the whole file range, with the same
// hold-for-the-whole-sequence semantics. Windows releases the lock when the
// handle closes, so a taker that dies inside the section cannot wedge it.
func flockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	for {
		err := windows.LockFileEx(
			windows.Handle(f.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK, // blocking: no FAIL_IMMEDIATELY
			0,
			0xFFFFFFFF,
			0xFFFFFFFF,
			ol,
		)
		if err == nil {
			return nil
		}
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			// The range is held; the blocking wait did not apply to this
			// handle. Retry rather than fail a takeover.
			time.Sleep(time.Millisecond)
			continue
		}
		return err
	}
}

// funlock releases the range locked by flockExclusive.
func funlock(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 0xFFFFFFFF, 0xFFFFFFFF, ol)
	if err != nil && !errors.Is(err, windows.ERROR_NOT_LOCKED) {
		return err
	}
	return nil
}
