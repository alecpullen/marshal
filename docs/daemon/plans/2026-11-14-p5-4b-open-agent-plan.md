# P5 plan 4b: opening an agent — implementation plan

Date: 2026-11-14. Status: ready for review.

Base: `origin/main` at `cef1e353` for Tasks 1 to 5. Every anchor below was
checked there. The main checkout's local `main` was behind it when this was
written: pull first.

- **Tasks 6 to 9 start from `main` once plan 3 is merged.** Their anchors in
  plan 3's files are its Contracts, re-checked then (see Execution).
- **The baseline.** The full suite at `cef1e353` ran 122 packages ok. Only
  `internal/acp` failed, with `TestStackPatchApplyBudget` and
  `TestStackRoundTripBudget`. Both are among the 12 wall-clock budget tests
  that CI skips (`docs/testing-ci-audit.md`). The suite below is CI's command,
  which skips them.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`. This plan's are F5's "Decided by plan 4b"
  bullets, the finding on a viewer's own turns, Migration order item 4b, and
  the "Plan 4b" record placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decisions 4, 5
and 9; F5; Migration order item 4b; Tests W4, W5 and W6.
Previous plans: [P5 plan 2, the inbox](2026-11-13-p5-2-inbox-plan.md).
Sibling plans: [plan 3, the fleet screen](2026-11-14-p5-3-fleet-screen-plan.md),
whose model and launch entry Tasks 6 to 8 extend, and
[plan 4a, requests](2026-11-14-p5-4a-requests-plan.md), which makes answers
settle on both paths.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

From the fleet screen, `⏎` opens the selected agent's session, or a daemon
session, in the session screen. Leaving that screen returns to the fleet.

- An agent's session opens through `POST /api/agents/{id}/attach`, as a viewer
  that cannot close or replace it.
- `/fleet` on any daemon session screen opens the fleet.
- The project attach refuses every session an agent record names.

## Non-goals

- **No viewer socket on an agent's own child.** An agent with no project child
  is refused with 409 until plan 6.
- **No agent attach over TCP or TLS.** That is plan 7.
- **No answering from the fleet screen.** That is plan 5. The session screen
  answers through its mirror, as it does today.
- **No needs-you count and no notifications.** That is plan 6.
- **No fix for a viewer's own turns being invisible to the fleet.** This
  plan's authoring found that gap, and the spec now records it (F5). It needs
  a decision before any plan takes it.

## Assumptions

- Every Go test command below runs with `TMPDIR=/private/tmp/p5-4b-suite`.
  Create the directory first: `test/daemon` binds unix sockets under it.
- **The project attach** (`web/bridge/attach.go`):
  - `attachProject` (`:33`) checks the upgrade headers. It resolves the child
    (`projectChildFor`), then the session: new, refused as a fleet session
    (`:88` to `:103`), tracked, or resumed.
  - It then dials `pc.socket`, hijacks, writes `101` with `Marshal-Session`,
    counts a viewer, and runs `Fleet.relay`.
  - Every failure after resolving the child rearms its linger.
  - The constants `attachPath`, `attachProtocol` and `sessionHeader` are at
    `:19`.
- **The socket's routes:** `SocketHandler` (`http.go:122`) sends
  `attachPath` and `pinPath` to their handlers, and everything else to the
  mux. The TCP handler has no attach: `TestTheTCPHandlerHasNoAttach`
  (`attach_test.go:619`).
- **The relay** (`web/bridge/relay.go`):
  - `relay` (`:65`) runs two pumps. `pumpClient` (`:146`) decodes each
    client frame with `watchedRequest` (`:190`), which already parses
    `method`, `params.replaces` and `params.sessionId`.
  - `pumpAgent` (`:166`) is the only writer to the client today.
  - `compactID` (`:306`) canonicalises a JSON-RPC ID.
- **The fleet's lookups** (`web/bridge/fleet.go`):
  - `runtimeForAgent` (`:795`): a paused agent has no runtime and gets
    `ErrUnknownAgent`.
  - `agentForSession` (`:1841`): the classifier's live map.
  - `ws.Agent(id)` and `ws.Agents()` are the records. A record's `SessionID`
    is its session, live or paused, and its `Name` its name.
  - `agentRuntime.project` (`:109`) is the project child an agent runs on,
    nil for an agent with its own child. `agentRuntime.sessionID` is its
    session.
  - `Pause` (`:2143`) releases the runtime and keeps the record.
- **The registry:** `IsFleet` (`registry.go:279`), and `attachViewer` and
  `detachViewer` (`:397`, `:409`) count viewers on any tracked session, a
  fleet one included.
- **The marshal client's errors:** a JSON-RPC error `-32000` whose `data` is
  an `acpwire.Error` with an empty code and a message reads as that message
  (`internal/acpclient/rpc/conn.go:410`).
- **The daemon client:** `Client.Attach` (`internal/daemon/client.go:173`)
  does the whole upgrade handshake for the project attach and returns an
  `Attachment` (`:151`).
- **The session screen** (`internal/app/tui`):
  - `WithDetachOnQuit` (`model.go:606`) marks a daemon session.
  - `beginEnd` (`model.go:4366`) is the one entry point for every ending:
    `/exit`, `/quit`, Ctrl+C, `/new`, `/clear` and a resume.
  - `detach` (`:4919`) leaves the session running and quits. `Detached`
    (`:6738`) and `ResumeSession` (`:6733`) are what launch reads from the
    final model.
  - `/detach`'s effect is in `tuiCommandEffects` (`commands_dispatch.go:258`).
    The command list is `internal/commands/commands.go`, where `detach` is at
    `:73`.
  - The status line's left segments are built by `statusLeftSegments`
    (`status.go:287`).
  - `detach_test.go` is the model for a daemon-session test.
- **Launch** (`internal/launch/daemon.go`):
  - `runDaemon` (`:176`) is a loop of one kind of pass: dial a session, run the
    session screen, then return, or loop for a resume.
  - `Attach` (`:125`) and `Run`'s daemon branch (`launch.go:93`) start it.
  - `daemonClient` (`:38`) is what tests fake (`daemon_test.go:62`).
  - `runProgram` (`launch.go:210`) builds `ProgramResult` from the final
    model's methods.
- **After plan 3:**
  - `internal/app/tui/fleet` exists, with `New`, `WithTUIConfig` and
    `WithNow`.
  - `launch.Fleet` runs the fleet program over a `fleetclient` on the socket,
    and `withFleetClient` injects a fake.
  - `acpclient.FleetState` lists `Agents` (each with its `Project`) and, for
    the local daemon, `Daemon.Children[].Sessions`.
- **The end-to-end harness** (`test/daemon`): `newDaemonHarness`,
  `daemonHarness.attach`, the provider fixture with a gated turn, and
  `waitFor`. `Fleet.Spawn` starts an agent on the project child.

## Decisions this plan makes beyond the spec

F5's "Decided by plan 4b" bullets record them.

1. **No viewer may close or replace any agent's session,** on either attach.
   - Today a project viewer can send `session/close` or a replacing
     `session/new` naming any session on its child, a fleet agent's on the
     same child included.
   - So the relay refuses both whenever the named session is an agent's: one
     rule, on every relay. The agent attach is the case F5 names.
2. **"An agent's session"** is any session an agent record names, live or
   paused, or one the classifier maps to a live agent. One helper,
   `agentOwning`, answers it for the relay and for both attaches.
3. **The refusal's sentence is one sentence,** shared by the relay and the
   session screen: "This is agent <name>'s session: finish it from the fleet
   screen." The name is the record's `Name`, else its ID.
4. **The session screen refuses every ending on an agent's session,** in
   `beginEnd`. F5 names `/exit` and `/new`, but `/quit`, `/clear`, Ctrl+C's
   quit and a resume all end the session the same way. In daemon mode
   Ctrl+C's quit is already a detach, which stays allowed.
5. **The agent attach refuses** as follows:
   - an unknown agent: 404;
   - a paused agent: 409;
   - an agent whose runtime failed to start: 502, with its error;
   - an agent with its own child: 409, saying it has no viewer socket yet;
   - an agent with no session yet: 409.

   It never resumes a paused agent: the fleet's resume is the way back.
6. **The loop goes back to the fleet once the fleet has run in it.** After
   that, every way the session screen ends returns to the fleet: quitting,
   `/detach`, `/fleet`, `/exit` on a daemon session, and a lost connection. A
   refused or failed open also returns to the fleet, with the reason as an
   Events line.
   - Before the fleet has run, as for a plain `marshal` or
     `marshal attach`, every ending behaves as today, except `/fleet`.
7. **The fleet client lives as long as the loop.** It is created the first
   time the loop runs the fleet program, and closed when the loop returns, so
   leaving a session screen does not reconnect it.
8. **A fleet target carries its project root,** for an agent too. It is the
   session screen's working directory when the session reports none, as
   `daemonTUIOptions` already falls back to the root.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 3, 4a and 4b`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-14-p5-3-fleet-screen-plan.md` and
    `2026-11-14-p5-4a-requests-plan.md`.

  Another plan's executor may have committed them already. Leave the main
  checkout's other untracked work alone.
- Then branch: `git switch -c p5-4b-open-agent` from `main`, or create a
  worktree on that branch.
- **Run Tasks 1 to 5 in order, beside plans 3 and 4a.** They touch:
  - `web/bridge`'s `attach.go`, `relay.go`, `http.go` (the socket handler
    only) and `fleet.go` (one helper);
  - `internal/daemon/client.go`;
  - the session screen: `internal/app/tui`'s `model.go`,
    `commands_dispatch.go` and `status.go`, and `internal/commands/commands.go`;
  - one new file in `test/daemon`.

  Plan 3 touches none of them. Plan 4a touches `web/bridge`'s `child.go` and
  `registry.go` only.
- **Before Task 6, wait for plan 3 to be merged to `main`.**
  - Merge `main` into this branch, and rerun the Verify of Tasks 1 to 5.
  - Check this plan's anchors in `internal/app/tui/fleet` and
    `internal/launch/fleet.go` against plan 3's code.
  - Plan 5 also changes the fleet model's keys. If it has merged first, add
    `⏎` beside its cases.
- Run each task's Verify and commit it as `p5-4b-open-agent: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The bridge (`web/bridge`, Tasks 1 and 2)

```go
// attach.go
const agentHeader = "Marshal-Agent"

// agentAttachID returns the agent ID a /api/agents/{id}/attach path names.
func agentAttachID(path string) (string, bool)

// attachAgent serves POST /api/agents/{id}/attach on the socket (P5 F5).
func (s *Server) attachAgent(w http.ResponseWriter, r *http.Request, id string)

// fleet.go: agentOwning is the agent whose session sessionID is: a record
// naming it, live or paused, or the classifier's live map (decision 2).
func (f *Fleet) agentOwning(sessionID string) (Agent, bool)

// finishFromFleet is the refusal's sentence (decision 3).
func finishFromFleet(a Agent) string

// relay.go: relay gains the agent whose session the attach opened, "" for a
// project attach.
func (f *Fleet) relay(pc *projectChild, sessionID, agentID string, client net.Conn, buffered *bufio.Reader, agent net.Conn)

// On relay:
	agentID  string
	clientMu sync.Mutex // serialises writes to the client: the agent's frames and the relay's refusals

// refusal answers a client frame the relay does not forward: a session/close
// or a replacing session/new that names an agent's session. ok is false for
// every other frame.
func (r *relay) refusal(frame []byte) (answer []byte, ok bool)
```

### The daemon client (`internal/daemon/client.go`, Task 3)

```go
type Attachment struct {
	SessionID string
	AgentID   string // the agent whose session this is; "" for a project attach
	Conn      io.ReadWriteCloser
}

// AttachAgent opens agentID's session as a viewer (P5 F5). ctx bounds the
// handshake only. A refusal is an *AttachError.
func (c *Client) AttachAgent(ctx context.Context, agentID string) (*Attachment, error)

// upgrade is the handshake both attaches share: path, and body when it is
// not nil.
func (c *Client) upgrade(ctx context.Context, path string, body []byte) (*Attachment, error)
```

### The session screen (`internal/app/tui`, Task 4)

```go
// WithAgentSession marks the session as fleet agent id's, called name
// (P5 F5): every ending is refused, and the status line names the agent.
func WithAgentSession(id, name string) Option

// AgentSession is the agent whose session this is, "" for any other.
func (m Model) AgentSession() string

// ToFleet reports whether the program ended with /fleet.
func (m Model) ToFleet() bool
```

### The fleet model (`internal/app/tui/fleet`, Task 6)

```go
// Target is what the fleet program ended to open (P5 F5). The zero value
// means quit.
type Target struct {
	AgentID   string // an agent's session, through the agent attach
	AgentName string
	Root      string // the project: an agent's, or a daemon session's child
	SessionID string // a daemon session, through the project attach; "" for an agent
}

func (m Model) Open() Target
func (m Model) Selected() string // the selected agent's or session's ID

func WithSelected(id string) Option // the row to start on
func WithNotice(line string) Option // an Events line from the loop
```

### Launch (`internal/launch`, Task 7)

```go
// ProgramResult gains:
	ToFleet bool         // the session screen ended with /fleet
	Open    fleet.Target // the fleet program's target; zero means quit

// daemonClient gains:
	AttachAgent(ctx context.Context, agentID string) (*daemon.Attachment, error)

// daemonStep is what the daemon loop runs next.
type daemonStep struct {
	fleet     bool
	selected  string // the fleet's selection to restore
	notice    string // an Events line for the fleet program
	agentID   string
	agentName string
	root      string // the project
	sessionID string // a daemon session; "" opens a new one on root
}
```

## Task 1 — the agent attach

**Files:** `web/bridge/attach.go`, `web/bridge/http.go` (`SocketHandler`
only), `web/bridge/fleet.go` (`agentOwning` only), `web/bridge/relay.go` (the
signature only), and `web/bridge/attach_test.go`.

1. **`agentOwning`** (decision 2) checks the classifier's map, then every
   record whose `SessionID` matches. An empty ID owns nothing.
2. **`finishFromFleet`** is decision 3's sentence.
3. **`SocketHandler`** sends a path for which `agentAttachID` answers true to
   `attachAgent`, beside the project attach.
   - `agentAttachID` accepts exactly `/api/agents/<id>/attach`, with an `id`
     that is not empty and holds no `/`.
   - The TCP handler is unchanged, so it answers 404 there.
4. **`attachAgent`:**
   - A method other than POST, or a missing upgrade, is refused as
     `attachProject` refuses it. The body is ignored.
   - A bridge with no fleet answers 501, as `attachProject` does.
   - Refusals, in this order (decision 5):
     - no record: 404 "unknown agent <id>";
     - no runtime: 409 "agent <name> is paused: resume it first";
     - a runtime that failed to start: 502 with its error;
     - a nil `project`: 409 "agent <name> runs on a child of its own, which
       has no viewer socket yet";
     - an empty `sessionID`: 409 "agent <name> has no session yet".
   - Otherwise it dials the project child's socket, hijacks, and writes `101`
     with `Marshal-Session` set to the agent's session and `Marshal-Agent` to
     its ID.
   - It counts a viewer, and runs `relay` with the agent's ID.
   - Every failure after the dial closes what it opened, as `attachProject`
     does. It never closes the agent's session. It rearms the linger, as
     every attach failure does.
5. **`relay`** takes the agent ID (Contracts). `attachProject` passes "". This
   task only threads it through. Task 2 uses it.
6. Tests in `attach_test.go`, on `newAttachHarness`. Track a fleet agent's
   session as `TestAnAttachToAFleetSessionIsRefused` does, and put its record
   in the harness's workspace.
   - `TestTheAgentAttachUpgrades`: `101`, both headers, and a viewer counted.
   - `TestTheAgentAttachRefusals`: a table over every refusal of item 4, each
     with its status and sentence, and no viewer connection opened.
   - `TestTheTCPHandlerHasNoAgentAttach`, beside
     `TestTheTCPHandlerHasNoAttach`.
   - `TestAgentAttachIDParsesOnlyItsPath`: a table of paths.

**Verify:**
- `go test ./web/bridge/ -run 'Attach|Relay' -count=1`
- Mutation check: drop the paused-agent refusal. Its case must fail. Restore
  it.

## Task 2 — the relay's refusals, and the widened 409

**Files:** `web/bridge/relay.go`, `web/bridge/attach.go`, and their tests.

1. **`relay.refusal`** (decision 1):
   - It decodes the client frame as `watchedRequest` does: one decode, shared
     between the two.
   - A `session/close` whose `sessionId` is an agent's is refused, and so is a
     `session/new` whose `replaces` is an agent's. Ownership is answered by
     `agentOwning`.
   - The answer echoes the request's ID. Its error is `-32000`, with
     `finishFromFleet`'s sentence as the message, and as the message of an
     `acpwire.Error`-shaped `data` with an empty code, so the marshal client
     reads it as that sentence.
2. **`pumpClient`** checks `refusal` before it registers a watch or forwards a
   frame.
   - A refused frame is answered to the client, never forwarded, and never
     watched.
   - Every write to the client, from either pump, goes through `clientMu`.
3. **The project attach's 409 widens** (closing P4 H15). Where it checks
   `IsFleet` and the classifier today (`attach.go:98`), it also refuses any
   session `agentOwning` answers for, with today's sentence.
4. Tests:
   - `relay_test.go`, on `newRelayPair`, with the pair's session owned by an
     agent:
     - `TestAViewerCannotCloseAnAgentsSession`: the client reads the error
       answer with the sentence, and the agent side reads nothing.
     - `TestAViewerCannotReplaceAnAgentsSession`: the same for a replacing
       `session/new`.
     - `TestAProjectViewerCannotCloseAnotherAgentsSession`: on a project
       relay, a close naming an agent's session is refused too.
     - `TestOtherFramesStillPass`: a prompt, and a close of a session no agent
       owns, pass unchanged.
   - `attach_test.go`:
     - `TestAnAttachToAPausedAgentsSessionIsRefused`: a record names the
       session, with no runtime, and the project attach answers 409.

**Verify:**
- `go test ./web/bridge/ -run 'Attach|Relay' -count=1`
- `go test ./web/bridge/ -count=1 -timeout 20m`. A hang in
  `TestRecipeRunMaxUSDSetsAPausingCapForTheRun` is the known intermittent one:
  rerun the package and record it.
- `go test -race ./web/bridge/ -run Relay -count=1`
- Mutation check: write the refusal without `clientMu`. The race run must
  report a race. Restore it.

## Task 3 — the daemon client opens an agent

**Files:** `internal/daemon/client.go` and `internal/daemon/client_test.go`.

1. **`upgrade`** is today's `Attach` handshake, with the path and the optional
   body as parameters.
   - It reads `Marshal-Agent` into `Attachment.AgentID`, trimmed.
   - It keeps every guarantee `Attach` has: the context bounds the handshake
     only, a cancelled handshake closes the connection, the bytes buffered
     behind the 101 are kept, and a refusal is an `*AttachError` with the
     daemon's sentence.
2. **`Attach`** calls `upgrade` with `attachPath` and its JSON body. Its
   behaviour does not change.
3. **`AttachAgent`** calls `upgrade` with `/api/agents/<escaped id>/attach` and
   no body.
   - It fails when the answer carries no `Marshal-Agent`, or one that is not
     the ID asked for. A daemon from before this plan answers 404 instead,
     which is already an `*AttachError`.
4. Tests, on the package's existing fake socket server:
   - `TestAttachAgentUpgrades`: the path, no body, and both IDs read.
   - `TestAttachAgentRefusalIsAnAttachError`: the status and the sentence.
   - `TestAttachAgentNeedsTheAgentHeader`.
   - every existing `Attach` test passes unchanged.

**Verify:**
- `go test ./internal/daemon/ -count=1`
- `go test -race ./internal/daemon/ -count=1`

## Task 4 — the session screen on an agent's session, and `/fleet`

**Files:**
- `internal/app/tui/model.go`, `commands_dispatch.go` and `status.go`;
- a new `internal/app/tui/agent_session_test.go`;
- `internal/commands/commands.go`.

1. **`WithAgentSession`** stores the ID and the name. `AgentSession` returns
   the ID.
2. **`beginEnd` refuses every ending on an agent's session** (decision 4):
   - It notes `This is agent <name>'s session: finish it from the fleet
     screen.`, refreshes the viewport, and returns nil.
   - It returns before any state changes, so no warning, stop or end flow
     starts.
3. **The status line** gains a segment after the mode segment, for an agent's
   session: `glyph.Agent`, a space, and the agent's name, in the identity
   style. Its priority is 0, and it is not pinned.
4. **`/fleet`:**
   - Add the command to `internal/commands/commands.go` after `detach`:
     "Leave this daemon session running and open the fleet screen", in the
     chat group, `TUIOnly`.
   - Its effect, in `tuiCommandEffects`, refuses as `/detach` does when the
     session is not a daemon session: "The fleet screen needs the daemon;
     this session runs embedded." It refuses while an ending settles, with
     `/detach`'s sentence.
   - Otherwise it sets a `toFleet` flag and returns `m.detach()`.
   - `ToFleet` returns the flag.
5. Tests in `agent_session_test.go`, built as `detach_test.go` builds a daemon
   session:
   - `TestEveryEndingIsRefusedOnAnAgentsSession`: a table of `/exit`,
     `/quit`, `/new`, `/clear` and a resume through `beginResume`. Each one
     notes the sentence, starts no end flow, and leaves `Detached` empty.
   - `TestDetachStillLeavesAnAgentsSession`: `/detach` detaches.
   - `TestTheStatusLineNamesTheAgent`.
   - `TestSlashFleetDetachesToTheFleet`: `ToFleet` and `Detached` are both
     set.
   - `TestSlashFleetNeedsADaemonSession`: in an embedded session it notes the
     refusal and does not quit.
   - The commands package's tests still pass with the new entry.

**Verify:**
- `go test ./internal/app/tui/ -run 'Agent|Fleet|Detach' -count=1`
- `go test ./internal/commands/ -count=1`
- `go test ./internal/app/tui/ -run TestTUIStaysOnTheClientSeam -count=1`

## Task 5 — W5 and W6 against a real bridge

**Files:** a new `test/daemon/agent_attach_test.go`.

1. A helper `attachAgent(t, h, agentID)` returns an `*attachment` through
   `h.client.AttachAgent`, `rpc.Dial` and `Host.Attach`, as
   `daemonHarness.attach` does.
2. `TestAViewerCannotEndAnAgentsSession` (W5):
   - Spawn an agent, with a fixture whose turn holds on a gate, and prompt it
     through its registry on a goroutine.
   - Open it with the helper. `AgentID` is the agent's.
   - `session.Close` fails, and its error reads as decision 3's sentence.
   - A `NewSession` on the viewer's host fails the same way.
   - The agent's run is still in flight: open the gate, and the prompt returns
     nil. `h.fleet.Snapshot()` still lists the agent.
3. `TestAPausedAgentsSessionIsRefusedEverywhere` (W6):
   - Spawn an agent and `Pause` it.
   - `h.client.Attach(h.root, <its session>)` is an `*AttachError` with 409.
   - `h.client.AttachAgent(<its id>)` is an `*AttachError` with 409.
4. `TestAnUnknownAgentIsNotFound`: `AttachAgent("nope")` gets 404.

**Verify:**
- `go test ./test/daemon/ -run 'Agent' -count=1 -v -timeout 10m`

## Task 6 — the fleet model opens (after plan 3)

**Files:** `internal/app/tui/fleet/`: `model.go`, `keys.go`, and a new
`open_test.go`.

1. **`⏎`** on a selected row ends the program with its `Target`:
   - In Agents, an agent row gives its ID, its name and its project root.
   - In Agents, a Sessions row gives its child's root and its session ID.
   - In Needs you, an agent item gives that agent's target.
   - On an intake item, or with nothing selected, `⏎` does nothing. Plan 5
     gives it approve and deny.
   - `q` and `ctrl+c` still end with the zero target.
2. **`Open`** returns the target. **`Selected`** returns the selected ID.
3. **`WithSelected`** starts the cursor on that ID once the first loaded state
   lists it, in Agents or Sessions.
4. **`WithNotice`** adds its line to Events when the model starts. It is not
   subject to the baseline rule, since it is not a transition.
5. **The footer** shows `⏎ open` while a row that can be opened is selected.
6. Tests in `open_test.go`, over plan 3's fake fleet:
   - `TestEnterOpensTheSelectedAgent`;
   - `TestEnterOpensADaemonSession`;
   - `TestEnterOnANeedsYouItemOpensItsAgent`;
   - `TestEnterOnAnIntakeItemDoesNothing`;
   - `TestQuitOpensNothing`;
   - `TestWithSelectedRestoresTheCursor`;
   - `TestWithNoticeAddsAnEvent`.
   - Regenerate the goldens if the footer change touched them, and inspect the
     diff: only the footer may move.

**Verify:**
- `go test ./internal/app/tui/fleet/ -count=1`

## Task 7 — the daemon loop: fleet, session, fleet (after plan 3)

**Files:** `internal/launch/daemon.go`, `launch.go`, `fleet.go`, and their
tests: `daemon_test.go` and `fleet_test.go`.

1. **`ProgramResult`** gains `ToFleet` and `Open`. `runProgram` reads them from
   the final model's `ToFleet()` and `Open()`, as it reads `Detached()`.
2. **`daemonClient`** gains `AttachAgent`. `daemon_test.go`'s fake gains it.
   - It serves exactly as the fake's `Attach` does, and sets `AgentID`.
   - It records the agent ID.
   - It fails with a configured error when one is set.
3. **One loop over `daemonStep`s replaces `runDaemon`'s body:**
   - `Run`'s daemon branch starts with a session step on the working root,
     with no session ID.
   - `Attach` starts with a session step on its root and its session ID.
   - `Fleet` starts with a fleet step.
   - The log, the version note and the connection handling are today's.
4. **A fleet step:**
   - It creates and starts the fleet client the first time, and keeps it for
     the rest of the loop (decision 7). An injected client is used as it is.
   - It runs `fleet.New` with the TUI config, `WithSelected` and, when the step
     has one, `WithNotice`.
   - The zero target ends the loop with the program's error.
   - An agent target becomes a session step with its `agentID`, `agentName`
     and `root`.
   - A session target becomes a session step with its `root` and
     `sessionID`.
5. **A session step:**
   - It dials through `AttachAgent` when it names an agent, else through
     `Attach`.
   - For an agent, the TUI options gain `tui.WithAgentSession(id, name)`.
   - When the dial fails and the fleet has run in this loop, the next step is
     the fleet, with the notice `could not open <name or session>: <error>`.
     Otherwise the error is returned as today.
6. **After a session step,** in this order (decision 6):
   - A lost connection. Once the fleet has run: the fleet, with the notice
     `lost the connection to <name or session>: <error>`. Before: today's
     error.
   - A program error is returned.
   - `ToFleet`: the fleet, selecting this session's agent or session ID.
   - A resume: the next session step is on the same root with the chosen ID.
   - `Detached`. Once the fleet has run: the fleet, with the selection.
     Before: today's message, and return.
   - Any other end. Once the fleet has run: the fleet, with the selection.
     Before: return, as today.
7. **The fleet client** is closed when the loop returns, if the loop created
   it.
8. Tests, with the fake daemon, a fake fleet client, and a program runner
   that switches on the model's type:
   - `TestTheFleetOpensAnAgentAndComesBack`: fleet, then `Open` of an agent,
     then the session screen with `AgentSession` set, then quit, then the
     fleet again with `Selected` on the agent, then quit. `AttachAgent` was
     called once.
   - `TestTheFleetOpensADaemonSession`: `Attach` with the target's root and
     ID.
   - `TestDetachingAnOpenedSessionReturnsToTheFleet`.
   - `TestSlashFleetFromAPlainSessionOpensTheFleet`: `Run` on the daemon, a
     session screen ending with `ToFleet`, then the fleet program.
   - `TestALostConnectionReturnsToTheFleetWithANotice`, using the fake's
     `drop`.
   - `TestARefusedOpenReturnsToTheFleet`: `AttachAgent` answers a 409
     `*AttachError`, and the fleet program starts with the notice.
   - `TestBeforeTheFleetEndingsAreUnchanged`: a plain daemon session's
     detach, end and lost connection behave as today. The existing tests in
     `daemon_test.go` cover most of this, and must pass unchanged.
   - `TestTheLoopClosesTheFleetClientItCreated`.

**Verify:**
- `go test ./internal/launch/ -count=1`
- `go test -race ./internal/launch/ -count=1`

## Task 8 — W4 against a real bridge (after plan 3)

**Files:** a new `test/daemon/fleet_loop_test.go`.

1. `TestTheFleetOpensAnAgentAndLeavesItRunning` (W4):
   - Use a fixture whose first turn holds on a gate and then replies. Spawn an
     agent.
   - Run `launch.Fleet(ctx, &out, h.socket, launch.WithProgramRunner(runner))`.
     The runner is scripted by the model it receives:
     1. The first `fleet.Model`: return `ProgramResult{Open: <the agent's
        target>}`. Task 6 tests the key that produces it.
     2. The `tui.Model`:
        - check that `AgentSession` is the agent's ID;
        - open a second viewer with Task 5's helper, and prompt through it on
          a goroutine;
        - wait until that viewer's `Status().Run` is set;
        - return `ProgramResult{}`, which is the quit.
     3. The second `fleet.Model`: check that `Selected` is the agent's ID,
        then return `ProgramResult{}`.
   - `launch.Fleet` returns nil.
   - The agent's run is still in flight afterwards, as the second viewer's
     `Status().Run` shows. Open the gate, and the run ends as done.
2. The test does not check the fleet's own `running` status for this run.
   This plan's authoring found that a viewer's turn does not reach the
   bridge's connection (spec F5). The second fleet pass sees the agent listed,
   not running.

**Verify:**
- `go test ./test/daemon/ -run TestTheFleetOpensAnAgentAndLeavesItRunning -count=1 -v -timeout 10m`
- `go test ./test/daemon/ -count=1 -timeout 15m`

## Task 9 — the docs, the record and full verification

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`, `cmd/webbridge/README.md`, and
`docs/daemon/p5-fleet-screen.md`: the "Plan 4b" placeholder only.

1. `AGENTS.md`: in the local daemon's paragraph, after `marshal attach [id]`,
   one sentence. `marshal fleet` opens an agent's or a daemon session's screen
   with `⏎`, and `/fleet` returns to the fleet from any daemon session.
2. `cmd/webbridge/README.md`: where it describes the project attach, describe
   the agent attach: the route, `Marshal-Agent`, the socket only, and the
   relay's refusals.
3. `docs/ARCHITECTURE.md`: in the daemon's description, one clause for the
   agent attach.
4. The manual run:
   - Build `marshal`, with the daemon running it.
   - `marshal fleet`. Spawn an agent with `curl` on the socket.
   - Open the agent with `⏎`. Prompt it. Try `/exit` and `/new` and read the
     sentence. Then `/fleet`, and land back on the fleet with the agent
     selected.
   - Open a plain daemon session from the Sessions group, and `/detach` back.
   - From a plain `marshal` session, `/fleet`.
   - Record what the fleet showed for the agent while the turn you started
     from its session screen ran, as the spec's finding predicts.
5. Fill the "Plan 4b" placeholder in the spec's Implementation record:
   - what was built;
   - where the plan was short, and how that was filled;
   - the manual run;
   - the verification, with each command's result.
6. Full verification, with each result recorded:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean, and so are `GOOS=linux go vet ./web/...` and
     `GOOS=windows go vet ./web/bridge/...`;
   - `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 30m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'`,
     which is CI's command, with every failure named;
   - `go test -race ./web/bridge/ -run 'Attach|Relay' -count=1` and
     `go test -race ./internal/launch/ ./internal/daemon/ -count=1`;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then
     `go test ./test/usability/... -run TestScripted -timeout 5m`.

**Verify:** the commands above, each recorded with its result.

## Done when

- `⏎` on the fleet screen opens an agent's session or a daemon session, and
  leaving it returns to the fleet with the row still selected.
- A viewer cannot close or replace any agent's session, through either attach.
  The project attach refuses every session an agent record names.
- `/fleet` opens the fleet from any daemon session.
- W4, W5 and W6 pass, and the record is filled.
