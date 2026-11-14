# Isolated agent latency and resource investigation

Date: 2026-11-14. Source: `fcebaa8b0122a1cf9ec65df2eae08298e5c4660e`.
Scope: measurement only; no production changes or daemon reconfiguration.

The reported symptom is broader than one model: Marshal feels slower and uses
more system resources than OpenCode or Codex across multiple providers. This
pass identifies Marshal-side costs independently of model inference. It does
not establish a cross-client ranking or explain every live stall.

## Isolation and method

Work ran in a separate managed worktree on
`codex/agent-latency-investigation`, frozen at the source revision above.
The running daemon, installed Marshal binary, real provider configuration,
credentials and existing session stores were not changed. Machine load was
not controlled; the daemon was left alone.

The opt-in [probe](../../internal/agent/latency_probe_test.go) uses the real
agent dispatch, native tools, policy authorizer, managed command service,
restricted backend, SQLite database and snapshot implementation. Providers,
homes, workspaces and stores are test-owned. Its build tag excludes it from
normal builds and CI; its tagged `TestMain` uses `testenv.Run`.

Fixtures contain 100, 1,000 or 5,000 distinct Go-like files of roughly 400 bytes
each. Contents differ to avoid an unrealistically cheap identical-blob case.
Three runs supply 15 measurements per tool/shell case and three measurements
per cold snapshot, turn and injected-wait case. Cases run sequentially, with
a warm snapshot store before tool measurement. Every tool sample must produce
one new audit and one handler invocation. Shell samples must exit zero and
return the fixture content; failed/skipped snapshots fail the probe.

The [dataset](measurements-2026-11-14.json) contains all 351 final probe rows,
15 projection benchmark results, the source revision, Go version and
probe SHA-256 hashes. Durations are milliseconds unless a field explicitly says
otherwise. Fields not measured for a row kind are absent. Independent medians
are not additive wall-clock phases.

## 1. Snapshot preflight dominates small state/command tools

Median end-to-end time, with the unchanged repository already snapshotted:

| Operation | 100 files | 1,000 files | 5,000 files |
| --- | ---: | ---: | ---: |
| Native `file.read` | 0.12 | 0.13 | 0.13 |
| Native `repo.search`, absent query | 3.68 | 34.12 | 171.62 |
| `todo.write` | 120.41 | 223.94 | 585.58 |
| `shell.run`: single-file `cat` | 127.52 | 226.61 | 588.01 |
| `shell.run`: single-file `grep` | 124.08 | 228.66 | 587.63 |

The 5,000-file breakdown explains the discrepancy with existing tool timing:

| Operation | Total | Snapshot `Track` | Handler | Legacy audit duration |
| --- | ---: | ---: | ---: | ---: |
| `todo.write` | 585.58 | 585.14 | 0.15 | 0.18 |
| Shell `cat` | 588.01 | 581.96 | 5.67 | 5.68 |
| Shell `grep` | 587.63 | 581.64 | 5.78 | 5.79 |

`todo.write` is classified as `RiskWorkspaceWrite`, so session-only todo
updates take the same workspace snapshot and write gate as filesystem writes.
All ordinary shell commands also take that preflight, including these reads.
Native `file.read` and `repo.search` take neither; their handlers ran on every
sample, rather than being served from the turn cache.

Cold captures were 230 ms, 552 ms and 2,013 ms for the three fixture sizes.
These include initial store/object creation and should not be conflated with
the warmed per-tool captures above. The snapshots remain necessary protection
for actual writes; these observations do not justify disabling them globally.

## 2. Existing metrics hide preparation and waiting

The timer in `executeToolCall` starts after snapshot capture and write-gate
acquisition. An injected 80 ms snapshot made a todo call take 81.19 ms while
its audit reported 0.14 ms. An injected 80 ms gate wait took 81.21 ms while its
audit reported 0.16 ms. The zero-delay control took 0.08 ms.

Turn metrics are initialized after synchronous titling and the turn-start
snapshot. Injecting 80 ms into either delayed the first runner-provider call
to roughly 81 ms, while `TurnMetrics.DurationMs` still reported zero for the
otherwise immediate scripted turn. The title provider is a separate scripted
provider; `provider_calls` in the dataset counts the runner's provider only.

These are controlled attribution experiments, not measured production wait
distributions. They establish that the current metrics cannot reliably
exonerate tool/turn preparation when the user observes a pause.

## 3. Login-shell startup is a separate repeatable cost

For one tiny file, median times were:

| Execution | Empty fixture home | Fixture profile containing `sleep 0.08` |
| --- | ---: | ---: |
| Direct `/bin/cat` | 1.58 | 1.52 |
| Nonlogin Bash | 3.11 | 3.05 |
| Restricted backend's login shell | 4.62 | 96.90 |

The default restricted backend invokes Bash with `-lc`, so profile cost is
paid again for each shell launch. The comparison subprocesses have explicit
environments, and the restricted backend uses its production environment
filter. No developer-owned profile was modified. This reproduces the mechanism;
it does not show that this user's actual profile is slow. Direct execution
here is an experimental control, not a production sandbox bypass proposal.

## 4. Transcript projection allocates substantially despite wire memoization

Existing ACP benchmarks use 200 finished turns, 2,000 tool calls and about
1,201 wire nodes (roughly 2.53 MB serialized). Three 20-iteration runs gave:

| Existing benchmark | Median ms/op | Allocated bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| Ten-node patch application | 0.95 | 374,038 | 974 |
| Current memoized projection | 3.44 | 13,299,896 | 23,778 |
| Memoized projection, unchanged transcript | 3.51 | 13,217,618 | 23,371 |
| Agent stack flush after one new call | 3.55 | 13,481,707 | 24,206 |
| Fresh projection comparison | 5.62 | 20,243,949 | 88,569 |

These are allocation churn figures, not resident memory/RSS. The fresh
projection row is a comparison path, not the current memoized implementation.
The existing ACP benchmarks disable the test-only fresh-projection consistency
check; the new agent benchmark leaves it at its production default, false.
The unchanged-transcript row uses the same large fixture and fixed clock with
no appended calls, with three 50-iteration runs. It confirms that reprojecting
unchanged state still pays the substantial rebuild/allocation cost.

A separate 100-iteration allocation profile identified `toolRows`,
`buildTurn` and `State.Transcript` among the largest allocation sources.
`WireMemo.ProjectState` still calls `SnapshotOf` and `Build` before it can reuse
wire nodes: caching the wire form does not avoid rebuilding the transcript
tree. Marshal's client advertises the Marshal capability and uses a 50 ms
stack interval; plain ACP clients use 200 ms. During turn forwarding, dirty
checks skip unchanged projections. The idle loop can deliberately reproject
without that check while the client's busy flag or a subagent remains active.
These measurements must not be presented as observed idle CPU usage or as a
measured allocation rate for every real session. No daemon/client
implementation was changed to investigate this shared path.

## 5. Normal history preparation was cheap in this controlled probe

Completed synthetic histories of 100 and 1,000 messages took 0.12 and
0.39 ms end to end with immediate provider replies and made one runner-provider
call. The normal history budget kept request content at 13,776 bytes in both
cases; tool schemas and protocol framing are not included in that byte count,
and this turn probe registers no tools. These cases use the scripted model's
fallback route/history budget, not a live provider preset. They do not measure
the larger replay budget a production route may select.

A deliberately unfinished-history stress case behaved differently: 1,000
messages without final assistant boundaries took 160.18 ms, sent an initial
508,568 content bytes and made a second provider call after compaction.
A CPU profile of that fixture shape was dominated by rune counting through
`estimateTokens`, `wireEstimate` and `trimReplayedReasoning`. This is a
pathological control, not evidence that ordinary long conversations have the
same overhead or that history processing explains the broad reported slowdown.

## Priorities and the daemon boundary

1. Add accurate end-to-end tool/turn timings before treating existing audit
   durations as performance evidence. Keep phase waits separate from work and
   preserve missing versus measured-zero values.
2. Give `todo.write` a session-state-only workspace effect so it can avoid
   snapshot/gate work while retaining its existing policy and persistence.
   This is the narrowest confirmed optimization candidate.
3. Prefer native read/search tools where appropriate. Any shell read fast path
   needs a backend-bound proof of the invocation actually executed; risk
   classification alone is insufficient to waive snapshot protection.
4. Investigate incremental transcript construction upstream of wire memoization
   for the resource symptom. This touches the session/viewmodel/ACP boundary
   shared with the daemon work, so coordinate implementation after its active
   changes settle. Pure benchmark/profiling work is already independent.
5. Then run a matched live-provider comparison against OpenCode/Codex: same
   repository, goal, model, reasoning setting, permissions and warm/cold state;
   record every model request, request/tool-schema bytes, first event, tool
   completion, process CPU, peak RSS and child-process costs. Separate local
   inference-server resources from client resources, and avoid overlapping
   intensive runs. This pass made no live model requests or direct cross-client
   workload comparison, so it establishes contributors, not the complete gap.

## Reproduction and verification

```sh
CGO_ENABLED=1 go test -tags=latencyprobe ./internal/agent \
  -run '^TestLatencyProbe$' -count=3 -v -timeout 10m
CGO_ENABLED=1 go test ./internal/acp -run '^$' \
  -bench 'Benchmark(StackPatchApply|StackFlushLargeSession|ProjectStateLargeSession|WireMemoLargeSession)$' \
  -benchmem -benchtime=20x -count=3 -timeout 3m
CGO_ENABLED=1 go test -tags=latencyprobe ./internal/agent -run '^$' \
  -bench '^BenchmarkLatencyProbeUnchangedProjection$' \
  -benchmem -benchtime=50x -count=3 -timeout 2m
```

The four-package baseline passed for `internal/agent`, `internal/tools/native`,
`internal/sandbox` and `internal/contextpack`. The ordinary agent suite also
passed with the probe tag active and the probe itself skipped; tagged vet passed.
All three final probe runs and all projection benchmark runs passed.

A separate temporary Marshal binary, forced through `--embedded` by a wrapper,
passed the real-binary approval deny/allow journey against its test-owned
provider (11.25 seconds). This verifies the isolated binary/tool path; it is
not a startup-latency measurement. No installed executable was replaced and
no production runtime, daemon, ACP or TUI file was edited.
