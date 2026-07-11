package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func findDiff(lines []diffLine, path string) (diffLine, bool) {
	for _, l := range lines {
		if l.Path == path {
			return l, true
		}
	}
	return diffLine{}, false
}

func TestConfigDiffNoChanges(t *testing.T) {
	cfg := config.Default()
	lines := configDiff(cfg, cfg)
	if len(lines) != 0 {
		t.Fatalf("expected no diff lines, got %d: %v", len(lines), lines)
	}
}

func TestConfigDiffScalarChange(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Privacy.RemoteProvidersAllowed = true

	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Privacy.RemoteProvidersAllowed")
	if !ok {
		t.Fatalf("missing Privacy.RemoteProvidersAllowed in %v", lines)
	}
	if l.Prefix != "~" {
		t.Fatalf("prefix = %q, want ~", l.Prefix)
	}
	if !strings.Contains(l.Detail, "false") || !strings.Contains(l.Detail, "true") {
		t.Fatalf("detail = %q, want false → true", l.Detail)
	}
}

func TestConfigDiffAddedProvider(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}

	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Providers.ollama")
	if !ok {
		t.Fatalf("missing Providers.ollama in %v", lines)
	}
	if l.Prefix != "+" {
		t.Fatalf("prefix = %q, want +", l.Prefix)
	}
}

func TestConfigDiffRemovedPreset(t *testing.T) {
	before := config.Default()
	before.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}
	after := config.Default()

	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Models.Presets.coder")
	if !ok {
		t.Fatalf("missing Models.Presets.coder in %v", lines)
	}
	if l.Prefix != "-" {
		t.Fatalf("prefix = %q, want -", l.Prefix)
	}
}

func TestConfigDiffMasksAPIKey(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "sk-supersecret-1234"},
	}

	lines := configDiff(before, after)
	for _, l := range lines {
		if strings.Contains(l.Path, "APIKey") {
			if strings.Contains(l.Detail, "supersecret") {
				t.Fatalf("APIKey diff leaked plaintext: %q", l.Detail)
			}
			if !strings.Contains(l.Detail, "••••") {
				t.Fatalf("APIKey diff not masked: %q", l.Detail)
			}
		}
	}
}

func TestConfigDiffSliceChange(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Indexing.Ignore = append([]string{"newpattern/**"}, before.Indexing.Ignore...)

	lines := configDiff(before, after)
	found := false
	for _, l := range lines {
		if l.Prefix == "+" && strings.Contains(l.Path, "Indexing.Ignore") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an added Indexing.Ignore item in %v", lines)
	}
}
