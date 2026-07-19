# Domain Low — Final polish: F-SEC-34, F-SEC-37, F-SEC-38, F-BUG-51, F-POL-130 (Batch 29)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the 5 remaining genuinely-open items in `docs/14-codebase-improvement-audit-2026-07-14.md` (F-SEC-34, F-SEC-37, F-SEC-38, F-BUG-51, F-POL-130) plus 2 doc-gap HIGHs whose code is already correct (F-SEC-03, F-SEC-08). After this plan lands, the audit's "Per-Finding Status" table is fully reconciled with the code on main.

**Architecture:** Four small code changes (one per finding), one doc-reconciliation pass for the doc-gap HIGHs, and a single Batch 29 entry in the audit table. Each code change is in a single package, follows existing patterns, and is verified by a focused test. No new types, no new dependencies.

**Tech Stack:** Go 1.22+; stdlib only.

## Global Constraints

- Go 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the
  tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the
  implementation step of each task.
- Every test change MUST pass: run `go test ./internal/<pkg> -run <TestName>`
  for the new test, then `go test ./internal/<pkg> -count=1` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public function signatures unless the task explicitly
  says to change them.
- All new tests must assert real behavior, not mock behavior, and must
  be race-clean under `go test -race`.
- A pre-existing uncommitted modification to `internal/app/tui/theme/theme.go`
  and `internal/app/tui/theme/theme_test.go` is present on main from a
  different session. Do not touch it; do not run `git checkout` to remove
  it. The branch's diff will include the plan's changes plus the
  audit-doc update only.

## File Structure

Files modified or created by this plan:

- `internal/acp/protocol.go` — Task 1 (ResourceLink scheme allow-list).
- `internal/acp/protocol_test.go` — Task 1 (new test).
- `internal/acp/server.go` — Task 2 (sanitize wire error messages).
- `internal/acp/server_test.go` — Task 2 (new test).
- `internal/commands/commands.go` — Task 3 (clamp `/export` path).
- `internal/commands/commands_test.go` — Task 3 (new test).
- `internal/acp/turn.go` — Task 4 (use `pending.Respond()`).
- `internal/acp/turn_test.go` — Task 4 (new test).
- `internal/repo/scanner.go` — Task 5 (verify symlink handling — likely no-op).
- `internal/repo/scanner_test.go` — Task 5 (regression test).
- `docs/14-codebase-improvement-audit-2026-07-14.md` — Task 6 (Batch 29 entry).
- No new files.

---

### Task 1: F-SEC-34 — Validate `ResourceLink` URI schemes

**Files:**
- Modify: `internal/acp/protocol.go:99-108, 156-163` (the `normalizePrompt` function and the `ContentBlock.ResourceLink`).
- Add tests: `internal/acp/protocol_test.go`.

**Problem:** `normalizePrompt` accepts any non-empty `URI` in a `resource_link` block. A client can send `javascript:alert(1)`, `data:text/html;base64,…`, or `file:///etc/passwd`.

**Fix:** Add a `validateResourceLinkScheme(uri string) error` helper that accepts only `https:`, `https?` (the regex form), and `file:` with a path that doesn't escape the workspace. Reject everything else with `invalidParamsError`. Call it from the `resource_link` arm of `normalizePrompt`.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/acp/protocol_test.go`, add:

```go
func TestNormalizePromptRejectsBadResourceLinkScheme(t *testing.T) {
    cases := []struct{
        name string
        uri  string
    }{
        {"javascript", "javascript:alert(1)"},
        {"data", "data:text/html;base64,PHNjcmlwdD4="},
        {"file_etc", "file:///etc/passwd"},
        {"empty", ""},
        {"ftp", "ftp://example.com/file"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            blocks := []ContentBlock{{
                Type: "resource_link",
                Name: "name",
                URI:  tc.uri,
            }}
            _, err := normalizePrompt(blocks)
            if err == nil {
                t.Fatalf("expected %q to be rejected, got nil", tc.uri)
            }
        })
    }
}

func TestNormalizePromptAcceptsHTTPSResourceLink(t *testing.T) {
    blocks := []ContentBlock{{
        Type: "resource_link",
        Name: "docs",
        URI:  "https://example.com/page",
    }}
    got, err := normalizePrompt(blocks)
    if err != nil {
        t.Fatalf("https URI rejected: %v", err)
    }
    if !strings.Contains(got, "https://example.com/page") {
        t.Fatalf("expected URI in output, got %q", got)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestNormalizePrompt -v
```

Expected: FAIL (`javascript:`, `data:`, etc. accepted).

- [ ] **Step 3: Add the validator**

In `internal/acp/protocol.go`, add:

```go
// validateResourceLinkScheme returns an error if uri is not one of the
// safe schemes. F-SEC-34. Accepts "https:" and "file:" (with a path
// that does not contain ".."). Rejects everything else.
func validateResourceLinkScheme(uri string) error {
    if uri == "" {
        return invalidParamsError("resource_link URI must not be empty")
    }
    // Find the first ":".
    idx := strings.Index(uri, ":")
    if idx < 0 {
        return invalidParamsError("resource_link URI has no scheme: %q", uri)
    }
    scheme := strings.ToLower(uri[:idx])
    switch scheme {
    case "https":
        return nil
    case "file":
        // Reject path traversal.
        if strings.Contains(uri[idx+1:], "..") {
            return invalidParamsError("resource_link file: URI contains '..': %q", uri)
        }
        return nil
    case "http":
        // http: is allowed only if the entire URI is the same-host
        // non-redirect target. For the prompt context, this is too
        // dangerous — reject.
        return invalidParamsError("resource_link http: URI not allowed; use https: instead")
    default:
        return invalidParamsError("resource_link URI scheme %q not allowed", scheme)
    }
}
```

In the `resource_link` arm of `normalizePrompt` (around line 162), add:

```go
if err := validateResourceLinkScheme(block.URI); err != nil {
    return "", err
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestNormalizePrompt -v
```

Expected: PASS.

- [ ] **Step 5: Run the full acp suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/acp -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/acp/protocol.go internal/acp/protocol_test.go
git commit -m "fix(acp): ResourceLink URI scheme allow-list (F-SEC-34)"
```

---

### Task 2: F-SEC-37 — Sanitize wire-output error messages

**Files:**
- Modify: `internal/acp/server.go:471-484` (the `dispatchRequest` function).
- Add tests: `internal/acp/server_test.go`.

**Problem:** `Error.Message` is set to `err.Error()` for every error, including internal server errors, permission-bridge failures, and decode errors. The string may include filesystem paths, internal package names, or stack-trace hints.

**Fix:** Replace `err.Error()` with a sanitized message for the wire. Server-side: log the full error via `slog.Default().Warn(...)`. Wire-side: produce a fixed, opaque message based on the error code. Add a helper `wireError(err error) string` that maps known JSON-RPC error codes to fixed strings and falls back to "internal error" for unknown errors.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/acp/server_test.go`, add:

```go
func TestDispatchRequestSanitizesWireErrorMessage(t *testing.T) {
    // Drive a handler that returns a *jsonRPCError with a
    // filesystem-path-like message.
    s := &Server{handlers: map[string]Handler{}}
    s.handlers["reveal"] = func(ctx context.Context, params json.RawMessage) (any, error) {
        return nil, &jsonRPCError{
            Code:    internalError,
            Message: "internal: open /home/alice/.config/marshal/secrets.json: permission denied",
        }
    }
    req := Request{
        JSONRPC: "2.0",
        Method:  "reveal",
        ID:      rawJSON("1"),
    }
    s.dispatchRequest(context.Background(), req) // synchronously fills resp

    // The wire Response sent on the output must NOT contain the
    // filesystem path. Read from the output buffer.
    out := s.out // the *json.Encoder set up in NewServer
    _ = out
    // ... (Assert the encoded JSON's "error.message" does not contain
    // "/home/alice" or "permission denied" — it should be a fixed
    // "internal error" string.)
}
```

Read `internal/acp/server_test.go` to match the existing test setup (most tests use the `dispatch` wrapper rather than the `dispatchRequest` private function; adapt as needed).

- [ ] **Step 2: Run the test to verify it fails**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestDispatchRequestSanitizes -v
```

Expected: FAIL (current code emits the raw `err.Error()` on the wire).

- [ ] **Step 3: Add the wire-error helper**

In `internal/acp/server.go` (or a new `errors.go` if the file gets too large), add:

```go
// wireError returns the safe, opaque message emitted to the client.
// Server-side: the full error is logged via slog before this is
// called. Wire-side: a fixed string per JSON-RPC error code. F-SEC-37.
func wireError(err error) string {
    var rpcErr *jsonRPCError
    if errors.As(err, &rpcErr) {
        switch rpcErr.Code {
        case parseError:
            return "parse error"
        case invalidRequest:
            return "invalid request"
        case methodNotFound:
            return "method not found"
        case invalidParams:
            return "invalid params"
        case internalError:
            return "internal error"
        case requestCancelled:
            return "request cancelled"
        default:
            return "server error"
        }
    }
    if errors.Is(err, context.Canceled) {
        return "request cancelled"
    }
    return "internal error"
}
```

In `dispatchRequest` (around line 486), change:

```go
resp.Error = &Error{
    Code:    codeFor(err),
    Message: wireError(err),
}
// Log the full error server-side for operators.
s.log().Warn("acp dispatch error", "method", req.Method, "err", err)
```

(`s.log()` is the nil-safe helper added in G1; the existing `Server.log()` method is the canonical accessor.)

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestDispatchRequestSanitizes -v
```

- [ ] **Step 5: Run the full acp suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/acp -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/acp/server.go internal/acp/server_test.go
git commit -m "fix(acp): sanitize wire error messages (F-SEC-37)"
```

---

### Task 3: F-SEC-38 — Clamp `/export` path to the working dir

**Files:**
- Modify: `internal/commands/commands.go:422-436` (the `export` command).
- Add tests: `internal/commands/commands_test.go`.

**Problem:** `path := strings.Join(args, " ")` lets a user write to any path the marshal process can write to. The redact flag controls secret redaction in the *content* but does not constrain the *location*.

**Fix:** After joining args, call `filepath.Clean(path)`. If the result is absolute, reject with an error message. Otherwise, prepend `state.WorkingDir` and verify the resolved path is still under `state.WorkingDir` (use `filepath.Rel` and check the result does not start with `..`).

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/commands/commands_test.go`, add:

```go
func TestExportRejectsAbsolutePath(t *testing.T) {
    state := newTestState(t)
    cmd, _ := cmdReg.Lookup("export")
    out := cmd.Handler(state, []string{"/etc/passwd"})
    if !strings.Contains(out, "Export failed") && !strings.Contains(out, "must be relative") {
        t.Fatalf("expected /etc/passwd to be rejected, got %q", out)
    }
}

func TestExportRejectsParentTraversal(t *testing.T) {
    state := newTestState(t)
    cmd, _ := cmdReg.Lookup("export")
    out := cmd.Handler(state, []string{"../../etc/passwd"})
    if !strings.Contains(out, "Export failed") && !strings.Contains(out, "escapes") && !strings.Contains(out, "..") {
        t.Fatalf("expected ../.. to be rejected, got %q", out)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
CGO_ENABLED=1 go test ./internal/commands -run TestExportRejects -v
```

Expected: FAIL (current code writes anywhere).

- [ ] **Step 3: Clamp the path**

In `internal/commands/commands.go` (line 422-436), change the export handler:

```go
Name:        "export",
Description: "Export this session to a self-contained HTML file",
Args:        "[relative-path]",
Handler: func(state *session.State, args []string) string {
    rel := strings.TrimSpace(strings.Join(args, " "))
    if rel == "" {
        rel = "marshal-session-" + state.SessionID() + ".html"
    }
    // F-SEC-38: clamp the export to the working dir.
    if filepath.IsAbs(rel) {
        return "Export failed: path must be relative to the working directory"
    }
    cleaned := filepath.Clean(rel)
    if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
        return "Export failed: path escapes the working directory"
    }
    path := filepath.Join(state.WorkingDir, cleaned)
    redactOn := state.Config.Privacy.RedactSecrets
    if err := export.Write(state, path, redactOn); err != nil {
        return "Export failed: " + err.Error()
    }
    return "Exported to " + path
},
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/commands -run TestExportRejects -v
```

- [ ] **Step 5: Run the full commands suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/commands -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/commands/commands.go internal/commands/commands_test.go
git commit -m "fix(commands): clamp /export path to working dir (F-SEC-38)"
```

---

### Task 4: F-BUG-51 — Use `pending.Respond()` in turn forwarder

**Files:**
- Modify: `internal/acp/turn.go:272-285` (the `EventPendingQuestionChanged` handler).
- Add tests: `internal/acp/turn_test.go`.

**Problem:** The forwarder writes `pending.ResponseChan <- answers` in a `select` with `<-turnCtx.Done()`. If the runner has abandoned this pending question and later reads from `ResponseChan`, the select may have already fired `<-turnCtx.Done()` and the answers are lost.

**Fix:** Replace the direct `select { case pending.ResponseChan <- answers: ... }` with `pending.Respond(answers)`. The `Respond` method (added in G3) uses `sync.Once` to safely send + close the channel exactly once. The `turnAnswered` sync.Map guard becomes unnecessary (the `sync.Once` inside `Respond` does the same job) — but keep the sync.Map for now as belt-and-suspenders; it doesn't hurt.

**Implementation steps:**

- [ ] **Step 1: Read the current code**

In `internal/acp/turn.go` around line 272, the existing block is:

```go
if ev.Type == session.EventPendingQuestionChanged &&
    ev.Payload.PendingQuestion != nil {
    pending := ev.Payload.PendingQuestion
    answers := make([]session.Answer, len(pending.Questions))
    for i, q := range pending.Questions {
        answers[i] = session.Answer{Question: q.Question, Answer: session.AnswerUnanswered}
    }
    if _, loaded := turnAnswered.LoadOrStore(pending.ResponseChan, true); !loaded {
        select {
        case pending.ResponseChan <- answers:
        case <-turnCtx.Done():
        }
    }
}
```

- [ ] **Step 2: Write the failing test**

In `internal/acp/turn_test.go`, add:

```go
func TestForwarderUsesRespondForQuestion(t *testing.T) {
    // Set up a forwarder with a pending question whose ResponseChan is
    // unbuffered and no runner is reading. Drive the forwarder with
    // the EventPendingQuestionChanged event. Assert that the channel
    // received the answer (via the close-side of Respond) and that
    // calling Respond twice is safe (no double-send panic).
    // ...
}
```

Read `internal/acp/turn_test.go` to match the existing test infrastructure. The exact test depends on the helpers in that file.

- [ ] **Step 3: Replace the direct send with `pending.Respond`**

In `internal/acp/turn.go` (line 272-285), change:

```go
if ev.Type == session.EventPendingQuestionChanged &&
    ev.Payload.PendingQuestion != nil {
    pending := ev.Payload.PendingQuestion
    answers := make([]session.Answer, len(pending.Questions))
    for i, q := range pending.Questions {
        answers[i] = session.Answer{Question: q.Question, Answer: session.AnswerUnanswered}
    }
    // F-BUG-51: use pending.Respond (sync.Once + close) so a stale
    // select that already fired <-turnCtx.Done() cannot lose the
    // answers. The turnAnswered sync.Map is belt-and-suspenders.
    if _, loaded := turnAnswered.LoadOrStore(pending.ResponseChan, true); !loaded {
        pending.Respond(answers)
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
CGO_ENABLED=1 go test ./internal/acp -run TestForwarder -v
```

- [ ] **Step 5: Run the full acp suite under race**

```bash
CGO_ENABLED=1 go test -race ./internal/acp -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/acp/turn.go internal/acp/turn_test.go
git commit -m "fix(acp): forwarder uses pending.Respond for question answers (F-BUG-51)"
```

---

### Task 5: F-POL-130 — Add regression test for `Scanner` symlink handling

**Files:**
- Modify: `internal/repo/scanner_test.go` (add a new test).
- Possibly modify: `internal/repo/scanner.go` (verify symlink handling already in place; likely no-op).

**Problem:** The audit says `Scanner` does not distinguish symlink from non-regular. The current code at `scanner.go:104-112` ALREADY does the right thing: symlinks are skipped with a clear "symlink" reason. This is a regression-test task, not a code-fix task.

**Fix:** Add a regression test that creates a symlink in a temp dir, runs the scanner, and asserts the symlink is in the `skipped` list with reason "symlink" and is NOT in the file list.

**Implementation steps:**

- [ ] **Step 1: Write the failing test**

In `internal/repo/scanner_test.go`, add:

```go
func TestScannerSkipsSymlinks(t *testing.T) {
    tmp := t.TempDir()
    // Create a real file
    if err := os.WriteFile(filepath.Join(tmp, "real.txt"), []byte("hello"), 0644); err != nil {
        t.Fatalf("write real: %v", err)
    }
    // Create a symlink to the real file
    if err := os.Symlink("real.txt", filepath.Join(tmp, "link.txt")); err != nil {
        t.Fatalf("symlink: %v", err)
    }
    // Run the scanner
    s := NewScanner(tmp, 0, nil)
    var files []string
    var skipped []SkippedEntry // or whatever the type is
    err := s.Scan(context.Background(), func(f File) error { files = append(files, f.Path); return nil }, func(skippedEntry) {})
    if err != nil {
        t.Fatalf("scan: %v", err)
    }
    // Assert the real file is in files
    foundReal := false
    for _, f := range files {
        if f == "real.txt" { foundReal = true }
    }
    if !foundReal {
        t.Fatalf("expected real.txt in files, got %v", files)
    }
    // Assert the symlink is NOT in files
    for _, f := range files {
        if f == "link.txt" {
            t.Fatalf("symlink should be skipped, but link.txt is in files: %v", files)
        }
    }
    // Assert the symlink is in skipped
    foundSkipped := false
    for _, sk := range skipped {
        if sk.Path == "link.txt" {
            foundSkipped = true
            if sk.Reason != "symlink" {
                t.Fatalf("expected reason=symlink, got %q", sk.Reason)
            }
        }
    }
    if !foundSkipped {
        t.Fatalf("expected link.txt in skipped, got %v", skipped)
    }
}
```

Read `internal/repo/scanner.go` and `scanner_test.go` to match the existing constructor and type names.

- [ ] **Step 2: Run the test to verify it passes (or fails)**

```bash
CGO_ENABLED=1 go test ./internal/repo -run TestScannerSkipsSymlinks -v
```

Expected: PASS (the symlink-skip is already in place; the test just documents the behavior).

- [ ] **Step 3: If the test reveals a gap, fix `scanner.go`**

If the scanner doesn't actually skip the symlink (the audit is correct and the code regressed), fix the skip block at `scanner.go:104-112` to use a cleaner check:

```go
if entry.Type()&os.ModeSymlink != 0 {
    s.skipped = append(s.skipped, skippedEntry{Path: rel, Reason: "symlink"})
    return nil
}
```

(If the existing code is already correct, no fix is needed; just confirm via the test.)

- [ ] **Step 4: Commit**

```bash
git add internal/repo/scanner.go internal/repo/scanner_test.go
git commit -m "test(repo): regression test for scanner symlink skip (F-POL-130)"
```

(Use `test(...)` not `fix(...)` if the code is already correct.)

---

### Task 6: Doc reconciliation + Batch 29 audit table entry

**Files:**
- Modify: `docs/14-codebase-improvement-audit-2026-07-14.md` (append Batch 29; mark F-03 and F-08 RESOLVED in the table).

**Why:** Task 1-5 close 5 code items. This task records the work in the audit table and reconciles the 2 stale-doc-gap HIGHs (F-03 and F-08) whose code is already correct.

**Implementation steps:**

- [ ] **Step 1: Verify the code state on main**

In the worktree (post-rebase), read:
- `internal/tools/native/file.go` and confirm the `SafeResolve` path resolver (added by the A3 batch) is in place. Find the F-SEC-03 audit heading and confirm the file:line reference is now stale; the fix is in the `resolveWorkspacePath` helper, not line 12.
- `internal/app/onboarding.go` and confirm the onboarding key entry has `EchoPassword` for both `keyModeInline` and `keyModeEnvName` (added in earlier batches; verified in A5 Task 3).

- [ ] **Step 2: Find the right insertion point**

Open the audit doc and find Batch 28 (added in the B-reconciliation batch). The new entry goes after Batch 28.

- [ ] **Step 3: Add the Batch 29 entry**

```markdown
### Batch 29 (Low — final polish + doc reconciliation): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-34 | RESOLVED | `normalizePrompt` now validates `resource_link` URIs against a scheme allow-list (`https:`, `file:` with no `..` traversal). `javascript:`, `data:`, `ftp:`, `http:` are rejected with `invalidParams`. 2 new tests. |
| F-SEC-37 | RESOLVED | `dispatchRequest` now uses a `wireError(err)` helper that maps each JSON-RPC error code to a fixed opaque string. The full error is logged server-side via `slog`. 1 new test. |
| F-SEC-38 | RESOLVED | `/export` clamps the path to the working dir; absolute paths and `..` traversal are rejected. 2 new tests. |
| F-BUG-51 | RESOLVED | Turn forwarder uses `pending.Respond(answers)` (sync.Once + close, added in G3) instead of a direct `pending.ResponseChan <- answers` select that could fire `<-turnCtx.Done()` and lose the answers. 1 new test. |
| F-POL-130 | RESOLVED | `Scanner` already skips symlinks (added by an earlier batch, `scanner.go:104-112`). Regression test added to lock the behavior in. 1 new test. |
| F-SEC-03 | RESOLVED | Doc-gap: code is correct. `SafeResolve` path resolver (added in the A3 batch) is in place. Marked RESOLVED to reconcile the audit table. |
| F-SEC-08 | RESOLVED | Doc-gap: code is correct. Onboarding key entry uses `EchoPassword` for both modes (verified in A5 Task 3). Marked RESOLVED to reconcile the audit table. |
```

- [ ] **Step 4: Update the per-finding section headings (if needed)**

If F-03 and F-08 have per-finding sections in the audit doc, prepend a "Status: RESOLVED (doc reconciliation — Batch 29)" line at the top of each. The other 5 findings (F-34, F-37, F-38, F-51, F-130) may or may not have per-finding sections; if they do, update them similarly.

The cleanest path: add a per-finding table row for each of the 7 findings, in a single `### Status` section at the top of the audit doc (matching the resolution-table format used by earlier batches). The 7 entries can go in a small table.

- [ ] **Step 5: Commit**

```bash
git add docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs(audit): mark final findings RESOLVED (Batch 29)"
```

---

## Self-Review

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./... -count=1
```

Verify:
- All 5 new code fixes are in place (Tasks 1-5).
- The Batch 29 audit entry matches Batch 28's format exactly.
- No production code outside the planned files was touched.
- The 6 commits have the exact messages from the plan.

If anything is off, fix it in a follow-up commit; don't amend prior commits.

## Notes for the implementer

- The pre-existing uncommitted modification to `internal/app/tui/theme/theme.go` is from a different session. Do not commit or amend it; the branch's diff will include only the plan's changes plus the audit-doc update.
- The plan's 5 code items are well-scoped and the test scaffolding mirrors existing patterns. Read the existing tests in each file before writing new ones.
- Task 5 is intentionally a regression-test task; the production code is already correct. If the test fails, fix the production code; if it passes, commit the test only.
- Task 6 is the largest; read the audit doc's existing Batch 28 entry (or the closest one) to match the exact format.
