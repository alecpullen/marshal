# Inline Approval and State Indication Design

**Date**: 2026-07-05
**Author**: Alec Pullen
**Status**: Draft

## Summary

Improve Marshal's TUI clarity and UX by (1) rendering the command-approval prompt inline at the bottom of the transcript instead of taking over the entire chat panel, and (2) making agent state — thinking, running a tool, running a shell command, awaiting approval, and delivering a final answer — visually distinct and obvious. The goal is to approach the clarity of Claude Code and opencode, where it's always obvious what the agent is doing.

## Motivation

- The approval UI currently replaces the entire chat viewport (`renderApprovalArea`, `model.go:1006`), hiding the conversation the user may need to decide whether to approve a command.
- Tool calls are invisible until their result appears in the transcript — no live "running `shell.run`" indicator.
- The status bar's activity indicator (`model.go:852`) is a small spinner + truncated label in a 9-wide cell; the distinction between thinking, tool-running, and awaiting-approval is subtle.
- The agent's final answer renders identically to intermediate assistant messages (plans, rationales) — no visual signal that "this is the response to your question."
- For long-running shell commands (tests, builds), there's no elapsed-time indicator while they run.

## Design

### Architecture: State-Polling (Approach A)

All live state flows through `session.State` mutation, polled by the TUI's existing 150ms `agentTickMsg`. No new `tea.Msg` types, no `tea.Program` reference in the runner, no new channels. This mirrors the existing patterns for `pendingApproval` and `inProgress` (streaming reasoning). The TUI remains rendering-only per CLAUDE.md and `docs/02-system-architecture.md`.

The 150ms polling latency is acceptable: shell commands take seconds; fast tools (<150ms) skip the live indicator entirely and just show their result — which is the correct UX.

### 1. Inline Approval UI

**Problem**: `renderApprovalArea` (`model.go:1006`) replaces the entire chat viewport when `state.PendingApproval() != nil`.

**Change**: Remove the chat-takeover branch from `renderChatPanel` (`model.go:993-1004`). The chat panel always renders `m.viewport.View()`. The approval block becomes part of the viewport content, appended in `refreshViewport` (`model.go:511-536`) as the last item, after messages and the live thinking box.

**Rendering**: A bordered panel using the existing `renderPanel` primitive (`model.go:756`):
- Header: `⚠ Approval` + risk-level badge
- Body: `Agent wants to run:` + command (or `File patch:` + diff summary for patches)
- For patches: the diff renders as a collapsed panel below the approval block (truncated inline as today; no expand toggle in this iteration — the full diff is visible in the Log tab's audit trail)
- Footer: help line `Enter approve · d deny · e edit · a always · r rollback`

**Key handling**: The `tc != nil` branch in `Update` (`model.go:299-365`) is unchanged — keys route to approval decisions. The decision still flows back via `PendingToolCall.ResponseChan`.

**Viewport behavior**: `GotoBottom()` already runs at the end of `refreshViewport`, so the approval block stays pinned to the bottom. The user can scroll up to read prior context while the approval waits. The input box stays focused for approve/deny/edit keys.

**What this removes**: The `renderApprovalArea` function and its call from `renderChatPanel`.

### 2. Live Tool-Call Blocks in Transcript

**Problem**: Tool calls are invisible until their result appears.

**New state** on `session.State` (alongside `pendingApproval`, guarded by the existing mutex):

```go
type ActiveToolCall struct {
    Name      string    // "shell.run", "file.read", etc.
    Args      string    // compact summary of args (command, path, query)
    StartedAt time.Time
}

func (s *State) SetActiveToolCall(ActiveToolCall)
func (s *State) ActiveToolCall() (ActiveToolCall, bool)
func (s *State) ClearActiveToolCall()
```

**Runner integration** (`internal/agent/runner.go`, `executeToolCall` ~line 358):
1. After policy evaluation passes (~line 412) and before calling `tool.Handler` (~line 450): `state.SetActiveToolCall({Name, Args, StartedAt: r.Now()})`
2. After handler returns: `state.ClearActiveToolCall()` — via `defer` to ensure cleanup on handler error/panic.

**Args summary**: A compact string per tool, derived from `ToolCall.Args` JSON in a small helper:
- `shell.run` → the `command` field
- `test.run` → the `command` field (or the configured test command)
- `file.read` → the `path` field
- `repo.search` → the `query` field
- `git.diff` / `git.status` → the `ref` field (or empty)
- `file.write_patch` → `"patch"` (the diff is shown via the approval flow)
- Others → first arg value or empty string

**TUI rendering** (`refreshViewport`, `model.go:511`):
- After rendering messages + thinking box, check `state.ActiveToolCall()`.
- If active, append an inline single-line block: spinner frame + tool name + args summary + elapsed time. Example: `⠹ shell.run  go test ./...  · 2s`
- Uses `renderPanel` chrome with a `Tool` title; spinner frame from `m.spinnerFrame` (already updated by the tick).
- Elapsed: `m.now().Sub(startedAt)`, formatted `Ns` / `Nm Ms`.
- The block disappears the instant the tool result message is appended (since `ClearActiveToolCall` runs before the result `AddMessage`). Transition: live block → result message, no gap or overlap.

**Dirty-tracking**: Extend the optimization in `refreshViewport` (`model.go:515-517`) to also check whether `ActiveToolCall` state changed — so the viewport rebuilds on tool start/end, not just on the 150ms tick.

### 3. Prominent State Pill/Banner

**Problem**: The status bar's activity indicator (`model.go:852-871`) is easy to miss; the distinction between thinking/tool/approval is subtle.

**Change**: Add a full-width state strip rendered between the chat panel and the input box, only visible when the agent is active (not idle).

**Rendering** — a single-line colored strip spanning the full `leftWidth`:
- **Thinking**: cyan/violet background, `⠹ thinking...`
- **Running tool**: amber (warning) background, `⠹ running shell.run · go test ./... · 2s` — reuses `ActiveToolCall` from Section 2
- **Awaiting approval**: red (error) background, `⚠ awaiting approval · shell.run` — draws the eye to the inline approval block
- **Idle**: strip hidden entirely (no vertical space consumed)

**Layout adjustment**: `chatBelowViewportRows` (`model.go:42`) currently = 4 (input box 3 + help line 1). When the strip is active, `chatHeight` shrinks by 1 row. The `resize` function (`model.go:160`) checks `state.Activity()` / `state.ActiveToolCall()` / `state.PendingApproval()` to decide whether to subtract the extra row. When idle, `chatHeight` stays as today. The chat area grows/shrinks by 1 row as the strip appears/disappears.

**Status bar**: The existing status bar (`model.go:835`) keeps its role (route info, model, brand). The busy cell is simplified to `IDLE` / `DONE` (the existing 2-second done badge). The spinner moves exclusively to the new strip to avoid double-indication.

**Tick driver**: The existing `agentTickMsg` (150ms) already updates `spinnerFrame` and triggers `refreshViewport`. The strip renders in `View` (not the viewport), reading `state.Activity()` + `state.ActiveToolCall()` + `spinnerFrame` directly — no new tick needed.

### 4. Distinct Final Answer Treatment

**Problem**: The agent's final answer renders identically to intermediate assistant messages.

**Change**: Mark the terminal answer message and render it with a distinct visual treatment.

**Marking**: Add a `Final bool` field to `session.Message`. The runner sets it `true` only in the `ActionAnswer`/`ActionFinal` branch of `Run` (`runner.go:169-211`). All other `AddMessage` calls leave it `false` (zero value). This is set in one place, unambiguous, and survives across renders.

**Rendering** (`renderMessage`, `model.go:1074`):
- If `msg.Final`: a cyan left border bar (2 chars wide) running the full height of the message, a bold `Response` label instead of `agent`, and slightly wider padding. This makes the answer visually pop as "the thing that answers your question," separated from the tool chatter above.
- If not `Final`: render as today (violet `agent` label, no border bar).

**Persistence**: The `Final` flag persists to SQLite so reloading a session preserves the distinction. Add a `final INTEGER DEFAULT 0` column to the `messages` table and update the save/load queries in `internal/db/`. The migration is additive (nullable/defaulted column), so existing sessions remain compatible.

### 5. Live Command Execution Indicator

**Problem**: For `shell.run` / `test.run`, the user sees nothing until the command finishes.

**Change**: Build on Section 2's `ActiveToolCall` state with command-specific richness.

**Rendering** — when `ActiveToolCall.Name` is `shell.run` or `test.run`, the live tool-call block expands from a single line to a small 2-line bordered panel:
```
⠹ shell.run                    2s
$ go test ./...
```
- First line: spinner + tool name + right-aligned elapsed (amber color)
- Second line: the command with a `$ ` prefix, dim mono style
- Uses `renderPanel` chrome with a `Running` title

**Non-command tools** (`file.read`, `repo.search`, `git.diff`, etc.) keep the single-line treatment from Section 2 — no expansion, since their args are short and they typically complete quickly.

**Elapsed time**: Computed in `refreshViewport` on each tick as `m.now().Sub(activeToolCall.StartedAt)`:
- `< 60s`: `Ns` (e.g. `2s`, `45s`)
- `>= 60s`: `Nm Ms` (e.g. `1m 30s`)
- Updates every 150ms via the existing tick.

**No stdout streaming**: Deliberately out of scope. Live stdout capture would require piping command output through `session.State` in real time — a much larger change to `execRunner` and the shell tool. The indicator shows command + elapsed only. The full output appears as the tool-result message when the command completes, exactly as today.

**Cleanup**: When the command finishes, `ClearActiveToolCall` runs (Section 2), the live panel disappears, and the tool-result message renders with the full stdout/stderr. The transition is seamless.

## Files

| File | Change |
|---|---|
| `internal/app/session/session.go` | Add `ActiveToolCall` struct + `SetActiveToolCall`/`ActiveToolCall`/`ClearActiveToolCall`; add `Final` field to `Message` |
| `internal/agent/runner.go` | Set/clear `ActiveToolCall` around `tool.Handler`; set `Final=true` on terminal answer `AddMessage` |
| `internal/app/tui/model.go` | Remove `renderApprovalArea` chat-takeover; append approval block + live tool-call block in `refreshViewport`; add state strip in `View`; simplify status bar busy cell; distinct `Final` rendering in `renderMessage`; extend dirty-tracking; adjust `resize` for strip row |
| `internal/db/` | Add `final` column to `messages` table; update save/load queries |

## Out of Scope

- Live stdout/stderr streaming for shell commands (full output appears on completion as today)
- Per-file approval for multi-file patches (single approve/deny for the whole patch)
- Syntax highlighting in diffs or code blocks
- Changing the approval decision protocol (`ResponseChan` stays as-is)
- Moving policy/routing/prompt logic into the TUI (TUI stays rendering-only)
- Real-time token-count or cost indicators
