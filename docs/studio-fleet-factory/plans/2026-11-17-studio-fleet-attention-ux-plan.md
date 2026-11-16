# Studio Fleet attention and recovery UX implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute this approved plan.

**Status:** approved by the user on 2026-11-17.

**Goal:** Make cross-project attention, readiness and recovery consistent and trustworthy in Fleet and Inbox.

**Architecture:** Reuse the shell's existing fleet and factory stores and derive a common presentation projection. Add independently tracked freshness for existing fleet snapshot sources, show factory entries in Inbox, and keep all mutations on their existing agent, network and factory routes.

**Tech stack:** Svelte 5, TypeScript, Tailwind, Vitest, Playwright/axe and the isolated `webbridge` fixture.

**Spec:** [Fleet attention and recovery UX](../studio-fleet-attention-ux-spec.md); dependencies: [visual foundation](../studio-visual-foundation-spec.md), [work-item UX](../studio-work-item-experience-spec.md) and existing [factory completion](../factory-completion-spec.md).

**Base:** `origin/main` at `47fa29d7`. Execute after foundation and work-item plans; coordinate with project navigation, the daemon reconnect conversion and current factory completion behavior. Recheck actual store and route contracts on the execution base. These plans share UI files and run sequentially.

**Non-goals:** a new dashboard, extra polling service, browser notifications, cross-project permission changes, factory auto-approval/completion, merging distinct factory and agent decisions, or new backend endpoints.

**Global constraints:** Use reported source states; preserve exact request IDs and existing action callbacks. A failed read keeps its last verified data marked stale or says unavailable if none; it never erases a healthy independent source. `Mine` applies existing ownership filtering only where a source reports ownership; factory entries remain explicitly project-wide until ownership exists. No developer credentials, actual factory effects or live services in fixtures.

## Contracts

Add the following to new `web/ui/src/lib/studio/attention.ts`. Existing source types remain in `../factory`, `../fleet` and `../api`.

```ts
import type { FactoryItem } from '../factory'
export type SourceFreshness = 'loading' | 'fresh' | 'stale' | 'unavailable'
export interface SourceState { freshness: SourceFreshness; lastUpdated: number | null; error: string | null }
export interface FactoryAttentionEntry {
  id: string
  itemId: string
  projectRoot: string
  title: string
  section: 'needs-you' | 'ready'
  reason: string
  href: string
  scope: 'project-wide'
}
export function factoryAttentionEntries(items: FactoryItem[]): FactoryAttentionEntry[]
export function sourceState(loading: boolean, lastUpdated: number | null, error: string | null): SourceState
```

`factoryAttentionEntries` uses actual item state and decision: a draft or decision is `needs-you`; a ready item without an active decision is `ready`; all others are absent. If both conditions are true, only the needs-you entry is emitted for that factory item, but a worker's separate agent request remains a distinct entry. Use stable ID `factory:<itemId>`, exact project root, and `factoryItemRoute` for the link. Say `Ready for review` without asserting unobserved CI. Preserve the order returned by existing `filterFactoryItems` for factory-only groups. `sourceState` is `loading` while a first read is in progress, `unavailable` whenever no snapshot exists and no first read is in progress, `stale` on error with a last snapshot, and `fresh` on a successful snapshot. A refreshing known snapshot remains displayed, not blanked. Do not invent a timestamp.

## Task 1: Track fleet source freshness independently

**Goal:** A failed agents, projects, inbox or network read is visible without hiding successful data from the others.

**Files:** `web/ui/src/lib/fleet.ts`, `web/ui/src/lib/fleet.test.ts`, `web/ui/src/lib/fleetsse.test.ts`, new `web/ui/src/lib/studio/attention.ts`, new `web/ui/src/lib/studio/attention.test.ts`.

**Steps:**

1. Implement/test `sourceState` in Contracts, including first load, first failure, failed refresh after success and recovery; no fabricated `lastUpdated`.
2. Replace `createFleetStore.refresh`'s all-or-nothing `Promise.all` with separately settled existing `listAgents`, `listProjects`, `getInbox` and `getNetworkPending` reads. Add source-specific `SourceState` for each to the store, with injectable clock/requests for tests or the existing test seams. Apply successful snapshots independently, keep a failed source's prior snapshot and set its error, and distinguish an unsupported network endpoint from a verified empty list. Retain the existing single SSE connection and refetch behavior.
3. Guard each source against an older in-flight refresh overwriting a newer SSE delta, project removal, user decision or later successful refresh: capture source revision at request start; if it changes, discard the obsolete response and schedule the normal refresh path. Only remove decisions for missing agents after an authoritative fresh agent snapshot. Keep `mergeAgents` run data and existing `decideNetwork`/`dismissNotice` behavior. Retain `error` only as a backwards-compatible aggregate for callers that have not yet migrated; it must not hide healthy source data.
4. Add `TestFleetSourcePartialFailure`, `TestFleetSourceOldResponseAfterDelta`, `TestFleetSourceNetworkUnknown`, `TestFleetSourceRecovery` and `TestFleetSourceNoDuplicateStreams` to the existing Vitest suites. Test first load and refresh, overlapping requests, removal, partial results and no accidental action retry.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/fleet.test.ts src/lib/fleetsse.test.ts src/lib/studio/attention.test.ts` passes.

## Task 2: Shared factory attention projection

**Goal:** Fleet and Inbox derive the same factory decision/ready entries without losing distinct agent requests.

**Files:** `web/ui/src/lib/studio/attention.ts`, `web/ui/src/lib/studio/attention.test.ts`, `web/ui/src/views/Fleet.svelte`, `web/ui/src/views/Fleet.test.ts`.

**Steps:**

1. Implement `factoryAttentionEntries` from Contracts using existing `filterFactoryItems` and `factoryItemRoute`; test draft, decision, ready, ready+decision, running, closed, missing title and special-character IDs. Never add entries or claims derived solely from PR merge/completion.
2. Replace `Fleet.svelte`'s separate `needsFactory`/`readyFactory` rendering inputs with the shared projection. Keep agent/network/intake and automation entries separate, preserve their existing callbacks and ordering, and count exactly the rows actually rendered. For ready factory entries replace the unconditional `Checks have passed` copy with `Ready for review`; detailed evidence remains on the item.
3. Display factory source freshness from the existing `FactoryState` (`loading`, `error`, `lastUpdated`) using `sourceState`; retain already loaded data as stale. Keep Retry calling only `onRefreshFactory`, not a factory command.
4. Test the same item having a factory question and the worker having its own approval, ready+decision, failed factory feed beside healthy agents, stale snapshot, and no duplicated factory item.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/studio/attention.test.ts src/views/Fleet.test.ts src/lib/factoryStore.test.ts` passes.

## Task 3: Add factory work to Inbox

**Goal:** Inbox is a detailed queue that includes current factory decisions and ready work with honest scope labels.

**Files:** `web/ui/src/App.svelte`, `web/ui/src/views/Home.svelte`, `web/ui/src/views/Home.test.ts`, `web/ui/src/lib/inbox.ts`, `web/ui/src/lib/inbox.test.ts`.

**Steps:**

1. Pass the existing shell `$factory` state and `refreshFactory` callback to `Home.svelte`; do not create another factory store or poller. Compute factory entries with Task 2's helper. Render needs-you and ready entries alongside existing source sections with `Factory · project-wide`, project name, reason and direct item link. Never issue a factory command from this summary.
2. Preserve `mineOnly` for agent/intake data. Because `FactoryItem` has no owner field, show factory entries in both scope views explicitly labeled `Project-wide`; do not imply they are individually owned or silently exclude them. Compute headings/counts from exactly the rendered factory plus scoped existing rows. Keep each agent pending request and factory decision separate even when they share an agent.
3. Show factory stale/unavailable status with last-update time and Retry without hiding healthy Inbox content. Preserve `PendingActions`, `NetworkDecision`, intake approval/deny, automation links, `onOpenAgent` and current callbacks. Verify project/item IDs are encoded once and Browser Back preserves scope.
4. Add `inboxIncludesFactoryAttention`, `inboxShowsProjectWideInMine`, `inboxKeepsDistinctWorkerAndFactoryRequests`, `inboxFactoryFailureKeepsAgents`, and `inboxCountMatchesVisibleEntries` to `Home.test.ts`, plus scope tests in `inbox.test.ts`.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/views/Home.test.ts src/lib/inbox.test.ts src/views/Fleet.test.ts` passes.

## Task 4: Per-source recovery and accessible queue presentation

**Goal:** Users can see which information is stale and reach the existing appropriate recovery surface.

**Files:** `web/ui/src/App.svelte`, `web/ui/src/views/Fleet.svelte`, `web/ui/src/views/Home.svelte`, `web/ui/src/lib/studio/AttentionCard.svelte`, `web/ui/src/lib/inbox/NetworkDecision.svelte`, `web/ui/src/views/Fleet.test.ts`, `web/ui/src/views/Home.test.ts`.

**Steps:**

1. Replace the shell's single aggregate fleet error banner with compact per-source status from Task 1 in both Fleet and Inbox. For each failed source, show unavailable or stale, its last verified time when present, and the existing refresh action. Do not show `Nothing needs you` when an essential attention source has never loaded or is unavailable; label partial results while keeping healthy entries visible.
2. Apply foundation layout to `AttentionCard` and Inbox rows: source, project, name, reason/status and an accessible direct action; wrap long content at 390 px instead of hiding essential reasons in `truncate`. Keep explicit existing inline agent, intake and network actions and pending/busy behavior. Distinguish unknown/unavailable from running and confirm that status text remains readable without color.
3. Keep ordering stable: preserve each source's authoritative ordering and use existing factory sorting; present sources in their current predictable order rather than inventing cross-source timestamps. Preserve notices, `undo`, optimistic protections and the existing refresh button behavior.
4. Test partial failures, reconnect, stale after success, mixed projects, long reasons, no-authoritative-data empty states, focus/keyboard actions, 200% zoom and reduced motion. Verify no clicking a card implicitly approves, retries or merges work.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/views/Fleet.test.ts src/views/Home.test.ts src/lib/inbox.test.ts src/lib/fleet.test.ts` passes.

## Task 5: Cross-project workflow proof

**Goal:** Fleet and Inbox show consistent actionable work through disconnection and recovery without duplicate requests.

**Files:** `web/ui/e2e/studio-fleet.pw.ts`, `web/ui/e2e/studio-shell.pw.ts`, `web/ui/e2e/fixtures.ts`, `docs/studio-fleet-factory/studio-fleet-attention-ux-spec.md`.

**Steps:**

1. Add fixture scenarios with two projects, a factory decision and a distinct worker approval, a ready item, running agents, a network decision and automation results. Navigate Fleet → Inbox → factory item → worker and Back. Assert counts, source labels, project scope, item/agent identity, and that actions only use the existing explicit callbacks.
2. Simulate factory, inbox and agents reads failing separately before and after a successful snapshot, then reconnect. Assert healthy sources remain visible, stale data is labeled, unknown is never shown as empty success, and one fleet stream/factory poller remains active. Simulate an older response after a newer delta.
3. Verify 1440 × 900, 768 × 1024, 390 × 844 and 390 × 600, dark/light axe, 200% zoom, keyboard/focus and reduced motion. Capture and inspect representative screenshots; append actual real-bridge versus mocked-presentation evidence and physical-device limitations to the spec.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/studio-fleet.pw.ts e2e/studio-shell.pw.ts` passes; `cd web/ui && npm run check && npm test -- --run && npm run build` passes. From root run `go build ./cmd/webbridge` and `go test ./web/bridge -run TestWebIsStdlibOnly`.

## Final verification

Run `scripts/e2e/run_browser_checks.sh`, `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent` and `git diff --check`. Report live service and physical-device coverage separately. Commit each verified task as `studio-fleet-attention-ux: task N — <title>`.
