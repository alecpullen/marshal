package config

import (
	"reflect"
	"testing"
)

func TestLoadLayersSubtaskIterationsSet(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	// Neither layer sets it.
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.SubtaskIterationsSet {
		t.Fatal("SubtaskIterationsSet should be false when no file sets it")
	}

	// User layer sets it.
	writeFile(t, home+"/.config/marshal/config.toml", "[agent]\nsubtask_iterations = 5\n")
	l, err = LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if !l.SubtaskIterationsSet {
		t.Fatal("SubtaskIterationsSet should be true when user file sets it")
	}

	// Project layer sets it (explicit 0 is still "set").
	writeFile(t, home+"/.config/marshal/config.toml", "")
	writeFile(t, work+"/.marshal/config.toml", "[agent]\nsubtask_iterations = 0\n")
	l, err = LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if !l.SubtaskIterationsSet {
		t.Fatal("SubtaskIterationsSet should be true when project file sets explicit 0")
	}
}

func TestLoadLayersAndProvenance(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[tui]\ntheme = \"light\"\n")
	writeFile(t, work+"/.marshal/config.toml", "[tui]\nmode = \"dark\"\n[project]\nname = \"repo\"\n")

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.TUI.Theme != "light" || l.Merged.TUI.Mode != "dark" {
		t.Fatalf("merged = %+v", l.Merged.TUI)
	}

	// theme: supplied by the user layer, overriding the built-in default.
	p := l.ProvenanceOf("tui.theme")
	if p.SetBy != LayerUser {
		t.Errorf("tui.theme SetBy = %v, want user", p.SetBy)
	}
	// mode: supplied by the project layer, overriding nothing (user==default).
	p = l.ProvenanceOf("tui.mode")
	if p.SetBy != LayerProject {
		t.Errorf("tui.mode SetBy = %v, want project", p.SetBy)
	}
	// a default-only value.
	p = l.ProvenanceOf("commands.test")
	if p.SetBy != LayerDefault {
		t.Errorf("commands.test SetBy = %v, want default", p.SetBy)
	}
}

func TestLegacySubtaskIterationsDefaultReplaced(t *testing.T) {
	// The old implicit default was 12. When a user explicitly sets
	// subtask_iterations=12, the loader treats it as unset (0) so the
	// current default applies instead.
	opts := LoadOptions{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}
	_, layers, err := LoadWithLayers(opts)
	if err != nil {
		t.Fatalf("LoadWithLayers: %v", err)
	}
	// Default config should have subtask_iterations = 0 (unset/unlimited)
	if layers.Merged.Agent.SubtaskIterations != 0 {
		t.Fatalf("expected SubtaskIterations=0 (unset), got %d", layers.Merged.Agent.SubtaskIterations)
	}
}

func TestProvenanceOfScalarComparison(t *testing.T) {
	def := Default()
	user := def
	// Change a scalar field in the user layer
	user.Agent.MaxToolIterations = 999
	layers := Layers{Default: def, User: user, Merged: user}
	p := layers.ProvenanceOf("agent.max_tool_iterations")
	if p.SetBy != LayerUser {
		t.Fatalf("expected SetBy=LayerUser, got %s", p.SetBy)
	}
}

func TestProvenanceReportsOverriddenLayer(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[tui]\ntheme = \"light\"\n")
	writeFile(t, work+"/.marshal/config.toml", "[tui]\ntheme = \"solarized\"\n")

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	p := l.ProvenanceOf("tui.theme")
	if p.SetBy != LayerProject || p.Overrides != LayerUser {
		t.Fatalf("got (setBy=%v, overrides=%v), want (project, user)", p.SetBy, p.Overrides)
	}
}

func TestLookupPathHandlesMaps(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{"ollama": {BaseURL: "http://localhost:11434/v1"}}
	v, ok := LookupPath(cfg, "providers.ollama.base_url")
	if !ok || v != "http://localhost:11434/v1" {
		t.Fatalf("LookupPath = %v, %v", v, ok)
	}
}

func TestLookupPathFallsBackToFieldName(t *testing.T) {
	// A struct with a field that has no toml tag should be found by lowercased name.
	type noTag struct {
		MyField string
	}
	v := reflect.ValueOf(noTag{MyField: "hello"})
	f, ok := fieldByTOMLTag(v, "myfield")
	if !ok {
		t.Fatal("fieldByTOMLTag should find MyField by lowercased name")
	}
	if f.Interface() != "hello" {
		t.Fatalf("got %v, want hello", f.Interface())
	}
}

func TestSaveUserConfigValueOnProviderMap(t *testing.T) {
	home := t.TempDir()
	path := UserConfigPath(home)
	if err := SaveUserConfigValue(path, "providers.ollama.api_key", "sk-test"); err != nil {
		t.Fatalf("SaveUserConfigValue: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["ollama"].APIKey != "sk-test" {
		t.Fatalf("providers.ollama.api_key = %q, want sk-test", cfg.Providers["ollama"].APIKey)
	}
}

func TestSaveUserConfigValue(t *testing.T) {
	home := t.TempDir()
	path := UserConfigPath(home)
	if err := SaveUserConfigValue(path, "tui.theme", "solarized"); err != nil {
		t.Fatalf("SaveUserConfigValue: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TUI.Theme != "solarized" {
		t.Fatalf("theme = %q, want solarized", cfg.TUI.Theme)
	}
	// A second value in a different section joins the same file.
	if err := SaveUserConfigValue(path, "tui.side_panel.enabled", false); err != nil {
		t.Fatalf("SaveUserConfigValue bool: %v", err)
	}
	cfg, _ = Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if cfg.TUI.SidePanel.Enabled {
		t.Fatal("side_panel.enabled should be false")
	}
}
