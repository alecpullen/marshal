# Task 4 Fix Report: Stable Fallback Provider/Model

Summary
- Added a regression test in `internal/agent/runner_test.go` for the two-turn path: successful routed turn followed by resolver error, with the second turn falling back to the original runner provider/model.
- Updated `internal/agent/runner.go` so route resolution returns per-turn effective provider/model values instead of mutating `Runner.Provider` and `Runner.Model`.
- Preserved existing behavior when no resolver is configured, preserved successful routed turns, and continued recording provider errors on resolver failure.

TDD Record
1. Added `TestRunFallsBackToOriginalProviderAndModelAfterResolverError`.
2. Ran:
   - `go test ./internal/agent -run TestRunFallsBackToOriginalProviderAndModelAfterResolverError -v`
3. Observed RED:
   - `runner_test.go:607: routed provider requests = 2, want 1`
4. Implemented the minimal fix by threading turn-local provider/model through `Run`, `chatWithRetry`, and `chatOnce`.
5. Re-ran the focused routing tests and got GREEN.

Implementation Notes
- `Runner.Provider` and `Runner.Model` remain the baseline fallback values configured by `NewRunner`.
- `resolveRoute` now starts from the baseline fallback, applies route-local overrides only for the current turn, and still updates `State.ActiveRoute` plus route context budget when a route resolves successfully.
- Resolver errors still call `State.SetProviderError(err)` and return control to the baseline fallback provider/model for that turn.

Verification
- `go test ./internal/agent -run TestRunFallsBackToOriginalProviderAndModelAfterResolverError -v` (RED before fix)
- `go test ./internal/agent -run 'TestRunResolvesQuestionRouteAndUpdatesModel|TestRunAppliesRouteContextBudgetToExistingPack|TestRunFallsBackToOriginalProviderAndModelAfterResolverError' -v`
- `go test ./internal/agent -v`
- `go test ./internal/contextpack -v`

Concerns
- None.
