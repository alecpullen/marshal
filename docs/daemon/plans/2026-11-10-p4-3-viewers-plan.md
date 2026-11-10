# P4 plan 3: Concurrent viewer connections — implementation plan

Date: 2026-11-10. Status: ready for review.
Base: `main` at `7802701f` (P4 plans 1 and 2 merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p4-c`, `-timeout 20m`): 120 packages ok, `web/bridge` included. The one failure was `internal/acp`'s `TestStackRoundTripBudget`, the known load flake, which passed three times when rerun alone.
- **The spec edits are already made,** uncommitted in the main checkout, in `p4-local-daemon.md`: the header, D2 (the viewer socket, viewer connections, the one run slot, the project child), D3 (the handshake's details, the relay's costs, the registry, the client's half), the Design table, Migration order items 3 and 4, and an Implementation record placeholder for each of plans 3 and 4.
- **Plan 4 runs beside this one.** It touches the bridge, `internal/daemon` and a new `test/daemon`, none of which this plan touches. Its last task waits for this plan to be on `main`, because it runs a real child with `--viewers`. Both plans fill their own placeholder in the spec's Implementation record.

Approved design: [p4-local-daemon.md](../p4-local-daemon.md): Goal, D2 (project children: the viewer socket, viewer connections, one run slot), Migration order item 3, Tests W2, W3 and W10.
Previous plans: [P4 plan 1, turns outlive their caller](2026-11-09-p4-1-runs-plan.md) and [P4 plan 2, the local profile](2026-11-09-p4-2-local-profile-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** one `marshal acp` process serves the bridge's own connection on stdio and any number of marshal connections at once on a unix socket, and a session runs one run at a time whichever connection started it.
- `marshal acp --viewers unix://<path>` accepts viewer connections concurrently, beside stdio.
- Each viewer connection's updates go to that connection only.
- A session's run slot is shared by the service's run path and ACP's own turn path.

**Non-goals:**
- **No bridge change.** Plan 4 starts the children and relays to them.
- **No TUI change.** The TUI reaches a viewer connection in plan 5.
- **No change to the stdio, `--listen` or embedded paths,** apart from the shared run slot, which they cannot tell apart from today when one connection drives a session.
- **No wire change.** No method, field or code is new. `acpclient.Methods` and the registered method count are unchanged.
- **`run` does not show ACP's turns.** A turn on ACP's path holds the shared slot but stays out of `run` and `lastRun` (D2; P5 with H3).
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p4-3-suite`. Create the directory first: `mkdir -p /private/tmp/p4-3-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp`.
- **The host** (`internal/acp/host.go`).
  - `agentHost` holds the session manager, the notification sink and the turn-canceller chain. `newAgentHost` (`:99`) builds the manager over `sink.Notify`.
  - `registerHandlers` (`:359`) builds one connection's managers. Every one of them notifies through `h.sink.Notify`: the turn manager (`:361`), the control manager (`:369`), watches (`:401`), work (`:425`), status (`:450`), sheets (`:461`), requests (`:468`), settings (`:480`), ends (`:490`) and commands (`:535`).
  - It registers `session/new`, `session/load`, `session/resume` and `session/delete` straight to the session manager (`:382` to `:385`).
  - It appends the connection's canceller to the host's chain (`:658`, `addTurnCanceller` `:143`). It returns the connection's cleanup (`:671`), which stops the child approvals and the work, status, sheet, request, settings, control and end managers.
  - `serveConnWith` (`:717`) builds a `Server`, registers the handlers, points the sink at the server's `Notify`, serves, and clears the sink when the connection ends. `serveConn` (`:711`) is it with no configure.
- **The session manager** (`internal/acp/session.go`).
  - `Create` (`:410`) serves `session/new`; with `replaces` it swaps the session inside its runtime (`replace`, `:486`), which needs no notification.
  - `Load` (`:535`) replays the transcript through the manager's own notify, which is the host's sink (`replay`, `:980`). `Resume` (`:566`) and `Delete` (`:637`) start or remove runtimes.
- **The entry points.**
  - `Run` (`internal/acp/run.go:76`) builds the headless trust resolver and one project-host registry, and calls `runWithConfig` (`:58`): one host, one stdio connection, then the bounded close of every session.
  - `ListenAndServe` (`listen.go:26`) prepares a unix socket: it creates the directory `0700`, removes a stale socket, listens, and makes the socket `0600`. Then `listenAndServeWithConfig` (`:76`) accepts one connection at a time, each wrapped in `idleDeadlineConn` (10 minutes).
  - `cmd/marshal` dispatches `acp` at `main.go:79`. `acpListenSpec` (`:111`) finds `--listen`. The hooks `acpRunner` and `acpListener` (`:21`, `:22`) let `TestACPListenFlagRoutesToListener` (`main_test.go:111`) check the routing.
- **The server** (`internal/acp/server.go`).
  - `MaxLineBytes` (`:138`) defaults to 1 MiB. The embedding sets it to P3's 64 MiB frame cap (`embedding.go:75`; `rpc.MaxFrameBytes`, `rpc/transport.go:16`).
  - `initialize` (`status.go:295`) records whether the client declared the marshal capability (`setMarshalClient`, `server.go:503`). `dispatch` (`:887`) finds and runs a method's handler.
- **ACP's turn path** (`internal/acp/turn.go`).
  - `runTurn` (`:682`) reserves the connection's per-session slot (`:715`), refusing a second turn with "session %s already has an active turn". It then admits the work with `rt.BeginWork` (`:759`) and registers the stop group.
  - Its deferred cleanup releases the slot and closes `slot.done` (`:735`). A waiter started right after reads `TakeWatchResume` once `slot.done` closes and calls `ResumeIfIdle` (`:1189`), which goes through `runTurn` again.
  - `HasActiveTurn` (`:586`) reads this connection's slots only. The idle stack loop flushes while it reports no turn, which is how a service turn's transcript reaches a marshal client (P3 plan 6b).
- **The stack loops** (`internal/acp/stack.go`).
  - A connection's first `session/stack` read starts the session's idle loop (`ensureStackIdle`, `:509`). The loop runs on `context.Background()` until `dropStacks` (`:369`) cancels it or the session goes.
  - Nothing cancels a connection's loops when the connection ends.
- **A marshal client's runs are the service's.** `session/prompt`, `swarm_start`, `sdd_start` and `sdd_answer` reach the service through `marshalOr` (`controls.go:596` to `:605`). Child approvals skip a marshal connection (`ensureChildApprovals`, `child_approvals.go:48`), and so does the watch-resume binding (`bindWatchResume`, `host.go:308`). So a marshal connection never owns a turn in a `TurnManager`.
- **The service's run path** (`internal/acp/service/turns.go`).
  - `runTurn` (`:246`) takes the session's slot (`s.turn`, refusing with `errTurnBusy` at `:264`), then `BeginWorkCancelable`, then the stop group.
  - `endRun` releases the slot in the critical section that sets `lastRun` (`:384` to `:386`).
- **The state** (`internal/app/session/session.go`).
  - `BeginWork` and `BeginWorkCancelable` (`:1867`, `:1879`) admit a parent operation.
  - `BeginTurnStop` (`:3232`) registers one stop group and replaces any previous one: "one operation at a time is the session's contract", which nothing enforces today.
- **Test helpers that exist.**
  - `openEmbedding` (`embedding_test.go:44`) adopts a bare runtime with a service pair and dials it over `rpc.Pipe`.
  - `registerTestApproval` (`requests_test.go:204`) opens an approval on a state.
  - In `controls_test.go`:
    - `seamState` (`conformance_test.go:366`);
    - `newControlRunner` (`:44`);
    - `promptInBackground` (`:313`);
    - `TestAMarshalPromptRunsOnTheService` (`:341`), which builds a `TurnManager` over a fake runner beside a service.
  - `testRunConfig` (`listen_test.go:47`) is a host config that starts no runtime. `TestListenAndServeAcceptsSuccessiveConnections` (`:109`) dials a socket in a short temporary directory.

**Decisions this plan makes beyond the spec** (D2 records them):

1. **One host, two kinds of connection.** The stdio connection is served exactly as today: through the host's sink, with its canceller in the chain. A viewer connection is served by a new `serveViewer`, which builds the same managers with the connection's own `Notify` in place of the sink. It never sets the sink, and it never joins the canceller chain.
2. **`registerHandlers` takes the notify function and a viewer flag.** The stdio, `--listen` and embedding paths pass the sink's `Notify` and `false`, so what they register is unchanged. A viewer passes its server's `Notify` and `true`.
3. **A viewer serves marshal clients only.** The server gains a `MarshalOnly` field beside `EndOnPanic`. With it set:
   - `initialize` refuses a client that did not declare the marshal capability, with invalid request "a viewer connection serves marshal clients only", and records nothing;
   - `dispatch` answers every other call the same way until a marshal `initialize` has been answered.
4. **A viewer cannot start, load, resume or delete a runtime.**
   - Its `session/new` is served only with `replaces`, through the same `Create`.
   - `session/new` without `replaces`, `session/load`, `session/resume` and `session/delete` answer invalid request "a viewer connection cannot start or load a session".
   - The bridge owns sessions' lifecycles (D3), and `Load`'s replay would reach the bridge's connection through the sink.
5. **A viewer's teardown stops its stack loops.** `TurnManager` gains `Close`, which drops every session's stacks and run watchers, as `dropStacks` does for one. A viewer's cleanup calls it after today's cleanup. The stdio and `--listen` paths do not call it: an orphaned turn's own forwarder keeps reporting through the sink after its connection ends, and this plan leaves that path as it is.
6. **The run slot is a claim on the state.** `State.ClaimRun` reserves the session's one run and returns its release, which is idempotent. A second claim while one is held reports false.
   - The service takes it in `runTurn` right after taking `s.turn`. A failed claim gives `s.turn` back and refuses with `errTurnBusy`. `endRun` releases it in the critical section that releases `s.turn`, and each refusal path after the claim releases it too.
   - ACP's `runTurn` takes it right after reserving its own slot. A failed claim gives the slot back and refuses with the sentence it already uses, "session %s already has an active turn". Its cleanup releases the claim before it closes `slot.done`, so the post-turn watch resume finds the slot free.
   - A `TurnRuntime` with no state (the existing tests' fakes) takes no claim, as it has no stop group today.
7. **`--viewers`.**
   - It takes `unix://<path>` only. It is refused with `--listen`, and without a path.
   - `acp.RunWithViewers` is `Run` plus the viewer listener. It prepares the socket as `ListenAndServe` does, through one helper both share. It binds before serving stdio, so the socket accepts once `initialize` on stdio is answered.
   - It accepts viewer connections on their own goroutines, each with P3's 64 MiB line cap and no idle deadline.
   - When stdio ends or the context does, it closes the listener (which removes the socket), cancels and waits for every viewer connection, and then closes the sessions and the project hosts, as `Run` does.
8. **The cap.** `internal/acp` gains `viewerMaxFrameBytes`, 64 MiB. A test pins it equal to `rpc.MaxFrameBytes`, so `internal/acp` gains no import of the client in production code.

**Execution:**
- In the main checkout, if `git status` shows any of these uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon: record P4 plans 3 and 4`:
  - `docs/daemon/p4-local-daemon.md`;
  - this plan and `docs/daemon/plans/2026-11-10-p4-4-relay-plan.md`.

  Plan 4's executor may have committed them already. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p4-3-viewers` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p4-3-viewers: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The run claim (`internal/app/session/session.go`)

```go
// ClaimRun reserves the session's one run (P4 D2): the service's run path and
// ACP's turn path both take it at admission, so a session runs one run at a
// time whichever connection started it. ok is false while another claim is
// held. release is idempotent.
func (s *State) ClaimRun() (release func(), ok bool)
```

### The host (`internal/acp/host.go`, `viewers.go`)

```go
// registerHandlers registers every handler on srv. notify is where this
// connection's managers send their updates: the host's sink for the stdio,
// --listen and embedding connections, the server's own Notify for a viewer.
// viewer refuses the lifecycle calls a viewer may not make and keeps the
// connection's canceller out of the host's chain (P4 D2).
func (h *agentHost) registerHandlers(srv *Server, alive *atomic.Bool, notify NotifyFunc, viewer bool) func()

// serveViewer serves one viewer connection until it ends or ctx does. It
// never sets the host's sink.
func (h *agentHost) serveViewer(ctx context.Context, conn net.Conn) error

// viewerMaxFrameBytes is the largest frame a viewer connection reads: P3's
// frame cap, rpc.MaxFrameBytes.
const viewerMaxFrameBytes = 64 << 20

// RunWithViewers is Run plus a viewer listener on the unix socket at path
// (P4 D2).
func RunWithViewers(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, path string) error

// runWithViewersConfig is RunWithViewers over a listener the caller bound,
// for tests, as listenAndServeWithConfig is for ListenAndServe.
func runWithViewersConfig(ctx context.Context, stdin io.Reader, stdout io.Writer, ln net.Listener, cfg runConfig) error

// listenUnix prepares and binds a unix socket the way ListenAndServe always
// has: the directory 0700, a stale socket removed, the socket 0600.
func listenUnix(path string) (net.Listener, error)
```

### The server and the turn manager (`internal/acp/server.go`, `stack.go`)

```go
// On Server, beside EndOnPanic:

	// MarshalOnly refuses a client that did not declare the marshal
	// capability, and every call before a marshal initialize (P4 D2). Set it
	// before Serve.
	MarshalOnly bool

// Close drops every session's stacks and run watchers, stopping their idle
// loops. A viewer connection's cleanup calls it.
func (m *TurnManager) Close()
```

### The command (`cmd/marshal/main.go`)

```go
var acpViewersRunner = acp.RunWithViewers

// acpViewersSpec extracts --viewers (or --viewers=VALUE) from the acp
// subcommand's arguments. An empty return means no viewer socket.
func acpViewersSpec(args []string) (string, error)
```

## Task 1 — one run slot

**Files:** `internal/app/session/session.go`, a new `internal/app/session/runclaim_test.go`, `internal/acp/service/turns.go`, `internal/acp/turn.go`, a new `internal/acp/runslot_test.go`.

1. Add `ClaimRun` beside `BeginWork`, guarded by its own small lock or an atomic. A state that is nil returns a no-op release and true, as the state's other nil-tolerant methods do.
2. In the service's `runTurn`, take the claim right after `s.turn = slot` (decision 6). On a failed claim, give `s.turn` back under `s.mu` and refuse with `errTurnBusy`. Keep the release on the slot. Release it on the `BeginWorkCancelable` refusal path, and in `endRun`'s critical section that clears `s.turn`.
3. In ACP's `runTurn`, take the claim right after the connection's slot is reserved, when `slotState` is not nil. On a failed claim, delete the slot it just reserved, cancel `slotCtx`, and refuse with today's "already has an active turn" sentence. In the deferred cleanup, release the claim after the slot is deleted and before `close(slot.done)`, so the watch-resume waiter finds the slot free.
4. Tests in `runclaim_test.go`:
   - `TestASecondClaimIsRefused`: a second claim while one is held reports false, and a claim after the release succeeds.
   - `TestAReleaseIsIdempotent`: releasing twice frees the slot once, and a later holder's claim is not released by the first holder's second release.
5. Tests in `runslot_test.go` build a service and a `TurnManager` over one `seamState`, each with its own blocking runner, as `TestAMarshalPromptRunsOnTheService` does. The `TurnRuntime` carries the state.
   - `TestAnACPTurnRefusesAServiceRun`: while the turn manager's prompt blocks, the service's `Prompt` fails with `CodeBusy`, and its runner never starts. Once the ACP turn ends, a service prompt runs.
   - `TestAServiceRunRefusesAnACPTurn`: while the service's prompt blocks, `PromptTurn` fails with "already has an active turn", and the turn manager's runner never starts. Once the service run ends, an ACP prompt runs.
   - `TestARefusedStartLeavesTheSlotFree`: a refused start on either path leaves no claim behind: the other path's next run starts at once.
   - `TestTheWatchResumeFindsTheSlotFree`: an ACP turn arms the state's resume latch with `SetWatchResume` (`watch_resume.go:17`) before it returns. The waiter's `ResumeIfIdle` then starts the follow-on turn, which proves the claim was released before `slot.done` closed.

**Verify:**
- `go test ./internal/app/session/ -run 'Claim|Release' -count=1`
- `go test ./internal/acp/ -run 'RefusesA|RefusedStart|WatchResumeFinds' -count=1`
- `go test ./internal/acp/... ./internal/app/session/ -count=1`
- Mutation check: remove the service's claim. `TestAServiceRunRefusesAnACPTurn` must fail. Restore it. Then drop ACP's release. `TestTheWatchResumeFindsTheSlotFree` and the second half of `TestAnACPTurnRefusesAServiceRun` must fail. Restore it. (The release's place before `close(slot.done)` closes a race that no test can force reliably, so it is a stated rule, not a mutation check.)

## Task 2 — viewer connections

**Files:** `internal/acp/host.go`, a new `internal/acp/viewers.go`, `internal/acp/server.go`, `internal/acp/status.go`, `internal/acp/stack.go`, a new `internal/acp/viewers_test.go`.

1. Give `registerHandlers` the `notify` and `viewer` parameters (decision 2). Replace every `h.sink.Notify` inside it with `notify`. `serveConnWith` passes `h.sink.Notify` and false.
2. With `viewer` set:
   - register `session/new` through a wrapper that refuses a blob without `replaces` and passes the rest to `manager.Create` (decision 4);
   - register `session/load`, `session/resume` and `session/delete` to the refusal;
   - skip `addTurnCanceller`;
   - add `turns.Close()` to the returned cleanup, after the existing calls.
3. Add `MarshalOnly` to `Server` (decision 3). `initialize` reads it: a non-marshal client is answered with the refusal, and the marshal flag is not set. `dispatch` refuses any method other than `initialize` while `MarshalOnly` is set and the client is not yet marshal. Both refusals are `jsonRPCError` with invalid request, so the sentence travels.
4. Add `TurnManager.Close` (decision 5): collect every session ID with a projector under `stacksMu`, then `dropStacks` each one outside it, as `dropStacks` takes the lock itself.
5. In `viewers.go`, add `serveViewer`. It builds a `Server` over the connection with `MaxLineBytes` set to `viewerMaxFrameBytes`, `MarshalOnly` set and the host's logger. Then it registers the handlers with the server's own `Notify` and true, defers the cleanup and the alive flag's reset, and serves. It does not touch the sink.
6. Tests in `viewers_test.go`. Build an `agentHost` with `testRunConfig`. Adopt a bare runtime and its service pair through `manager.adopt`, as `openEmbedding` does. Serve each viewer on its own `rpc.Pipe` through `serveViewer`, and dial each with `rpc.Dial`.
   - `TestAViewerRefusesAClientWithoutTheCapability`: a raw `initialize` without the marshal capability is refused with the sentence, and a following `session/status` is refused the same way.
   - `TestAViewerCannotStartOrLoadASession`: `session/new` without `replaces`, `session/load`, `session/resume` and `session/delete` are refused, and the manager's sessions are unchanged.
   - `TestTwoViewersEachSeeEveryUpdate` (W2): A and B attach to one session. A sets a notice, and both mirrors show it within a second. B adds a note, and A's transcript read shows it.
   - `TestAViewersUpdatesNeverReachTheSink`: the host's sink is set to a recorder, as the stdio connection would set it. A viewer's notice reaches the viewer, and the recorder sees no `session_status` frame for it.
   - `TestOnlyOneViewersAnswerWins` (W2): with an approval open on the state (`registerTestApproval`), A and B both answer it. Exactly one answer succeeds, and the other fails with `CodeRequestSettled`.
   - `TestAViewersSheetIsItsOwn` (W2): A reads the sheet, B does not, and a change that moves the sheet sends A a sheet update and B none. Read `sheet.go` for the update kind first.
   - `TestAViewerLeavingLeavesTheOthersServed`: after A hangs up, B still gets status updates.
   - `TestAViewerStaysOutOfTheCancellerChain`: serving two viewers adds nothing to the host's chain.
   - `TestCloseStopsTheStackLoops`: a turn manager whose `Stack` read started a session's idle loop drops it on `Close`. Its stacks map is empty, and a fence for the session returns at once.

**Verify:**
- `go test ./internal/acp/ -run 'Viewer|Viewers|CloseStops' -count=1`
- `go test ./internal/acp/... -count=1` (the known `*Budget` load flakes aside; rerun one alone before calling it a regression)
- Mutation check: pass `h.sink.Notify` to a viewer's managers. `TestAViewersUpdatesNeverReachTheSink` and `TestTwoViewersEachSeeEveryUpdate` must fail. Restore it.

## Task 3 — `marshal acp --viewers`

**Files:** `internal/acp/listen.go`, `internal/acp/viewers.go`, `internal/acp/viewers_test.go`, `cmd/marshal/main.go`, `cmd/marshal/main_test.go`.

1. Move `ListenAndServe`'s socket preparation into `listenUnix` (decision 7) and call it from there. Nothing `ListenAndServe` does changes: `TestListenAndServeAcceptsSuccessiveConnections` and `TestListenAndServeCutsIdleConnections` pass unchanged.
2. Add `RunWithViewers`. It builds the same trust resolver and project-host registry `Run` does, binds the socket with `listenUnix`, and calls `runWithViewersConfig`. Then it closes the hosts, as `Run` does.
3. `runWithViewersConfig` builds the host and starts the accept loop on its own goroutine, with one goroutine per viewer connection under a wait group. Then it serves stdio through `serveConn`. When stdio's serve returns, or the context ends, it:
   1. closes the listener;
   2. cancels the viewers' context and waits for them, bounded by the host's shutdown;
   3. closes the host's sessions;
   4. closes the lister.

   It joins the errors, as `runWithConfig` does. An accept error other than a closed listener ends the loop and is logged; it does not end stdio.
4. In `cmd/marshal`, add `acpViewersSpec` beside `acpListenSpec`, and the hook `acpViewersRunner`. The dispatch:
   - refuses `--viewers` together with `--listen`;
   - parses the viewers spec with `ParseListenAddr`, and refuses any network but `unix`;
   - calls `acpViewersRunner` with stdio and the path.
5. Tests:
   - `TestViewersAreServedAtOnce`: over a real socket in a short directory, a stdio pipe stays connected while two viewer connections initialize concurrently, each with the marshal capability. The second does not wait for the first to hang up, as it would on `--listen`.
   - `TestTheViewerSocketIsTheOwnersOnly`: the socket is mode `0600` in a `0700` directory, and a stale socket file at the path is replaced.
   - `TestStdioEndingClosesTheViewers`: closing stdio closes every viewer connection, removes the socket, and returns once the sessions are closed.
   - `TestViewerMaxFrameBytesIsTheClientsCap`: `viewerMaxFrameBytes` equals `rpc.MaxFrameBytes`.
   - `TestACPViewersFlagRoutesToTheViewersRunner`, in `cmd/marshal`, beside `TestACPListenFlagRoutesToListener`: `acp --viewers unix:///x/v.sock` reaches the hook with `/x/v.sock`. `--viewers=unix://…` works too. `--viewers tcp://…`, `--viewers` without a value, and `--viewers` with `--listen` are each refused.

**Verify:**
- `go test ./internal/acp/ -run 'Viewers|ViewerSocket|StdioEnding|ViewerMaxFrame|Listen' -count=1`
- `go test ./cmd/marshal/ -count=1`
- `CGO_ENABLED=1 go build ./cmd/marshal`, then a manual check from the worktree:
  1. start `./marshal acp --viewers unix:///private/tmp/p4-3-suite/v.sock` with its stdin held open (for example by a FIFO);
  2. send `initialize` with the marshal capability on two `nc -U` connections at once, and see both answered;
  3. close stdin, and see the socket file go.

  Record the transcript in the Implementation record.

## Task 4 — the record and full verification

**Files:** `docs/daemon/p4-local-daemon.md` (the "Plan 3" placeholder only).

1. Replace the placeholder under "### Plan 3 — concurrent viewer connections" with the record:
   - what was built;
   - every place the plan was short, and how it was filled;
   - the manual run's transcript;
   - the verification results, each with its number;
   - an "Open for plan 4" line if the child's contract differs in any way from D2 as written.

   Touch nothing else in the spec: plan 4 fills its own placeholder.
2. Full verification, in the worktree:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean;
   - `go test ./... -timeout 20m` passes apart from the known load flakes, each rerun alone;
   - `TestWebIsStdlibOnly` and `TestTUIStaysOnTheClientSeam` pass;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then `go test ./test/usability/... -run TestScripted -timeout 5m` passes.

**Verify:** the commands above, each recorded with its result.

## Done when

- `marshal acp --viewers unix://<path>` serves stdio and any number of marshal viewer connections at once, and each viewer's updates reach that viewer only.
- A viewer refuses a client without the marshal capability and every lifecycle call but `session/new` with `replaces`.
- A session runs one run at a time across the service's path and ACP's path, and each path refuses the other's second run with its own existing error.
- Tasks 1 to 3's tests pass, their mutation checks fail as stated, and Task 4's suite passes.
