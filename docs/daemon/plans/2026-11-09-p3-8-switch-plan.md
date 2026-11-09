# P3 plan 8: The switch — implementation plan

Date: 2026-11-09. Status: ready for review.
Base: `main` at `bff50180` (P3 plans 6c and 7b merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p3-b8`, `-timeout 20m`): 119 packages ok, `web/bridge` included (169s).
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, "Methods P3 adds" (plan 8), R1 (how the pipe is served), R4 (every change reaches the mirror), R10 (the key press), R11, the Design table, Migration order item 8, V1, V2, V7 and a new Finding. Task 8 adds the Implementation record, re-measures the counts, and rewrites the "Where the code is today" bullets that describe `inproc`.
- **This is the plan that changes what runs.** Embedded `marshal` runs the TUI over ACP on an in-memory pipe from Task 6 on. The goal is still that nothing a user sees changes. The session log is the one exception: it gains one `acp dispatch` line per call the TUI makes (R11).

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): Goal, R1 (packages and layering, how the pipe is served), R2 (framing, read your writes), R3 (the mirror), R4 (the transcript, every change reaches the mirror), R7 (sessions), R10 (budgets), R11 (embedded mode), Migration order item 8, Tests V1 to V8, and Done when.
Previous plans: [P3 plan 6c, pipelines, the end flow and sessions](2026-11-09-p3-6c-ending-plan.md), [P3 plan 7b, notes, commands, browsers and project data](2026-11-09-p3-7b-browsers-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy. The measurements under Assumptions came from a scratch counter and a scratch benchmark, both discarded.

**Goal:** embedded `marshal` is an ACP client of its own agent, and `internal/acpclient/inproc` is gone.
- `launch` starts each runtime, serves it with `marshal acp`'s agent host on one end of an in-memory pipe, dials the other end, and runs the TUI over the client.
- Every change to the transcript reaches the client's mirror, as every refresh saw it in process.
- Every TUI test runs over the pipe, against a test agent whose reads catch up with a state the test changed directly.
- `inproc` is deleted, and only `launch` and the test agent dial.

**Non-goals:**
- **No change for a client without the capability.** It receives no new update kind and no new cadence (R9, V6). `transcript_changed` is a session event that no ACP update carries.
- **No change to the TUI's non-test code,** except where the wire cannot keep a distinction the TUI relied on (Task 5's first triage case). The TUI's helpers and tests change.
- **No change to the service's logic.** Its 40 error texts that start `inproc:` stay as they are: users see them, and tests pin some. Renaming them is a follow-up.
- **No bridge transport, reattach or remote daemon.** P4.
- **No changelog entry:** nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-8-suite`. Create the directory first: `mkdir -p /private/tmp/p3-8-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **Embedded mode today** (`internal/launch/launch.go`). `Run` (`:56`) builds an `app.Embedded`, and each pass of its loop calls `embedded.Start` (`:78`), builds the seam with `inproc.New(embedded.SessionDeps(ctx))` (`:86`), seeds the file index through the session (`:93`), builds the TUI options and runs the program (`:122`), then takes one of three branches: `embedded.Resume` (`:130`), `embedded.TakeTrustReload` (`:134`) or `embedded.Finish` (`:137`). `SessionDeps` (`app/embedded.go:226`) is the runtime's `ServiceDeps` plus the trust prompt and its decision. `Resume` and `TakeTrustReload` close the runtime (`:240`, `:250`); `Finish` (`:270`) quiesces, marks the session ended and closes it. `rt.Logger` is the runtime's logger, at info level (`runtime.go:1759`).
- **`marshal acp`'s agent host** (`internal/acp/host.go`). `newAgentHost` (`:99`) builds the `SessionManager` from a `runConfig`, whose `startRuntime` must not be nil, and registers the host's chained turn canceller. `registerHandlers` (`:359`) registers every handler for one connection, installs the barrier and returns a cleanup that closes the connection's managers (`:671`). `serveConn` (`:711`) builds the `Server` with the host's logger, registers the handlers and serves. `close` (`:727`) is `CloseAll`. `AgentVersion` (`:31`) is stamped by `cmd/marshal` (`main.go:81`).
- **The session manager** (`internal/acp/session.go`). `sessions` maps an ID to its runtime under `mu`, and `lifecycleMu` is taken first (`:83`). `services` holds each runtime's `servicePair` (`:128`), built on first use by `servicePair` (`:803`) from `rt.ServiceDeps`, which carries no trust prompt. `closeRuntimeOnce` (`:212`) releases a runtime's service and then calls the configured closer. `CloseAll` (`:742`) empties the map, cancels each session's turn and closes each runtime once. `replace` (`:486`) re-keys a runtime under the new session's ID and points its pair at the new service session. `SessionManager.Create` (`:410`) starts a runtime when no `replaces` is given.
- **The server** (`internal/acp/server.go`). `Server` (`:79`) has `MaxLineBytes` (`:137`, 1 MiB when unset). `dispatch` (`:880`) recovers a handler's panic and answers "internal error: handler panicked" (`:883`). `reportFatal` (`:717`) hands an error to `Serve`, which shuts down and returns it (`:320`).
- **The stack's freshness.** The idle loop (`ensureStackIdle`, `stack.go:473`) marks a session's main stack dirty on every session event, and on each tick flushes it only when it is dirty or the band's `StaleAt` passed (`:541`, `flushProjector` with `onlyDirty`); subagent stacks flush on every tick unless final (`flushChildStacks`, `:223`). A fence drains the buffered events and flushes the same way (`serveFence`, `:502`). The client's busy flag is `clientBusy`, set by `session/stack_busy` (`setClientBusy`, `:402`; `projectBusy`, `:415`). `TurnManager.isMarshal` is `turn.go:322`. `registerTranscript` is `sheet.go:348`.
- **What publishes nothing.** The projection reads the transcript, the steps, the todos, the workspace, the in-flight message and the active calls (`viewmodel.SnapshotOf`, via `ProjectState`, `viewmodel/project.go:32`). Eight state calls change one of them and publish no session event: `Rewind` (`messages.go:498`), `SwitchBranch` (`:538`), `ClearMessages` (`:578`), `LogThinking` (`streaming.go:60`), `SetTodos` (`session.go:3733`), `AddRunEvent` (`runevents.go:52`), `ClearRunEvents` (`runevents.go:72`) and `SetWorkspace` (`workspace.go:49`), which publishes only on its own workspace broker (`:91`). `AppendActiveToolCallOutput` (`pending.go:481`) is silent by design: its comment says streaming output would flood the broker. The startup loads (`loadFromDB`, `loadStepsLocked`, `restoreWorkspace`) are silent too, before any client reads. `publishEvent` (`session.go:1691`) takes `s.mu` to read the broker, so a call must publish after it releases `s.mu`. The event names are the constants at `session.go:40`. `eventToSessionUpdate` (`turn.go:397`) returns nothing for a type it has no case for (`:526`).
- **The TUI refreshes on its ticks.** `handleAgentTick` (`model.go:5994`) refreshes the viewport while busy; `handleSpinnerTick` (`:6032`) refreshes it every 80ms while busy or while `Status().SubagentRunning`. `refreshViewport` reads `m.sess.Stack(m.drilledSheetID(), m.busy)` (`viewport_render.go:99`). In process, each read projected the state afresh.
- **The TUI's tests.** 1,292 tests in 107 files in `internal/app/tui`, plus the subpackages `agents`, `connect`, `settings`, `skills` and `textfield`, build their sessions through `inproc`. The helpers are in `seam_test.go`: `testSession` (`:23`), `stateOf` (`:30`), `stateOfSession` (`:36`), `testModel` (`:52`), `testModelWith` (`:59`), `testModelCached` (`:66`), `runnerOf` (`:72`), `withPromptRunner` (`:78`), `testModelStub` (`:149`), `testModelStubWith` (`:154`) and `withCommands` (`:162`); and elsewhere `testModelRecording` (`answers_seam_test.go:80`), `testModelControlled` (`controls_seam_test.go:86`), `testModelEnding` and `withEndService` (`ending_seam_test.go:26`, `:35`), `testModelReplacing` (`newsession_seam_test.go:36`), `testModelTurns` (`turns_seam_test.go:102`), `withSuggester` (`model_test.go:1999`), `newTestModelDeps` (`:6689`) and `newTestModelInRepo` (`:6720`). Thirty-five direct `inproc.New` calls remain in the package's tests, and eleven in the subpackages'. `TestModelHostIsTheOneItWasBuiltWith` (`model_test.go:6902`) asserts the host is an `*inproc.Host`. Every call site of every helper already has a `t` (or a `b`) in scope. No TUI test attaches an event broker to a state. `keyPress` (`approval_test.go:19`) builds a key press. The TUI's `TestMain` sets `viewmodel.CheckWireMemo` (`main_test.go:11`).
- **What the TUI suite reads.** At the base, one run of `internal/app/tui` made 660,367 mirror reads through the seam: `Pending` 420,652, `Status` 179,571, `Config` 52,433, `Stack` 3,295, `Work` 1,905, `Layers` 1,882, `Commands` 390, `ActiveWork` 199, `Sheet` 38 and `Tools` 2. Over the harness's pipe arm with every forwarder running, a barrier-passing call costs about 81µs, a mirror read 0.24µs, and opening a session (server, managers, dial and attach) 0.19ms. `go test ./internal/app/tui/... -count=1` took 32s wall, 28s of it the top package. So syncing before every read would add about 53s; comparing first costs one service read and two encodings per read, and syncs only when they differ (decision 4).
- **Other users of `inproc`.** `internal/launch/launch_test.go` reaches the adapter twice: `modelStateOf` (`:193`, used through `modelState` eight times) and `TestRunPassesAuthResolverToTheSeam` (`:1194`, which reads `*inproc.Host`'s `AuthResolver`). `internal/app`'s tests use it in `tui_seam_test.go:9`, `app_test.go:5521`, `:5607`, `:5616`, `:5779` and `turns_test.go:222`; package `app` cannot import `internal/acp`, which imports `app`. `TestAppImportsNoClient` lists `inproc` (`layering_test.go:59`). Comments in fourteen service files and `acpclient.go` mention it.
- **The client.** `rpc.Pipe` (`rpc/transport.go:118`) returns the client's transport, capped at `rpc.MaxFrameBytes` (64 MiB, `:16`), and the agent's byte stream. `rpc.Dial` (`rpc/dial.go:54`) takes `DialOptions{Handlers, SharedFilesystem, AgentVersion}`; `Host.Close`, `Done` and `Err` are `:124` to `:135`. `rpc.Session`'s transcript reads are `rpc/transcript.go`. Its other reads are `Status` (`rpc/session.go:170`), `Pending` and `ReconcileAccess` (`requests.go`), `Work` (`work.go`), `Config`, `Layers` and `Tools` (`settings.go`), `ActiveWork` (`ending.go`) and `Commands` (`browsers.go`). The rest are calls.
- **Test models.** The conformance harness's pipe arm (`openPipeArmsTB`, `conformance_test.go:225`) serves a state over a pipe with every manager and the full barrier, and `serveTranscript` (`:333`) attaches an event broker to the served state. `startStackAgent` (`stack_wire_test.go:49`) serves a bare `TurnManager`. `largeStackSession` (`stack_wire_test.go:413`, with `largeStackTurns` and `largeStackCalls` at `:30`) builds the 2,000-call session, and twenty `internal/acp` tests call it. `withoutMemoCheck` (`memocheck_test.go:11`) and `raceEnabled` (`stackwired_race_test.go`, `stackwired_norace_test.go`) are what the budget tests use.

**Decisions this plan makes beyond the spec** (R1, R4, R10, R11, V1, V2 and V7 record them):

1. **The silent changes publish.** `Rewind`, `SwitchBranch`, `ClearMessages`, `LogThinking`, `SetTodos`, `AddRunEvent`, `ClearRunEvents` and `SetWorkspace` each publish a new session event, `transcript_changed`, with no payload, once their mutex is released. `Rewind` publishes only when it moved the leaf, and `SetWorkspace` only when the workspace changed. Without this, a `/rewind` over the pipe would leave the old transcript on screen until an unrelated event, because the agent pushes a transcript only after an event marks it dirty. The event goes on the session's own broker, which every subscriber already reads; none acts on a type it does not know.
2. **While the TUI is busy, the agent flushes on every tick.** For a marshal client, the idle loop's tick flushes the main stack dirty or not while the client's busy flag for the session is set, or while a subagent runs. These are exactly the moments the TUI re-projected on its own ticks, and they cover what decision 1 does not: a running call's streaming output, and a running subagent's card. A fence keeps flushing only what is dirty, so no call's budget moves. On the 2,000-call session a busy turn costs one 4.3ms flush every 50ms, the share plan 4b accepted.
3. **`session/sync`.** A new method with no interface method, as `session/stack_busy` has none. The agent marks the session's stacks dirty and answers; the response barrier then fences every forwarder, so the response follows every update the state calls for. `rpc.Session.Sync` sends it. Only the test agent uses it in this plan.
4. **The test agent's reads compare before they sync.** `acptest.Session` wraps the client's session. Each of its ten mirror reads asks the service for its own answer first, compares the two JSON encodings (a stack by its roots and nodes only, since revisions differ), and returns the mirror's value at once when they match. When they differ it sends `session/sync` and reads again, up to three times, then returns the mirror's value whatever it is. So a test that changes the state and then renders sees the change, and the TUI always renders what crossed the wire. Syncing before every read would cost about 53s on the TUI package; comparing first costs a service read and two encodings, and syncs only after a change.
5. **The embedding is `marshal acp`'s host, adopting the embedding's runtime.** `acp.Embedding` builds an `agentHost` whose runtime starter refuses ("embedded mode starts no runtime") and whose closer closes nothing. `Adopt` registers the runtime under its session ID with the service pair `launch` built from `SessionDeps`, so the trust prompt and its decision are the embedding's (7a's open point). `Close` is `CloseAll`: it releases the session's service and closes no runtime, because the embedding closes its own afterwards, as it does today.
6. **A handler panic ends the embedding's connection.** A new `Server.EndOnPanic` field makes `dispatch`'s recover log the panic with its stack, answer the call as today, and report a fatal error naming the method, so `Serve` returns it and the TUI ends with it (R11). Only the embedding sets it; `marshal acp` and the bridge keep recovering.
7. **`launch` builds one pipe per pass of its loop,** and tears it down in this order after the program returns: it closes the client, stops the server and waits for it (bounded at 5s), and closes the embedding, which releases the service. Only then does it take the resume, trust-reload or finish branch, which closes the runtime. This is the order the ACP host closes a session in: a service never outlives the runtime it reads. The server runs on a context that does not end with the signal context, so the TUI's last calls during its own shutdown are still answered. The program's context ends when the client's connection does; launch then returns `launch: the agent's connection closed: <the agent's error>` through `Finish` and takes no resume or reload branch.
8. **Tests reach the served state through the test agent.** `acptest.Session.State()` is how `stateOf` reaches the state, and `launch`'s tests get the served pair through an unexported option. No test reaches through the client to the agent side by type assertion.
9. **A state built without a session ID is served under a fixed one,** `acptest.UnnamedSessionID`, because every call names its session. A test that compares such a session's ID with `""` compares it with the constant instead. In production every runtime has an ID.
10. **The helpers take the test.** Each helper that builds a session gains a `testing.TB` first parameter, so its agent closes with the test. Leaked agents would each keep two forwarders polling every 50ms for the rest of the binary's run.
11. **The harness keeps both arms (V1).** After the switch the TUI holds only the client, but the service is still the agent's logic, and the two arms agreeing is what proves the wire. Package `app`'s three TUI-over-runtime tests keep the service in process: `app` cannot import `internal/acp` (import cycle), and they test `app`'s builders, not the transport.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md` or this plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plan 8`. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-8-switch` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-8-switch: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path. Task 7 deletes a directory: stage it with `git rm -r internal/acpclient/inproc`.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The state (`internal/app/session/session.go`)

```go
// EventTranscriptChanged says something the transcript's projection reads
// changed without an event of its own: the active branch, the thinking log,
// the run events, the todos or the workspace (P3 plan 8). It carries no
// payload, and no ACP update carries it.
EventTranscriptChanged = "transcript_changed"
```

### The shared fixture (`internal/app/session/sessiontest/sessiontest.go`, new)

```go
// Package sessiontest holds session fixtures that more than one package's
// tests share. Only tests import it.
package sessiontest

// LargeTurns and LargeCalls shape LargeSession: each turn is a prompt, one
// step of LargeCalls shell.run calls with 1 KB of output, and a final reply.
const (
	LargeTurns = 200
	LargeCalls = 10
)

// LargeSession builds the 2,000-call session P3's budgets measure (R10).
func LargeSession(tb testing.TB) *session.State
```

### The wire (`internal/acpwire/stack.go`)

```go
// MethodSync is session/sync: its response follows every update the
// session's state calls for (P3 plan 8). Its params are ControlParams.
MethodSync = "session/sync"
```

### The agent (`internal/acp/stack.go`, `server.go`, `session.go`, `host.go`)

```go
// Sync handles session/sync: it marks the session's stacks dirty and
// answers, so the response barrier flushes them, and fences every other
// forwarder, before the response is written.
func (m *TurnManager) Sync(ctx context.Context, params json.RawMessage) (any, error)

// flushesEveryTick reports whether the idle loop flushes the session's main
// stack on every tick, dirty or not: for a marshal client, while its busy
// flag for the session is set or a subagent of st runs (P3 R4).
func (m *TurnManager) flushesEveryTick(sessionID string, st *session.State) bool

// In Server:
	// EndOnPanic makes a handler's panic end the connection: the panic is
	// logged with its stack, the call is answered as before, and Serve returns
	// an error naming the method. The embedding sets it (P3 R11). Set it before
	// Serve.
	EndOnPanic bool

// adopt registers rt under rt.SessionID with the pair its embedding built,
// so the pair is never built from rt.ServiceDeps. It refuses an empty ID and
// an ID already registered.
func (m *SessionManager) adopt(rt *app.Runtime, host *service.Host, sess *service.Session) error

// serveConnWith is serveConn with configure applied to the connection's
// server before its handlers are registered; serveConn passes nil.
func (h *agentHost) serveConnWith(ctx context.Context, stdin io.Reader, stdout io.Writer, configure func(*Server)) error
```

### The embedding (`internal/acp/embedding.go`, new)

```go
// EmbeddingConfig configures the embedding's agent host.
type EmbeddingConfig struct {
	// Logger receives the server's and the host's lines: the runtime's
	// logger, so they land in the session log (P3 R11).
	Logger *slog.Logger
	// MaxFrameBytes is the largest frame the agent's end reads: the pipe's
	// cap (P3 R2).
	MaxFrameBytes int
}

// Embedding is the agent host embedded mode runs in its own process (P3 R1,
// R11): marshal acp's host, serving runtimes the embedding started and still
// owns, to the embedding's own client over one connection.
type Embedding struct {
	// the agent host, built with a starter that refuses and a closer that
	// closes nothing
}

// NewEmbedding builds the host.
func NewEmbedding(cfg EmbeddingConfig) (*Embedding, error)

// Adopt serves rt under its session ID through the service pair the
// embedding built for it.
func (e *Embedding) Adopt(rt *app.Runtime, host *service.Host, sess *service.Session) error

// Service is the service serving a session now: the adopted one, or the
// one a replacement installed.
func (e *Embedding) Service(sessionID string) (*service.Session, bool)

// Serve serves one connection until it ends or ctx does.
func (e *Embedding) Serve(ctx context.Context, conn io.ReadWriter) error

// Close releases every session's service and closes no runtime.
func (e *Embedding) Close(ctx context.Context) error
```

### The client (`internal/acpclient/rpc/transcript.go`)

```go
// Sync returns once every update the agent has for this session is applied:
// session/sync's response follows them (P3 plan 8). It is not part of
// acpclient.Session.
func (s *Session) Sync(ctx context.Context) error
```

### The test agent (`internal/acp/acptest/acptest.go`, new)

```go
// Package acptest is the test agent: a test's state served over an
// in-memory pipe by the agent host embedded mode runs, and the client
// dialled to it (P3 V2). Only tests import it.
package acptest

// UnnamedSessionID is the ID a state built without one is served under.
const UnnamedSessionID = "acptest-session"

// Option adjusts Open.
type Option func(*options)

// Unsettled makes the session's reads the client's own, with no comparison
// and no sync, for a budget that measures the pipe itself.
func Unsettled() Option

// Open serves d's state through an acp.Embedding over a bare runtime,
// dials it, attaches the state's session and returns the client's host and
// session. Everything closes with tb.
func Open(tb testing.TB, d service.Deps, opts ...Option) (*Host, *Session)

// Host is the client's host. NewSession wraps the replacement's session as
// Open wraps the first.
type Host struct {
	*rpc.Host
	// the embedding and the test's options
}

func (h *Host) NewSession(ctx context.Context, name string) (acpclient.Session, error)

// Session is the client's session. Its ten mirror reads compare the mirror
// with the service's own answer and sync when they differ; everything else
// is the client's.
type Session struct {
	*rpc.Session
	// the embedding, the test, the options and the sync count
}

// State is the state the session serves now.
func (s *Session) State() *session.State

// Syncs is how many reads have synced so far, for acptest's own tests.
func (s *Session) Syncs() int

func (s *Session) Status() acpwire.SessionStatus
func (s *Session) Stack(subagentID int64, busy bool) acpwire.StackSnapshot
func (s *Session) Pending() acpwire.PendingRequests
func (s *Session) ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)
func (s *Session) Work() (acpwire.WorkSnapshot, bool)
func (s *Session) Config() config.Config
func (s *Session) Layers() config.Layers
func (s *Session) Tools() ([]string, bool)
func (s *Session) ActiveWork() acpwire.EndWork
func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)
```

### Embedded mode (`internal/launch/pipe.go`, new; `launch.go`)

```go
// agentConn is one pass's connection: the embedding serving the runtime,
// the server's goroutine, and the client the TUI runs on.
type agentConn struct {
	// the embedding, the client's host and session, the server's cancel and
	// its result
}

// dialAgent adopts rt with the pair, serves it on the agent's end of a
// pipe (wrapped by wrap when it is not nil), dials the client's end and
// attaches rt's session.
func dialAgent(ctx context.Context, rt *app.Runtime, host *service.Host, sess *service.Session, wrap func(io.ReadWriteCloser) io.ReadWriteCloser) (*agentConn, error)

// lost is closed when the connection ended before close was called.
func (c *agentConn) lost() <-chan struct{}

// err is the agent's error once the connection is lost: the server's, or
// else the client's.
func (c *agentConn) err() error

// close closes the client, stops the server and waits for it, then closes
// the embedding.
func (c *agentConn) close(ctx context.Context) error

// In options, for tests only:
	agentConnWrap func(io.ReadWriteCloser) io.ReadWriteCloser
	onServed      func(*service.Host, *service.Session)

// withAgentConn wraps the agent's end of each pass's pipe.
func withAgentConn(wrap func(io.ReadWriteCloser) io.ReadWriteCloser) Option

// withServed hands each pass's service pair to fn before the program runs.
func withServed(fn func(*service.Host, *service.Session)) Option
```

### The TUI's test helpers (after Task 5)

```go
func testSession(t testing.TB, state *session.State) acpclient.Session
func testModel(t testing.TB, state *session.State, opts ...Option) Model
func testModelWith(t testing.TB, d service.Deps, opts ...Option) Model
func runnerOf(r service.Runner) func() service.Runner
func withPromptRunner(t testing.TB, m Model, r service.Runner) Model
func withCommands(t testing.TB, m Model, reg *commands.Registry) Model
func testModelStub(t testing.TB, state *session.State, stub *stubSession, opts ...Option) Model
func testModelStubWith(t testing.TB, d service.Deps, stub *stubSession, opts ...Option) Model
func testModelRecording(t testing.TB, state *session.State, rec *answerRecorder, opts ...Option) Model
func testModelControlled(t testing.TB, d service.Deps, rec *controlRecorder, opts ...Option) Model
func testModelEnding(t testing.TB, state *session.State, end service.EndService, opts ...Option) Model
func withEndService(t testing.TB, m Model, end service.EndService) Model
func withSuggester(t testing.TB, m Model, suggest func(context.Context, string) (string, error)) Model
func newTestModelDeps(t *testing.T, edit func(*service.Deps)) Model
```

`testModelCached`, `testModelReplacing`, `testModelTurns`, `newTestModel` and `newTestModelInRepo` keep their signatures, with `service` types in place of `inproc`'s.

### The guard (`internal/acpclient/rpc/imports_test.go`)

```go
// TestOnlyTheEmbeddingDials walks the module's non-test Go files: only
// internal/launch and internal/acp/acptest import this package, nothing
// imports internal/acp/acptest, and nothing imports
// internal/acpclient/inproc (P3 V7).
func TestOnlyTheEmbeddingDials(t *testing.T)
```

## Task 1: The transcript's silent changes publish

**Goal:** every state call that changes what the transcript's projection reads publishes a session event, so a marshal client's mirror shows the change after the next call, as the next refresh showed it in process.

**Files:** `internal/app/session/session.go`, `messages.go`, `streaming.go`, `runevents.go`, `workspace.go`, `internal/app/session/transcript_events_test.go` (new), `internal/acp/conformance_transcript_test.go`.

**Steps:**

1. **The constant**, as in Contracts, after `EventSteeringReceipt` in the block at `session.go:40`.
2. **The eight calls publish** `EventTranscriptChanged` with an empty `Event{}` through `publishEvent`, once per call, after `s.mu` is released. `publishEvent` takes `s.mu` itself, so a call whose unlock is deferred is restructured: unlock explicitly, then publish.
   - `Rewind` publishes only when it moved the leaf. The early return for a message with no parent publishes nothing. Its branch-leaf database write stays where it is.
   - `SwitchBranch` and `ClearMessages` publish on every call.
   - `LogThinking`, `SetTodos`, `AddRunEvent` and `ClearRunEvents` publish on every call. `SetTodos` publishes after its persistence write.
   - `SetWorkspace` publishes only when `changed`, right after its workspace-broker publish. The workspace broker's event stays as it is.

   The startup loads (`loadFromDB`, `loadStepsLocked`, `loadJobExitsLocked`, `restoreWorkspace`) stay silent: no client has read the state yet. `AppendActiveToolCallOutput` stays silent (decision 2 covers it).
3. **No subscriber acts on it.** Search every subscription to a `*pubsub.Broker[session.Event]` (`grep -rn 'Subscribe(' internal --include='*.go'`, then follow each to the broker it reads). Each one either switches on the type and ignores an unknown one, or treats every event as "something changed". `eventToSessionUpdate` has no case for it, so no ACP update is sent. If any subscriber does something else with an unknown type, stop and report.
4. **Tests** in `transcript_events_test.go`, each over a state with an attached broker and one subscription:
   - `TestTheSilentTranscriptCallsPublish`: one row per call. A `Rewind` of the second turn's message, a `SwitchBranch` back, `ClearMessages`, `LogThinking`, `SetTodos`, `AddRunEvent`, `ClearRunEvents` and a `SetWorkspace` to a new active root each publish exactly one `transcript_changed`.
   - `TestANoOpPublishesNothing`: a `Rewind` of an unknown message and a `SetWorkspace` that changes nothing publish nothing.
   - `TestTheCallsPublishWithoutABroker`: each call on a state with no broker returns without a panic.
5. **The conformance case.** In `conformance_transcript_test.go`, add `transcriptChangesReachTheMirror` to the transcript table, as a table over the same eight calls. It runs on both arms (`seamArms()`). Each row:
   - builds a state of two turns, with one tool call whose target sits under a second directory, so a workspace move changes the projection;
   - reads `Stack(0, false)` once;
   - makes its change directly on the state;
   - asserts that a fresh `viewmodel.ProjectState` of the state now differs from that first read, so no row can pass without its push;
   - calls `ClearNotice(ctx, "")`, which changes nothing;
   - reads `Stack(0, false)` again, and asserts its roots and nodes encode as the fresh projection's do.

   On the pipe arm, the call's barrier is what delivers the patch.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/session/ ./internal/acp/
TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/session/ -run 'Silent|NoOpPublishes|PublishWithoutABroker' -count=1 -v 2>&1 | tail -30
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/ -run 'TranscriptConformance|transcriptChanges' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/session/... ./internal/acp/... ./internal/viewmodel/... -count=1
```

Expected: the new tests pass. Every existing session, ACP and viewmodel test passes unchanged, including the ones that count a turn's `session/update` notifications: no update kind is new.

Mutation check: remove `SetTodos`' publish. The todos row fails on the pipe arm and passes on the service arm. Restore it.

## Task 2: The busy tick and `session/sync`

**Goal:** while the TUI is busy, or a subagent runs, the agent re-projects a marshal client's transcript on every tick; and a client can ask for every pending update with one call.

**Files:** `internal/acp/stack.go`, `internal/acp/sheet.go`, `internal/acpwire/stack.go`, `internal/acpclient/rpc/transcript.go`, `internal/acp/stack_fresh_test.go` (new), `internal/acpclient/rpc/transcript_test.go`.

**Steps:**

1. **`flushesEveryTick`**, next to `projectBusy`. It reports true when `isMarshal()` and either the session's `clientBusy` flag is set (read under `busyMu`, and only when `busySet`) or `st.HasRunningSubagent()`.
2. **The tick.** In `ensureStackIdle`'s ticker branch (`stack.go:541`), the main projector's flush passes `onlyDirty` as the negation of `flushesEveryTick(sessionID, cur.State)`. The branch's other calls, the event branch and `serveFence` are unchanged. A fence still flushes only what is dirty (decision 2). Update the loop's doc comment to say both.
3. **`MethodSync`** in the stack's constant block (`acpwire/stack.go:283`), as in Contracts.
4. **`TurnManager.Sync`**, beside `StackBusy`. It decodes `acpwire.ControlParams`. It refuses an empty `sessionId` with `invalidParamsError("session/sync requires sessionId")` and a session `readLookup` does not find with `serverErrorf("unknown session: %s", id)`, as `StackBusy` does. Then it calls `markStackDirty(id)` and answers `struct{}{}`. Subagent stacks need no mark: a fence's `flushChildStacks` flushes them unless final. Register it in `registerTranscript` with `srv.Handle(acpwire.MethodSync, turns.Sync)`. A client without the capability may call it too; for that client no barrier runs, so it only marks the stack dirty.
5. **`rpc.Session.Sync`** sends `MethodSync` with the session's `ControlParams` and returns the call's error. Its doc comment says it is not part of `acpclient.Session`.
6. **Tests** in `stack_fresh_test.go`, over the harness's pipe arm (`openPipeArmsTB` over a `seamState`), type-asserting the arm's session to `*rpc.Session`:
   - `TestABusyClientSeesARunningCallsOutput`: read `Stack(0, true)`, so the busy flag is set. Start an active call with `SetActiveToolCall`, and poll `Stack(0, true)` until its running node appears. Then append "hello" with `AppendActiveToolCallOutput`, which publishes nothing. Within 500ms a read shows a running node whose output contains "hello".
   - `TestARunningSubagentKeepsTheMainStackFlushing`: the same with the busy flag clear and a registered subagent still running.
   - `TestAnIdleClientFlushesOnlyWhatIsDirty`: with the busy flag clear and no subagent, the silent append does not show within 300ms. Then `SetActivity`, which publishes, makes it show within 500ms.
   - `TestSyncDeliversASilentChange`: with the busy flag clear, after a silent append, `Sync` returns, and the very next `Stack(0, false)` shows the output with no polling.
   - `TestSyncRefusesBadParams`: an empty and an unknown session.
   - `TestOnlyAMarshalClientFlushesEveryTick` (V6): a `TurnManager` whose `Marshal` reports false answers `flushesEveryTick` false with a running subagent and a set busy flag; one whose `Marshal` reports true answers true for each.

   In `rpc/transcript_test.go`, `TestSyncSendsTheSessionsMethod`, over the scripted agent the file's other tests use: the agent receives `session/sync` with the session's ID, and an agent error is returned.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/ -run 'BusyClient|RunningSubagentKeeps|IdleClientFlushes|SyncDelivers|SyncRefuses|FlushesEveryTick|ACPClientMethodTable' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acpclient/rpc/ -run 'Sync' -count=1 -v 2>&1 | tail -10
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/ -run 'BusyClient|SyncDelivers' -race -count=3
```

Expected: the new tests pass, three times under `-race`. Every existing stack test and budget passes unchanged. `TestStackBusyFlipBudget` and `TestResponseBarrierBudget` measure calls, and a fence is unchanged.

Mutation checks:
- Make `flushesEveryTick` ignore the busy flag. `TestABusyClientSeesARunningCallsOutput` fails. Restore it.
- Drop `markStackDirty` from `Sync`. `TestSyncDeliversASilentChange` fails. Restore it.

## Task 3: The embedding

**Goal:** `internal/acp` can serve a runtime the embedding started, with the embedding's own service pair, to one client over a pipe, through `marshal acp`'s handlers; and a handler panic can end that connection.

**Files:** `internal/acp/embedding.go` (new), `internal/acp/embedding_test.go` (new), `internal/acp/session.go`, `internal/acp/host.go`, `internal/acp/server.go`, `internal/acp/server_test.go`.

**Steps:**

1. **`EndOnPanic`.** Add the field to `Server`, after `MaxLineBytes`, as in Contracts. In `dispatch`'s deferred recover, when it is set:
   - log the panic at error level with the method, the value and `debug.Stack()`;
   - answer the call with today's error;
   - call `reportFatal` with an error naming the method and the value.

   When it is not set, nothing changes. A panic in a notification's handler goes through the same `dispatch`, so it is covered too.
2. **`serveConnWith`.** Move `serveConn`'s body into it, calling `configure(srv)` (when not nil) right after `NewServer` and before `registerHandlers`; `serveConn` calls it with nil. Nothing else in `marshal acp`'s path changes.
3. **`SessionManager.adopt`**, as in Contracts:
   - take `lifecycleMu`, then `mu`;
   - refuse an empty `rt.SessionID` and an ID already in `sessions`, each with an error naming the ID;
   - set `sessions[rt.SessionID] = rt`;
   - under `servicesMu`, set `services[rt]` to a pair of the given host and session, so `servicePair` never builds one from `rt.ServiceDeps`.

   A replacement already re-keys the runtime and repoints its pair (`replace`), so an adopted runtime stays served under its new ID.
4. **`Embedding`**, in `embedding.go`:
   - **`NewEmbedding`** builds `newAgentHost(runConfig{…})` with:
     - a `startRuntime` that returns `errors.New("acp: embedded mode starts no runtime")`;
     - a `closeRuntime` that returns nil, since every runtime it serves is the embedding's own;
     - no lister;
     - the config's logger.

     It refuses a nil logger or a frame cap that is not positive.
   - **`Adopt`** calls `adopt`.
   - **`Service`** is the manager's `Service`.
   - **`Serve`** calls `serveConnWith(ctx, conn, conn, …)` with a `configure` that sets the server's `Logger`, `MaxLineBytes` to the config's cap, and `EndOnPanic`.
   - **`Close`** is the host's `close` (`CloseAll`).

   The type's doc comment names decisions 5 and 6.
5. **Tests** in `embedding_test.go`. Each test builds a bare runtime, as `acptest` will: `&app.Runtime{State: st, SessionID: "s1", EventBroker: broker}`, with the broker attached to the state. It builds its pair with `service.New(deps)`, adopts it, serves it on `rpc.Pipe()` and dials with `rpc.Dial`.
   - `TestAnEmbeddingServesTheRuntimeItAdopted`: `Attach("s1")` succeeds; the client's `Status()` encodes as the served session's; a `SetNotice` from the client reaches the state.
   - `TestAnEmbeddingServesTheEmbeddingsTrustPrompt`: with `Deps.TrustPrompt` set to a directory, the client host's `TrustPrompt()` answers it. This is what an adoption must carry, where a pair built from `ServiceDeps` would carry none.
   - `TestAnEmbeddingStartsNoRuntime`: a `session/new` without `replaces` fails with the refusal's text, and `Service` still finds only "s1".
   - `TestAnEmbeddedReplacementStaysServed`: `Deps.NewSession` returns a new state with ID "s2", and the test's wrapper points the bare runtime's `State` and `SessionID` at it, as `Runtime.NewSession` does (`runtime.go:2142`). `session/new` with `replaces: "s1"` answers "s2"; `Attach("s2")` succeeds; `Service("s2")` is the new session, and `Service("s1")` reports false.
   - `TestAnEmbeddingsCloseKeepsTheRuntime`: after `Close`, the served session answers `CodeSessionClosed` to `Stop`, `Service("s1")` reports false, and the state still takes an `AddMessage`: nothing shut it down.
   - `TestAdoptRefusesAnEmptyOrTakenID`.
   - In `server_test.go`: `TestAPanicEndsTheConnectionWithEndOnPanic`: a handler that panics, on a server with `EndOnPanic`, makes `Serve` return an error naming the method, and the log holds a line with the panic and a stack. `TestAPanicIsAnsweredWithoutEndOnPanic`: without it, the call is answered "internal error" and `Serve` keeps serving the next call. If an existing test already pins this, extend it instead.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/ -run 'Embedding|Embedded|Adopt|APanic' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/ -run 'Embedding|Embedded' -race -count=3
```

Expected: the new tests pass, three times under `-race`. Every existing host, session and server test passes unchanged: `marshal acp`'s path gains only the nil `configure`.

Mutation checks:
- Make `adopt` skip the pair, so `servicePair` builds one from `rt.ServiceDeps` on first use. `TestAnEmbeddingServesTheEmbeddingsTrustPrompt` fails. Restore it.
- Make the embedding's closer call `rt.Close`. `TestAnEmbeddingsCloseKeepsTheRuntime` fails. Restore it.

## Task 4: The test agent

**Goal:** a test can serve a state over the pipe through the embedding, and read it back after changing it directly, as it read the in-process adapter.

**Files:** `internal/acp/acptest/acptest.go` (new), `internal/acp/acptest/acptest_test.go` (new), `internal/acp/acptest/main_test.go` (new), `internal/app/session/sessiontest/sessiontest.go` (new), `internal/acp/stack_wire_test.go`.

**Steps:**

1. **The shared fixture.** Move `largeStackSession`'s body into `sessiontest.LargeSession`, with `LargeTurns` and `LargeCalls`, as in Contracts. In `stack_wire_test.go`, `largeStackSession` calls it, and `largeStackTurns` and `largeStackCalls` become those constants, so the twenty callers are unchanged.
2. **`Open`:**
   - **The bare runtime.** Pick the session ID: the state's own, or `UnnamedSessionID` when it has none. Build `app.Runtime{State, SessionID, EventBroker, Logger}` with a new `pubsub.Broker[session.Event]`, attached to the state with `SetEventBroker`, and a logger that discards.
   - **The replacement.** When `d.NewSession` is not nil, wrap it: after the caller's function returns a state, attach the broker to that state, then point the runtime's `State` and `SessionID` at it, as `Runtime.NewSession` does.
   - **The pair.** Build it with `service.New` over the deps, with the wrapped `NewSession` in place.
   - **Serving.** Build an `acp.Embedding` with the discarding logger and `rpc.MaxFrameBytes`, and adopt the runtime with the pair. Serve it on the agent's end of `rpc.Pipe()` in a goroutine, on a context of its own.
   - **The client.** Dial the client's end with `SharedFilesystem: true`, and attach the ID.
   - **Cleanup.** Register one `tb.Cleanup` that closes the client, cancels the server's context, waits up to 5s for `Serve` to return (an error through `tb.Error` if it does not), and closes the embedding.
   - A failure anywhere is `tb.Fatalf` naming the step.
3. **The settling reads** (decision 4). Each of the ten reads in Contracts works the same way:
   - it asks `Embedding.Service(id)` for the service now serving the ID;
   - when none is found (a retired or closed session), it returns the client's read as is;
   - otherwise it reads the service's answer and the client's, and compares their `json.Marshal` encodings. For `Stack`, it compares only `Roots` and `Nodes`. For `ReconcileAccess`, the service's call settles a satisfied access request, as the TUI's call did in process and as the request forwarder does on each pass;
   - when they match, it returns the client's value;
   - when they differ, it calls `Sync` (5s timeout), increments the sync count, and compares again with a fresh service answer, up to three syncs;
   - after the third, or after a failed sync, it returns the client's value. A read cannot fail, so the test's own assertions decide.

   With `Unsettled`, each read returns the client's value directly. `Commands`, the host's `ProviderTemplates` and `TrustPrompt` are not wrapped: each is fetched once and never changes (R3).
4. **`Host.NewSession`** calls the client host's `NewSession`. On success it wraps the returned session as `Open` wraps the first, over the same embedding and options. On failure it returns a nil interface and the error.
5. **`State`** returns `Embedding.Service(id)`'s `State()`, or the state `Open` was given when no service is found.
6. **`main_test.go`** runs `testenv.Run` and sets `viewmodel.CheckWireMemo`, as the TUI's `TestMain` does.
7. **Tests** in `acptest_test.go`, each over a state from `session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: "s1"})`:
   - `TestADirectChangeIsReadBack`: one row per read. Each row reads once, then changes the state directly, then reads again and gets the change:
     - `SetTurnUsage` for `Status`;
     - `AddMessage` for `Stack`;
     - `SetPendingApproval` for `Pending`;
     - `SetLayers` for `Layers`;
     - a changed `Config` through `ApplyInteraction` or another setter the state offers, for `Config`;
     - a registered subagent for `Sheet` of its ID;
     - the shapes the service's own tests use for `Work`, `ActiveWork`, `Tools` and `ReconcileAccess`.

     `Syncs()` grew for each row.
   - `TestAReadThatMatchesDoesNotSync`: on a fresh state, read each of the ten twice. `Syncs()` is zero after the second round. This is what catches a read whose two encodings never agree. If one never agrees, fix the client or the agent so it round-trips, not the comparison.
   - `TestTheAgentClosesWithTheTest`: open inside a subtest. After the subtest, the host's `Done()` is closed and the embedding's `Service` reports false.
   - `TestAReplacementIsFollowed`: `Deps.NewSession` returns a state with ID "s2". `Host.NewSession` answers a session whose `ID()` is "s2", whose `State()` is that state, and whose `Status` reads back a direct change to it.
   - `TestAStateWithoutAnIDIsServed`: a state with no session ID is served as `UnnamedSessionID`, and its `Status` reads.
   - `TestUnsettledReadsDoNotSync`: with `Unsettled`, a direct change followed at once by a read leaves `Syncs()` at zero.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/... ./internal/app/session/...
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/acptest/ -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/acptest/ -race -count=3
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acp/... -count=1
```

Expected: the new tests pass, three times under `-race`. Every `internal/acp` budget passes on the moved fixture, with medians where they were.

Mutation check: make a read skip the comparison and return the client's value. The read's `TestADirectChangeIsReadBack` row fails. Restore it.

## Task 5: The TUI's tests over the pipe

**Goal:** every TUI test runs against the test agent, with only its helpers changed, and the R10 key press is measured.

**Files:** `internal/app/tui/seam_test.go`, `answers_seam_test.go`, `controls_seam_test.go`, `ending_seam_test.go`, `newsession_seam_test.go`, `turns_seam_test.go`, `model_test.go`, every other `internal/app/tui` test file that names `inproc` or calls a helper whose signature changes, `internal/app/tui/{agents,connect,settings,skills,textfield}/*_test.go` that name `inproc`, `internal/app/tui/pipe_budget_test.go` (new), `internal/app/tui/memocheck_test.go` (new), `internal/app/tui/race_test.go` and `norace_test.go` (new). Plus whatever agent-side or client-side fixes step 5 finds, each in its own package.

**Steps:**

1. **Measure first.** Before changing anything, record the wall time of `TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/tui/... -count=1` (32s at the base, on the development machine). Step 6 compares against it.
2. **The helpers**, as in Contracts:
   - Every `inproc.Deps` becomes `service.Deps`, and every `inproc.Runner`, `EndService`, `PlanRun`, `ReferenceFinder`, `Session` and `Host` becomes the service's.
   - Every `inproc.New(d)` becomes `acptest.Open(t, d)`.
   - Each helper that did not take a test gains `t testing.TB` first.
   - `stateOfSession` walks `Unwrap` to an `*acptest.Session` and returns its `State()`. Its panic names `*acptest.Session`.
   - `TestModelHostIsTheOneItWasBuiltWith` compares `m.Host()` with the host `acptest.Open` returned.
3. **The call sites.** Pass each helper's new first argument from the test in scope: `t`, or `b` in a benchmark. A mechanical rewrite of the call sites is fine. Review its diff: no assertion may change in this step.
4. **The subpackages.** The eleven `inproc.New` calls in `agents`, `connect`, `settings`, `skills` and `textfield` become `acptest.Open(t, d)`, using whichever of the two results each used.
5. **Run and triage.** Run the whole `internal/app/tui/...` tree. Each failure is one of these five cases, and gets the fix its case names:
   - **The wire changes a value.** For example: nil against empty, a dropped field, a time's precision, an error's form. Make the TUI see what it saw in process. When the wire drops something it can carry, fix the agent or `rpc`. When the TUI told nil from empty, which JSON cannot keep, use a length check in the TUI's code. Never change the assertion.
   - **The test reached the adapter itself.** It asserted on a concrete adapter type, or compared a Go error's identity, which only an `*acpwire.Error`'s code survives on the wire. Compare the host or session `acptest` returned, or the sentence, as the conformance cases do.
   - **An unnamed state's ID.** The test compared a session ID with `""`. Compare it with `acptest.UnnamedSessionID` (decision 9).
   - **Timing.** The test waited on something that over the pipe arrives on a forwarder's next pass, for example a `Changed` after a direct change. Wait on the signal with a deadline, as `batchResults` does. Never add a sleep.
   - **A missing push.** The agent never sends what the TUI needs, so the settle cannot catch up. Fix the agent, as Tasks 1 and 2 did, and add a conformance case that pins it.

   Record each fix outside the test files, by file and case, for Task 8's Implementation record. Stop and report in either of these situations:
   - a failure fits none of the five cases;
   - its fix would change what a client without the capability receives, or would fall outside `internal/acp`, `internal/acp/service`, `internal/acpclient/rpc`, `internal/acp/acptest` or the TUI's render code.
6. **The time budget.** Run the tree again with `-count=1` and compare with step 1. If it takes more than twice as long, stop and report with the slowest packages and tests (`go test -json` gives each test's time). Do not loosen the comparison.
7. **The key press (R10).** In `pipe_budget_test.go`, `TestAKeyPressOverThePipeBudget`:
   - It builds two `sessiontest.LargeSession` states. One model goes over the service in process (`service.New`, then `New`); the other over `acptest.Open(t, d, acptest.Unsettled())`. Both are sized 100×40 and refreshed once.
   - For each model, 30 times, it sends one `keyPress("a")` through `Update`, then calls `View()`, and times the two together. It takes each arm's median.
   - Assert that the pipe arm's median is at most the in-process median plus 1ms. Log both.
   - It skips under `-short` and with the race detector, and turns `viewmodel.CheckWireMemo` off while it measures. Copy `withoutMemoCheck` into `memocheck_test.go`, and `raceEnabled` into the build-tagged `race_test.go` and `norace_test.go`, from `internal/acp`'s.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/...
grep -rn 'acpclient/inproc' internal/app/tui && echo "inproc still imported" || echo "no inproc"
time TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/tui/... -count=1
TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/tui/ -run 'TestAKeyPressOverThePipeBudget|Golden|FrameInvariants' -count=1 -v 2>&1 | tail -30
TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/tui/ -run 'Boundary|ClientSeam' -count=1
```

Expected:
- no TUI test imports `inproc`;
- every TUI package passes, within twice step 1's time;
- T6's goldens pass with no golden file changed (`git status internal/app/tui/testdata` is clean);
- the key-press budget passes, and its log shows both medians;
- `TestTUIStaysOnTheClientSeam` passes: no non-test TUI file imports `rpc` or `acptest`.

Mutation check: make `acptest`'s `Status` read return the client's value without comparing. Status tests that change the state directly, such as the notice and usage tests, fail. Restore it.

## Task 6: Embedded mode over the pipe

**Goal:** `launch.Run` runs the TUI over ACP on an in-memory pipe, and a connection that ends ends the program with the agent's error.

**Files:** `internal/launch/pipe.go` (new), `internal/launch/launch.go`, `internal/launch/launch_test.go`, `internal/launch/pipe_test.go` (new).

**Steps:**

1. **`dialAgent`**, as in Contracts:
   - build `acp.NewEmbedding` with `rt.Logger` and `rpc.MaxFrameBytes`, and adopt `rt` with the pair;
   - take `rpc.Pipe()`, wrapping the agent's end with `wrap` when it is not nil;
   - serve the agent's end in a goroutine, on a context derived from `context.WithoutCancel(ctx)` with its own cancel, so the signal context ending does not cut off the TUI's last calls;
   - dial the client's end with `SharedFilesystem: true` and `AgentVersion: acp.AgentVersion`;
   - attach `rt.SessionID`.

   A failure after the embedding exists closes what was built, in `close`'s order, and returns the error wrapped with `launch:` and the step.
2. **`lost` and `err`.** A goroutine closes `lost` when the client's `Done()` fires before `close` began. `err` is the server's `Serve` error when it returned one, and otherwise the client's `Err()`.
3. **`close`** is decision 7's order:
   - the client's `Close`;
   - the server's cancel, then a wait for `Serve` of at most 5s, logging a warning on `rt.Logger` if it does not return;
   - the embedding's `Close`, bounded by the context given.
4. **The loop** (`launch.go`). In each pass, after `embedded.Start`:
   - build the pair with `service.New(embedded.SessionDeps(ctx))`;
   - call `onServed` when set, then `dialAgent`;
   - seed the file index from the client session;
   - build the program's context with `context.WithCancel(ctx)`, and cancel it from a goroutine when `lost` closes;
   - run the program over `tui.New(conn.Host, conn.Session, …)`.

   When the program returns:
   - cancel its context;
   - note whether the connection was lost;
   - `close` the connection, and log a close error at warn;
   - if the connection was lost, return `embedded.Finish(errors.Join(progResult.Err, fmt.Errorf("launch: the agent's connection closed: %w", conn.err())))` without taking the resume or trust-reload branch;
   - otherwise take today's branches unchanged.

   Every other line of the pass is unchanged: the TUI options, the early return when `ctx` ended, the branches. Delete the `inproc` import. The package doc says the TUI runs over the pipe.
5. **The test options.** `withAgentConn` and `withServed`, as in Contracts, in `pipe.go`. They are unexported, so only this package's tests can reach them.
6. **The existing tests** (`launch_test.go`):
   - `modelState(t, model)` becomes the state of the pair `withServed` recorded for the pass. Each test that used it gains the option, through one helper that returns the option and a getter. `modelStateOf` is deleted.
   - `TestRunPassesAuthResolverToTheSeam` asserts that the model's host is an `*rpc.Host` ("the TUI model holds the pipe's client"), and reads the resolver from the recorded service host's `AuthResolver()`.
   - No other assertion changes.
7. **New tests** in `pipe_test.go`, over `Run` with the injected runtime options `launch_test.go` already uses:
   - `TestRunServesTheTUIOverThePipe`: inside the fake program runner:
     - the model's host is an `*rpc.Host` whose `SharedFilesystem()` is true;
     - its session is an `*rpc.Session` whose `ID()` is the served session's;
     - its `Status()` encodes as the served session's.
   - `TestRunEndsWhenTheAgentsConnectionCloses` (R11): `withAgentConn` returns a wrapper the test can close. The fake runner closes it, then waits on its own context. That context ends within 2s, and `Run` returns an error containing "the agent's connection closed".
   - `TestRunReleasesTheServiceBeforeTheRuntimeCloses`: after `Run` returns, the recorded service session answers `CodeSessionClosed` to `Stop`, and the session is marked ended in the project database, as `Finish` marks it.
   - `TestRunResumesOnAFreshPipe`: a runner that asks to resume once records two different served pairs; the first is released when the second pass starts.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/launch/ ./cmd/marshal/
TMPDIR=/private/tmp/p3-8-suite go test ./internal/launch/ -count=1 -v 2>&1 | tail -60
TMPDIR=/private/tmp/p3-8-suite go test ./internal/launch/ -race -count=2
CGO_ENABLED=1 go build -o /private/tmp/p3-8-suite/marshal ./cmd/marshal
```

Expected: every launch test passes, the moved ones included, twice under `-race`, and the binary builds.

Mutation checks:
- Drop the goroutine that cancels the program on `lost`. `TestRunEndsWhenTheAgentsConnectionCloses` fails: its runner never returns before the test's deadline. Restore it.
- Close the embedding after the branch instead of before it. `TestRunReleasesTheServiceBeforeTheRuntimeCloses` still passes. If it fails, read the order again. The test pins that the service is released, not the order, so record in the self-review that the order is a choice the code comments must state, and leave it as decided.

## Task 7: `inproc` deleted

**Goal:** the in-process transport is gone. Package `app`'s tests use the service directly, and a guard keeps the dial in `launch`.

**Files:** `internal/acpclient/inproc/inproc.go` (deleted), `internal/app/tui_seam_test.go`, `internal/app/app_test.go`, `internal/app/turns_test.go`, `internal/app/layering_test.go`, `internal/acpclient/imports_test.go`, `internal/acpclient/rpc/imports_test.go`, `internal/acpclient/acpclient.go`, the service files whose comments name `inproc`.

**Steps:**

1. **Package `app`'s tests.** In `tui_seam_test.go`, `app_test.go` and `turns_test.go`, every `inproc.Deps`, `inproc.New` and `inproc.Runner` becomes the service's (`service.Deps`, `service.New`, `service.Runner`). These tests keep the service in process (decision 11). Add a sentence to `testTUIModelWith`'s comment saying why.
2. **Delete** `internal/acpclient/inproc` with `git rm -r`.
3. **The guards:**
   - `TestAppImportsNoClient` (`layering_test.go:59`) denies `marshal/internal/acpclient/rpc` where it denied `inproc`. Its comment says `app` never dials.
   - `TestClientImportsStayOnTheWireSide`'s comment names `rpc` as the subdirectory that may import more.
4. **`TestOnlyTheEmbeddingDials`** (V7), in `rpc/imports_test.go`:
   - find the module root from the test file's directory (three levels up), and walk it with `filepath.WalkDir`;
   - skip directories whose name starts with `.` (the repository holds worktrees under `.claude` and `.marshal`), `testdata` and `node_modules`;
   - parse every non-test `.go` file with `parser.ImportsOnly`, and map each importing directory to the paths it imports;
   - assert that the directories importing `marshal/internal/acpclient/rpc` are exactly `internal/launch` and `internal/acp/acptest`, that none imports `marshal/internal/acp/acptest` or `marshal/internal/acpclient/inproc`, and name each offender.
5. **The comments.** Rewrite each comment that names `inproc` in `internal/acp/service` and `acpclient.go`. `service.go:349`, for example, becomes "Tests use it to reach the state behind a session; the TUI cannot import this package". Leave the `inproc:` error texts alone (Non-goals). `grep -rn 'inproc' internal cmd --include='*.go' | grep -v '"inproc:'` prints nothing afterwards.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./...
grep -rn 'inproc' internal cmd --include='*.go' | grep -v '"inproc:' && echo "inproc still named" || echo "clean"
TMPDIR=/private/tmp/p3-8-suite go test ./internal/app/ ./internal/acpclient/... ./internal/launch/ ./internal/acp/service/ -count=1
TMPDIR=/private/tmp/p3-8-suite go test ./internal/acpclient/rpc/ -run 'OnlyTheEmbeddingDials|RPCImports' -count=1 -v 2>&1 | tail -10
```

Expected: `go vet` is clean, the grep prints "clean", and every package passes.

Mutation check: import `rpc` from a scratch non-test file in `internal/app`. Both `TestOnlyTheEmbeddingDials` and `TestAppImportsNoClient` fail. Delete the file.

## Task 8: The docs and full verification

**Goal:** the spec, the design and the maps record the switch, and the whole tree is green with the real binary driving the pipe.

**Files:** `docs/daemon/p3-acp-transport.md`, `docs/daemon/design.md`, `docs/ARCHITECTURE.md`, `AGENTS.md`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 6c did. Expect `acpclient.Methods` unchanged (111 rows: 109 existing, none new, 2 carrying none), and 140 registered methods (139 plus `session/sync`), plus the `session/cancel` notification.
2. **"Where the code is today."** Rewrite the bullets that describe what this plan changed:
   - "The adapter is most of the logic" says the service is, behind the handlers, and `inproc` is deleted;
   - the server bullet gives the new count;
   - "TUI tests build state, not runtimes" says the helpers serve that state over the pipe through `acptest`.
3. **The Implementation record.** Add a "Plan 8 — the switch" entry after plan 7b's. It names:
   - the eight publishing calls and `transcript_changed`;
   - the busy tick and its cost;
   - `session/sync`;
   - the embedding: adopt, the refusing starter, the closer that closes nothing, `EndOnPanic`;
   - `acptest` and its settling reads;
   - the measured TUI suite time before and after;
   - the key press's two medians;
   - every fix Task 5's triage made outside the tests, by case;
   - `launch`'s pipe and its teardown order;
   - the deletion;
   - the guard;
   - the counts.

   Under "Open for P4", record:
   - V2's settle can hide a push the agent never makes after a call; the conformance cases and the usability suite carry that proof;
   - the service's `inproc:` error texts.

   In the header's `Status:`, say P3 is implemented.
4. **`design.md`.** In §4.5, replace the sentence about `inproc` (`:224` to `:226`) with: P3 deleted it, and embedded mode runs the TUI over ACP on an in-memory pipe. In the phase table's P3 row, add "— implemented (see the spec's implementation record)".
5. **`docs/ARCHITECTURE.md`.** Change three things:
   - the flow's step 2 (`:8`): `launch.Run` serves the runtime with the ACP agent host on an in-memory pipe and runs the TUI over the client;
   - the Entrypoint row (`:79`): "the agent host on a pipe and the TUI over its client";
   - the Client seam row (`:82`): drop `internal/acpclient/inproc/`, and say `rpc` is the client the TUI runs on, with `internal/acp/acptest` as the test agent.
6. **`AGENTS.md`:**
   - `internal/acp/service/` (`:69`): drop "(inproc aliases it until P3 plan 8)";
   - `internal/acpclient/` (`:79`): "the TUI's Host/Session interfaces; rpc/ is the JSON-RPC client the TUI runs on";
   - `internal/launch/` (`:80`): "embedded mode's composition root: the run loop, the agent host on a pipe, the TUI over its client";
   - add `internal/acp/acptest/` after `internal/acp/service/`: "the test agent: a test state served over a pipe, for TUI tests";
   - the `internal/acp/` line gains "embedding" at the end of its list;
   - the testing note at `:231` names `service.Deps.HomeDir`;
   - "Inject auth explicitly" says "the session's service, which builds the probes and sign-ins" where it says "the client seam's adapter".
7. **The usability suite**, which drives the real binary, so the pipe, end to end: `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` at the repository root (the harness finds `./marshal`; delete the binary afterwards and never stage it), then `TMPDIR=/private/tmp/p3-8-suite go test ./test/usability/... -run TestScripted -v -timeout 5m`.
8. **Main may have moved.** If `main` moved while this branch ran, merge it into the branch before the full verification. Resolve the conflicts, then rerun Task 5's Verify. Other branches change TUI tests and session code.
9. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-8-suite
gofmt -l internal cmd test
go vet ./...
TMPDIR=/private/tmp/p3-8-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes, `internal/acp/acptest` and `internal/app/session/sessiontest` included. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, `TestAnswerCallBudget`, `TestStackBusyFlipBudget`, `TestControlCallBudget`, `TestSettingsCallBudget`, `TestEndCallBudget`, `TestNoteCallBudget`, `TestBrowserCallBudget`;
  - `internal/app/tui`: `TestAKeyPressOverThePipeBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

**The manual run is the user's** (Done when). The executor reports that it is due and lists it. P2's checklist, plus:
- a long session's transcript stays smooth while streaming, a running shell call's output included;
- Esc cancels an MCP sign-in;
- a sudo prompt;
- `/new`, and `/exit` with a postmortem;
- `/rewind`, which shows the earlier transcript at once.

## Self-review

- **Spec coverage:**
  - Goal and Done when ("runs entirely over ACP on the pipe, and `inproc` is deleted"): Tasks 6 and 7.
  - R1 (how the pipe is served): Tasks 3 and 6.
  - R4 (every change reaches the mirror): Tasks 1 and 2, proved by `transcriptChangesReachTheMirror` and `TestABusyClientSeesARunningCallsOutput`.
  - R7: a replacement through the embedding is `TestAnEmbeddedReplacementStaysServed` and `TestAReplacementIsFollowed`; the TUI's own `/new` tests run over the pipe in Task 5.
  - R10's key press: `TestAKeyPressOverThePipeBudget`.
  - R11: the connection ending is `TestRunEndsWhenTheAgentsConnectionCloses`, a panic is `TestAPanicEndsTheConnectionWithEndOnPanic`, and the same host code is `Embedding` over `agentHost`.
  - The tests: V1 is decision 11 and the harness unchanged. V2 is Task 5. V5 needs nothing new: plan 5's and 7a's secret tests already run over the embedding's handlers. V6 is `TestOnlyAMarshalClientFlushesEveryTick` and Task 1's step 3. V7 is Task 7. V8 is Task 8.
- **What could go wrong:**
  - **The settle hides a missing push.** A read that disagrees with the service syncs, so V2 cannot see a push the agent never makes after a call. The conformance suite proves each call's push (V3); Tasks 1 and 2 add the ones this plan found; the usability suite and the manual run exercise real pushes. The record says so.
  - **Task 5's failure count is unknown.** The triage rule bounds what may change, and the stop conditions bound how far a fix may reach.
  - **The suite gets slower.** Comparing first was chosen because syncing before every read would add about 53s. Step 6 stops the plan at twice the base time.
  - **A test with two models over one state.** `withPromptRunner`, `withCommands`, `withEndService` and `withSuggester` serve the same state a second time. The second `Open` attaches its own broker, so the first agent's idle loop stops hearing events; its reads still settle, because `Sync` marks the stack dirty itself.
  - **A replacement writes the runtime's fields from a handler goroutine,** as `Runtime.NewSession` does in production. A race report there is not this plan's; record it if one appears.
  - **The session log grows** by one dispatch line per call (R11). A turn makes a handful of calls.
  - **A progress callback runs on the client's read goroutine** (plan 1), so it must not call the session. The TUI's two `show` callbacks open a browser and set a URL, and call nothing.
  - **Teardown order.** The service is released before the runtime closes, as the ACP host closes a session. In process nothing released it at all. A run still in flight at that point, which the TUI's quit path prevents, would be stopped with the cause "conversation ended" before `Finish` quiesces.
