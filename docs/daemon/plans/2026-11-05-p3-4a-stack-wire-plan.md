# P3 plan 4a: The stack over the wire — implementation plan

Date: 2026-11-05. Status: ready for review.
Base: `main` at `8e2245d5` (P3 plan 1 merged, while P2 plan 7b executes). Every anchor below was checked there.
- **This plan runs in parallel with P2 plan 7b** (decided with the user, 2026-11-05). It touches `internal/acp`, `internal/acpclient/rpc` and `internal/acpwire/stack.go`. Plan 7b touches none of them. The two share only `AGENTS.md`, in different sections.
- The JSON shapes it pins are today's, unchanged: the stack patch's fields are moved, not altered.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R3 (the mirror), R4 (the transcript), R10 (budgets), Migration order item 4a, and tests V4 and V6. Its "Decided with the user" list records the split of plan 4.
Previous plan: [P3 plan 1, the client core](2026-11-05-p3-1-client-core-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a client mirror of a session's transcript, fed by the stack patches the agent already sends, proved against the real stack projector over the pipe and measured against R10's budgets on a 2,000-call session.
- `acpwire` gains the stack patch, the session update envelope and the stack method names.
- The agent flushes stack patches every 50ms for a marshal client, and a client can unsubscribe from a subagent's stack.
- `rpc.Stacks` mirrors every stack a connection reads.

**Non-goals:**
- **No `rpc.Session` and no interface method.** Plan 4b wires the mirror into the session type's `Stack`, `StackNode` and `Callers`, and decides when the TUI releases a subagent.
- **The `busy` question stays open.** The mirror serves the agent's tree, whose busy flag follows the agent's rule. Plan 4b checks the T6 goldens against that rule (R4).
- **No read-your-writes ordering.** Plan 3 adds it to the server for every update kind, stack patches included.
- **No change for other clients.** A client without the marshal capability keeps the 200ms and 500ms flushes, and nothing else it sees changes (V6). The new method is one no existing client calls.
- **No TUI, P2 or bridge change.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-4a-suite`. Create the directory first: `mkdir -p /private/tmp/p3-4a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 7b uses `/private/tmp/p2-7b-suite`. Never share a directory between the two runs.
- **The base.** P3 plan 1's last task ran the full suite before its merge, and `8e2245d5` is that merge. This plan was written without rerunning it, to keep load off plan 7b's run. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **Two suites may run at once.** Load makes the flakes listed under Task 5 likelier. Rerun a failure alone before treating it as new.
- **The stack projector today** (`internal/acp/stack.go`):
  - Each connection has its own `TurnManager` (`newTurnManagerFor`, `host.go:175`, called once per connection by `registerHandlers`, `:295`), and with it its own projectors, keyed by session and subagent (`stackKey`, `:61`).
  - A projector becomes active on the first `session/stack` read for its key (`stackSnapshot`, `:275`). Its revision starts at 1 and its sent nodes are the snapshot's.
  - A flush diffs the projected tree against the sent nodes and sends one `stack_patch`: the upserted nodes, the removed IDs, the new `roots`, and `rev` and `baseRev` (`diff`, `:69`; `notifyStackPatch`, `:138`). An unchanged tree sends nothing.
  - Flushes run every 200ms during a turn (`turn.go:927`), and every 500ms while idle, on the session's idle loop (`ensureStackIdle`, `:328`). Every flush also projects each active subagent projector, unless it is final (`flushChildStacks`, `:191`).
  - A `session/stack` read of an active projector first sends the patch since the last flush, then answers with the snapshot at the new revision.
- **The patch's envelope** is `session/update` with `{"sessionId": …, "update": {"kind": "stack_patch", …}}` (`SessionUpdateParams`, `protocol.go:141`).
- **A snapshot's node order is the projection's**: a depth-first walk from each root through `children`, then the band node, which no root reaches (`viewmodel/wire.go:59`, `viewmodel/project.go:37`). A patch carries no order, so a mirror must rebuild it from `roots` and `children`.
- **The pipe applies frames in arrival order.** P3 plan 1's connection hands every notification to `Handlers.Notify` on its read goroutine before it delivers a later response (`internal/acpclient/rpc/conn.go`). So a call made after a flush returns only once the flush's patches are applied.
- **`startTestAgent`** (`internal/acp/client_core_test.go:25`) serves a server on a pipe with handlers a test registers.

**Decisions this plan makes beyond the spec** (all are recorded in the spec):

1. **Plan 4 splits** (decided with the user, 2026-11-05). 4a needs nothing layering moves: `session/stack` and `stack_patch` exist, and the per-connection `TurnManager` serves them. So 4a runs alongside P2. Plan 4b, after plan 3, does the rest: the session type's stack reads, `StackNode` and `Callers`, the `busy` decision against the goldens, and when the TUI releases a subagent.
2. **The patch type moves to `acpwire`, unchanged.** `acp`'s unexported `stackPatch` becomes `acpwire.StackPatch` with the same fields and tags, and `acp.StackParams` becomes an alias of `acpwire.StackParams`. `notifyStackPatch` keeps building its map envelope, so the bytes on the wire do not change.
3. **A marshal client gets a 50ms flush, in both loops.**
   - A `TurnManager` built with `TurnManagerConfig.Marshal` reporting true flushes every 50ms during a turn and every 50ms while idle. Anything faster coalesces into the next tick, as today.
   - Each loop reads the interval when it starts. `initialize` precedes every turn and every stack read, so the client's capability is known by then.
   - Every other client keeps 200ms and 500ms.
4. **A client can unsubscribe from a subagent's stack.**
   - `session/stack_unsubscribe` with a session and a subagent ID forgets that projector, so no flush projects it again.
   - A later `session/stack` read subscribes again, from a fresh snapshot.
   - The main transcript cannot be unsubscribed.
   - An unknown session or subagent is not an error: the result is `{}` either way.
5. **The mirror applies patches by revision, and recovers from a gap by refetching.**
   - A patch whose base is the mirror's revision applies: each upsert replaces or adds its node, each removal deletes one, and the roots and revision become the patch's.
   - A patch at or below the mirror's revision is a duplicate and is dropped.
   - Any other patch is a gap. The mirror marks itself fetching, and starts one `session/stack` fetch on its own goroutine, because `Notify` runs on the read goroutine and must not block on the connection.
   - While a fetch is in flight, patches are queued. The fetched snapshot is installed, then every queued patch newer than it is applied in order. One that does not chain starts another fetch.
   - This refines R4's "the patch is dropped". The gap patch's content is in the fetched snapshot, because the agent answers a read with a revision at least as new as every patch it sent before it. The queue keeps the patches that arrive after the response.
6. **The first read fetches synchronously.** The mirror's entry is created in the fetching state before the request is sent, so patches arriving meanwhile are queued and none is lost.
7. **The mirror rebuilds the agent's node order.** A read returns the nodes in the projection's order: depth-first from each root through `children`, then every node no root reaches, in the order the mirror first received them. The order is rebuilt only after a change. A read shares node values with the mirror, which replaces nodes on upsert and never changes one in place.
8. **`Changed` is per session.** Each session's channel has a buffer of one, and it fires after any install or applied patch for that session's main stack or any of its subagents' (R3).
9. **The budgets are measured on a 2,000-call session.**
   - R10 sets two: applying a ten-node patch, decoding included, under 2ms; and a full `session/stack` round trip to an applied mirror, over the pipe, under 50ms.
   - The agent's flush of the same session is measured too, with no budget. At a 50ms cadence its cost decides whether R4's interval is affordable, and the plan records it for plan 4b.
   - A missed budget stops the plan (R10).

**Execution:**
- Branch first: `git switch -c p3-4a-stack-wire` from `main`.
- If `git status` shows `docs/daemon/p3-acp-transport.md` or this plan uncommitted, commit them first as explicit paths, with the message `p3-4a-stack-wire: record the plan 4 split and plan 4a`. The main checkout holds other untracked files (`scripts/lmstudio-bench/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p3-4a-stack-wire: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/stack.go`)

Added after `StackSnapshot`:

```go
// The stack's methods and its update kind (P3 R4).
const (
	MethodStack            = "session/stack"
	MethodStackUnsubscribe = "session/stack_unsubscribe"
	MethodSessionUpdate    = "session/update"
	UpdateStackPatch       = "stack_patch"
)

// StackParams is session/stack's and session/stack_unsubscribe's params.
// SubagentID 0 names the main transcript.
type StackParams struct {
	SessionID  string `json:"sessionId"`
	SubagentID int64  `json:"subagentId,omitempty"`
}

// StackPatch is a stack_patch update: what changed in one stack since
// BaseRev. A client whose revision is BaseRev applies it and moves to Rev.
type StackPatch struct {
	Kind       string     `json:"kind"`
	SubagentID int64      `json:"subagentId,omitempty"`
	Rev        uint64     `json:"rev"`
	BaseRev    uint64     `json:"baseRev"`
	Roots      []string   `json:"roots"`
	Upsert     []WireNode `json:"upsert,omitempty"`
	Remove     []string   `json:"remove,omitempty"`
}

// SessionUpdate is a session/update notification's params, with the update
// left raw for its kind's decoder.
type SessionUpdate struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}
```

### Agent (`internal/acp`)

- `StackParams` becomes `type StackParams = acpwire.StackParams`.
- `stackPatch` is deleted. Every use takes `acpwire.StackPatch`, with `Kind` set to `acpwire.UpdateStackPatch`.
- `TurnManagerConfig` gains `Marshal func() bool`: whether the connection's client declared the marshal capability. Nil reports false.
- `newTurnManagerFor` gains a last parameter `marshal func() bool`, passed into the config. `registerHandlers` passes `srv.marshalClient`.
- `(*TurnManager).stackIntervals() (turn, idle time.Duration)`: 50ms and 50ms for a marshal client, otherwise `stackFlushInterval` and `stackIdleInterval`.
- `marshalStackInterval = 50 * time.Millisecond`.
- `(*TurnManager).StackUnsubscribe(ctx context.Context, params json.RawMessage) (any, error)`, registered as `session/stack_unsubscribe` beside `session/stack` (`host.go:389`).

### Client (`internal/acpclient/rpc/stacks.go`, new)

```go
// Stacks mirrors the transcripts one connection has read (P3 R3, R4).
type Stacks struct{ /* unexported */ }

// NewStacks returns an empty mirror. Bind it before the first read.
func NewStacks() *Stacks

// Bind sets the connection the mirror fetches through.
func (s *Stacks) Bind(c *Conn)

// Notify applies session/update stack_patch updates and ignores every other
// notification. It is a Handlers.Notify, or part of one: it runs on the read
// goroutine and never blocks on the connection.
func (s *Stacks) Notify(method string, params json.RawMessage)

// Stack returns the mirrored transcript of a session (subagentID 0) or of
// one of its subagents. The first read of a stack fetches it with
// session/stack, which subscribes the connection; later reads come from the
// mirror without I/O. The nodes are in the agent's order, and shared with
// the mirror: a caller must not change them.
func (s *Stacks) Stack(ctx context.Context, sessionID string, subagentID int64) (acpwire.StackSnapshot, error)

// Release forgets a subagent's mirrored stack and unsubscribes it with
// session/stack_unsubscribe.
func (s *Stacks) Release(ctx context.Context, sessionID string, subagentID int64) error

// Changed signals, with a buffer of one, after any stack of sessionID
// changes in the mirror.
func (s *Stacks) Changed(sessionID string) <-chan struct{}
```

### Pinned strings

- `Stacks.Stack` before `Bind`: `rpc: stacks are not bound to a connection`.
- `Stacks.Release` with subagent 0: `rpc: the main transcript cannot be released`.
- `session/stack_unsubscribe` with subagent 0: invalid params, `session/stack_unsubscribe requires subagentId`.
- `session/stack_unsubscribe` without a session: invalid params, `session/stack_unsubscribe requires sessionId`.

## Task 1: Wire types

**Goal:** the stack patch, its envelope and the stack's names are wire types, and the agent sends the same bytes as before.

**Files:**
- `internal/acpwire/stack.go`;
- `internal/acp/stack.go`, `internal/acp/stack_test.go`, and any other `internal/acp` test that names `stackPatch`.

**Steps:**

1. **Declarations.** Add the Contracts block to `acpwire/stack.go`.
2. **The agent.**
   - Replace `acp.StackParams` with the alias.
   - Delete `stackPatch` and use `acpwire.StackPatch` in `diff`, `notifyStackPatch` and the tests, setting `Kind` from `acpwire.UpdateStackPatch`.
   - `notifyStackPatch` keeps its map envelope and its omission rules exactly.
3. **Tests.** `stackPatchFromNotice` (`stack_test.go:52`) decodes into `acpwire.StackPatch` and keeps its key checks, which pin the unchanged envelope. Add `TestStackPatchDecodesAsTheWireType`, which decodes a flushed patch's raw envelope as an `acpwire.SessionUpdate` and its update as an `acpwire.StackPatch`, and compares every field with the patch `diff` produced.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpwire/ ./internal/acp/
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acpwire/ -count=1
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acp/ -run 'Stack|Hold|LastRequest' -count=1
```

Expected: all pass, `TestWireFieldsFollowTheRules` included.

## Task 2: The agent's cadence and unsubscribe

**Goal:** a marshal client's stacks flush every 50ms, and a client can stop a subagent's stack being projected.

**Files:** `internal/acp/stack.go`, `internal/acp/turn.go`, `internal/acp/host.go`, `internal/acp/stack_test.go`.

**Steps:**

1. **The config.** Add `TurnManagerConfig.Marshal` and keep it on the `TurnManager`. Add `newTurnManagerFor`'s parameter, and pass `srv.marshalClient` from `registerHandlers`.
2. **The intervals.**
   - Add `marshalStackInterval` and `stackIntervals`.
   - The turn's flush ticker (`turn.go:927`) uses the turn interval, and the idle loop's ticker (`stack.go:343`) uses the idle interval. Each is read when its loop starts.
   - Rewrite the two constants' comments: they are now the intervals for clients without the capability.
3. **Unsubscribe.** `StackUnsubscribe` decodes `StackParams`.
   - A missing session ID, or subagent 0, is the pinned invalid-params error.
   - Otherwise it deletes the projector under `stackKey(session, subagent)` from `m.stacks`, under `stacksMu`, and returns an empty object.
   - Register it as `session/stack_unsubscribe` beside `session/stack`.
   - `flushChildStacks` lists keys before it flushes, so a key deleted meanwhile must not come back. Change its loop to skip a key `m.stacks` no longer holds, instead of recreating it through `stackFor`.
4. **Tests** (`stack_test.go`):
   - `TestMarshalClientsGetTheFastStackCadence`: a manager whose `Marshal` reports true returns 50ms for both intervals. One with `Marshal` false, or nil, returns 200ms and 500ms.
   - `TestStackUnsubscribeStopsASubagentsPatches`, on `newSyncStack` (`stacknode_test.go:30`) with a registered child as in `TestStackSubagentSnapshotAndPatch` (`:520`):
     - read the child's stack, then unsubscribe it;
     - change the child's transcript and flush the children. No patch is sent;
     - read the child's stack again. The snapshot is fresh, with revision 1, and the next change sends a patch again.
   - `TestStackUnsubscribeRefusesTheMainTranscript`: subagent 0, and a missing session, give the pinned errors. An unknown subagent returns `{}`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -n 'newTurnManagerFor(' internal/acp/host.go
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acp/ -count=1
```

Expected:
- the grep shows the call in `registerHandlers` passing `srv.marshalClient`;
- the package passes, and every existing stack test with it (V6).

Mutation check: make `stackIntervals` ignore `Marshal`, and `TestMarshalClientsGetTheFastStackCadence` fails. Restore it.

## Task 3: The client's mirror

**Goal:** `rpc.Stacks` keeps a correct copy of every stack it reads, from patches alone, and recovers from a gap.

**Files:** `internal/acpclient/rpc/stacks.go` (new), `internal/acpclient/rpc/stacks_test.go` (new).

**Steps:**

1. **State.** One mutex guards a map from `stackKey`'s form (session, and subagent when not 0) to an entry, and a map from session to its `Changed` channel. An entry holds:
   - the revision, the roots, and the nodes by ID;
   - the IDs in first-arrival order;
   - the cached read order, cleared on every change;
   - whether a fetch is in flight, and the queue of patches received during it.
2. **`Notify`.**
   - Handle only `session/update`. Decode the params as `acpwire.SessionUpdate`, and decode the update only when its `kind` is `stack_patch`. Anything else, or an update that does not decode, is ignored.
   - Find the entry for the session and the patch's subagent. A patch for a stack nobody read is dropped.
   - Then apply decision 5's rules, under the mutex. A gap starts the fetch of step 4.
   - After any applied patch, signal the session's `Changed` without blocking.
3. **`Stack`.**
   - An existing entry that is not fetching returns its snapshot at once. One that is fetching returns its last snapshot, which is empty during a first fetch, as a later read would.
   - With no entry:
     - create one in the fetching state;
     - call `session/stack` through the bound connection with `acpwire.StackParams`;
     - install the result as step 4 does, and return it.
   - On an error, delete the entry and return the error.
   - Before `Bind`, return the pinned error.
4. **Fetch and install.**
   - A gap's fetch runs on a new goroutine with a background context. The connection's end fails it, and then the entry is deleted, so the next read fetches again.
   - Installing replaces the entry's roots, nodes, first-arrival order and revision with the snapshot's. It then drains the queue in order: a patch at or below the revision is dropped, one that chains applies, and one that does not starts another fetch, keeping the rest queued. Finally it signals `Changed`.
5. **The read order.** On a read after a change, rebuild the order: depth-first from each root through each node's `children`, visiting a node once, then every unvisited node in first-arrival order. A child ID with no node is skipped. A read copies `roots` and the ordered nodes into a new `StackSnapshot` with the session, the subagent and the revision.
6. **`Release`.** Subagent 0 is the pinned error. Otherwise delete the entry and call `session/stack_unsubscribe`, returning its error.
7. **Tests** (`stacks_test.go`). Each drives a fake agent over `Pipe`, as P3 plan 1's `conn_test.go` does, with the connection's `Handlers.Notify` set to the mirror's `Notify`:
   - `TestStacksFirstReadFetchesThenServesFromTheMirror`: the first read sends one `session/stack` and returns its snapshot. A second read sends nothing.
   - `TestStacksApplyChainedPatches`: after a snapshot at revision 1, patches 1→2 (an upsert and a new child) and 2→3 (a removal) give the expected nodes, roots and revision. `Changed` fired.
   - `TestStacksDropDuplicatePatches`: a patch at or below the revision changes nothing.
   - `TestStacksRefetchOnAGap`: a patch 5→6 on a mirror at revision 2 starts one `session/stack` fetch. A patch 9→10 arrives while it is in flight, and the agent answers with revision 9. The mirror ends at revision 10 with both changes.
   - `TestStacksQueueDuringTheFirstFetch`: the agent sends a patch 4→5 before answering the first read with revision 4. The read returns at revision 4, and the mirror is at 5 afterwards.
   - `TestStacksReadOrderFollowsRootsAndChildren`: a patch adds a child in the middle of a turn's children. The read lists it in the depth-first position, and the band-like unreached node last.
   - `TestStacksReleaseUnsubscribes`: releasing a subagent sends `session/stack_unsubscribe` with its params and drops its entry. Releasing subagent 0 is the pinned error.
   - `TestStacksIgnoreOtherUpdates`: an `agent_message_chunk` update, and a patch for a stack never read, change nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/rpc/
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acpclient/rpc/ -count=1 -race
```

Expected: all pass under `-race`, `TestRPCImportsStayOnTheWireSide` included. Mutation check: make `Notify` drop patches while a fetch is in flight instead of queueing them, and `TestStacksRefetchOnAGap` fails. Restore it.

## Task 4: The mirror against the real projector

**Goal:** over the pipe, the mirror converges on exactly what the agent's projector sent, and R10's budgets hold on a 2,000-call session.

**Files:** `internal/acp/stack_wire_test.go` (new).

**Steps:**

1. **The agent.** `startStackAgent(t, st *session.State)` serves, through `startTestAgent`, a `TurnManager`:
   - its `Lookup` returns a `TurnRuntime` over `st` with an event broker, as `newSyncStack` does;
   - its `Notify` is the server's own `Notify`;
   - its `Marshal` reports true.

   It registers `session/stack`, `session/stack_unsubscribe` and a `test/ping` handler that returns at once, and returns the manager and a client `Conn` whose `Handlers.Notify` is a bound `Stacks`.
2. **Settling.** `settle(t, conn)` calls `test/ping`. By decision 6 of P3 plan 1, every patch sent before the call is applied when it returns.
3. **The invariant.** `sameAsProjector(t, m, mirror, key)` compares the mirror's read with the projector's state: the projector's `sent` map and `sentOrder` against the read's nodes, each encoded with `json.Marshal`, in the same order, with the same roots and the same revision. The test reads the projector under its mutex, as `stackSnapshot` does.
4. **Convergence tests:**
   - `TestStackMirrorConvergesThroughATurn`. A scripted session: a user message, a step with three tool calls, narration, a finished step, a final reply, then a second turn.
     - Read the stack, then after each change call `m.flushStack`, `settle`, and check `sameAsProjector`.
     - The check also holds with `m.flushDirtyStack` and a band wake, as in `TestStackDirtyFlushRunsWhenBandWakeIsDue` (`stack_test.go:634`).
   - `TestStackMirrorRecoversFromALostPatch`:
     - lose a patch: change the session, then, under the main projector's mutex, call `diff` on a fresh `viewmodel.ProjectState` without notifying. The projector's revision moves on and the client never hears of it;
     - change the session again and flush. That patch is a gap for the mirror;
     - after `settle` and waiting for the refetch, the mirror matches the projector.
   - `TestStackMirrorFollowsASubagent`:
     - read a registered child's stack, change the child, flush, settle and check the child's key;
     - `Release` it. A child change then sends no patch, and the mirror has no entry for it.
5. **The 2,000-call session.** `largeStackSession(t)` builds 200 turns. Each has:
   - a user message;
   - one step with ten `shell.run` calls through `LogToolCall`, each with distinct IDs, `Args` naming a command, and a 1 KB `ResultContent`;
   - the step's end, and a final reply.

   Log its node count and the snapshot's encoded size once, as `t.Logf`, for the implementation record.
6. **Budgets.** Run each as a benchmark and as a test with the budget, so the full suite checks it:
   - `BenchmarkStackPatchApply`: on a mirror installed from the large session's snapshot, apply a ten-node patch through `Notify`, from its raw bytes, with each iteration chaining the revision. `TestStackPatchApplyBudget` fails when the median of 20 runs reaches 2ms.
   - `BenchmarkStackRoundTrip`: a fresh `Stacks` reads the large session's stack over the pipe, from the request to the installed mirror. `TestStackRoundTripBudget` fails when the median of 10 runs reaches 50ms.
   - `BenchmarkStackFlushLargeSession`: one `flushStack` of the large session after one new tool call, on the agent alone. It has no budget; its result goes in the implementation record.
   - The budget tests skip under `-short` and when the race detector is on (a `race` build tag file sets a flag), because both distort timing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acp/ -run 'TestStackMirror|Budget' -count=1 -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL|.*nodes)'
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acp/ -run 'TestStackMirror' -count=3 -race
TMPDIR=/private/tmp/p3-4a-suite go test ./internal/acp/ -run '^$' -bench 'BenchmarkStack' -benchtime 20x
```

Expected:
- the convergence tests pass, under `-race` too;
- both budget tests pass;
- the benchmarks report under 2,000,000 ns/op for the patch and under 50,000,000 ns/op for the round trip. If either misses, stop and report (R10): do not loosen a budget, shrink the fixture or skip the test.

Mutation check: make the mirror's read order first-arrival order only, and `TestStackMirrorConvergesThroughATurn` fails. Restore it.

## Task 5: Docs and verification

**Goal:** the spec records plan 4a, and the full suite is green.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`.

**Steps:**

1. **The spec.** Add a "Plan 4a — the stack over the wire" entry to the Implementation record, after plan 1's. It lists:
   - the `acpwire` types;
   - the 50ms cadence for marshal clients and `session/stack_unsubscribe`;
   - `rpc.Stacks` and its rules;
   - the large session's node count and size, and the measured patch apply, round trip and agent flush, on the "development machine";
   - what is left for plan 4b.
2. **`AGENTS.md`.** No change: plan 1 already named `rpc/` in "Where things live".
3. **Main may have moved.** If P2 plan 7b merged while this branch ran, merge `main` into the branch before the full verification. No conflict is expected.
4. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd ./web
go vet ./...
TMPDIR=/private/tmp/p3-4a-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p3-4a-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `8e2245d5`, or it is one of the load flakes below and passes on its own with `-count=3`. A budget test that fails only while plan 7b's suite runs is load: rerun it alone. If it still fails, stop and report.

**Baseline.** At `8e2245d5`, P3 plan 1's run passed. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

No manual check: nothing a user runs reads the mirror yet.

## Integration notes

- **P2 plan 7b** runs alongside. The plans share no file. Whichever merges second merges `main` first and reruns the suite.
- **P3 plan 3** adds read-your-writes to the server. A stack flush before a call's response belongs to that mechanism, and so does the session type that holds a `Stacks`.
- **P3 plan 4b:**
  - `rpc.Session.Stack(subagentID, busy)` reads the mirror. The main stack is fetched when the session attaches, so its reads never do I/O. A subagent's first read fetches synchronously (R4).
  - The `busy` decision against the T6 goldens.
  - When the TUI leaves a drill-in, the session calls `Release`. The interface needs a way to say so, or the session releases a subagent stack that has gone unread.
  - `StackNode` and `Callers` over the wire.
  - If the agent's flush at 2,000 calls (Task 4) takes a large share of 50ms, 4b revisits the cadence before the switch.

## Self-review

- **Every task verifiable alone.** Task 1 moves a type without changing a byte on the wire. Task 2 changes only marshal clients' timing, and adds a method no existing client calls. Task 3 is the mirror against a fake agent. Task 4 proves it against the real projector and measures it. Task 5 records it.
- **Cold executor.** The patch rules, the queue during a fetch, the order rebuild, the invariant the convergence tests check, the fixture's shape and the budget tests' statistics are spelled out.
- **Contracts.** The wire types, the agent's additions, the mirror's API and every error string are declared there.
- **Anchors.** Every path, symbol and line was checked at `8e2245d5`.
- **Spec.** R4 and the Migration order are updated, with the split recorded in "Decided with the user".
- **No placeholders.**
