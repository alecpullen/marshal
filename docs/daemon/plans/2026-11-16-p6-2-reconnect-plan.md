# P6 plan 2: the session screen reconnects, and keeps its draft — implementation plan

Date: 2026-11-16. Status: ready for review.

Base: `origin/main` at `87c41ad9` with P5 plan 8's branch (`d57f178f`, PR
#100) merged in. Every anchor below was checked there. Plan 8 touches none of
this plan's files.

- **The baseline** is recorded in the spec's "Implementation record".
- **The spec is already written,** uncommitted in the main checkout (see
  Execution).
- **Plans 1, 3 and 6 run beside this one.** Plan 6's last task adds a note
  through this plan's note-once helper, so it waits for this plan on `main`.

Approved design: [p6-daemon-lifecycle.md](../p6-daemon-lifecycle.md):
decision 5; B2; Migration order item 2; Tests L1's screen half.
Previous plans: [P5 plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md),
whose daemon loop this plan extends.
Sibling plans: [plan 1](2026-11-16-p6-1-versions-plan.md),
[plan 3](2026-11-16-p6-3-staleness-plan.md),
[plan 6](2026-11-16-p6-6-wake-warnings-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A session screen whose connection closes comes back on its session.** The
  loop dials the same session again for up to 60s, showing a small waiting
  screen meanwhile, and gives up as today only after that.
- **The prompt being typed survives,** a reconnect and a return from the fleet
  alike.
- **A restart can say why.** The `marshal/restarting` notification is defined
  and read here, and the waiting screen shows its reason. Plan 4 sends it.
- **A note the loop adds is added once,** so reconnects do not stack version
  notes.

## Non-goals

- **Sending `marshal/restarting`,** and restarting anything. Plans 4 and 5.
- **Keeping the screen's scroll position or open panels** across a reconnect.
  The new screen opens at the transcript's end, as any attach does.
- **Drafts across program runs.** They live as long as the loop.

## Assumptions

Each was checked at the base.

- **The loop's session step** dials (`dialDaemon`, `internal/launch/daemon.go:588`),
  runs the session screen, and, when the connection was lost, ends the loop
  or returns to the fleet with a line in Events (`connectionLost` at
  `daemon.go:385`, the message at `:391`). It is reached through
  `sessionStep` (`daemon.go:331`).
- **A lost connection cancels the program's context** (`daemon.go`'s
  `conn.lost()` watcher), and Bubble Tea's `Run` still returns the last model
  when its context ends (`charm.land/bubbletea/v2@v2.0.8/tea.go:1150`). So the
  screen's state, its draft included, is readable after a loss.
- **`runProgram` reads the program's result** from the final model by
  interface assertions, one per field (`internal/launch/launch.go:244`), into
  `ProgramResult` (`launch.go:26`).
- **The version note** is added on every attach whose agent's version differs
  (`daemon.go:352`, its text from `versionNote` at `:547`).
- **The draft** is the composer's text (`internal/app/tui/model.go:173`) and
  its paste attachments (`model.go:582`). Nothing carries it between screens.
- **A refused attach** is a `*daemon.AttachError` carrying the status
  (`internal/daemon/client.go:255`).
- **Every notification on a dialled connection** reaches the caller's
  `DialOptions.Handlers.Notify` after the host's own routing
  (`internal/acpclient/rpc/dial.go:65`).
- **A session's newest message** is `Session.Messages` with `Last: 1`
  (`internal/acpclient/acpclient.go:829`, `internal/acpwire/history.go:5`).
- **Launch's tests** use a fake daemon that serves each attach from a runtime
  of its own over a pipe and can drop it (`internal/launch/daemon_test.go:93`,
  `drop` at `:203`). `TestALostConnectionNamesTheReattach` (`:631`) and
  `TestALostConnectionReturnsToTheFleetWithANotice` (`:1081`) expect the loss
  to end things at once.

## Decisions this plan makes beyond the spec

1. **The waiting screen is its own package,** `internal/app/tui/reconnect`.
   It renders from what launch gives it, under the TUI's boundary rule: the
   label, the reason when there is one, the deadline, and a channel of
   attempts that launch closes when it is done.
2. **Launch dials while the waiting screen runs.** The attempts run on their
   own goroutine. When the screen ends because the user gave up, launch
   cancels them and waits for them to stop. When it ends otherwise, launch
   waits for their result.
3. **What is retried.** A dial failure, an attach refused with a 5xx status,
   and a session the new connection cannot attach yet. A refusal with a 4xx
   status ends the attempts at once.
4. **The reconnected step reuses the session's ID.** A first attach that
   opened a new session reconnects to the session it opened, not to another
   new one.
5. **`daemonStep` carries a connection already dialled,** so the session step
   that follows a successful reconnect runs on it instead of dialling again.
6. **The note-once check** compares the newest message's content with the
   note's text. A read that fails adds the note, as today.
7. **The window and the waits are a test seam** (`withReconnect`), so no test
   waits 60s. Production uses 60s, 1s and 5s.

## Execution

- In the main checkout, if `git status` shows the spec or this batch's plans
  uncommitted, commit them to `main` first, as explicit paths, with the message
  `docs/daemon: record P6 and its plans 1, 2, 3 and 6`. The paths are listed
  in [plan 1](2026-11-16-p6-1-versions-plan.md)'s Execution.
- Then branch: `git switch -c p6-2-reconnect` from `main`, or create a
  worktree on that branch.
- Run each task's Verify and commit it as `p6-2-reconnect: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p6-2-suite`.

## Contracts

### The notification (`internal/acpwire/restart.go`, Task 1)

```go
// MethodRestarting is what the bridge tells a viewer before it closes the
// viewer's connection for a restart (B2, B4).
const MethodRestarting = "marshal/restarting"

// RestartingParams says why, and what comes next.
type RestartingParams struct {
	Reason  string `json:"reason"`
	Version string `json:"version,omitempty"` // the build the restart brings
}
```

### The draft (`internal/app/tui`, Task 2)

```go
// Draft is the composer's text and its paste attachments.
type Draft struct{ /* the executor's */ }

// Empty reports whether the draft holds nothing.
func (d Draft) Empty() bool

// Draft is the screen's draft as it stands.
func (m Model) Draft() Draft

// WithDraft opens the screen with d in its composer.
func WithDraft(d Draft) Option
```

### The waiting screen (`internal/app/tui/reconnect`, Task 3)

```go
// Attempt is one dial that failed.
type Attempt struct {
	N   int
	Err string
}

// Model is the waiting screen (decision 1).
type Model struct{ /* the executor's */ }

// New waits for attempts until the channel closes or the user gives up.
// reason is the restart's, "" when none arrived.
func New(label, reason string, deadline time.Time, attempts <-chan Attempt, opts ...Option) Model

// GaveUp reports whether the user ended the wait.
func (m Model) GaveUp() bool

// Option configures the screen; WithTheme and WithNow are its two.
type Option func(*Model)
```

### Launch (`internal/launch`, Task 4)

```go
// ProgramResult gains:
	Draft  tui.Draft // the session screen's draft when it ended
	GaveUp bool      // the waiting screen ended because the user gave up

// daemonLoop gains:
	drafts map[string]tui.Draft // by session ID, for the loop's life

// daemonStep gains:
	dialled *daemonConn // a connection the reconnect already dialled

// daemonConn gains:
	restarting *acpwire.RestartingParams // the last marshal/restarting, nil when none

// withReconnect sets the window and the waits (decision 7).
func withReconnect(window, first, max time.Duration) Option

// reconnect dials step again until it attaches, a refusal ends it, the
// window ends or the user gives up.
func (l *daemonLoop) reconnect(ctx context.Context, step daemonStep, reason string) (*daemonConn, error)

// addNoteOnce adds text as a note unless it is already the session's newest
// message (decision 6).
func addNoteOnce(ctx context.Context, sess acpclient.Session, text string) error
```

## Task 1 — `marshal/restarting`

**Files:** `internal/acpwire/restart.go` (new), `internal/acpwire/restart_test.go`.

1. The constant and the params type, as Contracts declares them.
2. `TestRestartingParamsRoundTrip`: the JSON field names are `reason` and
   `version`, and an empty version is left out.

**Verify:** `TMPDIR=/private/tmp/p6-2-suite go test ./internal/acpwire/ -count=1`.

## Task 2 — the draft in and out of the session screen

**Files:** `internal/app/tui/model.go`, `internal/app/tui/draft.go` (new),
`internal/app/tui/draft_test.go` (new).

1. `Draft` holds the composer's text and its paste attachments, copied so a
   later edit of either model never reaches the other.
2. `WithDraft` sets both when the model is built, with the cursor at the end of
   the text. A draft given to a screen that also opens a panel on start (the
   connect panel) is still in the composer when the panel closes.
3. Tests:
   - `TestADraftRoundTrips`: text with a condensed paste in it, read with
     `Draft` and given to a new model with `WithDraft`, renders the same
     composer and expands to the same prompt when sent.
   - `TestAnEmptyComposerIsAnEmptyDraft`.
   - `TestADraftIsACopy`: editing the first model after `Draft` leaves the
     draft unchanged.

**Verify:** `TMPDIR=/private/tmp/p6-2-suite CGO_ENABLED=1 go test ./internal/app/tui/ -run Draft -count=1 -timeout 10m`.

## Task 3 — the waiting screen

**Files:** `internal/app/tui/reconnect/reconnect.go`, `reconnect_test.go`,
`testdata/` (all new).

1. The screen centres two lines: "Reconnecting to <label>…" (or "<label> is
   restarting: <reason>" when there is a reason), and below it the attempt
   count, the last error's first line, and the seconds left. It uses the TUI's
   theme for colour, and renders without colour under `NO_COLOR` as the other
   screens do.
2. It waits on the attempts channel with a command of its own, repainting on
   each attempt and once a second, and quits when the channel closes.
3. `esc` and `q` quit with `GaveUp` true. ctrl+c quits the same way.
4. Tests:
   - `TestTheWaitingScreenNamesTheSession` and
     `TestTheWaitingScreenSaysWhyARestartHappens`.
   - `TestEachAttemptRepaints`: an attempt's number and error show.
   - `TestTheScreenEndsWhenTheAttemptsDo`: closing the channel quits, with
     `GaveUp` false.
   - `TestEscGivesUp`.
   - Goldens at 80×24, with and without `NO_COLOR`, as the fleet screen's
     goldens are written.
   - The TUI's boundary test passes with the new package.

**Verify:** `TMPDIR=/private/tmp/p6-2-suite CGO_ENABLED=1 go test ./internal/app/tui/... -count=1 -timeout 15m`.

## Task 4 — the loop reconnects, and keeps the drafts

**Files:** `internal/launch/daemon.go`, `internal/launch/launch.go`,
`internal/launch/daemon_test.go`, `internal/launch/fleet_test.go`.

1. `runProgram` reads `Draft` and `GaveUp` from the final model, as it reads
   the other fields.
2. `dialDaemon` passes a notification handler that records the last
   `marshal/restarting` on the connection.
3. `sessionStep`:
   - runs on `step.dialled` when it is set, instead of dialling;
   - gives the screen `tui.WithDraft` with the session's kept draft, if any;
   - after the screen ends, keeps its draft under the session's ID, or clears
     the entry when the draft is empty. This holds for every ending, so a
     return from the fleet finds it;
   - on a lost connection, calls `reconnect` with the step's session ID
     (decision 4) and the connection's restart reason. On success it returns
     the same step with `dialled` set; on failure it reports the loss exactly
     as today.
4. `reconnect` runs the attempts on their own goroutine (decisions 2 and 3),
   feeding the waiting screen's channel, and runs the waiting screen through
   the program runner.
5. The version note is added through `addNoteOnce`.
6. Tests, with the fake daemon, which gains a way to drop the pipe and then
   serve the next attach of the same session ID:
   - `TestALostConnectionReconnectsToTheSameSession`: two session screens on one
     session ID, with the waiting screen between them.
   - `TestAReconnectedScreenHasTheDraft`: the first screen's result carries a
     draft, and the second screen's model reports it before any update.
   - `TestAReturnFromTheFleetHasTheDraft`: a screen ends with `/fleet` and a
     draft; the fleet opens the same session; the new screen has the draft.
   - `TestAnEmptyDraftClearsTheEntry`.
   - `TestTheReconnectGivesUpAfterItsWindow`: with a short window and a fake
     that never serves again, the loop ends with today's message naming
     `marshal attach`.
   - `TestARefusedReconnectStopsAtOnce`: a fake answering 409 is asked once.
   - `TestGivingUpStopsTheAttempts`: the waiting screen ends with `GaveUp`, and
     no attempt is made after it.
   - `TestTheRestartReasonReachesTheWaitingScreen`: the fake sends
     `marshal/restarting` before it drops, and the waiting screen's model has
     the reason.
   - `TestTheVersionNoteIsAddedOnce`: two attaches with a differing version
     add one note.
   - `TestALostConnectionNamesTheReattach` and
     `TestALostConnectionReturnsToTheFleetWithANotice` use `withReconnect` with
     a short window, and otherwise keep what they check.

**Verify:** `TMPDIR=/private/tmp/p6-2-suite CGO_ENABLED=1 go test ./internal/launch/ -count=1 -timeout 15m`.

**Mutation check:** reconnect with an empty session ID, opening a new session
as a first attach does; `TestALostConnectionReconnectsToTheSameSession` must
fail. Restore.

## Task 5 — a real daemon, the docs, the record and full verification

**Files:** `test/daemon/reconnect_test.go` (new), `test/daemon/main_test.go`,
`docs/daemon/p6-daemon-lifecycle.md`, `AGENTS.md`.

1. The harness's child (`runChild`, `test/daemon/main_test.go:38`) writes its
   process ID to the file a new environment variable names, when it is set.
   `GET /api/daemon` reports no process IDs, and the test needs one.
2. `TestAScreenComesBackAfterTheChildRestarts` (L1's screen half): against
   the real bridge harness, run `launch.Attach` on a session with a program
   runner, as `TestTheFleetOpensAnAgentAndLeavesItRunning` drives the loop.
   - The first pass types a draft into the screen's model with key messages,
     then ends the child's process with SIGTERM and waits for its context to
     end, returning the model's draft.
   - The bridge restarts the child as it restarts a crashed one.
   - The second pass is a session screen on the same session ID, with that
     draft and the session's transcript.
3. Fill the "Plan 2" record.
4. `AGENTS.md`'s local daemon paragraph: a session screen whose connection
   closes reconnects to its session for up to a minute, keeping its draft.
   Add `internal/app/tui/reconnect` to the map's TUI line.
5. **The manual run.** With the installed daemon, type a draft in a session
   screen, run `marshal daemon stop` and `marshal daemon start` from another
   terminal, and record what the screen shows and whether it comes back with
   the draft.
6. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p6-2-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

**Verify:** `TMPDIR=/private/tmp/p6-2-suite CGO_ENABLED=1 go test ./test/daemon/ -run Comes -count=1 -timeout 15m`,
then the full verification above.

## Done when

- A session screen whose connection closes reconnects to the same session for
  up to 60s, with the waiting screen meanwhile, and gives up as today after.
- The draft comes back after a reconnect and after a return from the fleet.
- The waiting screen shows a restart's reason when one arrives.
- The version note is added once.
- Every test named above passes, the mutation check fails as described, and
  CI's suite command passes.
