# Containerized Agent Runtime (S1) — Deferred Follow-Ups

The following items were identified during branch review and
deliberately deferred from the initial S1 implementation. Each is
documented here so it can be tracked and addressed in subsequent work.

Items marked **✅ Resolved** were addressed either by the S1 completion
plan or the follow-ups pass, and are kept here for historical context.

## 1. ~~Container environment is never populated~~ ✅ Resolved

**Resolved by the S1 completion plan.** `ContainerConfig.Env` is now
populated: `NewFleet` passes `f.agentEnv` (a merge of
`InheritedAgentEnv()` and explicit `--agent-env KEY=VALUE` flags) into
the `ContainerConfig`, and `buildRunArgs` injects it via `-e` flags.
`RuntimeProfile` still has no per-project `Env` field — the container
receives only explicitly-supplied provider keys, not `HOME`/`PATH`/
general host env (those come from the image). An agent image that
expects ambient env vars beyond provider credentials won't find them.

## 2. ~~No idle/read timeout on hung connections~~ ✅ Resolved

**Resolved by the follow-ups pass.** The listen path wraps every
accepted connection in an `idleDeadlineConn` (`internal/acp/idle.go`):
the read deadline is refreshed by traffic in *either* direction, and a
connection silent for `acpIdleTimeout` (10 minutes) is closed so the
single-connection accept loop is never held hostage. Mid-turn
connections stay alive because outbound session/update notifications
extend the deadline. `runConfig.idleTimeout` lets tests shrink the
window (`TestListenAndServeCutsIdleConnections`).

Because a healthy idle agent would now be churned through a reattach
cycle every 10 minutes, the webbridge complements this with a keepalive
ping (`Child.startKeepalive` in `web/bridge/child.go`, every 5 minutes)
so quiet-but-healthy connections are never cut, and
`containerTransport.Wait` now returns on agent hangup (`lost` channel,
`errLostConn`) instead of wedging on `docker wait` for a container whose
control connection closed — supervise reattaches to the still-running
container.

## 3. ~~Reattach-preference and cleanup tests need an injectable seam~~ ✅ Resolved

**Resolved by the S1 completion plan.** `containerTransport` now has an
injectable `commandRunner` seam (`c.run`), and the reattach-preference
path (`Open` → `listAgentContainers` → `Reattach`) and the dial-failure
cleanup path (`start` → `Kill` on dial error) are both covered by unit
tests using a fake runner.

## 4. ~~`Resume` does not restore the ACP session~~ ✅ Resolved

**Resolved by the S1 completion plan.** `Agent.SessionID` is now
persisted (`workspace.go`), and `Resume` calls `restoreSession` which
calls `reg.Load` with the persisted session id. `ReattachAll` and
`RuntimeForSession` also use `restoreSession`.

## 5. ~~`SetTurnCanceller` overwritten per connection~~ ✅ Resolved

**Resolved by the follow-ups pass.** The host-level canceller is now
registered once in `newAgentHost` (`internal/acp/host.go`): it fans a
cancel request out to an append-only chain of per-connection cancellers
(`addTurnCanceller`), so a turn that outlived its connection's bounded
`waitHandlers` timeout remains cancellable through the manager
(`TestChainedCancellerReachesOrphanedTurns`). Entries are deliberately
never removed: releasing a connection's entry would strand its
orphaned turn — the exact bug the chain fixes — and cancellation of a
TurnManager with no active turn for the session is a nil-returning map
lookup.

## 6. ~~`agentIDFromContainer` is unused in production~~ ✅ Resolved

**Removed by the follow-ups pass.** Reattach is persisted-record-based
(`ReattachAll` reads workspace records, not a container scan), so the
inverse of `containerNameFor` had no production caller. The function
and its round-trip test were deleted from `web/bridge/container.go`;
`containerNameFor` itself remains in production use.