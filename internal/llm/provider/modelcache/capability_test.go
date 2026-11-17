package modelcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverridesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	in := CapabilityOverrides{Rejections: map[string]Rejection{
		"ollama/llava": {ObservedAt: now, ConfigHash: "hash-a"},
	}}
	if err := SaveOverrides(dir, in); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	got := LoadOverrides(dir)
	if len(got.Rejections) != 1 {
		t.Fatalf("loaded %d rejections, want 1", len(got.Rejections))
	}
	if !got.VisionRejected("ollama", "llava", "hash-a", DefaultCapabilityTTL, now) {
		t.Fatalf("VisionRejected = false, want true")
	}
	// A pair with no entry is never rejected.
	if got.VisionRejected("ollama", "qwen2.5-coder:7b", "hash-a", DefaultCapabilityTTL, now) {
		t.Fatalf("VisionRejected for an unrecorded pair = true, want false")
	}
}

func TestOverridesExpire(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	in := CapabilityOverrides{Rejections: map[string]Rejection{
		"ollama/llava": {ObservedAt: now.Add(-31 * 24 * time.Hour), ConfigHash: "hash-a"},
	}}
	if err := SaveOverrides(dir, in); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	got := LoadOverrides(dir)
	if got.VisionRejected("ollama", "llava", "hash-a", DefaultCapabilityTTL, now) {
		t.Fatalf("31-day-old entry still rejected, want expired")
	}
	// The same entry is live at the boundary the other way.
	if !got.VisionRejected("ollama", "llava", "hash-a", DefaultCapabilityTTL, now.Add(-24*time.Hour)) {
		t.Fatalf("30-day-old entry rejected, want live")
	}
}

func TestOverridesConfigHashMismatch(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	in := CapabilityOverrides{Rejections: map[string]Rejection{
		"ollama/llava": {ObservedAt: now, ConfigHash: "hash-a"},
	}}
	if err := SaveOverrides(dir, in); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	got := LoadOverrides(dir)
	if got.VisionRejected("ollama", "llava", "hash-b", DefaultCapabilityTTL, now) {
		t.Fatalf("rejection survived a config hash change, want ignored")
	}
}

func TestOverridesFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := SaveOverrides(dir, CapabilityOverrides{}); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "model_capabilities.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

// TestLoadOverridesMissingFileIsEmpty keeps the store an optimization: no
// caller can act on a read error, so a missing file must be empty, not nil.
func TestLoadOverridesMissingFileIsEmpty(t *testing.T) {
	got := LoadOverrides(t.TempDir())
	if got.Rejections == nil {
		t.Fatalf("Rejections is nil, want an empty map")
	}
	if len(got.Rejections) != 0 {
		t.Fatalf("loaded %d rejections from a missing file", len(got.Rejections))
	}
}

// TestLoadOverridesCorruptFileIsEmpty mirrors Load: a corrupt file costs one
// retried image request, never a failed launch.
func TestLoadOverridesCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model_capabilities.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadOverrides(dir)
	if len(got.Rejections) != 0 {
		t.Fatalf("loaded %d rejections from a corrupt file", len(got.Rejections))
	}
}

// TestSaveOverridesReplacesFile checks the tmp+rename replacement path does
// not leave the temp file behind.
func TestSaveOverridesReplacesFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	first := CapabilityOverrides{Rejections: map[string]Rejection{
		"ollama/llava": {ObservedAt: now, ConfigHash: "hash-a"},
	}}
	second := CapabilityOverrides{Rejections: map[string]Rejection{
		"openai/gpt-4o": {ObservedAt: now, ConfigHash: "hash-b"},
	}}
	if err := SaveOverrides(dir, first); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	if err := SaveOverrides(dir, second); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	got := LoadOverrides(dir)
	if got.VisionRejected("ollama", "llava", "hash-a", DefaultCapabilityTTL, now) {
		t.Fatalf("stale rejection survived a rewrite")
	}
	if !got.VisionRejected("openai", "gpt-4o", "hash-b", DefaultCapabilityTTL, now) {
		t.Fatalf("new rejection missing after rewrite")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "model_capabilities.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only model_capabilities.json", names)
	}
}
