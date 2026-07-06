# Task 6: Tester and implementer-feedback prompts

## Status
Completed.

## What changed
- Added `testerPrompt`.
- Updated `implementerPrompt` to tell the implementer to use tester feedback about failing tests in shared task state.
- Added tests that require:
  - tester prompt emits `VERDICT:` guidance
  - tester prompt forbids source modification
  - tester prompt embeds shared task state
  - implementer prompt references tests / tester feedback

## TDD evidence

RED:
```
go test ./internal/agent/swarm/ -run 'TesterPrompt|ImplementerPrompt' -v
```
Failed as expected with `undefined: testerPrompt`.

GREEN:
```
go test ./internal/agent/swarm/ -run 'TesterPrompt|ImplementerPrompt' -v
```
Passed:
- `TestTesterPromptDemandsVerdict`
- `TestImplementerPromptMentionsTesterFeedback`

Prompt regression:
```
go test ./internal/agent/swarm/ -run 'Prompt' -v
```
Passed:
- `TestRolePromptsEmbedSharedTaskState`
- `TestTesterPromptDemandsVerdict`
- `TestImplementerPromptMentionsTesterFeedback`

Additional pre-commit check:
```
go vet ./...
```
Passed.

## Files changed
- `internal/agent/swarm/prompts.go`
- `internal/agent/swarm/prompts_test.go`

## Commit
`0f06327 feat(swarm): add tester prompt and tester-aware implementer prompt`

## Self-review
The change is prompt-only and keeps existing prompt function signatures intact. The tester prompt instructs read/test-only behavior and exact PASS/FAIL verdict output, while the implementer prompt now has the feedback hook needed by the later loop task.

## Concerns
None.
