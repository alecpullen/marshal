# Fleet CLI — design

A terminal client for a `webbridge` fleet. It runs on any machine (a laptop,
the homelab box itself over SSH) and connects to a bridge on the LAN or
tailnet. Agents keep running on the server when no client is attached.

Status: design approved 2026-10-13; implementation plan not yet written.
Partly superseded on 2026-11-12. Daemon P5
([`docs/daemon/p5-fleet-screen.md`](../daemon/p5-fleet-screen.md)) builds this
loop into the Go TUI instead of an Ink client.

- **Carries over:** §1 (the loop), and §3 to §5 (screens, keys, notifications,
  connection), with the differences that spec lists.
- **Does not carry over:** §2 (the Ink client, the reused TypeScript modules,
  the `web/ui` seams) and §6's vitest and Ink tests. P5's server profiles
  replace §2.3.
- **No longer holds:** §1's non-goal of no change to the bridge's HTTP
  contract. P5 adds an inbox view and an agent attach (design §4.4).

## 1. Goal and scope

**Outcome.** Run the core fleet loop from a terminal: see every agent and what
needs you, spawn an agent, attach to one to watch, prompt and steer it, answer
its approvals and questions, review its diff, and land or discard its work —
then detach and leave it running.

**In v1**

- Agents table with live status, grouped by urgency.
- Needs-you queue: agent approvals and questions, plus intake submissions
  (MCP- or issue-originated spawns awaiting confirmation).
- Spawn form.
- Attach: live transcript, prompt, steer, cancel, mode switch.
- Approvals (inline) and questions (modal).
- Diff view; finish flow (exit with build+test gate and override, merge,
  discard).
- Desktop notifications while the client runs.
- Named server profiles with a switcher.

**Out of v1:** shelling into an agent container, workspace/template
management, credentials, secrets, network egress decisions, automations,
review drafts, budgets editing. These stay in the web UI.

**Non-goals.** No changes to `webbridge`'s HTTP contract. No change to the
`marshal` binary or the bridge image. Attaching never holds the agent
(`session/hold` is not used); detaching is closing a stream.

## 2. Architecture

```
 laptop / any host                         homelab
┌──────────────────────────┐   HTTPS+SSE  ┌─────────────────────────────┐
│ marshal-fleet (Node/Ink) │ ───────────▶ │ webbridge                   │
│  ├─ Ink components       │  bearer tok  │  ├─ fleet registry, events  │
│  ├─ useStore adapter     │              │  └─ ACP ─▶ marshal acp      │
│  └─ web/ui/src/lib/*.ts  │              │           (one per agent)   │
└──────────────────────────┘              └─────────────────────────────┘
```

**Server.** `webbridge` unchanged. The loop uses only existing routes:
`/api/agents`, `/api/projects`, `/api/events` (`?stream=fleet` and
`?sessionId=`), `/api/sessions/{id}/{prompt,steer,cancel,mode,stack,nodes/…}`,
`/api/permissions/{id}`, `/api/questions/{id}`, `/api/pending…`,
`/api/agents/{id}/{diff,commit-draft,verify,gate,exit,merge,discard}`,
`/api/budgets`.

**Client.** A new npm package at `web/cli/` (working name `marshal-fleet`):
Node ≥ 20, Ink + React, bundled with `tsup` to one JS file run by `node`.
It lives in this repo so it can import `web/ui`'s logic modules directly;
it ships separately from `marshal` and `webbridge`.

### 2.1 Reused modules

Imported from `web/ui/src/lib/` by relative path (the bundler inlines them):

| Module | Used for |
|---|---|
| `api.ts` | every request function and contract type (`listAgents`, `spawnAgent`, `promptSession`, `steerSession`, `cancelSession`, `setMode`, `resolvePermission`, `resolveQuestion`, `listPending`/`approvePending`/`denyPending`, `getDiff`, `getCommitDraft`, `runGate`, `exitAgent`, `mergeAgent`, `discardAgent`, `getNode`, `getBudgets`) |
| `sse.ts` | `connectSSE` (session stream, `Last-Event-ID` resume) and `connectFleetSSE` |
| `stack.ts` | `createStackStore`: transcript snapshot plus live patches, overflow refetch |
| `store.ts` | `createSessionStore`: pending permission/question, mode |
| `fleet.ts` | `toRow`, `applyDeltaTo`, `groupAgents`, urgency ordering |
| `inbox.ts` | `buildInbox`: needs-you merge of agents and intake |
| `diff.ts`, `exit.ts` | diff parsing, finish-flow helpers |

These depend on `svelte/store`, which is plain JavaScript and runs in Node
without the Svelte compiler; `svelte` becomes a runtime dependency of the CLI.
A `useStore(store)` hook (`useSyncExternalStore` over `subscribe`) binds them
to React. Contract drift in the bridge breaks `tsc` in both clients.

### 2.2 Seams added to `web/ui`

Browser behaviour must be byte-for-byte unchanged; `web/ui`'s tests must pass.

1. **Base URL and token.** `api.ts` gains
   `configureClient({ baseUrl, token })`. `request()` and `sse.ts` prefix
   `baseUrl` (default `''`) to every path. A configured token is returned by
   `getToken()` ahead of `sessionStorage`.
2. **No prompt.** `ensureToken()`/`requestToken()` never call
   `window.prompt` when a token is configured, and guard `typeof window`.
3. **Event-ID storage.** `sse.ts` already wraps `sessionStorage` in
   try/catch. The CLI installs a `Map`-backed `globalThis.sessionStorage`
   before importing, so resume works within a run. Resume across process
   restarts is not needed: a fresh attach takes a snapshot.

### 2.3 Server profiles

`~/.config/marshal/fleet.toml` (honours `MARSHAL_CONFIG_DIR` /
`XDG_CONFIG_HOME`). The CLI refuses to read it if it is group- or
world-readable, and writes it `0600`.

```toml
default = "homelab"

[servers.homelab]
url = "https://homelab.tailnet:7700"
token_cmd = "op read op://infra/marshal-bridge/token"
notify = true

[servers.local]
url = "http://127.0.0.1:7700"
token = "…"
notify = false
```

Exactly one of `token` / `token_cmd` per profile. `token_cmd` runs once per
connect via `/bin/sh -c`; its trimmed stdout is the token. One profile is
active at a time: switching (`s`) aborts every stream, clears stores, calls
`configureClient` with the new profile, and reconnects. This keeps
`api.ts`'s module-level state correct without making shared code
multi-tenant.

`marshal-fleet [--server NAME]` picks a profile; with no config file it
prints a starter config and exits non-zero.

## 3. Screens

One screen, persistent panels (lazygit model). Left column: Agents, Needs
you, Events. Right: a tabbed detail panel for the selected agent. `z` zooms
the focused panel to full screen.

```
┌ Agents ───────────────── 5 ┐┌[Transcript] Diff  Info ───────┐
│ ST NAME         CTX Δ  AGE││ ▸ Read hooks_test.go          │
│ ⚠  fix-flaky…  38% 2   2m││ ▸ Edited hooks_test.go +3 −1 │
│ ●  s3b-egress  41% 7   0s││ ⚠ shell go test ./internal/… │
│ ●  docs-sweep  12% 0   5s││   risk low  [y]es [n]o        │
│ ✓  docs-deploy 22% 4   1h││                               │
│ ✗  ci-fixer     9% 0   3h││                               │
├ Needs you (1) ───────────┤│                               │
│ fix-flaky-hooks approval ││                               │
├ Events ──────────────────┤│                               │
│ 14:02 docs-deploy ready  ││ › _                           │
└──────────────────────────┘└───────────────────────────────┘
 homelab ● [tab]focus [1-3]tab [z]oom [n]ew [f]inish [?]help
```

### 3.1 Agents panel

Rows come from `listAgents` → `toRow`, updated by `applyDeltaTo` on fleet
deltas, ordered by `groupAgents` (needs you, running, ready, earlier).

Columns by terminal width:

| Terminal width | Left column width | Columns |
|---|---|---|
| < 80 × 24 | — | "terminal too small (80×24)" |
| 80–99 | 28 | ST · NAME · CTX · Δ · AGE |
| 100–139 | 40% | + PROJECT · MODE |
| ≥ 140 | 40% | + ACTIVITY · COST · CHECK |

- **ST** glyph is always paired with colour and never colour-only:
  `⚠` pending, `●` running, `✓` ready to land, `✗` error, `·` idle/earlier.
  `NO_COLOR` keeps every state legible.
- **CTX** `contextPct`; **Δ** `changedFiles`; **AGE** since `updatedAt`;
  **COST** per-agent `spentUsd` from `getBudgets` (refetched on `budget`
  deltas); **CHECK** latest gate (`pass` / `fail` / `–`).
- Numbers right-aligned; text truncated with `…`.
- `/` filters by name or project (substring, case-insensitive).

### 3.2 Needs you panel

`buildInbox(agents, pending, mine=true).needsYou`, oldest first. An agent
item shows its pending kind; an intake item shows origin and title.
`⏎` on an agent item selects it and focuses the Transcript composer; on an
intake item it opens approve/deny.

### 3.3 Events panel

A ring of the last 200 lines: fleet transitions (agent became ready, errored,
needs you), action failures, connection changes. Newest at the bottom.

### 3.4 Transcript tab (attach)

Selecting an agent opens `createStackStore(sessionId)` and
`createSessionStore(sessionId, project)`; moving the selection away closes
both (detach). Only the selected agent holds session streams.

- One headline row per step and per tool call, from the wire tree.
  `e` expands the focused row; a truncated node fetches full detail via
  `getNode`. Assistant messages get light markdown (bold, inline code,
  fenced code, lists).
- An agent whose `getStack` returns `'unsupported'` shows "transcript
  unavailable for this agent version" and still offers the composer.
- Composer: `i` or `⏎` focuses it. `⏎` sends `promptSession` when the agent
  is idle and `steerSession` during a turn; the placeholder says which.
  `esc` leaves the composer. `ctrl-x` cancels the turn. `ctrl-o` cycles mode
  via `setMode`.
- **Approval** (`pendingPermission`) renders inline at the tail:
  tool, command or diff summary, risk, `[y]es [n]o`, plus `[e]dit` when a diff
  is present (sends `Decision.edited`). It takes keys whenever the
  Transcript has focus and the composer does not.
- **Question** (`pendingQuestion`) opens a modal: option list
  (single or multi), free-text field when allowed, `[d]ecline`.

### 3.5 Diff tab

File list (`j/k`) above a coloured unified diff (`J/K` scroll) from
`getDiff`, parsed by `diff.ts`. Header shows totals (`2 files +5 −2`).

### 3.6 Info tab

Mode, origin, context bar, cost against per-agent cap, branch → target,
latest check (`[v]erify` runs `runGate`), workspace, PR URL if pushed,
updated age.

### 3.7 Finish modal (`f`)

1. Commit message, pre-filled from `getCommitDraft` (empty on
   `'unsupported'`).
2. `exitAgent`. On `blocked` with a failed `verify`, show the failed command
   and output; offer an override reason (re-calls `exitAgent` with
   `override`) or abort.
3. Result: destination, branch, PR URL.

Merge (`mergeAgent`) and discard (`discardAgent`) are alternatives in the
same modal. Discard requires typing the agent name.

### 3.8 Spawn modal (`n`)

Project (from `listProjects`, available only), name, mode, first prompt
(`recentPrompts` as `ctrl-r` history). Calls `spawnAgent`; a returned
`warning` goes to Events. The new agent is selected.

### 3.9 Keys

| Context | Keys |
|---|---|
| Global | `tab`/`shift-tab` panel · `1-3` tab · `z` zoom · `n` new · `s` server · `/` filter · `?` help · `q` quit |
| Agents | `j/k` move · `⏎` attach (focus composer) · `a` answer pending · `x` cancel turn · `f` finish · `d` diff tab |
| Transcript | `j/k` scroll · `g/G` top/bottom · `e` expand · `i` compose · `esc` leave composer · `ctrl-x` cancel · `ctrl-o` mode |
| Diff | `j/k` file · `J/K` scroll · `f` finish |

`ctrl-c` is left to the terminal: it exits cleanly and restores the screen.
The footer shows only keys actionable in the focused panel; `?` shows all.

## 4. Notifications

While the client runs, it watches fleet deltas and the refetched agent list
for two transitions, per agent:

- **needs you:** an agent gains a `pending` request, or an intake
  submission appears;
- **ready:** an agent goes from `running` to `idle` with `changedFiles > 0`.

Each fires a terminal bell and a desktop notification — `osascript`
(`display notification`) on macOS, `notify-send` on Linux, none elsewhere —
unless the profile sets `notify = false`. Notifications are not suppressed
for the attached agent, because the client cannot tell whether its terminal
is in front. A burst within 2 s for the same agent collapses to one.

## 5. Connection and errors

- **Status dot** in the footer: `●` connected, `◌` reconnecting, `✗` down.
  Both stream kinds reconnect with exponential backoff (1 s → 30 s).
- **Down:** panels keep the last state, dimmed; actions are disabled with a
  status-bar message. Reconnect refetches `listAgents` and `listPending`.
- **`replay_overflow`:** handled by the reused stores (snapshot refetch).
- **401:** stop all streams and show "token rejected for profile
  `<name>`". Never re-prompt; the user edits the profile or switches
  (`s`).
- **Action failure:** status bar for 4 s and a line in Events. `BudgetError`
  uses `budgetMessage`.

## 6. Testing

- `vitest` (as in `web/ui`): `useStore`, the `sessionStorage` shim, profile
  parsing and permission check, `token_cmd`, notification transition
  detection, column selection by width.
- `ink-testing-library` render tests for each panel and modal at 80×24 and
  140×40, including `NO_COLOR`.
- One integration test against a stub HTTP server that serves recorded
  fixtures for `/api/agents`, the fleet and session SSE streams, and records
  POSTs: select → approve → prompt → finish with a failed gate and an
  override.
- `web/ui`: existing `vitest` suite passes after the §2.2 seams, plus tests
  for `configureClient` (prefixing, token precedence) and the
  no-`window` path.

## 7. Open questions

None blocking. Deferred: shell into agent (bridge terminal endpoints exist),
multi-server views (all fleets at once), and per-agent notification muting.
