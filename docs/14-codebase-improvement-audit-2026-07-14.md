# Codebase Improvement Audit — 2026-07-14

## Scope

A parallel exploration swarm (five `explore` agents) was dispatched across
distinct layers of the Marshal codebase. Each agent returned structured
findings against a fixed rubric:

- **CRITICAL / HIGH** — security flaw, correctness bug, or unsafe default
  that should block a release.
- **MEDIUM** — real bug, performance issue, or robustness gap that affects
  daily use.
- **LOW / POLISH** — code quality, dead code, observability, ergonomics.

This document consolidates all findings. Each entry includes a stable
identifier, file path and line range, severity, problem statement, and a
concrete suggested fix.

Total: **~191 findings** across six domains (15 high/critical, ~55 medium,
~120 low/polish). Findings are open unless explicitly marked resolved.

## Top priorities (release blockers)

These are the items most likely to cause user-visible harm, security
exposure, or data loss. Fix order below is recommended, not required.

### F-SEC-01 — `shell.run` / `test.run` pass arbitrary user strings to `/bin/sh -lc`

- **File:** `internal/tools/native/runner.go:13`,
  `internal/tools/native/command.go:30-62`,
  `internal/sandbox/shell_unix.go:10`,
  `internal/sandbox/container.go:156-167`
- **Severity:** CRITICAL
- **Problem:** All shell backends (execRunner, restricted, passthrough,
  container) execute via `exec.Command("/bin/sh", "-lc", command)`. Any
  policy/allowlist bypass therefore grants arbitrary code execution. The
  policy guardrails are heuristic and cannot prevent shell-level obfuscation
  (`command; rm ...`, backticks, `${IFS}`, quoting tricks) once the string
  reaches the shell.
- **Fix:** Introduce an argv-based runner path for commands that do not need
  a shell; keep `/bin/sh -lc` only as an explicit shell-escape mode. At
  minimum, document the implication and ensure the TUI approval dialog
  surfaces it.

### F-SEC-02 — Policy engine auto-approves all non-shell tools regardless of `RiskLevel`

- **File:** `internal/tools/policy/policy.go:192-194`
- **Severity:** HIGH
- **Problem:** The fallback after guardrails/F4 rules allows any tool name
  other than `shell.run` / `test.run` with `ApprovalNotRequired`. This
  blanket-allows `file.write_patch`, `todo.write`, `job.kill`, `repo.index`,
  `diagnostics.check`, etc., regardless of their registered `RiskLevel`.
- **Fix:** Drive default approval off the registered `Risk` level. Require
  confirmation for `RiskWorkspaceWrite`, `RiskCommand`, `RiskNetwork`,
  `RiskDestructive`; reserve auto-allow for `RiskReadOnly`.

### F-SEC-03 — Symlink / mount-point traversal in file and search tools

- **File:** `internal/tools/native/helpers.go:29-93`,
  `internal/tools/native/file.go:42-57`,
  `internal/tools/native/search.go:50-92, 143-230`,
  `internal/repo/scanner.go:72-127`
- **Severity:** HIGH
- **Problem:** `resolveWorkspacePath` and `resolveWorkspacePathMulti` only
  verify that the *relative path* stays within a root. They do not follow
  symlinks. A file like `workspace/link-to-etc` resolves inside the workspace
  but reads/writes `/etc/passwd`. `filepath.WalkDir` in `repo.search` and
  `repo.Scanner` also follow directory symlinks, escaping the workspace
  silently.
- **Fix:** For every operation, `filepath.EvalSymlinks` the final absolute
  path and re-verify containment under the root. Use `O_NOFOLLOW` semantics
  or a bind-mount-based workspace view. Re-check containment immediately
  before read/write to close the TOCTOU window.

### F-SEC-04 — SSRF via HTTP redirects in `web.fetch`

- **File:** `internal/tools/native/web.go:59-89`
- **Severity:** HIGH
- **Problem:** The default `http.Client` follows redirects. `isPrivateURL`
  inspects only the originally supplied URL string, so a public URL that
  redirects to `http://169.254.169.254/` or link-local addresses bypasses the
  check. DNS rebinding is also possible because hostnames are not resolved
  before the check.
- **Fix:** Provide a custom `CheckRedirect` that re-runs `isPrivateURL` on
  every redirect target and resolves hostnames to IPs first. Reject
  redirects to private/link-local/loopback ranges and non-http(s) schemes.

### F-SEC-05 — MCP server env vars unvalidated (LD_PRELOAD / PATH hijack)

- **File:** `internal/tools/mcp/manager.go:25-30`
- **Severity:** HIGH
- **Problem:** `srv.Env` is built by string-concatenating user-supplied keys
  and values: `fmt.Sprintf("%s=%s", k, v)`. A config containing
  `LD_PRELOAD=…`, `PATH=…`, `DYLD_INSERT_LIBRARIES=…`, `PYTHONPATH=…`
  hijacks the spawned MCP server. There is no allow-list, deny-list, or
  escaping for values containing `=`, newlines, or NULs.
- **Fix:** Validate keys against an allow-list of safe names (or at minimum
  a deny-list of `LD_*`, `DYLD_*`, `PATH`, `LD_PRELOAD`, `IFS`, etc.).
  Reject values containing `\n`, `\r`, or `\x00`. Document the trust model
  in the config.

### F-SEC-06 — Arbitrary command execution via MCP server config

- **File:** `internal/tools/mcp/client.go:41-59`, `manager.go:48-101`
- **Severity:** HIGH
- **Problem:** `client.Start` calls `exec.CommandContext(ctx, c.Command, c.Args...)`
  with no validation. Anyone who can write `marshal/config.toml` (a malicious
  skill, a compromised plugin, a misconfigured sync) gets arbitrary code
  execution as the Marshal process.
- **Fix:** Validate the command against a known-safe list (`npx`, `uvx`,
  `python -m`, `node`, `deno`). For unlisted commands, require an explicit
  `trust = "unrestricted"` flag. Log a clear warning when running an
  untrusted command. Consider routing MCP servers through the existing
  sandboxed shell.

### F-SEC-07 — Approval "always allow" pattern uses `argv0 + " *"` (false sense of safety)

- **File:** `internal/app/tui/model.go:2098-2118`
- **Severity:** HIGH
- **Problem:** For `shell.run` / `test.run`, the always-allow pattern is
  `words[0] + " *"`. This means "always allow git …" permits `git ; rm -rf /`
  because only argv0 is checked.
- **Fix:** Use the full command string (or a normalized argv list) as the
  pattern; require the policy engine to match against the full parsed
  command, not just argv0.

### F-SEC-08 — Onboarding writes raw API keys to project-local config

- **File:** `internal/app/onboarding.go:237-280`
- **Severity:** HIGH
- **Problem:** If the user types a raw OpenRouter/OpenAI key that does not
  match `^[A-Z][A-Z0-9_]*$`, it is saved directly to `.marshal/config.toml`
  as `api_key = "..."`. This leaks credentials into a project-local file
  that is often committed.
- **Fix:** Treat any non-empty key input as sensitive. If the string looks
  like an env var name, write `api_key_env`; otherwise prompt the user to
  set it in their environment and only persist a placeholder.

### F-SEC-09 — `legacyRoute` bypasses `remote_providers_allowed` privacy policy

- **File:** `internal/llm/routing/router.go:50-53, 100-114`
- **Severity:** HIGH
- **Problem:** `legacyRoute` is used as the final fallback whenever no
  configured role/preset exists. It does **not** check
  `r.config.RemoteAllowed`, even though the rest of `resolveProfileRole`
  enforces it. A user with `privacy.remote_providers_allowed = false` and a
  stale `legacy_provider` will silently have their prompts sent to that
  remote endpoint. The returned `Route` also has `LocalOnly` left as
  `false`.
- **Fix:** Gate the fallback on `r.config.RemoteAllowed || isLocalProvider(r.config.LegacyProvider)`.
  Return `ErrRemoteProviderBlocked` (or a dedicated
  `ErrLegacyProviderBlocked`) and let the caller surface it.

### F-SEC-10 — `MaxTurnContextTokens` uses `max` instead of `min`, risks model overflow

- **File:** `internal/agent/runner.go:832-845`
- **Severity:** HIGH
- **Problem:** The comment says the configured value is a floor, but the
  code does `if effective > r.MaxTurnContextTokens { r.MaxTurnContextTokens = effective }`
  — the configured value is treated as a *ceiling* (the model-derived value
  only ever raises it). A user-configured `MaxTurnContextTokens=100000` for
  a 32k model feeds it 100k tokens, which the model truncates or 400s on.
- **Fix:** Replace with `if window > 0 && (r.MaxTurnContextTokens == 0 || effective < r.MaxTurnContextTokens) { ... }`
  (i.e. use the smaller value). Fix the comment to match the chosen
  semantics.

### F-SEC-11 — Parallel `actions[]` read-only violation has no iteration budget

- **File:** `internal/agent/runner.go:599-617`
- **Severity:** HIGH
- **Problem:** When `r.allReadOnly(action.Actions)` returns an error, the
  runner appends a `BuildCorrectionMessage` and `continue`s *without*
  incrementing `iteration` or `consecutiveParseFailures`. A model that keeps
  emitting non-read-only actions in the `actions` array loops indefinitely
  with no budget pressure ever applied.
- **Fix:** Increment `iteration` and update stats before the `continue`;
  treat the violation as a parse failure so `maxConsecutiveParseFailures`
  kicks in.

### F-SEC-12 — `failOutbound` can deadlock on full channels, freezing ACP

- **File:** `internal/acp/server.go:391-401`
- **Severity:** HIGH
- **Problem:** `failOutbound` does an unconditional `ch <- outboundResult{err: err}`.
  Channels are buffered with capacity 1. If a response was already
  delivered and the request goroutine abandoned the waiter on
  `<-ctx.Done()` without reading the buffered response, the buffer is full
  and `failOutbound`'s send blocks forever, freezing the entire `Serve`
  loop.
- **Fix:** Use a non-blocking send (`select { case ch <- …: default: }`),
  or close the channel after sending. Add a regression test that
  pre-loads a response, cancels the caller context, then triggers `Serve`
  shutdown.

### F-SEC-13 — `m.bridge == nil` silently drops approval requests, hangs the turn

- **File:** `internal/acp/turn.go:86-90, 241-248`
- **Severity:** HIGH
- **Problem:** When a turn manager is constructed without a `Perms` client
  (or `cfg.Perms == nil`), `m.bridge` is never set. The forwarder does
  `if m.bridge != nil` and silently skips the request. The runner is
  blocked on `pending.ResponseChan` indefinitely with no timeout or
  cancellation path.
- **Fix:** Make `Perms` a required field, or treat nil as a documented
  default (auto-approve / auto-deny), or fail the turn with a structured
  error when a pending approval arrives without a bridge.

### F-BUG-14 — Duplicate channel-send risk on approval/question response

- **File:** `internal/app/tui/model.go:784-904`,
  `internal/app/tui/approval.go:115-164`,
  `internal/app/tui/question.go:138-162`,
  `internal/app/session/session.go:846-889`
- **Severity:** HIGH
- **Problem:** `handleApproval` and `handleQuestion` route messages to
  sub-models. When the form completes, the parent sends the decision on
  `tc.ResponseChan` and clears the pending approval/question. A fast
  double-Enter, an `Esc` race with `huh` completion, or
  `ResolvePendingForShutdown` racing the TUI send can produce a second send
  on a buffered channel. The channel has buffer 1; the second send blocks.
- **Fix:** Guard `tc.ResponseChan` with a `sync.Once` or a `responded` flag
  before sending. Clear `pendingApproval` / `pendingQuestion` before
  sending to prevent re-entrancy. `ResolvePendingForShutdown` should close
  the channel rather than send when no one has responded.

### F-BUG-15 — `reloadAgentRuntime` leaves inconsistent state on reload failure

- **File:** `internal/app/app.go:776-842`
- **Severity:** HIGH
- **Problem:** If `buildAgentRunner` returns an error, `rt.State.Config` has
  already been updated by the config reloader. Subsequent turns still use
  the old runner with the new config, which can mismatch provider/model
  settings.
- **Fix:** Validate the new config before mutating `state.Config`; on
  failure roll back to the previous snapshot and surface the error in the
  TUI.

---

## Detailed findings

Each entry below is a single finding with file/line, severity, problem,
and suggested fix. Items are grouped by domain. The "F-XXX-NN" identifier
is stable and can be referenced from implementation branches.

### A. Security & sandbox

#### F-SEC-16 — Conservative command guardrails are substring-based and bypassable

- **File:** `internal/tools/native/command.go:110-136`,
  `internal/tools/policy/policy.go:25-28, 235-260`
- **Severity:** MEDIUM
- **Problem:** Guards use `strings.Contains(lower, "rm -rf")`, `"sudo"`,
  etc. Simple variants evade them: `rm -r -f /`, `rm -fr /`, `git clean
  -fdx`, `chmod --recursive 777 /`, `curl ... | python`. The legacy
  network-installer check only catches `sh/bash/zsh` after a pipe, missing
  `python`, `perl`, `ruby`, `awk`.
- **Fix:** Parse the shell AST once and match against normalized argv
  tokens (command basename plus flag canonicalization). Block all
  combinations of `rm` + recursive + force, `git reset --hard` with any
  args, `git clean -fd*`, `chmod/chown -R/--recursive`, and any pipe
  ending in an interpreter.

#### F-SEC-17 — Container backend still runs user command under `/bin/sh -lc`

- **File:** `internal/sandbox/container.go:156-167`
- **Severity:** MEDIUM
- **Problem:** Even though the container is isolated, the user command is
  passed as a string to a shell inside the container, allowing command
  substitution and shell injection.
- **Fix:** Parse the command into a clean `[]string` argv and invoke it
  directly. Provide a separate `shell.run` mode only when the user
  explicitly asks for shell semantics.

#### F-SEC-18 — `file.read` can exhaust memory on large regular files

- **File:** `internal/tools/native/file.go:46-60`
- **Severity:** MEDIUM
- **Problem:** The tool calls `os.Stat`, checks `IsRegular()`, then
  `os.ReadFile` the entire file. A multi-gigabyte regular file is fully
  loaded into memory before `limitOutput` truncates it. A symlink race
  could swap in a huge file after the stat.
- **Fix:** Cap reads to `maxOutputBytes + 1` (or a separate `max_file_bytes`
  config) using `io.LimitReader` over an opened file. Re-check size after
  open.

#### F-SEC-19 — `repo.search` silently ignores filesystem errors and follows symlinks

- **File:** `internal/tools/native/search.go:73-91`
- **Severity:** MEDIUM
- **Problem:** The `WalkDir` callback returns `nil` on any `err`, hiding
  permission-denied or unreachable paths. It also follows directory
  symlinks.
- **Fix:** Do not swallow walk errors; collect and surface them. Reject
  symlinked directories/files by resolving real paths and re-checking
  containment.

#### F-SAFE-20 — `AllowSudo` and `AllowDestructive` config flags are dead

- **File:** `internal/app/config/config.go:183-185`,
  `internal/tools/policy/policy.go:25-28, 160-170`
- **Severity:** MEDIUM
- **Problem:** The config file accepts `allow_sudo` and `allow_destructive`,
  and `config.SaveProjectConfig` persists them, but `PolicyEngine` never
  consults them. Users may enable these flags expecting controlled access.
- **Fix:** Either remove the flags from config to avoid a false sense of
  control, or implement them as escalation gates in the policy engine.

#### F-SAFE-21 — `RiskDestructive` risk level is unused

- **File:** `internal/tools/registry/types.go:15`,
  `internal/tools/native/native.go:110-130`
- **Severity:** MEDIUM
- **Problem:** The registry defines `RiskDestructive`, but no native tool
  uses it. Destructive shell patterns are classified as `RiskCommand` at
  best, and write tools are `RiskWorkspaceWrite`.
- **Fix:** Assign `RiskDestructive` to `shell.run` commands matching
  destructive patterns, and require explicit approval + pre-execution
  snapshot.

#### F-SAFE-22 — `file.write_patch` dry-run to apply TOCTOU window

- **File:** `internal/tools/native/file.go:143-230`
- **Severity:** MEDIUM
- **Problem:** The code validates patches in one loop, then re-reads and
  writes in a second loop. A background `shell.run` job or another
  process can modify the file between validation and write.
- **Fix:** Combine validation and application into one atomic pass, or
  record a content hash during validation and verify it immediately
  before writing. Use a file lock for the duration of the operation.

#### F-SAFE-23 — Default restricted backend leaks non-standard secrets

- **File:** `internal/sandbox/restricted.go:95-137`,
  `internal/sandbox/envutil/envutil.go:17-42`
- **Severity:** LOW
- **Problem:** With no explicit `env_allowlist`, the restricted backend
  passes the entire parent environment minus a suffix/prefix scrub list.
  A custom secret stored under a non-matching key name is leaked to
  sandboxed commands.
- **Fix:** Change the default to a minimal allowlist (`PATH`, `HOME`,
  `LANG`, `LC_*`, `USER`) rather than parent-env-minus-scrub.

#### F-SAFE-24 — Container runtime process inherits the full parent environment

- **File:** `internal/sandbox/container.go:96-105`
- **Severity:** LOW
- **Problem:** `buildContainerEnv` returns `nil`, so the Go `exec.Command`
  inherits all host environment variables when launching `docker` /
  `podman`. Host secrets (Docker creds, cloud tokens) are available to
  the runtime process and any credential helpers it invokes.
- **Fix:** Build a minimal, explicit env for the runtime process and pass
  `cmd.Env` explicitly.

#### F-SAFE-25 — `passthrough` backend silently disables all isolation

- **File:** `internal/sandbox/passthrough.go:18-31`,
  `internal/sandbox/sandbox.go:94-120`
- **Severity:** LOW
- **Problem:** `backend = "passthrough"` is accepted and runs commands
  with no sandboxing. The audit meta reports only `Enabled: true,
  Backend: "passthrough"` with no capability flags, but the startup path
  does not warn the user.
- **Fix:** Log a prominent warning at startup and require the user to
  confirm the choice. Consider gating passthrough behind a separate
  `unsafe_passthrough = true` flag.

#### F-SAFE-26 — Process-group termination may leave escaped grandchildren

- **File:** `internal/sandbox/process_unix.go:27-54`
- **Severity:** LOW
- **Problem:** The Unix backend kills by negative PGID. A malicious or
  buggy grandchild that calls `setpgid` to leave the group will survive
  timeout/cancellation.
- **Fix:** After the grace interval, send `SIGKILL` to the direct child
  PID as a fallback. On Linux, consider cgroups or pidfd-based tracking.

#### F-SEC-27 — `SaveUserConfigRule` path construction trusts `os.UserHomeDir`

- **File:** `internal/app/tui/model.go:847, 2155-2160`,
  `internal/app/config/save.go:226-248`
- **Severity:** MEDIUM
- **Problem:** `userConfigDir()` joins `~/.config/marshal` without checking
  whether the path is under a trusted directory. A malicious environment
  that manipulates `$HOME` can redirect user config writes.
- **Fix:** Resolve the absolute path and verify it is under the real home
  directory or a known XDG path; log a warning on mismatch.

#### F-SEC-28 — `replaceTriggerToken` can leave unbalanced `@` tokens

- **File:** `internal/app/tui/model.go:1195-1219`
- **Severity:** LOW
- **Problem:** If the input contains `@@file`, the second `@` is treated
  as a boundary and replaced; the first `@` remains, creating an invalid
  reference. The agent may interpret the leftover `@` as a new file
  reference.
- **Fix:** Skip consecutive `@` characters when locating the file trigger
  boundary.

#### F-SEC-29 — API keys collected by onboarding are displayed in plaintext

- **File:** `internal/app/onboarding.go:37-45, 60-72, 178-185`
- **Severity:** MEDIUM
- **Problem:** The single shared `textInput` is reused for URL and API key
  entry with no masking. Typed keys are visible on screen and may remain
  in terminal scrollback.
- **Fix:** Use a masked input (`textinput.EchoPassword` or a custom mask)
  when collecting API keys.

#### F-SEC-30 — `SaveProjectConfig` writes arbitrary provider config from working copy

- **File:** `internal/app/config/save.go:183-198`
- **Severity:** MEDIUM
- **Problem:** Provider and model presets are persisted exactly as in the
  working config. If a malicious or buggy settings pane injects a
  provider with a `base_url` pointing at an attacker-controlled endpoint,
  it is saved and used on next load.
- **Fix:** Validate URLs and env-var references before saving; reject
  providers whose `base_url` is not a valid HTTP(S) URL.

#### F-SEC-31 — `dispatchCommand` uses `strings.Fields` for slash-arg parsing

- **File:** `internal/app/tui/model.go:1503-1524`
- **Severity:** LOW
- **Problem:** `strings.Fields(raw)` loses quotes and escapes, so
  `/plan "my idea"` becomes `["my", "idea"]`.
- **Fix:** Use a shell-style argument parser (`shlex.Split`) for commands
  that accept quoted arguments, or document the limitation.

#### F-SEC-32 — Skill file content is injected as a system message verbatim

- **File:** `internal/skills/loader.go`, `internal/skills/tool.go:26-68`
- **Severity:** MEDIUM
- **Problem:** `handleSkillLoad` takes `skill.Body` and appends it to the
  session transcript as `RoleSystem`. A skill file with crafted content —
  instructions that override the system prompt, exfiltrate data, or
  include other `tool.load`-style directives — is treated as authoritative
  system text. The skill directory lookup has no signature/verification,
  and a project skill silently overrides a global one (last-wins at
  `loader.go:54`).
- **Fix:** Mark skill bodies as user-supplied content in the prompt
  (wrap in a fenced block the model is told to treat as reference
  material, not instructions); document the trust model; add a `name`
  collision warning rather than silently overwriting.

#### F-SEC-33 — `cwd` and `additionalDirectories` accepted as any absolute path

- **File:** `internal/acp/session.go:149-174`
- **Severity:** MEDIUM
- **Problem:** The only check on `cwd` and each `additionalDirectories`
  entry is `filepath.IsAbs`. An ACP client can pass `/etc`, `/root`,
  `/var/log`, or another user's home directory. The runtime then opens
  the per-cwd database at `<cwd>/.marshal/marshal.db` (creating
  directories as root) and may run tools against that path.
- **Fix:** Resolve the path with `filepath.Clean` and `EvalSymlinks`,
  compare against an explicit allow-list (user's home, `$TMPDIR`, project
  root). Reject paths that escape with `invalidParams`.

#### F-SEC-34 — `ResourceLink` URI is not scheme-validated

- **File:** `internal/acp/protocol.go:99-108, 132-139`
- **Severity:** LOW
- **Problem:** `normalizePrompt` accepts any non-empty `URI` in a
  `resource_link` block. A client can send `javascript:alert(1)`,
  `data:text/html;base64,…`, or `file:///etc/passwd`. The text is included
  verbatim in the prompt and any consumer rendering it as HTML would
  execute the URI.
- **Fix:** Accept only safe schemes (`https:`, `file:` with a path
  constraint, `marshal-resource:`). Reject everything else with
  `invalidParams`.

#### F-SEC-35 — MCP server inherits full parent process environment

- **File:** `internal/tools/mcp/client.go:42-43`
- **Severity:** MEDIUM
- **Problem:** `c.cmd.Env = append(c.cmd.Env, c.Env...)` starts from the
  parent process's full environment, then adds user-supplied vars. Host
  secrets (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GH_TOKEN`, `AWS_*`)
  are passed verbatim to the spawned MCP server, which may log or
  exfiltrate them.
- **Fix:** Build `c.cmd.Env` from a curated base (`PATH`, `HOME`, `LANG`,
  `TZ`, `TMPDIR`) and the user-supplied vars, explicitly omitting
  known-secret env names.

#### F-SEC-36 — `RegisterTools` uses `context.Background()` with no timeout

- **File:** `internal/tools/mcp/manager.go:48-101`
- **Severity:** MEDIUM
- **Problem:** `client.Call(ctx, "tools/list", nil, &res)` uses
  `context.Background()`. A malicious or hung MCP server can block
  registration forever, blocking Marshal startup.
- **Fix:** Accept a context (or use a per-server timeout, e.g. 10s) and
  return an error if `tools/list` doesn't complete. Skip-and-warn for
  individual servers rather than aborting all of `RegisterTools`.

#### F-SEC-37 — `initialize` JSON-RPC error and response structures written verbatim

- **File:** `internal/acp/protocol.go:36-39, 73-95`, `server.go:471-484`
- **Severity:** LOW
- **Problem:** `Error.Message` is set to `err.Error()` for every error,
  including internal server errors, permission-bridge failures, and decode
  errors. The string may include the underlying error's text, leaking
  filesystem paths, internal package names, or stack-trace hints.
- **Fix:** Sanitize error messages for wire output. Map internal errors to
  fixed strings and log the detail server-side.

#### F-SEC-38 — `/export` writes to a user-controlled path

- **File:** `internal/commands/commands.go:402-415`
- **Severity:** LOW
- **Problem:** `path := strings.Join(args, " ")` lets a user write to any
  path the marshal process can write to (e.g. `~/.bashrc`,
  `/tmp/anywhere.html`). The redact flag controls secret redaction in
  the *content* but does not constrain the *location*.
- **Fix:** Clamp the export to the working dir (or a user-confirmed
  absolute path) and reject `..` traversal.

### B. Tools & policy

#### F-BUG-39 — `file.write_patch` cannot create new files

- **File:** `internal/tools/native/file.go:189-197`
- **Severity:** MEDIUM
- **Problem:** During the apply loop the code unconditionally
  `os.ReadFile(path)`. For a new file that passed the dry-run (where
  `os.IsNotExist` caused a `continue`), the read fails and the tool
  errors out.
- **Fix:** Treat a non-existent file as an empty-content starting point
  (provided the patch search block is empty), write the patched result,
  preserve a sensible default mode.

#### F-BUG-40 — Patch parser silently drops malformed blocks

- **File:** `internal/tools/patch/parser.go:17-76`
- **Severity:** LOW
- **Problem:** If a `<<<<<<< SEARCH` is opened but never closed, the
  accumulated lines are discarded and `Parse` returns an empty patch list
  without error.
- **Fix:** Detect unclosed search/replace blocks and return an error
  describing the malformed input. Reject empty `currentPath`.

#### F-BUG-41 — Non-shell edited args leave `argsMap` inconsistent with `args`

- **File:** `internal/agent/runner.go:1118-1131`
- **Severity:** MEDIUM
- **Problem:** When the user edits the command in an approval prompt, the
  shell branch carefully re-marshals `argsMap`. For other tools the code
  does `args = json.RawMessage(edited); normalizedArgs, _ = normalizeArgs(args); _ = json.Unmarshal(args, &argsMap)`. Errors are silently
  discarded. If the user pastes invalid JSON, `args` is the edit but
  `argsMap` is the *original* — `changedFilesForTool` records wrong
  files, `argsMap["command"]` shows the old label.
- **Fix:** Surface the unmarshal error to the user and refuse to execute
  with mismatched args. Re-run policy evaluation against the *new* args.

#### F-BUG-42 — `web.fetch` HTML-to-text entity decoding is incomplete

- **File:** `internal/tools/native/web.go:173-180`
- **Severity:** LOW
- **Problem:** Only a handful of named entities are decoded. Numeric
  entities (`&#39;`, `&#x27;`) and many other named entities remain
  encoded.
- **Fix:** Use `html.UnescapeString` from the standard library.

#### F-BUG-43 — Job output combines stdout and stderr with no separator

- **File:** `internal/tools/native/jobs_manager.go:295-301`
- **Severity:** LOW
- **Problem:** `combined := stdoutStr + stderrStr` concatenates the two
  streams, making it hard to tell where stdout ends and stderr begins.
- **Fix:** Preserve the same `formatCommandOutput` structure used by
  foreground commands (`stdout:\n...\n\nstderr:\n...`).

#### F-POL-44 — Duplicate command guardrail logic between `command.go` and `policy.go`

- **File:** `internal/tools/native/command.go:110-136`,
  `internal/tools/policy/policy.go:25-28, 235-389`
- **Severity:** LOW
- **Problem:** Two independent implementations of conservative checks
  exist. They can drift and produce inconsistent decisions.
- **Fix:** Remove `validateConservativeCommand` and rely solely on the
  policy engine. If a fast-path is needed, have it call the same
  `policy` package helpers.

#### F-POL-45 — Inconsistent logger usage across policy and sandbox

- **File:** `internal/tools/policy/policy.go:369`,
  `internal/sandbox/sandbox.go:112-114`,
  `internal/sandbox/restricted.go:40-46`
- **Severity:** LOW
- **Problem:** `policy.go` uses `slog.Default()`, while `sandbox` accepts
  an injected logger.
- **Fix:** Thread a structured `*slog.Logger` through `PolicyEngine`.

#### F-POL-46 — `LimitsJSON` silently hides marshal failures

- **File:** `internal/tools/registry/types.go:74-102`
- **Severity:** LOW
- **Problem:** If `json.Marshal` of the limits map fails, the function
  returns `"{}"` with no indication of the error.
- **Fix:** Return the error to the caller so the audit layer can log a
  warning.

### C. ACP / MCP / swarm

#### F-BUG-47 — MCP client drops responses whose `id` is a non-numeric string

- **File:** `internal/tools/mcp/client.go:170-187`
- **Severity:** MEDIUM
- **Problem:** Requests are sent with `id = int64(atomic.AddInt64(...))`.
  The pending map is keyed by `interface{}` holding an `int64`. The
  response unmarshaller produces `id` as `float64` (default JSON number);
  the `default: key = v` branch keeps the value as-is (e.g., a string).
  If a server echoes the id as a quoted string, the lookup misses and the
  response is silently dropped. The calling `Call` blocks until context
  cancellation.
- **Fix:** Normalize all id representations to the same Go type on both
  sides.

#### F-BUG-48 — MCP `tools/call` handler ignores `isError` from the server

- **File:** `internal/tools/mcp/manager.go:115-139`
- **Severity:** MEDIUM
- **Problem:** `CallToolResult` carries an `IsError` flag. The handler
  maps `Content` into `Summary`/`Content` and returns
  `(registry.ToolResult{}, nil)` even when the MCP server reported a tool
  error. The agent loop treats a tool failure as success.
- **Fix:** Inspect `res.IsError`; on true, set `Error` on the
  `registry.ToolResult` or return a non-nil error.

#### F-BUG-49 — `publishReplacement` allows double-close of the prior runtime

- **File:** `internal/acp/session.go:505-525`
- **Severity:** MEDIUM
- **Problem:** `publishReplacement` takes `lifecycleMu`, then takes
  `m.mu` to read `prior`, releases `m.mu`, tears down `prior`, and only
  then re-acquires `m.mu` to write the new pointer. Between the two
  `m.mu` sections, a concurrent `publishReplacement` can read the same
  `prior` pointer and call `m.close(prior)` again. `app.Runtime.Close`
  may not be idempotent.
- **Fix:** Hold `m.mu` continuously across the read of `prior` and the
  write of the new pointer; tear down outside the map lock.

#### F-BUG-50 — `CancelAndWait` can return success without the runner actually finishing

- **File:** `internal/acp/turn.go:353-370`
- **Severity:** LOW
- **Problem:** `CancelAndWait` calls `slot.cancel()` and then waits on
  `slot.done`. If the runner goroutine never writes to `runErr` (because
  `Run` ignores `ctx.Done()`), `runErr` never gets a value, the main loop
  is stuck on `<-runErr`, and `slot.done` is never closed. `Close` then
  calls `m.close(rt)` on a still-active runtime.
- **Fix:** Enforce a bounded wait in `CancelAndWait` independent of the
  parent context, and surface a timeout error.

#### F-BUG-51 — `pending.ResponseChan` send in `forward()` ignores runner context

- **File:** `internal/acp/turn.go:252-263`
- **Severity:** LOW
- **Problem:** The forwarder writes `pending.ResponseChan <- answers` in
  a `select` with `<-turnCtx.Done()`. If the runner has abandoned this
  pending question and later reads from `ResponseChan`, the select may
  have already fired `<-turnCtx.Done()` and the answers are lost.
- **Fix:** Track pending-question ownership in the forwarder (one
  outstanding per session), gate the send on a dedicated question-id.

#### F-CON-52 — `deliverOutbound` / `failOutbound` channel send races

- **File:** `internal/acp/server.go:364-387, 391-401`
- **Severity:** MEDIUM
- **Problem:** See F-SEC-12. The combination of buffered-channel send
  with waiter cancellation is the root deadlock.
- **Fix:** Non-blocking sends; close the channel after sending; or use a
  `sync.Once` per-id.

#### F-CON-53 — MCP `readLoop` sends to channels while holding `c.mu`

- **File:** `internal/tools/mcp/client.go:189-201`
- **Severity:** MEDIUM
- **Problem:** After the scanner ends, `readLoop` iterates `c.pending`
  under `c.mu` and calls `ch <- Response{Error: ...}`. If a second write
  to the same id arrives, the send blocks while holding the mutex,
  deadlocking the client.
- **Fix:** Snapshot the entries, then send outside the lock; or use
  non-blocking sends.

#### F-CON-54 — `PermissionBridge.Request` blocks the forwarder goroutine

- **File:** `internal/acp/turn.go:226-264`
- **Severity:** MEDIUM
- **Problem:** The forwarder runs on the main `PromptTurn` goroutine,
  which is also the consumer of the broker subscription. The broker is
  terminal (must-deliver), so `Publish` from the runner blocks until the
  event is consumed. The forwarder calls `m.bridge.Request` which can
  block indefinitely. The runner is blocked on `Publish`, blocked on the
  forwarder. This is a turn-wide deadlock bounded only by editor
  responsiveness.
- **Fix:** Dispatch the permission bridge call in a separate goroutine;
  the main loop continues processing events.

#### F-CON-55 — `Server.fatalErr` drops excess fatal errors silently

- **File:** `internal/acp/server.go:99, 264, 407-412`
- **Severity:** LOW
- **Problem:** `reportFatal` uses `select` with `default` to drop excess
  fatal errors. The dropped error is unrecoverable but unobservable.
- **Fix:** Aggregate fatal errors with `errors.Join` and surface them in
  the final `Serve` return.

#### F-CON-56 — `Server.Serve` shutdown does not wait for `fatalErr` reporting goroutines

- **File:** `internal/acp/server.go:228-276`
- **Severity:** LOW
- **Problem:** Handlers that detect cancellation may still try to
  `reportFatal` after `waitHandlers` returns. The error is then dropped.
- **Fix:** Capture fatal errors via a `sync.Once`-guarded pointer and
  include them in the final return value.

#### F-BUG-57 — `Server.dispatch` does not recover from handler panics

- **File:** `internal/acp/server.go:497-506`
- **Severity:** MEDIUM
- **Problem:** A panic in a handler crashes the entire process. A panic
  in the permission bridge, session lifecycle, or turn management takes
  the whole editor connection down.
- **Fix:** Wrap the handler call in a `defer recover` and return a
  JSON-RPC `internalError` (-32603). Log the panic with the method name.

#### F-POL-58 — Dead code: `ProviderUsageMeter` is an inert wrapper

- **File:** `internal/agent/swarm/meter.go:48-63`
- **Severity:** LOW
- **Problem:** `ProviderUsageMeter` just delegates to `EstimateMeter`. The
  comment says it is wired but dormant. There is no caller.
- **Fix:** Remove until needed, or wire to the provider's real `usage`
  reporting.

#### F-POL-59 — Duplicate JSON unmarshal in `handleFrame`

- **File:** `internal/acp/server.go:319-326`
- **Severity:** LOW
- **Problem:** When `Method == ""` and `ID != nil`, the code unmarshals
  the line a second time as `map[string]json.RawMessage` just to check
  for the `"method"` key.
- **Fix:** Add a `hasMethod bool` flag to the `Request` struct using
  `json.RawMessage` for the method.

#### F-POL-60 — `Server.Serve` scanner buffer size is hard-coded

- **File:** `internal/acp/server.go:282-283`
- **Severity:** LOW
- **Problem:** Initialised with a 1 MiB max line. There is no way to
  override it. A hostile editor that sends a 1 MiB+ line triggers
  `scanner.Err()` and disconnects.
- **Fix:** Make the buffer size a field on `Server`, defaulting to 1 MiB.

#### F-POL-61 — `Server.Request` does not validate the method name

- **File:** `internal/acp/server.go:154-219`
- **Severity:** LOW
- **Problem:** `method` is sent verbatim to the editor. An empty string
  would be sent as `{"method":""}`.
- **Fix:** Return an error if `method == ""`.

#### F-POL-62 — `SessionManager.publishReplacement` ignores the caller's context

- **File:** `internal/acp/session.go:505-525`
- **Severity:** LOW
- **Problem:** The prior runtime is closed with a fresh `shutdownCtx()`
  (5s), not the caller's context. Inconsistent with `Close` which uses a
  fresh ctx anyway.
- **Fix:** Accept a context parameter on `publishReplacement` and use it
  for the prior close.

#### F-POL-63 — `lister.go` `MkdirAll` runs on every `ListSessions` call

- **File:** `internal/acp/lister.go:35-48`
- **Severity:** LOW
- **Problem:** `ListSessions` calls `os.MkdirAll(filepath.Dir(dbPath), 0o755)`
  for every request. A "list" RPC is a side-effecting operation.
- **Fix:** Only create the directory on `DeleteSession`; `ListSessions`
  should open the DB read-only or return "no sessions" if missing.

#### F-POL-64 — `MCPClient.Call` returns raw `client closed` error verbatim

- **File:** `internal/tools/mcp/client.go:105-145`
- **Severity:** LOW
- **Problem:** When the client is closed, `Call` returns `c.err` directly.
  Callers can't distinguish "user closed" from "read loop ended".
- **Fix:** Define `ErrClientClosed` and wrap with method context.

#### F-POL-65 — `validateLifecycleParams` does not resolve symlinks

- **File:** `internal/acp/session.go:162-169`
- **Severity:** LOW
- **Problem:** The 8-entry cap is checked before symlink resolution. An
  attacker can submit 8 symlinks pointing to the same sensitive
  directory.
- **Fix:** Resolve symlinks and de-duplicate by resolved path. Reject if
  outside the allow-list.

#### F-POL-66 — `PerCwdLister` opens a SQLite database per call without pooling

- **File:** `internal/acp/lister.go:35-48`
- **Severity:** LOW
- **Problem:** Each `session/list` call opens, migrates, queries, and
  closes the database.
- **Fix:** Cache the opened `*db.DB` keyed by cwd with a TTL or LRU
  eviction, and run `Migrate` once per process per cwd.

#### F-POL-67 — Swarm `overBudget` check is not atomic with the observe

- **File:** `internal/agent/swarm/orchestrator.go:70-72, 167-202`
- **Severity:** LOW
- **Problem:** `overBudget` reads `meter.Total()` and decides whether to
  start the next round. Between the read and `runRole`, parallel role
  observations can push the total over the budget. The budget is "soft"
  rather than hard.
- **Fix:** Re-check `overBudget` after each role finishes uniformly.

#### F-POL-68 — Swarm `ImplementerPrompt` does not surface tester failure details

- **File:** `internal/agent/swarm/prompts.go:24-26`
- **Severity:** LOW
- **Problem:** Tester failure text is stored in `ts.findings` as a
  free-form string. The implementer has to parse the prose.
- **Fix:** Have the tester publish a structured `[]TestFailure` (file +
  line + test name) and render it as a structured block in the
  implementer prompt.

#### F-POL-69 — `TestSessionLoadUsesExistingSessionOption` opens a real database

- **File:** `internal/acp/session_test.go:647-714`
- **Severity:** LOW (test hygiene)
- **Problem:** The test uses `app.StartRuntime` directly, creating a real
  on-disk database. Mixing real and fake seams makes the test slower and
  less deterministic.
- **Fix:** Use the fake seams (`fakeStartFixed`, `fakeRuntimeStart`) and
  inject a `*db.DB` for the replay path.

### D. Agent runtime & routing

#### F-BUG-70 — Native `ask_user` / `question.ask` do not consume iteration budget

- **File:** `internal/agent/runner.go:1387-1441`
- **Severity:** MEDIUM
- **Problem:** In the envelope (non-native) path, `ActionAskUser` and
  `ActionQuestionAsk` both `iteration++`. The native equivalents never
  call `iteration++`. A native model can ask→decline→ask→decline
  forever and never hit the iteration cap.
- **Fix:** `iteration++` (and stats update) on the native path the same
  way the envelope path does. When the answer is empty (declined), also
  `recordIdle`.

#### F-BUG-71 — `extractPinnedFiles` has no path-safety check beyond the file index

- **File:** `internal/agent/atfile.go:24-71`
- **Severity:** MEDIUM
- **Problem:** `extractPinnedFiles` matches `@(\S+)` and does
  `os.ReadFile(filepath.Join(workingDir, path))` with the only safeguard
  being membership in the file index from `db.GetFileIndex`. The
  function does *not* use the `safeWorkspacePath` helper that
  `patch_preview.go:54` uses. A goal like `@../etc/passwd` is harmless
  only because the file index happens not to contain those paths. The
  regex also matches shell metacharacters into the capture group.
- **Fix:** Route every accepted `path` through `safeWorkspacePath` and
  reject anything that escapes. Tighten the regex to a
  `[A-Za-z0-9._/\-]+` class.

#### F-BUG-72 — `/export` calls `export.Write` before computing the default path

- **File:** `internal/commands/commands.go:402-415`
- **Severity:** LOW
- **Problem:** `path` is empty when `args` is empty; the code passes the
  empty path to `export.Write(state, path, redactOn)` and *afterwards*
  computes the default filename.
- **Fix:** Compute `path` first, then call `export.Write` once.

#### F-BUG-73 — `ResolveRole` returns the wrong error after fallback exhaustion

- **File:** `internal/llm/routing/router.go:33-54`
- **Severity:** LOW
- **Problem:** When both the implementer fallback and `legacyRoute` fail,
  the function returns the *first* call's `errRoleNotConfigured`, not
  the more informative `fallbackErr`.
- **Fix:** Return the more specific of the two errors.

#### F-BUG-74 — `ResponseFormat` mutation leaks across `Run()` calls

- **File:** `internal/agent/runner.go:571-577`
- **Severity:** MEDIUM
- **Problem:** When the model produces two consecutive unparseable
  responses, the runner sets `r.ResponseFormat = &schema.ResponseFormat{Type: "json_object"}` on the shared Runner field. The second call
  to `Run()` on the same Runner inherits the JSON-mode forced format and
  silently changes how *every* model in that role is queried.
- **Fix:** Thread the response format through the loop as a local
  variable inside `RunTask`; pass into `chatOnce`.

#### F-BUG-75 — Hard-coded `"Unanswered"` literal instead of `session.AnswerUnanswered`

- **File:** `internal/agent/runner.go:1431` (compare `runner.go:695`)
- **Severity:** LOW
- **Problem:** `executeNativeQuestionAsk` compares `a.Answer != "Unanswered"`
  as a raw string while the envelope path uses `session.AnswerUnanswered`.
  If the sentinel is ever changed, the native path silently treats
  every answer as "not unanswered".
- **Fix:** `a.Answer != session.AnswerUnanswered`.

#### F-BUG-76 — Serial-tool batch short-circuits the rest of the serial queue

- **File:** `internal/agent/runner.go:1483-1494`
- **Severity:** MEDIUM
- **Problem:** If the first serial tool errors, the function returns
  immediately with `results[i] = nil` for the failing tool and `results[j] = nil` for all un-executed serial tools. The model that
  issued a batch of e.g. 3 questions sees a single error and no
  answers.
- **Fix:** Execute every serial tool even after one errors, or
  substitute a per-tool "failed: <err>" `Tool` message for missing
  slots.

#### F-BUG-77 — `extractJSONObject` uses `Index`/`LastIndex` of `{`/`}` — fragile

- **File:** `internal/agent/protocol.go:116-128`
- **Severity:** MEDIUM
- **Problem:** Takes everything between the first `{` and the last `}` in
  the model's response. A response like `Sure! Here is the JSON: {"a": 1, "b": {"nested": true}} some trailing text {not really json}` is
  treated as one giant object, which fails `json.Unmarshal` with a
  confusing error and burns a parse-failure slot.
- **Fix:** Find the first complete balanced JSON object (stack-based).

#### F-BUG-78 — `/diff` adds the diff to the state transcript as a system message

- **File:** `internal/commands/commands.go:279-305`
- **Severity:** LOW
- **Problem:** `/diff` calls `r.State.AddMessage(session.RoleSystem, diff, session.ContentTypeDiff)` then returns `""`. The TUI renders the
  diff as if it were a system event and re-injects it into model
  context on the next chat call.
- **Fix:** Return the diff as the command's response string and skip
  the state injection, or only inject on explicit user request.

#### F-CON-79 — `Runner` field access is partially synchronised

- **File:** `internal/agent/runner.go:188-193` and throughout
- **Severity:** MEDIUM
- **Problem:** `tracker`, `stats`, and `ForceClass` have dedicated
  mutexes, but other mutable fields — `r.ResponseFormat`,
  `r.MaxTurnContextTokens`, `r.Role`, `r.WriteGate`, `r.Snapshotter`,
  `r.Policy`, `r.Provider`, `r.Registry`, `r.State` — are read and
  written without locking. The Runner's own comment doesn't state
  "single-Run-only". The test harness reuses the same `*Runner` across
  many `RunTask` calls.
- **Fix:** Document `Runner` as not safe for concurrent `Run()` calls on
  the same instance, and audit every field mutation.

#### F-CON-80 — Goroutine leak risk on `requestQuestions` / `requestApproval`

- **File:** `internal/agent/runner.go:1553-1579, 1595-1616`
- **Severity:** LOW
- **Problem:** Both functions buffer the response channel and block on
  `<-ctx.Done()` or the channel. If the TUI goroutine that normally
  drains the channel exits (e.g. on shutdown) and the channel is *not*
  sent to, the agent goroutine blocks on `<-tc.ResponseChan` forever
  — pinned with no wider shutdown signal. With the default
  `r.RequestTimeout = 0`, a TUI that closes without sending a decision
  would leak a goroutine.
- **Fix:** Have the runner wrap the `select` with a `time.After` that
  escalates to `ctx.Err()` and explicitly closes/nils the pending slot.

#### F-CON-81 — `extractPinnedFiles` re-reads file index and disk for every match

- **File:** `internal/agent/atfile.go:24-71`
- **Severity:** LOW
- **Problem:** Called from the hot loop (once per turn on `RunTask`
  startup, plus once per drained steering message). Sequential I/O on
  the agent goroutine.
- **Fix:** Cache the file index in `Runner` (invalidated when
  `state.DB()` reports a re-scan), parallelise reads with a bounded
  semaphore.

#### F-SEC-82 — Tool-argument rewriting after user approval can swap a safe call for a risky one

- **File:** `internal/agent/runner.go:1211-1265`
- **Severity:** MEDIUM
- **Problem:** The rewrite loop is hard-bounded at 1 rewrite. On the
  second iteration, a `pre_tool_use` hook can turn a user-approved
  `git status` into a `rm -rf /tmp/foo`. The user is re-prompted (the
  intended mitigation), but the audit event uses the *last* hook's
  metadata — so a post-approval rewrite is recorded without the fact
  that the user approved something different.
- **Fix:** Include the *original* approved args in the audit event
  alongside the rewritten args, and log a "rewrote" flag; consider
  requiring an explicit "re-approve the rewritten form" step in the TUI.

#### F-POL-83 — Pinned sections can be dropped by budget because they're appended last

- **File:** `internal/contextpack/builder.go:48-69, 182-216`
- **Severity:** MEDIUM
- **Problem:** `PinFiles` appends pinned sections to `pack.Sections` and
  sets `Priority=100`, but `buildPackFromSections` processes sections
  in slice order and never sorts by priority. The doc comment at
  line 47 says "pinning means they are not dropped by the greedy
  rebudget pass", but the implementation does not honour that.
- **Fix:** Sort candidates by `Priority DESC` (stable) before
  processing, or process the pinned bucket first against a reserved
  slice of the budget.

#### F-POL-84 — Multiple `/…` commands are empty placeholders

- **File:** `internal/commands/commands.go:144-214`
- **Severity:** MEDIUM
- **Problem:** `/stop`, `/swarm`, `/sdd`, `/model`, `/settings`,
  `/memory`, `/ask`, `/edit`, `/auto`, `/mode` all have handlers that
  return a hard-coded string and do nothing else. Several are
  advertised in `/help` with non-trivial descriptions. The mode
  commands (`/ask`, `/edit`, `/auto`) print "Switched to X mode" but no
  mode state is changed.
- **Fix:** Either implement the commands or hide them from `/help` until
  they exist.

#### F-POL-85 — `Runner.ResponseFormat` and `MaxTurnContextTokens` mutations persist across `Run()` calls

- **File:** `internal/agent/runner.go:575-577, 839-841`
- **Severity:** MEDIUM
- See F-BUG-74. The hidden state on the Runner after a single
  parse-failure escalation or a route resolution makes the Runner
  non-reentrant in a way that surprises test code.

#### F-POL-86 — `legacyRoute` returns a `Route` with empty `ContextBudget` and `Capabilities`

- **File:** `internal/llm/routing/router.go:100-114`
- **Severity:** LOW
- **Problem:** Downstream code reads `route.ContextBudget.MaxRepoContextTokens` (used at `runner.go:346`) and other fields. A
  legacy route silently disables the budget cap and any other
  context-budget-derived behaviour. The runner's catalog-based
  context-window fallback treats the legacy model as "unknown" and
  never raises the budget.
- **Fix:** Derive at least a sane default `MaxRepoContextTokens`; skip
  legacy in the catalog or include a stub entry.

#### F-POL-87 — `summarizeAndContinue` failure path leaves state/model diverged

- **File:** `internal/agent/runner.go:466-474`
- **Severity:** LOW
- **Problem:** When `summarizeAndContinue` returns an error, the code
  calls `compactMessages(...)` in-place. The compacted slice is sent
  to the model on the next call, but the session's message list is
  unchanged. Subsequent `buildHistoryMessages` calls replay the
  *uncompacted* prior transcript alongside the now-compacted live
  messages — the model sees the same content twice.
- **Fix:** On `summarizeAndContinue` failure, also call
  `r.State.SetContextPack(...)` after rebuilding the pack from the
  compacted slice, or skip the lossy fallback and surface a
  user-visible error.

#### F-POL-88 — Silent error swallowing in policy re-evaluation after edit

- **File:** `internal/agent/runner.go:1118-1131`
- See F-BUG-41.

#### F-POL-89 — `requestQuestions` activity label omits the question

- **File:** `internal/agent/runner.go:1604`
- **Severity:** LOW
- **Problem:** `r.State.SetActivity(... Label: "waiting for your answer")`
  — the user sees the activity but not *which* question.
- **Fix:** Include the first 40 chars of the first question (or
  "Q1/N: …") in the label.

#### F-POL-90 — Inconsistent trimming between `buildCandidateSections` and `PinFiles`

- **File:** `internal/contextpack/builder.go:147-163 vs 50-67`
- **Severity:** LOW
- **Problem:** Both paths agree on the skip rule but the trim happens
  at different points.
- **Fix:** Factor the trim/skip into a single helper.

#### F-POL-91 — Skills `Risk` default is a bare string

- **File:** `internal/skills/skill.go:93`
- **Severity:** LOW
- **Problem:** `fm.Risk = "read_only"` is a string literal; the rest of
  the codebase uses `registry.RiskReadOnly` as the canonical value.
- **Fix:** Define `const DefaultSkillRisk = registry.RiskReadOnly`.

#### F-POL-92 — `runner_test.go` is 106 KB / 3000+ lines

- **File:** `internal/agent/runner_test.go`
- **Severity:** LOW
- **Problem:** Many test seams are private functions; the test file
  must construct end-to-end scenarios to exercise narrow code paths.
- **Fix:** Extract helpers into a smaller `toolExecutor` or
  `chatDispatcher` struct that can be replaced in tests; or move
  narrow tests to `*_internal_test.go`.

#### F-POL-93 — `RunTaskFunc` defined twice with the same comment

- **File:** `internal/agent/runner.go:186, 195-198`
- **Severity:** LOW
- **Problem:** The inline struct field declaration is dead; the named
  type is the single declaration.
- **Fix:** Drop the inline anonymous type on the field.

#### F-POL-94 — `SteeringProvider` interface is one method, used in only one call site

- **File:** `internal/agent/steering.go:6-8`
- **Severity:** LOW
- **Problem:** A whole interface for a single method. The interface
  indirection adds no value (no test fake, no alternate
  implementation).
- **Fix:** Inline the method on `session.State` and drop the interface.

#### F-POL-95 — `callerMustResetPressure` comment is misleading

- **File:** `internal/agent/runner.go:468`
- **Severity:** LOW
- **Problem:** The comment explains *why* `pressureSent` is reset, not
  what the variable tracks. Future readers will look for a
  `callerMustResetPressure` symbol that doesn't exist.
- **Fix:** Rename the variable to `pressureMessageSent` and update the
  comment.

#### F-POL-96 — `tests/cases` and swarm subdirs duplicate runner test seams

- **File:** `internal/agent/swarm/*_test.go`, `internal/agent/sdd/*_test.go`
- **Severity:** LOW
- **Problem:** Each subpackage re-implements small stubs for providers,
  registries, etc.
- **Fix:** Extract common stubs to an internal test package
  (e.g. `internal/agent/agenttest`).

### E. DB / repo scanner / symbols

#### F-BUG-97 — `repo.index` ignores configured ignore patterns and `.gitignore`

- **File:** `internal/tools/native/repo_index.go:31`
- **Severity:** HIGH
- **Problem:** `repo.NewScanner(repo.Config{Root: t.root})` is
  constructed without passing `t.config.Indexing.Ignore` or
  `SkipGitignore`. The scanner therefore does not apply user-configured
  ignore patterns and always honors `.gitignore`. Inconsistent with
  the config schema and can cause giant `vendor/`, `node_modules/`, or
  generated directories to be indexed.
- **Fix:** Pass `Ignore: t.config.Indexing.Ignore` and the skip flag.

#### F-BUG-98 — Gitignore parser treats anchored directory patterns as unanchored

- **File:** `internal/repo/gitignore.go:52-72`
- **Severity:** MEDIUM
- **Problem:** When a pattern ends with `/` (e.g. `build/`), the
  trailing slash is stripped *before* the "contains `/`" anchored check
  is performed. A bare `build/` becomes unanchored and matches
  `src/build/output.js` in addition to `build/`.
- **Fix:** Check for a slash (or leading slash) before stripping the
  trailing slash, or preserve anchoring explicitly when `dirOnly` is
  true and the original line had no leading slash.

#### F-BUG-99 — Gitignore implementation does not support negation (`!pattern`)

- **File:** `internal/repo/gitignore.go:21-97`
- **Severity:** MEDIUM
- **Problem:** Lines beginning with `!` are treated as literal
  patterns rather than negation rules. A `.gitignore` containing
  `*.log` followed by `!important.log` will incorrectly ignore
  `important.log`.
- **Fix:** Parse negation rules, track the last matching pattern's
  polarity, and only ignore if the final match is positive.

#### F-BUG-100 — `FindSymbols` LIKE pattern is not escaped

- **File:** `internal/db/symbols.go:97-99`
- **Severity:** MEDIUM
- **Problem:** User-provided `name` is wrapped in `%...%` and passed
  to `LIKE` without escaping. The underscore `_` is a SQL LIKE
  wildcard, so searching for `foo_bar` also matches `fooXbar`.
- **Fix:** Escape LIKE wildcards and add `ESCAPE '\'` to the query.

#### F-BUG-101 — `FilesMatchingBasename` LIKE pattern is not escaped

- **File:** `internal/db/files.go:127-140`
- **Severity:** MEDIUM
- **Problem:** The `basename` argument is interpolated into
  `"%"+basename+"%"` and used with `LIKE`. Underscores and percent
  signs are interpreted as wildcards.
- **Fix:** Escape `%` and `_` and add `ESCAPE '\'`.

#### F-BUG-102 — `Scanner` follows symlinks out of the workspace

- **File:** `internal/repo/scanner.go:72-127`
- **Severity:** HIGH
- **Problem:** `filepath.WalkDir` follows directory symlinks by
  default. The scanner only skips non-regular files *after*
  descending, so a symlink to an arbitrary directory outside the
  project root will be traversed and its files indexed/hashed.
- **Fix:** Detect symlinks via `entry.Type().IsRegular()` and skip
  them, or use `Lstat` semantics and skip `os.ModeSymlink` entries.

#### F-BUG-103 — `SaveSnapshot` inserts snapshot files outside the snapshot row's transaction

- **File:** `internal/db/snapshots.go:10-29`
- **Severity:** MEDIUM
- **Problem:** The `snapshots` row is inserted using `db.sqlDB.Exec`,
  then each `snapshot_files` row is inserted with separate
  `db.sqlDB.Exec` calls. With `MaxOpenConns(1)` these are serialized
  but not atomic. If an error occurs after the snapshot row is
  inserted, the parent row exists without children.
- **Fix:** Use a transaction (`Begin`, `Commit`, `Rollback`) wrapping
  both inserts.

#### F-BUG-104 — `LatestSnapshot` returns `id`/`hash` with partial file list on error

- **File:** `internal/db/snapshots.go:31-55`
- **Severity:** LOW
- **Problem:** On error while querying `snapshot_files`, the function
  returns the already-fetched `id` and `hash` alongside the error.
- **Fix:** Return zero/empty values on error.

#### F-BUG-105 — `MessagesOnBranch` builds an `IN (...)` query that can exceed SQLite limits

- **File:** `internal/db/sessions.go:183-261`
- **Severity:** MEDIUM
- **Problem:** For very long branches the `ids` slice grows large and
  the final query uses one placeholder per id. SQLite has a default
  limit of 999 host parameters.
- **Fix:** Use a temporary table or a CTE with `VALUES` rows, or batch
  the `IN` clause into chunks.

#### F-BUG-106 — `PruneSnapshotsOlderThan` accepts negative `days`

- **File:** `internal/db/snapshots.go:70-81`
- **Severity:** LOW
- **Problem:** Negative `days` computes a cutoff in the future
  (`AddDate(0,0,-days)`), deleting snapshots that are not actually
  old.
- **Fix:** Validate `days < 0` and return an error, or clamp to `0`.

#### F-BUG-107 — `GetOrCreateProject` relies on `LastInsertId() == 0` to detect updates

- **File:** `internal/db/projects.go:68-94`
- **Severity:** LOW
- **Problem:** Brittle assumption; the driver behavior may change.
- **Fix:** Use `RETURNING id` (SQLite 3.35+) or always follow the
  upsert with `SELECT id FROM projects WHERE root_path = ?`.

#### F-BUG-108 — `SaveToolCall` does not serialize `Sandbox.Enabled`

- **File:** `internal/db/audits.go:31-77`
- **Severity:** MEDIUM
- **Problem:** `Enabled` is inferred on read from
  `sandbox_backend != ""`, but a sandbox with an empty backend string
  will round-trip as `Enabled=false` even if the original was true.
- **Fix:** Add a `sandbox_enabled INTEGER` column and persist
  `boolToInt(event.Sandbox.Enabled)`.

#### F-BUG-109 — `SaveToolCall` drops `ResourceLimits` and `OutputTruncated` flags

- **File:** `internal/db/audits.go:48-52, 164-203`
- **Severity:** LOW
- **Problem:** `ResourceLimits` and `OutputTruncated` are stored
  inside `sandbox_limits_json` but the read path does not restore
  them. Audit accuracy for output truncation is lost on round-trip.
- **Fix:** Add deserialization for `resource_limits` and
  `output_truncated` in `GetToolCalls`.

#### F-BUG-110 — `ExtractSymbols` uses `context.Background()` and ignores cancellation

- **File:** `internal/repo/symbols.go:23`
- **Severity:** LOW
- **Problem:** The function signature does not accept a context; it
  always parses with `context.Background()`.
- **Fix:** Change `ExtractSymbols` to accept `ctx context.Context`.

#### F-BUG-111 — `repo.index` re-reads files from disk after scanning

- **File:** `internal/tools/native/repo_index.go:49-65`
- **Severity:** MEDIUM
- **Problem:** Files are discovered and hashed in `scanner.Scan()`,
  then immediately re-read with `os.ReadFile(filepath.Join(t.root, f.Path))`
  for symbol extraction. If a file is a symlink to a sensitive file,
  or is modified between scan and read, the symbol extraction operates
  on different bytes than those hashed.
- **Fix:** Reject non-regular files and record file metadata during
  scan; optionally open the same resolved path with `O_NOFOLLOW`
  semantics.

#### F-PERF-112 — `SaveSymbols` and `SaveFileIndex` delete-then-insert one row at a time

- **File:** `internal/db/symbols.go:22-51`, `internal/db/files.go:25-82`
- **Severity:** MEDIUM
- **Problem:** Both methods `DELETE` all project rows and then loop
  over the input, executing one prepared `INSERT` per row. With many
  files/symbols this is much slower than a multi-row `INSERT` or
  `UPSERT`.
- **Fix:** Batch inserts (e.g. build
  `INSERT INTO ... VALUES (?,...),(?,...)...` with `sqlbuilder` or a
  small helper), or use a temporary table + `INSERT ... SELECT`.

#### F-PERF-113 — `GetSymbols` and `GetFileIndex` return unbounded result sets

- **File:** `internal/db/symbols.go:55-80`, `internal/db/files.go:85-121`
- **Severity:** MEDIUM
- **Problem:** `repo.map` calls `GetSymbols(projectID)` and
  `GetFileIndex(projectID)` without limits.
- **Fix:** Add limit/offset variants, or cap `GetSymbols` to
  exported/top-level symbols for the map.

#### F-PERF-114 — Missing index on `files(project_id, path)` and `symbols(project_id)`

- **File:** `internal/db/migrations.go:12-21`
- **Severity:** MEDIUM
- **Problem:** `files` has `UNIQUE(project_id, path)` (which
  implicitly creates an index), but `GetFileIndex` only filters by
  `project_id`. `symbols` only has `idx_symbols_project_name`;
  queries like `GetSymbols(projectID)` are full table scans.
- **Fix:** Add `CREATE INDEX IF NOT EXISTS idx_symbols_project ON symbols(project_id);`
  and `CREATE INDEX IF NOT EXISTS idx_files_project ON files(project_id);`.

#### F-PERF-115 — `MessagesOnBranch` performs N+1 queries

- **File:** `internal/db/sessions.go:183-223`
- **Severity:** MEDIUM
- **Problem:** For each message on a branch it runs a separate
  `SELECT parent_id` query, then a second query for the full rows.
- **Fix:** Fetch all ancestor rows in a single recursive CTE
  (`WITH RECURSIVE`) or walk parent IDs in memory after loading the
  full session once.

#### F-PERF-116 — `ListSessions` uses correlated subqueries for counts and max date

- **File:** `internal/db/sessions.go:370-380`
- **Severity:** LOW
- **Problem:** The query contains two correlated subqueries per row.
  For projects with many sessions/messages this is slower than a join
  or pre-aggregation.
- **Fix:** Consider a join against
  `(SELECT session_id, MAX(created_at) updated_at, COUNT(*) message_count FROM messages GROUP BY session_id)`.

#### F-PERF-117 — `repo.search` reads every file before checking result caps

- **File:** `internal/tools/native/search.go:73-111`
- **Severity:** MEDIUM
- **Problem:** `searchFiles` walks the entire tree and collects all
  file paths into a slice before searching any of them. It also opens
  files without checking whether the remaining cap is already
  reached.
- **Fix:** Stop the walk once `len(matches) >= limit`, and avoid
  materializing the full file list.

#### F-PERF-118 — `repo.search` and `repo.index` have no file size limits

- **File:** `internal/tools/native/search.go:113-146`,
  `internal/repo/scanner.go:134-146`
- **Severity:** MEDIUM
- **Problem:** `searchFile` sets a 1 MiB scanner token buffer but
  will still attempt to read arbitrarily large files. `hashFile` reads
  entire files. A multi-gigabyte log or binary file will be fully read
  into memory/hashed.
- **Fix:** Cap files at a configurable max size; skip files over the
  cap with a warning.

#### F-PERF-119 — `DB.Open` uses a single connection with no WAL mode

- **File:** `internal/db/db.go:14-43`
- **Severity:** MEDIUM
- **Problem:** `SetMaxOpenConns(1)` serializes all DB access,
  including reads. Combined with default `journal_mode=DELETE`,
  read-heavy operations block behind writes.
- **Fix:** Enable WAL mode (`PRAGMA journal_mode=WAL`) and allow a
  small read pool while keeping writes serialized.

#### F-SEC-120 — `SaveFileIndex` and `SaveSymbols` lack project-level isolation

- **File:** `internal/db/files.go:25-82`, `internal/db/symbols.go:22-51`
- **Severity:** MEDIUM
- **Problem:** Both methods read existing rows, delete them, then
  insert new rows. With `MaxOpenConns(1)` they are serialized, but if
  the pool is widened, two concurrent `repo.index` calls for the same
  project could interleave and lose data.
- **Fix:** Acquire a project-level mutex or use advisory locks; document
  the exclusive-access assumption.

#### F-SEC-121 — `tableColumns` builds dynamic SQL from its argument

- **File:** `internal/db/db.go:162-184`
- **Severity:** LOW
- **Problem:** `fmt.Sprintf("PRAGMA table_info(%s)", table)` interpolates
  the table name directly. The caller is internal and table names
  are constants today; if `tableColumns` is ever exported or reused
  with external input, it becomes SQL injectable.
- **Fix:** Validate `table` against an explicit allowlist.

#### F-SEC-122 — `resolveWorkspacePathMulti` does not evaluate symlinks

- **File:** `internal/tools/native/helpers.go:55-82`
- **Severity:** MEDIUM
- **Problem:** The function checks `filepath.Rel(root, full)` for
  `..` traversal but does not resolve symlinks. A path like
  `allowed/link-to-root/etc/passwd` where `allowed/link-to-root` is a
  symlink can resolve outside the workspace after symlink resolution.
- **Fix:** Resolve symlinks on the final resolved path and verify it
  still lies within a root.

#### F-SEC-123 — `repo.search` does not re-verify every match

- **File:** `internal/tools/native/search.go:73-146`
- **Severity:** MEDIUM
- **Problem:** `searchFiles` walks from `start` (resolved against
  roots), but does not re-verify that every discovered file is still
  within a root.
- **Fix:** Verify each file path with `workspaceRel` before reading.

#### F-SEC-124 — `files_changed` JSON not validated/normalized

- **File:** `internal/db/audits.go:21-23, 67-68`
- **Severity:** LOW
- **Problem:** Paths with control characters or backslashes can
  corrupt the audit trail.
- **Fix:** Normalize paths with `filepath.ToSlash` before marshaling.

#### F-POL-125 — `joinSnapshotFiles` is dead code

- **File:** `internal/db/snapshots.go:101-104`
- **Severity:** LOW
- **Problem:** Defined but never called; the schema stores files in a
  normalized `snapshot_files` table.
- **Fix:** Remove the function.

#### F-POL-126 — `DB.exec` and `DB.queryRow` are thin wrappers with no added value

- **File:** `internal/db/db.go:186-192`
- **Severity:** LOW
- **Problem:** Forward only to `db.sqlDB`. They add indirection and
  prevent use of context-aware variants.
- **Fix:** Remove the wrappers or add context support.

#### F-POL-127 — `SaveFileIndex` manually closes `rows` on errors

- **File:** `internal/db/files.go:37-54`
- **Severity:** LOW
- **Problem:** The code calls `rows.Close()` explicitly on scan/iterate
  errors.
- **Fix:** Replace manual closes with `defer rows.Close()`.

#### F-POL-128 — `FindSymbols` and `GetSymbols` duplicate scan logic

- **File:** `internal/db/symbols.go`
- **Severity:** LOW
- **Problem:** The row-scanning loop is duplicated.
- **Fix:** Extract a `scanSymbol(rows *sql.Rows) (Symbol, error)`
  helper.

#### F-POL-129 — `repo.map` documents `maxFiles` confusion

- **File:** `internal/repo/map.go:65-83`
- **Severity:** LOW
- **Problem:** `renderNode` increments `fileCount` for every file but
  only prints files under `maxFiles`. Directory entries are not
  counted against the cap, but the comment could clarify.
- **Fix:** Document the behaviour.

#### F-POL-130 — `Scanner` does not distinguish symlink from non-regular

- **File:** `internal/repo/scanner.go:106-108`
- **Severity:** MEDIUM
- **Problem:** A directory symlink is followed (security issue);
  a symlink to a regular file is silently skipped (surprising).
- **Fix:** Explicitly handle symlinks: skip or reject with a clear
  reason.

#### F-POL-131 — `repo.index` does not surface `.gitignore` parse errors gracefully

- **File:** `internal/repo/scanner.go:52-62`
- **Severity:** LOW
- **Problem:** A malformed root `.gitignore` fails the entire
  `repo.index` call instead of falling back to no gitignore rules.
- **Fix:** Decide whether a bad `.gitignore` is fatal; if not, log a
  warning and continue.

#### F-POL-132 — `FilesMatchingBasename` matches by substring, not basename

- **File:** `internal/db/files.go:123-127`
- **Severity:** LOW
- **Problem:** Uses `path LIKE %basename%`. Docstring says "basename"
  but the query matches any substring.
- **Fix:** Clarify the docstring or change the query to anchor on
  path separators.

#### F-POL-133 — `Symbol` and `FileIndex` structs defined in `db` but used heavily by `repo`

- **File:** `internal/db/symbols.go:8-17`, `internal/db/files.go:9-16`
- **Severity:** LOW
- **Problem:** Tight coupling between `repo` and `db` for pure data
  types.
- **Fix:** Move the model types to a neutral package
  (e.g. `internal/repoindex`) or accept the coupling.
  Decision recorded in `docs/15-data-model-decisions.md` ADR-001; no code change.

#### F-POL-134 — `DetectLanguage` misses many common extensions

- **File:** `internal/repo/language.go:29-38`
- **Severity:** LOW
- **Problem:** No support for `.vue`, `.svelte`, `.scala`, `.clj`,
  `.ex`, `.exs`, `.erl`, `.hrl`, `.cs`, `.fs`, `.lhs`, etc.
- **Fix:** Expand the map or fall back to a more general detection
  mechanism.

#### F-POL-135 — `RecentTurnMetrics` accepts `limit <= 0`

- **File:** `internal/db/turnmetrics.go:76-87`
- **Severity:** LOW
- **Problem:** A caller passing `0` produces `LIMIT 0`. A negative
  value would generate an invalid SQL error.
- **Fix:** Apply the same default/clamp pattern as `FindSymbols`.

#### F-POL-136 — `todos.go` does not wrap errors with context

- **File:** `internal/db/todos.go`
- **Severity:** LOW
- **Problem:** Errors from `json.Marshal`, `Exec`, `QueryRow`, and
  `json.Unmarshal` are returned raw.
- **Fix:** Wrap errors with `fmt.Errorf("save todos: %w", err)` etc.

### F. TUI / session / onboarding

#### F-UIUX-137 — Onboarding API key written to disk verbatim when it lacks underscores

- **File:** `internal/app/onboarding.go:254-260`
- **Severity:** HIGH
- See F-SEC-08. Listed here under UIUX because the fix is also a flow
  change (add env-var name step).

#### F-UIUX-138 — Footer hints omit several active shortcuts

- **File:** `internal/app/tui/help/help.go:34-68`
- **Severity:** MEDIUM
- **Problem:** The persistent footer never hints `Ctrl+G`, `Ctrl+R`,
  `Ctrl+X`, `PgUp/PgDn`, or `Ctrl+U/Ctrl+D`, even though these are
  frequently useful.
- **Fix:** Include the next most relevant shortcuts with progressive
  disclosure.

#### F-UIUX-139 — Help overlay uses misaligned columns

- **File:** `internal/app/tui/help/help.go:72-96`
- **Severity:** LOW
- **Problem:** Key and description columns are hand-aligned with
  varying leading spaces; narrow terminals clip the right side
  without wrapping, and the alignment drifts when labels differ in
  length.
- **Fix:** Render as a two-column table with a fixed-width key column
  and wrapped descriptions.

#### F-UIUX-140 — Approval chooser overrides `Esc` in a non-obvious way

- **File:** `internal/app/tui/approval.go:120-125`
- **Severity:** MEDIUM
- **Problem:** `Esc` always denies the pending tool. Users may press
  `Esc` intending to return to the transcript and accidentally deny.
- **Fix:** Keep `Esc` as "deny" but require a visible confirmation
  when a destructive deny would abort a long-running agent turn, or
  flash a short-lived message explaining the action.

#### F-UIUX-141 — Question form does not auto-focus the first input

- **File:** `internal/app/tui/question.go:111-113`
- **Severity:** MEDIUM
- **Problem:** `newQuestionModel` calls `Init()` but immediately
  discards the returned `cmd`. The cursor may not blink/focus until
  the first key event.
- **Fix:** Return the `Init()` command from `newQuestionModel` and
  surface it as the first command when the question form is opened in
  `model.go`, or explicitly call `Focus()` on the first field.

#### F-UIUX-142 — Settings save button is invisible when blocked

- **File:** `internal/app/tui/settings/model.go:285-293`
- **Severity:** MEDIUM
- **Problem:** `SetSaveBlocked` only sets `footerMsg`. There is no
  visual change to the diff overlay's save control, so users pressing
  `Ctrl+S` just see a transient footer message and may not understand
  why save did nothing.
- **Fix:** In the diff overlay, render the save instruction as
  dimmed/disabled with the reason inline.

#### F-UIUX-143 — Memory browser does not refresh after marking stale/confirmed

- **File:** `internal/app/tui/memory/model.go:114-128`
- **Severity:** LOW
- **Problem:** `setConfidence` updates the DB and the local slice
  item, but the view only re-renders on the next `Update`. If the
  user marks an item stale there is no immediate feedback.
- **Fix:** Set a brief `footer` confirmation and ensure the view
  re-renders immediately, or publish a memory-updated event that the
  TUI can react to.

#### F-UIUX-144 — Browser bar and activity strip duplicate status information

- **File:** `internal/app/tui/browserbar.go:11-38`,
  `internal/app/tui/view.go:66-85, 181-188`,
  `internal/app/tui/status.go`
- **Severity:** LOW
- **Problem:** The browser bar and the right-side status segment
  both show the active URL/tool, and the activity strip repeats the
  current tool label.
- **Fix:** Dedupe: hide the right status URL segment when the
  browser bar is visible; show the activity strip only when the
  status line does not already describe the same activity.

#### F-UIUX-145 — SDD panel renders even when zero tasks visible

- **File:** `internal/app/tui/sdd_panel.go:22-42`
- **Severity:** LOW
- **Problem:** The panel always reserves `sddPanelRows = 10` rows via
  `m.sddPanelRows()`, but `renderSDDPanel` clamps its own output to at
  most 10 rows. Unused reserved rows leave dead space below the
  panel.
- **Fix:** Compute actual rendered height from the panel content and
  return it from `renderSDDPanel`.

#### F-UIUX-146 — Completion popup does not show `/` after accepting a command

- **File:** `internal/app/tui/completions.go:203-221`
- **Severity:** LOW
- **Problem:** The popup does not visually indicate that `/` is part
  of the accepted token when the user keeps typing arguments.
- **Fix:** Consider keeping a small hint that `/` is the command
  prefix if the input is just the command word.

#### F-BUG-147 — `handleApproval` / `handleQuestion` not reached when settings/memory overlay is open

- **File:** `internal/app/tui/model.go:476-489, 550-563`
- **Severity:** MEDIUM
- **Problem:** If the user opens settings while a tool is pending
  approval, the approval form is hidden and keypresses go to
  settings; the agent remains blocked waiting for a decision.
- **Fix:** Either block opening settings/memory when a pending
  approval/question exists, or resume the form automatically when
  the overlay closes. Add a status message explaining the pending
  decision.

#### F-BUG-148 — `BeginWork` is not called for approval/question response handling

- **File:** `internal/app/tui/model.go:796-808, 921`
- **Severity:** MEDIUM
- **Problem:** When the user answers an approval/question, the model
  writes to `tc.ResponseChan` synchronously in `Update`. If the
  agent is currently quiescing/shutting down, this can send a
  decision after `ResolvePendingForShutdown` has already sent a
  default deny, causing the runner to receive conflicting decisions.
- **Fix:** Check `m.state.PendingApproval()` /
  `PendingQuestion()` are still the same object before sending, and
  skip the send if nil.

#### F-BUG-149 — `cancelTurn` clears steering queue without checking if the agent actually cancelled

- **File:** `internal/app/tui/model.go:1338-1349`
- **Severity:** MEDIUM
- **Problem:** `cancelTurn` calls `m.agentCancel()` and immediately
  clears the steering queue and adds a system message. If the agent
  finishes between `m.agentCancel()` and the queue clear, valid
  follow-up messages are lost.
- **Fix:** Only clear steering and add the cancellation message
  after confirming `busy` was true and `agentCancel` was non-nil.
  Consider racing against `agentFinishedMsg` by setting a
  `cancelling` flag.

#### F-BUG-150 — `handleAgentFinished` does not reset `successPulse` if the agent errored

- **File:** `internal/app/tui/model.go:1390-1407`
- **Severity:** LOW
- **Problem:** When `msg.err != nil`, the code sets provider error
  but does not clear `successPulse`. If a previous turn succeeded
  within the last 2 seconds, the input border still shows teal
  success during the error state.
- **Fix:** Set `m.successPulse = false` whenever `msg.err != nil`.

#### F-BUG-151 — `updateViewportHeight` ignores activity strip when SDD is active

- **File:** `internal/app/tui/model.go:989-996`
- **Severity:** LOW
- **Problem:** `inputAreaRows()` includes `activityStripRows` only
  when `m.state.Activity().Kind != ActivityIdle`. During SDD,
  `renderInputArea` inserts a hint line but activity is idle, so
  `inputAreaRows()` undercounts by one row and the viewport
  overlaps the hint.
- **Fix:** Add the SDD hint row to `inputAreaRows()` when
  `SDDProgress().Active` is true.

#### F-BUG-152 — `transcriptHash` ignores message content

- **File:** `internal/app/tui/model.go:2069-2085`
- **Severity:** MEDIUM
- **Problem:** The viewport dirty hash only considers timestamp,
  count, width, and flags. If two messages have identical timestamps
  but different content (possible on fast systems with nanosecond
  precision collisions, or after editing a command), the viewport
  will not re-render.
- **Fix:** Include a hash of item content/role and the in-progress
  reasoning text in the hash.

#### F-BUG-153 — `settingsBlockReason` does not consider pending approval/question or picker

- **File:** `internal/app/tui/model.go:1368-1376`
- **Severity:** MEDIUM
- **Problem:** Save is only blocked by `m.busy` and
  `RunningJobsCount`. A pending approval or open picker is not
  considered "busy", so settings can be saved while the user is
  mid-decision.
- **Fix:** Include `PendingApproval`, `PendingQuestion`, and
  overlay-open states in the block reason.

#### F-BUG-154 — `Run()` passes the same `opts` slice to onboarding and runtime

- **File:** `internal/app/app.go:680`
- **Severity:** LOW
- **Problem:** `opts` are iterated twice: once in `Run` and again
  inside `StartRuntime`. `WithWorkingDir` is resolved twice and
  could drift.
- **Fix:** Document that options are idempotent; consider constructing
  a single resolved `options` struct.

#### F-BUG-155 — `BeginQuiesce`/`WaitForWork` race allows new work after quiesce starts

- **File:** `internal/app/session/session.go:791-834`
- **Severity:** MEDIUM
- **Problem:** `BeginWork` checks `s.quiescing` under `workMu`, then
  `s.workWG.Add(1)`. `BeginQuiesce` sets `quiescing` under `workMu`.
  A caller could check `quiescing=false`, then lose the CPU,
  quiesce starts, and the caller still adds to the WaitGroup because
  there is no second check after acquiring the lock for `Add`.
- **Fix:** Hold `workMu` across both the check and the `Add`, or
  recheck `quiescing` after `Add` and call `Done` if it changed.

#### F-BUG-156 — `SetPendingApproval` publishes a shallow copy that still shares `ResponseChan`

- **File:** `internal/app/session/session.go:1202-1212`
- **Severity:** MEDIUM
- **Problem:** The copy created for the event shares the original
  `ResponseChan` pointer. Subscribers that read the event cannot
  safely interact with the channel, and closing the channel in
  `ResolvePendingForShutdown` may race with the TUI sending on it.
- **Fix:** Do not expose `ResponseChan` in events; publish an event
  type that omits the channel.

#### F-BUG-157 — `pubsub.Broker` drops events on full buffer but `handleJobCount` relies on exact counts

- **File:** `internal/pubsub/broker.go:94-121`,
  `internal/app/tui/model.go:1411-1423`
- **Severity:** MEDIUM
- **Problem:** Job events use best-effort delivery with drop-head.
  If job count events are dropped, the TUI status line can show a
  stale count until the next event arrives.
- **Fix:** Mark the TUI job subscription terminal (`WithTerminal`)
  so every count update is delivered, or poll `RunningJobsCount` on
  each tick.

#### F-BUG-158 — `OnboardingModel` value receiver vs `*Model` return

- **File:** `internal/app/onboarding.go:116-229`
- **Severity:** LOW
- **Problem:** `Update` returns `(tea.Model, tea.Cmd)` with `m` as
  a value copy. Because `OnboardingModel` holds reference types,
  mutations in `Update` modify the shared objects, but value-copy
  semantics are subtly wrong for Bubble Tea v2 and could cause lost
  state if any plain field is added later.
- **Fix:** Use a pointer receiver consistently.

#### F-BUG-159 — `loadFileIndexPaths` uses `memoryDB` for file index

- **File:** `internal/app/app.go:856-869`,
  `internal/app/tui/model.go:1130-1150`
- **Severity:** LOW
- **Problem:** The lazy file-index path treats `memoryProject == 0`
  as "no way to populate", but the eager seed can also fail
  silently. When the DB has no file index, `@` shows no results
  with no explanation.
- **Fix:** When `@` is triggered and `populateFileIndexIfNeeded`
  returns no items, show a single disabled popup row such as "No
  indexed files — run /index".

#### F-BUG-160 — `m.pickerModel.Update(msg)` returns only a command, not a model

- **File:** `internal/app/tui/model.go:530-531`
- **Severity:** LOW
- **Problem:** The comment "route key messages to the picker"
  doesn't clarify that the picker is pointer-updated.
- **Fix:** Keep current behavior; improve the comment.

#### F-BUG-161 — `OnboardingModel` does not validate the Ollama response status code

- **File:** `internal/app/onboarding.go:82-114`
- **Severity:** LOW
- **Problem:** `fetchOllamaModels` reads the body without checking
  `resp.StatusCode`. A 500 response with a JSON body could be parsed
  and returned as models.
- **Fix:** Check `resp.StatusCode == http.StatusOK` before parsing.

#### F-POL-162 — `forceMode` is set but never rendered in help overlay

- **File:** `internal/app/tui/model.go:81, 1714-1715`
- **Severity:** LOW
- **Problem:** The comment says `forceMode` is reserved for future
  status-bar display. The help overlay lists modes without showing
  the current selection.
- **Fix:** Add a small hint in the help overlay or remove the
  obsolete comment.

#### F-POL-163 — `permissionForTool` is dead code

- **File:** `internal/app/tui/model.go:2087-2096`
- **Severity:** LOW
- **Problem:** Maps tool names to permission strings but never
  called.
- **Fix:** Remove and use `permissions.PermissionForTool`
  consistently.

#### F-POL-164 — `inputAreaRows` counts raw newlines in wrapped content

- **File:** `internal/app/tui/model.go:931-966`
- **Severity:** LOW
- **Problem:** `len(strings.Split(content, "\n"))` gives the number
  of logical lines, but wrapped lines inside the content may
  consume more terminal rows.
- **Fix:** Use `lipgloss.Height(content)` after rendering.

#### F-POL-165 — `renderCompletionPopup` recomputes `max` and `offset` every render

- **File:** `internal/app/tui/view.go:252-298`
- **Severity:** LOW
- **Problem:** The popup window math is duplicated from
  `completionPopup.reconcileOffset`. Both should agree; if they
  drift, the selected item can be hidden.
- **Fix:** Move all offset/index clamping into the popup model.

#### F-POL-166 — `activeTheme` global reloaded by `loadTheme` but styles not recomputed

- **File:** `internal/app/tui/model.go:1974-2060`
- **Severity:** LOW
- **Problem:** Existing `lipgloss.Style` instances held by other
  packages (e.g. `picker`, `memory`) may still use the old theme
  because they captured it at import time.
- **Fix:** Ensure all theme-dependent packages read `theme.Load()`
  lazily in their `View` functions, or reload theme at a single
  site and propagate a theme-changed event.

#### F-POL-167 — `warmSunsetTheme` in `huhtheme` is loaded at import time

- **File:** `internal/app/tui/huhtheme/theme.go:15, 24-77`
- **Severity:** LOW
- **Problem:** `theme.Load()` reads `$TERM` and `$NO_COLOR` once at
  package initialization. If the user changes these after startup
  (via `/settings` or environment), the huh surfaces won't reflect
  it.
- **Fix:** Call `theme.Load()` inside `WarmSunset()` on each
  invocation.

#### F-POL-168 — `OnboardingModel.saveConfig` hardcodes project name

- **File:** `internal/app/onboarding.go:238-239`
- **Severity:** LOW
- **Problem:** The generated config always contains
  `name = "my-project"` even though the wizard could have asked for
  a project name.
- **Fix:** Add a project-name step to onboarding or derive the
  name from the working directory basename.

#### F-POL-169 — `stateDone` check after onboarding is brittle if user quits via Ctrl+C

- **File:** `internal/app/app.go:675-677`
- **Severity:** LOW
- **Problem:** Does not distinguish "completed" from "cancelled" in
  logs or tests.
- **Fix:** Return an explicit sentinel error or log an info message
  when onboarding is cancelled.

#### F-POL-170 — `renderSwarmPanel` prints blank lines for missing roles

- **File:** `internal/app/tui/swarm_panel.go:48-59`
- **Severity:** LOW
- **Problem:** The loop always emits 5 newlines, printing empty
  rows when fewer than 5 roles are active.
- **Fix:** Only emit newlines for existing roles.

#### F-POL-171 — `truncateURL` uses byte length instead of rune/visible width

- **File:** `internal/app/tui/browserbar.go:44-60`
- **Severity:** LOW
- **Problem:** `len(raw)` and `raw[:max]` operate on bytes, so URLs
  containing multibyte characters may be truncated incorrectly.
- **Fix:** Use `[]rune` or `ansi.StringWidth`/`ansi.Cut` for
  width-aware truncation.

#### F-POL-172 — `transcript.go` glamour renderer cache never evicts entries

- **File:** `internal/app/tui/transcript.go:43-66`
- **Severity:** LOW
- **Problem:** `mdRenderers` maps width to renderer. On many
  resizes the map grows unbounded.
- **Fix:** Bound the cache size or clear it when width changes by
  more than a threshold.

#### F-POL-173 — `model.go` imports `patch` only for `patternForApproval`

- **File:** `internal/app/tui/model.go:35, 2106-2116`
- **Severity:** LOW
- **Problem:** The TUI package imports `patch` and `permissions` for
  approval pattern logic that arguably belongs in the
  policy/permissions package.
- **Fix:** Move `patternForApproval` and `commonDir` into
  `internal/permissions` or `internal/tools/policy`.

#### F-POL-174 — `help.go` footer hint for approval says "Enter×2"

- **File:** `internal/app/tui/help/help.go:43`
- **Severity:** LOW
- **Problem:** The wording confuses users. The form also supports
  direct `a`/`d`/`e` keys in the parent `Update`? No, those are not
  wired in `handleApproval`; only `Up/Down/j/k/Enter/Esc` are
  handled.
- **Fix:** Either wire `a`, `d`, `e` as shortcuts in `handleApproval`
  or change the footer to "Enter: arm, Enter again: confirm, Esc:
  deny, ↑↓/j/k: choose".

#### F-POL-175 — `runtime.go` comment says "closeFns (log file)" but includes arbitrary functions

- **File:** `internal/app/runtime.go:263-266`
- **Severity:** LOW
- **Problem:** Comment implies only the log file is closed via
  `closeFns`, but any future function could be appended.
- **Fix:** Rename to `resourceClosers` or document that functions
  are appended in setup order and closed in reverse.

### G. Cross-cutting concerns

#### F-XCUT-176 — No structured logging in ACP or MCP layers

- **File:** all ACP and MCP files
- **Severity:** LOW
- **Problem:** Errors are returned but not logged. When a permission
  request times out or an MCP server is slow, operators have no
  observability.
- **Fix:** Accept a `*slog.Logger` via the config seams (`NewServer`,
  `NewManager`, `NewSessionManager`) and log at structured levels for
  handler dispatches, outbound requests, MCP connect/list/call, and
  shutdown.

#### F-XCUT-177 — Concurrency: many shared `Runner` fields mutated without locking

- **File:** `internal/agent/runner.go` and `internal/acp/turn.go`
- **Severity:** MEDIUM
- **Problem:** See F-CON-79. Test harnesses reuse `*Runner` across
  many `RunTask` calls; the swarm sets the same `WriteGate` and
  `Snapshotter` on multiple role runners. The TUI thread reading
  `r.State.X` while the agent goroutine runs implicitly relies on
  the session-level mutex.
- **Fix:** Audit every field mutation; document `Runner` as not safe
  for concurrent `Run()` calls.

#### F-XCUT-178 — DB `MaxOpenConns(1)` serializes reads; no WAL mode

- **File:** `internal/db/db.go:14-43`
- **Severity:** MEDIUM
- See F-PERF-119. With WAL mode, many findings above (F-PERF-115,
  F-PERF-116) can be addressed in a single change.

#### F-XCUT-179 — Symlink/EvalSymlinks missing in every workspace path resolver

- **File:** `internal/tools/native/helpers.go`, `internal/repo/scanner.go`,
  `internal/tools/native/search.go`
- **Severity:** HIGH
- See F-SEC-03, F-SEC-19, F-SEC-102, F-SEC-122, F-SEC-123. A single
  helper `safeResolve(root, rel) (abs, error)` that combines
  `filepath.Clean`, `EvalSymlinks`, and re-containment would close
  multiple findings at once.

#### F-XCUT-180 — Snapshot writes not transactional across sites

- **File:** `internal/db/snapshots.go:10-29`
- **Severity:** MEDIUM
- See F-BUG-103. A common transactional pattern would also help
  F-PERF-112 (batched inserts).

#### F-XCUT-181 — Command-allow patterns don't match the full parsed command

- **File:** `internal/app/tui/model.go:2098-2118`,
  `internal/tools/policy/policy.go:25-28, 235-260`,
  `internal/tools/native/command.go:110-136`
- **Severity:** HIGH
- See F-SEC-07, F-SEC-16. A unified command-allow match function
  taking the full argv would close multiple findings.

#### F-XCUT-182 — Env-var allow-list missing in three places

- **File:** `internal/sandbox/restricted.go:95-137`,
  `internal/sandbox/container.go:96-105`,
  `internal/tools/mcp/manager.go:25-30`,
  `internal/tools/mcp/client.go:42-43`
- **Severity:** MEDIUM
- See F-SAFE-23, F-SAFE-24, F-SEC-05, F-SEC-35. A single
  `envutil.AllowList(parent []string) []string` would close all four.

#### F-XCUT-183 — Tool-arg edit path silently discards errors

- **File:** `internal/agent/runner.go:1118-1131`
- **Severity:** MEDIUM
- See F-BUG-41, F-POL-88.

#### F-XCUT-184 — Snapshotter/policy reload leaves inconsistent state

- **File:** `internal/app/app.go:776-842`
- **Severity:** HIGH
- See F-BUG-15.

#### F-XCUT-185 — Several advertised `/` commands are empty stubs

- **File:** `internal/commands/commands.go:144-214`
- **Severity:** MEDIUM
- See F-POL-84.

#### F-XCUT-186 — Goroutine-leak risks whenever a TUI consumer may exit

- **File:** `internal/agent/runner.go:1553-1579, 1595-1616`,
  `internal/acp/turn.go:226-264`
- **Severity:** MEDIUM
- See F-CON-80, F-CON-54.

#### F-XCUT-187 — `Runner.ResponseFormat` and `MaxTurnContextTokens` are mutable Runner fields

- **File:** `internal/agent/runner.go:571-577, 832-845`
- **Severity:** MEDIUM
- See F-BUG-74, F-POL-85.

#### F-XCUT-188 — `huh` form completion can race with explicit cancel

- **File:** `internal/app/tui/approval.go:115-164`,
  `internal/app/tui/question.go:138-162`
- **Severity:** HIGH
- See F-BUG-14.

#### F-XCUT-189 — `app.Runtime.Close` may be called on still-active runtime

- **File:** `internal/acp/turn.go:353-370`, `internal/acp/session.go:505-525`
- **Severity:** MEDIUM
- See F-BUG-50, F-BUG-49.

#### F-XCUT-190 — `pubsub` drop semantics are unsafe for job-count updates

- **File:** `internal/pubsub/broker.go:94-121`
- **Severity:** MEDIUM
- See F-BUG-157.

#### F-XCUT-191 — Database connection pool tuning for read/write contention

- **File:** `internal/db/db.go:14-43`
- **Severity:** MEDIUM
- See F-PERF-119. Combined with F-XCUT-178.

## Summary table

| Severity | Count |
|---|---|
| CRITICAL | 1 (F-SEC-01) |
| HIGH | 14 (F-SEC-02 .. F-BUG-15) |
| MEDIUM | ~55 |
| LOW / POLISH | ~120 |
| **Total** | **~191** |

### Highest-leverage fixes (smallest blast radius, biggest impact)

The following are recommended as the first implementation batch because
they close many findings with a single change:

1. **F-SEC-03 / F-SEC-102 / F-SEC-122 / F-SEC-123** — one
   `safeResolve()` helper closes 4 symlink findings.
2. **F-SEC-05 / F-SEC-35 / F-SAFE-23 / F-SAFE-24** — one
   `envutil.AllowList()` closes 4 env-leak findings.
3. **F-BUG-14 / F-BUG-148 / F-BUG-156** — one `sync.Once`/per-id
   responder closes 3 channel-send findings.
4. **F-SEC-07 / F-SEC-16** — one argv-aware command-allow match
   closes 2 command-pattern findings.
5. **F-PERF-119 / F-XCUT-178 / F-XCUT-191** — WAL mode + read pool
   closes 3 DB-perf findings.
6. **F-BUG-100 / F-BUG-101** — one `escapeLike()` helper closes 2
   SQL-wildcard findings.
7. **F-BUG-103 / F-XCUT-180** — one transactional snapshot insert
   closes 2 transactional findings.
8. **F-POL-84 / F-XCUT-185** — hide or implement the 10 empty
   `/` commands; closes 1 user-trust finding.
9. **F-BUG-74 / F-POL-85 / F-XCUT-187** — thread
   `ResponseFormat` / `MaxTurnContextTokens` as locals; closes 2
   state-leak findings and unblocks the test suite.

## Open / unresolved

All findings in this document are **open** unless explicitly marked
resolved. A future implementation batch should:

1. Pick a finding ID (e.g. F-SEC-01).
2. Implement the suggested fix in a focused branch.
3. Add or update tests covering the failure mode.
4. Mark the finding as resolved in this document, with a reference
   to the implementing commit.

Suggested next step: dispatch a follow-up `subagent-driven-development`
batch to fix the 15 high-severity findings first, then the MEDIUM
findings grouped by cross-cutting concern (see "Highest-leverage
fixes" above).

---

## Resolution status (updated 2026-07-22)

### Batch 1 (A1 — sandbox execution hardening): RESOLVED on branch `feature/domain-a-security-and-sandbox`

| Finding | Status | Notes |
|---|---|---|
| F-SAFE-20 | RESOLVED | `cfg.Tools.Shell.AllowSudo` / `AllowDestructive` now consulted in `evaluateGuardrails`; reason text changes but decision stays `DecisionDeny` |
| F-SAFE-21 | RESOLVED | `guardrailVerdict.destructive` field; destructive patterns set the flag |
| F-SAFE-23 | RESOLVED | `restricted` `buildEnv` nil branch now uses `envutil.AllowList` |
| F-SAFE-24 | RESOLVED | `container` `buildContainerEnv` uses `AllowList` + docker runtime var overlay |
| F-SAFE-25 | RESOLVED | `passthrough` requires `unsafe_passthrough = true` opt-in |
| F-SAFE-26 | RESOLVED | `terminateProcessTree` SIGKILLs direct child PID as fallback |
| F-SEC-17 | PARTIAL | Container invokes shell-free commands as argv; full `ClassifyCommand` integration in A2 (Task 3.6) |
| F-SEC-35 | RESOLVED | MCP client `buildChildEnv` filters by `IsDangerousKey` / `IsSecretKey` |
| F-SEC-36 | RESOLVED | `RegisterTools` per-server 10s timeout, skip-and-warn |

### Batch 1 incidental fixes

- F-SEC-16 (chmod -R/--recursive gap): closed by removing the substring pattern and using argv-aware `hasRecursiveFlag`

### Batch 2 (A3 — workspace path safety): PENDING
### Batch 3 (A2 — command classification overhaul): PENDING

### Batch 2 (A3 — workspace path safety): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-19 | RESOLVED | `repo.search` skips symlinks; walk errors collected |
| F-SAFE-22 | RESOLVED | `file.write_patch` apply loop re-checks ModTime before write |
| F-BUG-39 | RESOLVED | `file.write_patch` supports new file creation; non-empty SEARCH rejected |
| F-SEC-122 | RESOLVED | `resolveWorkspacePath`/`Multi` verify symlink containment |
| F-SEC-123 | RESOLVED | `repo.search` re-verifies each match via `workspaceRel`; symlinks skipped |
| F-SEC-102 | RESOLVED | `repo.Scanner` explicitly skips symlinks |

### Batch 3 (A2 — command classification overhaul): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-01 | RESOLVED | `shell.run` (native) and container backend use argv path for shell-free, non-destructive commands |
| F-SEC-07 | RESOLVED | TUI "always allow" pattern is now the full argv |
| F-SEC-16 | RESOLVED | `policy.ClassifyCommand` is argv-aware; substring patterns retained as per-stage safety net |
| F-SEC-17 | RESOLVED | Container argv path gated on `ClassifyCommand` |
| F-SEC-31 | RESOLVED | Slash commands use `shlex.Split` for quoted args |

### Batch 4 (E1 — DB integrity, query correctness, code hygiene): RESOLVED on branch `feature/domain-e-db-repo-symbols`

| Finding | Status | Notes |
|---|---|---|
| F-POL-126 | RESOLVED | DB.exec / DB.queryRow wrappers removed; callers use sqlDB directly |
| F-POL-125 | RESOLVED | joinSnapshotFiles removed |
| F-POL-127 | RESOLVED | SaveFileIndex uses defer rows.Close() |
| F-POL-128 | RESOLVED | scanSymbol helper extracted |
| F-POL-136 | RESOLVED | todos.go errors wrapped |
| F-BUG-103 | RESOLVED | SaveSnapshot is transactional; snapshot_files.path has CHECK (length > 0) |
| F-BUG-104 | RESOLVED | LatestSnapshot returns zero values on scan/iter error |
| F-BUG-106 | RESOLVED | PruneSnapshotsOlderThan rejects days < 0 |
| F-BUG-107 | RESOLVED | GetOrCreateProject always SELECTs id after upsert |
| F-BUG-135 | RESOLVED | RecentTurnMetrics clamps limit <= 0 |
| F-SEC-121 | RESOLVED | tableColumns table names are allowlisted; negative tests added |
| F-SEC-124 | RESOLVED | SaveToolCall normalizes FilesChanged via filepath.ToSlash |
| F-BUG-108 | RESOLVED | sandbox_enabled / resource_limits / output_truncated columns added; round-trip preserved; legacy-row Enabled fix prevents silent overwrite |
| F-BUG-109 | RESOLVED | ResourceLimits / OutputTruncated read back from new columns |
| F-PERF-114 | RESOLVED | idx_files_project, idx_symbols_project added |
| F-BUG-105 | RESOLVED | MessagesOnBranch uses recursive CTE; no IN-clause limit |
| F-BUG-115 | RESOLVED | Same change eliminates N+1 |
| F-BUG-116 | RESOLVED | ListSessions joins against aggregated CTE |

### Batch 5 (C — ACP/MCP/swarm, part 1): RESOLVED (merged 32e5168)

Plan: `docs/superpowers/plans/2026-07-15-domain-c-acp-mcp-swarm.md` (Tasks 1-7)

| Finding | Status | Notes |
|---|---|---|
| F-BUG-47 | RESOLVED | MCP `id` normalized to a single Go type on both send and receive |
| F-CON-53 | RESOLVED | `readLoop` no longer sends to channels while holding `c.mu` |
| F-BUG-48 | RESOLVED | `tools/call` handler surfaces server `isError` as an error |
| F-POL-64 | RESOLVED | `MCPClient.Call` wraps the raw `client closed` error |
| F-CON-52 | RESOLVED | `deliverOutbound` / `failOutbound` channel send races closed |
| F-CON-55 | RESOLVED | `Server.fatalErr` no longer drops excess fatal errors silently |
| F-CON-56 | RESOLVED | `Server.Serve` shutdown waits for fatalErr reporting goroutines |
| F-BUG-57 | RESOLVED | `Server.dispatch` recovers from handler panics |
| F-POL-59 | RESOLVED | Duplicate JSON unmarshal in `handleFrame` removed |
| F-POL-60 | RESOLVED | `Server.Serve` scanner buffer size no longer hard-coded |
| F-POL-61 | RESOLVED | `Server.Request` validates the method name |

### Batch 6 (C — ACP/MCP/swarm, part 2): RESOLVED (merged 32e5168)

Plan: `docs/superpowers/plans/2026-07-15-domain-c-acp-mcp-swarm.md` (Tasks 8-13)

| Finding | Status | Notes |
|---|---|---|
| F-BUG-50 | RESOLVED | `CancelAndWait` only returns success after the runner actually finishes |
| F-BUG-51 | RESOLVED | `pending.ResponseChan` send in `forward()` respects the runner context |
| F-CON-54 | RESOLVED | `PermissionBridge.Request` no longer blocks the forwarder goroutine |
| F-BUG-49 | RESOLVED | `publishReplacement` double-close of the prior runtime fixed |
| F-POL-62 | RESOLVED | `SessionManager.publishReplacement` honours the caller's context |
| F-POL-65 | RESOLVED | `validateLifecycleParams` resolves symlinks |
| F-POL-63 | RESOLVED | `lister.go` `MkdirAll` no longer runs on every `ListSessions` call |
| F-POL-66 | RESOLVED | `PerCwdLister` no longer opens a SQLite database per call |
| F-POL-58 | RESOLVED | Dead `ProviderUsageMeter` wrapper removed; `EstimateMeter` is the only meter |
| F-POL-67 | RESOLVED | Swarm `overBudget` check made atomic with the observe |
| F-POL-68 | RESOLVED | `ImplementerPrompt` surfaces structured tester failure details |
| F-POL-69 | RESOLVED | `TestSessionLoadUsesExistingSessionOption` uses fake seams instead of a real DB |

### Batch 7 (D1 — Runner state hygiene & reentrancy): RESOLVED on branch `feature/domain-d-agent-runtime`

| Finding | Status | Notes |
|---|---|---|
| F-BUG-74 | RESOLVED | `ResponseFormat` threaded as local `effectiveRF` in `RunTask`; not mutated on shared Runner. New test `TestResponseFormatResetsAcrossRunTaskCalls` |
| F-POL-85 | RESOLVED | Same fix as F-BUG-74 (duplicate finding); covers both mutation sites |
| F-CON-79 | RESOLVED | `Runner` doc comment declares the concurrency contract (not concurrent, sequential-reuse safe); persistent vs per-turn fields enumerated; new test `TestRunnerSequentialReuse` |
| F-POL-93 | RESOLVED | Dead inline `RunTaskFunc` declaration removed; named type at line 234 is the single source of truth |
| F-POL-95 | RESOLVED | `pressureSent` → `pressureMessageSent`; comment now describes what the variable tracks |

### Batch 8 (D2 — Native tool execution correctness): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-BUG-70 | RESOLVED | `executeNativeAskUser` and `executeNativeQuestionAsk` now `iteration++` and `recordIdle` on decline; mirror the envelope path. `iterationBudget` pointer field on Runner set in `RunTask`, nil outside |
| F-BUG-75 | RESOLVED | Hard-coded `"Unanswered"` literal replaced with `session.AnswerUnanswered` constant |
| F-BUG-76 | RESOLVED | Serial-tool execution continues after one tool errors; one `Tool` message produced per issued call (including error slots) |
| F-BUG-77 | RESOLVED | `extractJSONObject` rewritten as stack-based balanced-brace scanner; string-aware and escape-aware. 9 sub-tests in `TestExtractJSONObjectBalanced` |
| F-CON-80 | RESOLVED | `requestApproval`/`requestQuestions` get a `time.After(effectiveRequestTimeout)` arm; default 5 minutes. Returns `ErrRequestTimedOut` and clears pending slot |
| F-POL-89 | RESOLVED | `buildQuestionLabel` includes first question preview; "Q1/N: …" format for multi-question. Rune-aware truncation |

### Batch 9 (D3 — Tool approval re-evaluation & rewrite audit): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-82 | RESOLVED | `registry.AuditEvent` gains additive `OriginalArgs json.RawMessage` and `Rewritten bool` fields. Rewrite loop captures pre-hook args before each hook call. DB columns `original_args_json` and `rewritten` (additive migration) round-trip via `sql.Null*`. New test `TestAuditEventRecordsOriginalArgs` with `onceRewriteHookRunner` |
| F-POL-88 | RESOLVED | Same site as F-BUG-41; the non-shell branch (already tightened in D2) re-evaluates policy against new args and surfaces errors via `BuildCorrectionMessage`-style nudge |
| F-BUG-41 | RESOLVED | Shell edit branch now logs `slog.Default().Warn` for silently-discarded `normalizeArgs` and `json.Marshal` failures. New test `TestRunnerShellEditNormalizesSuccessfully` |

### Batch 10 (D4 — `@file` / pinned-files safety & contextpack): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-BUG-71 | RESOLVED | `extractPinnedFiles` regex tightened to `[A-Za-z0-9._/\-]+`; paths routed through `safeWorkspacePath` for `..` containment. 3 new tests cover dotdot, shell metachars, and valid paths |
| F-CON-81 | RESOLVED | New `fileIndexCache` type with projectID-keyed memoisation; bounded 4-goroutine semaphore for parallel file reads. New test `TestFileIndexCache` |
| F-POL-83 | RESOLVED | Pinned sections (Priority ≥ 100) processed first against reserved budget; regular sections preserve input order. New tests `TestPinnedSectionSurvivesBudgetPressure` and `TestRebudgetPutsPinnedSectionsFirst` |
| F-POL-90 | RESOLVED | Single `trimSectionContent` helper used by both `PinFiles` and `buildCandidateSections`. New test `TestTrimSectionContentHelper` |
| D3 followup | RESOLVED | `TestGetToolCalls_LegacyRows` now asserts `OriginalArgs == nil` and `Rewritten == false` for legacy rows |

### Batch 11 (D5 — Routing & state lifecycle): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-BUG-73 | RESOLVED | `ResolveRole` returns the fallback error (more specific) when both primary and legacy paths fail. New test `TestResolveRoleReturnsFallbackErrorOnExhaustion` |
| F-POL-86 | RESOLVED | `legacyRoute` now returns a `Route` with `MaxRepoContextTokens=8000` and `MaxConversationTokens=4000`. New test `TestLegacyRouteHasSaneDefaults` |
| F-POL-87 | RESOLVED | `summarizeAndContinue` failure now terminates the turn with a clear error rather than continuing with lossy in-place compaction. New test `TestSummarizeAndContinueFailureSkipsLossyFallback` |
| D1 followup | RESOLVED | Runner struct doc comment now describes the monotonic-growth behaviour of `resolveRoute` for `MaxTurnContextTokens` |

### Batch 12 (D6 — Slash command & export fixups): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-POL-84 | RESOLVED | `Command` struct gains additive `Hidden bool` field; 12 unimplemented stub commands marked hidden and excluded from `/help` listing (still runnable via `Lookup`). New tests `TestHelpHidesUnimplementedCommands` and `TestHiddenCommandsStillRunnable` |
| F-BUG-78 | RESOLVED | `/diff` returns the diff string instead of injecting it as a `ContentTypeDiff` system message. TUI renders via `ContentTypePlain` system-notice path. New test `TestDiffDoesNotInjectIntoState` |
| F-BUG-72 | RESOLVED | `/export` computes the default path before calling `export.Write`. New test `TestExportComputesPathBeforeWrite` |
| F-POL-91 | RESOLVED | `skills.DefaultSkillRisk` constant (in `internal/skills/skill.go`) wraps `string(registry.RiskReadOnly)`. New test `TestParseFrontmatterDefaultRiskMatchesRegistryConstant` |
| D5 followup | RESOLVED | `handoff_test.go:67` comment updated to reflect new terminate-on-failure contract |

### Batch 13 (D7 — Test infrastructure refactor): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-POL-92 | RESOLVED | `runner_test.go` (3000+ lines) split into 7 concern-specific files (approval, askuser, context, hooks, misc, parallel, parse) plus a `runner_testhelpers_test.go`. Test count preserved (95 → 95) |
| F-POL-96 | RESOLVED | `ScriptedProvider` extracted to new `internal/agent/agenttest/` package; swarm tests migrated from local copies. `swarm/provider_test.go` deleted |
| F-POL-94 | RESOLVED | `SteeringProvider` interface inlined onto `session.State.DrainSteering()`; `internal/agent/steering.go` deleted; `Runner.SteeringProvider` field removed |

### Batch 14 (D8 — deferred-item cleanup): RESOLVED

| Item | Status | Notes |
|---|---|---|
| F-POL-91 followup | RESOLVED | `DefaultSkillRisk` constant promoted to package-level (skill.go:13, declared at line 17); bare `string(registry.RiskReadOnly)` in `skill.go:101` replaced |
| `fileIndexCache.invalidate()` | RESOLVED | Dead method removed; auto-invalidation via `projectID` key covers all cases |
| Runner doc comment | RESOLVED | `fileIndexCache` added to the persistent-fields list |
| Dead `case session.ContentTypeDiff` | RESOLVED | Render path and `renderDiffBlock` helper removed from `transcript.go`; 2 corresponding tests removed from `transcript_test.go`; `diffview` import removed |
| Duplicate `extractJSONObject` | RESOLVED | New `internal/jsonextract` package owns the balanced-brace scanner; `internal/agent` and `internal/knowledge` both call `jsonextract.Extract` and wrap the error with their own sentinel. `TestParseExtractionHandlesBalancedBracesInStrings` is a regression test for the old fragile behaviour |
| `buildQuestionLabel` byte-slicing | RESOLVED | New `truncateRunes` helper is rune-aware; test `TestBuildQuestionLabel/long_question_with_multi-byte_characters_truncates_on_rune_boundary` is a regression test |

### Batch 15 (E2 — repo scanner, gitignore, language & indexing): RESOLVED on branch `feature/domain-e-db-repo-symbols`

| Finding | Status | Notes |
|---|---|---|
| F-BUG-100 | RESOLVED | FindSymbols uses escapeLike and ESCAPE '\' |
| F-BUG-101 | RESOLVED | FilesMatchingBasename uses escapeLike and ESCAPE '\' |
| F-POL-132 | RESOLVED | FilesMatchingBasename now anchors on path separator |
| F-POL-134 | RESOLVED | DetectLanguage extended with .vue / .svelte / .scala / etc. |
| F-BUG-98 | RESOLVED | gitignore trailing-slash patterns stay anchored |
| F-BUG-99 | RESOLVED | gitignore supports !negation with last-pattern-wins semantics |
| F-POL-131 | RESOLVED | Bad gitignore is logged as a warning; Scan continues |
| F-BUG-97 | RESOLVED | repo.index passes Indexing.Ignore to Scanner |
| F-BUG-110 | RESOLVED | ExtractSymbols takes context.Context |
| F-BUG-111 | RESOLVED | repo.index uses Scanner.ScanDetailed to avoid disk re-reads |
| F-POL-129 | RESOLVED | RenderDirectoryMap doc comment clarifies maxFiles |
| F-POL-133 | RESOLVED (ADR) | Coupling accepted; see docs/15-data-model-decisions.md |

### Batch 16 (E3 — DB & search performance / scaling / isolation): RESOLVED on branch `feature/domain-e-db-repo-symbols`

| Finding | Status | Notes |
|---|---|---|
| F-PERF-119 | RESOLVED | SQLite WAL + small read pool; existing on-disk DBs upgraded transparently |
| F-PERF-112 | RESOLVED | SaveSymbols / SaveFileIndex use multi-row VALUES batches of 200 |
| F-PERF-113 | RESOLVED | GetSymbols / GetFileIndex take an optional limit; repo.map passes repoMapMaxFiles |
| F-PERF-118 | RESOLVED | Indexing.MaxIndexableFileBytes / MaxSearchableFileBytes caps added |
| F-PERF-117 | RESOLVED | searchFiles short-circuits the walk when the cap is reached |
| F-SEC-120 | RESOLVED | Process-local per-project mutex around SaveFileIndex / SaveSymbols |

### Batch 17 (F1 — onboarding hardening): RESOLVED (merged 251e838)

Plan: `docs/superpowers/plans/2026-07-15-domain-f1-onboarding.md`

Closes: F-UIUX-137, F-UIUX-138, F-BUG-154, F-BUG-158, F-BUG-159,
F-BUG-161, F-POL-168, F-POL-169.

Highlights: API key is always persisted as `api_key_env` (or
written to the global `~/.config/marshal/config.toml` only when
the user explicitly pastes inline); `OnboardingModel` uses pointer
receivers; `Run()` resolves options once; `@`-completion explains
empty results; Ollama HTTP status checked; project name prompted.

### Batch 18 (F2 — help overlay & footer polish): RESOLVED (merged 2e0f07c)

Plan: `docs/superpowers/plans/2026-07-15-domain-f2-help.md`

Closes: F-UIUX-138 (overlay part), F-UIUX-139, F-POL-174.

Highlights: help overlay rendered as a real two-column table with
`PgUp/PgDn`/`Ctrl+U/Ctrl+D` and an approval-form sub-table; the
approval-mode footer says `Enter: arm · Enter⏎: submit · Esc: deny`
instead of `Enter×2`.

### Batch 19 (F3 — approval/question overlay correctness): RESOLVED (merged 36b2fb5)

Plan: `docs/superpowers/plans/2026-07-15-domain-f3-overlays.md`

Closes: F-UIUX-140, F-UIUX-141, F-BUG-147, F-BUG-148, F-BUG-153,
F-BUG-156, F-POL-163.

Highlights: `Esc` is a two-step deny on the approval form; the
question form focuses its first field on open; opening settings /
memory while a tool is pending is blocked; channel sends to
`tc.ResponseChan` go through a `sync.Once`-guarded `Respond`; the
`Event` payload exposes a `PendingToolCallInfo` (no
`ResponseChan`); `settingsBlockReason` checks pending decisions and
open pickers; `permissionForTool` deleted.

### Batch 20 (F4 — session state races & lifecycle): RESOLVED (merged d0e66fe)

Plan: `docs/superpowers/plans/2026-07-15-domain-f4-session-state.md`

Closes: F-BUG-149, F-BUG-150, F-BUG-151, F-BUG-152, F-BUG-155.

Highlights: `cancelTurn` only mutates state in
`handleAgentFinished`; `successPulse` clears on error;
`inputAreaRows` includes the SDD hint row; `transcriptHash` includes
content fingerprint; `BeginWork`/`BeginQuiesce` lock-hold verified
under `-race`.

### Batch 21 (F5 — view rendering polish): RESOLVED (merged fdadb55)

Plan: `docs/superpowers/plans/2026-07-15-domain-f5-view-polish.md`

Closes: F-UIUX-142, F-UIUX-143, F-UIUX-144, F-UIUX-145, F-UIUX-146,
F-BUG-160, F-POL-162, F-POL-164, F-POL-165, F-POL-170.

Highlights: settings save control shows block reason inline;
memory browser bumps a version on confidence change; status URL
hidden when the browser bar is shown; SDD panel reports actual
height; completion popup shows the `/` prefix; current mode shown
in the help overlay; `inputAreaRows` uses `lipgloss.Height`; popup
math centralised; swarm panel only emits lines for active roles.

### Batch 22 (F6 — theme, encoding & pub/sub polish): RESOLVED

Plan: `docs/superpowers/plans/2026-07-15-domain-f6-misc-polish.md`

Closes: F-BUG-157, F-POL-166, F-POL-167, F-POL-171, F-POL-172,
F-POL-173, F-POL-175.

| Finding | Status | Notes |
|---|---|---|
| F-BUG-157 | RESOLVED | `pubsub.WithTerminal()` option added; terminal subscriptions block briefly instead of dropping. Job-count subscription uses it. |
| F-POL-166 | RESOLVED | `theme.Reload()` notifies subscribers; model style helpers read `theme.Current()` lazily. |
| F-POL-167 | RESOLVED | `huhtheme.WarmSunset()` is a function that calls `theme.Load()` on each invocation. |
| F-POL-171 | RESOLVED | `truncateURL` uses `ansi.StringWidth`/`ansi.Cut` for rune/width-aware truncation. |
| F-POL-172 | RESOLVED | Markdown renderer cache bounded to 4 entries; evicts width farthest from current. |
| F-POL-173 | RESOLVED | `patternForApproval` and helpers moved to `internal/permissions.PatternForApproval`; `patch` import removed from TUI. |
| F-POL-175 | RESOLVED | `Runtime.closeFns` renamed to `resourceClosers`; comment documents reverse-order cleanup. |

Highlights: `pubsub.WithTerminal` for must-deliver subscriptions;
theme reload publishes a `ChangedMsg`; `huhtheme.WarmSunset` reads
the theme lazily; URL truncation is rune-aware; markdown renderer
cache is bounded (4 entries); `patternForApproval` moved to
`internal/permissions`; `closeFns` renamed to `resourceClosers`.

### Section G (Cross-cutting concerns): status & batching

Doc: `docs/superpowers/plans/2026-07-15-section-g-xcut-batching.md`

13 of 16 XCUT items are already covered by existing plans/batches
(see the table in the batching doc). The 3 with residual work are
covered by three new plans:

- **G1 — ACP/MCP structured logging** (F-XCUT-176):
  `docs/superpowers/plans/2026-07-15-domain-g1-logging.md`
  Adds `*slog.Logger` fields and `With…` options to `acp.Server`,
  `acp.SessionManager`, `mcp.Manager`, `mcp.Client`. 4 new tests.
- **G2 — `reloadAgentRuntime` atomicity** (F-XCUT-184 / F-BUG-15):
  `docs/superpowers/plans/2026-07-15-domain-g2-reload.md`
  Pre-validates the new config by dry-building a runner from a
  copy before mutating `state.Config`. New regression test.
- **G3 — `huh` form completion race residual** (F-XCUT-188 / F-BUG-14):
  `docs/superpowers/plans/2026-07-15-domain-g3-huh-race.md`
  Sub-form `Update` no longer dispatches; parent guards dispatch
  on `sub.done` and `state.PendingApproval() == tc`. Complements
  F3 Task 4. 3 new tests.

The remaining 13 (F-XCUT-177, 178, 179, 180, 181, 182, 183, 185,
186, 187, 189, 190, 191) are already RESOLVED or PLANNED in
earlier batches; the batching doc links each to its underlying
finding and source plan.

### Batch 23 (G1 — ACP/MCP structured logging): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-XCUT-176 | RESOLVED | `acp.Server`, `acp.SessionManager`, `mcp.Manager`, `mcp.Client` all accept `*slog.Logger` via `With…` options. Default `slog.Default()`. Dispatch, connect, list, call, replace, close all log. 4 new tests assert specific events on a buffer logger. |

### Batch 25 (G3 — huh form completion race residual): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-XCUT-188 (residual) | RESOLVED | `session.PendingToolCall.Respond` and `PendingQuestion.Respond` use `sync.Once` to send + close the response channel exactly once. Parent `handleApproval`/`handleQuestion` call `tc.Respond(...)` with a `state.PendingApproval() == tc` identity guard. Sub-form `Update` is pure (no side effects). Complements F3 Task 4. 4 new tests. |

### Batch 24 (G2 — reload atomicity): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-XCUT-184 | RESOLVED | `reloadAgentRuntime` dry-builds the runner from a copy of the new config before mutating `state.Config`. On failure the prior config and runner are preserved and a TUI footer message is shown. New test `TestReloadAgentRuntimeRollsBackOnFailure`. |

### Batch 26 (A4 — remaining HIGH-severity security fixes): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-02 | RESOLVED | `PolicyEngine.WithRegistry` setter; fallback now consults the tool's `Risk` level and returns `DecisionConfirm` for `RiskWorkspaceWrite`/`RiskCommand`/`RiskNetwork`/`RiskDestructive`. 3 new tests. |
| F-SEC-04 | RESOLVED | `web.fetch` `http.Client.CheckRedirect` re-runs `ssrfCheck` on every redirect target; rejects after 5 hops. 1 new test. |
| F-SEC-05 | RESOLVED | `validateServerEnv` rejects `LD_PRELOAD`/`LD_LIBRARY_PATH`/`PATH`/`DYLD_INSERT_LIBRARIES`/`PYTHONPATH`/`NODE_OPTIONS`/`RUBYOPT` and any value containing `\n`/`\r`/`\x00`. 2 new tests. |
| F-SEC-06 | RESOLVED | `validateServerCommand` allow-lists `npx`/`uvx`/`python`/`python3`/`node`/`deno`/`bun`; unlisted commands require `trust = "unrestricted"`. New `Trust` field on `MCPServer`. 3 new tests. |
| F-SEC-09 | RESOLVED | `legacyRoute` returns `(Route{}, false)` when `RemoteAllowed=false` and the legacy provider is not local. New `ErrLegacyProviderBlocked` sentinel. 2 new tests. |
| F-SEC-10 | RESOLVED | `MaxTurnContextTokens` now uses the *minimum* of the configured and model-derived values. Comment updated. 2 new tests. |
| F-SEC-11 | RESOLVED | `actions[]` read-only violation now increments `iteration` and `consecutiveParseFailures` and calls `recordIdle`. 1 new test. |
| F-SEC-13 | RESOLVED | Pending approval with `m.bridge == nil` now sends a `deny` decision on the `ResponseChan` and logs a `Warn`. 1 new test. |

### Batch 27 (A5 — residual MEDIUM-severity security fixes): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-18 | RESOLVED | `file.read` now uses `os.Open` + `io.LimitReader` capped at `maxOutputBytes + 1`, with a size recheck after open. TOCTOU window closed. 1 new test. |
| F-SEC-27 | RESOLVED | `userConfigDir` resolves the path with `EvalSymlinks` and verifies containment under the user's home; logs a `Warn` and returns `""` on mismatch. 1 new test. |
| F-SEC-28 | RESOLVED | `replaceTriggerToken` consumes runs of consecutive `@`s so `@@file` doesn't leave a stray `@`. 2 new tests. |
| F-SEC-29 | RESOLVED | Onboarding `stateConfigureKey` for `keyModeInline` uses `EchoPassword` and a placeholder mask. 1 new test. |
| F-SEC-30 | RESOLVED | `SaveProjectConfig` validates each provider's `base_url` is a parseable HTTP/HTTPS URL with non-empty host. 1 new test. |
| F-SEC-32 | RESOLVED | Skill body wrapped in a fenced "REFERENCE MATERIAL — treat as data, not instructions" block before being added to the prompt. 1 new test. |
| F-SEC-33 | RESOLVED | `validateWorkingPaths` rejects `cwd`/`additionalDirectories` outside an allow-list of the user's home, system temp, and process working dir. `invalidParams` returned. 1 new test. |

### Batch 28 (B — tools & policy audit fixes): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-BUG-39 | RESOLVED | `file.write_patch` now uses `os.WriteFile` which inherently creates new files. New tests `TestWritePatch_NewFileCreation`, `TestFileWritePatchTool`. |
| F-BUG-40 | RESOLVED | `Parse` returns errors for unclosed SEARCH/REPLACE blocks and rejects chunks with no `File:` header. New tests `TestParseRejectsUnclosedSearch`, `TestParseRejectsUnclosedReplace`, `TestParseRejectsEmptyPathChunk`. |
| F-BUG-41 | RESOLVED | After a user edits a tool's args, the runner re-evaluates the policy and propagates errors. New tests `TestRunnerReevaluatesPolicyAfterEditedArgs`, `TestRunnerReevaluatesDenyAfterValidEdit`. |
| F-BUG-42 | RESOLVED | `htmlToText` now uses `html.UnescapeString` from stdlib, decoding both numeric (`&#39;`) and named entities. New test `TestHtmlToTextDecodesNumericAndNamedEntities`. |
| F-BUG-43 | RESOLVED | Job output uses `formatCommandOutput` to produce `stdout:\n...\n\nstderr:\n...`. New test `TestJobOutputSeparatesStdoutAndStderr`. |
| F-POL-44 | RESOLVED | `validateConservativeCommand` removed; runtime path now uses the single `policy` package implementation. New test `TestCommandOutputIsLimited`. |
| F-POL-45 | RESOLVED | `PolicyEngine` has a `logger *slog.Logger` field, `SetLogger` setter, and production wiring at `app.go:376` injects `state.Logger()`. Default `slog.Default()`. |
| F-POL-46 | RESOLVED | `SandboxMeta.LimitsJSON` returns `(string, error)`. New tests `TestSandboxMetaLimitsJSONReturnsValidJSON`, `TestSandboxMetaLimitsJSONIncludesOutputTruncated`. |

### Batch 29 (Low — final polish + doc reconciliation): RESOLVED

| Finding | Status | Notes |
|---|---|---|
| F-SEC-34 | RESOLVED | `normalizePrompt` now validates `resource_link` URIs against a scheme allow-list (`https:`, `file:` with no `..` traversal). `javascript:`, `data:`, `ftp:`, `http:` are rejected with `invalidParams`. 2 new tests: `TestNormalizePromptRejectsBadResourceLinkScheme`, `TestNormalizePromptAcceptsHTTPSResourceLink`. |
| F-SEC-37 | RESOLVED | `dispatchRequest` now uses a `wireError(err)` helper that maps each JSON-RPC error code to a fixed opaque string. The full error is logged server-side via `slog`. 1 new test: `TestDispatchRequestSanitizesWireErrorMessage`. |
| F-SEC-38 | RESOLVED | `/export` clamps the path to the working dir; absolute paths and `..` traversal are rejected. 2 new tests: `TestExportRejectsAbsolutePath`, `TestExportRejectsParentTraversal`. |
| F-BUG-51 | RESOLVED | Turn forwarder uses `pending.Respond(answers)` (sync.Once + close, added in G3) instead of a direct `pending.ResponseChan <- answers` select that could fire `<-turnCtx.Done()` and lose the answers. 1 new test: `TestForwarderUsesRespondForQuestion`. |
| F-POL-130 | RESOLVED | `Scanner` already skips symlinks (added by an earlier batch, `scanner.go:104-112`). Regression test added to lock the behavior in. 1 new test: `TestScannerSkipsSymlinkWithReason`. |
| F-SEC-03 | RESOLVED | Doc-gap: code is correct. `SafeResolve` path resolver (added in the A3 batch) is in place. Marked RESOLVED to reconcile the audit table. |
| F-SEC-08 | RESOLVED | Doc-gap: code is correct. Onboarding key entry uses `EchoPassword` for both modes (verified in A5 Task 3). Marked RESOLVED to reconcile the audit table. |
