package config

import "testing"

func TestDefaultSkillLoadGateThreshold(t *testing.T) {
	cfg := Default()
	if cfg.Skills.LoadGateThresholdTokens != 131072 {
		t.Fatalf("LoadGateThresholdTokens = %d, want 131072", cfg.Skills.LoadGateThresholdTokens)
	}
}

func TestSkillLoadGateThresholdMergesFromLayer(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[skills]\nload_gate_threshold_tokens = 65536\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Skills.LoadGateThresholdTokens != 65536 {
		t.Fatalf("LoadGateThresholdTokens = %d, want 65536", l.Merged.Skills.LoadGateThresholdTokens)
	}
}

func TestSkillLoadGateThresholdExplicitZeroWins(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[skills]\nload_gate_threshold_tokens = 0\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Skills.LoadGateThresholdTokens != 0 {
		t.Fatalf("LoadGateThresholdTokens = %d, want 0 (explicit 0 must win over the default)", l.Merged.Skills.LoadGateThresholdTokens)
	}
}
