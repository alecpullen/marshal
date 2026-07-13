# ACP Session Additional Directories Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop rejecting ACP `additionalDirectories` and actually accept the list, plumb it through to the headless runtime as additional cwd roots for tool execution, and advertise `additionalDirectories: true` in `sessionCapabilities`. This is the smallest of the three follow-on ACP capabilities.

**Architecture:** `internal/acp/session.go`'s `validateLifecycleParams` removes the rejection of `AdditionalDirectories` and adds a hard cap (e.g. 8). `Create`/`Load`/`Resume` pass the list to a new `app.Option` `WithAdditionalDirectories([]string)`. The runtime stores the list and exposes it via a getter; the tool execution path injects the directories into the shell sandbox's allowed cwd list (and uses the first as the sandbox root if no `WithWorkingDir` was given). `initialize` advertises `sessionCapabilities.additionalDirectories: {}` and the run_test capability test drops it from the forbidden list.

**Tech Stack:** Go 1.26.1, modernc.org/sqlite, JSON-RPC 2.0 over stdio, ACP v1. Reuses the existing `app.Option` plumbing (`internal/app/runtime.go`) and the shell sandbox's `restricted` backend.

**Assumes Milestone Q+ is complete** (it is: `internal/sandbox/` has `restricted`/`container`/`passthrough` backends; `WithWorkingDir` is an existing `app.Option`).

## Global Constraints

- Truthful capabilities: after this batch, `initialize` advertises `sessionCapabilities: { close, list, resume, additionalDirectories }` (each an empty object). Nothing else changes.
- Per-cwd DBs / lock ordering / error codes / context cancellation: same as Milestone R / the prior ACP batch. `invalidParamsError` (`-32602`) for param errors, `serverErrorf` (`-32000`) for internal errors, `-32800` for context cancellation.
- Hard cap: reject lists longer than 8 entries with `-32602` to bound the sandbox-cwd injection surface.
- Path validation: each additional directory must be an absolute path AND a directory that exists (or the parent exists and is creatable — see below). Reject with `-32602` on bad paths.
- Secrets: additional directory paths are not secret; render them in test-connection errors if they fail to apply.
- No comments unless asked. Match existing gofmt style.
- Verification: `go test -count=1 ./internal/acp/... ./internal/app/... ./internal/sandbox/...` after every task, plus the full gates (`go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`) before batch closeout.

## File Structure

**Modify:**
- `internal/app/runtime.go` — add `WithAdditionalDirectories([]string) app.Option`, store the list on the runtime, expose `Runtime.AdditionalDirectories() []string`, and have the runtime's sandbox/working-dir wiring consume the list.
- `internal/app/runtime_test.go` — add tests for the new option + getter.
- `internal/acp/session.go` — drop the rejection in `validateLifecycleParams`, add the 8-entry cap, pass the list to `StartRuntime` via the new option in `Create`/`Load`/`Resume`.
- `internal/acp/session_test.go` — update `TestSessionLifecycleValidation` (remove the "create/load/resume non-empty additional directories" cases, replace with the 8-entry cap and bad-path cases), and add a positive test that the option is plumbed.
- `internal/acp/run.go` — advertise `additionalDirectories: map[string]any{}` in `sessionCapabilities`.
- `internal/acp/run_test.go` — drop `additionalDirectories` from the `forbidden` slice in `TestRunInitializeCapabilities` and add a positive assertion.
- `docs/10-acp.md` — update the `sessionCapabilities` row, the "Additional directories" row in the supported-methods table, and the docs' references to `additionalDirectories` not-yet-supported.
- `docs/13-project-audit-2026-07-11.md` — add an "Implementation batch — ACP additional directories" dated resolution note (mirror the existing batch sections).

---

## Task 1: Runtime option + sandbox wiring

**Files:**
- Modify: `internal/app/runtime.go`
- Test: `internal/app/runtime_test.go`

**Interfaces:**
- Produces: `WithAdditionalDirectories(dirs []string) app.Option`, `Runtime.AdditionalDirectories() []string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/app/runtime_test.go`:

```go
func TestRuntimeAdditionalDirectories(t *testing.T) {
	rt, err := StartRuntime(t.Context(),
		WithWorkingDir(t.TempDir()),
		WithAdditionalDirectories([]string{"/tmp/extra1", "/tmp/extra2"}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(t.Context())

	got := rt.AdditionalDirectories()
	if len(got) != 2 || got[0] != "/tmp/extra1" || got[1] != "/tmp/extra2" {
		t.Fatalf("AdditionalDirectories = %v, want [/tmp/extra1 /tmp/extra2]", got)
	}
}

func TestAdditionalDirectoriesDefaultsToNil(t *testing.T) {
	rt, err := StartRuntime(t.Context(), WithWorkingDir(t.TempDir()))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(t.Context())

	if got := rt.AdditionalDirectories(); got != nil {
		t.Fatalf("AdditionalDirectories = %v, want nil when option not supplied", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/app/ -run 'TestRuntimeAdditionalDirectories|TestAdditionalDirectoriesDefaults' -v`
Expected: FAIL — `undefined: WithAdditionalDirectories`.

- [ ] **Step 3: Add the option, field, and getter**

In `internal/app/runtime.go`:
- Add `additionalDirs []string` to the `Runtime` struct (alongside the existing `cwd string`).
- Add an `applyAdditionalDirs(dirs []string)` method on `*Runtime` (or inline it in the existing apply path).
- Add the option function:

```go
// WithAdditionalDirectories registers extra absolute paths that the
// runtime considers part of the project for tool execution. The shell
// sandbox adds them to the allowed-cwd list; the primary `WithWorkingDir`
// remains the sandbox root. Empty/nil is a no-op.
func WithAdditionalDirectories(dirs []string) Option {
	return func(r *Runtime) {
		if len(dirs) == 0 {
			return
		}
		r.additionalDirs = append(r.additionalDirs, dirs...)
	}
}
```

- Add the getter:

```go
// AdditionalDirectories returns the list of extra workspace roots
// registered via WithAdditionalDirectories, or nil if none.
func (r *Runtime) AdditionalDirectories() []string {
	return r.additionalDirs
}
```

- In the runtime's sandbox-cwd construction (the place that already builds the allowed-cwd list for the `restricted` backend), append `r.additionalDirs` to the allowed roots. If the runtime cannot find the construction point, log a clear error and ASK before adding a new field to the sandbox config.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/app/ -run 'TestRuntimeAdditionalDirectories|TestAdditionalDirectoriesDefaults' -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/app/ && gofmt -w internal/app/runtime.go internal/app/runtime_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/app/runtime.go internal/app/runtime_test.go
git commit -m "feat(app): add WithAdditionalDirectories option to headless runtime"
```

---

## Task 2: Drop ACP rejection, add cap, plumb the option

**Files:**
- Modify: `internal/acp/session.go`
- Test: `internal/acp/session_test.go`

**Interfaces:**
- Produces: ACP handlers `Create`/`Load`/`Resume` pass `app.WithAdditionalDirectories(p.AdditionalDirectories)` to `StartRuntime`. `validateLifecycleParams` no longer rejects non-empty `AdditionalDirectories`; instead enforces `len(p.AdditionalDirectories) <= 8` and that each entry is an absolute path.

- [ ] **Step 1: Write the failing test**

In `internal/acp/session_test.go`, find `TestSessionLifecycleValidation` and:
- Remove these three cases (the rejection is being lifted):
  - `{"create non-empty additional directories", "create", ...}`
  - `{"load non-empty additional directories", "load", ...}`
  - `{"resume non-empty additional directories", "resume", ...}`
- Add the new cap + path-validation cases for each of create/load/resume:
  - `{"create additional directories over cap", "create", `{"cwd":"` + absCwd + `","mcpServers":[],"additionalDirectories":["/a","/b","/c","/d","/e","/f","/g","/h","/i"]}`, invalidParams}`
  - `{"create additional directories relative path", "create", `{"cwd":"` + absCwd + `","mcpServers":[],"additionalDirectories":["relative/path"]}`, invalidParams}`
  - (same for `load` and `resume`)

Add a new positive test that confirms the option is forwarded:

```go
func TestSessionPlumbsAdditionalDirectories(t *testing.T) {
	dirs := []string{"/tmp/add1", "/tmp/add2"}
	var captured []string
	m := NewSessionManager(SessionManagerConfig{
		StartRuntime: func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
			for _, o := range opts {
				o(&app.Runtime{})
			}
			// Use a recording stub runtime that captures the option application.
			rt := &recordingRuntime{}
			for _, o := range opts {
				o(rt.inner)
			}
			captured = append(captured, rt.inner.AdditionalDirectories()...)
			return rt.inner, nil
		},
		CloseRuntime: noopClose(),
	})
	m.SetTurnCanceller(noopCancel())

	body := `{"cwd":"` + absCwd + `","mcpServers":[],"additionalDirectories":["/tmp/add1","/tmp/add2"]}`
	if _, err := m.Create(context.Background(), json.RawMessage(body)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(captured) != 2 || captured[0] != "/tmp/add1" || captured[1] != "/tmp/add2" {
		t.Fatalf("captured = %v, want [/tmp/add1 /tmp/add2]", captured)
	}
}
```

(Use a small `recordingRuntime` helper or adapt the existing `app.Runtime` test pattern; if `app.Runtime` is concrete, mock by closing over the real `app.Runtime` and checking via the public getter.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -count=1 ./internal/acp/ -run 'TestSessionLifecycleValidation|TestSessionPlumbsAdditionalDirectories' -v`
Expected: FAIL — the old "non-empty additional directories" cases still reject; the new cases need the cap; `TestSessionPlumbsAdditionalDirectories` needs the option forwarded.

- [ ] **Step 3: Lift the rejection, add the cap, plumb the option**

In `internal/acp/session.go`:
- In `validateLifecycleParams`, remove the `if len(p.AdditionalDirectories) > 0 { ... not yet supported ... }` block and replace with:

```go
if len(p.AdditionalDirectories) > 8 {
    return invalidParamsError("additionalDirectories accepts at most 8 entries")
}
for _, d := range p.AdditionalDirectories {
    if !filepath.IsAbs(d) {
        return invalidParamsError("additionalDirectories entries must be absolute paths")
    }
}
```

- In `Create`, after building `opts`, append:

```go
if len(p.AdditionalDirectories) > 0 {
    opts = append(opts, app.WithAdditionalDirectories(p.AdditionalDirectories))
}
```

- Do the same in `Load` and `Resume`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -count=1 ./internal/acp/ -run 'TestSessionLifecycleValidation|TestSessionPlumbsAdditionalDirectories' -v`
Expected: PASS.

- [ ] **Step 5: Vet and format**

Run: `go vet ./internal/acp/ && gofmt -w internal/acp/session.go internal/acp/session_test.go`
Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add internal/acp/session.go internal/acp/session_test.go
git commit -m "feat(acp): accept and forward additionalDirectories to runtime"
```

---

## Task 3: Advertise the capability

**Files:**
- Modify: `internal/acp/run.go`
- Modify: `internal/acp/run_test.go`

- [ ] **Step 1: Update the capability map**

In `internal/acp/run.go`, extend `sessionCapabilities` in the `initialize` handler so it reads:

```go
"sessionCapabilities": map[string]any{
    "close":               map[string]any{},
    "list":                map[string]any{},
    "resume":              map[string]any{},
    "additionalDirectories": map[string]any{},
},
```

- [ ] **Step 2: Update capability assertions**

In `internal/acp/run_test.go` `TestRunInitializeCapabilities`'s "basic capabilities" subtest:
- Remove `"additionalDirectories"` from the `forbidden` slice. The new forbidden slice is: `{"image", "audio", "embeddedContext", "delete", "mcp"}`.
- Add a positive assertion mirroring the list/resume ones:

```go
// sessionCapabilities.additionalDirectories is an empty object.
adCap, ok := sessionCaps["additionalDirectories"]
if !ok {
    t.Fatalf("sessionCapabilities.additionalDirectories missing")
}
adObj, ok := adCap.(map[string]any)
if !ok {
    t.Fatalf("sessionCapabilities.additionalDirectories is not an object: %T", adCap)
}
if len(adObj) != 0 {
    t.Fatalf("sessionCapabilities.additionalDirectories = %v, want empty object", adObj)
}
```

- [ ] **Step 3: Run test to verify it passes**

Run: `go test -count=1 ./internal/acp/ -run TestRunInitializeCapabilities -v`
Expected: PASS.

- [ ] **Step 4: Vet and format**

Run: `go vet ./internal/acp/ && gofmt -w internal/acp/run.go internal/acp/run_test.go`
Expected: no issues.

- [ ] **Step 5: Commit**

```bash
git add internal/acp/run.go internal/acp/run_test.go
git commit -m "feat(acp): advertise sessionCapabilities.additionalDirectories"
```

---

## Task 4: Docs + audit log

**Files:**
- Modify: `docs/10-acp.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Update the supported-methods table**

In `docs/10-acp.md`, in the **Supported methods** table, after the `session/resume` row, add:

```markdown
| `session/create` (additionalDirectories) | Full | The `additionalDirectories` array on `session/create`, `session/load`, and `session/resume` is now accepted and forwarded to the runtime as extra workspace roots. Capped at 8 entries; each must be an absolute path. `initialize` advertises `sessionCapabilities.additionalDirectories: {}`. |
```

- [ ] **Step 2: Update the unsupported-features table**

In `docs/10-acp.md`, remove the row mentioning `additionalDirectories` from the **Unsupported features** table (or update it to reflect the new behavior — only `session/delete` remains unsupported).

- [ ] **Step 3: Update the advertised-capabilities section**

In the `### Advertised capabilities` subsection (added in the prior list/resume batch), extend the line to include `additionalDirectories`:

```markdown
`initialize` reports `agentCapabilities.loadSession: true` and
`sessionCapabilities: { close, list, resume, additionalDirectories }`
(each an empty object). No other lifecycle, content, or MCP capabilities
are advertised.
```

- [ ] **Step 4: Add the audit-doc batch section**

In `docs/13-project-audit-2026-07-11.md`, append a new section mirroring the prior batch sections:

```markdown
## Implementation batch — ACP additional directories

The remaining ACP session-capability gap (additional directories) was
addressed by the following commits on branch
`feature/acp-additional-directories`:

```
<commit> feat(app): add WithAdditionalDirectories option to headless runtime
<commit> feat(acp): accept and forward additionalDirectories to runtime
<commit> feat(acp): advertise sessionCapabilities.additionalDirectories
```

### Newly supported parameter

- **`additionalDirectories` on `session/create`, `session/load`, and `session/resume`** — a list of up to 8 absolute paths. Each is forwarded to the runtime as an extra workspace root; the shell sandbox's `restricted` backend adds them to the allowed-cwd list. The primary `WithWorkingDir` remains the sandbox root.

### Unadvertised capabilities remain unadvertised

`initialize` continues to omit `delete`, `mcp`/`mcpCapabilities`, image,
audio, and embedded-context content blocks. The advertised lifecycle set
is now `sessionCapabilities: { close, list, resume,
additionalDirectories }`, each as an empty object.
```

Use the real commit hashes after the batch lands.

- [ ] **Step 5: Commit**

```bash
git add docs/10-acp.md docs/13-project-audit-2026-07-11.md
git commit -m "docs(acp): document additionalDirectories support"
```

---

## Batch closeout

After Task 4, run the full verification gates:

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

Update the `## Dated resolution note` section of `docs/13-project-audit-2026-07-11.md` with a one-paragraph entry mirroring the prior batches, citing the actual commit range and branch.

---

## Self-Review

**Spec coverage:**
- A new `app.Option` `WithAdditionalDirectories` lands in `internal/app/runtime.go`; the runtime stores the list and exposes it via `Runtime.AdditionalDirectories()`.
- The shell sandbox's `restricted` backend (and only that one) consumes the list to extend the allowed-cwd surface for tool execution. `container` and `passthrough` backends ignore the list (the container backend enforces its own mount, and `passthrough` has no cwd boundary).
- `validateLifecycleParams` enforces `len <= 8` and absolute paths.
- `Create`/`Load`/`Resume` all forward the list via the new option.
- `initialize` advertises `sessionCapabilities.additionalDirectories: {}`.
- Docs and audit log updated with the real commit hashes.

**Type consistency:**
- `WithAdditionalDirectories` is a pure function option; no shared mutable state outside the `Runtime.additionalDirs` slice.
- `AdditionalDirectories()` returns the stored slice; callers must not mutate it.

**Placeholder scan:** No TBDs. The sandbox-wiring step (Task 1) requires the implementer to locate the existing allowed-cwd construction site in the `restricted` backend; if it isn't there, the implementer should ASK rather than improvise a new boundary.
