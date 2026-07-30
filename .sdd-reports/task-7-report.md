# Task 7 Report: Todo Panel Header + Strikethrough

## What was implemented

1. **Modified `TestTodoPanelExpandedUsesStatusGlyphs`** — reversed the prior "no header" assertion to assert the new "tasks 1/6" header IS present. Changed from `sampleTodos(3,1,1)` to 6-item inline list to ensure all three glyph types are visible within the clipped window (header consumes 1 row of budget).

2. **Added 3 new tests**:
   - `TestTodoPanelHeaderTracksCounts` — verifies the header shows correct counts (e.g. "tasks 2/5") and is the first line.
   - `TestTodoLineStrikethroughWhenDone` — verifies completed items use strikethrough SGR code.
   - `TestTodoLineNoStrikethroughWhenPending` — verifies pending items do NOT use strikethrough.

3. **Implemented header in `renderTodoPanelBody`** — replaced the final `chrome.ClipLines(lines, focus, budget, theme.Current())` call with a header line (`mutedStyle().Render("tasks N/M")`) followed by `chrome.ClipLines(lines, focus, max(budget-1, 1), theme.Current())`.

4. **Updated `todoLine` completed case** — added `.Strikethrough(true)` to the label style for `TodoCompleted` status.

5. **Fixed `TestTurnSpinnerSitsAboveTodos`** — added 2 more todo items so the in-progress item is visible within the clipped window (header consumes 1 row). Changed search target from "first task" to "second task" (the in-progress item).

## Test results

```
go test ./internal/app/tui/ -run 'TestTodo' -v
--- PASS: TestTodoLineFitsWidthWithWideRunes (0.00s)
--- PASS: TestTodoPanelEmptyForNoTodos (0.00s)
--- PASS: TestTodoPanelHiddenMode (0.00s)
--- PASS: TestTodoPanelExpandedUsesStatusGlyphs (0.00s)
--- PASS: TestTodoPanelHeaderTracksCounts (0.00s)
--- PASS: TestTodoLineStrikethroughWhenDone (0.00s)
--- PASS: TestTodoLineNoStrikethroughWhenPending (0.00s)
--- PASS: TestTodoPanelCollapsedIsOneLine (0.00s)
--- PASS: TestTodoPanelAllDoneCollapsesToSummary (0.00s)
--- PASS: TestTodoPanelClipsToBudgetAndKeepsInProgressVisible (0.00s)
--- PASS: TestTodoPanelRespectsShortFrames (0.00s)
--- PASS: TestTodoPanelFitsWidth (0.00s)
--- PASS: TestTodoSignatureChangesWithContentAndStatus (0.00s)
--- PASS: TestTodoPanelIsPinnedBelowTranscript (0.01s)
PASS

go test ./internal/app/tui/ 2>&1 | tail -3
ok  	marshal/internal/app/tui	4.718s

go build ./...
(no output — success)
```

## Files changed

- `internal/app/tui/todos.go` — header implementation + strikethrough style
- `internal/app/tui/todos_test.go` — modified existing test + 3 new tests
- `internal/app/tui/view_test.go` — adjusted `TestTurnSpinnerSitsAboveTodos` for header row consumption

## Self-review findings

- The strikethrough SGR code (9) is combined with the foreground color as `\x1b[90;9m` (FGMuted + strikethrough). The test checks for `;9m` or `[9m` to handle both combined and standalone forms.
- The header consumes 1 row from the body budget via `max(budget-1, 1)`. The `budget < 2` guard earlier in the function already degrades to the one-liner before this return point, so the header is only added when there is sufficient space.
- `TestTurnSpinnerSitsAboveTodos` needed 2 more items to keep the in-progress item visible within the clipped window.

## Concerns

None.
