# Factory work-item completion and linked pull requests: specification

Date: 2026-11-16

Status: implementation specification for the [approved design](factory-completion-design.md).

Plan: [factory completion](plans/2026-11-16-factory-completion-plan.md).

## Outcome and boundaries

A work item can have multiple pull requests and remains open until a human explicitly completes it. A PR merging or closing, whether through Studio or elsewhere, is never a completion decision. Reopening restores the business status to open and retains the decision and merge history; it neither starts a worker nor creates a PR. The first release links PRs only from the item's configured repository and supports remote merges only where the forge explicitly implements the capability. GitHub is the first target. Existing factory delivery still owns its one branch and delivery PR; extra links do not change its ownership, review, CI, or publishing rules.

Keep completion independent of `FactoryItem.State`, which describes the execution lifecycle and may already be `closed` because the delivery PR closed. Old journal records have no completion decision: interpret their business status as open, including old closed execution items. Do not rewrite old records or infer a historical user decision. Completion does not delete workspaces or discard changes.

## Linked PRs and evidence

Add a separate linked-PR collection keyed by repository identity and immutable forge PR identity; do not interpret `FactoryItem.PRs` (delivery history) as a list of simultaneously linked PRs. Automatically associate the existing delivery PR when observed. A user can link a PR by number or URL; resolve it through the configured forge, verify it belongs to the configured repository, deduplicate by identity, and retain its factory or user provenance. An unavailable forge leaves previously stored links visible with their last observation marked stale. Preserve links and historical observations through completion and reopening. Present the PR title/number or URL, origin, state, head revision and evidence freshness. Disallow a cross-repository link with a clear explanation.

When available, an explicitly invoked assessment adapter can compare the approved work-item goal and acceptance criteria with linked PR evidence and known gaps. Store the result, timestamp, source and a digest of those inputs; mark it stale if any input changes. Show pending, unavailable, failed or inconclusive states honestly. No assessment outcome gates completion; do not create a planner session merely to complete an item. The initial implementation uses an optional adapter and presents unavailable when no capable planner is registered.

## User actions

On desktop and mobile, the work-item completion surface shows the goal, criteria, assessment and freshness, linked PRs and their observed states, and the active-run state. It offers **Complete work item**, **Merge PR**, and **Merge selected PRs and complete**. A single selected PR uses the same ordered-batch machinery. Completing with open, unavailable, closed-unmerged or unready PRs, or unmet/unknown criteria, is allowed after showing the evidence. A confirmation names the requested action, PR order and chosen merge strategy, and binds the item version and each PR's identity, head SHA and base branch. Where merge options are offered, use only supported forge options; default to merge commit when available and require an explicit selection otherwise. Never silently switch strategy.

For a merge batch, let the user choose and reorder open linked PRs. Before each merge re-fetch and verify the repository, immutable PR identity, head SHA and base branch. Reject changed evidence for a fresh review; normal forge permissions and branch protection remain authoritative. An already externally merged PR at the reviewed head can be recorded as externally merged, not as a merge performed by Marshal. A changed or unverifiable head, closed-unmerged PR, permission error, unsupported capability or uncertain outcome stops the batch. Display succeeded, failed/uncertain and unattempted entries separately. Never undo a successful merge or execute later entries after a stop. Let the user review and explicitly retry, start a new batch with remaining PRs, or complete separately with the remaining PRs open.

One confirmation authorizes completion only after every selected merge has been confirmed. If any merge fails or is uncertain, do not complete automatically. If all are confirmed but completion persistence fails, retain the authorization and recover the pending completion without merging again. For merge-only, do not change completion status. Each successful completion records user provenance, time, acceptance and assessment context, link observations, and the run choice; subsequent changes append history rather than rewriting that record. A new completion after reopening creates a new record.

## Active run

If the item's own run is active or its state cannot be established at the point of confirmation, explicitly show its state and let the user choose **Continue run** or **Request stop**. The choice applies only to this item's run. A stop request is journaled and delivered using the existing worker cancellation path; show requested, confirmed stopped, failed or unknown according to actual observation, not the button press. Completion and stop confirmation are distinct: an accepted completion remains complete if a stop fails. Continued work and later events remain visible without reopening the item.

If the delivery PR closes independently, retain current safe closure and worker cleanup behavior. If the user has explicitly chosen to continue and the selected delivery PR is merged by the combined action, let the in-flight work settle, preserve its checkout and output, and prevent further automatic publish or dispatch against the closed PR. Show a delivery decision needed; do not silently create a replacement PR. Continuing without merging that delivery PR leaves normal run behavior intact. Never treat an external PR closure as an instruction to keep a run going.

## Durability, authorization and concurrency

Use the factory journal and versioned commands. Persist the ordered batch, reviewed evidence, request identity, authorization, and each per-PR intent before dispatch. Distinguish prepared, dispatched, confirmed, failed, uncertain and unattempted outcomes. Serialize mutations per item; duplicate request IDs return the original result only for identical payloads. On restart or a lost response, query the forge for dispatched/uncertain entries before any further mutation. A PR confirmed merged at the reviewed head can be recorded with honest unknown/external provenance if the actor cannot be proven. If it cannot be proven, stop and ask the user; never blindly retry or dispatch the next entry. Recovery can finish an already authorized completion after all batch entries are proven merged. Do not claim exactly-once execution across the network.

Enforce the existing Studio project and repository authorization for all reads and mutations. These actions require a user-originated Studio decision in this release; existing MCP grants, factory autonomy or planner assessments do not grant merge or completion authority. Do not expose new MCP mutations implicitly. Do not persist credentials in the journal. The `web/` boundary remains standard-library only.

## Acceptance

Tests must demonstrate legacy replay, multiple links and provenance, duplicate and cross-repo rejection, stale forge/assessment evidence, standalone completion with open PRs, reopening without starting work, and real stop observation. Exercise ordered batches, changed heads, external merges, errors and lost responses at each durable boundary, restart, duplicate requests and concurrent confirmations. Verify delivery-PR closure and continuing-work behavior without weakening existing CI or ownership checks. Check desktop and narrow mobile flows, keyboard-accessible ordering and confirmation, and clear partial/uncertain recovery. Use fake forges and isolated repositories; report any live forge and real planner testing separately.
