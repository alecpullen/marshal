# Marshal Studio UI/UX alignment implementation plan

**Goal:** Bring Studio's fleet manager and software factory into alignment with the approved HIFI information architecture and interaction layouts while retaining Warm Sunset, preserving real contracts, and delivering Fleet → handoff → execution/review as independently shippable vertical slices.

**Spec:** [Marshal Studio UI/UX alignment design](../studio-uiux-alignment-design.md), approved 2026-11-15.

**Base:** `main` at `f42a7c59bef23dcb457057d7987432cc02d22b36`. The approved spec and this plan are uncommitted at authoring time and must accompany the implementation. Recheck anchors and the merge-base at execution; do not reset or overwrite unrelated work.

**Status:** approved 2026-11-15; execution deferred for a later or manual run. No plan tasks have started.

**Non-goals:** Adopting the HIFI purple/light palette; implementing planning/voice/workflow/host/merge backends; fake operational data or mutations; TUI changes; new bridge endpoints for these three slices; a framework/component-library migration; durable cross-device drafts; a long-lived parallel Studio or UI feature flag.

**Assumptions:** The UI remains Svelte 5 + TypeScript + Tailwind. `GET /api/factory/items` returns complete `FactoryItem` records. Existing fleet SSE, session, project, factory, and preview APIs remain the only operational contracts. The credential-free real browser bridge does not save or approve factory drafts because `factoryAPI.preflight` requires a GitHub repository and credential; those visual states use explicitly mocked API fixtures. Tasks are sequential; none is marked parallel-safe.

**Execution convention:** Write the named failing tests before each implementation. Run that task's Verify commands, commit one reviewable unit as `studio-uiux-alignment: task N — <title>`, then start the next task. Do not hand-edit generated files under `web/bridge/static/`; regenerate them with `npm run build` at the slice gates.

## Contracts

### C1. Hash routes — Task 1, consumed throughout

`FactoryRoute` moves from a list/create/item-only shape to the following declaration in `web/ui/src/lib/factory.ts`; route parsing remains in `lib/routes.ts`:

```ts
export type FactoryFilter = 'current' | 'needs-you' | 'ready' | 'history'
export type FactoryItemView = 'approval' | 'activity' | 'review' | 'prs' | 'documents'
export interface FactoryRoute {
  itemId?: string
  create?: boolean
  projectRoot?: string
  filter?: FactoryFilter
  view?: FactoryItemView
  agent?: string
}
```

Add these declarations to `lib/routes.ts`; import the factory-owned route types without a runtime cycle:

```ts
import type { FactoryFilter, FactoryItemView, FactoryRoute } from './factory'
export const ROADMAP_AREAS = ['planning', 'voice', 'workflow', 'hosts', 'environments', 'connections'] as const
export type RoadmapArea = (typeof ROADMAP_AREAS)[number]
export interface NewAgentRoute { projectRoot?: string }
export interface RoadmapRoute { area: RoadmapArea }
export type Page = 'fleet' | 'inbox' | 'new' | 'chat' | 'sessions' | 'runs' | 'run' | 'live' | 'library' | 'settings' | 'usage' | 'watches' | 'workspaces' | 'network' | 'project' | 'factory' | 'roadmap' | 'other'
export function parseNewAgentRoute(hash: string): NewAgentRoute | null
export function parseRoadmapRoute(hash: string): RoadmapRoute | null
export function parseFactoryRoute(hash: string): FactoryRoute | null
export function formatFactoryRoute(route: FactoryRoute): string
```

`ChatRoute` gains `from?: string`; `formatChatRoute` round-trips it. Chat IDs are encoded by `formatChatRoute` and decoded once by `parseChatRoute` using `decodeSafe`, so factory worker IDs containing slashes remain one route segment. `redirectLegacy('#fleet')` returns `'#'`. `pageFromHash('')` and `pageFromHash('#')` return `'fleet'`; `#inbox` returns `'inbox'`; valid roadmap routes return `'roadmap'`.

Canonical formatting rules:

- Absent, unknown, or `current` factory filters produce no `filter` query parameter; other valid filters do.
- Scoped factory routes are `#projects/<encoded-root>/factory`; global routes are `#factory`.
- Creation uses `#factory/new?project=<encoded-root>` when a project is supplied.
- Item routes encode the ID and emit valid `view`/`agent` parameters. An agent without an explicit view is formatted with `view=activity`.
- `parseFactoryRoute` sets `view: 'activity'` when `agent` is present without a view. Unknown views/filters are dropped.
- `ChatRoute.from` is retained only when its decoded value parses as a factory route with a nonempty `itemId`; it may coexist with node/dock/tab.

### C2. Pure Fleet/factory projections — Task 2

Create `web/ui/src/lib/studio/model.ts` with these exports:

```ts
import type { FactoryCheck, FactoryEvent, FactoryFilter, FactoryItem, FactoryItemView, FactoryRound } from '../factory'

export interface FactoryProjectSummary {
  root: string
  items: FactoryItem[]
  current: number
  needsYou: number
  ready: number
  linkedAgentIds: string[]
}
export interface FactoryCheckGroups { failed: FactoryCheck[]; passed: FactoryCheck[]; other: FactoryCheck[] }
export function isFactoryActive(item: FactoryItem): boolean
export function filterFactoryItems(items: FactoryItem[], filter?: FactoryFilter): FactoryItem[]
export function factoryWorkerIds(items: FactoryItem[], currentOnly?: boolean): Set<string>
export function isIndependentAgent(agentId: string, items: FactoryItem[]): boolean
export function summarizeFactoryProject(root: string, items: FactoryItem[]): FactoryProjectSummary
export function defaultFactoryItemView(item: FactoryItem, requested?: FactoryItemView): FactoryItemView
export function factoryItemRoute(item: FactoryItem): string
export function partitionFactoryChecks(round?: FactoryRound): FactoryCheckGroups
export function sortedFactoryHistory(item: FactoryItem): FactoryEvent[]
```

Behavior is fixed:

- Active polling states exclude `draft`, `ready`, `stopped`, `closed`, and `needs_input`.
- Current excludes `closed`/`stopped`; needs-you means a decision or draft; ready means `ready`; history means `closed`/`stopped`. Unknown states remain in current.
- Filtered items are sorted needs-you first, ready second, then `updatedAt` descending, then ID ascending. Invalid/missing timestamps sort as zero.
- Worker IDs are nonempty unique IDs. `currentOnly` uses the current filter; independent-agent exclusion uses all items by default.
- Project summaries include all of the project's items in sorted order, counts from current items, and linked IDs from current items only.
- Default item view is requested view when valid, except approval on a non-draft becomes activity. Without a request: draft → approval, ready → review, otherwise activity.
- Direct item routes prioritize draft → approval, decision → activity, ready → review, otherwise activity.
- Check partitioning preserves server order: failed has a conclusion outside `success`, `neutral`, and `skipped`; passed is `success`; all others are other.
- History sorts by ascending `sequence`, with original array order breaking ties.

### C3. Shell-level factory store — Task 3

Export the existing `PollSeam` from `lib/factory.ts`. Create `lib/factoryStore.ts`:

```ts
import type { Readable } from 'svelte/store'
import type { FactoryItem } from './factory'
import type { PollSeam } from './factory'

export interface FactoryState {
  items: FactoryItem[]
  loading: boolean
  refreshing: boolean
  error: string | null
  lastUpdated: number | null
}
export interface FactoryStore {
  state: Readable<FactoryState>
  start(): void
  stop(): void
  refresh(): Promise<void>
  getItem(id: string): Promise<FactoryItem>
  upsert(item: FactoryItem): void
  itemById(id: string): FactoryItem | undefined
}
export interface FactoryStoreOptions {
  list?: () => Promise<FactoryItem[]>
  get?: (id: string) => Promise<FactoryItem>
  now?: () => number
  pollSeam?: PollSeam
}
export function createFactoryStore(options?: FactoryStoreOptions): FactoryStore
```

The store defaults to `listFactoryItems`, `getFactoryItem`, `Date.now`, and the existing `createFactoryPoller`. `refresh()` coalesces concurrent list reads, sets `loading` before the first authoritative snapshot and `refreshing` thereafter, and rethrows read errors after recording them so the poller retains bounded backoff. Callers that intentionally handle the visible state may catch the rejection.

A global mutation revision guards list reads; a per-ID revision guards single-item reads. `upsert` increments both for that item. A stale list response is discarded and the poller schedules its next reconciliation. A stale single-item response returns the newer cached item. A successful missing-item read adopts the item without counting as a command mutation. `lastUpdated` changes only for an applied list snapshot.

### C4. Roadmap, automation, and navigation models — Tasks 4–6

`lib/roadmap.ts` imports `RoadmapArea` from routes and exports:

```ts
export interface RoadmapAction { label: string; hash: string }
export interface RoadmapEntry {
  area: RoadmapArea
  title: string
  summary: string
  current: string
  planned: string[]
  alternatives: RoadmapAction[]
  designHref?: string
}
export const ROADMAP: Record<RoadmapArea, RoadmapEntry>
```

`lib/automations/summary.ts` exports:

```ts
import type { CIHistory, ProjectStatus, ReviewDraft } from '../api'
export interface AutomationSummary {
  drafts: { root: string; draft: ReviewDraft }[]
  fixes: CIHistory[]
  errors: { root: string; message: string }[]
}
export function loadAutomationSummaries(projects: ProjectStatus[]): Promise<AutomationSummary>
```

`lib/nav/model.ts` exports:

```ts
import type { ProjectStatus } from '../api'
import type { FactoryItem } from '../factory'
export interface NavDestination {
  hash: string
  label: string
  glyph: string
  active: boolean
  planned?: boolean
  count?: number
}
export interface NavProjectDestination extends NavDestination { root: string; workItems: NavDestination[] }
export interface NavModel {
  primary: NavDestination[]
  projects: NavProjectDestination[]
  resources: NavDestination[]
  roadmap: NavDestination[]
  all: NavDestination[]
}
export interface NavModelInput {
  route: string
  projects: ProjectStatus[]
  factoryItems: FactoryItem[]
  needsCount: number
}
export function buildNavModel(input: NavModelInput): NavModel
export interface StudioNavProps { model: NavModel; open: boolean; onToggle: () => void; onNavigate: (hash: string) => void }
export interface MobileNavProps { model: NavModel; onNavigate: (hash: string) => void }
```

`all` is the deduplicated global/project destination list for the all-pages menu; contextual work items remain in their project navigation and Fleet. Planned destinations always carry `planned: true` and link only to their roadmap route.

### C5. Shared presentation and command contracts — Tasks 4, 7, 12–18

`PageHeader.svelte` and `StateMessage.svelte` use these props:

```ts
import type { Snippet } from 'svelte'
export interface Breadcrumb { label: string; hash?: string }
export interface PageHeaderProps {
  breadcrumbs?: Breadcrumb[]
  eyebrow?: string
  title: string
  subtitle?: string
  status?: Snippet
  actions?: Snippet
  onNavigate: (hash: string) => void
}
export interface StateMessageProps {
  tone?: 'neutral' | 'info' | 'success' | 'warn' | 'error'
  title: string
  body?: string
  actionLabel?: string
  onAction?: () => void
}
```

Fleet/factory cards use:

```ts
import type { Snippet } from 'svelte'
import type { AgentRow } from '../fleet'
import type { ProjectStatus } from '../api'
import type { FactoryItem } from '../factory'
import type { FactoryProjectSummary } from './model'
export interface AttentionCardProps {
  source: string
  title: string
  body?: string
  meta?: string
  href: string
  actionLabel: string
  onNavigate: (hash: string) => void
  actions?: Snippet
}
export interface ProjectFactoryCardProps { project?: ProjectStatus; summary: FactoryProjectSummary; onNavigate: (hash: string) => void }
export interface WorkItemCardProps { item: FactoryItem; onNavigate: (hash: string) => void }
export interface IndependentAgentListProps { agents: AgentRow[]; onOpen: (id: string) => void }
```

Factory configuration components use:

```ts
import type { FactoryCheckKey, FactoryConfig } from '../factory'
export interface ChoiceOption { value: string; label: string }
export interface ConfigSummaryRow { label: string; value: string; source?: string; warning?: boolean }
export interface ProfileSelectProps {
  label: string
  value: string
  profiles: string[]
  followLabel?: string
  disabled?: boolean
}
export function profileOf(route: { profile?: unknown } | undefined): string
export function keepUnavailable(options: ChoiceOption[], value: string): ChoiceOption[]
export function formatFactoryCheck(check: FactoryCheckKey): string
export function resolvedConfigRows(config: FactoryConfig): ConfigSummaryRow[]
```

`ProfileSelect.value` is `$bindable()` in Svelte. `resolvedConfigRows` always includes repository, project, target branch, host, environment, implementer, reviewer, reviewer instructions, expected checks/no-CI acknowledgement, auto permission, total rounds, and CI poll/wait; it never stringifies a routing object.

Factory commands extracted from FactoryItem use:

```ts
import type { FactoryCommand, FactoryCreate } from '../factory'
export interface FactoryCommandInput {
  answer?: string
  reason?: string
  additionalRounds?: number
  replacement?: FactoryCreate
  implementerRouting?: Record<string, unknown>
  reviewerRouting?: Record<string, unknown>
  confirmPreviousWorkerStopped?: boolean
}
export type RunFactoryCommand = (kind: FactoryCommand['kind'], input?: FactoryCommandInput) => Promise<void>
```

### C6. Route-leave guard — Task 10

Create `lib/navigationGuard.ts`:

```ts
export type RouteLeaveGuard = () => boolean
export interface NavigationGuard {
  register(guard?: RouteLeaveGuard): void
  allow(): boolean
}
export function createNavigationGuard(): NavigationGuard
```

`register(undefined)` clears the active guard. `allow()` returns true with no guard and otherwise invokes the guard. App adds the hash-history handling; this small contract does not touch the browser itself.

### C7. Embeddable agent conversation — Tasks 19–20

`lib/session/AgentConversation.svelte` uses:

```ts
import type { AgentRow } from '../fleet'
import type { ChatRoute, DockTabParam } from '../routes'
export interface AgentConversationProps {
  sessionId: string
  onBack: () => void
  route?: ChatRoute | null
  agent?: AgentRow
  embedded?: boolean
  onOpenFull?: (tab?: DockTabParam) => void
}
```

The component owns the existing session/stack stores and closes them on destroy. Full Chat is a thin wrapper with `embedded: false`. Embedded mode requires `onOpenFull`, hides the dock/ExitPanel/full-session-only header actions, never writes a chat hash, and routes task/inspect/file affordances to `onOpenFull` rather than an invisible dock.

## Task 1: Extend Studio hash routes

**Goal:** Parse and format every approved Fleet, Inbox, factory, roadmap, project-prefill, and return-route contract without breaking existing deep links.

**Files:** `web/ui/src/lib/factory.ts`, `web/ui/src/lib/routes.ts`, `web/ui/src/lib/routes.test.ts`.

**Steps:**

1. Before editing, run `cd web/ui && npm run check` and record any existing errors in the Task 1 commit message as the comparison baseline. Then extend `routes.test.ts` first. Add `factory routes parse scoped filters and item drill-ins`, `factory routes canonicalize defaults and reject malformed paths`, `chat route round-trips a validated factory return`, `chat route drops an invalid return`, `chat route encodes and decodes agent IDs`, `new agent route reads the project query`, and `roadmap route accepts only named areas`. Update `pageFromHash` and `redirectLegacy` tests for Fleet as default, `#fleet → #`, and `#inbox`.
2. Add the C1 types. At the existing `parseFactoryRoute`/`formatFactoryRoute` anchors, implement global/scoped list, creation, and item query parsing in that order. Reuse `decodeSafe`; never throw on malformed escapes. Parse `#projects/<root>/factory` here so App has one factory-route owner.
3. Add `parseNewAgentRoute` and `parseRoadmapRoute`; update `Page`/`pageFromHash` and `redirectLegacy`. Preserve all existing route behavior covered by the current test file.
4. Add `from` to `ChatRoute`, validate it through `parseFactoryRoute(...).itemId`, and include it in `formatChatRoute`. Change the existing Chat ID read/write to `id: decodeSafe(m[1])` and `encodeURIComponent(r.id)` so the new round-trip test covers slash/space IDs. Keep existing node/dock/tab behavior unchanged.

**Verify:** `cd web/ui && npm test -- src/lib/routes.test.ts` — all existing and new route tests pass. `npm run check` — no new errors versus the recorded base. Commit `studio-uiux-alignment: task 1 — extend Studio hash routes`.

## Task 2: Build pure Fleet and factory projections

**Goal:** Provide deterministic filtering, grouping, counts, direct actions, check grouping, and activity ordering independently of Svelte rendering.

**Files:** `web/ui/src/lib/studio/model.ts` (new), `web/ui/src/lib/studio/model.test.ts` (new).

**Steps:**

1. Create tests named `filtersAndSortsFactoryItems`, `summarizesCurrentProjectWork`, `keepsProjectsWithNoFactoryItemsEmpty`, `excludesHistoricalFactoryWorkersFromIndependentAgents`, `resolvesInitialAndRequestedItemViews`, `buildsDirectItemRoutes`, `partitionsChecksWithoutLosingPendingChecks`, and `sortsHistoryStablyBySequence`. Use complete factory fixtures and cover unknown states, empty worker IDs, invalid dates, duplicate workers, and tied sequences.
2. Implement every C2 function exactly as specified. `filterFactoryItems` returns a new sorted array and never mutates the store's array. `summarizeFactoryProject` matches `projectRoot` exactly. `factoryItemRoute` uses `formatFactoryRoute` rather than hand-built strings.
3. Keep this module free of API calls, Svelte stores, DOM access, and component imports so it remains the shared cold-path projection for Fleet, navigation, and factory views.

**Verify:** `cd web/ui && npm test -- src/lib/studio/model.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 2 — build Fleet and factory projections`.

## Task 3: Add the shared factory store

**Goal:** Replace future per-view polling with one testable store that preserves fresh command responses and honest loading/error state.

**Files:** `web/ui/src/lib/factory.ts`, `web/ui/src/lib/factoryStore.ts` (new), `web/ui/src/lib/factoryStore.test.ts` (new).

**Steps:**

1. Add tests `initialLoadAndRefreshState`, `emptySnapshotIsAuthoritative`, `failedRefreshKeepsItemsAndLastUpdated`, `upsertReplacesAndAddsItems`, `staleListCannotOverwriteCommandUpsert`, `staleSingleItemReadReturnsNewerCachedItem`, `unrelatedUpsertDoesNotDiscardMissingItemRead`, `cachedItemDoesNotFetch`, `missingItemFetchesOnce`, `itemErrorsPropagate`, `activeAndIdleCadence`, `visibilityAndStopSuspendPolling`, and `concurrentRefreshCoalesces`.
2. Export `PollSeam` and implement C3 using the existing `createFactoryPoller` at `lib/factory.ts:createFactoryPoller`. Use `writable`/`get` from Svelte, `errMessage` for visible errors, and C2 `isFactoryActive` for the active callback.
3. Make `start`/`stop` idempotent. Keep one in-flight list promise. Record errors, clear loading/refreshing, preserve items, and rethrow so existing poller backoff still works. Apply `lastUpdated = now()` only for an accepted list snapshot.
4. Implement global/per-item mutation revisions exactly as C3. Do not add SSE, command methods, or view-specific filtering to this store.

**Verify:** `cd web/ui && npm test -- src/lib/factory.test.ts src/lib/factoryStore.test.ts src/lib/studio/model.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 3 — add shared factory store`.

## Task 4: Add roadmap pages and shared page primitives

**Goal:** Provide honest Planned destinations and reusable HIFI hierarchy, breadcrumb, state, focus, and safe-area primitives.

**Files:** `web/ui/src/lib/roadmap.ts` (new), `web/ui/src/lib/roadmap.test.ts` (new), `web/ui/src/views/Roadmap.svelte` (new), `web/ui/src/views/Roadmap.test.ts` (new), `web/ui/src/lib/ui/PageHeader.svelte` (new), `web/ui/src/lib/ui/PageHeader.test.ts` (new), `web/ui/src/lib/ui/StateMessage.svelte` (new), `web/ui/src/lib/ui/StateMessage.test.ts` (new), `web/ui/src/app.css`.

**Steps:**

1. Add registry tests `containsExactlyTheSixApprovedAreas`, `usesOnlyValidInternalAlternatives`, and `usesSafeHttpsDesignLinks`. Add component tests `rendersPlannedWithoutOperationalData`, `opensWorkingAlternatives`, and `rendersExternalDesignLinkSafely`. Add PageHeader breadcrumb/action tests in `PageHeader.test.ts` and StateMessage action/alert tests in `StateMessage.test.ts`.
2. Implement C4 `ROADMAP` with these fixed contents:
   - Planning: current New agent/New factory work item; planned durable overview, editable decisions, document revisions, and separate handoff; alternatives `#new` and `#factory/new`.
   - Voice: current text composers; planned recording, noninterrupting questions, editable transcript, and quiet capture; alternative `#new`.
   - Workflow: current project defaults and handoff overrides; planned structured stages, loops, parallel reviews, and inspectable planner proposals; alternatives `#projects` and `#factory`.
   - Hosts: current configured local/bridge runtime and no in-app enrolment/admission; planned connection, capabilities, capacity, and pause-new-work; no alternative route.
   - Environments: current Workspaces gallery/designer/builds; planned repository PR trial flow and companion-service lifecycle; alternative `#workspaces`.
   - Connections: current repository/provider/credential settings; planned unified forge connection and capability discovery; alternatives `#settings/repos`, `#settings/providers`, and `#settings/credentials`.
   Use `https://github.com/alecpullen/marshal/blob/main/docs/studio-fleet-factory/design-notes.md` as each entry's design link.
3. Implement Roadmap with a visible Planned badge, summary, current capabilities, planned list, alternatives, and external design link. It must make no API calls and render no sample agents, checks, timestamps, or capacity.
4. Implement C5 PageHeader/StateMessage with semantic headings, breadcrumbs, optional actions, and alert/status roles. In `app.css`, add shared `:focus-visible` accent outlines, `.studio-safe-bottom` for safe-area bottom padding, `.studio-panel` with the approved 12 px radius, and `.studio-control` with the approved 8 px radius. Do not change Warm Sunset tokens.

**Verify:** `cd web/ui && npm test -- src/lib/roadmap.test.ts src/views/Roadmap.test.ts src/lib/ui/PageHeader.test.ts src/lib/ui/StateMessage.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 4 — add roadmap and page primitives`.

## Task 5: Build responsive Studio navigation components

**Goal:** Provide one desktop navigation model and an accessible phone/tablet navigation without yet replacing the live shell.

**Files:** `web/ui/src/lib/nav/model.ts` (new), `web/ui/src/lib/nav/model.test.ts` (new), `web/ui/src/lib/nav/StudioNav.svelte` (new), `web/ui/src/lib/nav/StudioNav.test.ts` (new), `web/ui/src/lib/nav/MobileNav.svelte` (new), `web/ui/src/lib/nav/MobileNav.test.ts` (new).

**Steps:**

1. Add model tests `marksPrimaryAndSubroutesActive`, `groupsProjectsAndCurrentWorkItems`, `showsNeedsCount`, `marksRoadmapDestinationsPlanned`, `deduplicatesAllPages`, and `sortsProjectsByShortNameThenRoot`. Add component tests `desktopCollapseAndNavigate`, `mobilePrimaryDestinations`, `drawerOpensClosesAndRestoresFocus`, `drawerEscapeClosesWithoutResolvingAgentRequests`, and `plannedLinksOpenRoadmap`.
2. Implement C4 `buildNavModel`. Primary destinations are Fleet `#` and Inbox `#inbox`; resources are Projects, Live, Runs, Workspaces, Watches, Library, Usage, and Settings; roadmap destinations are Hosts, Environments, and Connections. Project links use `formatFactoryRoute({ projectRoot })`; current work-item links use C2 `factoryItemRoute`.
3. Build StudioNav with labelled open and icon-only collapsed forms, `aria-current`, Planned badges, project/work-item context, and the existing `onToggle` preference callback. Use existing glyphs and shared Button/Badge primitives.
4. Build MobileNav with a top app bar/menu button, bottom Fleet/Inbox/Library/Settings destinations, and a Bits UI Dialog drawer over `model.all`. Escape/outside close only the drawer, focus returns to the menu button, and navigation closes it. Do not reuse the decision Modal because its Escape behavior resolves agent requests.

**Verify:** `cd web/ui && npm test -- src/lib/nav/model.test.ts src/lib/nav/StudioNav.test.ts src/lib/nav/MobileNav.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 5 — build responsive Studio navigation`.

## Task 6: Extract reusable automation summaries

**Goal:** Share Home's existing review-draft and CI-fix loading with Fleet while isolating per-project failures.

**Files:** `web/ui/src/lib/automations/summary.ts` (new), `web/ui/src/lib/automations/summary.test.ts` (new), `web/ui/src/views/Home.svelte`, `web/ui/src/views/Home.test.ts`.

**Steps:**

1. Add `loadsDraftsAndRecentFixesAcrossProjects`, `keepsSuccessfulProjectsWhenOneProjectFails`, `returnsEmptyAfterSuccessfulEmptyReads`, and `usesRecentFixesWindow` tests. Add a Home test that a partial automation error is shown without removing Needs you or Running.
2. Implement C4 `loadAutomationSummaries` using the existing `listReviewDrafts({ project, status: 'draft' })`, `listCIHistory({ project })`, `recentFixes`, and `errMessage`. Catch per project, not only around the whole `Promise.all`; preserve project order and filter drafts to `status === 'draft'` as Home does today.
3. At Home's `drafts`, `fixes`, `roots`, and `$effect` anchors, replace the inline `Promise.all` with the helper and retain stale-effect cleanup and `automationTick` refetch behavior. Change the visible page title from Home to Inbox; leave scope, decisions, pending actions, DiskPanel, and ActivityFeed behavior intact.

**Verify:** `cd web/ui && npm test -- src/lib/automations/summary.test.ts src/views/Home.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 6 — extract automation summaries`.

## Task 7: Build the Fleet home and shared cards

**Goal:** Render the approved Fleet priority order from real fleet, inbox, factory, network, project, and automation data.

**Files:** `web/ui/src/views/Fleet.svelte` (new), `web/ui/src/views/Fleet.test.ts` (new), `web/ui/src/lib/studio/AttentionCard.svelte` (new), `web/ui/src/lib/studio/ProjectFactoryCard.svelte` (new), `web/ui/src/lib/studio/IndependentAgentList.svelte` (new), `web/ui/src/lib/studio/ProjectWarnings.svelte` (new), `web/ui/src/lib/studio/cards.test.ts` (new), `web/ui/src/views/Dashboard.svelte`.

**Steps:**

1. Add Fleet tests `combinesFactoryAgentIntakeAndNetworkAttention`, `linksDraftDecisionAndReadyItemsToTheirViews`, `summarizesEveryProjectAndFactoryOnlyRoot`, `excludesFactoryWorkersFromIndependentAgents`, `keepsAgentPendingRequestsDistinctFromFactoryDecisions`, `showsEmptyOnlyAfterAuthoritativeLoads`, `retainsFleetContentWhenFactoryRefreshFails`, `showsPartialAutomationErrors`, and `preservesProjectWarnings`. Add `cards.test.ts` cases `longTitlesWrapWithoutOverflow`, `missingProjectRegistrationIsExplicit`, `zeroCountsAreRealEmptyCounts`, and `cardActionsNavigateDirectly`.
2. Implement C5 cards. AttentionCard accepts an optional actions snippet for existing `PendingActions`; its main link uses `onNavigate`. ProjectFactoryCard links to the scoped factory, `#factory/new?project=…`, and `#new?project=…`; a factory-only root is labelled Project not registered rather than being dropped or marked unavailable. IndependentAgentList reuses `AgentCard`.
3. Move Dashboard's unavailable, untrusted, orphan-worktree, and collision markup into ProjectWarnings and make Dashboard use it until Task 9. Do not change warning copy or conditions.
4. Implement Fleet in the approved order: warnings, Needs you, Ready for review, Project factories, Independent agents, and Fleet resources. Use C2 projections and C4 automation loading. Include factory drafts/decisions, existing inbox queue, network decisions, ready factory items, ready agents/drafts/fixes, and all project roots. Use `factory.loading/error/lastUpdated` for partial stale/error presentation; never convert a failed factory read into an empty state.
5. Fleet props are the existing Home inputs plus `factory: FactoryState`, `onRefreshFactory: () => void`, and `onRefreshFleet: () => void`. Reuse `PendingActions`, `NetworkDecision`, `formatAutomationRoute`, and existing notice/decision callbacks. Do not add a second SSE connection or factory poller.

**Verify:** `cd web/ui && npm test -- src/views/Fleet.test.ts src/views/Home.test.ts src/lib/studio/model.test.ts src/lib/studio/cards.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 7 — build the Fleet home`.

## Task 8: Wire the shared factory store through existing views

**Goal:** Make App own the factory poller and remove independent list/item polling before the new shell goes live.

**Files:** `web/ui/src/App.svelte`, `web/ui/src/views/Factory.svelte`, `web/ui/src/views/Factory.test.ts`, `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/views/FactoryNew.test.ts`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**

1. Update component tests first with a reusable `factoryState` fixture and spies for `getItem`, `refresh`, and `upsert`. Replace the FactoryItem fake-timer polling assertion with `loadsPreviewOnceAcrossStoreUpdates`; assert store updates rerender the item while preview fetches remain one. Add `commandResponsesUpsertTheSharedStore`, `stale409RefreshesWithoutClearingRetryInput`, and `scopedRouteFiltersWithoutDuplicatingPoller`.
2. In App at `const { state: fleet, actions } = createFleetStore()`, create C3 `factoryStore`, expose `const factory = factoryStore.state`, start it in the existing `onMount`, and stop it in cleanup. Pass state/callbacks to the existing Factory, FactoryNew, and FactoryItem branches. Pass the already-parsed `factoryRoute.projectRoot/filter` to Factory; item view/agent wiring remains Task 16.
3. In Factory, remove `onMount`/`createFactoryPoller` and render from the supplied FactoryState. Add optional `projectRoot`/`filter` props and apply C2 `filterFactoryItems` to the exact project scope; full filter controls and the HIFI dashboard remain Task 12. Keep empty distinct from error and use the shared Retry callback.
4. In FactoryNew, call `upsert` with each successful `createFactoryItem` response before showing the draft. Preserve request identity and all payload logic.
5. In FactoryItem, remove its poller and direct `getFactoryItem` import. Load the initial item through supplied `getItem`, derive subsequent item updates from FactoryState, and upsert command responses. Keep `command`'s request-ID comparison, expected version, decision ID, 409 refresh, and all input state. Preview loading remains item-local and must not be triggered by store updates.
6. App callbacks passed as fire-and-forget refresh actions catch the store rejection after the store has recorded the visible error; FactoryItem's 409 path continues to await and handle refresh failure itself.

**Verify:** `cd web/ui && npm test -- src/lib/factoryStore.test.ts src/views/Factory.test.ts src/views/FactoryNew.test.ts src/views/FactoryItem.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 8 — wire shared factory state`.

## Task 9: Replace the app shell and prefill project agent creation

**Goal:** Make Fleet the default home, activate Inbox/roadmap/scoped routes, retire duplicate navigation, and support project-prefilled New agent links.

**Files:** `web/ui/src/App.svelte`, `web/ui/src/app.css`, `web/ui/src/views/NewAgent.svelte`, `web/ui/src/views/NewAgent.test.ts`, `web/ui/src/views/Home.svelte`, `web/ui/src/lib/Rail.svelte` (delete), `web/ui/src/lib/Sidebar.svelte` (delete), `web/ui/src/lib/Sidebar.test.ts` (delete), `web/ui/src/views/Dashboard.svelte` (delete).

**Steps:**

1. Add NewAgent tests `routeProjectOverridesRememberedProject`, `unavailableRouteProjectCannotStart`, `unknownRouteProjectShowsNoticeAndRequiresSelection`, and `bareRouteKeepsRememberedDefaults`. Assert mode/model remembered choices still apply when only the project is route-prefilled.
2. Replace App's Rail/Sidebar composition with C4 StudioNav/MobileNav and C2/C4 `buildNavModel`. Compute the nav needs count as factory needs-you items + inbox queue entries + network decisions. Keep the existing fleet SSE, notification, pending/client, palette, token, toast, and decision-outcome behavior.
3. Route `#` to Fleet, `#inbox` to Home, valid roadmap routes to Roadmap, and unknown routes to StateMessage Not Found with a Fleet action. Replace the `hash === '#new'` branch with `parseNewAgentRoute(hash)` and pass its `projectRoot` to NewAgent. Keep `#fleet` on the Task 1 compatibility redirect. Factory already receives scope/filter from Task 8; continue passing only `factoryRoute.itemId` to FactoryItem until Task 16 adds view/agent props. Pass `chatRoute.from` so Chat Back uses a valid factory return and otherwise `#`.
4. Add responsive shell classes implementing `<768` phone nav, `768–1199` collapsed rail/drawer, and `≥1200` labelled sidebar. Change the no-preference sidebar default to `window.innerWidth >= 1200`; retain `marshal.ui.sidebar` values and toggle behavior. Add mobile bottom spacing/safe-area handling without covering composers.
5. Add `projectRoot?: string` to NewAgent. After `listProjects`, apply the route root over only the remembered project/isolation choice; retain remembered mode/model. Unknown roots do not fall back to the first project. Disable creation for unavailable projects and guard `create()` as well as the button.
6. Remove Rail, Sidebar, Dashboard, and their obsolete tests only after all their destinations/warnings are covered by StudioNav, Fleet, and Home. Keep `Home.svelte` as the Inbox implementation.

**Verify:** `cd web/ui && npm test -- src/lib/routes.test.ts src/views/NewAgent.test.ts src/views/Home.test.ts src/views/Fleet.test.ts src/lib/nav/model.test.ts src/lib/nav/StudioNav.test.ts src/lib/nav/MobileNav.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 9 — replace the Studio app shell`.

## Task 10: Add shell-wide unsaved-route guards

**Goal:** Protect Project settings and FactoryNew input across cards, navigation, dialogs, and browser Back without double-confirming programmatic navigation.

**Files:** `web/ui/src/lib/navigationGuard.ts` (new), `web/ui/src/lib/navigationGuard.test.ts` (new), `web/ui/src/App.svelte`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`, `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/views/FactoryNew.test.ts`.

**Steps:**

1. Add guard unit tests `allowsWithoutGuard`, `delegatesToGuard`, and `clearsGuard`. Add Project/FactoryNew tests `registersAndClearsDirtyGuard`, `savedDraftClearsNavigationWarning`, and `cancelledNavigationKeepsAllInput`.
2. Implement C6. In App, create one guard and pass `register` to Project and FactoryNew. Make `navigate(next)` a no-op for the current hash, call `allow()` once, and record an `approvedNext` hash before assigning `window.location.hash` so the resulting hashchange does not ask again.
3. At App's existing `update` hashchange handler, compare the incoming hash with the displayed hash. Consume `approvedNext` when present; otherwise call `allow()`. If the guard rejects a browser Back/manual hash change, restore the displayed hash with `history.replaceState(null, '', currentHash)` and leave the mounted form untouched. Run legacy redirects only after the incoming route is allowed.
4. At Project's `confirmLeave`, `navigate`, and `beforeunload` anchors, register a guard that returns true when clean and otherwise calls the existing confirmation copy. Remove the now-redundant local `navigate` precheck so App does not confirm twice; retain `beforeunload` and unregister on destroy.
5. In FactoryNew, introduce `hasUnsavedInput = !draft && hasInput()` and use it for both the route guard and the existing `beforeunload` effect. A successfully saved draft therefore permits navigation and reload without a false dirty warning. Otherwise dirty input uses `window.confirm('You have an unsaved factory handoff. Leave without saving it?')`. Retain input preservation and the new-tab project-settings link.

**Verify:** `cd web/ui && npm test -- src/lib/navigationGuard.test.ts src/views/Project.test.ts src/views/FactoryNew.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 10 — guard unsaved Studio routes`.

## Task 11: Verify and document slice 1 in browsers

**Goal:** Prove the Fleet-first shell at phone/tablet/desktop widths, add accessibility coverage, and ship the regenerated embedded SPA.

**Files:** `web/ui/package.json`, `web/ui/package-lock.json`, `web/ui/playwright.config.ts`, `web/ui/e2e/fixtures.ts` (new), `web/ui/e2e/studio-shell.pw.ts` (new), `web/ui/e2e/studio-fleet.pw.ts` (new), `web/ui/e2e/project-settings.pw.ts`, `web/ui/e2e/README.md`, `scripts/e2e/run_browser_checks.sh`, `docs/studio-fleet-factory/README.md`, `docs/studio-fleet-factory/studio-uiux-alignment-design.md`, `web/bridge/static/index.html`, `web/bridge/static/assets/` (generated).

**Steps:**

1. Run `cd web/ui && npm install --save-dev @axe-core/playwright`. Change Playwright desktop to 1440×900, retain phone 390×844, and add tablet 768×1024. Keep one worker and real fixture isolation.
2. Extract the existing `open`, `auth`, and `expectNoHorizontalOverflow` helpers from `project-settings.pw.ts` into `e2e/fixtures.ts`; add deterministic `FactoryItem`, `AgentStatus`, `ProjectStatus`, and `Inbox` fixture builders plus API route helpers. Update project-settings imports without changing its cases.
3. Add real-bridge tests `emptyFleetHasWorkingStartActions`, `allRoadmapPagesArePlannedAndReadOnly`, `projectPrefillAndUnknownProject`, `shellDeepLinksAndBrowserBack`, `routeGuardCoversShellNavigationAndBrowserBack`, and `unknownRouteHasFleetReturn`. Add mocked-presentation tests `mixedFleetGroupsRealShapedWork`, `factoryFailureDoesNotHideAgents`, `factoryWorkersAreNotIndependent`, `unavailableProjectCannotStart`, `fleetAttentionAndProjectLinksSurviveReload`, and `oneFleetStreamAndFactoryPollerSurviveNavigation`.
4. For the request-count test, intercept `/api/events?stream=fleet…` and `/api/factory/items`; navigate Fleet → Inbox → project factory → Fleet and assert no additional fleet stream or immediate factory-list read is created by route mounting. Use the existing SSE parser's actual `/api/events` route and avoid waiting long enough for an intentional reconnect/poll.
5. Add `expectNoHorizontalOverflow` and AxeBuilder scans to Fleet in dark and light themes. Add keyboard-only navigation and reduced-motion cases. Capture `fleet-desktop.png`, `fleet-tablet.png`, and `fleet-phone.png` through Playwright's test output, then copy the reviewed artifacts to `.docs-archive/studio-uiux/`.
6. Update browser README and the runner's comments for desktop/tablet/phone and real-versus-mocked fixtures. Link the approved spec and this plan from the Studio README. Append a slice-1 implementation record to the spec with commit, test counts, screenshot paths, and any baseline failures; do not claim live factory execution.
7. Run `npm run build` through the browser runner and include the resulting tracked embedded asset changes.

**Verify:** From the repository root, `scripts/e2e/run_browser_checks.sh` — all existing and new desktop/tablet/phone cases pass. `git diff --check` — no output. Commit `studio-uiux-alignment: task 11 — verify Fleet and navigation`.

## Task 12: Build the project-scoped factory dashboard

**Goal:** Deliver the HIFI project factory with decisions, filters, work-item cards, defaults, linked agents, and direct working actions.

**Files:** `web/ui/src/lib/studio/WorkItemCard.svelte` (new), `web/ui/src/lib/studio/WorkItemCard.test.ts` (new), `web/ui/src/lib/studio/FactoryDefaultsSummary.svelte` (new), `web/ui/src/views/Factory.svelte`, `web/ui/src/views/Factory.test.ts`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`, `web/ui/src/App.svelte`.

**Steps:**

1. Add WorkItemCard tests `showsStateStagePRChecksAndWorkers`, `usesDirectDraftDecisionReadyActions`, and `handlesUnknownStateAndLongContent`. Add Factory tests `scopesItemsToProject`, `filtersCurrentNeedsYouReadyAndHistoryThroughRoutes`, `showsNeedsDecisionBeforeWorkItems`, `loadsDefaultsWithoutBlockingItems`, `listsUniqueCurrentLinkedAgents`, and `showsGlobalAndScopedEmptyStates`.
2. Implement C5 WorkItemCard using C2 `factoryItemRoute`, `partitionFactoryChecks`, and worker IDs. Show actual state/stage, PR count, latest-round check counts, and linked-agent count; do not show percentages or a workflow strip.
3. Generalize Factory props to `projectRoot?: string`, `filter?: FactoryFilter`, `agents: AgentRow[]`, FactoryState, refresh, and navigation. App passes `$fleet.agents` for linked-worker status. Use C2 filtering/summaries. Add Current/Needs you/Ready/History controls that navigate with `formatFactoryRoute`, preserving project scope.
4. On scoped mount, load `getProjectSettings(projectRoot)` into FactoryDefaultsSummary. Show readable workspace, implementer/reviewer, target branch, expected checks, review instructions, rounds, and CI timing; a settings failure is a partial warning and leaves items usable. Link Customize to `formatProjectRoute(root)` and Workflow editor to `#roadmap/workflow`.
5. Add an expandable linked-agents section from current items, with real chat links and parked/status text when the fleet row is available. Add New work item and Start agent actions. At Project's existing Create factory work item link, add an Open project factory link using the scoped route.
6. Update App's factory branch to pass the parsed scope/filter. Keep global `#factory` as the same component with no scope.

**Verify:** `cd web/ui && npm test -- src/lib/studio/WorkItemCard.test.ts src/views/Factory.test.ts src/views/Project.test.ts src/lib/studio/model.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 12 — build the project factory dashboard`.

## Task 13: Reorganize handoff around effective configuration

**Goal:** Replace the dense technical form with the approved grouped handoff and readable summary while preserving every creation/default rule.

**Files:** `web/ui/src/lib/factory/config.ts` (new), `web/ui/src/lib/factory/config.test.ts` (new), `web/ui/src/lib/factory/ProfileSelect.svelte` (new), `web/ui/src/lib/factory/EffectiveConfigSummary.svelte` (new), `web/ui/src/views/FactoryNew.svelte`, `web/ui/src/views/FactoryNew.test.ts`.

**Steps:**

1. Add config tests `profileOfReadsOnlyStringProfiles`, `keepUnavailableRetainsSavedChoice`, `formatFactoryCheckUsesBothSyntaxes`, and `resolvedConfigRowsCoverEveryApprovalField`. Add FactoryNew tests `groupsHandoffAndShowsReadableSummary`, `focusesFirstInvalidField`, `summaryTracksOverridesWithoutMaterializingThem`, and `savedDraftUsesServerResolvedRows`. Keep all existing inheritance/request-ID tests.
2. Implement C5 config helpers and ProfileSelect/EffectiveConfigSummary. ProfileSelect uses a bindable value, a follow/empty option where supplied, sorted real profiles, and an unavailable saved choice. EffectiveConfigSummary renders labelled rows, source text, warnings, consequence, and optional plan/spec disclosures without raw JSON.
3. At FactoryNew's existing `Effective`, `profileOf`, `keepUnavailable`, `formatCheck`, `buildConfig`, `payload`, and `save` anchors, move only reusable helpers to config.ts. Preserve `buildConfig` and `payload` semantics byte-for-byte in behavior: omitted following fields, explicit empty checks, native/workspace conflict, repository branch fallback, and identical retry identity.
4. Reorganize the markup into Scope, Repository and environment, Roles and review, Authority and limits, and Approval summary. At `≥1200`, use two columns with the summary beside the form; below that use one column. On phone, Scope and final consequences remain visible, other sections are labelled disclosures, and Advanced starts closed.
5. Replace the current submit-only generic error path with field-linked errors plus the existing alert summary. Add `data-field` to Title, Plan, Repository, Implementer model profile, Reviewer model profile, Expected checks, and no-CI acknowledgement. On validation failure, set the relevant error, call `tick()`, and focus the first invalid control. Preserve server errors and all user input.
6. Keep the Task 10 route guard and new-tab Project settings link. Successful create still upserts the shared store and shows the server-returned draft; it does not approve or start work.

**Verify:** `cd web/ui && npm test -- src/lib/factory/config.test.ts src/views/FactoryNew.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 13 — reorganize factory handoff`.

## Task 14: Extract readable draft approval

**Goal:** Reuse the effective-configuration presentation for the saved draft's explicit Approve & start decision.

**Files:** `web/ui/src/lib/factory/DraftApproval.svelte` (new), `web/ui/src/lib/factory/DraftApproval.test.ts` (new), `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**

1. Add DraftApproval tests `showsResolvedConfigAndConsequence`, `showsPlanAndOptionalSpecification`, `approvesOnlyOnExplicitAction`, and `disablesWhileBusy`. Update FactoryItem's approval test to assert no raw routing JSON and the same resolved rows shown after creation.
2. Implement DraftApproval with `item: FactoryItem`, `busy: boolean`, and `onApprove: () => void`. Use C5 `resolvedConfigRows(item.draft.config)` and EffectiveConfigSummary. State that Approve & start authorizes execution under the displayed resolved snapshot; do not claim work has already started.
3. At FactoryItem's `data-testid="draft-approval"` block, replace the raw route JSON and duplicated `<dl>` content with DraftApproval. Keep the existing `command('approve')` path, expected version, request identity, and busy behavior.

**Verify:** `cd web/ui && npm test -- src/lib/factory/DraftApproval.test.ts src/views/FactoryItem.test.ts src/views/FactoryNew.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 14 — extract readable draft approval`.

## Task 15: Verify and document slice 2 in browsers

**Goal:** Prove project factory and handoff/draft UX across viewports and ship the regenerated embedded SPA.

**Files:** `web/ui/e2e/factory-handoff.pw.ts` (new), `web/ui/e2e/fixtures.ts`, `web/ui/e2e/README.md`, `docs/studio-fleet-factory/studio-uiux-alignment-design.md`, `web/bridge/static/index.html`, `web/bridge/static/assets/` (generated).

**Steps:**

1. Add real-bridge cases `projectFactoryLinksPreserveScope`, `handoffLoadsRealDefaultsAndValidation`, `handoffRouteGuardRetainsInput`, and `handoffHasNoOverflow`. Do not save or approve against the credential-free bridge.
2. Add explicitly mocked-presentation cases `projectFactoryFiltersAndDirectActions`, `savedDraftShowsResolvedServerConfiguration`, `draftApprovalDoesNotStartBeforeClick`, and `handoffSummaryTracksOverrides`. Intercept create/command calls and assert payloads; label the suite as presentation coverage, not live factory proof.
3. Add AxeBuilder scans for project factory, handoff, and draft approval in both themes plus automated keyboard disclosure/tab navigation. Perform a manual 200% browser-zoom smoke check and record the result in the slice-2 implementation record. Capture `factory-desktop.png`, `factory-phone.png`, `handoff-desktop.png`, `handoff-phone.png`, and `draft-phone.png` into `.docs-archive/studio-uiux/` through test output.
4. Append the slice-2 implementation record with commits, test counts, screenshots, and the explicit statement that real draft creation/approval was not run because the isolated bridge has no GitHub credential. Update the browser README's coverage table.
5. Regenerate embedded assets with the browser runner's build step.

**Verify:** From the repository root, `scripts/e2e/run_browser_checks.sh` — all cases pass. `git diff --check` — no output. Commit `studio-uiux-alignment: task 15 — verify project factory and handoff`.

## Task 16: Add work-item views, activity, documents, and PRs

**Goal:** Turn the monolithic item page into a route-driven HIFI workspace without changing command behavior.

**Files:** `web/ui/src/lib/factory/WorkItemHeader.svelte` (new), `web/ui/src/lib/factory/ActivityTimeline.svelte` (new), `web/ui/src/lib/factory/DocumentsView.svelte` (new), `web/ui/src/lib/factory/PullRequestsView.svelte` (new), `web/ui/src/lib/factory/ActivityTimeline.test.ts` (new), `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/src/App.svelte`.

**Steps:**

1. Add FactoryItem tests `initialViewFollowsStateOnlyOnce`, `explicitViewSurvivesStatePolling`, `approvalOnNonDraftFallsBackToActivity`, `tabsNavigateWithoutRemountingItem`, `documentsShowCurrentAndPriorApprovalRevisions`, and `pullRequestsShowRealRevisionAndState`. Add ActivityTimeline tests `ordersBySequence`, `mapsEffectsToWorkerRolesWhenAvailable`, `keepsUnmappedEventsFactoryLevel`, and `showsNewActivityCueWhenReadingOlderEvents`.
2. Implement WorkItemHeader with compact state/stage/round summary, breadcrumbs, view tabs, and secondary stop action. Approval is available only for drafts; other views are Activity, Review, Pull requests, and Documents. Use `formatFactoryRoute` for every tab.
3. Implement ActivityTimeline using C2 `sortedFactoryHistory`; associate `effectId` with effects/workers where possible and otherwise label Factory. Initially position at latest, preserve scroll when hidden, and show a New activity button only when new events arrive while not at the latest. Do not merge session streams or invent percentages.
4. Implement DocumentsView from the latest approval when present, otherwise the draft, with prior approval revisions expandable. Implement PullRequestsView from actual `item.prs`, with draft/state/head/base and safe external links.
5. Add `route: FactoryRoute` to FactoryItem and pass it from App. Initialize the selected view once using C2 `defaultFactoryItemView`; explicit route changes update the view, but item polling never does. Keep the same `{#key factoryRoute.itemId}` in App so query changes do not remount FactoryItem.
6. Move existing header/history/PR/draft-plan markup into these components. Keep decision and review components operational during their later extractions: decisions remain pinned in activity, and a decision banner in other views links back to activity. Worker links still open full Chat in this task.

**Verify:** `cd web/ui && npm test -- src/lib/factory/ActivityTimeline.test.ts src/views/FactoryItem.test.ts src/lib/routes.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 16 — add work-item views and activity`.

## Task 17: Extract decisions and recovery controls

**Goal:** Present clear decision/retry/recovery panels while preserving request identity, stale-version handling, and named-worker safeguards.

**Files:** `web/ui/src/lib/factory/DecisionPanel.svelte` (new), `web/ui/src/lib/factory/RecoveryPanel.svelte` (new), `web/ui/src/lib/factory/DecisionPanel.test.ts` (new), `web/ui/src/lib/factory/RecoveryPanel.test.ts` (new), `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**

1. Add DecisionPanel tests `answersCurrentDecision`, `decisionInputSurvivesItemRefresh`, and `unknownDecisionHasNoUnsupportedAction`. Add RecoveryPanel tests `retryRequiresReasonAndAllowance`, `routeOnlyRetryOmitsReplacement`, `replacementRetryUsesLatestApproval`, `retryInputSurvivesItemRefresh`, `loadsRealProfileChoices`, `retainsUnavailableCurrentProfile`, `refreshesEvidence`, `requiresExactNamedWorkerAttestation`, and `noCrossProviderContinueOption`.
2. Move answer state into DecisionPanel. Move reason, additional rounds, route replacements, replacement plan/spec/instructions, attestation, and worker-name state into RecoveryPanel. Do not key panels by item version; a same-decision refresh must not clear input.
3. Change FactoryItem's existing `command(kind)` anchor to `command(kind, input?: FactoryCommandInput)` implementing C5. Build the same `FactoryCommand` with latest `item.version`, decision ID, stable pending-request comparison, and upsert response. On 409, clear only the obsolete pending command and refresh; child input remains mounted.
4. DecisionPanel handles question decisions and renders unknown decision kinds with their question/reason but no unsupported command. RecoveryPanel handles exhausted/no-progress/worker-failed/invalid-result/CI-unavailable/CI-wait/conflict and named-worker recovery decisions. It loads `getModels()` only for retry-capable decisions, not for named-worker attestation, and uses C5 ProfileSelect for retry routes. Preserve the exact fresh-round copy and never offer an unimplemented continue-conversation option.
5. Keep Request stop and stopping confirmation outside these panels in WorkItemHeader/status. Keep all existing API payload assertions in FactoryItem tests as integration coverage.

**Verify:** `cd web/ui && npm test -- src/lib/factory/DecisionPanel.test.ts src/lib/factory/RecoveryPanel.test.ts src/views/FactoryItem.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 17 — extract decisions and recovery`.

## Task 18: Extract CI-first review, narration, and previews

**Goal:** Deliver the approved review hierarchy while preserving revision evidence, URL safety, and preview request bounds.

**Files:** `web/ui/src/lib/factory/ReviewView.svelte` (new), `web/ui/src/lib/factory/ReviewView.test.ts` (new), `web/ui/src/lib/factory/CIEvidence.svelte` (new), `web/ui/src/lib/factory/EngineeringReport.svelte` (new), `web/ui/src/lib/factory/PreviewCard.svelte` (new), `web/ui/src/lib/factory/reviewModel.ts` (new), `web/ui/src/lib/factory/reviewModel.test.ts` (new), `web/ui/src/lib/factory/PreviewCard.test.ts` (new), `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`.

**Steps:**

1. Move the existing FactoryItem narration and CI cases into `ReviewView.test.ts`, and preview/unsafe-URL/stale-response/mount-count cases into `PreviewCard.test.ts`. Add `failedChecksAndFindingsPrecedeCompactPasses`, `incompleteOrStaleCIIsNotSuccess`, `previewHeadMismatchIsWarning`, and `reviewFailureIsNotCleanReview`.
2. Implement reviewModel helpers for check-state text, narration sections, preview segments, status links, `isHttpUrl`, bounded display text, and head badges. Preserve the existing D10 validator exactly: literal absolute HTTP(S), nonempty host, no whitespace/control/angle/backtick, at most 2048 UTF-8 bytes, and no repair.
3. Implement CIEvidence using C2 `partitionFactoryChecks`; failures and related review findings are expanded, passes start collapsed, and incomplete/missing/stale-head evidence has explicit warning copy. Move review findings out of EngineeringReport into ReviewView's failure/findings section. EngineeringReport retains the narration section order, summary fallback, and implementation/review summaries, questions, and reasons without duplicating findings.
4. Move preview state, `loadPreview`, request sequence, alive cleanup, and Refresh from FactoryItem into PreviewCard. Props are `repoId`, `branch`, `prHead`, and current-revision `statusLinks`. Load once on mount, never on factory store updates, keep newest response, and retain all empty/error/gap/deployment/environment presentations.
5. Implement ReviewView in the approved order: CI/failures/findings/repair state, compact passes, PreviewCard, EngineeringReport, and actual PR/forge links. Replace FactoryItem's old two-column CI/report/preview block with ReviewView at the review route. Do not add Merge.

**Verify:** `cd web/ui && npm test -- src/lib/factory/reviewModel.test.ts src/lib/factory/ReviewView.test.ts src/lib/factory/PreviewCard.test.ts src/views/FactoryItem.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 18 — extract CI-first factory review`.

## Task 19: Extract an embeddable agent conversation

**Goal:** Reuse the complete existing Chat session behavior in a component that can safely render inside a factory item.

**Files:** `web/ui/src/lib/session/AgentConversation.svelte` (new), `web/ui/src/lib/session/AgentConversation.test.ts` (new), `web/ui/src/views/Chat.svelte`, `web/ui/src/views/Chat.test.ts`.

**Steps:**

1. Add embedded tests `ownsAndDisconnectsSelectedSession`, `doesNotWriteChatHash`, `hidesDockAndExitPanel`, `backReturnsToActivity`, `taskAffordanceOpensFullSessionTasks`, and `pendingPermissionAndQuestionStillResolve`. Keep every existing Chat test as full-session regression coverage.
2. Move the current Chat script/markup/style into AgentConversation and make Chat a thin wrapper passing `sessionId`, `route`, `agent`, and `onBack`. Preserve the existing `createStackStore`, `createSessionStore`, `routeEvent`, drill stores, transcript, Composer, modals, highlighter, and cleanup paths.
3. Add C7 `embedded`/`onOpenFull` behavior. Embedded header uses Back to activity and Open full session; hide Dock, ExitPanel, StatusShare, Review, and Network. Keep ModeSwitcher, connection state, transcript, Composer, and pending modals.
4. At the dock URL `$effect`, return before `formatChatRoute`/`history.replaceState` when embedded. In full mode include `from: route?.from` so dock changes cannot erase the factory return. Disable dock-only latest-edit/tasks effects and ⌘J/⌘P handling in embedded mode.
5. In embedded mode, NowBar `onOpenTasks`, browse Inspect, and file-open actions call `onOpenFull('tasks'|'inspect'|'files')`; do not dispatch selections to an invisible dock. Transcript reading, density, subagent drill, copy, steering, cancellation, and access changes retain current behavior.
6. Key embedded usage by `sessionId` in the parent so changing agents creates a fresh store and disconnects the previous one. Full Chat remains keyed by App's existing `chatSessionId`.

**Verify:** `cd web/ui && npm test -- src/lib/session/AgentConversation.test.ts src/views/Chat.test.ts src/lib/routes.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 19 — extract embeddable agent conversation`.

## Task 20: Integrate factory activity and agent drill-in

**Goal:** Deliver the HIFI shared-activity/agent-conversation layout with deterministic selection and reliable return paths.

**Files:** `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/src/lib/factory/ActivityTimeline.svelte`, `web/ui/src/lib/factory/ActivityTimeline.test.ts`, `web/ui/src/App.svelte`.

**Steps:**

1. Add FactoryItem tests `explicitAgentRouteWins`, `initialDesktopSelectionUsesLastWorker`, `noWorkersUsesFullWidthActivity`, `phoneDrillInHasBackToActivity`, `queryChangesDoNotRemountFactoryItem`, `openFullSessionCarriesValidatedFromRoute`, and `unknownAgentShowsInlineUnavailable`. Stub `matchMedia` in this test file so desktop/phone selection is deterministic. Add ActivityTimeline tests for preserved scroll/disclosure state while hidden.
2. Replace full-chat worker links in the activity view with `formatFactoryRoute({ itemId, view: 'activity', agent })`. Use a cleaned-up `matchMedia('(min-width: 1200px)')` listener for selection behavior. At `≥1200`, show activity beside a keyed AgentConversation; below that, show activity until an agent route explicitly selects the focused conversation.
3. An explicit valid worker agent route takes precedence. On initial desktop entry without an agent, select the last `item.workers` record; do not change selection on later polls. Parked workers remain openable and labelled. No workers means no empty conversation pane.
4. Keep the activity component mounted and hide it during internal drill-in so scroll/disclosure state survives. Back to activity removes only the agent parameter. Unknown agent IDs render an inline unavailable state with that action.
5. Implement `onOpenFull(tab)` with `formatChatRoute({ id: agentId, view: 'session', tab, from: formatFactoryRoute(currentItemRoute) })`. App's Chat Back already consumes validated `from`; subsequent full-session dock changes preserve it through Task 19. Only the selected AgentConversation mounts a session SSE connection.

**Verify:** `cd web/ui && npm test -- src/views/FactoryItem.test.ts src/lib/factory/ActivityTimeline.test.ts src/lib/session/AgentConversation.test.ts src/views/Chat.test.ts` — pass. `npm run check` — no new errors. Commit `studio-uiux-alignment: task 20 — integrate factory agent drill-in`.

## Task 21: Verify and document slice 3 in browsers

**Goal:** Prove active execution, decisions/recovery, CI-first review, and agent return paths across viewports and ship the regenerated embedded SPA.

**Files:** `web/ui/e2e/factory-item.pw.ts` (new), `web/ui/e2e/fixtures.ts`, `web/ui/e2e/README.md`, `docs/studio-fleet-factory/studio-uiux-alignment-design.md`, `web/bridge/static/index.html`, `web/bridge/static/assets/` (generated).

**Steps:**

1. Add mocked-presentation fixtures for active implementation, question, exhausted retry, named-worker recovery, failed CI, ready review, multiple workers/PRs, and preview head mismatch. Include long titles, logs, code, and missing optional fields.
2. Add browser cases `activeItemOpensActivity`, `agentDrillInReturnsToSameActivity`, `fullSessionBackReturnsToFactory`, `questionAnswerUsesCurrentDecision`, `retryUsesFreshRoundCopyAndRealPayload`, `recoveryAttestationRemainsDeliberate`, `failedCIPrecedesNarration`, `readyReviewCompactsPasses`, `previewMismatchIsVisible`, and `itemStatePollDoesNotChangeSelectedView`.
3. Mock the existing session endpoints used by AgentConversation: `/api/sessions/<id>/load`, `/stack`, `/access`, `/prompt`, `/steer`, `/cancel`, and `/api/events?sessionId=<id>…`. Add empty real-shaped responses for the existing diff/gate/file/task/terminal/preview reads mounted by full Chat so the return test is not obscured by unrelated dock errors. Assert one selected-session stream and no stream for unselected workers. These fixtures prove presentation/navigation only, not live agent execution.
4. Add AxeBuilder scans for active, decision, recovery, and ready views in both themes plus automated keyboard-only drill-in/return, reduced-motion, and no-horizontal-overflow checks. Perform a manual 200% browser-zoom review and record it in the slice-3 implementation record. Verify code/log containers scroll internally and primary touch targets are at least 44 px.
5. Capture `execution-desktop.png`, `execution-phone.png`, `decision-phone.png`, `recovery-desktop.png`, `ready-review-desktop.png`, and `ready-review-phone.png` into `.docs-archive/studio-uiux/` through test output. Compare them with the approved HIFI anchors for hierarchy and interaction, not palette/sample data.
6. Append the slice-3 implementation record with commits, test counts, screenshots, and explicit mocked-fixture limits. Update browser README coverage. Regenerate embedded assets through the runner.

**Verify:** From the repository root, `scripts/e2e/run_browser_checks.sh` — all cases pass. `git diff --check` — no output. Commit `studio-uiux-alignment: task 21 — verify execution and review`.

## Task 22: Reconcile documentation and run final verification

**Goal:** Close the approved redesign with current public docs, a complete outcome record, and the repository's full relevant verification gates.

**Files:** `docs/studio-fleet-factory/studio-uiux-alignment-design.md`, `docs/studio-fleet-factory/README.md`, `docs/studio-fleet-factory/plans/2026-11-15-studio-uiux-alignment-plan.md`, `docs/web-studio/design.md`, `web/ui/e2e/README.md`, `web/bridge/static/index.html`, `web/bridge/static/assets/` (generated if final source changed).

**Steps:**

1. Reconcile `docs/web-studio/design.md` §6–7 with the implemented Fleet-first information architecture: `#` Fleet, `#inbox` full inbox, scoped project factory routes, responsive navigation, and the newer HIFI design's precedence over the old Home/Fleet split. Do not rewrite historical mockup files.
2. Update the Studio README's active specification/implementation links and mark slices 1–3 with their actual status. Keep planning, voice, workflow editing, hosts, richer connections/environments, merge, and cross-device continuity listed as Planned unless a real implementation exists.
3. Append this plan's outcome record: execution branch/base, one commit per task, any deviations, exact verification results/counts, screenshot locations, known baseline failures, and remaining roadmap capabilities. Do not claim a check passed unless its command output proves it.
4. Run a final real-bridge smoke pass for Fleet, Inbox, project settings, handoff loading/validation, roadmap pages, deep-link reload, and browser Back. Review the generated SPA served by webbridge, not Vite. Confirm no external provider, forge, credential, or live agent is used by the fixture.
5. Run every gate below from the repository root unless the command changes directory. Record results in the outcome record before committing.

**Verify:**

```bash
cd web/ui
npm test
npm run check
npm run build
cd ../..
scripts/e2e/run_browser_checks.sh
go test ./web/bridge/...
CGO_ENABLED=1 go build ./cmd/webbridge
scripts/test.sh quick
go vet ./...
git diff --check
```

Expected: all commands pass, or an actually pre-existing baseline failure is reproduced on the base and recorded precisely rather than hidden. `npm run check` must have no new errors versus the recorded base. `TestWebIsStdlibOnly` remains in the Go bridge gate. Commit `studio-uiux-alignment: task 22 — reconcile docs and verify Studio`.

## Integration notes

- The first three slices require no new Go bridge endpoint or generated ACP contract change. If execution discovers that a required field is absent, stop that behavior's implementation and revise the approved design; do not substitute fixture data in production.
- `web/` remains standard-library-only on the Go side. The Svelte UI remains an external client and does not import `marshal/internal/...`.
- Existing factory create/approve/retry/recover semantics, project conditional writes, session access control, pending-request resolution, preview URL validation, and agent lifecycle are safety contracts, not visual details to rewrite.
- Mocked Playwright fixtures are named and documented as presentation tests. Only real bridge/API tests support claims about actual persistence or execution.
- Each slice's `npm run build` regenerates the tracked embedded SPA. Review generated asset diffs with the source diff and rebuild `webbridge` before the final smoke test.
- The original HIFI HTML/screenshots remain unchanged. New visual evidence belongs in `.docs-archive/studio-uiux/` unless deliberately promoted to public docs.

## Outcome record

Executed on branch `feat/studio-uiux-alignment` from base `f42a7c59`, in an isolated git worktree. One commit per task; tasks with disjoint files were run concurrently, which is why the commit order is not the numeric order.

| Commit | Task |
|---|---|
| `16fb94c9` | adds the approved design and this plan |
| `68854810` | task 1 — extend Studio hash routes |
| `0208bb5a` | task 2 — build Fleet and factory projections |
| `91cc34a4` | task 3 — add shared factory store |
| `995a996f` | task 6 — extract automation summaries |
| `16a4c000` | task 4 — add roadmap and page primitives |
| `48f60a46` | task 5 — build responsive Studio navigation |
| `f349473d` | task 7 — build the Fleet home |
| `cde6f5c7` | task 8 — wire shared factory state |
| `7d4c7c95` | task 9 — replace the Studio app shell |
| `7c994227` | task 10 — guard unsaved Studio routes |
| `8b2f33ce` | task 11 — verify Fleet and navigation |
| `804f08d0` | task 12 — build the project factory dashboard |
| `8a1ef3af` | task 13 — reorganize factory handoff |
| `0787f18d` | task 14 — extract readable draft approval |
| `7324872f` | task 15 — verify project factory and handoff |
| `8f939e42` | task 16 — add work-item views and activity |
| `a66d37b8` | task 17 — extract decisions and recovery |
| `2ee4f248` | task 18 — extract CI-first factory review |
| `6f4444b6` | task 19 — extract embeddable agent conversation |
| `e85c10ef` | task 20 — integrate factory agent drill-in |
| `90882147` | task 21 — verify execution and review |
| this commit | task 22 — reconcile docs and verify Studio |

### Deviations from the plan

- **Task 1, `Page` union.** The plan's C1 `Page` type has no `home` member, but `App.svelte` (not a task-1 file) still compared against it. A temporary `home` member kept the exported type compatible for one task; task 9 removed it together with the old shell, so the shipped `Page` is exactly C1's.
- **Task 1, anchor drift.** `listFactoryItems`/`getFactoryItem` live in `web/ui/src/lib/api.ts`, not `lib/factory.ts`; the store imports them from where they are rather than moving them.
- **Task order.** Tasks 6, 4 and 5 were executed before task 3's neighbours completed because their files are disjoint. Each still landed as its own commit with its own verification.
- **Task 11, tablet viewport.** Step 1 adds a third project (768×1024) to the Playwright config, which multiplies the pre-existing `project settings` failure from 6 to 9 (three cases × three widths). The plan's "exactly 6 pre-existing failures" bar is therefore unachievable after step 1; 9 is the recorded baseline.
- **Task 15, stale browser assertions.** Three assertions in `project-settings.pw.ts` still named the handoff summary's old `*-effective` hooks, which task 13 replaced with `config-*` rows. They were updated in place (3 lines, same intent) rather than restoring the old hooks, which is what returned the suite to exactly the 9-case baseline.
- **Task 21, zoom review.** The 200% zoom review is a measured Playwright case at a 720×450 CSS viewport (desktop only, skipped at the other two widths) rather than a hand-driven check. All four surfaces reported `clientWidth` 720 with `scrollWidth` 720, and the primary action stayed 44px tall and unclipped.
- **Task 21, extra cases.** Seven cases beyond the ten the plan names — one aggregate several-workers/PRs case, five accessibility/robustness cases, one zoom measurement — because step 4's requirements had no case of their own. 17 cases total.
- **Task 22, scope of the older design doc.** `docs/web-studio/design.md` §6–7 were reconciled as the plan requires; §8's page-level specs and the historical mockup HTML were left untouched.
- **Task 7/9, retired Dashboard behaviour.** The fleet store's `error` string is no longer rendered anywhere. `#pending`/`#sessions` remain routed and reachable from the palette and project cards.

### Final verification

Run at the repository root in the execution worktree unless the command changes directory.

| Command | Result |
|---|---|
| `cd web/ui && npm test` | 110 files, **1154 tests passed**, exit 0 |
| `cd web/ui && npm run check` | **5 errors and 1 warning in 6 files** (the recorded baseline, unchanged) |
| `cd web/ui && npm run build` | exit 0; no resulting asset diff |
| `scripts/e2e/run_browser_checks.sh` | **150 cases: 139 passed, 2 skipped, 9 failed** |
| `go test ./web/bridge/...` | ok (`marshal/web/bridge` 131.3s; `contract`, `contract/tsgen` ok) |
| `CGO_ENABLED=1 go build ./cmd/webbridge` | exit 0 |
| `go vet ./...` | exit 0 |
| `scripts/test.sh quick` | exit 0 |
| `git diff --check` | clean |

The five `npm run check` errors, all pre-existing and all outside this redesign's new code: `lib/session/AgentConversation.svelte:754:92` (the `Chat.svelte` error, moved with the extracted component), `views/Run.svelte:336:74`, `lib/fleet.test.ts:78:42`, `views/Review.test.ts:49:3`, `views/Runs.test.ts:15:52`.

The 9 browser failures are the three `project settings` cases — role overrides, the stale-save warning, and unsaved-edit confirmation — each at desktop, tablet and phone. They time out on `selectOption('gamma')` because the credential-free fixture bridge serves no model profiles for that case. No other case fails. The 2 skips are the 720×450 zoom case at the two widths it does not measure.

### Smoke pass

The real-bridge Playwright projects review the SPA the binary serves, not Vite: `studio-shell.pw.ts` and `project-settings.pw.ts` cover Fleet, Inbox, deep-link reload, browser Back, the roadmap pages, project settings, and the handoff's inherited sources and validation. `web/bridge/static/index.html` references the rebuilt `assets/index-CrFh852g.js` and `assets/index-CHqPrMo8.css`, so the embedded build is what was exercised. The fixture bridge runs with `--egress off`, a provider pointed at `http://127.0.0.1:9` that is never called, and no credential, forge or live agent.

### Known defects left in place

The axe scans surface two pre-existing palette defects in `web/ui/src/app.css` and `web/ui/src/lib/ui/Button.svelte` (an unlayered `button { color: inherit }` defeating `text-bg`, and a light variant that redefines only neutrals). Neither file is a redesign file. Every reported contrast pair is pinned by its exact values, so any new violation still fails the scan and the pinned list must shrink when the palette is fixed.

### Remaining roadmap capabilities

Planning workspace, voice capture, the extensible workflow editor, host management, richer connection and environment flows, merge, and cross-device continuity. Each is reachable in the shell as a roadmap page, which a real-bridge browser case asserts is planned and read-only; that is not an implementation.

### Evidence

Screenshots are in the gitignored `.docs-archive/studio-uiux/`: `fleet-desktop.png`, `fleet-tablet.png`, `fleet-phone.png`, `factory-desktop.png`, `factory-phone.png`, `handoff-desktop.png`, `handoff-phone.png`, `draft-phone.png`, `execution-desktop.png`, `execution-phone.png`, `decision-phone.png`, `recovery-desktop.png`, `ready-review-desktop.png`, `ready-review-phone.png`. Per-slice implementation records, including commits and test counts at each slice boundary, are in §16 of `docs/studio-fleet-factory/studio-uiux-alignment-design.md`.
