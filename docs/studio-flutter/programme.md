# Marshal Studio in Flutter — build programme

Status: **approved in outline**, 2026-11-17 (decisions 1–4). Authored against
`main` at `f2ea2717`.

The UI this builds is in [design.md](design.md), with the clickable mock in
[`mock/`](mock/). This document is the order of work: the phases, what each
one ships, what it depends on, and the decisions that cross phases. Each
phase gets its own spec and one or more executable plans, written when the
phase before it has landed, as the daemon and distribution programmes do.

**Numbering.** These phases are **F0–F8** (Flutter). W, S, P and D are taken
by the web studio, the server posture work, the daemon and distribution.

## What was checked before writing this

Five facts shape the programme. Each was read from the tree or run on
2026-11-17, not assumed.

1. **Dart reaches the local daemon directly.** D3 recorded that Dart's
   `HttpClient` cannot use a unix socket, and built the tray on
   `marshal daemon status --json` for that reason. That is no longer true: a
   spike with Dart 3.x's `HttpClient.connectionFactory` made a plain request
   and read a streamed event response over a unix socket. The local profile
   serves the same routes on its socket as the server profile does on TCP,
   without the bearer check and peer-checked instead
   (`cmd/webbridge/main.go`). So the native app needs no new listener and no
   token for the local daemon.
2. **Most of the API has no generated types.** `web/bridge/contract` holds
   the fleet loop only: 48 exported TypeScript types come out of it. About
   250 more are written by hand in `web/ui/src/lib` — `api.ts` (146),
   `factory.ts` (44), `planner.ts` (25), `stack.ts` (21) and
   `working-drafts.ts` (20) — against roughly 240 route registrations in
   `web/bridge`. A Dart client cannot be generated from today's contract.
3. **The web build fetches fonts from the internet.** The mock's compiled
   output refers to `fonts.gstatic.com`. A self-hosted Studio on a private
   network must not need that. The mock's `main.dart.js` is 2.7 MB before
   compression, and the engine directory beside it is 36 MB on disk across
   several variants.
4. **The toolkit has moved.** CI pins Flutter 3.35.1 for the tray app. Flutter
   3.50 takes Material and Cupertino out of the SDK into the `material_ui`
   and `cupertino_ui` packages; the mock is written against those, the tray
   app is not.
5. **The tray app is deliberately windowless.** Its `Info.plist` sets
   `LSUIElement`, and its boundary test allows `dart:io` in one file. The
   Studio needs a window, a Dock presence while that window is open, and a
   second place that may open a socket.

## Decisions that cross phases

Decisions 1–4 were confirmed on 2026-11-17. The rest are recommendations.

One app serves every platform. A single Flutter project builds for macOS,
Linux, the web, iOS and Android; `app/` already has its macOS and Linux
runners. What differs per platform is confined to two places: the tray,
which exists on desktop only, and the transport in `client/`, where the unix
socket exists on desktop only and a phone reaches a server over HTTPS. Linux
gets the Studio window in F2 with macOS. Phones use the browser Studio until
the native mobile builds, which follow F8.

The rules every screen follows are in [design-system.md](design-system.md),
written before any screen and enforced by tests from F0.

Phase documents: [F0](f0-foundations.md), [F1](f1-client.md),
[F2](f2-shell-overview-inbox.md), [F3](f3-agent-thread.md),
[F4](f4-inspector.md), [F5](f5-pull-requests-and-ci.md),
[F6](f6-factory.md), [F7](f7-automations-library-settings.md),
[F8](f8-cutover.md). Each was written on 2026-11-17, ahead of the phases
before it, and opens with what to re-read before starting.

1. **One app, in `app/`.** The tray stays as it is and becomes one entry
   into the same program; the Studio window is the other. The web Studio is
   the same code built for the browser, without the tray. The mock stays in
   `docs/` as the reference until F8, then goes.
2. **Wire types are generated, and the contract grows phase by phase.** Each
   phase moves the Go types it needs into `web/bridge/contract` and
   generates both TypeScript and Dart from them, with a currency test for
   each. This costs Go work in every phase. It is what stops a third client
   drifting, and it retires the hand-written TypeScript types as it goes.
   The alternative — Dart models written by hand and pinned by shared JSON
   fixtures, as D3 did for one document — is cheaper per phase and leaves
   250 types maintained twice.
3. **The Svelte Studio freezes when F2 starts.** Bug fixes only. A new
   Studio capability lands in Flutter, or waits. Without this the rewrite
   chases a moving target; the factory work is adding Svelte screens now.
4. **The Flutter web build ships beside the Svelte one until F8.** From F2
   the bridge serves it under `/next/`, so it can be used on a phone against
   a real server long before it is complete. The cost is a larger
   `webbridge` binary for the length of the programme; F2 measures it.
5. **Layers inside `app/lib/src/`, enforced by the boundary test.**
   - `client/` — the only code that opens a socket or makes a request. It
     has a native transport and a browser transport behind one interface.
   - `model/` — pure Dart: stores and view models. It imports neither
     Flutter nor `dart:io`, so every rule in it is a plain unit test.
   - `ui/` — widgets. It reads `model/` and never touches `client/`.
   - `daemon/` — the tray's existing CLI path, unchanged.
6. **No state-management package.** The stores are `ChangeNotifier`s handed
   down by inherited widgets, as the mock does. A package is added only where
   writing the thing would be foolish: a terminal emulator, Markdown, syntax
   highlighting. Each is chosen in the phase that needs it, against the
   pinned version, not here.
7. **The transcript is rendered, never regrouped.** The client draws the
   stack view model the agent sends, as the TUI and the Svelte client do.
8. **Ember tokens are the only source of colour.** One file. A golden test
   covers light and dark.
9. **Every icon-only control has a tooltip.** A test walks the widget tree
   of each screen and fails on one without.
10. **Phone and tablet are a layout, not a phase.** Every screen is built to
    the three widths in the design. Native iOS and Android builds are a
    follow-on after F8.

## Ship order

Each phase is usable on its own. "Bridge work" is Go under `web/bridge` and
stays standard library only.

| Phase | Ships | Bridge work | Needs |
|---|---|---|---|
| F0 | Toolchain on Flutter 3.50; the layers and the rewritten boundary rule; Ember tokens, bundled fonts and the shared widgets, with goldens | None | — |
| F1 | The client: Dart generation beside the TypeScript one, transports, event stream with reconnect, hosts | Fleet-loop types already in the contract; none new | F0 |
| F2 | The Studio window on macOS and Linux; sidebar, Overview and Inbox on live data, with asks that answer; `/next/` on the web | Serve `/next/` | F1 |
| F3 | The agent thread: transcript, composer, asks in place, starting an agent | Stack and session types into the contract | F2 |
| F4 | The inspector: Changes and shipping, Files, Terminal, Preview, Inspect | Diff, exit, files, terminal and preview types into the contract | F3 |
| F5 | Pull requests and CI everywhere; Fix with a worker | **New**: a pull-request index with check states; dispatching a fix worker | F4 |
| F6 | The factory: work items, handoff, planning, plan runs, the project page | Factory, planner, drafts and run types into the contract | F4 |
| F7 | Automations, Library, Settings | The remaining types into the contract | F2 |
| F8 | Cutover: Flutter is the Studio; Svelte and Node leave the repository | Embed the Flutter build at `/` | F3–F7 |

F5, F6 and F7 do not depend on each other and can run in any order. F5 is
placed first because pull requests and their CI are meant to be the front of
the review experience, and because it is the only phase that needs a bridge
capability that does not exist.

## The phases

### F0 — foundations

Nothing in this phase talks to a daemon.

- Move CI and `app/` to Flutter 3.50 and the `material_ui` and
  `cupertino_ui` packages. The tray keeps working and its tests keep
  passing.
- Lay out `client/`, `model/` and `ui/` and rewrite the boundary rule in
  `app/README.md`, `AGENTS.md` and `d3-tray-app.md`. Extend
  `no_io_test.dart` to the new rule: which files may import `dart:io`, that
  `model/` imports no Flutter, that `ui/` imports no `client/`.
- Correct D3's statement that Dart cannot use a unix socket, and record the
  spike.
- Bring the Ember tokens and the shared widgets over from the mock, with
  widget tests and light and dark goldens. Bundle Geist and Geist Mono.
- Prove the web build fetches nothing from the internet once fonts are
  bundled, and record its size over the wire.
- Three spikes whose answers later phases depend on, each a throwaway with a
  written finding: selecting text across a long, virtualised, streaming
  list; the tray and a normal window in one macOS app; a terminal emulator
  package on macOS and web.

Done when: the tray app builds and passes on 3.50, the boundary test
enforces the new layers, the goldens exist, and the three findings are
written.

### F1 — the client

No screen beyond a connection check.

- A Dart generator beside `tsgen`, with the same shape: generated file
  checked in, a currency test, one command to regenerate.
- Transports: the unix socket for the local daemon; HTTPS with a bearer
  token for a server; same-origin requests in the browser. The spec settles
  where the native app gets a server's token, given `fleet.toml` and the
  keychain entry `marshal fleet login` already writes.
- The event stream: connect, resume from the last event, reconnect with
  backoff, and report "stale" honestly. The rules are the ones `sse.ts`
  already encodes; they move, they are not reinvented.
- Hosts: more than one daemon at once, each with its own state, and "all
  hosts" as a merged view.
- Tests run against a fake bridge in-process. Shared JSON fixtures are
  parsed by a Go test and a Dart test, so neither side can drift.

Done when: a test client attaches to a real local daemon and to a real
server-profile bridge, lists agents, and follows the fleet stream through a
dropped connection.

### F2 — shell, Overview, Inbox

The first thing a person can use.

- The Studio window on macOS, opened from the tray and from the Dock, with
  the app showing in the Dock only while a window is open.
- The sidebar from live data: hosts, projects, threads with their status,
  the Inbox count.
- Overview: needs you, running now, projects, capacity, spend, activity.
  Pull requests appear here in F5.
- Inbox: every ask the fleet contract already carries — approvals,
  questions, intake, network decisions — answered from the card.
- The pure view models (navigation, the fleet projection, grouping and
  ordering) are ported from the Svelte `lib` with their tests before any
  widget is written.
- The bridge serves the Flutter build at `/next/`.

Done when: with real agents running, Overview and Inbox agree with
`marshal fleet`, and an approval answered in the app unblocks its agent.

### F3 — the agent thread

- The transcript from the stack view model: snapshot, patches, the revision
  gap rule, streaming output into live rows. Density, folding and the browse
  keys match the TUI.
- Text selection across the transcript while it streams.
- The composer: prompt, steer, stop, permission mode, model.
- Asks pinned in the thread they belong to.
- The new-work dialog, for the Agent kind only.

Done when: a full agent session — start, steer, answer, finish — can be
driven from the app with the TUI attached to the same session and both
showing the same transcript.

### F4 — the inspector

- Changes: gate, files, diff, by-step grouping, line comments that steer.
- Shipping: commit, gate with a recorded override, push, pull request,
  patch export, discard.
- Files, Terminal (typing pauses the agent), Preview, Inspect.
- The expanded state for review, and the sheet for narrow widths.

Done when: an agent's work is reviewed and shipped as a pull request without
leaving the app.

### F5 — pull requests and CI

- A pull-request index in the bridge: every open pull request Marshal
  produced, across independent agents, factory items and automations, with
  the state of each check and review against the current head.
- The Pull requests place, the pull-request card, the review head of a ready
  thread, and pull requests on Overview and in the Inbox.
- Fix with a worker: one request that starts a worker on a failing check,
  bound to the pull request and its head. Inside a factory's repair
  allowance it is the existing repair loop; outside it, this is new.
- The variations behind the caret: investigate only, a note, the worker, and
  always fixing failures in a project.

Done when: a pull request with a failing check is sent to a worker in one
click, the fix lands on the same pull request, and the card follows it to
ready.

### F6 — the factory

- The work-item thread: stage strip, activity, and the Plan, Agents, Checks,
  Changes and Pull requests tabs.
- Planning: "Plan first", then handoff review inside that thread.
- Decisions, recovery and completion as asks.
- Plan revisions and working drafts, with their conflict rules.
- Plan runs: lanes, graph, timeline.
- The project page: the board, defaults, health, discovery.

Done when: a work item goes from a planning conversation to a ready pull
request in the app, including one decision and one repair.

### F7 — automations, Library, Settings

- Automations: bots, schedules, watches, recipes.
- Library: skills, plugins, MCP, memory with scopes and promotion.
- Settings: hosts, environments and their designer, connections, models and
  routing, secrets, network, usage and audit, notifications, access.
- Planned capabilities appear as disabled rows where they will live.

Done when: nothing in the Svelte Studio lacks a home in the app.

### F8 — cutover

- The Flutter build is served at `/`; `/next/` redirects to it.
- `web/ui`, its tests, its browser suite and the Node toolchain are removed.
  The release workflow builds the web assets with Flutter.
- `AGENTS.md`, the architecture document and the web-studio documents are
  brought up to date. The public status page stays plain HTML.
- The mock is deleted.

Done when: a release ships with no Svelte in it and the end-to-end suite
runs against the Flutter Studio.

## Testing

- **Rules live in `model/` and are tested as plain Dart.** The Svelte app
  has about 1,150 unit tests; the ones that check a rule that survives the
  redesign are ported with the rule.
- **Widgets are tested against a fake client**, never a daemon.
- **Goldens** cover the tokens and each shared widget, in light and dark.
- **One end-to-end suite per phase** drives the real app against the
  credential-free fixture bridge the browser suite already uses.
- **A new test is proved by mutation**: revert the change and watch it fail.
- Nothing in the default suite reads the developer's config, keychain or
  running daemon, as `docs/test-isolation/feature-spec.md` requires.

## Risks

| Risk | Where it is settled |
|---|---|
| Selecting text across a virtualised, streaming transcript does not work well enough in Flutter | F0 spike, before F3 is planned |
| Flutter web is too heavy on a phone over a private network, or weak for screen readers | F0 measures size; F2 puts `/next/` on a real phone |
| No adequate terminal emulator on both macOS and web | F0 spike, before F4 is planned |
| The contract migration is larger than each phase allows | Decision 2 is confirmed or reversed before F1 |
| The Svelte Studio keeps growing during the rewrite | Decision 3 |
| The daemon's P6 changes the routes or the stream mid-programme | Each plan is authored against `main` at a named commit and re-reads what it edits |
| Scope: the Svelte client is about 53,000 lines with its tests | Phases are independently shippable; the programme can pause after any of them |

## Not in this programme

- Native iOS and Android builds, and push notifications from them.
- Windows.
- Multiple users: accounts, sign-in, roles.
- Any change to what an agent does. This is a client.
- Capabilities the designs mark as planned: voice capture, the workflow
  editor, host onboarding.

## Next

Approve the design system and the phase documents, then hand F0 to the
factory. Each later phase is handed over when the one before it has landed
and its "Before starting" check has been done.
