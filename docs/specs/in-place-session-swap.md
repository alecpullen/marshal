# Spec: In-Place New Session for /new and /clear

## Status
Draft — for review before implementation.

## Problem
Running `/new` or `/clear` in the TUI does not reset the conversation state. The old transcript (tool calls and thinking blocks) stays on screen and the footer `ctx` and `turn` counters keep their stale values. The intent is that `/new` and `/clear` start a genuinely fresh session without closing and reopening Marshal.

## Root cause
`internal/commands/commands.go` `/new` and `/clear` both call `state.ClearMessages()` and nothing else. `ClearMessages` (`internal/app/session/messages.go:385`) only sets `s.messages = nil`. It does not touch:
- the audit log (tool calls) or thinking log, which `Transcript()` merges into the rendered view, so old tool/thinking blocks remain visible;
- the context pack (`ContextPack()`), which feeds the footer `ctx` counter (`internal/app/tui/status.go:183`);
- turn usage (`TurnUsage()`), which feeds the footer `turn` counter (`status.go:190`).

So this is a state-clearing bug, not a TUI refresh bug. Additionally, the session is keyed everywhere by `SessionID` (runner, rollover controller, scratchpad, todos, `turn_metrics`, `generations`), and the TUI holds a single `*session.State` created once in `startRuntime` with no path to swap it. A true new session requires swapping that state and the derived runtime pieces.

## Goal
`/new` and `/clear` create a brand-new session (new session row, new `SessionID`, fresh `State`) and swap it into the running TUI in place, without a process restart. The old session remains fully persisted and is reachable again via `/resume` / `/sessions`.

Non-goals for this spec: the `/resume` picker UI itself (a follow-up), and any change to `/rewind` semantics within a single session.

## Design

### New primitive: `Runtime.NewSession()`
Add a method on `*Runtime` (in `internal/app/runtime.go`) that mirrors the new-session branch of `startRuntime` (around `runtime.go:483-501`) and the agent-runner construction, following the `reloadAgentRuntime` swap pattern (`internal/app/app.go:1303`):

1. Generate a new `sessionID` (`sess_<unixnano>`).
2. `database.CreateSession(sessionID, projectID, "", now)`.
3. Build a fresh `*session.State` via `session.New(cfg, workingDir, now, session.Persistence{DB: database, SessionID: sessionID, Logger: logger})`, then `SetTrusted`, `SetLayers`, `autoloadSkills`, and attach fresh steering/event/workspace brokers — the same wiring `startRuntime` does (`runtime.go:530-553`).
4. Rebuild the agent runner with `buildAgentRunner(...)` against the new state (same call shape as `reloadAgentRuntime` at `app.go:1306`).
5. Rebuild the rollover controller against the new `SessionID` (`NewRolloverController` is already session-scoped; see `app.go:393`).
6. Swap `rt.State`, `rt.Runner`, `rt.SessionID` and the rebuilt subsystems, cleaning up the old runner's resources the way `reloadAgentRuntime` does (close old MCP manager, shut down old job manager, run desktop closer). The DB handle and `ProjectID` are reused, not recreated.
7. Return the new `*session.State` (or an error, leaving the old session intact on failure).

### TUI integration
The TUI must point at the new state and re-render empty:
- Guard on `m.busy` (same guard the Alt+M model swap uses) so a swap cannot happen mid-generation.
- Replace the model's `state` reference with the new `*session.State`, re-point the runner reference, and clear the local viewport/messages cache.
- Call `refreshViewport()` so the transcript renders from the now-empty state.

### Command wiring
`/new` and `/clear` are currently pure `*session.State` handlers in `internal/commands/commands.go`. They cannot reach the `*Runtime`. Wire them like other runtime-touching commands: keep the command names/descriptions, but route their effect through the TUI command dispatch so the model can call `Runtime.NewSession()` and swap state, rather than only calling `ClearMessages()`.

`/clear` remains an alias of `/new` — both perform the full swap.

### What resets (and what does not)
Fresh `State` means all of these start at zero, which also fixes the visible bug:
- message list, audit log, thinking log → empty transcript;
- context pack → footer `ctx` resets;
- turn usage → footer `turn` resets;
- scratchpad, todos, branch tree (new session, no rewind history of its own).

Intentionally preserved: the DB, `ProjectID`, config, trusted flag, skill index, and the user's selected model/provider (carried onto the new state).

### Getting back to the old session
Messages are persisted incrementally via `db.SaveMessage` with parent links, so the prior session is fully saved before the swap. Recovery path is the existing `/sessions` and `/resume <session-id>` commands. A picker UI is a follow-up, not part of this change.

## Failure handling
If `buildAgentRunner` or `CreateSession` fails, do not swap. Keep the current session and surface the error via `state.SetProviderError` / a system message, matching the `reloadAgentRuntime` dry-run-then-swap approach so a failed `/new` never destroys the live session.

## Testing
- Unit: `Runtime.NewSession()` returns a state with a new `SessionID`, empty transcript, zeroed `ContextPack()`/`TurnUsage()`, and the same `ProjectID`/DB.
- Unit: `/new` and `/clear` produce identical effects (alias).
- TUI: after `/new`, footer `ctx` and `turn` show zero and the viewport is empty; `m.busy` blocks the swap during a generation.
- Regression: the prior session is still listed by `/sessions` and resumable via `/resume`.
- Keep `TestSlashCommandClearMessages`-style coverage but assert the new full-reset behavior rather than only the message count.

## Open questions
- Should the new session inherit the current model selection (proposed: yes) or reset to the configured default?
- Does the rollover controller need an explicit stop on the old `SessionID` before re-keying, or is building a new controller sufficient?