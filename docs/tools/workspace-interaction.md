<!-- tools: workspace.worktree, workspace.finish, question.ask, mode.request, tools.select, web.fetch, web.search, config.read -->

# Workspace and interaction

Eight tools cover isolation, merging, talking to the user, and reaching outside
the workspace.

## workspace.worktree

**For:** isolating the session in a git worktree for a branch, or returning to
the project root.

**Surface** (`internal/tools/native/workspace.go:21`), risk `workspace_write`:

| Arg | Required | Notes |
|---|---|---|
| `branch` | no | omit (or pass `{}`) to return to the project root |

Configured setup hooks run automatically in fresh worktrees, sandboxed, from
trusted project config.

**Failure modes:**

- `workspace.worktree requires a session` (`workspace.go:37`)
- `worktree: branch %s already exists but has no worktree at %s; delete the
  branch or remove it by hand before resuming` (`internal/worktree/worktree.go:68`)

The second is the branch precondition the description now warns about: a branch
that exists without a worktree is refused rather than silently reused. Choose a
branch that is not already present.

There is deliberately no removal path (`workspace.go:20`) — the tool covers
mechanics, and the `using-worktrees` skill covers judgment.

## workspace.finish

**For:** merging the session's worktree branch into the base branch, then
removing the worktree and deleting the branch.

**Surface** (`internal/tools/native/workspace_finish.go:27`), risk
`workspace_write`:

| Arg | Required | Notes |
|---|---|---|
| `message` | no | commit worktree changes first, or pass this to commit them |
| `squash` | no | default true; `false` for `--no-ff` |
| `target` | no | |

**Guards:** it refuses when the project checkout is dirty or its HEAD moved.

**A refusal is content, not an error** (`workspace_finish.go:25-26`) — the agent
must read it and act, not treat it as a tool malfunction. That is why the
description says to commit worktree changes first.

**Failure mode:** `workspace.finish requires a session` (`workspace_finish.go:43`).

Use this instead of `shell.run git merge`, and pop or drop any stash you created
before calling it. See `git.md` for the merge-hygiene note and why it fires too
late to help.

## question.ask

**For:** asking the user a question when the request is ambiguous or a decision
would materially change the outcome.

**Surface** (`internal/tools/native/question.go:23`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `questions` | yes | array of `{question, options?, multi?}` |

Options are strings or `{label, description}` objects. The handler blocks on the
user's response channel.

**Failure mode:** `at least one question is required` (`question.go:36`).

**Why there is a hand-rolled validator.** `ValidateQuestionAsk`
(`question.go:100`) exists because the generic schema check would report
`arguments are not valid JSON` — a message that names nothing the model can fix.
This validator names the exact field and embeds a valid example.

The two dispatch paths must agree: a single call goes through this validator,
while the same tool inside an `actions[]` batch goes through
`registry.ValidateArgs` against the schema (`question.go:95-97`). Tolerating a
stray key in one path would make the identical payload succeed as a single call
and fail in a batch.

## mode.request

**For:** asking the user for missing editing capability, system access, or both, for the current conversation.

**Surface:** native tool, risk `read_only`. Read-only classification does not auto-approve an access request, including in Auto mode.

| Arg | Required | Notes |
|---|---|---|
| `mode` | conditional | Editing hint: `edit`, `copilot` or `auto`. The user chooses the actual editing variant. |
| `system` | conditional | `true` requests system access. Defaults false. |
| `reason` | no | Plain-language purpose, at most 400 Unicode characters. |

Supply a valid mode or `system:true`. An explicitly supplied empty/unknown mode is invalid. For system access alone, use `{"system":true,"reason":"Read the requested files outside the workspace."}`.

The request displays only missing capabilities. Already in Edit/Copilot/Auto, a mode hint does not switch variants; a system-only grant preserves the mode, including Plan/Default. Already-satisfied requests return without a prompt.

The app runtime validates and applies the session grant before the canonical tool waiter receives a typed result. The TUI and ACP collect decisions; they do not persist grants or rebuild the runtime. Results carry the actual effective mode/system state and distinguish grant, denial, failure, cancellation and already-available access. Ordinary Edited command text is not an internal access grant.

System access permits filesystem scope outside the workspace and relaxes workspace shell guardrails, subject to interaction mode, per-tool policy and sandbox. It does not grant sudo. New/resumed conversations start without the grant; unrelated in-conversation config reload preserves a temporary mode override, while an explicit configured-mode change replaces it.

Only the top-level conversation can request escalation. A child must ask its parent. Denial preserves capabilities already held; cancel/shutdown and stale responses cannot grant a later request.

## tools.select

**For:** loading deferred tools so they become callable for the rest of the
session.

**Surface** (`internal/tools/native/tools_select.go:23`), risk `read_only`:

| Arg | Required | Notes |
|---|---|---|
| `names` | yes | array of tool names |

Unknown or non-deferred names are rejected. See `README.md` for what deferral
costs and why it exists.

**Failure modes:** `tool registry not available` (`tools_select.go:34`),
`session state not available` (`tools_select.go:37`).

## web.fetch and web.search

**For:** fetching a URL, and searching the web.

**Surface:** `web.fetch` (`internal/tools/native/web.go:42`) and `web.search`
(`web.go:121`), both risk `network`.

| Tool | Args |
|---|---|
| `web.fetch` | `url` (required) |
| `web.search` | `query` (required) |

**Registration:** `web.fetch` whenever `[web] enabled` — default true
(`internal/app/config/defaults.go:129-132`). `web.search` additionally requires
`search_url` (`native.go:254-258`).

**Failure modes:**

- `web tools are disabled` (`web.go:55`, `web.go:134`)
- `only http(s) URLs are allowed` (`web.go:63`)
- `URL resolves to a private or link-local address` (`web.go:66`)
- `web search provider is not configured` (`web.go:137`)

**The SSRF posture.** Private, loopback, and link-local addresses are refused
(`isPrivateURL`, `web.go:192`); redirects are re-checked against the same rule
and capped at 5 (`web.go:81-89`); and the connection is pinned to the first
resolved IP (`makeSafeDialContext`, `web.go:228`) so a DNS rebind between the
check and the dial cannot reach an internal address.

**Unattended children never receive these tools.** `WithoutNetwork`
(`internal/tools/registry/view.go:31`) strips every `network`-risk tool for the
SDD pipeline's role runners, which evaluate under an auto-approving policy clone
with no user watching — a network tool's confirmation would be silently
downgraded to allow. It also keeps `web.fetch`, an in-process HTTP client, from
bypassing the shell sandbox's network isolation.

## config.read

**For:** reading the merged project + global configuration.

**Surface** (`internal/tools/native/config_read.go:12`), risk `read_only`,
`Deferred: true` (`config_read.go:39`), `Cacheable: true` (`config_read.go:18`):

| Arg | Required | Notes |
|---|---|---|
| `sections` | no | top-level section names to include, e.g. `["agent","swarm"]`; omit for all |

Secret fields (`api_key`, `search_key`) are masked to `"***"`.

**Settings are changed through `/settings`, `/models`, or `/connect` — not
through a tool.** `config.read` is read-only by design: a model that could write
config could grant itself providers or permissions.

## Call sequence

Isolate: `workspace.worktree` with a branch → work → `workspace.finish` to merge
and clean up. Ask: `question.ask` when a decision is genuinely the user's, or
`mode.request` when the work needs a mode the session does not have. Reach
outside: `tools.select` to load `config.read` or `web.*`, then call it.
