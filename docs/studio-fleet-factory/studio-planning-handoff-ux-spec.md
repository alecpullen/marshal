# Studio planning, drafts and handoff experience

Date: 2026-11-17
Status: approved specification (2026-11-17); see the [implementation plan](plans/2026-11-17-studio-planning-handoff-ux-plan.md).

## Intent

Make the existing planner, working drafts and factory handoff feel like connected steps while preserving their distinct persistence and approval contracts. A user should understand what is saved, what will travel to another device, exactly what is being handed off, and what action starts factory work. Use the approved visual foundation and existing project context on desktop and phone.

## Journey and state

Provide clear, project-scoped navigation among the planner, working drafts and factory handoff, with truthful labels describing where each action leads. The planner's live session and its captured snapshot, a working draft's autosaved content, and a factory item's approved revision are different artifacts: do not silently synchronize or copy one into another, promise cross-device persistence for unsaved planner text, or invent a new conversion endpoint. Where a transfer already exists, show its exact source and target. A user can return to the source after a handoff, and a link to a created item must refer to the item the server actually returned.

In working drafts, make saving, saved remotely, local-only, offline, conflict, conversion pending/uncertain, converted and deleted states easy to distinguish without relying on color. Explain the practical consequence: whether another device can see the saved version, what has not yet reached the bridge, and what is read-only. Preserve local text and the existing field-by-field conflict resolution, pending request identity and no-blind-retry behavior. Do not imply a local-only or uncertain draft is safely saved to the bridge. Navigating or switching views must not discard unacknowledged local edits.

In planner sessions, put connection/mode/workspace state and actionable questions or permission requests near the conversation and composer. Keep the normal session's separate/shared workspace choice, warning and user override. Preserve requested versus confirmed mode and unknown-turn behavior; do not resend an uncertain turn or claim a disconnected session is running. On phone, context and handoff details can open as focused, accessible sections while the conversation and its current state remain recoverable.

## Handoff review

Present a clear progression from editable plan/specification and optional selected files to capture, review of the frozen snapshot, and create factory draft. No files are selected by default. Show selected file operations and rejected paths, base revision and exact document/file evidence before confirming; the user may inspect details and change the selection by capturing a fresh snapshot. The reviewed snapshot and created draft remain bound to the existing immutable hashes and request IDs. If content or settings change during review, use existing stale/conflict behavior and ask the user to review the refreshed evidence. Capturing or creating a draft never starts factory work: the existing approval and start remain separate actions.

Use actual backend states and preserve all existing saved-draft, offline and conflict controls. Do not add a requirement to use a specific planner or create a working draft before handoff. Unsupported or unavailable capabilities should be explained, with existing manual workflows available.

## Validation

Exercise planner → captured snapshot → reviewed draft, and separately working draft → review/conversion → factory item, at desktop and phone sizes. Test local-only and cross-device saved states, offline and retry, two-editor conflicts including a second conflict, selected and rejected files, stale snapshot, unknown turn and lost conversion response. Assert exact existing API payloads and no automatic approval or duplicate creation. Use isolated fixtures and label mocked presentation; report live planner, cross-device and physical-phone coverage separately.
