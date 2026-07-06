# Phase 5 Swarm Polish — Design

**Date:** 2026-07-06
**Status:** Approved (pending written-spec review)

## Background

Milestone O ("First swarm prototype") is already implemented and wired into the
running binary, even though `docs/10-mvp-implementation-checklist.md` still shows
its eight items unchecked. The engine exists and is reachable via `/swarm <goal>`:

- Shared task state — `internal/agent/swarm/state.go`
- Planner / Repo Scout / Implementer / Reviewer roles — `swarm/prompts.go`, `agent/prompts.go`
- Sequential orchestration `planner → scouts → implementer → reviewer` — `swarm/orchestrator.go`
- Parallel read-only scouts — `ReadOnlyView` registry + goroutines
- Write lock — `swarm/lock.go` (`WriteLock` → `agent.WriteGate`)
- Real wiring — `app.buildSwarmRunner` (app.go) → `swarm.New` → `tui.WithSwarmRunner`

The broader **Phase 5 "Swarm runtime"** roadmap (`docs/08`) lists three things the
eight-item prototype does not cover, and they are the scope of this cycle:

1. A **tester role** — `RoleTester` is *defined* (`agent/prompts.go`) but never used
   in the orchestration pipeline.
2. An **agent activity panel** — swarm progress is currently surfaced only as
   `announce()` chat messages; there is no at-a-glance roster.
3. **Agent budgets** — there is a per-runner tool-iteration cap and per-role
   *context* budgets, but no run-level budgeting.

This cycle does **not** change the single-agent loop's external behavior.

## Component 1 — Test-fix feedback loop (tester role integration)

Change `orchestrator.Run()` from:

```
planner → scouts → implementer → reviewer
```

to:

```
planner → scouts → [ implementer → tester ]*  → reviewer
                     └ loop while tests fail, at most max_fix_rounds rounds
```

### Tester registry scope

The tester runs tests but "does not modify source" (its existing prompt). It needs
command/test execution but must not write source. Add a new registry view:

- `registry.TesterView(src)` — includes read-only tools **and** test/command
  execution tools (e.g. `test.run`, `shell.run`), but excludes `patch`,
  `file.write`, and destructive tools.

The tester runs sequentially with the implementer, so the shared `WriteGate` is
never contended during the loop.

### Verdict protocol

The tester ends its final answer with a verdict line, mirroring the reviewer's
existing `APPROVE` convention:

- `VERDICT: PASS` — tests pass.
- `VERDICT: FAIL` — tests fail; the answer body carries the diagnosis.

Orchestrator behavior after each tester turn:

- **FAIL** with rounds remaining → write the tester's diagnosis into `TaskState`
  (a new finding / note the implementer can read), increment the round counter,
  and run the implementer again.
- **PASS**, rounds exhausted, or an **unparseable / missing verdict** → stop the
  loop and proceed to the reviewer. Unparseable is treated as "stop, don't loop"
  so a misbehaving model can never spin forever.

Each loop pass appends a `patchNote` (implementer) and a finding (tester) to
`TaskState`, so the reviewer sees the full history, not just the final state.

## Component 2 — Swarm activity roster panel

A dedicated roster panel, driven by structured state rather than chat scrollback,
rendered as a new element in the existing single-column view stack.

### State

Add `session.SwarmProgress` alongside the existing `session.Activity()` pattern,
concurrency-safe (the orchestrator updates it from the main goroutine, but scouts
report completion from separate goroutines):

- `Goal string`
- `Active bool`
- Ordered roles with per-role `Status` (`pending | active | done | failed`)
- Active role's round counter `n/N` (for the test-fix loop)
- Scout completion `k/total`

Setters mirror `state.SetActivity` / `state.Activity`.

### Rendering

The current `View()` stack is `transcript / inputArea / statusLine`
(`internal/app/tui/view.go`). Insert the panel **above the input area**, visible
only while a run is active, preserving the single-column stack:

```
transcript
[swarm roster panel]   ← only while SwarmProgress.Active
input area
status line
```

Roster mock:

```
╭─ Swarm: add regression test ─────╮
│ ✔ planner                        │
│ ✔ scouts (3/3)                   │
│ ● implementer   ⣷  round 2/3     │
│ ○ tester                         │
│ ○ reviewer                       │
╰──────────────────────────────────╯
```

- The transcript viewport height is reduced by the panel's row count while a run
  is active, and restored when it ends. Viewport sizing (computed on
  `WindowSizeMsg` and on progress start/stop) must reserve the panel rows so the
  layout never overflows the terminal height.
- On completion the panel clears and the existing `ts.Render()` summary is posted
  to the chat log for durable history.

### announce() reduction

Because live status now lives in the panel, `announce()` is trimmed to
milestone-level chat lines only (run started / run complete / run aborted). The
per-role `"Swarm: implementer"` style lines are removed in favor of the panel.

## Component 3 — Run-level budgets

Three knobs; enforcement is checked **between roles**, and whichever binds first
stops the run **after the current role**, then jumps to the reviewer / final
summary. Never a mid-role hard abort — a stopped role finalizes gracefully via the
existing `SalvagedReason` path.

### max_fix_rounds

Bounds Component 1's implementer↔tester loop.

### Per-role tool-iteration caps

Generalize the existing single `agent.Runner.MaxToolIterations` to a per-role
lookup. Each role's runner is constructed with its cap by the `RunnerFactory`.
Roles without an explicit cap fall back to `agent.max_tool_iterations`. Cap
exhaustion finalizes that role gracefully; the run continues.

### Whole-run token ceiling (pluggable meter)

Metering is behind an interface so the estimate-based implementation ships now and
a real-usage implementation lands in a later milestone without touching call sites:

```go
type TokenMeter interface {
    Observe(role AgentRole, promptTokens, completionTokens int)
    Total() int
}
```

- **`EstimateMeter`** (active default) — sums `contextpack.EstimateTokens()` over
  each role's prompt and final answer. Self-contained; identical across all local
  providers. Approximate, which is acceptable for a backstop.
- **`ProviderUsageMeter`** (stubbed, wired, dormant) — the seam for real provider
  `usage` parsing in a later milestone. For now it delegates to estimation so the
  structure exists but behavior is unchanged. Real `usage` parsing on the
  OpenAI-compatible provider + streaming layer is **out of scope** for this cycle.

The orchestrator holds one meter per run, calls `Observe` after each role turn,
and checks `Total()` against `max_total_tokens` (0 = unlimited) between roles.

## Config

`.marshal/config.toml`, new `[swarm]` section:

```toml
[swarm.budget]
max_fix_rounds   = 3
max_total_tokens = 120000   # 0 = unlimited

[swarm.budget.tool_iters]   # per-role; falls back to agent.max_tool_iterations
scout       = 8
implementer = 20
tester      = 6
```

Follows the existing config merge + `SaveProjectConfig` conventions in
`internal/app/config/`. All fields optional with defaults in `config.Default()`.

## Testing

- **Orchestrator** (table-driven, fake `RunnerFactory`): pass-first-round;
  fail-then-pass; fail-all-rounds (hits `max_fix_rounds`, proceeds to reviewer);
  token ceiling trips mid-run (stops after current role); unparseable/missing
  verdict (stops loop, no infinite loop); per-role tool cap exhaustion.
- **`EstimateMeter`** unit tests; `TokenMeter` interface satisfied by both impls.
- **`session.SwarmProgress`** concurrency test (parallel scout updates).
- **Panel render** golden test (active run + cleared state); viewport height
  reservation test.
- **Config** load/merge/save round-trip for the `[swarm.budget]` block.

## Out of scope (explicit)

- Real provider `usage` token parsing — later milestone; only the `ProviderUsageMeter`
  stub lands now.
- Phase 6 MCP / plugin ecosystem.
- Any change to single-agent-loop external behavior.
- Updating `docs/10` checkboxes is a bookkeeping follow-up, not part of this design.

## Follow-ups (not this cycle)

- Mark Milestone O items complete in `docs/10-mvp-implementation-checklist.md`.
- Real provider usage metering milestone (fills in `ProviderUsageMeter`).
