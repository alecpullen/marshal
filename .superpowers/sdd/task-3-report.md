# Task 3 Report: Polish Chat Transcript, Input, and Thinking Blocks

## Implementation Notes
- Replaced transcript row rendering with `renderMessage`, using lower-case role labels, role-specific styling, fixed label width, and wrapped body content.
- Updated `refreshViewport` to render completed messages through `renderMessage` while preserving existing thinking summary behavior.
- Polished the live thinking box header copy to `thinking` plus `streaming · Ctrl+G expands history`.
- Replaced the inline approval stub in `renderChatPanel` with `renderApprovalArea(tc)` and extracted the prior approval rendering body into that helper to preserve existing approval behavior.
- Updated the input area to render a cyan `❯ ` prompt before the text input view and suppressed the textinput default prompt so the prompt is not duplicated.
- Compressed generic key-help labels so the required `Ctrl+G thinking` help copy remains visible at common terminal widths.
- Updated stale layout assertions in `model_test.go` to match the current polished shell chrome and line-count handling.

## TDD RED/GREEN Evidence
### RED
- Added `TestPolishedTranscriptShowsRolesThinkingAndInput`.
- Ran `go test ./internal/app/tui -run TestPolishedTranscriptShowsRolesThinkingAndInput -v`.
- Observed expected failure: `View() missing "Ctrl+G thinking"` with the old transcript/help rendering.

### GREEN
- Implemented the Task 3 rendering changes in `internal/app/tui/model.go`.
- Re-ran `go test ./internal/app/tui -run 'TestPolishedTranscriptShowsRolesThinkingAndInput|TestPolishedViewFitsCommonTerminalSizes' -v`.
- Result: PASS.

## Tests and Results
- `go test ./internal/app/tui -run TestPolishedTranscriptShowsRolesThinkingAndInput -v` -> FAIL before implementation, as expected.
- `go test ./internal/app/tui -run 'TestPolishedTranscriptShowsRolesThinkingAndInput|TestPolishedViewFitsCommonTerminalSizes' -v` -> PASS.
- `go test ./internal/app/tui` -> PASS.

## Files Changed
- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`
- `.superpowers/sdd/task-3-report.md`

## Self-Review
- Confirmed the scope stayed inside the two owned source files plus the required report file.
- Confirmed approval rendering behavior remains functionally the same after extraction into `renderApprovalArea`.
- Confirmed the transcript test exercises the exact Task 3 polish points called out in the brief.
- Confirmed package tests pass after aligning older stale assertions with the current shell layout.

## Concerns
- The brief referenced `renderApprovalArea(tc)` as pre-existing, but this worktree did not contain that helper. I added it by extracting the prior inline approval rendering to satisfy the required `renderChatPanel` shape without expanding Task 5 behavior.
- The help copy outside the required `Ctrl+G thinking` label was shortened to keep the polished layout within the tested terminal sizes.
