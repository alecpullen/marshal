# Final Review Fix Report

## Findings fixed
- Fixed TUI broker pump subscription leakage by creating persistent model-owned job and steering subscription channels and re-arming reads from the same channels.
- Fixed stale broker-backed steering queue counts by publishing `SteeringEvent{QueueLen: currentLen}` after `DrainSteering`, `PopSteering`, and `ClearSteering`, outside the state lock.
- Fixed `ContentTypeDiff` transcript rendering by delegating `renderDiffBlock` to `internal/diffview.Render` with `ModeAuto` and highlighting enabled, preserving a plain fallback for empty renderer output.
- Fixed steering `@file` references by extracting pins from drained steering messages, updating `State.ContextPack()` with `contextpack.PinFiles`, and appending a fresh context-pack message before the next model call.

## Test-first failures observed
- `go test ./internal/app/tui -run TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions` failed with `pump did not deliver job count 3`, proving the re-armed pump left a stale terminal subscription that blocked the next publish.
- `go test ./internal/app/tui -run 'TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions|TestRenderDiffBlockUsesDiffviewAtWideWidth'` failed with `wide unified diff should render through diffview side-by-side layout`, proving transcript diffs were still using the old local renderer.
- `go test ./internal/app/session -run TestSteeringQueuePublishesQueueLenAfterDrainPopAndClear` failed with `timed out waiting for steering QueueLen 0`, proving drain/pop/clear did not publish queue length updates.
- `go test ./internal/agent -run TestRunTaskPinsAtFileReferencesFromDrainedSteering` failed because the second provider request contained the raw steering message `also inspect @steered.go` but not the pinned file content.

## Code changes
- `internal/app/tui/model.go` now subscribes to each broker once during model construction and stores the receive channels on the model.
- `internal/app/tui/pump.go` now turns an existing receive channel into a Bubble Tea command instead of subscribing on each re-arm.
- `internal/app/session/session.go` now publishes queue length updates from drain/pop/clear after releasing the session lock.
- `internal/app/tui/transcript.go` now renders diff transcript blocks through `diffview.Render` using width-aware auto mode and highlighting.
- `internal/agent/runner.go` now extracts `@file` pins from drained steering messages and injects an updated context-pack message into the live model request.
- Added focused regression coverage in TUI pump/transcript tests, session steering tests, and agent at-file steering tests.

## Exact test commands/results
- FAIL before fix: `go test ./internal/app/tui -run TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions`
- FAIL before fix: `go test ./internal/app/tui -run 'TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions|TestRenderDiffBlockUsesDiffviewAtWideWidth'`
- FAIL before fix: `go test ./internal/app/session -run TestSteeringQueuePublishesQueueLenAfterDrainPopAndClear`
- FAIL before fix: `go test ./internal/agent -run TestRunTaskPinsAtFileReferencesFromDrainedSteering`
- PASS after fix: `go test ./internal/app/tui -run 'TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions|TestRenderDiffBlockUsesDiffviewAtWideWidth|TestPumpBridgesJobEventsToMsgs|TestPumpReturnsNilOnCtxCancel|TestRenderDiffBlockColorsWithoutPanel'`
- PASS after fix: `go test ./internal/app/session -run TestSteeringQueuePublishesQueueLenAfterDrainPopAndClear`
- PASS after fix: `go test ./internal/agent -run TestRunTaskPinsAtFileReferencesFromDrainedSteering`
- PASS after fix: `go test ./internal/app/tui ./internal/app/session ./internal/agent`
- PASS after fix: `go test ./...`

## Commit hash(es)
- Code/test fix commit: `a2f07f1`
- This report is committed separately after the code/test fix so it can include the code commit hash.
