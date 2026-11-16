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

## Implementation record

Items 1–7 were implemented on 2026-11-17 on branch `f0-foundations`, one
commit each. Item 8 (the spikes) is handled separately.

### What was built, and how it differs from the plan

- **Toolkit (1).** Flutter 3.50.0 (Dart 3.14.0) in `pubspec.yaml` and in all
  three CI pins (`go-checks.yml`'s `app` job and both `release.yaml` app
  jobs). `material_ui` 1.11.0 and `cupertino_ui` 1.1.6 added; the tray never
  imported Material, so nothing in it switched. The 3.50 tool migrated the
  macOS runner on first build (SwiftPM plugin integration, deployment target
  10.15 → 12.0). `flutter build macos --debug` builds `Marshal.app` with
  `LSUIElement` set; it was not launched, and the Linux runner was not
  built (no Linux host).
- **Boundary (2).** `test/boundary_test.dart` replaced `no_io_test.dart`.
  It reads import and export directives, comments stripped, every branch
  of a configurable import, with relative and `package:` URIs resolved.
- **Tokens and fonts (3).** Geist 400/500/600 and Geist Mono 400 come from
  the official release v1.9.2 of `vercel/geist-font`, with `OFL.txt` beside
  them in `app/assets/fonts/`. That OFL also ships as an asset. One token
  beyond §2 of the design system: `none`, which is transparent. It is now
  in that table.
- **Kit (4).** All 21 components of design-system.md §5, plus the supporting
  pieces that §5 now lists: `EmberApp`, `Tip`, the §6 vocabulary, `Glyphs`
  and the internal `Press`. Material is used in two places only. `EmberApp`
  is a `MaterialApp` underneath, and `Field` is a Material `TextField`.
  `Menu` is `RawMenuAnchor`, `Sheet` is `showGeneralDialog`, and tooltips
  are `RawTooltip`. A sheet's barrier does not dim: §2 has no scrim token.
- **Gallery and enforcement (5).** The gallery is `lib/src/ui/gallery/` and
  its debug entry is `lib/gallery_main.dart`. The registry of screens is
  `test/support/screens.dart`. **The goldens are pinned to macOS**, which
  answers the first open question with the closest thing this host could
  check. Drawn with Geist, they rasterize through CoreText on macOS and
  FreeType on Linux, and no Linux host was available to generate or compare
  them. On any other OS they are skipped with the reason, so CI's Linux job
  does not check pixels until they are regenerated on Linux and the pin is
  flipped. The rest of the gallery test, and the four other enforcement
  tests, run everywhere.
- **Agent guidance (6).** `AGENTS.md` gains "The Studio's UI".

### Web build proof (7)

`app/web/` was created with `flutter create --platforms=web`. It has one
change of substance: `web/flutter_bootstrap.js` sets the engine's
`fontFallbackBaseUrl` to the app's own `assets/fonts/fallback/`. The
default is `https://fonts.gstatic.com/s/`.

Built with:

```sh
flutter build web -t lib/gallery_main.dart --release --no-web-resources-cdn
```

`--no-web-resources-cdn` puts CanvasKit in the build (the default fetches it
from `www.gstatic.com`) and ships the engine's Roboto fallback in
`assets/fonts/fallback/`. The build is 41 MB on disk, of which 36 MB is the
`canvaskit/` variants; a browser loads one of them.

It was served from `build/web` by a local static server that gzips
(level 9) and logs every request. It was loaded in headless Chrome with a
fresh profile and `--log-net-log`.

- **Hosts the page requested: only the serving host** (`127.0.0.1:8765`).
  The net log also shows Chrome's own traffic: `clients2.google.com`
  (clock), `accounts.google.com`, `www.google.com` and `www.gstatic.com`
  (Safe Browsing keys). Every one of those has the initiator "not an
  origin", meaning the browser and not the page. Every request the page
  made carries the initiator `http://127.0.0.1:8765`.
- **First load: 16 requests, all 200. 8,740,045 bytes uncompressed and
  3,214,314 bytes gzip on the wire (3.07 MiB).** The two largest are
  `canvaskit/chromium/canvaskit.wasm` (5,463,539 → 2,101,176) and
  `main.dart.js` (2,458,171 → 726,686). The fonts are Roboto (fallback)
  91,520 and Geist 400/500/600 with Geist Mono 400 at 249,883 together.
  Icons are tree-shaken to 3,449 (Cupertino) and 649 (Material). Brotli
  was not measured.
- **The fallback base was proved.** A build with `⌘ ✓ ▸` in the gallery
  (glyphs Geist 1.9.2 lacks) made the engine request Noto Sans Symbols 2,
  Math, KR, SC, TC and HK. Every request went to the serving host and got
  a 404. None went to another host, and those glyphs drew as missing.

### The second open question: glyph coverage

Geist 1.9.2 has `· › → − … – — ‘ ’ “ ”`. It lacks `⌘` (U+2318), `⌥`
(U+2325), `⏎` (U+23CE), `✓` (U+2713), `✗` (U+2717), `▸` (U+25B8), `▾`
(U+25BE) and `⋯` (U+22EF); Geist Mono has `⏎` and lacks the rest. On macOS
and Linux the system draws those from its own fonts. On the web they now
draw as missing rather than fetch from another host. A transcript that
needs them (shortcut keys in tooltips, tool output) needs a bundled
symbol fallback, chosen and licensed in the phase that first shows one
(F3).
