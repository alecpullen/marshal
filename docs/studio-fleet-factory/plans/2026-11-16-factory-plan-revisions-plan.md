# Active factory plan revisions implementation plan

**Goal:** Let users and authorized planners propose, assess, authorize and safely deliver factory plan/spec revisions during work without rewriting old evidence.

**Spec:** [Active factory plan revisions specification](../factory-plan-revisions-spec.md), based on the approved [product design](../factory-plan-revisions-design.md). The user approved this implementation plan on 2026-11-16.

**Base:** authored against local `main` at `1bc6d64b`; these planning documents are uncommitted. Before execution, start from current `main` and recheck the in-flight Claude planner, P5, MCP factory and working-drafts changes. Include the approved documents in the factory input; do not incorporate unrelated work merely to reproduce this base.

**Non-goals:** New LLM planner, cross-repository handoff, automatic merge, changing existing direct human steering behavior, implicit extra rounds, treating ACP receipt as proof of compliance, or blocking factory work merely because an unapproved proposal exists. `web/` stays Go standard library only; tests use isolated stores and fake services.

## Contracts

Add the following additive types in `web/bridge/factory_revision_types.go`; use JSON field names verbatim and equivalent TypeScript types in `web/ui/src/lib/factory.ts`. Existing journal records lacking `proposals` must replay unchanged. The currently active approval remains the last entry of `FactoryItem.Approvals` until activation occurs; old effects/rounds keep their recorded revision.

```go
type FactoryRevisionAssessment struct {
    Classification string `json:"classification"` // routine_clarification | scope_change | needs_user
    Reason string `json:"reason"`
    AffectedEffectID string `json:"affectedEffectId,omitempty"`
    AffectedWorkerID string `json:"affectedWorkerId,omitempty"`
    Recommendation string `json:"recommendation"` // amend | follow_up
    Strategy string `json:"strategy"` // wait_safe | interrupt | hold
    AssessorID string `json:"assessorId"`
    CandidateHash string `json:"candidateHash"`
    BaseRevision uint64 `json:"baseRevision"`
}
type FactoryRevisionDelivery struct {
    Strategy string `json:"strategy"` // wait_safe | interrupt | hold
    State string `json:"state"` // pending | dispatching | accepted | interrupting | waiting_safe_boundary | waiting_for_resume | delivered | undelivered | uncertain
    SessionID string `json:"sessionId,omitempty"`
    TurnID string `json:"turnId,omitempty"`
    RequestID string `json:"requestId,omitempty"`
    CandidateHash string `json:"candidateHash"`
}
type FactoryRevisionProposal struct {
    ID string `json:"id"`; Version uint64 `json:"version"`
    BaseRevision uint64 `json:"baseRevision"`; BaseHash string `json:"baseHash"`
    Plan string `json:"plan"`; Spec string `json:"spec"`; CandidateHash string `json:"candidateHash"`
    CreatedBy string `json:"createdBy"`; AutoAssess bool `json:"autoAssess"`
    Status string `json:"status"` // proposed | awaiting_authorization | authorized | activated | rejected | superseded | follow_up_pending | followed_up
    AssessmentState string `json:"assessmentState"` // not_requested | pending | assessing | assessed | unavailable | uncertain | failed
    AssessmentAttemptID string `json:"assessmentAttemptId,omitempty"`
    Assessment *FactoryRevisionAssessment `json:"assessment,omitempty"`
    AuthorizedBy string `json:"authorizedBy,omitempty"`; Authority string `json:"authority,omitempty"`
    Routing string `json:"routing,omitempty"` // amend | follow_up
    PauseState string `json:"pauseState,omitempty"` // requested | stopping | paused | uncertain
    PauseRequestID string `json:"pauseRequestId,omitempty"`
    Delivery *FactoryRevisionDelivery `json:"delivery,omitempty"`
    FollowUpRequestID string `json:"followUpRequestId,omitempty"`
    FollowUpItemID string `json:"followUpItemId,omitempty"`
    CreatedAt time.Time `json:"createdAt"`; UpdatedAt time.Time `json:"updatedAt"`
}
type FactoryRevisionCommand struct {
    ProposalID string `json:"proposalId,omitempty"`; ProposalVersion uint64 `json:"proposalVersion,omitempty"`
    BaseRevision uint64 `json:"baseRevision,omitempty"`; BaseHash string `json:"baseHash,omitempty"`
    Plan string `json:"plan,omitempty"`; Spec string `json:"spec,omitempty"`; CandidateHash string `json:"candidateHash,omitempty"`
    AttemptID string `json:"attemptId,omitempty"`; Assessment *FactoryRevisionAssessment `json:"assessment,omitempty"`
    Routing string `json:"routing,omitempty"`; Strategy string `json:"strategy,omitempty"`
    FollowUp *FactoryCreate `json:"followUp,omitempty"`
}
// FactoryCommand gains Revision *FactoryRevisionCommand `json:"revision,omitempty"`.
// MCPFactoryProjectGrant gains AssessPlanChanges and ApproveRoutineRevision
// booleans under json:"assessPlanChanges,omitempty" and
// json:"approveRoutineRevision,omitempty"; zero values grant nothing.
```

`FactoryItem.Proposals []FactoryRevisionProposal` is optional. A proposal is immutable except state/assessment/authority/delivery fields; editing creates a new ID and marks the previous unresolved proposal superseded. Require existing item `expectedVersion`, proposal version and relevant ID/hash/base on every mutation. A single unresolved proposal is allowed. Save for approved items in queued/running/checking/repairing/needs-input/ready; preserve a proposal through later state transitions. Reject save for draft, stopped/stopping, closed or recovering with an actionable error. An unapproved proposal does not block normal work or existing readiness; Studio labels any ready state as applying to the prior approved revision. While an authorized proposal has not activated, forbid conflicting revision/route-change retries and a second authorization, but allow user stop and explicitly display why other controls are unavailable.

Extend `ProjectSettings` with `AutoAssessPlanChanges *bool` (`json:"autoAssessPlanChanges,omitempty"`); nil means true, false is user override. Persist through existing conditional settings path and preserve unrelated fields. A saved proposal records the effective setting. If true, create an assessment intent with deterministic ID `proposalID + ":assessment:1"`; false remains `not_requested` until a manual request. An available configured planner adapter receives a bounded immutable request via an instance-scoped seam, never under the factory store lock. Existing MCP designated clients get an authorized polling tool; the server cannot wake an external conversation. Absent/unreachable planner -> `pending`/`unavailable`; a lost acknowledgement -> `uncertain`, never automatic resend. A human explicitly requesting another assessment generates a new attempt ID after an uncertain attempt.

Define an instance-owned `revisionPlanner` and result structures in `web/bridge/factory_revision_planner.go`:

```go
type RevisionAssessmentRequest struct {
    ItemID string `json:"itemId"`; ProposalID string `json:"proposalId"`
    AttemptID string `json:"attemptId"`; BaseRevision uint64 `json:"baseRevision"`
    CandidateHash string `json:"candidateHash"`; ApprovedPlan string `json:"approvedPlan"`
    ApprovedSpec string `json:"approvedSpec"`; ProposedPlan string `json:"proposedPlan"`
    ProposedSpec string `json:"proposedSpec"`; EffectID string `json:"effectId,omitempty"`
    WorkerID string `json:"workerId,omitempty"`
}
type revisionPlanner interface {
    Available(context.Context, string) (bool, error) // project root
    Assess(context.Context, RevisionAssessmentRequest) (FactoryRevisionAssessment, error)
}
```

The no-planner implementation reports unavailable, without goroutines retrying. The MCP polling endpoint exposes assigned pending requests only to the project's designated client with the new `assessPlanChanges` factory grant, `read` and current repo scope. No automatic turn is initiated. Assignment, claim and assessment results use stable attempt ID, item/proposal versions and candidate/base identity. Record `assessing` before dispatch or MCP claim; if a connection is lost afterward it is uncertain, not automatically requeued. Any external caller's authority remains attributed and project-scoped under the existing `factoryMCPAuthorizer` policy lock.

New project grant booleans `assessPlanChanges` and `approveRoutineRevision` default false and are editable in the existing conditional grant UI. Routine planner authorization requires a matching non-stale `routine_clarification` assessment, `PlannerDelegationEnabled`, the designated assessor/client and `approveRoutineRevision`, all rechecked under the existing policy mutex at journal commit. Classification is an assertion by a trusted planner, not proof; `scope_change` and `needs_user` require authenticated Studio user authorization. Never allow a client to self-designate or reuse `answerQuestion`/`approveAndQueue` for this capability.

User authorization persists candidate bytes, hash and base, `routing` and source without appending an active approval while an old effect is live. If planner unavailable require the user to choose `wait_safe`, `interrupt` or `hold`; an available planner's matching assessment supplies a recommendation that the user can override. An explicitly authorized follow-up uses a separate user-confirmed normal factory draft with deterministic create request ID and a durable intent/association; the source remains on its original revision. A new item is linked only after its factory journal record is confirmed, and source/item association is reconciled across crash; no direct approval or run. `amend` activates only after the previous factory effect is settled, active work/checkout is known idle and, if previously ready, the existing `set_draft` effect has been confirmed. Then append one approval with exact candidate plan/spec and prior config. Check active base again at activation; preserve the old history and effect identity. Fresh work uses the existing round allowance; exhausted -> existing limit/retry decision, no automatic grant. A new approval requires new revision-bound implementation and review plus fresh observed CI before ready even when SHA is unchanged.

For interrupt, add bridge-local wire types (do not import `internal/acpwire` from `web/`) and registry methods:

```go
type RevisionSteeringState struct { TurnID string `json:"turnId"`; Active bool `json:"active"`; Supported bool `json:"supported"` }
type RevisionSteeringReceipt struct { Version uint64 `json:"version"`; RequestID string `json:"requestId"`; TurnID string `json:"turnId"`; Status string `json:"status"` }
func (r *Registry) RevisionSteeringStatus(ctx context.Context, sessionID string) (RevisionSteeringState, error)
func (r *Registry) RevisionSendNow(ctx context.Context, sessionID, turnID, requestID, text string) (RevisionSteeringReceipt, error)
```

The first method sends `session/steering_status`; the second sends `session/steer` with `delivery:"now"`. Record intent, session ID, turn ID, request ID and candidate hash before sending. `session/steering_status` returns state, not the full receipt: capture actual `steering_receipt` updates via the existing session update stream and correlate session/request/turn; the persisted request-to-hash mapping binds a receipt to the proposal. If the receipt cannot be observed after restart or belongs to another request, remain uncertain; do not infer delivery from an idle state or resubmit. `delivered` means input entered runtime, not model compliance. An in-flight operation retains its original approval revision even after Send now; it cannot prove completion of the candidate revision. Detached commands need independent active-work evidence.

## Task 1: Proposal journal and conditional project setting

**Goal:** Persist immutable proposals and default-on assessment preference without changing current factory execution.

**Files:** Create `web/bridge/factory_revision_types.go`, `web/bridge/factory_revision_store_test.go`; modify `web/bridge/factory_types.go`, `web/bridge/factory_store.go`, `web/bridge/factory_controller.go`, `web/bridge/workspace.go`, `web/bridge/projects.go` and their existing tests.

**Steps:** Add the Contracts types, `FactoryItem.Proposals`, optional `ProjectSettings.AutoAssessPlanChanges` and an `autoAssessPlanChanges` default-true getter. Add factory command kinds `revision_propose`, `revision_supersede`, `revision_reject`, `revision_assess_request` to `FactoryCommand`, carrying a structured `FactoryRevisionCommand` with proposal ID/version, base revision/hash and immutable candidate plan/spec. Use `FactoryPlanHash`, `factoryImportLimit`, current `expectedVersion` and `FactoryStore.Update`; journal proposal and pending intent in one mutation. Reject stale base, duplicate unresolved proposal without explicit supersede, illegal states and invalid payload. Retain old proposals and old factory behavior. Preserve project settings through the existing ETag/conditional update. Tests: `TestFactoryRevisionLegacyJournalReplay`, `TestFactoryRevisionProposalImmutableAndSupersede`, `TestFactoryRevisionDefaultAutoAssess`, `TestFactoryRevisionProjectPreferenceRoundTrip`, `TestFactoryRevisionStaleBase`, `TestFactoryRevisionConcurrentProposals`, `TestFactoryRevisionPendingDoesNotBlockWorkOrReady`, `TestFactoryRevisionIdempotentRequestReplay`.

**Verify:** `go test ./web/bridge -run '^(TestFactoryRevision|TestProjectSettings|TestFactoryHTTPCreateApprovalAndReplay)' -count=1` and `go test -race ./web/bridge -run '^TestFactoryRevisionConcurrentProposals$' -count=1` pass. Commit `factory-revisions: task 1 — proposal journal`.

## Task 2: Assessment and delegated authority

**Goal:** Schedule auto/manual assessment safely and permit only explicitly delegated routine authorization.

**Files:** Create `web/bridge/factory_revision_planner.go`, `web/bridge/factory_revision_planner_test.go`; modify `web/bridge/factory_controller.go`, `web/bridge/factory_types.go`, `web/bridge/client.go`, `web/bridge/mcp_factory_auth.go`, `web/bridge/mcp_factory_tools.go`, `web/bridge/mcp_factory_tools_test.go`, `web/bridge/mcp_factory_auth_test.go`.

**Steps:** Implement the Contracts `revisionPlanner` seam, unavailable default and one durable assessment intent/attempt. Add `revision_assess_claim`, `revision_assess_result`, `revision_assess_retry` with exact hash/base/attempt/observed effect checks; dispatch outside locks and persist uncertainty on transport loss. Add MCP tools `factory_revision_pending` (`projectId`, read + `assessPlanChanges`), `factory_revision_claim` (`projectId`, `itemId`, `requestId`, `expectedVersion`, `proposalId`, `proposalVersion`, `attemptId`) and `factory_revision_assess` (`projectId`, `itemId`, `requestId`, `expectedVersion`, `proposalId`, `proposalVersion`, `attemptId`, `assessment`). Claim journals `assessing` before returning the assignment, and assessment is accepted only from the claiming designated client; a lost claim or result stays uncertain until explicit user retry. Use existing standard result envelope and project/repo scoping. Add client grant booleans `assessPlanChanges` and `approveRoutineRevision`, and policy-lock checks per Contracts. Planner-reported classification and provenance stay explicit. A pause recommendation is recorded, but if project delegation is off it requires an explicit user action before interrupting work; do not claim a pause from a recommendation.

Tests: `TestFactoryRevisionAssessmentDefaultPending`, `TestFactoryRevisionAssessmentDisabledManualRequest`, `TestFactoryRevisionPlannerUnavailableNoWake`, `TestFactoryRevisionAssessmentResultStaleTarget`, `TestFactoryRevisionAssessmentLostResponseUncertain`, `TestMCPRevisionProjectScopeAndGrant`, `TestMCPRevisionRoutineAuthorizationPolicy`, `TestMCPRevisionRevocationRacesAuthorization`, `TestMCPRevisionCannotSelfDesignate`. Use fakes to prove no provider endpoint is contacted by normal tests.

**Verify:** `go test ./web/bridge -run '^(TestFactoryRevisionAssessment|TestFactoryRevisionPlanner|TestMCPRevision)' -count=1` and `go test -race ./web/bridge -run '^TestMCPRevisionRevocationRacesAuthorization$' -count=1` pass. Commit `factory-revisions: task 2 — assessment and authority`.

## Task 3: Revision authorization, activation and follow-up

**Goal:** Bind authorization to exact content, preserve the old in-flight revision and activate a new one only at a verified safe boundary.

**Files:** Create `web/bridge/factory_revision_controller.go`, `web/bridge/factory_revision_controller_test.go`; modify `web/bridge/factory_controller.go`, `web/bridge/factory_types.go`, `web/bridge/factory_effects.go`, `web/bridge/factory_store.go`, `web/bridge/factory_http.go` and existing factory tests.

**Steps:** Add `revision_authorize`, `revision_delivery_choice`, `revision_follow_up_confirm` commands with item/proposal versions, candidate hash, base approval, source and explicit routing; derive Studio identity server-side. Authorized MCP routine changes go through the Task 2 policy lock and a server-supplied principal. Store an authorized pending activation; do not change `Approvals` while old effect is live. At the controller's safe boundary recheck target/approval, settled effect, `RuntimeKnown`, `TurnIdle`, `ActiveWorkKnown` and `WorkIdle`. For ready items, journal and confirm existing `set_draft` before activation; distinguish the revision transition from the existing external-conflict `set_draft` path. Append a single new approval only after the gate; use prior config and exact candidate. Record source and activation event atomically. Implement a durable follow-up create intent with deterministic namespaced `FactoryCreate.RequestID`, exact confirmed plan/spec/config and one source link; reconcile a lost create/association after restart and do not create duplicates. Require current item/proposal identity and existing normal preflight. Do not change existing retry semantics or auto-increase round allowance.

Tests: `TestFactoryRevisionAuthorizeStaleRejected`, `TestFactoryRevisionOldTurnKeepsApproval`, `TestFactoryRevisionActivateOnlyWhenIdle`, `TestFactoryRevisionReadyPRReturnsToDraft`, `TestFactoryRevisionReadyDraftFailureUncertain`, `TestFactoryRevisionExistingAllowanceEnforced`, `TestFactoryRevisionFollowUpIdempotentRecovery`, `TestFactoryRevisionUserChoiceOverridesRecommendation`, `TestFactoryRevisionRoutineAuthorizationAudited`, `TestFactoryRevisionSupersedeAfterApprovalRejected`.

**Verify:** `go test ./web/bridge -run '^(TestFactoryRevision|TestFactoryEndToEnd|TestFactoryRouting)' -count=1` passes. Commit `factory-revisions: task 3 — authorization and activation`.

## Task 4: Safe delivery and interruption

**Goal:** Dispatch authorized changes without confusing queued, accepted, delivered or completed work.

**Files:** Create `web/bridge/factory_revision_delivery.go`, `web/bridge/factory_revision_delivery_test.go`; modify `web/bridge/registry.go`, `web/bridge/factory_controller.go`, `web/bridge/factory_workers.go` and existing tests.

**Steps:** Implement `RevisionSteeringStatus`/`RevisionSendNow` as Contracts using the actual ACP method parameters and bridge-local wire structs; parse bounded `steering_receipt` notifications from the existing session-update callback, correlate session + turn + request against the durable candidate-bound intent, and store only observed statuses. For an assessment-time pause, journal a separate proposal `PauseState` and `PauseRequestID` tied to the affected effect and turn before calling the existing `session/cancel` path; the planner may request it automatically only when its project delegation and assessment capability are currently authorized. With delegation off, show the recommendation and let the user explicitly request pause. Distinguish this from the factory's terminal `stop`: a matching canceled effect settles as paused for this proposal, retains the checkout, and the scheduler must not dispatch more work for that item while the pause remains unresolved. Do not treat a cancellation request or detached command as idle; report `paused` only after observing turn and active work idle. On refusal or transport loss keep the pause uncertain and use existing explicit recovery rather than restarting. `wait_safe` records no fake steer receipt; after old turn settles and checkout is observed idle, activate and include the revision in the next normal `BuildFactoryPrompt`. `interrupt` first records the full intent, reads active supported turn, then sends `delivery:"now"`; never send when target no longer matches. Preserve old effect/round attribution even if Send now succeeds, prevent stale readiness, and wait for independent idle/active-work proof before activation and next work. A `delivered` receipt means delivered to runtime only. Unsupported runtime, lost notification, replaced newest receipt, disconnected worker or restart without evidence becomes visible unsupported/uncertain, never blind resend. Handle a user's `hold` without dispatch. After a proposal is superseded or rejected, the user explicitly resumes a paused worker; never leave it silently stopped.

Tests: `TestFactoryRevisionDelegatedAssessmentRequestsPause`, `TestFactoryRevisionPauseNeedsUserWhenDelegationOff`, `TestFactoryRevisionPauseIsNotFactoryStop`, `TestFactoryRevisionPauseDoesNotAssumeIdle`, `TestFactoryRevisionPausedWorkerExplicitResume`, `TestFactoryRevisionSteeringUsesExplicitTurnAndRequest`, `TestFactoryRevisionReceiptMustMatchIntent`, `TestFactoryRevisionQueuedIsNotDelivered`, `TestFactoryRevisionSendNowOldEvidenceStaysOld`, `TestFactoryRevisionInterruptDoesNotAssumeIdle`, `TestFactoryRevisionDetachedWorkBlocksActivation`, `TestFactoryRevisionLostReceiptIsUncertain`, `TestFactoryRevisionHoldDoesNotSend`, `TestFactoryRevisionWaitSafeNextPromptContainsNewPlan`, `TestFactoryRevisionDirectSteeringRemainsAvailable`. Use scripted registry transport and fake active-work observation.

**Verify:** `go test ./web/bridge -run '^(TestFactoryRevision|TestRegistry)' -count=1` and `go test -race ./web/bridge -run '^TestFactoryRevision' -count=1` pass. Commit `factory-revisions: task 4 — delivery evidence`.

## Task 5: Studio proposal and permission UX

**Goal:** Make revision creation, assessment, authorization and actual delivery status understandable on phone and desktop.

**Files:** `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`, `web/ui/src/lib/ClientsPanel.svelte`, `web/ui/src/lib/ClientsPanel.test.ts`, `web/ui/src/lib/api.ts`, `web/ui/src/lib/factory.ts`; create `web/ui/src/lib/FactoryRevision.svelte`, `web/ui/src/lib/FactoryRevision.test.ts`.

**Steps:** Add typed proposal/assessment/delivery and command structures matching Contracts. In factory item show current approved revision, side-by-side or stacked accessible plan/spec comparison, editable proposal and auto-assessment toggle showing effective project value; conditional settings changes preserve unrelated fields. Show assessment availability, reasons, routing recommendation, pause/interrupt evidence, explicit user approve and amend/follow-up action. If unavailable, present `wait_safe`, `interrupt`, `hold`; show the real receipt and explain its meaning. Preserve unsaved text after errors/conflicts, and show old revision readiness/evidence without presenting it as coverage for the new one. Add new MCP grant controls to ClientsPanel; false by default and no change to old permissions. No session-id or credential leaks. Tests: `proposalSaveDoesNotApprove`, `automaticAssessmentVisibleAndConfigurable`, `plannerUnavailableShowsPendingAndChoices`, `oldAndNewEvidenceAreDistinguished`, `receiptSentIsNotDelivered`, `staleProposalRetainsText`, `manualFollowUpConfirmation`, `revisionGrantsDefaultOff`, `projectPreferenceConflictRetainsDraft`. Use 390px stacking and keyboard accessible controls; do not require a planner to open the screen.

**Verify:** `cd web/ui && npm test -- src/views/FactoryItem.test.ts src/views/Project.test.ts src/lib/ClientsPanel.test.ts src/lib/FactoryRevision.test.ts` passes; `npm run check` has no new errors versus current-main baseline. Commit `factory-revisions: task 5 — Studio controls`.

## Task 6: End-to-end verification and records

**Goal:** Prove proposal-to-activation behavior and record actual planner/daemon limitations.

**Files:** Create `web/bridge/factory_revision_integration_test.go`; update `docs/studio-fleet-factory/factory-plan-revisions-design.md`, `docs/studio-fleet-factory/factory-plan-revisions-spec.md`, `docs/studio-fleet-factory/README.md` and this plan.

**Steps:** Use existing `TestFactoryEndToEnd` fixture patterns, fake MCP/planner and disposable Git remote. Exercise old turn continues -> proposal -> assessment -> authorization -> wait/interrupt with correlated receipt -> old effect settles -> activation -> fresh implementation/CI/review on new revision. Cover missing planner with manual delivery choice, ready PR redraft, a direct steering event, exhausted allowance, lost responses at assessment/delivery/activation/follow-up, restart without duplicate, and prior approval/review history retained. Tests: `TestFactoryRevisionEndToEnd`, `TestFactoryRevisionEndToEndInterruptRecovery`, `TestFactoryRevisionEndToEndFollowUp`, `TestFactoryRevisionEndToEndLegacyReplay`. Explicitly assert no old review marks the new revision ready. Verify Studio at 390px and desktop, including disconnected planner, pending/sent/uncertain and conditional settings. Live planner/daemon integration is opt-in using a disposable project; if unavailable record untested, never treat the fake as a live pass. Add implementation record with base, exact tests and evidence; keep historical documents intact.

**Verify:** `go test ./web/bridge -run '^(TestFactoryRevision|TestMCPRevision|TestFactoryEndToEnd|TestWebIsStdlibOnly)' -count=1`; `cd web/ui && npm test && npm run check && npm run build`; `CGO_ENABLED=1 go build ./cmd/marshal`; `go build ./cmd/webbridge`; `go test ./...`; `go vet ./...`; `git diff --check`; `gofmt -l` on changed Go files has no output. Capture the current-main baseline first and distinguish existing failures. Include embedded SPA assets according to repo convention. Commit `factory-revisions: task 6 — verify and document`.

## Integration notes

The user approved the implementation specification and plan on 2026-11-16. The factory's journal is the owner of authorization and evidence; the planner supplies assessments, not new independent control authority. Avoid assuming in-flight Claude or P5 work has landed. A queued/send-now request and a runtime `delivered` receipt are not equivalent to an agent executing the change. Uncertain effects, unobserved detached work and old-revision results remain unresolved until proven or explicitly reconciled.

## Implementation record

Delivered on branch `factory-revisions`, every task committed
as the plan directed, and the approved task text above is unchanged. The header's
historical base `1bc6d64b` was not reproduced: execution started from `8e983911`,
the commit that added the approved design, specification and this plan, so the
work sits on top of exactly the documents it implements. No unrelated branch was
merged and nothing under `internal/...` or `cmd/` was modified.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | Proposal journal: `FactoryRevisionProposal`, append-only history, `ProjectSettings.AutoAssessPlanChanges`, the two grant booleans | `65f468cf` |
| 5 | Studio controls: `FactoryRevision.svelte`, factory item, project settings, clients panel, typed `factory.ts`/`api.ts` | `79bd7e82` |
| 2 | Assessment: `revisionPlanner` seam with the unavailable default, intents and claim/result/retry, the three MCP tools, delegated authority under the policy lock | `7ddc8184` |
| 3 | Authorization and activation: the three revision commands, safe-boundary gate, ready-PR redraft, follow-up intent and reconciliation, server-derived Studio identity | `92475ce9` |
| 4 | Delivery: `RevisionSteeringState`/`Receipt`, `RevisionSteeringStatus`/`RevisionSendNow`, receipt correlation, `wait_safe`/`interrupt`/`hold`, pause and explicit resume | `11c8fd87` |
| fix | `factoryWorkers.Observe` reports a fully parked item as known-idle, so the ready-PR redraft is reachable in production | `29272851` |
| 6 | `web/bridge/factory_revision_integration_test.go` and this record; controller commits it after the documentation lands | task 6 |

The tasks ran 1, 5, 2, 3, 4, 6 rather than in numeric order: task 5 consumes only
task 1's types and shares no file with tasks 2–4, so the Studio controls could
land first. Task 6's four tests are `TestFactoryRevisionEndToEnd`,
`TestFactoryRevisionEndToEndInterruptRecovery`,
`TestFactoryRevisionEndToEndFollowUp` (with the subtest
`ready PR returns to draft before activation`) and
`TestFactoryRevisionEndToEndLegacyReplay`; they drive an old turn continuing, a
proposal, assessment, authorization, an interrupt with a correlated receipt, the
old effect settling, activation, and a fresh implementation/CI/review on the new
revision, plus the missing-planner manual choice, ready-PR redraft, a direct
steering event, the exhausted allowance, lost responses at assessment, delivery,
activation and follow-up, restart without duplicate, and retained prior
approval/review history. Each asserts that no old review marks the new revision
ready.

### Deviations from this plan, and why

| Planned | Delivered | Reason |
|---|---|---|
| Base current `main` at `1bc6d64b` | Base `8e983911`, the approved-documents commit | The plan's base predates its own documents; starting from the commit that carries them keeps the diff to the work described |
| Task 1 also modifies `web/bridge/projects.go` | Not modified | The new setting is optional (nil means true) and round-trips through the existing project-settings path |
| Ready-PR redraft recorded as an open limitation if unreachable | Fixed in `29272851`; the subtest runs and passes | The blocker was a real defect — `Observe` skipped parked workers, so a fully parked ready item was never known-idle. A parked worker has no runtime, hence no turn and no active work. The activation gate is unchanged and its idle-safety tests still pass |
| The integration test began with a conditional `t.Skipf` | The guard now fails (`t.Fatalf`) | A regression must be visible; the ready transition is reachable and covered |
| Task 6 creates the integration test only | Also appended this record and the design, specification and README records | The plan's own step requires the implementation record; the approved text is preserved and the records are additive |

### Acceptance gates, measured on this branch

| Gate | Result |
|---|---|
| `go test ./web/bridge -run '^(TestFactoryRevision\|TestMCPRevision\|TestFactoryEndToEnd\|TestWebIsStdlibOnly)' -count=1` | `ok marshal/web/bridge 1.951s`; 47 revision/MCP tests pass, none skipped |
| `go test ./web/bridge -run '^TestFactoryRevisionEndToEnd' -count=1 -v` | PASS — four tests; the ready subtest runs, no SKIP |
| `cd web/ui && npm test` | PASS — 115 files, 1229 tests |
| `cd web/ui && npm run check` | 5 errors in the same 5 pre-existing files as the base (`AgentConversation.svelte:754`, `Run.svelte:336`, `fleet.test.ts:78`, `Review.test.ts:49`, `Runs.test.ts:15`), 4 warnings; no new error |
| `cd web/ui && npm run build` | PASS — `built in 4.90s`, `web/bridge/static` regenerated and left in the tree |
| `CGO_ENABLED=1 go build ./cmd/marshal` | OK |
| `go build ./cmd/webbridge` | OK |
| `go test ./...` | PASS — exit 0; the two known load-sensitive flakes did not appear this run and are untouched by this diff |
| `go vet ./...` | clean |
| `git diff --check` | clean |
| `gofmt -l` on the changed Go file | no output |

### What remains untested

Live planner and live daemon integration are **untested**, not passed: both are
opt-in and need an explicitly configured disposable project and credentials,
which this run did not have. The planner, the steering runtime and the forge in
the end-to-end tests are scripted fakes, so nothing here is evidence of a live
endpoint. The Studio walkthrough at 390px and desktop — disconnected planner,
pending/sent/uncertain receipts, conditional settings — is also **untested** in a
browser, covered only by the component and view tests and `svelte-check`.
