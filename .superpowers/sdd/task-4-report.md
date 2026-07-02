# Task 4 Report

## Scope

- Updated `internal/contextpack/builder.go`
- Updated `internal/contextpack/contextpack_test.go`
- Updated `internal/agent/runner.go`
- Updated `internal/agent/runner_test.go`

## Test-first sequence

1. Added context-pack helper tests:
   - `TestRebudgetPreservesExistingPlanAndAppliesMaxTokens`
   - `TestRefreshPlanWithBudgetUsesProvidedMaxTokens`
2. Ran focused context-pack tests and captured expected failure:

   ```text
   go test ./internal/contextpack -run 'TestRebudget|TestRefreshPlanWithBudget' -v
   # marshal/internal/contextpack [marshal/internal/contextpack.test]
   internal/contextpack/contextpack_test.go:217:13: undefined: Rebudget
   internal/contextpack/contextpack_test.go:241:13: undefined: RefreshPlanWithBudget
   FAIL    marshal/internal/contextpack [build failed]
   FAIL
   ```

3. Implemented `RefreshPlanWithBudget` and `Rebudget`, and changed `RefreshPlan` to delegate through the stored pack budget or `DefaultMaxTokens`.
4. Ran full context-pack tests:

   ```text
   go test ./internal/contextpack -v
   PASS
   ok      marshal/internal/contextpack
   ```

5. Added runner routing tests:
   - `TestRunResolvesQuestionRouteAndUpdatesModel`
   - `TestRunAppliesRouteContextBudgetToExistingPack`
6. Ran focused runner tests and captured expected failure:

   ```text
   go test ./internal/agent -run 'TestRunResolvesQuestionRouteAndUpdatesModel|TestRunAppliesRouteContextBudgetToExistingPack' -v
   # marshal/internal/agent [marshal/internal/agent.test]
   internal/agent/runner_test.go:519:9: runner.RouteResolver undefined (type *Runner has no field or method RouteResolver)
   internal/agent/runner_test.go:557:9: runner.RouteResolver undefined (type *Runner has no field or method RouteResolver)
   FAIL    marshal/internal/agent [build failed]
   FAIL
   ```

7. Implemented per-turn route resolution in the runner:
   - added `RouteResolver`
   - resolved once after classification with `routing.TaskProfile{Class: string(task.Class)}`
   - applied resolved provider/model for the turn
   - stored `session.RouteInfo` from route preset fields
   - applied route repo-context budgets via `contextpack.Rebudget`
   - used `contextpack.RefreshPlanWithBudget` during plan refresh
   - preserved fallback behavior on resolver errors by recording provider error and continuing

## Final verification

```text
go test ./internal/contextpack -v
PASS
ok      marshal/internal/contextpack

go test ./internal/agent -v
PASS
ok      marshal/internal/agent
```
