//go:build !windows

package pipeline

import (
	"errors"
	"syscall"
)

// pidAlive reports whether pid names a running process. known is false when
// the platform cannot answer, in which case the caller must treat the owner
// as unknown rather than dead.
func pidAlive(pid int) (alive bool, known bool) {
	if pid <= 0 {
		return false, true
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, syscall.EPERM):
		return true, true // exists, owned by another user
	case errors.Is(err, syscall.ESRCH):
		return false, true
	default:
		return false, false
	}
}
