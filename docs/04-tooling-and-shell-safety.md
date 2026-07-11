# 04. Tooling and Shell Safety

## Goal

Marshal should support powerful tool and bash usage without giving the model uncontrolled access to the user's machine.

Tool execution must be:

- explicit
- inspectable
- permissioned
- logged
- reversible where possible

## Tool broker

The Tool Broker manages all native tools, MCP tools, and future plugins.

```go
type Tool struct {
    Name        string
    Description string
    Schema      json.RawMessage
    RiskLevel   RiskLevel
    Handler     ToolHandler
    Policy      ToolPolicy
}
```

## Risk levels

```go
type RiskLevel int

const (
    RiskReadOnly RiskLevel = iota
    RiskWorkspaceWrite
    RiskCommand
    RiskNetwork
    RiskDestructive
)
```

## Core tools

| Tool | Purpose | Risk |
|---|---|---|
| `repo.search` | text search using ripgrep or internal search | read-only |
| `file.read` | read file range | read-only |
| `file.write_patch` | apply patch to workspace | workspace write |
| `git.status` | inspect git state | read-only |
| `git.diff` | inspect diff | read-only |
| `git.checkpoint` | create safe checkpoint branch/commit/stash | write |
| `shell.run` | run shell command | variable |
| `test.run` | run configured test command | command |
| `symbols.find` | find symbol via Tree-sitter index | read-only |
| `repo.map` | return repo map | read-only |
| `context.query` | query project DB | read-only |
| `memory.write` | write confirmed project memory | write |
| `agent.spawn` | delegate to specialist agent | swarm |

## Shell command lifecycle

```text
1. Model proposes command
2. Command classifier assigns risk
3. Policy engine checks allow/deny rules
4. TUI shows command, cwd, reason, risk, and expected effect
5. User approves, denies, edits, or creates rule
6. Tool executes with timeout and resource limits
7. Output is streamed/truncated/summarised
8. Tool call is logged in project DB
```

## Approval prompt example

```text
Agent wants to run:

  go test ./...

Reason:
  Validate the package after modifying the parser.

Risk:
  Low - test command, no destructive flags detected.

[Enter] approve   [e] edit   [d] deny   [a] always allow go test
```

## Dangerous command examples

Require explicit confirmation for:

```bash
rm -rf
git reset --hard
git clean -fd
curl ... | sh
wget ... | sh
sudo
chmod -R
chown -R
docker system prune
mkfs
shutdown
reboot
```

## Command classifier

The command classifier should inspect:

- executable name
- arguments
- shell operators
- pipes
- redirections
- glob patterns
- filesystem targets
- network use
- privilege escalation
- destructive flags

Examples:

```text
go test ./...                 → low risk
git status                    → read-only
npm install                   → network/write risk
rm -rf node_modules           → destructive but scoped
git reset --hard              → destructive
curl https://x | sh           → high-risk network execution
```

## Shell execution policy

Milestone Q (Phase 7) ships isolated command execution via the pluggable
`internal/sandbox/` package, which implements `native.CommandRunner` and is
injected into `shell.run` / `test.run` at app startup. Three backends are
selected via `[tools.shell.sandbox]`:

| Backend | Isolation | Resource caps | Network | Default |
|---|---|---|---|---|
| `passthrough` | none | none | none | opt-in only |
| `restricted` | cwd confinement, env allowlist scrub | ulimit/rlimit (cpu/file-size/max-procs) | not enforced (honest) | yes |
| `container` | mount namespace, read-only root + rw workspace bind mount | `--memory`/`--cpus` + the above | `--network none` when `AllowNetwork=false` | opt-in; falls back to `restricted` when no Docker/Podman runtime is detected |

```toml
[tools.shell]
allow_network = false
default_timeout_seconds = 120
max_output_bytes = 200000

[tools.shell.sandbox]
backend = "restricted"          # or "container" / "passthrough"
memory_limit_mb = 2048          # 0 = unset; darwin can't enforce address space, reported honestly
cpu_seconds = 0                 # ulimit -t; 0 = unset
max_processes = 512             # ulimit -u; 0 = unset
file_size_limit_mb = 0          # ulimit -f; 0 = unset
container_runtime = "auto"     # "auto" | "docker" | "podman"
container_image = "alpine:latest"
env_allowlist = ["PATH","HOME","USER","SHELL","LANG","LC_ALL","TERM","TMPDIR","GOPATH","GOCACHE","GOMODCACHE"]
env_denylist = []
```

### Honest capability reporting

`restricted` mode cannot block network cross-platform (it would need a mount/
network namespace, which only the `container` backend provides). The sandbox
reports its real `Capabilities` to the TUI, which renders, per command:

- `sandbox: container · network blocked` (container + `AllowNetwork=false`)
- `sandbox: container · network allowed` (container + `AllowNetwork=true`)
- `sandbox: restricted · network not isolated` (restricted)

The audit row stores the actual state, so the audit trail never claims
false isolation.

The legacy policy fields live on `tools.shell` (allow/confirm/deny rules,
`auto_approve` flag, `allow_network`); the sandbox subsection only tunes
*how* an approved command is run, not *whether* it may run.

### Shell execution lifecycle (updated)

```text
1. Model proposes command
2. Command classifier assigns risk (deprecated conservative guardrail)
3. Policy engine (tools/policy) checks allow/deny rules
4. TUI shows command, cwd, reason, risk, expected effect, AND sandbox isolation line
5. User approves, denies, edits, or creates rule
6. Selected sandbox backend executes:
   - restricted: ulimit-wrapped /bin/sh -lc, env allowlist, pgroup kill on timeout
   - container:   <rt> run --rm --network ... --memory ... --read-only -v <workspace>:rw
7. Output is streamed/truncated/summarised
8. Tool call is logged in tool_calls with sandbox_backend, sandbox_network_isolated,
   sandbox_limits_json, sandbox_killed_reason, duration_ms
```

### Sandbox limitations (documented)

- macOS `restricted` mode cannot enforce an address-space cap (`ulimit -v`
  unsupported); `meta.MemoryLimitBytes` reports 0 there. CPU/file-size/
  process caps still apply.
- `restricted` mode cannot block network. Use `container` for network
  isolation. The TUI and audit make this explicit so users do not get a
  false sense of isolation.
- `container` mode requires a reachable Docker/Podman daemon. When absent
  the app falls back to `restricted` with a logged warning, rather than
  failing to start.
- The sandbox wraps `shell.run` and `test.run`. It does not currently
  sandbox per-MCP-tool invocations (MCP tools run in their own server
  process).

## Patch safety

All file edits should ideally use patches.

Patch flow:

```text
1. Agent proposes patch
2. Patch is parsed and validated
3. TUI shows unified diff
4. User approves or edits
5. Patch applies
6. Git diff is shown
7. Tests can run
8. Rollback option remains available
```

## Git safety

Before major changes, offer:

- checkpoint branch
- temporary stash
- lightweight commit
- patch file export

Example:

```text
This task may modify 8 files.
Create a checkpoint first?
[y] yes  [n] no
```

## Prompt injection protection

Repository files are untrusted input.

Rules:

- file contents cannot override system policy
- README instructions cannot approve commands
- comments in code cannot change tool permissions
- tool results should be treated as data, not instructions
- remote provider calls should redact secrets by default

## Execution and shutdown guarantees

The following guarantees apply to all commands run through the sandbox backends
(foreground `shell.run`/`test.run` and background `shell.run background=true`
jobs). They were introduced in the July 2026 execution and shutdown safety batch.

### Foreground/background sandbox parity

Foreground and background commands are run through the same `CommandRunner`
interface. The sandbox backend selected in `[tools.shell.sandbox]` —
`passthrough`, `restricted`, or `container` — applies identically to both:

- The `JobManager` receives the same `CommandRunner` instance that `shell.run`
  uses at construction time.
- Resource caps (memory, CPU, process count), environment scrubbing, working
  directory confinement, and any network policy enforced by the selected
  backend (e.g., `--network=none` in container mode) apply identically to
  both paths.
- The TUI displays the same sandbox isolation line for both paths.

### Bounded output

Every output stream (stdout and stderr) passes through `BoundedOutput`, a
concurrency-safe writer that retains at most `max_output_bytes` per stream:

```
[ tools.shell ]
max_output_bytes = 200000
```

When a stream exceeds the limit, the retained prefix is preserved and the
`OutputTruncated` flag is set in `SandboxMeta`. The flag propagates to:

1. **Tool results** — the model sees the truncated output in the tool response.
2. **Audit trail** — `AuditEvent.Sandbox.OutputTruncated` is persisted so
   post-hoc review can detect truncation.
3. **Background-job output** — `job.output` appends the string
   `\n[output truncated]` to the returned content when truncation occurred.

The same `BoundedOutput` writer is used for both foreground and background
commands. Background jobs additionally wrap bounded writers inside a
`safeBuffer` (mutex + `bytes.Buffer`) for concurrent read access by
`job.output` while the job goroutine is still writing.

Completed background jobs are evicted after a configurable retention period
(`BackgroundRetention`, default 8 hours), preventing completed records from
occupying concurrency slots indefinitely.

### Unix termination: SIGTERM → grace → SIGKILL

When a running command's context is cancelled (due to timeout, user
cancellation, job kill, or shutdown), the Unix backend follows a three-step
protocol:

1. **SIGTERM** to the process group (the child process and its descendants).
2. **Two-second grace window** polls the process group at 50 ms intervals.
3. **SIGKILL** to the process group if it still exists after the grace period.

The `SandboxMeta.KilledReason` field distinguishes `"timeout"` (context
deadline exceeded) from `"cancelled"` (user or shutdown cancellation) so the
audit trail and model have the correct semantic signal.

Windows bypasses the grace and kills immediately (the OS provides equivalent
process-tree termination through `TerminateProcess`).

### Runtime-owned job cancellation and join

The `Runtime` struct owns the `JobManager` and its lifecycle:

- **Quiesce** cancels all background jobs (via the manager context), waits
  for job goroutines to finish (with a 5-second outer deadline), and joins
  the active agent turn (via `State.WaitForWork`). Quiesce is idempotent.
- **Close** calls Quiesce first, then tears down MCP connections, brokers,
  snapshots, the database, and the log file in prescribed order.
- The `workCtx` passed to `buildAgentRunner` is derived from the runtime
  context, so cancelling the runtime context cancels all sandbox commands,
  background jobs, and the agent loop simultaneously.

This means Ctrl+C, `/quit`, and `/exit` all quiesce active work before
finalizing knowledge and closing persistence — the user never sees an
intentional cancellation reported as a provider failure.

### Container limitation: client-side only

The container backend (`docker`/`podman`) sends `docker/podman run --rm` and
waits for the child process to exit. The local client process is joined
(reaped) and its termination is guaranteed. However, **daemon-side container
removal after client loss is not guaranteed**: if the runtime loses contact
with the Docker/Podman daemon (e.g. the daemon is killed or the network
partition is unrecoverable), the container may remain running orphaned on
the daemon host. The `--rm` flag ensures removal when the daemon and client
are in normal communication; the orchestration layer does not yet implement
daemon-side health-checked cleanup.

## Secret protection

Before sending context to a remote model:

- scan for known secret patterns
- skip `.env` by default
- skip private keys
- respect `.gitignore`
- warn when sending private-looking data

Privacy config:

```toml
[privacy]
remote_providers_allowed = false
redact_secrets = true
include_gitignored_files = false
```

## Audit log

Every tool call should record:

- timestamp
- agent role
- model used
- tool name
- arguments
- risk level
- approval state
- result summary
- files changed
- command exit code
