# Factory work-item completion and linked PRs

Date: 2026-11-16
Status: approved product design. See the [implementation specification](factory-completion-spec.md)
and [implementation plan](plans/2026-11-16-factory-completion-plan.md). Builds on
the [Studio decision record](design-notes.md) and existing factory lifecycle.

## Intent

A work item represents an outcome that may require more than one pull request.
The user, not a PR merge alone, decides when that outcome is complete. Make
that decision easy on desktop and phone, including when the user wants to merge
and complete in one action. Preserve the history needed to reopen work later.

## Agreed behavior

- Completion is a separate explicit user decision. A merged PR can be evidence
  of progress, but never automatically completes the entire work item.
- Offer **Merge and complete work item** for a selected PR and **Merge selected
  PRs and complete** for a set of linked PRs. Show the intended actions and
  the selected PRs before confirmation. One user confirmation authorizes the
  batch and the completion decision if all selected merges are confirmed; do
  not ask for another completion approval after a successful batch.
- Offer **Merge PR** without completing the item, and allow completion as a
  separate action. The user may complete an item with open linked PRs after
  seeing them; don't prevent the decision merely because a PR is still open.
- A batch attempts the selected PRs in an explicit, visible order and stops at
  the first failure. Show which merged, which failed and which remain untouched.
  Do not automatically complete after a failure. Let the user decide whether to
  retry, continue with the remaining PRs, or complete with open PRs. Merges that
  already happened must never be repeated or represented as rolled back.
- When available, a planner assesses the outcome against acceptance criteria,
  linked PRs and known gaps and presents the result to aid the user. A planner
  is optional: an unavailable, pending, failed or inconclusive assessment never
  prevents a user from completing. An assessment is not user approval.
- Reopening a completed item returns it to an open state without automatically
  starting agents or creating a PR. Retain the original completion, PR and
  merge history; the user decides what further work to start.
- When the item's own factory run is active at completion, show that fact and
  let the user choose whether it continues or is asked to stop. A stop request
  is not proof of stopped work; show the actual outcome. Do not stop unrelated
  work. If the run continues or finishes after completion, add its subsequent
  events and results to the item history without silently changing the user's
  completion decision or automatically reopening the item.

## Approaches considered

1. **Extend the existing work item (selected):** add an explicit completion
   decision, links to PRs and completion history. Preserve the current factory
   implementation flow while allowing one outcome to include several PRs.
2. **Create a separate parent feature above factory items:** naturally groups
   multiple one-PR items but adds a new lifecycle and navigation hierarchy
   before basic completion can work. A parent feature remains possible later.

## Proposed interaction

The completion view shows the work-item goal and acceptance criteria when
available, planner assessment and its freshness, linked PRs with their actual
forge state, and any incomplete or unknown evidence. It presents three clear
choices: complete without merging; merge selected and complete; or merge a PR
while leaving the item open. The user can select and order PRs, see which are
already merged, closed, open, unavailable or not currently mergeable, and review
what each selection would do. Missing forge access or an out-of-date assessment
is shown as such rather than reported as success. A failed merge is a recorded
outcome and the next user choice is obvious.

The existing factory creates one delivery branch and PR; a slice implementing
this design must add a way to associate multiple PRs with a work item rather
than assuming the existing `PRs` history is a collection of simultaneous linked
PRs. Preserve PR identity and provenance, and display whether an association was
created by the factory or the user. Initial scope is the work item's project;
any cross-project or cross-repository linking needs its own explicit design.

Completion records who decided, when, the acceptance context and the state of
linked PRs observed at the decision. The user may complete with open PRs or
unmet/unknown criteria; record what was known, including uncertainty, rather
than fabricating a clean assessment. Link later changes to a new completion
record instead of rewriting an earlier decision. Support a user-selected
reopen and show previous completion records in the item history.

## Merge, completion and recovery boundaries

The user confirmation identifies the item, exact ordered PR set, merge options
and a version of the displayed evidence. Recheck forge identity, current PR
state and relevant revision immediately before each merge. Use the forge's
normal authorization, branch protections and merge rules; do not silently
bypass a failed check or change merge strategy. Surface an unsupported forge or
missing permission as such.

Record a durable per-PR merge intent before each external mutation and its
confirmed result afterwards. A merge is confirmed only by authoritative forge
evidence, not by a button press or lost response. On a transport failure or
restart, reconcile actual PR state and revision before any retry. If the result
cannot be proven, stop the batch and show an uncertain outcome. No later PR is
attempted and no item is completed until the user chooses how to proceed.

After all selected PRs are confirmed merged, persist the separately authorized
completion. If completion fails after merge, show the successful merges and the
pending/uncertain completion action; recovery must not merge again. A single-PR
combined action follows the same rules. Concurrent clicks, stale work-item
versions, external merges or head changes must not create duplicate effects or
approve a PR revision the user did not review; show conflicts and refresh the
review.

The existing controller currently closes an item when its factory PR is merged
or closed. The implementation must separate that PR/worker lifecycle from the
user's completion decision while preserving safe cleanup, stop and recovery
semantics and compatibility with old journal records. It must not equate an
externally closed, unmerged PR with successful completion. Completing or
reopening is not by itself authorization to delete retained workspaces,
discard uncommitted changes, stop unrelated work, or start an agent. Workspace
retention and cleanup are a separate feature.

## Scope, constraints and validation

The first delivery targets the forge and PR capabilities the current factory
actually supports; other forges must be reported according to actual capability
rather than assumed to support merge. Keep `web/` standard-library-only, do not
import agent internals, and use the existing backend policy and authorization
boundaries. The planner assessment goes behind an optional capability seam;
this feature must work without the in-flight Claude planner. Recheck current
`main`, P5, active plan-revision work and forge contracts before implementation
planning. Do not require a parent feature or multi-project hierarchy.

Use fake forges and disposable repositories to test multiple linked PRs,
ordering, permissions and stale revisions; an external merge/closure; each
failure and lost response; restart after every persisted effect; simultaneous
confirmation; completion with open PRs; planner unavailable/stale; reopen with
no agent started; legacy journal replay and existing factory ownership. Check
phone and desktop interaction, accessible confirmation and failure recovery.
Record untested live-forge and planner scenarios as untested.
