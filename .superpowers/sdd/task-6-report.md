# Task 6 Report: Wire Command Registry in App

## Changes Made

### `internal/app/app.go`
- Added `"marshal/internal/commands"` import
- Changed `buildAgentRunner` signature to return `*registry.Registry` alongside `*agent.Runner` (Approach A)
- Created `cmdReg` via `commands.New()` and called `commands.RegisterAll(cmdReg, toolReg, cfg.Project.Name)` after successful agent runner build
- Added `tui.WithCommandRegistry(cmdReg)` to `tuiOpts` (always, even when runner fails — so `/help` works in provider error state)

### `internal/app/tui/model.go`
- Added nil guard in `dispatchCommand` for `m.cmdRegistry` — returns early with "Command registry not available." if nil

### `internal/app/live_agent_test.go`
- Updated `newLiveAgentRunner` to use `runner, _, err := buildAgentRunner(...)` for the new 3-return signature

## Verification

- `go build ./internal/app/` — passes
- `go test ./internal/app/ -v` — all 14 tests pass
- `go test ./internal/app/tui/ -v` — all 43 tests pass
- `go build ./cmd/marshal/` — full binary build passes

## Commit

```
ec2eedb feat: wire command registry into app
```
