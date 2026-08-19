package config

import (
	"strings"
	"testing"
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
