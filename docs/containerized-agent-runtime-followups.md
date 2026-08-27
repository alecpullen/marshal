# Containerized Agent Runtime (S1) — Deferred Follow-Ups

The following items were identified during branch review and
deliberately deferred from the initial S1 implementation. Each is
documented here so it can be tracked and addressed in subsequent work.

## 1. Container environment is never populated

`fleet.go` constructs `ContainerConfig` without an `Env` field, and
`RuntimeProfile` has no `Env` field. Combined with the security-hardened
`cmd.Env = []string{}` (which correctly prevents host env inheritance),
a containerized agent is born with zero environment — no API keys, no
`HOME`, no `PATH`, no credentials. The agent inside the container cannot
authenticate to any provider.

**Action:** Wire explicit env injection (API keys, `HOME`, `PATH`) into
`ContainerConfig` and `RuntimeProfile` so the containerized agent is
functional end-to-end.

## 2. No idle/read timeout on hung connections

`listenAndServeWithConfig` serves one connection at a time. A client
that opens the socket and then goes silent (never sends EOF, never
closes) blocks `Serve` indefinitely — the accept loop is stuck and no
new dialer can be served until the hung client hangs up or the whole
listener is cancelled.

**Action:** Wrap each accepted connection with an idle deadline (e.g.
`conn.SetReadDeadline` refreshed per frame, or a per-connection context
with timeout) so a stuck peer cannot hold the host hostage.

## 3. Reattach-preference and cleanup tests need an injectable seam

`containerTransport.Open()` checks `listAgentContainers` first and
reattaches if a matching container is running. The `Kill()`-on-dial-
failure cleanup path is also untested. Both paths shell out to `docker`
and cannot be exercised deterministically without faking the command
runner.

**Action:** Introduce an injectable command-runner seam in
`containerTransport` and add unit tests for the reattach-preference
path and the dial-failure cleanup path.

## 4. `Resume` does not restore the ACP session

`Resume` calls `startRuntime` (which starts a fresh container) but
never calls `reg.Load` to reattach to the prior session. The `Agent`
struct has no persisted `SessionID` field, so after a Pause the ACP
session id is lost from bridge memory. A resumed agent starts with an
empty container: workspace files survive (bind mount) but conversation
state does not.

**Action:** Persist the ACP session id on the `Agent` record and have
`Resume` reattach to it via `reg.Load`.

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