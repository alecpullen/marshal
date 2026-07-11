# Project Audit — 2026-07-11

## Scope

This audit tracks findings identified during the execution and shutdown safety
review (July 2026). Each finding is either left open or marked resolved with a
reference to the implementation batch that addressed it.

## Release blockers

### RB1 — Background sandbox bypass (RESOLVED)

**Finding:** Background jobs (`shell.run background=true`) bypass the sandbox
backend. They are started by a `JobManager` that owns no `CommandRunner` and
executes `exec.Command` directly, meaning restricted/container isolation,
output limits, and process-group kill logic apply only to foreground commands.

**Resolution:** `JobManager` now receives the configured `CommandRunner` at
construction time. Both foreground and background commands use the same
sandbox backend, output limit (`max_output_bytes`), `BoundedOutput` writers,
and `OnStart` PID capture. See implementation batch below.

### RB2 — (Open) Container resource-limit accuracy

**Finding:** The container backend applies `--memory` and a `timeout`-wrapped
entrypoint, but `--cpus` is intentionally **not** used (the design prefers
wall-clock `timeout` over CPU-core quota). The `MemoryLimitBytes` field may
report a value that Darwin/ulimit cannot enforce in restricted mode. This is
documented honest capability reporting — no actionable fix planned outside a
broader resource-control framework.

### RB3 — (Open) ACP shutdown reliability

**Finding:** The ACP (Alternative Control Protocol) transport does not yet
exist. Shutdown ordering for ACP-backed sessions is out of scope until that
transport is implemented.

### RB10 — Active-turn quit/shutdown ordering (RESOLVED)

**Finding:** Ctrl+C, `/quit`, and `/exit` can tear down persistence (database,
logger) before the in-flight agent turn and background jobs have completed.
When the agent turn notices the closed database it reports a spurious provider
error, confusing the user and polluting the audit trail.

**Resolution:** `Runtime.Close` now calls `Quiesce` first — which cancels the
in-flight turn, resolves pending state, shuts down all background jobs, and
waits for completion — before closing MCP, brokers, snapshots, the database,
and the log file. The TUI's `beginShutdown` path cancels the agent context
early and returns `tea.Quit`; the runtime's `Close` then drains cleanly.
See implementation batch below.

## Background-job findings

### BJ1 — Unbounded output buffers (RESOLVED)

**Finding:** `job.stdout` / `job.stderr` use unbounded `bytes.Buffer`, so a
long-running background job could exhaust memory.

**Resolution:** Buffers are now bounded by `max_output_bytes`. Each background
job wraps a `BoundedOutput` (the same writer used by foreground commands)
inside a `safeBuffer` for concurrent read access. The job's `OutputTruncated`
flag propagates through `SandboxMeta` and is surfaced via the `[output
truncated]` marker in `job.output`.

### BJ2 — Completed jobs consume concurrency slots (RESOLVED)

**Finding:** `JobManager.Start` iterates all jobs (including completed ones)
when counting against `maxJobs`, so terminal jobs hold slots indefinitely.

**Resolution:** `Start` and `List` now count only running jobs against the
concurrency limit. Completed jobs are evicted after the configured retention
period (`BackgroundRetention`, default 8 hours).

### BJ3 — Indefinite wait on kill/shutdown (RESOLVED)

**Finding:** `Kill` and `Shutdown` block indefinitely on job completion.
If a job ignores SIGKILL (e.g., stuck in D-state), the shutdown never
finishes.

**Resolution:** `Shutdown` respects the caller's context deadline and returns
`ctx.Err()` when the deadline is exceeded. `Kill` joins the runner goroutine
via the `done` channel. The `Runtime.Quiesce` path applies a 5-second outer
deadline for the `JobManager.Shutdown` call.

### BJ4 — Lost job manager on runtime reload (RESOLVED)

**Finding:** When `reloadAgentRuntime` creates a new `JobManager`, the old one
is not shut down, so its goroutines and context are leaked.

**Resolution:** The old `JobManager` is shut down (with a 2-second context
deadline) before the new one replaces it. The pointer swap is guarded by
`rt.mu` so `Quiesce` always snapshots a consistent pointer.

### BJ5 — Missing audit metadata (RESOLVED)

**Finding:** Background job results do not record sandbox metadata (`Backend`,
`KilledReason`, `OutputTruncated`, etc.) in the audit trail.

**Resolution:** `JobInfo` now carries `Sandbox registry.SandboxMeta` and
`OutputTruncated bool`. The `job.output` tool returns these fields. Audit
metadata flows through the same `CommandResult.Meta` path that foreground
commands use.

## Runtime findings

### RT1 — Settings reload during active work (RESOLVED)

**Finding:** The TUI settings panel allows saving config changes while an
agent turn or background job is in flight, which can race against the
`reloadAgentRuntime` path.

**Resolution:** The settings model exposes `SetSaveBlocked(reason string)`.
The TUI calls this on every update cycle, blocking save/reload when an agent
turn is running, background jobs are active, a tool is pending approval, or a
question is pending. Tool output truncation is also blocked while settings
are open. The block reason is displayed in the settings footer.

## Non-goals (all open)

The following items are out of scope for this batch and remain open:

- Record-level confidentiality or encryption at rest.
- Streaming encryption for provider connections.
- Interposition (MITM-style audit proxy).
- Filesystem sandboxing in `restricted` mode beyond cwd confinement and env
  scrubbing (requires mount namespace → container backend).
- Resource-accounting fairness between concurrent background jobs.
- Container image signing or supply-chain verification.
- Background-job pause/resume or checkpoint/restore.
- Multi-tenant isolation.
- FIPS / government-grade compliance.

## Implementation batch

The findings above marked **RESOLVED** were addressed by the following commits
on branch `feature/execution-shutdown-safety`:

```
0dd889e test(agent): make parallel read regression race safe
cc47d8f feat(native): bound observed command output
05fe7b3 feat(sandbox): stream bounded output and join cancellation
5984cda fix(sandbox): tighten OnStart test and return SIGKILL error on non-ESRCH failure
76a667e feat(native): run background jobs through command runner
d8710ce feat(session): track and resolve active work
690b3a2 fix(app): make runtime own sandboxed background jobs
9a20022 fix(app): propagate reload cleanup errors, accumulate Close shutdown error, tighten output limit test
32f75aa fix(app): quiesce work before runtime cleanup
ea00717 test(app): tighten runtime close ordering and error-join tests
2434839 fix(tui): cancel safely and guard live settings reload
dc2ff27 fix(tui): deduplicate runtime message handlers and remove dead SetSaveBlocked
ca86a23 style: gofmt -w .
1ee0fab docs: add network-policy / BackgroundRetention config docs
```

### Verification commands

```bash
# Focused tests (step 4)
gofmt -w internal/agent/runner_test.go internal/tools/native/output.go ...
go test -count=1 ./internal/tools/native ./internal/sandbox ./internal/app ./internal/app/tui
go test -race -count=1 ./internal/tools/native ./internal/sandbox ./internal/app/session ./internal/app/tui

# Full repository gates (step 5)
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal

# Acceptance criteria scan (step 6)
rg -n 'NewJobManager\(execRunner|func \(execRunner\) Start|type ProcessRunner|type runningCmd' internal
rg -n 'bytes\.Buffer' internal/sandbox internal/tools/native/runner.go
rg -n 'cmd\.Run\(' internal/sandbox
rg -n 'sandboxing.*planned|planned.*sandbox' README.md CLAUDE.md docs
git status --short
```

## Dated resolution note

Resolved findings were closed on 2026-07-12 as part of Task 9 of the execution
and shutdown safety plan. The implementation commit range spans
`0dd889e..1ee0fab` on branch `feature/execution-shutdown-safety`.
