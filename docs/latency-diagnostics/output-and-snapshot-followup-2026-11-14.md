# Completed output and snapshot follow-up

Investigation date: 2026-11-14. PR [#69](https://github.com/alecpullen/marshal/pull/69)
landed on main as `cef1e353`. This follow-up lives on
`codex/output-snapshot-investigation`, advanced to that merge commit. It adds
opt-in diagnostic tests and evidence only; no production cache or snapshot
behavior has changed. The installed binary, daemon and live configuration were
not used or changed.

## Findings and recommended order

1. Avoid constructing retained command items that a durable terminal record
   already supersedes. Then cache only the cleaned tail of settled output,
   with bounded retention, exact input validation and coordinator-owned locking.
2. Reuse compression/decompression state and scratch buffers within one
   snapshot capture. For buffered blobs, compare stored bytes against the
   source bytes already read and hashed, rather than hashing the stored bytes
   a second time.
3. Re-run the real-client workload from `real-repo-method.md` after integrating
   each change. These component measurements do not establish a new end-to-end
   turn latency or idle CPU result.

The earlier real-client profile attributed 61.88% of sampled quiet CPU to the
work coordinator and 81.57% of the separate allocation profile to snapshot
capture. See `real-repo-results-2026-11-14.md` for workload and limitations.

## Completed command output

`internal/app/runtime.go` reconciles work every 200 ms. Its `commandCandidates`
first projects durable session exits, then projects retained manager jobs. It
constructs a retained `commandItem`, including cleaning its entire output,
before checking whether the durable terminal item already wins. That item can
then be discarded. Even the winning durable output is cleaned on every tick.
`cleanWorkTail` strips terminal sequences, sanitizes and redacts the whole
input before keeping an 8 KiB tail; repeated regular-expression work dominates
this fixture. Truncating before redaction would change semantics.

The probe uses two settled commands with real `go test -json` output captured
from `./internal/tools/registry` (77,893 bytes per retained job). Durable exits
go through the production `State.AddJobExit` path and its 16 KiB tail cap plus
truncation marker. It verifies both projections are identical before measuring
the durable-only case.

Three samples, median per projection of both commands:

| Projection | Time | Allocated bytes | Allocations |
| --- | ---: | ---: | ---: |
| Current durable and retained records | 45.530 ms | 8,521,900 | 75,670 |
| Only the durable records that win | 7.588 ms | 1,519,808 | 13,012 |

Avoiding discarded work reduced this component's time by about 83%. The second
row omits the retained list entirely for this all-settled fixture: it estimates
the available saving, rather than benchmarking an integrated early-guard fix.
Production must retain the current precedence, epoch, settling and identity
rules for every other case.

A separate two-entry exact-input memo probe measured two warmed hits at about
41 ns and zero allocations, versus 7.873 ms cleaning two durable tails or
37.794 ms cleaning two retained outputs. This isolates the avoided cleaning;
it excludes miss costs, locking, eviction, source snapshot collection and all
other reconciliation. Hits use the same immutable string backing as the
settled source snapshots. Equal strings with different backing may require a
linear comparison. These figures are not whole-coordinator timings.

### Proposed cache contract

- Own the cache in the work coordinator, rather than a process-global pool.
  Use canonical work identity (conversation, source epoch, execution or legacy
  identity) and result revision, and require exact raw-input equality on hits.
  `ResultRevision` is computed from settlement metadata, not an output digest;
  `AddJobExit` can replace output under the same identity and revision.
  Implementation review also identified a mutable cleaning dependency:
  `redact.RegisterSecret` adds literal secrets during a session. Cache hits
  must validate that registry's generation, and recompute from the full raw
  input if it changes, including secrets crossing the tail clipping boundary.
- Cache derived cleaned text only. Rebuild state, capabilities, save errors,
  acknowledgments, attachment and progress from fresh snapshots. Running
  progress can change without a lifecycle revision, so caching an entire work
  view by that revision is unsafe.
- Bound both entry count and retained raw-plus-clean bytes; bypass oversized
  entries and evict history that disappears. A starting design could cap at
  64 entries and 4 MiB, but those bounds need workload validation. Raw strings
  retain their backing storage; count that retention conservatively or copy
  bounded inputs on insertion. Avoid retaining a large backing allocation
  through a small substring.
- Reconciliation can also run from action paths concurrently with the timer.
  Protect cache access with its own lock, compute misses outside source locks,
  and recheck inputs before publishing. Late concurrent insertion must never
  let different raw input return stale sanitized text. Preserve full-input
  cleaning, UTF-8 clipping and secret masking.

Before integration, add focused behavior/race coverage for corrected output
with unchanged revision, restarted epochs, legacy IDs, eviction, concurrent
reconciliation and fresh action/progress fields. The current probe validates
cleaning equivalence and same-key input changes; it is not a production cache.

## Snapshot work

`internal/snapshot/object_writer.go` already reads and hashes small sources
before checking whether their loose object exists. Existing installed objects
are reopened, decompressed and hashed to verify their names; new objects get
fresh zlib writers and additional validation. The per-object codec and copy
buffer allocations recur across thousands of files and captures.

The probe encodes real `go.mod`, `internal/agent/metrics.go` and
`internal/app/runtime.go` bytes as Git loose blobs with the production
`NoCompression` setting. It compares fresh codecs with warmed codec state
reused across objects in a capture. Hash checks use the same precomputed
expected digest in both reader variants. A third reader variant compares
decoded bytes directly to the already buffered source, including its header,
and requires exact length plus zlib checksum and EOF.

Three samples, median time per object:

| Source | Fresh writer | Reset writer | Fresh reader + hash | Reset reader + hash | Reset reader + byte comparison |
| --- | ---: | ---: | ---: | ---: | ---: |
| go.mod | 30.878 µs | 0.901 µs | 4.607 µs | 2.182 µs | 1.137 µs |
| agent/metrics.go | 33.241 µs | 2.621 µs | 8.367 µs | 5.895 µs | 2.950 µs |
| app/runtime.go | 73.761 µs | 41.322 µs | 90.732 µs | 87.215 µs | 44.558 µs |

Fresh writer allocation was about 733,178 bytes (716 KiB), 15 allocations per
object; warmed reset was zero. Fresh reader variants allocated 44,204–73,516
bytes in 10 allocations, reset/hash 188 bytes in four, and reset/compare 56
bytes in three. Setup of reusable state is excluded from the warmed rows and
is still paid once per capture. Large-file hashing dominates reader time:
reader reuse alone barely improved the largest example; exact comparison to
already hashed bytes roughly halved its verification time.

These are in-memory codec experiments, not complete snapshot implementations.
They omit directory walks, filesystem opens, source validation, Git processes,
budgets, syncing and publication. They do not prove those remaining costs can
be removed or predict how much the earlier roughly 490 ms captures will fall.

### Proposed snapshot contract

- Keep scratch state on each capture's object writer. Reset codecs for each
  object, release file references after use, and discard state at capture end.
  Reuse bounded source/copy buffers; avoid global plaintext pools shared across
  workspaces. The buffered path is currently limited to 8 MiB.
- For small blobs whose exact source bytes are already retained and hashed,
  stream-compare the existing object's header and body to those bytes, with
  exact length, verified checksum and EOF. Preserve filesystem/type checks and
  corruption errors. This proves byte equality without a second content hash.
  Large streamed sources keep the hash-based path. A later change could also
  avoid rehashing buffered bytes while writing a new object, but this probe did
  not measure that separately.
- Preserve source length/identity rechecks, cancellation, storage budgets,
  independent Git agreement, sync ordering and publish-after-verification.
  An object name, size or mtime alone is not proof that stored data remains
  correct across captures. Do not introduce a metadata-only verified-object
  cache.
- Keep capture frequency in this first change. Turn-start capture also updates
  the catalog and surfaces diagnostics even though its returned hash is not
  recorded as a pre-write snapshot. Deferring or removing it changes behavior
  and needs a separate design, rather than being bundled with buffer reuse.

The semantic probe checks fresh/reset encoded byte equality, checksum errors,
truncation, invalid headers, wrong decoded bytes, excess decoded bytes and a
valid reset after errors. Production integration must also retain the existing
store-level corruption, source-change, budget, cancellation and durability
tests, and add capture-level reuse coverage.

## Reproduction and evidence

Measured with Go 1.26.1, darwin/arm64, Apple M5. Benchmarks ran sequentially
with `-benchtime=300ms -count=3`, source at `4b5ab9d6`. Relevant source/corpus
files are byte-identical at follow-up base `cef1e353`. These short samples are
directional component measurements, not statistical confidence intervals.

Raw logs, exact registry output fixture, corpus SHA-256 hashes, individual
samples and medians are in `output-snapshot-probe-2026-11-14/`.

Both focused semantic tests passed again on `cef1e353`, and tagged vet passed
for `./internal/app ./internal/snapshot`. The full default suite was not rerun
for these build-tagged, investigation-only additions; no production code or
default test behavior changed.

From the repository root:

```sh
MARSHAL_LATENCY_OUTPUT_FILE="$PWD/docs/latency-diagnostics/output-snapshot-probe-2026-11-14/registry-output.jsonl" \
CGO_ENABLED=1 go test -tags=latencyprobe ./internal/app \
  -run '^TestSettledTailMemoProbePreservesCleaning$' \
  -bench 'BenchmarkCompleted(CommandProjection|TailCleaning)Probe' \
  -benchmem -benchtime=300ms -count=3 -timeout 3m

MARSHAL_LATENCY_SOURCE_DIR="$PWD" \
CGO_ENABLED=1 go test -tags=latencyprobe ./internal/snapshot \
  -run '^TestCodecReuseProbePreservesBytesAndRejectsCorruption$' \
  -bench '^BenchmarkSnapshotCodecReuseProbe$' \
  -benchmem -benchtime=300ms -count=3 -timeout 3m
```

To capture a fresh output fixture independently:

```sh
CGO_ENABLED=1 go test -json ./internal/tools/registry -count=1 -timeout 2m > /tmp/registry-output.jsonl
```
