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
	l, ok := findDiff(lines, "Providers.ollama.BaseURL")
	if !ok {
		t.Fatalf("missing Providers.ollama.BaseURL in %v", lines)
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
	l, ok := findDiff(lines, "Models.Presets.coder.Name")
	if !ok {
		t.Fatalf("missing Models.Presets.coder.Name in %v", lines)
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

	// No plaintext secret appears anywhere in the diff.
	for _, l := range lines {
		if strings.Contains(l.Detail, "supersecret") {
			t.Fatalf("diff leaked plaintext secret in line %s%s: %q", l.Prefix, l.Path, l.Detail)
		}
	}

	// The APIKey field is present in the diff and its detail is masked.
	foundMasked := false
	for _, l := range lines {
		if strings.Contains(l.Path, "APIKey") {
			if strings.Contains(l.Detail, "••••") {
				foundMasked = true
			}
		}
	}
	if !foundMasked {
		t.Fatalf("expected masked APIKey field in diff lines: %+v", lines)
	}
}

func TestConfigDiffMasksCDPURLCredentials(t *testing.T) {
	before := config.Default()
	after := config.Default()
	after.Desktop.CDPURL = "http://user:supersecretpw@localhost:9222"

	lines := configDiff(before, after)

	// The password must never appear in the diff.
	for _, l := range lines {
		if strings.Contains(l.Detail, "supersecretpw") {
			t.Fatalf("diff leaked CDPURL password in line %s%s: %q", l.Prefix, l.Path, l.Detail)
		}
	}

	// The CDPURL field is present and its userinfo is masked (url.User
	// percent-encodes the placeholder) while the host is preserved for
	// debugging.
	found := false
	for _, l := range lines {
		if strings.Contains(l.Path, "CDPURL") {
			found = true
			if strings.Contains(l.Detail, "supersecretpw") {
				t.Fatalf("CDPURL password leaked in %q", l.Detail)
			}
			if !strings.Contains(l.Detail, "localhost:9222") {
				t.Fatalf("expected host preserved in %q", l.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("expected a CDPURL diff line, got: %+v", lines)
	}
}

func TestConfigDiffAgentRoleKeyedMap(t *testing.T) {
	before := config.Default()
	before.Agents = map[routing.AgentRole]config.AgentRoleConfig{
		routing.RoleImplementer: {Context: routing.ContextBudget{MaxRepoContextTokens: 48000}},
	}
	after := config.Default()
	after.Agents = map[routing.AgentRole]config.AgentRoleConfig{
		routing.RoleImplementer: {Context: routing.ContextBudget{MaxRepoContextTokens: 64000}},
	}

	// Must not panic: map keys are routing.AgentRole, not plain string.
	lines := configDiff(before, after)
	l, ok := findDiff(lines, "Agents.implementer.Context.MaxRepoContextTokens")
	if !ok {
		t.Fatalf("missing Agents.implementer.Context.MaxRepoContextTokens in %v", lines)
	}
	if l.Prefix != "~" {
		t.Fatalf("prefix = %q, want ~", l.Prefix)
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
