Milestone K Context Pack v1 final review fixes

Changes
- Moved plan-refresh ownership into `internal/contextpack.RefreshPlan`, so plan insertion now reuses context-pack budget/truncation behavior instead of rebuilding token usage in `internal/agent`.
- Enforced plan placement before file snippets and tool output while preserving existing section metadata, including unknown/future section kinds.
- Added regression coverage for ordering, budget enforcement/truncation, unknown section preservation, and runner integration.

Commands and results
- `go test ./internal/contextpack -run 'TestRefreshPlan' -v`
  - PASS
  - Covered:
    - `TestRefreshPlanInsertsBeforeSnippetsAndToolOutput`
    - `TestRefreshPlanRespectsMaxTokensAndMarksTruncated`
    - `TestRefreshPlanPreservesUnknownSectionKinds`
- `go test ./internal/agent -run 'TestRunAddsPlanToContextPack|TestRunPreservesContextPackSectionMetadataWhenAddingPlan' -v`
  - PASS
  - Covered:
    - `TestRunAddsPlanToContextPackForActionCalls`
    - `TestRunAddsPlanToContextPackBeforeSnippetsAndToolOutput`
    - `TestRunPreservesContextPackSectionMetadataWhenAddingPlan`
- `go test ./internal/contextpack ./internal/agent -v`
  - PASS
- `go test ./...`
  - PASS
