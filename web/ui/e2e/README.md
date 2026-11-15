# Browser checks

Playwright checks for the Fleet-first Studio shell, its navigation, and the
project/factory settings surfaces. They drive the SPA the bridge actually
serves, so what they observe is the shipped bundle and the shipped API — not a
jsdom stand-in.

```bash
scripts/e2e/run_browser_checks.sh
```

The script builds the SPA, builds `webbridge`, writes a fixture config with
three agent profiles and no real provider, starts the bridge on port 7799 with
an isolated `HOME`/config/data dir, runs the suite at desktop (1440×900),
tablet (768×1024) and phone (390×844), and stops the bridge. `KEEP=1` leaves
the fixture directory — the generated config, the throwaway project and the
bridge log — in `/tmp` and prints the path, so a failure can be inspected after
the fact; the bridge itself is always stopped. `PW_PORT` moves it off 7799.

Prerequisites: `npm install` in `web/ui`, plus the browser:

```bash
cd web/ui && npx playwright install chromium
```

## Two fixture styles, never interchangeable

| Style | Files | What it proves |
|---|---|---|
| Real bridge | `studio-shell.pw.ts`, `project-settings.pw.ts`, `factory-handoff.pw.ts` | The shipped SPA and the shipped API agree: routes, deep links, the route guard, the roadmap, settings reads/writes, project-factory scope and filters, inherited handoff defaults, and field-linked validation. |
| Mocked presentation | `studio-fleet.pw.ts`, `factory-handoff.pw.ts` | The shipped Fleet, project factory, handoff and saved-draft approval render a given state correctly. The fleet/inbox/factory/settings reads are answered from `fixtures.ts`, and creates and commands are recorded and answered there too. |

A mocked case is **never** evidence that the bridge produced the state. It is
evidence about rendering only.

Neither style creates, approves, retries, or recovers factory work. The fixture
bridge has no GitHub credential and `factoryAPI.preflight` requires one, so a
real-bridge case that tried to would be proving the credential's absence rather
than the UI. States that need real factory records — mixed attention, a failed
factory read, an unavailable project, a worker agent — are therefore covered by
the mocked-presentation suite.

Every case runs in all three projects. No case depends on an external provider,
forge, credential, or live agent.

## Why these live beside vitest, not inside it

The specs are `*.pw.ts`, and `web/ui/playwright.config.ts` matches only that
name. vitest's default include picks up `*.spec.ts`, so a browser spec named
`*.spec.ts` would be collected by `npm test` and fail there for having no
browser. `npm test` stays jsdom-only; these run under `npm run test:browser`.

## Shared helpers and fixtures

`e2e/fixtures.ts` owns `open`, `reopen`, `auth`, `expectNoHorizontalOverflow`,
`tabTo`, `setTheme` and the `FactoryItem` / `AgentStatus` / `ProjectStatus` /
`Inbox` builders, plus `mockFleet` — the route interceptor the presentation
suite runs on. The builders are typed from the generated contract, so a field
the bridge renames breaks the fixture instead of silently vanishing from a mock.
`mockFleet` holds the fleet stream open rather than completing it: a completed
stream is one the SPA reconnects to on a timer, which would make the
request-count case meaningless.

## What is covered

Real bridge:

| Case | Surface |
|---|---|
| `emptyFleetHasWorkingStartActions` | Fleet home, project card actions, resource directory |
| `allRoadmapPagesArePlannedAndReadOnly` | all six roadmap areas, Planned badge, no inputs, no writes |
| `projectPrefillAndUnknownProject` | `#new?project=…` prefilled and unknown, handoff prefill |
| `shellDeepLinksAndBrowserBack` | deep-linked reload, shell navigation, Browser Back, `#fleet` redirect |
| `routeGuardCoversShellNavigationAndBrowserBack` | the one route guard, declined by click and by Back |
| `unknownRouteHasFleetReturn` | Not Found with a working Fleet return |
| `role overrides follow the agent default independently and reset` | project settings, model card |
| `a stale save warns, keeps the edit, and offers reload` | `If-Match` / 412 path |
| `factory form shows inherited sources and keeps its input` | handoff review, settings link |
| `no horizontal overflow and readable labels` | project settings, all three widths |
| `the handoff form does not overflow either viewport` | handoff form, all three widths |
| `unsaved edits are confirmed before in-app navigation` | draft guard |
| `projectFactoryLinksPreserveScope` | project factory breadcrumbs, scoped filters, header actions, global list |
| `handoffLoadsRealDefaultsAndValidation` | inherited handoff summary, field-linked validation, focus order |
| `handoffRouteGuardRetainsInput` | the handoff's own unsaved-input guard |
| `handoffHasNoOverflow` | handoff at all three widths, phone disclosures, desktop two columns |

Mocked presentation (`studio-fleet.pw.ts` and `factory-handoff.pw.ts`, labelled in-file):

| Case | Surface |
|---|---|
| `mixedFleetGroupsRealShapedWork` | the section order and grouping of a fleet holding one of everything |
| `factoryFailureDoesNotHideAgents` | a failed factory read beside a usable agent list |
| `factoryWorkersAreNotIndependent` | a worker agent linked to its item, not listed as independent |
| `unavailableProjectCannotStart` | project warning, and the prefill that refuses to start |
| `fleetAttentionAndProjectLinksSurviveReload` | attention and project links identical across a reload |
| `oneFleetStreamAndFactoryPollerSurviveNavigation` | one fleet stream and one factory list read across Fleet → Inbox → factory → Fleet |
| `fleetFitsAndScansCleanInBothThemes` | axe (dark and light), no horizontal overflow at any width |
| `keyboardOnlyNavigationReachesTheShell` | Tab and Enter alone reach Inbox and the resource links |
| `reducedMotionKeepsTheShellUsable` | nothing animates under `prefers-reduced-motion`, navigation still works |
| `projectFactoryFiltersAndDirectActions` | scoped counts, defaults, decision-first order, every filter, each card's direct action |
| `handoffSummaryTracksOverrides` | the summary's sources, live overrides, and a create payload that omits every followed field |
| `savedDraftShowsResolvedServerConfiguration` | the saved draft's rows come from the server's resolution, not the handoff's summary |
| `draftApprovalDoesNotStartBeforeClick` | the approval consequence, and exactly one `approve` command on the click |
| `factoryAndDefaultsScanCleanInBothThemes` | axe (dark and light) on the project factory and its defaults card, no horizontal overflow |
| `handoffScansCleanInBothThemes` | axe (dark and light) on the handoff summary and form, no horizontal overflow |
| `draftApprovalScansCleanInBothThemes` | axe (dark and light) on the saved draft's approval, no horizontal overflow |
| `handoffKeyboardReachesDisclosuresAndSubmit` | keyboard-only disclosure toggles and Tab order to the save action |

## Known baseline failures

Three cases in `project-settings.pw.ts` fail at every width for a fixture
reason, not a UI one: the "Model profile" select offers no `gamma` option
because the fixture bridge serves no model profiles, so `selectOption` times out
after 30s. They fail identically at the base commit `f42a7c59`:

- `role overrides follow the agent default independently and reset`
- `a stale save warns, keeps the edit, and offers reload`
- `unsaved edits are confirmed before in-app navigation`

The slice-1 implementation record in
`docs/studio-fleet-factory/studio-uiux-alignment-design.md` lists them with
their counts. Do not fix, skip, or weaken them as part of unrelated work.

The slice-2 full run surfaced one more failing case, in the same file and at
the same three widths:

- `factory handoff › shows inherited sources and keeps its input when opening settings`

It asserts the handoff form's old `implementer-effective`,
`reviewer-effective` and `target-branch-effective` hooks, which Task 13
replaced with the `config-<slug>` rows of the shared effective-configuration
summary. The case is stale, not the UI: the shipped handoff shows the same
values under the new hooks. `project-settings.pw.ts` is outside the slice-2
browser task's files, so that task did not change it; the slice-2
implementation record carries the decision.

## Accessibility baseline

`fleetFitsAndScansCleanInBothThemes` pins two pre-existing palette defects that
live in `web/ui/src/app.css` and `web/ui/src/lib/ui/Button.svelte` — files the
slice-1 browser task does not own. Filled buttons lose `text-bg` to an unlayered
`button { color: inherit }`, and the light variant redefines only the neutrals,
leaving the signal colours at dark-theme values on a light background. The scan
fails on any violation outside those exact contrast pairs, so a new defect still
fails; the pinned list must shrink when the palette is fixed.

The three slice-2 scans (`factoryAndDefaultsScanCleanInBothThemes`,
`handoffScansCleanInBothThemes`, `draftApprovalScanCleanInBothThemes`) scan the
`main` region and pin the same palette family. They report the dark `--color-fg`
on a filled accent button, and in the light theme an accent link or attention
text on the page background and on a card's surface, a muted badge on
`--color-line`, and attention on its own tint. Any other rule, or a pair
outside that family, fails the scan.

## Screenshots

`fleetFitsAndScansCleanInBothThemes` writes `fleet-<project>.png` through
Playwright's own test output (`web/ui/test-results/`, gitignored). The reviewed
copies live in the gitignored `.docs-archive/studio-uiux/` as
`fleet-desktop.png`, `fleet-tablet.png` and `fleet-phone.png`. They are the
mocked presentation fixture: deterministic evidence of hierarchy and spacing,
not a live factory.

The slice-2 scans write the same way: `factory-desktop.png`,
`factory-phone.png`, `handoff-desktop.png`, `handoff-phone.png` and
`draft-phone.png` are in `.docs-archive/studio-uiux/`. The project factory and
saved draft are the mocked presentation fixture, and the handoff is the same
mocked project settings, so all five are rendering evidence rather than a live
factory.
