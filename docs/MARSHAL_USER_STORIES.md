# Marshal — User Stories

**Version** 0.5  
**Date** March 2026  
**Companion** Marshal Design Specification v0.6  

> **Priority key**
>
> - **must have** — core to the value proposition, blocks Phase 1 ship
> - **should have** — important, delivers clear value, Phase 1 or 2
> - **could have** — useful enhancement, Phase 2 or 3


---


# Personas


## Dev — the interactive developer


A solo developer running Marshal from the terminal on their own codebase. They want to delegate bounded, well-defined coding tasks without babysitting a TUI. They care about git safety, fast feedback, and being able to pick up where they left off after a failed run. This is the primary persona.


## Ops — the CI/automation consumer


A developer integrating Marshal into a CI pipeline or automated workflow. They invoke Marshal headlessly and consume output programmatically. They care about predictable exit codes, structured output, and zero interactive prompts.


## Auditor — the retrospective reviewer


Often the same person as Dev, reviewing what the agent did after the fact — understanding why a session failed, or building intuition about how the models behave over time.


---


# Epic 1 — Core loop (Dev)

### US-001

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to submit a coding task from the terminal so that the executor and critic models run automatically and produce a reviewed result without my involvement.

**Acceptance criteria**

1. Running `marshal` followed by a task description starts a session and invokes the executor.
2. The critic is invoked automatically after each executor response without any prompt.
3. The loop retries automatically on a FAIL verdict, passing critic feedback to the executor.
4. The loop runs to completion (PASS or max_rounds exhaustion) without requiring user input.
5. The TUI updates in real time showing which agent is active and what round is in progress.


### US-002

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want successful tasks to be committed automatically so that I do not have to manually stage and commit agent-produced changes.

**Acceptance criteria**

1. On PASS, all changes are staged and committed with an agent-attributed commit message.
2. The commit message includes the task description, model names, and round count.
3. The commit SHA is displayed in the TUI and recorded in the session store.
4. `auto_commit = false` leaves changes staged but not committed.


### US-003

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want failed sessions to be automatically reverted so that my working tree is never left dirty after an exhausted loop.

**Acceptance criteria**

1. On exhaustion, a hard reset to the pre-session HEAD SHA is performed.
2. The isolation branch is deleted without merging.
3. The working tree is clean after revert completes.
4. The TUI clearly indicates revert occurred and shows the HEAD SHA restored.
5. `auto_revert = false` leaves changes in place with an explicit dirty-state warning.


### US-004

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want each session to run on an isolated branch so that my main branch is never modified until a PASS verdict is confirmed.

**Acceptance criteria**

1. A branch named `marshal/task-<id>` is created at session start.
2. All executor changes are applied to the isolation branch, not the original.
3. On PASS, the isolation branch is merged and deleted.
4. On FAIL/exhaustion, the isolation branch is deleted without merging.
5. `branch_isolation = false` disables this behaviour.


### US-005

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to see the critic's reasoning alongside the verdict so that I understand why a round passed or failed.

**Acceptance criteria**

1. The critic's full response is displayed in the TUI log.
2. FAIL verdicts include the specific issue and fix instruction from the JSON output.
3. PASS verdicts include the one-sentence explanation.
4. Critic output is visually distinct: purple prefix vs blue prefix.


### US-006

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to see the diff sent to the critic so that I know exactly what code changes were reviewed.

**Acceptance criteria**

1. The unified diff of all changes since session start is extracted after each executor response.
2. The diff is stored per-round in the session store and viewable in the session browser.
3. The diff used for critic review is identical to what `git diff HEAD` would show.



# Epic 2 — Input and task submission (Dev)

### US-007

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want a minimal launch screen when I open Marshal so that I can see endpoint health and recent tasks before typing anything.

**Acceptance criteria**

1. The launch screen shows executor and critic endpoint health (green = reachable, red = unreachable).
2. The last four sessions are listed with verdict and age.
3. Active configuration is shown (executor model, critic model, max rounds, branch isolation).
4. No session is started until the user types a task.
5. A red endpoint dot prevents submission and shows an actionable error.


### US-008

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to type a task directly from the launch screen so that I can start a session with a single motion.

**Acceptance criteria**

1. Any printable key activates the prompt bar with that character pre-filled.
2. Pressing Enter submits the task.
3. Pressing Escape from an empty prompt bar returns to the launch screen.
4. Arrow keys navigate recent task history.


### US-009

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer, I want to expand the prompt into a full composer for complex tasks so that I can write detailed descriptions and pin relevant files.

**Acceptance criteria**

1. Tab from the prompt bar expands to the composer. Text is preserved.
2. The composer provides a multi-line editing area.
3. Files can be pinned as context; each is sent in the executor system prompt.
4. Per-session overrides: max rounds, branch isolation, auto commit, dry run.
5. Estimated token count shown in statusbar.
6. Escape returns to prompt bar with text preserved.
7. `ctrl+↵` or the run button submits.


### US-010

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer, I want to rerun a recent task quickly so that I can retry a failed task or repeat a common operation.

**Acceptance criteria**

1. Last four tasks listed on launch screen and in prompt bar view.
2. Arrow keys fill the input with selected history item.
3. Enter on a history item submits it as a new session.
4. Rerunning creates a new session — it does not modify the original record.



# Epic 3 — Observability (Dev + Auditor)

### US-011

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want live streaming output from the executor and critic so that I can follow along without waiting for a round to complete.

**Acceptance criteria**

1. Output streams token-by-token into the log panel.
2. A blinking cursor shows the current streaming position.
3. The active agent badge updates when the agent changes.
4. Streaming is not batched or delayed.


### US-012

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want a persistent info panel showing session metrics so that I can monitor token usage and progress without reading the log.

**Acceptance criteria**

1. Info panel shows: status, round dots, executor model, critic model, token count, budget progress bar, branch name, elapsed time.
2. Token count updates after each model call.
3. Progress bar fills proportionally against the context limit.
4. Transitions to post-run summary on completion.


### US-013

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer, I want a warning when approaching the token context limit so that I can take action before a request is rejected.

**Acceptance criteria**

1. At `token_budget_warn` (default 80%), a visible warning appears in the statusbar.
2. At 95%, the next call is blocked and the user is prompted to compact or reduce scope.
3. The warning is non-modal — does not interrupt a round in progress.
4. The threshold is configurable.


### US-014

**Persona** Dev  
**Priority** could have  
**Phase** 2

As a developer, I want to see DeepSeek-R1's reasoning chain so that I can understand the logic behind a critic verdict.

**Acceptance criteria**

1. Phase 1: think-blocks stripped silently.
2. Phase 2: think-blocks rendered in italic purple above the verdict, separated by a thin line.
3. The panel toggles with T without interrupting the session.
4. Think-block content stored per-round in the session store.


### US-015

**Persona** Auditor  
**Priority** must have  
**Phase** 2

As a developer reviewing past work, I want to browse all past sessions so that I can find and inspect any previous run.

**Acceptance criteria**

1. Session browser lists all sessions with: ID, task, verdict badge, round count, tokens, age.
2. Sessions sorted newest-first.
3. Filterable by verdict (all / pass / fail).
4. List navigation is instant — no round-trips per keystroke.


### US-016

**Persona** Auditor  
**Priority** must have  
**Phase** 2

As a developer reviewing past work, I want to expand a session and see each round in detail.

**Acceptance criteria**

1. Enter on a row expands it inline — no navigation away.
2. Each round shows: executor block, critic block with left-border colour coding, token count, cached token count.
3. All rounds shown in order.
4. Original session row remains visible as the expansion header.


### US-017

**Persona** Auditor  
**Priority** should have  
**Phase** 2

As a developer reviewing past work, I want to view the full diff for any session or round.

**Acceptance criteria**

1. D from session browser opens the diff viewer.
2. Additions in green, deletions in red.
3. The diff is the actual git diff recorded at session time.
4. Q or Escape exits, returning to the session browser at the same scroll position.


### US-018

**Persona** Auditor  
**Priority** could have  
**Phase** 2

As a developer reviewing past work, I want to see cache savings per session.

**Acceptance criteria**

1. Each session record stores `cached_tokens` and `total_tokens`.
2. Session browser shows cached tokens as a percentage.
3. Post-run info panel shows "N% cached".
4. Cache savings stored per-round.



# Epic 4 — Configuration and backends (Dev)

### US-019

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to configure Marshal via a project-level TOML file so that my configuration is version-controlled alongside my code.

**Acceptance criteria**

1. `marshal.toml` in the project root is loaded on startup.
2. All options have sensible defaults — a five-line `marshal.toml` is sufficient to start.
3. `${VAR_NAME}` syntax for environment variable interpolation.
4. Missing or malformed `marshal.toml` produces a clear error.
5. `marshal.toml` is committed. `.marshal/` is git-ignored.


### US-020

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer, I want to view the loaded configuration from within the TUI so that I can verify which models and settings are active.

**Acceptance criteria**

1. Config view shows all sections and their loaded values.
2. Resolved env vars shown as "$VAR ✓".
3. Model names colour-coded: executor in blue, critic in purple.
4. Statusbar confirms endpoint reachability.
5. Config view is read-only.


### US-021

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to point Marshal at any OpenAI-compatible endpoint so that I can use RunPod, Ollama, or any other provider.

**Acceptance criteria**

1. Switching providers requires only a `marshal.toml` change.
2. `OpenAICompatibleBackend` works with any `/v1/chat/completions` endpoint.
3. Executor and critic can use different endpoints simultaneously.
4. Connectivity check runs on startup and is displayed on the launch screen.


### US-022

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer, I want independent temperature settings for the executor and critic.

**Acceptance criteria**

1. `temperature` configurable per-agent in `marshal.toml`.
2. Critic defaults to 0.0. Executor defaults to 0.2.
3. Temperature stored per-round in the session store.



# Epic 5 — CI and automation (Ops)

### US-023

**Persona** Ops  
**Priority** must have  
**Phase** 1

As a CI pipeline, I want to invoke Marshal headlessly and receive structured output so that I can parse the result and take automated action.

**Acceptance criteria**

1. `marshal run --no-tui "task"` runs the loop with no interactive UI.
2. Progress emitted as newline-delimited JSON.
3. Final `session_complete` event includes: verdict, rounds, tokens_total, cached_tokens, commit_sha, duration_ms.
4. No interactive prompts at any point.


### US-024

**Persona** Ops  
**Priority** must have  
**Phase** 1

As a CI pipeline, I want documented exit codes from Marshal.

**Acceptance criteria**

1. Exit 0: PASS. Exit 1: FAIL/exhausted. Exit 2: config error. Exit 3: connectivity error. Exit 4: git error.
2. Exit codes are stable — will not change between minor versions.


### US-025

**Persona** Ops  
**Priority** must have  
**Phase** 1

As a CI pipeline, I want transient API failures to be retried automatically.

**Acceptance criteria**

1. HTTP 429, 502, 503 retried with exponential backoff.
2. Default: 3 attempts, 1s initial, factor 2.
3. Configurable via `[retry]` in `marshal.toml`.
4. HTTP 400, 401, 404 are terminal — not retried.


### US-026

**Persona** Ops  
**Priority** should have  
**Phase** 1

As a CI pipeline, I want a dry-run mode so that I can test the loop without making commits.

**Acceptance criteria**

1. `marshal run --dry-run "task"` runs the full loop but skips all git write operations.
2. Diff printed to stdout but not committed.
3. No branch created or deleted.
4. Session recorded with a `dry_run` flag.


### US-027

**Persona** Ops  
**Priority** could have  
**Phase** 2

As a CI pipeline, I want to list past sessions programmatically.

**Acceptance criteria**

1. `marshal sessions list --json` outputs all sessions as a JSON array.
2. Filters: `--verdict`, `--since <date>`, `--limit N`.
3. Output schema is stable between minor versions.



# Epic 6 — Context management (Dev)

### US-028

**Persona** Dev  
**Priority** should have  
**Phase** 2

As a developer, I want earlier rounds summarised automatically so that token usage stays bounded.

**Acceptance criteria**

1. Phase 1: `PassThroughCompactor` (no-op hook wired).
2. Phase 2: `SummaryCompactor` summarises rounds 1 to N-1 when `round > compact_after`.
3. Most recent round always passed in full.
4. Compaction transparent to the loop engine.
5. Session store records whether compaction was applied.


### US-029

**Persona** Dev  
**Priority** could have  
**Phase** 1

As a developer, I want prompt caching tracked and surfaced.

**Acceptance criteria**

1. `CacheHit` and `CachedTokens` stored per model call.
2. Cached count shown in topbar alongside total.
3. Post-run info panel shows total cached and percentage.
4. Cache tracked per-round.



# Epic 7 — Git safety (Dev)

### US-030

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want Marshal to refuse to start if my working tree is dirty.

**Acceptance criteria**

1. On session start, Marshal checks for uncommitted changes.
2. Dirty working tree → exit 4 with a clear message.
3. Check happens before any branch is created or any model called.


### US-031

**Persona** Dev  
**Priority** should have  
**Phase** 2

As a developer, I want to identify and clean up orphaned marshal branches.

**Acceptance criteria**

1. `marshal sessions list` flags sessions that ended without clean branch deletion.
2. `marshal sessions clean` deletes branches matching `marshal/task-*` not referenced by an active session.
3. Lists branches before deletion and asks for confirmation.


### US-032

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want the original branch restored correctly if Marshal crashes.

**Acceptance criteria**

1. On unhandled error, Marshal attempts to checkout the original branch before exiting.
2. If checkout succeeds, the isolation branch is listed for manual cleanup.
3. Session recorded as "error" (distinct from "fail").



# Epic 8 — Library use (Dev + Ops)

### US-033

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer building tooling, I want to import the Marshal loop engine as a Go library.

**Acceptance criteria**

1. `internal/loop` importable with no side effects.
2. No global state initialised on import.
3. Loop engine does not reference TUI or CLI packages.
4. Minimal example in repo documentation.


### US-034

**Persona** Ops  
**Priority** could have  
**Phase** 2

As a developer building automation, I want the loop engine to accept a `context.Context`.

**Acceptance criteria**

1. All `Backend.Complete()` calls accept a `context.Context`.
2. Cancelling the context abandons the current model call.
3. Session recorded as "cancelled".
4. Git state cleaned up on cancellation.



# Epic 9 — Multi-agent pipeline (Dev + Ops)

### US-035

**Persona** Dev  
**Priority** must have  
**Phase** 3

As a developer, I want to describe a large feature and have Marshal decompose it into a dependency-ordered task plan.

**Acceptance criteria**

1. `marshal pipeline "feature"` invokes the planner and displays the task graph.
2. Each task shows: ID, description, `depends_on`, `files_likely_affected`.
3. Graph validated before display — cycles and missing refs produce clear errors.
4. User confirms before execution (`--auto-approve` skips).
5. Plan can be edited interactively before confirmation.


### US-036

**Persona** Dev  
**Priority** must have  
**Phase** 3

As a developer, I want pipeline tasks to execute in dependency order.

**Acceptance criteria**

1. Scheduler performs topological sort before execution.
2. A task starts only when all `depends_on` tasks have PASS verdicts.
3. A failed task blocks all dependents.
4. `fail_fast=false`: tasks with no dependency on the failed task continue.
5. Pipeline session record stores the full task graph and per-task outcomes.


### US-037

**Persona** Dev  
**Priority** should have  
**Phase** 3

As a developer, I want independent tasks to run in parallel.

**Acceptance criteria**

1. Tasks with no shared `depends_on` and no overlapping `files_likely_affected` run concurrently up to `max_parallel`.
2. `max_parallel` configurable in `[pipeline]`. Default 1 (sequential).
3. Each parallel task runs in its own goroutine with its own isolation branch.
4. File conflict detection serialises tasks whose `files_likely_affected` overlap.
5. DAG progress view shows all running tasks simultaneously.
6. Parallel execution produces identical results to sequential — performance optimisation only.


### US-038

**Persona** Dev  
**Priority** must have  
**Phase** 3

As a developer, I want an integration critic to review all task changes as a combined whole.

**Acceptance criteria**

1. After all tasks complete, integration critic receives the combined diff.
2. `cross_task_issues` identifies which tasks are implicated in each issue.
3. Integration PASS → merge all task branches in topological order → single pipeline commit.
4. Integration FAIL → task branches held open, implicated tasks identified for rerun.
5. Integration critic uses `temperature=0` focused on cross-task coherence.


### US-039

**Persona** Ops  
**Priority** must have  
**Phase** 3

As a CI pipeline, I want to invoke a Marshal pipeline headlessly and receive structured output.

**Acceptance criteria**

1. `marshal pipeline --no-tui "feature"` runs with no interactive UI.
2. Progress as newline-delimited JSON: `pipeline_start`, `task_start`, `task_complete`, `integration_start`, `integration_complete`, `pipeline_complete`.
3. Exit codes: 0=PASS, 1=FAIL, 2=config error, 3=connectivity error, 4=git error.
4. `--auto-approve` skips plan confirmation.



# Epic 10 — Security-first prompts (Dev + Ops)

### US-040

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want every executor call to include security standing instructions so that the code Marshal writes is secure by default.

**Acceptance criteria**

1. Executor system prompt includes a non-configurable standing instruction block.
2. Covers: input validation, parameterised queries, secrets handling, auth middleware, least privilege, cryptography.
3. No configuration option to disable the standing instructions.
4. Visible via `marshal config --show-prompts` for transparency.


### US-041

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want every critic call to include security standing checks so that security regressions are caught on every round.

**Acceptance criteria**

1. Critic system prompt includes a non-configurable security check block.
2. Covers: unvalidated input, raw query construction, auth bypass, hardcoded secrets, sensitive logging, new dependencies.
3. Security FAIL carries the same weight as a functional FAIL.
4. Security checks run on every round — cannot be toggled off.



# Epic 11 — Skills system (Dev)

### US-042

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want to invoke a built-in skill so that the executor and critic are automatically specialised for the task type.

**Acceptance criteria**

1. `marshal run --skill <n> "task"` applies the skill's `system_prompt_additions` to both agents.
2. Built-in skills in Phase 1: schema-migration, security-audit, test-generation, documentation, dependency-audit.
3. Skill additions layer on top of base prompts and security instructions — not instead of them.
4. Unknown skill name → exit 2 with a list of available skills.


### US-043

**Persona** Dev  
**Priority** should have  
**Phase** 1

As a developer, I want to inspect a skill's prompt additions before running.

**Acceptance criteria**

1. `marshal skills list` shows all available skills with name and description.
2. `marshal skills show <n>` prints the full `system_prompt_additions` for executor and critic.
3. Output indicates additions are layered — not the complete prompt.


### US-044

**Persona** Dev  
**Priority** should have  
**Phase** 2

As a developer, I want to define my own custom skills in `.marshal/skills/`.

**Acceptance criteria**

1. TOML files in `.marshal/skills/` loaded as custom skills on startup.
2. Same schema as built-in skills.
3. Appear in `marshal skills list` marked "(custom)".
4. Custom skill with same name as built-in overrides it for that project.
5. Malformed skill files produce a clear validation error on startup.


### US-045

**Persona** Dev  
**Priority** should have  
**Phase** 2

As a developer, I want a skill to auto-inject relevant context files.

**Acceptance criteria**

1. Skills declare an `auto_inject` list in `[context]`.
2. Listed files appended to executor context for every round.
3. Shown in composer and in statusbar token count.
4. Missing files produce a warning, not an error — session proceeds.


### US-046

**Persona** Dev  
**Priority** could have  
**Phase** 3

As a developer running a pipeline, I want to assign a skill to specific tasks.

**Acceptance criteria**

1. Planner output supports an optional `skill` field per task.
2. Per-task skill overrides the pipeline-level `--skill` flag.
3. Pipeline-level `--skill` applies to tasks without their own skill.
4. Skill routing visible in DAG progress view per task node.



# Epic 12 — Open skills system (Dev + Community)

### US-047

**Persona** Dev  
**Priority** must have  
**Phase** 1

As a developer, I want skills to resolve from project, user-global, and built-in directories in priority order.

**Acceptance criteria**

1. `marshal skills which <n>` prints the full path and source tier of the file that would be loaded.
2. A project skill takes priority over a same-named user-global skill, which takes priority over built-in.
3. `marshal skills list` indicates source tier: (project), (user), or (built-in).
4. Skills from all three tiers appear without additional configuration.
5. `~/.config/marshal/skills/` created on first use if absent.


### US-048

**Persona** Dev  
**Priority** should have  
**Phase** 2

As a developer writing a skill, I want to validate and scaffold skill files from the CLI.

**Acceptance criteria**

1. `marshal skills validate <path>` exits 0 if valid, 1 with each error listed.
2. Validation checks: required fields present, `system_prompt_additions` non-empty for at least one agent, no `system_prompt` or `system_prompt_override` keys, valid semver `marshal_min` if provided.
3. `marshal skills new` prompts for name, description, tags, agents, then writes a skeleton to `.marshal/skills/<n>.toml`.
4. Skeleton includes commented examples of every supported field.
5. `marshal skills new --global` writes to `~/.config/marshal/skills/`.



---


# Story Map


| ID | Story (short) | Priority | Phase |
| --- | --- | --- | --- |
| US-001 | Submit task, executor and critic run automatically | must have | 1 |
| US-002 | Successful tasks auto-committed | must have | 1 |
| US-003 | Failed sessions auto-reverted | must have | 1 |
| US-004 | Sessions run on isolated branch | must have | 1 |
| US-005 | Critic reasoning displayed in TUI | must have | 1 |
| US-006 | Actual diff sent to critic and visible | must have | 1 |
| US-007 | Launch screen with endpoint health | must have | 1 |
| US-008 | Type task directly from launch screen | must have | 1 |
| US-009 | Tab to expand prompt into composer | should have | 1 |
| US-010 | Rerun a recent task quickly | should have | 1 |
| US-011 | Live streaming output in log panel | must have | 1 |
| US-012 | Persistent info panel with session metrics | must have | 1 |
| US-013 | Token budget warning at configurable threshold | should have | 1 |
| US-014 | R1 think-block panel | could have | 2 |
| US-015 | Session browser with filter | must have | 2 |
| US-016 | Expand session to see per-round detail | must have | 2 |
| US-017 | Diff viewer for any session or round | should have | 2 |
| US-018 | Cache savings shown per session | could have | 2 |
| US-019 | Configure Marshal via marshal.toml | must have | 1 |
| US-020 | View loaded config from TUI | should have | 1 |
| US-021 | Point Marshal at any OpenAI-compatible endpoint | must have | 1 |
| US-022 | Independent temperature per agent | should have | 1 |
| US-023 | Headless mode with structured JSON output | must have | 1 |
| US-024 | Documented exit codes | must have | 1 |
| US-025 | Transient API failure retry | must have | 1 |
| US-026 | Dry-run mode | should have | 1 |
| US-027 | List sessions as JSON | could have | 2 |
| US-028 | Automatic context compaction | should have | 2 |
| US-029 | Prompt cache tracking | could have | 1 |
| US-030 | Refuse to start on dirty working tree | must have | 1 |
| US-031 | Identify and clean orphaned marshal branches | should have | 2 |
| US-032 | Restore original branch on crash | must have | 1 |
| US-033 | Import loop engine as Go library | should have | 1 |
| US-034 | context.Context support for cancellation | could have | 2 |
| US-035 | Planner decomposes feature into dependency task graph | must have | 3 |
| US-036 | Pipeline executes tasks in dependency order | must have | 3 |
| US-037 | Independent tasks run in parallel | should have | 3 |
| US-038 | Integration critic reviews combined cross-task diff | must have | 3 |
| US-039 | Headless pipeline mode with structured JSON output | must have | 3 |
| US-040 | Security standing instructions in every executor call | must have | 1 |
| US-041 | Security standing checks in every critic call | must have | 1 |
| US-042 | Invoke a built-in skill for a session | must have | 1 |
| US-043 | View skill prompt additions before running | should have | 1 |
| US-044 | Define a custom skill in .marshal/skills/ | should have | 2 |
| US-045 | Skill auto-injects context files | should have | 2 |
| US-046 | Skill routing per-task in a pipeline | could have | 3 |
| US-047 | Three-tier skill resolution with marshal skills which | must have | 1 |
| US-048 | marshal skills validate and new scaffold command | should have | 2 |
