# Milestone N: Knowledge Agent v1 Design

## Goal

Milestone N gives Marshal a persistent project memory. At the end of each session, a cheap "knowledge" model call summarizes what happened and extracts durable facts/architecture notes about the project, along with summaries of files touched during the session. These memories are surfaced in a new TUI browser (manual stale-marking) and fed back into future context packs, so repeated work on a project benefits from what earlier sessions learned.

## Scope

In scope:

- Summarize the session transcript at session end and store it on `agent_sessions`.
- Extract durable project memories (`fact` / `architecture` / `decision` kinds) with a confidence state (`tentative` / `confirmed` / `stale`).
- Summarize files touched during the session (via the existing audit log's `FilesChanged`), storing the summary on the file's index row.
- A memory browser overlay in the TUI, opened with `Ctrl+M`, supporting manual stale-toggling.
- Feed non-stale memories into context packs via a new `memory` section.

Out of scope:

- Automatic stale detection (e.g. contradiction detection between memories, or detecting when a memory no longer matches current code). Staleness is manually toggled only, per the Milestone N checklist.
- Promoting `tentative` → `confirmed` automatically across sessions (e.g. by reinforcement count). All new memories start `tentative`; promotion to `confirmed` and demotion to `stale` are both manual, one-way actions in the browser (see TUI Memory Browser section) — there is no automatic or undo transition between states.
- Whole-repo hash-drift scanning for changed files. File summarization is scoped to files this session's tool calls actually touched (`AuditEvent.FilesChanged`), not a fresh repo-wide diff.
- Onboarding brief generation, test-failure notes (Phase 4 roadmap items deferred to a later milestone).
- Periodic/mid-session summarization. The knowledge pass runs exactly once, at session end.
- Any change to `repo.index`'s full-replace scan cadence beyond preserving summaries across unchanged hashes.

## Package Placement

New top-level package `internal/knowledge/`, sibling to `internal/agent` and `internal/contextpack`. It owns the knowledge-extraction prompt, response parsing, and the `EndSession` orchestration function. It depends on `internal/db`, `internal/app/session`, `internal/llm/routing`/`provider`/`schema` — the same dependency shape `internal/agent` has today, but kept separate because it is a once-per-session lifecycle rather than part of the per-turn `Runner`.

New TUI package `internal/app/tui/memory/`, structurally identical to `internal/app/tui/settings/`.

## Schema

Add to `internal/db/migrations.go`'s `schema` string:

```sql
CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,             -- 'fact' | 'architecture' | 'decision'
    content TEXT NOT NULL,
    confidence TEXT NOT NULL,       -- 'tentative' | 'confirmed' | 'stale'
    source_session_id TEXT REFERENCES agent_sessions(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
```

This is a new table — no `ALTER TABLE` handling needed.

`files.summary` is a new column added to the existing `files` table via `DB.Migrate()`'s backward-compatible `ALTER TABLE ... ADD COLUMN` step (`tableColumns()`/`PRAGMA table_info` check, same as prior column additions), not a new table.

`agent_sessions.ended_at` and `agent_sessions.summary` already exist in the schema (added speculatively in Milestone I) but have no Go write path yet — this milestone adds it.

## Core Types

`internal/db/memories.go`:

```go
type Memory struct {
    ID              int64
    Kind            string // "fact", "architecture", "decision"
    Content         string
    Confidence      string // "tentative", "confirmed", "stale"
    SourceSessionID string
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

// SaveMemory inserts a new memory row with confidence "tentative".
func (db *DB) SaveMemory(projectID int64, kind, content, sourceSessionID string, now time.Time) error

// GetMemories returns all memory rows for a project, ordered by id.
func (db *DB) GetMemories(projectID int64) ([]Memory, error)

// SetMemoryConfidence updates a single memory's confidence state.
func (db *DB) SetMemoryConfidence(id int64, confidence string, now time.Time) error
```

`internal/db/files.go` changes:

```go
type FileIndex struct {
    Path          string
    Language      string
    Hash          string
    SizeBytes     int64
    LastIndexedAt time.Time
    Summary       string // new
}

// SaveFileIndex behavior change: before deleting existing rows, it loads the
// current index and carries forward Summary for any path+hash unchanged from
// before. Rows with a changed hash (or new paths) get an empty Summary,
// which the knowledge pass fills in later via UpdateFileSummary.
func (db *DB) SaveFileIndex(projectID int64, files []FileIndex) error

// UpdateFileSummary sets Summary for a single existing file row, without
// touching hash/language/size/last_indexed_at.
func (db *DB) UpdateFileSummary(projectID int64, path, summary string) error
```

`internal/db/sessions.go` addition:

```go
// EndSession sets ended_at and summary on an existing session row.
func (db *DB) EndSession(sessionID string, endedAt time.Time, summary string) error
```

`internal/knowledge/knowledge.go`:

```go
// RouteResolver is declared locally rather than imported from
// internal/agent, even though it is structurally identical to
// agent.RouteResolver: internal/agent's MemoryProvider (see Context Pack
// Integration below) needs contextpack.MemoryNote, and internal/knowledge
// needs a route resolver, so a shared type in either direction would create
// an import cycle between the two packages. Go's structural interfaces mean
// *routedProviderResolver (internal/app/app.go) satisfies both without
// either package importing the other.
type RouteResolver interface {
    Resolve(task routing.TaskProfile) (routing.Route, provider.Provider, error)
}

type EndSessionInput struct {
    DB            *db.DB
    ProjectID     int64
    SessionID     string
    State         *session.State
    RouteResolver RouteResolver
    WorkingDir    string
    Now           func() time.Time
    Logger        *slog.Logger
}

// EndSession summarizes the session, extracts memories, and summarizes
// session-touched files. Best-effort: errors are logged, never returned to
// the caller, and never block process exit. No-ops if the session has no
// user messages.
func EndSession(ctx context.Context, in EndSessionInput)
```

`internal/knowledge/protocol.go` (mirrors `agent/protocol.go`'s `ParseAction`):

```go
type Extraction struct {
    SessionSummary string
    Memories       []MemoryNote
    FileSummaries  map[string]string // path -> one-line summary
}

// ParseExtraction extracts and validates the single JSON object the
// knowledge prompt instructs the model to return. Tolerates a leading/
// trailing ```json fence, same as ParseAction.
func ParseExtraction(raw string) (Extraction, error)
```

Response JSON shape:

```json
{
  "session_summary": "short paragraph",
  "memories": [
    {"kind": "fact", "content": "..."},
    {"kind": "architecture", "content": "..."}
  ],
  "file_summaries": {
    "internal/foo/bar.go": "one-line summary of what this file does"
  }
}
```

`Extraction.Memories` uses a `MemoryNote{Kind, Content string}` type declared in `internal/knowledge` itself — this is a separate, identically-shaped type from `contextpack.MemoryNote` (see Context Pack Integration), not a shared one, for the same import-cycle reason `RouteResolver` above is declared locally rather than imported.

## Data Flow

1. `internal/app/app.go`'s `Run()`, immediately after `runOpts.programRunner(...)` returns (TUI has exited, by any path) and before the deferred `database.Close()` fires, calls `knowledge.EndSession(context.Background(), ...)`.
2. `EndSession` returns immediately if `state.Messages()` contains no user messages.
3. It resolves a provider/model via `RouteResolver.Resolve(routing.TaskProfile{Class: "knowledge"})`. This requires a small change to `internal/llm/routing/router.go`'s `roleForTaskClass`, which today only maps task classes to `RoleRepoScout`/`RoleImplementer`: add a `"knowledge"` class mapping to `routing.RoleKnowledge` (a role constant that already exists but is currently unused by the router). If the active profile has no `RoleKnowledge` entry, `StaticRouter`'s existing implementer-role fallback (`router.go:34-36`) applies unchanged.
4. It builds one prompt (`internal/knowledge/prompts.go`) containing: the message transcript (`state.Messages()`), the audit log's tool names/result summaries (`state.AuditLog()`), and the distinct file paths from `AuditEvent.FilesChanged` across the session's audit log, with those files' current content included.
5. One non-streaming `provider.Chat` call is made (same call shape as `Runner.chatOnce`, no tool loop).
6. `ParseExtraction` parses the response. On parse failure: log and return (no retry — this is background best-effort work).
7. Persist, in order: `db.EndSession(sessionID, now, extraction.SessionSummary)`; one `db.SaveMemory` call per extracted memory; one `db.UpdateFileSummary` call per entry in `FileSummaries` whose path was actually touched this session (entries for paths outside the session's `FilesChanged` set are ignored, guarding against the model summarizing files it merely read).

## Context Pack Integration

`internal/contextpack/contextpack.go`: add `SectionMemory SectionKind = "memory"` and:

```go
// MemoryNote is contextpack's own view of a durable memory — just enough
// to render a section. It is declared here (not imported from
// internal/knowledge) so that internal/agent, which already depends on
// contextpack, never needs to depend on internal/knowledge (see the
// import-cycle note in the Core Types section).
type MemoryNote struct {
    Kind    string
    Content string
}
```

`internal/contextpack/builder.go`: add

```go
// MergeMemories replaces any existing memory section in pack with a single
// new section built from non-stale memories (joined newline-separated,
// title "Project Memories", priority 15 — above plan/file_snippet/
// tool_output, below repo_card), then rebuilds within the pack's existing
// token budget. Mirrors RefreshPlanWithBudget's replace-and-rebuild shape.
func MergeMemories(pack Pack, memories []MemoryNote, maxTokens int, now func() time.Time) Pack
```

`internal/agent/runner.go`: `Runner` gains a field:

```go
type MemoryProvider interface {
    Memories(projectID int64) ([]contextpack.MemoryNote, error)
}

type Runner struct {
    // ...existing fields...
    MemoryProvider MemoryProvider
    ProjectID      int64
}
```

At the top of `Run`, before the first `messages` build, if `r.MemoryProvider != nil`, fetch memories and merge them into `r.State.ContextPack()` via `contextpack.MergeMemories`, then `r.State.SetContextPack(...)` — same pattern the plan-refresh step later in `Run` already uses.

`internal/app/app.go`'s `buildAgentRunner` wires a thin adapter (filtering out `stale` confidence, converting `db.Memory` to `contextpack.MemoryNote`) as `runner.MemoryProvider`, and sets `runner.ProjectID`. This adapter is the one place that needs both `db.Memory` and `contextpack.MemoryNote` in scope, which is fine since `app.go` already imports both `internal/db` and (transitively, via `agent`) `internal/contextpack`.

## TUI Memory Browser

`internal/app/tui/memory/model.go`:

```go
type Model struct {
    db        *db.DB
    projectID int64
    memories  []db.Memory
    cursor    int
}

func New(database *db.DB, projectID int64) Model // loads memories on construction
func (m Model) Init() tea.Cmd
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (m Model) View() string
```

Keybindings inside the browser: `↑`/`k` and `↓`/`j` move the cursor; `c` sets the selected memory's confidence to `confirmed`; `s` sets it to `stale`; both write immediately via a direct `db.SetMemoryConfidence` call (no buffered save/cancel — unlike the settings form, each keypress is a single immediate action) and update the in-memory row so the browser reflects the change without a reload; `Esc` closes the browser. There is no toggle/undo — `s` and `c` are two independent one-way actions, not a pair that reverses each other, which avoids needing to track or persist a memory's "prior" confidence state.

`internal/app/tui/memory/messages.go`: `ClosedMsg{}`.

`internal/app/tui/model.go` wiring (mirrors the `settingsOpen`/`settingsModel` pattern at lines 35-36, 104-117, 125-133, 200-203, 269-271):

- New fields `memoryOpen bool`, `memoryModel memory.Model`.
- New keybinding `Ctrl+M` (unused today) opens it: `m.memoryModel = memory.New(database, projectID); m.memoryOpen = true`.
- When `memoryOpen`, `Update` short-circuits key handling to `m.memoryModel.Update(msg)`, same as the settings short-circuit.
- `View()` returns `m.memoryModel.View()` early when `memoryOpen`, same as settings.
- `memory.ClosedMsg` sets `memoryOpen = false`.

`tui.Model` needs access to `*db.DB`/`projectID` for this, which it does not hold today (only `settings.Model` needed `config`/paths). Add a `WithMemoryStore(database *db.DB, projectID int64) Option` following the existing `WithConfigReloader`/`WithRunner` functional-option pattern, wired from `app.go`'s `Run()` alongside the other `tuiOpts`.

## Error Handling

- `EndSession` never returns an error to its caller; every internal failure (route resolution, provider call, parse failure, DB write failure) is logged via `Logger` and the function returns, having persisted whatever succeeded before the failure. A failed knowledge pass must never make Marshal's exit look like a crash.
- Session-end skip (no user messages) is not an error — it is normal behavior for a session where the user opened Marshal and quit immediately.
- Malformed model responses (fails `ParseExtraction`) are logged and dropped entirely — no partial extraction, no retry.
- `db.UpdateFileSummary` for a path not present in the file index (e.g. a file the model hallucinated) is a no-op (`0` rows affected), not an error.
- Memory browser actions (`s`/`c`) against a DB write failure: log via the existing session logger pattern and leave the in-memory list unchanged, matching the settings model's existing `footer` error-display convention.

## Testing

- `internal/db/memories_test.go` (new): `SaveMemory`/`GetMemories` roundtrip; `SetMemoryConfidence` transitions; ordering.
- `internal/db/files_test.go`: extend for summary carry-forward on unchanged hash, summary clearing on changed hash, `UpdateFileSummary` roundtrip and no-op-on-missing-path.
- `internal/db/sessions_test.go`: extend for `EndSession` setting `ended_at`/`summary`.
- `internal/knowledge/protocol_test.go` (new): valid extraction parsing, fenced-JSON tolerance, malformed JSON, missing fields.
- `internal/knowledge/prompts_test.go` (new): prompt includes transcript, audit summaries, and session-touched file content.
- `internal/knowledge/knowledge_test.go` (new): `EndSession` against a fake provider + in-memory DB — happy path persists summary/memories/file-summaries; empty-session skip; provider error is swallowed and logged; parse-failure is swallowed and logged; a file-summary entry for a path outside `FilesChanged` is ignored.
- `internal/contextpack/contextpack_test.go`: extend for `MergeMemories` (budget truncation, replace-on-rebuild, empty-memories no-op).
- `internal/agent/runner_test.go`: extend for the `MemoryProvider` merge call at the top of `Run` (memories present vs. `MemoryProvider` nil).
- `internal/app/tui/memory/model_test.go` (new), `view_test.go` (new): mirror `settings/model_test.go`'s style — cursor movement, stale/confirm toggling, close message.
- `internal/app/tui/model_test.go`: extend for `Ctrl+M` open/close short-circuit, mirroring existing settings-overlay tests.

## Acceptance Criteria

- `go build ./cmd/marshal` and `go test ./...` succeed.
- Milestone N checklist (`docs/10-mvp-implementation-checklist.md`) is fully checked.
- Ending a session with at least one user message produces a session summary, zero or more memories, and zero or more file summaries for session-touched files, all queryable from the DB afterward.
- Ending a session with no user messages performs no LLM call and no writes beyond what already existed.
- `Ctrl+M` opens a memory browser listing all non-deleted memories for the project; `s` marks the selected memory stale, `c` marks it confirmed; `Esc` closes it.
- A subsequent session's context pack includes a `memory` section built from non-stale memories, subject to the pack's token budget.
- A failed knowledge-pass LLM call does not change Marshal's process exit code or produce a visible error to the user.
