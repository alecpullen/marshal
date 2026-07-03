# Task 5 Report: Context Pack Memory Section and MergeMemories

## Implementation Summary

Successfully implemented Task 5 as specified in the task brief. Added support for durable project memories in the contextpack package through:

1. **New `SectionMemory` section kind** (priority 15)
2. **`MemoryNote` type** for representing individual memory entries
3. **`newMemorySection()` helper function** that formats memory notes with optional kind tags
4. **`MergeMemories()` public function** that replaces/inserts memory sections and respects token budgets

## Files Changed

- `internal/contextpack/contextpack.go`: Added `SectionMemory` constant and `MemoryNote` type
- `internal/contextpack/builder.go`: Added `newMemorySection()` and `MergeMemories()` functions
- `internal/contextpack/contextpack_test.go`: Added four new test functions

## TDD Evidence

### RED Phase
Command: `go test ./internal/contextpack/... -run TestMergeMemories -v`

Output (build failed, as expected):
```
internal/contextpack/contextpack_test.go:262:16: undefined: MemoryNote
internal/contextpack/contextpack_test.go:267:13: undefined: MergeMemories
internal/contextpack/contextpack_test.go:272:46: undefined: SectionMemory
...
```

### GREEN Phase
Command: `go test ./internal/contextpack/... -v`

Output (all tests pass):
```
=== RUN   TestMergeMemoriesInsertsBeforePlanAndSnippets
--- PASS: TestMergeMemoriesInsertsBeforePlanAndSnippets (0.00s)
=== RUN   TestMergeMemoriesReplacesExistingMemorySection
--- PASS: TestMergeMemoriesReplacesExistingMemorySection (0.00s)
=== RUN   TestMergeMemoriesEmptyIsNoOp
--- PASS: TestMergeMemoriesEmptyIsNoOp (0.00s)
=== RUN   TestMergeMemoriesRespectsMaxTokensAndMarksTruncated
--- PASS: TestMergeMemoriesRespectsMaxTokensAndMarksTruncated (0.00s)
PASS
ok  	marshal/internal/contextpack	0.497s
```

Total: 14 tests, all passing (10 existing + 4 new)

## Self-Review Findings

### ✓ Insertion Point Verification
**Requirement:** Memory section must insert immediately before first plan/file-snippet/tool-output section

**Evidence:** Test `TestMergeMemoriesInsertsBeforePlanAndSnippets` verifies:
- Pack with [RepoCard, Plan, FileSnippet] → [RepoCard, Memory, Plan, FileSnippet]
- Memory inserted at index 1 (before plan at index 2)

**Code path:** Lines 246-250 in builder.go check `(section.Kind == SectionPlan || section.Kind == SectionFileSnippet || section.Kind == SectionToolOutput)` before inserting.

### ✓ Priority Ordering
Required: repo_card=10, memory=15, plan=20, file_snippet=30, tool_output=40

Verified in `buildCandidateSections()` (builder.go):
- SectionRepoCard: Priority = 10 ✓
- SectionMemory: Priority = 15 (newMemorySection line 227) ✓
- SectionPlan: Priority = 20 ✓
- SectionFileSnippet: Priority = 30 ✓
- SectionToolOutput: Priority = 40 ✓

### ✓ Priority Field Is Not Used for Sorting
Confirmed: `buildPackFromSections()` (builder.go:153) processes sections in order without sorting by Priority. Priority is metadata only.

### ✓ Replacement Logic
Test `TestMergeMemoriesReplacesExistingMemorySection` verifies:
- Old memory section with "stale note" is removed
- New memory section with "fresh note" replaces it at same position
- Net section count unchanged (3 before, 3 after)

Code path: Line 241 in builder.go skips old memory sections with `continue`.

### ✓ Empty Memories No-Op
Test `TestMergeMemoriesEmptyIsNoOp` verifies:
- `MergeMemories(pack, nil, ...)` returns pack with original sections unchanged
- Code path: `newMemorySection(nil)` returns `(Section{}, false)`, so memory insertion is skipped

### ✓ Token Budget Respect
Test `TestMergeMemoriesRespectsMaxTokensAndMarksTruncated` verifies:
- Memory section is truncated when exceeding budget
- `TokenUsage.Truncated` is set to true
- EstimatedTokens never exceeds MaxTokens

Code path: All sections passed to `buildPackFromSections()` which enforces budget.

### ✓ Existing Tests Unchanged
All 10 existing contextpack tests still pass:
- TestEstimateTokensRoundsUpByFourRunes
- TestBuilderOrdersSectionsAndTracksTokens
- TestBuilderTruncatesToBudget
- TestRenderUsesStableSectionFormat
- TestEmptyPackRendersEmptyAndClonesSafely
- TestRefreshPlanInsertsBeforeSnippetsAndToolOutput
- TestRefreshPlanRespectsMaxTokensAndMarksTruncated
- TestRefreshPlanPreservesUnknownSectionKinds
- TestRebudgetPreservesExistingPlanAndAppliesMaxTokens
- TestRefreshPlanWithBudgetUsesProvidedMaxTokens

## Code Structure Alignment

MergeMemories mirrors RefreshPlanWithBudget's pattern:
1. Validate maxTokens, set default if needed
2. Preserve or compute generatedAt timestamp
3. Build candidate section(s)
4. Iterate through existing sections, removing old ones and inserting new at correct position
5. Rebuild pack with `buildPackFromSections()` to apply token budget

## Concerns

None. All requirements met, all tests pass (existing and new), implementation follows established patterns.

## Commit

```
58becd0 feat(contextpack): add memory section and MergeMemories
```

Commit includes exactly the three files specified in the task brief:
- internal/contextpack/contextpack.go
- internal/contextpack/builder.go
- internal/contextpack/contextpack_test.go
