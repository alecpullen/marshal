package config

import (
	"testing"

	"marshal/internal/limits"
)

// TestDefaultIndexingBounds covers the shipped index defaults: a default
// configuration prunes heavy directories and binary payloads, and caps both
// how much one file may contribute and how much content one pass retains.
func TestDefaultIndexingBounds(t *testing.T) {
	cfg := Default()
	if got, want := cfg.Indexing.MaxIndexableFileBytes, int64(2<<20); got != want {
		t.Errorf("MaxIndexableFileBytes = %d, want %d", got, want)
	}
	// The bound is pinned as a number so changing it is a deliberate act, and
	// tied to internal/limits so the scanner and this default cannot drift:
	// internal/repo cannot be imported here (it reaches this package through
	// internal/db, internal/subagentwork and internal/worktree), which is why
	// the value lives in a leaf package both sides may name.
	if got, want := cfg.Indexing.MaxTotalContentBytes, int64(512<<20); got != want {
		t.Errorf("MaxTotalContentBytes = %d, want %d", got, want)
	}
	if got, want := cfg.Indexing.MaxTotalContentBytes, limits.MaxTotalContentBytes; got != want {
		t.Errorf("MaxTotalContentBytes = %d, limits.MaxTotalContentBytes = %d; the default must be the shared bound", got, want)
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
