# P5 plan 4a: fleet requests reach the bridge, and leave it — implementation plan

Date: 2026-11-14. Status: ready for review.

Base: `origin/main` at `cef1e353`. Every anchor below was checked there. The
main checkout's local `main` was behind it when this was written: pull first.

- **The baseline.** The full suite at `cef1e353` ran 122 packages ok. Only
  `internal/acp` failed, with `TestStackPatchApplyBudget` and
  `TestStackRoundTripBudget`. Both are among the 12 wall-clock budget tests
  that CI skips (`docs/testing-ci-audit.md`), so the suite below skips them too.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`. This plan's are F5's "Answers" bullet, the
  finding that replaces "Not yet checked", Migration order item 4a, the Design
  table's `internal/acp` and `internal/app/session` rows, and the "Plan 4a"
  record placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decision 5's
"Answers", F5's "Answers", Migration order item 4a.
Previous plans: [P5 plan 2, the inbox](2026-11-13-p5-2-inbox-plan.md).
Sibling plans: [plan 3, the fleet client](2026-11-14-p5-3-fleet-screen-plan.md)
and [plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Why this plan exists

The spec left one thing unchecked: whether an answer from a session's viewer
settles the bridge's brokered request for the same approval. Checking it found
two faults, and the first is larger than the spec's question.

1. **No real agent's approval or question reaches the bridge today.**
   - The agent numbers its outbound requests as strings, `"marshal-out-N"`
     (`internal/acp/server.go:228`).
   - The bridge's child reader decodes a child-initiated request's ID into an
     `int`, and drops the frame when that fails (`web/bridge/child.go:502`).
   - So every `session/request_permission` and `session/request_question` from
     a real `marshal acp` child is dropped. The agent then waits forever,
     because neither side has a timeout.
   - Proof, by a probe run while authoring: a fleet agent on a project child,
     with the confirmation rule `reattach_test.go` uses, made its one tool
     call and then sat in `running` for 40s with no pending request. An
     instrumented reader logged `session/request_permission id="marshal-out-1"`
     being dropped.
   - The bridge's own tests never saw it: their fake child sends the integer
     ID 9000 (`web/bridge/child_test.go:219`).
   - This is also why plan 2's manual run never saw an approval: both of its
     risky turns "hung".
2. **When the viewer answers first, the bridge never hears.**
   - The agent forwards a bridge-started turn's prompt with
     `PermissionBridge.Request` (`internal/acp/turn.go:926`). The RPC's context
     ends with the turn, or when steering interrupts the prompt's operation
     (`promptRPCContext`, `internal/acp/questions.go:106`).
   - A viewer's `session/answer_approval` settles the session's canonical
     prompt (`service/answers.go:36`). Nothing ends the forwarded RPC, so the
     bridge keeps listing the request.
   - Even when the RPC's context does end, `Server.Request` only forgets its
     waiter (`server.go:277`). It never tells the peer, so the bridge's entry
     stays until someone answers it there.
   - The other direction already holds. The bridge's answer settles the prompt
     through `pending.Respond`. The viewer's mirror then drops it, and a late
     viewer answer gets `CodeRequestSettled`.

## Goal

A fleet agent's approvals and questions reach the bridge. Whichever path answers
first, the bridge's answer or a viewer's, settles the request on both paths:

- The bridge stops listing a request the agent settled elsewhere, within the
  inbox's one-second recompute.
- A later answer through the bridge gets 410, which is what
  `POST /api/permissions/{id}` already answers for a stale request.
- A later answer through a viewer gets `CodeRequestSettled`, as today.

## Non-goals

- **No change to who may answer.** The fleet answers through the bridge and a
  viewer through its mirror (decision 5).
- **No agent attach.** That is plan 4b. This plan's end-to-end tests dial the
  project child's viewer socket directly, which only the socket's owner can do.
- **No wall-clock timeout on any request.** A person deciding is not a hung
  request (`registry.go:772`).
- **No change to `PermissionBridge`'s own decision mapping,** or to how a
  typed access request is settled.

## Assumptions

- Every Go test command below runs with `TMPDIR=/private/tmp/p5-4a-suite`.
  Create the directory first: `test/daemon` binds unix sockets under it.
- **The bridge's child** (`web/bridge/child.go`):
  - `readLoop` (`:480`) runs on one goroutine. A frame with a method and an ID
    is a child-initiated request. It is decoded as an `int` (`:502`) and
    handled on its own goroutine by `answerRequest` (`:565`). A frame with a
    method and no ID goes to `OnNotification`.
  - `answerRequest` writes the result, or the error as `-32000`, with the
    `int` ID.
  - `OnRequest` (`:101`) has one setter, `Registry`'s constructor
    (`registry.go:158`). No test sets it or calls `handleChildRequest`.
- **The registry** (`web/bridge/registry.go`):
  - `handleChildRequest` (`:712`) ignores its `id`.
  - `awaitPermission` (`:723`) and `awaitQuestion` (`:790`) register the
    request, defer its removal, and wait for the HTTP answer or the session's
    context (`:785`, `:848`).
  - `ResolvePermission` and `ResolveQuestion` answer `ErrGone` for an ID that
    is no longer waiting, and the routes map that to 410 (`http.go:331`).
- **The agent** (`internal/acp`):
  - `Server.Request` (`server.go:213`) sends `"id": "marshal-out-N"`.
    `deliverOutbound` (`:676`) matches a response by its unquoted ID.
  - `$/cancel_request` is `acpwire.MethodCancelRequest`. Its params are
    `acpwire.CancelRequestParams{ID json.RawMessage}`. The agent handles it
    inbound (`handleCancelRequest`, `:436`), and the TUI's client sends it
    (`internal/acpclient/rpc/conn.go:208`).
  - `Server.Notify` (`:191`) sends a notification.
  - The prompt forwarders are four:
    - the approval goroutine at `turn.go:918`;
    - the question goroutine at `:956`;
    - the skill-gate goroutine below it;
    - the access collection in `resolveAccessRequest` (`:1152`).

    A child's approvals go through `childApprovals.collect`
    (`child_approvals.go:166`). Each builds its RPC context with
    `promptRPCContext`.
  - `permissions_test.go:152`, `TestPromptTurnBridgesPendingApprovalToPermissionClient`,
    drives a turn's approval through a fake `PermissionClient`. It is the model
    for this plan's turn tests.
- **The session** (`internal/app/session/prompts.go`):
  - A registered prompt's record has a `done` channel. `settlePromptLocked`
    (`:356`) closes it exactly once, on every settlement: an answer, a
    cancellation, a steering interruption.
  - `ClearPrompt` (`:655`) removes the record afterwards. `PromptContext`
    (`:639`) answers a cancelled context for a key with no record.
  - `lookupPromptLocked` finds a record by key, checking its kind and
    operation.
- **The end-to-end harness** (`test/daemon`):
  - `newDaemonHarness` (`attach_test.go:36`) runs a real local-profile bridge.
    Its project children are this test binary re-run as `acp --viewers`.
    Its state directory is the local `stateDir`, which the struct does not
    keep.
  - The child's viewer socket is `<stateDir>/v/<first 16 hex digits of
    sha256(root)>.sock` (`web/bridge/projectchild.go:63`).
  - `writeApprovalPolicy`, `trustProject` and `waitApproval`
    (`reattach_test.go:32`, `:53`, `:95`) make a shell call ask, and wait for
    the ask to reach a viewer.
  - `Fleet.Spawn` starts an agent on the project child.
    `Fleet.RegistryForSession` and `Registry.Prompt` prompt it, and
    `Registry.Prompt` blocks until the turn ends.

## Decisions this plan makes beyond the spec

F5's "Answers" bullet records them.

1. **The bridge keeps a child request's ID as the child sent it,** raw JSON,
   and answers with exactly those bytes.
   - A string ID and a number ID are both requests.
   - `OnRequest` no longer receives the ID: its one handler never used it.
2. **The agent tells the peer when it stops waiting.** When an outbound
   request's context ends before its answer, `Server.Request` sends
   `$/cancel_request` with that request's ID, then returns the context's
   error as today.
   - A connection that has closed sends nothing.
   - This is the protocol's own notification, the one the agent already
     honours inbound.
3. **The bridge honours `$/cancel_request` for a request the child sent it.**
   - Each child-initiated request runs under a context. `readLoop` registers
     it before it starts the handler, so a cancel that follows its request is
     never missed.
   - The cancel ends that context. The registry's wait then returns, its
     deferred cleanup removes the entry, and `answerRequest` answers `-32800`
     ("request cancelled"). The child ignores that answer, having forgotten
     the request.
   - A cancel naming no request in flight is ignored.
   - `$/cancel_request` never reaches `OnNotification`.
4. **A forwarded prompt's RPC ends when the prompt settles,** by any path.
   - `promptRPCContext` takes the prompt's settled channel as a third input.
   - Each forwarder passes `PromptSettled` for its key.
   - When the RPC fails and the prompt has settled, the forwarder returns
     quietly. It does not cancel the turn, answer the prompt or log a failure,
     as it already does for a steering interruption.
5. **Every way an agent waits on its client is covered:** approvals,
   questions, skill gates, access requests, and a child's approvals. A
   question answered elsewhere is never answered again with a fabricated
   Unanswered.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 3, 4a and 4b`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-14-p5-3-fleet-screen-plan.md` and
    `2026-11-14-p5-4b-open-agent-plan.md`.

  Another plan's executor may have committed them already. Leave the main
  checkout's other untracked work alone.
- Then branch: `git switch -c p5-4a-requests` from `main`, or create a
  worktree on that branch.
- **This plan runs beside plans 3 and 4b.** Its files are its own:
  - `web/bridge/child.go` and `registry.go`, and their tests;
  - `internal/app/session/prompts.go`;
  - `internal/acp`'s `server.go`, `turn.go`, `questions.go` and
    `child_approvals.go`;
  - in `test/daemon`, one new file and the harness's `stateDir` field in
    `attach_test.go`.

  Plan 3 adds a different new file in `test/daemon`. Plan 4b changes
  `web/bridge`'s attach and relay, which this plan does not touch.
- Run the tasks in order. Run each task's Verify and commit it as
  `p5-4a-requests: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The bridge's child (`web/bridge/child.go`, Task 1)

```go
// On Child:
	// OnRequest handles a child-initiated request (a frame with both a method
	// and an id). ctx ends when the child cancels the request with
	// $/cancel_request, or when this generation's pipe closes. The handler's
	// result or error is answered with the request's own id.
	OnRequest func(ctx context.Context, method string, params json.RawMessage) (result any, err error)

	// inbound is each child request in flight, by its id's compact JSON, so a
	// $/cancel_request can end the one it names. Guarded by inboundMu.
	inboundMu sync.Mutex
	inbound   map[string]context.CancelFunc

// errRequestCancelled is the handler's error when the child cancelled the
// request; answerRequest answers it as JSON-RPC -32800.
var errRequestCancelled = errors.New("bridge: the child cancelled the request")
```

### The registry (`web/bridge/registry.go`, Task 1)

```go
func (r *Registry) handleChildRequest(ctx context.Context, method string, params json.RawMessage) (any, error)
func (r *Registry) awaitPermission(ctx context.Context, params json.RawMessage) (any, error)
func (r *Registry) awaitQuestion(ctx context.Context, params json.RawMessage) (any, error)
```

### The session (`internal/app/session/prompts.go`, Task 2)

```go
// PromptSettled is closed once the prompt key names has settled, by an
// answer, a cancellation or a steering interruption. A key with an ID that
// names no live record is already settled, and gets a closed channel. A key
// with no ID (an unregistered legacy prompt) gets nil, which never fires.
func (s *State) PromptSettled(key PromptKey) <-chan struct{}
```

### The agent (`internal/acp`, Task 3)

```go
// questions.go: the RPC context ends when parent ends, when original ends, or
// when settled closes. A nil original or settled is ignored.
func promptRPCContext(parent, original context.Context, settled <-chan struct{}) (context.Context, func())

// questions.go: settledElsewhere reports whether settled is closed.
func settledElsewhere(settled <-chan struct{}) bool

// turn.go: promptSettledFor is PromptSettled for rt's state, nil without one.
func promptSettledFor(rt *TurnRuntime, key session.PromptKey) <-chan struct{}

// child_approvals.go: childPromptSettled is PromptSettled for owner, nil
// without one.
func childPromptSettled(owner *session.State, key session.PromptKey) <-chan struct{}
```

## Task 1 — the bridge brokers a real agent's requests, and honours their cancel

**Files:** `web/bridge/child.go`, `web/bridge/registry.go`,
`web/bridge/child_test.go`, `web/bridge/registry_test.go`.

1. In `readLoop`, a frame with a method and an ID is a child request,
   whatever JSON its ID is (decision 1).
   - Keep the raw ID. Its key is its compact JSON, made by the `compactID`
     helper `relay.go:306` already defines in the package.
   - Before starting the handler, create its context from a parent that ends
     with the generation, and store the cancel func in `inbound` under the key.
     This has to happen on the read goroutine, so a cancel read next finds it.
   - Then start `answerRequest` on its own goroutine with the context, the raw
     ID, the method and the params.
   - The generation's parent context is one `Child` already holds if it has
     one. If it holds none, add one created in `spawn` and cancelled where the
     generation's pipes close. Use the same place `failPending` runs for a
     dead generation.
2. Also in `readLoop`, a frame whose method is `$/cancel_request` and which
   has no ID:
   - decode its params' `id`, take its key's entry out of `inbound`, and call
     the cancel func;
   - ignore an ID that names nothing;
   - never pass the frame to `OnNotification`.
3. `answerRequest`:
   - It removes its own `inbound` entry when the handler returns.
   - It answers with the raw ID, byte for byte.
   - A handler error answers `-32000` with the error's text, as today. When
     the context was cancelled by the child, the answer is `-32800` with the
     message "request cancelled".
4. `OnRequest` takes the context and no ID (Contracts).
   - `handleChildRequest` passes the context to `awaitPermission` and
     `awaitQuestion`.
   - Each wait also returns when the context ends, with `errRequestCancelled`.
     Its deferred cleanup already removes the entry, so `Pending` stops
     reporting it.
5. Tests:
   - In `child_test.go`, teach the fake child (`:201`) two more methods. Keep
     every existing case.
     - `test/ask_permission_string` sends `session/request_permission` with
       the ID `"marshal-out-1"`.
     - `test/cancel_request` sends `$/cancel_request` for `"marshal-out-1"`.
   - The new tests:
     - `TestAChildRequestWithAStringIDIsHandled`: the handler runs, and the
       answer the fake reads back carries `"marshal-out-1"` exactly.
     - `TestAChildsCancelEndsItsRequest`: with a handler that blocks on its
       context, the cancel ends it, and the fake reads a `-32800` answer for
       that ID.
     - `TestACancelForNoRequestIsIgnored`: a cancel naming an unknown ID
       changes nothing, and reaches no `OnNotification`.
   - In `registry_test.go`:
     - `TestACancelledPermissionStopsBeingPending`: after the cancel,
       `Pending` reports nothing for the session, and `ResolvePermission`
       answers `ErrGone`.
     - `TestACancelledQuestionStopsBeingPending`: the same for a question and
       `ResolveQuestion`.

**Verify:**
- `go test ./web/bridge/ -run 'Child|Registry|Permission|Question' -count=1`
- `go test ./web/bridge/ -count=1 -timeout 20m`. A hang in
  `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` is the known intermittent one:
  rerun the package and record it.
- Mutation check: decode the ID as an `int` again. The string-ID test must
  fail. Restore it.

## Task 2 — the session says when a prompt has settled

**Files:** `internal/app/session/prompts.go`, and a new
`internal/app/session/prompt_settled_test.go`.

1. `PromptSettled` (Contracts):
   - Under `accessMu`, look the key up as `lookupPromptLocked` does.
   - A record answers its `done` channel.
   - No record answers a channel that is already closed: one shared closed
     channel is fine.
   - A key with an empty ID answers nil.
2. Tests, each over a fresh `State` with a registered approval, as the
   package's existing prompt tests build one:
   - `TestPromptSettledClosesOnAnAnswer`: `ResolveApproval` closes it.
   - `TestPromptSettledClosesOnACancellation`: a steering cancellation of the
     prompt's operation closes it.
   - `TestPromptSettledIsClosedForAnUnknownKey`;
   - `TestPromptSettledIsNilForAnUnregisteredPrompt`;
   - `TestPromptSettledStaysOpenWhileThePromptWaits`: it is not closed before
     any settlement.

**Verify:**
- `go test ./internal/app/session/ -run PromptSettled -count=1`
- `go test ./internal/app/session/ -count=1`

## Task 3 — the agent stops waiting out loud, and on any settlement

**Files:** `internal/acp/server.go`, `turn.go`, `questions.go`,
`child_approvals.go`, and their tests: `server_test.go`, `permissions_test.go`,
`questions_test.go` and `child_approvals_test.go`.

1. `Server.Request`: when its context ends before the answer, it does three
   things, then returns the context's error as today:
   - it forgets the waiter, as today;
   - it sends `$/cancel_request`, with `acpwire.CancelRequestParams` carrying
     the request's ID as a JSON string, through `Notify`;
   - it ignores a send error, which means the connection is going.

   A closed server sends nothing (decision 2).
2. `promptRPCContext` takes the settled channel (Contracts). Its context also
   ends when the channel closes, and its cleanup stops the watcher. Use one
   goroutine selecting on the channel and on the context's own end.
3. Each forwarder passes its prompt's settled channel. On an error, each
   checks `settledElsewhere` first and returns quietly when it is true:
   - **the approval goroutine** (`turn.go:918`): `promptSettledFor(rt,
     pa.Key)`. It does not cancel the turn.
   - **the question goroutine** (`:956`): `promptSettledFor(rt,
     pending.Key)`. It does not answer Unanswered.
   - **the skill-gate goroutine**: `promptSettledFor(rt, sg.Key)`. It does not
     cancel the turn.
   - **`resolveAccessRequest`** (`:1152`): `promptSettledFor(rt, accessKey)`.
     Its existing re-read of the request already returns nil for a request
     that settled.
   - **`childApprovals.collect`** (`child_approvals.go:180`):
     `childPromptSettled(src.Owner, src.Key)`. It does not deny.

   The steering checks stay as they are, after the settled check.
4. Tests:
   - `server_test.go`:
     - `TestACancelledOutboundRequestTellsThePeer`: a request whose context is
       cancelled before an answer makes the peer read `$/cancel_request` with
       that request's ID, and `Request` returns `context.Canceled`.
     - `TestAnAnsweredOutboundRequestSendsNoCancel`.
   - `permissions_test.go`, modelled on
     `TestPromptTurnBridgesPendingApprovalToPermissionClient`:
     `TestASettledApprovalEndsItsForwardedRequest`.
     - The fake `PermissionClient` blocks until its context ends, and records
       that it ended.
     - While it waits, the test settles the approval through the session's
       own answer path, as a viewer's `session/answer_approval` does.
     - The fake's context ends, and the turn goes on to its end, not cancelled.
   - `questions_test.go`: `TestASettledQuestionEndsItsForwardedRequest`, the
     same for a question. The question keeps the answer the session was given,
     not Unanswered.
   - `child_approvals_test.go`: `TestASettledChildApprovalEndsItsForwardedRequest`.
     The child's approval keeps the decision it was given, and no denial is
     delivered after it.

**Verify:**
- `go test ./internal/acp/ -run 'Cancel|Settled|Permission|Question|ChildApproval' -count=1`
- `go test ./internal/acp/ -count=1 -timeout 20m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget)$'`
- Mutation check: drop the settled channel from the approval goroutine's
  `promptRPCContext` call. `TestASettledApprovalEndsItsForwardedRequest` must
  fail. Restore it.

## Task 4 — both paths, against a real bridge

**Files:** a new `test/daemon/requests_test.go`, and `test/daemon/attach_test.go`
(the harness's `stateDir` field only).

1. `daemonHarness` keeps its state directory in a new `stateDir` field.
2. A helper `viewerOf(t, h, sessionID)` returns an `*attachment` on the agent's
   session over the project child's own viewer socket.
   - The socket's path is computed as `projectchild.go:63` computes it.
   - It dials with `rpc.Dial` and attaches with `Host.Attach`, as
     `daemonHarness.attach` does after its upgrade.
   - Plan 4b's agent attach replaces it in its own tests. This helper exists
     because that route does not exist yet.
3. Each test spawns a fleet agent on the harness's project.
   - Use `writeApprovalPolicy` and `trustProject` first, and a fixture whose
     first turn is the shell call `reattach_test.go` scripts, and whose second
     is a short text reply.
   - Prompt it through `RegistryForSession(...).Prompt` on its own goroutine:
     `Prompt` blocks until the turn ends.
4. The tests:
   - `TestAFleetAgentsApprovalReachesTheBridge`:
     - within 30s, `h.fleet.Snapshot()` shows the agent's `Pending` as an
       approval;
     - `h.fleet.ResolvePermission(id, Decision{Approved: true})` with its ID
       lets the turn finish: `Prompt` returns nil, and the fixture saw two
       completions.

     This is the probe this plan's finding came from, made a test.
   - `TestAViewersAnswerSettlesTheFleetsRequest`:
     - with the approval pending at the bridge, a viewer from `viewerOf`
       answers it with `AnswerApproval`;
     - within 3s, the snapshot shows no pending request for the agent, and
       `h.fleet.Inbox()` no longer lists it in `needsYou`;
     - `ResolvePermission` with the old ID answers `ErrGone`;
     - the turn finishes.
   - `TestTheFleetsAnswerSettlesTheViewersRequest`:
     - with a viewer attached and the approval in its mirror (`waitApproval`),
       the test answers it through `ResolvePermission`;
     - the viewer's `Pending().Approvals` empties within 3s;
     - `AnswerApproval` with the key the viewer saw fails with
       `acpclient.IsCode(err, acpwire.CodeRequestSettled)`;
     - the turn finishes.

**Verify:**
- `go test ./test/daemon/ -run 'Approval|Request' -count=1 -v -timeout 10m`
- `go test ./test/daemon/ -count=1 -timeout 15m`
- Mutation check: skip the `$/cancel_request` send in `Server.Request`.
  `TestAViewersAnswerSettlesTheFleetsRequest` must fail. Restore it.

## Task 5 — the docs, the record and full verification

**Files:** `cmd/webbridge/README.md` if it describes brokered requests, and
`docs/daemon/p5-fleet-screen.md`: the "Plan 4a" placeholder only.

1. `cmd/webbridge/README.md`: find where it describes approvals and questions
   reaching the bridge. If it does, add one sentence: a request the agent
   settles elsewhere, for example by a viewer's answer, leaves the bridge's
   list, and a later answer gets 410. If it describes neither, change nothing.
2. The manual run:
   - Build `webbridge` and `marshal`.
   - Run `./webbridge --profile local --marshal-bin $PWD/marshal` with its TCP
     listener on and a fixed token, in a scratch data directory, as plan 2's
     record did.
   - Spawn an agent on a scratch project whose project config asks before
     `rm -f marker.txt`, and prompt it to run that.
   - With `curl -N` on `GET /api/events?stream=fleet`:
     - an inbox delta moves the agent into `needsYou`;
     - `POST /api/permissions/{id}` answers it, and the turn ends.
   - Answering from a viewer first needs a TUI on the agent's session, which
     only plan 4b's agent attach gives. Record that this half was covered by
     Task 4's end-to-end test, not by hand.
3. Fill the "Plan 4a" placeholder in the spec's Implementation record:
   - what was built;
   - where the plan was short, and how that was filled;
   - what the manual run showed;
   - the verification, with each command's result.
4. Full verification, with each result recorded:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean, and so are `GOOS=linux go vet ./web/...` and
     `GOOS=windows go vet ./web/bridge/...`;
   - `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 30m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'`,
     which is CI's command, with every failure named;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then
     `go test ./test/usability/... -run TestScripted -timeout 5m`.

**Verify:** the commands above, each recorded with its result.

## Done when

- A real agent's approvals and questions reach the bridge, and its answer
  reaches the agent.
- An answer on either path settles the request on both. The bridge drops a
  request the agent settled elsewhere, and a late answer through it gets 410.
- Task 4's three end-to-end tests pass, and the record is filled.
