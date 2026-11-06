package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/strutil"
)

func TestMaskSecrets(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-secret", APIKeyEnv: "OPENAI_API_KEY", BaseURL: "https://api.openai.com"},
		},
		Web: WebConfig{SearchKey: "search-secret", SearchURL: "https://x"},
	}
	masked := MaskSecrets(cfg)
	if masked.Providers["openai"].APIKey != "***" {
		t.Fatalf("api_key = %q, want ***", masked.Providers["openai"].APIKey)
	}
	if masked.Providers["openai"].APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("api_key_env should be preserved, got %q", masked.Providers["openai"].APIKeyEnv)
	}
	if masked.Providers["openai"].BaseURL != "https://api.openai.com" {
		t.Fatalf("base_url should be preserved, got %q", masked.Providers["openai"].BaseURL)
	}
	if masked.Web.SearchKey != "***" {
		t.Fatalf("search_key = %q, want ***", masked.Web.SearchKey)
	}
	if masked.Web.SearchURL != "https://x" {
		t.Fatalf("search_url should be preserved, got %q", masked.Web.SearchURL)
	}
	// Original must be untouched.
	if cfg.Providers["openai"].APIKey != "sk-secret" {
		t.Fatal("MaskSecrets mutated the input")
	}
}

func TestMaskSecretsCDPURL(t *testing.T) {
	cfg := Default()
	cfg.Desktop.CDPURL = "ws://user:pass@localhost:9222"
	masked := MaskSecrets(cfg)
	if strings.Contains(masked.Desktop.CDPURL, "user:pass") {
		t.Errorf("CDPURL credentials not masked: %s", masked.Desktop.CDPURL)
	}
	if !strings.Contains(masked.Desktop.CDPURL, "localhost:9222") {
		t.Errorf("CDPURL host should be preserved: %s", masked.Desktop.CDPURL)
	}
	if cfg.Desktop.CDPURL != "ws://user:pass@localhost:9222" {
		t.Error("input config must not be mutated")
	}
}

func TestRedactSecretsMasksTheSecretSet(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"openai":    {APIKey: "sk-openai-4567", BaseURL: "https://api.openai.com"},
			"anthropic": {APIKey: "sk-ant-8901"},
			"local":     {BaseURL: "http://localhost:11434"},
		},
		Web:     WebConfig{SearchKey: "search-key-2345", SearchURL: "https://search"},
		Desktop: DesktopConfig{CDPURL: "ws://user:hunter2@localhost:9222"},
	}
	red := RedactSecrets(cfg)

	if got, want := red.Providers["openai"].APIKey, strutil.MaskKey("sk-openai-4567"); got != want {
		t.Errorf("openai api_key = %q, want %q", got, want)
	}
	if got, want := red.Providers["anthropic"].APIKey, strutil.MaskKey("sk-ant-8901"); got != want {
		t.Errorf("anthropic api_key = %q, want %q", got, want)
	}
	if got := red.Providers["local"].APIKey; got != "" {
		t.Errorf("empty api_key = %q, want empty", got)
	}
	if got, want := red.Web.SearchKey, strutil.MaskKey("search-key-2345"); got != want {
		t.Errorf("search_key = %q, want %q", got, want)
	}
	if strings.Contains(red.Desktop.CDPURL, "hunter2") || strings.Contains(red.Desktop.CDPURL, "user:") {
		t.Errorf("cdp_url credentials not masked: %s", red.Desktop.CDPURL)
	}

	encoded, err := json.Marshal(red)
	if err != nil {
		t.Fatalf("marshal redacted config: %v", err)
	}
	for _, secret := range []string{"sk-openai-4567", "sk-ant-8901", "search-key-2345", "hunter2"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("redacted config still contains %q: %s", secret, encoded)
		}
	}

	// The input must be untouched.
	if cfg.Providers["openai"].APIKey != "sk-openai-4567" {
		t.Error("RedactSecrets mutated the input providers map")
	}
	if cfg.Web.SearchKey != "search-key-2345" {
		t.Error("RedactSecrets mutated the input search key")
	}
	if cfg.Desktop.CDPURL != "ws://user:hunter2@localhost:9222" {
		t.Error("RedactSecrets mutated the input cdp url")
	}
}

func TestRestoreSecretsInvertsTheMask(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"openai":    {APIKey: "sk-openai-4567", BaseURL: "https://api.openai.com", APIKeyEnv: "OPENAI_API_KEY"},
			"anthropic": {APIKey: "sk-ant-8901"},
			"local":     {BaseURL: "http://localhost:11434"},
		},
		Web:     WebConfig{SearchKey: "search-key-2345", SearchURL: "https://search"},
		Desktop: DesktopConfig{Enabled: true, CDPURL: "ws://user:hunter2@localhost:9222"},
	}
	got, err := RestoreSecrets(RedactSecrets(cfg), nil, cfg)
	if err != nil {
		t.Fatalf("RestoreSecrets: %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("RestoreSecrets(RedactSecrets(cfg)) = %+v, want %+v", got, cfg)
	}
}

func TestRestoreSecretsKeepsLiteralsAndEmpties(t *testing.T) {
	client := Config{
		Providers: map[string]ProviderConfig{
			"openai": {APIKey: "sk-typed-9999"},
			"local":  {},
		},
		Web:     WebConfig{SearchKey: "typed-search-key"},
		Desktop: DesktopConfig{CDPURL: "ws://localhost:9222"},
	}
	got, err := RestoreSecrets(client, nil, Config{
		Providers: map[string]ProviderConfig{"openai": {APIKey: "sk-stored-1234"}},
		Web:       WebConfig{SearchKey: "stored-search-1234"},
	})
	if err != nil {
		t.Fatalf("RestoreSecrets: %v", err)
	}
	if !reflect.DeepEqual(got, client) {
		t.Fatalf("RestoreSecrets kept nothing: got %+v, want %+v", got, client)
	}
}

func TestRestoreSecretsFollowsARename(t *testing.T) {
	client := Config{Providers: map[string]ProviderConfig{"new": {APIKey: strutil.MaskKey("sk-old-1234")}}}
	stored := Config{Providers: map[string]ProviderConfig{"old": {APIKey: "sk-old-1234"}}}

	got, err := RestoreSecrets(client, map[string]string{"new": "old"}, stored)
	if err != nil {
		t.Fatalf("RestoreSecrets with rename: %v", err)
	}
	if key := got.Providers["new"].APIKey; key != "sk-old-1234" {
		t.Fatalf("renamed api_key = %q, want sk-old-1234", key)
	}

	// Without the rename map the unique-match step finds the same key.
	got, err = RestoreSecrets(client, nil, stored)
	if err != nil {
		t.Fatalf("RestoreSecrets without rename: %v", err)
	}
	if key := got.Providers["new"].APIKey; key != "sk-old-1234" {
		t.Fatalf("unique-match api_key = %q, want sk-old-1234", key)
	}

	// A second stored key with the same last four characters is ambiguous.
	ambiguous := Config{Providers: map[string]ProviderConfig{
		"old":   {APIKey: "sk-old-1234"},
		"other": {APIKey: "sk-other-1234"},
	}}
	_, err = RestoreSecrets(client, nil, ambiguous)
	want := "config: providers.new.api_key: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("ambiguous mask error = %v, want %q", err, want)
	}
}

func TestRestoreSecretsPrefersTheFirstStoredConfig(t *testing.T) {
	client := Config{Providers: map[string]ProviderConfig{"openai": {APIKey: strutil.MaskKey("sk-first-5678")}}}
	first := Config{Providers: map[string]ProviderConfig{"openai": {APIKey: "sk-first-5678"}}}
	second := Config{Providers: map[string]ProviderConfig{"openai": {APIKey: "sk-second-5678"}}}

	got, err := RestoreSecrets(client, nil, first, second)
	if err != nil {
		t.Fatalf("RestoreSecrets: %v", err)
	}
	if key := got.Providers["openai"].APIKey; key != "sk-first-5678" {
		t.Fatalf("api_key = %q, want sk-first-5678", key)
	}
}

func TestRestoreSecretsFailsOnAnUnmatchedMask(t *testing.T) {
	client := Config{
		Providers: map[string]ProviderConfig{"openai": {APIKey: "••••zzzz"}},
	}
	got, err := RestoreSecrets(client, nil, Config{
		Providers: map[string]ProviderConfig{"openai": {APIKey: "sk-real-1234"}},
	})
	want := "config: providers.openai.api_key: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("provider mask error = %v, want %q", err, want)
	}
	if key := got.Providers["openai"].APIKey; key != "••••zzzz" {
		t.Fatalf("failed restore changed the client: api_key = %q", key)
	}

	searchClient := Config{Web: WebConfig{SearchKey: "••••qqqq"}}
	_, err = RestoreSecrets(searchClient, nil, Config{Web: WebConfig{SearchKey: "stored-search-1234"}})
	want = "config: web.search_key: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("search key mask error = %v, want %q", err, want)
	}

	cdpClient := Config{Desktop: DesktopConfig{CDPURL: "ws://***@10.0.0.9:9222"}}
	_, err = RestoreSecrets(cdpClient, nil, Config{Desktop: DesktopConfig{CDPURL: "ws://user:pass@127.0.0.1:9222"}})
	want = "config: desktop.cdp_url: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("cdp url mask error = %v, want %q", err, want)
	}
}

// TestRestoreSecretsSkipsAMaskedStoredSecret pins the circular-match guard: a
// stored value that is itself a mask never comes back as the secret, because
// masking it is a no-op.
func TestRestoreSecretsSkipsAMaskedStoredSecret(t *testing.T) {
	masked := strutil.MaskKey("sk-real-1234")
	_, err := RestoreSecrets(
		Config{Providers: map[string]ProviderConfig{"openai": {APIKey: masked}}},
		nil,
		Config{Providers: map[string]ProviderConfig{"openai": {APIKey: masked}}},
	)
	want := "config: providers.openai.api_key: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("provider error = %v, want %q", err, want)
	}

	searchMask := strutil.MaskKey("search-real-1234")
	_, err = RestoreSecrets(
		Config{Web: WebConfig{SearchKey: searchMask}},
		nil,
		Config{Web: WebConfig{SearchKey: searchMask}},
	)
	want = "config: web.search_key: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("search key error = %v, want %q", err, want)
	}

	cdpMask := maskURLUserinfo("ws://user:pass@127.0.0.1:9222")
	_, err = RestoreSecrets(
		Config{Desktop: DesktopConfig{CDPURL: cdpMask}},
		nil,
		Config{Desktop: DesktopConfig{CDPURL: cdpMask}},
	)
	want = "config: desktop.cdp_url: no stored secret matches its mask"
	if err == nil || err.Error() != want {
		t.Fatalf("cdp url error = %v, want %q", err, want)
	}
}

// TestRedactLayersMasksEveryLayer pins that no layer — including the merged
// config — hands a literal secret to the caller.
func TestRedactLayersMasksEveryLayer(t *testing.T) {
	layer := func() Config {
		return Config{
			Providers: map[string]ProviderConfig{"openai": {APIKey: "sk-layer-1234"}},
			Web:       WebConfig{SearchKey: "search-layer-1234"},
		}
	}
	got := RedactLayers(Layers{Default: layer(), User: layer(), Merged: layer()})
	keyMask, searchMask := strutil.MaskKey("sk-layer-1234"), strutil.MaskKey("search-layer-1234")
	for name, cfg := range map[string]Config{"Default": got.Default, "User": got.User, "Merged": got.Merged} {
		if key := cfg.Providers["openai"].APIKey; key != keyMask {
			t.Errorf("%s api_key = %q, want %q", name, key, keyMask)
		}
		if key := cfg.Web.SearchKey; key != searchMask {
			t.Errorf("%s search_key = %q, want %q", name, key, searchMask)
		}
	}
}
