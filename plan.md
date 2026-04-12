# Plan: Marshal — a Go rewrite of Aider with multi-model orchestration

## Context

You want a Go CLI/TUI that matches Aider's feature surface (repo map, edit formats, git-native workflow, slash commands, watch mode, linter loop, multi-provider model support) **and** embeds the Marshal multi-model system described in `MARSHAL_MODEL_INTERACTIONS.md`: four specialised models (Marshal / Executor / Critic / Compactor), branch-isolated single-task loops with PASS/FAIL verdicts, a DAG-scheduled multi-task pipeline with an integration critic, layered prompts (base + security + skill), and prompt-prefix caching.

The goals shape each other:
- **Aider's architecture assumes one "main" model plus a weak model and optional editor model.** The Marshal design replaces that with a fixed four-role roster. Those aren't compatible shapes — so the Go rewrite should adopt Marshal's roster as the foundation and layer Aider's features on top, rather than porting Aider's `Coder` hierarchy verbatim and bolting the critic onto the side.
- **Aider's edit formats (editblock / udiff / whole / patch) exist because models can't edit files directly.** Marshal's Phase 3 gives the executor real tools (`read_file`, `write_file`, `run_command`). So edit formats become a Phase-1 compatibility layer for models that don't support tool use, and the long-term path is tool-use + branch-observed diff review.
- **Aider optimises for a single fluid chat loop.** Marshal optimises for discrete, reversible tasks. The TUI needs to support both: a conversational prompt bar that feels like Aider, but every submission is internally a bounded task run.

The outcome is a new tool (working name: `marshal`) that a current Aider user should feel at home in, but that produces more reliable edits because every change is critic-reviewed on an isolation branch before merge.

## Interaction model (load-bearing)

**The UX must feel like a fluid conversation** — the Aider / Claude Code loop where you type, the model streams, you type again, the model streams again. It must **not** feel modal ("submit task → wait → verdict → next task"). But the underlying execution model from `MARSHAL_MODEL_INTERACTIONS.md` is still discrete reversible tasks. Both statements have to be true.

The resolution:

- **Every user turn is implicitly a task**, assigned an auto-generated id. No "confirm task" dialog, no clarification gate by default. Marshal-model clarification is reserved for genuinely ambiguous asks and surfaces as an inline follow-up message in the chat stream, not a modal.
- **Two layers of branch isolation, both invisible until the user asks.**
  - **Target branch** — whatever branch the user had checked out when they launched marshal (e.g. `main`, `feature/staff-portal`). Marshal never writes to this branch automatically. It is only touched when the user types `/ship`.
  - **Session staging branch** — `marshal/session-<timestamp>` created from the target branch HEAD when the session starts. All task work accumulates here. This is the branch the user's TUI is "chatting against".
  - **Task branch** — `marshal/task-<id>` branched off the session staging branch for each user turn. On PASS, squash-merged into the session staging branch and deleted. On fail-after-retries, silently deleted — the session staging branch is untouched and the target branch stays pristine.
- **`/ship` is the one explicit commit ritual.** It squash-merges the session staging branch into the target branch, prints the resulting commit SHA, and starts a fresh session staging branch from the new target HEAD so the user can keep working. `/ship --message "..."` sets the commit message; otherwise the compactor model generates one from the shipped tasks' prompts. `/ship --dry-run` shows the combined diff without merging.
- **Executor streaming is the chat response.** Tokens stream into the chat pane exactly like Aider. The critic verdict arrives as a subtle status-line badge (✓ PASS / ✗ retry / ⚠ reverted), not a blocking gate. Think-blocks remain collapsible.
- **Tasks are tracked in a background ledger.** Each task records: id, user prompt, rounds, diff summary, resulting commit SHA on the session staging branch, final verdict, timestamp, session id, shipped-at (null until the parent session is shipped). The ledger is a SQLite table queried by the TUI and the `/history`, `/undo`, `/revert <id>` commands.
- **Reversal is per-task and never touches the target branch.** `/undo` reverts the most recent task on the session staging branch via `git revert -n <sha>`. `/history` lists recent tasks with ids. `/revert <id>` reverts a specific past task even if it isn't the most recent. Because the staging branch absorbs all revert work, the target branch is completely unaffected by experimentation — the user only sees the cumulative cleaned-up result when they `/ship`.
- **Reversal may conflict with later tasks that touched overlapping files.** The revert path attempts `git revert -n <sha>`; on conflict, marshal reports the conflicting later task(s) and offers: (a) also revert the dependent tasks, (b) cancel. No automatic rewriting of dependent tasks — that path lies.
- **Safety guarantees.** Marshal never force-pushes, never rewrites history on `main` or any non-`marshal/*` branch, and `/ship` is the only command that writes to the target branch. Every `/ship` produces a normal forward commit the user can `git reset` manually. Session staging branches are cleaned up on `/quit` unless `--keep-session-branch` is passed.

This interaction model is the product. The multi-model loop is the mechanism that makes it reliable. If a milestone change threatens the fluid feel, the fluid feel wins.

## Architectural decisions

Lock these in before M0 — changing them later is expensive.

| Concern | Decision | Why |
|---|---|---|
| Language | Go 1.22+ | User requirement; good TUI/concurrency/binary-dist story |
| TUI | `charmbracelet/bubbletea` + `lipgloss` + `bubbles` | De facto standard; supports streaming + collapsible sections |
| Model backends | Native HTTP client targeting **OpenAI-compatible `/v1/chat/completions`** only; no LiteLLM equivalent | Marshal's config already assumes this; covers Fireworks, OpenAI, OpenRouter, Ollama, Together, Groq, DeepSeek, Anthropic-via-proxy. Avoids a giant abstraction layer. |
| Anthropic / Google native APIs | Separate `Backend` implementations behind the same interface | ~5% of code for ~30% of providers |
| Git | **Shell out to `git`** via `os/exec`, not `go-git` | Matches behaviour exactly (diffs, hooks, submodules, partial clones); `go-git` has gaps |
| Tree-sitter | `smacker/go-tree-sitter` with language packs; reuse `.scm` query files copied verbatim from `aider/queries/` | Query files are the real asset; grammars are commodity |
| Token counting | `pkoukk/tiktoken-go` for OpenAI families; heuristic char-based fallback for Anthropic/others | Aider uses litellm's tokenizer which we don't have |
| Config | TOML via `BurntSushi/toml` (matches `marshal.toml` example in the spec) plus CLI flag overlay via `spf13/cobra` + `spf13/viper` | One config format, env-var overrides |
| Session store | SQLite via `modernc.org/sqlite` (pure-Go, no cgo) | Needed for think-blocks, token costs, verdict history; SQLite keeps binary single-file |
| Sandbox for tool-use executor | `os/exec` with explicit allowlist + chroot-style path prefix check — **not** containers in v1 | Containers are a v2 hardening story |
| Concurrency model | One goroutine per pipeline task; `errgroup` for tier-level sync; file-overlap serialisation via per-path `sync.Mutex` map | Matches the marshal spec's "tasks with overlapping files_likely_affected are serialised" rule |
| Packaging | `goreleaser` → static binaries for linux/macOS/windows on amd64+arm64 | Standard Go path |

## Milestones

Each milestone is sized to ship standalone — you should be able to use `marshal` after every milestone, it just does less. Work top-to-bottom; only M5/M7/M10 can run in parallel with their neighbours.

---

### M0 — Skeleton & config (1 week)

**Deliverable:** `marshal --version` works, loads `marshal.toml`, prints the resolved four-model config.

- `cmd/marshal/main.go` — cobra root with `run`, `pipeline`, `chat`, `config`, `version` subcommands (all stubs except `version`/`config`)
- `internal/config` — TOML loader matching §7.4/§7.5 of the marshal doc, env-var expansion (`${FIREWORKS_API_KEY}`), precedence: flag > env > `./marshal.toml` > `~/.config/marshal/config.toml`
- `internal/logging` — structured slog with `--verbose` and file-sink support
- CI scaffold: `go vet`, `go test ./...`, `golangci-lint`, goreleaser snapshot
- `CONTRIBUTING.md` and `README.md` stubs

**Verify:** `go test ./...` green; `marshal config show` prints the parsed config with secrets redacted; `marshal config show --profile dev` picks the local/Ollama block.

---

### M1 — Backend interface + OpenAI-compatible client (1 week)

**Deliverable:** Can send a chat-completion request to Fireworks/OpenAI/Ollama and stream the response to stdout.

- `internal/backend/backend.go`:
  ```go
  type Backend interface {
      Complete(ctx, req Request) (Response, error)
      Stream(ctx, req Request) (<-chan Chunk, error)
      TokenCount(messages []Message) (int, error)
      SupportsTools() bool
      SupportsJSONMode() bool
  }
  ```
- `internal/backend/openai_compat.go` — SSE streaming parser, retry with exponential backoff (port Aider's `RETRY_TIMEOUT=60s` behaviour from `aider/sendchat.py`), rate-limit handling
- `internal/backend/registry.go` — map `config.Role → Backend` so marshal/executor/critic/compactor can each point at a different provider
- `internal/tokens` — `tiktoken-go` wrapper with per-model encoder cache; char-heuristic fallback; image-token tile math copying `aider/models.py::token_count_for_image`
- Manual test CLI: `marshal debug chat --role executor "hello"` streams a reply

**Verify:** `marshal debug chat --role executor "write a haiku"` streams tokens against Ollama locally and against Fireworks with a real key. Unit tests mock the SSE stream.

---

### M2 — Git layer (3–4 days)

**Deliverable:** Programmatic branch create / checkout / diff / commit / revert.

- `internal/git/repo.go` wraps `os/exec` for:
  - `HeadSHA()`, `CurrentBranch()`, `IsDirty()`, `DirtyFiles()`, `TrackedFiles()`
  - `CreateBranch(name, from SHA)`, `Checkout(name)`, `MergeSquash(branch, message)`, `DeleteBranch(name)`, `ResetHard(sha)`, `RevertNoCommit(sha)`
  - `Diff(opts)` returning unified diff string (default `-U1` to match the marshal spec)
  - `.gitignore` and `.marshalignore` respect via `gobwas/glob`
- Attribution config: co-authored-by trailer (`Co-authored-by: marshal (<model>) <marshal@local>`) matching Aider's behaviour
- `internal/git/session.go` — **Session abstraction** reflecting the target/staging/task hierarchy:
  - `Session{targetBranch, stagingBranch, targetStartSHA}` — created at TUI startup; `Start()` captures the current branch as `targetBranch`, creates `marshal/session-<timestamp>` from its HEAD, and leaves the user checked out on the staging branch
  - `TaskTx{id, parentStagingSHA}` — per-task helper: creates `marshal/task-<id>` from the staging HEAD, exposes `Commit(msg)`, `Merge() → squash into staging`, `Abandon() → delete without merging`
  - `Ship(message)` — squash-merges the staging branch into the target branch, returns the new target SHA, then starts a fresh staging branch from the new target HEAD (session continues seamlessly)
  - `Teardown()` — on `/quit`, deletes the staging branch unless `--keep-session-branch` was passed; the target branch is always left as marshal found it (unless `/ship` was called)

**Verify:** Unit tests against a temp-dir repo. Manual: `marshal debug git-session --task test` starts on `main`, creates a session staging branch, runs a task branch with a dummy commit, merges it to staging, runs `Ship`, asserts `main` advances by exactly one squash commit and a new staging branch is started from the new `main` HEAD.

---

### M3 — Single-task loop (Phase 1 of marshal) (2 weeks)

**This is the core of the product.** Everything later is either additive or a TUI skin.

**Deliverable:** `marshal run "add a /healthz endpoint"` executes one full loop against a real repo, branch-isolated, with critic verdict, and merges or reverts.

- `internal/loop/engine.go` — orchestrator. **No confirmation gate.** A user turn becomes a task immediately:
  1. Generate task id; `git.TaskTx` creates `marshal/task-<id>` from the current **session staging branch** HEAD (recorded as `parent_staging_sha`)
  2. Ledger insert: `tasks(id, session_id, prompt, parent_staging_sha, started_at, status='running')`
  3. Round loop (default `max_rounds=3`):
     - Assemble executor prompt (M8 handles layering; M3 uses flat prompts)
     - Stream executor response to the active `internal/ui/sink` — in M3 this is stdout, M4 makes it the chat pane
     - Apply executor output (M3 writes a stub that assumes whole-file replies; M6 adds real edit formats)
     - `git diff -U1` against the staging HEAD
     - Call critic with task + executor prose + diff
     - Strip `<think>...</think>` blocks (reuse regex logic from `aider/reasoning_tags.py`); think-blocks are stored on the round row for later display, not in the critic-verdict parse path
     - Parse JSON verdict with tolerant parser (strip markdown fences, trailing commas)
     - PASS → squash-merge the task branch **into the session staging branch**, delete the task branch, update ledger row with `staging_sha` and `status='passed'`, break. **The target branch is not touched.**
     - FAIL → store `issue`/`fix` for next round
  4. On retry exhaustion: delete the task branch (no merge), update ledger with `status='failed'`, surface a one-line failure to the chat sink. The staging branch is unchanged, the target branch is untouched.
  5. **Marshal-model clarification is opt-in and rare.** The marshal system prompt instructs it to ask at most one question, and only when the task is genuinely ambiguous (e.g. "which database?" when two exist). When it does ask, the question streams into the chat pane like a normal assistant message and the user's reply becomes part of the same task (not a new one). Default behaviour for unambiguous prompts: skip marshal entirely and route straight to the executor. A `[marshal] clarify = "never" | "ambiguous" | "always"` config knob lets users opt out completely.
- `internal/loop/verdict.go` — strict JSON schema: `{verdict, summary, issue, fix, concerns[]}`, validation, error surfaces
- `internal/session` — SQLite store with three tables:
  - `sessions(id, target_branch, target_start_sha, staging_branch, started_at, shipped_at, shipped_target_sha)`
  - `tasks(id, session_id, prompt, parent_staging_sha, staging_sha, status, started_at, ended_at, summary)` — one row per user turn; `status` ∈ `{running, passed, failed, reverted_by_user}`; this is the ledger the reversal system reads
  - `rounds(session_id, task_id, round, role, model, prompt_tokens, completion_tokens, duration_ms, content, verdict_json, think_blocks)`
- `internal/prompts/phase1` — first-draft system prompts for marshal / executor / critic (refine in M8); the marshal prompt must be tuned for "silent pass-through by default"

**Critical subtlety to get right:** Aider's `base_coder.py` is the reference for "how do you build a chat-completions message array for code edits" — read it, but don't copy the Coder-subclass architecture. The message shape in `MARSHAL_MODEL_INTERACTIONS.md §2.2` is authoritative.

**Verify:** End-to-end tests in a tiny fixture repo, starting on `main`:
1. Session start: assert `marshal/session-<ts>` exists and is checked out, `main` unchanged.
2. Task "add a `Hello()` function to main.go": assert task branch created, round 1 runs, diff shows the function, critic returns PASS, task branch squash-merged to staging branch, staging HEAD advanced by one commit, task row in SQLite with `status='passed'`. **`main` still unchanged.**
3. Task that deliberately produces bad code: assert FAIL → retry → eventual failure, task branch deleted, staging HEAD unchanged, task row with `status='failed'`.
4. `Ship("test commit")`: assert `main` advances by exactly one squash commit containing only the changes from step 2, session row updated with `shipped_target_sha`, a *new* staging branch started from the new `main` HEAD.

---

### M4 — Conversational TUI + task ledger (2 weeks)

**Deliverable:** `marshal chat` feels like Aider/Claude Code — type, watch tokens stream, type again — but every turn is a tracked reversible task, and the user can list/undo/revert tasks by id.

**Fluid-loop UX requirements (non-negotiable):**
- Typing a prompt and hitting enter starts streaming executor tokens into the chat pane *immediately*. No confirmation modal, no "Running task..." spinner that blocks input.
- Critic verdicts render as an unobtrusive badge at the end of the assistant turn: green `✓` for PASS, amber `↻ round N` while retrying, red `✗ reverted` on failure. A one-line summary sits next to the badge. Think-blocks are collapsed by default; `tab` on the focused turn expands them.
- Retries are visible but not noisy — the TUI shows "retrying (round 2/3)" inline, then streams round 2's executor output as a *continuation* of the same assistant turn, not a new message. The user sees one cohesive turn per task regardless of round count.
- The user can start typing their next prompt while the current task is still running. Pressing enter queues the prompt; it runs as a new task as soon as the current one finishes (PASS or fail). A small `queued: 1` indicator shows in the status bar.

**Components (`internal/ui/tui`, bubbletea):**
- `chatViewport` — streams executor tokens, glamour-rendered markdown, per-turn verdict badge, collapsible think-block, collapsible diff preview
- `promptBar` — multi-line textarea with `/` command completion, always editable (never disabled while a task runs)
- `statusBar` — model roster, token spend this session, `target ← staging` branch pair (e.g. `main ← marshal/session-20260412-1430`), unshipped task count, queued-task count, current round indicator
- `historyPane` — toggled with `ctrl+h`, shows the task ledger: `[id] prompt (status, N rounds, ±lines)` with keybindings to revert or show diff. Visually separates shipped vs. unshipped tasks.
- `thinkBlock` — purple italic, collapsed by default

**Task ledger + staging commands (new — not in Aider):**
- `/history [N]` — show the last N tasks from the ledger (default 10) with id, prompt snippet, status, diff size
- `/undo` — revert the most recent *successful unshipped* task: `git revert -n <staging_sha>` on the **session staging branch**, commit as "Revert task <id>", update ledger row with `status='reverted_by_user'`. If the revert hits a conflict because later tasks touched the same files, abort and print the implicated task ids — do not try to auto-resolve. **The target branch is never touched.**
- `/revert <id>` — same as `/undo` but targets a specific task id; detects and reports dependent later tasks the same way. Only operates on unshipped tasks; shipped tasks are immutable from marshal's perspective (the user can `git revert` them manually).
- `/task <id>` — show a single task's full detail: prompt, rounds, verdicts, diff, think-blocks
- **`/ship [message]`** — the one explicit commit ritual. Squash-merges the session staging branch into the target branch using the provided message (or a compactor-generated one summarising the unshipped tasks), prints the new target SHA, updates the session row with `shipped_target_sha` and `shipped_at`, then **starts a fresh session staging branch from the new target HEAD** so chatting can continue seamlessly. Ledger rows for the shipped tasks are marked with the ship timestamp.
- `/ship --dry-run` — print the combined diff that *would* land on the target branch, without merging
- `/discard` — abandon the current session staging branch entirely (confirmation prompt required); the target branch stays untouched, ledger rows for the discarded tasks are marked `status='discarded'`, a fresh staging branch starts from the unchanged target HEAD
- `/session` — show the current session: target branch, staging branch, task count shipped vs unshipped, start time

**Slash command dispatcher (`internal/commands`) — M4 subset:**
`/add`, `/drop`, `/ls`, `/diff`, `/quit`, `/help`, `/model`, `/clear`, `/history`, `/undo`, `/revert`, `/task`, `/ship`, `/discard`, `/session`. Remaining commands land in M10.

**Wiring:**
- `internal/loop.Engine` exposes a `Submit(prompt) <-chan Event` channel API; the TUI renders events (`TokenChunk`, `RoundStart`, `VerdictBadge`, `TaskMerged`, `TaskReverted`, `ClarificationQuestion`)
- A task queue (`internal/loop/queue.go`) serialises submissions so two tasks don't fight over the working branch. One task in flight at a time; the queue is explicit so `/history` can show pending items.

**Verify:** Manual checklist in a scratch repo starting on `main` —
1. Launch `marshal chat`; assert status bar shows `main ← marshal/session-<ts>` and `main` is unchanged
2. Type 3 prompts in rapid succession without waiting; assert they queue and execute in order on the staging branch
3. Each produces a green badge and a squash-merge commit on the **staging branch**; `git log main` is still unchanged
4. `/history` lists all three with correct ids and diff sizes, all marked unshipped
5. `/undo` reverts the most recent; staging branch gains a `Revert task <id>` commit; ledger row updated; `main` still untouched
6. `/revert <id>` on a task that was touched by a later task — assert the conflict message lists the implicated later task and no revert commit is made
7. While a task is running, type the next prompt — assert status bar shows `queued: 1` and the second task starts automatically when the first finishes
8. Expand a critic think-block with `tab` — assert purple italic rendering
9. `/ship --dry-run` — assert combined diff is shown, no branches change
10. `/ship "test session"` — assert `main` advances by exactly one squash commit, status bar updates to show a new staging branch from the new `main` HEAD, ledger shows the shipped tasks with `shipped_at` set, subsequent prompts still work
11. `/quit` and restart: assert the old staging branch was cleaned up and a new session starts cleanly

---

### M5 — Repo map (1.5 weeks) — *can parallelise with M4*

**Deliverable:** Executor system prompt includes a token-budgeted, PageRank-ranked repo map equivalent to Aider's.

- `internal/repomap` ports `aider/repomap.py`:
  - Copy `.scm` query files from `aider/queries/tree-sitter-language-pack/` verbatim into `internal/repomap/queries/` (they're MIT-licensed via aider)
  - `go-tree-sitter` + language-pack parsers for Python/JS/TS/Go/Ruby/Rust/C/C++/Java/PHP (match aider's set)
  - Tag extraction: `(rel_fname, line, name, kind)` tuples, cached in SQLite keyed by file mtime+size
  - Rank: build the symbol-reference graph, run PageRank (use `gonum.org/v1/gonum/graph/network.PageRank`), boost chat files and mentioned idents
  - Token-budget packing: start from the top-ranked tags, include their "signatures" (one or two context lines from the source), stop at `max_map_tokens`
- Per-session cache invalidation on file changes

**Verify:** Run on the aider repo itself and compare token counts + top-ranked tags against aider's own `aider --map-refresh` output (they should be close, not identical — PageRank on a different graph shape is legitimate).

---

### M6 — Edit formats (1 week)

**Deliverable:** Executor can respond in `editblock` or `whole` format and the changes apply correctly.

- `internal/edit/searchreplace.go` — port `aider/coders/search_replace.py` fuzzy matcher (normalised whitespace, prefix-trimmed, leading-whitespace preserved). This is small but finicky — copy the test cases from `tests/basic/test_editblock.py` verbatim.
- `internal/edit/editblock.go` — parse SEARCH/REPLACE blocks, apply with fuzzy matcher, return `AppliedEdits{paths, rejected}`
- `internal/edit/wholefile.go` — parse fenced `path:\n\`\`\`content\`\`\`` blocks
- `internal/edit/udiff.go` — parse unified diffs, apply via `git apply --cached` (cheat: shell out)
- `internal/edit/format.go` — `Format` interface + `FormatFor(model) Format` picker consulting the per-model settings table

M6 replaces M3's stub executor-output handler. The loop now supports any edit format; the critic is unchanged because it only sees the real `git diff`.

**Verify:** Edit-format-specific tests. Integration test: run the M3 end-to-end test using the `editblock` format, assert identical outcome.

---

### M7 — Linter feedback loop (3–4 days) — *can parallelise with M6*

**Deliverable:** After a round's edits, configured linters run on changed files; failures are fed into the next round as critic-style feedback.

- `internal/linter` — shell out to linters based on file extension (Go → `golangci-lint run`, Python → `flake8`, JS/TS → `eslint`, …). All configurable via `[linters]` table in `marshal.toml`.
- Errors parsed into `LintResult{file, line, message}` and injected into the *executor's* next round prompt under a dedicated section (not the critic prompt — linter errors are objective, critic verdicts are subjective)
- `--lint` one-shot CLI flag: run linter, treat any failures as a task, loop until clean
- `/lint` slash command in the TUI

**Verify:** Make an intentional style error in a fixture; assert marshal catches it and fixes it in round 2.

---

### M8 — Prompt layering + compaction + skills (1 week)

**Deliverable:** System prompts are assembled from Base + Security + Skill layers; git diff is always the last message (for prompt-prefix caching); compaction kicks in after `compact_after` rounds.

- `internal/prompts/layers.go` — prompt assembler with fixed layer order, rejects any skill that tries to override a higher layer
- `internal/prompts/base/{marshal,executor,critic,compactor}.md` — canonical base prompts (write these carefully, they define product behaviour)
- `internal/prompts/security/` — standing instructions (no credential exfil, no network egress from tool-use, etc.)
- `internal/skills` — skill registry loaded from `~/.config/marshal/skills/*.toml`:
  ```toml
  name = "schema-migration"
  executor_additions = "..."
  critic_additions = "..."
  ```
  Ship three built-ins: `schema-migration`, `security-audit`, `test-generation`
- `internal/loop/compactor.go` — when `round > compact_after` (default 2), call compactor model with the conversational history only (no diffs, no verdict JSON), replace in-place
- **Caching discipline:** add a `cachekey` assertion in tests that the system-prompt prefix is byte-identical across rounds 1→N of a session, and the git diff is always the final message. This is the prompt-prefix-cache contract from §4.

**Verify:** Golden-file tests for prompt assembly. Integration test with `compact_after=1` that asserts rounds 3+ see a compacted history, not the raw rounds 1–2.

---

### M9 — Multi-task pipeline (Phase 3 of marshal) (2 weeks)

**Deliverable:** `marshal pipeline "add staff portal with timesheets and Rentman integration"` decomposes the feature into a task graph, runs tasks in parallel tiers on isolation branches, invokes the integration critic, and merges or holds.

- `internal/planner` — marshal model call that emits the task-graph JSON from §3.2; validation against a jsonschema; user confirmation in the TUI before execution
- `internal/pipeline/scheduler.go`:
  - Topological sort of task DAG → execution tiers
  - Within a tier, tasks with overlapping `files_likely_affected` are serialised via a `map[path]*sync.Mutex`
  - Each task runs in its own goroutine via `errgroup.WithContext`
  - Each task is a full M3 loop on its own `marshal/task-<id>` branch
- `internal/pipeline/integration.go` — after all tasks PASS, compute combined diff across all task branches, call the integration critic with the schema from §3.4, route PASS → topological merge **into the session staging branch** (not the target branch) / FAIL → keep branches, report implicated tasks. The user still has to `/ship` explicitly after a pipeline succeeds, consistent with single-task flow.
- TUI: pipeline view showing tiers as columns, task cards with per-task round counters and verdicts
- `--pipeline-only` flag: emit the task graph and exit without executing (useful for debugging the planner)

**Verify:** Fixture repo with a three-task feature; assert branches exist during execution, integration critic runs once, merges happen in topo order, HEAD ends at the merged state. Chaos test: make task 2 fail on every retry, assert the pipeline holds branches and surfaces the failure without merging.

---

### M10 — Aider-parity polish (2 weeks)

**Deliverable:** Everything on the Aider feature inventory that isn't already covered. Structured as a checklist so you can ship incrementally.

- **Slash commands** (finish the set): `/commit`, `/tokens`, `/run`, `/test`, `/git`, `/map`, `/map-refresh`, `/settings`, `/web`, `/paste`, `/read-only`, `/reset`, `/save`, `/load`, `/copy`, `/copy-context`, `/editor`, `/edit`, `/think-tokens`, `/reasoning-effort`, `/multiline-mode`, `/report`
- **Read-only files** — `abs_read_only_fnames` equivalent: tracked in session, included in executor context, never written
- **Watch mode** (`internal/watch`) — `fsnotify`-based, scan touched files for `// ai` / `# ai` / `// ai!` / `# ai?` markers, tree-sitter-extract the comment's enclosing block, submit as a task
- **Chat history summarisation** — port `aider/history.py::ChatSummary`; note this lives at the TUI chat level, not the loop level (M8's compaction is round-level)
- **Image/multimodal** — encode images as base64 data URIs, check per-model `supports_vision` flag, reuse M1's tile-token math
- **`.marshalignore`** — gitignore-syntax exclusions from the editable file set
- **Voice** (`/voice`) — record via `gordonklaus/portaudio`, transcribe via an OpenAI-compatible Whisper endpoint (Fireworks doesn't host Whisper — either OpenAI or Groq)
- **Web scraping** (`/web`) — `chromedp` for JS pages, `html-to-markdown` for the result, fall back to plain GET if chromedp unavailable
- **Help RAG** (`/help`) — embed aider/marshal docs into a local BM25 + embedding index (`blevesearch/bleve` for BM25, executor-model embeddings for vectors), query with `/help`
- **Onboarding** — detect available API keys from env, offer an interactive setup if `marshal.toml` is missing
- **Analytics** — opt-in, PostHog-compatible; log command names and token spend only, never prompts
- **One-shot mode** — `-m "task"` / `-f task.txt` / `--exit` flags for scripting

Ship this milestone in two sub-phases if needed: M10a = commands + read-only + watch + history + ignore (core), M10b = voice + web + help + onboarding + analytics (nice-to-have).

**Verify:** Drive an interactive TUI session through every command. Diff the feature checklist in this plan against the Phase-1 agent report in the conversation history.

---

### M11 — Headless / CI mode (3 days)

**Deliverable:** `marshal run --no-tui --json "..."` emits machine-readable output suitable for GitHub Actions.

- `internal/output/jsonstream` — emits NDJSON events: `session_start`, `round_start`, `round_end`, `verdict`, `merged`, `session_end`, with stable schemas
- Exit codes: `0 = PASS & merged`, `1 = task exhausted retries`, `2 = config error`, `3 = git error`, `4 = pipeline integration FAIL`
- Token-cost accounting per run, emitted in `session_end`
- Example `.github/workflows/marshal.yml` in `docs/ci/`

**Verify:** `marshal run --no-tui --json "..."` in a fixture repo and pipe into `jq`; assert schema conformance.

---

### M12 — Executor tool use (Phase 3 agentic) (1.5 weeks)

**Deliverable:** For models that support tool use (Qwen3-Coder, GPT-4, Claude), the executor calls `read_file` / `write_file` / `run_command` directly instead of emitting edit-format prose. The critic pipeline is unchanged.

- `internal/tools` — three tools matching §6.2 schemas exactly
- `internal/sandbox`:
  - Path allowlist: prefix-check against `repoRoot`; reject `..` traversal and symlinks that escape
  - Command allowlist: configurable via `[tools.run_command] allowlist = ["go test", "npm test", ...]`
  - Network egress: explicitly disabled for `run_command` (no `iptables` in v1 — just a documented limitation plus a "don't run with untrusted inputs" warning)
- `internal/backend/openai_compat` — wire up `tools` and `tool_choice` parameters; multi-turn tool-call loop within a single executor round
- Fallback: if `backend.SupportsTools() == false`, route to M6 edit formats automatically

**Verify:** Run the M3 end-to-end test in tool-use mode against a tool-capable model; assert the files are modified via tool calls (logged) and the critic sees the real diff.

---

### M13 — Benchmarks, docs, release (1 week)

**Deliverable:** Tagged v0.1.0 release on GitHub with binaries for 6 platforms, docs site, a benchmark report vs Aider.

- Port a subset of `benchmark/` from Aider — just the "exercism" runner, enough to produce a comparable edit-success-rate number
- Jekyll or Hugo docs at `docs/` — model-roster explainer, config reference, skill authoring guide, CI example, TUI keybindings
- `goreleaser` config → `linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}`
- `CHANGELOG.md`

**Verify:** Install the released binary on a fresh machine, walk through `marshal config init` → `marshal chat` → complete a real task against a public repo. Run the benchmark and publish the number.

---

## Critical files to reference from aider during the port

These are the files the Go code is conceptually descended from — read them before writing the Go equivalent, but do not port line-for-line (different language, different architecture).

| Aider file | Go counterpart | Why |
|---|---|---|
| `aider/coders/search_replace.py` | `internal/edit/searchreplace.go` | Fuzzy matcher; **port the test cases verbatim** from `tests/basic/test_editblock.py` |
| `aider/reasoning_tags.py` | `internal/reasoning/tags.go` | `<think>` stripping logic and tag patterns |
| `aider/repomap.py` | `internal/repomap/*.go` | Ranking algorithm and token-budget packing |
| `aider/queries/tree-sitter-language-pack/*.scm` | `internal/repomap/queries/*.scm` | **Copy verbatim**, they are the real asset |
| `aider/repo.py` | `internal/git/repo.go` | GitRepo API shape and attribution rules |
| `aider/linter.py` | `internal/linter/linter.go` | Language → command mapping, error parsing |
| `aider/history.py` | `internal/history/summary.go` | Chat-history compaction decision logic (distinct from M8 round-compaction) |
| `aider/commands.py` | `internal/commands/*.go` | Slash command dispatcher inventory |
| `aider/models.py::token_count_for_image` | `internal/tokens/image.go` | Tile-based vision-token math |
| `aider/sendchat.py` | `internal/backend/openai_compat.go` | Retry/backoff behaviour; `RETRY_TIMEOUT=60s` |
| `aider/watch.py` | `internal/watch/watch.go` | AI-comment regex and tree-sitter block extraction |
| `aider/resources/model-settings.yml` | `internal/models/settings.toml` | Per-model config table; start from this and trim to models you actually support |

## End-to-end verification plan

After M12 the full system can be exercised by this scenario — keep it as the top-level integration test:

1. Fresh clone of a small public repo (use `charmbracelet/bubbletea` examples as the test subject); user checks out `main`
2. `marshal chat` in the repo root; verify the repo map populates, model roster is the local/Ollama profile, status bar shows `main ← marshal/session-<ts>`, `main` HEAD unchanged
3. **Fluid conversation test.** Type three prompts in rapid succession without pausing: *"add a `--version` flag"*, *"make the help text mention the new flag"*, *"add a test for it"*. Observe: no confirmation prompts, first task streams immediately, other two queue visibly in the status bar, all three execute in order, each ends with a green PASS badge, the **staging branch** accumulates three squash commits, `main` is still unchanged, `/history` lists three unshipped tasks.
4. **Clarification test.** Type *"update the config"* (ambiguous — there are multiple config files). Observe: marshal model streams a single clarifying question inline, user replies, the reply is treated as part of the *same* task (same id, same branch), executor proceeds.
5. **Failure test.** Type *"replace the main function with something completely unrelated"*. Observe: critic FAILs round 1 (badge shows `↻ round 2`), executor retries with the fix hint, critic FAILs again at round 3, task branch silently deleted, badge updates to `✗ reverted`, staging branch HEAD unchanged, `main` still unchanged, one-line failure summary in the chat.
6. **Task ledger test.** `/history` shows the successful tasks from step 3 and the failed task from step 5 with distinct statuses. `/undo` reverts the most recent successful task (the test addition); staging branch log shows a `Revert task <id>` commit; `main` still untouched. `/revert <id>` on the `--version` flag task (the first of the three, now with dependent later tasks) — assert the conflict message lists the dependent tasks by id and refuses to auto-revert.
7. **Ship test.** `/ship --dry-run` prints the combined diff of the remaining unshipped tasks; `/ship "add version flag and help text"` squash-merges the staging branch into `main`, `main` advances by exactly one commit with the user's message, status bar updates to show a new staging branch from the new `main` HEAD, ledger rows for the shipped tasks get `shipped_at` set, subsequent prompts still work against the fresh staging branch.
8. **Pipeline test.** `marshal pipeline "add a new example that demonstrates list + form composition, with tests"`. Observe: planner emits a 3-task graph, user confirms (pipeline mode is the one place confirmation is kept — the plan is too high-stakes to auto-run), tier 1 tasks run in parallel goroutines with separate TUI lanes, tier 2 (tests) waits, integration critic reviews the combined diff, all tasks merge in topological order **into the staging branch**, user `/ship`s explicitly to land on `main`.
9. **Headless test.** `marshal run --no-tui --json "fix the TODO in cmd/main.go"` piped into `jq .verdict`; assert schema match. Headless mode still uses the staging branch pattern and auto-`ship`s on success unless `--no-ship` is passed (CI flag).
10. **Watch test.** `marshal --watch` in another terminal; add a `// ai: add a comment explaining this function` marker to a source file; save; observe marshal pick it up and run a task that appears in the `marshal chat` ledger of the primary terminal as an unshipped task.
11. **Safety test.** At any point, run `git log main` from outside marshal; assert `main` has only advanced in response to explicit `/ship` calls. Delete the session staging branch via `git branch -D`; assert marshal detects the loss and reports cleanly rather than corrupting state.

All eleven steps passing green is the definition of v0.1.0-ready.

## Locked decisions

These were confirmed before M0 and are not up for revision in this plan:

1. **Task merges use `git merge --squash`** so every task = one commit, making `/undo` and `/revert <id>` trivial
2. **Session staging branch model.** Marshal creates `marshal/session-<timestamp>` off the user's target branch at session start; all task work accumulates there; the target branch is only touched when the user types `/ship`. Safety first: the user's existing branch is never modified without an explicit command.
3. **Backends.** OpenAI-compatible only in v0.1; native Anthropic and Google adapters are a follow-up (`internal/backend/anthropic.go`, `internal/backend/vertex.go`) slotting into the same `Backend` interface without touching the loop.
4. **Edit formats are kept.** M6 ships the full editblock/udiff/wholefile/patch set so marshal works with models that aren't trained for tool use. M12's tool-use mode is additive, selected per-model via `backend.SupportsTools()`.
5. **TUI:** `bubbletea` + `lipgloss` + `bubbles`.
6. **Binary name:** `marshal`.
7. **License:** Apache-2.0, matching Aider, with attribution notices for the ported tree-sitter queries and search-replace test cases.
