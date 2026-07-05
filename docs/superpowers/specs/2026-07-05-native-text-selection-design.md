# Native Text Selection in the Marshal TUI

## Problem

Marshal runs Bubble Tea with `tea.WithAltScreen()` and `tea.WithMouseCellMotion()`. The mouse-motion option causes the terminal emulator to forward mouse events to the application instead of handling them natively. As a result, users cannot click-and-drag to highlight text or use the terminal's native copy/paste workflow.

## Goal

Restore native terminal text selection so users can highlight and copy text in the TUI with their standard mouse/keyboard interactions.

## Decision

Remove application-level mouse capture. This is the smallest change that solves the problem and matches Marshal's terminal-native design.

## Changes

1. **`internal/app/app.go`** — Remove `tea.WithMouseCellMotion()` from the `tea.NewProgram` call in `runProgram()`. Keep `tea.WithAltScreen()` so the full-screen TUI behavior is preserved.
2. **`internal/app/tui/model.go`** — Remove the `tea.MouseMsg` branch from `Update()`. Without mouse capture, the runtime will not deliver mouse messages, so this branch is dead code.

## Behavior

- Click-and-drag text selection is handled by the terminal emulator and works everywhere selection normally works in the terminal.
- Copy and paste use the terminal/OS clipboard workflow.
- Mouse wheel events are no longer captured by Bubble Tea. Most terminal emulators translate wheel events in the alternate screen to arrow or page keys, so scrolling is expected to keep working. Keyboard scrolling (`PgUp`, `PgDown`, `Ctrl+U`, `Ctrl+D`) is unchanged.

## Verification

- `go build ./cmd/marshal` succeeds.
- `go test ./...` passes.
- Manual check: run Marshal and confirm that dragging the cursor across transcript text highlights it and copies to the system clipboard via the terminal's copy command.

## Scope

No config changes, no new keybindings, no new dependencies.
