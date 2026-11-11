# Task 5 implementation report

## Result

Added recovery admission and observation-based reconciliation for exact tagged spawns, clean published Git heads, exact draft PRs, park/resume state, and ready/draft PR changes. Interrupted prompts are never replayed or inferred successful from an idle runtime. Unknown outcomes retain a recovery decision. Parked factory workers are excluded from generic reattachment; known running container workers have a factory-specific reattach path that refuses to launch a replacement container or native child. Stop remains stopping when turn or detached work is unknown.

Added factory agent and branch ownership queries and guards at shared Fleet merge, discard, exit, and new-run entry points. The review and CI bots skip owned branches; manual review and schedules targeting them return conflicts. Factory conflicts map to HTTP 409. Existing Git publication already compares the remote head with the expected SHA and never force-pushes. A stopped/closed item's agent record can be discarded while its factory journal history remains.

## Red and green evidence

The focused red command initially failed on missing recovery and ownership functions:

```
PATH=/workspace/toolchains/go/bin:$PATH GOCACHE=/tmp/factory-go-cache go test ./web/bridge -run '^TestFactory(Recovery|Ownership)' -count=1
# marshal/web/bridge [marshal/web/bridge.test]
web/bridge/factory_ownership_test.go:9:6: undefined: factoryItemOwnsAgent
web/bridge/factory_recovery_test.go:13:16: c.recoveryOutcome undefined
FAIL marshal/web/bridge [build failed]
```

Final required focused race command passed:

```
PATH=/workspace/toolchains/go/bin:$PATH GOCACHE=/tmp/factory-go-cache go test -race ./web/bridge -run 'TestFactory(Recovery|Ownership)|TestReviewBot|TestCIFixer|TestSchedule|TestReattach' -count=1
ok marshal/web/bridge 6.558s
```

The first race invocation in the sandbox could not bind `httptest` sockets; it was rerun with local socket permission. One intermediate assertion found that `factoryDecision` changed an unresolved prompt to `needs_input`; recovery now restores `recovering`, and the final race command passed.

## Self-review and decisions

- Reconciliation requires observed identity; no model effect is completed from idle status alone. A located spawn remains recovering until its runtime and active work are observed. An explicit operator attestation must name the previous worker and is refused for an unidentified spawn.
- Retry refuses any dispatched/uncertain effect and all recovering items. This prevents a new worker from starting against a possibly active old worker.
- Task 6 must construct and attach the controller, call `Recover` before bots/schedules, and expose its routes; `cmd/webbridge` does not yet construct a factory controller.
- The current ACP contract has no durable completion receipt for a prompt interrupted across restart. The conservative recovery decision reflects that limit. A named-worker operator attestation records the prior worker and permits an explicit retry with additional allowance. Retry persists a fresh `resume` intent, starts a new ACP session in the retained checkout using approved routing and policy, records the new session ID, and preserves the prior effect and attestation. An unidentifiable spawn or an unattested unknown prompt cannot take that path. If a fresh session response is lost, recovery checks its persisted identity rather than replaying the session creation blindly.
- No live runtime, model, credential, or forge service was used in tests.

## Attestation continuation follow-up

Added `TestFactoryRecoveryAttestationNamesWorker` coverage for refusal of an unnamed attestation, journaled retry intent, fresh session identity, retained checkout, and prior history. `TestFactoryRecoveryLostOutcomes` rejects duplicate tagged workers. The focused non-race attestation test and final required race command passed:

```
PATH=/workspace/toolchains/go/bin:$PATH GOCACHE=/tmp/factory-go-cache go test ./web/bridge -run '^TestFactoryRecoveryAttestationNamesWorker$' -count=1
ok marshal/web/bridge 0.179s

PATH=/workspace/toolchains/go/bin:$PATH GOCACHE=/tmp/factory-go-cache go test -race ./web/bridge -run 'TestFactory(Recovery|Ownership)|TestReviewBot|TestCIFixer|TestSchedule|TestReattach' -count=1
ok marshal/web/bridge 4.785s
```
