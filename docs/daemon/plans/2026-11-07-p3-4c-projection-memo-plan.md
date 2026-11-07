# P3 plan 4c: The projection's cost — implementation plan

Date: 2026-11-07. Status: ready for review.
Base: `main` at `6fecfe18` (P3 plans 2 and 6a merged). Every anchor below was checked there. The full suite passed there: 119 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-next-suite`, `-timeout 20m`; `web/bridge` 87s).
- **It runs alongside P3 plan 3b** ([status](2026-11-07-p3-3b-status-plan.md)). Plan 3b changes `internal/acp/session.go`, `host.go`, a new `status.go`, `internal/acp/service/events.go`, `internal/acpclient` and `internal/acpwire`. This plan changes none of them. Both touch `internal/acp/ordering_test.go`, in different functions: plan 3b changes the `responseBarrier` call in `install` (`:153`), this plan one line at the top of `TestResponseBarrierBudget` (`:544`). Each adds its own Implementation record entry to `docs/daemon/p3-acp-transport.md`.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:` and Migration order item 4 (this plan, and plan 4b after it). Task 4 adds only the Implementation record and a finding.
- Nothing a user sees changes. The transcript the agent sends and the one the TUI renders stay byte for byte the same; they only cost less to build.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R4 (the transcript's cadence), R10 (budgets), Migration order item 4, and tests V4 and V8.
Previous plan: [P3 plan 4a, the stack over the wire](2026-11-05-p3-4a-stack-wire-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Why now.** On the 2,000-call session, one agent flush after one new tool call takes 7.8ms (plan 4a measured 8.4ms; 7.84ms/op at `6fecfe18`). Plan 3a's barrier makes every call that changes the transcript pay one flush before it answers (8.47ms). R4's 50ms cadence spends that every 50ms while a long turn streams. And the TUI pays most of it today, in process, on every viewport refresh: `service.Stack` runs the whole projection each time. A throwaway benchmark of each stage at `6fecfe18`, on the same session (1,201 nodes), split the flush like this:

| Stage | Time | Allocated |
|---|---|---|
| `SnapshotOf` (copy the transcript) | 0.24 ms | 1.2 MB |
| `Build` (group into turns, beats, rows) | 2.54 ms | 11.3 MB |
| `Project` (each node's wire form) | 1.89 ms | 5.4 MB |
| encode every node (the flush's diff) | 1.94 ms | 2.8 MB |
| `ProjectState` (snapshot, build, project, band) | 5.81 ms | 19.5 MB |
| one flush (`BenchmarkStackFlushLargeSession`) | 7.84 ms | 23.1 MB |

After one change, almost every node's wire form and encoding is what it was. This plan remembers them. `Build` stays whole: making it incremental needs stable identities for transcript items, which `session.State` does not give (each `Transcript()` call copies every item). That is a later plan's work, and this plan records how much of the cost it leaves.

**Goal:** a projection after a change re-projects and re-encodes only the nodes that changed.
- `viewmodel.WireMemo` remembers each node's wire form and encoding from its last projection, and reuses them for a node whose version, parent, children and working directory are unchanged.
- The agent's stack projector projects through a memo and takes each node's encoding from it.
- The service's `Stack`, which the TUI reads today, projects through memos too.
- A check that tests turn on compares every memoized projection with a fresh one.

**Non-goals:**
- **No change to `Build`, `SnapshotOf`, `session.State` or any node version.** The memo trusts the versions as they are.
- **No change to the wire.** Patches, snapshots and their bytes are what they were.
- **No change to R4's cadence or R10's budgets.** Plan 4b decides the cadence, with this plan's numbers.
- **No change to `internal/app/tui`'s code.** Only its `main_test.go` turns the check on.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-4c-suite`. Create the directory first: `mkdir -p /private/tmp/p3-4c-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 3b uses `/private/tmp/p3-3b-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 4 likelier, the budget tests most of all. Rerun a failure alone before treating it as new.
- **The projection.** `ProjectState(st, busy, now)` (`internal/viewmodel/project.go:32`) takes a snapshot (`SnapshotOf`, `:12`), builds the turns (`Build`, `stack.go:223`), projects them (`Project(turns, workdir)`, `wire.go:59`), and appends the band node (`ProjectBand`, `wire.go:17`), which is not in `Roots`. `Project` walks each root depth-first and calls `wireNode(n, workdir)` (`wire.go:77`) for every node, setting `Parent`. `workdir` is the active root; `wireTools` uses it to elide each call's subject (`CallSubject`).
- **What a node's wire form reads.** `wireNode` reads the node's ID, kind, `Live`, `Version`, children IDs and payload, and `workdir`. Nothing else.
- **Versions.** `Node.Version` (`stack.go:60`) is "a hash of every payload field the renderer reads". Tool rows (`versionOfTools`), items (`versionOfItem`), beats (`versionOfStep`, which folds in each child's ID and version), completions, plan rows, task headers and receipts set it. Turn nodes leave it 0 and carry no payload. An in-flight call (`activeNode`, `stack.go:728`) leaves it 0 and is `Live`. `versionHex` writes 0 as an empty string (`wire.go:257`).
- **The TUI already trusts versions.** Its render cache (`internal/app/tui/viewport_render.go:275`) reuses a node's rendered rows while the node and its descendants are not live and its `Version` is unchanged. A version that missed a payload field would already show as a stale row in the TUI.
- **The agent's projector** (`internal/acp/stack.go`). `stackProjector` (`:31`) keeps, under its mutex, the encoding of every node it last sent (`sent`, `sentOrder`, `sentRoots`). Two paths project, both under that mutex, and both end by recording what the client holds:
  - `flushProjectorFor` (`:185`) calls `ProjectState` (`:193`) and `diff` (`:75`), which encodes every node with `json.Marshal`, compares each with `sent`, and replaces `sent` when anything changed;
  - `stackSnapshot` (`:317`) calls `ProjectState` (`:324`); on activation it encodes every node into `sent` (`:334`), otherwise it diffs.

  A projector is created by `stackFor` and deleted by an unsubscribe or `dropStacks`, so a new subscription always starts with a new projector.
- **The service's stack** (`internal/acp/service/stack.go:12`) answers `Stack(subagentID, busy)` with `ProjectState(src, busy, src.Now())` (`:23`) on every call. The TUI calls it from `refreshViewport` (`internal/app/tui/viewport_render.go:99`), on the `Update` goroutine.
- **Benchmarks and budgets.** `BenchmarkStackFlushLargeSession` (`stack_wire_test.go:464`) flushes the 2,000-call session (`largeStackSession`, `:333`) after one new call. `TestResponseBarrierBudget` (`ordering_test.go:544`) logs the median of a call that adds one message (8.47ms in plan 3a). `TestStackPatchApplyBudget` (`stack_wire_test.go:496`) and `TestStackRoundTripBudget` (`:529`) hold R10's 2ms and 50ms. All three skip under `-short` and with the race detector (`raceEnabled`).
- **Test entry points.** `internal/acp/main_test.go`, `internal/acp/service/main_test.go` and `internal/app/tui/main_test.go` each call `testenv.Run(m)`. `internal/viewmodel` has no `TestMain`. Only `internal/acp/fence_test.go` uses `t.Parallel()` among these packages.

**Decisions this plan makes beyond the spec:**

1. **The memo keys a node by what its wire form reads:** its ID, `Version`, `Parent`, children IDs, and the working directory. Kind is part of the ID's identity, and the payload is covered by `Version`, which is the same promise the TUI's render cache relies on.
2. **Live nodes and nodes with `Version` 0 are always projected afresh.** A live node's payload can change without its version (an in-flight call's output), and a node without a version has nothing to compare. Turn nodes are cheap: they carry no payload.
3. **The memo holds only the last projection.** Each call keeps the entries of the nodes it returned and drops the rest, so it never holds more than one tree.
4. **Encodings are made on demand and kept.** The agent's diff asks for every node's encoding; the service never does, so the TUI's path pays no encoding.
5. **Reused values are shared.** A reused node's slices and pointers are the ones returned before. Callers must not change a returned node, which `rpc.Stacks` already requires of its readers.
6. **A check proves the memo in every test that projects.** `viewmodel.CheckWireMemo`, when set, makes each memoized projection also project afresh and panic on any difference. The test entry points of `viewmodel`, `acp`, the service and the TUI set it, so the whole TUI suite, T6's goldens included, and every stack test run against it. Budget tests and benchmarks turn it off while they measure.
7. **The service keeps two memos per session**: one for the main transcript, and one for the subagent read last. The TUI reads one drilled subagent at a time, and a read of a different subagent resets the second memo.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 3b's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 3b and 4c`. Plan 3b gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-4c-projection-memo` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-4c-projection-memo: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The memo (`internal/viewmodel/memo.go`, new)

```go
// WireMemo remembers the wire form of every node one transcript last
// projected to, so a projection after a change re-projects only the nodes
// that changed. A node is reused when it is not live, its Version is not
// zero, and its Version, Parent, children IDs and the working directory
// are what they were. Reused values share their slices and pointers with
// earlier results: callers must not change a returned node.
//
// The zero value is ready. A WireMemo is not safe for concurrent use.
type WireMemo struct{ /* unexported */ }

// Project returns what Project(turns, workdir) returns, reusing the wire
// form of every unchanged node from the memo's last call. The memo then
// holds exactly the nodes this call returned.
func (m *WireMemo) Project(turns []*Node, workdir string) acpwire.WireTree

// ProjectState returns what ProjectState(st, busy, now) returns, with the
// turns projected through m. The band node is projected afresh.
func (m *WireMemo) ProjectState(st *session.State, busy bool, now time.Time) Projection

// Encoded returns the JSON encoding of the node with id from the memo's
// last call, encoding it on the first ask and keeping it. It reports false
// for an ID the last call did not return, the band's included.
func (m *WireMemo) Encoded(id string) ([]byte, bool)

// CheckWireMemo, when set, makes every WireMemo call also project afresh
// and panic on any difference, naming the first node that differs. Test
// entry points set it; production never does.
var CheckWireMemo atomic.Bool
```

### The memo-check helper (`internal/acp/memocheck_test.go`, new)

```go
// withoutMemoCheck turns viewmodel.CheckWireMemo off until tb ends, so a
// budget test or benchmark measures the memo and not the check.
func withoutMemoCheck(tb testing.TB)
```

## Task 1: The memo

**Goal:** `viewmodel.WireMemo` returns exactly what a fresh projection returns, and reuses every unchanged node.

**Files:** `internal/viewmodel/memo.go` (new), `internal/viewmodel/memo_test.go` (new), `internal/viewmodel/main_test.go` (new).

**Steps:**

1. **Entries.** The memo keeps one entry per node ID from its last call: the key (decision 1), the wire node, and the encoding once asked for. It also counts, in an unexported field, how many nodes its last call projected afresh, for the tests.
2. **`Project`.** Walk the turns exactly as `Project` does: the same order, the same `Parent`, the same `Roots`. For each node, reuse the entry's wire node when the node is reusable (decision 2) and its key matches; otherwise call `wireNode`, set `Parent`, and make a new entry without an encoding. Replace the memo's entries with the ones this call used. A reused node keeps its kept encoding.
3. **`ProjectState`.** Do what `ProjectState` does (`project.go:32`), with step 2 in place of `Project`. Do not change the package-level `ProjectState`.
4. **`Encoded`.** Return the kept encoding, or encode the entry's wire node with `json.Marshal`, keep it, and return it. Report false for an ID with no entry.
5. **The check.** When `CheckWireMemo` is set, `Project` also calls the package-level `Project` and compares the two trees with `reflect.DeepEqual`; on a difference it panics with a message naming the first node ID whose wire form differs. `Encoded` also compares its result with `json.Marshal` of the entry's node. Both checks run after the memo's own work, so the memo is never fed the fresh result.
6. **`main_test.go`.** Set `CheckWireMemo` before `testenv.Run(m)`, as the other packages' entry points call it.
7. **Tests in `memo_test.go`.** Build states with `session.New` and the state's own setters, as `project_test.go` does (`:17`), and drive one memo through a sequence, comparing each result with a fresh `ProjectState` (whole tree with `reflect.DeepEqual`, and every non-band node's `Encoded` with `json.Marshal` of the fresh node):
   - `TestWireMemoMatchesAFreshProjection`: a user message; a step with three calls of one tool (a merged run) and an edit; an in-flight call (live); streaming reasoning; the call settling; the final reply; a second turn; a todo write; a job completion; a subagent card; and the workspace's active root moving to another directory, which changes every call's elided subject.
   - `TestWireMemoReusesUnchangedNodes`: build ten turns of ten `shell.run` calls each, as `largeStackSession` (`internal/acp/stack_wire_test.go:333`) builds its two hundred, project, open a new step with one call, and project again. The second call projects afresh every turn node (their version is 0) and at most five others (the new call, its beat, the turn's receipt), far fewer than the tree holds; and a call node from the first turn returns the same `Tool` pointer as the first projection did.
   - `TestWireMemoNeverReusesLiveOrUnversionedNodes`: two projections of the same busy state: the in-flight call's node and every turn node are projected afresh both times.
   - `TestEncodedReportsUnknownIDs`: the band's ID and an unknown ID report false.
   - `TestTheCheckPanicsOnAStaleEntry`: with the check on, change a kept entry's wire node through the unexported map; the next `Project` panics, naming that node.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/viewmodel/
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/viewmodel/ -count=1
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/viewmodel/ -run 'WireMemo|Encoded|TheCheck' -race -count=3
```

Expected: every `viewmodel` test passes with the check on, the new ones included.

Mutation checks:
- Leave `Version` out of the key. `TestWireMemoMatchesAFreshProjection` fails (the settled call keeps its in-flight form, or an edited row its old one). Restore it.
- Leave the working directory out of the key. The step that moves the active root fails: a call's elided subject changes while its version does not. Restore it.

The children and the parent stay in the key without a mutation check of their own. A beat's version already folds in its children's IDs and versions, so no sequence here moves a child list without a version, but a node's wire form carries both, and the key names everything the wire form reads.

## Task 2: The agent's projector

**Goal:** each stack projector projects through its own memo and takes each node's encoding from it, so a flush encodes only what changed.

**Files:** `internal/acp/stack.go`, `internal/acp/main_test.go`, `internal/acp/memocheck_test.go` (new), `internal/acp/stack_wire_test.go`, `internal/acp/ordering_test.go`.

**Steps:**

1. **One memo per projector.** `stackProjector` gains a `viewmodel.WireMemo`, used only under the projector's mutex.
2. **Both paths project through it.** `flushProjectorFor` and `stackSnapshot` call the memo's `ProjectState` in place of the package's. Each such projection is followed, as now, by recording what the client holds (the activation's encoding, or `diff`), so the memo's last projection is always the one `sent` describes.
3. **`diff` and the activation take encodings from the memo.** For each node, use `Encoded(id)`; for a node the memo does not hold (the band), use `json.Marshal` as now. Compare and record exactly as now. Nothing else in `diff` changes: the same upserts, removals, revisions and roots.
4. **The check.** `internal/acp/main_test.go` sets `CheckWireMemo` before `testenv.Run(m)`. `withoutMemoCheck` stores false, and restores the previous value with `tb.Cleanup`. Call it first in `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, and every benchmark in `stack_wire_test.go` and `ordering_test.go`.
5. **A test against a fresh projection.** `sameAsProjector` (`stack_wire_test.go:169`) compares the mirror with what the projector recorded, so a projector that recorded stale encodings would still agree with its mirror. Add `TestFlushedPatchesMatchAFreshProjection` beside `TestStackMirrorConvergesThroughATurn` (`:220`): the same scripted turn, and after each flush and `settle`, every node of the mirror except the band equals `json.Marshal` of the same node in a fresh `viewmodel.ProjectState` of the state, with the same busy flag, and the mirror holds no node the fresh projection lacks.
6. **Benchmarks beside `BenchmarkStackFlushLargeSession`**, each on `largeStackSession` with one new call per iteration (added with the timer stopped, as that benchmark does):
   - `BenchmarkProjectStateLargeSession`: the package-level `ProjectState`, the TUI's path today;
   - `BenchmarkWireMemoLargeSession`: one memo's `ProjectState`, warmed by one projection before the timer starts.
7. **Measure.** Run the three benchmarks and the three budget tests, and record the numbers for Task 4.
   - The flush is expected near 4ms (the snapshot, `Build` and the band stay). **If `BenchmarkStackFlushLargeSession` is not below 5.5ms, stop and report**: the memo is not reusing what it should.
   - `TestResponseBarrierBudget`'s changing-call median should fall by about as much.
   - `TestStackRoundTripBudget` should not get slower: a first read encodes every node once, as before.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/acp/ -count=1
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/acp/ -run 'Stack|Ordering|AReadAfter|AResponseFollows|Fence' -race -count=2
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/acp/ -run 'Budget' -count=1 -v 2>&1 | grep -E 'median|ok|FAIL|PASS'
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/acp/ -run '^$' -bench 'LargeSession' -benchmem -count=3
```

Expected: every `internal/acp` test passes with the check on, the 4a stack-wire tests and the 3a ordering tests included; the budget tests pass; the flush benchmark is below 5.5ms.

Mutation check: in `flushProjectorFor`, project with the package-level `ProjectState` but keep taking encodings from the projector's memo, which then never advances. A changed node's stale encoding matches what was sent, so it is never upserted, and `TestFlushedPatchesMatchAFreshProjection` fails. Restore it.

## Task 3: The service's stack

**Goal:** the TUI's in-process `Stack` reads project through memos, and the whole TUI suite proves them.

**Files:** `internal/acp/service/stack.go`, `internal/acp/service/service.go`, `internal/acp/service/stack_test.go`, `internal/acp/service/main_test.go`, `internal/app/tui/main_test.go`.

**Steps:**

1. **Two memos per session** (decision 7). `Session` gains a main memo, a memo for one subagent with the ID it serves, and a mutex of their own (not `mu`, which guards the event loop's values and the sheet cache).
2. **`Stack` projects through them.** Subagent 0 uses the main memo. Any other ID that resolves to a child uses the subagent memo, first resetting it to its zero value if it served another ID. An ID that names no child still returns the empty snapshot and touches no memo. Everything else `Stack` returns stays as it is.
3. **The check.** Both `main_test.go` files set `CheckWireMemo` before `testenv.Run(m)`.
4. **Tests in `stack_test.go`.**
   - `TestStackReusesTheMainMemo`: two reads of an unchanged busy state return a reused node's `Tool` pointer unchanged; after a new call, the new node is present.
   - `TestStackReusesTheSubagentMemo`: two reads of the same child, unchanged, return a reused node's `Tool` pointer unchanged.
   - `TestStackResetsTheSubagentMemoForAnotherChild`: reading child 2 after child 1 returns child 2's tree, and reading child 1 again returns child 1's; each equals a fresh `ProjectState` of that child.
5. **The TUI suite.** Run `./internal/app/tui/...` whole. With the check on, every `Stack` the TUI reads, every golden included, is compared with a fresh projection. A panic there means a node changed without its version, or the TUI changed a node it was given. Either way, stop and report which node and which test.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/service/
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/acp/service/ -count=1
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p3-4c-suite go test ./internal/acp/service/ -run 'Stack' -race -count=3
```

Expected: the service and the whole TUI tree pass with the check on, T6's goldens unchanged.

Mutation check: reset the subagent memo on every read. `TestStackReusesTheSubagentMemo` fails. Restore it. (A memo fed the wrong tree still returns a correct one, only with less reuse, so correctness tests cannot catch a mixed-up memo; the reuse tests do.)

## Task 4: The docs and full verification

**Goal:** the spec records what the memo saves and what it leaves.

**Files:** `docs/daemon/p3-acp-transport.md`.

**Steps:**

1. **The Implementation record.** Add a "Plan 4c — the projection's cost" entry after the last entry on `main`. It names `WireMemo`, its key, what it never reuses, `Encoded`, and `CheckWireMemo` and where it is set; the projector's and the service's memos; and the numbers, before and after, on the development machine: `BenchmarkStackFlushLargeSession`, `BenchmarkProjectStateLargeSession` against `BenchmarkWireMemoLargeSession`, `TestResponseBarrierBudget`'s changing-call median, and `TestStackRoundTripBudget`'s median. Plan 3b adds its entry at the same place. If both branches have merged, keep both entries, in merge order.
2. **A finding for what is left.** Under "Findings while drafting", add that `Build` and `SnapshotOf` are now most of a flush (give their share from the numbers), that making them incremental needs stable transcript identities from `session.State`, and that plan 4b decides R4's cadence with these numbers and plan 7 decides whether `AddNote` stays synchronous (R6, R10).
3. **Main may have moved.** If plan 3b merged while this branch ran, merge `main` into the branch before the full verification. Expect a conflict only in the P3 spec's Implementation record. Keep both entries. `ordering_test.go` merges without a conflict: the two plans change different functions.
4. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-4c-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-4c-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, and plan 3b's `TestNoticeCallBudget` once it has merged;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.
5. **A manual run.** Build `CGO_ENABLED=1 go build ./cmd/marshal` and start it in a scratch repository. Run a turn with several tool calls, expand and collapse a row, drill into a subagent if one runs, and move to a worktree if the project has one. The transcript renders as before. Report what you saw.

## Self-review

- **Spec coverage.** R4's cadence and R10's budgets: unchanged, re-measured in Task 2, and handed to plan 4b. V4 (the large transcript within R10's budgets): Task 2's budget tests. V8: Task 4.
- **What could go wrong.**
  - **A version that misses a payload field.** The memo would reuse a stale wire form. The TUI's render cache already depends on the same versions, so such a gap would be a visible bug today. `CheckWireMemo` makes it a test failure in every package that projects, the TUI's goldens included.
  - **A caller that changes a node it was given.** The memo would serve the change back. The check compares with a fresh projection on every call, so the next projection in a test panics.
  - **The memo and the projector disagree about what the client holds.** Both paths that project also record the result, under the same mutex, and a new subscription starts with a new projector, so the memo's last projection is always what `sent` describes.
  - **Smaller gains than expected.** `Build` stays. Task 2 stops at 5.5ms rather than shipping a memo that does not reuse.
  - **Plan 3b.** It touches none of this plan's files except `ordering_test.go`, in another function.
