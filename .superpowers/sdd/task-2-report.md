# Task 2 Report: Background Shell Jobs

## Status

DONE

## Commits

- `767c834` feat(tools): background shell jobs with job.output/kill/list

## Summary

Implemented background shell jobs for Marshal as specified in the task brief:

- Added `JobManager` (`internal/tools/native/jobs_manager.go`) with `Start`, `Output`, `Kill`, `List`, `RunningCount`, and `Shutdown`.
- Added platform-specific process control (`internal/tools/native/jobs_unix.go`, `internal/tools/native/jobs_windows.go`) using process groups on Unix and best-effort kill on Windows.
- Extended `execRunner` (`internal/tools/native/runner.go`) with a `Start` method so it satisfies the new `ProcessRunner` interface used by `JobManager`.
- Added `job.output`, `job.kill`, and `job.list` tools (`internal/tools/native/jobs.go`).
- Extended `shell.run` (`internal/tools/native/command.go`) with a `background: bool` argument; background jobs are validated by the same conservative guardrails as foreground commands.
- Wired the job manager into `toolSet` and `Options` (`internal/tools/native/native.go`), with defaults from config (`max_background_jobs = 25`, `background_retention = "8h"`).
- Added `ShellToolConfig` fields and pointer-typed TOML overlays (`internal/app/config/config.go`, `internal/app/config/save.go`).
- Added `RunningJobsCount`/`SetRunningJobsCount` to `session.State` (`internal/app/session/session.go`) and rendered the count in the TUI status line (`internal/app/tui/status.go`).
- Added tests (`internal/tools/native/jobs_test.go`, `internal/app/config/config_test.go`).

Also fixed a pre-existing `go vet` failure in `internal/app/app.go`: `reloadAgentRuntime` was copying a `sync.Mutex` via `*runner = *newRunner`. Changed the signature to use `**agent.Runner` and `**swarm.Orchestrator` so the function reassigns pointers instead of copying structs. Updated the two call sites in `internal/app/app_test.go`.

## Verification

```bash
gofmt -w .
go vet ./...              # clean
CGO_ENABLED=1 go build ./cmd/marshal   # builds
go test ./internal/...    # PASS
go test ./...             # PASS
go test -race ./internal/tools/native/ -run 'TestJobManager|TestShellRunBackground|TestJobOutputAndList'  # PASS
```

Focused native tool tests all pass, including the failing-test-first `TestJobManagerStartAndKill` and `TestJobManagerEnforcesMaxJobs`.

## Concerns

- Background jobs use a plain `execRunner` rather than the configured sandbox runner (`sandbox.Sandbox` does not implement `ProcessRunner`). This matches the brief’s scope (process groups / best-effort kill) but means background jobs are not sandboxed. Future work could extend the sandbox backends with `Start`.
- The job manager stores a `retention` duration but does not yet evict old completed jobs; the map only grows until `Shutdown` or process exit.

## Fix Review Round

Addressed the review findings in the following files:

- `internal/app/app.go`: passed `Config: cfg` to `native.RegisterAll` so user `max_background_jobs` and `background_retention` values are honored.
- `internal/tools/native/jobs_manager.go`:
  - Added a `dir` field to `JobManager` and updated `NewJobManager` to accept it.
  - `Start` now builds `CommandRequest{Command: command, Dir: m.dir, Timeout: timeout}` so background jobs execute in the workspace root.
  - Implemented retention enforcement via `evictCompleted()`, called on `Start`, `Output`, `Kill`, `List`, and `RunningCount`; terminal jobs older than `retention` are removed.
  - Added per-job `completedAt` tracking and removed the unused `StatusLost` constant.
- `internal/tools/native/native.go`: updated the fallback `NewJobManager` call to pass the resolved workspace root.
- `internal/tools/native/command.go`: removed the duplicate `validateConservativeCommand` call on the foreground `shell.run` path (the background path still validates before launching).
- `internal/tools/native/jobs_test.go`: updated existing tests for the new `NewJobManager` signature and added tests for workspace directory, retention eviction, and config wiring.

### Verification

```bash
gofmt -w .
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
go test ./internal/...
go test ./...
go test -race ./internal/tools/native/ -run 'TestJobManager|TestShellRunBackground|TestJobOutputAndList|TestRegisterAllHonors' -v
```

All focused job tests and the full `./internal/...` and `./...` suites pass.
