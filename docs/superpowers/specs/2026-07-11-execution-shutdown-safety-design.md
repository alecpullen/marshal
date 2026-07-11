# Execution and Shutdown Safety Design

**Date:** 2026-07-11

## Purpose

This batch closes the execution-lifecycle gap identified in the project audit: foreground commands use the configured sandbox, while background commands launch through raw `execRunner`; output can grow without a bound; process termination is inconsistent; and application shutdown can close resources while a turn or background job is still active.

The implementation will unify foreground and background execution behind `native.CommandRunner`, make background work runtime-owned, bound command output, and enforce an ordered shutdown sequence. It is deliberately limited to execution and shutdown safety so it can ship as one independently testable change.

## Goals

1. Run foreground and background shell commands through the same selected sandbox backend.
2. Preserve live background output, job status, and process identification without exposing `os/exec.Cmd` outside an execution backend.
3. Bound stdout and stderr retained in memory for every command.
4. Make timeout, user cancellation, and runtime shutdown terminate the complete process tree and finish within a bounded interval.
5. Make the runtime own and join all background jobs before closing dependent resources.
6. Cancel an active TUI turn before `/quit`, `/exit`, or `Ctrl+C` exits the program.
7. Prevent configuration reload while a turn or background job is active.
8. Keep sandbox capability and audit reporting accurate for background commands.

## Non-goals

This batch does not change:

- ACP request concurrency or `session/load` behavior;
- provider locality classification or prompt redaction;
- workspace symlink confinement;
- web SSRF protection;
- container resource-limit accuracy beyond preserving current behavior;
- snapshot retention or branch semantics;
- the command approval policy or persistent permission rules.

Those findings remain separate audit batches.

## Chosen approach

`CommandRunner.Run` will remain the single execution boundary. `JobManager` will stop depending on `ProcessRunner.Start` and will invoke `CommandRunner.Run` asynchronously with a per-job context.

`CommandRequest` will gain optional observation fields:

```go
type CommandRequest struct {
    Command        string
    Dir            string
    Timeout        time.Duration
    MaxOutputBytes int
    Stdout         io.Writer
    Stderr         io.Writer
    OnStart        func(pid int)
}
```

The fields have these contracts:

- `MaxOutputBytes <= 0` selects the native-tool default output limit.
- `Stdout` and `Stderr` receive output incrementally and must not receive more than the configured retained-output limit per stream.
- `OnStart` is called at most once after successful process start and before waiting for process completion.
- `CommandResult.Stdout` and `CommandResult.Stderr` remain bounded snapshots for foreground callers and audit summaries.
- Backends remain responsible for starting, waiting for, cancelling, and collecting metadata from the process they create.

This approach reuses the sandbox boundary instead of creating a second background-process abstraction. It also removes the unexported `runningCmd` coupling between `JobManager` and `execRunner`.

## Components

### Bounded output capture

A concurrency-safe bounded writer in `internal/tools/native` will retain the first configured number of bytes and record whether later output was dropped. It will:

- never allocate beyond the configured bound plus fixed bookkeeping;
- accept writes after truncation by returning `len(p), nil`, preventing child-process pipe failures;
- expose a safe string snapshot while a command is running;
- expose a `Truncated()` flag;
- optionally forward the retained bytes to another bounded observer.

Each stdout and stderr stream receives its own limit. A command configured with `200000` bytes can therefore retain at most `400000` bytes across both streams. The final rendered tool content remains subject to the existing combined result truncation.

`SandboxMeta` will add `OutputTruncated bool`. Audit persistence will record the flag in `sandbox_limits_json`; no database migration is required because the limits column already contains JSON.

### Sandbox execution

Restricted, passthrough, and container backends will replace `cmd.Run()` with explicit `cmd.Start()` and `cmd.Wait()` so they can call `OnStart` and connect observed output before the process begins.

All backends will use the same bounded-capture helper. When observation writers are supplied, output is retained once by the backend and mirrored incrementally to the job's bounded buffers. The observer must also be bounded, so mirroring cannot reintroduce unbounded memory growth.

On Unix:

- restricted and passthrough shell processes will start in a new process group;
- cancellation first sends `SIGTERM` to the process group;
- after a fixed two-second grace period, remaining processes receive `SIGKILL`;
- backend `Run` does not return until `Wait` completes;
- a timeout is reported as `KilledReason = "timeout"`, explicit cancellation as `"cancelled"`.

Container execution continues to use the pinned Docker/Podman client path and current container arguments. This batch guarantees cancellation and joining of the runtime client process but does not claim that the current backend forcibly removes a daemon-side container after client loss; that guarantee belongs to the later container-hardening batch. Capability text and tests must not imply otherwise.

On Windows, existing process primitives remain supported without claiming Unix process-group semantics. The Windows implementation must still return within the shutdown bound by using the platform process-kill path already available to `exec.Cmd`.

### Background job lifecycle

`JobManager` will accept a `CommandRunner` and a runtime parent context:

```go
func NewJobManager(
    ctx context.Context,
    runner CommandRunner,
    dir string,
    maxJobs int,
    retention time.Duration,
    maxOutputBytes int,
) *JobManager
```

Starting a job will:

1. evict expired completed jobs;
2. count only jobs whose status is `running` against `maxJobs`;
3. create a child context derived from the runtime context;
4. apply the requested timeout to that child context;
5. create bounded stdout and stderr buffers;
6. register the job before launching its goroutine;
7. invoke `runner.Run` with the buffers and `OnStart` callback;
8. map the result into `completed`, `failed`, `timed_out`, or `killed`;
9. store exit code, sandbox metadata, truncation state, and completion time;
10. publish the updated running-job count.

`job.output` continues to show live retained output. Its combined output is constructed only from bounded snapshots. Completed jobs remain queryable until retention eviction but no longer consume a running-job slot.

`JobManager.Kill` cancels the job context and waits for the backend to finish. `JobManager.Shutdown(ctx)` cancels every running job and waits for all job goroutines. It returns a joined error if the caller's shutdown deadline expires before all jobs finish.

Starting a job after shutdown returns a stable `ErrJobManagerClosed` error.

### Runtime ownership

`Runtime` will own the active `JobManager` directly. Construction will create the selected sandbox first, create the job manager with that sandbox, and pass the manager into native tool registration.

Runtime teardown will have two explicit phases:

```go
func (rt *Runtime) Quiesce(ctx context.Context) error
func (rt *Runtime) Close(ctx context.Context) error
```

`Quiesce` stops work but keeps persistence and logging available for knowledge finalization. `Close` calls `Quiesce` idempotently and then closes resources. This split lets both the TUI and future headless transports use `Close` safely, while `app.Run` can quiesce work, run the knowledge pass against stable state, and then close storage.

The shutdown sequence will be:

1. `Quiesce` marks the runtime as closing and rejects new turns/jobs;
2. `Quiesce` cancels the runtime root context, including the active agent turn;
3. `Quiesce` clears pending approval and question state so transports cannot remain blocked;
4. `Quiesce` calls `JobManager.Shutdown` with a five-second deadline;
5. `app.Run` performs the existing five-second knowledge finalization against stable state;
6. `Close` closes MCP clients;
7. `Close` closes job, steering, and session-event brokers;
8. `Close` performs bounded snapshot pruning;
9. `Close` closes the database and log file;
10. `Close` completes state shutdown idempotently.

Knowledge finalization remains initiated by `app.Run`, but it will run only after `Quiesce` has joined the active turn and jobs and before `Close` closes the database. The finalization retains its existing five-second timeout.

`Runtime.Quiesce` and `Runtime.Close` will each be idempotent through separate `sync.Once` guards. They return joined cleanup errors instead of silently discarding failures, while still attempting every later cleanup stage.

### TUI quit and reload behavior

The TUI will use one `beginShutdown` helper for `Ctrl+C`, `/quit`, and `/exit`. It will:

- cancel the active turn if present;
- clear steering messages;
- resolve pending approvals as denied and pending questions as unanswered without blocking;
- request application quit.

An intentional `context.Canceled` result from the active runner will not become a provider error.

While `busy` is true or the session reports running background jobs, opening settings remains allowed for inspection, but saving/reloading is rejected with a visible message:

> Stop the active turn and background jobs before applying settings.

This avoids replacing the runner, sandbox, registry, MCP manager, or job manager while they are in use. Supporting hot replacement is outside this batch.

## Data flow

```text
shell.run(background=true)
  -> policy and approval
  -> JobManager.Start
  -> runtime-derived job context
  -> selected Sandbox.Run
  -> bounded stdout/stderr observers
  -> live job.output + status broker
  -> bounded result + SandboxMeta

quit / signal / runtime close
  -> cancel runtime context
  -> active turn returns
  -> JobManager cancels and joins jobs
  -> knowledge finalization
  -> MCP/brokers/snapshots/DB/log close
```

## Error handling

- An empty command or directory retains the current validation errors.
- Failure before process start sets job status to `failed`, removes it from the running count, and retains the error for `job.output`.
- A non-zero exit code sets `failed` and records the exit code.
- Deadline expiry sets `timed_out`, even when process termination returns an `exec.ExitError`.
- Explicit `job.kill` sets `killed` and waits for completion.
- Runtime cancellation sets running jobs to `killed` unless their individual deadline expired first.
- Output truncation is visible in `job.output`, tool content, and sandbox audit metadata.
- Shutdown continues through all cleanup stages, joining independent errors for the caller.
- Repeated `Close`, `Shutdown`, and `Kill` operations are safe and do not panic.

## Testing strategy

### Native tool and job tests

- A fake sandbox runner proves background jobs call the configured runner rather than `execRunner`.
- Foreground and background output are capped at the configured bound.
- Live `job.output` returns data written before completion.
- Completed jobs do not consume `maxJobs`.
- `job.kill` waits for backend completion and reports `killed`.
- Runtime-context cancellation terminates jobs and reports no remaining running jobs.
- Starting after shutdown returns `ErrJobManagerClosed`.

### Sandbox tests

- `OnStart` fires exactly once for each backend that successfully starts.
- Restricted and passthrough timeout tests spawn a child that ignores `SIGTERM`; the full process group is gone after the grace period.
- Output truncation is reported without deadlocking a noisy child.
- Cancellation and timeout retain distinct killed reasons.
- Existing environment and capability tests remain unchanged.

### Runtime tests

- `Runtime.Close` cancels an active runner before closing the DB.
- Background jobs are joined before broker and DB closure.
- Knowledge finalization observes a stable transcript after the runner finishes.
- Repeated close is harmless.
- Cleanup errors are returned while later cleanup stages still execute.

### TUI tests

- `Ctrl+C`, `/quit`, and `/exit` cancel an active turn before returning `tea.Quit`.
- Pending approval and question waiters are released during shutdown.
- Intentional cancellation does not set `ProviderError`.
- Settings save while busy or while jobs run is rejected with the exact visible message.
- Settings save remains available when idle.

### Verification commands

```bash
go test -count=1 ./internal/tools/native ./internal/sandbox ./internal/app ./internal/app/tui
go test -race -count=1 ./internal/tools/native ./internal/sandbox ./internal/app/session ./internal/app/tui
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

The pre-existing flaky `TestRunAllowsParallelReadBatchWithoutStalling` test must be made race-safe before the full-suite command can be treated as a reliable gate. That test-only repair is included in this batch because it is necessary to verify the concurrency work; it must not change production agent behavior.

## Documentation changes

- Update `docs/04-tooling-and-shell-safety.md` to describe sandbox parity for foreground and background execution.
- Update `README.md` and `CLAUDE.md` so sandboxing is no longer listed as planned.
- Update `docs/13-project-audit-2026-07-11.md` to mark only the findings resolved by this batch.
- Document the output cap, termination grace period, shutdown order, and container cancellation limitation.

## Acceptance criteria

1. No production background-command path directly constructs `execRunner` or `exec.Cmd` outside a sandbox backend.
2. Foreground and background output retention is bounded by configuration.
3. A completed job never consumes a running-job concurrency slot.
4. Timeout, kill, and shutdown finish within the documented grace and shutdown bounds on supported test platforms.
5. Runtime shutdown joins active turns and jobs before closing the database or log.
6. `Ctrl+C`, `/quit`, and `/exit` share the same safe cancellation path.
7. Configuration reload cannot replace execution infrastructure while work is active.
8. Sandbox metadata for background commands is persisted and exposes output truncation.
9. The targeted race suite, full test suite, vet, and build all pass from a clean worktree.
