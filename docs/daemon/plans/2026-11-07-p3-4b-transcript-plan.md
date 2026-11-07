# P3 plan 4b: The transcript read and the sheet — implementation plan

Date: 2026-11-07. Status: ready for review.
Base: `main` at `9301125d` (P3 plans 3b and 4c merged). Every anchor below was checked there. The full suite passed there: 119 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-next-suite`, `-timeout 20m`; `web/bridge` 88s).
- **It runs alongside P3 plan 5** ([agent requests](2026-11-07-p3-5-requests-plan.md)). Both plans add a manager to `internal/acp`, a fence to the response barrier, a case to the client's router, methods to the conformance harness's `seam`, and rows to `acpclient.Methods`. "Integration with plan 5" (at the end) lists the conflicts this causes and how to resolve each one.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, a "Decided with the user (2026-11-07)" entry (plan 5's), "Methods P3 adds" (4b and 5), G1, R3 (the `Sheet` row and "How the sheet arrives"), R4 (cadence, `busy`, subagents, reads without a runner), R5, R8, R9, R10, and Migration order items 4b and 5. `docs/daemon/design.md` §4.4, §6.3 and the P4 row changed with them. Task 7 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes. The TUI still runs on `inproc`, in process, until plan 8. Its one change is that leaving a drill-in now tells the session (`ReleaseSubagent`), which in process only drops a memo.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R2 (read your writes), R3 (the mirror: the `Stack` and `Sheet` rows), R4 (the transcript), R9 (compatibility), R10 (budgets), Migration order item 4b, and tests V1, V3, V4, V6 and V8.
Previous plans: [P3 plan 4a, the stack over the wire](2026-11-05-p3-4a-stack-wire-plan.md), [P3 plan 3b, status](2026-11-07-p3-3b-status-plan.md), [P3 plan 4c, the projection's cost](2026-11-07-p3-4c-projection-memo-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** a marshal client reads a session's transcript, its row details, its callers and its sheet over ACP, from mirrors the agent keeps current, with the TUI's busy rule.
- `rpc.Session` gains `Stack`, `StackNode`, `Callers`, `Sheet`, `RefreshSheet` and `ReleaseSubagent`. The stack mirror is plan 4a's `rpc.Stacks`, now owned by the host.
- The agent projects a connection's stacks with the busy flag the client sets (`session/stack_busy`), so the TUI's rule survives the wire.
- `session/sheet` serves the sheet, and a per-session forwarder keeps every subscribed sheet current. `session/callers` answers through the service.
- The stack reads stop requiring an agent runner, and project at the state's clock.
- The interface gains `ReleaseSubagent`, and the TUI calls it when it pops a drill-in.
- The conformance harness gains the transcript, row-detail, callers and sheet cases.

**Non-goals:**
- **No requests.** `Pending` and the answer calls are plan 5's.
- **No change to the projection, `Build`, the memo or any wire byte of an existing method.** `session/stack`, `stack_patch` and `session/stack_node` keep their shapes. The only behaviour change an existing client could see is that a session without a runner now answers `session/stack` and `session/stack_node`, where it answered "unknown session".
- **No nested drill-in.** The agent's stack reads still find only the session's own subagents (`State.Subagent`), where the service searches the whole tree. Children never dispatch subagents, so the TUI never drills into a grandchild (R4 records this).
- **No change to R4's cadence.** 50ms stays (decision 5).
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-4b-suite`. Create the directory first: `mkdir -p /private/tmp/p3-4b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 5 uses `/private/tmp/p3-5-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 7 likelier, the budget tests most of all. Rerun a failure alone before treating it as new.
- **The agent's stack** (`internal/acp/stack.go`). `TurnManager.Stack` (`:275`) resolves the session with `m.lookup` (`:285`), picks the source with `stackSource` (`:133`, which uses `State.Subagent` and refuses an unknown or child-less subagent with invalid params), computes the agent's busy rule (`:294`–`:299`: `stackTurnBusy`, or a child's own running status), and answers through `stackSnapshot` (`:332`), which projects at `time.Now()` (`:339`). `flushProjectorFor` (`:200`) projects at `time.Now()` (`:203`). `flushChildStacks` (`:220`) flushes every active subagent projector with its running status, and marks one `final` once its subagent stops; a `final` projector is skipped from then on. `dropStacks` (`:366`) forgets a session's projectors. The idle loop (`ensureStackIdle`, `:386`) resolves the session with `m.lookup` on a fence (`:429`) and on a tick (`:458`).
- **The turn manager's lookup needs a runner.** `newTurnManagerFor` (`internal/acp/host.go:174`) builds the `Lookup` every stack read uses, and it refuses a runtime whose `Runner` is nil (`:185`–`:198`). So today `session/stack` and `session/stack_node` answer "unknown session" for a session whose provider failed to build, which in process shows its transcript. `treeFor` (`stacknode.go:20`) uses `m.lookup` too (`:21`). `TurnManagerConfig` is at `turn.go:147`, the `lookup` field at `:203`, and `NewTurnManager` at `:270`.
- **The host's wiring** (`registerHandlers`, `host.go:315`). `session/stack`, `session/stack_unsubscribe` and `session/stack_node` are registered at `:343`–`:345`. The status manager is built at `:391` and the barrier installed at `:397`. The connection's cleanup returns at `:553`. `responseBarrier` (`:288`) takes the four managers positionally and skips a nil one. It is also called by `ordering_test.go:153`, `status_test.go:125` and `conformance_test.go:95`.
- **The service** (`internal/acp/service`). `Stack` (`stack.go:16`) projects at the source state's clock through a main memo and a last-subagent memo (`stackMu`, `subMemo`, `subMemoID`: `service.go:277`–`:286`), and reads an unknown subagent as an empty snapshot. `Sheet` (`sheet.go:34`) computes the in-memory values on every call (`activityOf`) and copies the cached database and git values; `RefreshSheet` (`sheet.go:70`) re-reads those and fires `Changed`. `Callers` is in `stacknode.go`. `childState` (`sheet.go:195`) finds a child anywhere in the tree.
- **The client** (`internal/acpclient/rpc`). `Stacks` (`stacks.go`) is plan 4a's mirror: `Stack` (`:129`) fetches on a first read and serves the mirror after, `Release` (`:183`) forgets an entry and sends `session/stack_unsubscribe`, `signal` (`:222`) taps a per-session channel, `refetch` (`:234`) recovers a gap on its own goroutine. Nothing outside tests builds one. `Host` (`dial.go:23`) holds the connection and the attached sessions; `Dial` (`:35`) installs the host's own `Handlers`. `routeNotify` (`session.go:188`) applies `session_status` to the attached session it names and ignores every other kind. `Session` (`session.go:14`) holds the status mirror and `Changed`.
- **The interface** (`internal/acpclient/acpclient.go`). `Sheet` (`:221`–`:230`), `RefreshSheet` (`:232`–`:236`), `Stack` (`:238`–`:252`), `StackNode` (`:254`–`:263`) and `Callers` (`:265`–`:271`). The method table rows are `methods.go:46`–`:50`; `Sheet`, `RefreshSheet` and `Callers` are marked new. Only `service.Session` implements `acpclient.Session` (`service.go:291`).
- **The TUI.** `refreshViewport` reads `m.sess.Stack(m.drilledSheetID(), m.busy)` (`viewport_render.go:99`), and `sheetDataFor` reads `m.sess.Sheet(m.drilledSheetID())` (`model.go:1380`). The view stack changes in `drillIntoSubagent` (`model.go:3710`), `popDrill` (`model.go:3743`) and the session swap (`commands_dispatch.go:219`, where the old session closes anyway). `stackRecorder` (`stack_render_test.go:15`) is the model for a test session that records calls. `refreshSheetCmd` (`pump.go:28`) returns no message: it relies on `RefreshSheet` firing `Changed`.
- **The goldens.** `buildLive` (`transcript_golden_test.go:671`) and `buildTray` (`:736`) set `m.busy = true` with no run in flight. An agent that read its own run slot would build both trees as idle, so the `live` and `tray` goldens would change. That is why the busy flag stays on the wire (decision 1).
- **Test models.** `stackAgent` (`stack_wire_test.go:36`; `startStackAgent` at `:49`, `install` at `:89`) serves the stack methods to a raw `rpc.Conn` feeding an `rpc.Stacks`; `settle` (`:148`) waits for the frames before a ping. `startStatusAgent` (`status_test.go:94`) is the model for a manager served to a raw connection with an update recorder. The harness is `conformance_test.go` (`seam` at `:25`, `openPipeSeamTB` at `:66`). `largeStackSession` (`stack_wire_test.go:412`) is R10's 2,000-call session. `withoutMemoCheck` (`memocheck_test.go`) turns plan 4c's memo check off for a budget test. The scripted agent in `rpc/session_test.go` (`startScripted` `:25`, `dialScripted` `:115`, `attachScripted` `:173`) is the model for the client's tests.
- **Measured at the base, on the 2,000-call session:** one `Sheet(0)` read and its encoding take 90µs and allocate 0.87MB (the audit log's copy); one `Status()` read 5µs; one agent flush 4.3ms (plan 4c).

**Decisions this plan makes beyond the spec** (R3, R4 and R10 record them):

1. **The busy flag stays, as a connection's flag for a session.** `session/stack_busy {sessionId, busy}` sets the flag every stack of that session is projected with for this connection: the main transcript and every subagent's, as the TUI's rule passes one flag for whatever tree it reads. A connection that never sends it keeps the agent's rule. The handler re-projects the session's active stacks with the new flag and sends their patches before it answers, so the barrier is not needed for it.
2. **`Stack` blocks only on a first read and at a flip.** `rpc.Session.Stack(subagentID, busy)` sends `session/stack_busy` when `busy` differs from the flag it last sent (or none was sent), then reads through the stack mirror, whose first read of a tree fetches it. A flip happens twice a turn and costs one agent flush (4.3ms on the 2,000-call session), measured against 10ms (R10). Making it asynchronous would show one frame built with the old flag, and every TUI test that sets `m.busy` and then renders would see it once plan 8 puts the TUI on the pipe.
3. **The session owns its mirrors; the host owns the stack mirror.** `Dial` builds one `rpc.Stacks`, bound to the connection, and the router feeds every `session/update` to it. A change it applies fires the attached session's `Changed`. The sheet mirror lives in the session, one entry per subagent ID read.
4. **`ReleaseSubagent` is explicit.** The interface gains it; the TUI calls it from `popDrill` with the ID it popped. Over the wire it forgets the subagent's sheet entry and sends `session/stack_unsubscribe` through `Stacks.Release`, and the agent's unsubscribe releases that subagent's sheet subscription with its stack. In process it drops the service's subagent memo. Releasing `0` does nothing.
5. **R4's cadence stays at 50ms.** After plan 4c a flush of the 2,000-call session costs 4.3ms: about 9% of one core while a long turn streams, on the agent's goroutine. The idle loop projects only when an event marked the stack dirty. In process today the TUI spends about 4ms of its `Update` goroutine on every refresh; over the pipe it applies a patch in about 1ms.
6. **The sheet forwarder polls,** as the status forwarder does: every 50ms and when the barrier asks, for each sheet the connection has read. Most of a sheet is computed from the state on every read, so no event says it changed. It sends a `sheet` update for each one whose encoding changed. The main sheet is never released, like the main stack; its poll costs 90µs per tick on the 2,000-call session.
7. **The stack reads need no runner.** `TurnManagerConfig` gains `Reads`: a lookup that returns the session's state and events and does not check the runner. `Stack`, `treeFor` and the idle loop use it; prompts and every other call keep `Lookup`. A nil `Reads` means `Lookup`, so existing tests are unchanged.
8. **The agent projects at the state's clock** (`State.Now()`), in `flushProjectorFor` and `stackSnapshot`, as the service's `Stack` does. In production that is the wall clock; under a test clock the two arms agree.
9. **A refused stack read is an empty snapshot.** `session/stack` refuses a subagent ID that names no child (invalid params); the client returns the empty snapshot with that ID, as the service does, and keeps no entry, so the next read asks again.
10. **`Callers` on the wire omits an empty list,** and the client returns nil for it. The TUI treats nil and empty alike.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, `docs/daemon/design.md`, this plan or plan 5's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 4b and 5`. Plan 5 gives the same instruction, so whichever plan starts first commits all four. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-4b-transcript` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-4b-transcript: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### Wire types (`internal/acpwire/stack.go`, `stacknode.go`, `sheet.go`)

In `stack.go`, the method block gains two names, and one params type follows `StackParams`:

```go
const (
	MethodStack            = "session/stack"
	MethodStackUnsubscribe = "session/stack_unsubscribe"
	MethodStackBusy        = "session/stack_busy"
	MethodStackNode        = "session/stack_node"
	MethodCallers          = "session/callers"
	MethodSessionUpdate    = "session/update"
	UpdateStackPatch       = "stack_patch"
)

// StackBusyParams is session/stack_busy's params: the busy flag every stack
// of the session is projected with for this connection from now on (P3 R4).
type StackBusyParams struct {
	SessionID string `json:"sessionId"`
	Busy      bool   `json:"busy"`
}
```

In `stacknode.go`, beside `StackNodeResult`:

```go
// StackNodeParams is session/stack_node's params. SubagentID 0 names the
// main transcript.
type StackNodeParams struct {
	SessionID  string `json:"sessionId"`
	NodeID     string `json:"nodeId"`
	SubagentID int64  `json:"subagentId,omitempty"`
}

// CallersParams is session/callers' params.
type CallersParams struct {
	SessionID string `json:"sessionId"`
	NodeID    string `json:"nodeId"`
}

// CallersResult is session/callers' answer. An empty list is omitted.
type CallersResult struct {
	Refs []string `json:"refs,omitempty"`
}
```

In `sheet.go`, after `SheetRefresh`:

```go
// The sheet's method and update kind (P3 R3).
const (
	MethodSheet = "session/sheet"
	UpdateSheet = "sheet"
)

// SheetParams is session/sheet's params. With Refresh it re-reads the values
// Refresh names first, as RefreshSheet does. Either way it answers the sheet
// of SubagentID, and subscribes a marshal client to it.
type SheetParams struct {
	SessionID  string        `json:"sessionId"`
	SubagentID int64         `json:"subagentId,omitempty"`
	Refresh    *SheetRefresh `json:"refresh,omitempty"`
}

// SheetUpdate is a sheet update: the whole sheet of one subscription,
// sent whenever it differs from the last one sent. SubagentID is the ID the
// client read, which can differ from Sheet.SubagentID: an ID that names no
// child reads as the session's own sheet.
type SheetUpdate struct {
	Kind       string       `json:"kind"`
	SubagentID int64        `json:"subagentId,omitempty"`
	Sheet      SessionSheet `json:"sheet"`
}
```

`internal/acp/stacknode.go`'s `StackNodeParams` becomes `type StackNodeParams = acpwire.StackNodeParams`, as `StackParams` already is. Its JSON is unchanged.

### The agent's stack (`internal/acp/turn.go`, `stack.go`)

```go
// In TurnManagerConfig:

	// Reads resolves a session for the transcript reads (session/stack,
	// session/stack_node and the idle loop): its state and events, whether or
	// not its runner is built. Nil means Lookup.
	Reads Lookup

// setClientBusy records the busy flag this connection's stacks of the
// session are projected with, and reports whether it changed (P3 R4).
func (m *TurnManager) setClientBusy(sessionID string, busy bool) bool

// projectBusy is the busy flag a stack of the session is projected with: the
// one the client set, or rule when it set none.
func (m *TurnManager) projectBusy(sessionID string, rule bool) bool

// StackBusy handles session/stack_busy: it sets the session's busy flag for
// this connection and, when it changed, re-projects every active stack of
// the session with it and sends their patches before answering.
func (m *TurnManager) StackBusy(ctx context.Context, params json.RawMessage) (any, error)
```

### The sheet and callers (`internal/acp/sheet.go`, new)

```go
// sheetInterval is how often a sheet forwarder compares each subscribed
// sheet with the last one it sent (P3 R3).
const sheetInterval = 50 * time.Millisecond

// SheetManagerConfig wires one connection's sheet handlers.
type SheetManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
	// Notify sends sheet updates.
	Notify NotifyFunc
	// Marshal reports whether the connection's client declared the marshal
	// capability. Nil means it did not.
	Marshal func() bool
}

// SheetManager serves session/sheet for one connection, and runs one sheet
// forwarder per session a marshal client reads a sheet of.
type SheetManager struct{ /* unexported */ }

func NewSheetManager(cfg SheetManagerConfig) *SheetManager

func (m *SheetManager) Sheet(ctx context.Context, params json.RawMessage) (any, error)

// Release drops the subscription to one subagent's sheet. The session's own
// sheet (0) is never released.
func (m *SheetManager) Release(sessionID string, subagentID int64)

// Close stops every forwarder. The connection's cleanup calls it.
func (m *SheetManager) Close()

// fenceSession waits until the session's sheet forwarder has sent every
// subscribed sheet as it is now, where it differs from the last one sent.
func (m *SheetManager) fenceSession(ctx context.Context, sessionID string)

// callersHandler serves session/callers through the session's service.
func callersHandler(lookup func(sessionID string) (*service.Session, bool)) Handler

// registerTranscript registers session/stack, session/stack_node,
// session/stack_busy, session/stack_unsubscribe (which also releases the
// subagent's sheet), session/sheet and session/callers on srv.
func registerTranscript(srv *Server, turns *TurnManager, sheets *SheetManager, lookup func(sessionID string) (*service.Session, bool))
```

### The response barrier (`internal/acp/host.go`)

Plan 5 makes the same change, word for word. If it merged first, the change is already there: keep it and only append this plan's manager.

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

### The client (`internal/acpclient/rpc/stacks.go`, `transcript.go` new)

```go
// OnChange sets a function the mirror calls, with the session's ID, after
// every change it applies to that session's stacks, beside its own Changed.
func (s *Stacks) OnChange(f func(sessionID string))

// stackCallTimeout bounds the calls Stack makes on a read: the busy flip and
// a first fetch.
const stackCallTimeout = 5 * time.Second

func (s *Session) Stack(subagentID int64, busy bool) acpwire.StackSnapshot
func (s *Session) StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error)
func (s *Session) Callers(ctx context.Context, nodeID string) ([]string, error)
func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet
func (s *Session) RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error
func (s *Session) ReleaseSubagent(ctx context.Context, subagentID int64) error
```

### The interface (`internal/acpclient/acpclient.go`) and the service

```go
// In acpclient.Session, after Callers:

	// ReleaseSubagent says the caller stopped reading the subagent's
	// transcript and sheet, as the TUI does when it leaves a drill-in. The
	// next read of either starts afresh. Releasing 0, the session's own,
	// does nothing.
	//
	// ACP: session/stack_unsubscribe (exists), which also releases the
	// subagent's sheet.
	ReleaseSubagent(ctx context.Context, subagentID int64) error
```

```go
// In internal/acp/service/stack.go:

// ReleaseSubagent drops the subagent's projection memo, if it is the one
// the session keeps.
func (s *Session) ReleaseSubagent(ctx context.Context, subagentID int64) error
```

### The harness (`internal/acp/conformance_test.go`, `conformance_transcript_test.go` new)

```go
// seam gains:
	Stack(subagentID int64, busy bool) acpwire.StackSnapshot
	StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error)
	Callers(ctx context.Context, nodeID string) ([]string, error)
	Sheet(subagentID int64) acpwire.SessionSheet
	RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error
	ReleaseSubagent(ctx context.Context, subagentID int64) error

// serveTranscript registers the transcript handlers on the pipe arm's server
// over the served state and service, and returns the managers the barrier
// fences.
func serveTranscript(tb testing.TB, srv *Server, d service.Deps, served *service.Session, sessionID string) (*TurnManager, *SheetManager)

func TestTranscriptConformance(t *testing.T)
func TestStackBusyFlipBudget(t *testing.T)
```

## Task 1: The wire types

**Goal:** `acpwire` carries the busy flip, the node detail params, the callers call and the sheet's method and update.

**Files:** `internal/acpwire/stack.go`, `internal/acpwire/stacknode.go`, `internal/acpwire/sheet.go`, `internal/acpwire/stack_test.go`, `internal/acpwire/sheet_test.go`, `internal/acp/stacknode.go`.

**Steps:**

1. Add the constants and types in Contracts. Keep `StackParams` as it is.
2. Make `internal/acp/stacknode.go`'s `StackNodeParams` an alias of `acpwire.StackNodeParams`.
3. Tests:
   - `TestStackBusyParamsEncodeBusyFalse` (`stack_test.go`): `StackBusyParams{SessionID: "s1", Busy: false}` encodes `"busy":false` (the field has no `omitempty`: false is a value the agent must receive).
   - `TestSheetUpdateRoundTrips` (`sheet_test.go`): a `SheetUpdate` whose `SubagentID` is 7 and whose sheet has `SubagentID` 0, tools, files and a context section, encodes and decodes to an equal value, and its `kind` is `sheet`.
   - `TestSheetParamsOmitAnAbsentRefresh` (`sheet_test.go`): `SheetParams{SessionID: "s1"}` encodes without a `refresh` key; with `Refresh: &SheetRefresh{Stats: true}` it carries `"refresh":{"stats":true}`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-4b-suite
go build ./...
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acpwire/ ./internal/acp/ -count=1
```

Expected: both packages pass; `internal/acp`'s stack-node tests pass unchanged on the alias.

## Task 2: The agent's stack reads

**Goal:** the agent serves a session's stack without its runner, at the state's clock, with the busy flag the client sets.

**Files:** `internal/acp/turn.go`, `internal/acp/stack.go`, `internal/acp/stacknode.go`, `internal/acp/host.go`, `internal/acp/stack_busy_test.go` (new), `internal/acp/stack_wire_test.go`, `internal/acp/host_test.go`.

**Steps:**

1. **`Reads`.** Add the field in Contracts to `TurnManagerConfig`, and a `reads` field to `TurnManager`, set in `NewTurnManager` from `cfg.Reads`, or from `cfg.Lookup` when it is nil. Use `m.reads` in `Stack` (`stack.go:285`), in `treeFor` (`stacknode.go:21`), and in both idle-loop lookups (`stack.go:429`, `:458`). Every other `m.lookup` stays.
2. **The host's `Reads`.** In `newTurnManagerFor`, set `Reads` to a closure that takes the runtime from `manager.Get`, refuses a nil runtime or a nil `State`, and returns a `TurnRuntime` carrying only `SessionID`, `State` and `Events` (the runtime's event broker, asserted as `Lookup` asserts it). It does not check the runner and does not start the child-approval collector.
3. **The state's clock.** In `flushProjectorFor`, take `now` from `st.Now()` instead of `time.Now()`. In `stackSnapshot`, project at `src.Now()`.
4. **The busy flag.**
   - Add to `TurnManager` a map from session ID to the client's flag, under a mutex of its own, made in `NewTurnManager`. `setClientBusy` and `projectBusy` read and write it as in Contracts. `dropStacks` deletes the session's entry.
   - At the top of `flushProjectorFor`, replace `busy` with `m.projectBusy(sessionID, busy)`. In `Stack`, apply `projectBusy` to the rule it computes (`:294`–`:299`) before calling `stackSnapshot`. Those two places cover the turn's flush, the idle loop, the subagents and a first read.
   - `StackBusy`: decode `StackBusyParams`; an empty `sessionId` is `invalidParamsError`; resolve the session with `m.reads`, answering an unknown one with `serverErrorf("unknown session: %s", id)`. When `setClientBusy` reports a change: clear `final` on every projector of the session whose key has the `"<session>#"` prefix, flush the session's main projector if it exists (look it up in `m.stacks`; never create one) with `flushProjectorFor(p, id, 0, rt.State, false, false, false)`, then call `flushChildStacks(id, rt.State)`. The patches are sent from the handler, so they precede its response. Answer `struct{}{}`.
5. **Registration.** In `registerHandlers`, register `session/stack_busy` to `turns.StackBusy` beside the three stack methods (`host.go:343`–`:345`). Task 3 moves all four into `registerTranscript`. Add `session/stack_busy` to `stackAgent.install` (`stack_wire_test.go:89`) too.
6. **Tests** in `stack_busy_test.go`, over `startStackAgent` (a marshal client feeding an `rpc.Stacks`), on a frozen-clock state with one finished turn (a user message, a step, a final assistant message), so the agent's rule builds it idle and it carries a receipt:
   - `TestStackBusySetsTheFlagTheStacksAreProjectedWith`: the first read has a receipt; `session/stack_busy` with `true` returns, and the mirror read straight after it, with no `settle`, has no receipt; with `false` the receipt is back.
   - `TestStackBusyReachesAFinishedSubagent`: register a child with a finished turn and finish the subagent; read its stack, then call `flushChildStacks` directly so its projector is `final`; `session/stack_busy` with `true` removes the child's receipt from the mirror.
   - `TestTheSameBusyFlagSendsNoPatch`: two calls with the same flag; the second sends no `stack_patch` (count with `stackAgent.counts`).
   - `TestStackBusyRefusesBadParams`: a missing `sessionId` is invalid params; an unknown session is the unknown-session error.
7. **The runner test** in `host_test.go`: `TestStackServesASessionWithoutARunner`. Install `&app.Runtime{State: st}` (no runner) in the manager's map, as `host_test.go:275` does, register the handlers, and call the `session/stack` handler for it: it returns a snapshot holding the state's message, not "unknown session". The `session/stack_node` handler answers one of its nodes.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -run 'StackBusy|TheSameBusyFlag|WithoutARunner|Stack|Barrier' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -count=1
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -run 'StackBusy|StackMirror|FlushedPatches' -race -count=3
```

Expected: the new tests pass, three times under `-race`; plan 4a's and 4c's stack tests pass unchanged.

Mutation checks:
- Remove `projectBusy` from `flushProjectorFor`. `TestStackBusyReachesAFinishedSubagent` fails. Restore it.
- Make `StackBusy` skip clearing `final`. `TestStackBusyReachesAFinishedSubagent` fails. Restore it.
- Leave `Stack` on `m.lookup`. `TestStackServesASessionWithoutARunner` fails. Restore it.

## Task 3: The agent's sheet and callers

**Goal:** `acp.Server` serves `session/sheet` and `session/callers`, keeps a marshal client's subscribed sheets current, and releases a subagent's sheet with its stack.

**Files:** `internal/acp/sheet.go` (new), `internal/acp/sheet_test.go` (new), `internal/acp/host.go`, `internal/acp/status_test.go`, `internal/acp/ordering_test.go`, `internal/acp/conformance_test.go`, `internal/acpclient/methods.go`, `internal/acpclient/acpclient.go`.

**Steps:**

1. **The handler.** `SheetManager.Sheet` decodes `SheetParams`, refuses an empty `sessionId` with `invalidParamsError`, and resolves the service with `Lookup` (unknown: `serverErrorf("unknown session: %s", id)`). With `Refresh`, it calls the service's `RefreshSheet` first and returns its error as it is. When `Marshal` reports true, it subscribes the connection to `(sessionId, subagentId)` (step 2). It returns the service's `Sheet(subagentId)`.
2. **The forwarder.** One per session per connection, started by the first subscription, as `StatusManager.ensureForwarder` (`status.go:195`) starts one: its fence is registered before its goroutine starts. It holds the set of subscribed subagent IDs, each with the encoding of the last sheet it sent (none at first), under its own mutex; a subscription to an ID it already has changes nothing.
   - Its loop wakes on a `sheetInterval` ticker and on its fence, as `runForwarder` (`status.go:219`) does. Each pass resolves the service through `Lookup`; if the session is gone it answers a pending fence, then stops.
   - Otherwise it copies the subscribed IDs (in ascending order) under the mutex, and for each reads `Sheet(id)`, encodes it, and, when the encoding differs from the last one sent for that ID, sends `session/update` with `SessionUpdateParams{SessionID, Update: acpwire.SheetUpdate{Kind: acpwire.UpdateSheet, SubagentID: id, Sheet: …}}`. It records the new encoding only if the ID is still subscribed. It never sends under a mutex.
   - `Release` removes the ID from the session's set (ignoring `0`). `Close` cancels every forwarder and makes later subscriptions do nothing.
3. **Callers.** `callersHandler` decodes `CallersParams`, refuses an empty `sessionId` or `nodeId` with `invalidParamsError`, resolves the service (unknown session as above), calls `Callers`, and returns `CallersResult{Refs: …}`.
4. **`registerTranscript`.** It registers `session/stack` (`turns.Stack`), `session/stack_node` (`turns.StackNode`), `session/stack_busy` (`turns.StackBusy`), `session/sheet` (`sheets.Sheet`), `session/callers` (`callersHandler(lookup)`), and `session/stack_unsubscribe` as a handler that runs `turns.StackUnsubscribe` and, when it succeeds, decodes the same `StackParams` and calls `sheets.Release(sessionId, subagentId)`.
5. **The barrier.** Change `responseBarrier` to the variadic form in Contracts: it calls each non-nil fencer's `fenceSession` in order. The existing calls (`host.go:397`, `ordering_test.go:153`, `status_test.go:125`, `conformance_test.go:95`) compile unchanged: their untyped nils become nil interfaces, which it skips. Never pass a typed nil manager.
6. **Host wiring.** In `registerHandlers`, delete the four stack registrations (`:343`–`:345` and Task 2's `stack_busy`). After the status manager, build the `SheetManager` (`Lookup` is `manager.Service`, `Notify` is `h.sink.Notify`, `Marshal` is `srv.marshalClient`), call `registerTranscript(srv, turns, sheets, manager.Service)`, and pass `sheets` to `responseBarrier` after `statuses`. Call `sheets.Close()` in the cleanup, after `statuses.Close()`.
7. **The method table.** Mark `Sheet`, `RefreshSheet` and `Callers` existing in `methods.go`, and change their doc lines in `acpclient.go` from `(new in P3)` to `(exists)`, keeping the rest of each line. Rewrite the `Sheet` doc's last sentence to "P3 serves it from a mirror that a sheet update keeps current." `TestACPClientMethodTableMatchesTheServer` and `TestMethodDocsNameTheirACPMethod` hold the table and the docs together.
8. **Tests** in `sheet_test.go`, serving a `SheetManager` on a pipe with `startTestAgent`, `registerInitialize`, `registerTranscript` (over a `TurnManager` built as `stackAgent.install` builds one) and `responseBarrier(sheets)`, to a raw `rpc.Conn` whose `Notify` records each `sheet` update, as `startStatusAgent` (`status_test.go:94`) records `session_status`. The state is `session.New(config.Default(), dir, …, session.Persistence{SessionID: "s1"})`, and the service `service.New(service.Deps{State: …})`.
   - `TestTheFirstSheetArrivesBeforeTheResponse`: the first `session/sheet` call returns after exactly one `sheet` update for subagent 0 was recorded, and its sheet equals the result.
   - `TestASheetChangeIsSentWithinTheInterval`: log a tool call on the state directly; a `sheet` update whose tools include it arrives within 500ms. Then three quiet intervals send nothing.
   - `TestRefreshSheetReturnsAfterItsSheet` (V3): in a git repository built as the service's `sheetRepo` builds one (skip without git), with every `session/update` frame slowed by 50ms as `ordering_test.go` slows them: modify `a.txt`, then call `session/sheet` with `refresh: {rebase: true}`; when it returns, the recorded updates include one whose sheet lists `a.txt` as changed.
   - `TestReleasingASubagentStopsItsSheet`: subscribe to a running child's sheet; `session/stack_unsubscribe` for it returns; log a call on the child; within 300ms no `sheet` update for that subagent arrives, while one for 0 still would (log a call on the parent and see it).
   - `TestAClientWithoutTheCapabilityGetsNoSheetUpdates` (V6): `session/sheet` still answers, and no `sheet` update is recorded within 200ms.
   - `TestSheetRefusesBadParams` and `TestCallersRefusesBadParams`: a missing `sessionId` (and for callers a missing `nodeId`) is invalid params; an unknown session is the unknown-session error.
   - `TestCallersAnswersThroughTheService`: with a `ReferenceFinder` that answers `a.go:10` for the edit the service's `editEvent` describes, `session/callers` returns `{"refs":["a.go:10"]}`; with no finder it returns `{}`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -run 'Sheet|Callers|ACPClientMethodTable|Barrier|AResponseFollows|Status|Notice' -count=1 -v 2>&1 | tail -40
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/... ./internal/acpwire/ ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -run 'Sheet|Callers' -race -count=3
```

Expected: the new tests pass, three times under `-race`; plan 3a's and 3b's ordering and status tests pass on the variadic barrier; the method-table tests pass with the three rows existing.

Mutation checks:
- Drop `sheets` from `responseBarrier` in the test's server. `TestRefreshSheetReturnsAfterItsSheet` and `TestTheFirstSheetArrivesBeforeTheResponse` fail. Restore it.
- Make the unsubscribe handler skip `sheets.Release`. `TestReleasingASubagentStopsItsSheet` fails. Restore it.

## Task 4: The client's transcript and sheet

**Goal:** `rpc.Session` reads the stack, the row details, the callers and the sheet over the connection, with the TUI's busy rule, and releases a subagent.

**Files:** `internal/acpclient/rpc/stacks.go`, `internal/acpclient/rpc/dial.go`, `internal/acpclient/rpc/session.go`, `internal/acpclient/rpc/transcript.go` (new), `internal/acpclient/rpc/transcript_test.go` (new).

**Steps:**

1. **`OnChange`.** `Stacks.signal` calls the function `OnChange` set, with the session ID, after tapping its own channel and outside its mutex. A nil function is skipped. Nothing else in `Stacks` changes.
2. **The host's mirror.** `Host` gains a `*Stacks`. `Dial` builds it with `NewStacks`, binds it to the connection once the connection exists, and sets `OnChange` to a function that taps the attached session's `Changed` (as `apply` does, under the session's mutex, never after the close). `routeNotify` hands every `session/update` to the stacks' `Notify` first, then decodes the kind as now, and gains a case for `acpwire.UpdateSheet`: decode `SheetUpdate` and hand it to the attached session's sheet mirror. `Attach` gives the session the host's `*Stacks`.
3. **`Stack`.**
   - Under the session's mutex, see whether `busy` differs from the flag last sent, or none was sent. If it does, send `session/stack_busy` with `StackBusyParams{SessionID, Busy}` under a `stackCallTimeout` context; on success record the flag. On failure keep the old record, so the next read tries again.
   - Then read with `Stacks.Stack` under a `stackCallTimeout` context. On any error return `acpwire.StackSnapshot{SessionID: s.id, SubagentID: subagentID}`: the agent refuses a subagent ID that names no child, and the service reads that as empty (decision 9).
4. **`StackNode` and `Callers`.** Each sends its method with `StackNodeParams` or `CallersParams`. `Callers` returns the result's `Refs`, which is nil when the agent omitted it.
5. **The sheet mirror.** The session keeps one entry per subagent ID read, each holding the last sheet and whether an update replaced it since the entry's call went out.
   - `Sheet(id)`: an entry returns its sheet without I/O. Otherwise create the entry, then send `session/sheet` with `SheetParams{SessionID, SubagentID: id}` under a `stackCallTimeout` context. On error remove the entry and return `acpwire.SessionSheet{}`. On success install the result unless an update arrived during the call, as `Attach` does for the status.
   - An update for an ID with an entry replaces its sheet, marks it updated, and taps `Changed`. One for an ID without an entry is dropped.
   - `RefreshSheet(ctx, req)`: make sure entry 0 exists (create it if not), clear its updated mark, send `session/sheet` with `SheetParams{SessionID, Refresh: &req}`, install the result unless an update arrived during the call, then tap `Changed` and return nil. On error return it; a created entry is removed. `Changed` fires even when nothing differed, because the TUI's `refreshSheetCmd` returns no message and relies on it.
6. **`ReleaseSubagent(ctx, id)`.** `0` returns nil. Otherwise remove the sheet entry for `id`, then return `Stacks.Release(ctx, s.id, id)`.
7. **Tests** in `transcript_test.go`, over the scripted agent of `session_test.go` (`dialScripted`, `attachScripted`, `next`, `answer`, `send`, and its frame helpers):
   - `TestStackSendsTheBusyFlagBeforeItsFirstRead`: `Stack(0, true)` sends `session/stack_busy` with `busy: true`, then `session/stack`; the returned snapshot is the agent's.
   - `TestStackSendsTheBusyFlagOnlyWhenItChanges`: two reads with `true` send one `session/stack_busy`; a read with `false` sends another, and the mirror is read without a fetch.
   - `TestARefusedStackReadIsEmpty`: the agent answers `session/stack` for subagent 9 with an invalid-params error; `Stack(9, false)` returns an empty snapshot whose `SubagentID` is 9, and the next read sends `session/stack` again.
   - `TestAStackPatchFiresChanged`: after a first read, a chaining `stack_patch` for the session taps `Changed`, and the next read shows it.
   - `TestSheetInstallsTheUpdateThatPrecedesTheResponse`, `TestSheetInstallsTheResultWhenNoUpdateCame`, `TestASheetUpdateReplacesTheSheetAndFiresChanged`, `TestASheetUpdateForAnUnreadSheetIsDropped`: the same four rules plan 3b's tests pin for the status, for the sheet.
   - `TestRefreshSheetSendsItsFlagsAndFiresChanged`: `RefreshSheet` sends `session/sheet` with `refresh: {changed: true}` and subagent 0, and `Changed` fires after it returns.
   - `TestReleaseSubagentUnsubscribesAndForgetsTheSheet`: after reading subagent 3's stack and sheet, `ReleaseSubagent(3)` sends `session/stack_unsubscribe` for 3; the next `Sheet(3)` sends `session/sheet` again. `ReleaseSubagent(0)` sends nothing.
   - `TestStackNodeAndCallersSendTheirMethods`: each sends its method with the right params; an omitted `refs` returns nil.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/acpclient/...
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acpclient/... -count=1
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acpclient/rpc/ -race -count=3
```

Expected: every `acpclient` package passes, its import test included, three times under `-race`; plan 3b's session tests and plan 4a's stacks tests pass unchanged.

Mutation check: make `Stack` skip `session/stack_busy` when an entry already exists. `TestStackSendsTheBusyFlagOnlyWhenItChanges` fails. Restore it.

## Task 5: The interface's release and the TUI

**Goal:** `acpclient.Session` has `ReleaseSubagent`, the service implements it, and the TUI calls it when it leaves a drill-in.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acp/service/stack.go`, `internal/acp/service/stack_test.go`, `internal/app/tui/model.go`, `internal/app/tui/drill_test.go`.

**Steps:**

1. **The interface.** Add `ReleaseSubagent` to `Session` after `Callers`, with the doc in Contracts. Add its row to `methods.go` after `Callers`' row: `{Iface: "Session", Name: "ReleaseSubagent", ACP: "session/stack_unsubscribe", Exists: true}`.
2. **`Stack`'s doc.** Replace its `busy` paragraph's last two sentences ("P3 drops the parameter: …") with: "P3 keeps it (R4): over the wire the client sends the flag with session/stack_busy when it changes, and every stack of the session is projected with it." Replace "Stack does not block." with "Stack does not block in process. Over the wire it makes a call only on a tree's first read and when busy changes."
3. **The service.** `ReleaseSubagent` returns `errSessionClosed` for a closed session and nil for `0`. Otherwise, under `stackMu`, when `subMemoID` is the ID, it resets `subMemo` and sets `subMemoID` to 0, and returns nil. Tests in `stack_test.go`: `TestReleaseSubagentDropsItsMemo` (read a child's stack, release it, and `subMemoID` is 0), `TestReleaseSubagentOfAnotherChildKeepsTheMemo`, and `TestReleaseSubagentOnAClosedSessionFails`.
4. **The TUI.** In `popDrill`, take the popped view before shrinking the stack, and after the existing resets call `m.sess.ReleaseSubagent(m.ctx, popped.ID)`, ignoring the error: a failed release leaves only a projection nobody reads.
5. **The TUI test.** `TestPopDrillReleasesTheSubagent` in `drill_test.go`: a `releaseRecorder` wraps `m.sess` as `stackRecorder` does (embedding `acpclient.Session`, with `Unwrap`), recording each `ReleaseSubagent` ID. Drill into a registered child and pop: the recorder holds the child's ID once. Popping with nothing drilled records nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acpclient/... ./internal/acp/service/ ./internal/app/tui/
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acpclient/... ./internal/acp/service/ ./internal/acp/ -count=1
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/app/tui/ -run 'Drill|Pop|Stack|Golden' -count=1
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/app/tui/ -count=1
```

Expected: everything passes; `TestMethodTableCoversTheInterfaces` sees the new row; T6's goldens are unchanged.

## Task 6: The conformance harness

**Goal:** the transcript, row-detail, callers and sheet cases run against the service and against the client over a pipe, and agree (V1), and the busy flip is measured (R10).

**Files:** `internal/acp/conformance_test.go`, `internal/acp/conformance_transcript_test.go` (new), `internal/acp/service/stack_test.go`, `internal/acp/service/stacknode_test.go`, `internal/acp/service/callers_test.go`, `internal/acp/service/sheet_test.go`.

**Steps:**

1. **The seam.** Add the six methods in Contracts to `seam`.
2. **The pipe arm.** In `openPipeSeamTB`, after the status manager, call `serveTranscript(tb, srv, d, served, sessionID)`, and pass its two managers to `responseBarrier`: turns before the status manager, sheets after it. `serveTranscript` builds a `TurnManager` whose `Lookup` and `Reads` both return `&TurnRuntime{SessionID: sessionID, State: d.State, Events: broker}` for the served ID (`broker` is a new `pubsub.Broker[session.Event]`, as `stackAgent.install` makes one), with `Notify: srv.Notify` and `Marshal: srv.marshalClient`; a `SheetManager` over the served service; and `registerTranscript` over them. It closes the sheet manager with the test.
3. **The table.** `TestTranscriptConformance` runs every case once per arm, as `TestStatusConformance` does. Move these tests from the service's tests into it, deleting them there, and copy the helpers they use (`stackState`, `hasKind`, `stackCallNode`, `recordCall`, `fakeFinder`, `editEvent`, `logEdit`, `requireGit`, `runGit`, `sheetRepo`) into the new file:
   - the stack: "the stack is the shared projection" (`TestStackIsTheSharedProjection`), "the stack builds with the busy flag it is given" (`TestStackBuildsWithTheBusyFlagItIsGiven`), "a subagent's stack reads its transcript" (`TestStackOfASubagentReadsItsTranscript`), "an unknown subagent's stack is empty" (`TestStackOfAnUnknownSubagentIsEmpty`), "the stack reads the state's clock" (`TestStackReadsTheStatesClock`);
   - the row detail: "a call's detail" (`TestStackNodeReturnsACallsDetail`), "a subagent's detail" (`TestStackNodeReadsASubagentsTranscript`), "an unknown node or subagent fails" (`TestStackNodeFailsForAnUnknownNodeOrSubagent`);
   - the callers: "the callers ask the finder" (`TestCallersAsksTheFinderForTheResolvedSymbol`), "no callers without a lookup" (`TestCallersIsNilWithoutALookup`);
   - the sheet: "the session's activity" (`TestSheetCarriesTheSessionsActivity`), "a subagent's activity" (`TestSheetReadsASubagentsOwnActivity`), "a nested subagent" (`TestSheetFindsANestedSubagent`), "an unknown subagent reads as the session" (`TestSheetReadsAnUnknownSubagentAsTheSession`), "a refresh rebases onto HEAD" (`TestRefreshSheetRebasesOntoHead`), "a refresh reads the turns, totals and repo counts" (`TestRefreshSheetReadsTurnsTotalsAndRepoCounts`);
   - a new case, "a released subagent reads the same": read a child's stack and sheet, `ReleaseSubagent`, read both again; the second reads equal the first.

   Each case builds its whole state before `open`, as plan 3b's cases do: the pipe arm's first read is what the agent sends then. Keep each case's assertions, with two exceptions. `TestStackIsTheSharedProjection`'s `Rev` check stays in the service's tests as `TestStackCarriesNoRevision`: the service keeps no revisions, and the mirror does. And compare node lists by their JSON encodings (`json.Marshal` of each list), as plan 4a's mirror tests do: the wire does not keep a nil slice apart from an empty one, and nothing renders the difference.
4. **A difference is a bug, never the case's,** as in plan 3b: fix the wire type, the agent or the service so both arms agree. If the fix would change what the TUI sees today, stop and report it instead.
5. **The budget (R10).** `TestStackBusyFlipBudget`: the pipe arm over `largeStackSession`, with `withoutMemoCheck`. Read `Stack(0, false)` once, then time 20 reads alternating `true` and `false`, each of which flips. The median must be under 10ms (decision 2). Log it with `t.Logf` for the Implementation record, with plan 4c's flush for comparison. Skip under `-short` and with the race detector, as `TestNoticeCallBudget` does.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -run 'TestTranscriptConformance|TestStackBusyFlipBudget|TestStatusConformance' -count=1 -v 2>&1 | tail -60
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/... -count=1
TMPDIR=/private/tmp/p3-4b-suite go test ./internal/acp/ -run 'TestTranscriptConformance' -race -count=3
```

Expected: every case passes on both arms, three times under `-race`; the budget test passes and logs its median; plan 3b's status cases still pass with the transcript managers in the pipe arm; the service package passes without the moved tests.

Mutation check: make the router skip `sheet` updates. The pipe arm of "a refresh rebases onto HEAD" fails and the service arm passes. Restore it.

## Task 7: The docs and full verification

**Goal:** the spec records what plan 4b built, the numbers it measured, and the counts it moved.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`.

**Steps:**

1. **The counts.** Re-measure the spec's "Where the code is today" counts as plan 3b did. Expect, if plan 5 has not merged: 111 rows (`ReleaseSubagent` added), 43 of them an existing ACP method (`Sheet`, `RefreshSheet`, `Callers` and `ReleaseSubagent` added), 65 new in P3 carried by 58 distinct new methods, and 3 carrying none; and 78 registered methods (75 plus `session/stack_busy`, `session/sheet` and `session/callers`). If plan 5 merged first, measure the merged tree and record what it shows. Update the section's numbers.
2. **The Implementation record.** Add a "Plan 4b — the transcript read and the sheet" entry after plan 3b's. It names: `Reads` and the runner-less reads; the state's clock; the busy flag, `session/stack_busy` and the two goldens that decided it; `SheetManager`, its forwarder and `sheetInterval`; `session/callers`; `registerTranscript` and the unsubscribe that releases the sheet; the variadic barrier; the host-owned `rpc.Stacks`, `OnChange` and the sheet mirror; `ReleaseSubagent` in the interface, the service and `popDrill`; the harness's cases; the busy flip's median against plan 4c's flush; and the cadence decision with its numbers. Plan 5 adds its entry at the same place. If both have merged, keep both entries, in merge order.
3. **`AGENTS.md`.** The map's `internal/acp/` line gains "sheet" in its list, after "status".
4. **Main may have moved.** If plan 5 merged while this branch ran, merge `main` into the branch before the full verification, and resolve the conflicts as "Integration with plan 5" says.
5. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-4b-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-4b-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`, `TestNoticeCallBudget`, and now `TestStackBusyFlipBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.

No manual run: nothing a user sees changes.

## Integration with plan 5

Plan 5 adds the request manager and its mirror. Whichever branch merges second merges `main` first and resolves these, keeping both sides:
- **`responseBarrier`'s declaration** is the same in both plans, so it merges cleanly. If it does not, keep one copy.
- **`host.go`'s `registerHandlers`:** both build a manager after the status manager, both append it to the `responseBarrier` call, and both close it in the cleanup. The barrier's order is `turns, works, watches, statuses, sheets, requests`.
- **`conformance_test.go`:** both add methods to `seam` and a line to `openPipeSeamTB` that serves their handlers and adds their manager to its `responseBarrier` call.
- **`rpc/session.go`'s `routeNotify`:** both add a case for their update kind.
- **`acpclient/methods.go`:** this plan's `Callers` and `ReleaseSubagent` rows sit next to plan 5's `Pending` row.
- **The spec:** both add an Implementation record entry and re-measure the counts. Record the merged tree's counts.

## Self-review

- **Spec coverage.** R3's `Stack` and `Sheet` rows: Tasks 3 and 4. R4's `busy` decision against the goldens: decision 1, Task 2, and the goldens unchanged in Task 5's Verify; its cadence: decision 5; subagents and their release: Tasks 3 to 5; reads without a runner: Task 2. R2 and V3, read your writes for the sheet: Task 3's `TestRefreshSheetReturnsAfterItsSheet`; for the busy flip: `TestStackBusySetsTheFlagTheStacksAreProjectedWith`, which reads with no `settle`. R9 and V6: `TestAClientWithoutTheCapabilityGetsNoSheetUpdates`, and no existing method's bytes change. R10: `TestStackBusyFlipBudget`. V1: Task 6. V4: the budget runs on the 2,000-call session. V8: Task 7.
- **What could go wrong.**
  - **A flip stalls the TUI.** It costs one flush, twice a turn, at a moment the transcript changes anyway; the budget test bounds it at 10ms on the largest fixture.
  - **The flag reaches a subagent's finished tree late.** `StackBusy` clears `final` before it flushes the children, and `TestStackBusyReachesAFinishedSubagent` pins it.
  - **A typed nil in the variadic barrier.** Every caller passes managers that exist or an untyped nil; step 5 of Task 3 says so.
  - **The sheet poll's cost.** 90µs per subscribed sheet per tick on the largest fixture; a subagent's is released with its drill-in.
  - **Plan 5.** The shared files and their resolutions are listed above.
