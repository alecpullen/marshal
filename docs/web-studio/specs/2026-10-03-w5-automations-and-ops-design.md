# W5 · Automations & ops — phase spec

Parent design: [`docs/web-studio/design.md`](../design.md) (§8.5 Terminal/Preview, §8.9 automations, §8.10, §8.12, §8.14; §9 row W5).
Previous phases, assumed complete: W1–W4 (all plans).
Plans, executed in this order:
1. [W5.1 · Engine: hold, memory scopes, preview ports](../plans/2026-10-03-w5-1-engine-plan.md)
2. [W5.2 · Bridge: terminal and preview](../plans/2026-10-03-w5-2-bridge-terminal-preview-plan.md)
3. [W5.3 · Bridge: recipes, schedules, notifications, status links](../plans/2026-10-03-w5-3-bridge-recipes-schedules-plan.md)
4. [W5.4 · Bridge: forge automations (review bot, CI fixer)](../plans/2026-10-03-w5-4-bridge-automations-plan.md)
5. [W5.5 · UI: terminal, preview, recipes, schedules, notifications, memory, status](../plans/2026-10-03-w5-5-ui-ops-plan.md)
6. [W5.6 · UI: automations](../plans/2026-10-03-w5-6-ui-automations-plan.md)

## 1. Summary

W5 is the last planned phase. It finishes the session dock with Terminal
and Preview, and turns the Studio from something you drive into
something that also works on its own:

- recipes, and schedules that run them;
- a PR review bot and a CI fixer, driven by forge webhooks or polling;
- notifications;
- memory shared across projects, with scopes and promotion;
- public, read-only status pages.

Multiple users stay deferred (design §5.4).

## 2. What earlier phases left in place

| From | Used for |
|---|---|
| W1–W2: the stack stream, the dock with disabled Terminal/Preview tabs, W2.1's node detail (the selected command's output for terminal replay), W2.1's review comments (steering into an agent) | Terminal replay, review findings sent to the author agent |
| W3: the control agent, shared `home/data` (one SQLite file shared by all containerized agents), the usage ledger, budgets and per-agent caps, `fleet.json` up to v8, the Library page, watches | Memory scopes rely on the shared DB. Recipes set per-agent caps. Recipes appear in the Library. |
| W4: workspaces (`workspacecfg`, templates, spawn with a workspace), the egress sidecar on `marshal-agents`, the secret provider, the credentials and repos routes, project settings (`intake`), `fleet.json` v10 | Preview through the sidecar, webhook secrets in the provider, automations per project |

## 3. Scope

### In scope

| Area | Deliverables |
|---|---|
| Engine | Session hold (`session/hold`); memory scopes, provenance and promotion; the `[preview]` workspace section; workspace-aware memory retrieval |
| Bridge | Terminal (exec with a PTY through `script`, hold and hand back, recording, audit); preview reverse proxy through the sidecar; recipes; schedules; new origins; outbound notification webhooks; status links and the public page; forge extensions (PRs, reviews, checks, logs); the webhook receiver; the review bot; the CI fixer with its guards |
| UI | Terminal and Preview tabs; "Test shell" in the workspace designer; recipes in the Library and New agent; schedules; browser notifications and settings; memory scopes; status links; the automations pages and inbox drafts; "Request review bot" on Ship |

### Out of scope

- Accounts, roles and SSO (Later).
- Team-shared memory across users (Later).
- Parallel task execution.

## 4. Engine

### 4.1 Session hold — `session/hold`

```
→ { sessionId, on: true|false }
← { held: bool }
```

`session.State` gains:
- `SetHold(on bool)`;
- `Held() bool`;
- `WaitUnheld(ctx) error`, which blocks while held and returns early on
  ctx cancel.

The runner calls `WaitUnheld` at two points:
- the loop top, before the steering drain (`internal/agent/runner.go:1063`);
- the top of `executeToolCall` (`internal/agent/execute.go:182`).

So a held agent finishes the model call it is in, then waits before its
next tool or model call.

While held, the session's `session_telemetry`/activity reports `held`. A
`session/update {kind:"hold", held}` is emitted on every change.

Capability: `holdControl`.

### 4.2 Memory scopes, provenance, promotion

Migration (`internal/db/migrations.go`) adds these columns to `memories`:

| Column | Type | Default |
|---|---|---|
| `scope` | TEXT NOT NULL | `'project'` |
| `scope_key` | TEXT NOT NULL | `''`; the workspace name for the workspace scope |
| `owner_id` | TEXT NOT NULL | `'local'` |
| `learned_agent` | TEXT NOT NULL | `''` |
| `learned_step` | INTEGER NOT NULL | `0` |
| `confirmed_by` | TEXT NOT NULL | `'[]'`; a JSON array of agent labels |

It also adds an index on `(scope, scope_key)`. Existing rows become
project-scoped, with the project they were learned in.

**Retrieval.** `dbMemoryProvider.Memories(projectID)` (`internal/app/app.go:391`)
returns, in this order:
1. the project's project-scoped memories;
2. workspace-scoped memories where `scope_key` equals `$MARSHAL_WORKSPACE`
   (set by the bridge at spawn);
3. global memories.

Stale memories are skipped, as today.

**Promotion suggestions.** A memory is a candidate when another project
holds a memory with the same `content_hash` (the existing column, from
migration v2). Two cases:
- a project memory matching one in another project suggests `global`;
- with a workspace, a match in another project on the same workspace
  suggests `workspace`.

**ACP.** The existing `memory_*` methods (`internal/acp/memory.go`) are
extended, and three are added:

| Method | Change |
|---|---|
| `memory_list` | Gains an optional `scope` filter. Each entry gains `scope`, `scopeKey`, `ownerId`, provenance, and the learned-in project root. |
| `memory_suggestions` (new) | Returns `[{memoryId, matchProjectRoot, suggestedScope}]` |
| `memory_promote` (new) | Takes `{sessionId, id, scope, scopeKey?}`. It moves the row's scope and merges duplicates: rows with the same hash in other projects are deleted, and their agents are appended to `confirmed_by`. |
| `memory_confirm` (new) | Takes `{id, agent}`. It appends to `confirmed_by`. |

**Knowledge extraction** (`internal/knowledge`) records `learned_agent`
(the session's actor label) and `learned_step` when it saves a memory.

### 4.3 Preview ports

`internal/workspacecfg` gains a `[preview]` section,
`ports = [3000, 8080]` (layer 0, side panel). Validation: each port must
be 1–65535 and unique.

## 5. Bridge

### 5.1 Terminal

| Route | Behaviour |
|---|---|
| `POST /api/agents/{id}/terminal` | Opens a terminal. Body: `{cols, rows}`. Returns `{terminalId}`. |
| `GET /api/agents/{id}/terminal/{tid}/events` | SSE output: `{data: base64}` chunks, then `{exit: code}` |
| `POST /api/agents/{id}/terminal/{tid}/input` | Body: `{data: base64}`, at most 64 KiB |
| `POST …/resize` | Body: `{cols, rows}` |
| `DELETE …/{tid}` | Closes the terminal |

**Implementation.** The runtime is called through `commandRunner`'s
streaming sibling:
- **Container mode:**
  `exec -i -w /work -e TERM=xterm-256color <container> script -qfc "stty rows R cols C; exec ${SHELL:-sh}" /dev/null`.
  `script` (util-linux) allocates the PTY inside the container, so the
  bridge needs no PTY library.
- **Process mode:** the same `script` command runs locally, with its
  working directory set to the agent's active root (resolved through
  `session/files` with an empty path, W2.1).
- **Resize** runs `stty -F <pty> rows R cols C` in a separate process (the
  wrapper records its `tty` in `/tmp/.marshal-tty-<id>`), so the kernel
  signals the foreground program with SIGWINCH and nothing is typed into it.

**Hold and hand-back:**
- The first input byte calls `session/hold {on:true}` in the background and
  broadcasts `{kind:"hold", held:true, by:"terminal"}`. Hold calls are
  serialized per terminal. An agent without `hold` (501) is asked once; any
  other failure backs off for 30s. Typing never waits on the call.
- **Hand back** (a button, or closing the terminal) calls `hold {on:false}`.
- The hold also auto-releases after 2 minutes without input.

**Recording and audit.** Output and input are recorded to
`<state>/terminal/<agentId>/<unix>.log` (mode 0600). A recording holds
everything typed or printed, secrets included. Opening and closing are audited
as `terminal_opened` and `terminal_closed`, with byte counts.

**Limits:** two concurrent terminals per agent.

### 5.2 Preview

**Declaring ports.** An agent's workspace declares `[preview].ports`. The
bridge exposes a port only when it is declared.

**Opening a preview.** `POST /api/agents/{id}/preview/{port}` returns
`{url}`.
- The URL is absolute and on the **preview origin**:
  `<scheme>://<host>:<previewPort>/preview/<agentId>/<port>/?t=<token>`. The
  token is random, valid for 12h, and bound to the agent and the port.
- **Separate origin.** A previewed app is whatever the agent runs, so it must
  not share an origin with the Studio: the UI keeps the bridge bearer token
  in `sessionStorage`, and a same-origin page could read it and call `/api`
  (terminals, credentials). The bridge serves previews from a second
  listener (`--preview-addr`, default the `--addr` host on a free port, `off`
  to disable). Another port is another origin. That listener serves only
  `/preview/…`; the API listener answers `/preview/…` with 404. The host and
  scheme of the URL come from the request that issued it (or the public
  URL). With no preview listener, issuing answers 501 `preview_unconfigured`.
- **Per-agent origin.** When the issuing host is `localhost` (or a
  `*.localhost` name, which browsers resolve to loopback), the URL's host is
  `a<hash of agentId>.localhost`, so one agent's preview is a different
  origin from another's and cannot read it. The preview handler answers 404
  unless the request arrived on the agent's own label. Any other host cannot
  be subdivided without wildcard DNS, so previews of all agents share an
  origin there.
- The first request with `t` sets the cookie `mp_<agentId>_<port>`. The
  cookie is HttpOnly and SameSite=Strict, with `Path` set to the preview
  prefix.
- `/preview/…` is outside `/api`, so `bearerAuth` doesn't apply. The
  preview handler checks the cookie or token instead. A request is also
  refused if the workspace no longer declares the port (rechecked every 10s).
- The cookie stays `SameSite=Strict`: the Studio and the preview are the same
  site (cookies ignore ports), so the iframe still sends it.

**Forwarding:**

| Mode | Path |
|---|---|
| Container | The egress sidecar gains a second listener, `:8081`, published to the host as `-p 127.0.0.1:<auto>:8081`. It forwards `/<agentId>/<port>/…` to `<agent IP on marshal-agents>:<port>`, and only for ports in the policy snapshot the bridge streams. The bridge reverse-proxies `/preview/…` to it with `httputil.ReverseProxy`. WebSockets pass through, because `ReverseProxy` supports `Upgrade`. |
| Process | Reverse-proxy to `127.0.0.1:<port>` |

### 5.3 Recipes

A recipe is stored as JSON at `<state>/recipes/<name>.json`, and is
audited when changed:

```json
{ "name": "review-pr", "title": "Review a PR", "description": "…",
  "kind": "prompt" | "sdd" | "swarm",
  "mode": "plan", "workspace": "go-service" | "", "routing": { … } | null,
  "inputs": [ { "name": "pr", "label": "PR number", "required": true } ],
  "prompt": "Review pull request #{{pr}} …",
  "limits": { "maxMinutes": 30, "maxUsd": 2.0 },
  "output": "none" | "review-findings" | "ci-result",
  "builtin": true }
```

- **Rendering** replaces `{{name}}` with each input value, after checking
  for `{{`. It is plain text, not `text/template`.
- **Running a recipe** (`POST /api/recipes/{name}/run {project, inputs, origin?}`)
  spawns an agent with the recipe's mode, workspace and routing, sends the
  rendered prompt (or starts the SDD or swarm run), and applies the
  limits:
  - `maxUsd` becomes a per-agent cap override (W3 budgets, pause);
  - `maxMinutes` becomes a deadline that cancels and pauses the agent.
- **Built-ins** (with `builtin: true`, editable by copying):
  - `review-pr`: plan mode, `output: review-findings`;
  - `fix-ci`: edit mode, `output: ci-result`;
  - `summarize-changes`;
  - `update-deps`.

**Structured outputs.** The agent ends with a fenced JSON block in its
final message. The bridge reads the last `final` node through
`session/stack` (W1) and parses the last ` ```json ` block:

| Output | Shape |
|---|---|
| `review-findings` | `{findings: [{severity: blocking\|should-fix\|nit, path, line, title, body, stepNode?}], summary}` |
| `ci-result` | `{reproduced: bool, command, cause, fixed: bool, notes}` |

A missing or invalid block marks the run "gave up: no structured result".

### 5.4 Schedules

Schedules are stored in `fleet.json` (v11) as `schedules`:

```
{id, name, recipe, project, inputs, cron, enabled, lastRun, lastResult, ownerId}
```

- **Cron format:** five fields with `*`, lists, ranges and steps, in UTC.
  It's parsed by a small standard-library cron parser, with a 1-minute
  minimum.
- **Scheduler:** a goroutine ticking each minute (started next to
  `StartPoller`, `poller.go:20`) runs each due schedule through the
  recipe runner, with origin `schedule`.
- **Overlap:** if the schedule's previous run is still active, the tick
  is skipped and recorded.
- **Routes:** `GET/POST/PUT/DELETE /api/schedules`, plus
  `POST /api/schedules/{id}/run` (run now).

**New origins** in `workspace.go`: `OriginSchedule = "schedule"`,
`OriginReviewBot = "review-bot"`, `OriginCI = "ci"`.

### 5.5 Notifications

- **In-browser:** handled by the UI from fleet deltas (§6.4).
- **Outbound webhooks.** Configured in `fleet.json` as `notifications`:
  `{webhooks: [{id, url, secretRef?, events: [...]}]}`. Event kinds:
  `needs_you`, `run_finished`, `budget`, `automation`, `watch_fired`,
  `network_block`.
  - The bridge POSTs
    `{event, at, agentId?, title, text, url}`. The `url` points at the
    Studio, built from `NewServer`'s `publicURLBase` (`http.go:42`).
  - When `secretRef` is set, the request carries the header
    `X-Marshal-Signature: sha256=<hmac>`.
  - Delivery retries 3 times with backoff and is never blocking.
  - The body's `text` field also suits Slack-compatible incoming webhooks.
- **Routes:** `GET/PUT /api/notifications`, and
  `POST /api/notifications/test`.

### 5.6 Status links and the public page

**Creating and managing links:**
- `POST /api/status-links {agentId, ttlHours (default 168, max 720)}`
  returns `{id, url, expiresAt}`.
- The token is 32 random bytes. It's stored hashed (`HashToken`,
  `client.go:53`) in `fleet.json` `statusLinks` (v11). The URL is
  `/s/<token>`.
- `GET /api/status-links` lists links; `DELETE /api/status-links/{id}`
  revokes one.
- Creating and revoking are audited.

**The public page.** `/s/{token}` and `/s/{token}/data` are outside `/api`
and served without bearer auth, but only after the token is checked
against the stored hashes and the link isn't expired.

`/s/{token}/data` returns only this whitelist, built from `session/stack`
and `session/run`:
- the agent name and project basename;
- status and elapsed time;
- progress (done and total from task nodes, or from the run);
- a task list of index, content and status;
- the current step headline (the live step's `headline` only).

It never includes tool output, file paths beyond the project basename,
narration beyond the headline, the composer, or secrets.

`/s/{token}` is a standalone light-theme HTML page from an embedded
`html/template`. It polls `/data` every 10s.

**Abuse limits:**
- a per-IP rate limit of 60 requests a minute on `/s/`, using an
  in-memory token bucket;
- 404 for unknown or expired tokens, with no distinction between the two.

### 5.7 Forge extensions

The `Forge` interface (`web/bridge/forge.go:47`) gains these methods,
implemented for GitHub and Gitea:

| Method | GitHub | Gitea |
|---|---|---|
| `ListPRs(ctx, repo, PRQuery{State, Since, Label}, cred) ([]PRInfo, error)` | `GET /repos/o/n/pulls?state=open` | `GET /repos/o/n/pulls?state=open` |
| `GetPR(ctx, repo, number, cred) (PRInfo, error)` (head ref and SHA, base, author, draft, labels, URL) | `GET /pulls/N` | `GET /pulls/N` |
| `PostReview(ctx, repo, number, Review{Body, Event:"COMMENT", Comments []ReviewLine{Path, Line, Body}}, cred) (string, error)` | `POST /pulls/N/reviews` | `POST /pulls/N/reviews` |
| `ListFailedChecks(ctx, repo, ref, cred) ([]CheckInfo, error)` | `GET /commits/{ref}/check-runs` with `conclusion=failure` | `GET /commits/{ref}/statuses` (failures) |
| `CheckLog(ctx, repo, CheckInfo, cred) (string, error)` (tail 256 KiB) | `GET /actions/jobs/{id}/logs` when the check is an Actions job; otherwise the check's `output.text` | The status `target_url` is not fetchable generically, so it returns the description and a link |

Fakes in tests extend the existing forge test servers
(`testFleetWithIssueForge`, `issues_test.go:13`).

### 5.8 Webhooks in

`POST /hooks/forge/{repoId}` is a non-`/api` path, so it's routed before
the static handler.

- **Verification.** The body (at most 1 MiB) is checked against the
  repo's webhook secret, which lives in the provider at
  `vault:hooks/<repoId>`:

  | Forge | Header |
  |---|---|
  | GitHub | `X-Hub-Signature-256`, HMAC-SHA256 |
  | Gitea | `X-Gitea-Signature`, hex HMAC-SHA256 |

  An invalid signature returns 401.
- **Events handled:**

  | Forge | Events |
  |---|---|
  | GitHub | `pull_request` (opened, synchronize, reopened, ready_for_review), `check_suite`/`check_run` (completed with failure), `workflow_run` (completed failure) |
  | Gitea | `pull_request`, `status` (failure) |

  Others get 204.
- **Setup:** `POST /api/repos/{id}/webhook-secret` generates and stores
  a secret, and returns it once for pasting into the forge.
- **Polling fallback:** the existing poller (`poller.go`) also polls PRs
  (with `ListPRs`) and failed checks for repos whose automations are on
  and have no webhook secret.

### 5.9 Review bot

**Settings** live per project in `projectSettings.automations.reviewBot`
(v11):

```
{enabled, repoId, labels?, skipDrafts, authors?, routing?, autoPost: bool, holdSeverities: [...]}
```

**Run:**
1. A PR event (or poll) matches the settings.
2. The bot de-duplicates by `(repo, number, headSHA)`.
3. It runs `review-pr` with origin `review-bot` on a read-only agent: a
   git-sourced spawn at `ref = head SHA`, plan mode, and the project's
   default workspace.

**On completion:**
1. Parse `review-findings`, and store a draft at
   `<state>/automations/review/<id>.json`:
   `{id, repoId, number, headSHA, agentId, findings, summary, status: draft|posted|discarded, createdAt}`.
2. Append the fleet delta `{kind:"automation", type:"review_draft", id}`
   and send a notification.
3. With `autoPost`, and no finding in `holdSeverities`, post immediately.

**Actions:**

| Action | Effect |
|---|---|
| **Post** | `PostReview`: the summary as the body; findings with a path and line as inline comments; the rest folded into the body |
| **Edit** | Change findings before posting |
| **Discard** | Discard the draft |
| **Send to author agent** | Shown when the PR's head branch matches a Marshal agent's `Branch`. Sends each selected finding through W2.1's review-comment route to that agent. |

Each action is audited (`review_posted` and so on).

### 5.10 CI fixer

**Settings:** `projectSettings.automations.ciFixer`, holding:
- `enabled`, `repoId`, `branches: [...]`;
- `maxMinutes`, `maxUsd`;
- `push: false` (a PR, the default) or `true` (push to the branch, only
  for branches matching `pushBranches`).

**Run:**
1. A failed check on a watched branch triggers it, de-duplicated by
   `(repo, sha, check)`.
2. Fetch the log with `CheckLog`.
3. Run `fix-ci` with origin `ci`:
   - a git-sourced spawn at the failing SHA, edit mode, isolated;
   - the prompt includes the check name, the failing command when it can
     be inferred, and the log tail;
   - limits from the settings.

**Guards (bridge-side, deterministic):**
1. **Reproduce first.** The `ci-result` must have `reproduced: true`.
   Otherwise the history entry is "didn't reproduce" and the agent is
   discarded.
2. **No test tampering.** Before shipping, the bridge reads the agent's
   diff (`session/diff`) and rejects the run as "gave up: forbidden
   change" when the diff matches any of these:
   - deletes a file matching `*_test.go`, `test_*.py`, `*_test.py`,
     `*.test.*` or `*.spec.*`, or any file under `tests?/`;
   - adds a line matching any of `t.Skip(`, `@pytest.mark.skip`,
     `@unittest.skip`, `it.skip(`, `describe.skip(`, `xit(`,
     `test.skip(`, `.only(`, `#[ignore]`;
   - edits CI config to disable a job (`if: false`,
     `continue-on-error: true` added under `.github/workflows/` or
     `.gitea/workflows/`).
3. **Gate.** Ship through the W2 exit path. The gate must pass, and
   overrides are never used by automation.

**History:** `<state>/automations/ci/<id>.json`, with status `fixed`,
`didn't reproduce` or `gave up`, the reason, the PR URL, and the cost
from the usage ledger.

## 6. Web UI

### 6.1 Terminal and Preview tabs (dock)

**Terminal:**
- In select mode on a shell row, the top pane replays that command's
  output, from W2.1 node detail.
- Below it, **Open shell** starts a terminal. The renderer is
  `@xterm/xterm` with `@xterm/addon-fit`. The standard-library-only rule
  covers the Go bridge, not the SPA, which already depends on npm
  packages. Its theme uses the Warm Sunset tokens, and resize sends the
  fitted `cols` and `rows`.
- While held, the strip shows "agent paused · Hand back", with the
  2-minute auto-release countdown.
- `y` copies the selection.

**Preview:**
- a port list from the workspace;
- **Open** shows an `<iframe>` of the preview URL, with reload, open in a
  new tab, and copy link.

The designer's "Test shell" opens a terminal in a one-off container of
the template image. The bridge route
`POST /api/workspaces/{name}/shell` starts `run --rm -it`-style through
the same terminal machinery, and stops it on close.

### 6.2 Recipes and schedules

- **Library → Recipes:**
  - a list (built-in badge);
  - an editor (JSON form fields with a prompt textarea, inputs and
    limits);
  - **Run** (pick a project, fill the inputs).
- **New agent → Recipes tab:** picking a recipe fills the mode,
  workspace and routing chips and the prompt, and shows its input fields.
- **Schedules** (`#schedules`, under Watches on the rail as a tab):
  - a table of name, recipe, project, cron (with a human-readable
    rendering), next run, last result and enabled;
  - a form with a cron helper (presets: hourly, daily 09:00, weekdays).

### 6.3 Automations (`#projects/<root>/automations/review-bot|ci-fixer`)

**Review bot:**
- settings;
- recent drafts with severity chips;
- a draft view: findings grouped by severity, each with `path:line`, the
  body, and an evidence link to the step (`#chat/<agentId>?node=…`);
- Post, Edit, Discard, and Send to author agent.

Drafts also appear in the Home inbox's Ready to ship group, as "Review
draft for PR #N".

**CI fixer:** settings, plus a history table of SHA, check, status,
reason, PR and cost.

**Ship panel:** "Request review bot" is enabled when the project's review
bot is enabled. After the push, it runs the bot on the new PR.

### 6.4 Notifications

**Browser notifications:**
- A **Settings → Notifications** page asks for permission and offers
  per-event toggles, stored under `marshal.ui.notify` and keyed so a
  future user record can take them over.
- Notifications fire on fleet deltas while the tab isn't visible:
  - needs you;
  - run finished;
  - budget;
  - automation drafts and results;
  - watch fired;
  - network block.

**Outbound webhooks:** a list, plus an add form (URL, secret ref, events)
with a **Test** button.

### 6.5 Memory scopes

The Library → Memory tab gains:
- a scope filter (Project, Workspace, Global);
- provenance columns (learned in, by agent and step, linking to the step
  when that agent still exists; confirmed by);
- a **Suggestions** panel: "Also learned in <project> — promote to
  global?", with Approve, which calls promote, and Dismiss, which is kept
  per browser.

### 6.6 Status links

- **Session header** gets a **Share status** button: TTL select, create,
  copy the link.
- **Run page** gets the same button.
- **Settings → Status links:** a list, with revoke.

## 7. Testing

| Layer | Tests |
|---|---|
| engine | Hold blocks at the loop top and before a tool call, and releases. The `hold` update. The memory migration from existing rows. Retrieval order (project, workspace, global). Suggestions, promote with merge, confirm. Knowledge records provenance. `[preview]` validation. |
| bridge | Terminal: the exec args, the hold on first input and its release (timer), recording, the audit entry, the limits. Preview: token, cookie and path checks, the sidecar forward (fake), process mode. Recipes: rendering, running each kind, limits mapped to the cap and deadline, structured-output parsing. The cron parser (table tests), the scheduler with an injected clock, overlap skipping. Webhook signature verification for both forges. Forge extensions against fake servers. The review bot: de-dupe, a draft, auto-post rules, send to author. The CI fixer: not reproduced, forbidden-change patterns (each one), success to a PR. Status links: whitelist fields only, expiry, revoke, rate limit, and the HTML template. Notifications: webhook signing, retries. `TestWebIsStdlibOnly`. |
| UI | The terminal component, with a mocked `@xterm/xterm`: input is base64-posted, output is written. The terminal hold strip. Preview iframe URL. Recipe forms and the New agent integration. The cron helper rendering. Automations drafts and actions. The notification permission flow (mock `Notification`). Memory suggestions. Status link creation. |

## 8. Acceptance criteria

1. Typing in the Terminal tab pauses the agent before its next action.
   **Hand back**, or 2 idle minutes, resumes it. The session is recorded
   and audited.
2. A dev server on a declared port opens in the Preview tab and is
   unreachable for undeclared ports.
3. A scheduled `summarize-changes` recipe runs daily with origin
   `schedule` and shows in the inbox.
4. Opening a PR on a watched repo produces a review draft with
   severity-labelled findings and step evidence. Post publishes it on the
   forge.
5. A failing CI check produces a CI-fixer run. A fix that skips a test is
   rejected as a forbidden change. A real fix opens a PR with a passing
   gate.
6. A status link shows only progress, tasks and the current headline,
   expires, and can be revoked.
7. A memory learned in two projects is suggested for promotion, and once
   promoted, it is available to agents in a third project.
8. All suites pass as in earlier phases.
