# Task 4 Report: Route "knowledge" task class to RoleKnowledge

## Summary

Task 4 has been completed successfully. The "knowledge" task class is now routed to `RoleKnowledge` in the routing layer, with fallback behavior to `RoleImplementer` when `RoleKnowledge` is not configured for a profile.

## Implementation

### Files Changed

1. **internal/llm/routing/router.go** (1 line changed)
   - Added `case "knowledge": return RoleKnowledge` to the `roleForTaskClass()` function

2. **internal/llm/routing/router_test.go** (44 lines added)
   - Added `TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured()`
   - Added `TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured()`

### TDD Evidence

#### RED (Failing Tests)
```
$ go test ./internal/llm/routing/... -run "TestResolveKnowledge" -v
=== RUN   TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured
    router_test.go:225: Resolve returned error: routing: role not configured: local_balanced role implementer
--- FAIL: TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured (0.00s)
=== RUN   TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured
--- PASS: TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured (0.00s)
FAIL	marshal/internal/llm/routing	0.459s
```

The first test failed because `roleForTaskClass("knowledge")` was not yet returning `RoleKnowledge` (it returned `RoleImplementer` via the default case). The second test passed because the default behavior already returned `RoleImplementer`, matching the expected fallback.

#### GREEN (Passing Tests)
```
$ go test ./internal/llm/routing/... -v
=== RUN   TestResolveQuestionUsesRepoScout
--- PASS: TestResolveQuestionUsesRepoScout (0.00s)
=== RUN   TestResolveEditUsesImplementerAndBudget
--- PASS: TestResolveEditUsesImplementerAndBudget (0.00s)
=== RUN   TestResolveFallsBackToImplementerForMissingRole
--- PASS: TestResolveFallsBackToImplementerForMissingRole (0.00s)
=== RUN   TestResolveQuestionMissingRepoScoutPresetDoesNotFallBackToImplementer
--- PASS: TestResolveQuestionMissingRepoScoutPresetDoesNotFallBackToImplementer (0.00s)
=== RUN   TestResolveQuestionRemoteBlockedDoesNotFallBackToImplementer
--- PASS: TestResolveQuestionRemoteBlockedDoesNotFallBackToImplementer (0.00s)
=== RUN   TestResolveUsesLegacyWhenNoProfileRouteExists
--- PASS: TestResolveUsesLegacyWhenNoProfileRouteExists (0.00s)
=== RUN   TestResolveMissingProfileWithoutLegacyReturnsError
--- PASS: TestResolveMissingProfileWithoutLegacyReturnsError (0.00s)
=== RUN   TestResolveMissingPresetReturnsError
--- PASS: TestResolveMissingPresetReturnsError (0.00s)
=== RUN   TestResolveBlocksRemotePresetWhenRemoteDisabled
--- PASS: TestResolveBlocksRemotePresetWhenRemoteDisabled (0.00s)
=== RUN   TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured
--- PASS: TestResolveKnowledgeUsesKnowledgeRoleWhenConfigured (0.00s)
=== RUN   TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured
--- PASS: TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured (0.00s)
PASS
ok  	marshal/internal/llm/routing	0.380s
```

All 11 tests pass: 9 pre-existing tests + 2 new knowledge tests.

## Commit

```
e799054 feat(routing): map the knowledge task class to RoleKnowledge
```

## Self-Review Findings

### Fallback Behavior Verification
The second test `TestResolveKnowledgeFallsBackToImplementerWhenNotConfigured()` correctly exercises the existing generic fallback logic already implemented in `StaticRouter.Resolve()`:

- When a task class maps to a role (e.g., "knowledge" → RoleKnowledge)
- And that role is not configured in the active profile
- The router falls back to `RoleImplementer` (via the condition `if role != RoleImplementer && errors.Is(err, errRoleNotConfigured)` at line 34)
- No new fallback-specific logic was added; only the class-to-role mapping

### Existing Tests Status
All 9 pre-existing tests pass without modification:
- `TestResolveQuestionUsesRepoScout`
- `TestResolveEditUsesImplementerAndBudget`
- `TestResolveFallsBackToImplementerForMissingRole`
- `TestResolveQuestionMissingRepoScoutPresetDoesNotFallBackToImplementer`
- `TestResolveQuestionRemoteBlockedDoesNotFallBackToImplementer`
- `TestResolveUsesLegacyWhenNoProfileRouteExists`
- `TestResolveMissingProfileWithoutLegacyReturnsError`
- `TestResolveMissingPresetReturnsError`
- `TestResolveBlocksRemotePresetWhenRemoteDisabled`

### No Unwanted Changes
- No changes to `internal/llm/routing/types.go` (as instructed)
- No changes to fallback logic in `Resolve()`, `resolveProfileRole()`, or `legacyRoute()` (as instructed)
- No new special-case handling for RoleKnowledge; it follows the same pattern as RoleRepoScout

## Concerns

None. The implementation is minimal, focused, and correct.
