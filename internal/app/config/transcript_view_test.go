package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptViewDefaultMergeAndSave(t *testing.T) {
	cfg := Default()
	if cfg.TUI.TranscriptView != TranscriptLegacy {
		t.Fatalf("default = %q", cfg.TUI.TranscriptView)
	}
	view := "notebook"
	if err := merge(&cfg, configFile{TUI: &fileTUI{TranscriptView: &view}}); err != nil {
		t.Fatal(err)
	}
	if cfg.TUI.TranscriptView != TranscriptNotebook {
		t.Fatalf("merged = %q", cfg.TUI.TranscriptView)
	}
	var file configFile
	writeSections(&file, cfg, Default())
	if file.TUI == nil || file.TUI.TranscriptView == nil || *file.TUI.TranscriptView != view {
		t.Fatalf("saved field = %+v", file.TUI)
	}
}

func TestTranscriptViewLayerPrecedenceAndInvalidFallbackDiagnostic(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config", "marshal"), 0700); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(home, ".config", "marshal", "config.toml")
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(projectDir, ".marshal"), 0700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(projectDir, ".marshal", "config.toml")
	if err := os.WriteFile(user, []byte("[tui]\ntranscript_view = 'notebook'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte("[tui]\ntranscript_view = 'legacy'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	layers, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: projectDir})
	if err != nil {
		t.Fatal(err)
	}
	if layers.Merged.TUI.TranscriptView != TranscriptLegacy {
		t.Fatalf("precedence = %q", layers.Merged.TUI.TranscriptView)
	}
	if err := os.WriteFile(project, []byte("[tui]\ntranscript_view = 'broken'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	layers, err = LoadLayers(LoadOptions{HomeDir: home, WorkingDir: projectDir})
	if err != nil {
		t.Fatal(err)
	}
	if layers.Merged.TUI.TranscriptView.Effective() != TranscriptLegacy {
		t.Fatalf("invalid effective = %q", layers.Merged.TUI.TranscriptView.Effective())
	}
	found := false
	for _, d := range Diagnose(layers.Merged, layers) {
		if d.Path == "tui.transcript_view" {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid transcript view produced no diagnostic")
	}
}
