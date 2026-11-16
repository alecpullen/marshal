# F2 — shell, Overview and Inbox

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`, ahead of F0 and F1. Part of [the Studio programme](programme.md).

Read first: [design.md](design.md) §2 and §5,
[design-system.md](design-system.md), [f1-client.md](f1-client.md),
`findings/tray-and-window.md`, and in the Svelte client: `lib/nav/model.ts`,
`lib/fleet.ts`, `lib/store.ts`, `lib/inbox.ts`, `lib/pending.ts`,
`lib/studio/model.ts`, `lib/interaction.ts`, with their tests.

**Before starting.** F0 and F1 have landed. The Svelte Studio is frozen from
the day this phase starts: bug fixes only. Re-read the files above at the
current `main`.

## What F0 found

From [findings/tray-and-window.md](findings/tray-and-window.md), verified by
a macOS debug run and unverified on Linux:

- macOS: keep `LSUIElement`, and switch the app's activation policy between
  accessory and regular from a small method channel in
  `MainFlutterWindow.swift`. Closing the window hides it; the window and
  engine are never destroyed. No window plugin is needed.
- Quit must be its own channel call that terminates the app.
- Linux: `app/linux/runner/my_application.cc` is the unmodified template and
  shows a window at launch, contrary to what D3 says. This phase edits it:
  no window on the first frame, hide on close, present on open, and hold
  the application so it survives with no window.
- A person must still confirm by eye the menu-bar item, the Dock icon
  coming and going, and the red button; the finding lists the steps.

## Context

This is the first phase a person can use. It builds the frame every later
phase fills — the window, the sidebar, navigation — and the two places that
need only the fleet-loop contract F1 already generated: Overview and Inbox.

The mock shows the target: `mock/lib/main.dart` (shell and sidebar),
`overview.dart`, `inbox.dart`. The mock's data is invented; here every
number comes from a daemon.

## Goal

- A Studio window on macOS and Linux, opened from the tray, and the same
  Studio in a browser at `/next/` on any bridge.
- The sidebar on live data: hosts, projects, each project's threads with
  status, the Inbox count.
- Overview: needs you, running now, projects, capacity, spend, activity.
- Inbox: every ask the fleet contract carries — an agent's approvals and
  questions, and intake submissions — answered from its card.
- Selecting a thread opens a placeholder that names it and says its screen
  arrives in F3. Nothing pretends to work.

## Non-goals

- The thread screen, the new-work dialog, pull requests, Automations,
  Library and Settings. Their sidebar rows exist and open a `StateMessage`
  in the planned state.
- Search (`⌘K`). The field is drawn disabled with a tooltip.
- Changing the tray's menu beyond adding "Open Studio".

## Decisions

1. **Window and tray.** Follow `findings/tray-and-window.md`. The app is a
   menu-bar item with no Dock icon while no window is open, and shows in
   the Dock while one is. Closing the last window does not quit. "Open
   Studio" in the tray's menu opens or focuses the window.
2. **Entry points.** One `main.dart`. On desktop it starts the tray and
   opens the window on request; in the browser it starts the Studio
   directly, with no tray code compiled in.
3. **Navigation is a pure model.** `model/nav_model.dart` takes the current
   place, the hosts, the projects, the agents and the inbox counts, and
   returns what the sidebar draws: fixed places with counts, projects in
   name order, each project's threads grouped as current and earlier. It is
   the successor of `buildNavModel` and its tests are ported first.
4. **Places.** An enumeration with exactly: overview, inbox, pull requests,
   project, thread, automations, library, settings. In the browser the
   place and its target are the URL, so reload and Back work; the same
   parser serves deep links on desktop.
5. **What a thread is, for now.** Every agent the fleet reports is a thread
   of kind agent under its project. Work items and plan runs join the tree
   in F6. An agent that belongs to a factory worker is hidden from the tree
   once F6 can show its work item; until then it is listed as an agent.
6. **Stores.** `model/fleet_store.dart` holds, per host, the agents,
   projects, inbox and budgets from snapshots, and applies the stream's
   deltas. It exposes the merged all-hosts view. It is the only writer of
   that state.
7. **Asks.** The fleet contract has two inbox item kinds, agent and intake
   (`InboxItemKind`), and an agent is parked on one of two pending kinds,
   approval and question (`PendingKind`). `AskCard` shows exactly these
   three in this phase. Approvals offer approve once, approve with the
   derived pattern, and deny; questions offer their options and a free
   reply; intake offers start, edit first and decline. Factory asks join in
   F6; network decisions and review-bot drafts join in F7. The card is
   built so a later kind is an addition, not a rewrite. An answer is posted, the card
   shows pending, and it leaves when the stream confirms. A failed post
   keeps the card with the error and a retry.
8. **Overview is a projection.** `model/overview_model.dart` computes the
   four sections from the store. It infers nothing: a count is a count of
   real items, and an unreachable host's rows are marked stale with their
   age.
9. **`/next/`.** The bridge embeds the Flutter web build in a second
   directory beside `static` and serves it under `/next/`, with the same
   fallback to its index for unmatched paths that `staticHandler` gives the
   Svelte build. API routes are unchanged. The Svelte Studio gains one
   link to it.
10. **Notifications stay where they are.** The desktop notifier belongs to
    `marshal fleet`. The app raises none in this phase.

## Work

Suggested pull requests, in order.

1. **Pure models.** `nav_model.dart`, `fleet_store.dart`,
   `overview_model.dart`, `inbox_model.dart`, each with tests ported from
   the Svelte tests that check a rule which survives. Record in the pull
   request which Svelte tests were ported, which were dropped and why.
2. **Shell.** Window, tray entry, sidebar, host switcher, the place router,
   the narrow and medium layouts, and the planned-state placeholders.
3. **Overview.** The four sections and the side rail.
4. **Inbox.** The list, its filters, and every ask kind answering for real.
5. **`/next/`.** The second embed, its handler and test in `web/bridge`,
   the build step in CI and in the release workflow, and the link from the
   Svelte Studio. Record the change in `webbridge` binary size.
6. **End to end.** One suite that starts the fixture bridge used by
   `scripts/e2e`, starts an agent that asks for approval, and drives the
   app to answer it.

## Tests

- `app/test/model/`: `nav_model_test.dart`, `fleet_store_test.dart`
  (snapshot, each delta kind, a removed project, an unknown delta kind
  ignored safely), `overview_model_test.dart`, `inbox_model_test.dart`.
- `app/test/ui/`: `shell_test.dart`, `overview_test.dart`,
  `inbox_test.dart`, each against a fake client, each registering its
  screen with the tooltip, states and overflow tests from F0.
- `web/bridge`: the `/next/` handler serves its index, falls back for
  unmatched paths, and never shadows an API route.

## Done when

- With real agents running on a local daemon and on a server, the sidebar,
  Overview and Inbox agree with what `marshal fleet` shows on each.
- An approval answered in the app unblocks its agent; a denial is delivered
  as a denial.
- Stopping a host marks its rows stale and then unavailable; nothing
  disappears and nothing claims to be current.
- The browser Studio at `/next/` does all of the above on a phone-width
  window.
- Every screen passes the design system's enforcement tests.

## Open questions

- Whether the planned-state placeholder for a thread should link to the
  Svelte session page until F3 lands. Recommended: yes, in the browser only.

---

## Implementation record (F2, 2026-11-17)

Branch `studio/f2-shell`, off `studio/f1-client`. Every task landed as one
commit, `f2-shell: task N — <title>`.

### What landed

| Task | What it is | Where |
|---|---|---|
| 1 | Places, thread references, the navigation model, and the age formatter | `app/lib/src/model/{place,thread_ref,nav_model,age,fleet_store}.dart` |
| 2 | The fleet store: per host snapshots, every delta kind, replay gaps, a host's reach kept with its rows | `app/lib/src/model/fleet_store.dart`, `app/test/model/fleet_store_test.dart` |
| 3 | The inbox and overview models: three ask kinds answered through the bridge, the four Overview sections | `app/lib/src/model/{inbox_model,overview_model}.dart` |
| 4 | The `marshal/window` channel (`show`, `hide`, `quit`) on both runners, and "Open Studio" as the tray's first item | `app/lib/src/ui/shell/window_host.dart`, `app/lib/src/menu/desktop.dart`, `app/macos/Runner/*.swift`, `app/linux/runner/my_application.cc` |
| 5 | The shell: sidebar, host switcher, the narrow panel, planned places. `NavRow` joined the kit (§10) | `app/lib/src/ui/shell/*`, `app/lib/src/ui/kit/nav_row.dart` |
| 6 | The Overview: needs you, running now, projects, and the capacity and spend rail below 1080 | `app/lib/src/ui/overview/overview.dart` |
| 7 | The Inbox: filters, every ask kind answered from its card, a failure kept with a retry | `app/lib/src/ui/inbox/inbox.dart` |
| 8 | `/next/`: the Flutter web build embedded beside the Svelte one, its handler, the CI and release steps, one link | `web/bridge/assets.go`, `web/bridge/static-next/index.html`, `.github/workflows/*` |
| 9 | The end-to-end flow, a widget test that runs it, and this record | `app/integration_test/f2_test.dart`, `app/test/ui/f2_flow_test.dart` |

`webbridge` binary size: 22,417,471 bytes before the `/next/` embed, 22,412,020
after (placeholder index only; the difference is Go build variance, not the
~600-byte page).

### Verification

| Task | Verify command | Result |
|---|---|---|
| 1–3 | `cd app && flutter test test/model/` | **pass** (45 tests) |
| 4 | `cd app && flutter analyze && flutter test && flutter build linux --debug` | analyze **pass**, test **pass** (399); `flutter build linux --debug` **environment-blocked**: no CMake/GTK3 on this machine |
| 5 | `cd app && flutter test` | **pass** (465) |
| 6 | `cd app && flutter test` | **pass** (529) |
| 7 | `cd app && flutter test` | **pass** (594) |
| 8 | `CGO_ENABLED=1 go test ./web/bridge -run 'Next\|Assets\|StdlibOnly' -count=1`; `cd web/ui && npm run check && npm test` | go **pass**; `npm run check` reports **exactly the 5 pre-existing errors and 5 warnings** (`fleet.test.ts`, `AgentConversation.svelte`, `Run.svelte`, `Review.test.ts`, `Runs.test.ts`, all `AgentRow`/`AccessState` nullability) and no new ones; `npm test` **pass** (120 files, 1355 tests) |
| 9 | `cd app && flutter analyze && flutter test && flutter test integration_test -d linux && dart format --output=none --set-exit-if-changed lib test`; `CGO_ENABLED=1 go test ./web/... -count=1` | analyze, test, format and Go **pass**; `flutter test integration_test -d linux` **environment-blocked**: no display and no CMake/GTK3. `test/ui/f2_flow_test.dart` runs the same flow and **passes** |

Environment-blocked, not failures: `flutter test --platform chrome` (no
working browser host here), `flutter build linux --debug` and the integration
test (no CMake/GTK3 development headers, no display).

### Decisions this plan left open

- **`NavRow` is a new kit component** (design-system.md §5 and §10): the
  sidebar's row is a component, not a one-off in `shell.dart`. Added with its
  document entry, gallery specimen and regenerated goldens.
- **A snapshot never overwrites a newer delta.** `FleetStore.refresh` records
  the delta sequence when it starts and keeps the rows a delta has changed
  since, so a connect-time read cannot put back an old `activity` or a lost
  `pending`. A `pending` delta reads the rows again, because the delta carries
  only the kind and the request itself is the snapshot's.
- **A budget delta re-reads the report** rather than assembling caps and
  blame from one delta (the Svelte store's views refetch, too).
- **"Earlier" is counted, not listed**: `NavProject.earlier` is a count and
  the sidebar draws "Earlier · N", as the mock does.
- **Projects group by root across hosts** (the contract's `NavProject` has no
  host), and a thread keeps its host so opening it stays exact.
- **The host switcher narrows the sidebar's tree only.** The Overview and the
  Inbox stay fleet-wide: they are projections of the whole store, and the
  contract's `buildNavModel` takes no host.
- **The narrow layout's navigation is a full-height panel** behind the app
  bar's button rather than a floating drawer: the kit has no scrim container
  outside `Sheet`, and extending the kit for a drawer is F0-sized work. The
  behaviour §4 asks for — the sidebar is not beside the content below 760, and
  the same rows are reachable — holds.
- **The Overview's rail has capacity and spend, no activity.** The contract
  carries no activity series, and §8 forbids inventing one; the rail says
  where it will come from.
- **`app/lib/src/model/age.dart`** is the design system's relative time
  ("8m", "1h 12m", "2d") in one place, since §8 specifies the format and the
  kit had no implementation of it.
- **The Svelte tests ported** (task 1): `marksPrimaryAndSubroutesActive` (the
  active row), `groupsProjectsAndCurrentWorkItems` (projects and their current
  work), `showsNeedsCount`, `marksRoadmapDestinationsPlanned` (the planned
  flag), `sortsProjectsByShortNameThenRoot`. **Dropped**:
  `deduplicatesAllPages` (F2's model has no all-pages list), the sub-route and
  query matching (a place is an exact path, and `parsePlace` ignores a query),
  and the roadmap destinations themselves (not F2's sidebar).

### Blocked on the wire, not on time

Two actions the design names cannot be built truthfully against this bridge:

- **"Always allow the derived pattern"** for an approval. `Decision` carries
  `approved`, `edited` and a typed capability choice and nothing else
  (`web/bridge/contract/answers.go`), and `PermissionRequest` carries no
  derived pattern; `approved: true` is "allow once". The card offers
  `Approve once` and `Deny`. The fix is a wire change (a `pattern` field on
  the decision, and the bridge deriving it), not a UI one.
- **"Edit first"** for an intake. `POST /api/pending/{id}/approve` takes no
  body (`web/bridge/http.go`), so there is nothing to send an edit through.
  The card offers `Start` and `Decline`.

Both are one entry in `InboxModel`'s action table each, so adding them is an
addition, not a rewrite (decision 7).

### Needs a Mac

The window and tray work is unverified by eye, exactly as the finding says:
the menu-bar item, "Open Studio" showing the window with a Dock icon, the red
button hiding it and removing the Dock icon, and the app surviving. The
finding's seven manual steps still apply.
