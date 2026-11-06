package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

// newLayersTestModel builds a model whose state carries a real Layers
// snapshot (user layer = defaults) and whose adapter reloads the layers from
// the given home directory — the same shape the production wiring produces.
func newLayersTestModel(t *testing.T, home, work string) Model {
	t.Helper()
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{})
	state.SetLayers(config.Layers{Default: config.Default(), User: config.Default(), Merged: config.Default()})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := testModelWith(inproc.Deps{
		State:    state,
		Commands: reg,
		HomeDir:  home,
		ReloadLayers: func() (config.Layers, bool) {
			layers, err := config.LoadLayers(config.LoadOptions{HomeDir: home, WorkingDir: work})
			if err != nil {
				return config.Layers{}, false
			}
			return layers, true
		},
	}, WithHomeDir(home))
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// TestProjectSaveAfterGlobalSaveDoesNotBakeUserValues is the regression
// for the stale layer snapshot: a global-scope save (user config) must
// refresh the session's layer snapshot, or the next project-scope save
// compares against the stale user layer and re-bakes the user-global
// value into the committable .marshal/config.toml. The refresh happens agent
// side, in the adapter, after every user write.
func TestProjectSaveAfterGlobalSaveDoesNotBakeUserValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	m := newLayersTestModel(t, home, work)

	// 1. Global-scope save: tui.mouse_capture is a writeGlobal field.
	m.dispatchCommand("/set tui.mouse_capture off")
	userData, err := os.ReadFile(config.UserConfigPath(home))
	if err != nil {
		t.Fatalf("user config not written by global save: %v", err)
	}
	if !strings.Contains(string(userData), "mouse_capture") {
		t.Fatalf("global save did not persist mouse_capture:\n%s", userData)
	}

	// The session snapshot must now reflect the new user layer.
	if m.sess.Layers().Merged.TUI.MouseCapture {
		t.Fatal("session layer snapshot went stale after a global-scope save")
	}

	// 2. Project-scope save of an unrelated field.
	m.dispatchCommand("/set agent.plan_first on")
	projectData, err := os.ReadFile(filepath.Join(work, ".marshal", "config.toml"))
	if err != nil {
		t.Fatalf("project config not written: %v", err)
	}
	if !strings.Contains(string(projectData), "plan_first") {
		t.Fatalf("project save dropped the project-layer value:\n%s", projectData)
	}
	if strings.Contains(string(projectData), "mouse_capture") {
		t.Fatalf("stale user layer re-baked into the project config:\n%s", projectData)
	}
}

// TestSetWithoutReloaderLeavesLayersAlone guards the no-reloader path: a model
// whose adapter has no layer reloader (most tests) still saves through the
// seam, and the state's layers are left exactly as they were.
func TestSetWithoutReloaderLeavesLayersAlone(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).WorkingDir = t.TempDir()
	seeded := config.Layers{Default: config.Default(), User: config.Default(), Merged: config.Default()}
	stateOf(m).SetLayers(seeded)

	m.dispatchCommand("/set tui.mouse_capture off")

	if got := m.sess.Layers(); got.Merged.TUI.MouseCapture != seeded.Merged.TUI.MouseCapture {
		t.Fatalf("layers changed without a reloader: %+v", got)
	}
}
