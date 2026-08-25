package config

import "testing"

func TestDefaultDepthIsFlat(t *testing.T) {
	if got := Default().TUI.Depth; got != "flat" {
		t.Fatalf("default tui.depth = %q, want \"flat\"", got)
	}
}

// Mirrors TestMergeSidePanel (config_test.go:1389) — merge takes a pointer to
// the Config and an unexported configFile, and returns an error.
func TestMergeDepth(t *testing.T) {
	cfg := Default()
	depth := "raised"
	if err := merge(&cfg, configFile{TUI: &fileTUI{Depth: &depth}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.TUI.Depth != "raised" {
		t.Fatalf("merged tui.depth = %q, want \"raised\"", cfg.TUI.Depth)
	}
}

func TestMergeDepthUnsetKeepsDefault(t *testing.T) {
	cfg := Default()
	mode := "light"
	if err := merge(&cfg, configFile{TUI: &fileTUI{Mode: &mode}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.TUI.Depth != "flat" {
		t.Fatalf("tui.depth = %q after an unrelated merge, want \"flat\"", cfg.TUI.Depth)
	}
}

// writeSections (save.go:175) is what populates the file struct on save.
func TestDepthSurvivesSave(t *testing.T) {
	cfg := Default()
	cfg.TUI.Depth = "full"
	var file configFile
	writeSections(&file, cfg, Default())
	if file.TUI == nil || file.TUI.Depth == nil || *file.TUI.Depth != "full" {
		t.Fatalf("writeSections dropped tui.depth: %+v", file.TUI)
	}
}
