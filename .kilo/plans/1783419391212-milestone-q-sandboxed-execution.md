# Milestone Q — Sandboxed Command Execution

## Status of prior "superpowers" work (context)

Verified already implemented in code; treat as **done**, out of scope here:

- Loop reliability trio — `internal/agent/{runner,progress,finalize}.go`
- Telemetry / evals — `internal/agent/metrics.go`, `internal/db/turnmetrics.go`, `internal/agent/eval_scenarios_test.go`
- TUI single-column redesign — plan present; `internal/app/tui/*`
- Structured output — `config.Agent.MaxStructuredOutputChars`, `internal/agent/envelope_schema.go`
- Ask-user action — `ask.user` in `internal/agent/{runner,prompts,protocol,envelope_schema}.go`
- Novelty-aware stall detection — `internal/agent/progress.go`

This plan covers only Milestone Q.

## Goal

Isolated, safe command execution for `shell.run` / `test.run` with resource controls,
network policy, and an audit trail — without breaking local-first zero-dependency use.

## Design decisions (resolved)

1. **Pluggable `Sandbox` abstraction** with three backends: `passthrough`, `restricted`, `container`.
2. **Default = `restricted`, on by default**, with non-breaking generous limits.
3. **Network isolation + no-host-access are enforced only by the `container` backend.**
   `restricted` degrades honestly and reports its real capabilities (no false security).
4. **Full `container` backend ships in Q** (Docker/Podman detection, `--network none|bridge`,
   `--memory`/`--cpus`, read-only root + rw workspace bind mount, non-root uid, auto-cleanup).
5. **Audit** extends the existing `tool_calls` table with nullable sandbox columns
   (same additive `ALTER TABLE` pattern already used for `command_exit_code`).

## Architecture

New package `internal/sandbox/` implementing the existing
`native.CommandRunner` interface (`Run(ctx, CommandRequest) (CommandResult, error)`),
injected via `native.Options.CommandRunner` in `internal/app/app.go:226`. The current
`execRunner{}` in `internal/tools/native/runner.go` becomes the `passthrough` backend.

```
internal/sandbox/
  sandbox.go        — Sandbox interface, Capabilities, Config, backend selection factory
  restricted.go     — in-process backend (default)
  restricted_unix.go / restricted_windows.go — SysProcAttr, ulimit wrapping, pgroup kill
  container.go      — Docker/Podman backend
  container_detect.go — runtime detection (docker/podman on PATH + daemon reachable)
  result.go         — CommandResult + SandboxMeta (backend, limits, network_isolated, killed_reason, duration)
```

### CommandResult extension

Add a `Meta SandboxMeta` field to `native.CommandResult` (or return it alongside).
`SandboxMeta{ Backend string; NetworkIsolated bool; MemoryLimitBytes int64; CPUSeconds int; KilledReason string; DurationMS int64 }`.
`runShellCommand` in `command.go` passes this through to the audit write.

## Task list

1. **Config surface** — add `[tools.shell.sandbox]` to `config.ShellToolConfig`
   (`internal/app/config/config.go`, plus `configFile` mirror + `save.go` round-trip):
   - `backend` (`"restricted"` default, `"passthrough"`, `"container"`)
   - `memory_limit_mb` (default 2048), `cpu_seconds` (default 0 = off), `max_processes` (default 512), `file_size_limit_mb` (default 0 = off)
   - `container_image` (default e.g. `"alpine:latest"` or configurable), `container_runtime` (`"auto"|"docker"|"podman"`)
   - `env_allowlist` (default: `PATH,HOME,USER,SHELL,LANG,LC_ALL,TERM,TMPDIR,GOPATH,GOCACHE,GOMODCACHE`), `env_denylist`
   - Reuse existing `AllowNetwork` for network policy; keep existing `DefaultTimeoutSeconds`/`MaxOutputBytes`.
   - Update `config_test.go` and `save_test.go` for defaults + merge + round-trip.

2. **Sandbox interface + factory** (`internal/sandbox/sandbox.go`):
   - `type Sandbox interface { Run(ctx, native.CommandRequest) (native.CommandResult, error); Capabilities() Capabilities }`
   - `Capabilities{ ResourceLimits, NetworkIsolation, FilesystemIsolation bool; Backend string }`
   - `New(cfg SandboxConfig, logger) (Sandbox, error)` selects backend; on `container` when no runtime detected, **fall back to `restricted` with a logged warning** (do not hard-fail startup).

3. **`passthrough` backend** — move current `execRunner` behavior here; `Capabilities` all false.

4. **`restricted` backend** (`restricted.go` + build-tagged unix/windows files):
   - cwd confinement to `CommandRequest.Dir` (reject empty/relative).
   - env scrubbing: build child env from `env_allowlist` minus `env_denylist` only.
   - unix: wrap as `/bin/sh -lc 'ulimit -t <cpu>; ulimit -f <fsize>; ulimit -u <procs>; exec <cmd>'`
     for limits `ulimit` supports; set `SysProcAttr{Setpgid: true}` and kill the **process group**
     on timeout/ctx-cancel (fixes current orphaned-child gap).
   - memory: apply via `ulimit -v` where supported (Linux). **Document that macOS `ulimit -v` is unavailable** — memory cap on darwin is best-effort/no-op; report accordingly in `SandboxMeta`.
   - windows: `restricted_windows.go` applies timeout + env scrub + output cap only (no ulimit); `Capabilities.ResourceLimits=false`.
   - enforce existing timeout + `MaxOutputBytes` (already in `command.go`).
   - `Capabilities{ ResourceLimits: true (unix), NetworkIsolation: false, FilesystemIsolation: false }`.

5. **`container` backend** (`container.go`, `container_detect.go`):
   - detect runtime (`container_runtime` config; `auto` = docker then podman on PATH + `info` reachable).
   - run: `<rt> run --rm --network <none|bridge> --memory <N>m --cpus <n> --read-only
     -v <workspaceRoot>:<workdir>:rw -w <workdir> --user <nonroot uid:gid>
     -e <allowlisted vars> <image> /bin/sh -lc <cmd>`.
   - `--network none` when `AllowNetwork=false`, else `bridge`.
   - capture stdout/stderr/exit code; enforce timeout via ctx + `docker kill`/`--rm` cleanup on cancel.
   - `Capabilities{ ResourceLimits: true, NetworkIsolation: true, FilesystemIsolation: true }`.
   - errors (image pull failure, runtime down) surface as tool errors, not crashes.

6. **Wire into app** — `internal/app/app.go:226`: construct `sandbox.New(...)` from `cfg.Tools.Shell.Sandbox`
   and pass as `native.Options.CommandRunner`. Keep injection seam intact so tests still pass `fakeRunner`.

7. **Audit extension** (`internal/db/`):
   - `db.go`: add nullable columns via existing additive ALTER pattern —
     `sandbox_backend TEXT, sandbox_network_isolated INTEGER, sandbox_limits_json TEXT, sandbox_killed_reason TEXT, duration_ms INTEGER`.
   - `migrations.go`: add same columns to the `CREATE TABLE tool_calls` for fresh DBs.
   - `audits.go`: extend `SaveToolCall`/`GetToolCalls` (and the `ToolCall` struct) to carry sandbox meta.
   - `internal/tools/native/command.go` `runShellCommand`: populate meta and thread to the audit write path (find where `SaveToolCall` is currently invoked and add fields).

8. **TUI surface** (minimal, non-blocking) — show backend + isolation in the approval/exec line,
   e.g. `network: blocked (container)` vs `network: not isolated (restricted)`, sourced from
   `Sandbox.Capabilities()`. Locate current shell approval/exec rendering in `internal/app/tui/*`
   and add a single status field; no layout redesign.

9. **Docs** — update `docs/04-tooling-and-shell-safety.md` (implement the `ShellPolicy` section for real,
   note macOS memory-limit caveat), and flip Milestone Q → ✅ in
   `docs/08-roadmap-and-milestones.md` and `docs/10-mvp-implementation-checklist.md`
   (add a Milestone Q checklist block). Note in `docs/02-system-architecture.md` that
   `internal/sandbox/` now exists.

## Failure modes to handle

- Container runtime absent/unreachable → fall back to `restricted` + warn (no startup failure).
- Timeout / ctx cancel → kill whole process group (restricted) or `docker kill` (container); set `KilledReason`.
- Empty/relative `Dir` → reject before exec.
- macOS memory limit unsupported → report `MemoryLimitBytes: 0` / capability note, don't claim enforcement.
- Windows → resource limits reported false; timeout + env scrub still apply.

## Validation

- `go build ./cmd/marshal` (CGO_ENABLED=1) and `go vet ./...` clean.
- `go test ./...` green, including existing `internal/tools/native/command_test.go` (fakeRunner path unchanged).
- New unit tests:
  - `internal/sandbox`: backend factory selection + container→restricted fallback; env allowlist scrubbing; capability reporting per backend.
  - `restricted_unix`: timeout kills process group (spawn child that outlives parent, assert reaped); ulimit wrapping present in invocation.
  - `container`: runtime detection logic and arg construction (table-test the built `docker run` argv without requiring a daemon; guard live-run tests behind a `docker`-available skip).
  - `internal/db`: migration adds columns on existing DB; `SaveToolCall`/`GetToolCalls` round-trip sandbox meta.
  - `internal/app/config`: sandbox defaults, merge precedence, save round-trip.
- Manual: `marshal` in this repo, run `go test ./...` via agent under `restricted` (works), then set
  `backend="container"` and confirm network-blocked run + audit row shows `sandbox_network_isolated=1`.

## Out of scope

- Linux `bwrap`/namespace backend and macOS `sandbox-exec` seatbelt profiles (future).
- Per-MCP-tool sandboxing (MCP tools run in their own server process).
- Regex argument guardrails / rich MCP approval (separate `docs/11` enhancements).

## Note for implementer

This plan requires source edits and a schema migration — switch to an implementation-capable agent to execute.
