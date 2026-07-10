# Borrowed Features Spec

This document captures the user-facing configuration shape for features
adapted from external projects (Claude Code, OpenAI Codex CLI, etc.) so
the TOML surface is documented in one place.

## F20: Lifecycle Hooks

Lifecycle hooks let users run local shell commands at well-defined
points in the agent loop (before/after a tool call, end of a turn).
Hooks are **local process execution only** — there is no remote/network
transport — and project-local hook entries only run when the project
has been explicitly trusted (see `internal/trust`).

### R1: TOML shape

The hook configuration is grouped under a single `[hooks]` table with a
list of entries under `[[hooks.entries]]`. The earlier draft that used
`[[hooks]]` is **invalid TOML** (mixing an array of tables with a
non-array `[hooks]` section in the same key), so the resolved shape is:

```toml
[hooks]
fail_closed = false

[[hooks.entries]]
event = "pre_tool_use"
matcher = "file.write_patch"
command = "./scripts/check-patch.sh"
timeout_ms = 2000

[[hooks.entries]]
event = "turn_end"
command = "./scripts/turn-end.sh"
timeout_ms = 1000
```

- `fail_closed` (bool, default `false`): when true, a hook that exits
  non-zero or times out is treated as a deny/decision by the agent loop.
- `entries` (array of tables): each entry binds an `event` name to a
  shell `command` with an optional `matcher` and a `timeout_ms` cap.

### R2: Trust

Project-local hook entries only run when the project has been marked
trusted (permanent or session) by the user. The trust decision is
propagated to callers via `config.LoadOptions.Trusted` (a `*bool`
out-parameter that `Load` sets to `true` for both
`DecisionTrustPermanent` and `DecisionTrustSession`).

### R3: Hook config example

A minimal `shell.run` policy hook:

```toml
[hooks]
fail_closed = false

[[hooks.entries]]
event = "pre_tool_use"
matcher = "shell.run"
command = "./scripts/marshal-shell-policy.sh"
timeout_ms = 2000
```

Project hooks only run in trusted projects, and failures default to
allow unless `fail_closed = true`.

## F21: Agent Client Protocol (ACP)

Marshal exposes a headless subcommand that speaks the Agent Client
Protocol (ACP) so external editors and reference clients can drive the
agent loop over a standard transport.

### Usage

```bash
marshal acp
```

The `marshal acp` subcommand speaks ACP over **stdio** (JSON-RPC 2.0
framing, one message per line on stdin/stdout) and is intended for
editors and reference clients, **not direct terminal use**. It is not
an interactive TUI mode — running it from a terminal will block
waiting for JSON-RPC frames on stdin.
