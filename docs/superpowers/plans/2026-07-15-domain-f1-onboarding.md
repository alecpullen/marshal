# Domain F1 — Onboarding Hardening Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in a dedicated
> worktree (suggested branch `feature/domain-f1-onboarding`). Steps use
> checkbox (`- [ ]`) syntax.

**Goal:** Resolve seven findings from
`docs/14-codebase-improvement-audit-2026-07-14.md` (Domain F —
TUI/session/onboarding) that concern the first-run onboarding wizard
and the seam between onboarding completion and the main TUI:

- **F-UIUX-137** (HIGH) — raw API keys written to project-local
  `config.toml` when they lack underscores.
- **F-UIUX-138** (MEDIUM) — footer hints omit `Ctrl+G`/`Ctrl+R`/`Ctrl+X`/
  `PgUp`/`PgDn`/`Ctrl+U`/`Ctrl+D`.
- **F-BUG-154** (LOW) — `Run()` passes the same `opts` slice through
  onboarding and `StartRuntime` so `WithWorkingDir` is resolved twice.
- **F-BUG-158** (LOW) — `OnboardingModel.Update` uses a value receiver
  but the public method set returns a `tea.Model` interface; later field
  additions could silently lose state.
- **F-BUG-159** (LOW) — `@`-completion shows zero results with no
  explanation when the file index has not been populated.
- **F-BUG-161** (LOW) — `fetchOllamaModels` ignores `resp.StatusCode`;
  a 500 with a JSON body is parsed as success.
- **F-POL-168** (LOW) — onboarding always writes
  `name = "my-project"` and does not ask for one.
- **F-POL-169** (LOW) — `stateDone` check after onboarding does not
  distinguish "completed" from "cancelled".

**Architecture:** Localized changes to `internal/app/onboarding.go`,
`internal/app/app.go`, and a single helper in `internal/app/tui/`.
No new packages, no new dependencies. All edits keep the public
`OnboardingModel` API and the `Run` option list backwards compatible.

**Tech Stack:** Go 1.22+, Bubble Tea v2, `huh` v2.

---

## Global Constraints

- Every code change MUST compile: run `CGO_ENABLED=1 go build ./...`
  after each task.
- Every test change MUST pass: run
  `CGO_ENABLED=1 go test ./internal/app/...` after each task.
- At the end, `CGO_ENABLED=1 go test ./...` must pass.
- Commit per task with the exact message shown.
- Preserve the public `OnboardingModel` interface (constructor +
  `Init`/`Update`/`View`); only internal field types may change.
- API-key changes (F-UIUX-137) MUST NOT regress the existing
  `test_*.go` golden config comparisons in `internal/app/`.

---

## File Structure

Files modified by this plan:

- `internal/app/onboarding.go` — Tasks 1, 2, 3, 4, 5, 6
- `internal/app/onboarding_test.go` — Tasks 1, 2, 3, 4, 5, 6 (add
  tests; create file if absent)
- `internal/app/app.go` — Task 7
- `internal/app/app_test.go` — Task 7 (add test)
- `internal/app/tui/model.go` — Task 8 (single small helper for
  F-BUG-159)
- `internal/app/tui/model_test.go` — Task 8 (add test)

---

### Task 1: F-UIUX-137 — API key handling never persists a raw key

**Files:**
- Modify: `internal/app/onboarding.go` (the `apiKey` step in
  `viewString`/`Update` and the `saveConfig` method)
- Add tests: `internal/app/onboarding_test.go`

**Problem:** `saveConfig` (line 254–260) checks only for an underscore
in the user's input. Any key like `sk-…`, `sk-proj-…`, or a paste that
happens to lack an underscore is persisted verbatim into
`.marshal/config.toml` (often a project-tracked file). This is the
single highest-severity TUI-side finding (HIGH).

**Fix:** Make the onboarding flow **always** treat the API-key field
as a secret reference. The wizard now presents a single prompt with
two visibly distinct modes:

1. **Env-var mode (default):** user types the name of an env var
   already set in their shell (e.g. `OPENAI_API_KEY`); the wizard
   writes `api_key_env = "OPENAI_API_KEY"`.
2. **Inline mode (opt-in):** user pastes a raw key. The wizard
   displays a warning ("this value will only be written to your global
   `~/.config/marshal/config.toml`, never to a project file"), then
   writes the key there.

The mode switch is a single `huh` confirm or a dedicated step. The
project-local `.marshal/config.toml` is NEVER written for either
case — instead the wizard writes a minimal project config containing
only `[project]`, `[commands]`, `[profile]`, `[providers.*]` with
`api_key_env = "<name>"` (or no key entry if env-var mode), and the
`[models.presets.*]` block. The actual key, if provided inline, goes
to `~/.config/marshal/config.toml` under `[providers.<name>].api_key`.

**Implementation steps:**

- [ ] **Step 1: Add a `keyMode` field**

In `internal/app/onboarding.go` add to the `OnboardingModel` struct:

```go
keyMode   keyModeKind // envName | inline | empty (unset)
keySecret string      // when keyMode == inline, the value the user typed
```

with `keyModeKind` as an `int` enum and a default of `keyModeUnset`.

- [ ] **Step 2: Replace the API-key input step**

In `viewString`, replace the bare "API Key:" prompt with two
successive steps:

1. "API key source" — `huh.NewSelect[keyModeKind]` with options
   `Use env var (recommended)` and `Paste key inline (writes to
   global config)`. Default = `keyModeEnvName`.
2. Conditionally, either "Env var name (e.g. `OPENAI_API_KEY`)" or
   "Paste API key" — both use `textinput.EchoPassword` so the value
   is masked on screen (per F-SEC-29, also closed in F3 followup).

Update the corresponding `Update` arm so `m.keyMode` is set by the
select and `m.apiKey` / `m.keySecret` are set by the matching input.

- [ ] **Step 3: Rewrite `saveConfig` to honour the new flow**

Replace the substring check on `strings.Contains(m.apiKey, "_")` with
a switch on `m.keyMode`:

```go
switch m.keyMode {
case keyModeEnvName:
    tomlContent.WriteString(fmt.Sprintf("api_key_env = %q\n\n", m.apiKey))
case keyModeInline:
    // Per the warning, persist to the GLOBAL config only.
    if err := writeGlobalProviderAPIKey(providerKey, m.keySecret); err != nil {
        return err
    }
    // Project config records only the env-var-style reference,
    // pointing at the same global value via the `api_key_env` key
    // so the resolver works the same way.
    tomlContent.WriteString(fmt.Sprintf("api_key_env = %q\n\n", "MARSHAL_GLOBAL_API_KEY"))
default:
    tomlContent.WriteString("# api_key_env = \"OPENAI_API_KEY\"\n\n")
}
```

Add a small private helper `writeGlobalProviderAPIKey` that uses
`config.SaveUserConfig` to merge the key into
`~/.config/marshal/config.toml` under
`[providers.<name>].api_key`. If the global file does not exist,
create it with a minimal header before merging.

The constant `MARSHAL_GLOBAL_API_KEY` is documented in
`docs/03-config-and-policy.md` (out of scope for this plan; add a
TODO comment in the helper).

- [ ] **Step 4: Add a regression test**

In `internal/app/onboarding_test.go` (create if missing):

```go
func TestOnboardingNeverWritesRawKeyToProject(t *testing.T) {
    m := newTestOnboardingModel() // helper that builds a model with
                                  // every step pre-filled
    m.keyMode = keyModeInline
    m.keySecret = "sk-deadbeef-no-underscore"
    dir := t.TempDir()
    m.workingDir = dir

    if err := m.saveConfig(); err != nil {
        t.Fatalf("saveConfig: %v", err)
    }
    data, err := os.ReadFile(filepath.Join(dir, ".marshal", "config.toml"))
    if err != nil {
        t.Fatalf("read: %v", err)
    }
    if strings.Contains(string(data), "sk-deadbeef") {
        t.Fatalf("raw key leaked into project config: %s", data)
    }
    if !strings.Contains(string(data), `api_key_env = "MARSHAL_GLOBAL_API_KEY"`) {
        t.Fatalf("expected api_key_env reference, got: %s", data)
    }
}
```

Also add a test that verifies env-var mode produces
`api_key_env = "<name>"` and that unset mode produces a commented-out
placeholder.

- [ ] **Step 5: Verify build and tests**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/... -run 'TestOnboarding' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/onboarding.go internal/app/onboarding_test.go
git commit -m "fix(onboarding): persist API keys only as env-var or global refs (F-UIUX-137)"
```

---

### Task 2: F-UIUX-138 (onboarding side) — footer hints cover onboarding states

**Files:**
- Modify: `internal/app/tui/help/help.go` (`FooterHints` struct +
  `Footer` function)
- Add tests: `internal/app/tui/help/help_test.go`

**Problem:** The footer omits `Ctrl+G` (thinking toggle), `Ctrl+R`
(rollback), `Ctrl+X` (clear queue), `PgUp`/`PgDn`, `Ctrl+U`/`Ctrl+D`.
`Ctrl+X` IS in the busy hint, but no other state surfaces these. The
progressive-disclosure principle from the tui-design skill says the
footer should always show 3–5 *contextually relevant* shortcuts, not
the same hint on every screen.

**Fix:** Add an `Idle` boolean to `FooterHints` and surface the most
relevant extras in idle mode: `Ctrl+R` (rollback), `Ctrl+G` (thinking).
`Ctrl+X` is only relevant when the steering queue is non-empty
(future-proof: today it shows whenever busy, which is acceptable).

`PgUp`/`PgDn`/`Ctrl+U`/`Ctrl+D` belong in the help overlay (Task 9,
F2 plan), not the footer.

**Implementation steps:**

- [ ] **Step 1: Extend the hints struct**

In `internal/app/tui/help/help.go`:

```go
type FooterHints struct {
    Busy            bool
    EditingCommand  bool
    ApprovalPending bool
    QuestionPending bool
    PopupOpen       bool
    // IdleRollbackEligible is true when a backup exists and the user is idle.
    IdleRollbackEligible bool
    // ThinkingVisible reflects the current thinking-block visibility toggle.
    ThinkingVisible bool
}
```

- [ ] **Step 2: Append contextual hints in the idle branch**

In `Footer`, in the `else` (idle) branch, after the existing
4 hints, append at most two more based on the new fields:

```go
} else {
    segs = append(segs,
        pair("Tab", "mode"),
        pair("Alt+M", "model"),
        pair("/", "command"),
        pair("@", "file"),
    )
    if h.IdleRollbackEligible {
        segs = append(segs, pair("Ctrl+R", "rollback"))
    }
    if h.ThinkingVisible {
        segs = append(segs, pair("Ctrl+G", "hide thinking"))
    } else {
        segs = append(segs, pair("Ctrl+G", "show thinking"))
    }
}
```

(If both would push the footer past the available width, the caller
is expected to truncate — out of scope for this plan; a followup may
add width-aware truncation.)

- [ ] **Step 3: Add tests**

In `internal/app/tui/help/help_test.go`:

```go
func TestFooterIdleShowsThinkingToggle(t *testing.T) {
    out := Footer(FooterHints{})
    if !strings.Contains(out, "Ctrl+G") {
        t.Fatalf("idle footer missing Ctrl+G: %q", out)
    }
}

func TestFooterIdleShowsRollbackWhenEligible(t *testing.T) {
    out := Footer(FooterHints{IdleRollbackEligible: true})
    if !strings.Contains(out, "Ctrl+R") {
        t.Fatalf("idle footer missing Ctrl+R: %q", out)
    }
}
```

- [ ] **Step 4: Wire the new fields from the main Model**

In `internal/app/tui/model.go` find the call to `help.Footer(...)`
and pass the new fields:

```go
hints := help.FooterHints{
    Busy:                 m.busy,
    EditingCommand:       m.editingCommand,
    ApprovalPending:      m.state.PendingApproval() != nil,
    QuestionPending:      m.state.PendingQuestion() != nil,
    PopupOpen:            m.pickerModel != nil || m.connectOpen,
    IdleRollbackEligible: !m.busy && m.state.HasBackup(),
    ThinkingVisible:      m.thinkingVisible,
}
```

(`m.thinkingVisible` is the existing field tracked by the `Ctrl+G`
binding; confirm the name with `grep` before editing.)

- [ ] **Step 5: Verify build and tests**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui/help/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/tui/help/help.go internal/app/tui/help/help_test.go internal/app/tui/model.go
git commit -m "feat(tui): surface Ctrl+R and Ctrl+G in idle footer (F-UIUX-138)"
```

---

### Task 3: F-POL-168 — Derive project name from working dir or ask

**Files:**
- Modify: `internal/app/onboarding.go` (`saveConfig` + `Update`/`viewString`)
- Add tests: `internal/app/onboarding_test.go`

**Problem:** `saveConfig` hardcodes `name = "my-project"`.

**Fix:** Add a `stateProjectName` step after provider selection. Use
`huh.NewInput` with a default of `filepath.Base(m.workingDir)`. Trim
leading/trailing whitespace; reject empty (re-prompt). Persist the
result via `m.projectName` and use it in the generated `[project]`
header.

- [ ] **Step 1: Add the step**

In `viewString`, after the provider selection branch, render a new
state that asks for project name with a default derived from
`m.workingDir`.

- [ ] **Step 2: Update `saveConfig`**

Replace `"my-project"` with `m.projectName`. If the user provided
nothing, fall back to `filepath.Base(m.workingDir)` (mirroring the
default in the input step).

- [ ] **Step 3: Test**

```go
func TestOnboardingProjectNameFromWorkingDir(t *testing.T) {
    m := newTestOnboardingModel()
    m.workingDir = "/tmp/myrepo"
    m.projectName = "" // simulate user pressing Enter on default
    // … run saveConfig …
    data, _ := os.ReadFile(filepath.Join(m.workingDir, ".marshal", "config.toml"))
    if !strings.Contains(string(data), `name = "myrepo"`) {
        t.Fatalf("expected name derived from working dir, got: %s", data)
    }
}
```

- [ ] **Step 4: Verify build and tests**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app -run 'TestOnboarding' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/onboarding.go internal/app/onboarding_test.go
git commit -m "feat(onboarding): prompt for project name (F-POL-168)"
```

---

### Task 4: F-POL-169 — Distinguish onboarding cancelled from completed

**Files:**
- Modify: `internal/app/app.go` (the `stateDone` branch around
  `Run()` line 675–677)
- Add tests: `internal/app/app_test.go`

**Problem:** When onboarding returns, the caller treats any non-error
as "finished"; Ctrl+C cancellation looks identical in logs.

**Fix:** Define a sentinel `errOnboardingCancelled = errors.New(
"onboarding cancelled by user")` in `internal/app/app.go`. Have the
onboarding controller return this sentinel when the user presses
Esc / Ctrl+C from a step that is not the final submit. The caller
in `app.Run` logs an info message ("onboarding cancelled, starting
with default config") instead of treating it as a fatal error.

- [ ] **Step 1: Define the sentinel**

In `internal/app/app.go`, near the top:

```go
var errOnboardingCancelled = errors.New("onboarding cancelled")
```

- [ ] **Step 2: Return it from onboarding**

In `internal/app/onboarding.go` find the Esc / Ctrl+C handling
inside `Update` and have it return `(m, tea.Quit)` plus signal a
cancelled state via a public `Cancelled() bool` accessor. The caller
in `app.Run` checks this accessor and returns `errOnboardingCancelled`
when true.

- [ ] **Step 3: Handle it in `app.Run`**

```go
if errors.Is(err, errOnboardingCancelled) {
    slog.Default().Info("onboarding cancelled; using default config")
    // fall through to runtime start
} else if err != nil {
    return err
}
```

- [ ] **Step 4: Test**

```go
func TestRunDistinguishesOnboardingCancelled(t *testing.T) {
    // Use the existing WithConfigLoader / WithProgramRunner test seams
    // to inject a fake ProgramRunner that emits a Ctrl+C keypress into
    // the onboarding controller, then asserts that Run returns nil
    // (not an error) and that the config loader was NOT called.
}
```

- [ ] **Step 5: Verify**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app -run 'TestRun' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/app/app_test.go internal/app/onboarding.go
git commit -m "fix(app): distinguish onboarding cancelled from completed (F-POL-169)"
```

---

### Task 5: F-BUG-161 — `fetchOllamaModels` checks status code

**Files:**
- Modify: `internal/app/onboarding.go` (`fetchOllamaModels`)
- Add tests: `internal/app/onboarding_test.go`

**Problem:** HTTP body is parsed without verifying
`resp.StatusCode == http.StatusOK`. A 500 with a JSON body is treated
as success.

**Fix:** Check the status code immediately after `http.DefaultClient.Do`;
return an explicit error otherwise.

- [ ] **Step 1: Add the check**

```go
if resp.StatusCode != http.StatusOK {
    body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
    resp.Body.Close()
    return nil, fmt.Errorf("ollama /api/tags returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
```

- [ ] **Step 2: Test**

Use `httptest.NewServer` returning a 500 with a JSON body. Assert
that `fetchOllamaModels` returns a non-nil error and a nil slice.

- [ ] **Step 3: Verify and commit**

```bash
git add internal/app/onboarding.go internal/app/onboarding_test.go
git commit -m "fix(onboarding): check ollama HTTP status code (F-BUG-161)"
```

---

### Task 6: F-BUG-158 — `OnboardingModel` uses pointer receivers

**Files:**
- Modify: `internal/app/onboarding.go` (every method on
  `OnboardingModel`)
- Add tests: `internal/app/onboarding_test.go`

**Problem:** `Update` returns `(tea.Model, tea.Cmd)` while the
receiver is a value. Mutating shared fields works today only because
every field is a reference type; a future plain field would silently
disappear.

**Fix:** Change every method receiver to a pointer (`*OnboardingModel`).
The returned interface is unchanged.

- [ ] **Step 1: Convert receivers**

Run:

```bash
grep -n "func (m OnboardingModel)" internal/app/onboarding.go
```

Convert each to `func (m *OnboardingModel)`. Re-run `go build` to
catch call sites.

- [ ] **Step 2: Test**

A test that mutates a plain (int) field via a `tea.KeyPressMsg`
handler and asserts the change persists across calls confirms
pointer semantics. (Add an `attempts` int counter for the test.)

- [ ] **Step 3: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app -v
git add internal/app/onboarding.go internal/app/onboarding_test.go
git commit -m "refactor(onboarding): use pointer receivers consistently (F-BUG-158)"
```

---

### Task 7: F-BUG-154 — `Run()` resolves options once

**Files:**
- Modify: `internal/app/app.go` (`Run`)
- Add tests: `internal/app/app_test.go`

**Problem:** The `opts` slice is iterated in `Run` and again in
`StartRuntime`. `WithWorkingDir` is resolved twice and could drift if
its factory is non-idempotent.

**Fix:** Materialise `opts` into a single `runOptions` struct inside
`Run` before the onboarding/runtime phase; pass the struct to both
phases.

- [ ] **Step 1: Define `runOptions`**

```go
type runOptions struct {
    workingDir     string
    configLoader   config.Loader
    programRunner  func(*tea.Program) error
    now            func() time.Time
}
```

- [ ] **Step 2: Apply each option once**

In `Run`, iterate `opts` into `runOptions` immediately after the
empty-input check.

- [ ] **Step 3: Pass `runOptions` to onboarding and runtime**

Replace the two iteration loops with single calls passing
`runOptions` (or selected fields) as parameters.

- [ ] **Step 4: Test**

```go
func TestRunResolvesOptionsOnce(t *testing.T) {
    calls := 0
    wd := func(string) (string, error) { calls++; return t.TempDir(), nil }
    // Use WithWorkingDir(wd) and assert calls == 1 after Run returns.
}
```

- [ ] **Step 5: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app -v
git add internal/app/app.go internal/app/app_test.go
git commit -m "refactor(app): resolve Run() options once (F-BUG-154)"
```

---

### Task 8: F-BUG-159 — Empty `@` completion explains itself

**Files:**
- Modify: `internal/app/tui/model.go` (the completion-popup path for
  `@`)
- Add tests: `internal/app/tui/model_test.go`

**Problem:** When the file index is empty, `@<tab>` shows an empty
popup with no explanation. Users think the feature is broken.

**Fix:** In the path that builds `@`-completion rows, after the
index lookup returns zero results, append a single disabled row
labelled "No indexed files — run `/index`" (or "Indexing in
progress…"). Use the existing `m.completionIndex` mechanism (or
whichever field the popup consumes) and ensure the disabled flag
prevents selection.

- [ ] **Step 1: Locate the builder**

In `internal/app/tui/model.go`, find the function that returns the
candidate list for the `@` trigger (search for the string `@` in
`completions.go` or `model.go`). Confirm the call site and the
popup's disabled-row convention.

- [ ] **Step 2: Append the placeholder row**

```go
if len(candidates) == 0 {
    candidates = []completionCandidate{{
        Display:    "No indexed files — run /index",
        Value:      "",
        Disabled:   true,
        Completion: "",
    }}
}
```

(The exact field names may differ; mirror the existing candidate
type.)

- [ ] **Step 3: Test**

A test that injects a `Runner` whose file index returns an empty
slice, then triggers `@` completion and asserts the placeholder row
is present.

- [ ] **Step 4: Verify and commit**

```bash
CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/tui -run 'TestCompletion' -v
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): explain empty @ completion (F-BUG-159)"
```

---

## Final verification

Run the full test suite plus a manual smoke test of the onboarding
wizard:

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
```

Manual smoke checklist:

- [ ] First-run onboarding prompts for API key source, defaults to
      "Use env var", and never writes a raw key into
      `.marshal/config.toml` even when the user picks "Paste inline".
- [ ] Esc / Ctrl+C during onboarding returns to the TUI with the
      default config and a friendly log line.
- [ ] `@<tab>` with no index shows the placeholder row.
- [ ] Footer shows `Ctrl+G` and (when eligible) `Ctrl+R` in idle.

Update `docs/14-codebase-improvement-audit-2026-07-14.md` with the
new resolution table entries (Batches F1–F6) in the same style as
the existing batches.
