# Snapshot inventory implementation results

Production change: `c861741c`, on `codex/snapshot-admission-investigation`,
based on `4d2f59c3` / main `011113b4`. Test-only synchronization correction:
`55d9930d`. This follows the measurements in
`snapshot-traversal-investigation-2026-11-14.md`.

## Implementation

Versioned-store accounting now builds a read-phase inventory of directory
aggregates and identities. Global usage, workspace detail and generation
detail reuse those aggregates instead of revisiting every object file.
Discovery similarly derives workspace and generation byte totals from one
walk. Individual object-file metadata is not retained in the inventory.

Inventories are local to an operation; they are never carried through
reconciliation mutations, reused after publication, or cached across captures.
Missing/replaced directories and changed directory identities fail closed.
The walker checks cancellation between entries. Ref readability, manifest
classification and protection remain separate live checks. Conservative
rounding, unknown/staging/symlink accounting and existing legacy-cache behavior
remain intact.

The post-publication rotation decision now measures only its target generation.
It preserves manifest/protection/ref checks and rejects a symlink at the target,
workspace root or `generations` parent. Independent review caught the missing
ancestor checks in the first implementation; both reproducing cases failed
before the correction and passed afterward.

Capture frequency, source/object verification, Git agreement, admission limits,
durability and publication order are unchanged. No daemon or installed-binary
changes were made.

## Matched component results

Three before/after pairs ran sequentially with fresh processes and private
stores. Both binaries used the same public Marshal source fixture at `fd328c3e`
(2,880 regular files / 57,496,726 bytes; 2,879 selected entries), native default
ignore rules, the 2,000,000-byte per-file cap and normal storage limits. Each
process measured five warm `Service.Track` captures with one stored workspace,
then five after seeding three unrelated Marshal-sized workspaces. No competing
builds, tests or benchmarks ran during these measurements.

Median of the three per-process medians, milliseconds:

| Operation | Before | After | Change |
| --- | ---: | ---: | ---: |
| Warm capture, one stored workspace | 515.7 | 429.5 | 16.7% lower |
| Warm capture, four stored workspaces | 732.4 | 536.7 | 26.7% lower |
| Global accounting, one workspace | 70.3 | 29.7 | 57.7% lower |
| Global accounting, four workspaces | 291.5 | 125.2 | 57.0% lower |
| Admission, four workspaces | 431.3 | 239.7 | 44.4% lower |
| Reconciliation, four workspaces | 93.6 | 71.1 | 24.0% lower |
| Workspace/generation accounting, four workspaces | 50.4 | 29.7 | 41.2% lower |
| Selection/planning, four workspaces | 48.4 | 50.7 | similar |
| Reachability, four workspaces | 41.3 | 41.9 | similar |

Only the warm-capture rows exercise the whole capture lifecycle. The remaining
rows are independent, nested/non-additive component calls and must not be
summed. The one-then-four stage order and small growth in primary history remain
fixed within each process; before/after binaries alternate between processes.
OS/source/build caches were warm, not cleared.

Every pair improved the warm-capture median in both store shapes. Counting was
separate from timing and confirms the structural change:

| Existing-object accounting visits per capture | Before | After |
| --- | ---: | ---: |
| Active workspace | 7 | 3 |
| Unrelated workspace | 3 | 1 |

For four stored workspaces, object-accounting queries fell from 50,111 to 18,796.
Both versions reported the same 264,368,128-byte store total at the comparable
end-of-stage state. The new commit created during counting receives only the
fresh post-publication measurement (one visit, previously two). These are
allocation-accounting seam visits, not all filesystem calls: source/object
verification, reachability enumeration and Git still perform their own work.

## Real-client results

Three additional before/after pairs used the real binary through embedded mode,
RPC and an 80x24 PTY, with LSP off, snapshots on and a test-owned scripted
provider. Both agents were built with `-buildvcs=false`; the before agent was
built from an archived `4d2f59c3` tree. Each invocation used a fresh disposable
source copy and private state. The workload remained two inspection/test turns
and twelve source reads. Every run passed its 29-request, 26-successful-audit,
six-pre-write-snapshot and resource-coverage assertions.

Median of three runs per binary:

| Metric | Before | After | Change |
| --- | ---: | ---: | ---: |
| Cold inspection/test turn | 4.851 s | 4.301 s | 11.3% lower |
| Warm inspection/test turn | 2.551 s | 2.201 s | 13.7% lower |
| Twelve-read turn | 0.651 s | 0.551 s | 15.3% lower |
| Median pre-command snapshot per run | 541.8 ms | 442.7 ms | 18.3% lower |
| Sampled client peak RSS | 284.1 MiB | 284.8 MiB | similar |
| Quiet CPU, percent of one core | 5.10% | 4.90% | similar |

All three pairs improved cold and warm turn times. These short controlled runs
do not establish a speedup for every provider/task; the scripted endpoint has
no real inference/network wait. UI completion is polled every 50 ms with two
positive samples required. Sampled RSS can miss peaks and is distinct from
allocation churn. The main result here is lower capture/turn latency, rather
than a measured reduction in retained memory or quiet CPU.

## Separate matched profiles

Before and after each ran ten warm captures with four stored workspaces in
separate profiling processes. Setup and slow counting were excluded. Parent CPU
profiles exclude Git subprocess CPU and wall waits; heap figures use
`alloc_space` end-minus-initial profiles and are sampled allocation estimates.

| Metric for ten captures | Before | After |
| --- | ---: | ---: |
| Profile wall interval | 7.53 s | 5.52 s |
| Sampled parent CPU | 5.50 s | 3.68 s |
| Allocated bytes, entire profile interval | 995,981,096 | 560,086,252 |
| Allocation attributed to global usage | 568,995,567 | 195,137,050 |

Total interval allocation fell about 43.8%; cumulative global-usage allocation
fell about 65.7%. Profiling overhead is included in the interval total. Most
remaining parent CPU is filesystem work: entry/source operations and stored
object verification now exceed accounting. Source selection and reachability
were intentionally not changed.

## Validation and incidental test correction

- Accounting regressions observed three/two/two object visits for global
  usage/workspace/discovery before implementation; each now observes one.
- New tests cover conservative totals and rounding, symlink exclusion,
  directory-only inventory retention, missing/replaced directories, fresh
  growth across calls, cancellation, and target-only rotation with protected,
  legacy, corrupt, unlisted, unreadable-ref and ancestor-symlink guards.
- Existing snapshot tests cover budget observers, rotation/reclamation,
  recovery, unknown entries and publication/source/object proofs. The initial
  full snapshot-package run passed; focused and race checks passed after the
  review-found ancestor correction.
- Repository and tagged vet passed. Independent production review completed;
  its material finding was fixed with reproducing tests.
- Final complete default Go suite passed: 123 packages,
  13,064 test/subtest pass events, no failures; six packages had no tests.
  Event interval 523.1 seconds, excluding binary builds. Scripted usability
  was required using a private freshly built embedded binary. Command, log
  hashes and initial/final validation results are recorded in `validation.json`.

The first complete-suite run failed
`TestACPSendNowCancelsPromptRPCWithoutCancelingTurn/approval`. The same failure
reproduced during 80 repeats on the untouched pre-change source. Its immediate
context assertion raced `context.AfterFunc`: canonical prompt settlement does
not join the asynchronous callback that cancels the derived RPC context.

The test-only correction waits for the actual context closure and registration
of the newer RPC, preserving active-turn and late-answer assertions. Eighty
repeats and ten race-checked repeats passed afterward. Deliberately disabling
cancellation forwarding in a disposable source copy still caused the corrected
test to fail at the expected wait; the copy was restored. ACP/daemon production
code did not change. The complete final suite passed with that correction.

## Evidence and reproduction

`snapshot-inventory-results-2026-11-14/` contains all six paired component runs,
both separate profiles, six real-client observation sets, medians and
source/binary/log hashes. The existing `traversal_probe_test.go` workload and
real-client harness were unchanged between the compared binaries. Reproduce
with `snapshot-traversal-investigation-2026-11-14.md` and `real-repo-method.md`,
alternating before/after binaries with fresh stores/processes. Keep timing,
counting and profiling separate.

Limitations: healthy single-generation benchmark stores, three samples, fixed
component stage order, warm OS caches and short histories. No live-provider,
daemon-transport or OpenCode/Codex comparison was performed. More complex store
states are covered by functional tests, not these timing claims.
