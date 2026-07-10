package settings

import (
	"path/filepath"
	"testing"

	"marshal/internal/app/config"
)

func TestIntegrationEditAndSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	m := New(config.Default(), dir, path)
	m.SetSize(100, 40)

	m = enterSection(t, m, "privacy")
	m = keyPress(m, "space")
	if !m.state.cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("privacy toggle did not take")
	}

	m = enterSection(t, m, "providers")
	m = keyPress(m, "a", "o", "l", "l", "a", "m", "a", "enter")
	if _, ok := m.state.cfg.Providers["ollama"]; !ok {
		t.Fatal("provider add did not take")
	}

	if !m.dirty() {
		t.Fatal("model should be dirty after edits")
	}

	_, cmd := m.Update(keyMsg("ctrl+s"))
	if cmd == nil {
		t.Fatal("ctrl+s should produce a save command")
	}
	msg := cmd()
	saved, ok := msg.(SavedMsg)
	if !ok {
		t.Fatalf("expected SavedMsg, got %T", msg)
	}

	if !saved.Cfg.Privacy.RemoteProvidersAllowed {
		t.Error("saved config lost the privacy edit")
	}
	if _, ok := saved.Cfg.Providers["ollama"]; !ok {
		t.Error("saved config lost the provider entry")
	}

	loaded, err := config.Load(config.LoadOptions{HomeDir: filepath.Join(dir, "no-home"), WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !loaded.Privacy.RemoteProvidersAllowed {
		t.Error("disk config lost the privacy edit")
	}
	if _, ok := loaded.Providers["ollama"]; !ok {
		t.Error("disk config lost the provider entry")
	}
}
