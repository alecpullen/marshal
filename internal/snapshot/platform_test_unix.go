//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// requireUnixFifo skips a test when the platform cannot create a named pipe. It
// never skips an assertion: the whole point of the test is that a pipe is
// refused, so a platform without pipes has nothing to assert.
func requireUnixFifo(t *testing.T) {
	t.Helper()
}

// mkFifo creates a named pipe.
func mkFifo(path string) error {
	return syscall.Mkfifo(path, 0o644)
}

// requireUnixShell skips a test that needs a POSIX shell to plant a hostile
// filter or external program.
func requireUnixShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}

// runningAsRoot reports whether the test runs with privileges that defeat
// permission-based assertions. Such a test skips rather than passing for the
// wrong reason.
func runningAsRoot() bool { return os.Geteuid() == 0 }

// requireReadableWorkspace skips a permission-based test when the environment
// cannot express the assertion at all: root bypasses permission bits, and a
// filesystem mounted with default permissions may ignore them.
func requireReadableWorkspace(t *testing.T) {
	t.Helper()
	if runningAsRoot() {
		t.Skip("running as root: permission bits do not restrict reads")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o000); err != nil {
		t.Skipf("cannot create an unreadable directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("this filesystem does not enforce directory read permission")
	}
}
