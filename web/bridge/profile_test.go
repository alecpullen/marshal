package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProfileUsesDevcontainerImage(t *testing.T) {
	dir := t.TempDir()
	writeDevcontainer(t, dir, `{"image": "ghcr.io/acme/dev:1"}`)

	got, reason := ResolveProfile(dir, RuntimeProfile{})
	if got.Image != "ghcr.io/acme/dev:1" {
		t.Fatalf("Image = %q, want ghcr.io/acme/dev:1 (reason: %s)", got.Image, reason)
	}
}

func TestResolveProfileToleratesLineComments(t *testing.T) {
	dir := t.TempDir()
	writeDevcontainer(t, dir, "{\n  // the team's image\n  \"image\": \"ghcr.io/acme/dev:2\"\n}")

	got, _ := ResolveProfile(dir, RuntimeProfile{})
	if got.Image != "ghcr.io/acme/dev:2" {
		t.Fatalf("Image = %q, want ghcr.io/acme/dev:2", got.Image)
	}
}

func TestResolveProfileFallsBackWhenDevcontainerBuilds(t *testing.T) {
	dir := t.TempDir()
	writeDevcontainer(t, dir, `{"build": {"dockerfile": "Dockerfile"}}`)

	got, reason := ResolveProfile(dir, RuntimeProfile{})
	if got.Image != DefaultRuntimeProfile().Image {
		t.Fatalf("Image = %q, want the default", got.Image)
	}
	if reason == "" {
		t.Fatal("fallback must explain itself")
	}
}

func TestResolveProfileOverrideWins(t *testing.T) {
	dir := t.TempDir()
	writeDevcontainer(t, dir, `{"image": "ghcr.io/acme/dev:1"}`)

	got, _ := ResolveProfile(dir, RuntimeProfile{Image: "explicit:tag", CPUs: 4, MemoryMB: 8192})
	if got.Image != "explicit:tag" {
		t.Fatalf("Image = %q, want explicit:tag", got.Image)
	}
	if got.CPUs != 4 || got.MemoryMB != 8192 {
		t.Fatalf("caps = (%v, %d), want (4, 8192)", got.CPUs, got.MemoryMB)
	}
}

func TestResolveProfileDefaultsCaps(t *testing.T) {
	got, _ := ResolveProfile(t.TempDir(), RuntimeProfile{})
	def := DefaultRuntimeProfile()
	if got.CPUs != def.CPUs || got.MemoryMB != def.MemoryMB {
		t.Fatalf("caps = (%v, %d), want (%v, %d)", got.CPUs, got.MemoryMB, def.CPUs, def.MemoryMB)
	}
}

func writeDevcontainer(t *testing.T, dir, body string) {
	t.Helper()
	sub := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "devcontainer.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
