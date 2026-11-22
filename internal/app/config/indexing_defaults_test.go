package config

import "testing"

// TestDefaultIndexingBounds covers the shipped index defaults: a default
// configuration prunes heavy directories and binary payloads, and caps both
// how much one file may contribute and how much content one pass retains.
func TestDefaultIndexingBounds(t *testing.T) {
	cfg := Default()
	if got, want := cfg.Indexing.MaxIndexableFileBytes, int64(2<<20); got != want {
		t.Errorf("MaxIndexableFileBytes = %d, want %d", got, want)
	}
	// Pinned to the value of repo.DefaultMaxTotalContentBytes, which this
	// package cannot name: importing internal/repo here would close a cycle
	// through internal/db, internal/subagentwork and internal/worktree.
	if got, want := cfg.Indexing.MaxTotalContentBytes, int64(512<<20); got != want {
		t.Errorf("MaxTotalContentBytes = %d, want %d", got, want)
	}
	for _, want := range []string{".lmstudio", "*.gguf", "*.safetensors", "Steam"} {
		found := false
		for _, pattern := range cfg.Indexing.Ignore {
			if pattern == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Ignore does not contain %q", want)
		}
	}
}
