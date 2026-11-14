# Latency while Marshal works on Marshal

The real-client benchmark reproduces substantial local overhead with immediate
scripted model replies. Repeated snapshots dominate the measured turn latency;
automatic language servers dominate child-process memory; and completed command
output keeps consuming CPU after the turn has finished. This pass measures these
contributors and makes no production performance change.

## Workload and evidence

The binary runs through `--embedded`, the normal RPC client seam and an 80x24
terminal on a private Git checkout of the actual Marshal source at `fd328c3e`:
2,880 tracked regular files, 57,496,726 bytes. Each run has private state, home,
configuration and a test-owned localhost provider. It never uses the running
daemon, configured providers or existing sessions.

Each process performs two inspection-and-test turns: todo updates, a real
source read, targeted repository search, Git status, an actual
`go test -json ./internal/tools/registry -count=1 -timeout 2m`, Git diff and todo
completion. A third turn reads twelve distinct source files. Every run verifies
29 requests and 26 successful persisted tool calls, including completed test
commands. The native tool surface has 23 schemas and a 131,072-token window.
Titling is disabled so the completion sequence is deterministic.

Three fresh processes per configuration ran sequentially, interleaved by
configuration. "Cold" means first task with fresh Marshal state and snapshot
store. Go build/module caches and OS file caches are warm; OS caches were not
flushed. Direct command controls use the same Bash/pipefail invocation, private
home, filtered environment and Go caches. The direct warm test command takes
roughly a quarter second. No unrelated builds or test suites overlapped the
measurements. Machine load remains uncontrolled.

Raw request sizes/times, per-process samples, tool timing phases, audits,
snapshot/index counts, binary/source hashes and the separate profile runs are
in [the dataset](measurements-real-repo-2026-11-14.json). The
[method](real-repo-method.md) includes reproduction and isolation details.

## Repeated timing results

Values are medians of three process runs. Turn elapsed time runs from Enter
through the completion marker appearing on screen. The observer polls at 50 ms
and requires two positive snapshots, adding roughly 50–100 ms of detection
latency. Input is pasted and observed before starting that timer.

| Private fixture configuration | First inspection + tests | Repeated inspection + tests | Twelve source reads |
| --- | ---: | ---: | ---: |
| Defaults, automatic LSP on | 4.55 s | 2.35 s | 0.60 s |
| LSP off, snapshots on | 3.50 s | 2.35 s | 0.60 s |
| LSP off, snapshots off | 0.40 s | 0.40 s | 0.10 s |
| LSP off, snapshots on, ignored-volume proxy | 3.50 s | 2.35 s | 0.60 s |

Startup to the ready composer is about 4.42 seconds across these cases. This
pass does not isolate that startup floor. The disabled settings are experimental
controls, not changes to the developer's configuration.

### Snapshot work blocks the loop even when the source is unchanged

With LSP disabled in both sides of the comparison, disabling snapshots removes
about 1.95 seconds from the warm inspection/test turn. Each turn does three
pre-write shell captures plus a turn-start capture. The read-only third turn
still pays for its turn-start snapshot: the first model request arrives about
493 ms after Enter with snapshots, versus 3 ms without them. Its twelve tool
calls then finish quickly.

The median shell pre-write snapshot phase is about 489 ms in the LSP-off case.
The actual Go test handler takes about 251 ms. The source tree does not need to
change for that snapshot cost to recur. All enabled runs verified six durable
pre-write snapshot records and no capture warnings; the disabled controls
verified zero records. Turn-start captures do not create those audit rows.

A separate full-workload profile, with LSP off, estimates 6.65 GiB of cumulative
allocations. `snapshotCapture` accounts for about 5.43 GiB (81.6%). Compression
writers, decompression readers, blob verification and copy buffers dominate.
These are allocations churned through the garbage collector, not 6.65 GiB of
resident memory. Median sampled client RSS peaks in the timing runs are around
277–306 MiB; the snapshots-off control does not show a lower RSS peak despite
its much lower elapsed time.

### Completed output is repeatedly cleaned during quiet time

After the tools finish, the unprofiled client continues using about 27% of one
CPU core through the ten-second quiet window. This persists with both LSP and
snapshots disabled.

The separate idle-only profile contains 2.23 CPU-seconds over 10.04 seconds.
`workCoordinator.loop → reconcileWork → commandCandidates → commandItem →
cleanWorkTail` accounts for 61.9% of sampled CPU. The path repeatedly calls
`progress.Clean` and `redact.Secrets` on retained command output.

The end-minus-initial heap profile estimates about 475 MiB allocated in that
quiet window, with about 326 MiB (68.6%) under the work coordinator. These are
sampled allocation estimates. They are not retained heap or RSS measurements.
The source explains the recurring work: the coordinator reconciles at least
every 200 ms, and `cleanWorkTail` cleans/redacts the whole supplied output
before bounding it to an 8 KiB tail. The two completed JSON test outputs are
about 78 KB each and remain available to this path.

### Automatic language servers have a large cold footprint

With default automatic LSP discovery, the median sampled peak for one `gopls`
process is 1,833 MiB. The median peak sum of owned descendants' RSS is about
2,021 MiB, compared with about 142 MiB when LSP is off. The latter still includes
Go test/build subprocesses. Main-client median RSS peaks are about 304 MiB and
277 MiB respectively.

These are sampled RSS figures, so transient peaks can be missed. Summed
descendant RSS can count shared pages more than once. The language servers
start with a private, cold user cache; this is not a measurement of an already
warm daemon's steady-state language-server memory. The clean source fixture
also discovers more language servers than the usual checkout's first-5,000-file
walk, which recognized Go, HTML, Markdown and YAML. Only Go among those has a
built-in default language-server mapping. The developer's actual LSP settings
were not loaded.

## Ignored-directory sensitivity and a scanner discrepancy

A metadata-only inventory of the usual checkout found about 2.61 GB in 39,180
ignored regular files. The proxy adds 38,508 generated sparse files totaling
2,559,627,282 logical bytes under the major ignored locations, including
`.home`, `.marshal`, `node_modules`, `.kilo`, `.opencode` and cache paths. It
copies no private content or filenames. LSP is off on both sides of this
comparison so placeholder names cannot alter language-server discovery.

The proxy leaves the measured turn latency essentially unchanged. Most added
files are excluded from scanning and capture. It also exposes a specific
discrepancy: Git ignores `scripts/__pycache__/d00/f00000.dat`, while Marshal's
scanner indexes all 183 files under that generated nested cache (20,648,680
bytes). Index counts rise from 2,870 to 3,053. The source treats a trailing slash
in a basename ignore pattern as a root anchor, so `__pycache__/` fails to cover
this nested directory. This deserves a focused ignore-semantics fix.

This control covers sparse placeholder traversal and the observed ignore rules.
It does not emulate actual cache contents, private state, dirty working files
or the full structure of the usual checkout.

## Next changes supported by this evidence

1. Cache sanitized output for settled command executions instead of repeatedly
   cleaning unchanged terminal output on the coordinator's timer. Keep
   redaction intact; do not simply truncate before redacting. Live-output and
   settled-output revisions need distinct invalidation rules. This directly
   targets the confirmed quiet CPU and allocation cost.
2. Reduce repeated snapshot work and per-object allocation while preserving
   byte-accurate rollback protection. Compression/decompression reuse and
   verified immutable-object reuse are concrete candidates. Audit whether the
   unconditional turn-start capture can be deferred without losing behavior.
   Shell risk classification alone is still insufficient to waive protection.
3. Make automatic language-server cost explicit and consider lazy startup or
   per-language choices. Do not infer that changing the developer's live LSP
   configuration is required from a cold private-cache measurement.
4. Correct nested basename-directory ignore semantics and compare them against
   Git's own answers.

The work-coordinator path is shared with the ongoing daemon work. This pass
does not edit it, restart the daemon or install a binary. The next implementation
should be coordinated against that work in the isolated performance branch.
Long-session transcript optimization and matched live-provider comparisons
remain separate investigations; these three-turn measurements establish local
contributors rather than the complete cross-client performance gap.

## Profiles

The separate profiles are preserved under
[`profiles-real-repo-2026-11-14/`](profiles-real-repo-2026-11-14/). For example:

```sh
go tool pprof -top -cum profiles-real-repo-2026-11-14/quiet.cpu.pprof
go tool pprof -top -cum -sample_index=alloc_space \
  -base=profiles-real-repo-2026-11-14/quiet.initial.heap.pprof \
  profiles-real-repo-2026-11-14/quiet.heap.pprof
```

## Verification

- All twelve unprofiled runs and both separate final profile runs passed their
  request, audit, command-exit, snapshot and resource-coverage assertions.
- The full default `CGO_ENABLED=1 go test ./... -count=1 -timeout 10m` run passed:
  123 packages passed, six had no tests, and no tests failed. Its usability
  journeys used a required temporary binary forced through `--embedded` and a
  short private `TMPDIR`.
- A separate real-client run and the environment-isolation/DCH regressions
  passed with the Go 1.27.0 race detector. That run is excluded from timing and
  profile results. The complete terminal harness suite also passed.
- `go vet -tags=latencyprobe` passed for the diagnostic command, usability,
  terminal harness and LSP packages. The ordinary builds exclude the tagged
  profiling and repository probes.
- Independent review verified the code, report calculations, dataset counts,
  source/profile hashes and attribution against the profiles. No actionable
  findings remain. The only ordinary-build code change is in the test terminal
  decoder, supporting the standard redraw sequence encountered by the fast
  control; production runtime code is unchanged.
