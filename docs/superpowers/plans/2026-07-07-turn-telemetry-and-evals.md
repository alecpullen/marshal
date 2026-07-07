# Turn Telemetry and Eval Scenarios Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit a `TurnMetrics` record for every agent turn, persist it to a new `turn_metrics` SQLite table, and encode the loop's expected behavior as six deterministic eval scenarios.

**Architecture:** A per-turn `turnStats` collector lives on `Runner` (mirroring the existing `tracker`/`trackerMu` pattern) and is emitted exactly once per `RunTask` via a nil-safe `MetricsObserver` hook. The app layer maps `agent.TurnMetrics` → `db.TurnMetricsRow` and inserts best-effort (never failing a turn). Evals assert on the emitted metrics, not transcripts.

**Tech Stack:** Go stdlib, existing SQLite layer (`internal/db`), existing `scriptedProvider` test harness.

**Spec:** `docs/superpowers/specs/2026-07-07-turn-telemetry-and-evals-design.md`

## Global Constraints

- Work on branch `turn-telemetry` (create from `main` before Task 1: `git checkout -b turn-telemetry`).
- Build and test with CGO enabled: `CGO_ENABLED=1 go test ./...` (tree-sitter dependency).
- `gofmt -l .` must print nothing and `go vet ./...` must be clean before every commit — EXCEPT the pre-existing `internal/app/app.go:463: assignment copies lock value` vet warning, which is on main and not yours to fix.
- `internal/db` must NOT import `internal/agent` (the app layer maps between their structs).
- Telemetry must never break a turn: observer is nil-safe, insert errors are logged and swallowed.
- Goal text is truncated to 200 runes (never split a UTF-8 rune).
- Outcome strings are exactly `"answered"`, `"salvaged"`, `"failed"`. Salvage reasons are the existing `"stalled"` / `"exhausted"` values from `finalizeReason`.
- The TUI is not touched by this plan.

---

### Task 1: TurnMetrics types and pure helpers

**Files:**
- Create: `internal/agent/metrics.go`
- Test: `internal/agent/metrics_test.go`

**Interfaces:**
- Consumes: `Task` (fields `Status TaskStatus`, `SalvagedReason string`), `TaskStatusCompleted` — all existing in `internal/agent/task.go`.
- Produces (Task 2 relies on these exact names):
  - `type TurnMetrics struct` — fields listed below.
  - `type turnStats struct { m TurnMetrics }` — mutable collector; all synchronization is external (Task 2 guards it with `Runner.statsMu`).
  - `func truncateGoal(goal string, max int) string`
  - `func outcomeFor(task *Task) string`

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/metrics_test.go`:

```go
package agent

import (
	"strings"
	"testing"
)

func TestTruncateGoal(t *testing.T) {
	cases := []struct {
		name string
		goal string
		want string
	}{
		{name: "short goal unchanged", goal: "fix the bug", want: "fix the bug"},
		{name: "exactly 200 runes unchanged", goal: strings.Repeat("a", 200), want: strings.Repeat("a", 200)},
		{name: "long goal truncated to 200 runes", goal: strings.Repeat("a", 250), want: strings.Repeat("a", 200)},
		{
			name: "multibyte runes not split",
			goal: strings.Repeat("é", 250),
			want: strings.Repeat("é", 200),
		},
		{name: "empty goal", goal: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateGoal(tc.goal, 200)
			if got != tc.want {
				t.Fatalf("truncateGoal length = %d runes, want %d", len([]rune(got)), len([]rune(tc.want)))
			}
		})
	}
}

func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		task *Task
		want string
	}{
		{
			name: "completed without salvage is answered",
			task: &Task{Status: TaskStatusCompleted},
			want: "answered",
		},
		{
			name: "completed with salvage reason is salvaged",
			task: &Task{Status: TaskStatusCompleted, SalvagedReason: "stalled"},
			want: "salvaged",
		},
		{
			name: "failed status is failed",
			task: &Task{Status: TaskStatusFailed},
			want: "failed",
		},
		{
			name: "executing (interrupted) is failed",
			task: &Task{Status: TaskStatusExecuting},
			want: "failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcomeFor(tc.task); got != tc.want {
				t.Fatalf("outcomeFor = %q, want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestTruncateGoal|TestOutcomeFor' -v`
Expected: compile error — `undefined: truncateGoal`, `undefined: outcomeFor`. The compile failure is the red state; proceed to Step 3 without writing stubs.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/metrics.go`:

```go
package agent

import "time"

// TurnMetrics summarises one RunTask execution. It is emitted exactly once
// per turn via Runner.MetricsObserver, including on error exits, so every
// turn is measurable: outcome, iterations, parse failures, stalls, tokens.
type TurnMetrics struct {
	StartedAt        time.Time
	DurationMs       int64
	Goal             string // truncated to 200 runes
	Class            string // TaskClass at execution time
	Role             string // AgentRole (general, planner, ...)
	Provider         string // resolved provider name for the turn
	Model            string // resolved model for the turn
	Iterations       int    // loop iterations consumed
	ToolCalls        int    // tool messages fed back to the model (incl. cached and errored)
	ToolErrors       int    // tool calls that returned an error message
	CacheHits        int    // turn-cache hits
	ParseFailures    int    // ParseAction failures in the main loop
	SoftStalls       int    // stalling assessments (nudges issued)
	HardStalls       int    // hard-stall assessments (forced finalize)
	Outcome          string // "answered" | "salvaged" | "failed"
	SalvageReason    string // "" | "stalled" | "exhausted"
	PromptTokens     int
	CompletionTokens int
}

// turnStats is the mutable per-turn collector behind TurnMetrics. It has no
// mutex of its own: Runner guards every access with statsMu (mirroring the
// tracker/trackerMu pattern), because executeActions mutates counters from
// worker goroutines.
type turnStats struct {
	m TurnMetrics
}

// truncateGoal caps goal at max runes without splitting a UTF-8 rune.
func truncateGoal(goal string, max int) string {
	runes := []rune(goal)
	if len(runes) <= max {
		return goal
	}
	return string(runes[:max])
}

// outcomeFor maps a finished task to the metrics outcome vocabulary. Any
// status other than completed (failed, or executing after an interrupt)
// counts as failed.
func outcomeFor(task *Task) string {
	switch {
	case task.Status == TaskStatusCompleted && task.SalvagedReason == "":
		return "answered"
	case task.Status == TaskStatusCompleted:
		return "salvaged"
	default:
		return "failed"
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestTruncateGoal|TestOutcomeFor' -v`
Expected: all PASS.

- [ ] **Step 5: Format, vet, commit**

```bash
gofmt -w internal/agent/metrics.go internal/agent/metrics_test.go
go vet ./internal/agent/...
git add internal/agent/metrics.go internal/agent/metrics_test.go
git commit -m "feat(agent): TurnMetrics types and helpers"
```

---

### Task 2: Collect and emit metrics from the Runner

**Files:**
- Modify: `internal/agent/runner.go` (Runner struct, `RunTask`, `maybeFinalizeOnStall`, `executeToolCall`, `chatOnce`)
- Modify: `internal/agent/metrics.go` (add Runner helper methods)
- Modify: `internal/agent/runner_test.go` (extend `scriptedProvider` with usage emission)
- Test: `internal/agent/metrics_test.go` (integration tests)

**Interfaces:**
- Consumes from Task 1: `TurnMetrics`, `turnStats`, `truncateGoal(goal string, max int) string`, `outcomeFor(task *Task) string`.
- Produces (Tasks 4 and 5 rely on these):
  - `Runner.MetricsObserver func(TurnMetrics)` — public field, nil-safe.
  - `scriptedProvider.usages []*schema.TokenUsage` — optional per-call usage emitted on the Done event.

- [ ] **Step 1: Write the failing integration tests**

Append to `internal/agent/metrics_test.go` (add imports `"context"`, `"errors"`, and `marshal/internal/app/config`, `marshal/internal/llm/schema`, `marshal/internal/tools/policy`, `marshal/internal/tools/registry` to the existing import block):

```go
// registerFakeRead registers a file.read fake; when cacheable is true, the
// turn cache serves repeat calls.
func registerFakeRead(t *testing.T, reg *registry.Registry, cacheable bool) {
	t.Helper()
	if err := reg.Register(registry.Tool{
		Name:      "file.read",
		Risk:      registry.RiskReadOnly,
		Cacheable: cacheable,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func captureMetrics(r *Runner) *TurnMetrics {
	var captured TurnMetrics
	got := &captured
	r.MetricsObserver = func(m TurnMetrics) { *got = m }
	return got
}

func TestRunTaskEmitsMetricsOnAnswer(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "how does pkg work?"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.Outcome != "answered" || m.SalvageReason != "" {
		t.Fatalf("outcome = %q/%q, want answered/\"\"", m.Outcome, m.SalvageReason)
	}
	if m.Iterations != 3 || m.ToolCalls != 2 || m.ToolErrors != 0 || m.ParseFailures != 0 {
		t.Fatalf("counters = %+v, want Iterations=3 ToolCalls=2 ToolErrors=0 ParseFailures=0", *m)
	}
	if m.Class != "question" || m.Role != "general" || m.Model != "test-model" || m.Provider != "scripted" {
		t.Fatalf("identity fields = %+v", *m)
	}
	if m.Goal != "how does pkg work?" || m.StartedAt.IsZero() {
		t.Fatalf("goal/startedAt = %q / %v", m.Goal, m.StartedAt)
	}
}

func TestRunTaskMetricsCountsParseFailures(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		"this is not a json action",
		`{"rationale":"done","action":{"type":"final","content":"Recovered."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.Outcome != "answered" || m.ParseFailures != 1 || m.ToolCalls != 0 {
		t.Fatalf("metrics = %+v, want answered with ParseFailures=1 ToolCalls=0", *m)
	}
}

func TestRunTaskMetricsCountsToolErrorsAndCacheHits(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, true) // cacheable: second identical read is a cache hit
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"missing.tool","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.ToolCalls != 3 || m.CacheHits != 1 || m.ToolErrors != 1 {
		t.Fatalf("metrics = %+v, want ToolCalls=3 CacheHits=1 ToolErrors=1", *m)
	}
}

func TestRunTaskMetricsCountsStalls(t *testing.T) {
	reg := registry.New()
	registerFakeRead(t, reg, false)
	read := `{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`
	p := &scriptedProvider{responses: []string{
		read, read, read,
		`{"rationale":"done","action":{"type":"final","content":"Forced."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.Outcome != "salvaged" || m.SalvageReason != "stalled" || m.HardStalls != 1 {
		t.Fatalf("metrics = %+v, want salvaged/stalled with HardStalls=1", *m)
	}
}

func TestRunTaskMetricsFailedOnProviderError(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{errs: []error{errors.New("boom")}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxRetries = 0
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err == nil {
		t.Fatal("RunTask err = nil, want provider failure")
	}
	if m.Outcome != "failed" {
		t.Fatalf("Outcome = %q, want failed (metrics must emit on error exits)", m.Outcome)
	}
}

func TestRunTaskMetricsAccumulatesTokens(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{
			"garbage that fails to parse",
			`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
		},
		usages: []*schema.TokenUsage{
			{PromptTokens: 10, CompletionTokens: 5},
			{PromptTokens: 7, CompletionTokens: 3},
		},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	m := captureMetrics(r)

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if m.PromptTokens != 17 || m.CompletionTokens != 8 {
		t.Fatalf("tokens = %d/%d, want 17/8 accumulated across calls", m.PromptTokens, m.CompletionTokens)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestRunTaskEmitsMetrics|TestRunTaskMetrics' -v`
Expected: compile error — `r.MetricsObserver undefined`, `unknown field usages`. Red state; proceed.

- [ ] **Step 3: Extend scriptedProvider with usage emission**

In `internal/agent/runner_test.go`, change the `scriptedProvider` struct and its Done event:

```go
type scriptedProvider struct {
	responses []string
	thinking  []string
	errs      []error
	usages    []*schema.TokenUsage
	calls     int
	requests  []schema.ChatRequest
}
```

and replace the two lines `ch <- schema.ChatEvent{Type: schema.ChatEventDone}` / `close(ch)` at the end of `Chat` with:

```go
	done := schema.ChatEvent{Type: schema.ChatEventDone}
	if idx < len(p.usages) {
		done.Usage = p.usages[idx]
	}
	ch <- done
	close(ch)
```

- [ ] **Step 4: Add the Runner fields and helper methods**

In `internal/agent/runner.go`, add to the `Runner` struct after the `UsageObserver UsageObserver` field:

```go
	// MetricsObserver, when set, receives one TurnMetrics per RunTask,
	// emitted on every exit path (answer, salvage, failure). Nil disables
	// collection output; counter bookkeeping still runs.
	MetricsObserver func(TurnMetrics)
```

and after the `trackerMu sync.Mutex` field:

```go
	stats   *turnStats
	statsMu sync.Mutex
```

Append to `internal/agent/metrics.go`:

```go
// withStats runs f with the current turn's collector under statsMu. It is a
// no-op before the first RunTask (stats nil), so direct calls to chatOnce or
// executeToolCall in tests never panic.
func (r *Runner) withStats(f func(*turnStats)) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if r.stats != nil {
		f(r.stats)
	}
}

// countToolCall records one tool message fed back to the model.
func (r *Runner) countToolCall(errored, cached bool) {
	r.withStats(func(s *turnStats) {
		s.m.ToolCalls++
		if errored {
			s.m.ToolErrors++
		}
		if cached {
			s.m.CacheHits++
		}
	})
}

// emitMetrics finalizes the turn's metrics from the finished task and hands
// them to MetricsObserver. Called exactly once per RunTask via defer.
func (r *Runner) emitMetrics(task *Task) {
	if r.MetricsObserver == nil {
		return
	}
	r.statsMu.Lock()
	m := r.stats.m
	r.statsMu.Unlock()
	m.DurationMs = r.Now().Sub(m.StartedAt).Milliseconds()
	m.Class = string(task.Class)
	m.Outcome = outcomeFor(task)
	m.SalvageReason = task.SalvagedReason
	r.MetricsObserver(m)
}
```

- [ ] **Step 5: Wire the collection points in runner.go**

All edits are in `internal/agent/runner.go`.

(a) In `RunTask`, immediately after the tracker initialization block (`r.trackerMu.Unlock()`), add stats initialization, and add the defer right after `task := NewTask(goal, r.Now())`:

```go
	r.statsMu.Lock()
	r.stats = &turnStats{m: TurnMetrics{
		StartedAt: r.Now(),
		Goal:      truncateGoal(goal, 200),
		Role:      string(r.role()),
	}}
	r.statsMu.Unlock()

	task := NewTask(goal, r.Now())
	defer func() { r.emitMetrics(task) }()
```

(the existing `task := NewTask(goal, r.Now())` line moves into this block — do not duplicate it).

(b) Immediately after `turnProvider, turnModel, route := r.resolveRoute(task)`:

```go
	r.withStats(func(s *turnStats) {
		s.m.Provider = turnProvider.Name()
		s.m.Model = turnModel
	})
```

(c) At the top of the tool loop, immediately after the `r.State.SetToolBudget(...)` line:

```go
		r.withStats(func(s *turnStats) { s.m.Iterations = iteration + 1 })
```

(d) In the parse-error branch (`if parseErr != nil { ... }`), before the `continue`:

```go
			r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
```

(e) In `maybeFinalizeOnStall`, inside the `switch a` — first line of `case assessHardStall:`:

```go
		r.withStats(func(s *turnStats) { s.m.HardStalls++ })
```

and first line of `case assessStalling:`:

```go
		r.withStats(func(s *turnStats) { s.m.SoftStalls++ })
```

(f) In `executeToolCall`, add a `r.countToolCall(...)` call immediately before EVERY return that produces a message. There are exactly ten:

| Return site | Call |
|---|---|
| unknown tool (`"unknown tool"`) | `r.countToolCall(true, false)` |
| patch encode failure | `r.countToolCall(true, false)` |
| args not a valid JSON object | `r.countToolCall(true, false)` |
| normalizeArgs failure | `r.countToolCall(true, false)` |
| cached result hit (just before `return ...BuildCachedToolResultMessage...`) | `r.countToolCall(false, true)` |
| policy Evaluate error | `r.countToolCall(true, false)` |
| policy deny | `r.countToolCall(true, false)` |
| user deny (`"denied by user"`) | `r.countToolCall(true, false)` |
| handler execErr | `r.countToolCall(true, false)` |
| success (before the final `return msgs, nil`) | `r.countToolCall(false, false)` |

The approval-wait error path (`return nil, waitErr`) is deliberately NOT counted — it produces no tool message and the turn is ending.

(g) In `chatOnce`, extend the existing usage-observer block:

```go
	if r.UsageObserver != nil && usage != nil {
		r.UsageObserver(usage.PromptTokens, usage.CompletionTokens)
	}
	if usage != nil {
		r.withStats(func(s *turnStats) {
			s.m.PromptTokens += usage.PromptTokens
			s.m.CompletionTokens += usage.CompletionTokens
		})
	}
```

- [ ] **Step 6: Run the new tests, then the whole package**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestRunTaskEmitsMetrics|TestRunTaskMetrics' -v`
Expected: all PASS.

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`
Expected: `ok` for `agent` and `agent/swarm` (nil observer keeps every existing test unaffected).

- [ ] **Step 7: Format, vet, commit**

```bash
gofmt -w internal/agent
go vet ./internal/agent/...
git add internal/agent/runner.go internal/agent/metrics.go internal/agent/metrics_test.go internal/agent/runner_test.go
git commit -m "feat(agent): collect and emit per-turn metrics from the runner"
```

---

### Task 3: Persist metrics — turn_metrics table

**Files:**
- Modify: `internal/db/migrations.go` (append table to the `schema` const)
- Create: `internal/db/turnmetrics.go`
- Test: `internal/db/turnmetrics_test.go`

**Interfaces:**
- Consumes: `db.Open(path string) (*DB, error)`, `(*DB).Migrate() error`, `(*DB).GetOrCreateProject(rootPath, name string) (int64, error)`, `(*DB).CreateSession(sessionID string, projectID int64, title string, startedAt time.Time) error`, internal helpers `db.exec` / `db.sqlDB.Query`.
- Produces (Task 4 relies on these):
  - `type TurnMetricsRow struct` — exact fields below.
  - `func (db *DB) InsertTurnMetrics(row TurnMetricsRow) (int64, error)`
  - `func (db *DB) RecentTurnMetrics(projectID int64, limit int) ([]TurnMetricsRow, error)` — newest first.

- [ ] **Step 1: Write the failing tests**

Create `internal/db/turnmetrics_test.go`:

```go
package db

import (
	"path/filepath"
	"testing"
	"time"
)

func openMetricsTestDB(t *testing.T) (*DB, int64) {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/tmp/proj", "proj")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}

func sampleRow(projectID int64, sessionID string) TurnMetricsRow {
	return TurnMetricsRow{
		ProjectID:        projectID,
		SessionID:        sessionID,
		StartedAt:        time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		DurationMs:       1234,
		Class:            "question",
		Role:             "general",
		Provider:         "scripted",
		Model:            "test-model",
		Goal:             "how does pkg work?",
		Iterations:       3,
		ToolCalls:        2,
		ToolErrors:       1,
		CacheHits:        1,
		ParseFailures:    1,
		SoftStalls:       1,
		HardStalls:       0,
		Outcome:          "answered",
		SalvageReason:    "",
		PromptTokens:     17,
		CompletionTokens: 8,
	}
}

func TestInsertAndRecentTurnMetricsRoundTrip(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	if err := database.CreateSession("sess_1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	want := sampleRow(projectID, "sess_1")
	id, err := database.InsertTurnMetrics(want)
	if err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertTurnMetrics returned id 0")
	}

	rows, err := database.RecentTurnMetrics(projectID, 10)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	want.ID = got.ID
	if got != want {
		t.Fatalf("row mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestInsertTurnMetricsNullSessionID(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	row := sampleRow(projectID, "") // empty -> stored as NULL
	if _, err := database.InsertTurnMetrics(row); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	rows, err := database.RecentTurnMetrics(projectID, 1)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "" {
		t.Fatalf("rows = %+v, want one row with empty SessionID", rows)
	}
}

func TestRecentTurnMetricsNewestFirstAndLimited(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	for i := 0; i < 3; i++ {
		row := sampleRow(projectID, "")
		row.Iterations = i + 1
		if _, err := database.InsertTurnMetrics(row); err != nil {
			t.Fatalf("InsertTurnMetrics %d: %v", i, err)
		}
	}
	rows, err := database.RecentTurnMetrics(projectID, 2)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (limit)", len(rows))
	}
	if rows[0].Iterations != 3 || rows[1].Iterations != 2 {
		t.Fatalf("order = %d,%d; want newest first (3,2)", rows[0].Iterations, rows[1].Iterations)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/db/ -run 'TurnMetrics' -v`
Expected: compile error — `undefined: TurnMetricsRow`, `database.InsertTurnMetrics undefined`. Red state; proceed.

- [ ] **Step 3: Add the migration**

In `internal/db/migrations.go`, append inside the `schema` const, after the `idx_memories_project` index line and before the closing backtick:

```sql
CREATE TABLE IF NOT EXISTS turn_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES agent_sessions(id) ON DELETE SET NULL,
    started_at TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    class TEXT NOT NULL,
    role TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    goal TEXT NOT NULL,
    iterations INTEGER NOT NULL,
    tool_calls INTEGER NOT NULL,
    tool_errors INTEGER NOT NULL,
    cache_hits INTEGER NOT NULL,
    parse_failures INTEGER NOT NULL,
    soft_stalls INTEGER NOT NULL,
    hard_stalls INTEGER NOT NULL,
    outcome TEXT NOT NULL,
    salvage_reason TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_turn_metrics_project ON turn_metrics(project_id, id);
```

- [ ] **Step 4: Write the implementation**

Create `internal/db/turnmetrics.go`:

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

// TurnMetricsRow mirrors one turn_metrics row. SessionID is "" when the row
// has no session (stored as NULL; agent_sessions.id is TEXT). internal/db
// deliberately does not import internal/agent — the app layer maps
// agent.TurnMetrics into this struct.
type TurnMetricsRow struct {
	ID               int64
	ProjectID        int64
	SessionID        string
	StartedAt        time.Time
	DurationMs       int64
	Class            string
	Role             string
	Provider         string
	Model            string
	Goal             string
	Iterations       int
	ToolCalls        int
	ToolErrors       int
	CacheHits        int
	ParseFailures    int
	SoftStalls       int
	HardStalls       int
	Outcome          string
	SalvageReason    string
	PromptTokens     int
	CompletionTokens int
}

// InsertTurnMetrics persists one turn's metrics and returns the new row id.
func (db *DB) InsertTurnMetrics(row TurnMetricsRow) (int64, error) {
	var sessionID sql.NullString
	if row.SessionID != "" {
		sessionID = sql.NullString{String: row.SessionID, Valid: true}
	}
	res, err := db.exec(
		`INSERT INTO turn_metrics (
			project_id, session_id, started_at, duration_ms, class, role,
			provider, model, goal, iterations, tool_calls, tool_errors,
			cache_hits, parse_failures, soft_stalls, hard_stalls, outcome,
			salvage_reason, prompt_tokens, completion_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ProjectID,
		sessionID,
		row.StartedAt.UTC().Format(time.RFC3339),
		row.DurationMs,
		row.Class,
		row.Role,
		row.Provider,
		row.Model,
		row.Goal,
		row.Iterations,
		row.ToolCalls,
		row.ToolErrors,
		row.CacheHits,
		row.ParseFailures,
		row.SoftStalls,
		row.HardStalls,
		row.Outcome,
		row.SalvageReason,
		row.PromptTokens,
		row.CompletionTokens,
	)
	if err != nil {
		return 0, fmt.Errorf("insert turn metrics: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("turn metrics insert id: %w", err)
	}
	return id, nil
}

// RecentTurnMetrics returns up to limit rows for a project, newest first.
func (db *DB) RecentTurnMetrics(projectID int64, limit int) ([]TurnMetricsRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, project_id, session_id, started_at, duration_ms, class,
			role, provider, model, goal, iterations, tool_calls, tool_errors,
			cache_hits, parse_failures, soft_stalls, hard_stalls, outcome,
			salvage_reason, prompt_tokens, completion_tokens
		 FROM turn_metrics
		 WHERE project_id = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query turn metrics: %w", err)
	}
	defer rows.Close()

	var out []TurnMetricsRow
	for rows.Next() {
		var r TurnMetricsRow
		var sessionID sql.NullString
		var started string
		if err := rows.Scan(
			&r.ID, &r.ProjectID, &sessionID, &started, &r.DurationMs, &r.Class,
			&r.Role, &r.Provider, &r.Model, &r.Goal, &r.Iterations, &r.ToolCalls,
			&r.ToolErrors, &r.CacheHits, &r.ParseFailures, &r.SoftStalls,
			&r.HardStalls, &r.Outcome, &r.SalvageReason, &r.PromptTokens,
			&r.CompletionTokens,
		); err != nil {
			return nil, fmt.Errorf("scan turn metrics row: %w", err)
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		parsed, err := time.Parse(time.RFC3339, started)
		if err != nil {
			return nil, fmt.Errorf("parse started_at: %w", err)
		}
		r.StartedAt = parsed.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turn metrics rows: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/db/`
Expected: all PASS (including the existing migration tests, which re-run the enlarged schema).

- [ ] **Step 6: Format, vet, commit**

```bash
gofmt -w internal/db
go vet ./internal/db/...
git add internal/db/migrations.go internal/db/turnmetrics.go internal/db/turnmetrics_test.go
git commit -m "feat(db): persist turn metrics"
```

---

### Task 4: Wire persistence into runner construction

**Files:**
- Modify: `internal/app/session/session.go` (two accessors, after the `New` constructor)
- Modify: `internal/app/app.go` (`buildAgentRunner` and the swarm factory in `buildSwarmRunner`)
- Test: `internal/app/metrics_recorder_test.go`

**Interfaces:**
- Consumes: `Runner.MetricsObserver func(agent.TurnMetrics)` (Task 2); `db.TurnMetricsRow`, `(*DB).InsertTurnMetrics`, `(*DB).RecentTurnMetrics` (Task 3).
- Produces:
  - `func (s *State) SessionID() string` and `func (s *State) Logger() *slog.Logger` on `session.State` (fields are set once in `New` and never mutated — no locking needed).
  - `func metricsRecorder(database *db.DB, projectID int64, sessionID string, logger *slog.Logger) func(agent.TurnMetrics)` in `internal/app/app.go`.

- [ ] **Step 1: Write the failing test**

Create `internal/app/metrics_recorder_test.go`:

```go
package app

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/db"
)

func TestMetricsRecorderPersistsTurn(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/tmp/proj", "proj")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("sess_1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	record := metricsRecorder(database, projectID, "sess_1", nil)
	record(agent.TurnMetrics{
		StartedAt:  time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		DurationMs: 42,
		Goal:       "eval goal",
		Class:      "question",
		Role:       "general",
		Model:      "test-model",
		Iterations: 2,
		ToolCalls:  1,
		Outcome:    "answered",
	})

	rows, err := database.RecentTurnMetrics(projectID, 5)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.SessionID != "sess_1" || got.Goal != "eval goal" || got.Outcome != "answered" ||
		got.Iterations != 2 || got.ToolCalls != 1 || got.Model != "test-model" {
		t.Fatalf("row = %+v", got)
	}
}

func TestMetricsRecorderSwallowsInsertFailure(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	database.Close() // closed DB: inserts will fail

	record := metricsRecorder(database, 1, "", nil)
	// Must not panic; errors are swallowed (logged when a logger is set).
	record(agent.TurnMetrics{Outcome: "answered"})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run 'TestMetricsRecorder' -v`
Expected: compile error — `undefined: metricsRecorder`. Red state; proceed.

- [ ] **Step 3: Add the State accessors**

In `internal/app/session/session.go`, immediately after the `New` constructor:

```go
// SessionID returns the persistence session id ("" when persistence is
// disabled). Set once in New; safe without locking.
func (s *State) SessionID() string { return s.sessionID }

// Logger returns the session logger (nil when persistence is disabled).
// Set once in New; safe without locking.
func (s *State) Logger() *slog.Logger { return s.logger }
```

(`log/slog` is already imported by session.go for the Persistence struct.)

- [ ] **Step 4: Add metricsRecorder and wire both runner constructors**

In `internal/app/app.go`, add (near `dbMemoryProvider`, which is the analogous db-backed adapter):

```go
// metricsRecorder returns a MetricsObserver that persists each turn's
// metrics. Failures are logged and swallowed: telemetry must never break a
// turn.
func metricsRecorder(database *db.DB, projectID int64, sessionID string, logger *slog.Logger) func(agent.TurnMetrics) {
	return func(m agent.TurnMetrics) {
		_, err := database.InsertTurnMetrics(db.TurnMetricsRow{
			ProjectID:        projectID,
			SessionID:        sessionID,
			StartedAt:        m.StartedAt,
			DurationMs:       m.DurationMs,
			Class:            m.Class,
			Role:             m.Role,
			Provider:         m.Provider,
			Model:            m.Model,
			Goal:             m.Goal,
			Iterations:       m.Iterations,
			ToolCalls:        m.ToolCalls,
			ToolErrors:       m.ToolErrors,
			CacheHits:        m.CacheHits,
			ParseFailures:    m.ParseFailures,
			SoftStalls:       m.SoftStalls,
			HardStalls:       m.HardStalls,
			Outcome:          m.Outcome,
			SalvageReason:    m.SalvageReason,
			PromptTokens:     m.PromptTokens,
			CompletionTokens: m.CompletionTokens,
		})
		if err != nil && logger != nil {
			logger.Warn("failed to persist turn metrics", "error", err)
		}
	}
}
```

Add `"log/slog"` to app.go's imports if not already present.

In `buildAgentRunner`, after the `runner.ProjectID = projectID` line:

```go
	runner.MetricsObserver = metricsRecorder(database, projectID, state.SessionID(), state.Logger())
```

In `buildSwarmRunner`'s `factory` closure, after the `r.ProjectID = projectID` line:

```go
		r.MetricsObserver = metricsRecorder(database, projectID, state.SessionID(), state.Logger())
```

- [ ] **Step 5: Run the tests, then the app and session packages**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run 'TestMetricsRecorder' -v`
Expected: both PASS.

Run: `CGO_ENABLED=1 go test -count=1 ./internal/app/...`
Expected: all packages `ok`.

- [ ] **Step 6: Format, vet, commit**

```bash
gofmt -w internal/app
go vet ./internal/app/... 2>&1 | grep -v "app.go:.*copies lock value" || true
git add internal/app/session/session.go internal/app/app.go internal/app/metrics_recorder_test.go
git commit -m "feat(app): wire turn-metrics persistence into runner construction"
```

(The grep filters the documented pre-existing mutex-copy warning; any OTHER vet output is a blocker.)

---

### Task 5: Eval scenario baseline

**Files:**
- Test: `internal/agent/eval_scenarios_test.go` (new)

**Interfaces:**
- Consumes: `Runner.MetricsObserver` (Task 2), `scriptedProvider` (runner_test.go), `newTestState` (runner_test.go), `TurnMetrics` (Task 1).
- Produces: nothing consumed later — this file is the regression baseline that future loop-improvement plans (reliability trio, ask_user, structured output) extend with new rows.

- [ ] **Step 1: Write the scenario table**

Create `internal/agent/eval_scenarios_test.go`:

```go
package agent

// Eval scenarios: deterministic end-to-end turns through RunTask, asserting
// on the TurnMetrics each turn emits rather than scraping transcripts. This
// table is the loop's regression baseline — when changing loop behavior
// (parse handling, stall detection, finalize), extend this table instead of
// writing one-off transcript assertions.

import (
	"context"
	"fmt"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// evalRegistry registers the fake tools scenarios use: a read-only
// file.read, a write-risk file.write_patch (policy auto-allows non-shell
// tools), and a validation tool demo.test.
func evalRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	fakes := []registry.Tool{
		{Name: "file.read", Risk: registry.RiskReadOnly},
		{Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite},
		{Name: "demo.test", Risk: registry.RiskReadOnly},
	}
	for _, tool := range fakes {
		tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "ok"}, nil
		}
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name, err)
		}
	}
	return reg
}

func evalRead(path string) string {
	return fmt.Sprintf(`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":%q}}}`, path)
}

func TestEvalScenarios(t *testing.T) {
	finalAnswer := `{"rationale":"done","action":{"type":"final","content":"Answer."}}`

	cases := []struct {
		name       string
		responses  []string
		forceClass TaskClass
		maxIters   int // 0 = default
		want       func(t *testing.T, m TurnMetrics)
	}{
		{
			name: "research turn answers after distinct reads",
			responses: []string{
				evalRead("a.go"), evalRead("b.go"), evalRead("c.go"),
				evalRead("d.go"), evalRead("e.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.Iterations != 6 || m.ToolCalls != 5 ||
					m.ParseFailures != 0 || m.SoftStalls != 0 || m.HardStalls != 0 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "edit turn patches and validates",
			responses: []string{
				"1. Read the file. 2. Patch it. 3. Validate.",
				evalRead("a.go"),
				`{"rationale":"apply","action":{"type":"patch","content":"File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}}`,
				`{"rationale":"validate","action":{"type":"tool_call","tool":"demo.test","args":{}}}`,
				finalAnswer,
			},
			forceClass: ClassEdit,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ToolCalls != 3 || m.ToolErrors != 0 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name:       "parse failure recovers to an answer",
			responses:  []string{"not a json action at all", finalAnswer},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ParseFailures != 1 || m.ToolCalls != 0 || m.Iterations != 2 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "exact repeat hard-stalls into salvage",
			responses: []string{
				evalRead("a.go"), evalRead("a.go"), evalRead("a.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "salvaged" || m.SalvageReason != "stalled" || m.HardStalls != 1 || m.ToolCalls != 3 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "exhaustion salvages with reason exhausted",
			responses: []string{
				evalRead("a.go"), evalRead("b.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			maxIters:   2,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "salvaged" || m.SalvageReason != "exhausted" || m.Iterations != 2 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name: "tool error recovers to an answer",
			responses: []string{
				`{"rationale":"r","action":{"type":"tool_call","tool":"missing.tool","args":{}}}`,
				evalRead("a.go"),
				finalAnswer,
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ToolErrors != 1 || m.ToolCalls != 2 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := evalRegistry(t)
			p := &scriptedProvider{responses: tc.responses}
			state := newTestState(t)
			r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
			r.SetForceClass(string(tc.forceClass))
			if tc.maxIters > 0 {
				r.MaxToolIterations = tc.maxIters
			}
			var got *TurnMetrics
			r.MetricsObserver = func(m TurnMetrics) { got = &m }

			if _, err := r.RunTask(context.Background(), "eval goal"); err != nil {
				t.Fatalf("RunTask err = %v", err)
			}
			if got == nil {
				t.Fatal("no TurnMetrics emitted")
			}
			tc.want(t, *got)
		})
	}
}
```

- [ ] **Step 2: Run the scenarios**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestEvalScenarios -v`
Expected: all six subtests PASS on the first run — they encode behavior Tasks 1–2 already implemented. If any fails, the runner integration (Task 2) is wrong: fix it there, do not weaken the scenario.

- [ ] **Step 3: Run the full repository suite**

Run: `CGO_ENABLED=1 go test -count=1 ./...`
Expected: every package `ok`.

- [ ] **Step 4: Format, vet, commit**

```bash
gofmt -w internal/agent/eval_scenarios_test.go
go vet ./internal/agent/...
git add internal/agent/eval_scenarios_test.go
git commit -m "test(agent): eval scenario baseline asserting turn metrics"
```

---

## Verification

After Task 5: `git log --oneline main..HEAD` shows five commits; `CGO_ENABLED=1 go test -count=1 ./...` is green. Optional live check: run `go run ./cmd/marshal` against a configured model, ask one question, then `sqlite3 <project-db-path> 'SELECT outcome, iterations, tool_calls FROM turn_metrics'` shows the turn's row.
