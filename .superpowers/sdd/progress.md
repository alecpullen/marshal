# Phase 4 — TUI Implementation Ledger

Branch: feature/phase-4-tui
Base: 40acdc0 Merge branch 'feature/phase-3-session-model-infra'
Plan: docs/superpowers/plans/2026-07-09-phase-4-tui.md

## Tasks

- [x] Task 1: F19 — Event bus (`internal/pubsub` + `internal/csync`)
- [x] Task 2: F19 — Bubble Tea subscription pump + first consumer (job count)
- [x] Task 3: F16 — Steering queue (type while the agent works)
- [ ] Task 4: F17 — Diff view upgrade (side-by-side / unified, `/diff`)
- [ ] Task 5: F18 — Editor completions (`/`-commands and `@file`)

## Workflow

Subagent-driven with review gate per task. Stop after each task's review
pass for explicit approval before proceeding to the next task.

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

## In progress

(none)
