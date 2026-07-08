# Slash Commands Dead on Startup Provider-Construction Failure

## Symptom

When the model provider cannot be constructed at startup (e.g. `api_key_env`
points at an unset env var, the `[providers.<name>]` entry is missing, an
unsupported `type`, a sandbox build error, or an MCP start failure), the TUI
launches but **every** slash command fails:

- Typing `/` shows no command suggestions.
- `/exit` (and every other command) reports "Unknown command: /exit" and the
  app does not exit.

With a working connection at startup, everything works fine.

## Root cause (verified against source)

`internal/app/app.go:495-509`:

```go
runner, toolReg, swarmRunner, mcpMgr, err = buildAgentRunner(...)
if err == nil { ... }

cmdReg := commands.New()
if err == nil {                                   // ← gate
    if err := commands.RegisterAll(cmdReg, toolReg); err != nil {
        return fmt.Errorf("register commands: %w", err)
    }
}
...
tuiOpts = append(tuiOpts, tui.WithCommandRegistry(cmdReg))   // empty registry still wired
if err == nil {
    tuiOpts = append(tuiOpts, tui.WithRunner(...))
    ...
} else {
    state.SetProviderError(err)
}
```

When `buildAgentRunner` returns a non-nil `err`, `commands.RegisterAll` is
**skipped**, so `cmdReg` is a non-nil but **empty** `*Registry`. The TUI is
still constructed (line 544) so the user can see the app, but:

- `dispatchCommand` (tui/model.go:731) calls `cmdRegistry.Lookup(name)` which
  returns `false` for every command → "Unknown command: /%s". The "registry not
  available" branch (model.go:726) only fires when `cmdRegistry == nil`, which
  it never is, so the user never sees the more accurate message.
- `updateCommandSuggestions` (model.go:617) calls `cmdRegistry.List()` which
  returns an empty slice → no suggestions appear as the user types `/`.

`buildAgentRunner` fails at startup precisely when the model "connection" is
down at construction time — most commonly `resolveAPIKey` returning an error
for an unset `api_key_env` (factory.go:35-46), a missing `[providers.<name>]`
entry (app.go:159-162), or an unsupported provider type (factory.go:31). A
provider that *constructs* but is *unreachable at chat time* does NOT hit this
path (commands register fine; the failure surfaces later as a per-turn
`ProviderError` inline), which is why the bug is specifically tied to
startup-time construction failures.

## Fix

Decouple command registration from provider/runner construction. Commands are
UI affordances, not model dependencies: `/help`, `/tools`, `/route`,
`/context`, `/config`, `/log`, `/rollback`, `/new`, `/clear`, `/stop`, `/ask`,
`/edit`, `/auto`, `/settings`, `/memory`, `/exit`, and `/quit` must always be
available so the user can inspect state, clear the conversation, open
settings, or quit even when no model is reachable.

### Edit `internal/app/app.go` (~line 504-509)

Always register the command set. `RegisterAll` only needs `toolReg`; when the
runner failed to build, `toolReg` is `nil`, so register the commands that
don't depend on the tool registry with a nil-safe `toolReg`. Two options:

- **Preferred**: make `commands.RegisterAll` tolerate a nil tool registry — the
  only command that uses it is `/tools` (commands.go:56-66), which can return
  "Tools unavailable (agent failed to initialise)" when `toolReg == nil`. Then
  call `RegisterAll(cmdReg, toolReg)` unconditionally and only `return` on its
  error, dropping the `err == nil` gate.

- Alternative: split `RegisterAll` into `RegisterCore` (everything except
  `/tools`) and keep `/tools` conditional. More surface area, less cohesive —
  not preferred.

The preferred path keeps a single registration call site and ensures `/tools`
fails gracefully with a clear message instead of disappearing entirely.

### Edit `internal/commands/commands.go` (~line 56-66, the `/tools` handler)

Make the `/tools` handler nil-safe:

```go
Handler: func(state *session.State, args []string) string {
    if toolReg == nil {
        return "Tools unavailable (agent failed to initialise). Fix the provider config and restart, or use /settings."
    }
    var b strings.Builder
    b.WriteString("Available tools:\n\n")
    for _, tool := range toolReg.List() {
        b.WriteString(fmt.Sprintf("  %s (%s) — %s\n", tool.Name, tool.Risk, tool.Description))
    }
    return b.String()
},
```

The captured `toolReg` in the closure is already the outer `toolReg`
parameter; a nil value is the new case to handle.

### Behaviour after the fix

- Startup with a broken provider: `buildAgentRunner` returns `err`;
  `RegisterAll(cmdReg, nil)` runs and registers all commands; the TUI launches
  with the provider error shown inline (existing behaviour via
  `state.SetProviderError`) **and** a full command set. `/exit` quits, `/help`
  lists commands, `/tools` reports unavailable, `/settings` opens settings,
  `/context` / `/route` / `/config` / `/log` work against session state.
- Startup with a working provider: unchanged — `toolReg` is non-nil, `/tools`
  lists real tools.
- The `if err == nil { WithRunner... } else { SetProviderError(err) }` block
  is unchanged; only the command-registration gate is removed.

## Task list

1. `internal/commands/commands.go`: nil-guard the `/tools` handler against
   `toolReg == nil` (return a clear "Tools unavailable" message).
2. `internal/app/app.go`: remove the `if err == nil` gate around
   `commands.RegisterAll`; call it unconditionally and `return` only on its
   own error. Keep the existing `WithCommandRegistry(cmdReg)` wiring.
3. Tests in `internal/app/app_test.go` (or a new `app_startup_test.go`):
   - Construct a config whose provider cannot be built (e.g. an unset
     `api_key_env`), run through the `app.Run` wiring up to the TUI opts
     (or call a refactored helper that returns the `cmdReg`), and assert
     `cmdReg.Lookup("exit")` is found and `cmdReg.List()` is non-empty.
   - Assert the provider error is still set on the state.
4. Tests in `internal/commands/commands_test.go`: assert `/tools` with a nil
   tool registry returns the "Tools unavailable" message and does not panic.

## Validation

- `go build ./cmd/marshal` clean.
- `go vet ./...` clean (the pre-existing `app.go:571` lock-copy vet warning is
  unrelated).
- `go test ./internal/app/... ./internal/commands/...` green, including the
  new tests.
- Manual: launch `marshal` with a deliberately broken provider config (unset
  the `api_key_env` var) and confirm `/help` lists commands, `/exit` quits,
  and `/tools` reports unavailable — instead of "Unknown command".

## Out of scope

- Hardening the per-turn stuck-agent window (up to 180s of retries with a
  runtime-broken connection). That is a separate reliability issue from the
  startup-registration bug fixed here; `/stop` already cancels the turn context.
- Adding an HTTP client-level timeout to `OpenAICompatible` (currently relies
  solely on `runner.RequestTimeout`). Separate change.
- Splitting the command registry into "core" vs "tool-dependent" subsets. The
  single nil-safe `RegisterAll` call is sufficient.