# Studio work-item and worker experience implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute this approved plan.

**Status:** approved by the user on 2026-11-17.

**Goal:** Make the next decision, worker activity and review evidence clear and easy to reach throughout a work item's lifecycle.

**Architecture:** Reorganize the existing work-item presentation while preserving its mounted decision/revision components, route and command contracts. Use the existing factory store, fleet agent rows and shared session conversation; do not invent new backend state or implement a second subagent UI.

**Tech stack:** Svelte 5, TypeScript, Tailwind, Vitest, Playwright/axe and existing isolated bridge fixtures.

**Spec:** [work-item and worker experience](../studio-work-item-experience-spec.md); [visual foundation](../studio-visual-foundation-spec.md).

**Base and dependencies:** Authored against `origin/main` `47fa29d7`. Execute after the approved visual foundation plan, especially its mobile shell and shared session header fixes. Reconcile the in-flight [subagent plan](2026-11-16-studio-worker-subagents-plan.md), plan revisions and completion implementation at execution; do not duplicate or remove their controls. Recheck anchors against current `main`.

**Non-goals:** backend or forge behavior, automatic completion, new statuses or progress percentages, replacing existing session drill-down, changing approval or review gates, or implementing subagents.

**Constraints:** Keep hash routes and expected item/decision versions; never lose an unsaved answer, revision, focused control or scroll position on polling. Distinguish requested, confirmed, stale and unknown states. Phone and desktop get the same actions and disclosures. Every task includes tests and can be committed independently; commit `studio-work-item-ux: task N — <title>`.

## Contracts

Add the following pure view helpers to the existing `web/ui/src/lib/studio/model.ts`; do not mutate input. `AgentRow` is the existing row type in `web/ui/src/lib/fleet.ts`.

```ts
import type { FactoryEvent, FactoryWorker } from '../factory'
import type { AgentRow } from '../fleet'

export interface WorkerPresentation {
  agentId: string
  role: string
  name: string
  status: string
  parked: boolean
  available: boolean
}
export function presentFactoryWorker(worker: FactoryWorker, agents: AgentRow[]): WorkerPresentation
export function formatFactoryEventTime(at: string, locale?: string): { display: string; exact: string }
```

`presentFactoryWorker` looks up the exact `agentId`; name falls back to role + ID, status comes only from that live row, `parked` always wins display state, and a missing row means `available=false` and `status='Unknown'`. It never conflates a missing agent with a parked one or fabricates a status. `formatFactoryEventTime` uses local timezone and `Intl.DateTimeFormat` for a valid instant, retains the input as `exact`, and returns `Unknown time` with the original input preserved for invalid/missing values. The caller keeps `datetime` only for valid inputs. Locale injection is for deterministic tests; timezone uses the runtime.

## Task 1: Decision-first activity and revision disclosure

**Goal:** Decisions are immediately discoverable while plan editing remains available and keeps its draft.

**Files:** `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/lib/FactoryRevision.svelte`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/e2e/factory-item.pw.ts`.

**Steps:**

1. Add tests for `questionBeforePlanChanges`, `recoveryBeforePlanChanges`, `revisionDisclosurePreservesDraftAndFocus`, and `revisionDisclosureRefreshPreservesChoice`. Start from fixture `pendingQuestion`/`namedWorkerRecovery`, assert the actionable panel appears before revision controls on phone and desktop, and that typing in either panel survives a same-decision poll.
2. In `FactoryItem.svelte`'s `activityHidden` pane, move the existing keyed decision/recovery block before `FactoryRevision`. Keep its `decision.id` key and existing `panelRun` unchanged. Wrap the still-mounted revision component in a native `<details>`/`<summary>` labeled `Plan changes`; display proposal status/count in the summary. Initialize closed on a new item mount; do not reassign its open state on polls or worker/view changes. Move the activity subtree outside the `selectedView` conditional and toggle `hidden`/`aria-hidden` when another view is selected, so changing tabs does not destroy either form. Keep the worker stream mounted only while its existing route warrants it; ensure hidden activity controls are not focusable.
3. Preserve the existing non-activity decision banner and `activityRoute`. Verify navigating away from activity and back, and opening/returning from a worker, retains a pending answer and revision until the item/decision identity actually changes. Avoid automatic focus jumps on live updates.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/views/FactoryItem.test.ts`; from root `scripts/e2e/run_browser_checks.sh e2e/factory-item.pw.ts --grep 'questionBeforePlanChanges|recoveryBeforePlanChanges|revisionDisclosure'`. Tests pass at desktop/tablet/phone and assert no mutation before explicit submission.

## Task 2: Readable worker list and state

**Goal:** People can identify each work-item worker and open its existing conversation without relying on raw IDs.

**Files:** `web/ui/src/lib/studio/model.ts`, `web/ui/src/lib/studio/model.test.ts`, new `web/ui/src/lib/factory/WorkerList.svelte`, new `web/ui/src/lib/factory/WorkerList.test.ts`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**

1. Implement and test `presentFactoryWorker` from Contracts using `FactoryWorker` and `AgentRow`. Preserve unknown roles as readable text, never infer live state from the work-item state, and include unique IDs in accessible details when labels collide.
2. Extract the `worker-links` card from `FactoryItem.svelte` into `WorkerList.svelte`; accept the existing item, agents and navigation callback. Render role, best available name and actual state/parked/unknown; provide a labeled way to inspect the exact agent ID. Use the same `formatFactoryRoute({ itemId, view: 'activity', agent: worker.agentId })` links as today. Preserve stale or missing entries rather than dropping them.
3. Add component tests for multiple workers, identical names, missing live row, parked, stopped and unknown states and special-character agent IDs. In `FactoryItem.test.ts`, verify selected worker is unchanged on a poll and back returns to activity without remounting the activity draft. Preserve future subagent entry points without claiming children exist.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/studio/model.test.ts src/lib/factory/WorkerList.test.ts src/views/FactoryItem.test.ts` passes.

## Task 3: Human-readable activity without lost history

**Goal:** Activity timestamps and scrolling are understandable while retaining exact event order and provenance.

**Files:** `web/ui/src/lib/studio/model.ts`, `web/ui/src/lib/studio/model.test.ts`, `web/ui/src/lib/factory/ActivityTimeline.svelte`, `web/ui/src/lib/factory/ActivityTimeline.test.ts`, `web/ui/e2e/factory-item.pw.ts`.

**Steps:**

1. Implement `formatFactoryEventTime` from Contracts and tests for valid UTC input, invalid input, empty input and deterministic locale output. Keep `sortedFactoryHistory` unchanged and retain exact original data for every event.
2. In `ActivityTimeline.svelte`, replace the raw visible `event.at` with the helper's readable local value and retain its exact value as an accessible title/text; do not set an invalid `<time datetime>`. Keep `kindLabel`, `provenanceLine`, `atLatest`, `seen`, `savedTop` and the existing new-activity cue.
3. At narrow widths remove the timeline's fixed nested scroll container in favor of the parent page scroll, while preserving a reader's place and showing new activity without jumping; keep the bounded timeline on desktop. Add tests for new events during a reader's scroll, entering a worker and returning, long history, unknown time and provenance. If reliable position preservation requires measuring the outer scroller, inject that scroller explicitly rather than reading global window state.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/studio/model.test.ts src/lib/factory/ActivityTimeline.test.ts`; from root `scripts/e2e/run_browser_checks.sh e2e/factory-item.pw.ts --grep 'activity|Activity|DrillIn'`. Expected: chronological records remain complete and no reader-position regression.

## Task 4: Execution and completion context

**Goal:** The header explains execution and user completion independently while preserving existing PR and review actions.

**Files:** `web/ui/src/lib/factory/WorkItemHeader.svelte`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/e2e/factory-item.pw.ts`.

**Steps:**

1. Add tests for execution `running` + completion `complete`, legacy missing completion, externally merged PR with completion open, reopening, stopped/unknown state and long labels. Assert no view changes merely because a poll changes state.
2. In `WorkItemHeader.svelte`, display execution status and, when `item.completion` is present, a separate labeled work-item completion status; absent/empty completion uses the backend's established open meaning, with no inferred completed state from a PR or execution state. Keep existing `stoppable`, stop callback, tabs and breadcrumb route contract.
3. Ensure the existing `CompletionPanel`, `ReviewView`, `PullRequestsView` and `DocumentsView` remain mounted/routed exactly as before and all explicit merge, completion, reopen and recovery actions retain payloads. Do not add completion from the header.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/views/FactoryItem.test.ts`; from root `scripts/e2e/run_browser_checks.sh e2e/factory-item.pw.ts`. Completion and PR existing regression cases remain green.

## Task 5: Cross-device workflow proof

**Goal:** A user can find and resolve work, inspect a worker and return without losing context.

**Files:** `web/ui/e2e/factory-item.pw.ts`, `web/ui/e2e/fixtures.ts`, `docs/studio-fleet-factory/studio-work-item-experience-spec.md`.

**Steps:**

1. Add `workItemDecisionToWorkerAndBack` using the existing mocked presentation fixture. At 1440 × 900, 768 × 1024, 390 × 844 and 390 × 600, exercise a pending decision, revision disclosure, named worker, conversation and return. Assert order, scroll/focus, unsaved text, selected route, no horizontal overflow, and exact request payload only after the user sends an answer.
2. Add `workItemReviewAndCompletionRemainSeparate` with an externally merged PR and an open business item. Verify review evidence and completion action remain independently accessible, including a partial/unknown merge outcome; do not simulate a successful backend transition in a presentation-only assertion.
3. Run axe and keyboard navigation in both themes, reduced motion and 200% zoom. Capture and inspect desktop/phone fixture screenshots, documenting fixture and live-service limitations in the spec delivery record.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/factory-item.pw.ts` passes all existing and new scenarios; `cd web/ui && npm run check && npm test -- --run && npm run build` succeeds. From root, `go build ./cmd/webbridge` embeds the new SPA; `go test ./web/bridge -run TestWebIsStdlibOnly` passes.

## Final verification

Run `scripts/e2e/run_browser_checks.sh`, `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent` and `git diff --check`. Report untested physical-device or live-agent coverage separately; do not modify generated static assets by hand.
