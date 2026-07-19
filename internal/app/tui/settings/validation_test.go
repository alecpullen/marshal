package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestWarningsRemoteProvidersNoProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	w := warningsFor("agent", cfg)
	if len(w) == 0 {
		t.Fatal("expected a remote-providers/no-provider warning")
	}
}

func TestWarningsContainerNoImage(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Sandbox.Backend = "container"
	cfg.Tools.Shell.Sandbox.ContainerImage = ""
	w := warningsFor("sandbox", cfg)
	if len(w) == 0 {
		t.Fatal("expected container/no-image warning")
	}
}

func TestWarningsProviderPlaintextKey(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{"a": {APIKey: "sk-1234"}}
	w := warningsFor("providers", cfg)
	found := false
	for _, msg := range w {
		if strings.Contains(msg, "plaintext") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plaintext-key info warning, got %v", w)
	}
}

func TestWarningsNoneForSafeConfig(t *testing.T) {
	w := warningsFor("web", config.Default())
	if len(w) != 0 {
		t.Fatalf("web section should have no warnings, got %v", w)
	}
}
