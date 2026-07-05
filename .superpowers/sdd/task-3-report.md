## Task 3 Report: Wire ActiveToolCall into the runner

### Changes Made

**`internal/app/session/session.go`** — Added `AddMessageFinal` method (after `AddMessage`):
- Mirrors `AddMessage` but sets `Final: true` on the message
- Captures reasoning and think duration (same as AddMessage)
- Calls existing `SaveMessage` signature (without `final` param; Task 5 will update this)

**`internal/agent/runner.go`** — Two changes:
- *Line 209*: Changed `AddMessage` → `AddMessageFinal` for terminal answers (`ActionAnswer` / `ActionFinal`)
- *Lines 455-458*: Added `SetActiveToolCall` (with `SummarizeToolArgs`) before tool execution, and `defer ClearActiveToolCall()` after it

**`internal/agent/runner_test.go`** — Added two tests:
- `TestRunnerSetsAndClearsActiveToolCall`: Verifies `ActiveToolCall` is set when a tool handler runs and cleared after Run completes
- `TestRunnerMarksFinalAnswer`: Verifies final answer messages have `Final: true`

### Test Results

```
=== RUN   TestRunnerSetsAndClearsActiveToolCall
--- PASS: TestRunnerSetsAndClearsActiveToolCall (0.00s)
=== RUN   TestRunnerMarksFinalAnswer
--- PASS: TestRunnerMarksFinalAnswer (0.00s)
```

Full suite: `go test ./...` — all packages pass, no regressions.
