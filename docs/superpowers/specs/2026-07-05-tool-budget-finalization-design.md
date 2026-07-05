# Tool-Budget Finalization & Anti-Spiral Design

**Date:** 2026-07-05
**Status:** Approved design, ready for implementation planning

## Problem

The agent frequently exhausts its `MaxToolIterations` budget (default 16) without
producing a final answer. When this happens today, `Runner.RunTask`
(`internal/agent/runner.go:255-257`) simply sets the task to `Failed`, appends a
system message ("Agent stopped: exceeded max tool iterations without a final
answer."), and returns `ErrMaxIterationsExceeded`. The user gets **nothing** —
not even a partial answer synthesized from everything the agent already learned.

Two failure modes drive this:

1. **No salvage on exhaustion.** The loop ends abruptly. All the context gathered
   across 16 iterations is discarded instead of being turned into a best-effort
   answer.
2. **Weak spiral detection.** The only guard is `shouldNudgeLoop`
   (`runner.go:532`), which fires a single advisory nudge and only when the
   *exact same* tool+args repeats three times in a row. An agent that churns
   through slightly-different reads and searches never trips it.

## Goals

- Never return empty-handed on budget exhaustion — produce a useful (possibly
  partial) final answer from the transcript.
- Detect churn earlier and by *category* (repeated reads/searches with no new
  write or validation), not just exact repeats.
- Apply escalating pressure to conclude: soft near the budget end, hard when the
  agent is clearly stalled.
- Make budget state visible to the user in the TUI, and the budget itself easy to
  tune.
- Reinforce all of the above with a tighter system prompt.

## Non-Goals

- Changing the default budget value (stays 16). The fix is smarter *use* of the
  budget, not a bigger one.
- Injecting a numeric iteration counter into the model's context. Budget state is
  conveyed to the model only qualitatively (via the pressure/nudge messages). The
  numeric counter is a TUI-only, user-facing affordance.
- Changing swarm orchestration semantics. A salvaged turn still counts as
  `Completed`; it is merely *inspectable* as salvaged.

## Design Decisions (locked)

| Decision | Choice |
|----------|--------|
| Scope | All five proposed changes (finalization pressure, salvage pass, broadened progress detection, prompt tightening, TUI budget display). |
| Salvage outcome | **Completed, but flagged** — status `Completed`, no error returned, answer shown, but a marker records that it was produced under budget pressure. |
| Stall response | **Escalating** — first stall nudges (advisory), continued stall forces finalization. |
| Default budget | **Keep 16**, but surface `max_tool_iterations` in the TUI settings screen for per-project tuning. |
| Model budget visibility | **Qualitative only** (via messages); TUI shows the numeric counter to the user. |
| Mechanics architecture | **Approach A** — one unified `finalize` primitive that all three runtime triggers converge on. |

## Architecture

All three runtime changes revolve around one primitive: *"make a model call that
must produce a final answer and must not call tools."* Approach A factors that
into a single helper so the finalization prompt is authored once and every trigger
yields the same "flagged completion" outcome.

Three triggers invoke it:

1. **Soft pressure (#1)** — near the budget end, inject an *advisory* system
   message but keep looping normally.
2. **Escalated stall (#3)** — a `ProgressTracker` detects churn; first stall
   nudges, a hard stall calls `finalize` immediately.
3. **Exhaustion salvage (#2)** — the loop's iteration budget runs out; call
   `finalize` as a last resort instead of bare-failing.

### Component 1 — the `finalize` primitive

New method on `Runner`:

```go
func (r *Runner) finalize(
    ctx context.Context,
    p provider.Provider,
    model string,
    messages []schema.ChatMessage,
    task *Task,
    reason finalizeReason, // "exhausted" | "stalled"
) (*Task, error)
```

Behavior:

- Appends a strong system directive to a copy of `messages`, e.g.:
  > "You are being asked to stop using tools and conclude. Produce the best final
  > answer you can from the transcript, context pack, and tool results gathered so
  > far. Do NOT call tools. If a required fact is genuinely missing, state what you
  > would check next and give your best partial answer."
- Makes **one** `chatWithRetry` call, reusing the turn's already-resolved
  `provider`/`model` (no re-routing).
- Parses the result with `ParseAction`:
  - If `answer`/`final`: record via `State.AddMessageFinal`, set
    `task.Status = Completed`, set the salvage flag (Component 4), return success.
  - If the model **still emits a tool call** (ignoring the directive): do **not**
    execute it. Extract any prose the model produced; if none, synthesize a
    fallback answer from `task.Plan` and the most recent tool results
    ("Ran out of tool budget before finishing. Here is what I found: …"). Record
    it as the final message with the salvage flag set. This guarantees we never
    return empty-handed.
  - If the `chatWithRetry` call itself errors: return that error so the caller can
    fall back to `ErrMaxIterationsExceeded` (Component 3).

This is the single seam shared by the exhaustion and hard-stall triggers.

### Component 2 — `ProgressTracker`

Replaces the ad-hoc `callHistory` / `shouldNudgeLoop` / `loopNudgeSent` fields.
A small struct owned by the Runner, reset at the start of each turn.

Records each **executed** tool call as:
- a **category**: `read`, `search`, `shell`, `write`, `patch` (derived from the
  tool name / registry risk), and
- its normalized args (reusing `normalizeArgs`).

Tracks:
- iteration index of the last `write`/`patch` and of the last validation (a
  `shell` run — e.g. tests/build), and
- a rolling window of recent categories.

`Assessment()` returns one of:

- `Progressing` — a new write/patch/validation happened recently, or the call is
  novel.
- `Stalling` — repeated reads/searches with **no new write or validation** since
  the previous stall signal.
- `HardStall` — stalling persisted across another iteration, **or** the same
  tool+args repeated 3× (today's exact-repeat signal, folded in as the strongest
  stall).

Runner maps assessments to actions:

- `Stalling` → inject the advisory nudge (now permitted to fire again after
  progress resumes, rather than strictly one-shot per turn).
- `HardStall` → call `finalize(reason="stalled")` and end the turn.

### Component 3 — loop integration (`RunTask`)

Inside the `for iteration := 0; iteration < r.MaxToolIterations; iteration++` loop:

1. **Soft finalization pressure (#1):** when
   `remaining := r.MaxToolIterations - iteration <= finalizePressureThreshold`
   (default 2), inject the advisory pressure message **once per turn**, before the
   model call. Purely additive to `messages`; the loop continues normally.
2. After each tool execution, feed the executed call into `ProgressTracker`. On
   `HardStall`, call `finalize` and return its result.
3. **Exhaustion (#2):** replace the current fail block (`runner.go:255-257`) with
   a `finalize(reason="exhausted")` call. Only if `finalize` itself returns an
   error do we set `task.Status = Failed`, append the existing system message, and
   return `ErrMaxIterationsExceeded`.

The happy path (model answers before budget) is unchanged.

### Component 4 — session state & the "flagged" outcome

- Add to `session.State`:
  - `ToolBudget struct { Used, Max int }`, updated each iteration.
  - A salvage marker (`FinalizedUnderPressure bool` plus a short reason string)
    set when `finalize` produces the answer.
- The final message carries a subtle marker so the transcript/TUI can render e.g.
  *"⚠ answer produced after tool budget exhausted"* — distinct from a clean
  completion, but still a completion.
- `Task` gets a matching field (e.g. `SalvagedReason`) so the swarm orchestrator
  can distinguish clean vs. salvaged completions. It still counts as
  `TaskStatusCompleted`; the field is purely for inspection.

### Component 5 — TUI budget display (#5) & tunable setting

- **Display:** the status/activity area shows `tools N/16`, sourced from
  `session.State.ToolBudget`. It warns (color change) as it approaches max. When a
  turn ends salvaged, show the marker from Component 4.
- **Tunable:** add `max_tool_iterations` to the TUI settings screen. The field
  already exists in `config.go` (`Agent.MaxToolIterations`) and round-trips
  through `save.go`; this only *exposes* it via the existing
  `settings/field.go` + `settings/model.go` plumbing. No new config surface.

### Component 6 — prompt tightening (#4)

Edit `baseRules` in `internal/agent/prompts.go` to add:

- "Use tools only to obtain facts you don't already have in the transcript or
  context pack."
- "Once the requested change is made and validated, produce a `final` answer — do
  not keep exploring."
- "Stop after validation succeeds."

Small and additive. Weaker than the runtime enforcement, but reinforces it.

## Data Flow

```
RunTask
  └─ for iteration in 0..Max:
       ├─ if remaining <= threshold: inject soft pressure (once)   [#1]
       ├─ chatWithRetry → action
       ├─ execute action
       │    └─ ProgressTracker.record(category, args)
       ├─ Assessment():
       │    ├─ Stalling  → inject nudge                            [#3]
       │    └─ HardStall → finalize(reason="stalled") → return     [#2/#3]
       └─ update State.ToolBudget                                   [#5]
  └─ (budget exhausted) → finalize(reason="exhausted")             [#2]
       ├─ success → Completed + salvage flag → return nil          [flagged]
       └─ error   → Failed + ErrMaxIterationsExceeded
```

## Testing Strategy

- **`finalize`:** produces a flagged completion; handles a model that ignores the
  no-tools directive (synthesized fallback path); propagates a chat error so the
  caller can fall back to `ErrMaxIterationsExceeded`.
- **`ProgressTracker`:** `Progressing` / `Stalling` / `HardStall` transitions,
  category derivation, exact-repeat 3× still detected, nudge re-arms after
  progress resumes.
- **Loop:** soft pressure injected at the correct iteration and only once;
  exhaustion path salvages instead of bare-failing; `ErrMaxIterationsExceeded`
  returned only when `finalize` also fails.
- **Existing tests:** `runner_test.go:394` (exhaustion) updates to assert
  salvage-then-flagged, plus a variant where salvage fails → error preserved.
- **Config/TUI:** `max_tool_iterations` settings field round-trips; budget counter
  renders and warns near max; salvage marker renders in the transcript.

## Files Touched (anticipated)

- `internal/agent/runner.go` — `finalize`, `ProgressTracker` wiring, loop
  integration, exhaustion replacement.
- `internal/agent/progress.go` (new) — `ProgressTracker` + `Assessment`.
- `internal/agent/prompts.go` — `baseRules` additions, finalization directive
  text.
- `internal/agent/task.go` — `SalvagedReason` field.
- `internal/app/session/…` — `ToolBudget`, salvage marker on state and final
  message.
- `internal/app/tui/…` — budget counter + salvage marker rendering.
- `internal/app/tui/settings/…` — expose `max_tool_iterations`.
- Corresponding `_test.go` files for each.
