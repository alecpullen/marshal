# Tool activity indicator — design

**Date:** 2026-07-04
**Status:** Approved, ready for implementation planning

## Problem

When the agent is working, the TUI only shows a static `WORKING` badge in the
status bar and a generic "Agent is executing tasks..." line in the Plan tab.
There is no indication of *what* is happening: is the model thinking, is a shell
command running, or is the user being asked to approve something? This makes the
agent feel opaque and can leave the user unsure whether a long-running command
is still alive.

## Goals

- Show the current high-level activity (thinking, running a tool, waiting for
  approval) at a glance.
- Display the specific tool or command name when one is running.
- Keep the indicator visible in both the status bar and the Plan tab.
- Animate the indicator with a lightweight ASCII spinner so the user knows the
  UI is alive.
- Briefly show a "done" state after a tool finishes before returning to idle.

## Non-goals

- Full command output streaming in the indicator (output already lives in the
  Log tab and in the transcript).
- Per-tool progress bars or elapsed-time timers.
- Audio or desktop notifications.

## Design

### 1. Session state — `internal/app/session/session.go`

Add a small activity snapshot to `State`:

```go
type ActivityKind string

const (
    ActivityIdle     ActivityKind = "idle"
    ActivityThinking ActivityKind = "thinking"
    ActivityTool     ActivityKind = "tool"
    ActivityApproval ActivityKind = "approval"
)

type Activity struct {
    Kind      ActivityKind
    Label     string        // human-readable, e.g. "shell.run: go test ./..."
    StartedAt time.Time
}
```

`State` gains:

```go
func (s *State) SetActivity(a Activity)
func (s *State) Activity() Activity
```

Both are guarded by the existing `mu` mutex, following the same pattern as
`SetActiveRoute`/`ActiveRoute`. The zero value `Activity{Kind: ActivityIdle}` is
returned when no activity has been set.

### 2. Agent runtime — `internal/agent/runner.go`

The runner sets activity at phase boundaries so the TUI can render it without
inferring state from side effects.

- **`chatOnce` (line 247)**: before consuming the event stream, set
  `ActivityThinking` with label `"thinking..."`. Clear to `ActivityIdle` when the
  stream ends (`ChatEventDone`) or on error. `BeginStreaming`/`EndStreaming`
  already bracket this function; activity is set right next to them.

- **`executeToolCall` (line 281)**: after parsing the tool and before invoking the
  handler, set `ActivityTool` with a label built from the tool name and the
  `command` argument when present:

  ```go
  label := toolName
  if command, ok := argsMap["command"].(string); ok && command != "" {
      label = fmt.Sprintf("%s: %s", toolName, command)
  }
  r.State.SetActivity(session.Activity{Kind: session.ActivityTool, Label: label, StartedAt: r.Now()})
  ```

  Clear to `ActivityIdle` after the handler returns (whether success or error).

- **`requestApproval` (line 356)**: when a pending approval is created, set
  `ActivityApproval` with the command that needs approval:

  ```go
  label := fmt.Sprintf("waiting for approval: %s", command)
  r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})
  ```

  Clear to `ActivityIdle` when the user responds or when the context is
  cancelled.

- **`Run` (line 77)**: set `ActivityIdle` in a `defer` or at the end of the
  function so a panic or context cancellation does not leave a stale
  `ActivityTool` behind.

### 3. Spinner — `internal/app/tui/spinner.go`

Add a tiny spinner helper to the TUI package:

```go
type Spinner struct {
    frames []string
    index  int
}

func NewSpinner() Spinner {
    return Spinner{frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}}
}

func (s *Spinner) Next() string {
    frame := s.frames[s.index]
    s.index = (s.index + 1) % len(s.frames)
    return frame
}
```

The TUI model holds one `Spinner`. It advances on each `agentTickMsg` while
`m.busy` is true.

### 4. TUI rendering — `internal/app/tui/model.go`

#### Status bar

Replace the static `WORKING`/`IDLE` badge with a dynamic activity badge.

- If `ActivityKind == ActivityIdle` and there was a recent non-idle activity,
  show `✓ <last-label>` briefly (e.g., 2 seconds) before reverting to `IDLE`.
- Otherwise render the current spinner frame + a compact label:
  - `thinking...`
  - `shell.run: go test ./...`
  - `waiting for approval: rm old.txt`

The badge keeps the existing `statusBarAccent` style. The label is truncated with
`truncateRunes` to fit the remaining status-bar width.

#### Plan tab

The Plan tab sidebar body adds an action line under the current plan:

```text
Current Plan:

 ● Redesign terminal UI layout
   → ⠋ shell.run: go test ./...
```

Use the same label as the status bar. When idle, fall back to the existing
"Ready for user input." text.

### 5. Done-state handling

Introduce a small `lastActivity Activity` field on the TUI model. When the
runner transitions from a non-idle activity to idle, the model stores the
previous activity and records `lastActivityAt time.Time`. The status bar renders
`✓ <label>` while `time.Since(lastActivityAt) < doneDisplayDuration` (2s), then
reverts to `IDLE`.

This is purely a TUI concern; `session.State` does not track done state.

### 6. Error handling & edge cases

- **No activity set:** `State.Activity()` returns the zero `ActivityIdle` value,
  so the TUI defaults to idle.
- **Long-running commands:** The spinner animates independently via the existing
  `agentTickMsg` loop. The label is static once set.
- **Approval timeout / cancellation:** `requestApproval` clears activity to idle
  on `<-ctx.Done()`.
- **Rapid transitions:** The spinner frame index is owned by the TUI and only
  advances on ticks, so a label swap does not reset the animation.
- **Defensive fallback in TUI:** If `State.Activity()` is idle but
  `PendingApproval()` is non-nil, the TUI still renders the approval label. This
  guards against any missed transition in the runner.

### 7. Testing

- **Session:** `SetActivity`/`Activity` round-trip; zero value is idle;
  concurrency safety alongside existing render-during-mutation tests.
- **Agent runner:** Mock `session.State` and assert activity transitions through
  `thinking → tool → idle` during a synthetic turn, and `thinking → approval →
  idle` when a risky command is used.
- **TUI spinner:** frame sequence wraps correctly.
- **TUI model:** while `busy` is true, the status bar contains the current
  spinner frame and the expected label; after `agentFinishedMsg`, the badge
  briefly shows `✓ <label>` before returning to `IDLE`.

## Deferred (explicitly out of scope)

- Per-tool progress bars or elapsed-time timers.
- Streaming command output into the indicator.
- Configurable spinner frame sets or color themes.
