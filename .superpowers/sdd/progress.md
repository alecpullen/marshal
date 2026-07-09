# Phase 4 — TUI Implementation Ledger

Branch: feature/phase-4-tui
Base: 40acdc0 Merge branch 'feature/phase-3-session-model-infra'
Plan: docs/superpowers/plans/2026-07-09-phase-4-tui.md

## Tasks

- [x] Task 1: F19 — Event bus (`internal/pubsub` + `internal/csync`)
- [x] Task 2: F19 — Bubble Tea subscription pump + first consumer (job count)
- [x] Task 3: F16 — Steering queue (type while the agent works)
- [x] Task 4: F17 — Diff view upgrade (side-by-side / unified, `/diff`)
- [x] Task 5: F18 — Editor completions (`/`-commands and `@file`)

## Workflow

Subagent-driven with review gate per task. After Task 2, user approved
continuing through the remaining tasks autonomously.

## Completed

- Task 1: `40acdc0..94ce93e` (3 commits)
  - pubsub + csync infra; `PublishTerminal` dropped; `WithTerminal`
    must-receive semantics documented on `Publish` godoc
  - 13 tests, -race clean
- Task 2: `94ce93e..18fb224` (1 commit)
  - Pump: `pumpJobEvents(ctx, *pubsub.Broker[native.JobEvent]) tea.Cmd`
  - Bridge: `jobCountMsg{count int}`; re-armed from `Update` on receipt
  - Wiring: `native.Options.JobBroker` seam; `JobManager.SetBroker` publishes
    alongside retained `SetOnChange` callback (callback kept for tests)
  - Status line reads `m.jobCount`; `state.RunningJobsCount()` polled fallback
    preserved when `m.jobBroker == nil`
  - 1 Minor: `Subscribe(ctx)` runs in the factory, not lazily — harmless
- Task 3: `18fb224..27b141c` (1 commit)
  - Steering queue in `session.State`, `agent.SteeringProvider`, loop-top drain,
    TUI busy-input enqueue, Ctrl+X clear, status segment, and queued transcript
  - Review verdict: SPEC ✅, QUALITY approved; only Minor notes recorded
- Task 4: `27b141c..0b2f375` (1 commit)
  - Diff renderer package, approval dialog integration, `/diff` command, and
    renderer/parser tests
  - Review verdict: SPEC ✅, QUALITY approved; only Minor notes recorded
- Task 5: `0b2f375..0d5a273` (5 commits)
  - `/` command and `@file` fuzzy completion popups, pinned context-pack support,
    runner extraction, and review-driven fixes for literal `@path` acceptance and
    TUI/runner trigger alignment
  - Verification from fixer: focused TUI tests PASS, `go test ./...` PASS
  - Review verdict after fixes: SPEC ✅, QUALITY approved
- Final branch review: `40acdc0..ac145da` (13 commits)
  - First final review found 4 Important branch-level issues: pump subscription
    leakage, stale broker-backed steering counts, legacy transcript diff rendering,
    and missing `@file` pinning for drained steering messages
  - Fixed in `a2f07f1`; report committed in `ac145da`
  - Final re-review verdict: Ready to merge, no Critical/Important/Minor issues

## In progress

(none)
