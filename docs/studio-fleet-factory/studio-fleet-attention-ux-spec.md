# Studio Fleet attention and recovery experience

Status: product direction approved 2026-11-17; Flutter port proposed for review 2026-11-18. See the linked implementation plan.

## Flutter port constraints

The target is Flutter `app/`, not the retiring Svelte Studio. Follow the [normative Flutter design system](../studio-flutter/design-system.md), existing kit and UI/model/client boundaries. The implementation plan identifies current screens and tests. Recheck in-flight changes before implementing and preserve already-working behavior; fixture evidence is not a live-service claim.

## Intent and scope

Make Fleet and Inbox useful when multiple projects have work in different states. A person should see what needs attention, what is ready to review, what is running, and which data is stale or unavailable; each entry should lead to the actual place to understand or act. Build on the existing Fleet-first shell, work-item experience and visual foundation rather than adding a competing dashboard or polling service.

## Attention and readiness

Fleet remains the concise cross-project overview; Inbox remains the detailed queue. Use the existing factory store, fleet inbox snapshot, network decisions and automation results. The Flutter `InboxModel` already includes Factory approval-draft, decision, recovery and completion asks. Preserve and improve these existing entries and include source-reported ready items where available, preserving separate identities and sources for a factory decision and any agent approval/question, even if they concern the same worker. Do not collapse them into one implied approval. Keep independent agents distinct from factory-owned workers and preserve the current Mine/Everyone scope for sources that have an owner; where factory ownership is not represented, label scope as project-wide instead of guessing an owner or hiding work.

Each attention entry shows source, project, item/agent name, concise reason and the existing direct action. Ready means the source actually reported ready; unknown checks or an unavailable feed must not appear as passing. Do not claim a work item is complete simply because its delivery is ready or a PR merged. Running and earlier states should use source data and avoid invented percentages or durations. Retain explicit inline agent/network actions where they already exist; new factory entries navigate to their existing item/decision, not issue a new mutation.

## Recovery and freshness

Show failure or staleness per source with the last verified update where known; a factory read failure must not hide healthy agents, and an agent/inbox failure must not be presented as an empty successful queue. Preserve the last trustworthy snapshot with an explicit stale label when the store already has one. Where no snapshot exists, say unavailable. Provide existing retry/refresh paths and route to existing recovery or decision panels; a refresh is not a retry of a factory mutation. Network, authorization, stale-data and unresolved operation outcomes stay distinguishable when the source can distinguish them; otherwise use unknown.

Order the user's next steps predictably: actionable needs first, then reported ready, then running and earlier work. Within a group retain the authoritative source ordering where available; for mixed sources use a deterministic timestamp and stable identity tie-breaker without inventing timestamps. Keep project context, source label, status, action and reason readable at phone widths. Provide empty and filtered-empty states, accessible navigation and no color-only state. No automatic action or new external notification channel is introduced.

## Validation

Test mixed projects and sources, multiple outstanding requests, the same worker with a separate factory decision, missing/unknown owner, stale and unavailable individual feeds, disconnected/reconnected streams, ready versus complete, and older snapshots after a failed refresh. Verify counts match visible rows and scope, stable ordering, route identity, no duplicate polls or mutations, and no loss of healthy results. Check phone/desktop, keyboard/focus, reduced motion and dark/light contrast. Real bridge and mocked fixture coverage must be reported separately.
