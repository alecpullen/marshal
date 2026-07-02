# Milestone M: Tree-Sitter Indexing v1 Design

## Goal

Milestone M adds Go symbol extraction to Marshal's repo index using tree-sitter. Agents and users can look up functions, methods, types, and imports by name without grepping the whole repo, and the repo map shows exported symbols inline per file.

## Scope

In scope:

- Add `github.com/smacker/go-tree-sitter` (with its bundled `golang` grammar subpackage) as a dependency.
- Parse `.go` files with tree-sitter and extract functions, methods, types, and imports.
- Store extracted symbols in a new `symbols` table, keyed by project.
- Extend `repo.index` to populate the symbol table as part of its existing scan.
- Add a `symbols.find` read-only tool for name/kind lookup.
- Extend `repo.map`'s directory map rendering to list exported symbols inline per Go file.

Out of scope:

- Any language other than Go (JS/TS/Python/Rust/etc. are later milestones).
- Tree-sitter query-language (`.scm`) based extraction — v1 walks node types directly.
- Package-level `var`/`const` declarations as symbols.
- Doc comments in stored signatures.
- Changes to `repo.card`.
- Incremental/partial re-indexing — symbol extraction runs on the same full-rescan cadence as file hashing today.

## Dependency and Build Impact

`github.com/smacker/go-tree-sitter` wraps the C tree-sitter core via cgo, and its `golang` grammar subpackage vendors the C grammar sources, so no separate grammar module is needed for Go. This is a deliberate departure from the project's prior pure-Go dependency stance (e.g. `modernc.org/sqlite` was chosen over `mattn/go-sqlite3` specifically to avoid cgo). From this milestone onward, building `marshal` requires `CGO_ENABLED=1` and a working C toolchain. This should be documented in `CLAUDE.md`'s Commands section once implemented, and CI must have a C compiler available.

Tree-sitter's parser is error-tolerant: a file with a syntax error still produces a tree with `ERROR` nodes around the broken region, so extraction should skip only the unparseable nodes and still return symbols for the rest of the file rather than failing the whole file.

## Core Types

`internal/repo/symbols.go`:

```go
// ExtractSymbols parses Go source and returns the symbols it finds.
// Parse errors in part of the file do not prevent extraction of the rest.
func ExtractSymbols(path string, source []byte) ([]db.Symbol, error)
```

Walks the tree-sitter parse tree for these node kinds:

- `function_declaration` → `Symbol{Kind: "function", Name: <identifier>}`
- `method_declaration` → `Symbol{Kind: "method", Name: <identifier>, Receiver: <receiver type text>}`
- `type_declaration` (each `type_spec` within it) → `Symbol{Kind: "type", Name: <identifier>}`
- `import_declaration` (each `import_spec` within it) → `Symbol{Kind: "import", Name: <import path>}`

Each symbol records `LineStart`/`LineEnd` from the node's byte range, and `Signature` as the source text of the declaration's header line(s) (e.g. `func (s *Scanner) Scan() ([]db.FileIndex, error)` for a method, up to the opening `{`).

`internal/db/symbols.go`:

```go
type Symbol struct {
    ID        int64
    FilePath  string
    Kind      string // "function", "method", "type", "import"
    Name      string
    Receiver  string // e.g. "*Scanner"; empty for non-methods
    Signature string
    LineStart int
    LineEnd   int
}

// SaveSymbols replaces the symbol index for a project. It deletes all
// existing symbols for the project and inserts the provided rows, in the
// same style as SaveFileIndex.
func (db *DB) SaveSymbols(projectID int64, symbols []Symbol) error

// GetSymbols returns all symbol rows for a project, ordered by file path
// then line start.
func (db *DB) GetSymbols(projectID int64) ([]Symbol, error)

// FindSymbols returns symbols for a project matching an optional
// case-insensitive name substring and/or kind filter, up to limit rows.
func (db *DB) FindSymbols(projectID int64, name, kind string, limit int) ([]Symbol, error)
```

## Schema

Add to `internal/db/migrations.go`'s `schema` string:

```sql
CREATE TABLE IF NOT EXISTS symbols (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    receiver TEXT,
    signature TEXT NOT NULL,
    line_start INTEGER NOT NULL,
    line_end INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_symbols_project_name ON symbols(project_id, name);
```

This is a new table, so no backward-compatible `ALTER TABLE` handling is needed (unlike columns added to existing tables).

## Data Flow

1. `repo.index` (`internal/tools/native/repo_index.go`) scans the workspace as it does today, producing `[]db.FileIndex`.
2. For each file where `Language == "go"`, the tool reads the file content, calls `repo.ExtractSymbols`, and appends the results to a combined `[]db.Symbol` slice. Read/parse errors on an individual file are collected as warnings and do not fail the overall index run.
3. After `SaveFileIndex`, the tool calls `db.SaveSymbols(projectID, symbols)` in the same handler invocation (not the same DB transaction as the file save — each is its own transaction, matching the existing pattern).
4. The tool's result summary is extended to mention symbol count alongside file/language counts.
5. `symbols.find` (`internal/tools/native/symbols_find.go`) calls `db.FindSymbols` directly against the persisted table; it does not re-parse anything.
6. `repo.map`'s handler (`internal/tools/native/repo_map.go`) loads both `db.GetFileIndex` and `db.GetSymbols`, and passes both to `repo.RenderDirectoryMap`.

## `symbols.find` Tool

```json
{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Case-insensitive substring match on symbol name"},
    "kind": {"type": "string", "enum": ["function", "method", "type", "import"]},
    "limit": {"type": "integer", "minimum": 1, "maximum": 200}
  },
  "required": []
}
```

- Default `limit` is 50 if unset or `<= 0`; values above 200 are clamped to 200.
- With no filters at all, returns the first `limit` symbols ordered by file path then line — this is a deliberate escape hatch for "show me anything," not an error case.
- Risk level: `RiskReadOnly`, same as `repo.map`/`repo.card`.
- Errors when `DB`/`ProjectID` are unconfigured, matching the existing repo tools' error message style (`"database not configured for symbols.find"`).
- Result content renders each match as one line: `kind name  file:line  signature`.

## Repo Map Integration

`RenderDirectoryMap`'s signature changes to:

```go
func RenderDirectoryMap(files []db.FileIndex, symbols []db.Symbol, maxFiles int) string
```

For each rendered `.go` file entry, the renderer looks up that file's symbols, filters to `Kind` in `{"function", "method", "type"}` where `Name` starts with an uppercase letter (exported), and appends them in parens after the filename, e.g.:

```
scanner.go (Scanner, NewScanner, Scan)
```

Imports are never shown in the map (they're rarely useful for orientation and would add noise). Files with no exported symbols (or non-Go files) render exactly as they do today, with no parens. This keeps the map's token footprint close to today's baseline while surfacing the most useful information for repo orientation. All extracted symbols — exported and unexported — remain fully queryable via `symbols.find` regardless of what the map shows.

## Error Handling

- Tree-sitter parse errors on an individual file: extraction returns whatever symbols it could find plus a non-fatal warning; `repo.index` continues with the rest of the scan.
- Unreadable file content during symbol extraction (e.g. permissions): same treatment — skip that file's symbols, keep its `FileIndex` entry, continue.
- `symbols.find`/`repo.map` with no DB/project configured: explicit error, same message style as existing repo tools.
- No new error types needed beyond what `repo.index` already surfaces in its result content for partial failures.

## Testing

- `internal/repo/symbols_test.go`: extraction of functions, methods (with receiver text), types, imports; a file with a deliberate syntax error still yields symbols for its valid portions.
- `internal/db/symbols_test.go`: `SaveSymbols`/`GetSymbols` roundtrip; full replace on re-save; `FindSymbols` name substring, kind filter, and limit behavior (including default and clamped limits).
- `internal/tools/native/repo_index_test.go`: extend existing tests so a Go fixture file results in stored symbols; a non-Go fixture results in no symbols; the tool result summary mentions a symbol count.
- `internal/tools/native/symbols_find_test.go` (new): filtering by name/kind, limit clamping, unconfigured-DB error, empty-filter behavior.
- `internal/repo/map_test.go`: exported symbols rendered inline for Go files; unexported symbols excluded; non-Go files unaffected; existing `maxFiles` truncation behavior still holds with the new parameter.
- `internal/tools/native/native_test.go`: update expected registered tool count to include `symbols.find`.

## Acceptance Criteria

- `go build ./cmd/marshal` succeeds with `CGO_ENABLED=1` and a C toolchain present.
- `go test ./...` passes.
- Milestone M checklist (`docs/10-mvp-implementation-checklist.md`) is fully checked.
- `repo.index` extracts and stores symbols for all `.go` files scanned.
- `symbols.find` returns correct results filtered by name and/or kind.
- `repo.map` shows exported symbols inline per Go file without ballooning token footprint for large repos.
- A file with a Go syntax error does not abort the overall `repo.index` run.
