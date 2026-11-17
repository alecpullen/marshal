# Studio planning, drafts and handoff experience

Status: product direction approved 2026-11-17; Flutter port proposed for review 2026-11-18. See the linked implementation plan.

## Flutter port constraints

The target is Flutter `app/`, not the retiring Svelte Studio. Follow the [normative Flutter design system](../studio-flutter/design-system.md), existing kit and UI/model/client boundaries. The implementation plan identifies current screens and tests. Recheck in-flight changes before implementing and preserve already-working behavior; fixture evidence is not a live-service claim.

## Intent

Make the existing planner, working drafts and factory handoff feel like connected steps while preserving their distinct persistence and approval contracts. A user should understand what is saved, what will travel to another device, exactly what is being handed off, and what action starts factory work. Use the approved visual foundation and existing project context on desktop and phone.

## Journey and state

Provide clear, project-scoped navigation among the planner, working drafts and factory handoff, with truthful labels describing where each action leads. The planner's live session and its captured snapshot, a working draft's autosaved content, and a factory item's approved revision are different artifacts: do not silently synchronize or copy one into another, promise cross-device persistence for unsaved planner text, or invent a new conversion endpoint. Where a transfer already exists, show its exact source and target. A user can return to the source after a handoff, and a link to a created item must refer to the item the server actually returned.

In working drafts, make saving, saved remotely, local-only, offline, conflict, conversion pending/uncertain, converted and deleted states easy to distinguish without relying on color. Explain the practical consequence: whether another device can see the saved version, what has not yet reached the bridge, and what is read-only. Preserve local text and the existing field-by-field conflict resolution, pending request identity and no-blind-retry behavior. Do not imply a local-only or uncertain draft is safely saved to the bridge. Navigating or switching views must not discard unacknowledged local edits.

In planner sessions, put connection/mode/workspace state and actionable questions or permission requests near the conversation and composer. Keep the normal session's separate/shared workspace choice, warning and user override. Preserve requested versus confirmed mode and unknown-turn behavior; do not resend an uncertain turn or claim a disconnected session is running. On phone, context and handoff details can open as focused, accessible sections while the conversation and its current state remain recoverable.

## Handoff review

Present editable plan/specification, any supported optional selected files, capture and frozen evidence clearly. No files are selected by default. Show the operations, rejected paths, base revision and document/file evidence that the current planner snapshot contract actually supplies. A change to reviewed evidence requires a fresh capture. Do not claim unsupported file selection is available; integrate it when the backend supports it.

The current Flutter `HandoffModel.approve()` posts `FactoryCreate`, and the sheet calls its action “Approve and start”; this is not a draft-only action. Verify the bridge's actual resulting state and start semantics and label the action and consequence accurately. Preserve a separate approval when using a backend draft-creation path; do not silently start work from capture or an unapproved draft. Bind the reviewed evidence to the submitted request and preserve existing hashes, versions and request identities. Surface stale/conflict and uncertain outcomes without a blind retry. Link only the item the bridge returns.

Use actual backend states and preserve all existing saved-draft, offline and conflict controls. Do not add a requirement to use a specific planner or create a working draft before handoff. Unsupported or unavailable capabilities should be explained, with existing manual workflows available.

## Validation

Exercise planner → captured snapshot → reviewed handoff, and separately working draft → review/conversion → factory item, at desktop and phone sizes. Test local-only and cross-device saved states, offline and retry, two-editor conflicts including a second conflict, selected and rejected files, stale snapshot, unknown turn and lost conversion response. Assert exact existing API payloads and no automatic approval or duplicate creation. Use isolated fixtures and label mocked presentation; report live planner, cross-device and physical-phone coverage separately.
