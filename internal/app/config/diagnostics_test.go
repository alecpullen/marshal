package config

import (
	"strings"
	"testing"

	"marshal/internal/llm/routing"
)

func diagPaths(ds []Diagnostic) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Path
	}
	return out
}

func hasPath(ds []Diagnostic, want string) bool {
	for _, d := range ds {
		if d.Path == want {
			return true
		}
	}
	return false
}

// projectLayerFor returns a Layers whose ProvenanceOf(dottedPath) reports
// the project layer (SetBy=LayerProject). It sets a dummy value at the path
// in Merged only, leaving User and Default empty at that path.
func projectLayerFor(t *testing.T, dottedPath string) Layers {
	t.Helper()
	def := Default()
	user := Default()
	merged := Default()

	segs := strings.Split(dottedPath, ".")
	if len(segs) >= 2 && segs[0] == "providers" {
		name := segs[1]
		if merged.Providers == nil {
			merged.Providers = map[string]ProviderConfig{}
		}
		pc := merged.Providers[name]
		switch segs[2] {
		case "api_key":
			pc.APIKey = "redacted"
		case "base_url":
			pc.BaseURL = "https://example.com"
		case "api_key_env":
			pc.APIKeyEnv = "SOME_VAR"
		}
		merged.Providers[name] = pc
	}

	return Layers{Default: def, User: user, Merged: merged}
}

func TestDiagnoseCleanConfigIsSilent(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	if ds := Diagnose(cfg, Layers{}); len(ds) != 0 {
		t.Errorf("clean config produced %v", diagPaths(ds))
	}
}

func TestDiagnoseProviderMissingBaseURL(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{"broken": {Type: "openai_compatible"}}
	ds := Diagnose(cfg, Layers{})
	if !hasPath(ds, "providers.broken.base_url") {
		t.Errorf("got %v, want a diagnostic for the missing base URL", diagPaths(ds))
	}
}

func TestDiagnoseInvalidBaseURL(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"broken": {Type: "openai_compatible", BaseURL: "not a url at all"},
	}
	if !hasPath(Diagnose(cfg, Layers{}), "providers.broken.base_url") {
		t.Error("want a diagnostic for the malformed base URL")
	}
}

func TestDiagnoseUnsetAPIKeyEnv(t *testing.T) {
	t.Setenv("MARSHAL_TEST_UNSET_KEY", "")
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "https://x.example/v1", APIKeyEnv: "MARSHAL_TEST_UNSET_KEY"},
	}
	if !hasPath(Diagnose(cfg, Layers{}), "providers.p.api_key_env") {
		t.Error("want a diagnostic for the unset env var")
	}
}

func TestDiagnoseAPIKeyEnvSilentWhenLiteralKeySet(t *testing.T) {
	t.Setenv("MARSHAL_TEST_UNSET_KEY", "")
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "https://x.example/v1", APIKey: "sk-present", APIKeyEnv: "MARSHAL_TEST_UNSET_KEY"},
	}
	if hasPath(Diagnose(cfg, Layers{}), "providers.p.api_key_env") {
		t.Error("want no diagnostic when api_key is present even if api_key_env is unset")
	}
}

func TestDiagnoseFixableEnvKeyIsReported(t *testing.T) {
	t.Setenv("MARSHAL_TEST_UNSET_KEY", "")
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "https://x.example/v1", APIKeyEnv: "MARSHAL_TEST_UNSET_KEY"},
	}
	ds := Diagnose(cfg, Layers{})
	if !hasPath(ds, "providers.p.api_key_env") {
		t.Errorf("want a diagnostic for the unset env var, got %v", diagPaths(ds))
	}
}

func TestDiagnoseSetAPIKeyEnvIsSilent(t *testing.T) {
	t.Setenv("MARSHAL_TEST_SET_KEY", "sk-present")
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "https://x.example/v1", APIKeyEnv: "MARSHAL_TEST_SET_KEY"},
	}
	if hasPath(Diagnose(cfg, Layers{}), "providers.p.api_key_env") {
		t.Error("a set env var should produce no diagnostic")
	}
}

func TestDiagnosePresetNamesMissingProvider(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{"real": {Type: "openai_compatible", BaseURL: "https://x.example/v1"}}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "deleted", Model: "m"},
	}
	if !hasPath(Diagnose(cfg, Layers{}), "models.presets.fast.provider") {
		t.Error("want a diagnostic for the dangling provider reference")
	}
}

func TestDiagnoseProfileRoleNamesMissingPreset(t *testing.T) {
	cfg := Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"main": {Name: "main", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "nonexistent"},
		}},
	}
	ds := Diagnose(cfg, Layers{})
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "nonexistent") {
		t.Errorf("want a diagnostic naming the missing preset, got %v", diagPaths(ds))
	}
}

func TestDiagnoseLiteralKeyInProjectConfigWarns(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "https://x.example/v1", APIKey: "sk-in-the-repo"},
	}
	ds := Diagnose(cfg, projectLayerFor(t, "providers.p.api_key"))
	if !hasPath(ds, "providers.p.api_key") {
		t.Errorf("want a warning for a literal key in project config, got %v", diagPaths(ds))
	}
	for _, d := range ds {
		if strings.Contains(d.Message, "sk-in-the-repo") {
			t.Error("diagnostic message must not echo the key material")
		}
	}
}

func TestDiagnoseLegacyAgentModelMigrationWarns(t *testing.T) {
	// Construct a legacy config on disk, load it through LoadLayers (which
	// runs MigrateLegacyAgentModel), then verify Diagnose reports the
	// deprecation warning at path "agent.provider".
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", `[agent]
provider = "openai"
model = "gpt-4o"

[profile]
default = ""

[providers.openai]
type = "openai_compatible"
base_url = "https://api.openai.com/v1"
`)
	writeFile(t, work+"/.marshal/config.toml", `[project]
name = "test"
`)

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if !l.Migrated {
		t.Fatal("expected Migrated=true for a legacy config")
	}

	ds := Diagnose(l.Merged, l)
	found := false
	for _, d := range ds {
		if d.Path == "agent.provider" {
			found = true
			if d.Severity != SeverityWarning {
				t.Errorf("severity = %v, want SeverityWarning", d.Severity)
			}
			if d.Message == "" {
				t.Error("empty diagnostic message")
			}
			break
		}
	}
	if !found {
		t.Fatalf("no diagnostic at path agent.provider; got %v", diagPaths(ds))
	}
}

// TestDiagnoseSilentOnValidMCPAuth guards against the new MCP auth check
// firing on a correctly configured server: both accepted values, with a url
// for the oauth case.
func TestDiagnoseSilentOnValidMCPAuth(t *testing.T) {
	for _, srv := range []MCPServerConfig{
		{URL: "https://mcp.example.com/mcp", Auth: "oauth", Trust: "unrestricted"},
		{Command: "mcp-server", Auth: ""},
	} {
		cfg := Default()
		cfg.MCP.Servers = map[string]MCPServerConfig{"ok": srv}
		if ds := Diagnose(cfg, Layers{}); len(ds) != 0 {
			t.Errorf("valid MCP auth produced %v", diagPaths(ds))
		}
	}
}

// TestDiagnoseMCPAuthOAuthWithoutURL: merge() copies auth verbatim, and
// manager.startRemote only consults it after the transport resolves to http,
// so auth = "oauth" on a server with no url is silently ignored without this
// diagnostic. That is the exact hand-edited-config case the write-time check
// in config.mcp.set cannot cover.
func TestDiagnoseMCPAuthOAuthWithoutURL(t *testing.T) {
	cfg := Default()
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"stdio-srv": {Command: "mcp-server", Auth: "oauth"},
	}
	ds := Diagnose(cfg, Layers{})
	if !hasPath(ds, "mcp.servers.stdio-srv.auth") {
		t.Fatalf("got %v, want a diagnostic for oauth without url", diagPaths(ds))
	}
	if ds[0].Severity != SeverityError {
		t.Errorf("severity = %v, want SeverityError (the setting does nothing)", ds[0].Severity)
	}
	if !strings.Contains(ds[0].Message, "ignored") {
		t.Errorf("message %q should say the setting is ignored", ds[0].Message)
	}
}

// TestDiagnoseMCPAuthUnknownValue: an unrecognized mode is ignored at
// runtime, so it must be reported rather than silently accepted.
func TestDiagnoseMCPAuthUnknownValue(t *testing.T) {
	cfg := Default()
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"srv": {URL: "https://mcp.example.com/mcp", Auth: "basic", Trust: "unrestricted"},
	}
	ds := Diagnose(cfg, Layers{})
	if !hasPath(ds, "mcp.servers.srv.auth") {
		t.Fatalf("got %v, want a diagnostic for the unknown auth value", diagPaths(ds))
	}
	if !strings.Contains(ds[0].Message, "basic") {
		t.Errorf("message %q should quote the bad value", ds[0].Message)
	}
	if !strings.Contains(ds[0].Message, "oauth") {
		t.Errorf("message %q should list the accepted values", ds[0].Message)
	}
}

// TestDiagnoseMCPAuthMatchesWriteTimeRule pins the load-time rule to the
// write-time one: config.mcp.set rejects oauth-without-http-transport and any
// value outside {"", "oauth"}. If that enum widens, this test should be
// updated alongside it rather than letting the two policies drift.
func TestDiagnoseMCPAuthMatchesWriteTimeRule(t *testing.T) {
	accepted := map[string]MCPServerConfig{
		"empty": {Command: "c"},
		"oauth": {URL: "https://mcp.example.com/mcp", Trust: "unrestricted"},
	}
	for name, srv := range accepted {
		srv.Auth = map[string]string{"empty": "", "oauth": "oauth"}[name]
		cfg := Default()
		cfg.MCP.Servers = map[string]MCPServerConfig{"s": srv}
		for _, d := range Diagnose(cfg, Layers{}) {
			if strings.HasPrefix(d.Path, "mcp.servers.") {
				t.Errorf("accepted auth %q should be silent, got %q", srv.Auth, d.Message)
			}
		}
	}
}

func TestDiagnoseOrdersErrorsBeforeWarnings(t *testing.T) {
	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{"broken": {Type: "openai_compatible"}}
	cfg.Models.Presets = map[string]routing.ModelPreset{"p": {Provider: "gone", Model: "m"}}
	ds := Diagnose(cfg, Layers{})
	for i := 1; i < len(ds); i++ {
		if ds[i-1].Severity > ds[i].Severity {
			t.Errorf("unsorted severities: %v", ds)
		}
	}
}
