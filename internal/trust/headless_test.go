package trust

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestHeadlessResolverNoStoredTrust(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Create a project config so hasProjectConfig is true.
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := NewHeadlessResolver(store, log)
	decision, err := resolver.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionDontTrust {
		t.Fatalf("decision = %s, want DontTrust", decision)
	}

	// Verify the warning was logged.
	if !bytes.Contains(buf.Bytes(), []byte("no stored trust")) {
		t.Fatalf("expected warning log, got: %s", buf.String())
	}
}

func TestHeadlessResolverNoProjectConfig(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	resolver := NewHeadlessResolver(store, log)
	decision, err := resolver.Resolve(dir, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionDontTrust {
		t.Fatalf("decision = %s, want DontTrust", decision)
	}
}

func TestHeadlessResolverPermanentTrustHashMatch(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	store := NewStore(dataDir)

	// Create a project config.
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(marshalDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Record permanent trust with the current hash.
	abs, _ := filepath.Abs(dir)
	currentHash, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrust(abs, true, currentHash); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	resolver := NewHeadlessResolver(store, log)
	decision, err := resolver.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionTrustPermanent {
		t.Fatalf("decision = %s, want TrustPermanent", decision)
	}
}

func TestHeadlessResolverHashMismatchDegrades(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	store := NewStore(dataDir)

	// Create a project config.
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(marshalDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[project]\nname = \"v1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Record permanent trust with the v1 hash.
	abs, _ := filepath.Abs(dir)
	v1Hash, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrust(abs, true, v1Hash); err != nil {
		t.Fatal(err)
	}

	// Change the config.
	if err := os.WriteFile(configPath, []byte("[project]\nname = \"v2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	resolver := NewHeadlessResolver(store, log)
	decision, err := resolver.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if decision != DecisionDontTrust {
		t.Fatalf("decision = %s, want DontTrust (config changed)", decision)
	}

	// Verify the warning about config change was logged.
	if !bytes.Contains(buf.Bytes(), []byte("config changed")) {
		t.Fatalf("expected config-change warning log, got: %s", buf.String())
	}
}

func TestHeadlessResolverRecordIsNoop(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	resolver := NewHeadlessResolver(store, log)

	// Record should not error.
	if err := resolver.Record(dir, DecisionTrustPermanent); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The store should still be empty — nothing was persisted.
	trusted, err := store.IsTrusted(dir)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("expected no trust persisted after Record no-op")
	}
}

func TestHeadlessResolverNoStoredTrustIsConsistent(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	store := NewStore(dataDir)

	// Create a project config.
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	resolver := NewHeadlessResolver(store, log)

	for i := 0; i < 2; i++ {
		buf.Reset()
		decision, err := resolver.Resolve(dir, true)
		if err != nil {
			t.Fatalf("Resolve #%d: %v", i+1, err)
		}
		if decision != DecisionDontTrust {
			t.Fatalf("decision #%d = %s, want DontTrust", i+1, decision)
		}
		if !bytes.Contains(buf.Bytes(), []byte("no stored trust")) {
			t.Fatalf("expected warning log on call #%d, got: %s", i+1, buf.String())
		}
	}
}
