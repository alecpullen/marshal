package settings

import (
	"slices"
	"strings"
	"testing"

	"marshal/internal/app/config"
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
	oldValue, newValue, err := registry.Apply("shell.allow_network", "on")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if newValue != "on" {
		t.Errorf("newValue = %q, want on", newValue)
	}
	if oldValue == newValue {
		t.Errorf("old and new both %q — toggle did not report a change", oldValue)
	}
	if !registry.Config().Tools.Shell.AllowNetwork {
		t.Error("Apply did not mutate the working config")
	}
}

func TestRegistryApplyErrors(t *testing.T) {
	registry := BuildRegistry(config.Default())
	if _, _, err := registry.Apply("no.such.key", "x"); err == nil {
		t.Error("unknown key must error")
	}
	if _, _, err := registry.Apply("shell.allow_network", "maybe"); err == nil {
		t.Error("bad bool must error")
	}
	for _, key := range registry.Keys() {
		field, _ := registry.Lookup(key)
		if field.kind != kindEnum || field.setStr == nil {
			continue
		}
		if _, _, err := registry.Apply(key, "definitely-not-an-option"); err == nil ||
			!strings.Contains(err.Error(), "one of") {
			t.Errorf("enum %s: want 'must be one of' error, got %v", key, err)
		}
		return
	}
	t.Error("registry has no writable enum field")
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
