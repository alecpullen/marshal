# Context diet: Tier 2a (tool surface)

Status: approved 2026-10-14. Builds on Tier 1 (`design.md`). Plan:
`plans/2026-10-14-tier2a-tool-surface-plan.md`.

## Goal

Cut the tools offered to the model from 53 to about 25, and the tool schemas
from about 31 KB to about 15 KB, without losing any capability that was
actually used.

Evidence: 52,549 tool calls since 2026-08-01, with each tool's error rate and
result summaries, plus 32 postmortems.

## Out of scope here

- **Tier 2b (LSP integration):** `definition`, `references`, `hover`,
  `symbols.find`, `repo.search kind=`, `diagnostics.check` and the post-edit
  diagnostics. The 145 LSP calls returned no useful results because of
  integration bugs: names resolved through Marshal's own index, queries sent
  at column 0, no wait for server start-up, and the server rooted at the
  project checkout rather than the session's worktree.
- **Permission-modes rework:** the `git.*` tools stay until read-only mode
  gets its own read-only command allowlist.
- **Swarm archive (separate plan):** it spans the TUI, ACP `session/swarm_start`
  and the web UI's run kinds and recipes, and it costs no prompt tokens.

## Decisions

1. **New `Internal` flag on registry tools.** An internal tool can still be
   dispatched by the runtime, but it's never offered to the model: it's left
   out of tool definitions, the envelope-mode tool list, the read-only batch
   list and the deferred announcement.
   - `file.write_patch` becomes internal. Envelope mode's `patch` action
     (unused since late September, deleted in Tier 3) still reaches it.
   - `test.run` becomes internal. The swarm tester's `TesterView` re-exposes a
     constrained, non-internal copy, so `test.run` no longer depends on the
     swarm archive.
2. **`file.edit` `{path, old_string, new_string}` replaces `file.write_patch`
   for the model.**
   - It goes through the same apply path: read-before-edit, changed-on-disk,
     backups, diff, post-edit diagnostics, near-match hints.
   - New files use `file.write`. There's no `replace_all` (YAGNI).
   - Edits to several files become parallel calls.
   - `patch.FromToolArgs` returns `[]patch.FilePatch` for either tool, so the
     approval preview, permission rules, changed-files tracking and failure
     hints treat both alike. It never turns `old_string`/`new_string` back
     into delimiter-parsed patch text, because that is exactly how `=======`
     used to corrupt edits.
   - **Legacy alias:** a permission rule or hook matcher on `file.write_patch`
     also applies to `file.edit`. The user's own config allows
     `file.write_patch` on `**`.
   - The pipeline's scoped views (fallback, artifact-writer, plan-writer)
     scope `file.edit` and `file.write` by their `path`. The plan-writer and
     artifact-writer views gain a scoped `file.write`, because they used to
     create files with empty-SEARCH patches.
   - The patch-format prompt section and the REPLACE-terminator rule are
     removed.
   - Unified-diff input (`---/+++/@@` hunks) has no `file.edit` equivalent: a
     model holding one must transcode it to `old_string`/`new_string`.
     Whole-file creation uses `file.write` and multi-file edits use parallel
     calls, so only the diff-syntax convenience is lost.
3. **Deleted tools** (calls since 2026-08-01):

   | Tool(s) | Calls |
   |---|---|
   | agent-facing `watch.*` | 6 |
   | `sessions.list`, `session.send` (plus mail injected at turn start) | 11 |
   | `ask_user` (plus `chat.go`'s fallback definition) | 0 |
   | `skill.install`, `skill.unload` | 8 / 0 |
   | `repo.map`, `repo.card`, `repo.index` | 59 / 4 / 14 |
   | `csv.inspect`, `json.query` | 0 / 6 |
   | `file.page` | 103 |

   The watch subsystem itself stays, because ACP and the web UI drive it.
4. **Background work:**
   - `agent.await` gains `peek: true`, which returns status immediately for an
     `id` or `job_id`. It absorbs `agent.output` and `job.output`.
   - `agent.output`'s `"transcript": true` option (a bounded transcript of the
     child's committed messages) is dropped rather than re-homed: `peek`
     returns the five most recent activity lines plus the summary, and no
     postmortem showed the transcript form in use.
   - `agent.kill` accepts `job_id`, absorbing `job.kill`. This deliberately
     drops `job.kill`'s `RiskCommand` level: `agent.kill` stays
     `RiskReadOnly`, so killing a background job no longer prompts. The blast
     radius is jobs the session itself started, inside the workspace. The
     alternative — splitting the risk by argument class — would make the
     approval decision depend on arguments, which the name-keyed policy does
     not express. `TestAgentKillIsReadOnlyRisk` pins the new behavior.
   - `job.list` goes, and so does `agent.await`'s `watch_id` (agents can no
     longer start watches).
5. **Subagent questions are removed and steering stays.**
   - `parent.ask`, `parent.respond` and the child-question channel go
     (session queue, TUI panel, ACP `AskChild`, `[agent]` parent-question
     settings). They had 0 calls in 2.5 months.
   - All 12 `parent.correct` calls were used to *steer* running subagents
     ("WRAP UP NOW", environment notes, scope changes). That capability
     becomes `agent.steer {id, message}` on the same steering path.
6. **History:** `recall_history` merges into `transcript_read` as a `query`
   parameter. Four postmortems record `recall_history` returning nothing, and
   two tools for one job is one too many.
7. **Scratchpad:** the four tools, the per-turn context-pack projection and
   the prompt addendum are removed (172 calls). Files under the workspace
   survive compaction just as well. The DB table and the `[scratchpad]`
   config section stay for compatibility.
8. **Conditional registration and trims:**
   - `codebase_search` is registered only when an embedding route resolves.
     Seventeen calls returned "semantic search unavailable".
   - Descriptions are trimmed for `agent.run`, `agent.await`, `agent.kill`,
     `question.ask`, `todo.write` and `transcript_read`.
   - `todoAddendum` is deleted; it repeats `todo.write`'s description.
   - **Roster:** no "cheap (no cost data…)" label without price data, only
     roles a subagent choice can use, no embedding preset, and a shorter
     guidance paragraph.

## Expected result

- Tools offered to the model: 53 → about 25.
- Tool schemas: about 31 KB → about 15 KB.
- System prompt: about 10.5 KB (after Tier 1) → about 8 KB.
- With Tier 1, the per-request prefix drops from about 18.5k to about 6k tokens.

## Measured result

Measured 2026-10-15 by `TestDefaultPromptBudget`
(`internal/app/prompt_budget_test.go`) against a default runtime in native
tools mode, with the fixture pinning `[lsp] enabled = false` and no embedding
route. Both are pinned in that fixture because both are machine-dependent:
without the LSP pin the count and the schema bytes vary with whatever
language servers happen to be on PATH. The system-prompt figure includes the
fixture's temporary working directory, which appears in the prompt, so it
moves by a byte or two between machines; the schema bytes and the tool count
do not depend on it.

| Metric | Before Tier 2a | After Tier 2a | Ceiling |
|---|---|---|---|
| Model-facing tools | 53 | 24 | — |
| Tool schemas | ~31 KB | 14,098 B | 16,000 B |
| System prompt | ~10.5 KB | 7,611 B | 9,000 B |

Both byte budgets landed inside the design's estimates. The tool count is one
under the "about 25" projection, and the gap is fully accounted for: the
measurement runs with no embedding route (so `codebase_search` is absent), no
language server (so `definition`, `references` and `hover` are absent), and
`shell.sudo` excluded — it is `Deferred`, and the walk skips deferred tools,
which are disclosed on demand via `tools.select`. `web.fetch` is present
because `[web] enabled` defaults to true.

The measurement deliberately runs at the floor of the tool surface, and the
guard test's ceilings are pinned to it so a regression cannot hide behind an
optional tool being absent. On a machine with a language server for the
repository and an embedding route, the same walk sees 27 tools and about
14,800 schema bytes; both stay under the ceilings.
