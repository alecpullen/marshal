# Tool architecture

This directory documents the model-facing tool surface, grouped by the job each
tool does. Every family file opens with a machine-readable index line — an HTML
comment naming the tools it covers — that `TestToolsDocumentationDrift`
(`internal/app/tools_doc_test.go`) checks against the live registry, so the docs
cannot silently drift away from the tools that actually exist. This file
documents how the surface is assembled rather than a family of tools, so it
carries no index line.

## How the surface is assembled

Tools are registered in three places, in this order.

**1. `native.RegisterAll`** (`internal/tools/native/native.go:204`) registers the
base set in a fixed order:

```
file.read, file.write_patch, file.edit, file.write, repo.search,
git.status, git.diff, git.log, git.blame, shell.run, test.run,
todo.write, question.ask, mode.request, tools.select,
workspace.worktree, workspace.finish
```

**2. Conditional additions inside `RegisterAll`.** These register only when the
runtime can actually serve them — offering a tool that can only answer
"unavailable" teaches the model a dead end:

| Tool | Condition | Anchor |
|---|---|---|
| `codebase_search` | an embedding route resolves | `native.go:230-233` |
| `shell.sudo` | platform-gated: non-Windows only | `sudo_register.go`, `sudo_register_windows.go` |
| `symbols` | a symbol querier is wired | `native.go:238-241` |
| `transcript_read` | session state exists | `native.go:243-248` |
| `config.read` | always | `native.go:253` |
| `web.fetch` | `[web] enabled` (default true) | `native.go:254-258` |
| `web.search` | additionally requires `search_url` | `native.go:254-258` |

**3. App-level additions**, after the policy engine exists — `native` cannot
reach the engine, so these are registered by `app.Run`:

- `agent.run`, `agent.await`, `agent.kill`, `agent.steer`
  (`internal/app/app.go:834` and the three registrations that follow).
- `skill.load` (`internal/skills/tool.go:20`, called from `app.go:799`).

Two further surfaces register outside `native` entirely and are documented in
their own packages, not here: the desktop `browser.*` tools when
`[desktop] enabled` (`internal/tools/desktop/tools.go:108`), and MCP tools when
a server is configured (`internal/tools/mcp/manager.go:339`).

## Registration flags

`registry.Tool` (`internal/tools/registry/types.go:32-49`) carries four flags
that decide how a tool reaches the model:

| Flag | Meaning |
|---|---|
| `Risk` | the approval class; see below |
| `Cacheable` | whether the result may be reused across identical calls |
| `Deferred` | hidden until `tools.select` loads it |
| `Internal` | dispatchable by the runtime but **never offered to the model** — no tool definition, no prompt listing, no deferred announcement |

`Internal` is the important one to understand when reading these docs. An
internal tool still exists and still runs; it is simply not part of the surface
the model chooses from. `file.write_patch` and `test.run` are internal: the
first is reached only by envelope-mode patch actions, the second only by the
swarm tester's role view. Both are documented here because the runtime reaches
them, but neither appears in a model's tool list.

## Risk levels

Six levels (`internal/tools/registry/types.go:11-20`), each gating a different
kind of action:

| Level | Gates |
|---|---|
| `read_only` | nothing is mutated; safe to auto-approve |
| `workspace_write` | writes inside the workspace (files, worktrees, todos) |
| `command` | arbitrary shell execution |
| `network` | outbound requests |
| `destructive` | privileged or irreversible operations |
| `environment` | mutates state **outside** the working directory (build caches, global config, system package managers) — always requires explicit approval, even in auto-approve modes, because those modes own the workspace, not the machine |

## tools.select

`tools.select` (`internal/tools/native/tools_select.go:23`) loads named deferred
tools for the rest of the session. Deferred tools are the low-use ones —
`config.read`, `shell.sudo`, and MCP tools above the disclosure threshold — kept
out of the prompt until asked for. Unknown or non-deferred names are rejected.

Loading a deferred tool costs one prompt-cache miss, because tool definitions
head every provider's prefix. That is the trade the deferral makes: a smaller
always-on prefix in exchange for a one-time miss when a tool is actually needed.

## The drift guard

`TestToolsDocumentationDrift` (`internal/app/tools_doc_test.go`) keeps this
directory and the live registry in step. It parses only the index line at the
top of each family file, and asserts three things:

1. **Registered → documented.** Every registered, non-internal tool appears in
   some family index.
2. **Indexed → registered.** Every indexed name is registered, or listed in
   `docsOnlyTools` with a reason (currently only `shell.sudo`, which does not
   register on Windows).
3. **Retired names absent.** No retired tool name appears anywhere in this
   directory. A name that is still registered is not retired in the registry
   sense and may be documented — `file.write_patch` is internal, not removed,
   and `editing.md` documents it deliberately.

Only the index line is parsed, not the prose. These docs necessarily name source
files and error strings, and a name-shaped match over prose would flag those as
unregistered tools.

If assertion 1 fails, add the tool to the right family index — do not widen
`docsOnlyTools`. If assertion 2 fails, the index line has a typo or names a tool
that no longer exists. If assertion 3 fires, a doc quotes a retired name;
rewrite the prose.
