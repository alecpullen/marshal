# Rollover Structured "files" Digest Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close P2 item 2 in `docs/marshal-context-rollover-spec.md`: a built-in structured digest provider (`FilesDigestProvider`) that derives a cheap "files touched + outstanding TODOs" digest from Marshal's own on-disk state, so non-sdd2 agent loops get the structured benefit without an LLM call.

**Architecture:** `FilesDigestProvider` implements the existing `rollover.DigestProvider` seam. It reads the session's `file_writes`/`file_reads` tables (already populated by `internal/filetrack`) and runs a read-only `git status --short` / `git grep -nE 'TODO|FIXME|XXX'` over the working tree via the existing `native.CommandRunner` interface — zero LLM cost. It is selected by a new `digest_provider = "files"` config value; the default (`"llm_summary"`, the empty string, and `"auto"`) preserves the current `LLMSummaryProvider` behaviour exactly, and `"minimal"` keeps the minimal fallback. A non-git workspace degrades gracefully (files-only digest); a present-but-failing git command fails the whole digest so the controller's archive-before-digest ordering falls back to the minimal digest rather than silently claiming "no changes."

**Tech Stack:** Go, `modernc.org/sqlite`, standard `testing`, the existing `native.CommandRunner` interface (`internal/tools/native`).

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter dependency). Test with `go test ./...`.
- Timestamps in `internal/db` are stored as **TEXT RFC3339 UTC** (`.UTC().Format(time.RFC3339)`), never integer epochs.
- `internal/db` imports nothing from `internal/app/config`; keep that direction (config may import db, never the reverse).
- Rollover is off by default (`[session.rollover] enabled = false`); this feature is additionally gated behind `digest_provider = "files"` and must not change behaviour for any session that does not opt in.
- A `DigestProvider` returning an error never loses history — the controller archives the outgoing generation **before** calling `Digest` (`internal/rollover/controller.go:147`), and falls back to `MinimalDigest` on any error. The structured provider relies on this: a git/file-read failure degrades to the minimal digest, never to a panic or lost archive.
- The verbose `rollover` log event must never contain digest text or wire content — only `digest_len` (`TestController_Rollover_VerboseEvent` enforces this). The new `digest_source = "files"` label is metadata only and is safe to log.
- New config fields use the existing pointer-field merge pattern (`*string`/`*bool` in `file_types.go`, `set(&dst, src)` in `merge.go`) so a partial TOML never zeroes an unset field back to its zero value.

---

## File Structure

- Create: `internal/rollover/filesdigest.go` — `FilesDigestProvider`, `fileStateSource` interface, digest text assembly (no I/O of its own).
- Create: `internal/rollover/filesdigest_test.go` — provider tests with a fake `fileStateSource`.
- Create: `internal/rollover/filesstate.go` — production `fileStateSource` (filetrack query + git/grep via `CommandRunner`).
- Create: `internal/rollover/filesstate_test.go` — `FilesState` tests with a real sqlite DB and a fake `CommandRunner`.
- Modify: `internal/app/config/types.go` — add `DigestProvider string` to `RolloverConfig`.
- Modify: `internal/app/config/file_types.go` — add `DigestProvider *string` to `fileRollover`.
- Modify: `internal/app/config/defaults.go` — default `DigestProvider = "llm_summary"`.
- Modify: `internal/app/config/merge.go` — merge + validate `digest_provider` (allow `"llm_summary"|"files"|"minimal"|"auto"`, where `"auto"` and `""` resolve to `"llm_summary"`).
- Modify: `internal/app/config/rollover_test.go` — default + merge + validation tests.
- Modify: `internal/app/app.go` — select the digest provider in `buildAgentRunner` based on `cfg.DigestProvider`; construct `FilesDigestProvider` when `"files"`.
- Modify: `internal/app/rollover_wiring_test.go` — assert the `"files"` provider is wired when configured.
- Modify: `docs/marshal-context-rollover-spec.md` — flip P2 item 2 (structured digest) to implemented; document `digest_provider` in Configuration.

---

## Task 1: DigestProvider config field and validation

**Files:**
- Modify: `internal/app/config/types.go` (after `DigestModel` field, ~line 140)
- Modify: `internal/app/config/file_types.go` (after `DigestModel`, ~line 177)
- Modify: `internal/app/config/defaults.go` (inside `Rollover{...}`, ~line 116)
- Modify: `internal/app/config/merge.go` (rollover block, ~line 250)
- Test: `internal/app/config/rollover_test.go`

**Interfaces:**
- Produces: `RolloverConfig.DigestProvider string` (`"llm_summary"` default); validated at merge to one of `"llm_summary"|"files"|"minimal"|"auto"`; `"auto"` and `""` normalize to `"llm_summary"`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/app/config/rollover_test.go`:

```go
func TestDefaultRolloverDigestProvider(t *testing.T) {
	if got := Default().Session.Rollover.DigestProvider; got != "llm_summary" {
		t.Errorf("default Rollover.DigestProvider = %q, want %q", got, "llm_summary")
	}
}

func TestMergeRolloverDigestProvider(t *testing.T) {
	cfg := Default()
	dp := "files"
	if err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{DigestProvider: &dp}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.Session.Rollover.DigestProvider != "files" {
		t.Errorf("Rollover.DigestProvider = %q, want %q", cfg.Session.Rollover.DigestProvider, "files")
	}
}

func TestMergeRolloverDigestProviderAutoNormalizes(t *testing.T) {
	cfg := Default()
	dp := "auto"
	if err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{DigestProvider: &dp}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.Session.Rollover.DigestProvider != "llm_summary" {
		t.Errorf("auto normalized to %q, want %q", cfg.Session.Rollover.DigestProvider, "llm_summary")
	}
}

func TestMergeRolloverDigestProviderRejectsUnknown(t *testing.T) {
	cfg := Default()
	dp := "magic"
	err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{DigestProvider: &dp}},
	})
	if err == nil || !strings.Contains(err.Error(), "digest_provider") {
		t.Fatalf("merge err = %v, want error mentioning digest_provider", err)
	}
}
```

Add `"strings"` to the import block of `rollover_test.go` if not present (check the existing import block — it currently only imports `"testing"`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run TestDefaultRolloverDigestProvider -v`
Expected: FAIL — `cfg.Session.Rollover.DigestProvider undefined`.

- [ ] **Step 3: Add the field, default, file-type, and merge validation**

In `internal/app/config/types.go`, inside `RolloverConfig`, add after the `DigestModel` field:

```go
	DigestProvider          string `toml:"digest_provider"`
```

In `internal/app/config/file_types.go`, inside `fileRollover`, add after `DigestModel *string`:

```go
	DigestProvider          *string `toml:"digest_provider"`
```

In `internal/app/config/defaults.go`, inside the `Rollover: RolloverConfig{...}` literal, add:

```go
				DigestProvider:          "llm_summary",
```

In `internal/app/config/merge.go`, inside the `if file.Session != nil && file.Session.Rollover != nil {` block, after the `set(&cfg.Session.Rollover.DigestModel, r.DigestModel)` line, add:

```go
		set(&cfg.Session.Rollover.DigestProvider, r.DigestProvider)
		switch cfg.Session.Rollover.DigestProvider {
		case "", "auto":
			cfg.Session.Rollover.DigestProvider = "llm_summary"
		case "llm_summary", "files", "minimal":
			// valid
		default:
			return fmt.Errorf("session.rollover.digest_provider: unrecognized value %q (want llm_summary, files, minimal, or auto)", cfg.Session.Rollover.DigestProvider)
		}
```

Confirm `fmt` is already imported in `merge.go` (it is — used by the retention validation added in the prior plan).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/config/ -run TestDefaultRollover|TestMergeRollover -v`
Expected: PASS for all four new tests plus the existing `TestDefaultRollover` / `TestMergeRollover` / `TestRolloverVerboseDefaultsFalseAndMerges`.

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Commit**

```bash
git add internal/app/config/types.go internal/app/config/file_types.go internal/app/config/defaults.go internal/app/config/merge.go internal/app/config/rollover_test.go
git commit -m "feat(config): add rollover digest_provider field with validation

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 2: FilesDigestProvider implementation

**Files:**
- Create: `internal/rollover/filesdigest.go`
- Test: `internal/rollover/filesdigest_test.go`

**Interfaces:**
- Consumes: `rollover.DigestProvider`, `rollover.GenerationHandle`, `rollover.SourceStructured` (from `digest.go`).
- Produces: `FilesDigestProvider` struct implementing `DigestProvider`; `fileStateSource` internal interface (query + command-runner seam) so the provider is unit-testable without a real git repo or DB; `errNoGit` sentinel error; `NewFilesDigestProvider(state fileStateSource) *FilesDigestProvider`.

- [ ] **Step 1: Write the failing tests**

Create `internal/rollover/filesdigest_test.go`:

```go
package rollover

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeFileState implements fileStateSource for testing.
type fakeFileState struct {
	written   []string
	read      []string
	status    string
	statusOK  bool
	todos     string
	todoOK    bool
	statusErr error
	todoErr   error
}

func (f *fakeFileState) WrittenFiles() ([]string, error) { return f.written, nil }
func (f *fakeFileState) ReadFiles() ([]string, error)     { return f.read, nil }
func (f *fakeFileState) GitStatusShort() (string, error) {
	if f.statusErr != nil {
		return "", f.statusErr
	}
	if !f.statusOK {
		return "", errNoGit
	}
	return f.status, nil
}
func (f *fakeFileState) OutstandingTodos() (string, error) {
	if f.todoErr != nil {
		return "", f.todoErr
	}
	if !f.todoOK {
		return "", nil
	}
	return f.todos, nil
}

func TestFilesDigestProvider_DigestContainsWrittenFiles(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		written:  []string{"internal/foo/bar.go", "cmd/marshal/main.go"},
		read:     []string{"internal/baz/qux.go"},
		status:   " M internal/foo/bar.go\n?? cmd/marshal/main.go",
		statusOK: true,
	}}
	digest, source, err := p.Digest(context.Background(), GenerationHandle{
		SessionID: "sess-1", GenerationID: "gen-1", Seq: 2,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if source != SourceStructured {
		t.Errorf("source = %q, want %q", source, SourceStructured)
	}
	for _, want := range []string{"internal/foo/bar.go", "cmd/marshal/main.go", "Generation 2"} {
		if !strings.Contains(digest, want) {
			t.Errorf("digest missing %q\ngot:\n%s", want, digest)
		}
	}
}

func TestFilesDigestProvider_DigestIncludesTodos(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		written: []string{"a.go"},
		status:  " M a.go", statusOK: true,
		todos: "a.go:10: TODO refactor this\nb.go:5: FIXME handle nil", todoOK: true,
	}}
	digest, _, err := p.Digest(context.Background(), GenerationHandle{Seq: 0})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if !strings.Contains(digest, "TODO refactor this") || !strings.Contains(digest, "FIXME handle nil") {
		t.Errorf("digest missing TODO/FIXME lines\ngot:\n%s", digest)
	}
}

func TestFilesDigestProvider_NoGitDegradedToFilesOnly(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		written: []string{"a.go"}, statusOK: false, todoOK: false,
	}}
	digest, source, err := p.Digest(context.Background(), GenerationHandle{Seq: 1})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if source != SourceStructured {
		t.Errorf("source = %q, want %q (degraded still structured)", source, SourceStructured)
	}
	if strings.Contains(digest, "git status") {
		t.Errorf("degraded digest should not mention git status\ngot:\n%s", digest)
	}
	if !strings.Contains(digest, "a.go") {
		t.Errorf("degraded digest missing written file\ngot:\n%s", digest)
	}
}

func TestFilesDigestProvider_GitErrorFailsDigest(t *testing.T) {
	// A real git error (not "no git") should fail the provider so the
	// controller falls back to the minimal digest, rather than silently
	// producing a digest that claims no changes when the workspace state
	// is actually unknown.
	p := &FilesDigestProvider{state: &fakeFileState{
		written: []string{"a.go"}, statusErr: errors.New("git crashed"),
	}}
	_, _, err := p.Digest(context.Background(), GenerationHandle{Seq: 0})
	if err == nil {
		t.Fatal("expected error when git status fails, got nil")
	}
}

func TestFilesDigestProvider_EmptyStateProducesMinimalStructured(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		status: "", statusOK: true, todoOK: true,
	}}
	digest, source, err := p.Digest(context.Background(), GenerationHandle{Seq: 3})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if source != SourceStructured {
		t.Errorf("source = %q, want %q", source, SourceStructured)
	}
	if !strings.Contains(digest, "Generation 3") {
		t.Errorf("empty-state digest missing generation header\ngot:\n%s", digest)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollover/ -run TestFilesDigestProvider -v`
Expected: FAIL — `undefined: FilesDigestProvider`, `undefined: fakeFileState`, `undefined: errNoGit`.

- [ ] **Step 3: Write the implementation**

Create `internal/rollover/filesdigest.go`:

```go
package rollover

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errNoGit signals that the workspace is not a git repository (or git is
// unavailable). It is not a fatal error: FilesDigestProvider degrades to a
// files-only digest in that case. A different error (git present but the
// command failed) is fatal and returned from Digest.
var errNoGit = errors.New("not a git repository")

// fileStateSource is the seam FilesDigestProvider reads workspace state
// through. The production implementation (FilesState) queries the session's
// file_reads / file_writes tables and runs git/grep via a CommandRunner;
// tests substitute a fake.
type fileStateSource interface {
	// WrittenFiles returns paths recorded as written this session, in
	// insertion order (most-recent last). An empty slice is valid.
	WrittenFiles() ([]string, error)
	// ReadFiles returns paths recorded as read this session.
	ReadFiles() ([]string, error)
	// GitStatusShort returns `git status --short` stdout. It returns
	// errNoGit when the workspace is not a git repo; any other error is
	// fatal.
	GitStatusShort() (string, error)
	// OutstandingTodos returns grep output for TODO/FIXME/XXX markers in
	// tracked source files, or "" when unavailable. Errors are treated
	// as "no todos" (non-fatal).
	OutstandingTodos() (string, error)
}

// FilesDigestProvider produces a structured resume digest from the session's
// on-disk file-tracking state and a read-only scan of the working tree,
// without an LLM call. On a non-fatal gap (no git, no todos) it degrades to a
// files-only digest; on a fatal git error it returns an error so the
// controller falls back to the minimal digest.
type FilesDigestProvider struct {
	state fileStateSource
}

// NewFilesDigestProvider constructs a provider backed by the given state
// source.
func NewFilesDigestProvider(state fileStateSource) *FilesDigestProvider {
	return &FilesDigestProvider{state: state}
}

// Name returns the provider label used in verbose logging and digest_source.
func (p *FilesDigestProvider) Name() string { return "files" }

// Digest assembles the structured resume digest. The digest always names the
// generation and lists written/read files; git status and TODO markers are
// included when available. A non-git workspace degrades gracefully; a
// present-but-failing git command fails the whole digest (returned as an
// error) so the controller's minimal fallback takes over rather than
// silently claiming "no changes."
func (p *FilesDigestProvider) Digest(_ context.Context, h GenerationHandle) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Generation %d — resuming from structured on-disk state.\n\n", h.Seq)

	written, werr := p.state.WrittenFiles()
	read, rerr := p.state.ReadFiles()
	if werr != nil {
		return "", "", fmt.Errorf("files digest: written files: %w", werr)
	}
	if rerr != nil {
		return "", "", fmt.Errorf("files digest: read files: %w", rerr)
	}

	b.WriteString("## Files written this session\n")
	if len(written) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, p := range written {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}

	b.WriteString("\n## Files read this session\n")
	if len(read) == 0 {
		b.WriteString("(none)\n")
	} else {
		// Cap the read list; it can be long and the digest is meant to be short.
		shown := read
		if len(shown) > 20 {
			shown = shown[:20]
		}
		for _, p := range shown {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		if len(read) > 20 {
			fmt.Fprintf(&b, "- ...and %d more\n", len(read)-20)
		}
	}

	status, serr := p.state.GitStatusShort()
	if serr != nil && !errors.Is(serr, errNoGit) {
		// git is present but the command failed — state is unknown.
		return "", "", fmt.Errorf("files digest: git status: %w", serr)
	}
	if serr == nil {
		b.WriteString("\n## Working tree (git status --short)\n")
		if strings.TrimSpace(status) == "" {
			b.WriteString("clean\n")
		} else {
			b.WriteString(status)
			if !strings.HasSuffix(status, "\n") {
				b.WriteString("\n")
			}
		}
	}

	todos, terr := p.state.OutstandingTodos()
	if terr == nil && strings.TrimSpace(todos) != "" {
		b.WriteString("\n## Outstanding TODO/FIXME/XXX markers\n")
		b.WriteString(todos)
		if !strings.HasSuffix(todos, "\n") {
			b.WriteString("\n")
		}
	}

	b.WriteString("\nContinue the task from the above. Re-read any file you need; the full transcript is archived (marshal history).\n")
	return b.String(), SourceStructured, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rollover/ -run TestFilesDigestProvider -v`
Expected: PASS for all five tests.

- [ ] **Step 5: Commit**

```bash
git add internal/rollover/filesdigest.go internal/rollover/filesdigest_test.go
git commit -m "feat(rollover): structured files digest provider (no LLM cost)

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 3: Production fileStateSource — filetrack + git + grep

**Files:**
- Create: `internal/rollover/filesstate.go`
- Test: `internal/rollover/filesstate_test.go`

**Interfaces:**
- Consumes: the session's `file_reads`/`file_writes` tables (populated by `internal/filetrack`); `native.CommandRunner` (`Run(ctx, CommandRequest) (CommandResult, error)`); the workspace root path.
- Produces: `NewFilesState(db *sql.DB, sessionID string, runner native.CommandRunner, root string) *FilesState` implementing `fileStateSource` (Task 2).

- [ ] **Step 1: Write the failing test**

Create `internal/rollover/filesstate_test.go`. The `rollover` package can't use the package-internal `newTestDB` helper from `internal/db`, so build a minimal `*sql.DB` from `modernc.org/sqlite` directly and run the two `CREATE TABLE` statements (copied verbatim from `internal/db/migrations.go:125-137`).

```go
package rollover

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/tools/native"

	_ "modernc.org/sqlite"
)

func openFilesStateDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS file_reads (
		    session_id TEXT NOT NULL,
		    path TEXT NOT NULL,
		    read_at TEXT NOT NULL,
		    PRIMARY KEY(session_id, path)
		)`,
		`CREATE TABLE IF NOT EXISTS file_writes (
		    session_id TEXT NOT NULL,
		    path TEXT NOT NULL,
		    written_at TEXT NOT NULL,
		    PRIMARY KEY(session_id, path)
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeRunner implements native.CommandRunner for testing.
type fakeRunner struct {
	byCmd    map[string]string // command -> stdout
	exitCode int
}

func (f *fakeRunner) Run(_ context.Context, req native.CommandRequest) (native.CommandResult, error) {
	out, ok := f.byCmd[req.Command]
	if !ok {
		return native.CommandResult{ExitCode: f.exitCode}, nil
	}
	return native.CommandResult{Stdout: out, ExitCode: 0}, nil
}

func TestFilesState_WrittenFiles(t *testing.T) {
	db := openFilesStateDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, p := range []string{"a.go", "b.go", "a.go"} { // dup -> deduped by PK
		if _, err := db.Exec(`INSERT INTO file_writes(session_id,path,written_at) VALUES('s1',?,?)`, p, now); err != nil {
			t.Fatal(err)
		}
	}
	fs := NewFilesState(db, "s1", &fakeRunner{}, "/repo")
	got, err := fs.WrittenFiles()
	if err != nil {
		t.Fatalf("WrittenFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2 (deduped): %v", len(got), got)
	}
}

func TestFilesState_GitStatusShort(t *testing.T) {
	db := openFilesStateDB(t)
	runner := &fakeRunner{byCmd: map[string]string{
		"git status --short": " M a.go\n?? b.go",
	}}
	fs := NewFilesState(db, "s1", runner, "/repo")
	out, err := fs.GitStatusShort()
	if err != nil {
		t.Fatalf("GitStatusShort: %v", err)
	}
	if out != " M a.go\n?? b.go" {
		t.Errorf("got %q", out)
	}
}

func TestFilesState_GitStatusShortNoGit(t *testing.T) {
	db := openFilesStateDB(t)
	runner := &fakeRunner{exitCode: 128} // git exits 128 outside a repo
	fs := NewFilesState(db, "s1", runner, "/repo")
	_, err := fs.GitStatusShort()
	if !errors.Is(err, errNoGit) {
		t.Fatalf("err = %v, want errNoGit", err)
	}
}

func TestFilesState_OutstandingTodos(t *testing.T) {
	db := openFilesStateDB(t)
	runner := &fakeRunner{byCmd: map[string]string{
		"git grep -nE 'TODO|FIXME|XXX' -- ':*.go'": "a.go:10: TODO fix\nb.go:5: FIXME nil",
	}}
	fs := NewFilesState(db, "s1", runner, "/repo")
	out, err := fs.OutstandingTodos()
	if err != nil {
		t.Fatalf("OutstandingTodos: %v", err)
	}
	if !strings.Contains(out, "TODO fix") {
		t.Errorf("missing TODO, got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollover/ -run TestFilesState -v`
Expected: FAIL — `undefined: NewFilesState`.

- [ ] **Step 3: Write the implementation**

Create `internal/rollover/filesstate.go`:

```go
package rollover

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/native"
)

// FilesState is the production fileStateSource: it reads the session's
// file_reads/file_writes tables and runs read-only git/grep commands via a
// CommandRunner. The CommandRunner is the same one the native tool set uses
// (sandboxed or not), so this provider inherits the workspace's command
// policy without re-implementing it.
type FilesState struct {
	db        *sql.DB
	sessionID string
	runner    native.CommandRunner
	root      string
}

// NewFilesState constructs a FilesState for the given session and workspace.
func NewFilesState(db *sql.DB, sessionID string, runner native.CommandRunner, root string) *FilesState {
	return &FilesState{db: db, sessionID: sessionID, runner: runner, root: root}
}

// WrittenFiles returns paths written this session, in insertion order.
func (f *FilesState) WrittenFiles() ([]string, error) {
	return f.queryPaths(`SELECT path FROM file_writes WHERE session_id = ? ORDER BY written_at`)
}

// ReadFiles returns paths read this session, in insertion order.
func (f *FilesState) ReadFiles() ([]string, error) {
	return f.queryPaths(`SELECT path FROM file_reads WHERE session_id = ? ORDER BY read_at`)
}

func (f *FilesState) queryPaths(query string) ([]string, error) {
	rows, err := f.db.Query(query, f.sessionID)
	if err != nil {
		return nil, fmt.Errorf("query paths: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// GitStatusShort runs `git status --short` and returns stdout. A non-zero
// exit (typically 128, "not a git repository") is mapped to errNoGit so the
// provider degrades to a files-only digest; any other failure is a real
// error.
func (f *FilesState) GitStatusShort() (string, error) {
	res, err := f.runner.Run(context.Background(), native.CommandRequest{
		Command: "git status --short",
		Dir:     f.root,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		// 128 is git's "fatal: not a git repository". Treat any non-zero
		// as "no git available" rather than guessing exit codes.
		return "", errNoGit
	}
	return res.Stdout, nil
}

// OutstandingTodos runs a scoped `git grep` for TODO/FIXME/XXX markers. It is
// best-effort: any error (including no-git) returns "" so the digest simply
// omits the section. Tracked Go files are the default scope.
func (f *FilesState) OutstandingTodos() (string, error) {
	res, err := f.runner.Run(context.Background(), native.CommandRequest{
		Command: "git grep -nE 'TODO|FIXME|XXX' -- ':*.go'",
		Dir:     f.root,
		Timeout: 30 * time.Second,
	})
	if err != nil || res.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(res.Stdout), nil
}
```

`internal/tools/native` exports `CommandRunner` (interface, native.go:61), `CommandRequest{Command, Dir, Timeout, ...}` (native.go:65), and `CommandResult{Stdout, ExitCode, ...}` (native.go:75) — the git tool uses the same shape (`git.go:73`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rollover/ -run TestFilesState -v`
Expected: PASS for all four tests.

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Commit**

```bash
git add internal/rollover/filesstate.go internal/rollover/filesstate_test.go
git commit -m "feat(rollover): production fileStateSource (filetrack + git grep)

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 4: Wire the "files" provider in app.go

**Files:**
- Modify: `internal/app/app.go` (`buildAgentRunner`, ~lines 544-549)
- Test: `internal/app/rollover_wiring_test.go`

**Interfaces:**
- Consumes: `config.RolloverConfig.DigestProvider` (Task 1); `rollover.NewFilesDigestProvider` / `NewFilesState` (Tasks 2–3); the sandboxed `commandRunner` already constructed in `buildAgentRunner` (`app.go:393`); `state.WorkingDir`; `database.SQLDB()`.
- Produces: when `DigestProvider == "files"`, the controller's `Digest` is a `*FilesDigestProvider`; `"minimal"` uses `minimalDigestProvider`; otherwise unchanged (LLMSummaryProvider or minimal fallback).

- [ ] **Step 1: Write the failing test**

Add to `internal/app/rollover_wiring_test.go`:

```go
func TestBuildAgentRunnerWiresFilesDigestProvider(t *testing.T) {
	ctx := context.Background()
	cfg := rolloverTestConfig()
	cfg.Session.Rollover.Enabled = true
	cfg.Session.Rollover.Policy = "turn_count"
	cfg.Session.Rollover.DigestProvider = "files"

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("sess_test", projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(cfg, tmp, time.Unix(100, 0), session.Persistence{DB: database, SessionID: "sess_test"})

	runner, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, database, projectID, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.Rollover == nil || runner.Rollover.Controller == nil {
		t.Fatal("rollover controller not wired")
	}
	if _, ok := runner.Rollover.Controller.Digest.(*rollover.FilesDigestProvider); !ok {
		t.Fatalf("Digest = %T, want *rollover.FilesDigestProvider", runner.Rollover.Controller.Digest)
	}
}

func TestBuildAgentRunnerDefaultsToLLMSummaryProvider(t *testing.T) {
	ctx := context.Background()
	cfg := rolloverTestConfig()
	cfg.Session.Rollover.Enabled = true
	cfg.Session.Rollover.Policy = "context_percent"
	// DigestProvider unset -> default "llm_summary"

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("sess_test", projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(cfg, tmp, time.Unix(100, 0), session.Persistence{DB: database, SessionID: "sess_test"})

	runner, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, database, projectID, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.Rollover == nil || runner.Rollover.Controller == nil {
		t.Fatal("rollover controller not wired")
	}
	if _, ok := runner.Rollover.Controller.Digest.(*rollover.LLMSummaryProvider); !ok {
		t.Fatalf("Digest = %T, want *rollover.LLMSummaryProvider (default)", runner.Rollover.Controller.Digest)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestBuildAgentRunnerWiresFilesDigestProvider -v`
Expected: FAIL — `Digest` is `*rollover.LLMSummaryProvider` (or the minimal fallback), not `*FilesDigestProvider`.

- [ ] **Step 3: Wire the provider**

In `internal/app/app.go`, the `buildAgentRunner` function builds the digest provider around line 545. Replace the current block:

```go
	modelCtxWindow := route.Preset.ContextWindow
	var digestProvider rollover.DigestProvider
	if runner.Provider != nil {
		digestProvider = rollover.NewLLMSummaryProvider(runner, rollover.SummaryDirective)
	}
	if rolloverCtrl, rerr := NewRolloverController(state.SessionID(), cfg.Session.Rollover, database, modelCtxWindow, digestProvider, usageCounter); rerr != nil {
```

with logic that selects the provider by `cfg.Session.Rollover.DigestProvider`:

```go
	modelCtxWindow := route.Preset.ContextWindow
	var digestProvider rollover.DigestProvider
	switch cfg.Session.Rollover.DigestProvider {
	case "files":
		// Structured digest from on-disk state: zero LLM cost. Uses the
		// same sandboxed CommandRunner as the native git tool so it
		// inherits the workspace's command policy.
		fileState := rollover.NewFilesState(database.SQLDB(), state.SessionID(), commandRunner, state.WorkingDir)
		digestProvider = rollover.NewFilesDigestProvider(fileState)
	case "minimal":
		digestProvider = minimalDigestProvider{}
	default: // "llm_summary" and any unset value
		if runner.Provider != nil {
			digestProvider = rollover.NewLLMSummaryProvider(runner, rollover.SummaryDirective)
		}
	}
	if rolloverCtrl, rerr := NewRolloverController(state.SessionID(), cfg.Session.Rollover, database, modelCtxWindow, digestProvider, usageCounter); rerr != nil {
```

Confirm `commandRunner` is in scope at this point in `buildAgentRunner` — it is constructed at line 393 and used by `nativeOpts` at line 450, so it is in scope here too. Confirm `database.SQLDB()` is the accessor for the underlying `*sql.DB` (used by `filetrack.New` at app.go:439, so yes). The local variable is named `fileState` (not `state`, which shadows the outer `state *session.State`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestBuildAgentRunner -v`
Expected: PASS for both new tests and the existing `TestBuildAgentRunnerWiresRollover` / `TestBuildAgentRunnerSkipsRolloverWhenDisabled`.

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Commit**

```bash
git add internal/app/app.go internal/app/rollover_wiring_test.go
git commit -m "feat(app): wire rollover files digest provider from config

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 5: Update the feature spec

**Files:**
- Modify: `docs/marshal-context-rollover-spec.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: Flip P2 item 2 to implemented**

Under "P2 — deferred", replace the unchecked structured-digest bullet:

```markdown
- [ ] A built-in structured digest provider for Marshal's own non-sdd2 agent loops (e.g. a generic "files touched + outstanding TODOs" digest), so sessions get some of the structured benefit without hand-writing a provider.
```

with:

```markdown
- [x] A built-in structured digest provider for Marshal's own non-sdd2 agent loops. Implemented as `FilesDigestProvider` (`internal/rollover/filesdigest.go`), selected by `digest_provider = "files"`. It derives a "files written + files read + git status + outstanding TODO/FIXME/XXX markers" digest from the session's `file_reads`/`file_writes` tables and a read-only `git status --short` / `git grep` scan via the same sandboxed `CommandRunner` the native tools use — zero LLM cost. It degrades gracefully (files-only) outside a git repo and fails over to the minimal digest on a real git error, relying on the controller's archive-before-digest ordering.
```

- [ ] **Step 2: Document `digest_provider` in Configuration**

In the Configuration TOML block (spec ~line 178-189), add after the `digest_model` line:

```toml
digest_provider = "llm_summary"  # "llm_summary" (default) | "files" | "minimal" | "auto" (= llm_summary)
```

- [ ] **Step 3: Add a digest-source note to the "Digest generation" section**

After the "Structured provider (`structured`)" bullet (spec ~line 140), add a paragraph:

```markdown
A built-in structured provider, `files` (`digest_provider = "files"`), gives Marshal's own non-sdd2 agent loops a zero-LLM-cost digest derived from the session's file-tracking state and a read-only working-tree scan. This is the generic "files touched + outstanding TODOs" case the spec deferred: any session can opt into it without hand-writing a `DigestProvider`. A caller that needs richer structured state (the sdd2 case) still registers its own provider and leaves `digest_provider` at the default.
```

- [ ] **Step 4: Commit**

```bash
git add docs/marshal-context-rollover-spec.md
git commit -m "docs: mark rollover structured digest provider implemented (P2.2)

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Final verification

- [ ] Run `gofmt -w .` and `go vet ./...`.
- [ ] Run the full suite: `go test ./...` — expected PASS.
- [ ] Confirm `digest_provider` defaults to `"llm_summary"`, so a default-config session is unchanged (existing `rollover_test.go` / `controller_test.go` / `rollover_wiring_test.go` still green).
- [ ] Confirm the `"files"` provider is selected when `digest_provider = "files"` and degrades to a files-only digest outside a git repo (covered by `TestFilesDigestProvider_NoGitDegradedToFilesOnly`).