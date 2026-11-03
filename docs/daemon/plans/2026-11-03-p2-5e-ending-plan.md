# P2 plan 5e: Ending and swapping — implementation plan

Date: 2026-11-03. Status: ready for review.
Base: `main` at `bf211f49` (plan 5d merged). Every anchor below was checked there.
- The JSON literals in Contracts were produced by encoding the declared types, and the declarations pass T2 (`TestWireFieldsFollowTheRules`).
- One of this plan's changes fixes a bug found while writing it (decision 10). Throwaway tests confirmed three of its four effects; the fourth was confirmed by reading the code.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C4 (the adapter), C8 (turns and runs), Migration order item 5e, its revision record entry "2026-11-03, against `bf211f49` (plan 5d merged)", and tests T1, T1b, T2 and T4.
Previous plan: [plan 5d, pipelines](2026-11-03-p2-5d-pipelines-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI ends a conversation and swaps in a new one through the client seam. The active-work warning, the acceptance, the settlement, the exit analysis and the postmortem report go through the session. Quitting closes the session. `/new` and `/clear` get their session from the host.
- `acpwire` gains the end types: `EndWork`, `EndWorkItem`, `EndAcceptParams`, `EndDecision`, and the two intent constants.
- `acpclient.Session` gains `ActiveWork`, `AcceptEnd`, `WaitForEnd`, `RunExitAnalysis`, `Postmortem` and `Close`, and `acpclient.Host` gains `NewSession`.
- `inproc` gains `EndService`, `Deps.End` and `Deps.NewSession`. The runtime's `NewSession` returns only the new state.
- The TUI's `EndController`, `WithEndController`, `SessionSwapper`, `SessionSwapperFunc`, `SessionSwapResult` and `WithSessionSwapper` are deleted. `WithLegacySession` hands the model the new session's legacy values until plan 9.
- A bug that left Marshal unable to run anything after `/new` or `/clear` is fixed (decision 10).

**Non-goals:**
- **No in-place resume.** `/resume` keeps ending the program and letting `Run` restart the runtime on the chosen session (decision 9). No `Host.Resume` is added.
- **No change to the runtime's end machinery.** `AcceptEnd`, `WaitForEnd` and `RunExitAnalysis` keep their semantics. `Run`'s teardown after the program returns (quiesce, end the session row, the shutdown hook, `rt.Close`) stays as it is.
- **No notes or notices.** "Kept open. Active work continues.", "Could not stop all work: …", "Cannot end the conversation: …", "Postmortem written to …", "Postmortem failed: …", "Postmortem analysis failed: …", "Resuming session …", "Started new conversation …" and "Failed to start new session: …" stay TUI writes until plan 6.
- **No config.** The exit policy keeps reading `postmortem.on_exit` from the state's config until plan 7.
- **No message queries or history.** `sessionHasUserTurn`, the cleared-message count and the session picker's `ListSessions` keep reading the state until plan 8.
- **No work-view change.** A session started without a provider still serves no work (Integration notes).
- **No ACP change, no web change, no changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-5e-suite`. Create the directory first: `mkdir -p /private/tmp/p2-5e-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green.** At `bf211f49`, all 117 packages passed, `web/bridge` included. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **`Run` always wires the end controller and the swapper** (`app.go:2871`, `2944`), whether or not the provider is ready. A model without them exists only in tests.
- **The runtime's end calls read `rt.State`** (`runtime.go:392`, `422`, `527`, `723`). `rt.NewSession` settles the old state's prompts, shuts it down (`runtime.go:2208`) and resets the runtime's end state to open (`runtime.go:2214` to `2221`). So the state a swap replaces is already shut down when the host closes its adapter session.
- **Resume restarts the program.** `finishReplacement` records the resume ID and quits (`model.go:5014` to `5021`). `Run` reads it from the program result, closes the runtime and starts a new one on that session (`app.go:2948` to `2957`). `rt.NewSession` cannot open a stored session: only `startRuntime` loads one.
- **`Update` has a value receiver** (`model.go:1672`). Every command this plan changes captures the session value and its arguments, never `m`.

**Decisions this plan makes beyond the spec** (decisions 1 to 3, 5, 6, 9, 10, 12 and 14 are also recorded in the spec's revision record):

1. **The end calls become `Session` methods over a runtime end service.** `inproc.EndService` holds the runtime's four end calls in wire types, and `Deps.End` carries it. `bindTurnDeps` binds it to `app`'s `endService`, which converts the runtime's shapes, as `tuiEndController` (`app.go:2506`) does today for the TUI's mirror types. `tuiEndController`, the TUI's `EndController` and its mirror types go. `EndAccepted` is dropped from the seam: the TUI never calls it.
2. **`ActiveWork` is a read; `AcceptEnd` is the check.** The warning renders `ActiveWork` without blocking, as it read the controller. It need not be authoritative: the TUI always accepts with `RequireDisplayed`, and the runtime refuses to stop work the user was not shown, returning the membership to ask about again. P3 serves the read from a mirror.
3. **A session without an end service ends at once.** Its `ActiveWork` is empty, `AcceptEnd` accepts, `WaitForEnd` settles at once, and `RunExitAnalysis` refuses with `CodeNoRunner`. The TUI drops its no-controller shortcuts (`model.go:4571`, `4622`, `4784`, `4849`, `4870`, `5139`) and always accepts and settles through the session. Only tests build such a model (Assumptions). In them, a quit now arrives through the settlement command, and a postmortem answered "with agent analysis" reports "Postmortem analysis failed: inproc: no exit analysis" before quitting.
4. **The postmortem report is written agent side.** `Session.Postmortem` builds and writes the report over the session's state, as `writePostmortemFor` (`model.go:5095`) does: the same home directory lookup, project slug and file name. The TUI's extraction command calls it and keeps its latches.
5. **`Close` replaces the TUI's shutdown calls.** `finishQuit` calls `Close` where it called `ResolvePendingForShutdown`, and no longer calls `Shutdown`. `Close` cancels the run in flight and marks it stopped, settles every pending request as shut down, shuts the state down and closes the session: reads keep their final values and calls fail. Closing a closed session does nothing, so a repeated quit is safe. The run path checks for a closed session when it takes the slot, so no run can start between `Close` and its cancel. The TUI's local cancels stay first. The state now shuts down before the dock releases its panels; the one panel with a close hook (`plugins`) only removes its scan clone.
6. **`Host.NewSession` replaces the swapper.** `Deps.NewSession` asks the runtime for a new session and returns its state. `Host.NewSession` calls it, then closes the current session and returns one over the new state. A failure leaves the current session open. `Host.Swap` becomes unexported: nothing outside the package calls it after Task 2. The TUI's "New session is not available in this build." branch (`commands_dispatch.go:173` to `177`) goes, because `Run` always wires the swapper. A host without `NewSession` now fails at the swap with "Failed to start new session: inproc: new sessions are not available".
7. **`WithLegacySession` hands the model the new session's legacy values.** After `Host.NewSession` the model still needs the new `*session.State` and tool registry for the groups plans 6 to 8 move. `WithLegacySession` takes a read of the runtime's current state and registry, which `Run` binds under `rt.mu`. Plan 9 deletes it with `WithLegacyState`.
8. **`rt.NewSession` returns only the state.** Nothing used the runner, the orchestrator, the factories or the registry it also returned (plan 5d's Integration notes). The legacy read takes the registry from `rt.ToolRegistry`.
9. **Resume stays a restart.** The spec's `Host.Resume` is not added. The runtime has no in-place resume: `rt.NewSession` only creates sessions, and a stored session's transcript, generation and work are loaded only by `startRuntime`. The TUI's remaining agent-side options (plans 6 to 8) are bound to the runtime a resume replaces. The TUI keeps reporting the session to resume as its program result, which touches no agent-side code. P3 decides whether resume becomes in place over `session/resume`.
10. **A replacement leaves the end idle.** This fixes a bug. `finishReplacement` sets the end stage to finishing (`model.go:5023`), and nothing resets it after the swap. Since `e57821a4` (2026-10-18), after a `/new` or `/clear`:
    - every run is refused with "The conversation is ending; this request was not started." (`model.go:4212`), so Marshal cannot prompt again until it restarts;
    - a second `/new` quits Marshal without starting a session;
    - `/exit` and Ctrl+C quit at once: no warning, no acceptance, no postmortem question;
    - `/resume` exits instead of resuming, because `beginEnd`'s finishing branch quits before the target is recorded.

    Throwaway tests confirmed the first three; the fourth was confirmed by reading `beginEnd` (`model.go:4521` to `4558`). The swap now returns the end stage to idle, whether the new session started or failed. After a failure the runtime keeps the old conversation frozen, and a retried `/new` goes through acceptance again, which the runtime accepts as a repeat.
11. **The exit analysis reads the runner under the runtime's lock.** The operation `Run` installs (`app.go:2934`) reads `rt.Runner` without `rt.mu`, while a reload may replace it. It now uses `rt.TurnRunner()`, as the run path's calls do. The refusal text is unchanged.
12. **The wire carries what the TUI renders.** An `EndWorkItem` has the ID, the label and the group. The phase, kind, parent flag, subagent ID and target list stay agent side: the warning renders none of them.
13. **T1b gains two names and the allowlist loses one entry.** T1b: `ResolvePendingForShutdown` and `Shutdown`, at plan `5e` (Task 3). The allowlist: `model.go` loses `marshal/internal/postmortem` (Task 3). `commands_dispatch.go` keeps `marshal/internal/app/session` for its notes (plan 6), and `model.go` keeps `marshal/internal/tools/registry` for the roster's registry (plan 7).
14. **The new ACP methods are named** `session/end_work`, `session/end_accept`, `session/end_wait`, `session/exit_analysis` and `session/postmortem`. `Close` and `Host.NewSession` map to `session/close` and `session/new`, which exist.

**Execution:**
- Branch first: `git switch -c p2-5e-ending` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-5e-ending: record the plan 5e revisions and plan 5e`. The main checkout holds other untracked files (`docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-5e-ending: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.** `ending.go` (new):

```go
// The intents an end names (C8): closing the conversation, or replacing it
// with a new or resumed one. A replacement never runs the exit postmortem.
const (
	EndIntentExit    = "exit"
	EndIntentReplace = "replace"
)

// EndWork is the work an end of the session would stop, oldest first: what
// the active-work warning shows.
type EndWork struct {
	Items []EndWorkItem `json:"items,omitempty"`
}

// EndWorkItem is one piece of work an end would stop. ID is opaque and
// distinct for every item. Group is the ID of the operation an attached
// command belongs to, empty when the item stands alone.
type EndWorkItem struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Group string `json:"group,omitempty"`
}

// EndAcceptParams is one acceptance of an end: its intent and the work IDs the
// user was shown. With RequireDisplayed, the end stops nothing while work the
// user was not shown is active.
type EndAcceptParams struct {
	Intent           string   `json:"intent"`
	DisplayedIDs     []string `json:"displayedIds,omitempty"`
	RequireDisplayed bool     `json:"requireDisplayed,omitempty"`
}

// EndDecision is an acceptance's outcome. Accepted false with Expanded set
// means nothing was frozen or stopped: Work is the membership to show and ask
// about again.
type EndDecision struct {
	Accepted bool     `json:"accepted,omitempty"`
	Expanded []string `json:"expanded,omitempty"`
	Work     EndWork  `json:"work"`
}
```

`TestEndingJSON` (Task 1) pins these encodings:

| Value | JSON |
|---|---|
| `EndWork` with items `{ID: "op-1", Label: "agent turn"}` and `{ID: "cmd-2", Label: "go test ./...", Group: "op-1"}` | `{"items":[{"id":"op-1","label":"agent turn"},{"id":"cmd-2","label":"go test ./...","group":"op-1"}]}` |
| `EndWork{}` | `{}` |
| `EndAcceptParams{Intent: EndIntentExit, DisplayedIDs: ["op-1", "cmd-2"], RequireDisplayed: true}` | `{"intent":"exit","displayedIds":["op-1","cmd-2"],"requireDisplayed":true}` |
| `EndAcceptParams{Intent: EndIntentReplace}` | `{"intent":"replace"}` |
| `EndDecision{Expanded: ["cmd-3"], Work: <the first EndWork>}` | `{"expanded":["cmd-3"],"work":{"items":[{"id":"op-1","label":"agent turn"},{"id":"cmd-2","label":"go test ./...","group":"op-1"}]}}` |
| `EndDecision{Accepted: true}` | `{"accepted":true,"work":{}}` |

**`internal/acpclient`, after Task 1.** `Host` gains, after `SharedFilesystem`:

```go
	// NewSession creates a session in the agent and makes it the current one,
	// closing the session it replaces. A name that is not empty is the new
	// session's title. A failure leaves the current session as it was.
	//
	// ACP: session/new (exists).
	NewSession(ctx context.Context, name string) (Session, error)
```

`Session` gains, at the end:

```go
	// ActiveWork is the work an end of the session would stop, as the
	// active-work warning shows it. It is read without blocking; AcceptEnd
	// checks the user's choice against the live membership.
	//
	// ACP: session/end_work (new in P3). An end_work update refreshes it.
	ActiveWork() acpwire.EndWork

	// AcceptEnd accepts an end of the session for params' intent. It checks
	// the displayed IDs and freezes the session's admission in one step: when
	// work the user was not shown is active, it freezes and stops nothing and
	// reports that work in Expanded. Accepting an end already accepted changes
	// nothing.
	//
	// ACP: session/end_accept (new in P3).
	AcceptEnd(ctx context.Context, params acpwire.EndAcceptParams) (acpwire.EndDecision, error)

	// WaitForEnd stops and joins the accepted end's work. It may be retried: a
	// failure reports the work still outstanding and never latches success.
	//
	// ACP: session/end_wait (new in P3).
	WaitForEnd(ctx context.Context) error

	// RunExitAnalysis runs the exit postmortem's agent pass with goal, after an
	// accepted exit has settled. The agent grants the pass its access, restores
	// it afterwards, and joins the pass's work before returning.
	//
	// ACP: session/exit_analysis (new in P3).
	RunExitAnalysis(ctx context.Context, goal string) error

	// Postmortem writes the session's postmortem report and returns the path
	// it wrote.
	//
	// ACP: session/postmortem (new in P3).
	Postmortem(ctx context.Context) (string, error)

	// Close ends the session: it cancels the run in flight, settles every
	// pending request as shut down, and shuts the session down. Reads keep
	// answering with their final values, and calls fail. Closing a closed
	// session does nothing.
	//
	// ACP: session/close (exists).
	Close(ctx context.Context) error
```

`methods.go` gains these rows. The `Host` row goes after `SharedFilesystem`'s, and the `Session` rows go at the end:

```go
	{Iface: "Host", Name: "NewSession", ACP: "session/new", Exists: true},
	{Iface: "Session", Name: "ActiveWork", ACP: "session/end_work"},
	{Iface: "Session", Name: "AcceptEnd", ACP: "session/end_accept"},
	{Iface: "Session", Name: "WaitForEnd", ACP: "session/end_wait"},
	{Iface: "Session", Name: "RunExitAnalysis", ACP: "session/exit_analysis"},
	{Iface: "Session", Name: "Postmortem", ACP: "session/postmortem"},
	{Iface: "Session", Name: "Close", ACP: "session/close", Exists: true},
```

**`internal/acpclient/inproc`, after Task 1** (`Swap` becomes `swap` in Task 2).

```go
// ending.go (new)

// EndService is the runtime's end of a conversation: the work an end would
// stop, the acceptance that freezes it, the settlement that stops and joins
// it, and the exit analysis.
type EndService interface {
	ActiveWork() acpwire.EndWork
	AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error)
	WaitForEnd(ctx context.Context) error
	RunExitAnalysis(ctx context.Context, goal string) error
}

var (
	errNoExitAnalysis = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no exit analysis"}
	errNoNewSession   = errors.New("inproc: new sessions are not available")
)

// inproc.go: Deps gains
	// End is the runtime's end service. Without it the session has no work to
	// warn about: ActiveWork is empty, AcceptEnd accepts, WaitForEnd settles
	// at once, and RunExitAnalysis refuses with CodeNoRunner.
	End EndService
	// NewSession creates a session in the runtime and returns its state.
	// Without it, Host.NewSession fails.
	NewSession func(name string) (*session.State, error)

// inproc.go: Session gains
	end EndService

// inproc.go (Task 2): Swap is renamed
func (h *Host) swap(state *session.State) *Session
```

**`internal/app`, after Task 2.**

```go
// ending.go (new)

// endService is the runtime's end of a conversation behind inproc.EndService.
// It converts the runtime's shapes to the wire's and leaves every decision to
// the runtime.
type endService struct{ rt *Runtime }

func (e *endService) ActiveWork() acpwire.EndWork
func (e *endService) AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error)
func (e *endService) WaitForEnd(ctx context.Context) error
func (e *endService) RunExitAnalysis(ctx context.Context, goal string) error

// endWorkOf converts a runtime work membership to the wire's.
func endWorkOf(work ActiveWork) acpwire.EndWork

// runtime.go
func (rt *Runtime) NewSession(name string) (*session.State, error)
func (rt *Runtime) newSessionLocked(name string, replacing bool) (*session.State, error)
```

**`internal/app/tui`.**

```go
// model.go (Task 3): the end types and the controller go. endIntent.kind holds
// acpwire.EndIntentExit or acpwire.EndIntentReplace, and the model's
// endSnapshot is an acpwire.EndWork.
func endWorkItems(snap acpwire.EndWork) []activeworkpanel.Item
func snapEmpty(snap acpwire.EndWork) bool

// model.go (Task 4)

// LegacySession is the runtime's current state and tool registry, which the
// model re-reads after Host.NewSession for the groups not yet behind the seam.
// Plan 9 deletes it with WithLegacyState.
type LegacySession struct {
	State        *session.State
	ToolRegistry *registry.Registry
}

// WithLegacySession wires the read of the runtime's current state and tool
// registry that follows Host.NewSession. Without it, a new session leaves the
// model's legacy state as it was.
func WithLegacySession(read func() LegacySession) Option

// The model gains the field legacySession func() LegacySession (Task 4).
```

**Test helpers.**

```go
// tui: model_test.go (Task 3). endTestController implements
// inproc.EndService over the wire types: work is an acpwire.EndWork, accepted
// an []acpwire.EndAcceptParams, and acceptFn a
// func(acpwire.EndAcceptParams) (acpwire.EndDecision, error). Its EndAccepted
// method and its unused activeErr field go. workSnapshot returns an
// acpwire.EndWork, and exitWithWork takes an inproc.EndService.

// tui: ending_seam_test.go (new, Task 3)

// testModelEnding builds a model whose session's end service is end.
func testModelEnding(state *session.State, end inproc.EndService, opts ...Option) Model

// withEndService replaces m's session with one over m's state whose end
// service is end, as withPromptRunner does for the runner.
func withEndService(m Model, end inproc.EndService) Model

// tui: newsession_seam_test.go (new, Task 4)

// fakeNewSession is a Deps.NewSession that records each call and returns
// newState, or err.
type fakeNewSession struct {
	newState *session.State
	called   int
	lastName string
	err      error
}

func (f *fakeNewSession) newSession(name string) (*session.State, error)

// legacy is a WithLegacySession read that returns f.newState.
func (f *fakeNewSession) legacy() LegacySession

// testModelReplacing builds a sized model over oldState whose host creates
// sessions through f and whose end service is end, with the legacy read wired.
func testModelReplacing(t *testing.T, oldState *session.State, f *fakeNewSession, end inproc.EndService, opts ...Option) Model
```

## Task 1: The session takes the end calls

**Goal:** the adapter serves the five end calls and `Close`, and the host creates sessions. Nothing calls them yet.

**Files:** `internal/acpwire/ending.go` (new), `ending_test.go` (new); `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `turns.go`, `ending.go` (new), `ending_test.go` (new); `internal/app/tui/seam_test.go`.

**Steps:**

1. **`acpwire`.** Add `ending.go` (Contracts).
2. **`acpclient.go`.** Add `NewSession` to `Host` and the six methods at the end of `Session`, with their Contracts doc comments. **`methods.go`:** add the Contracts' rows.
3. **`inproc/inproc.go`.** `Deps` and `Session` gain the Contracts' fields. **`inproc/events.go`:** `newSession` copies `Deps.End`.
4. **`inproc/ending.go`** (new) holds `EndService`, the errors, the six `Session` methods and `Host.NewSession`. Every `Session` method except `ActiveWork` and `Close` first returns `errSessionClosed` on a closed session.
   - **`ActiveWork`**: an empty `EndWork` on a closed session or without `s.end`; otherwise what `s.end.ActiveWork` returns. A closed session's work settled before it closed, so empty is its final value.
   - **`AcceptEnd`**: without `s.end`, return `EndDecision{Accepted: true}` and no error; otherwise forward the params unchanged and return what the service returns.
   - **`WaitForEnd`**: nil without `s.end`; otherwise the service's result.
   - **`RunExitAnalysis`**: `errNoExitAnalysis` without `s.end`; otherwise the service's result.
   - **`Postmortem`**: build the report with `postmortem.Build` over `s.state` and its database, find the home directory with `os.UserHomeDir`, and write the report with `postmortem.Write` under `postmortem.ProjectSlug` of the state's working directory and the session's ID, returning the path. Return each step's error as `writePostmortemFor` (`model.go:5095`) does, including "locate home directory: …" for an empty home.
   - **`Close`**: under `s.mu`, return nil if the session is already closed. Otherwise mark it closed, drop the paused plan run, take the slot's run and mark it stopped, as `Stop` does with `slot.stopped`. After releasing `s.mu`, in this order: cancel that run (`slot.cancel`), call the state's `ResolvePendingForShutdown` and `Shutdown`, cancel the subscriptions' context and close the change signal as `close` does, and return nil. `closeSignal` takes `s.mu` itself, so it must not run under the lock. `Close` stops no command group: the accepted end's settlement already did.
   - **`Host.NewSession`**: return `errNoNewSession` without `Deps.NewSession`. Call it with the name outside `h.mu`. On an error, return it and leave the current session open. Otherwise return `Swap` over the new state.
5. **`inproc/turns.go`.** In `runTurn`, where the slot is taken under `s.mu`, refuse a closed session first: release the lock, cancel, and return `errSessionClosed`. This closes the window between `Prompt`'s first check and the slot (decision 5).
6. **`tui/seam_test.go`.** `fakeHost` gains `NewSession`, which returns a nil session and the error "fakeHost creates no sessions", so it still satisfies `acpclient.Host`.
7. **Tests:**
   - **`acpwire/ending_test.go`** (new): **`TestEndingJSON`** encodes the six values and compares the bytes with the Contracts table, in the table style of `TestPipelinesJSON`;
   - **`inproc/ending_test.go`** (new). Each test builds its state with `newState(t, "s1")`. A test fake, `fakeEndService`, returns a set `EndWork` and decision, records the params, goals and wait calls it receives, and returns set errors:
     - **`TestEndCallsForwardToTheEndService`**. With the fake holding one item, `ActiveWork` returns it. `AcceptEnd` with `{Intent: "exit", DisplayedIDs: ["a"], RequireDisplayed: true}` passes the params unchanged and returns the fake's decision. `WaitForEnd` returns the fake's wait error "join failed", and `RunExitAnalysis(ctx, "goal")` records "goal" and returns nil;
     - **`TestASessionWithoutAnEndServiceEndsAtOnce`**. `ActiveWork` is empty, `AcceptEnd` returns `Accepted` with no items and no error, `WaitForEnd` returns nil, and `RunExitAnalysis` returns an error with `CodeNoRunner`;
     - **`TestPostmortemWritesTheReport`**. With `HOME` set to a temp directory, `Postmortem` returns a path under `postmortem.Dir(home, postmortem.ProjectSlug(state.WorkingDir))` whose file exists and decodes as a report;
     - **`TestCloseShutsTheSessionDown`**. With a pending approval set on the state, `Close` returns nil. The approval's channel receives a denial, `state.Done()` is closed, `Changed()` is closed, `ID()` still answers "s1", `Stop` returns an error with `CodeSessionClosed`, and a second `Close` returns nil;
     - **`TestCloseCancelsTheRunInFlight`**. A runner that blocks until its context is done: `Close` while it runs makes `Prompt` return `StopReasonCancelled` and no error;
     - **`TestNewSessionReplacesTheCurrentSession`**. `Deps.NewSession` records its name and returns a state "s2". `NewSession(ctx, "fix-auth")` returns a session whose `ID()` is "s2", the name reached `Deps.NewSession`, and the old session's `Stop` returns `CodeSessionClosed`;
     - **`TestAFailedNewSessionKeepsTheCurrentOne`**. `Deps.NewSession` fails with "boom": `NewSession` returns an error containing "boom", and the old session's `Stop` returns nil. Without `Deps.NewSession`, it returns `errNoNewSession`;
     - **`TestAClosedSessionEndsNothing`**. After `host.Swap`, the old session's `AcceptEnd`, `WaitForEnd`, `RunExitAnalysis` and `Postmortem` return `CodeSessionClosed`, its `ActiveWork` is empty, and the fake recorded nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/acpclient/inproc/ -run 'End|Postmortem|Close|NewSession' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/tui/...
go vet ./internal/acpwire/ ./internal/acpclient/...
```

Expected: every listed test passes, `TestAdapterDoesNotImportApp` and `TestClientImportsStayOnTheWireSide` included. T4 passes on both sides: `session/close` and `session/new` are registered, and the five new names are not. `tui` passes unchanged, because its test sessions embed `acpclient.Session` and `fakeHost` gained its method.

## Task 2: The runtime serves the end calls and new sessions

**Goal:** `bindTurnDeps` binds the runtime's end service and new-session call, `rt.NewSession` returns only the state, `Run`'s swapper goes through the host, and the exit analysis reads the runner under the lock. The TUI still uses its `EndController` and swapper until Tasks 3 and 4.

**Files:** `internal/app/ending.go` (new), `ending_test.go` (new), `turns.go`, `turns_test.go`, `runtime.go`, `runtime_test.go`, `runtime_host_test.go`, `app.go`, `app_test.go`; `internal/acpclient/inproc/` (every file that calls `Swap`).

**Steps:**

1. **`ending.go`** (new) holds `endService` and `endWorkOf` (Contracts).
   - **`ActiveWork`** returns `endWorkOf(e.rt.ActiveWork())`;
   - **`AcceptEnd`** calls `e.rt.AcceptEnd` with the intent as an `EndIntent`, the displayed IDs and the flag, and returns the decision's `Accepted`, `Expanded` and `endWorkOf` of its work, or the error;
   - **`WaitForEnd`** and **`RunExitAnalysis`** return the runtime's;
   - **`endWorkOf`** copies each item's ID, label and group, in order.
2. **`runtime.go`.** `NewSession` and `newSessionLocked` return `(*session.State, error)`. The body is unchanged: it still builds the runner, the orchestrator, the factories and the registry, and installs them on the runtime; only the return values shrink (decision 8). Update every caller: `runtime_test.go` (seven), `runtime_host_test.go` (one) and `app_test.go` (three).
3. **`turns.go`.** `bindTurnDeps` also sets `d.End` to an `endService` over `rt` and `d.NewSession` to `rt.NewSession`.
4. **`app.go`.**
   - **The swapper** (`app.go:2871`) calls `seamHost.NewSession` with `Run`'s context and the name, returns its error, and otherwise returns `State` and `ToolRegistry` read from `rt` under `rt.mu`, with the session `NewSession` returned. Its comment says the host creates the session through `Deps.NewSession`.
   - **The exit analysis operation** (`app.go:2934`) reads the runner with `rt.TurnRunner()` and returns the same "exit analysis: no agent runner" error when it is nil (decision 11).
5. **`inproc`.** Rename `Host.Swap` to `swap`, and update its callers in the package's tests (`h.Swap(` and `host.Swap(` become `h.swap(` and `host.swap(`). `Host.NewSession` calls `swap`.
6. **Tests:**
   - **`ending_test.go`** (new): **`TestEndServiceRoundTrips`** runs `TestTUIEndControllerAdapterRoundTrips`' flow over an `endService` on `startEndRuntime`: an empty membership on a fresh runtime; after `rt.BeginWork`, one item with an ID and a label; an acceptance requiring a displayed set that names another ID is refused with that item's ID in `Expanded`, and `rt.EndAccepted()` stays false; the acceptance of the shown ID is accepted with one item, and `rt.EndAccepted()` is true;
   - **`turns_test.go`**: **`TestBindTurnDepsServesTheRuntime`** also checks that `d.End` is an `*endService` over `rt` and that `d.NewSession` is set;
   - **`app_test.go`**: **`TestAdapterAccessFollowsNewSession`** sets `NewSession: rt.NewSession` in its `Deps` and replaces its `rt.NewSession` and `host.Swap` calls with one `host.NewSession(ctx, "")`, taking the new state from `rt.State`. Its assertions are unchanged.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn '\.Swap(' internal/ --include='*.go' | grep -v 'atomic\|CompareAndSwap'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/ -run 'TestEndServiceRoundTrips|TestBindTurnDeps|TestAdapterAccessFollowsNewSession|NewSession|Exit|End' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/ ./internal/acpclient/... ./internal/app/tui/...
go vet ./internal/app/ ./internal/acpclient/...
```

Expected: the `grep` prints nothing. Every listed test passes, and `app`, `acpclient` and `tui` pass.

## Task 3: Ending goes through the session

**Goal:** the TUI's warning, acceptance, settlement, exit analysis and postmortem go through the session, and quitting closes it. `EndController` and its mirror types are deleted.

**Files:** `internal/app/tui/model.go`, `commands_dispatch.go`, `moved_reads_test.go`, `boundary_allowlist_test.go`, `model_test.go`, `ending_seam_test.go` (new), `frame_invariants_test.go`, `frame_geometry_test.go`, `keys_cancel_test.go`, `system_access_test.go`, `commands_dispatch_test.go`; `internal/app/app.go`, `app_test.go`.

**Steps:**

1. **`model.go`, types.** Delete `EndIntentExit`, `EndIntentReplace`, `EndWorkItem`, `EndWorkSnapshot`, `EndAcceptRequest`, `EndDecision`, `EndController`, `WithEndController` and the `endController` field (`model.go:96` to `169`, `301` to `304`). `endIntent.kind` holds `acpwire.EndIntentExit` or `acpwire.EndIntentReplace`: update the uses at `model.go:4720`, `4758`, `4780`, `4917` and `6754`, and `commands_dispatch.go:179`, `242` and `245`. `endSnapshot`, `endWorkItems` and `snapEmpty` take `acpwire.EndWork`.
2. **`model.go`, the flow** (decision 3):
   - **`beginEnd`** reads `m.sess.ActiveWork()`;
   - **`acceptEnd`** drops its no-controller branch and calls `m.sess.AcceptEnd` with `m.ctx` and the params it built;
   - **`settleEnd`** captures `m.sess` and calls its `WaitForEnd` under the same timeout, with no nil branch;
   - **`refreshEndWarning`** always reads `m.sess.ActiveWork()`;
   - **`handlePostmortemDone`** requests the analysis for `ResultWithAgent` unconditionally;
   - **`runExitAnalysis`** captures `m.sess` and calls its `RunExitAnalysis`;
   - **`handlePostmortemWritten`** runs the analysis whenever the message asks for it.
3. **`model.go`, the postmortem** (decision 4). `extractPostmortemCmd` captures `m.sess` and `m.ctx`, and its command calls `Postmortem`. Delete `writePostmortemFor` and the `postmortem` import, and drop any import left unused.
4. **`model.go`, `finishQuit`** (decision 5). Call `m.sess.Close(m.ctx)` where it called `m.state.ResolvePendingForShutdown()`, ignoring the error, and delete `m.state.Shutdown()`. The local cancels stay first, and the dock releases stay after. Rewrite its comment: `Close` settles pending requests and shuts the session down; the work was stopped by the settlement.
5. **`app.go`.** Delete `tuiEndController`, `tuiEndWorkSnapshot` and the `WithEndController` wiring (`app.go:2506` to `2565`, `2941` to `2944`). The exit analysis operation stays.
6. **T1b.** Add `ResolvePendingForShutdown` and `Shutdown` to `movedStateReads` at plan `5e`, with a comment: quitting closes the session since 5e. **Allowlist:** `model.go` loses `marshal/internal/postmortem`.
7. **Tests.** Build every end test's session over an end service:
   - **`ending_seam_test.go`** (new) holds `testModelEnding` and `withEndService` (Contracts);
   - **`endTestController`** moves to the wire types (Contracts), and `workSnapshot` and `frame_invariants_test.go`'s `longWorkSnapshot` return `acpwire.EndWork`. Every `testModel(state, WithEndController(ctrl), …)` becomes `testModelEnding(state, ctrl, …)`, and every `m.endController = ctrl` becomes `m = withEndService(m, ctrl)`. The mirror types and intent constants in the tests become `acpwire`'s;
   - **`newSessionWarningModel`** builds its model with `testModelWith(inproc.Deps{State: oldState, End: ctrl}, …)` and keeps its swapper until Task 4;
   - **a test that quits a model without an end service** now gets the quit through the settlement command: drive it with `runEndCmd`. `TestCtrlCCancelsTurn` is one: its checks run after the end command chain;
   - **`app_test.go`**: delete `TestTUIEndControllerAdapterRoundTrips` and `TestTUIEndControllerIsTheOnlyEndSeam`. `TestEndServiceRoundTrips` and `TestBindTurnDepsServesTheRuntime` cover the binding, and `Run` binds through `bindTurnDeps`.
   - New tests in `ending_seam_test.go`:
     - **`TestQuitClosesTheSession`**. A model over a state with a pending approval and an end service with no work: `/exit` through `runEndCmd` ends in `tea.Quit`. The approval's channel receives a denial, `state.Done()` is closed, and `m.sess.Stop` returns `CodeSessionClosed`;
     - **`TestAnExitWithoutAnEndServiceStillQuits`**. A model with no end service and `postmortem.on_exit = "never"`: `beginEnd` for an exit returns the settlement command, and `runEndCmd` ends in `tea.Quit`;
     - **`TestTheExitPostmortemIsWrittenThroughTheSession`**. With `HOME` set to a temp directory, `on_exit = "always"` and one user message, the exit writes a report under that home and adds "Postmortem written to " and its path, then quits.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'EndController\|EndWorkSnapshot\|writePostmortemFor\|tuiEndController' internal/ --include='*.go'
grep -n 'm.state.ResolvePendingForShutdown\|m.state.Shutdown' internal/app/tui/*.go
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/tui/ -run 'End|Exit|Quit|Postmortem|Warning|CtrlC|Resume|NewSession|Analysis|Frame' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/...
```

Expected: both greps print nothing. The runtime keeps its own `EndAcceptRequest` and `EndDecision` in `internal/app`, so they are not grepped; the TUI's copies are deleted, and the build fails on any use left. Every listed test passes, and `tui` and `app` pass, T1, T1b and the goldens included.

## Task 4: New sessions come from the host

**Goal:** `/new` and `/clear` get their session from `Host.NewSession`, the model re-reads its legacy values through `WithLegacySession`, and a replacement leaves the end idle. The swapper is deleted.

**Files:** `internal/app/tui/model.go`, `commands_dispatch.go`, `model_test.go`, `seam_test.go`, `newsession_seam_test.go` (new); `internal/app/app.go`.

**Steps:**

1. **`model.go`.** Delete `SessionSwapResult`, `SessionSwapper`, `SessionSwapperFunc`, `WithSessionSwapper` and the `sessionSwapper` field (`model.go:75` to `94`, `300`, `734` to `739`). Add `LegacySession`, `WithLegacySession` and the `legacySession` field (Contracts).
2. **`commands_dispatch.go`.**
   - **`newSessionEffect`** keeps its busy refusal and drops the "not available" branch (decision 6).
   - **`startNewSession`** reads the cleared-message count from the state as before, then calls `m.host.NewSession` with `m.ctx` and the name. Whatever the outcome, it sets the end stage to idle first (decision 10). On an error it writes the same "Failed to start new session: …" note and returns. On success it sets `m.sess` and resets `watchResumeSeq`. When `legacySession` is set it reads it, takes the state when it is not nil, and takes the registry when it is not nil. The rest is unchanged, except that the change pump is always restarted, because the session is always new.
3. **`app.go`.** Replace the `WithSessionSwapper` block with `WithLegacySession`, bound to a read of `rt.State` and `rt.ToolRegistry` under `rt.mu`. Keep the comment that it is wired outside the provider block, so `/new` and `/clear` work when the first provider build failed.
4. **Tests:**
   - **`newsession_seam_test.go`** (new) holds `fakeNewSession` and `testModelReplacing` (Contracts). `testModelReplacing` builds `Deps{State: oldState, NewSession: f.newSession, End: end}` and wires `WithLegacySession(f.legacy)` and `setupCmdReg`;
   - **`model_test.go`**: `fakeSessionSwapper` goes. Its five uses at `model_test.go:2773` to `2904`, the one at `10349` and `newSessionWarningModel` move to `fakeNewSession` and `testModelReplacing`. `swapper.called` becomes `f.called > 0`, and `swapper.lastName` becomes `f.lastName`;
   - **`seam_test.go`**: `TestNewSessionSwapsTheClientSession` moves to `testModelReplacing`. Delete `stateOnlySwapper` and `TestNewSessionKeepsTheSeamSessionWhenTheSwapperOmitsIt`: the host always returns a session;
   - New tests in `newsession_seam_test.go`, each over `testModelReplacing` with an `endTestController` holding no work:
     - **`TestANewSessionLeavesTheEndIdle`**. After `/new` through `runEndCmd`, `m.endStage` is `endIdle`. A prompt then starts on the new session with a `withPromptRunner` runner, which receives the text, and no note says "The conversation is ending";
     - **`TestASecondNewSessionStartsAnother`**. Two `/new` commands, each through `runEndCmd`, call `NewSession` twice, and neither ends in `tea.Quit`;
     - **`TestExitAfterANewSessionStillWarns`**. After `/new`, with the end service holding one item, `/exit` opens the active-work warning and accepts nothing;
     - **`TestResumeAfterANewSessionRecordsTheTarget`**. After `/new`, `beginResume("sess-x")` through `runEndCmd` ends in `tea.Quit`, and `m.ResumeSession()` is "sess-x";
     - **`TestAFailedNewSessionCanBeRetried`**. With `f.err` set to "boom", `/new` writes "Failed to start new session: boom", keeps the old session's ID, and leaves the end idle. With `f.err` cleared, a second `/new` swaps in the new state.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'SessionSwapper\|SessionSwapResult\|WithSessionSwapper\|fakeSessionSwapper' internal/ --include='*.go'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/tui/ -run 'NewSession|Replac|Resume|Clear|End|Swap' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5e-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/...
```

Expected: the `grep` prints nothing. Every listed test passes, and `tui` and `app` pass, T1, T1b and the goldens included.

## Task 5: Verification and docs

**Goal:** the full suite is green, and the spec and the design record plan 5e.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 5e entry to the Implementation record, after plan 5d's. It lists:
   - the `acpwire` types;
   - the six `Session` methods and `Host.NewSession` with their `Methods` rows: two on existing ACP methods, five new;
   - `EndService`, the adapter's behaviour without one, `Close`, and `Host.NewSession` over `Deps.NewSession`;
   - the runtime's `endService` and new-session call in `Deps` through `bindTurnDeps`, `rt.NewSession`'s narrower result, and the exit analysis's locked read;
   - that the TUI ends and swaps through the seam, and which types, options and fields are gone, with `WithLegacySession` added;
   - the fix (decision 10);
   - T1b's two names and the allowlist's one entry.
2. **`design.md` §7.** The P2 row's plan text becomes "nine plans, with 2, 3 and 5 split into sub-plans; plans 1, 2a–2c, 3a–3d, 4 and 5a–5e implemented".
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-5e-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-5e-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `bf211f49`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** At `bf211f49`, all 117 packages passed. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository and check by eye:
- a prompt, then `/new`: "Started new conversation. Cleared N messages." appears, and a second prompt runs;
- `/new` again, then `/clear`: each starts a new conversation, and Marshal keeps running;
- start a long shell command, then `/exit`: the active-work warning lists it. Esc keeps it running; `/exit` again and Enter stops it, and the postmortem question follows (with `postmortem.on_exit` unset);
- answer the postmortem question with agent analysis: "Postmortem written to …" appears, the analysis runs, and Marshal exits;
- after `/new`, `/resume` and pick an earlier session: "Resuming session …" appears and Marshal reopens on it.

## Integration notes

- **Plan 6** moves the end flow's notes: "Kept open. Active work continues.", "Could not stop all work: …", "Cannot end the conversation: …", the three postmortem notes, "Resuming session …", "Started new conversation …" and "Failed to start new session: …".
- **Plan 7** moves the exit policy's read of `postmortem.on_exit`, and the roster's tool registry, which `WithLegacySession` then stops carrying.
- **Plan 8** moves `sessionHasUserTurn`, the cleared-message count and the session picker's `ListSessions`.
- **Plan 9** deletes `WithLegacySession` with `WithLegacyState`.
- **A follow-up, not part of P2:** a session started without a provider never serves work. `Run` sets `Deps.Work` only when the provider was ready (`app.go:2812`), and nothing sets it later, so after a `/connect` recovery the tray's work cards and the Tasks panel's Work tab stay off, even after `/new`. A throwaway test confirmed that a first-run session's `Work()` reports false. Binding `Deps.Work` always is not enough on its own: a reload rebinds only the watch manager into the work view (`app.go:3240`), so the view would miss jobs after a recovery until `/new`.
- **P3:**
  - the client serves `ActiveWork` from a mirror refreshed by an `end_work` update, and adds `session/end_accept`, `session/end_wait`, `session/exit_analysis` and `session/postmortem`;
  - `Close` sends `session/close`, which also tears the runtime down, and `Host.NewSession` sends `session/new`;
  - `Host.NewSession` and the postmortem become asynchronous: the swap and the report land when their results arrive;
  - it decides whether resume becomes in place over `session/resume` (decision 9).

## Self-review

- **Every task verifiable alone.** Task 1 adds calls nothing uses. Task 2 binds the runtime's side and moves `Run`'s swapper onto the host, while the TUI keeps its controller and swapper. Task 3 moves ending and Task 4 swapping. Each keeps T1, T1b and the goldens green.
- **Cold executor.** Each step names the function, what it calls after the change, and what it does with the result. The no-end-service behaviour, `Close`'s order and the swap's idle reset are spelled out. Each new test names its setup and the behaviour it pins.
- **Contracts.** The wire types and their encodings, the seven interface methods and their rows, `Deps`' fields, `EndService` and the adapter's errors, the runtime's end service and narrowed `NewSession`, the TUI's legacy read, and the test helpers are declared there.
- **Anchors.** Every path, symbol and line was checked at `bf211f49`.
- **Spec.** C4, C8 and the Migration order are updated, with the revision record "2026-11-03, against `bf211f49` (plan 5d merged)" holding decisions 1 to 3, 5, 6, 9, 10, 12 and 14.
- **No placeholders.**
