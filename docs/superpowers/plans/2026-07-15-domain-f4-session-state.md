# Domain F4 — Session State Races & Lifecycle Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in a
> dedicated worktree (suggested branch `feature/domain-f4-session-state`).
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Resolve five findings from
`docs/14-codebase-improvement-audit-2026-07-14.md` (Domain F) that
concern race conditions in the session state machine and viewport
rendering:

- **F-BUG-149** (MEDIUM) — `cancelTurn` calls `m.agentCancel()` and
  immediately clears the steering queue / appends a system message
  without confirming the agent actually cancelled. If the agent
  finished between the cancel call and the clear, in-flight
  follow-up messages are lost.
- **F-BUG-150** (LOW) — `handleAgentFinished` does not clear
  `successPulse` when `msg.err != nil`. The input border still
  flashes teal even though the turn errored.
- **F-BUG-151** (LOW) — `updateViewportHeight` ignores the SDD
  hint row when SDD is active but the activity strip is idle, so
  the viewport overlaps the hint.
- **F-BUG-152** (MEDIUM) — `transcriptHash` ignores message
  content. Two messages with identical timestamps but different
  content do not trigger a re-render.
- **F-BUG-155** (MEDIUM) — `BeginQuiesce`/`WaitForWork` race:
  `BeginWork` checks `quiescing`, releases the lock, then
  `Add`s to the WaitGroup. A `BeginQuiesce` between the check
  and the `Add` lets new work register after the gate.

**Architecture:** Two-layer changes. Tasks 1 and 2 are
small, model-local edits (`model.go`). Task 3 changes the
viewport-height accounting. Task 4 is a `transcript.go` change.
Task 5 is a `session.go` lock-hold change. The shared theme is
"check-then-act must hold the same lock the act mutates."

**Tech Stack:** Go 1.22+, Bubble Tea v2.

---

## Global Constraints

- Every code change MUST compile: `CGO_ENABLED=1 go build ./...`
  after each task.
- Every test change MUST pass:
  `CGO_ENABLED=1 go test ./internal/app/...` after each task.
- At the end, `CGO_ENABLED=1 go test ./...` must pass.
- Commit per task with the exact message shown.
- Preserve the public `session.State` API; only internal locking
  may change.
- All new tests must use the existing test seams
  (`WithConfigLoader`, `WithProgramRunner`, `WithNow`); do not
  spin up a real Bubble Tea program.

---

## File Structure

Files modified by this plan:

- `internal/app/tui/model.go` — Tasks 1, 2, 3
- `internal/app/tui/model_test.go` — Tasks 1, 2, 3 (add tests)
- `internal/app/tui/transcript.go` — Task 4
- `internal/app/tui/transcript_test.go` — Task 4 (add test)
- `internal/app/session/session.go` — Task 5
- `internal/app/session/session_test.go` — Task 5 (add test)

---

### Task 1: F-BUG-149 — `cancelTurn` only clears state if a turn was actually cancelled

**Files:**
- Modify: `internal/app/tui/model.go` (`cancelTurn` + new
  `agentFinishedMsg` race handling)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** Today, `cancelTurn` always calls `m.state.ClearSteering()`
and adds a "cancelled" system message, even if `m.agentCancel` was
nil. The fix is twofold: (1) early-return if nothing to cancel,
(2) when a cancel is in flight but the agent finishes first, the
finishing goroutine must observe the cancellation state and avoid
double-processing the steering queue.

**Fix:** Replace `cancelTurn` with the following:

1. If `m.agentCancel == nil` and `!m.busy`, return `false` (no
   turn was running).
2. Otherwise set a `m.cancelling = true` flag *before* calling
   the cancel function.
3. Call `m.agentCancel()`; nil out `m.agentCancel`.
4. Do NOT clear the steering queue yet. Instead, leave the
   flag in place.
5. In `handleAgentFinished`, if `m.cancelling` is set, clear
   the steering queue *and* add the cancellation message; the
   agent-finished path is the only authoritative place to
   mutate post-cancel state.
6. Reset `m.cancelling = false` at the end of
   `handleAgentFinished`.

**Implementation steps:**

- [ ] **Step 1: Add the cancelling flag**

In the `Model` struct, add:

```go
cancelling bool
```

- [ ] **Step 2: Rewrite `cancelTurn`**

```go
func (m *Model) cancelTurn() bool {
    if m.agentCancel == nil && !m.busy {
        return false
    }
    m.cancelling = true
    if m.agentCancel != nil {
        m.agentCancel()
        m.agentCancel = nil
    }
    m.refreshViewport()
    return true
}
```

- [ ] **Step 3: Move the queue clear and message into `handleAgentFinished`**

In `handleAgentFinished` (model.go:1443), add at the top:

```go
if m.cancelling {
    m.state.ClearSteering()
    m.queuedCount = 0
    m.state.AddMessage(session.RoleSystem, "Agent turn cancelled.", session.ContentTypePlain)
    m.cancelling = false
}
```

(Remove the equivalent lines from `cancelTurn`.)

- [ ] **Step 4: Add a regression test**

```go
func TestCancelTurnDoesNotClearSteeringWhenAgentAlreadyFinished(t *testing.T) {
    m := newTestModel()
    m.busy = true
    m.cancelling = false
    m.queuedCount = 1
    // Simulate agent finishing first.
    m, _ = m.Update(agentFinishedMsg{err: nil})
    if m.queuedCount != 0 {
        t.Fatalf("steering not drained on agent finish: %d", m.queuedCount)
    }
    // Now call cancelTurn — must not double-clear.
    if m.cancelTurn() {
        t.Fatalf("cancelTurn returned true with no in-flight turn")
    }
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestCancel' -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): move cancelTurn state mutation to handleAgentFinished (F-BUG-149)"
```

---

### Task 2: F-BUG-150 — Reset `successPulse` on error

**Files:**
- Modify: `internal/app/tui/model.go` (`handleAgentFinished`)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** When `msg.err != nil`, `successPulse` is left
untouched. If the previous turn succeeded, the input border still
flashes teal during the error state.

**Fix:** Add an `else if msg.err != nil` arm that sets
`m.successPulse = false`.

**Implementation steps:**

- [ ] **Step 1: Update `handleAgentFinished`**

```go
if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
    m.state.SetProviderError(msg.err)
    m.successPulse = false
} else if msg.err == nil {
    m.successPulse = true
}
```

(The existing `else if` already exists; just add the line.)

- [ ] **Step 2: Test**

```go
func TestSuccessPulseClearsOnError(t *testing.T) {
    m := newTestModel()
    m.successPulse = true
    m.busy = true
    m, _ = m.Update(agentFinishedMsg{err: errors.New("boom")})
    if m.successPulse {
        t.Fatal("successPulse should clear on error")
    }
}
```

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestSuccessPulse' -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): reset successPulse on agent error (F-BUG-150)"
```

---

### Task 3: F-BUG-151 — `inputAreaRows` includes SDD hint row

**Files:**
- Modify: `internal/app/tui/model.go` (`inputAreaRows`)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** `inputAreaRows` (model.go:981) adds `activityStripRows`
only when the activity is not idle. During SDD, the activity is
idle but the input area still renders an SDD hint line. The
viewport height is therefore off by one row and the hint is
covered by the transcript.

**Fix:** Add a third conditional that adds one row when
`m.state.SDDProgress().Active` is true.

**Implementation steps:**

- [ ] **Step 1: Locate the SDD progress accessor**

In `internal/app/session/session.go`, confirm the existence of
`SDDProgress()` (search for `sddProgress` field). Confirm the
return type's `Active` field.

- [ ] **Step 2: Update `inputAreaRows`**

```go
func (m Model) inputAreaRows() int {
    rows := inputBorderRows
    if m.state.Activity().Kind != session.ActivityIdle {
        rows += activityStripRows
    }
    if sd := m.state.SDDProgress(); sd != nil && sd.Active {
        rows++ // SDD hint row
    }
    // ... existing pending-question / pending-approval accounting ...
}
```

- [ ] **Step 3: Test**

```go
func TestInputAreaRowsIncludesSDDHint(t *testing.T) {
    m := newTestModel()
    m.state.SetSDDProgress(&session.SDDProgress{Active: true})
    rows := m.inputAreaRows()
    if rows < inputBorderRows+1 {
        t.Fatalf("expected SDD hint row, got %d", rows)
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestInputAreaRows' -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): include SDD hint row in inputAreaRows (F-BUG-151)"
```

---

### Task 4: F-BUG-152 — `transcriptHash` includes content fingerprint

**Files:**
- Modify: `internal/app/tui/transcript.go` (where
  `lastTranscriptHash` is built) and `internal/app/tui/model.go`
  (the `transcriptHash` helper)
- Add tests: `internal/app/tui/transcript_test.go`

**Problem:** The hash considers timestamp, count, width, and
flags but not message body. Two messages with identical
timestamps but different content do not trigger a re-render.

**Fix:** Include a `fnv64` hash of the concatenated
`(role|content-type|content)` triples. Use a single linear scan
already in place (do not introduce a second pass).

**Implementation steps:**

- [ ] **Step 1: Find the helper**

```bash
grep -n 'lastTranscriptHash\|transcriptHash' internal/app/tui/model.go internal/app/tui/transcript.go
```

- [ ] **Step 2: Update the hash**

```go
import "hash/fnv"

func transcriptHash(msgs []session.Message, width int, flags uint64) uint64 {
    h := fnv.New64a()
    fmt.Fprintf(h, "c=%d|w=%d|f=%d|", len(msgs), width, flags)
    for _, m := range msgs {
        fmt.Fprintf(h, "%s|%d|%s\x00", m.Role, m.CreatedAt.UnixNano(), m.Content)
    }
    return h.Sum64()
}
```

(Adjust the field names to match the actual `session.Message`
type.)

- [ ] **Step 3: Test**

```go
func TestTranscriptHashDistinguishesContent(t *testing.T) {
    a := transcriptHash([]session.Message{
        {Role: "user", CreatedAt: time.Unix(0, 1), Content: "hello"},
    }, 80, 0)
    b := transcriptHash([]session.Message{
        {Role: "user", CreatedAt: time.Unix(0, 1), Content: "goodbye"},
    }, 80, 0)
    if a == b {
        t.Fatal("hash should differ for different content")
    }
}
```

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestTranscriptHash' -v
git add internal/app/tui/transcript.go internal/app/tui/transcript_test.go internal/app/tui/model.go
git commit -m "fix(tui): include content in transcriptHash (F-BUG-152)"
```

---

### Task 5: F-BUG-155 — `BeginQuiesce`/`BeginWork` race fix

**Files:**
- Modify: `internal/app/session/session.go` (`BeginWork`,
  `BeginQuiesce`)
- Add tests: `internal/app/session/session_test.go`

**Problem:** `BeginWork` checks `quiescing`, releases
`workMu`, then `Add`s to the WaitGroup. A concurrent
`BeginQuiesce` between the check and the `Add` lets new work
register after the gate.

**Fix:** Hold `workMu` across the check and the `Add`. If a
caller already added and the gate then flipped, the caller
must `Done` to compensate.

The minimal change is:

```go
func (s *State) BeginWork() error {
    s.workMu.Lock()
    defer s.workMu.Unlock()
    if s.quiescing {
        return ErrSessionQuiescing
    }
    s.workWG.Add(1)
    return nil
}
```

(Looking at session.go:791, `BeginWork` already does this —
the lock IS held across the check and `Add`. The race described
in the audit must therefore come from elsewhere. Re-read the
audit finding and the surrounding code carefully before
implementing.)

**Implementation steps:**

- [ ] **Step 1: Re-verify the race**

Open `internal/app/session/session.go` lines 785-815 and the
related test file. Confirm the audit's claim: a test that calls
`BeginWork` and `BeginQuiesce` from two goroutines without
synchronisation.

- [ ] **Step 2: If the lock is already held, document the
  audit finding as a false positive**

If the existing implementation is correct, the task is to add
a regression test that exercises the contended path:

```go
func TestBeginWorkAndBeginQuiesceConcurrent(t *testing.T) {
    s := newTestState()
    var wg sync.WaitGroup
    for i := 0; i < 1000; i++ {
        wg.Add(2)
        go func() {
            defer wg.Done()
            _ = s.BeginWork()
            if err := s.BeginWork(); err == nil {
                s.EndWork()
            }
        }()
        go func() {
            defer wg.Done()
            s.BeginQuiesce()
        }()
    }
    wg.Wait()
    // After both gates flip, BeginWork must always return an error.
    if err := s.BeginWork(); !errors.Is(err, ErrSessionQuiescing) {
        t.Fatalf("expected ErrSessionQuiescing after BeginQuiesce, got %v", err)
    }
}
```

If the test fails, replace `BeginWork` with the
check-then-add-under-lock version (matching the existing
implementation) and re-run.

- [ ] **Step 3: Commit either the test or the fix**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/session -race -run 'TestBeginWork' -v
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "test(session): add BeginWork/BeginQuiesce race regression (F-BUG-155)"
```

(or, if a fix was needed, `fix(session): hold workMu across BeginWork check and Add`)

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test -race ./...
```

Manual smoke checklist:

- [ ] Start a long-running turn; press `Esc`. The "Agent turn
      cancelled" message appears exactly once and the steering
      queue is drained.
- [ ] Trigger an error from a tool; the input border stops
      flashing teal.
- [ ] Activate SDD mode; the SDD hint row sits *above* the
      transcript, not under it.
- [ ] Edit a past message (via the rerun UI); the viewport
      re-renders.
- [ ] Run the BeginWork/BeginQuiesce test under `-race` 100x.

Update `docs/14-codebase-improvement-audit-2026-07-14.md` with the
new resolution table entries.
