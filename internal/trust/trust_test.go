package trust

import (
	"os"
	"path/filepath"
	"testing"
)

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
