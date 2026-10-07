//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import "syscall"

// openNoFollowFlag is O_NOFOLLOW where the platform provides it.
//
// Restore uses it as the last guard on a destination file: opening a path that
// is a symlink FAILS rather than writing through it, so a link planted in the
// workspace cannot redirect a restore outside the workspace. os.Root already
// contains the path; this closes the remaining race between the check and the
// open.
const openNoFollowFlag = syscall.O_NOFOLLOW

// openNoFollowSupported reports whether the flag is meaningful here.
const openNoFollowSupported = true
