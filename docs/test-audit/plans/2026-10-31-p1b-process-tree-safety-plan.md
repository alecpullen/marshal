# Test audit P1b — process-tree cancellation and ownership

Date: 2026-10-31. Status: proposed; not executed.
Base: `68614578882c9a74ab1e6d5682426fb7934a8190`.
Requirements: [process-group failure](../audit.md#process-group-cancellation-has-an-environment-sensitive-failure),
[P1 roadmap](../roadmap.md#p1--snapshot-identity-and-cancellation-safety),
and the existing snapshot ownership guarantees in `internal/snapshot`.
Workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** tolerate a transient Unix process-group probe error without ever
claiming a group is gone until it is confirmed, and retain store ownership
when termination is uncertain.

**Non-goals:** treat EPERM as proof of exit, loosen cancellation or store
locking, change snapshot contents or identity, suppress failing tests, or
redesign Windows process containment. P2's shell and prompt-budget failures
remain separate.

**Evidence:** the recorded macOS test failed with EPERM in 6/10 sandboxed
and 6/10 elevated runs. During planning, an isolated parent/grandchild probe
outside the sandbox reproduced EPERM twice in 100 attempts after SIGKILL and
direct-child reap; the same group returned ESRCH 50 ms later in both cases.
The sampled `ps` output found no group members at that later observation. A
single-child comparison returned ESRCH in 20/20 runs. This supports a
transient group-lifecycle race, not a claim that every EPERM means the group
is dead. Tests must exercise persistent EPERM and live descendants too.

The current ownership path has two additional hazards relevant to the same
failure: `StoreLock.Release` clears its drain list before executing it and
does not restore failed drains, and manager calls untrack a tree unconditionally
after `runProcessTreeWith` returns, including when `waitGone` failed. A retry
can therefore lose the very condition that prevented release. The latter is a
code-path finding; no actual lost writer was demonstrated during planning.

**Decisions and safety contracts:**

- On Unix, retry `EPERM` from the signal-0 group probe until the existing
  `processTreeKillTimeout`, sleeping at the existing poll interval. Only
  `ESRCH` proves the group gone. A nil probe means still present. Any other
  error returns immediately. At the deadline, a last EPERM returns an error
  wrapping EPERM; a continuously present group returns the existing timeout
  error. Never retry or reinterpret the SIGKILL result as success.
- A failed `StoreLock` drain retains the OS lock, local semaphore and failed
  drain callbacks for retry; successful drains are removed and are not run a
  second time. Drains execute newest first. Concurrent `Release` calls must
  serialize and cannot report success while another call is still draining.
  Preserve callbacks registered while a release is in progress for the next
  drain before unlock. A failure does not discard them.
- For a manager-owned process tree, distinguish **confirmed gone** from
  **termination unknown**. An unknown tree remains registered and keeps any
  platform handle needed to retry; `Manager.Release` must not unlock until
  its drain confirms exit. A later successful drain removes and closes it.
  This applies to `RunManaged`, `gitRun`, `gitRunEnv`, and `gitRunEnvWriter`.
  A start that fails before a process exists closes and untracks normally.
  A started child whose tree assignment fails must be killed/reaped; if its
  descendants cannot be proven gone, retain ownership and report uncertainty.
  Serialize registration/start against release so a drain cannot observe an
  unstarted tree as gone and then let it start after unlocking.
- A direct child exiting normally does not prove its descendants exited.
  Before reporting its result or untracking, wait for the group/job to be
  confirmed gone. If descendants remain past the existing bound, return
  termination-unknown and let the ownership drain kill and recheck them;
  do not silently report success or release ownership.
  If no manager owns the tree, attempt kill and a bounded recheck before
  returning the uncertainty error; always surface inability to prove exit.
- Windows job handles must remain open while termination is uncertain; retain
  the documented assignment window. Never convert a failed job wait or kill
  to success. A service operation returns a release error to its caller even
  if its own operation succeeded; join operation and release errors when both
  occur, while retaining the lock for retry.
- Use test-owned child processes with hard deadlines and cleanup. Use
  instance-scoped fakes, not mutable package globals. Production paths keep
  their existing timeout and platform implementations.

## Contracts

Private Unix probe helper for Task 1:

```go
func waitForGroupGone(
    pgid int,
    timeout time.Duration,
    probe func(int) error,
    now func() time.Time,
    sleep func(time.Duration),
) error
```

The probe receives the positive group ID and probes that group; the production
adapter calls `syscall.Kill(-pgid, 0)`. This helper has no package-global
mutable dependencies.

Task 3 adds a private typed uncertainty result and an owned-tree lifecycle
contract used by all managed launch paths:

```go
type treeTerminationUnknownError struct { Cause error }
func (e *treeTerminationUnknownError) Error() string
func (e *treeTerminationUnknownError) Unwrap() error
```

The wrapper is returned only when a started tree's exit cannot be proved.
Cancellation with confirmed exit still returns `ctx.Err()`; ordinary child
exit and start errors keep their existing API behavior. Internal lifecycle
state distinguishes not-started, starting, live, confirmed-gone and unknown;
release must wait for an in-flight start to resolve before draining it.

## Task 1: Retry transient group-probe EPERM, fail closed on uncertainty

**Goal:** Unix cancellation waits for ESRCH through a transient EPERM and
returns an error when disappearance cannot be confirmed.

**Files:** `internal/snapshot/process_unix.go`, new
`internal/snapshot/process_unix_test.go`.

**Steps:**

1. Extract `waitForGroupGone` from `processTree.waitGone` using Contracts and
   Decisions. Keep `pgid <= 0` behavior, `processTreeKillTimeout`, and the
   5 ms production poll interval. On persistent EPERM wrap the final EPERM
   with `%w` and enough group context to diagnose it; never return nil for a
   live or inaccessible group.
2. Add `TestWaitForGroupGoneTransientEPERM`,
   `TestWaitForGroupGonePersistentEPERM`,
   `TestWaitForGroupGoneLiveUntilTimeout`, and
   `TestWaitForGroupGoneUnexpectedError` using injected per-call probe and a
   deterministic clock/sleep. Assert EPERM→ESRCH succeeds, persistent EPERM
   is detectable with `errors.Is`, live group times out, and a different
   error remains visible immediately. Assert timeout is bounded and no zero
   or negative group is signalled.
3. Strengthen `TestRunManagedCancellationKillsTree` only if needed to record
   that its heartbeat writer started before cancellation and remains stopped
   after return; keep the existing heartbeat check and error assertion. Do
   not mark EPERM or a still-growing heartbeat successful.

**Verify:**

```bash
go test ./internal/snapshot -run '^TestWaitForGroupGone' -count=1
go test ./internal/snapshot -run '^TestRunManagedCancellationKillsTree$' -count=30 -timeout 90s
gofmt -l internal/snapshot/process_unix.go internal/snapshot/process_unix_test.go
```

Expected: deterministic cases and repeated owned-child test pass on Unix;
formatting prints no paths. A persistent EPERM remains a failure.

## Task 2: Keep failed drains retryable

**Goal:** a failed or concurrent release cannot discard a safety drain or
unlock a store while a drain is still unresolved.

**Files:** `internal/snapshot/lock.go`,
`internal/snapshot/ownership_test.go`.

**Steps:**

1. In `StoreLock.Release` and `OnRelease`, serialize release attempts with a
   per-lock mutex and retain failed callbacks until they succeed. Run callbacks
   in the existing reverse-registration order; remove successful callbacks
   only, keep any callbacks added during draining, and complete all pending
   callbacks before releasing the file lock and local semaphore. Avoid holding
   the state mutex while executing callbacks. Keep `Release` idempotent after
   actual unlock; document the concurrency rule.
2. Add `TestStoreLockRetriesFailedDrain`: use a test-owned lock, one callback
   that fails once and one that succeeds. Verify first Release returns the
   injected error, another owner cannot acquire, the second Release reruns
   only the failed callback, and only then another owner acquires. Add
   `TestStoreLockConcurrentReleaseWaitsForDrain` with channel-controlled
   callback and two release callers; neither may report completion before the
   drain resolves. Add `TestStoreLockDrainsCallbackRegisteredDuringRelease`
   to prove late registration is not dropped. No sleeps as the event oracle.

**Verify:**

```bash
go test ./internal/snapshot -run '^TestStoreLock(RetriesFailedDrain|ConcurrentReleaseWaitsForDrain|DrainsCallbackRegisteredDuringRelease)$' -count=10 -timeout 90s
go test -race ./internal/snapshot -run '^TestStoreLock' -count=1 -timeout 90s
gofmt -l internal/snapshot/lock.go internal/snapshot/ownership_test.go
```

Expected: tests pass and formatting prints no paths.

## Task 3: Retain uncertain owned trees until confirmed gone

**Goal:** failed cancellation or concurrent release cannot forget an
unverified subprocess tree and release its store lock.

**Files:** `internal/snapshot/process.go`,
`internal/snapshot/process_unix.go`, `internal/snapshot/process_windows.go`,
`internal/snapshot/process_unsupported.go`, `internal/snapshot/manager.go`,
`internal/snapshot/ownership_test.go`; add a Unix-only test file
`internal/snapshot/process_ownership_unix_test.go` if needed for injected
group probes.

**Steps:**

1. Implement `treeTerminationUnknownError` from Contracts and a private,
   synchronized lifecycle for each tree. Register a managed tree before the
   start boundary, mark it starting before `cmd.Start`, and make the release
   drain wait for the start outcome before deciding whether it needs to kill
   or wait. Keep the handle open and the tree tracked whenever termination is
   unknown; close and untrack only after confirmed disappearance or a
   start failure that created no process. Ensure start/assignment failure
   after process creation performs kill/reap and reports uncertainty if
   group/job disappearance cannot be proved. No package-global factory.
2. Replace the unconditional untrack defers in `RunManaged`, `gitRun`,
   `gitRunEnv` and `gitRunEnvWriter` with this lifecycle. Update
   `killLiveTrees` to retry uncertain tracked trees, remove/close each only
   after confirmation, and return a joined error while any remains uncertain.
   Serialize `Manager.Release` attempts so one caller cannot see a temporary
   nil lock while another is draining and report success prematurely.
3. Add an instance-scoped test seam for tree creation/probing using an
   unexported manager field set before the test starts work. Use it to make
   a tree report persistent uncertainty without signalling unrelated
   processes. Add `TestManagerRetainsUnknownTreeAndLock`: after cancellation
   reports the wrapped probe error, repeated Release cannot let a second
   manager acquire; once the fake confirms gone, Release succeeds, closes
   exactly once and the second manager acquires. Cover each managed launch
   path, start/assignment failure, and two concurrent release callers.
4. Add `TestManagerReleaseWaitsForInFlightTreeStart` using channels to hold a
   start in progress, trigger Release, then allow a real test-owned child to
   start. Assert the lock stays held until the child is killed and reaped,
   and no child writes after unlock. Retain the existing cancellation and
   `TestReleaseKillsSubprocessTreeBeforeUnlock` heartbeat checks. Exercise
   Add `TestManagerRetainsTreeWhenLeaderExitsBeforeWriter` to show a normal
   leader exit does not discard a still-writing descendant or unlock early;
   terminate the owned writer during cleanup. Exercise Windows job-handle
   retention via platform-specific fakes/tests on Windows;
   do not claim Windows runtime verification from a Unix-only run.

**Verify:**

```bash
go test ./internal/snapshot -run '^Test(ManagerRetainsUnknownTreeAndLock|ManagerReleaseWaitsForInFlightTreeStart|ManagerRetainsTreeWhenLeaderExitsBeforeWriter|ReleaseKillsSubprocessTreeBeforeUnlock|RunManagedCancellationKillsTree)$' -count=10 -timeout 3m
go test -race ./internal/snapshot -run '^Test(ManagerRetainsUnknownTreeAndLock|ManagerReleaseWaitsForInFlightTreeStart|ManagerRetainsTreeWhenLeaderExitsBeforeWriter|ReleaseKillsSubprocessTreeBeforeUnlock)$' -count=1 -timeout 90s
gofmt -l internal/snapshot/process.go internal/snapshot/process_unix.go internal/snapshot/process_windows.go internal/snapshot/process_unsupported.go internal/snapshot/manager.go internal/snapshot/ownership_test.go
```

Expected: all available platform tests pass, no race is reported and gofmt
prints no paths. If a platform cannot be run, record that limitation.

## Task 4: Return ownership-release failures to the caller

**Goal:** a service operation cannot appear successful when its store lock
could not safely be released.

**Files:** `internal/snapshot/service.go`,
`internal/snapshot/service_test.go`.

**Steps:**

1. Change `Service.withStore` to a named error return and join `fn`'s error
   with any `m.Release()` error using `errors.Join`. Keep the warning log and
   retained-lock behavior. Preserve an operation error's typed cause and
   return nil only when both operation and release succeed.
2. Add `TestServiceWithStoreReturnsReleaseFailure` using a per-instance
   manager/lock test fixture with a controlled failing drain: assert an
   otherwise successful operation returns the injected error and a second
   owner cannot acquire. Add `TestServiceWithStoreJoinsOperationAndReleaseErrors`
   and verify both causes with `errors.Is`. Let the drain succeed and retry
   release during cleanup so no test-owned lock remains held.

**Verify:**

```bash
go test ./internal/snapshot -run '^TestServiceWithStore(ReturnsReleaseFailure|JoinsOperationAndReleaseErrors)$' -count=1 -timeout 90s
gofmt -l internal/snapshot/service.go internal/snapshot/service_test.go
```

Expected: tests pass and formatting prints no paths.

## Final verification and integration

```bash
CGO_ENABLED=1 go build -o /private/tmp/marshal-test-p1b ./cmd/marshal
go test ./internal/snapshot -count=1 -timeout 3m
go test ./internal/snapshot -run '^TestRunManagedCancellationKillsTree$' -count=30 -timeout 90s
go test -race ./internal/snapshot -run 'Test(StoreLock|ManagerRetainsUnknownTreeAndLock|ManagerReleaseWaitsForInFlightTreeStart)' -count=1 -timeout 90s
go test ./internal/app/tui/... -count=1 -timeout 2m
go test ./... -count=1 -timeout 5m
go vet ./...
gofmt -l internal/snapshot
USABILITY_MARSHAL_BINARY=/private/tmp/marshal-test-p1b go test ./test/usability/... -run TestScripted -v -timeout 5m
git diff --check
```

Formatting must print no paths. Run the owned-child test on macOS and Linux;
run Windows-specific tests on Windows where available and report any missing
platform. Compare the full-suite result to the P1a baseline, recording P2
failures separately. Do not mark P1 complete unless repeated cancellation,
lock retention and the default-temp-root snapshot suite pass. Update the
process-group audit finding and roadmap with actual results, including the
transient-EPERM evidence and remaining Windows limitations. Commit named
files and documentation with the work.

## Plan self-review

- EPERM is retried only as uncertainty, never accepted as exit. ESRCH or the
  platform's equivalent remains necessary before an owner can unlock.
- Failed drain retry and tree lifetime are independently tested; release and
  start interleavings are controlled rather than inferred from sleeps.
- P1a is integrated. No implementation occurred in this planning turn.
