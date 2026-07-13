# F20 Hooks Flakiness Investigation and Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Investigate and fix the intermittent failures in `internal/hooks/runner_test.go` (the `TestPreToolUse*` tests) that the prior `feature/tui-themes` audit flagged as flaky. The current best evidence is: the tests pass 3-in-a-row in clean runs but occasionally fail under `go test ./...` with the full suite. This plan diagnoses the root cause, applies a targeted fix, and pins the tests to be reliable.

**Architecture:** A focused investigation-then-fix. The plan reads the hooks runner, identifies the race/ordering concern in `runHook` (the `printf` flush, the pipe close, and the `cmd.Wait` path), and applies a minimal change to the runner that stabilizes the test surface without changing the user-facing hook contract.

**Tech Stack:** Go 1.26.1, `os/exec` (already in use). No new external dependencies.

**Assumes Milestone R is complete** (it is). The `internal/hooks` package is documented as F20 in `docs/12`.

## Global Constraints

- **The user-facing hook contract is unchanged:** the JSON payload format, the `decision`/`reason`/`rewrite` schema, the `[hooks.entries]` TOML shape — all stay identical.
- **No new external dependencies.**
- **No comments unless asked.** Match existing gofmt style.
- **The fix must be minimal:** the smallest change that eliminates the flakiness. Do not refactor the runner beyond what the fix requires.
- **Verification:** add a stress test that runs the suspect test 100x (or 50x) and asserts all pass. This both reproduces and pins the fix.

## File Structure

**Modify:**
- `internal/hooks/runner.go` — the targeted fix (see Tasks 2-3).
- `internal/hooks/runner_test.go` — add a stress test.

**Add (no new package files):**
- A `runHookStdinSync` helper if the fix requires it (in `runner.go`).

## Investigation Notes (read first)

The likely flakiness sources in `runHook` (`internal/hooks/runner.go:156-188`):

1. **`printf` without a trailing newline.** The test scripts use `printf '%s' '<json>'` (line 14, 30, 65 of `runner_test.go`). `printf` flushes on exit, but on some shells (especially macOS's `bash` when invoked via `sh -c`) the pipe inheritance can cause the read side to see partial output if the process is killed early. The test relies on `bytes.TrimSpace` to handle this, but a process that exits with a non-zero status before flushing produces empty stdout.

2. **`json.NewEncoder(stdin).Encode(payload)` followed by `_ = stdin.Close()` followed by `cmd.Wait()`.** The order matters: the runner writes the JSON to the hook's stdin, then closes stdin, then waits. If the hook reads stdin in a loop (e.g., `while read line; do ... done`), it sees the encoded JSON once. If the hook doesn't read stdin (the test scripts don't), stdin can be closed before the hook starts — that's fine for the test.

3. **`cmd.Wait()` is called after `stdin.Close()`.** If the hook writes a lot to stdout/stderr, the pipe buffers fill, and the hook blocks on `write()`. The `limitedBuffer` caps at 1 MiB, but the write syscall on a full pipe still blocks. A short hook (the test's `printf` script) shouldn't hit this.

The most likely real flakiness cause: **the `printf '%s'` test scripts produce no trailing newline AND the runner reads via `cmd.Stdout = stdout` which is a `*limitedBuffer` (a `bytes.Buffer` wrapper).** If the process exits before the pipe is fully drained, `bytes.Buffer.Bytes()` returns whatever was written. On some systems (older Linux, macOS under load), the buffer may be empty at the moment `Wait()` returns even though the process wrote data.

The minimal fix: **in the test scripts, change `printf '%s'` to `echo -n`** OR add an explicit `sleep 0` after the printf to give the pipe time to drain. The first option (`echo -n`) is the more idiomatic fix because `echo` is guaranteed to flush in POSIX shells.

A more robust fix in the runner: **read the stdout pipe via `io.ReadAll` after `cmd.Wait()` to ensure all data is captured.** This is the standard Go pattern. The current code uses `cmd.Stdout = stdout` (a `*limitedBuffer`) and calls `cmd.Wait()` — `Wait` does NOT drain remaining output. The fix: use `cmd.StdoutPipe()` and explicitly read it before/after `Wait()`.

After analysis, the plan's chosen fix is **fix the test scripts** (the test artifact, not the runner). The runner is correct for the hooks spec; the test scripts were the fragile part. A secondary fix in the runner (use `StdoutPipe` + `io.ReadAll`) is also added for defensiveness.

---

## Task 1: Reproduce the flakiness

**Files:**
- Test only (no production code).

- [ ] **Step 1: Add a stress test that runs the suspect test 100x**

In `internal/hooks/runner_test.go`, add:

```go
func TestPreToolUseStable(t *testing.T) {
    for i := 0; i < 100; i++ {
        dir := t.TempDir()
        script := filepath.Join(dir, "hook.sh")
        if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"decision\":\"block\",\"reason\":\"no patches\"}'\n"), 0755); err != nil {
            t.Fatal(err)
        }
        r := NewRunner(Config{Entries: []HookEntry{{Event: EventPreToolUse, Matcher: "file.write_patch", Command: script, TimeoutMS: 5000}}})
        out, err := r.RunPreToolUse(context.Background(), PreToolUseInput{ToolName: "file.write_patch", Args: json.RawMessage(`{"patch":"x"}`)})
        if err != nil {
            t.Fatalf("iter %d: RunPreToolUse() error = %v", i, err)
        }
        if out.Decision != DecisionBlock || out.Reason != "no patches" {
            t.Fatalf("iter %d: out = %+v", i, out)
        }
    }
}
```

- [ ] **Step 2: Run the stress test 3-5 times**

Run: `go test -count=5 ./internal/hooks/ -run TestPreToolUseStable -v 2>&1 | tail -50`

Expected: at least one of the 5 runs may flake (fails with empty stdout or unexpected decision). This confirms the flakiness is reproducible.

- [ ] **Step 3: If the stress test passes 5x, the flakiness is environment-dependent**

If the stress test passes consistently, the flakiness may be a timing-sensitive interaction with the wider test suite. In that case, the fix is still warranted (defensive), and Task 2-3 proceed.

- [ ] **Step 4: Commit the stress test as a baseline (DO NOT commit yet — keep it in the working tree)**

Don't commit yet. The stress test becomes a regression sentinel after the fix.

---

## Task 2: Fix the test scripts (primary fix)

**Files:**
- Modify: `internal/hooks/runner_test.go` (only the 3 test scripts at lines 14, 30, 65)

- [ ] **Step 1: Read the test scripts**

Read `internal/hooks/runner_test.go` and locate the 3 `printf '%s'` invocations in the test scripts (lines 14, 30, 65).

- [ ] **Step 2: Replace `printf '%s'` with `printf '%s\n'`**

This adds an explicit trailing newline. POSIX `printf` is guaranteed to flush the output on exit regardless of newline presence, but the explicit newline removes any ambiguity for downstream `bytes.TrimSpace` behavior.

For each of the 3 test scripts:
- `printf '%s' '{\"decision\":\"block\",...}` → `printf '%s\\n' '{\"decision\":\"block\",...}`
- `printf '%s' '{\"rewrite\":...}` → `printf '%s\\n' '{\"rewrite\":...}`
- `printf '%s' '{\"decision\":\"block\",\"reason\":\"secret visible\"}'` → `printf '%s\\n' '{\"decision\":\"block\",\"reason\":\"secret visible\"}'`

The `\\n` is an escape inside a Go string literal; the shell sees `\n` and outputs a literal newline.

- [ ] **Step 3: Run the stress test 10x to confirm fix**

Run: `go test -count=10 ./internal/hooks/ -run TestPreToolUseStable 2>&1 | tail -30`
Expected: 10/10 PASS.

- [ ] **Step 4: Run the existing tests**

Run: `go test -count=1 ./internal/hooks/`
Expected: all tests pass.

- [ ] **Step 5: Commit the fix**

```bash
git add internal/hooks/runner_test.go
git commit -m "fix(hooks): add trailing newline to test script printf"
```

---

## Task 3: Defensive fix in the runner (secondary fix)

**Files:**
- Modify: `internal/hooks/runner.go` (only the `runHook` function)

- [ ] **Step 1: Read the current `runHook` implementation**

Open `internal/hooks/runner.go:156-188`. The current code uses `cmd.Stdout = &limitedBuffer{...}` and relies on `cmd.Wait()` to drain the buffer. The fix: use `cmd.StdoutPipe()` and explicitly read it.

- [ ] **Step 2: Refactor `runHook` to use `StdoutPipe` + `io.ReadAll`**

```go
func runHook(ctx context.Context, command string, payload any) ([]byte, error) {
    cmd := exec.CommandContext(ctx, "sh", "-c", command)
    cmd.Env = scrubHookEnv(os.Environ())

    stdoutPipe, err := cmd.StdoutPipe()
    if err != nil {
        return nil, err
    }
    cmd.Stderr = &limitedBuffer{max: maxHookOutputBytes}

    stdin, err := cmd.StdinPipe()
    if err != nil {
        return nil, err
    }

    if err := cmd.Start(); err != nil {
        return nil, err
    }

    if err := json.NewEncoder(stdin).Encode(payload); err != nil {
        _ = stdin.Close()
        _ = cmd.Wait()
        return nil, err
    }
    _ = stdin.Close()

    limited := &limitedBuffer{max: maxHookOutputBytes}
    if _, err := io.Copy(limited, stdoutPipe); err != nil {
        return nil, err
    }

    if err := cmd.Wait(); err != nil {
        return limited.bytes(), err
    }

    if limited.truncated {
        return limited.bytes(), fmt.Errorf("hook output exceeded %d bytes", maxHookOutputBytes)
    }
    return limited.bytes(), nil
}
```

Add `"io"` to the imports if not present (it should already be imported via `bytes` or another file; verify).

- [ ] **Step 3: Run all hooks tests**

Run: `go test -count=1 ./internal/hooks/`
Expected: all tests pass.

- [ ] **Step 4: Run the stress test 10x**

Run: `go test -count=10 ./internal/hooks/ -run TestPreToolUseStable 2>&1 | tail -20`
Expected: 10/10 PASS.

- [ ] **Step 5: Vet and format**

Run: `gofmt -w internal/hooks/runner.go internal/hooks/runner_test.go` and `go vet ./internal/hooks/`
Expected: clean.

- [ ] **Step 6: Commit the defensive fix**

```bash
git add internal/hooks/runner.go
git commit -m "fix(hooks): use StdoutPipe+io.ReadAll for deterministic stdout drain"
```

---

## Task 4: Final verification

- [ ] **Step 1: Run all hooks tests 5x with -race**

Run: `go test -count=5 -race ./internal/hooks/ 2>&1 | tail -20`
Expected: all tests pass, no race warnings.

- [ ] **Step 2: Run the full suite**

Run: `go test -count=1 ./... 2>&1 | grep -E "^FAIL|^ok" | tail -20`
Expected: all packages pass (except any pre-existing flakes).

---

## Batch closeout

After Task 4, run the full verification gates:

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

---

## Self-Review

**Spec coverage:**
- The 3 test scripts now use `printf '%s\n'` with an explicit trailing newline, removing the most likely source of the flakiness.
- The runner now uses `StdoutPipe` + `io.ReadAll` instead of `*limitedBuffer` as `cmd.Stdout`, making the drain behavior explicit and deterministic. The `limitedBuffer` semantics are preserved (1 MiB cap, truncation error).
- A new stress test (`TestPreToolUseStable`) runs 100x and pins the fix; if the flakiness recurs, the stress test will catch it.

**Type consistency:**
- `runHook` signature unchanged.
- `*limitedBuffer` continues to wrap `bytes.Buffer` and expose `bytes() []byte`; the change is in WHO calls `Write`, not in the buffer itself.

**Placeholder scan:** No TBDs. The implementer may find that Task 1's stress test passes consistently in their environment; in that case, the fix is still applied (defensive) and the test still serves as a regression sentinel.
