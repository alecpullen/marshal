package config

import "testing"

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
