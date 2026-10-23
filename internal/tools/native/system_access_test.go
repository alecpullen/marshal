// internal/tools/native/system_access_test.go — system-access seam tests.
package native

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestSystemAccessFlagDefaultsOff pins the containment default: without a
// session (or with the flag off) an absolute write path is rejected exactly
// as before.
func TestSystemAccessFlagDefaultsOff(t *testing.T) {
	root := t.TempDir()

	noSession, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	if noSession.systemAccess() {
		t.Fatal("systemAccess() = true without session state, want false")
	}
	if _, err := noSession.resolveToolPath("/etc/passwd", false); err == nil {
		t.Fatal("resolveToolPath(/etc/passwd) without session state = nil error, want rejection")
	}

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	withSession, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	if withSession.systemAccess() {
		t.Fatal("systemAccess() = true on a fresh session, want false")
	}
	if _, err := withSession.resolveToolPath("/etc/passwd", false); err == nil {
		t.Fatal("resolveToolPath(/etc/passwd) with the flag off = nil error, want rejection")
	}
}

// TestSystemAccessFollowsTheLiveSessionFlag pins that the toolset reads the
// session's system-access flag on every call rather than capturing it at
// construction. The flag is what gates privileged execution and out-of-root
// path resolution, so a stale copy would keep granting access after the user
// turned it off (or keep refusing after they turned it on).
func TestSystemAccessFollowsTheLiveSessionFlag(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})

	tools, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	if tools.systemAccess() {
		t.Fatal("systemAccess() = true with the flag off")
	}
	state.SetSystemAccess(true)
	if !tools.systemAccess() {
		t.Fatal("systemAccess() = false after the flag was turned on")
	}
	state.SetSystemAccess(false)
	if tools.systemAccess() {
		t.Fatal("systemAccess() = true after the flag was turned off")
	}
}

// TestResolveToolPathSystemModeAbsolute pins the widened write resolution:
// an out-of-root absolute path resolves only with system access on.
func TestResolveToolPathSystemModeAbsolute(t *testing.T) {
	root := t.TempDir()
	// Resolve the temp dir first: on macOS /var is a symlink to /private/var,
	// and resolveToolPath returns the symlink-resolved path.
	outsideDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	outside := filepath.Join(outsideDir, "report.json")

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	tools, err := newToolSet(Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}

	if _, err := tools.resolveToolPath(outside, false); err == nil {
		t.Fatal("resolveToolPath(out-of-root absolute) with the flag off = nil error, want rejection")
	}

	state.SetSystemAccess(true)
	got, err := tools.resolveToolPath(outside, false)
	if err != nil {
		t.Fatalf("resolveToolPath(out-of-root absolute) with system access: %v", err)
	}
	if got != outside {
		t.Fatalf("resolveToolPath = %q, want %q", got, outside)
	}
}
