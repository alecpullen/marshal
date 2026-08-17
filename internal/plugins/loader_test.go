package plugins

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSkillMD = "+++\nname = \"alpha\"\ndescription = \"Alpha skill\"\n+++\n\n# Alpha\n\nDo alpha things.\n"

// writePluginSkill writes skills/<bundle>/SKILL.md under dir.
func writePluginSkill(t *testing.T, dir, bundle, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "skills", bundle, "SKILL.md"), content)
}

// installFakePlugin creates store/<name> with one skill and records it in
// the lockfile at lockPath with the correct content hash.
func installFakePlugin(t *testing.T, store, lockPath, name string) {
	t.Helper()
	dir := filepath.Join(store, name)
	writePluginSkill(t, dir, name, testSkillMD)
	hash, err := HashDir(dir)
	if err != nil {
		t.Fatalf("HashDir: %v", err)
	}
	lf, err := ReadLockfile(lockPath)
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}
	lf.Upsert(LockEntry{Name: name, Source: "test", Commit: "abc123", ContentHash: hash})
	if err := lf.Write(lockPath); err != nil {
		t.Fatalf("Write lockfile: %v", err)
	}
}

func TestLoadStoreReturnsVerifiedPlugins(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	installFakePlugin(t, store, lockPath, "plug")

	loaded, err := LoadStore(store, lockPath, slog.Default())
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded length = %d, want 1", len(loaded))
	}
	if loaded[0].Name != "plug" {
		t.Fatalf("Name = %q, want plug", loaded[0].Name)
	}
	if len(loaded[0].Contents.Skills) != 1 || loaded[0].Contents.Skills[0].Name != "alpha" {
		t.Fatalf("Skills = %v", loaded[0].Contents.Skills)
	}
}

func TestLoadStoreSkipsHashMismatch(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	installFakePlugin(t, store, lockPath, "plug")

	// Tamper with the installed contents.
	writeFile(t, filepath.Join(store, "plug", "skills", "plug", "SKILL.md"), "tampered")

	loaded, err := LoadStore(store, lockPath, slog.Default())
	if err != nil {
		t.Fatalf("LoadStore should not fail on tampering: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("tampered plugin should not load, got %v", loaded)
	}
}

func TestLoadStoreSkipsPluginMissingOnDisk(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	installFakePlugin(t, store, lockPath, "plug")
	if err := os.RemoveAll(filepath.Join(store, "plug")); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadStore(store, lockPath, slog.Default())
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("missing plugin should not load, got %v", loaded)
	}
}

func TestLoadStoreNoStoreDir(t *testing.T) {
	loaded, err := LoadStore(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "lock.json"), slog.Default())
	if err != nil {
		t.Fatalf("LoadStore should no-op for a missing store: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("loaded = %v, want empty", loaded)
	}
}

func TestLoadStoreNoLockfile(t *testing.T) {
	store := t.TempDir()
	writePluginSkill(t, filepath.Join(store, "orphan"), "orphan", testSkillMD)

	loaded, err := LoadStore(store, filepath.Join(t.TempDir(), "lock.json"), slog.Default())
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatal("plugins without lockfile entries should not load")
	}
}

func TestLoadStoreMalformedManifestLoggedAtError(t *testing.T) {
	store := t.TempDir()
	lock := filepath.Join(t.TempDir(), "plugins-lock.json")

	// Create a plugin with a malformed manifest
	pluginDir := filepath.Join(store, "bad-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.toml"), []byte("{{{ not toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Add a commands/*.md so the plugin isn't empty (to reach manifest parse)
	if err := os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "commands", "test.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hash the plugin dir for the lockfile
	hash, err := HashDir(pluginDir)
	if err != nil {
		t.Fatal(err)
	}

	// Write a lockfile with this plugin
	lf := Lockfile{Plugins: []LockEntry{{
		Name:        "bad-plugin",
		ContentHash: hash,
	}}}
	data, err := json.Marshal(lf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture logs
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))

	loaded, err := LoadStore(store, lock, logger)
	if err != nil {
		t.Fatalf("LoadStore should not return top-level error for per-plugin failure: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected 0 loaded plugins, got %d", len(loaded))
	}
	// Verify the error was logged at Error level
	logOutput := buf.String()
	if !strings.Contains(logOutput, "bad-plugin") {
		t.Fatalf("error log should mention plugin name, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "level=ERROR") {
		t.Fatalf("log should be at ERROR level, got: %s", logOutput)
	}
}

func TestLoadStoreMalformedLockfile(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	writeFile(t, lockPath, "{not json")

	loaded, err := LoadStore(store, lockPath, slog.Default())
	if err != nil {
		t.Fatalf("LoadStore should warn, not fail, on a malformed lockfile: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatal("no plugins should load with a malformed lockfile")
	}
}
