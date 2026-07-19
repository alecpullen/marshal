# 16 — Codebase Maintainability Audit (2026-07-19)

Method: 19 parallel read-only deep-dive audits — one per package or package group,
plus a repo-wide cross-cutting scan and a cross-check of the two prior audits
(`docs/13`, `docs/14`) against current code. Every finding below was verified
against source by the auditor that reported it, with file:line evidence.

**Status: findings open unless marked otherwise. Remediation order agreed with
owner: Theme 2 (bugs) → Theme 1 (dead code) → Theme 3 (duplication) → Theme 4
(god objects) → Theme 5 (docs/comments).**

## Executive summary

The core is healthier than expected: streaming, MCP lifecycle, `jobs_manager`
locking, and error-wrapping discipline (`%w` 326 vs 9 `%v`) are genuinely good,
and the prior audit program (~191 findings in docs/13–14) is verified as
actually implemented. The real problem is debris from abandoned development
directions: dead pipelines, dead config knobs, and copy-paste duplication that
has already started drifting. The audit also surfaced ~20 real bugs, two of
them security-relevant.

Scale: ~38k LOC production Go, ~48k LOC tests, 27 packages under `internal/`.

## Theme 1 — Dead code from abandoned directions

Pure deletions, no behavior change (~1,500+ LOC production plus tests):

- `internal/csync/` — entire package unused; zero non-test imports. Its doc says
  it was "adopted for new concurrent state" but `session.State` still uses
  ad-hoc mutexes.
- `internal/contextpack/builder.go` — `Builder`/`NewBuilder`/`BuildInput`/
  `buildCandidateSections` are test-only; production builds packs incrementally
  via `PinFiles`/`MergeMemories` (`internal/agent/runner.go:385-424`).
- `internal/agent/compact.go` — dead since 4dec97e5 ("terminate turn on
  summarization failure"); ~270 lines with its test.
- `internal/db/dbpool.go` — `OpenWithPool`/`readDB` opens 4 idle SQLite
  connections nothing reads through; unfinished perf plan
  (`docs/superpowers/plans/2026-07-15-domain-e3-db-perf.md`).
- `internal/llm` dead surface:
  - `Temperature`/`TopP`/`ReasoningEffort` (`routing/types.go:28-31`) plumbed
    through preset → config → settings UI but never copied into a `ChatRequest`;
    `ReasoningEffort` has no wire field at all.
  - `Embed` (`provider.go:15`, `openai_compatible.go:383-417`) — no production
    caller; only test fakes implement it.
  - `ProviderCapabilities` — only 3 of 9 fields ever read.
  - `ContextBudget.MaxConversationTokens` + five `Include*` booleans never read.
  - `ErrNoRoute` sentinel never returned.
- `internal/tools/policy` — `AllowSudo`/`AllowDestructive` only append
  "(flagged allowed)" to a deny reason; decision stays `DecisionDeny` with no
  TUI confirm. Dead config flags with full plumbing.
- `internal/agent/sdd` — `SDD.MaxTotalTokens` settable but no meter reads it.
- `internal/agent/metrics.go:24` — `SoftStalls` plumbed through 3 packages into
  the DB, never incremented, always 0.
- Scattered: `settings/validation.go` + write-only state fields (~150 lines),
  `skills.ToolDef` (parsed, never registered), `snapshot.Revert`,
  `db.SnapshotFiles`, `db.LoadTodos`, `scanner.Scan`/`hashFile` (test-only),
  `filetrack` List methods, 4 dead `PageHandle` methods,
  `contextpack.Pack.Pinned`, `repo.SkipGitignore`, swarm leftovers
  (`Worktree.MergeBase`, ledger SHA parsing, `TaskState.TestFailures`/
  `FinalSummary`, `WriteBranchReviewPackage`, `Orchestrator.NewMeter`, dead
  `overBudget` recheck at orchestrator.go:187), `connect` dead items
  (`successStyle`, `pendingSave`, `probeErr`, `nowNanos`, unreachable `back()`
  branch, `enterPickModel`), `app` dead items (`marshalGlobalAPIKey`,
  `_ = oldSnap`, `case "json"`, `jobBrokerCtx`, unused `stderr` param),
  `sandbox` dead items (`clock.go`, `killedReason`, `allowSet`),
  `session` dead items (`PendingApprovalInfo`/`PendingQuestionInfo` event
  fields, `DeactivateSkill`), tui dead items (`tailRunes`,
  `thinkingBoxTailLines`, unreachable `Update` cases at model.go:793-806),
  `diffview` dead items (`Options.Theme`, `LineFileHeader`, `contextStyle`),
  `subagent.go` unused `maxConcurrent` param, `dock.CloseMsg` (zero callers),
  `acp` dead items (`replaceExisting` return value, `runWithConfig` stderr
  param, `turnAnswered` belt-and-suspenders map).

## Theme 2 — Bugs surfaced by the audit

Ordered roughly by severity. (Execution batch 1.)

1. **[security] MCP policy matching is nondeterministic** — `policy.go:157-174`
   ranges a `map[string]string`, breaks on first match; overlapping `deny` can
   lose to map order. Fix: two-pass deny-first matching.
2. **[security] `MCPServerConfig.Trust` silently discarded** — decoded into
   `fileMCPServer` which has no Trust field (`config.go:419-423`, merge
   `:996-1006`, save `save.go:177`); documented `trust = "unrestricted"` does
   nothing. Fix: wire through fileMCPServer + merge + save.
3. **Self-deadlock + race in ACP lister** — `internal/acp/lister.go:52-77`
   re-locks a held mutex on TTL expiry; `:89` reads `existing.db` lock-free.
   Fix: single-mutex rewrite; add TTL-expiry + race tests.
4. **MCP client permanently dies on any >64KB line** — default
   `bufio.Scanner` limit at `internal/tools/mcp/client.go:221`; one oversized
   tool result poisons the client (`c.err` at :247). Fix: `scanner.Buffer`.
5. **Racy message-ID promotion in session** — `session.go:1036-1073` mutates a
   stashed pointer across an unlock; transient IDs leak to ACP clients; Rewind
   can orphan the pointer. Root cause: dual ID space + `dbIDToImID` map kept
   for session lifetime. Fix: persist-then-insert once with final ID; nil the
   map after load.
6. **Theme colors silently nil in TUI** — 9 package-level styles in
   `transcript.go:246,319,321,394`, `status.go:193-196`, `approval.go:18`
   capture nil color vars at init (before `loadTheme` runs at model.go:2522);
   warnings render bold-only. Fix: lazy style funcs reading `theme.Current()`.
7. **Busy-loop spinner** — `connect/connect.go:202-206` re-arms a zero-delay
   tick; spins a core during every probe. Fix: `tea.Tick(100ms, …)`.
8. **Onboarding Ollama fallback unreachable** — `onboarding.go:128-135` retries
   `/api/tags` only on transport error; default URL returns clean 404, so model
   listing always fails on the default path. Fix: retry on non-200 too.
9. **MCP manager subprocess leak** — `app.go:407-425, 493-498`: failures after
   `mcpMgr.Start` skip `mcpMgr.Close()`. Fix: deferred cleanup slice.
10. **Darwin sandbox reports a memory limit it never enforces** —
    `sandbox.go:134-137` vs `restricted_unix.go:75`; audit log records a
    phantom limit. Fix: zero `memBytes` when unsupported; windows stub.
11. **Desktop browser tools clobber URL/Title** — partial `BrowserInfo`
    literals replace the whole struct (`desktop/tools.go:185,288,392`;
    `SetBrowserInfo` at session.go:670). Fix: merge-style state updates.
12. **`config.Load` swallows user-file merge errors** — `config.go:656`
    ignores the error the project-file call checks at `:680-682`. Fix: check +
    wrap with file path.
13. **Policy data race on `pe.rules`** — `Evaluate` reads unlocked
    (`policy.go:181,233,249,496`) while `SetRules` writes under mutex; TUI
    calls it from the UI goroutine. Fix: RLock snapshot.
14. **Hand-rolled bubble sort per keystroke** — `completions.go:144-151`, O(n²)
    swaps on every keypress. Fix: `slices.SortFunc`.
15. **Default `execRunner` ignores `timeout_seconds`** —
    `internal/tools/native/runner.go:16-34`; test at native_test.go:97 masks
    it. Fix: `context.WithTimeout` mirroring `sandbox/timeout.go:13`.
16. **SDD error swallowing → reviewer grades empty diff** —
    `sdd/orchestrator.go:133,155,180,221,160,78-79` unchecked; `gitMergeBase`
    HEAD fallback (:300-306) yields base==head → "✅ ready to merge" on nothing.
    Also `BuildFixPrompt("", …, PlanTask{})` (:149) builds a malformed prompt,
    and `parseImplementerStatus` (:332-340) checks DONE before BLOCKED so "not
    done" in a BLOCKED summary returns DONE.
17. **`truncateVisible` mangles ANSI-styled diff lines** —
    `diffview/diffview.go:506-514` truncates by runes after chroma escapes are
    applied. Fix: truncate before styling or use `ansi.Truncate`.
18. **UTF-8 hazards** — `connect/connect.go:371` (`truncateErr` byte slice),
    `agent/title.go:66-68` (`title[:titleMaxChars]`), `fuzzy/fuzzy.go:34`
    (rune vs byte compare; non-ASCII never matches).
19. **Policy nil-config panics** — `NewEngine(nil,nil)` + `Evaluate` panics at
    `policy.go:200,268,285,292,299`.

## Theme 3 — Duplication & missed reuse

- **Config is the worst offender:** every setting exists in 4–5 places
  (runtime struct, nullable mirror, `Default()` at config.go:511-628, 361-line
  `merge()` at :771-1131, 215-line `SaveProjectConfig` at save.go:20-234). The
  Trust bug is the direct cost. Generic `set[T]` helper + toml tags on
  `routing` types + role-keyed profile map cuts ~250 lines.
- **Duplicate `AgentRole` types bridged by string casts** — `routing/types.go:3-20`
  (14 roles) vs `agent/prompts.go:13-26` (10 roles), joined by casts at
  `app.go:519,568`. Fix: `type AgentRole = routing.AgentRole` alias.
- **Truncator sprawl:** 7+ hand-rolled truncators with divergent semantics
  (`truncateRunes` ×2 with different behavior, `truncateErr` ×2, `truncateGoal`,
  `truncateForDisplay`, `truncateVisible`); `ptr[T]` duplicated at
  config/save.go:15 and tui/transcript.go:38. One shared helper.
- **Copy-paste clusters:** stall-handling ×6 in runner.go:542-761;
  persist→reload→apply ×4 in tui/model.go:344-376,590-634,2278-2305,2331-2361;
  run-dispatch ×3 (model.go:969-978,2015-2024,2041-2050); ACP shutdown ×3
  (server.go:267-330) + parse prologue ×9 + lifecycle handlers ×4
  (session.go:218-411); desktop tool handlers ×6; hooks/permissions frames ×2
  (~75 lines each, frames_collections.go:391-572) + `listStringOpts` third copy;
  insert helpers ×3 (→ `slices.Insert`); `scanMessage` ×2 (sessions.go:208-314);
  Migrate repetition (db.go:55-133); GetProject/GetProjectByRoot twins;
  env-upsert ×2 (container.go:118-131 = restricted.go:141-152);
  `usageBody`→`TokenUsage` mapping ×2; `chatNoTools`/`chatOnce` drainers ×2;
  swarm/SDD runner factories ~25 lines ×2 (app.go:516-591, already drifting —
  SDD silently omits MetricsObserver/MaxRetries/MaxTurnContextTokens/PlanFirst);
  `BrokerCloser`/`mustDB` type-assert-panic ×3; undo/redo/diff/rewind preamble
  ×4 (commands.go:277-411); `compactTokens`/`compactTokenCount` twins;
  `basename`/`basenameLower` twins; `List`/`ListDeferred`/`ListLoaded` ×3;
  `languageOf` duplicates `repo.DetectLanguage`; two localhost detectors
  (routing vs probe — one backs the F-SEC-09 security gate); two secret-env
  predicates (`IsSecretBearer` vs `IsSecretKey`, disagree on edge keys);
  AttachBackend ≈ StandaloneBackend; request-timeout set 3× contradicting the
  runner's own 5-min default; shutdown-answer logic duplicated session↔acp.
- **Path literals re-derived everywhere:** `.marshal/marshal.db` ×4
  (acp/lister.go:65,102,116; app.go:186), `~/.config/marshal` ×5.
- **Indirection that buys nothing:** `BrokerCloser` interface asserted back
  with panics (runtime.go:69-70, app.go:739-746,822-825); `sessionState any`
  re-asserted at 5 sites (native.go:93); `findClient` re-lookup
  (mcp/manager.go:186-216); `TaskProfile` one-field wrapper; variadic-optional
  bools every caller passes explicitly (prompts.go:182, runner.go:968).

## Theme 4 — God objects & over-engineering

- `tui/model.go` (2,657 lines; `Update` = 429 lines at :567-995),
  `session.go` (1,645; ~40 fields from 8 feature eras at :336-417),
  `runner.go` (1,806; `RunTask` = 465 lines at :330-795),
  `config.go` (1,161). Auditors' consistent advice: **no rewrites** — extract
  repeated blocks into helpers and split files by topic (session already
  demonstrates the pattern with `sdd_progress.go`/`swarm_progress.go`).
- `Evaluate` (policy.go:133-304) is a 170-line four-domain function with stale
  step numbering → extract `evaluateMCP`/`evaluateShell`.
- `approvalModel` shadows huh's select with parallel state + double-Esc timer
  (~60 lines removable, behavior change — needs owner sign-off).
- Split-brain command dispatch: 13 commands register placeholder handlers in
  commands/commands.go:160-259 while real logic lives in a second switch in
  tui/model.go:1940-2086.
- `GetToolCalls` sandbox round-trip (db/audits.go:188-264): reconstructs state
  from 3 sources incl. untyped `map[string]any` re-parse with 6 nested
  assertions; fix via dedicated columns or typed struct unmarshal.

## Theme 5 — Stale docs & comments

- `docs/14` tracker misreports ~45 fixed findings as open: Batch 5/6 (Section
  C, merged 32e5168) and F1–F5 (merged 251e838, 2e0f07c, 36b2fb5, d0e66fe,
  fdadb55) were implemented but never recorded. Fix: append resolution tables.
- `docs/plans/` holds abandoned docs that contradict the current UI
  (2026-07-03 TUI redesign dual-column), a shipped-but-unmarked milestone-g
  plan, vestigial `task.md`, and duplicate milestone stubs vs
  `docs/superpowers/plans/`. Keep one canonical plan home.
- Stale comments contradicting code: runner.go:139-142 (resolveRoute doc vs
  F-SEC-10 behavior), db.go:12-16 (describes a design that doesn't exist),
  help.go:41-43 (overlay removed in 9f27c1a), meter.go:26-33 (predates
  UsageObserver), compact-related comment at prompts.go:315-317 (false),
  settings/field.go:21-23 + reset.go:53 (describe scrapped Ctrl+S transaction
  model), session.go:1178-1187 (ClearMessages doc misleads), feature tags
  (F19 R4, F-SEC-19, F-PERF-117, F-SAFE-22) meaningless to future readers.
- Onboarding role list (onboarding.go:398) hardcodes 9 roles; routing defines
  14 — will drift again.

## Theme 6 — Test-side issues (noted, lower priority)

- `model_test.go` 4,728 lines: `session.New(config.Default(), "/repo", …)`
  repeated 53×; `newTestModel` helper underused (34 of 158 tests).
- `app_test.go` 69 KB / ~2,300 lines: `&Runtime{...}` setup blocks repeat.
- swarm tests hand-encode JSON protocol responses (brittle `ReplaceAll`
  escaping at orchestrator_test.go:55) and re-implement
  `agenttest.ScriptedProvider.Usages` (`usageScriptedProvider` :468-489);
  migrate to `RunTaskFunc` + agenttest.
- Misplaced `agent_run_test.go` (tests `agent.NewSubagentTool`, lives in
  `package native`).
- Misleading `TestOverBudgetRechecksAfterRole` (never calls `Run`).
- Vacuous test asserting nothing leaks to `Run`'s unused stderr param
  (app_test.go:989-1004).

## What's in good shape (do not touch)

- `internal/agent` satellite files (protocol, progress, history, spill,
  summarize, handoff, finalize, classify, toolargs, file_index_cache).
- MCP `Call`/pending-map machinery; `url_filter.go`.
- `jobs_manager.go` locking/WaitGroup discipline; `saferesolve.go`;
  `BoundedOutput`; tool-constructor pattern.
- `internal/sandbox` exec lifecycle, `terminateProcessTree`, platform split.
- `internal/llm` streaming/tool-call buffering core, catalog, factory.
- `internal/acp` protocol.go, permissions.go; locking discipline overall.
- `trust`, `export` (incl. XSS regression tests), `jsonextract`, `redact`,
  `diagnostics`, `pubsub` core, `permissions/pattern.go`, `cmd/marshal`.
- `agenttest` helper (~100 call sites, consistently reused).
- swarm/sdd core state files (state.go, lock.go, verdict.go, plan.go, prompts).

---

# Appendix — full per-package auditor reports

These are the verbatim reports from the 19 auditors, in scope order.




---

**Scope:** Package `internal/app` root files ONLY: `internal/app/app.go` (933 lines, dependency wiring/Run), `internal/app/onboarding.go` (517), `internal/app/runtime.go` (493), and `internal/app/logging/`. Do NOT audit subdirectories config/, session/, tui/ — other agents cover those.

# Audit Report: `internal/app` root files

## Scope summary

`app.go` wires the whole agent stack (routing, sandbox, registry, MCP, swarm/SDD, snapshots) and drives the TUI `Run` loop; `runtime.go` owns headless lifecycle (`StartRuntime`/`Quiesce`/`Close`); `onboarding.go` is a Bubble Tea wizard that writes the initial config; `logging/` is an 11-line `slog` wrapper. Overall health is mixed: `runtime.go`'s Once-based lifecycle is careful and well documented, but `app.go` suffers from an out-of-control constructor signature, inconsistent partial-failure cleanup, and copy-pasted runner setup, while `onboarding.go` contains two genuine bugs plus dead code.

## Findings

- **[HIGH] Ollama model-list fallback is unreachable for its stated purpose** — `onboarding.go:128-135`, default URL at `:242`. The `/v1/tags`→`/api/tags` retry only fires when `client.Get` returns a transport error. The default URL `http://localhost:11434/v1` yields `/v1/tags` → Ollama answers 404 with `err == nil`, so flow goes to the non-200 branch and model listing always fails on the default path; users must type the model manually. Fix: attempt the `/api/tags` fallback on non-200 status too (move the retry after the status check).
- **[HIGH] MCP manager leaks on two error paths** — `app.go:361-367` (defer only shuts down `jobManager`), `app.go:407-425`, `app.go:493-498`. If `reg.Register(agent.NewSubagentTool…)` or `desktop.RegisterAll` fails after `mcpMgr.Start` succeeded, `mcpMgr.Close()` is never called (only the `RegisterTools` branch at `:411-415` closes it) — leaked MCP subprocesses. Fix: replace the `jmErr` flag trick with a small `cleanup []func()` slice deferred once, appending `jobManager.Shutdown` and `mcpMgr.Close` as each resource is created.
- **[MED] `buildAgentRunner` returns 9 values; 7 error sites repeat the nil-tuple** — `app.go:322`, e.g. `:326, :341, :399, :424, :496`. Counting `nil`s is error-prone (the swarm/SDD/MCP ordering has already forced the awkward conditional assignment in `runtime.go:466-477`). Fix: return a small `agentBundle` struct + error; callers name the fields they need.
- **[MED] Swarm and SDD runner factories are ~25 lines of near-identical code, already drifting** — `app.go:516-555` vs `:567-591`. The SDD copy silently omits `MetricsObserver` (SDD turns never persist metrics), `MaxRetries`, `MaxTurnContextTokens`, and `PlanFirst` — likely accidental, but impossible to tell. Fix: extract one `newRoleRunner(role, scope, …)` helper so the intentional differences (WriteGate, per-role iters) are explicit one-liners.
- **[MED] Request-timeout logic duplicated 3× and contradicts the runner's own default** — `app.go:448-450, :537, :582` set 60s; `agent.NewRunner` leaves it 0 and `effectiveRequestTimeout` already falls back to 5 min (`internal/agent/runner.go:48, :1733`). The `== 0` guard at `:448` is always true. Fix: pick one value, put it in one named constant (or delete and let the runner default apply).
- **[MED] Password echo leaks into the model-name input** — `onboarding.go:263-269`: both branches set `EchoPassword` (pure duplication) and it is never reset; for OpenAI/OpenRouter the subsequent `stateModelSelection` text input (`:292-300`) stays masked. Fix: hoist the assignment once and reset to `EchoNormal` when leaving `stateConfigureKey`.
- **[MED] `Run`'s `stderr` parameter is never used** — `app.go:668`. `app_test.go:989-1004` asserts nothing leaks to it, a vacuous test. Fix: drop the parameter (or actually route diagnostics through it).
- **[MED] Hand-rolled TOML in `saveConfig`** — `onboarding.go:341-406` uses `fmt.Sprintf` with Go `%q`, which emits `\xNN` escapes that are invalid TOML for control chars; meanwhile `internal/app/config/save.go:20,222` already marshals via `go-toml/v2`. Fix: build the file with the same toml library (small struct) instead of string concatenation.
- **[LOW] Duplicated type-assert-and-panic blocks** — `app.go:739-746` and `:822-825` repeat the JobBroker assertion verbatim; `mustDB` (`:52-61`) is a third instance. One generic `must[T any](raw any) T` helper removes all three. (The `BrokerCloser`/`MCPCloser` interfaces themselves are justified — `runtime_test.go:46-95` injects recording fakes.)
- **[LOW] Production behavior branches on `flag.Lookup("test.v")`** — `app.go:696`. Tests should pass `WithSkipOnboarding(true)` explicitly instead of the binary detecting the test harness.
- **[LOW] Over-elaborate deadline math in `Quiesce`** — `runtime.go:170-184`: the `time.Nanosecond` floor is unnecessary (`context.WithTimeout` handles non-positive durations). Collapse to a clamp: `timeout := min(time.Until(deadline), jobShutdownTimeout)`.
- **[LOW] `Close` irregularities** — `runtime.go:239-252`: snapshot DB/FS prune is skipped when `Logger == nil` (unrelated coupling); the doc comment (`:200-209`) omits the desktop step added at `:261-264`; `defer pruneCancel()` at `:247` needs its own apologetic comment — just call it after `Prune`.
- **[LOW] Onboarding role list already stale** — `onboarding.go:398` hardcodes 9 role strings; `routing/types.go:6-19` defines 14 (`title`, `subtask`, `sdd_*` missing). Works via preset fallback but will drift again — derive from routing constants.
- **[LOW] Dead/vestigial code** — `marshalGlobalAPIKey` constant and store never read (`onboarding.go:42, :282`); `_ = oldSnap` (`app.go:886`, `snapshot.Service` has no `Close`); `case "json"` duplicates default (`app.go:652-654`); `jobBrokerCtx := ctx` alias (`app.go:755`); test-only `attempts` field in the prod struct (`onboarding.go:84-88`); TODO cites nonexistent `docs/03-config-and-policy.md` (`onboarding.go:413-414`); provider identity compared via the display string `"Ollama (Local)"` in three places (`onboarding.go:106, :239, :355`).
- **[LOW] Test bloat** — `app_test.go` is 69 KB / ~2,300 lines / 46 test funcs, 2.2× the code under test; several `&Runtime{...}` setup blocks repeat verbatim (e.g. `:536, :648, :720, :751`).

## Quick wins

1. Delete the dead items above (`marshalGlobalAPIKey`, `_ = oldSnap`, `case "json"`, `jobBrokerCtx`) — pure removals.
2. Hoist the duplicated `EchoMode` assignment and reset it after key entry (fixes the masked-model bug too).
3. Retry `/api/tags` on non-200 in `fetchOllamaModels` (fixes the broken default Ollama path).
4. Extract `must[T]` and one `requestTimeout` constant; merge the nested `if` at `app.go:712-716` into `&&`.
5. Move `dbMemoryProvider.Memories` (`app.go:274-287`) next to its type declaration (`:219-221`) — currently separated by 50 lines of unrelated resolver code.


---

**Scope:** Package `internal/app/config/` — TOML config loading, defaults, merge rules, save.go (~1.5k LOC; config.go alone is 1161 lines).

I have all the evidence needed. Compiling the report.

---

# Audit: `internal/app/config/`

## 1. Scope summary

This package defines the `Config` tree, loads/merges user (`~/.config/marshal/config.toml`) and project (`.marshal/config.toml`) TOML over `Default()`, and persists settings edits back to TOML. The design (defaults → nullable-mirror structs → field-wise merge) is sound and the trust-gating in `Load` is careful, but every setting is represented 4–5 times across parallel structs, making the package brittle — and that brittleness has already produced one real bug (a dead security field). Verdict: mechanically correct but heavy; needs consolidation, not redesign.

## 2. Findings (by impact)

- **[HIGH] `MCPServerConfig.Trust` is dead — the F-SEC-06 escape hatch cannot work** — `config.go:101`. Commit e625486e added `Trust string \`toml:"trust"\`` to `MCPServerConfig`, and `internal/tools/mcp/manager.go:58,116` honors it, even advising users to `set trust = "unrestricted"` (manager.go:65). But TOML is decoded into `fileMCPServer` (`config.go:419-423`), which has **no Trust field**; `merge` (`config.go:996-1006`) never sets it; `SaveProjectConfig` drops it (`save.go:177`). go-toml's default mode ignores unknown keys, so the key is silently discarded. The `toml` tags on `MCPServerConfig` itself are non-load-bearing in both directions. Fix: add `Trust *string` to `fileMCPServer`, one line in merge, one in save — or delete the field if intentionally unsettable.

- **[HIGH] `Load` silently swallows user-config merge errors** — `config.go:656`. `merge(&cfg, userFile)` ignores the returned error; the project-file call two dozen lines later checks it (`config.go:680-682`). A typo in `background_retention` in the user file makes `merge` abort mid-file — remaining user settings silently unapplied, load "succeeds". Fix: check the error; also wrap merge errors with the source path ("parse background_retention" currently doesn't say which file).

- **[HIGH] Every setting lives in 4–5 places** — runtime struct (`config.go:203`), nullable mirror (`config.go:241`), `Default()` (`config.go:511-628`), 361-line `merge()` (`config.go:771-1131`), 215-line `SaveProjectConfig` (`save.go:20-234`). The Trust bug is the direct cost. Pragmatic cuts, no redesign: (a) a generic `func set[T any](dst *T, src *T)` collapses most of merge's 3-line stanzas to one (~-150 lines); (b) delete the pure adapters — `modelPresetConfig` (`config.go:46-56`) and `contextBudgetConfig` (`config.go:58-66`) are field-identical to `routing.ModelPreset`/`routing.ContextBudget` (routing/types.go:22-48), so toml tags on the routing types (free — tags add no dependency) delete `presetFromConfig`/`contextBudgetFromConfig` (744-769); (c) replace `agentProfileConfig` + the 12-if `profileFromConfig` (`config.go:496-509, 703-742`) with `map[routing.AgentRole]string` — `configFile.Agents` already decodes into `map[routing.AgentRole]...` (`config.go:493`).

- **[MED] `save.go` mixes two code generations** — `ptr[T]` exists at `save.go:15` and is used from line 115 on, but lines 32–105 still declare a local per field then take its address (~70 lines of noise). Line 44 is a ~230-char one-line struct literal. Mechanical cleanup.

- **[MED] Inconsistent, undocumented save-guard policies** — profile/agent/privacy/shell/sandbox write unconditionally (`save.go:32-111`); everything else uses `file.X != nil || !reflect.DeepEqual(cfg.X, def.X)` (`save.go:115-204`), with three styles (`!=` at 118/131/185, DeepEqual, `len()>0` at 192/205/208). Since callers pass merged user+project config (`tui/model.go:2281`, `settings/browser.go:410`), unconditional sections bake user-global values into the project file. At minimum add a code comment stating the two policies and why.

- **[MED] Path construction duplicated** — `Load` (`config.go:651,658`), `HasConfig` (`config.go:1152-1155`, which also re-implements Load's home/work resolution at 1133-1150), `tui/model.go:271` and `2626`, plus onboarding.go. Add an unexported `configPaths(home, work)` for Load/HasConfig; consider exporting for the TUI.

- **[LOW] Noise in `Default()`** — `PermissionsConfig{Rules: nil}` and `HooksConfig{..., Entries: nil}` (`config.go:617-623`) are zero-value no-ops; nil-vs-empty-map policy for maps is arbitrary (harmless, merge tolerates both).

- **[LOW] Validation asymmetry** — `validateProviderBaseURL` runs only on `SaveProjectConfig` (`save.go:26`), not on Load or `SaveUserConfigProviderAPIKey`; hand-edited bad URLs fail later and elsewhere. Also note (docs issue, not code): all three save functions round-trip through `configFile`, stripping comments from hand-edited TOML.

**Good shape:** no goroutines/concurrency concerns; error wrapping is otherwise consistent; tests (886+553 LOC) are repetitive in setup but conventional and not egregious.

## 3. Quick wins

1. Wire `Trust` through `fileMCPServer` + merge + save (~10 lines) — fixes a broken documented security feature.
2. Check `merge` error at `config.go:656` and add file-path context to duration-parse errors.
3. Add `set[T]` helper; compress `merge()` (~-150 lines).
4. Convert `save.go:32-105` to `ptr(...)`; split line 44.
5. Delete the three adapter structs/copy functions via toml tags on routing types + role-keyed profile map (~-100 lines).
6. Extract shared `configPaths` helper; drop the nil-valued blocks in `Default()`.


---

**Scope:** Package `internal/app/session/` — in-memory app state, message list, shutdown context; session.go is 1645 lines.

# Audit: `internal/app/session/`

**Scope summary.** `State` is the in-memory hub for a chat session: message tree (append-only with branches/rewind), streaming/thinking buffer, pending approvals/questions, steering queue, pub/sub event surface, shutdown work-gate, plus ~20 small feature blobs (todos, skills, backups, SDD/swarm progress, browser, route, budgets). Concurrency discipline (one mutex, snapshot-then-publish after unlock) is consistent and the smaller files (`sdd_progress.go`, `swarm_progress.go`) are clean. The core message-persistence path, however, is genuinely fragile, and the State struct is a god object accumulated from every past milestone.

## Findings (by impact)

- **[HIGH] `appendMessage` ID promotion is racy and overcomplicated** — session.go:1036-1073. It stashes `ptr := &s.messages[len(s.messages)-1]`, unlocks, does a synchronous DB write, re-locks, then mutates `ptr.ID` and re-keys `msgByID`/`parentOf`/`childrenOf`. Three concrete problems: (1) `EventMessageAdded` (1042) publishes a transient in-memory ID that is deleted from the tree milliseconds later — ACP forwards that ID to clients; (2) a concurrent `Rewind`/`SwitchBranch` between unlock and re-lock replaces `s.messages` with copies, leaving `ptr` pointing into an orphaned array; (3) two concurrent appends interleave transient/DB ids so `parentOf` can dangle. Fix: persist first, then insert into the tree once with the final ID (or hold `s.mu` across the DB write — writes are per-session serialized anyway). That deletes the whole re-key block at 1055-1071.
- **[HIGH] Dual ID space in the message tree** — session.go:406-416, 555-601, 1007-1073. Tree keys are sometimes transient IDs (1..N), sometimes DB IDs after promotion, with a `dbIDToImID` translation map (initialized :500) used only inside `loadFromDB` yet kept for the session's lifetime. This is the root cause of the finding above and makes `Message.ID`'s meaning time-dependent. Fix: pick one ID strategy (DB ID when persisted, counter otherwise), and nil out `dbIDToImID` after load.
- **[MED] Dead speculative event fields** — session.go:52-54, 63-64, 197-200, 235-244, 1279-1288, 1304-1311. `Event.PendingApprovalInfo` / `Event.PendingQuestionInfo` are read only by this package's own tests (session_test.go:1269, 1321); the documented consumer (TUI) calls `PendingApproval()` directly and ACP uses the full pointer (acp/turn.go:248-250). `PendingQuestionInfo.ID` is never populated anywhere. Remove both Event fields, `PendingQuestionInfo`, and the snapshot-building in the setters (~50 lines).
- **[MED] God-object file** — session.go:336-417 declares ~40 fields from at least eight distinct feature eras (F14 tree, F16 steering, F21 events, Milestone P subagents, Task 5 work gate, backups, skills, todos…). The package already demonstrates the fix: progress state lives in `sdd_progress.go`/`swarm_progress.go`. Pragmatic step: pure file moves — message-tree+persistence (482-601, 990-1187), streaming (1189-1232), pending approval/question (1271-1344), steering (701-786) into topic files. No behavior change.
- **[MED] Duplicated shutdown-answer logic** — session.go:951-956 duplicates acp/turn.go:275-278 verbatim (build `AnswerUnanswered` per question). Export `UnansweredAnswers([]Question) []Answer` from session; use in both.
- **[MED] `SetTodos` returns an always-nil error** — session.go:1413-1424. Persistence failure is Warn-logged and swallowed; both callers (internal/tools/native/todos.go:63, tests) check the error pointlessly. Drop the return value.
- **[MED] `DeactivateSkill` is test-only** — session.go:1592. No production caller (runner only activates; internal/skills/tool.go uses `HasActiveSkill`). Wire it or delete it.
- **[LOW] Steering publish boilerplate ×4** — session.go:705-770. Push/Drain/Pop/Clear repeat lock→snapshot→unlock→publish; `DrainSteering` (731) and `ClearSteering` (765) compute `queueLen := len(...)` *after* nil-ing the queue, so it's always 0 — correct outcome, reads like a bug. Factor one helper.
- **[LOW] `ClearMessages` doc misleads** — session.go:1178-1187. It clears only `s.messages`; tree maps, `leafID`, `nextMsgID` survive, so `/new` (commands.go:31) silently forks off the old leaf and `Branches()` still reports stale tips. If intentional per F14, say so in the comment.
- **[LOW] Duplicated `Snapshotter` interface** — session.go:70-75 identical to internal/agent/runner.go:79-84; agent already imports session, so alias it.
- **[LOW] `persistenceEnabled()` requires `logger != nil`** — session.go:986-988: DB+SessionID without logger silently disables persistence (runtime_test.go:209 relies on this). Decouple or document loudly.
- **[LOW] Nits** — `sort` import in its own group (session.go:13); stale comment `Diff string // Added field for patch rendering` (253); `WaitForWork` goroutine per call (896-908) lingers if ctx cancels first (bounded, acceptable).

**Good shape:** swarm/sdd progress files, the Respond once/non-blocking pattern (production uses buffered(1) channels, runner.go:1667/1710), consistent publish-after-unlock discipline, and the test file — 1355 lines but focused, no egregious bloat.

## Quick wins

1. Delete `PendingApprovalInfo`/`PendingQuestionInfo` event fields and structs (~50 lines, zero consumers).
2. Drop `SetTodos`'s error return; delete `DeactivateSkill` (or wire it).
3. Nil `dbIDToImID` after `loadFromDB`; merge the `sort` import; remove the stale `Diff` comment.
4. Extract `UnansweredAnswers` helper shared with acp/turn.go; extract one steering-publish helper.
5. Fix `ClearMessages` doc comment to state the branch/tree survives.


---

**Scope:** Package `internal/app/tui/` ROOT files only (main Bubble Tea model): `model.go` (2657 lines — the biggest file in the repo), `transcript.go` (622), `view.go`, `approval.go`, `completions.go`, and any other .go files directly in that directory. Do NOT audit subdirectories (settings, memory, picker, theme, connect, etc.) — other agents cover those.

I have everything I need. Compiling the report.

---

## Audit: `internal/app/tui/` root (Bubble Tea model layer)

**Scope summary.** This is the main TUI model: `Model` + `Update`/`View` routing, transcript/markdown rendering, status line, completion popups, approval/question huh forms, and the dock/picker plumbing. Overall health is mediocre: individual renderers are clean and well-commented, but `model.go` is a genuine god-object with heavy copy-paste in the config-save paths, unreachable code, and one latent theming bug. Small files (`pump.go`, `events.go`, `spinner.go`, `todos.go`, `browserbar.go`, `question.go`, both panels) are in good shape — no changes needed there.

### Findings

- **[HIGH] Package-level styles capture nil theme colors permanently** — `transcript.go:246,319,321,394`, `status.go:193-196`, `approval.go:18`. Nine `var xStyle = lipgloss.NewStyle().Foreground(accentColor/warningColor/…)` initialize at package load, when those color vars are still nil (`loadTheme` runs later in `New()`, model.go:2522, and never rebuilds the styles). Verified against lipgloss v2.0.5: `set()` stores nil → no color is ever emitted, so `toolBulletStyle` renders completely plain and the warn/error styles are bold-only. It also contradicts the file's own lazy pattern (`mutedStyle()` etc., model.go:2543) and hides a triplicate of the same warn style (`statusWarnStyle`/`queuedStyle`/`warningStyle`). Fix: convert to lazy funcs reading `theme.Current()` (pattern already in the file) and collapse the three warn copies into one.
- **[HIGH] Persist→reload→apply config dance duplicated 4×** — `model.go:344-376` (`/set`), `590-634` (`settings.ChangedMsg`), `2278-2305` (`applyConnectDone`), `2331-2361` (`switchModelPreset`). ~100 lines with verbatim comments ("Keep the in-memory change so the user can correct…", "The runtime has already swapped cfg before cleanup can fail") — proven drift hazard. Fix: one helper, e.g. `m.persistAndReload(newCfg, okMsg, errPrefix string)`, encapsulating save → `configSavePending` → `setReg=nil` → reloader → `applyNewConfig`.
- **[HIGH] Unreachable cases in `Update`** — `model.go:793-806`. `tea.WindowSizeMsg` is handled at 577 and the five runtime messages are intercepted at 661-664, so the whole tail switch's non-key cases are dead (the 794 comment even admits it for one case). Delete them.
- **[HIGH] Hand-rolled bubble sort per keystroke** — `completions.go:144-151`. O(h²) swap loop over fuzzy hits; with a few-thousand-file index and a short query this is millions of swaps on every keypress. Fix: `slices.SortFunc` (already imported elsewhere) — a 5-line change.

- **[MED] `Update` is a 429-line god function** — `model.go:567-995`, with six routing layers. The keypress switch alone (807-979) is a self-contained unit; extract `handleKeypress` and the Enter-submit block. Incremental, no behavior change.
- **[MED] Run-dispatch block copy-pasted 3×** — `model.go:969-978` (Enter), `2015-2024` (`/swarm`), `2041-2050` (`/sdd`): identical `BeginWork`/`busy`/`WithCancel`/`tea.Batch(runAgentCmd…, tickCmd(), spinnerTickCmd())`. Extract `startAgentRun(runner AgentRunner, goal string) (tea.Cmd, bool)`.
- **[MED] `approvalModel` fights huh** — `approval.go:36-185`: shadows huh's select with parallel `candidates`/`selected` state, a two-step Enter (`submitPending`), and a 1.5s double-Esc timer (using `time.Now()` directly, bypassing the model's injectable `m.now`). The keymap is half-disabled to make this work. Pragmatic simplification: let huh navigate/submit natively and keep only the Esc-deny interception; that deletes ~60 lines of shadow state. (Behavior change — confirm with owner.)
- **[MED] SDD panel render-time cache couples View to layout math** — `model.go:83-87,1224-1236`, `view.go:67-73`: `sddPanelBody`/`sddPanelCachedRows` mutate during `View()` so `sddPanelRows()` (called from `resize`) reads a stale-or-fresh cache. `renderSDDPanel` is cheap string-building with no glamour; just call it in both places and delete the cache fields.
- **[MED] "Show all items" block pasted 3×** — `model.go:1304-1311`, `1335-1341`, `1384-1390`. Identical 6-line popup reset; add `(*completionPopup).showAll()`. Same file: `fuzzyScore` then `fuzzyMatchIndices` scans each item twice (`completions.go:135-137`) — merge into one pass.

- **[LOW] Dead/vestigial code** — `tailRunes` unused (`model.go:2487`); `thinkingBoxTailLines` unused const, `renderThinkingBox` hardcodes 3 (`transcript.go:169,182-184`); `openConnect(_ string)` ignored param (`model.go:2090`); `transcriptFrameRows = 0` still subtracted in two places (`view.go:28`, `model.go:564,1243`); `renderQueuedMessages` ignores `width` via `_ = width` (`transcript.go:331`).
- **[LOW] Readability nits** — builtin `max` shadowed by locals (`view.go:259`, `completions.go:193`); magic `8` instead of `completionPopupMax` (`model.go:1198`); status-priority doc drift (`status.go:92-96` omits SDD task=1 colliding with route=1 and browser=9); convoluted `if n := m.jobCount; m.jobBroker != nil` (`status.go:157`); `flags()` bit-packs values that are then `Fprintf`'d anyway (`model.go:2614-2623`); `ShouldShowStatusURL` exported but used only in-package (`model.go:1220`); `modeSwitchMessage`/`cycleMode` duplicate strings already in the commands registry — `cycleMode` could just `dispatchCommand("/"+next)` (`model.go:2173-2199`); `commandTrigger`'s space-retrigger reopens the full command list mid-args (`model.go:1398-1422`, self-confessed plan deviation).

### Quick wins

1. Lazy-func the 9 package styles (fixes invisible warning/error colors today) — ~30 lines, mechanical.
2. Delete dead cases in `Update` (model.go:793-806) and `tailRunes`, `thinkingBoxTailLines`.
3. `slices.SortFunc` in `completions.go:144`.
4. `persistAndReload` helper for the 4 config-save copies.
5. `popup.showAll()` + `completionPopupMax` in model.go:1198.
6. Tests: `model_test.go` is 4,728 lines / 158 tests with the setup line `session.New(config.Default(), "/repo", …)` repeated 53× while `newTestModel` (line 4000) is used by only 34 tests — route new tests through the helper and move it up top.


---

**Scope:** Package `internal/app/tui/settings/` — settings UI: frames_collections.go (586 lines), fieldlist.go (571), browser.go (479), panestack.go (257), plus remaining files.

I have everything I need. Compiling the report.

---

# Audit: `internal/app/tui/settings/`

**Scope summary.** This package implements the docked settings browser: a flat fuzzy-filterable registry of ~60 leaf fields (`registry.go`, `browser.go`), drill-down collection frames (`panestack.go`, `frames_*.go`) rendered/edited by one widget (`fieldlist.go`), with immediate per-keystroke persistence and reflect-based diff receipts (`configdiff.go`). The core field/fieldList/frame layering is genuinely good — but the package carries debris from two abandoned designs (a full-screen huh-form model and a Ctrl+S single-transaction model), plus copy-paste duplication in the collection frames.

## Findings (by impact)

1. **[HIGH] Dead code from the abandoned full-screen design** — `validation.go:9` `warningsFor` has zero production callers (only its own 81-line test and stale plan docs reference it); delete file+test. `state.snapshot` (state.go:16,31) is write-only — superseded by `BrowserPanel.baseline`. `actionState.pending` (state.go:23) is write-only in prod (only tests read it; `actLabel`/`act` consult `label` only). `fieldList.yankedID` (fieldlist.go:76,224,234) is write-only. `frame.keyPrompt/onAdd/addWizard` (field.go:80-82) are dead duplicates: `newCollectionFrame` (panestack.go:18-21) and frames_collections.go:131-132 copy them into `list.*`, and production only reads the `list` copies (browser.go:156, fieldlist.go:202). `paneStack.atRoot` (panestack.go:36) is unused; `depth()` is test-only.

2. **[HIGH] Stale comments describe the scrapped transaction model** — field.go:21-23 claims edits are "a single transaction guarded by Ctrl+S / double-Esc"; in fact every mutation saves immediately (browser.go:23, `flushChanges`). reset.go:53 promises reset is "undoable until save" — saves are immediate, no undo exists. field.go:75-76 says "Stack management lives in pane.go — Task 4" (file is panestack.go) and fieldlist.go:61 references "Task 4" — plan-doc references, not code facts. state.go:10-13 documents the dead snapshot. Rewrite these to match current behavior.

3. **[MED] `resetSection` silently no-ops for "interface"** — the switch (reset.go:10-44) covers 15 of the 16 sections in `sectionList()` (sections.go:26); `resetFields` (browser.go:182-191) offers a reset row for *every* section, so resetting Interface shows "✓ reset" but never touches `TUI.Theme/Mode`. Related inconsistency: `commandsFrame` hosts `project.name`/`project.languages` (frames_basic.go:57-60) which "Reset Commands" doesn't reset. Fix: add the `interface` case (or exclude it from `resetFields`), and move project fields or extend the commands reset.

4. **[MED] Triplicated slice-insert helpers** — `insertHook` (frames_collections.go:280-292), `insertRule` (frames_collections.go:574-586), `insertString` (frames_basic.go:161-173) are byte-identical generic logic. Go 1.26 stdlib `slices.Insert` replaces all three; callers already pass in-range `i+1`, so the clamping is unnecessary. Delete ~30 lines.

5. **[MED] hooks/permissions frames are near-duplicates** — frames_collections.go:391-466 and 496-572 repeat the same ~75 lines of sliceKeys/`strconv.Atoi`/moveUp/moveDown/yank/paste scaffolding, differing only in slice and row fields; `listStringOpts` (frames_basic.go:126-159) is a third copy of the move/yank/paste plumbing. One generic `sliceEntriesDrill[T]` (or a shared opts builder) removes ~100 lines without new abstraction beyond what `mapDrill` already pioneers.

6. **[MED] Section roots rebuilt on every refresh** — `collectionFields` (browser.go:152-178) calls `spec.root(b.reg.st)` for all 16 sections on every `matchedFields` call; `matchedFields` runs on every `fieldList.Refresh`, and `View` triggers ≥2 refreshes, plus one more root build per rendered collection row via `summary` (browser.go:170). Each root build allocates a fieldList with two `textinput.Model`s plus closure trees — hundreds of allocations per keystroke for nothing, since the roots are pure closures over `*state`. Cache the 16 roots once (registry or browser construction) and reuse; precompute the static "has add" set.

7. **[MED] Dead/triplicated branch in `configdiff.go`** — the `b.Kind() != a.Kind()` arm (lines 78-103) is unreachable: before/after are the same static type at every path; only absent (invalid) sides occur, already handled at 36-77. Those two blocks are themselves near-duplicates — collapse all three into one `emitSide(path, v, prefix)` helper and drop the mismatch arm.

8. **[LOW] Missed reuse** — the provider `api_key` field (frames_collections.go:87-99) hand-duplicates `secretRow` (setters.go:72-81), identical desc/keywords included, differing only by `invalidate()` calls; give `secretRow` a post-set hook. `NewBrowser`'s mono check (browser.go:58) duplicates `isMono()` (fieldlist.go:14). The yank/paste closures (frames_collections.go:113-128, 362-377) duplicate the "existing-set + uniqueCopyName + assert" pattern.

9. **[LOW] Misleading paste error** — type-mismatched paste (e.g. provider into hooks) reports "nothing yanked" (frames_collections.go:116,364,458,564) when something *was* yanked elsewhere. Say "incompatible yanked item".

10. **[LOW] Fragile index-keyed drill frames** — hooks/permissions entry frames capture slice index `i` (frames_collections.go:404, 511); deleting/reordering an earlier entry while drilled in silently retargets edits to the wrong row. browser.go:394-398 shows the authors know; re-resolving the index per refresh (or keying by a stable id) would close it.

## Quick wins

- Delete `validation.go`(+test), `state.snapshot`, `actionState.pending`, `yankedID`, `atRoot`, and the `frame`-level `keyPrompt/onAdd/addWizard` copies (~150 lines of dead weight, zero behavior change).
- Replace the three insert helpers with `slices.Insert` (~30 lines gone).
- Fix the stale comments in field.go, fieldlist.go, reset.go, state.go.
- Add the `interface` case to `resetSection` (or filter it from `resetFields`).
- Use `isMono()` in `NewBrowser`; route the provider API-key field through `secretRow`.
- Cache section roots in `NewBrowser` — one-line-per-call-site change with real allocation savings.


---

**Scope:** The remaining `internal/app/tui/` subpackages: `connect/` (connect.go 499 lines), `memory/`, `picker/`, `fuzzy/`, `theme/`, `chrome/`, `huhtheme/`, `probe/`, `help/`, `dock/`. Assess each briefly and deep-dive the worst offenders.

All verification done. Writing the report.

---

## Audit: `internal/app/tui/{connect,memory,picker,fuzzy,theme,chrome,huhtheme,probe,help,dock}`

**Scope summary.** These packages implement the shared TUI infrastructure: the provider-connect wizard (`connect`), docked memory browser (`memory`), generic filter-as-you-type modal (`picker` + `fuzzy`), color themes (`theme`, `huhtheme`), panel chrome (`chrome`), provider probing (`probe`), footer hints (`help`), and the dock host (`dock`). Overall health is decent — `fuzzy`, `dock`, `chrome`, `probe`, `help` are small and clean, and tests are proportional. The real problems are concentrated in `connect` (a busy-loop bug plus dead code) and cross-package duplication of row/panel layout that `chrome` was created to solve but only half-absorbed.

### Findings (by impact)

1. **[HIGH] Spinner tick is an unthrottled busy loop** — `connect/connect.go:202-206`. `tick()` returns a cmd that produces `TickMsg` *immediately*, with no delay; `Update` re-arms it on every tick while probing (`connect.go:107-112`), started at `connect.go:455`. During each probe (up to 5s, `probe/probe.go:15`) the UI spins a core re-rendering as fast as bubbletea can schedule. Fix: `tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return TickMsg{} })`.

2. **[HIGH] Bogus `"Catppuccin Mocha"` preset alias leaks into the settings UI** — `theme/presets.go:146`. The map mixes a display-name key into slug-cased keys; `theme.Names()` feeds the settings Theme enum (`settings/frames_tui.go:10`), so users see a duplicate entry, and selecting it with Mode=light looks up `"Catppuccin Mocha-light"`, misses, and silently falls back to warm-sunset (`theme/theme.go:184-187`). Fix: delete the alias; normalize any stored config value at load if needed.

3. **[MED] `huhtheme` ignores the configured theme** — `huhtheme/theme.go:24` calls `theme.Load()` (hardcoded warm-sunset dark) inside the closure, bypassing the configured preset/mode/overrides applied at `model.go:2523` and published via `theme.Reload` (`model.go:2539`). Approval/question forms render in the wrong palette for any non-default theme. Fix: `th := theme.Current()` — still lazy, and actually correct.

4. **[MED] Row/panel layout math duplicated 3×** — the marker + label + gap + right-aligned-detail rendering in `picker/picker.go:184-205` is near-identical to `memory/panel.go:171-196`; the `min(64, w-8)` width clamp appears at `connect.go:143-146`, `memory/panel.go:155-159`, `picker.go:163-166` with three different constants. Fix: add `chrome.Row(marker, label, right string, inner int, selected bool)` and `chrome.PanelWidth(maxW int)` and call them from all three.

5. **[MED] Fragmented dock-close protocol** — `dock.CloseMsg`/`dock.Close()` (`dock/dock.go:15-19`) have zero callers; panels invented per-package twins (`memory.ClosedMsg`, `messages.go:15`; `settings.BrowserClosedMsg`) each separately cased in `model.go:635-655`. Fix: either converge panels on `dock.CloseMsg` or delete the dead one — one line either way.

6. **[MED] `renderInput` byte-truncates a styled line** — `connect.go:176-178` (`line[:inner]`) can split an ANSI escape or UTF-8 rune and corrupt the panel; every other renderer uses `ansi.Truncate`. Fix: `ansi.Truncate(line, inner, "…")`.

7. **[LOW] Dead code in `connect`** — `successStyle` (`connect.go:26-28`) never called; `pendingSave` (`:69`) never referenced; `probeErr` (`:61,358,459`) written, never read; `nowNanos` test hook (`:497-499`) never overridden by any test; `back()`'s `stepPickModel` branch (`:219-225`) unreachable because `picker.CancelledMsg` at pick steps routes to `cancel()` (`:101-104`); `enterPickModel` (`:440-442`) is a one-line wrapper used once. Fix: delete all six.

8. **[LOW] Inconsistent step-entering style in `connect`** — `enterBaseURLStep`, `enterPickModelStep`, `buildModelPicker` are package-level funcs taking `*Model` (`connect.go:271-318`) while their siblings are methods. Make them methods; costs nothing.

9. **[LOW] Theme tier detection duplicated; 16-color path silently ignores presets** — the TERM check is copy-pasted (`theme.go:147` vs `:190`); on non-256 terminals `LoadWithConfig` swaps *any* chosen preset to `warmSunset16` (`:189-192`), so dracula/nord/catppuccin/light selections are silently ignored (no 16-color variants exist). Fix: extract `supports256Color(term string)`; document the limitation or drop the pretense of preset choice on 16-color terminals.

10. **[LOW] `memory` panel re-implements mono handling and mutates state in `View`** — `memory/panel.go:166-167,225-267`. `syncFilterStyles` rebuilds an empty `textinput.Styles` every frame inside `View`; the `isMono` branches mostly duplicate what `NoColor` slots already give every other panel. Fix: set styles once in `NewPanel`; keep only the virtual-cursor disable.

11. **[LOW] `fuzzy.isSubsequence` byte/rune mismatch** — `fuzzy/fuzzy.go:34` compares a `rune` from hay against `needle[i]` (a byte); non-ASCII queries never subsequence-match. Also the package doc (`:1-2`) omits the memory browser. Fix: iterate needle runes; update comment.

12. **[LOW] `help` footer shows a confusing duplicate** — `help/help.go:51-52` renders both "Enter arm" and "Enter⏎ submit"; the doc comment promises "3-5" hints but the idle branch can show 7-8 (`:67-83`). Fix the wording/comment.

13. **[LOW] Defensive dead branches in `chrome`** — `ClipLines`'s `len(out) > height` trim (`chrome.go:101-103`) is unreachable by construction; the focused-mono title branch (`:29-33`) is a no-op since both colors are `NoColor`. Harmless; delete or comment.

### Quick wins

- Replace `tick()` with `tea.Tick(100ms, …)` — one line, kills a CPU spin (#1).
- Delete the `"Catppuccin Mocha"` map entry (#2) and the six dead items in `connect` (#7).
- `theme.Load()` → `theme.Current()` in `huhtheme` (#3).
- `ansi.Truncate` in `renderInput` (#6); `time.Since` instead of `time.Now().Sub` (`connect.go:183`).
- Move `ctrl+d` into the key switch in `memory/panel.go:60-83`; extract `chrome.Row`/`chrome.PanelWidth` next time either file is touched (#4).


---

**Scope:** Package `internal/agent/` ROOT files only: `runner.go` (1806 lines, single-agent loop), `prompts.go` (380), and other .go files directly in that dir. Do NOT audit swarm/, sdd/, agenttest/ — other agents cover those.

I have a complete picture now. Here is my report.

---

## Audit: `internal/agent/` root files

### 1. Scope summary
`runner.go` drives the single-agent turn (classify → prompt → model loop → parse/execute actions → finalize), supported by small helpers: `prompts.go` (system-prompt builders), `protocol.go` (JSON action envelope), `progress.go` (loop/stall detection), `finalize.go` (salvaged answers), `compact.go`, `metrics.go`, `subagent.go`, `title.go`, `handoff.go`, `atfile.go`, etc. Overall health is mixed: the satellite files are mostly small, focused, and well-commented, but `runner.go` itself is a 1,806-line accumulation point with a 465-line `RunTask`, heavy intra-loop duplication, stale comments, and one shadowing hazard; there is also outright dead code left from an abandoned compaction direction.

### 2. Findings (by impact)

- **[HIGH] Dead compaction machinery** — `compact.go:22-90`, `runner.go:33`, `prompts.go:315-317` — Since commit `4dec97e5` ("terminate turn on summarization failure instead of lossy compaction"), `compactMessages` has no production caller; the overflow path now ends the turn (`runner.go:506-514`). Only `compact_test.go` (179 lines) references it. `compactKeepRecentMessages` is an unused const, and the "load-bearing marker: compactMessages identifies tool results by this prefix" comment is now false. Fix: delete `compact.go` + `compact_test.go` + the const, and reword the `BuildToolResultMessage` comment. ~270 lines gone.

- **[HIGH] Stall-handling block duplicated 6×** — `runner.go:542-548, 595-601, 658-664, 686-692, 715-721, 755-761` — The identical 7-line `if !steeringArrived { if finalized, res, ferr, nudge := r.maybeFinalizeOnStall(...); finalized { return ... } else if nudge != "" { ... } }` pattern is pasted into every loop branch (already drifting: `res` vs `resTask`). Any change to stall policy needs six edits. Fix: one helper, e.g. `messages, done, task, err := r.checkStall(ctx, p, model, messages, task, rf, steeringArrived)`, called six times. Removes ~40 lines and most of `RunTask`'s noise.

- **[MED] Variable shadowing across the hook-rewrite loop** — `runner.go:1255-1267` vs `1303` — Loop-scope `argsMap, normalizedArgs, parseErr := parseToolArgs(args)` shadows the function-scope `argsMap`/`normalizedArgs` declared at 1255/1263. After the loop, the label (1360), snapshot file list (`changedFilesForTool`, 1373), turn-cache key (`SetTurnToolResult`, 1412), and tracker signature (1422) all silently use the **pre-rewrite** values while `tool.Handler` executes the rewritten `args` (1392-1393). A rewrite that changes a patch's target file would snapshot/cache/track the wrong thing. Fix: hoist `parseToolArgs` above the cache check (deleting the duplicated inline parse at 1255-1267), and after the loop re-derive `argsMap`/`normalizedArgs` from the final `args`.

- **[MED] `RunTask` is a 465-line flag-fielded mega-loop** — `runner.go:330-795` — Envelope and native paths, steering drain, pressure message, skills re-render, overflow summarize, parse-failure escalation, and two ask-user paths all inline, with six local booleans/counters. Pragmatic shrink: extract the stall helper (above) and a `r.bumpIteration(&iteration)` for the `iteration++; withStats(...Iterations...)` pair repeated 8× (533, 565, 579, 627, 708, 741, 1501, 1533). No restructuring beyond that is warranted.

- **[MED] `SoftStalls` is a dead metric plumbed through three packages** — `metrics.go:24`, `internal/db/turnmetrics.go:28,62,110`, `internal/app/app.go:309` — Nothing ever increments it (`progress.go` has only `assessProgressing`/`assessHardStall`; plan docs show a `SoftStalls++` that was never landed). Stored, mapped, and asserted on (`eval_scenarios_test.go:74`) yet always 0. Fix: delete the field end-to-end, or add the intended increment.

- **[MED] Stale concurrency-doc comment contradicts code** — `runner.go:139-142` says `resolveRoute` "may grow MaxTurnContextTokens (monotonically)"; the F-SEC-10 logic at 887-895 now *shrinks* it to the smaller of config and model window. Anyone trusting the struct doc will misread the invariant. Fix: reword two lines.

- **[LOW] Unused parameter** — `subagent.go:97` — `NewSubagentTool(..., maxConcurrent int, ...)` never uses `maxConcurrent` ("recorded for documentation"), while the tool description hardcodes "Maximum concurrency: 2". Fix: drop the param at the 6 call sites.

- **[LOW] Byte-based title truncation + duplicate truncate helpers** — `title.go:66-68` `title[:titleMaxChars]` can split a UTF-8 rune; the package already has rune-aware `truncateRunes` (`runner.go:1758`) and a near-duplicate `truncateGoal` (`metrics.go:41`). Fix: one rune-aware helper used in all three places.

- **[LOW] Variadic-optional bools** — `prompts.go:182` (`nativeToolsOpt ...bool`), `runner.go:968` (`includeNativeToolsOpt ...bool`) — every caller passes an explicit bool; plain parameters are clearer. Also `BuildSystemPrompt` (`prompts.go:166`) is production-dead (only tests use it; production uses `BuildSystemPromptWithDeferred`) — merge or unexport.

### 3. Quick wins
- Delete `compact.go`/`compact_test.go`, `compactKeepRecentMessages`, and fix the stale `prompts.go:315` comment (finding 1).
- Reword the `resolveRoute` doc lines (`runner.go:139-142`) to match F-SEC-10 behavior.
- Remove `maxConcurrent` from `NewSubagentTool`.
- Replace `oneLineDescription` (`prompts.go:274-294`) with ~3 lines using `strings.Cut` — its `if b.Len() == 0 { continue }` branch is unreachable after `TrimSpace`.
- Drop the redundant `var err error` at `finalize.go:84`.
- Wrap the 11 `trackerMu.Lock/Unlock` sandwiches in `runner.go` in small `recordCall`/`recordIdle` Runner methods mirroring `withStats`.

Not flagged (candidly in good shape): `protocol.go`, `progress.go`, `history.go`, `spill.go`, `summarize.go`, `handoff.go`, `finalize.go`, `classify.go`, `toolargs.go`, `file_index_cache.go` are all small, single-purpose, and accurately commented; `prompts.go`'s role table is clear. The runner test files are large (~3.3k lines across five files) but not egregiously duplicated.


---

**Scope:** Packages `internal/agent/swarm/` (swarm orchestration, lock, state, verdict; orchestrator.go 305 lines) and `internal/agent/sdd/` (orchestrator.go 353). Also glance at `internal/agent/agenttest/` for test-helper reuse.

I have everything I need. Here is the audit report.

## Scope summary

`internal/agent/swarm` runs a fixed pipeline (planner → parallel scouts → implementer/tester fix loop → reviewer) over a mutex-guarded `TaskState` blackboard, with an estimate-based token budget; `internal/agent/sdd` executes a markdown plan task-by-task (implementer → reviewer fix loop → branch-review merge gate) with a file ledger for resume and optional git worktree isolation. Core files (`state.go`, `lock.go`, `verdict.go`, `plan.go`, both `prompts.go`) are small, focused, and well-commented — genuinely good shape. The problems are dead surface area left from earlier milestones, systematic error swallowing in sdd, and duplicated orchestrator/test scaffolding between the two packages.

## Findings

- **[MED] Dead config knob `SDD.MaxTotalTokens`** — `config.go:87`, plumbed through file schema (`config.go:415`), merge (`:989`), save (`save.go:170`), defaults (`:590`), and round-trip tests — but `sdd.Orchestrator` never reads it (no meter exists in sdd at all). Users can set a budget that does nothing. Fix: delete the field and its plumbing, or wire a `swarm.TokenMeter` into sdd (deleting is the honest minimal move; the swarm meter already exists if the feature is wanted later).

- **[MED] Systematic error swallowing in sdd orchestrator** — `sdd/orchestrator.go:133,155` (`WriteBranchReviewPackage`), `:180` (`WriteTaskBrief`), `:221` (`WriteReviewPackage`), `:160` (re-review `runRole`), `:78-79` (`NewWorkspace`/`Ensure` after worktree switch). Combined with `gitMergeBase` falling back to HEAD (`:300-306`), a git failure yields `base==head` → empty diff written → reviewer grades an empty diff and may announce "✅ ready to merge". Fix: check these errors; at minimum announce and skip the review when `diffPath == ""`.

- **[MED] Branch-fix prompt built from zero values** — `sdd/orchestrator.go:149`: `BuildFixPrompt("", branchTask.Summary, PlanTask{})` produces "fixing issues … for Task 0: " and "Read your previous report: \n\n" — a malformed dispatch prompt. Fix: add a small `BuildBranchFixPrompt(findings string)` instead of reusing the per-task builder with junk args.

- **[MED] `parseImplementerStatus` substring-matches the whole summary** — `sdd/orchestrator.go:332-340`: a BLOCKED summary containing "not done" uppercases to contain "DONE" and returns DONE (DONE is checked before BLOCKED). Fix: parse the `Status:` line the prompt already mandates (`prompts.go:26`), or at least check BLOCKED/NEEDS_CONTEXT before DONE.

- **[MED] Dead code from earlier milestones** — `Worktree.MergeBase` (`worktree.go:28-34`): only caller is its own test. `parseLedgerLine`'s SHA extraction (`ledger.go:96-112`, half the function): production only uses `TaskNumber` via `CompletedTasks`; BaseSHA/HeadSHA are parsed and never read. `TaskState.TestFailures()`/`FinalSummary()` (`swarm/state.go:73,91`): zero callers anywhere. `WriteBranchReviewPackage` (`workspace.go:94-96`): pure passthrough adding a second name for one method. `swarm.Orchestrator.NewMeter` (`orchestrator.go:45`): the only implementation is `EstimateMeter` (ProviderUsageMeter was dropped in 11fe140c), and `app.go:559` sets the field to exactly the built-in default — speculative generality. Fix: delete all of the above; inline `NewEstimateMeter()`.

- **[MED] swarm↔sdd orchestrator duplication** — identical `RunnerFactory` signatures (`swarm/orchestrator.go:30`, `sdd/orchestrator.go:17` — the sdd comment even references the swarm contract), identical `announce` (`swarm:269-271`, `sdd:286-288`), identical no-op `SetForceClass`/`SetPolicyRules` pairs, near-identical `runRole`. Fix: make `sdd.RunnerFactory` a type alias (`= swarm.RunnerFactory`); leave the 3-line methods alone (a shared package would be over-engineering). Also note inconsistent defaults: swarm `maxRounds()` defaults to 1 (`:57-62`), sdd to 3 (`:33-36`), and sdd keeps both `Cfg.MaxFixRounds` and a copied `MaxFixRounds` field — two sources of truth.

- **[MED] Test scaffolding divergence and bloat (swarm tests)** — sdd tests stub via `Runner.RunTaskFunc` (`sdd/orchestrator_test.go:19-28`, clean); swarm tests hand-encode JSON protocol responses with `strings.ReplaceAll(summary, "\n", "\\n")` (`swarm/orchestrator_test.go:55` — breaks on any `"` in a summary), carry two overlapping factory helpers (`:31` vs `:79`), and `usageScriptedProvider` (`:468-489`) re-implements what `agenttest.ScriptedProvider.Usages` already does — the exact reuse agenttest was extracted for (f03932e1). Fix: migrate swarm tests to `RunTaskFunc` + `agenttest.Usages`; ~150 lines and the escaping brittleness disappear.

- **[LOW] Redundant `overBudget` check** — `swarm/orchestrator.go:187-189`: nothing between the check at `:181` and this one touches the meter (only `AddPatchNote`/`UpdateSwarmRole`), so it can never fire. Delete.

- **[LOW] Stale comment in `meter.go:26-33`** — claims "the provider layer does not yet surface real token-usage reporting", but `UsageObserver` exists and is wired (`orchestrator.go:127,242`; tested at `:425`). Rewrite or drop.

- **[LOW] `hasRealUsage *bool` indirection** — `swarm/orchestrator.go:116-155`: a pointer-to-bool threaded through `scoutJob` because the observer closure is created before the job is appended. Set the observer after append capturing `jobs[i]` and use a plain bool field.

- **[LOW] Inconsistent token-progress updates** — scout `UsageObserver` (`:127-130`) skips `UpdateSwarmTokens` while `runRole`'s (`:245`) and the estimate fallback (`:257-260`) call it; with real usage the TUI token counter silently lags during the scout phase.

- **[LOW] Misleading test** — `TestOverBudgetRechecksAfterRole` (`orchestrator_test.go:369-385`) claims to reproduce F-POL-67 (orchestrator rechecks budget after each role) but never calls `Run`; it just exercises `overBudget` twice. Either test the loop or rename it.

## Quick wins

1. Delete `SDD.MaxTotalTokens` plumbing, `Worktree.MergeBase`, ledger SHA parsing, `TaskState.TestFailures`/`FinalSummary`, `WriteBranchReviewPackage`, `Orchestrator.NewMeter`, and the dead `overBudget` check at `orchestrator.go:187` — pure deletions, no behavior change.
2. `type RunnerFactory = swarm.RunnerFactory` in sdd; drop sdd's duplicated field by reading `Cfg.MaxFixRounds` with the default applied in `New`.
3. Fix `parseImplementerStatus` to read the `Status:` line; refresh the stale `meter.go` comment; add `UpdateSwarmTokens` to the scout observer.
4. Replace `BuildFixPrompt("", …, PlanTask{})` with a branch-specific fix prompt builder.
5. Migrate swarm orchestrator tests to `RunTaskFunc` and delete `usageScriptedProvider` in favor of `agenttest.ScriptedProvider.Usages`.

Note on `agenttest`: it is heavily and consistently reused (~100 call sites across `internal/agent` tests); the only gap is the swarm tests above. No changes needed there.


---

**Scope:** Package `internal/tools/native/` — native tools: file, search, shell, git, repo, symbols; jobs_manager.go (448 lines), file.go (307), plus the rest of the package.

# Audit: `internal/tools/native`

## Scope summary

This package implements the agent's built-in tools: file read/patch, repo search/index/map/card, symbols, git status/diff, shell/test execution, background job management, web fetch/search, todos, questions, and tool selection (~2,800 LOC non-test). Overall health is **good**: tools follow a consistent constructor pattern, security hardening (path containment, bounded output) is centralized, and `jobs_manager.go` is carefully synchronized with well-documented lock ordering. The main issues are duplicated plumbing (session-state assertions, path resolution variants, runner wiring) and vestigial artifacts from earlier design directions — not structural rot.

## Findings (by impact)

- **[MED] `sessionState any` erases a typed field, then re-asserts it at 5 sites** — `native.go:93` stores `*session.State` (Options field, `native.go:39`) as `any`; call sites assert back to `*session.State` (`file.go:283`) or to ad-hoc 1-method interfaces (`todos.go:59`, `question.go:40`, `tools_select.go:39`, `native.go:213`). The `any` buys nothing — `*session.State` satisfies all these interfaces. Fix: keep the field typed as `*session.State` and assert to the small interfaces only where needed; deletes the nil-handling duplication in every tool.

- **[MED] Redundant `safeBuffer` duplicates `BoundedOutput`** — `jobs_manager.go:86-103` defines a mutex-wrapped `bytes.Buffer`, but `runJob` (`jobs_manager.go:230-231`) already wraps each job's output in `BoundedOutput`, which has its own `String()`/`Truncated()` and enforces the same byte limit. The safeBuffer only ever receives ≤ limit bytes. Fix: store the two `*BoundedOutput`s on the job and read them in `Output` directly; delete `safeBuffer` (~20 lines).

- **[MED] Two parallel notification mechanisms for job count** — `SetOnChange` (`jobs_manager.go:135`) wired to `session.State.SetRunningJobsCount` (`native.go:213-215`) **and** `SetBroker` publishing `JobEvent` (`jobs_manager.go:144`); the TUI reads both (`status.go:161` fallback vs `jobCountMsg` pump). Also `notifyChange`'s counting loop (`jobs_manager.go:418-425`) duplicates `RunningCount` (`351-365`). Fix: have the broker subscriber update session state (or drop the callback and keep only the broker), and make `notifyChange` call an extracted `countRunningLocked()`.

- **[MED] `file.write_patch` reads and resolves every file twice** — validate loop (`file.go:163-206`) does `resolveWorkspacePathMulti` + `os.ReadFile`; apply loop (`file.go:212-281`) repeats both. Double I/O plus two near-identical blocks that can drift apart. Fix: single pass that caches `path`/`original` per patch in a slice, validates all, then applies from the cache.

- **[MED] Default `execRunner` silently ignores `CommandRequest.Timeout`** — `runner.go:16-34` never applies `req.Timeout`; only the sandbox runners do (`internal/sandbox/timeout.go:13`). `shell.run`'s `timeout_seconds` is a no-op whenever the fallback runner is in use (and `native_test.go:97` asserts the value is passed, masking that nothing enforces it). Fix: wrap ctx with `context.WithTimeout` in `execRunner.Run` when `req.Timeout > 0`, mirroring `runWithTimeout`.

- **[LOW] `languageOf` duplicates `repo.DetectLanguage`** — `diagnostics.go:45-64` hand-maps the same extensions `repo/language.go:29-52` already covers with identical keys for all diagnostics-relevant languages. Fix: use `repo.DetectLanguage` and drop the local function.

- **[LOW] Two path-resolution entry points, used inconsistently** — `resolveWorkspacePath` (`helpers.go:32-34`) is a 1-line wrapper over `SafeResolve` used only by `git.go:52`; everything else uses `resolveWorkspacePathMulti`. Consequence: `git.diff -- path` rejects paths in `additionalRoots` that `file.read` accepts. Fix: switch `git.go:52` to the multi variant and delete the wrapper (keep `SafeResolve` exported).

- **[LOW] Double defaulting of job-manager limits** — `native.go:204-209` re-applies `<= 0` defaults that `NewJobManager` already applies (`jobs_manager.go:111-119`). Also makes the retention guard `m.retention <= 0` at `jobs_manager.go:370` dead. Fix: delete the local defaults and the dead guard.

- **[LOW] Duplicated web client/timeout setup** — `web.go:60-82` vs `132-143` build the timeout/http.Client twice (with slightly different semantics). The `!t.webEnabled` checks (`web.go:45,121`) are unreachable since registration is gated at `native.go:151-156`. Fix: extract `t.webClient()`.

- **[LOW] `ask_user` re-marshals JSON and rebuilds the tool per call** — `question.go:80-83` marshals args, then calls `t.questionAskTool()` constructing a fresh Tool+handler each invocation, bypassing schema validation. Fix: extract a shared `askQuestions(ctx, questions)` helper both handlers call.

- **[LOW] Stale feature-tag comments** — "F19 R4" (`jobs_manager.go:21`), "F-SEC-19" (`search.go:83`), "F-PERF-117", "F-SAFE-22" (`file.go:257`) reference an external tracking doc; meaningless to future readers. Fix: keep the substantive explanation, drop the tags.

- **[LOW] Misplaced/duplicated tests** — `agent_run_test.go` (156 LOC) tests `agent.NewSubagentTool` but lives in `package native` (vestige of when agent.run was registered here; it moved to app per `native.go:148-150`). `native_test.go:74-78` duplicates `resolveWorkspacePath` cases already in `helpers_test.go`. Fix: move the former to `internal/agent`, delete the latter.

- **[LOW] `cap` shadows the builtin** — `file.go:73`, `web.go:94,160`. Rename to `limit`/`readCap`.

## Quick wins

1. Delete `safeBuffer`; store `*BoundedOutput` on the job (~20 lines, no behavior change).
2. Remove `native.go:204-209` double-defaults and the dead `retention <= 0` guard.
3. Replace `languageOf` with `repo.DetectLanguage`; route `git.diff` through `resolveWorkspacePathMulti` and delete `resolveWorkspacePath`.
4. Type `toolSet.sessionState` as `*session.State`; collapse the five assertion blocks.
5. Extract `webClient()` helper; remove unreachable `webEnabled` re-checks.
6. Move `agent_run_test.go` to the agent package; drop duplicated path-resolution tests in `native_test.go`.

Not problems: `jobs_manager.go` locking/WaitGroup discipline is solid (wg.Add under lock, lock-ordering comment at 247 is accurate, Shutdown idempotence is correct); `saferesolve.go` is a clean single-source-of-truth; `BoundedOutput` and the tool-constructor pattern are consistent and well-tested. `jobs_test.go` (702 LOC) is large but its fake runner is shared and the tests are not egregiously duplicated.


---

**Scope:** Packages `internal/tools/policy/` (policy.go 662 lines), `internal/tools/registry/`, and `internal/tools/patch/`.

# Audit: `internal/tools/policy`, `internal/tools/registry`, `internal/tools/patch`

**Scope summary.** Policy decides allow/confirm/deny for tool calls (shell guardrails, MCP policies, permission rules, registry risk); registry is a validated tool table with risk levels and audit types; patch parses and applies SEARCH/REPLACE blocks. Registry and patch are in good shape — small, validated, well tested. Policy is the problem child: three overlapping guardrail implementations, one security-relevant nondeterminism, dead config flags, and a real data race.

## Findings (by impact)

1. **[HIGH] MCP policy matching is nondeterministic** — `policy.go:157-174`. The pattern loop `range`s over `map[string]string` and `break`s on first match. When several patterns match (e.g. `mcp.*`=deny, `mcp.github.*`=allow), Go's random map order picks the winner, so a matching `deny` is silently skipped despite the "deny returns immediately" comment. Fix: iterate twice (deny pass, then allow/confirm pass) or collect matches and take most-restrictive.

2. **[HIGH] `AllowSudo`/`AllowDestructive` are dead configuration** — `policy.go:491-509`, consumed at `runner.go:1155-1159`. Setting either flag only appends `"(flagged allowed)"` to the reason; the decision stays `DecisionDeny`, which the runner hard-blocks with no TUI confirm. The doc comment at `policy.go:487-490` ("the TUI is still expected to confirm") misdescribes the system. These flags plus their config plumbing (`config.go:184-190,369-372,550-553,908-918`; `save.go:70-82`) and tests buy nothing. Fix: either make them downgrade Deny→Confirm, or delete flags and plumbing.

3. **[MED] Data race on `pe.rules`** — `Evaluate` reads `pe.rules` (`policy.go:181,233`), `pe.registry` (:249) and `pe.logger` (:496) without the mutex, while `SetRules`/`WithRegistry`/`SetLogger` write under it. The TUI calls `SetPolicyRules` from the UI goroutine (`tui/model.go:1086`) while the agent goroutine is mid-`Evaluate`. Comments at :82-83 and :90 promise "safe for concurrent use" — only `sessionRules` actually is (:275-277). Fix: snapshot `rules`/`registry` under `RLock` at the top of `Evaluate`.

4. **[MED] Two divergent rule-assembly paths** — `NewEngine` prepends `permissions.SafeCommands` and filters invalid actions (`policy.go:58-70`); `SetRules` (:91-95) replaces wholesale, and its production feeder `Runner.SetPolicyRules` (`runner.go:268-278`) rebuilds without SafeCommands or validation. After any settings save, safe-command auto-allows silently disappear. Fix: one `buildRules(cfg)` helper used by both.

5. **[MED] Guardrail logic triplicated, coverage inconsistent** — `analyzeCommand` (`policy.go:396-459`) layers `ClassifyCommand` (shlex, whole-string argv[0] only — `classify.go`), per-stage substring patterns, and a per-stage chmod/chown special case (:438-447) that re-implements `classify.go:52-56` with a different tokenizer. `destructivePatterns` (:38-45) exists only to tag the redundant substring entries. Net effect: `rm -fr /` is blocked, `echo x | rm -fr /` escapes both checks. Fix: run the argv-aware checks per stage using `st.args` (already extracted by `parseStages`), delete the destructive substring entries, `destructivePatterns`, and `hasRecursiveFlag`.

6. **[MED] `Evaluate` is a 170-line, four-domain function** — `policy.go:133-304`. MCP, shell guardrails, F4 rules, and registry risk interleaved; numbered comments are stale (MCP goes 1,2,**4**; shell restarts 1,1b,2…6). Extract `evaluateMCP`/`evaluateShell` helpers; fix numbering.

7. **[LOW] Nil-config panic paths** — `NewEngine` tolerates nil cfg (:59) and some sites guard (:218-221,:492), but `pe.config` is dereferenced bare at :200, :268, :285, :292, :299. `NewEngine(nil,nil)` + `Evaluate("shell.run", …)` with no matching rule panics. Fix: require non-nil config or guard once.

8. **[LOW] `ValidatePatch`'s bool is always `true`** — `diff.go:8,26`; both callers write the awkward `if !ok || err != nil` (`patch_preview.go:37-39`, `file.go:202-205`). Return `error` only. `ValidatePatch`/`ApplyPatch` also duplicate the normalize+replace loop (:8-27 vs :92-99).

9. **[LOW] Dead branch in parser** — `parser.go:43-50`: `commitChunk`'s `currentPath == ""` drop is unreachable (only call site :96 is guarded by the error at :92-94); its comment cites abandoned "legacy behavior". Also `flushChunk` validates but doesn't flush — rename.

10. **[LOW] Mechanical duplication** — `registry.go:55-112`: `List`/`ListDeferred`/`ListLoaded` copy the same filter+clone+sort loop (extract one predicate helper); `basename` vs `basenameLower` duplicated in one package (`classify.go:62`, `policy.go:462`); `matchRule` re-normalizes already-normalized input (`policy.go:546-547`); unreachable `result != DecisionDeny` at :649 (deny returns at :647).

## Quick wins

- Two-pass deny-first MCP matching (finding 1) — ~10 lines, kills a security nondeterminism.
- `RLock` snapshot at top of `Evaluate` (finding 3) — 3 lines.
- `ValidatePatch` → `error`-only signature, update two callers (finding 8).
- Delete `parser.go:43-50` dead branch and stale comment; fix step numbering in `Evaluate`.
- Merge the three `List*` loops and the two `basename` helpers.
- Product decision on `AllowSudo`/`AllowDestructive`: wire to Confirm or rip out (finding 2) — biggest config-debt reduction per line.


---

**Scope:** Packages `internal/tools/mcp/` (MCP client.go 277, manager.go 258, protocol) and `internal/tools/desktop/` (playwright-based tools.go 412).

## Audit: `internal/tools/mcp` + `internal/tools/desktop`

**Scope summary.** The MCP package implements a stdio JSON-RPC client (`client.go`) that spawns child MCP servers, plus a `Manager` that validates config, starts clients, and registers their tools into the tool registry. The desktop package registers six Playwright-backed `browser.*` tools with a small backend abstraction (standalone / attach-over-CDP / fake). Overall health is decent — the MCP client core is careful (pending-map lifecycle, EOF fan-out are well-tested) — but the desktop tools file is highly repetitive, the browser interface carries dead methods, and env-var validation exists in three overlapping layers.

### Findings

- **[HIGH] `bufio.Scanner` 64KB line limit will silently kill any MCP server returning large results** — `internal/tools/mcp/client.go:221`. Default `bufio.Scanner` max token is 64KB; MCP `tools/call` results routinely exceed that (file contents, DOM dumps). One oversized line makes `scanner.Scan()` return false with `bufio.ErrTooLong`, which is stored as `c.err` (client.go:247) and permanently poisons the client — every subsequent `Call` fails. Fix: one line, `scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)`.

- **[HIGH] Six near-identical tool handlers; the repetition already caused a state-clobbering bug** — `internal/tools/desktop/tools.go:89-399`. Each handler repeats decodeArgs → getSession → Page → `updateBrowserState{Active:true}` → op → `updateBrowserState{Active:false}`; the same `session.BrowserInfo{SessionOpen:true, Active:false, Mode:..., UpdatedAt:...}` literal appears ~10 times. Because `SetBrowserInfo` replaces the whole struct (`internal/app/session/session.go:670`), the partial literals in `readTool`/`fillTool`/`screenshotTool` post-op updates (tools.go:185, 288, 392) wipe the URL/Title set by navigate — the TUI browser bar loses the current page after any read. Fix: extract a `withPage(ctx, toolName, func(page) (ToolResult, error))` helper that sets active/inactive state and preserves URL/Title; cuts ~150 lines and the bug class.

- **[MED] Env validation duplicated across three layers with divergent rules** — `manager.go:23-47` hardcodes `mcpDenyListEnv`; `client.go:78-91` re-filters via `envutil.IsDangerousKey`/`IsSecretKey` (which already cover `LD_*`, `DYLD_*`, `IFS`, `PATH` — `internal/sandbox/envutil/allowlist.go:45-57`); behavior differs (Start-time error vs. silent drop), so e.g. `PYTHONPATH` passes envutil but is rejected by the manager, while a manager-approved key can still be silently dropped by the client. Fix: make `validateServerEnv` delegate to the envutil predicates and keep one deny-list source.

- **[MED] `PageHandle` interface carries 4 dead methods** — `browser/backend.go:14-29`. `HTML` has no production caller; `WaitForSelector` none; `WaitForLoadState` is used only in `standalone_test.go:34,70`; `Submit` is literally `p.page.Click(selector)` (standalone.go:160-162), identical to `Click`. Every implementation (fake.go, standalone.go) must maintain these. Fix: trim the interface to the 10 used methods; delete `Submit` or make it actually submit a form.

- **[MED] `AttachBackend` and `StandaloneBackend` are near-copies** — attach.go:31-50 ≈ standalone.go:28-49 (playwright.Run + connect + `startedOnce`), and the page-creation tail with timeout setup is identical (attach.go:76-81 = standalone.go:64-69). Attach even returns `&standalonePage{...}` (attach.go:81), so the types are already entangled. Fix: one backend struct with a `connect func(*playwright.Playwright) (playwright.Browser, error)` hook, or extract a shared `newPage(b, pwCtx, owned)` helper.

- **[LOW] Pointless `findClient` indirection** — `manager.go:186-216`. `pendingTool` stores `clientName` only to re-look-up the client it came from, yielding an impossible error ("disappeared during registration"). Store the `*Client` directly; delete `findClient`.

- **[LOW] `ctx` parameters ignored throughout `PageHandle`** — standalone.go:106-186: every method takes `ctx` and drops it (playwright-go limitation), so callers' cancellation/deadlines do nothing. At minimum document it on the interface.

- **[LOW] Error double-report** — `manager.go:246-252` returns a `ToolResult` with `Error` set *and* a non-nil Go error for the same failure; the caller/registry will log/report it twice. Pick one channel.

- **[LOW] Vestigial protocol fields** — `protocol.go:44-46` `ServerCapabilities.Tools` is parsed but never read; the whole `InitializeResult` is discarded (client.go:122). Harmless but noise. Also `Client.Close` reads `c.stdin/stdout/cmd` lock-free (client.go:144-152) and always returns nil.

### Quick wins

1. `scanner.Buffer(...)` in `client.go:221` — one line, removes a total-failure mode.
2. Add a `ts.setState(active bool, toolName string)` helper that merges with existing state instead of replacing; delete the ~10 repeated literals.
3. Trim `PageHandle` to used methods; delete `FakePage`/`standalonePage` dead code that exists only to satisfy them.
4. Replace `mcpDenyListEnv` body with `envutil.IsDangerousKey(k) || envutil.IsSecretKey(k)`.
5. In tests, `30_000_000_000` → `30 * time.Second` (tools_test.go:23,117); build screenshot/navigate result JSON with `json.Marshal` instead of `fmt.Sprintf` + `%q` (tools.go:140, 395).

Not flagged: the MCP `Call`/pending-map machinery and its tests (F-BUG-47, F-CON-53) are in good shape, and `url_filter.go` is small, correct, and well-tested.


---

**Scope:** Package `internal/acp/` — ACP v1 conversation lifecycle: session.go (690), server.go (630), turn.go (401), plus remaining files.

All files read and suspicions verified. Compiling the report.

## Scope summary

`internal/acp` implements the ACP v1 JSON-RPC server: a line-delimited transport/router (`server.go`), session lifecycle over `app.Runtime` (`session.go`), prompt-turn streaming (`turn.go`), per-cwd DB discovery (`lister.go`), permission bridging (`permissions.go`), and wire types (`protocol.go`). Overall the package is above average for this repo — locking is documented, errors are typed, tests are extensive. The real problems are a latent deadlock in `lister.go`, heavy copy-paste in the lifecycle handlers and `Serve` shutdown paths, and a few vestigial parameters/returns. `protocol.go` and `permissions.go` are in good shape; no findings there.

## Findings

- **[HIGH] Self-deadlock on stale cache entry** — `lister.go:52-53` vs `lister.go:77`. When a cached entry exists but exceeds the 30s TTL, `getOrOpen` locks `entry.mu` via `defer`, closes the DB, then reaches line 77 `entry.mu.Lock()` — re-locking a non-reentrant mutex the same goroutine already holds. The goroutine wedges forever, and every later call for that cwd blocks at line 52. No test overrides `ttl`, so the path is never exercised. Secondary race: line 89 reads `existing.db` without holding `existing.mu` (can return a nil or just-closed handle). Fix: drop `cachedDB.mu` entirely and do the whole check/open/store under `l.mu` (opens are rare, TTL-cached); add a TTL-expiry test.

- **[HIGH] Triplicated shutdown block in `Serve`** — `server.go:267-284`, `286-311`, `313-330`. All three select arms repeat cancel → `failOutbound` → `closeInput` → `waitHandlers` → a labeled drain loop (`drainLoop1/2/3`) → `joinErrors`. ~50 lines of near-identical code; a fix to one arm (e.g. missing drain) will drift. Extract `func (s *Server) shutdown(cause error) error` and have each arm return its variation of the cause join.

- **[MED] Lifecycle handlers are copy-paste** — `session.go:218-243` (Create), `250-291` (Load), `298-331` (Resume), `380-411` (Delete). Each repeats: nil-start check, `len(params)>0` + `json.Unmarshal`, `validateLifecycleParams`, `replaceExisting`, the identical 5-line opts build, `start`, `publishReplacement`. Load vs Resume differ only in the notify check and `replay`. Note the drift already starting: Load publishes under `rt.SessionID` (line 286) but Resume under `p.SessionID` (line 329). Extract a `startRuntimeForParams(ctx, rawParams, requireID) (*app.Runtime, sessionParams, error)` helper.

- **[MED] Lookup silently yields nil Run/Events** — `run.go:96-106`. `rt.Runner == nil` leaves `run` nil; the ignored type assertion `rt.EventBroker.(*pubsub.Broker[session.Event])` can yield a nil broker. `turn.go:219` then calls `rt.Run` in a goroutine — a panic there is *not* covered by `dispatch`'s recover (separate goroutine) and crashes the process. Return `ok=false` when Runner/broker are missing.

- **[MED] `PromptTurn` is a 180-line giant** — `turn.go:154-330`. The `forward` closure (lines 232-286) mixes update forwarding, permission bridging, and question auto-answering; the two-phase forward/drain loops reuse the outer `err` variable confusingly (lines 295-300). Split `forward` into methods on `TurnManager` (e.g. `forwardUpdate`, `handleApproval`, `handleQuestion`).

- **[MED] Inconsistent shutdown timeouts** — `session.go:153-159` defines `connectionShutdownTimeout`/`shutdownCtx()`, but `publishReplacement` hardcodes `5*time.Second` off the caller ctx (line 573), and `replaceExisting` uses unbounded caller ctx (lines 536-540). Use `shutdownCtx()` in all three.

- **[LOW] Dead return value** — `session.go:530` `replaceExisting` returns `*app.Runtime`; callers do `_ = old` (line 275) or `_,` (316, 400). Return only `error`.

- **[LOW] Dead `stderr` parameter** — `run.go:34` `runWithConfig` never uses `stderr`; logging comes from `cfg.logger` (line 131). Remove the param.

- **[LOW] Over-reused validator** — `session.go:394` Delete runs `validateLifecycleParams`, forcing clients to send `mcpServers: []` for `session/delete` — an irrelevant constraint from reusing the session/new matrix. Validate cwd+sessionId directly.

- **[LOW] Nine copies of the parse prologue** — `if len(params) > 0 { json.Unmarshal… }` in Create/Load/Resume/Delete/CloseSession/List/Cancel/PromptTurn/initialize. A 5-line `decodeParams(raw, &v)` helper removes them all.

- **[LOW] Stale/contradictory comments and double-guards** — `turn.go:372` comment says "Exported as a package-level var" but `cancelWait` is unexported. `turn.go:228,282` `turnAnswered sync.Map` is admitted "belt-and-suspenders" over `Respond`'s `sync.Once` — drop it.

- **[LOW] Minor inconsistencies** — two error-join idioms in one package (`joinErrors` `server.go:25` vs `errors.Join` `session.go:357,500,542`); `wireError`'s switch (`server.go:483-498`) parallels `codeFor` (`protocol.go:77-86`) — one code→message map would serve both; `waitHandlers` leaks its Wait goroutine if handlers hang past timeout (`server.go:546-549`); option funcs (`WithLogger`, `WithSessionManagerLogger`) duplicate the already-exported `Logger` field; `float64(e.MessageCount)` (`session.go:449`) is unneeded. Tests are heavy (~4.6k lines vs 1.9k prod) but harnessed — not egregious.

## Quick wins

1. Fix the `lister.go` deadlock (single-mutex rewrite, ~15 lines) — correctness bug with a one-line trigger (wait 30s).
2. Extract the `Serve` shutdown helper — deletes ~35 duplicated lines.
3. Delete `_ = old`, drop `replaceExisting`'s first return, remove dead `stderr` param, fix the `cancelWait` comment — four tiny deletions.
4. Add `decodeParams` helper — removes 9 repeated blocks in one pass.
5. Point `publishReplacement` at `shutdownCtx()` instead of the hardcoded 5s.


---

**Scope:** Package `internal/db/` — SQLite persistence: sessions.go (442), audits.go (272), symbols.go, plus remaining files.

## Audit: `internal/db/` — SQLite persistence layer

**Scope summary.** `internal/db` owns schema (`migrations.go`), open/migrate (`db.go`, `dbpool.go`), and per-table CRUD for sessions/messages, tool-call audits, files, symbols, memories, snapshots, todos, and turn metrics. Overall health is decent: queries are parameterized, errors are wrapped, timestamps and scan patterns are mostly consistent. The real problems are one piece of abandoned infrastructure (the read pool), one gnarly audit round-trip, and several copy-pasted scan/migrate blocks.

### Findings

**[HIGH] Read pool is dead infrastructure** — `dbpool.go:11-38`, `db.go:18-19,38-43`. `OpenWithPool` opens 4 extra SQLite connections (`readDB`), but no query anywhere in the repo reads through it — every method uses `db.sqlDB` (pinned to `MaxOpenConns=1`). `readDB` is only touched by `Close()` and `dbpool_test.go`, which tests the pool's own clamping. The design doc (`docs/superpowers/plans/2026-07-15-domain-e3-db-perf.md`) confirms this was a planned perf change that was never finished. It's pure cost: 4 idle connections, an exported `OpenWithPool(path, size)` knob used only by tests, and a misleading doc comment on `DB` (`db.go:12-16`). Fix: either route read-only queries through `readDB` (finishing the plan), or delete `readDB`/`OpenWithPool`/`dbpool_test.go` and keep a single `Open`. Deleting is the honest minimal move until the perf work is actually scheduled.

**[HIGH] `GetToolCalls` sandbox round-trip is overcomplicated and fragile** — `audits.go:188-264`. Sandbox state is reconstructed from *three* sources: dedicated columns, backend-inference, and an untyped `map[string]any` re-parse of `sandbox_limits_json` with six nested float64/bool/string assertions (`audits.go:231-263`), which duplicates data already stored in dedicated columns (`network_isolated`, `killed_reason`). The `sbEnabled` "upgrade-only" merge (`audits.go:192-198`, F-BUG-108) exists only because legacy rows defaulted wrong. Fix: add the four remaining fields (`memory_limit_bytes`, `cpu_seconds`, `max_processes`, `filesystem_isolated`) as dedicated columns via the existing `Migrate` column-add mechanism, and stop round-tripping the blob; short of that, unmarshal into a typed struct instead of `map[string]any` — one `json.Unmarshal` replaces ~35 lines of assertions.

**[MED] Duplicated message-scan block** — `sessions.go:208-239` (`MessagesOnBranch`) vs `sessions.go:283-314` (`GetMessages`): ~25 lines, character-identical (nullables, `Final`/`ParentID` handling, RFC3339 parse). The package already has the right pattern in `scanSymbol` (`symbols.go:64`) — extract `scanMessage(rows)` and use it in both.

**[MED] `Migrate` repeats itself and re-reads schema** — `db.go:55-133`: three hand-written copies of the same "introspect columns, loop a defs map, ALTER TABLE" pattern, and `tableColumns("messages")` is called twice (`db.go:95` and `db.go:116`). Collapse to one table-driven list of `(table, column, def)` tuples plus the two backfill UPDATEs; ~80 lines → ~30, one introspection per table.

**[MED] `GetProject`/`GetProjectByRoot` near-identical bodies** — `projects.go:19-63`: two 20-line functions differing only in the WHERE clause and not-found message. Extract one `scanProject(row, ...)` helper.

**[MED] Dead code: `SnapshotFiles`, `LoadTodos`** — `SnapshotFiles` (`snapshots.go:101`) has zero callers, tests included. `LoadTodos` (`todos.go:29`) is only called from a test (`session_test.go:893`); production saves todos (`session.go:1419`) but never restores them — a vestige of an abandoned resume feature. Delete both, or wire `LoadTodos` into session restore (and fix its `sql.ErrNoRows` leak, `todos.go:35-37`, which would error on first run).

**[LOW] Inconsistent conventions.** (a) `db.go:12-16` claims callers do their own queries via `SQLDB()` with their own contexts, but the package provides dozens of context-free methods; only `DeleteSession` (`sessions.go:323`) and `ListSessions` (`sessions.go:371`) take a `ctx`. The comment describes a design that doesn't exist — rewrite it. (b) `allowedTableInfo` (`db.go:161-166`) guards against SQL injection on names that are provably constant (its own comment admits this); drop it. (c) `snapshots.go:95-97` checks `RowsAffected` error and discards the value — dead check. (d) `snapshots.go:48,77` uses `err == sql.ErrNoRows`; the rest of the package uses `errors.Is`. (e) Limit clamping uses named consts in `sessions.go:345-348`/`turnmetrics.go:9-10` but magic 50/200 in `symbols.go:108-113`.

**[LOW] Split table ownership.** `file_reads`/`file_writes` are defined in this package's schema (`migrations.go:125-137`) but all access lives in `internal/filetrack/service.go` via the raw `SQLDB()` handle (`app.go:371`). Moving those four queries into `internal/db` would remove the last production use of the `SQLDB()` escape hatch.

### Quick wins
- Delete `SnapshotFiles`, `LoadTodos`, and (if not completing the perf plan) `readDB`/`OpenWithPool`.
- Extract `scanMessage`; dedupe `GetProject`/`GetProjectByRoot`.
- Table-drive `Migrate`'s column-adds; drop the duplicate `messages` introspection.
- Remove the `RowsAffected` no-op in `PruneSnapshotsOlderThan`; switch `== sql.ErrNoRows` to `errors.Is`.
- Fix the stale `DB` doc comment in `db.go:12-16`.


---

**Scope:** Package `internal/llm/` including subpackages `routing/` and `provider/` — provider abstraction, streaming, route resolver, model presets, role profiles.

I have a complete picture. Here's the audit report.

---

# Audit: `internal/llm/` (provider, routing, schema, streaming, catalog)

## Scope summary

This package family implements the LLM abstraction: a single `OpenAICompatible` provider (chat/stream/embed/models over SSE), a static role→preset router, wire schemas, an SSE decoder, and a curated model catalog. The core request/streaming path is genuinely well-built — clear comments, good tests, no duplication there. The rot is at the edges: multiple config/schema pipelines are plumbed end-to-end but never actually connected, and role/localhost concepts are duplicated across package boundaries. Overall health: **good core, significant dead surface area**.

## Findings (by impact)

- **[HIGH] Sampling-parameter pipeline is dead end-to-end** — `routing/types.go:28-31` (`Temperature`, `TopP`, `ReasoningEffort` on `ModelPreset`) and `schema/chat.go:58-64` (`Temperature/TopP/MaxTokens/Stop/ToolChoice` on `ChatRequest`, with deliberate pointer "unset vs zero" machinery) plus wire serialization at `openai_compatible.go:190-197`. No caller ever sets any of these: `runner.go:989-995`, `title.go:88`, `knowledge.go:133` all build `ChatRequest` without them, and nothing copies `preset.Temperature` into a request. Yet the settings UI lets users edit them (`frames_collections.go:339-349`) and config persists them (`config/save.go:216-217`) — silently ignored. `ReasoningEffort` has no wire field at all. **Fix:** either wire `Preset.Temperature/TopP` into `ChatRequest` in `resolveRoute` (~5 lines in `runner.go:876`), or delete the fields from both layers. Delete `ReasoningEffort` or implement it.

- **[HIGH] Duplicate `AgentRole` types bridged by unchecked casts** — `routing/types.go:3-20` defines 14 role constants; `agent/prompts.go:13-26` redefines 10 of them (plus `general`) as a separate `agent.AgentRole`. They're joined by string casts at `app.go:519` and `app.go:568` with a comment admitting "share string values". Adding/renaming a role in one package silently degrades to the implementer-preset fallback in the other. **Fix:** `agent` already imports `routing` (`runner.go:843`) — replace the duplicate with `type AgentRole = routing.AgentRole` and alias the constants.

- **[MED] `ProviderCapabilities` is 2/3 dead** — `schema/capabilities.go`: of 9 fields, only `ToolCalling`, `JSONMode`, `StructuredOutput` are ever read (`app.go:659-662`, `runner.go:615`). `Streaming`, `Embeddings`, `Vision`, `ReasoningTokens`, `ContextWindow`, `MaxOutputTokens` are written by `DefaultCapabilities()` (`openai_compatible.go:63-71`) and never consulted — the runner always sends `Stream: true` regardless. **Fix:** trim the struct to the three used fields (or start consulting `Streaming`).

- **[MED] `ContextBudget` fields parsed, editable, never read** — `routing/types.go:42-47`: `MaxConversationTokens` and the five `Include*` booleans round-trip through config (`config.go:762-767`) and tests, but the agent reads only `MaxRepoContextTokens` (`runner.go:865`). `legacyRoute` hardcodes `MaxConversationTokens: 4000` (`router.go:127-130`) that nothing consumes. **Fix:** drop the unread fields from `ContextBudget` and config, or implement them.

- **[MED] `Embed` is dead production code** — `provider.go:15`, `openai_compatible.go:383-417`, `schema/embed.go`, wire types `openai_compatible_wire.go:99-110`. No production caller invokes `.Embed(`; only test fakes implement it. The `indexing.use_embeddings` config flag exists but no indexing code calls the provider. **Fix:** wire embeddings into knowledge indexing, or delete the method — which also removes a stub from every test fake (`agenttest/provider.go:35` etc.).

- **[MED] Two divergent localhost detectors** — `routing.isLocalProvider` (`router.go:138-145`: localhost/127.0.0.1/::1/empty-host) vs `probe.IsLocalhost` (`probe.go:17-31`: adds `0.0.0.0` and `::1%` prefix, excludes empty). The routing one backs the F-SEC-09 remote-provider security gate; the probe one drives UI badges. **Fix:** export one shared helper and use the union of hosts in both places.

- **[MED] `ErrNoRoute` is vestigial** — `router.go:13`: exported sentinel never returned by any router path; referenced only by a test helper in `internal/agent`. **Fix:** delete it (and `isNoConfiguredRoute` simplifies).

- **[LOW] Stringly-typed task classes** — `roleForTaskClass` (`router.go:62-73`) switches on raw strings duplicating `agent.TaskClass` constants (`task.go:21-23`); `knowledge.go:58` passes a raw `"knowledge"` that isn't even an agent constant. The import cycle prevents `routing` from referencing `agent`'s constants, so define exported `Class*` constants in `routing` and have `agent` alias them. Also `TaskProfile` (`types.go:50-52`) is a one-field wrapper — `Resolve(class string)` would do.

- **[LOW] `ResolveRole` fallback chain is convoluted** — `router.go:34-60`: two error variables, an `isNoConfiguredRoute` helper, and reordered error returns (aftermath of F-BUG-73). It's correct and well-tested; a small cleanup as a loop over `[role, implementer]` then legacy would be easier to follow, but low priority.

- **[LOW] Latent goroutine leak** — `openai_compatible.go:151-157, 277`: producers send on an unbuffered channel with no `ctx`-aware send. Safe today (all consumers drain until close; the HTTP body is ctx-bound), but a future consumer abandoning the channel without cancelling ctx leaks the goroutine. One comment or a `select` on `ctx.Done()` would close the hole.

## Quick wins

1. Delete `ErrNoRoute` (`router.go:13`) and simplify `isNoConfiguredRoute`.
2. Extract the duplicated `usageBody`→`TokenUsage` mapping shared by `streamChatEvents` (`openai_compatible.go:304-310`) and `readChatResponse` (`openai_compatible.go:366-373`) into one 8-line helper.
3. Drop unread `Event.ID`/`Event.Event` fields from `streaming/sse.go:12-15` (no consumer anywhere) — or leave; smallest of the wins.
4. Trim `ProviderCapabilities` to the three read fields — one struct edit, compiler finds the rest.
5. Delete the `Include*`/`MaxConversationTokens` fields from `ContextBudget` — removes dead knobs from the settings UI too.
6. `Capabilities(ctx)` (`openai_compatible.go:75`) ignores ctx; if the doc-mandated signature isn't sacred, drop the parameter.

Not flagged: the streaming/tool-call buffering core, `catalog`, `factory.go`, `templates.go`, and the test suite (28 focused tests, no egregious bloat) are in good shape.


---

**Scope:** Package `internal/sandbox/` — restricted/container/passthrough command-execution backends (16 files, ~1.2k LOC).

All reading done; build and vet pass clean. Writing the report.

---

# Audit: `internal/sandbox/`

## Scope summary

Three command-execution backends (passthrough / restricted / container) behind a common `Sandbox` interface, plus shared exec lifecycle (`execute.go`), platform-specific process-group kill, and the `envutil` env-scrubbing helper package. Overall health is **good**: security reasoning is careful and well-documented, the goroutine lifecycle in `executeCommand` is correct (buffered `waitCh`, drain-after-kill, no leaks), `terminateProcessTree` is genuinely solid, and the platform split via build tags is clean. The main problems are dead code, one honesty bug in audit metadata, and a handful of localized duplications.

## Findings

1. **[HIGH] Dead code: whole `clock.go`, `killedReason`, `allowSet`** — `clock.go:10,15` (`nowFn`, `elapsedMS`), `timeout.go:21` (`killedReason`), `restricted.go:165` (`allowSet`). Repo-wide grep confirms zero call sites for all four (~50 LOC). `clock.go`'s comment ("tests can stub clock reads if needed") is speculative generality that never materialized — `executeCommand` calls `time.Now()` directly (`execute.go:44`). `allowSet` is additionally an exact duplicate of `denySet` (`restricted.go:173`). Fix: delete all four.

2. **[HIGH] Darwin restricted mode reports a memory limit it never enforces** — `restricted_unix.go:75` skips `ulimit -v` on darwin (`ulimitSupportsMem()`, line 27), but `metaFor` (`sandbox.go:134-137`) sets `MemoryLimitBytes` unconditionally. The comment at `restricted_unix.go:18-20` claims "memory caps simply report as 0 on darwin (see metaFor)" — metaFor does no such thing, and `docs/04-tooling-and-shell-safety.md:203` repeats the false claim. The audit layer (the package's stated "honest reporting" feature) records a phantom limit. Fix: in `metaFor`, zero `memBytes` when `caps.Backend == "restricted" && !ulimitSupportsMem()`; add a `ulimitSupportsMem` stub returning false to `restricted_windows.go` so it stays platform-neutral.

3. **[MED] Two divergent secret-detection algorithms in `envutil`** — `envutil.go:17` (`IsSecretBearer`: exact names + prefix/suffix rules) vs `allowlist.go:25` (`IsSecretKey`: substring `Contains`). Same security decision, different semantics, different consumers (hooks uses the former at `internal/hooks/runner.go:207`, sandbox/MCP the latter). E.g. `MY_TOKEN_INFO` is caught only by `IsSecretKey`. Fix: consolidate on `IsSecretKey` (the superset) everywhere, or document the deliberate distinction — currently neither function explains why both exist.

4. **[MED] Duplicated env-upsert and denylist-filter logic** — the "replace key if present, else append" scan appears verbatim in `container.go:118-131` and `restricted.go:141-152`; the "apply denylist to final result" loop is duplicated within `restricted.go` itself (lines 106-113 and 155-161). Fix: add `envutil.Set(env, k, v)` and `envutil.FilterKeys(env, denySet)` helpers; both backends (and MCP client's similar code) then share them.

5. **[MED] `executeCommand` select branches duplicate result assembly** — `execute.go:62-72` vs `80-93`: ~13 identical lines (duration, truncation, result struct). Only `KilledReason` and the kill differ. Fix: run the select to obtain `waitErr`/`KilledReason`, then build the result once after.

6. **[LOW] Container `timeout` prefix built twice** — `container.go:203-219`: both the argv and shell branches repeat the `timeout --preserve-status -s KILL <n>` prepend. Fix: build the inner argv first (`cmdArgs` or `["/bin/sh","-lc",command]`), then prepend the timeout wrapper once.

7. **[LOW] Inconsistent/redundant validation in `Container.Run`** — `container.go:71-73` pre-checks empty dir, then `resolveConfinedDir` (`restricted.go:185`) re-checks it; `Restricted.Run` relies on the latter alone. Also `metaFor(c.Capabilities(), c.cfg)` is recomputed four times per `Run`. Fix: drop the pre-check; compute meta once at the top.

8. **[LOW] Package-level errors defined in platform-specific files** — `errEmptyCommand`/`errEmptyDir` live in `restricted_unix.go:12-15` and `restricted_windows.go:9-12` but are used by platform-neutral `container.go`. Compiles fine, but misleading. Fix: move to `restricted.go`.

9. **[LOW] Test bloat in `container_test.go`** — `writeFakeRuntime`/`writeFakeInfoRuntime` (lines 24-112) duplicate a ~30-line shell script differing by 3 lines; `TestContainerRunWithFakeRuntime`, `TestContainerBuildArgsWithFakeRuntimeExecutes`, and `TestContainer_AvPathForSimpleCommands` are the same echo-assertion test three times; "AvPath" is a naming typo. Fix: parametrize the fake-runtime script, collapse the triple.

10. **[LOW] Trivial dead statements** — `container_detect.go:38-39` sets `probe.Stdout/Stderr = nil` (already the zero value); the loop guard `c == "" || c == "auto"` (line 29) is unreachable given how `candidates` is built.

## Quick wins

- Delete `clock.go`, `killedReason`, `allowSet` (~50 LOC, zero risk, no behavior change).
- Fix the darwin `MemoryLimitBytes` misreport (finding 2) — one condition plus a windows stub, closes a real audit-honesty bug.
- Drop the redundant empty-dir pre-check and nil-assignments in `container_detect.go` (findings 7, 10) — trivial deletions.
- Extract `envutil.Set`/`FilterKeys` (finding 4) — small helper, removes two copies in-scope and enables reuse by the MCP client later.


---

**Scope:** Packages `internal/repo/` (repo scanner 294 lines, tree-sitter symbols, repo map/card), `internal/contextpack/` (builder.go 359), `internal/knowledge/`, and `internal/skills/`.

# Audit: `internal/repo`, `internal/contextpack`, `internal/knowledge`, `internal/skills`

**Scope summary.** `repo` walks/hashes/indexes the workspace and renders repo maps/cards; `contextpack` maintains the token-budgeted context pack; `knowledge` runs a best-effort end-of-session LLM extraction; `skills` loads markdown+TOML skill files and exposes `skill.load`. Overall the code is well-commented and individually readable, but two packages carry substantial vestigial surface from abandoned design directions: contextpack's `Builder` path and skills' tool definitions are dead in production, and `builder.go` has heavy copy-paste duplication.

## Findings (by impact)

1. **[HIGH] contextpack `Builder`/`BuildInput`/`buildCandidateSections` is dead production code** — `internal/contextpack/builder.go:23-50,139-198`, `contextpack.go:45-65`. No production caller of `NewBuilder`/`Build` exists (only `contextpack_test.go:29,63,396,420`); production packs are built incrementally via `PinFiles`/`MergeMemories`/`RefreshPlanWithBudget` (`internal/agent/runner.go:385-424`). Consequently the `ToolOutput` type and all `SectionRepoCard`/`SectionToolOutput` producers are test-only too (~110 lines plus tests). Fix: delete `Builder`, `NewBuilder`, `BuildInput`, `ToolOutput`, `buildCandidateSections`; keep the kind constants still referenced by the insertion checks at `builder.go:110,331`.

2. **[HIGH] `builder.go` triplicated rebuild logic** — the maxTokens/generatedAt/now defaulting block is copied 3× (`builder.go:91-101`, `124-134`, `311-321`); the replace-kind-and-insert-before-section loop is copied 2× (`103-118` vs `324-339`); the greedy budget loop is copied verbatim for pinned and regular (`225-243` vs `245-263`). Fix: extract a `resolvePackParams` helper and a `replaceSection(sections, sec, ok, beforeFn)` helper, and run one loop over `append(pinned, regular...)`. Removes ~60 lines and real drift risk.

3. **[MED] `Scanner.Scan()` + `hashFile` are test-only** — `scanner.go:178-202,245-257`. The only production scan path is `ScanDetailed` (`internal/tools/native/repo_index.go:38`); all `Scan()` callers are in `scanner_test.go`. Fix: delete both and port the tests to `ScanDetailed` (or make `Scan` a thin adapter over it).

4. **[MED] `skills.ToolDef` parsed, tested, never used** — `skill.go:24-34,66-71`; only consumer is `skill_test.go:99-129`. Nothing registers skill-defined tools — `tool.go:13-24` registers only `skill.load`. Speculative generality. Fix: drop `ToolDef`/`Tools` from the frontmatter schema until tool registration is actually built.

5. **[MED] Knowledge prompt is unbounded** — `knowledge.go:113-130` reads full current content of every touched file; `prompts.go:42-65` stuffs the entire transcript plus full file bodies into one prompt. A long session or large file guarantees context overflow, and the failure is then swallowed (`knowledge.go:69-77`). Fix: cap transcript chars and per-file bytes (head N KB) in `readTouchedFiles`/`BuildExtractionPrompt`.

6. **[MED] Ignore patterns re-validated per entry; invalid pattern aborts whole scan** — `scanner.go:276-293` runs `filepath.Match` (which recompiles) twice per pattern per file/dir, and an invalid pattern returns a fatal error mid-walk (`129-132`, `145-148`) — inconsistent with the package's non-fatal-warnings design, and silent if the tree has no entries. Fix: validate `Config.Ignore` once in `NewScanner` (stash like `loadErr`), match without re-validation.

7. **[LOW] `SkipGitignore` knob has no production wiring** — `scanner.go:22-25`; `repo_index.go:35` hardcodes `false`, `IndexingConfig` (`internal/app/config/config.go:296`) has no such field; only tests set it. Wire it into config or delete it.

8. **[LOW] `Warnings()`/`Skipped()` never consumed in production** — `scanner.go:233-243`; `repo_index.go:59-74` builds its own warnings from `ReadErr` and drops scanner warnings, including a gitignore load failure (`scanner.go:90-92`). Have `repo_index` append `scanner.Warnings()` to its output.

9. **[LOW] Dead bits in `scanner.go`** — the second symlink check (`138-141`) is unreachable: the identical check at `109-112` runs for every entry before the dir/file split. `hashBytes` (`261-265`) never returns non-nil error, making the hash-error branch in `ScanDetailed` (`217-223`) dead.

10. **[LOW] gitignore matcher inefficiencies** — dir-only branch re-splits/joins path prefixes O(depth²) per pattern per path (`gitignore.go:90-103`) and `matchPattern` splits again; `starts` duplicates index 0 (`118-122`: `[]int{0}` then appends `i` starting at 0). Pass pre-split segments; start the extra-starts loop at 1.

11. **[LOW] contextpack misc** — `Pack.Pinned` is written (`builder.go:78`) but never read in production and silently dropped by `buildPackFromSections` (`200-204`) on every `Rebudget`/`MergeMemories`; `RefreshPlan` (`82-88`) is a test-only wrapper; `path:start-end` formatting duplicated (`65-68` vs `163-166`). `repo_index.go:80-97` also re-implements `RenderRepoCard`'s language-count block (`card.go:19-41`).

**In good shape:** `symbols.go` (clean tree-sitter walk, good comments), `map.go`, `card.go`, `gitignore.go` logic, knowledge's JSON parsing with documented import-cycle rationale (`protocol.go:14-26`), and the small skills loader/tool. `IsDefaultIgnoredDir` is properly reused by `search.go:105`. No goroutine issues: `chatOnce`'s early returns are safe because providers send nothing after Error/Done (`openai_compatible.go:297-342`).

## Quick wins

- Delete the unreachable symlink re-check (`scanner.go:138-141`) and `hashBytes`' error return.
- Collapse the two identical budget loops in `buildPackFromSections` into one.
- `loader.go:54`: use `idx.Set(...)` instead of writing the map directly (keeps one mutation path).
- Fix the duplicated `0` start in `gitignore.go:118-122`.
- Surface `scanner.Warnings()` in `repo.index` output (`repo_index.go:99-105`).
- Extract a `snippetSource(snip)` helper for the duplicated `path:start-end` formatting.


---

**Scope:** The small packages: `internal/commands/` (commands.go 492), `internal/diffview/` (diffview.go 515), `internal/pubsub/`, `internal/hooks/`, `internal/trust/`, `internal/permissions/`, `internal/export/`, `internal/csync/`, `internal/filetrack/`, `internal/jsonextract/`, `internal/diagnostics/`, `internal/redact/`, `internal/snapshot/`, plus `cmd/marshal/`. Assess each briefly; flag any that are vestigial, near-empty, or trivially mergeable.

# Audit: small packages + `cmd/marshal`

**Scope summary.** Thirteen utility packages plus the entry point: slash-command catalog, diff renderer, event broker, shell hooks, project trust, permission matching, HTML export, concurrency helpers, file read/write tracking, JSON extraction, diagnostics runner, secret redaction, git-shadow snapshots. Overall health is decent — most packages are small, focused, and tested — but there is one fully dead package (`csync`), one dead public method (`snapshot.Revert`), and a real rendering bug in `diffview`.

## Findings (by impact)

1. **[HIGH] `csync` is entirely unused** — `internal/csync/value.go:1`, `map.go:6`, `slice.go:6`. Zero non-test imports repo-wide (verified by grep); its own doc says it was "adopted for new concurrent state instead of ad-hoc mutex fields on session.State," yet `session.State` still uses ad-hoc mutexes (`internal/app/session/session.go:347,385`) and nothing imports csync. ~96 lines + ~130 lines of tests of pure dead weight. **Fix:** delete the package (or actually adopt it in session.State — but deletion is the honest minimal move).

2. **[MED] `truncateVisible` mangles ANSI-styled content** — `internal/diffview/diffview.go:506-514`. It checks `lipgloss.Width` correctly but then truncates by `[]rune(s)` on a string that already contains chroma emphasis escape sequences (applied at `diffview.go:414-419` before truncation). In side-by-side mode (auto-enabled at width ≥120, half ≈58 cols) any highlighted code line longer than ~58 visible chars gets sliced through escape sequences — garbled color bleed. **Fix:** truncate before styling, or use `lipgloss`'s ANSI-aware truncate (`reflow`/style-aware).

3. **[MED] `snapshot.Service.Revert` is dead code** — `internal/snapshot/service.go:118`. No callers outside the package; even the tests don't cover it (`TestService_RestoreReverts` at `service_test.go:68` tests `Restore`, not `Revert`). 20 lines of unused reverse-apply logic. **Fix:** delete; restore from history if ever needed.

4. **[MED] Command behavior is split-brained between registry and TUI** — `internal/commands/commands.go:160-259` vs `internal/app/tui/model.go:1940-2086`. Thirteen commands (`stop`, `mode`, `swarm`, `sdd`, `connect`, `models`, `model`, `settings`, `set`, `memory`, …) register placeholder handlers returning `""`; the real logic lives in a second `switch cmd.Name` in the TUI. Two files must be kept in sync to add one command, and usage validation (e.g. `/swarm <goal>`) lives only in the TUI. **Fix:** keep the registry as the catalog, but give `Command` an optional `Interactive func(*Model)` hook or move dispatch fully into one place; at minimum co-locate the TUI switch cases next to their registrations via a shared table.

5. **[MED] Hook stderr is captured then silently discarded** — `internal/hooks/runner.go:171`. `cmd.Stderr = &limitedBuffer{...}` is never read; on failure the reason is only `hook %q failed: exit status N` (`runner.go:67`). A failing hook is undebuggable. **Fix:** keep the buffer in a variable and append its tail (e.g. last 200 bytes) to the error/reason.

6. **[MED] `ensureRepo` re-inits git on every snapshot** — `internal/snapshot/git.go:28-47`. The `os.Stat` at lines 29-33 has an empty body (result unused, just a comment), then unconditionally runs `git init --bare` plus two `git config` spawns on **every** `Track` call — three wasted process spawns per snapshot. **Fix:** check for `<shadowDir>/HEAD` once and skip init/config when present.

7. **[LOW] `filetrack` interface is wider than its use** — `internal/tools/native/native.go:26-31` requires `ListReadFiles`/`ListWrittenFiles`, but the only callers are filetrack's own tests; production code uses only `RecordRead`/`RecordWrite`/`LastReadTime` (`internal/tools/native/file.go:82,171,278`). **Fix:** narrow the interface to 3 methods; drop the two List methods (and their tests) or wire them into a feature.

8. **[LOW] `commands.go` internal duplication** — undo/redo/diff/rewind repeat the same `Snapshotter()==nil` / `DB()==nil` preamble four times (`commands.go:277-285, 302-310, 327-335, 405-411`); `compactTokens` (`commands.go:480`) duplicates `compactTokenCount` (`internal/app/tui/model.go:2589`) — the comment even admits it. `Register` doesn't lowercase names but `Lookup` does (`types.go:36,51`), so a mixed-case registration would be silently unreachable. `/help` hardcodes keybindings (`commands.go:60-63`) that will drift from actual TUI bindings. **Fix:** one `snapshotContext()` helper; share one token-format func; lowercase in `Register`.

9. **[LOW] `diffview` dead/speculative surface** — `Options.Theme` (`diffview.go:39`) is never read anywhere (highlighting uses hardcoded colors, `highlight.go:55-64`); `LineFileHeader` (`diffview.go:51`) is handled in two switches but never produced by the parser; `contextStyle` is an empty style (no-op Render, line 253); `applyEmphasis` re-declares `span` duplicating `offset` (line 431) and hand-rolls a bubble sort (lines 437-443). **Fix:** delete Theme/LineFileHeader/contextStyle; use `sort.Slice` and the existing `offset` type.

10. **[LOW] `pubsub` doc/behavior gaps** — package doc (`broker.go:3-6`) promises terminal subscribers "must receive every event," but `sendBlocking` drops after 500 ms (`broker.go:93`) — undocumented contract exception; default buffer `16` is magic-duplicated at `broker.go:195` and `:202`; `Subscribe` after `Close` registers a sub that never shuts down until ctx cancel (`broker.go:206-208`). **Fix:** document the timeout in the package comment; extract `defaultBuffer` const; early-return a closed channel when `b.closed`.

11. **[LOW] `permissions` subtle matching uncommented** — `Evaluate` is last-match-wins with no comment (`rules.go:105-118`); the prefix fallback in `matchGlob` (`rules.go:55-58`) exists to let `cat *` match across `/`, which is non-obvious and easy to break. **Fix:** two-line comments.

**Good shape:** `trust`, `export` (well-tested, including XSS regression tests), `jsonextract`, `redact`, `diagnostics`, `pubsub` core, `permissions/pattern.go`, and `cmd/marshal` are clean and appropriately sized. The goldmark singleton-comparison in `export.go:141-165` is fragile-by-implementation-detail but covered by tests.

## Quick wins

- Delete `internal/csync/` (package + tests) — pure dead code.
- Delete `snapshot.Revert`, `diffview.Options.Theme`, `LineFileHeader`, `contextStyle`.
- Surface hook stderr tail in failure reasons (`runner.go:171`).
- Gate `ensureRepo` on a `HEAD` file check — saves 3 git spawns per snapshot.
- Merge `compactTokens`/`compactTokenCount` into one shared helper; add a `snapshotContext()` helper in `commands.go`.
- Fix `truncateVisible` to operate on unstyled text (truncate first, then highlight/emphasize).
- Narrow the `native.FileTracker` interface to the 3 methods actually called.


---

**Scope:** PRIOR-AUDIT CROSS-CHECK: read `docs/13-project-audit-2026-07-11.md` and `docs/14-codebase-improvement-audit-2026-07-14.md`, then verify against the current code which recommendations were actually implemented and which are still outstanding. Report only still-outstanding items with current file:line evidence, plus any 'fixed' item whose fix introduced new complexity. Also check `docs/plans/` for abandoned plan docs that no longer match the code.

# PRIOR-AUDIT CROSS-CHECK — Report

## Scope summary

I read both audit docs and verified every finding against current code and git history. Verdict: **the remediation program is essentially complete and high quality** — all ~191 findings in `docs/14` and all RESOLVED items in `docs/13` are actually fixed in code (I spot-verified ~40 sites). The real problem is that **the audit documents themselves are stale**: two entire implemented batches were never recorded, so the tracker now misreports ~45 fixed findings as open. `docs/13` is accurate; its only open item (RB2, container resource limits) is explicitly declared no-fix-planned.

## Findings

- **[HIGH] `docs/14` resolution section is missing two completed batches** — `docs/14-codebase-improvement-audit-2026-07-14.md:2091` states "All findings in this document are open unless explicitly marked resolved," and the batch tables jump from Batch 4 (line 2153) to Batch 7 (line 2176). Section C (F-BUG-47…F-POL-69, ~20 findings) was fully implemented in merge `32e5168` ("fix/domain-c-acp-mcp-swarm-2026-07-15", commits `c5224c0`…`d2faffd`) — verified in code: `internal/acp/server.go:603` (panic recover), `:378` (`hasMethod`, F-POL-59), `:446-448` (non-blocking `deliverOutbound`, F-CON-52); `internal/acp/turn.go:391-397` (bounded `CancelAndWait`, F-BUG-50); `internal/acp/session.go:553-576` (continuous-lock `publishReplacement`, F-BUG-49); `internal/tools/mcp/manager.go:246` (`IsError`, F-BUG-48); `internal/agent/swarm/meter.go` (`ProviderUsageMeter` deleted, F-POL-58). Fix: add the Batch 5/6 resolution table.

- **[HIGH] Batches 17–21 (F1–F5) marked "PLANNED" but are merged** — `docs/14…:2281-2344`. All five merged: `251e838` (F1), `2e0f07c` (F2), `36b2fb5` (F3), `d0e66fe` (F4), `fdadb55` (F5). Verified: onboarding pointer receivers/`api_key_env`/project-name/Ollama-status (`internal/app/onboarding.go:116,138,342-347,367`); double-Esc deny (`internal/app/tui/approval.go:51-52,126-130,204`); question focus (`question.go:112,130`); `BeginWork` lock held across check+Add (`internal/app/session/session.go:865-873`); `permissionForTool` and "Enter×2" gone; content-aware `transcriptHash` (`model.go:2595`). Fix: flip these tables to RESOLVED with the merge SHAs.

- **[MED] `perCwdLister` TTL cache (F-POL-63/66 fix) has a data race and is over-engineered** — `internal/acp/lister.go:85-90`: the double-check loser reads `existing.db` **without holding `existing.mu`**, while the winner may hold it mid-reopen with `entry.db = nil` (line 60) → nil-pointer deref on `d.ListSessions`. Also: 30s TTL serves stale `session/list` results, and `l.cache` grows unboundedly per cwd in a long-lived ACP server. Fix: read `existing.db` under `existing.mu`, or simpler — drop the TTL and open once per cwd (evict on error); the cache complexity outweighs opening a small SQLite file per RPC.

- **[LOW] F-SEC-02 residual bypass** — `internal/tools/policy/policy.go:113-115,248`: when `registry == nil` the pre-fix blanket-allow for all non-shell tools remains "for tests and legacy callers." Production wires it (`internal/app/app.go:378`), so no live exposure, but the nil-registry path is a footgun. Fix: make the registry a required constructor arg, or default-deny (`DecisionConfirm`) when nil.

- **[LOW] Stale comment in slimmed help package** — `internal/app/tui/help/help.go:41-43` says "?" is shown only "when it actually opens the help overlay"; the overlay was removed by the inline-interaction-system (`9f27c1a`) days after Batch 18 built it — `?` now prints the `/help` transcript cheatsheet. Package itself is lean and used (`view.go:29,199-208`). Fix: update the comment.

- **[MED] `docs/plans/` holds abandoned/vestigial plan docs** —
  - `docs/plans/2026-07-03-tui-redesign-design.md` + `-implementation.md`: specify a dual-column, right-sidebar-tab TUI (`tea.WithAltScreen`, "[1] Plan [2] Context" tabs) — an abandoned direction; current TUI is single-column with dock panels. No status marker.
  - `docs/plans/2026-07-02-milestone-g-patch-workflow.md`: full plan for the shipped patch workflow; zero checkboxes/status, stale "For Antigravity" header referencing `.agent/workflows/execute-plan.md`.
  - `docs/plans/task.md`: 8-line all-`[x]` checklist for native tools — vestigial.
  - Milestones i/k/l exist as short status stubs in `docs/plans/` **and** as full plans in `docs/superpowers/plans/` with different content — two canonical-looking locations. Fix: delete `task.md` and the two tui-redesign docs, add status headers to milestone-g, and keep one home (`docs/superpowers/plans/`) for Marshal's own plans — especially since `docs/plans` is the default SDD `plans_dir` for *user* projects (`internal/app/config` SDD default).

## Quick wins

- Append Batch 5/6 (C) and F1–F5 resolution tables to `docs/14` — pure doc edit, restores the tracker's trustworthiness.
- Lock `existing.mu` before reading `existing.db` at `internal/acp/lister.go:89` (2-line race fix).
- Rewrite the stale `help.go:41-43` comment.
- Delete `docs/plans/task.md`; mark or archive the four stale plan docs.
- Consider `DecisionConfirm` (not allow) when `PolicyEngine.registry == nil`.


---

**Scope:** CROSS-CUTTING SCAN across all of `internal/`: (a) duplicated helpers/logic reimplemented in multiple packages; (b) unused exported symbols and dead-code candidates; (c) dependency-layering problems (low-level packages importing high-level ones, awkward indirection); (d) inconsistent idioms for the same concern (error wrapping, logging, context handling, goroutine lifecycle, config access). Use repo-wide grep searches; do not deep-dive any single package — other agents do that.

# Cross-Cutting Audit — `internal/` (38k LOC prod, 48k LOC tests, 55 packages)

**Scope summary.** I scanned the whole `internal/` tree for duplication, dead code, layering inversions, and idiom drift, verifying every candidate by reading the code. Overall health is better than expected for a direction-shifted project: error wrapping is consistently `%w` (326 vs 9 `%v`), and real reuse exists (`pubsub`, `jsonextract`, `envutil`, `tui/fuzzy`, `picker`). The genuine debt is concentrated: one fully dead package, one dead builder pipeline, a pointless interface indirection, and a cluster of hand-rolled truncators with divergent semantics.

## Findings (by impact)

1. **[HIGH] `internal/csync` is entirely dead** — `internal/csync/{map,value,slice}.go` (+3 test files). Doc comment says "adopted for new concurrent state (F19 R3)", but zero non-test files import it (`grep internal/csync` → none). Vestige of an abandoned direction. **Fix:** delete the package and its tests.

2. **[HIGH] `contextpack` Builder pipeline is production-dead** — `internal/contextpack/builder.go:23-50` (`Builder`, `NewBuilder`, `Build`), `buildCandidateSections` (builder.go:139-198), `BuildInput` (contextpack.go:45), and `RefreshPlan` (builder.go:82) are called only from tests. In production the pack is always the zero value mutated by `PinFiles`/`RefreshPlanWithBudget`/`Rebudget`/`MergeMemories` (`internal/agent/runner.go:393-396,423,868,924`). ~120 lines of maintained, tested dead code. **Fix:** delete the unused entry points; keep the four mutators actually used.

3. **[HIGH] `BrokerCloser` interface adds indirection and panics, buys nothing** — `internal/app/runtime.go:69-70` types `Runtime.JobBroker`/`SteeringBroker` as a `Close() error` interface, then `app.go:739-745` and `app.go:822-824` type-assert them back to `*pubsub.Broker[...]` and `panic` on mismatch. `app` already imports `pubsub`, `native`, `session`. **Fix:** type the fields concretely; delete `BrokerCloser` and the four panic branches. Same for `mustDB` (app.go:52-58) if `DBCloser` has no second implementation.

4. **[MED] Truncator sprawl — same names, different semantics, one UTF-8 hazard** — `truncateRunes` appends "…" in `internal/agent/runner.go:1758` but silently hard-cuts in `internal/app/tui/model.go:2475`; `truncateErr` is rune-aware/40 in `settings/browser.go:207` but byte-based/48 in `connect/connect.go:371` (`s[:max]` can split a multi-byte rune). Plus `truncateGoal` (`agent/metrics.go:41`), `truncateForDisplay` (`commands/commands.go:487`), `truncateVisible` (`diffview/diffview.go:506`), `ptr[T]` duplicated at `config/save.go:15` and `tui/transcript.go:38`. **Fix:** one rune-aware `Truncate(s, n, ellipsis bool)` helper (e.g. in a tiny `internal/strutil` or reuse `tui/chrome`); delete the rest; fix the byte-slice in connect.

5. **[MED] Duplicated one-shot-LLM stream drainers** — `chatNoTools` (`internal/agent/title.go:87-102`) and `chatOnce` (`internal/knowledge/knowledge.go:132-153`) are the same drain-a-`Chat`-stream loop. **Fix:** export one `provider.ChatText(ctx, p, req)` helper and call it from both.

6. **[MED] Path literals re-derived in many places** — `.marshal/marshal.db` hardcoded 3× in `internal/acp/lister.go:65,102,116` and again as unexported `dbPath` in `internal/app/app.go:186`; `~/.config/marshal` rebuilt in `config.go:651`, `config.go:1153`, `onboarding.go:421`, `runtime.go:416`, and `tui/model.go:2630-2643` (`userConfigDir`). Rename the layout and you audit five files. **Fix:** export `db.Path(workingDir)` / `config.UserDir(home)` constants/helpers and call them everywhere.

7. **[MED] Two overlapping secret-env predicates** — `IsSecretBearer` (`internal/sandbox/envutil/envutil.go:17`, exact+prefix/suffix) vs `IsSecretKey` (`allowlist.go:25`, substring) with independent lists; they disagree on edge keys (e.g. `TOKENIZED`, `GOOGLE_API_KEY`). Security-relevant lists that can drift. **Fix:** make one predicate the source of truth (substring), keep the other as a documented alias, or merge lists.

8. **[LOW] Layering smells (no cycles, but awkward ownership)** — `db` imports `tools/registry` solely for `AuditEvent` types (`internal/db/audits.go:17,127`); `permissions` reaches into `app/session` and `tools/patch` for `PatternForApproval` (`internal/permissions/pattern.go:17,30`); low-level `llm/provider`, `sandbox`, `hooks` all take `app/config` sub-structs (config is imported by 16 packages). Acceptable shared-vocabulary pattern, but the audit-event type arguably belongs to `db`. **Fix:** only if touching these files anyway; don't churn.

9. **[LOW] Inconsistent logger acquisition** — some packages inject `*slog.Logger` (`sandbox`, `snapshot`), others call `slog.Default()` mid-logic (`agent/runner.go:1180-1183`, `pubsub/broker.go:94`, `app/onboarding.go:140`); four types copy-paste the identical nil→`slog.Default()` `log()` helper (`acp/session.go:143`, `acp/server.go:593`, `mcp/manager.go:100`, `mcp/client.go:67`). Harmless but noisy. Also `acp/session.go:625` uses `%v` instead of `%w`, breaking `errors.Is/As`; `pubsub/broker.go:89,104` silently `recover()`s panics, hiding subscriber bugs.

10. **[LOW] Exported-but-internal-only symbols** — e.g. 12 `Build*Message` funcs in `internal/agent/prompts.go`, `db.OpenWithPool` (`internal/db/dbpool.go:11`, only called by `Open`), `resolveWorkspacePath` (`internal/tools/native/helpers.go:32`, one-call alias of `SafeResolve`). **Fix:** unexport opportunistically when editing those files.

## Quick wins

- Delete `internal/csync` (finding 1) — pure deletion, zero risk.
- Delete `contextpack` dead pipeline (finding 2).
- Concrete-type the `Runtime` broker fields; delete `BrokerCloser` + 4 panics (finding 3).
- Fix `connect/connect.go:371` byte-truncation UTF-8 bug (finding 4) — one-line behavioral bug hiding in the sprawl.
- `acp/session.go:625` `%v` → `%w`.
- Extract the undo/redo/diff snapshot boilerplate in `internal/commands/commands.go:277-347` into one `withSnapshot(state, fn)` helper — three near-identical copies today.

Note for the owner: giant functions/files (`runner.RunTask` 465 lines, `commands.RegisterAll` 462, `tui.Model.Update` 428, `config.merge` 360; `model.go` 2657 lines) exist but are per-package deep-dive territory; I flag them here only so they aren't double-counted.
