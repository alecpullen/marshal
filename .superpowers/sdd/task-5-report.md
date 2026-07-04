# Task 5 Report: Extend AgentRunner Interface and Add Command Dispatch to TUI

## Status: Complete

## Changes Made

### `internal/app/tui/model.go`
1. **Imports** — Added `"marshal/internal/commands"` and `"marshal/internal/llm/routing"`
2. **AgentRunner interface** — Added `SetForceClass(class string)` method
3. **Model struct** — Added `cmdRegistry *commands.Registry`, `agentCancel context.CancelFunc`, `forceMode string` fields
4. **WithCommandRegistry option** — New option function following `WithConfigReloader` pattern
5. **Enter key handler** — Slash-commands intercepted BEFORE busy check: `/`-prefixed input calls `m.dispatchCommand(value)`. Agent context now created with `context.WithCancel(m.ctx)` for cancel support
6. **dispatchCommand method** — Full switch on `cmd.Name`:
   - `exit`/`quit` — `m.state.Shutdown()` + `tea.Quit`
   - `settings` — creates settings model overlay
   - `memory` — creates memory model overlay (guarded by nil DB check)
   - `stop` — calls `m.agentCancel()` if non-nil
   - `ask` — calls `m.runner.SetForceClass("question")`
   - `edit` — calls `m.runner.SetForceClass("edit")`
   - `auto` — calls `m.runner.SetForceClass("")`
   - `model` — validates preset from `newCfg.Models.Presets`, creates "switched" profile, calls `m.configReloader`
   - `default` — refreshes viewport
7. **agentFinishedMsg handler** — Added `m.agentCancel = nil` reset

### `internal/app/tui/model_test.go`
- Added `SetForceClass(string) {}` to `fakeAgentRunner` and `streamingRunner` test fakes

## Test Results
- Build: ✅ `go build ./internal/app/tui/`
- Tests: ✅ All 47 tests pass

## Verification
- Slash commands work even when `m.busy` is true (intercepted before busy check)
- Agent cancellation via `/stop` uses `context.WithCancel`
- Cancel function is reset on `agentFinishedMsg`
- Config reloader updates `m.state.Config` internally (confirmed in `app.go:253`)
