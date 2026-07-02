# Task 3 Report

## Scope

Added session route metadata storage and surfaced the active route in the TUI status area without changing the surrounding panel structure.

## Test-first sequence

### Added failing tests first

- `internal/app/session/session_test.go`
  - `TestStateActiveRouteStoresCopies`
- `internal/app/tui/model_test.go`
  - `TestViewShowsInactiveRouteByDefault`
  - `TestViewShowsActiveRoute`

### Focused red-phase verification

Ran the focused tests before implementation:

```bash
go test ./internal/app/session -run TestStateActiveRouteStoresCopies -v
go test ./internal/app/tui -run 'TestViewShowsInactiveRouteByDefault|TestViewShowsActiveRoute' -v
```

Observed expected failures:

- `internal/app/session/session_test.go`
  - `undefined: RouteInfo`
  - `state.SetActiveRoute undefined`
  - `state.ActiveRoute undefined`
- `internal/app/tui/model_test.go`
  - `state.SetActiveRoute undefined`
  - `undefined: session.RouteInfo`

These failures matched the brief: the route metadata API and route display were missing.

## Implementation

### Session state

- Added `session.RouteInfo` with `routing.AgentRole` for `Role`
- Added `activeRoute` storage on `session.State`
- Added:
  - `SetActiveRoute(route RouteInfo)`
  - `ActiveRoute() RouteInfo`
- Both methods lock around access and operate on value copies

### TUI

- Inserted a route line directly under `Status`
- Default state renders `Route: inactive`
- Active state renders:
  - `role`
  - `profile`
  - `preset`
  - `provider`
  - `model`
  - `local-only`

`local-only` is displayed directly from `RouteInfo.LocalOnly`

## Final verification

Ran:

```bash
go test ./internal/app/session -v
go test ./internal/app/tui -v
```

Results:

- `go test ./internal/app/session -v` -> PASS
- `go test ./internal/app/tui -v` -> PASS
