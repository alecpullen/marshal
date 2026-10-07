//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

// openNoFollowFlag is a no-op on a platform with no verified no-follow flag.
//
// This package already fails closed on such a platform (platformSupportsOwnership
// is false, so no store operation can run). The constant exists so the open call
// still compiles, and the containment guarantee comes from os.Root regardless.
const openNoFollowFlag = 0

// openNoFollowSupported reports whether a no-follow open flag is meaningful.
const openNoFollowSupported = false
