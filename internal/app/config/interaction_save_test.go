package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
)

func interactionBoolPtr(b bool) *bool { return &b }

func TestInteractionSaveEmitsCanonicalOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".marshal", "config.toml")
	cfg := Default()
	cfg.Agent.Mode = interaction.ModeAsk
	cfg.Agent.Auto = false
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `mode = "ask"`) && !strings.Contains(s, `mode = 'ask'`) {
		t.Fatalf("canonical mode missing:\n%s", s)
	}
	if strings.Contains(s, "approval_mode") {
		t.Fatalf("retired approval_mode emitted:\n%s", s)
	}
}

func TestInteractionCustomAgentSaveRetainsAbsentAndFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".marshal", "config.toml")
	cfg := Default()
	cfg.CustomAgents["inherit"] = routing.CustomAgent{Name: "inherit", Preset: "p"}
	cfg.CustomAgents["narrow"] = routing.CustomAgent{
		Name:   "narrow",
		Preset: "p",
		Mode:   interactionModePtr(interaction.ModeAct),
		Auto:   interactionBoolPtr(false),
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	loaded, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	inherit := loaded.CustomAgents["inherit"]
	if inherit.Mode != nil || inherit.Auto != nil {
		t.Fatalf("inherit = %v/%v, want absent", inherit.Mode, inherit.Auto)
	}
	narrow := loaded.CustomAgents["narrow"]
	if narrow.Mode == nil || *narrow.Mode != interaction.ModeAct {
		t.Fatalf("narrow.Mode = %v, want act", narrow.Mode)
	}
	if narrow.Auto == nil || *narrow.Auto {
		t.Fatalf("narrow.Auto = %v, want explicit false", narrow.Auto)
	}
}

func TestInteractionExplicitSectionSaveRetiresLegacyKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".marshal", "config.toml")
	writeFile(t, path, strings.Join([]string{
		"[agent]",
		`approval_mode = "auto"`,
		"",
		"[tools.shell]",
		"auto_approve = true",
		`guardrail_dynamic_argv0 = "deny"`,
		"",
	}, "\n"))
	cfg := Default()
	cfg.Agent.Mode = interaction.ModeAct
	cfg.Agent.Auto = true
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	for _, retired := range []string{"approval_mode", "auto_approve", "guardrail_dynamic_argv0"} {
		if strings.Contains(s, retired) {
			t.Fatalf("retired key %q survived an explicit save:\n%s", retired, s)
		}
	}
}

func TestInteractionSavePreservesExplicitRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".marshal", "config.toml")
	writeFile(t, path, strings.Join([]string{
		"[tools.shell]",
		`allow = { commands = ["custom-cmd"] }`,
		`confirm = { commands = ["custom-confirm"] }`,
		`deny = { patterns = ["custom-deny *"] }`,
		"",
	}, "\n"))
	cfg := Default()
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	for _, want := range []string{"custom-cmd", "custom-confirm", "custom-deny"} {
		if !strings.Contains(s, want) {
			t.Fatalf("authored rule %q lost:\n%s", want, s)
		}
	}
}

func TestInteractionSaveDoesNotPersistSessionGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".marshal", "config.toml")
	cfg := Default()
	cfg.Agent.Mode = interaction.ModeAct
	cfg.Agent.Auto = true
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	for _, forbidden := range []string{"system_access", "access_revision"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("session grant %q persisted:\n%s", forbidden, s)
		}
	}
}

func TestCloneConfigDoesNotAliasInteractionOrPermissionMaps(t *testing.T) {
	cfg := Default()
	cfg.CustomAgents["a"] = routing.CustomAgent{Name: "a", Preset: "p"}
	cfg.Permissions.Rules = []PermissionRule{{Permission: "shell.run", Pattern: "x", Action: "allow"}}

	clone := Clone(cfg)
	clone.CustomAgents["a"] = routing.CustomAgent{Name: "a", Preset: "changed"}
	clone.Permissions.Rules[0].Action = "deny"

	if got := cfg.CustomAgents["a"].Preset; got != "p" {
		t.Fatalf("original custom agent mutated: Preset = %q", got)
	}
	if got := cfg.Permissions.Rules[0].Action; got != "allow" {
		t.Fatalf("original permission rule mutated: Action = %q", got)
	}
}

func interactionModePtr(m interaction.Mode) *interaction.Mode { return &m }
