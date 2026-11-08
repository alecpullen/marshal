# P3 plan 6b: Turns and controls — implementation plan

Date: 2026-11-08. Status: ready for review.
Base: `main` at `725abd3c` (P3 plans 4b and 5 merged). Every anchor below was checked there. The full suite passed there: 119 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-base-suite`, `-timeout 20m`; `web/bridge` 85s).
- **It runs alongside P3 plan 7a** ([settings](2026-11-08-p3-7a-settings-plan.md)). Both plans add a manager to `internal/acp`, the same `marshalOr` helper, a fence to the response barrier, a case to the client's router, methods to the conformance harness's `seam`, and rows to `acpclient.Methods`. "Integration with plan 7a" (at the end) lists the conflicts this causes and how to resolve each one.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, "Decided with the user (2026-11-08)", "Methods P3 adds" (plans 6b, 6c, 7a and 7b), R3's `Work` row, R9's list, and Migration order items 6 and 7. Task 7 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (the service behind the handlers), R2 (read your writes, cancellation), R3 (the mirror: the `Work` row), R6, R9 (compatibility), R10 (budgets), "Decided with the user (2026-11-08)", Migration order item 6b, and tests V1, V3, V6 and V8.
Previous plans: [P3 plan 5, requests](2026-11-07-p3-5-requests-plan.md), [P3 plan 4b, the transcript read](2026-11-07-p3-4b-transcript-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client runs the session's turns and its controls over ACP on the session's service, and reads the session's work from a mirror the agent keeps current.
- For a marshal client, `session/prompt`, `session/steer`, `session/set_mode`, `session/set_effort`, `session/work_list`, `session/work_inspect` and `session/work_stop` are served by the session's service. Every other client reaches today's handlers.
- Eleven new methods: `session/stop`, `session/review`, `session/agent_run`, `session/suggest`, `session/verify_proposal`, `session/steering_pop`, `session/steering_clear`, `session/set_sudo_unlock`, `session/subagent_cancel`, `session/sdd_clear` and `session/watch_resume_take`.
- A `work_snapshot` update carries the service's work, whenever it changes, at most every 50ms, and before any call's response.
- A marshal connection binds no watch-resume hook, and releasing a service stops the run in flight.
- `rpc.Session` gains the twenty interface methods of the group, and the conformance harness gains their cases.

**Non-goals:**
- **No change for a client without the capability.** Its `session/prompt`, `session/steer`, `session/set_mode`, `session/set_effort` and `session/work_*` are today's, byte for byte, and so is `session/cancel`, which stays a notification for every client (R9).
- **No pipelines, no end flow, no sessions.** `StartSwarm`, `PlanPreflight`, `StartSDD`, `AnswerSDDGate`, `StopSDD`, `AuthorPlan`, the end flow, `Close`, `Host.NewSession` and `Host.ListSessions` are plan 6c's.
- **No change to the service's turn logic.** The slot, the stop group, the requeue, the codes and the watch-resume goal are the service's, unchanged. The handlers only decode and encode. The one service change is `Release` (Task 2).
- **No change to `internal/app/tui`.** The interface's methods keep their signatures; only their `ACP:` doc lines change.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-6b-suite`. Create the directory first: `mkdir -p /private/tmp/p3-6b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 7a uses `/private/tmp/p3-7a-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 7 likelier. Rerun a failure alone before treating it as new.
- **The service** (`internal/acp/service`). The turn calls are in `turns.go`: `Prompt` (`:42`), `Stop` (`:87`), `Review` (`:114`), `RunAgent` (`:137`), `Suggest` (`:160`), `VerifyProposal` (`:172`) and the one run path, `runTurn` (`:189`). `Prompt` puts a queued text back when the turn cannot start and returns `CodeBusy`, `CodeQuiescing` or `CodeNoRunner`; `Stop` marks the slot stopped, stops the turn's group with its cause and cancels the run. The controls are in `controls.go` (`Steer` `:20` through `TakeWatchResume` `:107`); `Steer` queues even when no turn runs. `SendNow` is `service.go:470`. The work calls are in `work.go`: `Work` (`:17`) returns the newest snapshot the session kept and whether it serves work at all, which it does only when its `Deps.Work` is set; `ListWork` (`:25`) codes a stale cursor as `CodeStaleWorkCursor`. `Release` (`events.go:228`) only calls `close` (`:217`): it marks the session closed and cancels its subscriptions, and stops no run.
- **ACP's handlers for the existing methods differ from the service** and stay as they are for other clients: `TurnManager.PromptTurn` (`turn.go:598`) runs its own turn slot; `Steer` (`turn.go:1691`) refuses a queued text with no turn in flight; `SetEffort` (`turn.go:1652`) wraps the setter's error as invalid params; `SetMode` (`interaction.go:179`) decodes `SetModeParams` strictly, with system access named `system_access`; the work handlers (`watch.go:368`, `:406`, `:429`) answer RFC 3339 times and code a stale cursor as invalid params. `session/cancel` is registered with `HandleNotification` (`host.go:360`), so a request carrying an id for it is answered `-32600` (`server.go:170`).
- **The transcript during a service turn.** The idle stack loop (`stack.go:473`) flushes every marshal interval while `HasActiveTurn` reports no turn, and `HasActiveTurn` reads only the `TurnManager`'s own slots. A marshal client's first `Stack` read starts the loop (`stack.go:307`), so a turn the service runs reaches the client's stack through it, at the 50ms cadence plan 4b kept.
- **The watch-resume hook.** `newTurnManagerFor`'s `Lookup` binds the runtime's `WatchResume` hook to `ResumeIfIdle` (`host.go:257`) on every lookup. `ResumeIfIdle` (`turn.go:1189`) starts a `TurnManager` turn. In process the TUI never binds the hook: it reads `Status().WatchResume`, takes the latch with `TakeWatchResume` and sends a `Prompt` whose `WatchResume` names the watch. `app.WatchResumeHook` (`runtime.go:1122`) has `Set` and `Invoke`, and its zero value works.
- **Closing a session.** `SessionManager.Close` (`session.go:523`) detaches the runtime, runs the turn canceller (which reaches only `TurnManager` turns), then `closeRuntimeOnce` (`:202`), which releases the service (`releaseService`, `:745`) before the runtime closes and quiesces.
- **Handlers and managers.** `RequestManager` (`internal/acp/requests.go`) is the model to copy: its handlers resolve a session's service with `Lookup` (`service`, `:228`), its forwarder starts on the first marshal read (`ensureForwarder`, `:250`), polls on a ticker and on its fence (`runForwarder`, `:275`), and sends only a changed encoding. In `registerHandlers` (`host.go:328`) the `TurnManager` is built at `:331`, the existing methods are registered at `:351` to `:360` and `:394` to `:396`, the barrier is installed at `:427`, and the cleanup closes the managers at `:585` to `:591`. `cancellable` (`server.go:443`) gives a handler a context that `$/cancel_request` ends.
- **Errors.** A missing `sessionId` is `invalidParamsError`; an unknown session is `serverErrorf("unknown session: %s", id)`. A service error is returned as it is, so an `*acpwire.Error`'s code travels as error data and `acpclient.IsCode` sees it on the client.
- **The client.** `routeNotify` (`rpc/session.go:205`) applies `session_status`, `sheet` and `pending_requests` to the attached session they name. `rpc/requests.go` is the model for a mirror read on first use (`pendingEntry`, `:69`; `installPending`, `:112`; `applyPending`, `:125`). The scripted agent in `rpc/session_test.go` is the model for the client's tests.
- **The interface** (`internal/acpclient/acpclient.go`): `SendNow` (`:219`), `Work` (`:360`) to `StopWork` (`:379`), `Steer` (`:385`) to `TakeWatchResume` (`:435`), `Prompt` (`:445`) to `VerifyProposal` (`:477`). Their rows in `methods.go` run from `Work` to `VerifyProposal`, plus `SendNow`.
- **Test models.** `TestAnswerApprovalOnceAndDeny`'s file (`conformance_requests_test.go`) shows a case table over both arms; `TestAnswerCallBudget` (`:841`) a budget over `largeStackSession` (`stack_wire_test.go:413`). `startTestAgent` (`client_core_test.go:25`) and `startStatusAgent` (`status_test.go:94`) serve managers to a raw `rpc.Conn`; `syncBuffer` (`ordering_test.go:27`) is a goroutine-safe buffer, and `ordering_test.go` slows `session/update` frames. `TestResumeIfIdleStartsTurnWhenIdle` (`turn_test.go:2986`) builds a `TurnRuntime` that `ResumeIfIdle` starts. The service's `fakeRunner` (`turns_test.go:22`) and `fakeWork` (`work_test.go:19`) are the fakes the moved cases use.

**Decisions this plan makes beyond the spec** ("Decided with the user (2026-11-08)" records the first; R3 and R9 the rest):

1. **A marshal client's turns and controls run on the service.** `marshalOr` sends a marshal client's call to the control manager, which calls the service, and every other client's to today's handler. Nothing in the turn is rewritten: the service's tests move into the conformance harness and pass on both arms.
2. **`Stop` is a new call, `session/stop`,** with the stop's cause. `session/cancel` is notification-only, and a notification can carry no answer for the barrier to follow. The method table's `Stop` row moves from `session/cancel` to `session/stop`.
3. **A marshal connection binds no watch-resume hook.** The TUI resumes a watch itself, so a bound hook would start the resume twice. The binding moves into a helper, `bindWatchResume`, which skips a marshal connection.
4. **The idle stack loop carries the transcript during a service turn.** Nothing changes in the loop; a test pins it.
5. **Releasing a service stops its run.** `Release` stops a run in flight with the cause `conversation ended` before it closes the session, so a runtime closing under a service turn does not wait out the run in its quiesce.
6. **The work mirror is the service's.** For a marshal client, `session/work_list`'s live view answers `WorkState` from the service and starts a forwarder that polls `Work()` every 50ms and on its fence, sending a `work_snapshot` update when the encoding changed. The service owns the conversation and revision guard and the "serves no work" answer, so the client reads exactly what the TUI reads in process. Other clients keep `work_state` and ACP's `Work*Wire` types (G4).
7. **The long calls are cancellable.** `session/prompt`, `session/review`, `session/agent_run`, `session/suggest` and `session/verify_proposal` take their context from `cancellable`, so the client's context ending stops the run, as in process. The controls are not.
8. **The prompt's params are ACP's, plus two optional fields.** The client sends its text as one text block (as `PromptParams.Text`'s comment says), with `queued` and `watchResume`. The marshal handler joins the text blocks' texts with a blank line, as `normalizePrompt` joins them, without `normalizePrompt`'s checks: the service decides what an empty text means, and a watch resume has none.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 7a's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 6b and 7a`. Plan 7a gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-6b-controls` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-6b-controls: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/turncalls.go`, new)

```go
// The turn, control and work methods, and the work update (P3 plan 6b).
const (
	MethodPrompt          = "session/prompt"
	MethodStop            = "session/stop"
	MethodReview          = "session/review"
	MethodAgentRun        = "session/agent_run"
	MethodSuggest         = "session/suggest"
	MethodVerifyProposal  = "session/verify_proposal"
	MethodSteer           = "session/steer"
	MethodSteeringPop     = "session/steering_pop"
	MethodSteeringClear   = "session/steering_clear"
	MethodSetMode         = "session/set_mode"
	MethodSetEffort       = "session/set_effort"
	MethodSetSudoUnlock   = "session/set_sudo_unlock"
	MethodSubagentCancel  = "session/subagent_cancel"
	MethodSDDClear        = "session/sdd_clear"
	MethodWatchResumeTake = "session/watch_resume_take"
	MethodWorkList        = "session/work_list"
	MethodWorkInspect     = "session/work_inspect"
	MethodWorkStop        = "session/work_stop"
	UpdateWork            = "work_snapshot"

	// SteerDeliveryNow is session/steer's delivery for Send now.
	SteerDeliveryNow = "now"
)

// ControlParams is the params of a call that names only its session:
// session/verify_proposal, steering_pop, steering_clear, sdd_clear and
// watch_resume_take.
type ControlParams struct {
	SessionID string `json:"sessionId"`
}

// TextBlock is an ACP text content block. Type is "text".
type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PromptCallParams is session/prompt's params from a marshal client: ACP's
// session and prompt blocks, plus PromptParams' queued text and watch resume.
// The client sends PromptParams.Text as one text block.
type PromptCallParams struct {
	SessionID   string       `json:"sessionId"`
	Prompt      []TextBlock  `json:"prompt"`
	Queued      bool         `json:"queued,omitempty"`
	WatchResume *WatchResume `json:"watchResume,omitempty"`
}

// StopParams is session/stop's params.
type StopParams struct {
	SessionID string `json:"sessionId"`
	Cause     string `json:"cause,omitempty"`
}

// ReviewCallParams is session/review's params.
type ReviewCallParams struct {
	SessionID string       `json:"sessionId"`
	Review    ReviewParams `json:"review"`
}

// AgentRunCallParams is session/agent_run's params.
type AgentRunCallParams struct {
	SessionID string         `json:"sessionId"`
	Run       AgentRunParams `json:"run"`
}

// SuggestParams is session/suggest's params.
type SuggestParams struct {
	SessionID   string `json:"sessionId"`
	LastMessage string `json:"lastMessage,omitempty"`
}

// SuggestResult is session/suggest's result.
type SuggestResult struct {
	Text string `json:"text"`
}

// SteerCallParams is session/steer's params: ACP's, whose delivery "now" with
// a turn and a request ID is Send now.
type SteerCallParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
	Delivery  string `json:"delivery,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// SteeringPopResult is session/steering_pop's result.
type SteeringPopResult struct {
	Text string `json:"text,omitempty"`
	OK   bool   `json:"ok"`
}

// EffortParams is session/set_effort's params.
type EffortParams struct {
	SessionID string `json:"sessionId"`
	Effort    string `json:"effort"`
}

// EffortResult is session/set_effort's result: the level asked for, and when
// it applies.
type EffortResult struct {
	Effort      string `json:"effort"`
	AppliesFrom string `json:"appliesFrom"`
}

// SudoUnlockParams is session/set_sudo_unlock's params.
type SudoUnlockParams struct {
	SessionID string `json:"sessionId"`
	On        bool   `json:"on"`
}

// SubagentCancelParams is session/subagent_cancel's params.
type SubagentCancelParams struct {
	SessionID  string `json:"sessionId"`
	SubagentID int64  `json:"subagentId"`
}

// SubagentCancelResult is session/subagent_cancel's result.
type SubagentCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// WatchResumeTakeResult is session/watch_resume_take's result.
type WatchResumeTakeResult struct {
	Name   string `json:"name,omitempty"`
	Repeat bool   `json:"repeat,omitempty"`
	OK     bool   `json:"ok"`
}

// WorkListParams is session/work_list's params. An empty or "live" view asks
// for the live snapshot.
type WorkListParams struct {
	SessionID string `json:"sessionId"`
	View      string `json:"view,omitempty"`
	Query     string `json:"query,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// WorkTargetParams is session/work_inspect's and session/work_stop's params.
type WorkTargetParams struct {
	SessionID string `json:"sessionId"`
	TaskKey   string `json:"taskKey"`
	Raw       bool   `json:"raw,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// WorkState is what the work mirror holds: Session.Work's two results.
type WorkState struct {
	Exists   bool         `json:"exists"`
	Snapshot WorkSnapshot `json:"snapshot"`
}

// WorkUpdate is a work_snapshot update: the whole state, sent whenever it
// differs from the last one sent on the connection.
type WorkUpdate struct {
	Kind string    `json:"kind"`
	Work WorkState `json:"work"`
}
```

Results: `session/prompt` answers a `PromptResult`; `session/suggest` a `SuggestResult`; `session/verify_proposal` a `VerifyCommands`; `session/steer` `{}`, or a `SteeringReceipt` for Send now; `session/steering_pop` a `SteeringPopResult`; `session/set_mode` an `Interaction`; `session/set_effort` an `EffortResult`; `session/subagent_cancel` a `SubagentCancelResult`; `session/watch_resume_take` a `WatchResumeTakeResult`; `session/work_list` a `WorkState` for the live view and a `WorkPage` otherwise; `session/work_inspect` a `WorkDetail`; `session/work_stop` a `WorkStopResult`; the others `{}`.

### The routing helper (`internal/acp/host.go`)

Plan 7a adds the same declaration, word for word, in the same place (right after `responseBarrier`). If it merged first, the helper is already there: keep it.

```go
// marshalOr serves one method two ways: forMarshal for a client that declared
// the marshal capability, which reaches the session's service (P3 R1, R9), and
// other for every other client, unchanged. A nil other answers such a client
// with method-not-found.
func marshalOr(marshal func() bool, forMarshal, other Handler) Handler
```

### The control handlers (`internal/acp/controls.go`, new)

```go
// workInterval is how often a work forwarder compares the session's work with
// the last one it sent (P3 R3).
const workInterval = 50 * time.Millisecond

// ControlManagerConfig wires one connection's control handlers.
type ControlManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends work_snapshot updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// ControlManager serves a session's turns, controls and work over its service
// for one connection, and runs one work forwarder per session a marshal client
// reads.
type ControlManager struct{ /* unexported */ }

func NewControlManager(cfg ControlManagerConfig) *ControlManager

func (m *ControlManager) Prompt(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) Stop(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) Review(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) AgentRun(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) Suggest(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) VerifyProposal(ctx context.Context, params json.RawMessage) (any, error)

func (m *ControlManager) Steer(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SteeringPop(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SteeringClear(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SetMode(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SetEffort(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SetSudoUnlock(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SubagentCancel(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) SDDClear(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) WatchResumeTake(ctx context.Context, params json.RawMessage) (any, error)

func (m *ControlManager) WorkList(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) WorkInspect(ctx context.Context, params json.RawMessage) (any, error)
func (m *ControlManager) WorkStop(ctx context.Context, params json.RawMessage) (any, error)

// Close stops every work forwarder. The connection's cleanup calls it.
func (m *ControlManager) Close()

// fenceSession waits until the session's work forwarder has sent the work as
// it is now, if it differs from the last one sent (P3 R2).
func (m *ControlManager) fenceSession(ctx context.Context, sessionID string)

// registerControls registers the eleven new methods on srv, and the seven
// existing ones through marshalOr: a client without the capability reaches
// others[method], unchanged. A method missing from others refuses such a
// client as method-not-found.
func registerControls(srv *Server, controls *ControlManager, others map[string]Handler)
```

### The watch-resume binding (`internal/acp/host.go`)

```go
// bindWatchResume points the runtime's watch-resume hook at this connection's
// turn manager, unless the client declared the marshal capability: a marshal
// client resumes a watch itself, with TakeWatchResume and a Prompt that names
// the watch, so a bound hook would resume it twice (P3 plan 6b).
func bindWatchResume(hook *app.WatchResumeHook, tm *TurnManager, sessionID string, alive *atomic.Bool, marshal func() bool)
```

### The client (`internal/acpclient/rpc/turns.go` and `rpc/work.go`, new)

```go
// turns.go
func (s *Session) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error)
func (s *Session) Stop(ctx context.Context, cause string) error
func (s *Session) Review(ctx context.Context, params acpwire.ReviewParams) error
func (s *Session) RunAgent(ctx context.Context, params acpwire.AgentRunParams) error
func (s *Session) Suggest(ctx context.Context, lastMessage string) (string, error)
func (s *Session) VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error)
func (s *Session) Steer(ctx context.Context, text string) error
func (s *Session) SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error)
func (s *Session) PopSteering(ctx context.Context) (string, bool)
func (s *Session) ClearSteering(ctx context.Context) error
func (s *Session) SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error)
func (s *Session) SetEffort(ctx context.Context, level string) (string, error)
func (s *Session) SetSudoUnlock(ctx context.Context, on bool) error
func (s *Session) CancelSubagent(ctx context.Context, subagentID int64) bool
func (s *Session) ClearFinishedRun(ctx context.Context) error
func (s *Session) TakeWatchResume(ctx context.Context) (name string, repeat bool, ok bool)

// work.go
// workCallTimeout bounds the first read's session/work_list.
const workCallTimeout = 5 * time.Second

func (s *Session) Work() (acpwire.WorkSnapshot, bool)
func (s *Session) ListWork(ctx context.Context, query acpwire.WorkQuery) (acpwire.WorkPage, error)
func (s *Session) InspectWork(ctx context.Context, taskKey string, raw bool) (acpwire.WorkDetail, error)
func (s *Session) StopWork(ctx context.Context, taskKey, reason string) (acpwire.WorkStopResult, error)
```

### The harness (`internal/acp/conformance_test.go`, `conformance_controls_test.go` new)

```go
// seam gains the twenty methods above, with the interface's signatures.

// serveControls registers the control handlers on the pipe arm's server over
// the served service, and returns the manager the barrier fences.
func serveControls(tb testing.TB, srv *Server, served *service.Session, sessionID string) *ControlManager

func TestControlConformance(t *testing.T)
func TestControlCallBudget(t *testing.T)
```

## Task 1: The wire types

**Goal:** `acpwire` carries the group's params, results and the work mirror's update.

**Files:** `internal/acpwire/turncalls.go` (new), `internal/acpwire/turncalls_test.go` (new).

**Steps:**

1. Add the constants and types in Contracts.
2. Tests in `turncalls_test.go`:
   - `TestPromptCallParamsCarryTheTextAsOneBlock`: `PromptCallParams{SessionID: "s1", Prompt: []TextBlock{{Type: "text", Text: "hi"}}, Queued: true}` encodes `"prompt":[{"type":"text","text":"hi"}]` and `"queued":true`, decodes to an equal value, and with `WatchResume` set carries `"watchResume":{"name":…,"seq":…}`.
   - `TestWorkUpdateRoundTrips`: a `WorkUpdate` whose state holds a snapshot with one item and `Exists: true` encodes and decodes to an equal value, and its `kind` is `work_snapshot`.
   - `TestAnEmptyWorkStateSaysItServesNoWork`: `WorkState{}` encodes `"exists":false`.
   - `TestTheControlResultsRoundTrip`: `SteeringPopResult`, `EffortResult`, `SubagentCancelResult` and `WatchResumeTakeResult` each round-trip, and a zero `SteeringPopResult` encodes `{"ok":false}`.

   `TestWireFieldsFollowTheRules` must keep passing: every new field is exported, camelCase-tagged and of an allowed type.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-6b-suite
go build ./...
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acpwire/ -count=1
```

Expected: the package passes, its rules test included.

## Task 2: The turn calls on the service

**Goal:** a marshal client's `session/prompt` runs on the session's service; `session/stop`, `session/review`, `session/agent_run`, `session/suggest` and `session/verify_proposal` call the service; a marshal connection binds no watch-resume hook; and releasing a service stops its run.

**Files:** `internal/acp/controls.go` (new), `internal/acp/controls_test.go` (new), `internal/acp/host.go`, `internal/acp/service/events.go`, `internal/acp/service/events_test.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **`marshalOr`** in `host.go`, right after `responseBarrier`, as in Contracts: when `marshal` is non-nil and reports true it calls `forMarshal`; otherwise it calls `other`, and when `other` is nil it returns `&jsonRPCError{Code: methodNotFound, Message: "method not found"}`. Plan 7a writes the same helper; keep its doc comment exactly as Contracts gives it.
2. **The manager.** `ControlManager` holds its config, a mutex, one work forwarder per session (Task 4 fills them in; this task declares the map and `Close`), and a `closed` flag, as `RequestManager` does. Its `service(sessionID)` helper refuses an empty ID with `invalidParamsError("sessionId is required")` and an unknown one with `serverErrorf("unknown session: %s", id)`, as `RequestManager.service` (`requests.go:228`) does.
3. **The turn handlers.** Each decodes its params type from Contracts (a decode error is `invalidParamsError`), resolves the service, and returns the service's answer or its error unchanged.
   - `Prompt`: decode `PromptCallParams`; join the blocks' `Text`s with `"\n\n"`, skipping no block and checking nothing (decision 8); call the service's `Prompt` with `acpwire.PromptParams{Text, Queued, WatchResume}` under the context `cancellable(ctx)` returns (calling its cancel when the call returns); return the `PromptResult`.
   - `Stop`: decode `StopParams`; call `Stop(ctx, Cause)`; return `struct{}{}`.
   - `Review` and `AgentRun`: decode their call params; call `Review(ctx, p.Review)` or `RunAgent(ctx, p.Run)` under `cancellable(ctx)`; return `struct{}{}`.
   - `Suggest`: decode `SuggestParams`; call `Suggest` under `cancellable(ctx)`; return `SuggestResult{Text}`.
   - `VerifyProposal`: decode `ControlParams`; call `VerifyProposal` under `cancellable(ctx)`; return the `VerifyCommands`.
4. **`registerControls`** as in Contracts. This task registers `session/stop`, `session/review`, `session/agent_run`, `session/suggest` and `session/verify_proposal` with `srv.Handle`, and `session/prompt` with `marshalOr(srv.marshalClient, controls.Prompt, others[acpwire.MethodPrompt])`. Tasks 3 and 4 add the rest.
5. **Host wiring.** In `registerHandlers`, right after `turns` is built (`host.go:331`), build `controls := NewControlManager(ControlManagerConfig{Lookup: manager.Service, Notify: h.sink.Notify, Marshal: srv.marshalClient})`. Delete the line registering `session/prompt` (`:351`). Right after the three `session/work_*` lines (`:394` to `:396`, after `works` is built), call `registerControls(srv, controls, map[string]Handler{acpwire.MethodPrompt: turns.PromptTurn})`; Tasks 3 and 4 add their methods to this map. Call `controls.Close()` in the cleanup, after `requests.Close()`. Leave the barrier alone until Task 4: these calls change nothing a forwarder of this manager carries, and the existing fences already cover the status, the stack, the sheet and the requests.
6. **`bindWatchResume`.** Move the body of the `if rt.WatchResume != nil` block in `newTurnManagerFor`'s `Lookup` (`host.go:257` to `:272`) into `bindWatchResume`, as in Contracts, keeping its comment. It returns at once when `marshal` is non-nil and reports true, with a comment naming decision 3. `Lookup` calls `bindWatchResume(rt.WatchResume, tm, sessionID, alive, marshal)` when the hook is non-nil.
7. **`Release` stops the run.** In `events.go`, `Release` now calls `s.Stop(context.Background(), "conversation ended")` before `s.close()`, and its doc says why (decision 5): the ACP host releases a session's service before the runtime closes, and the runtime's quiesce would otherwise wait for a run nobody can stop any more. With nothing running, `Stop` does nothing, so every other release is unchanged.
8. **The method table.** In `methods.go`, change `Stop`'s row to `session/stop` and mark `Review`, `RunAgent`, `Suggest` and `VerifyProposal` existing (`Stop` and `Prompt` already are). Rewrite their `ACP:` paragraphs in `acpclient.go`:
   - `Prompt`: "ACP: session/prompt (exists). The text travels as one text block, with queued and watchResume; a marshal client's turn runs on the session's service."
   - `Stop`: "ACP: session/stop (exists). session/cancel, a notification, stays for other clients."
   - `Review`, `RunAgent`, `Suggest`, `VerifyProposal`: "ACP: session/review (exists)." and so on.

   Keep the rest of each doc. `TestACPClientMethodTableMatchesTheServer` and `TestMethodDocsNameTheirACPMethod` hold the table and the docs together.
9. **Tests** in `controls_test.go`, serving a `ControlManager` and a `StatusManager` on a pipe with `startTestAgent`, `registerInitialize`, `registerStatus`, `registerControls` and `responseBarrier(statuses)`, to a raw `rpc.Conn` that declares the capability (or not), as `requests_test.go` serves its manager. The service is `service.New` over `session.New(config.Default(), t.TempDir(), …, session.Persistence{SessionID: "s1"})` with a test runner: copy the service package's `fakeRunner` into the file.
   - `TestAMarshalPromptRunsOnTheService`: the runner records the goal "hello"; `session/prompt` with one text block "hello" answers `end_turn`; a `TurnManager` registered as `others` for the method (with a `Lookup` that would also run the runner) reports `HasActiveTurn("s1")` false while the runner is held open.
   - `TestAPromptWithoutTheCapabilityReachesItsOtherHandler`: without the capability, the same call reaches the `others` handler (a stub that records it), and the service's runner never starts.
   - `TestAQueuedPromptThatCannotStartIsPutBack`: with no runner, a queued prompt answers an error whose data carries `CodeNoRunner`, and the state's steering queue holds the text again.
   - `TestStopStopsAServiceTurnWithItsCause`: with the runner blocked on its context, `session/stop` with the cause "turn stopped" makes the prompt answer `cancelled`.
   - `TestCancellingThePromptStopsTheRun`: cancel the client's context for `session/prompt` while the runner waits on its context; the runner's context ends within 2s.
   - `TestTheTurnCallsReachTheService`: `session/review`, `session/agent_run`, `session/suggest` and `session/verify_proposal` reach the service's `Deps.Review`, `RunAgent`, `Suggest` and `VerifyProposal` with the params given, and return their results.
   - `TestTheTurnCallsRefuseBadParams`: a missing `sessionId` is invalid params for each method; an unknown session is the unknown-session error.
   - `TestAServiceTurnReachesTheStackThroughTheIdleLoop` (decision 4): add the transcript handlers as `serveTranscript` (`conformance_test.go`) adds them. Read `session/stack` once (which starts the idle loop), then prompt with a runner that appends an assistant message to the state, publishes a session event, and blocks. Within 500ms, while the prompt is still in flight, a `stack_patch` update arrives that upserts a node for the message.
   - `TestAMarshalConnectionBindsNoWatchResume` and `TestAConnectionWithoutTheCapabilityBindsWatchResume`: call `bindWatchResume` with a zero `app.WatchResumeHook`, a `TurnManager` built as `TestResumeIfIdleStartsTurnWhenIdle` builds one, and an `alive` flag set true; then `Invoke` the hook with a resume report. With the capability no turn starts within 300ms; without it the runner starts.

   And in the service's `events_test.go`:
   - `TestReleaseStopsTheRunInFlight`: with a prompt held open by a runner waiting on its context, `Release` makes the prompt return `cancelled` within 2s, and a release with nothing running returns at once.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'Prompt|Stop|TurnCalls|IdleLoop|WatchResume|ResumeIfIdle|ACPClientMethodTable|Steer|Cancel' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'MarshalPrompt|StopStopsAServiceTurn|CancellingThePrompt|IdleLoop' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing prompt, cancel, watch-resume and permission test passes unchanged, none of them declaring the capability; the method-table tests pass.

Mutation checks:
- Make `marshalOr` always call `other`. `TestAMarshalPromptRunsOnTheService` fails. Restore it.
- Remove `bindWatchResume`'s early return. `TestAMarshalConnectionBindsNoWatchResume` fails. Restore it.
- Remove the `Stop` from `Release`. `TestReleaseStopsTheRunInFlight` fails. Restore it.

## Task 3: The controls

**Goal:** a marshal client's `session/steer`, `session/set_mode` and `session/set_effort` call the service, and the six new control methods do too.

**Files:** `internal/acp/controls.go`, `internal/acp/controls_test.go`, `internal/acp/host.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The handlers**, each resolving the service as the turn handlers do:
   - `Steer`: decode `SteerCallParams`. With `Delivery` equal to `SteerDeliveryNow`, call the service's `SendNow` with `acpwire.SendNowRequest{SessionID, TurnID, RequestID, Text}` and return the receipt; with an empty or `"queued"` delivery call `Steer(ctx, Text)` and return `struct{}{}`; any other delivery is `invalidParamsError("session/steer: unknown delivery %q: want queued or now", …)`, the message ACP's handler uses.
   - `SteeringPop`: return `SteeringPopResult` from `PopSteering`.
   - `SteeringClear`, `SDDClear`: call `ClearSteering` or `ClearFinishedRun`; return `struct{}{}`.
   - `SetMode`: decode the params into ACP's own `SetModeParams` (`interaction.go:39`), which decodes strictly and names system access `system_access`; a decode error is `invalidParamsError("session/set_mode: %v", err)`, as ACP's handler words it. Call `SetInteraction` with `acpwire.InteractionPatch{Mode, Auto, System}` from its fields, and return the `Interaction`.
   - `SetEffort`: decode `EffortParams`; return `EffortResult{Effort: p.Effort, AppliesFrom: applies}`, or the service's error unchanged.
   - `SetSudoUnlock`: decode `SudoUnlockParams`; call `SetSudoUnlock`; return `struct{}{}`.
   - `SubagentCancel`: decode `SubagentCancelParams`; return `SubagentCancelResult{Cancelled}` from `CancelSubagent`.
   - `WatchResumeTake`: return `WatchResumeTakeResult` from `TakeWatchResume`.
2. **Registration.** `registerControls` adds `session/steering_pop`, `session/steering_clear`, `session/set_sudo_unlock`, `session/subagent_cancel`, `session/sdd_clear` and `session/watch_resume_take` with `srv.Handle`, and `session/steer`, `session/set_mode` and `session/set_effort` through `marshalOr` over `others`. In `registerHandlers`, delete the lines registering `session/set_mode`, `session/set_effort` and `session/steer` (`host.go:352`, `:354`, `:355`), and add `turns.SetMode`, `turns.SetEffort` and `turns.Steer` to the `others` map under their `acpwire` names.
3. **The method table.** Mark `PopSteering`, `ClearSteering`, `SetSudoUnlock`, `CancelSubagent`, `ClearFinishedRun` and `TakeWatchResume` existing, and rewrite their `ACP:` paragraphs as "ACP: session/steering_pop (exists)." and so on. Add to `Steer`'s, `SendNow`'s, `SetInteraction`'s and `SetEffort`'s paragraphs: "A marshal client's call is served by the session's service."
4. **Tests** in `controls_test.go`, over the same agent as Task 2's:
   - `TestSteerQueuesWithNoTurnInFlight`: a marshal `session/steer` with no turn running answers `{}`, and the state's queue holds the text (ACP's own handler refuses this).
   - `TestSendNowAnswersTheReceipt`: with a turn the state reports active and supported, `session/steer` with delivery `now`, a turn and a request ID answers the receipt `SubmitSendNow` returned.
   - `TestSetModeTakesSystemAccess`: `{"sessionId":"s1","system_access":true}` reaches the setter as a patch with `System` true and answers the committed `Interaction`; an unknown key is invalid params.
   - `TestSetEffortAnswersWhenItApplies`: the setter's `next_turn` comes back as `appliesFrom`; the setter's error comes back unchanged, not as invalid params.
   - `TestTheControlsReachTheService`: pop, clear, sudo unlock, subagent cancel, SDD clear and watch-resume take each reach the state as the service's own tests expect (pop returns the oldest text and `ok`; an empty queue answers `ok` false; sudo unlock shows in `Status().SudoUnlock`; take returns the latch once).
   - `TestAControlWithoutTheCapabilityReachesTheTurnManager`: without the capability, a queued `session/steer` with no turn in flight answers ACP's "has no active turn" error, unchanged.
   - `TestTheControlsRefuseBadParams`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'Steer|SendNow|SetMode|SetEffort|Controls|ACPClientMethodTable|Interaction|Effort' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/... ./internal/acpclient/... -count=1
```

Expected: the new tests pass; every existing steer, send-now, set-mode and set-effort test passes unchanged.

Mutation check: route `SetMode`'s decode through `acpwire.InteractionPatch` instead of `SetModeParams`. `TestSetModeTakesSystemAccess` fails. Restore it.

## Task 4: The work mirror's agent half

**Goal:** a marshal client's work calls reach the service, and a forwarder keeps its work mirror current through the barrier.

**Files:** `internal/acp/controls.go`, `internal/acp/controls_test.go`, `internal/acp/host.go`.

**Steps:**

1. **The handlers.**
   - `WorkList`: decode `WorkListParams`. For an empty or `"live"` view: when `Marshal` reports true, make sure the session has a work forwarder (step 2); return `WorkState{Exists, Snapshot}` from the service's `Work()`. Any other view calls `ListWork` with `acpwire.WorkQuery{View, Query, Cursor, Limit}` and returns the `WorkPage` or the service's error unchanged, so a stale cursor travels as `CodeStaleWorkCursor`.
   - `WorkInspect`: decode `WorkTargetParams`; return `InspectWork(ctx, TaskKey, Raw)`.
   - `WorkStop`: decode `WorkTargetParams`; return `StopWork(ctx, TaskKey, Reason)`.
2. **The forwarder.** One per session per connection, as `RequestManager.ensureForwarder` and `runForwarder` (`requests.go:250`, `:275`) build and run one: its fence is registered before its goroutine starts, it wakes on a `workInterval` ticker and on its fence, it resolves the service through `Lookup` on each pass (answering a pending fence and stopping when the session is gone), and it sends `session/update` with the session's ID and `acpwire.WorkUpdate{Kind: acpwire.UpdateWork, Work: …}` only when the state's encoding differs from the last one it sent. It never sends under the manager's mutex. `Close` cancels every forwarder and makes later starts do nothing. `fenceSession` waits on the forwarder's fence, as `RequestManager.fenceSession` does.
3. **Registration and the barrier.** `registerControls` registers `session/work_list`, `session/work_inspect` and `session/work_stop` through `marshalOr` over `others`. In `registerHandlers`, delete their three lines (`host.go:394` to `:396`, just before the `registerControls` call), and add `works.List`, `works.Inspect` and `works.Stop` to the `others` map under their `acpwire` names. Append `controls` to the `responseBarrier` call, after `requests`.
4. **Tests** in `controls_test.go`, with `responseBarrier(statuses, controls)`; the service's `Deps.Work` is a copy of the service package's `fakeWork` that a test can publish a snapshot to.
   - `TestTheFirstWorkListArrivesBeforeTheResponse`: the first marshal live `session/work_list` returns after exactly one `work_snapshot` update for `s1` was recorded, and its state equals the result.
   - `TestANewWorkSnapshotIsSentWithinTheInterval`: publish a newer snapshot; an update carrying it arrives within 500ms. Three quiet intervals then send nothing.
   - `TestAWorkCallReturnsAfterItsUpdate` (V3): with every `session/update` frame slowed by 50ms, a `session/work_stop` whose fake publishes a snapshot before it answers returns only after the update carrying that snapshot was recorded.
   - `TestWorkPagesComeFromTheService`: an `attention` page answers the fake's page; a stale cursor answers an error whose data carries `CodeStaleWorkCursor`.
   - `TestASessionWithoutWorkSaysSo`: with no `Deps.Work`, the live view answers `exists` false.
   - `TestAClientWithoutTheCapabilityKeepsWorkState` (V6): without the capability, `session/work_list` reaches the `others` handler, and no `work_snapshot` update is recorded within 200ms.
   - `TestTheWorkForwarderStopsWhenTheSessionGoes`: make `Lookup` report false; within 500ms the forwarder has stopped and its fence is gone (a barrier for `s1` returns at once).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'Work|Barrier|AResponseFollows|Status' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -count=1
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'WorkList|WorkSnapshot|WorkCall' -race -count=3
```

Expected: the new tests pass, three times under `-race`; every existing work test (`watch_test.go`) passes unchanged; plan 3a's and 3b's ordering tests pass with the new fence.

Mutation check: drop `controls` from the test server's `responseBarrier`. `TestAWorkCallReturnsAfterItsUpdate` and `TestTheFirstWorkListArrivesBeforeTheResponse` fail. Restore it.

## Task 5: The client's turns, controls and work

**Goal:** `rpc.Session` sends the twenty calls and mirrors the work.

**Files:** `internal/acpclient/rpc/turns.go` (new), `internal/acpclient/rpc/work.go` (new), `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/turns_test.go` (new), `internal/acpclient/rpc/work_test.go` (new).

**Steps:**

1. **The calls** in `turns.go`. Each sends its method with its params type and the session's ID, and returns the result or the error, as `SetNotice` does.
   - `Prompt`: send `PromptCallParams` with one `TextBlock{Type: "text", Text: params.Text}`, `Queued` and `WatchResume`; decode the `PromptResult`.
   - `Stop`: `StopParams` with the cause.
   - `Steer`: `SteerCallParams` with the text and no delivery. `SendNow`: delivery `SteerDeliveryNow` with the request's turn and request IDs and text; decode the receipt.
   - `SetInteraction`: send an unexported `modeParams` struct with `sessionId`, `mode`, `auto` and `system_access` keys (each omitted when nil), because ACP names system access `system_access` (R9) and `acpwire` allows only camelCase tags; decode the `Interaction`.
   - `SetEffort`: return the result's `AppliesFrom`.
   - `PopSteering`, `CancelSubagent` and `TakeWatchResume` return their zero answers on a failed call (`"", false`; `false`; `"", false, false`), as a closed session answers in process.
2. **The work mirror** in `work.go`. The session holds one `WorkState` entry, absent until the first read, with an updated mark, as the request mirror does.
   - `Work()`: an entry returns its snapshot and `Exists` without I/O. Otherwise create the entry, send `session/work_list` with no view under a `workCallTimeout` context, and install the result unless an update arrived during the call, as `pendingEntry` does. On error remove the entry and return a zero snapshot and false; the next read asks again.
   - `ListWork`, `InspectWork`, `StopWork` send their methods and decode their results.
3. **Routing.** `routeNotify` gains a case for `acpwire.UpdateWork`: decode `WorkUpdate` and hand its state to the attached session it names. The session replaces its entry's state, marks it updated, and taps `Changed`. An update for a session that has not read its work is dropped.
4. **Tests**, over the scripted agent of `session_test.go`:
   - `TestPromptSendsTheTextAsOneBlock`: the frame carries one text block with the text, `queued` and `watchResume`; the result's stop reason comes back; a coded error comes back with `acpclient.IsCode` true.
   - `TestTheTurnCallsSendTheirMethods`: `Stop`, `Review`, `RunAgent`, `Suggest` and `VerifyProposal` send their methods with the session's ID and their params, and decode their results.
   - `TestSetInteractionNamesSystemAccess`: the frame carries `system_access`, not `system`, and omits a nil field.
   - `TestTheControlsSendTheirMethods`: `Steer`, `SendNow`, `PopSteering`, `ClearSteering`, `SetEffort`, `SetSudoUnlock`, `CancelSubagent`, `ClearFinishedRun` and `TakeWatchResume`; a failed `PopSteering` returns false.
   - `TestWorkFetchesOnItsFirstRead`, `TestWorkInstallsTheUpdateThatPrecedesTheResponse`, `TestAWorkUpdateReplacesTheStateAndFiresChanged`: the rules plan 5's tests pin for the request mirror, for the work.
   - `TestTheWorkCallsSendTheirMethods`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included, three times under `-race`; plan 3b's, 4b's and 5's client tests pass unchanged.

Mutation check: make the router skip `work_snapshot`. `TestAWorkUpdateReplacesTheStateAndFiresChanged` fails. Restore it.

## Task 6: The conformance harness and the budget

**Goal:** the group's cases run against the service and against the client over a pipe, and agree (V1); a control call stays inside R10's budget.

**Files:** `internal/acp/conformance_test.go`, `internal/acp/conformance_controls_test.go` (new), `internal/acp/service/turns_test.go`, `internal/acp/service/controls_test.go`, `internal/acp/service/work_test.go`, `internal/acp/service/sendnow_test.go`.

**Steps:**

1. **The seam.** Add the twenty methods in Contracts to `seam`.
2. **The pipe arm.** In `openPipeSeamTB`, after `serveRequests`, call `serveControls(tb, srv, served, sessionID)`, and add its manager to `responseBarrier` after `requests`. `serveControls` builds a `ControlManager` whose `Lookup` returns the served service for the served ID, with `Notify: srv.Notify` and `Marshal: srv.marshalClient`, calls `registerControls` with a nil `others`, and closes the manager with the test.
3. **The table.** `TestControlConformance` runs every case once per arm, as `TestRequestConformance` does. Move these tests from the service's tests into it, deleting them there, and copy the helpers they use (`fakeRunner`, `newFakeRunner`, `fakeTurnStopService`, `fakeWork`) into the new file:
   - turns: `TestPromptRunsTheRunner`, `TestPromptWithoutARunnerIsRefused`, `TestASecondRunIsBusy`, `TestAnEndingSessionRefusesThePrompt`, `TestStopStopsTheGroupBeforeTheRun`, `TestAStoppedRunThatStartedKeepsItsText`, `TestReviewRunsOnTheRunPath`, `TestRunAgentRunsOnTheRunPath`, `TestAStoppedReviewIsNotAnError`, `TestSuggestAndVerifyProposalForward`, `TestPromptWritesTheWatchResumeGoal`, `TestPromptCodesAProviderError`, `TestPromptPassesOtherErrorsThrough`;
   - controls: `TestSteerQueuesAndPopTakesTheOldest`, `TestClearSteeringEmptiesTheQueue`, `TestSetInteractionPassesThePatch`, `TestSetInteractionReturnsTheSettersError`, `TestSetEffortReportsWhenItApplies`, `TestSetSudoUnlockReachesStatus`, `TestCancelSubagentStopsOnlyARunningChild`, `TestClearFinishedRunClearsProgressAndEvents`, `TestTakeWatchResumeTakesTheLatchOnce`;
   - Send now: `TestSendNowRecordsTheReceiptBeforeReturning`, `TestSendNowReturnsTheStatesRefusal`;
   - work: `TestWorkReadsTheSeed`, `TestWorkWithoutAServiceReportsNone`, `TestWorkKeepsANewerSnapshotAndSignals`, `TestListWorkNamesTheSessionsConversation`, `TestAStaleCursorIsCoded`, `TestInspectAndStopNameTheirTarget`.

   The helpers to copy are `fakeRunner`, `newFakeRunner`, `fakeTurnStopService`, `fakeWork` and `closedCode`; the service's `newState` is the harness's `seamState`.

   These stay in the service's package. The tests that drive the in-process host's `swap` (`TestAClosedSessionRunsNothing`, `TestAClosedSessionControlsNothing`, `TestASwapSubscribesTheNewSessionAfresh`, `TestAClosedSessionServesNoWorkCalls`, `TestSendNowOnAClosedSessionFails`), because only the service has that swap. And the tests that pin how the service drives the state rather than what crosses the seam: its stop group and work registration, the idle activity, a stop that lands before the slot, a panic, the snapshot guard, its constants and its missing setters (`TestPromptRegistersItsWorkAndStopGroupWhileItRuns`, `TestARunEndsIdle`, `TestAgentReadyFollowsTheRunner`, `TestAStopRequestedBeforeTheTurnSkipsIt`, `TestAQueuedPromptSkippedByAnEarlyStopIsPutBack`, `TestStopWithNothingRunningLeavesTheNextTurnAlone`, `TestAPanickingRunnerFailsTheTurn`, `TestWorkDropsOlderAndForeignSnapshots`, `TestEffortAppliesValuesAreTheSessions`, `TestWithoutTheSettersTheCallsFail`).

   Each case builds its `Deps` and opens the arm before acting. Where a service test waits on one `Changed` signal and then reads, the case waits until the read shows the change, with a 5s deadline: over the wire the status and the sheet also signal `Changed`. Where a test runs a prompt on its own goroutine and stops it, keep that shape: the pipe arm's `Prompt` is a call in flight while the stop goes out as another. Keep each case's other assertions as they are.
4. **A difference is a bug, never the case's,** as in plan 5: fix the wire type, the handler or the client so both arms agree. If the fix would change what the TUI sees today, stop and report it instead.
5. **The budget (R10).** `TestControlCallBudget`: the pipe arm over `largeStackSession`. Time 30 `SetSudoUnlock` calls alternating true and false; each call includes its barrier and the `session_status` update that carries the change. The median must be under 1ms. Log it with `t.Logf` for the Implementation record. Skip under `-short` and with the race detector, as `TestAnswerCallBudget` does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'TestControlConformance|TestControlCallBudget|TestRequestConformance|TestStatusConformance|TestTranscriptConformance' -count=1 -v 2>&1 | tail -80
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-6b-suite go test ./internal/acp/ -run 'TestControlConformance' -race -count=3
```

Expected: every case passes on both arms, three times under `-race`; the budget test passes and logs its median; plans 3b's, 4b's and 5's cases still pass with the control manager in the pipe arm; the service package passes without the moved tests.

Mutation check: make the client's `Prompt` drop `Queued`. The moved case for a queued prompt's requeue (`TestPromptWithoutARunnerIsRefused`'s queued half) fails on the pipe arm. Restore it.

## Task 7: The docs and full verification

**Goal:** the spec records what plan 6b built and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 5 did. Expect, if plan 7a has not merged: 111 rows, 61 of them an existing ACP method (ten more: `Review`, `RunAgent`, `Suggest`, `VerifyProposal`, `PopSteering`, `ClearSteering`, `SetSudoUnlock`, `CancelSubagent`, `ClearFinishedRun` and `TakeWatchResume`; `Stop` moved from one existing method to another), 48 new in P3 carried by 44 distinct new methods, and 2 carrying none; and 96 registered methods (85 plus the eleven). If plan 7a merged first, measure the merged tree and record what it shows. Update the section's numbers and its "What the agent pushes today" list: a marshal client also receives `work_snapshot`.
2. **The Implementation record.** Add a "Plan 6b — turns and controls" entry after plan 4b's (after plan 7a's, if it merged first). It names: `marshalOr` and the seven methods routed through it; the control manager and its eleven new methods; `session/stop` and why `session/cancel` stays; the cancellable long calls; the prompt's text block and its two optional fields; `bindWatchResume`; `Release`'s stop; the idle loop carrying a service turn; the work forwarder, `workInterval` and `work_snapshot`; the barrier's new fence; `rpc.Session`'s twenty methods and its work mirror; the harness's cases; and the budget's median.
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "controls" in its list, after "requests".
4. **Main may have moved.** If plan 7a merged while this branch ran, merge `main` into the branch before the full verification, and resolve the conflicts as "Integration with plan 7a" says.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-6b-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-6b-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, `TestAnswerCallBudget`, `TestStackBusyFlipBudget`, and now `TestControlCallBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Integration with plan 7a

Plan 7a adds the settings manager. Whichever branch merges second merges `main` first and resolves these, keeping both sides:
- **`marshalOr`'s declaration** is the same in both plans, in the same place, so it merges cleanly. If it does not, keep one copy.
- **`host.go`'s `registerHandlers`:** this plan builds the control manager after `turns`, moves seven `Handle` lines into `registerControls`' `others` map after `works`, appends `controls` to the barrier and closes it in the cleanup; plan 7a builds its manager after the request manager, moves two `config/*` lines into its own `others` map after the remaining `config/*` lines, appends `settings` to the barrier and closes it. The barrier's order is `turns, works, watches, statuses, sheets, requests, controls, settings`.
- **`conformance_test.go`:** both add methods to `seam`; both add a line to the pipe arm that serves their handlers and add their manager to its `responseBarrier` call. Plan 7a also moves the pipe arm's body into a function that returns the host too; this plan's `serveControls` line goes into that body.
- **`rpc/session.go`'s `routeNotify`:** both add a case for their update kind.
- **`acpclient/methods.go` and `acpclient.go`:** different rows and different methods' docs; no line is shared.
- **The spec:** both add an Implementation record entry and re-measure the counts. Record the merged tree's counts.

## Self-review

- **Spec coverage.** The decision that the service runs a marshal client's turns: Tasks 2 to 4 (`marshalOr`), proved by `TestAMarshalPromptRunsOnTheService` and the conformance table. R3's `Work` row (first read, kept current at most every 50ms and before any response): Tasks 4 and 5. R2's cancellation: `TestCancellingThePromptStopsTheRun`. R2 and V3: `TestAWorkCallReturnsAfterItsUpdate`, and every moved case that reads after a call on the pipe arm. R9 and V6: `TestAPromptWithoutTheCapabilityReachesItsOtherHandler`, `TestAControlWithoutTheCapabilityReachesTheTurnManager`, `TestAClientWithoutTheCapabilityKeepsWorkState`. R10: `TestControlCallBudget`. V1: Task 6. V8: Task 7.
- **What could go wrong.**
  - **Two turn slots.** A marshal client's turn takes the service's slot, not the `TurnManager`'s, so a client without the capability on another connection could start a second turn on the same session. Embedded mode has one connection; P4's multi-viewer design decides how the two slots exclude each other (record it under the Implementation record's open points).
  - **The watch resumes twice, or never.** `bindWatchResume` skips only a marshal connection, and the TUI's own path (status, take, prompt) is the one in-process code uses today; both tests pin it.
  - **A stuck close.** `Release` stops the service's run before the runtime quiesces; `TestReleaseStopsTheRunInFlight` pins it.
  - **A transcript that stops moving during a turn.** The idle loop flushes while the `TurnManager` has no turn; `TestAServiceTurnReachesTheStackThroughTheIdleLoop` pins it.
  - **Plan 7a.** The shared files and their resolutions are listed above.
