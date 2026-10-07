//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

import "os"

// tryFlock fails closed on a platform with no verified advisory-lock binding.
// A silent in-process-only lock would be worse than an error: it would appear
// to provide cross-process exclusion while providing none.
func tryFlock(*os.File) (bool, error) {
	return false, storeErrorf(ReasonUnsupportedPlatform, "advisory file locking")
}

func releaseFlock(*os.File) error { return nil }
