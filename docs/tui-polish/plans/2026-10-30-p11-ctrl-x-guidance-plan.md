# P11 Ctrl+X guidance — implementation plan

Date: 2026-10-30. Status: proposed; not executed.
Base: `dee8b086b18193e00f525f0a92913ad904b84c69`.
Requirements: [triage item 12](../triage.md#p2--interaction-and-visibility)
and [the Ctrl+X footer finding](../audit.md#ctrlx-footer-guidance-can-conflict-or-repeat).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** show at most one actionable Ctrl+X hint, matching the action the
normal keyboard handler will take in the current context.

**Non-goals:** change Ctrl+C, Send now, subagent cancellation ownership,
steering settlement, browse navigation, prompt handling, web UI, or unrelated
P3 cleanup. This is phase 11 in the plan sequence and addresses P2 item 12.

**Decisions and assumptions:**

- While busy and drilled into a view whose stored status is running, Ctrl+X
  attempts to stop that exact subagent. Otherwise, while busy, it clears the
  ordinary steering queue through `ClearSteering`. While idle it does neither.
  The hint uses the same decision as the handler, and the clear-queue hint is
  shown only if a queue is nonempty. The existing protected Send now entry
  remains protected by `ClearSteering`.
- `viewStack` contains a snapshot: the child can finish or lose its cancel
  handle before the stop request arrives. If `CancelSubagent` returns false
  for a drilled-running snapshot, consume Ctrl+X, keep the queue untouched,
  and flash `Agent could not be stopped; queue unchanged`. Do not fall through
  to clearing a different target. A completed snapshot uses the normal busy
  queue action. Do not claim a successful stop when the call failed.
- A dock or inline prompt may own keyboard input before the global handler.
  Never add a Ctrl+X hint to such a surface. Existing prompt/approval, popup,
  and full-frame controls retain precedence. Browse mode declines Ctrl chords,
  so its footer may show the one actual Ctrl+X action when it fits.
- Preserve the existing narrow `StopOnly` fallback (`Ctrl+C stop`), rather
  than squeezing an incorrect or partial Ctrl+X label into the status row.
  Width-dependent shedding may omit the Ctrl+X hint; if it is displayed, it
  must be whole, unique and truthful.
- Use existing isolated test fixtures and preserve unrelated untracked files.
  Tasks are sequential and independently verifiable; stage named paths only.

## Contracts

Task 1 introduces a TUI-private decision, shared by the keyboard handler and
Task 2's footer construction:

```go
type ctrlXAction uint8

const (
    ctrlXNone ctrlXAction = iota
    ctrlXStopAgent
    ctrlXClearQueue
)

func (m Model) ctrlXAction() ctrlXAction
```

The method returns `ctrlXNone` when idle, `ctrlXStopAgent` when busy and
`drilledIntoRunningSubagent()` is true, otherwise `ctrlXClearQueue`. It is a
decision for the global handler only, not a promise that an earlier modal input
owner will forward the key. The stop target is the top `viewStack` entry.

Task 2 replaces the footer's independent `DrilledRunActive` and queue hint
decision with this explicit presentation contract in package `help`:

```go
type CtrlXHint uint8

const (
    CtrlXHintNone CtrlXHint = iota
    CtrlXHintStopAgent
    CtrlXHintClearQueue
)
```

`FooterHints.CtrlXHint` has type `CtrlXHint`. The parent converts its single
decision to a hint only when the global key path is reachable; clear-queue is
omitted when the queue is empty. No new policy belongs in `help`.

## Task 1: Make the Ctrl+X action stable on failed stops

**Goal:** a failed attempt to stop a drilled-in agent never clears the queue.

**Files:** `internal/app/tui/keypress.go`,
`internal/app/tui/status.go`, `internal/app/tui/drill_test.go`.

**Steps:**

1. Add the private action type and `Model.ctrlXAction()` from Contracts near
   `drilledIntoRunningSubagent` in `status.go`; use the existing snapshot
   status rather than dereferencing a copied cancel function. In the
   `ctrl+x` branch of `handleKeypress`, switch on that action. For
   `ctrlXStopAgent`, call `m.state.CancelSubagent` with the top ID, refresh
   the viewport on success, and return handled in either case. On failure use
   the existing `m.setFlash` to return the exact failure message from
   Decisions and leave the queue intact. For `ctrlXClearQueue` keep the
   existing `ClearSteering` and refresh path. For `ctrlXNone` retain the
   existing unhandled idle behavior.
2. Extend the existing `TestCtrlXStopsRunningSubagentWhileDrilled` and
   `TestCtrlXUndrilledClearsSteeringQueue` assertions to verify exact target,
   queue state and handled status. Add `TestCtrlXFailedDrilledStopKeepsQueue`
   for a finished live child behind a stale running snapshot and for a
   running child without a cancel handle; assert no cancellation, preserved
   queue and the flash. Add `TestCtrlXCompletedDrillAndIdle` to assert a
   completed snapshot clears the busy queue, while idle does not clear it.
   Keep the existing protected Send now test in `model_test.go` intact.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestCtrlX' -count=1 -timeout 90s
go test ./internal/app/tui -run 'Test.*SendNow' -count=1 -timeout 90s
gofmt -l internal/app/tui/keypress.go internal/app/tui/status.go internal/app/tui/drill_test.go
```

Expected: tests pass; formatting prints no paths.

## Task 2: Render one context-appropriate hint

**Goal:** footer and narrow status fallbacks never advertise a duplicate,
unavailable or conflicting Ctrl+X action.

**Files:** `internal/app/tui/help/help.go`,
`internal/app/tui/help/help_test.go`, `internal/app/tui/status.go`,
`internal/app/tui/status_test.go`.

**Steps:**

1. Add `CtrlXHint` and the `FooterHints.CtrlXHint` field from Contracts in
   `help`. Remove `DrilledRunActive` and the unconditional queue hint in
   `Footer`; emit a single `Ctrl+X stop agent` or `Ctrl+X clear queue` pair
   from `CtrlXHint` in the ordinary busy branch. For browse mode, append the
   same pair before the return when provided. Never append the pair in idle,
   prompt, edit, popup or `KeysInFrame` branches. Keep `QueueNonEmpty` only
   if another existing consumer needs it; otherwise remove it. Update
   existing help tests that expected an idle queue hint or a queue hint with
   no queue, since the key is not actionable in those states.
2. In `Model.footerHints`, compute `m.ctrlXAction()` and map it to one
   `CtrlXHint`. Set it to none if a dock is open, a pending approval,
   question, skill gate, sudo input, approval editor or completion popup owns
   the key; for clear queue also require a nonempty steering queue from the
   same status snapshot. Browsing remains eligible because `handleBrowseKey`
   lets Ctrl chords through. Preserve all unrelated hints and the
   `statusClusters` fallback ordering. Do not change the handler's action
   based on queue display or the footer's visual width. Update
   `TestStatusLineShowsQueueHint` to mark its fixture busy, matching the
   existing handler's requirement.
3. Add `TestFooterCtrlXMatchesAction` with a table of busy/idle, queue
   empty/nonempty, stop/clear/none, browsing, modal/popup and `StopOnly`
   states. Assert zero or one complete Ctrl+X pair and no other Ctrl+X label.
   Add `TestStatusCtrlXHintMatchesDrillAndWidth` using real model fixtures:
   render 80×24, 120×40 and a wide status row for undrilled, running and
   completed drilled views, with queue empty and nonempty; where a Ctrl+X
   hint fits, assert it names exactly the action from Task 1. Where the
   fallback sheds it, assert `Ctrl+C stop` remains and there is no fragment.
   Include modal, dock and browse cases, plus `NO_COLOR`.

**Verify:**

```bash
go test ./internal/app/tui/help -count=1
go test ./internal/app/tui -run 'Test(Status.*CtrlX|StatusLineShowsQueueHint|CtrlX)' -count=1 -timeout 90s
gofmt -l internal/app/tui/help internal/app/tui/status.go internal/app/tui/status_test.go
```

Expected: tests pass; formatting prints no paths.

## Task 3: Verify the displayed hint against the real key path

**Goal:** the composed frame and `Model.Update` agree on the Ctrl+X target,
including subagent completion races and modal input ownership.

**Files:** `internal/app/tui/drill_test.go`,
`internal/app/tui/model_test.go`.

**Steps:**

1. Add `TestCtrlXDisplayedActionThroughModel` with isolated parent and child
   sessions, a cancel recorder and queued steering. At wide and 80×24
   geometries, render the composed model and send Ctrl+X through
   `Model.Update`: for an undrilled busy model assert the queue is cleared;
   for a running drilled model assert only that agent is cancelled and the
   queue remains; for a completed drill assert queue clear; for idle assert
   no queue clear. Assert the displayed hint if present agrees with the
   observed action and appears at most once. Cover browsing and resize.
2. Add `TestCtrlXStaleSubagentNeverClearsQueueThroughModel`: render the
   running-agent hint, finish the actual child before the key, then send
   Ctrl+X through `Model.Update`; assert the queue survives and the failure
   feedback is shown. Include a pending prompt and a docked panel: neither
   advertises the global hint or leaks the key into the global action.
3. Keep the existing accepted Send now retention regression and verify it in
   the final package suite; do not alter session steering internals.

**Verify:**

```bash
go test ./internal/app/tui -run '^TestCtrlX(DisplayedActionThroughModel|StaleSubagentNeverClearsQueueThroughModel)' -count=1 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
gofmt -l internal/app/tui/drill_test.go internal/app/tui/model_test.go
```

Expected: tests pass; formatting prints no paths.

## Final verification and integration

After all tasks pass, run from the repository root:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p11-ctrl-x ./cmd/marshal
go test ./internal/app/tui/... ./internal/app/session -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/app/tui/help internal/app/tui/keypress.go internal/app/tui/status.go internal/app/tui/status_test.go internal/app/tui/drill_test.go internal/app/tui/model_test.go
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p11-ctrl-x go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Compare full-suite failures against the latest
triage baseline and record actual results. The existing scripted usability
suite is help-only; do not claim it covers subagent stopping. If a controlling
terminal is available, manually check the queue and drilled agent at 80×24,
resize, and one multiplexer/SSH environment with local fixtures; otherwise
record the limitation. After verification update the audit finding and triage
item 12, and commit the named plan and documentation with the work.

## Plan self-review

- The action decision is shared by handler and footer; failed stop cannot
  fall through to queue clearing. Modal ownership and narrow fallbacks have
  explicit expectations.
- The tasks include both isolated decision tests and real host input/render
  checks, including queue protection. No implementation occurred in planning.
