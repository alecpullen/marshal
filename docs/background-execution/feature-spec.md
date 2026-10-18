# Reliable background execution

Status: implemented (contracts below are the shipped behavior).
Date: 2026-10-17.
Source baseline: `f25afa3f`.
Implementation: complete on the `reliable-background-execution` branch; this
document is the contract the code implements.

## Purpose

Marshal must keep a command running when a tool stops waiting for it. Large downloads, LAN transfers and test suites must not be terminated by a foreground timeout, an await timeout or a configuration reload. Processes stop when they finish, fail, receive an explicit stop request, or their owning conversation is deliberately ended.

This feature covers the first two testing observations: background execution lifetime and `agent.await` behavior. It establishes the lifecycle needed by later task UI and steering work.

## Agreed behavior

The following decisions were agreed during brainstorming:

- One active top-level conversation per terminal. Subagents belong to that conversation. Starting or resuming another conversation replaces the current one; previous conversations do not continue running.
- Commands outliving a foreground waiting period automatically continue in the background, using the same process and a job handle.
- Elapsed wall-clock time does not request process termination. Timeouts bound waits, including when the agent supplies them.
- An explicit stop-turn request terminates that turn's currently foreground shell command. Already-backgrounded jobs remain running. If the current tool is awaiting background work, stopping the turn ends only the wait.
- Background jobs survive normal configuration reloads and ordinary turn completion.
- Ending Marshal stops its jobs. If active work would be stopped, warn the user first, independently of postmortem settings and before any postmortem prompt.

This feature uses a **10-second foreground wait default** (`tools.shell.foreground_wait_seconds`, constrained to a positive integer), configurable and overridable per call. The default is selected, not proposed: `config.Default()` ships it, and `resolveShellWait` uses it for an omitted or zero `timeout_seconds`.

## Evidence and current behavior

The original investigation and postmortem supplement are archived at:

- `.docs-archive/triage/2026-10-15-testing-observations.md`
- `.docs-archive/triage/2026-10-16-postmortem-evidence.md`

The investigation reproduced a background execution deadline inherited from `shell.run`'s foreground timeout: 120 seconds by default, capped at 300 seconds. It also found that configuration reload replaces and shuts down job managers, and that child tool registration can replace the parent manager's count callback.

Real-manager probes found that await expiry, cancellation and an `any` winner leave other jobs running. Stored postmortems corroborate recurring terminated shell calls, but lack the cancellation-cause data needed to attribute them to a particular mechanism. This feature preserves the non-destructive await behavior while correcting execution ownership and recording explicit stop causes.

The current model-facing tools are `shell.run`, `agent.await` with optional `peek`, and `agent.kill`. This feature extends those tools; it does not restore retired job/watch tools.

## Scope

Included:

- Managed execution for ordinary foreground and background shell commands.
- Automatic continuation after foreground wait expiry.
- Non-destructive await and peek operations.
- Explicit stop semantics for a turn, a target and a conversation.
- Stable ownership across parent/child tool registration and config reload.
- A warning before interactive exit or conversation replacement stops active work.
- Bounded output, accurate completion records and termination-cause evidence.
- Integration with the existing TUI, persistence and ACP behavior.

Separate follow-up features:

- Running conversations concurrently, detached processes and recovery across Marshal restarts.
- Send now steering within a still-running turn.
- The pinned task-card redesign, custom progress widgets and new job-management panels.
- Broad latency optimization, editing/worktree recovery changes and test keychain isolation.
- Automatic background continuation for `shell.sudo`; its password handling and privileged execution path remain separate.

Not claimed by this feature, and deliberately so:

- **No Send now.** There is no way to deliver a message into a still-running turn; steering is a follow-up.
- **No restart persistence.** Job handles and outputs restore for display, but a job never restarts and its process is not recovered after Marshal exits.
- **No whole-process write coordination.** Commands that hand off keep the coordination rules they already had; Marshal does not serialize workspace writes across concurrent commands or processes.

## Architecture

### One conversation-owned execution service

Extend the existing job manager into the owner of ordinary shell command execution. Every managed command starts under a context owned by the active conversation, independent of the tool-call wait context. A command begins with an execution identity even when the caller initially waits for it in the foreground.

Foreground and background describe the caller's attachment to the process, not separate execution implementations. A command transitions from foreground attachment to background attachment without launching a second process, changing its working directory, reapplying its side effects or repeating approval.

Reject a launch whose caller has already been stopped before admission. Once admitted, the execution service owns the process and accepts explicit stop requests independently of the wait context. The foreground wait period starts with execution admission, after permission/approval preflight; waiting for a user decision must not consume it.

The alternative is to retain separate execution paths and transfer a live process and its output streams at timeout. Managing execution from launch is preferred because ownership and cancellation remain consistent throughout the command's life.

### Ownership and configuration

- The active conversation owns one service. Parent, subagent and pipeline tool registries consume it; registration must not replace its owner callbacks.
- Record the requesting actor and turn separately from the owning conversation. A child finishing or a parent turn ending does not end a detached job.
- Capture the command, working directory, command runner/backend and output settings at launch. A later workspace or config change does not redirect an existing process.
- Ordinary config reload reuses the service and existing handles. Updated execution settings apply to future commands; resources referenced by running commands remain alive until they finish.
- Production registry construction must inject a conversation-owned service. It must not silently create a manager rooted in `context.Background()` with no shutdown owner.
- Maintain one authoritative snapshot for command state, background-job counts and shutdown decisions. Do not infer active work from a TUI cache or a callback last installed by a child.

### Responsibilities

| Component | Responsibility |
|---|---|
| Execution service | Process lifetime, output, attachment transition, explicit stop, capacity and terminal records. |
| Native tools | Validate arguments, launch approved commands, wait/peek, and return command or job results. |
| Agent runtime | Own turn stop requests and valid tool-result history; never treat wait expiry as process failure. |
| App/session runtime | Own the service across reloads and coordinate ending the conversation. |
| TUI | Render status and the active-work warning; submit user choices through a structural runtime interface. |
| Persistence/postmortem | Store final outcomes and enough provenance to explain termination. |

Routing, policy, prompt construction and process cancellation rules remain outside `internal/app/tui/`. The TUI must continue to avoid importing `internal/agent`.

## Tool behavior

### `shell.run`

Keep `command`, `background` and `timeout_seconds` for compatibility. Change the documented meaning of `timeout_seconds` to the maximum synchronous wait, not an execution deadline.

| Arguments or event | Required result |
|---|---|
| `background: true` | Start the command and return a job handle immediately. No execution deadline is derived from `timeout_seconds`. |
| Foreground; timeout omitted or zero | Wait for the configured foreground period (default 10 seconds). |
| Foreground; positive `timeout_seconds` | Wait for that duration. Do not inherit the previous execution-time cap or silently clamp the requested wait. |
| Negative `timeout_seconds` | Reject before starting a process. |
| Process exits while attached | Return the actual exit code and captured output as the shell result. |
| Waiting period expires | Return a normal continuation result with a job handle and recent output; the same process keeps running. |
| Explicit stop-turn while attached | Request termination of the foreground command and return/persist its actual outcome. |

With `background: true`, the call does not wait; a nonnegative timeout argument is accepted but does not create an execution limit. Describe this explicitly in the schema and tool description.

Add `tools.shell.foreground_wait_seconds`, default 10 and constrained to a positive integer. A config reload changes the default for future calls, not an already-running wait. Explicit `background: true` is the immediate-return option.

`tools.shell.default_timeout_seconds` is **deprecated compatibility data**. Existing files keep loading and saving it, and its value is preserved verbatim, but it no longer drives ordinary shell execution: it must not override `foreground_wait_seconds` and must not become a hidden execution deadline. It is no longer an editable `/settings` row (`/set tools.shell.default_timeout_seconds` no longer resolves). A config that carries only the legacy key still gets the new wait default.

An illustrative continuation result:

```text
Command is still running in the background.
job_id: job-12
Wait with agent.await using job_id; inspect with peek: true.
```

The actual result also carries structured identity/status information. The UI and persistence should not depend solely on parsing that sentence. Continue recognizing historical background-start summaries on resume.

The tool must not report a wait expiry as an error, a failed test or a killed command. A genuine nonzero exit still reports a failed command with its actual exit code.

Any internal ordinary test-command execution using the same shell path adopts these semantics. Changing the tool wait must not leave an internal `test.run` path that silently kills an equivalent long-running test command.

### `agent.await`

- Preserve the existing selectors: `id`, `job_id`, `any` and `all`, with exactly one selection.
- `peek: true` with a target returns its current status and output without changing its lifetime.
- An await timeout returns a normal result describing remaining work. It does not terminate targets.
- Turn cancellation or explicit interruption of an await ends the wait only. The targeted jobs and subagents remain independent.
- When `any` returns, cleanup of the losing waiters must not stop their targets.
- Await does not acquire a workspace write gate that could prevent the target from completing.
- A terminal result contains the actual outcome, exit code when available, stop cause when applicable, and a bounded output tail.

### `agent.kill` and explicit stops

Use the existing targeted stop tool for `id` or `job_id`. Cancellation is a request until execution settles. Repeated stops are harmless; stopping a completed target does not relabel its natural completion.

Do not add an execution-deadline argument. Marshal's wall-clock timers only affect waiting. Explicitly configured sandbox resource budgets remain distinct constraints and their enforcement must be reported as a resource-limit outcome, not misreported as wait expiry. A command's own timeout utility or an external OS/process failure is also distinct from Marshal ending a wait.

## Stop semantics

| Event | Foreground shell command | Already-backgrounded jobs | Subagents |
|---|---|---|---|
| Foreground wait expires | Becomes background; continues. | Continue. | Continue. |
| Await expires or is cancelled | No target termination. | Continue. | Continue. |
| Ordinary turn completes | No attached command remains. | Continue. | Continue under their existing session lifetime. |
| User explicitly stops the parent turn | Stop commands still attached to that turn. | Continue. | Continue; stopping a specific subagent is a separate action. |
| User/model stops a particular target | Stop the named command if applicable. | Stop only the named job. | Stop the named subagent and its attached foreground command. Its previously detached jobs remain conversation-owned. |
| Config reload | Continue with captured settings. | Continue with captured settings. | Preserve existing in-flight behavior. |
| Confirmed conversation end or process exit | Stop. | Stop. | Stop. |

For parallel tool batches, explicit turn stop applies to every command still attached to that turn. Persist an outcome for each call that started; calls not launched before the stop are marked as not started. Keep tool-call/result pairing valid.

The handoff-versus-stop race must be resolved at one authoritative boundary. If the process was already detached when the stop request was accepted, it continues. If it was still attached, the stop targets it. The returned status and UI must agree with that decision.

## Interactive exit and conversation replacement

### Warning before stopping work

All interactive paths that would end the current conversation use the same runtime active-work check. This includes normal quit/exit and the existing new/clear/resume paths when they would discard the current conversation. Existing busy restrictions on new/clear are not relaxed by this feature.

If work will be stopped, show a concise live summary of running commands and subagents, including the active parent operation when applicable. Routine infrastructure workers such as indexing, LSP and OAuth refresh do not require this warning.

Offer:

- **Keep Marshal open / Keep current conversation** — selected by default; Esc has the same effect. Work continues unchanged.
- **Stop work and continue exiting / replacing the conversation** — explicitly accepts terminating that work.

The warning is independent of approval mode and `postmortem.on_exit`. Neither Auto mode nor disabling postmortems disables it. Repeated exit requests must not stack prompts or silently bypass the warning.

Use current runtime state when accepting the decision. Work that completed while the prompt was open is not stopped or falsely marked cancelled. If additional work would now be terminated beyond the displayed set, refresh the warning before accepting that expanded destructive action.

### Confirmed exit sequence

1. Accept the active-work decision and prevent ordinary new work from starting.
2. Stop and join the current foreground operation, background commands and subagents. Persist their terminal outcomes and final output while the database remains open.
3. Enter the existing configured postmortem flow: prompt, automatic extraction or skip. This is the first point a postmortem prompt may appear.
4. If selected, run the postmortem analysis as an explicit exit phase, keeping the infrastructure it needs available. It must not reopen the normal conversation or recursively restart the exit warning.
5. Close remaining runtime resources and exit.

Stopping current work and permanently closing all runtime services must be separate operations so the optional postmortem pass remains possible. Cleanup of work owned by the explicitly selected postmortem phase belongs to that exit operation.

If postmortem extraction or analysis fails, report the failure and finish exiting according to existing behavior. Do not leave the user in an unexpectedly elevated normal conversation.

Conversation replacement stops the old conversation after confirmation and then follows the existing replacement/resume behavior; it does not add an exit postmortem where none currently exists. No jobs are transferred to the new conversation.

No interactive confirmation is possible after a forced OS kill or lost terminal. Graceful noninteractive shutdown still stops owned processes and records the available reason; no prompt may block a headless ACP client indefinitely. An explicit ACP session stop remains that client's stop request.

### Headless (ACP) ownership

The ACP transport applies the same ownership rules, and it never prompts:

- `session/cancel` is an explicit turn stop. It asks the conversation's command service to stop the commands still attached to the turn *before* cancelling the agent's waits and provider calls (cause `turn stop requested`), so a foreground command is terminated while a command that already detached keeps running. A cancel that lands while the turn is still starting is retained by the session and adopted by the registration, so it cannot be lost.
- Session close, close-all, and the load/resume/delete replacement path are explicit session-level stops: they stop the old conversation's attached commands (cause `conversation ended`) and only then tear down the runtime, which stops everything the conversation still owns. These paths call no prompt, no permission bridge and no postmortem UI — a headless shutdown that asked a question would block indefinitely with nobody attached to answer.
- An ordinary turn finish (including an ACP prompt returning) never requests a stop. Its cleanup only unregisters the turn's own stop group, and it clears only that registration, so a newer operation's target is never unregistered by an older one finishing.
- A transport deadline or a caller's cancelled request context ends only the *wait*. It does not stop the conversation, and the turn's detached commands remain conversation-owned.
- A session with no command service still cancels its agent; the unavailable stop is reported by the session, never as a transport error, and response codes are unchanged.

## Capacity, policy and output

- Reserve execution capacity before launching, including commands initially attached in the foreground. Handoff never needs a new slot and never kills an existing job to make room.
- Reuse the existing configured job capacity as the managed-command limit for this feature, counting attached and detached running commands. Preserve the existing configuration key for compatibility and document its broadened meaning. `tools.shell.max_background_jobs` therefore now means "max concurrent running commands" — it counts foreground-attached commands as well as detached ones, and `/settings` labels it `Max running commands`. Lowering it below the active count affects admission of future commands only.
- Preserve approval, sandbox selection, environment filtering, pre-launch rollback protection and command guardrails. Handoff is continuation of the already-approved invocation, not a second invocation or permission bypass.
- Preserve the current coordination rules for explicit background commands when a foreground command detaches. This feature does not claim to solve whole-process concurrent workspace editing; the broader effects/locking work is a separate triage item.
- Keep output bounded per command, retain a useful recent tail of stdout and stderr, and disclose truncation. Reaching the output limit must not stop execution or freeze the retained tail at the first captured bytes.
- While attached, stream output through the existing active-call display. After detachment, expose it through the existing job/await projection.
- A command that completes in the foreground produces its normal tool result. A detached command produces one background completion record. Do not render or persist duplicate completion cards for the same outcome.
- Show only live work as active. Historical completed records remain queryable under the existing retention policy without being counted as running.

## State, persistence and diagnostics

Track execution state separately from attachment. States must distinguish running, stopping and terminal outcomes. Preserve enough information to distinguish successful completion, nonzero exit, explicit stop, shutdown, resource limit, startup failure and an outcome lost to an abrupt process exit. Historical `timed_out` records remain readable; a wait expiry does not create that execution state.

Record:

- Job identity, owning conversation, originating actor/turn/tool call.
- Working directory and execution configuration generation.
- Process identity as soon as it starts, start/end times, actual exit code when available.
- Whether/when the command detached and the reason for detachment.
- Explicit stop request/cause separately from the actual terminal outcome.
- Output truncation and a bounded final output tail.

Persist final detached-job outcomes before reporting them complete. Report termination reason and effective wait/handoff behavior in structured diagnostics and postmortem evidence. Timers for grace periods after an explicit stop may escalate process-tree termination; they must not become independent lifetime deadlines.

The evidence reaches clients as typed data, on the existing transports:

- The stack-detail transport (`session/stack_node`) carries a `CallDetail.execution` object for every call with a managed-command record: identity, ownership (`conversationId`, `actor`, `turnId`, `stepId`, `toolCallId`), `cwd`/`generation`/`pid`, state/outcome/attachment, the effective `wait`, `detachReason`/`detachedAt`, `stopCause`/`stopRequestedAt`, an optional `exit`, and `truncated`. It is additive and omitted when there is no record: the coaching text a notice may carry stays in `notice.text`, so a client explains a wait, a handoff or a stop without parsing a sentence. An absent `exit` means no exit was ever observed and must never be rendered as success.
- The wire projection carries a card's `executionId`, `wait`, `detachReason` and `detachedAt` alongside the `status`, `attachment`, `stopCause`, optional `exit` and `truncated` it already carried.
- `postmortem.Report` gains an additive `execution_evidence` section — one entry per execution identity, from foreground call records and detached final records — stating ownership, effective wait, handoff, stop request, observed outcome and whether the settlement is failure evidence. A continuation (a wait that ended while the command kept running), an await expiry, a natural success and a lost record are not failures. The schema version is unchanged by the addition.

Command, working directory and output text travel through the existing redaction/scrubbing paths before they reach any of these.

Resume restores historical job outcomes. A previously active job whose process owner no longer exists is shown as lost, not running or successfully completed. This feature does not restart the process.

Expose shared data through ACP JSON where needed. `web/` remains standard-library-only and may not import `marshal/internal/...`. The initial UI change is the TUI warning and accurate existing job status; no web task-card redesign is included.

## Important execution examples

1. A download runs for 20 minutes. The foreground wait ends after 10 seconds; Marshal returns a job handle. Several subsequent await timeouts leave the download running. It finishes once and its output is available.
2. A shell call requests `timeout_seconds: 900`. That is a 900-second wait budget, not a shortened hidden execution deadline. When it expires, an unfinished command continues in the background.
3. The user stops a turn while its foreground `sleep` command is still attached. That command is terminated. An older background download keeps running.
4. The user stops a turn waiting in `agent.await`. The await ends; its target continues.
5. The user changes model settings while a background suite runs. Reload does not replace its owner, reset job IDs, lose its output or terminate it.
6. With postmortems disabled, quitting while a transfer runs still shows the active-work warning. Esc keeps the transfer running. Confirming stops it and exits.
7. With postmortems prompted, confirming exit first records the transfer's stopped outcome, then opens the postmortem prompt. The report can therefore include the final outcome.
8. `/new` or resume would replace a conversation with live jobs. The replacement warning prevents silent job loss. Confirming ends that work; no old conversation continues concurrently.

## Acceptance and verification

Use deterministic command-runner tests for ownership, events and races, plus a small number of actual-process integration tests for process survival and group termination. Short test deadlines stand in for long downloads; no real transfer or provider is required.

Required checks:

- A foreground call completes normally before the wait period, with actual stdout/stderr and exit code.
- An unfinished foreground command detaches with the same PID and runs exactly once; explicit background launch returns immediately.
- No execution timeout is derived from omitted, zero or positive wait arguments. Invalid negative values launch nothing.
- Await timeout, cancellation, `any` cleanup and peek preserve unfinished targets.
- Stop-turn terminates attached foreground commands and preserves detached jobs; targeted stop affects only its intended target.
- Completion/handoff/stop races produce one consistent outcome and one completion record.
- A long-output job keeps a recent bounded tail after truncation and after termination.
- Parent and child job counts agree; child/pipeline registration cannot replace owner callbacks.
- Reload preserves job handles, execution settings, output and final delivery. Capacity changes never evict running jobs.
- Every ordinary production command registry has a shutdown owner.
- Exit/replacement confirmation appears before any destructive stop and before postmortem UI, for `prompt`, `always` and `never` postmortem settings.
- Cancelling the warning changes no execution state. Live prompt updates and new-work races cannot expand an accepted stop silently.
- Confirmed shutdown records outcomes before optional extraction; the postmortem pass can still run and cannot reopen ordinary work.
- Existing single-conversation behavior, historical resume, shell approvals, sandbox constraints, ACP call pairing and prompt-prefix stability remain intact.

During implementation, run focused package tests first, then the full Go suite and build with cgo. Make app/runtime tests hermetic through existing injection seams and in-memory credentials; do not require a real keychain or external provider.

## Delivery boundaries

1. Conversation-owned command execution, bounded waiting and automatic handoff, including stop-turn semantics.
2. Reload/child/pipeline ownership integration, output settlement and termination evidence.
3. Interactive end-of-conversation warning and postmortem ordering.

Each delivery has an independently testable result. The executable decomposition and migration steps live in `docs/background-execution/plans/2026-10-17-reliable-background-execution-plan.md`; this document is the behavior those steps implemented.
