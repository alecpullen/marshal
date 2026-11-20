<!-- tools: agent.run, agent.await, agent.kill, agent.steer, agent.integrate, agent.discard, skill.load -->

# Delegation

Seven tools hand work to something else: six manage child agents, their owned
worktrees and background jobs, one loads an instruction set into the current
context.

## agent.run

**For:** delegating a scoped subtask to a fresh child agent.

**Surface** (`internal/agent/subagent.go:211`), risk `workspace_write`:

| Arg | Required | Notes |
|---|---|---|
| `prompt` | yes | the subtask, passed verbatim |
| `description` | yes | short label |
| `agent` | no | a **configured custom agent** — not a role binding |
| `model` | no | provider/model pair |
| `temperature` | no | sampling temperature override |
| `thinking` | no | reasoning effort override (low/medium/high) |
| `system` | no | grant system access; the parent must already have it |
| `workspace` | no | `"isolated"` (default when omitted) or `"shared"`; empty or unknown is refused |
| `base_commit` | no | isolated only: an exact existing commit the child starts from |

**The workspace contract.** Omitted `workspace` means `"isolated"`: the child
gets its own branch (`marshal/subagent/<task key>`) and worktree, created from
the parent's **committed** HEAD, and it commits its own scoped work there. The
parent therefore commits whatever the child needs — a new interface, a plan, a
fixture — before dispatching; uncommitted work is not copied and unrelated dirt
is neither staged nor committed. `"workspace": "shared"` binds the child to the
parent's captured checkout as it is: it edits the parent's working tree and
commits nothing, so its commits are the parent's.

An explicit empty or unknown `workspace` is refused rather than read as the
default, and `base_commit` with shared mode is refused because a shared child
does not check anything out. Isolated dispatch on a backend that cannot host a
linked git worktree (the container backend) or in a non-git directory returns an
actionable refusal — never a silent fall back to shared mode. All of these are
decided before a slot, a worktree or a provider request is spent.

**Identity and retention.** Admission allocates a stable task key from the
execution origin (turn, step and tool call — never the prompt text and never a
model call id alone) before any workspace exists, so a retried call observes the
record it already owns instead of starting a second child and a second branch.
The receipt, the card and the completion report all name that key together with
the mode, canonical worktree path, branch, starting commit and captured
integration target. A retained record outlives its child, so nothing has to be
reconstructed from the brief.

**Integrating a candidate is the parent's job.** An isolated child's branch is a
retained candidate: its report names the branch and the candidate commit, and
the parent brings it in explicitly with git (`git merge <branch>` or
`git cherry-pick <candidate>`). Nothing is merged automatically, and no
lifecycle tool exists yet for integrating or discarding a candidate — the
branch stays on disk for the parent or the user to keep or drop.

**Setup hooks run before the model.** Configured worktree setup hooks execute
inside the child's own worktree under its bounded authority, through the same
prepared-facts/admission path as an ordinary `shell.run`. A refused or failed
required hook fails the dispatch: no model tokens are spent and the worktree and
branch are retained as evidence.

**The async contract.** `agent.run` returns a handle immediately and the report
arrives later as a `[subagent N finished]` message. A model that assumes a
synchronous result will hallucinate completions — it will describe work the
child has not done yet. Do not treat the task as done until the report arrives;
use `agent.await` to wait or peek.

The concurrency cap is read from the session's configuration
(`state.SubagentMaxConcurrency()`, `subagent.go:209`) rather than hardcoded, so
the description always states the real number. Writes across agents are
serialized on one shared gate.

**The `"agent"` argument.** It names a configured custom agent. It is **not** a
role binding from the agent profiles table — passing a role name such as
`implementer` fails with `routing: custom agent not found`
(`internal/llm/routing/router.go:28`). This was the corpus's clearest
`agent.run` failure: five calls across five reports passed a role name.

**Failure modes:**

- `agent.run: system access requested but this session does not have it` (`subagent.go:236`)
- `routing: custom agent not found` (`router.go:28`)
- `agent.run: unknown workspace "…"; use "isolated" or "shared"`
- `agent.run: "workspace" must be "isolated" or "shared"; omit it for the default (isolated)`
- `agent.run: "base_commit" is only meaningful for an isolated workspace; …`
- `agent.run: isolated worktrees are unavailable on this execution backend; …`

**The child's tools.** A child has the parent's tools except `agent.run` and
`question.ask`. It cannot spawn its own children, and it cannot ask the user a
question — which is why a subtask prompt must be self-contained.

## agent.await, agent.kill, agent.steer

All three are risk `read_only` and live in `internal/agent/subagent_async.go`.

| Tool | Anchor | Args |
|---|---|---|
| `agent.await` | `subagent_async.go:40` | `id` (subagent), `job_id` (shell job), `task_key` (retained task), `any`, `all`, `peek`, `timeout_seconds` |
| `agent.kill` | `subagent_async.go:442` | `id` or `job_id` |
| `agent.steer` | `subagent_async.go:517` | `id`, `message` |

`agent.await` waits for one of four things — a subagent by `id`, a background
shell job by `job_id`, the first to finish with `any: true`, or everything with
`all: true` — and takes exactly one. `peek: true` with an `id` or `job_id`
returns current status and recent output immediately instead of waiting.
`timeout_seconds` bounds a wait; on expiry you get what is still running.

`"task_key"` is a fifth, mutually exclusive mode, and it is a **lookup, not a
wait**: it reads one retained owned task by the key `agent.run` reported and
returns its recorded outcome, mode, worktree, branch, candidate commit and, when
this session still holds it, the retained report body. A retained task outlives
its child, so the lookup keeps working after a restart and never revives
anything; a key another conversation owns is not found rather than borrowed.
The live numeric handles keep their existing semantics unchanged.

`agent.await` must stay `read_only`: a blocking handler that held the write gate
would deadlock the very child it is waiting for.

`agent.steer` sends an instruction into a running child, delivered at its next
step. It is **main-agent only** — `SubtaskScopeView` excludes it, so a child
cannot steer a sibling (`internal/app/app.go:861`).

## agent.integrate, agent.discard

The parent's two lifecycle tools, in `internal/agent/subagent_lifecycle.go`,
registered only when the conversation has a durable workspace service. Both
take exactly `task_key` and `expected_head` — neither accepts a path, branch or
owner, so a model cannot aim them at a checkout it does not own.

| Tool | Risk | Effect |
|---|---|---|
| `agent.integrate` | `workspace_write` | `git merge --no-ff` of the exact reviewed candidate SHA into the captured target checkout and branch |
| `agent.discard` | `destructive` | drops a verified clean owned branch and its worktree; retained dirty work is never force-removed |

A child's successful report does not mean its changes are in the parent; these
tools are how they get there. Both revalidate ownership, the exact candidate,
the target branch and the current target HEAD under a workspace lease before
touching git, and both journal their intent and result, so an interrupted
operation reconciles to one truthful receipt instead of a second merge. A stale
`expected_head`, a moved target, a detached or switched branch, or a dirty
source or target refuses rather than forcing anything. These are parent-only:
`SubtaskScopeView` excludes them from every child.

## skill.load

**For:** loading a skill's instruction set into the current context.

**Surface** (`internal/skills/tool.go:22`), risk `read_only`, `Cacheable: false`:

| Arg | Required | Notes |
|---|---|---|
| `name` | yes | |

**Loading an already-active skill is a re-fetch, not an error.** It resets the
skill's body age and returns nil (`tool.go:107-110`). This is how a model
recovers a body that has aged out of the wire — skill bodies are dropped from
the replayed history as they age, so asking again is the supported way to get
the full text back. A model that treats the repeat load as a mistake will
instead try to work from a partial memory of the skill.

**Failure mode:** `unknown skill %q. Available: %v` (`tool.go:104`) — the error
lists the available names, so a typo is recoverable from the error alone.

**Budget.** `skills.max_active` bounds how many skills are active at once. When
the cap is full, the least-recently-activated non-pinned skill is evicted rather
than the new load being refused (`tool.go:138`). Autoloaded skills are
user-configured always-on and are never evicted. The comment states the reason:

```go
// This replaces the old behaviour of refusing the new skill: refusing left
// the model stuck with whatever it happened to load first, with no way to
// change the set.
```

## Call sequence

Decompose first — see the `work-decomposition` skill — then:

1. `agent.run` once per independent unit.
2. `agent.await` to collect the reports (or `any: true` to take the first).
3. `agent.steer` to redirect a child that is going the wrong way.
4. `agent.kill` to stop one that is no longer needed.

For background shell work the same pair applies: `shell.run` with
`background: true` returns a `job_id`, and `agent.await` takes that `job_id`.
One pair of tools manages all background work, subagents and jobs alike.

Skills are loaded with `skill.load` when a skill's description matches what you
are about to do — before acting, not after. See the `dispatching-parallel-agents`
skill for when parallel dispatch is worth its coordination cost.
