package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testEntry(name string) LockEntry {
	return LockEntry{
		Name:        name,
		Source:      "https://github.com/acme/" + name + ".git",
		Commit:      "abc123",
		ContentHash: "sha256:deadbeef",
		InstalledAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
	}
}

func TestReadLockfileMissingReturnsEmpty(t *testing.T) {
	lf, err := ReadLockfile(filepath.Join(t.TempDir(), "plugins-lock.json"))
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}
	if len(lf.Plugins) != 0 {
		t.Fatalf("Plugins length = %d, want 0", len(lf.Plugins))
	}
}

func TestLockfileWriteThenReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "plugins-lock.json")
	lf := &Lockfile{}
	lf.Upsert(testEntry("beta"))
	lf.Upsert(testEntry("alpha"))

	if err := lf.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := ReadLockfile(path)
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}
	if len(got.Plugins) != 2 {
		t.Fatalf("Plugins length = %d, want 2", len(got.Plugins))
	}
	// Write sorts by name.
	if got.Plugins[0].Name != "alpha" || got.Plugins[1].Name != "beta" {
		t.Fatalf("order = [%s %s], want [alpha beta]", got.Plugins[0].Name, got.Plugins[1].Name)
	}
	if got.Plugins[0].ContentHash != "sha256:deadbeef" {
		t.Fatalf("ContentHash = %q", got.Plugins[0].ContentHash)
	}
	if !got.Plugins[0].InstalledAt.Equal(time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("InstalledAt = %v", got.Plugins[0].InstalledAt)
	}
}

func TestLockfileUpsertReplaces(t *testing.T) {
	lf := &Lockfile{}
	lf.Upsert(testEntry("plug"))
	updated := testEntry("plug")
	updated.Commit = "fff000"
	lf.Upsert(updated)

	if len(lf.Plugins) != 1 {
		t.Fatalf("Plugins length = %d, want 1", len(lf.Plugins))
	}
	entry, ok := lf.Find("plug")
	if !ok {
		t.Fatal("Find(plug) returned false")
	}
	if entry.Commit != "fff000" {
		t.Fatalf("Commit = %q, want fff000", entry.Commit)
	}
}

func TestLockfileRemove(t *testing.T) {
	lf := &Lockfile{}
	lf.Upsert(testEntry("plug"))

	if !lf.Remove("plug") {
		t.Fatal("Remove(plug) returned false")
	}
	if lf.Remove("plug") {
		t.Fatal("second Remove(plug) should return false")
	}
	if _, ok := lf.Find("plug"); ok {
		t.Fatal("Find(plug) should return false after Remove")
	}
}

func TestReadLockfileMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins-lock.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLockfile(path); err == nil {
		t.Fatal("ReadLockfile should error on malformed JSON")
	}
}
