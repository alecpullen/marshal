# P4 plan 5: The TUI's daemon mode — implementation plan

Date: 2026-11-11. Status: ready for review.
Base: `main` at `29851a61` (P4 plans 3 and 4 merged). Every anchor below was checked there. The full suite ran there (`TMPDIR=/private/tmp/p4bt`, `-timeout 20m`): 121 packages ok, `web/bridge` and `test/daemon` included. The one failure was `internal/acp`'s `TestStackRoundTripBudget`, the known load flake, which passed three times when rerun alone.
- **The spec edits are already made,** uncommitted in the main checkout, in `p4-local-daemon.md`: the header, a note under decision 4, D3's idle-timeout pointer, D5's new "The details (plan 5)" block, D7's "The details (plan 6)" block, the new D7a, D11, H8 and H9, the Design table, Migration order items 5 and 6, and a placeholder record for each of plans 5 and 6.
- **Plan 6 runs beside this one.** It touches the bridge, `cmd/webbridge`, `internal/daemon`, `internal/app/config` and `cmd/marshal/daemon.go`, none of which this plan touches. Both plans fill their own placeholder in the spec's Implementation record, and nothing else in the spec.

Approved design: [p4-local-daemon.md](../p4-local-daemon.md): Goal, decision 4, D5 (with its details block), D11's warning, Migration order item 5, Tests W1, W5, W6 and W10, Done when.
Previous plans: [P4 plan 3, concurrent viewer connections](2026-11-10-p4-3-viewers-plan.md) and [P4 plan 4, project children and the relay](2026-11-10-p4-4-relay-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations only. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** plain `marshal` in a project starts its session on the local daemon when one answers, quitting leaves that session and its run going, and `marshal attach` comes back to it as if the TUI had never left.
- `launch` checks the daemon once, attaches over the relay, and runs the TUI on the attached stream.
- The quit key and `/detach` leave the session running; `/exit` still ends it.
- A TUI follows a run it did not start, from its start to its outcome.
- `marshal attach [id]` reattaches; `--embedded` forces today's mode.

**Non-goals:**
- **No bridge, `internal/daemon`, config or `cmd/marshal/daemon.go` change.** Plan 6 owns those, the idle timeout included.
- **No agent-side change.** No method, field or code is new on the wire. `acpclient.Methods` and the registered method count are unchanged.
- **No trust prompt over the relay.** An untrusted project runs embedded until plan 7 (D8).
- **No watch wake while detached** (H8), and no environment warning in the TUI (H9).
- **No changelog entry.**

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p4-5-suite`. Create the directory first: `mkdir -p /private/tmp/p4-5-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `test/daemon`.
- **The launch loop** (`internal/launch/launch.go`).
  - `Run` (`:64`) builds `app.NewEmbedded`, then loops: `Start`, `service.New(embedded.SessionDeps(ctx))`, `dialAgent` (`pipe.go:51`), the TUI options (`:104` to `:130`), the program under a context that ends when the connection is lost (`:143` to `:152`), the close, and then the lost check, `ResumeSession` and the trust reload.
  - `runProgram` (`:192`) reads `ResumeSession()` from the final model through an interface.
  - `ProgramResult` (`:24`) carries `Err` and `ResumeSession`.
  - `agentConn` (`pipe.go:24`) is the model for a connection's lost tracking: `lost`, `markLost`, `connectionLost` and a `close` that claims the shutdown.
- **The daemon's client** (`internal/daemon/client.go`): `NewClient(socket)`, `Info(ctx)` (`:74`), and `Attach(ctx, root, sessionID)` (`:133`), which returns an `*Attachment{SessionID, Conn}` or an `*AttachError`. `Info.Children[i].Sessions` are sorted most recently active first by the bridge (`Registry.summaries`). `ChildInfo.Root` is the cleaned root the attach named.
- **Paths:** `config.DaemonSocketPath(home)`, `config.DaemonDir(home)` and `config.DataDir(home)` (`internal/app/config/locations.go:36` to `:52`); the trust store lives in `config.DataDir(home)` (`internal/app/embedded.go:116`). `repo.Root(dir)` (`internal/repo/path.go`) is the git root, or `dir` outside a repository.
- **Trust:** `trust.Evaluate(store, dir)` (`internal/trust/trust.go:284`) reports `needsPrompt` without prompting.
- **The client:** `rpc.Dial(ctx, rpc.NewStream(r, w, rpc.MaxFrameBytes), rpc.DialOptions{SharedFilesystem: true})` (`internal/acpclient/rpc/dial.go:54`; `ctx` bounds `initialize` only), then `Host.Attach(ctx, id)`. `Host.Agent()` (`:119`) is the agent's `initialize` info; `Host.Done()` and `Host.Err()` report a lost connection. `test/daemon`'s `daemonHarness.attach` (`attach_test.go:150`) is the same sequence.
- **The TUI** (`internal/app/tui/model.go` unless named).
  - `New` (`:1116`); options are `func(*Model)` (`Option`, `:574`); `Session()` (`:601`); `ResumeSession()` (`:6520`).
  - The Ctrl+C handling is at the top of `update` (`:1662`): pressed twice it stops a run (`ctrlCStop`) when `m.busy || m.agentCancel != nil`, else it quits through `m.beginEnd(nil)` (`ctrlCQuit`). While an ending is under way it calls `beginEnd(nil)`. The armed hint is `statusRightSegment` (`status.go:503`).
  - `/exit` and `/quit` are TUI effects in `tuiCommandEffects` (`commands_dispatch.go:251`); the commands are registered TUI-only in `commands.RegisterAll` (`internal/commands/commands.go:59`).
  - `finishQuit` (`:4787`) is the end flow's last step: it settles a call in flight, cancels `agentCancel` and `lspCancel`, closes the session and releases the dock's retained surfaces.
  - `beginTurn` (`:3960`) is the only place `m.busy` is set true; it sets `agentCancel`. `startSessionRun` (`:3984`) and `sessionRunCmd` (`:3996`) start a run and report `agentFinishedMsg{err, refusal}` (`:4136`). `refusedStart` (`:4013`) recognises `CodeBusy`, `CodeQuiescing`, `CodeNoRunner` and `CodeSessionClosed`.
  - `startWatchResumeRun` (`:4033`) and `flushQueuedSteering` (`:4063`) start follow-on runs with the refusal "Cannot start work".
  - `handleAgentFinished` (`:4935`) and `handlePlanAuthorFinished` (`:5120`) are the two finish handlers. `handleAgentFinished` uses `m.cancelling` to tell the TUI's own stop from a failure, clears the steering after its own stop, sets or clears the provider notice, pulses on success, computes the suggestion, flushes queued steering and takes a pending watch resume.
  - `handleChanged` (`:5166`) runs on every `changedMsg`; it acts on `st.WatchResume` when `Seq > m.watchResumeSeq` (`:5183`). `m.watchResumeSeq` is reset to 0 on a new session (`commands_dispatch.go:216`).
  - `cancelTurn` (`:4260`) stops through `sess.Stop` and works whenever `m.busy` is set.
  - `noticeForError` builds the provider notice for an error; `acpclient.IsCode` and `acpclient.CodeOf` (`internal/acpclient/errors.go:14`) read an `*acpwire.Error`'s code.
- **TUI test helpers that exist:** `testModelWith` and `withPromptRunner` (`seam_test.go:67`, `:91`), `testModelTurns` (`turns_seam_test.go:103`), `blockingAgentRunner` (`model_test.go:1250`), `waitRunClear` (`pipelines_seam_test.go:236`), `newTestModel` (`model_test.go:6714`), and `m.Update(changedMsg{ch: m.sess.Changed()})` as `jobs_test.go:52` delivers a change.
- **Launch test helpers that exist:** `pipeAppOptions` (`pipe_test.go:35`), `withServed` (`pipe.go:202`) and `WithProgramRunner`.
- **The end-to-end harness** (`test/daemon`): `newDaemonHarness` (`attach_test.go:35`) runs a real bridge on a short socket with this test binary as the project child; `waitFor` (`:358`) polls. A child inherits the test process's environment, so a `MARSHAL_CONFIG_DIR` set with `t.Setenv` before the first attach is the child's. `test/usability/provider_fixture_test.go`'s `fixtureProvider` is the model for a test-owned OpenAI-compatible endpoint (`/v1/models`, streamed `/v1/chat/completions`), and `agentChildConfig` (`test/usability/usability_test.go:245`) is the model for its config. `approvalCommand` (`approval_journey_test.go:19`) is a `shell.run` command that asks for approval in `act` mode with `auto` off.
- **`cmd/marshal`** (`main.go`): top-level flags are parsed at `:47`; `appRunner = launch.Run` (`:20`) is called as `appRunner(ctx, stdout)` (`:125`); subcommands dispatch by `args[0]`. `main_test.go:232` stubs `appRunner` with the variadic signature already.

**Decisions this plan makes beyond the spec** (D5's details block records them):

1. **The check lives in `launch`, the socket comes from `cmd/marshal`.** `launch.Run` probes only when given `WithDaemon(socket)`. Without it, every existing caller and test stays embedded and never dials anything.
2. **Daemon mode needs a `local` answer and a project whose trust needs no question.** Anything else runs embedded, silently. The trust condition goes when plan 7 brings D8.
3. **The quit key detaches only when nothing runs.** Ctrl+C pressed twice keeps stopping a run first, in both modes. `/detach` leaves at any time. `/exit` and `/quit` end the session in both modes.
4. **Following is mode-independent.** The TUI follows any run its status shows that it did not start. Embedded mode never has such a run, so it sees no difference.
5. **A run still in the status when the TUI's own call ended by its own cancellation is the TUI's own.** That run is still stopping, so the TUI ignores it rather than following it, and goes idle as it does today. Any other run left in the status after the TUI's own run ended (a refusal, or another viewer's run admitted right after) is followed.
6. **A follow-on start refused with `CodeBusy` is quiet.** A watch resume or a steering flush that the session refuses because another run holds the slot writes no note: with two viewers, both answer one wake or one queue, and one wins. The steering text is put back agent side, as today.
7. **A new model starts with the status's watch-resume sequence.** Attaching never replays a wake that fired before.
8. **The version note is a transcript note,** written once by `launch` before the program starts, through the session. It names both versions.
9. **The client log** is `config.DaemonDir(home)/tui.log`, opened `0600` in append mode, and closed when `Run` or `Attach` returns.

**Execution:**
- In the main checkout, if `git status` shows any of these uncommitted, commit them to `main` first as explicit paths, with the message `docs/daemon: record P4 plans 5 and 6`:
  - `docs/daemon/p4-local-daemon.md`;
  - this plan and `docs/daemon/plans/2026-11-11-p4-6-environment-plan.md`.

  Plan 6's executor may have committed them already. The main checkout holds other untracked files (`scripts/lmstudio-bench/`, `docs/fleet-cli/` and others). Leave them alone.
- Then branch: `git switch -c p4-5-daemon-mode` from `main`, or create a worktree on that branch.
- Run the tasks in order. Run each task's Verify and commit it as `p4-5-daemon-mode: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The TUI (`internal/app/tui`)

```go
// WithDetachOnQuit makes the quit key and /detach leave the session running
// (P4 D5): launch passes it in daemon mode.
func WithDetachOnQuit() Option

// Detached is the ID of the session a detach left running, or "" when the
// program did not end by detaching. The launch loop reads it from the final
// model.
func (m Model) Detached() string

// On Model:
	detachOnQuit bool   // WithDetachOnQuit
	detached     bool   // set by detach
	following    string // the ID of the run this TUI follows; "" when none
	ignoreRun    string // the ID of the TUI's own run that is still stopping

// On agentFinishedMsg, beside err and refusal:
	quietBusy bool // a follow-on start: a CodeBusy refusal writes no note
	followed  bool // the end of a followed run, built from lastRun

// syncFollowedRun starts following a run the status shows that this TUI did
// not start, or finishes the one it follows once the status no longer shows it.
func (m Model) syncFollowedRun(st acpwire.SessionStatus) (Model, tea.Cmd)

// afterRun ends every return path of the two finish handlers: it records the
// TUI's own still-stopping run (decision 5), runs the follow check while the
// model is idle, and batches what that returns with cmd.
func (m Model) afterRun(err error, followed bool, cmd tea.Cmd) (Model, tea.Cmd)
```

### The launch loop (`internal/launch`)

```go
// On ProgramResult, beside Err and ResumeSession:
	Detached string // the session a detach left running; "" otherwise

// WithDaemon makes Run try the local daemon on socket first (P4 D5): when a
// local-profile daemon answers and the project needs no trust question, the
// TUI runs over the relay; otherwise Run runs embedded.
func WithDaemon(socket string) Option

// Attach runs the TUI on a daemon session (marshal attach): sessionID, or the
// project's most recently active session when it is empty. It fails when no
// daemon answers on socket.
func Attach(ctx context.Context, stdout io.Writer, socket, sessionID string, opts ...Option) error

// daemonClient is the part of *daemon.Client the daemon mode uses.
type daemonClient interface {
	Info(ctx context.Context) (daemon.Info, error)
	Attach(ctx context.Context, root, sessionID string) (*daemon.Attachment, error)
}

// Test seams, unexported, beside withServed:
func withDaemonClient(c daemonClient) Option // replaces the client built for the socket
func withWorkingDir(dir string) Option       // replaces os.Getwd
func withAgentVersion(v string) Option       // replaces acp.AgentVersion as this build's version

const (
	daemonProbeTimeout     = 500 * time.Millisecond
	daemonHandshakeTimeout = 30 * time.Second
)
```

### The command (`cmd/marshal/main.go`)

```go
var attachRunner = launch.Attach
```

## Task 1 — following a run, and the follow-on refusals

**Files:** `internal/app/tui/model.go`, a new `internal/app/tui/follow_test.go`.

1. Add the `following` and `ignoreRun` fields and the two `agentFinishedMsg` fields (Contracts).
2. Add `syncFollowedRun(st acpwire.SessionStatus) (Model, tea.Cmd)` on the value receiver, as the other handlers are:
   - When `m.following` is set and `st.Run` is nil or has another ID, the followed run has ended. Clear `m.following`, build the finish's error from `st.LastRun` when its ID is the followed one (nil for `done`; `context.Canceled` for `cancelled`; `st.LastRun.Error` for `failed` (assigned to the `error` only when it is not nil, so a nil pointer never becomes a non-nil interface; a `failed` without one gets an `*acpwire.Error` whose message is "the run failed"); an `*acpwire.Error` with `CodeHumanGateRequired` for `gated`; nil when `lastRun` is another run's or missing), and return `m.handleAgentFinished(agentFinishedMsg{err: thatError, followed: true})`.
   - Otherwise, when `m.following` is empty, `st.Run` is set, its ID is not `m.ignoreRun`, `m.busy` is false, `m.agentCancel` is nil and `m.endStage == endIdle`, start following: set `m.following` to the run's ID, `m.busy` true, `m.turnStartedAt` to `acpwire.Time(st.Run.StartedAt)`, start the held window as `beginTurn` does, and return `tickCmd()` and `spinnerTickCmd()` batched. Do not touch `agentCancel` and do not clear the provider notice.
   - Clear `m.ignoreRun` once `st.Run` is nil or has another ID.
   - Otherwise return the model and nil.
3. In `handleChanged`, call `syncFollowedRun` with the status it already read, after `m.syncWork()`, and batch its command with the others.
4. In `handleAgentFinished`, at the very top, handle a quiet refusal: when `msg.quietBusy` is set and `acpclient.IsCode(msg.err, acpwire.CodeBusy)`, go idle as the refusal branch does (busy, `turnStartedAt`, `agentCancel`, `cancelling`) without a note, refresh the viewport, and then return what `syncFollowedRun(m.sess.Status())` returns.
5. On every return path of `handleAgentFinished` (the refusal branch, the gate branch and the normal end) and of `handlePlanAuthorFinished`, after every command the path builds (the steering flush and the watch resume included), apply decision 5 and then the follow check. Put both in one helper, `afterRun` (Contracts), that each path returns through with its own error, whether it was a followed run, and the command it built:
   - when `msg.followed` is false, the call ended with an error that `errors.Is(err, context.Canceled)`, and `m.sess.Status().Run` is set, record that run's ID in `m.ignoreRun`;
   - then, when `m.busy` is still false, call `syncFollowedRun(m.sess.Status())` and batch its command.

   `handlePlanAuthorFinished`'s message is `planAuthorFinishedMsg` (`sdd_authoring.go:14`); read its `err` there.
6. In `startWatchResumeRun` and `flushQueuedSteering`, mark their runs `quietBusy` (decision 6). Give `startSessionRun` and `sessionRunCmd` a `quietBusy bool` parameter carried into `agentFinishedMsg`; every other caller passes false.
7. In `New`, after the options are applied, set `m.watchResumeSeq` from `m.sess.Status().WatchResume` when it is not nil (decision 7).
8. Tests in `follow_test.go`. Each builds a model with `testModelWith` over a state whose runner is a `blockingAgentRunner`, and starts the "other" run by calling `m.sess.Prompt(ctx, acpwire.PromptParams{Text: "other"})` on a goroutine of the test, never through the model, so the model holds no run call of its own. Deliver changes with `m.Update(changedMsg{ch: m.sess.Changed()})`, polling until the status shows what the test waits for, as `waitRunClear` polls.
   - `TestARunTheTUIDidNotStartIsFollowed`: once the status shows the run, a change makes `m.busy` true and `m.following` the run's ID, and the returned command is not nil.
   - `TestTheEndOfAFollowedRunFinishesItLocally`: releasing the runner and delivering a change once `Status().Run` is nil clears `m.busy` and `m.following`, sets the success pulse and writes no note of the TUI's own.
   - `TestAFollowedFailureSetsTheNotice`: a runner that returns an error ends the followed run with the provider notice set and no second failure row (the service writes "✘ Turn failed: …" once).
   - `TestStoppingAFollowedRunIsTheTUIsOwnStop`: `cancelTurn` while following stops the run through the session; its end clears `m.cancelling` and the steering queue, and leaves no notice.
   - `TestTheTUIsOwnStoppingRunIsNotFollowed`: the model starts its own run with `startSessionRun` over `m.sess.Prompt(ctx, acpwire.PromptParams{Text: "x"})`, running the batch's first command on a goroutine as `TestAStoppedAuthoringIsNotAFailure` does. `blockingAgentRunner` ignores its context, so after `cancelTurn` the run stays in the status until the test releases it. The finish message is handled while `Status().Run` still shows the run. `m.busy` stays false after the finish and after a change, and `m.ignoreRun` clears once the run is gone.
   - `TestARefusedStartFollowsTheRunThatHoldsTheSlot`: while the other run holds the slot, the model's own prompt is refused with `CodeBusy`; after its finish message the model follows the other run without waiting for a change.
   - `TestAQuietWakeRefusalWritesNoNote`: an `agentFinishedMsg` with `quietBusy` and a `CodeBusy` error leaves the transcript's note count unchanged and the model idle, while one without `quietBusy` still writes "Cannot start work: …".
   - `TestANewModelDoesNotReplayAnOldWake`: a session whose status already carries a watch resume when `New` runs (a watch fire with a resume, produced as `watch_resume_test.go` produces one, and waited for in the status) gives a model whose first change starts no run. The status's resume comes from the service's `noteResume` (`internal/acp/service/events.go:277`).

**Verify:**
- `go test ./internal/app/tui/ -run 'Follow|Followed|OwnStopping|RefusedStartFollows|QuietWake|OldWake' -count=1`
- `go test ./internal/app/tui/... -count=1`
- Mutation check: drop decision 5's `ignoreRun` record. `TestTheTUIsOwnStoppingRunIsNotFollowed` must fail. Restore it. Then drop the follow check at the end of `handleAgentFinished`. `TestARefusedStartFollowsTheRunThatHoldsTheSlot` must fail. Restore it.

## Task 2 — detaching

**Files:** `internal/app/tui/model.go`, `internal/app/tui/status.go`, `internal/app/tui/commands_dispatch.go`, `internal/commands/commands.go`, `internal/commands/commands_test.go` if it lists the commands, a new `internal/app/tui/detach_test.go`.

1. Register `detach` in `commands.RegisterAll` right after `quit`: TUI-only, group Chat, description "Leave this daemon session running and quit".
2. Add `WithDetachOnQuit` and the `detachOnQuit` and `detached` fields (Contracts), and `Detached()`, which returns `m.sess.ID()` when `m.detached` is set.
3. Add `detach() tea.Cmd` beside `finishQuit`. It cancels a call in flight and drops the queued calls without waiting (the connection's close ends the call), cancels `lspCancel`, releases the dock's retained surfaces exactly as `finishQuit` does, sets `m.detached`, and returns `tea.Quit`. It does not close the session, does not cancel `agentCancel`, and does not touch the end flow's state.
4. In `update`'s Ctrl+C handling, when the armed action fires as `ctrlCQuit` and `m.detachOnQuit` is set, return `m.detach()` instead of `m.beginEnd(nil)`. The `ctrlCStop` path and the `m.endStage != endIdle` path are unchanged.
5. In `statusRightSegment`, the `ctrlCQuit` hint reads "Ctrl+C again to detach" when `m.detachOnQuit` is set.
6. Add a `detach` effect to `tuiCommandEffects`:
   - without `m.detachOnQuit`, note "Only a daemon session can be detached; /exit ends this one." and refresh;
   - while `m.endStage != endIdle`, note "The conversation is ending; it cannot be detached now." and refresh;
   - otherwise return `m.detach()`.
7. If a golden that lists the command palette changes, rewrite it with the package's `-update` flag (`transcript_golden_test.go:28`) and name the files in the record.
8. Tests in `detach_test.go`:
   - `TestTheQuitKeyDetachesAnIdleDaemonSession`: with `WithDetachOnQuit`, two Ctrl+C presses return `tea.Quit`; the final model's `Detached()` is the session's ID; the session was not closed (the state has not ended) and no end warning opened.
   - `TestTheQuitKeyStillStopsARunFirst`: with `WithDetachOnQuit` and a run in flight (`blockingAgentRunner`), two presses stop the run and do not detach.
   - `TestDetachLeavesARunGoing`: `/detach` during a run returns `tea.Quit`, `Detached()` is set, and `Status().Run` is still set afterwards.
   - `TestDetachIsRefusedInEmbeddedMode`: without the option, `/detach` writes its note and returns no quit.
   - `TestExitStillEndsADaemonSession`: with the option, `/exit` opens the end flow as without it.
   - `TestTheArmedHintSaysDetach`: one Ctrl+C at idle with the option shows "Ctrl+C again to detach" on the status line, and "Ctrl+C again to quit" without it.

**Verify:**
- `go test ./internal/app/tui/ -run 'Detach|QuitKey|ExitStillEnds|ArmedHint' -count=1`
- `go test ./internal/app/tui/... ./internal/commands/... -count=1`
- `go test ./internal/app/tui/ -run TestTUIStaysOnTheClientSeam -count=1`
- Mutation check: make `detach` close the session. `TestTheQuitKeyDetachesAnIdleDaemonSession` must fail. Restore it.

## Task 3 — the daemon mode in `launch`

**Files:** `internal/launch/launch.go`, a new `internal/launch/daemon.go`, a new `internal/launch/daemon_test.go`.

1. Add `Detached` to `ProgramResult`, and have `runProgram` read it from the final model through an interface, as it reads `ResumeSession`.
2. Add the options and seams (Contracts) to `options`: the socket, the client, the working directory and the agent version.
3. In `Run`, after the signal context and the options, and before `app.NewEmbedded`: when a socket was given, call `chooseDaemon(ctx, opts)`. When it reports daemon mode, return `runDaemon(ctx, stdout, opts, "")`; otherwise continue embedded exactly as today.
4. In `daemon.go`:
   - `chooseDaemon` resolves the working directory (the seam, else `os.Getwd`), its `repo.Root`, and the home (`os.UserHomeDir`). It reads `Info` with `daemonProbeTimeout`. It reports daemon mode only when the call succeeds, the profile is `local`, and `trust.Evaluate(trust.NewStore(config.DataDir(home)), root)` reports no prompt and no error (decision 2). It returns the root and home it resolved.
   - `Attach` builds the options as `Run` does, resolves the root and home the same way, and reads `Info` with `daemonProbeTimeout`. A failed read returns an error naming the socket and saying `marshal daemon install` starts a daemon. With an empty `sessionID`, it takes the first session of the child whose `Root` equals the root, and fails with "no daemon session for <root>; `marshal` starts one" when there is none. It skips the trust condition. Then it calls `runDaemon`.
   - `runDaemon(ctx, stdout, opts, sessionID)` opens the client log (decision 9) and loops:
     1. `Attach` the root and the session ID (empty for a new session) with `daemonHandshakeTimeout`. A refusal returns its error.
     2. `rpc.Dial` over `rpc.NewStream(conn, conn, rpc.MaxFrameBytes)` with `SharedFilesystem` and no `AgentVersion`, then `Host.Attach` of the attachment's ID, both under the handshake's context. Close the stream on any failure.
     3. When `Host.Agent().Version` differs from this build's version (the seam, else `acp.AgentVersion`), add one plain note to the session: "This marshal is <ours>; the daemon's agent is <theirs>. Restart the daemon after upgrading: `marshal daemon stop` then `marshal daemon start`." Show an empty version as `dev`.
     4. Build the TUI options: `WithDetachOnQuit`, `WithHomeDir(home)`, `WithWorkingDir` of the session's `Status().WorkingDir` (else the root), the file index as `Run` does, `WithOpenConnectOnStart` when `config.UserConfigPath(home)` does not exist, the clipboard as `Run` builds it, `WithContext(ctx)` and `WithLogger` of the client log.
     5. Run the program under a context that ends when `Host.Done()` closes before the loop closed the host itself, as `agentConn` tracks a loss.
     6. Close the host. Then, in this order: a lost connection returns "launch: the daemon's connection closed: <err>; `marshal attach <id>` reattaches", with the session's current ID; a `Detached` result prints "Detached from session <id>. `marshal attach <id>` reattaches." to `stdout` and returns nil; a `ResumeSession` with no error sets the session ID and loops; anything else returns the program's error.
5. Tests in `daemon_test.go`. The fake `daemonClient` answers `Info` from a field and serves `Attach` by starting a runtime with `app.NewEmbedded(ctx, pipeAppOptions(dir, home)...)` and `Start`, building its pair with `service.New(embedded.SessionDeps(ctx))`, adopting it into an `acp.NewEmbedding` and serving the embedding on one end of a `net.Pipe`, as `dialAgent` does; the attachment carries the runtime's session ID and the pipe's other end. It records each attach's root and session ID, and closes what it started in `t.Cleanup`. Program runners return results directly, as the existing launch tests do.
   - `TestRunUsesTheDaemonWhenItAnswers`: with `WithDaemon` and a local answer, the program's model is over an `*rpc.Session` whose ID is the fake's session, the fake saw the root and an empty ID, and no embedded runtime was started by `Run` (the config loader `WithApp` passes is never called).
   - `TestRunStaysEmbeddedWithoutADaemon`: an `Info` error runs embedded; so does a `server` profile.
   - `TestRunStaysEmbeddedForAProjectThatNeedsTrust`: a working directory with `.marshal/config.toml` and no trust record runs embedded, and the fake saw no attach.
   - `TestRunWithoutTheOptionNeverDials`: no `WithDaemon` means the fake's `Info` is never called.
   - `TestAttachWithoutADaemonFails`: the error names the socket and `marshal daemon install`.
   - `TestAttachWithNoIDTakesTheMostRecentSession`: `Info` lists two sessions for the root and one for another root; the attach names the root's first.
   - `TestAttachWithNoSessionFails`: the root's child has no session, or there is no child for the root.
   - `TestADetachPrintsTheReattachCommand`: a runner returning `Detached` makes `Run` return nil with the sentence on `stdout`.
   - `TestAResumeReattachesToTheChosenSession`: a runner returning `ResumeSession: "x"` makes the second attach name "x".
   - `TestALostConnectionNamesTheReattach`: the fake closes its pipe end while the runner waits on its context; `Run` returns an error naming `marshal attach` and the session's ID.
   - `TestAVersionSkewWritesOneNote`: with `withAgentVersion("other")`, the session's transcript holds exactly one note naming both versions; with equal versions, none.
   - `TestTheClientLogIsTheOwnersOnly`: after a daemon-mode run, `config.DaemonDir(home)/tui.log` exists with mode `0600`.

**Verify:**
- `go test ./internal/launch/ -run 'Daemon|Embedded|NeverDials|Attach|Detach|Resume|LostConnection|VersionSkew|ClientLog' -count=1`
- `go test ./internal/launch/ -count=1`
- Mutation check: drop the trust condition from `chooseDaemon`. `TestRunStaysEmbeddedForAProjectThatNeedsTrust` must fail. Restore it.

## Task 4 — `marshal attach` and `--embedded`

**Files:** `cmd/marshal/main.go`, `cmd/marshal/main_test.go`.

1. Add the top-level flag `--embedded` ("run the session in this process, without the daemon").
2. Plain `marshal` (no subcommand) calls `appRunner(ctx, stdout, launch.WithDaemon(config.DaemonSocketPath(home)))`, with `home` from `os.UserHomeDir`, unless `--embedded` was given; with it, `appRunner(ctx, stdout)` as today. A home that cannot be found runs embedded.
3. Add the `attach` subcommand through `attachRunner`: `marshal attach [session-id]`. It takes at most one argument and no flags, and is refused with `--embedded` ("marshal attach: --embedded cannot be combined with attach"). It passes the socket, the ID (or empty) and `stdout`.
4. Tests in `main_test.go`, stubbing `appRunner` and `attachRunner` as `main_test.go:232` stubs `appRunner`:
   - `TestPlainMarshalTriesTheDaemon`: the stub receives one option.
   - `TestEmbeddedSkipsTheDaemon`: with `--embedded`, it receives none.
   - `TestAttachRoutesToTheAttachRunner`: `attach` and `attach abc` reach the stub with "" and "abc" and the socket under the test's home.
   - `TestAttachRefusesExtraArgumentsAndEmbedded`: `attach a b` and `--embedded attach` are refused without calling the stub.

**Verify:**
- `go test ./cmd/marshal/ -count=1`
- `CGO_ENABLED=1 go build ./cmd/marshal`

## Task 5 — the end-to-end reattach

**Files:** a new `test/daemon/provider_test.go`, a new `test/daemon/reattach_test.go`.

1. In `provider_test.go`, add a small test-owned OpenAI-compatible endpoint on `httptest`, modelled on `test/usability`'s `fixtureProvider`: `GET /v1/models` lists one model, and `POST /v1/chat/completions` streams the next scripted reply. A reply can carry a gate: the handler waits on the gate's channel (or the request's context) before it writes. Any request beyond the script, or to another path, is recorded and refused. Add a helper that writes a user config for it into a fresh short directory and sets `MARSHAL_CONFIG_DIR` to it with `t.Setenv`, before the harness starts its first child. The config follows `agentChildConfig`: the fixture as the only provider with `tool_calling = true`, one local-only preset, the profile, `act` mode with `auto` off, narration and the intent nudge off, titling off, and `postmortem.on_exit = "never"`.
2. In `reattach_test.go`:
   - `TestAReattachedClientSeesTheRunToItsEnd` (W1): the script is a gated tool call to `shell.run` with a command that asks for approval (as `approvalCommand`), then a text reply "reattached and done". Client A attaches a new session and prompts on a goroutine; the test waits for A's status to show the run, closes A, and waits for `GET /api/daemon` to show no viewer. Client B attaches by the ID and sees the same run in its status. The test opens the gate; B's pending mirror shows the approval, and B allows it once with `AnswerApproval`. B's status then shows `lastRun` with outcome `done` and the run's ID, and B's transcript carries "reattached and done". The provider saw exactly two completions.
   - `TestTheTUIDetachesFromARunningTurn` (W5): the script is one gated text reply. Client A starts the run and leaves, as above. Then the test changes into the project root (`t.Chdir`) and calls `launch.Attach(ctx, &out, h.socket, id, launch.WithProgramRunner(runner))`. The runner waits until `GET /api/daemon` shows one viewer on the session and returns `launch.ProgramResult{Detached: id}`. `Attach` returns nil and `out` names `marshal attach <id>`. A new client B then sees the run still in its status; the test opens the gate, and B sees `lastRun` with outcome `done`.

**Verify:**
- `go test ./test/daemon/ -count=1`
- `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then `MARSHAL_DAEMON_TEST_MARSHAL=$PWD/marshal go test ./test/daemon/ -count=1`
- Mutation check: make the service's detaching runs derive their context from the call again (`internal/acp/service/turns.go`, the `runSpec`'s detach), and both new tests must fail. Restore it.

## Task 6 — the docs, the record and full verification

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`, `docs/daemon/p4-local-daemon.md` (the "Plan 5" placeholder only).

1. `AGENTS.md`: in the Commands section's list of subcommands and flags, add `attach` and `--embedded`. In the "Web control plane" paragraph about `webbridge --profile local`, add one sentence: plain `marshal` starts its session on the daemon when one answers and the project's trust needs no question, the quit key and `/detach` leave it running, and `marshal attach [id]` comes back to it.
2. `docs/ARCHITECTURE.md`: where it describes the local daemon and the relay (search for "--viewers"), add the TUI's daemon mode in one or two sentences, with the same facts.
3. Replace the placeholder under "### Plan 5 — the TUI's daemon mode" with the record:
   - what was built;
   - every place the plan was short, and how it was filled;
   - the manual run below;
   - the verification results, each with its number;
   - an "Open for plan 7" line on the trust condition in `chooseDaemon`, which D8 removes.

   Touch nothing else in the spec: plan 6 fills its own placeholder.
4. **The manual run,** from the worktree, with the built `marshal` and `webbridge` (`go build ./cmd/webbridge`) and a real provider the user's config already has:
   1. export `MARSHAL_DATA_DIR` as a short scratch directory for every command below, so the daemon's socket, state and registry, and the trust store, are the run's own (the config directory stays the user's, with its provider). Start a daemon by hand: `./webbridge --profile local --addr off --marshal-bin $PWD/marshal`; its socket defaults to `$MARSHAL_DATA_DIR/daemon/marshal.sock`, which is where `marshal` looks;
   2. run `./marshal --trust` in a project (the flag records the trust in the scratch store, so a project config needs no question), send a prompt that takes a while, and press Ctrl+C twice: the run stops (the quit key stops a run first);
   3. send it again and type `/detach`: the terminal shows the reattach line, and `marshal daemon status` (or `GET /api/daemon`) shows the session with no viewer;
   4. run `./marshal attach`: the run is still going or has finished, with its outcome in the transcript;
   5. press Ctrl+C twice at idle: the TUI detaches; `/exit` after a further attach ends the session, and `GET /api/daemon` no longer lists it.

   Record what happened at each step.
5. Full verification, in the worktree:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean;
   - `go test ./... -timeout 20m` passes apart from the known load flakes, each rerun alone;
   - `TestWebIsStdlibOnly` and `TestTUIStaysOnTheClientSeam` pass;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then `go test ./test/usability/... -run TestScripted -timeout 5m` passes.

**Verify:** the commands above, each recorded with its result.

## Done when

- Plain `marshal` in a trusted project starts its session on a running local daemon, and runs embedded when none answers, when the project needs a trust question, or with `--embedded`.
- The quit key at idle and `/detach` at any time leave the session and its run going and print how to come back; `/exit` ends the session.
- `marshal attach` and `marshal attach <id>` reattach, and a run already in flight shows as if the TUI had never left, through to its outcome.
- A TUI follows a run it did not start, and its own stopping run is never mistaken for another's.
- Tasks 1 to 5's tests pass, their mutation checks fail as stated, and Task 6's suite passes.
