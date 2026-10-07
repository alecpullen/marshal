//go:build windows

package snapshot

// openNoFollowFlag is a no-op on Windows, which has no O_NOFOLLOW.
//
// The containment guarantee does not depend on it: every restore write goes
// through os.Root, whose Windows implementation refuses a name that resolves
// outside the root, and restoreTreeEntry additionally refuses a destination that
// is a symlink (checked with Lstat) before opening it. This constant exists so
// the open call compiles identically on both platforms.
const openNoFollowFlag = 0

// openNoFollowSupported reports whether a no-follow open flag is meaningful.
const openNoFollowSupported = false
