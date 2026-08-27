# Containerized Agent Runtime (S1) — Deferred Follow-Ups

The following items were identified during branch review and
deliberately deferred from the initial S1 implementation. Each is
documented here so it can be tracked and addressed in subsequent work.

Items marked **✅ Resolved** were addressed by the S1 completion plan
and are kept here for historical context.

## 1. ~~Container environment is never populated~~ ✅ Resolved

**Resolved by the S1 completion plan.** `ContainerConfig.Env` is now
populated: `NewFleet` passes `f.agentEnv` (a merge of
`InheritedAgentEnv()` and explicit `--agent-env KEY=VALUE` flags) into
the `ContainerConfig`, and `buildRunArgs` injects it via `-e` flags.
`RuntimeProfile` still has no per-project `Env` field — the container
receives only explicitly-supplied provider keys, not `HOME`/`PATH`/
general host env (those come from the image). An agent image that
expects ambient env vars beyond provider credentials won't find them.

## 2. No idle/read timeout on hung connections

`listenAndServeWithConfig` serves one connection at a time. A client
that opens the socket and then goes silent (never sends EOF, never
closes) blocks `Serve` indefinitely — the accept loop is stuck and no
new dialer can be served until the hung client hangs up or the whole
listener is cancelled.

**Action:** Wrap each accepted connection with an idle deadline (e.g.
`conn.SetReadDeadline` refreshed per frame, or a per-connection context
with timeout) so a stuck peer cannot hold the host hostage.

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

## 5. `SetTurnCanceller` overwritten per connection

`registerHandlers` (called per connection) invokes
`manager.SetTurnCanceller(...)` with a closure capturing that
connection's `TurnManager`. When connection 2 attaches, the canceller is
replaced. If a turn outlives the bounded `waitHandlers` shutdown
timeout, that orphaned turn is uncancellable through the manager.

**Action:** Document the trade-off with a comment, or retain the
previous canceller and chain it so orphaned turns remain cancellable.

## 6. `agentIDFromContainer` is unused in production

`agentIDFromContainer` is only referenced by tests. `ReattachAll`
discovers agents to reattach from persisted workspace records, not by
scanning running containers, so the function is dead code in
production.

**Action:** Either wire it into a container-scan-based reattach path,
or remove it if persisted-record-based reattach is the final design.