# P5 plan 4c: a viewer's turn reaches the fleet — implementation plan

Date: 2026-11-15. Status: ready for review.

Base: `origin/main` at `bcceadab`, where plans 3 and 4a are merged. Every
anchor below was checked there.

- **Plan 4b is running beside this plan** and was not on `main` when this was
  written. Tasks 1 to 4 do not need it. Task 5 does: it tightens plan 4b's W4
  test, so it waits for plan 4b on `main` (see Execution).
- **The baseline** is CI's suite command at `bcceadab`, run when this plan was
  written. Its result is in this plan's record entry in the spec, under
  "Plan 4c".
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`: F5's "Decided with the user (2026-11-15)"
  block that replaces the open "A viewer's own turns" bullet, F5's "Decided
  by plan 4c" bullets, Findings, Migration order item 4c, the Design table and
  the "Plan 4c" record placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decision 5, F5,
Migration order item 4c, Test W4.
Previous plans: [plan 4a, requests](2026-11-14-p5-4a-requests-plan.md), whose
settlement this plan relies on.
Sibling plans: [plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md),
whose W4 Task 5 tightens; [plan 5a, the fleet's actions](2026-11-15-p5-5a-actions-plan.md),
whose cancel key reaches the runs this plan follows.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

A turn the user starts from an agent's session screen is a turn the fleet
sees, as if the bridge had started it:

- the fleet lists the agent as running for as long as the turn runs;
- the turn's tool calls move the Activity column, and its telemetry the CTX and
  Δ columns;
- the web UI's transcript of the agent shows the turn;
- the turn's approvals and questions reach the bridge, so the fleet lists them
  in needs you, and whichever side answers first settles them on both (plan
  4a);
- the fleet's cancel stops the turn.

The user chose this design on 2026-11-15 ("agent reports, full"). The daemon's
own TUI sessions stay out (H3).

## Non-goals

- **The daemon's TUI sessions.** The bridge follows only the sessions it
  created or loaded for a fleet agent. A TUI session on a project child is
  never followed, so H3 stays open.
- **A marshal client following a session.** A viewer already reads its runs
  through the session's status. The new method is for the bridge's own
  connection, and a marshal client is refused.
- **The web UI's composer.** The web UI shows a followed turn's updates and its
  end. Whether its composer disables itself during a turn it did not start is
  the web UI's own business, and unchanged here.
- **Containers.** A container's viewers arrive in plan 8. Following works on
  any child the bridge talks to, so nothing here changes for them.

## Assumptions

Each was checked at `bcceadab`.

- **The bridge's connection sees only its own turns.**
  - Its `TurnManager` subscribes to the session's events inside `runTurn`
    (`internal/acp/turn.go:682`), and only for a turn it started.
  - A marshal client's turn runs on the session's service
    (`internal/acp/service/turns.go:71`), through the viewer's control
    manager, and notifies only the viewer (`internal/acp/viewers.go:45`).
- **One run at a time, whichever connection started it.** Both paths take the
  state's run claim (`internal/app/session/session.go:1910`; `turn.go:725`;
  `service/turns.go:279`). A followed run and a bridge turn can therefore
  never overlap on one session.
- **The service knows its run.** `service.Session.Status` reports the run in
  flight and the last finished run, read under one lock
  (`internal/acp/service/service.go:371`). The run's ID is its stop group's
  turn ID (`internal/acpwire/runs.go:150`).
- **A session-lifetime subscriber already exists:** the child approval
  collector (`internal/acp/child_approvals.go:48`). It starts on the bridge's
  connection the first time any call looks the session up (`host.go:263`),
  skips marshal clients, and stops with the connection (`host.go:713`). The
  follower is built the same way.
- **The turn's forwarder is one closure** (`turn.go:858`). It projects each
  event (`eventToSessionUpdate`, `turn.go:397`) and drives approvals, typed
  access requests, questions and skill gates through the connection's bridges,
  each with plan 4a's settled channel (`turn.go:926`, `:974`, `:1017`,
  `resolveAccessRequest` at `:1126`). On a bridge failure it cancels the turn.
  Without a bridge it denies, or answers `Unanswered`.
- **The turn's end sends telemetry.** `finishTurn` (`turn.go:1596`) flushes
  the stack and sends `session_telemetry`, which is where the fleet's CTX and
  Δ come from (`web/bridge/fleetevents.go:84`).
- **`session/cancel` reaches only the connection's own turn**
  (`turn.go:2108`). It does nothing for a run on the service.
- **The bridge denies a request outside its own turn.**
  - `endTurn` clears the session's `accepting` flag (`web/bridge/registry.go:910`).
  - `awaitPermission` and `awaitQuestion` deny or decline at once when it is
    clear (`registry.go:734`, `:808`).
  - So a followed run's approval would be denied by the bridge, and the viewer
    would see it refused, unless the run opens that window first. This plan's
    ordering rule exists for this.
- **Frames from the child are read in order on one goroutine.** A notification
  is handled on the read goroutine before the next frame is read; a request is
  registered there and then handled on its own goroutine
  (`web/bridge/child.go:546`). A notification sent before a request is
  therefore applied before that request's handler runs.
- **The bridge's view of "running" is `Busy`.** `beginTurn` and `endTurn` set
  it around the bridge's own prompt (`registry.go:901`, `:910`). Every status
  read takes it from there (`fleet.go:1959`, `status.go:324`, `http.go:886`),
  and the inbox loop recomputes groups from it every second.
- **Where the fleet makes and restores agents' sessions.** Spawn tracks the
  new session as the fleet's (`fleet.go:1563`). `restoreSession` loads it, as
  the fleet's on a project child and plainly on an own child
  (`fleet.go:2079`, `:2087`). A child restart resumes every tracked session
  (`registry.go:998`).
- **The bridge's fake child** is `TestHelperProcess` (`web/bridge/child_test.go:39`),
  with scripted methods such as `test/emit_update` and `test/ask_permission`.
- **`test/daemon` can reach a project child's viewer socket directly**
  (`viewerOf`, `test/daemon/requests_test.go:42`), as plan 4a's tests do. This
  plan's end-to-end tests need nothing from plan 4b.

## Decisions this plan makes beyond the spec

1. **The opt-in is a method, `session/follow`,** sent by the bridge on its own
   connection for one session. Its params are the session's ID, and its
   result is an empty object.
   - Following is idempotent, and lasts until the connection closes or the
     session's runtime is replaced.
   - A viewer connection, or any marshal client, is refused with an invalid
     request error: "a marshal client reads its runs from the session's
     status".
   - An unknown session is a server error naming it, as the turn methods'
     lookups answer.
   - **Alternative:** a field on `session/new` and `session/load`. It would
     need repeating on every load and resume, and a method is testable alone.
2. **The agent reports only runs on the service.** The follower reports a run
   that the service reports as in flight. A turn the bridge prompted runs on
   the bridge's own `runTurn` and is never in the service's status, so the
   follower can never report the bridge's own turn back to it.
3. **One update kind, `followed_run`,** with a state of `started` or `ended`:
   - `started` carries the run's ID and kind;
   - `ended` carries the run's ID and outcome, and a failed run's message.

   The follower sends `started` before it forwards anything from the run, and
   `ended` after the run's last event and its telemetry.
4. **A followed run's updates are a turn's updates.** The follower forwards the
   run's events exactly as `runTurn` does: the same projection, the stack
   patches, and at the end the same telemetry. It uses one forwarder, shared
   with `runTurn`, so the two can never drift.
5. **A followed run's prompts go to the bridge, and failing quietly is safe.**
   - Approvals, typed access requests, questions and skill gates are forwarded
     through the connection's bridges, each with plan 4a's settled channel.
   - A bridge failure, or a missing bridge, never stops the run and never
     answers the prompt: the viewer that started the run still has the prompt
     in its mirror. That differs from `runTurn`, which cancels its own turn,
     denies without a bridge, or answers `Unanswered`.
   - Every prompt is still asked at most once per identity, with the same
     guards `runTurn` uses.
6. **The bridge's cancel stops a followed run.** `session/cancel` on the
   bridge's connection, with no turn of its own in flight on that session,
   stops the service's run if the connection follows it. This is what makes
   the fleet's `x` (plan 5a) work on a turn a viewer started.
7. **The bridge keeps the two kinds of turn apart.**
   - A session's record holds whether its own prompt is in flight and the ID
     of the followed run in flight.
   - `Busy` is true while either is, and so is `accepting`, as `beginTurn` sets
     it.
   - A followed run's `ended` clears them only if the bridge's own prompt is
     not in flight, so a late `ended` cannot hide a bridge turn that started
     straight after it.
   - An `ended` for a run other than the one recorded is ignored.
8. **The bridge follows every fleet agent's session,** after a spawn's
   `session/new`, after `restoreSession`'s load on either kind of child, and
   again for each followed session after a child restart's resume.
   - A child that answers method-not-found (an older `marshal`) is logged once
     per session and left as it was.
   - Any other failure is logged and does not fail the spawn or the restore:
     following improves what the fleet shows, and a spawned agent is still an
     agent without it.
9. **A followed run's end is a `turn_end` event,** as the bridge's own prompt's
   end is (`registry.go:506`), so the web UI's transcript and any other
   `turn_end` reader see it. The stop reason maps the outcome:
   - `done` and `gated` are `end_turn`;
   - `cancelled` is `cancelled`;
   - `failed` carries the run's message as the event's error, as a failed
     prompt does.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 4c, 5a, 5b and 6`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-15-p5-5a-actions-plan.md`,
    `2026-11-15-p5-5b-finish-plan.md` and `2026-11-15-p5-6-needs-you-plan.md`.

  Another plan's executor may have committed them already. Leave the main
  checkout's other untracked work alone.
- Then branch: `git switch -c p5-4c-viewer-turns` from `main`, or create a
  worktree on that branch.
- **Run Tasks 1 to 4 in order, beside plan 4b.** They touch:
  - `internal/acp`'s `turn.go`, `host.go` and a new `follow.go`, with their
    tests;
  - `web/bridge`'s `registry.go` and `fleet.go` (the spawn and restore call
    sites only), with their tests;
  - one new file in `test/daemon`.

  Plan 4b touches `web/bridge`'s `attach.go`, `relay.go`, `http.go` and one
  helper in `fleet.go`, far from this plan's call sites. Plans 5a and 6 do not
  touch any of these files.
- **Before Task 5, wait for plan 4b to be merged to `main`.** Merge `main`
  into this branch and rerun the Verify of Tasks 1 to 4.
- Run each task's Verify and commit it as `p5-4c-viewer-turns: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p5-4c-suite`.
  The unix-socket tests need a short path.

## Contracts

### The agent (`internal/acp`, Tasks 1 and 2)

```go
// follow.go
const methodFollow = "session/follow"

// followParams is session/follow's params.
type followParams struct {
	SessionID string `json:"sessionId"`
}

// updateFollowedRun is the session/update kind a follower sends when a run
// it follows starts or ends (decision 3).
const updateFollowedRun = "followed_run"

// followedRunUpdate is that update's body.
type followedRunUpdate struct {
	Kind    string             `json:"kind"`  // updateFollowedRun
	State   string             `json:"state"` // "started" or "ended"
	RunID   string             `json:"runId"`
	RunKind acpwire.RunKind    `json:"runKind,omitempty"` // on "started"
	Outcome acpwire.RunOutcome `json:"outcome,omitempty"` // on "ended"
	Error   string             `json:"error,omitempty"`   // a failed run's message
}

// runFollower reports one session's service runs to its connection
// (decision 2). It lives until stop, the connection's close, or the
// runtime's replacement.
type runFollower struct{ /* the executor's */ }

func (f *runFollower) start()
func (f *runFollower) stop()

// Follow is session/follow's handler.
func (m *TurnManager) Follow(ctx context.Context, params json.RawMessage) (any, error)

// CloseFollowers stops every follower this connection started.
func (m *TurnManager) CloseFollowers()

// TurnManagerConfig gains:
	// Service resolves a session's service, for the follower and for a
	// cancel that reaches a followed run. Nil disables both.
	Service func(sessionID string) (*service.Session, bool)
```

```go
// turn.go: runTurn's forward closure, as a value both runTurn and the
// follower build (decision 4).
type turnForwarder struct{ /* the executor's */ }

// forwarderOptions is what differs between the two.
type forwarderOptions struct {
	// failed is called when a bridge call fails for a reason other than
	// the prompt's own settlement. runTurn cancels its turn; the follower
	// logs.
	failed func()
	// followed marks the follower's forwarder: no prompt is ever answered
	// for lack of a bridge (decision 5).
	followed bool
}

func (m *TurnManager) newTurnForwarder(ctx context.Context, sessionID string, rt *TurnRuntime, opts forwarderOptions) *turnForwarder

// forward dispatches one session event to the connection.
func (f *turnForwarder) forward(ev pubsub.Event[session.Event])
```

### The bridge (`web/bridge`, Task 3)

```go
// registry.go

// Follow asks the agent to report id's runs from other connections
// (P5 F5, plan 4c). An agent without the method is left as it was.
func (r *Registry) Follow(ctx context.Context, id string) error

// sessionInfo gains:
	followed    bool   // Follow succeeded: a restart's resume follows it again
	ownTurn     bool   // the bridge's own prompt is in flight
	followedRun string // the followed run in flight; "" for none

// observeFollowedRun applies one followed_run update. It runs on the
// child's read goroutine, so it lands before any request read after it.
func (r *Registry) observeFollowedRun(params json.RawMessage)
```

## Task 1 — one forwarder for a turn and a followed run

**Files:** `internal/acp/turn.go`, `internal/acp/turn_test.go`.

1. Move `runTurn`'s `forward` closure (`turn.go:858`) into `turnForwarder`.
   - It holds what the closure captured: the session, the runtime, the
     context the prompts' RPCs derive from, the turn's projection, and the
     three once-per-identity guards (questions, skill gates, access
     requests).
   - `runTurn` builds one per turn with `failed` cancelling the turn and the
     subscription, as the closure's fatal paths do today, and calls
     `forward` wherever it called the closure, the fence's drain included.
2. Keep every behaviour the closure had for `runTurn`'s own forwarder:
   - a child actor's approval is skipped;
   - a marshal client's prompts are left to the mirror;
   - a notify error is fatal;
   - the no-bridge deny and `Unanswered`.
3. With `followed` set, the forwarder changes in exactly three ways
   (decision 5):
   - a missing bridge leaves the prompt unanswered, with a warning naming the
     session;
   - a failed question leaves the question unanswered, where `runTurn` answers
     `Unanswered`;
   - a failed notify calls `failed` rather than ending anything else.

   Settlement elsewhere and steering still end each RPC quietly, as in plan
   4a.
4. Tests:
   - `TestTheTurnsForwarderKeepsItsFailureRules`: with a bridge that fails, a
     turn's approval cancels the turn, a turn's question is answered
     `Unanswered`, and with no bridge a turn's approval is denied. This pins
     that the move changed nothing.
   - `TestAFollowedForwarderNeverAnswersForTheViewer`: with `followed` set, a
     failing bridge leaves the approval and the question pending in the
     state, and so does a missing bridge; `failed` is called for the failure.

   Both build the forwarder over a `TurnRuntime` with a real `session.State`,
   as `child_approvals_test.go`'s harness builds its runtime.

**Verify:** `TMPDIR=/private/tmp/p5-4c-suite CGO_ENABLED=1 go test ./internal/acp/ -count=1 -timeout 20m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget)$'`.

**Mutation check:** make the followed forwarder answer `Unanswered` on a
failed question; `TestAFollowedForwarderNeverAnswersForTheViewer` must fail.
Restore.

## Task 2 — the agent follows a session's service runs

**Files:** `internal/acp/follow.go` (new), `internal/acp/follow_test.go` (new),
`internal/acp/turn.go`, `internal/acp/host.go`.

1. `Follow` (decision 1):
   - refuses a marshal client, with the sentence in decision 1;
   - resolves the session through the turn manager's `Lookup`, which also
     starts the child collector, as every lookup on this connection does;
   - starts one follower per session and runtime: a second call for the same
     runtime does nothing; a replaced runtime stops the old follower and
     starts a new one, as `ensureChildApprovals` does (`child_approvals.go:48`).
2. The follower subscribes to the session's events for its whole life, as
   the child collector does, and reads the service's run on every event and on
   a ticker at the status forwarder's interval (`status.go:17`).
   - When the service reports a run whose ID differs from the one it is
     following, it sends `started` for it, then builds a fresh
     `turnForwarder` with `followed` set and a context that ends with the
     run.
   - While a run is followed, every root event goes through that forwarder,
     and the stack flushes on the turn's flush interval, as `runTurn`'s
     ticker does.
   - When the service reports no run, or a different one, the followed run
     has ended. The follower forwards what is still buffered, flushes the
     stack, sends the telemetry `finishTurn` sends, and then sends `ended` with
     the outcome and error from the service's last run.
   - An event that arrives while no run is followed is not forwarded. That is
     what keeps the bridge's own turn from being reported twice: its events
     are forwarded by its `runTurn`, and the service reports no run for it
     (decision 2).
3. The order is the contract (decision 3): nothing from a run before its
   `started`, and nothing from it after its `ended`.
4. `Cancel` (`turn.go:2108`): with no turn of its own for the session, and a
   follower for it reporting a run, it stops the service's run with the
   cancel's cause (decision 6).
5. In `registerHandlers` (`host.go:369`): register `session/follow` on every
   connection, refused on a viewer by `Follow`'s marshal check; pass
   `manager.Service` as the turn manager's `Service`; call `CloseFollowers`
   in the cleanup, beside `CloseChildApprovals` (`host.go:713`).
6. Tests in `follow_test.go`, each over a real service session on a real
   `session.State`, with a scripted runner and a recording notify:
   - `TestAFollowedRunIsReportedStartToEnd`: a run started on the service is
     reported as `started`, its tool call as a `tool_call` update, then its
     telemetry, then `ended` with outcome `done`, in that order.
   - `TestAFollowedRunsApprovalGoesToTheBridge`: the run's approval reaches
     the connection's permission client, and an answer there settles it in
     the state.
   - `TestAViewersAnswerEndsAFollowedRunsRequest`: answering the approval
     through the state first ends the forwarded RPC's context, and the run
     goes on.
   - `TestTheConnectionsOwnTurnIsNotFollowed`: a turn started through
     `PromptTurn` on the same connection is reported once, by the turn, and the
     follower sends no `started`.
   - `TestTheBridgesCancelStopsAFollowedRun`: a `session/cancel` for the
     session stops the service's run, which ends `cancelled`.
   - `TestAMarshalClientCannotFollow`: `Follow` on a marshal connection is
     refused with decision 1's sentence.
   - `TestFollowIsIdempotent`: two `Follow` calls report one run once.

**Verify:** Task 1's command.

**Mutation check:** send `started` after the run's first forwarded event; the
order assertion in `TestAFollowedRunIsReportedStartToEnd` must fail. Restore.

## Task 3 — the bridge follows its agents, and counts their runs

**Files:** `web/bridge/registry.go`, `web/bridge/fleet.go`,
`web/bridge/child_test.go`, `web/bridge/registry_test.go`.

1. `Follow` sends `session/follow` for a tracked session, through the
   registry's `call` (`registry.go:584`), so a method-not-found becomes
   `ErrUnsupported`. On success it marks the record `followed`.
2. `NewRegistry` (`registry.go:149`) chains onto the child's notifications,
   as it chains onto `OnRestart`, and hands each `session/update` whose kind
   is `followed_run` to `observeFollowedRun`.
3. `observeFollowedRun` (decision 7):
   - `started` records the run's ID and sets `Busy` and `accepting`;
   - `ended` for the recorded run clears the ID, and clears `Busy` and
     `accepting` unless the record's `ownTurn` is set, then emits `turn_end`
     with decision 9's stop reason;
   - an update for an untracked session, or an `ended` for another run, does
     nothing.
4. `beginTurn` and `endTurn` (`registry.go:901`, `:910`) set and clear
   `ownTurn`. `endTurn` leaves `Busy` and `accepting` set while a followed run
   is recorded, though the two cannot overlap today.
5. `resumeAll` (`registry.go:998`) clears both kinds of turn with `Busy`, and
   after each followed session's resume, follows it again.
6. In `fleet.go`, call `rt.reg.Follow` after the spawn's `TrackFleet`
   (`fleet.go:1563`) and after each branch of `restoreSession`'s load
   (`fleet.go:2079`, `:2087`), with decision 8's logging. The project child
   carries the client's sessions too, and they are never followed.
7. The fake child (`TestHelperProcess`) answers `session/follow` with an empty
   object, records it, and gains `test/followed_run`, which emits a
   `followed_run` update with the state and run ID it is given. A mode that
   answers `session/follow` with method-not-found stands for an older agent.
8. Tests:
   - `TestAFollowedRunMarksTheSessionBusy`: `started` makes `Sessions()`
     report the session busy, and `ended` clears it.
   - `TestAFollowedRunsApprovalIsBrokered`: a `started`, then a permission
     request on the same pipe, waits in `Pending` rather than being denied;
     with no `started` first, it is denied as today. This is the window the
     Assumptions describe.
   - `TestAFollowedRunsEndIsATurnEnd`: `ended` with outcome `cancelled` emits
     a `turn_end` event with stop reason `cancelled`, and one with `failed`
     carries the error.
   - `TestALateEndDoesNotHideTheBridgesTurn`: with the bridge's own prompt in
     flight, an `ended` for an earlier run leaves the session busy.
   - `TestAFleetAgentsSessionIsFollowed`: a spawn on the fake child sends
     `session/follow` for the new session, and a restart's resume sends it
     again.
   - `TestAnAgentWithoutFollowIsLeftAsItWas`: against the method-not-found
     mode, the spawn succeeds and the session is not marked followed.

**Verify:** `TMPDIR=/private/tmp/p5-4c-suite CGO_ENABLED=1 go test ./web/bridge/ -count=1 -timeout 20m`,
then `GOOS=linux go vet ./web/...` and `GOOS=windows go vet ./web/bridge/...`.

**Mutation check:** drop `accepting` from `observeFollowedRun`'s `started`;
`TestAFollowedRunsApprovalIsBrokered` must fail. Restore.

## Task 4 — a viewer's turn, against a real bridge

**Files:** `test/daemon/viewer_turns_test.go` (new).

Each test spawns a fleet agent on the project child and opens a viewer on its
session with `viewerOf`, as plan 4a's tests do, with the provider fixture
scripting the turn.

- `TestAViewersTurnShowsAsRunningInTheFleet`: the viewer prompts; the bridge's
  `GET /api/agents` reports the agent `running` and the inbox lists it under
  running while the turn runs; after it ends, the agent is idle and its
  changed-files count is the turn's.
- `TestAViewersTurnsApprovalReachesTheFleet`: the viewer's turn parks on an
  approval (`writeApprovalPolicy`, `test/daemon/reattach_test.go:32`); the
  bridge lists it as the agent's pending request and in `needsYou`; answering
  it through `POST /api/permissions/{id}` finishes the turn, and the viewer's
  mirror no longer lists it.
- `TestTheFleetCancelsAViewersTurn`: the viewer's turn is held mid-run by the
  fixture; `POST /api/sessions/{id}/cancel` ends it, and the viewer's status
  reports the last run as cancelled.

**Verify:** `TMPDIR=/private/tmp/p5-4c-suite CGO_ENABLED=1 go test ./test/daemon/ -count=1 -timeout 15m`.

**Mutation check:** comment out the fleet's `Follow` call after spawn;
`TestAViewersTurnShowsAsRunningInTheFleet` must fail. Restore.

## Task 5 — W4 checks the run through the fleet (after plan 4b)

**Files:** plan 4b's W4 test, `TestTheFleetOpensAnAgentAndLeavesItRunning`
(its Task 8), wherever that plan put it in `test/daemon`.

1. Where the test checks the agent is still running through a second viewer,
   check it through the fleet client's state instead: the agent's status is
   `running` when the fleet program runs again. Keep the second viewer's
   check beside it only if it proves something the fleet's state cannot.
2. If the merged test differs from plan 4b's text, follow its intent: the
   loop leaves the agent running, and the fleet now says so.

**Verify:** Task 4's command.

**Mutation check:** comment out the fleet's `Follow` call after spawn; the
tightened W4 must fail. Restore.

## Task 6 — the docs, the record and full verification

**Files:** `docs/daemon/p5-fleet-screen.md`, `AGENTS.md`.

1. In the spec, fill the "Plan 4c" record under Implementation record, as
   plan 4a's is written: what was built, where the plan was short and how
   that was filled, the manual run, and the verification table.
2. In `AGENTS.md`'s web control plane paragraph, add one sentence: the bridge
   follows each fleet agent's session, so a turn started from its session
   screen shows in the fleet and its requests reach the bridge.
3. **The manual run.** Run a real local daemon with a real model, as plan
   4a's manual run did. Spawn an agent, open it with `marshal attach` on its
   project child if plan 4b is not on `main` yet, or from `marshal fleet` if
   it is, and prompt it with a turn that asks for an approval. Record:
   - the fleet stream's deltas and the agent going to `running`;
   - the approval in the bridge's `GET /api/agents`;
   - answering it from whichever side was not used in Task 4, and the other
     side seeing it settled;
   - the agent back to idle.
4. Full verification:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean;
   - CI's suite command, with `TMPDIR=/private/tmp/p5-4c-suite`, passes;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` builds.

## Done when

- A turn started from an agent's session screen shows in the fleet as
  running, moves its Activity, CTX and Δ, and ends with a `turn_end`.
- Its approvals and questions reach the bridge, and the first answer from
  either side settles them on both.
- The bridge's cancel stops it.
- The daemon's TUI sessions are never followed.
- Every test named above passes, each mutation check fails as described, and
  CI's suite command passes.
