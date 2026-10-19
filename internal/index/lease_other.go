//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris

package index

import (
	"errors"
	"os"
)

// errLeaseUnsupported reports a platform with no verified advisory-lock
// binding. TryLease returns it as an error, so callers index without the
// lease rather than trusting a lock that excludes nobody.
var errLeaseUnsupported = errors.New("advisory file locking is not supported on this platform")

func tryFlock(*os.File) (bool, error) { return false, errLeaseUnsupported }

func releaseFlock(*os.File) error { return nil }
