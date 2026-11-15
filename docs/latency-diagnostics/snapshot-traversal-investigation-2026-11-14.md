# Snapshot traversal and admission investigation

Investigated current main `011113b4`, containing merged PRs #70 and #71, on
`codex/snapshot-admission-investigation`. This branch adds a build-tagged probe
and evidence only. No production behavior, capture frequency, daemon, installed
binary or live configuration changed.

## Main finding

Repeated storage accounting is a substantial part of warm snapshot latency,
and it scales with other projects' histories. In three fresh-process runs,
normal `Service.Track` captures of Marshal's own source took a median **523 ms
with one stored workspace** and **748 ms with four**. Each existing object in
the active workspace was visited **seven times for allocation accounting**;
each object in an unrelated workspace was visited three times.

The first implementation should build global/workspace/generation byte totals
from one filesystem inventory per accounting phase. It should not cache
verification across captures or change which commands receive snapshots.

## Workload and measurements

The explicitly marked disposable source fixture is the same tracked Marshal
source used in earlier experiments: `fd328c3e`, 2,880 regular files totaling
57,496,726 bytes. The production Git-based selector picked 2,879 entries under
the normal 2,000,000-byte per-file cap and native default ignore rules.

Each process created a fresh private store, captured the primary workspace,
then measured five warm `Service.Track` calls. It next seeded three unrelated
stores with separate copies of the same public source and measured five more
primary captures. Every workspace held one healthy active generation; no
rotation or reclamation was needed. Limits remained at the production defaults
(2 GiB per workspace, 10 GiB global). Store data was about 63 MiB for the first
stage and 252 MiB for the second. A deterministic clock advances one second per
primary capture so each publication adds a distinct commit/ref.

Three fresh processes ran sequentially on Apple M5 / Go 1.26.1 / darwin arm64.
The table is the median of each process's median, in milliseconds:

| Operation | One stored workspace | Four stored workspaces |
| --- | ---: | ---: |
| Normal `Service.Track` | 523.3 | 748.3 |
| Admission (`AdmitCapture`) | 212.3 | 433.9 |
| Global accounting (`UsageContext`) | 74.9 | 289.5 |
| Reconcile current workspace | 93.3 | 93.4 |
| Select paths and estimate reservation | 48.1 | 55.7 |
| Current workspace/generation accounting | 51.5 | 48.6 |
| Current generation reachability | 41.4 | 41.3 |
| One raw recursive store measurement | 21.1 | 83.9 |

Only the first row uses the full production capture lifecycle. Component rows
are separate read-mostly calls against the settled private store; they are
nested/non-additive and must not be summed into a capture time. Admission
includes reconciliation, selection and usage. Workspace/generation accounting
is the function the post-publication rotation check calls. Reachability is
already included in reconciliation.

The raw recursive measurement agrees with `UsageContext`'s global byte total in
this fixture, but supplies no generation refs, protection, quarantine or
recovery facts. It is a lower-bound comparison, not a replacement for admission
and not an estimate of the integrated speedup.

The stage comparison has a fixed order. The primary workspace grows from seven
published refs to thirteen while neighboring stores are seeded with one ref
each. Source and OS caches are warm; "cold" in raw records means an empty
snapshot store, not a cold filesystem. These are descriptive scaling
observations rather than a controlled causal estimate. The repeated visit
counts provide independent evidence of why unrelated stores add work.

## Where the repeated visits happen

A separate, intentionally slow counting pass wraps the manager instance's
allocation-size seam and records call paths. It is disabled during all timing
and profiling. These are **accounting visits**, not total filesystem traversals
or syscall counts: source verification, `looseObjects` enumeration and Git
reachability also perform work outside this seam.

For every preexisting active-workspace object, all three runs observed:

| Call path | Accounting visits per object |
| --- | ---: |
| Reconciliation: workspace total, then generation total | 2 |
| Global usage: whole v2 tree, workspace total, generation total | 3 |
| Post-publication rotation check: workspace total, generation total | 2 |
| **Total** | **7** |

The newly created commit is observed only in the two post-publication passes.
In the one-workspace counting pass, 3,132 old objects had seven visits apiece;
the new commit had two, totaling 21,926 object-accounting queries. With four
stored workspaces, the current workspace's 3,138 old objects still had seven
visits, the three neighbors' 9,381 objects had three apiece, and the new commit
had two: 50,111 object-accounting queries. Raw logs preserve the per-path
histograms and phase/workspace totals. The pattern is observed, not hardcoded
as a test expectation.

The source paths explain it:

- `reconcile.go`: `discoverV2Workspace` measures the complete workspace, then
  `discoverGenerations` remeasures each generation. Discovery also lists refs
  and calls `UnreachableBytes`, including a Git `rev-list`.
- `usage.go`: `usageWith` measures the complete v2 tree, then calls
  `measureV2Workspaces`; `MeasureWorkspaceGenerations` measures each complete
  workspace and then each of its generations again.
- `admission.go`: `AdmitCapture` invokes reconciliation and global usage.
  `AdmitAndCaptureWithCleanup` later calls `overRotationTarget`, which asks for
  the current workspace's full generation accounting again, although the
  decision concerns only the active generation.

The counted warm capture started three `for-each-ref` commands with one
workspace and six with four; each also started one `rev-list`, one `ls-files`
and one `hash-object`. This records Git commands crossing the manager's process
tree seam, not every possible OS process invocation.

## Separate profile

One additional probe process profiled ten warm `Service.Track` captures with
four stored workspaces, after setup, timing and counting had completed. CPU
sampling covered 7.64 seconds and recorded 5.48 seconds of parent-process CPU:

- Recursive `measureTree`: 2.79 s cumulative, 50.9% of sampled parent CPU.
- `UsageContext`: 2.16 s cumulative, 39.4%.
- `AdmitCapture`: 2.74 s cumulative, 50.0%.
- `captureToGeneration`: 1.92 s cumulative, 35.0%.

Those paths overlap. Parent CPU profiling excludes Git subprocess CPU and
wall-clock waits; the unprofiled wall-time table includes them.

An `alloc_space` end-minus-initial heap profile estimates 1,005,285,708 bytes
allocated during the profile interval. Of that, `measureTree` accounts for
753,592,255 bytes (75.0%), and `UsageContext` for 563,226,676 bytes (56.0%,
overlapping). Directory-entry information, stat results, path strings and
recursive measurements dominate. This is allocation churn, not retained heap
or RSS, and profiling/runtime overhead is included in the interval total.

## Concrete implementation recommendation

1. **Fuse v2 usage accounting.** Collect each directory's aggregate once and
   derive global, workspace and generation totals from that same traversal.
   Keep unknown entries, metadata, staging and symlink accounting conservative;
   preserve existing legacy-store cache behavior and fresh global-limit reads.
   Continue listing refs and reading protection/corruption state as required.
2. **Narrow the post-publication rotation measurement.** Measure the active
   generation directly, with its current manifest/protection/ref-read checks,
   instead of remeasuring the whole workspace and all generations.
3. **Reuse the same inventory approach during discovery.** Produce workspace
   and generation byte totals together before reconciliation acts on them.
   Do not carry stale measurements through reconciliation mutations.

Together these could reduce preexisting active-object accounting visits from
seven to three and unrelated-object visits from three to one. That is a
structural target, not a measured speedup. Each phase remains fresh at the
point it runs, including the post-write rotation decision.

Keep reachability scanning for now. It costs about 41 ms here, but admission
must still detect objects abandoned by failed captures; a clean capture record
alone is not sufficient proof that no abandoned objects exist. A reachability
fast path would need a separate durable dirty-state/recovery design.

Before implementing the inventory, cover accounting parity for multiple and
unlisted generations, legacy stores, unknown entries, staging, symlinks,
corrupt manifests/unreadable refs, cancellation, rotation/reclamation and
write-boundary budget tests. Then repeat these measurements and the real-client
workload. This investigation does not claim those harder store states are
represented by the healthy fixture.

## Reproduction and validation

Probe: `internal/snapshot/traversal_probe_test.go`, build tag `latencyprobe`.
Prepare the fixed marked disposable fixture using `real-repo-method.md`, then:

```sh
MARSHAL_SNAPSHOT_PROBE_REPO=/absolute/path/to/disposable/repo \
CGO_ENABLED=1 go test -tags=latencyprobe ./internal/snapshot \
  -run '^TestSnapshotTraversalProbe$' -v -count=1 -timeout 5m
```

Run three separate processes sequentially. For a separate fourth profile, set
`MARSHAL_SNAPSHOT_PROBE_PROFILE_PREFIX` to an absolute temporary output prefix.
Analyze allocation churn with `go tool pprof -alloc_space -base` using the
initial and final heap files. The fixture marker establishes deliberate opt-in;
the source archive hash in the evidence establishes the pinned input.

All three timing/count runs and the separate profile run completed both stages
and passed. Selector sanity, cleared capture state, workspace count,
zero-unreachable-object fixture, classification coverage, and one-walk byte
total equality were asserted; the exact 2,879-entry selection was recorded.
Tagged vet passed; a separate full probe under
the Go 1.27 race detector passed (its timings are excluded). Independent probe
review found no required code changes. The default full suite was not rerun:
this branch changes no production code or default-build tests.

`snapshot-traversal-2026-11-14/` holds the completed logs, individual
observations, summaries, source/binary hashes and separate profiles. The
installed agent, daemon, real stores and real credentials were never used.
