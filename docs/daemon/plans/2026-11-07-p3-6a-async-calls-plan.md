# P3 plan 6a: The asynchronous calls — implementation plan

Date: 2026-11-07. Status: ready for review.
Base: `main` at `1d34fb9a` (P2 plan 9 and P3 plan 3a merged). Every anchor below was checked there. The full suite passed there: 118 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-next-suite`, `-timeout 20m`).
- **It runs alongside P3 plan 2** ([layering](2026-11-07-p3-2-layering-plan.md)). This plan changes only `internal/app/tui` and its subpackages, which plan 2 does not touch. The two plans share only `docs/daemon/p3-acp-transport.md`, where each adds its own Implementation record entry.
- **The spec edits are already made**, uncommitted in the main checkout: R6 (the inventory's list, the queue, and the settings browser's check) and Migration order item 6's split into 6a and 6b. Task 6 adds only the Implementation record.
- It runs over `inproc`, in process. A converted call still runs on a goroutine there, so the conversions and their ordering are proved before anything crosses a wire.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R6 (calls on the `Update` goroutine), R10 (the synchronous-call budget), Migration order item 6a, and test V2.
Previous plan: [P3 plan 3a, ordering](2026-11-06-p3-3a-ordering-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** no call that can block runs on the TUI's `Update` goroutine, and the converted calls keep the order they had when they blocked.
- The model gets a call queue. A converted call starts from the queue, on the `Update` goroutine, reading the model as it is then. It runs off the goroutine, and its result is handled back on it. Calls run one at a time, in the order they were made.
- `Command`, `Host.NewSession`, `PlanPreflight`, `RefreshSheet` on opening the sheet, `ReloadConfig` at its five call sites and `ReconnectMCP` move onto the queue. The settings browser's `CanProbeModel` becomes the browser's own command.
- A test keeps every call R6 names off the `Update` goroutine.

**Non-goals:**
- **No wire.** Every call still goes through `inproc`. Plan 8 switches the TUI onto the pipe.
- **No other call becomes asynchronous.** The inventory (below) found every other call in `Update` cheap: in-memory state, a small database read or write, or a small file write. R6 keeps those synchronous.
- **No new indicator.** Nothing new is drawn while a call runs. The notes each call writes are the ones it writes today, written when its result arrives.
- **No change outside `internal/app/tui`,** apart from the spec. In particular, no `internal/app` test changes: plan 2 moves `app`'s `Run` tests, and none of them drives a call this plan converts.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-6a-suite`. Create the directory first: `mkdir -p /private/tmp/p3-6a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 2 uses `/private/tmp/p3-2-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 6 likelier. Rerun a failure alone before treating it as new.
- **The inventory** (measured at `1d34fb9a` with a `go/ast` scan of every non-test file under `internal/app/tui` for calls of the 110 seam method names, then read by hand). Calls already inside a `func() tea.Msg` literal or a goroutine run off `Update`. Of those made directly on it, these block:
  - `m.sess.Command` in `dispatchCommand` (`model.go:6086`): a command's handler may do anything, a provider call or git included;
  - `m.host.NewSession` in `startNewSession` (`commands_dispatch.go:183`): it builds a runtime session;
  - `m.sess.PlanPreflight` in the `sdd` effect (`commands_dispatch.go:412`): it inspects the plan and resolves its verify gate;
  - `m.sess.RefreshSheet` in `openSessionSheet` (`model.go:1274`): it runs git (`changedfiles.Read`). Its other site (`refreshSheetCmd`, `pump.go:30`) is already a command;
  - `m.sess.ReloadConfig` at five sites: `persistAndReload` (`model.go:744`), the `/set` user-config path in `handleSetCommand` (`model.go:844`), the saved branch of `settings.ChangedMsg` (`model.go:1689`), `savePresetsAndReload` (`model_options.go:136`) and `flushPendingModelOptions` (`model_options.go:167`). It rebuilds the runtime (`reloadAgentRuntime`, `internal/app/app.go:2957`), which starts MCP servers and builds providers;
  - `m.sess.ReconnectMCP` in `reconnectMCPServer` (`mcp.go:55`): the same rebuild;
  - `b.reg.st.host.CanProbeModel` in `startConfirm` (`settings/browser.go:523`): it builds a provider client.

  `Postmortem`, which P2 named, is already a command (`extractPostmortemCmd`, `model.go:4745`). So are the probes, the sign-ins, the plugin scan and install, the skill install and remove, the run outcome's diff, `Callers`, the row detail, the runs (`startSessionRun`, `model.go:3878`) and the end flow's waits. Every other direct call reads or writes in-memory state, a small database row or a small file: `Rollback` restores file backups (`session/backups.go:56`), `Intelligence` reads config and the database, and `PrepareProviderLogin` and `PrepareMCPLogin` only resolve an engine and its token store.
- **Who calls the converted paths:**
  - `persistAndReload` (`model.go:734`): `handleSetCommand` (`:832` and `:864`), the unsaved branch of `settings.ChangedMsg` (`:1724`), `applyConnectDone` (`:6787`), `switchModelPreset` (`:6867`) and `submitDoctorSecret` (`doctor_secret.go:136`). It returns `(saveErr, reloadErr error)`, and each caller reports.
  - `savePresetsAndReload` (`model_options.go:126`): `handleModelOptionsChanged` (`:99`).
  - `flushPendingModelOptions` (`model_options.go:157`): `handleAgentFinished` (`model.go:4889`) and `:5063`, each batching the command it returns.
  - `reconnectMCPServer` (`mcp.go:53`): `:106`.
  - `openSessionSheet` (`model.go:1269`): Ctrl+B (`keypress.go:178`) and `model.go:2213`.
  - `startNewSession` (`commands_dispatch.go:175`): the end flow, after an accepted replacement (`model.go:6353`).
- **`dispatchCommand`'s handler branch** (`model.go:6085`) calls `Command`, then opens the result's doc or notes its text, then hands an agent goal to a turn (steering when busy). Then, if the command is also TUI-only (`:6113`), its effect runs. Only `/trust` has both a handler and a TUI effect today, and its effect relies on the handler's text having been shown first. A handler error is noted and nothing else runs.
- **The Enter path** (`keypress.go:371` to `:393`): a `/` line goes to `dispatchCommand`; otherwise a busy turn steers, a session with no agent echoes the line as a note, and anything else starts a prompt.
- **`Update` already batches work after routing.** After every routed message, `Update` calls `detailQueryCmds` on the updated model and batches its command with the handler's (`model.go:1488` to `:1498` for the composer path, and `:1503` to `:1513` for the ordinary one). The queue is served in the same two places.
- **The end of a quit** is `finishQuit` (`model.go:4681`): it cancels the agent and LSP contexts, closes the session, releases the dock's surfaces and returns `tea.Quit`.
- **The test helpers.** `runCmdOnce` (`pump_test.go:88`) runs a command with a timeout. `drainCmds` (`model_test.go:65`) and `runCmdChain` (`model_test.go:1746`) run commands and the messages they produce.

**Decisions this plan makes beyond the spec** (R6 records them):

1. **One queue, run in order.** Today a blocking call freezes `Update`, and Bubble Tea delivers the keys typed meanwhile afterwards. The queue keeps that order without the freeze: converted calls run one at a time, in the order made, and a call made after another sees the model the first one left.
2. **A call starts from the queue, never inside the handler that made it.** The handler enqueues it, and `Update` starts the queue's head after routing, beside `detailQueryCmds`. Functions that return nothing today (`handleSetCommand`, `reconnectMCPServer`, `applyConnectDone`, `switchModelPreset`) can enqueue without changing their signatures.
3. **A call reads the model when it starts, not when it is made.** So a command entered after `/new` runs on the new session, as it does today.
4. **A prompt submitted while the queue holds anything waits behind it.** It is a step that runs, when it reaches the head, exactly what Enter would have done at that moment: steer when busy, echo when no agent, start a prompt otherwise. A step makes no call, so the queue moves on at once. A slash command still dispatches at once. Only the calls it makes queue.
5. **Each call keeps its notes and its effects, moved into its result.** Nothing is reported before the result arrives, except a failed save, which happens before the reload and is reported at once, as today.
6. **The sheet opens when its refresh returns**, so its first frame stays fresh, as the comment on `openSessionSheet` requires. A second Ctrl+B while that open is queued cancels it, as a press after the open would have closed the sheet.
7. **A quit waits for the call in flight.** `finishQuit` cancels the in-flight call's context, drops the queued steps, and finishes when the in-flight result arrives. A call that ignores its context is waited for, as a frozen `Update` waited for it today.
8. **`CanProbeModel` stays inside the settings browser.** The check becomes the browser's own command, as its capability probe already is, and the browser's existing request ID drops a stale answer. It does not join the model's queue: it orders nothing else.
9. **The guard is a syntax check.** It finds calls of the R6 names whose receiver ends in `sess` or `host`, and allows them only inside a `func() tea.Msg` literal, a function literal returning the queue's result type, or a `go` statement. That excludes `exec.Command`, and it is what the inventory's scan checked.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 2's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 2 and 6a`. Plan 2 gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-6a-async-calls` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-6a-async-calls: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- **Tests that break because a call now finishes later** are expected in Tasks 2 to 5. Fix each by settling the model with `settleCalls` after the step that made the call, then asserting as before. Do not weaken an assertion. A test that asserted something happened before `Update` returned now asserts it after settling, and the commit message of that task lists how many tests changed this way.

## Contracts

### The call queue (`internal/app/tui/asynccalls.go`, new)

```go
// callResult handles a converted call's result on the Update goroutine
// (P3 R6). It may return a command, as an Update handler does.
type callResult func(m *Model) tea.Cmd

// callRun runs a converted call off the Update goroutine and returns the
// handling of its result.
type callRun func(ctx context.Context) callResult

// callDoneMsg carries the in-flight call's result back to Update.
type callDoneMsg struct{ /* unexported */ }

// enqueueCall appends a converted call. start runs on the Update goroutine
// when the call reaches the head of the queue, reading the model as it is
// then. It returns the run, or nil when there is nothing left to call, in
// which case the queue moves on.
func (m *Model) enqueueCall(start func(m *Model) callRun)

// enqueueStep appends a step that makes no call: it runs on the Update
// goroutine when it reaches the head, and the queue moves on at once.
func (m *Model) enqueueStep(step func(m *Model) tea.Cmd)

// callsIdle reports whether no call is in flight and nothing is queued.
func (m Model) callsIdle() bool

// serveCalls runs queued steps from the head until one starts a call or
// the queue is empty, and returns their commands batched. Update calls it
// after every routed message, beside detailQueryCmds.
func (m *Model) serveCalls() tea.Cmd
```

### The test helper (`internal/app/tui/asynccalls_test.go`, new)

```go
// settleCalls serves m's call queue until it is idle, running each
// command and feeding each message back through Update, and returns the
// model. It fails t if the queue is not idle within five seconds.
func settleCalls(t *testing.T, m Model) Model
```

### The guard (`internal/app/tui/asynccalls_test.go`)

```go
// blockingCalls are the seam methods R6 says must not run on the Update
// goroutine.
var blockingCalls = []string{
	"Command", "NewSession", "PlanPreflight", "RefreshSheet",
	"ReloadConfig", "ReconnectMCP", "CanProbeModel", "Postmortem",
}

// TestBlockingCallsLeaveTheUpdateGoroutine fails when a non-test file
// under internal/app/tui calls one of blockingCalls on a receiver ending
// in sess or host outside a func() tea.Msg literal, a function literal
// returning callResult, or a go statement.
func TestBlockingCallsLeaveTheUpdateGoroutine(t *testing.T)
```

### The settings browser (`internal/app/tui/settings/browser.go`)

```go
// canProbeMsg is the answer to startConfirm's CanProbeModel check.
type canProbeMsg struct {
	RequestID int
	Provider  string
	Model     string
	CanProbe  bool
}
```

## Task 1: The call queue

**Goal:** a tested call queue, served by `Update`, used by nothing yet.

**Files:** `internal/app/tui/asynccalls.go` (new), `internal/app/tui/asynccalls_test.go` (new), `internal/app/tui/model.go`.

**Steps:**

1. **The queue.** The model holds a slice of entries (a call's start function, or a step), whether a call is in flight, the in-flight call's cancel function, and whether a quit is waiting. `enqueueCall` and `enqueueStep` append and do nothing else.
2. **`serveCalls`.** While nothing is in flight and the queue is not empty, take the head:
   - a step runs, and its command is kept;
   - a call's start runs; a nil run is skipped; otherwise derive a context from `m.ctx` with a cancel, mark the call in flight, and keep a command that runs it under that context and returns a `callDoneMsg` carrying its result. Stop there.

   Return the kept commands batched.
3. **`Update` serves the queue.** In both places `Update` batches `detailQueryCmds` (the two `switch next := updated.(type)` blocks, `model.go:1488` and `:1503`), batch `serveCalls` too, on the same updated model, in both the `Model` and the `*Model` case.
4. **`update` handles `callDoneMsg`.** Clear the in-flight mark and its cancel, run the result on the model, and keep its command. If a quit is waiting, return the command batched with `finishQuit`'s, which now finds nothing in flight. The next queued call starts from `Update`'s epilogue.
5. **A quit waits.** At the top of `finishQuit`, when a call is in flight, cancel its context, drop every queued entry, record that a quit is waiting, and return no command. When nothing is in flight, drop the queued entries and go on as today.
6. **`settleCalls`.** Loop until `callsIdle`: run `serveCalls`, run each command it returns (as `runCmdOnce` does, with the remaining time as the bound), and feed each message back through `Update`, following any further commands as `runCmdChain` does. Fail `t` after five seconds.
7. **Tests** (`asynccalls_test.go`), over `testModel`:
   - `TestQueuedCallsRunInOrder`: two calls whose runs record their order and block until released. The second does not start while the first is in flight, and the results are handled in order.
   - `TestAQueuedCallReadsTheModelWhenItStarts`: a call queued behind another reads a model field that the first call's result changed, and sees the new value.
   - `TestAStepRunsAtTheHeadAndTheQueueMovesOn`: a step queued between two calls runs after the first result is handled and before the second call starts. The second call starts in the same `serveCalls`.
   - `TestAQuitWaitsForTheCallInFlight`: with one call in flight and one queued, `finishQuit` returns no command and cancels the in-flight call's context. The queued call never starts. When the in-flight result arrives, `Update` returns `tea.Quit`, and the session is closed.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/ -run 'Queued|AStep|AQuit' -race -count=1
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/... -count=1
```

Expected: the four tests pass under `-race`, and the whole TUI tree passes unchanged: nothing enqueues yet.

Mutation check: make `serveCalls` start the next call while one is in flight. `TestQueuedCallsRunInOrder` fails. Restore it.

## Task 2: Commands

**Goal:** a command's handler runs from the queue, and everything after it happens when its result arrives.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/keypress.go`, and the tests that break.

**Steps:**

1. **The handler branch** (`model.go:6085`) enqueues a call and returns. Its start reads the current session and runs `Command` with the name and arguments. Its result does exactly what the branch does today, in the same order:
   - an error is noted, and nothing else runs;
   - the doc opens, or the text is noted;
   - an agent goal steers when busy, or starts a prompt when the agent is ready, returning the prompt's command;
   - then, when the command is also TUI-only, its effect runs, as `:6113` runs it. The effect's model is the one the result handles.

   A command with no handler keeps today's path: its TUI effect runs at once.
2. **A prompt waits behind the queue** (`keypress.go:376` to `:393`). When the queue is not idle, the submitted line becomes a step that, when it runs, does what this branch does: steer when busy, echo when no agent, start a prompt otherwise. When the queue is idle, the branch runs at once, as today.
3. **Tests:**
   - `TestACommandsResultLandsWhenItArrives`: a registry command whose handler blocks until released. After dispatch, its text is not yet noted and `Update` keeps routing (a window resize renders). After release and `settleCalls`, the text is noted once.
   - `TestTrustShowsTheHandlerTextBeforeItsPanel`: `/trust` with no pending decision notes the handler's text, then the effect's note, in that order, after settling.
   - `TestAPromptWaitsForTheCommandBeforeIt`: a blocking command, then a prompt submitted while it is in flight. The prompt does not start until the command's result is handled, then starts.
   - Fix the tests that dispatch a command with a handler and assert its result, as Execution says.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/ -run 'Command|Trust|Prompt' -race -count=1
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/... -count=1
```

Expected: the new tests pass under `-race`, and the whole TUI tree passes.

Mutation check: run the TUI effect before the handler's result in step 1. `TestTrustShowsTheHandlerTextBeforeItsPanel` fails. Restore it.

## Task 3: The new session, the preflight and the sheet

**Goal:** `Host.NewSession`, `PlanPreflight` and the sheet's refresh on opening run from the queue.

**Files:** `internal/app/tui/commands_dispatch.go`, `internal/app/tui/model.go`, `internal/app/tui/keypress.go` (only if the Ctrl+B handler needs the pending flag), and the tests that break.

**Steps:**

1. **`startNewSession`** (`commands_dispatch.go:175`). It sets the end stage to idle, as today, then enqueues a call and returns no command. The start reads the old session's message count, as today, and runs `NewSession` on the current host with the name. The result does everything the function does today after the call: the failure note, or the swap and every reset that follows it, through the final note. A prompt submitted meanwhile waits behind the call (Task 2) and so starts on the new session.
2. **The preflight** (the `sdd` effect, `commands_dispatch.go:412`). The busy check stays where it is. Then enqueue a call: the start runs `PlanPreflight` on the current session with the plan path and strategy, and the result notes `Cannot start plan run: …` or opens the run preflight, as today.
3. **The sheet** (`openSessionSheet`, `model.go:1269`).
   - When the sheet is open, close it, as today.
   - When an open is already queued, cancel it: clear the pending flag, and return.
   - Otherwise set the pending flag and enqueue a call. The start runs `RefreshSheet` with `Changed`, `Stats` and `Fleet`, as today. The result does nothing if the flag was cleared. Otherwise it clears the flag and does what the function does today after the refresh: the empty note, or the panel, replacing the Tasks panel.
4. **Tests:**
   - `TestNewSessionSwapsWhenItsResultArrives`: with a `NewSession` that blocks until released, the model still holds the old session after the accepted replacement. A prompt submitted then starts on the new session after release and settling.
   - `TestThePreflightOpensWhenItArrives`: `/sdd <plan>` opens the cast list only after settling, and a rejected plan notes the error.
   - `TestTheSheetOpensWhenItsRefreshReturns`: with a refresh that blocks, Ctrl+B opens nothing until release. A second Ctrl+B while it is queued cancels the open.
   - Fix the tests that break, as Execution says. Most are in `newsession_seam_test.go`, the `/sdd` tests and `sessionsheet_wiring_test.go`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/ -run 'NewSession|Preflight|Sheet|SDD' -race -count=1
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/... -count=1
```

Expected: the new tests pass under `-race`, and the whole TUI tree passes.

Mutation check: make `startNewSession`'s start capture the session when enqueued instead of reading it when it starts, then queue a command behind it in `TestNewSessionSwapsWhenItsResultArrives`. The command runs on the old session, and the test fails. Restore it.

## Task 4: The reloads

**Goal:** every `ReloadConfig` and `ReconnectMCP` runs from the queue, and each site's handling of its outcome happens when the reload returns.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/model_options.go`, `internal/app/tui/mcp.go`, `internal/app/tui/doctor_secret.go`, and the tests that break.

**Steps:**

1. **`persistAndReload`** takes the caller's report instead of returning it: `report func(m *Model, saveErr, reloadErr error)`.
   - The save stays synchronous. A failed save does what it does today, then reports at once.
   - After a successful save, clear `configSavePending` and the settings registry, as today, then enqueue a call. The start runs `ReloadConfig` on the current session with the config. The result does what the function does today after the reload: on error, apply the config and report the error; otherwise run `afterRuntimeReload` when it reloaded, apply the config, refresh the diagnostics, and report success.
   - Each of its six callers passes its reporting, unchanged, as `report`.
2. **`savePresetsAndReload`** gets the same shape, and `handleModelOptionsChanged` passes its two notes as its report.
3. **The other three `ReloadConfig` sites** enqueue a call each, with their code after the reload moved, unchanged, into the result:
   - the saved branch of `settings.ChangedMsg` (`model.go:1689`);
   - the `/set` user-config path in `handleSetCommand` (`model.go:844`);
   - `flushPendingModelOptions` (`model_options.go:157`). The busy and pending checks stay before the enqueue. Its result keeps the retry flag and both notes. It returns no command now, and its two callers stop batching one.
4. **`reconnectMCPServer`** (`mcp.go:53`) enqueues its `ReconnectMCP` call the same way, with its notes moved into the result.
5. **Tests:**
   - `TestASetReportsWhenItsReloadReturns`: with a reload that blocks until released, `/set` saves at once and notes nothing until release. After settling, it notes success and the settings registry is rebuilt.
   - `TestAFailedSaveReportsAtOnce`: a save that fails reports before any reload is queued, and no reload runs.
   - `TestReloadsRunInTheOrderMade`: two `/set` commands in a row. Their reloads reach the session in order, and their notes appear in order.
   - `TestAQuitWaitsForAReloadInFlight`: a quit during a blocked reload returns `tea.Quit` only after the reload returns.
   - Fix the tests that break, as Execution says. Most are in `model_test.go`, `mcp_test.go`, `provider_error_test.go` and `model_options_test.go`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/ -run 'Set|Reload|MCP|ModelOption|Provider|Doctor|Connect' -race -count=1
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/... -count=1
```

Expected: the new tests pass under `-race`, and the whole TUI tree passes.

Mutation check: in `persistAndReload`'s result, skip `afterRuntimeReload`. A provider-notice test in `provider_error_test.go` fails once it settles. Restore it.

## Task 5: The probe check and the guard

**Goal:** the settings browser checks `CanProbeModel` off the `Update` goroutine, and a test keeps every R6 call off it.

**Files:** `internal/app/tui/settings/browser.go`, its tests, `internal/app/tui/asynccalls_test.go`.

**Steps:**

1. **`startConfirm`** (`settings/browser.go:510`) opens the confirm screen as today, then, when the browser has a host, returns a command that runs `CanProbeModel` and returns a `canProbeMsg` with the current `capProbeID`, the provider and the model. It no longer sets `detectingCaps`.
2. **The answer.** The browser handles `canProbeMsg` as `handleCapabilityProbed` handles a probe: a message whose request ID, provider or model no longer matches the confirm screen is dropped. Otherwise, when the model can be probed, set `detectingCaps` and start the probe exactly as `startConfirm` does today. When it cannot, do nothing: "detecting…" never appears.
3. **The guard.** Write `TestBlockingCallsLeaveTheUpdateGoroutine` as Contracts declares it, walking every non-test `.go` file under `internal/app/tui` with `go/parser`, as `TestTUIStaysOnTheClientSeam` walks the tree. For each call of a `blockingCalls` name whose receiver expression's last identifier is `sess` or `host`, report the file and line unless an enclosing function literal returns `tea.Msg` or `callResult`, or a `go` statement encloses it.
4. **Tests:** the browser's tests that assert `detectingCaps` right after `startConfirm` feed it the `canProbeMsg` first. Add `TestTheProbeCheckRunsOffTheUpdateGoroutine`: `startConfirm` with a host whose `CanProbeModel` records the goroutine it runs on and blocks returns at once, and "detecting…" appears only after the answer arrives. A stale answer, after Esc, is dropped.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/tui/...
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/settings/ -race -count=1
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/ -run 'TestBlockingCallsLeaveTheUpdateGoroutine' -count=1 -v
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/... -count=1
```

Expected: the settings package passes under `-race`; the guard passes and reports nothing; the whole TUI tree passes.

Mutation check: put back a direct `m.sess.ReloadConfig` call in `handleSetCommand`. The guard names its file and line. Restore it.

## Task 6: The docs and full verification

**Goal:** the spec records plan 6a, and the whole suite passes.

**Files:** `docs/daemon/p3-acp-transport.md` (the Implementation record only).

**Steps:**

1. **The Implementation record.** Add a "Plan 6a — the asynchronous calls" entry after plan 3a's. It lists the inventory's result (the seven calls converted, `Postmortem` already a command, and why the rest stay synchronous), the queue and its four rules (order, start from the epilogue, read at start, a waiting prompt), the quit, the sheet's pending open, the browser's check, the guard, and how many tests changed only to settle. Plan 2 adds its entry at the same place. If both branches have merged, keep both entries, in merge order.
2. **Main may have moved.** If plan 2 merged while this branch ran, merge `main` into the branch before the full verification. Expect a conflict only in the P3 spec's Implementation record. Keep both entries.
3. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-6a-suite
gofmt -l internal
go vet ./...
TMPDIR=/private/tmp/p3-6a-suite go test ./internal/app/tui/... -race -count=1
TMPDIR=/private/tmp/p3-6a-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- the TUI tree passes under `-race`;
- every package passes. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.
4. **A manual run.** Build `CGO_ENABLED=1 go build ./cmd/marshal` and start it in a scratch repository with an MCP server configured. Check, and report:
   - `/help` notes its text;
   - `/set` on a user setting notes its result, and the screen keeps redrawing while the reload runs;
   - Ctrl+B opens the sheet with fresh changed files;
   - `/new` swaps the session, and a prompt typed straight after it lands in the new conversation;
   - Ctrl+C during a `/set` reload exits once the reload ends.

## Self-review

- **Spec coverage.** R6's list: Tasks 2 to 5, one call each, and the guard keeps it. R6's order: Task 1's queue and its tests. R10: unchanged, since every call that stays synchronous was already synchronous. V2: every TUI test passes, with only settling added where a result now arrives later.
- **What could go wrong.**
  - **A result handled on a model that moved on.** A result runs on the model `Update` holds when it arrives, which is the point. Each converted site's code after the call read only the model and the session, never values captured before the call, apart from the arguments it passes in. The start functions read the session when they run, so a swap between enqueue and start is respected.
  - **Work that waited for the freeze now overlaps a call.** Synchronous calls and keys other than Enter go on while a call runs. The agent already serializes a reload against a turn (`reloadMu`) and tolerates one during a turn: `/set` during a turn reloads today. A prompt waits behind any queued call, so the first visible thing to overlap is a slash command's own effect, which never depended on a reload.
  - **A queue that never drains.** A call that never returns would block the queue, as it blocked `Update` before, but the screen keeps redrawing and Ctrl+C still reaches `finishQuit`, which cancels the call's context.
  - **Plan 2.** It moves `app`'s `Run` tests to `launch`. None of them drives a converted call through the TUI: `TestRunWiresTheConfigSeam` calls `ReloadConfig` on the session directly.
