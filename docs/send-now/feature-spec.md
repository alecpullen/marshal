# Send now: interrupt an operation and steer the same turn

Status: approved design and written specification; implemented on branch `send-now` (see [Implementation record](#implementation-record)).
Date: 2026-10-18.
Source baseline: `de5e47bdc5e695d4e422e99059d27354a84dce14`.
Implementation: complete on branch `send-now`; see the implementation record at the end of this document.
Workflow: [Marshal brainstorming](../../internal/skills/builtin/brainstorming.md), followed by Marshal's internal plan-writing skill after written-spec review.

## Context and classification

The original TUI observation requested Send now for steering that can interrupt a blocking tool call, including long shell commands containing sleeps. The agent must learn what was interrupted and what remains running.

Latency issues and test isolation have been reported complete. Reliable background execution is being implemented separately; the system-access approval plan has been submitted for execution. This feature consumes their ownership and canonical approval contracts without treating those submitted plans as already merged.

This is a bounded extension of the existing steering, agent-loop, cancellation and TUI input flows. It adds operation interruption within one conversation turn. It does not add a scheduler, second process supervisor, multiple TUI conversations or controller-level workflow steering.

Related contracts:

- [Reliable background execution](../background-execution/feature-spec.md): waits do not terminate targets; explicit stops distinguish attached foreground commands from detached work.
- [System-access approval](../system-access-approval/feature-spec.md): canonical request settlement, stale-response protection, and applied permission state before acknowledgment.
- [Architecture](../ARCHITECTURE.md): rendering-only TUI, structural runtime boundaries, append-only prompt prefix and ACP JSON boundary.
- Historical [testing triage](../../.docs-archive/triage/2026-10-15-testing-observations.md) and [postmortem evidence](../../.docs-archive/triage/2026-10-16-postmortem-evidence.md): accurate interruption/resumption context is required; duplicate narration is a separate investigation.

## Approved decisions and alternatives

The user explicitly selected:

1. Stop an attached foreground command when Send now interrupts it; do not automatically detach it to keep it running.
2. Cancel an unanswered approval or question so the agent can process steering. Cancellation grants no permission; already-committed approval remains effective.
3. Interrupt the current operation and continue the same turn. Do not automatically stop and restart the turn.

The complete proposed design was approved, including queued-versus-immediate delivery, Alt+Enter and a composer, canonical runtime ownership, truthful outcomes, safe mutation boundaries and an initial normal-conversation scope.

Stopping and automatically restarting a turn was considered because it reuses existing cancellation paths. It was rejected because it introduces another turn boundary and complicates continuity, queued messages and tool histories. Automatically detaching the current command was rejected in favor of an explicit foreground stop.

## Current implementation evidence

These are source findings on the stated baseline, not claims that Send now already works:

- `internal/app/tui/keypress.go` handles busy Enter by calling State.PushSteering. `internal/app/session/steering.go` keeps a FIFO queue, and the agent drains it at loop top in `internal/agent/runner.go`. A blocking operation can therefore delay delivery.
- `recordInterruptMarker`/`failTurn` in runner.go record full-turn cancellation. Reusing that path for Send now would end the turn rather than resume its loop.
- `internal/agent/execute.go` passes the caller context through approvals, tools and batches. Native and envelope paths have different batching behavior; both need complete call/result accounting when interrupted.
- `WriteGate.Acquire` and `internal/agent/swarm/lock.go` use an unconditional mutex wait. Send now must be able to cancel waiting for admission without later acquiring the gate and launching stale work.
- `internal/agent/chat.go` owns request timeout/retry/stream cleanup. Steering needs a distinct cancellation cause so it cannot become a provider retry or an abandoned streaming goroutine.
- `internal/agent/requests.go` and session pending state own human waits. Current event copies and unconditional clears are not an identity-safe interruption protocol.
- `internal/app/tui/dock/dock.go` hosts one panel. A composer must preserve the displaced panel and its asynchronous updates rather than replacing its state.
- `internal/acp/turn.go` has queued `session/steer`, separate turn cancellation, and separate normal/swarm/SDD entry points. `internal/pipeline/adapter.go` and `internal/agent/swarm/orchestrator.go` are independent orchestration loops.

The background plan's `StopAttached(turnID, cause)` / explicit turn-stop contract closes that turn's future command admission. Send now must not invoke it as though it were a reusable operation interruption. It needs a narrower operation boundary under the same conversation-owned execution service.

## Scope

Included:

- Normal top-level conversation turns, including the parent waiting for independently running subagents.
- Cancellation of model requests, cancelable tools, awaits, gate waits and human requests owned by the interrupted parent operation.
- Explicit termination of commands still attached to that operation, with settled output/outcome.
- Queued steering preservation, repeated-submission handling and complete native/envelope tool histories.
- A discoverable TUI action/composer and an additive ACP entry into the same runtime service.

Excluded:

- Steering `/sdd` or `/swarm` controllers, declarative plan operations or their human gates.
- Directly steering a child by viewing/drilling into its transcript. Send now addresses the parent conversation.
- Stopping independent children or detached jobs, changing their execution deadlines, or a new stop-all action.
- Task-card redesign, progress widgets, narration deduplication, latency work and credential isolation.
- New permission grants, settings persistence, runtime reload, automatic rollback or automatic turn restart.
- Crash recovery, durable unsent drafts or a new persistent steering queue/schema.
- New execution capability for plugins/MCP, or a guarantee that remote side effects can be undone.

Unsupported runners expose Send now as unavailable and return an explicit unsupported result. They must not silently turn it into queued steering or full-turn cancellation. Existing ordinary Send behavior remains available according to that runner's current contract.

## User-visible actions

| Action/context | Behavior |
| --- | --- |
| Enter in ordinary chat during a turn | Queue as today; consume at the next normal steering boundary. |
| Alt+Enter in ordinary chat with text | Submit that text as Send now. Clear the draft only after runtime acceptance. |
| Alt+Enter in ordinary chat with no text and queued steering | Request immediate delivery of that queue; do not add an empty message. |
| Alt+Enter with neither text nor queued steering | No interruption; show a concise empty-input hint. |
| Alt+Enter while a panel or human prompt owns input | Open the temporary Send now composer; opening it does not cancel anything. |
| Enter in the composer | Submit its literal text, or flush existing queued steering when text is empty and the queue is nonempty. |
| Shift+Enter in the composer | Insert a newline. |
| Esc in the composer | Close it with no interruption or queue change; restore the prior valid surface/focus. |
| `/send-now <text>` | Command alternative that submits the argument as steering, using the same runtime operation. |
| `/send-now` without text | Flush queued steering when available; otherwise open the composer during a supported active turn. |
| Send now without a supported active turn | Explain why unavailable and preserve the draft. Do not implicitly start a new turn. |

Composer text is steering, including text beginning with `/`; it is not a second command parser. Reuse ordinary steering's paste expansion and file-reference processing. Validate expanded input before acceptance; never drop the source draft on a validation error.

The composer starts empty when opened from another surface. It never copies a password, credential, approval-command edit or question-answer draft into a steering message. The main chat draft and the other surface's input remain separate.

The composer belongs to the conversation and active turn shown when it opens. If either changes before submission, reject the stale submission and retain its text for deliberate reuse. Within that same turn, submission targets the operation current at runtime acceptance; merely opening a composer does not reserve an old operation.

## Runtime ownership and identities

App/session runtime owns acceptance, queue ordering, active turn/operation registration and receipts. Agent owns the execution checkpoints. The command service owns stop-versus-detach arbitration. Transports submit intent and render snapshots; the TUI does not implement cancellation/policy and must not import internal/agent.

Reuse the background implementation's conversation/actor/turn identities and execution service. Add a distinct, short-lived operation identity beneath the turn: a logical model call, a tool batch, or its associated human/gate wait. Do not confuse this with the background plan's turn-stop token. Each tool call also retains its own step/call/execution identity.

Runtime exposes three structural capabilities:

- Read an immutable active-turn steering snapshot: conversation, turn ID, support/active/held state, current operation ID/kind and pending message count.
- Submit Send now with expected conversation/turn identity, request ID and optional new text; return a receipt or an explicit rejection.
- Observe receipt changes and current snapshots without blocking the TUI.

Request IDs identify transport submissions; steering-message IDs identify individual queue entries. Opaque IDs are independent of provider call IDs, which providers may reuse. Queued and immediate messages share one ordered queue; preserve existing queued APIs for callers that do not request immediate delivery.

### Acceptance and interruption sequence

1. Validate expected conversation/turn, runner support, active/admission state and input. A flush requires an existing queued message. Reject a stale/inactive/closing target before changing the queue or invoking cancellation.
2. Under the runtime's synchronization boundary, recognize a duplicate request ID or append the new message once, assign its FIFO position, mark immediate delivery pending, and capture the current operation identity. Acceptance prevents that operation from admitting further stale work.
3. Release ordinary state locks before callbacks. For attached commands, the execution service atomically arbitrates interruption against natural completion and detachment. Request termination only for commands still attached to the accepted operation; stop admission for that old operation, not the whole turn.
4. Signal operation cancellation with a distinct user-steering cause. Cancel its matching pending human/gate requests through canonical settlement. The parent turn/session contexts remain alive.
5. Settle/join work that actually started, preserve actual outcomes, and finish already-admitted atomic mutation units. No new model request starts while the interrupted batch is still producing results.
6. At the checkpoint, append complete tool results, then the bounded interruption context and queued steering in FIFO order. Register a fresh operation for the same turn and continue subject to its existing hold/budget/policy rules.

An accepted message is pending, not yet delivered. A receipt becomes delivered when its message has been included in a submitted model request. Retries reuse that same history entry; they do not append a second copy.

No process/model call is awaited inside the TUI update handler or while holding session state locks. A slow stop produces a visible pending state and receipt updates.

### Repetition, completion and lifecycle races

- Retrying an identical request ID returns its existing receipt, with no duplicate message or stop. Reusing an ID with different text/target is rejected.
- Distinct Send now submissions append distinct messages. While one operation is already stopping, coalesce its stop request; deliver all accepted messages in acceptance order.
- A cancellation callback holds its captured operation identity/context. Unregistration is compare-and-clear; it cannot cancel or clear a newer operation.
- If the operation completed before acceptance, capture the new current operation or the intervening checkpoint. There is no claim that the completed operation was stopped.
- If detachment won at the command service boundary, the job continues. Report its identity/status; do not stop it later using a stale foreground classification.
- Accepted steering is checked before final-answer publication/turn closure. A turn cannot silently finish over an accepted, deliverable message.
- Explicit stop, shutdown or a hard budget limit can still end the turn. Preserve accepted but undelivered steering in the existing pending/interrupt-marker path and report it as undelivered. Do not erase it during UI cleanup, falsely mark delivery, or automatically start another turn. On closure disarm that turn's interruption callbacks and immediate-delivery flags; retained unsent text becomes ordinary follow-up queue content. A later explicit Send/Send now can use it, without reviving the closed turn's control request or changing its final undelivered receipt.
- Existing `hold` remains authoritative. Send now may interrupt current work and queue messages, but does not silently unhold the session. Show waiting-for-resume until the normal resume action permits the checkpoint/model request.

## Operation outcomes

| Operation at acceptance | Required behavior/evidence |
| --- | --- |
| Logical model request, including retries/streaming | Cancel its operation context, stop old-intent retries and drain/clean up the provider stream. Discard incomplete response/tool-call fragments. Keep known usage/inspection evidence. No whole-turn interrupt marker or provider-failure coaching solely for steering. |
| Attached ordinary shell/test command | Explicitly terminate its process tree through the existing service/backend, then return actual exit/stop cause and bounded partial stdout/stderr. Never report stopped merely because cancellation was requested. |
| Detached job or independent subagent | Continue unchanged. Identify relevant surviving targets in the interruption context; no target stop callback is invoked. |
| Parent `agent.await`, including any/all waiters | Cancel waiter contexts only. Preserve target execution/lifetime and report remaining target identities/statuses. |
| Waiting for write gate, hold release or pre-launch admission | Wake cancelable waits; do not later acquire permission to launch a stale call. Hold itself remains set. |
| Approval, access request or question owned by this parent operation | Settle canceled/interrupted once, without approval or fabricated answer. Release the waiter and ignore late transport responses. |
| Skill-load or masked sudo-auth prompt owned by this parent operation | Use cancellation without granting/sticky-denying future skill loads, persisting a rule, or retaining/submitting a password. Reuse existing secret cleanup. |
| File mutation before its commit unit | Respect cancellation before starting the mutation. |
| Atomic file mutation unit already committing | Finish the unit and its outcome recording. Report completed/partially completed work truthfully, then steer; do not force-abort or claim rollback. |
| Tool/plugin/MCP call without a safe cancellation acknowledgment | Request supported cancellation and show waiting-for-safe-boundary until the call settles. Record unknown remote side effects as unknown; never invent successful remote termination or start conflicting work while the old call is unresolved. |
| Batch members not yet admitted | Do not launch them; emit not-started results for their existing call IDs. |

Known process termination may use the existing graceful-then-forceful stop sequence after this explicit user request. That cleanup grace period is not an execution deadline and does not reintroduce time-based job termination.

Pausing during a multi-file/multi-tool operation is not an automatic transaction rollback. Preserve committed units, skip unstarted calls and disclose actual intermediate effects.

Pending requests in independent children are not automatically canceled by interrupting the parent wait. Child control remains explicit; viewing a child does not widen Send now's target.

## Canonical human-request settlement

Cancellation and user answers must arbitrate on the canonical request, owning session/turn/operation and a stable request identity. Display/event copies do not own a separate settlement decision. Register cancellation intent at the shared claim boundary during Send now acceptance, before signaling contexts/callbacks later; an approval cannot slip through the interval between accepting steering and sending its cancellation signal. Approval application and interruption use one consistent lock/claim order, with cancellation callbacks outside state locks.

- Cancellation intent accepted before approval application is accepted prevents both its grant and subsequent tool admission.
- An approval commit already accepted completes under its existing resolver contract; cancellation does not roll it back.
- Even when permission was approved first, a tool not admitted before the steering interruption must not launch under stale intent.
- A canceled question produces interruption/cancellation information, not a fabricated human answer. Existing transport compatibility sentinels may remain where required, but the canonical operation cause and tool result must identify steering.
- Clearing a canceled request compares identity; late cleanup or answers cannot erase/settle a newer request.
- Resolve only requests owned by this interrupted operation. Do not invoke blanket shutdown cleanup, clear the steering queue, or dismiss unrelated child/workflow prompts.

Extend the same canonical mechanism introduced by the system-access work where applicable. Do not create an access-only bypass, independently send/close event-copy channels, or use a canceled access request to grant permissions.

## Conversation and model history

Complete every assistant tool call that has entered live history with one terminal tool result, including not-started calls. Apply this to native-tool batches, envelope batches, serial questions and concurrent dispatch. Already-spawned independent agent.run children retain their handles; interrupting dispatch/waiting does not orphan or terminate them.

No model request is issued with dangling tool calls. A model stream canceled before its response is accepted contributes no executable partial tool calls. If a complete response already entered history, its unlaunched calls receive not-started results before steering.

Append interruption context and steering after those results. Preserve the existing history prefix byte-for-byte within the turn; dynamic operation state and file-reference context belong in the volatile working tail or append-only messages after history. Do not rewrite earlier system/history messages to describe interruption.

Use one bounded interruption note, at most 2 KiB of valid UTF-8 text, with IDs, actual outcomes and continuing work. Tool-result output uses existing bounded-output limits; do not duplicate full output into the note. Disclose truncation and link to existing execution/inspection information. Steering messages themselves retain ordinary input limits and are not truncated to this note budget.

The note distinguishes user steering from failure, timeout, full-turn stop and shutdown. Do not count an intentionally skipped/interrupted operation as evidence for repetitive-tool-failure coaching or automatically mark its todo failed. Preserve real errors and resource-limit outcomes when those won the race.

Messages incorporate once into this turn's history; queued messages already delivered are never reinjected. Keep ordinary steering envelopes and raw file-reference handling consistent with restart history. This feature adds no crash-durable queue; pre-delivery state remains session memory under the existing retention/interrupt behavior.

## TUI interaction and presentation

Use the existing dock, textarea/text-field, semantic theme and footer conventions. The composer is a small temporary surface, not a general panel-stack replacement.

- Outside the ordinary chat editor, intercept the explicit Send now shortcut before an approval/question/panel can reinterpret it as an answer. Preserve existing Ctrl+C stop/exit behavior.
- Retain the previous panel identity, input/focus and editor draft while composing. One surface owns keyboard input. Continue normal runtime and panel-owned asynchronous updates while its input is suspended.
- Esc restores the prior panel only if still valid. Do not resurrect an expired approval/question. A retained access panel refreshes from its canonical request if permissions changed.
- Submission clears only the accepted composer/chat text. Rejection retains it and explains inactive, stale, unsupported or closing state. Closing after acceptance may restore an unrelated panel; canceled prompt surfaces retire.
- Display the target as the parent conversation while drilled into a child. Never infer control target from the viewed transcript.
- While a submission is pending, show interrupting, waiting-for-safe-boundary or waiting-for-resume as applicable. Then show a concise truthful receipt; the actual tool results remain inspectable.
- Footer labels distinguish Enter queue/send and Alt+Enter Send now; the composer advertises Enter send now, Shift+Enter newline and Esc back. Include `/send-now` in command help/completion.
- At 80×24, 120×40 and 200×60, keep the composer target, input and submission/cancel keys readable. Use the existing size gate below minimum; accept no hidden submission. Preserve text over resize and support no-color/16-color themes.

No timer selects or submits an action. Opening/closing the composer never changes execution or permissions by itself. Pending-prompt cancellation happens at runtime acceptance of an explicit submission.

## ACP contract

Queued `session/steer` remains backward compatible: omitted `delivery` means `queued`, nonempty text is required, active-turn checks retain their behavior, and existing callers need no new fields.

Add these optional parameters to the same method:

| Field | Meaning |
| --- | --- |
| `delivery` | `queued` or `now`; reject unknown values. |
| `turnId` | Opaque expected active turn. Required for `now`. |
| `requestId` | Client-generated submission identity. Required for `now` and stable across retries. |

For `delivery: "now"`, text may be empty only when existing queued messages can be flushed. Session/turn/support checks and atomic acceptance use the same runtime capability as TUI; an inactive, stale, unsupported or closing target fails explicitly. No fallback to queued delivery.

The immediate response is the accepted request's current receipt: requestId, turnId, status and captured operationId when present. Acceptance does not mean a process has stopped or a message has been delivered.

Add read-only `session/steering_status` accepting sessionId, returning the current snapshot: turnId (empty if none), active, supported, held, operationId/kind when present and pendingCount. This supplies an authoritative turn identity even if a client missed an event.

Use additive `session/update` kinds:

- `steering_state`: the same complete snapshot, on turn/support/hold/operation/pending changes. Coalesce redundant updates.
- `steering_receipt`: requestId, turnId, current status, operationId when present and bounded actual outcome summaries when settled.

Receipt status vocabulary is `accepted`, `interrupting`, `waiting_safe_boundary`, `waiting_for_resume`, `delivered` and `undelivered`. Invalid requests return the existing JSON-RPC invalid-params/server-error forms rather than an accepted receipt. Turn IDs are scoped to one running invocation; they are not provider response IDs.

In-memory receipts live for the active turn, with finalized receipts observable through its final update. Same-ID retries during that turn are idempotent. After closure, old turn IDs are rejected; replay cannot target a later turn. No persisted RPC-replay store is introduced.

Ordinary permission/question bridges use canonical settlement for canceled parent-operation requests; late RPC answers cannot revive them. Preserve ordinary queued steering, `session/cancel`, manual mode changes and existing hold behavior.

No web UI redesign is included. New JSON is the boundary; web remains standard-library-only and imports no internal Go types. Legacy clients may continue using queued steering.

## Errors, retained work and observability

- Unsupported/stale/inactive/empty/closing requests change no execution/queue state and leave TUI text available.
- Failed termination or a noncooperating operation remains visibly pending/unknown until its actual outcome is known. Do not proceed as though it settled.
- Natural command failure or external resource termination retains its actual cause, even if steering arrived nearby.
- Use existing logging/inspection with conversation, turn, operation, request, step/call and execution IDs. Record acceptance, stop request, settlement and delivery boundaries without logging secrets or duplicating sensitive arguments.
- Preserve captured command settings, manager identities, approval/sandbox/environment enforcement, rollback protection and retained output. Send now does not reload config, rebuild providers/managers or grant new authority.
- Full-turn stop, targeted child/job stop and conversation end retain their existing ownership semantics. Exit still stops/joins work before the optional postmortem phase. Send now never cancels the session or starts postmortem analysis.

## Acceptance and verification

Use channel/barrier-driven runner, provider, canonical-prompt and command-service fixtures. Use a small real local process test for process-tree termination and partial output. No real download, SSH target, developer provider, keychain or sudo is required.

Required scenarios:

1. Ordinary Enter remains queued; Send now wakes a blocked parent and continues with the same turn identity.
2. A long foreground local command emits output, receives explicit stop, settles its process tree, and returns that output/actual outcome before the next model request.
3. Await by child/job and any/all interruption cancels waiters only; targets and losing waiters' targets survive.
4. Detached jobs and independent children survive; detachment/completion versus stop is arbitrated atomically and reported truthfully.
5. Model-call/retry/stream interruption produces no provider-failure retry loop, leaked stream, executable partial call or whole-turn interrupt marker.
6. Queued steering before, during and after interruption is included once in FIFO order. Empty-text flush works only for an existing queue.
7. Duplicate submission IDs do not duplicate messages/stops; distinct submissions during one pending interruption all survive.
8. Gate/admission waits cancel promptly, release only acquired gates and never launch skipped work afterward.
9. Atomic mutation already committing finishes with truthful results; not-started calls launch nothing. Native/envelope/parallel histories retain all required call/result pairs before steering.
10. Pending parent approval, access, question, skill and sudo-auth waits cancel canonically with no grant/fabricated answer/sticky denial/secret leak. Late answers and cleanup cannot affect new requests.
11. An approval accepted before interruption retains its committed state; an unadmitted tool still does not run. Earlier cancellation prevents the grant.
12. Composer open/Esc has no execution effect; cancel/submit/resize preserve correct drafts/focus and unrelated panels' async updates. Secret inputs never become steering.
13. Completion before submission rejects stale turn identity and preserves text; accepted undelivered messages survive a subsequent stop/hard limit as undelivered without automatic new-turn execution.
14. Held sessions stay held and show waiting-for-resume. Unsupported SDD/swarm controllers and child views cannot redirect control accidentally.
15. TUI size/color/hidden-input checks and existing Ctrl+C, ordinary approval, access, child control and postmortem regressions pass.
16. ACP queued compatibility, now/status/event payloads, inactive/stale/unsupported errors, retry identity and application ordering match the shared runtime behavior.
17. Prompt prefix remains stable; user-steering interruption does not activate repetitive-failure coaching or alter execution policy.

Package tests must use testenv and explicit app/auth/config seams. Final planning will name tests and narrow commands per package, followed by full Go tests, vet and cgo build. No live-credential flag or integration endpoint is needed.

Baseline verification performed during spec authoring:

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/agent ./internal/app/session ./internal/app/tui ./internal/acp -run 'MidTurnSteering|InterruptedTurn|Steering|Steer|Hold' -count=1
```

All four package checks passed. These exercise existing steering, hold and full-turn interruption behavior; they do not validate the proposed Send now implementation. No full-suite test, production-code edit or commit was performed.

## Assumptions, integration and review boundary

The user approved the behavior and initial scope. The following conservative details make that design concrete: one shared FIFO, literal composer text, empty Send now flushing an existing queue, preserving hold, explicit ACP turn/request identities, no crash-durable queue, and truthful deferral for noncooperating tools. They do not authorize stopping background targets or starting replacement turns.

Implement against the merged background and access contracts. Reuse existing ownership/settlement services; reconcile shared files rather than implementing competing state. Exact Go declarations and package task boundaries belong in the subsequent execution plan.

Open questions: none.

The user approved the written specification. It was executed as the [Send now implementation plan](plans/2026-10-18-send-now-plan.md); see the implementation record below for the merged prerequisite and implementation commits and the verification checks.

## Implementation record

Implemented on branch `send-now`, from the source baseline above. Prerequisite branches were merged into this branch's history before it started:

- System-access approval: `system-access-approval` merged at `320c7a8f`.
- Reliable background execution: `acf8674c` ("reliable-background-execution: task 5 — Give explicit turn stops their own targets").

Send now implementation commits, all on `send-now`:

| Task | Commit |
| --- | --- |
| 1 | `72d1f09e` |
| 2 | `57501021` |
| 3 | `4f009c8c` |
| 4 | `a60ebef1` |
| 5 | `b89dd37c` |
| 6 | `787aa8bb` |
| 7 | `cfa0190e` |
| 8 | `38e02ab2` |
| 9 | `c4a068f8` |
| 10 | `3df96188` |
| 11 | `79dc0c74` |
| 12 | `6de654e7` (committed as "part 1") |
| 13 | `d3df08d1` |
| 14 | `566818bc` |
| 15 | `737c4241` |

The task-12 commit is recorded as "part 1"; its remaining `status.go` initializer and help tests are present and green.

Checks at `737c4241`:

- `gofmt -l internal/app/session internal/agent internal/tools/native internal/app/tui internal/acp internal/app` — no output.
- `go test -race ./internal/app/session ./internal/agent ./internal/agent/swarm ./internal/tools/native ./internal/acp` — all pass.
- `go test -race ./internal/app/tui ./internal/app/tui/help ./internal/app/tui/steerpanel ./internal/commands` — all pass.
- `CGO_ENABLED=1 go build ./cmd/marshal` — succeeds.
- Scripted usability suite (`./test/usability/... -run TestScripted`) — passes.
- `go test ./...` still fails only in four pre-existing, environmental packages unrelated to this feature: `internal/app`, `internal/snapshot`, `internal/tools/desktop/browser` and `internal/worktree`. A clean `git archive HEAD` export of the committed tree fails in exactly those same four packages (macOS `/tmp` versus `/private/tmp` symlink paths, a network fetch to `example.com`, and a git-identity fixture), so they are the known baseline and not regressions. Every package this feature touches passes, including under `-race`.
