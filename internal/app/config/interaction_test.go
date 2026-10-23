package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/interaction"
)

func interactionLayers(t *testing.T, userTOML, projectTOML string) (Layers, error) {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	if userTOML != "" {
		writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"), userTOML)
	}
	if projectTOML != "" {
		writeFile(t, filepath.Join(work, ".marshal", "config.toml"), projectTOML)
	}
	return LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
}

func TestInteractionFreshDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Agent.Mode != interaction.ModeAct {
		t.Fatalf("fresh Mode = %q, want act", cfg.Agent.Mode)
	}
	if cfg.Agent.Auto {
		t.Fatal("fresh Auto = true, want false")
	}
	d, err := cfg.InteractionDefaults()
	if err != nil {
		t.Fatalf("InteractionDefaults: %v", err)
	}
	if d.Mode != interaction.ModeAct || d.Auto {
		t.Fatalf("InteractionDefaults = %+v, want act/false", d)
	}
}

func TestInteractionLegacyLayerMappings(t *testing.T) {
	cases := []struct {
		legacy string
		mode   interaction.Mode
		auto   bool
	}{
		{"plan", interaction.ModeAsk, false},
		{"default", interaction.ModeAsk, false},
		{"edit", interaction.ModeAsk, false},
		{"copilot", interaction.ModeAct, false},
		{"auto", interaction.ModeAct, true},
	}
	for _, tc := range cases {
		t.Run(tc.legacy, func(t *testing.T) {
			l, err := interactionLayers(t, "[agent]\napproval_mode = \""+tc.legacy+"\"\n", "")
			if err != nil {
				t.Fatalf("LoadLayers: %v", err)
			}
			if l.Merged.Agent.Mode != tc.mode || l.Merged.Agent.Auto != tc.auto {
				t.Fatalf("merged = %q/%v, want %q/%v", l.Merged.Agent.Mode, l.Merged.Agent.Auto, tc.mode, tc.auto)
			}
			if len(l.InteractionNotices) == 0 {
				t.Fatal("expected a migration notice")
			}
		})
	}
}

func TestInteractionCanonicalFieldPrecedence(t *testing.T) {
	// Project canonical mode wins over the user layer.
	l, err := interactionLayers(t, "[agent]\nmode = \"ask\"\n", "[agent]\nmode = \"act\"\n")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Agent.Mode != interaction.ModeAct {
		t.Fatalf("merged Mode = %q, want act (project wins)", l.Merged.Agent.Mode)
	}

	// Same-layer canonical mode ignores the legacy spelling.
	l, err = interactionLayers(t, "[agent]\nmode = \"ask\"\napproval_mode = \"auto\"\n", "")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Agent.Mode != interaction.ModeAsk || l.Merged.Agent.Auto {
		t.Fatalf("merged = %q/%v, want ask/false (legacy ignored)", l.Merged.Agent.Mode, l.Merged.Agent.Auto)
	}
}

func TestInteractionHigherAskClearsInheritedAuto(t *testing.T) {
	l, err := interactionLayers(t, "[agent]\nmode = \"act\"\nauto = true\n", "[agent]\nmode = \"ask\"\n")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Agent.Mode != interaction.ModeAsk {
		t.Fatalf("merged Mode = %q, want ask", l.Merged.Agent.Mode)
	}
	if l.Merged.Agent.Auto {
		t.Fatal("merged Auto = true, want false (Ask clears inherited Auto)")
	}
}

func TestInteractionExplicitFalsePresence(t *testing.T) {
	l, err := interactionLayers(t, "[agent]\nmode = \"act\"\nauto = true\n", "[agent]\nauto = false\n")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Agent.Mode != interaction.ModeAct || l.Merged.Agent.Auto {
		t.Fatalf("merged = %q/%v, want act/false", l.Merged.Agent.Mode, l.Merged.Agent.Auto)
	}
}

func TestInteractionUnknownExplicitFailsLoad(t *testing.T) {
	_, err := interactionLayers(t, "[agent]\nmode = \"bogus\"\n", "")
	if err == nil {
		t.Fatal("LoadLayers accepted an unknown explicit mode")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %v, want it to name the invalid value", err)
	}
}

func TestInteractionIgnoredLegacyCannotEnableAuto(t *testing.T) {
	l, err := interactionLayers(t, "[agent]\nmode = \"ask\"\napproval_mode = \"auto\"\n", "")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Agent.Auto {
		t.Fatal("ignored legacy approval_mode enabled Auto")
	}
}

func TestInteractionCustomAgentLayerNormalization(t *testing.T) {
	// Replacement-by-name: the project entry replaces the user entry.
	l, err := interactionLayers(t,
		"[custom_agents.x]\npreset = \"p\"\nmode = \"ask\"\n",
		"[custom_agents.x]\npreset = \"p\"\nmode = \"act\"\n")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	ca, ok := l.Merged.CustomAgents["x"]
	if !ok {
		t.Fatal("custom agent x missing")
	}
	if ca.Mode == nil || *ca.Mode != interaction.ModeAct {
		t.Fatalf("custom agent Mode = %v, want act", ca.Mode)
	}

	// A legacy custom-agent mode normalizes to the canonical tuple.
	l, err = interactionLayers(t, "[custom_agents.y]\npreset = \"p\"\napproval_mode = \"auto\"\n", "")
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	cy := l.Merged.CustomAgents["y"]
	if cy.Mode == nil || *cy.Mode != interaction.ModeAct || cy.Auto == nil || !*cy.Auto {
		t.Fatalf("custom agent y = %v/%v, want act/true", cy.Mode, cy.Auto)
	}
}

func TestInteractionLoadDoesNotRewriteLegacyFieldsDuringHoist(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"), "")
	projectPath := filepath.Join(work, ".marshal", "config.toml")
	writeFile(t, projectPath, strings.Join([]string{
		"[agent]",
		"approval_mode = \"auto\"",
		"",
		"[providers.local]",
		"base_url = \"http://127.0.0.1:11434/v1\"",
		"",
		"[models.presets.\"local/m\"]",
		"provider = \"local\"",
		"model = \"m\"",
		"",
	}, "\n"))

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.Merged.Agent.Mode != interaction.ModeAct || !l.Merged.Agent.Auto {
		t.Fatalf("merged = %q/%v, want act/true", l.Merged.Agent.Mode, l.Merged.Agent.Auto)
	}
	// The hoist moved providers/presets but must not rewrite the authored
	// legacy interaction field in the project file.
	raw, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if !strings.Contains(string(raw), "approval_mode") {
		t.Fatalf("hoist rewrote the authored legacy field:\n%s", raw)
	}
}
