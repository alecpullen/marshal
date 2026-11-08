# P3 plan 6c: Pipelines, the end flow and sessions — implementation plan

Date: 2026-11-09. Status: ready for review.
Base: `main` at `d103051b` (P3 plans 6b and 7a merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p3-b67`, `-timeout 20m`): 118 packages ok, and `web/bridge` timed out in the known `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` hang; rerun alone, `web/bridge` passed (156s), so 119 ok.
- **It runs alongside P3 plan 7b** ([browsers](2026-11-09-p3-7b-browsers-plan.md)). Both plans add a manager to `internal/acp`, rows to `acpclient.Methods`, methods to the conformance harness's `seam`, and an entry to the spec's Implementation record. "Integration with plan 7b" (at the end) lists the conflicts this causes and how to resolve each one, and the one step that belongs to whichever plan merges second.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, "Where the code is today", "Methods P3 adds" (plans 6c and 7b), R2's errors, R3's `ActiveWork` and `Commands` rows, R6, R7, R9's list, R10, and Migration order items 6c and 7b. Task 8 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (the service behind the handlers; the interface checks arrive with the last plan), R2 (errors, read your writes, cancellation), R3 (the mirror: the `ActiveWork` row), R6, R7 (sessions), R9 (compatibility), R10 (budgets), "Decided with the user (2026-11-08)", Migration order item 6c, and tests V1, V3, V6 and V8.
Previous plans: [P3 plan 6b, turns and controls](2026-11-08-p3-6b-controls-plan.md), [P3 plan 7a, settings](2026-11-08-p3-7a-settings-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client runs the session's pipelines and its end flow over ACP on the session's service, replaces and closes its session as the in-process host does, and reads what an end would stop from a mirror the agent keeps current.
- A marshal client's errors keep their sentence over the wire, as in process.
- For a marshal client, `session/swarm_start`, `session/sdd_start`, `session/sdd_answer`, `session/close` and `session/list` are served by the session's service (or its service host). Every other client reaches today's handlers.
- Eight new methods: `session/sdd_preflight`, `session/sdd_stop`, `session/sdd_author`, `session/end_work`, `session/end_accept`, `session/end_wait`, `session/exit_analysis` and `session/postmortem`.
- An `end_work` update carries what an end would stop, whenever it changes, at most every 50ms, and before any call's response.
- `session/new` with a `replaces` param swaps the runtime's session, as the in-process host's `NewSession` does.
- `rpc.Host` gains `ListSessions` and the replacement; `rpc.Session` gains the group's twelve methods; the conformance harness gains their cases.

**Non-goals:**
- **No change for a client without the capability.** Its `session/swarm_start`, `sdd_start`, `sdd_answer`, `close` and `list` are today's, byte for byte, and so is a `session/new` without `replaces` (R9). Its errors keep F-SEC-37's opaque text.
- **No notes, commands, browsers or project reads.** They are plan 7b's.
- **No change to the service's run, end or swap logic.** The handlers only decode and encode; the one agent-side change outside them is the session manager's re-keying (Task 4).
- **No change to `internal/app/tui`.** Plan 6a already made `PlanPreflight`, `Host.NewSession` and `Postmortem` asynchronous; the interface's methods keep their signatures, and only their `ACP:` doc lines change.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-6c-suite`. Create the directory first: `mkdir -p /private/tmp/p3-6c-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 7b uses `/private/tmp/p3-7b-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 8 likelier. Rerun a failure alone before treating it as new.
- **The wire hides a plain error today.** The server writes any handler error that is neither an `*acpwire.Error` nor a JSON-RPC error as `internal error` (`wireError`, `server.go:724`; F-SEC-37). So a run that fails with a plain error reaches the TUI as "✘ Turn failed: internal error" over the pipe, where in process it shows its own sentence (`model.go:4993`); plan 6b's conformance cases narrowed two assertions for this reason (`promptPassesOtherErrorsThrough`, `conformance_controls_test.go:808`; `reviewRunsOnTheRunPath`, `:609`). Plan 7a kept nine settings sentences with `settingsFailure` (`settings.go:873`), which turns a plain error into an uncoded `*acpwire.Error`; but the client rebuilds an `*acpwire.Error` only when its data carries a code (`decodeResponseError`, `rpc/conn.go:405`), so those sentences arrive prefixed "server error: ". `dispatch` converts a handler's error at `server.go:906` to `:921`; `marshalClient` is `server.go:488`. The TUI's two `CodeOf` callers switch on named codes (`refusedStart`, `model.go:4013`; `service/turns.go:61`), so an `*acpwire.Error` with no code reads as "not coded" there.
- **The service** (`internal/acp/service`). The pipelines are in `pipelines.go`: `StartSwarm` (`:34`), `PlanPreflight` (`:58`, no turn slot), `StartSDD` (`:77`), `AnswerSDDGate` (`:129`), `StopSDD` (`:166`) and `AuthorPlan` (`:179`); a gated run returns an `*acpwire.Error` with `CodeHumanGateRequired`, which `app`'s `planRun.Run` builds (`app/pipelines.go:152`). The end flow is in `ending.go`: `ActiveWork` (`:32`), `AcceptEnd` (`:41`), `WaitForEnd` (`:53`), `RunExitAnalysis` (`:65`), `Postmortem` (`:78`, which finds the report directory with `os.UserHomeDir`), `Close` (`:103`) and `Host.NewSession` (`:133`), which calls `Deps.NewSession` and `swap`s (`service.go:315`): the old session is closed and a new one built over the new state. `Host.ListSessions` is `browsers.go:16`. `app`'s `bindTurnDeps` (`app/turns.go:161`) wires `Deps.NewSession` to `Runtime.NewSession` (`runtime.go:2002`), which builds the new state and runner inside the same runtime and sets `rt.SessionID` to the new ID.
- **ACP's handlers for the existing methods differ from the service** and stay as they are for other clients: `TurnManager.SwarmStart` (`turn.go:636`) takes no role overrides and answers a cast; `SDDStart` (`turn.go:1607`) takes no strategy, overrides or verify commands, and reports a gate as `stopReason: "gate"`; `SessionManager.CloseSession` (`session.go:558`) cancels the turn manager's turn and closes the whole runtime, MCP servers included, which is far too slow for the TUI's `finishQuit` (`model.go:4815`, on the `Update` goroutine) and would close the runtime the embedding still owns; `SessionManager.List` (`session.go:616`) needs a `SessionLister`, encodes `updatedAt` as RFC 3339 and the message count in `_meta`. `SessionManager.Create` (`session.go:405`) starts a whole new runtime.
- **The session manager.** `sessions` maps an ACP session ID to its runtime, guarded by `mu`; `lifecycleMu` serialises structural changes and is taken before `mu` (`session.go:82`). `services` holds each runtime's `servicePair` (`:127`), keyed by the runtime pointer and built on first use by `servicePair` (`:740`) under `servicesMu`. A forwarder resolves its session through `Service(id)` on every pass and stops when the ID is gone. The turn manager's idle stack loop drops a session's projectors when its lookup fails (`stack.go:547`).
- **`newRoutingSessionManager`** (`routing_test.go:48`) starts real runtimes from a temporary user config whose provider is built without a network call, so `Runtime.NewSession` succeeds on them.
- **Handlers and managers.** `ControlManager` (`controls.go`) is the model: its handlers resolve a session's service with `service` (`:548`), its work forwarder starts on the first marshal read (`ensureForwarder`, `:409`), polls on a ticker and on its fence (`runForwarder`, `:433`), and sends only a changed encoding. `registerControls` is `controls.go:576`. In `registerHandlers` (`host.go:359`), `session/new`, `close` and `list` are registered at `host.go:382` to `:385`, the controls' `others` map is `:431`, the barrier is installed at `:480`, `session/swarm_start`, `sdd_start` and `sdd_answer` at `:498` to `:501`, and the cleanup closes the managers at `:644` to `:652`. `cancellable` (`server.go:443`) gives a handler a context that `$/cancel_request` ends.
- **The client.** `routeNotify` (`rpc/session.go:235`) applies each marshal update to the attached session it names; `rpc/work.go` is the model for a mirror read on first use. `closeLocked` (`:221`) closes a session's `Changed` once; `closeSessions` (`:330`) unregisters every session when the connection ends. `Host.current` is the session every host call names (`rpc/dial.go:41`), and `currentID` refuses before any attach. `Stacks.entries` (`rpc/stacks.go:50`) holds one entry per mirrored stack. `rpc` may import `acpclient` (`rpc/imports_test.go:55`).
- **The interface** (`internal/acpclient/acpclient.go`): `NewSession` (`:49`), `ListSessions` (`:71`), `StartSwarm` (`:503`) to `Close` (`:581`). Their rows in `methods.go` run from `StartSwarm` to `Close`, plus `NewSession` and `ListSessions`.
- **Test models.** `conformance_controls_test.go` shows a case table over both arms and a budget (`TestControlCallBudget`, `:1253`) over `largeStackSession` (`stack_wire_test.go:413`). `startControlAgent` (`controls_test.go:181`) serves a manager to a raw `rpc.Conn`. The service's `fakeEndService` (`ending_test.go:22`), `fakePlanRun` (`pipelines_test.go:19`) and `fakeRunner` (`turns_test.go:18`) are the fakes the moved cases use.

**Decisions this plan makes beyond the spec** (R2, R3, R7 and R9 record them):

1. **A marshal client's error keeps its sentence.** For a client that declared the capability, `dispatch` passes a handler's error through `settingsFailure` before converting it, so a plain error travels as an `*acpwire.Error` with no code and its own message; a coded error, a JSON-RPC error and a cancellation travel as they do today. The client rebuilds an `*acpwire.Error` from a `-32000` response whose data carries a message, with or without a code, so `err.Error()` is the sentence the TUI shows in process. Every other client keeps F-SEC-37's opaque text. This also gives plan 6b's turn errors and plan 7a's settings sentences their text back.
2. **A marshal client's pipelines run on the service.** `session/swarm_start`, `sdd_start` and `sdd_answer` go through `marshalOr`, as plan 6b routed the prompt (decided with the user, 2026-11-08): ACP's handlers take no overrides, strategy or verify commands, and report a gate differently. The service's gate error is an `*acpwire.Error`, so `CodeHumanGateRequired` crosses the wire as itself.
3. **The long calls are cancellable.** `session/swarm_start`, `sdd_start`, `sdd_answer`, `sdd_preflight`, `sdd_author`, `end_wait` and `exit_analysis` take their context from `cancellable`; a stopped preflight stops (R2).
4. **A marshal client's `session/close` ends the conversation, not the runtime.** It is served by the service's `Close`, as in process: the run cancelled, the requests settled, the state shut down. The runtime stays registered, for the embedding to close (plan 8) or the host's `CloseAll`. Closing the runtime from the TUI's `Update` would stop MCP servers on that goroutine and pull the runtime from under the embedding's shutdown.
5. **A replacement is `session/new` with `replaces`.** With the optional `replaces` param, `SessionManager.Create` asks that session's service host for `NewSession`, which builds the new state inside the same runtime as in process (the end flow's freeze, the layer reload and the shared LSP handle included), and then re-keys the runtime under the new session ID. Without `replaces`, `session/new` is today's, for every client. An optional param, not `marshalOr`: a marshal client may still want a whole new runtime (P4).
6. **The client retires a replaced or closed session.** After a successful `session/close`, and after a replacement's new session attaches, the old `rpc.Session` is unregistered from the host and its `Changed` closed; its reads keep answering the mirror's final values, and its calls reach the agent, which answers `CodeSessionClosed` or an unknown session. A replacement also forgets the old session's stack entries.
7. **The end flow's work is a mirror.** `ActiveWork()`'s first read sends `session/end_work`, which for a marshal client starts a forwarder polling the service's `ActiveWork()` every 50ms and on its fence, sending an `end_work` update when the encoding changed, as the work mirror does.
8. **The interface checks arrive with the last plan.** `rpc.Session` cannot satisfy `acpclient.Session` until plan 7b's methods exist, so this plan's replacement is the unexported `replaceSession`. Whichever of plans 6c and 7b merges second adds `Host.NewSession` (a wrapper over it) and the two compile-time checks (R1; Contracts).

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 7b's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 6c and 7b`. Plan 7b gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-6c-ending` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-6c-ending: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/endcalls.go`, new)

```go
// The pipeline, end-flow and session methods, and the end flow's update (P3
// plan 6c).
const (
	MethodSwarmStart   = "session/swarm_start"
	MethodSDDPreflight = "session/sdd_preflight"
	MethodSDDStart     = "session/sdd_start"
	MethodSDDAnswer    = "session/sdd_answer"
	MethodSDDStop      = "session/sdd_stop"
	MethodSDDAuthor    = "session/sdd_author"
	MethodEndWork      = "session/end_work"
	MethodEndAccept    = "session/end_accept"
	MethodEndWait      = "session/end_wait"
	MethodExitAnalysis = "session/exit_analysis"
	MethodPostmortem   = "session/postmortem"
	MethodSessionNew   = "session/new"
	MethodSessionClose = "session/close"
	MethodSessionList  = "session/list"
	UpdateEndWork      = "end_work"
)

// SwarmCallParams is session/swarm_start's params from a marshal client: ACP's
// session and goal, plus SwarmParams' role overrides.
type SwarmCallParams struct {
	SessionID string            `json:"sessionId"`
	Goal      string            `json:"goal"`
	Overrides map[string]string `json:"overrides,omitempty"`
}

// SDDCallParams is session/sdd_start's params from a marshal client: ACP's
// session and plan path, plus SDDParams' strategy, overrides and verify
// commands.
type SDDCallParams struct {
	SessionID string            `json:"sessionId"`
	PlanPath  string            `json:"planPath"`
	Strategy  string            `json:"strategy,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
	Verify    *VerifyCommands   `json:"verify,omitempty"`
}

// SDDAnswerParams is session/sdd_answer's params; ACP's carry the same keys.
type SDDAnswerParams struct {
	SessionID string `json:"sessionId"`
	Answer    string `json:"answer"`
}

// PreflightCallParams is session/sdd_preflight's params.
type PreflightCallParams struct {
	SessionID string              `json:"sessionId"`
	Preflight PlanPreflightParams `json:"preflight"`
}

// AuthorCallParams is session/sdd_author's params.
type AuthorCallParams struct {
	SessionID string           `json:"sessionId"`
	Author    AuthorPlanParams `json:"author"`
}

// EndAcceptCallParams is session/end_accept's params.
type EndAcceptCallParams struct {
	SessionID string          `json:"sessionId"`
	Accept    EndAcceptParams `json:"accept"`
}

// ExitAnalysisParams is session/exit_analysis' params.
type ExitAnalysisParams struct {
	SessionID string `json:"sessionId"`
	Goal      string `json:"goal"`
}

// PostmortemResult is session/postmortem's result: the report's path.
type PostmortemResult struct {
	Path string `json:"path"`
}

// NewSessionParams is session/new's params when it replaces a session: the
// session to replace, and the new one's title. Without Replaces, session/new
// is ACP's own and starts a runtime.
type NewSessionParams struct {
	Replaces string `json:"replaces"`
	Name     string `json:"name,omitempty"`
}

// NewSessionResult is the part of session/new's result a replacing client
// reads.
type NewSessionResult struct {
	SessionID string `json:"sessionId"`
}

// ListSessionsParams is session/list's params from a marshal client: its
// current session, whose service host serves the call, and the query.
type ListSessionsParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Limit     int    `json:"limit,omitempty"`
}

// ListSessionsResult is session/list's result for a marshal client.
type ListSessionsResult struct {
	Sessions []SessionEntry `json:"sessions"`
}

// EndWorkUpdate is an end_work update: the whole membership an end would
// stop, sent whenever it differs from the last one sent on the connection.
type EndWorkUpdate struct {
	Kind string  `json:"kind"`
	Work EndWork `json:"work"`
}
```

Results: `session/swarm_start`, `sdd_start`, `sdd_answer`, `sdd_stop`, `end_wait`, `exit_analysis` and a marshal client's `session/close` answer `{}`; `session/sdd_preflight` a `PlanPreflight`; `session/sdd_author` an `AuthoredPlan`; `session/end_work` an `EndWork`; `session/end_accept` an `EndDecision`; `session/postmortem` a `PostmortemResult`; a replacing `session/new` ACP's `SessionResponse`, whose `sessionId` the client reads; a marshal client's `session/list` a `ListSessionsResult`. `session/sdd_stop`, `end_work`, `end_wait`, `postmortem` and `close` take plan 6b's `ControlParams`.

### The pipeline handlers (`internal/acp/pipelines.go`, new)

```go
// Methods on plan 6b's ControlManager.
func (m *ControlManager) SwarmStart(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SDDPreflight(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SDDStart(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SDDAnswer(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SDDStop(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SDDAuthor(ctx context.Context, params json.RawMessage) (any, error)
```

### The end manager (`internal/acp/ending.go`, new)

```go
// endInterval is how often an end forwarder compares what an end would stop
// with the last one it sent (P3 R3).
const endInterval = 50 * time.Millisecond

// EndManagerConfig wires one connection's end-flow and session handlers.
type EndManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// HostLookup resolves the service host built with a session's service.
	HostLookup func(sessionID string) (*service.Host, bool)
	// Notify sends end_work updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// EndManager serves a session's end flow, its close and the host's session
// list over the session's service for one connection, and runs one end
// forwarder per session a marshal client reads.
type EndManager struct{ /* unexported */ }

func NewEndManager(cfg EndManagerConfig) *EndManager

func (m *EndManager) EndWork(ctx context.Context, params json.RawMessage) (any, error)
func (m *EndManager) EndAccept(ctx context.Context, params json.RawMessage) (any, error)
func (m *EndManager) EndWait(ctx context.Context, params json.RawMessage) (any, error)
func (m *EndManager) ExitAnalysis(ctx context.Context, params json.RawMessage) (any, error)
func (m *EndManager) Postmortem(ctx context.Context, params json.RawMessage) (any, error)
func (m *EndManager) CloseSession(ctx context.Context, params json.RawMessage) (any, error)
func (m *EndManager) ListSessions(ctx context.Context, params json.RawMessage) (any, error)

// Close stops every end forwarder. The connection's cleanup calls it.
func (m *EndManager) Close()

// fenceSession waits until the session's end forwarder has sent the
// membership as it is now, if it differs from the last one sent (P3 R2).
func (m *EndManager) fenceSession(ctx context.Context, sessionID string)

// registerEnds registers the five new end-flow methods on srv, and
// session/close and session/list through marshalOr: a client without the
// capability reaches others[method], unchanged.
func registerEnds(srv *Server, ends *EndManager, others map[string]Handler)
```

### The replacement (`internal/acp/session.go`)

```go
// replace serves session/new with replaces: the replaced session's service
// host builds the new session inside the same runtime, and the manager then
// files the runtime under the new session's ID (P3 R7, plan 6c decision 5).
func (m *SessionManager) replace(ctx context.Context, replaces, name string) (any, error)
```

### The client (`internal/acpclient/rpc/pipelines.go`, `ending.go` and `sessions.go`, new)

```go
// pipelines.go
func (s *Session) StartSwarm(ctx context.Context, params acpwire.SwarmParams) error
func (s *Session) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)
func (s *Session) StartSDD(ctx context.Context, params acpwire.SDDParams) error
func (s *Session) AnswerSDDGate(ctx context.Context, answer string) error
func (s *Session) StopSDD(ctx context.Context) error
func (s *Session) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)

// ending.go
// endCallTimeout bounds the first read's session/end_work.
const endCallTimeout = 5 * time.Second

func (s *Session) ActiveWork() acpwire.EndWork
func (s *Session) AcceptEnd(ctx context.Context, params acpwire.EndAcceptParams) (acpwire.EndDecision, error)
func (s *Session) WaitForEnd(ctx context.Context) error
func (s *Session) RunExitAnalysis(ctx context.Context, goal string) error
func (s *Session) Postmortem(ctx context.Context) (string, error)
func (s *Session) Close(ctx context.Context) error

// sessions.go
func (h *Host) ListSessions(ctx context.Context, cwd string, limit int) ([]acpwire.SessionEntry, error)

// replaceSession sends session/new with the current session as replaces,
// attaches the new session, and retires the old one (decision 6).
func (h *Host) replaceSession(ctx context.Context, name string) (*Session, error)

// retire unregisters s and closes its Changed, once; its reads keep the
// mirror's final values.
func (h *Host) retire(s *Session)

// forget drops every mirrored stack of sessionID without sending anything.
func (s *Stacks) forget(sessionID string)
```

### When both plans 6c and 7b have merged (`internal/acpclient/rpc/sessions.go`)

Whichever of the two plans merges second adds exactly these (decision 8). Plan 7b's plan gives the same declarations.

```go
// NewSession creates a session in the agent that replaces the current one,
// attaches it, and makes it current. A failure leaves the current session as
// it was.
func (h *Host) NewSession(ctx context.Context, name string) (acpclient.Session, error)

var (
	_ acpclient.Host    = (*Host)(nil)
	_ acpclient.Session = (*Session)(nil)
)
```

`NewSession` returns a nil interface, not a nil `*Session`, on failure.

### The harness (`internal/acp/conformance_test.go`, `conformance_ending_test.go` new)

```go
// hostSeam gains ListSessions; seam gains the twelve session methods above,
// with the interface's signatures.

// serveEnds registers the end handlers on the pipe arm's server over the
// served host and service, and returns the manager the barrier fences.
func serveEnds(tb testing.TB, srv *Server, servedHost *service.Host, served *service.Session, sessionID string) *EndManager

func TestEndingConformance(t *testing.T)
func TestEndCallBudget(t *testing.T)
```

## Task 1: A failure keeps its sentence

**Goal:** a marshal client's call that fails with a plain error returns that error's own sentence, as in process; every other client's failures stay opaque.

**Files:** `internal/acp/server.go`, `internal/acp/server_test.go`, `internal/acpclient/rpc/conn.go`, `internal/acpclient/rpc/conn_test.go`, `internal/acp/controls_test.go`, `internal/acp/conformance_controls_test.go`.

**Steps:**

1. **The agent.** In `Server.dispatch`, right after the handler returns an error and before the `*acpwire.Error` check (`server.go:906`), when `s.marshalClient()` reports true, replace the error with `settingsFailure(err)` (`settings.go:873`). Its comment names decision 1 and says why: the marshal client is the embedding's own TUI, which shows these sentences in process, and `settingsFailure` already leaves a coded error, a JSON-RPC error and a cancellation unchanged. Everything after it is unchanged, so the error travels as `-32000` with the whole `*acpwire.Error` as data.
2. **The client.** In `decodeResponseError` (`rpc/conn.go:405`), rebuild the `*acpwire.Error` from a `-32000` response's data when the data decodes and its code *or* its message is not empty (today: only its code). Update the function's doc comment to say so. A `-32000` whose data carries neither stays an `*Error`.
3. **Tests that pinned the opaque text for a marshal client.** `TestSetEffortAnswersWhenItApplies` (`controls_test.go:1217`) expects the setter's error at code `internalError`; it now expects an `*acpwire.Error` with no code and the message "effort refused", and still not invalid params. Rename nothing. It is the only such test found at the base; if another test fails because a marshal client's plain error now carries its sentence, update it the same way: assert the sentence, never loosen it to "any error".
4. **Plan 6b's narrowed cases get their assertion back** (`conformance_controls_test.go`): `promptPassesOtherErrorsThrough` also asserts the error's text is "boom" on both arms, and `reviewRunsOnTheRunPath` asserts the refusal's text is the service's (the text of the error the service arm returns for a review naming both a range and a base). Update their comments: the wire now carries an unclassified error's message (decision 1).
5. **Tests:**
   - In `server_test.go`, `TestAMarshalClientsPlainErrorKeepsItsSentence`: a server whose client declared the capability (through `registerInitialize` and an `initialize` request with `clientCapabilities.marshal`, as `client_core_test.go`'s tests declare it) answers a handler's `errors.New("the plan does not parse")` as code `-32000` with data `{"message":"the plan does not parse"}` and no `code` key.
   - `TestAMarshalClientsCancellationStaysACancellation`: the same server answers a handler's `context.Canceled` with the request-cancelled code, unchanged.
   - `TestDispatchRequestKeepsOtherErrorsOpaque` (`server_test.go:1075`) must keep passing unchanged: a client without the capability still gets `internal error`.
   - In `conn_test.go`, `TestAnUncodedAgentErrorKeepsItsMessage`: a response `{"code":-32000,"message":"server error: boom","data":{"message":"boom"}}` returns an `*acpwire.Error` whose `Error()` is "boom" and whose code is empty, and `acpclient.IsCode` is false for every code; a `-32000` with data `{}` stays an `*Error`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-6c-suite
go build ./...
go vet ./internal/acp/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'Dispatch|WireError|PlainError|Cancellation|SetEffort|TestControlConformance|TestSettingsConformance' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
```

Expected: the new tests pass; every existing server error test passes unchanged; plan 7a's settings cases pass, now with the bare sentence; the two strengthened cases pass on both arms.

Mutation checks:
- Drop the `settingsFailure` call from `dispatch`. `TestAMarshalClientsPlainErrorKeepsItsSentence` and the strengthened `promptPassesOtherErrorsThrough` fail on the pipe arm. Restore it.
- Restore the code-only check in `decodeResponseError`. `TestAnUncodedAgentErrorKeepsItsMessage` fails. Restore the fix.

## Task 2: The pipelines on the service

**Goal:** a marshal client's `session/swarm_start`, `session/sdd_start` and `session/sdd_answer` run on the session's service, and `session/sdd_preflight`, `session/sdd_stop` and `session/sdd_author` call it too.

**Files:** `internal/acpwire/endcalls.go` (new), `internal/acpwire/endcalls_test.go` (new), `internal/acp/pipelines.go` (new), `internal/acp/pipelines_calls_test.go` (new), `internal/acp/controls.go`, `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The wire types.** Add every constant and type in Contracts' wire block. Tests in `endcalls_test.go`:
   - `TestSDDCallParamsCarryTheRunsOptions`: an `SDDCallParams` with a strategy, one override and verify commands round-trips, and one with only a plan path encodes exactly `{"sessionId":…,"planPath":…}`, the keys ACP's own params use.
   - `TestEndWorkUpdateRoundTrips`: an `EndWorkUpdate` with one item round-trips, and its `kind` is `end_work`.
   - `TestNewSessionParamsNameTheReplacedSession`: `NewSessionParams{Replaces: "s1"}` encodes `{"replaces":"s1"}`.

   `TestWireFieldsFollowTheRules` must keep passing.
2. **The handlers** in `pipelines.go`, each a method on `ControlManager` that decodes its params type (a decode error is `invalidParamsError`), resolves the service with `m.service`, and returns the service's answer or its error unchanged:
   - `SwarmStart`: decode `SwarmCallParams`; call `StartSwarm(runCtx, acpwire.SwarmParams{Goal, Overrides})`; return `struct{}{}`.
   - `SDDStart`: decode `SDDCallParams`; call `StartSDD(runCtx, acpwire.SDDParams{PlanPath, Strategy, Overrides, Verify})`; return `struct{}{}`. A gate's `CodeHumanGateRequired` is the service's `*acpwire.Error` and travels as itself.
   - `SDDAnswer`: decode `SDDAnswerParams`; call `AnswerSDDGate(runCtx, Answer)`; return `struct{}{}`.
   - `SDDPreflight`: decode `PreflightCallParams`; return `PlanPreflight(runCtx, p.Preflight)`.
   - `SDDAuthor`: decode `AuthorCallParams`; return `AuthorPlan(runCtx, p.Author)`.
   - `SDDStop`: decode `ControlParams`; call `StopSDD(ctx)`; return `struct{}{}`.

   `runCtx` is the context `cancellable(ctx)` returns, its cancel called when the handler returns (decision 3), as `ControlManager.Review` does. `SDDStop` is not cancellable.
3. **Registration.** In `registerControls` (`controls.go:576`), add `session/sdd_preflight`, `session/sdd_stop` and `session/sdd_author` with `srv.Handle`, and `session/swarm_start`, `session/sdd_start` and `session/sdd_answer` through `marshalOr` over `others`, as the prompt is. Update the function's doc comment: it now registers fourteen methods unconditionally and ten through `marshalOr`. In `registerHandlers`, delete the three lines at `host.go:498`, `:500` and `:501`, and add `turns.SwarmStart`, `turns.SDDStart` and `turns.SDDAnswer` to the `others` map passed to `registerControls` (`host.go:431`) under their `acpwire` names. Leave `session/swarm_status` and `session/sdd_status` where they are.
4. **The method table.** In `methods.go`, mark `PlanPreflight`, `StopSDD` and `AuthorPlan` existing. Rewrite their `ACP:` paragraphs in `acpclient.go` as "ACP: session/sdd_preflight (exists)." and so on, keeping `PlanPreflight`'s "(asynchronous, R6)" note if its doc has one. Add to `StartSwarm`'s, `StartSDD`'s and `AnswerSDDGate`'s paragraphs: "A marshal client's call is served by the session's service."
5. **Tests** in `pipelines_calls_test.go`, over `startControlAgent` with a service whose `Deps` carry `Swarm`, `NewPlanRun`, `PlanPreflight` and `AuthorPlan` fakes (copy `fakePlanRun` from the service's `pipelines_test.go`):
   - `TestAMarshalSwarmRunsOnTheService`: `session/swarm_start` with a goal and one override reaches `Deps.Swarm` with both; an `others` stub for the method is never called.
   - `TestAPipelineWithoutTheCapabilityReachesItsOtherHandler`: one subtest per method, `session/swarm_start`, `session/sdd_start` and `session/sdd_answer`; without the capability, each call reaches its `others` stub, and neither `Deps.Swarm` nor `Deps.NewPlanRun` is called.
   - `TestAGatedPlanRunCrossesAsItsCode`: `session/sdd_start` over a plan run that returns the gate error answers an error for which `acpclient.IsCode(err, acpwire.CodeHumanGateRequired)` is true; `session/sdd_answer` with "yes" then resumes the run, which records the answer.
   - `TestSDDStartCarriesTheRunsOptions`: the strategy, overrides and verify commands reach `Deps.NewPlanRun` unchanged.
   - `TestThePreflightAndTheAuthorReachTheService`: `session/sdd_preflight` answers the fake's preflight; `session/sdd_author` answers the fake's candidate with `stopReason` `end_turn`; `session/sdd_stop` clears the state's gate.
   - `TestCancellingAPlanRunStopsIt`: cancel the client's context for `session/sdd_start` while the fake run waits on its context; the run's context ends within 2s.
   - `TestThePipelineCallsRefuseBadParams`: a missing `sessionId` is invalid params for each of the six methods; an unknown session is the unknown-session error.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acpwire/ -count=1
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'Swarm|SDD|Plan|Preflight|Author|Pipeline|ACPClientMethodTable|ACPWire' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'MarshalSwarm|GatedPlanRun|CancellingAPlanRun' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing swarm and SDD test passes unchanged (`TestACPWireSDDStartGateAndAnswer` among them, which declares no capability); the method-table tests pass.

Mutation check: make `registerControls` register `session/sdd_start` with `controls.SDDStart` directly, without `marshalOr`. The `session/sdd_start` subtest of `TestAPipelineWithoutTheCapabilityReachesItsOtherHandler` fails. Restore it.

## Task 3: The end flow and the close on the agent

**Goal:** the end flow's five methods call the session's service, a forwarder keeps a marshal client's end mirror current through the barrier, and a marshal client's `session/close` and `session/list` reach the service.

**Files:** `internal/acp/ending.go` (new), `internal/acp/ending_test.go` (new), `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The manager.** `EndManager` holds its config, a mutex, one forwarder per session and a `closed` flag, as `ControlManager` holds its work forwarders. Its `service(sessionID)` refuses an empty ID with `invalidParamsError("sessionId is required")` and an unknown one with `serverErrorf("unknown session: %s", id)`, as `ControlManager.service` does; its `host(sessionID)` does the same over `HostLookup`, as `SettingsManager.host` (`settings.go:784`) does.
2. **The handlers**, each decoding its params type, resolving the service, and returning the service's answer or its error unchanged:
   - `EndWork`: decode `ControlParams`; when `Marshal` reports true, make sure the session has an end forwarder (step 3); return the service's `ActiveWork()`.
   - `EndAccept`: decode `EndAcceptCallParams`; return `AcceptEnd(ctx, p.Accept)`.
   - `EndWait`: decode `ControlParams`; call `WaitForEnd(runCtx)`; return `struct{}{}`.
   - `ExitAnalysis`: decode `ExitAnalysisParams`; call `RunExitAnalysis(runCtx, Goal)`; return `struct{}{}`.
   - `Postmortem`: decode `ControlParams`; return `PostmortemResult{Path}` from `Postmortem(ctx)`.
   - `CloseSession`: decode `ControlParams`; call the service's `Close(ctx)`; return `struct{}{}` (decision 4). It does not touch the session manager: the runtime stays registered.
   - `ListSessions`: decode `ListSessionsParams`; resolve the service host for `SessionID`; return `ListSessionsResult{Sessions}` from its `ListSessions(ctx, Cwd, Limit)`.

   `runCtx` is `cancellable(ctx)`'s, as in Task 2.
3. **The forwarder.** One per session per connection, built and run exactly as `ControlManager.ensureForwarder` and `runForwarder` (`controls.go:409`, `:433`) build and run the work forwarder: its fence is registered before its goroutine starts, it wakes on an `endInterval` ticker and on its fence, it resolves the service through `Lookup` on each pass (answering a pending fence and stopping when the session is gone), and it sends `session/update` with the session's ID and `acpwire.EndWorkUpdate{Kind: acpwire.UpdateEndWork, Work: …}` only when the membership's encoding differs from the last one it sent. It never sends under the manager's mutex. `Close` cancels every forwarder and makes later starts do nothing.
4. **Registration.** `registerEnds` registers `session/end_work`, `end_accept`, `end_wait`, `exit_analysis` and `postmortem` with `srv.Handle`, and `session/close` and `session/list` through `marshalOr` over `others`. In `registerHandlers`, build `ends := NewEndManager(EndManagerConfig{Lookup: manager.Service, HostLookup: manager.ServiceHost, Notify: h.sink.Notify, Marshal: srv.marshalClient})` right after `settings` is built; delete the `session/close` and `session/list` lines (`host.go:384`, `:385`); call `registerEnds(srv, ends, map[string]Handler{acpwire.MethodSessionClose: manager.CloseSession, acpwire.MethodSessionList: manager.List})` right after the barrier line; append `ends` to the `responseBarrier` call, last, after `controls`; and call `ends.Close()` in the cleanup, after `controls.Close()`.
5. **The method table.** Mark `ActiveWork`, `AcceptEnd`, `WaitForEnd`, `RunExitAnalysis` and `Postmortem` existing, and rewrite their `ACP:` paragraphs as "ACP: session/end_work (exists). An end_work update refreshes it; the first read subscribes." for `ActiveWork`, and "ACP: session/end_accept (exists)." and so on for the others, keeping `Postmortem`'s asynchronous note if it has one. Add to `Close`'s paragraph: "A marshal client's call is served by the session's service: it ends the conversation and leaves the runtime to the embedding." Add to `ListSessions`': "A marshal client's call names its current session, whose service host serves it."
6. **Tests** in `ending_test.go`, serving an `EndManager` and a `StatusManager` on a pipe as `startControlAgent` serves the control manager, with `responseBarrier(statuses, ends)`; the service's `Deps.End` is a copy of the service package's `fakeEndService` that a test can change the membership of.
   - `TestTheFirstEndWorkArrivesBeforeTheResponse`: the first marshal `session/end_work` returns after exactly one `end_work` update for the session was recorded, and its membership equals the result.
   - `TestANewMembershipIsSentWithinTheInterval`: change the fake's membership; an update carrying it arrives within 500ms. Three quiet intervals then send nothing.
   - `TestAnAcceptanceReturnsAfterItsUpdate` (V3): with every `session/update` frame slowed by 50ms, a `session/end_accept` whose fake empties its membership before it answers returns only after the update carrying the empty membership was recorded.
   - `TestTheEndCallsReachTheService`: `end_accept` passes its params and answers the fake's decision; `end_wait` and `exit_analysis` reach the fake (the goal recorded); `postmortem` answers a path under the `HOME` the test sets.
   - `TestAMarshalCloseEndsTheConversationAndKeepsTheRuntime`: with an approval pending on the state, a marshal `session/close` answers `{}`, the approval is denied, the state is shut down, the `others` stub for `session/close` is never called, and the manager's `Lookup` still resolves the session (nothing was unregistered).
   - `TestACloseWithoutTheCapabilityReachesTheSessionManager` (V6): without the capability, `session/close` and `session/list` reach their `others` stubs.
   - `TestAMarshalListNamesItsCurrentSession`: `session/list` with the session's ID and a `cwd` answers the service host's rows from a migrated database (copy `browserDB` from the service's `browsers_test.go`), with `updatedAt` in Unix milliseconds; without a database it answers `CodeNoDatabase`.
   - `TestAClientWithoutTheCapabilityGetsNoEndWork` (V6): without the capability, `session/end_work` answers the membership and no `end_work` update is recorded within 200ms.
   - `TestTheEndForwarderStopsWhenTheSessionGoes`: make `Lookup` report false; within 500ms the forwarder has stopped and its fence is gone.
   - `TestTheEndCallsRefuseBadParams`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'End|Close|ListSessions|MarshalList|Barrier|AResponseFollows|ACPClientMethodTable' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'FirstEndWork|NewMembership|AnAcceptanceReturns|MarshalClose' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing session close and list test passes unchanged (`session_test.go`, `lister_test.go`); plans 3a's, 3b's, 6b's and 7a's ordering tests pass with the new fence.

Mutation checks:
- Drop `ends` from the test server's `responseBarrier`. `TestAnAcceptanceReturnsAfterItsUpdate` and `TestTheFirstEndWorkArrivesBeforeTheResponse` fail. Restore it.
- Make `CloseSession` call the session manager's close instead (route it to `others`). `TestAMarshalCloseEndsTheConversationAndKeepsTheRuntime` fails. Restore it.

## Task 4: The replacement on the agent

**Goal:** `session/new` with `replaces` swaps the runtime's session as the in-process host does, and files the runtime under the new session's ID.

**Files:** `internal/acp/session.go`, `internal/acp/session_replace_test.go` (new), `internal/acpclient/acpclient.go`.

**Steps:**

1. **`Create` looks for `replaces` first.** At the top of `SessionManager.Create` (`session.go:405`), decode the params into `acpwire.NewSessionParams` (a decode error falls through to the existing decoding, which reports it as it does today). When `Replaces` is not empty, return `m.replace(ctx, p.Replaces, p.Name)`. Otherwise everything below runs as today. Its doc comment gains a paragraph naming decision 5.
2. **`replace`**, as in Contracts:
   - Take `lifecycleMu` for the whole replacement, so a close or a load of the same session cannot interleave with it. (A replacement builds a runner and can take seconds; other lifecycle calls wait. Embedded mode has one session. Say so in its comment.)
   - Look up the runtime registered for `replaces`; an unknown ID is `serverErrorf("unknown session: %s", replaces)`.
   - Resolve its service host with `servicePair(replaces)`; a runtime that is closing refuses as `Service` does, with the same unknown-session error.
   - Call the host's `NewSession(ctx, name)`. Its error is returned unchanged and nothing else changes: the runtime's old session stays registered and current, as in process.
   - On success, the returned `acpclient.Session` is a `*service.Session` (type-assert it; a failed assertion is `serverErrorf` naming the type). Under `mu`, delete `sessions[replaces]` and set `sessions[newID] = rt`. Under `servicesMu`, set the runtime's pair's `sess` to the new session, so `Service(newID)` answers it and the replaced one, which the host's `swap` already closed, is never served again.
   - Return `SessionResponse{SessionID: newID, InteractionState: sessionInteractionState(rt)}`, as `Create` answers.
   - Log the replacement at debug with both IDs, as `Close` logs.
3. **The interface's doc.** Rewrite `NewSession`'s `ACP:` paragraph: "ACP: session/new (exists), with replaces naming the current session: the agent builds the new session inside the same runtime. Asynchronous (R6)." Keep the rest.
4. **Tests** in `session_replace_test.go`, over `newRoutingSessionManager` (real runtimes). Each test sets the turn canceller with `noopCancel()` and closes what it started with `Close`, as `TestStartRuntimeCarriesInjectedResolverToCommandRuntime` (`host_test.go:189`) does.
   - `TestAReplacingNewSessionKeepsTheRuntime`: create a session with today's params; send `Create` with `{"replaces":<id>,"name":"next"}`. The answer names a new ID; `Get(new)` returns the same runtime pointer; `Get(old)` reports false; the runtime's `SessionID` is the new ID and its state's title is "next"; `Service(new)` returns a session whose `ID()` is the new ID; the service `Service(old)` returned before the replacement now answers `CodeSessionClosed` to `Stop`.
   - `TestAFailedReplacementKeepsTheSession`: set the runtime's `Config.Agent.Mode` to `interaction.Mode("bogus")` before replacing; the call returns an error whose text contains "invalid interaction defaults"; `Get(old)` still returns the runtime, its `SessionID` is unchanged, and `Service(old)` returns the same service as before.
   - `TestAReplacementOfAnUnknownSessionIsRefused`: `replaces` naming no session answers the unknown-session error, and `All()` still lists only the runtime the test created.
   - `TestANewSessionWithoutReplacesStartsARuntime`: today's params still start a second runtime; every existing `session_test.go` test passes unchanged.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'Replac|NewSession|Create|SessionManager|ACPClientMethodTable' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -count=1
```

Expected: the new tests pass; every existing lifecycle test passes unchanged.

Mutation check: skip the `servicesMu` update of the pair's `sess`. `TestAReplacingNewSessionKeepsTheRuntime` fails: `Service(new)` answers the replaced session. Restore it.

## Task 5: The client's pipelines and end flow

**Goal:** `rpc.Session` sends the pipeline and end-flow calls and mirrors what an end would stop.

**Files:** `internal/acpclient/rpc/pipelines.go` (new), `internal/acpclient/rpc/ending.go` (new), `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/pipelines_test.go` (new), `internal/acpclient/rpc/ending_test.go` (new).

**Steps:**

1. **The pipeline calls** in `pipelines.go`, each sending its method with its params type and the session's ID, as `Review` does:
   - `StartSwarm`: `SwarmCallParams` from the params' goal and overrides.
   - `StartSDD`: `SDDCallParams` from the params' plan path, strategy, overrides and verify commands.
   - `AnswerSDDGate`: `SDDAnswerParams`.
   - `PlanPreflight`: `PreflightCallParams`; decode the `PlanPreflight`.
   - `AuthorPlan`: `AuthorCallParams`; decode the `AuthoredPlan`.
   - `StopSDD`: `ControlParams`.
2. **The end calls** in `ending.go`:
   - `AcceptEnd`: `EndAcceptCallParams`; decode the `EndDecision`.
   - `WaitForEnd`: `ControlParams`. `RunExitAnalysis`: `ExitAnalysisParams`. `Postmortem`: `ControlParams`; return the result's `Path`.
3. **The end mirror** in `ending.go`. The session holds one `EndWork` entry, absent until the first read, with an updated mark, as the work mirror does (`rpc/work.go`).
   - `ActiveWork()`: an entry returns its membership without I/O. Otherwise create the entry, send `session/end_work` under an `endCallTimeout` context, and install the result unless an update arrived during the call. On error remove the entry and return an empty `EndWork`; the next read asks again.
4. **Routing.** `routeNotify` gains a case for `acpwire.UpdateEndWork`: decode `EndWorkUpdate` and hand its membership to the attached session it names. The session replaces its entry's membership, marks it updated, and taps `Changed`. An update for a session that has not read its membership is dropped. Update `routeNotify`'s doc comment to list `end_work`.
5. **Tests**, over the scripted agent of `session_test.go`:
   - `TestThePipelineCallsSendTheirMethods`: each of the six sends its method with the session's ID and its params, and decodes its result; a gate error comes back with `acpclient.IsCode(err, acpwire.CodeHumanGateRequired)` true.
   - `TestTheEndCallsSendTheirMethods`: `AcceptEnd`, `WaitForEnd`, `RunExitAnalysis` and `Postmortem`.
   - `TestActiveWorkFetchesOnItsFirstRead`, `TestActiveWorkInstallsTheUpdateThatPrecedesTheResponse`, `TestAnEndWorkUpdateReplacesTheMembershipAndFiresChanged`: the rules plan 6b's tests pin for the work mirror, for the end flow.
   - `TestAFailedActiveWorkReadAsksAgain`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included, three times under `-race`; plans 3b's to 7a's client tests pass unchanged.

Mutation check: make the router skip `end_work`. `TestAnEndWorkUpdateReplacesTheMembershipAndFiresChanged` fails. Restore it.

## Task 6: The client's sessions

**Goal:** `rpc.Session.Close` ends the conversation and retires the mirror, `rpc.Host` lists the stored sessions, and the host replaces its current session.

**Files:** `internal/acpclient/rpc/sessions.go` (new), `internal/acpclient/rpc/ending.go`, `internal/acpclient/rpc/stacks.go`, `internal/acpclient/rpc/sessions_test.go` (new).

**Steps:**

1. **`retire`** in `sessions.go`, as in Contracts: under the host's mutex, delete the session from `sessions` only if the entry is this session; then, under the session's mutex, `closeLocked`. It does not clear `current`. A retired session's mirror fields are untouched, so its reads answer their final values, and `routeNotify` no longer finds it, so no update changes them.
2. **`Close`** in `ending.go`: when the session is already closed (its `closed` flag), return nil without sending. Otherwise send `session/close` with `ControlParams`. On success, or on an error carrying `CodeSessionClosed`, retire the session (its host is reached through a back-pointer the session gains: add a `host *Host` field, set where `Attach` builds the session) and return nil. Any other error is returned and the session stays as it was.
3. **`ListSessions`** in `sessions.go`: `currentID()` (its error returned as it is); send `session/list` with `ListSessionsParams{SessionID, Cwd, Limit}`; return the result's `Sessions`.
4. **`forget`** in `stacks.go`: under the mirror's mutex, delete every entry whose key names the session, and its `changed` channel. It sends nothing: the agent's loops for that ID stop when the ID goes.
5. **`replaceSession`** in `sessions.go`, as in Contracts:
   - `currentID()`; its error is returned (no session attached).
   - Send `session/new` with `NewSessionParams{Replaces: current, Name: name}` and decode a `NewSessionResult`. On error return it: the current session is unchanged (R7).
   - `Attach(ctx, result.SessionID)`; on error return it. (The agent has already replaced the session, so the old mirror now answers an unknown session; say so in the comment. The TUI reports the failure as it reports a failed replacement.)
   - Retire the old session (if it was attached) and `forget` its stacks. `Attach` has already made the new session current.
   - Return the new session.
6. **Tests** in `sessions_test.go`, over the scripted agent of `session_test.go`:
   - `TestCloseRetiresTheSession`: `Close` sends `session/close` once; afterwards `Changed` is closed, `Status()` answers the last mirrored status, a later `session_status` update for the ID changes nothing, and a second `Close` sends nothing and returns nil.
   - `TestACloseThatFailsKeepsTheSession`: an agent error other than `CodeSessionClosed` is returned, and `Changed` stays open.
   - `TestListSessionsNamesTheCurrentSession`: the frame carries the current session's ID, the `cwd` and the limit; the rows come back; before any attach it fails with the no-session error and sends nothing.
   - `TestReplaceSessionAttachesTheNewOne`: the frame carries `replaces` with the current ID and the name; the agent's answer names "s2"; the new session's status is fetched; the host's current session is "s2"; the old session's `Changed` is closed; a later `stack_patch` for the old ID reaches no mirror entry.
   - `TestAFailedReplacementKeepsTheCurrentSession`: an agent error is returned; nothing is attached, the current session is unchanged, and its `Changed` stays open.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acpclient/rpc/ -run 'Close|ListSessions|Replace' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing client test passes.

Mutation check: remove `retire`'s `closeLocked`. `TestCloseRetiresTheSession` and `TestReplaceSessionAttachesTheNewOne` fail. Restore it.

## Task 7: The conformance harness and the budget

**Goal:** the group's cases run against the service and against the client over a pipe, and agree (V1); an end call stays inside R10's budget.

**Files:** `internal/acp/conformance_test.go`, `internal/acp/conformance_ending_test.go` (new), `internal/acp/service/pipelines_test.go`, `internal/acp/service/ending_test.go`, `internal/acp/service/browsers_test.go`.

**Steps:**

1. **The seams.** Add `ListSessions` to `hostSeam`, and the twelve session methods in Contracts to `seam`.
2. **The pipe arm.** In `openPipeArmsTB`, after `serveControls`, call `serveEnds(tb, srv, servedHost, served, sessionID)`, and add its manager to `responseBarrier`, last. `serveEnds` builds an `EndManager` whose `Lookup` and `HostLookup` return the served service and host for the served ID, with `Notify: srv.Notify` and `Marshal: srv.marshalClient`, calls `registerEnds` with a nil `others`, and closes the manager with the test.
3. **The table.** `TestEndingConformance` runs every case once per arm, as `TestControlConformance` does; the `ListSessions` cases use `hostArms`. Move these tests from the service's tests into it, deleting them there, and copy the helpers they use (`fakeEndService`, `fakePlanRun`, `gatedPlanRun`, `browserDB`) into the new file:
   - pipelines: `TestStartSwarmRunsOnTheRunPath`, `TestAStoppedSwarmIsNotAnError`, `TestStartSDDBuildsThePlanWhenItStarts`, `TestAGatedPlanWaitsForItsAnswer`, `TestAGateCanPauseTheRunAgain`, `TestStopSDDDropsThePausedRun`, `TestStopSDDClearsAStaleQuestion`, `TestANewPlanRunReplacesAPausedOne`, `TestAuthorPlanReturnsTheCandidate`, `TestAStoppedAuthoringReportsCancelled`, `TestPlanPreflightForwardsWithoutTheSlot`;
   - the end flow: `TestEndCallsForwardToTheEndService`, `TestASessionWithoutAnEndServiceEndsAtOnce`, `TestPostmortemWritesTheReport`, `TestCloseShutsTheSessionDown`, `TestCloseCancelsTheRunInFlight`;
   - the host: `TestListSessionsReadsTheDatabase`.

   These stay in the service's package: the tests that read or set the session's paused run (`TestAnAnswerNeedsAPausedRunAndText`, `TestAStopThatReachesAGatedRunStillReportsTheGate`, `TestARefusedAnswerKeepsTheRunPaused`), the tests that drive the in-process host's `swap` (`TestAClosedSessionStartsNoPipeline`, `TestAClosedSessionEndsNothing`, `TestNewSessionReplacesTheCurrentSession`, `TestAFailedNewSessionKeepsTheCurrentOne`), and `TestBrowsersWithoutADatabase`, which plan 7b's `Memories` half shares. Add one new case instead, `listSessionsWithoutADatabaseIsCoded`: the host's `ListSessions` on a state with no database fails with `CodeNoDatabase` on both arms.

   Each case builds its `Deps` and opens the arm before acting. Where a service test waits on one `Changed` signal and then reads, the case waits until the read shows the change, with a 5s deadline. `TestCloseShutsTheSessionDown`'s case keeps every assertion: on the pipe arm, `Changed` is closed by `retire`, `ID()` keeps its value, and `Stop` after the close answers `CodeSessionClosed` from the agent.
4. **A difference is a bug, never the case's,** as in plans 5 and 6b: fix the wire type, the handler or the client so both arms agree. If the fix would change what the TUI sees today, stop and report it instead.
5. **The budget (R10).** `TestEndCallBudget`: the pipe arm over `largeStackSession`, with `Deps.End` a fake whose decision accepts and whose membership never changes. Read `ActiveWork()` once (which subscribes), then time 30 `AcceptEnd` calls; each includes its barrier, the end forwarder's fence included. The median must be under 1ms. Log it with `t.Logf` for the Implementation record. Skip under `-short` and with the race detector, as `TestControlCallBudget` does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'TestEndingConformance|TestEndCallBudget|TestControlConformance|TestSettingsConformance|TestRequestConformance|TestStatusConformance|TestTranscriptConformance' -count=1 -v 2>&1 | tail -80
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-6c-suite go test ./internal/acp/ -run 'TestEndingConformance' -race -count=3
```

Expected: every case passes on both arms, three times under `-race`; the budget test passes and logs its median; the earlier plans' cases still pass with the end manager in the pipe arm; the service package passes without the moved tests.

Mutation check: make the client's `StartSDD` drop `Strategy`. The moved `TestStartSDDBuildsThePlanWhenItStarts` case fails on the pipe arm. Restore it.

## Task 8: The docs and full verification

**Goal:** the spec records what plan 6c built and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`, and, only if plan 7b merged first, `internal/acpclient/rpc/sessions.go` and `internal/acpclient/rpc/sessions_test.go`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 6b did. Expect, if plan 7b has not merged: 111 rows, 95 of them an existing ACP method (eight more: `PlanPreflight`, `StopSDD`, `AuthorPlan`, `ActiveWork`, `AcceptEnd`, `WaitForEnd`, `RunExitAnalysis` and `Postmortem`), 14 new in P3 carried by 14 distinct new methods, and 2 carrying none; and 125 registered methods (117 plus the eight), plus the `session/cancel` notification. If plan 7b merged first, measure the merged tree and record what it shows. Update the section's "What the agent pushes today" list: a marshal client also receives `end_work`.
2. **The Implementation record.** Add a "Plan 6c — pipelines, the end flow and sessions" entry after plan 7a's (after plan 7b's, if it merged first). It names: the error rule and the client's decode, and the two plan 6b cases it strengthened; the pipelines through `marshalOr` and the three new pipeline methods; the cancellable long calls; the end manager, its five new methods, `endInterval` and `end_work`; the barrier's new fence; a marshal client's close and list; the replacement and the re-keying under `lifecycleMu`; `rpc.Session`'s twelve methods, its end mirror and `retire`; `rpc.Host.ListSessions` and `replaceSession`; the harness's cases; and the budget's median. Record two open points for P4: a replacement holds `lifecycleMu` while it builds the runner, and a marshal client's `session/list` reads the project database for any `cwd` it names, where ACP's handler checks the trusted roots (F-SEC-33).
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "end flow" in its list, after "controls".
4. **Main may have moved.** If plan 7b merged while this branch ran, merge `main` into the branch before the full verification, resolve the conflicts as "Integration with plan 7b" says, and then add the declarations under "When both plans 6c and 7b have merged" to `rpc/sessions.go`, with one test in `sessions_test.go`: `TestNewSessionIsTheReplacement`, which runs `NewSession` against the scripted agent and gets the attached session back as an `acpclient.Session`, and on an agent error gets a nil interface. If plan 7b has not merged, leave them to it.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-6c-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-6c-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, `TestAnswerCallBudget`, `TestStackBusyFlipBudget`, `TestControlCallBudget`, `TestSettingsCallBudget`, and now `TestEndCallBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Integration with plan 7b

Plan 7b adds the browser manager. Whichever branch merges second merges `main` first and resolves these, keeping both sides:
- **`host.go`'s `registerHandlers`:** this plan moves three pipeline lines into the controls' `others` map, builds the end manager after `settings`, replaces the `session/close` and `session/list` lines with `registerEnds`, appends `ends` to the barrier and closes it in the cleanup; plan 7b deletes the fourteen `Handle` lines of the commands, memories, diff, file, skills and plugins and registers them through its own `registerBrowsers` call near the end. The hunks are different lines; keep both. Plan 7b adds no fence, so the barrier's order stays `turns, works, watches, statuses, sheets, requests, settings, controls, ends`.
- **`conformance_test.go`:** both add methods to `seam`; this plan also adds `ListSessions` to `hostSeam`, and a `serveEnds` line and barrier entry to the pipe arm; plan 7b adds a `serveBrowsers` line and attaches the transcript broker to the served state in `serveTranscript`. Keep every line.
- **`rpc/session.go`:** this plan adds an `end_work` case to `routeNotify` and a `host` field to `Session`; plan 7b adds a commands entry to `Session`. Keep both.
- **`acpclient/methods.go` and `acpclient.go`:** different rows and different methods' docs; no line is shared.
- **`AGENTS.md`:** both add a word to the `internal/acp/` line; keep both, "end flow" before "browsers".
- **The interface checks** belong to the second merge: it adds the declarations under "When both plans 6c and 7b have merged" (Task 8, step 4).
- **The spec:** both add an Implementation record entry and re-measure the counts. Record the merged tree's counts: after both, 111 rows, 109 of them existing, none new, 2 carrying none, and 139 registered methods.

## Self-review

- **Spec coverage.** R2's errors: Task 1. The 2026-11-08 decision that the service runs a marshal client's turns, extended to the pipelines: Task 2 (`marshalOr`), proved by `TestAMarshalSwarmRunsOnTheService` and the conformance table. R3's `ActiveWork` row (first read, kept current at most every 50ms and before any response): Tasks 3 and 5. R7: Tasks 3 (close), 4 and 6 (replacement). R2's cancellation: `TestCancellingAPlanRunStopsIt`. R2 and V3: `TestAnAcceptanceReturnsAfterItsUpdate`, and every moved case that reads after a call on the pipe arm. R9 and V6: `TestAPipelineWithoutTheCapabilityReachesItsOtherHandler`, `TestACloseWithoutTheCapabilityReachesTheSessionManager`, `TestAClientWithoutTheCapabilityGetsNoEndWork`, `TestDispatchRequestKeepsOtherErrorsOpaque`. R10: `TestEndCallBudget`. V1: Task 7. V8: Task 8.
- **What could go wrong.**
  - **An error that should stay opaque.** The rule applies only to a client that declared the capability, which in P3 is the embedding's own TUI; every other client's path is unchanged and pinned. P4's remote TUI is the user's own client of the user's own daemon; the server still logs every error in full, as before.
  - **A replacement that races a close.** Both take `lifecycleMu`; the replacement holds it until the re-keying is done.
  - **A runtime that leaks after a marshal close.** It stays registered on purpose (decision 4); the host's `CloseAll` still closes it, and plan 8's embedding closes it on exit.
  - **The old session's loops.** Every forwarder resolves its session on each pass and stops when the ID is gone; the idle stack loop drops the old ID's projectors (`stack.go:547`).
  - **Plan 7b.** The shared files and their resolutions are listed above.
