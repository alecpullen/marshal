//go:build windows

package snapshot

import (
	"errors"
	"testing"
)

// requireUnixFifo skips a test that needs a named pipe, which Windows cannot
// create with the same semantics (a Windows pipe has no filesystem path that
// git lists as an untracked entry).
func requireUnixFifo(t *testing.T) {
	t.Helper()
	t.Skip("named pipes are not creatable as workspace entries on this platform")
}

// mkFifo is unreachable on Windows because requireUnixFifo skips first.
func mkFifo(string) error { return errors.New("named pipes are not creatable on this platform") }

// requireUnixShell skips a test that needs a POSIX shell.
func requireUnixShell(t *testing.T) {
	t.Helper()
	t.Skip("a POSIX shell is not available on this platform")
}

// runningAsRoot reports whether the test runs with privileges that defeat
// permission-based assertions. Windows has no equivalent of euid 0.
func runningAsRoot() bool { return false }

// requireReadableWorkspace skips a permission-based test on a platform whose
// permission model does not make a directory unenumerable this way.
func requireReadableWorkspace(t *testing.T) {
	t.Helper()
	t.Skip("directory read permissions are not enforced this way on this platform")
}
