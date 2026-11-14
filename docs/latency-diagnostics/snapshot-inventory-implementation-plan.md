# Snapshot accounting inventory implementation plan

**Goal:** remove repeated accounting traversals while preserving current store
classification, budgets, recovery and snapshot verification.

**Spec:** `snapshot-traversal-investigation-2026-11-14.md`, approved for
implementation by the user. Native execution in the existing isolated
`codex/snapshot-admission-investigation` worktree; independent final review.

**Architecture:** a read-phase inventory retains aggregate directory usage
only. Global accounting, workspace/generation detail and discovery consume
those aggregates without revisiting object files. New inventories are created
for each operation; reconciliation mutations and publication never reuse a
previous phase's inventory. Rotation measures just the generation in question.

## Constraints and review focus

- No capture-frequency, source verification, daemon or live-config changes.
- Preserve conservative rounding, metadata/unknown/symlink/staging accounting,
  corrupt/protected/unlisted generations, legacy cache behavior and fresh limits.
- Retain only directory totals/identity, not an entry for each object file.
- Missing or replaced inventory directories fail closed; cancellation interrupts
  the walk. Ref readability/protection checks remain live.
- Post-publication rotation remeasures its target; no cross-capture cache.
- Review unknown/new directories during classification, multiple generations,
  accounting errors, unreadable refs, callback reentrancy and budget observers.

## Task 1: Shared phase inventory

Files: new `internal/snapshot/usage_inventory.go` and
`usage_inventory_test.go`; modify `usage.go` and `reconcile.go`.

- [x] Write and observe failing regression tests for one object-accounting
  visit per usage, workspace measurement and discovery operation.
- [x] Add a context-aware walker that accumulates normal `Usage` fields and
  retains only directory aggregates and identities for a caller-owned phase.
- [x] Build versioned global usage once and pass its inventory into workspace
  detail. Public workspace measurement creates its own fresh inventory.
- [x] Share discovery's workspace inventory with generation/scratch accounting.
- [x] Cover accounting parity, freshness across calls, missing/replaced
  directories and prompt cancellation; run existing accounting/recovery tests.
- [x] Commit verified component.

## Task 2: Narrow rotation measurement

Files: modify `usage.go` and `admission.go`; add tests in
`usage_inventory_test.go`.

- [x] Write and observe a failing test: a readable oversized target can be
  assessed without walking an unrelated generation.
- [x] Share generation classification/ref-reading logic with a target-only
  measurement. Preserve manifest, protection, directory type and ref errors.
- [x] Test fresh growth, protected targets, corrupt manifests and unreadable
  refs; run full snapshot package and focused Go 1.27 race checks.
- [x] Commit verified component.

## Task 3: Evidence, integration and review

- [x] Repeat the same three-process one/four-workspace diagnostic on the fixed
  public Marshal source, plus a separate profile; compare against committed
  baseline logs and check the predicted seven-to-three visit reduction.
- [x] Repeat real-client timings with LSP off/snapshots on in private fixtures.
- [x] Run complete default Go suite with a required private embedded binary,
  repository/tagged vet, and independent code/evidence review.
- [x] Record results and limitations; commit and publish a separate PR.

## Progress

Base: `4d2f59c3` investigation; main base `011113b4`.
Tasks 1 and 2 share generation accounting; keep its classification logic in
one helper before narrowing the rotation caller. Validation consumes both.

Tasks 1/2 implemented together because they share generation classification.
RED: usage/workspace/discovery visits3/2/2; unrelated unreadable generation blocked target rotation. GREEN focused tests. Full snapshot package112.955s passed before ancestor-guard correction; focused safety/accounting tests2.185s and Go1.27 race8.447s passed afterward.
Independent review identified missing ancestor no-follow checks in target rotation; both new regressions failed before the guard correction and passed after it.

Task 3 complete: three matched component pairs, two separate profiles and three
real-client pairs. Warm captures were 16.7% / 26.7% faster for one / four stored
workspaces; the real warm turn fell from 2.551 to 2.201 seconds. The initial full
suite exposed a pre-existing asynchronous ACP assertion race, reproduced on the
baseline. A test-only wait passed 80 repeats, ten race-checked repeats and a
cancellation-removal mutation check. The complete final suite passed 123
packages with no failures. Vet, focused race checks and independent code/evidence
reviews are complete.
