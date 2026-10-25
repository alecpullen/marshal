# P2 plan 2b: runs, queues and events — implementation plan

Date: 2026-10-25. Status: ready for review.
Base: `main` at `2268ce0b` (plan 2a merged) or later.
- Every anchor below was checked at `2268ce0b`. Line numbers are there to find the code fast; the function names are the anchors.
- The read sites come from the same type-checked scan of the TUI tree that plan 2a used. Each task lists all of its sites.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C2 (reads and `Changed`), C3 (wire rules), C4 (the adapter), C5 (status), C8 (`SendNow`), Migration order item 2b and its revision record entry against `2268ce0b`, and tests T1, T1b, T2, T4 and T5.
Previous plan: [plan 2a, status](2026-10-25-p2-2a-status-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI learns that its session changed from one signal, `Session.Changed()`, and reads the steering queue, Send now, jobs, git, subagents and run progress from `Session.Status()`. Nothing a user sees changes.
- `acpwire` holds the run, queue and steering types. ACP's steering wire types move there, and their JSON is unchanged.
- `Session` gains `Changed` and `SendNow`. The in-process adapter subscribes to the five session brokers, keeps Send-now receipts and the pending watch resume, and fires `Changed`.
- The TUI's job, watch, steering, workspace and subagent pumps are replaced by one change pump. `WithSendNow` and the five broker options are deleted.
- T1b gains seven names.

**Non-goals:**
- **No work group.** The work pump, `WithWork`, the work types and the work calls stay as they are until plan 5 (spec revision record, `2268ce0b`).
- **No sheet.** `Session.Sheet`, HEAD, the changed files and the base-ref command (`sheetBaseRefCmd`) are plan 2c. This plan only changes what triggers that command when the workspace moves.
- **No other writes.** `PushSteering`, `PopSteering`, `ClearSteering`, `ClearSDDProgress`, `ClearRunEvents`, `ClearSDDGate`, `CancelSubagent` and `TakeWatchResume` stay on `m.state` until plan 5.
- **No child states.** The drill-in, the approval lookup and the work-origin jump read `m.state.Subagents()` and `m.state.Subagent(id)` for their `Child` state. Those reads stay for plans 3 and 4, so `Subagents` and `Subagent` do not enter T1b here.
- **No ACP handler.** `session/status` is still new in P3.
- **No changelog entry.** Nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-2b-suite`. Create the directory first: `mkdir -p /private/tmp/p2-2b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **Pre-existing failures** at `2268ce0b` are listed in Task 11, from a full run with `-timeout 20m`. A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`. The exceptions are named there.

**Decisions this plan makes beyond the spec** (decisions 1 to 4 are also recorded in the spec's revision record):
1. **The work group waits for plan 5.** `viewmodel`'s work types cannot move into `acpwire` as they are (`time.Time` and `progress` fields, untagged `WorkQuery` and `WorkTarget`, an embedded `WorkTarget` in `WorkAck`). Moving the pump means porting the tray's and the tasks panel's work rendering, which belongs with the work calls.
2. **The job list and the watch snapshot are deleted, not moved.** `m.jobs` and `m.watches` are written from their events and read by no production code. `Status` carries the running job count from `State.RunningJobsCount`. The job manager sets that count just before it publishes each job event (`jobs_manager.go:2303`), so it equals the newest event's count.
3. **`SendNow` moves into the session now** (C8 places it in plan 5). Today `submitSendNow` caches the receipt it gets back (`model.go:5957`), so the next frame shows it before its event arrives. The adapter does the same: it records the receipt before `SendNow` returns. `WithSendNow` goes. The snapshot is a fresh read of the state's, as `refreshSteeringSnapshot` takes on every tick today.
4. **HEAD stays in the TUI until plan 2c.** Only `sheetBaseRefCmd` and the initial `sheetBaseRef` read it.
5. **Git info is a fresh read.** `Status` calls `gitinfo.Read(activeRoot)`. That function reads only files and caches each directory for 2 seconds (`gitinfo.go:28`), and its doc says it is safe on the render path. The TUI's own cache, its 5-second throttle and its re-reads on resize, on the agent tick, on a workspace event and on `/new` all go. The branch is at most 2 seconds stale, which is at least as fresh as today: a resize also hit that 2-second cache.
6. **`Changed` belongs to one session.** The adapter closes it when a swap replaces the session.
   - The change pump's message carries the channel it read. `handleChanged` ignores a message whose channel is no longer `m.sess.Changed()` and does not re-arm it, so the old pump ends.
   - `startNewSession` starts a pump for the new session.
   - The adapter fires `Changed` after each event from the five brokers and after a successful `SendNow`. Values no event announces (swarm and SDD progress, the SDD gate) reach the screen on the 150 ms tick and on input, as they do today.
7. **The pending watch resume is a slot with a sequence number.**
   - On each watch event with `Resume` set, the adapter replaces the slot and increments the sequence.
   - On each change message, the TUI acts on a sequence newer than the last one it saw, then records it, whether or not the turn starts. Today `handleWatchMsg` drops an event it cannot act on in the same way.
   - Two fires between reads give one wake, for the newer watch. Today the second event would also be dropped, because the first wake made the session busy.
   - The sequence restarts with each session, so `startNewSession` resets the TUI's mark.
   - The turn-end latch (`TakeWatchResume` in `handleAgentFinished`) is a write and stays for plan 5.
8. **One status read per function**, as plan 2a's decision 2 sets out. In each function or function literal, take `st := m.sess.Status()` once, in the smallest block that encloses all of that function's moved reads, before the first of them. Reuse an `st` that plan 2a or an earlier task already took. A function with one moved read may write `m.sess.Status().X` inline. The named exceptions are in the tasks.
9. **The workspace follow compares the whole value.** A workspace event fires only when `SetWorkspace` changes the value (`workspace.go:49`). `handleChanged` keeps the last `acpwire.Workspace` it saw and starts `sheetBaseRefCmd` when the value differs and the active root is not empty, as `handleWorkspaceMsg` does.
10. **Subscriptions.** The watch subscription is terminal (`pubsub.WithTerminal`), because a dropped resume would lose a wake. The others are best effort, as the TUI's steering, workspace and subagent subscriptions are today: `Changed` coalesces, and receipts are versioned. The adapter subscribes even when the provider failed at start, which the TUI's job, watch and steering pumps did not. That adds only repaints.
11. **`WithContext` carries the program's context.** The two broker options wired in every start (`WithWorkspaceBroker`, `WithSubagentBroker`) also set `m.ctx`. Without a replacement, a start whose provider failed would keep a background context until a runner is adopted. `m.ctx` is the parent of `/mcp auth` and of each turn.
12. **The steering conversion lives in `viewmodel`.** `acp` and the adapter both need it, and the adapter cannot import `acp` (`acp` imports `app`, which imports the adapter). Every other new conversion is the adapter's own, as plan 2a's are.
13. **The guard gains seven names**: `SteeringQueue`, `SteeringSnapshot`, `SubmitSendNow`, `RunningJobsCount`, `SwarmProgress`, `SDDProgress` and `SDDGate`. The TUI never read `SteeringSnapshot` or `SubmitSendNow` through `m.state`. Listing them keeps Send now on the session.

**Execution:**
- Branch first: `git switch -c p2-2b-events` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-2b-events: record the plan 2b revisions and plan 2b`.
- Run the tasks in order. Run each task's Verify and commit it as `p2-2b-events: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- From Task 9 on, the change pump is a command that blocks until the next signal. `drainCmds` (`model_test.go:89`) gives up on a command after 20 ms, so draining stays safe. A test that waits on `Changed` itself must read the channel before it drains anything.

## Contracts

**`internal/acpwire`, after Task 1.** `steering.go` (new):

```go
// SteeringState is the Send now snapshot of a session's running turn.
type SteeringState struct {
	Version       uint64 `json:"version"`
	TurnID        string `json:"turnId"`
	Active        bool   `json:"active"`
	Supported     bool   `json:"supported"`
	Held          bool   `json:"held"`
	OperationID   string `json:"operationId,omitempty"`
	OperationKind string `json:"operationKind,omitempty"`
	PendingCount  int    `json:"pendingCount"`
}

// SteeringOutcome is one settled effect an interrupted operation left behind.
type SteeringOutcome struct {
	Kind        string `json:"kind"`
	State       string `json:"state"`
	ToolCallID  string `json:"toolCallId,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
	AgentID     string `json:"agentId,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// SteeringReceipt is the state of one accepted Send now request.
type SteeringReceipt struct {
	Version     uint64            `json:"version"`
	RequestID   string            `json:"requestId"`
	TurnID      string            `json:"turnId"`
	Status      string            `json:"status"`
	OperationID string            `json:"operationId,omitempty"`
	Outcomes    []SteeringOutcome `json:"outcomes,omitempty"`
}

// SendNowRequest is one Send now submission.
type SendNowRequest struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	RequestID string `json:"requestId"`
	Text      string `json:"text,omitempty"`
}

// The receipt statuses.
const (
	SteeringAccepted      = "accepted"
	SteeringInterrupting  = "interrupting"
	SteeringWaitingSafe   = "waiting_safe_boundary"
	SteeringWaitingResume = "waiting_for_resume"
	SteeringDelivered     = "delivered"
	SteeringUndelivered   = "undelivered"
)
```

`runs.go` (new):

```go
// Steering is the steering queue and the Send now state.
type Steering struct {
	Queue   []string         `json:"queue,omitempty"`   // the queued texts, oldest first
	State   SteeringState    `json:"state"`
	Receipt *SteeringReceipt `json:"receipt,omitempty"` // the newest receipt for State.TurnID
}

// Subagent mirrors the session's SubagentView without its child state.
type Subagent struct {
	ID       int64  `json:"id"`
	Label    string `json:"label,omitempty"`
	Status   string `json:"status"`
	Role     string `json:"role,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	Headline string `json:"headline,omitempty"` // running children only
}

// The subagent statuses.
const (
	SubagentRunning = "running"
	SubagentDone    = "done"
	SubagentFailed  = "failed"
)

// SwarmProgress mirrors the session's SwarmProgress.
type SwarmProgress struct {
	Goal       string      `json:"goal,omitempty"`
	Active     bool        `json:"active,omitempty"`
	Roles      []SwarmRole `json:"roles,omitempty"`
	TokensUsed int         `json:"tokensUsed,omitempty"`
	TokensMax  int         `json:"tokensMax,omitempty"`
}

// SwarmRole mirrors the session's SwarmRole.
type SwarmRole struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Tokens    int    `json:"tokens,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

// The swarm role statuses.
const (
	SwarmRolePending = "pending"
	SwarmRoleActive  = "active"
	SwarmRoleDone    = "done"
	SwarmRoleFailed  = "failed"
)

// SDDProgress mirrors the session's SDDProgress.
type SDDProgress struct {
	Active         bool         `json:"active,omitempty"`
	PlanName       string       `json:"planName,omitempty"`
	PlanPath       string       `json:"planPath,omitempty"`
	Branch         string       `json:"branch,omitempty"`
	BaseRef        string       `json:"baseRef,omitempty"`
	LedgerPath     string       `json:"ledgerPath,omitempty"`
	ArtifactsDir   string       `json:"artifactsDir,omitempty"`
	Tasks          []string     `json:"tasks,omitempty"`
	TaskTimings    []TaskTiming `json:"taskTimings,omitempty"`
	TaskExecTypes  []string     `json:"taskExecTypes,omitempty"`
	TotalTasks     int          `json:"totalTasks,omitempty"`
	DoneTasks      int          `json:"doneTasks,omitempty"`
	CurrentTask    int          `json:"currentTask,omitempty"`
	Phase          string       `json:"phase,omitempty"`
	PhaseStartedAt int64        `json:"phaseStartedAt,omitempty"`
	Detail         string       `json:"detail,omitempty"`
	FixRound       int          `json:"fixRound,omitempty"`
	MaxFixRounds   int          `json:"maxFixRounds,omitempty"`
	TokensUsed     int          `json:"tokensUsed,omitempty"`
	TokensMax      int          `json:"tokensMax,omitempty"`
	StartedAt      int64        `json:"startedAt,omitempty"`
	Finished       bool         `json:"finished,omitempty"`
	Succeeded      bool         `json:"succeeded,omitempty"`
	EndedAt        int64        `json:"endedAt,omitempty"`
	Error          string       `json:"error,omitempty"`
}

// TaskTiming mirrors the session's TaskTiming.
type TaskTiming struct {
	StartedAt int64 `json:"startedAt,omitempty"`
	EndedAt   int64 `json:"endedAt,omitempty"`
}

// SDDGate mirrors the session's SDDGate. A gate with no question is closed.
type SDDGate struct {
	TaskN     int    `json:"taskN,omitempty"`
	Question  string `json:"question,omitempty"`
	TaskTitle string `json:"taskTitle,omitempty"`
	Report    string `json:"report,omitempty"`
}

// Git mirrors gitinfo.Info for the session's active root.
type Git struct {
	Branch   string `json:"branch,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	InRepo   bool   `json:"inRepo,omitempty"`
}

// WatchResume is the newest resume-bearing watch fire. Seq increases with
// each fire, so a reader acts on a value once.
type WatchResume struct {
	Name   string `json:"name"`
	Repeat bool   `json:"repeat,omitempty"`
	Seq    uint64 `json:"seq"`
}
```

`SessionStatus` (`status.go`) gains, after `Browser`:

```go
	Steering    Steering      `json:"steering"`
	RunningJobs int           `json:"runningJobs,omitempty"`
	Subagents   []Subagent    `json:"subagents,omitempty"`
	Swarm       SwarmProgress `json:"swarm"`
	SDD         SDDProgress   `json:"sdd"`
	SDDGate     SDDGate       `json:"sddGate"`
	Git         Git           `json:"git"`
	WatchResume *WatchResume  `json:"watchResume,omitempty"`
```

**`internal/viewmodel`, after Task 2** (`steering.go`, new):

```go
// SteeringStateOf projects a session's Send now snapshot onto the wire.
func SteeringStateOf(snap session.SteeringSnapshot) acpwire.SteeringState

// SteeringReceiptOf projects a session receipt onto the wire, deep-copying
// its outcomes.
func SteeringReceiptOf(rec session.SteeringReceipt) acpwire.SteeringReceipt
```

**`internal/acpclient`, after Tasks 4 and 5.** `Session` gains:

```go
	// Changed fires after anything Status returns may have changed. It is a
	// coalescing signal with a buffer of 1: a reader re-reads Status and never
	// infers what changed. It is closed when a swap replaces the session.
	//
	// ACP: none (the client fires it; P3's mirror fires it after applying a
	// session/update notification).
	Changed() <-chan struct{}

	// SendNow submits one Send now request: its text goes to the running
	// turn at the next safe boundary, interrupting the operation in flight.
	// The receipt is in Status before SendNow returns.
	//
	// ACP: session/steer (exists), with delivery "now".
	SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error)
```

and `Methods` gains, after the `Status` row:

```go
	{Iface: "Session", Name: "Changed"},
	{Iface: "Session", Name: "SendNow", ACP: "session/steer", Exists: true},
```

**`internal/acpclient/inproc`, after Tasks 3 to 5.**

```go
type Deps struct {
	State *session.State
	// Context bounds the session's event subscriptions. Nil means
	// context.Background().
	Context context.Context
	// The runtime's brokers. A nil broker is not subscribed.
	Jobs      *pubsub.Broker[native.JobEvent]
	Watches   *pubsub.Broker[watch.Event]
	Steering  *pubsub.Broker[session.SteeringEvent]
	Workspace *pubsub.Broker[session.WorkspaceEvent]
	Subagents *pubsub.Broker[session.SubagentEvent]
	// GitInfo reads the git info for a directory. Nil means gitinfo.Read.
	GitInfo func(dir string) gitinfo.Info
}

func (s *Session) Changed() <-chan struct{}
func (s *Session) SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error)
```

**`internal/app/tui`, after Tasks 6 to 9.**

```go
// events.go
type changedMsg struct{ ch <-chan struct{} }

// pump.go
func pumpChanged(ch <-chan struct{}) tea.Cmd

// model.go
func WithContext(ctx context.Context) Option
func (m Model) handleChanged(msg changedMsg) (Model, tea.Cmd)
func (m *Model) sendNowTarget() (acpwire.SteeringState, bool)
func (m *Model) submitSendNow(request acpwire.SendNowRequest, clear func()) bool
func (m Model) steeringStatusSegment(r *acpwire.SteeringReceipt) (string, bool)

// run_panel.go, runoutcome.go, eta.go, livestrip.go
func runPanelSummaryLine(p acpwire.SDDProgress, spinner string, now time.Time, width int) string
func runPanelSummaryText(p acpwire.SDDProgress, now time.Time, budget int) string
func runPanelFinishedLine(p acpwire.SDDProgress, width int) string
func runPanelFinishedParts(p acpwire.SDDProgress, budget int) (string, color.Color, string)
func runOutcomeDoc(p acpwire.SDDProgress, git worktree.GitOps, repoRoot string, now time.Time, width int) commands.Doc
func taskGlyph(i int, p acpwire.SDDProgress) string
func taskDuration(i int, p acpwire.SDDProgress, now time.Time) string
func estimateRemaining(p acpwire.SDDProgress, now time.Time) (low, high time.Duration, openEnded, ok bool)
func currentTaskElapsed(p acpwire.SDDProgress, now time.Time) time.Duration
func swarmStripText(p acpwire.SwarmProgress) string

// gatepanel/gatepanel.go
func New(g acpwire.SDDGate) *Panel

// sessionsheet/section.go: Data's fields
	Git   acpwire.Git
	Swarm acpwire.SwarmProgress
	SDD   acpwire.SDDProgress

// sessionsheet/section_swarm.go
func roleGlyph(s string) string
```

**TUI test helpers (`seam_test.go`, Tasks 6, 7 and 9).**

```go
// testModelWith builds a model over an adapter made from d, as testModel
// builds one over d.State alone.
func testModelWith(d inproc.Deps, opts ...Option) Model

// stubSession wraps the adapter's session for tests that need a value only
// events or a call produce: patch adjusts every Status read, and onSendNow
// sees each SendNow request before the adapter does.
type stubSession struct {
	acpclient.Session
	patch     func(*acpwire.SessionStatus)
	onSendNow func(acpwire.SendNowRequest)
}

func (s *stubSession) Status() acpwire.SessionStatus
func (s *stubSession) SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error)

// testModelStub builds a model whose session is stub, wrapped around an
// adapter session over state.
func testModelStub(state *session.State, stub *stubSession, opts ...Option) Model

// batchResults runs cmd and returns the messages it produces: each command
// of a batch runs with a 50 ms limit, and one that blocks (the change pump)
// is skipped.
func batchResults(t *testing.T, cmd tea.Cmd) []tea.Msg
```

**Strings the tests pin:**

| Where | Text |
|---|---|
| `SendNow` on a closed session | `&acpwire.Error{Code: acpwire.CodeSessionClosed, Message: "inproc: the session was replaced"}` |
| T1b violation (unchanged) | `<file>:<line> reads state.<Name>, which moved behind the seam in plan <plan>: read it from m.sess` |

## Task 1: The wire types

**Goal:** `acpwire` holds the steering, run and queue types, `SessionStatus` has its 2b fields, and T2's rules pass over all of them.

**Files:** `internal/acpwire/steering.go`, `internal/acpwire/runs.go`, `internal/acpwire/status.go`, `internal/acpwire/steering_test.go`.

**Steps:**

1. **`steering.go`.** Declare the types and constants exactly as in Contracts. The file's comment says these are the JSON contract `session/steer` and `session/steering_status` already serve, and that it is duplicated field by field rather than aliasing a session type, so a change to the session's types cannot change the bytes a client reads. That is the comment over the types in `acp/turn.go:1634`, which Task 2 deletes.
2. **`runs.go`.** Declare the types and constants exactly as in Contracts. Each type's one-line doc names the value it mirrors, as in Contracts. Times are Unix milliseconds (`Millis`). The constants' values are the session's own strings, and the subagent statuses name `SubagentRunning`, `SubagentDone` and `SubagentFailed`.
3. **`status.go`.** Add the eight fields from Contracts after `Browser`. In `SessionStatus`'s doc, drop "Plan 2b adds the runs, queues and git info", so it reads: plan 2c adds the session's start time, plans 3 and 4 the sandbox info, and plan 6 the current notice.
4. **`steering_test.go`.** `TestSteeringReceiptJSONIsTheACPContract` marshals a receipt with every field set and one outcome with every field set, and compares the bytes with this literal:

   ```
   {"version":3,"requestId":"r1","turnId":"t1","status":"delivered","operationId":"op1","outcomes":[{"kind":"tool","state":"stopped","toolCallId":"c1","executionId":"e1","agentId":"a1","summary":"s"}]}
   ```

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/acpwire/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpwire/
```

Expected: the new test passes, and so do `TestWireFieldsFollowTheRules` and `TestWireImportsOnlyTheStandardLibrary`, now over the new types.

## Task 2: ACP uses the wire steering types

**Goal:** `acp` serves Send now through the `acpwire` types and the `viewmodel` conversion, with the same JSON.

**Files:** `internal/viewmodel/steering.go`, `internal/viewmodel/steering_test.go`, `internal/acp/turn.go`, `internal/acp/turn_test.go`, `internal/acp/permissions_test.go`.

**Steps:**

1. **`viewmodel/steering.go`.** `SteeringStateOf` and `SteeringReceiptOf` (Contracts) have the bodies of `acp`'s `steeringStateWire` and `steeringReceiptWire` (`turn.go:485` and `500`), returning the `acpwire` types. `OperationKind` and `Status` are `string(…)`.
2. **`acp/turn.go`.**
   - Delete `SteeringStateWire`, `SteeringOutcomeWire` and `SteeringReceiptWire` with their comments (1634–1668).
   - Delete `steeringStateWire` and `steeringReceiptWire` with their doc comments (483–522).
   - `steeringStateUpdate` (528) and `SteeringStatus` (1800, 1806) call `viewmodel.SteeringStateOf`. `steeringReceiptUpdate` (549) and `sendNow` (1757) call `viewmodel.SteeringReceiptOf`.
   - `SteeringStatus`'s two literals (1787, 1789) are `acpwire.SteeringState`.
   - `steeringStateUpdate`'s doc (525) names `acpwire.SteeringState`.
3. **Tests.** In `turn_test.go` and `permissions_test.go` (935), `SteeringStateWire` becomes `acpwire.SteeringState`, `SteeringReceiptWire` becomes `acpwire.SteeringReceipt` and `SteeringOutcomeWire` becomes `acpwire.SteeringOutcome`. `TestACPSendNowQueuedCompatibilityAndWireRoundTrip` (`turn_test.go:4677`) calls `viewmodel.SteeringReceiptOf` where it called `steeringReceiptWire` (4787), and its failure message names the new function.
4. **`viewmodel/steering_test.go`.**
   - `TestSteeringStateOfCopiesEveryField`: a snapshot with all eight fields set maps to the expected `acpwire.SteeringState`.
   - `TestSteeringReceiptOfCopiesEveryField`: a receipt with every field and one outcome set maps to the expected value. Changing the source's outcome afterwards leaves the result unchanged.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'Steering(State|Receipt|Outcome)Wire|steering(State|Receipt)Wire\(' internal
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/viewmodel/ ./internal/acp/ 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
go vet ./internal/viewmodel/ ./internal/acp/
```

Expected: the grep prints nothing, both packages pass, and the two new tests run.

## Task 3: The adapter's status reads

**Goal:** `Status` carries the steering queue and snapshot, the running job count, the subagent views, swarm and SDD progress, the SDD gate and git info.

**Files:** `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/runs.go`, `internal/acpclient/inproc/runs_test.go`, `internal/acpclient/inproc/inproc_test.go`.

**Steps:**

1. **`Deps` gains `GitInfo`** (Contracts). `New` keeps its `Deps` on the `Host`. `New` and `Swap` both build a session through `newSession(d Deps) *Session`, `Swap` from a copy of the host's `Deps` with `State` set to its argument. The session keeps `d.GitInfo`, or `gitinfo.Read` when it is nil.
2. **`Status`** fills the new fields:

   | Field | From |
   |---|---|
   | `Steering.Queue` | `SteeringQueue()` |
   | `Steering.State` | `viewmodel.SteeringStateOf(SteeringSnapshot())` |
   | `RunningJobs` | `RunningJobsCount()` |
   | `Subagents` | `Subagents()`, through `subagentsOf` |
   | `Swarm` | `SwarmProgress()`, through `swarmOf` |
   | `SDD` | `SDDProgress()`, through `sddOf` |
   | `SDDGate` | `SDDGate()`, field by field |
   | `Git` | the session's git reader on the `ActiveRoot` of the workspace `Status` already reads, through `gitOf` |

   `Steering.Receipt` and `WatchResume` stay nil until Task 4.
3. **`runs.go`** holds the conversions:
   - `subagentsOf(views []session.SubagentView) []acpwire.Subagent` returns nil for none.
     - `Status` maps `SubagentRunning`, `SubagentDone` and `SubagentFailed` to the `acpwire` constants.
     - `Role` is `string(v.Role)`.
     - `Headline` is set only when the view is running and has a `Child`. It is computed as the TUI's `subagentHeadline` (`steps.go:703`) does: the child's trimmed `LatestNarrationLine()`, cut with `viewmodel.FirstSentence` and cleaned with `viewmodel.StripEmphasis`, and "" for an empty line.
   - `swarmOf(p session.SwarmProgress) acpwire.SwarmProgress` copies every field. Each role's `Status` is `string(…)` and its `StartedAt` goes through `acpwire.Millis`.
   - `sddOf(p session.SDDProgress) acpwire.SDDProgress` copies every field into new slices, with a nil slice staying nil. Every time, the task timings included, goes through `acpwire.Millis`.
   - `gitOf(i gitinfo.Info) acpwire.Git` copies the three fields.
4. **`TestStatusMirrorsTheState`** (`inproc_test.go:88`) builds its session with a `GitInfo` that returns `{Branch: "feature", Worktree: "wt", InRepo: true}`. It keeps the view `RegisterSubagent` returns, and its `want` gains:
   - `Subagents`: one entry with that view's ID, `Label` "scout" and `Status` `acpwire.SubagentRunning` (the child has no narration, so no headline);
   - `Git`: the fake's value.

   The other new fields stay zero in this test.
5. **`runs_test.go`** (T5):
   - `TestStatusCarriesTheSteeringQueueAndState`: after `PushSteering("a")` and `PushSteering("b")`, `Steering.Queue` is `[a b]`. After `SetOperationInterrupt` with a no-op handler and `BeginSteeringTurn`, `Steering.State` has `Active` and `Supported` set and the turn's ID.
   - `TestStatusCarriesTheRunningJobCount`: after `SetRunningJobsCount(3)`, `RunningJobs` is 3.
   - `TestStatusCarriesSubagentViews`: register two children with `RegisterSubagentWithMeta`, each with a role, provider, model and `Fallback` true. The first child holds a narration message (`ContentTypeNarration`) `**Reading** the config. Then the tests.`. Finish the second with `FinishSubagent(id, "done", nil)`. The first view has `Status` running and `Headline` `Reading the config.`, the second has `Status` done and no headline, and both carry their meta.
   - `TestStatusCarriesSwarmAndSDDProgress`: `SetSwarmProgress` with one role started at `time.Unix(100, 5_000_000)`, `SetSDDProgress` with every field set (times and one task timing at `time.Unix(…)` values) and `SetSDDGate` with all four fields. `Swarm`, `SDD` and `SDDGate` equal the expected literals, with every time in milliseconds (the role's `StartedAt` is `100005`).
   - `TestStatusReadsGitForTheActiveRoot`: a `GitInfo` fake records the directory it is asked for. After `SetWorkspace` with `ActiveRoot` "/project/wt", `Git` is the fake's value and the fake saw "/project/wt".

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/...
```

Expected: the five new tests pass, along with the updated `TestStatusMirrorsTheState` and the rest of the package.

## Task 4: Changed and the events

**Goal:** the adapter subscribes to the five brokers, keeps the receipts and the pending watch resume, and fires `Changed`. `Run` hands it the brokers.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`, `internal/acpclient/inproc/events_test.go`, `internal/app/app.go`.

**Steps:**

1. **`acpclient.go` and `methods.go`.** Add `Changed` to `Session` after `Status`, with the doc from Contracts, and its row to `Methods`.
2. **`Deps`** gains `Context` and the five brokers (Contracts).
3. **`Session`** gains a cancel function, the `changed` channel (capacity 1), a mutex, a flag recording that `changed` is closed, the receipts by request ID, the resume slot and its sequence.
4. **`events.go`:**
   - `newSession` derives a cancellable context from `d.Context`, or from `context.Background()` when it is nil, and subscribes to each non-nil broker with it. The watch subscription passes `pubsub.WithTerminal[watch.Event]()`, and the others pass no option. When at least one broker was subscribed, it starts `run` in a goroutine.
   - `run` selects on the context and the five channels. A channel that was never subscribed is nil, so it never fires. A channel that closes is set to nil. `run` returns when the context is done or every channel it held has closed, and it closes `changed` on the way out.
     - A watch event whose `Resume` is set calls `noteResume(name, ev.Mode == watch.ModeRepeat)`.
     - A steering event with a receipt calls `absorbReceipt`.
     - Every event, these included, then calls `signal`.
   - `signal` sends on `changed` without blocking, unless it is closed. `closeSignal` closes it once. Both hold the mutex, so a send never races the close.
   - `absorbReceipt(r session.SteeringReceipt)` keeps `r` unless the stored receipt for its request ID has a version at least as high, as the TUI's `absorbSteeringEvent` (`model.go:5860`) does.
   - `latestReceipt(turnID string) (session.SteeringReceipt, bool)` returns the stored receipt with the highest version among those for `turnID`, or among all of them when `turnID` is "", as the TUI's `latestSteeringReceipt` (`model.go:6216`) does.
   - `noteResume(name string, repeat bool)` increments the sequence and replaces the slot with `{name, repeat, seq}`.
   - `close` marks the session closed, cancels its context and calls `closeSignal`. `Swap` calls it on the session it replaces, in place of setting `closed` alone.
5. **`Status`**:
   - `Steering.Receipt` points at `viewmodel.SteeringReceiptOf` of `latestReceipt(Steering.State.TurnID)`, or is nil when there is none.
   - `WatchResume` is a copy of the slot, or nil.
6. **`Changed`** returns the channel.
7. **`app.go:2803`.** `inproc.New` gets `Context: ctx` and the five brokers `Run` already unwraps at 2795–2799. Move the comment at 2864–2866 ("Workspace events must flow even when the initial provider build failed…") above this call and make it about all five: the brokers exist whatever the provider's state, so the session's events flow from the first frame. The TUI's own broker options stay until Task 9.
8. **`events_test.go`** (T5). `waitChanged(t, sess)` fails unless `Changed` fires within 2 s.
   - `TestChangedFiresOnEachBroker`: a table over the five brokers. Publishing one event on each fires `Changed`.
   - `TestChangedHoldsOneSignal`: `cap(sess.Changed())` is 1.
   - `TestAResumeFireSetsThePendingResume`:
     - a watch event named "build", in repeat mode, with `Resume` set gives `WatchResume` `{build, true, 1}`;
     - a later event without `Resume` leaves it as it was;
     - an event named "lint", in once mode, with `Resume` set gives `{lint, false, 2}`.
   - `TestReceiptsKeepTheNewestRevisionPerRequest`: use a state with a no-op interrupt handler and an open steering turn, so the snapshot names the turn. Publish steering events carrying receipts for that turn's ID, and after each, wait for `Changed` and check `Steering.Receipt`:
     - `req-a` version 2 interrupting gives interrupting;
     - `req-a` version 3 delivered gives delivered;
     - `req-a` version 2 again leaves it delivered;
     - `req-b` version 1 accepted still gives `req-a` delivered, because it has the highest version.
   - `TestSwapClosesChanged`: after `Swap`, receiving from the old session's `Changed` reports closed within 2 s. With a broker, the new session's `Changed` fires on a publish. The old one is closed even when it had no broker.
   - `TestRunEndsWhenItsBrokersClose`: a session with only a job broker. After `Close` on the broker, `Changed` reports closed within 2 s.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-2b-suite go test -race ./internal/acpclient/... -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/acpclient/... ./internal/app/
```

Expected:
- the race-enabled run passes, the six new tests among it, and so do `TestMethodTableCoversTheInterfaces` and `TestMethodDocsNameTheirACPMethod`;
- the ACP table test passes, because `Changed` has no ACP method;
- the TUI and app packages fail only on Task 11's baseline list.

## Task 5: SendNow on the session

**Goal:** `Session.SendNow` submits a Send now request and records its receipt before returning.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/sendnow_test.go`.

**Steps:**

1. **`acpclient.go` and `methods.go`.** Add `SendNow` after `Changed`, with the doc from Contracts, and its row. `acpclient.go` imports `context`.
2. **`inproc.go`, `SendNow`.**
   - A closed session returns the closed-session error from the pinned strings, kept in a package variable.
   - Otherwise it calls `s.state.SubmitSendNow` with the request's four fields as a `session.SendNowRequest`.
   - A refusal is returned unchanged, so the TUI shows its text as it does today.
   - On acceptance it calls `absorbReceipt` and `signal`, then returns `viewmodel.SteeringReceiptOf` of the receipt.
   - The doc says acceptance is a bounded, memory-only call, so `ctx` is not consulted. That is the contract `WithSendNow` states today (`model.go:1214`).
3. **`sendnow_test.go`** (T5):
   - `TestSendNowRecordsTheReceiptBeforeReturning`: use a state with a no-op interrupt handler and an open steering turn, and a session with no brokers. `SendNow` with the state's session ID, the turn, request "r1" and text "steer" returns an accepted receipt. `Status().Steering.Receipt` names "r1" at once, `Steering.Queue` holds "steer", and `Changed` holds a signal.
   - `TestSendNowReturnsTheStatesRefusal`: with no steering turn open, `SendNow` returns an error, and `Steering.Receipt` stays nil.
   - `TestSendNowOnAClosedSessionFails`: after `Swap`, the old session's `SendNow` returns an error for which `acpclient.IsCode(err, acpwire.CodeSessionClosed)` holds.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/...
```

Expected: the three new tests pass, and so does the ACP table test, because `session/steer` is registered.

## Task 6: Send now and the steering queue through the session

**Goal:** the TUI submits Send now through `Session.SendNow` and reads the queue, the snapshot and the receipt from `Status`. Its own steering caches and `WithSendNow` are gone.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/keypress.go`, `internal/app/tui/status.go`, `internal/app/tui/viewport_render.go`, `internal/app/app.go`, and the tests `internal/app/tui/seam_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/pump_test.go`, `internal/app/tui/drill_test.go`, `internal/app/tui/status_test.go`, `internal/app/app_test.go`.

**Steps:**

1. **Delete** `WithSendNow` (`model.go:1214`), the fields `queuedCount` (519), `steeringSnapshot` (526), `steeringReceipts` (531), `steeringSnapshotFn` and `submitSendNowFn` (535–536) with their comments, and the methods `absorbSteeringEvent` (5860), `refreshSteeringSnapshot` (5886) and `latestSteeringReceipt` (6216).
2. **`handleSteering`** (5840) only refreshes the viewport and re-arms its pump. `steeringMsg` keeps its fields until Task 9 deletes it.
3. **`sendNowTarget`** (5917) reads `snap := m.sess.Status().Steering.State` and returns it when `m.busy`, `snap.Active` and `snap.Supported` all hold. Otherwise it returns the zero value and false. The doc drops the "no runtime seam" case.
4. **`submitSendNow`** (5940) takes an `acpwire.SendNowRequest`.
   - It mints the request ID as now.
   - It calls `m.sess.SendNow(m.ctx, request)`, and on an error rejects with today's text.
   - On acceptance it runs `clear` and refreshes the viewport.
   - The receipt cache, the queue mirror and the snapshot refresh (5955–5962) go.

   The four request literals become `acpwire.SendNowRequest`: `commands_dispatch.go:116` and `:141`, and `model.go:6042` and `6198`.
5. **Queue reads.**
   - Each of these lines, which only mirror the queue length into `queuedCount`, goes: `keypress.go:389` and `:455`; `model.go:4273` (with its comment), `4597`, `5416` (`finishQuit`) and `5568`.
     - `popOldestSteering` (4268) then returns `m.state.PopSteering()`'s result directly.
     - Where a comment explains the mirror ("The surviving depth is re-read", at `keypress.go:386` and `model.go:5565`), drop that sentence.
   - These reads become `len(m.sess.Status().Steering.Queue) == 0`, inline: `sendNowFlushOrOpen` (`commands_dispatch.go:136`), `beginSendNow` (`model.go:6038`) and `handleComposerSubmit` (`model.go:6193`).
   - `refreshViewport` (`viewport_render.go:88`): `queued := m.sess.Status().Steering.Queue`.
6. **`statusLeftSegments`** (`status.go:257`), using its `st`:
   - the queued segment (436) reads `len(st.Steering.Queue)`;
   - the Send-now segment (443) calls `m.steeringStatusSegment(st.Steering.Receipt)`.
7. **`steeringStatusSegment`** (`model.go:6233`) takes the receipt. A nil receipt returns "", false. The rest of the body is unchanged, with each status compared against its `acpwire` constant.
8. **`footerHints`** (`status.go:555`) takes one `st`:
   - `IdleRollbackEligible` reads `st.HasBackup`;
   - `QueueNonEmpty` is `len(st.Steering.Queue) > 0`;
   - `SendNowAvailable` is `m.busy && st.Steering.State.Active && st.Steering.State.Supported`.
9. **`handleAgentTick`** (6811): the `refreshSteeringSnapshot` call and its comment (6817–6820) go.
10. **`app.go`**: the `WithSendNow` line and its comment (2831–2836) go. In `app_test.go:6679`, the `WithSendNow` option goes.
11. **Tests.**
    - **`seam_test.go`**: add `stubSession` and `testModelStub` (Contracts). `patch` runs over the embedded session's value. `onSendNow`, when set, is called before the embedded `SendNow`.
    - **`newSendNowFixture`** (`model_test.go:10176`) builds its model with `testModelStub`, through a stub whose `onSendNow` appends to `f.submits`. The `submits` field holds `acpwire.SendNowRequest`. The `refreshSteeringSnapshot` call (10203) goes.
    - **`TestSendNowReceiptVersionsAndSlowStopRemainTruthful`** (10515) becomes `TestSendNowStatusSegmentFollowsTheReceipt`. The stub's `patch` sets `Steering.Receipt` for the fixture's turn to interrupting, then waiting for a safe boundary, then delivered. The status line shows `send now: interrupting`, `send now: waiting for a safe boundary` and `send now: delivered` in turn, and the interrupting line contains neither "stopped" nor "delivered". The version rules are Task 4's `TestReceiptsKeepTheNewestRevisionPerRequest`.
    - **`TestSendNowFinishDoesNotDropAcceptedFeedback`** (10668): the depth check reads `len(f.m.sess.Status().Steering.Queue)`. The receipt check fails when `f.m.sess.Status().Steering.Receipt` is the submitted request's and is delivered.
    - **`pump_test.go`**: delete `TestPumpSteeringEventsCarriesSnapshotAndReceipt` (109) and `TestModelSteeringMsgWithoutBrokerCachesState` (141). The caches they cover are gone.
    - **The rule for `queuedCount` in tests:**
      - delete an assignment `m.queuedCount = N` where the test has already pushed N entries onto the state;
      - replace it with N `PushSteering` calls where it has not, as in `TestCancelTurnDoesNotClearSteeringWhenAgentAlreadyFinished` and `TestStatusLineShowsQueueHint` (`status_test.go:421`);
      - a read of `m.queuedCount` becomes `len(m.sess.Status().Steering.Queue)`.

      The sites are in `drill_test.go` (97, 122), `model_test.go` (3454–3623, 4623–4637) and `status_test.go` (423).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE '\b(queuedCount|steeringSnapshot|steeringReceipts|submitSendNowFn|refreshSteeringSnapshot|WithSendNow)\b' --include='*.go' internal/app/tui internal/app/app.go internal/app/app_test.go
grep -rnE 'm\.state\.SteeringQueue\(' --include='*.go' internal/app/tui | grep -v '_test.go:'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/ -run 'SendNow|Steering|Queue|CtrlX|Cancel|FollowUp|Composer' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/... ./internal/app/
```

Expected: both greps print nothing, the named tests pass, and both packages fail only on Task 11's baseline list.

## Task 7: Jobs, git and subagents

**Goal:** the job count, git info, the running-subagent hint and the tray's agent rows come from `Status`. The TUI's job and git caches are gone.

**Files:** `internal/app/tui/status.go`, `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/livebox_view.go`, `internal/app/tui/steps.go`, `internal/app/tui/sessionsheet/section.go`, and the tests `internal/app/tui/seam_test.go`, `internal/app/tui/status_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/workspace_msg_test.go`, `internal/app/tui/click_test.go`, `internal/app/tui/sessionsheet/section_repo_test.go`.

**Steps:**

1. **Jobs.**
   - Delete the fields `jobCount` and `jobs` (`model.go:496` and `499`), the comment over `jobs`, and the comment's sentences about `jobCount` above `jobBroker`.
   - `handleJobCount` (5763) stops setting them. It still refreshes, flushes and re-arms.
   - In `statusLeftSegments`, the broker-or-fallback block (`status.go:428–433`) becomes one check: `st.RunningJobs > 0` gives the `jobs N` segment.
   - `modeReceipt` (`model.go:7316`) reads `m.sess.Status().RunningJobs > 0`.
2. **Git.**
   - Delete `gitInfo` and `lastGitRead` (`model.go:586–590`) with their comment.
   - In `statusLeftSegments`, the branch and worktree segments (397–401) read `st.Git`.
   - `sheetData` (2125) passes `Git: st.Git` from one `st`. Task 8 adds that function's other reads to the same `st`.
   - `childSheetData` (2166) passes `Git: m.sess.Status().Git`.
   - These git reads go: in `New` (1910–1911), keeping the `HeadSHA` line; in `update`'s `WindowSizeMsg` block (2432–2435, with their comment); in `handleWorkspaceMsg` (6270–6271), which keeps only `baseCmd = sheetBaseRefCmd(msg.activeRoot)` under its non-empty check; in `handleAgentTick` (6812–6816, with its `st` if nothing else uses it); and in `startNewSession` (`commands_dispatch.go:203–204`).
   - `sessionsheet.Data.Git` (`sessionsheet/section.go:42`) is `acpwire.Git`, and the file drops `gitinfo`.
3. **Subagents.**
   - `footerHints` sets `RunActive: st.SubagentRunning`, and `hasRunningSubagent` (`status.go:584–592`) goes.
   - `boxInput` (`livebox_view.go:55`) takes one `st` at its top. Task 8 adds that function's other reads to it.
   - The agent loop finds the view whose `ID` matches in `st.Subagents` instead of calling `m.state.Subagent(id)` (116), and skips the item when there is none. It still sets `in.Agents[it.TaskKey]` from the view's model, provider and fallback.
   - It sets `in.Headlines[it.TaskKey] = v.Headline` only when `viewmodel.WorkActive(it)` holds and the headline is not empty, creating the map the first time. `livebox` treats a missing headline and an empty one alike (`headlined`, `livebox/running.go:294`).
   - Delete `subagentHeadline` (`steps.go:701–713`).
4. **Tests.**
   - **`seam_test.go`**: add `testModelWith` (Contracts).
   - **`status_test.go`**: the tests that set `m.gitInfo` build their model with `testModelWith`, through a `GitInfo` that returns a local variable the test sets: `TestStatusLineShowsGitBranch` (430), `TestStatusLineShowsWorktree` (439), `TestStatusLineOmitsGitSegmentsWhenNotInRepo` (448), `TestStatusLineCollapseDropsWorktreeBeforeBranch` (460) and `TestStatusLineDropsHintsBeforePathAndWorktree` (694). Their expectations are unchanged.
   - **`TestWorkspaceMsgUpdatesGitInfoWhenDockOpen`** (`status_test.go:568`) becomes `TestStatusLineFollowsTheActiveRootsBranchWhenDockOpen`. Keep its real repository on branch `pipeline/feature`. With a dock panel open, `SetWorkspace` with `ActiveRoot` that repository makes the rendered status line show `⎇ pipeline/feature`, with no message sent.
   - **`model_test.go`**:
     - delete `TestStatusLineJobCountFromBroker` (3398) and `TestJobCountMsgUpdatesModel` (3422);
     - rename `TestStatusLineJobCountFallbackWhenNoBroker` (3411) to `TestStatusLineShowsTheRunningJobCount`, with an unchanged body;
     - delete `TestGitInfoPopulatedOnResize` (7324) and `TestGitInfoRefreshThrottled` (7332). The throttle is gone, and Task 3's `TestStatusReadsGitForTheActiveRoot` covers the read;
     - in `TestNewSessionRereadsGitInfo` (2777), drop the `gitInfo` and `lastGitRead` presets and their checks, and check `m.sess.Status().Git.Branch == "feat-new"`;
     - in `TestDockedSettingsBrowserDoesNotSwallowRuntimeMessages` (6695), drop the `jobCount` check. The panel check stays.
   - **`workspace_msg_test.go`**: delete `TestHandleWorkspaceMsgRereadsGitInfo`. `TestHandleWorkspaceMsgRebasesRail` covers the base-ref command.
   - **`click_test.go:216`**: delete the `m.jobs` line.
   - **`sessionsheet/section_repo_test.go`**: its `gitinfo.Info` literals become `acpwire.Git`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE '\bgitInfo\b|lastGitRead|\bjobCount\b|m\.jobs\b|hasRunningSubagent|subagentHeadline|gitinfo\.Read' --include='*.go' internal/app/tui | grep -v '_test.go:'
grep -rnE 'm\.state\.(RunningJobsCount|Subagent)\(' --include='*.go' internal/app/tui | grep -v '_test.go:'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/ -run 'StatusLine|Git|Job|Box|Tray|Agent|Subagent|NewSession|Workspace' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/... ./internal/app/
```

Expected:
- the first grep prints nothing;
- the second prints only `m.state.Subagent(id)` in `boxAgentAtCursor` (`browse.go:361`), a drill-in read that plan 3 moves;
- the named tests pass, and both packages fail only on Task 11's baseline list.

## Task 8: Run progress

**Goal:** swarm and SDD progress and the SDD gate come from `Status`, and the helpers that render them take the wire types.

**Files:** `internal/app/tui/status.go`, `internal/app/tui/keypress.go`, `internal/app/tui/livebox_view.go`, `internal/app/tui/model.go`, `internal/app/tui/view.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/run_panel.go`, `internal/app/tui/runoutcome.go`, `internal/app/tui/eta.go`, `internal/app/tui/livestrip.go`, `internal/app/tui/gatepanel/gatepanel.go`, `internal/app/tui/sessionsheet/section.go`, `internal/app/tui/sessionsheet/section_swarm.go`, `internal/app/tui/sessionsheet/section_sdd.go`, and the tests `internal/app/tui/run_panel_test.go`, `internal/app/tui/eta_test.go`, `internal/app/tui/runoutcome_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/gatepanel/gatepanel_test.go`, `internal/app/tui/sessionsheet/section_sdd_test.go`, `internal/app/tui/sessionsheet/section_swarm_test.go`.

This task spans three packages because `sessionsheet.Data` and `gatepanel.New` take the values the TUI now reads. Their change is a type swap.

**Steps:**

1. **Helper types** (Contracts). Each body reads the same fields. Where it computes with a time, it converts it first with `acpwire.Time`:
   - `run_panel.go`: 69–70, 84–85 and 123;
   - `runoutcome.go`: 45, 61 and 157–165;
   - `eta.go`: 35–39 and 83.

   `swarmStripText` (`livestrip.go:14`) compares role statuses with the `acpwire` constants.
2. **`gatepanel`.** The `gate` field (44) and `New`'s parameter (51) are `acpwire.SDDGate`.
3. **`sessionsheet`.**
   - `Data.Swarm` and `Data.SDD` (`section.go:79`, `83`) take the wire types.
   - `roleGlyph` (`section_swarm.go:28`) takes the status string and compares it with the `acpwire` constants, and so do lines 66 and 83.
   - `elapsed(r.StartedAt, d.Now)` (64) passes `acpwire.Time(r.StartedAt)`.
   - `section_sdd.go:39` converts both times with `acpwire.Time`.
4. **Reads** (decision 8):
   - `transientStatusSegment` (`status.go:231`): `m.sess.Status().SDD.Active`, inline;
   - `statusLeftSegments`: `st.Swarm` (404) and `st.SDD` (410);
   - `clearFinishedRun` (`keypress.go:505`): inline. The two clears that follow stay on `m.state`;
   - `boxInput` (`livebox_view.go:69–70`): `st.SDD` and `st.Swarm`, from Task 7's `st`;
   - `sheetData` (`model.go:2138–2139`): `st.Swarm` and `st.SDD`, from Task 7's `st`;
   - `update`'s `gatepanel.AnswerMsg` case (2821): `m.sess.Status().SDD.PlanPath`, inline;
   - `inputChromeRows` (3775): inline;
   - `activeSpinnerFrame` (4849): take `st` before the SDD check (4853), and the activity read at 4856 becomes `act := st.Activity`;
   - `handleAgentFinished` (5598): `gate := m.sess.Status().SDDGate`, inline. This function's other status read (5632) is in a later block, so it stays inline too;
   - `renderInputArea` (`view.go:263`) and `inputBarColor` (`view.go:290`): inline;
   - the `/run` closure (`commands_dispatch.go:442`) takes one `st` and passes `st.SDD` and `st.WorkingDir` to `runOutcomeDoc`.
5. **Tests.**
   - A literal passed to a helper, `gatepanel.New` or `sessionsheet.Data` becomes the `acpwire` type. Its times go through `acpwire.Millis`, and its role statuses are `acpwire` constants. That covers `run_panel_test.go`, `eta_test.go`, `runoutcome_test.go`, `gatepanel_test.go`, `section_sdd_test.go`, `section_swarm_test.go` and `model_test.go:9342`.
   - A literal passed to `SetSDDProgress`, `SetSwarmProgress` or `SetSDDGate` keeps the session type.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'session\.(SDDProgress|SwarmProgress|SwarmRole[A-Za-z]*|SDDGate|TaskTiming)\b' --include='*.go' internal/app/tui | grep -v '_test.go:'
grep -rnE 'm\.state\.(SDDProgress|SwarmProgress|SDDGate)\(' --include='*.go' internal/app/tui | grep -v '_test.go:'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/ -run 'RunPanel|Run|SDD|Swarm|Eta|Gate|Sheet|Spinner|Status' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/... ./internal/app/
```

Expected: both greps print nothing, the named tests pass apart from the baseline's `TestStatusLineUntrustedIsWarningColored`, and both packages fail only on Task 11's baseline list.

## Task 9: One change pump

**Goal:** the TUI's five broker pumps are replaced by one pump on `Session.Changed`, and its handler does what theirs did.

**Files:** `internal/app/tui/events.go`, `internal/app/tui/pump.go`, `internal/app/tui/model.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/app.go`, and the tests `internal/app/tui/seam_test.go`, `internal/app/tui/pump_test.go`, `internal/app/tui/watches_test.go`, `internal/app/tui/watch_resume_test.go`, `internal/app/tui/workspace_msg_test.go`, `internal/app/tui/jobs_test.go`, `internal/app/tui/model_options_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/status_test.go`.

**Steps:**

1. **Delete:**
   - the options `WithJobBroker`, `WithWatchBroker`, `WithSteeringBroker`, `WithWorkspaceBroker` and `WithSubagentBroker`, between `model.go:1167` and `1247`. `WithWork` (1196) sits among them and stays;
   - their fields and caches (`jobBroker`, `jobEvents`, `watchBroker`, `watchEvents`, `watches`, `steeringBroker`, `steeringEvents`, `workspaceBroker`, `workspaceEvents`, `subagentBroker` and `subagentEvents`) with their comments;
   - the five subscriptions in `New` (1871–1885);
   - the five pumps (`pump.go:15–106`);
   - the five messages (`events.go:58–102`);
   - the five handlers (`handleJobCount`, `handleWatchMsg`, `handleSteering`, `handleWorkspaceMsg` and `handleSubagentMsg`).

   `pump.go` keeps `pumpWorkEvents`, `sheetBaseRefCmd` and `sheetChangedFiles`.
2. **Add** `changedMsg` (`events.go`), `pumpChanged` (`pump.go`) and `WithContext` (`model.go`), as declared in Contracts.
   - `pumpChanged` returns nil for a nil channel. Otherwise it blocks until a signal arrives or the channel closes. A closed channel gives a nil message, which ends the pump. A signal gives `changedMsg{ch}`.
   - `WithContext` sets `m.ctx`.
3. **Model fields:**
   - `watchResumeSeq uint64` is the last watch-resume sequence the model acted on;
   - `workspaceSeen acpwire.Workspace` is the workspace the sheet's base ref last followed.

   `New` sets `workspaceSeen` from its `st`.
4. **`Init`** (1953) appends `pumpChanged(m.sess.Changed())` in place of the five pump appends. The work pump stays.
5. **`handleChanged`:**
   1. When `msg.ch` is not `m.sess.Changed()`, it returns the model and no command: that pump belonged to a replaced session.
   2. It takes one `st` and starts its commands with `pumpChanged(msg.ch)`.
   3. When `st.Workspace` differs from `workspaceSeen`, it records the new value. When that value's active root is not empty, it adds `sheetBaseRefCmd(root)`.
   4. When `st.WatchResume` is set and its `Seq` is greater than `watchResumeSeq`, it records the sequence. It then calls `m.startWatchResumeRun(name, repeat)` and adds the command when it gets one.
   5. It refreshes the viewport, adds `flushPendingModelOptions()`'s command when there is one, and returns them all in one `tea.Batch`.
6. **Routing.**
   - In `update`'s runtime list (`model.go:2649`), `changedMsg` replaces `jobCountMsg`, `steeringMsg`, `workspaceMsg`, `subagentMsg` and `watchMsg`.
   - In `handleRuntimeMessage`'s ending list (6885), `changedMsg` replaces `jobCountMsg`, `subagentMsg`, `watchMsg`, `workspaceMsg` and `steeringMsg`.
   - `dispatchRuntimeMessage` (6913) loses those five cases and gains `case changedMsg: return m.handleChanged(msg)`.
7. **`startNewSession`** (`commands_dispatch.go:188`). When the swap returned a session, it resets `watchResumeSeq` to 0. It sets `workspaceSeen` from one `st` taken after the swap (which also serves the base-ref read at 245). It adds `pumpChanged(m.sess.Changed())` to the commands it returns. When the swap returned no session, the old pump is still live and nothing changes.
8. **`app.go`.** Delete `WithWorkspaceBroker` and `WithSubagentBroker` (2867–2868) with the comment above them, if Task 4 left any of it, and, inside the provider block, `WithJobBroker`, `WithWatchBroker` and `WithSteeringBroker` (2877, 2878 and 2880). `WithWork` (2879) stays. In place of the first two, add `tui.WithContext(ctx)`, outside the provider block.
9. **Tests.**
   - **`seam_test.go`**: add `batchResults` (Contracts).
   - **`pump_test.go`**: delete `TestPumpBridgesJobEventsToMsgs`, `TestPumpReturnsNilOnCtxCancel` and `TestModelJobPumpRearmsWithoutLeakingTerminalSubscriptions`, and add:
     - `TestPumpChangedCarriesItsChannel`: after a signal on a channel, the pump's message is a `changedMsg` holding that channel;
     - `TestPumpChangedEndsOnClose`: on a closed channel, the pump returns nil;
     - `TestHandleChangedIgnoresAReplacedSession`: register a subagent, so a refresh would change the transcript. A `changedMsg` holding another channel then returns a nil command and leaves `lastTranscriptHash` as it was, while one holding `m.sess.Changed()` returns a command and changes it.
   - **`watches_test.go`**: delete the file. Its two tests cover the deleted cache and pump, and its two event builders lose their callers.
   - **`watch_resume_test.go`**:
     - a helper `withWatchResume(m *Model, name string, repeat bool, seq uint64) changedMsg` wraps `m.sess` in a `stubSession` whose `patch` sets `WatchResume`, and returns `changedMsg{ch: m.sess.Changed()}`;
     - the other six `TestWatchResume*` tests call `m.handleChanged(withWatchResume(&m, "build", repeat, 1))` in place of `handleWatchMsg`, with `repeat` true where the test built its event with `watch.ModeRepeat`;
     - `TestWatchResumeEventNotBearingNoWake` becomes `TestNoPendingResumeNoWake`, which sends a plain `changedMsg{ch: m.sess.Changed()}`;
     - add `TestWatchResumeActsOncePerSequence`: sequence 1 while busy starts nothing; sequence 1 again once idle starts nothing; sequence 2 starts the turn;
     - the latch tests are unchanged.
   - **`workspace_msg_test.go`**: `TestHandleWorkspaceMsgRebasesRail` becomes `TestChangedRebasesTheSheetWhenTheWorkspaceMoves`. It calls `handleChanged` after `SetWorkspace` and finds the `sheetBaseRefMsg` among `batchResults` of the returned command. Its checks are unchanged. Add `TestChangedLeavesTheSheetWhenTheWorkspaceStays`: a second `handleChanged` with the same workspace yields no `sheetBaseRefMsg`.
   - **Routing tests**: each test that sent `jobCountMsg`, `subagentMsg`, `workspaceMsg` or `watchMsg` only to drive a runtime message sends `changedMsg{ch: m.sess.Changed()}` instead. That covers `TestDockedSettingsBrowserDoesNotSwallowRuntimeMessages`, `TestWarningRefreshesFromRuntimeEvents` (9386), `TestOptionsPendingChangeFlushesWhenIdle` and `TestOptionsReloadFailureRetainsPendingCandidate` (`model_options_test.go:165` and `214`), and `TestJobCountRepaintsViewport` (`jobs_test.go:40`), renamed `TestChangedRepaintsViewport`.
   - **Subagent tests**: delete `TestSubagentBrokerPublishesRefreshMessage` (`model_test.go:8176`), which Task 4's `TestChangedFiresOnEachBroker` covers. `TestHandleSubagentMsgRefreshesViewport` (8199) becomes `TestChangedRefreshesTheViewportForANewSubagent`, which registers the subagent and calls `handleChanged`.
   - **`status_test.go`**: any test still sending `workspaceMsg` follows the routing rule.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'jobCountMsg|watchMsg|steeringMsg|workspaceMsg|subagentMsg|With(Job|Watch|Steering|Workspace|Subagent)Broker|pump(Job|Watch|Steering|Workspace|Subagent)Events' --include='*.go' internal/app
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/ -run 'Changed|Pump|WatchResume|Resume|Workspace|Runtime|Warning|Options|NewSession|Repaint' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/... ./internal/app/
```

Expected: the grep prints nothing, the named tests pass, and both packages fail only on Task 11's baseline list.

Then run `./marshal` (built with `CGO_ENABLED=1 go build ./cmd/marshal`) in this repository and check by eye:
- the branch segment shows;
- typing while a turn runs shows `queued N`;
- Alt+Enter mid-turn shows the `send now:` segment.

## Task 10: The guard and the allowlist

**Goal:** T1b keeps the 2b reads moved, and T1's allowlist drops the imports the TUI no longer makes.

**Files:** `internal/app/tui/moved_reads_test.go`, `internal/app/tui/boundary_allowlist_test.go`.

**Steps:**

1. **`movedStateReads`** gains these seven names, each mapped to `"2b"`: `SteeringQueue`, `SteeringSnapshot`, `SubmitSendNow`, `RunningJobsCount`, `SwarmProgress`, `SDDProgress` and `SDDGate`.
2. **The allowlist.** Run `TestTUIStaysOnTheClientSeam`. For each "no longer uses" error, delete that entry from `boundaryAllowlist`, and delete a file's key when its list empties. Never add an entry: a "uses …, which is agent side" error means a task left an agent-side call behind, and that task's change is what needs fixing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2b-suite go test ./internal/app/tui/ -run 'TestMovedStateReadsStayMoved|TestMovedStateReadsAreRealNames|TestMovedReadsGuardFindsSelectors|TestTUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/tui/
```

Expected: all four pass.

As a check of the guard (no commit), temporarily add `_ = m.state.SDDProgress()` to `transientStatusSegment` and see `TestMovedStateReadsStayMoved` fail with the T1b string naming `status.go` and plan 2b. Then revert it.

## Task 11: Verification and docs

**Goal:** the full suite matches the baseline, and the spec and design record plan 2b.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 2b entry to the Implementation record. It lists:
   - the wire types, `Changed` and `SendNow`;
   - the steering types moved out of `acp`;
   - the deleted pumps, caches and options;
   - the seven names added to T1b;
   - the allowlist entries Task 10 removed, by count.
2. **`design.md` §7.** The P2 row's status adds `plan 2b implemented` after `plan 2a implemented`.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-2b-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-2b-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the failures are exactly the baseline below. A flaky one counts only if it passes alone with `-count=3`.

**Baseline at `2268ce0b`**, from a full run with `-timeout 20m`:
- `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree` and `TestSubagentShellOutputDoesNotReachParent`.
- `internal/app`: `TestDefaultPromptBudget` fails alone too: `tool schemas are 15322 bytes, over the 15000 ceiling`. It has failed since `0cad39ae`, which lengthened the `shell.run` and `test.run` schemas. This plan changes no schema.
- `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce` is intermittent. It failed in the full run, passed alone, and failed alone in 7 of 10 runs at `4a81ef11`. It counts as pre-existing whether it passes or fails.
- `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`.
- `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`.
- `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`.
- Known to be flaky under load, and passing in this run: `TestRunManagedCancellationKillsTree` (`internal/snapshot`), `TestRuntimeCloseRunsNoGitGC` (`internal/app`) and `TestACPChildApprovalUsesOwningSessionRegistry` (`internal/acp`).
- `internal/acp`: `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` fails intermittently (about one run in three) on `the outbound approval RPC context was not cancelled by the interruption`. It exists at `2268ce0b` with only a mechanical rename on this branch, and passes 6 of 6 alone, so it is pre-existing: the test reads `ctx.Err()` immediately after a cancellation that settles asynchronously. It counts as pre-existing whether it passes or fails, and should be given a polled assertion before plan 2c leans on it.
- `web/bridge` took 109 s.

Finally, run `./marshal` in this repository and check by eye:
- the status line's branch, `jobs N` and `queued N` segments;
- a subagent's tray row shows its model and, while it runs, its headline;
- `/new` keeps the status line live: start a turn in the new session and watch its activity update.

## Integration notes

- **Plan 2c** starts from this branch's merge. It adds `Session.Sheet` and takes HEAD, the changed files, the base-ref command and `sessionsheet`'s `d.State` reads behind it. `handleChanged`'s workspace follow is where it learns that the base ref must move.
- **Plan 3** moves the drill-in. After it, `m.state.Subagents()` and `m.state.Subagent(id)` keep only the approval readers, which plan 4 moves. Both names then join T1b.
- **Plan 5** moves the work group (spec revision record, `2268ce0b`), the turn-end latch (`TakeWatchResume`) and the steering writes.
- **P3** serves `Status` from a mirror. The conversions in `inproc/runs.go` and `inproc.go` must move to a package `acp` can use when `session/status` gets a handler, as the steering conversion moved to `viewmodel` here.

## Self-review

- Every task commits alone.
  - Tasks 1 to 5 are agent side, and each builds on the one before.
  - Tasks 6 to 8 move reads while the old pumps still run, so each leaves the TUI working.
  - Task 9 replaces the pumps once nothing reads their caches.
  - Task 10 locks the reads in.
- Task 4 touches `acpclient`, `inproc` and one wiring call in `app.go`, because adding a method to `Session` breaks the build until its one implementation has it. Task 5 has the same reason.
- Task 8 spans `tui`, `gatepanel` and `sessionsheet`, because the latter two take the values the TUI reads. Their change is a type swap.
- Cold-executor check:
  - every read site is listed with its function and line;
  - the `st` rule is decision 8, and its exceptions (`handleAgentFinished`, the `/run` closure) are named;
  - every deleted test names what covers its behaviour now;
  - every changed test names its new mechanism (`testModelWith`, `stubSession`, `batchResults`).
- Contracts hold every cross-task declaration: the wire types, the interface methods, the adapter's `Deps`, the TUI's changed signatures and the test helpers.
- Anchors were checked at `2268ce0b`. The baseline was taken there in a detached worktree.
- **Verification record (this branch).** `gofmt -l ./internal ./cmd` printed nothing, `go vet ./...` was clean, `CGO_ENABLED=1 go build ./cmd/marshal` succeeded, `go test -race ./internal/acpclient/...` was clean, and the ACP method-table, T1, T1b and self-check guards passed. The full `go test ./... -timeout 20m` run under `TMPDIR=/private/tmp/p2-2b-suite` failed exactly on the baseline above — `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestSubagentShellOutputDoesNotReachParent`, `TestDefaultPromptBudget`, `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestStandaloneBackendNavigateAndRead` and `TestCLIGitOpsCommitAllWithoutARepoIdentity`. No new failures.
- `internal/app`'s `TestStartupSnapshotMaintenanceIsBoundedAndSkipsStorelessWorkspaces` is TMPDIR-sensitive: it fails under the default TMPDIR and passes under the plan's, which is why every Verify block sets that variable. It is not part of this branch's diff.
- **Review pass (post-implementation).** A branch review fixed five items without changing a contract: receipts are pruned to the current turn so `Status` scans a bounded map; `SendNow` re-checks `closed` after submission to close the swap window; `runCmdOnce` returns a distinct `cmdTimeout` so `TestPumpChangedEndsOnClose` can no longer pass on a block; `TestSteeringReceiptOmitsEmptyFields` pins the `omitempty` absence half; and the two doc lines that miscounted the allowlist (13 entries span 9 files, not 7) and called `WithSendNow` current were corrected.
- Nothing here contradicts the spec. Decisions 1 to 4 are written into its revision record. C5, C8 and the Migration order were updated to match.
