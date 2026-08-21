package config

import "testing"

func TestLoadWithLayersReturnsBoth(t *testing.T) {
	opts := LoadOptions{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}
	cfg, layers, err := LoadWithLayers(opts)
	if err != nil {
		t.Fatalf("LoadWithLayers: %v", err)
	}
	if layers.Merged.Agent.MaxToolIterations != cfg.Agent.MaxToolIterations {
		t.Fatal("Layers.Merged should match returned Config")
	}
	// With no user or project config, the default layer equals the merged
	// layer.
	if layers.Default.Agent.MaxToolIterations != layers.Merged.Agent.MaxToolIterations {
		t.Fatal("Layers.Default should equal Merged when no config file exists")
	}
}
