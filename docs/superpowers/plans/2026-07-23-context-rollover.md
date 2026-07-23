# Context Rollover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a long-running Marshal session periodically archive its model-facing conversation window and continue from a short resume digest, so context stays bounded without silently destroying history.

**Architecture:** A new `internal/rollover` package owns the generation lifecycle: policy evaluation, token counting, digest production, and archival. `internal/db` gains a content-addressed blob table, a generation/turn archive, and an FTS5 index. The agent runner calls the controller at two safe boundaries — the end of `RunTask` (cross-turn) and the existing context-overflow point inside the tool loop (intra-turn) — replacing today's `summarizeAndContinue`, which digests but discards. Everything is inert unless `[session.rollover] enabled = true`.

**Tech Stack:** Go, `modernc.org/sqlite` v1.53.0 (FTS5 verified working by probe), Bubble Tea TUI, existing `internal/llm/schema` message types.

## Spec reconciliation (read this before starting)

The source spec (`docs/superpowers/specs/marshal-context-rollover-spec.md`) makes four assumptions that do not hold in this codebase. This plan deliberately deviates. Do not "correct" it back toward the spec.

1. **There is no existing content-addressed store.** Task 1 builds one (`content_blobs` in SQLite).
2. **There is no existing FTS5 machinery.** Code search is tree-sitter + `LIKE`. Task 3 builds the FTS5 index from scratch.
3. **Per-backend `BackendCounter` adapters are unnecessary.** Every backend — including Ollama, whose template targets `http://localhost:11434/v1` (`internal/llm/provider/templates.go:18`) — goes through the OpenAI-compatible client, which already normalizes `usage.prompt_tokens` (`internal/llm/provider/openai_compatible_wire.go:26`). One `UsageCounter` reading that normalized value replaces the spec's adapter-per-backend design (Task 5).
4. **Intra-turn compaction already exists, and it is where the real context pressure lives.** `summarizeAndContinue` (`internal/agent/handoff.go:39`, fired at `internal/agent/runner.go:494`) already does LLM-digest-and-restart, but discards the outgoing transcript. The spec lists this as non-goal #2; this plan instead unifies it under rollover (Task 13). Tool results never reach `session.State.Messages()` — they exist only in the in-turn wire slice — so archiving that wire slice is the only way the blob store and `recall_history` get meaningful content to index.

Timestamps in this schema are **TEXT RFC3339**, not the spec's `INTEGER`, matching every existing table.

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter dependency).
- `go build ./cmd/marshal`, `go test ./...`, `gofmt -w .`, and `go vet ./...` must pass before every commit.
- Default config must leave existing behaviour byte-for-byte unchanged: `[session.rollover] enabled = false`.
- New **tables** go in the single idempotent `schema` const in `internal/db/migrations.go`. New **columns on existing tables** would additionally need a `columnAdd` entry in `migrationColumns` (`internal/db/db.go:47`) — this plan adds only new tables, so no `columnAdd` entries are required.
- The TUI renders only — no routing, policy, or prompt logic in `internal/app/tui/`.
- `internal/rollover` must **not** import `internal/agent` (agent imports rollover). Inject behaviour via function types.
- Timestamps persist as `t.UTC().Format(time.RFC3339)`.
- Every task ends with `gofmt -w .` and a commit.

## File Structure

**New:**
- `internal/db/blobs.go` — content-addressed blob put/get.
- `internal/db/generations.go` — generation rows, turn archive, reconciliation.
- `internal/db/generations_search.go` — FTS5 index maintenance and search.
- `internal/rollover/types.go` — `Generation`, `GenerationHandle`, `Signal`, `Policy`, `Store`.
- `internal/rollover/counter.go` — `TokenCounter`, `EstimatorCounter`, `UsageCounter`, `ResolveCounter`.
- `internal/rollover/policy.go` — pure `Decide` function.
- `internal/rollover/digest.go` — `DigestProvider`, `LLMSummaryProvider`, `MinimalDigest`, `SummaryDirective`.
- `internal/rollover/controller.go` — `Controller`, the seam the runner calls.
- `internal/history/render.go` — shared rendering for CLI and slash command.
- `internal/tools/native/recall.go` — `recall_history` tool.
- `internal/commands/history.go` — `/history` slash command.
- `cmd/marshal/history.go` — `marshal history` subcommand.

**Modified:**
- `internal/db/migrations.go` — three new tables + one FTS5 virtual table.
- `internal/app/config/types.go`, `defaults.go` — `[session.rollover]`.
- `internal/app/session/session.go` — generation boundary state.
- `internal/agent/history.go` — generation-scoped replay.
- `internal/agent/runner.go` — archive + rollover at both boundaries.
- `internal/agent/handoff.go` — `summarizeAndContinue` delegates to the controller.
- `internal/tools/native/native.go` — conditional `recall_history` registration.
- `internal/commands/commands.go` — register `/history`.
- `cmd/marshal/main.go` — subcommand dispatch.
- `internal/app/app.go` — controller wiring.

## Task order

Tasks 1–4 are storage and config foundations with no behaviour change. Tasks 5–9 build the rollover package in isolation. Tasks 10–13 integrate it into the agent. Tasks 14–17 expose the archive. Each task is independently testable and independently rejectable.

---

## Task 1: Content-addressed blob store

**Files:**
- Create: `internal/db/blobs.go`
- Create: `internal/db/blobs_test.go`
- Modify: `internal/db/migrations.go` (add `content_blobs`)

**Interfaces:**
- Consumes: nothing.
- Produces: `func (db *DB) PutBlob(content string, at time.Time) (hash string, err error)`, `func (db *DB) GetBlob(hash string) (string, error)`, `var ErrBlobNotFound`, and the test helper `newTestDB(t *testing.T) *DB`. Tasks 2 and 3 use all of these.

- [ ] **Step 1: Write the failing test**

Create `internal/db/blobs_test.go`:

```go
package db

import (
	"strings"
	"testing"
	"time"
)

// newTestDB opens a migrated in-memory database. Shared by every test file
// in this package that needs the rollover tables.
func newTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	return database
}

func TestPutBlobIsContentAddressedAndDeduplicates(t *testing.T) {
	database := newTestDB(t)
	now := time.Now().UTC()

	content := strings.Repeat("tool output ", 500)
	hash1, err := database.PutBlob(content, now)
	if err != nil {
		t.Fatalf("PutBlob failed: %v", err)
	}
	if len(hash1) != 64 {
		t.Fatalf("hash = %q (%d chars), want 64 hex chars of sha256", hash1, len(hash1))
	}

	hash2, err := database.PutBlob(content, now)
	if err != nil {
		t.Fatalf("second PutBlob failed: %v", err)
	}
	if hash1 != hash2 {
		t.Fatalf("identical content produced different hashes: %q vs %q", hash1, hash2)
	}

	var count int
	if err := database.SQLDB().QueryRow(`SELECT COUNT(*) FROM content_blobs`).Scan(&count); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("blob rows = %d, want 1 (identical content stored once)", count)
	}

	got, err := database.GetBlob(hash1)
	if err != nil {
		t.Fatalf("GetBlob failed: %v", err)
	}
	if got != content {
		t.Fatalf("round-trip mismatch: got %d chars, want %d", len(got), len(content))
	}
}

func TestGetBlobMissingHashReturnsErrBlobNotFound(t *testing.T) {
	database := newTestDB(t)
	_, err := database.GetBlob("deadbeef")
	if err == nil {
		t.Fatal("GetBlob on an unknown hash returned nil error, want ErrBlobNotFound")
	}
	if !errors.Is(err, ErrBlobNotFound) {
		t.Fatalf("GetBlob error = %v, want it to wrap ErrBlobNotFound", err)
	}
}
```

Add `"errors"` to that file's import block — the second test needs it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run 'TestPutBlob|TestGetBlob' -v`
Expected: FAIL — `database.PutBlob undefined (type *DB has no field or method PutBlob)`

- [ ] **Step 3: Add the table to the schema**

In `internal/db/migrations.go`, append inside the `schema` backtick string, immediately before the closing backtick:

```sql
CREATE TABLE IF NOT EXISTS content_blobs (
    hash       TEXT PRIMARY KEY,
    content    TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
```

- [ ] **Step 4: Write the implementation**

Create `internal/db/blobs.go`:

```go
package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrBlobNotFound is returned by GetBlob when the hash is not in the store.
var ErrBlobNotFound = errors.New("blob not found")

// PutBlob stores content under its sha256 hash and returns that hash.
// Storing identical content twice collapses onto the first row rather than
// duplicating it, which is the point: archived tool outputs repeat heavily
// across turns, and the archive should pay for each distinct payload once.
func (db *DB) PutBlob(content string, at time.Time) (string, error) {
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	_, err := db.sqlDB.Exec(
		`INSERT INTO content_blobs (hash, content, size_bytes, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(hash) DO NOTHING`,
		hash, content, len(content), at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return "", fmt.Errorf("put blob: %w", err)
	}
	return hash, nil
}

// GetBlob resolves a content hash back to its stored content.
func (db *DB) GetBlob(hash string) (string, error) {
	var content string
	row := db.sqlDB.QueryRow(`SELECT content FROM content_blobs WHERE hash = ?`, hash)
	if err := row.Scan(&content); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: %s", ErrBlobNotFound, hash)
		}
		return "", fmt.Errorf("get blob: %w", err)
	}
	return content, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/db/ -run 'TestPutBlob|TestGetBlob' -v`
Expected: PASS, both tests

- [ ] **Step 6: Verify the package is still clean**

Run: `gofmt -w . && go vet ./internal/db/ && go test ./internal/db/`
Expected: no vet output, `ok  	marshal/internal/db`

- [ ] **Step 7: Commit**

```bash
git add internal/db/blobs.go internal/db/blobs_test.go internal/db/migrations.go
git commit -m "feat(db): add content-addressed blob store for archived turn payloads"
```

---

## Task 2: Generation and turn archive

**Files:**
- Create: `internal/db/generations.go`
- Create: `internal/db/generations_test.go`
- Modify: `internal/db/migrations.go` (add `session_generations`, `generation_turns`)

**Interfaces:**
- Consumes: `PutBlob`/`GetBlob` and `newTestDB` from Task 1.
- Produces:
  - `type Generation struct { ID, SessionID string; Seq int; StartedAt time.Time; EndedAt *time.Time; SeedDigest, DigestSource, EndReason string }`
  - `type ArchivedTurn struct { ID int64; TurnSeq int; Role, Content, ToolCalls string; CreatedAt time.Time }`
  - `func (db *DB) BeginGeneration(g Generation) error`
  - `func (db *DB) EndGeneration(generationID string, endedAt time.Time, endReason string) error`
  - `func (db *DB) ArchiveTurns(generationID string, turns []ArchivedTurn, blobThreshold int, at time.Time) error`
  - `func (db *DB) GenerationsForSession(sessionID string) ([]Generation, error)`
  - `func (db *DB) TurnsForGeneration(generationID string) ([]ArchivedTurn, error)`
  - `func (db *DB) GenerationTurnCount(generationID string) (int, error)`
  - test helper `seedSession(t *testing.T, database *DB) string`
  - unexported `nullString(string) sql.NullString`, `rowScanner` — reused by Task 3.

  Tasks 3, 8, 9, 15, 16, 17 consume these.

- [ ] **Step 1: Write the failing test**

Create `internal/db/generations_test.go`:

```go
package db

import (
	"strings"
	"testing"
	"time"
)

// seedSession creates the project and agent_sessions rows that
// session_generations has a foreign key onto.
func seedSession(t *testing.T, database *DB) string {
	t.Helper()
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	sessionID := "sess-1"
	if err := database.CreateSession(sessionID, projectID, "t", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return sessionID
}

func TestGenerationLifecycleAndTurnArchive(t *testing.T) {
	database := newTestDB(t)
	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	if err := database.BeginGeneration(Generation{
		ID: "gen-0", SessionID: sessionID, Seq: 0, StartedAt: now,
	}); err != nil {
		t.Fatalf("BeginGeneration gen-0 failed: %v", err)
	}

	small := "what does the runner do?"
	big := strings.Repeat("x", 5000)
	turns := []ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: small, CreatedAt: now},
		{TurnSeq: 1, Role: "tool", Content: big, ToolCalls: `[{"name":"file.read"}]`, CreatedAt: now},
	}
	if err := database.ArchiveTurns("gen-0", turns, 2048, now); err != nil {
		t.Fatalf("ArchiveTurns failed: %v", err)
	}

	// Under-threshold content stays inline; over-threshold content moves to
	// the blob store and leaves content NULL.
	var inline, ref any
	row := database.SQLDB().QueryRow(
		`SELECT content, content_ref FROM generation_turns WHERE generation_id = ? AND turn_seq = 0`, "gen-0")
	if err := row.Scan(&inline, &ref); err != nil {
		t.Fatalf("scan turn 0: %v", err)
	}
	if inline == nil || ref != nil {
		t.Fatalf("turn 0: content=%v content_ref=%v, want inline content and NULL ref", inline, ref)
	}
	row = database.SQLDB().QueryRow(
		`SELECT content, content_ref FROM generation_turns WHERE generation_id = ? AND turn_seq = 1`, "gen-0")
	if err := row.Scan(&inline, &ref); err != nil {
		t.Fatalf("scan turn 1: %v", err)
	}
	if inline != nil || ref == nil {
		t.Fatalf("turn 1: content=%v content_ref=%v, want NULL content and a blob ref", inline, ref)
	}

	// Reads resolve both storage paths transparently.
	got, err := database.TurnsForGeneration("gen-0")
	if err != nil {
		t.Fatalf("TurnsForGeneration failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("turn count = %d, want 2", len(got))
	}
	if got[0].Content != small {
		t.Fatalf("turn 0 content = %q, want %q", got[0].Content, small)
	}
	if got[1].Content != big {
		t.Fatalf("turn 1 content resolved to %d chars, want %d", len(got[1].Content), len(big))
	}
	if got[1].ToolCalls != `[{"name":"file.read"}]` {
		t.Fatalf("turn 1 tool_calls = %q, want the archived JSON", got[1].ToolCalls)
	}

	ended := now.Add(time.Minute)
	if err := database.EndGeneration("gen-0", ended, "rollover"); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}
	if err := database.BeginGeneration(Generation{
		ID: "gen-1", SessionID: sessionID, Seq: 1, StartedAt: ended,
		SeedDigest: "continuing from gen 0", DigestSource: "llm_summary",
	}); err != nil {
		t.Fatalf("BeginGeneration gen-1 failed: %v", err)
	}

	gens, err := database.GenerationsForSession(sessionID)
	if err != nil {
		t.Fatalf("GenerationsForSession failed: %v", err)
	}
	if len(gens) != 2 {
		t.Fatalf("generation count = %d, want 2", len(gens))
	}
	if gens[0].EndReason != "rollover" || gens[0].EndedAt == nil {
		t.Fatalf("gen 0 = %+v, want ended_at set and end_reason 'rollover'", gens[0])
	}
	if gens[1].SeedDigest != "continuing from gen 0" || gens[1].DigestSource != "llm_summary" {
		t.Fatalf("gen 1 = %+v, want seed digest and source persisted", gens[1])
	}
	if gens[1].EndedAt != nil {
		t.Fatalf("gen 1 ended_at = %v, want nil while live", gens[1].EndedAt)
	}

	count, err := database.GenerationTurnCount("gen-0")
	if err != nil {
		t.Fatalf("GenerationTurnCount failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("GenerationTurnCount = %d, want 2", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestGenerationLifecycle -v`
Expected: FAIL — `undefined: Generation`

- [ ] **Step 3: Add the tables to the schema**

In `internal/db/migrations.go`, append inside the `schema` string after `content_blobs`:

```sql
CREATE TABLE IF NOT EXISTS session_generations (
    generation_id TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    seq           INTEGER NOT NULL,
    started_at    TEXT NOT NULL,
    ended_at      TEXT,
    seed_digest   TEXT,
    digest_source TEXT,
    end_reason    TEXT,
    UNIQUE(session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_session_generations_session ON session_generations(session_id, seq);

CREATE TABLE IF NOT EXISTS generation_turns (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    generation_id TEXT NOT NULL REFERENCES session_generations(generation_id) ON DELETE CASCADE,
    turn_seq      INTEGER NOT NULL,
    role          TEXT NOT NULL,
    content       TEXT,
    content_ref   TEXT,
    tool_calls    TEXT,
    created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_generation_turns_gen ON generation_turns(generation_id, turn_seq);
```

- [ ] **Step 4: Write the implementation**

Create `internal/db/generations.go`:

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Generation is one model-facing conversation window within a logical
// session. A session is a sequence of generations; a rollover ends one and
// begins the next, seeded with SeedDigest instead of the prior transcript.
type Generation struct {
	ID           string
	SessionID    string
	Seq          int
	StartedAt    time.Time
	EndedAt      *time.Time
	SeedDigest   string
	DigestSource string
	EndReason    string
}

// ArchivedTurn is one message in a generation's archived transcript.
// Content is always the resolved text on read, whether the row stored it
// inline or behind a blob reference.
type ArchivedTurn struct {
	ID        int64
	TurnSeq   int
	Role      string
	Content   string
	ToolCalls string
	CreatedAt time.Time
}

// BeginGeneration inserts a new live generation row (ended_at NULL).
func (db *DB) BeginGeneration(g Generation) error {
	_, err := db.sqlDB.Exec(
		`INSERT INTO session_generations
		 (generation_id, session_id, seq, started_at, seed_digest, digest_source)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.SessionID, g.Seq, g.StartedAt.UTC().Format(time.RFC3339),
		nullString(g.SeedDigest), nullString(g.DigestSource),
	)
	if err != nil {
		return fmt.Errorf("begin generation: %w", err)
	}
	return nil
}

// EndGeneration closes a live generation. endReason is 'rollover',
// 'session_end', or 'error'. The ended_at IS NULL guard makes this
// idempotent: re-closing an already-closed generation is a no-op rather
// than overwriting the original reason.
func (db *DB) EndGeneration(generationID string, endedAt time.Time, endReason string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE session_generations SET ended_at = ?, end_reason = ?
		 WHERE generation_id = ? AND ended_at IS NULL`,
		endedAt.UTC().Format(time.RFC3339), endReason, generationID,
	)
	if err != nil {
		return fmt.Errorf("end generation: %w", err)
	}
	return nil
}

// ArchiveTurns appends turns to a generation's archive in one transaction.
// Content at or above blobThreshold bytes goes to the content-addressed
// store and is referenced by hash rather than duplicated inline: tool
// outputs are both the largest payloads and the most likely to repeat.
func (db *DB) ArchiveTurns(generationID string, turns []ArchivedTurn, blobThreshold int, at time.Time) error {
	if len(turns) == 0 {
		return nil
	}
	if blobThreshold <= 0 {
		blobThreshold = 2048
	}
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin archive turns: %w", err)
	}
	defer tx.Rollback()

	for _, turn := range turns {
		var inline, ref sql.NullString
		if len(turn.Content) >= blobThreshold {
			hash, perr := db.PutBlob(turn.Content, at)
			if perr != nil {
				return perr
			}
			ref = sql.NullString{String: hash, Valid: true}
		} else {
			inline = sql.NullString{String: turn.Content, Valid: true}
		}
		if _, err := tx.Exec(
			`INSERT INTO generation_turns
			 (generation_id, turn_seq, role, content, content_ref, tool_calls, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			generationID, turn.TurnSeq, turn.Role, inline, ref,
			nullString(turn.ToolCalls), turn.CreatedAt.UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("insert generation turn: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit archive turns: %w", err)
	}
	return nil
}

// GenerationsForSession returns every generation for a session, oldest first.
func (db *DB) GenerationsForSession(sessionID string) ([]Generation, error) {
	rows, err := db.sqlDB.Query(
		`SELECT generation_id, session_id, seq, started_at, ended_at, seed_digest, digest_source, end_reason
		 FROM session_generations WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query generations: %w", err)
	}
	defer rows.Close()

	var out []Generation
	for rows.Next() {
		var g Generation
		var startedAt string
		var endedAt, seed, source, reason sql.NullString
		if err := rows.Scan(&g.ID, &g.SessionID, &g.Seq, &startedAt, &endedAt, &seed, &source, &reason); err != nil {
			return nil, fmt.Errorf("scan generation: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, startedAt)
		if err != nil {
			return nil, fmt.Errorf("parse generation started_at: %w", err)
		}
		g.StartedAt = parsed.UTC()
		if endedAt.Valid {
			e, perr := time.Parse(time.RFC3339, endedAt.String)
			if perr != nil {
				return nil, fmt.Errorf("parse generation ended_at: %w", perr)
			}
			e = e.UTC()
			g.EndedAt = &e
		}
		g.SeedDigest = seed.String
		g.DigestSource = source.String
		g.EndReason = reason.String
		out = append(out, g)
	}
	return out, rows.Err()
}

// TurnsForGeneration returns a generation's archived transcript in order,
// resolving blob references so callers never see the storage split.
func (db *DB) TurnsForGeneration(generationID string) ([]ArchivedTurn, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, turn_seq, role, content, content_ref, tool_calls, created_at
		 FROM generation_turns WHERE generation_id = ? ORDER BY turn_seq, id`, generationID)
	if err != nil {
		return nil, fmt.Errorf("query generation turns: %w", err)
	}
	defer rows.Close()

	var out []ArchivedTurn
	refs := map[int64]string{}
	for rows.Next() {
		turn, serr := scanArchivedTurn(rows, refs)
		if serr != nil {
			return nil, serr
		}
		out = append(out, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Blob resolution runs only after the result set is drained: GetBlob
	// shares the single connection this query streams from.
	return db.resolveRefs(out, refs)
}

// GenerationTurnCount returns how many turns a generation archived.
func (db *DB) GenerationTurnCount(generationID string) (int, error) {
	var n int
	err := db.sqlDB.QueryRow(
		`SELECT COUNT(*) FROM generation_turns WHERE generation_id = ?`, generationID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count generation turns: %w", err)
	}
	return n, nil
}

// rowScanner is the subset of *sql.Rows / *sql.Row that scanArchivedTurn
// needs, so Task 3's search query can reuse the same scanner.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanArchivedTurn reads one row. Rows whose content lives in the blob
// store record their hash in refs, keyed by row id, for resolution after
// the result set is drained. refs is per-call state, never package state:
// two concurrent readers must not share it.
func scanArchivedTurn(rows rowScanner, refs map[int64]string) (ArchivedTurn, error) {
	var t ArchivedTurn
	var content, ref, toolCalls sql.NullString
	var createdAt string
	if err := rows.Scan(&t.ID, &t.TurnSeq, &t.Role, &content, &ref, &toolCalls, &createdAt); err != nil {
		return ArchivedTurn{}, fmt.Errorf("scan generation turn: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return ArchivedTurn{}, fmt.Errorf("parse turn created_at: %w", err)
	}
	t.CreatedAt = parsed.UTC()
	t.Content = content.String
	t.ToolCalls = toolCalls.String
	if ref.Valid {
		refs[t.ID] = ref.String
	}
	return t, nil
}

func (db *DB) resolveRefs(turns []ArchivedTurn, refs map[int64]string) ([]ArchivedTurn, error) {
	for i := range turns {
		hash, ok := refs[turns[i].ID]
		if !ok {
			continue
		}
		content, err := db.GetBlob(hash)
		if err != nil {
			return nil, fmt.Errorf("resolve turn %d content: %w", turns[i].ID, err)
		}
		turns[i].Content = content
	}
	return turns, nil
}

// nullString maps "" to SQL NULL so absent optional text round-trips as
// NULL rather than as an empty string.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/db/ -run TestGenerationLifecycle -v`
Expected: PASS

- [ ] **Step 6: Verify the package and the race detector are clean**

Run: `gofmt -w . && go vet ./internal/db/ && go test ./internal/db/ -race`
Expected: no vet output, `ok  	marshal/internal/db`

- [ ] **Step 7: Commit**

```bash
git add internal/db/generations.go internal/db/generations_test.go internal/db/migrations.go
git commit -m "feat(db): add session generation and turn archive tables"
```

---

## Task 3: FTS5 index and archive search

**Files:**
- Create: `internal/db/generations_search.go`
- Create: `internal/db/generations_search_test.go`
- Modify: `internal/db/migrations.go` (add `generation_turns_fts`)
- Modify: `internal/db/generations.go` (index rows as they are archived)

**Interfaces:**
- Consumes: `ArchivedTurn`, `scanArchivedTurn`, `resolveRefs`, `nullString`, `newTestDB`, `seedSession` from Tasks 1–2.
- Produces:
  - `type SearchHit struct { Turn ArchivedTurn; GenerationID string; GenerationSeq int; SessionID string }`
  - `func (db *DB) SearchArchivedTurns(sessionID, query, generationID string, limit int) ([]SearchHit, error)`

  Tasks 14, 15, 16 consume these.

**Design note:** the spec proposes an external-content FTS table (`content='generation_turns'`). That cannot work here: external-content FTS indexes the base table's `content` column, which is NULL precisely for the large blob-referenced rows that most need indexing. This task uses a **contentless** FTS table (`content=''`) whose rowid mirrors `generation_turns.id`, and always indexes the *resolved* text. Contentless tables support `MATCH` and `bm25` ranking but cannot return the indexed column, so results join back to `generation_turns` for display — which is what we want anyway, since that path already resolves blobs.

- [ ] **Step 1: Write the failing test**

Create `internal/db/generations_search_test.go`:

```go
package db

import (
	"strings"
	"testing"
	"time"
)

func TestSearchArchivedTurnsFindsInlineAndBlobBackedRows(t *testing.T) {
	database := newTestDB(t)
	sessionID := seedSession(t, database)
	now := time.Now().UTC()

	for _, g := range []Generation{
		{ID: "gen-0", SessionID: sessionID, Seq: 0, StartedAt: now},
		{ID: "gen-1", SessionID: sessionID, Seq: 1, StartedAt: now},
	} {
		if err := database.BeginGeneration(g); err != nil {
			t.Fatalf("BeginGeneration %s failed: %v", g.ID, err)
		}
	}

	// Small inline row, and a blob-backed row whose only copy of the search
	// term lives in the content store.
	inline := "please refactor the approval policy engine"
	blobBacked := strings.Repeat("padding ", 400) + " tree-sitter symbol extraction failed"
	if err := database.ArchiveTurns("gen-0", []ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: inline, CreatedAt: now},
		{TurnSeq: 1, Role: "tool", Content: blobBacked, CreatedAt: now},
	}, 2048, now); err != nil {
		t.Fatalf("ArchiveTurns gen-0 failed: %v", err)
	}
	if err := database.ArchiveTurns("gen-1", []ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: "unrelated question about themes", CreatedAt: now},
	}, 2048, now); err != nil {
		t.Fatalf("ArchiveTurns gen-1 failed: %v", err)
	}

	hits, err := database.SearchArchivedTurns(sessionID, "approval", "", 10)
	if err != nil {
		t.Fatalf("SearchArchivedTurns failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits for 'approval' = %d, want 1", len(hits))
	}
	if hits[0].Turn.Content != inline {
		t.Fatalf("hit content = %q, want %q", hits[0].Turn.Content, inline)
	}
	if hits[0].GenerationSeq != 0 || hits[0].GenerationID != "gen-0" {
		t.Fatalf("hit generation = %s/%d, want gen-0/0", hits[0].GenerationID, hits[0].GenerationSeq)
	}

	// The blob-backed row must be just as findable, with content resolved.
	hits, err = database.SearchArchivedTurns(sessionID, "tree-sitter", "", 10)
	if err != nil {
		t.Fatalf("SearchArchivedTurns for blob row failed: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits for 'tree-sitter' = %d, want 1 (blob-backed rows must be indexed)", len(hits))
	}
	if hits[0].Turn.Content != blobBacked {
		t.Fatalf("blob-backed hit content = %d chars, want %d resolved from the store",
			len(hits[0].Turn.Content), len(blobBacked))
	}

	// Scoping to a generation excludes the others.
	hits, err = database.SearchArchivedTurns(sessionID, "themes", "gen-0", 10)
	if err != nil {
		t.Fatalf("scoped SearchArchivedTurns failed: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits scoped to gen-0 = %d, want 0 (the match is in gen-1)", len(hits))
	}
}

func TestSearchArchivedTurnsTreatsQueryAsLiteralText(t *testing.T) {
	database := newTestDB(t)
	sessionID := seedSession(t, database)
	now := time.Now().UTC()
	if err := database.BeginGeneration(Generation{
		ID: "gen-0", SessionID: sessionID, Seq: 0, StartedAt: now,
	}); err != nil {
		t.Fatalf("BeginGeneration failed: %v", err)
	}
	if err := database.ArchiveTurns("gen-0", []ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: "quoted text here", CreatedAt: now},
	}, 2048, now); err != nil {
		t.Fatalf("ArchiveTurns failed: %v", err)
	}

	// FTS5 operator characters in user input must not become syntax errors.
	if _, err := database.SearchArchivedTurns(sessionID, `"unbalanced AND (`, "", 10); err != nil {
		t.Fatalf("query with FTS5 operators returned an error, want it treated as literal text: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestSearchArchivedTurns -v`
Expected: FAIL — `database.SearchArchivedTurns undefined`

- [ ] **Step 3: Add the FTS table to the schema**

In `internal/db/migrations.go`, append inside the `schema` string after `generation_turns`:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS generation_turns_fts USING fts5(
    body,
    content=''
);
```

- [ ] **Step 4: Index rows as they are archived**

In `internal/db/generations.go`, replace the insert loop body inside `ArchiveTurns` so it captures the row id and mirrors the resolved text into the FTS index within the same transaction:

```go
	for _, turn := range turns {
		var inline, ref sql.NullString
		if len(turn.Content) >= blobThreshold {
			hash, perr := db.PutBlob(turn.Content, at)
			if perr != nil {
				return perr
			}
			ref = sql.NullString{String: hash, Valid: true}
		} else {
			inline = sql.NullString{String: turn.Content, Valid: true}
		}
		res, ierr := tx.Exec(
			`INSERT INTO generation_turns
			 (generation_id, turn_seq, role, content, content_ref, tool_calls, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			generationID, turn.TurnSeq, turn.Role, inline, ref,
			nullString(turn.ToolCalls), turn.CreatedAt.UTC().Format(time.RFC3339),
		)
		if ierr != nil {
			return fmt.Errorf("insert generation turn: %w", ierr)
		}
		rowID, ierr := res.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("generation turn id: %w", ierr)
		}
		// Index the resolved text regardless of storage path, so search
		// behaves identically for inline and blob-backed rows.
		if _, ierr := tx.Exec(
			`INSERT INTO generation_turns_fts (rowid, body) VALUES (?, ?)`,
			rowID, turn.Content,
		); ierr != nil {
			return fmt.Errorf("index generation turn: %w", ierr)
		}
	}
```

- [ ] **Step 5: Write the search implementation**

Create `internal/db/generations_search.go`:

```go
package db

import (
	"fmt"
	"strings"
)

// SearchHit is one archived turn matched by a full-text query, carrying
// enough generation context for a caller to render "which generation was
// this from" without a second lookup.
type SearchHit struct {
	Turn          ArchivedTurn
	SessionID     string
	GenerationID  string
	GenerationSeq int
}

// defaultSearchLimit caps an unbounded search so a broad query cannot pull
// an entire archive into memory.
const defaultSearchLimit = 25

// SearchArchivedTurns runs a full-text query over archived turns. An empty
// sessionID searches every session; an empty generationID searches every
// generation within the selected scope. Results are ranked by bm25, best
// first, and blob-backed content is resolved before returning.
func (db *DB) SearchArchivedTurns(sessionID, query, generationID string, limit int) ([]SearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	sql := `SELECT t.id, t.turn_seq, t.role, t.content, t.content_ref, t.tool_calls, t.created_at,
	               g.generation_id, g.seq, g.session_id
	        FROM generation_turns_fts f
	        JOIN generation_turns t ON t.id = f.rowid
	        JOIN session_generations g ON g.generation_id = t.generation_id
	        WHERE generation_turns_fts MATCH ?`
	args := []any{ftsPhrase(query)}
	if sessionID != "" {
		sql += ` AND g.session_id = ?`
		args = append(args, sessionID)
	}
	if generationID != "" {
		sql += ` AND g.generation_id = ?`
		args = append(args, generationID)
	}
	sql += ` ORDER BY bm25(generation_turns_fts) LIMIT ?`
	args = append(args, limit)

	rows, err := db.sqlDB.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search archived turns: %w", err)
	}
	defer rows.Close()

	var hits []SearchHit
	refs := map[int64]string{}
	for rows.Next() {
		var t ArchivedTurn
		var content, ref, toolCalls any
		var createdAt, genID, sessID string
		var seq int
		if err := rows.Scan(&t.ID, &t.TurnSeq, &t.Role, &content, &ref, &toolCalls, &createdAt,
			&genID, &seq, &sessID); err != nil {
			return nil, fmt.Errorf("scan search hit: %w", err)
		}
		turn, err := archivedTurnFromParts(t.ID, t.TurnSeq, t.Role, content, ref, toolCalls, createdAt, refs)
		if err != nil {
			return nil, err
		}
		hits = append(hits, SearchHit{
			Turn: turn, SessionID: sessID, GenerationID: genID, GenerationSeq: seq,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resolve blob-backed content only after the result set is drained.
	turns := make([]ArchivedTurn, len(hits))
	for i := range hits {
		turns[i] = hits[i].Turn
	}
	resolved, err := db.resolveRefs(turns, refs)
	if err != nil {
		return nil, err
	}
	for i := range hits {
		hits[i].Turn = resolved[i]
	}
	return hits, nil
}

// ftsPhrase wraps a user query as a single quoted FTS5 phrase so that
// operator characters in user input are matched literally instead of
// parsed as query syntax (or rejected as a syntax error).
func ftsPhrase(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}
```

- [ ] **Step 6: Add the shared row-assembly helper**

The search scan and `scanArchivedTurn` must agree on how a row becomes an `ArchivedTurn`. Add this to `internal/db/generations.go` and rewrite `scanArchivedTurn` to call it:

```go
// archivedTurnFromParts assembles an ArchivedTurn from already-scanned
// column values, recording a blob hash in refs when the row is
// blob-backed. Shared by TurnsForGeneration and SearchArchivedTurns so the
// two paths cannot drift.
func archivedTurnFromParts(id int64, turnSeq int, role string, content, ref, toolCalls any, createdAt string, refs map[int64]string) (ArchivedTurn, error) {
	t := ArchivedTurn{ID: id, TurnSeq: turnSeq, Role: role}
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return ArchivedTurn{}, fmt.Errorf("parse turn created_at: %w", err)
	}
	t.CreatedAt = parsed.UTC()
	if s, ok := content.(string); ok {
		t.Content = s
	}
	if s, ok := toolCalls.(string); ok {
		t.ToolCalls = s
	}
	if s, ok := ref.(string); ok && s != "" {
		refs[id] = s
	}
	return t, nil
}

func scanArchivedTurn(rows rowScanner, refs map[int64]string) (ArchivedTurn, error) {
	var id int64
	var turnSeq int
	var role, createdAt string
	var content, ref, toolCalls any
	if err := rows.Scan(&id, &turnSeq, &role, &content, &ref, &toolCalls, &createdAt); err != nil {
		return ArchivedTurn{}, fmt.Errorf("scan generation turn: %w", err)
	}
	return archivedTurnFromParts(id, turnSeq, role, content, ref, toolCalls, createdAt, refs)
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/db/ -run 'TestSearchArchivedTurns|TestGenerationLifecycle' -v`
Expected: PASS, all three tests

- [ ] **Step 8: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/db/ && go test ./internal/db/ -race`
Expected: no vet output, `ok  	marshal/internal/db`

- [ ] **Step 9: Commit**

```bash
git add internal/db/generations_search.go internal/db/generations_search_test.go internal/db/generations.go internal/db/migrations.go
git commit -m "feat(db): add FTS5 index and search over archived generation turns"
```

---

## Task 4: `[session.rollover]` configuration

**Files:**
- Modify: `internal/app/config/types.go`
- Modify: `internal/app/config/defaults.go`
- Modify: `internal/app/config/file_types.go`
- Modify: `internal/app/config/merge.go`
- Create: `internal/app/config/rollover_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `cfg.Session.Rollover` of type `RolloverConfig` with fields `Enabled bool`, `Policy string`, `ContextPercentThreshold int`, `TurnCountThreshold int`, `TokenCounter string`, `DigestModel string`, `RecallToolEnabled string`, `Retention string`, `BlobThresholdBytes int`. Tasks 8, 14, and 17 read these.

- [ ] **Step 1: Write the failing test**

Create `internal/app/config/rollover_test.go`:

```go
package config

import "testing"

func TestDefaultRolloverIsDisabledAndSafe(t *testing.T) {
	cfg := Default()
	r := cfg.Session.Rollover
	if r.Enabled {
		t.Fatal("rollover is enabled by default; it must default to false so existing sessions are unaffected")
	}
	if r.Policy != "context_percent" {
		t.Fatalf("default policy = %q, want %q", r.Policy, "context_percent")
	}
	if r.ContextPercentThreshold != 70 {
		t.Fatalf("default context_percent_threshold = %d, want 70", r.ContextPercentThreshold)
	}
	if r.TurnCountThreshold != 40 {
		t.Fatalf("default turn_count_threshold = %d, want 40", r.TurnCountThreshold)
	}
	if r.TokenCounter != "auto" {
		t.Fatalf("default token_counter = %q, want %q", r.TokenCounter, "auto")
	}
	if r.RecallToolEnabled != "auto" {
		t.Fatalf("default recall_tool_enabled = %q, want %q", r.RecallToolEnabled, "auto")
	}
	if r.Retention != "forever" {
		t.Fatalf("default retention = %q, want %q", r.Retention, "forever")
	}
	if r.BlobThresholdBytes != 2048 {
		t.Fatalf("default blob_threshold_bytes = %d, want 2048", r.BlobThresholdBytes)
	}
	if r.DigestModel != "" {
		t.Fatalf("default digest_model = %q, want empty (use the session's main model)", r.DigestModel)
	}
}

func TestMergeRolloverOverridesOnlyProvidedFields(t *testing.T) {
	cfg := Default()
	enabled := true
	policy := "caller_checkpoint"
	if err := merge(&cfg, configFile{
		Session: &fileSession{
			Rollover: &fileRollover{Enabled: &enabled, Policy: &policy},
		},
	}); err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if !cfg.Session.Rollover.Enabled {
		t.Fatal("merge did not apply enabled = true")
	}
	if cfg.Session.Rollover.Policy != "caller_checkpoint" {
		t.Fatalf("policy = %q, want %q", cfg.Session.Rollover.Policy, "caller_checkpoint")
	}
	if cfg.Session.Rollover.ContextPercentThreshold != 70 {
		t.Fatalf("unspecified context_percent_threshold = %d, want the default 70 to survive merge",
			cfg.Session.Rollover.ContextPercentThreshold)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run TestDefaultRollover -v`
Expected: FAIL — `cfg.Session undefined (type Config has no field or method Session)`

- [ ] **Step 3: Add the config types**

In `internal/app/config/types.go`, add `Session` to the `Config` struct (after `Hooks`):

```go
	Session       SessionConfig                         `toml:"session"`
```

and add the two new types near `SnapshotsConfig`:

```go
// SessionConfig groups per-session runtime behaviour that is not specific
// to any one agent role.
type SessionConfig struct {
	Rollover RolloverConfig `toml:"rollover"`
}

// RolloverConfig controls context rollover: archiving the model-facing
// window and restarting it from a short resume digest. Disabled by
// default; when disabled, nothing in this struct has any effect.
type RolloverConfig struct {
	Enabled bool `toml:"enabled"`
	// Policy is "context_percent", "turn_count", or "caller_checkpoint".
	Policy string `toml:"policy"`
	// ContextPercentThreshold is the percentage of the active model's
	// context window at which a rollover is due. Only read when Policy is
	// "context_percent".
	ContextPercentThreshold int `toml:"context_percent_threshold"`
	// TurnCountThreshold is the turn count at which a rollover is due.
	// Read when Policy is "turn_count", and as a backstop under
	// "caller_checkpoint" so a caller that never requests a rollover still
	// gets one eventually.
	TurnCountThreshold int `toml:"turn_count_threshold"`
	// TokenCounter is "auto", "estimator", or "usage".
	TokenCounter string `toml:"token_counter"`
	// DigestModel overrides the model used to summarize an outgoing
	// generation. Empty means use the session's main model.
	DigestModel string `toml:"digest_model"`
	// RecallToolEnabled is "auto", "always", or "never".
	RecallToolEnabled string `toml:"recall_tool_enabled"`
	// Retention is "forever" (the only value honoured today; pruning
	// policies are deferred).
	Retention string `toml:"retention"`
	// BlobThresholdBytes is the archived-content size at or above which a
	// turn's content moves to the content-addressed store.
	BlobThresholdBytes int `toml:"blob_threshold_bytes"`
}
```

- [ ] **Step 4: Add the defaults**

In `internal/app/config/defaults.go`, inside the `Default()` struct literal (alongside `Snapshots:`), add:

```go
		Session: SessionConfig{
			Rollover: RolloverConfig{
				Enabled:                 false,
				Policy:                  "context_percent",
				ContextPercentThreshold: 70,
				TurnCountThreshold:      40,
				TokenCounter:            "auto",
				DigestModel:             "",
				RecallToolEnabled:       "auto",
				Retention:               "forever",
				BlobThresholdBytes:      2048,
			},
		},
```

- [ ] **Step 5: Add the nullable file mirrors**

In `internal/app/config/file_types.go`, add near `fileSnapshots`:

```go
type fileSession struct {
	Rollover *fileRollover `toml:"rollover"`
}

type fileRollover struct {
	Enabled                 *bool   `toml:"enabled"`
	Policy                  *string `toml:"policy"`
	ContextPercentThreshold *int    `toml:"context_percent_threshold"`
	TurnCountThreshold      *int    `toml:"turn_count_threshold"`
	TokenCounter            *string `toml:"token_counter"`
	DigestModel             *string `toml:"digest_model"`
	RecallToolEnabled       *string `toml:"recall_tool_enabled"`
	Retention               *string `toml:"retention"`
	BlobThresholdBytes      *int    `toml:"blob_threshold_bytes"`
}
```

and add the field to `configFile` (after `Hooks`):

```go
	Session     *fileSession     `toml:"session"`
```

- [ ] **Step 6: Add the merge stanza**

In `internal/app/config/merge.go`, add after the `file.Snapshots` block:

```go
	if file.Session != nil && file.Session.Rollover != nil {
		r := file.Session.Rollover
		set(&cfg.Session.Rollover.Enabled, r.Enabled)
		set(&cfg.Session.Rollover.Policy, r.Policy)
		set(&cfg.Session.Rollover.ContextPercentThreshold, r.ContextPercentThreshold)
		set(&cfg.Session.Rollover.TurnCountThreshold, r.TurnCountThreshold)
		set(&cfg.Session.Rollover.TokenCounter, r.TokenCounter)
		set(&cfg.Session.Rollover.DigestModel, r.DigestModel)
		set(&cfg.Session.Rollover.RecallToolEnabled, r.RecallToolEnabled)
		set(&cfg.Session.Rollover.Retention, r.Retention)
		set(&cfg.Session.Rollover.BlobThresholdBytes, r.BlobThresholdBytes)
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/app/config/ -run 'TestDefaultRollover|TestMergeRollover' -v`
Expected: PASS, both tests

- [ ] **Step 8: Verify the whole config package still passes**

Run: `gofmt -w . && go vet ./internal/app/config/ && go test ./internal/app/config/`
Expected: no vet output, `ok  	marshal/internal/app/config`

Note: `internal/app/config/save.go` round-trips config to TOML. If its tests fail because the new section is absent from the written output, add the `[session.rollover]` block there following the existing `[snapshots]` pattern.

- [ ] **Step 9: Commit**

```bash
git add internal/app/config/
git commit -m "feat(config): add [session.rollover] settings, disabled by default"
```

---

## Task 5: Token counters

**Files:**
- Create: `internal/rollover/counter.go`
- Create: `internal/rollover/counter_test.go`

**Interfaces:**
- Consumes: `contextpack.EstimateTokens` (`internal/contextpack/builder.go:24`), `schema.ChatMessage`.
- Produces:
  - `type TokenCounter interface { Name() string; CountTokens(ctx context.Context, wire []schema.ChatMessage) (int, error) }`
  - `type EstimatorCounter struct{}`
  - `type UsageCounter struct{ ... }` with `func NewUsageCounter() *UsageCounter` and `func (c *UsageCounter) Observe(promptTokens int)`
  - `func ResolveCounter(name string, usage *UsageCounter) TokenCounter`

  Tasks 8 and 17 consume these.

**Design note:** the spec calls for one `BackendCounter` per runtime, arguing that Ollama's `prompt_eval_count` differs from the OpenAI-style `usage.prompt_tokens`. That argument does not apply here — Marshal reaches Ollama through its OpenAI-compatible endpoint (`internal/llm/provider/templates.go:18`), and `internal/llm/provider/openai_compatible.go:281` already normalizes every backend's usage into `schema.Usage`. One `UsageCounter` fed from the existing `Runner.UsageObserver` covers Ollama, llama.cpp, vLLM, and LM Studio together.

- [ ] **Step 1: Write the failing test**

Create `internal/rollover/counter_test.go`:

```go
package rollover

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func TestEstimatorCounterSumsAllMessages(t *testing.T) {
	wire := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: strings.Repeat("a", 400)},
		{Role: schema.RoleUser, Content: strings.Repeat("b", 400)},
	}
	got, err := EstimatorCounter{}.CountTokens(context.Background(), wire)
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}
	if got != 200 {
		t.Fatalf("CountTokens = %d, want 200 (800 chars at 4 chars/token)", got)
	}
	if EstimatorCounter{}.Name() != "estimator" {
		t.Fatalf("Name = %q, want %q", EstimatorCounter{}.Name(), "estimator")
	}
}

func TestUsageCounterPrefersTheLargerOfObservedAndEstimated(t *testing.T) {
	ctx := context.Background()
	wire := []schema.ChatMessage{{Role: schema.RoleUser, Content: strings.Repeat("a", 400)}} // estimates 100

	c := NewUsageCounter()
	// With no observation yet, it must fall back to the estimator rather
	// than reporting zero, which would suppress every rollover.
	got, err := c.CountTokens(ctx, wire)
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}
	if got != 100 {
		t.Fatalf("unobserved CountTokens = %d, want the estimate 100", got)
	}

	// A larger real measurement wins: the estimator under-counts code.
	c.Observe(900)
	if got, _ = c.CountTokens(ctx, wire); got != 900 {
		t.Fatalf("CountTokens after Observe(900) = %d, want 900", got)
	}

	// A stale, smaller measurement must not mask a window that has since
	// grown past it.
	big := []schema.ChatMessage{{Role: schema.RoleUser, Content: strings.Repeat("a", 8000)}} // estimates 2000
	if got, _ = c.CountTokens(ctx, big); got != 2000 {
		t.Fatalf("CountTokens with a grown wire = %d, want the larger estimate 2000", got)
	}
}

func TestResolveCounter(t *testing.T) {
	usage := NewUsageCounter()
	tests := []struct {
		name  string
		input string
		usage *UsageCounter
		want  string
	}{
		{"auto with a usage counter", "auto", usage, "usage"},
		{"auto without one", "auto", nil, "estimator"},
		{"explicit estimator", "estimator", usage, "estimator"},
		{"explicit usage", "usage", usage, "usage"},
		{"unknown name falls back", "wat", usage, "estimator"},
		{"empty behaves as auto", "", usage, "usage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveCounter(tc.input, tc.usage).Name(); got != tc.want {
				t.Fatalf("ResolveCounter(%q).Name() = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollover/ -v`
Expected: FAIL — the package does not exist yet (`no Go files in .../internal/rollover`)

- [ ] **Step 3: Write the implementation**

Create `internal/rollover/counter.go`:

```go
// Package rollover archives a session's model-facing conversation window
// and restarts it from a short resume digest, so a long-running session
// stays inside a small context window without silently discarding history.
package rollover

import (
	"context"
	"sync/atomic"

	"marshal/internal/contextpack"
	"marshal/internal/llm/schema"
)

// TokenCounter reports how large the current generation's wire-level
// conversation is. Marshal always has a working default; a provider that
// reports real usage upgrades accuracy without changing the interface.
type TokenCounter interface {
	Name() string
	CountTokens(ctx context.Context, wire []schema.ChatMessage) (int, error)
}

// EstimatorCounter uses Marshal's existing 4-chars-per-token heuristic. It
// is always available, needs no network round trip, and is good enough to
// fire a rollover in the right ballpark — rolling over slightly early or
// late costs only a slightly shorter or longer generation.
type EstimatorCounter struct{}

func (EstimatorCounter) Name() string { return "estimator" }

func (EstimatorCounter) CountTokens(_ context.Context, wire []schema.ChatMessage) (int, error) {
	total := 0
	for _, m := range wire {
		total += contextpack.EstimateTokens(m.Content)
	}
	return total, nil
}

// UsageCounter reports the largest of (a) the prompt-token count the
// provider reported for the most recent call and (b) the current estimate.
//
// Neither number alone is right. The observed count is exact but stale the
// moment another tool result is appended; the estimate is current but
// under-counts code-heavy content. Taking the larger keeps the trigger
// conservative in both directions: a stale small observation cannot mask a
// window that has since grown, and a pessimistic estimate cannot undo a
// real measurement.
type UsageCounter struct {
	estimator EstimatorCounter
	observed  atomic.Int64
}

func NewUsageCounter() *UsageCounter { return &UsageCounter{} }

func (UsageCounter) Name() string { return "usage" }

// Observe records a provider-reported prompt-token count. Safe to call
// from the provider callback goroutine.
func (c *UsageCounter) Observe(promptTokens int) {
	if promptTokens > 0 {
		c.observed.Store(int64(promptTokens))
	}
}

func (c *UsageCounter) CountTokens(ctx context.Context, wire []schema.ChatMessage) (int, error) {
	estimate, err := c.estimator.CountTokens(ctx, wire)
	if err != nil {
		return 0, err
	}
	if observed := int(c.observed.Load()); observed > estimate {
		return observed, nil
	}
	return estimate, nil
}

// ResolveCounter picks a counter by configured name. "auto" (and the empty
// string) prefer real usage when a UsageCounter is wired up and fall back
// to the estimator otherwise. An unrecognised name falls back to the
// estimator rather than failing: a bad config value must not disable
// rollover silently.
func ResolveCounter(name string, usage *UsageCounter) TokenCounter {
	switch name {
	case "usage":
		if usage != nil {
			return usage
		}
		return EstimatorCounter{}
	case "estimator":
		return EstimatorCounter{}
	default: // "auto", "", anything unrecognised
		if usage != nil {
			return usage
		}
		return EstimatorCounter{}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rollover/ -v`
Expected: PASS, all three tests

- [ ] **Step 5: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/rollover/ && go test ./internal/rollover/ -race`
Expected: no vet output, `ok  	marshal/internal/rollover`

- [ ] **Step 6: Commit**

```bash
git add internal/rollover/counter.go internal/rollover/counter_test.go
git commit -m "feat(rollover): add token counters with estimator and provider-usage sources"
```

---

## Task 6: Rollover policy decision

**Files:**
- Create: `internal/rollover/policy.go`
- Create: `internal/rollover/policy_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const PolicyContextPercent = "context_percent"`, `PolicyTurnCount = "turn_count"`, `PolicyCallerCheckpoint = "caller_checkpoint"`
  - `type Policy struct { Mode string; ContextPercent int; TurnCount int }`
  - `type Signal struct { TurnsInGeneration int; Tokens int; ContextWindow int; Requested bool }`
  - `func Decide(p Policy, s Signal) bool`

  Task 8 consumes these.

- [ ] **Step 1: Write the failing test**

Create `internal/rollover/policy_test.go`:

```go
package rollover

import "testing"

func TestDecide(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		signal Signal
		want   bool
	}{
		{
			name:   "context_percent under threshold",
			policy: Policy{Mode: PolicyContextPercent, ContextPercent: 70},
			signal: Signal{Tokens: 6000, ContextWindow: 10000},
			want:   false,
		},
		{
			name:   "context_percent exactly at threshold",
			policy: Policy{Mode: PolicyContextPercent, ContextPercent: 70},
			signal: Signal{Tokens: 7000, ContextWindow: 10000},
			want:   true,
		},
		{
			name:   "context_percent with an unknown window does not guess",
			policy: Policy{Mode: PolicyContextPercent, ContextPercent: 70},
			signal: Signal{Tokens: 999999, ContextWindow: 0},
			want:   false,
		},
		{
			name:   "context_percent with an unknown window still honours the turn backstop",
			policy: Policy{Mode: PolicyContextPercent, ContextPercent: 70, TurnCount: 40},
			signal: Signal{Tokens: 10, ContextWindow: 0, TurnsInGeneration: 40},
			want:   true,
		},
		{
			name:   "turn_count below threshold",
			policy: Policy{Mode: PolicyTurnCount, TurnCount: 40},
			signal: Signal{TurnsInGeneration: 39},
			want:   false,
		},
		{
			name:   "turn_count at threshold",
			policy: Policy{Mode: PolicyTurnCount, TurnCount: 40},
			signal: Signal{TurnsInGeneration: 40},
			want:   true,
		},
		{
			name:   "turn_count with no threshold configured never fires",
			policy: Policy{Mode: PolicyTurnCount},
			signal: Signal{TurnsInGeneration: 10000},
			want:   false,
		},
		{
			name:   "caller_checkpoint fires only when requested",
			policy: Policy{Mode: PolicyCallerCheckpoint, TurnCount: 40},
			signal: Signal{TurnsInGeneration: 5},
			want:   false,
		},
		{
			name:   "caller_checkpoint honours a request",
			policy: Policy{Mode: PolicyCallerCheckpoint, TurnCount: 40},
			signal: Signal{TurnsInGeneration: 5, Requested: true},
			want:   true,
		},
		{
			name:   "caller_checkpoint backstop fires when the caller never asks",
			policy: Policy{Mode: PolicyCallerCheckpoint, TurnCount: 40},
			signal: Signal{TurnsInGeneration: 40},
			want:   true,
		},
		{
			name:   "unknown policy never fires",
			policy: Policy{Mode: "nonsense", TurnCount: 1},
			signal: Signal{TurnsInGeneration: 100, Requested: true},
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.policy, tc.signal); got != tc.want {
				t.Fatalf("Decide(%+v, %+v) = %v, want %v", tc.policy, tc.signal, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollover/ -run TestDecide -v`
Expected: FAIL — `undefined: Policy`

- [ ] **Step 3: Write the implementation**

Create `internal/rollover/policy.go`:

```go
package rollover

// Rollover trigger modes, as set by [session.rollover] policy.
const (
	// PolicyContextPercent rolls over once the generation crosses a
	// percentage of the active model's context window. This is the mode
	// that generalises across model sizes without per-model tuning.
	PolicyContextPercent = "context_percent"
	// PolicyTurnCount rolls over after a fixed number of turns.
	PolicyTurnCount = "turn_count"
	// PolicyCallerCheckpoint rolls over only when the calling agent asks,
	// with the turn count available as a backstop.
	PolicyCallerCheckpoint = "caller_checkpoint"
)

// Policy is the resolved trigger configuration for one session.
type Policy struct {
	Mode           string
	ContextPercent int
	TurnCount      int
}

// Signal is everything Decide needs to know about the current generation.
type Signal struct {
	TurnsInGeneration int
	Tokens            int
	ContextWindow     int
	Requested         bool
}

// Decide reports whether a rollover is due. It is pure: it neither
// performs a rollover nor consults the clock, so every trigger rule is
// testable in isolation.
//
// The turn count acts as a backstop in two cases beyond its own mode:
// under caller_checkpoint it bounds a caller that never asks, and under
// context_percent it bounds a session whose model context window is
// unknown. Marshal never guesses a window size (see resolveRoute in
// internal/agent/route.go), so an unknown window disables the percentage
// rule rather than inventing a denominator.
func Decide(p Policy, s Signal) bool {
	turnBackstop := p.TurnCount > 0 && s.TurnsInGeneration >= p.TurnCount

	switch p.Mode {
	case PolicyContextPercent:
		if p.ContextPercent > 0 && s.ContextWindow > 0 {
			if s.Tokens*100 >= s.ContextWindow*p.ContextPercent {
				return true
			}
		}
		return turnBackstop
	case PolicyTurnCount:
		return turnBackstop
	case PolicyCallerCheckpoint:
		return s.Requested || turnBackstop
	default:
		return false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rollover/ -run TestDecide -v`
Expected: PASS, all eleven subtests

- [ ] **Step 5: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/rollover/ && go test ./internal/rollover/`
Expected: no vet output, `ok  	marshal/internal/rollover`

- [ ] **Step 6: Commit**

```bash
git add internal/rollover/policy.go internal/rollover/policy_test.go
git commit -m "feat(rollover): add pure policy decision for rollover triggers"
```

---

## Task 7: Digest providers

**Files:**
- Create: `internal/rollover/digest.go`
- Create: `internal/rollover/digest_test.go`

**Interfaces:**
- Consumes: `schema.ChatMessage`, `schema.RoleSystem`.
- Produces:
  - `type GenerationHandle struct { SessionID, GenerationID string; Seq int; Wire []schema.ChatMessage; Goal string }`
  - `type DigestProvider interface { Digest(ctx context.Context, outgoing GenerationHandle) (digest string, source string, err error) }`
  - `type Chatter func(ctx context.Context, msgs []schema.ChatMessage) (string, error)`
  - `type LLMSummaryProvider struct { Chat Chatter }`
  - `func MinimalDigest(seq int) string`
  - `const SummaryDirective`, `const SourceLLMSummary`, `SourceStructured`, `SourceMinimal`
  - `var ErrEmptyDigest`

  Tasks 8, 13, and 17 consume these.

**Note:** `SummaryDirective` is `handoffSummaryDirective` moved verbatim out of `internal/agent/handoff.go:14`. Task 13 deletes the original. Copy the text exactly — it is tuned prompt content, not prose to paraphrase.

- [ ] **Step 1: Write the failing test**

Create `internal/rollover/digest_test.go`:

```go
package rollover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func TestLLMSummaryProviderAppendsTheDirectiveAndReturnsTheSummary(t *testing.T) {
	var seen []schema.ChatMessage
	p := LLMSummaryProvider{Chat: func(_ context.Context, msgs []schema.ChatMessage) (string, error) {
		seen = msgs
		return "  ## Current State\nhalfway done  ", nil
	}}

	h := GenerationHandle{
		SessionID: "s", GenerationID: "gen-0", Seq: 0,
		Wire: []schema.ChatMessage{
			{Role: schema.RoleUser, Content: "add a rollover feature"},
			{Role: schema.RoleAssistant, Content: "reading files"},
		},
	}
	digest, source, err := p.Digest(context.Background(), h)
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	if source != SourceLLMSummary {
		t.Fatalf("source = %q, want %q", source, SourceLLMSummary)
	}
	if digest != "## Current State\nhalfway done" {
		t.Fatalf("digest = %q, want the trimmed summary text", digest)
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d messages, want the 2 wire messages plus the directive", len(seen))
	}
	if seen[2].Role != schema.RoleSystem || seen[2].Content != SummaryDirective {
		t.Fatalf("last message = %+v, want the summary directive as a system message", seen[2])
	}
}

func TestLLMSummaryProviderRejectsAnEmptySummary(t *testing.T) {
	p := LLMSummaryProvider{Chat: func(context.Context, []schema.ChatMessage) (string, error) {
		return "   \n ", nil
	}}
	if _, _, err := p.Digest(context.Background(), GenerationHandle{}); !errors.Is(err, ErrEmptyDigest) {
		t.Fatalf("Digest error = %v, want ErrEmptyDigest", err)
	}
}

func TestLLMSummaryProviderPropagatesChatErrors(t *testing.T) {
	boom := errors.New("provider unreachable")
	p := LLMSummaryProvider{Chat: func(context.Context, []schema.ChatMessage) (string, error) {
		return "", boom
	}}
	if _, _, err := p.Digest(context.Background(), GenerationHandle{}); !errors.Is(err, boom) {
		t.Fatalf("Digest error = %v, want it to wrap the provider error", err)
	}
}

func TestMinimalDigestNamesTheGenerationAndPointsAtTheArchive(t *testing.T) {
	got := MinimalDigest(3)
	if !strings.Contains(got, "generation 3") {
		t.Fatalf("MinimalDigest(3) = %q, want it to name generation 3", got)
	}
	if !strings.Contains(got, "marshal history") {
		t.Fatalf("MinimalDigest(3) = %q, want it to point at the archived transcript", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollover/ -run 'TestLLMSummary|TestMinimalDigest' -v`
Expected: FAIL — `undefined: LLMSummaryProvider`

- [ ] **Step 3: Write the implementation**

Create `internal/rollover/digest.go`:

```go
package rollover

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/llm/schema"
)

// Digest source labels, recorded on the new generation so the archive says
// where its seed came from.
const (
	SourceLLMSummary = "llm_summary"
	SourceStructured = "structured"
	SourceMinimal    = "minimal"
)

// ErrEmptyDigest is returned when a provider produces no usable text.
var ErrEmptyDigest = errors.New("rollover: digest provider returned empty text")

// SummaryDirective asks the model for a self-contained handoff. Moved
// verbatim from internal/agent/handoff.go: the summary is the ONLY context
// that survives a rollover, so vague next steps are useless.
const SummaryDirective = `Summarize this conversation so the task can continue with no other context. This summary will be the ONLY context available afterwards, so be thorough and specific. Do not call tools; respond with plain text only, covering:

## Current State
The exact original request, what has been completed, what is in progress, and what remains.

## Files & Changes
Files modified (with what changed), files read and why they matter, and important file paths / line numbers.

## Technical Context
Commands that worked and commands that failed (exact commands), key decisions made and why, gotchas discovered, assumptions made.

## Exact Next Steps
Numbered, concrete steps with file paths — "3. Update parseAction in internal/agent/protocol.go to accept the actions array", not "continue implementing".`

// GenerationHandle describes the outgoing generation to a digest provider.
// A summarizing provider reads Wire; a structured provider ignores it and
// reports state it already holds on disk, using GenerationID only to record
// which generation it is resuming from.
type GenerationHandle struct {
	SessionID    string
	GenerationID string
	Seq          int
	Wire         []schema.ChatMessage
	Goal         string
}

// DigestProvider produces the resume digest that seeds the next
// generation. This is the seam that lets a caller with its own on-disk
// state supply a cheap, exact digest instead of paying for summarization.
type DigestProvider interface {
	Digest(ctx context.Context, outgoing GenerationHandle) (digest string, source string, err error)
}

// Chatter is a one-shot model call. Declared as a function type so this
// package never imports internal/agent (which imports this one).
type Chatter func(ctx context.Context, msgs []schema.ChatMessage) (string, error)

// LLMSummaryProvider is the built-in fallback: one extra model call per
// rollover, summarizing the outgoing window. Bounded and infrequent by
// design — rollovers are rare relative to turns.
type LLMSummaryProvider struct {
	Chat Chatter
}

func (p LLMSummaryProvider) Digest(ctx context.Context, outgoing GenerationHandle) (string, string, error) {
	msgs := append(append([]schema.ChatMessage{}, outgoing.Wire...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: SummaryDirective})
	text, err := p.Chat(ctx, msgs)
	if err != nil {
		return "", "", fmt.Errorf("summarize generation %d: %w", outgoing.Seq, err)
	}
	summary := strings.TrimSpace(text)
	if summary == "" {
		return "", "", ErrEmptyDigest
	}
	return summary, SourceLLMSummary, nil
}

// MinimalDigest is the last-resort seed used when digest generation fails.
// It loses detail but never blocks the session, and the full transcript is
// already archived by the time it is reached.
func MinimalDigest(seq int) string {
	return fmt.Sprintf(
		"Continuing from generation %d, whose transcript was archived rather than carried forward. "+
			"A summary could not be produced. Re-read any files you need; run `marshal history` to inspect the archived transcript.",
		seq)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rollover/ -run 'TestLLMSummary|TestMinimalDigest' -v`
Expected: PASS, all four tests

- [ ] **Step 5: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/rollover/ && go test ./internal/rollover/`
Expected: no vet output, `ok  	marshal/internal/rollover`

- [ ] **Step 6: Commit**

```bash
git add internal/rollover/digest.go internal/rollover/digest_test.go
git commit -m "feat(rollover): add pluggable digest providers with LLM summary default"
```

---

## Task 8: Rollover controller

**Files:**
- Create: `internal/rollover/controller.go`
- Create: `internal/rollover/controller_test.go`

**Interfaces:**
- Consumes: `Policy`, `Signal`, `Decide` (Task 6); `TokenCounter` (Task 5); `DigestProvider`, `GenerationHandle`, `MinimalDigest`, `SourceMinimal` (Task 7); `db.Generation`, `db.ArchivedTurn` (Task 2).
- Produces:
  - `type Store interface { BeginGeneration(g db.Generation) error; EndGeneration(generationID string, endedAt time.Time, endReason string) error; ArchiveTurns(generationID string, turns []db.ArchivedTurn, blobThreshold int, at time.Time) error }` — `*db.DB` already satisfies it.
  - `type Controller struct { ... }` with exported config fields and these methods:
    - `func (c *Controller) Start(ctx context.Context) error`
    - `func (c *Controller) Current() (generationID string, seq int, seedDigest string)`
    - `func (c *Controller) RequestRollover()`
    - `func (c *Controller) Archive(ctx context.Context, msgs []schema.ChatMessage) error`
    - `func (c *Controller) EndTurn()`
    - `func (c *Controller) Due(ctx context.Context, wire []schema.ChatMessage, contextWindow int) bool`
    - `func (c *Controller) Rollover(ctx context.Context, h GenerationHandle) (seedDigest string, err error)`
    - `func (c *Controller) Close(ctx context.Context) error`

  Tasks 12, 13, and 17 consume these.

**Ordering guarantee this task must enforce:** the outgoing transcript is archived by `Archive` calls *before* `Rollover` runs, and `Rollover` closes the outgoing generation *before* it asks for a digest. A digest failure therefore can never lose history — it downgrades the seed to `MinimalDigest` and the session continues.

- [ ] **Step 1: Write the failing test**

Create `internal/rollover/controller_test.go`:

```go
package rollover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

// fakeStore records calls in order so tests can assert that archival
// happens before a generation is closed and digested.
type fakeStore struct {
	begun    []db.Generation
	ended    []string
	archived map[string][]db.ArchivedTurn
	calls    []string
	failNext error
}

func newFakeStore() *fakeStore {
	return &fakeStore{archived: map[string][]db.ArchivedTurn{}}
}

func (f *fakeStore) BeginGeneration(g db.Generation) error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.begun = append(f.begun, g)
	f.calls = append(f.calls, "begin:"+g.ID)
	return nil
}

func (f *fakeStore) EndGeneration(id string, _ time.Time, reason string) error {
	f.ended = append(f.ended, id)
	f.calls = append(f.calls, "end:"+id+":"+reason)
	return nil
}

func (f *fakeStore) ArchiveTurns(id string, turns []db.ArchivedTurn, _ int, _ time.Time) error {
	f.archived[id] = append(f.archived[id], turns...)
	f.calls = append(f.calls, fmt.Sprintf("archive:%s:%d", id, len(turns)))
	return nil
}

type stubDigest struct {
	text   string
	source string
	err    error
	calls  int
}

func (s *stubDigest) Digest(context.Context, GenerationHandle) (string, string, error) {
	s.calls++
	return s.text, s.source, s.err
}

func newTestController(store Store, digest DigestProvider, policy Policy) *Controller {
	n := 0
	at := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	return &Controller{
		SessionID:     "sess-1",
		Store:         store,
		Counter:       EstimatorCounter{},
		Digest:        digest,
		Policy:        policy,
		BlobThreshold: 2048,
		Now:           func() time.Time { return at },
		NewID: func() string {
			id := fmt.Sprintf("gen-%d", n)
			n++
			return id
		},
	}
}

func TestStartOpensGenerationZero(t *testing.T) {
	store := newFakeStore()
	c := newTestController(store, &stubDigest{}, Policy{Mode: PolicyTurnCount, TurnCount: 2})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if len(store.begun) != 1 || store.begun[0].Seq != 0 {
		t.Fatalf("begun = %+v, want exactly one generation at seq 0", store.begun)
	}
	if store.begun[0].SeedDigest != "" {
		t.Fatalf("gen 0 seed digest = %q, want empty (nothing precedes it)", store.begun[0].SeedDigest)
	}
	id, seq, seed := c.Current()
	if id != "gen-0" || seq != 0 || seed != "" {
		t.Fatalf("Current() = (%q, %d, %q), want (\"gen-0\", 0, \"\")", id, seq, seed)
	}
}

func TestArchiveMapsWireMessagesToTurnsWithIncreasingSeq(t *testing.T) {
	store := newFakeStore()
	c := newTestController(store, &stubDigest{}, Policy{Mode: PolicyTurnCount, TurnCount: 99})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := c.Archive(ctx, []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "do the thing"},
		{Role: schema.RoleAssistant, Content: "reading", ToolCalls: []schema.ToolCall{{Name: "file.read"}}},
	}); err != nil {
		t.Fatalf("first Archive failed: %v", err)
	}
	if err := c.Archive(ctx, []schema.ChatMessage{
		{Role: schema.RoleTool, Content: "file contents"},
	}); err != nil {
		t.Fatalf("second Archive failed: %v", err)
	}

	turns := store.archived["gen-0"]
	if len(turns) != 3 {
		t.Fatalf("archived %d turns, want 3", len(turns))
	}
	for i, turn := range turns {
		if turn.TurnSeq != i {
			t.Fatalf("turn %d has TurnSeq %d, want %d (seq must increase across Archive calls)", i, turn.TurnSeq, i)
		}
	}
	if turns[0].Role != "user" || turns[2].Role != "tool" {
		t.Fatalf("roles = %q/%q, want user/tool", turns[0].Role, turns[2].Role)
	}
	if !strings.Contains(turns[1].ToolCalls, "file.read") {
		t.Fatalf("turn 1 tool calls = %q, want the tool call serialised as JSON", turns[1].ToolCalls)
	}
}

func TestArchiveIsANoOpWhenThereIsNothingNew(t *testing.T) {
	store := newFakeStore()
	c := newTestController(store, &stubDigest{}, Policy{Mode: PolicyTurnCount, TurnCount: 99})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := c.Archive(ctx, nil); err != nil {
		t.Fatalf("Archive(nil) failed: %v", err)
	}
	if len(store.archived["gen-0"]) != 0 {
		t.Fatalf("archived %d turns for an empty input, want 0", len(store.archived["gen-0"]))
	}
}

func TestDueUsesThePolicyAndTurnCounter(t *testing.T) {
	store := newFakeStore()
	c := newTestController(store, &stubDigest{}, Policy{Mode: PolicyTurnCount, TurnCount: 2})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if c.Due(ctx, nil, 0) {
		t.Fatal("Due at 0 turns = true, want false")
	}
	c.EndTurn()
	if c.Due(ctx, nil, 0) {
		t.Fatal("Due at 1 turn = true, want false with threshold 2")
	}
	c.EndTurn()
	if !c.Due(ctx, nil, 0) {
		t.Fatal("Due at 2 turns = false, want true with threshold 2")
	}
}

func TestDueHonoursAnExplicitRequestUnderCallerCheckpoint(t *testing.T) {
	store := newFakeStore()
	c := newTestController(store, &stubDigest{}, Policy{Mode: PolicyCallerCheckpoint})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if c.Due(ctx, nil, 0) {
		t.Fatal("Due without a request = true, want false")
	}
	c.RequestRollover()
	if !c.Due(ctx, nil, 0) {
		t.Fatal("Due after RequestRollover = false, want true")
	}
}

func TestRolloverClosesTheOldGenerationBeforeDigestingAndOpensTheNext(t *testing.T) {
	store := newFakeStore()
	digest := &stubDigest{text: "picked up at step 4", source: SourceLLMSummary}
	c := newTestController(store, digest, Policy{Mode: PolicyTurnCount, TurnCount: 1})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := c.Archive(ctx, []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("Archive failed: %v", err)
	}
	c.EndTurn()

	seed, err := c.Rollover(ctx, GenerationHandle{SessionID: "sess-1", GenerationID: "gen-0", Seq: 0})
	if err != nil {
		t.Fatalf("Rollover failed: %v", err)
	}
	if seed != "picked up at step 4" {
		t.Fatalf("seed = %q, want the provider's digest", seed)
	}

	want := []string{"begin:gen-0", "archive:gen-0:1", "end:gen-0:rollover", "begin:gen-1"}
	if fmt.Sprint(store.calls) != fmt.Sprint(want) {
		t.Fatalf("call order = %v, want %v (archive must precede close, close must precede the new generation)", store.calls, want)
	}
	if store.begun[1].SeedDigest != "picked up at step 4" || store.begun[1].DigestSource != SourceLLMSummary {
		t.Fatalf("gen 1 = %+v, want the digest and its source recorded", store.begun[1])
	}
	if store.begun[1].Seq != 1 {
		t.Fatalf("gen 1 seq = %d, want 1", store.begun[1].Seq)
	}

	// Counters reset with the new generation.
	if c.Due(ctx, nil, 0) {
		t.Fatal("Due immediately after a rollover = true, want false (turn count must reset)")
	}
	id, seq, gotSeed := c.Current()
	if id != "gen-1" || seq != 1 || gotSeed != "picked up at step 4" {
		t.Fatalf("Current() = (%q, %d, %q), want gen-1/1/the digest", id, seq, gotSeed)
	}
}

func TestRolloverFallsBackToAMinimalDigestWhenTheProviderFails(t *testing.T) {
	store := newFakeStore()
	digest := &stubDigest{err: errors.New("model unreachable")}
	c := newTestController(store, digest, Policy{Mode: PolicyTurnCount, TurnCount: 1})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := c.Archive(ctx, []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("Archive failed: %v", err)
	}

	seed, err := c.Rollover(ctx, GenerationHandle{GenerationID: "gen-0", Seq: 0})
	if err != nil {
		t.Fatalf("Rollover returned an error on digest failure: %v — it must degrade, not block the session", err)
	}
	if !strings.Contains(seed, "generation 0") {
		t.Fatalf("seed = %q, want the minimal digest naming generation 0", seed)
	}
	if store.begun[1].DigestSource != SourceMinimal {
		t.Fatalf("digest source = %q, want %q", store.begun[1].DigestSource, SourceMinimal)
	}
	// History must survive a digest failure.
	if len(store.archived["gen-0"]) != 1 {
		t.Fatalf("archived turns after digest failure = %d, want 1 (archival precedes digesting)",
			len(store.archived["gen-0"]))
	}
}

func TestCloseEndsTheLiveGeneration(t *testing.T) {
	store := newFakeStore()
	c := newTestController(store, &stubDigest{}, Policy{Mode: PolicyTurnCount, TurnCount: 5})
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if len(store.ended) != 1 || store.calls[len(store.calls)-1] != "end:gen-0:session_end" {
		t.Fatalf("calls = %v, want the live generation closed with reason session_end", store.calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rollover/ -run 'TestStart|TestArchive|TestDue|TestRollover|TestClose' -v`
Expected: FAIL — `undefined: Controller`

- [ ] **Step 3: Write the implementation**

Create `internal/rollover/controller.go`:

```go
package rollover

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

// Store is the persistence Marshal's rollover controller needs. *db.DB
// satisfies it; tests supply a fake.
type Store interface {
	BeginGeneration(g db.Generation) error
	EndGeneration(generationID string, endedAt time.Time, endReason string) error
	ArchiveTurns(generationID string, turns []db.ArchivedTurn, blobThreshold int, at time.Time) error
}

// Controller owns one logical session's generation lifecycle. It is safe
// for concurrent use: the runner archives from the turn goroutine while
// the provider callback may be observing usage.
type Controller struct {
	SessionID     string
	Store         Store
	Counter       TokenCounter
	Digest        DigestProvider
	Policy        Policy
	BlobThreshold int
	Now           func() time.Time
	NewID         func() string
	Logger        *slog.Logger

	mu         sync.Mutex
	genID      string
	genSeq     int
	seedDigest string
	turnSeq    int
	turnsInGen int
	requested  bool
	closed     bool
}

// Start opens generation 0. Call once, before the first turn.
func (c *Controller) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.begin(0, "", "")
}

// begin opens a generation. Caller must hold c.mu.
func (c *Controller) begin(seq int, seedDigest, digestSource string) error {
	id := c.NewID()
	if err := c.Store.BeginGeneration(db.Generation{
		ID:           id,
		SessionID:    c.SessionID,
		Seq:          seq,
		StartedAt:    c.Now(),
		SeedDigest:   seedDigest,
		DigestSource: digestSource,
	}); err != nil {
		return fmt.Errorf("open generation %d: %w", seq, err)
	}
	c.genID = id
	c.genSeq = seq
	c.seedDigest = seedDigest
	c.turnSeq = 0
	c.turnsInGen = 0
	c.requested = false
	return nil
}

// Current reports the live generation's id, sequence, and seed digest.
func (c *Controller) Current() (string, int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.genID, c.genSeq, c.seedDigest
}

// RequestRollover asks for a rollover at the next safe boundary. It is a
// request, not an interrupt: an in-flight tool call always finishes first,
// because the only place Due is consulted is between completed turns.
func (c *Controller) RequestRollover() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requested = true
}

// EndTurn records that a turn completed in the live generation.
func (c *Controller) EndTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turnsInGen++
}

// Archive appends wire messages to the live generation's transcript.
// Called incrementally rather than only at rollover time, so a crashed
// session still has an archive up to its last completed turn.
func (c *Controller) Archive(ctx context.Context, msgs []schema.ChatMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	c.mu.Lock()
	genID := c.genID
	turns := make([]db.ArchivedTurn, 0, len(msgs))
	now := c.Now()
	for _, m := range msgs {
		turns = append(turns, db.ArchivedTurn{
			TurnSeq:   c.turnSeq,
			Role:      string(m.Role),
			Content:   m.Content,
			ToolCalls: encodeToolCalls(m.ToolCalls),
			CreatedAt: now,
		})
		c.turnSeq++
	}
	threshold := c.BlobThreshold
	c.mu.Unlock()

	if err := c.Store.ArchiveTurns(genID, turns, threshold, now); err != nil {
		return fmt.Errorf("archive turns for generation %s: %w", genID, err)
	}
	return nil
}

// Due reports whether a rollover is due right now. contextWindow is the
// active model's window, or 0 when unknown.
func (c *Controller) Due(ctx context.Context, wire []schema.ChatMessage, contextWindow int) bool {
	tokens, err := c.Counter.CountTokens(ctx, wire)
	if err != nil {
		// A counting failure must not wedge the session; fall through with
		// zero tokens so only the turn-count rules can fire.
		if c.Logger != nil {
			c.Logger.Warn("rollover: token counting failed", "error", err, "counter", c.Counter.Name())
		}
		tokens = 0
	}
	c.mu.Lock()
	sig := Signal{
		TurnsInGeneration: c.turnsInGen,
		Tokens:            tokens,
		ContextWindow:     contextWindow,
		Requested:         c.requested,
	}
	policy := c.Policy
	c.mu.Unlock()
	return Decide(policy, sig)
}

// Rollover closes the outgoing generation and opens the next one, seeded
// with a resume digest.
//
// Order matters and is load-bearing: the transcript is already archived by
// prior Archive calls, and the outgoing generation is closed BEFORE the
// digest is requested. A digest failure therefore degrades to a minimal
// seed rather than losing history or blocking the session.
func (c *Controller) Rollover(ctx context.Context, h GenerationHandle) (string, error) {
	c.mu.Lock()
	genID, genSeq := c.genID, c.genSeq
	c.mu.Unlock()

	if err := c.Store.EndGeneration(genID, c.Now(), "rollover"); err != nil {
		return "", fmt.Errorf("close generation %s: %w", genID, err)
	}

	if h.GenerationID == "" {
		h.GenerationID = genID
		h.Seq = genSeq
	}
	h.SessionID = c.SessionID

	digest, source, err := c.Digest.Digest(ctx, h)
	if err != nil || digest == "" {
		if c.Logger != nil {
			c.Logger.Warn("rollover: digest failed, seeding minimally",
				"error", err, "generation", genID, "seq", genSeq)
		}
		digest, source = MinimalDigest(genSeq), SourceMinimal
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.begin(genSeq+1, digest, source); err != nil {
		return "", err
	}
	return digest, nil
}

// Close ends the live generation at session shutdown.
func (c *Controller) Close(ctx context.Context) error {
	c.mu.Lock()
	genID := c.genID
	alreadyClosed := c.closed
	c.closed = true
	c.mu.Unlock()
	if genID == "" || alreadyClosed {
		return nil
	}
	if err := c.Store.EndGeneration(genID, c.Now(), "session_end"); err != nil {
		return fmt.Errorf("close generation %s at session end: %w", genID, err)
	}
	return nil
}

// encodeToolCalls serialises a message's tool calls for the archive.
// Returns "" when there are none, so the column stays NULL.
func encodeToolCalls(calls []schema.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	encoded, err := json.Marshal(calls)
	if err != nil {
		return ""
	}
	return string(encoded)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/rollover/ -v`
Expected: PASS, every test in the package

- [ ] **Step 5: Verify the package is clean under the race detector**

Run: `gofmt -w . && go vet ./internal/rollover/ && go test ./internal/rollover/ -race`
Expected: no vet output, `ok  	marshal/internal/rollover`

- [ ] **Step 6: Commit**

```bash
git add internal/rollover/controller.go internal/rollover/controller_test.go
git commit -m "feat(rollover): add generation controller with archive-before-digest ordering"
```

---

## Task 9: Startup reconciliation of crashed generations

**Files:**
- Modify: `internal/db/generations.go`
- Modify: `internal/db/generations_test.go`

**Interfaces:**
- Consumes: the `session_generations` table (Task 2).
- Produces: `func (db *DB) ReconcileOpenGenerations(at time.Time) (int, error)` — returns how many stale generations were closed. Task 17 calls it at startup.

**Why:** a session killed mid-generation leaves `ended_at IS NULL` forever, which would make the archive claim that a long-dead generation is still live. Because turns are archived incrementally (Task 8), the transcript itself is already intact up to the last completed turn; only the generation row needs closing out.

- [ ] **Step 1: Write the failing test**

Append to `internal/db/generations_test.go`:

```go
func TestReconcileOpenGenerationsClosesStaleRowsOnly(t *testing.T) {
	database := newTestDB(t)
	sessionID := seedSession(t, database)
	start := time.Now().UTC().Add(-time.Hour)

	if err := database.BeginGeneration(Generation{
		ID: "gen-0", SessionID: sessionID, Seq: 0, StartedAt: start,
	}); err != nil {
		t.Fatalf("BeginGeneration gen-0 failed: %v", err)
	}
	if err := database.EndGeneration("gen-0", start.Add(time.Minute), "rollover"); err != nil {
		t.Fatalf("EndGeneration failed: %v", err)
	}
	// gen-1 is left open, as if the process was killed.
	if err := database.BeginGeneration(Generation{
		ID: "gen-1", SessionID: sessionID, Seq: 1, StartedAt: start.Add(time.Minute),
	}); err != nil {
		t.Fatalf("BeginGeneration gen-1 failed: %v", err)
	}

	now := time.Now().UTC()
	closed, err := database.ReconcileOpenGenerations(now)
	if err != nil {
		t.Fatalf("ReconcileOpenGenerations failed: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed = %d, want 1 (only the open generation)", closed)
	}

	gens, err := database.GenerationsForSession(sessionID)
	if err != nil {
		t.Fatalf("GenerationsForSession failed: %v", err)
	}
	if gens[0].EndReason != "rollover" {
		t.Fatalf("gen 0 end_reason = %q, want the original 'rollover' to be left alone", gens[0].EndReason)
	}
	if gens[1].EndReason != "error" || gens[1].EndedAt == nil {
		t.Fatalf("gen 1 = %+v, want it closed with end_reason 'error'", gens[1])
	}

	// Running it again must be a no-op.
	closed, err = database.ReconcileOpenGenerations(now)
	if err != nil {
		t.Fatalf("second ReconcileOpenGenerations failed: %v", err)
	}
	if closed != 0 {
		t.Fatalf("second run closed = %d, want 0", closed)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestReconcileOpenGenerations -v`
Expected: FAIL — `database.ReconcileOpenGenerations undefined`

- [ ] **Step 3: Write the implementation**

Append to `internal/db/generations.go`:

```go
// ReconcileOpenGenerations closes out every generation still marked live,
// recording end_reason 'error'. Run once at startup: a generation left
// open means the process died before it could be closed cleanly. The
// archived turns themselves are already intact, because turns are written
// incrementally rather than only at rollover time.
func (db *DB) ReconcileOpenGenerations(at time.Time) (int, error) {
	res, err := db.sqlDB.Exec(
		`UPDATE session_generations SET ended_at = ?, end_reason = 'error' WHERE ended_at IS NULL`,
		at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("reconcile open generations: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reconcile open generations rows: %w", err)
	}
	return int(n), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db/ -run TestReconcileOpenGenerations -v`
Expected: PASS

- [ ] **Step 5: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/db/ && go test ./internal/db/`
Expected: no vet output, `ok  	marshal/internal/db`

- [ ] **Step 6: Commit**

```bash
git add internal/db/generations.go internal/db/generations_test.go
git commit -m "feat(db): close crashed generations on startup reconciliation"
```

---

## Task 10: Generation boundary on session state

**Files:**
- Modify: `internal/app/session/session.go`
- Create: `internal/app/session/generation_test.go`

**Interfaces:**
- Consumes: the existing `State.mu`, `State.messages` (`internal/app/session/session.go:150`).
- Produces:
  - `type GenerationInfo struct { ID string; Seq int; SeedDigest string; StartMsgID int64 }`
  - `func (s *State) BeginGeneration(id string, seq int, seedDigest string)`
  - `func (s *State) Generation() GenerationInfo`

  Tasks 11, 12, and 17 consume these.

**Why an ID and not an index:** `State.Messages()` returns the active branch of a message tree that rollback can re-root (`rebuildActiveBranch`, `internal/app/session/messages.go:235`). A slice index would silently point at the wrong turn after a rollback; a message ID either appears on the branch or does not, which Task 11 can detect and handle.

- [ ] **Step 1: Write the failing test**

Create `internal/app/session/generation_test.go`:

```go
package session

import "testing"

func TestBeginGenerationRecordsTheCurrentLeafAsTheBoundary(t *testing.T) {
	s := &State{}

	// No messages yet: the boundary is 0, meaning "replay everything".
	s.BeginGeneration("gen-0", 0, "")
	if got := s.Generation(); got.StartMsgID != 0 || got.ID != "gen-0" || got.Seq != 0 {
		t.Fatalf("Generation() = %+v, want gen-0/0 with StartMsgID 0", got)
	}
	if got := s.Generation().SeedDigest; got != "" {
		t.Fatalf("gen 0 seed digest = %q, want empty", got)
	}

	s.mu.Lock()
	s.messages = []Message{{ID: 7}, {ID: 8}, {ID: 9}}
	s.mu.Unlock()

	s.BeginGeneration("gen-1", 1, "resume from here")
	got := s.Generation()
	if got.StartMsgID != 9 {
		t.Fatalf("StartMsgID = %d, want 9 (the last message on the branch at rollover time)", got.StartMsgID)
	}
	if got.ID != "gen-1" || got.Seq != 1 || got.SeedDigest != "resume from here" {
		t.Fatalf("Generation() = %+v, want gen-1/1 with the seed digest", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/session/ -run TestBeginGeneration -v`
Expected: FAIL — `s.BeginGeneration undefined`

- [ ] **Step 3: Write the implementation**

In `internal/app/session/session.go`, add the field to `State` (alongside `turnIndex`):

```go
	generation      GenerationInfo
```

and add the type and methods near the other `State` accessors:

```go
// GenerationInfo identifies the live rollover generation and where its
// replayable history begins. StartMsgID is the id of the last message on
// the active branch when the generation opened: history replay includes
// only messages after it. Zero means "replay everything", which is the
// state of a session that has never rolled over.
type GenerationInfo struct {
	ID         string
	Seq        int
	SeedDigest string
	StartMsgID int64
}

// BeginGeneration records a new generation boundary at the current end of
// the active branch. Called by the rollover controller's caller after a
// rollover completes, and once at session start.
func (s *State) BeginGeneration(id string, seq int, seedDigest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var startMsgID int64
	if n := len(s.messages); n > 0 {
		startMsgID = s.messages[n-1].ID
	}
	s.generation = GenerationInfo{
		ID:         id,
		Seq:        seq,
		SeedDigest: seedDigest,
		StartMsgID: startMsgID,
	}
}

// Generation returns the live generation boundary.
func (s *State) Generation() GenerationInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/session/ -run TestBeginGeneration -v`
Expected: PASS

- [ ] **Step 5: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/app/session/ && go test ./internal/app/session/ -race`
Expected: no vet output, `ok  	marshal/internal/app/session`

- [ ] **Step 6: Commit**

```bash
git add internal/app/session/session.go internal/app/session/generation_test.go
git commit -m "feat(session): track the live rollover generation boundary"
```

---

## Task 11: Generation-scoped history replay

**Files:**
- Modify: `internal/agent/history.go`
- Modify: `internal/agent/history_test.go`
- Modify: `internal/agent/runner.go:392` and `internal/agent/runner.go:416` (two call sites)

**Interfaces:**
- Consumes: `session.GenerationInfo` (Task 10).
- Produces: `func buildHistoryMessages(prior []session.Message, maxTokens int, gen session.GenerationInfo) []schema.ChatMessage` — the third parameter is new.

**Behaviour:** when a generation boundary is set, replay only messages after it and prepend the seed digest as a system message. When the boundary is not on the active branch — which happens after the user rolls back past it — ignore the boundary and replay normally, because a boundary pointing into a discarded branch would otherwise blank the history for no reason.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/history_test.go`:

```go
func TestBuildHistoryMessagesWithoutAGenerationBoundaryReplaysEverything(t *testing.T) {
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "first"},
		{ID: 2, Role: session.RoleAssistant, Content: "first answer", Final: true},
	}
	got := buildHistoryMessages(prior, 8000, session.GenerationInfo{})
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (no boundary means replay everything)", len(got))
	}
}

func TestBuildHistoryMessagesAfterRolloverReplacesHistoryWithTheDigest(t *testing.T) {
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "before the boundary"},
		{ID: 2, Role: session.RoleAssistant, Content: "old answer", Final: true},
		{ID: 3, Role: session.RoleUser, Content: "after the boundary"},
		{ID: 4, Role: session.RoleAssistant, Content: "new answer", Final: true},
	}
	gen := session.GenerationInfo{ID: "gen-1", Seq: 1, SeedDigest: "you were at step 4", StartMsgID: 2}

	got := buildHistoryMessages(prior, 8000, gen)
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3 (digest + the 2 messages after the boundary)", len(got))
	}
	if got[0].Role != schema.RoleSystem || !strings.Contains(got[0].Content, "you were at step 4") {
		t.Fatalf("first message = %+v, want the seed digest as a system message", got[0])
	}
	for _, m := range got[1:] {
		if strings.Contains(m.Content, "before the boundary") || strings.Contains(m.Content, "old answer") {
			t.Fatalf("pre-boundary content leaked into the replay: %q", m.Content)
		}
	}
}

func TestBuildHistoryMessagesIgnoresABoundaryThatIsNotOnTheBranch(t *testing.T) {
	// The user rolled back past the rollover point, so message 99 is gone.
	prior := []session.Message{
		{ID: 1, Role: session.RoleUser, Content: "first"},
		{ID: 2, Role: session.RoleAssistant, Content: "first answer", Final: true},
	}
	gen := session.GenerationInfo{ID: "gen-1", Seq: 1, SeedDigest: "stale digest", StartMsgID: 99}

	got := buildHistoryMessages(prior, 8000, gen)
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (an off-branch boundary must be ignored, not blank the history)", len(got))
	}
	for _, m := range got {
		if strings.Contains(m.Content, "stale digest") {
			t.Fatalf("a stale digest was injected for an off-branch boundary: %+v", got)
		}
	}
}
```

Add `"strings"` and `"marshal/internal/llm/schema"` to that file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestBuildHistoryMessages -v`
Expected: FAIL — `not enough arguments in call to buildHistoryMessages`

- [ ] **Step 3: Write the implementation**

Replace `buildHistoryMessages` in `internal/agent/history.go`:

```go
// buildHistoryMessages converts prior session transcript entries into chat
// messages so the model remembers earlier turns. Only user turns and final
// (non-salvaged) assistant answers are replayed: intermediate reasoning,
// plans, system notices, and salvaged fallbacks are noise or unreliable.
// When the total exceeds maxTokens, the oldest turns are dropped first.
//
// gen scopes the replay to the live rollover generation. After a rollover,
// history before the boundary is not replayed at all — the seed digest
// stands in for it. A boundary that is not on the active branch (the user
// rolled back past the rollover point) is ignored rather than honoured,
// since replaying nothing would be strictly worse than replaying the
// branch the user actually chose.
func buildHistoryMessages(prior []session.Message, maxTokens int, gen session.GenerationInfo) []schema.ChatMessage {
	if maxTokens <= 0 {
		maxTokens = defaultHistoryBudgetTokens
	}

	scoped, seeded := scopeToGeneration(prior, gen)

	var candidates []schema.ChatMessage
	for _, m := range scoped {
		switch m.Role {
		case session.RoleUser:
			candidates = append(candidates, schema.ChatMessage{Role: schema.RoleUser, Content: m.Content})
		case session.RoleAssistant:
			if m.Final && !m.Salvaged {
				candidates = append(candidates, schema.ChatMessage{Role: schema.RoleAssistant, Content: m.Content})
			}
		}
	}

	// Walk backwards accumulating until the budget is spent, then restore order.
	budget := maxTokens * 4 // chars
	total := 0
	start := len(candidates)
	for i := len(candidates) - 1; i >= 0; i-- {
		total += len(candidates[i].Content)
		if total > budget {
			break
		}
		start = i
	}
	replayed := candidates[start:]

	if seeded == "" {
		return replayed
	}
	out := make([]schema.ChatMessage, 0, len(replayed)+1)
	out = append(out, schema.ChatMessage{
		Role:    schema.RoleSystem,
		Content: "Earlier conversation was archived to keep the context small. Resume from this summary:\n\n" + seeded,
	})
	return append(out, replayed...)
}

// scopeToGeneration returns the messages that belong to the live
// generation, plus the seed digest to prepend (empty when there is none or
// when the boundary does not apply).
func scopeToGeneration(prior []session.Message, gen session.GenerationInfo) ([]session.Message, string) {
	if gen.StartMsgID == 0 {
		return prior, ""
	}
	for i, m := range prior {
		if m.ID == gen.StartMsgID {
			return prior[i+1:], gen.SeedDigest
		}
	}
	// Boundary is not on this branch: fall back to unscoped replay.
	return prior, ""
}
```

- [ ] **Step 4: Update the two call sites**

In `internal/agent/runner.go`, at both line 392 and line 416, change:

```go
			messages = append(messages, buildHistoryMessages(priorTranscript, r.HistoryBudgetTokens)...)
```

to:

```go
			messages = append(messages, buildHistoryMessages(priorTranscript, r.HistoryBudgetTokens, r.State.Generation())...)
```

- [ ] **Step 5: Fix the pre-existing history tests**

The existing tests in `internal/agent/history_test.go` call `buildHistoryMessages` with two arguments. Add `session.GenerationInfo{}` as the third argument to each — the zero value preserves their current expectations exactly.

Run: `go test ./internal/agent/ -run TestBuildHistoryMessages -v`
Expected: PASS, both the pre-existing and the three new tests

- [ ] **Step 6: Verify the agent package still passes**

Run: `gofmt -w . && go vet ./internal/agent/ && go test ./internal/agent/`
Expected: no vet output, `ok  	marshal/internal/agent`

- [ ] **Step 7: Commit**

```bash
git add internal/agent/history.go internal/agent/history_test.go internal/agent/runner.go
git commit -m "feat(agent): scope replayed history to the live rollover generation"
```

---

## Task 12: Runner archival and cross-turn rollover

**Files:**
- Modify: `internal/agent/runner.go`
- Create: `internal/agent/rollover.go`
- Create: `internal/agent/rollover_test.go`

**Interfaces:**
- Consumes: `rollover.Controller`, `rollover.GenerationHandle` (Tasks 7–8); `session.State.BeginGeneration`, `State.Generation`, `State.TurnUsage` (Task 10 and `internal/app/session/session.go:238`).
- Produces:
  - `Runner.Rollover *rollover.Controller` — nil disables rollover entirely.
  - `func (r *Runner) flushArchive(ctx context.Context, messages []schema.ChatMessage, from int) int`
  - `func (r *Runner) maybeRollover(ctx context.Context, messages []schema.ChatMessage, goal string)`

  Tasks 13 and 17 consume these.

**Why the end of `RunTask` is a safe boundary by construction:** `RunTask` returns only after the tool loop has finished, so no tool call can be in flight there. The spec's "defer the rollover until the tool call resolves" rule needs no explicit deferral logic — the only place `Due` is consulted is a point where nothing is in flight.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/rollover_test.go`:

```go
package agent

import (
	"context"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
)

// recordingStore captures what the runner archives.
type recordingStore struct {
	begun    []db.Generation
	archived map[string][]db.ArchivedTurn
}

func newRecordingStore() *recordingStore {
	return &recordingStore{archived: map[string][]db.ArchivedTurn{}}
}

func (s *recordingStore) BeginGeneration(g db.Generation) error {
	s.begun = append(s.begun, g)
	return nil
}

func (s *recordingStore) EndGeneration(string, time.Time, string) error { return nil }

func (s *recordingStore) ArchiveTurns(id string, turns []db.ArchivedTurn, _ int, _ time.Time) error {
	s.archived[id] = append(s.archived[id], turns...)
	return nil
}

type fixedDigest struct{ text string }

func (f fixedDigest) Digest(context.Context, rollover.GenerationHandle) (string, string, error) {
	return f.text, rollover.SourceStructured, nil
}

func newTestRolloverController(store rollover.Store, policy rollover.Policy, digest string) *rollover.Controller {
	n := 0
	return &rollover.Controller{
		SessionID:     "sess-1",
		Store:         store,
		Counter:       rollover.EstimatorCounter{},
		Digest:        fixedDigest{text: digest},
		Policy:        policy,
		BlobThreshold: 2048,
		Now:           time.Now,
		NewID: func() string {
			id := "gen-" + string(rune('0'+n))
			n++
			return id
		},
	}
}

func TestFlushArchiveWritesOnlyTheNewTail(t *testing.T) {
	store := newRecordingStore()
	ctrl := newTestRolloverController(store, rollover.Policy{Mode: rollover.PolicyTurnCount, TurnCount: 99}, "d")
	if err := ctrl.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	r := &Runner{Rollover: ctrl}
	ctx := context.Background()

	messages := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system prompt"},
		{Role: schema.RoleUser, Content: "the goal"},
	}
	from := r.flushArchive(ctx, messages, 1) // skip the system prompt
	if from != 2 {
		t.Fatalf("flushArchive returned %d, want 2 (the new cursor)", from)
	}
	messages = append(messages, schema.ChatMessage{Role: schema.RoleTool, Content: "tool output"})
	if got := r.flushArchive(ctx, messages, from); got != 3 {
		t.Fatalf("second flushArchive returned %d, want 3", got)
	}

	turns := store.archived["gen-0"]
	if len(turns) != 2 {
		t.Fatalf("archived %d turns, want 2 (goal and tool output, not the system prompt)", len(turns))
	}
	if turns[0].Content != "the goal" || turns[1].Content != "tool output" {
		t.Fatalf("archived turns = %q / %q, want the goal then the tool output",
			turns[0].Content, turns[1].Content)
	}
}

func TestFlushArchiveIsANoOpWithoutAController(t *testing.T) {
	r := &Runner{}
	if got := r.flushArchive(context.Background(), []schema.ChatMessage{{Content: "x"}}, 0); got != 0 {
		t.Fatalf("flushArchive without a controller returned %d, want 0", got)
	}
}

func TestMaybeRolloverOpensTheNextGenerationAndUpdatesSessionState(t *testing.T) {
	store := newRecordingStore()
	ctrl := newTestRolloverController(store, rollover.Policy{Mode: rollover.PolicyTurnCount, TurnCount: 1}, "resume here")
	ctx := context.Background()
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	state := newTestState(t)
	r := &Runner{Rollover: ctrl, State: state}
	r.maybeRollover(ctx, []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}}, "hi")

	if len(store.begun) != 2 {
		t.Fatalf("generations begun = %d, want 2 (the rollover opened the next one)", len(store.begun))
	}
	if store.begun[1].SeedDigest != "resume here" {
		t.Fatalf("gen 1 seed = %q, want %q", store.begun[1].SeedDigest, "resume here")
	}
	gen := state.Generation()
	if gen.ID != "gen-1" || gen.Seq != 1 || gen.SeedDigest != "resume here" {
		t.Fatalf("session generation = %+v, want gen-1/1 with the digest", gen)
	}
}

func TestMaybeRolloverDoesNothingWhenNotDue(t *testing.T) {
	store := newRecordingStore()
	ctrl := newTestRolloverController(store, rollover.Policy{Mode: rollover.PolicyTurnCount, TurnCount: 99}, "d")
	ctx := context.Background()
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	state := newTestState(t)
	r := &Runner{Rollover: ctrl, State: state}
	r.maybeRollover(ctx, nil, "hi")

	if len(store.begun) != 1 {
		t.Fatalf("generations begun = %d, want 1 (no rollover was due)", len(store.begun))
	}
	if id := state.Generation().ID; id != "" {
		t.Fatalf("session generation = %q, want it untouched when no rollover fired", id)
	}
}
```

`newTestState` already exists in `internal/agent/runner_testhelpers_test.go`. If its signature differs, use whatever that file provides to construct a `*session.State`; do not add a second helper.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestFlushArchive|TestMaybeRollover' -v`
Expected: FAIL — `unknown field Rollover in struct literal of type Runner`

- [ ] **Step 3: Add the runner field**

In `internal/agent/runner.go`, add to the `Runner` struct (near `Snapshotter`):

```go
	// Rollover archives the model-facing window and restarts it from a
	// resume digest when the configured policy says it is due. nil (the
	// default) disables rollover entirely and preserves prior behaviour.
	Rollover *rollover.Controller
```

Add `"marshal/internal/rollover"` to the import block, and add `Rollover` to the persisted-fields list in the concurrency-contract comment at `internal/agent/runner.go:129`.

Also propagate it in `CopyFrom` (`internal/agent/runner.go:268`), alongside `r.UsageObserver = other.UsageObserver`:

```go
	r.Rollover = other.Rollover
```

- [ ] **Step 4: Write the helpers**

Create `internal/agent/rollover.go`:

```go
package agent

import (
	"context"

	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
)

// flushArchive writes messages[from:] to the live generation's archive and
// returns the new cursor. Archival is incremental so a crashed session
// still has a transcript up to its last completed step.
//
// An archive failure is logged and swallowed: losing archive fidelity is
// bad, but failing the user's turn because a write failed is worse, and
// the live conversation is unaffected either way.
func (r *Runner) flushArchive(ctx context.Context, messages []schema.ChatMessage, from int) int {
	if r.Rollover == nil || from < 0 || from >= len(messages) {
		return from
	}
	if err := r.Rollover.Archive(ctx, messages[from:]); err != nil && r.State != nil {
		r.State.Logger().Warn("rollover: archiving turn failed", "error", err)
	}
	return len(messages)
}

// maybeRollover ends the turn and, if the policy says a rollover is due,
// performs it and records the new generation boundary on session state.
//
// This runs only after the tool loop has finished, which is what makes the
// spec's safe-boundary rule hold by construction: no tool call can be in
// flight at the point RunTask returns.
func (r *Runner) maybeRollover(ctx context.Context, messages []schema.ChatMessage, goal string) {
	if r.Rollover == nil {
		return
	}
	r.Rollover.EndTurn()

	var window int
	if r.State != nil {
		_, window = r.State.TurnUsage()
	}
	if !r.Rollover.Due(ctx, messages, window) {
		return
	}

	genID, seq, _ := r.Rollover.Current()
	digest, err := r.Rollover.Rollover(ctx, rollover.GenerationHandle{
		GenerationID: genID,
		Seq:          seq,
		Wire:         messages,
		Goal:         goal,
	})
	if err != nil {
		if r.State != nil {
			r.State.Logger().Error("rollover: failed to roll over generation", "error", err, "generation", genID)
		}
		return
	}
	newID, newSeq, _ := r.Rollover.Current()
	if r.State != nil {
		r.State.BeginGeneration(newID, newSeq, digest)
	}
}
```

- [ ] **Step 5: Wire the runner's turn loop**

In `internal/agent/runner.go`, immediately after each of the two places the goal is appended (the initial build around line 394 and the plan-first rebuild around line 418), record the archive cursor. Change:

```go
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
```

to:

```go
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
	archiveFrom := len(messages) - 1
```

for the first occurrence, and to this for the second (inside the plan-first rebuild, where `archiveFrom` already exists):

```go
			messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
			archiveFrom = len(messages) - 1
```

Then, immediately after `archiveFrom` is first declared, add the deferred flush-and-rollover:

```go
	// Archive whatever the turn produced and consider a rollover. Deferred
	// so every return path from the tool loop is covered; the closure reads
	// the current value of messages and archiveFrom, not their values here.
	defer func() {
		archiveFrom = r.flushArchive(ctx, messages, archiveFrom)
		r.maybeRollover(ctx, messages, goal)
	}()
```

Finally, keep the archive current during long tool loops by flushing before each model call. Immediately before `res, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages, effectiveRF)` (around line 504), add:

```go
		archiveFrom = r.flushArchive(ctx, messages, archiveFrom)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run 'TestFlushArchive|TestMaybeRollover' -v`
Expected: PASS, all four tests

- [ ] **Step 7: Verify the agent package is unaffected when rollover is off**

Run: `gofmt -w . && go vet ./internal/agent/ && go test ./internal/agent/ -race`
Expected: no vet output, `ok  	marshal/internal/agent`. Every pre-existing test runs with `Rollover == nil` and must still pass unchanged — that is the regression guard for "a session that never enables rollover is unaffected."

- [ ] **Step 8: Commit**

```bash
git add internal/agent/rollover.go internal/agent/rollover_test.go internal/agent/runner.go
git commit -m "feat(agent): archive turns and roll over at the cross-turn safe boundary"
```

---

## Task 13: Unify intra-turn compaction under rollover

**Files:**
- Modify: `internal/agent/handoff.go`
- Modify: `internal/agent/rollover.go`
- Modify: `internal/agent/runner.go:494-502`
- Modify: `internal/agent/rollover_test.go`

**Interfaces:**
- Consumes: `rollover.SummaryDirective` (Task 7), `Controller.Rollover` (Task 8), `flushArchive` / `maybeRollover` (Task 12).
- Produces: `func (r *Runner) compactContext(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, goal string, responseFormat *schema.ResponseFormat) ([]schema.ChatMessage, error)` — the single entry point the tool loop calls when the window overflows.

**Why:** `summarizeAndContinue` already digests the window and restarts it; it just throws the outgoing transcript away and records nothing. Routing it through the controller archives the transcript, records a generation, and makes the digest source pluggable — while producing exactly the same wire-level result when rollover is disabled.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/rollover_test.go`:

```go
func TestCompactContextArchivesAndRollsOverWhenEnabled(t *testing.T) {
	store := newRecordingStore()
	// TurnCount 0 with caller_checkpoint means only an explicit request
	// fires — the overflow path must roll over regardless of policy.
	ctrl := newTestRolloverController(store, rollover.Policy{Mode: rollover.PolicyCallerCheckpoint}, "resume from the digest")
	ctx := context.Background()
	if err := ctrl.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	state := newTestState(t)
	r := &Runner{Rollover: ctrl, State: state, Registry: registry.New()}
	messages := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system prompt"},
		{Role: schema.RoleUser, Content: "the goal"},
		{Role: schema.RoleTool, Content: "a very large tool result"},
	}

	fresh, err := r.compactContext(ctx, nil, "", messages, "the goal", nil)
	if err != nil {
		t.Fatalf("compactContext failed: %v", err)
	}

	// The outgoing transcript is archived, not discarded.
	turns := store.archived["gen-0"]
	if len(turns) == 0 {
		t.Fatal("compactContext archived nothing; the outgoing transcript must be preserved")
	}
	var sawToolResult bool
	for _, turn := range turns {
		if strings.Contains(turn.Content, "a very large tool result") {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatal("the oversized tool result was not archived")
	}

	// A new generation is open and seeded.
	if len(store.begun) != 2 || store.begun[1].SeedDigest != "resume from the digest" {
		t.Fatalf("generations = %+v, want a second one seeded with the digest", store.begun)
	}
	if state.Generation().Seq != 1 {
		t.Fatalf("session generation seq = %d, want 1", state.Generation().Seq)
	}

	// The fresh window is short and carries the digest forward.
	if len(fresh) >= len(messages)+1 {
		t.Fatalf("fresh window has %d messages, want it shorter than the outgoing %d", len(fresh), len(messages))
	}
	var carried bool
	for _, m := range fresh {
		if strings.Contains(m.Content, "resume from the digest") {
			carried = true
		}
		if strings.Contains(m.Content, "a very large tool result") {
			t.Fatal("the oversized tool result survived into the fresh window")
		}
	}
	if !carried {
		t.Fatal("the fresh window does not contain the resume digest")
	}
}
```

Add `"strings"` and `"marshal/internal/tools/registry"` to that file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestCompactContext -v`
Expected: FAIL — `r.compactContext undefined`

- [ ] **Step 3: Move the summary directive out of handoff.go**

In `internal/agent/handoff.go`, delete the `handoffSummaryDirective` const entirely and replace its single use inside `summarizeAndContinue` with `rollover.SummaryDirective`:

```go
	req := append(append([]schema.ChatMessage{}, messages...),
		schema.ChatMessage{Role: schema.RoleSystem, Content: rollover.SummaryDirective})
```

Add `"marshal/internal/rollover"` to that file's imports. The directive text is now owned by `internal/rollover/digest.go` (Task 7); it must exist in exactly one place so the two compaction paths cannot drift.

- [ ] **Step 4: Write the dispatcher and the rollover-backed path**

Append to `internal/agent/rollover.go`:

```go
// compactContext reclaims the model-facing window when it overflows the
// turn budget. With a rollover controller wired up it archives the
// outgoing transcript and opens a new generation; without one it falls
// back to the original summarize-and-discard behaviour, so a session with
// rollover disabled behaves exactly as it did before.
func (r *Runner) compactContext(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, goal string, responseFormat *schema.ResponseFormat) ([]schema.ChatMessage, error) {
	if r.Rollover == nil {
		return r.summarizeAndContinue(ctx, p, model, messages, goal, responseFormat)
	}
	return r.rolloverAndContinue(ctx, messages, goal)
}

// rolloverAndContinue archives the oversized window, rolls the generation
// forward, and rebuilds a short window around the resume digest. The
// rebuilt shape matches summarizeAndContinue's deliberately: the model
// sees the same system prompt, context pack, goal, digest, and continue
// instruction it would have seen before rollover existed.
func (r *Runner) rolloverAndContinue(ctx context.Context, messages []schema.ChatMessage, goal string) ([]schema.ChatMessage, error) {
	// Archive before digesting: a digest failure must never lose history.
	if err := r.Rollover.Archive(ctx, messages); err != nil && r.State != nil {
		r.State.Logger().Warn("rollover: archiving the outgoing window failed", "error", err)
	}

	genID, seq, _ := r.Rollover.Current()
	digest, err := r.Rollover.Rollover(ctx, rollover.GenerationHandle{
		GenerationID: genID,
		Seq:          seq,
		Wire:         messages,
		Goal:         goal,
	})
	if err != nil {
		return nil, err
	}
	newID, newSeq, _ := r.Rollover.Current()
	if r.State != nil {
		r.State.BeginGeneration(newID, newSeq, digest)
	}

	fresh := []schema.ChatMessage{
		BuildSystemPromptWithDeferred(r.role(), r.Registry.List(), r.Registry.ListDeferred(), r.SkillIndex, r.State.ActiveSkills(), r.NativeTools),
	}
	fresh = appendContextPackMessage(fresh, r.State.ContextPack())
	fresh = append(fresh,
		schema.ChatMessage{Role: schema.RoleUser, Content: goal},
		schema.ChatMessage{Role: schema.RoleAssistant, Content: "Progress summary (the earlier transcript was archived to fit the context budget):\n\n" + digest},
		schema.ChatMessage{Role: schema.RoleUser, Content: "Continue the task from that summary. Do not repeat work the summary marks as completed."},
	)

	r.State.AddMessage(session.RoleSystem, "Context rolled over mid-turn; continuing from a resume digest.", session.ContentTypePlain)
	return fresh, nil
}
```

Add `"marshal/internal/app/session"` and `"marshal/internal/llm/provider"` to the imports of `internal/agent/rollover.go`.

- [ ] **Step 5: Route the overflow branch through the dispatcher**

In `internal/agent/runner.go` around line 494, change:

```go
			if fresh, serr := r.summarizeAndContinue(ctx, turnProvider, turnModel, messages, goal, effectiveRF); serr == nil {
```

to:

```go
			if fresh, serr := r.compactContext(ctx, turnProvider, turnModel, messages, goal, effectiveRF); serr == nil {
```

and, inside that success branch, reset the archive cursor so the fresh window is not re-archived:

```go
				messages = fresh
				archiveFrom = len(fresh)
				pressureMessageSent = false // the fresh transcript may legitimately approach the budget again
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run 'TestCompactContext|TestFlushArchive|TestMaybeRollover' -v`
Expected: PASS, all five tests

- [ ] **Step 7: Verify the untouched path still behaves identically**

Run: `go test ./internal/agent/ -race`
Expected: `ok  	marshal/internal/agent`. The pre-existing `summarizeAndContinue` tests must pass unchanged — with `Rollover == nil` the dispatcher delegates straight to it.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/rollover.go internal/agent/rollover_test.go internal/agent/handoff.go internal/agent/runner.go
git commit -m "feat(agent): route intra-turn compaction through the rollover controller"
```

---

## Task 14: `recall_history` tool

**Files:**
- Create: `internal/tools/native/recall.go`
- Create: `internal/tools/native/recall_test.go`
- Modify: `internal/tools/native/native.go` (conditional registration)

**Interfaces:**
- Consumes: `db.SearchArchivedTurns` (Task 3), `config.RolloverConfig` (Task 4), the existing `toolSet` fields `db`, `sessionState`, `config` (`internal/tools/native/native.go:85`), and `decodeArgs` (`internal/tools/native/helpers.go`).
- Produces: `func (t *toolSet) recallHistoryTool() registry.Tool` and `func recallToolEnabled(cfg config.RolloverConfig) bool`.

**Gating rationale (from the spec):** recall is a genuine safety net when the digest is an LLM summary, because summaries are lossy by nature. Under `caller_checkpoint` the caller's own on-disk state is already ground truth, so the tool mostly adds a way for the model to go looking for things it does not need. Hence `auto` resolves to off for `caller_checkpoint` and on otherwise.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/native/recall_test.go`:

```go
package native

import (
	"testing"

	"marshal/internal/app/config"
)

func TestRecallToolEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.RolloverConfig
		want bool
	}{
		{
			name: "off when rollover itself is disabled",
			cfg:  config.RolloverConfig{Enabled: false, RecallToolEnabled: "always"},
			want: false,
		},
		{
			name: "never means never",
			cfg:  config.RolloverConfig{Enabled: true, RecallToolEnabled: "never", Policy: "context_percent"},
			want: false,
		},
		{
			name: "always means always",
			cfg:  config.RolloverConfig{Enabled: true, RecallToolEnabled: "always", Policy: "caller_checkpoint"},
			want: true,
		},
		{
			name: "auto is on for summary-digest sessions",
			cfg:  config.RolloverConfig{Enabled: true, RecallToolEnabled: "auto", Policy: "context_percent"},
			want: true,
		},
		{
			name: "auto is off for caller_checkpoint sessions",
			cfg:  config.RolloverConfig{Enabled: true, RecallToolEnabled: "auto", Policy: "caller_checkpoint"},
			want: false,
		},
		{
			name: "an empty setting behaves as auto",
			cfg:  config.RolloverConfig{Enabled: true, Policy: "turn_count"},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := recallToolEnabled(tc.cfg); got != tc.want {
				t.Fatalf("recallToolEnabled(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native/ -run TestRecallToolEnabled -v`
Expected: FAIL — `undefined: recallToolEnabled`

- [ ] **Step 3: Write the implementation**

Create `internal/tools/native/recall.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

type recallHistoryArgs struct {
	Query        string `json:"query"`
	GenerationID string `json:"generation_id"`
	Limit        int    `json:"limit"`
}

// recallExcerptChars caps how much of a matched turn the model sees, so
// paging one exchange back in cannot itself blow the context budget that
// rollover exists to protect.
const recallExcerptChars = 1500

// recallToolEnabled resolves the recall_tool_enabled setting. "auto" turns
// the tool on when the digest will be an LLM summary (lossy, so recall is
// a real safety net) and off under caller_checkpoint, where the caller's
// on-disk state is already complete ground truth.
func recallToolEnabled(cfg config.RolloverConfig) bool {
	if !cfg.Enabled {
		return false
	}
	switch cfg.RecallToolEnabled {
	case "never":
		return false
	case "always":
		return true
	default: // "auto", ""
		return cfg.Policy != "caller_checkpoint"
	}
}

func (t *toolSet) recallHistoryTool() registry.Tool {
	tool := registry.Tool{
		Name:        "recall_history",
		Description: "Search this session's archived conversation history, including turns from earlier generations that were rolled over and are no longer in context. Use it when you need a specific past exchange the resume summary did not carry forward.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Text to search for across archived turns"},"generation_id":{"type":"string","description":"Optional generation to restrict the search to"},"limit":{"type":"integer","minimum":1,"maximum":25,"description":"Maximum matches to return (default 5, clamped to 25)"}},"required":["query"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.sessionState == nil {
			return registry.ToolResult{}, errors.New("session history is not available for recall_history")
		}
		args, err := decodeArgs[recallHistoryArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if strings.TrimSpace(args.Query) == "" {
			return registry.ToolResult{}, errors.New("recall_history requires a non-empty query")
		}
		limit := args.Limit
		if limit <= 0 {
			limit = 5
		}
		if limit > 25 {
			limit = 25
		}

		hits, err := t.db.SearchArchivedTurns(t.sessionState.SessionID(), args.Query, args.GenerationID, limit)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("search archived history: %w", err)
		}
		if len(hits) == 0 {
			return registry.ToolResult{
				Summary: "No archived turns matched",
				Content: fmt.Sprintf("Nothing in this session's archived history matches %q.", args.Query),
			}, nil
		}

		var b strings.Builder
		for _, hit := range hits {
			fmt.Fprintf(&b, "--- generation %d, turn %d (%s) ---\n%s\n\n",
				hit.GenerationSeq, hit.Turn.TurnSeq, hit.Turn.Role, excerpt(hit.Turn.Content))
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Recalled %d archived turn(s)", len(hits)),
			Content: strings.TrimSpace(b.String()),
		}, nil
	}
	return tool
}

func excerpt(content string) string {
	if len(content) <= recallExcerptChars {
		return content
	}
	return content[:recallExcerptChars] + "\n...[truncated]"
}
```

- [ ] **Step 4: Register the tool conditionally**

In `internal/tools/native/native.go`, after the `if tools.webEnabled { ... }` block in `RegisterAll`, add:

```go
	if recallToolEnabled(tools.config.Session.Rollover) {
		all = append(all, tools.recallHistoryTool())
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tools/native/ -run TestRecallToolEnabled -v`
Expected: PASS, all six subtests

- [ ] **Step 6: Verify the package is clean**

Run: `gofmt -w . && go vet ./internal/tools/native/ && go test ./internal/tools/native/`
Expected: no vet output, `ok  	marshal/internal/tools/native`. Existing tests build a `toolSet` with a zero-value config, so `recall_history` stays unregistered and no tool-count assertion changes.

- [ ] **Step 7: Commit**

```bash
git add internal/tools/native/recall.go internal/tools/native/recall_test.go internal/tools/native/native.go
git commit -m "feat(tools): add recall_history tool gated on rollover configuration"
```

---

## Task 15: Shared history rendering and the `/history` command

**Files:**
- Create: `internal/history/render.go`
- Create: `internal/history/render_test.go`
- Create: `internal/commands/history.go`
- Modify: `internal/commands/commands.go` (register the command)

**Interfaces:**
- Consumes: `db.Generation`, `db.ArchivedTurn`, `db.SearchHit`, `db.GenerationsForSession`, `db.TurnsForGeneration`, `db.GenerationTurnCount`, `db.SearchArchivedTurns` (Tasks 2–3).
- Produces:
  - `type Store interface { GenerationsForSession(sessionID string) ([]db.Generation, error); TurnsForGeneration(generationID string) ([]db.ArchivedTurn, error); GenerationTurnCount(generationID string) (int, error); SearchArchivedTurns(sessionID, query, generationID string, limit int) ([]db.SearchHit, error) }`
  - `func ListGenerations(s Store, sessionID string) (string, error)`
  - `func DumpGeneration(s Store, sessionID string, seq int) (string, error)`
  - `func Search(s Store, sessionID, query string, limit int) (string, error)`

  Task 16 consumes all three renderers, so the CLI and the slash command cannot drift.

- [ ] **Step 1: Write the failing test**

Create `internal/history/render_test.go`:

```go
package history

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
)

type fakeStore struct {
	gens   []db.Generation
	turns  map[string][]db.ArchivedTurn
	counts map[string]int
	hits   []db.SearchHit
}

func (f fakeStore) GenerationsForSession(string) ([]db.Generation, error) { return f.gens, nil }
func (f fakeStore) TurnsForGeneration(id string) ([]db.ArchivedTurn, error) {
	return f.turns[id], nil
}
func (f fakeStore) GenerationTurnCount(id string) (int, error) { return f.counts[id], nil }
func (f fakeStore) SearchArchivedTurns(string, string, string, int) ([]db.SearchHit, error) {
	return f.hits, nil
}

func testStore() fakeStore {
	at := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	ended := at.Add(time.Hour)
	return fakeStore{
		gens: []db.Generation{
			{ID: "gen-0", Seq: 0, StartedAt: at, EndedAt: &ended, EndReason: "rollover"},
			{ID: "gen-1", Seq: 1, StartedAt: ended, SeedDigest: "you were at step 4", DigestSource: "llm_summary"},
		},
		turns: map[string][]db.ArchivedTurn{
			"gen-0": {
				{TurnSeq: 0, Role: "user", Content: "add rollover", CreatedAt: at},
				{TurnSeq: 1, Role: "assistant", Content: "on it", CreatedAt: at},
			},
		},
		counts: map[string]int{"gen-0": 2, "gen-1": 0},
		hits: []db.SearchHit{
			{GenerationSeq: 0, GenerationID: "gen-0",
				Turn: db.ArchivedTurn{TurnSeq: 0, Role: "user", Content: "add rollover", CreatedAt: at}},
		},
	}
}

func TestListGenerationsShowsCountsAndDigests(t *testing.T) {
	got, err := ListGenerations(testStore(), "sess-1")
	if err != nil {
		t.Fatalf("ListGenerations failed: %v", err)
	}
	for _, want := range []string{"gen 0", "gen 1", "2 turns", "rollover", "you were at step 4", "live"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ListGenerations output missing %q:\n%s", want, got)
		}
	}
}

func TestDumpGenerationRendersTheTranscript(t *testing.T) {
	got, err := DumpGeneration(testStore(), "sess-1", 0)
	if err != nil {
		t.Fatalf("DumpGeneration failed: %v", err)
	}
	for _, want := range []string{"user", "add rollover", "assistant", "on it"} {
		if !strings.Contains(got, want) {
			t.Fatalf("DumpGeneration output missing %q:\n%s", want, got)
		}
	}
}

func TestDumpGenerationRejectsAnUnknownSequence(t *testing.T) {
	if _, err := DumpGeneration(testStore(), "sess-1", 7); err == nil {
		t.Fatal("DumpGeneration for an unknown generation returned nil error, want an error")
	}
}

func TestSearchRendersHitsWithTheirGeneration(t *testing.T) {
	got, err := Search(testStore(), "sess-1", "rollover", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if !strings.Contains(got, "gen 0") || !strings.Contains(got, "add rollover") {
		t.Fatalf("Search output missing the hit and its generation:\n%s", got)
	}
}

func TestSearchReportsNoMatches(t *testing.T) {
	s := testStore()
	s.hits = nil
	got, err := Search(s, "sess-1", "nothing", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if !strings.Contains(got, "No archived turns") {
		t.Fatalf("Search output for zero hits = %q, want an explicit no-matches message", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/history/ -v`
Expected: FAIL — no Go files in `internal/history`

- [ ] **Step 3: Write the implementation**

Create `internal/history/render.go`:

```go
// Package history renders a session's archived rollover generations for
// human consumption. Both `marshal history` and the /history slash command
// call these functions, so the two surfaces cannot drift apart.
package history

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/db"
)

// Store is the read side of the generation archive. *db.DB satisfies it.
type Store interface {
	GenerationsForSession(sessionID string) ([]db.Generation, error)
	TurnsForGeneration(generationID string) ([]db.ArchivedTurn, error)
	GenerationTurnCount(generationID string) (int, error)
	SearchArchivedTurns(sessionID, query, generationID string, limit int) ([]db.SearchHit, error)
}

// digestPreviewChars keeps a listing scannable when a digest runs long.
const digestPreviewChars = 200

// ListGenerations renders every generation in a session with its turn
// count, lifetime, and seed digest.
func ListGenerations(s Store, sessionID string) (string, error) {
	gens, err := s.GenerationsForSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("list generations: %w", err)
	}
	if len(gens) == 0 {
		return fmt.Sprintf("No archived generations for session %s.", sessionID), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Session %s — %d generation(s)\n\n", sessionID, len(gens))
	for _, g := range gens {
		count, err := s.GenerationTurnCount(g.ID)
		if err != nil {
			return "", fmt.Errorf("count turns for generation %s: %w", g.ID, err)
		}
		status := "live"
		if g.EndedAt != nil {
			status = fmt.Sprintf("ended %s (%s)", g.EndedAt.Format(time.RFC3339), g.EndReason)
		}
		fmt.Fprintf(&b, "gen %d  %d turns  started %s  %s\n",
			g.Seq, count, g.StartedAt.Format(time.RFC3339), status)
		if g.SeedDigest != "" {
			fmt.Fprintf(&b, "        seed (%s): %s\n", g.DigestSource, preview(g.SeedDigest))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// DumpGeneration renders one generation's full archived transcript.
func DumpGeneration(s Store, sessionID string, seq int) (string, error) {
	gens, err := s.GenerationsForSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("load generations: %w", err)
	}
	var target *db.Generation
	for i := range gens {
		if gens[i].Seq == seq {
			target = &gens[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("session %s has no generation %d", sessionID, seq)
	}

	turns, err := s.TurnsForGeneration(target.ID)
	if err != nil {
		return "", fmt.Errorf("load generation %d transcript: %w", seq, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Session %s — generation %d (%s)\n", sessionID, seq, target.ID)
	if target.SeedDigest != "" {
		fmt.Fprintf(&b, "\nSeed digest (%s):\n%s\n", target.DigestSource, target.SeedDigest)
	}
	fmt.Fprintf(&b, "\n%d turn(s):\n\n", len(turns))
	for _, turn := range turns {
		fmt.Fprintf(&b, "[%d] %s  %s\n%s\n",
			turn.TurnSeq, turn.Role, turn.CreatedAt.Format(time.RFC3339), turn.Content)
		if turn.ToolCalls != "" {
			fmt.Fprintf(&b, "tool_calls: %s\n", turn.ToolCalls)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Search renders full-text matches across a session's archived turns.
func Search(s Store, sessionID, query string, limit int) (string, error) {
	hits, err := s.SearchArchivedTurns(sessionID, query, "", limit)
	if err != nil {
		return "", fmt.Errorf("search archived turns: %w", err)
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No archived turns match %q.", query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d match(es) for %q\n\n", len(hits), query)
	for _, hit := range hits {
		fmt.Fprintf(&b, "gen %d, turn %d (%s)  %s\n%s\n\n",
			hit.GenerationSeq, hit.Turn.TurnSeq, hit.Turn.Role,
			hit.Turn.CreatedAt.Format(time.RFC3339), preview(hit.Turn.Content))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func preview(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= digestPreviewChars {
		return s
	}
	return s[:digestPreviewChars] + "…"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/history/ -v`
Expected: PASS, all five tests

- [ ] **Step 5: Add the slash command**

Create `internal/commands/history.go`:

```go
package commands

import (
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/history"
)

// historyHandler backs /history. It reads the current session's archive:
// with no arguments it lists generations, with a number it dumps that
// generation, and with a query it searches.
func historyHandler(state *session.State, args []string) string {
	database := state.DB()
	if database == nil {
		return "No database available to read session history."
	}
	sessionID := state.SessionID()

	if len(args) == 0 {
		out, err := history.ListGenerations(database, sessionID)
		if err != nil {
			return fmt.Sprintf("Could not list generations: %s", err)
		}
		return out
	}

	if seq, err := strconv.Atoi(args[0]); err == nil {
		out, derr := history.DumpGeneration(database, sessionID, seq)
		if derr != nil {
			return fmt.Sprintf("Could not dump generation %d: %s", seq, derr)
		}
		return out
	}

	out, err := history.Search(database, sessionID, strings.Join(args, " "), 25)
	if err != nil {
		return fmt.Sprintf("Could not search history: %s", err)
	}
	return out
}
```

- [ ] **Step 6: Register the command**

In `internal/commands/commands.go`, add to the `commands` slice (in the `groupSettings` group):

```go
		{
			Name:        "history",
			Description: "List archived conversation generations, dump one, or search them",
			Args:        "[<generation-number> | <search query>]",
			Group:       groupSettings,
			Handler:     historyHandler,
		},
```

- [ ] **Step 7: Run the command tests**

Run: `go test ./internal/commands/ ./internal/history/`
Expected: `ok` for both packages

- [ ] **Step 8: Verify everything is clean**

Run: `gofmt -w . && go vet ./internal/history/ ./internal/commands/`
Expected: no output

- [ ] **Step 9: Commit**

```bash
git add internal/history/ internal/commands/history.go internal/commands/commands.go
git commit -m "feat(history): add archive renderers and the /history slash command"
```

---

## Task 16: `marshal history` CLI subcommand

**Files:**
- Create: `cmd/marshal/history.go`
- Create: `cmd/marshal/history_test.go`
- Modify: `cmd/marshal/main.go`

**Interfaces:**
- Consumes: `history.ListGenerations`, `history.DumpGeneration`, `history.Search` (Task 15); `db.Open`, `db.Path` (`internal/db/paths.go:7`).
- Produces: `func runHistory(ctx context.Context, args []string, stdout io.Writer) error` and `var historyRunner = runHistory` (a test seam matching the existing `appRunner` / `acpRunner` pattern in `cmd/marshal/main.go:13`).

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/history_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunDispatchesHistorySubcommand(t *testing.T) {
	original := historyRunner
	t.Cleanup(func() { historyRunner = original })

	var gotArgs []string
	sentinel := errors.New("history ran")
	historyRunner = func(_ context.Context, args []string, _ io.Writer) error {
		gotArgs = args
		return sentinel
	}

	err := run(context.Background(), []string{"history", "sess-1", "--generation", "2"},
		strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("run returned %v, want the history runner's error", err)
	}
	if strings.Join(gotArgs, " ") != "sess-1 --generation 2" {
		t.Fatalf("history args = %v, want the subcommand's own arguments", gotArgs)
	}
}

func TestRunHistoryRequiresASessionID(t *testing.T) {
	err := runHistory(context.Background(), nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("runHistory with no session id returned nil error, want a usage error")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Fatalf("error = %v, want it to mention the missing session id", err)
	}
}
```

Add `"io"` to that file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestRunHistory -v`
Expected: FAIL — `undefined: historyRunner`

- [ ] **Step 3: Write the implementation**

Create `cmd/marshal/history.go`:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"marshal/internal/db"
	"marshal/internal/history"
)

// runHistory implements `marshal history <session-id>`, the read-only view
// of a session's archived rollover generations. It opens the project
// database directly rather than starting a session, so an archive can be
// inspected without launching the TUI — which is exactly when you want it,
// while debugging why a rollover fired.
func runHistory(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stdout)
	generation := fs.Int("generation", -1, "dump this generation's full transcript")
	search := fs.String("search", "", "full-text search across archived turns")
	allSessions := fs.Bool("all-sessions", false, "with --search, search every session, not just this one")
	limit := fs.Int("limit", 25, "maximum search results")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("marshal history: a session id is required (usage: marshal history <session-id> [--generation N] [--search QUERY])")
	}
	sessionID := rest[0]

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	database, err := db.Open(db.Path(workingDir))
	if err != nil {
		return fmt.Errorf("open project database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate project database: %w", err)
	}

	var out string
	switch {
	case *search != "":
		scope := sessionID
		if *allSessions {
			scope = ""
		}
		out, err = history.Search(database, scope, *search, *limit)
	case *generation >= 0:
		out, err = history.DumpGeneration(database, sessionID, *generation)
	default:
		out, err = history.ListGenerations(database, sessionID)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, out)
	return nil
}
```

- [ ] **Step 4: Wire the dispatch**

In `cmd/marshal/main.go`, add the seam variable next to the existing ones:

```go
var historyRunner = runHistory
```

and add the branch inside `run`, before the ACP branch:

```go
	if len(args) > 0 && args[0] == "history" {
		return historyRunner(ctx, args[1:], stdout)
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -v`
Expected: PASS, both new tests and any pre-existing ones

- [ ] **Step 6: Verify the binary builds**

Run: `gofmt -w . && go vet ./cmd/marshal/ && go build ./cmd/marshal`
Expected: no output, a `marshal` binary produced

- [ ] **Step 7: Commit**

```bash
git add cmd/marshal/history.go cmd/marshal/history_test.go cmd/marshal/main.go
git commit -m "feat(cli): add marshal history subcommand for the generation archive"
```

---

## Task 17: Wire rollover into app startup

**Files:**
- Modify: `internal/app/app.go` (around lines 421–452)
- Modify: `internal/agent/rollover.go` (add `DigestChat`)
- Create: `internal/app/rollover_wiring_test.go`

**Interfaces:**
- Consumes: everything from Tasks 4–13.
- Produces: `func (r *Runner) DigestChat(ctx context.Context, msgs []schema.ChatMessage) (string, error)` and a fully wired `*rollover.Controller` on the runner when `cfg.Session.Rollover.Enabled` is true.

- [ ] **Step 1: Write the failing test**

Create `internal/app/rollover_wiring_test.go`:

```go
package app

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/rollover"
)

func TestRolloverPolicyFromConfig(t *testing.T) {
	got := rolloverPolicyFromConfig(config.RolloverConfig{
		Policy:                  "context_percent",
		ContextPercentThreshold: 65,
		TurnCountThreshold:      30,
	})
	want := rollover.Policy{Mode: rollover.PolicyContextPercent, ContextPercent: 65, TurnCount: 30}
	if got != want {
		t.Fatalf("rolloverPolicyFromConfig = %+v, want %+v", got, want)
	}
}

func TestRolloverPolicyFromConfigRejectsAnUnknownMode(t *testing.T) {
	// An unrecognised policy must not silently become a real one; Decide
	// returns false for an unknown mode, which disables the trigger.
	got := rolloverPolicyFromConfig(config.RolloverConfig{Policy: "nonsense", TurnCountThreshold: 5})
	if got.Mode == rollover.PolicyContextPercent || got.Mode == rollover.PolicyTurnCount ||
		got.Mode == rollover.PolicyCallerCheckpoint {
		t.Fatalf("unknown policy resolved to %q, want it left unrecognised", got.Mode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestRolloverPolicy -v`
Expected: FAIL — `undefined: rolloverPolicyFromConfig`

- [ ] **Step 3: Add the digest chat helper**

Append to `internal/agent/rollover.go`:

```go
// DigestChat runs a single non-tool model call. It is the Chatter the
// built-in LLM summary digest provider is wired to, exported so app
// wiring can hand it to the rollover controller without that package
// reaching into the runner's unexported chat helpers.
func (r *Runner) DigestChat(ctx context.Context, msgs []schema.ChatMessage) (string, error) {
	p, model := r.Provider, r.Model
	if r.DigestModel != "" {
		model = r.DigestModel
	}
	res, err := r.chatWithRetryNoNativeTools(ctx, p, model, msgs, nil)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
```

and add the field to the `Runner` struct in `internal/agent/runner.go`, next to `Rollover`:

```go
	// DigestModel overrides the model used for rollover digests. Empty
	// means use the session's main model.
	DigestModel string
```

Add `DigestModel` to `CopyFrom` alongside `Rollover`:

```go
	r.DigestModel = other.DigestModel
```

- [ ] **Step 4: Write the wiring**

In `internal/app/app.go`, add this helper near the other private helpers:

```go
// rolloverPolicyFromConfig translates config into a rollover policy. An
// unrecognised policy string is passed through unchanged: rollover.Decide
// returns false for a mode it does not know, so a typo disables the
// trigger rather than silently selecting a different one.
func rolloverPolicyFromConfig(cfg config.RolloverConfig) rollover.Policy {
	return rollover.Policy{
		Mode:           cfg.Policy,
		ContextPercent: cfg.ContextPercentThreshold,
		TurnCount:      cfg.TurnCountThreshold,
	}
}
```

Then, in `buildAgentRunner`, immediately after the `runner.UsageObserver` assignment (around line 426), replace that assignment with one that also feeds the token counter, and construct the controller:

```go
	var usageCounter *rollover.UsageCounter
	if cfg.Session.Rollover.Enabled && database != nil {
		usageCounter = rollover.NewUsageCounter()
	}
	runner.UsageObserver = func(promptTokens, completionTokens int) {
		state.SetTurnUsage(promptTokens + completionTokens)
		if usageCounter != nil {
			usageCounter.Observe(promptTokens)
		}
	}

	if cfg.Session.Rollover.Enabled && database != nil {
		// Close out any generation left open by a previous crash before
		// opening this session's first one.
		if closed, rerr := database.ReconcileOpenGenerations(time.Now().UTC()); rerr != nil {
			state.Logger().Warn("rollover: startup reconciliation failed", "error", rerr)
		} else if closed > 0 {
			state.Logger().Info("rollover: closed generations left open by a previous run", "count", closed)
		}

		runner.DigestModel = cfg.Session.Rollover.DigestModel
		ctrl := &rollover.Controller{
			SessionID:     state.SessionID(),
			Store:         database,
			Counter:       rollover.ResolveCounter(cfg.Session.Rollover.TokenCounter, usageCounter),
			Digest:        rollover.LLMSummaryProvider{Chat: runner.DigestChat},
			Policy:        rolloverPolicyFromConfig(cfg.Session.Rollover),
			BlobThreshold: cfg.Session.Rollover.BlobThresholdBytes,
			Now:           func() time.Time { return time.Now().UTC() },
			NewID:         func() string { return uuid.NewString() },
			Logger:        state.Logger(),
		}
		if serr := ctrl.Start(ctx); serr != nil {
			state.Logger().Error("rollover: could not open the first generation; rollover disabled for this session", "error", serr)
		} else {
			runner.Rollover = ctrl
			id, seq, _ := ctrl.Current()
			state.BeginGeneration(id, seq, "")
			cleanup = append(cleanup, func() { _ = ctrl.Close(context.Background()) })
		}
	}
```

Add `"marshal/internal/rollover"` and `"time"` to the imports if absent. For `NewID`, use whatever UUID helper the file already uses for session ids — check how `state.SessionID()` is generated in `internal/app/session/session.go` and reuse that generator rather than adding a new dependency.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/app/ -run TestRolloverPolicy -v`
Expected: PASS, both tests

- [ ] **Step 6: Verify the whole tree builds and passes**

Run: `gofmt -w . && go vet ./... && go build ./cmd/marshal && go test ./...`
Expected: no vet output, a built binary, and `ok` for every package

- [ ] **Step 7: Manually verify the default is inert**

Run: `go run ./cmd/marshal` in a scratch repo with no `[session.rollover]` config, send one message, then exit and check:

```bash
sqlite3 .marshal/marshal.db "SELECT COUNT(*) FROM session_generations; SELECT COUNT(*) FROM generation_turns;"
```

Expected: `0` and `0` — with rollover disabled, no generation rows are created at all.

- [ ] **Step 8: Manually verify a rollover end to end**

Add to the scratch repo's `.marshal/config.toml`:

```toml
[session.rollover]
enabled = true
policy = "turn_count"
turn_count_threshold = 2
```

Run `go run ./cmd/marshal`, send three messages, exit, then:

```bash
go run ./cmd/marshal history <session-id>
go run ./cmd/marshal history <session-id> --generation 0
go run ./cmd/marshal history <session-id> --search "<a word you typed>"
```

Expected: at least two generations listed with turn counts; generation 0 dumps its full transcript; the search returns the matching turn.

- [ ] **Step 9: Commit**

```bash
git add internal/app/app.go internal/app/rollover_wiring_test.go internal/agent/rollover.go internal/agent/runner.go
git commit -m "feat(app): wire the rollover controller into session startup"
```

---

## Acceptance criteria

Verified by the tests and manual steps above:

- `context_percent` at threshold 70 rolls over when the counter crosses 70% of the model's window, seeding a new generation with an LLM digest, with the prior transcript intact in `generation_turns` — Tasks 6, 8, 12.
- `turn_count` at threshold 40 rolls over on the 41st turn instead — Tasks 6, 8; manual step in Task 17.
- A rollover due mid-tool-call happens after the tool call completes — guaranteed by construction in Task 12: `Due` is consulted only where no tool call can be in flight.
- With rollover disabled (the default), no `session_generations` or `generation_turns` rows are created — Task 17 step 7, plus every pre-existing agent test running with `Rollover == nil`.
- Oversized tool output lands in `content_ref` with `content` NULL and is still findable by search — Tasks 2 and 3.
- A three-generation session lists all three with turn counts and digests, each individually dumpable — Tasks 15 and 16.

## Spec edge cases that need no code here

- **"Rollover requested but no safe boundary is reached for a long time."** The spec asks for a grace period and a warning. It does not apply to this design: the only place `Due` is consulted is the end of `RunTask`, which is reached on every path including cancellation and error, so a pending request always resolves at the next turn boundary. There is no state in which a request can hang indefinitely, and therefore no grace period to configure. If a future caller consults `Due` from somewhere a tool call *can* be in flight, that guarantee is lost and the grace period becomes necessary — note it in the review of any such change.
- **"Structured digest provider references state that has since changed."** Explicitly out of scope per the spec: a digest provider owns the accuracy of its own output as of rollover time.

## Deferred (P2, explicitly not in this plan)

- Retention pruning beyond `retention = "forever"`. The setting is parsed and stored but only `forever` is honoured. Note that pruning a contentless FTS5 table requires the `INSERT INTO ... VALUES('delete', rowid, body)` form, and blob rows would need reference counting before deletion.
- A built-in structured digest provider for Marshal's own loops. The `DigestProvider` seam exists (Task 7); nothing ships against it besides `LLMSummaryProvider`.
- sdd2 orchestrator integration. `caller_checkpoint` and `Controller.RequestRollover` exist and are tested, but nothing calls `RequestRollover` yet — that is the sdd2 pipeline's job when it is built.
- A calibration pass comparing `EstimatorCounter` against real provider usage. `UsageCounter` already reports the larger of the two, which bounds the error in the safe direction; the measurement itself is still worth doing.
