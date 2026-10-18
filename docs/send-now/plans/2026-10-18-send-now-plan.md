# Send now — implementation plan

Date: 2026-10-18. Status: executed; implementation complete on branch `send-now` (see the [feature-spec implementation record](../feature-spec.md#implementation-record)).
Base: `de5e47bdc5e695d4e422e99059d27354a84dce14`.
Approved design: [feature-spec.md](../feature-spec.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md) and [work-decomposition](../../../internal/skills/builtin/work-decomposition.md).

**Goal:** interrupt the current operation, settle its actual effects and tool results, and deliver ordered steering inside the same normal conversation turn.

**Non-goals:** SDD/swarm controller steering, child steering by drill selection, stopping detached work, new execution deadlines, task cards/widgets, permission grants, runtime reload, automatic rollback/restart, durable unsent queues or a web UI redesign.

**Prerequisites:** execute after reliable background execution and system-access approval have landed. Their current checkout artifacts are [background plan](../../background-execution/plans/2026-10-17-reliable-background-execution-plan.md) and [access plan](../../system-access-approval/plans/2026-10-18-system-access-approval-plan.md). At authoring time they are submitted/in progress and their production APIs are not present at this base. Their prose anchors below are explicitly prerequisite APIs, not claims about current source.

The integration decisions are pinned: extend their single JobManager/execution service, use their actual execution identity type at its boundary, reuse session accessMu/canonical access settlement, and add an operation scope distinct from their turn-stop scope. Do not substitute StopAttached(turnID)/RequestTurnStop: those close future turn admission. If prerequisite spelling differs, adapt names/types at the boundary to the equivalent merged implementation; retain the Go contracts and behavior below. If a prerequisite is absent, do not execute a partial Send now implementation or recreate its service.

Execute all tasks sequentially. Run the task checks and commit before proceeding: `send-now: task N — <title>`. Most tasks share build units or files. No task is parallel-safe. Never commit unrelated untracked artifacts. Public spec/plan changes travel with implementation; plan authoring itself does not commit or edit production code.

## Contracts

Task 1 creates this **complete new file**, `internal/app/session/steering_types.go`. The declarations are shared by session, tools, agent, app and transports; they contain no provider/process/UI dependencies.

```go
package session

import (
    "context"
    "errors"
)

var ErrSendNow = errors.New("operation interrupted by user steering")

type OperationRef struct {
    SessionID string `json:"sessionId"`
    TurnID string `json:"turnId"`
    ID string `json:"operationId"`
}
type OperationKind string
const (
    OperationModel OperationKind = "model"
    OperationTools OperationKind = "tools"
    OperationSetup OperationKind = "setup"
)
type SteeringStatus string
const (
    SteeringAccepted SteeringStatus = "accepted"
    SteeringInterrupting SteeringStatus = "interrupting"
    SteeringWaitingSafe SteeringStatus = "waiting_safe_boundary"
    SteeringWaitingResume SteeringStatus = "waiting_for_resume"
    SteeringDelivered SteeringStatus = "delivered"
    SteeringUndelivered SteeringStatus = "undelivered"
)
type SteeringMessage struct {
    ID string
    Text string
}
type SteeringNote struct {
    ID string
    Text string
}
type SteeringBatch struct {
    Messages []SteeringMessage
    Notes []SteeringNote
}
type SendNowRequest struct {
    SessionID string `json:"sessionId"`
    TurnID string `json:"turnId"`
    RequestID string `json:"requestId"`
    Text string `json:"text,omitempty"`
}
type SteeringSnapshot struct {
    Version uint64 `json:"version"`
    TurnID string `json:"turnId"`
    Active bool `json:"active"`
    Supported bool `json:"supported"`
    Held bool `json:"held"`
    OperationID string `json:"operationId,omitempty"`
    OperationKind OperationKind `json:"operationKind,omitempty"`
    PendingCount int `json:"pendingCount"`
}
type SteeringOutcome struct {
    Kind string `json:"kind"`
    State string `json:"state"`
    ToolCallID string `json:"toolCallId,omitempty"`
    ExecutionID string `json:"executionId,omitempty"`
    AgentID string `json:"agentId,omitempty"`
    Summary string `json:"summary,omitempty"`
}
type SteeringReceipt struct {
    Version uint64 `json:"version"`
    RequestID string `json:"requestId"`
    TurnID string `json:"turnId"`
    Status SteeringStatus `json:"status"`
    OperationID string `json:"operationId,omitempty"`
    Outcomes []SteeringOutcome `json:"outcomes,omitempty"`
}

// Begin has already closed old-operation admission and requested stops
// before it returns. Wait joins those exact executions outside state locks.
type OperationStop struct {
    Wait func(context.Context) ([]SteeringOutcome, error)
}
type OperationInterrupt func(context.Context, OperationRef) (OperationStop, error)

type PromptKind string
const (
    PromptApproval PromptKind = "approval"
    PromptQuestion PromptKind = "question"
    PromptSkill PromptKind = "skill"
    PromptSudo PromptKind = "sudo"
)
type PromptKey struct {
    ID string
    Kind PromptKind
    Operation OperationRef
}
```

The remaining cross-task signatures are declarations to implement in their named tasks, not additional files to paste as stubs.

```go
// package session; Task 2, in existing steering.go
func (s *State) SetOperationInterrupt(handler OperationInterrupt)
func (s *State) BeginSteeringTurn(ctx context.Context) (string, error)
func (s *State) BeginSteeringOperation(ctx context.Context, turnID string, kind OperationKind) (context.Context, OperationRef, error)
func (s *State) FinishSteeringOperation(ref OperationRef, outcomes []SteeringOutcome) []SteeringOutcome
func (s *State) SubmitSendNow(request SendNowRequest) (SteeringReceipt, error)
func (s *State) SteeringSnapshot() SteeringSnapshot
func (s *State) StageSteering(turnID string) (SteeringBatch, error)
func (s *State) SteeringToSubmit(turnID string) SteeringBatch
func (s *State) MarkSteeringSubmitted(turnID string, ids []string)
func (s *State) HasImmediateSteering(turnID string) bool
func (s *State) TryFinishSteeringTurn(turnID string) bool
func (s *State) EndSteeringTurn(turnID, reason string)
func (s *State) DrainSteeringForInterrupt() []string

// Task 2 context helper file.
func OperationFromContext(ctx context.Context) (OperationRef, bool)
func SteeringRequested(ctx context.Context) bool
func CheckSteering(ctx context.Context) error
func IsSteeringCancellation(ctx context.Context, err error) bool
func SteeringTurnContext(ctx context.Context) context.Context
func BeginSteeringMutation(ctx context.Context) (func(), error)

// package session; Task 3, in existing pending.go.
func (s *State) RegisterApproval(ctx context.Context, pending *PendingToolCall) (PromptKey, error)
func (s *State) RegisterQuestion(ctx context.Context, pending *PendingQuestion) (PromptKey, error)
func (s *State) RegisterSkillGate(ctx context.Context, pending *PendingSkillGate) (PromptKey, error)
func (s *State) RegisterSudoAuth(ctx context.Context, pending *PendingSudoAuth) (PromptKey, error)
func (s *State) ResolveApproval(key PromptKey, decision UserApprovalDecision, apply func() error) (bool, error)
func (s *State) ResolveQuestion(key PromptKey, answers []Answer) bool
func (s *State) ResolveSkillGate(key PromptKey, choice SkillGateChoice, apply func() error) (bool, error)
func (s *State) ResolveSudoAuth(key PromptKey, decision SudoAuthDecision) bool
func (s *State) CancelPrompt(key PromptKey, cause error) bool
func (s *State) PromptCause(key PromptKey) error
func (s *State) PromptContext(key PromptKey) context.Context
func (s *State) ClearPrompt(key PromptKey)
func (s *State) CancelOperationPrompts(ref OperationRef)

// Task 4; package native, extending the prerequisite's manager.
func (m *JobManager) InterruptOperation(ctx context.Context, ref session.OperationRef) (session.OperationStop, error)

// Task 6; package agent; optional interface, existing WriteGate retained.
type ContextWriteGate interface {
    AcquireContext(context.Context) (func(), error)
}
func (r *Runner) recordSteeringOutcomes(ctx context.Context, toolCallID string, outcomes []session.SteeringOutcome)
func (r *Runner) takeSteeringOutcomes(ref session.OperationRef) []session.SteeringOutcome
// Task 7; package swarm.
func (l *WriteLock) AcquireContext(ctx context.Context) (func(), error)

// Task 11; package tui.
func WithSendNow(
    snapshot func() session.SteeringSnapshot,
    submit func(session.SendNowRequest) (session.SteeringReceipt, error),
) Option

// Task 14; package app.
func (rt *Runtime) SteeringSnapshot() session.SteeringSnapshot
func (rt *Runtime) SubmitSendNow(request session.SendNowRequest) (session.SteeringReceipt, error)
```

The composer declarations below are also cross-task contracts (implemented in Task 10 and consumed by Task 11):

```go
// package steerpanel
type SubmitMsg struct { ComposerID, SessionID, TurnID, Text string }
type CloseMsg struct { ComposerID string }
type Panel struct {
    id, sessionID, turnID string
    input textarea.Model
}
func New(sessionID, turnID string) *Panel
func (p *Panel) ID() string
func (p *Panel) Text() string
func (p *Panel) Update(msg tea.Msg) tea.Cmd
func (p *Panel) View(width, maxHeight int) string
func (p *Panel) Sizing() dock.Sizing
```

**State/locks:** add `steeringMu sync.Mutex` and private `steeringControl` in session.go. The queue is `[]SteeringMessage` under State.mu; preserve Push/Queue/Drain/Pop/Clear public string APIs. Ordering is prerequisite accessMu → steeringMu → State.mu, with workMu only briefly checked after acquiring accessMu. No inverse acquisition. Snapshot/event publication and callbacks happen outside these locks. Operation tokens hold an atomic requested flag so admission sees intent before ctx cancellation.

**Private controller record:** current turn ID/context/active/closing; current operation token; handler; request records keyed by request ID; in-flight staged messages/notes in FIFO order; submitted/archived IDs; revision counter. Each request record stores its original target/text fingerprint, covered message IDs, receipt and captured operation. No persisted queue/table. Each operation token owns its own cancel, interrupt-once and settlement channel; cleanup compares exact ref.

**Pending additions:** all four pending prompt structs gain exported `Key PromptKey` and a private canonical responder reference shared by event copies. UserApprovalDecision gains `Canceled bool`. For registered prompts, Respond delegates to the canonical resolver; display/event copies have no response channel. Unregistered legacy fixtures retain their current Respond behavior. Preserve the access plan's typed Access metadata/result and its guarded access response path.

**Events:** replace the existing SteeringEvent declaration with this complete declaration, and add the constants below to session.go. Add fields `Steering *SteeringSnapshot` and `SteeringReceipt *SteeringReceipt` to the existing Event union. Update Event's comment: registered prompt snapshots carry a canonical responder/key, never a response channel. Every published value is a deep snapshot. Increment Version under steeringMu; clients compare snapshot revisions and receipt revisions per request ID, not one global revision that could discard another request's final outcome.

```go
type SteeringEvent struct {
    QueueLen int
    Message string
    Snapshot *SteeringSnapshot
    Receipt *SteeringReceipt
}
const (
    EventSteeringState = "steering_state"
    EventSteeringReceipt = "steering_receipt"
)
```

**Scope:** add opt-in `EnableSteering bool` to agent.Runner; default false. Only app's normal root/general runner enables it. Existing direct/shared-state runner tests and child/controller/swarm runners therefore retain their current lifecycle. TUI additionally requires busy and a supported active snapshot. ACP knows controller activity from activeTurns and reports unsupported there; it never gives a controller a Send now operation.

**Outcome vocabulary:** Kind is model/tool/command/approval/question/skill/sudo; State is not_started/completed/interrupted/continues/unknown. Preserve the command service's real exit/stop data in normal tool results. Receipt summaries contain IDs and bounded context, not duplicated raw output or secrets.

## Task 1: Preserve queued steering through a typed FIFO

**Goal:** steering has stable internal message IDs while all existing queued-send callers remain source compatible.

**Files:** new `internal/app/session/steering_types.go` (complete code in Contracts); existing `session.go`, `steering.go` and `session_test.go` in that package.

**Steps**

1. Create steering_types.go verbatim from Contracts. Change State.steeringQueue to []SteeringMessage. In PushSteering generate an ID with crypto/rand.Text, append text once under State.mu and retain the current broker notification.
2. Rewrite SteeringQueue/DrainSteering/PopSteering as ordered string projections of the typed entries. Keep current ClearSteering semantics for this task; Task 2 protects accepted immediate entries. Do not change subagent/watch report queues.
3. Add existing-file tests: `TestSteeringTypedFIFOCompatibility` verifies Push/Queue/Drain/Pop order and copied slices; `TestSteeringMessageIDsAreUnique` verifies equal text gets distinct IDs; `TestSteeringClearDoesNotTouchReports` verifies independent report queues survive.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/session -run 'Steering|Interrupted' -count=1
```

Expected: legacy queue behavior passes with one underlying FIFO and no schema/persistence change.

## Task 2: Add operation ownership, request receipts and interruption arbitration

**Goal:** session state accepts Send now atomically, interrupts a captured operation once, and tracks submission separately from acceptance.

**Files:** `internal/app/session/steering.go`, `session.go`, `hold.go`, `session_test.go`; new `steering_context.go` below.

**Steps**

1. Add the private controller fields and events from Contracts. Keep the existing broker; implement private `publishSteeringState(receipt *SteeringReceipt)` by capturing copies under locks and publishing outside them to both brokers. Suppress identical snapshots except for genuine receipt transitions. Update SetHold to publish a steering snapshot after releasing State.mu.
2. Implement BeginSteeringTurn: reject a second active eligible turn/quiescing state; allocate opaque ID and initialize fresh request/in-flight ledgers. Keep existing queued text. Supported is true only when an OperationInterrupt handler is installed; tests explicitly inject a no-op handler. BeginSteeringOperation rejects the wrong/closing turn, pending immediate steering, or an existing unjoined operation. It creates a token/context with context.WithCancelCause and a fresh operation ID.
3. Implement SubmitSendNow under accessMu → steeringMu → State.mu:
   - Validate active/supported/not closing, exact session/turn and nonempty request ID. Old turn IDs reject after closure, even for a previously accepted request. Final receipt events remain observable before the stream closes.
   - For an existing ID in this active turn, identical target/text returns a deep copy of its current receipt before checking queue emptiness; a retried flush remains idempotent after its queue was consumed. Different target/text for that ID errors. For a new ID require nonblank text or a nonempty existing queue. Whitespace-only text is a flush, never a new empty entry. Failed validation changes nothing.
   - Append new text once, record the IDs of queued messages covered by this request, capture the current token or checkpoint, set requested on that token, and publish accepted receipt/pending snapshot. Setting intent under accessMu makes it visible to canonical access/prompt claims before later cancellation signaling.
   - Start one interruption worker per captured operation, outside locks; subsequent requests cover their own queue frontier and share that worker. At a checkpoint, wake it without inventing an interrupted operation.
4. The worker invokes the installed handler using the original turn context. Its Begin closes old-operation command admission/requests stops and returns an OperationStop with nonnil Wait. Then cancel matching prompts and call token.cancel(ErrSendNow); then Wait outside locks. Merge real outcomes and close the token's interruption-settlement channel. Handler errors produce unknown/waiting-safe information; do not claim a stop or release conflicting work before the original operation actually settles.
5. FinishSteeringOperation waits for its interruption worker when one exists, then merges callback/tool outcomes and clears only that exact token. Build one safe UTF-8 interruption note ≤2048 bytes, no raw output; name surviving relevant jobs/children and truncation. Queue the note for the checkpoint. Ordinary finish with no steering creates no interruption note. Clear only the current token; late old completion cannot clear a replacement.
6. StageSteering moves new queued entries/notes into an ordered in-flight ledger and returns them once. It does not mark delivered or persist transcript messages. SteeringToSubmit returns all unsubmitted staged entries in order. MarkSteeringSubmitted marks only supplied IDs and advances a request to delivered when all its covered message IDs were submitted. Retries are idempotent.
7. At closure, TryFinishSteeringTurn atomically succeeds only with no accepted deliverable immediate work; it marks the turn closing so new requests reject before final publication. EndSteeringTurn disarms old callbacks/immediate flags, marks remaining receipts undelivered, and retains unsubmitted text as ordinary FIFO follow-up content. DrainSteeringForInterrupt returns queued plus staged unsubmitted text once, marking it archived so the existing interruption marker and follow-up queue do not duplicate it.
8. ClearSteering removes ordinary queued entries, but preserves entries protected by accepted immediate requests; full-turn cleanup uses EndSteeringTurn/DrainSteeringForInterrupt. Already submitted entries are never requeued. Staging is deliberately ephemeral until model submission, avoiding a persisted-history plus follow-up-queue duplicate.
9. Add private `beginSteeringMutation(ctx context.Context) (func(), error)`: under accessMu → steeringMu reject requested/closed scope; count the admitted commit unit and return idempotent release. Interruption during an admitted unit produces waiting_safe_boundary; no lock remains held during file I/O. Release only that unit and leave full-turn cancellation effective between units.
10. Create steering_context.go with this complete file:

```go
package session

import (
    "context"
    "errors"
    "sync"
    "sync/atomic"
)

type steeringContextKey struct{}
type steeringOperation struct {
    owner *State
    ref OperationRef
    kind OperationKind
    parent context.Context
    cancel context.CancelCauseFunc
    requested atomic.Bool
    interruptOnce sync.Once
    settled chan struct{}
}

func OperationFromContext(ctx context.Context) (OperationRef, bool) {
    if ctx == nil { return OperationRef{}, false }
    op, ok := ctx.Value(steeringContextKey{}).(*steeringOperation)
    if !ok || op == nil { return OperationRef{}, false }
    return op.ref, true
}
func SteeringRequested(ctx context.Context) bool {
    if ctx == nil { return false }
    op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
    return op != nil && op.requested.Load()
}
func CheckSteering(ctx context.Context) error {
    if ctx == nil { return nil }
    if err := SteeringTurnContext(ctx).Err(); err != nil { return err }
    if SteeringRequested(ctx) { return ErrSendNow }
    return ctx.Err()
}
func IsSteeringCancellation(ctx context.Context, err error) bool {
    if err == nil || ctx == nil || SteeringTurnContext(ctx).Err() != nil { return false }
    return SteeringRequested(ctx) &&
        (errors.Is(err, ErrSendNow) || errors.Is(err, context.Canceled))
}
func SteeringTurnContext(ctx context.Context) context.Context {
    if ctx == nil { return context.Background() }
    op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
    if op != nil && op.parent != nil { return op.parent }
    return ctx
}
func BeginSteeringMutation(ctx context.Context) (func(), error) {
    if ctx == nil { return func(){}, nil }
    op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
    if op == nil || op.owner == nil {
        if err := ctx.Err(); err != nil { return nil, err }
        return func(){}, nil
    }
    return op.owner.beginSteeringMutation(ctx)
}
```

11. Add tests to session_test.go:
    - `TestSendNowAcceptanceFIFOAndDuplicateIDs`: queued/flush/new messages have stable order; active-turn duplicates return the latest receipt without appending/stopping twice even after a flush drains its queue; conflicting reuse and post-closure retries reject.
    - `TestSendNowIntentPrecedesCancellationSignal`: block the handler's Begin and observe requested flag immediately; no new operation/admission passes.
    - `TestSendNowCoalescesCapturedOperationOnly`: two requests stop once; delayed cleanup cannot cancel the next operation.
    - `TestSendNowStageSubmitAndRetryExactlyOnce`: staged text remains undelivered until Mark; retry does not re-stage/re-persist it.
    - `TestSendNowClosureRetainsUnsubmittedWithoutMarkerDuplicate`: archived marker entries are not requeued; other unsent text remains ordinary follow-up.
    - `TestSendNowCompletionAdmissionRace`: TryFinish vs Submit has one winner; accepted work prevents ordinary finalization.
    - `TestSendNowHoldAndCommitBoundary`: hold stays set; an admitted mutation finishes, later units are rejected, release is idempotent.
    - `TestSendNowContextCausePreservesRealErrors`: root cancel and real command/network failures are not relabeled steering.
    - `TestSendNowInvalidTargetAndUnsupportedAreNoOps`: stale/session/empty/closing/nil-handler errors preserve queue and execution.
    - `TestSendNowEventsAreIndependentSnapshots`: mutate received slices; state unaffected, versions advance and no secret/output text appears in receipts.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session -run 'SendNow|Steering|Hold' -count=1
```

Expected: deterministic barrier tests pass; no callback or I/O runs under session locks.

## Task 3: Settle parent-operation prompts canonically

**Goal:** cancellation/answers race once, preserve accepted grants and cannot answer or clear a newer prompt.

**Files:** `internal/app/session/pending.go`, `session.go`, `pending_test.go`, `sudoauth_test.go`, `skillgate_test.go`.

**Steps**

1. Add Key/shared canonical responder to the four pending structs. Implement the Register/Resolve/Cancel/Cause/Clear methods from Contracts in pending.go. Registration generates an opaque ID and captures OperationFromContext; reject occupied matching slots and an already requested context before publishing. Child/non-steering contexts still get canonical IDs but never attach to the parent's operation.
2. Store a private record per key until its operation completes (or explicit standalone cleanup): canonical pointer, original context, unclaimed/answer-claimed/canceled/settled state, done channel and terminal cause. Event copies share this responder/key and omit ResponseChan. Registered Respond routes by key; it never uses a copied sync.Once/channel. Preserve unregistered legacy fixtures.
3. Under accessMu arbitrate each response claim against SteeringRequested on its captured token. Mark the winner before releasing locks. Invoke an optional approval/skill apply callback outside locks, then deliver once to the canonical buffered channel and close it. Apply failure delivers nonapproved with its cause. An already-claimed callback completes before cancellation may return from CancelPrompt; cancellation never rolls back its accepted grant.
4. CancelPrompt compares key and claims only unclaimed records; it sends nonapproved/empty-compatible responses with a distinct cause. If an answer claim is in progress, wait outside state locks for its result. PromptCause retains the actual cause so a waiter distinguishes steering from denial/Unanswered. PromptContext returns the recorded original context; an expired key returns an already-canceled context so it cannot start a late RPC. ClearPrompt compare-clears that slot, never whichever prompt is now pending.
5. CancelOperationPrompts selects only exact OperationRef matches, including skill/sudo prompts, and waits for accepted response claims outside locks. It grants no authority, creates no sticky skill denial and zeroes canceled sudo material through existing cleanup. Never invoke ResolvePendingForShutdown or touch a child's independent scope.
6. In prerequisite RegisterAccess retain its original context and record the operation ref plus a PromptKey whose ID is its opaque access request ID. Mirror its original context/completion in the prompt registry; do not introduce a second settlement claim. CancelPrompt/CancelOperationPrompts delegate access keys to the existing CancelAccess path, outside any nonreentrant accessMu hold. Its canonical finish updates the mirrored record. In ResolveAccess check SteeringRequested while holding its existing accessMu, before application acceptance; SubmitSendNow takes the same gate. Existing accepted application still finishes. This is the shared cancellation-intent boundary, not a second access resolver.
7. In ResolvePendingForShutdown and generic SetPending* compatibility wrappers, use canonical compare/settlement for registered prompts; retain existing legacy behavior for unregistered calls. Control-plane cleanup never clears accepted steering. Release retained prompt records only after their waiters/operation join.
8. Add tests:
   - `TestPromptSteeringCancellationWinsBeforeGrant`: cancellation intent with delayed signal prevents approval/skill apply.
   - `TestPromptAcceptedApplyFinishesBeforeCancelReturns`: barrier proves accepted grant survives and waiter receives actual result.
   - `TestPromptLateEventCopyCannotAnswerNewRequest`: old copy Respond/Clear cannot close/send a newer channel.
   - `TestPromptCancelKindsHaveDistinctCause`: approval/question/skill/sudo each unblock once; no fabricated answer/sticky deny.
   - `TestPromptCancelPreservesIndependentChild`: matching parent requests cancel, child remains pending.
   - `TestPromptSteeringSudoCleanupHasNoSecretSnapshot`: response/event/receipt never expose retained password bytes.
   - `TestAccessSteeringClaimSharesCanonicalGate`: exercise both sides of application-acceptance race against access resolver.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session -run 'Prompt|Pending|Access|Sudo|SkillGate|SendNow' -count=1
```

Expected: canonical settlement once; access/ordinary legacy regressions pass without a new prompt queue.

## Task 4: Stop only commands attached to the interrupted operation

**Goal:** the existing execution service arbitrates stop versus detach without closing the turn's future admission.

**Files:** `internal/tools/native/jobs_manager.go`, `jobs_test.go`, `jobs_wait_test.go`; prerequisite launch/wait implementation in this same package.

**Steps**

1. Extend the prerequisite LaunchSpec and internal execution record with `Operation session.OperationRef` captured from OperationFromContext at launch. Zero ref means no Send now target. Keep the prerequisite's conversation/actor/turn/step/call provenance, generation and capture-on-launch settings.
2. Add a closed-operation set keyed by complete OperationRef at the service's existing admission/detachment mutex. Launch refuses a closed operation even if its original turn remains open. A new operation in that same turn can launch normally. Remove retired scope bookkeeping only after no launch/wait/receipt can still reference it.
3. Implement InterruptOperation from Contracts: under that same boundary, close old-operation admission and select matching executions still attached. A natural terminal result is completed; a detached execution is continues and is never stopped. Mark selected executions with explicit steering stop cause before invoking backend cancellation outside locks. Do not implement snapshot-then-RequestStop, which races detachment.
4. Return OperationStop with a nonnil Wait even for zero selected commands. Wait joins the captured set and returns actual command/continues outcomes with full execution IDs; backend stop errors stay unknown/waiting until the original command actually settles. Root turn cancellation remains effective during join. No timeout converts an unknown running command into stopped.
5. In the prerequisite foreground wait/handoff path, a steering stop already accepted prevents timeout-driven detachment. If detachment won first, return normal continuation and report continues. Terminal stdout/stderr/exit/stop cause still use the existing result/output pipeline exactly once.
6. Add tests:
   - `TestInterruptOperationStopsAttachedAndPreservesDetached`: exact scope matching; other children/turns unchanged.
   - `TestInterruptOperationAllowsNextOperationSameTurn`: old scope rejects admission, new one succeeds; turn-stop flag stays false.
   - `TestInterruptOperationHandoffAndCompletionRace`: barrier arbitration yields one terminal/continues outcome without duplicate launch/completion.
   - `TestInterruptOperationRepeatedStopAndBackendFailure`: one stop request, unknown remains pending until actual exit.
   - `TestInterruptOperationRealProcessTreeAndOutput`: test-owned local shell prints a marker then blocks with a child; explicit steering stop joins the group and preserves the marker, without real downloads/SSH.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/tools/native -run 'InterruptOperation|JobManager|WaitForeground' -count=1
```

Expected: one supervisor, no timer kill, no stop of already-detached work, and future same-turn launches remain possible.

## Task 5: Adapt native prompts, shell waits and file commit units

**Goal:** native tools honor interruption intent before admission and report actual completed/partial effects.

**Files:** `internal/tools/native/question.go`, `sudo.go`, `file.go`, `mode_request.go`, `question_test.go`, `sudo_test.go`, `file_test.go`, `jobs_wait_test.go`; ordinary shell handler in this package's prerequisite implementation.

**Steps**

1. In questionAskTool's handler remove the local questionStore adapter and replace SetPendingQuestion/unconditional clear with t.sessionState.RegisterQuestion/ClearPrompt. Replace the unconditional channel receive with a response/ctx select. On ctx.Done call CancelPrompt and wait for a previously accepted response claim; return PromptCause when canceled rather than recording a fabricated answer. Preserve actual answers that won. In question_test.go replace the two raw pq.ResponseChan sends with pq.Respond using the identical answer values; registered prompts need canonical settlement.
2. In sudo.go's `requestSudoPassword`, register the prompt/key and cancel canonically. Zero password buffers on cancellation and recheck CheckSteering before privileged dispatch. Retain the existing password-on-stdin/no-PTY backend; this task does not create managed background sudo.
3. In prerequisite mode.request keep its typed canonical result and context; a canceled result caused by steering returns ErrSendNow, not a false denial or full-turn cancellation. Do not apply access in the tool.
4. In ordinary shell launch, check CheckSteering before Launch and capture Operation into LaunchSpec. When the foreground wait ends for steering, wait for the captured execution's real settlement using SteeringTurnContext(ctx), not the canceled operation ctx and not context.Background. Service interruption was requested before ctx signaling. Preserve actual partial result; if handoff already won, return continuation and its identity.
5. In file.go `applyFilePatches`, before each actual-file commit unit call BeginSteeringMutation. After admission, finish that file's write, tracker/backup/diff result recording and release the unit exactly once. Check again before the next file. In file.write's corresponding os.WriteFile path do the same. Never use context.WithoutCancel around an entire multi-file tool.
6. Refactor applyFilePatches' result construction into private `patchResult(diffs []string, backups []session.BackupFile, symbols []registry.SymbolRef, warning string) registry.ToolResult` using the existing final formatting. On interruption between committed files, return that nonempty truthful partial result plus ErrSendNow; store the committed backups. Avoid speculative backups for files never written. File-edit/write successes that finished remain successes.
7. Add tests:
   - `TestQuestionSteeringCancelAndAnswerRace`: actual winning answer vs distinct cancellation cause, no late clear.
   - `TestSudoSteeringCancelBeforeDispatchZerosSecret`: fake privileged runner is never invoked; no credential escapes.
   - `TestShellSteeringWaitReturnsActualStoppedOutput`: service outcome arrives before the handler's result; no detached-job mislabel.
   - `TestFilePatchSteeringBetweenUnitsKeepsCommittedEvidence`: first file committed with backup/diff, second unchanged, partial result retained.
   - `TestFileWriteSteeringBeforeAndDuringCommit`: no write before admission, completed write preserved if interruption follows admission.
   - `TestModeRequestSteeringNeverReportsGrant`: canceled original operation cannot grant or misreport authority.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/tools/native -run 'Steering|ModeRequest|Question|Sudo|FilePatch|FileWrite' -count=1
```

Expected: actual effects survive in results, and prompts/mutations do not start after cancellation intent.

## Task 6: Preserve complete tool histories across interruption

**Goal:** agent admission/batches stop stale work without losing started results or inventing tool failures.

**Files:** `internal/agent/runner.go`, `execute.go`, `requests.go`, `snapshot_warning.go`, `subagent_async.go`, `requests_test.go`, `runner_parallel_test.go`, `interrupt_test.go`, `approval_parallel_test.go`, `await_extend_test.go`, `subagent_async_test.go`; existing response fixtures in `step_test.go`, `runner_skillgate_test.go`, `runner_askuser_test.go`, `runner_testhelpers_test.go`, `runner_parse_test.go`, `runner_misc_test.go` and `runner_approval_test.go`.

**Steps**

1. Add optional ContextWriteGate from Contracts beside existing WriteGate. Add private `acquireToolGate(ctx context.Context) (func(), error)`: use AcquireContext when implemented; otherwise preserve legacy Acquire but check CheckSteering immediately after acquiring and release on cancellation. Production's real gate becomes cancelable in Task 7; legacy fake gates can honestly remain waiting-safe.
2. In executeToolCall check CheckSteering before validation/hook/policy admission, after any human approval/hook rewrite, before snapshot/gate and immediately before Handler. A requested operation returns a structured not_started result for an unadmitted call. Skip failure tracker/stall/todo-failure coaching for intentional steering. Continue normal handling for genuine errors that won the race.
3. Add private `steeringToolResult(action ModelAction, result registry.ToolResult, started bool, cause error) []schema.ChatMessage`. Preserve nonempty result content/files/exit/sandbox/notice from partial execution; append a bounded interruption explanation and use ToolNotice Kind `steering_interruption` with Data operationId/state. Stamp normal toolCallID/step identity and one audit per admitted/skipped call, but do not cache partial/interrupted output or count it as a failed-repeat observation. Actual successful completion remains completed even if intent arrives immediately afterward. Add the two outcome helpers in Contracts backed by a private Runner mutex/map keyed by complete OperationRef: record one tool outcome plus structured target outcomes from the notice, stamping the call ID; take removes only that operation's copied slice after its workers join. Call unknown/plugin/MCP handlers directly and wait for their return; do not abandon a goroutine on ctx.Done. Locally settled cancellation with unconfirmed remote effects is unknown, never a claim that remote work stopped, and is not automatically retried.
4. In snapshotCapture suppress only an operation-steering cancellation warning, not a real capture failure/root cancellation. Retain pre-write protection for every mutation that actually launches.
5. In requestApproval/requestQuestions/requestSkillGate use Task 3 registration, cause checks and compare-clear. On canceled context settle by key; wait for an already-accepted response claim. In skillLoadGate propagate steering before ordinary denial/Unanswered/sticky skill changes; requestSkillGate must return the distinct cause rather than a fabricated SkillGateDeny. After an approved response, recheck admission before tool launch.
6. Refactor executeNativeToolCalls/executeActions to retain an ordered result slot for every declared call. Check intent before each serial launch, before acquiring the parallel semaphore and again after acquiring it. Use select on ctx.Done for semaphore waiting. Fill skipped slots with not_started; join already-started workers. Never return early on steering with a partially populated result list.
7. In executeNativeQuestionAsk preserve its call pairing while distinguishing actual human answers from canceled questions. In the concurrent agent.run dispatch path keep successfully returned child handles; canceled dispatch must not cancel independent children or orphan their result slot.

   In subagent_async.go's NewSubagentAwaitTool, handle IsSteeringCancellation before ordinary timeout/error conversion in every single-child, single-job, any and all return branch. Add private `func (a awaitOptions) awaitSteeringResult(ctx context.Context, state *session.State, partial registry.ToolResult) (registry.ToolResult, error)`: preserve completed partial Summary/Content, rescan outstanding targets with scanPending, append bounded IDs/current status, and return ErrSendNow with a steering_interruption notice whose Data["outcomes"] is `[]session.SteeringOutcome`. Surviving children use Kind tool/AgentID; jobs use Kind command/ExecutionID; State is continues. The waiter is interrupted, not the targets. Cancel/join any-mode waiter goroutines through their waiter context before return; never call KillJob/CancelSubagent/turn stop. This extends the prerequisite's lifetime-safe wait, without replacing it.
8. In the listed existing response fixtures, replace raw pending.ResponseChan sends with that pending object's Respond using the identical decision/answer value (including multiline literals). These are mechanical registered-prompt fixture updates; keep direct-channel tests only where they deliberately construct an unregistered legacy pending. In runner_skillgate_test.go the receiver is sg, question fixtures use q, and approval fixtures use pending/tc/state.PendingApproval().
9. Add tests:
   - `TestSteeringNativeBatchHasResultForEveryCall` and `TestSteeringEnvelopeBatchHasResultForEveryCall`: started/complete/skipped slots keep ID/order and no stale launch.
   - `TestSteeringPartialToolErrorPreservesEvidence`: nonempty result plus ErrSendNow retains output/files and avoids failed-repeat escalation.
   - `TestSteeringApprovalWinnerStillCannotLaunchStaleTool`: approval claim wins but interruption wins later admission.
   - `TestSteeringParallelSemaphoreStopsUnlaunchedCalls`: workers admitted before intent settle; pending slots never launch.
   - `TestSteeringAgentRunDispatchKeepsIndependentHandles`: children started before interruption remain running and reported once.
   - `TestSteeringAwaitModesPreserveTargetsAndPartialResults`: single-child/single-job/any/all cancel their waiters, retain collected results and report surviving IDs with zero target-stop calls.
   - `TestSteeringSnapshotCancelDoesNotWarnAsFailure`: actual capture errors still warn; steering cancellation does not.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/agent -run 'Steering|Approval|Parallel|Interrupted|Snapshot' -count=1
```

Expected: complete native/envelope histories and no spurious failure coaching.

## Task 7: Make the production write gate cancelable

**Goal:** waiting for the shared write token responds to cancellation without leaking a token or changing serialization.

**Files:** `internal/agent/swarm/lock.go`, `lock_test.go`.

**Steps**

1. Replace lock.go with this complete file. Zero-value use and existing Acquire/release callers remain supported.

```go
package swarm

import (
    "context"
    "sync"
)

type WriteLock struct {
    init sync.Once
    token chan struct{}
}
func (l *WriteLock) initialize() {
    l.init.Do(func() {
        l.token = make(chan struct{}, 1)
        l.token <- struct{}{}
    })
}
func (l *WriteLock) Acquire() func() {
    release, _ := l.AcquireContext(context.Background())
    return release
}
func (l *WriteLock) AcquireContext(ctx context.Context) (func(), error) {
    if ctx == nil { ctx = context.Background() }
    l.initialize()
    if err := ctx.Err(); err != nil { return nil, err }
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-l.token:
    }
    var once sync.Once
    release := func() { once.Do(func(){ l.token <- struct{}{} }) }
    if err := ctx.Err(); err != nil { release(); return nil, err }
    return release, nil
}
```

2. Add tests to lock_test.go: `TestWriteLockCanceledWaitDoesNotAcquire` (barrier with token held); `TestWriteLockCancelAcquireRaceDoesNotLeakToken` (repeated bounded races); `TestWriteLockMixedLegacyAndContextSerialization` (maximum one holder); `TestWriteLockReleaseIsIdempotent` (double release still one token). Keep existing lock tests.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/agent/swarm -run 'WriteLock|Lock' -count=1
```

Expected: cancellation wakes blocked admission and legacy safety remains intact.

## Task 8: Cancel logical model calls without retry or salvage

**Goal:** a model-operation interruption cleans up its stream and returns a distinct signal to the same turn.

**Files:** `internal/agent/chat.go`, `runner.go`, `chat_internal_test.go`, `chat_retry_test.go`, `chat_thinkloop_test.go`.

**Steps**

1. Add private Runner field `steeringTurnID string` for the current eligible invocation. CopyFrom preserves the destination's live ID rather than copying the source's field; reload cannot overwrite an in-flight invocation. Add `withModelOperation(ctx context.Context, run func(context.Context) (chatResult, error)) (chatResult, error)`: reuse an existing matching model/setup operation; otherwise begin/finish OperationModel when an eligible turn is registered. No eligible turn leaves existing behavior unchanged.
2. Wrap the entire chatWithRetryWithNativeTools logical call, including connectivity retries/backoff, in that helper. An operation is not replaced for every retry. Distinguish nested wrapper calls by OperationFromContext so they do not register a second simultaneous operation.
3. At retry, fallback/no-tools and waitForConnectivity branches, test IsSteeringCancellation before retry/salvage logic and return ErrSendNow with no executable Text/ToolCalls. Preserve known Usage/inspection evidence. Root turn cancellation and genuine provider/timeout/loop errors retain their current classification.
4. In chatOnceAttempt check CheckSteering before dispatch. Immediately before `p.Chat(ctx, req)` call private `commitSteeringSubmission()` (implemented here using State.SteeringToSubmit): persist staged interruption notes as existing RoleUser/ContentTypeSubagentReport and staged messages with the existing mid-turn steering envelope/ContentTypeSteering, then MarkSteeringSubmitted for their IDs. Do not persist at StageSteering; otherwise an undelivered entry could exist both in replay history and the follow-up queue. Retries find no unsubmitted IDs and persist nothing twice.
5. On context cancellation drain provider events using the existing request-body/stream-close contract; never break an unbuffered sender and leak it. On return with steering cause, discard partial response/tool calls before the caller's salvage path. Keep captured usage/inspection and close the open attempt normally.
6. Add tests:
   - `TestSteeringCancelsModelStreamWithoutRetryOrSalvage`: test-owned stream ends via ctx, no text/tools salvaged and one cleanup.
   - `TestSteeringStopsConnectivityAndFallbackRetries`: no retry ladder after explicit steering.
   - `TestSteeringModelUsageAndInspectionAreRetained`: known usage/canceled inspection remain recorded.
   - `TestSteeringSubmissionPersistsAndAcknowledgesOnce`: real chat dispatch marks receipt delivered; retries do not duplicate persisted steering.
   - `TestSteeringRealProviderFailureKeepsItsCause`: injected failure before cancellation does not become a user interruption.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/agent -run 'Steering.*Model|Steering.*Retry|Steering.*Submission|Connectivity|ThinkingLoop' -count=1
```

Expected: no hanging stream, retry storm, executable partial call or duplicate history entry.

## Task 9: Resume at a steering checkpoint inside the same turn

**Goal:** startup, tool execution, model calls and finalization yield to steering without a second RunTask invocation.

**Files:** `internal/agent/runner.go`, `finalize.go`, `interrupt_test.go`, `runner_parallel_test.go`, `chat_internal_test.go`.

**Steps**

1. Add opt-in Runner.EnableSteering, default false. After RunTask's RunTaskFunc injection guard, enabled root/general runners (role general, State.SubagentDepth()==0, no inherited eligible operation) call BeginSteeringTurn and set steeringTurnID. Defer EndSteeringTurn/field reset; do not start eligible controls in direct legacy/shared-state tests or child/SDD/swarm runners. New feature fixtures explicitly enable it and install an interrupt handler. CopyFrom preserves the live invocation's enabled flag/steeringTurnID.
2. Add private `steeringCheckpoint(ctx context.Context, messages []schema.ChatMessage) ([]schema.ChatMessage, bool, error)`. Respect WaitUnheld with the real turn context, then StageSteering and append note/user envelopes after existing tool results, processing raw @file pins through existing helpers. Return whether user steering arrived for the doom-loop guard. Do not add a goal, reset budgets, increment turn index or emit a full-turn interrupt marker.
3. Replace loop-top DrainSteering with that checkpoint, before ordinary completion decisions. Keep genuine hard budget limits: if they prevent another request, EndSteeringTurn marks undelivered and retains text. Accepted messages are persisted only at actual adapter submission in Task 8.
4. Wrap each native/envelope/single-tool dispatch batch in BeginSteeringOperation(OperationTools), passing that derived context to all calls. Always finish/join the batch, pass takeSteeringOutcomes(ref) to FinishSteeringOperation, and append its complete result list before checking the distinct steering signal and continuing the loop. Merge duplicate command/target outcomes by kind plus stable identity, retaining actual terminal service results over a prior continues snapshot. Keep original turn context for full-turn stop.
5. Place optional TitleManager.OnUserTurn/Classifier calls and turn-start snapshot under short OperationSetup scopes. If interrupted, keep the existing title/local Classify fallback and continue initialization once; do not republish the goal or increment the turn twice. Pending steering blocks further optional old-intent model work until the initial messages/checkpoint exist.
6. For PlanFirst, keep its planMessages slice append-only. On ErrSendNow checkpoint into that slice and retry planning with appended steering; carry the same newly appended steering/note tail into main messages once when planning completes, without re-persisting it. Other r.chatWithRetry callers (main request, finalize/handoff) return the same signal to the controlling loop; intercept it before failTurn or partial-salvage handling.
7. Add private `commitSteeringFinal(publish func()) bool`: with no eligible turn, publish normally; otherwise TryFinishSteeringTurn is the atomic final-admission check. Invoke publish only after it succeeds, outside state locks. Use it at all five current final publication sites: runner.go's two AddMessageFinalWithUsage, two AddMessageSalvaged, and finalize.go's AddMessageSalvaged. If it refuses due to accepted immediate work, reset the provisional Task.Status to executing and clear its provisional Summary, then return/continue via the checkpoint instead of publishing a stale answer. Finalize returns ErrSendNow to its RunTask caller in that case.
8. In recordInterruptMarker use DrainSteeringForInterrupt so staged/queued undelivered feedback appears once in the existing marker. Do not call this marker for operation-only steering. TUI cleanup later preserves unarchived accepted entries.
9. Add tests:
   - `TestSendNowContinuesSameTurnThroughShellAndAwait`: same goal/turn index, complete tool results then steering; next same-turn command can run.
   - `TestSendNowStartupAndPlanFirstDoNotDuplicateGoal`: title/classifier/planning interruption keeps one goal, append-only histories and one persistence of each steering item.
   - `TestSendNowFinalAnswerRaceRespectsAcceptedMessage`: gate barriers prove either rejection with draft retained or steering before final, never silent loss.
   - `TestSendNowFullStopAndHardBudgetRetainUndelivered`: no automatic replacement turn; marker/queue do not duplicate text.
   - `TestSendNowHeldTurnWaitsForExplicitResume`: cancel current operation, keep held flag, then submit messages only on resume.
   - `TestSendNowUnsupportedAndChildRunnersDoNotRegisterParentControl`: no eligible controller/child hijack.
   - `TestSendNowPromptPrefixAndToolPairing`: native/envelope/provider request snapshots preserve earlier prefix, all call IDs paired before steering.
10. Re-run existing InterruptedTurn, mid-turn steering, native/envelope, plan-first, finalize and prefix-stability regressions. No new provider service or prompt-prefix rewrite is permitted.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/agent -run 'SendNow|MidTurnSteering|InterruptedTurn|PlanFirst|Finalize|PromptPrefix|Parallel' -count=1
```

Expected: one running turn, truthful complete histories and no unrequested restart.

## Task 10: Add the temporary steering composer

**Goal:** a small dock component collects literal steering without reading another surface's input or changing execution.

**Files:** new `internal/app/tui/steerpanel/steerpanel.go` and `steerpanel_test.go`.

**Steps**

1. Create steerpanel.go with this complete file. It emits a bound message only; the parent performs validation/acceptance. Its ID distinguishes late messages from another composer.

```go
package steerpanel

import (
    "crypto/rand"
    "strings"

    "charm.land/bubbles/v2/textarea"
    tea "charm.land/bubbletea/v2"
    "charm.land/lipgloss/v2"

    "marshal/internal/app/tui/chrome"
    "marshal/internal/app/tui/dock"
    "marshal/internal/app/tui/layout"
    "marshal/internal/app/tui/theme"
)

type SubmitMsg struct { ComposerID, SessionID, TurnID, Text string }
type CloseMsg struct { ComposerID string }
type Panel struct {
    id, sessionID, turnID string
    input textarea.Model
}
var _ dock.Panel = (*Panel)(nil)

func New(sessionID, turnID string) *Panel {
    input := textarea.New()
    input.Prompt = ""
    input.ShowLineNumbers = false
    input.CharLimit = 0 // validate the complete expanded draft in the parent
    input.Placeholder = "Steer the current turn..."
    input.KeyMap.InsertNewline.SetKeys("shift+enter")
    input.Focus()
    return &Panel{id: rand.Text(), sessionID: sessionID, turnID: turnID, input: input}
}
func (p *Panel) ID() string { return p.id }
func (p *Panel) Text() string { return p.input.Value() }
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
    if key, ok := msg.(tea.KeyPressMsg); ok {
        switch key.String() {
        case "enter", "alt+enter":
            out := SubmitMsg{ComposerID: p.id, SessionID: p.sessionID, TurnID: p.turnID, Text: p.Text()}
            return func() tea.Msg { return out }
        case "esc":
            id := p.id
            return func() tea.Msg { return CloseMsg{ComposerID: id} }
        case "ctrl+c":
            return nil // parent retains its existing global interrupt behavior
        }
    }
    var cmd tea.Cmd
    p.input, cmd = p.input.Update(msg)
    return cmd
}
func (p *Panel) View(width, maxHeight int) string {
    if width <= 0 || maxHeight <= 0 { return "" }
    th := theme.Current()
    pw := layout.PanelWidth(width)
    text := lipgloss.NewStyle().Foreground(th.FGDefault)
    muted := lipgloss.NewStyle().Foreground(th.FGMuted)
    styles := textarea.DefaultDarkStyles()
    styles.Focused.Text, styles.Blurred.Text = text, text
    styles.Focused.Placeholder, styles.Blurred.Placeholder = muted, muted
    styles.Focused.Prompt, styles.Blurred.Prompt = lipgloss.NewStyle(), lipgloss.NewStyle()
    styles.Focused.CursorLine, styles.Blurred.CursorLine = text, text
    p.input.SetStyles(styles)
    p.input.SetWidth(max(1, pw-2))
    p.input.SetHeight(max(1, maxHeight-4))
    rows := []string{
        "Target: parent conversation",
        "Stops foreground work; background tasks continue.",
        p.input.View(),
        muted.Render("Enter send now; Shift+Enter newline; Esc back"),
    }
    return chrome.Panel("Send now", strings.Join(rows, "\n"), pw, maxHeight, true, th)
}
```

2. Create steerpanel_test.go with this complete file:

```go
package steerpanel

import (
    "strings"
    "testing"

    tea "charm.land/bubbletea/v2"
    "charm.land/lipgloss/v2"
    "github.com/charmbracelet/x/ansi"

    "marshal/internal/app/tui/theme"
)

func TestSteerPanelSubmitsLiteralBoundText(t *testing.T) {
    p := New("session", "turn")
    p.Update(tea.PasteMsg{Content: "/literal instruction"})
    cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
    msg := cmd().(SubmitMsg)
    if msg.ComposerID != p.ID() || msg.SessionID != "session" || msg.TurnID != "turn" ||
        msg.Text != "/literal instruction" { t.Fatalf("%+v", msg) }
    long := strings.Repeat("x", 5000)
    other := New("session", "turn")
    other.Update(tea.PasteMsg{Content: long})
    if other.Text() != long { t.Fatal("silently truncated pasted draft") }
}
func TestSteerPanelEscAndEmptySubmissionAreOnlyMessages(t *testing.T) {
    p := New("session", "turn")
    close := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})().(CloseMsg)
    if close.ComposerID != p.ID() { t.Fatal("wrong composer") }
    submit := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})().(SubmitMsg)
    if submit.Text != "" { t.Fatal("invented text") }
    if p.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) != nil { t.Fatal("captured global ctrl+c") }
}
func TestSteerPanelShiftEnterAndPasteStayInComposer(t *testing.T) {
    p := New("session", "turn")
    p.Update(tea.PasteMsg{Content: "first"})
    p.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
    p.Update(tea.PasteMsg{Content: "second"})
    if !strings.Contains(p.Text(), "\n") || !strings.Contains(p.Text(), "second") { t.Fatal(p.Text()) }
    if New("session", "turn").Text() != "" { t.Fatal("copied another surface") }
}
func TestSteerPanelLayoutAndMonochrome(t *testing.T) {
    previous := theme.Current()
    t.Cleanup(func(){ theme.Reload(previous) })
    for _, mono := range []bool{false, true} {
        theme.Reload(theme.LoadFor(mono, "xterm-256color"))
        for _, width := range []int{80, 120, 200} {
            for _, height := range []int{6, 9, 16, 24} {
                p := New("session", "turn")
                out := p.View(width, height)
                if lipgloss.Height(out) > height { t.Fatalf("height %d", height) }
                for _, line := range strings.Split(out, "\n") {
                    if ansi.StringWidth(line) > width { t.Fatalf("width %d", width) }
                }
                for _, label := range []string{"Send now", "parent conversation", "background tasks continue", "Enter send now", "Esc back"} {
                    if !strings.Contains(ansi.Strip(out), label) { t.Fatalf("missing %q: %s", label, out) }
                }
            }
        }
    }
}
func TestSteerPanelResizeKeepsDraftAndIdentity(t *testing.T) {
    p := New("session", "turn"); id := p.ID()
    p.Update(tea.PasteMsg{Content: "keep this draft"})
    p.View(80,9); p.View(120,16)
    if p.ID()!=id || p.Text()!="keep this draft" { t.Fatal("resize changed draft") }
}
```

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui/steerpanel -count=1
```

Expected: component has no runtime effects and fits short dock budgets.

## Task 11: Integrate Send now with TUI routing and prompt responses

**Goal:** the explicit action works through normal/prompt input, preserves suspended surfaces and uses canonical runtime/prompt operations.

**Files:** `internal/app/tui/model.go`, `keypress.go`, `commands_dispatch.go`, `events.go`, `pump.go`, `status.go`, `view.go`, `model_test.go`, `commands_dispatch_test.go`, `pump_test.go`, `sudoauth.go`.

**Steps**

1. Implement WithSendNow in model.go, storing the two Contracts callbacks. Add private fields for the active steerpanel, one retained dock panel/its canonical prompt binding and the pre-composer focus state. Add cached steering snapshot and latest receipts with per-key versions. No general panel stack.
2. Add private methods `openSteeringComposer()`, `closeSteeringComposer(id string)` and `submitSendNow(request session.SendNowRequest, clear func()) bool`. Opening requires supported active snapshot, saves current surface and opens an empty composer. Submission expands ordinary-chat pastes first, validates complete text without truncation, assigns crypto/rand.Text request ID, and calls the bounded memory-only submit callback. Actual stop/join runs in Task 2's worker. Clear only accepted text; rejection preserves the draft and explains the reason.
3. Extend the access plan's existing Update reconciliation wrapper, rather than adding competing wrappers. Handle global Alt+Enter after Ctrl+C/size handling but before panel/prompt key routing:
   - Plain chat editor with text or queued messages submits directly; no popup acceptance/command parsing happens first.
   - A panel, prompt, command-edit or secret field owning focus opens the blank composer. Never read its value.
   - Unsupported/idle/closing/below-minimum states do not submit. Preserve the current raw draft.
4. Handle steerpanel.SubmitMsg only when its ComposerID matches the active composer and its bound session/turn are still current; text is literal. On accepted submission close composer and restore a valid unrelated surface. CloseMsg only restores; no execution or queue mutation. A late SubmitMsg after Esc/replacement cannot act.
5. Preserve suspended asynchronous state with one routing pass: for non-key/non-paste messages, temporarily route the existing inner update against the retained underlying dock panel, then capture its resulting panel/closure and restore the active composer. Parent runtime events and the access reconciliation run once. Do not send a panel result to both panels. Key/paste messages go only to composer. On restoring a prompt, require the same canonical key/access ID; expired requests retire, refreshed access snapshots reset through existing reconciliation.
6. In model.go's dispatchCommand(raw string), recognize the registered `send-now` name before shlex.Split can reinterpret its payload; require an exact command-token boundary and existing registry entry. Preserve the raw suffix's interior whitespace/newlines/quotes and send it literally through private `sendNowCommand(raw string) (tea.Model, tea.Cmd)`, defined beside the command effects in commands_dispatch.go. With no argument flush queue or open composer. Its failed path restores the raw command draft cleared by the existing Enter handler. Do not add a command to the model-facing tool registry.
7. Extend steeringMsg/pumpSteeringEvents/handleSteering with the optional snapshots/receipts from existing SteeringEvent. Keep QueueLen compatibility and one persistent subscription. Drop stale snapshots and stale per-request receipt revisions; do not discard another request's outcome using one global receipt revision.
8. Render bounded interrupting/waiting-safe/waiting-resume/delivered/undelivered status in existing input/status presentation. Do not add a task card. While composer owns input, hide the base input cursor and ordinary prompt controls; preserve transcript/background updates. Read snapshot directly on ticks for no-broker test/runtime cases.
9. Change handleApproval/handleQuestion/handleSkillGate/handleSudoAuth to resolve registered prompt keys, including policy/skill side effects inside ResolveApproval/ResolveSkillGate's accepted apply callback. Only render success after a winning response; late/canceled decisions do not persist rules, change authority or clear current prompts. Preserve legacy unregistered fixture handling and accesspanel's existing shared access resolver.
10. In handleAgentFinished's steering cleanup preserve Task 2's accepted/undelivered follow-up state; do not blindly ClearSteering on an eligible turn's completion. Existing explicit Ctrl+X still clears ordinary queued entries; it cannot revoke an already accepted Send now request. Maintain ordinary Ctrl+C stop/exit and child-specific Ctrl+X controls.
11. Add tests in the existing TUI test files:
    - `TestSendNowAltEnterAcceptsOnlyActiveParentDraft`: exact parent binding, accepted draft clears, rejected draft remains, child drill does not retarget.
    - `TestSendNowComposerDoesNotCopyPromptOrSecretInput`: approval edit/question/sudo/settings values never become composer text.
    - `TestSendNowComposerEscRestoresSurfaceWithoutInterruption`: same panel/draft/focus; no submit callback invoked.
    - `TestSendNowComposerRoutesSuspendedAsyncResultsOnce`: settings/probe/install-style result reaches retained panel while runtime events continue; expiry prevents prompt resurrection.
    - `TestSendNowLateComposerMessageCannotTargetNewTurn`: stale composer/session/turn message has no side effect.
    - `TestSendNowSizeGateResizeAndNoColor`: supported sizes work, hidden submission ignored, resize preserves text.
    - `TestSendNowReceiptVersionsAndSlowStopRemainTruthful`: status stays interrupting/waiting, no early stopped/delivered label.
    - `TestSendNowCommandLiteralFlushAndRejectionDraft`: command text/empty queue behavior and preservation.
    - `TestSendNowPromptAnswerRaceHasNoLateGrant`: canonical resolution wins once, no later rule/access mutation.
    - `TestSendNowFinishDoesNotDropAcceptedFeedback`: completion/hard-stop UI cleanup preserves undelivered text.
12. Keep existing approval/child/mode/status/paste/Ctrl+C/postmortem tests; only update obsolete assumptions that all steering is disposable on cancellation, explaining the accepted-immediate distinction.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui -run 'SendNow|Steering|Approval|Question|Sudo|CtrlC|Postmortem|Pump' -count=1
```

Expected: no hidden submission, lost draft, secret copy, stale grant or blocked UI during stop/join.

## Task 12: Expose contextual Send now help

**Goal:** footer hints distinguish queueing from interruption and only advertise the available action.

**Files:** `internal/app/tui/help/help.go`, `help_test.go`; one initializer field in `internal/app/tui/status.go`.

**Steps**

1. Add `SendNowAvailable bool` to FooterHints. In Footer's Busy branch change `pair("Enter", "send")` to `pair("Enter", "queue")`, and append `pair("Alt+Enter", "send now")` when SendNowAvailable. Also append that hint for normal approval/question/skill prompt branches when available; their ordinary Enter semantics remain unchanged.
2. In status.go's footerHints initializer set `SendNowAvailable: m.busy && m.steeringSnapshot.Active && m.steeringSnapshot.Supported` (use the field name established in Task 11). While the composer is active its own footer supplies keys.
3. Add `TestFooterSendNowRequiresCapability` (supported busy gets queue/send-now; idle/unsupported does not) and `TestFooterPromptKeepsAnswerAndSendNowDistinct` (Enter answer/confirm still appears once; Send now separate). Update existing busy footer assertion to expect queue.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app/tui/help ./internal/app/tui -run 'Footer|SendNow' -count=1
```

Expected: keyboard action is discoverable and accurately labeled at narrow widths.

## Task 13: Register the command alternative

**Goal:** /send-now is discoverable through existing command help/completion without becoming a model tool.

**Files:** `internal/commands/commands.go`, `commands_test.go`.

**Steps**

1. In RegisterAll's built-in chat command entries, beside the existing TUI-only conversation commands, insert this exact entry:

```go
{
    Name: "send-now",
    Description: "Interrupt the current operation and steer the same conversation turn",
    Args: "[message]",
    Group: groupChat,
    TUIOnly: true,
},
```

2. Add `TestSendNowCommandIsTUIOnlyAndDiscoverable` in commands_test.go: RegisterAll then Lookup("send-now") succeeds with those fields and no Handler; existing completion/list includes it. The TUI effect already exists from Task 11.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/commands -run 'SendNow|Register|List' -count=1
```

Expected: command registry exposes the alternative and generic headless command execution does not pretend to support it.

## Task 14: Wire runtime ownership and preserve it across reload/replacement

**Goal:** production app supplies one operation interruption handler and the shared TUI facade without reload or manager replacement.

**Files:** `internal/app/runtime.go`, `app.go`, `runtime_test.go`, `app_test.go`.

**Steps**

1. In buildAgentRunnerWithLock enable `runner.EnableSteering = true` only for its root/general conversation runner; the depth/role predicate in RunTask remains authoritative. Do not set it in child/controller/swarm factories. After constructing runtime/State with the prerequisite's conversation-owned JobManager, bind State.SetOperationInterrupt to that **captured conversation manager**, not a closure that blindly dereferences a future rt.State/manager after replacement. Handler calls manager.InterruptOperation and returns its OperationStop. Do not bind a child as the parent's handler.
2. Add Runtime.SteeringSnapshot and SubmitSendNow from Contracts. Briefly capture State under Runtime.mu then release; delegate to that State's memory-only operation. Do not acquire reloadMu for a whole rebuild, wait for a process, build a provider or invoke config persistence. Missing State returns inactive/error.
3. On NewSession bind the new State to its own new manager after successful construction; old State keeps its handler until end/join. Reuse the existing lifecycle's cancellation/retirement; an old request can never address a replacement conversation.
4. Config reload within the same conversation retains State/controller, operation tokens, handler and the stable manager from the background work. Runner.CopyFrom must not reset steeringTurnID for an invocation already running. No Send now path calls ConfigReloader or replaces service pointers.
5. In Run's tuiOpts append `tui.WithSendNow(rt.SteeringSnapshot, rt.SubmitSendNow)` beside the existing access/runtime options. These bound methods follow the current conversation, while a request's explicit SessionID/TurnID prevents stale targeting.
6. Add app tests:
   - `TestRuntimeSendNowUsesCapturedConversationManager`: exact manager receives the operation, same-turn next command works.
   - `TestRuntimeSendNowDoesNotSaveReloadOrReplaceManagers`: spy counters zero and identity/captured settings unchanged.
   - `TestRuntimeSendNowReloadRetainsOperationAndReceipts`: explicit unrelated reload preserves active controls and IDs.
   - `TestRuntimeSendNowReplacementRejectsOldBinding`: old request/callback cannot stop new conversation work; old work end follows lifecycle contract.
   - `TestRuntimeSendNowUnavailableFailsClosed`: nil manager/handler or unsupported/no turn gives no queue/stop mutation.
   - `TestRuntimeSendNowTUIOptionsExerciseRealFacade`: construct actual tui.Model with runtime callbacks, public key/steerpanel messages and hermetic fake process; verify accepted receipt then model delivery.
7. Use existing testenv, WithConfigLoader/WithAuthResolver and fakeRunner fixtures. Tests own endpoints and processes; no developer keychain/model/download.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./internal/app -run 'RuntimeSendNow|RuntimeNewSession|ReloadAgentRuntime' -count=1
```

Expected: one conversation owner and no hidden rebuild or cross-session control.

## Task 15: Add ACP now/status/receipt support and canonical bridge delivery

**Goal:** ACP uses the same operation service, preserves queued clients and cannot turn steering cancellation into full-turn cancellation.

**Files:** `internal/acp/turn.go`, `host.go`, `permissions.go`, `questions.go`, `turn_test.go`, `permissions_test.go`, `questions_test.go`.

**Steps**

1. Replace the existing SteerParams declaration with this complete declaration, and add the wire declarations below it. These duplicate the external JSON contract; no wire field aliases an internal struct.

```go
type SteerParams struct {
    SessionID string `json:"sessionId"`
    Text string `json:"text"`
    Delivery string `json:"delivery,omitempty"`
    TurnID string `json:"turnId,omitempty"`
    RequestID string `json:"requestId,omitempty"`
}
type SteeringStateWire struct {
    Version uint64 `json:"version"`
    TurnID string `json:"turnId"`
    Active bool `json:"active"`
    Supported bool `json:"supported"`
    Held bool `json:"held"`
    OperationID string `json:"operationId,omitempty"`
    OperationKind string `json:"operationKind,omitempty"`
    PendingCount int `json:"pendingCount"`
}
type SteeringOutcomeWire struct {
    Kind string `json:"kind"`
    State string `json:"state"`
    ToolCallID string `json:"toolCallId,omitempty"`
    ExecutionID string `json:"executionId,omitempty"`
    AgentID string `json:"agentId,omitempty"`
    Summary string `json:"summary,omitempty"`
}
type SteeringReceiptWire struct {
    Version uint64 `json:"version"`
    RequestID string `json:"requestId"`
    TurnID string `json:"turnId"`
    Status string `json:"status"`
    OperationID string `json:"operationId,omitempty"`
    Outcomes []SteeringOutcomeWire `json:"outcomes,omitempty"`
}
```

2. Add TurnRuntime fields `SteeringSnapshot func() session.SteeringSnapshot` and `SendNow func(session.SendNowRequest) (session.SteeringReceipt, error)`. In host.go's Lookup literal bind rt.SteeringSnapshot/rt.SubmitSendNow. Register `srv.Handle("session/steering_status", turns.SteeringStatus)` beside existing session/steer registration.
3. Implement private converters `steeringStateWire(session.SteeringSnapshot) SteeringStateWire` and `steeringReceiptWire(session.SteeringReceipt) SteeringReceiptWire`, copying all fields/slices. Implement `func (m *TurnManager) SteeringStatus(ctx context.Context, params json.RawMessage) (any, error)` using existing sessionIDParams validation/lookup. Return an inactive snapshot for idle sessions. For an active unsupported controller, return Active true/Supported false and its opaque slot ID, never a control token. Add private `id string` to activeTurn, initialized with crypto/rand.Text at runTurn reservation.
4. In Steer normalize omitted delivery to queued; reject unknown modes. Queued retains its current active-turn/nonempty-text checks and Steer callback. For now require nonempty turnId/requestId, active supported normal snapshot, and the SendNow callback; pass the exact request to runtime, mapping validation to existing JSON-RPC errors. Empty text is accepted only when runtime can flush queued work. Return steeringReceiptWire; no silent queued fallback.
5. In messageUpdate/session event translation add EventSteeringState/EventSteeringReceipt as additive `kind:"steering_state"` / `"steering_receipt"` maps plus the converted fields. Publish final receipt updates before closing the turn event stream. Existing clients may ignore new kinds.
6. In PermissionBridge.Request/RequestSkillGate, registered prompt Key.ID paths do not require ResponseChan and never send/close a copied channel. Collect the client response then call pending.Respond, which delegates canonical resolution. Retain direct-channel legacy behavior only for unregistered fixtures. Keep the prerequisite typed access CollectAccess→ResolveAccess flow.
7. In QuestionBridge.Ask use the canonical responder already established for registered PendingQuestion. Add private bridge request-context helper `promptRPCContext(parent context.Context, original context.Context) (context.Context, func())`: context.WithCancelCause(parent) plus context.AfterFunc on the original prompt context, cleaning up both hooks. State.PromptContext(key) supplies the owning operation context. This cancels a pending outbound RPC when its operation is interrupted, without leaking a permission/question goroutine until a late client answer.
8. In runTurn's approval/question/skill/access error branches, distinguish PromptCause(key)==ErrSendNow or IsSteeringCancellation on the prompt context. These end only that RPC; they must not invoke slotCancel/subCancel, fabricate answers or grant/deny sticky policy. Real client/transport failures retain current full-turn failure behavior. Access cancellation still uses its canonical resolver/gate.
9. Add tests:
   - `TestACPSendNowQueuedCompatibilityAndWireRoundTrip`: existing parameters/results unchanged; new modes and DTO fields round trip.
   - `TestACPSendNowStatusHasAuthoritativeTurnIdentity`: missed event recovery via status; unsupported controller Active true/Supported false cannot interrupt.
   - `TestACPSendNowOrderingAndActualReceipts`: accepted response precedes settled output; delivered only at submitted model request; same turn remains active.
   - `TestACPSendNowRetryAndStaleTurnCannotTargetLaterWork`: one message/stop per request ID, old turn rejected.
   - `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn`: request context stops; parent continues; late client response cannot grant/answer a new prompt.
   - `TestACPSendNowAccessApplyRaceKeepsCommittedGrant`: both sides of canonical application acceptance are accurate.
   - `TestACPSendNowFlushValidationAndUnsupportedNoFallback`: empty flush/unknown delivery/missing IDs/idle/closing all preserve state on rejection.
   - `TestACPSendNowFinalReceiptBeforeTurnStreamCloses`: final delivered/undelivered event observable, with copied slices and per-request version ordering.

**Verify**

```sh
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/acp -run 'SendNow|Steer|Permission|Question|SkillGate|Mode|Hold' -count=1
```

Expected: new ACP behavior matches TUI/runtime and old queued/permission clients retain compatibility.

## Task 16: Document and verify the assembled behavior

**Goal:** public documentation and hermetic end-to-end checks describe the complete same-turn interruption feature.

**Files:** `README.md`, `docs/ARCHITECTURE.md`, `docs/send-now/feature-spec.md` and this plan.

**Steps**

1. In README's TUI interaction description (the paragraph naming Enter and Esc), add:

> During a normal conversation turn, Enter queues steering and Alt+Enter sends it now. Send now interrupts the current operation: it stops attached foreground commands, ends waits without stopping their targets, and cancels unanswered prompts without granting permission. Background jobs and independent subagents continue. Use /send-now as the command alternative. SDD/swarm controller steering is not supported.

2. In ARCHITECTURE's single-turn data-flow discussion add:

> Send now is a runtime operation beneath the active conversation turn. Session state owns ordered steering, operation identities, canonical human-request settlement and receipts. The existing command service arbitrates interruption against completion/detachment; it does not close future turn admission. Complete tool results precede interruption context and steering in the append-only model history. TUI and ACP use the same runtime facade; transports do not implement process/policy control.

3. After successful implementation checks, update the spec's implementation status and record the actual merged prerequisite/implementation commits and checks. Keep its approved behavior current. Do not mark implementation complete based on authoring snippet compilation.
4. Run the final commands below. Resolve failures in the responsible package task, rerun its narrow checks and affected broad checks. Review the assembled diff for no manager/provider reload, no turn-stop substitute, no TUI agent import, no web internal import, no persisted queue/schema, no secret event fields, and no new automatic turn.

**Verify**

```sh
gofmt -l internal/app/session internal/agent internal/tools/native internal/app/tui internal/acp internal/app
git diff --check
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test -race ./internal/app/session ./internal/agent ./internal/agent/swarm ./internal/tools/native ./internal/acp
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./...
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go vet ./...
GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go build -o /private/tmp/marshal-send-now ./cmd/marshal
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-send-now GOCACHE=/private/tmp/codex-gocache CGO_ENABLED=1 go test ./test/usability/... -run TestScripted -v -timeout 5m
```

Expected: gofmt produces no output; diff/race/full suite/vet/build/scripted usability pass. The process test uses a local fixture, all providers/endpoints are fake/test owned, credentials are injected, and live flags are not enabled.

## Coverage and handoff

| Approved scenario | Owning tasks |
| --- | --- |
| Same turn, queued/immediate order, repeated submissions | 1, 2, 9 |
| Foreground stop, actual output and detach races | 4, 5, 9 |
| Await any/all/child/job targets continue | 6, 9 |
| Model cleanup, no retry/salvage, usage | 8, 9 |
| Gate waits, atomic mutation and unstarted batch results | 2, 5, 6, 7 |
| Prompt cancellation/accepted grants/late answers | 3, 5, 6, 11, 15 |
| Composer, secrets, drafts, suspended panels, size/color | 10, 11, 12 |
| Hard limits, final-answer race, hold, unsupported workflows | 2, 9, 11, 15 |
| Runtime identity/reload/replacement and ACP parity | 14, 15 |
| Prompt-prefix and full-project regressions | 9, 16 |

The executor reads Contracts plus the current task, applies its exact code and anchored edits, runs its named checks, and commits before continuing. Existing source anchors were inspected at the authoring base. Prerequisite API extensions are explicitly labeled and have their semantics/signatures pinned here. No task delegates an unresolved product decision to execution.

## Authoring validation

Plan authoring changed documentation only. Temporary Go overlays under `/private/tmp/marshal-send-now-plan-validation` checked the complete snippets without creating or replacing repository source files:

- The composer's five embedded tests passed, including literal/bound submission, retained long pasted text, key handling, resize, short layouts and monochrome. The overlay used the existing gatepanel package directory so Go could run vet without creating the future steerpanel directory.
- The session type/context snippets compiled with a temporary stub for the later controller mutation helper. Existing steering/hold tests and swarm lock tests passed against the full replacement lock.
- The ACP wire declarations and exact command entry compiled in their owning packages; existing `Steer|Register|List` checks passed. ACP's local Unix-socket tests required a rerun outside the filesystem sandbox after bind was denied.
- Structural checks confirmed 16 sequential tasks with Goal/Files/Steps/Verify, resolved plan links, balanced fences, no placeholders and no trailing whitespace.

These checks validate embedded code and compatibility at the authoring base. They do not implement or validate the new controller/transport behavior, the unmerged prerequisite APIs or the final assembled feature. No production implementation, full-suite run or commit was part of this authoring turn.
