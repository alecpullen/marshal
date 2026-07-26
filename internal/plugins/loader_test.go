package plugins

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/skills"
)

const testSkillMD = "+++\nname = \"alpha\"\ndescription = \"Alpha skill\"\n+++\n\n# Alpha\n\nDo alpha things.\n"

// writePluginSkill writes skills/<bundle>/SKILL.md under dir.
func writePluginSkill(t *testing.T, dir, bundle, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "skills", bundle, "SKILL.md"), content)
}

func TestScanBundleFindsSkills(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	writePluginSkill(t, dir, "beta", "+++\nname = \"beta\"\ndescription = \"Beta skill\"\nrisk = \"write\"\n+++\n\nBeta body.\n")

	skills, err := ScanBundle(dir)
	if err != nil {
		t.Fatalf("ScanBundle: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills length = %d, want 2", len(skills))
	}
	byName := map[string]string{}
	for _, s := range skills {
		byName[s.Name] = s.Description
	}
	if byName["alpha"] != "Alpha skill" || byName["beta"] != "Beta skill" {
		t.Fatalf("skills = %v", byName)
	}
}

func TestScanBundleSkipsNonSkillDirsAndFiles(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	// A directory without SKILL.md, and a stray file under skills/.
	writeFile(t, filepath.Join(dir, "skills", "docs", "notes.md"), "not a skill")
	writeFile(t, filepath.Join(dir, "skills", "stray.md"), "not in a bundle dir")

	skills, err := ScanBundle(dir)
	if err != nil {
		t.Fatalf("ScanBundle: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "alpha" {
		t.Fatalf("skills = %v, want only [alpha]", skills)
	}
}

func TestScanBundleSkipsMalformedSkill(t *testing.T) {
	dir := t.TempDir()
	writePluginSkill(t, dir, "alpha", testSkillMD)
	writePluginSkill(t, dir, "bad", "# no frontmatter\n")

	skills, err := ScanBundle(dir)
	if err != nil {
		t.Fatalf("ScanBundle: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "alpha" {
		t.Fatalf("skills = %v, want only [alpha]", skills)
	}
}

func TestScanBundleNoSkillsDir(t *testing.T) {
	if _, err := ScanBundle(t.TempDir()); err == nil {
		t.Fatal("ScanBundle should error when skills/ is missing")
	}
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

func TestLoadStoreMergesSkills(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	installFakePlugin(t, store, lockPath, "plug")

	idx := skills.NewIndex()
	if err := LoadStore(idx, store, lockPath, slog.Default()); err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	skill, ok := idx.Load("alpha")
	if !ok {
		t.Fatal("Load(alpha) returned false")
	}
	if skill.Description != "Alpha skill" {
		t.Fatalf("Description = %q", skill.Description)
	}
}

func TestLoadStoreSkipsHashMismatch(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	installFakePlugin(t, store, lockPath, "plug")

	// Tamper with the installed contents.
	writeFile(t, filepath.Join(store, "plug", "skills", "plug", "SKILL.md"), "tampered")

	idx := skills.NewIndex()
	if err := LoadStore(idx, store, lockPath, slog.Default()); err != nil {
		t.Fatalf("LoadStore should not fail on tampering: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Fatalf("tampered plugin should not load, got %v", idx.List())
	}
}

func TestLoadStoreSkipsPluginMissingOnDisk(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	installFakePlugin(t, store, lockPath, "plug")
	if err := os.RemoveAll(filepath.Join(store, "plug")); err != nil {
		t.Fatal(err)
	}

	idx := skills.NewIndex()
	if err := LoadStore(idx, store, lockPath, slog.Default()); err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Fatalf("missing plugin should not load, got %v", idx.List())
	}
}

func TestLoadStoreNoStoreDir(t *testing.T) {
	idx := skills.NewIndex()
	err := LoadStore(idx, filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "lock.json"), slog.Default())
	if err != nil {
		t.Fatalf("LoadStore should no-op for a missing store: %v", err)
	}
}

func TestLoadStoreNoLockfile(t *testing.T) {
	store := t.TempDir()
	writePluginSkill(t, filepath.Join(store, "orphan"), "orphan", testSkillMD)

	idx := skills.NewIndex()
	if err := LoadStore(idx, store, filepath.Join(t.TempDir(), "lock.json"), slog.Default()); err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Fatal("plugins without lockfile entries should not load")
	}
}

func TestLoadStoreMalformedLockfile(t *testing.T) {
	store := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "plugins-lock.json")
	writeFile(t, lockPath, "{not json")

	idx := skills.NewIndex()
	if err := LoadStore(idx, store, lockPath, slog.Default()); err != nil {
		t.Fatalf("LoadStore should warn, not fail, on a malformed lockfile: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Fatal("no plugins should load with a malformed lockfile")
	}
}
