# Domain D3 — Tool Approval Re-evaluation & Rewrite Audit Fixes

> **Status:** Implementation plan for Batch D3 of the Domain D audit.
> **Branch:** `feature/domain-d-agent-runtime` (worktree at `.worktrees/domain-d-agent-runtime`)
> **Affected findings:** F-BUG-41 (partial — logging gap), F-POL-88 (logging gap), F-SEC-82 (original args in audit event)

## Goal

Resolve the three open findings from the codebase audit that relate to tool-call argument editing, policy re-evaluation after edit, and audit-event recording of pre-rewrite arguments.

## Architecture

- **Runner state hygiene:** The `handlePolicyDecision` function (already updated by D2) validates edited JSON, re-normalises args, re-evaluates policy, and denies on `DecisionDeny`. What remains is a silent error discard on the shell branch's `normalizeArgs` call and the absence of structured warning logs.
- **Audit event extensibility:** `AuditEvent` (in `internal/tools/registry/audit.go`) gets two new optional fields: `OriginalArgs json.RawMessage` and `Rewritten bool`. These are additive — older audit rows round-trip correctly. The `executeToolCall` rewrite loop captures the pre-hook args and stamps them on the event.
- **DB schema:** The `tool_calls` table gets two new nullable columns: `original_args_json TEXT` and `rewritten INTEGER DEFAULT 0`. This matches the existing pattern used for other additive columns (`command_exit_code`, `files_changed`, etc.).

## Tech Stack

Go stdlib only — `log/slog`, `encoding/json`, `database/sql`.

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build: `CGO_ENABLED=1 go build ./...`
- Tests: `go test ./internal/agent/...` after each task; `go test ./...` at the end.
- All changes are backward-compatible: DB columns are `ADD COLUMN`; audit struct fields are `omitempty` / optional.
- Public API of `Runner`, `PolicyEngine`, and registry types must not change — only additive changes to `AuditEvent` struct.

---

## File Structure

Files modified:

| File | Change |
|------|--------|
| `internal/agent/runner.go` | Add warning log in shell branch (F-BUG-41/F-POL-88); capture original args in rewrite loop (F-SEC-82) |
| `internal/agent/runner_test.go` | New tests for F-BUG-41/F-POL-88 and F-SEC-82 |
| `internal/tools/registry/audit.go` | Add `OriginalArgs` and `Rewritten` fields to `AuditEvent` |
| `internal/tools/registry/audit_test.go` | Test round-trip of new fields |
| `internal/db/audits.go` | Serialize/deserialize `original_args_json` and `rewritten` columns |
| `internal/db/db.go` | Register new columns in the migration `columnDefs` map |
| `internal/db/migrations.go` | Add `original_args_json` and `rewritten` columns to CREATE TABLE |

---

### Task 1: F-BUG-41 / F-POL-88 — Add warning log for silent error discard in shell branch

**Files:**
- Modify: `internal/agent/runner.go:1159-1165`
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `slog.Default().Warn(...)` (stdlib)
- Produces: structured warning in the agent's log when `normalizeArgs` fails after a shell command edit; the error is still discarded for execution, but the log trail is useful.

- [ ] **Step 1: Write the failing test**

The shell branch at line 1164 silently discards `normalizedArgs, _ = normalizeArgs(args)`. This test verifies that after the fix, the runner still executes correctly (the log is not observable from Go tests by default, but we can verify no regression in behaviour).

The existing test `TestRunnerNonShellToolApprovalAndJSONEditing` (line 2237) already exercises the non-shell edit path. A companion test for the shell branch edit path exists at `TestRunnerReevaluatesPolicyAfterEditedArgs` (line 750) but tests invalid JSON.

No new test is strictly required for this logging-only change; the existing tests already cover the happy path. We will add a test for the shell edit path that exercises a valid command edit:

```go
func TestRunnerShellEditNormalizesSuccessfully(t *testing.T) {
    reg := registry.New()
    var calledArgs string
    if err := reg.Register(registry.Tool{
        Name: "shell.run", Risk: registry.RiskCommand,
        Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
            calledArgs = string(call.Args)
            return registry.ToolResult{Summary: "ran"}, nil
        },
    }); err != nil {
        t.Fatal(err)
    }
    p := &scriptedProvider{responses: []string{
        `{"rationale":"run","action":{"type":"tool_call","tool":"shell.run","args":{"command":"echo hello"}}}`,
        `{"rationale":"done","action":{"type":"final","content":"done"}}`,
    }}
    state := newTestState(t)
    pol := policy.NewEngine(&config.Config{}, nil)
    runner := NewRunner(p, reg, pol, state, "test-model")
    runner.SetForceClass(string(ClassQuestion))

    go func() {
        for state.PendingApproval() == nil {
            time.Sleep(10 * time.Millisecond)
        }
        tc := state.PendingApproval()
        tc.ResponseChan <- session.UserApprovalDecision{
            Approved: true,
            Edited:   "echo edited",
        }
    }()

    if err := runner.Run(context.Background(), "run"); err != nil {
        t.Fatalf("Run: %v", err)
    }
    if !strings.Contains(calledArgs, "echo edited") {
        t.Errorf("calledArgs = %q, want to contain 'echo edited'", calledArgs)
    }
}
```

- [ ] **Step 2: Run the new test to verify it passes with current code**

Run: `go test ./internal/agent -run 'TestRunnerShellEditNormalizesSuccessfully' -v`
Expected: PASS (the shell edit path already works; this is a regression guard).

- [ ] **Step 3: Add warning log to the shell branch**

In `internal/agent/runner.go`, line 1164, change:
```go
normalizedArgs, _ = normalizeArgs(args)
```
to:
```go
normalizedArgs, nerr = normalizeArgs(args)
if nerr != nil {
    slog.Default().Warn("tool-arg-edit normalize failed", "tool", toolName, "error", nerr)
}
```

Add `"log/slog"` to the imports if not already present.

- [ ] **Step 4: Build and run tests**

Run: `go build ./... && go test ./internal/agent -count=1`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "fix(agent): log normalizeArgs warning on shell edit (F-BUG-41/F-POL-88)"
```

---

### Task 2: F-SEC-82 — Audit event records original approved args

**Files:**
- Modify: `internal/tools/registry/audit.go` — add `OriginalArgs` and `Rewritten` fields
- Modify: `internal/tools/registry/audit_test.go` — test round-trip of new fields
- Modify: `internal/agent/runner.go` — capture pre-hook args in rewrite loop
- Modify: `internal/agent/runner_test.go` — test that audit event records original args
- Modify: `internal/db/audits.go` — serialize/deserialize new fields
- Modify: `internal/db/db.go` — register new columns in migration
- Modify: `internal/db/migrations.go` — add new columns to schema

**Interfaces:**
- Consumes: `AuditEvent.OriginalArgs json.RawMessage` (additive, optional, `omitempty` not needed since `json.RawMessage` nil encodes as `null`)
- Consumes: `AuditEvent.Rewritten bool` (additive, zero value is `false`)
- Produces: When a `pre_tool_use` hook rewrites args after user approval, the audit event captures both the pre-hook (user-approved) args and the final (executed) args, plus a `Rewritten=true` flag.

- [ ] **Step 1: Add fields to `AuditEvent` struct**

In `internal/tools/registry/audit.go`, add after `Error` field:
```go
// OriginalArgs holds the user-approved args before any pre_tool_use hook
// rewrite. Nil when no rewrite occurred.
OriginalArgs json.RawMessage `json:"original_args,omitempty"`

// Rewritten is true when a pre_tool_use hook rewrote the tool arguments
// after user approval.
Rewritten bool `json:"rewritten,omitempty"`
```

- [ ] **Step 2: Add round-trip test for the new fields**

In `internal/tools/registry/audit_test.go`, add:
```go
func TestNewAuditEventRoundTripsOriginalArgs(t *testing.T) {
    now := time.Unix(456, 0)
    tool := testTool("shell.run")
    tool.Risk = RiskCommand
    call := ToolCall{
        ID:   "call-rewrite",
        Name: "shell.run",
        Args: json.RawMessage(`{"command":"git --no-pager log"}`),
    }
    result := ToolResult{Summary: "ran"}
    event := NewAuditEvent(now, tool, call, result, ApprovalApproved, nil)
    event.OriginalArgs = json.RawMessage(`{"command":"git status"}`)
    event.Rewritten = true

    if string(event.OriginalArgs) != `{"command":"git status"}` {
        t.Fatalf("OriginalArgs = %s", event.OriginalArgs)
    }
    if !event.Rewritten {
        t.Fatal("Rewritten should be true")
    }
    if string(event.Args) != `{"command":"git --no-pager log"}` {
        t.Fatalf("Args = %s", event.Args)
    }

    // Verify that a non-rewritten event has zero values.
    event2 := NewAuditEvent(now, tool, call, result, ApprovalApproved, nil)
    if event2.OriginalArgs != nil {
        t.Fatalf("OriginalArgs = %s, want nil", event2.OriginalArgs)
    }
    if event2.Rewritten {
        t.Fatal("Rewritten should be false")
    }
}
```

- [ ] **Step 3: Run the test to verify it passes**

Run: `go test ./internal/tools/registry -run 'TestNewAuditEventRoundTripsOriginalArgs' -v`
Expected: PASS.

- [ ] **Step 4: Add DB columns**

In `internal/db/migrations.go`, add to the `tool_calls` CREATE TABLE:
```sql
    original_args_json TEXT,
    rewritten INTEGER DEFAULT 0
```

In `internal/db/db.go`, add to the `columnDefs` map:
```go
"original_args_json": "TEXT",
"rewritten":          "INTEGER DEFAULT 0",
```

- [ ] **Step 5: Update `SaveToolCall` and `GetToolCalls` in `audits.go`**

In `SaveToolCall` (lines 65-87 of `audits.go`):
- Add `originalArgsJSON` variable: `var originalArgsJSON any; if len(event.OriginalArgs) > 0 { originalArgsJSON = string(event.OriginalArgs) }`
- Add `rewritten` variable: `rewritten := 0; if event.Rewritten { rewritten = 1 }`
- Add columns to the INSERT: `original_args_json` and `rewritten`
- Add the values to the value list

In `GetToolCalls`:
- Add scan variables: `var origArgs sql.NullString; var rewritten sql.NullInt64`
- Add to the SELECT query: `original_args_json, rewritten`
- Add to `rows.Scan`: `&origArgs, &rewritten` 
- After scan: `if origArgs.Valid { e.OriginalArgs = json.RawMessage(origArgs.String) }; if rewritten.Valid { e.Rewritten = rewritten.Int64 == 1 }`

- [ ] **Step 6: Capture pre-hook args in the rewrite loop**

In `internal/agent/runner.go`, in `executeToolCall`, the rewrite loop starts at line 1277.

Before the hook call at line 1307, capture the current args as the user-approved version:
```go
// Capture the user-approved args before any hook rewrite, so the
// audit event can record both the original and final args.
originalApprovedArgs := args

rewrittenArgs, hookOut, hookErr := r.runPreToolUseHook(ctx, toolName, args)
```

If a rewrite happens (the loop continues), these captured args are the original approved args.

After the loop breaks (line 1331), add logic when building the audit event to stamp `OriginalArgs` and `Rewritten`. However, the audit event is built in multiple places after the loop (lines 1310, 1317, 1369, 1386). The cleanest approach is to set the fields on the event right after creation when a rewrite occurred.

Instead, I'll use a different approach: after the loop, determine if a rewrite occurred by comparing the final args with the pre-hook args. But a simpler approach: track whether a rewrite happened with a boolean.

At line 1277, before the loop, declare:
```go
var originalApprovedArgs json.RawMessage
```

After the hook call, if `len(hookOut.Rewrite) > 0`, set `originalApprovedArgs` to the pre-hook args (which were captured before the hook call).

Then at each audit event creation site, pass the original args and rewritten flag.

Actually, the simplest approach: use a closure or just set the fields after the event is created. Let me trace all audit event creation sites after the loop:

1. Line 1310: Hook error — this is for the *original* args that were blocked. `OriginalArgs` is not relevant here.
2. Line 1317: Hook block — same, original args are not rewritten.
3. Line 1369: Tool execution error — AFTER the loop, all rewrites done. Set `OriginalArgs` and `Rewritten`.
4. Line 1386: Tool execution success — AFTER the loop. Set `OriginalArgs` and `Rewritten`.

So I only need to handle sites 3 and 4.

Implementation:
```go
var originalApprovedArgs json.RawMessage
var toolWasRewritten bool
for rewriteCount := 0; ; rewriteCount++ {
    // ... existing parse/eval/approval ...
    
    // Capture the user-approved args before hook
    preHookArgs := args
    
    rewrittenArgs, hookOut, hookErr := r.runPreToolUseHook(ctx, toolName, args)
    // ... handle hook error/block/halt ...
    if len(hookOut.Rewrite) > 0 {
        originalApprovedArgs = preHookArgs
        toolWasRewritten = true
        args = rewrittenArgs
        continue
    }
    break
}
```

Then at the audit event creation:
```go
event := registry.NewAuditEvent(...)
event.OriginalArgs = originalApprovedArgs
event.Rewritten = toolWasRewritten
```

Wait, but the hook error and block sites also create events. For those, OriginalArgs should be nil because no rewrite happened. That's fine because `originalApprovedArgs` would be nil at that point since we only set it when `len(hookOut.Rewrite) > 0`.

Actually wait, for the hook error/block case, the args being passed to the audit event are the *approved* args (pre-hook). The block happens before any rewrite. So OriginalArgs would be nil which is correct.

Let me now think about what happens if the user edits the args AND then a hook rewrites. In that case, `handlePolicyDecision` returns edited args in `policyResult.Args`. Then the hook runs on those edited args. If it rewrites, `originalApprovedArgs` captures the user-edited version. This is correct.

But wait, what if the user edits in the approval dialog AND then a hook rewrites? In `handlePolicyDecision`, when the user edits:
- For shell: `argsMap["command"] = edited` and re-marshals
- For other: parses the edited JSON

The returned `args` from `handlePolicyDecision` contains the user-edited version. Then the hook runs on those args. If the hook rewrites, `originalApprovedArgs` captures the user-edited args. This is the correct behavior.

- [ ] **Step 7: Write test for F-SEC-82**

In `internal/agent/runner_test.go`, add a test that simulates a pre-tool hook rewriting `git status` to `git --no-pager log` and verifies the audit event has `OriginalArgs == {"command":"git status"}` and `Rewritten == true`:

```go
func TestAuditEventRecordsOriginalArgs(t *testing.T) {
    // Simulate user approval of "git status" followed by a pre_tool_use
    // hook rewriting to "git --no-pager log". Verify the audit event
    // preserves the original approved args.
    executed := false
    reg := registry.New()
    if err := reg.Register(registry.Tool{
        Name: "shell.run", Risk: registry.RiskCommand,
        Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
            executed = true
            return registry.ToolResult{Summary: "ran"}, nil
        },
    }); err != nil {
        t.Fatal(err)
    }
    // Hook rewrites "git status" to "git --no-pager log"
    hook := fakeHookRunner{preOut: hooks.Output{
        Rewrite: json.RawMessage(`{"command":"git --no-pager log"}`),
    }}
    p := &scriptedProvider{responses: []string{
        `{"rationale":"r","action":{"type":"tool_call","tool":"shell.run","args":{"command":"git status"}}}`,
        `{"rationale":"r","action":{"type":"answer","content":"done"}}`,
    }}
    cfg := config.Default()
    pol := policy.NewEngine(&cfg, nil)
    state := newTestState(t)
    // Enable persistence so the DB round-trip is also exercised.
    runner := NewRunner(p, reg, pol, state, "test-model")
    runner.HookRunner = hook
    runner.SetForceClass(string(ClassQuestion))

    if _, err := runner.RunTask(context.Background(), "check status"); err != nil {
        t.Fatalf("RunTask() error = %v", err)
    }
    if !executed {
        t.Fatal("tool was not executed")
    }

    log := state.AuditLog()
    if len(log) == 0 {
        t.Fatal("audit log is empty")
    }
    // Find the execution event (should be the last one with no error)
    var execEvent *registry.AuditEvent
    for i := len(log) - 1; i >= 0; i-- {
        if log[i].Error == "" && log[i].ToolName == "shell.run" {
            execEvent = &log[i]
            break
        }
    }
    if execEvent == nil {
        t.Fatal("no successful shell.run audit event found")
    }
    if !execEvent.Rewritten {
        t.Fatal("expected Rewritten=true on the audit event")
    }
    if execEvent.OriginalArgs == nil {
        t.Fatal("expected OriginalArgs to be set")
    }
    // The original args should be the user-approved version before the rewrite.
    // After the rewrite loop, the hook rewrote "git status" to "git --no-pager log".
    // But the TestPreToolUseRewriteReentersPolicy pattern shows that after a
    // rewrite, the loop continues and the user is re-prompted. The audit event
    // captures the args that were ultimately executed.
    if !strings.Contains(string(execEvent.OriginalArgs), "git status") {
        t.Fatalf("OriginalArgs = %s, want to contain 'git status'", string(execEvent.OriginalArgs))
    }
}
```

- [ ] **Step 8: Implement the capture in the rewrite loop**

In `internal/agent/runner.go`, around the rewrite loop (lines 1277-1331), add tracking variables and populate them:

1. Before the loop, declare:
```go
var originalApprovedArgs json.RawMessage
```

2. Before the `runPreToolUseHook` call (line 1306-1307), save the pre-hook args:
```go
// Capture the user-approved args so the audit event can record what
// the user approved vs what was actually executed after rewrite.
preHookArgs := args
rewrittenArgs, hookOut, hookErr := r.runPreToolUseHook(ctx, toolName, args)
```

3. In the rewrite branch (line 1326-1328), before `continue`:
```go
if len(hookOut.Rewrite) > 0 {
    originalApprovedArgs = preHookArgs
    args = rewrittenArgs
    continue
}
```

4. At each audit event creation site that executes the tool (lines 1369 and 1386), add:
```go
event.OriginalArgs = originalApprovedArgs
// We need to determine if a rewrite happened. Since originalApprovedArgs is
// non-nil only when a rewrite occurred, we can derive it.
if originalApprovedArgs != nil {
    event.Rewritten = true
}
```

Wait, actually `originalApprovedArgs` might not be nil if the pre-hook args were set but no rewrite happened. Let me think again...

On line 1307, `preHookArgs := args` is set before every hook call, not just when a rewrite happens. So `originalApprovedArgs` would be set on every pass. But no — I save it to `originalApprovedArgs` only in the rewrite branch (step 3 above). The local `preHookArgs` variable goes out of scope each iteration. So `originalApprovedArgs` is only set across iterations when a rewrite actually happens. This is correct.

But for the determination of `Rewritten`, I can't just check `originalApprovedArgs != nil` because the first pass through the loop doesn't set it. Let me add a separate boolean:

```go
var toolWasRewritten bool
```

Set it in the rewrite branch:
```go
if len(hookOut.Rewrite) > 0 {
    originalApprovedArgs = preHookArgs
    toolWasRewritten = true
    args = rewrittenArgs
    continue
}
```

Then at audit event creation:
```go
event.OriginalArgs = originalApprovedArgs
event.Rewritten = toolWasRewritten
```

This is cleaner.

For the hook error (line 1310) and block (line 1317) sites: these happen before any rewrite, so `originalApprovedArgs` would still be nil and `toolWasRewritten` false. The event would have zero values, which is correct.

- [ ] **Step 9: Build and run tests**

Run: `go build ./... && go test ./internal/agent -run 'TestAuditEventRecordsOriginalArgs' -v`
Expected: PASS.

Run: `go test ./internal/agent -count=1`
Expected: all tests PASS.

- [ ] **Step 10: Run DB tests to confirm round-trip**

Run: `go test ./internal/db -count=1`
Expected: all tests PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/tools/registry/audit.go internal/tools/registry/audit_test.go \
      internal/agent/runner.go internal/agent/runner_test.go \
      internal/db/audits.go internal/db/db.go internal/db/migrations.go
git commit -m "fix(agent): preserve original approved args in audit event on hook rewrite (F-SEC-82)"
```

---

## Self-Review

1. **Spec coverage:**
   - F-BUG-41 / F-POL-88 → Task 1 (warning log on shell normalizeargs failure)
   - F-SEC-82 → Task 2 (OriginalArgs + Rewritten in audit event, DB persistence)

2. **Backward compatibility:**
   - `AuditEvent.OriginalArgs` is `json.RawMessage` — nil when not set.
   - `AuditEvent.Rewritten` is `bool` — false when not set.
   - DB columns are `ALTER TABLE ADD COLUMN` (nullable/zero-default).
   - `GetToolCalls` handles `NULL` values for the new columns via `sql.NullString`/`sql.NullInt64`.

3. **No public API changes:** `Runner`, `PolicyEngine`, `Tool`, `ToolCall` signatures unchanged. Only additive fields on `AuditEvent`.

4. **All tests pass:** `go test ./...` must pass before final commit.
