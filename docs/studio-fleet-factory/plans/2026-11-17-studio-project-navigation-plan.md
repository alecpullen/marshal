# Studio project settings and navigation implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute this approved plan.

**Status:** approved by the user on 2026-11-17.

**Goal:** Clarify project-to-factory navigation and make project CI defaults understandable and editable without losing the existing configuration semantics.

**Architecture:** Reuse the current project settings record, optimistic version/ETag handling, shell factory store and existing hash routes. Build a structured CI editor that reads optional same-project, same-repository observations from already loaded factory items; retain raw editing and never synthesize forge requirements.

**Tech stack:** Svelte 5, TypeScript, Tailwind, Vitest and Playwright/axe using the repository's isolated bridge and presentation fixtures.

**Spec:** [project settings and navigation](../studio-project-navigation-spec.md); [visual foundation](../studio-visual-foundation-spec.md).

**Base and dependencies:** Authored against `origin/main` `47fa29d7`. Execute after the visual foundation plan; coordinate with the [work-item experience plan](2026-11-17-studio-work-item-experience-plan.md) and active subagent/settings work, reconciling current files before editing. These plans share UI files and are sequential, not parallel-safe.

**Non-goals:** a new global factory settings model, automatic forge discovery or new backend APIs, automatically selecting checks, changing no-required-CI approval, granting permissions, or reworking project/work-item ownership.

**Constraints:** Keep existing project settings sections, inheritance/override semantics, ETag conflict behavior, unavailable values, unrelated fields and per-section save. Browsing and suggestions perform no mutations. Do not call stale or partial CI evidence a required-check list. Keep raw entry available; never replace invalid input with an empty inherited value. Commit each verified task as `studio-project-ux: task N — <title>`.

## Contracts

Add to existing `web/ui/src/lib/factory/config.ts`; import `FactoryCheckKey` and `FactoryItem` from `../factory`. All functions are pure and do not mutate arguments.

```ts
export interface CheckParseError { line: number; message: string }
export interface CheckParseResult { checks: FactoryCheckKey[]; errors: CheckParseError[] }
export interface CheckSuggestion {
  key: FactoryCheckKey
  observedAt: string
  headSha: string
  itemId: string
}
export function parseFactoryChecks(text: string): CheckParseResult
export function formatFactoryChecks(checks: FactoryCheckKey[]): string
export function suggestFactoryChecks(items: FactoryItem[], projectRoot: string, repoId: string): CheckSuggestion[]
```

`parseFactoryChecks` parses nonblank lines: `commit_status:` is followed by the complete context (including colons) and `check_run:` is followed by a name (including colons) and its final `:<positive integer appId>`. Valid status app ID is `0`; reject invalid kind, empty name, noninteger/out-of-range app ID and unknown syntax with the original one-based line number. Do not discard valid lines on an error. `formatFactoryChecks` preserves names and case; if a saved name cannot be represented in a single raw line, the UI keeps structured mode with the original value and explains why raw mode is unavailable. `suggestFactoryChecks` considers only items whose `projectRoot` and `repoId` exactly match the selected project and configured repo, and only actual `round.ci.checks` evidence. Dedupe by `(kind,name,appId)`, select the observation with latest valid `observedAt` (stable item/round order for ties), preserve unknown timestamps as unknown, and label each suggestion with its actual item and revision. Empty repo yields no suggestions. No implicit selection or completeness claim.

## Task 1: Lossless check parsing and observations

**Goal:** CI check draft values and observed suggestions have precise, tested identities.

**Files:** `web/ui/src/lib/factory/config.ts`, `web/ui/src/lib/factory/config.test.ts`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`.

**Steps:**

1. Add Contracts and unit tests `parseChecksPreservesColonNames`, `parseChecksReportsEveryInvalidLine`, `formatChecksRoundTrips`, `suggestChecksStayInProjectAndRepo`, `suggestChecksKeepDistinctApps`, and `suggestChecksHandleStaleOrMissingEvidence`. Include case-sensitive names, unknown timestamps, no repo, app ID zero for status and duplicate observations.
2. Implement the pure helpers. Preserve literal input for an error, prohibit silent truncation, and ensure a saved non-representable name remains available in structured mode instead of being normalized. Do not infer required checks from statuses/conclusions.
3. In `Project.svelte`, replace the local `parseChecks` implementation with `parseFactoryChecks` in `factoryDefaultsBody`, throwing a field-specific error when any parse error exists. Preserve empty text as the existing absent/inherit value and leave all other settings unchanged. Add a regression test for a colon in a status context and one for the last-colon app ID.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/factory/config.test.ts src/views/Project.test.ts` passes; existing settings payload and ETag tests remain green.

## Task 2: Structured CI editor with raw fallback

**Goal:** Users can view, add, edit and remove explicit CI check identities without memorizing the wire syntax.

**Files:** new `web/ui/src/lib/factory/ExpectedChecksEditor.svelte`, new `web/ui/src/lib/factory/ExpectedChecksEditor.test.ts`, `web/ui/src/views/Project.svelte`, `web/ui/src/views/Project.test.ts`.

**Steps:**

1. Implement `ExpectedChecksEditor.svelte` with a string draft supplied by and reported to the parent, using the Task 1 helpers as the only parser. Default to structured rows for valid representable values. Each row has labeled type, name/context and, for check runs, positive integer app ID; provide Add and Remove. Empty rows are unsaved draft state, not an instruction to remove all checks.
2. Provide a clearly labeled `Advanced: raw check identities` mode. Keep the exact raw text in memory, including malformed lines, when switching away; when errors exist show them and keep the raw editor visible rather than dropping data. Switching back after a valid structured change produces the canonical text through `formatFactoryChecks`. Explain the `check_run` versus `commit_status` identity difference. Do not allow names containing newline to be converted into a lossy raw representation.
3. Replace the expected-checks `<textarea>` on the Factory card in `Project.svelte` with the editor while preserving the existing `expectedChecksText` draft and `saveDefaults` behavior. Do not autosave, mutate on mode switch, or acknowledge no-required-CI. All unrelated settings and unknown route members remain intact.
4. Add component and page tests for type changes, multiple rows, same-name different-app checks, raw/structured round trip, invalid raw input, cleared/inherited values, no backend writes before Save, and keyboard/screen-reader field/error associations.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/factory/ExpectedChecksEditor.test.ts src/views/Project.test.ts` passes.

## Task 3: Offer recorded CI observations

**Goal:** Users may explicitly add an observed check as a suggestion without treating it as an authoritative requirement.

**Files:** `web/ui/src/App.svelte`, `web/ui/src/views/Project.svelte`, `web/ui/src/lib/factory/ExpectedChecksEditor.svelte`, `web/ui/src/views/Project.test.ts`, `web/ui/src/lib/factory/ExpectedChecksEditor.test.ts`.

**Steps:**

1. Pass the shell's existing `FactoryState` from `App.svelte` to `Project.svelte`; do not create another poller or perform a new forge/API request. Derive suggestions with `suggestFactoryChecks` from the current project's existing factory item list and the project's configured repo. If a factory list is loading, stale/error or unavailable, present an explanatory nonblocking message and keep manual entry usable.
2. Render an optional `Previously observed checks` disclosure with check name/type/app ID, source item, revision and observation time. Selecting a suggestion adds a row but never saves, selects other suggestions, changes inheritance, or calls it required by the repository. Retain selections and unsaved input if the store refreshes; previously saved checks missing from suggestions remain visible.
3. Test that same-name different-app candidates stay distinct; other projects/repos never appear; no repo/no history/stale feed works manually; repeated selection deduplicates; and no API mutation occurs until Save. Assert the exact saved `expectedChecks` payload only includes user's explicit selections.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/lib/factory/ExpectedChecksEditor.test.ts src/views/Project.test.ts src/lib/factoryStore.test.ts` passes; do not create a second shell store.

## Task 4: Project context and section navigation

**Goal:** People can move between project, factory and work-item surfaces and reach project settings sections without losing work.

**Files:** `web/ui/src/views/Project.svelte`, `web/ui/src/views/Factory.svelte`, `web/ui/src/lib/factory/WorkItemHeader.svelte`, `web/ui/src/lib/routes.ts`, `web/ui/src/lib/routes.test.ts`, `web/ui/src/views/Project.test.ts`, `web/ui/e2e/project-settings.pw.ts`.

**Steps:**

1. Add a `Project settings` section-navigation control in `Project.svelte` for Workspace, Model, Factory, Session, Intake, Planner delegation, Health and Policy. On desktop render a compact list; on phone use a labeled disclosure. Section actions scroll the actual page container and focus the section heading with `tabindex=-1`; do not change the app hash, reload, save, unregister the existing route guard or discard draft fields. Keep existing sections and separate saves intact.
2. Group existing create/open factory and independent-agent actions near the project context, using existing `#factory/new`, scoped factory and `#new` project prefill routes. Add a real `Start agent` link using the existing route formatter; preserve the current `Create factory work item` and `Open project factory` URLs.
3. In factory and work-item breadcrumbs, keep project identity visible and linked to the existing encoded `formatProjectRoute` when the item has a project root. If it is missing, display `Project unavailable` rather than silently linking elsewhere. Do not alter `formatFactoryRoute`, selected item view, worker selection or Browser Back behavior.
4. Test nested/special-character roots and item IDs, unavailable/missing roots, section jumps with unsaved edits, focus, route guard, Back, keyboard navigation, phone drawer and no mutation from navigating. Keep working external links and named landmarks.

**Verify:** `cd web/ui && npm run check && npm test -- --run src/views/Project.test.ts src/lib/routes.test.ts src/views/Factory.test.ts src/views/FactoryItem.test.ts`; from root `scripts/e2e/run_browser_checks.sh e2e/project-settings.pw.ts e2e/factory-item.pw.ts`. All pass.

## Task 5: Cross-surface and conflict proof

**Goal:** Project setup, check editing and navigation work together on desktop and phone without changing backend semantics.

**Files:** `web/ui/e2e/project-settings.pw.ts`, `web/ui/e2e/factory-handoff.pw.ts`, `web/ui/e2e/fixtures.ts`, `docs/studio-fleet-factory/studio-project-navigation-spec.md`.

**Steps:**

1. Add a real-bridge test for structured manual checks → Save defaults → handoff inherited summary, including colon-containing status context, an ETag conflict, reload/unsaved confirmation and exact payload. Use the isolated fixture project and no forge or provider.
2. Add a clearly labeled mocked-presentation case for recorded check suggestions and navigation project → factory → item → worker → Back. Verify source metadata, no automatic selection, stale warning, keyboard/focus, special-character IDs and no writes from navigation. Do not claim the fixture verified live forge discovery.
3. Run dark/light axe scans, 390 × 844 and 390 × 600, tablet/desktop, 200% zoom and reduced motion. Save and inspect representative screenshots with fixture status; append results and any untested physical-device/live-forge coverage to the spec delivery record.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/project-settings.pw.ts e2e/factory-handoff.pw.ts e2e/factory-item.pw.ts` passes; `cd web/ui && npm run check && npm test -- --run && npm run build` passes. From the root `go build ./cmd/webbridge` embeds the SPA and `go test ./web/bridge -run TestWebIsStdlibOnly` passes.

## Final verification

Run `scripts/e2e/run_browser_checks.sh`, `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent` and `git diff --check`. Report live forge and physical-device coverage separately. Never modify embedded static assets by hand.
