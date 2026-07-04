# Task 4 Report: Route reasoning deltas through the agent runner

## Status
DONE

## What Was Implemented

`internal/agent/runner.go`'s `chatOnce` now routes `schema.ChatEventDelta` events by kind:

- `event.Kind == schema.DeltaThinking` -> `r.State.AppendThinking(event.Delta)`
- otherwise (the zero-value `schema.DeltaAnswer`) -> appended to the local `strings.Builder` as before

The whole event-consumption loop is now wrapped with `r.State.BeginStreaming()` before the loop and `defer r.State.EndStreaming()` immediately after, so the in-progress message lifecycle (Task 3) always opens and closes for every `chatOnce` call, including when the provider errors mid-stream (`schema.ChatEventError` case returns early, but the deferred `EndStreaming()` still runs).

`chatOnce`'s signature and its answer-text return value are unchanged. `chatWithRetry` and `Run` (planning call, ReAct loop iterations, `AddMessage` calls) were not touched.

## Files Changed

- `internal/agent/runner.go` — `chatOnce` (added `BeginStreaming`/`defer EndStreaming`, and kind-based routing of delta events)
- `internal/agent/runner_test.go`:
  - `scriptedProvider` gained a `thinking []string` field; `Chat` now emits a `schema.ChatEvent{Type: schema.ChatEventDelta, Kind: schema.DeltaThinking, Delta: p.thinking[idx]}` event before the content delta when `idx < len(p.thinking) && p.thinking[idx] != ""`; channel buffer grew from 2 to 3 to hold the extra event without blocking.
  - Added `TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText`
  - Added `TestChatOnceEndsStreamingEvenOnProviderError`

Both additions match the brief's code exactly (no deviations).

## TDD Evidence

### RED

Command:
```
go test ./internal/agent/... -run 'TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText|TestChatOnceEndsStreamingEvenOnProviderError' -v
```

Output (failing, before Step 3's `chatOnce` change):
```
=== RUN   TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText
    runner_test.go:135: chatOnce returned "considering the question{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}", want "{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}"
--- FAIL: TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText (0.00s)
=== RUN   TestChatOnceEndsStreamingEvenOnProviderError
    runner_test.go:160: InProgress().Active = true after error, want false (EndStreaming must still run)
--- FAIL: TestChatOnceEndsStreamingEvenOnProviderError (0.00s)
FAIL
FAIL	marshal/internal/agent	0.553s
```

This is the expected failure mode: with the old `chatOnce`, all delta events (thinking and answer alike) were concatenated into the single `strings.Builder`, so the thinking text leaked into the returned answer text; and since `chatOnce` never called `BeginStreaming`/`EndStreaming`, the test's manually-set `Active: true` state (from calling `state.BeginStreaming()` before invoking `chatOnce`) was never reset to `false` after the provider error.

### GREEN

Command:
```
go test ./internal/agent/... -race -v
```

Output (tail):
```
=== RUN   TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText
--- PASS: TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText (0.00s)
=== RUN   TestChatOnceEndsStreamingEvenOnProviderError
--- PASS: TestChatOnceEndsStreamingEvenOnProviderError (0.00s)
=== RUN   TestRunAnswersQuestionWithoutToolCalls
--- PASS: TestRunAnswersQuestionWithoutToolCalls (0.00s)
... (all other pre-existing TestRun*, TestClassify, TestParseAction*, TestBuild*, TestPreviewPatchDiff*, TestNewTaskDefaultsToPendingStatus, TestSplitPlanLinesTrimsAndDropsBlankLines pass unchanged)
PASS
ok  	marshal/internal/agent	1.504s
```

Full package run (32 top-level test functions) passed under `-race` with no stray warnings.

Additional verification: `go build ./...` succeeds, `go vet ./internal/agent/...` is clean.

## Self-Review Findings

- Implemented exactly the code given in the brief for all 3 steps (test double extension, new tests, `chatOnce` rewrite) — no deviations.
- Every pre-existing `TestRun*` test (retries, planning, tool-calls, approval, context-pack handling, route resolution/fallback) passed unmodified in behavior; only the `scriptedProvider` struct/`Chat` method were extended (additively — `thinking` defaults to nil for all pre-existing call sites, so their event sequences are unchanged).
- `-race` run is clean, no data races detected, no stray output.
- Touched only `internal/agent/runner.go` and `internal/agent/runner_test.go`, both inside the required worktree. Confirmed via `git status --porcelain` that only these two files were staged/committed by this task; two unrelated pre-existing working-tree modifications (`.superpowers/sdd/task-2-report.md`, `.superpowers/sdd/task-3-report.md`, left over from earlier task agents in this branch's history, unrelated to this task's numbering) were left untouched and unstaged.

## Issues or Concerns

None. The routing falls out naturally from Task 3's lifecycle exactly as the task description predicted: intermediate ReAct-loop `chatOnce` calls that never reach `AddMessage` simply have their live-streamed reasoning overwritten by the next `BeginStreaming()` call, with no special-casing required in `runner.go`.

## Post-Review Fix: TestChatOnceEndsStreamingEvenOnProviderError didn't test what it claimed

### What was wrong

Task review found that `TestChatOnceEndsStreamingEvenOnProviderError` pre-seeded state via `state.BeginStreaming()` + `state.AppendThinking("partial thought")` *before* calling `chatOnce`. But `chatOnce` itself calls `r.State.BeginStreaming()` as its first action, which resets `InProgress()` to a fresh `InProgressMessage{Active: true}` — wiping the test's pre-seeded thinking text before the provider's error event was ever read. The test's `Active == false` assertion still passed (because `EndStreaming()` does run via the deferred call), but the test never actually proved that reasoning captured *during* a call survives that call ending in error — the property its name and setup implied. Separately, `scriptedProvider.Chat` checked `p.errs[idx]` first and returned immediately, so no test double could express "emit a thinking delta, then error" within a single call — the pre-seed workaround existed only because the double couldn't do the real thing.

### What was changed

1. **`internal/agent/runner_test.go` — `scriptedProvider.Chat`**: reordered so the thinking-delta check runs first (unconditionally, if `idx < len(p.thinking) && p.thinking[idx] != ""`), then the `errs[idx]` check (return early on error, as before), then the content+done emission. This lets a single call emit a thinking delta followed by an error. All pre-existing literals like `&scriptedProvider{errs: []error{...}}` have `thinking` at its zero value (`nil`), so `idx < len(p.thinking)` stays false and this new branch never fires for them — verified no existing test's behavior changed.

2. **`internal/agent/runner_test.go` — `TestChatOnceEndsStreamingEvenOnProviderError`**: rewritten to construct `&scriptedProvider{thinking: []string{"partial thought"}, errs: []error{errors.New("boom")}}` and removed the pre-seed calls (`state.BeginStreaming()` / `state.AppendThinking(...)`) entirely — the scenario is now expressed through the provider double so it flows through `chatOnce`'s real internal lifecycle instead of being clobbered by it. After `chatOnce` returns, the test asserts:
   - `err` is non-nil (the provider error propagated)
   - `state.InProgress().Reasoning == "partial thought"` (the thinking delta arriving before the error was captured by `AppendThinking` and preserved — `EndStreaming` does not clear `Reasoning`, only `AddMessage` does, per Task 3's design)
   - `state.InProgress().Active == false` (the deferred `EndStreaming()` still ran despite the early return on error)

No changes were made to `internal/agent/runner.go` — the reviewer had already approved the production code; this was a test-only fix.

### Self-review: does the rewritten test actually catch a regression?

Yes. If `chatOnce`'s `defer r.State.EndStreaming()` were changed to also clear `Reasoning` (a regression against Task 3's "only `AddMessage` clears reasoning" design), the second assertion (`Reasoning == "partial thought"`) would fail immediately, since `Reasoning` would be empty after `EndStreaming()` ran. The test now genuinely exercises the fixed-call path: thinking delta is appended via `AppendThinking` inside the real `chatOnce` loop, then the error event arrives and is returned, then the deferred `EndStreaming()` runs — and the assertions confirm reasoning text survives that sequence while `Active` still flips to `false`.

### Test run confirming the fix

Command: `go test ./internal/agent/... -race -v`

Result: all 32 top-level test functions passed, including:
```
=== RUN   TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText
--- PASS: TestChatOnceRoutesThinkingDeltasToStateAndReturnsAnswerText (0.00s)
=== RUN   TestChatOnceEndsStreamingEvenOnProviderError
--- PASS: TestChatOnceEndsStreamingEvenOnProviderError (0.00s)
...
PASS
ok  	marshal/internal/agent	1.567s
```

Every pre-existing `TestRun*` test (retries, planning, tool calls, approval, context-pack handling, route resolution/fallback) passed unmodified, confirming the `scriptedProvider.Chat` reordering did not change behavior for any existing test double usage.

### Files changed

- `internal/agent/runner_test.go` (test-only; `internal/agent/runner.go` untouched)
