# Task 10: Swarm roster panel in TUI

## Status
Completed.

## What changed
- Added `renderSwarmPanel` and fixed `swarmPanelRows` reservation.
- Rendered the active swarm panel between transcript and input.
- Reserved viewport height only when `SwarmProgress.Active` is true.
- Recomputed viewport height during tick/input updates so live progress can appear/disappear without layout drift.
- Recomputed viewport height on `agentFinishedMsg` so the reservation is released after swarm completion.
- Added focused panel and row-reservation tests.

## TDD evidence

RED:
```
go test ./internal/app/tui/ -run SwarmPanel -v
```
Failed as expected with undefined `renderSwarmPanel`, `swarmPanelRows`, and `swarmPanelRows()`.

GREEN:
```
go test ./internal/app/tui/ -run SwarmPanel -v
```
Passed:
- `TestRenderSwarmPanelShowsRolesAndStatus`
- `TestRenderSwarmPanelInactiveIsEmpty`
- `TestSwarmPanelRowsReservedOnlyWhenActive`
- `TestAgentFinishedReleasesSwarmPanelReservation`

Regression:
```
go test ./internal/app/tui/ -run 'View|Resize' -v
go test ./internal/app/tui/
```
Passed.

Build/vet:
```
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```
Passed.

## Files changed
- `internal/app/tui/swarm_panel.go`
- `internal/app/tui/swarm_panel_test.go`
- `internal/app/tui/view.go`
- `internal/app/tui/model.go`

## Commit
`f2e592b feat(tui): add swarm roster panel`

## Self-review
The panel is hidden when inactive, fixed-height when active, and the viewport height formula now accounts for the reserved rows. Rendering caps to five role rows to preserve the fixed reservation defined by the task.

## Concerns
None.
