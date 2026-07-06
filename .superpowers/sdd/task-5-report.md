# Task 5: session.SwarmProgress live state

## Status
Completed.

## What changed
- Added `SwarmRoleStatus` constants: `pending`, `active`, `done`, `failed`.
- Added `SwarmRole` and `SwarmProgress`.
- Added mutex-protected `State` accessors:
  - `SetSwarmProgress`
  - `SwarmProgress`
  - `UpdateSwarmRole`
  - `ClearSwarmProgress`
- Added `swarmProgress` to `State`.
- Added tests for copy semantics, role updates, clearing, and concurrent updates.

## TDD evidence

RED:
```
go test ./internal/app/session/ -run Swarm -v
```
Failed as expected with undefined `SwarmProgress` / accessor symbols.

GREEN:
```
go test ./internal/app/session/ -run Swarm -v
```
Passed:
- `TestSwarmProgressSetAndCopy`
- `TestUpdateSwarmRole`
- `TestClearSwarmProgress`
- `TestSwarmProgressConcurrentUpdates`

Race check:
```
go test -race ./internal/app/session/ -run TestSwarmProgressConcurrentUpdates
```
Passed.

Additional pre-commit check:
```
go vet ./...
```
Passed.

## Files changed
- `internal/app/session/session.go`
- `internal/app/session/swarm_progress.go`
- `internal/app/session/swarm_progress_test.go`

## Commit
`7a2a388 feat(session): add SwarmProgress live run state`

## Self-review
The implementation mirrors the existing `Activity`/`Plan` state style: all access is under `State.mu`, and `SwarmProgress()` returns a copy so callers cannot mutate internal slices.

## Concerns
None.
