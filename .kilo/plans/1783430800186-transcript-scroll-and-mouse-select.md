# Transcript Scroll & Mouse Selection Fix

## Context

Two regressions in the TUI transcript:

1. **Scroll no longer sticks.** `refreshViewport()` ends with `m.viewport.GotoBottom()` (model.go:683). It is called on every `agentTickMsg` (every 150 ms while an agent turn is in flight), on `agentFinishedMsg`, on most key handling paths, and on resize. So the moment the user scrolls up with PgUp/Ctrl+U, the next tick (or any transcript mutation) yanks the viewport back to the bottom. Tests (`TestPageKeysScrollViewport`, `TestCtrlUCtrlDScrollViewport`) only pass because they run a single `Update` with no subsequent `refreshViewport`.

2. **Mouse selection/copy doesn't work.** `runProgram` (app.go:588) enables `tea.WithMouseCellMotion()`. With mouse cell-motion mode on, the terminal intercepts mouse drag events and sends them to the program as `tea.MouseMsg` rather than letting the terminal emulator perform native text selection. The Model's `Update` never handles `tea.MouseMsg`, so drag-select does nothing AND the OS-level selection is suppressed. This is the classic Bubble Tea alt-screen + mouse-mode tradeoff: enabling mouse for wheel-scroll disables the terminal's native click-drag selection.

User decision: when the agent emits new content while the user is scrolled up, **stay scrolled** (manual re-follow via a key), with a visible "scrolled up" hint.

## Root causes (confirmed against code)

- `internal/app/tui/model.go:683` — unconditional `GotoBottom()` in `refreshViewport()`.
- `internal/app/tui/model.go:696-699` — `tickCmd` every 150 ms drives `refreshViewport()` while busy.
- `internal/app/tui/model.go:455-464` — scroll keys (PgUp/PgDn/Ctrl+U/Ctrl+D) update the viewport but a following `refreshViewport()` (from any message) resets it.
- `internal/app/app.go:588` — `tea.WithMouseCellMotion()` enables mouse capture for wheel events but blocks native drag-to-select.
- `internal/app/tui/model.go` `Update` — no `tea.MouseMsg` case, so wheel events are dropped (the viewport never receives them).

## Design decisions

### D1. Follow mode (auto vs manual)
Add a `viewportFollow bool` field (default true). `refreshViewport()` calls `GotoBottom()` **only when `viewportFollow` is true**. When the user scrolls up via any scroll key, set `viewportFollow = false`. When the user scrolls back to the bottom (via a scroll-down key) or explicitly re-engages follow, set `viewportFollow = true`.

This is the standard "less/pager auto-follow" model and matches the user's chosen behavior.

### D2. Re-follow triggers
- Any scroll-down key (PgDn / Ctrl+D / wheel-down) that lands the viewport at `AtBottom()` sets `viewportFollow = true`.
- A dedicated re-follow key: **`Ctrl+G`** is currently bound to `thinkingExpanded` toggle. Reuse is risky. Instead add **`End`** key → `m.viewport.GotoBottom(); viewportFollow = true`. This is the conventional "jump to bottom" binding.
- Starting a new user submission (`tea.KeyEnter` → agent turn) sets `viewportFollow = true` and `GotoBottom()` — the user clearly wants to see the new turn.

### D3. "Scrolled up" hint
Render a one-line hint above the transcript when `!viewportFollow && viewport.TotalLineCount() > viewport.Height`:
```
↑ scrolled (n lines below) — End to follow
```
Use `mutedStyle` (gray 244). Render it inside `renderTranscriptFrame()` or `View()` by prepending it to the viewport output and reducing the viewport height by 1 while shown (so it doesn't overwrite transcript content). Keep it simple: render it as a leading line and `Height(viewport.Height-1)` on the viewport style when the hint is visible.

### D4. Mouse wheel scroll vs native selection
Two options for restoring copy:

- **Option A (recommended): Enable mouse capture only for wheel events, route to viewport.** Keep `WithMouseCellMotion()` (needed so wheel events arrive as `MouseMsg`), handle `tea.MouseMsg` in `Update` by forwarding to `m.viewport.Update(msg)`. Native click-drag selection remains blocked in cell-motion mode — this is a known Bubble Tea limitation. To allow copy, add **`Shift`** as an escape: most terminals, when the user holds `Shift` while dragging, bypass the app's mouse reporting and perform native selection. This requires no code (terminal-level behavior) but must be communicated to the user via the hint or docs. Bubble Tea's mouse parser already ignores shift-drag (it never sends a `MouseMsg` for shift-drag because the terminal doesn't report it), so the OS selection works.

- **Option B: Disable mouse capture entirely (`WithMouseCellMotion` off), rely on keyboard scroll only.** Restores full native selection but loses mouse-wheel scrolling. Simpler and removes the entire mouse-message code path.

**Chosen: Option A.** Mouse-wheel scroll is valuable; shift-drag gives copy back. Document the shift-drag trick in the scrolled-up hint's tooltip-style line and in CLAUDE.md if desired. If the user later finds shift-drag unreliable on their terminal, fall back to Option B by deleting the `WithMouseCellMotion` option and the `MouseMsg` case.

### D5. Arrow keys / j-k for scroll
Currently `KeyUp`/`KeyDown` are consumed by command-suggestion navigation (model.go:465-472) and fall through to the textarea. Add `j`/`k` scroll? **No** — out of scope; keep existing key bindings. Only add the `End` key and the mouse-wheel path. (PgUp/PgDn/Ctrl+U/Ctrl+D already exist and work once D1 is in place.)

## Tasks

1. **`internal/app/tui/model.go`** — add `viewportFollow bool` field; set `true` in `New()`.
2. **`internal/app/tui/model.go` `refreshViewport()`** — guard `GotoBottom()` with `if m.viewportFollow { m.viewport.GotoBottom() }`. Keep `SetContent` always (content must refresh). When `!viewportFollow`, `SetContent` already clamps `YOffset` if past end (viewport.go:130-132) so the offset is preserved as long as content didn't shrink below it.
3. **`internal/app/tui/model.go` `Update` KeyMsg** — in the scroll key handlers:
   - `tea.KeyPgUp`, `tea.KeyCtrlU`: after `m.viewport.Update(msg)` / `HalfViewUp()`, set `m.viewportFollow = false`.
   - `tea.KeyPgDown`, `tea.KeyCtrlD`: after scrolling, if `m.viewport.AtBottom()` set `m.viewportFollow = true`.
   - Add `tea.KeyEnd`: `m.viewport.GotoBottom(); m.viewportFollow = true`.
   - On `tea.KeyEnter` submit path (model.go:477): set `m.viewportFollow = true` before dispatching the turn.
4. **`internal/app/tui/model.go` `Update`** — add a `case tea.MouseMsg:` (top-level switch, before the KeyMsg case) that forwards to `m.viewport, cmd = m.viewport.Update(msg)`, then applies the same follow-toggle logic as scroll keys (wheel-up → `viewportFollow=false`; wheel-down at bottom → `true`). Return `m, cmd`.
5. **`internal/app/tui/model.go` `agentFinishedMsg`** — do **not** force `GotoBottom`. Let `refreshViewport()` honor `viewportFollow`. (Leave the existing `refreshViewport()` call; with D2 it only re-bottoms when following.)
6. **`internal/app/tui/view.go` `renderTranscriptFrame()`** — when `!m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height`, prepend the hint line and reduce the viewport render height by 1. E.g.:
   ```go
   hint := mutedStyle.Render("↑ scrolled — End to follow")
   h := m.viewport.Height
   if !m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height {
       return hint + "\n" + lipgloss.NewStyle().Width(...).Height(max(h-1,1)).Render(m.viewport.View())
   }
   ```
7. **`internal/app/app.go` `runProgram`** — keep `tea.WithMouseCellMotion()` (per D4 Option A). No change unless we choose Option B.

## Validation

- `go build ./...`
- `go test ./internal/app/tui/...`
- New/updated tests in `model_test.go`:
  - `TestScrollUpDisablesFollow`: add 100 messages, `refreshViewport`, PgUp → assert `viewportFollow==false` and `YOffset < bottom`. Send an `agentTickMsg` → assert `YOffset` unchanged (not yanked to bottom).
  - `TestScrollDownReEnablesFollow`: from scrolled-up state, PgDn until `AtBottom()` → assert `viewportFollow==true`.
  - `TestEndKeyReFollows`: scrolled up → `tea.KeyEnd` → `viewportFollow==true` and `AtBottom()`.
  - `TestMouseWheelScrollsViewport`: send `tea.MouseMsg{Button: MouseButtonWheelUp, Action: MouseActionPress}` → `viewportFollow==false`, `YOffset` decreased.
  - `TestSubmitReEnablesFollow`: scrolled up → submit a message → `viewportFollow==true`.
- `TestPageKeysScrollViewport` / `TestCtrlUCtrlDScrollViewport` already pass; keep them (they now also leave follow=false, which doesn't break their assertions).
- Manual: run `marshal`, start a turn, scroll up with PgUp — viewport stays put, hint line appears. Press End — snaps to bottom. Shift-drag to select text in terminal — native selection works.
- `gofmt -w .` and `go vet ./...`.

## Risks

- **Shift-drag copy is terminal-dependent.** iTerm2, kitty, Alacritty, Terminal.app all support it, but if the user's terminal doesn't, fall back to Option B (delete `WithMouseCellMotion` + the `MouseMsg` case). Flag this in the hint.
- **`SetContent` while scrolled up**: viewport preserves `YOffset` unless content shrank past it. Long transcript growth is fine (maxYOffset grows). If a turn *removes* lines (rare — rollback re-render), the viewport clamps to the new bottom. Acceptable.
- **Hint height accounting**: `inputAreaRows`/`updateViewportHeight` compute viewport height from total rows. The hint must not steal from the viewport's allocated height incorrectly — easiest is to render the hint *inside* the viewport's height budget (reduce `m.viewport.Height` by 1 when the hint is visible). Implement by adjusting `updateViewportHeight` to subtract 1 when `!viewportFollow && content overflows`, OR render hint as part of `renderTranscriptFrame` with its own height and let the viewport render 1 row shorter. Choose the latter to keep `inputAreaRows` unchanged.