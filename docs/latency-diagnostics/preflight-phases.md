# Preflight phase timing and async candidates

Status: instrumentation landed; the async changes below are analysis only — none
are implemented yet.

## The gap this closes

`RunTask` publishes the user's prompt to session state before it does any
model-backed startup work, so the prompt appears in the TUI immediately. The
work between that publication and the first provider request produces no
transcript row. Before this change a slow turn therefore looked identical to a
slow model, and the investigation of 2026-11-14 recorded `TurnMetrics.DurationMs`
reporting zero for a scripted turn that had 80 ms injected into titling or the
turn-start capture.

## What is measured now

Each phase is a span in `internal/agent/phases.go`, recorded in execution order.
`PromptReadyMs` is the wall time from turn start to the moment the first request
became buildable, so a gap between it and the sum of the phases is time no span
claimed — an unattributed pause stays visible instead of being folded into
whichever phase ran next.

| Phase | Covers | Runs every turn |
|---|---|---|
| `register` | steering-turn registration, prompt publish, stop-group registration | yes |
| `title` | `TitleManager.OnUserTurn` — a synchronous model call on turn 1 | when a title manager is wired and the session is top-level |
| `snapshot` | turn-start workspace capture (`SnapshotPhaseTurnStart`) | when a snapshotter is configured |
| `classify` | local `Classify`, plus the optional classifier model call on a keyword miss | yes (the model call only on a miss) |
| `route` | route, provider, credential and base-URL resolution | yes |
| `memories` | `mergeMemories` | yes |
| `semantic` | `mergeSemantic` — retrieval over the embedding index | yes |
| `todos` | `mergeTodos` | yes |
| `pins` | `@file` extraction and pinning | yes |
| `history` | `LoadAllTurnToolAudit` + `buildHistoryMessages` | general role |

Reported three ways: `TurnMetrics.PromptReadyMs` / `TurnMetrics.Phases` for
observers and tests, one `turn preflight` log line with the compact breakdown
(`title=120ms snapshot=482ms route=3ms …`), and the optional `latency` section
of the session postmortem (below). The line is `Info` at or above 750 ms and
`Debug` below, so it is quiet on healthy turns and shows itself on the ones
worth investigating.

Phases are per-turn wall clock. Nothing here assumes phases are disjoint, so the
log deliberately does not present their sum as wall time.

## Postmortem section

The runner also records each measurement in a bounded in-memory collector on
`session.State` (`internal/app/session/preflight.go`). A session postmortem
reads it back at the end of the session and adds a `latency` object to the
report (`internal/postmortem/latency.go`). The section is additive and
optional: the report schema version is unchanged, and a session that measured
no preflight omits the field entirely, because an absent section means
"unavailable", never "zero latency".

```json
"latency": {
  "source": "live",
  "turns_measured": 12,
  "session_turns": 12,
  "dropped": 0,
  "phases": [
    {"phase": "history", "count": 12, "total_ms": 410, "max_ms": 96, "max_turn": 3},
    {"phase": "snapshot", "count": 12, "total_ms": 5210, "max_ms": 980, "max_turn": 1}
  ],
  "examples": [
    {"turn": 1, "prompt_ready_ms": 1180,
     "unattributed_ms": 200,
     "phases": [{"phase": "snapshot", "duration_ms": 980}]}
  ]
}
```

- `source` is `live` for this process's in-memory collector. A resumed process
  starts its collector empty, so its earlier turns are absent rather than fast;
  `session_turns` greater than `turns_measured` makes that partial coverage
  visible.
- `phases` aggregates are complete for the process, keyed by the fixed phase
  vocabulary and ordered by name. `max_turn` pairs the maximum with the turn it
  came from, even after that sample's detail was evicted.
- `examples` holds at most 32 of the slowest retained turns, slowest first, and
  each reports `unattributed_ms` — the part of `prompt_ready_ms` no phase span
  covered — rather than folding it into a phase.
- The collector keeps at most 64 detailed samples; evicted samples only reduce
  detail (`dropped` counts them), never the aggregates. The serialized section
  is capped at 64 KiB.
- Timings are in-memory only. They are not persisted to `turn_metrics`, so a
  postmortem written after a process restart reports no latency for turns it
  did not see.

## Async candidates, in payoff order

The constraint that decides each one is *what the phase feeds*: anything that
contributes to the prompt cannot outlive prompt assembly, only overlap with
other independent work.

### 1. Title generation — best payoff, blocked by a design decision

`TitleManager.OnUserTurn` writes only the session title, which is not part of the
prompt. On a cold session it is a full model round-trip on the critical path and
the only phase here whose result is never read by the turn.

Two obstacles, both real:

- The call is synchronous **by design** (`runner.go`, "single-model safe"). Two
  concurrent model calls against a single-backend local model is exactly the
  thrash that comment is avoiding, so moving it off-path is a product decision
  about concurrent requests to one model, not a mechanical change.
- It runs inside `withSetupOperation`, whose lifecycle is a steering operation
  joined when the scope returns. A background title needs its own lifecycle and
  a guarantee it cannot outlive the turn or land after the session closes.

Lowest-risk version: fire it after the prompt is built, still within the turn,
and abandon the result if the turn ends first. Worth doing, but it needs an
explicit decision on concurrent model calls.

### 2. Turn-start snapshot — biggest payoff, medium risk

`snapshot` scales with repository size and dominated the measured pre-write
costs. It must complete *before the first workspace write of the turn*, not
before the prompt. It could therefore overlap with `route` … `history` and be
joined at the first write tool.

The risk is ordering: a write that lands before the join captures the wrong
baseline. Safe only if the join point is structural (in the pre-write path,
before the gate) rather than a convention. Do not do this one opportunistically.

### 3. `semantic` ∥ `history` — modest payoff, low risk

These two are independent: retrieval reads the embedding index, history reads
SQLite and assembles messages. Both must finish before the request, but neither
needs the other. Overlapping them shaves the smaller of the two on a cold
embedding index.

Caveat: both touch `r.State` (context pack, generation) and share `ctx`, so the
overlap needs care about which one owns which mutation. Modest win, and the
first thing to try if the log says these two together are the problem.

### 4. `classify` — negligible

The common path is the local keyword `Classify` (microseconds). The model call
happens only on a `Question` keyword miss, and a concurrent version needs a join
before the loop reads `task.Class`. Not worth the complexity unless the log
shows the miss path firing often.

### Not candidates

- `route` gates the history budget, request options and the status bar, and the
  loop cannot start without it.
- `history` and `pins` build the prompt itself.
- `memories` and `todos` are in-memory merges.

## Recommendation

Read the log for a few real slow turns before changing anything. The analysis
above ranks candidates by expected payoff, but §1 and §2 are the expensive ones
and both carry a constraint that a local model or a write-ordering bug would
punish. If the breakdown shows `snapshot` dominating, the answer is more likely
incremental capture (see the snapshot-inventory results) than concurrency; if it
shows `title`, the single-model decision is the thing to settle first.
