# P2 plan 5d: Pipelines — implementation plan

Date: 2026-11-03. Status: ready for review.
Base: `main` at `cc07ed11` (plan 5c merged). Every anchor below was checked there.
- The JSON literals in Contracts were produced by encoding the declared types, and the declarations pass T2 (`TestWireFieldsFollowTheRules`).
- Three of this plan's changes fix bugs found while writing it (decisions 9, 10 and 11). Decision 9 and the first half of decision 11 were confirmed by throwaway tests. Decision 10 and the rest of decision 11 were confirmed by reading the code.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C8 (turns and runs), C11 (project data), Migration order item 5d, its revision record entry "2026-11-03, against `cc07ed11` (plan 5c merged)", and tests T1, T1b, T2 and T4.
Previous plan: [plan 5c, prompt](2026-11-03-p2-5c-prompt-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI starts swarm runs, plan runs and plan authoring through the session, answers and stops the SDD human gate through it, and reads a plan's preflight from it. Every run the TUI starts now takes 5c's run path, so the TUI's own stop registration goes.
- `acpwire` gains the pipeline types: `SwarmParams`, `SDDParams`, `PlanPreflightParams`, `PlanPreflight`, `VerifyGate`, `PlanInspection`, `PlanTask`, `PlanDiagnostic`, `AuthorPlanParams`, `AuthoredPlan`, and the strategy and severity constants.
- `acpclient.Session` gains `StartSwarm`, `PlanPreflight`, `StartSDD`, `AnswerSDDGate`, `StopSDD` and `AuthorPlan`.
- `inproc` gains `PlanRun`, the four calls' `Deps`, and the paused plan run. The runtime gains `RunSwarm`, `NewPlanRun`, `PlanPreflight` and `AuthorPlan`.
- The TUI options `WithSwarmRunner`, `WithPipelineFactory`, `WithSwarmOverrideFactory` and `WithPlanAuthorFactory` are deleted, and `SessionSwapResult` keeps only `State`, `Session` and `ToolRegistry`.
- Three bugs are fixed (decisions 9 to 11).

**Non-goals:**
- **No ending or swap.** `finishQuit` keeps cancelling the turn's context, and `SessionSwapper` and `EndController` stay, until plan 5e.
- **No plan files.** The plan picker and the preflight's task count keep calling `sddplans.Discover`, Discard keeps calling `sddauthor.RemoveCandidate`, and `/sdd` scaffolding stays, until plan 8 (C11).
- **No message queries.** `--from-last-plan` keeps reading the last approved plan from the state (`lastFinalAssistantPlan`) until plan 8.
- **No config.** The preflight keeps reading the config it renders, and the confirm keeps persisting proposed verify commands with `config.SaveVerifyCommands`, until plan 7.
- **No notes or notices.** The refusal, failure, cancellation and gate notes stay TUI writes until plan 6.
- **No ACP change** beyond decision 9's fix, which turns `session/sdd_start`'s "internal error: handler panicked" for an unbuildable plan into its intended "could not build a runner" error. The new and extended ACP methods are P3's.
- **No web change.**
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-5d-suite`. Create the directory first: `mkdir -p /private/tmp/p2-5d-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **The base is green.** At `cc07ed11`, all 117 packages passed, `web/bridge` included. `web/bridge`'s `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` has hung about one run in three since `90488446`. The hang is in its fake agent (`fakeagent_test.go`), not in product code, so treat a hang there as known.
- **T6's goldens never change.** No task runs `TestTranscriptGoldens` with `-update`. Every TUI task's Verify runs the `tui` package, which runs the goldens.
- **The runtime builds the swarm orchestrator, the pipeline factory, the swarm override factory and the plan author factory with the runner,** in `buildAgentRunnerWithLock` (`app.go:1442`, `1448`, `1488`, `1491`). They are nil exactly when `rt.Runner` is. `NewSession` and the reload replace them under `rt.mu` (`app.go:3179` to `3192`), and the reload copies a new orchestrator into the existing one rather than replacing the pointer.
- **The TUI's copies went stale.** `Run` passes the four to the TUI only when the provider was ready at startup (`app.go:2852` to `2859`), and the reload never re-passes them. The new-session effect re-reads them from the swap result (`commands_dispatch.go:207` to `218`). So after a `/connect` recovery the TUI had none until `/new`, and the pipeline and author factories it held kept the config the runtime started with.
- **A plan run's controller resolves its verify commands once,** when the factory builds it (`app.go:1885`, through `pipeline.DefaultVerifier`). With no command resolved, the gate is skipped (`verify.go:162`).
- **`pipeline.InspectPlan` computes `StrictBlocked` only when the requested strategy is strict.** Checked by a throwaway test: a plan with one `marshal.agent` block reports `StrictBlocked` true when inspected as strict and false as auto.
- **`Update` has a value receiver** (`model.go:1785`). Every closure this plan adds captures the session value and its arguments, never `m`.

**Decisions this plan makes beyond the spec** (decisions 1 to 3, 5 to 11, 13, 14, 16 and 18 are also recorded in the spec's revision record):

1. **Swarm, plan and authoring runs take the run path.** `StartSwarm`, `StartSDD`, `AnswerSDDGate` and `AuthorPlan` take 5c's slot through `runTurn`. They get one run at a time, `BeginWork`, the stop group, the idle reset and the coded refusals, and a stopped run is not an error. The TUI starts the first three through `startSessionRun` and authoring through its own command, because authoring returns a result. `startAgentRun`, `runAgentCmd` and the TUI's stop registration (`beginTurnStopRegistration`, `releaseTurnStop`, `stopTurnWork`, `endPendingStop`) go. `cancelTurn` becomes `Stop`, then the local cancel.
2. **Per-run choices travel as parameters and apply agent side.** The cast list's role overrides (keyed by role name), the strategy and the proposed verify commands are fields of `SwarmParams` and `SDDParams`. `RunSwarm` installs the override factory for one run and restores it when the run returns, whatever its outcome. `NewPlanRun` builds the controller with the overrides, sets its strategy, and replaces its verify commands when the params carry some. The TUI's `restoreRunner`, `pipelineRunner` and its `RoleOverrideRunner` assertion go. A swarm override no longer outlives a refused start: before, a `BeginWork` refusal left it installed until the next run ended.
3. **A plan run is built when it starts, not at dispatch.** `/sdd <plan>` asks the session for `PlanPreflight`: the plan's inspection (`pipeline.InspectPlan`, which writes nothing) and the verify gate a run would use. A plan the inspection rejects is refused with "Cannot start plan run: …", the note the factory wrote before. That now covers a plan that fails to compile, which used to open a preflight with no strategy choice and fail when started. `.marshal/pipeline/<slug>` is created when the run starts, not when the preflight opens.
4. **The preflight is a synchronous call,** made from `Update` as the factory and its inspection were. P3 makes it asynchronous (Integration notes).
5. **The human gate is a coded error.** A gated `StartSDD` or `AnswerSDDGate` returns `CodeHumanGateRequired`, which plan 1 defined for this, and the TUI opens the gate panel on that code. The session keeps the paused run: a swap drops it with the session, and a new `StartSDD` replaces it. `AnswerSDDGate` records the answer as the user's message and resumes the run. An answer refused at admission leaves the run paused, where today the gate was cleared before the refusal. `StopSDD` drops the paused run and clears the gate. The answer's user message lands when the resumed run starts, a moment after the panel closes.
6. **Authoring returns its candidate.** `AuthorPlan` resolves the draft path with the session's clock, builds the author and runs it, all agent side, and returns `AuthoredPlan`: the path, whether it was auto-accepted, and the inspection, with `StopReasonCancelled` when stopped. An empty `Design` means the goal is the design. The review panel renders the wire type. The TUI keeps choosing the design: the goal, or the last approved plan for `--from-last-plan`.
7. **The runtime's pipeline calls move into `Deps`.** `bindTurnDeps` binds `Swarm`, `NewPlanRun`, `PlanPreflight` and `AuthorPlan` to runtime methods that read the current orchestrator and factories under `rt.mu`. Two staleness bugs go with it: `/swarm`, `/sdd` and `/sdd new` now work as soon as a `/connect` recovery builds the runner, and `/sdd` and `/sdd new` pick up config a reload applied (verify commands, fix rounds, role routing).
8. **`Status().AgentReady` gates the three commands,** in place of the TUI's nil checks on its orchestrator and factories, with the same notes. Production builds all of them with the runner.
9. **A plan the factory cannot build no longer crashes the TUI.** This fixes a bug. `buildPipelineController` returns a nil `*pipeline.ControllerAdapter` on failure, after writing "Cannot start plan run: …". The factory closure (`app.go:1488`) returned it as an interface, which is then not nil. So the TUI's `runner == nil` check (`commands_dispatch.go:434`) missed it, and `adapter.Controller()` dereferenced nil. A throwaway test confirmed it: `/sdd missing.md` writes the note and then panics. ACP's `sdd_start` has the same check and answered "internal error: handler panicked". The closure now returns nil, and `NewPlanRun` reports a plan it cannot build as an error.
10. **Proposed verify commands now reach the run.** This fixes a bug. Pressing `f` in the preflight proposes commands, and the note says "Press Enter to run with them". The confirm persists them to the project config. But the controller had been built at dispatch from the config the runtime started with, and nothing reloads after the save, so the run kept its old commands. In a repository with no manifest that meant no gate at all. `SDDParams.Verify` carries the proposal, and `NewPlanRun` sets it on the controller.
11. **A stopped plan authoring is not a failure.** This fixes a bug. `handlePlanAuthorFinished` never cleared `cancelling`. A stop therefore wrote "✘ Plan authoring failed: context canceled" with an error notice. The next normal turn then reported "Agent turn cancelled." and cleared the steering queue. A throwaway test confirmed both. By reading `model.go:2011`: while `cancelling` stayed set, Ctrl+C on a running turn armed quit instead of stop. Now a stopped authoring writes "Agent turn cancelled.", sets no notice and clears `cancelling`, as `handleAgentFinished` does.
12. **Authoring starts through `beginTurn`,** so it also clears a stale provider notice and the ghost suggestion, as every other run start does.
13. **Two authoring notes change.** "Cannot resolve plan path: …" and "Cannot start plan authoring: …" become failed authorings, "✘ Plan authoring failed: resolve plan path: …" and "✘ Plan authoring failed: start plan authoring: …", with an error notice, because both now happen inside the run. A run whose controller cannot be built after its preflight passed (a failed `mkdir`) reports "✘ Turn failed: run plan: cannot build a runner for …" after the factory's own note.
14. **The wire carries what the TUI renders, and no more.** `PlanInspection` holds the effective strategy, whether the plan has blocks, whether strict is blocked, the operation totals, each task's counts, and the diagnostics. It does not hold the plan's IR or text. Role overrides are keyed by the role's name (`routing.AgentRole`'s string).
15. **T1b gains seven names and the allowlist loses seven entries.** T1b: `ClearSDDGate` (Task 3), and `BeginWork`, `EndWork`, `BeginTurnStop`, `EndTurnStop`, `RequestTurnStop` and `SetActivity` (Task 4), all at plan `5d`. The allowlist: `model.go` loses `marshal/internal/agent/swarm` and `marshal/internal/pipeline`; `commands_dispatch.go` and `sdd_command.go` lose `marshal/internal/pipeline` (Task 3); `sdd_authoring.go` and `sddreview/review.go` leave the map (Task 4). `model.go` keeps `sddplans` and `sddauthor` for plan 8 and `config.SaveVerifyCommands` for plan 7.
16. **The new ACP methods are named** `session/sdd_preflight`, `session/sdd_stop` and `session/sdd_author`. `StartSwarm`, `StartSDD` and `AnswerSDDGate` map to `session/swarm_start`, `session/sdd_start` and `session/sdd_answer`. P3 extends the first two with the overrides, the strategy and the verify commands.
17. **The runtime owns its runner types.** `app.PipelineRunner` and `app.SwarmOverrideFactory` replace `tui.AgentRunner` and `tui.SwarmOverrideFactory` in the runtime's fields and builders. ACP's host keeps converting the factory's result to its own `AgentRunner`, which has the same methods.
18. **The spec's plan count is corrected.** Plan 5c's review changed the header to "five plans … (twelve plan documents in all)" and the design row to match. The Migration order lists nine plans, and 5d, 5e and 6 to 9 are still to come. The spec edits that ship with this plan fix the header, and Task 5 fixes the design row.

**Execution:**
- Branch first: `git switch -c p2-5d-pipelines` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-5d-pipelines: record the plan 5d revisions and plan 5d`. The main checkout holds other untracked files (`docs/fleet-cli/` and others). Leave them alone.
- Run the tasks in order. Run each task's Verify and commit it as `p2-5d-pipelines: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1.** `pipelines.go` (new):

```go
// The plan-run strategies (C8). An empty strategy lets the plan choose:
// adaptive when it has executable blocks, agent otherwise.
const (
	StrategyAgent    = "agent"
	StrategyAdaptive = "adaptive"
	StrategyStrict   = "strict"
)

// SwarmParams is one swarm run: its goal, and the role overrides for this run
// only.
type SwarmParams struct {
	Goal      string            `json:"goal"`
	Overrides map[string]string `json:"overrides,omitempty"` // role name -> model preset
}

// SDDParams is one plan run: the plan, its strategy, the role overrides for
// this run only, and the verify commands that replace the configured ones for
// this run. Nil Verify keeps the configured commands.
type SDDParams struct {
	PlanPath  string            `json:"planPath"`
	Strategy  string            `json:"strategy,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"` // role name -> model preset
	Verify    *VerifyCommands   `json:"verify,omitempty"`
}

// PlanPreflightParams names the plan a preflight inspects and the strategy
// asked for; an empty strategy lets the plan choose.
type PlanPreflightParams struct {
	PlanPath string `json:"planPath"`
	Strategy string `json:"strategy,omitempty"`
}

// PlanPreflight is what a plan run's preflight shows from the agent side: the
// plan's inspection and the verify gate the run would use.
type PlanPreflight struct {
	Inspection PlanInspection `json:"inspection"`
	Verify     VerifyGate     `json:"verify"`
}

// VerifyGate is the build and test commands a plan run's verify gate runs.
type VerifyGate struct {
	Build    string `json:"build,omitempty"`
	Test     string `json:"test,omitempty"`
	Known    bool   `json:"known,omitempty"`    // a command was resolved
	Detected bool   `json:"detected,omitempty"` // found in the repository's manifest, not configured
}

// PlanInspection is a plan's parse, compile and preflight result.
type PlanInspection struct {
	Strategy      string           `json:"strategy"` // the effective strategy
	HasBlocks     bool             `json:"hasBlocks,omitempty"`
	StrictBlocked bool             `json:"strictBlocked,omitempty"`
	DetOps        int              `json:"detOps,omitempty"`
	AgentOps      int              `json:"agentOps,omitempty"`
	EstCalls      int              `json:"estCalls,omitempty"`
	Tasks         []PlanTask       `json:"tasks,omitempty"`
	Diagnostics   []PlanDiagnostic `json:"diagnostics,omitempty"`
}

// PlanTask is one task's compile report.
type PlanTask struct {
	TaskN    int    `json:"taskN"`
	Title    string `json:"title"`
	DetOps   int    `json:"detOps,omitempty"`
	AgentOps int    `json:"agentOps,omitempty"`
	EstCalls int    `json:"estCalls,omitempty"`
}

// PlanDiagnostic is one plan-authoring error or warning.
type PlanDiagnostic struct {
	TaskN    int    `json:"taskN,omitempty"` // 0 for the plan as a whole
	Severity string `json:"severity"`        // DiagnosticError or DiagnosticWarning
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
}

// The diagnostic severities.
const (
	DiagnosticError   = "error"
	DiagnosticWarning = "warning"
)

// AuthorPlanParams asks for one plan authored from a design.
type AuthorPlanParams struct {
	Goal   string `json:"goal"`
	Design string `json:"design,omitempty"` // the design to convert; the goal when empty
}

// AuthoredPlan is the candidate plan an authoring run wrote, for review.
type AuthoredPlan struct {
	StopReason   string          `json:"stopReason"`
	PlanPath     string          `json:"planPath,omitempty"`
	AutoAccepted bool            `json:"autoAccepted,omitempty"`
	Inspection   *PlanInspection `json:"inspection,omitempty"` // nil when the candidate has none
}
```

The JSON `TestPipelinesJSON` pins:
- `SwarmParams{Goal: "add a regression test"}`: `{"goal":"add a regression test"}`;
- `SwarmParams{Goal: "add a regression test", Overrides: map[string]string{"planner": "local/qwen"}}`: `{"goal":"add a regression test","overrides":{"planner":"local/qwen"}}`;
- `SDDParams{PlanPath: "docs/plans/p.md", Strategy: StrategyAdaptive, Overrides: map[string]string{"sdd_implementer": "kimi/k3"}, Verify: &VerifyCommands{Build: "make build", Test: "make test"}}`: `{"planPath":"docs/plans/p.md","strategy":"adaptive","overrides":{"sdd_implementer":"kimi/k3"},"verify":{"build":"make build","test":"make test"}}`;
- `SDDParams{PlanPath: "docs/plans/p.md"}`: `{"planPath":"docs/plans/p.md"}`;
- `PlanPreflightParams{PlanPath: "docs/plans/p.md", Strategy: StrategyStrict}`: `{"planPath":"docs/plans/p.md","strategy":"strict"}`;
- `PlanPreflight{Inspection: PlanInspection{Strategy: StrategyAdaptive, HasBlocks: true, DetOps: 1, Tasks: []PlanTask{{TaskN: 1, Title: "Add file", DetOps: 1}}}, Verify: VerifyGate{Build: "go build ./...", Test: "go test ./...", Known: true, Detected: true}}`: `{"inspection":{"strategy":"adaptive","hasBlocks":true,"detOps":1,"tasks":[{"taskN":1,"title":"Add file","detOps":1}]},"verify":{"build":"go build ./...","test":"go test ./...","known":true,"detected":true}}`;
- `PlanPreflight{Inspection: PlanInspection{Strategy: StrategyAgent}}`: `{"inspection":{"strategy":"agent"},"verify":{}}`;
- `PlanDiagnostic{TaskN: 2, Severity: DiagnosticError, Message: "unknown block", Line: 12}`: `{"taskN":2,"severity":"error","message":"unknown block","line":12}`;
- `AuthorPlanParams{Goal: "add retries"}`: `{"goal":"add retries"}`;
- `AuthorPlanParams{Goal: "Convert the approved plan into an executable SDD plan.", Design: "approved plan content"}`: `{"goal":"Convert the approved plan into an executable SDD plan.","design":"approved plan content"}`;
- `AuthoredPlan{StopReason: StopReasonEndTurn, PlanPath: "/repo/.marshal/plans/add-retries.md", Inspection: &PlanInspection{Strategy: StrategyAgent}}`: `{"stopReason":"end_turn","planPath":"/repo/.marshal/plans/add-retries.md","inspection":{"strategy":"agent"}}`;
- `AuthoredPlan{StopReason: StopReasonCancelled}`: `{"stopReason":"cancelled"}`.

**`internal/acpclient`, after Task 1.** `Session` gains, at the end of the interface, in this order:

```go
	// StartSwarm runs a swarm over params.Goal, with params' role overrides
	// for this run only, and returns when it ends. It starts, refuses and stops
	// as Prompt does; a stopped run returns no error.
	//
	// ACP: session/swarm_start (exists).
	StartSwarm(ctx context.Context, params acpwire.SwarmParams) error

	// PlanPreflight inspects the plan a run would execute and resolves the
	// verify gate it would use, without running or writing anything.
	//
	// ACP: session/sdd_preflight (new in P3).
	PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)

	// StartSDD runs the plan at params.PlanPath with params' strategy, role
	// overrides and verify commands, and returns when it ends. It starts,
	// refuses and stops as Prompt does; a stopped run returns no error. A run
	// its human gate pauses returns CodeHumanGateRequired: the question is in
	// Status, and AnswerSDDGate resumes the run.
	//
	// ACP: session/sdd_start (exists).
	StartSDD(ctx context.Context, params acpwire.SDDParams) error

	// AnswerSDDGate records answer as the user's message and resumes the plan
	// run paused at its human gate, and returns when that run ends again, as
	// StartSDD does.
	//
	// ACP: session/sdd_answer (exists).
	AnswerSDDGate(ctx context.Context, answer string) error

	// StopSDD abandons the plan run paused at its human gate: the question
	// clears and the run can no longer be answered.
	//
	// ACP: session/sdd_stop (new in P3).
	StopSDD(ctx context.Context) error

	// AuthorPlan runs the plan author over params and returns the candidate it
	// wrote, for review. It starts, refuses and stops as Prompt does; a stopped
	// run returns StopReasonCancelled and no error.
	//
	// ACP: session/sdd_author (new in P3).
	AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)
```

`Methods` gains these rows at the end, in the same order:

| Name | ACP | Exists |
|---|---|---|
| `StartSwarm` | `session/swarm_start` | true |
| `PlanPreflight` | `session/sdd_preflight` | false |
| `StartSDD` | `session/sdd_start` | true |
| `AnswerSDDGate` | `session/sdd_answer` | true |
| `StopSDD` | `session/sdd_stop` | false |
| `AuthorPlan` | `session/sdd_author` | false |

**`internal/acpclient/inproc`, after Task 1.**

```go
// inproc.go: Deps gains, at the end:

	// Swarm, NewPlanRun, PlanPreflight and AuthorPlan are the runtime's
	// pipeline calls. Each reads the runtime's current orchestrator or
	// factory, so the same Deps keep working after a swap and a reload.
	// Without one, the matching Session call refuses with CodeNoRunner.
	Swarm         func(ctx context.Context, params acpwire.SwarmParams) error
	NewPlanRun    func(params acpwire.SDDParams) (PlanRun, error)
	PlanPreflight func(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)
	AuthorPlan    func(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)

// inproc.go: Session gains, after verifyProposal:

	swarm         func(context.Context, acpwire.SwarmParams) error
	newPlanRun    func(acpwire.SDDParams) (PlanRun, error)
	planPreflight func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)
	authorPlan    func(context.Context, acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)
	// paused is the plan run its human gate paused, nil when none. It is
	// guarded by mu.
	paused PlanRun

// pipelines.go (new)

// PlanRun is one plan-execution run. Run runs it, or resumes it after Answer.
// A run its human gate pauses returns an error carrying CodeHumanGateRequired.
type PlanRun interface {
	Run(ctx context.Context) error
	Answer(answer string)
}

func (s *Session) StartSwarm(ctx context.Context, params acpwire.SwarmParams) error
func (s *Session) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)
func (s *Session) StartSDD(ctx context.Context, params acpwire.SDDParams) error
func (s *Session) AnswerSDDGate(ctx context.Context, answer string) error
func (s *Session) StopSDD(ctx context.Context) error
func (s *Session) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)

// settlePlan records how run ended: a run its gate paused becomes the
// session's paused run.
func (s *Session) settlePlan(run PlanRun, err error)

// checkStrategy returns an error naming strategy unless it is empty or one of
// the three strategies.
func checkStrategy(strategy string) error

var (
	errNoSwarm      = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no swarm runner"}
	errNoPlanRunner = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no plan runner"}
	errNoPreflight  = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no plan preflight"}
	errNoPlanAuthor = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no plan author"}
	errNoGoal       = errors.New("inproc: a run needs a goal")
	errNoPlanPath   = errors.New("inproc: a plan run needs a plan path")
	errNoAnswer     = errors.New("inproc: the gate needs an answer")
	errNoPausedPlan = errors.New("inproc: no plan run is waiting on an answer")
)
```

`checkStrategy`'s error is `fmt.Errorf("inproc: unknown strategy %q (want agent, adaptive or strict)", strategy)`.

**`internal/app`, after Task 2.** `pipelines.go` (new):

```go
// PipelineRunner is a swarm or plan-execution runner. *swarm.Orchestrator and
// *pipeline.ControllerAdapter satisfy it, and ACP's AgentRunner has the same
// methods.
type PipelineRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
	AnswerGate(answer string)
}

// SwarmOverrideFactory builds a per-run swarm.RunnerFactory with role->preset
// overrides applied.
type SwarmOverrideFactory func(overrides map[routing.AgentRole]string) swarm.RunnerFactory

// RunSwarm runs the runtime's swarm over params.Goal, with params' role
// overrides installed for this run only.
func (rt *Runtime) RunSwarm(ctx context.Context, params acpwire.SwarmParams) error

// NewPlanRun builds the plan run params names over the runtime's current
// pipeline factory. It does not run it.
func (rt *Runtime) NewPlanRun(params acpwire.SDDParams) (inproc.PlanRun, error)

// PlanPreflight inspects the plan params names against the runtime's working
// directory and resolves the verify gate a run would use. It writes nothing.
func (rt *Runtime) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)

// AuthorPlan resolves the draft path for params.Goal, runs the runtime's plan
// author over it, and returns the candidate.
func (rt *Runtime) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)

// planRun is one plan-execution run (inproc.PlanRun).
type planRun struct {
	runner   PipelineRunner
	planPath string
}

func (r *planRun) Run(ctx context.Context) error
func (r *planRun) Answer(answer string)

// roleOverrides converts the wire's role-name overrides; nil when empty.
func roleOverrides(names map[string]string) map[routing.AgentRole]string

// planInspectionOf converts an inspection to the wire; nil for nil.
func planInspectionOf(insp *pipeline.Inspection) *acpwire.PlanInspection

// verifyGateOf resolves the verify gate for repoRoot from the configured
// commands.
func verifyGateOf(repoRoot string, configured config.SDDVerifyConfig) acpwire.VerifyGate

// authoredPlanOf converts an authoring result to the wire, without a stop
// reason.
func authoredPlanOf(result sddauthor.Result) acpwire.AuthoredPlan
```

The runtime's error texts: `RunSwarm` without an orchestrator returns `&acpwire.Error{Code: acpwire.CodeNoRunner, Message: "run swarm: no swarm runner"}`. `NewPlanRun` without a factory returns `&acpwire.Error{Code: acpwire.CodeNoRunner, Message: "run plan: no plan runner"}`, and for a factory that builds nothing `fmt.Errorf("run plan: cannot build a runner for %s", params.PlanPath)`. `AuthorPlan` without a factory returns `&acpwire.Error{Code: acpwire.CodeNoRunner, Message: "author plan: no plan author"}`, and wraps its two other failures as `"resolve plan path: %w"` and `"start plan authoring: %w"`. `planRun.Run` reports a gate as `&acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: err.Error()}`.

**`internal/app/tui`, after Tasks 3 and 4.**

```go
// model.go

// pendingAgentRun captures the run waiting for the user to confirm it in the
// cast list.
type pendingAgentRun struct {
	goal string
	// kind is "sdd" or "swarm", set by openRunPreflight.
	kind string
	// verifyBuild/verifyTest hold commands proposed by the offer-to-fill
	// flow. When set, they are persisted on confirm and used for this run.
	verifyBuild string
	verifyTest  string
}

// openRunPreflight opens the cast list for kind ("sdd" or "swarm") and waits
// for the user to confirm. pf is the plan's preflight for "sdd", nil for
// "swarm".
func (m *Model) openRunPreflight(kind, goal string, pf *acpwire.PlanPreflight)

// verifyGateRow renders the /sdd preflight's verify-gate row from the gate
// the session resolved.
func verifyGateRow(v acpwire.VerifyGate) castlist.Row

// roleNames converts the cast list's per-run overrides to the wire's
// role-name keys; nil when empty.
func roleNames(overrides map[routing.AgentRole]string) map[string]string

// sdd_command.go: parsedSDDArgs.Strategy becomes a string, one of acpwire's
// three strategies or empty.

// sdd_authoring.go (Task 4)

// planAuthorFinishedMsg reports an authoring run's end. refusal is the note a
// refused start writes.
type planAuthorFinishedMsg struct {
	result  acpwire.AuthoredPlan
	err     error
	refusal string
}

// planAuthorCmd runs one authoring run through the session and reports its
// end.
func planAuthorCmd(ctx context.Context, sess acpclient.Session, params acpwire.AuthorPlanParams) tea.Cmd

// sddreview/review.go (Task 4)

// New returns a review panel for the given authored plan.
func New(result acpwire.AuthoredPlan) *Panel
```

**Test helpers.**

```go
// tui: turns_seam_test.go. turnRecorder gains, with methods that record
// and then call the wrapped session's:
	swarms     []acpwire.SwarmParams         // Task 3
	preflights []acpwire.PlanPreflightParams // Task 3
	sdds       []acpwire.SDDParams           // Task 3
	answers    []string                      // Task 3
	sddStops   int                           // Task 3
	authors    []acpwire.AuthorPlanParams    // Task 4

// tui: pipelines_seam_test.go (new, Task 3)

// fakePlanRun is a plan run whose first gates runs pause at the human gate,
// and which records each answer.
type fakePlanRun struct {
	mu      sync.Mutex
	gates   int
	runs    int
	answers []string
}

// preflightOf returns a Deps.PlanPreflight that always returns pf.
func preflightOf(pf acpwire.PlanPreflight) func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)

// planRunOf returns a Deps.NewPlanRun that appends each params to *got (when
// got is not nil) and returns run.
func planRunOf(run *fakePlanRun, got *[]acpwire.SDDParams) func(acpwire.SDDParams) (inproc.PlanRun, error)

// pausedGateModel builds a sized model whose session holds a plan run paused
// at its gate, with gate set in the state and the gate panel open.
func pausedGateModel(t *testing.T, gate session.SDDGate) (Model, *fakePlanRun, *turnRecorder)
```

## Task 1: The session takes the pipeline calls

**Goal:** the adapter runs swarm, plan and authoring runs on 5c's run path, keeps a plan run its gate paused, and forwards the preflight. Nothing calls it yet.

**Files:** `internal/acpwire/pipelines.go` (new), `pipelines_test.go` (new); `internal/acpclient/acpclient.go`, `methods.go`; `internal/acpclient/inproc/inproc.go`, `events.go`, `pipelines.go` (new), `pipelines_test.go` (new).

**Steps:**

1. **`acpwire`.** Add `pipelines.go` (Contracts).
2. **`acpclient.go`.** Add the six methods at the end of `Session`, with their Contracts doc comments. **`methods.go`:** add their rows at the end of `Methods`, with the Contracts' `Exists` column.
3. **`inproc/inproc.go`.** `Deps` and `Session` gain the Contracts' fields. **`inproc/events.go`:** `newSession` copies the four new `Deps` fields.
4. **`inproc/pipelines.go`** (new) holds `PlanRun`, the six methods, `settlePlan`, `checkStrategy` and the errors (Contracts). Every method first returns `errSessionClosed` on a closed session (`AuthorPlan` and `PlanPreflight` with a zero result).
   - **`StartSwarm`**: return `errNoSwarm` without `s.swarm`, and `errNoGoal` for a goal that is empty after trimming. Otherwise call `runTurn` with a run that calls `s.swarm(ctx, params)`. Return nil when stopped, and the error otherwise.
   - **`PlanPreflight`**: return `errNoPreflight` without `s.planPreflight`, `errNoPlanPath` for an empty path after trimming, and `checkStrategy`'s error. Otherwise return what `s.planPreflight` returns. It takes no slot.
   - **`StartSDD`**: return `errNoPlanRunner` without `s.newPlanRun`, `errNoPlanPath` for an empty path, and `checkStrategy`'s error, all before the slot. Otherwise call `runTurn` with a run that:
     1. builds the run with `s.newPlanRun(params)`, returning its error;
     2. under `s.mu`, sets `s.paused` to nil: a new run replaces a paused one;
     3. calls the run's `Run`, passes the result to `settlePlan`, and returns it.

     Return nil when stopped, and the error otherwise, `CodeHumanGateRequired` included.
   - **`AnswerSDDGate`**: return `errNoAnswer` for an answer that is empty after trimming. Under `s.mu`, read `s.paused`. Without one, return `errNoPausedPlan`. Otherwise call `runTurn` with a run that:
     1. under `s.mu`, returns `errNoPausedPlan` when `s.paused` no longer holds that run (`StopSDD` or a new run came first), and otherwise sets `s.paused` to nil;
     2. records the answer with `s.state.AddMessage(session.RoleUser, answer, session.ContentTypePlain)`;
     3. calls the run's `Answer` with the answer, then its `Run`, passes the result to `settlePlan`, and returns it.

     Return as `StartSDD` does. A refusal at admission never reaches step 1, so the run stays paused (decision 5).
   - **`StopSDD`**: under `s.mu`, set `s.paused` to nil. Then call `s.state.ClearSDDGate()` and return nil, whether or not a run was paused.
   - **`AuthorPlan`**: return `errNoPlanAuthor` without `s.authorPlan`, and `errNoGoal` for an empty goal. Otherwise call `runTurn` with a run that calls `s.authorPlan(ctx, params)` and keeps its result. When stopped, return `acpwire.AuthoredPlan{StopReason: acpwire.StopReasonCancelled}` and no error. On an error, return a zero result and the error. Otherwise return the kept result with `StopReason` set to `StopReasonEndTurn`.
   - **`settlePlan`**: when `acpclient.IsCode(err, acpwire.CodeHumanGateRequired)`, set `s.paused` to the run under `s.mu`. Otherwise do nothing.
5. **Tests:**
   - **`acpwire/pipelines_test.go`** (new): **`TestPipelinesJSON`** encodes the twelve values and compares the bytes with the Contracts literals, in the table style of `TestTurnsJSON`;
   - **`inproc/pipelines_test.go`** (new). Each test builds its state with `newState(t, "s1")`. A test fake, `fakePlanRun`, gates its first `gates` runs (returning `&acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}`) and then returns nil, records its answers and its run count, and can block until its context is done after signalling that it started:
     - **`TestStartSwarmRunsOnTheRunPath`**. `Deps.Swarm` records its params and, while it runs, sees `state.CurrentTurnStop()` set. `StartSwarm` with the goal "g" and the overrides `{"planner": "local/qwen"}` passes them unchanged and returns nil. An empty goal returns an error and calls nothing. Without `Deps.Swarm` it returns `CodeNoRunner`;
     - **`TestAStoppedSwarmIsNotAnError`**. A blocking `Deps.Swarm` that returns its context's error: `Stop` while it runs makes `StartSwarm` return nil;
     - **`TestStartSDDBuildsThePlanWhenItStarts`**. `Deps.NewPlanRun` records its params and returns the fake. `StartSDD` passes the params unchanged, runs the fake once and returns nil. An empty path, and the strategy "fast", each return an error before `Deps.NewPlanRun` is called. Without `Deps.NewPlanRun` it returns `CodeNoRunner`. With `Deps.NewPlanRun` failing with "boom", the error contains "boom" and `state.CurrentTurnStop()` is nil afterwards;
     - **`TestAGatedPlanWaitsForItsAnswer`**. The fake gates once. `StartSDD` returns an error with `CodeHumanGateRequired`. `AnswerSDDGate(ctx, "use 30s")` returns nil: the fake recorded "use 30s" and ran twice, and the last message is a user message "use 30s". A second `AnswerSDDGate` returns `errNoPausedPlan` and adds no message;
     - **`TestAGateCanPauseTheRunAgain`**. The fake gates twice. After `StartSDD` and one answer, both returning `CodeHumanGateRequired`, a second answer returns nil;
     - **`TestAnAnswerNeedsAPausedRunAndText`**. With nothing paused, `AnswerSDDGate(ctx, "x")` returns `errNoPausedPlan`. With a paused run, `AnswerSDDGate(ctx, "  ")` returns `errNoAnswer`. Neither adds a message;
     - **`TestStopSDDDropsThePausedRun`**. After a gated `StartSDD`, with `state.SetSDDGate` holding a question, `StopSDD` returns nil, `state.SDDGate().Question` is empty, and `AnswerSDDGate` returns `errNoPausedPlan`;
     - **`TestANewPlanRunReplacesAPausedOne`**. Run A gates. `StartSDD` with run B, which does not gate, returns nil, and `AnswerSDDGate` then returns `errNoPausedPlan`. Run A ran once;
     - **`TestARefusedAnswerKeepsTheRunPaused`**. Run A gates. After `state.BeginQuiesce()`, `AnswerSDDGate` returns `CodeQuiescing` and adds no message, and the fake recorded no answer. The paused run is still `s.paused`;
     - **`TestAuthorPlanReturnsTheCandidate`**. `Deps.AuthorPlan` records its params and returns `{PlanPath: "/repo/p.md"}`. `AuthorPlan` returns that path with `StopReasonEndTurn`. An empty goal returns an error and calls nothing. Without `Deps.AuthorPlan` it returns `CodeNoRunner`;
     - **`TestAStoppedAuthoringReportsCancelled`**. A blocking `Deps.AuthorPlan`: `Stop` while it runs makes `AuthorPlan` return `StopReasonCancelled` and no error;
     - **`TestPlanPreflightForwardsWithoutTheSlot`**. While a blocking `Prompt` holds the slot, `PlanPreflight` returns what `Deps.PlanPreflight` returns. The strategy "fast" returns an error. Without `Deps.PlanPreflight` it returns `CodeNoRunner`;
     - **`TestAClosedSessionStartsNoPipeline`**. After `host.Swap`, the old session's six calls return errors with `acpwire.CodeSessionClosed`, and no `Deps` func is called.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/acpwire/ ./internal/acpclient/...
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/acpclient/inproc/ -run 'Swarm|SDD|Plan|Gate|Answer|Author|Preflight|Pipeline' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/tui/...
go vet ./internal/acpwire/ ./internal/acpclient/...
```

Expected: every listed test passes, `TestAdapterDoesNotImportApp` and `TestClientImportsStayOnTheWireSide` included. T4 passes on both sides: `session/swarm_start`, `session/sdd_start` and `session/sdd_answer` are registered, and the three new names are not. `tui` passes unchanged, because its test sessions embed `acpclient.Session`.

## Task 2: The runtime serves the pipeline calls

**Goal:** the runtime answers the four `Deps` calls over its current orchestrator and factories, `Run` binds them, the runtime owns its runner types, and the pipeline factory returns a true nil. The TUI still uses its own options until Tasks 3 and 4.

**Files:** `internal/app/pipelines.go` (new), `turns.go`, `runtime.go`, `app.go`; `pipelines_test.go` (new), `turns_test.go`.

**Steps:**

1. **`pipelines.go`** (new) holds the Contracts' types, runtime methods, `planRun`, and the four helpers.
   - **`RunSwarm`** reads `rt.SwarmRunner` and `rt.SwarmOverrideFactory` under `rt.mu`. Without an orchestrator it returns the Contracts' refusal. When `params.Overrides` is not empty and the override factory is set, it calls the orchestrator's `SetRunnerFactory` with the factory's result for `roleOverrides(params.Overrides)`, and defers `RestoreRunner`. It returns the orchestrator's `Run` with `params.Goal`;
   - **`NewPlanRun`** reads `rt.PipelineFactory` under `rt.mu`. Without one it returns the Contracts' refusal. It calls the factory with the path and `roleOverrides(params.Overrides)`, and returns the Contracts' "cannot build a runner" error for a nil result. When the result is a `*pipeline.ControllerAdapter`, it sets the controller's `Strategy` to `params.Strategy` when that is not empty. When `params.Verify` is set and either command is not empty, it sets the controller's `Verifier.Build` and `Verifier.Test` to the two commands, the way configured commands win in `ResolveVerifyCommands` (decision 10). It returns a `planRun` over the runner and the path;
   - **`PlanPreflight`** reads `rt.State` under `rt.mu`. It calls `pipeline.InspectPlan` with the path, the state's `WorkingDir`, and the requested strategy, `pipeline.StrategyAuto` when `params.Strategy` is empty. It returns the inspection's error unchanged. Otherwise it returns the inspection through `planInspectionOf`, and the gate from `verifyGateOf(state.WorkingDir, state.ConfigSnapshot().SDD.Verify)`. Agent-side code reads the config through `ConfigSnapshot`, because a reload replaces `State.Config` from another goroutine (`session.go:1048`);
   - **`AuthorPlan`** reads `rt.State` and `rt.PlanAuthorFactory` under `rt.mu`. Without a factory it returns the Contracts' refusal. The design is `params.Design`, or `params.Goal` when that is empty after trimming. It resolves the path with `sddplans.DraftPath(state.WorkingDir, state.ConfigSnapshot().SDD.PlansDir, params.Goal, state.Now())`. It builds the request from the goal, the design, the working directory and the path, calls the factory, then runs the author with the same request. The two failures before the run are wrapped as the Contracts say, and the run's error is returned unchanged. It returns `authoredPlanOf(result)`;
   - **`planRun.Run`** calls the runner's `Run` with the plan path. An error that `errors.Is(err, pipeline.ErrHumanGateRequired)` becomes the Contracts' gate error. Any other result is returned unchanged. **`planRun.Answer`** calls the runner's `AnswerGate`;
   - **`roleOverrides`** returns nil for an empty map, and otherwise the same map keyed by `routing.AgentRole`;
   - **`planInspectionOf`** maps the effective strategy, `HasMarshalBlocks`, `StrictBlocked`, the report's totals, each `Report.Tasks` entry's number, title and three counts, and each diagnostic's task, severity, message and line;
   - **`verifyGateOf`** calls `pipeline.ResolveVerifyCommands(repoRoot, configured.Build, configured.Test)`. `Detected` is set when the result is known and either command differs from the configured one. That is `verifyGateRow`'s "(detected)" rule (`model.go:4303`);
   - **`authoredPlanOf`** takes the path from the request, or from the inspection's plan when the request has none (`sddreview`'s `CandidatePath` rule, `review.go:99`). It also carries `AutoAccepted` and the inspection through `planInspectionOf`.
2. **`turns.go`, `bindTurnDeps`** also sets `d.Swarm = rt.RunSwarm`, `d.NewPlanRun = rt.NewPlanRun`, `d.PlanPreflight = rt.PlanPreflight` and `d.AuthorPlan = rt.AuthorPlan`. Its doc says it binds the runner, the run calls and the pipeline calls.
3. **`app.go`, the pipeline factory closure** (`app.go:1488`) keeps `buildPipelineController`'s result in a variable and returns nil when it is nil, so a plan it cannot build gives a nil interface (decision 9). A comment says why.
4. **The runtime's types** (decision 17). The runtime's `PipelineFactory` and `SwarmOverrideFactory` fields (`runtime.go:123`, `126`), `NewSession`, `newSessionLocked`, `buildAgentRunner`, `buildAgentRunnerWithLock` and the factory closure use `PipelineRunner` and `SwarmOverrideFactory` in place of `tui.AgentRunner` and `tui.SwarmOverrideFactory`. Until Task 3 deletes the TUI's options, `Run` converts where it hands them to the TUI:
   - `WithPipelineFactory` and the swap result's `PipelineFactory` get a closure that calls the factory and returns its result as a `tui.AgentRunner`. A nil result converts to nil. A nil factory gives a nil closure, not one that would call it;
   - `WithSwarmOverrideFactory` and the swap result's `SwarmOverrideFactory` get `tui.SwarmOverrideFactory(…)` conversions.
5. **Tests.** Factories and plans follow `app_test.go`'s `TestBuildPipelineControllerReturnsAdapter` and the TUI's `newSDDPreflightModel` (`model_test.go:9795`). The three plan bodies are the TUI tests': executable (one `marshal.file` block), prose-only, and mixed (one `marshal.agent` block):
   - **`pipelines_test.go`** (new):
     - **`TestRunSwarmInstallsTheOverridesForOneRun`**. An orchestrator from `swarm.New(state, base)`, where `base` fails with "base factory". An override factory records its overrides and returns a factory that fails with "override factory". `RunSwarm` with `{"planner": "local/qwen"}` returns an error containing "override factory", and the factory saw `routing.RolePlanner` mapped to "local/qwen". Afterwards the orchestrator's `NewRunner` fails with "base factory" again. `RunSwarm` without overrides returns an error containing "base factory", and the override factory was called only once;
     - **`TestRunSwarmWithoutAnOrchestratorIsRefused`**. On `&Runtime{State: state}`, `RunSwarm` returns `CodeNoRunner`;
     - **`TestNewPlanRunSetsTheStrategyAndVerify`**. Over a working directory holding the executable plan, `rt.PipelineFactory` records its overrides and returns a `pipeline.NewControllerAdapter` over `pipeline.NewController` (with `worktree.NewFakeGitOps()`). `NewPlanRun` with the strategy "strict", the overrides `{"sdd_implementer": "kimi/k3"}` and the verify commands "make build" and "make test" returns a `*planRun`. Its controller's `Strategy` is `pipeline.StrategyStrict` and its `Verifier` holds both commands, and the factory saw `routing.RoleSDDImplementer` mapped to "kimi/k3". Without `Verify`, and with empty `Verify` commands, the `Verifier`'s commands stay empty. This is decision 10's regression test;
     - **`TestNewPlanRunReportsAPlanItCannotBuild`**. A factory returning nil makes `NewPlanRun` fail with "cannot build a runner". Without a factory it returns `CodeNoRunner`;
     - **`TestThePipelineFactoryReturnsNilForAPlanItCannotBuild`**. The factory `buildAgentRunner` returns, as `TestReloadAgentRuntimeSwapsPipelineFactory` builds it, returns a nil interface for "/nonexistent/plan.md", and the state's last message starts with "Cannot start plan run:". This is decision 9's regression test;
     - **`TestAPlanRunReportsItsGateAsACode`**. Over a fake `PipelineRunner` whose `Run` returns `fmt.Errorf("task 2: %w", pipeline.ErrHumanGateRequired)`, `planRun.Run` returns an error with `CodeHumanGateRequired`. When it returns another error, `planRun.Run` returns that error unchanged. `Answer("use 30s")` reaches `AnswerGate`;
     - **`TestPlanPreflightInspectsThePlan`**, a table over the three bodies:
       - the executable plan, no strategy: `adaptive`, `HasBlocks`, `DetOps` 1, one task `{1, "Add file", DetOps 1}`;
       - the prose-only plan, no strategy: `agent`, no blocks;
       - the mixed plan, strategy "strict": `strict`, `HasBlocks`, `StrictBlocked`, `AgentOps` 1, `EstCalls` 1.

       A missing path returns an error containing "no such file";
     - **`TestPlanPreflightResolvesTheVerifyGate`**. With "make build" and "make test" configured in the state's config, the gate is known and not detected, and holds those commands. With nothing configured and a `go.mod` in the working directory, it is known and detected, with "go build ./..." and "go test ./...". In an empty directory it is not known;
     - **`TestAuthorPlanReturnsTheCandidate`**. A factory records its request and returns `sddauthor.NewRunner` over a model that writes the executable plan to the request's path. `AuthorPlan` with the goal "add marker file" returns a path inside the state's plans directory, and the file exists. The request's design is "add marker file", and the result's inspection has the strategy `adaptive`. Without a factory it returns `CodeNoRunner`. A factory failing with "no route" makes the error contain "start plan authoring: no route";
   - **`turns_test.go`, `TestBindTurnDepsServesTheRuntime`** also checks that `Swarm`, `NewPlanRun`, `PlanPreflight` and `AuthorPlan` are set.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/ -run 'Swarm|PlanRun|PipelineFactory|PipelineController|Preflight|AuthorPlan|PlanAuthor|BindTurnDeps|Reload' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/acp/ -run 'SDD|Swarm'
go vet ./internal/app/
```

Expected: no `--- FAIL`, and `ok` for both packages.

## Task 3: Swarm and plan runs go through the session

**Goal:** `/swarm`, `/sdd <plan>` and the SDD gate go through the session, the preflight reads `PlanPreflight`, and the TUI holds no orchestrator or pipeline factory.

**Files:** `internal/app/tui/model.go`, `commands_dispatch.go`, `sdd_command.go`; `boundary_allowlist_test.go`, `moved_reads_test.go`, `model_test.go`, `sdd_command_test.go`, `sdd_authoring_test.go`, `verifygate_test.go`, `turns_seam_test.go`, `pipelines_seam_test.go` (new); `internal/app/app.go`; `internal/agent/runner.go`, `internal/agent/swarm/orchestrator.go`, `internal/pipeline/adapter.go`, `internal/acp/turn.go`.

**Steps:**

1. **`sdd_command.go`.** `parsedSDDArgs.Strategy` becomes a string, and `setStrategy` accepts `acpwire.StrategyAgent`, `StrategyAdaptive` and `StrategyStrict` with the same error text. The `pipeline` import goes.
2. **`commands_dispatch.go`:**
   - **`swarm`**: after the usage check, the "Swarm is not available (agent failed to initialise)." note is written when `!m.sess.Status().AgentReady`. While busy it returns, as now. Otherwise it calls `m.openRunPreflight("swarm", goal, nil)`;
   - **`sdd`**: the "Plan execution is not available (agent failed to initialise)." note is written when `!m.sess.Status().AgentReady`, still before the picker. After the busy check, it calls `m.sess.PlanPreflight(m.ctx, acpwire.PlanPreflightParams{PlanPath: planPath, Strategy: parsed.Strategy})`. On an error it writes "Cannot start plan run: " plus the error, refreshes and returns (decisions 3 and 9). Otherwise it calls `m.openRunPreflight("sdd", planPath, &pf)`. The factory call, the strategy assignment and `m.pipelineRunner` go;
   - the new-session effect (`commands_dispatch.go:207` to `215`) stops reading `SwarmRunner`, `PipelineFactory` and `SwarmOverrideFactory`. It keeps `PlanAuthorFactory` until Task 4.
3. **`model.go`, the preflight:**
   - **`openRunPreflight`** takes `(kind, goal string, pf *acpwire.PlanPreflight)` (Contracts). For "sdd" it keeps the task count from `sddplans.Discover` and the config lines. It renders the operation totals and the per-task lines from `pf.Inspection`, with today's wording. The verify row comes from `verifyGateRow(pf.Verify)`. The strategy options use `pf.Inspection`'s `HasBlocks` and `StrictBlocked` with today's disabled reasons, and the initial strategy is `pf.Inspection.Strategy`, or "agent" when that is empty, as today's default is. That is the effective strategy: the explicit one when `/sdd` named one, and otherwise adaptive or agent by blocks, which is what the controller assertion computed. With a nil `pf` the "sdd" branch shows no inspection lines and no strategy options, and its verify row is the unknown-gate row. `pendingRun` holds the goal and the kind;
   - **`verifyGateRow`** takes the session's gate (Contracts): not known gives today's warning, and otherwise the detail is the two commands joined as now, with " (detected)" when `Detected` is set;
   - **`pendingAgentRun`** loses `runner` and `modelOverrides` (Contracts).
4. **`model.go`, the confirm (`castlist.StartMsg`, `model.go:2487`).** It keeps closing the dock, the pending-run and busy checks, and the verify persistence and its notes. It then builds the run from the message:
   - "swarm": `acpwire.SwarmParams{Goal: run.goal, Overrides: roleNames(pm.Overrides)}`, started with `m.startSessionRun("Cannot start work", …)` and a run that calls `sess.StartSwarm`;
   - "sdd": `acpwire.SDDParams{PlanPath: run.goal, Strategy: pm.Strategy, Overrides: roleNames(pm.Overrides)}`, with `Verify` set to the proposed pair when either is not empty, started the same way through `sess.StartSDD`.

   Each run captures `sess := m.sess` and the params, never `m`. The factory rebuild, the strategy assignments and the override installation go. **`castlist.CancelMsg`** loses its `restoreRunner` block. **`roleNames`** (Contracts) returns nil for an empty map.
5. **`model.go`, the gate:**
   - **`gatepanel.AnswerMsg`** closes the dock, then calls `m.startSessionRun("Cannot start work", …)` with a run that calls `sess.AnswerSDDGate(ctx, text)`. The user message, the `pipelineRunner` check and its `AnswerGate` call go: the session records the answer (decision 5);
   - **`gatepanel.StopMsg`** closes the dock, calls `m.sess.StopSDD(m.ctx)` in place of `m.state.ClearSDDGate()`, logs an error with `m.state.Logger().Warn`, and keeps its note;
   - **`handleAgentFinished`'s gate branch** tests `acpclient.IsCode(msg.err, acpwire.CodeHumanGateRequired)` in place of `errors.Is(msg.err, pipeline.ErrHumanGateRequired)`. Its `restoreRunner` block goes. Its watch-resume comment names `beginTurn` where it named `startAgentRun` (`model.go:5577`).
6. **`model.go`, deleted:** `AgentRunner`, `SwarmOverrideFactory`, `RoleOverrideRunner`, the `swarmRunner`, `pipelineFactory`, `swarmOverrideFactory`, `pipelineRunner` and `restoreRunner` fields, `WithSwarmRunner`, `WithPipelineFactory`, `WithSwarmOverrideFactory`, `SessionSwapResult`'s `SwarmRunner`, `PipelineFactory` and `SwarmOverrideFactory`, `startAgentRun` and `runAgentCmd`. `agentFinishedMsg`'s doc says every run's refusal note is set, now that every run goes through the session. The `pipeline` and `agent/swarm` imports go.
7. **`app/app.go`:** `Run` deletes the local `swarmRunner` (`app.go:2764`), the `WithSwarmRunner`, `WithPipelineFactory` and `WithSwarmOverrideFactory` options, and Task 2's conversions. The swapper discards `NewSession`'s orchestrator, pipeline factory and override factory as `_`, and fills only `State`, `Session`, `PlanAuthorFactory` and `ToolRegistry`.
8. **Comments that name the TUI's runner.** Each now names the runtime or ACP:
   - `agent/runner.go:668` and `agent/swarm/orchestrator.go:63` say the method satisfies ACP's `AgentRunner`;
   - `orchestrator.go:42` to `43` say the orchestrator satisfies the runtime's `PipelineRunner`;
   - `orchestrator.go:53`, `69` and `77` to `78` say the runtime's `RunSwarm` restores the factory when the run ends;
   - `pipeline/adapter.go:12`, `200` and `209` name the runtime's plan run, its resume and its tests;
   - `acp/turn.go:68` names `app.PipelineRunner`.
9. **`boundary_allowlist_test.go`:** `model.go` loses `marshal/internal/agent/swarm` and `marshal/internal/pipeline`, and `commands_dispatch.go` and `sdd_command.go` lose `marshal/internal/pipeline`. **`moved_reads_test.go`:** add `"ClearSDDGate": "5d"` at the end of the map, with a comment that the gate's stop goes through `StopSDD` since 5d.
10. **Test helpers.** `turnRecorder` gains its Task 3 fields and methods (Contracts). **`pipelines_seam_test.go`** (new) holds `fakePlanRun`, `preflightOf`, `planRunOf` and `pausedGateModel` (Contracts):
    - `fakePlanRun.Run` counts the run and, while `gates` is above zero, decrements it and returns `&acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}`. Otherwise it returns nil;
    - `pausedGateModel` builds with `testModelTurns` over `inproc.Deps{State: …, Runner: runnerOf(&testAgentRunner{}), NewPlanRun: planRunOf(run, nil)}`, with a fake that gates once. It calls `m.sess.StartSDD` with the path "/plans/p.md" and expects `CodeHumanGateRequired`. It sets the gate with `state.SetSDDGate` and opens `gatepanel.New(m.sess.Status().SDDGate)`, as `handleAgentFinished` does.
11. **Tests moved.** Each keeps its assertions unless the step says otherwise. Models that need a runner build with `testModelWith(inproc.Deps{State: state, Runner: runnerOf(&testAgentRunner{}), …}, …)`:
    - **`TestSwarmCommandOpensPreflightThenStartsRun`.** `Deps.Swarm` records its params. After draining the confirm's batch, the swarm got the goal "add a regression test". `fakeSwarmRunner` goes;
    - **`TestSwarmCommandPreflightCancelClearsPendingRun`** and **`TestSwarmCommandWithoutGoalShowsUsage`** build over `Deps` with a runner in place of `WithSwarmRunner`;
    - **`TestSDDCommandOpensPreflightThenStartsRun`.** `Deps` add `PlanPreflight: preflightOf(…)` with an agent-strategy inspection, and `NewPlanRun: planRunOf(run, &got)`. After draining, `got` holds one entry with the path "/repo/docs/plans/feature-plan.md", and the fake ran once. `fakeSDDRunner` goes;
    - **`TestSDDCommandPreflightCancelClearsPendingRun`**, **`TestSDDCommandWithoutPlanOpensPlanPicker`** and **`TestSDDCustomPathPickerDispatchesPlanPath`** build over `Deps` with a runner and `preflightOf` in place of `WithPipelineFactory`. `TestSDDCommandWithoutRunnerReportsUnavailable` does not change;
    - **`TestSDDControllerRebuiltWithOverrides`** becomes **`TestSDDStartCarriesTheOverrides`**. Confirming with `castlist.StartMsg{Strategy: "adaptive", Overrides: {routing.RoleSDDImplementer: "kimi/k3"}}` and draining makes `got[0]` hold the strategy "adaptive" and the overrides `{"sdd_implementer": "kimi/k3"}`;
    - **`TestSDDPreflightMetaShowsVerifyTimeout`** calls `m.openRunPreflight("sdd", "/path/plan.md", &acpwire.PlanPreflight{})`;
    - **the four `TestSDDPreflight…` strategy tests** drop `newSDDPreflightModel` and open the preflight on `newTestModelInRepo` with a `PlanPreflight` value:
      - **`TestSDDPreflightSelectsAdaptiveForExecutablePlan`**: `{Strategy: adaptive, HasBlocks, DetOps: 1, one task}`. Adaptive is selected and "deterministic ops" renders;
      - **`TestSDDPreflightSelectsAgentForProseOnlyPlan`**: `{Strategy: agent}`. Agent is selected and "no executable blocks found" renders;
      - **`TestSDDPreflightBlocksStrictForMixedPlan`**: `{Strategy: strict, HasBlocks, StrictBlocked}`. Strict is selected and the view renders "fallback or unresolved work remains". The old version passed only because its cast rows had no route, which blocks Enter whatever the strategy, so it now asserts the reason itself;
      - **`TestSDDPreflightExplicitStrategyOverridesAdaptive`**: `{Strategy: agent, HasBlocks}`. Agent is selected, and the test's comment says the session reports the explicit strategy as the effective one.

      `newSDDPreflightModel` goes. `app`'s `TestPlanPreflightInspectsThePlan` covers the inspection;
    - **`TestSDDAuthoringToPreflight`.** `newAuthoringModel` (`sdd_authoring_test.go`) builds over `Deps` with a runner and `preflightOf` an adaptive inspection, keeps `WithPlanAuthorFactory` until Task 4, and drops `WithPipelineFactory`;
    - **`TestRunAgentCmdPanicRecovery`** and `panicRunner` are deleted. `inproc`'s `TestAPanickingRunnerFailsTheTurn` covers the run path;
    - **`TestGateOpensPanelNotATranscriptMessage`** and **`TestWarningSurvivesLateApprovalEvents`** send `agentFinishedMsg{err: &acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}}`;
    - **`TestAnswerMsgResumesTheRun`** builds with `pausedGateModel`. After `gatepanel.AnswerMsg{Text: "use 30s"}`, the panel is closed and the command is not nil. Draining it records the answer "use 30s", the fake recorded it, and the user message "use 30s" is in the transcript. `recordingPipelineRunner` and `runnerAnswer` go;
    - **`TestGatePagingThroughModel`.** `openGate` builds with `pausedGateModel` and the test's gate fixture. After Enter produces the `AnswerMsg` and `Update` returns a command, the test drains it before checking the fake's answer and the echoed message;
    - **`verifygate_test.go`.** `TestVerifyGateRowStates` passes a `VerifyGate`: `{Build: "make build", Test: "make test", Known: true}` has no warning, and `{}` has one. A third case, `{Build: "go build ./...", Test: "go test ./...", Known: true, Detected: true}`, renders "(detected)". `TestProposalPersistsOnApprove` and `turns_seam_test.go`'s `TestTheProposalGoesThroughTheSession` call `m.openRunPreflight("sdd", "plan.md", &acpwire.PlanPreflight{})`;
    - **`sdd_command_test.go`** compares with `acpwire.StrategyAdaptive` and `acpwire.StrategyStrict`;
    - **test comments.** `TestEnterWithRunnerDispatchesAgentRunAndTick` and `TestAgentCommandRegistersAndReleasesSessionWork` say "the run command" where they say `runAgentCmd`.
12. **New tests, in `pipelines_seam_test.go`:**
    - **`TestTheProposalReachesThePlanRun`.** Over `Deps` with a runner, `VerifyProposal` returning "make build" and "make test", `preflightOf` an agent inspection, and `planRunOf(run, &got)`: `/sdd plan.md`, then `f`, the proposal's message, the confirm and draining. `got[0].Verify` holds both commands. This is decision 10's regression test at the TUI;
    - **`TestAPlanThePreflightRejectsIsReportedNotRun`.** With a `PlanPreflight` that fails with "pipeline plan: read missing.md: no such file", `/sdd missing.md` writes "Cannot start plan run: pipeline plan: read missing.md: no such file", opens no dock, and leaves the model idle. This is decision 9's regression test at the TUI;
    - **`TestTheGateStopsThroughTheSession`.** On `pausedGateModel`, `gatepanel.StopMsg` records one `StopSDD`, empties `Status().SDDGate.Question`, and writes the "Plan run stopped." note;
    - **`TestSwarmWithoutAnAgentIsUnavailable`.** Without a runner, `/swarm fix it` writes "Swarm is not available (agent failed to initialise)." and opens no dock.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'startAgentRun|runAgentCmd|pipelineRunner|restoreRunner|swarmRunner|pipelineFactory|swarmOverrideFactory|RoleOverrideRunner|SwarmOverrideFactory|AgentRunner|"marshal/internal/(pipeline|agent/swarm)"' internal/app/tui --include='*.go' --exclude='*_test.go'
grep -rnE 'tui\.(AgentRunner|SwarmOverrideFactory|WithSwarmRunner|WithPipelineFactory|WithSwarmOverrideFactory|RoleOverrideRunner)' internal cmd --include='*.go'
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/tui/ -run 'Swarm|SDD|Gate|Preflight|Proposal|VerifyGate|Answer|StopMsg|Warning|ThroughTheSession|PlanRun|Moved|Seam' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/tui/...
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/ ./internal/acp/ ./internal/agent/... ./internal/pipeline/
go vet ./internal/app/... ./internal/acp/
```

Expected: both greps print nothing. Every listed test passes, and `tui` passes, T1, T1b and the goldens included.

## Task 4: Plan authoring goes through the session

**Goal:** `/sdd new` runs through `AuthorPlan`, the review panel renders the wire type, a stopped authoring is not a failure, and the TUI's own stop registration goes.

**Files:** `internal/app/tui/model.go`, `sdd_authoring.go`, `commands_dispatch.go`, `sddreview/review.go`; `boundary_allowlist_test.go`, `moved_reads_test.go`, `sdd_authoring_test.go`, `notice_test.go`, `interrupt_test.go`, `frame_geometry_test.go`, `turns_seam_test.go`, `pipelines_seam_test.go`, `sddreview/review_test.go`; `internal/app/app.go`.

**Steps:**

1. **`sdd_authoring.go`** holds `planAuthorFinishedMsg` and `planAuthorCmd` (Contracts) and no `session` or `sddauthor` import. `planAuthorCmd`'s command calls `sess.AuthorPlan(ctx, params)` and returns its result and error with the refusal "Cannot start work". `runPlanAuthorCmd` goes.
2. **`model.go`, `startSDDAuthoring`:**
   - the ending check and its note stay first;
   - the "Plan authoring is not available (agent failed to initialise)." note is written when `!m.sess.Status().AgentReady`;
   - the busy check, the goal and design choice, `--from-last-plan` and the usage note stay as they are;
   - the draft path, the factory call, `BeginWork`, the stop registration and the hand-set turn state go. It calls `m.beginTurn()` (decision 12) and returns the batch of `planAuthorCmd(ctx, sess, acpwire.AuthorPlanParams{Goal: goal, Design: design})`, `tickCmd()` and `spinnerTickCmd()`, capturing `sess := m.sess`.
3. **`model.go`, `handlePlanAuthorFinished`:**
   - it clears `busy`, `turnStartedAt` and `agentCancel`. Its `releaseTurnStop` and `SetActivity` calls go;
   - a refused start (`msg.refusal` set and `refusedStart(msg.err)`) clears `cancelling`, writes `msg.refusal + ": " + msg.err.Error()`, refreshes and returns;
   - a stopped authoring (`m.cancelling`, or `msg.result.StopReason` is `StopReasonCancelled`) clears `cancelling`, clears the provider notice, writes "Agent turn cancelled.", refreshes and returns (decision 11);
   - a failure and a success are handled as now. The auto-accepted note names `msg.result.PlanPath`, and the review opens with `sddreview.New(msg.result)`.
4. **`sddreview/review.go`.** The panel holds an `acpwire.AuthoredPlan`, and `New` takes one (Contracts):
   - `CandidatePath` returns `result.PlanPath`;
   - `hasErrors` is true for a nil inspection, or one with a diagnostic whose severity is `acpwire.DiagnosticError`;
   - `bodyRows` renders the same rows from the wire. The task count is `len(Tasks)`, the totals and strategy come from the inspection's fields, and each diagnostic's severity is compared with `acpwire.DiagnosticError`.

   `diagError` and `diagWarning` go, along with the comment about the boundary, and the `sddauthor` import goes.
5. **`model.go`, the stop.** `cancelTurn` calls `m.sess.Stop(m.ctx, "turn stopped")`, then cancels `agentCancel`, and its doc says every run is the session's. `handleAgentFinished`'s two `releaseTurnStop` calls go. Deleted: `beginTurnStopRegistration`, `releaseTurnStop`, `stopTurnWork`, the `endPendingStop` field, `PlanAuthorFactory`, the `planAuthorFactory` field, `WithPlanAuthorFactory` and `SessionSwapResult.PlanAuthorFactory`.
6. **`commands_dispatch.go`.** The new-session effect stops reading `PlanAuthorFactory`. **`app/app.go`:** delete the `WithPlanAuthorFactory` option. The swapper discards the plan author factory as `_` too, so its result has only `State`, `Session` and `ToolRegistry`.
7. **`boundary_allowlist_test.go`:** delete the `sdd_authoring.go` and `sddreview/review.go` entries. **`moved_reads_test.go`:** add `BeginWork`, `EndWork`, `BeginTurnStop`, `EndTurnStop`, `RequestTurnStop` and `SetActivity` at `"5d"`, with a comment that every run the TUI starts registers its work, stop group and idle reset in the session since 5d.
8. **Tests moved:**
   - **`sdd_authoring_test.go`.** A new helper, `authoringModel(t)`, builds with `testModelTurns` over `Deps` with a runner, Task 3's `preflightOf`, and an `AuthorPlan` from `fakeAuthorPlan(state)`, and returns the model and its recorder. `newAuthoringModel` keeps its signature and returns `authoringModel`'s model, so its callers in `model_test.go` do not change. `fakeAuthorPlan` resolves its path with `sddplans.DraftPath` over the state's working directory and plans directory. It writes the executable plan there and returns `{PlanPath: path, Inspection: &acpwire.PlanInspection{Strategy: adaptive, HasBlocks: true, DetOps: 1, Tasks: one task}}`. `fakePlanAuthorFactory` and `fakeModelRunner` go. `TestStaleReviewMessagesCannotAffectReplacement` opens its replacement with `sddreview.New(acpwire.AuthoredPlan{PlanPath: replacementPath})`. `TestSDDNewFromLastPlanUsesFinalAssistantMessage` builds with `authoringModel`, drains the command, and also checks that the recorded params have the goal "Convert the approved plan into an executable SDD plan." and the design "approved plan content". The other tests keep their assertions;
   - **`notice_test.go`, `TestPlanAuthorFailureDoesNotDoubleReport`** keeps its assertions;
   - **`sddreview/review_test.go`** and **`frame_geometry_test.go`'s `generatedReviewFixture`** build `acpwire.AuthoredPlan` values with `PlanInspection`, `PlanTask` and `PlanDiagnostic` fields equal to their `pipeline` fixtures' (`acpwire.DiagnosticError` and `DiagnosticWarning` for the severities), and keep their assertions;
   - **`interrupt_test.go`.** A helper, `runningPrompt(t, state)`, builds a sized model over `Deps` whose runner is a `contextRunner`. It calls `startPrompt("hello", false)`, runs the batch's first command in a goroutine, and waits up to one second for the runner to start:
     - **`TestCancelTurnStopsTurnWorkBeforeCancelling`**: with the test service set, it wraps the model's `agentCancel` to append "agent-cancel" before calling the original. `cancelTurn` makes one service stop with the cause "turn stopped", before "agent-cancel";
     - **`TestNormalFinishUnregistersWithoutStopping`**: over a `fakeAgentRunner`, draining `startPrompt`'s batch and passing the finish to `Update` makes no service stop and leaves `state.CurrentTurnStop()` nil;
     - **`TestCancelTurnWithoutServiceStillCancels`**: with no service, `cancelTurn` ends the runner's context within one second;
     - **`TestCancelTurnLeavesDetachedWorkRunning`**: with the test service set, `cancelTurn` makes one stop call and no targeted execution stop;
     - **`TestTurnStopSurvivesReplacementRelease`** is deleted. `agent`'s `TestTurnStopWinsAgainstRegistrationReplacement` covers the same race on the state.
9. **New tests, in `pipelines_seam_test.go`:**
   - **`TestAuthoringGoesThroughTheSession`.** `/sdd new add marker file` records `{Goal: "add marker file", Design: "add marker file"}`, and the review opens on the returned path;
   - **`TestAStoppedAuthoringIsNotAFailure`.** Over an `AuthorPlan` that blocks until its context is done: `/sdd new x`, the run in a goroutine, `cancelTurn`, then its finish through `Update`. No message contains "Plan authoring failed", `m.state.Notice()` reports none, `cancelling` is false, and "Agent turn cancelled." is written once. A following `handleAgentFinished(agentFinishedMsg{})` with `busy` set writes no second "Agent turn cancelled.". This is decision 11's regression test;
   - **`TestARefusedAuthoringIsReported`.** After `state.BeginQuiesce()`, draining `/sdd new x` writes "Cannot start work: session is quiescing" and leaves the model idle;
   - **`TestAuthoringWithoutAnAgentIsUnavailable`.** Without a runner, `/sdd new x` writes "Plan authoring is not available (agent failed to initialise)." and starts nothing.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'beginTurnStopRegistration|releaseTurnStop|stopTurnWork|endPendingStop|runPlanAuthorCmd|planAuthorFactory|PlanAuthorFactory|sddauthor\.(Result|Request|Factory)|"marshal/internal/sddauthor"' internal/app/tui --include='*.go' | grep -v 'model.go:.*"marshal/internal/sddauthor"'
grep -nE 'tui\.WithPlanAuthorFactory|PlanAuthorFactory:' internal/app/app.go
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/tui/ -run 'SDDNew|Authoring|Review|Discard|Candidate|PlanAuthor|Cancel|Stop|Finish|Moved|Seam' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/tui/...
TMPDIR=/private/tmp/p2-5d-suite go test ./internal/app/ -run 'AuthorPlan|PlanAuthor|BindTurnDeps|Adapter|SendNow' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
go vet ./internal/app/...
```

Expected: both greps print nothing (`model.go` keeps its `sddauthor` import for `RemoveCandidate`, which the first grep's filter allows). Every listed test passes, and `tui` passes, T1, T1b and the goldens included.

## Task 5: Verification and docs

**Goal:** the full suite is green, and the spec, the design and `AGENTS.md` record plan 5d.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`, `AGENTS.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 5d entry to the Implementation record, after plan 5c's. It lists:
   - the `acpwire` types;
   - the six `Session` methods and their `Methods` rows: three on existing ACP methods, three new;
   - the paused plan run, the gate as `CodeHumanGateRequired`, and what `StopSDD` does;
   - the runtime's four calls in `Deps` through `bindTurnDeps`, the runtime's own runner types, and the factory's nil fix;
   - that the TUI starts every run through the session, and which options, fields and swap result fields are gone;
   - the three fixes and the notes that changed (decisions 9 to 13);
   - T1b's seven names and the allowlist's seven entries.
2. **`design.md` §7.** The P2 row's plan text becomes "nine plans, with 2, 3 and 5 split into sub-plans; plans 1, 2a–2c, 3a–3d, 4 and 5a–5d implemented" (decision 18).
3. **`AGENTS.md`, Design constraints.** The "TUI renders only" bullet loses the sentence "It talks to the agent loop through the structural `AgentRunner` interface so it never imports `internal/agent`.": the interface is gone, and the next sentence already says the TUI reaches the agent side through `internal/acpclient`.
4. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-5d-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-5d-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat main..HEAD -- internal/app/tui/testdata internal/app/tui/livebox/testdata
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the test line prints nothing, or only `web/bridge`'s known hang (Assumptions). Treat any other failure as new unless it also fails at `cc07ed11`, or it is one of the load flakes below and passes on its own with `-count=3`;
- the `git diff` prints nothing.

**Baseline.** At `cc07ed11`, all 117 packages passed. Earlier full runs saw these fail under load and pass alone:
- `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`);
- `TestRunManagedCancellationKillsTree` and `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`);
- `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).

Finally, run `./marshal` in this repository and check by eye:
- `/swarm` with a goal: the cast list opens, Enter starts the run, the roles progress, and Ctrl+C twice stops it with "Agent turn cancelled.";
- `/sdd` on a plan with a `marshal.file` block: the preflight selects adaptive and shows the deterministic ops. Pick an override with `o`, then Enter: the run starts;
- `/sdd missing.md`: "Cannot start plan run: …" appears once, and the TUI keeps running;
- `/sdd` on a plan in a repository with no build manifest, then `f`, then Enter: the run's verify gate runs the proposed commands;
- a plan run that reaches a human gate: the gate panel opens. Answer it, and the run resumes with the answer in the transcript. On a second gate, Esc stops it with "Plan run stopped.";
- `/sdd new` with a goal: the review opens on the candidate. Run `/sdd new` again and stop it with Ctrl+C twice: "Agent turn cancelled.", no failure, and the next prompt runs and stops normally.

## Integration notes

- **Plan 5e (ending and swapping)** replaces `finishQuit`'s context cancel with `Close`, and `SessionSwapper` with `Host.NewSession` and `Host.Resume`. After this plan the swap result holds only `State`, `Session` and `ToolRegistry`. `rt.NewSession` still returns the orchestrator and the factories, and only the swapper discards them.
- **Plan 6** moves the refusal, failure, cancellation and gate notes, "Cannot start plan run: …" included.
- **Plan 7** moves the preflight's config reads and the persistence of proposed verify commands.
- **Plan 8** replaces `sddplans.Discover` in the picker and the preflight's task count, `sddauthor.RemoveCandidate`, the scaffold, and `lastFinalAssistantPlan`.
- **A follow-up, not part of P2:** the preflight's strict option is disabled for blocked work only when `/sdd` named strict, because `InspectPlan` computes `StrictBlocked` only for a strict request (Assumptions). A plan with fallback work can be started as strict from the preflight and then fails at its start. This plan keeps that behaviour.
- **P3:**
  - the client sends `session/swarm_start` and `session/sdd_start` with the overrides, and `session/sdd_start` with the strategy and verify commands too;
  - `session/sdd_start` and `session/sdd_answer` report a gate as `stopReason: "gate"`, which the client maps to `CodeHumanGateRequired`;
  - it adds `session/sdd_preflight`, `session/sdd_stop` and `session/sdd_author`;
  - the preflight call becomes asynchronous: the dispatch shows the preflight when the result arrives (decision 4).

## Self-review

- **Every task verifiable alone.** Task 1 adds calls nothing uses. Task 2 adds the runtime's side and binds it, while the TUI keeps its options. Task 3 moves swarm and plan runs and the gate, and Task 4 authoring and the stop registration. Each keeps T1, T1b and the goldens green.
- **Cold executor.** Each step names the function, what it calls after the change, and what it does with the result. The paused run's life is spelled out: replaced by a new run, kept by a refused answer, dropped by a stop. Each new test names its setup and the behaviour it pins.
- **Contracts.** The wire types, the six `Session` methods and their rows, `Deps`' fields, the adapter's paused run, helpers and errors, the runtime's methods, types and errors, the TUI's changed functions and messages, and the test helpers are declared there.
- **Anchors.** Every path, symbol and line was checked at `cc07ed11`.
- **Spec.** C8, C11, the header and the Migration order are updated, with the revision record "2026-11-03, against `cc07ed11` (plan 5c merged)" holding decisions 1 to 3, 5 to 11, 13, 14, 16 and 18.
- **No placeholders.**
