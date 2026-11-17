# Studio work-item and worker experience

Status: product direction approved 2026-11-17; Flutter port proposed for review 2026-11-18. See the linked implementation plan.

## Flutter port constraints

The target is Flutter `app/`, not the retiring Svelte Studio. Follow the [normative Flutter design system](../studio-flutter/design-system.md), existing kit and UI/model/client boundaries. The implementation plan identifies current screens and tests. Recheck in-flight changes before implementing and preserve already-working behavior; fixture evidence is not a live-service claim.

## Intent and scope

Make it easy to understand a factory work item's current state, respond to what needs a person, follow its workers, and inspect evidence on desktop and phone. Build on the [visual foundation](studio-visual-foundation-spec.md), existing Fleet-first navigation, completion/PR flow, and the planned [worker-subagent experience](studio-worker-subagents-design.md). Do not change factory policy, backend transitions, existing review gates or completion semantics. This is a proposed follow-up and its details are implementation choices for user review.

## Behavior

`WorkItemScreen` already places `ItemAsks` above activity and offers plan revisions through the Plan tab. Retain that priority and make the revision path discoverable without displacing active decisions. Preserve unsaved revision text and in-flight state across refresh and worker drill-in where the existing route lifetime permits; if it does not, retain it in a model owned for the item. Keep existing decision and recovery actions accessible without duplicating them. A proposal must not be approved, discarded or interrupt current work automatically.

Replace raw `role: agentId` worker links with a compact worker list: readable role and known agent name, actual session state when available, and parked/unavailable/unknown where that is all the client knows. Retain an accessible way to inspect the agent ID, distinguish a parked worker from a missing agent, and never call an unknown agent running. Each item opens the existing shared worker conversation; phone uses the existing focused drill-in and return, desktop retains side-by-side context. Preserve selected worker and view through refresh and browser Back; don't automatically switch on a worker state change. Do not duplicate child agent controls planned in the subagent feature; add them to the shared worker surface when that feature is available.

Keep the activity timeline's recorded order, messages, provenance and new-activity/scroll behavior. Show relative event time with the exact timestamp available through a tooltip, following the Flutter design system; an invalid or absent timestamp remains visible as unknown rather than a fabricated date. Avoid announcing a failed, requested or uncertain event as successful. On phone, avoid fighting the page with unnecessary nested scrolling; preserve access to earlier history and the user's reading position when new events arrive.

The review, pull-request and documents views remain available and retain CI-first evidence, freshness and revision distinctions. Keep completion a separate user decision and keep merge order, partial-failure recovery, stop choices and existing authorization intact. The work-item header must distinguish execution state from completion state where provided, without implying that a merged PR automatically completed the item. No new status, agent or progress percentage may be invented from missing data.

## Verification

Exercise pending question, recovery, running, ready, complete, stopped, stale and unknown records. Test polling while typing an answer or revision, opening the revision sheet and entering/returning from workers, unknown/parked identities, relative/exact and invalid timestamp rendering, long histories, multiple PRs and completion actions. Use fixture-backed desktop/tablet/phone Flutter widget and browser checks, keyboard and focus checks, screen-reader labels, dark/light contrast and actual visible-content assertions. Use the existing real-bridge tests for real contracts and label mock-backed presentation evidence. Preserve routes, request IDs, decision IDs, item versions, user edits and current history; report real-device/live-service coverage separately.
