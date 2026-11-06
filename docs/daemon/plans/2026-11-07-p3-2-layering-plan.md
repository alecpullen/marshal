# P3 plan 2: Layering — implementation plan

Date: 2026-11-07. Status: ready for review.
Base: `main` at `1d34fb9a` (P2 plan 9 and P3 plan 3a merged). Every anchor below was checked there. The full suite passed there: 118 packages ok, exit 0 (`TMPDIR=/private/tmp/p3-next-suite`, `-timeout 20m`).
- **It runs alongside P3 plan 6a** ([the asynchronous calls](2026-11-07-p3-6a-async-calls-plan.md)). Plan 6a changes only `internal/app/tui` and its subpackages. This plan changes no file there. The two plans share only `docs/daemon/p3-acp-transport.md`, where each adds its own Implementation record entry.
- **The spec edits are already made**, uncommitted in the main checkout: the header's `Status:`, R1's refinement (where the service lives, `ServiceDeps`, `app.Embedded`), R6 (plan 6a's inventory), Migration order items 2 and 6, and the "Methods P3 adds" heading for plan 6b. Task 4 adds only the Implementation record and the re-measured counts.
- Nothing a user sees changes, and no ACP method, update or wire type changes.

Approved design: [p3-acp-transport.md](../p3-acp-transport.md): R1 (packages and layering), R11 (embedded mode), Migration order item 2, and tests V7 and V8.
Previous plan: [P3 plan 3a, ordering](2026-11-06-p3-3a-ordering-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** embedded mode gets its own composition root, and the adapter's logic gets the home the ACP handlers will call.
- `inproc`'s logic moves, unchanged, to `internal/acp/service`. `inproc` becomes aliases for it, so the TUI and its tests do not change.
- The runtime half of `app.Run` becomes `app.Embedded` and `(*app.Runtime).ServiceDeps`. The TUI half becomes `launch.Run`, and `cmd/marshal` calls it.
- `app`'s non-test code imports neither `tui`, nor Bubble Tea, nor `inproc`, and a test keeps it that way.

**Non-goals:**
- **No ACP handler calls the service yet.** Plan 3b adds the first ones, and builds each ACP session's service with `ServiceDeps`.
- **No pipe in embedded mode.** The TUI still runs on `inproc`, in process, until plan 8.
- **No rewrite.** The service's files move with only their package clause and doc comment changed. Its error texts keep their `inproc:` prefix: some tests pin them, and renaming them is not this plan's job.
- **No change under `internal/app/tui`.** Three comments there still name `app.Run` (`model.go:441`, `:698` and `:6357`). Plan 8 rewrites the code around them. Plan 6a edits the same file.
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p3-2-suite`. Create the directory first: `mkdir -p /private/tmp/p3-2-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`. Plan 6a uses `/private/tmp/p3-6a-suite`. Never share a directory between runs.
- **Two suites may run at once.** Load makes the flakes listed under Task 4 likelier. Rerun a failure alone before treating it as new.
- **`inproc` today** (`internal/acpclient/inproc`) is 25 non-test files (4,973 lines) and their tests, all in package `inproc`. It exports exactly eight names: `Deps`, `New`, `Host`, `Session`, `Runner`, `PlanRun`, `EndService` and `ReferenceFinder`. Its doc comment (`inproc.go:1`) says it must not import `internal/app`. It imports `acpclient`, `acpwire` and agent-side packages, none of them `app`, `acp` or `tui`.
- **Who imports `inproc`:**
  - `app`'s non-test code: `app.go` (the `Deps` block in `Run`: `inproc.ReferenceFinder` at `:2730`, `inproc.Deps` at `:2742`, `inproc.New` at `:2791`), `turns.go` (`TurnRunner` returns `inproc.Runner` at `:23`; `bindTurnDeps(d *inproc.Deps, rt)` at `:161`), and `pipelines.go` (`NewPlanRun` returns `inproc.PlanRun` at `:54`).
  - `app`'s tests: `app_test.go`, `turns_test.go` and `tui_seam_test.go`.
  - 34 test files in `internal/app/tui`, and one test file each in `tui/agents`, `tui/connect`, `tui/skills` and `tui/textfield`, plus two in `tui/settings`. None of them change.
- **The layering today.** `acp` imports `app` (`host.go:629`, `session.go:27`, `run.go:86`, `listen.go:59`). `app` imports `tui` and Bubble Tea only in `app.go` (non-test) and in `app_test.go` and `tui_seam_test.go`. `cmd/marshal` calls `app.Run` through `var appRunner = app.Run` (`main.go:20`, called at `:104`). `main_test.go:160` swaps `appRunner` for a function with the signature `func(context.Context, io.Writer, ...app.Option) error`.
- **`app.Run` today** (`app.go:2577`) does, in order:
  - returns nil at once if `ctx` is already done;
  - fills the options' defaults (`now`, a layered config loader that re-applies provider template defaults with `provider.RebindTemplateDefaults`, and `runProgram`), applies the caller's options once, and sets `deferTrustPrompt`;
  - resolves the credential resolver once (`resolveAuthResolver`) and stores it back on the options;
  - wraps `ctx` in `signal.NotifyContext` for `os.Interrupt` and `SIGTERM`;
  - resolves the working directory and the home directory, sets `configReloader` to a closure over the current runtime (`reloadAgentRuntime`), and computes `firstRun` (`:2647`);
  - builds `trustDecide` (`:2649`: a permanent decision writes the trust store, a session decision sets `sessionTrusted`, and both ask for a reload) and `trustRefresh` (`:2666`);
  - creates the host registry with the injected workers when none was given (`:2682`), closed by a deferred function, and defers closing the current runtime;
  - then loops. Each pass:
    - starts a runtime (`startRuntime`);
    - computes `providerReady` from the provider notice;
    - builds the `Deps` (`:2730` to `:2790`, ending with `bindTurnDeps`);
    - builds the adapter (`:2791`) and the TUI options: the file index seed, the connect panel on first run, the home and working directories, the clipboard, the context and the logger;
    - logs `marshal started` (`:2821`);
    - adds the embedding warning when no worker was injected (`:2834`), and starts the OAuth refresh workers (`:2840`);
    - returns nil if `ctx` is done;
    - marks the state able to raise a secret prompt (`:2854`), and installs the exit-analysis operation (`:2868`);
    - runs the program (`:2876`);
    - on a resume request, closes the runtime, clears the session ID, sets the existing session and loops (`:2878`);
    - with no trust reload pending, quiesces with a 1s bound, marks the session ended, calls the shutdown hook, cleans the spill files, closes the runtime and returns the joined errors (`:2891`);
    - otherwise clears the trust reload, closes the runtime and loops (`:2923`).
- **The runtime already holds what the `Deps` need.** `startRuntime` (`runtime.go:1574`) resolves the home directory as `Run` does (`:1611`) and stores it in `HomeDir`. It stores the options' `configReloader` in `ConfigReloader` (`:1894`), which is `Run`'s closure, and `TrustPromptPending` (`:1891`). `rt.WorkingDir` is the working directory `Run` reads after each start.
- **28 tests in `app_test.go` drive `Run`** through `WithProgramRunner`. Only `TestRunResolvesOptionsOnce` (`:4925`) needs unexported production code (it counts applications of a `func(*options)`). `TestRunLogsToFileNotTerminal` (`:2155`) uses `logPath`. The tests' helpers are `fakeTrustResolver` (`:58`), `nativeToolAgentConfig` (`:1540`), `knowledgeEnabledConfig` (`:2763`), `modelState` (`:2861`), `workerFunc` (`:4871`), `countingAuthResolver` (`:6015`), and `modelStateOf` (`tui_seam_test.go:16`).
- **Three more `app` tests build a TUI over a real runtime** without `Run`: `TestAdapterAccessResolvesWithoutConfigReload` (`:6969`), `TestAdapterAccessFollowsNewSession` (`:7032`) and `TestRuntimeSendNowTUIOptionsExerciseRealFacade` (`:7210`). They need unexported `app` code (`buildAgentRunner`, `newAccessTestRuntime`), so they stay in package `app`. Test files may keep importing `tui` and `inproc`: only non-test imports matter to the layering.
- **The test-tree guard to copy.** `TestClientImportsStayOnTheWireSide` (`internal/acpclient/imports_test.go`) parses the package's non-test files with `go/parser` and checks each import.

**Decisions this plan makes beyond the spec** (R1 and the Migration order record them):

1. **The service is a package of its own: `internal/acp/service`.** `app` builds its `Deps` and its runtime methods return its types, so `app` imports it. `acp` imports `app`, so the service cannot be `acp` itself. Under `internal/acp`, it still reads as the agent side of ACP, and `acp`'s handlers import it from plan 3b.
2. **`inproc` becomes aliases, not a second implementation.** Its eight exported names are aliases for the service's, and `New` forwards. Every TUI test keeps compiling and behaving as it does, and plan 6a, which edits TUI tests, is not disturbed.
3. **`ServiceDeps` belongs to the runtime.** Every field of today's `Deps` block comes from the runtime, except the startup trust prompt and its decision, which belong to the embedding. Plan 3b's ACP sessions get their service from the same method.
4. **`app.Embedded` keeps what needs the options.** Run's options are opaque functions, so anything that reads them stays in `app`: the directories, first run, the host registry and its workers, the credential resolver, the trust decision and its reload, the embedding warning, the OAuth workers, the secret prompt, the exit-analysis operation, and the shutdown sequence. `launch` keeps the signals, the TUI, the program and the loop's control flow.
5. **`app.Run` is rebuilt on `Embedded` first, and moved after** (Task 2, then Task 3). Each step keeps every existing test green, so a regression shows in the task that caused it.
6. **`launch` takes options of its own.** `launch.Option` carries the program runner and wraps `app` options, so `WithProgramRunner` keeps its meaning (a nil runner is ignored).
7. **27 `Run` tests move to `launch`**, with copies of their helpers. `TestRunResolvesOptionsOnce` stays in `app` as a test of `Embedded`, since it counts applications of an `app` option.

**Execution:**
- In the main checkout, if `git status` shows `docs/daemon/p3-acp-transport.md`, this plan or plan 6a's plan uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon/plans: record P3 plans 2 and 6a`. Plan 6a gives the same instruction, so whichever plan starts first commits all three. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p3-2-layering` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p3-2-layering: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path. Task 1 moves files: stage the moves with `git mv`, so history follows them.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The service (`internal/acp/service`, moved from `internal/acpclient/inproc`)

The package keeps `inproc`'s exported names, unchanged: `Deps`, `New`, `Host`, `Session`, `Runner`, `PlanRun`, `EndService` and `ReferenceFinder`. Only the package clause and the doc comment change.

```go
// Package service is the agent side of the client seam (P3 R1): it
// implements acpclient.Host and acpclient.Session over a runtime's state
// and the functions its Deps carry. inproc calls it in process until P3
// plan 8; acp's handlers call it from plan 3b.
//
// It must not import internal/app or internal/acp: app builds its Deps
// and returns its types, and acp imports app.
package service
```

### The wrapper (`internal/acpclient/inproc/inproc.go`, rewritten)

```go
// Package inproc is the in-process transport of the client seam: the
// agent-side service (internal/acp/service), called directly. P3 plan 8
// deletes it.
package inproc

type Deps = service.Deps
type Host = service.Host
type Session = service.Session
type Runner = service.Runner
type PlanRun = service.PlanRun
type EndService = service.EndService
type ReferenceFinder = service.ReferenceFinder

// New builds the in-process host and session over d (service.New).
func New(d Deps) (*Host, *Session)
```

### The runtime's service deps (`internal/app/servicedeps.go`, new)

```go
// ServiceDeps builds the client seam's service deps from the runtime, as
// Run built them: the brokers, the project, the reference finder, the
// access and interaction calls, the commands, the credential resolver,
// the model cache, the config reload and layer reload, the trust refresh,
// the home directory, the tools, the skill index, the file index, work
// when the provider is ready, and the turn calls (bindTurnDeps). ctx
// becomes Deps.Context. It sets no trust prompt: that belongs to the
// embedding.
func (rt *Runtime) ServiceDeps(ctx context.Context) service.Deps
```

### The embedded driver (`internal/app/embedded.go`, new)

```go
// Embedded runs embedded mode's runtimes: one at a time, over options
// resolved once, on one project-host registry.
type Embedded struct{ /* unexported */ }

// NewEmbedded applies opts once, after embedded mode's defaults (the
// template-rebinding config loader and the deferred trust prompt), and
// resolves the credential resolver, the directories, first run and the
// host registry. ctx is the context every runtime's config reload runs
// under.
func NewEmbedded(ctx context.Context, opts ...Option) (*Embedded, error)

func (e *Embedded) WorkingDir() string
func (e *Embedded) HomeDir() string
func (e *Embedded) FirstRun() bool

// Start starts the next runtime: a new session, the one Resume named, or
// the same session reloaded after a trust decision. It adds the embedding
// warning, starts the OAuth refresh workers, marks the state able to raise
// a secret prompt and installs the exit-analysis operation.
func (e *Embedded) Start(ctx context.Context) (*Runtime, error)

// SessionDeps is the current runtime's ServiceDeps plus the startup trust
// prompt and its decision.
func (e *Embedded) SessionDeps(ctx context.Context) service.Deps

// Resume closes the current runtime; the next Start opens sessionID.
func (e *Embedded) Resume(sessionID string)

// TakeTrustReload reports whether a trust decision asked for a reload. If
// so, it closes the current runtime and clears the request; the next Start
// reloads with the decision in force.
func (e *Embedded) TakeTrustReload() bool

// Finish ends the current runtime as embedded mode's exit does: quiesce
// (bounded to one second), mark the session ended, the shutdown hook, the
// spill files, close. It returns progErr joined with the quiesce and close
// errors.
func (e *Embedded) Finish(progErr error) error

// Close closes the current runtime, if any, then the host registry it
// created.
func (e *Embedded) Close(ctx context.Context) error
```

### Launch (`internal/launch/launch.go`, new)

```go
// Package launch is embedded mode's composition root (P3 R1): it runs the
// TUI over the agent side, in process, and owns the loop around it.
package launch

// ProgramResult is the value returned by a ProgramRunner. It carries the
// program's outcome plus an optional request to resume another session.
type ProgramResult struct {
	Err           error
	ResumeSession string
}

// ProgramRunner runs the TUI program. The default runs a tea.Program.
type ProgramRunner func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult

type Option func(*options)

// WithProgramRunner replaces the program runner. A nil runner is ignored.
func WithProgramRunner(runner ProgramRunner) Option

// WithApp passes runtime options through to app.NewEmbedded.
func WithApp(opts ...app.Option) Option

// Run runs embedded marshal until the user quits.
func Run(ctx context.Context, stdout io.Writer, opts ...Option) error
```

### The layering guard (`internal/app/layering_test.go`, new)

```go
// TestAppImportsNoClient fails when one of app's non-test files imports
// the TUI (internal/app/tui or a subpackage), Bubble Tea, or the in-process
// adapter (P3 R1, V7).
func TestAppImportsNoClient(t *testing.T)
```

### The service guard (`internal/acp/service/imports_test.go`, new)

```go
// TestServiceImportsStayAgentSide fails when one of the service's non-test
// files imports the TUI (internal/app/tui or a subpackage) or the JSON-RPC
// client (internal/acpclient/rpc). Importing app, acp, launch or inproc
// would be a cycle the compiler already refuses.
func TestServiceImportsStayAgentSide(t *testing.T)
```

## Task 1: The service moves

**Goal:** `inproc`'s logic lives in `internal/acp/service`, `inproc` is aliases, and `app` builds the service directly.

**Files:**
- every `.go` file in `internal/acpclient/inproc`, moved to `internal/acp/service` with `git mv`. `inproc.go` becomes `service.go`; every other file keeps its name;
- `internal/acpclient/inproc/inproc.go` (new, the wrapper);
- `internal/acp/service/imports_test.go` (new);
- `internal/app/app.go`, `internal/app/turns.go`, `internal/app/pipelines.go` (imports and qualifiers only).

**Steps:**

1. **Move.** `git mv` every file, tests included, as listed under Files. In each moved file, change the package clause to `service`. Replace the doc comment of `service.go` with the one in Contracts. Change nothing else in the moved files.
2. **The wrapper.** Write `internal/acpclient/inproc/inproc.go` as in Contracts: eight aliases and a `New` that returns `service.New(d)`. It imports only the service.
3. **`app`'s non-test code imports the service.** In `app.go`, `turns.go` and `pipelines.go`, replace the `inproc` import with the service and the `inproc.` qualifiers with `service.`. `app`'s test files keep importing `inproc`: the aliases make their values the same types.
4. **The guard.** Write `TestServiceImportsStayAgentSide`, as `TestClientImportsStayOnTheWireSide` walks its directory: parse each non-test file's imports, and report any import of `marshal/internal/app/tui`, a path under it, or `marshal/internal/acpclient/rpc`.
5. **Search for stragglers.** `grep -rn 'acpclient/inproc' --include='*.go' internal cmd web` must list only import lines in test files, the wrapper's own package, and `boundary_test.go:236` (the TUI's denied list, which stays: `inproc` is still agent side to the TUI).

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/acp/... ./internal/acpclient/... ./internal/app/...
TMPDIR=/private/tmp/p3-2-suite go test ./internal/acp/service/ ./internal/acpclient/... ./internal/app/ ./internal/app/tui/... -count=1
```

Expected: everything builds, and every listed package passes: the service's moved tests, `acpclient` (whose method-table and import tests still see the interfaces), `app`, and the whole TUI tree, unchanged.

Mutation check: add a blank import of `marshal/internal/acpclient/rpc` to `service.go`. `TestServiceImportsStayAgentSide` fails. Remove it.

## Task 2: `app.Embedded` and `ServiceDeps`

**Goal:** the runtime half of `app.Run` is `Embedded` and `ServiceDeps`, and `Run` (still in `app`) is a thin loop over them. Every existing test passes unchanged.

**Files:** `internal/app/servicedeps.go` (new), `internal/app/embedded.go` (new), `internal/app/embedded_test.go` (new), `internal/app/app.go`, `internal/app/app_test.go`.

**Steps:**

1. **`ServiceDeps`.** Move the body of `Run`'s `Deps` block (`app.go:2723` to `:2790`, from `providerReady` to `bindTurnDeps`) into `ServiceDeps`, with these changes and no others:
   - the state, the brokers, the project ID, the LSP manager and the data directory come from `rt`'s fields, as `Run` read them after each start;
   - `ReloadConfig` is `rt.ConfigReloader`, which is `Run`'s closure in embedded mode;
   - `ReloadLayers` is `layerReloaderFor(rt.HomeDir, rt.WorkingDir, rt.State.Trusted)`;
   - `TrustRefresh` refreshes the config hash in the trust store under `config.DataDir(rt.HomeDir)`, as `trustRefresh` does;
   - `HomeDir` is `rt.HomeDir`, and `Context` is `ctx`;
   - `TrustPrompt` and `DecideTrust` stay unset;
   - `Work` is set only when the provider is ready, computed from the provider notice as `Run` does;
   - it ends with `bindTurnDeps`, as the block does.
2. **`Embedded`.** Move `Run`'s option handling and the runtime-side steps of its loop into `Embedded`, keeping their order and their exact behaviour:
   - `NewEmbedded`: the defaults, the caller's options applied once, `deferTrustPrompt`, the credential resolver resolved once and stored back, the directories, the reloader closure over the current runtime (as `Run`'s, under `NewEmbedded`'s `ctx`), first run, and the host registry with the injected workers when none was given;
   - its trust decision: `trustDecide`'s two branches, recording that a reload is wanted;
   - `Start`: `startRuntime` with the resolved options, then the `marshal started` log line, the embedding warning when no worker was injected, the OAuth refresh workers, the secret prompt and the exit-analysis operation;
   - `SessionDeps`: `ServiceDeps(ctx)`, with `TrustPrompt` set to the working directory when `rt.TrustPromptPending`, and `DecideTrust` set to the trust decision;
   - `Resume`, `TakeTrustReload` and `Finish` exactly as the matching branches of the loop (`:2878`, `:2923` and `:2891`);
   - `Close`: the deferred closes of `Run`, in the same order (the runtime, then the registry it created, with `hostCloseTimeout`).
3. **`Run` over `Embedded`.** `Run` keeps its early return, builds an `Embedded` (adding its program runner to its own copy of the options as today), wraps `ctx` in the signal context before `NewEmbedded`, defers `Close`, and loops: `Start`; build the adapter from `SessionDeps`; build the TUI options as today, reading the directories and first run from `Embedded`; return nil if `ctx` is done; run the program; then `Resume` and continue, or `TakeTrustReload` and continue, or return `Finish`. `Run` still lives in `app.go` and still imports `tui` after this task.
4. **`TestRunResolvesOptionsOnce` becomes `TestNewEmbeddedResolvesOptionsOnce`** in `embedded_test.go`: the counting option is applied exactly once by `NewEmbedded`, followed by `Start`, `Resume`, a second `Start` and `Finish`. Delete the `Run` version from `app_test.go`.
5. **`TestServiceDepsServesTheRuntime`** (`embedded_test.go`): over a runtime started with `StartRuntime` as `TestAdapterAccessFollowsNewSession` starts one, the service built from `ServiceDeps` names the runtime's tools, reports a reload path, and serves work. Over a runtime whose provider build failed, it serves no work, as `providerReady` decides.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go vet ./internal/app/
TMPDIR=/private/tmp/p3-2-suite go test ./internal/app/ -count=1
TMPDIR=/private/tmp/p3-2-suite go test ./internal/app/ -run 'TestRun|Embedded|ServiceDeps|Adapter' -race -count=1
```

Expected: the package passes, every `TestRun…` test unchanged, the two new tests with them, and the run tests also pass under `-race`.

Mutation check: drop `Tools` from `ServiceDeps`. `TestRunWiresToolNames` and `TestServiceDepsServesTheRuntime` fail. Restore it.

## Task 3: `launch` takes the TUI half

**Goal:** `launch.Run` is embedded mode, `cmd/marshal` calls it, and `app`'s non-test code imports neither `tui`, nor Bubble Tea, nor `inproc`.

**Files:**
- `internal/launch/launch.go` (new), `internal/launch/launch_test.go` (new), `internal/launch/main_test.go` (new);
- `internal/app/app.go`, `internal/app/app_test.go`, `internal/app/tui_seam_test.go`, `internal/app/layering_test.go` (new);
- `cmd/marshal/main.go`, `cmd/marshal/main_test.go`;
- comments naming `app.Run`: `internal/tools/native/native.go:302`, `internal/app/session/session.go:212` and `:1627`, `internal/worker/worker.go:2`.

**Steps:**

1. **`launch.Run`.** Move `Run`'s remaining body into `launch.Run`, unchanged in behaviour: the early return, the signal context, `NewEmbedded` with the options `WithApp` collected, the deferred `Close`, and the loop of Task 2 step 3. Move `ProgramResult`, `ProgramRunner`, `runProgram` and `WithProgramRunner` from `app.go` too. `launch` builds the adapter with `inproc.New(e.SessionDeps(ctx))`.
2. **`app` lets go.** Delete `Run`, the moved types and functions, and the options' `programRunner` field from `app`, along with the default it had in `StartRuntime` (`runtime.go:1564`). Drop `app.go`'s imports of `tui`, Bubble Tea and `app/clipboard` once unused (`inproc` went in Task 1).
3. **The tests move.** Move the 27 remaining `Run` tests from `app_test.go` to `launch_test.go` (package `launch`), with these changes only:
   - `Run(…)` stays `Run(…)`; each `app` option is wrapped in one `WithApp(…)`, and `WithProgramRunner` and `ProgramResult` are `launch`'s own;
   - copy the helpers they use (listed under Assumptions) into `launch_test.go`, with `modelStateOf` reading the state through `inproc.Session`'s `State`, as today;
   - `TestRunLogsToFileNotTerminal` builds the log path as `filepath.Join(dir, ".marshal", "marshal.log")`, the path `logPath` returns;
   - `TestWithProgramRunnerNilLeavesRunnerConfigurable` tests `launch.WithProgramRunner`.

   `main_test.go` gives the package `testenv.Run`, as `internal/app`'s `TestMain` does. Delete from `app_test.go` and `tui_seam_test.go` every helper no remaining `app` test uses. The three tests listed under Assumptions stay in `app`, with `testTUIModelWith`.
4. **`cmd/marshal`.** `appRunner` becomes `launch.Run`, and `main_test.go`'s replacement takes `...launch.Option`.
5. **The guard.** Write `TestAppImportsNoClient`, as `TestServiceImportsStayAgentSide` walks its directory, over `internal/app`'s own non-test files (not its subdirectories). It reports `marshal/internal/app/tui` or a path under it, `charm.land/bubbletea/v2`, and `marshal/internal/acpclient/inproc`.
6. **Comments.** In the four comments listed under Files, `app.Run` becomes `launch.Run` where it means the embedded loop, or `app.StartRuntime` where it means building the runtime. Read each before changing it.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
go vet ./internal/app/ ./internal/launch/ ./cmd/marshal/
TMPDIR=/private/tmp/p3-2-suite go test ./internal/launch/ ./internal/app/ ./cmd/marshal/ -count=1
TMPDIR=/private/tmp/p3-2-suite go test ./internal/launch/ -race -count=1
go list -deps ./internal/app | grep -E 'internal/app/tui|bubbletea|acpclient/inproc'
```

Expected: everything builds; the three packages pass, and `launch` passes under `-race`; the last command prints nothing (V7: `app` depends on no client code, directly or not).

Mutation check: add a blank import of `charm.land/bubbletea/v2` to `internal/app/app.go`. `TestAppImportsNoClient` fails. Remove it.

## Task 4: The docs and full verification

**Goal:** the spec, the agent guide and the architecture doc describe the layering as it is.

**Files:** `docs/daemon/p3-acp-transport.md`, `AGENTS.md`, `docs/ARCHITECTURE.md`, `docs/daemon/design.md`.

**Steps:**

1. **The counts.** Re-measure the P3 spec's "Where the code is today" counts as P2 plan 9 measured them: interface methods and their ACP rows from `acpclient.Methods`, and the methods `acp.Server` registers. Record the result in the Implementation record. Change the section only if a number moved.
2. **The Implementation record.** Add a "Plan 2 — layering" entry after plan 3a's. It names the service's package and its import rule, the aliases, `ServiceDeps` and what it leaves to the embedding, `Embedded` and its methods, `launch.Run` and its options, which tests moved where, and the two guards. Plan 6a adds its entry at the same place. If both branches have merged, keep both entries, in merge order.
3. **`AGENTS.md`.**
   - The map: `internal/acp/` gains `internal/acp/service/` (the agent side of the client seam, which `inproc` aliases until P3 plan 8); the `acpclient` line says `inproc/` is aliases over that service; a new `internal/launch/` line under "App shell & config" (embedded mode's composition root: the loop, the program, the TUI).
   - "Testing notes": `app.Run()`'s functional options become `launch.Run`'s, which carry the program runner and wrap `app`'s runtime options (`WithConfigLoader`, `WithNow`, …); the run tests live in `internal/launch/launch_test.go`. The `inproc.Deps.HomeDir` advice stays: the alias keeps it true.
4. **`docs/ARCHITECTURE.md`.** The "Client seam" row (`:82`) names `internal/acp/service/` as the implementation, with `inproc` its in-process transport until P3 plan 8. Add `internal/launch/` where the app shell is described, and fix any sentence that says `app.Run` runs the TUI.
5. **`docs/daemon/design.md`.** Where §4.5 names `internal/acpclient/inproc` (`:221`), add that P3 plan 2 moved its logic to `internal/acp/service`.
6. **Main may have moved.** If plan 6a merged while this branch ran, merge `main` into the branch before the full verification. Expect a conflict only in the P3 spec's Implementation record. Keep both entries.
7. **Full verification:**

```bash
cd /Users/alecpullen/projects/marshal
mkdir -p /private/tmp/p3-2-suite
gofmt -l internal cmd
go vet ./...
TMPDIR=/private/tmp/p3-2-suite go test ./... -timeout 20m 2>&1 | tail -150
```

Expected:
- `gofmt -l` prints nothing, and `go vet` is clean;
- every package passes, `internal/launch` and `internal/acp/service` among them. Known load flakes, each to be rerun alone before treating it as new:
  - `internal/acp`: `TestStackPatchApplyBudget`, `TestStackRoundTripBudget`, `TestResponseBarrierBudget`;
  - `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce`, `TestRuntimeCloseRunsNoGitGC`;
  - `internal/snapshot`: `TestRunManagedCancellationKillsTree`;
  - `web/bridge`: `TestRecipeRunMaxUSDSetsAPausingCapForTheRun`, which hangs about one run in three in its fake agent. A hang there is known.
8. **A manual run.** Build `CGO_ENABLED=1 go build ./cmd/marshal` and start it in a scratch repository. It opens, answers a prompt, `/new` swaps the session, `/resume` restarts on another session, and Ctrl+C exits with the usual warning. Report what you saw.

## Self-review

- **Spec coverage.** R1, the service and its home: Task 1. R1, `launch` and `app` stopping importing `tui`: Tasks 2 and 3. R11 (one process, same log file and data directory): unchanged, since `Embedded` starts runtimes exactly as `Run` did. V7: `TestAppImportsNoClient`, `TestServiceImportsStayAgentSide` and the `go list -deps` check. V8: Task 4's full run. Migration order item 2's re-measure: Task 4 step 1.
- **What could go wrong.**
  - **A behaviour hidden in the loop's order.** Task 2 rebuilds `Run` on `Embedded` while every `TestRun…` test still calls `Run`, so a reordering shows before anything moves. The embedding warning and the workers now start before the `Deps` are built rather than after. Nothing between the two reads the transcript or the workers.
  - **The aliases drift from the service.** They are aliases, not copies, so they cannot. A ninth exported name in the service would need a ninth alias only if a client used it, and the build would say so.
  - **A test that needed `app`'s internals moves.** Only `TestRunResolvesOptionsOnce` did, and it stays in `app`. The three TUI-over-runtime tests stay too.
  - **Plan 6a.** It edits TUI files only. None of the moved `Run` tests drives a call plan 6a makes asynchronous: `TestRunWiresTheConfigSeam` calls `ReloadConfig` on the session directly, not through the TUI.
