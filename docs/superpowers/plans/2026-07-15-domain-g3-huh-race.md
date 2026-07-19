# Domain G3 — `huh` Form Completion Race Residual (F-XCUT-188 / F-BUG-14)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the residual race in F-BUG-14 / F-XCUT-188 where the `huh` form's internal `Update` may return `done=true` (or trigger a sub-form completion) at the same instant the parent `approvalModel.Update` is processing an `Esc` keypress. The F3 plan covers the channel-send race via `sync.Once`; this plan covers the *form-level* race that the responder alone cannot prevent.

**Architecture:** Two small changes:

1. The sub-form `Update` (`approvalModel.Update`, `questionModel.Update`) sets a local `done` flag and returns the model without dispatching to a responder. The parent `model.go` `handleApproval`/`handleQuestion` is the single place that calls `tc.Respond(...)`, and it only does so after asserting that the sub-model's `done` flag is true **and** `state.PendingApproval() == tc` (so a `ResolvePendingForShutdown` close on a different slot cannot cause a double-respond).
2. `ResolvePendingForShutdown` calls `Respond` (covered by F3 Task 4); this plan tightens the order so that the sub-form's `done` flag and the parent responder call share the same mutex segment.

**Tech Stack:** Go 1.22+; stdlib only.

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the
  tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after each
  implementation step.
- Every test change MUST pass: run `go test ./internal/app/tui -run <TestName>`
  for the new test, then `go test ./internal/app/tui -count=1` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.

## File Structure

Files modified or created by this plan:

- `internal/app/tui/approval.go` — Task 1 (add `done` flag check; refuse to dispatch if not done).
- `internal/app/tui/approval_test.go` — Task 1 (new test).
- `internal/app/tui/question.go` — Task 1 (same change for the question form).
- `internal/app/tui/question_test.go` — Task 1 (new test).
- `internal/app/tui/model.go` — Task 2 (parent dispatch guard + slot identity check).
- `internal/app/tui/model_test.go` — Task 2 (race regression test).

---

### Task 1: Sub-form `Update` only completes when the user actually confirms

**Files:**
- Modify: `internal/app/tui/approval.go` (`approvalModel.Update` — add `done` flag set path).
- Modify: `internal/app/tui/question.go` (`questionModel.Update` — same pattern).
- Add tests in respective `*_test.go` files.

**Problem:** `huh`'s form can return `done=true` from a sub-form
completion (e.g. the user hit Enter on the last field) at the same
time the parent receives an `Esc` from a different `tea.Msg`
interleaving. Both paths then race to call the responder (or, in
the pre-F3 world, both attempt a buffered send on
`tc.ResponseChan`).

**Fix:** Have the sub-model's `Update` set a `done` flag on the
model struct and `return (am, nil)` without invoking any side
effect. The parent's `handleApproval`/`handleQuestion` is the
single dispatcher: it consults `sub.done` and `state.PendingApproval()`
identity before calling `tc.Respond(...)`.

**Implementation steps:**

- [ ] **Step 1: Add the `done` flag**

In `internal/app/tui/approval.go`, ensure the `approvalModel` struct
has a `done bool` (and probably already does from the existing
"completion" path — verify and rename if it's currently
`am.done = true` set deep inside a `huh` callback).

- [ ] **Step 2: Refuse to dispatch from the sub-form**

In `approvalModel.Update`, the existing code may have a branch
like:

```go
if am.form.State == huh.StateCompleted {
    am.choice = choiceApprove // or similar
    am.done = true
    return am, nil
}
```

Keep that. The fix is to **remove any direct call to a
responder** (there should not be one here today, but the audit
found some in earlier versions — verify) and to keep the
sub-model pure: the only output is `(am, nil)`. The parent
inspects `am.done` in its own `Update`.

- [ ] **Step 3: Repeat for the question form**

Same change in `internal/app/tui/question.go`.

- [ ] **Step 4: Test**

```go
func TestApprovalSubFormMarksDoneWithoutDispatching(t *testing.T) {
    am := newTestApprovalModel()
    am, _ = am.Update(simulateHuhCompletion()) // a tea.Msg that huh would send
    if !am.done {
        t.Fatalf("sub-form completion should set done")
    }
    if am.choice != choiceApprove {
        t.Fatalf("expected choiceApprove from completion; got %v", am.choice)
    }
}
```

The `simulateHuhCompletion` helper is constructed by inspecting
`huh.Form` internals — if the public API doesn't expose a
synthetic completion message, drive the form by tabbing through
the fields and pressing Enter, which is the real-world path
that triggers `done=true`.

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run TestApprovalSubForm -v
git add internal/app/tui/approval.go internal/app/tui/approval_test.go internal/app/tui/question.go internal/app/tui/question_test.go
git commit -m "fix(tui): sub-form completion isolated to sub-model (F-XCUT-188)"
```

---

### Task 2: Parent `handleApproval` / `handleQuestion` guards the dispatch

**Files:**
- Modify: `internal/app/tui/model.go` (`handleApproval`, `handleQuestion`).
- Modify: `internal/app/tui/model_test.go` (regression test).

**Problem:** The parent TUI's `Update` receives a `tea.Msg` from
the sub-form (which is now a "completion" message indicating
`am.done = true`) and also an `Esc` keypress that may arrive in
the same `Update` call. Both reach `handleApproval` /
`handleQuestion` (or the equivalent branch) and either may
attempt to call `tc.Respond(...)`. Even with `sync.Once`, calling
`Respond` on a slot that has already been `ResolvePendingForShutdown`-ed
emits a no-op but consumes a `tea.Cmd` budget unnecessarily, and a
double-call on a different `tc` pointer (e.g. after a reload) would
respond to the wrong request.

**Fix:** In the parent dispatch, before calling `tc.Respond(...)`:

1. Check `state.PendingApproval() == tc` (or
   `state.PendingQuestion() == tq` for the question variant).
2. Check the sub-model's `done` flag is true (so we don't
   respond to a stray `Esc` that arrived before the sub-form
   was actually completed).

If either check fails, return without dispatching.

**Implementation steps:**

- [ ] **Step 1: Find the dispatch site**

In `internal/app/tui/model.go`, locate where
`tc.ResponseChan <- ...` was (now replaced with `tc.Respond(...)`
per F3 Task 4). The new code is in the `handleApproval` arm of
`Update` (around line 800-900). Add the two guards immediately
before the `tc.Respond(...)` call.

- [ ] **Step 2: Add the guards**

```go
case am, ok := m.approvalModel.Update(msg):
    m.approvalModel = am
    if m.approvalModel.IsDone() {
        tc := m.state.PendingApproval()
        if tc != nil && tc == m.approvalModel.ownedPending { // see step 3
            switch m.approvalModel.Choice() {
            case choiceApprove:
                tc.Respond(approveDecision(...))
            case choiceDeny:
                tc.Respond(denyDecision(...))
            }
        }
        m.approvalModel = nil
    }
    return m, nil
```

- [ ] **Step 3: Track the owned pending pointer**

Add a field `ownedPending *session.PendingToolCall` to
`approvalModel` and set it in the constructor. This is the
identity check the guard uses.

- [ ] **Step 4: Test**

```go
func TestHandleApprovalRejectsForeignPending(t *testing.T) {
    m := newTestModel()
    m.approvalModel = newTestApprovalModel()
    m.approvalModel.ownedPending = &session.PendingToolCall{ID: "A"}

    // State advances and the "real" pending becomes something else
    m.state.SetPendingApproval(&session.PendingToolCall{ID: "B"})

    // Sub-form says it's done
    m.approvalModel.done = true
    m.approvalModel.choice = choiceApprove

    // Drive Update with a noop msg
    m, _ = m.Update(tickMsg{})

    // Pending A should NOT have been responded to
    if m.state.PendingApproval().ID != "B" {
        t.Fatalf("guard should not have responded to foreign pending")
    }
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run TestHandleApproval -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): parent dispatch guards respond to sub-form completion (F-XCUT-188)"
```

---

## Self-Review

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./internal/app/tui -count=1
```

Update the audit doc with:

```markdown
### Batch 25 (G3 — huh form completion race residual): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-XCUT-188 (residual) | RESOLVED | Sub-form `Update` no longer dispatches; parent `handleApproval`/`handleQuestion` guard dispatch on `sub.done` and `state.PendingApproval() == tc`. Complements F3 Task 4 (sync.Once). 3 new tests. |
```

Note: F-BUG-14 / F-XCUT-188 was a HIGH-severity finding. F3 Task 4
closed the channel-send half; this plan closes the form-state half.
