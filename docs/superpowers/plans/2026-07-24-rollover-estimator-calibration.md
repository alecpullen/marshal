# Rollover EstimatorCounter Calibration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close P2 item 3 in `docs/marshal-context-rollover-spec.md`: a calibration harness that records paired `EstimatorCounter`-vs-provider-token observations and reports the estimator's error. `UsageCounter` already bounds the error in the safe direction (it reports the larger of the two), but the magnitude is unmeasured; this plan measures it.

**Architecture:** A `token_calibration` table records, per chat turn that reports provider usage, the `EstimatorCounter` value for that turn's wire and the provider-reported `prompt_tokens` already flowing through `Runner.UsageObserver`. Recording is gated by `[session.rollover.calibration] enabled = false` (off by default). A new `marshal calibrate-tokens --from-db` subcommand aggregates the recorded samples into a mean/min/max estimator:provider ratio and prints it, with an explicit scope caveat (the estimator counts only message `Content` runes / 4 — not tool-call JSON, the system prompt, or role tokens — so the ratio is estimator-vs-`prompt_tokens`, not estimator-vs-full-prompt). Both features sit behind config flags and are inert by default.

**Tech Stack:** Go, `modernc.org/sqlite`, standard `testing`.

## Global Constraints

- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter dependency). Test with `go test ./...`.
- Timestamps in `internal/db` are stored as **TEXT RFC3339 UTC** (`.UTC().Format(time.RFC3339)`), never integer epochs. String comparison of two such values is chronological.
- `internal/db` imports nothing from `internal/app/config`; keep that direction (config may import db, never the reverse).
- The single-connection pool discipline (see `ArchiveTurns`, `PruneSessionGenerations`): resolve/read via `db.sqlDB` *before* opening a write transaction; do not issue a `db.sqlDB` call while a `tx` is open on the same connection.
- Rollover is off by default (`[session.rollover] enabled = false`); calibration is additionally gated behind `[session.rollover.calibration] enabled = false` and must not change behaviour for any session that does not opt in. A calibration-sample insert failure is logged and swallowed (it is a measurement tool, not a runtime dependency — telemetry must never break a turn).
- `EstimatorCounter.CountTokens` is `(runeCount + 3) / 4` summed over message `Content` — it does **not** count tool-call JSON, system-prompt scaffolding, or role tokens. The calibration report must state this scope explicitly so the ratio is not misread as "estimator vs. full prompt."
- New config fields use the existing pointer-field merge pattern (`*string`/`*bool` in `file_types.go`, `set(&dst, src)` in `merge.go`) so a partial TOML never zeroes an unset field back to its zero value.

---

## File Structure

- Create: `internal/db/calibration.go` — `CalibrationSample`, `CalibrationSummary`, `InsertCalibrationSample`, `CalibrationSummary` aggregation query.
- Create: `internal/db/calibration_test.go` — table + aggregation tests.
- Create: `cmd/marshal/calibrate.go` — `runCalibrateTokens` subcommand (`--from-db`, `--project`, `--session`).
- Create: `cmd/marshal/calibrate_test.go` — subcommand output tests.
- Modify: `cmd/marshal/main.go` — dispatch `calibrate-tokens` subcommand.
- Modify: `internal/db/migrations.go` — add `token_calibration` table to the `const schema` string.
- Modify: `internal/agent/runner.go` — add `CalibrationObserver` field.
- Modify: `internal/agent/chat.go` — invoke `CalibrationObserver` after `UsageObserver`.
- Modify: `internal/app/app.go` — record a calibration sample per turn when `calibration.enabled`.
- Modify: `internal/app/config/types.go` — add `CalibrationConfig` under `RolloverConfig`.
- Modify: `internal/app/config/file_types.go` — add `fileCalibration`.
- Modify: `internal/app/config/defaults.go` — `Calibration{Enabled: false}`.
- Modify: `internal/app/config/merge.go` — merge calibration block.
- Modify: `docs/marshal-context-rollover-spec.md` — flip P2 item 3 (calibration) to implemented; note the measurement scope caveat.

---

## Task 1: token_calibration table and DB API

**Files:**
- Create: `internal/db/calibration.go`
- Test: `internal/db/calibration_test.go`
- Modify: `internal/db/migrations.go`

**Interfaces:**
- Produces: `CalibrationSample{ID, ProjectID, SessionID, Provider, Model, EstimatorTokens, ProviderTokens, CreatedAt}`; `func (db *DB) InsertCalibrationSample(CalibrationSample) (int64, error)`; `func (db *DB) CalibrationSummary(projectID int64, sessionID string) (CalibrationSummary, error)`; `type CalibrationSummary{Samples, MeanEstimator, MeanProvider, MeanRatio, MaxRatio, MinRatio}`.

- [ ] **Step 1: Write the failing test**

Create `internal/db/calibration_test.go`:

```go
package db

import (
	"testing"
	"time"
)

func TestInsertAndSummarizeCalibration(t *testing.T) {
	d := newTestDB(t)
	defer d.Close()
	pid, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateSession("s1", pid, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ est, prov int }{
		{100, 120}, {200, 250}, {400, 390},
	} {
		if _, err := d.InsertCalibrationSample(CalibrationSample{
			ProjectID: pid, SessionID: "s1", Provider: "ollama", Model: "qwen",
			EstimatorTokens: s.est, ProviderTokens: s.prov, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	sum, err := d.CalibrationSummary(pid, "s1")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Samples != 3 {
		t.Fatalf("samples = %d, want 3", sum.Samples)
	}
	// ratios: 100/120=0.833, 200/250=0.8, 400/390=1.025
	wantMean := (0.833333 + 0.8 + 1.025641) / 3
	if !approxEqual(sum.MeanRatio, wantMean, 0.01) {
		t.Errorf("mean ratio = %.4f, want ~%.4f", sum.MeanRatio, wantMean)
	}
	if sum.MaxRatio < 1.0 {
		t.Errorf("max ratio = %.4f, want >= 1.0", sum.MaxRatio)
	}
	if sum.MinRatio > 0.85 {
		t.Errorf("min ratio = %.4f, want < 0.85", sum.MinRatio)
	}
}

func TestCalibrationSummaryEmpty(t *testing.T) {
	d := newTestDB(t)
	defer d.Close()
	pid, err := d.GetOrCreateProject("/repo2", "repo2")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := d.CalibrationSummary(pid, "s-none")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Samples != 0 {
		t.Fatalf("samples = %d, want 0", sum.Samples)
	}
}

func approxEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < tol
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestInsertAndSummarizeCalibration -v`
Expected: FAIL — `undefined: CalibrationSample`, `undefined: InsertCalibrationSample`.

- [ ] **Step 3: Write the implementation**

`internal/db/migrations.go` defines a single `const schema = \`...\`` string (lines 3–194) of `CREATE TABLE IF NOT EXISTS` statements, closing with the `generation_turns_fts` virtual table and a backtick at line 194. Append the new table inside that string, immediately before the closing backtick (after the `generation_turns_fts` block). The exact edit: replace the final lines

```go
CREATE VIRTUAL TABLE IF NOT EXISTS generation_turns_fts USING fts5(
    content,
    content='',
    tokenize='porter unicode61'
);
`
```

with

```go
CREATE VIRTUAL TABLE IF NOT EXISTS generation_turns_fts USING fts5(
    content,
    content='',
    tokenize='porter unicode61'
);

CREATE TABLE IF NOT EXISTS token_calibration (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL,
    session_id TEXT,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    estimator_tokens INTEGER NOT NULL,
    provider_tokens INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_cal_project ON token_calibration(project_id, session_id);
`
```

`IF NOT EXISTS` makes this safe against any DB that already ran the prior schema — the migration is idempotent, matching every other table in this string.

Create `internal/db/calibration.go`:

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

// CalibrationSample is one paired observation of the EstimatorCounter's
// token count versus the provider-reported prompt-token count for a single
// chat turn.
type CalibrationSample struct {
	ID              int64
	ProjectID       int64
	SessionID       string
	Provider        string
	Model           string
	EstimatorTokens int
	ProviderTokens  int
	CreatedAt       time.Time
}

// CalibrationSummary aggregates paired calibration observations for a
// project/session. Ratios are estimator/provider; a ratio < 1.0 means the
// estimator under-counts (the safe direction for rollover, since
// UsageCounter takes the larger of the two).
type CalibrationSummary struct {
	Samples       int
	MeanEstimator float64
	MeanProvider  float64
	MeanRatio     float64
	MaxRatio      float64
	MinRatio      float64
}

// InsertCalibrationSample persists one paired observation.
func (db *DB) InsertCalibrationSample(s CalibrationSample) (int64, error) {
	var sessionID sql.NullString
	if s.SessionID != "" {
		sessionID = sql.NullString{String: s.SessionID, Valid: true}
	}
	res, err := db.sqlDB.Exec(
		`INSERT INTO token_calibration
		 (project_id, session_id, provider, model, estimator_tokens, provider_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ProjectID, sessionID, s.Provider, s.Model,
		s.EstimatorTokens, s.ProviderTokens,
		s.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("insert calibration sample: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("calibration insert id: %w", err)
	}
	return id, nil
}

// CalibrationSummary aggregates calibration samples for a project (and,
// optionally, a single session). When sessionID is empty, all sessions for
// the project are summed.
func (db *DB) CalibrationSummary(projectID int64, sessionID string) (CalibrationSummary, error) {
	query := `SELECT estimator_tokens, provider_tokens FROM token_calibration WHERE project_id = ?`
	args := []any{projectID}
	if sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return CalibrationSummary{}, fmt.Errorf("calibration summary: %w", err)
	}
	defer rows.Close()

	var sum CalibrationSummary
	var sumEst, sumProv, ratioSum float64
	var ratioSamples int
	for rows.Next() {
		var est, prov int
		if err := rows.Scan(&est, &prov); err != nil {
			return CalibrationSummary{}, fmt.Errorf("scan calibration: %w", err)
		}
		sum.Samples++
		sumEst += float64(est)
		sumProv += float64(prov)
		if prov > 0 {
			r := float64(est) / float64(prov)
			if ratioSamples == 0 {
				sum.MinRatio = r
				sum.MaxRatio = r
			}
			if r < sum.MinRatio {
				sum.MinRatio = r
			}
			if r > sum.MaxRatio {
				sum.MaxRatio = r
			}
			ratioSum += r
			ratioSamples++
		}
	}
	if err := rows.Err(); err != nil {
		return CalibrationSummary{}, fmt.Errorf("iterate calibration: %w", err)
	}
	if sum.Samples > 0 {
		sum.MeanEstimator = sumEst / float64(sum.Samples)
		sum.MeanProvider = sumProv / float64(sum.Samples)
		// Mean ratio is over samples with a non-zero provider count only;
		// samples with prov==0 are excluded from the ratio (undefined).
		if ratioSamples > 0 {
			sum.MeanRatio = ratioSum / float64(ratioSamples)
		}
	}
	return sum, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestInsertAndSummarizeCalibration -v`
Expected: PASS for both new tests; the migration creates the table on the test DB (verify `newTestDB` runs `Migrate` — it does, since other generation tests rely on it).

- [ ] **Step 5: Commit**

```bash
git add internal/db/calibration.go internal/db/calibration_test.go internal/db/migrations.go
git commit -m "feat(db): token_calibration table and estimator/provider summary

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 2: Calibration config and per-turn recording

**Files:**
- Modify: `internal/app/config/types.go` (add `CalibrationConfig`)
- Modify: `internal/app/config/file_types.go` (add `fileCalibration`)
- Modify: `internal/app/config/defaults.go`
- Modify: `internal/app/config/merge.go`
- Test: `internal/app/config/rollover_test.go`
- Modify: `internal/agent/runner.go` (add `CalibrationObserver` field)
- Modify: `internal/agent/chat.go` (invoke the observer)
- Modify: `internal/app/app.go` (record a sample per turn when enabled)

**Interfaces:**
- Produces: `[session.rollover.calibration] enabled = false`; when enabled, every turn that reports provider usage also records a `CalibrationSample` with the `EstimatorCounter` value for that turn's wire. `Runner.CalibrationObserver func(wire []schema.ChatMessage, promptTokens int)` — nil by default, set only when calibration is enabled.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/config/rollover_test.go`:

```go
func TestDefaultRolloverCalibrationDisabled(t *testing.T) {
	if Default().Session.Rollover.Calibration.Enabled {
		t.Error("default calibration.Enabled = true, want false")
	}
}

func TestMergeRolloverCalibration(t *testing.T) {
	cfg := Default()
	enabled := true
	if err := merge(&cfg, configFile{
		Session: &fileSession{Rollover: &fileRollover{
			Calibration: &fileCalibration{Enabled: &enabled},
		}},
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !cfg.Session.Rollover.Calibration.Enabled {
		t.Error("calibration.Enabled not merged")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/config/ -run TestDefaultRolloverCalibration -v`
Expected: FAIL — `cfg.Session.Rollover.Calibration undefined`.

- [ ] **Step 3: Add the types, default, file-type, and merge**

In `internal/app/config/types.go`, add a new struct and field. After the `RolloverConfig` struct (after the `Verbose bool` field):

```go
// CalibrationConfig controls EstimatorCounter calibration recording.
// When enabled, each turn that reports provider usage also persists a
// paired estimator-vs-provider token observation to the token_calibration
// table, so the estimator's error can be measured later. Disabled by
// default — it is a measurement tool, not a runtime dependency.
type CalibrationConfig struct {
	Enabled bool `toml:"enabled"`
}
```

And add a field inside `RolloverConfig` (after `Verbose`):

```go
	Calibration             CalibrationConfig `toml:"calibration"`
```

In `internal/app/config/file_types.go`, add after `fileRollover`:

```go
type fileCalibration struct {
	Enabled *bool `toml:"enabled"`
}
```

And add a field inside `fileRollover` (after `Verbose *bool`):

```go
	Calibration             *fileCalibration `toml:"calibration"`
```

In `internal/app/config/defaults.go`, inside the `Rollover: RolloverConfig{...}` literal, add:

```go
				Calibration: CalibrationConfig{Enabled: false},
```

In `internal/app/config/merge.go`, inside the rollover block (after the `set(&cfg.Session.Rollover.Verbose, r.Verbose)` line), add:

```go
		if r.Calibration != nil {
			set(&cfg.Session.Rollover.Calibration.Enabled, r.Calibration.Enabled)
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/config/ -run TestDefaultRollover|TestMergeRollover -v`
Expected: PASS.

- [ ] **Step 5: Add the CalibrationObserver to the runner and invoke it**

In `internal/agent/runner.go`, add to the `Runner` struct (near `UsageObserver`, ~line 187):

```go
	// CalibrationObserver, when set, receives the wire messages and the
	// provider-reported prompt-token count after each chatOnce that reports
	// usage. Used by the rollover calibration harness to record paired
	// estimator-vs-provider observations. Nil disables recording.
	CalibrationObserver func(wire []schema.ChatMessage, promptTokens int)
```

In `internal/agent/chat.go`, after the existing `UsageObserver` call (chat.go:99-101), add:

```go
	if r.CalibrationObserver != nil && usage != nil {
		r.CalibrationObserver(messages, usage.PromptTokens)
	}
```

`messages` is the `[]schema.ChatMessage` parameter already in scope at the `chatOnce` call site (chat.go:46), and `schema` is already imported in `chat.go`.

- [ ] **Step 6: Record a calibration sample per turn when enabled**

In `internal/app/app.go`, the `UsageObserver` closure is at ~line 508. After the `runner.UsageObserver = ...` assignment (which ends ~line 513), add (only when calibration is enabled):

```go
	if cfg.Session.Rollover.Calibration.Enabled {
		estCounter := rollover.EstimatorCounter{}
		prov := route.Preset.Provider
		model := route.Preset.Model
		sid := state.SessionID()
		runner.CalibrationObserver = func(wire []schema.ChatMessage, promptTokens int) {
			est, err := estCounter.CountTokens(context.Background(), wire)
			if err != nil {
				return
			}
			if _, err := database.InsertCalibrationSample(db.CalibrationSample{
				ProjectID:       projectID,
				SessionID:       sid,
				Provider:        prov,
				Model:           model,
				EstimatorTokens: est,
				ProviderTokens:  promptTokens,
				CreatedAt:       time.Now(),
			}); err != nil && state.Logger() != nil {
				state.Logger().Warn("calibration sample insert failed", "error", err)
			}
		}
	}
```

Confirm `route.Preset.Provider` and `route.Preset.Model` exist on `ModelPreset` (`internal/llm/routing/types.go:44-45`). Confirm `context` and `time` are imported in `app.go` (they are). The insert failure is logged and swallowed — telemetry never breaks a turn.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go build ./...`
Expected: builds.

Run: `go test ./internal/app/... ./internal/agent/...`
Expected: PASS — the new observer is nil by default, so no existing test changes; the calibration observer is only set when config enables it, which no existing test does.

- [ ] **Step 8: Commit**

```bash
git add internal/app/config/types.go internal/app/config/file_types.go internal/app/config/defaults.go internal/app/config/merge.go internal/app/config/rollover_test.go internal/agent/runner.go internal/agent/chat.go internal/app/app.go
git commit -m "feat(rollover): record estimator-vs-provider token calibration per turn

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 3: `marshal calibrate-tokens` subcommand

**Files:**
- Create: `cmd/marshal/calibrate.go`
- Create: `cmd/marshal/calibrate_test.go`
- Modify: `cmd/marshal/main.go`

**Interfaces:**
- Produces: `marshal calibrate-tokens [--from-db] [--project <dir>] [--session <id>]` — with `--from-db` it aggregates recorded `token_calibration` rows into a report; without `--from-db` it prints a usage hint pointing at `--from-db` (a live-corpus mode that drives a provider is intentionally out of scope: the recorded real-turn samples are the honest measurement and require no extra provider plumbing).

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/calibrate_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"marshal/internal/db"
)

func TestRunCalibrateTokens_FromDBEmpty(t *testing.T) {
	tmp := t.TempDir()
	if err := setupCalibrateDB(t, tmp); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runCalibrateTokens(context.Background(), []string{"--from-db", "--project", tmp}, &out)
	if err != nil {
		t.Fatalf("runCalibrateTokens: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("No calibration samples")) {
		t.Errorf("expected 'No calibration samples' in output, got:\n%s", out.String())
	}
}

func TestRunCalibrateTokens_FromDBWithSamples(t *testing.T) {
	tmp := t.TempDir()
	database, pid := setupCalibrateDBWithProject(t, tmp)
	for _, s := range []struct{ est, prov int }{
		{100, 120}, {200, 250},
	} {
		if _, err := database.InsertCalibrationSample(db.CalibrationSample{
			ProjectID: pid, SessionID: "s1", Provider: "ollama", Model: "qwen",
			EstimatorTokens: s.est, ProviderTokens: s.prov, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()

	var out bytes.Buffer
	if err := runCalibrateTokens(context.Background(), []string{"--from-db", "--project", tmp}, &out); err != nil {
		t.Fatalf("runCalibrateTokens: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("samples:")) {
		t.Errorf("expected 'samples:' in report, got:\n%s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("ratio")) {
		t.Errorf("expected 'ratio' in report, got:\n%s", out.String())
	}
}

func setupCalibrateDB(t *testing.T, dir string) error {
	t.Helper()
	database, err := db.Open(db.Path(dir))
	if err != nil {
		return err
	}
	defer database.Close()
	return database.Migrate()
}

func setupCalibrateDBWithProject(t *testing.T, dir string) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, err := database.GetOrCreateProject(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return database, pid
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestRunCalibrateTokens -v`
Expected: FAIL — `undefined: runCalibrateTokens`.

- [ ] **Step 3: Write the subcommand**

Create `cmd/marshal/calibrate.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"marshal/internal/db"
)

// runCalibrateTokens implements `marshal calibrate-tokens`, a read-only
// report on EstimatorCounter accuracy. With --from-db it aggregates the
// token_calibration rows already recorded for a project (by the per-turn
// calibration observer when [session.rollover.calibration] enabled = true).
// Without --from-db it prints a usage hint.
func runCalibrateTokens(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("calibrate-tokens", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fromDB := fs.Bool("from-db", false, "summarize recorded token_calibration rows for the project")
	projectDir := fs.String("project", "", "project directory (default: cwd)")
	session := fs.String("session", "", "limit to one session id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*fromDB {
		fmt.Fprintln(stdout, "calibrate-tokens: pass --from-db to summarize recorded samples.")
		fmt.Fprintln(stdout, "To record samples, set [session.rollover] enabled = true and [session.rollover.calibration] enabled = true, then run sessions.")
		fmt.Fprintln(stdout, "Scope note: EstimatorCounter counts only message Content runes / 4; it does not count tool-call JSON, the system prompt, or role tokens. The ratio is estimator-vs-prompt_tokens, not estimator-vs-full-prompt.")
		return nil
	}

	dir := *projectDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve project dir: %w", err)
		}
	}
	database, err := db.Open(db.Path(dir))
	if err != nil {
		return fmt.Errorf("open project database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	pid, err := database.GetOrCreateProject(dir, "")
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	sum, err := database.CalibrationSummary(pid, *session)
	if err != nil {
		return fmt.Errorf("calibration summary: %w", err)
	}
	if sum.Samples == 0 {
		fmt.Fprintln(stdout, "No calibration samples recorded for this project.")
		fmt.Fprintln(stdout, "Enable [session.rollover.calibration] and run sessions to record samples.")
		return nil
	}

	fmt.Fprintf(stdout, "EstimatorCounter calibration report\n")
	fmt.Fprintf(stdout, "  project dir: %s\n", dir)
	if *session != "" {
		fmt.Fprintf(stdout, "  session:     %s\n", *session)
	}
	fmt.Fprintf(stdout, "  samples:     %d\n", sum.Samples)
	fmt.Fprintf(stdout, "  mean estimator tokens: %.1f\n", sum.MeanEstimator)
	fmt.Fprintf(stdout, "  mean provider tokens:   %.1f\n", sum.MeanProvider)
	fmt.Fprintf(stdout, "  estimator:provider ratio (mean): %.3f\n", sum.MeanRatio)
	fmt.Fprintf(stdout, "  estimator:provider ratio (min):  %.3f\n", sum.MinRatio)
	fmt.Fprintf(stdout, "  estimator:provider ratio (max):  %.3f\n", sum.MaxRatio)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Interpretation: ratio < 1.0 means the estimator under-counts (safe for rollover,")
	fmt.Fprintln(stdout, "since UsageCounter takes the larger of the two). ratio > 1.0 means the estimator")
	fmt.Fprintln(stdout, "over-counts, which would roll over early but never overflow.")
	fmt.Fprintln(stdout, "Scope: estimator counts message Content runes / 4 only — not tool-call JSON,")
	fmt.Fprintln(stdout, "system prompt, or role tokens — so this is estimator-vs-prompt_tokens, not")
	fmt.Fprintln(stdout, "estimator-vs-full-prompt. A ratio near 1.0 means the heuristic tracks the")
	fmt.Fprintln(stdout, "provider's own count well for this workload.")
	return nil
}
```

Wire the subcommand in `cmd/marshal/main.go`. Add a dispatch branch in `run` (after the `acp` branch, ~line 28):

```go
	if len(args) > 0 && args[0] == "calibrate-tokens" {
		return calibrateRunner(ctx, args[1:], stdout)
	}
```

And add the var alongside `historyRunner` (near line 15):

```go
var calibrateRunner = runCalibrateTokens
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -run TestRunCalibrateTokens -v`
Expected: PASS for both tests.

Run: `go build ./...`
Expected: builds.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/calibrate.go cmd/marshal/calibrate_test.go cmd/marshal/main.go
git commit -m "feat(cmd): marshal calibrate-tokens --from-db report

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Task 4: Update the feature spec

**Files:**
- Modify: `docs/marshal-context-rollover-spec.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: Flip P2 item 3 to implemented**

Under "P2 — deferred", replace the unchecked calibration bullet:

```markdown
- [ ] A calibration pass comparing `EstimatorCounter` against real provider usage. `UsageCounter` already reports the larger of the two, which bounds the error in the safe direction; the measurement is still worth doing.
```

with:

```markdown
- [x] A calibration pass comparing `EstimatorCounter` against real provider usage. Implemented as a `token_calibration` table (`internal/db/calibration.go`) recording one paired estimator-vs-`prompt_tokens` observation per chat turn when `[session.rollover.calibration] enabled = true` (off by default), plus a `marshal calibrate-tokens --from-db` report aggregating the recorded samples into mean/min/max ratios. The report states its scope explicitly: `EstimatorCounter` counts only message `Content` runes / 4 — not tool-call JSON, the system prompt, or role tokens — so the ratio is estimator-vs-`prompt_tokens`, not estimator-vs-full-prompt. `UsageCounter` already bounds the error in the safe direction (it takes the larger of the two); this measurement quantifies the magnitude.
```

- [ ] **Step 2: Document the calibration config block**

In the Configuration TOML block (spec ~line 178-189), after the `blob_threshold_bytes` line, add:

```toml
[session.rollover.calibration]
enabled = false            # record estimator-vs-provider token pairs per turn for the calibrate-tokens report
```

- [ ] **Step 3: Commit**

```bash
git add docs/marshal-context-rollover-spec.md
git commit -m "docs: mark rollover estimator calibration implemented (P2.3)

Claude-Session: https://claude.ai/code/session_01QwpTHE7NinxkyLyiYgqzP5"
```

---

## Final verification

- [ ] Run `gofmt -w .` and `go vet ./...`.
- [ ] Run the full suite: `go test ./...` — expected PASS.
- [ ] Confirm `calibration.enabled` defaults to `false`, so a default-config session is unchanged (existing `rollover_test.go` / `controller_test.go` / `rollover_wiring_test.go` still green).
- [ ] Confirm the `marshal calibrate-tokens --from-db` subcommand prints "No calibration samples" on a fresh project and a populated report after samples exist.