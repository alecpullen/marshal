# SDD end-to-end findings — config & project identity plan (2026-08-31)

Findings from driving `/sdd` end-to-end against
`.docs-archive/superpowers/plans/2026-08-31-config-project-identity-refactor.md`
(9 tasks, 113 deterministic ops, glm-5.3-flash @ ollama-cloud as
implementer/reviewer), supervised externally via agent-tui.

Outcome: 9/9 task commits + one branch-review-fix commit, SPEC PASS on
re-review, `go build`/`go vet`/`go test ./...` green on the branch tip. But
the run needed **five supervisor interventions** to get there, and every one
of them points at a fixable gap in the plan format, the authoring skill, or
the pipeline.

---

## 1. What broke, in order

| # | Stage | Failure | Root cause |
|---|-------|---------|------------|
| 1 | task 1 prepare | `paths.go`: `expected 'package', found 'EOF'` | Plan patch replaced a whole file with a bare comment — no `package` clause. |
| 2 | task 1 prepare | `import cycle not allowed` | Plan made `internal/db` import `internal/repo`, but `repo` already imports `db`. |
| 3 | task 1 prepare | `undefined: repo` | Plan said "(file X gains the import)" in **prose**. Prose is never executed; no patch op existed for the import. |
| 4 | task 1 prepare | `go test` exit 1 with all packages `ok` | `TestStatusLineUntrustedIsWarningColored` fails under `TERM=xterm-256color`, passes under `TERM=dumb`. Pre-existing, at HEAD, unrelated to the plan. The pipeline inherits the TUI's env, so any real-terminal launch fails every verify that includes `./internal/app/...`. |
| 5 | task 3 prepare | `canonical_test.go`: `declared and not used: wd` | Plan-authored test code didn't compile (unused variable). |
| 6 | task 6 prepare | ~37 `not enough arguments in call to SaveProjectConfig` | The plan changed the signature but only patched production callers + a prose note saying "update every call in save_test.go". 29 identical call sites were written as 26 identical patch blocks, which preflight demoted to agent ops (ambiguous match) — and agent ops run **after** the prepare-phase go test, so the build was already broken. |
| 7 | task 6 prepare | patches to `save_sections_test.go` blocked | Preflight validates against the **pre-run tree**; the file is created by task 2, so task 6 patches to it are always "file not found" → blocked → skipped. |
| 8 | task 6 verify | `TestSetRefreshesOpenBrowserRegression` | Plan's `applyProjectLayer` lacked the zero-`Layers` early return its own prose promised ("zero Layers preserves historical behaviour"), so zero-layer callers (tests, headless) silently dropped sections. |
| 9 | task 6 verify | `TestConfigAgentSetProjectScope` nil-pointer panic | Plan's `t.sessionState.Layers()` ignored the codebase's existing nil-`sessionState` idiom. |
| 10 | task 6 verify | plan's own new test failed | `TestSaveProjectConfigDoesNotBakeUserValues` set `Commands.Test` to the **default** value; layer-aware saving correctly dropped it. The test contradicted the feature. |
| 11 | resume (×3) | `search block not found` | Resume re-applies a failed task's ops, but the failed attempt already applied them. Supervisor had to `git reset --hard` + delete untracked files by hand each time. |
| 12 | branch review | `unparseable output` retry | Reviewer's verdict file missed the required SPEC/QUALITY markers once; retry 1/2 recovered. |

Every failure except #4 and #12 was a **plan content defect** that a
mechanical check could have caught before the run started.

## 2. Pipeline bugs and gaps (marshal itself)

These are independent of this plan and worth their own issues:

1. **`phase="verify"` fence attributes are silently ignored.**
   `internal/pipeline/parse_blocks.go:163` reads `phase`/`expect_exit` from
   the block *content* props only; fence-info attrs are used for
   `file`/`path`/`replace` but not for `marshal.run`. Every run op in this
   plan (all written per the skill's documented syntax) defaulted to
   `prepare` — running **before** assertions, outside the verify-gate fixer
   loop, and fatally. Either parse fence attrs for run ops, or change the
   skill to put `phase = "verify"` in the body. The silent part is the
   problem: unrecognized attrs should be a parse error, not a default.

2. **Upfront preflight against the base tree makes valid plans
   unexecutable.** Patches to files created by earlier tasks are always
   blocked (#7), and N identical patches — a legitimate pattern for
   mechanical call-site ripples — are always ambiguous (#6). Fix: re-run
   preflight (or at least re-validate blocked ops) **at task start** against
   the current worktree state. Sequential application makes repeated
   identical patches unambiguous at that point.

3. **Resume is not crash-safe at op granularity.** A task that fails in
   prepare/verify leaves its mutations uncommitted in the worktree; the
   resume re-applies them and dies on the first patch (#11). Fix: on resume,
   if the worktree is dirty relative to the ledger's last commit, offer or
   perform an automatic `git reset --hard` + untracked-file cleanup of the
   failed task's file list before re-applying.

4. **Failure output is thrown away.** The TUI stores
   `firstLine(err.Error())`, so `ops run: ... output: <the actual compiler
   errors>` never reaches the transcript or the DB. Supervising this run
   required manually re-running the verify command in the worktree every
   time. Fix: write full op output to `.marshal/pipeline/<slug>/task-N.log`
   (or progress.md) and surface that path in the error row.

5. **Environment sensitivity of the test suite gates the pipeline.**
   `TestStatusLineUntrustedIsWarningColored` is TERM-dependent (#4). Whether
   the fix belongs in the test (`t.Setenv("TERM", ...)` / construct an
   explicit theme) or in the render path (256-color tier emitting no SGR in
   a non-TTY), `go test ./...` must be green in a normal developer terminal
   or every SDD verify is a coin flip.

6. **No formatting step.** The branch reviewer flagged missing trailing
   newlines/gofmt on plan-created files (M-6). Cheap fix: run `gofmt -w` on
   touched `.go` files after op application (or a `gofmt -l` assert in the
   verify gate).

7. **Observability during agent phases.** The parent turn shows
   `0 in · 0 out` while subagents (reviewer/fixer) run for 20-40 minutes —
   indistinguishable from a hang without tailing `marshal.log`. Also the
   repeated `file.write_patch TOCTOU re-check skipped: nil fileTracker`
   warnings show pipeline agents run without a file tracker.

8. **Estimation is wrong.** The cast list reported `fallback ops: 26 · est.
   model calls: 0` for a run that later spent 100+ minutes in model calls
   (review 21m, fix 43m, re-review 32m). Estimation ignores agent ops and
   the review/fix loop entirely.

9. **Hard stop after fix rounds.** "branch review still reports 2 blocking
   findings" ended a 100-minute run with no recourse but manual work. A
   human gate ("stop / another fix round / accept and finish") would fit the
   supervised-by-design nature of the pipeline. (The two remaining findings,
   I-5/I-6, are real but minor edge cases.)

## 3. Plan-authoring skill changes (`internal/skills/builtin/marshal-sdd-plan-authoring.md`)

The skill's rules are good but under-specified about *execution semantics*.
Add:

1. **Compile closure rule.** For every patch that introduces a symbol
   reference (new import, new identifier), the plan must contain the
   corresponding import patch as a `marshal.patch` — never prose. Prose
   parentheticals like "(gains the X import)" are silently dropped; the
   skill should name this explicitly as a defect class.

2. **Caller census rule.** When a patch changes a signature or exported
   API, the author must grep for **all** call sites — including `*_test.go`
   — and state the count in the plan ("37 call sites: 6 production, 31
   test"). Each site gets a patch or the task gets a scoped `marshal.agent`
   op. Counts in the plan let the reviewer verify completeness without
   redoing the search.

3. **Cross-task file rule.** Never `marshal.patch` a file created by an
   earlier task's `marshal.file` — preflight runs against the base tree.
   Either fold the final content into the original `marshal.file`, or use
   `marshal.file replace="true"` with full content.

4. **Test-code discipline.** Plan-authored test files are code: no unused
   variables, no references to symbols that don't exist yet at that task's
   point in the sequence, and values must be chosen against `Default()` when
   the feature under test does default-comparison. The authoring workflow
   should include mentally type-checking each new test file — or better,
   tooling (§4).

5. **Import-cycle awareness.** Before adding `import "marshal/internal/x"`,
   check the reverse edge. A one-line rule plus "when in doubt, duplicate a
   15-line helper" (which is what the fix shipped).

6. **Document the actual run-op semantics** once pipeline fix §2.1 lands —
   and until then, the skill and parser must agree on where `phase` lives.

## 4. Should plan text be machine-generated? — Yes, in three places

The run demonstrated that hand-written block text is the dominant defect
source (10 of 12 failures). Three tools would have caught or prevented all
of them:

1. **Plan checker (dry-run compile).** `marshal sdd --check <plan>` (or
   `/sdd --check`): parse, then simulate — apply all ops sequentially into a
   temp worktree, run `go build ./...` (and `go vet ./...`) after each task,
   report the first task whose post-state doesn't compile. No tests run, no
   model calls, ~1 minute. This catches failures #1, #2, #3, #5, #6, #7 —
   everything except behavioral test assertions and env issues. It also
   gives the *authoring* agent a self-correction loop: write plan → check →
   fix → check → hand to user. That loop is the single highest-leverage
   improvement available.

2. **Ripple generator.** For mechanical renames/signature changes, a helper
   that takes a search/replace pair plus a path glob and emits N
   uniquely-contexted `marshal.patch` blocks (the supervisor did exactly
   this with an ad-hoc Python script for 34 call sites). Could be a
   subcommand (`marshal plan ripple`) or a tool available to the authoring
   agent. This removes both the ambiguity demotion and the "prose says
   update the callers" failure mode.

3. **Block normalization at write time.** `marshal.file` content for `.go`
   files should be gofmt-normalized and newline-terminated by the pipeline
   at apply time (or flagged by the checker), eliminating the M-6 class.

The plan *narrative* (goals, design decisions, risk prose) should stay
human/model-authored — that part was accurate and useful to the reviewer.
It's the executable blocks that benefit from generation and mechanical
checking.

## 5. What worked well

- Deterministic ops + per-task commits + worktree isolation: 113 ops
  applied byte-exactly once the plan was correct; the main checkout was
  never touched; each task is an individually reviewable commit.
- The branch reviewer earned its keep: it caught a genuine design hazard in
  the *plan itself* (I-1: interactive trust prompt reachable inside the TUI
  event loop), verified the fix, and produced a precise, actionable verdict.
- Ledger-based resume across plan edits worked — task 1-5 commits survived
  three plan revisions; only op-level idempotence (§2.3) was missing.
- The verdict-parse retry recovered automatically (§1.12).

## 6. Suggested priority

1. Dry-run plan checker (§4.1) — prevents ~80% of observed failures.
2. `phase` fence-attr parsing + unknown-attribute errors (§2.1).
3. Task-start preflight re-validation (§2.2) — unblocks cross-task patches
   and repeated-patch ripples.
4. Resume auto-reset of the failed task's dirty state (§2.3).
5. Full op output to a log file, surfaced in the TUI error (§2.4).
6. TERM-sensitive status-line test (§2.5) — it makes `go test ./...` red for
   every developer in a real terminal, pipeline or not.
7. Ripple generator (§4.2), gofmt normalization (§4.3/§2.6), then the skill
   text updates (§3), which should land alongside the tooling they describe.
