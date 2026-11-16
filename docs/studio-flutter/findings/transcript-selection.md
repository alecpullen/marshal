# Transcript selection spike

Spike for `f0-foundations.md` Work item 8. Project: `spikes/transcript_selection/`
(Flutter 3.50.0, Dart 3.14, `material_ui`). Throwaway.

## Question

Can several thousand variable-height text rows be (a) built lazily, (b) appended
to several times a second while the user reads, and (c) selected across rows,
including rows scrolled out of the built window, and copied as plain text in
reading order?

## What was built

All over 5,000 rows of deterministic variable text (3 to 42 words, so 1 to
several wrapped lines), in a 800x600 viewport, appended at 4 rows/s (`append()`
every 250 ms) in the append tests.

| Id | Approach |
|----|----------|
| A | `SelectionArea` > `ListView.builder`, one `Text` per row |
| A2 | A, with each row's text ending in a near-zero-size `\n` span |
| A3 | A2, plus `AutomaticKeepAlive` on every row (built rows never disposed) |
| B | `SelectionArea` > `ListView.builder`, one `Text.rich` per turn of 10 rows (500 items) |
| C | No `SelectionArea`. Own `ModelSelectList` (`lib/model_select.dart`): selection anchors are `(row, char offset)` in the data model, a `Listener` does hit-testing on built rows via `RenderParagraph.getPositionForOffset`, edge auto-scroll by timer, each row paints its own highlight span, Cmd/Ctrl+C and Cmd/Ctrl+A build their text from the model |

## Results

Measured by `flutter test` (widget tests driving mouse-kind `TestGesture`s,
reading `SelectedContent.plainText` via `onSelectionChanged` or the mocked
clipboard) unless stated.

| Behaviour | A | A2 | A3 | B | C |
|-----------|---|----|----|---|---|
| Lazy build | works: 14 of 5000 rows built | 11 of 5000 | 11, grows as scrolled (never freed) | 2 of 500 turns | works |
| Drag across visible rows | works | works | works | works | works |
| Plain text separates rows with a newline | **fails**: `...w0_2R1 w1_0...`, one line | works with the `\n` span | works | works (the newlines are in the text) | works (`join('\n')`) |
| Selection survives 20 appends at 4/s (viewport top or tail) | works (text identical before and after) | works | works | works | works |
| Drag with edge auto-scroll to far rows | works: 36 contiguous rows from row 0, start kept although row 0 was disposed | works (28 rows) | works | works (39 rows) | works (63 rows, contiguous) |
| Wheel-style scroll in steps while button held | works: start row retained, 55 to 68 contiguous rows over 4,800 px | works | works | works | not tested |
| Start row scrolled out by a single large jump (`jumpTo(3000)`) while button held, then extended | **fails silently**: selection restarts at the first built row (R38 / R30), start lost; no error | same | same (keep-alive did not help, the row is not built when jumped past) | same, restarts at R30 turn | not applicable: anchors are model data |
| Release, scroll far away, copy | text of the original selection is still copied | same | same | same | works |
| Scroll back after a copy while away | selection was cleared in one probe (`onSelectionChanged(null)`), kept in another; inconsistent, not root-caused | same | same | same | selection kept |
| Select all (Cmd+A / `selectAll()`) | selects the **built rows only**: 11 of 5000 (1,277 chars) | same | same | same | works: 5,020 of 5,020 lines |
| Select all after an existing selection, then scroll-away | n/a | **throws** `Null check operator used on a null value` in `StaticSelectionContainerDelegate._updateLastSelectionEdgeLocationsFromGeometries` (macOS desktop run) | n/a | throws, same stack (macOS run) | not applicable |
| Select all with no prior focus | produces nothing (widget test) | | | | |

Reading the table: stock `SelectionArea` keeps a live drag working across
rows, including through continuous scrolling and auto-scroll, and survives
appends. It cannot select content that is not built (select-all, a jump past
the start row, or any non-contiguous scroll), and the bundled plain-text join
does not put newlines between separate `Text` rows.

Numbers: build window with the 800x600 viewport and the default cache extent is
about 11 to 14 rows. Append at 4 rows/s over 20 appends added no observable
cost in the tests; no frame-time numbers were taken (the widget tester does not
measure raster time).

## Verification

- Automated, `flutter test` (headless widget test, VM): all rows above for A,
  A2, A3, B and C. Files `test/selection_test.dart`, `test/model_test.dart`.
- macOS desktop (`flutter test integration_test/selection_it.dart -d macos`,
  real app, real clipboard, appends at 4/s): C passed (autoscroll plus 100
  appends in 1.6 s, 99 contiguous rows, real clipboard equal to the selection).
  A2 and B: a drag with live appends copied correct text (667 chars, 7 lines for
  B). The select-all-after-scroll exception reproduced here for A2 and B. Gestures
  are synthesized by the test binding, not real trackpad/mouse events.
- Web: `flutter build web` compiles the spike. The selection behaviour was
  **not** run in a browser (no `--platform chrome` run). Flutter web's
  `SelectionArea` is the same framework code, but browser text-selection
  semantics were not checked.
- Not verified: real human input on macOS (trackpad drag, double and triple
  click, shift-click extend, native context menu), Linux, frame times,
  accessibility, 5,000 rows with C on the web, C with rich text (links, code
  blocks, differing styles per row) or per-row widgets that are not text
  (tool cards). C was written for plain-text rows only.

## Recommendation for F3

Use approach C, model-backed selection, for the transcript list. The one
requirement that fixes the choice is "select across rows that are not built":
stock `SelectionArea` cannot do it, silently drops the start of a selection on
a long jump, selects only built rows on select-all, and can throw. The
acceptance in F3 decision 5 (select across rows with rows arriving, copy plain
text in reading order) is met by C because selection is data and copy is a
slice of the projection that already exists as the plain-text transcript, so it
also matches the F3 end-to-end assertion.

Concretely: keep anchors as `(node id, offset)` (not a row index, so appends and
folding do not move them), build the copy string from the viewmodel's plain-text
projection, paint the highlight inside each row from the anchors, and treat
rows that are not text (tool cards, diffs) as one unit with a defined plain-text
form. Cost: F3 owns hit-testing, auto-scroll, double and triple-click word and
line selection, keyboard extension, the context menu and accessibility, which
`SelectionArea` provides for free. Not proven by the spike: those extras and
rich text inside rows.

Fallback if C proves too expensive: B (one `Text.rich` per turn, newlines in the
text), accepting that select-all only covers the built window and a long jump
loses the selection start. Do not use A without the newline span.

The spike's `ModelSelectList` is a starting point, not production code.
