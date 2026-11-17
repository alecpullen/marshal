package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadOrCreateToken pins the token hand-off the local profile relies on: a
// 0600 file that a restart reuses, so an open Studio tab keeps working, and
// that is minted when it is missing or left empty.
func TestReadOrCreateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "studio.token")

	tok, err := readOrCreateToken(path)
	if err != nil {
		t.Fatalf("readOrCreateToken: %v", err)
	}
	if len(tok) != 32 {
		t.Errorf("token %q is %d characters, want a 128-bit hex token", tok, len(tok))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s is mode %04o, want 0600: it holds a credential", path, perm)
	}

	// A restart reuses the token rather than minting a new one.
	again, err := readOrCreateToken(path)
	if err != nil {
		t.Fatalf("readOrCreateToken on an existing file: %v", err)
	}
	if again != tok {
		t.Errorf("second read = %q, want the token already on disk %q", again, tok)
	}

	// A file someone filled in is used as it stands.
	if err := os.WriteFile(path, []byte("preset\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	preset, err := readOrCreateToken(path)
	if err != nil {
		t.Fatalf("readOrCreateToken on a preset file: %v", err)
	}
	if preset != "preset" {
		t.Errorf("token = %q, want the file's own %q", preset, "preset")
	}

	// An emptied file is minted rather than used as an empty credential.
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("empty %s: %v", path, err)
	}
	fresh, err := readOrCreateToken(path)
	if err != nil {
		t.Fatalf("readOrCreateToken on an empty file: %v", err)
	}
	if fresh == "" || fresh == "preset" {
		t.Errorf("token from an empty file = %q, want a freshly minted one", fresh)
	}
}
