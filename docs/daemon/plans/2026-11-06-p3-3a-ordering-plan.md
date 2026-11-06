# P3 plan 3a: Ordering — implementation plan

Date: 2026-11-06. Status: ready for review.
Base: `main` at `133f005b` (P2 plans 7c and 7d merged, plan 8a's docs committed), while P2 plans 8a and 8b execute. Every anchor below was checked there.
- **It can start at once.** It touches only `internal/acp` and `p3-acp-transport.md`. P2 plans 8a, 8b and 9 touch no file in `internal/acp`. Plan 9 edits the P3 spec too, in other sections.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Depends on:` and `Status:`, R2's "Read your writes" sub-bullets, and Migration order item 3's split into 3a and 3b. Task 4 adds only the Implementation record.
- The JSON on the wire does not change: no new method, update kind or field. Only when the agent writes some updates moves, and only for a marshal client.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R2 (read your writes), R10 (the synchronous-call budget), R11 (never a hang), Migration order item 3a, and tests V3 and V6.
Previous plan: [P3 plan 4a, the stack over the wire](2026-11-05-p3-4a-stack-wire-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** for a marshal client, a call's response follows every update the call caused, so a read after a call sees its effect, as it does in process.
- The server runs a response barrier between a session call's handler and its response.
- Every goroutine that forwards a session's events as updates serves a fence: it writes the updates for everything published before the fence, flushes what it owns, and answers.
- The guarantee is proved over the pipe with deliberately slowed updates, and its cost on a 2,000-call session is measured against R10.

**Non-goals:**
- **No `session/status` and no `session_status`.** Plan 3b adds them after layering, and adds `session_status` to the barrier.
- **No change for other clients.** A client without the marshal capability gets its responses exactly as today (V6).
- **Host calls flush nothing.** No call that names no session causes a session update today. Plan 3b revisits this when `session_status` exists.
- **No faster projection.** A call that changes a large transcript pays one projection before its response. The idle tick paid it 50ms later before. The cost is measured and recorded, not reduced.
- **No TUI, P2, `acpclient` or bridge change.** No changelog entry.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-3a-suite`. Create the directory first: `mkdir -p /private/tmp/p3-3a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. P2 plans 8a, 8b and 9 use `/private/tmp/p2-8a-suite`, `/private/tmp/p2-8b-suite` and `/private/tmp/p2-9-suite`. Never share a directory between runs.
- **The base.** The full suite at `6c6d16b2` passed 117 packages, with `internal/acp`'s `TestStackPatchApplyBudget` and `TestStackRoundTripBudget` failing under full-suite load and passing alone with `-count=3`. `133f005b` adds only docs. This plan was written without rerunning the suite, to keep load off plans 8a and 8b. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` hangs about one run in three in its fake agent. A hang there is known.
- **Up to three suites may run at once.** Load makes the flakes listed under Task 4 likelier. Rerun a failure alone before treating it as new.
- **How a response is written today** (`internal/acp/server.go`): `dispatchRequest` (`:658`) runs the handler through `dispatch`, removes the request from the in-flight registry, and encodes the response under `outMu`. A notification is encoded under the same mutex, synchronously, by `Notify` (`:165`). So a notification whose `Notify` returned before the response is encoded precedes it on the wire.
- **Every update reaches the wire through the host's sink.** `registerHandlers` (`host.go:295`) gives each manager `h.sink.Notify`, and `serveConn` points the sink at the connection's `srv.Notify` (`host.go:633`). The sink writes synchronously on the caller's goroutine.
- **Updates written on the handler's own goroutine already precede its response.** `session/hold` with no turn (`hold.go:34`), `session/set_mode`'s `mode_changed` (`interaction.go:207`), `session/load`'s replay (`session.go:784`) and the end of every turn (`finishTurn`, `turn.go:1459`) all notify before they return.
- **Four goroutines write updates the handler does not wait for:**
  - **The turn's forwarder** (`runTurn`, `turn.go:645`). It subscribes to the session's events with a terminal subscription (`:730`), and its loop (`:939`) forwards each event through `forward` (`:783`), which marks the stack dirty and sends the event's update (agent text, tool calls, `steering_state`, `steering_receipt`, `hold` and others). A flush ticker (`:935`) flushes dirty stacks. After the loop, a drain (`:971`) forwards what is left, and `finishTurn` flushes and sends the telemetry.
  - **The idle stack loop** (`ensureStackIdle`, `stack.go:371`), started by the first `session/stack` read (`stack.go:289`). It subscribes to the session's events (non-terminal), marks the main stack dirty on each, and on each tick with no active turn flushes the main projector (dirty only), every subagent projector and the run detail.
  - **The work forwarder** (`ensureWorkForwarder` and `runWorkForwarder`, `watch.go:453` and `:485`), started by the first `work_list` or `work_inspect`. It forwards each work snapshot of the bound epoch as a `work_state` update.
  - **The watch forwarder** (`ensureForwarder`, `watch.go:191`). It forwards each watch event as a `watch` update.
- **A publish that returned has reached the subscriber's buffer, or been dropped.** `pubsub`'s `publish` (`internal/pubsub/broker.go:301`) sends synchronously: blocking for a terminal subscription, best effort otherwise. So when a handler returns, every event it published is either already consumed by the forwarder or waiting in the forwarder's channel. A best-effort drop only happens to a full buffer, which already holds an event that marks the same thing.
- **The pipe applies frames in arrival order.** P3 plan 1's connection hands each notification to `Handlers.Notify` on its read goroutine before it delivers a later response (`internal/acpclient/rpc/conn.go`). So a client sees an update before the response that follows it.
- **The test servers.** `startTestAgent` (`client_core_test.go:25`) serves a `Server` on a pipe. `stackAgent` (`stack_wire_test.go:36`) builds a marshal-capable `TurnManager` over a state and a broker, with an `rpc.Stacks` mirror. `largeStackSession` (`:335`) builds the 2,000-call session. `raceEnabled` (`stackwired_race_test.go`) lets a budget test skip under the race detector.

**Decisions this plan makes beyond the spec** (R2 and the Migration order record them):

1. **Ordering goes early, as plan 3a** (proposed 2026-11-06, for the user to confirm). The guarantee needs only the server and the update forwarders, which exist and which no P2 plan changes. So it runs alongside P2's close-out. Plan 3b, after layering, keeps the rest of plan 3: `session/status`, `session_status`, `Changed`, the notice calls and the conformance harness.
2. **The barrier runs only for a marshal client.** For any other client the response is written exactly when it is today (R9: no default changes).
3. **The barrier is per session.** It runs only for a request whose params carry a top-level `sessionId`, and fences only that session's forwarders. A request without one, a notification, and a request whose params are not an object skip it.
4. **A fence is drain, flush, answer.** A forwarder asked for a fence:
   - takes the number of events buffered in its channel at that moment, and handles exactly that many, as its loop would;
   - then flushes what it owns: the turn's forwarder its dirty stacks, the idle loop its stacks and run detail when no turn is active;
   - then answers.

   Those buffered events are the ones published before the fence, so their updates precede the response. Events published later wait for the loop.
5. **A forwarder's fence lives exactly as long as its loop.** The turn's forwarder registers its fence when it subscribes and removes it after `finishTurn`, so a fence asked during the turn's last drain waits until the drain has written everything. A fence whose forwarder has gone answers at once.
6. **A stuck fence never hangs a call** (R11). The barrier waits at most two seconds. Then it logs a warning naming the method and the session, and the response is written.
7. **The barrier runs after the request leaves the in-flight registry,** so a `$/cancel_request` that arrives during the barrier is ignored, as one arriving after the response would be.
8. **The budget.** On the 2,000-call session, with the main stack subscribed, a session call that changes nothing must round-trip in under 1ms median over the pipe (R10's synchronous-call budget). A call that adds one message to the transcript is measured too, with no budget: it carries one projection of the session. A missed budget stops the plan (R10).

**Execution:**
- Branch first: `git switch -c p3-3a-ordering` from `main`.
- If `git status` shows `docs/daemon/p3-acp-transport.md` or this plan uncommitted, commit them first as explicit paths, with the message `p3-3a-ordering: record the plan 3 split and plan 3a`. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, P2 plan 9's plan, and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p3-3a-ordering: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Fences (`internal/acp/fence.go`, new)

```go
// fence lets one forwarder goroutine confirm that it has written the
// updates for every event published before it was asked (P3 R2).
type fence struct{ /* unexported */ }

func newFence() *fence

// requests is the channel the forwarder's loop serves. Each request is a
// channel the forwarder closes once it has drained, flushed and answered.
func (f *fence) requests() <-chan chan struct{}

// ask blocks until the forwarder answers, the forwarder closes the fence,
// or ctx ends.
func (f *fence) ask(ctx context.Context)

// close marks the forwarder gone: pending and later asks return at once.
// It is idempotent.
func (f *fence) close()

// fenceSet holds the live fences of one manager's forwarders, by session.
type fenceSet struct{ /* unexported */ }

func (s *fenceSet) add(sessionID string, f *fence)
func (s *fenceSet) remove(sessionID string, f *fence)

// wait asks every fence registered for sessionID, one after another, in
// the order they were added.
func (s *fenceSet) wait(ctx context.Context, sessionID string)
```

### Server (`internal/acp/server.go`)

```go
// responseBarrierTimeout bounds how long a response waits for its session's
// fences (P3 R11).
const responseBarrierTimeout = 2 * time.Second

// SetResponseBarrier installs fn, which the server runs after a request's
// handler returns and before its response is written, when the client
// declared the marshal capability and the request's params name a session
// (P3 R2). fn returns once every update the call caused is written.
func (s *Server) SetResponseBarrier(fn func(ctx context.Context, sessionID string))
```

The `Server` gains two unexported fields, both guarded by `stateMu`: the barrier, and its timeout (set to `responseBarrierTimeout` by `NewServer`, and shortened by tests).

### Managers

- `TurnManager`, `WorkManagerACP` and `WatchManagerACP` each gain a `fences fenceSet` field and a method `fenceSession(ctx context.Context, sessionID string)` that calls `fences.wait`.
- `responseBarrier(turns *TurnManager, works *WorkManagerACP, watches *WatchManagerACP) func(ctx context.Context, sessionID string)` in `host.go`: fences the turn manager's, then the work manager's, then the watch manager's forwarders for the session. `registerHandlers` passes its result to `srv.SetResponseBarrier`.

### Pinned strings

- The timeout's warning: message `acp: response barrier timed out`, with attributes `method` and `session_id`.

## Task 1: Fences

**Goal:** a tested fence and fence set, used by nothing yet.

**Files:** `internal/acp/fence.go` (new), `internal/acp/fence_test.go` (new).

**Steps:**

1. **`fence`.** An unbuffered channel of requests, a `done` channel closed once by `close` (guarded by a `sync.Once`).
   - `ask` makes a request channel, sends it on `requests` in a select with `done` and `ctx.Done()`, then waits for the request channel to close, in a select with `done` and `ctx.Done()`.
   - A forwarder serves a request by receiving it from `requests()`, doing its drain and flush, and closing it.
2. **`fenceSet`.** A mutex and a map from session to a slice of fences, in the order added. `remove` deletes one fence by identity and drops an empty slice. `wait` copies the session's slice under the mutex and asks each fence outside it.
3. **Tests** (`fence_test.go`):
   - `TestAFenceWaitsForItsForwarder`: a goroutine serving the fence closes each request only after setting a flag. `ask` returns after the flag is set.
   - `TestAFenceReturnsWhenItsForwarderIsGone`: `ask` on a closed fence returns at once. An `ask` blocked on a fence nobody serves returns when another goroutine closes the fence. `close` twice does not panic.
   - `TestAFenceReturnsWhenTheContextEnds`: an `ask` on an unserved fence returns once its context is cancelled.
   - `TestAFenceSetAsksEveryFenceInOrder`: two served fences for one session record their order of service, which is the order added. A fence for another session is not asked. `remove` stops a fence being asked.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-3a-suite go test ./internal/acp/ -run 'Fence' -race -count=1
```

Expected: the four tests pass under `-race`.

## Task 2: The forwarders serve fences

**Goal:** each of the four forwarders registers a fence for its session while its loop runs, and serves it by draining, flushing and answering.

**Files:** `internal/acp/turn.go`, `internal/acp/stack.go`, `internal/acp/watch.go`, `internal/acp/fence_test.go`.

**Steps:**

1. **The managers.** Add the `fences` field to `TurnManager`, `WorkManagerACP` and `WatchManagerACP`, and each one's `fenceSession`. A zero `fenceSet` must work, so the constructors need no change beyond what the map needs.
2. **The turn's forwarder** (`runTurn`).
   - Directly after the terminal subscription (`turn.go:730`), create a fence and add it to `m.fences` under the session. Register a deferred function there that removes and closes it. Deferred functions run after the return value is computed, so it outlives the drain and `finishTurn`.
   - Add a case to the forwarding loop's select (`:939`) for the fence's requests. On a request:
     - take the number of events buffered in `sub` at that moment, and forward that many, as the event case does, stopping early if the channel closes or `turnCtx` ends;
     - then flush the dirty stacks, as the flush ticker's case does;
     - then close the request.
   - The drain after the loop (`:971`) does not serve fences. A fence asked then waits for the deferred close, which comes after everything is written.
3. **The idle stack loop** (`ensureStackIdle`). Inside the goroutine, create a fence, add it to `m.fences`, and remove and close it when the goroutine returns. Add a select case for its requests. On a request:
   - take the number of buffered events and mark the stack dirty that many times, as the event case does, stopping early if the channel closes;
   - then, if no turn is active, do exactly what the tick does: look the session up (dropping the stacks when it has gone), flush the main projector (dirty only), the subagent projectors and the run detail;
   - then close the request.
4. **The work forwarder.** In `ensureWorkForwarder`, after the subscription and the epoch, create the fence and add it to `m.fences`, and pass it to `runWorkForwarder`. Its deferred retirement removes and closes it. Add a select case for its requests: forward that many buffered snapshots with the epoch check, then close the request.
5. **The watch forwarder.** In `ensureForwarder`, create the fence before starting the goroutine and add it to `m.fences`. The goroutine's deferred cleanup removes and closes it. Add a select case: forward that many buffered events, as the event case does, then close the request.
6. **Tests** (`fence_test.go`, on the managers directly, no server):
   - `TestTheTurnForwarderFenceCoversEventsPublishedBefore`: a `TurnManager` whose `Notify` records each update kind, with a turn running as in `TestSteerDeliversToActiveTurn` (`turn_test.go:1805`). Its runtime's `Steer` publishes an `EventSteeringState` event on the session's broker, as the runtime does. After `Steer` returns, `fenceSession` returns only once a `steering_state` update has been recorded. Make `Notify` sleep 50ms for `session/update` frames, so a fence that did not wait would return first.
   - `TestTheIdleStackFenceFlushesAChange`: a manager over a state and broker, as `stackAgent` builds one, with the main stack read once so the idle loop runs. Add a message to the state and publish a session event. `fenceSession` returns only after a `stack_patch` carrying the new message's node has been recorded.
   - `TestTheWorkForwarderFenceCoversASnapshot`: a work manager over a fake work service whose subscription the test feeds. Extend `fakeWorkService` (`watch_test.go:171`) with an optional channel that `SubscribeWork` returns when set. After one `work_list`, publish a snapshot of the bound epoch on the channel. `fenceSession` returns only after a `work_state` update has been recorded.
   - `TestTheWatchForwarderFenceCoversAnEvent`: a watch manager whose lookup returns a broker the test owns, started by one `watch_list`. Publish one event. `fenceSession` returns only after a `watch` update has been recorded.
   - `TestAFenceOnAFinishedTurnReturnsAtOnce`: after a turn ends, `fenceSession` for its session returns without blocking, because the turn's fence was removed.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-3a-suite go test ./internal/acp/ -run 'Fence' -race -count=1
TMPDIR=/private/tmp/p3-3a-suite go test ./internal/acp/ -count=1
```

Expected: the fence tests pass under `-race`, and the whole package passes, every existing turn, stack, work and watch test with it (V6).

Mutation check: make the turn's forwarder close a fence request without forwarding the buffered events. `TestTheTurnForwarderFenceCoversEventsPublishedBefore` fails. Restore it.

## Task 3: The response barrier

**Goal:** over the pipe, a marshal client's call returns only after the updates it caused, and every other call is answered as before.

**Files:** `internal/acp/server.go`, `internal/acp/host.go`, `internal/acp/ordering_test.go` (new).

**Steps:**

1. **The server.**
   - Add `responseBarrierTimeout`, the two fields and `SetResponseBarrier`. `NewServer` sets the timeout.
   - In `dispatchRequest`, after the request leaves the in-flight registry and before the response is built, run the barrier when all hold:
     - one is installed;
     - `marshalClient()` reports true;
     - the params decode as an object with a non-empty string `sessionId`.
   - The barrier gets a context derived from the handler's, bounded by the timeout. If the bound passes first, log the pinned warning with the method and the session, and write the response.
   - The barrier runs for error responses too: a failed call may have published.
   - `dispatchNotification` is unchanged.
2. **The host.** Add `responseBarrier`, and call `srv.SetResponseBarrier(responseBarrier(turns, works, watches))` in `registerHandlers` once all three managers exist.
3. **Tests** (`ordering_test.go`). One helper builds what `registerHandlers` builds for a marshal client, on `startTestAgent`:
   - a `TurnManager`, a work manager and a watch manager over a test state, broker, fake work service and watch broker, each notifying through a wrapper of `srv.Notify` that sleeps 50ms before every `session/update` frame;
   - the handlers the tests call, and `test/ping`;
   - the barrier from `responseBarrier`, and `srv.setMarshalClient(true)`;
   - a client `rpc.Conn` whose `Notify` records each update and feeds an `rpc.Stacks` mirror, as `stackAgent.notify` does.

   The tests:
   - `TestAReadAfterACallSeesItsTranscriptChange` (V3): read the main stack through the mirror. Call a test handler that adds a message to the state and publishes a session event, as the runtime does. When the call returns, the mirror's read, with no fetch, already holds the new message's node.
   - `TestAResponseFollowsTheSteeringItCaused`: with a turn running, `session/steer` returns only after the client has recorded a `steering_state` update.
   - `TestAResponseFollowsTheWorkStateItCaused` and `TestAResponseFollowsTheWatchEventItCaused`: a test handler naming the session publishes one snapshot (or one watch event), and the client has the update when the call returns.
   - `TestTheBarrierWaitsOnlyForMarshalSessionCalls` (V6): wrap the installed barrier to count its runs. A call from a client the server has not recorded as marshal, a call whose params carry no `sessionId`, a call with array params and a notification all leave the count at zero. One marshal session call makes it one.
   - `TestAStuckFenceDoesNotHoldTheResponse` (R11): set the server's barrier timeout to 50ms, and add a fence nobody serves to the turn manager's set for the session. A session call returns in well under a second, and the logger records the pinned warning. Capture the log with `WithLogger` over a buffer.
   - Each test runs under `-race` in Verify.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
grep -n 'SetResponseBarrier' internal/acp/host.go
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-3a-suite go test ./internal/acp/ -run 'ReadAfterACall|ResponseFollows|BarrierWaits|StuckFence|Fence' -race -count=3
TMPDIR=/private/tmp/p3-3a-suite go test ./internal/acp/ -count=1
```

Expected:
- the grep shows the call in `registerHandlers`;
- the ordering tests pass three times under `-race`;
- the whole package passes.

Mutation checks, each restored after:
- make `responseBarrier` return a function that does nothing. `TestAReadAfterACallSeesItsTranscriptChange` and the three `ResponseFollows` tests fail.
- drop the `marshalClient()` condition. `TestTheBarrierWaitsOnlyForMarshalSessionCalls` fails.

## Task 4: The budget and the docs

**Goal:** the barrier's cost is measured against R10, and the spec records plan 3a.

**Files:**
- `internal/acp/ordering_test.go`;
- `docs/daemon/p3-acp-transport.md` (the Implementation record only).

**Steps:**

1. **`TestResponseBarrierBudget`**, skipped under `-short` and when `raceEnabled`, as the 4a budget tests are. Over the pipe, on `largeStackSession`, with the barrier installed for a marshal client and the main stack read once:
   - time 30 session calls to a handler that changes nothing; the median must be under 1ms;
   - time 10 calls to a handler that adds one message and publishes its event; log the median, with no budget;
   - log both medians with `t.Logf`, as `TestStackRoundTripBudget` does.
2. **`BenchmarkResponseBarrierNoChange`**: the same no-change call, for the record.
3. **The spec.** R2, Migration order item 3 and the header already carry this plan's decisions, committed with the plan. Add a "Plan 3a — ordering" entry to the Implementation record, after plan 4a's. It lists the fence and fence set, the barrier and its conditions, the four forwarders and what each flushes, the timeout, and the two medians with the machine they were measured on.
4. **Main may have moved.** If P2 plan 8a, 8b or 9 merged while this branch ran, merge `main` into the branch before the full verification. Plan 9 edits the P3 spec's header, "Where the code is today" and a new section after "In scope". Expect a conflict there only if both plans changed the same lines, and keep both sides.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-3a-suite
go vet ./...
TMPDIR=/private/tmp/p3-3a-suite go test ./internal/acp/ -run 'Budget' -count=1 -v 2>&1 | grep -E 'median|--- |ok|FAIL'
TMPDIR=/private/tmp/p3-3a-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `go vet` is clean;
- `TestResponseBarrierBudget` passes, and logs the no-change median under 1ms and the change median;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, and this plan's `TestResponseBarrierBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

## Self-review

- **Spec coverage.** R2 read your writes: Tasks 2 and 3. R10: Task 4's budget. R11: Task 3's timeout test. V3: Task 3's ordering tests under `-race` with slowed updates. V6: `TestTheBarrierWaitsOnlyForMarshalSessionCalls`, plus the package's existing tests.
- **What could go wrong.**
  - **A deadlock between a barrier and a forwarder.** A forwarder never waits on a handler. The turn's forwarder sends permission requests from separate goroutines, and the barrier holds no lock while it waits. The timeout bounds any case missed.
  - **A turn that outlives its connection.** Its forwarder's fence is on the old connection's `TurnManager`, so a new connection's barrier does not wait for it. Embedded mode has one connection. P4's reattach revisits this.
  - **Cost per call.** A fence on an unchanged session drains nothing and finds the stack clean. Subagent projectors are projected on every fence, as on every tick. Task 4 measures both cases.
