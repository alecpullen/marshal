# P3 plan 3b: Status — implementation plan

Date: 2026-11-07. Status: ready for review.
Base: `main` at `6fecfe18` (P3 plans 2 and 6a merged). Every anchor below was checked there. The full suite passed there: 119 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-next-suite`, `-timeout 20m`; `web/bridge` 87s).
- **It runs alongside P3 plan 4c** ([the projection's cost](2026-11-07-p3-4c-projection-memo-plan.md)). Plan 4c changes `internal/viewmodel`, `internal/acp/stack.go`, `internal/acp/service/stack.go` and four `main_test.go` files. This plan changes none of them. Both add tests to `internal/acp`, in different files, and each adds its own Implementation record entry to `docs/daemon/p3-acp-transport.md`.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, R2's note on host calls, R3's "How the status arrives", the "Methods P3 adds" headings (the sheet moved to plan 4b), and Migration order items 3 and 4. Task 5 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (the service behind the handlers), R2 (read your writes), R3 (the mirror), R9 (compatibility), R10 (budgets), Migration order item 3b, and tests V1, V3, V6 and V8.
Previous plans: [P3 plan 3a, ordering](2026-11-06-p3-3a-ordering-plan.md), [P3 plan 2, layering](2026-11-07-p3-2-layering-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client reads a session's status over ACP, from a mirror the agent keeps current, and sets the session's notice.
- Each ACP session gets the client seam's service, built with `(*app.Runtime).ServiceDeps`. These are the first ACP handlers that call it.
- `session/status` returns the status. For a marshal client it also starts a forwarder that sends `session_status` whenever the status changes, at most every 50ms, and before any call's response.
- `session/notice_set`, `session/notice_clear` and `session/notice_dismiss` call the service.
- `rpc.Session` mirrors the status and fires `Changed`.
- The conformance harness starts: one table of status cases runs against the service directly and against the client over a pipe.

**Non-goals:**
- **No sheet.** `session/sheet` (`Sheet`, `RefreshSheet`) moved to plan 4b. A subagent's sheet needs the subscribe-and-release rule that plan 4b gives a subagent's stack (decision 5). Until then, an ACP session's `ChangedFiles` stays 0, because nothing over the wire refreshes the sheet cache that counts them.
- **No new session over the wire.** The client attaches to a session the agent already holds (`Host.Attach`). `Host.NewSession` and `Session.Close` over the wire are plan 6b's.
- **No change to `internal/app/tui`, `internal/viewmodel`, or the stack code** (`internal/acp/stack.go`, `internal/acp/service/stack.go`). Plan 4c owns those.
- **No change to an existing ACP method or update.** Only the four new methods and the one new update kind.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-3b-suite`. Create the directory first: `mkdir -p /private/tmp/p3-3b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 4c uses `/private/tmp/p3-4c-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 5 likelier. Rerun a failure alone before treating it as new.
- **The service** (`internal/acp/service`) is `inproc`'s logic, moved by plan 2. `New(d Deps) (*Host, *Session)` (`service.go:290`) builds a session over `d.State`, and `newSession` (`events.go`) subscribes it to the runtime's brokers.
  - `Status()` (`service.go:344`) builds the status from the state on every call, one field at a time. It reads `gitinfo.Read` (two small file reads) and nothing slower.
  - `SetNotice`, `ClearNotice` and `DismissNotice` (`notes.go:44`, `:54`, `:64`) set the state's notice. They fire no `Changed`, and `State.SetNotice` (`session/notice.go:53`) publishes no event.
  - The unexported `close()` (`events.go:217`) marks the session closed, discards staged plugin scans, cancels its subscriptions and closes `Changed`. It leaves the state alone. The exported `Close` (`ending.go:103`) also settles pending requests and shuts the state down, which only the runtime's own close may do.
- **`(*app.Runtime).ServiceDeps(ctx)`** (`internal/app/servicedeps.go:26`) builds the service's `Deps` from a runtime. It works on a runtime literal that has only a `State`: every broker and database read goes through `must`, which returns the zero value for nil. It needs a non-nil `State`.
- **The ACP session manager** (`internal/acp/session.go`) maps session IDs to `*app.Runtime` (`sessions`, `:93`). Every path that tears a runtime down goes through `closeRuntimeOnce` (`:192`), which runs the closer once per runtime pointer (`once.Do` at `:208`): close, close-all, replacement on load, resume and delete, and a failed replay.
- **Handlers and managers.** `registerHandlers` (`host.go:313`) builds one manager per concern per connection, each resolving a session through a `Lookup` func over the session manager. The `initialize` handler is inline at `:319`. `WorkManagerACP` (`watch.go:298`) is the model for a per-connection forwarder: `ensureWorkForwarder` (`:501`) starts one per session on the first read, registers its fence before the goroutine starts, and `runWorkForwarder` (`:535`) serves the fence and removes and closes it on the way out. `works.Close()` runs in the connection's cleanup (`:615`).
- **The barrier.** `responseBarrier(turns, works, watches)` (`host.go:289`) is installed at `:457`. It runs only for a marshal client's call whose params carry a `sessionId` (plan 3a). `ordering_test.go:153` calls it too.
- **The unknown-session error.** Handlers answer a session the manager does not hold with `serverErrorf("unknown session: %s", id)` (`hold.go:27`, `interaction.go:197`), and a missing `sessionId` with `invalidParamsError`.
- **The client.** `rpc.Dial` (`dial.go:30`) builds the connection with the caller's `Handlers` (`:31`) and returns a `Host` holding only the connection, the agent's info and `SharedFilesystem`. `rpc.Stacks` (`stacks.go`) is the model for a mirror fed by `session/update`: it decodes the envelope with `acpwire.SessionUpdate`, peeks at `kind`, and signals a buffered `Changed`.
- **Tests that pipe a real server.** `startTestAgent` (`client_core_test.go:25`) serves handlers on a pipe; `startAgentHost` (`:342`) serves the real host. `ordering_test.go` slows every `session/update` frame to prove ordering; `largeStackSession` (`stack_wire_test.go:333`) builds the 2,000-call session R10's budgets use. Budget tests skip under `-short` and under the race detector (`raceEnabled`).
- **The method table.** `acpclient.Methods` (`methods.go`) marks `Status` (`:42`), `SetNotice` (`:91`), `ClearNotice` (`:92`) and `DismissNotice` (`:93`) new in P3. Their docs end `ACP: … (new in P3)` (`acpclient.go:196`, `:564`, `:570`, `:575`). `TestACPClientMethodTableMatchesTheServer` (`acpclient_methods_test.go:20`) fails when a row marked new is registered, and `TestMethodDocsNameTheirACPMethod` keeps each doc line in step with its row.
- **The service tests that become the table.** `TestStatusOfAFreshState` (`service_test.go:65`), `TestStatusMirrorsTheState` (`:89`), `TestNoticeCallsChangeTheBanner` (`notes_test.go:80`) and `TestStatusCarriesTheRuntimesNotice` (`:139`). The state helper is `newState` (`service_test.go:23`). `TestStatusMirrorsTheState` builds the session, then sets every field, then reads `Status()` once; it calls nothing else on the session.

**Decisions this plan makes beyond the spec** (R2, R3 and the Migration order record them):

1. **The session manager owns each ACP session's service.** It builds it on first use with `rt.ServiceDeps`, keyed by the runtime pointer, and releases it inside `closeRuntimeOnce`, before the runtime closes. Sessions outlive connections, so their services do too. Building on first use means a client that never asks for status pays nothing. Keying by runtime means a session replaced on load or resume gets a fresh service.
2. **The forwarder polls.** Every 50ms, and whenever its fence is asked, it reads the status, encodes it, and sends it only when the encoding differs from the last one it sent. Most status fields change without any broker event (usage, activity, the notice), so polling is what keeps the mirror current. It never reads the service's `Changed`: that channel has one buffer, and a second reader would steal its signal.
3. **The first value arrives as an update, before the response.** `session/status` starts the forwarder before it returns, and the barrier then fences the forwarder. The forwarder has sent nothing yet, so it sends the current status before the response is written. Every update for a session on a connection comes from that one goroutine, in order, so no revision number is needed. The response still carries the status, for clients without the marshal capability. A marshal client uses the result only if no update arrived first, which happens only when the barrier timed out.
4. **The client attaches.** `Host.Attach` binds a session the agent already holds. Plan 8's embedded mode attaches to the session `launch` starts, and `session/new` over the wire is plan 6b's.
5. **The sheet moves to plan 4b.** A subagent's sheet needs what plan 4b decides for a subagent's stack: a first read that subscribes, and a release when the TUI leaves the drill-in.
6. **Host calls still flush nothing.** No host call changes a session's status yet. Plan 7's config calls are the first that can, and plan 7 decides how they flush.
7. **`Changed` stays out of the table.** The service fires it on every broker event. The client fires it after it applies an update, and the agent sends only changes. Both meet the interface's promise ("fires after anything Status returns may have changed"), but not in the same moments. Each arm keeps its own tests of it.
8. **The harness lives in `internal/acp`'s tests**, with two arms: `service` (the service itself, which `inproc` aliases) and `pipe` (the client over a pipe to a server whose status handlers call a service built from the same `Deps`). Its interface, `seam`, holds the methods both arms implement. Each group's plan adds its methods to it, and plan 8 replaces it with `acpclient.Session`.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 4c's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 3b and 4c`. Plan 4c gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-3b-status` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-3b-status: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/status.go` and `notes.go`)

```go
// The status methods and update kind (P3 R3).
const (
	MethodStatus        = "session/status"
	MethodNoticeSet     = "session/notice_set"
	MethodNoticeClear   = "session/notice_clear"
	MethodNoticeDismiss = "session/notice_dismiss"
	UpdateSessionStatus = "session_status"
)

// StatusParams is session/status's and session/notice_dismiss's params.
type StatusParams struct {
	SessionID string `json:"sessionId"`
}

// StatusUpdate is a session_status update: the session's whole status,
// sent whenever it differs from the last one sent on the connection.
type StatusUpdate struct {
	Kind   string        `json:"kind"`
	Status SessionStatus `json:"status"`
}
```

In `notes.go`, beside `Notice`:

```go
// NoticeSetParams is session/notice_set's params.
type NoticeSetParams struct {
	SessionID string `json:"sessionId"`
	Notice    Notice `json:"notice"`
}

// NoticeClearParams is session/notice_clear's params.
type NoticeClearParams struct {
	SessionID string `json:"sessionId"`
	Category  string `json:"category"`
}
```

`session/status` returns a `SessionStatus`. The three notice methods return `{}`.

### The service (`internal/acp/service/events.go`)

```go
// Release marks the session closed and releases its event subscriptions
// and Changed, leaving the state to the runtime that owns it. The ACP host
// releases a session's service before it closes the runtime. Releasing
// twice does nothing.
func (s *Session) Release()
```

### The session manager (`internal/acp/session.go`)

```go
// Service returns the client-seam service of the session's current
// runtime, building it with the runtime's ServiceDeps on first use. It
// reports false for an unknown session, a runtime with no state, and a
// runtime that is closing.
func (m *SessionManager) Service(id string) (*service.Session, bool)
```

### The status handlers (`internal/acp/status.go`, new)

```go
// statusInterval is how often a forwarder compares the session's status
// with the last one it sent (P3 R3).
const statusInterval = 50 * time.Millisecond

// StatusManagerConfig wires one connection's status handlers.
type StatusManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends session_status updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the
	// marshal capability. Nil means it did not.
	Marshal func() bool
}

// StatusManager serves session/status and the notice calls for one
// connection, and runs one status forwarder per session a marshal client
// reads.
type StatusManager struct{ /* unexported */ }

func NewStatusManager(cfg StatusManagerConfig) *StatusManager

func (m *StatusManager) Status(ctx context.Context, params json.RawMessage) (any, error)
func (m *StatusManager) NoticeSet(ctx context.Context, params json.RawMessage) (any, error)
func (m *StatusManager) NoticeClear(ctx context.Context, params json.RawMessage) (any, error)
func (m *StatusManager) NoticeDismiss(ctx context.Context, params json.RawMessage) (any, error)

// Close stops every forwarder. The connection's cleanup calls it.
func (m *StatusManager) Close()

// fenceSession waits until the session's status forwarder has sent the
// status as it is now, if it differs from the last one sent (P3 R2).
func (m *StatusManager) fenceSession(ctx context.Context, sessionID string)
```

### Host wiring (`internal/acp/host.go`)

```go
// registerInitialize registers the initialize handler: registerHandlers'
// own, moved out unchanged so a test can serve the real handshake.
func registerInitialize(srv *Server)

// registerStatus registers the four status methods on srv.
func registerStatus(srv *Server, statuses *StatusManager)

// responseBarrier gains the status manager, fenced last.
func responseBarrier(turns *TurnManager, works *WorkManagerACP, watches *WatchManagerACP, statuses *StatusManager) func(ctx context.Context, sessionID string)
```

### The client (`internal/acpclient/rpc/session.go`, new)

```go
// Attach binds a session the agent already holds. It sends session/status,
// which subscribes this connection to the session's status, and returns
// once the mirror holds a value. Attaching a session that is already
// attached returns the same Session and sends nothing.
func (h *Host) Attach(ctx context.Context, sessionID string) (*Session, error)

// Session is one agent session seen through the connection: a mirror of
// what the agent pushes, and calls that send their ACP methods. Each P3
// group plan adds its methods.
type Session struct{ /* unexported */ }

func (s *Session) ID() string
func (s *Session) Status() acpwire.SessionStatus
func (s *Session) Changed() <-chan struct{}
func (s *Session) SetNotice(ctx context.Context, notice acpwire.Notice) error
func (s *Session) ClearNotice(ctx context.Context, category string) error
func (s *Session) DismissNotice(ctx context.Context) error
```

### The harness (`internal/acp/conformance_test.go`, new)

```go
// seam is the part of acpclient.Session that both arms implement (spec
// V1). Each group's plan adds its methods; plan 8 replaces it with
// acpclient.Session.
type seam interface {
	ID() string
	Status() acpwire.SessionStatus
	SetNotice(ctx context.Context, notice acpwire.Notice) error
	ClearNotice(ctx context.Context, category string) error
	DismissNotice(ctx context.Context) error
}

// seamArm opens one implementation of the seam over d.
type seamArm struct {
	name string
	open func(t *testing.T, d service.Deps) seam
}

func seamArms() []seamArm

func TestStatusConformance(t *testing.T)
```

## Task 1: Each ACP session's service

**Goal:** the session manager hands out each session's service, built from its runtime, and releases it before the runtime closes.

**Files:** `internal/acp/service/events.go`, `internal/acp/service/events_test.go`, `internal/acp/session.go`, `internal/acp/session_test.go`.

**Steps:**

1. **`Release`.** Add it to `events.go` as in Contracts. It does exactly what `close()` does: it may call it. Test it in `events_test.go` (`TestReleaseLeavesTheStateRunning`): after `Release`, `Changed` is closed, `SetNotice` returns the session-closed code, the state has not been shut down (a message added to the state afterwards appears in its transcript), and a second `Release` does not panic.
2. **The registry.** In `SessionManager`, add a services map keyed by `*app.Runtime`, guarded by its own mutex (not `mu`, not `lifecycleMu`, not `closeMu`).
3. **`Service`.**
   - Look the runtime up as `Get` does. Report false for an unknown ID or a nil `State`.
   - Under the services mutex: return the runtime's entry if there is one. Otherwise refuse a runtime that is closing: one that already has an entry in `closeOnce` (read under `closeMu`). Otherwise build `service.New(rt.ServiceDeps(context.Background()))`, keep its session, store it, and return it.
   - Lock order: the services mutex, then `closeMu`. `closeRuntimeOnce` never holds `closeMu` while it takes the services mutex, so the two cannot deadlock.
4. **Release before close.** Inside `closeRuntimeOnce`'s `once.Do`, before `m.close`, take the services mutex, remove the runtime's entry and `Release` it. A runtime whose service was never built releases nothing. Because `Service` checks `closeOnce` under the services mutex, a build that races a close is either refused or released.
5. **Tests in `session_test.go`.** Use runtime literals with a `State` (`session.New(config.Default(), …)`), installed in the manager's map as `host_test.go:275` does, a no-op turn canceller (`SetTurnCanceller`, which `Close` requires), and a `CloseRuntime` that records its calls:
   - `TestServiceIsBuiltOncePerRuntime`: two calls return the same session, whose `ID()` is the state's.
   - `TestServiceIsReleasedBeforeTheRuntimeCloses`: after `Close(ctx, id)`, the service's `Changed` is closed, and the recording closer ran after the release (the closer checks that the channel is already closed).
   - `TestAReplacedRuntimeGetsAFreshService`: `publishReplacement` with a new runtime for the same ID; `Service` returns a different session, and the old one is released.
   - `TestServiceRefusesUnknownAndStatelessRuntimes`: an unknown ID, and a runtime literal with no `State`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/service/ ./internal/acp/ -run 'TestRelease|TestService|TestAReplacedRuntime' -count=1 -v 2>&1 | tail -30
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/service/ ./internal/acp/ -count=1
```

Expected: the new tests pass, and both packages pass whole.

Mutation check: delete the release from `closeRuntimeOnce`. `TestServiceIsReleasedBeforeTheRuntimeCloses` and `TestAReplacedRuntimeGetsAFreshService` fail. Restore it.

## Task 2: The status handlers and the forwarder

**Goal:** `acp.Server` serves `session/status` and the three notice calls. For a marshal client, a forwarder keeps the client's status current and answers the barrier.

**Files:** `internal/acpwire/status.go`, `internal/acpwire/notes.go`, `internal/acpwire/status_test.go` (new), `internal/acp/status.go` (new), `internal/acp/status_test.go` (new), `internal/acp/host.go`, `internal/acp/ordering_test.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **Wire types.** Add the constants and types in Contracts. Add `TestStatusUpdateRoundTrips` to `status_test.go`: a `StatusUpdate` carrying a status with a notice, a turn route and a quota encodes and decodes to an equal value, and its `kind` is `session_status`.
2. **Handlers.** In `status.go`:
   - Each handler decodes its params, refuses an empty `sessionId` with `invalidParamsError`, and resolves the service with `Lookup`. An unknown session is `serverErrorf("unknown session: %s", id)`.
   - `Status` returns the service's `Status()`. When `Marshal` reports true, it first makes sure the session has a forwarder (step 3).
   - `NoticeSet`, `NoticeClear` and `NoticeDismiss` call the service's method and return an empty object. An error from the service is returned as it is, so its code travels as error data.
3. **The forwarder.** One per session per connection, started by the first `Status` from a marshal client, as `ensureWorkForwarder` starts a work forwarder: it registers its fence before its goroutine starts, and a second start for the same session does nothing.
   - It holds the encoding of the last status it sent, empty at first.
   - Its loop wakes on a `statusInterval` ticker and on its fence. Each time, it resolves the service through `Lookup`, so a runtime replaced on load or resume is followed. If the session is gone, it answers any fence request first, then stops.
   - Otherwise it reads `Status()`, encodes it, and, when the encoding differs from the last one sent, sends `session/update` with `SessionUpdateParams{SessionID, Update: acpwire.StatusUpdate{Kind: acpwire.UpdateSessionStatus, Status: …}}` and keeps the encoding. It never sends under the manager's mutex.
   - On the way out it removes and closes its fence, as `runWorkForwarder` does. `Close` cancels every forwarder and makes later starts do nothing.
4. **Host wiring.**
   - Move the inline `initialize` handler (`host.go:319`) into `registerInitialize`, unchanged, and call it where it was.
   - Build the `StatusManager` in `registerHandlers` after the work manager: `Lookup` is `manager.Service`, `Notify` is `h.sink.Notify`, and `Marshal` is `srv.marshalClient`. Register its methods with `registerStatus`.
   - Give `responseBarrier` the status manager and fence it last, after the watch manager. A nil manager is skipped, as the others are. Update `ordering_test.go:153` to pass nil.
   - Call `statuses.Close()` in the connection's cleanup, beside `works.Close()`.
5. **The method table.** Mark `Status`, `SetNotice`, `ClearNotice` and `DismissNotice` existing in `methods.go`, and change their four doc lines in `acpclient.go` from `(new in P3)` to `(exists)`, keeping the rest of each line. `Sheet` and `RefreshSheet` stay new.
6. **Tests in `status_test.go`.** Serve a `StatusManager` on a pipe with `startTestAgent`, over a service built with `service.New` from a test state (`session.New(config.Default(), t.TempDir(), …, session.Persistence{SessionID: "s1"})`), with `registerInitialize`, `registerStatus` and `responseBarrier(nil, nil, nil, statuses)`. The client is a raw `rpc.Conn` whose `Notify` records each update; after `initialize` with the marshal capability, the connection is a marshal client.
   - `TestTheFirstStatusArrivesBeforeTheResponse`: the first `session/status` call returns after exactly one `session_status` for `s1` was recorded, and that update's status equals the result.
   - `TestAStatusChangeIsSentWithinTheInterval`: set a notice on the state directly; a `session_status` carrying it arrives within 500ms. Then wait three intervals with nothing changing: no further update arrives.
   - `TestANoticeCallReturnsAfterItsStatus` (V3): with every `session/update` frame slowed by 50ms, as `ordering_test.go` slows them, `session/notice_set` returns only after the recorded updates include one carrying the notice. The same holds for `notice_clear` and `notice_dismiss`.
   - `TestAClientWithoutTheCapabilityGetsNoStatusUpdates` (V6): `initialize` without the capability; `session/status` still returns the status, a notice call still works, and no `session_status` is recorded within 200ms.
   - `TestTheForwarderStopsWhenTheSessionGoes`: make `Lookup` report false; within 500ms the forwarder has stopped and its fence is gone (a barrier for `s1` returns at once).
   - `TestStatusRefusesBadParams`: a missing `sessionId` is invalid params; an unknown session is the unknown-session error.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/ -run 'Status|Notice|Forwarder|ACPClientMethodTable|Barrier|AResponseFollows' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/ -run 'Status|Notice|Forwarder' -race -count=3
```

Expected: the new tests pass, three times under `-race`; `TestACPClientMethodTableMatchesTheServer` and `TestMethodDocsNameTheirACPMethod` pass with the four rows marked existing; plan 3a's ordering tests still pass.

Mutation checks:
- Drop the status manager from `responseBarrier`. `TestANoticeCallReturnsAfterItsStatus` and `TestTheFirstStatusArrivesBeforeTheResponse` fail. Restore it.
- Make the forwarder send on every tick, changed or not. The quiet half of `TestAStatusChangeIsSentWithinTheInterval` fails. Restore it.

## Task 3: The client's session

**Goal:** `rpc.Host.Attach` returns a session whose `Status` reads a mirror the agent's updates keep current.

**Files:** `internal/acpclient/rpc/session.go` (new), `internal/acpclient/rpc/dial.go`, `internal/acpclient/rpc/session_test.go` (new).

**Steps:**

1. **Routing.** `Dial` builds the host first, then the connection with handlers of its own: notifications go to the host's router, and agent requests go to the caller's `Request` unchanged. The router applies a `session_status` update to the attached session it names, and drops one for a session that is not attached. Then it passes every notification, `session_status` included, to the caller's `Notify`, so a caller (and `rpc.Stacks`) sees exactly what it saw before. Decode as `Stacks.Notify` does.
2. **`Attach`.**
   - An attached ID returns its session without a call.
   - Otherwise create the session and register it before sending, so an update that arrives during the call is applied. Send `session/status`.
   - On error, unregister it and return the error.
   - On success, if no update was applied during the call, install the result (the barrier timed out on the agent). Return the session.
3. **The mirror.** `Status` returns the held value without I/O. Applying an update replaces the value and fires `Changed`, a channel with a buffer of one that never blocks the read goroutine. When the connection ends, the host closes every attached session's `Changed`: `Dial` starts one goroutine that waits on the connection's `Done`.
4. **The calls.** `SetNotice`, `ClearNotice` and `DismissNotice` send their methods with the session's ID and return the error. A coded error comes back as `*acpwire.Error`, as plan 1's connection already maps it.
5. **Tests in `session_test.go`**, over a scripted agent on `Pipe`: the agent's stream answers `initialize` with the marshal capability, as `startFakeAgent` (`dial_test.go:52`) and `marshalAnswer` (`:97`) do, and the test writes frames to it and reads the client's, as the `fake` in `conn_test.go:19` and the frame helpers in `stacks_test.go` (`updateFrame`, `resultFrame`) do:
   - `TestAttachInstallsTheUpdateThatPrecedesTheResponse`: the agent sends a `session_status` (status A), then answers `session/status` with status B. After `Attach`, `Status()` is A.
   - `TestAttachInstallsTheResultWhenNoUpdateCame`: the agent answers with B and sends nothing else. `Status()` is B.
   - `TestAnUpdateReplacesTheStatusAndFiresChanged`: after `Attach`, an update with C; `Changed` fires and `Status()` is C.
   - `TestAnUpdateForAnotherSessionIsDropped`.
   - `TestTheCallersNotifySeesEveryNotification`: a `session_status` and an unrelated notification both reach the `Notify` passed to `Dial`.
   - `TestAttachingTwiceSendsOneCall`.
   - `TestChangedClosesWhenTheConnectionEnds`.
   - `TestNoticeCallsSendTheirMethods`: each call sends its method with the session's ID and the right params, and a coded error from the agent comes back with `acpclient.IsCode` true.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included (the client still imports only what `acpclient` may), three times under `-race`.

Mutation check: make the router skip `session_status` updates. `TestAttachInstallsTheUpdateThatPrecedesTheResponse` and `TestAnUpdateReplacesTheStatusAndFiresChanged` fail. Restore it.

## Task 4: The conformance harness

**Goal:** one table of status cases runs against the service and against the client over a pipe, and gives the same answers (V1).

**Files:** `internal/acp/conformance_test.go` (new), `internal/acp/service/service_test.go`, `internal/acp/service/notes_test.go`.

**Steps:**

1. **The arms.**
   - `service`: `service.New(d)`'s session.
   - `pipe`: a server on `rpc.Pipe` with `registerInitialize`, a `StatusManager` whose `Lookup` returns a session built with `service.New(d)` for the state's ID, `registerStatus`, and `responseBarrier(nil, nil, nil, statuses)`. The client is `rpc.Dial` (which declares the capability) and `Host.Attach` on the state's ID. Both ends close with the test, as `startTestAgent` closes them.
2. **The table.** `TestStatusConformance` runs every case once per arm, as `t.Run(arm.name+"/"+case.name, …)`. Each case receives the arm's `open`. Move these four tests from the service's tests into it, deleting them there:
   - "a fresh state" (`TestStatusOfAFreshState`);
   - "the status mirrors the state" (`TestStatusMirrorsTheState`). Set every field on the state before `open`, not after: the pipe arm's first value is read when it attaches, and a change after that arrives only with the next tick. Its expected value stays exactly as it is;
   - "the notice calls change the banner" (`TestNoticeCallsChangeTheBanner`): the calls go through the seam, so the barrier makes each read after a call current;
   - "the runtime's notice reaches the status" (`TestStatusCarriesTheRuntimesNotice`): set the notice before `open`.

   Keep each case's assertions as they are. Copy the `newState` helper into the harness; the service's tests keep theirs.
3. **What stays service-only.** `Changed`, a closed session, a swap and the run-error classifier stay in the service's tests. The client has no `Close` until plan 6b, and decision 7 keeps `Changed` per arm.
4. **A difference is a bug, never the case's.** If the pipe arm fails a case the service arm passes, a value does not survive the wire: an empty list encoding as absent, a time losing precision, a field without a JSON tag. Fix it in the wire type or in the service so both arms agree. If the fix would change what the TUI sees today, stop and report it instead.
5. **The budget (R10).** `TestNoticeCallBudget`: the pipe arm over `largeStackSession`, 30 `SetNotice` calls alternating between two notices, so each changes the status. Each call includes its barrier and its update. The median must be under 1ms. Log the median with `t.Logf` for the Implementation record. Skip under `-short` and with the race detector, as the 4a budget tests do. Add `BenchmarkNoticeCall` beside it.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/ -run 'TestStatusConformance|TestNoticeCallBudget' -count=1 -v 2>&1 | tail -30
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-3b-suite go test ./internal/acp/ -run 'TestStatusConformance' -race -count=3
```

Expected: eight subtests pass (four cases, two arms), three times under `-race`; the budget test passes and logs its median; the service package passes without the four moved tests.

Mutation check: make `Attach` ignore updates, so the mirror keeps only its first value. The pipe arm of "the notice calls change the banner" fails and the service arm passes. Restore it.

## Task 5: The docs and full verification

**Goal:** the spec records what plan 3b built and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`.

**Steps:**

1. **The counts.** Re-measure the P3 spec's "Where the code is today" counts as plan 2 did: the interface methods and their ACP rows from `acpclient.Methods`, and the methods `acp.Server` registers. Expect 110 rows, with 39 of them an existing ACP method (four more than before), 68 new in P3, and 3 carrying none; and 75 registered methods (71 plus the four). Record the result in the Implementation record, and update the section's numbers.
2. **The Implementation record.** Add a "Plan 3b — status" entry after plan 2's. It names: the session manager's `Service` and the release in `closeRuntimeOnce`; `Release`; the status manager, its forwarder and its interval; the first value arriving before the response; the barrier's fourth fence; `registerInitialize`; `rpc.Session` and `Attach`; the harness, its arms and the four moved cases; the budget's median; and what is left: the sheet (plan 4b), `Changed` for the other update kinds (each mirror's plan), and closing a session over the wire (plan 6b). Plan 4c adds its entry at the same place. If both branches have merged, keep both entries, in merge order.
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "status" in its list.
4. **Main may have moved.** If plan 4c merged while this branch ran, merge `main` into the branch before the full verification. Expect a conflict only in the P3 spec's Implementation record. Keep both entries.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-3b-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-3b-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, and now `TestNoticeCallBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Self-review

- **Spec coverage.** R1, the service behind the handlers: Task 1 and Task 2. R2, read your writes for the new update: Task 2's barrier fence and `TestANoticeCallReturnsAfterItsStatus`. R3, the status row (first value, kept current at most every 50ms and before any response): Tasks 2 and 3. R9 and V6, a client without the capability: `TestAClientWithoutTheCapabilityGetsNoStatusUpdates`. R10, a synchronous call under 1ms: `TestNoticeCallBudget`. V1, the harness: Task 4. V8: Task 5.
- **What could go wrong.**
  - **The first value races the response.** The handler's result is read before the barrier, and the update during it, so the update is never older. The client keeps the update and ignores the result unless nothing came.
  - **A service outlives its runtime.** It is released inside the runtime's one close, and `Service` refuses a closing runtime under the same mutex the release takes.
  - **Polling costs.** Each tick reads the status (two small file reads) and encodes a few kilobytes, for each session a marshal client attached: one session in embedded mode. Many sessions behind a bridge are P4's question.
  - **A value that does not survive the wire.** The conformance table compares exact values on both arms, so it shows up as a pipe-only failure (Task 4 step 4).
  - **Plan 4c.** It changes the stack code and the projection, which this plan does not touch. Both add tests to `internal/acp`, in different files.
