# P2 plan 5c: Prompt — implementation plan

Date: 2026-11-03. Status: ready for review.
Base: `main` at `90488446` (plans 5a and 5b merged). Every anchor below was checked there.
- The JSON literals in Contracts were produced by encoding the declared types, and the declarations pass T2 (`TestWireFieldsFollowTheRules`).
- Two of this plan's changes fix bugs found while writing it (decisions 10 and 11). Both were confirmed before the plan was written: the first by a throwaway test, the second by reading the code.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C4 (the adapter), C5 (`agentReady`), C6 (`Stack`'s `busy`), C8 (turns and runs), Migration order item 5c, its revision record entry "2026-11-03, against `90488446` (plans 5a and 5b merged)", and tests T1, T1b, T2 and T4.
Previous plan: [plan 5b, turn control](2026-11-02-p2-5b-turn-control-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI runs prompt turns, reviews and custom-agent runs through the session, stops them through it, and asks it for the next-reply suggestion and the verify proposal. Admitting a run, registering its stop group and its work, and setting the session idle at its end all happen agent side.
- `acpwire` gains `PromptParams`, `PromptResult` and its stop reasons, `ReviewParams`, `AgentRunParams`, `VerifyCommands`, `CodeNoRunner`, and `SessionStatus.AgentReady`.
- `acpclient.Session` gains `Prompt`, `Stop`, `Review`, `RunAgent`, `Suggest` and `VerifyProposal`.
- `inproc` gains the run path, which admits one run at a time, and `Deps` for the runtime's runner and its four calls. The runtime gains `TurnRunner`, `RunReview`, `RunCustomAgent`, `SuggestNextReply` and `ProposeVerifyCommands`.
- The TUI options `WithRunner`, `WithRunnerSource`, `WithReviewDispatcher`, `WithSuggestionProvider`, `WithSubagentFactory` and `WithCustomAgentRunnerFactory` are deleted, and `SessionSwapResult` loses `Runner` and `ReviewDispatcher`.
- Run-now and the verify proposal work (decisions 10 and 11).

**Non-goals:**
- **No swarm, SDD or plan-authoring run.** `startAgentRun`, `runAgentCmd`, the TUI's own stop registration and `handlePlanAuthorFinished` stay for them until plan 5d.
- **No SDD gate.** Its answer and its user message move with `AnswerSDDGate` in plan 5d.
- **No ending or swap.** `finishQuit` keeps cancelling the turn's context, and the exit analysis keeps `rt.Runner`, until plan 5e.
- **No goal prompt for Run-now.** The roster still passes an empty goal, as it does today (decision 10).
- **No notes or notices.** The refusal and failure notes, the provider notice and the no-runner echo of the user's text stay TUI writes until plan 6.
- **No ACP change.** `session/prompt` and `session/cancel` are unchanged, and the four new ACP methods are P3's to add.
- **No web change.**
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-5c-suite`. Create the directory first: `mkdir -p /private/tmp/p2-5c-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green apart from one known hang.** At `90488446`, 116 packages passed. `web/bridge` failed when `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` hit the 10-minute timeout. That hang comes from the test's fake agent (`fakeagent_test.go`), which answers requests inside its read loop. While `blockPrompt` holds the loop, the `session/cancel` that the test's budget check sends blocks the writer for good. The test hangs about one run in three on its own, and no file under `web/` changed since the last green run at `80532121`. Treat that failure as known.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **The runner writes the user's message and resets the activity itself.** `agent.Runner.RunTask` adds the goal as a user message (`runner.go:1125`) and sets the session idle when it returns (`runner.go:1082`). So `Prompt` writes no message, and the idle reset the run path adds matters for reviews and agent runs, whose runners write a child state.
- **Production builds the runner only when the provider is built.** `Run` reads that as `providerReady` (`app.go:2797`). When startup's provider build fails, `rt.Runner` and `rt.CustomAgentFactory` stay nil until a reload builds them (`app.go:3228`, `app.go:3291`).
- **The runtime replaces its runner, state and factory under `rt.mu`**, in `NewSession` (`runtime.go:2118` to `2146`) and in the reload (`app.go:3194` to `3291`). The runner source read `rt.Runner` under `rt.mu` (`app.go:2866`), and so do the new runtime methods.
- **The TUI's context does not change.** `New` defaults `m.ctx` to `context.Background()` (`model.go:1611`), and `Run` passes `WithContext(ctx)` (`app.go:2874`), so deleting `WithRunner`, which also set `m.ctx`, changes nothing.
- **`Update` has a value receiver** (`model.go:1923`). A closure that captures `m` keeps a pointer to a model that `Update` has already copied out and returned, so what the closure writes never reaches the live model. That is the Run-now bug (decision 10). Every closure this plan adds captures the session value and a context, never `m`.

**Decisions this plan makes beyond the spec** (decisions 1 to 12 and 15 are also recorded in the spec's revision record):

1. **One run at a time, agent side.** The adapter keeps one turn slot. `Prompt`, `Review` and `RunAgent` take it, and a second run while one holds it returns `CodeBusy`, as ACP's `runTurn` refuses a second turn. The TUI never starts one while busy, so this only guards the seam.
2. **One run path.** Every run takes the slot, calls `BeginWork`, registers its stop group (`BeginTurnStop`), runs, then sets the session idle, releases the slot, and ends its stop group and its work. That is the order of ACP's `runTurn` and of the TUI's `startAgentRun`. Registration comes after `BeginWork` because it stamps the turn onto the work item `BeginWork` opened.
3. **A refused start comes back from the call.** The run reports `CodeBusy`, `CodeQuiescing` (for any error that is `session.ErrSessionQuiescing`, the conversation's ending included), `CodeNoRunner` or `CodeSessionClosed`. The TUI writes the note today's synchronous refusal writes ("Cannot start work: …", "Cannot start review: …") and goes idle. `startAgentRun`'s `started` result goes. A prompt marked `Queued` goes back on the steering queue agent side when it is refused for being busy, ending or runner-less. That settles the put-back plan 5b left to this plan. The TUI has already applied a turn's start by then (the held window and the suggestion reset), which shows only while the session is ending, the one time `BeginWork` refuses.
4. **`Stop` acts on the session's run, and only on it.** If the run has registered its stop group, `Stop` stops that group (the commands attached to the turn first) and then cancels the run's context. If it has not registered yet, `Stop` marks the slot stopped, and the run path skips the run once it has registered. With nothing running, `Stop` does nothing. It records no pending stop, so a late `Stop` can never cancel the next turn. The TUI still cancels the context it passed (`agentCancel`) after `Stop`. In process, that also covers a `Stop` that lands before the run took the slot, and `finishQuit` keeps using it until plan 5e.
5. **A stopped run is not an error.** `Prompt` returns `StopReasonCancelled` with no error, and `Review` and `RunAgent` return no error, as ACP's `resultOrError` does.
6. **A run's end sets the session idle agent side.** `handleAgentFinished`'s two `SetActivity` calls go. `runAgentCmd`, which swarm and SDD runs still use, sets the session idle itself when `Run` returns, so those runs keep their reset until plan 5d. `handlePlanAuthorFinished` keeps its own.
7. **`Status().AgentReady` replaces the TUI's `m.runner == nil` checks.** The adapter reports whether `Deps.Runner` returns a runner. `Prompt` without one returns `CodeNoRunner` and records nothing. The TUI checks `AgentReady` before every run it starts. When it is false, a submitted prompt keeps echoing the user's text locally (`keypress.go:388`), which plan 6 moves with the notes.
8. **The runtime's calls move into `Deps`.** `Run` binds `Deps.Runner`, `Review`, `RunAgent`, `Suggest` and `VerifyProposal` to runtime methods that read the runtime's current runner, state and factory under `rt.mu`, through one helper, `bindTurnDeps`. They keep working after a swap and after a reload builds the runner, so `adoptRunner` and the runner source go. Review, Run-now, the suggestion fallback and the proposal used to stay unwired after a `/connect` recovery until a restart. They now work as soon as `AgentReady` is true.
9. **`ReviewParams` carries the base and the range apart.** The TUI keeps refusing both together before it starts anything. The adapter refuses them too, and `RunReview` encodes the base as `base:<ref>`, the form `resolveReviewRange` reads.
10. **Run-now runs on the live model and shows its subagent.** This fixes a bug. The roster's dispatch closure captured `m`, so `startAgentRun` set `busy`, `agentCancel` and the stop teardown on a stale copy. The run went ahead, but the live model was not busy, Ctrl+C and `/stop` could not reach it, and a second prompt could start beside it. The custom agent also ran on a child state nothing registered, so its output was never shown. Now:
    - the dispatch returns a command whose message, `runAgentMsg`, the live model handles;
    - `RunCustomAgent` runs the agent as a registered subagent labelled `agent · <name>`, and appends its summary to the transcript, as a review does;
    - an agent that cannot be built reports as a failed turn (`✘ Turn failed: run agent: build subagent: …`), not as "Cannot run custom agent …";
    - the swarm-runner fallback goes, because production always wired the factory.

    The roster still passes an empty goal (non-goal).
11. **The verify proposal parses its own subagent's transcript.** This fixes a bug. The proposal's runner writes to its child state (`app.go:2435`), but `proposalFromSession` scanned the parent session's messages. So `f` failed with "proposal task returned no parseable JSON" unless the main conversation's last reply happened to hold a build/test object. The TUI's test runner wrote into the parent, which hid this. `ProposeVerifyCommands` parses the child state's messages. The parse moves from `proposalFromSession` into `app`'s `verifyCommandsFrom`, and the error texts stay.
12. **`Suggest` is the model call only.** The TUI keeps its rules: it extracts the deterministic suggestion, decides when to ask, and discards stale answers. It asks only when `AgentReady` is true, where it asked only with a provider wired.
13. **`VerifyProposal` is not a run.** It takes no slot, no work and no stop registration, as today, and runs under the TUI's context.
14. **T1b and the allowlist do not change.** Every state call the prompt path stops making (`BeginWork`, `EndWork`, `BeginTurnStop`, `EndTurnStop`, `RequestTurnStop`, `SetActivity`) is still made by the swarm, SDD and plan-authoring paths until plan 5d. No allowlisted import goes: each edited file on the allowlist still uses every entry it has, most of them `marshal/internal/app/session` for the notes it writes.
15. **The new ACP methods are named** `session/review`, `session/agent_run`, `session/suggest` and `session/verify_proposal`.

**Execution:**
- Branch first: `git switch -c p2-5c-prompt` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-5c-prompt: record the plan 5c revisions and plan 5c`. The main checkout holds other untracked files (`docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-5c-prompt: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.** `turns.go` (new):

```go
// PromptParams is one prompt for a session's turn (C8).
type PromptParams struct {
	// Text is the user's message. P3 sends it as one text content block.
	Text string `json:"text"`
	// Queued says the text came off the steering queue. When the turn cannot
	// start, the session puts the text back on the queue.
	Queued bool `json:"queued,omitempty"`
}

// PromptResult is how a turn ended.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// How a turn ended, as PromptResult reports it. They are ACP's stop reasons.
const (
	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"
)

// ReviewParams is one /review run: the reviewer's focus, an optional model,
// and what to review. At most one of Range and Base is set; with neither, the
// reviewer reviews the uncommitted changes.
type ReviewParams struct {
	Focus string `json:"focus,omitempty"`
	Model string `json:"model,omitempty"`
	Range string `json:"range,omitempty"` // an explicit git range
	Base  string `json:"base,omitempty"`  // a ref: review from its merge base with HEAD
}

// AgentRunParams runs one custom agent, by name, with a goal.
type AgentRunParams struct {
	Agent string `json:"agent"`
	Goal  string `json:"goal,omitempty"`
}

// VerifyCommands is a build and test command pair, as VerifyProposal proposes
// it. An empty command means the step does not apply.
type VerifyCommands struct {
	Build string `json:"build,omitempty"`
	Test  string `json:"test,omitempty"`
}
```

`errors.go` gains, after `CodeSessionClosed`:

```go
	// CodeNoRunner means the session has no agent runner: its provider is not built.
	CodeNoRunner ErrorCode = "no_runner"
```

`status.go`: `SessionStatus` gains, after `SubagentRunning`:

```go
	// AgentReady says the session's agent runner is built, so Prompt, Review
	// and RunAgent can start. It is false while the provider is not built.
	AgentReady bool `json:"agentReady,omitempty"`
```

The JSON `TestTurnsJSON` pins:
- `PromptParams{Text: "fix the build"}`: `{"text":"fix the build"}`;
- `PromptParams{Text: "and the tests", Queued: true}`: `{"text":"and the tests","queued":true}`;
- `PromptResult{StopReason: StopReasonCancelled}`: `{"stopReason":"cancelled"}`;
- `ReviewParams{Focus: "main.go", Base: "main"}`: `{"focus":"main.go","base":"main"}`;
- `ReviewParams{}`: `{}`;
- `AgentRunParams{Agent: "linter", Goal: "check style"}`: `{"agent":"linter","goal":"check style"}`;
- `AgentRunParams{Agent: "linter"}`: `{"agent":"linter"}`;
- `VerifyCommands{Build: "go build ./...", Test: "go test ./..."}`: `{"build":"go build ./...","test":"go test ./..."}`;
- `VerifyCommands{}`: `{}`.

**`internal/acpclient`, after Task 1.** `Session` gains, at the end of the interface, in this order:

```go
	// Prompt runs one turn with params.Text as the user's message and returns
	// when the turn ends. One run at a time: a turn that cannot start returns
	// CodeBusy (a run holds the session), CodeQuiescing (the session is ending)
	// or CodeNoRunner (no runner is built), and a Queued text then goes back on
	// the steering queue. A stopped turn returns StopReasonCancelled and no
	// error; a failed one returns the runner's error.
	//
	// ACP: session/prompt (exists).
	Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error)

	// Stop stops the session's run: the commands attached to it first, then
	// the run itself. With nothing running it does nothing.
	//
	// ACP: session/cancel (exists).
	Stop(ctx context.Context, cause string) error

	// Review runs a reviewer subagent over what params names and returns when
	// it ends. It starts, refuses and stops as Prompt does; a stopped review
	// returns no error.
	//
	// ACP: session/review (new in P3).
	Review(ctx context.Context, params acpwire.ReviewParams) error

	// RunAgent runs the named custom agent with the goal, as a subagent the
	// session shows, and returns when it ends. It starts, refuses and stops as
	// Prompt does; a stopped run returns no error.
	//
	// ACP: session/agent_run (new in P3).
	RunAgent(ctx context.Context, params acpwire.AgentRunParams) error

	// Suggest asks the session's model for the user's most likely next reply
	// to lastMessage. "" means it has none.
	//
	// ACP: session/suggest (new in P3).
	Suggest(ctx context.Context, lastMessage string) (string, error)

	// VerifyProposal runs a one-shot subagent that inspects the repository
	// and proposes the commands that build and test it.
	//
	// ACP: session/verify_proposal (new in P3).
	VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error)
```

`Methods` gains these rows at the end, in the same order:

| Name | ACP | Exists |
|---|---|---|
| `Prompt` | `session/prompt` | true |
| `Stop` | `session/cancel` | true |
| `Review` | `session/review` | false |
| `RunAgent` | `session/agent_run` | false |
| `Suggest` | `session/suggest` | false |
| `VerifyProposal` | `session/verify_proposal` | false |

**`internal/acpclient/inproc`, after Task 1.**

```go
// inproc.go: Deps gains, at the end:

	// Runner returns the runtime's current agent runner, or nil while the
	// provider is not built. A nil func means the session never has one.
	Runner func() Runner
	// Review, RunAgent, Suggest and VerifyProposal are the runtime's calls.
	// Each reads the runtime's current state, so the same Deps keep working
	// after a swap. Without one, the matching Session call fails.
	Review         func(ctx context.Context, params acpwire.ReviewParams) error
	RunAgent       func(ctx context.Context, params acpwire.AgentRunParams) error
	Suggest        func(ctx context.Context, lastMessage string) (string, error)
	VerifyProposal func(ctx context.Context) (acpwire.VerifyCommands, error)

// inproc.go: Session gains, with the other fields:

	runner         func() Runner
	review         func(context.Context, acpwire.ReviewParams) error
	runAgent       func(context.Context, acpwire.AgentRunParams) error
	suggest        func(context.Context, string) (string, error)
	verifyProposal func(context.Context) (acpwire.VerifyCommands, error)
	// turn is the run in flight, nil when none. It is guarded by mu.
	turn *turnSlot

// turns.go (new)

// Runner runs one turn of the agent loop. *agent.Runner satisfies it.
type Runner interface {
	Run(ctx context.Context, goal string) error
}

// turnSlot is the run a session has in flight. Its fields are guarded by
// Session.mu.
type turnSlot struct {
	cancel  context.CancelFunc
	token   *session.TurnStopToken // nil until the run registers its stop group
	stopped bool                    // Stop reached the run
}

func (s *Session) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error)
func (s *Session) Stop(ctx context.Context, cause string) error
func (s *Session) Review(ctx context.Context, params acpwire.ReviewParams) error
func (s *Session) RunAgent(ctx context.Context, params acpwire.AgentRunParams) error
func (s *Session) Suggest(ctx context.Context, lastMessage string) (string, error)
func (s *Session) VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error)

// runTurn is the one path every run takes (decision 2). It reports whether
// Stop reached the run, and the run's error or the refusal.
func (s *Session) runTurn(ctx context.Context, run func(context.Context) error) (stopped bool, err error)

// currentRunner is Deps.Runner's runner, or nil. Status reports AgentReady
// from it.
func (s *Session) currentRunner() Runner

var (
	errTurnBusy      = &acpwire.Error{Code: acpwire.CodeBusy, Message: "inproc: a run is already in flight"}
	errNoRunner      = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no agent runner"}
	errNoReview      = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no review runner"}
	errNoAgentRun    = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no agent runner for custom agents"}
	errNoSuggest     = errors.New("inproc: no suggestion provider")
	errNoProposal    = errors.New("no subagent factory wired")
	errRangeAndBase  = errors.New("review: --base and an explicit range are mutually exclusive")
	errNoAgentName   = errors.New("inproc: an agent run needs an agent name")
)
```

**`internal/app`, after Task 2.** `turns.go` (new):

```go
// TurnRunner returns the runtime's current agent runner, or nil while the
// provider is not built. It never returns a typed nil.
func (rt *Runtime) TurnRunner() inproc.Runner

// RunReview runs a reviewer subagent over the runtime's current session.
func (rt *Runtime) RunReview(ctx context.Context, params acpwire.ReviewParams) error

// RunCustomAgent runs the named custom agent as a subagent of the runtime's
// current session.
func (rt *Runtime) RunCustomAgent(ctx context.Context, params acpwire.AgentRunParams) error

// SuggestNextReply asks the runner's model for the user's most likely next
// reply to lastMessage, in at most 10 words. "" means none.
func (rt *Runtime) SuggestNextReply(ctx context.Context, lastMessage string) (string, error)

// ProposeVerifyCommands runs a one-shot subagent that proposes build and test
// commands, and parses its reply.
func (rt *Runtime) ProposeVerifyCommands(ctx context.Context) (acpwire.VerifyCommands, error)

// bindTurnDeps points d's runner and run calls at rt. Run uses it, and so do
// the tests that build Deps the way Run does.
func bindTurnDeps(d *inproc.Deps, rt *Runtime)

// runCustomAgent runs the named custom agent with goal through runSubagentTask.
func runCustomAgent(ctx context.Context, state *session.State, factory agent.SubagentRunnerFactory, name, goal string) error

// proposeVerifyCommands runs the proposal subagent and parses its own
// transcript.
func proposeVerifyCommands(ctx context.Context, factory agent.SubagentRunnerFactory) (acpwire.VerifyCommands, error)

// verifyCommandsFrom returns the newest build and test object in msgs'
// assistant messages.
func verifyCommandsFrom(msgs []session.Message) (acpwire.VerifyCommands, error)

// verifyProposalPrompt is the proposal subagent's goal, unchanged from the TUI.
const verifyProposalPrompt = "Inspect this repository and return exactly one JSON object with \"build\" and \"test\" fields: the shell commands (argv, no pipes) to compile and test it, or empty strings if a step does not apply. Only propose commands that will actually work."

// review.go

// runSubagentTask runs one subagent the session shows: it builds the child
// from req, registers it under label, runs prompt, finishes the view, and
// appends the child's summary to the transcript. kind prefixes its errors.
func runSubagentTask(ctx context.Context, state *session.State, factory agent.SubagentRunnerFactory, req agent.SubagentRequest, kind, label, prompt string) error
```

**`internal/app/tui`, after Tasks 3 and 4.**

```go
// model.go

// agentFinishedMsg reports a run's end. refusal is the note a refused start
// writes, as "Cannot start work"; it is empty for swarm and SDD runs, which
// the TUI refuses before it starts them.
type agentFinishedMsg struct {
	err     error
	refusal string
}

// beginTurn marks a run as started on the model and returns its context: the
// provider notice is cleared, busy and the turn's start are set, the held
// window starts, the suggestion is reset, and agentCancel cancels the context.
func (m *Model) beginTurn() context.Context

// startSessionRun starts a run the session owns.
func (m *Model) startSessionRun(refusal string, run func(context.Context) error) (tea.Model, tea.Cmd)

// startPrompt starts a prompt turn through the session.
func (m *Model) startPrompt(text string, queued bool) (tea.Model, tea.Cmd)

// sessionRunCmd runs run under ctx and reports its end.
func sessionRunCmd(ctx context.Context, refusal string, run func(context.Context) error) tea.Cmd

// refusedStart reports whether err says the run never started: CodeBusy,
// CodeQuiescing, CodeNoRunner or CodeSessionClosed.
func refusedStart(err error) bool

// runAgentMsg asks the live model to start a Run-now (Task 4).
type runAgentMsg struct{ agent, goal string }

// runNowDispatch is the roster's Run-now dispatch. It captures nothing.
func runNowDispatch(agentName, goal string) tea.Cmd

// agents/panel.go (Task 4)

// RunNow is the Run-now action for the named agent: the dispatch's command,
// or nil without a dispatch. The Run-now field's action calls it.
func (p *Panel) RunNow(agentName string) tea.Cmd
```

**Test helpers.**

```go
// tui: seam_test.go (Task 3)

// runnerOf returns a Deps.Runner that always returns r.
func runnerOf(r inproc.Runner) func() inproc.Runner

// withPromptRunner replaces m's session with one over m's state whose runner
// is r.
func withPromptRunner(m Model, r inproc.Runner) Model

// tui: turns_seam_test.go (new, Task 3)

// turnRecorder is a session whose run calls are recorded before the wrapped
// session makes them. Every other method is the wrapped session's.
type turnRecorder struct {
	acpclient.Session
	prompts   []acpwire.PromptParams
	stops     []string
	reviews   []acpwire.ReviewParams   // Task 4
	agentRuns []acpwire.AgentRunParams // Task 4
	suggests  []string                 // Task 4
	proposals int                      // Task 4
}

// testModelTurns builds a sized model over an adapter made from d whose
// session is rec, wrapped around the adapter's session.
func testModelTurns(t *testing.T, d inproc.Deps, rec *turnRecorder, opts ...Option) Model

// tui: model_test.go (Task 4)

// withSuggester replaces m's session with one over m's state that has a
// runner and calls suggest for Suggest.
func withSuggester(m Model, suggest func(context.Context, string) (string, error)) Model
```

## Task 1: The session takes the run calls

**Goal:** the adapter runs prompts, reviews and agent runs on one path, stops them, forwards the suggestion and the proposal, and reports `AgentReady`. Nothing calls it yet.

**Files:** `internal/acpwire/turns.go` (new), `turns_test.go` (new), `errors.go`, `status.go`; `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `turns.go` (new), `turns_test.go` (new).

**Steps:**

1. **`acpwire`.** Add `turns.go`, `CodeNoRunner` and `AgentReady` (Contracts).
2. **`acpclient.go`.** Add the six methods at the end of `Session`, with their Contracts doc comments. **`methods.go`:** add their rows at the end of `Methods`, with the Contracts' `Exists` column.
3. **`inproc/inproc.go`:**
   - `Deps` and `Session` gain the Contracts' fields;
   - **`Status`** sets `AgentReady` to whether `currentRunner()` is non-nil.
4. **`inproc/events.go`.** `newSession` copies the five new `Deps` fields.
5. **`inproc/turns.go`** (new) holds `Runner`, `turnSlot`, the six methods, `runTurn`, `currentRunner` and the errors (Contracts). Every method first returns `errSessionClosed` on a closed session (`Suggest` with "", `Prompt` with a zero result).
   - **`currentRunner`** returns nil when `s.runner` is nil, and otherwise what it returns.
   - **`runTurn`**, in this order:
     1. Derive the run's context from `ctx` with a cancel, and build a slot holding the cancel. Under `s.mu`, if `s.turn` is set, cancel and return `errTurnBusy`; otherwise set `s.turn` to the slot.
     2. Call `s.state.BeginWork()`. On an error, clear `s.turn` (when it is still this slot) and cancel. Return `&acpwire.Error{Code: acpwire.CodeQuiescing, Message: err.Error()}` when `errors.Is(err, session.ErrSessionQuiescing)`, and the error itself otherwise.
     3. Defer the cleanup, which runs in this order: `s.state.SetActivity(session.Activity{Kind: session.ActivityIdle})`; under `s.mu`, read the slot's `stopped` into the result and clear `s.turn` when it is still this slot; `s.state.EndTurnStop(token)`; `s.state.EndWork()`; cancel.
     4. Call `s.state.BeginTurnStop("")`. Under `s.mu`, store the token in the slot, and mark the slot stopped when the pending cause it returned is not empty: `BeginTurnStop` has already stopped the group for that cause. Then read `stopped`. Skip the run when it is set. Also skip it when the run's context is already done, and make the context's error the run's error.
     5. Otherwise call `run` with the run's context. A panic in it is recovered and becomes the error `fmt.Errorf("panic in agent run: %v", r)`, as `runAgentCmd` does.
   - **`Stop`**: under `s.mu`, take `s.turn`. With none, return nil. Otherwise set its `stopped` and read its token. After unlocking, if the token is set, call `token.Stop(cause)`, and log an error that is not `session.ErrNoTurnStopService` with `s.state.Logger().Warn("turn stop was not accepted by the command service", "error", err)`. Then call the slot's cancel. Return nil.
   - **`Prompt`**: take `currentRunner()`. Without one, put a `Queued` text back with `s.state.PushSteering`, and return `errNoRunner`. Otherwise call `runTurn` with a run that calls the runner's `Run` with `params.Text`. When the error's code (`acpclient.CodeOf`) is `CodeBusy` or `CodeQuiescing`, put a `Queued` text back the same way and return the error. When stopped, return `StopReasonCancelled` and no error. On another error, return a zero result and the error. Otherwise return `StopReasonEndTurn`.
   - **`Review`**: return `errNoReview` without `s.review`, and `errRangeAndBase` when both `Range` and `Base` are set. Otherwise call `runTurn` with a run that calls `s.review(ctx, params)`. Return nil when stopped, and the error otherwise.
   - **`RunAgent`**: return `errNoAgentRun` without `s.runAgent`, and `errNoAgentName` for an empty `Agent`. Otherwise run it as `Review` does, through `s.runAgent`.
   - **`Suggest`** returns `errNoSuggest` without `s.suggest`, and otherwise its result. **`VerifyProposal`** returns `errNoProposal` without `s.verifyProposal`, and otherwise its result. Neither takes the slot (decision 13).
6. **Tests:**
   - **`acpwire/turns_test.go`** (new): **`TestTurnsJSON`** encodes the nine values and compares the bytes with the Contracts literals, in the table style of `TestRequestsJSON`;
   - **`inproc/turns_test.go`** (new). Each test builds its state with `newState(t, "s1")`. A test fake, `fakeRunner`, records each goal it gets, can block until a channel closes, signals when `Run` starts, and returns a set error. A fake turn-stop service records its `StopAttached` calls, as the TUI's `fakeTUISessionStopService` (`interrupt_test.go`) does:
     - **`TestPromptRunsTheRunner`**. With `Deps.Runner` returning the fake, `Prompt` with "hello" returns `StopReasonEndTurn` and no error, and the fake got "hello";
     - **`TestPromptRegistersItsWorkAndStopGroupWhileItRuns`**. Inside the fake's `Run`, `state.CurrentTurnStop()` is set. After `Prompt` returns, it is nil, and `state.BeginQuiesce()` then `state.WaitForWork` with a one-second context returns nil;
     - **`TestARunEndsIdle`**. A fake that sets the activity to `ActivityThinking` inside `Run` leaves `state.Activity().Kind` idle once `Prompt` returns;
     - **`TestPromptWithoutARunnerIsRefused`**. With no `Deps.Runner`, and again with one that returns nil, `Prompt` returns `CodeNoRunner` and `Status().AgentReady` is false. A `Queued` text is then in `state.SteeringQueue()`, and an unqueued one is not;
     - **`TestAgentReadyFollowsTheRunner`**. `Deps.Runner` returns a variable that starts nil: `AgentReady` is false, then true once the variable holds the fake;
     - **`TestASecondRunIsBusy`**. While a blocking `Prompt` runs in a goroutine (wait for its start), a second `Prompt` returns `CodeBusy` and puts its `Queued` text back on the queue, and `Review` returns `CodeBusy`;
     - **`TestAnEndingSessionRefusesThePrompt`**. After `state.BeginQuiesce()`, `Prompt` returns `CodeQuiescing` with the message "session is quiescing", the fake got nothing, `state.CurrentTurnStop()` is nil, and a `Queued` text is back on the queue;
     - **`TestStopStopsTheGroupBeforeTheRun`**. With the fake stop service set (`state.SetTurnStopService`) and a blocking runner that appends "agent-cancel" when its context is done, `Stop(ctx, "turn stopped")` while it runs makes the order `service-stop`, then `agent-cancel`. The service saw the cause "turn stopped", and `Prompt` returns `StopReasonCancelled` with no error;
     - **`TestAStopRequestedBeforeTheTurnSkipsIt`**. After `state.RequestTurnStop("early")` with nothing registered, `Prompt` returns `StopReasonCancelled`, and the fake got nothing;
     - **`TestStopWithNothingRunningLeavesTheNextTurnAlone`**. `Stop` with no run returns nil, and a following `Prompt` runs the fake and returns `StopReasonEndTurn`;
     - **`TestAPanickingRunnerFailsTheTurn`**. A runner that panics makes `Prompt` return an error containing "panic in agent run". `state.CurrentTurnStop()` is nil afterwards, and a second `Prompt` with the fake runs;
     - **`TestReviewRunsOnTheRunPath`**. `Deps.Review` records its params and, while it runs, `state.CurrentTurnStop()` is set. `Review` passes the params unchanged and returns nil. With both `Range` and `Base`, it returns `errRangeAndBase` and `Deps.Review` is not called. Without `Deps.Review`, it returns `CodeNoRunner`;
     - **`TestRunAgentRunsOnTheRunPath`**. As for review, through `Deps.RunAgent`. An empty `Agent` returns an error and calls nothing;
     - **`TestAStoppedReviewIsNotAnError`**. A blocking `Deps.Review` that returns its context's error once stopped: `Stop` while it runs makes `Review` return nil;
     - **`TestSuggestAndVerifyProposalForward`**. Each returns what its `Deps` func returns, and without one each returns an error;
     - **`TestAClosedSessionRunsNothing`**. After `host.Swap`, the old session's six calls return errors with `acpwire.CodeSessionClosed`, and the fake got nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/acpclient/inproc/ -run 'Prompt|Run|Stop|Busy|Ending|AgentReady|Panicking|Review|Suggest|VerifyProposal|Closed' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/tui/...
go vet ./internal/acpwire/ ./internal/acpclient/...
```

Expected: every listed test passes, `TestAdapterDoesNotImportApp` and `TestClientImportsStayOnTheWireSide` included. T4 passes on both sides: `session/prompt` and `session/cancel` are registered, and the four new names are not. `tui` passes unchanged, because its test sessions embed `acpclient.Session`.

## Task 2: The runtime serves the run calls

**Goal:** the runtime answers the five `Deps` calls over its current runner, state and factory, and `Run` binds them. The TUI still uses its own options until Tasks 3 and 4.

**Files:** `internal/app/turns.go` (new), `review.go`, `app.go`; `turns_test.go` (new).

**Steps:**

1. **`review.go`.** Move `runReviewSubagent`'s body from the `factory(req)` call to the end into `runSubagentTask` (Contracts). It formats its two errors as `"%s: build subagent: %w"` and `"%s: subagent run failed: %w"` with `kind`, and builds the meta's role from `req.Role`. `runReviewSubagent` keeps its signature: it resolves the range, builds the label and the prompt as now, and calls `runSubagentTask` with the kind "review" and the label `"review · " + label`. Its tests do not change.
2. **`turns.go`** (new) holds the Contracts' runtime methods, functions and constant:
   - **`TurnRunner`**, under `rt.mu`, returns nil when `rt.Runner` is nil, and `rt.Runner` otherwise;
   - **`RunReview`** reads `rt.State` and `rt.CustomAgentFactory` under `rt.mu`. Without a factory it returns `errors.New("review: no agent runner")`. Otherwise it calls `runReviewSubagent` with the focus, the model and a range that is `params.Range`, or `"base:" + params.Base` when the range is empty and the base is set;
   - **`RunCustomAgent`** reads the same two fields. Without a factory it returns `errors.New("run agent: no agent runner")`, and otherwise calls `runCustomAgent`;
   - **`runCustomAgent`** calls `runSubagentTask` with `agent.SubagentRequest{Agent: name}`, the kind "run agent", the label `"agent · " + name`, and `goal` as the prompt;
   - **`SuggestNextReply`** holds the body of the `WithSuggestionProvider` closure in `Run` (`app.go:2913` to `2934`) unchanged: the runner under `rt.mu`, `""` and no error without one, the same two messages to `Chat`, and `""` for "NONE";
   - **`ProposeVerifyCommands`** reads `rt.CustomAgentFactory` under `rt.mu`. Without one it returns `errors.New("no subagent factory wired")`, and otherwise calls `proposeVerifyCommands`;
   - **`proposeVerifyCommands`** calls the factory with `agent.SubagentRequest{}`. A factory error becomes `fmt.Errorf("build proposal runner: %w", err)`, and a nil runner `errors.New("proposal runner is nil")`. It runs the child with `verifyProposalPrompt`, and a run error becomes `fmt.Errorf("proposal task failed: %w", err)`. It returns `verifyCommandsFrom(childState.Messages())`: the child's transcript, not the parent's (decision 11);
   - **`verifyCommandsFrom`** is `proposalFromSession`'s loop (`model.go:4696`) over `msgs`, returning `acpwire.VerifyCommands`, with the same "proposal task returned no parseable JSON" error;
   - **`bindTurnDeps`** sets `d.Runner = rt.TurnRunner`, `d.Review = rt.RunReview`, `d.RunAgent = rt.RunCustomAgent`, `d.Suggest = rt.SuggestNextReply` and `d.VerifyProposal = rt.ProposeVerifyCommands`.
3. **`app.go`:**
   - `Run` calls `bindTurnDeps(&deps, rt)` right after building `deps`, before `inproc.New`;
   - the `WithSuggestionProvider` closure's body becomes a call to `rt.SuggestNextReply`. The option itself goes in Task 4.
4. **Tests, in `turns_test.go`** (new). The factories follow `review_test.go`'s: `agent.NewRunner(nil, nil, nil, childState, "test-model")` with a `RunTaskFunc`:
   - **`TestTurnRunnerIsNilWithoutARunner`**. On `&Runtime{}`, `TurnRunner()` compares equal to nil as an interface. With `Runner` set to an `agent.NewRunner(…)` value, it does not;
   - **`TestRunCustomAgentShowsTheChildAndItsSummary`**. A factory records its request and returns a child whose task gets "check style" and returns the summary "Done.". `runCustomAgent(ctx, state, factory, "linter", "check style")` returns nil. The request's `Agent` is "linter", `state.Subagents()` holds one view labelled "agent · linter" with status done, and the last message is the assistant's "Done.";
   - **`TestRunCustomAgentReportsABuildError`**. A factory that fails with "unknown custom agent" makes the error contain "run agent: build subagent: unknown custom agent", and registers no subagent;
   - **`TestProposeVerifyCommandsReadsTheChildTranscript`**. The parent state holds the assistant message `{"build":"wrong","test":"wrong"}`. The child's task adds `{"build":"make build","test":"make test"}` to the child state. `proposeVerifyCommands` returns "make build" and "make test". This is decision 11's regression test;
   - **`TestProposeVerifyCommandsWithoutJSONFails`**. A child that writes plain text makes the error "proposal task returned no parseable JSON";
   - **`TestVerifyCommandsFromTakesTheNewestObject`**. Over an older JSON object, a newer one and a newest non-JSON reply, it returns the newer object. That carries over the TUI's `TestProposalFromSession`;
   - **`TestRunReviewPassesTheBaseAsAMergeBase`**. Skip without `git`. On a `Runtime` whose state's working directory is an empty temp dir, with a factory that fails the test if called, `RunReview(ctx, acpwire.ReviewParams{Base: "main"})` fails with an error containing `cannot find merge-base for "main"`. That error comes only from a `base:` range;
   - **`TestTheRuntimeCallsFailWithoutAFactory`**. On `&Runtime{State: state}`, `RunReview`, `RunCustomAgent` and `ProposeVerifyCommands` return errors, and `SuggestNextReply` returns "" and no error;
   - **`TestBindTurnDepsServesTheRuntime`**. `d := inproc.Deps{State: state}`, `bindTurnDeps(&d, rt)` over a runtime with that state and no runner: the session `inproc.New(d)` makes reports `AgentReady` false. After `rt.Runner` is set, it reports true without a new session.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/ -run 'Review|CustomAgent|VerifyCommands|ProposeVerify|TurnRunner|RuntimeCalls|BindTurnDeps|SendNow|Adapter' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
go vet ./internal/app/
```

Expected: no `--- FAIL`, and `ok` for the package. The review tests pass unchanged.

## Task 3: Prompt turns and their stop go through the session

**Goal:** every prompt turn starts through `Prompt` and stops through `Stop`, a refused start is reported from the run's end, and the TUI holds no runner.

**Files:** `internal/app/tui/model.go`, `keypress.go`, `commands_dispatch.go`, `mcp.go`; `seam_test.go`, `turns_seam_test.go` (new), `model_test.go`, `provider_error_test.go`, `watch_resume_test.go`, `controls_seam_test.go`, `interrupt_test.go`; `internal/app/app.go`, `app_test.go`.

**Steps:**

1. **`model.go`, the run start:**
   - `agentFinishedMsg` gains `refusal` (Contracts);
   - **`beginTurn`** (Contracts) holds the lines `startAgentRun` runs after its stop registration (`busy`, `turnStartedAt`, `startHeldWindow`, the suggestion reset and generation bump, and the cancellable context it stores in `agentCancel`), plus the provider-notice clear. In `startAgentRun` the clear therefore runs after the registration instead of before it. Nothing reads the notice in between;
   - **`startAgentRun`** returns `(tea.Model, tea.Cmd)`. It keeps the ending check, `BeginWork` with its refusal note, and the stop registration, then calls `beginTurn` and returns the batch of `runAgentCmd`, `tickCmd` and `spinnerTickCmd`. Its doc says only swarm and SDD runs use it, until plan 5d. After this task its callers are the gate answer (`model.go:2613`) and `castlist.StartMsg` (`model.go:2680`);
   - **`runAgentCmd`** defers `state.SetActivity(session.Activity{Kind: session.ActivityIdle})` right after its deferred `EndWork`, so the reset runs after the panic recovery and before `EndWork` (decision 6);
   - **`startSessionRun`**: when an ending is in progress, it writes the note `startAgentRun` writes ("The conversation is ending; this request was not started."), clears `busy`, refreshes and returns no command. Otherwise it calls `beginTurn` and returns the batch of `sessionRunCmd(ctx, refusal, run)`, `tickCmd()` and `spinnerTickCmd()`;
   - **`sessionRunCmd`** returns a command that calls `run(ctx)` and returns `agentFinishedMsg{err: …, refusal: refusal}`;
   - **`startPrompt`** reads `sess := m.sess`, and calls `startSessionRun` with "Cannot start work" and a run that calls `sess.Prompt(ctx, acpwire.PromptParams{Text: text, Queued: queued})`, returning only its error. The run captures `sess`, never `m`;
   - **`refusedStart`** (Contracts) uses `acpclient.CodeOf`.
2. **The prompt callers:**
   - **`keypress.go`**, the submit (`keypress.go:388`): the no-runner branch tests `!m.sess.Status().AgentReady` and keeps its local user-message echo. Otherwise it calls `m.startPrompt(value, false)`;
   - **`dispatchCommand`**, the prompt-body branch (`model.go:6808`): the "Agent runner is not available." note is written when `!m.sess.Status().AgentReady`. Otherwise it calls `m.startPrompt(goal, false)`. The agent-goal branch (`model.go:6833`) starts only when `AgentReady`, through `m.startPrompt(res.AgentGoal, false)`;
   - **`startWatchResumeRun`** tests `!m.sess.Status().AgentReady` in place of `m.runner == nil`, calls `m.startPrompt(goal, false)`, assigns the model to `*m` and returns the command;
   - **`flushQueuedSteering`** tests `AgentReady` in place of `m.runner == nil`, pops as now, calls `m.startPrompt(followUp, true)`, assigns and returns. Its put-back and the comment about it go: a `Queued` prompt is put back agent side (decision 3).
3. **`handleAgentFinished`:**
   - a new first branch handles a refused start: when `msg.refusal` is set and `refusedStart(msg.err)`, it clears `busy`, `turnStartedAt`, `agentCancel` and `cancelling`, writes the note `msg.refusal + ": " + msg.err.Error()`, updates the viewport height, refreshes, and returns no command;
   - both `m.state.SetActivity` calls go, the SDD gate branch's included. The rest is unchanged.
4. **`cancelTurn`:** after setting `cancelling`, it calls `m.sess.Stop(m.ctx, "turn stopped")`, ignoring the error. It then calls `m.stopTurnWork("turn stopped")` only when `m.endPendingStop` is set, which only a swarm, SDD or plan-authoring run sets (decision 4). Then it cancels `agentCancel` as now. The doc comment says the session's run is stopped through the session, and a run the TUI registered itself through the state.
5. **Deleted:**
   - the `runner` and `runnerSource` fields, `WithRunner` and `WithRunnerSource`;
   - **`adoptRunner`**, and its calls in `afterRuntimeReload` (`model.go:1276`) and `mcp.go:122`. Both comments stop mentioning adoption: a runner a reload builds is used from the next turn;
   - `SessionSwapResult.Runner`, and its assignment in the new-session effect (`commands_dispatch.go:207`).
6. **`app/app.go`:** delete the `WithRunnerSource` and `WithRunner` options, the `Runner` field of the swap result (the swapper discards `rt.NewSession`'s runner as `_`), the local `runner := rt.Runner` that no longer has a use, and the comment's mention of `tui.adoptRunner` (`app.go:2848`).
7. **Test helpers.** `seam_test.go` gains `runnerOf` and `withPromptRunner` (Contracts). `withPromptRunner` builds its session as `testModel` does, over `inproc.Deps{State: m.state, Runner: runnerOf(r)}`, and sets `m.sess`, as 5b's `withEffortSetter` does.
8. **Tests moved off the runner field.** Each keeps its assertions unless a step says otherwise:
   - **`model_test.go`.** The eight tests that pass `WithRunner` build with `testModelWith(inproc.Deps{State: state, Runner: runnerOf(runner)}, …)` and their other options: `TestEnterWithRunnerDispatchesAgentRunAndTick`, `TestEnterWhileBusyIsIgnored`, `TestAgentCommandRegistersAndReleasesSessionWork`, `TestBusyTickRefreshesViewport`, `TestSlashCommandNotSentToAgent`, `TestDispatchPromptCommandStartsAgentRun`, `TestDispatchPromptCommandPreservesQuotedArgs` and `TestAgentGoalDispatchStartsRun`. In `TestAgentCommandRegistersAndReleasesSessionWork`, `BeginWork` now runs inside the command. `blockingAgentRunner` gains an optional `started chan struct{}` that `Run` closes on entry, and the test waits on it (one second at most) in place of its 10 ms sleep, before `BeginQuiesce`;
   - **`model_test.go`.** `TestAgentFinishedFlushesQueuedSteering`, `TestFlushQueuedSteeringStartsFollowUpTurn` and `TestAgentFinishedKeepsQueueOnFailure` use `m = withPromptRunner(m, runner)` in place of `m.runner = runner`. `TestPostAcceptedPromptCannotReopenWork` does the same right after `newSessionWarningModel`, calls `m.startPrompt("do more work", false)`, and drops the `started` check: the command is nil and the runner got nothing. `TestSuggestionClearedOnTurnStart` calls `m.startPrompt("continue", false)`;
   - **`model_test.go`, `TestRunAgentCmdPanicRecovery`** also checks that `state.Activity().Kind` is idle after the command;
   - **`watch_resume_test.go`.** Its eleven `m.runner = runner` lines become `m = withPromptRunner(m, runner)`, each before the test's `withWatchResume` call;
   - **`controls_seam_test.go`.** `TestAFollowUpPopsThroughTheSession` and `TestATurnEndTakesTheLatchThroughTheSession` build their model with `controlsDispatchModel(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)`, and drop the `m.runner` line;
   - **`provider_error_test.go`.** `TestSuccessfulReloadAdoptsMissingRunner` and `TestSuccessfulReloadKeepsExistingRunner` are replaced by **`TestARunnerBuiltAfterStartupIsUsedWithoutAdoption`**. Over `Deps` whose `Runner` returns a variable that starts nil, submitting "first" echoes it and starts nothing. Once the variable holds a `fakeAgentRunner`, submitting "second" starts a turn whose runner gets "second". `TestStartAgentRunClearsProviderNotice` and `TestStartAgentRunKeepsUnrelatedNotice` become `TestStartPromptClearsProviderNotice` and `TestStartPromptKeepsUnrelatedNotice`, and call `m.startPrompt("retry", false)`;
   - **`app/app_test.go`, `TestRuntimeSendNowTUIOptionsExerciseRealFacade`.** It builds with `testTUIModelWith(inproc.Deps{State: state, Runner: func() inproc.Runner { return agentProcess }}, tui.WithCommandRegistry(reg), tui.WithContext(runCtx))`.
9. **New tests:**
   - **`turns_seam_test.go`** (new) holds `turnRecorder` and `testModelTurns` (Contracts). Its `Prompt` and `Stop` record, then call the wrapped session's. `testModelTurns` builds as `testModelControlled` does, then sizes the model to 80×24 and refreshes it:
     - **`TestASubmitPromptsThroughTheSession`.** Over `Deps` with a `fakeAgentRunner`, typing "hello" and pressing Enter through `sendKey` records the prompt `{Text: "hello"}`, the runner gets "hello", and the model is not busy once the commands are drained;
     - **`TestAFollowUpIsAQueuedPrompt`.** With "later" pushed on the queue, `flushQueuedSteering`'s command records `{Text: "later", Queued: true}`;
     - **`TestARefusedFollowUpGoesBackOnTheQueue`.** With "later" queued and `state.BeginQuiesce()` called, draining `flushQueuedSteering`'s command leaves "later" in `state.SteeringQueue()`. The last message is "Cannot start work: session is quiescing", and the model is not busy;
     - **`TestCancelTurnStopsThroughTheSession`.** After `startPrompt` with a blocking runner whose command runs in a goroutine (wait for its start), `cancelTurn` records the stop "turn stopped", and the runner's context is done within one second;
     - **`TestTheTurnEndLeavesTheActivityToTheSession`.** With the activity set to `ActivityThinking`, `handleAgentFinished(agentFinishedMsg{})` leaves it at `ActivityThinking`;
   - **`interrupt_test.go`, `TestCancelTurnLeavesNoPendingStop`.** With the test service set, `busy` true and no registration, `cancelTurn` makes no service stop, and the next `state.BeginTurnStop("")` returns an empty pending cause.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'WithRunner\(|WithRunnerSource|runnerSource|adoptRunner|m\.runner[^A-Za-z_]|snap\.Runner' internal --include='*.go'
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/tui/ -run 'ThroughTheSession|Prompt|FollowUp|Steer|WatchResume|Latch|Cancel|Stop|Notice|Runner|Suggestion|AgentGoal|Dispatch|Busy|SessionWork|PostAccepted|Turn|Panic' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/tui/...
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/ -run 'SendNow|Adapter|BindTurnDeps' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
go vet ./internal/app/...
```

Expected: the grep prints nothing. Every listed test passes, and `tui` passes, the goldens included.

## Task 4: Review, Run-now, the suggestion and the proposal go through the session

**Goal:** `/review`, the roster's Run-now, the suggestion fallback and the proposal go through the session, and the TUI holds no factory or dispatcher.

**Files:** `internal/app/tui/model.go`, `commands_dispatch.go`, `agents/panel.go`; `model_test.go`, `verifygate_test.go`, `turns_seam_test.go`; `internal/app/app.go`.

**Steps:**

1. **`commands_dispatch.go`, the `review` entry of `tuiCommandEffects`:**
   - while busy it returns, as now;
   - when `!m.sess.Status().AgentReady`, it writes "Review subagent is not available in this build." and returns;
   - it parses the arguments, and with both a base and a range it writes "review: --base and an explicit range are mutually exclusive" and returns, before anything starts;
   - otherwise it builds `acpwire.ReviewParams{Focus: …, Model: …, Range: …, Base: …}` and calls `m.startSessionRun("Cannot start review", …)` with a run that calls `sess.Review`, capturing `sess := m.sess`. Its `BeginWork`, its registration and the `base:` encoding go: the run path and `RunReview` own them.
2. **`model.go`, Run-now:**
   - **`runNowDispatch`** (Contracts) returns nil for an empty name, and otherwise a command that returns `runAgentMsg{agent: agentName, goal: goal}`;
   - **`openAgentsRoster`** passes `runNowDispatch` to the roster in place of its closure;
   - **`agents/panel.go`** gains `RunNow` (Contracts). The Run-now field's action (`agents/panel.go:509`) returns `p.RunNow(name)`, and its comment stops promising an inline goal prompt;
   - `update`'s panel-message switch (`model.go:2465`) gains a `runAgentMsg` case, after `castlist.CancelMsg`. While busy it returns no command. When `!m.sess.Status().AgentReady`, it refreshes and returns no command. Otherwise it calls `m.startSessionRun("Cannot start work", …)` with a run that calls `sess.RunAgent(ctx, acpwire.AgentRunParams{Agent: …, Goal: …})`;
   - delete `buildCustomAgentRunner`, the `customAgentFactory` field, `WithCustomAgentRunnerFactory` and `CustomAgentRunnerFactory`.
3. **`model.go`, the suggestion:**
   - **`computeSuggestion`** asks for the fallback when the mode is "llm" and `m.sess.Status().AgentReady`, in place of `m.suggestionProvider != nil`;
   - **`runSuggestionFallbackCmd`** takes an `acpclient.Session` in place of the provider, and calls `Suggest` under the same 10-second timeout;
   - delete the `suggestionProvider` field and `WithSuggestionProvider`.
4. **`model.go`, the proposal:**
   - **`startVerifyProposal`** reads `sess, ctx := m.sess, m.ctx`, and its command calls `sess.VerifyProposal(ctx)`, returning `verifyProposeMsg` with the error unchanged, or with the build and test;
   - delete `proposalFromSession`, the `subagentFactory` field, `WithSubagentFactory`, `SubagentRunnerFactory`, and the `jsonextract` import.
5. **`model.go`, `/review`'s dispatcher:** delete `runReviewCmd`, the `reviewDispatcher` field, `WithReviewDispatcher`, `SessionSwapResult.ReviewDispatcher` and its assignment in the new-session effect (`commands_dispatch.go:225`).
6. **`app/app.go`:** delete the `WithCustomAgentRunnerFactory`, `WithSubagentFactory`, `WithReviewDispatcher` and `WithSuggestionProvider` options, and the swap result's `ReviewDispatcher`. Also delete `beginSessionStop` (`app.go:1510`) with its comment: the two review dispatchers were its only callers, and the run path now registers the review's stop group.
7. **Tests:**
   - **`model_test.go`, the review tests.** `TestDispatchReviewCommandWhileBusyIsRefused` and `TestDispatchReviewCommandStartsSubagent` build with `testModelWith(inproc.Deps{State: …, Runner: runnerOf(&testAgentRunner{}), Review: …}, …)`, where `Review` sends `params.Focus` where the dispatcher sent its focus. `TestDispatchReviewCommandNilDispatcherShowsError` builds without a runner, and keeps its assertions;
   - **`model_test.go`, the suggestion tests.** `withSuggester` (Contracts) builds its session over `inproc.Deps{State: m.state, Runner: runnerOf(&testAgentRunner{}), Suggest: suggest}`. The eight tests that assign `m.suggestionProvider` call `m = withSuggester(m, fp.Provide)` instead. `TestSuggestionNoProviderNoCall` drops its `m.suggestionProvider = nil` line: its model has no runner;
   - **`model_test.go`.** Delete `TestBuildCustomAgentRunnerWithFactory`, `TestBuildCustomAgentRunnerUnknownAgentSurfacesError` and `TestBuildCustomAgentRunnerFactoryError`. `app`'s `TestRunCustomAgentReportsABuildError` covers the error;
   - **`verifygate_test.go`.** `TestProposalPersistsOnApprove` builds with `testModelWith(inproc.Deps{State: state, VerifyProposal: …}, WithCommandRegistry(reg))`, where `VerifyProposal` returns "make build" and "make test". Delete `proposalRunner` and `TestProposalFromSession`, which `app`'s `TestVerifyCommandsFromTakesTheNewestObject` replaces;
   - **new, in `turns_seam_test.go`.** `turnRecorder`'s `Review`, `RunAgent`, `Suggest` and `VerifyProposal` record, then call the wrapped session's:
     - **`TestReviewGoesThroughTheSession`.** Over `Deps` with a runner and a `Review` that returns nil, `/review --base main src` records `{Focus: "src", Base: "main"}`, and the model is not busy once the commands are drained;
     - **`TestReviewRefusesABaseAndARangeBeforeStarting`.** `/review --base=main main...HEAD focus` records no review, leaves the model idle, and writes the "mutually exclusive" note;
     - **`TestRunNowStartsOnTheLiveModel`.** Over `Deps` with a runner and a `RunAgent` that returns nil, open the roster the way `Update` does: call `tuiCommandEffects["agents"]` with the address of a copy of the model, and make the model it returns (through `asModel`) the live one. Take the `*agents.Panel` from the live model's dock, call `RunNow("linter")`, run the command, and pass its message to the live model's `Update`. The model `Update` returns is busy and has an `agentCancel`, and draining records `{Agent: "linter"}`. This is decision 10's regression test: before this task the run started on the copy, and the live model stayed idle;
     - **`TestRunNowWhileBusyStartsNothing`.** With `busy` set, the `runAgentMsg` records no run and returns no command;
     - **`TestTheSuggestionGoesThroughTheSession`.** In "llm" mode, over `Deps` with a runner and a `Suggest` that returns "yes, go ahead", a finished turn whose last assistant message is "I've finished the refactor and all tests pass." records that message as the suggestion request, and the model's suggestion is "yes, go ahead" after the result arrives;
     - **`TestTheProposalGoesThroughTheSession`.** As `TestProposalPersistsOnApprove` drives it, pressing `f` records one proposal.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'WithReviewDispatcher|WithSuggestionProvider|WithSubagentFactory|WithCustomAgentRunnerFactory|ReviewDispatcher|reviewDispatcher|suggestionProvider|subagentFactory|customAgentFactory|buildCustomAgentRunner|proposalFromSession|runReviewCmd|CustomAgentRunnerFactory|SubagentRunnerFactory' internal/app/tui --include='*.go'
grep -nE 'tui\.With(ReviewDispatcher|SuggestionProvider|SubagentFactory|CustomAgentRunnerFactory)|ReviewDispatcher:|beginSessionStop' internal/app/*.go
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/tui/ -run 'ThroughTheSession|Review|RunNow|Suggestion|Proposal|VerifyGate|Roster|Agents' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/tui/...
TMPDIR=/private/tmp/p2-5c-suite go test ./internal/app/ -run 'Review|CustomAgent|VerifyCommands|ProposeVerify|SendNow|Adapter|BindTurnDeps' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
go vet ./internal/app/...
```

Expected: both greps print nothing. Every listed test passes, and `tui` passes, the goldens included.

## Task 5: Verification and docs

**Goal:** the full suite is green, and the spec and design record plan 5c.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 5c entry to the Implementation record, after plan 5b's. It lists:
   - the `acpwire` types, `CodeNoRunner` and `AgentReady`;
   - the six `Session` methods and their `Methods` rows: two on existing ACP methods, four new;
   - the adapter's run path: one slot, `BeginWork`, the stop group, the idle reset, and `Stop` acting on the slot only;
   - the runtime's five calls in `Deps` through `bindTurnDeps`, and `runSubagentTask` shared by the review and the custom-agent run;
   - that the TUI starts prompts, reviews and Run-now through the session, that the six options and the swap result's `Runner` and `ReviewDispatcher` are gone, and that swarm, SDD and plan-authoring runs keep the TUI's path until plan 5d;
   - the two fixes, Run-now and the verify proposal, and the Run-now note that changed;
   - that T1b and the allowlist did not change, and why.
2. **`design.md` §7.** In the P2 row, "3d, 4, 5a and 5b implemented" becomes "3d, 4, 5a, 5b and 5c implemented".
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-5c-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-5c-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `90488446`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** At `90488446`, 116 packages passed and `web/bridge` hung as the Assumptions describe. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository and check by eye:
- send a prompt: the turn runs, the spinner shows, and the session goes idle at the end;
- while a turn runs, press Ctrl+C twice: the turn stops with "Agent turn cancelled.";
- queue a message during a turn's final answer: it starts as the next turn;
- `/review`: a reviewer card appears, and its summary lands in the transcript;
- `/agents`, pick a custom agent, Run now: an `agent · <name>` card appears, the session is busy, and Ctrl+C stops it;
- `/sdd` on a plan in a repository with no build manifest, then `f`: the proposed commands come from the proposal subagent.

## Integration notes

- **Plan 5d (pipelines)** moves the swarm, SDD and plan-authoring runs onto the run path. It deletes `startAgentRun` with its synchronous `BeginWork` refusal, `runAgentCmd`, the TUI's stop registration (`beginTurnStopRegistration`, `releaseTurnStop`, `stopTurnWork`, `endPendingStop`) and `handlePlanAuthorFinished`'s idle reset. T1b can then take `BeginWork`, `EndWork`, `BeginTurnStop`, `EndTurnStop`, `RequestTurnStop` and `SetActivity`.
- **Plan 5e (ending and swapping)** replaces `finishQuit`'s context cancel with `Close`, and moves the exit analysis off `rt.Runner`.
- **Plan 6** moves the refusal, failure and cancellation notes, the provider notice and the no-runner echo of the user's text.
- **The Run-now goal prompt** that the roster's comment promises was never built. It is a follow-up, not part of P2.
- **P3:**
  - the client sends `session/prompt` with the text as one content block, and `session/cancel`. A stop the user asks for before the turn exists must reach it once it does: in process the cancelled context covers that (decision 4), and P3's client sends `session/cancel` when the turn starts;
  - it adds `session/review`, `session/agent_run`, `session/suggest` and `session/verify_proposal`;
  - `Stack`'s `busy` parameter stays until P3 drops it, and `RefreshSheet` stays a client call at a turn's end: `Prompt`'s return is that boundary.

## Self-review

- **Every task verifiable alone.** Task 1 adds calls nothing uses. Task 2 adds the runtime's side and binds it, while the TUI keeps its options. Task 3 moves prompt turns and the stop, and Task 4 the other runs and the two queries. Each keeps T1, T1b and the goldens green.
- **Cold executor.** Each step names the function, what it calls after the change, and what it does with the result. The run path's order and `Stop`'s two cases are spelled out. Each new test names its setup and the behaviour it pins.
- **Contracts.** The wire types and codes, the six `Session` methods and their rows, `Deps`' fields, the adapter's slot, helpers and errors, the runtime's methods, the TUI's new functions and message, and the test helpers are declared there.
- **Anchors.** Every path, symbol and line was checked at `90488446`.
- **Spec.** C5, C6, C8 and the Migration order are updated, with the revision record "2026-11-03, against `90488446` (plans 5a and 5b merged)" holding decisions 1 to 12 and 15.
- **No placeholders.**
