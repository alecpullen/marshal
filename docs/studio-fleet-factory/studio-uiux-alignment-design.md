# Marshal Studio UI/UX alignment — fleet manager and software factory

Date: 2026-11-15

Status: approved 2026-11-15, including written-specification review and implementation-planning handoff

## 1. Context

Marshal Studio already has two overlapping operational homes:

- `Home` (`#`) is an inbox with needs-you, ready, running, disk, and activity content.
- `Fleet` (`#fleet`) groups independent agent cards by project.

The factory is a functional bootstrap implementation, but its navigation and presentation do not yet express the approved product hierarchy. `#factory` is a global list, `#factory/new` is a dense technical form, and `#factory/<id>` combines draft approval, decisions, recovery, CI, narration, previews, workers, and history in one page. Raw route JSON and technical configuration remain visible. Factory navigation is not a first-class part of the app shell.

The approved HIFI explorations in this directory describe a different information architecture: Fleet is the shared home; project factories and independent agents coexist; attention and readiness lead; planning, handoff, execution, recovery, and review have distinct presentations. The relevant anchors are:

- `fleet-factory.html` and `screenshots/fleet-factory-comparison.png`;
- `handoff.html`, `execution.html`, `decision.html`, and `recovery.html`;
- `change-review-v2.html` and `ready-review.html`;
- `work-item.html`, `voice-planning.html`, `workflow-editor.html`, `hosts.html`, and `start-agent.html` for later capabilities.

The HIFI record approves layout and interaction direction, not its illustrative data. It explicitly leaves exact colors and typography open and rejects the purple workflow strip in the factory dashboard. The existing Web Studio design separately selects Warm Sunset, dark-first, with Geist typography.

The current factory list endpoint, `GET /api/factory/items`, returns complete deep-copied `FactoryItem` records. Those records include project, state, stage, decisions, drafts, approvals, workers, PRs, rounds, CI, effects, and history. The existing fleet SSE and factory HTTP contracts therefore support the first three redesign slices without new bridge endpoints.

## 2. Goals and success criteria

The redesign must make Studio trustworthy and useful before making every HIFI surface functional.

1. Fleet is the default route and combines the operational value of the current Home and Fleet pages.
2. A user can identify the most urgent real work without visiting separate Home and Fleet pages.
3. Any current factory item or independent agent is reachable from Fleet in at most two navigation actions.
4. Factory-linked agents never also appear as independent agents.
5. Every visible operational action maps to a real contract. Every unavailable HIFI capability is unmistakably marked **Planned**.
6. Before saving or approving a handoff, the user can identify its scope, repository, environment, effective model profiles, CI expectations, repair allowance, authority setting, and approval consequence.
7. A work-item user can answer “what is happening?”, “what needs me?”, “what evidence supports readiness?”, and “how do I steer or recover?” without reading raw JSON or scanning the entire event history.
8. Phone, keyboard, reduced-motion, and light-theme users retain the same core capabilities with no horizontal page overflow.
9. Loading, stale, unavailable, accepted, in-progress, and completed states remain distinguishable.

No analytics or user-tracking system is introduced to measure these goals; they are acceptance criteria for scripted usability and browser review.

## 3. Approved approach and trade-offs

### Chosen approach: Fleet-first vertical slices

Rebuild the navigation and Fleet foundation first, then carry the same patterns through handoff and work-item execution/review. Use existing contracts for working features and expose future capabilities through honest roadmap pages.

### Alternatives rejected

- **Targeted reskin:** lower cost, but leaves the split Home/Fleet experience, weak factory navigation, dense handoff, and monolithic work item intact.
- **Full HIFI reconstruction:** broader immediate visual coverage, but creates a large parallel UI and too much simulated or roadmap functionality before the core working journeys improve.

### Other decisions and trade-offs

- Retain Warm Sunset and Geist rather than adopting the HIFI light purple palette. HIFI supplies hierarchy, layout, responsive behavior, and interaction patterns.
- Show roadmap destinations in the main UI, as requested, but keep their content separate from live operational data.
- Use one shell-level factory store rather than independent polling in Fleet, dashboards, and item views. This adds store complexity but prevents duplicated requests and inconsistent snapshots.
- Deliver independently shippable slices rather than a long-lived parallel Studio or feature flag.
- Keep Svelte 5, Tailwind, the Go bridge, and existing component primitives. No framework, runtime component-library, or icon-library migration is justified.
- Use explicit mocked API fixtures for expensive visual states, while retaining real-bridge browser tests for actual contracts. Mocked presentation tests are never evidence of live factory execution.

## 4. Product structure and navigation

### Fleet home

Fleet contains, in priority order:

1. **Needs you:** factory decisions and drafts, agent approvals/questions, intake submissions, and network decisions. Each entry names its source, waiting state, consequence, and direct action.
2. **Ready for review:** ready factory items plus existing independent-agent and automation outcomes.
3. **Project factories:** project-grouped summaries of real factory work.
4. **Independent agents:** agents not referenced by any factory worker.
5. **Fleet resources:** existing operational modules and clearly labelled roadmap resources.

Fleet is the prioritized operational summary. A separate Inbox retains the full needs-you, ready, running, and earlier queues. The current Home behavior moves to Inbox rather than being duplicated as a second default home.

Factory decisions and agent pending requests are not inferred to be equivalent. If both exist, both remain visible with distinct source labels.

### Projects and factories

A project is the bridge between independent and factory work. No new factory entity is invented for this redesign: current backend behavior already allows factory items within a project.

- Projects with factory items show current, needs-you, ready, and linked-agent summaries.
- Projects without factory items remain visible and offer working New work item and Start agent paths.
- “Current” and “linked agent” counts use current factory items only. A linked-agent count is the number of unique nonempty worker agent IDs, not a claim that all are running.
- Independent-agent exclusion considers worker IDs across all factory items, including historical items, so an old factory session is not reclassified as independent.

### Desktop navigation

One HIFI-style labelled sidebar replaces the competing Rail and agent Sidebar structures. It contains:

- Fleet and Inbox;
- project/factory links, with contextual current work-item links for the active project;
- existing Projects, Live, Runs, Workspaces, Watches, Library, and Usage destinations;
- Settings;
- roadmap Hosts, Environments, and Connections entries with Planned badges.

The sidebar is collapsible to an icon rail. The existing `marshal.ui.sidebar` local-storage preference (`"1"` open, `"0"` collapsed) remains the desktop record.

### Phone and tablet navigation

- Phone bottom navigation contains Fleet, Inbox, Library, and Settings.
- An accessible all-pages menu contains every remaining working and roadmap destination.
- Factory and project navigation lives inside Fleet rather than adding another primary bottom-nav destination.
- Tablet uses a collapsed rail by default and an accessible navigation drawer.
- Navigation drawers and dialogs trap and restore focus; the persistent bottom nav respects safe-area insets.

## 5. Route contract

| Route | Behavior |
|---|---|
| `#` | Fleet home |
| `#fleet` | Compatibility redirect to `#` |
| `#inbox` | Full operational inbox; current Home content moves here |
| `#factory` | Cross-project factory work list, default filter `current` |
| `#factory?filter=current\|needs-you\|ready\|history` | Filtered global factory list |
| `#projects/<root>` | Existing project overview/settings, with clearer factory navigation |
| `#projects/<root>/factory` | Project-scoped factory dashboard, default filter `current` |
| `#projects/<root>/factory?filter=…` | Scoped factory filter |
| `#factory/new?project=<root>` | Redesigned handoff creation |
| `#new?project=<root>` | Start agent with the named project preselected |
| `#factory/<id>` | Work item with an initial state-appropriate view |
| `#factory/<id>?view=…&agent=…` | Selected work-item view and optional agent drill-in |
| `#roadmap/<area>` | Planned-capability page |

All other existing deep links, including `#pending`, session, run, review, workspace, settings, and automation routes, remain valid unless an existing compatibility redirect already applies.

For `#new?project=<root>`, a known available project is preselected. A known unavailable project remains visible with its unavailable explanation and cannot be started. An unknown root leaves the normal project picker unselected and shows a nonblocking “Project is no longer available” notice. Bare `#new` retains its current remembered-default behavior.

### Factory filters

- `current`: state is neither `closed` nor `stopped`.
- `needs-you`: the item has a decision or its state is `draft`.
- `ready`: state is `ready`.
- `history`: state is `closed` or `stopped`.
- An unknown future state remains visible in `current` and renders its raw state as a safe label rather than disappearing.

An absent or unknown filter falls back to `current`. The filter is route state so Back, reload, and shared links restore it.

### Work-item views

Allowed `view` values are:

- `approval`: draft review and Approve & start;
- `activity`: compact progress, decisions, combined factory activity, workers, and steering;
- `review`: CI evidence, related findings/repair state, engineering narration, selected code, and previews;
- `prs`: pull-request links and revision/state details;
- `documents`: the current approved or draft plan/specification and approval revisions.

With no explicit view, the initial item load chooses `approval` for a draft, `review` for a ready item, and `activity` otherwise. This default is chosen once on initial entry. Polling never changes the selected view when the item state changes.

An explicit `approval` view on a non-draft falls back to `activity`. Unknown views are dropped and use the same initial-state rule. `agent` selects an activity drill-in; an unknown or unavailable agent produces an inline unavailable state with Back to activity, not a blank panel.

Work-item cards link directly to the appropriate view: drafts to approval, decisions to the pinned decision in activity, and ready items to review.

Open full session uses `#chat/<agent>?from=<encoded-factory-hash>`. `parseChatRoute` retains `from` only when decoding it yields a valid factory item route; invalid values are dropped. The Chat Back action navigates to a valid `from` route and otherwise retains its existing Fleet fallback. `from` may coexist with the existing node/dock/tab parameters.

### Roadmap areas

The typed roadmap registry supports exactly:

- `planning`;
- `voice`;
- `workflow`;
- `hosts`;
- `environments`;
- `connections`.

The registry has one stable entry shape:

```ts
type RoadmapArea = 'planning' | 'voice' | 'workflow' | 'hosts' | 'environments' | 'connections'
interface RoadmapAction { label: string; hash: string }
interface RoadmapEntry {
  area: RoadmapArea
  title: string
  summary: string
  current: string
  planned: string[]
  alternatives: RoadmapAction[]
  designHref?: string
}
```

`current` states what is possible today, including “no in-app host control” where that is the honest answer. `alternatives` contains only valid in-app hash routes and may be empty. `designHref`, when present, is an absolute HTTPS link to the relevant public Marshal design document and opens with `noopener noreferrer`.

Unknown roadmap areas render Not Found with a Fleet link. When a real capability ships, its roadmap route redirects to the production route.

## 6. Components and screen behavior

### Application shell and shared primitives

New or extracted shared components own the repeated patterns:

- `StudioNav` and `MobileNav`;
- `PageHeader` for breadcrumbs, title, real status, metadata, and one primary action;
- `AttentionCard`, `ProjectFactoryCard`, `WorkItemCard`, and `IndependentAgentList`;
- shared filter, empty, error, stale, and Not Found states;
- `EffectiveConfigSummary` and a shared model-profile picker;
- `RoadmapPage` backed by the typed registry.

`App.svelte` remains the composition root for routes and shell-level stores. Existing `Rail.svelte` and `Sidebar.svelte` are retired only after their working destinations and preferences are represented in the new shell.

### Fleet projection

A pure, tested client view model combines fleet agents/projects/inbox, factory items, network decisions, and existing automation summaries. It does not mutate server state or infer unsupported states.

Existing unavailable-project, untrusted-project, orphan-worktree, and non-isolated-collision warnings remain visible. Existing agent approval/question modals and pending actions are reused.

Existing Home automation-summary loading is extracted for reuse by Fleet and Inbox. Only one of those views is mounted at a time; per-project automation failures are partial errors and do not block factory or agent content.

### Project factory dashboard

`Factory.svelte` supports global and project-scoped projections rather than maintaining two unrelated list implementations.

The project-scoped dashboard contains:

- project/repository identity and real status;
- Needs your decision;
- Current/Needs you/Ready/History filters;
- work-item cards with title, repository, backend state/stage, direct action, PR/check summary, and linked-agent count;
- a compact factory-defaults summary with a working project-settings link;
- an expandable linked-agents section listing unique workers from current items, with real chat links and parked/status text when available;
- New work item as the primary working action.

The rejected purple workflow strip is not implemented. Import handoff and Talk to planner are not presented as working actions. Workflow editor is a Planned link beside the real defaults/settings path.

### Handoff and draft approval

`FactoryNew.svelte` retains its existing creation/default-resolution contract but is reorganized into:

1. **Scope:** title, plan, optional specification.
2. **Repository and environment:** repository, target branch, native/workspace choice.
3. **Roles and review:** implementer/reviewer profiles, reviewer instructions, expected-check source, and no-required-CI acknowledgement.
4. **Authority and limits:** implementer auto permission and total rounds; CI poll/wait remain under Advanced.
5. **Approval summary:** effective values and their inheritance sources, plus the Save draft consequence.

Desktop uses two columns with the summary visible beside the form. Phone uses compact expandable sections; Scope and final consequences remain visible, and Advanced starts collapsed.

`EffectiveConfigSummary` is shared by creation and the draft-approval view. It replaces raw `JSON.stringify` route output with readable profile names and source labels. Empty override fields continue to mean “follow”; fallbacks are not materialized into override fields.

The existing two-stage flow remains: Save draft, review the saved resolved snapshot, then Approve & start. Saving a draft does not start work. Approval authorizes execution under the displayed configuration.

`App.svelte` owns one active route-leave guard, registered by the mounted view. StudioNav, MobileNav, breadcrumbs, cards, and other `onNavigate` callers all pass through it. FactoryNew and the existing Project settings editor register their dirty-state guards with this shell mechanism rather than protecting only their own links.

Cancelling an in-app navigation keeps the current hash and all input. If browser Back changes the hash before the guard runs, cancelling restores the prior hash with `history.replaceState` and leaves the form mounted. Reload/close continues to use `beforeunload`. Successfully saving and opening the resulting draft clears FactoryNew's dirty state. Durable cross-device draft synchronization is not added.

### Work item

`FactoryItem.svelte` is decomposed into focused components for:

- compact header/progress and primary action;
- draft approval;
- decisions and answers;
- retry and named-worker recovery;
- combined activity;
- CI evidence and review findings;
- engineering narration and selected code;
- pull requests and documents;
- preview/test environments;
- worker list and agent conversation.

The active desktop view pairs factory activity with an agent conversation. An explicit `agent` route parameter takes precedence. With no agent parameter, initial desktop entry selects the last record in `item.workers`; if there are no workers, activity uses the full width. Parked status is displayed but does not prevent opening the worker's retained conversation. Later worker updates never replace the user's selection. Phone opens activity first and uses a focused agent drill-in with explicit Back to activity.

Combined activity uses factory history and available effect/round/worker relationships. Events without a worker mapping remain factory-level. This slice does not merge multiple session SSE streams into a fabricated unified raw-tool transcript; individual agent detail remains in the drill-in.

Activity is ordered by ascending factory event `sequence`, preserving server order for equal values, and initially positioned at the latest event. It uses concrete stages, rounds, milestones, and events, never unsupported percentage estimates. Meaningful events lead; available raw details remain expandable. A user who scrolls away from the latest event gets a New activity cue rather than forced scrolling.

Changing only the work-item `view` or `agent` query keeps the same keyed FactoryItem instance mounted. The hidden activity pane retains its scroll position and disclosure state during an internal agent drill-in. Open full session is a separate route: its Back control restores the originating factory view and agent through a validated `from` route parameter, but transient scroll and disclosure state are not persisted across that full-session unmount.

The ready/review presentation follows the approved CI-first direction:

1. expanded failing CI and related actionable findings/repair status;
2. compact passing-check summary;
3. preview/test-environment status and revision;
4. engineering narration with optional selected code;
5. actual forge links.

No in-Studio merge action is shown until a merge contract exists. Open in forge is the working path.

### Agent conversation extraction

An embeddable agent conversation is extracted from the existing Chat implementation for use by both full Chat and the work-item drill-in. It owns the selected session's existing transcript/composer/pending-request behavior and closes that session connection when unmounted.

The embedded panel provides Open full session for the existing dock, terminal, files, tasks, preview, and review functionality. It does not create a second agent, duplicate factory control, or reimplement session policy in the factory view.

### Recovery capabilities

Recovery uses real profile pickers populated from the existing models API, preserving unavailable current values. It retains:

- reason and additional-round allowance;
- optional implementer/reviewer route changes;
- optional replacement plan, specification, and reviewer instructions;
- expected-version and request-ID handling;
- named-worker attestation for recovery.

Copy continues to state that a route-only retry starts a fresh round and does not reuse the interrupted review. The unimplemented cross-provider “continue the old conversation” choice is not offered.

Factory decisions without structured options use an answer field. Option buttons are shown only where an existing agent question contract supplies real options.

## 7. State ownership and data flow

### Fleet state

The existing shell-level fleet store and its single SSE connection remain authoritative for agents, projects, inbox, telemetry, network decisions, and automation ticks.

### Factory state

A new shell-level factory store has this public shape:

```ts
interface FactoryState {
  items: FactoryItem[]
  loading: boolean
  refreshing: boolean
  error: string | null
  lastUpdated: number | null
}
interface FactoryStore {
  state: Readable<FactoryState>
  start(): void
  stop(): void
  refresh(): Promise<void>
  getItem(id: string): Promise<FactoryItem>
  upsert(item: FactoryItem): void
  itemById(id: string): FactoryItem | undefined
}
```

`loading` describes the initial authoritative list load; `refreshing` describes a background read. `lastUpdated` is the local epoch-millisecond time of the last applied list snapshot. `itemById` is a synchronous current-state lookup. `getItem` returns a current cached item when present and otherwise fetches the deep link; its errors propagate to the item view.

One visibility-aware poller serves Fleet, global/scoped factory dashboards, and work-item views.

- Initial and refresh reads use `GET /api/factory/items`.
- If the initial list does not contain a deep-linked ID, the store issues one `GET /api/factory/items/<id>`. A 404 produces the item Not Found state; a transport failure produces Retry and does not become Not Found.
- Polling uses the existing active/inactive cadence: two seconds while any item is active, otherwise 15 seconds, with bounded error backoff.
- Active retains the current meaning: state is not one of `draft`, `ready`, `stopped`, `closed`, or `needs_input`.
- Hidden-document polling is suspended; becoming visible schedules an immediate refresh.
- Create and command responses upsert immediately. A command that makes an item active schedules reconciliation.
- Each list or single-item read records the store's mutation revision. If a create/command upsert occurs while that read is in flight, the older response is discarded. A discarded list response schedules another refresh; a discarded single-item read returns the current cached item. Neither can overwrite the newer command response.
- `lastUpdated` changes only after an applied authoritative list snapshot.
- Work-item commands use the latest item `version` and current decision ID.

Preview-environment loading remains item-local and revision-aware. It is not repeated on every factory poll. Existing URL validation and display bounds remain unchanged.

A future factory SSE stream may replace polling internals without changing view projections.

### Roadmap state

Roadmap pages use static typed content and make no speculative API calls. They contain no fictional live agents, checks, capacity, timestamps, or successful mutations.

## 8. Loading, errors, concurrency, and recovery

### Partial loading and freshness

- Each data domain fails independently. Factory failure does not hide agents/projects; automation or preview failure does not blank a work item.
- Initial loads use stable placeholders. Background refresh retains current content and shows a small refreshing indicator.
- Failed refresh retains last-known content with a stale warning and Retry. A hidden-tab pause alone is not an error or stale warning.
- Empty states require a successful authoritative load. A failed request never renders “Nothing needs you.”
- Unknown routes and missing items render useful Not Found states with working return links.

### Commands and uncertain outcomes

HTTP success is not presented as proof that asynchronous work completed. Copy distinguishes:

- draft saved;
- approval accepted;
- stop requested versus worker stopped;
- retry authorized versus round completed;
- evidence refreshed versus readiness established.

Buttons show in-flight state and prevent duplicate submission while leaving content readable.

A byte-identical network retry reuses the original request ID; editing the payload creates a new identity. A transport failure with an uncertain outcome offers a safe same-request retry. An authoritative HTTP rejection is not described as uncertain.

Action errors remain beside the action and use accessible alert/status announcements. Toasts are secondary confirmation, never the only error record.

### Concurrent changes

- Factory `409`: refresh the item, clear the obsolete pending command, preserve replacement/reason input, and require review before retrying.
- Project-settings `412`: retain edits, block stale overwrite, and offer explicit Reload.
- Existing pending-request `410` handling remains: the request was resolved elsewhere and the queue refreshes.
- Polling never changes selected tabs, expanded sections, scroll position, or draft input.
- New activity while reading older events produces a quiet cue rather than forced scrolling.

### Validation and consequential actions

- Validation appears beside the field and in a submit-time summary; focus moves to the first invalid field.
- Failed saves and cancelled navigation preserve all input.
- Disabled primary actions have a nearby explanation.
- Stop, recovery attestation, additional-round grants, and approval state their consequences before submission.
- Named-worker recovery retains exact worker-ID matching and explicit attestation. An unreachable worker is never represented as confirmed stopped.

### Evidence and readiness

- CI remains revision-bound. Missing, incomplete, stale-head, or unavailable evidence is not success.
- Failed or unavailable review is not a clean review.
- Passing checks collapse into a summary; failures and actionable findings default to expanded.
- Preview links retain provider status and revision. A head mismatch is visibly a warning.
- A ready item does not imply the whole feature is complete or merged.
- Stop, pause, cancel, retry, recover, delivered, acknowledged, and completed are not treated as synonyms.

## 9. Visual, responsive, and accessibility rules

### Visual system

`app.css` remains the palette authority: Warm Sunset, dark by default, with the existing light variant and semantic status colors. Geist and Geist Mono remain the typography. Existing glyphs remain the icon vocabulary.

Shared tokens may be extended for focus rings, subtle surfaces, and status backgrounds. Page-local hex palettes are not allowed.

The shared visual scale is:

- page titles: 24 px phone and 28 px desktop;
- section headings: 18 px;
- card headings: 14–16 px;
- body text: 14 px;
- metadata: 12 px minimum;
- phone page padding: 16 px;
- desktop page padding: 24–32 px;
- panel radius: 12 px;
- control radius: 8 px.

Primary actions use coral. Existing attention, success, failure, planner, implementer, and reviewer semantics remain intact. Motion is limited to live indicators and small state transitions and respects reduced motion.

Mockup phone frames, OS status bars, fictional account chrome, sample counts, and illustrative model assignments are not production requirements.

### Responsive breakpoints

- **Below 768 px:** top app bar, bottom primary nav, one-column content, focused drill-ins, safe-area-aware composers/actions.
- **768–1199 px:** collapsed rail by default with accessible drawer and single-column content; work-item and handoff split panes wait for the desktop breakpoint.
- **1200 px and above:** labelled sidebar by default, collapsible to rail; supported side-by-side activity/conversation and handoff/summary layouts.

### Accessibility

- Full keyboard operation, visible focus, logical focus order, and focus restoration.
- Semantic headings, landmarks, tabs, disclosures, alerts/status, and labelled controls.
- Minimum 44 px touch targets for primary controls and no drag-only interactions.
- No horizontal document overflow; code, logs, paths, and diffs scroll within their containers.
- Readable at 200% browser zoom.
- Reduced-motion and dark/light contrast review.
- Automated accessibility scans on Fleet, factory dashboard, handoff, decision, and ready-review fixtures, plus keyboard and screen-reader spot checks.

Add `@axe-core/playwright` as a development-only dependency for those scans; it adds no production runtime dependency.

## 10. Testing and acceptance evidence

### Unit and component tests

Use Vitest and Testing Library for:

- route parsing/formatting, redirects, filters, work-item views, and unknown routes;
- Fleet grouping/counts, worker exclusion, and projects without factory items;
- factory filters, state/action mapping, and unknown states;
- factory-store cadence, visibility, backoff, mutation revision, and stale-response disposal;
- navigation active states, preferences, mobile menu, and roadmap registry;
- handoff inheritance, overrides, validation, dirty state, request identity, and effective summaries;
- decisions, retry/recovery, CI grouping, narration, preview revisions, and agent return;
- long content, missing optional data, multiple PRs/workers, empty history, and malformed URLs.

Existing API safety/concurrency tests remain unchanged unless an intentional behavior change requires a reviewed update.

### Browser fixtures

Extend Playwright with two explicit fixture styles:

1. **Real bridge:** navigation, settings, configuration reads, handoff form/default resolution, and validation. The current bridge requires a GitHub-backed repository and credential before saving a draft, so the credential-free fixture does not create or approve factory items.
2. **Mocked API:** saved drafts, active, stalled, ready, disconnected, and otherwise expensive presentation states. Test names and setup identify these as presentation fixtures; a mocked save is never evidence that the real create/approve contract succeeded.

Cover 390 px phone, 768 px tablet, and 1440 px desktop. Cover dark and light themes on primary screens.

Core journeys are:

- Fleet triage → project factory → decision/review → same originating context;
- Fleet → independent agent → return, without duplicate factory classification;
- nav collapse/menu, deep-link reload, filters, and browser Back;
- handoff defaults → overrides → validation → saved draft → approval summary;
- active item → activity → agent drill-in → restored activity;
- answer, route-only retry, stop requested, and named-worker recovery;
- ready item → CI → narration → previews → forge;
- factory failure, uncertain command, stale refresh, `409`/`412`/`410`, and missing item;
- every roadmap page, including Planned labeling, current alternatives, no fake data, and no speculative requests.

### Visual and performance evidence

Capture deterministic desktop/phone screenshots for Fleet, project factory, handoff, active execution, decision/recovery, and ready review. Compare hierarchy, spacing, progressive disclosure, and interactions with the approved HIFI anchors, not their rejected palette or sample data.

Local comparison artifacts belong in `.docs-archive/studio-uiux/` unless deliberately promoted to public documentation. A broad brittle screenshot-golden suite is not required.

Browser tests assert one fleet SSE connection and one factory poller across navigation. Route changes must not multiply polling or reset drafts. The built SPA served by the real bridge is inspected, not only the Vite development server.

### Verification gates

Each slice runs its narrow tests, `npm run check`, relevant browser journeys, and `npm run build`.

The overall completion gate is:

```bash
cd web/ui
npm test
npm run check
npm run build
cd ../..

# Builds the SPA and bridge, starts an isolated real fixture bridge,
# and runs the Playwright desktop/tablet/phone projects.
scripts/e2e/run_browser_checks.sh

go test ./web/bridge/...
CGO_ENABLED=1 go build ./cmd/webbridge
scripts/test.sh quick
```

The browser configuration is extended to include the tablet project and any additional real/mock fixture files. Final review includes a real-bridge smoke test and the documented HIFI comparison.

## 11. Delivery slices and file ownership

### Slice 1 — Fleet and navigation

Primary areas:

- `web/ui/src/App.svelte`, `app.css`, and `lib/routes.ts`;
- `NewAgent.svelte` for the project-prefilled route;
- the shell-level route-leave guard used by dirty forms;
- new shell/navigation, Fleet, Inbox, roadmap, and shared presentation components;
- new Fleet projection and shell-level factory store;
- `Home.svelte`/`Dashboard.svelte` migration and eventual retirement of duplicate Rail/Sidebar structures;
- route, store, projection, component, and browser tests;
- regenerated embedded assets.

Deliver the shell, routes, Fleet, project/factory grouping, independent agents, roadmap pages, and responsive navigation.

### Slice 2 — Project factory and handoff

Primary areas:

- `Factory.svelte`, `FactoryNew.svelte`, `Project.svelte`;
- shared work-item cards, filters, defaults summary, profile picker, and effective-config summary;
- handoff/draft component tests and real-bridge browser coverage;
- regenerated embedded assets.

Preserve existing creation, inheritance, validation, request-ID, and settings contracts.

### Slice 3 — Execution, decisions, and review

Primary areas:

- `FactoryItem.svelte` and new focused factory components;
- Chat conversation extraction and existing Chat tests;
- work-item route state, activity/drill-in, recovery, CI/review, documents, PRs, and previews;
- mocked presentation fixtures plus real command-contract unit/component tests;
- regenerated embedded assets.

Preserve existing agent ownership, command, evidence, and recovery semantics.

### Later slices

Planning workspace, voice capture, extensible workflow editor, host management, richer environment/connection flows, merge, and cross-device continuity replace roadmap entries only after their own behavioral designs and contracts are approved.

Each slice includes browser evidence and an implementation record. No long-lived UI feature flag is required.

## 12. Documentation maintenance

This document is the durable design for the alignment work. During implementation:

- link it from `docs/studio-fleet-factory/README.md`;
- reconcile the older `docs/web-studio/design.md` Home/Fleet information architecture with this newer Fleet-first direction;
- keep original HIFI HTML and screenshots unchanged as historical anchors;
- record adopted versus roadmap HIFI details;
- add an implementation record per slice rather than silently rewriting approved decisions.

Repository convention is to commit public feature documentation with the work it describes. This specification is not committed during the brainstorming step itself.

## 13. Explicit non-goals

- Replacing Warm Sunset or Geist with the HIFI light purple styling.
- Building the future workflow engine, coordinator role, voice system, host scheduler, or merge backend as part of this UI redesign.
- Fake mutations, fabricated live telemetry, fictional operational success, or unsupported progress percentages.
- Implementing the rejected purple factory workflow strip.
- Changing the TUI or weakening ACP and `web/` standard-library boundaries.
- Rebuilding every existing Studio settings/operations page before improving the core fleet/factory journeys.
- Durable cross-device handoff drafts, interactive notification bots, multi-user administration, cross-project factories, and multi-repository work items.
- A new front-end framework, production component-library migration, icon library, long-lived parallel Studio, or permanent UI feature flag.

## 14. Assumptions

No unapproved product assumptions remain. The design relies on the verified current contract that factory list responses contain complete item records and that existing project/fleet/factory APIs remain available. If implementation discovers a missing contract, the affected behavior is revised explicitly or remains Planned; it is not simulated as working.

## 15. Open questions

None. The task-by-task implementation plan is `plans/2026-11-15-studio-uiux-alignment-plan.md`. The user approved the plan on 2026-11-15 and chose to execute it later or manually. Slice 1 is implemented and browser-verified; its record is §16.

## 16. Implementation records

### Slice 1 — Fleet and navigation

Delivered on branch `feat/studio-uiux-alignment` in commit order: routes (`68854810`), Fleet and factory projections (`0208bb5a`), the shared factory store (`91cc34a4`), roadmap and page primitives (`16a4c000`), responsive navigation (`48f60a46`), automation summaries (`995a996f`), the Fleet home (`f349473d`), shared factory state in the shell (`cde6f5c7`), the application shell (`7d4c7c95`), the route guard (`7c994227`), and the browser verification this record covers.

Browser verification is `scripts/e2e/run_browser_checks.sh`, which builds the SPA and `webbridge`, starts an isolated fixture bridge, and runs the Playwright suite at desktop (1440×900), tablet (768×1024) and phone (390×844).

**Result: 63 cases, 54 pass, 9 fail.** The 9 are one known baseline, not a slice defect: the three `project-settings.pw.ts` cases below fail at *each* of the three widths. At the base commit `f42a7c59` the same three cases failed at the two widths that existed then (6 failures of 12 cases), for the same reason — the fixture bridge serves no model profiles, so the "Model profile" select offers no `gamma` option and `selectOption` times out after 30s. Adding the tablet project multiplies that same baseline by three widths rather than adding a new failure. The cases are `role overrides follow the agent default independently and reset`, `a stale save warns, keeps the edit, and offers reload`, and `unsaved edits are confirmed before in-app navigation`.

All 45 new cases pass at all three widths: 6 real-bridge cases in `e2e/studio-shell.pw.ts` (`emptyFleetHasWorkingStartActions`, `allRoadmapPagesArePlannedAndReadOnly`, `projectPrefillAndUnknownProject`, `shellDeepLinksAndBrowserBack`, `routeGuardCoversShellNavigationAndBrowserBack`, `unknownRouteHasFleetReturn`) and 9 cases in `e2e/studio-fleet.pw.ts` (the six mocked-presentation cases plus the axe, keyboard and reduced-motion cases), each run in the desktop, tablet and phone projects. Real-bridge cases create, approve, retry and recover nothing: this bridge has no GitHub credential and `factoryAPI.preflight` requires one, so no live factory execution is claimed here. The mocked-presentation cases answer the fleet, inbox and factory reads from `e2e/fixtures.ts`; they are evidence about rendering only.

Accessibility. `fleetFitsAndScansCleanInBothThemes` runs axe on Fleet in dark and light at every width and asserts no horizontal overflow. It found two pre-existing palette defects that this slice may not fix, because their fixes live in `web/ui/src/app.css` and `web/ui/src/lib/ui/Button.svelte`, outside this task's files:

1. `Button`'s filled variants request `text-bg`, but app.css sets an unlayered `button { color: inherit }` so form controls inherit the page colour. Unlayered rules outrank Tailwind's utility layer, so `text-bg` is lost and an accent-filled button draws `--color-fg` on `--color-accent` (1.95:1) instead of `--color-bg`.
2. The light variant redefines only the neutrals, so the signal colours keep their dark-theme values on a light background: accent and attention headings at 2.23:1 and 1.71:1, a muted `Planned` badge on `--color-line` at 3.78:1, and a tinted attention badge at 1.44:1.

Both are pinned by the exact contrast pair axe reports, so any violation outside those pairs — including a new rule — still fails the scan, and the pinned list must shrink when the palette is fixed. Keyboard-only navigation and `prefers-reduced-motion` are covered by their own cases.

Screenshots are the deterministic presentation fixture, taken through Playwright's test output and copied to the gitignored `.docs-archive/studio-uiux/`: `fleet-desktop.png` (1440×900), `fleet-tablet.png` (768×1024), `fleet-phone.png` (390×844). They are hierarchy and spacing evidence, not a live factory.

Unit and type baselines are unchanged by this slice: `npm test` is 1070 passing, and `npm run check` reports the same 5 errors and 1 warning in the same 6 files as before it. The regenerated embedded SPA is committed as `web/bridge/static/index.html` with `assets/index-BSG0H76E.js` and `assets/index-DRxGVYUe.css`.

### Slice 2 — Project factory and handoff

Delivered on branch `feat/studio-uiux-alignment` in commit order: the project factory dashboard (`804f08d0`), the reorganized handoff around effective configuration (`8a1ef3af`), the extracted readable draft approval (`0787f18d`), and the browser verification this record covers.

Browser verification is `scripts/e2e/run_browser_checks.sh`, unchanged from slice 1: it builds the SPA and `webbridge`, starts an isolated fixture bridge, and runs the Playwright suite at desktop (1440×900), tablet (768×1024) and phone (390×844). Slice 2 adds `web/ui/e2e/factory-handoff.pw.ts` (12 cases) and extends `web/ui/e2e/fixtures.ts` with mocked project settings, model profiles, create and command interception, and their payload recorders.

**Result: 99 cases, 87 pass, 12 fail.** Nine are the known baseline, unchanged from slice 1: `role overrides follow the agent default independently and reset`, `a stale save warns, keeps the edit, and offers reload` and `unsaved edits are confirmed before in-app navigation`, each failing at all three widths because the fixture bridge serves no model profiles, so the "Model profile" select offers no `gamma` option. The other three are one case this slice surfaced, also at all three widths: `factory handoff › shows inherited sources and keeps its input when opening settings` in `project-settings.pw.ts` asserts the handoff form's old `implementer-effective`, `reviewer-effective` and `target-branch-effective` hooks, which task 13 replaced with the `config-<slug>` rows of the shared effective-configuration summary. The case is stale, not the UI — the shipped handoff shows the same values under the new hooks — and `project-settings.pw.ts` is outside this task's files, so this task did not change it. The fix belongs to whichever task owns that file (update the three assertions to `config-implementer`, `config-reviewer` and `config-target-branch`, or restore the old hooks on the summary); the slice-2 browser result stays at 12 failures until that decision is taken.

All 36 slice-2 cases pass at all three widths — 12 cases in each of the desktop, tablet and phone projects:

- real bridge (4): `projectFactoryLinksPreserveScope` (breadcrumbs, scoped filters that keep the project, header and empty-state actions, the unscoped list), `handoffLoadsRealDefaultsAndValidation` (repository branch and built-in limits inherited from the answered project, field-linked errors with the first invalid control focused, and no create posted), `handoffRouteGuardRetainsInput` (the shell's guard declines and keeps the handoff, then accepts), and `handoffHasNoOverflow` (no sideways scrolling at any width, phone disclosures, desktop two columns);
- mocked presentation (4): `projectFactoryFiltersAndDirectActions` (scoped counts and defaults, decision section above the work list, every filter as a route, each card's direct action), `handoffSummaryTracksOverrides` (sources, live overrides, and a payload that omits every followed field), `savedDraftShowsResolvedServerConfiguration` (the preview renders the server's resolved rows, including a host and limits the handoff never sent), and `draftApprovalDoesNotStartBeforeClick` (the consequence copy, zero commands before the click, then exactly one `approve`);
- accessibility and evidence (4): axe scans of the project factory with its defaults card, the handoff and the saved draft's approval in dark and light, plus keyboard-only disclosure and Tab-order coverage.

Real draft creation and approval were **not** run: the isolated bridge has no GitHub credential and `factoryAPI.preflight` requires one. The saved draft, its approval, and the create and command payload assertions are the mocked presentation fixture. They prove what the SPA sends and renders; they are not evidence that the bridge accepted a create or an approval.

Accessibility. The three scans are scoped to `main` and pin the palette family slice 1 found in `web/ui/src/app.css` and `web/ui/src/lib/ui/Button.svelte`, by the exact contrast pair axe measured rather than by node: the dark `--color-fg` on a filled accent button, and in the light theme an accent link and attention text on both the page background and a card's surface, a muted badge on `--color-line`, and attention on its own tint. The factory surface drew the light accent-on-surface and muted-on-line pairs; the handoff drew attention on a card surface; the saved draft drew the dark filled-button pair plus the light accent-on-background and muted-on-line pairs. Any other rule, or any pair outside the family, fails the scan. Keyboard coverage opens the phone disclosures and `Advanced` with Enter alone, asserts the controls they hide appear, and Tabs to the save action at every width.

Manual 200% browser-zoom smoke check. At a 720×450 CSS viewport — what a 1440×900 window becomes at 200% zoom — the project factory and handoff pages both reported `documentElement.clientWidth` 720 with `scrollWidth` 720, so neither scrolled sideways. The handoff switched to its phone layout: Sections 2–4 and `Advanced` were closed disclosures, the title and plan fields stayed visible, and the save action remained reachable and unclipped (44px tall, 672px wide inside the 720px viewport).

Screenshots are the deterministic presentation fixture, taken through Playwright's test output and copied to the gitignored `.docs-archive/studio-uiux/`: `factory-desktop.png`, `factory-phone.png`, `handoff-desktop.png`, `handoff-phone.png`, `draft-phone.png`. They are hierarchy, spacing and disclosure evidence, not a live factory.

Unit and type baselines are unchanged by this task: it changes only Playwright specs and this record, so `npm test` stays at 1092 passing and `npm run check` at the same 5 errors and 1 warning in the same 6 files. The regenerated embedded SPA is committed as `web/bridge/static/index.html` with `assets/index-j3Niiawc.js` and `assets/index-w5fTGmkA.css`.
