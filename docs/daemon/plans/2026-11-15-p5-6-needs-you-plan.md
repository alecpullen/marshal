# P5 plan 6: viewers on agents' own children, needs you, and notifications — implementation plan

Date: 2026-11-15. Status: ready for review.

Base: `origin/main` at `bcceadab`, where plans 3 and 4a are merged. Every
anchor below was checked there.

- **Plan 4b is running, and half of this plan builds on it.** Tasks 1, 4 and
  5 need nothing from it. Tasks 2, 3, 6, 7 and 8 extend its agent attach, its
  session-screen option and its daemon loop, so they wait for plan 4b on
  `main`. Their anchors in plan 4b's files are its Contracts, re-checked then
  (see Execution).
- **The baseline** is CI's suite command at `bcceadab`. Its result is in the
  spec's "Plan 4c" record entry, where this batch's baseline is recorded.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`: F6's and F8's "Decided by plan 6"
  bullets, Migration order item 6, the Design table and the "Plan 6" record
  placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decisions 6 and
8; F6 (host-process agents only); F8; Migration order item 6; Tests W10 and
W11.
Previous plans: [plan 3, the fleet screen](2026-11-14-p5-3-fleet-screen-plan.md),
whose transitions this plan notifies on;
[plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md), whose
attach, session screen and loop this plan extends.
Sibling plans: [plan 5b](2026-11-15-p5-5b-finish-plan.md), whose test child
entry accepts the `--viewers` children this plan starts.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **Every host-process agent can be opened.** An agent with a child of its
  own (a git agent, a workspace agent, or one behind the egress proxy) starts
  that child with a viewer socket, and the agent attach reaches it. Only a
  container's agent is still refused, until plan 8.
- **The session screen shows how many things need you.** In daemon mode its
  status line reads `needs you N`: the queue's items, not counting the shown
  agent's own. It shows nothing at zero, and nothing in embedded mode.
- **The fleet notifies.** A bell and a desktop notification when an agent
  needs you or becomes ready, or a submission arrives; a burst for one agent
  within 2s collapses to one. It keeps notifying on a session screen opened
  from the fleet.

## Non-goals

- **Containers' viewers and the allowlist.** They are plan 8.
- **`fleet.toml`.** Its `notify = false`, for the daemon's `local` table and
  for each server, arrives with the file in plan 7. This plan's switch is a
  launch option, on by default, which plan 7 sets from the file.
- **Muting one agent** (H1), and suppressing a notification for the agent on
  screen (F8: the client cannot tell whether its terminal is in front).
- **The daemon's TUI sessions in the count** (H3).

## Assumptions

Each was checked at `bcceadab`, except where a plan 4b name is cited from its
Contracts.

- **An agent's own child on the local profile is a host process** with no
  arguments: `newRuntime` falls back to `&Child{MarshalBin: marshalBin}` when
  there is no container runtime (`web/bridge/fleet.go:449`, `:462`).
  `startRuntime` builds and starts it (`fleet.go:898`), and `releaseAgent`
  stops it (`fleet.go:1975`, its `Stop` at `:2023`).
- **Which agents get their own child** on the local profile: a git agent, a
  workspace agent, and any agent while the egress proxy is on
  (`fleet.go:835`).
- **A project child's viewer socket** is
  `<state>/v/<16 hex of sha256(root)>.sock` (`web/bridge/projectchild.go:57`,
  `:63`), its directory made `0700` before the child starts
  (`projectchild.go:77`), and the child started with `--viewers unix://…`
  (`projectchild.go:102`). `RunWithViewers` serves stdio and the socket
  together (`internal/acp/viewers.go:60`).
- **Plan 4b's agent attach** refuses an agent with no project child with 409,
  "agent <name> runs on a child of its own, which has no viewer socket yet"
  (plan 4b, Task 1). Its `relay` takes the project child, the session, the
  agent and both connections (plan 4b, Contracts).
- **Plan 3's transitions** (`internal/app/tui/fleet/transitions.go:28`)
  compare two states of one epoch and give needs-you (an agent entering
  `groups.needsYou`, or a submission entering the queue), ready (an agent
  entering `groups.ready`) and failed. A state from another epoch, or one not
  loaded, gives none: that is F8's baseline.
- **The fleet model's wait** on the client's change signal is released by a
  quit gate when the program quits (`internal/app/tui/fleet/model.go:78`), the
  fix plan 3's review made for a program run repeatedly in one process.
- **The session screen's pump** (`internal/app/tui/pump.go:14`) waits on its
  session's change signal, and `handleChanged` ignores a signal from any other
  channel (`internal/app/tui/model.go:5373`). A fleet's signal needs its own
  message.
- **The status line** is built from segments (`internal/app/tui/status.go:287`
  for the left, `:503` for the right).
- **The clipboard is the model for a side effect the TUI triggers:** launch
  passes `tui.WithClipboard` an implementation from `internal/app/clipboard`
  (`internal/launch/daemon.go:271`, `internal/app/tui/model.go:632`).
- **A bell** can come from the TUI itself: Bubble Tea's `Raw` writes a
  sequence to the terminal without the renderer
  (`charm.land/bubbletea/v2@v2.0.8/raw.go:33`).

## Decisions this plan makes beyond the spec

1. **An own child's socket** is `<state>/v/a-<16 hex of sha256(agent ID)>.sock`,
   as F6 says. The `a-` keeps it apart from the project children's names.
   - The bridge removes a stale socket before the child starts, and removes
     the socket after the child stops.
   - Only a host process gets one. A container's child is unchanged.
   - The arguments are added where `newRuntime`'s fallback builds the host
     process, so a test that replaces `newRuntime` keeps its own child as it
     was. Task 1's tests drive the fallback itself.
2. **The agent attach reaches an own child** through that socket, counting its
   viewer on the agent's own registry. A container's agent is refused with
   409, "agent <name> runs in a container, which has no viewer socket yet".
   The relay takes what it used the project child for: the registry that
   counts the viewer, and a hook that rearms a project child's linger, which
   is nil for an own child.
3. **One watch outlives both programs.** The notifications' state (the last
   state seen and each key's last notification time) lives in a
   `fleet.Watch` that launch creates once per daemon loop. The fleet program
   and every session screen after it observe the client through it.
   - So a change that lands while the loop switches programs is still
     compared against the state before it, rather than becoming a new
     baseline.
   - The watch's first observed state, and the first of each epoch, notifies
     nothing (F8).
4. **What notifies,** from plan 3's transitions:
   - an agent's needs-you: "<agent> needs you: approval", or "…: question";
   - a submission: "<title> needs you: intake";
   - ready: "<agent> is ready".

   A failed agent is an Events line, not a notification.
5. **The burst** is per key: an agent's ID, or a submission's. A notification
   within 2s of the last one sent for the same key is dropped.
6. **The bell is the TUI's, the desktop notification launch's.** For each
   notification the watch passes, the program returns `tea.Raw` with the bell
   character, and the watch calls the notifier launch gave it. The notifier
   must not block: the desktop one runs its command on its own goroutine,
   bounded by 5s, and logs a failure once.
7. **The desktop notifier** is a new package, `internal/app/notify`, which the
   TUI never imports. On macOS it runs `osascript -e` with `display
   notification`, its text quoted for AppleScript. On Linux it runs
   `notify-send`. Elsewhere it does nothing. The title is "Marshal".
8. **Where the count shows,** in the loop of plan 4b:
   - the loop creates its fleet client when it starts, not when the fleet
     program first runs. Every daemon session screen gets it, so a plain
     `marshal` shows the count from the start;
   - the watch, and so notifications, starts with the first fleet program and
     is passed to every program after it. A plain `marshal` that never opens
     the fleet does not notify, as decision 8's title says: notifications run
     wherever the fleet is open.
9. **The count's segment** is on the status line's right, before the
   session's own right-hand segment, and reads `needs you N` in the warning
   colour. Its number is the queue's items minus the shown agent's own.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 4c, 5a, 5b and 6`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-15-p5-4c-viewer-turns-plan.md`,
    `2026-11-15-p5-5a-actions-plan.md` and `2026-11-15-p5-5b-finish-plan.md`.
- Then branch: `git switch -c p5-6-needs-you` from `main`, or create a
  worktree on that branch.
- **Run Tasks 1, 4 and 5 first, beside plan 4b.** They touch `web/bridge`'s
  `fleet.go` (the runtime's build, start and release) and `projectchild.go`
  (one helper); `internal/app/tui/fleet`'s new `watch.go`; and the new
  `internal/app/notify`.
- **Before Task 2, wait for plan 4b on `main`.** Merge `main` into this branch,
  rerun the Verify of Tasks 1, 4 and 5, and check this plan's anchors in plan
  4b's `attach.go`, `relay.go`, `internal/app/tui` status segment and
  `internal/launch` loop against its code.
- Plan 4c also edits `web/bridge/fleet.go`, at the spawn and restore call
  sites. This plan's edits are in the runtime's build, start and release, so a
  merge of the two touches different lines.
- Run each task's Verify and commit it as `p5-6-needs-you: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p5-6-suite`.

## Contracts

### The bridge (`web/bridge`, Tasks 1 and 2)

```go
// projectchild.go

// agentViewerSocketPath is <stateDir>/v/a-<16 hex of sha256(agentID)>.sock
// (decision 1).
func agentViewerSocketPath(stateDir, agentID string) string

// fleet.go: agentRuntime gains:
	viewerSocket string // the own child's viewer socket; "" for a container or a project child

// relay.go (after plan 4b): relay's project child becomes what it used it
// for (decision 2).
type relayEnd struct {
	reg    *Registry // counts the viewer
	linger func()    // rearms a project child's linger; nil for an own child
}
```

### The watch (`internal/app/tui/fleet/watch.go`, Task 4)

```go
// Notifier shows a desktop notification. Implementations must not block.
type Notifier interface {
	Notify(n Notification)
}

// Notification is one thing the user should look at (decision 4).
type Notification struct {
	Key  string // the agent's or the submission's ID: the burst's key
	Text string
}

// Watch turns the fleet's states into notifications across programs
// (decision 3). It is safe for one program at a time.
type Watch struct{ /* the executor's */ }

// NewWatch watches with n, which may be nil for the bell alone. now is the
// burst's clock.
func NewWatch(n Notifier, now func() time.Time) *Watch

// Observe compares state with the last one observed, sends what survives
// the burst to the notifier, and returns it, so the program can ring the
// bell.
func (w *Watch) Observe(state acpclient.FleetState) []Notification

// WithWatch gives the fleet model the loop's watch.
func WithWatch(w *Watch) Option
```

### The session screen (`internal/app/tui`, Task 6)

```go
// WithFleet gives a daemon session screen the fleet, for its needs-you
// count, and the loop's watch, which may be nil (decision 8).
func WithFleet(f acpclient.Fleet, w *fleet.Watch) Option

// fleetChangedMsg is the fleet's change signal, apart from the session's.
type fleetChangedMsg struct{ ch <-chan struct{} }
```

### The notifier (`internal/app/notify`, Task 5)

```go
// Desktop is the system's desktop notifier (decision 7).
type Desktop struct{ /* the executor's */ }

// New answers the notifier for goos, running commands with run.
func New(goos string, run func(ctx context.Context, name string, args ...string) error, log *slog.Logger) *Desktop

// Notify satisfies fleet.Notifier.
func (d *Desktop) Notify(n fleet.Notification)

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string
```

`internal/app/notify` may import `internal/app/tui/fleet` for the
`Notification` type. The TUI never imports `internal/app/notify`.

### Launch (`internal/launch`, Task 7)

```go
// WithNotify turns the fleet's notifications on or off. On by default;
// plan 7 sets it from fleet.toml.
func WithNotify(on bool) Option
```

## Task 1 — an agent's own child serves viewers

**Files:** `web/bridge/projectchild.go`, `web/bridge/fleet.go`,
`web/bridge/fleet_test.go` or a new `web/bridge/ownviewers_test.go`.

1. `agentViewerSocketPath` beside `viewerSocketPath` (`projectchild.go:63`).
2. In `newRuntime`'s host-process fallback (`fleet.go:462`), start the child
   with `--viewers unix://<socket>`, and make the socket's directory `0700`
   first, as `buildProjectChild` does (`projectchild.go:77`).
3. `startRuntime` (`fleet.go:898`) records the socket on the runtime for a
   host-process child only, and removes a stale file at that path before the
   child starts.
4. `releaseAgent` removes the socket after the child's `Stop`
   (`fleet.go:2023`). A child that is only detached keeps its socket.
5. Tests:
   - `TestAnOwnChildStartsWithAViewerSocket`: a host-process agent's child
     carries `--viewers` with decision 1's path, and the runtime records it.
   - `TestAContainerChildHasNoViewerSocket`: a containerized child's arguments
     are unchanged, and the runtime records none.
   - `TestAStaleViewerSocketIsRemoved`: a file left at the path is gone before
     the child starts.
   - `TestTheViewerSocketGoesWithTheChild`: after the agent is stopped, the
     path is gone.
   - `TestAgentViewerSocketPathFitsTheLimit`: a long agent ID under a long
     state directory stays within the 104 bytes macOS allows, as the project
     children's test checks theirs.

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./web/bridge/ -count=1 -timeout 20m`,
then `GOOS=linux go vet ./web/...` and `GOOS=windows go vet ./web/bridge/...`.

## Task 2 — the agent attach reaches an own child (after plan 4b)

**Files:** `web/bridge/attach.go`, `web/bridge/relay.go`,
`web/bridge/attach_test.go`.

1. In plan 4b's `attachAgent`, replace the own-child refusal: an agent whose
   runtime records a viewer socket is dialled there. A containerized agent
   gets decision 2's refusal instead. Every other refusal keeps its order.
2. The relay takes `relayEnd` in place of the project child (decision 2). The
   project attach passes its child's registry and its linger rearm; the agent
   attach passes the project child's when the agent is on one, and the
   agent's own registry with no linger otherwise.
3. Tests, beside plan 4b's:
   - `TestTheAgentAttachReachesAnOwnChild`: a 101, both headers, and the
     viewer counted on the agent's own registry.
   - In plan 4b's refusal table, the own-child row becomes the container row,
     with decision 2's sentence.
   - `TestDetachingFromAnOwnChildLeavesItRunning`: closing the viewer leaves
     the child and its session, and arms no linger.

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./web/bridge/ -run 'Attach|Relay' -count=1`.

**Mutation check:** pass the project child's registry for an own child; the
viewer count assertion in `TestTheAgentAttachReachesAnOwnChild` must fail.
Restore.

## Task 3 — an own child's agent, opened against a real bridge (after plan 4b)

**Files:** `test/daemon/own_child_test.go` (new), `test/daemon/main_test.go`
if plan 5b has not merged.

1. The child entry must accept `acp --viewers <url>`, which it does today. If
   plan 5b has not merged, nothing else changes there.
2. `TestAnOwnChildsAgentOpensAsAViewer`: a git agent from a local bare
   repository, spawned in process with `h.fleet.Spawn` and
   `SpawnOptions.RepoID` (reuse plan 5b's helper for the repository if it has
   merged). Open it with `daemon.Client.AttachAgent`; read its transcript
   through the attachment; close it, and see the agent still listed and its
   session still open.
3. **If the harness cannot spawn a git agent from a local bare repository,**
   record why, and drive the same test with an agent behind a stub egress
   proxy instead, which also runs on its own child (`fleet.go:835`).

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./test/daemon/ -count=1 -timeout 15m`.

## Task 4 — the watch (W11)

**Files:** `internal/app/tui/fleet/watch.go` (new),
`internal/app/tui/fleet/watch_test.go` (new), `internal/app/tui/fleet/model.go`.

1. `Watch` keeps the last state it observed and each key's last notification
   time. `Observe` runs plan 3's `transitions` against the last state,
   keeps needs-you and ready, words them as decision 4 says, applies
   decision 5's burst, calls the notifier for each survivor, and stores the
   new state.
2. The fleet model, given a watch, observes every state it reads and returns
   one `tea.Raw` bell for a non-empty result, batched with its other commands.
   Its Events lines are unchanged: they come from its own transitions, as
   today.
3. Tests in `watch_test.go` (W11):
   - `TestTheFirstStateNotifiesNothing`, and
     `TestANewEpochNotifiesNothing`.
   - `TestAnApprovalNotifies` and `TestAQuestionNotifies`, each with
     decision 4's text.
   - `TestASubmissionNotifies`.
   - `TestReadyNotifies`.
   - `TestAFailureDoesNotNotify`.
   - `TestABurstCollapses`: two needs-you for one agent 1s apart send one;
     a third 3s after the first sends again.
   - `TestTheBurstIsPerAgent`: two agents within 2s send two.
   - `TestTheWatchSpansPrograms`: a state observed by one model and the next
     by a second model on the same watch notifies the change between them.
   - `TestTheFleetModelRingsTheBell`: a notifying state's update returns a
     command whose message is the bell.

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./internal/app/tui/fleet/ -count=1`.

**Mutation check:** drop the epoch check from `Observe`'s baseline;
`TestANewEpochNotifiesNothing` must fail. Restore.

## Task 5 — the desktop notifier

**Files:** `internal/app/notify/notify.go` (new),
`internal/app/notify/notify_test.go` (new), with `testenv.Run` in its
`TestMain`.

1. Decision 7's notifier, over an injected `run`, so no test runs a real
   command.
2. Tests:
   - `TestMacOSRunsOsascript`: the command and its arguments, with a body
     holding a quote and a backslash quoted correctly.
   - `TestLinuxRunsNotifySend`.
   - `TestOtherSystemsDoNothing`.
   - `TestNotifyDoesNotBlock`: a `run` that never returns leaves `Notify`
     returning at once, and the 5s bound ends the command's context.
   - `TestAFailureIsLoggedOnce`.

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./internal/app/notify/ -count=1`.

## Task 6 — the session screen's count (W10, after plan 4b)

**Files:** `internal/app/tui/model.go`, `internal/app/tui/status.go`,
`internal/app/tui/pump.go`, a new `internal/app/tui/needsyou_test.go`.

1. `WithFleet` stores the fleet and the watch. The model waits on the fleet's
   change signal with its own message, `fleetChangedMsg`, released by a quit
   gate as the fleet model's wait is (`internal/app/tui/fleet/model.go:78`).
2. On each fleet change it repaints, and with a watch it observes and rings
   the bell as the fleet model does.
3. Decision 9's segment. The shown agent is plan 4b's `AgentSession()`; on a
   session that is not an agent's, every item counts.
4. Tests in `needsyou_test.go`, over a fake fleet (W10):
   - `TestTheCountChangesWhenAnotherAgentParks`: a state with another agent
     in the queue shows `needs you 1`, and a second shows `needs you 2`.
   - `TestTheCountLeavesOutTheShownAgent`.
   - `TestNoCountAtZero`.
   - `TestNoCountWithoutAFleet`: a model built without `WithFleet`, as
     embedded mode builds it, shows no segment.
   - `TestTheSessionScreenNotifiesWithAWatch` and
     `TestNoBellWithoutAWatch`.
   - `TestTheFleetWaitEndsOnQuit`: quitting releases the wait, as
     `TestQuitReleasesTheWait` checks the fleet model's.
   - A status-line golden at 100 columns with the segment, beside the status
     line's existing goldens.

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./internal/app/tui/ -count=1 -timeout 15m`.

**Mutation check:** count the shown agent's own items;
`TestTheCountLeavesOutTheShownAgent` must fail. Restore.

## Task 7 — launch wires the count and the notifier (after plan 4b)

**Files:** `internal/launch/daemon.go`, `internal/launch/fleet.go`,
`internal/launch/launch.go`, their tests.

1. Decision 8's lifetimes in plan 4b's daemon loop: the fleet client starts
   with the loop and closes with it; the watch is created when the first fleet
   program runs, with `notify.New` for this system when notifications are on,
   and passed to every program after it.
2. Every daemon session screen gets `tui.WithFleet` with the client and the
   watch, nil until the fleet has run. Embedded mode gets neither.
3. `WithNotify(false)` creates the watch with a nil notifier, so the bell
   still rings and nothing else happens.
4. Tests, with the program runner and the fakes `launch_test.go` uses:
   - `TestADaemonSessionGetsTheFleet`: a plain `marshal`'s session screen is
     given the client and no watch.
   - `TestTheWatchStartsWithTheFleet`: after a fleet program, the next session
     screen is given the same watch the fleet program had.
   - `TestEmbeddedModeHasNoFleet`.
   - `TestTheLoopsFleetClientClosesWithIt`.
   - `TestNotifyOffKeepsTheBell`.

**Verify:** `TMPDIR=/private/tmp/p5-6-suite CGO_ENABLED=1 go test ./internal/launch/ -count=1 -timeout 15m`.

## Task 8 — W10 against a real bridge (after plan 4b)

**Files:** `test/daemon/needs_you_test.go` (new).

- `TestTheCountFollowsARealFleet`: two fleet agents on the project child; a
  session screen model built with `tui.WithFleet` over a real `fleetclient`
  on the harness's socket, showing the first agent's session. When the second
  agent parks on an approval (`writeApprovalPolicy`), the model's status line
  reads `needs you 1`; when it is answered through the bridge, the segment
  goes.

**Verify:** Task 3's command.

## Task 9 — the docs, the record and full verification

**Files:** `docs/daemon/p5-fleet-screen.md`, `AGENTS.md`.

1. Fill the "Plan 6" record, as plan 3's is written.
2. `AGENTS.md`'s local daemon paragraph: an agent on its own host-process
   child can be opened too, and the session screen shows the needs-you count
   in daemon mode. Add `internal/app/notify/` to the map, under "App shell &
   config".
3. **The manual run.** Against a real local daemon with a real model: open a
   git agent's session from the fleet if one can be spawned locally; with two
   agents, prompt one so it parks on an approval while the other's session is
   on screen, and record the count, the bell and the desktop notification;
   let an agent finish with changed files and record the ready notification.
4. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p5-6-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

## Done when

- A host-process agent with its own child opens as a viewer, and its socket
  comes and goes with the child.
- A daemon session screen shows `needs you N` for every other agent's items.
- The fleet rings and notifies on needs you, ready and a submission, with the
  burst collapsed, and keeps notifying on a session screen opened from it.
- W10 and W11 pass, every other test named above passes, each mutation check
  fails as described, and CI's suite command passes.
