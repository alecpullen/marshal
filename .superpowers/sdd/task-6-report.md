# Task 6 Report: Native-mode Eval Scenarios and Telemetry

## What Was Implemented

Task 6 adds end-to-end eval coverage for native tool-calling mode and completes the final validation for the native-tool-calling feature branch.

### Changes

1. **`internal/agent/eval_scenarios_test.go`**
   - Added `evalNativeRead` helper to build `schema.ToolCall` values.
   - Added `TestEvalNativeScenarios` with two native-mode rows:
     - `native research turn answers after reads`: two scripted `file.read` native tool calls followed by a final text answer. Asserts `Outcome == "answered"`, `Iterations == 2`, `ToolCalls == 2`, `ParseFailures == 0`.
     - `native tool error recovers to an answer`: one call to an unknown tool (`missing.tool`) followed by a final answer. Asserts `Outcome == "answered"`, `ToolErrors == 1`, `ToolCalls == 1`, `ParseFailures == 0`.
   - Fixed the iteration expectation for the research row from `3` to `2`. In native mode the runner increments `Iterations` only on tool-call turns; the final answer turn returns directly without incrementing the counter, so two reads + one answer equals two iterations.

2. **`internal/agent/metrics.go`**
   - Added a comment on `ParseFailures` documenting that it counts unparseable JSON-envelope actions and is always `0` in native tool-calling mode because the provider returns parsed `tool_calls` directly.

3. **`docs/plans/task.md`**
   - Marked Task 6 as complete (`[x]`).

4. **`.superpowers/sdd/progress.md`**
   - Updated Task 6 status from "in progress" to "complete".

## Test Commands Run and Their Output

```
$ CGO_ENABLED=1 go test -count=1 -run TestEvalNativeScenarios -v ./internal/agent/
=== RUN   TestEvalNativeScenarios
=== RUN   TestEvalNativeScenarios/native_research_turn_answers_after_reads
=== RUN   TestEvalNativeScenarios/native_tool_error_recovers_to_an_answer
--- PASS: TestEvalNativeScenarios (0.00s)
    --- PASS: TestEvalNativeScenarios/native_research_turn_answers_after_reads (0.00s)
    --- PASS: TestEvalNativeScenarios/native_tool_error_recovers_to_an_answer (0.00s)
PASS
ok  	marshal/internal/agent	0.551s
```

```
$ CGO_ENABLED=1 go test -count=1 ./...
?   	marshal/cmd/marshal	[no test files]
ok  	marshal/internal/agent	0.698s
ok  	marshal/internal/agent/swarm	0.266s
ok  	marshal/internal/app	0.743s
ok  	marshal/internal/app/config	0.602s
?   	marshal/internal/app/logging	[no test files]
ok  	marshal/internal/app/session	1.437s
ok  	marshal/internal/app/tui	1.538s
ok  	marshal/internal/app/tui/memory	0.954s
ok  	marshal/internal/app/tui/settings	1.581s
ok  	marshal/internal/commands	0.771s
ok  	marshal/internal/contextpack	1.267s
ok  	marshal/internal/db	1.702s
ok  	marshal/internal/knowledge	1.399s
ok  	marshal/internal/llm/provider	1.507s
ok  	marshal/internal/llm/routing	1.508s
?   	marshal/internal/llm/schema	[no test files]
ok  	marshal/internal/llm/streaming	1.471s
ok  	marshal/internal/repo	1.614s
ok  	marshal/internal/skills	1.457s
ok  	marshal/internal/tools/mcp	1.438s
ok  	marshal/internal/tools/native	1.542s
ok  	marshal/internal/tools/patch	1.591s
ok  	marshal/internal/tools/policy	1.493s
ok  	marshal/internal/tools/registry	1.522s
```

```
$ gofmt -l .
(no output)
```

```
$ go vet ./...
internal/app/app.go:552:12: assignment copies lock value to *runner: marshal/internal/agent.Runner contains sync.Mutex
```

Only the documented pre-existing `internal/app/app.go` mutex-copy warning remains.

## Files Changed

- `internal/agent/eval_scenarios_test.go`
- `internal/agent/metrics.go`
- `docs/plans/task.md`
- `.superpowers/sdd/progress.md`
- `.superpowers/sdd/task-6-report.md` (this report)

## Self-Review Findings

- The native research-row iteration expectation was corrected to match the actual runner behavior (tool-call turns only are counted), preventing a false failure.
- `ParseFailures == 0` is asserted for both native rows and is consistent with the new comment in `metrics.go`.
- The existing JSON-envelope eval rows are untouched, preserving back-compat coverage.
- The full test suite passes, `gofmt` is clean, and `go vet` shows only the known warning.
- The commit scope is limited to Task 6 verification files.

## Issues or Concerns

- The `.kilo/` directory is untracked in the worktree. It was not added to the commit.
- `TestRunAllowsParallelReadBatchWithoutStalling` in `internal/agent/runner_test.go` flaked once during validation (failed with "executed 4 reads, want 5"); it passed on the next run and is unrelated to Task 6 changes.
- No live provider test was run (out of scope for this automated validation step).
