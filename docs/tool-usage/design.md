# Tool-Usage Prompting & Tool Architecture Docs — Design

Date: 2026-10-17
Status: Approved (brainstorming), pending spec review
Scope: prompting and documentation only — no changes to tool behaviour, registration, or schemas

## 1. Problem

Agents routinely reach for the wrong tool, or the right tool with the wrong
arguments. The user observes this directly as "confusion around tool calls".
The goal is to reduce it by telling agents, in the places they actually read,
which tool owns which job.

Two surfaces carry that instruction today:

- `baseRules` in `internal/agent/prompts.go` (`prompts.go:467`), the "Rules:"
  block at the head of the cached system-prompt prefix.
- Each tool's own `Description` (`internal/tools/native/*.go`,
  `internal/agent/subagent.go`, `internal/skills/tool.go`), which ships in the
  tool schema the model sees on every request.

The built-in skills are a third, weaker surface: they are ranked and loaded on
demand, and five of the eleven name no tool at all.

### 1.1 Measured evidence

From the 47 postmortem reports in `~/.config/marshal/postmortems/` (3 projects:
`marshal`, `av-portal`, `alpine-dsp`; 2026-09-22 → 2026-10-15), measured this
session:

| Bucket | Count | Status |
|---|---|---|
| Failed tool calls, all tools | 310 | — |
| `file.write_patch` failures | 150 | **already closed** — tool is now `Internal` (`file.go:547`) and the model edits through `file.edit` |
| `file.read` failures | 57 | split — dominated by reads of paths that do not exist and by oversized reads; the doubled-worktree prefix is now diagnosed (`helpers.go:251`) and ranged reads of oversized files now stream (`file.go:107`) |
| `repo.search` failures | 46 | **already closed** — the `kind` arg was removed (`c987c570`) and out-of-range `context` now clamps with a notice instead of erroring (`search.go:97`) |
| `file.write`, `shell.run`, `agent.run`, `file.edit`, `workspace.worktree` | 45 total | mixed; `agent.run` and `workspace.worktree` are live |

Error categories, all 47 reports (310 failures, every one bucketed):

| Category | Count |
|---|---|
| read-before-edit / stale-file guard | 94 |
| patch format / block matching | 50 |
| `repo.search` argument shape | 45 |
| path resolution (`stat` failed: missing, doubled, or stale paths) | 43 |
| `file.write` path rejections (absolute, non-relative, worktree-doubled) | 16 |
| cancelled / timeout | 12 |
| `agent.run` configuration | 8 |
| `file.read` paging | 3 |
| `workspace.worktree` branch precondition | 3 |
| other (tool-arg slips, one retired-tool reference, misc) | 36 |

Parse issues (1,847 summed counts): 1,627 `parse_failure` (994 `turn_metrics`,
593 `envelope`, plus patch-format entries) and 220 `repair` (152 `envelope`).

Agent-authored observations (501 entries) mention `file.read` 47 times,
`shell.run` 41, `repo.search` 27, and use the phrases "rather than" (25) and
"instead of" (25) — selection mistakes, stated in the agents' own words.

**The honest reading of this corpus.** Its two largest buckets were closed by
structural work that has already landed, not by prompting. What remains for
prompting is narrower and different in kind:

1. **Selection between sibling tools** — `shell.run` doing the work of
   `repo.search` / `symbols` / `file.read` / `git.*` / `workspace.finish`.
   The corpus records this directly (`merged the branch with shell.run git
   merge rather than the workspace.finish tool`); 4 reports mention `git
   merge` or `git stash` by hand, with one such mention being the explicit
   "rather than the workspace.finish tool" statement.
2. **Precondition surprises** — `agent.run` called with a *role* name
   (`custom agent not found: custom agent implementer`, 5 calls across 5
   reports) and `workspace.worktree` on an existing branch (3 calls).
3. **Whole-file vs matching-line confusion** — `file.read` vs `repo.search`
   vs `transcript_read`, which no description currently disambiguates.

This spec therefore does not claim to fix the 150 `file.write_patch` failures.
It targets categories 1–3, which the measurements above still show live.

## 2. Goals

1. Two rules in `baseRules` that name the tool families by job (finding and
   reading; repository state and isolation).
2. Cross-references in the descriptions of the tools agents most often confuse,
   each naming the sibling it is confused with.
3. Real tool names threaded into the five built-in skills that name none.
4. `docs/tools/` — family-grouped architecture docs describing every
   model-facing tool: schema, risk, call sequences, failure modes, and Go
   anchors.
5. A cheap Go test that fails when the docs and the registry drift apart.
6. A written outline for a future `using-tools` skill, deferred, not built.

## 3. Non-goals

- No new tools, no removed tools, no schema or handler changes.
- No change to tool registration order, risk levels, or approval policy.
- No raise of the prompt budgets. `maxSystemPromptBytes = 9000` and
  `maxToolSchemaBytes = 15000` (`internal/app/prompt_budget_test.go:19-20`) stay
  exactly as they are; this spec is the "design note saying why" that
  `internal/app/prompt_budget_test.go:15-17` requires for prefix changes.
- No `using-tools` skill is created or registered. It is outlined only.
- No attempt to fix envelope-mode JSON malformation by prompt text. That path
  is addressed structurally; a prompt cannot teach JSON syntax reliably.

## 4. Design

### 4.1 Two rules in `baseRules`

Two new constants in the rule block at `prompts.go:450-465`, appended to
**both** `baseRules` (`prompts.go:467`) and `baseRulesSystem` (`prompts.go:481`).
They are access-mode neutral, so unlike `ruleFileToolsWrite` they do not fork:

```go
rulePreferPurposeBuilt = "- Prefer repo.search, symbols, and file.read over shell.run for finding and reading code; use shell.run for building, testing, and running programs.\n"
ruleGitAndWorkspace    = "- Use the git.* tools for repository state and the workspace tools for worktree isolation and merging; do not run git merge or git stash through shell.run.\n"
```

Split deliberately, with no overlap:

- Rule 1 owns **finding and reading**.
- Rule 2 owns **repository state and isolation**.
- Neither restates `ruleFileToolsWrite` (`prompts.go:453`), which already owns
  **writing**. `file.edit` and `file.write` are deliberately absent from rule 1
  so the write instruction is paid for once, by the rule that governs it.

Byte math: 149 + 156 = **305 bytes**, against measured headroom of 1,395
(system prompt 7,605 / 9,000). Resulting prefix ≈ 7,910 bytes.

Rule 2's wording works around a hard test constraint: `workspace.finish` is a
banned string in `baseRules` (`prompts_test.go:969-984`, alongside
`.docs-archive` and `dispatch a reviewer subagent`), and `skill.load` is
likewise banned (`prompts_test.go:98`). So rule 2 says "the workspace tools"
and never names the finishing tool. The pointer lives in the tool description
instead, where it already exists (`workspace_finish.go:31`).

Rule 2 addresses the corpus's clearest wrong-tool class. `gitHygieneNote`
(`command.go:107`) already coaches on `git merge`/`git stash`, but only *after*
the command has run — a failed-call cost already paid. Rule 2 pays it up front
in the cached prefix.

### 4.2 Cross-references in tool descriptions

Each addition names the specific sibling the corpus shows agents confusing.
Budget cost is charged against the 1,401 bytes of schema headroom.

| Tool | File | Addition | Est. bytes |
|---|---|---|---|
| `shell.run` | `command.go:34` | `Use git.* for repository state and the workspace tools for worktree isolation and merging.` | ~100 |
| `agent.run` | `subagent.go:211` | narrow `"agent" names a configured custom agent.` → `... (not a role binding from the agent profiles table).` | ~55 |
| `repo.search` | `search.go:41` | `Use file.read for whole files; repo.search returns matching lines.` | ~65 |
| `transcript_read` | `transcript.go:50` | `For source code use file.read, repo.search, or symbols instead.` | ~70 |
| `codebase_search` | `codebase_search.go:24` | `For literal strings use repo.search; this is for concepts.` | ~60 |
| `workspace.worktree` | `workspace.go:23` | `Refuses when the branch already exists without a worktree; choose a branch that is not already present.` | ~105 |
| `file.read` | `file.go:87` | copyedit only: `If the file exceeds the size limit I show the head` → `the head is shown`. No new instruction. | 0 |

Total estimated addition ≈ 455 bytes. Each must be **measured** after editing
by reading the number `TestDefaultPromptBudget` logs, and reported in the
commit; the ceiling is not raised to accommodate any of them.

`file.edit` and `file.write` already cross-reference each other correctly
(`file_edit.go:24`, `file.go:761`) and are left alone.

`codebase_search` is only registered when an embedding route resolves
(`native.go:232`), so its description change is invisible in a default
configuration — which is correct, since the tool is too.

### 4.3 Threading tool names into five skills

Five built-in skills name no tool at all, so an agent reading them cannot tell
which mechanism performs the work they mandate:

| Skill | File | Threading |
|---|---|---|
| `systematic-debugging` | `systematic-debugging.md` | `shell.run` for the reproduction command; `repo.search` / `symbols` / `file.read` for reading the error's source |
| `test-driven-development` | `test-driven-development.md` | `shell.run` for the red/green runs; `file.edit` / `file.write` for the test and implementation |
| `verification-before-completion` | `verification-before-completion.md` | `shell.run` for build, tests, vet, gofmt |
| `work-decomposition` | `work-decomposition.md` | `agent.run` to dispatch one child per independent unit; `agent.await` to collect |
| `marshal-executing-plans` | `marshal-executing-plans.md` | `shell.run` for the task's verification command; `git.status` / `git.diff` / `git.log` for the staging and log steps it already describes |

Pinned substrings must survive verbatim (`internal/skills/builtin_test.go`):

- `marshal-executing-plans`: `"Commit after each verified task"` (line 46) and
  `"verbatim"` (line 211).
- `brainstorming`, `marshal-writing-plans`, `using-skills`: `docs/specs/`,
  `docs/plans/`, `verbatim`, `anchor`, `approval`, `question.ask`,
  `one question per call`, `user review`, `self-review` (pin loops at
  `builtin_test.go:184-225`).

The three other pinned skills (`brainstorming`, `marshal-writing-plans`,
`using-skills`) already name tools and are out of scope.

No skill may name a retired tool — `TestNoToolDescriptionNamesARetiredTool`
(`app_test.go:4946`) scans every skill body (the scan itself at
`app_test.go:4991-4999`) for the retired list
(`app_test.go:4973`), which includes `file.write_patch`, `symbols.find`,
`repo.search kind`, `agent.output`, `recall_history`, and `scratchpad.*`.

### 4.4 `docs/tools/` — family-grouped architecture docs

Seven files under `docs/tools/`, grouped by the job the tools do:

| File | Contents |
|---|---|
| `README.md` | How the tool surface is assembled: `RegisterAll` order (`native.go:204`), app-level additions (`agent.*`, `skill.load`), conditional registration (`codebase_search`, `symbols`, `transcript_read`, `web.*`, platform-gated `shell.sudo`), `Internal` vs `Deferred` vs model-facing, `tools.select`, risk levels, and the drift guard |
| `read-search.md` | `file.read`, `repo.search`, `symbols`, `codebase_search`, `transcript_read` |
| `editing.md` | `file.edit`, `file.write`, `file.write_patch` (internal-only; what it was, why the model no longer reaches it), read-before-edit and stale-file guards |
| `execution.md` | `shell.run`, `shell.sudo`, background jobs and `agent.await`'s `job_id` form, `todo.write` |
| `git.md` | `git.status`, `git.diff`, `git.log`, `git.blame`, `gitHygieneNote`, and the `workspace.finish` interplay |
| `delegation.md` | `agent.run`, `agent.await`, `agent.kill`, `agent.steer`, `skill.load` |
| `workspace-interaction.md` | `workspace.worktree`, `workspace.finish`, `question.ask`, `mode.request`, `tools.select`, `web.fetch`, `web.search`, `config.read` |

`todo.write` sits in `execution.md` because it describes work being run, not
work being read; its real hazard — it mutates session state despite reading
like a passive tracker — is documented there rather than used to justify a
different home. `transcript_read` stays in `read-search.md` because its
confusion is with the other read tools, which is that file's job.

Each family doc opens with a machine-readable tool index — a single HTML
comment naming the exact tools the file documents — and otherwise follows one
shape:

```markdown
<!-- tools: file.read, repo.search, symbols, codebase_search, transcript_read -->
```

The index is what the drift guard parses (§4.5). It is a line of prose that
would be redundant otherwise, and it is the price of a guard with no
false-positive surface: matching tool names in free text cannot distinguish
`repo.search` from `prompts.go`.

`README.md` carries no index line: it documents how the surface is assembled,
not a family of tools, so indexing it would duplicate the family docs and give
the guard two places to disagree with itself. The guard skips it by name.
Between them the six family indexes must cover every registered tool.

Each family doc otherwise follows one shape:

1. **What the tools are for** — the job, in one paragraph.
2. **Exact surface** — arguments, required vs optional, enums, defaults, risk
   level, and registration flags, matching the code.
3. **Call sequences** — the ordered, realistic usage these tools appear in
   (locate → read → edit → verify; worktree → work → finish; dispatch → await).
4. **Failure modes** — the real error strings the handlers emit, with what the
   agent should do instead. Examples: `repo.search mode must be
   auto|substring|regex`, `<path> is too large to read (<n> bytes; limit
   <n>)`, `file <path> was never read this session; read it before editing`,
   `path <p> contains a duplicated worktree segment`, `worktree: branch <b>
   already exists but has no worktree at <p>`, `custom agent not found`.
5. **Go anchors** — `file.go:NNN` references for the definition and the guard,
   plus a short code snippet where the snippet is the clearest explanation
   (schema literals, the stale-file guard, `gitHygieneNote`, the clamping
   notice, the doubled-worktree diagnostic).

Snippets are quoted from the tree at the cited line and must be re-verified
rather than pasted from memory. Anchors are the same style `AGENTS.md` uses in
its "Where things live" map.

`docs/ARCHITECTURE.md` gains one line pointing at `docs/tools/README.md`, so
the tool surface is reachable from the canonical map.

### 4.5 Drift guard

New test `TestToolsDocumentationDrift` in `internal/app/tools_doc_test.go`,
using the `StartRuntime` fixture pattern already established by
`TestDefaultPromptBudget` and `TestNoToolDescriptionNamesARetiredTool` in the
same package — that is the only place the *complete* registry exists, since
`agent.*` is registered by `app.Run` (`app.go:834-863`) and `skill.load` by
`skills.RegisterTool` (`tool.go:20`).

The test reads the docs from the repository root via
`filepath.Join("..", "..", "docs", "tools")`, following the existing precedent
at `app_test.go:4135` and `project_skills_test.go:13`.

Three assertions, all driven by the machine-readable index from §4.4:

1. **Registered → documented.** Every registered tool that is not `Internal`
   must appear in some `docs/tools/*.md` index. A new model-facing tool that
   ships without documentation fails here.
2. **Indexed → registered.** Every name in every index must be registered, or
   appear in an inline `docsOnlyTools` allowlist carrying a one-line reason.
   This catches the stale-reference class — a doc whose index names a tool that
   no longer exists.
3. **Retired tools are absent.** No name from the retired list
   (`app_test.go:4973`) may appear in `docs/tools/` — checked against the file
   bodies, since a retired name in prose is exactly the regression this guards.

The guard parses only the index lines, never free text: prose in these docs
necessarily mentions source files (`prompts.go`, `native.go`) and error strings,
and a name-shaped match over prose would flag those as unregistered tools.
Index lines must be the first non-blank line of each file so a doc cannot
silently drop out of the check.

The fixture config must enable what it can (web, embeddings) so the registry is
as complete as the test environment allows; names that remain unregistered only
because their configuration cannot be built in a unit test go in
`docsOnlyTools` with the reason. The implementer verifies empirically which
names land there rather than assuming.

`Internal` tools are exempt from assertion 1 and documented anyway
(`file.write_patch` in `editing.md`) — documentation is allowed to describe
what the model cannot call, as long as it never claims the model can call it.
Because assertion 2 checks the index rather than the prose, an internal tool may
be indexed and described; the doc states plainly that the model does not reach
it by default.

### 4.6 Deferred: the `using-tools` skill

The user's instruction is to outline this, not build it. A design note lands at
`docs/tool-usage/future-using-tools-skill.md` describing a skill that would:

- Explain the tool families and their boundaries (the same content as
  `docs/tools/`, in skill form).
- Be loaded on demand like any other skill, not autoloaded — the corpus shows
  skill over-loading already costs a large share of one transcript, so adding
  to the always-on prefix is the wrong move.
- Be referenced from `using-skills` (which *is* autoloaded) as a pointer, only
  if and when it is built.

Nothing is registered, and no prompt text references it.

## 5. Constraints & guards

Every constraint below was verified against the tree this session.

| Guard | Location | Consequence for this work |
|---|---|---|
| `maxSystemPromptBytes = 9000` | `prompt_budget_test.go:19` | 305-byte rule addition fits in 1,395 bytes of headroom; ceiling unchanged |
| `maxToolSchemaBytes = 15000` | `prompt_budget_test.go:20` | ~455-byte description additions fit in 1,401 bytes; ceiling unchanged |
| Banned system-prompt strings | `prompt_budget_test.go:95` | `.docs-archive`, `dispatch a reviewer subagent`, `config.agent.set`, `file.write_patch format`, `symbols.find`, `diagnostics.check`, `recall_history`, `scratchpad.write`, `Skill suggestions for this request` |
| Banned in `baseRules`/`baseRulesSystem` | `prompts_test.go:969-984` | `.docs-archive`, `dispatch a reviewer subagent`, `workspace.finish`, `>>>>>>> REPLACE` |
| `baseRules` must not name `skill.load` | `prompts_test.go:98` | Neither new rule mentions skills |
| Pinned skill substrings | `builtin_test.go:17-226` | Threading must preserve `marshal.patch`, `Commit after each verified task`, `verbatim`, `docs/specs/`, `docs/plans/`, `anchor`, `approval` |
| Retired-tool scan over descriptions and skill bodies | `app_test.go:4946-4999` | New prose may not name `file.write_patch`, `symbols.find`, `repo.search kind`, `agent.output`, `recall_history`, `scratchpad.*`, and the rest of the retired list |
| `baseRules` is embedded byte-identically | `prompts_test.go:188` | Adding to the constant is fine; no golden fixture exists to update |
| Ceiling governance | `docs/context-diet/design.md:81-82`, `prompt_budget_test.go:15-17` | Ceilings may fall later but never rise without a design note; this spec is that note |

## 6. Validation

Per change, narrowest-first:

```bash
gofmt -w internal/agent/prompts.go internal/tools/native/*.go
go test ./internal/agent/ -run 'TestBaseRules|TestBuildSystemPrompt' -v
go test ./internal/skills/ -run TestBuiltIn -v
go test ./internal/app/ -run 'TestDefaultPromptBudget|TestToolsDocumentationDrift|TestNoToolDescriptionNamesARetiredTool' -v
go test ./internal/app/... ./internal/agent/... ./internal/tools/native/... ./internal/skills/...
```

Success criteria:

- `TestDefaultPromptBudget` passes and **logs** the new byte counts, which are
  recorded in the commit message. System prompt must stay under 9,000 and
  schemas under 15,000.
- `TestToolsDocumentationDrift` passes with all seven docs present and every
  non-internal tool documented.
- No existing test is weakened, skipped, or renamed to accommodate the change.

## 7. Risks

- **Prompt-space creep.** Seven description edits plus two rules is ~760 bytes.
  Mitigation: every addition is measured, the ceilings are not raised, and each
  description change is justified by a counted failure in §1.1. The `file.read`
  edit adds no instruction at all.
- **Description edits are model-facing contracts.** Changing `agent.run`'s
  wording could alter dispatch behaviour beyond the intended clarification.
  Mitigation: additions are appended clauses; no existing instruction is
  removed or reworded except `file.read`'s first-person phrasing.
- **Docs rot.** Mitigated by the drift guard, which is the reason the guard
  exists at all.
- **The corpus ages.** Its largest buckets are already closed, so the
  remaining categories are thinner than the raw totals suggest. The design
  accepts the smaller target rather than claiming the larger win.

## 8. Decisions & trade-offs

- **Fix in place, no new always-on cost.** Rejected: a new always-on prompt
  section (costs every request) and a new autoloaded skill (the corpus shows
  skill over-loading is itself a friction source).
- **Two rules, not one.** Splitting by job keeps each rule short and
  non-overlapping, and lets a future edit change one without disturbing the
  other.
- **`workspace.finish` stays out of `baseRules`.** A test bans it there, and
  the pointer already exists in the tool description and now in rule 2's
  generic "workspace tools" phrasing.
- **Docs describe tool families, not individual tools.** Seven files, not
  thirty, so each file is short enough to hold in context and to keep current.
- **The `using-tools` skill is deferred.** Outlined in
  `docs/tool-usage/future-using-tools-skill.md`; the decision to build it is a
  separate one, made with the outline in hand.

## 9. References

- Postmortem corpus: `~/.config/marshal/postmortems/{marshal,av-portal,alpine-dsp}/`
- System prompt: `internal/agent/prompts.go` (`baseRules` at line 467)
- Budget governance: `docs/context-diet/design.md`
- Repo conventions: `AGENTS.md` — published specs live under `docs/<feature>/`
- Tool surface map: `docs/ARCHITECTURE.md`
