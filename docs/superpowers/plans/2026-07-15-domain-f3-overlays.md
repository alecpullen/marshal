# Domain F3 — Approval/Question Overlay Correctness Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in a
> dedicated worktree (suggested branch `feature/domain-f3-overlays`).
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Resolve seven findings from
`docs/14-codebase-improvement-audit-2026-07-14.md` (Domain F) that
concern the inline approval/question forms, their overlay-priority
behaviour, and the channel-send safety between TUI and agent runner:

- **F-UIUX-140** (MEDIUM) — `Esc` denies a tool call without any
  "are you sure" feedback. Users hit Esc intending "back to
  transcript" and accidentally abort the agent.
- **F-UIUX-141** (MEDIUM) — the question form's first field is not
  focused until the first key event; cursor doesn't blink
  immediately.
- **F-BUG-147** (MEDIUM) — opening settings while a tool is pending
  approval hides the approval form. The agent blocks forever.
- **F-BUG-148** (MEDIUM) — sending the decision on `tc.ResponseChan`
  has no nil check; `ResolvePendingForShutdown` can race with the
  TUI send and the channel has buffer 1.
- **F-BUG-153** (MEDIUM) — `settingsBlockReason` only considers
  `m.busy` and `RunningJobsCount`. A pending approval or open picker
  is not "busy", so save proceeds mid-decision.
- **F-BUG-156** (MEDIUM) — `SetPendingApproval` publishes a shallow
  copy that shares `ResponseChan`; subscribers can read the channel
  and a `ResolvePendingForShutdown` close can race with the TUI
  send.
- **F-POL-163** (LOW) — `permissionForTool` is dead code; should be
  removed in favour of `permissions.PermissionForTool`.

**Architecture:** All edits live in `internal/app/tui/` and
`internal/app/session/`. The channel-send safety work shares a
`sync.Once`-guarded `Respond` helper with F4 (BeginQuiesce race)
because both findings share the same root cause: a buffered channel
of capacity 1 with no responder ownership.

**Tech Stack:** Go 1.22+, Bubble Tea v2, `huh` v2.

---

## Global Constraints

- Every code change MUST compile: `CGO_ENABLED=1 go build ./...`
  after each task.
- Every test change MUST pass:
  `CGO_ENABLED=1 go test ./internal/app/...` after each task.
- At the end, `CGO_ENABLED=1 go test ./...` must pass.
- Commit per task with the exact message shown.
- Preserve the public `PendingToolCall.ResponseChan` type
  (`chan<- UserApprovalDecision`); only ownership of the send site
  changes.
- Channel sends to `ResponseChan` MUST go through the new
  `respondApproval` / `respondQuestion` helpers from Task 4 (no
  direct sends from the TUI).

---

## File Structure

Files modified by this plan:

- `internal/app/session/session.go` — Tasks 4, 5 (helper + event
  type)
- `internal/app/session/session_test.go` — Tasks 4, 5 (add tests)
- `internal/app/tui/approval.go` — Tasks 1, 6
- `internal/app/tui/approval_test.go` — Task 1 (add test)
- `internal/app/tui/question.go` — Task 2
- `internal/app/tui/question_test.go` — Task 2 (add test)
- `internal/app/tui/model.go` — Tasks 3, 4, 5
- `internal/app/tui/model_test.go` — Tasks 3, 4, 5 (add tests)
- `internal/app/tui/model.go` — Task 7 (remove dead
  `permissionForTool`)

---

### Task 1: F-UIUX-140 — Visible feedback when Esc denies a tool

**Files:**
- Modify: `internal/app/tui/approval.go` (`approvalModel.Update`
  Esc branch + new footer prompt)
- Add tests: `internal/app/tui/approval_test.go`

**Problem:** Pressing Esc on the approval form silently chooses
`choiceDeny` and returns. Users sometimes press Esc thinking it
"closes" the form, then the agent reports the tool was denied and
they have no idea why.

**Fix:** A second Esc within 1.5 s of the first confirms the deny.
On the *first* Esc, swap the form into a "press Esc again to deny,
any other key to cancel" state with a brief flash banner. This
preserves the original "Esc cancels" intent while preventing
accidental denies, per the tui-design skill's "moderate action:
inline confirmation" pattern.

**Implementation steps:**

- [ ] **Step 1: Add state to `approvalModel`**

In `internal/app/tui/approval.go`:

```go
type approvalModel struct {
    // ... existing fields ...
    pendingDenyAt time.Time // zero means no pending deny
}
```

- [ ] **Step 2: Update the Esc branch**

```go
case "esc":
    if am.pendingDenyAt.IsZero() {
        am.pendingDenyAt = time.Now()
        return am, nil
    }
    if time.Since(am.pendingDenyAt) <= 1500*time.Millisecond {
        am.done = true
        am.choice = choiceDeny
        return am, nil
    }
    am.pendingDenyAt = time.Time{} // expired; treat as new first press
    return am, nil
```

- [ ] **Step 3: Update the View**

When `pendingDenyAt` is non-zero, render a one-line warning above
the form (or below the existing prompt) that explains "press Esc
again to deny · any other key cancels".

- [ ] **Step 4: Add tests**

```go
func TestEscRequiresDoublePressToDeny(t *testing.T) {
    am := newTestApprovalModel()
    am, _ = am.Update(keyPress("esc"))
    if am.IsDone() {
        t.Fatalf("first Esc should not complete the form")
    }
    am, _ = am.Update(keyPress("esc"))
    if !am.IsDone() || am.Choice() != choiceDeny {
        t.Fatalf("second Esc should deny; got done=%v choice=%v", am.IsDone(), am.Choice())
    }
}

func TestEscAfterOtherKeyResetsPendingDeny(t *testing.T) {
    am := newTestApprovalModel()
    am, _ = am.Update(keyPress("esc"))
    am, _ = am.Update(keyPress("down"))
    if !am.pendingDenyAt.IsZero() {
        t.Fatalf("non-Esc key should reset pending deny")
    }
}
```

(`keyPress` is a small helper in the test file that wraps
`tea.KeyPressMsg`.)

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestEsc' -v
git add internal/app/tui/approval.go internal/app/tui/approval_test.go
git commit -m "feat(tui/approval): double-Esc to deny (F-UIUX-140)"
```

---

### Task 2: F-UIUX-141 — Question form focuses the first field

**Files:**
- Modify: `internal/app/tui/question.go` (`newQuestionModel`
  Init/focus handling)
- Add tests: `internal/app/tui/question_test.go`

**Problem:** `newQuestionModel` calls `qm.form.Init()` but
discards the returned command. The first field may not receive a
focus message until the first keypress.

**Fix:** Surface the `Init` command and return it from the
constructor's caller (`model.handleQuestion`). Additionally, after
the form is built, call `Focus()` on the first field via the
public `huh.Form` API if available; otherwise ensure the init
command is dispatched.

**Implementation steps:**

- [ ] **Step 1: Store the init command**

```go
qm.initCmd = qm.form.Init()
```

(Add `initCmd tea.Cmd` to the struct.)

- [ ] **Step 2: Expose it**

```go
func (qm *questionModel) Init() tea.Cmd { return qm.initCmd }
```

- [ ] **Step 3: Return it from the parent**

In `internal/app/tui/model.go` `handleQuestion`, after
constructing the model, return its `Init()` cmd:

```go
if m.questionModel == nil {
    m.questionModel = newQuestionModel(q, max(m.width-4, 30))
    return m, m.questionModel.Init()
}
```

- [ ] **Step 4: Test**

A test that constructs a `questionModel`, calls `Init()`,
performs the returned command, and asserts that the form's
`Focused()` returns true for the first field.

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestQuestion' -v
git add internal/app/tui/question.go internal/app/tui/question_test.go internal/app/tui/model.go
git commit -m "fix(tui/question): surface Init() command for focus (F-UIUX-141)"
```

---

### Task 3: F-BUG-147 — Block opening overlays while a tool is pending

**Files:**
- Modify: `internal/app/tui/model.go` (`Update` overlay-open
  paths for settings and memory)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** If the user presses `Ctrl+O` (settings) while an
approval is pending, the settings overlay opens; the approval form
disappears; the agent blocks on `tc.ResponseChan` indefinitely.

**Fix:** When a pending approval/question exists, intercept the
shortcut and surface a transient footer message ("Resolve the
pending approval to open settings"). Same for memory browser
(`Ctrl+K`).

**Implementation steps:**

- [ ] **Step 1: Add the guard**

In `Update`, locate the keypress handler that opens settings
(search for `settingsOpen = true` or the `ctrl+o` arm). Add at
the top of the branch:

```go
if m.state.PendingApproval() != nil || m.state.PendingQuestion() != nil {
    m.state.AddMessage(session.RoleSystem,
        "Resolve the pending tool decision before opening settings.",
        session.ContentTypePlain)
    m.refreshViewport()
    return m, nil
}
```

Repeat for the memory browser handler (`ctrl+k`).

- [ ] **Step 2: Add a regression test**

Construct a `Model` with a non-nil `PendingApproval` (use the
test seam exposed by `session.SetPendingApproval`). Dispatch a
`ctrl+o` `tea.KeyPressMsg` and assert that `m.settingsOpen` stays
false and a system message was added.

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestSettingsBlockedByApproval' -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): block overlay open while approval pending (F-BUG-147)"
```

---

### Task 4: F-BUG-156 + F-BUG-148 — Guarded channel sends and a saner event payload

**Files:**
- Modify: `internal/app/session/session.go` (`PendingToolCall`,
  `PendingQuestion`, `SetPendingApproval`, `SetPendingQuestion`,
  `Event` payload type)
- Add tests: `internal/app/session/session_test.go`
- Modify: `internal/app/tui/model.go` (replace direct
  `tc.ResponseChan <-` with `respondApproval`)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** Two related channel-send races:

1. `SetPendingApproval` (session.go:1202-1212) publishes a
   `*PendingToolCall` whose `ResponseChan` is the same channel
   the TUI later writes to. A subscriber that reads the snapshot
   and the `ResolvePendingForShutdown` close-on-shutdown path
   both touch the channel after the owner (TUI) has been racing
   for the same slot.
2. `handleApproval` / `handleQuestion` (model.go:849, 882–924,
   961+) send directly to `tc.ResponseChan` with no `sync.Once`
   or nil-check, so a second `Enter` after the form is already
   `done` blocks on the buffered send.

**Fix:**

- Make the public event payload opaque. Add a new
  `PendingToolCallInfo` struct in `session` that carries
  everything TUI subscribers need *except* the channel, plus a
  per-pending-call `*sync.Once` responder.
- The agent-runtime path keeps the full `*PendingToolCall`
  privately inside `state`; the public getter
  `PendingApproval()` returns the full pointer (used by the
  TUI's `handleApproval` to call the responder), and the
  `publishEvent` call now publishes `PendingToolCallInfo`.
- The TUI calls `tc.Respond(session.UserApprovalDecision{...})`
  instead of `tc.ResponseChan <- ...`. `Respond` uses
  `sync.Once` internally.

**Implementation steps:**

- [ ] **Step 1: Add the info type and responder**

In `internal/app/session/session.go`, define:

```go
type PendingToolCallInfo struct {
    ID         string
    Name       string
    Command    string
    Args       string
    RiskLevel  string
    Diff       string
    Schema     string
    HasBackup  bool
}

type PendingQuestionInfo struct {
    ID        string
    Questions []QuestionSummary // a small DTO of session.Question
}
```

- [ ] **Step 2: Refactor `PendingToolCall`**

Replace the embedded `ResponseChan` with a `respondOnce sync.Once`
and a `respond func(UserApprovalDecision)` closure built when the
pending call is created (or replace with a method):

```go
type PendingToolCall struct {
    ID          string
    Name        string
    Command     string
    Args        string
    RiskLevel   string
    Diff        string
    Schema      string
    HasBackup   bool
    ResponseChan chan UserApprovalDecision // unexported ownership; see below

    responded sync.Once
}

func (p *PendingToolCall) Respond(d UserApprovalDecision) {
    if p == nil {
        return
    }
    p.responded.Do(func() {
        p.ResponseChan <- d
        close(p.ResponseChan)
    })
}
```

For `PendingQuestion`:

```go
type PendingQuestion struct {
    ID         string
    Questions  []Question
    ResponseChan chan []Answer

    responded sync.Once
}

func (p *PendingQuestion) Respond(a []Answer) {
    if p == nil {
        return
    }
    p.responded.Do(func() {
        p.ResponseChan <- a
        close(p.ResponseChan)
    })
}
```

- [ ] **Step 3: Update `SetPendingApproval` to publish info only**

```go
func (s *State) SetPendingApproval(tc *PendingToolCall) {
    s.mu.Lock()
    s.pendingApproval = tc
    s.mu.Unlock()
    var info *PendingToolCallInfo
    if tc != nil {
        info = &PendingToolCallInfo{
            ID: tc.ID, Name: tc.Name, Command: tc.Command,
            Args: tc.Args, RiskLevel: tc.RiskLevel,
            Diff: tc.Diff, Schema: tc.Schema, HasBackup: tc.HasBackup,
        }
    }
    s.publishEvent(EventPendingApprovalChanged, Event{PendingApprovalInfo: info})
}
```

Add `PendingApprovalInfo *PendingToolCallInfo` to the `Event`
struct (additive). `PendingQuestion` mirrors the same change.

Update existing event consumers to read `*Info` where they
currently read `*PendingToolCall` (search
`internal/app/tui`/`internal/acp`).

- [ ] **Step 4: Update `ResolvePendingForShutdown` to use `Respond(nil)`**

```go
func (s *State) ResolvePendingForShutdown() {
    s.mu.Lock()
    tc := s.pendingApproval
    q := s.pendingQuestion
    s.mu.Unlock()
    if tc != nil {
        tc.Respond(UserApprovalDecision{Approved: false})
    }
    if q != nil {
        // Empty answers == declined by shutdown.
        q.Respond(nil)
    }
    s.SetPendingApproval(nil)
    s.SetPendingQuestion(nil)
}
```

- [ ] **Step 5: Replace direct channel sends in the TUI**

In `internal/app/tui/model.go` `handleApproval`, replace every
`tc.ResponseChan <- session.UserApprovalDecision{...}` with
`tc.Respond(session.UserApprovalDecision{...})`. Same for
`handleQuestion`'s `q.ResponseChan <- answers` →
`q.Respond(answers)`.

- [ ] **Step 6: Add a nil guard in `handleApproval`**

Before calling `tc.Respond`, assert that `m.state.PendingApproval()
== tc`; if not, the pending was already resolved and we should
just clean up local state without sending. (This is the fix
described in F-BUG-148.)

- [ ] **Step 7: Tests**

```go
func TestRespondIsIdempotent(t *testing.T) {
    tc := &PendingToolCall{ResponseChan: make(chan UserApprovalDecision, 1)}
    tc.Respond(UserApprovalDecision{Approved: true})
    // Must not panic on second call.
    tc.Respond(UserApprovalDecision{Approved: false})
    select {
    case <-tc.ResponseChan:
    case <-time.After(100*time.Millisecond):
        t.Fatal("expected one response")
    }
    select {
    case _, ok := <-tc.ResponseChan:
        if ok {
            t.Fatal("channel should be closed after Respond")
        }
    case <-time.After(100*time.Millisecond):
        t.Fatal("expected channel to be closed")
    }
}

func TestEventInfoHidesResponseChan(t *testing.T) {
    s := newTestState()
    tc := &PendingToolCall{ID: "x", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
    s.SetPendingApproval(tc)
    var info *PendingToolCallInfo
    s.Subscribe(func(e Event) {
        if e.PendingApprovalInfo != nil {
            info = e.PendingApprovalInfo
        }
    })
    s.SetPendingApproval(tc) // republish for subscriber to fire
    if info == nil || info.ID != "x" {
        t.Fatalf("info not delivered")
    }
}
```

Plus a TUI test that simulates a double-Enter on a completed
form and asserts no panic, no extra send, and the second Enter
just clears the form.

- [ ] **Step 8: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/... -v
git add internal/app/session/session.go internal/app/session/session_test.go internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(session+tui): guard approval/question responders (F-BUG-148, F-BUG-156)"
```

---

### Task 5: F-BUG-153 — Extend `settingsBlockReason`

**Files:**
- Modify: `internal/app/tui/model.go` (`settingsBlockReason`)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** Save is only blocked by `m.busy` and
`RunningJobsCount`. A pending approval or open picker is not
considered "busy", so the user can save settings while a
decision is pending.

**Fix:** Extend `settingsBlockReason` to also check
`m.state.PendingApproval() != nil`, `m.state.PendingQuestion()
!= nil`, and `m.pickerModel != nil`. Each returns a distinct
`settingsBusyMessage` variant so the diff overlay can render
the reason inline.

- [ ] **Step 1: Update the function**

```go
func (m Model) settingsBlockReason() string {
    if m.busy || m.state.RunningJobsCount() > 0 {
        return settingsBusyMessage
    }
    if m.state.PendingApproval() != nil {
        return "Resolve the pending tool approval to save."
    }
    if m.state.PendingQuestion() != nil {
        return "Answer the pending question to save."
    }
    if m.pickerModel != nil {
        return "Close the picker to save."
    }
    return ""
}
```

- [ ] **Step 2: Test**

Construct a `Model` with a non-nil `PendingApproval`; call
`settingsBlockReason`; assert the new message is returned.

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestSettingsBlock' -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "fix(tui): block settings save on pending decisions (F-BUG-153)"
```

---

### Task 6: F-POL-163 — Remove dead `permissionForTool`

**Files:**
- Modify: `internal/app/tui/model.go` (remove the function)
- Add tests: `internal/app/tui/model_test.go` (regression
  sentinel: build must not reference the symbol)

**Problem:** `permissionForTool` (model.go:2087-2096) maps tool
names to permission strings but is never called.

**Fix:** Delete the function and its doc comment. Replace the
single live call site (`permissions.PermissionForTool(tc.Name)`
already used at line 893) — confirm via `grep` that nothing else
references it.

- [ ] **Step 1: Find every reference**

```bash
grep -rn 'permissionForTool\b' --include='*.go'
```

- [ ] **Step 2: Delete the function**

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "refactor(tui): remove dead permissionForTool (F-POL-163)"
```

---

## Final verification

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
```

Manual smoke checklist:

- [ ] Trigger an approval, press `Esc`, then press `Esc` again
      within 1.5 s — the form denies and the footer flashes the
      confirmation. Press `Esc`, then `↓` — pending deny is reset.
- [ ] Open a question form; verify the cursor blinks in the
      first field immediately.
- [ ] While a tool approval is pending, press `Ctrl+O`; settings
      does **not** open and a system message explains why.
- [ ] Trigger a rapid double-Enter on a completed approval form;
      the channel is not double-sent (no deadlock, no duplicate
      message in agent logs).
- [ ] With a pending approval, open `/settings` via the slash
      command; the save control shows the new block reason.

Update `docs/14-codebase-improvement-audit-2026-07-14.md` with the
new resolution table entries.
