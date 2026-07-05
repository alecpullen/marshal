# Task 5: Move approval panel from viewport to input area

## Status: Complete

## Commits
- `601d40d` — feat(tui): move approval panel from viewport to input area

## Changes

### transcript.go
- Renamed `renderApprovalInline` → `renderApprovalPanel`
- Removed the internal bordered-panel wrapper (border + `\n\n` suffix)
- Removed `if width < 10` guard (input area handles minimum width)
- Function now returns bare content string (title, command, risk, help line)

### view.go
- `renderInputArea` now checks `m.state.PendingApproval()`
- When pending approval + editing: shows textarea with `❯` prompt inside input border
- When pending approval + not editing: shows `renderApprovalPanel` content inside input border
- When no pending approval (normal): activity strip + suggestions + prompt as before

### model.go
- Restructured approval key handling with proper nested edit-mode branching:
  - Edit mode (editingCommand=true): Esc cancels, Enter submits edited command, other keys pass to `m.input.Update(msg)` (instead of silently ignoring)
  - Not editing: Enter approves, Esc/d deny, e enters edit mode (pre-fills textarea), a approve+add rule, r rollback
- Added `m.lastTranscriptHash = 0` on every transition that changes approval state
- Removed unnecessary `// Ignore all other key inputs when approval prompt is shown and not editing` comment

## Build
- `go build ./internal/app/tui/` — passes
- `go test ./internal/app/tui/ -run TestView` — fails due to pre-existing `lastMessageCount` field removal in test files (expected — Task 8 handles test updates)
