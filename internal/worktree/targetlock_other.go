//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package worktree

import "os"

// platformTargetLocking is false here: this platform has neither a verified
// shared/exclusive advisory-lock binding nor a way to take one non-blockingly.
// Acquisition returns ErrTargetLockUnsupported rather than an in-process-only
// lock that would claim cross-process exclusion it cannot provide.
const platformTargetLocking = false

// tryTargetLock fails closed on a platform with no verified lock binding.
func tryTargetLock(*os.File, bool) (bool, error) {
	return false, ErrTargetLockUnsupported
}

// unlockTargetFile has nothing to release: no lock was ever taken.
func unlockTargetFile(*os.File) error { return nil }
