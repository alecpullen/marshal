# P0 — Text selection in the transcript

Parent: [`design.md`](design.md) §5. This spec replaces §5's sketch.
Depends on: nothing. TUI only. No change to session, agent, database or
protocol.
Status: spec agreed 2026-10-19 and revised while planning (B6, B7 and the
design section reflect the real renderers). Plan:
[`plans/2026-10-19-p0-text-selection-plan.md`](plans/2026-10-19-p0-text-selection-plan.md).

Decided:
- **Copied text:** cleaned rows (B6).
- **When it copies:** on release (B5).
- **Clicks:** drag only; there is no multi-click selection (B1).

## Goal

Drag across the transcript to select text while it scrolls and streams,
and get clean text on the clipboard, without giving up wheel scrolling.

Today the TUI runs in the alternate screen with mouse capture on
(`tui/view.go:51-69`). Native selection then needs the terminal's
override modifier, and the selection is lost when the text under it is
redrawn. Ctrl+S hands the mouse to the terminal, but then the wheel stops
scrolling.

P0 is the gate for keeping the TUI in Go (`design.md` §3).

## In scope

1. Drag selection inside the transcript viewport, anchored to the
   transcript's content rather than to screen positions.
2. Scrolling while dragging, both automatically at the edges and with the
   wheel.
3. A highlight painted when the frame is drawn.
4. Copy on release, through the existing `copyText` path.
5. Cleaned copied text: decoration removed, and truncated rows that are
   fully selected copied in full.
6. Click-to-toggle moves from press to release.
7. Shift+click extends the selection, where the terminal delivers it.
8. Esc, a click, Ctrl+S and drilling in or out all clear the selection.
9. Updated help text, hints and the Ctrl+S notice.

## Out of scope (follow-ups)

- **F1.** Rejoining soft-wrapped rows. Wrapping happens at 43 `ansi.Wrap`
  sites and inside glamour, so it needs a wrap-map contract between the
  renderers and the copier.
- **F2.** Double-click to select a word, triple-click to select a row.
  These conflict with click-to-toggle (a single click would have to wait
  out the double-click delay).
- **F3.** Code blocks in markdown are wrapped by glamour, so they copy as
  wrapped rows. Browse-mode `y` on the message copies the exact source.
- **F4.** Selection in the now bar, dock panels, the inspector, the input
  box or the status line.
- **F5.** Anchoring by node ID plus text offset, so a selection survives
  re-layout (B2 clears it today).
- **F6.** Extending the selection from the keyboard.

`tui.mouse_capture` and Ctrl+S are unchanged.

## Behaviour

### B1. Start, extend, end

- **Press.** A left-button press inside the transcript viewport records
  where it happened. Nothing is drawn yet and nothing toggles.
- **Drag.** Moving to a different cell with the button held starts a
  selection: the anchor is the press point and the head is the pointer.
  Further motion moves the head.
- **Release without a selection.** If no selection started (the release
  is on the press cell), this is a click. The existing click behaviour
  runs on release: toggle, jump, drill-in, and moving the browse cursor
  (`handleTranscriptClick`, `click.go:190`). Clicks on the now bar
  (`handleNowBarClick`) also move to release, so every left click acts at
  the same moment. A click clears any existing selection and then does
  its normal click action.
- **Release with a selection.** The selection is copied (B5) and stays
  highlighted.
- **Shape.** The selection runs like text, as in a terminal, not as a
  rectangle. It covers the start cell to the end of its row, every row in
  between, and the end row up to and including the end cell. It can be
  dragged in either direction; the endpoints are put in order before use.
- **Leaving the viewport.** If the pointer leaves the viewport during a
  drag, the head clamps to the nearest viewport cell, and B3 handles
  vertical scrolling.
- **Presses outside the viewport** (now bar, input, dock, status line)
  never start a selection.
- **Shift+click** with a selection present moves the head to the clicked
  cell and copies again. Without a selection, it is an ordinary click.
  Terminals that keep Shift+click for their own selection never send it.
  It is a convenience, not a requirement.
- **A lost release.** If a new press arrives while a drag is still in
  progress, the old drag ends without copying.
- Right and middle buttons are unchanged.

### B2. Anchoring

- **Endpoints point at content.** Each endpoint is stored as a block key,
  a row within that block, and a column. Lines added or removed in other
  blocks (streaming below, a notice, a new turn) leave the selection on
  the same text.
- **During a drag**, endpoints are re-resolved on every refresh and
  clamped to their block's rows. Nothing is cleared.
- **After release**, each refresh that changes the content re-resolves
  the endpoints. If an endpoint's block is gone, or the plain text the
  selection covers differs from what was copied, the selection is
  cleared. This covers resizing, expand and collapse, task folding,
  density changes, and live rows whose text keeps changing.
- **Other ways it clears:** drilling into or out of a subagent, Ctrl+S
  (either direction), and a config reload that turns
  `tui.mouse_capture` off.

### B3. Scrolling during a drag

- **The wheel** scrolls as it does today, including the bounded
  live-region rule (`scrollLiveRegionAt`). The head then follows the
  pointer to its new content position.
- **Edge auto-scroll.** Holding the pointer on a row above the viewport
  scrolls up one row every 50 ms and moves the head with it. When nothing
  sits above the viewport (it starts at screen row 0), the viewport's
  first row counts as the edge. Holding the pointer below the viewport
  (now bar, input, status line) scrolls down the same way. Auto-scroll
  stops at either end of the transcript, on release, or when the pointer
  comes back inside the viewport.
- There is no horizontal scrolling (the transcript scrolls vertically
  only).

### B4. Streaming

- **During a drag the transcript does not follow new output**, even if it
  was following. This hold must not change the frame. The scroll-hint
  row appears whenever `viewportFollow` is false (`model.go:3869`), so
  the hold is a separate flag that only skips `GotoBottom` in
  `refreshViewport`. `viewportFollow` is left alone, so the content never
  shifts a row under the pointer.
- **On release, following resumes as it was.** A transcript that was
  following jumps to the bottom, and the selection scrolls up with its
  text.
- **Live rows.** A selection over a live row (a spinner, streaming
  narration) copies that row's text as it was at release. B2 then clears
  the selection once the text changes.

### B5. Copying

- **On release**, the cleaned text (B6) goes through `m.copyText`
  (`browse_actions.go:87`). That tries the local clipboard first and
  falls back to OSC 52, and it shows the existing "Copied N lines" or
  "Nothing to copy" flash.
- **Every finished drag replaces the clipboard.** There is no separate
  copy key.

### B6. Copied text

These rules come from rendering a real transcript. They cover the prompt,
markdown answers with lists and code, turn separators, narrated steps,
thinking rows, file, group and failed shell rows (collapsed and expanded),
subagent cards and notices. `TestSelectedTextOfTheRenderedTranscript` locks
them against the real renderers.

A row is described by:
- the plain text of the line;
- its **segment**: the sub-region it sits in (a tool row, card or thinking
  row), else its block, else the blank line between blocks;
- whether it is a **meta head**: the first glyph row of a step block, or the
  first row of any sub-region.

Rules apply to the selected cells of each row:

1. **ANSI** escapes are stripped. Trailing whitespace is trimmed.
2. **Gutter.** Within the first 12 columns, a run of leading spaces followed
   by at most one gutter glyph and the space after it is decoration. Gutter
   glyphs are the `glyph` package's transcript glyphs (`▍ ❯ ▸ · ✓ ✗ ⚠ ? ⚙ ✎ ≡ ›
   ◈ ⧉ ◇ ● ◆ ┆ ○`) and the spinner's frames. This one rule covers every depth
   the renderers use: the top-level gutter, the rail at columns 1, 3, 5 and
   7, and a tool row's glyph at column 5. A row with no glyph is **bare**.
3. **Disclosure markers.** A trailing ` ▹` or ` ▿` is decoration.
4. **Right-aligned meta.** On a meta head whose text ends at the frame's last
   column, everything after the last run of two or more spaces is
   decoration: a step's owner, model and duration, or a card's or job's
   outcome. A row ending in `…` is never cut this way.
5. **Meta rows.** In a step block, a bare row indented 12 or more columns
   that ends at the frame edge holds only the step's meta and adds no line.
6. **Rules.** A row whose text starts or ends with `─` loses the rule. A
   turn separator adds no line, and a task divider copies as its label.
7. **Indentation.** Within each segment, the indentation shared by the
   selected pieces is removed. Bare rows and glyph rows are measured
   separately. For a bare row the shared indentation is mostly hanging
   indent; for a glyph row (after its glyph) it is content, such as a code
   block's margin.
   - Relative indentation survives: code in narration, in answers and in
     tool bodies, and test output in failed tails.
   - A piece that starts mid-row does not count, and is not dedented.
8. **Full text for truncated rows.** A collapsed tool or tool-group row
   ending in `…`, selected from its text column to its last cell, copies the
   untruncated head (`toolRowHead`, `toolGroupHead`). Step headlines are never
   truncated (they wrap), so they need no full text. Active tool rows, job
   cards and other truncating rows copy what is shown.
9. **Blank lines.** A row with nothing on it (layout spacing) adds no line
   when the previous copied line is already blank. Blank lines at either end
   of the selection are dropped. Rows are joined with `\n`.

Known limits, left for follow-ups:
- Soft-wrapped rows stay broken (F1), including an expanded tool head that
  wraps.
- Glamour's padding around inline code copies as double spaces.
- A content row inside a bare run that starts with a gutter glyph (such as
  `✓ ok` in test output) loses that glyph.

**A partly selected row** starts at the selection's start column. A start
column inside decoration is moved to the row's copy start.

**The highlight matches what is copied.** It covers selected cells from the
row's first text column to its last copyable column. Decoration and meta are
never highlighted. A fully selected truncated row is highlighted as shown and
copied in full.

### B7. Esc, keys and hints

- **Esc** clears a selection. It does so after any open popup, modal,
  inline prompt or panel has had the key, and before drilling out,
  dismissing a notice or entering browse mode. An Esc that clears a
  selection does nothing else. Approval, question, sudo and skill-gate forms
  get their keys first, as today.
- **Typing** goes to the input as usual and leaves the selection alone.
- **The Ctrl+S notice on re-capture** becomes "captured — wheel scrolls,
  drag selects and copies". The status-line hint shows `drag copy` while
  the mouse is captured, in place of `Ctrl+S select`; released, it still
  shows `Ctrl+S scroll`. There is no key cheatsheet to update: `/help` lists
  commands only.
- **Ctrl+S needs no explicit clear.** Its notice rebuilds the transcript,
  and the rebuild clears any selection once the mouse is no longer
  captured (B2).

## Design

The implementation lives in `internal/app/tui` and is pure presentation. It
does not touch policy, routing or the session, which keeps the "TUI renders
only" rule.

| File | Holds |
|---|---|
| `selection_text.go` (new) | The copier: `selRow`, `rowSpan`, `spanOf`, `selectionText`. Pure functions over plain rows |
| `selection.go` (new) | Block keys and spans, anchors, `selRowAt`, revalidation and painting |
| `selection_input.go` (new) | Press, drag, release, wheel, edge auto-scroll |
| `transcript.go`, `toolgroup.go` | Extract `toolRowHead` and `toolGroupHead`, so the copier can recover a truncated row's full text |
| `viewport_render.go` | `addBlock` records a `blockSpan` for every block. `refreshViewport` keeps `contentLines`, skips `GotoBottom` while dragging, and revalidates the selection |
| `model.go`, `click.go` | Model fields. All four mouse message types route through `handleTranscriptMouse`, from `update` and from `scrollTranscript` (panels open). Click handlers take a `tea.Mouse` and run on release. Tick routing. Drill clears |
| `view.go` | `renderTranscriptFrame` paints the visible rows |
| `keypress.go`, `help/help.go` | Esc, the Ctrl+S notice, the footer hint |

**Row facts come from data the renderer already records.** `selRowAt`
derives a row's segment and meta-head flag from `blockSpans` and
`nodeRegions`, and gets full text from `browseTree`. No renderer reports
per-row metadata. (`stepHeaderLine` in `steps.go` is dead code; the live
headline is built inline in `renderStep`.)

**Painting** uses `lipgloss.StyleRanges` with reverse video on the visible
rows of `m.viewport.View()`. Visible row `i` is content line `YOffset()+i`,
because the viewport's `SoftWrap` is off.

Reverse video replaces the `BGTextSelection` theme token this spec first
proposed. A new slot would touch the Theme struct, nine palettes, the
16-colour downsampling pairs and the contrast tests. `chrome.SelectionStyle`
already uses reverse video at 16 colours. Reverse video also reads under
`NO_COLOR` and stays distinct from the browse cursor's `BGSelection` band.

The bubbles viewport's `SetHighlights` is not used. It re-parses the whole
content on every call and scrolls to the first match.

**Cost:**
- A frame paints only the visible rows.
- The copy at release walks the covered rows once.
- Revalidation runs only when the content changes and a selection exists.

## Tests

Test names are in the plan. Each one was checked by mutation: removing the
behaviour it guards makes it fail.

| ID | Covers |
|---|---|
| T1 | Copier rules over synthetic rows (`selection_text_test.go`) |
| T2 | The whole rendered fixture transcript copies to a pinned text (`TestSelectedTextOfTheRenderedTranscript`, `TestDragCopiesTheRenderedTranscript`) |
| T3 | Click versus drag: a press alone does nothing, a click toggles on release, a click clears a selection |
| T4 | A drag copies through the clipboard path and the selection stays. Shift+click extends; a press during a drag abandons it |
| T5 | Streaming during a drag leaves the view and the frame still; following resumes on release |
| T6 | Anchors follow their text when rows change above; the selection clears when its text changes or capture turns off |
| T7 | Edge auto-scroll and its stale-tick guard; the wheel during a drag |
| T8 | Esc, Ctrl+S and drill clears, and the Ctrl+S notice |
| T9 | A `selection` variant in `TestFrameInvariants`, plus the reverse-video and gutter checks on painted rows |

## Done when

- T1–T9 pass, and the full `./internal/app/tui/...` suite passes, apart
  from the known local baseline failures.
- **Manual check.** While a turn streams, drag-select from inside the
  visible transcript to rows above it, scrolling by dragging past the edge
  and by the wheel. The pasted text must match the transcript's plain
  text under B6. Run it in:
  - iTerm2, Ghostty and Terminal.app, locally;
  - one of them over SSH, which uses OSC 52.

  For each terminal, record whether Shift+click arrives.
- **Known terminal limits** go in the manual check's notes, not the code:
  - Terminal.app has no OSC 52, so over SSH it cannot copy;
  - iTerm2 needs "Applications in terminal may access clipboard" turned
    on;
  - tmux needs `set-clipboard on`.

## Implementation record

Executed 2026-10-19 from
[`plans/2026-10-19-p0-text-selection-plan.md`](plans/2026-10-19-p0-text-selection-plan.md),
one commit per task on top of base `907c67ff` (`main`):

- `ef72ed69` — task 1, copier for selected text
- `294464f3` — task 2, untruncated tool heads
- `35f3e180` — task 3, selection state, anchoring and painting
- `6c4025ea` — task 4, mouse input — press, drag, release, wheel,
  auto-scroll
- `74ea9fff` — task 5, Esc, Ctrl+S and drill
- `a8fc4393` — task 6, footer hint
- this commit — task 7, verification and docs

**Automated verification.** `CGO_ENABLED=1 go build ./cmd/marshal`,
`go vet ./...` and the whole `./internal/app/tui/...` tree pass, and
T1–T9 pass. A full `go test ./...` run fails only on the same three tests
that failed at the base commit before any change:
`TestRunManagedCancellationKillsTree` (`internal/snapshot`),
`TestStandaloneBackendNavigateAndRead`
(`internal/tools/desktop/browser`) and
`TestCLIGitOpsCommitAllWithoutARepoIdentity` (`internal/worktree`).

**Manual terminal check: not yet performed.** The per-terminal check from
"Done when" — iTerm2, Ghostty and Terminal.app locally, one of them over
SSH; drag-select across a scroll boundary while a turn streams, the paste
compared against B6, Shift+click arrival, and the clipboard limits — is
still pending. Until its results are recorded here, that clause of "Done
when" stays open.

**Deviations from the plan:**

- The manual check above was deferred by decision during execution; every
  code change landed exactly as written.
- `TestStatusLineUntrustedIsWarningColored` passes on this machine at the
  base commit, so the plan's assumption that it fails locally is stale;
  the `-skip` flags in the verify commands are harmless.
- `gofmt -l .` on this machine lists pre-existing non-project paths (the
  module cache under `.home/go/pkg/mod` and old worktrees under
  `.marshal/worktrees/` and `.worktrees/`); project sources are
  gofmt-clean.
- Each new file needed a final trailing newline added (gofmt requires
  one); no other bytes changed.
- Tasks ran sequentially; the plan's parallel-safe pairs (1 with 2, 6
  with 1–5) were not run concurrently.
- `design.md`'s status line no longer says "Phase specs and plans are not
  yet written" (committing the P0 spec and plan made it false), and §7's
  P0 row is marked implemented with the manual check pending.
- Checked during execution, no code change: `return m,
  m.handleSelScrollTick(msg)` in the value-receiver `update` returns the
  mutated model on this toolchain (the call is evaluated before the first
  return operand is copied), so edge auto-scroll through the real event
  loop behaves as the plan intends.
