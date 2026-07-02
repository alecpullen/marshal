# Task 3 Report: Inject Context Packs Into Agent Prompts

## Scope

Implemented Task 3 exactly within the owned files:

- `internal/agent/prompts.go`
- `internal/agent/prompts_test.go`
- `internal/agent/runner.go`
- `internal/agent/runner_test.go`

## TDD Sequence

1. Added failing tests for:
   - `BuildContextPackMessage` empty-pack behavior
   - `BuildContextPackMessage` rendered-pack behavior
   - stored context-pack injection into runner requests
   - omission of empty packs
   - plan reinjection into the context pack for action calls
2. Ran:

```bash
go test ./internal/agent -run 'TestBuildContextPackMessage|TestRunInjectsStoredContextPack|TestRunOmitsContextPackWhenEmpty|TestRunAddsPlanToContextPackForActionCalls' -v
```

Observed expected failure: `BuildContextPackMessage` was undefined.

3. Implemented the minimal production changes:
   - added `BuildContextPackMessage(pack contextpack.Pack) (schema.ChatMessage, bool)`
   - injected stored context-pack messages before the user goal
   - rebuilt the stored context pack with the generated plan for subsequent action requests
   - preserved empty-pack behavior by skipping injection when `contextpack.Render(pack) == ""`

4. Re-ran the targeted tests and confirmed they passed.

## Implementation Notes

- `BuildContextPackMessage` delegates rendering to `contextpack.Render` and returns `false` for empty output.
- `Runner.Run` now:
  - prepends a rendered context-pack message, if present, to the initial chat request
  - refreshes the stored pack with the current plan after planning, using `contextpack.NewBuilder().Build(...)`
  - rebuilds the base prompt messages so later action calls include the updated plan-bearing pack
- Empty packs still produce no injected message and do not alter runner behavior.

## Test Results

Commands run:

```bash
go test ./internal/agent -run 'TestBuildContextPackMessage|TestRunInjectsStoredContextPack|TestRunOmitsContextPackWhenEmpty|TestRunAddsPlanToContextPackForActionCalls' -v
go test ./internal/agent -v
go test ./...
```

All passed.

## Self-Review

- Change scope stayed within the four owned files.
- No Milestone J files were modified.
- Context-pack building uses only in-memory section data already stored in session state; no filesystem I/O was added.
- The plan merge preserves repo-card, file-snippet, and tool-output sections from the stored pack and rebuilds the pack through the shared builder so token budgeting remains centralized.

## Commit

Created commit:

- `feat(agent): inject context packs into prompts`

## Review Fix: Preserve Stored Context-Pack Metadata

Reviewer finding: the first implementation rebuilt the stored pack through `contextpack.BuildInput`, which narrowed upstream data and lost section metadata such as file-snippet `Source` ranges.

Fix applied:

- changed `internal/agent/runner.go` so plan refresh now clones the stored pack and updates only `SectionPlan`
- preserved all non-plan sections exactly, including `Kind`, `Title`, `Content`, `Source`, `Priority`, and `EstimatedTokens`
- preserved unknown and future section kinds by carrying them through untouched
- recomputed `TokenUsage.EstimatedTokens` from the final section list without dropping sections

Regression test added:

- `TestRunPreservesContextPackSectionMetadataWhenAddingPlan`

This test verifies a stored file-snippet section with `Source: "internal/app/app.go:1-3"` still has that exact `Source` after `Run` adds the current plan.

Review-fix test results:

```bash
go test ./internal/agent -run 'TestRunAddsPlanToContextPackForActionCalls|TestRunPreservesContextPackSectionMetadataWhenAddingPlan' -v
go test ./internal/agent -v
go test ./...
```

All passed.
