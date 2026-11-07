# P3 plan 5: Agent requests — implementation plan

Date: 2026-11-07. Status: ready for review.
Base: `main` at `9301125d` (P3 plans 3b and 4c merged). Every anchor below was checked there. The full suite passed there: 119 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-next-suite`, `-timeout 20m`; `web/bridge` 88s).
- **It runs alongside P3 plan 4b** ([the transcript read and the sheet](2026-11-07-p3-4b-transcript-plan.md)). Both plans add a manager to `internal/acp`, a fence to the response barrier, a case to the client's router, methods to the conformance harness's `seam`, and rows to `acpclient.Methods`. "Integration with plan 4b" (at the end) lists the conflicts this causes and how to resolve each one.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, "Decided with the user (2026-11-07)" (this plan's design), "Methods P3 adds" (4b and 5), G1, R3 (the `Pending` row and "How the requests arrive"), R4, R5 (rewritten), R8, R9, R10, and Migration order items 4b and 5. `docs/daemon/design.md` §4.4, §6.3 and the P4 row changed with them. Task 6 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R2 (read your writes), R3 (the mirror: the `Pending` row), R5 (agent requests, decided with the user 2026-11-07), R8 (secrets), R9 (compatibility), R10 (budgets), Migration order item 5, and tests V1, V3, V5, V6 and V8.
Previous plans: [P3 plan 3b, status](2026-11-07-p3-3b-status-plan.md), [P3 plan 3a, ordering](2026-11-06-p3-3a-ordering-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client reads a session's open requests over ACP, from a mirror the agent keeps current, and answers each one with a call that returns its outcome.
- `session/pending` returns the open requests. For a marshal client it also starts a forwarder that sends them whole, as a `pending_requests` update, whenever they change, at most every 50ms, and before any call's response.
- `session/answer_approval`, `answer_question`, `answer_skill_gate`, `answer_sudo`, `answer_access` and `session/rollback` call the service's own answers.
- For a marshal client, the turn's forwarder and the child-approval collector stop sending `session/request_permission` and `session/request_question`.
- `rpc.Session` mirrors the requests and sends the answers.
- The conformance harness gains the request cases, and a test proves a sudo password crosses the wire in one frame only.

**Non-goals:**
- **No change for a client without the capability.** It keeps today's `session/request_permission` and `session/request_question`, from the turn and from the child collector, byte for byte (R9).
- **No `session/request_secret` and no `request_settled`.** The spec dropped them (R5).
- **No change to the service's request logic.** The keys, the owners, the forced deny, the always and session rules, the skill gate's remembered choice and the rollback's audit row are the service's, unchanged. The handlers only decode and encode.
- **No change to `internal/app/tui`.** The interface's methods keep their signatures; only their `ACP:` doc lines change.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-5-suite`. Create the directory first: `mkdir -p /private/tmp/p3-5-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 4b uses `/private/tmp/p3-4b-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 6 likelier. Rerun a failure alone before treating it as new.
- **The service** (`internal/acp/service`). `Pending` (`requests.go:16`) reads every open request from the state (the session's own approval, each running subagent's, the question, the skill gate, the sudo prompt and the typed access request) and settles nothing; its keys are the registry IDs, or `local-N` minted for an unregistered prompt. The answers are in `answers.go`: `AnswerApproval` (`:36`), `AnswerQuestion` (`:156`), `AnswerSkillGate` (`:185`), `AnswerSudo` (`:240`), `ReconcileAccess` (`:266`), `AnswerAccess` (`:289`) and `Rollback` (`:314`). Each returns `errRequestSettled` (`CodeRequestSettled`) for a key that names nothing open, and `errSessionClosed` on a closed session. `ReconcileAccess` settles a typed access request that asks for nothing through the runtime's `AccessRequest`, then returns the one to present. Measured at the base on the 2,000-call session: one `Pending()` read and its encoding take 0.13µs.
- **The turn asks every client today.** In `TurnManager.PromptTurn`'s `forward` (`internal/acp/turn.go:807`), after the event's `session/update`, three blocks drive the requests: approvals and typed access requests (`:831`, through `bridge.Request` or `resolveAccessRequest`, or a deny when no bridge is wired), questions (`:890`, through `qbridge.Ask`, or `Unanswered`), and skill gates (`:926`). The child-approval collector (`child_approvals.go`) is started by `ensureChildApprovals` (`:46`), which the host's `Lookup` calls on every session call (`host.go:256`). `TurnManager.marshal` (`turn.go:210`) reports the connection's capability; `stackIntervals` (`stack.go:62`) is its only reader today.
- **Handlers and managers.** `StatusManager` (`internal/acp/status.go`) is the model to copy: its handlers resolve a session's service with `Lookup` (`service`, `:173`), its forwarder starts on the first marshal read (`ensureForwarder`, `:195`), polls on a ticker and on its fence (`runForwarder`, `:219`), and sends only a changed encoding. `registerStatus` (`:286`) registers its methods. In `registerHandlers` (`host.go:315`), the status manager is built at `:391` and the barrier installed at `:397`; the cleanup returns at `:553`. `responseBarrier` (`host.go:288`) takes the four managers positionally and skips a nil one; `ordering_test.go:153`, `status_test.go:125` and `conformance_test.go:95` call it too.
- **Errors.** A missing `sessionId` is `invalidParamsError`; an unknown session is `serverErrorf("unknown session: %s", id)`. A service error is returned as it is, so an `*acpwire.Error`'s code travels as error data (plan 1) and `acpclient.IsCode` sees it on the client.
- **Logging.** `Server.dispatch` (`server.go:878`) logs each call's method, ID, duration and error, never its params; the error log at `:777` logs the handler's error. No service answer puts a password in an error.
- **The client.** `routeNotify` (`rpc/session.go:188`) applies `session_status` to the attached session it names and ignores every other kind. `Session` (`session.go:14`) holds the status mirror and `Changed`; `Attach` (`:41`) installs a result only when no update arrived during the call. The scripted agent in `rpc/session_test.go` (`startScripted` `:25`, `dialScripted` `:115`, `attachScripted` `:173`) is the model for the client's tests.
- **The interface** (`internal/acpclient/acpclient.go`). `Pending` (`:273`–`:282`), `AnswerApproval` (`:284`–`:293`), `AnswerQuestion` (`:295`–`:300`), `AnswerSkillGate` (`:302`–`:308`), `AnswerSudo` (`:310`–`:316`), `ReconcileAccess` (`:318`–`:326`), `AnswerAccess` (`:328`–`:334`) and `Rollback` (`:336`–`:340`). Their rows are `methods.go:51`–`:58`; all but `ReconcileAccess` are marked new, and `ReconcileAccess` carries no ACP method.
- **Test models.** `TestPromptTurnBridgesPendingApprovalToPermissionClient` (`permissions_test.go:152`) runs a turn whose runner publishes an approval, with `fakePermissionClient` (`:19`); `fakeQuestionClient` is at `questions_test.go:43`. `newChildApprovalHarness` (`child_approvals_test.go:73`) wires the child collector. `startStatusAgent` (`status_test.go:94`) serves a manager to a raw `rpc.Conn` that records updates. The harness is `conformance_test.go` (`seam` at `:25`, `openPipeSeamTB` at `:66`). `largeStackSession` (`stack_wire_test.go:412`) is R10's 2,000-call session. `syncBuffer` (`ordering_test.go:27`) is a goroutine-safe buffer.

**Decisions this plan makes beyond the spec** (R3 and R5 record them):

1. **The request manager is a status manager for requests.** One forwarder per session per connection, started by the first `session/pending` from a marshal client, polling every 50ms and on its fence. Each pass settles a satisfied access request (`ReconcileAccess`), then reads `Pending`, and sends both as one `PendingState` when the encoding changed. Polling costs 0.13µs a pass on the largest fixture.
2. **The update carries two things.** `PendingState.Requests` is `Pending`'s value; `PendingState.Access` is the access request `ReconcileAccess` returned. The client's `Pending` returns the first and its `ReconcileAccess` the second, so both reads are the service's, exactly.
3. **The first read subscribes.** `Pending` and `ReconcileAccess` fetch with `session/pending` on their first read, then serve the mirror, as the stack and the sheet do. `Attach` is unchanged, so plan 3b's tests are too.
4. **An answer is a call whose response follows the update.** The barrier fences the request forwarder, so the update that removes an answered request is written before the answer's response, and a `Pending` read after the call no longer shows it. The response carries the service's error (`CodeRequestSettled` included) and `AnswerAccess`'s result.
5. **The turn leaves a marshal client alone.** `forward` returns after the event's `session/update` when the client declared the capability, and `ensureChildApprovals` starts no collector for such a client. Neither denies for want of a bridge: the request stays open for the mirror. Every request any part of the session raises reaches the client through the service's `Pending`, which reads the state, so nothing is lost: a turn's, a subagent's, a pipeline's, a sudo prompt.
6. **The password is zeroed on both ends.** The agent's service hands the decoded slice to the sudo handler, which zeroes it after use, as in process. The client zeroes the caller's slice when `AnswerSudo` returns, so the TUI's copy does not outlive the answer either.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, `docs/daemon/design.md`, this plan or plan 4b's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 4b and 5`. Plan 4b gives the same instruction, so whichever plan starts first commits all four. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-5-requests` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-5-requests: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/requests.go`)

```go
// The request methods and update kind (P3 R5).
const (
	MethodPending         = "session/pending"
	MethodAnswerApproval  = "session/answer_approval"
	MethodAnswerQuestion  = "session/answer_question"
	MethodAnswerSkillGate = "session/answer_skill_gate"
	MethodAnswerSudo      = "session/answer_sudo"
	MethodAnswerAccess    = "session/answer_access"
	MethodRollback        = "session/rollback"
	UpdatePending         = "pending_requests"
)

// PendingParams is session/pending's and session/rollback's params.
type PendingParams struct {
	SessionID string `json:"sessionId"`
}

// PendingState is what the request mirror holds: the open requests, and the
// typed access request the access panel presents (ReconcileAccess's answer),
// read after a request that asked for nothing was settled.
type PendingState struct {
	Requests PendingRequests `json:"requests"`
	Access   *AccessRequest  `json:"access,omitempty"`
}

// PendingUpdate is a pending_requests update: the whole state, sent whenever
// it differs from the last one sent on the connection.
type PendingUpdate struct {
	Kind    string       `json:"kind"`
	Pending PendingState `json:"pending"`
}

// AnswerApprovalParams is session/answer_approval's params.
type AnswerApprovalParams struct {
	SessionID string         `json:"sessionId"`
	Key       string         `json:"key"`
	Answer    ApprovalAnswer `json:"answer"`
}

// AnswerQuestionParams is session/answer_question's params: one answer per
// question, in order.
type AnswerQuestionParams struct {
	SessionID string   `json:"sessionId"`
	Key       string   `json:"key"`
	Answers   []Answer `json:"answers"`
}

// AnswerSkillGateParams is session/answer_skill_gate's params.
type AnswerSkillGateParams struct {
	SessionID string          `json:"sessionId"`
	Key       string          `json:"key"`
	Choice    SkillGateChoice `json:"choice"`
}

// AnswerSudoParams is session/answer_sudo's params. They carry a password:
// neither end logs them (P3 R8).
type AnswerSudoParams struct {
	SessionID string     `json:"sessionId"`
	Key       string     `json:"key"`
	Answer    SudoAnswer `json:"answer"`
}

// AnswerAccessParams is session/answer_access's params. The call answers an
// AccessResult.
type AnswerAccessParams struct {
	SessionID string       `json:"sessionId"`
	Choice    AccessChoice `json:"choice"`
}
```

`session/pending` answers a `PendingState`, `session/answer_access` an `AccessResult`, and the other five `{}`.

### The request handlers (`internal/acp/requests.go`, new)

```go
// requestInterval is how often a request forwarder compares the session's
// open requests with the last ones it sent (P3 R3).
const requestInterval = 50 * time.Millisecond

// RequestManagerConfig wires one connection's request handlers.
type RequestManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends pending_requests updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// RequestManager serves session/pending, the answer calls and
// session/rollback for one connection, and runs one request forwarder per
// session a marshal client reads.
type RequestManager struct{ /* unexported */ }

func NewRequestManager(cfg RequestManagerConfig) *RequestManager

func (m *RequestManager) Pending(ctx context.Context, params json.RawMessage) (any, error)
func (m *RequestManager) AnswerApproval(ctx context.Context, params json.RawMessage) (any, error)
func (m *RequestManager) AnswerQuestion(ctx context.Context, params json.RawMessage) (any, error)
func (m *RequestManager) AnswerSkillGate(ctx context.Context, params json.RawMessage) (any, error)
func (m *RequestManager) AnswerSudo(ctx context.Context, params json.RawMessage) (any, error)
func (m *RequestManager) AnswerAccess(ctx context.Context, params json.RawMessage) (any, error)
func (m *RequestManager) Rollback(ctx context.Context, params json.RawMessage) (any, error)

// Close stops every forwarder. The connection's cleanup calls it.
func (m *RequestManager) Close()

// fenceSession waits until the session's request forwarder has sent the open
// requests as they are now, if they differ from the last ones sent (P3 R2).
func (m *RequestManager) fenceSession(ctx context.Context, sessionID string)

// pendingState is what one pass reads: it settles a typed access request
// that asks for nothing (ReconcileAccess), then reads the open requests.
func pendingState(ctx context.Context, svc *service.Session) acpwire.PendingState

// registerRequests registers the seven request methods on srv.
func registerRequests(srv *Server, requests *RequestManager)
```

### The response barrier (`internal/acp/host.go`)

Plan 4b makes the same change, word for word. If it merged first, the change is already there: keep it and only append this plan's manager.

```go
// sessionFencer is a manager whose forwarders serve the response barrier
// (P3 R2).
type sessionFencer interface {
	fenceSession(ctx context.Context, sessionID string)
}

// responseBarrier fences each manager's forwarders for the session, in the
// order given, skipping a nil one. A fence whose forwarder has gone answers
// at once.
func responseBarrier(fencers ...sessionFencer) func(ctx context.Context, sessionID string)
```

### The turn (`internal/acp/turn.go`)

```go
// isMarshal reports whether the connection's client declared the marshal
// capability. A marshal client's requests are the request mirror's (P3 R5).
func (m *TurnManager) isMarshal() bool
```

### The client (`internal/acpclient/rpc/requests.go`, new)

```go
// pendingCallTimeout bounds the first read's session/pending.
const pendingCallTimeout = 5 * time.Second

func (s *Session) Pending() acpwire.PendingRequests
func (s *Session) ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)
func (s *Session) AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error
func (s *Session) AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error
func (s *Session) AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error
func (s *Session) AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error
func (s *Session) AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error)
func (s *Session) Rollback(ctx context.Context) error
```

### The harness (`internal/acp/conformance_test.go`, `conformance_requests_test.go` new)

```go
// seam gains:
	Pending() acpwire.PendingRequests
	ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)
	AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error
	AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error
	AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error
	AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error
	AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error)
	Rollback(ctx context.Context) error

// serveRequests registers the request handlers on the pipe arm's server over
// the served service, and returns the manager the barrier fences.
func serveRequests(tb testing.TB, srv *Server, served *service.Session, sessionID string) *RequestManager

func TestRequestConformance(t *testing.T)
func TestASudoPasswordCrossesTheWireOnce(t *testing.T)
func TestAnswerCallBudget(t *testing.T)
```

## Task 1: The wire types

**Goal:** `acpwire` carries the request mirror's update, its read, and the seven calls' params.

**Files:** `internal/acpwire/requests.go`, `internal/acpwire/requests_test.go`.

**Steps:**

1. Add the constants and types in Contracts after `AccessResult`'s status values.
2. Tests in `requests_test.go`:
   - `TestPendingUpdateRoundTrips`: a `PendingUpdate` holding an approval (with a subagent owner and `Forced`), a question with options, a skill gate, a sudo prompt and an access request in both `Requests.Access` and `Access` encodes and decodes to an equal value, and its `kind` is `pending_requests`.
   - `TestAnEmptyPendingStateEncodesNoRequests`: `PendingState{}` encodes as `{"requests":{}}`: every list and pointer is omitted.
   - `TestAnswerSudoParamsCarryThePasswordAsBytes`: `AnswerSudoParams{Answer: SudoAnswer{OK: true, Password: []byte("pw")}}` decodes back to the same bytes; a deny (`OK: false`, no password) encodes no `password` key.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-5-suite
go build ./...
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acpwire/ -count=1
```

Expected: the package passes.

## Task 2: The request handlers and the forwarder

**Goal:** `acp.Server` serves `session/pending`, the five answers and `session/rollback`, and keeps a marshal client's open requests current through the barrier.

**Files:** `internal/acp/requests.go` (new), `internal/acp/requests_test.go` (new), `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **`pendingState`.** Call the service's `ReconcileAccess(ctx)`, then `Pending()`, and return `PendingState{Requests: …}` with `Access` set to the reconciled request when it reported one.
2. **The handlers.** Each decodes its params, refuses an empty `sessionId` with `invalidParamsError`, and resolves the service with `Lookup` (unknown: `serverErrorf("unknown session: %s", id)`), as `StatusManager.service` does. Then:
   - `Pending`: when `Marshal` reports true, make sure the session has a forwarder (step 3); return `pendingState(ctx, svc)`.
   - `AnswerApproval`, `AnswerQuestion`, `AnswerSkillGate`, `AnswerSudo`: call the service's answer with the key and the answer, and return `struct{}{}` or the service's error as it is. `AnswerSudo` never puts the params or the password in an error or a log line.
   - `AnswerAccess`: return the service's `AccessResult`, or its error.
   - `Rollback`: decode `PendingParams`, call the service's `Rollback`, and return `struct{}{}` or its error.
3. **The forwarder.** One per session per connection, as `StatusManager.ensureForwarder` and `runForwarder` (`status.go:195`, `:219`) build and run one: its fence is registered before its goroutine starts, it wakes on a `requestInterval` ticker and on its fence, it resolves the service through `Lookup` on each pass (answering a pending fence and stopping when the session is gone), and it sends `session/update` with `SessionUpdateParams{SessionID, Update: acpwire.PendingUpdate{Kind: acpwire.UpdatePending, Pending: pendingState(…)}}` only when the encoding differs from the last one it sent. It never sends under the manager's mutex. `Close` cancels every forwarder and makes later starts do nothing.
4. **`registerRequests`** registers the seven methods with their `acpwire` names.
5. **The barrier.** Change `responseBarrier` to the variadic form in Contracts: it calls each non-nil fencer's `fenceSession` in order. The existing calls (`host.go:397`, `ordering_test.go:153`, `status_test.go:125`, `conformance_test.go:95`) compile unchanged: their untyped nils become nil interfaces, which it skips. Never pass a typed nil manager.
6. **Host wiring.** In `registerHandlers`, after the status manager, build the `RequestManager` (`Lookup` is `manager.Service`, `Notify` is `h.sink.Notify`, `Marshal` is `srv.marshalClient`), call `registerRequests`, and pass `requests` to `responseBarrier` after `statuses`. Call `requests.Close()` in the cleanup, after `statuses.Close()`.
7. **The method table.** In `methods.go`, change the rows: `Pending` and `ReconcileAccess` to `session/pending`, `AnswerApproval` to `session/answer_approval`, `AnswerQuestion` to `session/answer_question`, `AnswerSkillGate` to `session/answer_skill_gate`, `AnswerSudo` to `session/answer_sudo`, `AnswerAccess` to `session/answer_access`, and mark all eight, `Rollback` included, existing. Rewrite their `ACP:` paragraphs in `acpclient.go`:
   - `Pending`: "ACP: session/pending (exists). A pending_requests update refreshes it; the first read subscribes."
   - `AnswerApproval`, `AnswerQuestion`, `AnswerSkillGate`, `AnswerSudo`, `AnswerAccess`: "ACP: session/answer_… (exists)." `AnswerSudo`'s adds: "The password travels only in its params, and the client zeroes the caller's slice when the call returns."
   - `ReconcileAccess`: "ACP: session/pending (exists). The agent settles a request that asks for nothing before each pending_requests update, and this reads the access request the update carries."
   - `Rollback`: "ACP: session/rollback (exists)."

   Keep the rest of each doc. `TestACPClientMethodTableMatchesTheServer` and `TestMethodDocsNameTheirACPMethod` hold the table and the docs together.
8. **Tests** in `requests_test.go`, serving a `RequestManager` on a pipe with `startTestAgent`, `registerInitialize`, `registerRequests` and `responseBarrier(requests)`, to a raw `rpc.Conn` whose `Notify` records each `pending_requests` update, as `startStatusAgent` (`status_test.go:94`) records `session_status`. The service is `service.New` over `session.New(config.Default(), t.TempDir(), …, session.Persistence{SessionID: "s1"})`.
   - `TestTheFirstPendingArrivesBeforeTheResponse`: with an approval registered on the state, the first `session/pending` returns after exactly one `pending_requests` update for `s1` was recorded, and its state equals the result.
   - `TestANewRequestIsSentWithinTheInterval`: register an approval on the state directly; an update listing it arrives within 500ms. Three quiet intervals then send nothing.
   - `TestAnAnswerReturnsAfterItsUpdate` (V3): with every `session/update` frame slowed by 50ms, as `ordering_test.go` slows them, `session/answer_approval` returns only after the recorded updates include one with no approval. The same holds for a question, a skill gate and a sudo prompt.
   - `TestAnAnswerToNothingOpenIsRequestSettled`: an answer for an unknown key returns an error whose data carries `CodeRequestSettled`.
   - `TestASatisfiedAccessRequestIsSettledByTheForwarder`: an access request whose runtime reader reports it satisfied leaves the update's `Access` empty and the state's request settled, without any client call.
   - `TestAClientWithoutTheCapabilityGetsNoPendingUpdates` (V6): `session/pending` answers, an answer works, and no `pending_requests` update is recorded within 200ms.
   - `TestRequestsRefuseBadParams`: a missing `sessionId` is invalid params for each method; an unknown session is the unknown-session error.
   - `TestTheRequestForwarderStopsWhenTheSessionGoes`: make `Lookup` report false; within 500ms the forwarder has stopped and its fence is gone (a barrier for `s1` returns at once).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/ -run 'Pending|Answer|Request|ACPClientMethodTable|Barrier|AResponseFollows|Status|Notice' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/ -run 'Pending|Answer|Request' -race -count=3
```

Expected: the new tests pass, three times under `-race`; plan 3a's and 3b's ordering and status tests pass on the variadic barrier; the method-table tests pass with the eight rows existing.

Mutation checks:
- Drop `requests` from the test server's `responseBarrier`. `TestAnAnswerReturnsAfterItsUpdate` and `TestTheFirstPendingArrivesBeforeTheResponse` fail. Restore it.
- Make `pendingState` skip `ReconcileAccess`. `TestASatisfiedAccessRequestIsSettledByTheForwarder` fails. Restore it.

## Task 3: The turn leaves a marshal client's requests to the mirror

**Goal:** for a marshal client, no turn and no child collector sends `session/request_permission` or `session/request_question`, or settles a request on the client's behalf.

**Files:** `internal/acp/turn.go`, `internal/acp/child_approvals.go`, `internal/acp/marshal_requests_test.go` (new).

**Steps:**

1. **`isMarshal`.** Add it to `turn.go` as in Contracts: `m.marshal != nil && m.marshal()`. Make `stackIntervals` (`stack.go:62`) use it.
2. **The forwarder.** In `forward` (`turn.go:807`), right after the block that sends the event's `session/update` and before the approval block (`:831`), return when `m.isMarshal()`, with a comment naming R5: the request mirror shows the request, and nothing here may answer it, deny it or fail it.
3. **The child collector.** At the top of `ensureChildApprovals` (`child_approvals.go:46`), after its nil checks, return when `m.isMarshal()`.
4. **Tests** in `marshal_requests_test.go`:
   - `TestATurnAsksAMarshalClientNothing`: as `TestPromptTurnBridgesPendingApprovalToPermissionClient` runs a turn, with `Marshal: func() bool { return true }`, a `fakePermissionClient` and a `fakeQuestionClient`. The runner publishes an approval, a question and a skill gate, each with its own buffered channel, then waits 300ms. No decision arrives on any channel, and neither client was called.
   - `TestATurnStillAsksAClientWithoutTheCapability`: the same turn with `Marshal` nil: the permission client is called for the approval and the skill gate, and the question client once.
   - `TestTheChildCollectorLeavesAMarshalClientsApprovals`: build `newChildApprovalHarness` with a recording permission client, set the turn manager's `marshal` to report true, then start the collector and register a child approval. Within 300ms the client is not called, and the child's approval is still open.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/ -run 'MarshalClient|WithoutTheCapability|ChildApproval|PromptTurn|Forwarder|Permission|Question|SkillGate|Access' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/ -count=1
```

Expected: the new tests pass, and every existing permission, question, skill-gate, access and child-approval test passes unchanged: none of them declares the capability.

Mutation check: remove the early return from `forward`. `TestATurnAsksAMarshalClientNothing` fails. Restore it.

## Task 4: The client's requests

**Goal:** `rpc.Session` mirrors the open requests and sends the answers.

**Files:** `internal/acpclient/rpc/requests.go` (new), `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/requests_test.go` (new).

**Steps:**

1. **The mirror.** The session holds one `PendingState` entry, absent until the first read, with an updated mark as the sheet's and the status's mirrors have.
   - `Pending()`: an entry returns its `Requests` without I/O. Otherwise create the entry, send `session/pending` with `PendingParams` under a `pendingCallTimeout` context, and install the result unless an update arrived during the call, as `Attach` does. On error remove the entry and return an empty `PendingRequests`; the next read asks again.
   - `ReconcileAccess(ctx)`: read as `Pending` does (fetching with `ctx` on a first read), and return the entry's `Access` and whether it is set.
2. **Routing.** `routeNotify` gains a case for `acpwire.UpdatePending`: decode `PendingUpdate` and hand its state to the attached session it names. The session replaces its entry's state, marks it updated, and taps `Changed`. An update for a session that has not read its requests is dropped.
3. **The answers.** Each sends its method with its params type and returns the error, as `SetNotice` does. `AnswerAccess` decodes the `AccessResult`. `AnswerSudo` zeroes `answer.Password`, byte by byte, after the call returns, whatever its outcome. `Rollback` sends `PendingParams`.
4. **Tests** in `requests_test.go`, over the scripted agent of `session_test.go`:
   - `TestPendingFetchesOnItsFirstRead`: the first `Pending()` sends `session/pending` and returns the result's requests; the second sends nothing.
   - `TestPendingInstallsTheUpdateThatPrecedesTheResponse` and `TestPendingInstallsTheResultWhenNoUpdateCame`: the two rules plan 3b's tests pin for the status, for the requests.
   - `TestAPendingUpdateReplacesTheRequestsAndFiresChanged`.
   - `TestReconcileAccessReadsTheMirror`: an update whose `Access` is set makes `ReconcileAccess` return it with no call; one without makes it report false.
   - `TestAnswersSendTheirMethods`: each answer sends its method with the session's ID, the key and the answer; `AnswerAccess` returns the agent's result; a coded error comes back with `acpclient.IsCode` true.
   - `TestAnswerSudoZeroesThePassword`: after `AnswerSudo` returns, the caller's password slice is all zeros, and the frame the agent read carried the password's base64 encoding.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included, three times under `-race`; plan 3b's session tests pass unchanged.

Mutation check: make the router skip `pending_requests`. `TestAPendingUpdateReplacesTheRequestsAndFiresChanged` fails. Restore it.

## Task 5: The conformance harness and the secret

**Goal:** the request cases run against the service and against the client over a pipe, and agree (V1); a sudo password crosses the wire in one frame only (V5); an answer stays inside R10's budget.

**Files:** `internal/acp/conformance_test.go`, `internal/acp/conformance_requests_test.go` (new), `internal/acp/service/requests_test.go`, `internal/acp/service/answers_test.go`.

**Steps:**

1. **The seam.** Add the eight methods in Contracts to `seam`.
2. **The pipe arm.** In `openPipeSeamTB`, after the status manager, call `serveRequests(tb, srv, served, sessionID)`, and add its manager to `responseBarrier` after `statuses`. `serveRequests` builds a `RequestManager` whose `Lookup` returns the served service for the served ID, with `Notify: srv.Notify` and `Marshal: srv.marshalClient`, calls `registerRequests`, and closes the manager with the test.
3. **The table.** `TestRequestConformance` runs every case once per arm, as `TestStatusConformance` does. Move these tests from the service's tests into it, deleting them there, and copy the helpers they use (`approvalChan`, `registerApproval`, `approvalDecision`) into the new file:
   - "the session's requests are listed" (`TestPendingListsTheSessionsRequests`);
   - "a running subagent's approval is listed" (`TestPendingAddsTheRunningSubagentsApprovals`);
   - "a forced approval is marked" (`TestPendingMarksForcedApprovals`);
   - "the state's access request is listed" (`TestPendingListsTheStatesAccessRequest`);
   - "once and deny" (`TestAnswerApprovalOnceAndDeny`);
   - "a child's approval settles on the child" (`TestAChildsApprovalSettlesOnTheChild`);
   - "an answer to a settled approval is request-settled" (`TestAnAnswerToASettledApprovalIsRequestSettled`);
   - "a question's answers are delivered" (`TestAnswerQuestionDeliversTheAnswers`);
   - "a skill gate's choice is recorded" (`TestAnswerSkillGateRecordsTheChoice`);
   - "an access answer resolves through the runtime" (`TestAnswerAccessResolvesThroughTheRuntime`);
   - "a rollback restores and logs" (`TestRollbackRestoresAndLogs`).

   Two new cases join them: "the access request to present", where `ReconcileAccess` returns a registered request's ID, session and flags (`TestReconcileAccessReadsTheRuntime`'s assertions without its call count, which stays in the service's tests because the forwarder reconciles on every pass); and "a sudo answer reaches the prompt", where the decision on the prompt's channel is OK and carries the same password bytes, and a second answer is request-settled (the service's `TestAnswerSudoHandsThePasswordOver` stays where it is: it pins that in process the handler gets the very slice).

   Each case registers every request on the state before `open`, as plan 3b's cases set the status: the pipe arm's first read is what the agent sends then. A read after an answer is current on both arms, because the answer is a call. Keep each case's other assertions as they are.
4. **A difference is a bug, never the case's,** as in plan 3b: fix the wire type or the service so both arms agree. If the fix would change what the TUI sees today, stop and report it instead.
5. **The secret (V5).** `TestASudoPasswordCrossesTheWireOnce`: serve the pipe arm with the agent's stream wrapped so every byte it reads and writes is copied into a `syncBuffer`, and the server's logger writing into another. Register a sudo prompt, read `Pending`, and answer it with the password `p3-sudo-canary-7f3a`. Then the password's base64 encoding (JSON's form for `[]byte`) appears exactly once in the bytes the agent read, inside the `session/answer_sudo` request, and neither the password nor its base64 encoding appears in the bytes the agent wrote (no update and no response carries it) or in the log. A `pending_requests` update listing the sudo prompt was written. Copy what `openPipeSeamTB` does, adding the two wrappers, rather than changing it.
6. **The budget (R10).** `TestAnswerCallBudget`: the pipe arm over `largeStackSession`. For each of 30 runs, register an approval on the state, wait until `Pending` lists it, then time `AnswerApproval` with a once answer. Each call includes its barrier and its update. The median must be under 1ms. Log it with `t.Logf` for the Implementation record. Skip under `-short` and with the race detector, as `TestNoticeCallBudget` does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/ -run 'TestRequestConformance|TestASudoPasswordCrossesTheWireOnce|TestAnswerCallBudget|TestStatusConformance' -count=1 -v 2>&1 | tail -60
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-5-suite go test ./internal/acp/ -run 'TestRequestConformance|TestASudoPasswordCrossesTheWireOnce' -race -count=3
```

Expected: every case passes on both arms, three times under `-race`; the secret test passes; the budget test passes and logs its median; plan 3b's status cases still pass with the request manager in the pipe arm; the service package passes without the moved tests.

Mutation check: make the agent's `AnswerSudo` handler echo the params in its error. `TestASudoPasswordCrossesTheWireOnce` fails. Restore it.

## Task 6: The docs and full verification

**Goal:** the spec records what plan 5 built and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 3b did. Expect, if plan 4b has not merged: 110 rows, 47 of them an existing ACP method (eight more: `Pending`, the five answers, `ReconcileAccess` and `Rollback`), 61 new in P3 carried by 56 distinct new methods, and 2 carrying none; and 82 registered methods (75 plus the seven). If plan 4b merged first, measure the merged tree and record what it shows. Update the section's numbers, and its "What the agent asks today" bullet: a marshal client is asked nothing; it reads `session/pending`.
2. **The Implementation record.** Add a "Plan 5 — agent requests" entry after plan 3b's (after plan 4b's, if it merged first). It names: the request manager, its forwarder and `requestInterval`; `pendingState` and the two halves of the update; the seven methods; the barrier's new fence and its variadic form; the turn's and the child collector's early returns for a marshal client; `rpc.Session`'s mirror, its first-read fetch, the answers and the zeroed password; the harness's cases; the secret test; and the budget's median.
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "requests" in its list, after "status".
4. **Main may have moved.** If plan 4b merged while this branch ran, merge `main` into the branch before the full verification, and resolve the conflicts as "Integration with plan 4b" says.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-5-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-5-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, and now `TestAnswerCallBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Integration with plan 4b

Plan 4b adds the sheet manager and the transcript reads. Whichever branch merges second merges `main` first and resolves these, keeping both sides:
- **`responseBarrier`'s declaration** is the same in both plans, so it merges cleanly. If it does not, keep one copy.
- **`host.go`'s `registerHandlers`:** both build a manager after the status manager, both append it to the `responseBarrier` call, and both close it in the cleanup. The barrier's order is `turns, works, watches, statuses, sheets, requests`.
- **`conformance_test.go`:** both add methods to `seam` and a line to `openPipeSeamTB` that serves their handlers and adds their manager to its `responseBarrier` call.
- **`rpc/session.go`'s `routeNotify`:** both add a case for their update kind.
- **`acpclient/methods.go`:** plan 4b's `Callers` and `ReleaseSubagent` rows sit next to this plan's `Pending` row.
- **The spec:** both add an Implementation record entry and re-measure the counts. Record the merged tree's counts.

## Self-review

- **Spec coverage.** R3's `Pending` row (first read, kept current at most every 50ms and before any response): Tasks 2 and 4. R5: the mirror (Task 2), the answers as calls (Tasks 2 and 4), sudo (Tasks 4 and 5), `ReconcileAccess` from the mirror (Tasks 2 and 4), the turn and child collector leaving a marshal client alone (Task 3), `Rollback` (Task 2). R2 and V3: `TestAnAnswerReturnsAfterItsUpdate`. R8 and V5: `TestASudoPasswordCrossesTheWireOnce`. R9 and V6: `TestAClientWithoutTheCapabilityGetsNoPendingUpdates` and `TestATurnStillAsksAClientWithoutTheCapability`. R10: `TestAnswerCallBudget`. V1: Task 5. V8: Task 6.
- **What could go wrong.**
  - **A request nobody answers.** For a marshal client the turn no longer denies for want of a bridge. The request stays in the state until the client answers it or the turn is cancelled, which is what the TUI does in process.
  - **The forwarder reconciles too eagerly.** It settles only a request that asks for nothing, through the runtime's own reader, as the TUI's per-update call did; `TestASatisfiedAccessRequestIsSettledByTheForwarder` pins it.
  - **The password leaks into a frame or a log.** The secret test reads every byte both ways and the server's log.
  - **A typed nil in the variadic barrier.** Every caller passes managers that exist or an untyped nil; step 5 of Task 2 says so.
  - **Plan 4b.** The shared files and their resolutions are listed above.
