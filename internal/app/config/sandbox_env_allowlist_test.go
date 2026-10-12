package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sandboxAllowlistDiag(ds []Diagnostic) *Diagnostic {
	for i := range ds {
		if ds[i].Path == "tools.shell.sandbox.env_allowlist" {
			return &ds[i]
		}
	}
	return nil
}

// TestDiagnoseWarnsOnExplicitEmptySandboxEnvAllowlist covers the "make the
// sentinel loud" half of the hardening. An explicit-empty allowlist is what
// silently broke every `go` invocation in the shell, and it is reachable by
// accident from /settings, so it is reported rather than silently honoured.
func TestDiagnoseWarnsOnExplicitEmptySandboxEnvAllowlist(t *testing.T) {
	cfg := Default()
	if got := sandboxAllowlistDiag(Diagnose(cfg, Layers{})); got != nil {
		t.Fatalf("default config must not warn, got %q", got.Message)
	}

	cfg.Tools.Shell.Sandbox.EnvAllowlist = []string{}
	got := sandboxAllowlistDiag(Diagnose(cfg, Layers{}))
	if got == nil {
		t.Fatal("explicit-empty allowlist produced no diagnostic")
	}
	if got.Severity != SeverityWarning {
		t.Errorf("severity = %v, want SeverityWarning", got.Severity)
	}
	if !strings.Contains(got.Message, "GOPATH") {
		t.Errorf("message should name the toolchain consequence, got %q", got.Message)
	}

	// A curated non-empty list — even a single entry — is a deliberate,
	// working choice and must stay quiet.
	cfg.Tools.Shell.Sandbox.EnvAllowlist = []string{"PATH"}
	if got := sandboxAllowlistDiag(Diagnose(cfg, Layers{})); got != nil {
		t.Errorf("non-empty allowlist warned: %q", got.Message)
	}
}

// TestDiagnoseAttributesEmptyAllowlistToItsLayer pins that the warning names
// the file carrying the `[]`. Both layers can hold the value, and which one
// does is the difference between "fix your project config" and "fix your
// user-global config".
func TestDiagnoseAttributesEmptyAllowlistToItsLayer(t *testing.T) {
	def := Default()
	user := Default()
	user.Tools.Shell.Sandbox.EnvAllowlist = []string{}

	cfg := Default()
	cfg.Tools.Shell.Sandbox.EnvAllowlist = []string{}

	got := sandboxAllowlistDiag(Diagnose(cfg, Layers{Default: def, User: user, Merged: cfg}))
	if got == nil {
		t.Fatal("explicit-empty allowlist produced no diagnostic")
	}
	if got.Source != LayerUser.String() {
		t.Errorf("source = %q, want %q", got.Source, LayerUser.String())
	}
}

// TestSaveProjectConfigPersistsEnvAllowlistReset covers the "reset that
// actually sticks" half. [] is a meaningful value for this key, not a deleted
// key, so putSlice's len(fileVal) > 0 rule wrote nothing on a reset — the
// empty list stayed in the file and won the next merge, making the reset look
// like a no-op. putEnvAllowlist treats a present-but-empty file value as
// present, so the reset persists.
func TestSaveProjectConfigPersistsEnvAllowlistReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[tools.shell.sandbox]\nenv_allowlist = []\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if al := cfg.Tools.Shell.Sandbox.EnvAllowlist; al == nil || len(al) != 0 {
		t.Fatalf("explicit-empty allowlist did not survive the merge: %#v", al)
	}

	// The settings reset: restore this section's built-in defaults.
	cfg.Tools.Shell.Sandbox.EnvAllowlist = Default().Tools.Shell.Sandbox.EnvAllowlist
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The reset must override the empty list that was in the file. Whether it
	// writes the default list or drops the key is an implementation detail;
	// what matters is that the empty value no longer survives.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "env_allowlist = []") {
		t.Fatalf("reset left the explicit-empty list in place:\n%s", raw)
	}

	// The real requirement: the reset has to survive a *second* layer that
	// still carries []. Writing nothing (the old len(fileVal) > 0 rule) let
	// the user-global list win the next merge, which is what made the reset
	// look like a no-op.
	writeFile(t, filepath.Join(dir, ".config", "marshal", "config.toml"),
		"[tools.shell.sandbox]\nenv_allowlist = []\n")

	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if want := Default().Tools.Shell.Sandbox.EnvAllowlist; !reflect.DeepEqual(loaded.Tools.Shell.Sandbox.EnvAllowlist, want) {
		t.Fatalf("after reset allowlist = %#v, want defaults %#v (the user layer's [] won the merge)", loaded.Tools.Shell.Sandbox.EnvAllowlist, want)
	}
}

// TestSaveProjectConfigKeepsExistingEmptyEnvAllowlist documents where the
// reset can and cannot help. When the file already carries env_allowlist = []
// and the save is not changing the value, the entry is left alone: the save
// layer cannot tell "the user deliberately denied all env" from "the user just
// pressed reset and the merged value happens to equal the default", so it
// preserves what is there rather than guessing. /settings offers the targeted
// restore action for that case, and /doctor reports the condition.
func TestSaveProjectConfigKeepsExistingEmptyEnvAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[tools.shell.sandbox]\nenv_allowlist = []\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if al := loaded.Tools.Shell.Sandbox.EnvAllowlist; al == nil || len(al) != 0 {
		t.Fatalf("deliberate explicit-empty allowlist was not preserved: %#v", al)
	}
}

// TestSaveProjectConfigKeepsExplicitEmptyEnvAllowlist is the converse: the
// sentinel must remain storable. A save that carries an explicit-empty list
// has to write it, or a user who deliberately denies the environment could
// not express that choice at all.
func TestSaveProjectConfigKeepsExplicitEmptyEnvAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[agent]\nmax_retries = 5\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Tools.Shell.Sandbox.EnvAllowlist = []string{}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if al := loaded.Tools.Shell.Sandbox.EnvAllowlist; al == nil || len(al) != 0 {
		t.Fatalf("explicit-empty allowlist = %#v, want empty non-nil", al)
	}
}

// TestSaveProjectConfigDoesNotBakeDefaultEnvAllowlist guards the ordinary
// case: saving a config whose allowlist merely holds the default, into a file
// that never mentioned the key, must not start writing it. That would pin
// machine-local defaults into a shared, often-committed project file.
func TestSaveProjectConfigDoesNotBakeDefaultEnvAllowlist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[agent]\nmax_retries = 5\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "env_allowlist") {
		t.Errorf("default allowlist was baked into the project config:\n%s", raw)
	}
}
