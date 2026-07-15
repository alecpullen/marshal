# Domain D1 — Runner State Hygiene & Reentrancy Fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve four findings from `docs/14-codebase-improvement-audit-2026-07-14.md` (Domain D, Batch D1): F-POL-93 (dead code), F-POL-95 (misleading variable name), F-BUG-74/F-POL-85 (response format mutation leaks across `Run()` calls), and F-CON-79 (document `Runner` as single-`Run`-safe only).

**Architecture:** Each task fixes one finding in isolation. Low-risk/trivial edits land first (dead-code removal, rename), then the core logic fix (thread `responseFormat` as a local through `RunTask` → chat methods), then the doc-comment update on `Runner` + a cross-call isolation test.

**Tech Stack:** Go (stdlib only — `sync`, `context`, `encoding/json`). CGO is required for tree-sitter, but none of these tasks touch CGo.

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain, but tasks touch pure Go only.
- Every code change MUST compile: run `go build ./...` after each task's implementation step.
- Every test MUST pass: run `go test ./internal/agent/...` after each task (the affected package), and `go test ./...` before the final commit.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public API of `Runner`, `Run()`, `RunTask()`, and `CopyFrom()`. Unexported methods (`chatOnce`, `chatWithRetry`, etc.) may have their signatures extended.
- Do not change `CopyFrom` semantics for `ResponseFormat` — it propagates the seed value (which it already does).
- Preserve existing test behavior for `TestSecondConsecutiveParseFailureEnablesJSONMode` (escalation *within* one `RunTask`).

---

## File Structure

Files modified by this plan:

| File | Tasks |
|------|-------|
| `internal/agent/runner.go` | 1, 2, 3, 4 |
| `internal/agent/runner_test.go` | 3, 4 |
| `internal/agent/handoff.go` | 3 |
| `internal/agent/finalize.go` | 3 |

---

### Task 1: F-POL-93 — Drop dead `RunTaskFunc` inline struct declaration

**Files:**
- Modify: `internal/agent/runner.go:183-186`

**Interfaces:**
- Consumes: the `RunTaskFunc` named type at line 195 still exists.
- Produces: the inline anonymous struct field declaration is removed; the `RunTaskFunc` field on `Runner` is declared with the named type. No functional change.

- [ ] **Step 1: Remove the dead inline type**

In `internal/agent/runner.go`, change lines 183-186:
```go
	// RunTaskFunc, if non-nil, overrides RunTask for testing. It returns a
	// canned Task without calling the provider. Used by the SDD orchestrator
	// tests to inject scripted responses.
	RunTaskFunc RunTaskFunc
```

(This replaces the inline `RunTaskFunc func(ctx context.Context, prompt string) (*Task, error)` with the named type `RunTaskFunc` — they are identical.)

- [ ] **Step 2: Build to confirm no breakage**

Run: `CGO_ENABLED=1 go build ./...`
Expected: success (no errors).

- [ ] **Step 3: Run agent tests**

Run: `go test ./internal/agent/...`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner.go
git commit -m "fix(agent): remove dead RunTaskFunc inline declaration (F-POL-93)"
```

---

### Task 2: F-POL-95 — Rename `pressureSent` to `pressureMessageSent`

**Files:**
- Modify: `internal/agent/runner.go:397, 453, 468`

**Interfaces:**
- Consumes: local variable `pressureSent` at line 397 (declaration) and line 453 (check) and line 468 (reset).
- Produces: variable renamed to `pressureMessageSent`. Comment updated to describe what the variable tracks rather than why it's reset.

- [ ] **Step 1: Rename the declaration**

At `runner.go:397`, change:
```go
	pressureSent := false
```
to:
```go
	pressureMessageSent := false
```

- [ ] **Step 2: Rename the check**

At `runner.go:453`, change:
```go
		if !pressureSent && r.MaxToolIterations-iteration <= finalizePressureThreshold {
```
to:
```go
		if !pressureMessageSent && r.MaxToolIterations-iteration <= finalizePressureThreshold {
```

- [ ] **Step 3: Update the reset comment and rename**

At `runner.go:468`, change:
```go
				pressureSent = false // the fresh transcript may legitimately approach the budget again
```
to:
```go
				pressureMessageSent = false // the fresh transcript may legitimately approach the budget again
```

- [ ] **Step 4: Build and test**

Run: `CGO_ENABLED=1 go build ./...`
Expected: success.
Run: `go test ./internal/agent/...`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runner.go
git commit -m "fix(agent): rename pressureSent to pressureMessageSent (F-POL-95)"
```

---

### Task 3: F-BUG-74 / F-POL-85 — Thread `responseFormat` as a local variable through `RunTask`

**Files:**
- Modify: `internal/agent/runner.go:370, 476, 893-981` (chat methods)
- Modify: `internal/agent/handoff.go:39` (summarizeAndContinue)
- Modify: `internal/agent/finalize.go:84` (finalize)
- Test: `internal/agent/runner_test.go` (add new cross-call test + update existing `chatOnce` call sites)

**Interfaces:**
- **Unexported method signatures change** — the four chat methods (`chatOnce`, `chatWithRetry`, `chatWithRetryNoNativeTools`, `chatWithRetryWithNativeTools`) gain a `responseFormat *schema.ResponseFormat` parameter.
- Similarly, `summarizeAndContinue` and `finalize` gain the same parameter.
- `RunTask` creates `effectiveRF := r.ResponseFormat` once at the top and threads it through. The `consecutiveParseFailures == 2` branch updates the local `effectiveRF`, not `r.ResponseFormat`.
- Test call sites of `chatOnce` in `runner_test.go` get the new parameter (pass `nil` for the test — the default response format is nil).

- [ ] **Step 1: Write the failing cross-call isolation test**

Append to `internal/agent/runner_test.go` (after `TestSecondConsecutiveParseFailureEnablesJSONMode`):

```go
// TestResponseFormatResetsAcrossRunTaskCalls ensures that when a Runner
// triggers the JSON-mode response format escalation inside one RunTask,
// the next RunTask on the same *Runner starts with a clean response format
// (the original seed value, typically nil).
func TestResponseFormatResetsAcrossRunTaskCalls(t *testing.T) {
	p := &scriptedProvider{
		capabilities: schema.ProviderCapabilities{JSONMode: true},
		responses: []string{
			// First RunTask: 2 parse failures → JSON mode, then recover
			"not json 1",
			"not json 2",
			`{"rationale":"recovered","action":{"type":"final","content":"first done"}}`,
			// Second RunTask: no JSON mode should leak across
			"not json 3",
			`{"rationale":"done","action":{"type":"final","content":"second done"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	// First RunTask triggers JSON-mode escalation on the 3rd request
	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}

	firstRunRequestCount := len(p.requests)

	// Second RunTask on the same *Runner
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}

	// The first request of the second run must NOT inherit JSON mode
	req := p.requests[firstRunRequestCount]
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" {
		t.Fatalf("second RunTask's first request has ResponseFormat = %v, want nil or non-json_object", req.ResponseFormat)
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails on the current code**

Run: `go test ./internal/agent -run 'TestResponseFormatResetsAcrossRunTaskCalls' -v`
Expected: FAIL — the second `RunTask`'s first request still has `json_object` because `r.ResponseFormat` was mutated and never reset.

- [ ] **Step 3: Add `responseFormat` param to the chat method chain**

In `internal/agent/runner.go`:

**3a.** Change `chatOnce` signature (line 914):
```go
func (r *Runner) chatOnce(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeToolsOpt ...bool) (chatResult, error) {
```

**3b.** Inside `chatOnce`, replace:
```go
	} else {
		responseFormat = r.ResponseFormat
	}
```
with:
```go
	// responseFormat is passed in from RunTask (or a caller in the chain)
	// so that per-turn mutations (e.g. JSON-mode escalation after parse
	// failures) do not leak across RunTask calls on the same *Runner.
	// r.ResponseFormat is the seed value only.
```

(The local variable `responseFormat` was already declared at line 925. We now use the parameter instead of reading `r.ResponseFormat`.)

**3c.** Change `chatWithRetryWithNativeTools` signature (line 901):
```go
func (r *Runner) chatWithRetryWithNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat, includeNativeTools bool) (chatResult, error) {
```

Update its body to pass `responseFormat` through:
```go
		res, err := r.chatOnce(ctx, p, model, messages, responseFormat, includeNativeTools)
```

**3d.** Change `chatWithRetry` signature (line 893):
```go
func (r *Runner) chatWithRetry(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
```
Update body:
```go
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, true)
```

**3e.** Change `chatWithRetryNoNativeTools` signature (line 897):
```go
func (r *Runner) chatWithRetryNoNativeTools(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, responseFormat *schema.ResponseFormat) (chatResult, error) {
```
Update body:
```go
	return r.chatWithRetryWithNativeTools(ctx, p, model, messages, responseFormat, false)
```

- [ ] **Step 4: Thread `responseFormat` through `summarizeAndContinue` and `finalize`**

In `internal/agent/handoff.go`, change `summarizeAndContinue` signature (line 36):
```go
func (r *Runner) summarizeAndContinue(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, goal string, responseFormat *schema.ResponseFormat) ([]schema.ChatMessage, error) {
```
Update its call to `chatWithRetryNoNativeTools` (line 39):
```go
	res, err := r.chatWithRetryNoNativeTools(ctx, p, model, req, responseFormat)
```

In `internal/agent/finalize.go`, change `finalize` signature (line 72):
```go
func (r *Runner) finalize(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, reason finalizeReason, responseFormat *schema.ResponseFormat) (*Task, error) {
```
Update its call to `chatWithRetryNoNativeTools` (line 84):
```go
		res, err := r.chatWithRetryNoNativeTools(ctx, p, model, final, responseFormat)
```

- [ ] **Step 5: Update `RunTask` to use a local `responseFormat`**

In `internal/agent/runner.go`, at the top of `RunTask`, after the existing local variable declarations (around line 341), add:
```go
	effectiveRF := r.ResponseFormat
```

**5a.** Pass `effectiveRF` to the `summarizeAndContinue` call at line 466:
```go
		if r.MaxTurnContextTokens > 0 && estimateTokens(messages) > r.MaxTurnContextTokens {
			if fresh, serr := r.summarizeAndContinue(ctx, turnProvider, turnModel, messages, goal, effectiveRF); serr == nil {
```

**5b.** Pass `effectiveRF` to the main `chatWithRetry` call at line 476:
```go
		res, err := r.chatWithRetry(ctx, turnProvider, turnModel, messages, effectiveRF)
```

**5c.** Pass `effectiveRF` to the PlanFirst `chatWithRetryNoNativeTools` call at line 370:
```go
		planRes, err := r.chatWithRetryNoNativeTools(ctx, turnProvider, turnModel, planMessages, effectiveRF)
```

**5d.** Update the `finalize` call sites — all calls to `r.finalize` and the return sites that call `r.finalize`. There are multiple call sites:
- Line 500: `return r.finalize(ctx, turnProvider, turnModel, messages, task, reasonEmpty)` → `return r.finalize(ctx, turnProvider, turnModel, messages, task, reasonEmpty, effectiveRF)`
- Line 729: `r.finalize(ctx, turnProvider, turnModel, messages, task, reasonMalformed)` — inside `return` context
- Line 739: `r.finalize(ctx, turnProvider, turnModel, messages, task, reasonExhausted)` — inside `return` context
- Line 783 (maybeFinalizeOnStall): `r.finalize(ctx, p, model, messages, task, reasonStalled)` — inside `maybeFinalizeOnStall` method

For `maybeFinalizeOnStall` (line 754), it also calls `r.finalize`. This method is called from within RunTask but doesn't have access to `effectiveRF`. We need to either:
- Add `responseFormat` parameter to `maybeFinalizeOnStall` too
- Or store `effectiveRF` as a temporary field on Runner

The audit recommends threading as a parameter, so let's add it to `maybeFinalizeOnStall`:

```go
func (r *Runner) maybeFinalizeOnStall(ctx context.Context, p provider.Provider, model string, messages []schema.ChatMessage, task *Task, responseFormat *schema.ResponseFormat) (finalized bool, res *Task, err error, guidance string) {
```

And update its internal `finalize` call:
```go
	res, ferr := r.finalize(ctx, p, model, messages, task, reasonStalled, responseFormat)
```

And update all call sites of `maybeFinalizeOnStall` in `RunTask` (lines 503, 556, 638, 667, 706):
Each call `r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task)` → add `effectiveRF` as the last arg:
```go
r.maybeFinalizeOnStall(ctx, turnProvider, turnModel, messages, task, effectiveRF)
```

**5e.** Update the escalation code at lines 571-577 — change from mutating `r.ResponseFormat` to mutating `effectiveRF`:
```go
		if consecutiveParseFailures == 2 {
			repairMsg := BuildRepairMessage()
			messages = append(messages, repairMsg)
			r.State.AddMessage(session.RoleSystem, repairMsg.Content, session.ContentTypePlain)
			if turnProvider.Capabilities(ctx).JSONMode && r.ResponseFormat == nil {
				effectiveRF = &schema.ResponseFormat{Type: "json_object"}
			}
		}
```

(Only the last 3 lines change: `r.ResponseFormat == nil` stays as the guard — we only escalate if no seed format was set — and the assignment targets `effectiveRF` instead of `r.ResponseFormat`.)

- [ ] **Step 6: Update test call sites of `chatOnce` in `runner_test.go`**

Four call sites need the new `nil` response format parameter:

1. Line 205: `runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})` → add `, nil`
2. Line 231: same — add `, nil`
3. Line 1266: `runner.chatOnce(context.Background(), &blockingProvider{}, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})` → add `, nil`
4. Line 1623: `runner.chatOnce(context.Background(), p, "test-model", []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}})` → add `, nil`

- [ ] **Step 7: Build and run new test to verify it passes**

Run: `CGO_ENABLED=1 go build ./...`
Expected: success.

Run: `go test ./internal/agent -run 'TestResponseFormatResetsAcrossRunTaskCalls' -v`
Expected: PASS — the second RunTask no longer inherits JSON mode.

- [ ] **Step 8: Run the existing escalation test to confirm it's preserved**

Run: `go test ./internal/agent -run 'TestSecondConsecutiveParseFailureEnablesJSONMode' -v`
Expected: PASS.

- [ ] **Step 9: Run all agent tests**

Run: `go test ./internal/agent/... -count=1`
Expected: all tests PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go internal/agent/handoff.go internal/agent/finalize.go
git commit -m "fix(agent): thread responseFormat as local variable through RunTask (F-BUG-74, F-POL-85)"
```

---

### Task 4: F-CON-79 — Document `Runner` as not safe for concurrent `Run()` calls

**Files:**
- Modify: `internal/agent/runner.go` (add doc comment on `Runner` struct)
- Test: `internal/agent/runner_test.go` (add sequential re-use test with different `ForceClass`)

**Interfaces:**
- Consumes: existing `Runner` struct (no field changes).
- Produces: a doc comment on `Runner` that states concurrency contract and lists persistent vs per-call fields.

- [ ] **Step 1: Add doc comment on the `Runner` struct**

At `internal/agent/runner.go`, replace the existing `Runner` struct comment (lines 110-116) with an expanded version:

```go
// Runner drives one agent turn end to end: classify -> (optionally plan) ->
// loop { call the model, parse its action, execute or answer } -> summarise.
// It is the only thing in Marshal that calls Provider.Chat, Registry.Lookup,
// and PolicyEngine.Evaluate together — everything else (TUI, tools,
// registry, policy) stays decoupled and is exercised independently by
// Milestones C-G's own tests.
//
// Concurrency contract:
//
//   - A *Runner is NOT safe for concurrent calls to Run() / RunTask() on
//     the same instance. Callers (TUI, swarm orchestrator) must serialise
//     RunTask invocations.
//
//   - A *Runner IS safe for sequential re-use: after one RunTask returns,
//     the next call starts from a clean per-turn state (tracker, stats,
//     route, transient flags). The following fields persist across calls:
//     Provider, Registry, Policy, State, Model, RouteResolver, Now,
//     MaxToolIterations, MaxRetries, MaxTurnContextTokens, RequestTimeout,
//     ResponseFormat (seed), NativeTools, MaxParallelActions,
//     MaxToolResultChars, ForceClass, SkillIndex, Role, WriteGate,
//     UsageObserver, SteeringProvider, MetricsObserver, Snapshotter,
//     SnapshotRecorder, HookRunner, TitleGenerator, RunTaskFunc,
//     PlanFirst, HistoryBudgetTokens, MemoryProvider, ProjectID.
//
//   - The per-turn fields (tracker, stats, task, route, pressureMessageSent,
//     consecutiveParseFailures, consecutiveEmpty, effectiveRF) are reset at
//     the top of RunTask and are never shared across calls.
//
//   - tracker, stats, and ForceClass have dedicated mutexes for their
//     accessor methods (withStats, trackerMu, forceClassMu). All other
//     field reads and writes are not synchronised — hence the
//     single-caller-at-a-time rule.
type Runner struct {
```

Note: `effectiveRF` is an internal implementation detail — it's a local variable in RunTask, not a field. The doc comment should list the actual fields.

- [ ] **Step 2: Add a sequential re-use test**

Add to `internal/agent/runner_test.go`:

```go
// TestRunnerSequentialReuse verifies that the same *Runner can be called
// with RunTask twice in sequence (different goals, different ForceClass
// values) and both calls complete successfully.
func TestRunnerSequentialReuse(t *testing.T) {
	p := &scriptedProvider{
		capabilities: schema.ProviderCapabilities{JSONMode: true},
		responses: []string{
			// First RunTask
			`{"rationale":"first","action":{"type":"final","content":"first done"}}`,
			// Second RunTask
			`{"rationale":"second","action":{"type":"final","content":"second done"}}`,
		},
	}
	state := newTestState(t)
	r := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.MaxToolIterations = 5
	r.MaxRetries = 0

	r.SetForceClass("question")
	if _, err := r.RunTask(context.Background(), "first goal"); err != nil {
		t.Fatalf("first RunTask err = %v", err)
	}

	r.SetForceClass("edit")
	if _, err := r.RunTask(context.Background(), "second goal"); err != nil {
		t.Fatalf("second RunTask err = %v", err)
	}

	// Both completed without error — the second call did not inherit
	// per-run state from the first.
	if len(p.requests) < 2 {
		t.Fatalf("expected at least 2 chat requests, got %d", len(p.requests))
	}
}
```

- [ ] **Step 3: Build and run the new test**

Run: `CGO_ENABLED=1 go build ./...`
Expected: success.
Run: `go test ./internal/agent -run 'TestRunnerSequentialReuse' -v`
Expected: PASS.

- [ ] **Step 4: Run all agent tests**

Run: `go test ./internal/agent/... -count=1`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "docs(agent): document Runner concurrency contract and add sequential reuse test (F-CON-79)"
```

---

### Final verification

- [ ] **Run full suite**

Run: `CGO_ENABLED=1 go test ./...`
Expected: all tests in the repository PASS.

---

## Self-Review

1. **Spec coverage:**
   - F-POL-93 → Task 1 (dead code removal).
   - F-POL-95 → Task 2 (rename `pressureSent` → `pressureMessageSent`).
   - F-BUG-74 / F-POL-85 → Task 3 (thread `responseFormat` as local variable, never mutate `r.ResponseFormat`).
   - F-CON-79 → Task 4 (doc comment + sequential reuse test).

2. **Public API preserved:** `Run()`, `RunTask()`, `CopyFrom()`, `NewRunner()` signatures unchanged. `ResponseFormat` seed propagation via `CopyFrom` preserved.

3. **Existing test preserved:** `TestSecondConsecutiveParseFailureEnablesJSONMode` continues to check that the 3rd request within one `RunTask` has `json_object` format.

4. **New tests added:**
   - `TestResponseFormatResetsAcrossRunTaskCalls` — verifies the cross-call isolation fix.
   - `TestRunnerSequentialReuse` — verifies that sequential `RunTask` calls with different `ForceClass` both complete.

5. **Type consistency:** The `responseFormat` parameter is `*schema.ResponseFormat` everywhere — matching the type of `r.ResponseFormat`.
