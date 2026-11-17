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
| Mocked presentation | `studio-fleet.pw.ts`, `factory-handoff.pw.ts`, `factory-item.pw.ts` | The shipped Fleet, project factory, handoff, saved-draft approval and work-item surfaces render a given state correctly, and the work item's routes navigate where a real one would. The fleet/inbox/factory/settings reads are answered from `fixtures.ts`; so are the session reads and event streams an embedded or full conversation mounts. Creates and commands are recorded and answered there too. |

A mocked case is **never** evidence that the bridge produced the state. It is
evidence about rendering only.

Neither style creates, approves, retries, or recovers factory work. The fixture
bridge has no GitHub credential and `factoryAPI.preflight` requires one, so a
real-bridge case that tried to would be proving the credential's absence rather
than the UI. States that need real factory records — mixed attention, a failed
factory read, an unavailable project, a worker agent, an active round, a
decision, a recovery, failing CI, a ready review, several workers and PRs — are
therefore covered by the mocked-presentation suite. The session answers a
work-item conversation mounts are empty but real-shaped: this bridge has no
agent behind those ids, and a navigation case must not be obscured by reads
that belong to the full session rather than to the work item.

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
| `activeItemOpensActivity` | active execution: header state, stage and round, every recorded event, the worker link, and exactly one selected-session stream (none for a worker the item does not hold) |
| `agentDrillInReturnsToSameActivity` | a deep-linked worker drill-in, and Back landing on the same item and activity |
| `fullSessionBackReturnsToFactory` | Open full session, the full page's dock, and Back consuming `from` |
| `questionAnswerUsesCurrentDecision` | the question panel's answer, carrying the decision ID and the version it was reviewed at |
| `retryUsesFreshRoundCopyAndRealPayload` | the fresh-round copy, the real profile list, and a route-only retry payload with no replacement |
| `recoveryAttestationRemainsDeliberate` | the named-worker attestation staying disabled until it is exact, confirmed and explained |
| `failedCIPrecedesNarration` | failing checks and findings before the engineering report, and a check with no URL or log |
| `readyReviewCompactsPasses` | passing checks collapsed behind their count, a clean review, and no merge action |
| `previewMismatchIsVisible` | a preview revision that differs from the recorded PR head, one that matches, and one never reported |
| `itemStatePollDoesNotChangeSelectedView` | a poll that changes the item's state without moving the reader's selected tab |
| `severalWorkersAndPRsAreNamedNotAggregated` | each worker and PR as its own row, including the ones with nothing recorded |
| `activeAndDecisionScanCleanInBothThemes` | axe (dark and light) on active execution and the question panel, no horizontal overflow |
| `recoveryAndReadyScanCleanInBothThemes` | axe (dark and light) on recovery and the ready review, no horizontal overflow |
| `keyboardOnlyDrillInAndReturn` | Tab and Enter alone drill into a worker and return to the same activity |
| `reducedMotionKeepsTheItemUsable` | nothing animates under `prefers-reduced-motion`, and the item stays navigable |
| `codeAndLogsScrollInsideTheirContainers` | a 120-line log scrolls inside its own box rather than the page; primary actions stay 44px |
| `twoHundredPercentZoomKeepsTheWorkItemUsable` | the four work-item surfaces at a 720×450 CSS viewport — what 1440×900 becomes at 200% zoom |

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

Those three are the whole baseline: at every width, the three cases above are
the only failures, so a full run is 150 cases — 139 pass, 2 skip (the 200% zoom
review's two non-desktop slots) and 9 fail, which is three cases × three widths.

The slice-2 record names a fourth case, `factory handoff › shows inherited
sources and keeps its input when opening settings`; it passes now. Task 15
updated its assertions to the `config-<slug>` rows the shared
effective-configuration summary renders, so the case and the UI agree again.

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

The two slice-3 scans (`activeAndDecisionScanCleanInBothThemes`,
`recoveryAndReadyScanCleanInBothThemes`) scan the whole page, because a work
item's conversation sits outside `main`. They draw the same palette family at
these surfaces' own nodes, so they pin it by pair as well: in the dark theme
`#ece9e4 on #ff875f` wherever a filled action is drawn, and the empty
conversation's muted hint at `#77736e on #18171a`; in the light theme
`#ff875f on #faf8f5`, a warn-toned status badge on its own tint
(`#ffaf5f on #fbedde`), a gold role badge on its tint (`#ffaf00 on #f4e5c7`),
and the empty conversation's hint at `#908b84 on #f2efea`. Nothing else is
pinned: a new rule, or a pair outside that list, fails the scan. The dark
`required` list is empty on purpose — these surfaces draw no enabled filled
button until a person fills in the decision or retry form — while the light
`required` list keeps `#ff875f on #faf8f5`, which every width draws.

The 200% zoom review is `twoHundredPercentZoomKeepsTheWorkItemUsable`: it sets a
720×450 CSS viewport — what a 1440×900 window reports at 200% zoom — and asserts
`documentElement.clientWidth` 720 with `scrollWidth` 720 on the active,
decision, recovery and ready surfaces, plus a 44px-tall, unclipped answer
action. It runs in the desktop project only, because one CSS viewport is the
measurement.

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

The slice-3 scans add `execution-desktop.png` and `execution-phone.png` (active
execution), `decision-phone.png` (the pending question), `recovery-desktop.png`
(the named-worker recovery), and `ready-review-desktop.png` and
`ready-review-phone.png` (the ready review) to the same directory. They are the
mocked presentation fixture, so they are hierarchy, spacing and disclosure
evidence — never a live agent or a persisted factory record.
