# Domain D6 — Slash Command & Export Fixups Implementation Plan

> **For agentic workers:** Execute this plan task-by-task in the
> `.worktrees/domain-d-agent-runtime` worktree. Steps use checkbox
> (`- [ ]`) syntax.

**Goal:** Resolve five findings from `docs/14-codebase-improvement-audit-2026-07-14.md` (Domain D):
F-POL-91 (skill Risk default), F-POL-84 (slash command stubs), F-BUG-78
(/diff state injection), F-BUG-72 (/export path order), plus a D5
followup (stale comment in handoff_test.go:67).

**Architecture:** Simple, isolated changes. Each task modifies at most
two files plus their tests. No new packages or dependencies.

**Tech Stack:** Go 1.22+, stdlib plus existing project imports.

---

## Global Constraints

- Every code change MUST compile: run `go build ./...` after each task.
- Every test change MUST pass: run `go test ./internal/commands/...` and
  `go test ./internal/skills/...` after each task's test step.
- At the end, `go test ./...` must pass.
- Commit per task with the exact message shown.
- Preserve existing public function signatures.

---

## File Structure

Files modified by this plan:

- `internal/agent/handoff_test.go` — Task 0 (comment fix)
- `internal/skills/skill.go` — Task 1 (use registry constant)
- `internal/skills/skill_test.go` — Task 1 (add test)
- `internal/commands/commands.go` — Tasks 2, 3, 4
- `internal/commands/commands_test.go` — Tasks 2, 3, 4 (add tests)

---

### Task 0: D5 followup — stale comment in handoff_test.go:67

**Files:**
- Modify: `internal/agent/handoff_test.go` line 67

**Problem:** Line 67 says "empty summary must return an error so the
caller can fall back to compactMessages". After D5/F-POL-87, the caller
no longer falls back to `compactMessages`. Instead it terminates the
turn.

- [ ] **Step 1: Update the comment**

In `internal/agent/handoff_test.go`, replace line 67:

```go
t.Fatal("empty summary must return an error so the caller can fall back to compactMessages")
```

with:

```go
t.Fatal("empty summary must return an error so the caller can terminate the turn (see runner.go summarizeAndContinue path)")
```

- [ ] **Step 2: Verify build and surrounding tests pass**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run 'TestSummarizeAndContinueErrorsOnEmptySummary' -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/handoff_test.go
git commit -m "docs(agent): update stale comment in summarizeAndContinue test (D5 followup)"
```

---

### Task 1: F-POL-91 — Skills Risk default is a bare string literal

**Files:**
- Modify: `internal/skills/skill.go` line 93
- Add test: `internal/skills/skill_test.go`

**Problem:** `fm.Risk = "read_only"` is a string literal. The rest of
the codebase uses `registry.RiskReadOnly` as the canonical value.

**Fix:** Import `marshal/internal/tools/registry` and use
`string(registry.RiskReadOnly)`. The `frontmatter.Risk` field is
`string` and `registry.RiskReadOnly` is `registry.RiskLevel` (= `string`
underlying), so an explicit cast is needed.

**No circular dependency:** `internal/skills` does not currently import
`internal/tools/registry` and `internal/tools/registry` imports stdlib
+ `marshal/internal/app/session`. Adding the import is safe.

- [ ] **Step 1: Update the assignment**

In `internal/skills/skill.go`:

1. Add `"marshal/internal/tools/registry"` to imports.
2. Replace `fm.Risk = "read_only"` on line 93 with:
   ```go
   fm.Risk = string(registry.RiskReadOnly)
   ```

- [ ] **Step 2: Add a test that verifies the default matches the registry constant**

Append to `internal/skills/skill_test.go`:

```go
func TestParseFrontmatterDefaultRiskMatchesRegistryConstant(t *testing.T) {
	raw := `+++
name = "no-risk-skill"
description = "A skill without explicit risk"
+++

Body.
`
	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if skill.Risk != string(registry.RiskReadOnly) {
		t.Fatalf("Risk = %q, want %q (registry.RiskReadOnly)", skill.Risk, string(registry.RiskReadOnly))
	}
}
```

Add `"marshal/internal/tools/registry"` to the test imports.

- [ ] **Step 3: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/skills -v`
Expected: all tests PASS, including the new one.

- [ ] **Step 4: Commit**

```bash
git add internal/skills/skill.go internal/skills/skill_test.go
git commit -m "fix(skills): use registry.RiskReadOnly constant instead of bare string (F-POL-91)"
```

---

### Task 2: F-POL-84 — Slash command stubs advertised in /help

**Files:**
- Modify: `internal/commands/commands.go` lines 144-225 (stub commands)
- Add test: `internal/commands/commands_test.go`

**Problem:** `/stop`, `/swarm`, `/sdd`, `/model`, `/settings`,
`/memory`, `/ask`, `/edit`, `/auto`, `/mode` have handlers that return
a hard-coded string and do nothing else. Several have non-trivial
descriptions listed in `/help`.

**Fix strategy:** Minimal change — hide unimplemented commands from
`/help` by splitting the registration into two lists:

1. `implementedCommands` — all commands with real implementations
   (registered normally, shown in `/help`).
2. `unimplementedCommands` — stub commands that work (return a message
   or empty string) but are NOT registered. They can still be invoked
   by typing the name (the TUI looks up by name) but won't appear in
   `/help`.

**Implementation detail:** However, looking at the TUI code, commands
are looked up via `cmdReg.Lookup(name)`. If we don't register
unimplemented commands, they won't be found and the TUI treats the
input as a normal chat message. That's actually fine — the user can
still type `/help` to see what's available, and typing an unknown
command just sends it as a chat message.

But the instructions say "don't remove them entirely — just exclude
from the `/help` listing." So we need them registered but not listed.
The cleanest approach:

1. Add a `Hidden` field to `Command` struct.
2. In `List()`, skip hidden commands.
3. Mark the stub commands with `Hidden: true`.
4. The `/help` handler uses `List()` so it won't show them.
5. The `/stop` handler should still return `""` (the TUI special-cases
   `/stop` to cancel the turn after the handler runs — see TUI lines
   1634-1638).

**Alternative:** Add a separate `unimplementedCommands` variable and
register them separately, but mark hidden. This is the cleanest.

Actually, wait — let me reconsider. The simplest approach that matches
the audit's recommended fix ("hide unimplemented commands from `/help`"):

1. Add a `Hidden bool` field to `Command`.
2. Make `List()` filter out hidden commands.
3. Set `Hidden: true` on the stubs.
4. Keep `/stop` registered (it works via TUI special case returning "").
5. Keep `/mode` (the user can type it, it returns "", no special behavior).

The audit specifically says `/stop` is a stub but the TUI has special
handling for it (lines 1634-1638 in model.go: `case "stop":`). So
`/stop` SHOULD remain registered and return `""`.

For `/ask`, `/edit`, `/auto` — they should also remain registered since
the existing test `TestModeSwitchCommands` checks for them. The audit
says they print "Switched to X mode" but no mode state is changed —
hiding them from help reduces user confusion while preserving
functionality.

For `/swarm` and `/sdd` — these are also handled by the TUI in
special-case switch branches (since they open pickers). The test
`TestRegisterAllIncludesSwarmCommand` checks for them. Keep them
registered but hidden from help.

For `/model`, `/settings`, `/memory` — stubs. `/settings` and `/memory`
are handled by the TUI in special-case branches (lines 1614-1632).
`/model` has no special handling — it's a pure stub.

- [ ] **Step 1: Add `Hidden` field to `Command`**

In `internal/commands/types.go`, add `Hidden bool` to the `Command`
struct after the `Args` field.

- [ ] **Step 2: Update `List()` to skip hidden commands**

In `internal/commands/types.go`, update `List()`:

```go
func (r *Registry) List() []Command {
	cmds := make([]Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		cmds = append(cmds, cmd)
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}
```

- [ ] **Step 3: Mark stub commands as Hidden**

In `internal/commands/commands.go`, set `Hidden: true` for these
commands:
- `stop` (line 144-148)
- `ask` (lines 149-155)
- `edit` (lines 156-162)
- `auto` (lines 163-169)
- `mode` (lines 170-175)
- `swarm` (lines 176-181)
- `sdd` (lines 182-187)
- `connect` (lines 188-192)
- `models` (lines 193-197)
- `model` (lines 198-203)
- `settings` (lines 216-220)
- `memory` (lines 221-225)

Do NOT mark `export`, `diff`, `rollback`, `undo`, `redo`, `rename`,
`rewind`, `branches`, `config`, `log`, `context`, `route`, `tools`,
`help`, `new`, `clear`, `exit`, `quit`, `trust` — these have real
implementations.

- [ ] **Step 4: Add tests**

Add the following tests to `internal/commands/commands_test.go`:

```go
func TestHelpHidesUnimplementedCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, ok := cmdReg.Lookup("help")
	if !ok {
		t.Fatal("help command not registered")
	}
	result := cmd.Handler(newTestState(), nil)

	// Unimplemented commands must NOT appear in /help output.
	for _, name := range []string{"swarm", "sdd", "settings", "memory", "connect", "models"} {
		if strings.Contains(result, "/"+name) {
			t.Errorf("help output should not contain /%s, got:\n%s", name, result)
		}
	}

	// Mode commands should still be listed in /help since they say
	// "Switched to X mode" — the audit says "or implement". For this
	// batch we do NOT require mode implementation, but we DO hide
	// them. Verify they're hidden.
	for _, name := range []string{"ask", "edit", "auto", "mode"} {
		if strings.Contains(result, "/"+name) {
			t.Errorf("help output should not contain /%s, got:\n%s", name, result)
		}
	}

	// Implemented commands must still appear.
	for _, name := range []string{"help", "new", "config", "route", "context", "log", "diff", "rollback", "undo", "redo", "export", "rename", "rewind", "branches", "trust", "tools", "exit", "quit", "clear"} {
		if !strings.Contains(result, "/"+name) {
			t.Errorf("help output should contain /%s, got:\n%s", name, result)
		}
	}
}
```

```go
func TestHiddenCommandsStillRunnable(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	// Hidden commands must still be findable via Lookup.
	for _, name := range []string{"stop", "ask", "edit", "auto", "mode", "swarm", "sdd", "settings", "memory", "model", "connect", "models"} {
		_, ok := cmdReg.Lookup(name)
		if !ok {
			t.Errorf("hidden command /%s must still be registered for Lookup", name)
		}
	}
}
```

- [ ] **Step 5: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/commands -v`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/commands/types.go internal/commands/commands.go internal/commands/commands_test.go
git commit -m "fix(commands): hide unimplemented stub commands from /help (F-POL-84)"
```

---

### Task 3: F-BUG-78 — /diff adds diff to state as system message

**Files:**
- Modify: `internal/commands/commands.go` lines 312-313
- Add test: `internal/commands/commands_test.go`

**Problem:** `/diff` calls `state.AddMessage(session.RoleSystem, diff,
session.ContentTypeDiff)` then returns `""`. The TUI renders the diff
as a system event. While `buildHistoryMessages` does exclude
`RoleSystem` messages from model history on the next turn, the explicit
`AddMessage` is unnecessary and the empty return is an anti-pattern:
the diff is consumed as a side effect and invisible to the handler
caller. The audit wants the handler to communicate via its return
value.

**Fix:** Remove the `state.AddMessage(...)` call and return `diff`
instead. The TUI caller (model.go:1604-1608) will add it as a system
message with `ContentTypePlain`. This means:
- The diff is still visible in the transcript.
- No state-injection side-effect in the command handler.
- The TUI caller uses its standard message-addding path.

- [ ] **Step 1: Modify the /diff handler**

In `internal/commands/commands.go`, replace lines 312-313:

```go
				state.AddMessage(session.RoleSystem, diff, session.ContentTypeDiff)
				return ""
```

with:

```go
				return diff
```

- [ ] **Step 2: Add a test**

Append to `internal/commands/commands_test.go`:

```go
func TestDiffDoesNotInjectIntoState(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, ok := cmdReg.Lookup("diff")
	if !ok {
		t.Fatal("diff command not registered")
	}
	// Without snapshotter/DB, the diff handler returns a friendly
	// message. Verify the state has no ContentTypeDiff messages.
	before := len(state.Messages())
	result := cmd.Handler(state, nil)
	after := len(state.Messages())

	if after != before {
		t.Errorf("expected no new messages in state after /diff, got %d -> %d", before, after)
	}
	if result == "" {
		t.Error("/diff should return a message, not empty string")
	}
	// No message in state should have ContentTypeDiff.
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeDiff {
			t.Errorf("state should not contain ContentTypeDiff messages: %q", m.Content)
		}
	}
}
```

- [ ] **Step 3: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/commands -v`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/commands/commands.go internal/commands/commands_test.go
git commit -m "fix(commands): return diff string instead of injecting into state (F-BUG-78)"
```

---

### Task 4: F-BUG-72 — /export calls export.Write before computing default path

**Files:**
- Modify: `internal/commands/commands.go` lines 416-424
- Add test: `internal/commands/commands_test.go`

**Problem:** When `args` is empty, `path` is `""`. The code at line 418
passes the empty path to `export.Write(state, path, redactOn)` and
*afterwards* computes the default filename (lines 421-423).
`export.Write` already handles empty path (it defaults to
`marshal-session-<ID>.html` in `WorkingDir`), but the caller's display
string is computed redundantly. The fix is to compute the default path
first, then call `export.Write` once.

- [ ] **Step 1: Modify the /export handler**

In `internal/commands/commands.go`, replace lines 416-425:

```go
			path := strings.Join(args, " ")
			redactOn := state.Config.Privacy.RedactSecrets
			if err := export.Write(state, path, redactOn); err != nil {
				return fmt.Sprintf("Export failed: %v", err)
			}
			if path == "" {
				path = filepath.Join(state.WorkingDir, "marshal-session-"+state.SessionID()+".html")
			}
			return "Exported to " + path
```

with:

```go
			path := strings.Join(args, " ")
			if path == "" {
				path = filepath.Join(state.WorkingDir, "marshal-session-"+state.SessionID()+".html")
			}
			redactOn := state.Config.Privacy.RedactSecrets
			if err := export.Write(state, path, redactOn); err != nil {
				return fmt.Sprintf("Export failed: %v", err)
			}
			return "Exported to " + path
```

Note: `filepath` is already imported.

- [ ] **Step 2: Add a test**

Append to `internal/commands/commands_test.go`:

```go
func TestExportComputesPathBeforeWrite(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	// Override WorkingDir to a temp dir so we don't write into the real
	// working directory.
	tmpDir := t.TempDir()
	state.WorkingDir = tmpDir

	cmd, ok := cmdReg.Lookup("export")
	if !ok {
		t.Fatal("export command not registered")
	}

	// Call /export with no args. The handler should compute a default
	// path and call export.Write with it.
	result := cmd.Handler(state, nil)

	if !strings.Contains(result, "Exported to ") {
		t.Fatalf("export output missing 'Exported to': %q", result)
	}
	if !strings.Contains(result, tmpDir) {
		t.Fatalf("export output should contain working dir: %q", result)
	}
	if strings.Contains(result, "failed") {
		t.Fatalf("export failed unexpectedly: %q", result)
	}

	// Verify the file was actually created at the default path.
	defaultPath := filepath.Join(tmpDir, "marshal-session-"+state.SessionID()+".html")
	if _, err := os.Stat(defaultPath); os.IsNotExist(err) {
		t.Fatalf("export file not found at default path: %s", defaultPath)
	}
}
```

Add `"os"` and `"path/filepath"` to the test imports if not present.

- [ ] **Step 3: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/commands -v`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/commands/commands.go internal/commands/commands_test.go
git commit -m "fix(commands): compute default export path before calling export.Write (F-BUG-72)"
```

---

## Self-Review & Verification

1. **Spec coverage:**
   - Task 0 (D5 followup): updated stale comment in handoff_test.go:67.
   - Task 1 (F-POL-91): `string(registry.RiskReadOnly)` replaces `"read_only"`.
   - Task 2 (F-POL-84): `Hidden` field excludes stubs from `List()`/`/help`.
   - Task 3 (F-BUG-78): `/diff` returns string instead of state injection.
   - Task 4 (F-BUG-72): default path computed before `export.Write` call.

2. **No TUI regression:** The `/diff` handler change (Task 3) makes the
   TUI add the diff as `ContentTypePlain` via its standard path. The
   diff is still displayed; the content type difference (`Diff` vs
   `Plain`) only affects rendering (diff-block vs markdown), not model
   context inclusion.

3. **No type mismatch:** `registry.RiskReadOnly` is `RiskLevel` (= `string`);
   explicit cast `string(registry.RiskReadOnly)` matches the `string` field.

4. **Hidden field:** Adding `Hidden bool` to `Command` is a backward-
   compatible additive change. All existing call sites that construct
   `Command` literals without `Hidden` default to `false` (visible).

- [ ] **Final verification**

Run: `CGO_ENABLED=1 go build ./... && go test ./...`
Expected: all tests PASS across the entire project.

If pass, create final commit with diff summary.
