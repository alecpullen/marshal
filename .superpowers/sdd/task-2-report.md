# Task 2 Report: Implement Built-in Command Handlers

## Status: ✅ Complete

## What was done

1. **Created** `internal/commands/commands.go` with `RegisterAll()` function registering 17 built-in commands:
   - `exit` / `quit` — exit Marshal
   - `new` / `clear` — start new conversation (uses `ClearMessages()`)
   - `help` — lists all registered commands via `cmdReg.List()`
   - `tools` — lists all tools via `toolReg.List()`
   - `route` — shows active model route via `ActiveRoute()`
   - `context` — shows message count, total chars, context pack sections
   - `stop` — cancel current turn (no-op handler)
   - `ask` / `edit` / `auto` — mode switching (no-op handlers, just return message)
   - `model` — switch model preset (no-op handler, has `<preset-name>` arg)
   - `config` — shows project, working dir, profile, remote allowed, auto-approve
   - `settings` / `memory` — open panels (no-op handlers)
   - `rollback` — rolls back last patch via `HasBackup()`/`RollbackBackup()`

2. **Fixed** three type mismatches in the brief's code to match actual types:
   - `cfg.RemoteProvidersAllowed` → `cfg.Privacy.RemoteProvidersAllowed`
   - `cfg.AutoApprove` → `cfg.Tools.Shell.AutoApprove`
   - `pack.Files` → `pack.Sections` (contextpack.Pack has `Sections`, not `Files`)

3. **Build** verified: `go build ./internal/commands/` → no errors

4. **Committed**: `78b19d0` with message `feat: add built-in command handlers`
