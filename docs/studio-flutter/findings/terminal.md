# Finding: terminal widget

Spike date 2026-11-17. Flutter 3.50.0, Dart 3.14.0. Spike project: `spikes/terminal/`.

## Question

Is there a Flutter terminal emulator widget that takes an output byte stream
(ANSI/VT, colours, cursor movement, alternate screen), sends keystrokes and
resize events back, takes the Ember colours and a bundled mono font, and runs
on macOS desktop and the web? (Needed by `f4-inspector.md` decision 9.)

## Recommendation

**Use `xterm` 4.0.0 (pub.dev, publisher terminal.studio, MIT), pinned exactly: `xterm: 4.0.0`.**
It is adequate. It is pure Dart (no `dart:io`/`dart:ffi` in `lib/`), tagged for
all six Flutter platforms, and passed every capability test below on macOS
desktop and in Chrome. Risk: the last release is 2024-02-27, so it is
effectively unmaintained; it still resolves and runs on Flutter 3.50. Pin it
and be ready to vendor it (about 40 small Dart files, MIT). Writing our own is
not justified.

## Candidates

| Package | Version / date | Platforms (pub.dev tags) | Verdict |
|---|---|---|---|
| `xterm` | 4.0.0, 2024-02-27; sdk `>=3.0.0 <4.0.0`, flutter `>=3.19.0`; 240 likes | android, ios, linux, macos, windows, web; wasm-ready | **Chosen.** Tested here |
| `kterm` | 1.6.0, 2026-10-22; 3 likes | all six | Newer, but a young, little-used package (a fork-like of xterm). Not tested. Watch as a fallback |
| `terminal_view` | 0.6.1, 2026-11-14; 2 likes | all six | "built for mobile"; very new, 2 likes. Not tested |
| `ghostty_vte` 0.1.3 | 2026-05-02 | not Flutter-widget-tagged | Native/FFI parser; not a web-capable widget. Not pursued |
| `flutter_pty` 0.4.2 | 2025-01-06 | native | Spawns a local pty; irrelevant, our shell runs in the bridge |

Material is in `material_ui` on this Flutter (`package:flutter/material.dart` is
deprecated); xterm itself needs only `flutter/widgets`, so it is unaffected. The
spike uses `material_ui`.

## What was tested, and how

All in `spikes/terminal/test/widget_test.dart` unless noted. The canned stream
(`cannedStream()` in `lib/main.dart`) covers colours, cursor movement, wrap,
2000 lines of scrollback and an alternate-screen program.

| Capability | Result | Verified by |
|---|---|---|
| SGR colours: 16-colour, 256-colour, truecolor, bold | Stored per cell correctly | VM unit test |
| Cursor movement (CUB, CUF, CUP, erase line) | Correct | VM unit test |
| Line wrap (120 chars at 80 cols) | Wraps, wrapped flag set | VM unit test |
| Scrollback (5000 lines, `maxLines: 3000`) | Capped at 3000, oldest evicted, newest present | VM unit test |
| Alternate screen (`?1049h/l`) | Alt buffer used, main buffer restored on leave | VM unit test |
| Keystrokes out | Enter `\r`, Ctrl-C `\x03`, ArrowUp `\x1b[A`, Escape `\x1b` emitted via `Terminal.onOutput` | widget test (VM and Chrome); macOS integration test |
| Typed text (text-input path) | `hi` emitted | macOS integration test only (`sendKeyEvent` in the VM does not drive text input) |
| Resize | `TerminalView` reports `cols x rows` through `Terminal.onResize` on layout change (66x23 to 41x17 in test) | widget test (VM and Chrome); macOS integration test |
| Theming | `TerminalTheme` takes all 16 ANSI colours plus fg, bg, cursor, selection; `TerminalStyle` takes size, line `height`, family and fallbacks. `lib/ember.dart` maps the Ember dark tokens | asserted on the widget's properties; analyze clean |
| Split UTF-8 across chunks | See below | unit test |

Commands run, all green: `flutter analyze` (no issues), `flutter test` (8
tests), `flutter test --platform chrome` (8 tests), `flutter build web`,
`flutter test integration_test -d macos` (builds the debug macOS app, runs it,
1 test passes).

## Verification limits

- **Verified on macOS desktop** (integration test in a real debug `.app`): the
  widget builds, renders, takes the canned stream and enters the alternate
  screen, emits ArrowUp/Ctrl-C and typed text, and reports its size.
- **Verified in Chrome** (`flutter test --platform chrome`, plus a successful
  `flutter build web`): the same widget tests; not a visual check, and I did not
  drive it in a browser by hand.
- **Not verified**: pixel output (no screenshot was compared), so the colours
  are asserted in the cell model and the theme object, not as painted pixels;
  Geist Mono rendering (the font is not bundled in this spike or in `app/` yet,
  so `fontFamily: 'Geist Mono'` falls back to Menlo; the API for a custom
  family is proven, the glyph metrics are not); mouse selection and copy,
  IME composition, bracketed paste, and performance under heavy output; a live
  stream against a running bridge; Linux.

## Fit with the bridge's terminal routes

(`web/bridge/terminal.go`, client in `web/ui/src/lib/api.ts` and
`web/ui/src/lib/dock/Terminal.svelte`.)

- `POST /api/agents/{id}/terminal` with `{cols, rows}` returns `{terminalId}`.
  Open it with the widget's first reported size.
- `GET .../terminal/{tid}/events` is SSE. Each message is JSON, either
  `{"data": "<base64>"}` (output) or `{"exit": n}`. The bridge replays retained
  output from id 0, so the client does not resume (`resume: false`); reopening
  the tab redraws from the start. `lib/bridge_stream.dart` decodes this.
- `POST .../input` takes `{"data": base64(text)}`, at most 64 KiB; the web
  client sends in 16 KiB pieces, strictly in order, one request at a time, and
  holds keystrokes typed before `terminalId` arrives. `inputBodies()` does the
  chunking; the Flutter client must keep the same ordering queue.
  `Terminal.onOutput` gives the text to send.
- `POST .../resize` with `{cols, rows}`: wire to `Terminal.onResize`. The bridge
  retries its stty, so calling on every layout change is fine.
- `POST .../release` (hand back) and `DELETE .../{tid}` are plain calls with no
  widget involvement. Pause-on-typing hooks on `onOutput`.
- **One real gotcha:** `Terminal.write` takes a `String`, not bytes. A shell's
  output can split a multi-byte UTF-8 character across two SSE chunks; decoding
  each chunk on its own corrupts it (the unit test shows this). Decode through
  one long-lived `Utf8Decoder` chunked conversion per terminal. Decoding is lossy
  for invalid bytes, which is acceptable.
- The Svelte client uses the same shape with xterm.js, so no bridge change is
  needed. The SSE client itself belongs to f1 (web needs a browser-capable
  stream; `dart:io` is unavailable there).

## Theming and font

`TerminalTheme` is a plain const class of colours, so every value comes from the
Ember tokens (the design system forbids colour values outside `tokens.dart`;
the spike's `ember.dart` is a throwaway copy). Map: `background=bg`,
`foreground=fg`, `cursor=accent`, `selection=accent at 18%`, red/green/yellow =
`err`/`ok`/`warn`, bright black = `faint`. Blue, magenta and cyan have no Ember
token: the design system will need to add three terminal-only ANSI colours (the
spike invented placeholders) or decide that those programs' blue/magenta/cyan map
to `muted`/`accent`/`fg`. Dark and light each need a theme object; swap on
brightness. The font is `TerminalStyle(fontFamily: 'Geist Mono', fontSize: 12,
height: 1.45)`, matching the `mono` role; the font must be declared in the app's
pubspec. xterm measures a cell from the font at layout time, so the cell size
follows the font.

## If xterm becomes a problem

Vendor it into `app/` (MIT). If that is untenable, writing a replacement means:
a VT500-class parser (CSI, OSC, DCS, modes including 1049 alt screen, mouse
reporting, bracketed paste), a cell buffer with scrollback and wrap/reflow on
resize, a canvas renderer with wide-character and font-fallback handling, input
encoding (kitty or legacy key sequences), selection, IME and a11y. That is
months of work for a result no better than xterm; do not do it.
