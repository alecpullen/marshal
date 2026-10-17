<!-- tools: shell.run, shell.sudo, test.run, todo.write -->

# Running commands

Four tools run commands or track work. `shell.run` is the general one; the other
three are constrained or specialised.

## shell.run

**For:** building, testing, running programs, and any command that is not a file
read or a repository-state query.

**Surface** (`internal/tools/native/command.go:34`), risk `command`:

| Arg | Required | Notes |
|---|---|---|
| `command` | yes | |
| `timeout_seconds` | no | clamped to a default and a maximum |
| `background` | no | start a job and return immediately |

Runs with the workspace root as the current working directory, subject to
conservative guardrails. Never use it to create or modify files — no
redirection, heredocs, or tee; those bypass diff review, backups, and rollback.
Use `git.*` for repository state and the workspace tools for worktree isolation
and merging.

**Failure mode:** `command is required` (`command.go:52` for the background
path, `command.go:122` for the foreground path).

**Background form.** With `background: true` the call returns immediately
(`command.go:61-67`):

```
job_id: <id>
```

The job is then collected with `agent.await`'s `job_id` form — see
`delegation.md`. `agent.await` and `agent.kill` cover both subagents and shell
jobs (`internal/app/app.go:846-862`), so one pair of tools manages all
background work.

**Call sequence for background work:** `shell.run` with `background: true` →
read the `job_id` → `agent.await` with that `job_id`, or `agent.await` with
`any: true` to take the first to finish.

## shell.sudo

**For:** running a single command as root.

**Surface** (`internal/tools/native/sudo.go:154`), risk `destructive`,
`Deferred: true`. Platform-gated: registered only on non-Windows
(`sudo_register.go` carries `//go:build !windows`; `sudo_register_windows.go`
returns an empty slice, because there is no sudo on Windows and privileged
execution is a non-goal).

| Arg | Required | Notes |
|---|---|---|
| `command` | yes | the **bare** command, without a leading sudo |
| `reason` | no | why it needs root; shown in the password prompt |
| `timeout_seconds` | no | default 300, max 900 |

Pass the bare command: Marshal adds the leading sudo itself, and a command that
invokes a privilege wrapper is refused.

**The security contract.** The password is read from a masked prompt and
delivered to the child on stdin — never argv, never the environment, and no PTY,
so the secret cannot be echoed or captured (`internal/tools/native/native.go:126-129`).
Because stdin is consumed by the password, interactive commands see EOF: pass
non-interactive flags (`apt-get -y`, `--non-interactive`) rather than relying on
a prompt.

**Fail closed.** When no `SudoPolicy` is wired the tool refuses every command
(`native.go:91-93`). The catastrophic floor applies inside the payload too, so
the tool is not a way around the floor `shell.run` obeys.

`shell.sudo` is `Deferred`, so it is not in the prompt until `tools.select`
loads it. It is available only in the TUI; other clients cannot prompt for a
password.

## test.run (internal)

**For:** running the configured test command.

**Surface** (`internal/tools/native/command.go:77`), risk `command`,
`Internal: true` (`command.go:86`).

| Arg | Required | Notes |
|---|---|---|
| `command` | no | overrides the configured test command; subject to the same approval policy |

**The general agent runs tests through `shell.run`.** Only the swarm tester
(`TesterView`) receives this constrained tool, which is why it is internal: it
exists to give one role a narrower surface, not to give the model a second way
to run tests.

## todo.write

**For:** tracking multi-step work.

**Surface** (`internal/tools/native/todos.go:51`), risk `workspace_write`:

| Arg | Required | Notes |
|---|---|---|
| `todos` | yes | array of `{id?, content, status}` |
| `drop_unfinished` | no | when true, the submitted list becomes the whole list |

Statuses: `pending`, `in_progress`, `completed`.

**The hazard: it mutates session state despite reading like a passive tracker.**
It *replaces* the list. Unfinished items omitted from the submission are carried
over unless `drop_unfinished: true` (`todos.go:88-105`), and the result content
names what was carried. Identity and timestamps are the store's, never the
model's (`todos.go:82-86`).

The auto-carry behaviour is deliberate, and the comment says why:

```go
		// Auto-carry: unfinished items missing from the submitted list are
		// kept (appended with their status) instead of erroring. The old
		// drop-guard fired during ordinary reorganisation and forced a
		// retry loop with the model.
```

**Failure modes:**

- `todo %d has empty content; each item needs a non-empty %q string describing the task` (`todos.go:67`)
- `invalid todo status %q; use pending|in_progress|completed` (`todos.go:72`)
- `todo store not available` (`todos.go:76`)

The empty-content check is hand-rolled because `registry.ValidateArgs` only
checks that the arguments are a JSON object — the declared schema's `required`
is not enforced — so `content` has to be checked in the handler.

## Call sequence

Foreground: `shell.run` → read the output → act on it. Background: `shell.run`
with `background: true` → `agent.await` with the returned `job_id`. Multi-step
work: `todo.write` at the start, then again as each item completes.
