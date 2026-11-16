# Changing an active factory plan

Date: 2026-11-16
Status: product design, [implementation specification](factory-plan-revisions-spec.md)
and [plan](plans/2026-11-16-factory-plan-revisions-plan.md) approved 2026-11-16. Builds on [the Studio decision record](design-notes.md) and the current
factory approval, round, effect and recovery model.

## Intent

Let a user propose a plan or specification change while factory work is
running, understand its impact, authorize it within the existing project
delegation policy, and see how and when the change reaches affected work. The
factory preserves prior approved revisions and their evidence. The user can
still steer an individual agent directly; this feature does not force all
interaction through a planner.

## Agreed behavior

- Store change proposals, assessment, approvals and delivery state on the
  existing factory work item. The planner can create and assess proposals
  through a defined interface; it is not the sole owner of factory state.
- Saving a proposed plan or specification change triggers planner assessment
  automatically by default. The user can turn that behavior off, and the UI
  clearly shows whether it is enabled before saving a proposal. Saving a change
  is not itself approval or authorization to redirect work.
- Work continues under the existing approved revision while assessment or
  authorization is pending. The planner may decide to pause affected work if
  continuing risks material rework or requires a decision beyond its authority;
  unaffected work continues. If the planner is unavailable, visibly show that
  assessment is pending while the current work continues. The user can choose
  to pause or redirect work directly.
- After an approved change, the planner chooses the delivery strategy. When the
  active work can safely continue, deliver at an appropriate point. If
  continuing would likely cause rework or conflict, request an interruption or
  pause and deliver as soon as the runtime can safely do so. A request to stop
  is not proof that a tool, command or worker has stopped; uncertain outcomes
  must stay visible and be reconciled before checkout reuse.
- If the planner is unavailable when the user approves a change, the user can
  choose to deliver at the next safe point, request interruption, or leave
  delivery pending until the planner returns. These are explicit choices; an
  interruption request still needs confirmation from the runtime before work
  is shown as stopped or a checkout is reused.
- Follow the project's existing delegation setting: with the user opted in,
  a planner may handle routine clarifications within approved scope; a
  consequential scope decision goes to the user. The toggle is off by default.
  Existing authorized direct human steering is unaffected.
- Show proposed, pending assessment, awaiting authorization, approved, delivery
  pending, sent and acknowledged states accurately. Queuing or sending a
  message alone is not proof that it was received, applied or completed. If the
  runtime cannot provide a particular acknowledgement, show that limitation
  and do not imply stronger evidence.
- The planner may recommend amending the current work item or making a linked
  follow-up, depending on whether the change can reasonably be incorporated.
  The user retains the ability to choose the current work item. This routing
  choice does not expand delegated authority over scope.

## Approaches considered

1. **Factory-owned proposals and revisions (selected):** preserve the proposal,
   assessment, authorization and delivery evidence alongside the factory item,
   while allowing planner services and Studio to propose and assess changes.
   The factory already owns the approved revision, dispatch and recovery.
2. **Planner-owned change records:** the planner sends commands to the factory
   after approval. This makes a planner outage or transfer harder to reconcile
   and separates approval history from the controller that uses it.

## Proposed flow

1. Open a factory item and propose a change to the plan or specification.
   Show the approved text and proposed text together, with the affected item
   and current revision. The user can save a proposal without approving it.
2. Show the automatic-assessment control and its current setting. When on,
   schedule assessment after saving; when off, retain the proposal and let the
   user request assessment. If no planner is available, preserve the pending
   request and expose its state. Automatic assessment must not invent a
   planner session or silently transfer responsibility.
3. Show the planner's assessment, reasons, recommended amend/follow-up choice,
   affected work and whether a pause is requested. Proposed pausing is limited
   to the affected work and to the planner's delegated authority. If the
   controller cannot reliably pause or observe the relevant operation, show
   that as an unresolved state; do not falsely report it paused.
4. Record authorization against the exact proposed artifact contents and the
   prior approved revision. A changed proposal or stale base requires another
   assessment/authorization; concurrent changes must not silently replace an
   approved version. Distinguish a user authorization from a planner action
   under an enabled delegation policy and record provenance.
5. Record delivery intent before dispatch, choose continue or interrupt from
   the planner's assessment or the user's explicit choice when the planner is
   unavailable, and show actual delivery/acknowledgement evidence.
   If dispatch outcome is unknown after a disconnect or restart, reconcile or
   ask for input rather than blindly resending. Preserve history when a user
   directly steers the worker; do not claim it accepted a revision without
   evidence.
6. Tie subsequent worker reports, CI and reviews to the approved plan revision
   and Git revision they actually cover. A new approval cannot reuse an old
   clean review as proof for the new scope; already completed work and old
   evidence remain in history rather than being rewritten. The controller must
   not mark the new scope ready before appropriate current evidence exists.

The implementation design must define whether a proposal can be made for an
item in each existing state, how multiple proposals and supersession work, how
follow-up items are linked, which settings control automatic assessment, and
what acknowledgement the current daemon/ACP path actually supplies. Those are
builder decisions only where they preserve the agreed behavior above; if a
runtime contract cannot support safe interruption or a user-visible behavior,
report the constraint rather than silently weakening it.

## Constraints and scope

Keep the existing factory journal, approval history and idempotency protections;
existing retry and route-change behavior must remain valid. Don't assume a
running Claude ACP planner or a general worker abstraction has landed: the
Claude integration is in flight, so design against an injected planner seam and
report when it is unavailable. No new planner model is required for a user to
view or manually authorize a proposal. The existing project planner delegation
policy must be used rather than duplicated; reconcile with the recent MCP
factory handoff implementation and its per-client authority/provenance.

Maintain the `web/` standard-library-only and TUI client seam boundaries. The
implementation plan must check current `main`, the daemon P5 branch and the
Claude branch before modifying shared session, cancellation, factory or UI
interfaces. It must not require unrelated daemon work to land just to save and
review a proposal, or assert P5 already supports web relay. Handling active
worker interruption and acknowledgement depends on the actual runtime
contracts and must be tested, with unsupported cases made explicit.

## Validation

Use isolated stores, fake clocks, fake planner/worker adapters and disposable
repositories. Exercise pending assessment with and without a planner, visible
and configurable automatic assessment, delegated versus user authorization,
unrelated work continuing, pause request versus confirmed pause, concurrent
proposals and stale authorization, direct steering, acknowledgement versus
queued delivery, cancellation and loss of response, restart reconciliation,
revision-bound CI/review evidence and retry compatibility. Verify a
phone-width and desktop UI with no silent reordering or loss of proposal text.
Live planner/daemon tests are opt-in and should be reported as untested when no
configured endpoint is available. The implementation specification and plan
follow only after this design is approved.

## Implementation record

Delivered on branch `factory-revisions` from base `8e983911`, the commit that
added this design with its [specification](factory-plan-revisions-spec.md) and
[plan](plans/2026-11-16-factory-plan-revisions-plan.md). One commit per task;
the [specification's implementation record](factory-plan-revisions-spec.md#implementation-record)
holds the commit table, the deviations, the acceptance gates and the limits in
full. This section records what the delivered behavior is and what the runtime
actually supports.

The agreed behavior above is delivered as written. A proposal is journal
history: it changes neither the active revision nor a running turn. Assessment is
requested by default and is a durable intent before any planner is contacted.
Authorization binds the exact candidate bytes, candidate hash and base approval
revision, and records who authorized it. The old turn keeps the approval and
revision it started under; the candidate activates only at a verified safe
boundary, after a ready pull request has been returned to draft and that
transition confirmed. The fresh implementation, CI and review then run under the
new approval, and no result from the old revision can mark the new one ready.
Direct human steering remains the ordinary queued steer, recorded without being
mistaken for this proposal's delivery.

The ready-PR redraft is reachable and covered. It was blocked while
`factoryWorkers.Observe` skipped a parked worker entirely: a ready item's
workers are all parked, so `RuntimeKnown`, `TurnIdle`, `ActiveWorkKnown` and
`WorkIdle` all stayed false and `activateAuthorizedRevision` returned before the
`set_draft` transition. Commit `29272851` makes `Observe` report a fully parked
item as known-idle — a parked worker has no runtime, hence no turn and no active
work. The activation gate itself is unchanged and still requires the full
conjunction, so `TestFactoryRevisionInterruptDoesNotAssumeIdle` and
`TestFactoryRevisionDetachedWorkBlocksActivation` still hold. The integration
test's ready subtest, `TestFactoryRevisionEndToEndFollowUp/ready PR returns to draft before activation`,
now runs and passes, and fails rather than skips if that conjunction ever stops
being observable again.

### Runtime facts and limits

- Studio identity for authorization is derived server-side; a browser cannot
  assert who authorized a revision.
- MCP routine authorization stays on the task 2 assessment-result path under the
  existing `factoryMCPAuthorizer` policy lock. A classification remains the
  planner's assertion within the user's grant, never a machine proof of scope.
- The planner and `projectSettings` seams are nil-safe and are **not** wired in
  `cmd/webbridge/main.go`. Production therefore runs the unavailable planner by
  default: the intent is recorded `unavailable`, nothing is synthesized or
  retried in the background, and the user chooses `wait_safe`, `interrupt` or
  `hold` explicitly.
- `wait_safe` keeps `waiting_safe_boundary`; it never claims `delivered`. The
  activated approval is the evidence that the next factory prompt carries the
  candidate.
- A `delivered` receipt means the runtime accepted the input. It is never proof
  that the model complied with it or that any work completed.
- Live planner and live daemon integration is untested (opt-in, no configured
  disposable project in this run), and the Studio surfaces were not walked in a
  real browser at 390px or desktop. Neither is a pass: the planner, the steering
  runtime and the forge in the end-to-end tests are scripted fakes.
