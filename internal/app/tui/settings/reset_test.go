package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestResetSectionProviders(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	resetSection(&cfg, "providers")
	if cfg.Providers != nil {
		t.Fatalf("providers = %v, want nil", cfg.Providers)
	}
}

func TestResetSectionShell(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.AllowNetwork = true
	cfg.Tools.Shell.AutoApprove = true
	resetSection(&cfg, "shell")
	def := config.Default()
	if cfg.Tools.Shell.AllowNetwork != def.Tools.Shell.AllowNetwork {
		t.Fatalf("AllowNetwork = %v, want %v", cfg.Tools.Shell.AllowNetwork, def.Tools.Shell.AllowNetwork)
	}
}

func TestResetFieldConfirmThenApply(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	f := resetField(st, "providers", "Providers")

	if label := f.ActLabel(); label != "reset to defaults" {
		t.Fatalf("idle label = %q, want 'reset to defaults'", label)
	}

	_ = f.Act()
	if label := f.ActLabel(); label != "again to confirm" {
		t.Fatalf("armed label = %q, want 'again to confirm'", label)
	}

	_ = f.Act()
	if label := f.ActLabel(); label != "✓ reset" {
		t.Fatalf("applied label = %q, want '✓ reset'", label)
	}
	if st.cfg.Providers != nil {
		t.Fatal("reset should have cleared providers")
	}
}

func TestResetFieldDisarmClearsConfirm(t *testing.T) {
	st := newState(config.Default())
	f := resetField(st, "shell", "Shell")
	_ = f.Act()
	if f.ActLabel() != "again to confirm" {
		t.Fatal("should be armed")
	}
	if f.Disarm != nil {
		f.Disarm()
	}
	if f.ActLabel() != "reset to defaults" {
		t.Fatalf("after disarm label = %q, want 'reset to defaults'", f.ActLabel())
	}
}

func TestEverySectionRootHasResetRow(t *testing.T) {
	state := newState(config.Default())
	for _, sp := range sectionList() {
		rows := withResetRow(state, sp.ID, sp.Title, sp.Root(state)).List.Rows()
		found := false
		for _, r := range rows {
			if r.Kind == kindAction && strings.HasPrefix(r.ID, sp.ID+".reset") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("section %q has no reset row", sp.ID)
		}
	}
}
