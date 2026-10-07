//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

// platformSupportsOwnership is false here: this platform has neither a verified
// advisory-lock binding nor a process-tree implementation, so every ownership
// and accounting entry point fails closed instead of running with a weaker
// guarantee.
const platformSupportsOwnership = false
