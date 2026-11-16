# Active factory plan revisions — feature specification

Date: 2026-11-16
Status: implementation specification drafted from the approved [product design](factory-plan-revisions-design.md); plan approved 2026-11-16.

## 1. Outcome

A user can propose a plan/specification revision during factory work, see an assessment and authorize the exact change. Work continues under the previous approved revision while assessment or authorization is pending. A planner with appropriate delegated authority may handle routine clarifications, recommend amendment versus linked follow-up and choose whether to wait or interrupt. Pending, sent and actually delivered are distinct. Existing direct user steering remains available.

The implementation must work without the in-flight Claude ACP planner: save/review/authorize manually with an unavailable planner and keep automatic assessment requests pending until a configured compatible planner becomes available. Do not build an LLM planner or claim an MCP client can be woken through MCP. This slice does not add cross-repository plans, automatic merge, an unlimited repair budget, or rewrite previously recorded reports.

## 2. Model and states

Add a bounded, append-only proposal history to each `FactoryItem` in its existing checksummed journal. A proposal has server-generated ID, immutable base approval revision and plan hash, immutable candidate plan/spec bytes and candidate hash using the existing `FactoryPlanHash`, creator provenance, version, timestamps, assessment state/result, authorization state, selected routing (`amend` or `follow_up`), delivery strategy/evidence and optional linked item ID. Preserve existing item/approval/effect versions and legacy journal bytes/validation when no proposals exist; old records replay without migration. Limits match `factoryImportLimit`; don't include project/repo/worker configuration edits in plan/spec proposals.

User-authored submissions work for queued, implementing, reviewing, checking, repairing, needs-input and ready items with an approved revision. Draft items use the existing factory-draft editing flow; closed, stopped, stopping and recovering items reject creation with a reason. If state changes after a proposal was saved, retain it and show what action is possible. One unresolved proposal per item in this slice; a replacement explicitly supersedes the old proposal without deleting it and binds to the then-current approved revision. A subsequent change to candidate content creates a new proposal; stale base or stale item/proposal versions conflict rather than silently rebase. A proposal alone does not change approval or block progress/ready under the existing approved scope. If work reaches ready while proposal is pending, show that ready applies only to the prior revision and keep the proposal available for decision.

Assessment states: `not_requested`, `pending`, `assessing`, `assessed`, `unavailable`, `uncertain` and `failed`. Project setting `autoAssessPlanChanges` is optional with effective default **true**; the proposal editor shows the current value and lets the user change it via the project's existing conditional settings update. Save with effective auto true persists one assessment intent; false saves as `not_requested`, and manual assessment creates the intent. An intent identifies proposal ID/candidate hash/base revision and an immutable request ID. A pending request is not proof it was dispatched. With no registered compatible planner, show pending/unavailable and keep existing work running. Externally connected MCP planners may poll for assigned pending assessments; their connected state is not proof of availability. With an available Studio-hosted planner adapter, dispatch via a capability-checked injected seam. Never synthesize or wake an external MCP turn merely by saving. On lost response or planner disconnect, mark the attempt uncertain; do not dispatch it again automatically. A human may explicitly request a new assessment after reviewing the uncertain attempt.

The assessment records classification (`routine_clarification`, `scope_change`, or `needs_user`), reasons, affected work, recommended routing (`amend`/`follow_up`) and strategy (`wait_safe`, `interrupt`, `hold`). It is advisory and tied to candidate hash/base and the actual worker/effect/revision observed. When that target changes before authorization or delivery, refresh the assessment or ask the user; do not silently act on stale assumptions. Work not identified as affected is never interrupted. The planner may request a pause as soon as assessment identifies material rework risk or out-of-authority uncertainty; this is a recorded request, not proof that work stopped. If interruption/pause cannot be proven through the runtime, show requested/unknown and never reuse an affected checkout until known idle under existing factory observation rules. Pausing cannot stop detached work by assumption.

## 3. Authorization and routing

User authorization of a proposal is explicit and conditional on current item version, proposal ID/version, candidate hash and base approval revision. Record identity and time. The planner can authorize only an assessment classified `routine_clarification` when the project's existing `plannerDelegationEnabled` is true, the designated planner identity is the same as the assessor and it has an explicit new `approveRoutineRevision` capability. A classification is a trusted planner's assertion within the user's grant, not a machine proof that scope cannot change. Scope changes and `needs_user` always require user authorization. Recheck toggle, designation and capability at the authorization linearization point using the existing MCP factory policy lock; a client cannot grant itself authority or use `user_directed` as verified human approval. Keep existing MCP `answerQuestion` and `approveAndQueue` meanings unchanged. Store provenance in the same journal mutation.

User chooses `amend` or `follow_up`, informed by the assessment. For follow-up, the original approved scope and its active worker are unchanged. Create the linked item only after the user confirms its draft; use a durable intent and deterministic request ID so a response loss cannot create duplicates. The new item starts as an unapproved factory draft with its own configuration and normal preflight; show its link in both histories after association. If creation or linking is uncertain, retain the intent and reconcile or request input. A follow-up proposal is not complete merely because an item ID was sent to the client.

For an amendment, authorization creates a durable **authorized pending activation**, preserving the candidate bytes and prior approval identity. Do not append a new active `FactoryApproval` while an old factory effect is running: current scheduling and `BuildFactoryPrompt` use the last approval, so doing so would reinterpret an in-flight operation. The existing approval remains active until the controller observes the old effect complete and the relevant worker/checkout safely idle. The user's authorization remains attached to the proposal, even if the planner disappears. If the current PR is ready, return it to draft through the existing journaled, identity-checked `set_draft` effect before activation; if that action fails or is uncertain, remain pending and do not claim the PR is safe to change. Once the safe boundary is verified, append exactly one new `FactoryApproval` containing the candidate text and existing config, then mark the proposal activated. An intervening approval makes its base stale and requires a fresh proposal/authorization.

The existing round allowance remains in force; an amendment does not silently grant extra rounds. If fresh work is needed and the allowance is exhausted, show the existing limit/retry flow. Preserve previous approval, round, CI and review entries. A new revision requires fresh implementation/report and revision-bound review/CI before readiness; old clean results cannot satisfy the new revision, even if the Git SHA matches. If a work item has already been marked ready for its old scope, display that distinction while the pending proposal is resolved.

## 4. Delivery, interruption and recovery

An authorized amendment records delivery intent before invoking any worker. When the planner is available, use its assessed strategy; the user may override it. When the planner is unavailable, the user explicitly chooses `wait_safe`, `interrupt`, or `hold`. `hold` leaves delivery pending; no automatic fallback is implied. `wait_safe` lets the old turn finish and supplies approved direction in the next factory prompt after activation. It does not label the direction delivered before that prompt is accepted. `interrupt` requests existing `session/steer` Send now with a stable request ID, target turn ID and candidate hash. Record `accepted`, `interrupting`, `waiting_safe_boundary`, `waiting_for_resume`, `delivered` or `undelivered` only from matching runtime receipts; a successful ordinary queued steer is not a delivery receipt. The UI explains `delivered` means delivered into the runtime, not proof the agent followed it or completed work.

Current `Registry.Steer` discards the result and sends no delivery/request ID. Add a bridge-local adapter over the existing wire methods rather than importing `internal/...` from `web/`. A receipt must match the session, turn and request ID in the persisted intent; that intent binds the request ID to the immutable candidate hash. The runtime receipt itself does not carry the candidate hash. Send now can alter a currently running old-revision turn; that turn's existing effect and report remain attributed to its original approval revision and cannot satisfy the new revision. After it settles and checkout/worker are known idle, activate the new approval and schedule fresh revision-bound work. Do not run two workers on the same checkout or count an interrupt request as an idle observation. If receipt recovery cannot prove the outcome, mark `uncertain` and require reconciliation or explicit user input; never blindly resend or mark applied. Direct human steering remains possible under existing behavior and is recorded without claiming it was this proposal's delivery.

## 5. Interfaces and UI

Use existing authenticated factory item and command APIs with additive command kinds and structures; protect them with `expectedVersion`, stable `requestId`, item ownership and project authorization. Add proposal creation, rejection, assessment request/submission, authorization, explicit delivery strategy, supersession and follow-up confirmation. A paused worker needs an explicit user resume if the proposal is rejected or superseded; neither action silently restarts work. Define bounded schemas in the implementation plan. MCP clients may read accessible proposals and, with a separately granted `assessPlanChanges` capability, submit an assessment for their assigned intent. A separate `approveRoutineRevision` capability plus project delegation controls routine authorization; no existing MCP grant silently authorizes plan revision. Authorize through the existing client/project/repo policy lock, preserve namespaced request replay, and record client-reported direction as an assertion. Project UI exposes and revokes these capabilities using the existing conditional grant editor. A client cannot self-designate or alter the toggle.

Studio shows current versus proposed plan/spec with accessible comparison, editable proposal creation, auto-assessment setting, assessment/provenance, explicit authorization and routing, delivery options and receipts. Show conflicts and retain unsaved proposal text. Present current approved revision and old/new evidence separately on desktop and 390px screens. The factory's existing `answer`, `retry`, `stop`, route changes, grant accounting and MCP tools remain available and unchanged unless a conflicting command is rejected with a clear reason. No live modification of an already published approval or running worker's mutable context is implied by a saved proposal.

## 6. Verification

Use fake clocks, isolated project/factory stores, scripted planner/worker seams and disposable Git repositories. Test default-on setting and project override, save with planner absent, external planner polling without fake wake-up, matching/stale assessment and auth, delegation and revocation race, supersession, direct user authorization, follow-up creation/replay, old-revision work continuing, safe-boundary activation, send-now receipt matching, interruption with detached work, response loss and restart at every persisted intent, no duplicate delivery, previous evidence retention, fresh revision/CI/review readiness, ready-PR return to draft, allowance exhaustion, legacy factory journal replay and existing MCP permissions. Test two-browser conflict and phone/desktop UX. Opt-in live planner/daemon proof must identify actual versions and credentials; record untested when unavailable.

## Implementation record

Delivered on branch `factory-revisions` from base `8e983911`, the commit that
added this specification and its plan; the approved text above is unchanged and
this section is the record. One commit per task, then the fix and this record.

| Task | Deliverable | Commit |
|---|---|---|
| 1 | `FactoryRevisionProposal` and the append-only proposal journal, proposal/assessment/authority/delivery fields, `ProjectSettings.AutoAssessPlanChanges` (nil means true) and the two new grant booleans | `65f468cf` |
| 5 | `FactoryRevision.svelte` and the Studio controls in the factory item, project settings and clients panel, over the typed `factory.ts`/`api.ts` contract | `79bd7e82` |
| 2 | The instance-owned `revisionPlanner` seam with the goroutine-free `unavailableRevisionPlanner` default, durable assessment intents and `revision_assess_claim`/`_result`/`_retry`, the `factory_revision_pending`/`_claim`/`_assess` tools, `WithAuthorizedRevisionAssessment` under the policy lock | `7ddc8184` |
| 3 | `revision_authorize`/`revision_delivery_choice`/`revision_follow_up_confirm`, the authorized-pending-activation record, the safe-boundary gate, the ready-PR `set_draft` redraft, the follow-up create intent and reconciliation, server-derived Studio identity | `92475ce9` |
| 4 | `RevisionSteeringState`/`RevisionSteeringReceipt`, `Registry.RevisionSteeringStatus`/`RevisionSendNow`, `steering_receipt` correlation against the candidate-bound intent, `wait_safe`/`interrupt`/`hold`, pause journaling through `Registry.Cancel`, explicit resume | `11c8fd87` |
| fix | `factoryWorkers.Observe` reports a fully parked item as known-idle, making the activation gate satisfiable for a ready item | `29272851` |
| 6 | `web/bridge/factory_revision_integration_test.go`, this record, the design record and the README entry | task 6, committed by the controller after this documentation |

The tasks ran in the order 1, 5, 2, 3, 4, 6: task 5 consumes only task 1's
types, so the Studio controls could land before the planner and delivery work
whose files they do not share.

Files, from `git diff --name-only 8e983911..HEAD`. New in `web/bridge`:
`factory_revision_types.go`, `factory_revision_planner.go`,
`factory_revision_controller.go`, `factory_revision_delivery.go`, their four
`_test.go` files and, from this task, `factory_revision_integration_test.go`.
Modified: `client.go`, `factory_controller.go`, `factory_effects.go`,
`factory_http.go`, `factory_store.go`, `factory_types.go`, `factory_workers.go`,
`mcp_factory_auth.go`, `mcp_factory_tools.go`, `registry.go`, `workspace.go` and
the matching tests. In `web/ui`: `src/lib/FactoryRevision.svelte` and
`src/lib/FactoryRevision.test.ts` are new; `src/lib/factory.ts`,
`src/lib/api.ts`, `src/lib/ClientsPanel.svelte`, `src/views/FactoryItem.svelte`,
`src/views/Project.svelte` and their tests are modified. Nothing under
`internal/...` or `cmd/` is touched, `web/` stays Go standard library only, and
`TestWebIsStdlibOnly` passes.

### End-to-end proof

`web/bridge/factory_revision_integration_test.go` runs the lifecycle on the
delivery fixture: a disposable bare Git remote, a scripted forge, real worker
sessions on a scripted child transport and the real journal. Real here are the
journal, the scheduling derivation, the remote, the checkouts and the
publish/CI/review chain. Scripted are the planner's judgement
(`fakeRevisionPlanner`), the steering runtime's answers and receipts
(`revisionRuntimeFake`) and the forge. All four tests pass:

| Test | What it proves |
|---|---|
| `TestFactoryRevisionEndToEnd` | The old turn keeps running under its revision while a proposal is saved, assessed and authorized; Send now names the observed turn and request identity and carries the candidate; a direct steering event stays an ordinary queued steer and moves no delivery evidence; the old effect settles and only then does approval 2 activate; the new revision earns its own implementation, CI and review; the old round's review survives; exactly one `set_ready`, under revision 2; the prompts show `exact plan` before activation and `revised plan` after; the journal replays with no second approval |
| `TestFactoryRevisionEndToEndInterruptRecovery` | A lost assessment answer is `uncertain` and is retried only by an explicit `revision_assess_retry` that retires the old attempt; a lost Send now outcome is `uncertain`, is never resent on its own and blocks activation; a restart re-derives the same state with one intent; a receipt naming another turn is rejected and the correlated one is recorded; the recovered item still reaches ready with one activation and one round granted |
| `TestFactoryRevisionEndToEndFollowUp` | A follow-up is authorized, confirmed and created once as an unapproved draft naming its source, and the source keeps its ready revision; the confirmation replays without creating a second draft; a ready item's amendment returns the PR to draft (`set_draft`, confirmed) before activation, keeping one `set_ready` per revision; a lost association is restored by restart reconciliation without a duplicate; old rounds and reviews stay intact |
| `TestFactoryRevisionEndToEndLegacyReplay` | An item journalled before proposals existed keeps its legacy approval bytes exactly, records the assessment `unavailable` with no planner and no background retry, requires an explicit delivery choice, reaches `waiting_safe_boundary` under `wait_safe`, activates at the safe boundary, carries the candidate in the next factory prompt, and then hits the existing exhausted-allowance decision without a new grant; the whole item replays byte for byte |

Each of these asserts that no old review marks the new revision ready: the
readiness assertions count `set_ready` effects by `ApprovalRevision` and require
exactly one at the new revision, and the new round's review, CI and head SHA must
be its own.

### Deviations from the plan, and why

| Planned | Delivered | Reason |
|---|---|---|
| Tasks in numeric order | 1, 5, 2, 3, 4, 6 | Task 5 consumes only task 1's types; the Studio and bridge halves have disjoint files |
| Task 1's file list named `web/bridge/projects.go` | Not modified | The setting round-trips through the existing project-settings path; `AutoAssessPlanChanges` is optional with nil meaning true, so no handler changed |
| Task 6 was to record the ready-PR redraft as an open limitation | `29272851` fixes `factoryWorkers.Observe`, and the integration test's ready subtest runs and passes | The blocker was a real defect, not a supported limitation: a parked worker has no runtime, so a fully parked item is known-idle. The activation gate is unchanged and its idle-safety tests still pass |
| The integration test began with a conditional `t.Skipf` for that shape | The guard now calls `t.Fatalf` | A regression must fail, not silently skip: the ready transition is reachable and is asserted |
| — | The ready subtest is `TestFactoryRevisionEndToEndFollowUp/ready PR returns to draft before activation` | The plan named four top-level tests, all delivered; this shape is a subtest of the follow-up test, which owns the ready fixture |

### Acceptance gates, measured on this branch

| Gate | Result |
|---|---|
| `go test ./web/bridge -run '^(TestFactoryRevision\|TestMCPRevision\|TestFactoryEndToEnd\|TestWebIsStdlibOnly)' -count=1` | `ok marshal/web/bridge 1.951s`; 47 top-level revision/MCP tests pass, none skipped |
| `go test ./web/bridge -run '^TestFactoryRevisionEndToEnd' -count=1 -v` | PASS — all four tests, and the ready subtest runs (`--- PASS: TestFactoryRevisionEndToEndFollowUp (0.49s)`, no SKIP) |
| `cd web/ui && npm test` | PASS — 115 files, 1229 tests |
| `cd web/ui && npm run check` | 5 errors, 4 warnings in 9 files — exactly the pre-existing set at `src/lib/session/AgentConversation.svelte:754`, `src/views/Run.svelte:336`, `src/lib/fleet.test.ts:78`, `src/views/Review.test.ts:49`, `src/views/Runs.test.ts:15`; no new error, same files and count as the base |
| `cd web/ui && npm run build` | PASS — `built in 4.90s`; `web/bridge/static` regenerated from this source and committed with it |
| `CGO_ENABLED=1 go build ./cmd/marshal` | OK |
| `go build ./cmd/webbridge` | OK |
| `go test ./...` | PASS — exit 0, no failing package. The two known load-sensitive flakes (`internal/tools/native` `jobs_test.go:475`, `TestSpawnWorkspaceTimeoutPausesTheAgent`) did not appear on this run and are unrelated to this diff |
| `go vet ./...` | clean |
| `git diff --check` | clean |
| `gofmt -l` on the changed Go file | no output |

### Limits the runtime imposes

- Studio identity for authorization is derived server-side. A browser cannot
  assert who authorized a revision, and no session ID or credential appears in a
  revision payload.
- MCP routine authorization stays on the task 2 assessment-result path under the
  existing policy lock; `scope_change` and `needs_user` always need an
  authenticated Studio user.
- The planner and `projectSettings` seams are nil-safe and are not wired in
  `cmd/webbridge/main.go`. Production runs the unavailable planner by default:
  the intent is recorded `unavailable`, nothing is synthesized, woken or retried,
  and the user chooses `wait_safe`, `interrupt` or `hold` explicitly.
- `wait_safe` keeps `waiting_safe_boundary` and never claims `delivered`. The
  activated approval is the evidence the next factory prompt carries the
  candidate; that is asserted through `BuildFactoryPrompt` and the implementer
  prompts the fixture captured.
- A `delivered` receipt means the runtime accepted the input. It is never proof
  that the model complied, that the work changed, or that any operation stopped.

### What remains untested

Live planner and live daemon integration are **untested**, not passed: both are
opt-in and need an explicitly configured disposable project and credentials,
which this run did not have. Everything the end-to-end tests call a planner,
a steering runtime or a forge is a scripted fake; no provider endpoint, real
keychain or running daemon was contacted.

The Studio walkthrough at 390px and desktop is **untested** in a real browser,
including the disconnected-planner, pending/sent/uncertain receipt and
conditional-settings states. Those states are covered by the component and view
tests (`src/lib/FactoryRevision.test.ts` has `plannerUnavailableShowsPendingAndChoices`,
`receiptSentIsNotDelivered`, `oldAndNewEvidenceAreDistinguished`,
`stacks the comparison on a phone and offers no dead controls without a proposal`,
`shows the effective automatic assessment and asks explicitly when it is off`,
`staleProposalRetainsText`, `manualFollowUpConfirmation`), by the project and
clients-panel suites, and by `svelte-check`; that is presentation evidence, not
a browser pass. A two-browser conflict on the same proposal is likewise covered
by the journal-level concurrency test and the UI conflict tests, not by two live
browsers.
