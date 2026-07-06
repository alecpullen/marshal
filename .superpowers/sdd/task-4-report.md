# Task 4: [swarm.budget] config section

## Status
Completed.

## What changed
- Added `Config.Swarm SwarmConfig`.
- Added `SwarmConfig` and `SwarmBudgetConfig`.
- Added defaults:
  - `MaxFixRounds = 3`
  - `MaxTotalTokens = 120000`
  - `ToolIters = map[string]int{}`
- Added TOML load/merge support for `[swarm.budget]` and `[swarm.budget.tool_iters]`.
- Added focused tests for defaults and project config merge behavior.

## TDD evidence

RED:
```
go test ./internal/app/config/ -run TestSwarmBudget -v
```
Failed as expected because `Config` had no `Swarm` field.

GREEN:
```
go test ./internal/app/config/ -run TestSwarmBudget -v
```
Passed:
- `TestSwarmBudgetDefaults`
- `TestSwarmBudgetMergesFromFile`

Regression:
```
go test ./internal/app/config/...
```
Passed.

Additional pre-commit check:
```
go vet ./...
```
Passed.

## Files changed
- `internal/app/config/config.go`
- `internal/app/config/config_test.go`

## Commit
`c9c068b feat(config): add [swarm.budget] section`

## Self-review
The implementation follows the existing pointer-backed config-file merge pattern. The tests use the existing `.marshal/config.toml` project config layout via `writeFile` and `Load`.

## Concerns
None.
