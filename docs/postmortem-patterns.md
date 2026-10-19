# Marshal Postmortem Patterns (latest sessions)

Source: 46 non-archived session reports under `~/.config/marshal/postmortems/marshal/`
(`sess_*`.json, roughly 2026-09-30 through 2026-10-19). This document aggregates
mechanically extracted data plus the agent-authored observations in those reports.
It is a triage input, not a change plan.

## Scale

- Sessions: 46 (none of them trivial; many are multi-turn plan/execution work)
- Prompt tokens: ~3.68B; completion ~42.2M; reasoning ~41.6k
- Failed-turn tokens: ~298M (~8% of prompt spend)
- Turn outcomes: 419 answered, 83 failed, 5 salvaged (malformed/exhausted/empty/repeat_failure)
- Tool failures: 360 (rolled-up count)
- Approval denials: 62
- Parse issues: 106
- Agent observations: 499
- Execution evidence: 859 shell jobs — 759 completed, 100 nonzero exits (mostly expected test failures, not infra crashes)

Most-used models: deepseek-v4.1-flash (27), glm-5.3 (15), glm-5.3-flash (12),
kimi-k3 (9), gpt-6.1-sol (6), minimax-m3 (5), gpt-6-luna (5), plus one each of
beam/mistral-large-4/gpt-5.6-luna/grok-4.7/longcat-2.5-preview-free/k3-256k.

## Headline patterns (by agent-observation volume)

Numbers count observations whose text matches the theme. Themes overlap; an obs can
land in more than one, and 218 did not match any keyword bucket yet.

1. **Worktree / env / config boundaries — 71 obs**
   Path resolution in worktrees, missing archive dirs inside worktrees, dirty
   checkouts blocking `workspace.finish`, `.git` being a file in a linked
   worktree, missing HOME/CGO, wrong base path. Recurring cost: manual stash +
   relocation + retries before a merge can land.

2. **Subagent wait / control — 74 obs**
   Long branch-review subagent with two 40-minute timeouts and the same wait
   strategy repeated; waits for subagents extending far past useful bounds;
   dispatch prompts with braces/quotes breaking JSON parsing at the harness
   wrapper; invented/fake subagent-activity narration during timeouts.

3. **Shell over native tools — 26 obs**
   `shell.run` with `grep -rn`, `sed -n`, `jq` to read/search because
   `file.read` rejects large files or out-of-workspace paths; repeated full test
   suites (killed by signal) instead of targeted tests; broad commands run 7x.

4. **File path / preconditions — 28 obs**
   Invented filenames (`repo_search.go`, `toolresult.go`), absolute paths that
   escape the workspace, `file.write_patch` on never-read files, guessed plan
   paths, doubled `.marshal/worktrees/...` prefixes, archive paths read inside
   worktrees where the gitignored archive does not exist.

5. **`file.write_patch` parse / read failures — 27 obs**
   Malformed or unbalanced SEARCH/REPLACE blocks ("no SEARCH/REPLACE blocks
   found", "unclosed SEARCH block"); write_patch on files never read; edit
   rejected because the file changed since last read. Highest single tool
   failure count (172 of 360).

6. **Transcript / history parsing — 20 obs**
   `recall_history` queries returning zero results; 111 harness turn_metrics parse
   failures in one session; 54 envelope parse failures (missing tool/args); one
   `transcript_read` failed because `generation_seq` was omitted.

7. **Argument / schema mistakes — 17 obs**
   `repo.search` `context` 4–25 (cap is 0–3), same violation repeated 70+
   minutes apart; `priority` field on `todo.write` entries; `path` to
   `symbols.find`; `question.ask` options passed as objects instead of strings;
   `ask_user` content passed as a JSON object.

8. **Parallel / protocol misuse — 10 obs**
   `shell.run` placed in the parallel-actions array (not read-only); pairing
   `skill.load` with `shell.run` in parallel; copied example tool lists into
   system prompt without checking the live registry.

9. **Fabrication / narration-only — 4 obs**
   Fabricated tool-call and tool-result prose blocks instead of real tool calls;
   invented success reports called out by the user; fake subagent tails during
   timeouts; fabricated pass status.

10. **Config / home boundary — 4 obs**
    Reads of `~/.config/marshal/config.toml` and `~/.config/marshal/skills/*`
    rejected as out-of-workspace; agent advised hand-editing config after
    `config.providers.set` failed.

11. **Exit / stop path — 1 confirmed, user-reported (see below)**
    "Stop work and continue exiting" does not exit while a job is running; the
    user had to SIGKILL. Not extracted from a report, because the failure
    prevents the report from being written.

Also noted: several observations (e.g. `TestStatusLine` wrong-base assumption,
wrong queue delay, missing parent directory for `file.write`) are task-specific,
not harness defects.

## Quantitative triage data

Tool failures (rolled-up count):
- `file.write_patch` 172
- `repo.search` 60
- `file.read` 42
- `shell.run` 22
- `file.edit` 22
- `file.write` 19
- `agent.run` 13
- `agent.await` 3
- `scratchpad.read` 2
- others (config.providers.set, session.send, file.page, workspace.worktree,
  transcript_read) 1 each

Approval denials are almost all shell `grep`/`rm -rf`-style commands that the
risk classifier rightly blocks; they are a tool-choice signal, not a permission
bug.

## Sessions worth a closer look

- `sess_1792339589894846000`: 32 turns, 304M prompt tokens, 127 tool errors,
  21 approval denials, 1 failure; introduced a schema-budget overflow early and a
  race in `Manager.List/Snapshot` discovered late.
- `sess_1792390973387492000`: most recent, 6 turns, 3.94M prompt, 50 tool
  errors, 2 failed turns, hard stall; repeated zero-match `repo.search` and one
  failed `transcript_read`.
- `sess_1792383562365742000`: 13/13 answered but 15.97M prompt; repeated
  gofmt fixes from plan content without trailing newlines.
- There is one unended DB row (`sess_1792393387001455000`, started 2026-10-19
  07:03Z, no `ended_at`) with no matching postmortem yet — inspect before
  treating the latest state as complete.

## New finding: exit / stop path (added 2026-10-19)

Not in the ten extracted buckets, because it was reported by the user rather
than mined from a report — and because it destroys its own evidence.

**Symptom.** With a job running, choosing "Stop work and continue exiting" in
the active-work warning does not exit. The user had to SIGKILL the process.

**Why the corpus never showed it.** A SIGKILLed session writes no postmortem.
The unended DB row `sess_1792393387001455000` is almost certainly that session:
its ID is later than the newest report (`sess_1792390973387492000`), it has no
`ended_at`, and no report file exists for it. Every occurrence of this bug
removes one session from the evidence base, so any frequency here is a floor,
not a count.

**Code path.** `activeworkpanel` returns `DoneMsg{Stop, IDs}` →
`Model.acceptEnd(displayedIDs)` → `EndController.AcceptEnd` with
`RequireDisplayed: true` → `settleEnd` → `WaitForEnd` under
`endSettlementTimeout`.

**Root cause: confirmed — the refusal loop.** The settlement-stall hypothesis is
ruled out by the killed session's own transcript.

Evidence, from `sess_1792393387001455000` in `.marshal/marshal.db`:

- `Kept open. Active work continues.` appears **four times**. That string is
  written only by `Model.declineEnd()` — the "Keep Marshal open" branch. The
  user therefore reached the *keep* path four times while trying to exit.
- No `Could not stop all work: …` message appears. That string is written only
  by `handleEndSettled` on a failed join, so `WaitForEnd` never reported a
  failure. The settlement stall did not occur.
- `Unknown command: /exis` — the user also tried the `/exit` slash command.

**The mechanism.** `AcceptEnd` refuses when the membership grew past what was
displayed. The refusal calls `renderEndWarning()`, which calls
`activeworkpanel.Panel.Replace()`, which sets `p.cursor = 0`. Cursor 0 is
"Keep Marshal open". The user's selection is silently moved off "Stop work and
continue exiting" and onto the safe choice, with no visible cue that it moved.
The next Enter therefore submits `DoneMsg{Stop: false}` → `declineEnd()` →
"Kept open. Active work continues." A membership that churns (a running job
spawning children, IDs changing) refuses every time, so the exit never
completes and the panel's "stop" option appears inert.

The spec (`docs/background-execution/feature-spec.md`) requires the warning to
refresh rather than silently accept an expanded destructive action, so the
refresh itself is correct and must stay. The defect is that the refresh
**re-seats the cursor on the safe choice without telling the user**, converting
a deliberate destructive choice into its opposite.

**Fixed 2026-10-19.** The refresh now preserves the user's selection and says
what changed, so the re-ask is explicit:

- `activeworkpanel.Panel.Replace` no longer resets the cursor; it preserves the
  user's choice (clamping only an out-of-range value). A fresh panel still
  starts on the safe choice, so a new warning never defaults to stopping.
- `Panel.SetNotice` renders one explanatory line above the choices, and
  `Model.renderEndWarningNotice` sets it on a refusal:
  "N new item(s) appeared after you chose — review and confirm again".
- The refusal path in `Model.acceptEnd` keeps the user's selection and the
  notice, so the next Enter re-submits the *refreshed* IDs — the set now on
  screen — which satisfies `RequireDisplayed` and is accepted. The exit is
  reachable again.

Pinned by `TestRefreshPreservesTheUsersSelection`,
`TestFreshPanelStartsOnTheSafeChoice`, `TestNoticeIsRenderedAndCleared`
(`activeworkpanel`) and
`TestWarningRefusalPreservesTheChoiceAndExplainsTheGrowth` (`tui`). The runtime
contract is unchanged: `TestAcceptEndRejectsExpansionWithoutStoppingAnything`
still pins that a refusal stops nothing and freezes nothing.

## Corpus gaps

- **Report count reconciled: 46 files, 46 counted.** An earlier revision of this
  document claimed 47 files on disk; that was a miscount. `ls
  ~/.config/marshal/postmortems/marshal/*.json | wc -l` returns 46, matching the
  count this document uses. No exclusion gap exists.
- **Two sessions have no report, and only one is a defect.**
  `sess_1792393387001455000` (above) is the SIGKILLed session; its friction is
  absent from every number here. `sess_1792395834405436000` is also unended but
  is the *currently running* session, so its missing report is expected.
- **Post-hoc extraction is not currently possible.** `postmortem.Build` takes a
  live `*session.State`, and `session.New` is the only constructor — there is no
  loader that reconstructs a `State` from stored messages. The killed session's
  report therefore cannot be regenerated from its transcript without writing
  that loader first. Its raw evidence is still in `messages` and `tool_calls`,
  and was read directly for the root cause above.
- **218 of 499 observations matched no bucket.** The ranking below rests on
  roughly 56% of the recorded evidence. The exit-path finding is proof that the
  unbucketed remainder contains real defects, so bucketing it is part of the
  triage, not cleanup after it.

## Triage order (revised 2026-10-19)

Supersedes the earlier "for discussion" order. Ranked by severity first, then
leverage per unit of risk. The previous order ranked by observation volume,
which put two diffuse buckets ahead of a bug that loses data.

0. **Exit / stop path** (new). Highest severity: forces SIGKILL, loses the
   postmortem, leaves an unended DB row, and orphans owned processes. Also a
   measurement blind spot. **Root cause confirmed and fixed** (refusal loop; see
   above). The refresh no longer re-seats the cursor on the safe choice: it
   preserves the user's selection and says what appeared, so the next Enter
   re-confirms the set now on screen and is accepted. `RequireDisplayed` stays
   `true` — nothing unseen is ever stopped — and the runtime contract is
   unchanged. Post-hoc recovery of the killed session's report is still blocked
   on a missing session loader (see Corpus gaps).
1. **Self-correcting tool contracts** (cheap, low risk). Clamp `repo.search`
   `context` instead of erroring (60 failures, the same violation repeated 70+
   minutes apart), and fix `todo.write` `priority`, `symbols.find` `path`,
   `question.ask` options-as-objects, `transcript_read` `generation_seq`. A cap
   the model must remember across a long session is a cap it will violate.
2. **`file.write_patch` reachability** (172 of 360 failures — the largest single
   number). Decide before writing parser code: `AGENTS.md` describes it as
   internal-only, reached solely by envelope-mode patch actions. If it is
   legacy, de-advertising it collapses most of those 172 failures for almost no
   risk; parse hardening is the expensive branch.
3. **`file.read` limits → shell fallback** (42 `file.read` failures, 26
   shell-over-native obs). The shell fallback is a symptom: `file.read` rejects
   large and out-of-workspace files, so the model reaches for `grep`/`sed`.
   Fixing paging and limits removes the incentive; prompt guidance alone does
   not.
4. **Worktree path & merge ergonomics** (71 obs). Doubled prefixes, missing
   parent dirs, `.git`-as-file, gitignored archives absent in worktrees, merge
   blocked by benign dirty files.
5. **Subagent wait policy** (74 obs). Bounded waits, no repeated identical
   retry, no narration-as-activity during timeouts.
6. **Transcript / history correctness** (20 obs). The 111 harness
   `turn_metrics` parse failures in one session are a harness bug that corrupts
   the metrics this document is built from; `recall_history` exactness second.
7. **Token / context growth** (~3.7B prompt across 46 sessions). Cache-stable
   prefix audit, stop re-reading large files through shell, trim tool output.

Run the 218-observation bucketing pass alongside steps 0–1, not after step 7.

Everything above is extractive; the agent-pass observations were written by an
agent pass and should be treated as recorded friction, not verified defects.
