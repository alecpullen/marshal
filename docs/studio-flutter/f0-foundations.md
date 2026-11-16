# F0 — foundations

Status: **draft for approval**, 2026-11-17. Authored against `main` at
`f2ea2717`. Part of [the Studio programme](programme.md).

Read first: [programme.md](programme.md), [design-system.md](design-system.md),
[design.md](design.md), `app/README.md`, `docs/distribution/d3-tray-app.md`.

## Context

`app/` is a Flutter project that today is only the tray: a menu-bar item
with no window. Its code is `lib/main.dart`, `lib/src/daemon/` (`cli.dart`,
`status.dart`) and `lib/src/menu/` (`model.dart`, `render.dart`,
`host.dart`). `test/no_io_test.dart` allows `dart:io` in
`lib/src/daemon/cli.dart` only. CI's `app` job in
`.github/workflows/go-checks.yml` pins Flutter 3.35.1 and runs
`flutter pub get`, `flutter analyze`, `flutter test` and a format check.

The Studio will be built in this same project. Before any of it talks to a
daemon, the project needs the current toolkit, a layout that keeps future
work in its lane, the design system as code with tests that hold the line,
and answers to three technical questions later phases depend on.

`docs/studio-flutter/mock/` is a separate throwaway Flutter project, built
on Flutter 3.50. It is the visual reference. It is not moved; its widgets
are re-created in `app/` to the design system's rules.

## Goal

- `app/` builds and passes on Flutter 3.50, with the tray unchanged in
  behaviour.
- The layers `client/`, `model/` and `ui/` exist and a test enforces what
  each may import.
- The design system exists as code: tokens, bundled fonts, the kit, a
  gallery, and every enforcement test named in design-system.md §11.
- The web build of `app/` loads with no request to any other host.
- Three spikes have written findings.

## Non-goals

- Any request to a daemon or a bridge, and any generated contract type.
- A Studio window in the shipped app. The gallery is reachable only in
  tests and behind a debug entry.
- Changing what the tray does or how it reaches the daemon.
- Editing the mock.

## Decisions

1. **Toolkit.** Flutter stable 3.50.x, pinned exactly in CI as today's pin
   is. Material and Cupertino come from the `material_ui` and
   `cupertino_ui` packages; nothing imports `package:flutter/material.dart`
   or `package:flutter/cupertino.dart`.
2. **Layout of `app/lib/src/`.**
   - `daemon/` and `menu/` — the tray, untouched apart from imports.
   - `client/` — the bridge client. Empty in F0 apart from a README stating
     its rule.
   - `model/` — pure Dart stores and view models. Empty in F0 likewise.
   - `ui/tokens.dart` — the Ember tokens and the named text roles.
   - `ui/kit/` — the components in design-system.md §5, one file each, and
     `kit.dart` exporting them together with the parts of Flutter's widgets
     layer a screen may use.
   - `ui/gallery/` — one screen showing every kit component in every
     variant and state.
3. **The boundary rule, rewritten.** It replaces the one-file rule in
   `app/README.md`, in `AGENTS.md` (the "Native app" entry) and in
   `d3-tray-app.md`:
   - `dart:io` may be imported only by `lib/src/daemon/cli.dart` and by
     files under `lib/src/client/` whose name ends `_io.dart`.
   - Browser APIs may be imported only by files under `lib/src/client/`
     whose name ends `_web.dart`.
   - `lib/src/model/` imports neither Flutter nor `dart:io` nor a browser
     API.
   - `lib/src/ui/` never imports `lib/src/client/`.
   - Material and Cupertino may be imported only under `lib/src/ui/kit/`,
     in `lib/src/ui/tokens.dart`, and by the tray's existing files.
   - The tray still learns everything from `marshal daemon status --json`
     and the commands in its menu. That part of the old rule stands.
4. **Fonts.** Geist and Geist Mono are bundled as assets under the SIL Open
   Font License, with the licence file beside them. Bundling must also
   cover whatever fallback the web engine would otherwise fetch.
5. **The kit is re-created, not copied.** The mock uses sizes and spacings
   outside the design system's scales. Each component is written to the
   scales and compared by eye with the mock.
6. **D3's unix-socket statement is corrected.** `d3-tray-app.md` says Dart's
   `HttpClient` cannot use a unix socket. A spike on 2026-11-17 showed
   `HttpClient.connectionFactory` making a request and reading a streamed
   response over one. Amend the statement and say the tray keeps its CLI
   path by choice.

## Work

Suggested pull requests, in order. Each leaves the suite green.

1. **Toolkit.** Move the CI pin and `pubspec` to Flutter 3.50; switch the
   tray's imports to `material_ui`; refresh `pubspec.lock`; fix what the
   newer analyzer and formatter report. The tray's four existing test files
   pass unchanged in meaning.
2. **Layers.** Create the directories and the two READMEs. Replace
   `no_io_test.dart` with `boundary_test.dart` enforcing decision 3, keeping
   the existing check that the exempt file exists. Update the three
   documents that state the rule, and amend D3 per decision 6.
3. **Tokens and fonts.** `tokens.dart` with the values in design-system.md
   §2–§4 and the text roles in §3, resolving light and dark. Bundle the
   fonts. Add `contrast_test.dart`.
4. **The kit.** Every component in design-system.md §5, each with a widget
   test of its variants and states.
5. **Gallery and enforcement.** The gallery, its goldens in light and dark,
   and `design_rules_test.dart`, `tooltips_test.dart`, `states_test.dart`,
   `gallery_test.dart` and `overflow_test.dart`. Where a test needs screens
   to exist, it is written against the gallery now and takes a registry of
   screens that later phases add to.
6. **Agent guidance.** Add to `AGENTS.md` a short section pointing any work
   under `app/lib/src/ui/` to design-system.md and naming the tests.
7. **Web build proof.** Build `app/` for the web with the gallery as its
   entry, serve it from a local directory, and record in this document's
   implementation record: the hosts requested during load (must be only the
   serving host) and the compressed bytes transferred on first load.
8. **Spikes.** Each is a throwaway outside `app/lib/`, with its finding
   written to `docs/studio-flutter/findings/`:
   - `transcript-selection.md` — a list of several thousand variable-height
     text rows that is virtualised, has rows appended several times a
     second, and lets the user select text across rows and copy it, on
     macOS and web. State what works, what does not, and the approach F3
     should take.
   - `tray-and-window.md` — one macOS app that is a menu-bar item with no
     Dock icon while no window is open, and a normal app with a Dock icon
     while one is. State the mechanism and whether Linux needs anything.
   - `terminal.md` — a terminal emulator widget fed a byte stream and
     sending keystrokes, on macOS and web. Name the package and version, or
     state that none is adequate and what writing one would involve.

## Tests

By name, in `app/test/`: `boundary_test.dart`, `contrast_test.dart`,
`design_rules_test.dart`, `tooltips_test.dart`, `states_test.dart`,
`gallery_test.dart`, `overflow_test.dart`, one `kit/<component>_test.dart`
per component, and the tray's existing tests.

Each new enforcement test is proved by mutation before it is trusted: add a
violation, watch the test fail, remove it.

## Done when

- CI's `app` job passes on Flutter 3.50 and the tray behaves as before on
  macOS and Linux.
- A deliberately added hex colour, raw text style, off-scale padding,
  Material import in a screen file, `dart:io` import in `model/`, or
  tooltip-less icon button each fails its test.
- The gallery renders every kit component in light and dark and matches its
  goldens.
- The web build loads with requests to the serving host only, and the
  transferred size is recorded.
- The three findings are written, each ending in a recommendation.

## Open questions

- Whether goldens are stable across macOS and Linux runners. If they are
  not, pin them to the Linux runner CI uses and say so in the test.
- Whether Geist's glyph coverage needs a bundled fallback for symbols used
  in transcripts.
