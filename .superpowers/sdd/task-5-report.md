# Task 5 Report: Summarize-and-continue instead of destructive compaction

## What was implemented

- Created `internal/agent/handoff.go` with:
  - `handoffSummaryDirective` — a structured prompt asking the model for a self-contained mid-task handoff summary (current state, files & changes, technical context, exact next steps).
  - `errEmptyHandoffSummary` — returned when the summary call produces only whitespace so the caller can fall back.
  - `(r *Runner) summarizeAndContinue(...)` — requests the summary via `chatWithRetryNoNativeTools`, then rebuilds the working message list as: system prompt + context pack + original goal + summary + "continue" instruction. Records a system message on `r.State` noting the compaction.

- Created `internal/agent/handoff_test.go` with the two unit tests from the brief:
  - `TestSummarizeAndContinueRebuildsMessages`
  - `TestSummarizeAndContinueErrorsOnEmptySummary`

- Modified `internal/agent/runner.go`:
  - Raised `DefaultMaxTurnContextTokens` from `16384` to `60000`.
  - Replaced the unconditional loop-top `messages = compactMessages(...)` call with the new `summarizeAndContinue` / `compactMessages` fallback logic. `compactMessages` is preserved as the fallback when summarization fails or returns empty text.

- Added `TestLoopCompactsViaSummaryWhenOverBudget` to `internal/agent/runner_test.go`.

## TDD / test evidence

Step 1 — compile failure with missing method:

```
$ go test ./internal/agent/ -run TestSummarizeAndContinue -v
# marshal/internal/agent [marshal/internal/agent.test]
internal/agent/handoff_test.go:26:23: runner.summarizeAndContinue undefined (type *Runner has no field or method summarizeAndContinue)
FAIL
```

Step 2 — handoff unit tests pass after implementation:

```
$ go test ./internal/agent/ -run TestSummarizeAndContinue -v
=== RUN   TestSummarizeAndContinueRebuildsMessages
--- PASS: TestSummarizeAndContinueRebuildsMessages (0.00s)
=== RUN   TestSummarizeAndContinueErrorsOnEmptySummary
--- PASS: TestSummarizeAndContinueErrorsOnEmptySummary (0.00s)
PASS
ok  	marshal/internal/agent	0.569s
```

Step 3 — full package tests pass:

```
$ go test ./internal/agent/...
ok  	marshal/internal/agent	0.687s
ok  	marshal/internal/agent/swarm	(cached)
```

Step 4 — formatting and vetting:

```
$ gofmt -w . && go vet ./internal/agent/...
# no output (success)
```

## Files changed

- `internal/agent/handoff.go` (created)
- `internal/agent/handoff_test.go` (created)
- `internal/agent/runner.go` (modified)
- `internal/agent/runner_test.go` (modified)

## Self-review findings

- The handoff implementation follows the brief exactly and uses existing helpers (`BuildSystemPrompt`, `appendContextPackMessage`, `chatWithRetryNoNativeTools`).
- The loop-top wiring runs immediately before the next model call, where the last message is always a completed result or prose message, so the rebuilt list cannot orphan a `tool_call_id`.
- `pressureSent` is reset after a successful handoff so the fresh transcript can approach the budget again without premature finalize pressure.
- `compactMessages` remains available as the fallback path.
- All existing agent tests pass with the new default budget; no test required adjustment for the 16k→60k change.

## Issues / concerns

- The brief's integration test set `runner.MaxTurnContextTokens = 1500` and used `strings.Repeat("word ", 2000)` to force a budget trip. In the current codebase, tool output larger than `DefaultMaxToolResultChars` (8000 chars) is spilled to disk and only a 2000-char preview is kept inline. With the current system prompt, the post-tool-loop token count is ~1475, so the 1500 threshold never trips and `summarizeAndContinue` is never invoked. I adjusted the test threshold to `1000` so the budget actually triggers and the test verifies the handoff path. This is a test calibration, not a production behavior change.

## Fix applied during review

### Finding

`internal/agent/runner_test.go:248` — `runner.MaxTurnContextTokens` was changed from the brief's specified `1500` to `1000`. This deviated from the task spec.

### Root cause

The original test used `strings.Repeat("word ", 2000)` (~10000 chars). Task 4's `spillToolResult` spills tool output above `DefaultMaxToolResultChars` (8000 chars) to disk, leaving only a ~2000-char preview inline. With the inline preview plus the rest of the transcript, the estimated token count fell just under the brief's 1500-token threshold, so the test never tripped the budget.

### Fix

- Reduced the tool output to `strings.Repeat("word ", 1400)` (7000 chars). This stays under the 8000-character spill limit, so the full output remains inline in the transcript.
- Restored `runner.MaxTurnContextTokens = 1500` to match the brief.
- With 7000 inline characters, the tool result alone contributes ~1750 estimated tokens, so the 1500-token budget is exceeded after a single tool call and `summarizeAndContinue` is invoked as intended.

### Files changed

- `internal/agent/runner_test.go`

### Tests run

```
$ go test ./internal/agent/ -run TestLoopCompactsViaSummaryWhenOverBudget -v
=== RUN   TestLoopCompactsViaSummaryWhenOverBudget
--- PASS: TestLoopCompactsViaSummaryWhenOverBudget (0.00s)
PASS
ok  	marshal/internal/agent	0.545s
```

```
$ go test ./internal/agent/...
ok  	marshal/internal/agent	0.661s
ok  	marshal/internal/agent/swarm	(cached)
```

```
$ gofmt -w . && go vet ./internal/agent/...
# no output (success)
```
