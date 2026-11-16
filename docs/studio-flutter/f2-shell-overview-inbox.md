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
