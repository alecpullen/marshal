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

### RB3 — (RESOLVED) ACP shutdown reliability

**Finding:** The ACP (Alternative Control Protocol) transport now exists
(`marshal acp` subcommand, `internal/acp/`) but shutdown ordering for
ACP-backed sessions was incomplete. EOF did not close session-owned resources,
replacing an active ACP runtime leaked the previous runtime, permission
responses and cancellation were blocked during an active prompt, and
`session/load` attempted to insert an existing session row instead of loading
persisted state.

**Resolution:** See ACP findings below.

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

## ACP findings

### ACP1 — Synchronous ACP permission/cancel deadlock (RESOLVED)

**Finding:** The ACP server executed request handlers synchronously on its only
input loop, so a running `session/prompt` prevented the same loop from reading
permission responses or `session/cancel`. This caused a deadlock: the prompt
handler could not complete without a permission response, and the permission
response could not be read until the prompt handler returned.

**Resolution:** The server input loop now routes frames to handlers and waiters
concurrently. Inbound requests are dispatched in tracked goroutines. Outbound
requests (permission requests) register a waiter before writing; the read loop
delivers the matched response to the waiter. The transport reader remains
available at all times. See implementation batch below.

### ACP2 — session/load duplicate insert / empty load (RESOLVED)

**Finding:** `session/load` used `UpsertProject` then `CreateSession` to load an
existing session, which hit a UNIQUE constraint on the session primary key.
When the upsert path was avoided, the session was created without hydrating
message history, returning an empty load.

**Resolution:** `session/load` now performs no project or session row mutation.
It validates that the project and session exist, reads the active conversation
branch from the database, and replays it through standard `session/update`
notifications before returning `result: null`. Missing projects or sessions
return errors without side effects.

### ACP3 — Replacing an active ACP runtime without closing it (RESOLVED)

**Finding:** When `session/new` was called for a session that already had a
loaded runtime, or when `session/load` replaced an existing runtime, the old
runtime was overwritten without calling `Close`. This leaked goroutines, open
connections, and database handles.

**Resolution:** The session manager now closes the previous runtime (via the
Batch 1 lifecycle — `Quiesce` then `Close`) before replacing it with a new
one. The pointer swap is guarded by a mutex. A session must be explicitly
closed before it can be replaced.

### ACP4 — No session-manager shutdown on EOF (RESOLVED)

**Finding:** When the ACP transport reached EOF, `Server.Serve` returned but
the session manager was not notified, so all loaded runtimes remained open
indefinitely. There was no lifecycle integration between transport EOF and
runtime cleanup.

**Resolution:** `acp.Run` now owns both the server and session manager. When
`Serve` returns (EOF, scanner error, context cancellation), it cancels all
handler contexts, fails all outbound waiters, shuts down the session manager
(which closes every loaded runtime), and waits up to five seconds for
completion. No runtime survives transport shutdown.

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

## Implementation batch — execution/shutdown safety

The findings above marked **RESOLVED** in the execution/shutdown safety sections
were addressed by the following commits on branch
`feature/execution-shutdown-safety`:

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

## Implementation batch — ACP reliability v1 lifecycle

The ACP findings above were addressed by the following commits on branch
`feature/acp-reliability-v1-lifecycle`:

```
2b6e5c5 feat(acp): add strict v1 wire primitives
b767b38 fix(acp): keep transport reader live during handlers
3046d87 fix(acp): address Task 2 review findings
f708159 feat(app): load existing runtimes safely
0742670 fix(app): address Task 3 review findings
42c957c fix(acp): serialize and cancel turns per session
4504408 fix(acp): address Task 4 review findings
51bd385 feat(acp): load replay and close owned sessions
763b458 fix(acp): address Task 5 review findings
28b9683 fix(acp): advertise and clean up session lifecycle
82f76c1 fix(acp): address Task 6 review finding
fb2f8c4 test(acp): cover concurrent lifecycle wire flows
f423a51 test(acp): address Task 7 review findings
```

### Newly verified protocol corrections

During this batch the following ACP v1 wire-format issues were verified and
corrected:

- **Content arrays**: prompt input uses `[]ContentBlock` (text + resource_link)
  instead of a plain string. All other block types and invalid content are
  rejected with `-32602`.
- **Standard updates**: turn/replay output uses the standard `session/update`
  method with `user_message_chunk`, `agent_message_chunk`, and
  `agent_thought_chunk` update types. No Marshal-specific notification methods
  (`message_added`, `thinking_changed`, etc.) are emitted.
- **Truthful capabilities**: `initialize` advertises exactly the implemented
  optional lifecycle capabilities (`loadSession: true`,
  `sessionCapabilities: { close: {} }`). Image, audio, embedded-resource,
  resume, list, delete, and HTTP/SSE-MCP capabilities are omitted.
- **Session close**: `session/close` is a stable method that removes the
  runtime, cancels the active turn, joins the runner, and closes owned
  resources. Unknown sessions return `-32000`.

These corrections ensure ACP v1 wire compliance for the implemented surface.

## Dated resolution note

Resolved findings for the execution and shutdown safety batch were closed on
2026-07-12 as part of Task 9 of the execution and shutdown safety plan. The
implementation commit range spans `0dd889e..1ee0fab` on branch
`feature/execution-shutdown-safety`.

Resolved findings for the ACP reliability v1 lifecycle batch were closed on
2026-07-12 as part of Task 8 of the ACP reliability v1 lifecycle plan. The
implementation commit range spans `2b6e5c5..f423a51` on branch
`feature/acp-reliability-v1-lifecycle`.
