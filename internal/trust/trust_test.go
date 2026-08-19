package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrustStoreDirectoryPermissions(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "marshal-data")
	s := NewStore(dataDir)
	if err := s.Save(map[string]Record{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory permissions: got %o, want 700", perm)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	trusted, err := store.IsTrusted("/some/project")
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("expected not trusted for new store")
	}

	err = store.SetTrust("/some/project", true, "abc123")
	if err != nil {
		t.Fatalf("SetTrust: %v", err)
	}

	trusted, err = store.IsTrusted("/some/project")
	if err != nil {
		t.Fatalf("IsTrusted after set: %v", err)
	}
	if !trusted {
		t.Fatal("expected trusted after SetTrust")
	}
}

func TestStoreSessionOnlyNotPersisted(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	err := store.SetTrust("/some/project", false, "")
	if err != nil {
		t.Fatalf("SetTrust session-only: %v", err)
	}

	trusted, err := store.IsTrusted("/some/project")
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("session-only trust should not be persisted")
	}
}

func TestStorePersistenceAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	store1 := NewStore(dir)
	store1.SetTrust("/some/project", true, "abc123")

	store2 := NewStore(dir)
	trusted, err := store2.IsTrusted("/some/project")
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if !trusted {
		t.Fatal("expected trusted across store instances")
	}
}

func TestHasProjectConfig(t *testing.T) {
	dir := t.TempDir()

	if HasProjectConfig(dir) {
		t.Fatal("expected no project config in empty dir")
	}

	marshalDir := filepath.Join(dir, ".marshal")
	os.MkdirAll(marshalDir, 0755)
	os.WriteFile(filepath.Join(marshalDir, "config.toml"), []byte("[project]\nname = \"test\""), 0644)

	if !HasProjectConfig(dir) {
		t.Fatal("expected project config to be detected")
	}
}

func TestStoreEmptyLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected empty records, got %d", len(records))
	}
}

func TestStoreMultipleProjects(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.SetTrust("/proj/a", true, "hash1")
	store.SetTrust("/proj/b", true, "hash2")

	trusted, _ := store.IsTrusted("/proj/a")
	if !trusted {
		t.Fatal("expected /proj/a trusted")
	}
	trusted, _ = store.IsTrusted("/proj/b")
	if !trusted {
		t.Fatal("expected /proj/b trusted")
	}
	trusted, _ = store.IsTrusted("/proj/c")
	if trusted {
		t.Fatal("expected /proj/c not trusted")
	}
}

func TestConfigHashForMissingFile(t *testing.T) {
	dir := t.TempDir()
	hash, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("ConfigHashFor missing file: %v", err)
	}
	if hash != "" {
		t.Fatalf("expected empty hash for missing config, got %q", hash)
	}
}

func TestConfigHashForStableAndDistinct(t *testing.T) {
	dir := t.TempDir()
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(marshalDir, "config.toml")
	if err := os.WriteFile(path, []byte("[project]\nname = \"a\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hash1, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("ConfigHashFor: %v", err)
	}
	if hash1 == "" {
		t.Fatal("expected non-empty hash for present config")
	}
	// Same content yields the same hash.
	hash1b, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("ConfigHashFor repeat: %v", err)
	}
	if hash1b != hash1 {
		t.Fatalf("hash not stable: %q vs %q", hash1, hash1b)
	}
	// Modified content yields a different hash.
	if err := os.WriteFile(path, []byte("[project]\nname = \"b\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hash2, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("ConfigHashFor modified: %v", err)
	}
	if hash2 == hash1 {
		t.Fatalf("expected distinct hash after content change, both = %q", hash1)
	}
}

func TestStoreStoredConfigHash(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	abs, _ := filepath.Abs(dir)

	// Empty before any SetTrust.
	hash, err := store.StoredConfigHash(abs)
	if err != nil {
		t.Fatalf("StoredConfigHash empty: %v", err)
	}
	if hash != "" {
		t.Fatalf("expected empty hash for missing record, got %q", hash)
	}

	if err := store.SetTrust(abs, true, "abc123"); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	hash, err = store.StoredConfigHash(abs)
	if err != nil {
		t.Fatalf("StoredConfigHash after set: %v", err)
	}
	if hash != "abc123" {
		t.Fatalf("StoredConfigHash = %q, want abc123", hash)
	}
}

func TestStoreRefreshConfigHash(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	abs, _ := filepath.Abs(dir)

	if err := store.SetTrust(abs, true, "oldhash"); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	trustedAt := before[abs].TrustedAt

	if err := store.RefreshConfigHash(abs, "newhash"); err != nil {
		t.Fatalf("RefreshConfigHash: %v", err)
	}

	hash, err := store.StoredConfigHash(abs)
	if err != nil {
		t.Fatalf("StoredConfigHash: %v", err)
	}
	if hash != "newhash" {
		t.Fatalf("StoredConfigHash = %q, want newhash", hash)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatalf("Load after refresh: %v", err)
	}
	if !after[abs].Trusted {
		t.Fatal("refresh cleared the trusted flag")
	}
	if !after[abs].TrustedAt.Equal(trustedAt) {
		t.Fatalf("refresh changed TrustedAt: was %v, now %v", trustedAt, after[abs].TrustedAt)
	}
}

// RefreshConfigHash must never grant trust: it only updates an existing
// trusted record, so a project that was never trusted (or was trusted
// for the session only) stays untrusted.
func TestStoreRefreshConfigHashNeverGrantsTrust(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	abs, _ := filepath.Abs(dir)

	if err := store.RefreshConfigHash(abs, "newhash"); err != nil {
		t.Fatalf("RefreshConfigHash absent record: %v", err)
	}
	trusted, err := store.IsTrusted(abs)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("RefreshConfigHash created a trusted record from nothing")
	}

	// Session-only trust leaves no record; refresh must not create one.
	if err := store.SetTrust(abs, false, ""); err != nil {
		t.Fatalf("SetTrust session: %v", err)
	}
	if err := store.RefreshConfigHash(abs, "newhash"); err != nil {
		t.Fatalf("RefreshConfigHash session-only: %v", err)
	}
	trusted, err = store.IsTrusted(abs)
	if err != nil {
		t.Fatalf("IsTrusted after session-only: %v", err)
	}
	if trusted {
		t.Fatal("RefreshConfigHash promoted session-only trust to permanent")
	}
}

func TestStoreLoadToleratesZonelessTimestamp(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write a trust store with a zoneless timestamp (no timezone offset).
	trustData := `{"/some/project": {"trusted": true, "config_hash": "abc123", "trusted_at": "2026-07-24T15:30:00"}}`
	if err := os.WriteFile(store.path, []byte(trustData), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	records, err := store.Load()
	if err != nil {
		t.Fatalf("Load with zoneless timestamp: %v", err)
	}
	r, ok := records["/some/project"]
	if !ok {
		t.Fatal("expected record for /some/project")
	}
	if !r.Trusted {
		t.Fatal("expected trusted = true")
	}
	if r.ConfigHash != "abc123" {
		t.Fatalf("config_hash = %q, want abc123", r.ConfigHash)
	}
	if r.TrustedAt.IsZero() {
		t.Fatal("expected non-zero TrustedAt")
	}
	// Verify it was parsed as UTC.
	if r.TrustedAt.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", r.TrustedAt.Location())
	}
}

func TestStoreIsTrustedToleratesZonelessTimestamp(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Write a trust store with a zoneless timestamp.
	trustData := `{"/some/project": {"trusted": true, "config_hash": "abc123", "trusted_at": "2026-07-24T15:30:00"}}`
	if err := os.WriteFile(store.path, []byte(trustData), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	trusted, err := store.IsTrusted("/some/project")
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if !trusted {
		t.Fatal("expected trusted = true")
	}
}

func writeProjectConfig(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte(contents), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestEvaluateNoProjectConfig(t *testing.T) {
	dir := t.TempDir()
	decision, needsPrompt, err := Evaluate(NewStore(t.TempDir()), dir)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision != DecisionDontTrust || needsPrompt {
		t.Fatalf("got (%v, %v), want (dont_trust, false)", decision, needsPrompt)
	}
}

func TestEvaluateUntrustedProjectNeedsPrompt(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "[project]\nname = \"x\"\n")
	decision, needsPrompt, err := Evaluate(NewStore(t.TempDir()), dir)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision != DecisionDontTrust || !needsPrompt {
		t.Fatalf("got (%v, %v), want (dont_trust, true)", decision, needsPrompt)
	}
}

func TestEvaluateTrustedHashMatchSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "[project]\nname = \"x\"\n")
	store := NewStore(t.TempDir())
	abs, _ := filepath.Abs(dir)
	hash, err := ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("ConfigHashFor: %v", err)
	}
	if err := store.SetTrust(abs, true, hash); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	decision, needsPrompt, err := Evaluate(store, dir)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision != DecisionTrustPermanent || needsPrompt {
		t.Fatalf("got (%v, %v), want (trust_permanent, false)", decision, needsPrompt)
	}
}

func TestEvaluateStaleHashRePrompts(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, "[project]\nname = \"x\"\n")
	store := NewStore(t.TempDir())
	abs, _ := filepath.Abs(dir)
	hash, _ := ConfigHashFor(dir)
	if err := store.SetTrust(abs, true, hash); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	// Config changes after trust was recorded → must re-prompt.
	writeProjectConfig(t, dir, "[project]\nname = \"changed\"\n")
	decision, needsPrompt, err := Evaluate(store, dir)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if decision != DecisionDontTrust || !needsPrompt {
		t.Fatalf("got (%v, %v), want (dont_trust, true)", decision, needsPrompt)
	}
}

func TestTerminalResolverRePromptsOnConfigChange(t *testing.T) {
	dir := t.TempDir()
	marshalDir := filepath.Join(dir, ".marshal")
	if err := os.MkdirAll(marshalDir, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(marshalDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[project]\nname = \"v1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	store := NewStore(dataDir)
	in := strings.NewReader("")
	resolver := NewTerminalResolver(store)
	resolver.SetIn(in)

	// First resolution: no record exists, the prompt is needed but
	// stdin is empty so we get DontTrust. Record the trust via the
	// store directly to simulate a prior trust-grant.
	abs, _ := filepath.Abs(dir)
	currentHash, _ := ConfigHashFor(dir)
	if err := store.SetTrust(abs, true, currentHash); err != nil {
		t.Fatal(err)
	}

	// Second resolution with the same config: should return
	// TrustPermanent without prompting (hash matches).
	decision, err := resolver.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve unchanged: %v", err)
	}
	if decision != DecisionTrustPermanent {
		t.Fatalf("decision = %s, want TrustPermanent", decision)
	}

	// Modify the config: the stored hash no longer matches, so
	// Resolve must fall through to the prompt path. With an empty
	// stdin the prompt returns DontTrust.
	if err := os.WriteFile(configPath, []byte("[project]\nname = \"v2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	decision, err = resolver.Resolve(dir, true)
	if err != nil {
		t.Fatalf("Resolve changed: %v", err)
	}
	if decision != DecisionDontTrust {
		t.Fatalf("decision = %s, want DontTrust after config change (re-prompt fell through to empty stdin)", decision)
	}
}
