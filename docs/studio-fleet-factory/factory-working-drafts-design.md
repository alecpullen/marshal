# Cross-device factory working drafts

Date: 2026-11-15
Status: design, [implementation specification](factory-working-drafts-spec.md)
and [plan](plans/2026-11-15-factory-working-drafts-plan.md) approved 2026-11-15.

## Intent

A user can begin preparing a factory handoff on one device and continue on
another without losing an unfinished title, plan, specification or handoff
choices. This is useful on desktop and on a phone where a browser may be
suspended or disconnected. A working draft is not an approved plan, a factory
run or a factory-owned execution workspace.

## Agreed behavior

- Autosave a working draft to the backend as the user edits. It is associated
  with a project and can be reopened on another device connected to the same
  Marshal installation. Incomplete or currently invalid fields remain editable.
- Show whether edits are saving, saved, pending offline, conflicted or failed.
  Never claim that an edit reached the backend before the backend confirms it.
  Browser suspension and sudden process termination can interrupt the last
  unacknowledged edit; communicate this honestly rather than promising lossless
  persistence.
- Keep pending edits on the device while offline and retry after reconnection.
  Compare the device's base version with the backend version. When they differ,
  preserve both and provide comparison and field-level choices of what to keep.
  Let the user edit the resolved result, including manually combining parts of
  the plan or specification. Retain both versions until the resolution is
  confirmed saved. Do not silently overwrite either side; automatic merging
  within text and real-time collaborative editing are not required.
- Keep working drafts separate from the existing factory draft and approval
  states. Creating a factory draft is an explicit action; it does not approve or
  start work. Reuse the factory's existing validation and preflight when the
  user creates a factory draft. A lost response must be safely retryable without
  duplicate items.

## Approaches

1. **Separate backend working-draft store (selected):** accepts incomplete
   handoffs and supports cross-device autosave without changing the meaning of
   factory draft/approval states. Requires a durable bridge store and an explicit
   conversion step.
2. **Autosave into factory items:** reuses the existing journal but would make
   its draft state accept incomplete input and require changes to validation,
   preflight and assumptions made by current factory clients.

## Proposed experience

A project can have multiple working drafts. Show a list with title (or an
untitled label), last-saved time and save/conflict state; opening one resumes
editing. Creating another does not overwrite it. Keep a route from the factory
and project to the list and new draft. Cross-device discovery lists only drafts
actually saved to that backend; device-only edits that never reached it are
shown only on their originating device.

Preserve the user's entered fields and their meaning, including project and
repository choices, native or workspace selection, explicit role/branch and
limit overrides, reviewer instructions, CI check mode and no-required-CI
acknowledgement. Inherited settings stay **inherit**, not a frozen copy of the
currently resolved values. When creating the factory draft, re-read current
project settings and show the effective values and validation errors; the
existing factory draft records the resolved snapshot. Don't silently replace a
saved but now unavailable repository, workspace or profile.

Use the normal factory handoff screen to explicitly create the factory draft.
The working draft remains available until the backend confirms creation and
returns the item identity. Thereafter show the link to the factory draft and
stop autosaving into it; the two records must not imply that later edits
silently update an approved or running item. Users can explicitly remove an
unneeded working draft; deletion and any retention/cleanup policy need safe
handling across devices. No automatic expiry or destructive cleanup is
introduced by this design.

## Proposed persistence and concurrency semantics

The backend stores each working draft with a stable ID, project identity,
monotonic version and last-saved time. Autosave uses a conditional update
against the version that the device last observed. The backend atomically
accepts the update or returns the current version and contents as a conflict;
responses can arrive out of order, so an older response must not mark a newer
local edit as saved. Scope every operation to the authenticated installation
and project. Do not put credentials into working drafts, URLs or logs.

A device retains a local copy of unsent changes, the server version they were
based on, and save/conversion request identities until the backend confirms the
outcome. A network error is not evidence that a write failed. On reconnect,
reconcile the pending operation before issuing a replacement. If two devices
edit concurrently or the server changed while offline, keep both versions and
show the differences. The user chooses values field by field and can edit the
combined result; saving it creates a new version based on the current server
version. If the server changes again while the conflict is open, keep the
in-progress combined result as well as the newly observed version and repeat
comparison instead of overwriting anything. Explicitly surface local-storage failure,
quota or private-browsing restrictions. Local storage is device-specific and
may be cleared by the browser; backend-confirmed state is the cross-device
source of truth.

The conversion action freezes the exact working-draft version and the resulting
factory-create request, including a stable request ID, before sending. Backend
create must map a working draft/version to at most one resulting factory item,
including simultaneous conversions from different devices and loss of an
acknowledgement. Persist enough association and request identity to reconcile
across browser and backend restart; do not rely on an in-memory retry ID. If an
outcome cannot be proven, show an uncertain state and offer reconciliation,
not a second create. Existing factory approvals, worktree ownership,
preflight, CI and recovery gates remain authoritative.

Protect against multiple tabs and different devices editing the same draft.
A delete or conversion racing an autosave is version-checked: never resurrect
a deleted or converted draft through an older save. Preserve any uncommitted
local changes and offer recovery rather than discarding them. How long an
explicitly deleted draft or tombstone is retained is an implementation choice
that must preserve those guarantees across restart.

## Scope and integration

This slice covers the current manual factory handoff UI and the backend it
needs. The existing MCP handoff and proposed Claude ACP planner may later use
the same draft service; do not require either integration to ship this slice or
invent their unmerged APIs. The factory's current `FactoryNew.svelte` keeps
input in component state and request identity only in memory; the factory
`Create` path already has request-ID replay and resolves defaults before
storing a valid factory draft. Preserve those semantics.

The UI must work at phone and desktop widths, with accessible labels, clear
save/error/conflict states and a usable diff comparison. The designer chooses
layout, autosave debounce, storage schema and retention mechanics. These choices
must meet the behavior above and not depend on a particular device being
online. `web/` remains standard-library-only; avoid in-flight P5 attachment and
relay changes and recheck factory-settings and MCP branches before planning.

## Validation before delivery

Use isolated backend stores, fake clocks and disposable browser storage. Test
incomplete drafts, default-following settings, multiple drafts/projects,
creation and reopening on two browser contexts, offline edits and reconnect,
concurrent conflicts and repeated conflicts, stale autosave responses, storage
quota failure, browser reload, backend restart, deletion races, concurrent
conversion, lost conversion acknowledgement, request replay and a preflight
failure with no new factory item. Confirm the saved factory draft contains the
exact approved handoff inputs and that no factory run is started. Verify desktop
and phone views and keyboard operation. If a browser or backend capability is
unavailable, expose the limitation and record it rather than claiming a pass.
