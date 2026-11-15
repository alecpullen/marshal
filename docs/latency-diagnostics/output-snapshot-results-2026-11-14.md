# Completed-output caching and snapshot reuse results

Implemented on `codex/output-snapshot-investigation`, based on merged main
`cef1e353`. Production changes are `8a306691`, `e7be9c13` and `43c2a590`.
PR #69's fixes remain on main; this is their separate follow-up.

## Changes

The work coordinator now avoids constructing retained command items when a
durable terminal item already wins. Settled output uses a coordinator-owned,
thread-safe LRU cache, limited to 64 entries and 4 MiB of owned key, revision,
raw and cleaned strings. Entries require exact raw input, canonical work
identity, result revision and redaction-registry generation to match. Missing
history is pruned; oversized entries bypass caching. Live progress, save
errors, lifecycle state and actions are still projected from fresh snapshots.

Review identified that registered literal secrets can change while a session
runs. The redactor now exposes a generation counter; a registry change forces
cleaning again from the full raw input, including secrets crossing the tail's
clipping boundary. Cleaning that races a registry change retries rather than
tagging stale output with the newer generation. The reproducing test failed
before this correction and passed afterward.

Each snapshot object writer now reuses zlib writer/reader state, its bounded
source buffer and a 64 KiB streaming buffer during one capture. Codecs release
file references after use; no state is pooled across captures or workspaces.
An existing small blob is compared against its already read and hashed source
bytes, including header, exact length, checksum and EOF. Large streaming blobs
retain hash verification. New-object structural validation is streamed rather
than materialized into another complete buffer.

Capture frequency, source identity/length checks, corruption errors, storage
budgets, independent Git agreement and durability/publication ordering are
preserved. The daemon, installed binary and live configuration were untouched.

## Integrated projection benchmark

Same two completed commands and real registry-test JSON fixture as the prior
investigation; three 300 ms samples on Go 1.26.1 / Apple M5 / darwin arm64.
These are warmed projections, not cache misses or complete agent turns.

| Both durable and retained records | Before | After |
| --- | ---: | ---: |
| Median projection time | 45.530 ms | 12.528 µs |
| Allocated bytes per projection | 8,521,900 | 4,773 |
| Allocations per projection | 75,670 | 98 |

Unlike the original two-hit memo experiment, the after row includes the
production cache's locks, owned-input comparison, registry generation check,
pruning and fresh metadata projection. The remaining allocations belong to
that projection. Raw final benchmark output is in the evidence directory.

The production unchanged-blob regression allocated 210,072 bytes per operation
before scratch reuse. It now passes a conservative 16 KiB-per-operation bound
for a roughly 127 KiB unchanged blob, including reading and verifying it.

## Real-client comparison on Marshal itself

The real binary ran through `--embedded`, its RPC client and an 80x24 PTY with
a test-owned scripted provider. Each disposable checkout contains the same
tracked Marshal source at `fd328c3e`: 2,880 regular files / 57,496,726 bytes.
LSP was disabled and snapshots enabled in every invocation's private config.
The workload is unchanged: two inspection/test turns followed by twelve source
reads. Every run asserted 29 model requests, 26 successful tool audits, six
persisted pre-write snapshots and usable samples in all resource windows.

Three preliminary optimized runs initially appeared slower than the historical
measurements. Rather than compare different machine conditions, the baseline
binary was rerun interleaved with the optimized binary: baseline 1, after 1,
baseline 2, after 2, baseline 3, after 3. Each used a fresh source copy, process
and private state. The baseline binary's SHA-256 matches the earlier committed
evidence. No other builds, tests or benchmarks ran during these measurements;
brief read-only profile-inspection commands ran during some paired samples.
The OS page cache was not cleared, and shared build/module caches were warm.

Median of three paired runs per binary:

| Metric | Baseline | Optimized | Change |
| --- | ---: | ---: | ---: |
| Quiet CPU, percent of one core | 27.99% | 5.21% | 81.4% lower |
| Sampled client peak RSS | 327.0 MiB | 257.1 MiB | 21.4% lower |
| Last quiet-window RSS | 85.1 MiB | 86.6 MiB | similar |
| Median pre-command snapshot per run | 613.5 ms | 561.7 ms | 8.4% lower |
| Cold inspection/test turn | 4.801 s | 4.601 s | 4.2% lower |
| Warm inspection/test turn | 2.851 s | 2.700 s | 5.3% lower |
| Twelve-read turn | 0.750 s | 0.851 s | 13.4% higher |

The resource improvement is more consistent than the turn timings. All three
quiet-CPU comparisons favor the optimized binary. Read-turn times span
0.701–0.801 s before and 0.701–1.000 s after; much of that turn is still its
mandatory startup snapshot. The median became roughly 100 ms slower, so these
samples do not establish a general end-to-end speedup. UI completion is polled
every 50 ms and requires two positive samples. Cold build/command variation
and filesystem work also affect these short runs. Last quiet RSS did not
improve consistently: allocation churn and retained memory are different.

## Profiles and remaining work

Separate full-workload and ten-second quiet profiles were taken before the
paired timing runs. Compared with the earlier same-workload profiles:

| Profile metric | Earlier | Optimized |
| --- | ---: | ---: |
| Full workload allocated bytes | 7,143,132,744 | 1,540,582,796 |
| Allocation attributed to snapshot capture | 5,826,841,930 | 783,289,545 |
| Quiet-window allocated bytes | 498,277,505 | 153,621,459 |
| Quiet allocation attributed to coordinator | 341,897,246 | 7,903,017 |
| Quiet sampled CPU | 2.23 s / 10.04 s | 0.47 s / 10.02 s |

Snapshot allocation fell about 86.6%; coordinator quiet allocation fell about
97.7%. These are sampled allocation estimates, not retained heap or RSS, and
the profile runs are independent observations rather than statistical bounds.
Completed-output cleaning no longer appeared in the quiet CPU samples; the
remaining quiet activity is mostly TUI/event-loop/runtime work. Absence from a
short CPU profile does not mean a function has zero cost.

In the optimized full profile, syscalls account for about half of sampled CPU;
snapshot work still accounts for about 48% cumulatively. `measureTree` remains
visible, as do stored-object verification and entry/source operations. The
next investigation should target repeated filesystem traversal and admission
work while retaining source and object proofs. Deferring turn-start snapshots
or trusting object metadata across captures would change protection semantics
and is outside this change.

## Validation and evidence

- Full app package tests passed, including corrected same-revision output,
  fresh action fields, legacy/restarted identities, bounded cache retention,
  eviction/pruning and secret-generation invalidation.
- Full snapshot package tests passed, including source changes, corrupt
  objects, Git disagreements, budgets and durability. New tests also check
  reset after writer/decoder failures and corruption after prior success.
- Focused Go 1.27 race checks passed for cache/concurrent registration and the
  snapshot source/corruption/publication paths.
- Repository-wide vet and tagged diagnostic vet passed.
- Independent review found the secret-registry invalidation issue above; its
  reproducing test failed before the fix and passed after it. No other
  actionable findings remained from the review.
- Complete default Go suite passed: 123 packages, 13,043 test/subtest pass
  events, no failures; 6 packages had no tests. Event interval
  533.6 seconds, excluding the preceding binary build. Scripted usability was
  required and ran using a private freshly built `--embedded` wrapper. The
  command, log hash and validation scope are recorded in `validation.json`.

`output-snapshot-results-2026-11-14/` contains all eleven raw observation sets
(three preliminary runs, six paired runs and two profiles), log hashes,
binary/source archive hashes, benchmark output, five binary profiles and
readable profile summaries. Reproduce with `real-repo-method.md`, using the
fixed `fd328c3e` source fixture and the binary under test; run the two binaries
alternately with fresh fixtures. Component probes remain opt-in under
`latencyprobe`.

Limitations: no live-provider or OpenCode/Codex comparison, no long-history
session, and no daemon transport measurement. RSS was sampled every 100 ms and
can miss peaks; descendant sums can double-count shared pages. LSP memory is
outside this comparison. No claims about those paths follow from these runs.
