# Domain D2: Native Tool Execution Correctness Fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve six open native-tool correctness findings (F-BUG-70, F-BUG-75, F-BUG-76, F-BUG-77, F-CON-80, F-POL-89) from `docs/14-codebase-improvement-audit-2026-07-14.md` (Domain D).

**Architecture:** Each task fixes one finding in isolation. Low-risk edits land first (constant swap, label change), then the logic changes (iteration budget, serial batch, balanced JSON, goroutine leak). Tests are added to existing `_test.go` files and run with `go test ./internal/agent/...` after each task.

**Tech Stack:** Go (stdlib only — `encoding/json`, `strings`, `time`).

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the implementation step of each task.
- Every test change MUST pass: run `go test ./internal/agent -run <TestName>` for the new test, then `go test ./internal/agent/...` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public function signatures unless the task explicitly says to change them.
- All work happens in the worktree at `.worktrees/domain-d-agent-runtime` (branch `feature/domain-d-agent-runtime`).

---

## File Structure

Files modified by this plan:

- `internal/agent/runner.go` — F-BUG-70, F-BUG-75, F-BUG-76, F-POL-89, F-CON-80.
- `internal/agent/runner_test.go` — Tests for all above.
- `internal/agent/protocol.go` — F-BUG-77 (balanced JSON extractor).
- `internal/agent/protocol_test.go` — F-BUG-77 tests.

---

### Task 1: F-BUG-75 — Replace literal `"Unanswered"` with `session.AnswerUnanswered`

**Files:**
- Modify: `internal/agent/runner.go:1481`
- Test: `internal/agent/runner_test.go`

**Rationale:** Line 1481 compares `a.Answer != "Unanswered"` as a raw string; envelope path at line 720 uses `session.AnswerUnanswered`. If the sentinel ever changes, the native path silently mis-classifies all answers.

- [ ] **Step 1: Write the failing test** — `TestNativeQuestionAskDeclined` that issues a `question.ask` action via native tools, the user declines (empty answer), and verify the runner sees `a.Answer == session.AnswerUnanswered`.
- [ ] **Step 2: Fix the code** — Change line 1481 from `a.Answer != "Unanswered"` to `a.Answer != session.AnswerUnanswered`.
- [ ] **Step 3: Build & test** — `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run TestNativeQuestionAskDeclined -v`
- [ ] **Step 4: Commit** — `git commit -m "F-BUG-75: replace hard-coded Unanswered literal with session.AnswerUnanswered constant"`

---

### Task 2: F-POL-89 — Include question preview in activity label

**Files:**
- Modify: `internal/agent/runner.go:1654`
- Test: `internal/agent/runner_test.go`

**Rationale:** `r.State.SetActivity(... Label: "waiting for your answer")` shows the user an activity but not *which* question. Include the first 40 chars of the first question (or "Q1/N: …").

- [ ] **Step 1: Fix the code** — Change the label at line 1654 to include a truncated preview. Format: `"waiting for your answer: Q1/N: <first 40 chars>"` when multiple questions, or `"waiting for your answer: <first 40 chars>"` for a single question.
- [ ] **Step 2: Build** — `CGO_ENABLED=1 go build ./...`
- [ ] **Step 3: Add test** — `TestRequestQuestionsActivityLabel` that calls `requestQuestions` directly with a known question and checks the label on the state.
- [ ] **Step 4: Run tests** — `go test ./internal/agent -run TestRequestQuestionsActivityLabel -v`
- [ ] **Step 5: Commit** — `git commit -m "F-POL-89: include first question preview in requestQuestions activity label"`

---

### Task 3: F-BUG-70 — Native ask_user / question.ask consume iteration budget

**Files:**
- Modify: `internal/agent/runner.go` (`executeNativeAskUser`, `executeNativeQuestionAsk`)
- Test: `internal/agent/runner_test.go`

**Rationale:** In the envelope (non-native) path, `ActionAskUser` and `ActionQuestionAsk` both call `iteration++` (lines 683-684, 716-717). The native equivalents (`executeNativeAskUser` at line 1437, `executeNativeQuestionAsk` at line 1462) never call `iteration++`. The outer batch loop does increment `iteration` at line 564, but a model that outputs multiple tool calls in one batch can interleave ask_user with other tools without each ask counting against the budget. When the answer is empty (declined), the envelope path also calls `recordIdle` (line 688) but the native path does not.

**Fix approach:** Have `executeNativeAskUser` and `executeNativeQuestionAsk` increment `iteration` via `r.withStats` and call `recordIdle` when the answer is empty. However, these methods do not have access to the local `iteration` variable. Instead, store a per-Runner field that accumulates native ask iterations and check it in the main loop alongside the local iteration counter.

**Simpler approach:** Increment `r.stats.m.Iterations` directly via `r.withStats` in the native ask methods. In the main loop, use `r.stats.m.Iterations` (not the local `iteration`) for the budget check at line 452, or add the native counter to the local one. Since the main loop already increments `iteration` once per batch, we add an additional per-call increment in the native methods.

**Actually, the simplest correct approach:** Since `iteration` is a local in the main loop but the native ask methods are called from within that loop, we can pass a callback or store a pointer. But to avoid API changes, just add the per-ask increments directly into `r.stats.m.Iterations` and modify the budget check at line 452 to use `max(iteration, r.stats.m.Iterations)` — or simply also increment `r.stats.m.Iterations` from the main loop.

**Simplest:**
- In `executeNativeAskUser` after the answer: add `r.withStats(func(s *turnStats) { s.m.Iterations++ })` and if declined, also `r.trackerMu.Lock(); r.tracker.recordIdle("ask_user declined"); r.trackerMu.Unlock()`.
- In `executeNativeQuestionAsk` after answers: add `r.withStats(func(s *turnStats) { s.m.Iterations++ })` and if allUnanswered, also `r.trackerMu.Lock(); r.tracker.recordIdle("question.ask declined"); r.trackerMu.Unlock()`.
- Also bump the local `iteration` variable. Since we need access to it, use a Runner field as a side channel.
- **Revised approach:** Store a `nativeIterBudgetRemaining *int` on the Runner that points to the local `iteration` from RunTask. Initialize at start of RunTask, nil after. In native ask methods, dereference if non-nil. This avoids changing the method signatures.

**Final approach (simplest that works with no API surface change):**
- Add `iterationBudget *int` field on Runner (unexported, set at start of RunTask).
- In `executeNativeAskUser`/`executeNativeQuestionAsk`, dereference `r.iterationBudget` if non-nil and increment.
- Budget check in main loop reads both `iteration` and whatever native methods have added.

- [ ] **Step 1: Fix the code** — Add `iterationBudget *int` field on Runner. Set it at the top of RunTask (`r.iterationBudget = &iteration`). In `executeNativeAskUser`, increment `*r.iterationBudget` and call `r.withStats(...)`. On empty answer, call `recordIdle`. In `executeNativeQuestionAsk`, same pattern. Clear `r.iterationBudget = nil` in a defer.
- [ ] **Step 2: Add test** — `TestNativeAskUserCountsAgainstIterationBudget` sets `MaxToolIterations=2`, scripts a native model that asks 4 times, verifies the runner returns with `ErrMaxIterationsExceeded` (not hangs).
- [ ] **Step 3: Build & test** — `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run TestNativeAskUserCountsAgainstIterationBudget -v`
- [ ] **Step 4: Run full agent tests** — `go test ./internal/agent/...`
- [ ] **Step 5: Commit** — `git commit -m "F-BUG-70: make native ask_user/question.ask consume iteration budget like envelope path"`

---

### Task 4: F-BUG-76 — Serial-tool batch does not short-circuit remaining serial tools

**Files:**
- Modify: `internal/agent/runner.go` (`executeActions`)
- Test: `internal/agent/runner_test.go`

**Rationale:** At lines 1539-1543, the serial-tool phase (ask_user, question.ask) returns immediately on the first error: `if err != nil { return nil, err }`. Remaining serial tools never execute, leaving `results[j] = nil`. The model that issued 3 questions sees a single error and no answers for the other two.

**Fix:** Replace the early return with a pattern that records the error for the failing slot and continues executing the remaining serial tools. Each tool slot must have exactly one `[]schema.ChatMessage` entry (even if it's an error message) so the flat-mapped results in Phase 3 have the correct count.

- [ ] **Step 1: Write the failing test** — `TestSerialBatchContinuesAfterError` that exercises `executeActions` with 3 serial tools where the 2nd errors, verifies 3 results are produced (2 success + 1 error message).
- [ ] **Step 2: Fix the code** — Change the Phase 1 serial-tool loop at lines 1539-1543 from `if err != nil { return nil, err }` to recording the error and continuing. Build an error tool message for the failed slot.
- [ ] **Step 3: Build & test** — `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run TestSerialBatchContinuesAfterError -v`
- [ ] **Step 4: Commit** — `git commit -m "F-BUG-76: execute remaining serial tools even after one errors"`

---

### Task 5: F-BUG-77 — Balanced JSON extraction

**Files:**
- Modify: `internal/agent/protocol.go:116-129` (`extractJSONObject`)
- Test: `internal/agent/protocol_test.go`

**Rationale:** `extractJSONObject` uses `strings.Index`/`LastIndex` of `{`/`}` which fails on responses like `{"a": 1, "b": {"nested": true}} trailing {noise}`.

**Fix:** Rewrite with a stack-based scanner that tracks `{`/`}` depth, respects string boundaries with `\` escape handling, and returns the first complete balanced JSON object.

- [ ] **Step 1: Write tests** — `TestExtractJSONObjectBalanced` with at least 6 cases:
  1. Simple flat object
  2. Nested object
  3. Array values
  4. String containing braces
  5. Multiple top-level objects
  6. Malformed (no balanced object)
- [ ] **Step 2: Fix the code** — Rewrite `extractJSONObject` with stack-based balanced-brace detection respecting string boundaries and escapes.
- [ ] **Step 3: Build & test** — `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run TestExtractJSONObjectBalanced -v`
- [ ] **Step 4: Run all protocol tests** — `go test ./internal/agent -run TestParseAction -v`
- [ ] **Step 5: Commit** — `git commit -m "F-BUG-77: rewrite extractJSONObject with stack-based balanced-brace scanner"`

---

### Task 6: F-CON-80 — Goroutine leak risk on requestQuestions/requestApproval

**Files:**
- Modify: `internal/agent/runner.go` (`requestApproval`, `requestQuestions`)
- Test: `internal/agent/runner_test.go`

**Rationale:** Both functions block on `<-tc.ResponseChan` or `<-ctx.Done()`. If the TUI exits without sending a decision, the agent goroutine blocks forever. With `r.RequestTimeout = 0`, a TUI that closes without sending would leak a goroutine.

**Fix:** Wrap the `select` with a `time.After` arm. If `r.RequestTimeout == 0`, use a default (say 5 minutes). On timeout, close/nil the pending slot and return a sentinel error `ErrRequestTimedOut`.

- [ ] **Step 1: Define sentinel error** — `var ErrRequestTimedOut = errors.New("agent: request timed out")` in runner.go.
- [ ] **Step 2: Fix `requestApproval`** — Add a `time.After` arm to the select at lines 1620-1629.
- [ ] **Step 3: Fix `requestQuestions`** — Add a `time.After` arm to the select at lines 1656-1665.
- [ ] **Step 4: Write tests** — `TestRequestApprovalTimeout` and `TestRequestQuestionsTimeout` with a fake channel that never sends, a short `RequestTimeout`, verify the functions return `ErrRequestTimedOut`.
- [ ] **Step 5: Build & test** — `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run TestRequest.*Timeout -v`
- [ ] **Step 6: Run full test suite** — `go test ./...`
- [ ] **Step 7: Commit** — `git commit -m "F-CON-80: add timeout guard on requestQuestions/requestApproval bridge channels"`

---

### Verification

- [ ] Run `CGO_ENABLED=1 go build ./...`
- [ ] Run `go test ./internal/agent/...`
- [ ] Run `go test ./...`
