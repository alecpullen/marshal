//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

import (
	"errors"
	"testing"
)

// requireUnixFifo skips a test that needs a named pipe.
func requireUnixFifo(t *testing.T) {
	t.Helper()
	t.Skip("named pipes are not available on this platform")
}

// mkFifo is unreachable on this platform because requireUnixFifo skips first.
func mkFifo(string) error { return errors.New("named pipes are not available on this platform") }

// requireUnixShell skips a test that needs a POSIX shell.
func requireUnixShell(t *testing.T) {
	t.Helper()
	t.Skip("a POSIX shell is not available on this platform")
}

// runningAsRoot reports whether the test runs with privileges that defeat
// permission-based assertions.
func runningAsRoot() bool { return false }

// requireReadableWorkspace skips a permission-based test on a platform whose
// permission model is not known here.
func requireReadableWorkspace(t *testing.T) {
	t.Helper()
	t.Skip("directory read permissions are not enforced this way on this platform")
}
