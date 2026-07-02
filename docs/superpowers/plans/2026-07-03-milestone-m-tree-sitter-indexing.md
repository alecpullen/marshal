# Milestone M: Tree-Sitter Indexing v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract Go functions, methods, types, and imports with tree-sitter, store them per project, expose them through a `symbols.find` tool, and show exported symbols inline in the repo map.

**Architecture:** `internal/repo/symbols.go` walks a tree-sitter parse tree of a Go file and returns `[]db.Symbol`. `internal/db/symbols.go` persists them in a new `symbols` table using the same delete-then-insert replace pattern as `SaveFileIndex`. `repo.index` extracts and saves symbols for every `.go` file it scans. `symbols.find` is a new read-only tool that queries the table by name/kind. `repo.map` loads symbols alongside the file index and `RenderDirectoryMap` lists each Go file's exported functions/methods/types inline.

**Tech Stack:** Go 1.26.1, `github.com/smacker/go-tree-sitter` (cgo) + its bundled `golang` grammar, `modernc.org/sqlite` (existing), standard library `database/sql`.

## Global Constraints

- Go version floor: 1.26.1 (per `go.mod`).
- New dependency: `github.com/smacker/go-tree-sitter@v0.0.0-20240827094217-dd81d9e9be82` (verified pseudo-version; wraps tree-sitter's C core via cgo). Building `marshal` from this milestone onward requires `CGO_ENABLED=1` and a C toolchain.
- Follow the existing `Save<X>`/`Get<X>` replace-on-save transaction pattern from `internal/db/files.go` for all new DB persistence code.
- Follow the existing `registry.Tool` + `decodeArgs[T]` pattern from `internal/tools/native/search.go` for the new tool.
- All new tools use `registry.RiskReadOnly` — this milestone adds no write or command capability.
- `internal/repo/card.go` (`repo.card`) is explicitly out of scope and must not change.
- New table `symbols` has no backward-compatible `ALTER TABLE` handling needs — it's created fresh via `CREATE TABLE IF NOT EXISTS`, unlike columns added to pre-existing tables.

## File Structure

- `internal/db/symbols.go` (create) — `Symbol` type, `SaveSymbols`, `GetSymbols`, `FindSymbols`.
- `internal/db/symbols_test.go` (create) — persistence and query tests.
- `internal/db/migrations.go` (modify) — add `symbols` table + index to the schema string.
- `internal/repo/symbols.go` (create) — `ExtractSymbols` and tree-sitter node-walking helpers.
- `internal/repo/symbols_test.go` (create) — extraction tests for functions, methods, types, imports, and malformed input.
- `internal/repo/map.go` (modify) — `RenderDirectoryMap` gains a `symbols []db.Symbol` parameter and renders exported symbols inline.
- `internal/repo/map_test.go` (modify) — update existing calls for the new parameter; add symbol-rendering tests.
- `internal/tools/native/repo_index.go` (modify) — extract and save symbols for every scanned `.go` file.
- `internal/tools/native/repo_index_test.go` (modify) — assert symbols are persisted.
- `internal/tools/native/repo_map.go` (modify) — load symbols and pass them to `RenderDirectoryMap`.
- `internal/tools/native/repo_map_test.go` (modify) — assert exported symbols appear in `repo.map` output.
- `internal/tools/native/symbols_find.go` (create) — the `symbols.find` tool.
- `internal/tools/native/symbols_find_test.go` (create) — tool-level filtering and error tests.
- `internal/tools/native/native.go` (modify) — register `symbols.find`.
- `internal/tools/native/native_test.go` (modify) — add `symbols.find` to the expected tool list.
- `go.mod` / `go.sum` (modify) — add the tree-sitter dependency.
- `CLAUDE.md` (modify) — document the new cgo build requirement.
- `docs/10-mvp-implementation-checklist.md` (modify) — check off Milestone M items.

---

### Task 1: `db.Symbol` type and storage

**Files:**
- Create: `internal/db/symbols.go`
- Create: `internal/db/symbols_test.go`
- Modify: `internal/db/migrations.go`

**Interfaces:**
- Produces: `db.Symbol` struct (`ID int64`, `FilePath string`, `Kind string`, `Name string`, `Receiver string`, `Signature string`, `LineStart int`, `LineEnd int`); `(*DB) SaveSymbols(projectID int64, symbols []Symbol) error`; `(*DB) GetSymbols(projectID int64) ([]Symbol, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/db/symbols_test.go`:

```go
package db

import "testing"

func TestSaveAndGetSymbols(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	symbols := []Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "NewScanner", Signature: "func NewScanner(root string) *Scanner", LineStart: 3, LineEnd: 5},
		{FilePath: "scanner.go", Kind: "method", Name: "Scan", Receiver: "*Scanner", Signature: "func (s *Scanner) Scan() ([]string, error)", LineStart: 7, LineEnd: 9},
	}
	if err := db.SaveSymbols(projectID, symbols); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	got, err := db.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(got) != len(symbols) {
		t.Fatalf("expected %d symbols, got %d", len(symbols), len(got))
	}
	if got[0].Name != "NewScanner" || got[1].Name != "Scan" {
		t.Fatalf("expected symbols ordered by line_start, got %+v", got)
	}
	if got[1].Receiver != "*Scanner" {
		t.Errorf("expected receiver *Scanner, got %q", got[1].Receiver)
	}
}

func TestSaveSymbolsReplacesExisting(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	if err := db.SaveSymbols(projectID, []Symbol{
		{FilePath: "a.go", Kind: "function", Name: "Old", Signature: "func Old()", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}
	if err := db.SaveSymbols(projectID, []Symbol{
		{FilePath: "b.go", Kind: "function", Name: "New", Signature: "func New()", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("SaveSymbols replace failed: %v", err)
	}

	got, err := db.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(got) != 1 || got[0].Name != "New" {
		t.Fatalf("expected only New symbol after replace, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/... -run 'TestSaveAndGetSymbols|TestSaveSymbolsReplacesExisting' -v`
Expected: FAIL — `db.Symbol` / `SaveSymbols` / `GetSymbols` undefined.

- [ ] **Step 3: Add the `symbols` table to the schema**

In `internal/db/migrations.go`, add after the `tool_calls` table (before the closing backtick):

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

- [ ] **Step 4: Implement the storage functions**

Create `internal/db/symbols.go`:

```go
package db

import "fmt"

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
// existing symbols for the project and inserts the provided rows. Callers
// are expected to pass the complete current symbol set for the project.
func (db *DB) SaveSymbols(projectID int64, symbols []Symbol) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin save symbols transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM symbols WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete existing symbols: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO symbols (project_id, file_path, kind, name, receiver, signature, line_start, line_end)
							 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare symbol insert: %w", err)
	}
	defer stmt.Close()

	for _, s := range symbols {
		_, err := stmt.Exec(projectID, s.FilePath, s.Kind, s.Name, s.Receiver, s.Signature, s.LineStart, s.LineEnd)
		if err != nil {
			return fmt.Errorf("insert symbol %s: %w", s.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save symbols: %w", err)
	}
	return nil
}

// GetSymbols returns all symbol rows for a project, ordered by file path
// then line start.
func (db *DB) GetSymbols(projectID int64) ([]Symbol, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, file_path, kind, name, receiver, signature, line_start, line_end
		 FROM symbols
		 WHERE project_id = ?
		 ORDER BY file_path, line_start`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	var symbols []Symbol
	for rows.Next() {
		var s Symbol
		if err := rows.Scan(&s.ID, &s.FilePath, &s.Kind, &s.Name, &s.Receiver, &s.Signature, &s.LineStart, &s.LineEnd); err != nil {
			return nil, fmt.Errorf("scan symbol row: %w", err)
		}
		symbols = append(symbols, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate symbol rows: %w", err)
	}
	return symbols, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/db/... -run 'TestSaveAndGetSymbols|TestSaveSymbolsReplacesExisting' -v`
Expected: PASS

- [ ] **Step 6: Run the full package test suite**

Run: `go test ./internal/db/...`
Expected: PASS (no regressions to existing `files`/`projects`/`sessions`/`audits` tests)

- [ ] **Step 7: Commit**

```bash
git add internal/db/symbols.go internal/db/symbols_test.go internal/db/migrations.go
git commit -m "feat(db): add symbols table and storage functions"
```

---

### Task 2: Add tree-sitter dependency and extract functions/methods

**Files:**
- Create: `internal/repo/symbols.go`
- Create: `internal/repo/symbols_test.go`
- Modify: `go.mod`, `go.sum`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: `db.Symbol` (Task 1).
- Produces: `repo.ExtractSymbols(path string, source []byte) ([]db.Symbol, error)`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/smacker/go-tree-sitter@v0.0.0-20240827094217-dd81d9e9be82`
Expected: `go.mod`/`go.sum` gain the module (it will be pruned again by `go mod tidy` until Step 3 adds real usage — do not run `go mod tidy` between this step and Step 3).

- [ ] **Step 2: Write the failing test**

Create `internal/repo/symbols_test.go`:

```go
package repo

import (
	"testing"

	"marshal/internal/db"
)

func TestExtractSymbolsFunctions(t *testing.T) {
	source := []byte(`package foo

func NewScanner(root string) *Scanner {
	return &Scanner{root: root}
}
`)
	got, err := ExtractSymbols("scanner.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{
		FilePath:  "scanner.go",
		Kind:      "function",
		Name:      "NewScanner",
		Signature: "func NewScanner(root string) *Scanner",
		LineStart: 3,
		LineEnd:   5,
	})
}

func TestExtractSymbolsMethods(t *testing.T) {
	source := []byte(`package foo

func (s *Scanner) Scan() ([]string, error) {
	return nil, nil
}

func (s Scanner) Value() int {
	return 0
}
`)
	got, err := ExtractSymbols("scanner.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{
		FilePath:  "scanner.go",
		Kind:      "method",
		Name:      "Scan",
		Receiver:  "*Scanner",
		Signature: "func (s *Scanner) Scan() ([]string, error)",
		LineStart: 3,
		LineEnd:   5,
	})
	assertHasSymbol(t, got, db.Symbol{
		FilePath:  "scanner.go",
		Kind:      "method",
		Name:      "Value",
		Receiver:  "Scanner",
		Signature: "func (s Scanner) Value() int",
		LineStart: 7,
		LineEnd:   9,
	})
}

func TestExtractSymbolsToleratesSyntaxError(t *testing.T) {
	source := []byte(`package foo

func Broken( {
	// missing closing paren above; deliberately malformed
}

func Valid() int {
	return 1
}
`)
	got, err := ExtractSymbols("broken.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "broken.go", Kind: "function", Name: "Valid"})
}

// assertHasSymbol fails the test unless got contains a symbol matching
// want's Name and Kind. Fields left at their zero value on want are not
// checked, so callers can assert only the fields relevant to a test.
func assertHasSymbol(t *testing.T, got []db.Symbol, want db.Symbol) {
	t.Helper()
	for _, s := range got {
		if s.Name != want.Name || s.Kind != want.Kind {
			continue
		}
		if want.FilePath != "" && s.FilePath != want.FilePath {
			t.Errorf("symbol %s: FilePath = %q, want %q", s.Name, s.FilePath, want.FilePath)
		}
		if s.Receiver != want.Receiver {
			t.Errorf("symbol %s: Receiver = %q, want %q", s.Name, s.Receiver, want.Receiver)
		}
		if want.Signature != "" && s.Signature != want.Signature {
			t.Errorf("symbol %s: Signature = %q, want %q", s.Name, s.Signature, want.Signature)
		}
		if want.LineStart != 0 && s.LineStart != want.LineStart {
			t.Errorf("symbol %s: LineStart = %d, want %d", s.Name, s.LineStart, want.LineStart)
		}
		if want.LineEnd != 0 && s.LineEnd != want.LineEnd {
			t.Errorf("symbol %s: LineEnd = %d, want %d", s.Name, s.LineEnd, want.LineEnd)
		}
		return
	}
	t.Fatalf("expected symbol %s (%s) not found in %+v", want.Name, want.Kind, got)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/repo/... -run TestExtractSymbols -v`
Expected: FAIL — `ExtractSymbols` undefined.

- [ ] **Step 4: Implement extraction for functions and methods**

Create `internal/repo/symbols.go`:

```go
package repo

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"marshal/internal/db"
)

// ExtractSymbols parses Go source with tree-sitter and returns the
// functions, methods, types, and imports it finds. Tree-sitter produces a
// partial tree around syntax errors, so a malformed region of the file
// does not prevent extraction of symbols from the rest of it.
func ExtractSymbols(path string, source []byte) ([]db.Symbol, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(golang.GetLanguage())

	tree, err := parser.ParseCtx(context.Background(), nil, source)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	root := tree.RootNode()
	var symbols []db.Symbol
	for i := 0; i < int(root.NamedChildCount()); i++ {
		symbols = append(symbols, extractDeclaration(path, root.NamedChild(i), source)...)
	}
	return symbols, nil
}

func extractDeclaration(path string, node *sitter.Node, source []byte) []db.Symbol {
	switch node.Type() {
	case "function_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "function", "")}
	case "method_declaration":
		return []db.Symbol{funcSymbol(path, node, source, "method", receiverType(node, source))}
	default:
		return nil
	}
}

func funcSymbol(path string, node *sitter.Node, source []byte, kind, receiver string) db.Symbol {
	name := ""
	if nameNode := node.ChildByFieldName("name"); nameNode != nil {
		name = nameNode.Content(source)
	}
	return db.Symbol{
		FilePath:  path,
		Kind:      kind,
		Name:      name,
		Receiver:  receiver,
		Signature: headerSignature(node, source),
		LineStart: int(node.StartPoint().Row) + 1,
		LineEnd:   int(node.EndPoint().Row) + 1,
	}
}

// headerSignature returns the declaration text up to (but not including)
// its body, or the full declaration text if it has no body.
func headerSignature(node *sitter.Node, source []byte) string {
	end := node.EndByte()
	if body := node.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}
	return strings.TrimSpace(string(source[node.StartByte():end]))
}

// receiverType returns a method's receiver type text (e.g. "*Scanner"), or
// "" if node has no receiver field or the receiver has no type.
func receiverType(node *sitter.Node, source []byte) string {
	receiver := node.ChildByFieldName("receiver")
	if receiver == nil || receiver.NamedChildCount() == 0 {
		return ""
	}
	param := receiver.NamedChild(0)
	typeNode := param.ChildByFieldName("type")
	if typeNode == nil {
		return ""
	}
	return typeNode.Content(source)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/repo/... -run TestExtractSymbols -v`
Expected: PASS

- [ ] **Step 6: Tidy the module and document the cgo requirement**

Run: `go mod tidy`
Expected: `github.com/smacker/go-tree-sitter` remains as a direct dependency in `go.mod` (it's now used by `internal/repo/symbols.go`).

In `CLAUDE.md`, update the Commands section:

```diff
 ## Commands
 
 ```bash
-# Build
+# Build (requires CGO_ENABLED=1 and a C toolchain — needed for the
+# tree-sitter dependency used by Go symbol extraction)
 go build ./cmd/marshal
```

- [ ] **Step 7: Run the full package test suite**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/repo/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/repo/symbols.go internal/repo/symbols_test.go go.mod go.sum CLAUDE.md
git commit -m "feat(repo): extract Go function and method symbols with tree-sitter"
```

---

### Task 3: Extract type declarations

**Files:**
- Modify: `internal/repo/symbols.go`
- Modify: `internal/repo/symbols_test.go`

**Interfaces:**
- Consumes: `extractDeclaration` switch (Task 2), `db.Symbol` (Task 1).
- Produces: `type_declaration` handling added to `extractDeclaration`; new symbols carry `Kind: "type"`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/repo/symbols_test.go`:

```go
func TestExtractSymbolsTypes(t *testing.T) {
	source := []byte(`package foo

type Scanner struct {
	root string
}

type Matcher interface {
	Match(s string) bool
}

type ID int
`)
	got, err := ExtractSymbols("types.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Scanner", Signature: "type Scanner struct", LineStart: 3, LineEnd: 5})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Matcher", Signature: "type Matcher interface", LineStart: 7, LineEnd: 9})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "ID", Signature: "type ID int", LineStart: 11, LineEnd: 11})
}

func TestExtractSymbolsGroupedTypeBlock(t *testing.T) {
	source := []byte(`package foo

type (
	Foo int
	Bar string
)
`)
	got, err := ExtractSymbols("types.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Foo", Signature: "type Foo int", LineStart: 4, LineEnd: 4})
	assertHasSymbol(t, got, db.Symbol{FilePath: "types.go", Kind: "type", Name: "Bar", Signature: "type Bar string", LineStart: 5, LineEnd: 5})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/repo/... -run 'TestExtractSymbolsTypes|TestExtractSymbolsGroupedTypeBlock' -v`
Expected: FAIL — no `type` symbols found (type_declaration falls through the `default` case).

- [ ] **Step 3: Implement type extraction**

In `internal/repo/symbols.go`, add a case to `extractDeclaration`:

```go
	case "type_declaration":
		return typeSymbols(path, node, source)
```

Add the new functions:

```go
func typeSymbols(path string, node *sitter.Node, source []byte) []db.Symbol {
	var symbols []db.Symbol
	for i := 0; i < int(node.NamedChildCount()); i++ {
		spec := node.NamedChild(i)
		if spec.Type() != "type_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		typeNode := spec.ChildByFieldName("type")
		if nameNode == nil || typeNode == nil {
			continue
		}
		name := nameNode.Content(source)
		symbols = append(symbols, db.Symbol{
			FilePath:  path,
			Kind:      "type",
			Name:      name,
			Signature: "type " + name + " " + typeKindWord(typeNode, source),
			LineStart: int(spec.StartPoint().Row) + 1,
			LineEnd:   int(spec.EndPoint().Row) + 1,
		})
	}
	return symbols
}

// typeKindWord summarizes a type_spec's type node as a short trailing word
// for its signature: the underlying type text for simple aliases (e.g.
// "int"), or the composite keyword ("struct"/"interface"/...) with its
// opening brace stripped for struct/interface/composite types.
func typeKindWord(typeNode *sitter.Node, source []byte) string {
	text := typeNode.Content(source)
	line := strings.SplitN(text, "\n", 2)[0]
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "{"))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/repo/... -run 'TestExtractSymbolsTypes|TestExtractSymbolsGroupedTypeBlock' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite**

Run: `CGO_ENABLED=1 go test ./internal/repo/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/repo/symbols.go internal/repo/symbols_test.go
git commit -m "feat(repo): extract Go type symbols with tree-sitter"
```

---

### Task 4: Extract import declarations

**Files:**
- Modify: `internal/repo/symbols.go`
- Modify: `internal/repo/symbols_test.go`

**Interfaces:**
- Consumes: `extractDeclaration` switch (Task 2/3), `db.Symbol` (Task 1).
- Produces: `import_declaration` handling added to `extractDeclaration`; new symbols carry `Kind: "import"`, with `Name` set to the unquoted import path.

- [ ] **Step 1: Write the failing tests**

Append to `internal/repo/symbols_test.go`:

```go
func TestExtractSymbolsImportsSingle(t *testing.T) {
	source := []byte(`package foo

import "fmt"
`)
	got, err := ExtractSymbols("imports.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "imports.go", Kind: "import", Name: "fmt", Signature: `"fmt"`, LineStart: 3, LineEnd: 3})
}

func TestExtractSymbolsImportsGroupedWithAlias(t *testing.T) {
	source := []byte(`package foo

import (
	"fmt"
	bar "example.com/bar"
)
`)
	got, err := ExtractSymbols("imports.go", source)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertHasSymbol(t, got, db.Symbol{FilePath: "imports.go", Kind: "import", Name: "fmt", Signature: `"fmt"`, LineStart: 4, LineEnd: 4})
	assertHasSymbol(t, got, db.Symbol{FilePath: "imports.go", Kind: "import", Name: "example.com/bar", Signature: `bar "example.com/bar"`, LineStart: 5, LineEnd: 5})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/repo/... -run 'TestExtractSymbolsImports' -v`
Expected: FAIL — no `import` symbols found.

- [ ] **Step 3: Implement import extraction**

In `internal/repo/symbols.go`, add a case to `extractDeclaration`:

```go
	case "import_declaration":
		return importSymbols(path, node, source)
```

Add the new function. Go's grammar represents a single unparenthesized
import as a direct `import_spec` child, and a parenthesized import block as
an `import_spec_list` wrapping multiple `import_spec` children — handle
both:

```go
func importSymbols(path string, node *sitter.Node, source []byte) []db.Symbol {
	var specs []*sitter.Node
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "import_spec":
			specs = append(specs, child)
		case "import_spec_list":
			for j := 0; j < int(child.NamedChildCount()); j++ {
				if sub := child.NamedChild(j); sub.Type() == "import_spec" {
					specs = append(specs, sub)
				}
			}
		}
	}

	var symbols []db.Symbol
	for _, spec := range specs {
		pathNode := spec.ChildByFieldName("path")
		if pathNode == nil {
			continue
		}
		importPath := strings.Trim(pathNode.Content(source), `"`)
		signature := pathNode.Content(source)
		if aliasNode := spec.ChildByFieldName("name"); aliasNode != nil {
			signature = aliasNode.Content(source) + " " + signature
		}
		symbols = append(symbols, db.Symbol{
			FilePath:  path,
			Kind:      "import",
			Name:      importPath,
			Signature: signature,
			LineStart: int(spec.StartPoint().Row) + 1,
			LineEnd:   int(spec.EndPoint().Row) + 1,
		})
	}
	return symbols
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/repo/... -run 'TestExtractSymbolsImports' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite**

Run: `CGO_ENABLED=1 go test ./internal/repo/...`
Expected: PASS (all extraction tests from Tasks 2-4 pass together)

- [ ] **Step 6: Commit**

```bash
git add internal/repo/symbols.go internal/repo/symbols_test.go
git commit -m "feat(repo): extract Go import symbols with tree-sitter"
```

---

### Task 5: `db.FindSymbols` query

**Files:**
- Modify: `internal/db/symbols.go`
- Modify: `internal/db/symbols_test.go`

**Interfaces:**
- Consumes: `Symbol`, `SaveSymbols`, `GetSymbols` (Task 1).
- Produces: `(*DB) FindSymbols(projectID int64, name, kind string, limit int) ([]Symbol, error)`. `limit <= 0` defaults to 50; values above 200 are clamped to 200.

- [ ] **Step 1: Write the failing tests**

Append to `internal/db/symbols_test.go`:

```go
func TestFindSymbolsFiltersByNameAndKind(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	if err := db.SaveSymbols(projectID, []Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "NewScanner", LineStart: 3, LineEnd: 5, Signature: "func NewScanner() *Scanner"},
		{FilePath: "scanner.go", Kind: "method", Name: "Scan", Receiver: "*Scanner", LineStart: 7, LineEnd: 9, Signature: "func (s *Scanner) Scan()"},
		{FilePath: "scanner.go", Kind: "type", Name: "Scanner", LineStart: 1, LineEnd: 1, Signature: "type Scanner struct"},
		{FilePath: "card.go", Kind: "function", Name: "RenderCard", LineStart: 1, LineEnd: 3, Signature: "func RenderCard() string"},
	}); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	got, err := db.FindSymbols(projectID, "scan", "", 0)
	if err != nil {
		t.Fatalf("FindSymbols failed: %v", err)
	}
	// "scan" matches NewScanner, Scan, and Scanner (all contain "scan" as a
	// case-insensitive substring); RenderCard does not.
	if len(got) != 3 {
		t.Fatalf("expected 3 name matches, got %d: %+v", len(got), got)
	}

	got, err = db.FindSymbols(projectID, "", "type", 0)
	if err != nil {
		t.Fatalf("FindSymbols kind failed: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Scanner" {
		t.Fatalf("expected 1 type match, got %+v", got)
	}

	got, err = db.FindSymbols(projectID, "", "", 0)
	if err != nil {
		t.Fatalf("FindSymbols no filter failed: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected all 4 symbols with no filter, got %d", len(got))
	}
}

func TestFindSymbolsLimitDefaultsAndClamps(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	symbols := make([]Symbol, 0, 60)
	for i := 0; i < 60; i++ {
		symbols = append(symbols, Symbol{
			FilePath: "a.go", Kind: "function", Name: "F", LineStart: i + 1, LineEnd: i + 1, Signature: "func F()",
		})
	}
	if err := db.SaveSymbols(projectID, symbols); err != nil {
		t.Fatalf("SaveSymbols failed: %v", err)
	}

	got, err := db.FindSymbols(projectID, "", "", 0)
	if err != nil {
		t.Fatalf("FindSymbols default limit failed: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("expected default limit of 50, got %d", len(got))
	}

	got, err = db.FindSymbols(projectID, "", "", 1000)
	if err != nil {
		t.Fatalf("FindSymbols clamp failed: %v", err)
	}
	if len(got) != 60 {
		t.Fatalf("expected all 60 symbols under clamp of 200, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/db/... -run TestFindSymbols -v`
Expected: FAIL — `FindSymbols` undefined.

- [ ] **Step 3: Implement `FindSymbols`**

In `internal/db/symbols.go`, add the `strings` import and the function:

```go
import (
	"fmt"
	"strings"
)
```

```go
// FindSymbols returns symbols for a project matching an optional
// case-insensitive name substring and/or exact kind, ordered by file path
// then line start. limit defaults to 50 when <= 0 and is clamped to 200.
func (db *DB) FindSymbols(projectID int64, name, kind string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := `SELECT id, file_path, kind, name, receiver, signature, line_start, line_end
			   FROM symbols
			   WHERE project_id = ?`
	args := []any{projectID}
	if name != "" {
		query += ` AND LOWER(name) LIKE ?`
		args = append(args, "%"+strings.ToLower(name)+"%")
	}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY file_path, line_start LIMIT ?`
	args = append(args, limit)

	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query symbols: %w", err)
	}
	defer rows.Close()

	var symbols []Symbol
	for rows.Next() {
		var s Symbol
		if err := rows.Scan(&s.ID, &s.FilePath, &s.Kind, &s.Name, &s.Receiver, &s.Signature, &s.LineStart, &s.LineEnd); err != nil {
			return nil, fmt.Errorf("scan symbol row: %w", err)
		}
		symbols = append(symbols, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate symbol rows: %w", err)
	}
	return symbols, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db/... -run TestFindSymbols -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./internal/db/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/db/symbols.go internal/db/symbols_test.go
git commit -m "feat(db): add FindSymbols query with name/kind filtering"
```

---

### Task 6: Wire symbol extraction into `repo.index`

**Files:**
- Modify: `internal/tools/native/repo_index.go`
- Modify: `internal/tools/native/repo_index_test.go`

**Interfaces:**
- Consumes: `repo.ExtractSymbols` (Task 2-4), `db.Symbol`, `(*DB).SaveSymbols` (Task 1).
- Produces: `repo.index`'s result `Content` gains a `Symbols: N` line; `Summary` becomes `"Indexed %d files, %d symbols"`.

- [ ] **Step 1: Write the failing test**

In `internal/tools/native/repo_index_test.go`, replace the `main.go` fixture content and add symbol assertions:

```go
func TestRepoIndexTool(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.index")
	if !ok {
		t.Fatal("repo.index not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.index failed: %v", err)
	}
	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strings.Contains(res.Summary, "1 file") {
		t.Fatalf("expected summary to contain '1 file', got %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "1 symbol") {
		t.Fatalf("expected summary to contain '1 symbol', got %q", res.Summary)
	}
	if !strings.Contains(res.Content, "go: 1") {
		t.Fatalf("expected content to contain 'go: 1', got %q", res.Content)
	}

	files, err := dbConn.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected 1 indexed main.go, got %+v", files)
	}
	if files[0].Language != "go" {
		t.Fatalf("expected Language == 'go', got %q", files[0].Language)
	}

	symbols, err := dbConn.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(symbols) != 1 || symbols[0].Name != "main" || symbols[0].Kind != "function" {
		t.Fatalf("expected 1 main function symbol, got %+v", symbols)
	}
}

func TestRepoIndexToolSkipsSymbolsForNonGoFiles(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "README.md"), []byte("# hi\n"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("repo.index")
	if _, err := tool.Handler(context.Background(), registry.ToolCall{}); err != nil {
		t.Fatalf("repo.index failed: %v", err)
	}

	symbols, err := dbConn.GetSymbols(projectID)
	if err != nil {
		t.Fatalf("GetSymbols failed: %v", err)
	}
	if len(symbols) != 0 {
		t.Fatalf("expected no symbols for non-Go files, got %+v", symbols)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/... -run TestRepoIndexTool -v`
Expected: FAIL — summary lacks "1 symbol"; `GetSymbols` returns no rows.

- [ ] **Step 3: Wire extraction into the tool**

In `internal/tools/native/repo_index.go`, add `os` and `path/filepath` to the imports:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"marshal/internal/db"
	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)
```

Replace the body of the tool's `Handler` (everything from after `SaveFileIndex` through the `return`) with:

```go
		if err := t.db.SaveFileIndex(t.projectID, files); err != nil {
			return registry.ToolResult{}, fmt.Errorf("save file index: %w", err)
		}

		var symbols []db.Symbol
		for _, f := range files {
			if f.Language != "go" {
				continue
			}
			content, readErr := os.ReadFile(filepath.Join(t.root, f.Path))
			if readErr != nil {
				// Unreadable file: keep its file-index entry, skip symbols.
				continue
			}
			fileSymbols, extractErr := repo.ExtractSymbols(f.Path, content)
			if extractErr != nil {
				// Unparseable file: keep its file-index entry, skip symbols.
				continue
			}
			symbols = append(symbols, fileSymbols...)
		}
		if err := t.db.SaveSymbols(t.projectID, symbols); err != nil {
			return registry.ToolResult{}, fmt.Errorf("save symbols: %w", err)
		}

		langCounts := map[string]int{}
		for _, f := range files {
			if f.Language != "" {
				langCounts[f.Language]++
			}
		}

		langs := make([]string, 0, len(langCounts))
		for lang := range langCounts {
			langs = append(langs, lang)
		}
		sort.Strings(langs)

		var b strings.Builder
		b.WriteString("Languages:\n")
		for _, lang := range langs {
			b.WriteString(fmt.Sprintf("  %s: %d\n", lang, langCounts[lang]))
		}
		fmt.Fprintf(&b, "\nSymbols: %d\n", len(symbols))

		return registry.ToolResult{
			Summary: fmt.Sprintf("Indexed %d files, %d symbols", len(files), len(symbols)),
			Content: b.String(),
		}, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/... -run TestRepoIndexTool -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/repo_index.go internal/tools/native/repo_index_test.go
git commit -m "feat(tools): extract and persist symbols during repo.index"
```

---

### Task 7: Add the `symbols.find` tool

**Files:**
- Create: `internal/tools/native/symbols_find.go`
- Create: `internal/tools/native/symbols_find_test.go`
- Modify: `internal/tools/native/native.go`
- Modify: `internal/tools/native/native_test.go`

**Interfaces:**
- Consumes: `(*DB).FindSymbols` (Task 5), `decodeArgs[T]` (`internal/tools/native/helpers.go`, existing).
- Produces: registered tool `"symbols.find"`, `registry.RiskReadOnly`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tools/native/symbols_find_test.go`:

```go
package native

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestSymbolsFindTool(t *testing.T) {
	tmp := t.TempDir()

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	if err := dbConn.SaveSymbols(projectID, []db.Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "NewScanner", Signature: "func NewScanner(root string) *Scanner", LineStart: 3, LineEnd: 5},
		{FilePath: "scanner.go", Kind: "method", Name: "Scan", Receiver: "*Scanner", Signature: "func (s *Scanner) Scan() ([]string, error)", LineStart: 7, LineEnd: 9},
		{FilePath: "scanner.go", Kind: "type", Name: "Scanner", Signature: "type Scanner struct", LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("symbols.find")
	if !ok {
		t.Fatal("symbols.find not found")
	}

	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"name":"scan"}`)})
	if err != nil {
		t.Fatalf("symbols.find failed: %v", err)
	}
	if !strings.Contains(res.Content, "NewScanner") || !strings.Contains(res.Content, "Scan") {
		t.Fatalf("expected NewScanner and Scan in content: %s", res.Content)
	}
	if strings.Contains(res.Content, "type Scanner") {
		t.Fatalf("expected type Scanner excluded by name filter: %s", res.Content)
	}

	res, err = tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"kind":"type"}`)})
	if err != nil {
		t.Fatalf("symbols.find kind filter failed: %v", err)
	}
	if !strings.Contains(res.Content, "type Scanner") {
		t.Fatalf("expected type Scanner in kind-filtered content: %s", res.Content)
	}

	res, err = tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("symbols.find with no filters failed: %v", err)
	}
	if !strings.Contains(res.Summary, "3 symbols") {
		t.Fatalf("expected 3 symbols summary, got %q", res.Summary)
	}
}

func TestSymbolsFindToolRejectsUnknownKind(t *testing.T) {
	tmp := t.TempDir()
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("symbols.find")
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"kind":"bogus"}`)}); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestSymbolsFindToolRequiresDB(t *testing.T) {
	tmp := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, _ := reg.Lookup("symbols.find")
	if _, err := tool.Handler(context.Background(), registry.ToolCall{}); err == nil {
		t.Fatal("expected error when DB not configured")
	}
}
```

Also update `internal/tools/native/native_test.go`'s `want` map in
`TestRegisterAllRegistersExpectedTools` to add:

```go
		"symbols.find":     registry.RiskReadOnly,
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/native/... -run 'TestSymbolsFindTool|TestRegisterAllRegistersExpectedTools' -v`
Expected: FAIL — `symbols.find` not registered.

- [ ] **Step 3: Implement the tool**

Create `internal/tools/native/symbols_find.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/tools/registry"
)

type symbolsFindArgs struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Limit int    `json:"limit"`
}

var validSymbolKinds = map[string]bool{
	"function": true, "method": true, "type": true, "import": true,
}

func (t *toolSet) symbolsFindTool() registry.Tool {
	tool := registry.Tool{
		Name:        "symbols.find",
		Description: "Find functions, methods, types, and imports in the indexed repository by name and/or kind. Run repo.index first if no index exists.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"kind":{"type":"string","enum":["function","method","type","import"]},"limit":{"type":"integer"}},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for symbols.find")
		}
		args, err := decodeArgs[symbolsFindArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Kind != "" && !validSymbolKinds[args.Kind] {
			return registry.ToolResult{}, fmt.Errorf("symbols.find kind %q is not one of function, method, type, import", args.Kind)
		}

		symbols, err := t.db.FindSymbols(t.projectID, args.Name, args.Kind, args.Limit)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("find symbols: %w", err)
		}
		if len(symbols) == 0 {
			return registry.ToolResult{
				Summary: "No matching symbols",
				Content: "Run repo.index to build the symbol index first, or adjust your filters.",
			}, nil
		}

		var b strings.Builder
		for _, s := range symbols {
			fmt.Fprintf(&b, "%s %s  %s:%d  %s\n", s.Kind, s.Name, s.FilePath, s.LineStart, s.Signature)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Found %d symbols", len(symbols)),
			Content: b.String(),
		}, nil
	}
	return tool
}
```

In `internal/tools/native/native.go`, add `tools.symbolsFindTool()` to the
slice in `RegisterAll` (after `tools.repoCardTool()`):

```go
	for _, tool := range []registry.Tool{
		tools.fileReadTool(),
		tools.fileWritePatchTool(),
		tools.repoSearchTool(),
		tools.gitStatusTool(),
		tools.gitDiffTool(),
		tools.shellRunTool(),
		tools.testRunTool(),
		tools.repoIndexTool(),
		tools.repoMapTool(),
		tools.repoCardTool(),
		tools.symbolsFindTool(),
	} {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/native/... -run 'TestSymbolsFindTool|TestRegisterAllRegistersExpectedTools' -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./internal/tools/native/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/symbols_find.go internal/tools/native/symbols_find_test.go internal/tools/native/native.go internal/tools/native/native_test.go
git commit -m "feat(tools): add symbols.find tool"
```

---

### Task 8: Show exported symbols inline in the repo map

**Files:**
- Modify: `internal/repo/map.go`
- Modify: `internal/repo/map_test.go`
- Modify: `internal/tools/native/repo_map.go`
- Modify: `internal/tools/native/repo_map_test.go`

**Interfaces:**
- Consumes: `db.Symbol`, `(*DB).GetSymbols` (Task 1).
- Produces: `repo.RenderDirectoryMap(files []db.FileIndex, symbols []db.Symbol, maxFiles int) string` (signature change — `symbols` inserted as the second parameter).

- [ ] **Step 1: Write the failing tests**

In `internal/repo/map_test.go`, update the two existing calls to pass `nil` for symbols, and add two new tests:

```go
package repo

import (
	"strings"
	"testing"

	"marshal/internal/db"
)

func TestRenderDirectoryMap(t *testing.T) {
	files := []db.FileIndex{
		{Path: "cmd/marshal/main.go", Language: "go"},
		{Path: "internal/app/app.go", Language: "go"},
		{Path: "internal/db/db.go", Language: "go"},
		{Path: "README.md", Language: "markdown"},
	}
	out := RenderDirectoryMap(files, nil, 100)
	for _, want := range []string{"cmd/", "internal/", "README.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in map:\n%s", want, out)
		}
	}
}

func TestRenderDirectoryMapTruncates(t *testing.T) {
	files := []db.FileIndex{
		{Path: "a.go", Language: "go"},
		{Path: "b.go", Language: "go"},
		{Path: "c.go", Language: "go"},
		{Path: "d.go", Language: "go"},
		{Path: "e.go", Language: "go"},
	}
	out := RenderDirectoryMap(files, nil, 2)
	if !strings.Contains(out, "... (3 more files)") {
		t.Errorf("expected truncation marker in map:\n%s", out)
	}
}

func TestRenderDirectoryMapShowsExportedSymbols(t *testing.T) {
	files := []db.FileIndex{
		{Path: "internal/repo/scanner.go", Language: "go"},
	}
	symbols := []db.Symbol{
		{FilePath: "internal/repo/scanner.go", Kind: "type", Name: "Scanner", LineStart: 1, LineEnd: 3},
		{FilePath: "internal/repo/scanner.go", Kind: "function", Name: "NewScanner", LineStart: 5, LineEnd: 7},
	}
	out := RenderDirectoryMap(files, symbols, 100)
	if !strings.Contains(out, "scanner.go (Scanner, NewScanner)") {
		t.Errorf("expected inline exported symbols in map:\n%s", out)
	}
}

func TestRenderDirectoryMapExcludesUnexportedAndImports(t *testing.T) {
	files := []db.FileIndex{
		{Path: "scanner.go", Language: "go"},
	}
	symbols := []db.Symbol{
		{FilePath: "scanner.go", Kind: "function", Name: "hashFile", LineStart: 1, LineEnd: 3},
		{FilePath: "scanner.go", Kind: "import", Name: "fmt", LineStart: 1, LineEnd: 1},
	}
	out := RenderDirectoryMap(files, symbols, 100)
	if strings.Contains(out, "(") {
		t.Errorf("expected no inline suffix for unexported/import-only file:\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/repo/... -run TestRenderDirectoryMap -v`
Expected: FAIL — `RenderDirectoryMap` doesn't accept a `symbols` parameter yet.

- [ ] **Step 3: Implement the map changes**

Replace `internal/repo/map.go` in full:

```go
package repo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"marshal/internal/db"
)

// RenderDirectoryMap renders a simple indented directory tree from a file
// index. It shows up to maxFiles file entries; if there are more, it appends
// a truncation note. Each Go file's exported top-level functions, methods,
// and types are listed inline in parens after the filename. Unexported
// symbols and imports are omitted here to keep the map compact, but remain
// fully queryable via the symbols.find tool.
func RenderDirectoryMap(files []db.FileIndex, symbols []db.Symbol, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 200
	}

	tree := &dirNode{name: ".", children: map[string]*dirNode{}}
	for _, f := range files {
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		insertPath(tree, parts, f.Path)
	}

	bySymbolFile := groupExportedSymbols(symbols)

	var b strings.Builder
	var fileCount int
	renderNode(&b, tree, "", &fileCount, maxFiles, bySymbolFile)

	if fileCount > maxFiles {
		fmt.Fprintf(&b, "\n... (%d more files)\n", fileCount-maxFiles)
	}
	return b.String()
}

type dirNode struct {
	name     string
	children map[string]*dirNode
	files    []string
}

func insertPath(node *dirNode, parts []string, fullPath string) {
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		node.files = append(node.files, fullPath)
		return
	}
	child, ok := node.children[parts[0]]
	if !ok {
		child = &dirNode{name: parts[0], children: map[string]*dirNode{}}
		node.children[parts[0]] = child
	}
	insertPath(child, parts[1:], fullPath)
}

func renderNode(b *strings.Builder, node *dirNode, prefix string, fileCount *int, maxFiles int, bySymbolFile map[string][]db.Symbol) {
	dirs := make([]string, 0, len(node.children))
	for name := range node.children {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	for _, name := range dirs {
		fmt.Fprintf(b, "%s%s/\n", prefix, name)
		renderNode(b, node.children[name], prefix+"  ", fileCount, maxFiles, bySymbolFile)
	}

	sort.Strings(node.files)
	for _, fullPath := range node.files {
		if *fileCount < maxFiles {
			fmt.Fprintf(b, "%s%s%s\n", prefix, filepath.Base(fullPath), exportedSymbolSuffix(fullPath, bySymbolFile))
		}
		*fileCount++
	}
}

// groupExportedSymbols indexes symbols by file path, keeping only the
// exported top-level functions, methods, and types useful for repo-map
// orientation. Imports and unexported symbols are excluded here; both
// remain fully queryable via symbols.find.
func groupExportedSymbols(symbols []db.Symbol) map[string][]db.Symbol {
	byFile := map[string][]db.Symbol{}
	for _, s := range symbols {
		if s.Kind == "import" || !isExportedName(s.Name) {
			continue
		}
		byFile[s.FilePath] = append(byFile[s.FilePath], s)
	}
	return byFile
}

func exportedSymbolSuffix(path string, byFile map[string][]db.Symbol) string {
	syms := byFile[path]
	if len(syms) == 0 {
		return ""
	}
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return " (" + strings.Join(names, ", ") + ")"
}

func isExportedName(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
```

- [ ] **Step 4: Update the `repo.map` tool to load and pass symbols**

In `internal/tools/native/repo_map.go`, replace the `Handler` body:

```go
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for repo.map")
		}
		files, err := t.db.GetFileIndex(t.projectID)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load file index: %w", err)
		}
		if len(files) == 0 {
			return registry.ToolResult{
				Summary: "No indexed files",
				Content: "Run repo.index to build the file index first.",
			}, nil
		}
		symbols, err := t.db.GetSymbols(t.projectID)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load symbol index: %w", err)
		}
		content := repo.RenderDirectoryMap(files, symbols, repoMapMaxFiles)
		return registry.ToolResult{
			Summary: fmt.Sprintf("Directory map with %d files", len(files)),
			Content: content,
		}, nil
	}
```

- [ ] **Step 5: Add a tool-level symbol test**

Append to `internal/tools/native/repo_map_test.go`:

```go
func TestRepoMapToolIncludesExportedSymbols(t *testing.T) {
	tmp := t.TempDir()

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	if err := dbConn.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "scanner.go", Language: "go", Hash: "abc", SizeBytes: 14, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("save file index: %v", err)
	}
	if err := dbConn.SaveSymbols(projectID, []db.Symbol{
		{FilePath: "scanner.go", Kind: "type", Name: "Scanner", LineStart: 1, LineEnd: 3},
	}); err != nil {
		t.Fatalf("save symbols: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.map")
	if !ok {
		t.Fatal("repo.map not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.map failed: %v", err)
	}
	if !strings.Contains(res.Content, "scanner.go (Scanner)") {
		t.Fatalf("expected inline exported symbol in map content: %s", res.Content)
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/repo/... ./internal/tools/native/... -v -run 'RenderDirectoryMap|RepoMapTool'`
Expected: PASS

- [ ] **Step 7: Run the full test suite**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./...`
Expected: PASS across every package

- [ ] **Step 8: Commit**

```bash
git add internal/repo/map.go internal/repo/map_test.go internal/tools/native/repo_map.go internal/tools/native/repo_map_test.go
git commit -m "feat(repo): show exported Go symbols inline in the repo map"
```

---

### Task 9: Mark Milestone M complete

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

**Interfaces:**
- None — documentation only.

- [ ] **Step 1: Check off the Milestone M items**

In `docs/10-mvp-implementation-checklist.md`, change the Milestone M section:

```diff
 ## Milestone M: Tree-sitter indexing v1
 
-- [ ] Add Tree-sitter dependency
-- [ ] Parse Go files first
-- [ ] Extract functions/types/imports
-- [ ] Store symbols
-- [ ] Add `symbols.find` tool
-- [ ] Add symbol summaries to repo map
+- [x] Add Tree-sitter dependency
+- [x] Parse Go files first
+- [x] Extract functions/types/imports
+- [x] Store symbols
+- [x] Add `symbols.find` tool
+- [x] Add symbol summaries to repo map
```

- [ ] **Step 2: Run the full test suite one final time**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./...`
Expected: PASS, no vet warnings

- [ ] **Step 3: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark Milestone M complete"
```
