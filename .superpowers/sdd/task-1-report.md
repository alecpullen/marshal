# Task 1: B-Domain Fix Verification Report

**Date:** 2026-07-16
**Branch:** `feature/domain-b-audit-reconciliation` (worktree)
**Merge commit containing fixes:** `a189a0c` (Merge `fix/tools-policy-audit-2026-07-15`)

---

## F-BUG-39: `file.write_patch` can create new files

**Status:** PASS

**Fix location:**
- `internal/tools/native/file.go` lines 178–180: Dry-run loop skips `os.Stat` error when `os.IsNotExist(statErr)` — new file creation is allowed.
- `internal/tools/native/file.go` lines 196–198: Read loop skips `os.ReadFile` error when `os.IsNotExist(err)` — new file creation is allowed.
- `internal/tools/native/file.go` lines 224–230: Apply loop checks that SEARCH block is empty for new files, returning a clear error if not.
- `internal/tools/native/file.go` line 273: Uses `os.WriteFile` (not `os.OpenFile` with `O_CREATE` — the fix uses `os.WriteFile` which creates the file if it doesn't exist, equivalent to `O_CREATE|O_WRONLY|O_TRUNC`).

**Tests:**
- `TestWritePatch_NewFileCreation` — PASS
- `TestFileWritePatchTool` — PASS

**Notes:** The fix does not use `os.OpenFile` with `O_CREATE` as the brief suggested; instead it uses `os.WriteFile` (line 273) which inherently creates the file. The early-exit checks for non-existent files were removed/relaxed in the validate and read loops. This is functionally equivalent and correct.

---

## F-BUG-40: Patch parser rejects unclosed/empty blocks

**Status:** PASS

**Fix location:**
- `internal/tools/patch/parser.go` lines 28–36: `flushChunk` closure returns errors for unclosed SEARCH (line 33) or REPLACE (line 35) blocks.
- `internal/tools/patch/parser.go` lines 91–94: `>>>>>>> REPLACE` arm checks `currentPath == ""` and returns error `"patch: chunk has no File: header before line %q"`.
- `internal/tools/patch/parser.go` lines 43–49: `commitChunk` silently drops chunks with empty path (legacy behavior), but the `>>>>>>> REPLACE` arm catches this case first.

**Tests:**
- `TestParseRejectsUnclosedSearch` — PASS
- `TestParseRejectsUnclosedReplace` — PASS
- `TestParseRejectsEmptyPathChunk` — PASS
- `TestParsePatches` — PASS

---

## F-BUG-41: Re-evaluate policy after user edits

**Status:** PASS

**Fix location:**
- `internal/agent/runner.go` lines 1199–1212: After user edits args, calls `r.Policy.Evaluate(toolName, argsMap)` (line 1204). If the new decision is `DecisionDeny`, returns a tool-error message (line 1208–1211). If error from Evaluate, returns `fmt.Errorf("policy re-evaluate after edit: %w", perr)` (line 1206). `DecisionAllow` and `DecisionConfirm` proceed because the user already approved.

**Tests:**
- `TestRunnerReevaluatesPolicyAfterEditedArgs` — PASS
- `TestRunnerReevaluatesDenyAfterValidEdit` — PASS

---

## F-BUG-42: `web.fetch` decodes HTML entities

**Status:** PASS

**Fix location:**
- `internal/tools/native/web.go` line 191: `s = html.UnescapeString(s)` in the `htmlToText` function (line 189).

**Tests:**
- `TestHtmlToTextDecodesNumericAndNamedEntities` — PASS (covers `&amp;`, `&#39;`, `&#x27;`, `&copy;`, `&nbsp;`)

---

## F-BUG-43: Job output splits stdout/stderr

**Status:** PASS

**Fix location:**
- `internal/tools/native/jobs_manager.go` line 297: `combined := formatCommandOutput(stdoutTail, stderrTail)` — uses the helper instead of raw concatenation.
- `internal/tools/native/command.go` lines 125–127: `formatCommandOutput` helper definition: `return "stdout:\n" + stdout + "\n\nstderr:\n" + stderr`.

**Tests:**
- `TestJobOutputSeparatesStdoutAndStderr` — PASS
- `TestJobOutputIsLiveAndBounded` — PASS
- `TestJobOutputAndListTools` — PASS

---

## F-POL-44: Deduplicated guardrail

**Status:** PASS

**Fix location:**
- `validateConservativeCommand` function: **absent** from the entire codebase (confirmed via grep — no matches).
- `internal/tools/native/command.go` lines 48–49 and 93–94: Runtime path uses `t.guardrail(command)` which is wired to `pol.GuardrailCheck(cmd)` in `internal/app/app.go` line 392.
- `internal/tools/policy/policy.go` lines 528–534: `GuardrailCheck` calls `pe.EvaluateGuardrails(command, "deny")` which runs the AST-based guardrail analysis.
- `internal/tools/policy/policy.go` lines 491–522: `EvaluateGuardrails` is the single source of truth for conservative guardrail checks.

**Tests:**
- `TestCommandOutputIsLimited` — PASS
- `TestShellRunBlocksDangerousCommandBeforeRunner` — PASS
- `TestShellRunRejectsGuardrailDeniedCommand` — PASS

---

## F-POL-45: Logger injected into `PolicyEngine`

**Status:** PASS

**Fix location:**
- `internal/tools/policy/policy.go` line 52: `logger *slog.Logger` struct field.
- `internal/tools/policy/policy.go` line 76: Default `logger: slog.Default()` in `NewEngine`.
- `internal/tools/policy/policy.go` lines 102–110: `SetLogger` method.
- `internal/app/app.go` line 376: `pol.SetLogger(state.Logger())` in production wiring.

**Tests:**
- `go test -race ./internal/tools/policy -count=1` — PASS (all 37 tests pass, no race conditions)

---

## F-POL-46: `LimitsJSON` returns its marshal error

**Status:** PASS

**Fix location:**
- `internal/tools/registry/types.go` line 75: Method signature `func (m SandboxMeta) LimitsJSON() (string, error)` — returns `(string, error)`, not just `string`.
- `internal/tools/registry/types.go` lines 99–101: Error is returned when `json.Marshal` fails: `return "", err`.

**Tests:**
- `TestSandboxMetaLimitsJSONReturnsValidJSON` — PASS
- `TestSandboxMetaLimitsJSONIncludesOutputTruncated` — PASS

---

## Summary Table

| Finding | Status | Key File(s) | Key Line(s) | Test(s) |
|---------|--------|-------------|-------------|---------|
| F-BUG-39 | PASS | `internal/tools/native/file.go` | 178–180, 196–198, 224–230, 273 | `TestWritePatch_NewFileCreation`, `TestFileWritePatchTool` |
| F-BUG-40 | PASS | `internal/tools/patch/parser.go` | 28–36, 43–49, 91–94 | `TestParseRejectsUnclosedSearch`, `TestParseRejectsUnclosedReplace`, `TestParseRejectsEmptyPathChunk` |
| F-BUG-41 | PASS | `internal/agent/runner.go` | 1199–1212 | `TestRunnerReevaluatesPolicyAfterEditedArgs`, `TestRunnerReevaluatesDenyAfterValidEdit` |
| F-BUG-42 | PASS | `internal/tools/native/web.go` | 191 | `TestHtmlToTextDecodesNumericAndNamedEntities` |
| F-BUG-43 | PASS | `internal/tools/native/jobs_manager.go`, `internal/tools/native/command.go` | 297, 125–127 | `TestJobOutputSeparatesStdoutAndStderr` |
| F-POL-44 | PASS | `internal/tools/native/command.go`, `internal/tools/policy/policy.go` | 48–49, 93–94, 528–534 | `TestCommandOutputIsLimited`, `TestShellRunBlocksDangerousCommandBeforeRunner` |
| F-POL-45 | PASS | `internal/tools/policy/policy.go`, `internal/app/app.go` | 52, 76, 102–110, 376 | `go test -race ./internal/tools/policy -count=1` (37 tests) |
| F-POL-46 | PASS | `internal/tools/registry/types.go` | 75, 99–101 | `TestSandboxMetaLimitsJSONReturnsValidJSON`, `TestSandboxMetaLimitsJSONIncludesOutputTruncated` |

**All 8 findings: PASS** — 0 FAIL, 0 BLOCKED.
