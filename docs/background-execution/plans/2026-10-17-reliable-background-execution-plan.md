# Reliable background execution — implementation plan

Status: implemented. All 12 tasks are complete on branch `reliable-background-execution`; see "Execution record" below.
Date: 2026-10-17.
Base: `main` at `16d0c04a5a357fae9250765c3d51beda5c07a3cd`.
Design: [Reliable background execution](../feature-spec.md).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md), [work-decomposition](../../../internal/skills/builtin/work-decomposition.md), then [marshal-executing-plans](../../../internal/skills/builtin/marshal-executing-plans.md) after approval.

## Goal and boundaries

Ordinary commands survive bounded waits and configuration reloads. Explicit stops have precise targets. Ending the active conversation stops its work after an interactive warning and settles execution evidence before the optional exit postmortem.

Non-goals: concurrent TUI conversations; processes surviving Marshal; process restart/recovery; Send now; task-card redesign; custom progress widgets; watcher UI/lifecycle redesign; general latency or keychain work; automatic backgrounding of `shell.sudo`; whole-process workspace-write serialization.

## Assumptions and execution rules

- The current spec and the user's request to turn it into a plan are the design basis. The spec's proposed **10-second configurable foreground wait** is the planning baseline, subject to plan review.
- Reuse `JobManager`, job-exit storage, transcript projection and the dock host. Do not create a second process supervisor.
- Keep one active top-level TUI conversation. New/clear/resume replace it. Existing ACP support for separate sessions is unchanged.
- Preserve approvals, guardrails, pre-write snapshots, sandbox/environment rules and the stable prompt prefix. `shell.sudo` retains password-on-stdin/no-PTY handling.
- This plan covers ordinary `shell.run`/`test.run` and those tools in pipeline registries. `pipeline.Verifier` and declarative SDD run/assert operations use a separate command interface with explicit gate/operation budgets; redesigning those contracts is outside this background-tool feature. Do not silently change every internal command runner's timeout while removing ordinary shell execution deadlines.
- Retain `tools.shell.default_timeout_seconds` as deprecated load/save compatibility data. It currently does not drive ordinary shell execution. It must not override the new wait default or become a hidden deadline.
- TUI code renders and submits choices through structural runtime interfaces; it never imports `internal/agent`. `web/` remains standard-library-only.
- Tasks are sequential and include their own regressions. Verify and commit one task before the next: `reliable-background-execution: task N — <title>`. Most tasks share files, so parallel implementation is inappropriate.
- Paths are repository-relative. Existing symbols below were checked at the base commit; recheck them before execution. Explicitly **new** APIs/files are deliverables, not existing anchors.
- Use runner barriers and short injected wait budgets, plus a few real local process tests. No real download, SSH target, external provider or keychain is required by the new tests.
- Preserve unrelated untracked `docs/fleet-cli/` and `internal/app/tui/config.toml`.

## Shared implementation contracts

1. The service owns every ordinary command from admission through settlement. Execution state and foreground/background attachment are separate. Wait timers affect attachment only.
2. Capture runner/backend, cwd, output policy, conversation/actor/turn/step/tool-call identities and config generation before starting the goroutine. Publish PID from `OnStart`.
3. Explicit turn stop first marks/stops attached executions at the service's admission/detachment synchronization boundary, then cancels agent waits/provider calls. Ordinary waiter cancellation does not request process termination. A stopped turn cannot admit more commands. Use a new `State.RequestTurnStop(cause string)` entry point with a registered callback; service acceptance precedes agent cancellation. Allocate its operation token at the parent entry point, before dispatch. Synchronous review/pipeline workers inherit that token; asynchronous `agent.run` starts a separate token. Keep actor/turn provenance alongside this stop-group identity.
4. A foreground terminal result has one normal tool audit. A detached terminal result has one `JobExit`. Completion before the continuation audit must still pair correctly.
5. Extend existing JSON-backed job-exit records. Use a dedicated command-execution kind in persisted `ToolNotice.Data` for structured tool launch/continuation metadata, with typed encode/decode helpers. Empty text is metadata, not coaching. No new SQLite table is needed.
6. New execution identity includes a runtime generation, owning conversation and origin call; display job IDs alone are insufficient across resumes. Pair structured records by identity, not timestamp order. Keep legacy summary/timestamp fallback.
7. Runtime end acceptance checks the displayed opaque work IDs and freezes ordinary admission atomically. If any currently active ID was not displayed, refresh and stop nothing. Finished IDs need no reconfirmation.
8. Exit phases are `open → stopping work → postmortem → closing → closed`. Replacement omits postmortem. Only scoped exit-analysis work may launch during the postmortem phase.

New service API names for the sequential tasks: `Launch(ctx, spec)`, `WaitForeground(ctx, executionID, waitBudget)`, `StopAttached(turnID, cause)` and `RequestStop(executionID, cause)`. `LaunchSpec` contains the captured command/runner/cwd/output/origin/generation and initial attachment. `ForegroundResult` distinguishes a terminal command result from continuation metadata; neither API derives an execution deadline from its caller. Existing `Wait` remains a non-destructive waiter. Runtime end operations are `ActiveWork()`, `AcceptEnd(intent, displayedIDs)`, `WaitForEnd(ctx)` and `RunExitAnalysis(ctx, goal)`. These are new contracts to implement in the anchored files, with concrete result/error types defined alongside their owning service.

## Task 1: Expose the foreground wait setting

**Goal** — Config and settings expose a positive foreground wait without reviving execution deadlines.

**Files**

- `internal/app/config/types.go`
- `internal/app/config/file_types.go`
- `internal/app/config/defaults.go`
- `internal/app/config/merge.go`
- `internal/app/config/save.go`
- `internal/app/config/config_test.go`
- `internal/app/config/save_test.go`
- `internal/app/tui/settings/frames_shell.go`
- `internal/app/tui/settings/registry_test.go`
- `docs/background-execution/feature-spec.md`

**Steps**

1. Insert these mechanical additions immediately after the corresponding existing timeout field/entry:

   | Anchor | Insert verbatim |
   | --- | --- |
   | `ShellToolConfig` in `types.go` | ``ForegroundWaitSeconds int `toml:"foreground_wait_seconds"` `` |
   | `fileShell` in `file_types.go` | ``ForegroundWaitSeconds *int `toml:"foreground_wait_seconds"` `` |
   | `Shell: ShellToolConfig{` in `defaults.go` | `ForegroundWaitSeconds: 10,` |
   | `set(&cfg.Tools.Shell.DefaultTimeoutSeconds, s.DefaultTimeoutSeconds)` in `merge.go` | `set(&cfg.Tools.Shell.ForegroundWaitSeconds, s.ForegroundWaitSeconds)` |
   | timeout `putKey` call in `save.go` | `putKey(&shell.ForegroundWaitSeconds, fileField[int](file.Tools.Shell, "ForegroundWaitSeconds"), cfg.Tools.Shell.ForegroundWaitSeconds, def.Tools.Shell.ForegroundWaitSeconds)` |

2. Immediately after the new merge assignment, add:

   ```go
   if cfg.Tools.Shell.ForegroundWaitSeconds <= 0 {
       return fmt.Errorf("tools.shell.foreground_wait_seconds must be positive")
   }
   ```

3. Replace the timeout field closure inside `shellFrame` with:

   ```go
   func() *field {
       f := intField("shell.foreground_wait", "Foreground wait (s)",
           func() int { return s.cfg.Tools.Shell.ForegroundWaitSeconds }, 1,
           func(v int) { s.cfg.Tools.Shell.ForegroundWaitSeconds = v })
       f.TomlPath = "tools.shell.foreground_wait_seconds"
       f.Desc = "seconds to wait before a command continues in the background"
       return f
   }(),
   ```

4. In that frame, replace label `Max background jobs` with `Max running commands` and description `max concurrent background shell processes` with `max concurrent foreground and background shell processes`. Keep the TOML key. Document the old timeout field as deprecated, retaining load/save compatibility.
5. Add existing-file tests for default 10, user/project precedence, positive override, zero/negative rejection, layer-aware save round trips, legacy-only config keeping default 10 and the new settings path/minimum. Replace `shell.timeout`/`tools.shell.default_timeout_seconds` expectations in the settings registry's alias/apply/describe tests with `shell.foreground_wait`/`tools.shell.foreground_wait_seconds`; config load/save tests retain legacy-field coverage. Document compatibility and broadened capacity in the spec.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/config ./internal/app/tui/settings
```

Expected: the new value loads/saves correctly; invalid values fail before runtime construction; existing config/settings behavior passes.

## Task 2: Keep bounded tails receiving output after truncation

**Goal** — Managed commands retain recent stdout/stderr and keep forwarding useful output after the capture cap.

**Files**

- `internal/tools/native/output.go`
- `internal/tools/native/output_test.go`
- `internal/tools/native/native.go`
- `internal/tools/native/runner.go`
- `internal/tools/native/runner_test.go`
- `internal/sandbox/execute.go`
- `internal/sandbox/execute_test.go`

**Steps**

1. At `BoundedOutput`, `NewBoundedOutput` and `Write`, introduce a tail mode/constructor that retains the most recent bounded bytes while forwarding every incoming chunk to its observer. Preserve the existing prefix constructor for unrelated callers. Synchronize reads/writes, handle oversized individual writes without unbounded retained allocation and avoid invalid UTF-8 boundaries in returned text.
2. Add adjacent to `CommandRequest.MaxOutputBytes`:

   ```go
   // RetainOutputTail keeps recent output and forwards all observer bytes.
   // The default preserves prefix capture for existing non-managed callers.
   RetainOutputTail bool
   ```

3. At native `runCmd` and sandbox `executeCommand`, select tail mode when requested. The inner runner currently stops forwarding at its prefix cap; changing only the manager's outer buffer would still lose late bytes. Preserve timeout support for unrelated explicit internal callers and the privileged path; ordinary managed commands will pass zero execution timeout.
4. Extend output/runner tests with cap-plus-late-marker data, several writes, a single oversized write, both streams, observers after truncation, concurrent reads and prefix-mode compatibility. One short real local command proves the final markers arrive.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/tools/native ./internal/sandbox
CGO_ENABLED=1 go test -race ./internal/tools/native -run 'Output|Streams'
```

Expected: tails remain recent and bounded; observers keep receiving bytes; no capture races or unrelated prefix regressions.

## Task 3: Separate managed execution lifetime from attachment

**Goal** — JobManager supervises attached and detached commands without wall-clock execution deadlines.

**Files**

- `internal/tools/native/jobs_manager.go`
- `internal/tools/native/jobs_test.go`
- `internal/tools/native/jobs_wait_test.go`

**Steps**

1. Refactor `JobManager`, `job`, `Start` and `runJob` around the shared contracts. Add **new** managed-launch, foreground-wait and stop-attached APIs. Launch takes an immutable execution snapshot and initial attachment. Derive process contexts from the conversation service, never from the caller's wait budget. Foreground waiting starts at admission after approval and includes process startup time.
2. As a compiling transition, keep `Start(ctx, command, timeout)` as a background wrapper whose deprecated duration is ignored; remove its `context.WithTimeout` behavior now. Task 7 removes this wrapper after migrating callers. Replace `TestJobTimeoutIsDistinctFromKill` with proof that a former duration cannot kill execution. Historical `StatusTimedOut` remains readable.
3. Resolve cwd/runner before goroutine launch, submit `RetainOutputTail: true` and no execution timeout, and replace unbounded `safeBuffer` accumulation with bounded tails. Publish PID immediately inside `OnStart`.
4. Reserve capacity for all admitted commands. Track managed active count separately from detached-job count; stopping processes retain slots until settled. Promotion consumes no new slot. `JobEvent.Count` and awaitable outstanding jobs represent detached running/stopping work; authoritative execution snapshots include both attachments. Publish state/PID/attachment changes even when counts stay equal. Retention removes only terminal jobs; lowering capacity never evicts active ones.
5. Document one lock order and arbitrate completion, wait expiry, explicit target stop and turn stop at one authoritative boundary. Return either a terminal foreground result or a continuation handle. Stop accepted while attached targets the command; detach accepted first survives turn stop. Plain wait cancellation/deadline ends waiting or detaches without canceling the process. Reject stopped-turn admission.
6. Preserve actual nonzero exit codes and distinguish startup failure, explicit stop, reported resource limit and natural completion. Record stop request/cause/time separately from final outcome. Assert a resource-limit cause only when backend evidence supports it; otherwise retain the observed exit and configured resource constraints without guessing. A late stop must not relabel a natural completion. Use stopping state until termination settles. Process-group grace escalation remains permitted after explicit stop.
7. Use `fakeCommandRunner` barriers to test no execution deadline, live PID, same identity/PID through handoff, capacity/promotion, canceled admission, nonzero exits, repeat stops, both stop/detach orderings and completion races. Existing `Wait` tests retain non-destructive cancellation. Keep callbacks outside manager locks and test callback snapshot reentry.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/tools/native
CGO_ENABLED=1 go test -race ./internal/tools/native -run 'Job'
```

Expected: no timer-driven termination; every race produces one consistent outcome; output/capacity stay bounded.

## Task 4: Persist and project one completion per execution

**Goal** — Structured identity and durable final evidence settle existing cards accurately without duplicate completions.

**Files**

- `internal/tools/registry/types.go`
- `internal/tools/registry/audit.go`
- `internal/db/audits_test.go`
- `internal/db/jobexits.go`
- `internal/app/session/jobexit.go`
- `internal/app/session/jobexit_test.go`
- `internal/app/session/session.go`
- `internal/tools/native/jobs_manager.go`
- `internal/tools/native/jobs_test.go`
- `internal/app/app.go`
- `internal/viewmodel/cards.go`
- `internal/viewmodel/cards_test.go`
- `internal/viewmodel/wire.go`
- `internal/viewmodel/wire_test.go`
- `internal/app/tui/jobs.go`
- `internal/app/tui/jobs_test.go`
- `internal/app/tui/steps.go`
- `internal/app/tui/cards_render_test.go`

**Steps**

1. At `ToolNotice` and `cloneToolNotice`, add typed execution encode/decode helpers using the existing JSON channel. Add this entry to the notice constant block:

   ```go
   NoticeCommandExecution = "command_execution"
   ```

   Store a versioned payload under `Data["execution"]`: execution/display IDs; conversation/actor/turn/step/tool-call identities; cwd/config generation; PID; start/end; execution/attachment state; effective wait; detach reason/time; stop reason/time; optional actual exit code; truncation. Decode persisted JSON number/map forms as well as in-memory values. Deep-copy nested metadata when auditing. Empty notice text creates no coaching footer.
2. Extend `JobExitRecord` and session `JobExit` additively with this metadata, including a nullable actual exit code. Preserve old integer fields/unknown conventions for legacy compatibility, without interpreting unknown as success. Continue using `session_state` JSON; no SQL migration is needed.
3. Refactor `AddJobExit` to save detached completion before publishing its complete event. Serialize append/snapshot/save ordering independently of the broad state mutex, preventing concurrent saves from overwriting one another. Give the manager a settlement acknowledgment/error path. Surface failed persistence and retain it for retry; do not announce durable completion on failure. Keep the existing 16 KiB final-tail cap.
4. At `runJob` and app `SetOnExit` wiring in `buildAgentRunnerWithLock`, send only detached results to the exit sink; foreground results use normal tool audits. Keep LSP rescan behavior. Settle once even if execution finishes during handoff or before its continuation audit; do not deadlock completion/report callbacks with service/state locks.
5. At `JobStartedID`, `lostJobs`, viewmodel `JobStartID`, `indexCards` and `settleJob`, prefer execution identity and origin call. Match structured starts/exits regardless of timestamp order, retaining legacy fallback. Unfinished restored starts are lost, never restarted. Foreground metadata alone must not create a background card.
6. Extend `JobState`/wire projection with status, attachment, stop cause, truncation and optional exit. Update node hashes for relevant state/output changes. At `runningJobs`, include detached running/stopping jobs only; an attached command already has its active tool row. Update `renderJobCard` in `steps.go` to distinguish stopping, lost, failed and completed outcomes and unknown exits. Terminal records leave active counts but remain queryable.
7. Extend existing audit/jobexit/cards/wire/jobs tests for JSON round trips, deep-copy isolation, concurrent saves, persistence failure, foreground versus detached completion, exit-before-start-audit, reused display IDs, legacy data and lost jobs.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/tools/registry ./internal/db ./internal/app/session ./internal/tools/native ./internal/viewmodel ./internal/app/tui
CGO_ENABLED=1 go test -race ./internal/app/session ./internal/tools/native -run 'Job'
```

Expected: exactly one completion per execution; durable ordering and legacy restoration hold even under adversarial event ordering.

## Task 5: Give explicit turn stops their own targets

**Goal** — Explicit turn stops mark attached commands before canceling the agent, while ordinary wait cancellation remains non-destructive.

**Files**

- `internal/agent/await_sources.go`
- `internal/agent/runner.go`
- `internal/agent/runner_copyfrom_test.go`
- `internal/agent/execute.go`
- `internal/agent/subagent.go`
- `internal/agent/interrupt_test.go`
- `internal/agent/runner_parallel_test.go`
- `internal/app/session/session.go`
- `internal/app/session/subagents.go`
- `internal/app/session/subagents_async_test.go`
- `internal/app/app.go`
- `internal/app/tui/model.go`
- `internal/app/tui/commands_dispatch.go`
- `internal/app/tui/sdd_authoring.go`
- `internal/app/tui/interrupt_test.go`

**Steps**

1. Beside `JobAwaitSource`, introduce the **new** narrow runner-facing execution-control interface, injected by app construction. At each parent turn/review/SDD/ACP entry point, register an operation stop token before dispatch; `RunTask` reuses it and adds actor/turn identity before title/provider work can block. Direct runner use allocates a token when none is present. Clear only the same registration on finish. Carry the dependency through `Runner.CopyFrom` and all runner factories. Task 11 wires the ACP entry point to the same contract.
2. At session work/turn state adjacent to `BeginWork`/`EndWork`, add stop registration that retains a stop requested before registration. Run callbacks outside state locks. Explicit stop asks the service to atomically stop/reject the turn's attached commands, then cancels its agent context. Ordinary cleanup cancels/unregisters without requesting execution stop.
3. At `executeToolCall`, stamp handler-context execution origin after approval/hook validation, using the assigned call/step IDs. On handler failure, audit the returned partial `ToolResult` instead of discarding it for an empty result; preserve actual output, exit and stop evidence in both the audit and model-facing error result. Keep guardrails, snapshots and write-gate ordering.
4. At TUI `cancelTurn`, invoke the structural/session stop operation before existing cancellation. Cover review, pipeline and SDD turn-entry variants. At `NewSubagentTool`/`CancelSubagent`, wrap targeted child cancellation with that child's explicit stop operation. Normal child completion does not invoke its explicit-stop wrapper. Parent stop preserves independent children and detached jobs.
5. At `executeNativeToolCalls` and `executeActions`, retain valid tool-call/result pairs after interruption: started calls receive settled outcomes; pre-admission cancellations receive not-started results. Do not discard the rest of a batch when one call is interrupted. Cover native/envelope paths and existing parallel dispatch; do not introduce general parallel shell execution.
6. Extend existing session/interrupt/parallel tests for early stop, callback replacement races, parent/child targeting, plain wait cancellation, normal finish, canceled approval and retained partial error evidence. Assert surviving background work is still alive.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/session ./internal/agent ./internal/app/tui
CGO_ENABLED=1 go test -race ./internal/agent ./internal/app/session -run 'Interrupt|Cancel|Parallel|Subagent'
```

Expected: explicit stops reach attached executions only; background work and valid history survive cancellation.

## Task 6: Route shell and test calls through managed execution

**Goal** — Ordinary foreground calls return the same background execution when their wait expires.

**Files**

- `internal/tools/native/command.go`
- `internal/tools/native/command_test.go`
- `internal/tools/native/jobs_test.go`
- `internal/tools/native/native.go`
- `internal/tools/native/schemas_test.go`
- `internal/agent/await_sources.go`
- `internal/agent/subagent_async.go`
- `internal/agent/subagent_async_test.go`
- `internal/agent/await_extend_test.go`
- `internal/agent/await_extend_race_test.go`
- `internal/app/awaitsources.go`

**Steps**

1. At `shellRunTool`/`runShellCommand`, replace separate direct-run/background paths with one managed launch. Snapshot `activeRoot`, runner/config generation/output policy and Task 5 origin. Validate guardrails before admission. Stream to the active-call writer only while attached; handoff switches display ownership without another launch or approval.
2. Replace ordinary shell/test use of `defaultShellTimeout`, `maxShellTimeout`, `defaultTestTimeout` and `clampTimeout` with wait resolution: negative rejects before launch, omitted/zero uses positive config default, positive is honored without the old cap. Reject unrepresentable duration overflow clearly before launch; never wrap or silently clamp. Retain helpers still used by unrelated tools.
3. Explicit background returns immediately with structured identity and no execution deadline. Accept nonnegative wait arguments without applying them to process lifetime. Foreground expiry returns normal continuation with handle, status, recent output and await guidance; it has no fake exit code/error. Natural completion returns actual exit/output. An explicit attached stop waits for the process's actual outcome.
4. Route `testRunTool`'s configured/override command through identical default wait/continuation behavior, retaining internal-tool/TesterView restrictions. Leave `shell.sudo` separate.
5. Describe wait-only `timeout_seconds`, continuation and explicit stop in schema/tool text. Update existing clamp/timeout tests to the new contract while retaining real startup-error/nonzero-exit coverage.
6. Extend `jobAwaitAdapter`, await job information and `jobResultText`/peek with status, optional exit and stop cause. Only detached jobs appear in await's outstanding-job scan; newly managed foreground commands must not become extra any/all targets. Keep await read-only, selector validation and pending-approval liveness. Timeout, cancel and any-loser cleanup cancel waiters only. Apply negative wait validation consistently, including peek.
7. Add real-manager regressions for individual job/child waits, mixed any/all, peek, canceled/expired waits and winners with unfinished losers. Shell tests prove one launch/PID/identity, no timer kill, tail handoff, immediate background, defaults/zero/negative, actual exits and test-command continuation. Inspect a resolved 900-second wait without actually waiting 900 seconds.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/tools/native ./internal/agent ./internal/app
CGO_ENABLED=1 go test -race ./internal/tools/native ./internal/agent -run 'Job|Shell|Await|Subagent'
```

Expected: waits hand off without restarting/killing; all await variants preserve unfinished targets; explicit stops still work.

## Task 7: Preserve service ownership across reloads and child registries

**Goal** — Every ordinary command registry uses its conversation's service, and reload changes future launches only.

**Files**

- `internal/app/app.go`
- `internal/app/runtime.go`
- `internal/app/app_test.go`
- `internal/app/runtime_test.go`
- `internal/tools/native/native.go`
- `internal/tools/native/native_test.go`
- `internal/tools/native/command_test.go`
- `internal/tools/native/activeroot_test.go`
- `internal/tools/native/file_edit_test.go`
- `internal/tools/native/system_access_test.go`
- `internal/tools/native/scope_test.go`
- `internal/tools/native/jobs_manager.go`
- `internal/tools/native/jobs_test.go`
- `internal/tools/native/jobs_wait_test.go`
- `internal/app/tui/model.go`
- `internal/app/tui/model_options.go`
- `internal/app/tui/model_test.go`
- `internal/watch/loop_test.go`

**Steps**

1. At startup/`buildAgentRunnerWithLock`, separate creating the owned service from building a tool generation. Reuse the service on rebuild. Install parent count/broker/exit sinks only at owner construction. Remove callback replacement and unowned `context.Background()` manager creation from `newToolSet`.
2. Read-only filtered registration may omit a service, but ordinary command dispatch without an injected owner must fail clearly before launch. Update native command test setup with explicit test-owned managers/cleanup; file-only tests need no process owner.
3. At `buildSubagentFactoryWithLock`, `makePipelineRegistryFactory` and `buildPlanAuthorFactory`, inject the conversation service or use the no-command path for filtered read-only scopes. Child output remains child-local; counts/final sink remain parent-owned. Pipeline launches capture per-dispatch worktree cwd instead of a parent-global resolver.
4. At `reloadAgentRuntime`, build candidates against the borrowed service without changing defaults/callbacks. Commit a new launch generation/defaults atomically only after validation; failed candidates neither mutate nor close the service. Remove old-manager shutdown on reload. Retain resources actually referenced by admitted runner/backends until their commands release them; inspect real closers rather than retaining unrelated generations wholesale. Completion rescans resolve a live LSP handle.
5. Future capacity/output/retention defaults can change; admitted runner/cwd/output and chosen wait stay fixed. Lowered capacity blocks future admission only. Preserve handles/IDs, reload serialization and the stable write lock.
6. At `settingsBlockReason`, `handleModelOptionsChanged` and `flushPendingModelOptions`, remove blanket detached-job blocking after service reuse is safe. Preserve top-level busy/pending-decision/dock restrictions.
7. Migrate remaining `Start(ctx, command, timeout)` calls/tests to the new launch API, including active-root and watch-source tests, then remove Task 3's compatibility wrapper. Update explicit owner injection in command, active-root, file-edit, system-access and scope fixtures. Change `TestReloadAgentRuntimeReplacesReachableManagerWhenIdle` to assert service preservation. Add running-job successful/failed reload tests, changed backend/cwd/output/capacity, old/new generation launches, child registration, pipeline cwd/output, completion sink and shutdown ownership. Use app injection seams and memory credentials.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/tools/native ./internal/app ./internal/app/tui ./internal/pipeline
CGO_ENABLED=1 go test ./internal/watch
CGO_ENABLED=1 go test -race ./internal/app -run 'Reload|JobManager|BackgroundShell|SubagentShell|PipelineRegistry'
```

Expected: handles and service identity survive reload; old commands retain captured settings; command registries have explicit owners.

## Task 8: Add authoritative conversation-end phases

**Goal** — Runtime can validate a displayed work set, stop/join it, then admit isolated exit analysis before closure.

**Files**

- `internal/app/runtime.go`
- `internal/app/runtime_test.go`
- `internal/app/app.go`
- `internal/app/app_test.go`
- `internal/app/session/session.go`
- `internal/app/session/session_test.go`
- `internal/app/session/subagents.go`
- `internal/app/session/subagents_async_test.go`
- `internal/agent/subagent.go`
- `internal/tools/native/jobs_manager.go`
- `internal/tools/native/jobs_test.go`

**Steps**

1. At runtime `BeginWork`, session `BeginWork`/`EnterSubagent` and service admission, introduce a shared end-controller gate with stable work IDs. Include parent operations, child creation/pipeline work and commands; exclude infrastructure. Registration and snapshot-check/freeze share a synchronization boundary, rather than combining independently sampled counts.
2. Add **new** runtime operations for snapshots, end acceptance against displayed IDs, joining stopped work and scoped exit analysis. Snapshot labels are bounded/scrubbed; an active parent awaiting provider/approval counts. Visually group attached commands under their parent without double-counting stoppable targets, but retain each command/child's distinct opaque ID in the displayed membership so new work cannot hide behind an existing parent ID.
3. Acceptance rejects expansion by refreshing without stopping anything. Otherwise freeze ordinary admission atomically, stop only still-active targets, resolve pending user decisions and join parent/children/commands/report goroutines plus durable finalization. Release locks before cancellation/join callbacks.
4. Split `Quiesce` into stopping current conversation work and terminal infrastructure closure. Do not permanently `State.Shutdown`, cancel the infrastructure root, close the service or consume terminal `quiesceOnce` before optional analysis. Disable watch auto-resume/ordinary admission during end phases without adding watcher prompts.
5. Exit analysis receives a scoped authorization token/context and separate work epoch accepted only during postmortem. Its commands/children inherit that token. Success/failure/cancellation always stops/joins that epoch before closure; it cannot reopen ordinary work. Keep needed provider/tool services and database open until then.
6. Make `Quiesce`/`Close` reuse stop/join idempotently for headless/signal shutdown without prompts. Existing deadlines bound joins, not process lifetime. Failed joins report outstanding work and permit retry instead of latching success; interactive extraction waits for settlement while the UI stays responsive. Preserve bounded headless error reporting and DB-close ordering. Surface finalization failures truthfully; retry pending writes before extraction.
7. Serialize `NewSession` against reload/end acceptance. Do not mutate state/owner/resume intent before acceptance and settlement. A failed replacement build leaves the old conversation frozen with an explicit retry-or-exit error; no implicit reopening or job transfer.
8. Extend runtime/session tests for subset acceptance, new-work races, work finishing during prompt, frozen admission, pending approval, nested children, durable output before DB close, analysis-only admission/failure cleanup, repeated close, join retry and replacement boundaries. Use barriers for race ordering.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/session ./internal/tools/native ./internal/agent ./internal/app
CGO_ENABLED=1 go test -race ./internal/app ./internal/app/session -run 'Runtime|Quiesce|Shutdown|NewSession|Subagent'
```

Expected: confirmed work cannot silently expand; settlement precedes extraction/DB close; only scoped exit analysis starts after freezing.

## Task 9: Add the active-work warning panel

**Goal** — A rendering-only dock panel defaults to keeping work and returns the exact displayed IDs on confirmation.

**Files**

- `internal/app/tui/activeworkpanel/panel.go` — new, complete contents below.
- `internal/app/tui/activeworkpanel/panel_test.go` — new, complete contents below.

**Steps**

1. Create `panel.go` verbatim. It follows existing `dock.Panel`/`postmortempanel.Panel` patterns. The runtime host owns labels, refreshing and all decisions.

   ```go
   package activeworkpanel

   import (
   	"fmt"
   	"strings"

   	tea "charm.land/bubbletea/v2"
   	"charm.land/lipgloss/v2"

   	"marshal/internal/app/tui/chrome"
   	"marshal/internal/app/tui/dock"
   	"marshal/internal/app/tui/theme"
   )

   type Item struct {
   	ID    string
   	Label string
   }

   type DoneMsg struct {
   	Stop bool
   	IDs  []string
   }

   type Panel struct {
   	replacing bool
   	items     []Item
   	cursor    int
   }

   var _ dock.Panel = (*Panel)(nil)

   func New(replacing bool, items []Item) *Panel {
   	p := &Panel{replacing: replacing}
   	p.Replace(items)
   	return p
   }

   func (p *Panel) Replace(items []Item) {
   	p.items = append([]Item(nil), items...)
   	p.cursor = 0
   }

   func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

   func (p *Panel) Update(msg tea.Msg) tea.Cmd {
   	key, ok := msg.(tea.KeyPressMsg)
   	if !ok {
   		return nil
   	}
   	switch key.String() {
   	case "esc", "n", "N":
   		return p.done(false)
   	case "up", "left", "k", "h":
   		p.cursor = 0
   	case "down", "right", "j", "l":
   		p.cursor = 1
   	case "enter":
   		return p.done(p.cursor == 1)
   	}
   	return nil
   }

   func (p *Panel) done(stop bool) tea.Cmd {
   	ids := make([]string, len(p.items))
   	for i, item := range p.items {
   		ids[i] = item.ID
   	}
   	result := DoneMsg{Stop: stop, IDs: ids}
   	return func() tea.Msg { return result }
   }

   func (p *Panel) View(width, maxHeight int) string {
   	if width < 4 || maxHeight < 4 {
   		return ""
   	}
   	keep, stop := "Keep Marshal open", "Stop work and continue exiting"
   	if p.replacing {
   		keep, stop = "Keep current conversation", "Stop work and replace conversation"
   	}
   	rows := []string{fmt.Sprintf("Active work (%d) will be stopped:", len(p.items))}
   	available := max(0, maxHeight-5)
   	shown := min(len(p.items), available)
   	if shown < len(p.items) && available > 0 {
   		shown = max(0, available-1)
   	}
   	line := lipgloss.NewStyle().MaxWidth(max(1, width-6))
   	for _, item := range p.items[:shown] {
   		rows = append(rows, line.Render("  "+item.Label))
   	}
   	if shown < len(p.items) && available > 0 {
   		rows = append(rows, fmt.Sprintf("  +%d more active items", len(p.items)-shown))
   	}
   	if maxHeight >= 5 {
   		rows = append(rows, "")
   	}
   	for i, label := range []string{keep, stop} {
   		marker := "  "
   		if i == p.cursor {
   			marker = "> "
   		}
   		rows = append(rows, line.Render(marker+label))
   	}
   	content := strings.Join(rows, "\n")
   	return chrome.PanelWithHints("Active work", "arrows choose · enter confirm · esc keep",
   		content, width-2, min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())
   }
   ```

2. Create `panel_test.go` verbatim. Task 10 adds host/resize/accessibility coverage in existing TUI tests.

   ```go
   package activeworkpanel

   import (
   	"strings"
   	"testing"

   	tea "charm.land/bubbletea/v2"
   )

   func TestDefaultAndEscapeKeepWork(t *testing.T) {
   	for _, code := range []rune{tea.KeyEnter, tea.KeyEscape} {
   		p := New(false, []Item{{ID: "job:1", Label: "download"}})
   		cmd := p.Update(tea.KeyPressMsg{Code: code})
   		if cmd == nil || cmd().(DoneMsg).Stop {
   			t.Fatalf("key %v did not keep work", code)
   		}
   	}
   }

   func TestConfirmationCapturesDisplayedIDs(t *testing.T) {
   	items := []Item{{ID: "job:1", Label: "download"}}
   	p := New(false, items)
   	items[0].ID = "mutated"
   	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
   	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
   	p.Replace([]Item{{ID: "job:2", Label: "test"}})
   	got := cmd().(DoneMsg)
   	if !got.Stop || len(got.IDs) != 1 || got.IDs[0] != "job:1" {
   		t.Fatalf("confirmation = %+v", got)
   	}
   	next := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})().(DoneMsg)
   	if next.Stop {
   		t.Fatal("refresh did not restore keep as default")
   	}
   }

   func TestSmallAndReplacementViews(t *testing.T) {
   	p := New(true, []Item{{ID: "job:1", Label: "download"}})
   	if p.View(0, 0) != "" {
   		t.Fatal("zero-size view must be empty")
   	}
   	for _, size := range [][2]int{{80, 9}, {120, 16}, {200, 24}} {
   		view := p.View(size[0], size[1])
   		if !strings.Contains(view, "Keep current conversation") || !strings.Contains(view, "download") {
   			t.Fatalf("missing warning content at %v", size)
   		}
   	}
   }
   ```

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/tui/activeworkpanel
```

Expected: default Enter/Esc keep work; explicit selection captures immutable displayed IDs; refresh restores the safe choice.

## Task 10: Route interactive ending through settlement and postmortem

**Goal** — Every interactive end path warns before stopping work and completes optional analysis without reopening the conversation.

**Files**

- `internal/app/tui/model.go`
- `internal/app/tui/commands_dispatch.go`
- `internal/app/tui/sdd_authoring.go`
- `internal/app/tui/model_test.go`
- `internal/app/tui/commands_dispatch_test.go`
- `internal/app/tui/keys_cancel_test.go`
- `internal/app/tui/frame_geometry_test.go`
- `internal/app/tui/frame_invariants_test.go`
- `internal/app/tui/postmortempanel/postmortempanel.go`
- `internal/app/tui/postmortempanel/postmortempanel_test.go`
- `internal/app/app.go`
- `internal/app/app_test.go`

**Steps**

1. Beside `SessionSwapper`/`WithSessionSwapper`, add the **new** structural end-controller interface/option and wire its runtime adapter in `app.Run`. It exposes Task 8 snapshots/acceptance, async stop/join and exit analysis through neutral data. Policy and process decisions stay outside the model.
2. Replace destruction in `beginShutdown`, `finishQuit`, `newSessionEffect` and `beginResume` with one pending intent: exit, new/clear arguments or resume ID. Preserve existing busy refusal for new/clear. Consult runtime work, not cached `m.jobs`/counts or only `m.busy`. Do not mutate resume ID/state or announce resumption before acceptance.
3. Open `activeworkpanel` independently of approval mode/postmortem settings. Repeated quit/Ctrl+C cannot stack/bypass it. Esc/default discards intent and preserves work. Restore a displaced pending approval/gate surface after decline so children cannot be stranded.
4. Consume live work events while prompting; runtime snapshots refresh changed displayed work. Late approval/gate events must not replace the warning. Submit `DoneMsg.IDs` to atomic acceptance; expanded work refreshes with keep selected. Completed work disappears without cancellation labels. No remaining work proceeds through atomic admission freeze without a needless second warning.
5. Stop/join asynchronously, showing stopping/remaining work while input remains responsive. Repeated requests are idempotent. Failed joins offer retry and never advance as if settled. Queue/watch auto-resume/settings mutations and ordinary prompt starts cannot reopen work after accepted ending.
6. Only after settlement apply `postmortem.on_exit`: never closes, always extracts/closes, prompt opens the existing panel with its empty-session behavior. Extraction sees final command/child evidence. Keep I/O off the rendering loop.
7. Replace ordinary `startAgentRun` elevation for `postmortemAgentPending` with scoped exit analysis from Task 8. Preserve explicit user choice for analysis; temporary edit/system access belongs only to that operation. Failure/refused start cleans up and closes, never returns to an elevated ordinary prompt. Analysis cannot recursively trigger the active-work warning; its detached work is joined during phase cleanup.
8. Accepted replacement goes from old-work settlement to existing new/resume behavior with no added postmortem or transferred handles. Show runtime errors accurately.
9. Extend exit/model/command tests across entry points, parent/child/job work, approval modes, all postmortem policies, default/Esc, repeated requests, new/completing work races, pending approval restoration, no mutation on decline, analysis success/failure and reload during prompting. Extend frame tests at 80×24, 120×40, 200×60 and narrow resize/NO_COLOR. Keep both choices visible and keyboard-accessible.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/tui/... ./internal/app
CGO_ENABLED=1 go test -race ./internal/app ./internal/app/tui -run 'Exit|Postmortem|Shutdown|Resume|NewSession|Cancel'
```

Expected: warning precedes destructive stops and postmortem everywhere; decline preserves work; analysis never reopens the conversation.

## Task 11: Preserve ACP semantics and expose execution evidence

**Goal** — ACP cancellation follows ownership rules and diagnostics explain wait, handoff and stop outcomes.

**Files**

- `internal/acp/turn.go`
- `internal/acp/turn_test.go`
- `internal/acp/session.go`
- `internal/acp/session_test.go`
- `internal/acp/stacknode.go`
- `internal/acp/stacknode_test.go`
- `internal/acp/stack_test.go`
- `internal/postmortem/postmortem.go`
- `internal/postmortem/postmortem_test.go`
- `internal/postmortem/write_test.go`
- `internal/viewmodel/wire.go`
- `internal/viewmodel/wire_test.go`
- `docs/background-execution/feature-spec.md`
- `docs/ARCHITECTURE.md`

**Steps**

1. At `TurnManager.runTurn`, distinguish normal slot cleanup from explicit `Cancel`/`CancelAndWait`. Explicit cancellation invokes actor turn-stop before canceling waits/provider calls. Cleanup does not stop detached jobs. Transport/request deadline ends waiting; explicit session closure/replacement stops the conversation.
2. At `SessionManager.closeRuntimeOnce`, `Close`, `CloseAll` and `replaceExisting`, use the noninteractive end controller. No TUI/approval prompt blocks headless shutdown. Preserve response codes, terminal subscriptions, per-session turn exclusivity and bounded join errors.
3. Extend existing wire/detail JSON additively for execution identity, state/attachment, optional exit, stop cause, wait/detach and truncation. At `CallDetail`/`NoticeDetail` expose typed execution evidence separately from coaching text. Reuse stack/detail transport; no web panel or internal web import is needed. Scrub commands/cwd/output through current redaction paths.
4. At `postmortem.Report`/`Build`, add execution evidence from foreground audits and detached final records: ownership, effective wait, handoff, stop request and observed outcome. Deduplicate by execution identity; continuation/await expiry is not a failed command. Preserve schema version for additive fields and existing failure evidence. Test extraction after settlement, including nonzero/resource-limited/startup/lost/legacy cases.
5. Keep feature spec and architecture current with implemented contracts: wait-only arguments, deprecated config, broadened capacity, conversation ownership and exit phases. Do not claim Send now, restart persistence or whole-process write coordination. Once this plan is approved, record the selected default in the spec instead of leaving it an unselected proposal.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/acp ./internal/viewmodel ./internal/postmortem
CGO_ENABLED=1 go test -race ./internal/acp -run 'Cancel|Close|Replace|ToolCall|Stack'
CGO_ENABLED=1 go test ./web/bridge -run '^TestWebIsStdlibOnly$'
```

Expected: headless stops never prompt; calls/results stay paired; JSON/evidence explain outcomes without prose parsing; the web boundary holds.

## Task 12: Verify complete lifecycle composition and integration

**Goal** — End-to-end regressions prove the user contract and the completed feature passes repository checks.

**Files**

- `internal/app/app_test.go`
- `internal/app/runtime_test.go`
- `internal/tools/native/jobs_test.go`
- `internal/sandbox/process_unix_test.go`
- `internal/acp/turn_test.go`
- `docs/background-execution/feature-spec.md`
- `docs/background-execution/plans/2026-10-17-reliable-background-execution-plan.md`

**Steps**

1. Compose existing app/runtime injection seams into regressions: handoff → repeated await expiry → reload → natural completion; stop before/after detach; targeted child stop with its detached job surviving; declined warning; accepted warning → durable terminal output → postmortem → DB close; replacement leaves no work behind. Use scripted local providers/fake runners and `credentials.NewMemStore` where credentials are required.
2. Add a small real-process case using existing runner/process infrastructure: initial marker, survival across a short foreground wait, late marker beyond the output cap, natural completion. Assert one launch/PID and recent tail. Separately stop a process tree explicitly and collect final output. Use barriers/temp files or the existing Unix-specific test file, not long sleeps.
3. Check the spec's complete acceptance list, especially admission/detach/stop races, partial batch results, failed reload rollback, durable event ordering, restored lost jobs and exit-analysis cleanup. Earlier tasks own their focused regressions; this task verifies composition.
4. Format only changed Go files, then run the checks below. Test packages already sanitize XDG; new app fixtures must inject config/provider/credential dependencies instead of consulting ambient user settings. Do not skip failures or expand into an unrelated keychain redesign. Record genuine pre-existing failures with evidence and resolve or obtain explicit acceptance before integration.
5. Update spec/plan completion evidence. Dispatch the repository-required reviewer subagent for this nontrivial completed code change before integration, address material findings and rerun affected checks. Stage exact task files, preserving unrelated work. Follow the user's branch/PR integration instructions; writing this plan does not authorize merge/push.

**Verify**

From the isolated feature checkout rooted at this plan's base, format/check the feature's changed Go files, then run repository checks. Confirm the base diff contains only the intended feature before formatting; do not include unrelated work.

```sh
git diff --name-only -z 16d0c04a -- '*.go' | xargs -0 gofmt -w
git diff --name-only -z 16d0c04a -- '*.go' | xargs -0 gofmt -l
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
git diff --check
CGO_ENABLED=1 go test -race ./internal/tools/native ./internal/app/session ./internal/app ./internal/agent ./internal/acp
```

Expected: the second formatting command prints nothing; full tests/vet/build pass with cgo; no lifecycle races remain; review confirms the contract.

## Integration notes

- After approval, execute one task at a time with the built-in execution skill. Revalidate anchors if the base moves.
- Tasks 3–8 share service/runtime contracts and files; keep them sequential. Task 9 is independently verifiable; Task 10 activates the warning.
- Keep the public spec/plan with implementation commits per AGENTS.md.
- Intentional compatibility changes: shell/test timeouts become wait budgets, long calls may return handles, old execution caps disappear, capacity includes attached commands, and active-work warnings are independent of postmortem settings.
- Timers can bound waiting/joining or escalate termination after an explicit stop. They cannot impose ordinary process lifetime limits.
- Manual check using isolated config/data and a harmless local long command: observe continuation, change settings, peek/await, decline exit, then accept exit and inspect final postmortem evidence. A large transfer is unnecessary.

## Execution record

Executed task-by-task on branch `reliable-background-execution`, from the
plan's base commit. One commit per task, plus one follow-up commit for the
branch review findings:

| Task | Commit |
| --- | --- |
| 1 — Expose the foreground wait setting | `29803824` |
| 2 — Keep bounded tails receiving output after truncation | `37ed149e` |
| 3 — Separate managed execution lifetime from attachment | `51fd9494` |
| 4 — Persist and project one completion per execution | `35427205` |
| 5 — Give explicit turn stops their own targets | `acf8674c` |
| 6 — Route shell and test calls through managed execution | `bfe8bfd7` |
| 7 — Preserve service ownership across reloads and child registries | `8e9d6a6e` |
| 8 — Add authoritative conversation-end phases | `6cf6428d` |
| 9 — Add the active-work warning panel | `1231a4b0` |
| 10 — Route interactive ending through settlement and postmortem | `e57821a4` |
| 11 — Preserve ACP semantics and expose execution evidence | `2c20a61c` |
| 12 — Verify complete lifecycle composition and integration | `2637875f` |
| Branch review findings | `22dac64b` |

### Acceptance evidence

- `CGO_ENABLED=1 go test ./...`, `go vet ./...`, `go build ./cmd/marshal` and
  the plan's focused `-race` run all pass with cgo enabled. The only failures
  anywhere are the pre-existing ones recorded below.
- `git diff --name-only -z 16d0c04a -- '*.go' | xargs -0 gofmt -l` prints
  nothing, and `git diff --check` reports no whitespace errors.
- Three independent reviewers covered the branch with disjoint scopes (command
  service/sandbox/config; session work gate/runtime end lifecycle; transports,
  surfaces and evidence). Each returned "approved with minor notes"; the
  follow-up commit above addresses the material findings (intent relabelling on
  a repeated acceptance, the unbounded `Kill` join, settlement classification
  and keying, identity-keyed child-work release, the repeated-settlement guard,
  and moving postmortem extraction off the render loop).

### Pre-existing failures (not caused by this feature)

Verified against the base commit in a separate checkout: they fail there
identically, and this branch touches none of those packages.

- `internal/snapshot`: 12 failures (`TestLookupDistinguishesExpiryNotFoundAndCorrupt`,
  `TestLookupFindsLegacySnapshotAndRestoresIt`,
  `TestLookupFallsBackToLegacyWhenV2DoesNotRetain`,
  `TestServiceStoreDirMatchesManagerCatalog`,
  `TestDiscoverFindsV2AndLegacyStoresWhoseCheckoutIsGone`,
  `TestCleanupReconcilesVersionedStoreWithoutConfirmation`,
  `TestRootedMaintainsWorktreeStoresNotJustActiveRoot`,
  `TestRootedMaintenanceCoversBothRootsAcrossRebind`,
  `TestRootedMaintainsInactiveStoreWhoseCheckoutIsGone`,
  `TestService_ExcludesLargeFile`, `TestService_ExcludesGitignoredFiles`,
  `TestService_Prune`, `TestTrackSucceedsWithNestedGitignoredLargeFile`).
- `internal/app`: `TestRuntimeCloseRunsNoGitGC`,
  `TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces`.
- `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`
  (the fetched example.com text no longer contains "Example").
- `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity` (the
  fixture environment supplies a git identity).

The plan's manual check (a harmless local long command through continuation,
settings change, peek/await, declined exit, then accepted exit with postmortem
evidence) is left to the integrator: it needs an interactive terminal, and the
composition is covered by the Task 12 regressions instead.

## Plan authoring checks

- Verified existing file paths, declaration anchors and relative document links against the stated base; new files/APIs are explicitly identified.
- Checked all 12 tasks for goal/files/steps/verification, dependency order and coverage of the spec's lifecycle cases.
- Compiled and passed the embedded panel tests through a temporary Go overlay. Compiled the config/settings edits through a separate temporary overlay; the existing config suite and settings registry construction/parity checks passed. No production source was edited by these checks.
- Full feature tests, race checks, vet and build remain execution-time acceptance checks; they are not claimed complete by plan authoring.
