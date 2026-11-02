# P2 plan 5b: turn control — implementation plan

Date: 2026-11-02. Status: ready for review.
Base: `main` once plan 5a (`p2-5a-work`) has merged. Written while plan 5a ran.
- Anchors were checked at `80532121` (plan 4 merged). Plan 5a changes none of the TUI functions this plan edits. It does add `Session` methods, `Methods` rows, `Deps` fields and `movedStateReads` entries, so this plan places its own additions at the end of each list rather than after a named entry. Plan 5a also turns `Run`'s `inproc.Deps` literal into a local `deps` built before `inproc.New`. Task 1 adds its two fields to that literal wherever it stands.
- The JSON literals in Contracts were produced by encoding the declared types, and the declarations pass T2 (`TestWireFieldsFollowTheRules`).

Approved design: [p2-client-seam.md](../p2-client-seam.md): C3 (wire rules), C4 (the adapter), C8 (turn control), Migration order item 5b, its revision record entry "2026-11-02, against `80532121` (written while plan 5a ran)", and tests T1b, T2, T4 and T5.
Previous plan: [plan 5a, work](2026-11-02-p2-5a-work-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI steers, pops and clears the steering queue, stops a drilled subagent, clears a finished run, takes the watch-resume latch, and changes the interaction mode, the effort and the sudo unlock through the session. Nothing a user sees changes (decision 9).
- `acpwire` gains `InteractionPatch` and the effort timing values.
- `acpclient.Session` gains `Steer`, `PopSteering`, `ClearSteering`, `SetInteraction`, `SetEffort`, `SetSudoUnlock`, `CancelSubagent`, `ClearFinishedRun` and `TakeWatchResume`.
- `inproc.Deps` gains the runtime's interaction and effort setters. `tui.WithInteractionSetter`, `tui.WithSessionEffortSetter` and `app`'s `accessTUIOptions` are deleted.
- T1b gains eight names. T1's allowlist does not change (decision 10).

**Non-goals:**
- **No `SetIdle`.** The three idle resets stay in the run-finish handlers until plans 5c and 5d move those ends agent side (decision 2).
- **No turn stop.** `BeginTurnStop`, `RequestTurnStop` and `EndTurnStop` move with `Prompt` in plan 5c.
- **No SDD gate.** `ClearSDDGate` moves with `AnswerSDDGate` in plan 5d.
- **No ACP change.** `session/steer`, `session/set_mode` and `session/set_effort` are unchanged, and the six new ACP methods are P3's to add.
- **No notes.** The system notes these paths write (`m.state.AddMessage`) stay until plan 6.
- **No web change.**
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-5b-suite`. Create the directory first: `mkdir -p /private/tmp/p2-5b-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green.** The full suite passed at `80532121` (117 packages `ok`, no failures), and plan 5a's Task 7 runs it again at its merge. A failure during this plan is new, unless it also fails on that merge, or it is a known load flake (Task 4) that passes on its own with `-count=3`.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **Production always wires both setters.** `Run` passes `accessTUIOptions(rt)` unconditionally (`app.go:2854`), and the options bind `rt.SetInteraction` and `rt.SetSessionEffort` (`app.go:2150`). Both read the runtime's current state, so they keep working after a swap. No test pins the TUI's "unavailable" notes for an unwired setter.
- **`New` defaults `m.ctx` to `context.Background()`** (`model.go:1669`), so the TUI passes `m.ctx` to the new calls, as plan 4's calls do. The adapter reads no context in this plan.

**Decisions this plan makes beyond the spec** (decisions 1 to 6 are also recorded in the spec's revision record):

1. **Nine calls, each a thin forward to the state or the runtime.** The adapter adds no policy. A call that returns an error returns `errSessionClosed` on a closed session. A call that reports a result with a bool reports nothing on a closed session, as `ReconcileAccess` does.
2. **No `SetIdle`.** The TUI sets the session idle in three places, and all three are where a run the TUI owns ends: `handleAgentFinished` (twice, the SDD gate's branch included) and `handlePlanAuthorFinished`. Plans 5c and 5d move those ends agent side, so a `SetIdle` added now would be deleted again. `SetActivity` stays off T1b until then.
3. **`ClearFinishedRun` replaces `ClearRunEvents` and `ClearSDDProgress`.** The TUI's one caller, `clearFinishedRun`, clears both together. The TUI keeps its own test of `Status().SDD.Finished` before calling.
4. **`SetInteraction` takes `acpwire` types.** `acpclient` may use only `acpwire` and C3's shared data packages, and `interaction` is not one of them. `acpwire.InteractionPatch` carries the patch. The result is `Status`'s `acpwire.Interaction`, which the adapter now builds with one helper, `interactionOf`, shared with `Status`. The TUI keeps importing `interaction` (an allowed package) for its mode constants.
5. **The setters move into `Deps`.** `inproc.Deps` gains `SetInteraction` and `SetEffort`, bound to `rt.SetInteraction` and `rt.SetSessionEffort` in `Run`'s `inproc.Deps` literal. That is where `accessTUIOptions` bound the options, so they keep working after a swap. Without a setter, the call returns an error, and the TUI prints the error as it prints a setter's failure. The two "unavailable" notes go, because the TUI can no longer tell a missing setter from a failing one, and production always wires both (Assumptions).
6. **`Steer` is the queued delivery, and in process it always queues.** `flushQueuedSteering` puts a refused follow-up back on the queue while no turn runs. ACP's `session/steer` requires an active turn, so that put-back moves agent side with `Prompt` in plan 5c. The six new ACP names are in the Contracts' doc comments.
7. **`SetEffort` reports when the change applies** as `acpwire.EffortAppliesNextStep` or `EffortAppliesNextTurn`, which equal the session's values. `effort.go` compares with the `acpwire` value.
8. **`CancelSubagent` keeps the state's bool.** The TUI's drilled Ctrl+X consumes the key either way, and says "Agent could not be stopped; queue unchanged" when the call reports false.
9. **Nothing a user sees changes**, except the note an unwired interaction or effort setter would print (decision 5). Production never reaches it.
10. **The allowlist does not change.** Every file this plan edits keeps its `marshal/internal/app/session` import for the notes it writes, which plan 6 moves. T1b gains `PushSteering`, `PopSteering`, `ClearSteering`, `CancelSubagent`, `ClearRunEvents`, `ClearSDDProgress` and `TakeWatchResume` in Task 2, and `SetSudoUnlockEnabled` in Task 3.

**Execution:**
- Branch first: `git switch -c p2-5b-turn-control` from `main`, after plan 5a has merged.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-5b-turn-control: record the plan 5b revisions and plan 5b`. The main checkout holds other untracked files (`docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-5b-turn-control: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.** `status.go` gains, after `Interaction`:

```go
// InteractionPatch changes some of a session's interaction state (C8). A nil
// field is left alone, so revoking system access (false) and leaving it alone
// (nil) are different patches. Mode is "ask" or "act".
type InteractionPatch struct {
	Mode   *string `json:"mode,omitempty"`
	Auto   *bool   `json:"auto,omitempty"`
	System *bool   `json:"system,omitempty"`
}

// When an effort change takes effect, as SetEffort reports it. They equal
// session.EffortAppliesNextStep and session.EffortAppliesNextTurn.
const (
	EffortAppliesNextStep = "next_step"
	EffortAppliesNextTurn = "next_turn"
)
```

The JSON `TestInteractionPatchJSON` pins:
- mode "act", auto false, system true: `{"mode":"act","auto":false,"system":true}`;
- the empty patch: `{}`;
- system false alone: `{"system":false}`.

**`internal/acpclient`, after Task 1.** `Session` gains, at the end of the interface, in this order:

```go
	// Steer queues text for the running turn to read at its next step. In
	// process it queues even when no turn runs.
	//
	// ACP: session/steer (exists), with the queued delivery.
	Steer(ctx context.Context, text string) error

	// PopSteering removes and returns the oldest queued steering text. It
	// reports false when the queue is empty, and on a closed session.
	//
	// ACP: session/steering_pop (new in P3).
	PopSteering(ctx context.Context) (string, bool)

	// ClearSteering drops the queued steering text. An accepted Send now
	// request keeps the entry it covers.
	//
	// ACP: session/steering_clear (new in P3).
	ClearSteering(ctx context.Context) error

	// SetInteraction applies one interaction patch and returns the committed
	// state. A nil field is left alone.
	//
	// ACP: session/set_mode (exists).
	SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error)

	// SetEffort sets the session's effort override for the active route, or
	// clears it for "", and returns when the change applies:
	// EffortAppliesNextStep or EffortAppliesNextTurn.
	//
	// ACP: session/set_effort (exists).
	SetEffort(ctx context.Context, level string) (string, error)

	// SetSudoUnlock turns sudo's own timestamp grace on or off for the
	// session. Status().SudoUnlock reports it.
	//
	// ACP: session/set_sudo_unlock (new in P3).
	SetSudoUnlock(ctx context.Context, on bool) error

	// CancelSubagent asks the running subagent with that ID to stop. It
	// reports false when no running subagent has the ID, and on a closed
	// session.
	//
	// ACP: session/subagent_cancel (new in P3).
	CancelSubagent(ctx context.Context, subagentID int64) bool

	// ClearFinishedRun clears a finished run's progress and its event log.
	//
	// ACP: session/sdd_clear (new in P3).
	ClearFinishedRun(ctx context.Context) error

	// TakeWatchResume takes the watch-resume latch: the watch a resume fire
	// named when it landed too late for the turn that just ended. ok is false
	// when the latch is not set, and on a closed session.
	//
	// ACP: session/watch_resume_take (new in P3).
	TakeWatchResume(ctx context.Context) (name string, repeat bool, ok bool)
```

`Methods` gains these rows at the end, in the same order:

| Name | ACP | Exists |
|---|---|---|
| `Steer` | `session/steer` | true |
| `PopSteering` | `session/steering_pop` | false |
| `ClearSteering` | `session/steering_clear` | false |
| `SetInteraction` | `session/set_mode` | true |
| `SetEffort` | `session/set_effort` | true |
| `SetSudoUnlock` | `session/set_sudo_unlock` | false |
| `CancelSubagent` | `session/subagent_cancel` | false |
| `ClearFinishedRun` | `session/sdd_clear` | false |
| `TakeWatchResume` | `session/watch_resume_take` | false |

**`internal/acpclient/inproc`, after Task 1.**

```go
// inproc.go: Deps gains, at the end:

	// SetInteraction and SetEffort are the runtime's interaction and effort
	// setters. Each reads the runtime's current state, so the same Deps keep
	// working after a swap. Without one, the matching Session call fails.
	SetInteraction func(interaction.Patch) (interaction.State, error)
	SetEffort      func(level string) (applies string, err error)

// inproc.go: Session gains, with the other fields:

	setInteraction func(interaction.Patch) (interaction.State, error)
	setEffort      func(string) (string, error)

// inproc.go

// interactionOf is s on the wire. Status uses it too.
func interactionOf(s interaction.State) acpwire.Interaction

// controls.go (new)

func (s *Session) Steer(ctx context.Context, text string) error
func (s *Session) PopSteering(ctx context.Context) (string, bool)
func (s *Session) ClearSteering(ctx context.Context) error
func (s *Session) SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error)
func (s *Session) SetEffort(ctx context.Context, level string) (string, error)
func (s *Session) SetSudoUnlock(ctx context.Context, on bool) error
func (s *Session) CancelSubagent(ctx context.Context, subagentID int64) bool
func (s *Session) ClearFinishedRun(ctx context.Context) error
func (s *Session) TakeWatchResume(ctx context.Context) (name string, repeat bool, ok bool)

// errNoInteraction and errNoEffort are what SetInteraction and SetEffort
// return when Deps carried no setter.
var (
	errNoInteraction = errors.New("inproc: no interaction setter")
	errNoEffort      = errors.New("inproc: no effort setter")
)
```

**Test helpers.**

```go
// tui: controls_seam_test.go (new, Task 2)

// controlRecorder is a session whose turn-control calls are recorded before
// the wrapped session makes them. Every other method is the wrapped session's.
type controlRecorder struct {
	acpclient.Session
	steers       []string
	pops         int
	clears       int
	cancels      []int64
	runClears    int
	resumeTakes  int
	interactions []acpwire.InteractionPatch // Task 3
	efforts      []string                   // Task 3
	unlocks      []bool                     // Task 3
}

// testModelControlled builds a model over an adapter made from d whose
// session is rec, wrapped around the adapter's session.
func testModelControlled(d inproc.Deps, rec *controlRecorder, opts ...Option) Model

// tui: effort_test.go (Task 3)

// withEffortSetter replaces m's session with one over m's state whose
// adapter calls set for SetEffort.
func withEffortSetter(m Model, set func(string) (string, error)) Model

// tui: commands_plan_test.go (Task 3)

// newDispatchModelWith is newDispatchModel over an adapter made from d.
func newDispatchModelWith(t *testing.T, d inproc.Deps, opts ...Option) Model
```

## Task 1: The session takes the turn-control calls

**Goal:** the adapter answers all nine calls over the state and the runtime's setters, and `Run` gives it the setters. Nothing in the TUI calls them yet.

**Files:** `internal/acpwire/status.go`, `controls_test.go` (new); `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `controls.go` (new), `controls_test.go` (new); `internal/app/app.go`.

**Steps:**

1. **`acpwire/status.go`.** Add `InteractionPatch` and the two effort values after `Interaction` (Contracts).
2. **`acpclient.go`.** Add the nine methods at the end of `Session`, with their Contracts doc comments. **`methods.go`:** add their rows at the end of `Methods`, with the Contracts' `Exists` column.
3. **`inproc/inproc.go`:**
   - `Deps` gains `SetInteraction` and `SetEffort`, and `Session` gains `setInteraction` and `setEffort` (Contracts);
   - **`interactionOf`** (Contracts) holds the conversion `Status` writes inline for `Interaction` today, and `Status` calls it.
4. **`inproc/events.go`.** `newSession` copies the two setters from `Deps`.
5. **`inproc/controls.go`** (new). Every call follows decision 1 for a closed session, then:
   - **`Steer`** calls `s.state.PushSteering(text)`;
   - **`PopSteering`** returns `s.state.PopSteering()`;
   - **`ClearSteering`** calls `s.state.ClearSteering()`;
   - **`SetInteraction`** returns `errNoInteraction` without a setter. Otherwise it builds an `interaction.Patch` whose `Mode` points at `interaction.Mode(*patch.Mode)` when `patch.Mode` is set, and whose `Auto` and `System` are the patch's pointers. It calls the setter and returns `interactionOf` of the committed state, with the setter's error unchanged;
   - **`SetEffort`** returns `errNoEffort` without a setter. Otherwise it returns the setter's result and error unchanged;
   - **`SetSudoUnlock`** calls `s.state.SetSudoUnlockEnabled(on)`;
   - **`CancelSubagent`** returns `s.state.CancelSubagent(subagentID)`;
   - **`ClearFinishedRun`** calls `s.state.ClearSDDProgress()`, then `s.state.ClearRunEvents()`, the order `clearFinishedRun` uses;
   - **`TakeWatchResume`** returns `s.state.TakeWatchResume()`.
6. **`app/app.go`.** `Run`'s `inproc.Deps` literal gains `SetInteraction: rt.SetInteraction` and `SetEffort: rt.SetSessionEffort`. `accessTUIOptions` is unchanged until Task 3.
7. **Tests:**
   - **`acpwire/controls_test.go`** (new): **`TestInteractionPatchJSON`** encodes the three patches and compares the bytes with the Contracts literals, in the table style of `TestRequestsJSON`;
   - **`inproc/controls_test.go`** (new). Each test builds its state with `newState(t, "s1")`:
     - **`TestSteerQueuesAndPopTakesTheOldest`**. After `Steer` with "a" then "b", `state.SteeringQueue()` holds both in order. `PopSteering` returns "a" then "b", each with true, then false;
     - **`TestClearSteeringEmptiesTheQueue`**. After two `Steer` calls and `ClearSteering`, the queue is empty;
     - **`TestSetInteractionPassesThePatch`**. `Deps.SetInteraction` records its patch and returns `interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 5}`. `SetInteraction` with mode "act" and system true records a patch whose mode is `interaction.ModeAct`, whose system is true and whose auto is nil, and returns `acpwire.Interaction{Mode: "act", Auto: true, System: true, Revision: 5}`;
     - **`TestSetInteractionReturnsTheSettersError`**. A setter error comes back as it is;
     - **`TestSetEffortReportsWhenItApplies`**. `Deps.SetEffort` records "low" and returns `session.EffortAppliesNextStep`. `SetEffort` returns `acpwire.EffortAppliesNextStep` and no error;
     - **`TestWithoutTheSettersTheCallsFail`**. Without `Deps.SetInteraction` and `Deps.SetEffort`, both calls return errors, and the state's interaction is unchanged;
     - **`TestSetSudoUnlockReachesStatus`**. After `SetSudoUnlock(ctx, true)`, `Status().SudoUnlock` is true, and after `false` it is false;
     - **`TestCancelSubagentStopsOnlyARunningChild`**. A registered child with a cancel function (`SetSubagentCancel`) is stopped: the call reports true and the function ran. An unknown ID reports false;
     - **`TestClearFinishedRunClearsProgressAndEvents`**. With `SetSDDProgress(session.SDDProgress{PlanName: "p"})` and one `AddRunEvent`, `ClearFinishedRun` leaves `state.SDDProgress().PlanName` empty and `state.RunEvents()` empty;
     - **`TestTakeWatchResumeTakesTheLatchOnce`**. After `state.SetWatchResume("ci", true)`, `TakeWatchResume` returns "ci", true, true, and a second call reports false;
     - **`TestEffortAppliesValuesAreTheSessions`**. `acpwire.EffortAppliesNextStep` and `EffortAppliesNextTurn` equal the session's constants;
     - **`TestAClosedSessionControlsNothing`**. After `host.Swap`, the old session's `Steer`, `ClearSteering`, `SetInteraction`, `SetEffort`, `SetSudoUnlock` and `ClearFinishedRun` return errors with `acpwire.CodeSessionClosed`. `PopSteering`, `CancelSubagent` and `TakeWatchResume` report false, and the old state's queued text and armed latch are still there.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/acpclient/inproc/ -run 'Steer|Steering|Interaction|Effort|Sudo|Cancel|FinishedRun|WatchResume|ControlsNothing|StatusMirrors' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/ -run 'Access'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/tui/...
go vet ./internal/acpwire/ ./internal/acpclient/... ./internal/app/
```

Expected: every listed test passes, `TestAdapterDoesNotImportApp` included. T4 passes on both sides: the three existing ACP names are registered and the six new ones are not. `tui` passes unchanged, because its test sessions embed `acpclient.Session`.

## Task 2: Steering, the subagent stop, the finished run and the watch resume go through the session

**Goal:** the TUI queues, pops and clears steering text, stops a drilled subagent, clears a finished run and takes the watch-resume latch through the session, and reads none of those from the state.

**Files:** `internal/app/tui/keypress.go`, `model.go`; `controls_seam_test.go` (new); `moved_reads_test.go`.

**Steps:**

1. **`keypress.go`, `handleKeypress`:**
   - the drilled Ctrl+X (`ctrlXStopAgent`) calls `m.sess.CancelSubagent(m.ctx, top.ID)` and keeps both outcomes;
   - the undrilled Ctrl+X (`ctrlXClearQueue`) calls `m.sess.ClearSteering(m.ctx)`, ignoring the error;
   - Enter while busy calls `m.sess.Steer(m.ctx, value)`, ignoring the error.
2. **`keypress.go`, `clearFinishedRun`.** When `Status().SDD.Finished`, it calls `m.sess.ClearFinishedRun(m.ctx)` in place of the two state calls, ignoring the error.
3. **`model.go`:**
   - **`popOldestSteering`** returns `m.sess.PopSteering(m.ctx)`;
   - **`flushQueuedSteering`** puts a refused follow-up back with `m.sess.Steer(m.ctx, followUp)`;
   - **`handleAgentFinished`** clears a cancelled turn's queue with `m.sess.ClearSteering(m.ctx)`, and takes the latch with `m.sess.TakeWatchResume(m.ctx)`. Its two `SetActivity` calls stay (decision 2);
   - **`dispatchCommand`**, in its prompt-body branch and its agent-goal branch, queues the goal with `m.sess.Steer(m.ctx, …)` while busy.
4. **`moved_reads_test.go`.** `movedStateReads` gains, at plan `"5b"`: `PushSteering`, `PopSteering`, `ClearSteering`, `CancelSubagent`, `ClearRunEvents`, `ClearSDDProgress` and `TakeWatchResume`. The comment says the steering queue, the drilled subagent's stop, a finished run's clear and the watch-resume latch go through the session.
5. **Tests:**
   - the tests that drive these paths are unchanged and now go through the adapter: `TestEnterWhileBusyEnqueuesSteering`, `TestCtrlXClearsSteering`, `TestEnterOnEmptyDrainsQueuedFollowUp`, `TestCancelTurnDropsSteeringQueue`, `TestAgentFinishedFlushesQueuedSteering`, `TestFlushQueuedSteeringStartsFollowUpTurn`, `TestDispatchPromptCommandBusySteers` (`model_test.go`); `TestCtrlXStopsRunningSubagentWhileDrilled`, `TestCtrlXUndrilledClearsSteeringQueue`, `TestCtrlXFailedDrilledStopKeepsQueue` (`drill_test.go`); `TestNextTurnClearsRunEvents` (`run_panel_test.go`); `TestHandleAgentFinishedConsumesLatch` and its neighbours (`watch_resume_test.go`);
   - **`controls_seam_test.go`** (new) holds `controlRecorder` and `testModelControlled` (Contracts). Each recording method appends what it was given, or counts the call, then calls the wrapped session's. `testModelControlled` builds as `answers_seam_test.go`'s `testModelRecording` does, over `inproc.New(d)`;
   - **new, in `controls_seam_test.go`:**
     - **`TestQueuedInputSteersThroughTheSession`.** A busy model, as `TestEnterWhileBusyEnqueuesSteering` sets it up, submits "next". The recorder holds the steer "next", and `state.SteeringQueue()` holds it;
     - **`TestCtrlXClearsTheQueueThroughTheSession`.** As `TestCtrlXClearsSteering` drives it, the recorder counts one clear;
     - **`TestADrilledCtrlXCancelsThroughTheSession`.** As `TestCtrlXStopsRunningSubagentWhileDrilled` sets it up, the recorder holds the drilled view's ID;
     - **`TestAFollowUpPopsThroughTheSession`.** As `TestFlushQueuedSteeringStartsFollowUpTurn` sets it up, the recorder counts one pop;
     - **`TestTheNextTurnClearsTheFinishedRunThroughTheSession`.** As `TestNextTurnClearsRunEvents` sets it up, the recorder counts one run clear;
     - **`TestATurnEndTakesTheLatchThroughTheSession`.** As `TestHandleAgentFinishedConsumesLatch` sets it up, the recorder counts one take, and the resumed turn starts.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'm\.state\.(PushSteering|PopSteering|ClearSteering|CancelSubagent|ClearRunEvents|ClearSDDProgress|TakeWatchResume)\(' internal/app/tui --include='*.go' | grep -v '_test.go'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/tui/ -run 'ThroughTheSession|Steer|CtrlX|FollowUp|RunEvents|Latch|WatchResume|MovedStateReads' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/
```

Expected: the grep prints nothing. Every listed test passes, and `tui` passes, the goldens included.

## Task 3: Interaction, effort and the sudo unlock go through the session

**Goal:** mode, auto and system-access changes, effort changes and the sudo unlock go through the session. The two setter options and `accessTUIOptions` are deleted.

**Files:** `internal/app/tui/model.go`, `effort.go`, `commands_dispatch.go`; `effort_test.go`, `commands_plan_test.go`, `interaction_commands_test.go`, `system_access_test.go`, `model_test.go`, `controls_seam_test.go`, `moved_reads_test.go`; `internal/app/app.go`, `app_test.go`.

**Steps:**

1. **`model.go`:**
   - delete the `interactionSetter` and `effortSetter` fields with their comments, and `WithInteractionSetter` and `WithSessionEffortSetter`;
   - **`applyInteraction`** takes an `acpwire.InteractionPatch` and returns `(acpwire.Interaction, bool)`. It calls `m.sess.SetInteraction(m.ctx, patch)`. On an error it writes the same "Could not change interaction state: %v" note and reports false. Its nil-setter branch goes. On success it sets `m.syncedAccessMode` to the committed `Mode`;
   - **`modeReceipt`** takes an `acpwire.Interaction`, and compares `Mode` with `string(interaction.ModeAsk)` and `string(interaction.ModeAct)`;
   - **`setInteractionMode`**, **`setAuto`** and **`setSystem`** build `acpwire.InteractionPatch` values. `setInteractionMode` points `Mode` at `string(mode)`.
2. **`effort.go`, `applyEffort`.** It calls `m.sess.SetEffort(m.ctx, level)`. Its nil-setter branch goes. It compares the result with `acpwire.EffortAppliesNextStep`.
3. **`commands_dispatch.go`.** The `system` entry of `tuiCommandEffects`, in its `unlock` branch, calls `m.sess.SetSudoUnlock(m.ctx, on)` in place of the state call. When that returns an error it writes "Could not change the privileged unlock: %v" and returns. The notes are otherwise unchanged.
4. **`app/app.go`.** Delete `accessTUIOptions` and its call in `Run`. The setters reach the TUI through `Deps` (Task 1).
5. **`moved_reads_test.go`.** `movedStateReads` gains `"SetSudoUnlockEnabled": "5b"`, with a comment: `/system unlock` goes through the session.
6. **TUI tests:**
   - a test that passed `WithInteractionSetter(fn)` to `testModel(state, …)` builds its model with `testModelWith(inproc.Deps{State: state, SetInteraction: fn}, …)` and the other options. That covers `system_access_test.go` (`TestSystemCommandTogglesFlagAndNotes`, and `newAccessTestFixture`, whose `Deps` already exist) and the five fixtures in `model_test.go`;
   - **`commands_plan_test.go`** gains `newDispatchModelWith` (Contracts), and `newDispatchModel` calls it with `inproc.Deps{State: state}`. The fixture that passed the setter uses `newDispatchModelWith` with the setter in `Deps`;
   - **`interaction_commands_test.go`.** `newInteractionDispatchModel` builds with `newDispatchModelWith` and `inproc.Deps{State: state, SetInteraction: state.ApplyInteraction}`. `TestInteractionCommandFailureDoesNotClaimSuccess` puts its failing setter in `Deps` the same way;
   - **`effort_test.go`** gains `withEffortSetter` (Contracts), which replaces the session as plan 4's `TestSendNowComposerRoutesSuspendedAsyncResultsOnce` did. Its four tests call it in place of assigning `m.effortSetter`, and their setters return `acpwire.EffortAppliesNextTurn`;
   - **new, in `controls_seam_test.go`:**
     - **`TestModeChangesGoThroughTheSession`.** Over `inproc.Deps{State: state, SetInteraction: state.ApplyInteraction}`, `/act` records one patch whose mode is "act", and `Status().Interaction.Mode` is "act";
     - **`TestEffortGoesThroughTheSession`.** Over `Deps` whose `SetEffort` returns `session.EffortAppliesNextTurn`, `/effort low` records "low", and the last note is "Effort set to low (from the next turn).";
     - **`TestSystemUnlockTogglesThroughTheSession`.** `/system unlock` records true, `Status().SudoUnlock` is true, and the last note starts "Privileged unlock enabled". A second `/system unlock` records false.
7. **`app/app_test.go`:**
   - **`TestAdapterAccessResolvesWithoutConfigReload`** adds `SetInteraction: rt.SetInteraction` and `SetEffort: rt.SetSessionEffort` to its `Deps`, and passes no options;
   - **`TestAdapterAccessFollowsNewSession`** does the same to the `Deps` it builds once, and drops `opts`. After the swap, it also checks that `/act` on the replacement model changes `newState`'s interaction mode to act, through the same `Deps`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'WithInteractionSetter\|WithSessionEffortSetter\|interactionSetter\|effortSetter\|accessTUIOptions\|SetSudoUnlockEnabled' internal --include='*.go' | grep -v '_test.go' | grep -v 'internal/app/session/\|internal/acpclient/inproc/\|internal/acp/'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/tui/ -run 'ThroughTheSession|Effort|Interaction|Mode|System|MovedStateReads' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/tui/...
TMPDIR=/private/tmp/p2-5b-suite go test ./internal/app/ -run 'Access' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/...
```

Expected: the grep prints nothing. Every listed test passes, and `tui` passes, the goldens included.

## Task 4: Verification and docs

**Goal:** the full suite is green, and the spec and design record plan 5b.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 5b entry to the Implementation record, after plan 5a's. It lists:
   - `acpwire.InteractionPatch` and the two effort values;
   - the nine `Session` methods and their `Methods` rows: three on existing ACP methods, six new;
   - what the adapter does: thin forwards to the state, the runtime's setters in `Deps` bound in `Run`, `interactionOf` shared with `Status`, and a closed session refusing every call;
   - that the TUI makes these calls through the session, that the two setter options and `accessTUIOptions` are gone, and that the three idle resets stay for plans 5c and 5d;
   - T1b's eight new names at `5b`, and that the allowlist did not change.
2. **`design.md` §7.** In the P2 row, "3d, 4 and 5a implemented" becomes "3d, 4, 5a and 5b implemented".
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-5b-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-5b-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing. Treat a failure as new unless it also fails on plan 5a's merge, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** At `80532121` every package passed (117 `ok`). Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository and check by eye:
- while a turn runs, type a message and press Enter: it shows as queued, and the turn reads it. Queue another and press Ctrl+X: the queue empties;
- dispatch a subagent, drill into it, and press Ctrl+X: the subagent stops;
- `/act`, `/ask`, `/auto on` and `/system on`: each changes the status badge and writes its receipt;
- `/effort low`, then Alt+E to cycle: the note says when each applies;
- `/system unlock` twice: the notes say enabled, then disabled.

## Integration notes

- **Plan 5c (Prompt)** moves turn ends agent side. It deletes the idle resets in `handleAgentFinished`, moves the refused follow-up's put-back with `Prompt` (decision 6), and takes `Stop` with the turn-stop calls. `TakeWatchResume` stays a TUI call: the TUI decides whether a resumed turn may start.
- **Plan 5d (pipelines)** deletes `handlePlanAuthorFinished`'s idle reset and moves `ClearSDDGate` with `AnswerSDDGate`.
- **Plan 6** moves the notes these paths write.
- **P3:**
  - the client sends `session/steer` with queued delivery, `session/set_mode` (mapping `system` to `system_access`) and `session/set_effort`;
  - it adds `session/steering_pop`, `session/steering_clear`, `session/set_sudo_unlock`, `session/subagent_cancel`, `session/sdd_clear` and `session/watch_resume_take`.

## Self-review

- **Every task verifiable alone.** Task 1 adds calls nothing in the TUI uses. Task 2 moves the state calls, and Task 3 the setters and the unlock. Each keeps T1, T1b and the goldens green.
- **Cold executor.** Each step names the function, the call it makes after the change, and what it does with the result. Each new test names its setup and the behaviour it pins.
- **Contracts.** The wire type and values, the nine `Session` methods and their rows, `Deps`' setters, the adapter's helpers and errors, and the test helpers are declared there.
- **Anchors.** Every path, symbol and line was checked at `80532121`. Plan 5a edits none of the TUI functions this plan changes. The one shared spot is `Run`'s `inproc.Deps` literal, which Task 1 extends wherever it stands, and this plan adds its interface methods, rows, `Deps` fields and `movedStateReads` entries at the end of each list.
- **Spec.** C8 and the Migration order are updated, with the revision record "2026-11-02, against `80532121` (written while plan 5a ran)" holding decisions 1 to 6.
- **No placeholders.**
