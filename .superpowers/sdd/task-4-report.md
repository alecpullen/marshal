# Task 4 Report: Remove "· Xs ago" from completed tool calls

**Status: ✅ Already Complete**

The function `renderCompletedToolCall` in `internal/app/tui/transcript.go:438` was already updated by the prior commit `e66f977` (feat(tui): unified transcript timeline). The signature already has no `now` parameter, and the body contains no elapsed calculation or "· Xs ago" suffix. The function body matches the desired output exactly.

## Verification

- `go build ./internal/app/tui/` — passes
- `go test ./internal/app/tui/ -v -count=1` — build failures are pre-existing (`lastMessageCount` undefined in `model_test.go:1633` and `view_test.go:113`), unrelated to this task
- No `time` import change needed (still used by `renderThinkingSummary`, `renderActiveToolCall`, and `formatElapsed`)

## Commits

No changes required — task was already satisfied by the previous commit.
