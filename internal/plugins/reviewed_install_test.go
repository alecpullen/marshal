package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errReviewedInjected is the failure a test injects into one filesystem call.
var errReviewedInjected = errors.New("injected failure")

// --- fixtures -------------------------------------------------------------

// reviewedDirs is one install's on-disk world: a clone to copy from, a store to
// install into, and the lockfile that records it.
type reviewedDirs struct {
	store, lock, clone string
}

func newReviewedDirs(t *testing.T) reviewedDirs {
	t.Helper()
	root := t.TempDir()
	return reviewedDirs{
		store: filepath.Join(root, "store"),
		lock:  filepath.Join(root, "config", "plugins.lock.json"),
		clone: filepath.Join(root, "clone"),
	}
}

func writeReviewedFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func mustReviewedHash(t *testing.T, dir string) string {
	t.Helper()
	h, err := HashDir(dir)
	if err != nil {
		t.Fatalf("hash %s: %v", dir, err)
	}
	return h
}

func readReviewedFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// readReviewedBytes reads path, treating a missing file as absent.
func readReviewedBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// reviewedLeftovers lists the temporary directories an install may create —
// a stage, a backup, or a lockfile temporary. None of them may outlive a
// completed or rolled-back install other than the backup a failed restore
// deliberately retains.
func reviewedLeftovers(t *testing.T, store, lock string) []string {
	t.Helper()
	var found []string
	for _, dir := range []string{store, filepath.Dir(lock)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if strings.Contains(name, ".stage-") || strings.Contains(name, ".bak-") || strings.Contains(name, ".tmp-") {
				found = append(found, filepath.Join(dir, name))
			}
		}
	}
	return found
}

func reviewedBackups(t *testing.T, store string) []string {
	t.Helper()
	entries, err := os.ReadDir(store)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", store, err)
	}
	var found []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			found = append(found, filepath.Join(store, e.Name()))
		}
	}
	return found
}

// installReviewedFixture installs version 1 of "demo" for real and returns its
// content hash and the lockfile bytes as they stand after that install.
func installReviewedFixture(t *testing.T, d reviewedDirs) (hash string, lockBefore []byte) {
	t.Helper()
	writeReviewedFiles(t, d.clone, map[string]string{"hooks.toml": "v1\n"})
	hash = mustReviewedHash(t, d.clone)
	if err := InstallReviewed(context.Background(), ReviewedInstall{
		Name:         "demo",
		Source:       "https://example.com/demo.git",
		Commit:       "commit-v1",
		ReviewedHash: hash,
		CloneDir:     d.clone,
		StoreDir:     d.store,
		LockPath:     d.lock,
	}); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.store, "demo")); err != nil {
		t.Fatalf("install v1 left no plugin: %v", err)
	}
	return hash, readReviewedBytes(t, d.lock)
}

// --- tests ----------------------------------------------------------------

// TestInstallReviewedStagesAndCommits pins the happy path for a new install and
// for an update: the installed content hashes to exactly what was reviewed, the
// reviewed commit is what the lockfile records, and an unrelated entry is not
// touched by either install.
func TestInstallReviewedStagesAndCommits(t *testing.T) {
	d := newReviewedDirs(t)

	other := LockEntry{
		Name:        "other",
		Source:      "https://example.com/other.git",
		Commit:      "cafe",
		ContentHash: "sha256:other",
	}
	if err := (&Lockfile{Plugins: []LockEntry{other}}).Write(d.lock); err != nil {
		t.Fatalf("seed lockfile: %v", err)
	}

	writeReviewedFiles(t, d.clone, map[string]string{
		"hooks.toml":           "v1\n",
		"skills/demo/SKILL.md": "---\nname: demo\n---\nbody\n",
	})
	hash := mustReviewedHash(t, d.clone)
	if err := InstallReviewed(context.Background(), ReviewedInstall{
		Name:         "demo",
		Source:       "https://example.com/demo.git",
		Commit:       "abc123",
		ReviewedHash: hash,
		CloneDir:     d.clone,
		StoreDir:     d.store,
		LockPath:     d.lock,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	dest := filepath.Join(d.store, "demo")
	if got := mustReviewedHash(t, dest); got != hash {
		t.Errorf("installed content hash %s, want the reviewed %s", got, hash)
	}
	lf, err := ReadLockfile(d.lock)
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	entry, ok := lf.Find("demo")
	if !ok {
		t.Fatalf("install recorded no lockfile entry: %+v", lf.Plugins)
	}
	if entry.ContentHash != hash {
		t.Errorf("lockfile content hash %q, want the reviewed %q", entry.ContentHash, hash)
	}
	if entry.Commit != "abc123" {
		t.Errorf("lockfile commit %q, want the reviewed abc123", entry.Commit)
	}
	if entry.Source != "https://example.com/demo.git" {
		t.Errorf("lockfile source %q, want the reviewed source", entry.Source)
	}
	if kept, ok := lf.Find("other"); !ok || kept != other {
		t.Errorf("unrelated lockfile entry changed: %+v", kept)
	}
	if left := reviewedLeftovers(t, d.store, d.lock); len(left) > 0 {
		t.Errorf("install left staging behind: %v", left)
	}

	// An update replaces the plugin: the new content is on disk, the old file
	// is gone, and the lockfile still holds exactly one entry for the name.
	writeReviewedFiles(t, d.clone, map[string]string{"hooks.toml": "v2\n"})
	if err := os.Remove(filepath.Join(d.clone, "skills", "demo", "SKILL.md")); err != nil {
		t.Fatalf("drop old clone file: %v", err)
	}
	v2hash := mustReviewedHash(t, d.clone)
	if err := InstallReviewed(context.Background(), ReviewedInstall{
		Name:         "demo",
		Source:       "https://example.com/demo.git",
		Commit:       "def456",
		ReviewedHash: v2hash,
		CloneDir:     d.clone,
		StoreDir:     d.store,
		LockPath:     d.lock,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := readReviewedFile(t, filepath.Join(dest, "hooks.toml")); got != "v2\n" {
		t.Errorf("updated plugin holds %q, want v2", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "skills", "demo", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("updated plugin kept a file the review no longer contained: %v", err)
	}
	if got := mustReviewedHash(t, dest); got != v2hash {
		t.Errorf("updated install hash %s, want the reviewed %s", got, v2hash)
	}
	lf, err = ReadLockfile(d.lock)
	if err != nil {
		t.Fatalf("read lockfile after update: %v", err)
	}
	if len(lf.Plugins) != 2 {
		t.Fatalf("update changed the number of entries: %+v", lf.Plugins)
	}
	entry, _ = lf.Find("demo")
	if entry.Commit != "def456" || entry.ContentHash != v2hash {
		t.Errorf("update recorded %+v, want the reviewed commit and hash", entry)
	}
	if _, ok := lf.Find("other"); !ok {
		t.Error("update dropped an unrelated lockfile entry")
	}
	if left := reviewedLeftovers(t, d.store, d.lock); len(left) > 0 {
		t.Errorf("update left staging behind: %v", left)
	}
}

// TestInstallReviewedRejectsChangedContentAndLock pins the two changes that must
// stop an install before it swaps anything: a clone that no longer matches the
// reviewed hash, and a lockfile edited between the snapshot and the swap. In
// both cases the installed plugin and the lockfile bytes survive untouched.
func TestInstallReviewedRejectsChangedContentAndLock(t *testing.T) {
	t.Run("clone changed after the review", func(t *testing.T) {
		d := newReviewedDirs(t)
		v1hash, _ := installReviewedFixture(t, d)
		lockBefore := readReviewedBytes(t, d.lock)

		// The user reviewed v1's hash; the clone now holds v2.
		writeReviewedFiles(t, d.clone, map[string]string{"hooks.toml": "v2\n"})
		err := InstallReviewed(context.Background(), ReviewedInstall{
			Name:         "demo",
			Source:       "https://example.com/demo.git",
			Commit:       "commit-v2",
			ReviewedHash: v1hash,
			CloneDir:     d.clone,
			StoreDir:     d.store,
			LockPath:     d.lock,
		})
		if err == nil {
			t.Fatal("install accepted content that did not match the review")
		}
		if !strings.Contains(err.Error(), "changed since the review") {
			t.Errorf("error does not name the mismatch: %v", err)
		}

		dest := filepath.Join(d.store, "demo")
		if got := readReviewedFile(t, filepath.Join(dest, "hooks.toml")); got != "v1\n" {
			t.Errorf("installed plugin now holds %q, want the old v1", got)
		}
		if got := mustReviewedHash(t, dest); got != v1hash {
			t.Errorf("installed content changed: hash %s, want the old %s", got, v1hash)
		}
		if got := readReviewedBytes(t, d.lock); string(got) != string(lockBefore) {
			t.Errorf("lockfile changed on a rejected install:\n%s\nwant:\n%s", got, lockBefore)
		}
		if left := reviewedLeftovers(t, d.store, d.lock); len(left) > 0 {
			t.Errorf("rejected install left staging behind: %v", left)
		}
	})

	t.Run("lockfile edited before the swap", func(t *testing.T) {
		d := newReviewedDirs(t)
		v1hash, _ := installReviewedFixture(t, d)

		// Another writer appends an entry while this install is copying: the
		// injected copy runs between the snapshot and the swap.
		intruder := LockEntry{
			Name: "intruder", Source: "https://example.com/intruder.git",
			Commit: "intruder-commit", ContentHash: "sha256:intruder",
		}
		ops := defaultInstallOps()
		copyDir := ops.copyDir
		ops.copyDir = func(src, dst string) error {
			if err := copyDir(src, dst); err != nil {
				return err
			}
			lf, err := ReadLockfile(d.lock)
			if err != nil {
				return err
			}
			lf.Upsert(intruder)
			return lf.Write(d.lock)
		}

		writeReviewedFiles(t, d.clone, map[string]string{"hooks.toml": "v2\n"})
		err := installReviewed(context.Background(), ReviewedInstall{
			Name:         "demo",
			Source:       "https://example.com/demo.git",
			Commit:       "commit-v2",
			ReviewedHash: mustReviewedHash(t, d.clone),
			CloneDir:     d.clone,
			StoreDir:     d.store,
			LockPath:     d.lock,
		}, ops)
		if err == nil {
			t.Fatal("install overwrote a lockfile that changed under it")
		}
		if !strings.Contains(err.Error(), "lockfile") || !strings.Contains(err.Error(), "changed") {
			t.Errorf("error does not name the lockfile change: %v", err)
		}

		dest := filepath.Join(d.store, "demo")
		if got := readReviewedFile(t, filepath.Join(dest, "hooks.toml")); got != "v1\n" {
			t.Errorf("installed plugin now holds %q, want the old v1", got)
		}
		if got := mustReviewedHash(t, dest); got != v1hash {
			t.Errorf("installed content changed: hash %s, want the old %s", got, v1hash)
		}
		// The other writer's edit is the one that survives, and this install
		// left no entry of its own.
		lf, err := ReadLockfile(d.lock)
		if err != nil {
			t.Fatalf("read lockfile: %v", err)
		}
		if _, ok := lf.Find("intruder"); !ok {
			t.Error("the concurrent lockfile edit was lost")
		}
		if entry, ok := lf.Find("demo"); !ok || entry.Commit != "commit-v1" {
			t.Errorf("demo entry is %+v, want the previous install's entry", entry)
		}
		if left := reviewedLeftovers(t, d.store, d.lock); len(left) > 0 {
			t.Errorf("aborted install left staging behind: %v", left)
		}
	})
}

// TestInstallReviewedRollsBackFailures pins that one injected failure at each
// step of the swap leaves the previously installed plugin and the previous
// lockfile in place, with no partial plugin behind — and that a restore which
// itself fails is reported and its backup retained rather than deleted.
func TestInstallReviewedRollsBackFailures(t *testing.T) {
	// The rename order is: installed copy aside, stage into place, lockfile
	// commit, then (only on a failure) the restore.
	failingRename := func(failAt ...int) func(string, string) error {
		calls := 0
		return func(oldpath, newpath string) error {
			calls++
			for _, n := range failAt {
				if calls == n {
					return errReviewedInjected
				}
			}
			return os.Rename(oldpath, newpath)
		}
	}

	for _, tc := range []struct {
		name     string
		inject   func(ops *installOps)
		wantErr  string
		wantKept bool
	}{
		{
			name:    "copy",
			inject:  func(ops *installOps) { ops.copyDir = func(string, string) error { return errReviewedInjected } },
			wantErr: "copy plugin",
		},
		{
			name:    "hash",
			inject:  func(ops *installOps) { ops.hashDir = func(string) (string, error) { return "", errReviewedInjected } },
			wantErr: "hash the staged copy",
		},
		{
			name:    "stage placement",
			inject:  func(ops *installOps) { ops.rename = failingRename(2) },
			wantErr: "place plugin",
		},
		{
			name:    "lockfile commit",
			inject:  func(ops *installOps) { ops.rename = failingRename(3) },
			wantErr: "commit the lockfile",
		},
		{
			name:     "failed restore",
			inject:   func(ops *installOps) { ops.rename = failingRename(2, 3) },
			wantErr:  "rollback failed",
			wantKept: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newReviewedDirs(t)
			v1hash, lockBefore := installReviewedFixture(t, d)

			writeReviewedFiles(t, d.clone, map[string]string{"hooks.toml": "v2\n"})
			ops := defaultInstallOps()
			tc.inject(&ops)
			err := installReviewed(context.Background(), ReviewedInstall{
				Name:         "demo",
				Source:       "https://example.com/demo.git",
				Commit:       "commit-v2",
				ReviewedHash: mustReviewedHash(t, d.clone),
				CloneDir:     d.clone,
				StoreDir:     d.store,
				LockPath:     d.lock,
			}, ops)
			if err == nil {
				t.Fatalf("injected %s failure installed anyway", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name the %s failure", err, tc.name)
			}

			if got := readReviewedBytes(t, d.lock); string(got) != string(lockBefore) {
				t.Errorf("lockfile changed after a %s failure:\n%s\nwant:\n%s", tc.name, got, lockBefore)
			}

			dest := filepath.Join(d.store, "demo")
			backups := reviewedBackups(t, d.store)
			switch {
			case tc.wantKept:
				// The restore failed: the destination is gone, the backup is
				// retained, and the error names it.
				if len(backups) != 1 {
					t.Fatalf("failed restore left %d backups, want exactly the retained one: %v", len(backups), backups)
				}
				if !strings.Contains(err.Error(), backups[0]) {
					t.Errorf("error %q does not name the retained backup %s", err, backups[0])
				}
				if got := readReviewedFile(t, filepath.Join(backups[0], "hooks.toml")); got != "v1\n" {
					t.Errorf("retained backup holds %q, want the previous v1", got)
				}
				if _, statErr := os.Lstat(dest); !os.IsNotExist(statErr) {
					t.Errorf("a partial install is visible at %s: %v", dest, statErr)
				}
			default:
				if len(backups) != 0 {
					t.Errorf("rollback left a backup behind: %v", backups)
				}
				if got := readReviewedFile(t, filepath.Join(dest, "hooks.toml")); got != "v1\n" {
					t.Errorf("installed plugin now holds %q, want the previous v1", got)
				}
				if got := mustReviewedHash(t, dest); got != v1hash {
					t.Errorf("installed content changed: hash %s, want the previous %s", got, v1hash)
				}
			}

			// No stage and no lockfile temporary outlives a failure, and a
			// rollback that succeeded leaves nothing but the installed plugin.
			if left := reviewedLeftovers(t, d.store, d.lock); len(left) > 0 {
				if !tc.wantKept || len(left) != 1 {
					t.Errorf("failed install left staging behind: %v", left)
				}
			}
		})
	}
}

// TestInstallReviewedRejectsInvalidName pins that a name which is not a single
// directory inside the store — including one derived from a normalized GitHub
// source — is refused before any path is created.
func TestInstallReviewedRejectsInvalidName(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	writeReviewedFiles(t, clone, map[string]string{"hooks.toml": "v1\n"})
	hash := mustReviewedHash(t, clone)

	names := []string{"", ".", "..", "../escape", "sub/dir", `sub\dir`, `..\escape`}
	// github:owner/.. and github:owner/. normalize without complaint, so the
	// guard against them is the name check, not the source parser.
	for _, source := range []string{"github:owner/..", "github:owner/."} {
		_, name, err := NormalizeSource(source)
		if err != nil {
			t.Fatalf("NormalizeSource(%q): %v", source, err)
		}
		if ValidName(name) {
			t.Fatalf("NormalizeSource(%q) produced the valid name %q; the case is not exercised", source, name)
		}
		names = append(names, name)
	}

	for _, name := range names {
		t.Run(strings.NewReplacer("/", "_", `\`, "_", "..", "dotdot").Replace("name_"+name), func(t *testing.T) {
			root := t.TempDir()
			store := filepath.Join(root, "store")
			err := InstallReviewed(context.Background(), ReviewedInstall{
				Name:         name,
				Source:       "https://example.com/demo.git",
				Commit:       "abc123",
				ReviewedHash: hash,
				CloneDir:     clone,
				StoreDir:     store,
				LockPath:     filepath.Join(root, "config", "plugins.lock.json"),
			})
			if err == nil {
				t.Fatalf("install accepted the invalid name %q", name)
			}
			if !strings.Contains(err.Error(), "invalid plugin name") {
				t.Errorf("error does not name the invalid plugin name: %v", err)
			}
			if _, statErr := os.Lstat(store); !os.IsNotExist(statErr) {
				t.Errorf("a rejected install created the store %s: %v", store, statErr)
			}
		})
	}

	// A name alone is not a review: without a content hash there is nothing to
	// hold the install to.
	root := t.TempDir()
	err := InstallReviewed(context.Background(), ReviewedInstall{
		Name:     "demo",
		Source:   "https://example.com/demo.git",
		CloneDir: clone,
		StoreDir: filepath.Join(root, "store"),
		LockPath: filepath.Join(root, "config", "plugins.lock.json"),
	})
	if err == nil {
		t.Fatal("install accepted an empty reviewed hash")
	}
	if _, statErr := os.Lstat(filepath.Join(root, "store")); !os.IsNotExist(statErr) {
		t.Errorf("an install without a reviewed hash created the store: %v", statErr)
	}
}
