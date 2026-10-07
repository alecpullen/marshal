//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

import "os"

// allocatedSize and freeSpaceBytes fail closed here. This platform has no
// verified allocation or filesystem-space binding, and reporting a guess would
// be worse than an error: an under-counted allocation silently admits writes the
// budget never allowed, and an over-counted free space promises room that does
// not exist.
//
// The error carries ReasonUnsupportedPlatform, so a caller can tell "this
// platform cannot enforce the bound" apart from "this filesystem failed".
func allocatedSize(path string, _ os.FileInfo) (int64, error) {
	return 0, storeErrorf(ReasonUnsupportedPlatform, "filesystem allocation accounting for %s", path)
}

func freeSpaceBytes(path string) (int64, error) {
	return 0, storeErrorf(ReasonUnsupportedPlatform, "filesystem free-space query for %s", path)
}
