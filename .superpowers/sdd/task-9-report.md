# Task 9 Report

## Summary

Wired durable project memories into `agent.Runner` by adding a runner-local `MemoryProvider` interface that returns `contextpack.MemoryNote`, threading `MemoryProvider` and `ProjectID` onto `Runner`, and merging memories into the session context pack at the start of each turn after route resolution and before the first model message is built.

## TDD Evidence

### RED

Added failing tests in `internal/agent/runner_test.go`:

- `TestRunMergesMemoriesIntoContextPackBeforeFirstMessage`
- `TestRunWithoutMemoryProviderLeavesContextPackEmpty`

Ran:

```bash
go test ./internal/agent/... -run "TestRunMergesMemories|TestRunWithoutMemoryProvider" -v
```

Observed expected failure:

```text
internal/agent/runner_test.go:374:9: runner.MemoryProvider undefined (type *Runner has no field or method MemoryProvider)
internal/agent/runner_test.go:377:9: runner.ProjectID undefined (type *Runner has no field or method ProjectID)
FAIL    marshal/internal/agent [build failed]
```

### GREEN

Implemented:

- `agent.MemoryProvider` interface in `internal/agent/runner.go`
- `Runner.MemoryProvider MemoryProvider`
- `Runner.ProjectID int64`
- `Runner.mergeMemories()`
- call to `r.mergeMemories()` in `Run()` immediately after route resolution

Ran:

```bash
go test ./internal/agent/... -v
```

Result: `marshal/internal/agent` passed, including the two new tests.

## Verification

Ran:

```bash
gofmt -w internal/agent/runner.go internal/agent/runner_test.go
go build ./cmd/marshal
go test ./...
```

Results:

- `gofmt` completed successfully
- `go build ./cmd/marshal` passed
- `go test ./...` passed across the repository

## Changed Files

- `internal/agent/runner.go`
- `internal/agent/runner_test.go`
- `.superpowers/sdd/task-9-report.md`

## Self-Review

- Kept `internal/agent` independent of `internal/knowledge` by using `contextpack.MemoryNote` only.
- Followed the brief’s merge point so route budgeting happens before memory injection.
- Preserved non-blocking behavior: missing memory provider, provider errors, and empty memory lists do not fail a turn.
- Used existing context-pack token budgeting, defaulting to `contextpack.DefaultMaxTokens` when the current pack has no max token budget.

## Concerns

- `mergeMemories()` ignores provider errors by design per the task brief, so failures are silent. That matches the requirement, but it also means memory source health is not surfaced from `Runner`.
