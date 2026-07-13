# ACP Session Delete (Batch A2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement ACP `session/delete` to remove a session row (and its messages) from a per-cwd Marshal database. The handler requires both a `cwd` and a `sessionId`, matching the existing `session/load` and `session/resume` shape. `initialize` advertises `sessionCapabilities.delete: {}`.

**Architecture decision: per-cwd, like the rest of the ACP API.** The audit doc said `session/delete` "requires a global session-id → project-root index" — that was a forward-looking note, not a design constraint. The existing `session/list` (Batch A1) is per-cwd. For consistency, `session/delete` is per-cwd too: the caller already knows the cwd (they used it on `session/new` or `session/list`), and the handler opens that cwd's DB, deletes the session row, and returns an empty object. There is no global session registry, and none is needed for this handler. The audit doc and `docs/10-acp.md` are updated to reflect the per-cwd design.

**Cascade behavior:** deleting a session must remove its messages (FK CASCADE on `messages.session_id` already exists in the schema per the prior audit's findings). The plan also adds a corresponding in-memory cleanup: if a runtime is currently loaded for the deleted session id, it is cancelled and closed before the DB delete.

**Tech Stack:** Go 1.26.1, modernc.org/sqlite, JSON-RPC 2.0 over stdio, ACP v1. Reuses the existing `db.DeleteSession` capability (verify it exists; if not, add it as part of Task 1) and the existing `SessionManager.close` + `cancel` machinery.

**Assumes Milestone R + the prior list/resume batch are complete** (they are).

## Global Constraints

- Truthful capabilities: after this batch, `initialize` advertises `sessionCapabilities: { close, list, resume, additionalDirectories, delete }` (each an empty object). Nothing else changes.
- Per-cwd DBs / lock ordering / error codes / context cancellation: same as Milestone R. `invalidParamsError` (`-32602`) for param errors, `serverErrorf` (`-32000`) for internal errors, `-32800` for context cancellation. `loadOrFail`-style `-32000` for unknown session ids.
- Idempotency: deleting an unknown session id is `-32000` (the audit doc's "session close" precedent — unknown session returns server error, not silent success). The test surface explicitly asserts this.
- Cascade: the session's `messages` rows are removed by the existing FK CASCADE; no application-level loop.
- The handler MUST cancel and close any currently-loaded runtime for the session id before deleting the row. This prevents the in-memory map from holding a pointer to a session whose row no longer exists.
- The handler MUST be safe to call when no runtime is loaded for the id (the common case: deleting a row that was discovered via `session/list` but never resumed). It MUST be safe to call when the runtime is loaded in another SessionManager (out of scope for this batch; documented as a known limitation).
- Secrets: session deletion does not touch any secret fields. No new masking needed.
- No comments unless asked. Match existing gofmt style.
- Verification: `go test -count=1 ./internal/acp/... ./internal/db/...` after every task; full gates before batch closeout.

## File Structure

**Modify:**
- `internal/db/sessions.go` — verify/add `DeleteSession(ctx, sessionID string) (existed bool, err error)`. If the helper doesn't exist, add it with the cascade taking care of messages.
- `internal/db/sessions_test.go` — add `TestDeleteSession` (cascade, idempotency: second delete returns `existed=false, err=nil`).
- `internal/acp/session.go` — add `Delete(ctx, params) (any, error)` handler; wire it into the manager.
- `internal/acp/session_test.go` — add `delete` validation cases (missing cwd, missing sessionId, relative cwd) and happy-path tests (loaded runtime, not-loaded runtime, double-delete).
- `internal/acp/run.go` — register `srv.Handle("session/delete", manager.Delete)`.
- `internal/acp/run_test.go` — drop `delete` from the `forbidden` slice and add a positive capability assertion.
- `internal/acp/lister.go` — add a small `DeleteSession(ctx, cwd, sessionID) (existed bool, err error)` method on `perCwdLister` (mirrors `ListSessions`).
- `internal/acp/lister_test.go` — test the lister delete path.
- `docs/10-acp.md` — add a `session/delete` row in the supported-methods table; remove `delete` from the unsupported-features table; update the advertised-capabilities note.
- `docs/13-project-audit-2026-07-11.md` — add an "Implementation batch — ACP session delete (Batch A2)" section (mirror prior batch sections).

---

## Task 1: DB delete helper

**Files:**
- Modify: `internal/db/sessions.go`
- Test: `internal/db/sessions_test.go`

**Interfaces:**
- Produces: `func (db *DB) DeleteSession(ctx context.Context, sessionID string) (existed bool, err error)`. Cascade-deletes the session row; messages cascade via the existing FK.

- [ ] **Step 1: Read the existing schema**

Open `internal/db/sessions.go` and confirm:
- The `agent_sessions` table has a `PRIMARY KEY (id)` and the `messages` table has `session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE` (or equivalent).
- If `DeleteSession` already exists, skip to Step 5 (no-op verification). If not, add it.

- [ ] **Step 2: Write the failing test**

Add to `internal/db/sessions_test.go`:

```go
func TestDeleteSessionCascadesMessages(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject("/home/user/proj", "p")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_to_delete", pid, "x", t0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.SaveMessage("sess_to_delete", "user", "hi", "plain", t0.Add(time.Hour), "", 0, false, 0); err != nil {
		t.Fatalf("save: %v", err)
	}

	existed, err := d.DeleteSession(context.Background(), "sess_to_delete")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !existed {
		t.Fatal("first delete should report existed=true")
	}

	// Cascade: messages should be gone too. Confirm via a count query.
	var msgCount int
	if err := d.sqlDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM messages WHERE session_id = ?", "sess_to_delete").Scan(&msgCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if msgCount != 0 {
		t.Fatalf("messages not cascaded: count = %d", msgCount)
	}
}

func TestDeleteSessionUnknownIsIdempotent(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	existed, err := d.DeleteSession(context.Background(), "no_such_session")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if existed {
		t.Fatal("delete of unknown id should report existed=false")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -count=1 ./internal/db/ -run 'TestDeleteSession' -v`
Expected: FAIL — `undefined: db.DeleteSession` (or compile error).

- [ ] **Step 4: Implement `DeleteSession`**

In `internal/db/sessions.go`, add:

```go
// DeleteSession removes a session row by id. Messages cascade via the
// messages.session_id FK. Returns existed=false if no row matched.
func (db *DB) DeleteSession(ctx context.Context, sessionID string) (bool, error) {
	res, err := db.sqlDB.ExecContext(ctx, "DELETE FROM agent_sessions WHERE id = ?", sessionID)
	if err != nil {
		return false, fmt.Errorf("delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete session rows affected: %w", err)
	}
	return n > 0, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -count=1 ./internal/db/ -run 'TestDeleteSession' -v`
Expected: PASS.

- [ ] **Step 6: Vet and format**

Run: `go vet ./internal/db/ && gofmt -w internal/db/sessions.go internal/db/sessions_test.go`
Expected: no issues.

- [ ] **Step 7: Commit**

```bash
git add internal/db/sessions.go internal/db/sessions_test.go
git commit -m "feat(db): add DeleteSession with cascade via messages FK"
```

---

## Task 2: Lister delete path

**Files:**
- Modify: `internal/acp/lister.go`
- Test: `internal/acp/lister_test.go`

**Interfaces:**
- Produces: `perCwdLister.DeleteSession(ctx, cwd, sessionID) (existed bool, err error)`. Mirrors the existing `ListSessions` open/migrate/list/close pattern.

- [ ] **Step 1: Write the failing test**

Add to `internal/acp/lister_test.go`:

```go
func TestPerCwdListerDeleteSession(t *testing.T) {
	root := t.TempDir()
	absCwd, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(absCwd, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d, err := db.Open(filepath.Join(absCwd, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		t.Fatalf("migrate: %v", err)
	}
	pid, _ := d.GetOrCreateProject(absCwd, "p")
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_lister_delete", pid, "x", t0); err != nil {
		_ = d.Close()
		t.Fatalf("create: %v", err)
	}
	_ = d.Close()

	l := newPerCwdLister()
	existed, err := l.DeleteSession(context.Background(), absCwd, "sess_lister_delete")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if !existed {
		t.Fatal("first delete should report existed=true")
	}

	// Idempotency: second delete returns existed=false.
	existed, err = l.DeleteSession(context.Background(), absCwd, "sess_lister_delete")
	if err != nil {
		t.Fatalf("second DeleteSession: %v", err)
	}
	if existed {
		t.Fatal("second delete should report existed=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/acp/ -run TestPerCwdListerDeleteSession -v`
Expected: FAIL — `l.DeleteSession undefined`.

- [ ] **Step 3: Implement `DeleteSession` on `perCwdLister`**

In `internal/acp/lister.go`, add (mirroring the existing `ListSessions` open/migrate/close pattern):

```go
func (l *perCwdLister) DeleteSession(ctx context.Context, cwd, sessionID string) (bool, error) {
	d, err := db.Open(filepath.Join(cwd, ".marshal", "marshal.db"))
	if err != nil {
		return false, err
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		return false, err
	}
	return d.DeleteSession(ctx, sessionID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/acp/ -run TestPerCwdListerDeleteSession -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/acp/ && gofmt -w internal/acp/lister.go internal/acp/lister_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/lister.go internal/acp/lister_test.go
git commit -m "feat(acp): add DeleteSession to perCwdLister"
```

---

## Task 3: ACP handler

**Files:**
- Modify: `internal/acp/session.go`
- Test: `internal/acp/session_test.go`

**Interfaces:**
- Produces: `func (m *SessionManager) Delete(ctx context.Context, params json.RawMessage) (any, error)`. Cancels and closes any loaded runtime for the session id, then deletes the row from the per-cwd DB. Returns `map[string]any{}` on success, `-32602` on bad params, `-32000` on internal errors and unknown session id.

- [ ] **Step 1: Write the failing tests**

In `internal/acp/session_test.go`, find `TestSessionLifecycleValidation` and add:

```go
{"delete missing cwd", "delete", `{"sessionId":"sess_x","mcpServers":[]}`, invalidParams},
{"delete relative cwd", "delete", `{"cwd":"relative/path","sessionId":"sess_x","mcpServers":[]}`, invalidParams},
{"delete missing sessionId", "delete", `{"cwd":"` + absCwd + `","mcpServers":[]}`, invalidParams},
{"delete non-empty mcpServers", "delete", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[{"name":"x"}]}`, invalidParams},
{"delete non-empty additional directories", "delete", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["/tmp"]}`, invalidParams},
```

Add a `"delete"` branch to the dispatch switch:

```go
case "delete":
    res, err = m.Delete(context.Background(), json.RawMessage(tc.params))
```

(Remove the `delete` case from the previous plan's `TestSessionLifecycleValidation` if it was guarded by `additionalDirectories` — they were paired; the rejection lifted in the additional-directories batch.)

Add a happy-path test that exercises both "runtime loaded" and "runtime not loaded" branches:

```go
func TestSessionDeleteWithLoadedRuntime(t *testing.T) {
	var idSeq atomic.Int64
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&idSeq, nil),
		CloseRuntime: noopClose(),
		Lister:       &fakeLister{deleteExisted: true},
	})
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)

	// Load the session first so a runtime is registered.
	if _, err := m.Load(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_delete_loaded","mcpServers":[]}`)); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Now delete it. The handler must cancel+close the loaded runtime AND delete the row.
	if _, err := m.Delete(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_delete_loaded","mcpServers":[]}`)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestSessionDeleteWithoutLoadedRuntime(t *testing.T) {
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister:       &fakeLister{deleteExisted: true},
	})
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)

	// Delete without ever loading — the row exists in the DB but no runtime is registered.
	if _, err := m.Delete(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_never_loaded","mcpServers":[]}`)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestSessionDeleteUnknownIdReturnsServerError(t *testing.T) {
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister:       &fakeLister{deleteExisted: false},
	})
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)

	_, err := m.Delete(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"no_such_session","mcpServers":[]}`))
	if err == nil {
		t.Fatal("expected server error for unknown session id")
	}
}
```

Add `deleteExisted bool` to the existing `fakeLister` helper:

```go
type fakeLister struct {
	entries      []db.SessionEntry
	nextCursor   string
	err          error
	deleteExisted bool
}

func (f *fakeLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.entries, f.nextCursor, nil
}

func (f *fakeLister) DeleteSession(ctx context.Context, cwd, sessionID string) (bool, error) {
	return f.deleteExisted, f.err
}
```

(If `fakeLister` already has a `DeleteSession` method from prior work, extend it; otherwise add both methods.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/acp/ -run 'TestSessionLifecycleValidation|TestSessionDelete' -v`
Expected: FAIL — `m.Delete undefined`, `fakeLister.DeleteSession` undefined.

- [ ] **Step 3: Implement `Delete` and the `DeleteSession` method on `SessionLister`**

In `internal/acp/session.go`:

- Extend the `SessionLister` interface:

```go
type SessionLister interface {
    ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error)
    DeleteSession(ctx context.Context, cwd, sessionID string) (existed bool, err error)
}
```

- Add a nil-lister guard helper. If `m.lister == nil`, return a clear server error.

- Add the handler:

```go
// Delete handles session/delete. It validates params, cancels and closes
// any loaded runtime for the session id, deletes the row from the per-cwd
// database, and returns an empty object. Missing runtime is not an error
// (the common case: deleting a row discovered via session/list without
// ever resuming). Missing session id is a server error.
func (m *SessionManager) Delete(ctx context.Context, params json.RawMessage) (any, error) {
    if m.lister == nil {
        return nil, serverErrorf("acp: SessionManager has no SessionLister configured")
    }
    cancel, err := m.requireReady()
    if err != nil {
        return nil, err
    }
    var p sessionParams
    if len(params) > 0 {
        if err := json.Unmarshal(params, &p); err != nil {
            return nil, fmt.Errorf("acp: parse session/delete params: %w", err)
        }
    }
    if err := validateLifecycleParams(&p, true); err != nil {
        return nil, err
    }

    if _, replaceErr := m.replaceExisting(ctx, p.SessionID, cancel); replaceErr != nil {
        return nil, replaceErr
    }

    existed, err := m.lister.DeleteSession(ctx, p.Cwd, p.SessionID)
    if err != nil {
        return nil, serverErrorf("acp: delete session: %v", err)
    }
    if !existed {
        return nil, serverErrorf("acp: no such session %q in %q", p.SessionID, p.Cwd)
    }
    return map[string]any{}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/acp/ -run 'TestSessionLifecycleValidation|TestSessionDelete' -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/acp/ && gofmt -w internal/acp/session.go internal/acp/session_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/session.go internal/acp/session_test.go
git commit -m "feat(acp): implement session/delete with runtime teardown + row delete"
```

---

## Task 4: Register handler and advertise capability

**Files:**
- Modify: `internal/acp/run.go`
- Modify: `internal/acp/run_test.go`

- [ ] **Step 1: Register the handler**

In `internal/acp/run.go`, after the existing `srv.Handle("session/resume", manager.Resume)` line, add:

```go
srv.Handle("session/delete", manager.Delete)
```

- [ ] **Step 2: Update the capability map**

Extend `sessionCapabilities` in the `initialize` handler:

```go
"sessionCapabilities": map[string]any{
    "close":                 map[string]any{},
    "list":                  map[string]any{},
    "resume":                map[string]any{},
    "additionalDirectories": map[string]any{},
    "delete":                map[string]any{},
},
```

- [ ] **Step 3: Update capability assertions**

In `internal/acp/run_test.go` `TestRunInitializeCapabilities`:
- Remove `"delete"` from the `forbidden` slice. The new slice is: `{"image", "audio", "embeddedContext", "mcp"}`.
- Add a positive assertion mirroring the list/resume ones:

```go
// sessionCapabilities.delete is an empty object.
deleteCap, ok := sessionCaps["delete"]
if !ok {
    t.Fatalf("sessionCapabilities.delete missing")
}
deleteObj, ok := deleteCap.(map[string]any)
if !ok {
    t.Fatalf("sessionCapabilities.delete is not an object: %T", deleteCap)
}
if len(deleteObj) != 0 {
    t.Fatalf("sessionCapabilities.delete = %v, want empty object", deleteObj)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/acp/ -run TestRunInitializeCapabilities -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/acp/ && gofmt -w internal/acp/run.go internal/acp/run_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/run.go internal/acp/run_test.go
git commit -m "feat(acp): register session/delete and advertise sessionCapabilities.delete"
```

---

## Task 5: Docs + audit log

**Files:**
- Modify: `docs/10-acp.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Add `session/delete` to the supported-methods table**

In `docs/10-acp.md`, in the **Supported methods** table, after the `session/list` and `session/resume` rows, add:

```markdown
| `session/delete` | Full | Requires an absolute `cwd` and a `sessionId`. Cancels and closes any loaded runtime for the id, then removes the session row (and its messages via FK cascade) from `<cwd>/.marshal/marshal.db`. Returns an empty object on success, `-32000` for an unknown session id. Per-cwd, like the rest of the lifecycle API. |
```

- [ ] **Step 2: Remove `delete` from the unsupported-features table**

The prior batch already moved the "still unsupported" rows; the only remaining mention of `delete` in the unsupported-features table needs to go. Confirm by reading the table; remove the row.

- [ ] **Step 3: Update the advertised-capabilities section**

In the `### Advertised capabilities` subsection, extend the line:

```markdown
`initialize` reports `agentCapabilities.loadSession: true` and
`sessionCapabilities: { close, list, resume, additionalDirectories,
delete }` (each an empty object). No other lifecycle, content, or MCP
capabilities are advertised.
```

- [ ] **Step 4: Add the audit-doc batch section**

In `docs/13-project-audit-2026-07-11.md`, append a new section mirroring the prior batch sections:

```markdown
## Implementation batch — ACP session delete (Batch A2)

The remaining ACP lifecycle gap (session/delete) was closed by the
following commits on branch `feature/acp-session-delete`:

```
<commit> feat(db): add DeleteSession with cascade via messages FK
<commit> feat(acp): add DeleteSession to perCwdLister
<commit> feat(acp): implement session/delete with runtime teardown + row delete
<commit> feat(acp): register session/delete and advertise sessionCapabilities.delete
```

### Newly supported method

- **`session/delete`** — accepts `cwd` (absolute) and `sessionId`; cancels and closes any loaded runtime for the id, then removes the row (and its messages via the existing FK CASCADE) from `<cwd>/.marshal/marshal.db`. Returns an empty object on success, `-32000` for an unknown session id.

### Design note

The earlier audit note suggested `session/delete` would require a global
session-id → project-root index. This batch instead takes the per-cwd
approach: the caller already knows the `cwd` from `session/list` or the
original `session/new`, and the handler opens that cwd's DB. The
audit-doc note is updated accordingly. A global session registry
remains a future feature; it would enable `session/delete` without a
`cwd` parameter, but is out of scope here.

### Unadvertised capabilities remain unadvertised

`initialize` continues to omit `mcp`/`mcpCapabilities`, image, audio,
and embedded-context content blocks. The advertised lifecycle set is
now `sessionCapabilities: { close, list, resume, additionalDirectories,
delete }`, each as an empty object.
```

Use the real commit hashes after the batch lands.

- [ ] **Step 5: Commit**

```bash
git add docs/10-acp.md docs/13-project-audit-2026-07-11.md
git commit -m "docs(acp): document session/delete support (Batch A2)"
```

---

## Batch closeout

After Task 5, run the full verification gates:

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

Update the `## Dated resolution note` section of `docs/13-project-audit-2026-07-11.md` with a one-paragraph entry citing the actual commit range and branch.

---

## Self-Review

**Spec coverage:**
- `db.DeleteSession` removes the row; the existing `messages.session_id` FK CASCADE handles message removal.
- `perCwdLister.DeleteSession` opens/migrates/deletes/closes per call (mirrors `ListSessions`).
- `SessionManager.Delete` cancels and closes any loaded runtime for the id (`replaceExisting`) before calling the lister. Returns `-32000` for an unknown session id (matches the `session/close` precedent).
- `initialize` advertises `sessionCapabilities.delete: {}`; the `forbidden` slice in `TestRunInitializeCapabilities` drops `delete`.
- Docs and audit log updated with the real commit hashes; the audit-doc "global index" note is reframed as a future feature, not a blocker.

**Type consistency:**
- `SessionLister` interface gains `DeleteSession(ctx, cwd, sessionID) (bool, error)`. The `fakeLister` in the test file mirrors it.
- `Delete` returns `map[string]any{}` on success, matching the shape of `session/close` and `session/resume`.

**Placeholder scan:** No TBDs. The `additionalDirectories` rejection in `TestSessionLifecycleValidation` was lifted in the prior batch; if the implementer finds the delete case still guarded by `additionalDirectories`, the brief here is consistent with the post-lift state and they should remove the relevant test row.
