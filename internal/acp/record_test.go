package acp

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordFiles are the three files a startRecord watches, laid out in a
// temporary directory standing in for the executable and both config files.
type recordFiles struct {
	dir        string
	executable string
	user       string
	project    string
}

// newRecordFiles lays those three files out and returns their paths.
func newRecordFiles(t *testing.T) recordFiles {
	t.Helper()
	dir := t.TempDir()
	f := recordFiles{
		dir:        dir,
		executable: filepath.Join(dir, "marshal"),
		user:       filepath.Join(dir, "config.toml"),
		project:    filepath.Join(dir, ".marshal", "config.toml"),
	}
	writeRecordFile(t, f.executable, "the build")
	writeRecordFile(t, f.user, "user config v1")
	writeRecordFile(t, f.project, "project config v1")
	return f
}

// record is the record those three files start from.
func (f recordFiles) record(t *testing.T) *startRecord {
	t.Helper()
	return newStartRecord(f.executable, f.user, f.project, discardLogger())
}

// writeRecordFile writes content at path, creating its directory.
func writeRecordFile(t *testing.T, path, content string) {
	t.Helper()
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// assertStale fails unless rec reports exactly want, in that order.
func assertStale(t *testing.T, rec *startRecord, want ...string) {
	t.Helper()
	got := rec.stale()
	if len(got) != len(want) {
		t.Fatalf("stale = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stale = %q, want %q", got, want)
		}
	}
}

// configCases names each config file the record watches with the reason it
// gives, as the fixed text B3 shows.
func configCases() []struct {
	name   string
	path   func(recordFiles) string
	reason string
} {
	return []struct {
		name   string
		path   func(recordFiles) string
		reason string
	}{
		{"user config", func(f recordFiles) string { return f.user }, "config.toml changed"},
		{"project config", func(f recordFiles) string { return f.project }, ".marshal/config.toml changed"},
	}
}

// TestARecordIsCurrentAtStart is the baseline: a record taken from the files
// as they are reports nothing stale and has nothing pending (decision 1).
func TestARecordIsCurrentAtStart(t *testing.T) {
	f := newRecordFiles(t)
	rec := f.record(t)
	assertStale(t, rec)
	if got := rec.pending(); got != nil {
		t.Fatalf("pending at start = %+v, want nil", got)
	}
}

// TestAReplacedExecutableIsStale is decision 1's executable entry: the file it
// took is gone and a different one is at the path.
func TestAReplacedExecutableIsStale(t *testing.T) {
	f := newRecordFiles(t)
	rec := f.record(t)
	if err := os.Remove(f.executable); err != nil {
		t.Fatalf("remove the executable: %v", err)
	}
	writeRecordFile(t, f.executable, "the next build, longer than the one it replaced")
	assertStale(t, rec, "a new marshal build")

	// The reasons come in the fixed order: the build, then config.toml, then
	// the project's config.
	writeRecordFile(t, f.user, "edited user config")
	writeRecordFile(t, f.project, "edited project config")
	assertStale(t, rec, "a new marshal build", "config.toml changed", ".marshal/config.toml changed")
}

// TestAnEditedConfigIsStale is decision 1's config entry: the SHA-256 of the
// content the record kept, for each file it watches.
func TestAnEditedConfigIsStale(t *testing.T) {
	for _, c := range configCases() {
		t.Run(c.name, func(t *testing.T) {
			f := newRecordFiles(t)
			rec := f.record(t)
			writeRecordFile(t, c.path(f), "edited after the child started")
			assertStale(t, rec, c.reason)
		})
	}
}

// TestACreatedConfigIsStale is decision 2's absent entry: a file that was not
// there when the record was taken is a change once it exists.
func TestACreatedConfigIsStale(t *testing.T) {
	for _, c := range configCases() {
		t.Run(c.name, func(t *testing.T) {
			f := newRecordFiles(t)
			path := c.path(f)
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove %s: %v", path, err)
			}
			rec := f.record(t)
			assertStale(t, rec)
			writeRecordFile(t, path, "a config the child never read")
			assertStale(t, rec, c.reason)
		})
	}
}

// TestAFilePutBackIsCurrent is decision 2's "compares against the record every
// time": a file put back as it was gives no reason.
func TestAFilePutBackIsCurrent(t *testing.T) {
	f := newRecordFiles(t)
	rec := f.record(t)
	writeRecordFile(t, f.user, "edited")
	assertStale(t, rec, "config.toml changed")
	writeRecordFile(t, f.user, "user config v1")
	assertStale(t, rec)
}

// TestAnOwnWriteIsNotStale is decision 3: the child's own write is not the
// child's own staleness.
func TestAnOwnWriteIsNotStale(t *testing.T) {
	cases := append(configCases(), struct {
		name   string
		path   func(recordFiles) string
		reason string
	}{"executable", func(f recordFiles) string { return f.executable }, "a new marshal build"})

	for _, c := range cases {
		t.Run(c.name+" after wrote", func(t *testing.T) {
			f := newRecordFiles(t)
			rec := f.record(t)
			writeRecordFile(t, c.path(f), "written by the child itself")
			rec.wrote(c.path(f))
			assertStale(t, rec)
		})
		t.Run(c.name+" without wrote", func(t *testing.T) {
			f := newRecordFiles(t)
			rec := f.record(t)
			writeRecordFile(t, c.path(f), "written by the child itself")
			assertStale(t, rec, c.reason)
		})
	}

	// The comparison is after cleaning both paths, so the record's path and
	// the write's path need not be spelled the same way.
	t.Run("an uncleaned path", func(t *testing.T) {
		f := newRecordFiles(t)
		rec := f.record(t)
		writeRecordFile(t, f.user, "written by the child itself")
		rec.wrote(f.dir + "/.marshal/../config.toml")
		assertStale(t, rec)
	})
}

// TestWroteIgnoresOtherPaths is decision 3's "only a path the record keeps": a
// write elsewhere changes nothing, so the config edited alongside it is still
// a reason.
func TestWroteIgnoresOtherPaths(t *testing.T) {
	f := newRecordFiles(t)
	rec := f.record(t)
	other := filepath.Join(f.dir, "not-watched.toml")
	writeRecordFile(t, other, "a file the record does not keep")
	writeRecordFile(t, f.user, "edited")

	rec.wrote(other)
	rec.wrote("")
	rec.wrote(filepath.Join(f.dir, "elsewhere", "config.toml"))
	assertStale(t, rec, "config.toml changed")
}

// TestAnUnreadableExecutableStillWatchesConfig is decision 2: a record that
// cannot read its executable keeps no entry, logs why once, and its config
// entries still work.
func TestAnUnreadableExecutableStillWatchesConfig(t *testing.T) {
	f := newRecordFiles(t)
	if err := os.Remove(f.executable); err != nil {
		t.Fatalf("remove the executable: %v", err)
	}
	var logs bytes.Buffer
	rec := newStartRecord(f.executable, f.user, f.project,
		slog.New(slog.NewTextHandler(&logs, nil)))

	if got := strings.Count(logs.String(), "could not read the executable"); got != 1 {
		t.Fatalf("the record logged %d reasons it could not read the executable, want 1:\n%s", got, logs.String())
	}
	assertStale(t, rec)

	writeRecordFile(t, f.user, "edited")
	assertStale(t, rec, "config.toml changed")

	// No entry was kept, so a build appearing at the path is not a change
	// the record can report either.
	writeRecordFile(t, f.executable, "a build that appeared later")
	assertStale(t, rec, "config.toml changed")
}

// TestPendingNamesWhatItWaitsFor is decision 4: the status reads the record's
// remembered answer, never the files.
func TestPendingNamesWhatItWaitsFor(t *testing.T) {
	f := newRecordFiles(t)
	rec := f.record(t)

	// While the child is current there is nothing to wait for.
	if got := rec.pending(); got != nil {
		t.Fatalf("pending before any answer = %+v, want nil", got)
	}

	// A remembered reason with nothing busy waits for a quiet moment.
	rec.remember([]string{"a new marshal build", "config.toml changed"}, nil)
	got := rec.pending()
	if got == nil {
		t.Fatal("pending after a remembered reason = nil, want an answer")
	}
	if got.Reason != "a new marshal build" {
		t.Fatalf("pending reason = %q, want %q", got.Reason, "a new marshal build")
	}
	if got.WaitingFor != "a quiet moment" {
		t.Fatalf("pending waiting-for = %q, want %q", got.WaitingFor, "a quiet moment")
	}

	// With something busy, the first busy line is what it waits for.
	rec.remember([]string{"config.toml changed"}, []string{"the running turn", "a background job"})
	got = rec.pending()
	if got == nil || got.Reason != "config.toml changed" || got.WaitingFor != "the running turn" {
		t.Fatalf("pending = %+v, want reason %q waiting for %q", got, "config.toml changed", "the running turn")
	}

	// The last answer replaces the one before it: a child that is current
	// again has nothing pending, however busy it is.
	rec.remember(nil, []string{"the running turn"})
	if got := rec.pending(); got != nil {
		t.Fatalf("pending after a current answer = %+v, want nil", got)
	}

	// A status read never hashes: editing a config changes nothing until the
	// answer is remembered, and remembering it is what the status shows.
	writeRecordFile(t, f.user, "edited")
	if got := rec.pending(); got != nil {
		t.Fatalf("pending after an unremembered edit = %+v, want nil", got)
	}
	rec.remember(rec.stale(), nil)
	got = rec.pending()
	if got == nil || got.Reason != "config.toml changed" || got.WaitingFor != "a quiet moment" {
		t.Fatalf("pending = %+v, want reason %q waiting for %q", got, "config.toml changed", "a quiet moment")
	}
}
