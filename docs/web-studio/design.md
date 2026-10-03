# Marshal Web Studio — Design

Status: draft, in progress. Decisions recorded 2026-10-03.
Base: `main` at `2ddc09e`.
Mockups:
- [`mockups/design.html`](mockups/design.html): high-fidelity, the chosen direction.
- [`mockups/proposal.html`](mockups/proposal.html): the options we chose between.

Open both in a browser.

Related: the single-stack TUI work in [`../single-stack/`](../single-stack/),
whose transcript model this design reuses.

---

## 1. Summary

Marshal Web Studio is the browser UI for a self-hosted Marshal: something
like Claude Code on the web crossed with Cursor, but running on your own
machine or server. In the browser you can:

- start agents and watch them work;
- steer them and answer their questions;
- review and ship what they produce;
- run multi-agent plans and swarms;
- design the workspaces agents run in;
- manage the automations, monitors and knowledge around all of that.

Two rules shape the design:

1. **Same core UI logic as the TUI.** The transcript is the single-stack
   model from P1–P3: turn → task → step → tool row, with narration
   headlines, owner labels, folding, receipts, density levels, browse mode
   and the inspector. The web restyles it and never re-implements it.
2. **The bridge stays an external client.** `web/` is standard library only
   and must not import `marshal/internal/...` (`TestWebIsStdlibOnly`). All
   agent-side data crosses a JSON contract.

## 2. Goals and non-goals

### Goals

- One home for every agent, run and automation, across all projects.
- Transcript parity with the TUI, plus a workbench (diff, files, terminal,
  preview) that a browser can do better than a terminal.
- Workspaces as a first-class, reusable, versioned object. A workspace
  bundles:
  - toolchains and packages;
  - shared files and read-only library mounts;
  - secrets, network rules, resources and a setup script.
- Automations built from the same pieces: a PR review bot, a CI fixer,
  watches and schedules.
- A design ready for multiple users from day one: owners on every object,
  actor identity everywhere, and every action auditable.

### Non-goals (for now)

- Building multiple users: accounts, sign-in, SSO, enforcing roles. The
  design leaves room for these, and the work is deferred (§9).
- A full IDE. The editor view from the proposal (option C) is not planned.
- Granting folder trust from the browser. This stays a deliberate TUI-only
  action.
- Replacing the TUI. Both clients read the same sessions and can hand a
  session to each other.

## 3. Decisions

| Area | Decision | Mockup |
|---|---|---|
| App shell | Left icon rail + agent sidebar + content. The sidebar collapses. | design §1 |
| Home | **Inbox**: groups for needs-you, ready-to-ship and running, with a capacity and cost side panel | design §2 |
| Live wall | **A separate page** in the rail, not a toggle on Home | design §2 |
| New agent | **Prompt first**, with defaults as chips; issues, recent prompts and recipes as tabs underneath | design §3 |
| Agent session | **One layout, four states**: transcript plus a dock that can be collapsed, docked or expanded, and follows either the agent or your selection | design §1 |
| Review & ship | **PR style**, with a "By step" toggle | design §4 |
| Runs | **Stage lanes** and a **task graph** as two views of one run page, with a timeline tab | design §5 |
| Workspaces | **Layer builder**, with the source file editable next to it (two views of one document). The gallery is the list page | design §6 |
| Supporting pages | Project overview, Library, Models & routing, Usage & audit, as mocked | proposal §4 |
| New features in scope | Watch monitors, network inspector, PR review bot, CI fixer, shared memory across projects, status page | design §7–12 |
| Theme | Warm Sunset (the TUI's palette), dark-first. The status page is light | all |
| Multiple users | Designed for, built later | design §13 |
| TUI logic in the web | Send the view model over ACP (`_marshal/stack`) | §5.2 |
| Terminal and the agent | Typing in the terminal **pauses the agent automatically**; handing control back resumes it | §8.5 |
| Plan dependencies | Add **`Depends on:` lines** to the `/sdd` plan format; the task graph reads them | §8.7 |
| Secrets | **Keep secrets out of agent containers wherever possible.** A pluggable secret provider, with an external manager recommended and a small built-in encrypted store for local single-machine use | §8.15 |
| Network control | **One shared egress proxy** in the bridge, with agents on an internal-only network and identified per agent | §8.11, §8.15 |
| Specs location | Feature specs live under `docs/<feature>/`; AGENTS.md updated to say so | §10 |

## 4. Where we start

The table below lists the web client's capabilities and where each one is
handled today. The full inventory is in the proposal mockup, §2.

| Capability | Where |
|---|---|
| Fleet dashboard, attention list, spawn (also from issues), chat with permissions and questions, six modes, steer and cancel | `web/ui` (`Dashboard`, `Chat`, `AttentionList`, …) |
| Diff, merge, discard, patch; the exit path (commit → verify gate → push → PR, gate override) | `web/bridge/exit.go`, `forge_*.go`, `ExitPanel` |
| Sessions, projects, MCP clients and tokens, intake queue, audit, disk and prune | `web/bridge/http.go` routes, matching panels |
| Containerized agents with runtime profiles, resource caps, the env allowlist, credentials, bare mirrors, mount translation | `web/bridge/container.go`, `profile.go`, `env.go`, `credential.go`, `mirror.go`, `mounts.go` |
| Issue poller, fleet limits and queue, `/mcp` endpoint | `poller.go`, `queue.go`, `mcp*.go` |
| ACP methods with no web screen yet: skills, plugins, memory, `sdd_*`, `swarm_*`, roster, commands, verify, commit, resume | `internal/acp` |
| Engine features with no ACP exposure: the step/task view model, settings and routing editing, turn metrics, watches, history and rewind, the last-request record (`/context request`) | `internal/app/tui/stack`, `internal/app/config`, `internal/db`, `internal/watch` |

## 5. Architecture

### 5.1 Processes

```
browser (Svelte SPA)
   │  HTTPS: JSON + SSE (auth: bearer token today, user sessions later)
webbridge (Go, standard library only)
   │  ACP over stdio or a Unix socket, one per agent
marshal acp  ×N   (child process, or a container per agent)
```

The bridge owns:

- the fleet, intake, mirrors, worktrees and containers;
- the forge, credentials, audit and (new) the workspace build pipeline.

Each `marshal acp` process owns one agent's session, its tools and its view
model.

### 5.2 Transcript parity: sending the view model

The browser must not re-implement grouping, so the logic travels as data.

1. **Move the package.** `internal/app/tui/stack` moves to
   `internal/viewmodel` (structure only, no styling, which it already is).
   The TUI imports it from there, and so does `internal/acp`.
2. **New ACP notification `_marshal/stack`:**
   - On attach (`session/load` and `session/new`), a **snapshot**: the full
     node tree.
   - Afterwards, **patches**: `upsert` and `remove` operations keyed by
     the existing node IDs (`turn:<id>`, `task:<turn>:<todo>:<seg>`,
     `step:<seq>`, `tool:s<step>:<callId>`, …), each with the node's
     version hash, so unchanged nodes are never resent.
3. **Bridge** passes the notification through the per-session SSE stream
   unchanged. It may cache the latest snapshot so a reconnecting browser
   resyncs without asking the agent.
4. **Browser** keeps a node map and renders it. Density, folding overrides,
   the browse cursor and the dock selection are client state. They aren't
   sent back, just as in the TUI.

Contract sketch (the field names follow `viewmodel` types):

```json
{ "method": "_marshal/stack", "params": {
  "sessionId": "…", "kind": "patch", "rev": 812,
  "ops": [
    { "op": "upsert", "id": "step:14", "parent": "task:turn:31:t3:1", "version": "9f2c…",
      "node": { "kind": "step", "live": true, "actor": { "role": "implementer", "label": "implementer", "model": "qwen3-coder", "provider": "ollama" },
                "headline": "The guard returns the wrong error; switching it to ErrEmpty.", "inferred": false,
                "startedAt": "…", "endedAt": null, "state": "running" } },
    { "op": "remove", "id": "think:live" }
  ] } }
```

- **Rules computed in Go and sent as fields:** task fold eligibility
  (`unresolvedFailure`), receipt numbers, inferred headlines, and the
  render-time re-binding of steps to tasks. The browser renders these and
  doesn't recompute them.
- **Existing ACP updates stay:** `agent_message_chunk`, `tool_call` and
  `tool_call_update` remain for other ACP clients. The web UI uses the
  stack stream for structure, and tool-call updates only to stream output
  into live rows.

### 5.3 New bridge API (by area)

All routes keep today's auth and audit rules. Ones marked **(exists)**
already ship.

| Area | Routes (sketch) | Backed by |
|---|---|---|
| Sessions | `GET /api/sessions/{id}/stack` (snapshot), the per-session SSE stream carries the stack | ACP `_marshal/stack` |
| Files | `GET /api/agents/{id}/files?path=` (tree), `GET …/file?path=` (read only, confined to the worktree) | `agentpath.go` confinement |
| Terminal | `POST /api/agents/{id}/terminal` (open), SSE for output, `POST …/input` (stdin); pauses the agent while held | container exec; new |
| Preview | `/preview/{agent}/{port}/…` reverse proxy to declared ports | `net/http/httputil`; new |
| Runs | `POST /api/runs` (sdd or swarm), `GET /api/runs/{id}`, `POST /api/runs/{id}/answer` | ACP `sdd_*`, `swarm_*` |
| Library | `GET/POST/DELETE /api/skills…`, `…/plugins…`, `…/memory…` | ACP skills, plugins, memory |
| Models | `GET/PUT /api/models/providers`, `…/routing`, OAuth connect | new ACP config methods (user-global only) |
| Usage | `GET /api/usage?by=project\|role\|model&range=` | `turn_metrics`, the pricing table |
| Workspaces | `GET/PUT /api/workspaces/{name}`, `POST …/build`, `GET …/builds/{n}` (log over SSE), `POST …/shell` | new build pipeline |
| Network | `GET /api/network?workspace=&agent=`, `POST /api/network/rules` | new egress proxy log |
| Watches | `GET/POST/DELETE /api/watches`, `GET …/{id}/series` | `internal/watch` over ACP (new methods) |
| Automations | `GET/PUT /api/projects/{id}/automations/{review-bot\|ci-fixer}`, `POST /api/hooks/forge/{repo}` (webhook in) | forge adapters, intake `Submit` |
| Status links | `POST /api/status-links`, `DELETE …/{token}`, public `GET /s/{token}` | new; filtered view model |
| Fleet events | new SSE kinds: `run`, `watch`, `network_block`, `automation`, `budget` | `fleetevents.go` |

### 5.4 Ready for multiple users (designed for, built later)

These go in from W1, so adding users later doesn't need a redesign:

- **Owner on every object.** Agents, runs, workspaces, secrets, memories,
  watches, automations and status links all carry an owner. With one
  operator, every owner is `DefaultOwnerID`, which the bridge already has.
- **Origin on every agent.** Each agent records who or what started it:
  `you | schedule | ci | review-bot | mcp:<client> | issue:<n>`. This drives
  avatars and the inbox's "Mine / Everyone" switch.
- **Every action is one operation.** Each mutating action is a single
  bridge operation with a verb and an object, so a later permission check
  can sit at one point and every action is already written to the audit log
  the same way.
- **Per-person UI preferences** (dock width, density, rail state) are stored
  per browser now and keyed so they can move to a user record later.
- **Deferred:** user accounts, sign-in and SSO, roles (viewer, operator,
  admin) and their enforcement, per-user notifications, team-shared global
  memory.

## 6. Visual design system

- **Theme:** Warm Sunset, dark-first, matching the TUI.

  | Token | Value | Use |
  |---|---|---|
  | accent | coral `#ff875f` | primary actions, the live/running state |
  | violet | `#d787ff` | reviewer, selection, browse mode |
  | gold | `#ffaf00` | implementer, workspaces |
  | ok | teal `#3fd4b4` | success |
  | err | `#ff87af` | failures |
  | warn | `#ffaf5f` | needs you, warnings |
  | info | `#5fd7ff` | info, planner and branch reviewer |

  Neutrals are warm near-blacks (`#121113` → `#2c2a30`). A light theme is
  derived for the status page and an optional light mode.
- **Type:** Geist for the UI and Geist Mono for code, paths, durations and
  counts. Durations and counts use tabular figures.
- **Glyphs:** the TUI vocabulary (`tui/glyph`) is reused as icons, so the
  two clients read the same:

  | Glyph | Meaning |
  |---|---|
  | `▸` / spinner | running |
  | `✓` `✗` `⚠` `?` | outcomes |
  | `✎` | edit |
  | `≡` | file |
  | `›` | shell |
  | `◈` | search / library |
  | `⧉` | agent |
  | `◇` | web / preview |
  | `○` | watch |
  | `┆` | job |
  | `▦` | workspace |
  | `⋔` | runs |
  | `◉` | live |
  | `⌂` | home |

- **Components:**
  - rail, sidebar list items, cards, tags (semantic and role-coloured);
  - segmented controls, toggles, chips with popovers, a command palette;
  - the transcript set: task row, open-task rule, step header, tool row,
    output block, now bar and composer;
  - the dock: header, follow badge, tabs, strip;
  - diffs, the gate checklist, stage cells, graph nodes, sparklines, toasts.
- **Motion:** live dots pulse, and nothing else animates by default. Honour
  `prefers-reduced-motion`.
- **Keyboard:** TUI parity:
  - `Esc` enters browse mode; `j`/`k`/`J`/`K`/`g`/`G` move;
  - `Enter` changes density, `i` inspects, `y` copies, `o` opens, `f`
    drills in, `z` folds;
  - `⌘K` opens the palette, `⌘J` or `\` sizes the dock, `/` filters;
  - in approval dialogs, `y`/`n`/`a`.

## 7. Information architecture

| Rail | Route | Page |
|---|---|---|
| ⌂ Home | `/` | Inbox |
| ◉ Live | `/live` | Live wall |
| ⧉ Agents | `/:project/agents/:id` (+ `/new`, `/review`) | Session, New agent, Review & ship |
| ⋔ Runs | `/:project/runs/:id?view=lanes\|graph\|timeline` | Plan runs and swarms |
| ▦ Workspaces | `/workspaces`, `/workspaces/:name/edit\|network\|builds` | Gallery, designer, network inspector |
| ≡ Projects | `/projects/:id` (+ `/automations/review-bot\|ci-fixer`) | Project overview, automations |
| ○ Watches | `/watches` | Watch monitors |
| ◈ Library | `/library/skills\|plugins\|mcp\|memory\|recipes` | Library, shared memory |
| ∿ Usage | `/usage?tab=cost\|audit\|disk` | Usage & audit |
| ⚙ Settings | `/settings/models\|providers\|tokens\|notifications` | Settings |
| (public) | `/s/:token` | Status page |

## 8. Page specs

### 8.1 Shell

**Layout:**
- Rail (52px).
- Sidebar (256px, collapsible, remembered per browser) with a project
  switcher, search (`⌘K`) and agents grouped as Needs you, Running, Ready
  to ship, Earlier.
- Content.

**Behaviour:**
- The sidebar groups update live from fleet SSE.
- Every group sorts by urgency, then by time.

### 8.2 Home: Inbox

**Groups:**
- **Needs you:** approvals and questions, oldest first. Each shows owner,
  origin and step, the "why" (the narration's first sentence), risk and
  sandbox or network. Approvals offer *Approve once*, *Allow "<pattern>" in
  <workspace>* and *Deny*. Questions offer their options plus a free reply.
- **Ready to ship:** gate status, diff stat and summary. Actions are
  Review, Open PR, and post a review-bot draft.
- **Running:** progress segments, headline and elapsed time.

**Side panel:** capacity (containers, warm pools, state volume), today's
spend against the cap with a sparkline, and tripped watches.

**Switch:** "Mine / Everyone". With one operator, both show everything,
with origin avatars.

**Data:**
- Exists: fleet store, pending requests, disk.
- New: budgets, warm pools, watches, automation origins.

### 8.3 Live wall

**Layout:** a grid of tiles (3 or 4 across, paged past 12). Each tile
shows:
- name, project and elapsed time;
- progress segments;
- the last three step headlines (outline density);
- workspace, model and gate chips.

**Behaviour:**
- Tiles that need you are tinted and take decisions inline.
- Clicking a tile opens the session with the dock collapsed.
- Filters: project, "Runs only".

**Data:** the stack stream for each visible agent, outline density only.
The bridge should send a reduced "last 3 headlines" view per agent to limit
traffic.

### 8.4 New agent

**Layout:** a prompt box with chips for project, branch, workspace, model,
mode and isolation, each defaulted from the project.

**Workspace picker** shows each workspace's contents, its warm-pool status,
and a warning when the verify gate would be skipped.

**Tabs underneath:** Issues (forge), Recent prompts, Recipes.

**Data:**
- Exists: `POST /api/agents`, IssuePicker.
- New: recipes, workspace metadata.

### 8.5 Agent session: one layout, four states

Transcript in the centre (max 780px). On the right, a **dock** with tabs
Inspect · Changes · Files · Terminal · Preview.

**Two independent axes:**

| | Follows the agent (nothing selected) | Follows your selection |
|---|---|---|
| **Collapsed** (46px strip) | Badges: gate failing (Changes), unread output (Terminal) | n/a. Selecting opens the dock |
| **Docked** (~440px, resizable) | "● following agent": Changes shows the file being edited and the live gate; Terminal tails the running command | "◆ step 3.2": Inspect shows the node's details; Changes filters to that step's hunks; Terminal shows that command's run |
| **Expanded** (~65%) | Same as docked, wider; the transcript becomes an **outline** navigator | Same; picking an outline entry moves the selection |

**Rules:**
- **Entering selection:** `Esc` (browse mode) or a click on a node. Browse
  keys move the selection, and the dock follows.
- **Leaving selection:** `Esc` again, or "Back to live".
- **Pinning:** a pinned tab doesn't switch when the selection changes
  (`⌘P`). It's for keeping a terminal open while you browse.
- **Now bar** in selection mode shows the live mirror row
  (`↓ live: <headline>`), so the running agent stays visible.
- **Expanded dock:** the transcript switches to outline density, and the
  composer stays at the foot of the outline.
- **Inspect tab** contents:
  - actor, why, timing, sandbox, approval, call ID;
  - tabs for output, args, narration, thinking;
  - "fixed by" and "caused by" links between steps (derived from later
    steps touching the same files or test);
  - the last model request (`/context request`), when the node is a step.
- **Terminal:** shows the selected command's replay, then a shell in the
  agent's container. **Typing pauses the agent automatically** (decided).
  The dock shows "agent paused · Hand back". The agent resumes when you
  hand back, or after 2 minutes without typing. Shell sessions are
  recorded in the audit log.
- **URL state:** `#step-3.2`, `?dock=wide`. The dock width and default
  state are remembered per browser.

**Keyboard:** `⌘J` or `\` cycles collapsed → docked → expanded; `i`
inspect; `Esc` back to live; `⌘P` pin; plus the TUI browse keys.

### 8.6 Review & ship

**Layout:**
- **Left panel:**
  - the gate checklist, with an override that requires a reason (exists);
  - the file list;
  - the conversation summary;
  - **Ship:** a commit message drafted from the receipt and summary, plus
    toggles to open a PR and to request the review bot. Actions: Discard,
    Merge locally, Push & open PR.
- **Diff:** unified or split, with "viewed" marks. Line comments are sent
  to the agent as steering. The agent's reply and new commits appear in the
  thread, which you can resolve.
- **"By step" toggle:** groups hunks by task and step, using the step's
  files changed and the per-call diff on each tool record. Shows the net
  effect of each step when later steps rewrite earlier lines.

### 8.7 Runs: lanes and graph

**One run page with three views:**
- **Lanes:** rows are plan tasks; columns are Implement, Verify, Review,
  Commit. Cells show the state, fix rounds and commit SHA. A roles legend
  shows model, tokens and cost.
- **Graph:** nodes are tasks with role, state and progress; edges are
  dependencies; a dashed critical path. Dependencies come from optional
  `Depends on:` lines in the plan (decided), and from planner-split
  swarms when those produce them.
- **Timeline:** a tab with per-role bars and tokens per minute.

**Dock:** the same dock as the session page. Selecting a cell or node shows
that role's transcript.

**Plan format addition:** an optional line inside a task section.

```markdown
## Task 4: Exit 2 on ErrEmpty
Depends on: 2, 3
```

- **Parser** (`pipeline.ParsePlan`): reads it into
  `TaskSpec.DependsOn []int`. It rejects unknown task numbers and cycles
  with a plan diagnostic.
- **Run order:** today tasks run in file order. With dependencies, the
  controller may start a task once its dependencies are committed. The
  first version keeps the serial order and only validates and draws the
  graph. Running tasks in parallel is a later change, because it needs a
  worktree per task.
- **Without the line:** a task depends on the previous task, which matches
  today's behaviour.

**Data:**
- Exists: ACP `sdd_*`, `swarm_*`, `SDDProgress`, the pipeline ledger.
- New: bridge routes, run SSE, dependency edges.

### 8.8 Workspaces

**Gallery:** templates with version, contents, usage, starters, "Import
devcontainer.json" and "Snapshot a running agent".

**Designer:** the layer builder and `workspace.toml` are two views of one
document. Selecting a layer highlights its source lines, and edits sync
both ways. The view switch has three positions: Layers | Layers + source |
Source.

**Layers:**
1. base image
2. toolchains
3. packages
4. libraries and repos (read-only mounts, shared caches)
5. shared files
6. secrets (references to the vault; values are never written to disk)
7. network (open | allowlist | off, with per-host usage)
8. resources
9. setup script

**Side panel:** build (size, cold and warm start, warm pool), the verify
gate (what becomes runnable), policy (default mode, workspace-scoped
approval rules), history (versions, diff between versions).

**Template file:** `.marshal/workspaces/<name>.toml`. It's covered by the
trust hash, so a changed template in an untrusted project is ignored, as
project config is today. Draft schema:

```toml
[workspace]
name = "go-service"
base = "marshal/agent:0.19"
toolchains = ["go@1.26", "node@22"]

[packages]
apt = ["ripgrep", "jq", "sqlite3"]
go  = ["github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64"]

[[mounts]]
repo = "design-system"; target = "/lib/design-system"; readonly = true
[[mounts]]
volume = "gomodcache"; target = "/root/go/pkg/mod"

[files]
"fixtures/" = { target = "/work/shared", readonly = true }

[secrets]
GITHUB_TOKEN = "vault:github/marshal-bot"

[network]
mode = "allowlist"
egress = ["proxy.golang.org", "registry.npmjs.org", "api.github.com"]

[resources]
cpu = 4; memory = "8g"; disk = "20g"; timeout = "2h"

[policy]
mode = "edit"
allow = ["go test *", "go build *"]

setup = "go mod download"
```

**Build pipeline (bridge):**
- Generate a Dockerfile per layer.
- Build through the detected runtime (Docker or Podman), with a cache per
  layer. Only changed layers rebuild.
- Tag `marshal-ws/<name>:v<n>`.
- Keep warm pools of N idle containers.

**Compatibility:** a `devcontainer.json` `image` stays supported as a
"base image only" workspace.

### 8.9 Project overview and automations

**Project overview:**
- defaults (workspace, model and reviewer, mode, isolation, ship target);
- intake (issue labels, MCP clients, schedules);
- health (gate runnable, mirror freshness, leftover worktrees, index);
- policies;
- a session-sheet tab reusing the TUI's sections.

**PR review bot:**
- **Trigger:** a forge webhook (or the poller) runs the read-only "Review
  a PR" recipe.
- **Drafts:** reviews land in the inbox as drafts with severity-labelled
  findings (blocking, should fix, nit) and evidence links to steps.
- **Actions:** Post, Edit, Discard. When the PR came from a Marshal agent,
  "Send to author agent" sends a finding back to it as steering.
- **Settings:** which PRs, reviewer model, auto-post or hold.

**CI fixer:**
- **Trigger:** a failed check on watched branches.
- **Stages:** fetch log → reproduce → fix → gate → PR (or push, if
  allowed).
- **Rules:**
  - only start if the failure reproduces;
  - a time and cost limit;
  - **never skip, disable or quarantine tests**, enforced in the recipe's
    prompt and policy.
- **History:** fixed, didn't reproduce, gave up.

### 8.10 Watch monitors

**Table:** source, condition, a 24h sparkline, the action on trip, state.

**Detail:** a chart with the threshold, what tripped and what was done, the
clear condition, affected agents.

**Actions:**
- Exist in the engine: notify, wake or resume an agent, start a recipe,
  prune.
- New: reroute a role to another model (for example, when local inference
  is slow).

**Agent-started watches** are listed too, marked with the agent that
started them.

**Data:** `internal/watch` exists, but ACP methods to list and create
watches are new.

### 8.11 Network inspector

**Per workspace or per agent:** hosts with rule, request count, data,
agents and last seen; plus Requests and By-agent views.

**Blocked requests from a running agent** become a decision in the inbox:
Block, Allow for this agent, Add to workspace. "Add to workspace" writes a
draft change to the template.

**Needs:** the shared egress proxy described in §8.15, which logs
connections. This is new work; today the sandbox can only switch the
network on or off.

### 8.12 Shared memory across projects

**Memories gain:**
- a **scope**: project, workspace or global;
- **provenance**: learned in which project, agent and step; confirmed by
  which agents;
- an **owner**.

**Promotion:** promoting a memory to a wider scope is suggested when it
holds in a second project, and needs your approval.

**Data:** listing, deleting and setting confidence exist over ACP. Scopes,
provenance and promotion are new, and need a schema change in `memories`.

### 8.13 Models & routing, Usage & audit

**Models & routing:**
- providers with health;
- role → model routing (default, implementer, reviewer, planner,
  summarizer and title, embedding);
- budgets: a daily cap, a per-agent cap, and what happens at the cap.

Saves go only to the user-global config, as everywhere else.

**Usage & audit:**
- cost, tokens, agent hours and PRs shipped;
- cost by role;
- the audit log and disk panels, moved here from today's UI.

### 8.14 Status page

A public, read-only page for a run or agent (`/s/<token>`), light theme.

**Shows:** progress, task list, and the current step headline only.

**Never shows:** tool output, file contents, secrets, transcripts or the
composer.

**Built from:** the view model, filtered on the server to task and step
headlines.

**Links:** tokens are unguessable, expire (default 7 days) and can be
revoked. Creating, listing and revoking links is audited.

### 8.15 Secrets and network security

**Principle:** the safest secret is one the agent never holds. The agent is
driven by a model and reads untrusted text (issues, web pages, dependency
READMEs). Anything in its environment can be printed, written to a file or
sent out. The choice of secret store matters less than keeping secrets out
of the container.

**Where each secret is used:**

1. **Bridge-side use (default).**
   - Git push, PR creation and forge API calls already run in the bridge
     with its own credentials (`credential.go`, `exit.go`, `push.go`).
     Agents never see forge tokens. This stays the rule for anything the
     bridge can do on the agent's behalf.
   - Model provider keys are used by the agent's runtime. They should
     reach the provider through the proxy below, not as environment
     variables.
2. **Proxy-injected credentials.**
   - For HTTP APIs an agent must call itself, a workspace can declare a
     service, for example `[secrets.inject] "api.github.com" =
     "vault:github/marshal-bot"`.
   - The agent calls the host normally, and the egress proxy adds the
     credential to the request.
   - This needs the proxy to terminate TLS for those hosts only. The
     bridge issues a per-workspace CA that is trusted inside the
     container. Every other host passes through untouched (CONNECT
     tunnel).
3. **Environment injection (last resort).**
   - Only for tools that must read a secret locally, for example a CLI
     with no HTTP equivalent.
   - The secret is marked in the template, values are redacted in
     transcripts (`internal/redact`), and each use is written to the
     audit log.

**Secret storage: a pluggable provider in the bridge.**

| Backend | When | Notes |
|---|---|---|
| External manager (OpenBao / HashiCorp Vault over HTTP, 1Password via the `op` CLI, `pass`) | **Recommended** for anything shared, remote or long-lived | Key management, rotation, access policy and audit are battle-tested. Reachable from standard-library Go (`net/http`, `os/exec`). |
| Built-in encrypted store | Local, single machine, nothing else installed | AES-256-GCM (standard library). The key comes from the OS keyring or a key file **outside** the state volume, never next to the data. Owner-scoped like `Credential`. |
| Environment variables | Today's behaviour; kept as a fallback | Read at use time, never persisted (the current `Credential` design). |

**Why not only a built-in vault?** It would put Marshal in charge of key
storage, rotation and access control. That's the hard part of a vault, and
the part most likely to go wrong in a home-grown one. It stays a
convenience backend for local use.

**Network enforcement: one shared egress proxy.**

- **Isolation:** agent containers join an **internal-only** container
  network (`docker network create --internal`) with no route out. The
  proxy is the only bridge between that network and the outside. Agents
  can't bypass it by ignoring `HTTP_PROXY`.
- **One proxy:** a single process in the bridge, or one sidecar container
  next to it. It's a standard-library HTTP CONNECT proxy, plus TLS
  termination only for injected services.
- **Identity:** each agent gets per-agent proxy credentials in its
  `HTTPS_PROXY` URL, checked together with the container's internal IP.
  The proxy then applies **that agent's workspace policy**: allowlist,
  injected credentials, logging.
- **Why shared and not per workspace:**
  - one process to run, update and monitor;
  - one connection log for the network inspector;
  - policy changes apply live without restarting containers;
  - no extra container per workspace.

  Per-workspace sidecars only isolate better between mutually untrusted
  tenants. Revisit that when multiple users arrive (§5.4).
- **Model providers:** local providers (Ollama on the host) are reached
  through the proxy as an allowed host like any other. Remote provider
  keys use proxy injection, so they're never in the container.

## 9. Roadmap

| Phase | Ships |
|---|---|
| **W1 · Foundation** | Design system and shell; `viewmodel` move and the `_marshal/stack` stream; transcript rendering with browse keys; Home inbox; owner and origin fields in the data model |
| **W2 · Session & ship** | Session dock (all four states) with Inspect, Changes and Files; Review & ship (PR style + by step); New agent (prompt first) |
| **W3 · Runs & control** | Runs (lanes, graph, timeline); Library; Models & routing; Usage & budgets; Live wall; Watch monitors |
| **W4 · Workspaces** | Gallery, layer builder + source, build pipeline, mounts, secrets vault, egress proxy, warm pools; Network inspector; Projects 2.0 |
| **W5 · Automations & ops** | Terminal and Preview tabs; PR review bot; CI fixer; schedules and recipes; notifications; shared memory scopes; status page |
| **Later** | Multiple users (accounts, sign-in, roles, team memory) |

## 10. Open questions

Answered on 2026-10-03 and recorded above:
- terminal pauses the agent (§8.5);
- `Depends on:` lines (§8.7);
- secrets approach (§8.15);
- shared egress proxy (§8.15);
- specs live under `docs/<feature>/`, with AGENTS.md updated.

Still open:

1. **Stack stream volume.** Is one SSE stream per session enough for the
   live wall with 12+ agents, or should the bridge send a reduced "last 3
   headlines" feed? (§8.3 suggests the reduced feed.)
2. **Workspace templates in the repo or the bridge.** The draft says in the
   repo (`.marshal/workspaces/`), where they're reviewed and trust-hashed.
   Should the bridge also hold user-level templates shared across repos?
3. **First external secret manager to support.** OpenBao / HashiCorp Vault
   (HTTP, most capable) or 1Password (`op` CLI, most common on
   developers' machines)?
4. **TLS termination for injected credentials.** Acceptable for the hosts a
   workspace lists for injection, or should injection be limited to plain
   reverse-proxy endpoints (`http://github.internal`) that avoid a CA
   inside the container?
