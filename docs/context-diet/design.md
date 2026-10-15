# Context diet: Tier 1

Status: approved 2026-10-14. Plan: `plans/2026-10-14-tier1-context-cuts-plan.md`.

## Problem

Every model request carries a fixed overhead that Marshal adds before the
conversation starts. Measured on 2026-10-14 against a default runtime in
native-tools mode, plus this repository's real database:

| Part | Size | Tokens (≈ bytes/4) |
|---|---|---|
| Tool schemas (53 tools) | 31.2 KB | 7.8k |
| System prompt | 15.7 KB | 3.9k |
| Context pack: auto-extracted memories | ~16 KB | ~4k |
| Context pack: repo card + directory map + recent sessions | ~9.5 KB | ~2.4k |
| Autoloaded `using-skills` body | 1.8 KB | 0.5k |

That's about 18.5k tokens before the repo's own AGENTS.md. Real requests
since 2026-08-01 averaged about 88k prompt tokens, and only 4.5% of prompt
tokens were cache reads, so this prefix is paid almost in full on every request.

Pi-style agents run with a ~1–2k token prefix. Most of Marshal's extra prefix
is either low-signal or actively misleading:

- **Memories.** A background LLM pass ran every 5 turns, at compaction and at
  exit. It stored 404 memories in this repo, all `tentative`, and many of them
  short-lived ("PR #54 is open and mergeable", "Task 23 Step 4 of the
  transcript-beats plan…"). One postmortem records the agent acting on a stale
  memory that contradicted current behaviour.
- **Directory map.** It's alphabetical and capped at 200 files, so it shows
  `.github/`, `cmd/` and `docs/`, lists test function names, and truncates
  with "… 1757 more files". Most of `internal/` never appears. The "Recent
  sessions" seed included a greeting-only knowledge-agent session.
- **Skill hints.** The ranker's own comment records that "a skill applies" and
  "no skill applies" can't be separated (worst true positive 0.486 < best true
  negative 0.586). The hint message repeats a roster that is already in the
  system prompt.
- **Dogfood rules.** The shipped base rules tell every user to write specs to
  `.docs-archive/superpowers/{specs,plans}/`, and `file.write` /
  `file.write_patch` append a note saying the same thing. This contradicts the
  built-in skills (`docs/specs/`, `docs/plans/`) and this repo's AGENTS.md
  (`docs/<feature>/`). At least 8 postmortems record wrong guesses of
  `.docs-archive/…-plan.md`, or reading the gitignored archive from inside a
  worktree where it doesn't exist.
- **Deferred `config.*` tools.** Thirty tools cost ~4.9 KB of announcement
  text on every request. They were called 95 times in 52,549 tool calls, and
  61 of those were `config.read`. `/settings`, `/models` and `/connect`
  cover the writes.

## Decisions

1. **Memory: curated-only injection.** Delete the knowledge package (auto
   extraction, the 5-turn scheduler, the exit pass) and the `memory.write`
   tool. Keep injecting memories, but only ones a person curated:
   `confidence = confirmed`, or `scope` is `workspace` or `global`. Stale
   memories are never injected. Today that's zero memories in this repo, so
   zero tokens, and the web fleet's memory library (promote/confirm) keeps
   working. The DB table, `/memory` panel and web library are unchanged.
2. **Sessions are always marked ended at TUI exit** (`ended_at` set, empty
   summary). Previously only a successful knowledge pass ended a session, so
   sessions without a provider were never marked ended.
3. **Drop the repo card, directory map and recent-sessions seeds** from the
   context pack, including the re-seed on index completion. `repo.map` /
   `repo.card` remain as tools (Tier 2 decides their fate).
4. **Delete skill hints entirely:** the embedding ranker, the class defaults
   and the per-turn hint message. The roster in the system prompt and the
   autoloaded `using-skills` remain.
5. **Remove repo-specific rules from the shipped prompt:**
   - `ruleDocsArchive` and the `specOrPlanDocNudge` write note: deleted.
   - `ruleReviewerSubagent`: deleted from the product and added as one line to
     this repo's AGENTS.md.
   - `ruleWorkspaceFinish`: moved into the `workspace.finish` tool description,
     where it only costs tokens when worktree tools are present.
   - The built-in skills keep `docs/specs/` and `docs/plans/` as defaults but
     defer to a location named in the repo instructions.
6. **Delete the 29 `config.*` setter tools; keep `config.read`** (deferred).
   It's the masked-secrets way to inspect config and accounts for most use.
   The forced-approval special case for `config.write` goes with the setters.
7. **Add a prompt-budget guard test** that builds the default runtime and
   fails when the native-mode system prompt or tool schemas exceed a ceiling,
   or when the removed text reappears. Tier 2 will lower the ceilings.

## Expected result

- System prompt: about 15.7 KB → 10.5 KB.
- Tool schemas: about 31.2 KB → 31.0 KB (Tier 1 removes only `memory.write`;
  Tier 2 is the schema diet).
- Context pack: about 25.7 KB → 0 KB at turn start for this repo.
- Per-request prefix: about 18.5k → about 10.5k tokens.

## Out of scope (later tiers)

Tool consolidation (git.\*, test.run, job/watch/scratchpad families, file.page,
symbols.find, ask_user, parent.\* conditional registration), the patch format,
envelope mode, snapshots, swarm, the `knowledge` routing role, the memory UI,
and the worktree `.docs-archive` seeding.

The postmortems also point at the empty `HOME` in `shell.run` and
`workspace.finish` refusing on a dirty `.marshal/config.toml` as top friction
sources. Both are worth fixing, but they aren't context cuts.
