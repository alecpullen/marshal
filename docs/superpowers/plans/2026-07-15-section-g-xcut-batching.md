# Section G — Cross-cutting Concerns: Status & Batching

> **Scope:** Findings `F-XCUT-176` … `F-XCUT-191` (16 items) in
> `docs/14-codebase-improvement-audit-2026-07-14.md`.
>
> **Goal:** Decide which items are already covered by existing plans, which
> need new plans, and produce a single page that the SDD orchestrator can
> consume.

## TL;DR

13 of the 16 XCUT findings are **fully covered** by existing plans
(some are already RESOLVED on a feature branch, some are PLANNED).
3 findings have residual work that warrants a new implementation plan:

| Plan | New file | XCUT items closed |
|---|---|---|
| **G1 — ACP/MCP structured logging** | `2026-07-15-domain-g1-logging.md` | F-XCUT-176 |
| **G2 — `reloadAgentRuntime` atomicity** | `2026-07-15-domain-g2-reload.md` | F-XCUT-184 |
| **G3 — `huh` form completion race (residual)** | `2026-07-15-domain-g3-huh-race.md` | F-XCUT-188 (residual) |

F-XCUT-190 is already covered by F6 (planned).

## Status table

| ID | Title | Underlying finding(s) | Covered by | Status |
|---|---|---|---|---|
| F-XCUT-176 | No structured logging in ACP or MCP | new | **— none —** | OPEN → plan G1 |
| F-XCUT-177 | Shared `Runner` fields mutated without locking | F-CON-79 | D1 | RESOLVED |
| F-XCUT-178 | DB `MaxOpenConns(1)`, no WAL | F-PERF-119 | E3 | RESOLVED |
| F-XCUT-179 | Symlink/`EvalSymlinks` missing everywhere | F-SEC-03 / 19 / 102 / 122 / 123 | A3 | RESOLVED |
| F-XCUT-180 | Snapshot writes not transactional | F-BUG-103 | E1 | RESOLVED |
| F-XCUT-181 | Command-allow patterns don't match full argv | F-SEC-07 / 16 | A2 | RESOLVED |
| F-XCUT-182 | Env-var allow-list missing in 3 places | F-SAFE-23 / 24, F-SEC-05 / 35 | A1 | RESOLVED |
| F-XCUT-183 | Tool-arg edit silently discards errors | F-BUG-41, F-POL-88 | D3 | RESOLVED |
| F-XCUT-184 | Snapshotter/policy reload leaves inconsistent state | F-BUG-15 | **— none —** | OPEN → plan G2 |
| F-XCUT-185 | Several advertised `/` commands are empty stubs | F-POL-84 | D6 | RESOLVED |
| F-XCUT-186 | Goroutine-leak risks when TUI consumer may exit | F-CON-80, F-CON-54 | D2 (F-CON-80) + C-plan Task 8 (F-CON-54) | RESOLVED via existing plans |
| F-XCUT-187 | `ResponseFormat` / `MaxTurnContextTokens` mutable on Runner | F-BUG-74, F-POL-85 | D1 | RESOLVED |
| F-XCUT-188 | `huh` form completion can race with explicit cancel | F-BUG-14 | F3 (Task 4 covers channel-send race; **the sub-form `Update` race is the residual**) | PARTIAL → plan G3 |
| F-XCUT-189 | `app.Runtime.Close` called on still-active runtime | F-BUG-49, F-BUG-50 | C-plan Task 8 (F-BUG-50) + Task 9 (F-BUG-49) | RESOLVED via existing plan |
| F-XCUT-190 | `pubsub` drop semantics unsafe for job-count updates | F-BUG-157 | F6 plan | PLANNED |
| F-XCUT-191 | DB connection pool tuning for read/write contention | F-PERF-119 | E3 | RESOLVED |

## Batching rationale

The XCUT items are **rollup findings** — they identify patterns
("this same problem appears in N places") rather than independent bugs.
Each one is satisfied by closing its underlying items. The
resolution table at the bottom of the audit doc tracks the underlying
items; this document only adds plans for the cross-cutting items
whose underlying work is **not already in flight**.

### Why F-XCUT-176 needs a new plan

The C plan threaded `*slog.Logger` only into MCP `RegisterTools` and
ACP `Server.dispatch` (panic recovery). It did **not** add a
constructor seam for a logger on:

- `acp.Server` (for handler dispatches, outbound requests, shutdown)
- `mcp.Manager` (for connect/list/call, server-skipped events)
- `mcp.Client` (for protocol read loop diagnostics)
- `acp.SessionManager` (for `publishReplacement` and `Close`)

Currently every layer falls back to `slog.Default()`. The fix is a
new `Logger *slog.Logger` field on each public constructor and a
`nil` → `slog.Default()` fallback so existing call sites don't
break. This is a small, well-scoped plan.

### Why F-XCUT-184 needs a new plan

`reloadAgentRuntime` is described in F-BUG-15 and not touched by
any existing plan. The fix is a pre-validation step that
re-runs `buildAgentRunner` against a copy of the new config and
only mutates `state.Config` on success. This is a single
~50-line change with one new test.

### Why F-XCUT-188 needs a residual plan

F3 Task 4 covers the `sync.Once` responder and the
`ResolvePendingForShutdown` close race (the "macro" view of
F-BUG-14). The "micro" race that F-XCUT-188 calls out — `huh`'s
own form `Update` returning `done=true` while the parent
`approvalModel.Update` is processing an `Esc` — is not covered.
Fix: clear the pending approval/question in the TUI's
`handleApproval`/`handleQuestion` **before** returning, and
have the sub-form's `Update` set a `done` flag that the parent
checks before dispatching to `tc.Respond`. The plan is small
(one file, one test) and complements F3 Task 4.

### Why F-XCUT-186 and F-XCUT-189 don't need new plans

- F-XCUT-186 = F-CON-80 (D2) + F-CON-54 (C-plan Task 8). Both
  layers have explicit `time.After` arms / goroutine dispatching
  in their existing plans. No residual.
- F-XCUT-189 = F-BUG-50 (C-plan Task 8 bounded wait) +
  F-BUG-49 (C-plan Task 9 continuous lock hold). No residual.

## New plans

### Plan G1 — ACP/MCP structured logging (F-XCUT-176)

`docs/superpowers/plans/2026-07-15-domain-g1-logging.md`

**Scope:**
- Add `*slog.Logger` field + `WithLogger` option (or constructor
  parameter) on `acp.Server`, `acp.NewServer`, `mcp.NewManager`,
  `mcp.NewClient`, `acp.NewSessionManager`.
- Default to `slog.Default()` when nil.
- Log: handler dispatch (method, duration, error), outbound
  request, MCP connect/list/call, server-skipped events,
  server-shutdown sequence.
- Tests: assert that a test logger receives specific events
  (use a `*slog.Logger` backed by a `bytes.Buffer` and parse).

**Estimated tasks:** 4

### Plan G2 — `reloadAgentRuntime` atomicity (F-XCUT-184, F-BUG-15)

`docs/superpowers/plans/2026-07-15-domain-g2-reload.md`

**Scope:**
- In `internal/app/app.go:776-842`, build a "dry" runner
  from a deep copy of the candidate config before mutating
  `state.Config`.
- Only on dry-build success, atomically swap `state.Config` and
  the new runner.
- On failure, leave the previous config/runner untouched and
  surface the error in the TUI as a transient footer.
- Add `TestReloadAgentRuntimeRollsBackOnFailure`.

**Estimated tasks:** 1

### Plan G3 — `huh` form completion race residual (F-XCUT-188)

`docs/superpowers/plans/2026-07-15-domain-g3-huh-race.md`

**Scope:**
- In `internal/app/tui/approval.go` and `question.go`: have
  the sub-form `Update` set a local `done` flag and `return
  (am, nil)` without calling the parent's responder.
- In `internal/app/tui/model.go` `handleApproval` and
  `handleQuestion`: before calling `tc.Respond`, check that
  the sub-model's `done` is true and that
  `state.PendingApproval() == tc` (defends against the
  `ResolvePendingForShutdown` already-responded case).
- Add a regression test that simulates: parent sends
  `Esc` while `huh` simultaneously emits `done` via an
  internal `tea.Cmd`; the responder fires exactly once.

**Estimated tasks:** 2

## Resolution recording (post-implementation)

When each new plan lands, update the audit doc's resolution
table with new `### Batch 23` / `### Batch 24` / `### Batch 25`
sections following the existing format. Cross-link from the
new plans to `F-XCUT-176` / `F-XCUT-184` / `F-XCUT-188`
respectively.

## Verification

After all three plans are merged:

```bash
CGO_ENABLED=1 go build ./...                  # all three compile
CGO_ENABLED=1 go test ./... -count=1          # full suite green
rg -n "F-XCUT-(176|184|188)" docs/14-codebase-improvement-audit-2026-07-14.md
# Should show only the "### Batch N" status rows, not "#### F-XCUT-…"
```

## Open / unresolved after this batch

After G1–G3 land, every XCUT item is either RESOLVED or PLANNED.
The audit doc's "Total" / "Open" counts drop by:

- 1 LOW (F-XCUT-176) from G1
- 1 HIGH (F-XCUT-184 / F-BUG-15) from G2
- 1 HIGH (F-XCUT-188 / F-BUG-14 residual) from G3

No new cross-cutting findings are expected; the next audit
sweep should focus on per-domain regressions rather than
new shared-infrastructure rollups.
