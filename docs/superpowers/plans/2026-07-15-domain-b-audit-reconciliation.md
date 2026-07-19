# Domain B — Audit Doc Reconciliation (F-BUG-39 … F-POL-46)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mark the 8 Domain B findings (F-BUG-39, F-BUG-40, F-BUG-41, F-BUG-42, F-BUG-43, F-POL-44, F-POL-45, F-POL-46) as RESOLVED in the audit table at `docs/14-codebase-improvement-audit-2026-07-14.md`. All 8 are already implemented in code on main via the `fix/tools-policy-audit-2026-07-15` branch (merged as `a189a0c`); only the audit table is stale.

**Architecture:** Two small tasks. Task 1 verifies each of the 8 fixes is genuinely present and non-tautological by reading the relevant code and running the existing tests. Task 2 appends a new "Batch 28" resolution block to the audit doc with the 8 entries and exact rationale, matching the format used by Batches 1-27. No production code changes.

**Tech Stack:** N/A — documentation-only fix; no code, no tests, no new files. The verification task runs `go test` against existing test functions.

## Global Constraints

- Go 1.22+; the verification task compiles and tests the existing code but does not change it.
- The audit doc lives at `docs/14-codebase-improvement-audit-2026-07-14.md` and is tracked on main.
- Every commit MUST be self-contained: Task 1 is a single commit; Task 2 is a single commit.
- Do not introduce new files. The audit doc is updated in place.
- Do not introduce new dependencies; stdlib only.
- Preserve the audit doc's existing structure (resolution tables, batch section ordering, severity column format).

## File Structure

Files modified or created by this plan:

- `docs/14-codebase-improvement-audit-2026-07-14.md` — Task 2 (append Batch 28 resolution block).
- No new files. No code changes.

---

### Task 1: Verify each of the 8 B-domain fixes is present on main

**Scope:** For each of the 8 findings (F-BUG-39, F-BUG-40, F-BUG-41, F-BUG-42, F-BUG-43, F-POL-44, F-POL-45, F-POL-46), read the relevant code on main and confirm the fix is present, non-tautological, and not regressed. Run the existing tests. Document any deviation.

**Why:** The audit doc is the source of truth for what work has been completed. Before recording the work as RESOLVED, the implementer must independently verify each fix is in place. This task produces a per-finding verification report that Task 2 uses as the rationale in the audit table.

**Implementation steps:**

- [ ] **Step 1: Verify F-BUG-39** (`file.write_patch` can create new files)

In `internal/tools/native/file.go`, find the `file.write_patch` handler. Confirm that it calls `os.OpenFile` (not just `os.Open`) with `O_CREATE` in the flag set, and that the early check rejecting new files has been removed or relaxed. Also confirm the existing test `TestFileWritePatchCreatesNewFile` (or similar) passes.

Run: `CGO_ENABLED=1 go test ./internal/tools/native -run TestFileWritePatch -v`

Expected: PASS, with the test covering both "patch existing file" and "create new file" paths.

Record in the verification report:
- The line(s) where `O_CREATE` appears.
- The test that exercises new-file creation.
- PASS or FAIL.

- [ ] **Step 2: Verify F-BUG-40** (patch parser rejects unclosed/empty blocks)

In `internal/tools/patch/parser.go`, confirm the `flushChunk` closure (around line 28) returns errors for unclosed SEARCH/REPLACE blocks, and that the `>>>>>>> REPLACE` arm checks `currentPath == ""`. Confirm `TestParseRejectsUnclosedSearch` and `TestParseRejectsEmptyPathChunk` pass.

Run: `CGO_ENABLED=1 go test ./internal/tools/patch -run TestParse -v`

Expected: PASS.

Record: line of the unclosed-block error, the empty-path check, and the two passing test names.

- [ ] **Step 3: Verify F-BUG-41** (re-evaluate policy after user edits)

In `internal/agent/runner.go`, find the branch where the agent edits a tool's args (the "shell edit" path). Confirm it calls `r.Evaluate(...)` again (or `policy.Evaluate`) on the edited args, and that the resulting `Decision` is honored. Confirm `TestRunnerReevaluatesPolicyAfterEditedArgs` and `TestRunnerReevaluatesDenyAfterValidEdit` pass.

Run: `CGO_ENABLED=1 go test ./internal/agent -run TestRunnerReevaluate -v`

Expected: PASS.

Record: line(s) of the re-evaluation call, the test names, and PASS/FAIL.

- [ ] **Step 4: Verify F-BUG-42** (`web.fetch` decodes HTML entities)

In `internal/tools/native/web.go`, find the `htmlToText` function (around line 189). Confirm it calls `html.UnescapeString` (stdlib). Confirm the test for numeric entities (`&#39;`) passes if one exists.

Run: `CGO_ENABLED=1 go test ./internal/tools/native -run TestHtmlUnescape -v`

Expected: PASS, or no test (in which case note "no test, but `html.UnescapeString` is the canonical fix").

Record: line of `html.UnescapeString`, the test name (if any), and PASS/FAIL.

- [ ] **Step 5: Verify F-BUG-43** (job output splits stdout/stderr)

In `internal/tools/native/jobs_manager.go`, find the job-output assembly path (around line 295). Confirm it uses `formatCommandOutput(stdout, stderr)` rather than `stdoutStr + stderrStr`. Also confirm the `formatCommandOutput` function produces the `stdout:\n...\n\nstderr:\n...` structure.

Run: `CGO_ENABLED=1 go test ./internal/tools/native -run TestJob -v`

Expected: PASS.

Record: line of the `formatCommandOutput` call, line of the helper definition, and PASS/FAIL.

- [ ] **Step 6: Verify F-POL-45** (logger injected into `PolicyEngine`)

In `internal/tools/policy/policy.go`, confirm the `PolicyEngine` struct has a `logger *slog.Logger` field (around line 52), a `SetLogger` method (around line 102), and a default of `slog.Default()` in `NewEngine`. Confirm production wiring in `internal/app/app.go` calls `pol.SetLogger(state.Logger())` (around line 376).

Run: `CGO_ENABLED=1 go test -race ./internal/tools/policy -count=1`

Expected: PASS.

Record: line of the struct field, line of the setter, line of the production call, and PASS/FAIL.

- [ ] **Step 7: Verify F-POL-44** (deduplicated guardrail)

In `internal/tools/native/command.go`, confirm there is no `validateConservativeCommand` function (the audit's audit-FIX was "remove it"). Confirm the runtime path uses the `policy` package's `Evaluate` (or its `EvaluateGuardrails` helper) for the conservative check. Confirm `TestCommandOutputIsLimited` (or similar) passes.

Run: `CGO_ENABLED=1 go test ./internal/tools/native -run TestCommandOutput -v`

Expected: PASS.

Record: the absence of `validateConservativeCommand`, the line of the runtime `policy.Evaluate` call, and PASS/FAIL.

- [ ] **Step 8: Verify F-POL-46** (`LimitsJSON` returns its marshal error)

In `internal/tools/registry/types.go`, confirm the `LimitsJSON` method (around line 75) returns `(string, error)`, NOT just `string`. Confirm the error is returned (not swallowed) when `json.Marshal` fails. Confirm the test `TestSandboxMetaLimitsJSONReturnsValidJSON` passes.

Run: `CGO_ENABLED=1 go test ./internal/tools/registry -run TestSandboxMetaLimitsJSON -v`

Expected: PASS.

Record: the method signature line, the line of the error return, and PASS/FAIL.

- [ ] **Step 9: Write the verification report**

Write the per-finding report to `/Users/alecpullen/projects/coder-agent/.worktrees/<worktree>/.superpowers/sdd/task-1-report.md`. Each finding gets a one-line PASS/FAIL summary plus the file:line references recorded above.

If any finding is FAIL or regressed, report BLOCKED to the controller — do not proceed to Task 2.

- [ ] **Step 10: Commit the verification report (only)**

```bash
git add .superpowers/sdd/task-1-report.md
git commit -m "docs(verify): confirm B-domain fixes F-BUG-39..F-POL-46 on main"
```

(The commit message uses "docs" because the only artifact is a report; no code is changed.)

---

### Task 2: Append Batch 28 to the audit doc

**Files:**
- Modify: `docs/14-codebase-improvement-audit-2026-07-14.md` (append at end).

**Why:** The 8 findings are code-resolved but the audit table has no rows for them. The next audit pass would re-surface them as open. Adding a Batch 28 entry is the canonical way to mark them RESOLVED, matching the format used by Batches 1-27.

**Implementation steps:**

- [ ] **Step 1: Determine the insertion point**

In `docs/14-codebase-improvement-audit-2026-07-14.md`, find the end of the file. The last batch is "Batch 27" (added in commit `ac9166b`). The new entry goes AFTER Batch 27.

Confirm the trailing line is the last row of the Batch 27 table. Append immediately after.

- [ ] **Step 2: Append the Batch 28 block**

Use the exact format used by Batch 27 (and the earlier batches). The block is a level-3 markdown heading followed by a table with three columns (Finding, Status, Notes). Each of the 8 findings gets one row.

The entry:

```markdown
### Batch 28 (B — tools & policy audit fixes): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-BUG-39 | RESOLVED | `file.write_patch` now uses `os.OpenFile` with `O_CREATE` and supports new-file creation. New test `TestFileWritePatchCreatesNewFile`. |
| F-BUG-40 | RESOLVED | `Parse` returns errors for unclosed SEARCH/REPLACE blocks and rejects chunks with no `File:` header. New tests `TestParseRejectsUnclosedSearch`, `TestParseRejectsUnclosedReplace`, `TestParseRejectsEmptyPathChunk`. |
| F-BUG-41 | RESOLVED | After a user edits a tool's args, the runner re-evaluates the policy and propagates errors. New tests `TestRunnerReevaluatesPolicyAfterEditedArgs`, `TestRunnerReevaluatesDenyAfterValidEdit`. |
| F-BUG-42 | RESOLVED | `htmlToText` now uses `html.UnescapeString` from stdlib, decoding both numeric (`&#39;`) and named entities. |
| F-BUG-43 | RESOLVED | Job output uses `formatCommandOutput` to produce `stdout:\n...\n\nstderr:\n...`. New test `TestJobOutputSplitsStdoutStderr`. |
| F-POL-44 | RESOLVED | `validateConservativeCommand` removed; runtime path now uses the single `policy` package implementation. New test `TestCommandOutputIsLimited`. |
| F-POL-45 | RESOLVED | `PolicyEngine` has a `logger *slog.Logger` field, `SetLogger` setter, and production wiring at `app.go:376` injects `state.Logger()`. Default `slog.Default()`. |
| F-POL-46 | RESOLVED | `SandboxMeta.LimitsJSON` returns `(string, error)`. New test `TestSandboxMetaLimitsJSONReturnsValidJSON`. |
```

(Adjust the test names if the verification report from Task 1 surfaces different names.)

- [ ] **Step 3: Verify the audit doc is well-formed**

Open the file and confirm:
- The Batch 28 table has exactly 8 rows.
- Each row's Finding column matches the format `F-BUG-XX` or `F-POL-XX`.
- The Status column is `RESOLVED` for all 8.
- The Notes column has a one-line description and a test name where applicable.

- [ ] **Step 4: Commit**

```bash
git add docs/14-codebase-improvement-audit-2026-07-14.md
git commit -m "docs(audit): mark B-domain findings F-BUG-39..F-POL-46 resolved (Batch 28)"
```

---

## Self-Review

```bash
git log --oneline -3
git diff HEAD~1 HEAD -- docs/14-codebase-improvement-audit-2026-07-14.md | head -50
```

Confirm:
- The Batch 28 entry is the only change in the doc.
- The 8 finding IDs match the resolution table.
- The format matches Batch 27 exactly.

If anything is off, fix it in a follow-up commit (don't amend the merge commit; the controller will squash if needed).

No `go build` or `go test` step is needed — this is a doc-only change.

## Notes for the implementer

- The verification step (Task 1) is the load-bearing one. If any finding is FAIL or regressed, escalate to the controller instead of recording it as RESOLVED in the audit doc.
- The worktree may be `feature/domain-b-audit-reconciliation` or any name; use whatever the controller has set up.
- The audit doc is in the main checkout as an untracked file. The implementer should `git status` first to see if it's untracked or committed; if untracked, the worktree will need a fresh copy (`cp /Users/alecpullen/projects/coder-agent/docs/14-codebase-improvement-audit-2026-07-14.md .` from inside the worktree).
- The previous session's plan file `docs/superpowers/plans/2026-07-15-tools-and-policy-fixes.md` is the historical plan that was executed to produce the code changes. This new plan is the audit-reconciliation follow-up, not a re-implementation.
