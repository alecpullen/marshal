package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

// newLayersTestModel builds a model whose state carries a real Layers
// snapshot (user layer = defaults) and a layerReloader that re-reads the
// user config from disk — the same shape the production wiring produces.
func newLayersTestModel(t *testing.T, work string) Model {
	t.Helper()
	state := session.New(config.Default(), work, time.Unix(100, 0), session.Persistence{})
	state.SetLayers(config.Layers{Default: config.Default(), User: config.Default(), Merged: config.Default()})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// wireLayerReloader points the model's layerReloader at the real loader
// over the given home directory, mirroring app.layerReloaderFor.
func wireLayerReloader(m *Model, home, work string) {
	m.layerReloader = func() (config.Layers, bool) {
		layers, err := config.LoadLayers(config.LoadOptions{HomeDir: home, WorkingDir: work})
		if err != nil {
			return config.Layers{}, false
		}
		return layers, true
	}
}

// TestProjectSaveAfterGlobalSaveDoesNotBakeUserValues is the regression
// for the stale layer snapshot: a global-scope save (user config) must
// refresh the session's layer snapshot, or the next project-scope save
// compares against the stale user layer and re-bakes the user-global
// value into the committable .marshal/config.toml.
func TestProjectSaveAfterGlobalSaveDoesNotBakeUserValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	m := newLayersTestModel(t, work)
	m.configLayers = new(*config.Layers)
	wireLayerReloader(&m, home, work)

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
	if m.state.Layers().Merged.TUI.MouseCapture {
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

// TestReloadLayersRefreshesOpenDockPanel pins the panel-refresh half of
// the fix: a docked settings browser that captured a snapshot at open
// time must receive the fresh snapshot when layers reload.
func TestReloadLayersRefreshesOpenDockPanel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	m := newLayersTestModel(t, work)
	m.configLayers = new(*config.Layers)
	wireLayerReloader(&m, home, work)

	m.openSettingsBrowser("")
	browser, ok := m.dock.Panel().(*settings.BrowserPanel)
	if !ok {
		t.Fatalf("expected *settings.BrowserPanel, got %T", m.dock.Panel())
	}
	if browser.Layers().Merged.TUI.MouseCapture != config.Default().TUI.MouseCapture {
		t.Fatal("precondition: browser snapshot should start at defaults")
	}

	// Change the user config behind the panel's back, then reload.
	if err := config.SaveUserConfigValue(config.UserConfigPath(home), "tui.mouse_capture", false); err != nil {
		t.Fatalf("seed user config: %v", err)
	}
	m.reloadLayers()

	if m.state.Layers().Merged.TUI.MouseCapture {
		t.Fatal("session state did not adopt the reloaded layers")
	}
	if browser.Layers().Merged.TUI.MouseCapture {
		t.Fatal("open dock panel kept its stale layer snapshot")
	}
}

// TestReloadLayersNoReloaderIsNoop guards the nil-reloader path: models
// built without WithLayerReloader (most tests) must not panic.
func TestReloadLayersNoReloaderIsNoop(t *testing.T) {
	m := newLayersTestModel(t, t.TempDir())
	m.reloadLayers() // must not panic
}
