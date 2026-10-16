# Tool-Usage Prompting & Tool Architecture Docs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `marshal-executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce wrong-tool and wrong-argument tool calls by naming the tool families by job in the cached system-prompt rules, cross-referencing the confused siblings in tool descriptions, threading real tool names into the five built-in skills that name none, and publishing `docs/tools/` — family-grouped architecture docs guarded against registry drift by a Go test.

**Architecture:** Three code surfaces (prompt rules, tool descriptions, skill bodies) plus seven documentation files and one drift-guard test. No tool behaviour, registration, schema, or risk level changes. Every prompt addition is measured against the existing ceilings, which are never raised.

**Tech Stack:** Go 1.x with cgo (tree-sitter), SQLite. Tests use the `go test` standard library. Docs are plain Markdown.

**Spec:** `docs/tool-usage/design.md` (committed at `e423f699`)

**Authored against:** branch `main`, commit `e423f699`

---

## Global Constraints

- Build and test with `CGO_ENABLED=1`. A C toolchain is required.
- Run `gofmt -w` on every changed Go file. `go vet` must stay clean for each changed package.
- **Never raise `maxSystemPromptBytes` (9000) or `maxToolSchemaBytes` (15000)** (`internal/app/prompt_budget_test.go:19-20`). They may only fall. If an addition does not fit, cut the addition — not the ceiling.
- **Measured baseline at `e423f699`** (verified this session by running `TestDefaultPromptBudget`):
  `system prompt 7606 bytes (ceiling 9000), tool schemas 13599 bytes (ceiling 15000) across 23 model-facing tools`.
  Headroom: 1394 bytes (prompt) + 1401 bytes (schemas).
- **Never add these strings to the system prompt** (`prompt_budget_test.go:95`): `.docs-archive`, `dispatch a reviewer subagent`, `config.agent.set`, `Skill suggestions for this request`, `file.write_patch format`, `scratchpad.write`, `recall_history`, `diagnostics.check`, `symbols.find`.
- **Never add these to `baseRules` / `baseRulesSystem`** (`internal/agent/prompts_test.go:969-984`): `.docs-archive`, `dispatch a reviewer subagent`, `workspace.finish`, `>>>>>>> REPLACE`. `baseRules` must also never contain `skill.load` (`prompts_test.go:98`).
- **Never name a retired tool** in any tool description, tool schema, or skill body (`internal/app/app_test.go:4946-4999`). The list is extracted to `retiredToolNames` in Task 11; it includes `file.write_patch`, `symbols.find`, `repo.search kind`, `agent.output`, `recall_history`, `scratchpad.*`, `job.kill`, `job.list`, `session.send`, `watch_id`, and others.
- **Preserve pinned skill substrings** (`internal/skills/builtin_test.go`): `marshal.patch` (line 17), `Commit after each verified task` (line 46), `agent_observations` / `do not summarize` / `agent_pass` (line 63), `marshal-executing-plans` (line 79), `docs/specs/` / `question.ask` / `one question per call` / `user review` / `self-review` (brainstorming), `docs/plans/` / `verbatim` / `anchor` / `approval` (writing-plans), `verbatim` (executing-plans), `docs/specs/` / `docs/plans/` / `verbatim` (using-skills).
- One commit per task. Commit messages use the repo's `area: summary` style. Record the measured byte counts in the commit message for Tasks 1 and 2.
- `web/` is untouched by this plan (stdlib-only boundary, `TestWebIsStdlibOnly`).
- Do not touch `internal/app/tui/` — the TUI renders only.

## Review Focus

1. **The spec contradicts itself on `file.write_patch`.** §4.4 requires `editing.md` to document it; §4.5 assertion 3 bans every retired name from `docs/tools/`, and `file.write_patch` is in the retired list. Resolution (Task 11): assertion 3 skips any retired name that is *currently registered*. `file.write_patch` is still registered with `Internal: true` (`internal/tools/native/file.go:547`), so it is internal-only, not removed — documenting it is correct, and a genuinely removed tool still fails the guard. This is the one deliberate deviation from the spec's literal text.
2. **`codebase_search` is invisible to the budget test.** It is registered only when an embedding route resolves (`internal/tools/native/native.go:230-233`), and the budget fixture configures no embedding preset. Its 59-byte description addition therefore does not appear in the measured schema total. Do not treat the smaller-than-estimated delta as a mistake.
3. **The drift guard's fixture must enable embeddings and web search**, or `codebase_search` and `web.search` never register and assertion 1 silently under-checks. The exact fixture config is given in Task 11 and was verified this session to register 29 tools.
4. **Skill pins.** Task 3 edits five skill bodies; `internal/skills/builtin_test.go` pins substrings in three of them (`marshal-executing-plans` twice). Re-run `go test ./internal/skills/` after every skill edit.
5. **Rule 2 must not name `workspace.finish`.** It is a banned string in `baseRules`. Rule 2 says "the workspace tools"; the concrete pointer lives in the tool description (`internal/tools/native/workspace_finish.go:31`).

---

## Task 1: Two tool-selection rules in `baseRules`

**Goal:** The cached system-prompt prefix tells the model which tool family owns finding/reading and which owns repository state/isolation.

**Files:**
- Modify: `internal/agent/prompts.go` (rule const block at lines 450-465; `baseRules` at 467; `baseRulesSystem` at 481)

**Interfaces:**
- Consumes: nothing.
- Produces: constants `rulePreferPurposeBuilt` and `ruleGitAndWorkspace`, composed into both `baseRules` and `baseRulesSystem` via `baseRulesFor(systemAccess bool) string` (`prompts.go:494`).

- [ ] **Step 1: Add the two constants and give `ruleStopAfterValidation` a trailing newline.**

The rule const block currently ends:

```go
	ruleFinalAnswer          = "- Once the requested change is made and validated, produce a final answer — do not keep exploring.\n"
	ruleStopAfterValidation  = "- Stop after validation succeeds; do not re-verify work that already passed."
)
```

Replace those three lines with:

```go
	ruleFinalAnswer          = "- Once the requested change is made and validated, produce a final answer — do not keep exploring.\n"
	ruleStopAfterValidation  = "- Stop after validation succeeds; do not re-verify work that already passed.\n"
	rulePreferPurposeBuilt   = "- Prefer repo.search, symbols, and file.read over shell.run for finding and reading code; use shell.run for building, testing, and running programs.\n"
	ruleGitAndWorkspace      = "- Use the git.* tools for repository state and the workspace tools for worktree isolation and merging; do not run git merge or git stash through shell.run."
)
```

`ruleStopAfterValidation` gains a `\n` so the two new rules can follow it; the composed string still ends without a trailing newline. `ruleStopAfterValidation` is referenced only in the two compositions below (verified: `prompts.go:464`, `477`, `491`).

- [ ] **Step 2: Append both rules to `baseRules`.**

```go
const baseRules = "Rules:\n" +
	rulePreferSmallChanges +
	ruleReadBeforeEdit +
	ruleFileToolsWrite +
	ruleUntrustedText +
	ruleTraceCodePath +
	ruleNarrowestValidation +
	ruleAskUser +
	ruleFactGathering +
	ruleFinalAnswer +
	ruleStopAfterValidation +
	rulePreferPurposeBuilt +
	ruleGitAndWorkspace
```

- [ ] **Step 3: Append both rules to `baseRulesSystem`.**

```go
const baseRulesSystem = "Rules:\n" +
	rulePreferSmallChanges +
	ruleReadBeforeEdit +
	ruleFileToolsWriteSystem +
	ruleUntrustedText +
	ruleTraceCodePath +
	ruleNarrowestValidation +
	ruleAskUser +
	ruleFactGathering +
	ruleFinalAnswer +
	ruleStopAfterValidation +
	rulePreferPurposeBuilt +
	ruleGitAndWorkspace
```

Both rules are access-mode neutral, so unlike `ruleFileToolsWrite` they do not fork.

- [ ] **Step 4: Format and run the narrow tests.**

Run:
```bash
gofmt -w internal/agent/prompts.go
CGO_ENABLED=1 go test ./internal/agent/ -run 'TestBaseRules|TestBuildSystemPrompt' -count=1 -v
```
Expected: PASS. `TestBaseRulesCarryNoRepoSpecificWorkflow` must still pass — rule 2 says "the workspace tools", never `workspace.finish`.

- [ ] **Step 5: Measure the prompt budget.**

Run:
```bash
CGO_ENABLED=1 go test ./internal/app/ -run TestDefaultPromptBudget -count=1 -v
```
Expected: PASS, and the log line reads approximately
`system prompt 7911 bytes (ceiling 9000), tool schemas 13599 bytes (ceiling 15000) across 23 model-facing tools`.
The prompt delta is 305 bytes (149 + 155 + the 1-byte newline on `ruleStopAfterValidation`). Record the exact logged numbers in the commit message. If the system prompt exceeds 9000, do not raise the ceiling — shorten the rules.

- [ ] **Step 6: Commit.**

```bash
git add internal/agent/prompts.go
git commit -m "agent: add tool-selection rules to baseRules"
```
Append the measured byte counts to the commit body, e.g. `prompt 7606 -> 7911 bytes (ceiling 9000)`.

---

## Task 2: Cross-references in tool descriptions

**Goal:** Each tool the corpus shows agents confusing names the sibling it is confused with, in the description the model reads on every request.

**Files:**
- Modify: `internal/tools/native/command.go` (`shell.run`, description at lines 35-38)
- Modify: `internal/tools/native/search.go` (`repo.search`, description at line 42)
- Modify: `internal/tools/native/transcript.go` (`transcript_read`, description at lines 51-55)
- Modify: `internal/tools/native/codebase_search.go` (`codebase_search`, description at line 25)
- Modify: `internal/tools/native/workspace.go` (`workspace.worktree`, description at lines 24-25)
- Modify: `internal/tools/native/file.go` (`file.read`, description at line 87)
- Modify: `internal/agent/subagent.go` (`agent.run`, description at line 212)

**Interfaces:**
- Consumes: nothing.
- Produces: nothing new; description strings only. `file.edit` and `file.write` already cross-reference each other correctly (`file_edit.go:24`, `file.go:761`) and are left alone.

- [ ] **Step 1: `shell.run` — point at `git.*` and the workspace tools.**

In `internal/tools/native/command.go`, replace:

```go
			"Never use shell.run to create or modify files (no redirection, heredocs, or tee) — use file.write or file.edit instead. ",
```

with:

```go
			"Never use shell.run to create or modify files (no redirection, heredocs, or tee) — use file.write or file.edit instead. " +
			"Use git.* for repository state and the workspace tools for worktree isolation and merging. ",
```

- [ ] **Step 2: `repo.search` — point at `file.read` for whole files.**

In `internal/tools/native/search.go`, replace:

```go
		Description: "Search workspace files for matching lines. Default mode=auto: bare words do a case-sensitive substring match, pattern-shaped queries are compiled as RE2. context adds 0-3 surrounding lines; results cap at 200 with guidance. Use symbols to find declarations and references.",
```

with:

```go
		Description: "Search workspace files for matching lines. Default mode=auto: bare words do a case-sensitive substring match, pattern-shaped queries are compiled as RE2. context adds 0-3 surrounding lines; results cap at 200 with guidance. Use symbols to find declarations and references. Use file.read for whole files; repo.search returns matching lines.",
```

- [ ] **Step 3: `transcript_read` — point at the source-reading tools.**

In `internal/tools/native/transcript.go`, replace:

```go
			`"query" searches archived turns for a distinctive phrase (an exact command, error text, path or decision) and ignores paging.`,
```

with:

```go
			`"query" searches archived turns for a distinctive phrase (an exact command, error text, path or decision) and ignores paging. ` +
			`For source code use file.read, repo.search, or symbols instead.`,
```

- [ ] **Step 4: `codebase_search` — point at `repo.search` for literal strings.**

In `internal/tools/native/codebase_search.go`, replace:

```go
		Description: "Semantic search over the indexed codebase. Returns the most relevant code/doc snippets for a natural-language query.",
```

with:

```go
		Description: "Semantic search over the indexed codebase. Returns the most relevant code/doc snippets for a natural-language query. For literal strings use repo.search; this is for concepts.",
```

- [ ] **Step 5: `workspace.worktree` — state the branch precondition.**

In `internal/tools/native/workspace.go`, replace:

```go
			"Configured setup hooks run automatically in fresh worktrees (sandboxed, from trusted project config).",
```

with:

```go
			"Configured setup hooks run automatically in fresh worktrees (sandboxed, from trusted project config). " +
			"Refuses when the branch already exists without a worktree; choose a branch that is not already present.",
```

The refusal it describes is real: `internal/worktree/worktree.go:68` emits
`worktree: branch %s already exists but has no worktree at %s; delete the branch or remove it by hand before resuming`.

- [ ] **Step 6: `file.read` — copyedit the first-person phrasing.**

In `internal/tools/native/file.go`, replace:

```go
		Description: "Read a workspace file. If the file exceeds the size limit I show the head and how to continue (file.read start_line / repo.search). Lines are clipped at 1500 chars; ranged reads over the byte budget are truncated with a footer. Use start_line/end_line (1-based, inclusive) to page.",
```

with:

```go
		Description: "Read a workspace file. If the file exceeds the size limit, the head is shown with how to continue (file.read start_line / repo.search). Lines are clipped at 1500 chars; ranged reads over the byte budget are truncated with a footer. Use start_line/end_line (1-based, inclusive) to page.",
```

This adds no instruction — it removes the first-person voice that made the description read as narration rather than a contract.

- [ ] **Step 7: `agent.run` — disambiguate `"agent"` from a role binding.**

In `internal/agent/subagent.go`, replace:

```go
\"agent\" names a configured custom agent.", concurrency),
```

with:

```go
\"agent\" names a configured custom agent (not a role binding from the agent profiles table).", concurrency),
```

This targets the corpus's clearest `agent.run` failure: 5 calls across 5 reports passed a *role* name and got `routing: custom agent not found` (`internal/llm/routing/router.go:28`).

- [ ] **Step 8: Format, run the narrow tests, and measure.**

Run:
```bash
gofmt -w internal/tools/native/command.go internal/tools/native/search.go internal/tools/native/transcript.go internal/tools/native/codebase_search.go internal/tools/native/workspace.go internal/tools/native/file.go internal/agent/subagent.go
CGO_ENABLED=1 go test ./internal/tools/native/ -count=1
CGO_ENABLED=1 go test ./internal/agent/ -count=1
CGO_ENABLED=1 go test ./internal/app/ -run 'TestDefaultPromptBudget|TestNoToolDescriptionNamesARetiredTool' -count=1 -v
```
Expected: all PASS. The budget log should read approximately
`system prompt 7911 bytes (ceiling 9000), tool schemas 13980 bytes (ceiling 15000) across 23 model-facing tools`.
The schema delta is 381 bytes, not the spec's ~455 estimate, because `codebase_search` is not registered in the budget fixture (Review Focus 2). Record the exact logged numbers in the commit message.

- [ ] **Step 9: Commit.**

```bash
git add internal/tools/native/command.go internal/tools/native/search.go internal/tools/native/transcript.go internal/tools/native/codebase_search.go internal/tools/native/workspace.go internal/tools/native/file.go internal/agent/subagent.go
git commit -m "tools: cross-reference sibling tools in descriptions"
```
Append the measured byte counts to the commit body, e.g. `schemas 13599 -> 13980 bytes (ceiling 15000)`.

---

## Task 3: Thread tool names into five built-in skills

**Goal:** The five built-in skills that name no tool now name the mechanism that performs the work they mandate.

**Files:**
- Modify: `internal/skills/builtin/systematic-debugging.md`
- Modify: `internal/skills/builtin/test-driven-development.md`
- Modify: `internal/skills/builtin/verification-before-completion.md`
- Modify: `internal/skills/builtin/work-decomposition.md`
- Modify: `internal/skills/builtin/marshal-executing-plans.md`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing new. Skill bodies are embedded via `//go:embed` (`internal/skills/builtin.go`), so no registration changes.

- [ ] **Step 1: `systematic-debugging.md` — `shell.run` for the reproduction, read tools for the source.**

Replace:

```markdown
1. **Reproduce it.** Get a reliable command that shows the failure. If you cannot reproduce it, everything after this is speculation. For intermittent failures, run it in a loop until you have a rate.
2. **Read the actual error.** The whole message, the whole stack trace, the actual line. Not the summary you assumed it said.
```

with:

```markdown
1. **Reproduce it.** Get a reliable command that shows the failure, and run it with `shell.run`. If you cannot reproduce it, everything after this is speculation. For intermittent failures, run it in a loop until you have a rate.
2. **Read the actual error.** The whole message, the whole stack trace, the actual line. Not the summary you assumed it said. Then read the source it names with `repo.search`, `symbols`, or `file.read` — not from memory.
```

- [ ] **Step 2: `test-driven-development.md` — `shell.run` for red/green, file tools for the code.**

Replace:

```markdown
2. **Verify red** — run it and confirm it fails, *and that it fails for the right reason*. A test that passes before you write code is testing nothing.
3. **Green** — write the minimum implementation that passes.
4. **Verify green** — run it again.
```

with:

```markdown
2. **Verify red** — run it with `shell.run` and confirm it fails, *and that it fails for the right reason*. A test that passes before you write code is testing nothing.
3. **Green** — write the minimum implementation that passes, with `file.edit` (or `file.write` for a new file).
4. **Verify green** — run it again with `shell.run`.
```

- [ ] **Step 3: `verification-before-completion.md` — `shell.run` for the proof commands.**

Replace:

```markdown
## Before claiming completion

1. **Run the build.** `CGO_ENABLED=1 go build ./cmd/marshal`
```

with:

```markdown
## Before claiming completion

Run each of these with `shell.run` and read the output it returns.

1. **Run the build.** `CGO_ENABLED=1 go build ./cmd/marshal`
```

- [ ] **Step 4: `work-decomposition.md` — `agent.run` / `agent.await` for dispatch and collection.**

Replace:

```markdown
## Then decide

Two or more genuinely independent units → `dispatching-parallel-agents`.
Otherwise, do them in order yourself. Parallelism has coordination cost; it is not free.
```

with:

```markdown
## Then decide

Two or more genuinely independent units → `dispatching-parallel-agents`.
Dispatch one child per independent unit with `agent.run`, then collect the
reports with `agent.await`. Otherwise, do them in order yourself. Parallelism
has coordination cost; it is not free.
```

- [ ] **Step 5: `marshal-executing-plans.md` — `shell.run` for verification, `git.*` for staging and the log.**

Replace:

```markdown
3. Run the task's verification command(s) and confirm they pass.
4. Commit after each verified task:
   - Stage only the files the task touched (`git add <paths>`), never
     `git add -A` on an unfamiliar tree.
```

with:

```markdown
3. Run the task's verification command(s) with `shell.run` and confirm they
   pass.
4. Commit after each verified task:
   - Check what changed with `git.status` and `git.diff` first.
   - Stage only the files the task touched (`git add <paths>`), never
     `git add -A` on an unfamiliar tree.
```

Then replace:

```markdown
6. When all tasks verify, report a summary with the list of commits
   (`git log --oneline <base>..HEAD`).
```

with:

```markdown
6. When all tasks verify, report a summary with the list of commits
   (`git.log`, or `git log --oneline <base>..HEAD`).
```

The pinned substring `Commit after each verified task` is in step 4's intro line and survives both edits; `verbatim` is untouched.

- [ ] **Step 6: Run the skill tests.**

Run:
```bash
CGO_ENABLED=1 go test ./internal/skills/ -count=1 -v
CGO_ENABLED=1 go test ./internal/app/ -run TestNoToolDescriptionNamesARetiredTool -count=1 -v
```
Expected: PASS. `TestBuiltInSkillWorkflowContracts`, `TestLoadSkillsIncludesBuiltInExecutingPlansSkill`, and `TestBuiltInSkillsAllLoad` must all still pass.

- [ ] **Step 7: Commit.**

```bash
git add internal/skills/builtin/systematic-debugging.md internal/skills/builtin/test-driven-development.md internal/skills/builtin/verification-before-completion.md internal/skills/builtin/work-decomposition.md internal/skills/builtin/marshal-executing-plans.md
git commit -m "skills: name the tools that perform each mandated step"
```

---

## Task 4: `docs/tools/README.md` — how the tool surface is assembled

**Goal:** A reader can tell, from one file, which tools exist, how they get registered, and what the flags on each mean.

**Files:**
- Create: `docs/tools/README.md`

**Interfaces:**
- Consumes: nothing.
- Produces: the directory `docs/tools/` and the convention the drift guard (Task 11) enforces. `README.md` carries **no** `<!-- tools: ... -->` index line — the guard skips it by name, because it documents assembly rather than a family of tools.

- [ ] **Step 1: Create `docs/tools/README.md` with these sections.**

1. `# Tool architecture` — one paragraph: this directory documents the model-facing tool surface, grouped by the job each tool does. Each family file opens with a machine-readable index line that `TestToolsDocumentationDrift` (`internal/app/tools_doc_test.go`) checks against the live registry.
2. `## How the surface is assembled` — the three registration sites, in order:
   - `native.RegisterAll` (`internal/tools/native/native.go:204`) registers the base set in a fixed order: `file.read`, `file.write_patch`, `file.edit`, `file.write`, `repo.search`, `git.status`, `git.diff`, `git.log`, `git.blame`, `shell.run`, `test.run`, `todo.write`, `question.ask`, `mode.request`, `tools.select`, `workspace.worktree`, `workspace.finish`.
   - Conditional additions inside `RegisterAll`: `codebase_search` (only when an embedding route resolves, `native.go:230-233`), `shell.sudo` (platform-gated, `sudo_register.go` / `sudo_register_windows.go`), `symbols` (only when a querier is wired, `native.go:238-241`), `transcript_read` (only when session state exists, `native.go:244-248`), `config.read` (`native.go:253`), `web.fetch` and `web.search` (`native.go:254-258`).
   - App-level additions after the policy engine exists: `agent.run`, `agent.await`, `agent.kill`, `agent.steer` (`internal/app/app.go:834-863`) and `skill.load` (`internal/skills/tool.go:20`, called from `app.go:799`).
   - Also registered outside `native`: desktop `browser.*` tools when `[desktop] enabled` (`internal/tools/desktop/tools.go:108`) and MCP tools when a server is configured (`internal/tools/mcp/manager.go:339`). These are documented in their own packages, not here.
3. `## Registration flags` — a table over `registry.Tool` (`internal/tools/registry/types.go:32-49`): `Risk`, `Cacheable`, `Deferred`, `Internal`. State plainly that `Internal` means "dispatchable by the runtime but never offered to the model — no tool definition, no prompt listing, no deferred announcement", and that `Deferred` means "hidden until `tools.select` loads it".
4. `## Risk levels` — the six levels from `registry/types.go:11-20`: `read_only`, `workspace_write`, `command`, `network`, `destructive`, `environment`, with one line each on what they gate.
5. `## tools.select` — deferred disclosure: `tools.select` (`internal/tools/native/tools_select.go:23`) loads named deferred tools for the rest of the session; unknown or non-deferred names are rejected.
6. `## The drift guard` — the index-line contract: the first non-blank line of each family file is `<!-- tools: name1, name2 -->`; every registered non-internal tool must appear in some index; every indexed name must be registered or listed in `docsOnlyTools`; no retired tool name may appear anywhere in the directory.

- [ ] **Step 2: Verify the file exists and carries no index line.**

Run:
```bash
test -f docs/tools/README.md && ! grep -q '<!-- tools:' docs/tools/README.md && echo OK
```
Expected: `OK`.

- [ ] **Step 3: Commit.**

```bash
git add docs/tools/README.md
git commit -m "docs: add docs/tools/README.md describing tool-surface assembly"
```

---

## Task 5: `docs/tools/read-search.md`

**Goal:** The five read/search tools are documented with their exact surface, the sequences they appear in, and the errors they emit.

**Files:**
- Create: `docs/tools/read-search.md`

**Interfaces:**
- Consumes: the index-line convention from Task 4.
- Produces: an index line the drift guard (Task 11) parses.

- [ ] **Step 1: Create the file. The first non-blank line must be exactly:**

```markdown
<!-- tools: file.read, repo.search, symbols, codebase_search, transcript_read -->
```

Then `# Reading and searching`, then the five sections below.

- [ ] **Step 2: Write the five sections, each with the same five parts** (what it is for; exact surface; call sequence; failure modes; Go anchors).

**`file.read`** — `internal/tools/native/file.go:86`. Risk `read_only`. Args: `path` (required), `start_line`, `end_line` (1-based, inclusive). Lines clipped at 1500 chars; ranged reads over the byte budget truncate with a footer. Failure modes to quote verbatim: `file.read path is required` (`file.go:99`), `file.read start_line must be <= end_line` (`file.go:102`), and the oversize error `%w: %s is too large to read (%d bytes; limit %d)` (`file.go:431`). Anchor the oversized-range streaming path at `file.go:105-115`.

**`repo.search`** — `internal/tools/native/search.go:41`. Risk `read_only`. Args: `query` (required), `path`, `max_results`, `mode` (`auto|substring|regex`), `include`, `context` (0-3). Defaults: `max_results` 200 hard cap; `mode=auto` treats pattern-shaped queries as RE2 when they compile and falls back to a literal substring match silently. Failure modes: `repo.search query is required` (`search.go:55`), `repo.search invalid regex %q: %w` (`search.go:89`), `repo.search mode must be auto|substring|regex, got %q` (`search.go:94`). Document the clamp: an out-of-range `context` is coerced into `[0,3]` and a `context_clamped` notice carries the remediation instead of an error (`search.go:97-115`, notice kind `registry.NoticeContextClamped` at `internal/tools/registry/types.go:94`). Quote the comment explaining why.

**`symbols`** — `internal/tools/native/symbols.go:26`. Risk `read_only`. Args: `name` (required), `op` (`find|definition|references|hover`, default `definition`), `path`, `line`. Registered only when a querier is wired (`native.go:238-241`); answers through the language server first, the symbol index as fallback. Note that `name` may be qualified as `Type.Method`.

**`codebase_search`** — `internal/tools/native/codebase_search.go:24`. Risk `read_only`. Args: `query` (required), `limit`, `path`. Registered only when an embedding route resolves (`native.go:230-233`). When no embedder is configured the handler returns content, not an error: `Semantic search is not configured: no embedding model configured. Set an \`embedding\` role in your profile to enable it.` (`codebase_search.go:42`).

**`transcript_read`** — `internal/tools/native/transcript.go:50`. Risk `read_only`. Args: `query`, `limit_matches`, `generation_seq`, `session_id`, `turn_seq`, `offset`, `limit` (default 20, max 100), `max_chars` (default 16000, max 64000), `include_all`. Document why `generation_seq` and `turn_seq` are pointers: an explicit `0` must be distinguishable from an omitted field (`transcript.go:59-64`).

- [ ] **Step 3: Add the call sequence** the family exists for: locate → read → edit → verify. Concretely: `repo.search` or `symbols` to find the site, `file.read` with `start_line`/`end_line` to read it, `file.edit` to change it, `shell.run` to verify. State the boundary the descriptions now carry: `repo.search` returns matching lines, `file.read` returns whole files, `codebase_search` is for concepts, `transcript_read` is for this session's history.

- [ ] **Step 4: Verify the index line parses.**

Run:
```bash
head -1 docs/tools/read-search.md
```
Expected: `<!-- tools: file.read, repo.search, symbols, codebase_search, transcript_read -->`

- [ ] **Step 5: Commit.**

```bash
git add docs/tools/read-search.md
git commit -m "docs: document the read/search tool family"
```

---

## Task 6: `docs/tools/editing.md`

**Goal:** The three file-writing tools are documented, including the internal-only patch tool and the guards that make editing safe.

**Files:**
- Create: `docs/tools/editing.md`

**Interfaces:**
- Consumes: the index-line convention from Task 4.
- Produces: an index line the drift guard (Task 11) parses.

- [ ] **Step 1: Create the file. The first non-blank line must be exactly:**

```markdown
<!-- tools: file.edit, file.write, file.write_patch -->
```

Then `# Editing files`, then the sections below.

- [ ] **Step 2: Document `file.edit`** — `internal/tools/native/file_edit.go:21`. Risk `workspace_write`. Args: `path`, `old_string`, `new_string` (all required). `old_string` must match byte-for-byte and be unique. Failure modes: `file.edit: old_string is required; use file.write to create or overwrite a whole file` (`file_edit.go:36`), `file.edit: old_string and new_string are identical; nothing to change` (`file_edit.go:39`).

- [ ] **Step 3: Document `file.write`** — `internal/tools/native/file.go:759`. Risk `workspace_write`. Args: `path`, `content` (both required). Creates or overwrites a whole file; shares `file.write_patch`'s plumbing — root resolution, the stale-file guard, backups, diff generation, diagnostics, and changed-files tracking (`file.go:753-756`). New files skip the stale/read guard; existing files must have been read this session.

- [ ] **Step 4: Document `file.write_patch` as internal-only.** — `internal/tools/native/file.go:533`. Risk `workspace_write`, `Internal: true` (`file.go:547`). State plainly that **the model does not reach this tool by default**: it is reached only by envelope-mode patch actions, and the model edits through `file.edit`. Document what it was (search/replace blocks with a terminator line, plus unified-diff acceptance) and why the model no longer reaches it — the corpus's single largest failure bucket was 150 `file.write_patch` failures, closed structurally by making the tool internal rather than by prompting.

  **Do not quote the retired patch terminator string in this file.** It is a banned string in `baseRules` (`prompts_test.go:969-984`) and, while that test only scans `baseRules`, keeping the retired patch format out of published docs is the point of the change. Describe the format in prose instead.

- [ ] **Step 5: Document the two guards, with snippets.**

The read-before-edit / stale-file guard, quoted from `internal/tools/native/file.go:601` and `file.go:811`:

```go
				return registry.ToolResult{}, fmt.Errorf(
					"file %s was never read this session; read it before editing", fp.Path)
```

The doubled-worktree diagnostic, quoted from `internal/tools/native/helpers.go:268`:

```go
	return fmt.Errorf("%w: path %q contains a duplicated worktree segment; relative paths resolve from the current worktree root — did you mean %q?", ErrPathEscapes, rel, suggestion)
```

Explain the doubled-worktree case: a path that repeats the `.marshal/worktrees/<branch>/` prefix is rejected with a suggestion, unless the literal path really exists (`helpers.go:251-272`).

- [ ] **Step 6: Verify the index line parses.**

Run:
```bash
head -1 docs/tools/editing.md
```
Expected: `<!-- tools: file.edit, file.write, file.write_patch -->`

- [ ] **Step 7: Commit.**

```bash
git add docs/tools/editing.md
git commit -m "docs: document the editing tool family and its guards"
```

---

## Task 7: `docs/tools/execution.md`

**Goal:** Command execution and the todo list are documented, including background jobs and the `agent.await` `job_id` form.

**Files:**
- Create: `docs/tools/execution.md`

**Interfaces:**
- Consumes: the index-line convention from Task 4.
- Produces: an index line the drift guard (Task 11) parses.

- [ ] **Step 1: Create the file. The first non-blank line must be exactly:**

```markdown
<!-- tools: shell.run, shell.sudo, test.run, todo.write -->
```

Then `# Running commands`, then the sections below.

- [ ] **Step 2: Document `shell.run`** — `internal/tools/native/command.go:34`. Risk `command`. Args: `command` (required), `timeout_seconds`, `background`. Runs with the workspace root as cwd; subject to conservative guardrails. Failure mode: `command is required` (`command.go:51`, `command.go:121`). Document the `background: true` form: it returns `job_id: <id>` immediately (`command.go:61-65`), and the job is collected with `agent.await`'s `job_id` form.

- [ ] **Step 3: Document `shell.sudo`** — `internal/tools/native/sudo.go:154`. Risk `destructive`, `Deferred: true`. Platform-gated: registered only on non-Windows (`sudo_register.go` has `//go:build !windows`; `sudo_register_windows.go` returns an empty slice). Document the security contract from the package comment: the password is read from a masked prompt and delivered on the child's stdin — never argv, never the environment, and no PTY (`internal/tools/native/native.go:126-129`). When no `SudoPolicy` is wired the tool refuses every command (fail closed, `native.go:91-93`).

- [ ] **Step 4: Document `test.run` as internal-only** — `internal/tools/native/command.go:78`. Risk `command`, `Internal: true` (`command.go:85`). State plainly that the general agent runs tests through `shell.run`; only the swarm tester (`TesterView`) receives this constrained tool. Args: `command` (optional override of the configured test command).

- [ ] **Step 5: Document `todo.write`** — `internal/tools/native/todos.go:51`. Risk `workspace_write`. Args: `todos` (required array of `{id?, content, status}`), `drop_unfinished`. Statuses: `pending`, `in_progress`, `completed`. Document the hazard the spec calls out: **it mutates session state despite reading like a passive tracker.** It *replaces* the list; unfinished items omitted from the submission are carried over unless `drop_unfinished: true` (`todos.go:88-105`). Identity and timestamps are the store's, never the model's (`todos.go:82-86`). Failure modes: `todo %d has empty content; each item needs a non-empty %q string describing the task` (`todos.go:67`), `invalid todo status %q; use pending|in_progress|completed` (`todos.go:72`), `todo store not available` (`todos.go:76`).

- [ ] **Step 6: Add the call sequence** for background work: `shell.run` with `background: true` → `job_id` → `agent.await` with that `job_id` (or `agent.await` with `any: true` to take the first to finish). Note that `agent.await` and `agent.kill` cover both subagents and shell jobs (`internal/app/app.go:846-862`).

- [ ] **Step 7: Verify the index line parses.**

Run:
```bash
head -1 docs/tools/execution.md
```
Expected: `<!-- tools: shell.run, shell.sudo, test.run, todo.write -->`

- [ ] **Step 8: Commit.**

```bash
git add docs/tools/execution.md
git commit -m "docs: document the execution tool family"
```

---

## Task 8: `docs/tools/git.md`

**Goal:** The four read-only git tools and the `git merge` / `git stash` hygiene nudge are documented, together with how they relate to `workspace.finish`.

**Files:**
- Create: `docs/tools/git.md`

**Interfaces:**
- Consumes: the index-line convention from Task 4.
- Produces: an index line the drift guard (Task 11) parses.

- [ ] **Step 1: Create the file. The first non-blank line must be exactly:**

```markdown
<!-- tools: git.status, git.diff, git.log, git.blame -->
```

Then `# Repository state`, then the sections below.

- [ ] **Step 2: Document the four tools**, all risk `read_only`, all in `internal/tools/native/git.go`:

| Tool | Line | Args | Notes |
|---|---|---|---|
| `git.status` | `git.go:17` | none | `git status --short`; summary is `working tree clean` or `working tree has changes` |
| `git.diff` | `git.go:38` | `path` (optional) | `git diff --` or `git diff -- <path>`; summary `no diff` / `diff present` |
| `git.log` | `git.go:101` | `path` (optional), `limit` (default 20, max 100) | `git log --format='%h %ad %an %s' --date=short` |
| `git.blame` | `git.go:142` | `path` (required), `start_line`, `end_line` | whole-file blame caps at 200 lines with a marker; range blame uses `-L` |

Failure mode to quote: `git.blame: start_line and end_line must be given together with 1 <= start_line <= end_line` (`git.go:156`).

- [ ] **Step 3: Document `gitHygieneNote`** — `internal/tools/native/command.go:107`. Quote it verbatim:

```go
func gitHygieneNote(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	switch {
	case strings.HasPrefix(trimmed, "git merge ") || trimmed == "git merge":
		return "note: for session work prefer workspace.finish over hand-merging; pop or drop any stash you create before finishing."
	case strings.HasPrefix(trimmed, "git stash"):
		return "note: remember to pop or drop this stash when done."
	}
	return ""
}
```

Explain the timing problem this note has: it fires **after** the command has already run, so the failed-call cost is already paid. That is why rule 2 in `baseRules` (Task 1) pays it up front in the cached prefix, and why `shell.run`'s description now points at `git.*` and the workspace tools.

- [ ] **Step 4: Document the `workspace.finish` interplay.** `workspace.finish` (`internal/tools/native/workspace_finish.go:31`) already carries the pointer: `Use this instead of shell.run git merge, and pop or drop any stash you created before calling it.` Cross-link to `workspace-interaction.md` (Task 10) rather than duplicating its surface.

- [ ] **Step 5: Verify the index line parses.**

Run:
```bash
head -1 docs/tools/git.md
```
Expected: `<!-- tools: git.status, git.diff, git.log, git.blame -->`

- [ ] **Step 6: Commit.**

```bash
git add docs/tools/git.md
git commit -m "docs: document the git tool family and merge hygiene"
```

---

## Task 9: `docs/tools/delegation.md`

**Goal:** Subagent dispatch and skill loading are documented, including the async contract and the `"agent"` argument's real meaning.

**Files:**
- Create: `docs/tools/delegation.md`

**Interfaces:**
- Consumes: the index-line convention from Task 4.
- Produces: an index line the drift guard (Task 11) parses.

- [ ] **Step 1: Create the file. The first non-blank line must be exactly:**

```markdown
<!-- tools: agent.run, agent.await, agent.kill, agent.steer, skill.load -->
```

Then `# Delegation`, then the sections below.

- [ ] **Step 2: Document `agent.run`** — `internal/agent/subagent.go:211`. Risk `workspace_write`. Args: `prompt`, `description` (both required), `agent`, `model`, `temperature`, `thinking`, `system`. Document the async contract the description teaches: it returns a handle immediately and the report arrives later as a `[subagent N finished]` message; a model that assumes a synchronous result will hallucinate completions. The concurrency cap is stated from `state.SubagentMaxConcurrency()`, not hardcoded (`subagent.go:204-207`). Failure modes: `agent.run: system access requested but this session does not have it` (`subagent.go:236`), and the routing error `routing: custom agent not found` (`internal/llm/routing/router.go:28`) — which is what the `"agent"` argument now disclaims: it names a **configured custom agent**, not a role binding from the agent profiles table. Note that the child has the parent's tools except `agent.run` and `question.ask`.

- [ ] **Step 3: Document `agent.await`, `agent.kill`, `agent.steer`** — `internal/agent/subagent_async.go:40`, `:442`, `:517`. All risk `read_only`. `agent.await` takes `id` (subagent), `job_id` (shell job), `any`, `all`, `peek`, `timeout_seconds`; `agent.kill` takes `id` or `job_id`; `agent.steer` takes `id` and `message` and is main-agent only — `SubtaskScopeView` excludes it so a child cannot steer a sibling (`internal/app/app.go:860-862`).

- [ ] **Step 4: Document `skill.load`** — `internal/skills/tool.go:20`. Risk `read_only`, `Cacheable: false`. Args: `name` (required). Loading an already-active skill is a **re-fetch, not an error**: it resets the skill's body age and returns nil (`tool.go:93-97`, `tool.go:107-110`), which is how a model recovers a body that has aged out of the wire. Failure mode: `unknown skill %q. Available: %v` (`tool.go:104`). Note that `skills.max_active` bounds how many load and that the least-recently-activated non-pinned skill is evicted rather than the new load being refused (`tool.go:138-160`).

- [ ] **Step 5: Add the call sequence:** decompose → `agent.run` per independent unit → `agent.await` to collect → `agent.steer` to redirect a running child → `agent.kill` to stop one. Cross-link to the `work-decomposition` and `dispatching-parallel-agents` skills.

- [ ] **Step 6: Verify the index line parses.**

Run:
```bash
head -1 docs/tools/delegation.md
```
Expected: `<!-- tools: agent.run, agent.await, agent.kill, agent.steer, skill.load -->`

- [ ] **Step 7: Commit.**

```bash
git add docs/tools/delegation.md
git commit -m "docs: document the delegation tool family"
```

---

## Task 10: `docs/tools/workspace-interaction.md`

**Goal:** Worktree isolation, merging, user interaction, deferred-tool disclosure, network access, and config inspection are documented.

**Files:**
- Create: `docs/tools/workspace-interaction.md`

**Interfaces:**
- Consumes: the index-line convention from Task 4.
- Produces: an index line the drift guard (Task 11) parses.

- [ ] **Step 1: Create the file. The first non-blank line must be exactly:**

```markdown
<!-- tools: workspace.worktree, workspace.finish, question.ask, mode.request, tools.select, web.fetch, web.search, config.read -->
```

Then `# Workspace and interaction`, then the sections below.

- [ ] **Step 2: Document `workspace.worktree`** — `internal/tools/native/workspace.go:23`. Risk `workspace_write`. Args: `branch` (optional; `{}` returns to the project root). Failure modes: `workspace.worktree requires a session` (`workspace.go:36`), and the branch precondition `worktree: branch %s already exists but has no worktree at %s; delete the branch or remove it by hand before resuming` (`internal/worktree/worktree.go:68`) — the 3-call failure class the description now warns about. Note there is deliberately no removal path (`workspace.go:20`).

- [ ] **Step 3: Document `workspace.finish`** — `internal/tools/native/workspace_finish.go:29`. Risk `workspace_write`. Args: `message`, `squash` (default true), `target`. Merges the session's worktree branch into the base branch, then removes the worktree and deletes the branch. Guards: refuses when the project checkout is dirty or its HEAD moved. A refusal is **content, not an error** — the agent must read it and act (`workspace_finish.go:25-26`). Failure mode: `workspace.finish requires a session` (`workspace_finish.go:43`).

- [ ] **Step 4: Document `question.ask`** — `internal/tools/native/question.go:25`. Risk `read_only`. Args: `questions` (required array of `{question, options?, multi?}`); options are strings or `{label, description}` objects. It blocks on the user's response channel. Failure mode: `at least one question is required` (`question.go:36`). Document the hand-rolled validator `ValidateQuestionAsk` (`question.go:100`) and why it exists: the generic schema check would report `arguments are not valid JSON`, so this one names the exact field and embeds a valid example. Note the two paths must agree — a single call goes through the validator, the same tool inside an `actions[]` batch goes through `registry.ValidateArgs` against the schema (`question.go:95-97`).

- [ ] **Step 5: Document `mode.request`** — `internal/tools/native/mode_request.go:35`. Risk `read_only`. Args: `mode` (required), `system`. The handler posts a pending approval with a `mode-elevation:` reason prefix and blocks; it does **not** apply the switch itself — the responding transport (TUI or ACP) owns that (`mode_request.go:29-31`). Failure mode: `mode is required` (`mode_request.go:46`).

- [ ] **Step 6: Document `tools.select`** — `internal/tools/native/tools_select.go:23`. Risk `read_only`. Args: `names` (required array). Loads named deferred tools for the rest of the session; rejects unknown or non-deferred names. Failure modes: `tool registry not available` (`tools_select.go:34`), `session state not available` (`tools_select.go:37`).

- [ ] **Step 7: Document `web.fetch` and `web.search`** — `internal/tools/native/web.go:44` and `:123`. Both risk `network`. `web.fetch` takes `url` (required); `web.search` takes `query` (required). Registration: `web.fetch` whenever `[web] enabled` (default true, `internal/app/config/defaults.go:129-132`); `web.search` additionally requires `search_url` (`native.go:254-258`). Failure modes: `web tools are disabled` (`web.go:55`, `web.go:134`), `only http(s) URLs are allowed` (`web.go:63`), `URL resolves to a private or link-local address` (`web.go:66`), `web search provider is not configured` (`web.go:137`). Document the SSRF posture: private, loopback, and link-local addresses are refused, redirects are re-checked and capped at 5, and the connection is pinned to the first resolved IP to prevent DNS rebinding (`web.go:192-232`). Note that unattended children (the SDD pipeline's role runners) never receive these tools.

- [ ] **Step 8: Document `config.read`** — `internal/tools/native/config_read.go:14`. Risk `read_only`, `Deferred: true`, `Cacheable: true`. Args: `sections` (optional array of top-level section names). Returns the merged project + global config with `api_key` and `search_key` masked to `"***"`. Note that settings are changed through `/settings`, `/models`, or `/connect` — not through a tool.

- [ ] **Step 9: Verify the index line parses.**

Run:
```bash
head -1 docs/tools/workspace-interaction.md
```
Expected: `<!-- tools: workspace.worktree, workspace.finish, question.ask, mode.request, tools.select, web.fetch, web.search, config.read -->`

- [ ] **Step 10: Commit.**

```bash
git add docs/tools/workspace-interaction.md
git commit -m "docs: document the workspace and interaction tool family"
```

---

## Task 11: `TestToolsDocumentationDrift`

**Goal:** A Go test fails when `docs/tools/` and the live tool registry drift apart, in either direction, or when a retired tool name reappears in the docs.

**Files:**
- Modify: `internal/app/app_test.go` (extract the retired list to a package-level var)
- Create: `internal/app/tools_doc_test.go`

**Interfaces:**
- Consumes: the six index lines from Tasks 5-10 and the `README.md` skip convention from Task 4.
- Produces: `TestToolsDocumentationDrift`, `retiredToolNames`, `docsOnlyTools`.

- [ ] **Step 1: Extract the retired list in `internal/app/app_test.go`.**

Inside `TestNoToolDescriptionNamesARetiredTool`, delete the declaration line:

```go
	retired := []string{"file.page", "repo.map", "repo.card", "repo.index", "ask_user", "skill.install", "skill.unload", "csv.inspect", "json.query", "sessions.list", "session.send", "watch.start", "agent.output", "job.output", "job.kill", "job.list", "recall_history", "scratchpad.", "parent.respond", "parent.ask", "parent.correct", "file.write_patch", "watch_id", "symbols.find", "diagnostics.check", "repo.search kind"}
```

Then change **both** `for _, name := range retired {` loops (one over tool descriptions, one over skill bodies) to `for _, name := range retiredToolNames {`.

Then add, immediately above the `TestNoToolDescriptionNamesARetiredTool` doc comment:

```go
// retiredToolNames are tool names (and name-shaped fragments) the model must
// never be taught again. Shared by TestNoToolDescriptionNamesARetiredTool and
// TestToolsDocumentationDrift so the two cannot drift apart.
var retiredToolNames = []string{"file.page", "repo.map", "repo.card", "repo.index", "ask_user", "skill.install", "skill.unload", "csv.inspect", "json.query", "sessions.list", "session.send", "watch.start", "agent.output", "job.output", "job.kill", "job.list", "recall_history", "scratchpad.", "parent.respond", "parent.ask", "parent.correct", "file.write_patch", "watch_id", "symbols.find", "diagnostics.check", "repo.search kind"}
```

- [ ] **Step 2: Create `internal/app/tools_doc_test.go`.**

```go
package app

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"marshal/internal/trust"
)

// docsOnlyTools names tools that docs/tools/ documents but that a unit-test
// runtime cannot register. Every entry needs a reason. On Linux/macOS the map
// is empty in practice — shell.sudo registers there — but the entry keeps the
// guard honest on Windows, where sudo_register_windows.go returns no tools.
var docsOnlyTools = map[string]string{
	"shell.sudo": "platform-gated: registered only on non-Windows (sudo_register.go build tag)",
}

// toolsDocIndexRe matches the machine-readable index line at the top of each
// docs/tools family file: <!-- tools: a, b, c -->
var toolsDocIndexRe = regexp.MustCompile(`^<!--\s*tools:\s*(.*?)\s*-->$`)

// TestToolsDocumentationDrift keeps docs/tools/ and the live tool registry in
// step. Three assertions, all driven by the index line at the top of each
// family doc:
//
//  1. every registered, non-internal tool is documented somewhere;
//  2. every documented name is registered (or in docsOnlyTools);
//  3. no retired tool name appears anywhere in the docs.
//
// Only the index line is parsed. Prose in these docs necessarily names source
// files (prompts.go, native.go) and error strings, and a name-shaped match
// over prose would flag those as unregistered tools.
func TestToolsDocumentationDrift(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	// The fixture enables what it can (embeddings, web search) so the registry
	// is as complete as a unit test allows: codebase_search needs a resolvable
	// embedding route and web.search needs a search URL.
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
[lsp]
enabled = false
[indexing]
embedding_preset = "mock/m"
[web]
enabled = true
search_url = "http://localhost:9999/search"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	registered := map[string]bool{}
	for _, tl := range rt.Runner.Registry.List() {
		registered[tl.Name] = true
	}
	if len(registered) == 0 {
		t.Fatal("no tools registered; the drift check below would be vacuous")
	}

	dir := filepath.Join("..", "..", "docs", "tools")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	indexed := map[string]string{} // tool name -> file that indexes it
	var docFiles []string
	docText := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		docFiles = append(docFiles, e.Name())
		docText[e.Name()] = string(raw)

		// README.md documents how the surface is assembled, not a family of
		// tools, so it carries no index line. It is still scanned for retired
		// names below.
		if e.Name() == "README.md" {
			continue
		}

		first := ""
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				first = strings.TrimSpace(line)
				break
			}
		}
		m := toolsDocIndexRe.FindStringSubmatch(first)
		if m == nil {
			t.Errorf("%s: first non-blank line must be a <!-- tools: ... --> index, got %q", e.Name(), first)
			continue
		}
		for _, name := range strings.Split(m[1], ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if prev, dup := indexed[name]; dup {
				t.Errorf("tool %s is indexed by both %s and %s", name, prev, e.Name())
				continue
			}
			indexed[name] = e.Name()
		}
	}
	if len(indexed) == 0 {
		t.Fatal("no tool index lines found under docs/tools; the drift check would be vacuous")
	}

	// 1. Registered -> documented.
	var undocumented []string
	for name := range registered {
		tl, _ := rt.Runner.Registry.Lookup(name)
		if tl.Internal {
			continue
		}
		if _, ok := indexed[name]; !ok {
			undocumented = append(undocumented, name)
		}
	}
	sort.Strings(undocumented)
	for _, name := range undocumented {
		t.Errorf("tool %s is registered and model-facing but not indexed by any docs/tools file", name)
	}

	// 2. Indexed -> registered.
	var stale []string
	for name, file := range indexed {
		if registered[name] {
			continue
		}
		if _, ok := docsOnlyTools[name]; ok {
			continue
		}
		stale = append(stale, name+" ("+file+")")
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("docs/tools indexes %s, which is not registered and not in docsOnlyTools", s)
	}

	// 3. Retired names absent. A name that is still registered is not retired
	// in the registry sense and may be documented: file.write_patch is
	// Internal, not removed, and editing.md documents it deliberately.
	for _, name := range retiredToolNames {
		if registered[name] {
			continue
		}
		for _, file := range docFiles {
			if strings.Contains(docText[file], name) {
				t.Errorf("docs/tools/%s mentions retired tool %s", file, name)
			}
		}
	}
}
```

- [ ] **Step 3: Run it.**

Run:
```bash
gofmt -w internal/app/tools_doc_test.go internal/app/app_test.go
CGO_ENABLED=1 go test ./internal/app/ -run 'TestToolsDocumentationDrift|TestNoToolDescriptionNamesARetiredTool' -count=1 -v
```
Expected: PASS.

If assertion 1 reports an undocumented tool, the fix is to add it to the right family index — not to widen `docsOnlyTools`. If assertion 2 reports a stale name, the index line has a typo or names a tool that no longer exists. If assertion 3 fires, a doc quotes a retired name; rewrite the prose.

- [ ] **Step 4: Confirm the guard bites.** Temporarily delete one name from a family index line (for example `symbols` from `read-search.md`), re-run, and confirm assertion 1 fails naming `symbols`. Restore the line and re-run to confirm PASS. Do not commit the temporary edit.

- [ ] **Step 5: Commit.**

```bash
git add internal/app/app_test.go internal/app/tools_doc_test.go
git commit -m "app: add TestToolsDocumentationDrift guarding docs/tools against registry drift"
```

---

## Task 12: `docs/ARCHITECTURE.md` pointer and the deferred `using-tools` outline

**Goal:** The tool surface is reachable from the canonical architecture map, and the deferred skill idea is written down without being built.

**Files:**
- Modify: `docs/ARCHITECTURE.md` (Key layers table, `Tools` row at line 85)
- Create: `docs/tool-usage/future-using-tools-skill.md`

**Interfaces:**
- Consumes: `docs/tools/README.md` from Task 4.
- Produces: nothing executable. No skill is registered and no prompt text references the outline.

- [ ] **Step 1: Add the pointer to `docs/ARCHITECTURE.md`.**

The `Tools` row currently reads:

```markdown
| Tools | `internal/tools/registry/`, `internal/tools/native/`, `internal/tools/patch/`, `internal/tools/policy/`, `internal/tools/mcp/` | Tool registration, file/search/shell/git tools, patch apply, risk policy, MCP clients. |
```

Replace it with:

```markdown
| Tools | `internal/tools/registry/`, `internal/tools/native/`, `internal/tools/patch/`, `internal/tools/policy/`, `internal/tools/mcp/` | Tool registration, file/search/shell/git tools, patch apply, risk policy, MCP clients. The model-facing surface is documented family-by-family in [`docs/tools/`](tools/README.md). |
```

- [ ] **Step 2: Create `docs/tool-usage/future-using-tools-skill.md`.**

It must state, in prose:

- **What it would be:** a skill that explains the tool families and their boundaries — the same content as `docs/tools/`, in skill form.
- **Why it is deferred:** it would be loaded on demand like any other skill, not autoloaded. The postmortem corpus shows skill over-loading already costs a large share of one transcript, so adding to the always-on prefix is the wrong move. The two `baseRules` rules (Task 1) and the description cross-references (Task 2) cover the highest-frequency confusions at a fraction of the cost.
- **How it would be wired if built:** registered as a built-in skill in `internal/skills/builtin.go` with a `//go:embed` directive, and referenced from `using-skills` (which *is* autoloaded) as a pointer — only if and when it is built.
- **What would have to be true first:** evidence that the rules and descriptions did not close the selection errors they target. The corpus is the measurement; re-run the postmortem analysis before deciding.
- **Explicitly:** nothing is registered today, and no prompt text references this file.

- [ ] **Step 3: Verify.**

Run:
```bash
grep -n 'docs/tools/' docs/ARCHITECTURE.md
test -f docs/tool-usage/future-using-tools-skill.md && echo OK
CGO_ENABLED=1 go test ./internal/skills/ -count=1
```
Expected: the grep shows the new pointer; `OK`; skills tests PASS (no skill was added, so `TestBuiltInSkillsAllLoad`'s count of 11 is unchanged).

- [ ] **Step 4: Commit.**

```bash
git add docs/ARCHITECTURE.md docs/tool-usage/future-using-tools-skill.md
git commit -m "docs: point ARCHITECTURE at docs/tools and outline the deferred using-tools skill"
```

---

## Task 13: Changelog and full verification

**Goal:** The change is recorded and the whole suite is green.

**Files:**
- Modify: `CHANGELOG.md` (add a bullet under `### Changed` in `## [Unreleased]`, which starts at line 41)

**Interfaces:**
- Consumes: everything from Tasks 1-12.
- Produces: nothing.

- [ ] **Step 1: Add the changelog entry.**

Under `## [Unreleased]` → `### Changed`, add:

```markdown
**Tool-usage prompting and tool architecture docs**

- Two rules in the cached system-prompt prefix now name the tool families by
  job: `repo.search` / `symbols` / `file.read` for finding and reading code,
  and the `git.*` / workspace tools for repository state and isolation. The
  prompt grew by the measured amount recorded in the commit; the ceilings
  (`maxSystemPromptBytes`, `maxToolSchemaBytes`) were not raised.
- The descriptions of `shell.run`, `repo.search`, `transcript_read`,
  `codebase_search`, `workspace.worktree`, `file.read` and `agent.run` now
  name the sibling tool each is most often confused with. `agent.run`'s
  `"agent"` argument is documented as a configured custom agent, not a role
  binding.
- Five built-in skills that named no tool (`systematic-debugging`,
  `test-driven-development`, `verification-before-completion`,
  `work-decomposition`, `marshal-executing-plans`) now name the tools that
  perform the steps they mandate.
- New `docs/tools/` documents the model-facing tool surface family by family —
  exact arguments, risk levels, call sequences, real failure modes, and Go
  anchors. `TestToolsDocumentationDrift` fails when the docs and the registry
  drift apart in either direction, or when a retired tool name reappears.
```

- [ ] **Step 2: Run the full verification.**

```bash
gofmt -l .            # expect no output
go vet ./...          # expect clean
CGO_ENABLED=1 go build ./cmd/marshal
CGO_ENABLED=1 go test ./...
```

Expected: all green. **Establish the baseline first** if you have not already: run `CGO_ENABLED=1 go test ./...` at `e423f699` in a clean `git worktree add --detach` and compare. Known environmental failures on this machine (not regressions) are recorded in `docs/context-diet/plans/2026-10-14-tier1-context-cuts-plan.md` under "Global Constraints": `internal/snapshot` Lookup/Rooted/Service tests, `internal/app` `TestRuntimeCloseRunsNoGitGC` and `TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces`, `internal/app/tui` `TestStatusLineUntrustedIsWarningColored`, `internal/tools/desktop/browser` `TestStandaloneBackendNavigateAndRead`, and `internal/worktree` `TestCLIGitOpsCommitAllWithoutARepoIdentity`. Confirm any other failure against the clean worktree before blaming the diff.

- [ ] **Step 3: Confirm the ceilings were not raised.**

Run:
```bash
grep -n 'maxSystemPromptBytes\|maxToolSchemaBytes' internal/app/prompt_budget_test.go
```
Expected: `maxSystemPromptBytes = 9000` and `maxToolSchemaBytes = 15000`, unchanged from `e423f699`.

- [ ] **Step 4: Commit.**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog for tool-usage prompting and tool architecture docs"
```

---

## Final Verification

```bash
gofmt -l .                                        # no output
go vet ./...                                      # clean
CGO_ENABLED=1 go build ./cmd/marshal              # builds
CGO_ENABLED=1 go test ./internal/agent/ ./internal/skills/ ./internal/tools/native/ ./internal/app/ -count=1
CGO_ENABLED=1 go test ./...                       # full suite
```

Success criteria:

- `TestDefaultPromptBudget` passes and logs a system prompt under 9000 bytes and tool schemas under 15000 bytes, with the ceilings unchanged.
- `TestToolsDocumentationDrift` passes with all seven docs present, every non-internal registered tool indexed, and no retired name in the docs.
- `TestNoToolDescriptionNamesARetiredTool` passes over both tool descriptions and skill bodies.
- `TestBuiltInSkillWorkflowContracts` and the per-skill pin tests pass unchanged.
- No existing test is weakened, skipped, or renamed to accommodate the change.

## Integration Notes

- **Nothing in this plan changes tool behaviour.** Registration order, risk levels, approval policy, schemas, and handlers are untouched. The only runtime-visible changes are prompt text and tool description strings.
- **The prompt prefix stays cache-stable.** Both new rules are constants composed into `baseRules` / `baseRulesSystem`, which sit at the head of the cached prefix and never change within a turn. `TestPromptPrefixStableAcrossSteps` and `TestCrossTurnStableRegionIsAppendOnly` must pass after every task.
- **`docs/tools/` is a new published directory.** It is not gitignored (unlike `docs/specs/`), and AGENTS.md requires published specs and docs to live under `docs/<feature>/` and be committed with the work they describe.
- **The plan file itself** lives at `docs/tool-usage/plans/2026-10-17-tool-usage-prompting-plan.md`, following the repo's tracked-plan convention (`docs/context-diet/plans/`, `docs/prompt-caching/plans/`, `docs/web-studio/plans/`).
- **If the drift guard's fixture ever stops registering a tool** because a config knob changed, the failure will be assertion 1 naming that tool. Fix the fixture, not the docs.
