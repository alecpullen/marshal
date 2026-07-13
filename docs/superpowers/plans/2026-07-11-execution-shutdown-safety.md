# Execution and Shutdown Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make foreground and background commands use the same configured sandbox, bound retained command output, and make quit/config-reload/runtime teardown cancel and join active work before dependent resources close.

**Architecture:** Keep `native.CommandRunner.Run` as the sole process-execution boundary and extend `CommandRequest` with bounded output observers plus a start callback. `JobManager` runs that interface asynchronously under a runtime-owned context. A session work gate tracks active agent turns; `Runtime.Quiesce` closes the gate, cancels and joins turns/jobs, and `Runtime.Close` then releases MCP, brokers, snapshots, database, and logging. TUI quit paths resolve pending interaction waiters, while settings remain inspectable but cannot save during active work.

**Tech Stack:** Go 1.24, `os/exec`, platform-specific process groups, Bubble Tea v2, existing pub/sub and SQLite layers.

**Spec:** `docs/superpowers/specs/2026-07-11-execution-shutdown-safety-design.md`

## Global Constraints

- Follow TDD in every production task: add the named failing tests, run them and observe the expected failure, then implement only enough behavior to pass.
- `CommandRunner.Run` remains the only execution API. Do not add a public `Start` method or expose `*exec.Cmd` from `internal/sandbox`.
- Each stdout and stderr stream has its own `MaxOutputBytes` limit. A write after the limit returns `len(p), nil` and sets a truncation flag.
- Foreground and background commands must receive the selected `sandbox.Sandbox`; production background execution must never instantiate `execRunner` or `exec.Cmd` outside a backend.
- Unix cancellation is `SIGTERM` to the process group, a two-second grace period, then `SIGKILL`; Windows uses the supported `exec.Cmd.Process.Kill` path without claiming process-group isolation.
- Container mode guarantees cancellation/join of the local Docker/Podman client only. Do not claim daemon-side container removal in code, tests, or documentation.
- `Runtime.Quiesce` must leave the DB and logger usable for `knowledge.EndSession`. `Runtime.Close` calls `Quiesce` and then closes resources.
- All shutdown methods are idempotent and continue later cleanup after earlier errors. Use `errors.Join` for independent failures.
- Settings save rejection text is exactly: `Stop the active turn and background jobs before applying settings.`
- Do not broaden this batch into ACP concurrency/session loading, privacy/redaction, symlink confinement, SSRF, policy parsing, snapshot semantics, or container-limit accuracy.
- Preserve the uncommitted audit document when making task commits. Stage only the paths named in each task.

---

### Task 1: Make the parallel-read regression test race-safe

This test-only repair establishes a trustworthy race gate before lifecycle concurrency changes begin.

**Files:**
- Modify: `internal/agent/runner_test.go`

- [ ] **Step 1: Reproduce the race**

Run:

```bash
go test -race -count=20 ./internal/agent -run TestRunAllowsParallelReadBatchWithoutStalling
```

Expected: the race detector reports concurrent access to the test-local `executed` slice from parallel `file.read` handlers.

- [ ] **Step 2: Protect both writes and the final read**

Add `var executedMu sync.Mutex` beside `executed`. In the handler, lock around `append`. After `RunTask`, copy the length under the same lock:

```go
var executedMu sync.Mutex
var executed []string

Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
	executedMu.Lock()
	executed = append(executed, string(call.Args))
	executedMu.Unlock()
	return registry.ToolResult{Summary: "ok", Content: "package main"}, nil
},
```

```go
executedMu.Lock()
executedCount := len(executed)
executedMu.Unlock()
if executedCount != 5 {
	t.Fatalf("executed %d reads, want 5 (batch of 4 + 1 follow-up)", executedCount)
}
```

- [ ] **Step 3: Verify the regression repeatedly**

Run:

```bash
go test -race -count=20 ./internal/agent -run TestRunAllowsParallelReadBatchWithoutStalling
```

Expected: PASS with no race report.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner_test.go
git commit -m "test(agent): make parallel read regression race safe"
```

---

### Task 2: Add the bounded output contract and audit metadata

Create one reusable, concurrency-safe capture primitive before changing any backend.

**Files:**
- Create: `internal/tools/native/output.go`
- Create: `internal/tools/native/output_test.go`
- Modify: `internal/tools/native/native.go`
- Modify: `internal/tools/native/runner.go`
- Create: `internal/tools/native/runner_test.go`
- Modify: `internal/tools/registry/types.go`
- Create: `internal/tools/registry/types_test.go`

**Interfaces:**

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

type BoundedOutput struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
	observer  io.Writer
}

func NewBoundedOutput(limit int, observer io.Writer) *BoundedOutput
func (b *BoundedOutput) Write(p []byte) (int, error)
func (b *BoundedOutput) String() string
func (b *BoundedOutput) Truncated() bool
func OutputLimit(limit int) int
```

`OutputLimit` returns `defaultMaxOutputBytes` for non-positive input. `BoundedOutput.Write` retains and forwards only the prefix that fits; observer errors are returned only for the forwarded prefix, while fully dropped writes return `len(p), nil`.

- [ ] **Step 1: Add failing capture and metadata tests**

In `output_test.go`, add:

- `TestBoundedOutputRetainsPrefixAndReportsTruncation`: limit 5, write `abc` then `def`, expect string `abcde`, both writes report their original lengths, and `Truncated()` is true.
- `TestBoundedOutputForwardsOnlyRetainedBytes`: attach a `bytes.Buffer`, write beyond the limit, and expect observer content to equal the retained prefix exactly.
- `TestBoundedOutputConcurrentSnapshot`: write from 16 goroutines while calling `String` and `Truncated`; run under `-race` and assert `len(String()) <= limit`.
- `TestOutputLimitUsesDefault`: non-positive values return `defaultMaxOutputBytes`; a positive value is unchanged.

In `types_test.go`, add `TestSandboxMetaLimitsJSONIncludesOutputTruncated`; unmarshal the JSON and require `output_truncated: true` only when the field is true.

Run:

```bash
go test ./internal/tools/native ./internal/tools/registry
```

Expected: FAIL to compile because `BoundedOutput`, the observer fields, and `SandboxMeta.OutputTruncated` do not exist.

- [ ] **Step 2: Implement `BoundedOutput` and extend the request**

Add `io` to `native.go`, add the exact `CommandRequest` fields above, and implement the methods in `output.go`. Allocate `buf` with capacity `min(limit, 32*1024)`, never capacity `limit`, so a large user limit does not eagerly reserve memory.

When an observer accepts fewer bytes than supplied without an error, return `io.ErrShortWrite`. Never hold more than `limit` bytes and never call the observer for dropped bytes.

- [ ] **Step 3: Record truncation in sandbox audit JSON**

Add this field to `registry.SandboxMeta`:

```go
OutputTruncated bool
```

Add this branch to `LimitsJSON`:

```go
if m.OutputTruncated {
	limits["output_truncated"] = true
}
```

No database migration is needed; `internal/db/audits.go` already persists `LimitsJSON()`.

- [ ] **Step 4: Convert the fallback runner to bounded observed output**

In `execRunner.Run`, replace `bytes.Buffer` with `NewBoundedOutput(req.MaxOutputBytes, req.Stdout)` and its stderr equivalent. Use explicit `Start` and `Wait`, call `req.OnStart(cmd.Process.Pid)` once after a successful start, and set `result.Meta.OutputTruncated` when either capture truncated. Leave the legacy `execRunner.Start` method temporarily so the old `JobManager` still compiles at this green checkpoint; Task 4 removes the method together with its final callers.

Add `TestExecRunnerBoundsAndObservesOutput` and `TestExecRunnerCallsOnStartOnce`. The first runs a command that emits more than 8 bytes on both streams and asserts result and observer bounds.

- [ ] **Step 5: Verify**

Run:

```bash
go test -race -count=1 ./internal/tools/native ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/output.go internal/tools/native/output_test.go internal/tools/native/native.go internal/tools/native/runner.go internal/tools/native/runner_test.go internal/tools/registry/types.go internal/tools/registry/types_test.go
git commit -m "feat(native): bound observed command output"
```

---

### Task 3: Give every sandbox backend observed output and bounded termination

Centralize start/wait/cancel behavior so restricted, passthrough, and container backends cannot drift.

**Files:**
- Create: `internal/sandbox/execute.go`
- Create: `internal/sandbox/execute_test.go`
- Create: `internal/sandbox/process_unix.go`
- Create: `internal/sandbox/process_windows.go`
- Modify: `internal/sandbox/passthrough.go`
- Modify: `internal/sandbox/restricted.go`
- Modify: `internal/sandbox/container.go`
- Modify: `internal/sandbox/shell_unix.go`
- Modify: `internal/sandbox/shell_windows.go`
- Modify: `internal/sandbox/restricted_unix.go`
- Modify: `internal/sandbox/restricted_windows.go`
- Modify: `internal/sandbox/sandbox_test.go`
- Create: `internal/sandbox/restricted_test.go`
- Create: `internal/sandbox/container_test.go`

**Interfaces:**

```go
const terminationGrace = 2 * time.Second

func executeCommand(ctx context.Context, cmd *exec.Cmd, req native.CommandRequest, meta registry.SandboxMeta) (native.CommandResult, error)
func configureProcessGroup(cmd *exec.Cmd)
func terminateProcessTree(cmd *exec.Cmd, grace time.Duration) error
```

`executeCommand` owns the `BoundedOutput` objects, invokes `OnStart`, waits exactly once, maps context state to `KilledReason`, and sets duration/truncation metadata. It must use an unbound `exec.Cmd` plus an explicit context-select; do not rely on `exec.CommandContext`'s immediate direct-child kill.

- [ ] **Step 1: Add failing backend contract tests**

Add table-driven coverage for restricted and passthrough:

- `TestSandboxRunCallsOnStartOnce` records the PID and requires one positive callback.
- `TestSandboxRunBoundsResultAndObserver` emits 64 bytes per stream with an 8-byte limit and requires both result strings and observers to be 8 bytes plus `Meta.OutputTruncated == true`.
- `TestSandboxCancellationReason` cancels a parent context and expects `KilledReason == "cancelled"`.
- `TestSandboxTimeoutReason` uses `CommandRequest.Timeout` and expects `KilledReason == "timeout"`.

On Unix, add `TestSandboxCancellationKillsProcessGroupAfterGrace`: run a shell that starts a child ignoring `TERM`, capture both PIDs via a temporary file, cancel, require `Run` to return between two and four seconds, and use `syscall.Kill(pid, 0)` polling to prove both processes are gone. Skip only on Windows.

For container, use the existing fake-runtime fixture to assert observed output, one `OnStart` call, and local-client cancellation. Do not assert daemon-side removal.

Run:

```bash
go test -count=1 ./internal/sandbox
```

Expected: FAIL because the current backends use unbounded buffers, do not observe starts, and kill immediately.

- [ ] **Step 2: Implement the shared execution loop**

`executeCommand` performs this sequence:

1. Create stdout/stderr via `native.NewBoundedOutput(req.MaxOutputBytes, req.Stdout/Stderr)` and attach them before start.
2. Call `configureProcessGroup(cmd)` before `cmd.Start()`.
3. Record start time, call `cmd.Start`, then call `OnStart` once if non-nil.
4. Start one goroutine that sends `cmd.Wait()` to a buffered channel.
5. Select between that result and `ctx.Done()`.
6. On cancellation, call `terminateProcessTree(cmd, terminationGrace)`, then receive the wait result before returning.
7. Prefer `"timeout"` when `errors.Is(ctx.Err(), context.DeadlineExceeded)`; otherwise use `"cancelled"`.
8. Fill exit code, duration, output strings, and `OutputTruncated` before returning the process error.

The command constructors in `shell_unix.go` / `shell_windows.go` become `shellCommand(command string) *exec.Cmd` and use `exec.Command`, not `exec.CommandContext`.

- [ ] **Step 3: Implement platform termination**

Unix `configureProcessGroup` sets `SysProcAttr.Setpgid = true`. Unix `terminateProcessTree` sends `SIGTERM` to `-pid`, polls group existence until the grace deadline, and sends `SIGKILL` to `-pid` if it still exists. Treat `ESRCH` as success.

Windows `configureProcessGroup` is a no-op and `terminateProcessTree` calls `cmd.Process.Kill()` when a process exists. It must not sleep for the Unix grace interval.

Remove `applyProcmgmt` from the restricted platform files; retain only resource-limit helpers there.

- [ ] **Step 4: Route all backends through the helper**

- Restricted keeps validation, confined dir, env building, and `restrictedWrapCommand`; it obtains `runCtx` from `runWithTimeout(ctx, req)` and calls `executeCommand(runCtx, cmd, req, metaFor(r.Capabilities(), r.cfg))`.
- Passthrough obtains `runCtx` from `runWithTimeout(ctx, req)`, creates the shell command, applies `req.Dir`, and calls `executeCommand(runCtx, cmd, req, registry.SandboxMeta{Enabled: true, Backend: "passthrough"})`.
- Container keeps its pinned runtime path and exact argument/environment construction, obtains `runCtx` from `runWithTimeout(ctx, req)`, creates `exec.Command(c.runtimePath, args...)`, and calls `executeCommand(runCtx, cmd, req, metaFor(c.Capabilities(), c.cfg))`.
- Remove all backend-local `bytes.Buffer`, `cmd.Run`, elapsed-time, and killed-reason code.

- [ ] **Step 5: Verify normal and race paths**

Run:

```bash
go test -count=1 ./internal/sandbox
go test -race -count=1 ./internal/sandbox
```

Expected: PASS. The Unix process-group test should take roughly two seconds because its child ignores `SIGTERM`.

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox
git commit -m "feat(sandbox): stream bounded output and join cancellation"
```

---

### Task 4: Rebuild `JobManager` on `CommandRunner.Run`

Remove the raw-process coupling and make the manager runtime-scoped, bounded, and joinable.

**Files:**
- Rewrite: `internal/tools/native/jobs_manager.go`
- Modify: `internal/tools/native/jobs_test.go`
- Modify: `internal/tools/native/native.go`
- Delete: `internal/tools/native/jobs_unix.go`
- Delete: `internal/tools/native/jobs_windows.go`

**Interfaces:**

```go
var ErrJobManagerClosed = errors.New("job manager is closed")

func NewJobManager(ctx context.Context, runner CommandRunner, dir string, maxJobs int, retention time.Duration, maxOutputBytes int) *JobManager
func (m *JobManager) Start(ctx context.Context, command string, timeout time.Duration) (string, error)
func (m *JobManager) Kill(id string) error
func (m *JobManager) Shutdown(ctx context.Context) error
```

Add `Sandbox registry.SandboxMeta` and `OutputTruncated bool` to `JobInfo`. Each job owns `stdout`, `stderr`, `cancel`, `done`, and its final error; it no longer owns `runningCmd`.

- [ ] **Step 1: Replace job tests with runner-level behavior tests**

Create a controllable fake `CommandRunner` that records `CommandRequest`, calls `OnStart(4242)`, writes live bytes to the observers, waits on either a release channel or `ctx.Done`, and records that it returned.

Add these tests:

- `TestJobManagerUsesConfiguredRunnerAndReportsSandboxMeta` proves the fake sandbox is called, PID is observed, and its metadata survives in `JobInfo`.
- `TestJobOutputIsLiveAndBounded` reads output before release, emits beyond the configured limit, and requires the truncation marker and flag.
- `TestCompletedJobsDoNotConsumeConcurrency` with `maxJobs=1` completes job 1 and starts job 2 while job 1 remains retained.
- `TestJobKillWaitsForRunner` makes cancellation cleanup block until released and proves `Kill` does not return early.
- `TestJobTimeoutIsDistinctFromKill` requires `timed_out` for a job deadline and `killed` for explicit cancellation.
- `TestJobManagerParentCancellationKillsJobs` cancels the constructor context and waits for zero running jobs.
- `TestJobManagerShutdownJoinsAndRejectsStart` requires `Shutdown` to wait and subsequent `Start` to return `ErrJobManagerClosed`.
- `TestJobManagerShutdownHonorsCallerDeadline` uses a deliberately stuck fake and expects `context.DeadlineExceeded`.

Run:

```bash
go test ./internal/tools/native
```

Expected: FAIL to compile against the old constructor and `Shutdown()` signature.

- [ ] **Step 2: Implement the asynchronous Run lifecycle**

The constructor derives `managerCtx, managerCancel := context.WithCancel(ctx)`, normalizes max jobs, retention, and output limit, and initializes a closed flag plus a manager-wide wait group.

`Start` must:

1. reject blank commands and a closed manager;
2. evict expired completed jobs;
3. count only `StatusRunning` jobs while holding the manager lock;
4. reject an already-cancelled call context, then derive the long-lived job context from `managerCtx` (not the tool-call context), with an optional timeout;
5. allocate bounded observer buffers and register the job before spawning;
6. increment the wait group before the goroutine starts;
7. call `runner.Run` with `MaxOutputBytes`, both observers, and an `OnStart` callback that updates PID;
8. classify completion using the job context cause and the explicit-kill flag;
9. store exit code, sandbox metadata, error text, truncation, and completion time;
10. close `done`, decrement the wait group, and publish the new count.

Use a per-job `killRequested bool`; do not pre-mark the job terminal before the runner returns.

- [ ] **Step 3: Make output, kill, and shutdown deterministic**

`Output` snapshots buffers safely even before `OnStart`, joins stdout/stderr, applies `tailString`, and appends `\n[output truncated]` once when either buffer or sandbox metadata reports truncation.

`Kill` sets `killRequested`, calls the cancel function, then waits on `done`; repeated kill of a terminal job is harmless.

`Shutdown` sets `closed`, cancels the manager context, and waits for the manager wait group through a channel:

```go
done := make(chan struct{})
go func() {
	m.wg.Wait()
	close(done)
}()
select {
case <-done:
	return nil
case <-ctx.Done():
	return ctx.Err()
}
```

Close state must be set before cancellation so no concurrent `Start` can add to the wait group after waiting begins.

- [ ] **Step 4: Remove the sandbox bypass fallback**

Delete `ProcessRunner`, `runningCmd`, and both platform job files. In `newToolSet`, when no manager was supplied, construct one from the already-selected `runner`:

```go
jobManager = NewJobManager(context.Background(), runner, root, maxBg, retention, maxOutputBytes)
```

This fallback exists for package tests and direct library use. Task 6 makes production construct and own the manager explicitly.

- [ ] **Step 5: Verify**

Run:

```bash
go test -race -count=1 ./internal/tools/native
```

Expected: PASS with no goroutine leak or race report.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native
git commit -m "feat(native): run background jobs through command runner"
```

---

### Task 5: Add a session work gate and shutdown waiter resolution

Give runtime shutdown a race-free way to reject new turns, cancel current turns, and wait for their Bubble Tea commands to return.

**Files:**
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`

**Interfaces:**

```go
var ErrSessionQuiescing = errors.New("session is quiescing")

func (s *State) BeginWork() error
func (s *State) EndWork()
func (s *State) BeginQuiesce()
func (s *State) WaitForWork(ctx context.Context) error
func (s *State) ResolvePendingForShutdown()
```

The state owns a separate `workMu`, `workWG`, and `quiescing` flag. `BeginWork` locks, rejects after quiesce, then calls `Add(1)` before unlocking. `BeginQuiesce` locks and sets the gate before any wait begins.

- [ ] **Step 1: Add failing session lifecycle tests**

Add:

- `TestWorkGateWaitsForActiveWork`: begin work, start `WaitForWork`, prove it blocks, end work, prove it returns.
- `TestWorkGateRejectsAfterQuiesce`: call `BeginQuiesce`, then require `errors.Is(BeginWork(), ErrSessionQuiescing)`.
- `TestResolvePendingForShutdownReleasesWaiters`: install buffered approval and question channels, queue steering, call the method, and assert denial, one `AnswerUnanswered` per question, nil pending pointers, and an empty steering queue.
- `TestResolvePendingForShutdownNeverBlocks`: install unbuffered channels with no receivers and require the method to return promptly while still clearing state.

Run:

```bash
go test ./internal/app/session
```

Expected: FAIL to compile because the lifecycle methods do not exist.

- [ ] **Step 2: Implement the work gate and pending resolution**

`WaitForWork` waits via a channel around `workWG.Wait()` and respects the caller context. It is valid only after `BeginQuiesce`; document that contract.

`ResolvePendingForShutdown` atomically takes and clears pending pointers and steering under the state mutex, then publishes cleared-state events and uses non-blocking sends:

```go
select {
case approval.ResponseChan <- UserApprovalDecision{Approved: false}:
default:
}
```

Construct question answers in original order with `Answer: AnswerUnanswered` and use the same non-blocking send pattern.

- [ ] **Step 3: Register TUI work before dispatch**

On normal and swarm submissions, call `state.BeginWork()` before creating the asynchronous command. If it returns `ErrSessionQuiescing`, do not start a runner. Change the command helper to:

```go
func runAgentCmd(ctx context.Context, state *session.State, runner AgentRunner, goal string) tea.Cmd {
	return func() tea.Msg {
		defer state.EndWork()
		err := runner.Run(ctx, goal)
		return agentFinishedMsg{err: err}
	}
}
```

Update both call sites and existing batch-command tests. Add `TestAgentCommandRegistersAndReleasesSessionWork` using a blocking fake runner.

- [ ] **Step 4: Verify**

Run:

```bash
go test -race -count=1 ./internal/app/session ./internal/app/tui
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(session): track and resolve active work"
```

---

### Task 6: Wire the selected sandbox and `JobManager` into runtime ownership

Make the application construct one manager from the sandbox and keep its replacement reachable after an idle config reload.

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/runtime.go`
- Modify: `internal/app/app_test.go`
- Modify: `internal/app/live_agent_test.go`

**Interface change:** `buildAgentRunner` returns the constructed `*native.JobManager` immediately after `*snapshot.Service`:

```go
func buildAgentRunner(
	ctx context.Context,
	cfg config.Config,
	state *session.State,
	database *db.DB,
	projectID int64,
	skillIndex *skills.Index,
	dataDir string,
	jobBroker *pubsub.Broker[native.JobEvent],
) (*agent.Runner, *registry.Registry, *swarm.Orchestrator, *mcp.Manager, *snapshot.Service, *native.JobManager, error)
```

Add `JobManager *native.JobManager`, `workCtx context.Context`, `workCancel context.CancelFunc`, and a mutex protecting reload-owned pointers to `Runtime`.

- [ ] **Step 1: Add failing app wiring tests**

Add:

- `TestBuildAgentRunnerBackgroundShellUsesConfiguredSandbox`: configure restricted mode with an explicit-empty env allowlist, start a background command that prints a sentinel secret from the parent env, and require the output not to contain it. Also require job metadata backend `restricted`.
- `TestBuildAgentRunnerUsesConfiguredOutputLimit`: set `MaxOutputBytes=8`, run foreground and background noisy commands, and require bounded results and truncation metadata.
- `TestStartRuntimeOwnsJobManager`: require a non-nil runtime manager and pointer identity with the manager used by registered `job.*` tools (expose a package-private test accessor only if direct behavior cannot prove identity).
- `TestReloadAgentRuntimeReplacesReachableManagerWhenIdle`: reload, require the runtime pointer to change, old manager to reject starts, and the new manager to execute jobs.

Run:

```bash
go test ./internal/app -run 'Test(BuildAgentRunner|StartRuntime|ReloadAgentRuntime)'
```

Expected: FAIL because `buildAgentRunner` neither returns nor explicitly constructs a manager, and `MaxOutputBytes` is not wired.

- [ ] **Step 2: Construct manager after sandbox selection**

Inside `buildAgentRunner`, after `sandbox.New` succeeds, create:

```go
jobManager := native.NewJobManager(
	ctx,
	commandRunner,
	state.WorkingDir,
	cfg.Tools.Shell.MaxBackgroundJobs,
	cfg.Tools.Shell.BackgroundRetention,
	cfg.Tools.Shell.MaxOutputBytes,
)
```

Pass both `MaxOutputBytes: cfg.Tools.Shell.MaxOutputBytes` and `JobManager: jobManager` to `native.RegisterAll`. Return the manager on success. If later construction fails, shut it down with a bounded context before returning.

- [ ] **Step 3: Give `StartRuntime` a dedicated work context**

Create `workCtx, workCancel := context.WithCancel(ctx)` before broker and runner construction. Use it for `buildAgentRunner` and broker pumps. Store it and the returned job manager on `Runtime`. Startup-error cleanup must call `workCancel`, close any MCP manager, shut down the job manager, close brokers, DB, and log file.

- [ ] **Step 4: Replace execution infrastructure atomically on idle reload**

Change `reloadAgentRuntime` to accept `rt *Runtime` instead of loose active MCP/job-context pointers. It builds replacements using `rt.workCtx`, then under the runtime pointer mutex swaps `ToolRegistry`, `MCPManager`, `Snapshot`, and `JobManager` after `Runner.CopyFrom` / swarm copy succeeds. After the swap, close the old MCP manager and call old-job-manager `Shutdown` with a two-second context. The TUI guard in Task 8 guarantees no active work; still propagate cleanup errors.

Update all app and live-agent call sites for the expanded return tuple and reload signature.

- [ ] **Step 5: Verify**

Run:

```bash
go test -race -count=1 ./internal/app
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/app/runtime.go internal/app/app_test.go internal/app/live_agent_test.go
git commit -m "fix(app): make runtime own sandboxed background jobs"
```

---

### Task 7: Implement ordered, idempotent runtime quiesce and close

Separate work shutdown from resource cleanup so knowledge finalization sees stable state and an open database.

**Files:**
- Modify: `internal/app/runtime.go`
- Create: `internal/app/runtime_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**

```go
func (rt *Runtime) Quiesce(ctx context.Context) error
func (rt *Runtime) Close(ctx context.Context) error
```

Add separate `quiesceOnce`, `closeOnce`, `quiesceErr`, and `closeErr` fields. Use a package variable `jobShutdownTimeout = 5 * time.Second` so timeout behavior is testable without sleeping five seconds.

- [ ] **Step 1: Add failing lifecycle-order tests**

Use fakes/hooks rather than real five-second waits. Add:

- `TestRuntimeQuiesceCancelsAndJoinsTurnBeforeReturning`: register session work, block it until the work context is cancelled, then require `Quiesce` to wait for `EndWork`.
- `TestRuntimeQuiesceJoinsBackgroundJobs`: use a blocking runner-backed manager and require zero running jobs before return.
- `TestRuntimeQuiesceLeavesDatabaseOpen`: insert/read a row successfully after quiesce.
- `TestRuntimeCloseClosesResourcesAfterQuiesce`: record cleanup order and require work, jobs, MCP, brokers, snapshot, DB, log/state order.
- `TestRuntimeCloseIsIdempotent`: call twice and assert each injected close hook ran once.
- `TestRuntimeCloseJoinsErrorsAndContinues`: inject failures into multiple cleanup seams, require `errors.Is` for each, and prove later hooks ran.
- `TestRunQuiescesBeforeKnowledgeAndClosesAfter`: make the program runner launch tracked work, exit, and use a knowledge test seam to assert no active work plus usable DB during finalization.

Run:

```bash
go test ./internal/app -run 'Test(Runtime|RunQuiesces)'
```

Expected: FAIL because `Quiesce` and idempotent ordered cleanup are absent.

- [ ] **Step 2: Implement `Runtime.Quiesce`**

Within `quiesceOnce`:

1. call `State.BeginQuiesce()`;
2. call `workCancel()`;
3. call `State.ResolvePendingForShutdown()` and `State.Shutdown()`;
4. snapshot the current `JobManager` under the runtime pointer mutex;
5. call `JobManager.Shutdown` with `min(caller deadline, jobShutdownTimeout)`;
6. call `State.WaitForWork` with the caller context;
7. store `errors.Join(jobErr, workErr)`.

Always attempt both joins. Do not close MCP, brokers, snapshots, DB, or logger here.

- [ ] **Step 3: Implement `Runtime.Close`**

Call `Quiesce` first, then inside `closeOnce` attempt every stage in this order:

1. MCP manager;
2. job-broker pump cancellation and job broker;
3. steering broker;
4. event broker;
5. bounded snapshot DB prune and filesystem prune;
6. database;
7. reverse-order `closeFns` (the log file);
8. idempotent state shutdown.

Append each error and store `errors.Join(errs...)`. Return `errors.Join(rt.quiesceErr, rt.closeErr)` on every call.

- [ ] **Step 4: Put knowledge between the two phases**

In `Run`, after `programRunner` returns:

```go
quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), jobShutdownTimeout)
quiesceErr := rt.Quiesce(quiesceCtx)
cancelQuiesce()

knowledgeCtx, cancelKnowledge := context.WithTimeout(context.Background(), shutdownKnowledgeTimeout)
knowledge.EndSession(knowledgeCtx, knowledge.EndSessionInput{
	DB:            database,
	ProjectID:     projectID,
	SessionID:     sessionID,
	State:         state,
	RouteResolver: newRoutedProviderResolver(state.Config),
	WorkingDir:    workingDir,
	Now:           runOpts.now,
	Logger:        logger,
})
cancelKnowledge()

closeErr := rt.Close(context.Background())
return errors.Join(progErr, quiesceErr, closeErr)
```

Retain a deferred best-effort `Close` immediately after `StartRuntime` as panic/early-return protection. Remove the separate goroutine that only calls `state.Shutdown` on parent cancellation; the runtime work context now owns this behavior.

- [ ] **Step 5: Verify**

Run:

```bash
go test -race -count=1 ./internal/app ./internal/app/session
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/runtime.go internal/app/runtime_test.go internal/app/app.go internal/app/app_test.go
git commit -m "fix(app): quiesce work before runtime cleanup"
```

---

### Task 8: Unify safe TUI quit and block settings save during work

Keep settings inspection available, but make all quit paths cancel cleanly and all save paths respect the execution-lifecycle guard.

**Files:**
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`
- Modify: `internal/app/tui/settings/model.go`
- Modify: `internal/app/tui/settings/model_test.go`

**Interfaces:**

```go
const settingsBusyMessage = "Stop the active turn and background jobs before applying settings."

func (m *Model) beginShutdown() tea.Cmd
func (m Model) settingsBlockReason() string
func (m *Model) syncSettingsSaveBlock()
func (m *Model) SetSaveBlocked(reason string)
```

The settings model gains `saveBlocked string`. `saveCmd` checks it before any file write or reload, sets `footerMsg`, and returns nil.

- [ ] **Step 1: Add failing quit and cancellation tests**

Add table-driven tests for `ctrl+c`, `/quit`, and `/exit` that begin a blocking fake turn and install pending approval/question waiters. Each path must:

- call the turn cancel function;
- clear steering;
- deliver denial and unanswered responses when channels can accept them;
- clear pending state;
- return `tea.Quit`.

Add `TestIntentionalAgentCancellationDoesNotSetProviderError`: feed `agentFinishedMsg{err: context.Canceled}` and require `ProviderError() == nil`. Preserve the existing non-cancellation error test.

Run:

```bash
go test ./internal/app/tui -run 'Test(CtrlC|Quit|Exit|IntentionalAgentCancellation)'
```

Expected: FAIL because the three paths do not share safe cancellation and cancellation becomes a provider error.

- [ ] **Step 2: Implement one shutdown path**

`beginShutdown` calls `agentCancel` if present, clears it, resets cached steering count, calls `state.ResolvePendingForShutdown()`, calls `state.Shutdown()`, and returns `tea.Quit`. Route the top-level Ctrl+C guard and both command names through it.

In `agentFinishedMsg`, set a provider error only when:

```go
if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
	m.state.SetProviderError(msg.err)
}
```

- [ ] **Step 3: Add failing settings guard tests**

Add:

- settings-package `TestSaveBlockedDoesNotWrite`: set a path with known contents, call `SetSaveBlocked(settingsBusyMessage)`, send Ctrl+S, and require unchanged bytes, nil command, and exact footer.
- TUI `TestSettingsSaveBlockedDuringAgentTurn`: open settings while busy, edit a value, send Ctrl+S, and require the exact footer plus no reloader call.
- TUI `TestSettingsSaveBlockedDuringBackgroundJob`: set `RunningJobsCount(1)` before opening settings and assert the same.
- TUI `TestSettingsSaveAllowedWhenIdle`: open/edit/save and require the reloader call.
- TUI `TestSettingsOverlayDoesNotSwallowAgentFinishedOrJobCount`: while settings is open, feed both runtime messages and require busy/job cached state to update while the overlay stays open.

Run:

```bash
go test ./internal/app/tui ./internal/app/tui/settings -run 'TestSettings'
```

Expected: FAIL because settings has no save block and the overlay routes runtime messages away from the parent.

- [ ] **Step 4: Implement the settings guard and runtime-message routing**

Add `SetSaveBlocked` and the early `saveCmd` return. In the parent, `settingsBlockReason` returns the exact constant when `m.busy || m.state.RunningJobsCount() > 0`, else empty. Call `syncSettingsSaveBlock` whenever settings opens and after `agentFinishedMsg` / `jobCountMsg`.

Before overlay routing, handle these non-key messages through a parent helper: `agentFinishedMsg`, `jobCountMsg`, `steeringMsg`, `agentTickMsg`, and `spinnerTickMsg`. The helper updates parent state and re-arms broker/tick commands exactly as the current switch does. Overlay models continue receiving their own key/internal messages, but cannot freeze runtime state.

Keep opening settings via Ctrl+O or `/settings` available during active work.

- [ ] **Step 5: Verify TUI and settings under race detection**

Run:

```bash
go test -race -count=1 ./internal/app/tui ./internal/app/tui/settings
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go internal/app/tui/settings/model.go internal/app/tui/settings/model_test.go
git commit -m "fix(tui): cancel safely and guard live settings reload"
```

---

### Task 9: Document guarantees, close the audit items, and run release gates

Update only claims delivered by this batch, then verify the complete repository.

**Files:**
- Modify: `docs/04-tooling-and-shell-safety.md`
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Update the tooling and shell safety guide**

Document:

- foreground/background sandbox parity;
- the per-stream `max_output_bytes` cap and visible truncation marker;
- Unix `SIGTERM`, two-second grace, and `SIGKILL` behavior;
- runtime-owned job cancellation/join;
- the container limitation: the local runtime client is joined, but daemon-side removal after client loss is not yet guaranteed.

- [ ] **Step 2: Correct top-level project status**

In `README.md` and `CLAUDE.md`, remove wording that describes sandboxing as wholly planned. State the actually supported restricted/container/passthrough backends and link to `docs/04-tooling-and-shell-safety.md`. Do not claim restricted mode provides filesystem or network isolation.

- [ ] **Step 3: Mark only this batch's audit findings resolved**

In the audit document:

- mark release blocker 1 (background sandbox bypass) resolved by this batch;
- mark release blocker 10 (active-turn quit/shutdown ordering) resolved;
- mark background-job findings about unbounded buffers, completed-job slots, indefinite waits, lost managers on reload, and missing audit metadata resolved;
- mark runtime settings reload during active work resolved;
- leave container resource-limit accuracy, ACP shutdown, restricted filesystem scope, and every non-goal open;
- add the implementation commit range and verification commands to a dated resolution note.

- [ ] **Step 4: Run formatting and focused tests**

Run:

```bash
gofmt -w \
  internal/agent/runner_test.go \
  internal/tools/native/output.go internal/tools/native/output_test.go \
  internal/tools/native/native.go internal/tools/native/runner.go internal/tools/native/runner_test.go \
  internal/tools/native/jobs_manager.go internal/tools/native/jobs_test.go \
  internal/tools/registry/types.go internal/tools/registry/types_test.go \
  internal/sandbox/execute.go internal/sandbox/execute_test.go \
  internal/sandbox/process_unix.go internal/sandbox/process_windows.go \
  internal/sandbox/passthrough.go internal/sandbox/restricted.go internal/sandbox/container.go \
  internal/sandbox/shell_unix.go internal/sandbox/shell_windows.go \
  internal/sandbox/restricted_unix.go internal/sandbox/restricted_windows.go \
  internal/sandbox/sandbox_test.go internal/sandbox/restricted_test.go internal/sandbox/container_test.go \
  internal/app/session/session.go internal/app/session/session_test.go \
  internal/app/app.go internal/app/app_test.go internal/app/live_agent_test.go \
  internal/app/runtime.go internal/app/runtime_test.go \
  internal/app/tui/model.go internal/app/tui/model_test.go \
  internal/app/tui/settings/model.go internal/app/tui/settings/model_test.go
go test -count=1 ./internal/tools/native ./internal/sandbox ./internal/app ./internal/app/tui
go test -race -count=1 ./internal/tools/native ./internal/sandbox ./internal/app/session ./internal/app/tui
```

Expected: PASS.

- [ ] **Step 5: Run full repository gates**

Run:

```bash
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

Expected: all commands exit 0.

- [ ] **Step 6: Scan acceptance criteria mechanically**

Run:

```bash
rg -n 'NewJobManager\(execRunner|func \(execRunner\) Start|type ProcessRunner|type runningCmd' internal
rg -n 'bytes\.Buffer' internal/sandbox internal/tools/native/runner.go
rg -n 'cmd\.Run\(' internal/sandbox
rg -n 'sandboxing.*planned|planned.*sandbox' README.md CLAUDE.md docs
git status --short
```

Expected: the first three searches return no production matches; documentation has no stale planned-only claim; status shows only intended batch files.

- [ ] **Step 7: Commit documentation**

```bash
git add docs/04-tooling-and-shell-safety.md README.md CLAUDE.md docs/13-project-audit-2026-07-11.md
git commit -m "docs: describe execution and shutdown safety guarantees"
```

---

## Completion Checklist

- [ ] Background and foreground commands demonstrably use the same selected sandbox.
- [ ] Result and live-output retention are bounded per stream, with truncation in UI/tool output and audit metadata.
- [ ] Completed jobs do not consume concurrency slots.
- [ ] Kill, timeout, manager shutdown, and runtime shutdown join backend completion within documented bounds.
- [ ] Runtime quiesces active turns/jobs before knowledge finalization and closes persistence afterward.
- [ ] Ctrl+C, `/quit`, and `/exit` share safe cancellation and never report intentional cancellation as a provider failure.
- [ ] Settings can be inspected during work but cannot save/reload until idle.
- [ ] Container documentation makes only the client-process cancellation guarantee.
- [ ] Targeted race tests, full tests, vet, and CGO build pass.
- [ ] No audit finding outside the approved batch is marked resolved.
