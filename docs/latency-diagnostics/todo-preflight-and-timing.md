# Todo preflight and complete execution timing

This follow-up implements the first fixes supported by the November 14
measurements. Only the native `todo.write` tool declares session-only effects.
It retains its workspace-write risk, policy, approval, hooks, audit and cache
invalidation behavior, but does not snapshot or acquire the workspace gate.
Unknown effects remain conservative. Todo reconciliation and persistence are
serialized by the session, independently of workspace writes.

Tool diagnostics record total elapsed time from entry through cleanup, including
holds, validation, approval, hooks, snapshots, gate waits, handler execution and
audit persistence. Separate optional phase durations distinguish an unexecuted
handler from a handler measured at zero. Repeated policy/hook phases accumulate.
One structured local log record is emitted per dispatched invocation, including
the special native `question.ask` path and its human wait. Calls settled by
steering before dispatch do not enter these timers. An injectable clock
supports deterministic measurements. Records contain identifiers and durations,
never arguments, output or prompts. Existing audit/process durations keep their
meaning; no database migration or ACP contract change is needed.

Turn metrics begin before startup work, including title generation and the
turn-start snapshot, and reset even when startup is canceled. They are emitted
after turn cleanup. Production clocks retain Go's monotonic elapsed-time support.

Implementation order: regression tests and session serialization; todo effect
metadata and preflight exemption; complete tool/turn timing; focused, race and
full-suite validation; repeated isolated latency probes; independent review.
Transcript projection and shell execution changes remain deferred. This work
does not install a binary or restart the running daemon.

## Measurement results

The unchanged isolated probe was repeated three times after the review fixes,
with five samples per tool case per run. The baseline and follow-up use private
SQLite databases, real snapshot stores, test-owned homes and scripted providers.
No intensive tests ran alongside either measurement pass; machine load remains
uncontrolled. These are sequential fixture measurements, not a matched live
comparison against other clients.

| Repository files | Before: todo median | After: todo median | After: snapshots per call |
| --- | ---: | ---: | ---: |
| 100 | 120.407 ms | 0.109 ms | 0 |
| 1,000 | 223.939 ms | 0.113 ms | 0 |
| 5,000 | 585.577 ms | 0.109 ms | 0 |

Each row summarizes 15 successful calls; each call invoked the native handler
once and recorded an audit event with persistence enabled. The whole-loop
speedup is not measured by this table. Shell snapshots and transcript projection
have not been optimized.
The probe discards the session log sink in both versions; logging-enabled
correctness is verified separately, so these timings do not quantify file-log
I/O overhead. Raw observations and the measured production-file hashes are in
[the follow-up dataset](measurements-todo-fix-2026-11-14.json); the original
[baseline dataset](measurements-2026-11-14.json) remains unchanged.

The injected 80 ms title and snapshot controls now each report a median 82 ms
in turn metrics, matching their roughly 82 ms outer elapsed times. The baseline
reported zero milliseconds for these controls. Deterministic regressions also
verify that total tool time includes gate release, that holds and approvals are
measured, and that absent handler duration differs from measured zero. Native
questions cover rejected payloads, human answers and canceled waits. Review
also moved todo timestamp capture inside the serialized transaction, preventing
concurrent updates from committing timestamps in the wrong order.

## Reading the timings

The existing session logger writes `tool timing` records to the project's
`.marshal/marshal.log` at info level. Records contain `tool`, `call_id`,
`total_ns`, `cache_hit`, `returned_error` and `handler_error`. Optional durations
are `hold_ns`, `policy_approval_ns`, `hooks_ns`, `snapshot_ns`,
`workspace_gate_ns`, `handler_ns` and `question_wait_ns`. All are nanoseconds;
missing means the phase did not run. Phases are accumulated subsets of total,
not an exhaustive breakdown. Total includes audit persistence, result processing
and cleanup, and excludes writing the timing record itself. Background commands
are measured until dispatch returns; their eventual process lifetime retains
its separate existing measurement.

`returned_error` describes the dispatcher's Go error, not tool success: denied
or invalid calls can return a model-facing error message with a nil Go error.
Use the existing audit/result records to interpret outcomes. Native questions
have no registry handler duration; their presentation and human wait appear in
`question_wait_ns`. No arguments, outputs, questions or answers are added to
the timing log.

Reproduce the fixture measurements with:

```sh
CGO_ENABLED=1 go test -tags=latencyprobe ./internal/agent \
  -run '^TestLatencyProbe$' -count=3 -v -timeout 10m
```

## Final verification

- `CGO_ENABLED=1 go test ./... -count=1 -timeout 10m` passed on Go 1.26.1:
  123 packages passed and six had no tests; no test failures. This run used a
  short private `TMPDIR` and a temporary rebuilt binary forced through
  `--embedded`, with `USABILITY_REQUIRE_BINARY=1`, so real-binary usability
  journeys ran without attaching to the running daemon.
- Focused race checks passed on Go 1.27.0 across `internal/agent`,
  `internal/tools/native`, `internal/app/session` and `internal/tools/registry`,
  selecting todo, tool/turn timing, native question timing, serialized update
  and workspace effect tests.
- `CGO_ENABLED=1 go vet ./...` passed, as did all three final latency-probe runs.
- Independent review found the timestamp ordering and native question timing
  gaps described above. Both have regression tests and were resolved; the
  follow-up review had no remaining actionable findings.
