package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeHome points os.UserHomeDir at a temp dir and returns it.
func fakeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// projectWithConfig returns a temp project root containing
// .marshal/config.toml, so trust is actually relevant to it.
func projectWithConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte("# project config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// writeTrustStore writes ~/.local/share/marshal/trust.json with the given
// raw body.
func writeTrustStore(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "marshal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trust.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectTrustNoProjectConfigIsNotApplicable(t *testing.T) {
	fakeHome(t)
	// A project with no .marshal/config.toml: trust changes nothing for
	// it, so reporting "untrusted" would be a false alarm.
	if got := projectTrust(t.TempDir()); got != "na" {
		t.Fatalf("projectTrust = %q, want \"na\"", got)
	}
}

func TestProjectTrustConfigWithoutStoreIsUntrusted(t *testing.T) {
	fakeHome(t)
	root := projectWithConfig(t)
	// No trust.json at all — the common "never opened in the TUI" case.
	if got := projectTrust(root); got != "untrusted" {
		t.Fatalf("projectTrust = %q, want \"untrusted\"", got)
	}
}

func TestProjectTrustConfigWithoutRecordIsUntrusted(t *testing.T) {
	home := fakeHome(t)
	root := projectWithConfig(t)
	writeTrustStore(t, home, `{"/some/other/project":{"trusted":true}}`)
	if got := projectTrust(root); got != "untrusted" {
		t.Fatalf("projectTrust = %q, want \"untrusted\"", got)
	}
}

func TestProjectTrustRecordWithTrustedFalseIsUntrusted(t *testing.T) {
	home := fakeHome(t)
	root := projectWithConfig(t)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolved = root
	}
	// A record exists but explicitly withholds trust.
	writeTrustStore(t, home, `{"`+resolved+`":{"trusted":false}}`)
	if got := projectTrust(root); got != "untrusted" {
		t.Fatalf("projectTrust = %q, want \"untrusted\"", got)
	}
}

func TestProjectTrustRecordKeyedByResolvedPathIsTrusted(t *testing.T) {
	home := fakeHome(t)
	root := projectWithConfig(t)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	writeTrustStore(t, home, `{"`+resolved+`":{"trusted":true,"config_hash":"abc"}}`)
	if got := projectTrust(root); got != "trusted" {
		t.Fatalf("projectTrust = %q, want \"trusted\"", got)
	}
}

func TestProjectTrustRecordKeyedByRawPathIsTrusted(t *testing.T) {
	home := fakeHome(t)
	root := projectWithConfig(t)
	// On macOS t.TempDir() sits under a symlinked /var, so the raw and
	// resolved paths differ. A store written against the raw path must
	// still count.
	writeTrustStore(t, home, `{"`+root+`":{"trusted":true}}`)
	if got := projectTrust(root); got != "trusted" {
		t.Fatalf("projectTrust = %q, want \"trusted\"", got)
	}
}

func TestProjectTrustCorruptStoreDoesNotClaimTrust(t *testing.T) {
	home := fakeHome(t)
	root := projectWithConfig(t)
	writeTrustStore(t, home, "{not json")
	// Failing closed matters here: claiming trust on an unreadable store
	// would tell the user their project config is active when it is not.
	if got := projectTrust(root); got != "untrusted" {
		t.Fatalf("projectTrust = %q, want \"untrusted\"", got)
	}
}
