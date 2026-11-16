# Studio planning, drafts and handoff UX implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute this approved plan.

**Status:** approved by the user on 2026-11-17.

**Goal:** Make planning, cross-device drafts and factory handoff feel connected while preserving their separate persistence and approval guarantees.

**Architecture:** Improve presentation in the existing `Planner.svelte`, `WorkingDrafts.svelte`, conflict editor and handoff screens using their existing controller and API contracts. Reuse project-scoped route formatters, captured snapshot evidence and the foundation's shared primitives; do not invent a planner-to-working-draft synchronization path.

**Tech stack:** Svelte 5, TypeScript, Tailwind, Vitest, Playwright/axe, existing isolated bridge and deterministic fixtures.

**Spec:** [planning and handoff UX](../studio-planning-handoff-ux-spec.md); dependencies: [visual foundation](../studio-visual-foundation-spec.md), existing [working drafts](../factory-working-drafts-spec.md) and [Claude planner](2026-11-15-claude-acp-planner-plan.md).

**Base:** `origin/main` at `47fa29d7`; execute after the foundation plan and reconcile the current planner, drafts and project-navigation work. No task is parallel-safe with the other UI plans where files overlap.

**Non-goals:** new planner backend or MCP provider, automatic data transfer between artifacts, editing approved factory plans, automatic approval/start, new persistence formats, or new forge operations. One click must never silently turn an unsaved local draft into an approved run.

**Global constraints:** Preserve request IDs, snapshot/hash bindings, exact content, ETag/version checks, offline local storage, conflict data and current permissions. No selected files by default. Unknown turn/operation is not blindly resent. Maintain keyboard/focus, phone and desktop access and existing route guard behavior.

## Contracts

Add these pure display helpers to new `web/ui/src/lib/planning/experience.ts` (create the directory). `WorkingDraftView` is from `../working-drafts`; `PlannerView`, `PlannerSnapshotReply` and `PlannerSnapshotView` are from `../planner`.

```ts
import type { WorkingDraftView } from '../working-drafts'
import type { PlannerView, PlannerSnapshotReply, PlannerSnapshotView } from '../planner'
export interface PlanningStatus { label: string; detail: string; tone: 'neutral' | 'info' | 'success' | 'warn' | 'error' }
export function workingDraftStatus(view: WorkingDraftView | null): PlanningStatus
export function plannerConnectionStatus(view: PlannerView | null, connected: boolean, loadError: string): PlanningStatus
export function handoffStage(snapshot: PlannerSnapshotReply | null, reviewed: PlannerSnapshotView | null): 'editing' | 'captured' | 'reviewed'
```

`workingDraftStatus` preserves the precedence already in `WorkingDrafts.svelte` (`converting`, `converted`, `deleted`, conflict, offline, error, saving, local-only, pending, saved); wording must state whether the bridge has acknowledged the latest version. It never equates local persistence with remote persistence. `plannerConnectionStatus` never claims connected/confirmed access solely from the requested mode. `handoffStage` is display-only: a `reviewed` stage requires the existing reviewed snapshot object and does not authorize creating a factory item.

## Task 1: Project-scoped journey navigation

**Goal:** Users can find the planner, project working drafts and factory handoff without losing project scope or implying artifact synchronization.

**Files:** `web/ui/src/views/Planner.svelte`, `web/ui/src/views/WorkingDrafts.svelte`, `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/lib/routes.ts`, `web/ui/src/lib/routes.test.ts`, `web/ui/src/views/Planner.test.ts`, `web/ui/src/views/WorkingDrafts.test.ts`.

**Steps:**

1. Add tests for `planningLinksPreserveProject`, `planningUnknownProjectIsNotReassigned`, `planningNavigationDoesNotTransferContent`, and `planningReturnRetainsUnsavedDraft`. Include encoded project paths and an absent project.
2. Replace `Planner.svelte`'s hard-coded `factoryHash = '#factory'` with `formatFactoryRoute({projectRoot})` when the server's `view.projectRoot` or new-session route supplies it; retain `#factory` only when no project is known. Add links to `formatWorkingDraftsRoute({}, projectRoot)` and the existing handoff route with labels explaining they are separate artifacts. Use existing `plannerRoute`/`formatWorkingDraftsRoute` for links from drafts and handoff. Do not submit data or mint an item on navigation.
3. Respect the existing working-draft controller, local persistence and route guard. Returning to a planner session uses its existing session ID; if none is known, link to the planner start rather than claiming it is the same session. A missing project or unavailable target remains visible as such.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/routes.test.ts src/views/Planner.test.ts src/views/WorkingDrafts.test.ts src/views/FactoryNew.test.ts` passes.

## Task 2: Truthful draft save and conflict status

**Goal:** A user can tell whether the current draft is on the bridge and resolve conflicts without losing either side.

**Files:** new `web/ui/src/lib/planning/experience.ts`, new `web/ui/src/lib/planning/experience.test.ts`, `web/ui/src/views/WorkingDrafts.svelte`, `web/ui/src/lib/WorkingDraftConflict.svelte`, `web/ui/src/views/WorkingDrafts.test.ts`, `web/ui/src/lib/working-drafts.test.ts`.

**Steps:**

1. Implement and test `workingDraftStatus` from Contracts for every `WorkingDraftView` state and precedence, including saving while a newer edit remains pending, local-only, offline, conflict, converting/uncertain, converted and deleted. Do not modify `WorkingDraftController` or infer remote acknowledgement from a local write.
2. Replace the local `status` derivation in `WorkingDrafts.svelte` with the helper; show a concise status and explanatory detail near the editor and review action, with `role=status` for ordinary updates and an alert for actionable failures. Keep the current controller, timer, persistence, retry and `reviewProblem` checks.
3. Restructure `WorkingDraftConflict.svelte` into scannable field groups with local/server choice, editable combined result and clear save consequence. Keep each existing atomic field group, `combined` state, callbacks and second-conflict behavior unchanged; do not default to overwriting or discard either source. Ensure opening/closing details does not unmount the editor or reset changes.
4. Add tests for a second conflict while editing combined values, offline → retry with identical request, local-only draft, converted/deleted records, and status labels readable without color.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/planning/experience.test.ts src/views/WorkingDrafts.test.ts src/lib/working-drafts.test.ts` passes.

## Task 3: Planner conversation and pending interactions

**Goal:** A planner's active context, questions, connection and composer are understandable and reachable on small screens.

**Files:** `web/ui/src/lib/planning/experience.ts`, `web/ui/src/lib/planning/experience.test.ts`, `web/ui/src/views/Planner.svelte`, `web/ui/src/views/Planner.test.ts`.

**Steps:**

1. Implement `plannerConnectionStatus` from Contracts and tests for no view, connected/confirmed, requested-but-unconfirmed mode, disconnected, read error and `unknownTurn`. Distinguish mode confirmation from connection state.
2. In `Planner.svelte`, place the concise connection/project/workspace status and pending `cards` above or beside the conversation; keep the `send`, `answer`, `cancelTurn`, `freshSession`, request IDs and `lines` ordering. Give the prompt a reachable label and preserve its text and extra context on rejected or uncertain sends. Do not retry an unknown turn.
3. Use an accessible disclosure for expanded context and a focused phone-sized conversation layout. Preserve DOM state and selection when opening context or handoff, and retain both workspace occupancy choices and warnings. Do not hide any existing action or infer unsupported capabilities.
4. Add tests for separate/shared workspace, unconfirmed access, disconnected/unknown turn, multiple pending requests, keyboard focus and retained prompt after failure.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/planning/experience.test.ts src/views/Planner.test.ts` passes.

## Task 4: Capture and review handoff evidence

**Goal:** Users can tell whether they are editing, captured or reviewing an immutable snapshot before creating a draft.

**Files:** `web/ui/src/lib/planning/experience.ts`, `web/ui/src/lib/planning/experience.test.ts`, `web/ui/src/views/Planner.svelte`, `web/ui/src/views/Planner.test.ts`.

**Steps:**

1. Implement `handoffStage` from Contracts; test no snapshot, captured-only, reviewed, and stale/replaced snapshot. Ensure a reviewed value for a previous snapshot cannot label the current one reviewed; use the existing snapshot identity/hash comparison.
2. Organize the current Handoff section into visibly labeled editing, capture and review groups while preserving `capture`, `reviewSnapshot`, `createDraft`, `pendingDraft` and `plannerHandoffRequest` semantics. Show the user-selected file list, actual rejected paths, base commit and hash, then reviewed content/diffs. Keep a clear statement that creating a draft does not start work.
3. Changing source text or selected files invalidates or labels old captured/reviewed evidence using the existing snapshot validation path; it must never silently update an already reviewed snapshot. A fresh snapshot requires user action. Keep all selected-file checkboxes initially empty and retain the no-required-CI acknowledgement where the backend requires it.
4. Test no files, selected edits, rejected paths, stale snapshot, settings changed during review, lost create response with identical request ID and exact content, and no automatic factory approval.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/planning/experience.test.ts src/views/Planner.test.ts src/lib/planner.test.ts` passes.

## Task 5: Cross-device and phone workflow proof

**Goal:** Both existing handoff routes are understandable and preserve data across failure and navigation.

**Files:** new `web/ui/e2e/planning-handoff-ux.pw.ts`, `web/ui/e2e/fixtures.ts`, `docs/studio-fleet-factory/studio-planning-handoff-ux-spec.md`.

**Steps:**

1. Extend the existing isolated/route-intercepted fixtures with planner and draft responses; label mocked scenarios. Exercise planner prompt → selected files → snapshot → review → create draft, asserting exact API calls, snapshot hash and no approval/start. Exercise working draft edit → remote acknowledgement → review → convert separately; do not pretend they are one artifact.
2. Test two browser contexts editing the same bridge draft, offline/local-only status, a second conflict, stale settings and lost conversion response with the same request ID. Use the real isolated bridge where it supports the scenario and clearly label any modeled responses.
3. Verify dark/light, keyboard/focus, 390 × 844 and 390 × 600, tablet/desktop, long diffs and no horizontal page overflow. Capture and inspect representative screenshots. Append actual evidence and live-provider/physical-phone limitations to the spec.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/planning-handoff-ux.pw.ts e2e/factory-handoff.pw.ts` passes; `cd web/ui && npm run check && npm test -- --run && npm run build` passes. From root run `go build ./cmd/webbridge` and `go test ./web/bridge -run TestWebIsStdlibOnly`.

## Final verification

Run `scripts/e2e/run_browser_checks.sh`, `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent` and `git diff --check`. Report physical-device and live planner testing separately. Commit each verified task as `studio-planning-handoff-ux: task N — <title>`.
