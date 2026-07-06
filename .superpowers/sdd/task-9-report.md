# Task 9: Wire budgets, meter, and per-role caps in app.go

## Status
Completed.

## What changed
- Added `roleToolIterations`.
- Applied per-role tool iteration caps in the swarm runner factory.
- Wired `MaxFixRounds`, `MaxTotalTokens`, and `NewMeter` onto the constructed swarm orchestrator.
- Added a focused helper test for role-specific cap override and agent default fallback.

## TDD evidence

RED:
```
go test ./internal/app/ -run TestRoleToolIterations -v
```
Failed as expected with `undefined: roleToolIterations`.

GREEN:
```
go test ./internal/app/ -run TestRoleToolIterations -v
```
Passed.

Build/vet:
```
go build ./...
go vet ./...
```
Passed. The first sandboxed build hit a Go cache permission denial under `~/Library/Caches/go-build`; rerun outside the sandbox passed.

## Files changed
- `internal/app/app.go`
- `internal/app/app_test.go`

## Commit
`d7ae061 feat(app): wire swarm budgets, meter, and per-role tool caps`

## Self-review
The helper keeps cap selection pure and testable. The factory still uses runner defaults when both role-specific and agent-wide caps are zero.

## Concerns
None.
