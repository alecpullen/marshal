# Mid-turn effort override

Status: implemented.
Date: 2026-10-18.
Source baseline: `ab22c47b`, rebased onto `fb1639d3` (main after [reasoning replay](../reasoning-replay/feature-spec.md) merged).
Implementation: complete. Plan: [plans/2026-10-18-mid-turn-effort-plan.md](plans/2026-10-18-mid-turn-effort-plan.md).

## Purpose

Let the user change the reasoning effort of the running conversation and have it take effect at the next step of the current turn, not only from the next turn.

The motivating case is an agent over-deliberating at a high effort level. The user should be able to turn it down while the turn is still running, without permanently editing the preset.

## Evidence

- `RunTask` binds provider, model, thinking, temperature, max output and context window once per turn (`runner.go`, `turnRequestOptions`). Every step's request reads that frozen value (`chat.go`).
- The only effort control today edits the preset's `thinking` in the model options panel. That is a saved config change, followed by `reloadAgentRuntime`.
- The reload updates the status bar at once (`SetActiveRoute`), while the running turn keeps its turn-start values. The footer can therefore name a model or effort the turn is not using.
- Step-boundary input already exists: steering and subagent reports are drained at the loop top.
- Reasoning replay keeps provider-bound reasoning in history. Toggling thinking on or off inside a tool loop is unsafe with replayed reasoning (Anthropic forbids it; it is unverified on Codex and the DeepSeek hosts). Changing the level is not.

## Scope

In scope:

- An unsaved, per-session effort override.
- Level changes take effect from the next step. Changes that cross the off boundary take effect from the next turn.
- TUI: `/effort` and `alt+e`.
- ACP: `session/set_effort` and `effort_changed` updates.
- A status bar that shows the running turn's real route and effort, with pending changes.

Out of scope:

- Temperature and max-output overrides.
- Mid-turn model switching.
- Persisting the override across restart or resume.
- A web UI control (the ACP surface is provided for it).
- Subagent effort (still `agent.run`'s `thinking` argument or the child's preset).

## Design

### Session override

`session.State` holds an `EffortOverride` with three fields:

- `Level`: `""` means none; otherwise a level name or `off`.
- `Provider` and `Model`: the route the level was validated against.

It is thread-safe, in-memory and session-scoped: `/new` starts a fresh state, and a resumed session starts without an override.

### Setting it

`Runtime.SetSessionEffort(level)` is the single entry point for the TUI and ACP, mirroring `Runtime.SetSessionMode`.

- **Validation.** It validates `level` against the active route's thinking options, using `provider.ThinkingOptionsFor` over the model cache, the same source the model panel uses. It accepts `""` to clear, any level in `Levels`, and `off` only when `Levels` contains it. When options are unknown (nil), any non-empty level is accepted, as preset `thinking` is today.
- **Recording.** It records the override with the active route's provider and model.
- **Return value.** It returns when the change takes effect:
  - `next_step` when a turn is running and the change keeps thinking on (or keeps it off);
  - `next_turn` when no turn is running, or when the change crosses the off boundary.
- **Event.** It publishes `EventEffortChanged`.
- **Unsupported routes.** A route whose provider cannot take effort (`ReasoningCapable == false`) is refused with an error naming the model.

### Runner

**Effective effort at turn start**, in order of precedence:

1. `Runner.ThinkingOverride` (subagents and custom agents);
2. the session override, if its provider and model match the turn's route (otherwise it is dropped with a transcript notice);
3. the preset's `thinking`.

The runner publishes the turn's route as `State.SetTurnRoute(RouteInfo)`, with `Thinking` set to the effective effort, and clears it when the turn ends.

**At each step boundary**, before building the request, the runner computes the target effort: the override's level, or the preset's when the override is clear.

- **Off boundary.** If the target and the current effort fall on different sides of the off boundary (`"off"` versus anything else, `""` counting as on), the change waits for the next turn. A one-line notice is shown once per pending value.
- **Otherwise,** when the target differs from the current effort:
  - set `turnRequestOptions.thinking`;
  - add the transcript note `Effort → <level> from step N`;
  - update the turn route's `Thinking`;
  - publish `EventEffortChanged`.

`Runner.ThinkingOverride` still wins; when it is set, the session override is ignored for that runner.

### TUI

- **`/effort <level>`** sets the override, **`/effort default`** clears it, and **`/effort`** alone opens a picker of the active model's levels with the current one marked. A rejected level lists the valid ones.
- **`alt+e`** cycles through the levels plus `default`.
- **Status bar.** While a turn runs, the footer renders `TurnRoute()` (model and `think <effective>`) instead of `ActiveRoute()`.
  - When `ActiveRoute()`'s preset or model differs, it appends `→ <model> (next turn)`.
  - When the override is pending against the turn's effort, it shows `think high → max (next step)` or `→ off (next turn)`.
  - Idle, it renders `ActiveRoute()` as today, using the override when one is set.

### ACP

- **`session/set_effort {sessionId, effort}`** calls `Runtime.SetSessionEffort` through a `TurnRuntime.SetEffort` hook. It responds with `{effort, appliesFrom}`.
- **Validation failures** return invalid params. An unknown session or a runtime without the hook returns a server error, as `session/set_mode` does.
- **Updates.** `EventEffortChanged` is forwarded as a `session/update` of kind `effort_changed`, with `effort` (the effective level), `pending` (the requested level, or `""`) and `appliesFrom`.

## Error handling

- An invalid level is refused before anything changes.
- A stale override (the route changed between turns) is dropped at turn start with a notice. A level the new model may not support is never sent.
- Off-boundary changes are deferred, never applied mid-loop.
- A reasoning-incapable route refuses the override outright.

## Verification

- **Session:** override and turn-route accessors under `-race`.
- **Runner** (`agenttest.ScriptedProvider`, with `OnChat` setting the override during step 1):
  - step 2's `ChatRequest.Thinking` is the new level, with the transcript note;
  - `off` set mid-turn leaves later steps unchanged and the next turn starts at `off`;
  - clearing reverts to the preset from the next step;
  - a stale override is dropped at turn start;
  - `ThinkingOverride` wins;
  - with replay on, reasoning items still go out after a level change.
- **Runtime:** validation accepts and rejects correctly, `appliesFrom` is right, and an incapable route is refused.
- **TUI:** `/effort` parsing and the picker, `alt+e` cycling, and status-bar rendering for running, pending and next-turn model.
- **ACP:** `session/set_effort` validation and response, `effort_changed` update shape, unknown session.
- **Budget:** `TestDefaultPromptBudget` still passes; no system-prompt text is added.

## Open questions

None.

## Implementation record

Implemented on branch `mid-turn-effort`, based on `fb1639d3` (main after
reasoning replay merged). One commit per plan task:

| Task | Commit | Title |
| --- | --- | --- |
| 1 | `f3b465ea` | Session state for the override and the turn route |
| 2 | `dcffa658` | Effort level validation helpers |
| 3 | `de2ad5e1` | `Runtime.SetSessionEffort` |
| 4 | `e85cc228` | Runner applies the override |
| 5 | `45a43e2c` | TUI `/effort`, `alt+e` and an honest status bar |
| 6 | `a4ef7236` | ACP `session/set_effort` and `effort_changed` |

Notes:

- The plan's prerequisite grep (`reasoning-replay: task`) expects eleven task
  commits; the reasoning-replay work landed as one squashed commit
  (`fb1639d3`). Its substance — including `agenttest.ScriptedProvider.ReasoningItems`
  and `internal/agent/reasoning_replay_test.go` — is present, so the plan's
  `runner.go`/`chat.go` anchors were re-checked against it and the work proceeded.
- The picker's clearing row carries the literal `"default"` as its `Value`,
  because the picker routing switch drops an empty `Value` before the `effort`
  case is reached. `applyEffort` maps `"default"` back to `""`.
- No system-prompt text was added; the effort notes are transcript `RoleSystem`
  messages, so the cache-stable prompt prefix is unaffected.
