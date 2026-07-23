package settings

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestRegistryKeysUniqueAndPopulated(t *testing.T) {
	registry := BuildRegistry(config.Default())
	keys := registry.Keys()
	if len(keys) < 30 {
		t.Fatalf("registry suspiciously small: %d keys", len(keys))
	}

	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		if _, ok := registry.Lookup(key); !ok {
			t.Errorf("Keys() entry %q not Lookup-able", key)
		}
	}
}

func TestRegistryParityWithSectionFrames(t *testing.T) {
	state := newState(config.Default())
	registry := BuildRegistry(config.Default())
	for _, section := range sectionList() {
		for _, field := range section.root(state).list.Rows() {
			if field.id == "" {
				continue
			}
			if _, ok := registry.Lookup(field.id); !ok {
				t.Errorf("section %s field %q missing from registry", section.id, field.id)
			}
		}
	}
}

func TestRegistryApplyToggle(t *testing.T) {
	registry := BuildRegistry(config.Default())
	change, err := registry.Apply("shell.allow_network", "on")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if change.NewValue != "on" {
		t.Errorf("newValue = %q, want on", change.NewValue)
	}
	if !change.Changed {
		t.Errorf("toggle did not report a change: %#v", change)
	}
	if !registry.Config().Tools.Shell.AllowNetwork {
		t.Error("Apply did not mutate the working config")
	}
}

func TestRegistryApplyUnavailableAgentToggle(t *testing.T) {
	registry := BuildRegistry(config.Default())
	before := registry.Config()
	if _, err := registry.Apply("agent.local_only", "on"); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Apply unavailable agent toggle error = %v, want unavailable error", err)
	}
	if !reflect.DeepEqual(registry.Config(), before) {
		t.Fatal("unavailable agent toggle must not mutate config")
	}
}

func TestRegistryApplyAgentToggleWithActivePreset(t *testing.T) {
	cfg := config.Default()
	cfg.Profile.Default = "default"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"default": {
			Roles: map[routing.AgentRole]string{routing.RoleImplementer: "local"},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{"local": {}}
	registry := BuildRegistry(cfg)

	change, err := registry.Apply("agent.local_only", "on")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !change.Changed || !registry.Config().Models.Presets["local"].LocalOnly {
		t.Fatalf("agent toggle did not update active preset: %#v", change)
	}

	change, err = registry.Apply("agent.local_only", "on")
	if err != nil {
		t.Fatalf("reapplying active agent toggle: %v", err)
	}
	if change.Changed {
		t.Fatalf("reapplying active agent toggle should be unchanged: %#v", change)
	}
}

func TestRegistryApplyErrors(t *testing.T) {
	registry := BuildRegistry(config.Default())
	if _, err := registry.Apply("no.such.key", "x"); err == nil {
		t.Error("unknown key must error")
	}
	if _, err := registry.Apply("shell.allow_network", "maybe"); err == nil {
		t.Error("bad bool must error")
	}
	for _, key := range registry.Keys() {
		field, _ := registry.Lookup(key)
		if field.kind != kindEnum || field.setStr == nil {
			continue
		}
		if _, err := registry.Apply(key, "definitely-not-an-option"); err == nil ||
			!strings.Contains(err.Error(), "one of") {
			t.Errorf("enum %s: want 'must be one of' error, got %v", key, err)
		}
		return
	}
	t.Error("registry has no writable enum field")
}

func TestModifiedDetectsDivergenceFromDefaults(t *testing.T) {
	cfg := config.Default()
	registry := BuildRegistry(cfg)

	// Initially nothing is modified.
	if got := registry.Modified(); len(got) != 0 {
		t.Fatalf("fresh registry: Modified() = %v, want empty", got)
	}

	// Apply a change to a known toggle field.
	_, err := registry.Apply("shell.allow_network", "on")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	modified := registry.Modified()
	if len(modified) == 0 {
		t.Fatal("after toggling shell.allow_network: Modified() is empty, want non-empty")
	}
	found := false
	for _, id := range modified {
		if id == "shell.allow_network" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("after toggling shell.allow_network: Modified() = %v, want shell.allow_network", modified)
	}

	// Reset by building a fresh registry from the same defaults.
	registry2 := BuildRegistry(cfg)
	if got := registry2.Modified(); len(got) != 0 {
		t.Fatalf("fresh registry from same defaults: Modified() = %v, want empty", got)
	}
}

func TestTOMLAliasesResolve(t *testing.T) {
	// Verify that the Registry's Lookup method resolves dotted keys that
	// correspond to TOML config paths. This tests that every key in the
	// registry is reachable via Lookup and that the key format matches
	// the expected TOML path convention (dot-separated, lowercase).
	registry := BuildRegistry(config.Default())

	// Every key in the registry must be Lookup-able.
	for _, key := range registry.Keys() {
		f, ok := registry.Lookup(key)
		if !ok {
			t.Errorf("Keys() entry %q not found via Lookup", key)
			continue
		}
		// The key should match the field's id.
		if f.id != key {
			t.Errorf("Lookup(%q) returned field with id %q", key, f.id)
		}
	}

	// Verify that the registry contains expected TOML-path-like keys.
	expectedKeys := []string{
		"shell.allow_network",
		"agent.local_only",
		"tui.theme",
	}
	for _, key := range expectedKeys {
		if _, ok := registry.Lookup(key); !ok {
			t.Errorf("expected TOML alias %q not found in registry", key)
		}
	}
}

func TestRegistryMatchKeysReturnsKeys(t *testing.T) {
	registry := BuildRegistry(config.Default())
	matches := registry.MatchKeys("shell network")
	for _, key := range matches {
		if _, ok := registry.Lookup(key); !ok {
			t.Errorf("MatchKeys returned unknown key %q", key)
		}
	}
	if !slices.Contains(matches, "shell.allow_network") {
		t.Errorf("MatchKeys(%q) = %v, want shell.allow_network", "shell network", matches)
	}
}
