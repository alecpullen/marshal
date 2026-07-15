# Domain E2 — Repo Scanner, Gitignore, Language & Indexing Correctness

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close 12 findings in `internal/repo/`, `internal/tools/native/repo_index.go`, `internal/db/files.go`, and `internal/db/symbols.go` by wiring the configured `Indexing.Ignore` and `SkipGitignore` flags into the scanner, fixing the gitignore parser (anchored dir patterns and `!negation`), escaping SQL `LIKE` wildcards, threading `context.Context` through `ExtractSymbols`, eliminating the disk re-read in `repo.index`, and expanding `DetectLanguage`. Findings: F-BUG-97, F-BUG-98, F-BUG-99, F-BUG-100, F-BUG-101, F-BUG-110, F-BUG-111, F-POL-129, F-POL-131, F-POL-132, F-POL-133, F-POL-134.

**Architecture:** Most changes are in `internal/repo/` (gitignore parser, scanner, language, symbol extraction). Two changes touch `internal/db/` (`FilesMatchingBasename` and `FindSymbols` LIKE escaping) and depend on the `internal/db/sqlutil/escapeLike` helper that **E1** adds in its Task 14 (the recursive CTE task). E1 should be merged first; E2 picks up `sqlutil` once available.

**Tech Stack:** Go 1.26, tree-sitter (no version change), `modernc.org/sqlite`. No new dependencies.

**Source audit:** `docs/14-codebase-improvement-audit-2026-07-14.md`, domain E (items F-BUG-97, F-BUG-98, F-BUG-99, F-BUG-100, F-BUG-101, F-BUG-110, F-BUG-111, F-POL-129, F-POL-131, F-POL-132, F-POL-133, F-POL-134).

**Pre-resolved (handled in earlier batches, do not redo):** F-BUG-102, F-POL-130, F-SEC-122, F-SEC-123. Listed here for tracking only.

## Global Constraints

- `go build ./...` must succeed after every task.
- `go test ./...` must pass after every task.
- `gofmt -w .` after any file change.
- This batch assumes `internal/db/sqlutil/escapeLike` already exists (added by E1). If E1 has not yet merged, port the helper in a pre-step before any DB-related task in E2.
- Gitignore semantics must match git's behaviour for the cases listed in the test plan; do not try to implement the full gitignore spec (no `**`/brace expansion, no character classes — those are explicitly out of scope).
- Backward compatibility for `repo.Scanner` callers: every signature change must preserve the public surface; the existing `Config{Root}` callers continue to work.
- New exported symbols have doc comments. New error paths return wrapped errors.

---

## File structure

### Modified files

- `internal/repo/gitignore.go` — fix anchored dir patterns (F-BUG-98); add `!negation` (F-BUG-99); degrade gracefully on parse errors (F-POL-131)
- `internal/repo/gitignore_test.go` — new test cases for the two parser fixes
- `internal/repo/scanner.go` — wire `Config.Ignore` and `Config.SkipGitignore` (F-BUG-97)
- `internal/repo/symbols.go` — `ExtractSymbols` takes `ctx context.Context` (F-BUG-110)
- `internal/repo/language.go` — extend the extension map (F-POL-134)
- `internal/repo/map.go` — document `maxFiles` semantics (F-POL-129)
- `internal/tools/native/repo_index.go` — pass `Ignore` / `SkipGitignore` to the scanner; pass `ctx` to `ExtractSymbols`; use the in-memory scan results (F-BUG-111)
- `internal/db/files.go` — `FilesMatchingBasename` uses `escapeLike` (F-BUG-101) and anchors on the path separator (F-POL-132)
- `internal/db/symbols.go` — `FindSymbols` uses `escapeLike` (F-BUG-100)
- `internal/db/files_test.go`, `internal/db/symbols_test.go` — new tests for `escapeLike` and the basename anchor

### New files

None. The gitignore negation logic and the file-content capture can both live in the existing files.

---

# Task 1: F-BUG-100 / F-BUG-101 — Escape `LIKE` wildcards in `FindSymbols` and `FilesMatchingBasename`

**Files:**
- Modify: `internal/db/symbols.go:97-99` (the `FindSymbols` name filter)
- Modify: `internal/db/files.go:127-140` (the `FilesMatchingBasename` query)
- New: `internal/db/sqlutil/like.go` (if E1 has not yet merged, this task ports the helper locally and the post-E1 step removes the duplicate)

> If E1 has already merged, the helper is at `internal/db/sqlutil/like.go` and this task is a one-line import. If E1 has not merged, copy the helper from the E1 plan into a new local `internal/db/sqlutil/like.go` (the E1 task creates the same file with the same content; whichever ships first wins).

**Interfaces:**
- Produces: `func escapeLike(s string) string` — escapes `%`, `_`, and `\` for SQL `LIKE`
- Produces: `LIKE ? ESCAPE '\'` appended to the relevant queries

- [ ] **Step 1: Confirm `escapeLike` exists**

```bash
ls internal/db/sqlutil/like.go 2>/dev/null || echo NEEDS_PORT
```
If `NEEDS_PORT`, copy the helper definition from the E1 plan into a new `internal/db/sqlutil/like.go` and `internal/db/sqlutil/like_test.go`. If it already exists, continue.

- [ ] **Step 2: Add a unit test for the basename anchor**

The `FilesMatchingBasename` query currently uses `path LIKE %basename%`, which matches any substring. The audit (F-POL-132) notes that the docstring says "basename" but the query is a substring match. This task fixes both:
- Escape the user input (`escapeLike`)
- Anchor on the path separator so `main` matches `src/main.go` and `cmd/main.go` but **not** `cmd/remaint.go`

In `internal/db/files.go`, replace the `LIKE` predicate with:
```go
pattern := "%/" + escapeLike(basename)
...
WHERE project_id = ? AND path LIKE ? ESCAPE '\'
```
with the second `?` bound to `pattern`. Update the ordering's `substr(path, length(path) - length(?) + 1) = ?` predicate to compare against `basename` (not the pattern) so the exact-suffix check still works.

- [ ] **Step 3: Add a unit test for the wildcard escape**

In `internal/db/files_test.go`, add a row whose path contains an underscore (`test_helper.go`) and search for `_helper.go`; with the fix, the path matches; without the fix, the path also matches `Xhelper.go` and the assertion fails.

In `internal/db/symbols_test.go`, add a `Symbol` whose name is `foo_bar` and a second whose name is `fooXbar`. Search for `foo_bar`; with the fix, only the first matches; without the fix, both match.

- [ ] **Step 4: Build & test**

```bash
go test ./internal/db/ -run 'FilesMatchingBasename|FindSymbols'
```

- [ ] **Step 5: Commit**

```bash
git add internal/db/files.go internal/db/symbols.go internal/db/files_test.go internal/db/symbols_test.go internal/db/sqlutil/
git commit -m "db: escape LIKE wildcards in FindSymbols and FilesMatchingBasename (F-BUG-100, F-BUG-101, F-POL-132)"
```

---

# Task 2: F-POL-134 — Expand `DetectLanguage` extension map

**Files:**
- Modify: `internal/repo/language.go:29-38`
- Modify: `internal/repo/language_test.go`

- [ ] **Step 1: Add a representative set of missing extensions**

Append to `extensionLanguages`:
```go
"cs": "csharp", "fs": "fsharp", "vb": "visualbasic",
"vue": "vue", "svelte": "svelte", "astro": "astro",
"scala": "scala", "sbt": "scala",
"clj": "clojure", "cljs": "clojurescript", "cljc": "clojure",
"ex": "elixir", "exs": "elixir",
"erl": "erlang", "hrl": "erlang",
"hs": "haskell", "lhs": "haskell",
"swift": "swift", "m": "objc", "mm": "objcpp",
"dart": "dart", "lua": "lua", "r": "r", "jl": "julia",
"nim": "nim", "zig": "zig", "v": "v",
"pl": "perl", "pm": "perl", "t": "perl",
"groovy": "groovy", "gradle": "groovy",
"asm": "assembly", "s": "assembly",
"proto": "protobuf",
```

> Rationale: covers the most common languages from a 2025 GitHub Octoverse top-30 plus the explicit list in the audit (`.vue`, `.svelte`, `.scala`, `.clj`, `.ex`, `.exs`, `.erl`, `.hrl`, `.cs`, `.fs`, `.lhs`).

- [ ] **Step 2: Add table-driven tests**

```go
func TestDetectLanguage(t *testing.T) {
    cases := []struct{ path, want string }{
        {"main.go", "go"},
        {"App.vue", "vue"},
        {"Component.svelte", "svelte"},
        {"script.exs", "elixir"},
        {"module.erl", "erlang"},
        {"test.lhs", "haskell"},
        {"foo.unknown", ""},
    }
    for _, c := range cases {
        if got := DetectLanguage(c.path); got != c.want {
            t.Errorf("DetectLanguage(%q) = %q, want %q", c.path, got, c.want)
        }
    }
}
```

- [ ] **Step 3: Build & test**

```bash
go test ./internal/repo/ -run Language
```

- [ ] **Step 4: Commit**

```bash
git add internal/repo/language.go internal/repo/language_test.go
git commit -m "repo: expand DetectLanguage extension map (F-POL-134)"
```

---

# Task 3: F-BUG-98 — Preserve anchor for trailing-slash gitignore patterns

**Files:**
- Modify: `internal/repo/gitignore.go:52-72`
- Modify: `internal/repo/gitignore_test.go`

- [ ] **Step 1: Add a failing test**

```go
func TestGitignoreDirOnlyAnchored(t *testing.T) {
    g, err := ParseGitignore("build/\n")
    if err != nil { t.Fatalf("Parse: %v", err) }
    if g.Match("src/build/output.js", false) {
        t.Errorf("build/ must not match nested paths when unanchored")
    }
    if !g.Match("build/output.js", false) {
        t.Errorf("build/ must match top-level build/output.js")
    }
    if !g.Match("build", true) {
        t.Errorf("build/ must match the build directory itself")
    }
}
```

- [ ] **Step 2: Track `anchored` separately from `dirOnly`**

Rewrite `parseGitignorePattern` to record whether the line had a leading `/` and whether it had a trailing `/`, and use the leading-`/` decision **before** stripping the trailing `/`:
```go
func parseGitignorePattern(line string) (gitignorePattern, error) {
    p := gitignorePattern{}
    if strings.HasPrefix(line, "/") {
        p.anchored = true
        line = line[1:]
    }
    dirOnly := strings.HasSuffix(line, "/")
    if dirOnly {
        line = strings.TrimSuffix(line, "/")
    }
    // A slash anywhere in the (post-strip) pattern anchors it relative
    // to the .gitignore location.
    if strings.Contains(line, "/") {
        p.anchored = true
    }
    p.dirOnly = dirOnly
    p.segments = strings.Split(line, "/")
    for _, seg := range p.segments {
        if _, err := filepath.Match(seg, ""); err != nil {
            return gitignorePattern{}, fmt.Errorf("invalid gitignore pattern %q: %w", line, err)
        }
    }
    return p, nil
}
```

- [ ] **Step 3: Update `Match` for the new `dirOnly` semantics**

The `Match` function already iterates prefixes when `dirOnly` is true; with the fix above it will not re-anchor at every prefix because the `anchored` flag is now correctly set. The current `Match` body is correct — no change needed beyond the parser fix.

- [ ] **Step 4: Build & test**

```bash
go test ./internal/repo/ -run Gitignore
```

- [ ] **Step 5: Commit**

```bash
git add internal/repo/gitignore.go internal/repo/gitignore_test.go
git commit -m "repo: preserve anchor for trailing-slash gitignore patterns (F-BUG-98)"
```

---

# Task 4: F-BUG-99 — Support `!negation` in gitignore

**Files:**
- Modify: `internal/repo/gitignore.go` (parser + `Match`)
- Modify: `internal/repo/gitignore_test.go`

- [ ] **Step 1: Add a failing test**

```go
func TestGitignoreNegation(t *testing.T) {
    g, err := ParseGitignore("*.log\n!important.log\n")
    if err != nil { t.Fatalf("Parse: %v", err) }
    if !g.Match("debug.log", false) {
        t.Errorf("*.log must match debug.log")
    }
    if g.Match("important.log", false) {
        t.Errorf("!important.log must un-ignore important.log")
    }
    // A path matched only by the negative pattern (not by a preceding
    // positive one) must NOT be ignored — git's behaviour.
    g2, _ := ParseGitignore("!foo\n")
    if g2.Match("foo", false) {
        t.Errorf("!foo alone must not match anything (no positive match to override)")
    }
}
```

- [ ] **Step 2: Add the `negate` field to `gitignorePattern` and parse `!`**

```go
type gitignorePattern struct {
    anchored bool
    dirOnly  bool
    negate   bool
    segments []string
}

func parseGitignorePattern(line string) (gitignorePattern, error) {
    p := gitignorePattern{}
    if strings.HasPrefix(line, "!") {
        p.negate = true
        line = line[1:]
    }
    if strings.HasPrefix(line, "/") {
        p.anchored = true
        line = line[1:]
    }
    ...
    return p, nil
}
```

- [ ] **Step 3: Implement last-pattern-wins semantics in `Match`**

A path is ignored only if the **last** pattern that matches it is **not** a negation. Iterate the pattern slice in order, and for each pattern that matches the path, set `ignored = !p.negate`. After the loop, return `ignored`.

Replace the `Match` body with:
```go
func (g *Gitignore) Match(path string, isDir bool) bool {
    path = filepath.ToSlash(path)
    ignored := false
    for _, p := range g.patterns {
        if p.dirOnly {
            pathSegments := strings.Split(path, "/")
            end := len(pathSegments)
            if !isDir {
                end = len(pathSegments) - 1
            }
            matched := false
            for i := 1; i <= end; i++ {
                prefix := strings.Join(pathSegments[:i], "/")
                if matchPattern(p, prefix) {
                    matched = true
                    break
                }
            }
            if matched {
                ignored = !p.negate
            }
            continue
        }
        if matchPattern(p, path) {
            ignored = !p.negate
        }
    }
    return ignored
}
```

- [ ] **Step 4: Build & test**

```bash
go test ./internal/repo/ -run Gitignore
```

- [ ] **Step 5: Commit**

```bash
git add internal/repo/gitignore.go internal/repo/gitignore_test.go
git commit -m "repo: support !negation in gitignore (F-BUG-99)"
```

---

# Task 5: F-POL-131 — Degrade gracefully on gitignore parse errors

**Files:**
- Modify: `internal/repo/scanner.go:55-74` (`NewScanner`)

- [ ] **Step 1: Add a test**

```go
func TestNewScannerContinuesOnBadGitignore(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("[\n"), 0o644); err != nil {
        t.Fatalf("write: %v", err)
    }
    s := NewScanner(Config{Root: dir})
    if s.loadErr == nil {
        t.Fatal("expected loadErr to be set for malformed gitignore")
    }
    if s.gitignore != nil {
        t.Fatal("expected gitignore to be nil when parse fails")
    }
    // Scan must still succeed (the gitignore rules are simply not applied).
    if _, err := s.Scan(); err != nil {
        t.Errorf("Scan should succeed when gitignore fails to parse: %v", err)
    }
}
```

- [ ] **Step 2: Confirm current behaviour matches the expectation**

`NewScanner` already sets `s.loadErr = err` and leaves `s.gitignore` nil on parse failure. The `Scan` method already returns the error early. The fix is to **silence** the load error during `Scan` and treat the absence of gitignore rules as "no rules" — the user opted to provide a `.gitignore` and a typo shouldn't block indexing.

In `internal/repo/scanner.go`:
- Change `Scan()` so it does **not** short-circuit on `s.loadErr`. Add a `warnings []string` field to `Scanner` and append `"gitignore: <error>"` to it.
- Expose a new `Warnings() []string` method (alongside the existing `Skipped()`).

```go
type Scanner struct {
    config    Config
    gitignore *Gitignore
    loadErr   error
    skipped   []skippedEntry
    warnings  []string
}

func (s *Scanner) Scan() ([]db.FileIndex, error) {
    root := s.config.Root
    if s.loadErr != nil {
        s.warnings = append(s.warnings, "gitignore: "+s.loadErr.Error())
    }
    ...
}
```

- [ ] **Step 3: Build & test**

```bash
go test ./internal/repo/ -run Scanner
```

- [ ] **Step 4: Commit**

```bash
git add internal/repo/scanner.go internal/repo/scanner_test.go
git commit -m "repo: surface gitignore parse error as warning, not fatal (F-POL-131)"
```

---

# Task 6: F-BUG-97 — Wire configured `Indexing.Ignore` and `SkipGitignore` into `repo.index`

**Files:**
- Modify: `internal/tools/native/repo_index.go:31`
- Modify: `internal/tools/native/native.go:87-111` (add `config config.Config` to `toolSet`)
- Modify: `internal/tools/native/native.go:159-180` (assign `tools.config = opts.Config` in `newToolSet`)

- [ ] **Step 1: Add the field**

```go
type toolSet struct {
    ...
    config config.Config
    ...
}
```

In `newToolSet`, after the existing assignments, add `tools.config = opts.Config`. The `config.Config` import is already present.

- [ ] **Step 2: Pass the flags to the scanner**

Replace the scanner construction in `repo_index.go`:
```go
scanner := repo.NewScanner(repo.Config{
    Root:          t.root,
    Ignore:        t.config.Indexing.Ignore,
    SkipGitignore: false, // honour the user's .gitignore by default
})
```

> The audit suggests passing `SkipGitignore` from config; since no such flag exists in `IndexingConfig`, leave the field at the default (`false`) — honour `.gitignore` is the safer default. Document the choice in the doc comment of `repoIndexTool`.

- [ ] **Step 3: Add a test**

In `internal/tools/native/repo_index_test.go` (or create it if missing), write a test that:
- Creates a temp directory with one `.go` file in `src/keepme.go` and one in `vendor/secret.go`.
- Configures `Config{Indexing: IndexingConfig{Ignore: []string{"vendor/**"}}}`.
- Calls the tool handler.
- Asserts that the file index contains `keepme.go` but not `secret.go`.

- [ ] **Step 4: Build & test**

```bash
go test ./internal/tools/native/ -run RepoIndex
```

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/repo_index.go internal/tools/native/native.go internal/tools/native/repo_index_test.go
git commit -m "native: repo.index honours Indexing.Ignore config (F-BUG-97)"
```

---

# Task 7: F-BUG-110 — Thread `context.Context` through `ExtractSymbols`

**Files:**
- Modify: `internal/repo/symbols.go:18` — change `ExtractSymbols(path string, source []byte)` to `ExtractSymbols(ctx context.Context, path string, source []byte)`
- Modify: `internal/tools/native/repo_index.go:59` — pass the tool's `ctx` to `ExtractSymbols`
- Modify: every other caller (search the codebase for `repo.ExtractSymbols`)

- [ ] **Step 1: Update the function signature and use the context**

```go
func ExtractSymbols(ctx context.Context, path string, source []byte) ([]db.Symbol, error) {
    parser := sitter.NewParser()
    defer parser.Close()
    parser.SetLanguage(golang.GetLanguage())

    tree, err := parser.ParseCtx(ctx, nil, source)
    if err != nil {
        return nil, fmt.Errorf("parse %s: %w", path, err)
    }
    ...
}
```

- [ ] **Step 2: Update callers**

```bash
grep -rn 'ExtractSymbols' internal/
```
Pass `ctx` through every call site. The only production caller is `repo_index.go:59`; tests may need updating too.

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./internal/repo/ ./internal/tools/native/
```

- [ ] **Step 4: Commit**

```bash
git add internal/repo/symbols.go internal/tools/native/repo_index.go
git commit -m "repo: ExtractSymbols takes context.Context (F-BUG-110)"
```

---

# Task 8: F-BUG-111 — Stop re-reading files from disk in `repo.index`

**Files:**
- Modify: `internal/repo/scanner.go` — capture the file contents (or at least the on-disk path + size) during `Scan()` and expose them through a new `IndexedFile` type or by extending `db.FileIndex` with a temporary `content []byte` field
- Modify: `internal/tools/native/repo_index.go` — use the already-read contents instead of `os.ReadFile`

- [ ] **Step 1: Add an internal scanner result type**

Create `internal/repo/scanned.go`:
```go
package repo

// ScannedFile is the per-file result returned by Scanner.ScanDetailed.
// The Content slice is the bytes that were hashed; downstream consumers
// (e.g. repo.index) use it for symbol extraction without re-reading the
// file from disk.
type ScannedFile struct {
    db.FileIndex
    Content []byte
}
```

- [ ] **Step 2: Add a `ScanDetailed` method that captures content**

Refactor `Scan` to share a private walk function. Add:
```go
func (s *Scanner) ScanDetailed() ([]ScannedFile, error) {
    var out []ScannedFile
    err := s.walk(func(path, rel string) (db.FileIndex, []byte, error) {
        content, err := os.ReadFile(path)
        if err != nil {
            return db.FileIndex{}, nil, err
        }
        hash, size, err := hashBytes(content)
        if err != nil {
            return db.FileIndex{}, nil, err
        }
        return db.FileIndex{
            Path:      rel,
            Language:  DetectLanguage(rel),
            Hash:      hash,
            SizeBytes: size,
        }, content, nil
    })
    if err != nil { return nil, err }
    return out, nil
}
```
Keep `Scan` as a thin wrapper that drops the `Content` field.

- [ ] **Step 3: Use `ScanDetailed` in `repo_index.go`**

Replace:
```go
files, err := scanner.Scan()
...
content, readErr := os.ReadFile(filepath.Join(t.root, f.Path))
```
with:
```go
scanned, err := scanner.ScanDetailed()
...
files := make([]db.FileIndex, 0, len(scanned))
var symbols []db.Symbol
var warnings []string
for _, sf := range scanned {
    files = append(files, sf.FileIndex)
    if sf.FileIndex.Language != "go" {
        continue
    }
    fileSymbols, extractErr := repo.ExtractSymbols(ctx, sf.FileIndex.Path, sf.Content)
    ...
}
```

- [ ] **Step 4: Build & test**

```bash
go test ./internal/tools/native/ -run RepoIndex
go test ./internal/repo/
```

- [ ] **Step 5: Commit**

```bash
git add internal/repo/scanner.go internal/repo/scanned.go internal/tools/native/repo_index.go
git commit -m "native: repo.index uses scan-captured file contents (F-BUG-111)"
```

---

# Task 9: F-POL-129 — Document `RenderDirectoryMap` `maxFiles` semantics

**Files:**
- Modify: `internal/repo/map.go:14-41` (doc comment + inline comment in `renderNode`)

- [ ] **Step 1: Tighten the doc comment**

```go
// RenderDirectoryMap renders a simple indented directory tree from a file
// index.
//
// The cap is applied per file entry: at most maxFiles file rows are
// printed. Directory entries are NOT counted against the cap and are
// always printed. Symbols are inlined for the printed files only; the
// symbol table is otherwise unchanged. Unexported symbols and imports
// are omitted here to keep the map compact, but remain fully queryable
// via the symbols.find tool.
```

- [ ] **Step 2: Add a comment in `renderNode`**

```go
// fileCount counts every file (including the ones we skip because we
// hit maxFiles), so that the truncation note at the bottom is exact.
// Directories are intentionally not counted.
```

- [ ] **Step 3: Build & test**

```bash
go test ./internal/repo/ -run Map
```

- [ ] **Step 4: Commit**

```bash
git add internal/repo/map.go
git commit -m "repo: document RenderDirectoryMap maxFiles semantics (F-POL-129)"
```

---

# Task 10: F-POL-133 — Decide and document the `db` ↔ `repo` coupling

**Files:**
- Modify: `docs/05-context-and-project-knowledge.md` (or a new `docs/15-data-model-decisions.md`) — record the decision

This task is a documentation-only decision. No code change.

- [ ] **Step 1: Open a PR description / ADR**

Add a one-page ADR to `docs/15-data-model-decisions.md` (create the file if it doesn't exist):
```markdown
# ADR-001: `db.FileIndex` and `db.Symbol` live in `internal/db/`

## Context

F-POL-133 in `docs/14-codebase-improvement-audit-2026-07-14.md` notes that
`db.FileIndex` and `db.Symbol` are defined in `internal/db/` but are
heavily used by `internal/repo/` and `internal/tools/native/`, creating
a `db → repo → db` import graph that is theoretically reversible (a
neutral `internal/repoindex/` package would break the cycle). The audit
offers two options: move the types or accept the coupling.

## Decision

We accept the coupling. The marshal schema is small (one file index,
one symbol table) and the alternative — a third package with no
behaviour and only types — adds an import alias layer without buying
any decoupling in practice. If a future need (e.g. a different
storage backend) requires the split, this ADR is the place to revisit.

## Consequences

- The `internal/repo` package will continue to import `internal/db`.
- Tests for `internal/repo` may continue to use `db` helpers.
- New DB-backed fields on `FileIndex` / `Symbol` do not require
  changes in `internal/repo`.
```

- [ ] **Step 2: Reference the ADR from the audit doc**

In `docs/14-codebase-improvement-audit-2026-07-14.md`, add a footnote
to the F-POL-133 entry: "Decision recorded in `docs/15-data-model-decisions.md` ADR-001; no code change."

- [ ] **Step 3: Commit**

```bash
git add docs/15-data-model-decisions.md docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs: ADR-001 accepts db↔repo coupling (F-POL-133)"
```

---

# Task 11: Full sweep

- [ ] **Step 1: Run the entire test suite**

```bash
go test ./...
```

- [ ] **Step 2: Update the audit doc**

Edit `docs/14-codebase-improvement-audit-2026-07-14.md` and add a new "Resolution status" subsection at the bottom of the file:

```markdown
### Batch 5 (E2 — repo scanner, gitignore, language & indexing): RESOLVED on branch `feature/domain-e2-repo-scanner`
| Finding | Status | Notes |
|---|---|---|
| F-BUG-100 | RESOLVED | FindSymbols uses escapeLike and ESCAPE '\\' |
| F-BUG-101 | RESOLVED | FilesMatchingBasename uses escapeLike and ESCAPE '\\' |
| F-POL-132 | RESOLVED | FilesMatchingBasename now anchors on path separator |
| F-POL-134 | RESOLVED | DetectLanguage extended with .vue / .svelte / .scala / etc. |
| F-BUG-98 | RESOLVED | gitignore trailing-slash patterns stay anchored |
| F-BUG-99 | RESOLVED | gitignore supports !negation with last-pattern-wins semantics |
| F-POL-131 | RESOLVED | Bad gitignore is logged as a warning; Scan continues |
| F-BUG-97 | RESOLVED | repo.index passes Indexing.Ignore to Scanner |
| F-BUG-110 | RESOLVED | ExtractSymbols takes context.Context |
| F-BUG-111 | RESOLVED | repo.index uses Scanner.ScanDetailed to avoid disk re-reads |
| F-POL-129 | RESOLVED | RenderDirectoryMap doc comment clarifies maxFiles |
| F-POL-133 | RESOLVED (ADR) | Coupling accepted; see docs/15-data-model-decisions.md |
```

- [ ] **Step 3: Commit the audit update**

```bash
git add docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs: mark domain-E2 findings resolved"
```

- [ ] **Step 4: Push / open PR (only if requested)**
