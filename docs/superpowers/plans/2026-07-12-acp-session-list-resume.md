# ACP Session List & Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the stabilized ACP v1 `session/list` and `session/resume` methods in Marshal's headless ACP transport, advertising exactly the capabilities we implement.

**Architecture:** Add a per-cwd `ListSessions` query to `internal/db` (sessions live in `<cwd>/.marshal/marshal.db`, one DB per project). Inject a `SessionLister` seam into the ACP `SessionManager` so `session/list` is testable with a fake and wired in production to a small adapter that opens the matching per-cwd DB. `session/resume` reuses the existing `Load` machinery (`StartRuntime` + `WithExistingSession`) but skips the replay loop and returns an empty object. `initialize` advertises `sessionCapabilities.list` and `sessionCapabilities.resume` alongside the existing `close`; everything else (delete, image, audio, embeddedContext, additionalDirectories, mcp) remains honestly unadvertised.

**Tech Stack:** Go 1.26.1, `database/sql` over `modernc.org/sqlite`, JSON-RPC 2.0 over stdio, ACP v1.

## Global Constraints

- **Truthful capabilities:** `initialize` advertises only the methods we implement. After this batch the advertised lifecycle caps are `close`, `list`, and `resume` — never `delete` (Batch A2), never `additionalDirectories`, never `mcp`/`mcpCapabilities`. A capability's handler is registered and advertised in the same task that owns it; the per-cwd `SessionLister` production wiring for `list` is added in Task 4 (the handler returns a clear server error until then). The branch is merged only after the full gates pass, so no intermediate commit is released.
- **Per-cwd DBs:** there is no global session registry. `session/list` therefore requires an absolute `cwd` and filters by it; a request with no `cwd` returns `-32602`. This is an honest, documented limitation.
- **Error codes:** param errors use `invalidParamsError` (`-32602`); internal/session errors use `serverErrorf` (`-32000`). Context cancellation surfaces as `-32800`.
- **Concurrency:** `SessionManager` keeps its existing lock ordering (`lifecycleMu` before `mu`). New handlers reuse `replaceExisting` / `publishReplacement` / `detach` unchanged.
- **No comments** unless asked. Match existing `gofmt` style.
- Verification gates at the end of every task: `go test -count=1 <pkg>`, plus the full gates (`go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`) before the batch closes.

## File Structure

- `internal/db/sessions.go` — add `SessionEntry` struct and `ListSessions(ctx, cwd, cursor, limit)` query.
- `internal/db/sessions_test.go` — new tests for `ListSessions` ordering, filtering, pagination, `updatedAt`/`messageCount` derivation.
- `internal/acp/session.go` — add `SessionLister` interface, `Lister` field on `SessionManager` + `SessionManagerConfig`, `List` handler (validation + projection), and `Resume` handler (validation + restore-without-replay).
- `internal/acp/session_test.go` — extend `TestSessionLifecycleValidation` with `list` and `resume` cases; add `TestSessionListProjectsFromLister`, `TestSessionListPagination`, `TestSessionResumeRestoresWithoutReplay`, `TestSessionResumeClosesOldRuntime`.
- `internal/acp/run.go` — register `session/list` and `session/resume`; add `list` and `resume` to `sessionCapabilities`; add a `lister` field to `runConfig` and wire it into `SessionManager`.
- `internal/acp/run_test.go` — update `TestRunInitializeCapabilities`: drop `list`/`resume` from the forbidden list and assert they are advertised as empty objects; add a lister-aware positive test.
- `internal/acp/lister.go` — new file: the production `*db.DB`-backed `SessionLister` adapter plus a per-call opener used by `Run`.
- `internal/acp/lister_test.go` — new file: tests for the real lister against a temp per-cwd DB.
- `docs/10-acp.md` — update the support matrix and the unsupported-features table.

---

## Task 1: DB — `ListSessions` query and `SessionEntry`

**Files:**
- Modify: `internal/db/sessions.go`
- Test: `internal/db/sessions_test.go`

**Interfaces:**
- Produces: `type SessionEntry struct { SessionID, Cwd, Title string; UpdatedAt time.Time; MessageCount int }` and `func (db *DB) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]SessionEntry, string, error)`. `cursor` is an opaque base64-encoded offset; an empty/unparseable cursor is an error. `nextCursor` is the empty string when there are no more results. `cwd` must be an absolute path that resolves to an existing project root; if no project matches, return an empty slice (no error).

- [ ] **Step 1: Write the failing tests**

Append to `internal/db/sessions_test.go` (after the existing tests). Note this file is `package db`, so call unqualified `Open`, `Migrate`, `GetOrCreateProject`, etc. Add `context` and `fmt` to the import block if absent (`time` is already imported).

```go
func TestListSessions(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cwd := "/home/user/project"
	pid, err := d.GetOrCreateProject(cwd, "project")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	otherCwd := "/home/user/other"
	pid2, err := d.GetOrCreateProject(otherCwd, "other")
	if err != nil {
		t.Fatalf("get or create project 2: %v", err)
	}
	_ = pid2

	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_old", pid, "", t0); err != nil {
		t.Fatalf("create old: %v", err)
	}
	if err := d.CreateSession("sess_new", pid, "Implement list", t0.Add(2*time.Hour)); err != nil {
		t.Fatalf("create new: %v", err)
	}
	if _, err := d.SaveMessage("sess_new", "user", "hello", "plain", t0.Add(3*time.Hour), "", 0, false, 0); err != nil {
		t.Fatalf("save msg: %v", err)
	}
	// Session in a different project must never appear.
	if err := d.CreateSession("sess_other", pid2, "other", t0.Add(time.Hour)); err != nil {
		t.Fatalf("create other: %v", err)
	}

	// Filter by cwd, default limit.
	got, next, err := d.ListSessions(context.Background(), cwd, "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if next != "" {
		t.Fatalf("nextCursor = %q, want empty", next)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].SessionID != "sess_new" || got[1].SessionID != "sess_old" {
		t.Fatalf("order = %+v, want [sess_new sess_old]", got)
	}
	if got[0].Title != "Implement list" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].Cwd != cwd {
		t.Fatalf("cwd = %q", got[0].Cwd)
	}
	if !got[0].UpdatedAt.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("updatedAt = %v, want %v", got[0].UpdatedAt, t0.Add(3*time.Hour))
	}
	if got[0].MessageCount != 1 {
		t.Fatalf("messageCount = %d, want 1", got[0].MessageCount)
	}
	// old session has no messages: updatedAt falls back to started_at.
	if !got[1].UpdatedAt.Equal(t0) {
		t.Fatalf("updatedAt fallback = %v, want %v", got[1].UpdatedAt, t0)
	}
	if got[1].MessageCount != 0 {
		t.Fatalf("messageCount = %d, want 0", got[1].MessageCount)
	}

	// Unknown cwd returns empty slice, no error.
	unknown, _, err := d.ListSessions(context.Background(), "/no/such/project", "", 0)
	if err != nil {
		t.Fatalf("unknown cwd: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown cwd returned %+v", unknown)
	}
}

func TestListSessionsPagination(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cwd := "/home/user/pp"
	pid, err := d.GetOrCreateProject(cwd, "pp")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := d.CreateSession(fmt.Sprintf("sess_%d", i), pid, "", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Page size 2 from the top (newest first).
	page1, next1, err := d.ListSessions(context.Background(), cwd, "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || page1[0].SessionID != "sess_4" || page1[1].SessionID != "sess_3" {
		t.Fatalf("page1 = %+v", page1)
	}
	if next1 == "" {
		t.Fatalf("expected nextCursor on page1")
	}
	page2, next2, err := d.ListSessions(context.Background(), cwd, next1, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || page2[0].SessionID != "sess_2" || page2[1].SessionID != "sess_1" {
		t.Fatalf("page2 = %+v", page2)
	}
	if next2 == "" {
		t.Fatalf("expected nextCursor on page2")
	}
	page3, next3, err := d.ListSessions(context.Background(), cwd, next2, 2)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 || page3[0].SessionID != "sess_0" {
		t.Fatalf("page3 = %+v", page3)
	}
	if next3 != "" {
		t.Fatalf("next3 = %q, want empty", next3)
	}

	// Invalid cursor is an error.
	if _, _, err := d.ListSessions(context.Background(), cwd, "not-base64!!!", 2); err == nil {
		t.Fatalf("expected error for invalid cursor")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/db`
Expected: FAIL — `ListSessions` undefined, `SessionEntry` undefined.

- [ ] **Step 3: Implement `SessionEntry` and `ListSessions`**

Append to `internal/db/sessions.go`. The cursor is an opaque, base64-encoded decimal offset. `updatedAt` is the session's last message `created_at`, falling back to `started_at` when there are no messages. Results are ordered by `updatedAt DESC, id DESC` (newest activity first). `limit` defaults to 50 and is clamped to the inclusive range `[1, 200]`.

```go
// SessionEntry is a single row returned by ListSessions, projected from an
// agent_sessions row joined to its project root and message activity.
type SessionEntry struct {
	SessionID    string
	Cwd          string
	Title        string
	UpdatedAt    time.Time
	MessageCount int
}

const (
	listSessionsDefaultLimit = 50
	listSessionsMaxLimit     = 200
)

const listSessionsSQL = `
SELECT s.id,
       p.root_path,
       s.title,
       COALESCE((SELECT MAX(m.created_at) FROM messages m WHERE m.session_id = s.id), s.started_at) AS updated_at,
       (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id) AS message_count
FROM agent_sessions s
JOIN projects p ON p.id = s.project_id
WHERE p.root_path = ?
ORDER BY updated_at DESC, s.id DESC
LIMIT ? OFFSET ?`

// ListSessions returns sessions whose project root matches cwd, newest
// activity first. cursor is an opaque base64-encoded offset from a previous
// nextCursor; pass "" to start from the beginning. limit defaults to 50 and
// is clamped to [1, 200]. The returned nextCursor is empty when no more rows
// remain. A cursor that cannot be decoded returns an error.
func (db *DB) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]SessionEntry, string, error) {
	if limit <= 0 {
		limit = listSessionsDefaultLimit
	}
	if limit > listSessionsMaxLimit {
		limit = listSessionsMaxLimit
	}

	offset, err := decodeListCursor(cursor)
	if err != nil {
		return nil, "", fmt.Errorf("decode session list cursor: %w", err)
	}

	rows, err := db.sqlDB.QueryContext(ctx, listSessionsSQL, cwd, limit+1, offset)
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionEntry
	for rows.Next() {
		var (
			e         SessionEntry
			updatedAt string
			title     sql.NullString
		)
		if err := rows.Scan(&e.SessionID, &e.Cwd, &title, &updatedAt, &e.MessageCount); err != nil {
			return nil, "", fmt.Errorf("scan session row: %w", err)
		}
		if title.Valid {
			e.Title = title.String
		}
		parsed, perr := time.Parse(time.RFC3339, updatedAt)
		if perr != nil {
			return nil, "", fmt.Errorf("parse updated_at %q: %w", updatedAt, perr)
		}
		e.UpdatedAt = parsed.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate session rows: %w", err)
	}

	var nextCursor string
	if len(out) > limit {
		nextCursor = encodeListCursor(offset + limit)
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func encodeListCursor(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeListCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	dec, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(dec))
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative cursor offset: %d", n)
	}
	return n, nil
}
```

Add the new imports to the `internal/db/sessions.go` import block: `context`, `encoding/base64`, `strconv`. The existing import block already has `database/sql`, `errors`, `fmt`, `time` — keep them. The scan reads exactly five columns in the SELECT order: `id`, `root_path`, `title`, `updated_at`, `message_count`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -count=1 ./internal/db`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/db && gofmt -w internal/db/sessions.go internal/db/sessions_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/db/sessions.go internal/db/sessions_test.go
git commit -m "feat(db): add ListSessions query for ACP session/list"
```

---

## Task 2: ACP `session/list` handler + capability advertisement

**Files:**
- Modify: `internal/acp/session.go`
- Modify: `internal/acp/session_test.go`
- Modify: `internal/acp/run.go`
- Modify: `internal/acp/run_test.go`

**Interfaces:**
- Consumes: `db.SessionEntry` and `db.DB.ListSessions` from Task 1.
- Produces: `type SessionLister interface { ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) }`; a `Lister SessionLister` field on `SessionManager` and `SessionManagerConfig`; `func (m *SessionManager) List(ctx, params) (any, error)` registered as `session/list`; `initialize` advertises `sessionCapabilities.list`.

- [ ] **Step 1: Write the failing list-validation and projection tests**

In `internal/acp/session_test.go`, first extend `TestSessionLifecycleValidation`'s dispatch switch so `list` and `resume` route somewhere (resume lands in Task 3). Add cases for `list` (`session/list` accepts only `cwd` and `cursor`; unknown fields like `mcpServers` are ignored, matching the lenient `encoding/json` default):

```go
		{"list missing cwd", "list", `{}`, invalidParams},
		{"list relative cwd", "list", `{"cwd":"relative/path"}`, invalidParams},
```

Add a `"list"` branch to the switch in that test:

```go
			case "list":
				res, err = m.List(context.Background(), json.RawMessage(tc.params))
```

(Also add a `"resume"` branch that calls `m.Resume(...)` now; the `resume` cases are added in Task 3. For this task, leave the switch without `resume` cases — it will fail to compile because the previous step already added the branch. Therefore add the `"resume"` branch only in Task 3. Concretely, in this step add only the `"list"` branch and the three list cases above.)

Add a new happy-path projection test below the existing tests:

```go
func TestSessionListProjectsFromLister(t *testing.T) {
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	want := []db.SessionEntry{
		{SessionID: "sess_a", Cwd: absCwd, Title: "A", UpdatedAt: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), MessageCount: 2},
		{SessionID: "sess_b", Cwd: absCwd, Title: "", UpdatedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), MessageCount: 0},
	}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&atomic.Int64{}, nil),
		CloseRuntime: noopClose(),
		Lister: &fakeLister{entries: want, nextCursor: ""},
	})
	m.SetTurnCanceller(noopCancel())

	res, err := m.List(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`"}`))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	obj, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", res)
	}
	sessions, ok := obj["sessions"].([]map[string]any)
	if !ok {
		t.Fatalf("sessions type %T", obj["sessions"])
	}
	if len(sessions) != 2 {
		t.Fatalf("len = %d", len(sessions))
	}
	if sessions[0]["sessionId"] != "sess_a" {
		t.Fatalf("sessions[0] = %+v", sessions[0])
	}
	if sessions[0]["updatedAt"] != "2026-07-03T00:00:00Z" {
		t.Fatalf("updatedAt = %v", sessions[0]["updatedAt"])
	}
	if sessions[0]["cwd"] != absCwd {
		t.Fatalf("cwd = %v", sessions[0]["cwd"])
	}
	if _, hasTitle := sessions[0]["title"].(string); hasTitle && sessions[0]["title"] != "A" {
		t.Fatalf("title = %v", sessions[0]["title"])
	}
	meta, _ := sessions[0]["_meta"].(map[string]any)
	if meta == nil || meta["messageCount"] != float64(2) {
		t.Fatalf("_meta = %+v", meta)
	}
	if sessions[1]["title"] != "" && sessions[1]["title"] != nil {
		t.Fatalf("empty title should be omitted, got %v", sessions[1]["title"])
	}
	if _, hasNext := obj["nextCursor"]; hasNext {
		t.Fatalf("unexpected nextCursor: %+v", obj["nextCursor"])
	}
}
```

Add the fake lister helper near the other fakes at the top of `internal/acp/session_test.go`:

```go
type fakeLister struct {
	entries    []db.SessionEntry
	nextCursor string
	err        error
}

func (f *fakeLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.entries, f.nextCursor, nil
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/acp`
Expected: FAIL — `m.List` undefined, `SessionManagerConfig.Lister` unknown, `SessionLister` undefined.

- [ ] **Step 3: Implement the `SessionLister` interface, the `Lister` field, and the `List` handler**

In `internal/acp/session.go`:

Add an import for `marshal/internal/db` (the file already imports `marshal/internal/app`; add `db` alongside).

Add the interface and config field. Place the interface near the other type declarations before `SessionManagerConfig`:

```go
// SessionLister exposes per-cwd session discovery for the session/list
// handler. The production implementation opens the matching per-cwd
// database; tests inject a fake.
type SessionLister interface {
	ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error)
}
```

Add `Lister SessionLister` to `SessionManagerConfig`:

```go
type SessionManagerConfig struct {
	StartRuntime      RuntimeStarter
	CloseRuntime       RuntimeCloser
	Notify            NotifyFunc
	Lister            SessionLister
	Options           []app.Option
}
```

Store it on `SessionManager` (add a field `lister SessionLister` next to `cancel TurnCanceller`) and initialise it in `NewSessionManager`:

```go
	return &SessionManager{
		start:   cfg.StartRuntime,
		close:   closeFn,
		notify:  cfg.Notify,
		lister:  cfg.Lister,
		options: cfg.Options,
		sessions: map[string]*app.Runtime{},
	}
```

Add the `List` handler. `listParams` is a distinct shape (`cwd` required, optional opaque `cursor`); it deliberately does not carry `mcpServers`/`sessionId`.

```go
type listParams struct {
	Cwd    string `json:"cwd"`
	Cursor string `json:"cursor,omitempty"`
}

// List handles session/list. It validates cwd (required, absolute),
// queries the injected SessionLister, and projects the result into the
// ACP SessionInfo[] shape with cursor pagination metadata. mcpServers is
// not accepted on list requests.
func (m *SessionManager) List(ctx context.Context, params json.RawMessage) (any, error) {
	if m.lister == nil {
		return nil, serverErrorf("acp: SessionManager has no SessionLister configured")
	}
	var p listParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/list params: %v", err)
		}
	}
	if strings.TrimSpace(p.Cwd) == "" {
		return nil, invalidParamsError("cwd is required for session/list")
	}
	if !filepath.IsAbs(p.Cwd) {
		return nil, invalidParamsError("cwd must be an absolute path")
	}

	entries, next, err := m.lister.ListSessions(ctx, p.Cwd, p.Cursor, 0)
	if err != nil {
		return nil, err
	}

	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{
			"sessionId": e.SessionID,
			"cwd":        e.Cwd,
			"_meta":      map[string]any{"messageCount": e.MessageCount},
		}
		if e.Title != "" {
			item["title"] = e.Title
		}
		if !e.UpdatedAt.IsZero() {
			item["updatedAt"] = e.UpdatedAt.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	result := map[string]any{"sessions": items}
	if next != "" {
		result["nextCursor"] = next
	}
	return result, nil
}
```

- [ ] **Step 4: Register `session/list` in `run.go` and advertise the capability**

In `internal/acp/run.go`, after the existing `srv.Handle("session/close", manager.CloseSession)` line, add:

```go
	srv.Handle("session/list", manager.List)
```

Extend the `sessionCapabilities` map in the `initialize` handler so it reads:

```go
			"sessionCapabilities": map[string]any{
				"close":  map[string]any{},
				"list":   map[string]any{},
			},
```

(Do not add `resume` here — Task 3 does that, keeping each commit internally consistent.)

- [ ] **Step 5: Update `run_test.go` capability assertions**

In `internal/acp/run_test.go` `TestRunInitializeCapabilities`'s "basic capabilities" subtest:

- Remove `"list"` and (in this task only `"list"`) from the `forbidden` slice. Leave `"resume"`, `"delete"`, `"image"`, `"audio"`, `"embeddedContext"`, `"additionalDirectories"`, `"mcp"` forbidden.
  After this task the forbidden slice is:
  ```go
  forbidden := []string{"image", "audio", "embeddedContext", "resume", "delete", "additionalDirectories", "mcp"}
  ```
- Add a positive assertion right after the `close` block:

```go
		// sessionCapabilities.list is an empty object.
		listCap, ok := sessionCaps["list"]
		if !ok {
			t.Fatalf("sessionCapabilities.list missing")
		}
		listObj, ok := listCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.list is not an object: %T", listCap)
		}
		if len(listObj) != 0 {
			t.Fatalf("sessionCapabilities.list = %v, want empty object", listObj)
		}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -count=1 ./internal/acp`
Expected: PASS.

- [ ] **Step 7: Vet and format**

Run: `go vet ./internal/acp && gofmt -w internal/acp/session.go internal/acp/session_test.go internal/acp/run.go internal/acp/run_test.go`
Expected: no issues.

- [ ] **Step 8: Commit**

```bash
git add internal/acp/session.go internal/acp/session_test.go internal/acp/run.go internal/acp/run_test.go
git commit -m "feat(acp): implement session/list with cwd-scoped discovery"
```

---

## Task 3: ACP `session/resume` handler + capability advertisement

**Files:**
- Modify: `internal/acp/session.go`
- Modify: `internal/acp/session_test.go`
- Modify: `internal/acp/run.go`
- Modify: `internal/acp/run_test.go`

**Interfaces:**
- Consumes: `validateLifecycleParams`, `replaceExisting`, `publishReplacement`, `app.WithExistingSession`, `app.WithWorkingDir` — all already produced by the existing `Load` implementation.
- Produces: `func (m *SessionManager) Resume(ctx, params) (any, error)` registered as `session/resume`; `initialize` advertises `sessionCapabilities.resume`.

- [ ] **Step 1: Write the failing resume tests**

Extend `TestSessionLifecycleValidation` cases with resume rows:

```go
		{"resume missing sessionId", "resume", `{"cwd":"` + absCwd + `","mcpServers":[]}`, invalidParams},
		{"resume relative cwd", "resume", `{"cwd":"relative/path","sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"resume missing cwd", "resume", `{"sessionId":"sess_x","mcpServers":[]}`, invalidParams},
		{"resume non-empty mcpServers", "resume", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[{"name":"x"}]}`, invalidParams},
		{"resume non-empty additional directories", "resume", `{"cwd":"` + absCwd + `","sessionId":"sess_x","mcpServers":[],"additionalDirectories":["/tmp"]}`, invalidParams},
```

Add the `"resume"` branch to the dispatch switch:

```go
			case "resume":
				res, err = m.Resume(context.Background(), json.RawMessage(tc.params))
```

The existing `TestSessionLifecycleValidation` manager already wires `Notify` (a no-op) and `SetTurnCanceller`; resume needs canceller but not notify, so the existing setup is fine.

Add a dedicated happy-path test proving resume restores the runtime and does NOT replay (returns `{}`), and that it tears down any prior runtime for the same id:

```go
func TestSessionResumeRestoresWithoutReplay(t *testing.T) {
	var idSeq atomic.Int64
	var notifyCount atomic.Int64
	notifier := func(method string, params any) error {
		notifyCount.Add(1)
		return nil
	}
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: fakeRuntimeStart(&idSeq, nil),
		CloseRuntime: noopClose(),
		Notify:       notifier,
	})
	m.SetTurnCanceller(noopCancel())

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	// Use a fixed pre-existing session id; the fake starter ignores it and
	// returns sess_<n>, so check that the published id differs from the
	// requested id only insofar as the starter controls the SessionID.
	_, err := m.Resume(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_resume_ok","mcpServers":[]}`))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if notifyCount.Load() != 0 {
		t.Fatalf("session/update emitted %d notifications, want 0", notifyCount.Load())
	}
}

func TestSessionResumeClosesOldRuntime(t *testing.T) {
	var idSeq atomic.Int64
	var (
		mu     sync.Mutex
		events []string
	)
	starter := fakeRuntimeStart(&idSeq, nil)
	closer := func(ctx context.Context, rt *app.Runtime) error {
		mu.Lock()
		events = append(events, "close "+rt.SessionID)
		mu.Unlock()
		return nil
	}
	canceller := func(ctx context.Context, id string) error {
		mu.Lock()
		events = append(events, "cancel "+id)
		mu.Unlock()
		return nil
	}
	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: starter,
		CloseRuntime: closer,
	})
	m.SetTurnCanceller(canceller)

	// First resume publishes a runtime under sess_resume_close.
	if _, err := m.Resume(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_resume_close","mcpServers":[]}`)); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	// Second resume for the same id must cancel+close the prior runtime
	// before publishing the new one.
	if _, err := m.Resume(context.Background(), json.RawMessage(`{"cwd":"`+absCwd+`","sessionId":"sess_resume_close","mcpServers":[]}`)); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "cancel sess_resume_close") {
		t.Fatalf("expected turn cancel for sess_resume_close, events=%q", joined)
	}
	if !strings.Contains(joined, "close ") {
		t.Fatalf("expected a close event, events=%q", joined)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/acp`
Expected: FAIL — `m.Resume` undefined.

- [ ] **Step 3: Implement the `Resume` handler**

In `internal/acp/session.go`, add `Resume` right after `Load`. It reuses `validateLifecycleParams(&p, true)` (sessionId required), `requireReady` (needs the canceller), `replaceExisting`, `StartRuntime` with `WithWorkingDir` + `WithExistingSession`, and `publishReplacement`. It does NOT call `replay` and does NOT require `Notify`. It returns an empty object.

```go
// Resume handles session/resume. It restores an existing persisted session
// like Load but, per ACP v1, MUST NOT replay conversation history. It
// validates params, cancels and closes any pre-existing runtime for the
// same id, starts a new runtime with WithExistingSession, publishes it,
// and returns an empty result object.
func (m *SessionManager) Resume(ctx context.Context, params json.RawMessage) (any, error) {
	if m.start == nil {
		return nil, fmt.Errorf("acp: SessionManager has no StartRuntime configured")
	}
	cancel, err := m.requireReady()
	if err != nil {
		return nil, err
	}
	var p sessionParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("acp: parse session/resume params: %w", err)
		}
	}
	if err := validateLifecycleParams(&p, true); err != nil {
		return nil, err
	}

	if _, replaceErr := m.replaceExisting(ctx, p.SessionID, cancel); replaceErr != nil {
		return nil, replaceErr
	}

	opts := append([]app.Option{}, m.options...)
	opts = append(opts, app.WithWorkingDir(p.Cwd), app.WithExistingSession(p.SessionID))
	rt, err := m.start(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("acp: start runtime: %w", err)
	}
	m.publishReplacement(rt.SessionID, rt)
	return map[string]any{}, nil
}
```

- [ ] **Step 4: Register `session/resume` in `run.go` and advertise the capability**

In `internal/acp/run.go`, after the `session/list` registration added in Task 2, add:

```go
	srv.Handle("session/resume", manager.Resume)
```

Extend the `sessionCapabilities` map so it reads:

```go
			"sessionCapabilities": map[string]any{
				"close":  map[string]any{},
				"list":   map[string]any{},
				"resume": map[string]any{},
			},
```

- [ ] **Step 5: Update `run_test.go` capability assertions for resume**

In `TestRunInitializeCapabilities`'s "basic capabilities" subtest:

- Remove `"resume"` from the `forbidden` slice. After this task the forbidden slice is:
  ```go
  forbidden := []string{"image", "audio", "embeddedContext", "delete", "additionalDirectories", "mcp"}
  ```
- Add a positive assertion mirroring the `list` one from Task 2:

```go
		// sessionCapabilities.resume is an empty object.
		resumeCap, ok := sessionCaps["resume"]
		if !ok {
			t.Fatalf("sessionCapabilities.resume missing")
		}
		resumeObj, ok := resumeCap.(map[string]any)
		if !ok {
			t.Fatalf("sessionCapabilities.resume is not an object: %T", resumeCap)
		}
		if len(resumeObj) != 0 {
			t.Fatalf("sessionCapabilities.resume = %v, want empty object", resumeObj)
		}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -count=1 ./internal/acp`
Expected: PASS.

- [ ] **Step 7: Vet and format**

Run: `go vet ./internal/acp && gofmt -w internal/acp/session.go internal/acp/session_test.go internal/acp/run.go internal/acp/run_test.go`
Expected: no issues.

- [ ] **Step 8: Commit**

```bash
git add internal/acp/session.go internal/acp/session_test.go internal/acp/run.go internal/acp/run_test.go
git commit -m "feat(acp): implement session/resume without replay"
```

---

## Task 4: Production `SessionLister` wiring + lister integration test

**Files:**
- Create: `internal/acp/lister.go`
- Create: `internal/acp/lister_test.go`
- Modify: `internal/acp/run.go`

**Interfaces:**
- Consumes: `db.Open`, `db.Migrate`, `db.DB.ListSessions` from Task 1; the `SessionLister` interface from Task 2.
- Produces: `type perCwdLister struct { ... }` implementing `SessionLister` by opening `<cwd>/.marshal/marshal.db`, migrating, listing, and closing the handle on every call; `func newPerCwdLister() *perCwdLister`; the `runConfig.lister` field; production wiring in `Run`/`runWithConfig`.

- [ ] **Step 1: Write the failing test against a real per-cwd DB**

`internal/acp/lister_test.go`:

```go
package acp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/db"
)

func TestPerCwdListerRealDB(t *testing.T) {
	root := t.TempDir()
	absCwd, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// Seed a real per-cwd DB exactly as StartRuntime would.
	d, err := db.Open(filepath.Join(absCwd, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		t.Fatalf("migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject(absCwd, "project")
	if err != nil {
		_ = d.Close()
		t.Fatalf("project: %v", err)
	}
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := d.CreateSession("sess_alpha", pid, "Alpha", t0.Add(time.Hour)); err != nil {
		_ = d.Close()
		t.Fatalf("create: %v", err)
	}
	if _, err := d.SaveMessage("sess_alpha", "user", "hi", "plain", t0.Add(2*time.Hour), "", 0, false, 0); err != nil {
		_ = d.Close()
		t.Fatalf("save: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	// The lister must open, list, and close per call.
	l := newPerCwdLister()
	got, next, err := l.ListSessions(context.Background(), absCwd, "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if next != "" {
		t.Fatalf("nextCursor = %q, want empty", next)
	}
	if len(got) != 1 || got[0].SessionID != "sess_alpha" {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Cwd != absCwd {
		t.Fatalf("cwd = %q", got[0].Cwd)
	}
	if got[0].Title != "Alpha" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].MessageCount != 1 {
		t.Fatalf("messageCount = %d", got[0].MessageCount)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -count=1 ./internal/acp`
Expected: FAIL — `newPerCwdLister` undefined.

- [ ] **Step 3: Implement the production lister**

`internal/acp/lister.go`:

```go
package acp

import (
	"context"
	"path/filepath"

	"marshal/internal/db"
)

// perCwdLister implements SessionLister by opening the per-cwd Marshal
// database (<cwd>/.marshal/marshal.db), migrating it idempotently,
// querying sessions, and closing the handle before returning. Each call
// is independent; there is no connection pooling across list requests.
type perCwdLister struct{}

func newPerCwdLister() *perCwdLister { return &perCwdLister{} }

func (l *perCwdLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	d, err := db.Open(filepath.Join(cwd, ".marshal", "marshal.db"))
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		return nil, "", err
	}
	return d.ListSessions(ctx, cwd, cursor, limit)
}
```

- [ ] **Step 4: Wire the lister into `runConfig` and `Run`**

In `internal/acp/run.go`:

Add a `lister SessionLister` field to `runConfig`:

```go
type runConfig struct {
	startRuntime RuntimeStarter
	closeRuntime RuntimeCloser
	lister       SessionLister
	shutdown     time.Duration
}
```

Pass it to the `SessionManager`:

```go
	manager := NewSessionManager(SessionManagerConfig{
		StartRuntime: cfg.startRuntime,
		CloseRuntime: cfg.closeRuntime,
		Lister:       cfg.lister,
		Notify:       srv.Notify,
	})
```

Update `Run` to inject the production lister:

```go
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	return runWithConfig(ctx, stdin, stdout, stderr, runConfig{
		startRuntime: app.StartRuntime,
		lister:       newPerCwdLister(),
		shutdown:     connectionShutdownTimeout,
	})
}
```

(Existing tests in `run_test.go` construct `runConfig` with only `startRuntime`/`closeRuntime`/`shutdown`; the new `lister` field is nil there, which is fine because those tests do not exercise `session/list`. The `List` handler already returns a clear server error when `lister` is nil.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -count=1 ./internal/acp`
Expected: PASS.

- [ ] **Step 6: Add a lister-aware end-to-end test over the wire (optional but recommended)**

Add to `internal/acp/run_test.go` a test that drives `runWithConfig` with a real per-cwd DB and asserts the `session/list` wire response. Mirror the existing `TestRunInitializeCapabilities` framing style (Buffer scanner over stdout). This guards the run.go registration + capability wiring together. If the existing integration tests already cover handler wiring through `runWithConfig`, this step may be folded into the existing suite — otherwise add a focused test:

```go
func TestRunSessionListWire(t *testing.T) {
	root := t.TempDir()
	absCwd, _ := filepath.Abs(root)
	d, err := db.Open(filepath.Join(absCwd, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Migrate(); err != nil {
		_ = d.Close()
		t.Fatalf("migrate: %v", err)
	}
	pid, _ := d.GetOrCreateProject(absCwd, "p")
	if err := d.CreateSession("sess_wire", pid, "Wire", time.Now().UTC()); err != nil {
		_ = d.Close()
		t.Fatalf("create: %v", err)
	}
	_ = d.Close()

	req := `{"jsonrpc":"2.0","id":1,"method":"session/list","params":{"cwd":"` + absCwd + `"}}` + "\n"
	in := strings.NewReader(req)
	out := &bytes.Buffer{}
	cfg := runConfig{
		startRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) { return &app.Runtime{}, nil },
		closeRuntime: func(ctx context.Context, rt *app.Runtime) error { return nil },
		lister:       newPerCwdLister(),
		shutdown:     0,
	}
	if err := runWithConfig(context.Background(), in, out, io.Discard, cfg); err != nil {
		t.Fatalf("runWithConfig: %v", err)
	}
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scan.Scan() {
		t.Fatalf("no response; output=%q", out.String())
	}
	var resp Response
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result %T", resp.Result)
	}
	sessions, ok := res["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions %T", res["sessions"])
	}
	var found bool
	for _, s := range sessions {
		m, _ := s.(map[string]any)
		if m["sessionId"] == "sess_wire" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sess_wire not in sessions: %+v", sessions)
	}
}
```

Add imports `bufio`, `io`, `time`, `marshal/internal/db` to `run_test.go` if missing.

- [ ] **Step 7: Vet and format**

Run: `go vet ./internal/acp && gofmt -w internal/acp/lister.go internal/acp/lister_test.go internal/acp/run.go internal/acp/run_test.go`
Expected: no issues.

- [ ] **Step 8: Commit**

```bash
git add internal/acp/lister.go internal/acp/lister_test.go internal/acp/run.go internal/acp/run_test.go
git commit -m "feat(acp): wire per-cwd SessionLister for production session/list"
```

---

## Task 5: Docs — update the ACP support matrix

**Files:**
- Modify: `docs/10-acp.md`

- [ ] **Step 1: Update the supported-methods table**

In the **Supported methods** table, add two rows after the `session/load` row:

```markdown
| `session/list` | Full | Requires an absolute `cwd`. Filters by project root; no global session registry exists, so a request with no `cwd` returns `-32602`. Cursor-paginated. |
| `session/resume` | Full | Restores an existing persisted session like `session/load` but does **not** replay history. Returns an empty object. |
```

- [ ] **Step 2: Update the unsupported-features table**

In the **Unsupported features** table, change the `session/list`/`session/resume` entry. Replace the existing row:

```markdown
| `session/resume`, `session/list`, `session/delete` | Not implemented. |
```

with:

```markdown
| `session/delete` | Not implemented. Requires a global session-id → project-root index that does not yet exist; tracked for a follow-up batch. |
```

Leave the remaining rows (`$/cancel_request`, dynamic MCP server arrays, additional workspace directories, image/audio/embedded resource blocks, full tool-call/plan/config-option projection, structured question/elicitation, HTTP/SSE/WS transports, ACP v2) unchanged.

- [ ] **Step 3: Update the capability-advertisement note**

In the **Newly verified protocol corrections**-style description (or add a short paragraph under **Supported methods**) record the resulting advertisement:

```markdown
### Advertised capabilities

`initialize` reports `agentCapabilities.loadSession: true` and
`sessionCapabilities: { close, list, resume }` (each an empty object). No
other lifecycle, content, or MCP capabilities are advertised.
```

- [ ] **Step 4: Commit**

```bash
git add docs/10-acp.md
git commit -m "docs(acp): document session/list and session/resume support"
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

Then, on the implementation branch, append a new "Implementation batch — ACP session discovery (list/resume)" section to `docs/13-project-audit-2026-07-11.md` mirroring the existing ACP batch sections: list the commit range, the two newly supported methods, the cwd-required limitation for `session/list`, and the unchanged unadvertised set. Use the real commit hashes once the batch lands (do not commit a placeholder commit range).