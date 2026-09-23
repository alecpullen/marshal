// internal/tools/native/system_access_test.go — system-access seam tests.
package native

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
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

// TestGuardrailClosureCarriesSystemFlag pins that the native pre-flight
// receives the toolset's own session flag, not a captured parent value.
func TestGuardrailClosureCarriesSystemFlag(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})

	var got []bool
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{result: CommandResult{ExitCode: 0}},
		SessionState:  state,
		Guardrail: func(command string, system bool) error {
			got = append(got, system)
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"echo hi"}`); err != nil {
		t.Fatalf("shell.run: %v", err)
	}
	state.SetSystemAccess(true)
	if _, err := invokeTool(t, reg, "shell.run", `{"command":"echo hi"}`); err != nil {
		t.Fatalf("shell.run: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("guardrail invoked %d times, want 2", len(got))
	}
	if got[0] {
		t.Fatal("guardrail received system=true with the flag off")
	}
	if !got[1] {
		t.Fatal("guardrail received system=false with the flag on")
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
