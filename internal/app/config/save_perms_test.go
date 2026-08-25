package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUserConfigWritesAreOwnerOnly pins S-01: the user-global config stores
// provider API keys inline, and was written 0644 into a 0755 directory —
// readable by every other user on the machine.
func TestUserConfigWritesAreOwnerOnly(t *testing.T) {
	cases := []struct {
		name  string
		write func(path string) error
	}{
		{"api key", func(p string) error {
			return SaveUserConfigProviderAPIKey(p, "openai", ProviderConfig{APIKey: "sk-secret-value"})
		}},
		{"rule", func(p string) error {
			return SaveUserConfigRule(p, PermissionRule{Permission: "shell.run", Pattern: "go test", Action: "allow"})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "marshal")
			path := filepath.Join(dir, "config.toml")

			if err := tc.write(path); err != nil {
				t.Fatalf("write: %v", err)
			}

			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := fi.Mode().Perm(); perm&0o077 != 0 {
				t.Errorf("config file mode = %04o, want no group/other access", perm)
			}
			di, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if perm := di.Mode().Perm(); perm&0o077 != 0 {
				t.Errorf("config dir mode = %04o, want no group/other access", perm)
			}
		})
	}
}

// TestUserConfigTightensExistingPermissions pins the migration half: a config
// written by an older build keeps its 0644 forever otherwise, because
// os.WriteFile only applies its mode when creating the file — so the leak
// would silently survive the fix.
func TestUserConfigTightensExistingPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "marshal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("# pre-existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{APIKey: "sk-secret-value"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("pre-existing config not tightened: mode = %04o", perm)
	}

	// The key must actually be there — a permissions fix that lost the write
	// would pass the check above for the wrong reason.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sk-secret-value") {
		t.Error("API key was not persisted")
	}
}
