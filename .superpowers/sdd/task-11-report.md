# Task 11 Report

## Summary

Wired the Task 10 memory browser into the real TUI with `Ctrl+K`, added a guarded `WithMemoryStore` option, wired Task 9 runner memory injection in `app.go` through a real DB-backed adapter that filters stale memories, and invoked Task 8 `knowledge.EndSession` at process shutdown as a best-effort pass that does not change `Run`'s returned program error.

## TDD Evidence

### RED

1. Added `TestCtrlKOpensMemoryBrowser` and `TestMemoryClosedMsgClosesOverlay` in `internal/app/tui/model_test.go`.
2. Ran:

```bash
go test ./internal/app/tui/... -run 'TestCtrlKOpensMemoryBrowser|TestMemoryClosedMsgClosesOverlay' -v
```

Observed failure:

- `undefined: WithMemoryStore`
- `m.memoryOpen undefined (type Model has no field or method memoryOpen)`

3. Added `TestRunTriggersKnowledgeEndSessionButSkipsWithNoMessages`, `TestRunWiresMemoryBrowserOpensWithCtrlK`, and `TestDBMemoryProviderFiltersStaleMemories` in `internal/app/app_test.go`.
4. Ran:

```bash
go test ./internal/app/... -run 'TestRunTriggersKnowledgeEndSession|TestRunWiresMemoryBrowser|TestDBMemoryProviderFiltersStaleMemories' -v
```

Observed failure before app wiring:

- `view missing memory browser after Ctrl+K`
- `undefined: dbMemoryProvider`

### GREEN

1. Implemented TUI memory overlay wiring in `internal/app/tui/model.go`.
2. Ran:

```bash
go test ./internal/app/tui/... -v
```

Result: PASS

3. Implemented app wiring in `internal/app/app.go`.
4. Ran:

```bash
go test ./internal/app/... -v
```

Result: PASS

## Changed Files

- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `.superpowers/sdd/task-11-report.md`

## Commands Run

```bash
go test ./internal/app/tui/... -run 'TestCtrlKOpensMemoryBrowser|TestMemoryClosedMsgClosesOverlay' -v
go test ./internal/app/tui/... -v
go test ./internal/app/... -run 'TestRunTriggersKnowledgeEndSession|TestRunWiresMemoryBrowser|TestDBMemoryProviderFiltersStaleMemories' -v
go test ./internal/app/... -v
gofmt -w internal/app/tui/model.go internal/app/tui/model_test.go internal/app/app.go internal/app/app_test.go
go test ./...
go build ./cmd/marshal
```

## Self-Review

- `Ctrl+K` opens the memory browser only when a DB has been supplied, avoiding the `memory.New` nil-DB panic path.
- `Run` now always passes the DB/project pair into the TUI, even when provider routing fails, so the memory browser stays available.
- `dbMemoryProvider` filters `db.MemoryConfidenceStale` before producing `contextpack.MemoryNote` values.
- `knowledge.EndSession` runs after the program runner and `Run` still returns the original program runner error unchanged.
- `knowledge.EndSession` uses a separate resolver in `app.go`, preserving the `internal/agent` / `internal/knowledge` import boundary.

## Concerns

- None.

## Fix report: task review findings

### Changed files

- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/app/tui/model_test.go`
- `.superpowers/sdd/task-11-report.md`

### Exact commands run

```bash
gofmt -w internal/app/app_test.go internal/app/tui/model_test.go
go test ./internal/app/tui/... -run 'TestCtrlKWithoutMemoryStoreDoesNothing' -v
go test ./internal/app/... -run 'TestRunUsesLiveConfigForShutdownKnowledgePass|TestRunReturnsProgramRunnerErrorAfterKnowledgeEndSession' -v
gofmt -w internal/app/app.go
go test ./internal/app/... -run 'TestRunUsesLiveConfigForShutdownKnowledgePass|TestRunReturnsProgramRunnerErrorAfterKnowledgeEndSession' -v
go test ./internal/app/tui/... -v
go test ./internal/app/... -v
go build ./cmd/marshal
go test ./...
```

### Pass/fail summary

- Initial focused run failed as expected on stale shutdown config: `TestRunUsesLiveConfigForShutdownKnowledgePass` resolved `"used old config"` instead of `"used reloaded config"`.
- Added regression coverage for `Ctrl+K` without a memory DB and for preserving the original `programRunner` error while still running `knowledge.EndSession`.
- After fixing `Run` to build the shutdown resolver from `state.Config` after `programRunner` returns, all focused tests passed.
- Required verification passed:
  - `go test ./internal/app/tui/... -v`
  - `go test ./internal/app/... -v`
  - `go build ./cmd/marshal`
  - `go test ./...`

### Concerns

- None.
