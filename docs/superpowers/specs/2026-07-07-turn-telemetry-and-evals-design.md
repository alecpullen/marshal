# Turn Telemetry and Eval Scenarios — Design

**Goal:** Make every agent turn measurable (outcome, iterations, parse failures, stalls, tokens) and encode the loop's expected behavior as deterministic eval scenarios, so subsequent loop improvements (parse budget, error feedback, compaction, ask_user, structured output) are verifiable instead of guesswork.

**Status:** Approved 2026-07-07. First of four sub-projects (telemetry/evals → reliability trio → ask_user → native structured output).

## 1. Collection: `TurnMetrics` in `internal/agent`

New file `internal/agent/metrics.go`:

```go
// TurnMetrics summarises one RunTask execution. Emitted exactly once per
// turn via Runner.MetricsObserver, including on error exits.
type TurnMetrics struct {
	StartedAt        time.Time
	DurationMs       int64
	Goal             string // truncated to 200 chars
	Class            string // TaskClass at execution time
	Role             string // AgentRole (general, planner, ...)
	Provider         string // resolved route provider name ("" if default)
	Model            string // resolved model for the turn
	Iterations       int    // loop iterations consumed
	ToolCalls        int    // tool executions (incl. cached + errored)
	ToolErrors       int    // tool executions that returned an error message
	CacheHits        int    // turn-cache hits
	ParseFailures    int    // ParseAction failures in the main loop
	SoftStalls       int    // stalling assessments (nudges issued)
	HardStalls       int    // hard-stall assessments (forced finalize)
	Outcome          string // "answered" | "salvaged" | "failed"
	SalvageReason    string // "" | "stalled" | "exhausted"
	PromptTokens     int
	CompletionTokens int
}
```

- `Runner` gains `MetricsObserver func(TurnMetrics)` beside the existing `UsageObserver`. Nil-safe: when nil, no collection cost beyond counter increments. All existing constructions compile unchanged.
- A per-turn `turnStats` value is created at the top of `RunTask` (like the tracker) and emitted via `defer` so every exit path — final answer, finalize salvage, provider failure, exhaustion failure, context cancellation — produces exactly one emission.
- Outcome mapping: `TaskStatusCompleted` with empty `SalvagedReason` → `answered`; completed with non-empty `SalvagedReason` → `salvaged`; everything else → `failed`.
- Collection points (all already exist in the code):
  - parse-error branch in the loop → `ParseFailures++` (single goroutine, no lock)
  - `maybeFinalizeOnStall` → `SoftStalls++` / `HardStalls++` (single goroutine)
  - `executeToolCall` exits → `ToolCalls++`, plus `ToolErrors++` or `CacheHits++`; incremented inside the existing `trackerMu` critical sections because `executeActions` runs tool calls in goroutines
  - `chatOnce` usage → accumulate `PromptTokens`/`CompletionTokens` (all chat calls happen on the RunTask goroutine: planning, loop, finalize)
- Swarm sub-runners emit their own rows with `Role` set — per-role measurement for free. Truncation: goal is cut at 200 chars on rune boundaries.

## 2. Persistence: `turn_metrics` table in `internal/db`

Appended to `internal/db/migrations.go` (idempotent, matching existing pattern):

```sql
CREATE TABLE IF NOT EXISTS turn_metrics (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        INTEGER NOT NULL REFERENCES projects(id),
    session_id        INTEGER REFERENCES agent_sessions(id),
    started_at        TEXT NOT NULL,
    duration_ms       INTEGER NOT NULL,
    class             TEXT NOT NULL,
    role              TEXT NOT NULL,
    provider          TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    goal              TEXT NOT NULL,
    iterations        INTEGER NOT NULL,
    tool_calls        INTEGER NOT NULL,
    tool_errors       INTEGER NOT NULL,
    cache_hits        INTEGER NOT NULL,
    parse_failures    INTEGER NOT NULL,
    soft_stalls       INTEGER NOT NULL,
    hard_stalls       INTEGER NOT NULL,
    outcome           TEXT NOT NULL,
    salvage_reason    TEXT NOT NULL DEFAULT '',
    prompt_tokens     INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL
);
```

New `internal/db/turnmetrics.go` following the per-table-file pattern (`audits.go`, `memories.go`):
- `type TurnMetricsRow struct` — a plain params/row struct defined in `internal/db` mirroring the table columns, with `ProjectID int64` and `SessionID *int64` (db must not import `internal/agent`; the app layer maps `agent.TurnMetrics` → `db.TurnMetricsRow`).
- `InsertTurnMetrics(row TurnMetricsRow) (int64, error)`.
- `RecentTurnMetrics(projectID int64, limit int) ([]TurnMetricsRow, error)` — newest first.
- `turnmetrics_test.go` covering insert/read round-trip and nullable session_id.

## 3. Wiring: `internal/app/app.go`

`buildAgentRunner` sets `runner.MetricsObserver` to a closure that maps `agent.TurnMetrics` → db params and inserts. Insert errors are logged at warn level and swallowed — telemetry must never break a turn (same tolerance as the memory provider). When no database is configured (tests, ephemeral runs), the observer stays nil. TUI untouched.

## 4. Evals: `internal/agent/eval_scenarios_test.go`

Table-driven scenarios on the existing `scriptedProvider` + fake registry tools, asserting on the `TurnMetrics` captured via `MetricsObserver` (not transcript scraping). Initial table:

| Scenario | Script | Asserted metrics |
|---|---|---|
| research turn | 5 distinct reads, then final | outcome=answered, ParseFailures=0, SoftStalls=0, HardStalls=0, Iterations=6, ToolCalls=5 |
| edit + validate | read, patch, shell.run test, final | outcome=answered, ToolCalls=3, ToolErrors=0 |
| parse-failure recovery | garbage text, then valid final | outcome=answered, ParseFailures=1 |
| exact-repeat stall | same read 3x, finalize answers | outcome=salvaged, SalvageReason=stalled, HardStalls=1 |
| exhaustion salvage | distinct reads past MaxToolIterations, finalize answers | outcome=salvaged, SalvageReason=exhausted |
| tool error recovery | unknown tool, then valid read, then final | outcome=answered, ToolErrors=1 |

These are the regression baseline later sub-projects extend (the reliability trio adds parse-budget scenarios; structured output re-runs the parse scenarios per capability path).

## 5. Edge cases

- Observer emitted exactly once, including panics avoided via plain defer (no recover — a panic still emits nothing new).
- Context cancellation mid-turn → outcome=failed with partial counters.
- `MaxToolIterations` reached → Iterations equals the configured max.
- Goal shorter than 200 chars stored verbatim; truncation never splits a UTF-8 rune.

## 6. Out of scope

- TUI stats view (roadmap item 5 consumes this table later).
- Live-model eval harness.
- Retention/pruning of turn_metrics rows.
- Recording per-tool-call rows (the audit log already covers that).

## 7. Testing summary

- `internal/agent`: metrics assembly unit tests (each collection point) + the six eval scenarios.
- `internal/db`: migration + insert/query round-trip tests.
- `internal/app`: one wiring test that a completed turn produces a row (using the existing app_test.go dependency-injection seams).
