# Loop Reliability Trio Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the agent loop finish tasks it currently wastes: stop malformed JSON from burning the tool budget (parse-correction budget), stop repeated failing tool calls at the source (actionable error feedback), and stop context overflow from silently degrading instruction-following (mid-turn compaction).

**Architecture:** Three independent changes inside the existing loop. (1) The tool loop stops counting parse failures against `MaxToolIterations`; instead a consecutive-failure counter drives an escalation ladder (correction → strict repair + JSON mode → salvage/fail). (2) `file.read` and `file.write_patch` errors are enriched at the tool layer with concrete recovery data (closest paths, nearest file region). (3) A deterministic, marker-based compactor shrinks old tool-result messages when the turn transcript exceeds a configurable token budget.

**Tech Stack:** Go stdlib only. Tests use the existing `scriptedProvider` harness and the eval table.

**Prerequisite:** The turn-telemetry plan (`docs/superpowers/plans/2026-07-07-turn-telemetry-and-evals.md`) must be merged first — this plan edits `internal/agent/metrics.go` collection points and extends `internal/agent/eval_scenarios_test.go`.

## Global Constraints

- Work on branch `loop-reliability` (create from `main` after the telemetry branch merges: `git checkout -b loop-reliability`).
- Build and test with CGO enabled: `CGO_ENABLED=1 go test ./...`.
- `gofmt -l .` must print nothing and `go vet ./...` must be clean before every commit, except the documented pre-existing `internal/app/app.go` mutex-copy vet warning.
- Salvage-reason vocabulary grows from `"" | "stalled" | "exhausted"` to also include `"malformed"` — update the comment on `TurnMetrics.SalvageReason` when you touch it; the DB column is TEXT and needs no migration.
- **Iterations semantics change:** `TurnMetrics.Iterations` becomes "parsed actions executed," not "model calls made." The telemetry eval row `parse failure recovers to an answer` currently asserts `Iterations=2`; Task 1 updates it to `Iterations=1`. This is the only existing assertion the semantics change touches.
- No new dependencies. No LLM-based summarization in the compactor — it must be deterministic.
- The TUI is not touched by this plan.

---

### Task 1: Parse-correction budget with escalation

Parse failures currently consume tool iterations (`runner.go`, the `parseErr != nil` branch does `continue` inside `for iteration := 0; iteration < r.MaxToolIterations; iteration++`). A weak model emitting bad JSON can burn all 16 iterations and produce nothing. After this task: parse failures never consume iterations, the second consecutive failure escalates to a strict repair prompt (and enables provider JSON mode when available), and the third consecutive failure ends the turn (Task 2 handles how it ends).

**Files:**
- Modify: `internal/agent/runner.go` (loop restructure in `RunTask`)
- Modify: `internal/agent/prompts.go` (add `BuildRepairMessage`)
- Modify: `internal/agent/eval_scenarios_test.go` (update `Iterations` assertion in the parse-recovery row)
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `BuildCorrectionMessage(err error) schema.ChatMessage` (prompts.go), `r.withStats` (metrics.go), `turnProvider.Capabilities(ctx)` (provider interface).
- Produces (Task 2 relies on these):
  - `maxConsecutiveParseFailures = 3` constant in runner.go.
  - The loop-local `consecutiveParseFailures int` counter and the `iteration`-only-on-parse-success structure.
  - `BuildRepairMessage(parseErr error) schema.ChatMessage` in prompts.go.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/runner_test.go`:

```go
func TestParseFailuresDoNotConsumeToolIterations(t *testing.T) {
	// Two garbage responses, then a read, then final. With MaxToolIterations=2
	// the old loop would exhaust before reaching the final answer; the new
	// loop only counts parsed actions, so the turn completes normally.
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "x"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &scriptedProvider{responses: []string{
		"garbage one",
		"garbage two",
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 2
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "question")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Answer." || task.SalvagedReason != "" {
		t.Fatalf("task = %+v, want clean answer despite 2 parse failures", task)
	}
}

func TestSecondConsecutiveParseFailureEscalatesToRepair(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{
		"garbage one",
		"garbage two",
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	// Third request must contain the strict repair directive, not just the
	// mild correction.
	if len(p.requests) < 3 {
		t.Fatalf("expected >= 3 provider calls, got %d", len(p.requests))
	}
	lastReq := p.requests[2]
	found := false
	for _, m := range lastReq.Messages {
		if strings.Contains(m.Content, "Respond with ONLY one JSON object") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the strict repair message in the third request")
	}
}

func TestSecondConsecutiveParseFailureEnablesJSONMode(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{
		responses: []string{
			"garbage one",
			"garbage two",
			`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
		},
		capabilities: schema.ProviderCapabilities{JSONMode: true},
	}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	if _, err := r.RunTask(context.Background(), "question"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	third := p.requests[2]
	if third.ResponseFormat == nil || third.ResponseFormat.Type != "json_object" {
		t.Fatalf("third request ResponseFormat = %+v, want json_object enabled after repeated parse failures", third.ResponseFormat)
	}
}
```

The third test needs `scriptedProvider` to report capabilities. In `runner_test.go`, add the field and change the method:

```go
type scriptedProvider struct {
	responses    []string
	thinking     []string
	errs         []error
	usages       []*schema.TokenUsage
	capabilities schema.ProviderCapabilities
	calls        int
	requests     []schema.ChatRequest
}

func (p *scriptedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}
```

(The existing method returns a zero value; this change is behavior-preserving for all existing tests.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestParseFailuresDoNotConsume|TestSecondConsecutiveParseFailure' -v`
Expected: `TestParseFailuresDoNotConsumeToolIterations` FAILS (turn exhausts/salvages instead of answering); the other two FAIL (no repair message, no ResponseFormat).

- [ ] **Step 3: Add BuildRepairMessage**

Append to `internal/agent/prompts.go`:

```go
// BuildRepairMessage is the escalated correction used after consecutive
// parse failures: unlike BuildCorrectionMessage it restates the full
// required envelope inline, because a model that failed twice needs the
// shape in front of it, not a description of the error.
func BuildRepairMessage(parseErr error) schema.ChatMessage {
	return schema.ChatMessage{
		Role: schema.RoleUser,
		Content: fmt.Sprintf(
			`Your last response could not be parsed: %s.

Respond with ONLY one JSON object in exactly this shape and nothing else — no prose, no markdown fences:

{"rationale": "<one sentence>", "action": {"type": "tool_call" | "patch" | "answer" | "final", "tool": "<tool name, for tool_call only>", "args": { }, "content": "<for patch/answer/final only>"}}`,
			parseErr.Error(),
		),
	}
}
```

- [ ] **Step 4: Restructure the loop**

In `internal/agent/runner.go`:

(a) Add to the `const` block at the top of the file:

```go
	// maxConsecutiveParseFailures bounds how many times in a row the loop
	// re-prompts a model whose output cannot be parsed before giving up on
	// the turn. Parse failures do not consume tool iterations.
	maxConsecutiveParseFailures = 3
```

(b) Replace the loop header and the parse-error branch. The current shape is:

```go
	for iteration := 0; iteration < r.MaxToolIterations; iteration++ {
		r.State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})
		r.withStats(func(s *turnStats) { s.m.Iterations = iteration + 1 })
		...
		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			messages = append(messages, BuildCorrectionMessage(parseErr))
			r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
			continue
		}
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
		producedValidAction = true
		...
	}
```

Change it to:

```go
	iteration := 0
	consecutiveParseFailures := 0
	for iteration < r.MaxToolIterations {
		r.State.SetToolBudget(session.ToolBudget{Used: iteration, Max: r.MaxToolIterations})
		...
		action, parseErr := ParseAction(raw)
		if parseErr != nil {
			consecutiveParseFailures++
			r.withStats(func(s *turnStats) { s.m.ParseFailures++ })
			messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
			if consecutiveParseFailures >= maxConsecutiveParseFailures {
				break // Task 2 turns this into salvage-or-fail below the loop.
			}
			if consecutiveParseFailures >= 2 {
				// Escalate: full envelope inline, and constrain decoding when
				// the provider can. Enabling ResponseFormat on the Runner is
				// deliberate — once a model has proven it needs JSON mode,
				// keeping it on for later turns only helps.
				if r.ResponseFormat == nil && turnProvider.Capabilities(ctx).JSONMode {
					r.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}
				}
				messages = append(messages, BuildRepairMessage(parseErr))
			} else {
				messages = append(messages, BuildCorrectionMessage(parseErr))
			}
			continue
		}
		consecutiveParseFailures = 0
		iteration++
		r.withStats(func(s *turnStats) { s.m.Iterations = iteration })
		messages = append(messages, schema.ChatMessage{Role: schema.RoleAssistant, Content: raw})
		producedValidAction = true
		...
	}
```

The `...` sections (pressure message, skills refresh, chat call; and the action dispatch below the parse branch) are unchanged — do not modify them in this task. Note the pressure-message check `r.MaxToolIterations-iteration <= finalizePressureThreshold` keeps working because `iteration` still ranges over the same values.

- [ ] **Step 5: Update the telemetry eval row**

In `internal/agent/eval_scenarios_test.go`, the row `parse failure recovers to an answer` asserts `m.Iterations != 2`. Change it to assert the new semantics:

```go
				if m.Outcome != "answered" || m.ParseFailures != 1 || m.ToolCalls != 0 || m.Iterations != 1 {
					t.Fatalf("metrics = %+v", m)
				}
```

(Iterations now counts parsed actions — the garbage response no longer counts.)

- [ ] **Step 6: Run the tests, then the package**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestParseFailures|TestSecondConsecutive|TestEvalScenarios' -v`
Expected: all PASS.

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`
Expected: `ok`. `TestExhaustionWithoutValidActionFailsHard` still passes at this point because three consecutive failures `break` out and the existing `producedValidAction == false` path below the loop returns `ErrMaxIterationsExceeded` (Task 2 refines the error).

- [ ] **Step 7: Format, vet, commit**

```bash
gofmt -w internal/agent
go vet ./internal/agent/...
git add internal/agent/runner.go internal/agent/prompts.go internal/agent/runner_test.go internal/agent/eval_scenarios_test.go
git commit -m "feat(agent): parse failures escalate instead of consuming the tool budget"
```

---

### Task 2: Salvage or fail cleanly on persistent malformed output

Three consecutive parse failures now `break` out of the loop. Define what happens next: if the turn produced at least one valid action, salvage via `finalize` with a new `"malformed"` reason; otherwise fail with a specific error instead of the misleading `ErrMaxIterationsExceeded`.

**Files:**
- Modify: `internal/agent/runner.go` (post-loop exit handling, new sentinel error)
- Modify: `internal/agent/finalize.go` (new `finalizeReason`, fallback copy)
- Modify: `internal/agent/metrics.go` (SalvageReason comment)
- Modify: `internal/agent/eval_scenarios_test.go` (two new rows)
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes from Task 1: the `break` at `maxConsecutiveParseFailures` and the `consecutiveParseFailures` counter.
- Produces: `reasonMalformed finalizeReason = "malformed"`, `var ErrModelOutputMalformed = errors.New("agent: model could not produce a parseable action after repeated corrections")`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/runner_test.go`:

```go
func TestPersistentMalformedOutputSalvagesWhenWorkExists(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: "x"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// One valid read, then garbage forever (scriptedProvider repeats the
	// last response). finalize's own attempts also receive garbage, so the
	// salvage is synthesized.
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		"garbage",
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	task, err := r.RunTask(context.Background(), "question")
	if err != nil {
		t.Fatalf("RunTask err = %v, want salvage", err)
	}
	if task.Status != TaskStatusCompleted || task.SalvagedReason != "malformed" {
		t.Fatalf("task = %+v, want completed with SalvagedReason=malformed", task)
	}
}

func TestPersistentMalformedOutputFailsFastWithoutWork(t *testing.T) {
	state := newTestState(t)
	p := &scriptedProvider{responses: []string{"never valid json"}}
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))

	_, err := r.RunTask(context.Background(), "question")
	if !errors.Is(err, ErrModelOutputMalformed) {
		t.Fatalf("err = %v, want ErrModelOutputMalformed", err)
	}
	// The key win: 3 model calls, not MaxToolIterations (16).
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (fail fast)", p.calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestPersistentMalformed' -v`
Expected: first test FAILS (`SalvagedReason` is `"exhausted"`, not `"malformed"`); second FAILS (`ErrModelOutputMalformed` undefined — compile error counts).

- [ ] **Step 3: Implement**

(a) In `internal/agent/runner.go`, next to `ErrMaxIterationsExceeded`:

```go
var ErrModelOutputMalformed = errors.New("agent: model could not produce a parseable action after repeated corrections")
```

(b) In `internal/agent/finalize.go`, extend the reason constants:

```go
	reasonMalformed finalizeReason = "malformed"
```

and add a case to the `switch reason` inside `synthesizeFallback`, alongside the existing exhausted/stalled cases:

```go
	case reasonMalformed:
		b.WriteString("I could not produce a well-formed next step, so here is a summary of progress so far.\n\n")
```

(match the surrounding cases' style — look at them before writing; they build the same `b strings.Builder`).

(c) In `internal/agent/runner.go`, the post-loop code currently reads:

```go
	if producedValidAction {
		if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, reasonExhausted); ferr == nil {
			return res, nil
		}
	}
	task.Status = TaskStatusFailed
	r.State.AddMessage(session.RoleSystem, "Agent stopped: exceeded max tool iterations without a final answer.", session.ContentTypePlain)
	return task, ErrMaxIterationsExceeded
```

Replace with:

```go
	exitReason := reasonExhausted
	exitErr := ErrMaxIterationsExceeded
	exitNote := "Agent stopped: exceeded max tool iterations without a final answer."
	if consecutiveParseFailures >= maxConsecutiveParseFailures {
		exitReason = reasonMalformed
		exitErr = ErrModelOutputMalformed
		exitNote = "Agent stopped: the model could not produce a parseable action after repeated corrections."
	}
	if producedValidAction {
		if res, ferr := r.finalize(ctx, turnProvider, turnModel, messages, task, exitReason); ferr == nil {
			return res, nil
		}
	}
	task.Status = TaskStatusFailed
	r.State.AddMessage(session.RoleSystem, exitNote, session.ContentTypePlain)
	return task, exitErr
```

(d) In `internal/agent/metrics.go`, update the `SalvageReason` field comment to `// "" | "stalled" | "exhausted" | "malformed"`.

- [ ] **Step 4: Add the eval rows**

Append two rows to the `cases` table in `internal/agent/eval_scenarios_test.go`:

```go
		{
			name: "persistent malformed output salvages prior work",
			responses: []string{
				evalRead("a.go"),
				"garbage forever",
			},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "salvaged" || m.SalvageReason != "malformed" || m.ParseFailures < 3 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
		{
			name:       "escalated repair recovers a weak model",
			responses:  []string{"garbage", "garbage", `{"rationale":"done","action":{"type":"final","content":"Answer."}}`},
			forceClass: ClassQuestion,
			want: func(t *testing.T, m TurnMetrics) {
				if m.Outcome != "answered" || m.ParseFailures != 2 || m.Iterations != 1 {
					t.Fatalf("metrics = %+v", m)
				}
			},
		},
```

Note the first row's `RunTask` returns nil error (salvaged); the table's existing `if err != nil { t.Fatalf }` handling is correct for it.

- [ ] **Step 5: Run everything, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...`
Expected: all PASS, including `TestExhaustionWithoutValidActionFailsHard` — verify it still asserts `ErrMaxIterationsExceeded`; with `MaxToolIterations=2` and garbage responses the parse cap (3) triggers first, so that test's expectation changes to `ErrModelOutputMalformed`. If it fails on that, update the test's expected error to `ErrModelOutputMalformed` and its comment — the swarm's "broken planner" detection intent is preserved by any non-nil error; check `internal/agent/swarm` for `ErrMaxIterationsExceeded` references (`grep -rn ErrMaxIterationsExceeded internal/agent/swarm/`) and update comparisons to also accept the new error if any exist.

```bash
gofmt -w internal/agent
go vet ./internal/agent/...
git add internal/agent
git commit -m "feat(agent): salvage or fail fast on persistently malformed model output"
```

---

### Task 3: Actionable file.read errors — closest-path suggestions

`file.read` on a missing path returns `stat foo.go: no such file or directory`, and weak models retry blindly. Enrich the error with up to 3 closest known paths from the repo file index.

**Files:**
- Modify: `internal/db/files.go` (new query)
- Modify: `internal/tools/native/file.go` (enrich the stat-failure error)
- Test: `internal/db/files_test.go`, `internal/tools/native/file_test.go`

**Interfaces:**
- Consumes: `toolSet` fields `db *db.DB`, `projectID int64` (native.go); `files` table (`project_id`, `path` columns).
- Produces: `func (db *DB) FilesMatchingBasename(projectID int64, basename string, limit int) ([]string, error)`.

- [ ] **Step 1: Write the failing DB test**

Append to `internal/db/files_test.go` (reuse the file's existing DB-setup helper if one exists — check the top of the file; otherwise open+migrate a temp DB as in `turnmetrics_test.go`):

```go
func TestFilesMatchingBasename(t *testing.T) {
	database, projectID := openMetricsTestDB(t) // helper from turnmetrics_test.go, same package
	files := []FileIndex{
		{Path: "internal/agent/runner.go", Hash: "h1", SizeBytes: 1, Language: "go", LastIndexedAt: time.Now()},
		{Path: "internal/agent/runner_test.go", Hash: "h2", SizeBytes: 1, Language: "go", LastIndexedAt: time.Now()},
		{Path: "cmd/marshal/main.go", Hash: "h3", SizeBytes: 1, Language: "go", LastIndexedAt: time.Now()},
	}
	if err := database.SaveFileIndex(projectID, files); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}

	got, err := database.FilesMatchingBasename(projectID, "runner.go", 5)
	if err != nil {
		t.Fatalf("FilesMatchingBasename: %v", err)
	}
	if len(got) != 2 || got[0] != "internal/agent/runner.go" {
		t.Fatalf("got %v, want the two runner*.go paths, exact basename first", got)
	}

	if got, _ := database.FilesMatchingBasename(projectID, "nomatch.txt", 5); len(got) != 0 {
		t.Fatalf("got %v, want no matches", got)
	}
}
```

Check the exact `FileIndex` struct fields first (`grep -n "type FileIndex" -A 10 internal/db/files.go`) and adjust the literal to match; do not guess field names — the assertion logic stays the same.

- [ ] **Step 2: Run to verify failure, then implement the query**

Run: `CGO_ENABLED=1 go test ./internal/db/ -run TestFilesMatchingBasename -v` — expect compile failure (`FilesMatchingBasename` undefined).

Append to `internal/db/files.go`:

```go
// FilesMatchingBasename returns indexed paths whose final path element
// contains basename (case-insensitive), exact basename matches first. Used
// to build "did you mean" suggestions for failed file reads.
func (db *DB) FilesMatchingBasename(projectID int64, basename string, limit int) ([]string, error) {
	rows, err := db.sqlDB.Query(
		`SELECT path FROM files
		 WHERE project_id = ?
		   AND LOWER(path) LIKE '%' || LOWER(?) || '%'
		 ORDER BY (LOWER(path) LIKE '%/' || LOWER(?)) DESC, LENGTH(path) ASC
		 LIMIT ?`,
		projectID, basename, basename, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query files by basename: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan file path: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
```

Run the test again — expect PASS (adjust the ORDER BY expectation only if the exact-basename-first ordering assertion fails; the requirement is: both matches returned, deterministic order with the exact match first).

- [ ] **Step 3: Write the failing tool test and enrich file.read**

Append to `internal/tools/native/file_test.go` (mirror the file's existing toolSet construction — check how other tests in that file build `toolSet`/call handlers, and whether a test DB is already wired; if the existing tests build the toolSet without a DB, construct one with `db.Open` + `Migrate` + `GetOrCreateProject` + `SaveFileIndex` and set the `db`/`projectID` fields):

```go
func TestFileReadMissingFileSuggestsClosestPaths(t *testing.T) {
	// Setup: workspace temp dir, a DB whose file index knows
	// "internal/agent/runner.go", and a file.read call for "runner.go".
	// (Build toolSet exactly as the neighboring tests in this file do,
	// then set its db and projectID fields to the test DB.)
	...
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "file.read",
		Args: json.RawMessage(`{"path":"runner.go"}`),
	})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "internal/agent/runner.go") {
		t.Fatalf("error %q does not suggest the indexed path", err)
	}
	if !strings.Contains(err.Error(), "closest indexed paths") {
		t.Fatalf("error %q missing the suggestion preamble", err)
	}
}
```

(The `...` is test setup that MUST follow the existing conventions in `file_test.go` — read that file's first test before writing; the handler-invocation and assertion halves above are complete and non-negotiable.)

In `internal/tools/native/file.go`, replace the stat-failure return in `fileReadTool`:

```go
		info, err := os.Stat(path)
		if err != nil {
			return registry.ToolResult{}, t.enrichMissingFileError(args.Path, err)
		}
```

and add to the same file:

```go
// enrichMissingFileError appends "did you mean" candidates from the repo
// file index so the model can correct the path instead of retrying blindly.
func (t *toolSet) enrichMissingFileError(requested string, statErr error) error {
	base := fmt.Errorf("stat %s: %w", requested, statErr)
	if t.db == nil || !os.IsNotExist(statErr) {
		return base
	}
	suggestions, err := t.db.FilesMatchingBasename(t.projectID, filepath.Base(requested), 3)
	if err != nil || len(suggestions) == 0 {
		return base
	}
	return fmt.Errorf("%w — file not found; closest indexed paths: %s", base, strings.Join(suggestions, ", "))
}
```

Add `"path/filepath"` to file.go's imports.

- [ ] **Step 4: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/db/... ./internal/tools/native/...`
Expected: all PASS.

```bash
gofmt -w internal/db internal/tools/native
go vet ./internal/db/... ./internal/tools/native/...
git add internal/db/files.go internal/db/files_test.go internal/tools/native/file.go internal/tools/native/file_test.go
git commit -m "feat(tools): suggest closest indexed paths when file.read misses"
```

---

### Task 4: Actionable patch errors — nearest-region context

`patch.ValidatePatch` returns `search block not found in <path>` with nothing else; the model's retry has no better information than its first attempt. Return the closest-matching region of the actual file so the next SEARCH block can be correct.

**Files:**
- Modify: `internal/tools/patch/diff.go` (add `NearestRegion`, enrich `ValidatePatch`'s not-found error)
- Test: `internal/tools/patch/diff_test.go`

**Interfaces:**
- Consumes: `FilePatch`/`Chunk` types (`internal/tools/patch/parser.go` — check exact field names `Search`/`Replace` before coding; they are used at diff.go:11).
- Produces: `func NearestRegion(content, search string) string` (exported for reuse), and the enriched error text `search block not found in <path>; closest region in the file is:\n<lines>`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tools/patch/diff_test.go`:

```go
func TestNearestRegionFindsClosestWindow(t *testing.T) {
	content := "package main\n\nfunc a() {\n\tx := 1\n\ty := 2\n\treturn\n}\n\nfunc b() {\n\tz := 3\n}\n"
	// Search block that ALMOST matches func a's body (y := 2 vs y := 99).
	search := "func a() {\n\tx := 1\n\ty := 99\n\treturn\n}"

	region := NearestRegion(content, search)
	if !strings.Contains(region, "y := 2") {
		t.Fatalf("region %q should contain the actual near-miss line", region)
	}
	if strings.Contains(region, "func b()") {
		t.Fatalf("region %q matched the wrong window", region)
	}
}

func TestNearestRegionEmptyInputs(t *testing.T) {
	if got := NearestRegion("", "anything"); got != "" {
		t.Fatalf("got %q, want empty for empty content", got)
	}
	if got := NearestRegion("some content", ""); got != "" {
		t.Fatalf("got %q, want empty for empty search", got)
	}
}

func TestValidatePatchNotFoundIncludesNearestRegion(t *testing.T) {
	content := "line one\nline two\nline three\n"
	fp := FilePatch{Path: "x.txt", Chunks: []Chunk{{Search: "line one\nline TWO\nline three", Replace: "r"}}}
	ok, err := ValidatePatch(content, fp)
	if ok || err == nil {
		t.Fatal("expected validation failure")
	}
	if !strings.Contains(err.Error(), "closest region") || !strings.Contains(err.Error(), "line two") {
		t.Fatalf("error %q should include the nearest actual region", err)
	}
}
```

(Adjust `FilePatch{...Chunks: []Chunk{...}}` literal to the exact type/field names in parser.go — `grep -n "type FilePatch\|type Chunk" -A 8 internal/tools/patch/parser.go` — the test logic is fixed.)

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/tools/patch/ -run 'TestNearestRegion|TestValidatePatchNotFound' -v`
Expected: compile error (`NearestRegion` undefined). Red; proceed.

- [ ] **Step 3: Implement**

Append to `internal/tools/patch/diff.go`:

```go
// NearestRegion returns the window of content lines (same line count as the
// search block, up to 20) that best matches the search block, formatted for
// inclusion in an error message. It is deterministic and O(lines × window):
// scoring is the count of exactly-matching lines per window. Returns "" when
// either input is empty or nothing matches at all.
func NearestRegion(content, search string) string {
	if content == "" || search == "" {
		return ""
	}
	contentLines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	searchLines := strings.Split(strings.ReplaceAll(search, "\r\n", "\n"), "\n")
	window := len(searchLines)
	if window > 20 {
		window = 20
	}
	if window > len(contentLines) {
		window = len(contentLines)
	}
	if window == 0 {
		return ""
	}

	bestScore, bestStart := 0, -1
	for start := 0; start+window <= len(contentLines); start++ {
		score := 0
		for i := 0; i < window; i++ {
			if i < len(searchLines) && strings.TrimSpace(contentLines[start+i]) == strings.TrimSpace(searchLines[i]) {
				score++
			}
		}
		if score > bestScore {
			bestScore, bestStart = score, start
		}
	}
	if bestStart < 0 {
		return ""
	}
	return strings.Join(contentLines[bestStart:bestStart+window], "\n")
}
```

Change the not-found error in `ValidatePatch`:

```go
		if count == 0 {
			if region := NearestRegion(normContent, normSearch); region != "" {
				return false, fmt.Errorf("search block not found in %s; closest region in the file is:\n%s\n(make your SEARCH block match these lines exactly)", fp.Path, region)
			}
			return false, fmt.Errorf("search block not found in %s", fp.Path)
		}
```

- [ ] **Step 4: Run, format, vet, commit**

Run: `CGO_ENABLED=1 go test -count=1 ./internal/tools/patch/... ./internal/tools/native/...`
Expected: all PASS (the native package's patch-failure test at `file.go:125` passes the enriched error text through `%v` — its assertions on "patch validation failed" still hold).

```bash
gofmt -w internal/tools/patch
go vet ./internal/tools/patch/...
git add internal/tools/patch/diff.go internal/tools/patch/diff_test.go
git commit -m "feat(patch): include the nearest file region in search-block mismatch errors"
```

---

### Task 5: Mid-turn transcript compaction

The `messages` slice grows unbounded within a turn; past a local model's window, the provider silently truncates and instruction-following collapses. Add a deterministic compactor: when the estimated transcript exceeds a configurable token budget, old tool-result messages are shrunk to their summary line.

**Files:**
- Create: `internal/agent/compact.go`
- Modify: `internal/agent/runner.go` (hook before each chat call in the loop; new `MaxTurnContextTokens` field)
- Modify: `internal/app/config/config.go` (new `[agent] max_turn_context_tokens` key — add the field everywhere `MaxToolIterations` appears: the `AgentConfig` struct, `Default()`, the file-config pointer mirror, and the merge function; `grep -n MaxToolIterations internal/app/config/config.go` lists every site)
- Modify: `internal/app/app.go` (wire config → `runner.MaxTurnContextTokens` in `buildAgentRunner` and the swarm factory, next to the `MaxToolIterations` wiring)
- Test: `internal/agent/compact_test.go`, plus one config merge assertion in the config package's existing merge test file

**Interfaces:**
- Consumes: `BuildToolResultMessage` format (`"Tool %s result: %s"` first line — compact.go depends on this marker; a doc comment on `BuildToolResultMessage` must note the coupling).
- Produces: `func estimateTokens(messages []schema.ChatMessage) int`, `func compactMessages(messages []schema.ChatMessage, budgetTokens, keepRecent int) []schema.ChatMessage`, `Runner.MaxTurnContextTokens int` (0 disables), config default `16384`.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/compact_test.go`:

```go
package agent

import (
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func toolResultMsg(name, body string) schema.ChatMessage {
	return schema.ChatMessage{Role: schema.RoleUser, Content: "Tool " + name + " result: ok\n\n" + body}
}

func TestEstimateTokensCharsOverFour(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: strings.Repeat("a", 400)},
		{Role: schema.RoleUser, Content: strings.Repeat("b", 400)},
	}
	if got := estimateTokens(msgs); got != 200 {
		t.Fatalf("estimateTokens = %d, want 200 (800 chars / 4)", got)
	}
}

func TestCompactMessagesUnderBudgetIsNoOp(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		toolResultMsg("file.read", strings.Repeat("x", 100)),
	}
	got := compactMessages(msgs, 1000, 4)
	if len(got) != 2 || got[1].Content != msgs[1].Content {
		t.Fatal("under-budget transcript must not be modified")
	}
}

func TestCompactMessagesShrinksOldToolResultsOnly(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens each
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system prompt"},
		{Role: schema.RoleUser, Content: "the goal"},
		toolResultMsg("file.read", big),   // old: compacted
		toolResultMsg("repo.search", big), // old: compacted
		{Role: schema.RoleAssistant, Content: "an action"},
		toolResultMsg("file.read", big), // recent (within keepRecent=2): kept
		{Role: schema.RoleAssistant, Content: "another action"},
	}
	got := compactMessages(msgs, 1200, 2)

	if got[0].Content != "system prompt" || got[1].Content != "the goal" {
		t.Fatal("system prompt and goal must never be compacted")
	}
	if strings.Contains(got[2].Content, big) || strings.Contains(got[3].Content, big) {
		t.Fatal("old tool results were not compacted")
	}
	if !strings.Contains(got[2].Content, "compacted") {
		t.Fatalf("compacted message %q missing marker", got[2].Content)
	}
	if !strings.Contains(got[5].Content, big) {
		t.Fatal("recent tool result within keepRecent must be kept verbatim")
	}
	if got[4].Content != "an action" {
		t.Fatal("assistant messages must never be compacted")
	}
	if estimateTokens(got) >= estimateTokens(msgs) {
		t.Fatal("compaction did not shrink the transcript")
	}
}

func TestCompactMessagesDoesNotMutateInput(t *testing.T) {
	big := strings.Repeat("x", 4000)
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "sys"},
		toolResultMsg("file.read", big),
		toolResultMsg("file.read", big),
		toolResultMsg("file.read", big),
	}
	before := msgs[1].Content
	_ = compactMessages(msgs, 100, 0)
	if msgs[1].Content != before {
		t.Fatal("compactMessages must return a new slice, not mutate the caller's")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestEstimateTokens|TestCompactMessages' -v`
Expected: compile error (`estimateTokens`, `compactMessages` undefined). Red; proceed.

- [ ] **Step 3: Implement compact.go**

Create `internal/agent/compact.go`:

```go
package agent

import (
	"strings"

	"marshal/internal/llm/schema"
)

// toolResultPrefix must match BuildToolResultMessage's first line. The
// compactor identifies tool-result messages by this marker rather than by
// tracking indices through every append site in the loop.
const toolResultPrefix = "Tool "

const compactedNote = "\n\n[full output compacted to fit the context budget — re-run the tool if you need it again]"

// estimateTokens is the rough 4-chars-per-token heuristic used across
// Marshal (see DefaultMaxToolResultChars).
func estimateTokens(messages []schema.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / 4
}

// compactMessages returns a copy of messages where, while the estimated
// token count exceeds budgetTokens, the oldest still-verbatim tool-result
// message (excluding the last keepRecent messages) is shrunk to its first
// line plus a note. Message count and order are preserved so the model's
// view of the conversation stays coherent. budgetTokens <= 0 disables
// compaction.
func compactMessages(messages []schema.ChatMessage, budgetTokens, keepRecent int) []schema.ChatMessage {
	if budgetTokens <= 0 || estimateTokens(messages) <= budgetTokens {
		return messages
	}
	out := append([]schema.ChatMessage(nil), messages...)
	cutoff := len(out) - keepRecent
	// Index 0 is the system prompt; index 1 is the context pack or goal.
	// Neither is ever a tool result, but start at 1 anyway for safety.
	for i := 1; i < cutoff; i++ {
		m := out[i]
		if m.Role != schema.RoleUser || !strings.HasPrefix(m.Content, toolResultPrefix) {
			continue
		}
		if strings.HasSuffix(m.Content, compactedNote) {
			continue // already compacted
		}
		firstLine, _, _ := strings.Cut(m.Content, "\n")
		out[i].Content = firstLine + compactedNote
		if estimateTokens(out) <= budgetTokens {
			break
		}
	}
	return out
}
```

Also add one sentence to the doc comment of `BuildToolResultMessage` in `internal/agent/prompts.go`: `// The "Tool <name> result:" first line is a load-bearing marker: compactMessages identifies tool results by it.`

- [ ] **Step 4: Verify compact tests pass, then hook into the loop**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestEstimateTokens|TestCompactMessages' -v` — expect PASS.

In `internal/agent/runner.go`:

(a) Add to the `Runner` struct, next to `MaxToolResultChars`:

```go
	// MaxTurnContextTokens caps the estimated size of the in-turn message
	// transcript; past it, old tool results are compacted before the next
	// model call. 0 disables compaction.
	MaxTurnContextTokens int
```

(b) In `NewRunner`, add `MaxTurnContextTokens: DefaultMaxTurnContextTokens,` and define next to the other defaults:

```go
	DefaultMaxTurnContextTokens = 16384
	compactKeepRecentMessages   = 6
```

(c) In the `RunTask` loop, immediately before the `raw, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages)` line:

```go
		messages = compactMessages(messages, r.MaxTurnContextTokens, compactKeepRecentMessages)
```

(d) Integration test — append to `internal/agent/compact_test.go`:

```go
func TestRunTaskCompactsWhenOverBudget(t *testing.T) {
	reg := registry.New()
	big := strings.Repeat("x", 6000)
	if err := reg.Register(registry.Tool{
		Name: "file.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok", Content: big}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &scriptedProvider{responses: []string{
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"b.go"}}}`,
		`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"c.go"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
	}}
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxTurnContextTokens = 2000 // two big results blow the budget

	if _, err := r.RunTask(context.Background(), "q"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	// The final request must be under-ish budget: at least one earlier tool
	// result compacted, and the request that produced the final answer must
	// not carry all three big payloads verbatim.
	last := p.requests[len(p.requests)-1]
	verbatim := 0
	compacted := 0
	for _, m := range last.Messages {
		if strings.Contains(m.Content, big) {
			verbatim++
		}
		if strings.Contains(m.Content, "compacted") {
			compacted++
		}
	}
	if compacted == 0 {
		t.Fatal("no compacted messages in the final request")
	}
	if verbatim >= 3 {
		t.Fatal("all big tool results still verbatim — compaction did not run")
	}
}
```

Add `"context"` and `marshal/internal/app/config`, `marshal/internal/tools/policy`, `marshal/internal/tools/registry` to compact_test.go's imports.

Run: `CGO_ENABLED=1 go test -count=1 ./internal/agent/...` — expect all PASS.

- [ ] **Step 5: Config key and app wiring**

In `internal/app/config/config.go`, mirror `MaxToolIterations` exactly (grep lists every site): add `MaxTurnContextTokens int \`toml:"max_turn_context_tokens"\`` to `AgentConfig`; set `MaxTurnContextTokens: 16384` in `Default()`; add `MaxTurnContextTokens *int \`toml:"max_turn_context_tokens"\`` to the file-config `Agent` pointer struct; add the merge assignment in the merge function following the adjacent field's pattern. Add one case to the config package's existing merge test asserting a file value of `8000` overrides the default.

In `internal/app/app.go`, next to the existing `if cfg.Agent.MaxToolIterations > 0 { ... }` in `buildAgentRunner` AND next to `roleToolIterations` usage in the swarm factory:

```go
	if cfg.Agent.MaxTurnContextTokens > 0 {
		runner.MaxTurnContextTokens = cfg.Agent.MaxTurnContextTokens
	}
```

(in the factory the variable is `r`, not `runner`).

Run: `CGO_ENABLED=1 go test -count=1 ./internal/app/...` — expect all PASS.

- [ ] **Step 6: Full suite, format, vet, commit**

```bash
CGO_ENABLED=1 go test -count=1 ./...
gofmt -w internal/agent internal/app
go vet ./internal/agent/... ./internal/app/config/... 2>&1 | grep -v "copies lock value" || true
git add internal/agent/compact.go internal/agent/compact_test.go internal/agent/runner.go internal/agent/prompts.go internal/app/config internal/app/app.go
git commit -m "feat(agent): compact old tool results when the turn transcript exceeds its token budget"
```

---

## Verification

`git log --oneline main..HEAD` shows five commits. `CGO_ENABLED=1 go test -count=1 ./...` green. The eval table now has eight rows and encodes: parse failures cost 3 model calls max instead of 16 iterations; malformed-output turns salvage prior work. Live check: point Marshal at a weak local model, give it a task it previously failed with JSON garbage, and confirm the turn either recovers after the repair message or ends in 3 calls with a readable salvage instead of a 16-iteration spin.
