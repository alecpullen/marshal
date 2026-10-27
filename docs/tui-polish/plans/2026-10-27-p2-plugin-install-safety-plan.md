# P2 plugin install safety — implementation plan

Date: 2026-10-27. Status: executed 2026-10-27 — tasks 1–4, commits `5f129508`,
`9ab6d5bb`, `c6db5b98`, `daf74b1b`. Task 4 ran in parallel with Task 1 (their
file sets are disjoint); the remaining tasks ran in plan order.
Base: `e306ccbaa70aa5a7b2c2cc4a1068a6231748b21d`.
Requirements: [triage item 3](../triage.md#p1--restore-trustworthy-decisions-and-prevent-data-loss)
and [plugin audit findings](../audit.md#plugin-install-can-confirm-a-scan-of-a-different-source).
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** the TUI installs exactly the plugin source and content the user
reviewed, cleans abandoned scans, and preserves an existing installation on
ordinary installation failures.

**Non-goals:** plugin removal redesign, skill management, plugin activation
without restart, generic clipboard/paste routing, other P1/P2 triage items, or
changes to CLI/ACP installation. This is the second implementation plan in
sequence; its scope is the next **P1** item in the priority triage.

**Decisions and assumptions:**

- A review belongs to one install-frame generation and the exact entered
  source and selected scope. Changing either invalidates it. A result from an
  abandoned or superseded generation cannot become the current review.
- Confirm shows the scanned source and scope, and the complete executable
  hooks, MCP command/argument boundaries and policies are reachable through a
  keyboard detail view before installation. Escaping terminal controls is
  mandatory; never execute plugin content while scanning or reviewing.
- Hash the scanned clone after removing `.git`. Copy into a private staging
  directory in the destination store, hash the stage and compare it to the
  reviewed hash immediately before committing. A mismatch aborts without
  changing the destination or lockfile. Validate every derived plugin name
  with `plugins.ValidName`, including the `github:` normalization branch.
- A scan owns its temporary directory until a synchronized claim transfers it
  to the active review. Leaving/replacing the panel cancels the scan and
  disposes an unclaimed result even if its Bubble Tea result is never delivered.
  A successful confirmation transfers ownership to the install worker; Escape
  cannot delete its source while copying. The worker always cleans its own
  clone and staging paths, including when the panel is no longer visible.
- Stage the replacement and lockfile before swapping. For an existing install,
  rename it to a unique backup in the same store; rename the staged directory
  into place; atomically rename a staged lockfile over the old lockfile. On
  ordinary errors before the lockfile rename, restore the backup and report
  any rollback failure including the retained backup path. For a new install,
  remove the newly placed directory on rollback. Never discard the old backup
  until the lockfile commit succeeds. Preserve unrelated lockfile entries.
- The directory swap and lockfile rename are two separate filesystem actions,
  not one crash-atomic transaction. A crash or power failure between them can
  leave a backup or a temporarily mismatched lockfile; startup verification
  must continue to fail closed. This plan covers error rollback, not automatic
  crash recovery or coordination with another process editing the same store.
  Detect an externally changed lockfile before the swap and abort instead of
  knowingly overwriting it; a concurrent writer after that check remains a
  documented limitation.
- Existing P0 removal confirmation behavior and restart notification remain.
  Tests use isolated local fixtures and injected per-instance dependencies;
  no real credentials, network, live services, or package-global test hooks.

Tasks run sequentially. Each task is independently verified and may be
committed after it passes as `p2-plugin-install-safety: task N — <title>`.
No parallel-safe markers: tasks share panel state or consume prior contracts.
Stage named files only; the checkout contains unrelated untracked work.

## Contracts

Task 1 introduces this optional dock lifecycle hook in `internal/app/tui/dock`:

```go
// CloseAware is notified when the host permanently releases a panel.
type CloseAware interface { OnClose() }

// SwapRetained replaces the visible panel without closing either panel and
// returns the displaced panel to its owner for later restoration or release.
func (h *Host) SwapRetained(panel Panel) Panel

// ReleaseRetained closes a panel previously returned by SwapRetained.
func (h *Host) ReleaseRetained(panel Panel)
```

`Host.Open` calls `OnClose` on a displaced panel before replacing it;
`Host.CloseNow` calls it once before clearing the slot. A retained panel is
restored with `SwapRetained` or disposed with `ReleaseRetained`, not `Open`.
Calls are synchronous,
idempotent from the panel's perspective, and must not block on workers.
Temporary suspension for Send now must not invoke the hook: a suspended
panel is retained and restored, not permanently released.

Task 2 introduces an unexported, mutex-protected scan lease in
`internal/app/tui/plugins`:

```go
type scanLease struct { /* synchronized state; private fields */ }
func newScanLease(cancel context.CancelFunc) *scanLease
func (l *scanLease) Publish(dir string) bool
func (l *scanLease) Claim() (string, bool)
func (l *scanLease) Close()
```

`Publish` transfers cleanup ownership of the complete temporary parent to
the lease, or immediately deletes it if closed. `Claim` succeeds once only
for a published, open lease and transfers the parent to the caller. `Close`
cancels and deletes an unclaimed published parent, or marks the lease closed
so a later `Publish` deletes it; repeated calls are safe. Cleanup is outside
the mutex. Each result carries the lease pointer and a generation ID; only
the active install frame can claim it. Error paths clean temporary files even
if no result is delivered.

Task 3 introduces this internal installer contract in `internal/plugins`:

```go
type ReviewedInstall struct {
    Name, Source, Commit, ReviewedHash string
    CloneDir, StoreDir, LockPath string
}
func InstallReviewed(ctx context.Context, in ReviewedInstall) error
```

The function owns staging and backup paths it creates, but not `CloneDir`;
the caller owns the scan lease/clone and always cleans it after this call.
The public signature is production behavior, not a global test seam. Internal
per-call filesystem operations may be injected through an unexported helper
for deterministic error tests; ordinary callers use real operations.

## Task 1: Notify panels when the dock releases them

**Goal:** a displaced or closed plugin panel can cancel resources even when
its result message will never reach the dock.

**Files:** `internal/app/tui/dock/dock.go`,
`internal/app/tui/dock/dock_test.go`, `internal/app/tui/model.go`, and
`internal/app/tui/send_now_composer_submit_test.go`.

**Steps:**

1. Add the three declarations in Contracts. Update `Host.Open` and
   `Host.CloseNow` to notify the old panel exactly once on permanent removal;
   `SwapRetained` exchanges the slot without notification, and
   `ReleaseRetained` notifies an explicitly discarded retained panel. Retain
   host sizing and message ownership. Repeated `CloseNow` is inert. Do not
   compare arbitrary interface values with `==` if a panel is not comparable.
2. Change `openSteeringComposer`, `routeWithSuspendedSurface`, and
   `restoreComposerSurface` to use `SwapRetained` for temporary exchanges and
   `ReleaseRetained` when the displaced surface is discarded. Do the same for
   the `endDisplaced` warning/`declineEnd` path: declining restores it without
   notification, while completing the exit releases it. On normal program
   shutdown, release active and retained surfaces once. Preserve existing
   message routing and restoration behavior.
3. Add `TestHostCloseAwareDisplacementAndClose` for replace, close, repeated
   close and panels without the interface. Add
   `TestHostSuspendRestoreDoesNotCloseRetainedPanel` covering Send now
   suspension, restore and actual discard using the real host/model path.

**Verify:**

```bash
go test ./internal/app/tui/dock -count=1
go test ./internal/app/tui -run 'Test.*(Suspend|Composer|SendNow)' -count=1
gofmt -l internal/app/tui/dock
```

Expected: tests pass and formatting prints no paths. This task adds the host
contract; the plugin panel adopts it in Task 2.

## Task 2: Bind each scan to its review and own its lifetime

**Goal:** source/scope edits and panel closure cannot present a stale scan or
leak its temporary clone.

**Files:** `internal/app/tui/plugins/panel.go`,
`internal/app/tui/plugins/panel_test.go`; create
`internal/app/tui/plugins/scan_lease.go` and
`internal/app/tui/plugins/scan_lease_test.go`.

**Steps:**

1. Implement the scan-lease contract. Make clone/scanner creation injectable
   per panel through an unexported constructor or option; production uses
   `plugins.NewInstaller`, `Clone`, `ScanPlugin` and `HashDir`. Capture a
   cancelable context, generation, entered source and scope before starting a
   worker; validate the normalized name with `plugins.ValidName`. After
   cloning, remove `.git`, scan and hash; publish the temp parent through the
   lease. Return a result containing generation, raw and normalized source,
   scope, commit returned by `Clone`, contents, reviewed hash and lease.
2. On successful result, claim only if the lease and generation still match
   the active install frame and the current source and scope equal the
   captured values. Close stale leases and leave the current flow unchanged.
   On source or scope change, or leaving the install frame, increment the
   generation, cancel/close the outstanding lease, clear the review and
   remove Confirm. Implement `Panel.OnClose` to do the same without waiting.
   Capture the confirmed clone parent in `runInstallFromScan` and clear the
   panel's reference before launching the worker; a later panel close must not
   touch worker-owned files. The worker defers cleanup on every outcome.
3. Add `TestScanLeaseClosePublishClaimRaces` with deterministic channels and
   repeated Close for close-before-publish, publish-before-close,
   claim-before-close and failed scan. Assert every unclaimed directory is
   deleted and a claimed directory remains owned by its claimant.
4. Add panel tests `TestScanReviewInvalidatedBySourceOrScope`,
   `TestAbandonedScanCannotAttachToReopenedFrame`,
   `TestPanelReplacementCleansUndeliveredScan`, and
   `TestInstallOwnsCloneAfterConfirm`. Drive real panel/host updates with local
   temporary paths and delayed fake clone/copy; assert no stale Confirm, no
   leaked clone, and no deletion of worker-owned input on Escape/replacement.

**Verify:**

```bash
go test ./internal/app/tui/plugins ./internal/app/tui/dock -count=1
go test -race ./internal/app/tui/plugins ./internal/app/tui/dock -count=1
gofmt -l internal/app/tui/plugins
```

Expected: tests pass and formatting emits no paths. Until Task 3 lands, the
old installer still removes a destination before copying; do not treat Task 2
alone as completion of this plan.

## Task 3: Stage and roll back reviewed installation

**Goal:** an ordinary copy, hash, rename or lockfile failure does not destroy
the previous plugin or silently alter its lockfile.

**Files:** create `internal/plugins/reviewed_install.go` and
`internal/plugins/reviewed_install_test.go`; modify
`internal/app/tui/plugins/panel.go` and its tests.

**Steps:**

1. Implement `plugins.InstallReviewed` with the contract above. Reject an
   invalid name or empty reviewed hash before creating destination paths. Read
   the lockfile and capture its bytes/existence; fail on malformed input.
   Create a staging directory inside `StoreDir`, copy `CloneDir` with
   `CopyDir`, hash the stage with `HashDir`, and reject a mismatch. Prepare a
   new lockfile as a sibling temporary file using the scanned source, commit
   and matching hash; preserve unrelated entries and sort using `Lockfile.Write`.
2. Re-read and compare the original lockfile bytes/existence immediately before
   swapping. If changed, abort and clean the stage. If destination exists,
   rename it to a unique same-store backup; rename stage to destination; then
   rename the prepared lockfile over `LockPath`. If stage placement or lockfile
   commit fails, remove the new destination if present and restore the backup;
   report both original and rollback errors and retain the backup when restore
   fails. Remove the backup only after successful lockfile commit; return a
   warning/error with its path if cleanup fails rather than pretending it
   disappeared. Never remove a destination by a name derived without validation.
3. Route `runInstallFromScan` through `InstallReviewed` with the reviewed
   identity and hash. The worker owns and defers cleanup of its clone even if
   result delivery is lost. Keep `busy`, `installErr`, restart notification
   and scoped destination semantics. The confirmation cannot execute without a
   current claimed review matching source and scope.
4. Add `TestInstallReviewedStagesAndCommits` for new install and update,
   unrelated lock entries, matching content hash and saved commit. Add
   `TestInstallReviewedRejectsChangedContentAndLock` for a clone mutation and
   a lockfile changed before swap; old directory and lock bytes survive.
   Add `TestInstallReviewedRollsBackFailures` with per-call injected failures at
   copy, hash, stage placement and lockfile rename; assert old content and
   lockfile survive, no partial new plugin remains, and a failed restore reports
   and retains its backup. Add `TestInstallReviewedRejectsInvalidName` for
   traversal and invalid normalized GitHub names. Panel integration tests must
   invoke Confirm through the real update path and assert mismatch is visible.

**Verify:**

```bash
go test ./internal/plugins -run '^TestInstallReviewed' -count=1
go test ./internal/plugins ./internal/app/tui/plugins -count=1
go test -race ./internal/app/tui/plugins -count=1
gofmt -l internal/plugins/reviewed_install.go internal/plugins/reviewed_install_test.go internal/app/tui/plugins
```

Expected: all focused and package tests pass, no formatting output, and every
injected error leaves either the original install or an explicitly reported
recovery backup.

## Task 4: Expose full executable content in review

**Goal:** a user can inspect the exact source and complete executable entries
before confirming, including long commands and arguments at 80×24.

**Files:** `internal/app/tui/plugins/panel.go`,
`internal/app/tui/plugins/panel_test.go`.

**Steps:**

1. Add a panel-owned review-detail state for the selected hook, MCP server or
   MCP policy. Replace their passive scalar rows with actions that open this
   state on Enter. Show the reviewed normalized source and scope as separate
   rows in the confirmation frame. Display the full hook command, MCP command
   plus each individually quoted argument, or policy in a wrapped, scrollable
   view; escape untrusted terminal controls using `strutil.ShowControls`.
   Use PgUp/PgDn to page and Esc to return to the same review selection;
   reserve visible hints and never allow Enter in this detail to install.
   The existing Confirm row remains the only installation action.
2. Add `TestReviewDetailShowsCompleteExecutableContent`: use hook and MCP
   commands longer than 80 columns, arguments containing spaces/quotes and a
   long policy. Page through all markers at 80×24, then return and confirm;
   verify raw terminal controls are absent and argument boundaries remain
   distinct. Add `TestReviewDetailResizeAndNoColor` for resize, overflow hints,
   preserved focus, and monochrome rendering.

**Verify:**

```bash
go test ./internal/app/tui/plugins -run 'Test(ReviewDetail|ConfirmScreen)' -count=1
go test ./internal/app/tui/plugins -count=1
gofmt -l internal/app/tui/plugins
```

Expected: tests pass and formatting emits no paths. Existing ordering tests
must still prove MCP entries and policies are stable across renders.

## Final verification and integration

Run from the repository root after all tasks:

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-p2-plugin-safety ./cmd/marshal
go test ./internal/plugins ./internal/app/tui/plugins ./internal/app/tui/dock -count=1
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -timeout 5m
go vet ./...
gofmt -l internal/plugins internal/app/tui/plugins internal/app/tui/dock
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-p2-plugin-safety go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Record actual output and compare any full-suite failures with the P1 baseline
in [triage](../triage.md); do not hide or silently skip failures. The existing
scripted usability suite covers help only. Use a temporary local Git repository
with a benign long hook/MCP fixture for a manual 80×24 review, source change,
cancel-during-scan and update check; never execute its plugin content. Record
which terminal environments were checked. Update the four plugin findings and
triage status only after verification, then commit the named documentation and
plan with the work.

## Plan self-review

- The four triage findings map to Tasks 2–4; Task 1 supplies explicit lifecycle
  ownership. Content identity, the worker's cleanup ownership, rollback and
  failure reporting are separate contracts with fixture-based checks.
- The plan does not claim two-file crash atomicity or multi-process exclusion.
  It preserves the previous install for ordinary errors and reports retained
  backups if rollback cannot finish.
- Anchors and existing test seams were inspected at the stated base. No new
  implementation or prototype tests have been run; review is required before
  executing this plan.
