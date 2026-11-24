# Resource bounds: implementation plan

Spec: [`design.md`](./design.md) (approved 2026-11-22).
Authored against: `cf8ebf82` (`main`), clean tree apart from untracked `docs/resource-bounds/`, `docs/logo/`, `web/ui/`.

## Goal

Bound the resources three code paths can consume: the index scan's retained
file content, snapshot capture on the tool-call path, and the index defaults
that let a home-directory workspace be walked at all. Every bound records the
reason it fired instead of failing silently.

## Non-goals

Carried verbatim from `design.md` — do not implement any of these:

- **A2, the streaming scanner rewrite.** `ScanDetailed`'s signature is left
  intact precisely so A2 stays an incremental change later.
- **Changing `snapshots.enabled` or index enable defaults.**
- **Cleaning the 83 GB snapshot store.** Operational, not code.
- **zram priority / `vm.swappiness 150` / zswap.** Host configuration.
- **The USB mouse dropping off bus 1 port 5.** Unrelated hardware fault.

## Assumptions

From `design.md`, plus two the authoring pass added:

- 512 MiB retained content and a 2 MiB per-file index cap are the defaults. Both
  are config fields, so a user can raise them.
- 5 minutes is the backoff window; 5 seconds is the interactive capture bound.
  Both are constants until someone needs otherwise.
- *(authoring, corrected during execution)* `internal/app/config` **cannot**
  import `internal/repo` for the content bound: `internal/repo` already reaches
  `internal/app/config` through `internal/db`, `internal/subagentwork` and
  `internal/worktree`, so the edge is a cycle. The 512 MiB bound therefore lives
  in `internal/limits` (`limits.MaxTotalContentBytes`), a leaf both packages
  name. `internal/repo` references it directly and declares no second name, so
  the number has exactly one declaration. Code snippets below that still spell
  `repo.DefaultMaxTotalContentBytes` are as originally planned; the landed code
  uses `limits.MaxTotalContentBytes`.
- *(authoring)* A failed `stat` in `walk` must not skip the file. It makes the
  file unretainable (hashed by streaming) and records `ContentOmitted`, rather
  than adding a new skip reason, which keeps the existing skip vocabulary
  unchanged; the first such file is named in a warning so it is not silently
  dropped.

## Contracts

New or changed declarations that more than one task depends on. The scanner
block is `internal/repo`; the config block is `internal/app/config`.

```go
// internal/repo/scanner.go

// DefaultMaxTotalContentBytes is the most file content one scan retains. It is
// a total, not a per-file bound: a tree of many small files passes every
// per-file cap and still sums to an unbounded working set.
const DefaultMaxTotalContentBytes int64 = 512 << 20

type Config struct {
	Root   string
	Ignore []string
	// MaxIndexableFileBytes caps the size of files that will be hashed and
	// indexed during scanning. Files larger than this threshold are skipped
	// and recorded in Skipped(). Zero means no limit.
	MaxIndexableFileBytes int64
	// MaxTotalContentBytes caps the total file content one scan retains across
	// all files. A file beyond the budget is still hashed and indexed (path,
	// hash, language, size) but its Content is nil and
	// ScannedFile.ContentOmitted is set. Zero means
	// DefaultMaxTotalContentBytes: a bound never disappears.
	MaxTotalContentBytes int64
}

// walk's callback gains the retain flag. walk has exactly one caller
// (ScanDetailed), so no other call site changes.
func (s *Scanner) walk(ctx context.Context, fn func(path, rel string, retain bool) (db.FileIndex, []byte, error)) ([]ScannedFile, error)

// hashBytes no longer returns an error: it always returned nil.
func hashBytes(content []byte) (string, int64)

// hashFile streams a file through sha256 without holding it in memory.
func hashFile(path string) (string, int64, error)

// internal/repo/scanned.go

type ScannedFile struct {
	db.FileIndex
	Content []byte
	ReadErr error
	// ContentOmitted reports that Content is nil because the scan's total
	// content budget was exhausted, not because the file could not be read.
	// ReadErr is nil for such a file and its FileIndex is valid. Consumers must
	// not infer this state from a nil Content: an empty file legitimately has
	// nil Content too.
	ContentOmitted bool
}

// internal/app/config/types.go

type IndexingConfig struct {
	// ... existing fields unchanged ...
	MaxTotalContentBytes int64 `toml:"max_total_content_bytes"`
}

// internal/index/run.go

type Deps struct {
	// ... existing fields unchanged ...
	MaxTotalContentBytes int64 // zero => repo.DefaultMaxTotalContentBytes
}
```

```go
// internal/snapshot/manager.go

// captureBackoff is how long a workspace whose capture failed is skipped
// before capture is attempted again. It is a timed backoff, not a permanent
// negative cache: a transient failure must not disable snapshots for the rest
// of the session.
const captureBackoff = 5 * time.Minute

// captureSuppression records that capture for one workspace root failed, why,
// and until when further attempts are suppressed.
type captureSuppression struct {
	reason StoreReason
	until  time.Time
}

func (m *Manager) suppressedCapture(root string) (StoreReason, bool)
func (m *Manager) noteCaptureFailure(root string, reason StoreReason)
func (m *Manager) clearCaptureSuppression(root string)
func (m *Manager) captureKey(root string) (string, bool)
```

```go
// internal/agent/snapshot_warning.go

// snapshotCaptureDeadline bounds one pre-write capture. The capture is on the
// tool-call path, so it gets a bound of its own rather than inheriting the
// manager's 30s command deadline.
const snapshotCaptureDeadline = 5 * time.Second

// Added to the snapshotReason const block beside reasonCaptureSkipped.
reasonCaptureTimeout snapshotReason = "capture_timeout"
```

---

## Task 1: Bound the scanner's retained content

**Goal:** `repo.Scanner` retains at most a configurable total of file content,
still indexes every file past that total, and marks such files `ContentOmitted`
so no consumer mistakes them for empty.

**Files:** `internal/repo/scanner.go`, `internal/repo/scanned.go`,
`internal/repo/scanner_test.go`.

**Steps**

1. In `internal/repo/scanned.go`, add `ContentOmitted bool` to `ScannedFile`
   with the doc comment from Contracts.

2. In `internal/repo/scanner.go`, add `DefaultMaxTotalContentBytes` and the
   `Config.MaxTotalContentBytes` field, both verbatim from Contracts.

3. Change `walk`'s signature (line 85) to the Contracts form.

4. Inside `walk`, declare the budget state before `filepath.WalkDir`:

```go
	var results []ScannedFile
	var totalContent int64
	budget := s.config.MaxTotalContentBytes
	if budget <= 0 {
		budget = DefaultMaxTotalContentBytes
	}
	omissions := 0
```

5. Replace the per-file cap block and the `fn` call (currently
   `scanner.go:158-172`, from `// Skip files that exceed the configurable size
   cap.` through the closing `})` of the `results` append) with exactly:

```go
		// The cap and the budget both need the size, so stat once here rather
		// than once per bound. A failed stat is not fatal: the file is treated
		// as unretainable and hashed by streaming, which keeps a file we cannot
		// measure out of the retained working set without inventing a new skip
		// reason for it.
		size, sizeKnown := int64(0), false
		if info, infoErr := entry.Info(); infoErr == nil {
			size, sizeKnown = info.Size(), true
		}

		// Skip files that exceed the configurable size cap.
		if s.config.MaxIndexableFileBytes > 0 && sizeKnown && size > s.config.MaxIndexableFileBytes {
			s.skipped = append(s.skipped, SkipEntry{Path: rel, Reason: fmt.Sprintf("file too large (%d bytes)", size)})
			return nil
		}

		// The budget is a total, so it can only be judged here, once per file,
		// after the per-file filters have run. Once it rejects a file it keeps
		// rejecting for the rest of the walk, so the omitted set is a prefix of
		// the walk rather than an arbitrary interleaving.
		retain := sizeKnown && totalContent+size <= budget
		fi, content, fnErr := fn(path, rel, retain)
		if fnErr != nil {
			s.warnings = append(s.warnings, fnErr.Error())
		}
		if retain && fnErr == nil {
			totalContent += int64(len(content))
		} else if !retain && sizeKnown && omissions == 0 {
			omissions++
			s.warnings = append(s.warnings, fmt.Sprintf(
				"content budget exhausted: files past %d retained bytes are indexed without content, starting at %s",
				budget, rel))
		}
		results = append(results, ScannedFile{
			FileIndex:      fi,
			Content:        content,
			ReadErr:        fnErr,
			ContentOmitted: !retain,
		})
		return nil
```

6. Replace `ScanDetailed` (line 187) wholesale with:

```go
// ScanDetailed scans the repo root, computing file hashes and capturing each file's
// content so callers can use it without re-reading from disk.
// Per-file read failures are non-fatal: the returned ScannedFile will have a
// nil Content and a non-nil ReadErr. The scan continues to other files.
// A file past the scan's total content budget is hashed by streaming and
// returned with a nil Content and ContentOmitted set: it stays in the file
// index, it just carries no bytes.
func (s *Scanner) ScanDetailed(ctx context.Context) ([]ScannedFile, error) {
	return s.walk(ctx, func(path, rel string, retain bool) (db.FileIndex, []byte, error) {
		if !retain {
			hash, size, err := hashFile(path)
			if err != nil {
				return db.FileIndex{
					Path:     rel,
					Language: DetectLanguage(rel),
				}, nil, fmt.Errorf("hash %s: %w", rel, err)
			}
			return db.FileIndex{
				Path:      rel,
				Language:  DetectLanguage(rel),
				Hash:      hash,
				SizeBytes: size,
			}, nil, nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return db.FileIndex{
				Path:     rel,
				Language: DetectLanguage(rel),
			}, nil, fmt.Errorf("read %s: %w", rel, err)
		}
		hash, size := hashBytes(content)
		return db.FileIndex{
			Path:      rel,
			Language:  DetectLanguage(rel),
			Hash:      hash,
			SizeBytes: size,
		}, content, nil
	})
}
```

7. Replace `hashBytes` (line 226) and add `hashFile`:

```go
// hashBytes computes the SHA-256 hash of content and returns the hex-encoded
// hash along with the content length.
func hashBytes(content []byte) (string, int64) {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), int64(len(content))
}

// hashFile computes the SHA-256 hash of a file's bytes without holding them in
// memory, and returns the hash and the number of bytes read. It is the path
// taken for a file past the scan's content budget, so its digest must match
// hashBytes for the same bytes.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 64<<10))
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
```

8. Add `"io"` to the imports of `scanner.go`.

9. Fix call sites of the two changed signatures. Run
   `grep -n "hashBytes\|\.walk(" internal/repo/*.go` and update every hit:
   `hashBytes` now returns two values, and `walk` callbacks take a third
   `retain bool` parameter (ignore it in tests unless the test is about the
   budget).

10. Add to `internal/repo/scanner_test.go`:

```go
func TestScanDetailedOmitsContentPastBudget(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", 1024)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewScanner(Config{Root: root, MaxTotalContentBytes: 2 << 10})
	scanned, err := s.ScanDetailed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) != 3 {
		t.Fatalf("scanned %d files, want 3", len(scanned))
	}
	retained := 0
	for _, sf := range scanned {
		if sf.Content != nil {
			retained++
			if sf.ContentOmitted {
				t.Errorf("%s: retained content marked omitted", sf.Path)
			}
			continue
		}
		if !sf.ContentOmitted {
			t.Errorf("%s: nil content not marked omitted", sf.Path)
		}
		if sf.ReadErr != nil {
			t.Errorf("%s: omitted file has read error %v", sf.Path, sf.ReadErr)
		}
		if sf.Hash == "" || sf.SizeBytes != 1024 {
			t.Errorf("%s: omitted file lost its index entry: hash=%q size=%d", sf.Path, sf.Hash, sf.SizeBytes)
		}
	}
	if retained != 2 {
		t.Errorf("retained %d files, want 2", retained)
	}
	if got := len(s.Warnings()); got != 1 {
		t.Errorf("warnings = %v, want exactly one budget warning", s.Warnings())
	}
}

func TestScanDetailedRetainedBytesNeverExceedBudget(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("y", 700)
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("f%02d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const budget = 2000
	s := NewScanner(Config{Root: root, MaxTotalContentBytes: budget})
	scanned, err := s.ScanDetailed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, sf := range scanned {
		total += len(sf.Content)
	}
	if total > budget {
		t.Fatalf("retained %d bytes, budget %d", total, budget)
	}
	if len(scanned) != 10 {
		t.Fatalf("scanned %d files, want 10: truncation must not shrink the file index", len(scanned))
	}
}

func TestHashFileMatchesHashBytes(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("abcdefgh"), 9000) // more than one 64 KiB chunk
	path := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	streamedHash, streamedSize, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, wantSize := hashBytes(content)
	if streamedHash != wantHash {
		t.Errorf("hashFile = %q, hashBytes = %q", streamedHash, wantHash)
	}
	if streamedSize != wantSize {
		t.Errorf("hashFile size = %d, hashBytes size = %d", streamedSize, wantSize)
	}
}
```

Add `bytes`, `fmt`, `strings` to that file's imports if absent. The existing
`TestScanDetailedReturnsContent` (line 460) must still pass unchanged.

**Verify**

```bash
cd ~/projects/marshal && scripts/test.sh quick ./internal/repo
```
Expected: PASS, including the three new tests and the untouched
`TestScanDetailedReturnsContent`.

---

## Task 2: Make the index pass consume the new scanner signals

**Goal:** A `ContentOmitted` file keeps its existing embeddings instead of being
purged, produces no bogus parse error, and the pass reports scanner skips,
scanner warnings and a pathological index root.

**Files:** `internal/index/indexer.go`, `internal/index/run.go`,
`internal/index/indexer_test.go`.

**Steps**

1. In `Reindex` (`indexer.go:46`), guard the purge path. Replace

```go
		seen[sf.Path] = true
		if sf.ReadErr != nil {
			continue
		}
```

with

```go
		seen[sf.Path] = true
		// A file whose content the scan chose not to retain is still present:
		// it must keep its existing chunks rather than be treated as
		// changed-and-empty, which is what the purge below would otherwise do.
		if sf.ReadErr != nil || sf.ContentOmitted {
			continue
		}
```

2. In `run.go`, propagate scanner diagnostics immediately after the successful
   `ScanDetailed` call (after the `err != nil` return, before the `files :=`
   loop):

```go
	rep.Warnings = append(rep.Warnings, scanner.Warnings()...)
	// Skips are summarised in one bounded line rather than one warning per
	// path: a mis-rooted workspace skips tens of thousands of entries, and a
	// per-file list would bury the diagnostics that matter.
	if skipped := scanner.Skipped(); len(skipped) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"scan skipped %d entries; first: %s (%s)",
			len(skipped), skipped[0].Path, skipped[0].Reason))
	}
```

3. In the symbol loop (`run.go:69`), skip omitted files after the existing
   `ReadErr` branch:

```go
		if sf.ContentOmitted {
			// The file index above still covers this file; only its symbols are
			// unavailable, because symbol extraction needs the bytes.
			continue
		}
```

4. Add the pathological-root warning. Append to `run.go`, at file scope:

```go
// pathologicalIndexRoot names the kind of root that makes an index pass walk
// orders of magnitude more than a project tree, or returns "" for an ordinary
// root. Such a root is still indexed — refusing would break non-git project
// directories — but the pass says so rather than silently spending minutes on
// it.
func pathologicalIndexRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return "a filesystem root"
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == abs {
		return "the user's home directory"
	}
	return ""
}
```

5. In `Run`, immediately after `rep` is initialised (line 45), add:

```go
	if kind := pathologicalIndexRoot(deps.Root); kind != "" {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"index root %s is %s, so this pass walks far more than a project tree; set indexing.root or run Marshal from a project directory",
			deps.Root, kind))
	}
```

6. Add `"os"` and `"path/filepath"` to `run.go`'s imports.

7. Add to `internal/index/indexer_test.go`, beside the existing
   `TestReindexTransientReadErrorKeepsExistingChunks` (line 245), whose setup it
   mirrors exactly:

```go
func omitted(path, hash string) repo.ScannedFile {
	return repo.ScannedFile{
		FileIndex:      db.FileIndex{Path: path, Language: "go", Hash: hash, SizeBytes: 16},
		ContentOmitted: true,
	}
}

func TestReindexContentOmittedKeepsExistingChunks(t *testing.T) {
	database := newTestDB(t)
	projectID := mustCreateProject(t, database, t.TempDir())
	ix := NewIndexer(database, &fakeEmbedder{model: "m1"})
	ctx := context.Background()

	first := []repo.ScannedFile{scanned("a.go", "h1", "package a\n")}
	if _, err := ix.Reindex(ctx, projectID, first, syms("a.go")); err != nil {
		t.Fatal(err)
	}
	state, err := database.ChunkedFiles(projectID)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := state["a.go"]
	if !ok {
		t.Fatal("a.go has no chunks after the first pass")
	}

	second := []repo.ScannedFile{omitted("a.go", before.FileHash)}
	stats, err := ix.Reindex(ctx, projectID, second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesPurged != 0 {
		t.Errorf("purged %d files, want 0", stats.FilesPurged)
	}
	after, err := database.ChunkedFiles(projectID)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := after["a.go"]
	if !ok {
		t.Fatal("a.go lost its chunks: an omitted file must keep them")
	}
	if got.FileHash != before.FileHash {
		t.Errorf("hash = %q, want %q", got.FileHash, before.FileHash)
	}
}
```

If `fakeEmbedder`'s model field or the `newTestDB`/`mustCreateProject`/`scanned`/
`syms` helpers differ from the above, copy the exact setup from
`TestReindexTransientReadErrorKeepsExistingChunks` — that test is the template
and must keep passing.

8. Add to `internal/index/run_test.go`:

- `TestRunReportsScanSkips` — build a temp root containing a regular file and a
  symlink, `os.Symlink("a.txt", filepath.Join(root, "link.txt"))`, run `Run`
  with a `Deps{Root: root, DB: newTestDB(t), Embedder: nil}` against a created
  project, and assert some `rep.Warnings` entry contains
  `"scan skipped 1 entries"` and `"(symlink)"`.
- `TestRunWarnsOnFilesystemRoot` — assert `pathologicalIndexRoot("/")` returns
  `"a filesystem root"` and `pathologicalIndexRoot(t.TempDir())` returns `""`.

**Verify**

```bash
cd ~/projects/marshal && scripts/test.sh quick ./internal/index
```
Expected: PASS, including the untouched
`TestReindexTransientReadErrorKeepsExistingChunks`.

---

## Task 3: Tighten index defaults and plumb the content budget

**Goal:** A default configuration prunes heavy directories and binary blobs,
caps indexable files at 2 MiB, carries a 512 MiB content budget end to end, and
proves through `Run` that a budget-omitted file keeps its chunks.

**Files:** `internal/app/config/defaults.go`, `internal/app/config/types.go`,
`internal/app/config/file_types.go`, `internal/app/config/merge.go`,
`internal/app/config/save.go`, `internal/app/hosts.go`, `internal/index/run.go`,
`internal/app/config/indexing_defaults_test.go` (new),
`internal/index/run_test.go`.

**Steps**

1. `internal/app/config/types.go` — add to `IndexingConfig` (line 772), directly
   after the `MaxIndexableFileBytes` field at line 777:

```go
	MaxTotalContentBytes    int64    `toml:"max_total_content_bytes"`
```

2. `internal/app/config/file_types.go:114` — after the existing
   `MaxIndexableFileBytes *int64` line:

```go
	MaxTotalContentBytes    *int64   `toml:"max_total_content_bytes"`
```

3. `internal/app/config/merge.go:100` — after the existing
   `set(&cfg.Indexing.MaxIndexableFileBytes, …)` line:

```go
		set(&cfg.Indexing.MaxTotalContentBytes, file.Indexing.MaxTotalContentBytes)
```

4. `internal/app/config/save.go:319` — after the existing
   `MaxIndexableFileBytes:` entry:

```go
			MaxTotalContentBytes:   strutil.Ptr(cfg.Indexing.MaxTotalContentBytes),
```

5. `internal/app/config/defaults.go` — replace the `Indexing:` literal
   (lines 59–66) with:

```go
		Indexing: IndexingConfig{
			UseTreesitter:  false,
			UseEmbeddings:  false,
			SummariseFiles: false,
			// The bare names below are matched against the basename as well as
			// the relative path, so each prunes at any depth. They cover
			// dependency caches, toolchain installs and model/Steam payloads
			// that a home-directory workspace holds in bulk and that no index
			// should read.
			Ignore: []string{
				"node_modules/**", "vendor/**", "dist/**", ".git/**",
				".cache", ".lmstudio", ".cargo", ".rustup", ".gradle", ".m2",
				".npm", ".venv", "venv", "__pycache__", "target", ".next",
				".turbo", "Steam",
				"*.safetensors", "*.gguf", "*.onnx", "*.pak", "*.iso", "*.dmg",
			},
			MaxIndexableFileBytes:  2 * 1024 * 1024, // 2 MiB
			MaxTotalContentBytes:   repo.DefaultMaxTotalContentBytes,
			MaxSearchableFileBytes: 1 * 1024 * 1024, // 1 MiB
		},
```

6. Add `"marshal/internal/repo"` to `defaults.go`'s imports if absent.

7. `internal/index/run.go` — add `MaxTotalContentBytes int64` to `Deps` (after
   `MaxBytes`), with the Contracts comment, and pass it into the scanner
   config on line 47:

```go
	scanner := repo.NewScanner(repo.Config{
		Root:                 deps.Root,
		Ignore:               deps.Ignore,
		MaxIndexableFileBytes: deps.MaxBytes,
		MaxTotalContentBytes: deps.MaxTotalContentBytes,
	})
```

8. `internal/app/hosts.go:540` — add the matching field beside `MaxBytes:`:

```go
		MaxTotalContentBytes: cfg.Indexing.MaxTotalContentBytes,
```

9. `internal/app/config/indexing_defaults_test.go` (new file):

```go
func TestDefaultIndexingBounds(t *testing.T) {
	cfg := Default()
	if got, want := cfg.Indexing.MaxIndexableFileBytes, int64(2<<20); got != want {
		t.Errorf("MaxIndexableFileBytes = %d, want %d", got, want)
	}
	if got, want := cfg.Indexing.MaxTotalContentBytes, repo.DefaultMaxTotalContentBytes; got != want {
		t.Errorf("MaxTotalContentBytes = %d, want %d", got, want)
	}
	for _, want := range []string{".lmstudio", "*.gguf", "*.safetensors", "Steam"} {
		found := false
		for _, pattern := range cfg.Indexing.Ignore {
			if pattern == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Ignore does not contain %q", want)
		}
	}
}
```

The constructor is `Default()` (`defaults.go:10`), and the test file is
`package config`; add `"marshal/internal/repo"` to the test file's imports for
the `repo.DefaultMaxTotalContentBytes` reference.

10. Add a round-trip case for `max_total_content_bytes` to
    `internal/app/config/save_test.go`, using
    `TestSaveProjectConfigRoundTripsAgentScalars` (line 382) as the template —
    it is the closest existing "set a scalar, save, reload, assert it survived"
    test. No existing test names `max_indexable_file_bytes`, so there is no
    established case to extend; follow that template's shape exactly.

11. Add to `internal/index/run_test.go`:

- `TestRunContentBudgetKeepsChunksOfOmittedFiles` — create a temp root with two
  1 KiB `.go` files, run `Run` once with a `Deps` carrying
  `MaxTotalContentBytes: 1 << 10` and a non-nil fake embedder, then assert
  `rep.Files == 2` and that `database.ChunkedFiles(projectID)` is non-empty for
  the retained file. Run again with the same deps and assert the second
  `Report` reports no growth in `FilesEmbedded` and that the omitted file's
  chunks were not purged (`FilesPurged == 0`).

**Verify**

```bash
cd ~/projects/marshal && scripts/test.sh quick ./internal/app/config ./internal/index
```
Expected: PASS. The pre-existing config round-trip tests for
`max_indexable_file_bytes` must still pass.

---

## Task 4: Back off capture for a workspace that keeps failing

**Goal:** A failed capture suppresses further attempts for that workspace for
five minutes, and a later success clears the suppression.

**Files:** `internal/snapshot/manager.go`, `internal/snapshot/admission.go`,
`internal/snapshot/backoff_test.go` (new).

**Steps**

1. In `manager.go`, add `captureBackoff` and `captureSuppression` verbatim from
   Contracts. Place the constant beside the other snapshot timing constants in
   the block that sets `commandDeadline: 30 * time.Second` (line 413) and the
   type beside the `Manager` struct.

2. Add the `Manager` field, beside `mu sync.Mutex` (line 102):

```go
	// captureSuppressions records a timed backoff per canonical workspace root
	// after a failed capture. Guarded by mu. Never persisted: a restart retries
	// once, which is the correct cost.
	captureSuppressions map[string]captureSuppression
```

3. Add the four methods verbatim from Contracts, with these bodies:

```go
func (m *Manager) suppressedCapture(root string) (StoreReason, bool) {
	key, ok := m.captureKey(root)
	if !ok {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.captureSuppressions[key]
	if !ok {
		return "", false
	}
	if !m.now().Before(entry.until) {
		delete(m.captureSuppressions, key)
		return "", false
	}
	return entry.reason, true
}

func (m *Manager) noteCaptureFailure(root string, reason StoreReason) {
	key, ok := m.captureKey(root)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.captureSuppressions == nil {
		m.captureSuppressions = map[string]captureSuppression{}
	}
	m.captureSuppressions[key] = captureSuppression{reason: reason, until: m.now().Add(captureBackoff)}
}

func (m *Manager) clearCaptureSuppression(root string) {
	key, ok := m.captureKey(root)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.captureSuppressions, key)
}

// captureKey canonicalises a workspace root so two spellings of the same
// directory share one suppression entry.
func (m *Manager) captureKey(root string) (string, bool) {
	if root == "" {
		return "", false
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, true
}
```

4. In `admission.go`, change `AdmitAndCaptureWithCleanup` (line 640) to named
   results and add the gate plus the recording defer:

```go
func (m *Manager) AdmitAndCaptureWithCleanup(ctx context.Context, cat *Catalog, req CaptureRequest) (captured string, admission *AdmissionResult, err error) {
	if reason, ok := m.suppressedCapture(req.WorkspaceRoot); ok {
		return "", nil, storeErrorf(reason,
			"snapshot capture for %s is suppressed for %s after an earlier failure",
			req.WorkspaceRoot, captureBackoff)
	}
	defer func() {
		if err == nil {
			m.clearCaptureSuppression(req.WorkspaceRoot)
			return
		}
		if reason := ReasonOf(err); reason != "" {
			m.noteCaptureFailure(req.WorkspaceRoot, reason)
		}
	}()
```

Then change exactly one statement in the existing body: the first line is
`admission, err := m.AdmitCapture(ctx, cat, req)`. With both names now bound as
results, `:=` is a "no new variables on left side of :=" compile error, so it
becomes `admission, err = m.AdmitCapture(ctx, cat, req)`.

Every other statement stays byte-identical. `state, err := cat.ReserveStaging(…)`
and `result, err := m.captureToGeneration(…)` each introduce one new variable,
so their `:=` is still legal and `err` is the named result rather than a shadow
(both statements sit in the function's own block). Every existing `return`
assigns to the named results before the defer runs, so the defer observes the
real error.

If `ReasonOf` returns `""` for an uncategorised error, nothing is recorded —
that is deliberate, so an unknown fault does not suppress a workspace.

5. `internal/snapshot/backoff_test.go` (new):

- `TestCaptureBackoffSuppressesSecondAttempt` — with `newTestManager(t)` (from
  `manager_test.go:19`), call `noteCaptureFailure(root, ReasonCommandTimeout)`
  and assert `suppressedCapture(root)` returns that reason and `true`; then call
  `clearCaptureSuppression(root)` and assert it returns `false`.
- `TestCaptureBackoffExpires` — set the manager's `now` seam to a fixed instant,
  record a failure, advance `now` past `captureBackoff`, and assert
  `suppressedCapture` returns `false` and that the entry was deleted from
  `captureSuppressions`.
- `TestCaptureKeyCanonicalisesRoot` — assert
  `captureKey(root+"/./") == captureKey(root)`.

**Verify**

```bash
cd ~/projects/marshal && scripts/test.sh quick ./internal/snapshot
```
Expected: PASS, including the existing admission, capture and service tests.

---

## Task 5: Bound the pre-write capture at the tool-call path

**Goal:** One pre-write capture can spend at most five seconds, a capture that
hits that bound is reported as `capture_timeout` rather than being swallowed as
a cancellation, and the tool call proceeds either way.

**Files:** `internal/agent/snapshot_warning.go`,
`internal/agent/snapshot_warning_test.go`.

**Steps**

1. Add `snapshotCaptureDeadline` verbatim from Contracts, beside the other
   `snapshotReason` / capture constants near the top of `snapshot_warning.go`.

2. Add `reasonCaptureTimeout snapshotReason = "capture_timeout"` to the
   `snapshotReason` const block beside `reasonCaptureSkipped` (line 138), with a
   comment in the same voice as its neighbours:

```go
	// reasonCaptureTimeout is a capture that exceeded snapshotCaptureDeadline:
	// the workspace could not be enumerated in time, so rollback protection for
	// this tool call is missing for a reason that is worth naming.
	reasonCaptureTimeout snapshotReason = "capture_timeout"
```

3. In the `switch reason` that builds the problem detail (the block containing
   `case reasonCaptureSkipped:` at line 265) add a sibling case:

```go
	case reasonCaptureTimeout:
		return fmt.Sprintf("the workspace could not be enumerated within %s", snapshotCaptureDeadline)
```

`snapshotReasonDetail` returns a string in every case and ends with
`default: return "the capture failed"`, so the case above is the whole change
there. Do **not** add `reasonCaptureTimeout` to `snapshotRecoverySubcommand`
(line 311): that list produces the `marshal snapshots cleanup` advice, and a
capture that ran out of time is not a store that needs cleaning. Falling through
to its `default: return ""` is the correct outcome.

4. In `snapshotCapture` (line 615), replace the opening and the `err != nil`
   head of the switch:

```go
func (r *Runner) snapshotCapture(ctx context.Context, phase SnapshotPhase, files []string) {
	if r == nil || r.Snapshotter == nil {
		return
	}
	// A capture sits on the tool-call path, so it gets a bound of its own
	// rather than inheriting the manager's 30s command deadline: a workspace too
	// large to enumerate must cost the tool call a few seconds, not thirty.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, snapshotCaptureDeadline)
	defer cancel()
	hash, err := r.Snapshotter.Track(ctx)
	switch {
	case err != nil:
		// Our own bound firing is a capture outcome, not a cancellation, so it
		// is tested BEFORE the cancellation filters below. Those treat any error
		// matching ctx.Err() as an expected abort and log it silently, which
		// would make a timeout invisible.
		if errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil {
			r.reportSnapshotProblem(phase, snapshotProblem{reason: reasonCaptureTimeout, err: err})
			return
		}
		if snapshotIsSteeringCancellation(ctx, err) {
```

The rest of the function body is unchanged.

5. Add to `internal/agent/snapshot_warning_test.go`, using the existing
   `fakeWriteSnapshotter` (line 30) and `writeToolRunner` (line 85) harness:

- `TestPreWriteCaptureTimeoutIsReportedNotSwallowed` — a `fakeWriteSnapshotter`
  whose `Track` blocks until `ctx.Done()` and returns `ctx.Err()`; assert the
  transcript contains a `capture_timeout` warning and that the tool call still
  ran (mirror `TestPreWriteCaptureFailureStillRunsToolAndWarns`, line 117).
- `TestPreWriteCaptureDeadlineIsFiveSeconds` — assert
  `snapshotCaptureDeadline == 5*time.Second`, so the number is pinned by a test
  rather than only by a constant.

**Verify**

```bash
cd ~/projects/marshal && scripts/test.sh quick ./internal/agent
```
Expected: PASS, including `TestSteeringSnapshotCancelDoesNotWarnAsFailure`
(line 1235) and `TestContextCancellationErrorIsSuppressed` (line 398), which
pin that real cancellations stay silent.

---

## Final verification

Run from `~/projects/marshal`:

```bash
scripts/test.sh quick ./internal/repo ./internal/index ./internal/app/config ./internal/snapshot ./internal/agent
go vet ./...
gofmt -l .
scripts/test.sh quick
```

Expected: every package passes; `go vet` is silent; `gofmt -l .` prints nothing;
the whole suite passes with `-short`. Do not run `scripts/test.sh budgets` — the
repository's own documentation states those need an idle laptop.

## Integration notes

- **Behaviour changes now visible.** Index skips and scanner warnings appear in
  `Report.Warnings`, and a budget-dropped file keeps its old embeddings instead
  of being purged. Both follow from the approved design; neither is a
  regression to fix.
- **Ordering matters for one seam.** Task 3 threads `MaxTotalContentBytes`
  through `Deps`. Tasks 1 and 2 must land first, because `ContentOmitted` and
  the scanner budget do not exist before then.
- **Parallel-safe with: Task 4.** Task 4 (`internal/snapshot`) and Task 5
  (`internal/agent`) share no files, and neither consumes the other's output.
  Every other pair runs sequentially.
- **Commit per task**, as `<plan-slug>: task N — <task title>`.
- **Do not commit this plan separately.** Per `AGENTS.md`, feature docs live
  under `docs/<feature>/` and commit *with the work they describe*; this plan and
  `design.md` should go in with Task 1 or the finishing commit, never alone.
- `max_total_content_bytes` is a new user-visible config key. It is additive, so
  no migration is required and an old config file keeps working.
