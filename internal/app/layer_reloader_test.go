package app

import (
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/trust"
)

// seedProjectConfig writes a minimal project config into dir/.marshal.
func seedProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLayerReloaderForNeverPromptsAndReplaysSessionTrust pins the reloader
// contract: it must resolve trust without any possibility of prompting
// (the TUI owns stdin), and a session-only trust decision — which never
// wrote a store record — must still keep the project layer applied.
func TestLayerReloaderForNeverPromptsAndReplaysSessionTrust(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", filepath.Join(home, "data"))
	work := t.TempDir()
	seedProjectConfig(t, work, "[agent]\nplan_first = true\n")

	// Untrusted session: the project layer must be dropped, not prompted for.
	untrusted := layerReloaderFor(home, work, func() bool { return false })
	layers, ok := untrusted()
	if !ok {
		t.Fatal("untrusted reload failed")
	}
	if layers.ProjectApplied() {
		t.Error("untrusted session must not apply the project layer")
	}
	if layers.Merged.Agent.PlanFirst {
		t.Error("project-layer value leaked into an untrusted session's reload")
	}

	// Session-only trust (no store record): the decision must be replayed so
	// the project layer stays applied.
	sessionTrusted := layerReloaderFor(home, work, func() bool { return true })
	layers, ok = sessionTrusted()
	if !ok {
		t.Fatal("session-trusted reload failed")
	}
	if !layers.ProjectApplied() {
		t.Fatal("session-only trust lost the project layer on reload")
	}
	if !layers.Merged.Agent.PlanFirst {
		t.Error("project-layer value dropped despite session trust")
	}

	// An untrusted session must not consult the trust store's prompt path
	// even when a permanent record exists: the session's own decision is
	// authoritative for the reload (a fresh launch with a changed config
	// would re-prompt, i.e. drop the layer — the reload mirrors that
	// without ever prompting).
	if err := trust.NewStore(config.DataDir(home)).SetTrust(trust.Canonicalize(work), true, ""); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	layers, ok = untrusted()
	if !ok {
		t.Fatal("reload with a store record but untrusted session failed")
	}
	if layers.ProjectApplied() {
		t.Error("untrusted session must not apply the project layer even with a store record")
	}
}

// TestLayerReloaderForNoProjectConfig covers the no-config case: the
// reloader must succeed with default layers rather than erroring.
func TestLayerReloaderForNoProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", filepath.Join(home, "data"))
	work := t.TempDir()

	layers, ok := layerReloaderFor(home, work, func() bool { return false })()
	if !ok {
		t.Fatal("reload without a project config failed")
	}
	if layers.ProjectApplied() {
		t.Error("no project config exists, but ProjectApplied is true")
	}
}
