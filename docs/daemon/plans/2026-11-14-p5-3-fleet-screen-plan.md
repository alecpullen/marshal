# P5 plan 3: the fleet client and a read-only fleet screen — implementation plan

Date: 2026-11-14. Status: ready for review.

Base: `origin/main` at `cef1e353`. Every anchor below was checked there. The
main checkout's local `main` was behind it when this was written: pull first.

- **The baseline.** The full suite at `cef1e353` ran 122 packages ok. Only
  `internal/acp` failed, with `TestStackPatchApplyBudget` and
  `TestStackRoundTripBudget`. Both are among the 12 wall-clock budget tests
  that CI skips (`docs/testing-ci-audit.md`). The suite below is CI's command,
  which skips them.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`. This plan's are the header, F3's and F4's
  "Decided by plan 3" bullets, Migration order item 3, and the "Plan 3"
  record placeholder.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decisions 3, 4
and 9; F3; F4; Migration order item 3; Tests W3.
Screen design: [`docs/fleet-cli/design.md`](../../fleet-cli/design.md) §3.1 to
§3.3, §3.6, §3.9 and §5, with the spec's differences.
Previous plans: [P5 plan 1, the contract](2026-11-13-p5-1-contract-plan.md),
whose types this plan renders, and [plan 2, the inbox](2026-11-13-p5-2-inbox-plan.md),
whose groups and queue it shows.
Sibling plans: [plan 4a, requests](2026-11-14-p5-4a-requests-plan.md) and
[plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

`marshal fleet` opens a read-only fleet screen on the local daemon.
- **The left column** has three panels:
  - Agents, grouped as the bridge's inbox groups them, with the daemon's own
    sessions in a Sessions group below;
  - Needs you;
  - Events.
- **The right panel** shows the selected agent's Activity or Info tab.
- **It stays live** from the fleet stream, reconnects on its own, and shows
  the connection's state.

## Non-goals

- **No actions.** Spawn, answering, the diff, the finish flow, intake and
  cancelling a turn are plan 5. So is the Diff tab, and with it the `2` key.
- **No opening a session.** `⏎` is plan 4b.
- **No server profiles.** The client's transport can carry a token, but only
  the socket is wired. `--server` and `fleet.toml` are plan 7.
- **No notifications and no needs-you count** on the session screen. That is
  plan 6, which reuses this plan's transitions.
- **No web UI change and no bridge change.**

## Assumptions

- Every Go test command below runs with `TMPDIR=/private/tmp/p5-3-suite`.
  Create the directory first: `test/daemon` binds unix sockets under it.
- **The bridge's surface** (`web/bridge/http.go`):
  - The routes are in `Server.routes` (`:164`): `GET /api/daemon`,
    `GET /api/projects`, `GET /api/agents`, `GET /api/inbox`, and
    `GET /api/budgets` (`budget.go:384`).
  - On the socket, `SocketHandler` (`:122`) serves them all with no token.
  - `GET /api/events?stream=fleet` serves the fleet stream (`:668`, through
    `sse.go`).
    - Each event is `id: N` then one or more `data:` lines, then a blank line.
      A line starting with `:` is a heartbeat.
    - A fresh connect gets live events only. A reconnect sends its last ID as
      `Last-Event-ID` and gets the retained events after it.
    - A client whose ID fell out of the ring, or that lags, gets the data
      `{"type":"replay_overflow"}` with no `id:` line.
- **The deltas** are `contract.FleetDelta`, `BudgetDelta`,
  `ProjectRemovedDelta` and `InboxDelta` (`web/bridge/contract/stream.go`,
  `inbox.go`).
  - A delta's `sessionId` is the agent's ID: the classifier rewrites it
    (`fleetevents.go:80`).
  - The bridge moves an agent's `updatedAt` on every activity, mode, telemetry
    and run delta (`fleetevents.go:173`).
  - Kinds outside the loop also arrive: `watch`, `reroute`, `automation` and
    `network_block`.
- **The web UI's rules** this client ports are `applyDeltaTo`
  (`web/ui/src/lib/fleet.ts:82`), `mergeAgents` and `refetchAgentsFor`
  (`fleet.ts:211`, `:268`). The access revision rule is `applyAccessReport`'s
  (`interaction.ts:122`).
- **The seam:**
  - `acpclient`'s interfaces are in `internal/acpclient/acpclient.go`. Their
    method table is `Methods` (`methods.go:18`), and the table's tests are
    `methods_test.go`.
  - Its non-test files may import only what `allowedOnTheWireSide` allows
    (`imports_test.go:53`).
  - The TUI's imports are checked by `seamImportAllowed`
    (`internal/app/tui/boundary_test.go:27`).
- **The daemon client:** `internal/daemon.Client` dials the socket with an
  `http.Transport` whose `DialContext` ignores the URL's host
  (`internal/daemon/client.go:49`).
- **The TUI kit** this screen reuses:
  - `chrome.Frame` (`internal/app/tui/chrome/frame.go:21`) draws a titled,
    focusable panel border.
  - `chrome.SelectionStyle` styles the selected row.
  - `theme.LoadWithConfig` (`theme.go:211`) resolves a theme from
    `config.TUIConfig`'s fields and honours `NO_COLOR`. The session screen
    loads it in `loadTheme` (`model.go:7395`).
  - `glyph` (`glyph/glyph.go`) is the one glyph vocabulary.
  - `transcript_golden_test.go` is the golden-file pattern: a package `-update`
    flag, `NO_COLOR=1`, and the theme reloaded per render.
- **Launch:**
  - `Attach` (`internal/launch/daemon.go:125`) probes the daemon with
    `daemonProbeTimeout` and words a missing daemon as
    "no daemon answers on %s: %v; `marshal daemon install` starts one".
  - `openClientLog` opens `tui.log`.
  - `runProgram` (`launch.go:210`) runs any `tea.Model`.
  - Tests inject a `daemonClient` and a `ProgramRunner` (`daemon_test.go:62`,
    `:207`).
- **The user's TUI config** is read without a project layer as
  `cmd/marshal/daemon.go:191` reads it:
  `config.Load(config.LoadOptions{HomeDir: home, SkipProjectConfig: true})`.
- **`cmd/marshal`:** `run` dispatches subcommands (`main.go:76` onwards), and
  `runAttach` (`:147`) is the model for a daemon-only subcommand. Its tests swap
  `attachRunner` (`main_test.go:324`).

## Decisions this plan makes beyond the spec

F3's and F4's "Decided by plan 3" bullets record them.

1. **One read.** `Fleet.State()` returns one `FleetState`: rows, inbox,
   projects, budgets, gates, the daemon's info and the connection. It is
   consistent, as `Session.Status` is, rather than seven reads that can tear.
2. **Plan 3's interface has the reads and `Changed` only.** Plan 5 adds the
   actions, their routes and the last action's error, beside the answer types
   F1 already gives it.
3. **The route table** is `acpclient.FleetRoutes`, beside `Methods`.
   - `TestFleetRouteTableCoversTheInterface` keeps it complete.
   - `TestTheFleetRoutesExistOnTheBridge` (`test/daemon`) checks each route
     against a real bridge.
4. **`acpclient` may import the contract** in its non-test files, as the TUI
   may. Its own import test (`imports_test.go`) gains the one path.
5. **The client opens the stream before it reads.**
   - It reads the snapshot once the stream has answered 200, and applies the
     deltas that arrived meanwhile after the reads, in order.
   - The spec's order, reads then stream, loses whatever happens between the
     two. Every delta is safe to apply over a snapshot that already holds it.
6. **Freshness beyond the spec's rules:**
   - An inbox that names an agent with no row, or moves any agent to another
     group, makes the client refetch the agents, at most once a second. A turn
     ending changes an agent's status with no delta of its own.
   - Activity, mode, telemetry and run deltas move the row's `UpdatedAt` to
     the time the client applied them, as the bridge does.
   - A mode delta's access replaces the row's when the row has none, or when
     the delta's revision is not older than the row's. A refetch always
     replaces it.
   - Gate deltas fill `FleetState.Gates`, by agent ID.
   - A `project_removed` delta drops the project and its agents' rows.
   - Locally, the daemon's info is reread every 5s, because its sessions are
     not on the stream.
7. **Every full read is a baseline.**
   - The client counts full reads in `FleetState.Epoch`: the first connect,
     each reconnect, and each overflow.
   - The screen adds no agent events for the first state of an epoch. This is
     F8's baseline rule, and plan 6's notifications reuse it.
8. **States before the first read.** `Conn` starts as `FleetConnecting` and
   `Loaded` as false. The panels say "Connecting to local…" until the first
   read lands.
9. **The ST glyph comes from the inbox group,** so the glyph and the group
   always agree:
   - needs you is `⚠`, in the warning colour;
   - running is `●`, in the info colour;
   - ready is `✓`, in the success colour;
   - earlier is `✗` in the error colour when the row's status is `error`,
     and `·` muted otherwise.
10. **Two glyphs join the vocabulary:**
    - `glyph.Live`, `●`, is a running agent or a connected stream. It shares
      `Brand`'s shape, as `Beat` does, because the banner never appears on the
      fleet screen.
    - `glyph.Waiting`, `◌`, is a stream connecting or reconnecting.

    `glyph.Error`, `✗`, is a stream that is down.
11. **The layout's numbers.** Below 80×24 the screen says "terminal too small
    (80×24)". Otherwise:
    - The footer takes the bottom row.
    - The left column is 28 columns wide below 100 columns, and 40% of the
      width (rounded down) from 100 up. The right panel takes the rest.
    - Needs you shows as many rows as it has items: at least 1 and at most 5.
      Events shows 3 rows below 30 rows of height, and 6 from 30 up. Agents
      takes the rest. Each panel is a closed `chrome.Frame`, so it costs two
      border rows.

    Erratum: the width rule is the other way round from 100 up. The Agents
    table takes the larger pane and the right panel keeps 40%, because the
    table's fixed columns and their gaps fill a 40% pane on their own. The
    record in `docs/daemon/p5-fleet-screen.md` has the arithmetic.
12. **The right panel's tabs are Activity (`1`) and Info (`3`).** `2` stays
    free for plan 5's Diff, so the numbers never move. A Sessions row has no
    tabs: the panel shows its project, its session ID, its viewers and its
    last activity.
13. **The theme.** Launch reads the user's `[tui]` config, without the project
    layer, and passes it in. The fleet model resolves and reloads the theme
    once, in `New`, as the session screen does from its session's config. A
    read that fails uses `config.Default().TUI`, and the failure goes to
    `tui.log`.
14. **No mouse capture.** The fleet program asks for the alternate screen and
    nothing else, so the terminal's own selection works.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 3, 4a and 4b`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan, `2026-11-14-p5-4a-requests-plan.md` and
    `2026-11-14-p5-4b-open-agent-plan.md`.

  Another plan's executor may have committed them already. Leave the main
  checkout's other untracked work alone.
- Then branch: `git switch -c p5-3-fleet-screen` from `main`, or create a
  worktree on that branch.
- **Plans 4a and 4b's first tasks run beside this one.** Their files are
  apart from this plan's:
  - Plan 4a is `web/bridge`'s child and registry, `internal/acp` and
    `internal/app/session`.
  - Plan 4b's first tasks are the bridge's attach and relay,
    `internal/daemon`, and the session screen's model.
  - Each of the three adds its own new file in `test/daemon`.
  - Only plan 4b's later tasks, which open agents from this screen and change
    launch's loop, wait for this plan on `main`.
- Run the tasks in order. Run each task's Verify and commit it as
  `p5-3-fleet-screen: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

### The seam (`internal/acpclient/fleet.go`, Task 1)

```go
// FleetConn is the fleet stream's connection state (P5 F3).
type FleetConn int

const (
	FleetConnecting   FleetConn = iota // before the first connect
	FleetConnected                     // the stream is up
	FleetReconnecting                  // retrying, for under a minute
	FleetDown                          // retries have failed for a minute; still retrying every 30s
	FleetRejected                      // a 401: the stream and every retry have stopped
)

// FleetState is the fleet as the client last read it. Every slice and map is
// the caller's own copy.
type FleetState struct {
	Server   string    // the bridge's name on screen: "local" for the daemon
	Conn     FleetConn
	ConnErr  string    // why the stream is not connected; "" when it is
	Loaded   bool      // a full read has landed
	Epoch    uint64    // bumped by every full read: a connect, a reconnect, an overflow
	Agents   []contract.AgentStatus   // GET /api/agents' order, kept current by deltas
	Inbox    contract.Inbox
	Projects []contract.ProjectStatus
	Budgets  *contract.BudgetReport          // nil until read, or after a failed read
	Gates    map[string]contract.GateRecord  // the latest gate per agent ID, from gate deltas
	Daemon   *contract.DaemonInfo            // the local daemon's info; nil for a server
}

// Fleet is the fleet the TUI renders (P5 decision 3). Reads never block.
type Fleet interface {
	// State is the fleet as the client last read it.
	//
	// Routes: GET /api/agents, GET /api/inbox, GET /api/projects,
	// GET /api/budgets, GET /api/daemon, GET /api/events?stream=fleet.
	State() FleetState

	// Changed fires after anything State returns may have changed. It is a
	// coalescing signal with a buffer of 1. It is closed when the client
	// closes, so a reader learns there is nothing more to wait for.
	//
	// Routes: none.
	Changed() <-chan struct{}
}

// FleetRoute is one row of the fleet's route table.
type FleetRoute struct {
	Name   string   // the Fleet method
	Routes []string // "METHOD /path[?query]" as the doc's Routes: line lists them; empty for none
}

// FleetRoutes maps every Fleet method to the bridge routes that serve it.
var FleetRoutes []FleetRoute
```

### The client (`internal/fleetclient`, Tasks 2 and 3)

```go
// Transport is how the client reaches one bridge.
type Transport struct {
	Server  string       // the name the screen shows
	BaseURL string       // "http://daemon" on the socket
	HTTP    *http.Client // no overall timeout: the stream is long-lived
	// Token is the bearer token for one connect; nil sends none.
	Token func(ctx context.Context) (string, error)
	// Local marks the local daemon: GET /api/daemon is read.
	Local bool
}

// Socket is the local daemon's transport on the unix socket at path.
func Socket(path string) Transport

type Client struct{ /* unexported */ }

type Option func(*Client)

// Test seams; production uses the defaults in brackets.
func WithBackoff(first, max time.Duration) Option // [1s, 30s]
func WithResetAfter(d time.Duration) Option       // [1 minute connected]
func WithDownAfter(d time.Duration) Option        // [1 minute failing]
func WithRefetchEvery(d time.Duration) Option     // [1s: the agents refetch limit]
func WithDaemonEvery(d time.Duration) Option      // [5s]
func WithNow(now func() time.Time) Option         // [time.Now]

func New(t Transport, opts ...Option) *Client

// Start connects in the background and keeps the stream until ctx ends or
// Close is called. It returns at once.
func (c *Client) Start(ctx context.Context)
func (c *Client) Close() error

func (c *Client) State() acpclient.FleetState
func (c *Client) Changed() <-chan struct{}
```

### The fleet model (`internal/app/tui/fleet`, Tasks 5 and 6)

```go
type Model struct{ /* unexported */ }

type Option func(*Model)

func WithTUIConfig(cfg config.TUIConfig) Option
func WithNow(now func() time.Time) Option // the clock for ages and event times

func New(f acpclient.Fleet, opts ...Option) Model
func (m Model) Init() tea.Cmd
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (m Model) View() tea.View

// transitionKind is one change the Events panel reports, and plan 6 notifies.
type transitionKind int

const (
	transitionNeedsYou transitionKind = iota // an agent entered groups.needsYou, or a submission entered the queue
	transitionReady                          // an agent entered groups.ready
	transitionFailed                         // an agent's status became error
)

type transition struct {
	Kind    transitionKind
	AgentID string // "" for a submission
	Name    string // the agent's name, or the submission's title
	Pending contract.PendingKind // for transitionNeedsYou on an agent
}

// transitions compares two states of one epoch. A state from another epoch,
// or one that is not loaded, gives none.
func transitions(prev, next acpclient.FleetState) []transition
```

### Glyphs (`internal/app/tui/glyph/glyph.go`, Task 6)

```go
	// Live marks a running agent, or a connected stream, on the fleet screen.
	// It shares Brand's shape, which the fleet screen never shows.
	Live = Brand
	// Waiting marks a stream that is connecting or reconnecting.
	Waiting = "◌"
```

### Launch (`internal/launch/fleet.go`, Task 7)

```go
// Fleet runs the fleet screen on the local daemon at socket (P5 F4). It fails,
// saying what to do, when no local daemon answers.
func Fleet(ctx context.Context, stdout io.Writer, socket string, opts ...Option) error

// withFleetClient replaces the client built for the socket; tests inject it.
func withFleetClient(f acpclient.Fleet) Option
```

## Task 1 — the seam

**Files:**
- a new `internal/acpclient/fleet.go`, and a new `internal/acpclient/fleet_test.go`;
- `internal/acpclient/imports_test.go`;
- `internal/app/tui/boundary_test.go`.

1. Declare the seam's types, the interface and `FleetRoutes` as the Contracts
   section has them.
   - Every interface method's doc ends with a `Routes:` line.
   - `FleetRoutes` has one row per method, listing the same routes in the same
     order.
2. `allowedOnTheWireSide` allows `marshal/web/bridge/contract` (decision 4).
3. `seamImportAllowed` allows `marshal/web/bridge/contract`, under the "Data
   packages the TUI renders from" comment, as F1 says plan 3 does.
4. Tests in `fleet_test.go`, modelled on `methods_test.go`:
   - `TestFleetRouteTableCoversTheInterface`: every `Fleet` method has exactly
     one row, every row names a method, and no row repeats.
   - `TestFleetDocsNameTheirRoutes`: each method's `Routes:` doc line lists
     its row's routes in order, or reads `Routes: none.` for an empty row.

**Verify:**
- `go test ./internal/acpclient/ -count=1`
- `go test ./internal/app/tui/ -run TestTUIStaysOnTheClientSeam -count=1`

## Task 2 — the fleet client: reads, the stream and the deltas

**Files:** a new `internal/fleetclient/` package: `client.go`, `stream.go`,
`apply.go`, and their tests.

1. **`Socket`** builds a transport like `internal/daemon.NewClient`'s: an
   `http.Transport` dialling the socket, `BaseURL` `http://daemon`, `Server`
   `local`, `Local` true, and no token.
2. **`New` and `State`.** `New` sets `Server` and `FleetConnecting`. `State`
   copies every slice and map under the client's lock.
3. **The connect:**
   - It opens `GET /api/events?stream=fleet`, with the token when the
     transport has one.
   - Once the stream has answered 200, it reads `GET /api/agents`,
     `/api/inbox`, `/api/projects` and `/api/budgets`, and `GET /api/daemon`
     when `Local`. Each read is bounded by 10s.
   - The deltas that arrive during the reads are kept, and applied after the
     reads, in order (decision 5).
   - It then sets `Loaded`, bumps `Epoch`, sets `FleetConnected`, and fires
     `Changed`.
   - A failed budgets read leaves `Budgets` nil. Any other failed read fails
     the connect.
4. **The stream parser** reads event frames:
   - `id:` and `data:` lines, with several `data:` lines joined by newlines;
   - `:` heartbeat lines are ignored;
   - a blank line ends an event.

   It keeps the last `id` it saw for the reconnect.
5. **Applying a delta** (`apply.go`), keyed by `kind`, by `applyDeltaTo`'s
   rules plus decision 6:
   - `activity`: `Activity`.
   - `telemetry`: `ContextPct` and `ChangedFiles`, each when present.
   - `mode`: the access revision rule.
   - `hold`: `Held`.
   - `pending`: the status becomes `awaiting-approval` or `awaiting-question`
     from `pendingKind`, and the agents are refetched (F3).
   - `gate`: `Gates[agentID]`.
   - `run`: changes no field but `UpdatedAt`.
   - `activity`, `mode`, `telemetry` and `run` also set the row's `UpdatedAt`
     to now.
   - `budget`: the budgets are refetched.
   - `project_removed`: the project and its agents' rows are dropped.
   - `inbox`: replaces `Inbox`. When it names an agent with no row, or puts an
     agent in another group than the previous inbox did, the agents are
     refetched, at most once per `WithRefetchEvery`.
   - `watch`, `reroute`, `automation` and `network_block` change nothing, and
     fire no `Changed`.
   - An unknown kind changes nothing.
   - `{"type":"replay_overflow"}` refetches everything the connect read, and
     bumps `Epoch`.
6. **Refetching the agents** replaces every row with the snapshot's, which
   wins over anything a delta set. The client keeps no run digest, so
   `mergeAgents`' one exception does not apply.
7. **`Changed`** has a buffer of 1. A send never blocks.
8. **In this task a dropped stream ends the client.** The state becomes
   `FleetDown` with the error. Task 3 replaces that with reconnecting.
9. Tests, over an `httptest.Server`: a fake bridge serving fixture JSON built
   from the contract's types, and an events handler the test feeds frames
   into.
   - `TestTheFirstReadFillsTheState`: every field, `Loaded`, `Epoch` 1, and
     `FleetConnected`.
   - `TestDeltasDuringTheFirstReadAreKept`: a delta sent while `/api/agents`
     is held is applied after the read.
   - `TestDeltasUpdateTheRows`: a table, one case per kind and field above,
     including the access revision rule both ways and `UpdatedAt` moving.
   - `TestAPendingDeltaRefetchesTheAgents`.
   - `TestABudgetDeltaRefetchesTheBudgets`.
   - `TestAnInboxNamingAnUnknownAgentRefetchesAtMostOnceASecond`.
   - `TestAGroupChangeRefetchesTheAgents`.
   - `TestKindsOutsideTheLoopChangeNothing`.
   - `TestAnOverflowRefetchesEverythingAndBumpsTheEpoch`.
   - `TestTheParserJoinsDataLinesAndSkipsHeartbeats`.
   - `TestStateIsACopy`: changing a returned slice or map does not change the
     next `State`.
   - `TestChangedCoalesces`.
   - `TestTheDaemonIsReadOnlyLocally`.

**Verify:**
- `go test ./internal/fleetclient/ -count=1`
- `go test -race ./internal/fleetclient/ -count=1`, with no data race
- Mutation check: apply the deltas buffered during the first read before it,
  not after. `TestDeltasDuringTheFirstReadAreKept` must fail. Restore it.

## Task 3 — the fleet client: reconnecting, down, a 401, the daemon's sessions

**Files:** `internal/fleetclient/` and its tests.

1. **A dropped stream reconnects** (F3):
   - The state becomes `FleetReconnecting`, with the error in `ConnErr`.
   - It waits the backoff: the first wait, doubling each failure, up to the
     maximum.
   - Each attempt sends the last event ID as `Last-Event-ID`.
   - A successful attempt rereads everything as the connect does, bumps
     `Epoch`, and sets `FleetConnected`.
2. **The backoff resets** to its first wait once the stream has stayed up for
   `WithResetAfter`.
3. **Down.** When attempts have failed without a break for `WithDownAfter`,
   the state becomes `FleetDown`. It keeps retrying at the maximum wait.
4. **A 401** on any request, a read or the stream:
   - stops the stream and every retry;
   - sets `FleetRejected`, with `ConnErr` "token rejected for <Server>";
   - is never retried, so the client makes no further request.
5. **Locally,** while connected, the client rereads `GET /api/daemon` every
   `WithDaemonEvery`. It fires `Changed` only when the answer differs.
6. **`Close`** ends the stream and every timer, and `Start`'s goroutine
   returns. `Close` twice is a no-op.
7. Tests, with the waits shortened to milliseconds:
   - `TestADroppedStreamReconnectsWithTheLastEventID`: the second request
     carries the header, the state passes through `FleetReconnecting`, and
     `Epoch` is 2 after.
   - `TestTheBackoffDoublesToItsMaximum`: the attempt times, from a fake
     events handler that refuses.
   - `TestTheBackoffResetsAfterAMinuteConnected`.
   - `TestTheStateGoesDownAfterAMinuteOfFailures`, and back to connected when
     the handler accepts again.
   - `TestA401StopsEverything`: no request reaches the fake after the 401, and
     `ConnErr` says "token rejected for test".
   - `TestTheDaemonsSessionsAreReread`.
   - `TestCloseStopsTheClient`: no goroutine is left, checked as the
     package's other tests check (a done channel the loop closes).

**Verify:**
- `go test ./internal/fleetclient/ -count=1`
- `go test -race ./internal/fleetclient/ -count=1`
- Mutation check: send no `Last-Event-ID` on a reconnect.
  `TestADroppedStreamReconnectsWithTheLastEventID` must fail. Restore it.

## Task 4 — the client against a real bridge

**Files:** a new `test/daemon/fleetclient_test.go`.

1. `TestTheFleetClientFollowsARealBridge`, on `newDaemonHarness`:
   - Use a fixture whose one turn holds on a gate and then replies with text.
   - Start a client with `fleetclient.Socket(h.socket)`.
   - Spawn an agent. Within 10s its row is in `State().Agents`.
   - Prompt it through its registry, on a goroutine.
   - Within 5s the inbox lists the agent in `running`.
   - Open the gate. Within 5s the agent has left `running`.
   - `Daemon` lists the project's child.
2. `TestTheFleetRoutesExistOnTheBridge`: for each route of every row of
   `acpclient.FleetRoutes`, a request to the harness's socket answers neither
   404 nor 405.
   - Split the route's query from its path.
   - Bound each request by 2s, and read only the status of the events route.

**Verify:**
- `go test ./test/daemon/ -run 'FleetClient|FleetRoutes' -count=1 -v -timeout 10m`

## Task 5 — the fleet model: state, keys and events

**Files:** a new `internal/app/tui/fleet/` package: `model.go`, `keys.go`,
`events.go`, `transitions.go`, and their tests. The rendering in this task is
the minimum the tests need. Task 6 draws the screen.

1. **`New`** stores the client and options, and reloads the theme from the
   TUI config (decision 13).
2. **`Init`** returns a command that waits on `Changed()` and a one-second
   tick, for the ages.
3. **On each change**, the model reads `State()`, then:
   - adds an event line for each connection change;
   - adds an event line for each transition from the previous state;
   - re-arms the wait.
4. **`transitions`** (Contracts):
   - Two states of different epochs, or either not loaded, give none
     (decision 7).
   - Needs you: an agent ID in `groups.needsYou` that was not before, with its
     row's pending kind. A submission ID in the queue that was not before.
   - Ready: an agent ID in `groups.ready` that was not before.
   - Failed: a row whose status is `error` and was not before.
5. **The Events ring** keeps the last 200 lines, newest last. Each line is
   `HH:MM <text>` in local time, from the model's clock:
   - `<name> needs you: approval` or `… question`;
   - `intake: <title>`;
   - `<name> is ready`;
   - `<name> failed`;
   - `connected to <server>`, `reconnecting to <server>`,
     `<server> is down: <ConnErr>` and `token rejected for <server>`.

   An agent's name is its `Name`, else the first 8 characters of its ID.
6. **The rows.** The Agents panel lists `groups` in order: needs you, running,
   ready, earlier.
   - Each non-empty group has a header line, which the cursor skips.
   - An ID with no row is skipped.
   - Locally, a Sessions group follows, one row per session of every child in
     `Daemon.Children`.
7. **Selection is by ID,** an agent's or a session's, so it survives a reorder.
   When the selected ID goes, the cursor keeps its index, clamped to the list.
8. **Keys** (F4, without plan 4b's and plan 5's):
   - `tab` and `shift-tab` move focus: Agents, Needs you, Events, the right
     panel, and round again.
   - `j`, `k`, `down` and `up` move the cursor in Agents and in Needs you.
     Moving in Needs you selects that item's agent for the right panel.
   - `1` shows Activity, and `3` shows Info.
   - `z` zooms the focused panel to the whole screen above the footer. `z`
     again, or `esc`, unzooms.
   - `/` starts a filter on Agents: case-insensitive substring of the name or
     the project. `esc` clears it, and `enter` keeps it and leaves the input.
   - `?` toggles a help overlay listing every key.
   - `q` and `ctrl+c` quit.
9. **While the state is not connected,** the panels keep the last state. A
   flag tells Task 6's rendering to dim them.
10. Tests, over a fake `acpclient.Fleet` in `fleet_test.go` whose state the
    test sets and whose `Changed` it fires:
    - `TestTransitions`: a table over `transitions`, with each kind, the epoch
      rule and the not-loaded rule.
    - `TestTheFirstStateAddsNoAgentEvents`, and again after an epoch change.
    - `TestTransitionsAddEvents`.
    - `TestConnectionChangesAddEvents`.
    - `TestTheEventsRingKeeps200`.
    - `TestTabCyclesTheFocus`.
    - `TestSelectionFollowsTheIDAcrossAReorder`.
    - `TestTheCursorSkipsGroupHeaders`.
    - `TestTheFilterNarrowsTheAgents`.
    - `TestNeedsYouSelectsItsAgent`.
    - `TestQuitKeysQuit`: `q` and `ctrl+c` each return `tea.Quit`.

**Verify:**
- `go test ./internal/app/tui/fleet/ -count=1`
- `go test ./internal/app/tui/ -run TestTUIStaysOnTheClientSeam -count=1`
- Mutation check: drop the epoch rule from `transitions`.
  `TestTheFirstStateAddsNoAgentEvents` must fail. Restore it.

## Task 6 — the fleet screen drawn, and its golden frames

**Files:**
- `internal/app/tui/fleet/view.go` and `internal/app/tui/fleet/view_test.go`;
- `internal/app/tui/fleet/testdata/`;
- `internal/app/tui/glyph/glyph.go`: the two constants.

1. Add `glyph.Live` and `glyph.Waiting` (Contracts).
2. **`View`** returns a `tea.View` with `AltScreen` on and no mouse mode
   (decision 14).
   - Below 80×24 it shows "terminal too small (80×24)" alone.
   - Otherwise it draws the layout of decision 11. Each panel is a closed
     `chrome.Frame`, with its title and count, and `Focused` set on the
     focused panel.
3. **The Agents panel's columns,** by terminal width (fleet-cli §3.1):
   - 80–99: ST, NAME, CTX, Δ, AGE.
   - 100–139: then PROJECT and MODE.
   - 140 and up: then ACTIVITY, COST and CHECK.
   - The ST glyph and its colour follow decision 9.
   - NAME and ACTIVITY share what is left of the width; the others are fixed
     at their widest value.
   - CTX is `contextPct` with `%`. Δ is `changedFiles`. AGE is the time since
     `updatedAt`: `0s` to `59s`, then `1m` to `59m`, `1h` to `23h`, then
     days.
   - PROJECT is the root's base name. MODE is the access mode, or `?` when
     access is nil.
   - COST is the agent's `spentUsd` from the budgets, as `$0.00`, or `–`.
   - CHECK is the latest gate: `pass`, `fail`, or `–`.
   - Numbers are right-aligned. Text that does not fit ends in `…`.
   - The selected row uses `chrome.SelectionStyle`.
   - A group header is the group's name, muted.
4. **A Sessions row** shows a muted `·`, the project's base name, the first 8
   characters of the session ID, its viewers as `N viewer(s)`, and its age
   since `lastActive`.
5. **The Needs you panel** shows one line per queue item:
   - an agent's line is its name and its pending kind;
   - an intake's line is `intake`, its origin and its title.
6. **The Events panel** shows the ring's last lines, the newest at the bottom.
7. **The Activity tab**, for an agent:
   - its status and its activity line;
   - what it waits on: its pending kind, and a one-line summary of the
     request;
   - its context, its changed files and its age;
   - its latest gate, when it has one.

   The summary is a port of `describePending` (`fleet.ts:119`): an approval's
   command, else its tool's name; a question's first question; else
   "Approval required" or "Answer required". It never fails on a payload of
   another shape.
8. **The Info tab** (fleet-cli §3.6) shows:
   - the mode, the origin and the owner;
   - a context bar, 20 cells wide;
   - the cost against the per-agent cap, when the budgets are loaded;
   - the branch and its target;
   - the latest gate;
   - the workspace's name and source;
   - the pull request's URL;
   - the age.

   A missing value is `–`.
9. **The right panel's title** is its tab strip: `[Activity]  Info` or
   `Activity  [Info]`. For a Sessions row it is `Session`.
10. **Before `Loaded`,** every panel says "Connecting to <server>…".
11. **The footer** is `<server> <dot>`, then the focused panel's keys, then
    `?help q quit`.
    - The dot is `glyph.Live` in the success colour when connected,
      `glyph.Waiting` in the warning colour when connecting or reconnecting,
      and `glyph.Error` in the error colour when down or rejected.
    - While not connected, `ConnErr` follows the dot, and every panel's text
      is muted.
12. **Golden frames (W3),** in `view_test.go`:
    - Use a `-update` flag, as `transcript_golden_test.go` does.
    - The scene is fixed, on a fixed clock: five agents, one per group plus
      one `error` agent in earlier; one intake submission; two daemon
      sessions; three event lines; budgets loaded; one gate.
    - The files are `fleet-80x24`, `fleet-100x30`, `fleet-140x40`,
      `fleet-100x30-info`, `fleet-100x30-down` and `fleet-too-small` (79×24),
      each `.golden`, rendered under `NO_COLOR=1`.
    - `TestTheFleetScreenMatchesItsGoldens` compares the six.
    - `TestColourAddsNoText`: for each size, a render with the 256-colour
      theme, with its ANSI stripped, equals the golden byte for byte.
    - `TestEachStateGlyphHasItsColour`: in the 256-colour render, each ST
      glyph appears styled in its colour (decision 9), so no state is told by
      colour alone and none lacks its colour.

**Verify:**
- `go test ./internal/app/tui/fleet/ -count=1`
- `go test ./internal/app/tui/fleet/ -run Golden -count=1 -update`, then
  inspect the six files by eye, and commit them
- `go test ./internal/app/tui/... -count=1 -timeout 20m`
- Mutation check: draw the running glyph uncoloured.
  `TestEachStateGlyphHasItsColour` must fail. Restore it.

## Task 7 — `marshal fleet`

**Files:**
- a new `internal/launch/fleet.go`, and a new `internal/launch/fleet_test.go`;
- `internal/launch/launch.go`: the options struct's new field;
- `cmd/marshal/main.go` and `cmd/marshal/main_test.go`.

1. **`launch.Fleet`** (Contracts):
   - It returns at once on a done context, and wraps the context with
     `signal.NotifyContext`, as `Run` does.
   - It probes the daemon's info with `daemonProbeTimeout`. A failure answers
     "launch: no daemon answers on <socket>: <err>; `marshal daemon install`
     starts one", as `Attach` does. A profile other than `local` answers
     "launch: the daemon on <socket> runs the <profile> profile; the fleet
     screen needs the local daemon".
   - It opens `tui.log` with `openClientLog`.
   - It reads the user's TUI config (decision 13).
   - It builds the client: the injected one, else
     `fleetclient.New(fleetclient.Socket(socket))`, started on the context
     and closed on return.
   - It runs `fleet.New(client, fleet.WithTUIConfig(cfg))` through the program
     runner, and returns the program's error.
2. **`options`** gains `fleetClient acpclient.Fleet` for `withFleetClient`. An
   injected client is neither started nor closed by `Fleet`.
3. **`cmd/marshal`:**
   - Add `var fleetRunner = launch.Fleet`, and dispatch `fleet` to a new
     `runFleet`, beside `attach`.
   - `runFleet` refuses `--embedded`, wherever it appears, with "marshal
     fleet: --embedded cannot be combined with fleet: the fleet screen needs
     the daemon".
   - It refuses any argument with "marshal fleet: unexpected argument %q".
     Plan 7 adds `--server` and `login`.
   - It calls `fleetRunner` with `config.DaemonSocketPath(home)`.
4. Tests:
   - `fleet_test.go`, with `daemon_test.go`'s fake daemon and a fake fleet
     client:
     - `TestFleetWithoutADaemonFails`: the message names
       `marshal daemon install`.
     - `TestFleetRefusesAServerProfileDaemon`.
     - `TestFleetRunsTheFleetProgram`: the runner receives a `fleet.Model`.
     - `TestFleetUsesTheUsersTheme`: with a user config setting `[tui]`'s
       theme, the model was built with it. Check through a test accessor in
       the fleet package if the model exposes none.
   - `main_test.go`, modelled on `TestAttachRoutesToTheAttachRunner`:
     - `TestFleetRoutesToTheFleetRunner`;
     - `TestFleetRefusesArgumentsAndEmbedded`.

**Verify:**
- `go test ./internal/launch/ ./cmd/marshal/ -count=1`
- `CGO_ENABLED=1 go build -o marshal ./cmd/marshal` and
  `./marshal fleet --embedded` prints the refusal

## Task 8 — the docs, the record and full verification

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`, and
`docs/daemon/p5-fleet-screen.md`: the "Plan 3" placeholder only.

1. `AGENTS.md`:
   - add `fleet` (the fleet screen on the local daemon) to the list of
     subcommands `marshal` dispatches;
   - add `internal/fleetclient/`, "the fleet client over the daemon's socket",
     to "Where things live", under "App shell & config";
   - add `fleet` to the TUI's list of interactive surfaces.
2. `docs/ARCHITECTURE.md`:
   - in the client seam row, one clause: `acpclient.Fleet` and
     `internal/fleetclient`, the fleet screen's read of a bridge;
   - in the entrypoint row: `marshal fleet`.
3. The manual run:
   - Build `marshal`.
   - Install and start the daemon (`marshal daemon install`), or run
     `webbridge --profile local` by hand as plan 2's record did.
   - Run `marshal fleet` in an 80×24 terminal, then at 100 and 140 columns.
   - Spawn an agent with `curl` on the socket's `POST /api/agents`, and watch
     it appear and move between groups.
   - Start a plain `marshal` session in another terminal, and check it appears
     under Sessions.
   - Stop the daemon. Check the dot and the dimming. Start it, and check the
     reconnect.
   - Record what was seen at each size, and anything a terminal showed that
     the goldens did not.
4. Fill the "Plan 3" placeholder in the spec's Implementation record:
   - what was built;
   - where the plan was short, and how that was filled;
   - the manual run;
   - the verification, with each command's result.
5. Full verification, with each result recorded:
   - `gofmt -l internal cmd test web` prints nothing;
   - `go vet ./...` is clean;
   - `CGO_ENABLED=1 go test -p=1 ./... -count=1 -timeout 30m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'`,
     which is CI's command, with every failure named;
   - `go test -race ./internal/fleetclient/ ./internal/app/tui/fleet/ -count=1`;
   - `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`, then
     `go test ./test/usability/... -run TestScripted -timeout 5m`.

**Verify:** the commands above, each recorded with its result.

## Done when

- `marshal fleet` shows the local daemon's agents, needs you, events and
  sessions, live, with Activity and Info for the selected agent.
- It reconnects, goes down and comes back as F3 says, and says so in the
  footer and in Events.
- W3's goldens pass at 80×24, 100×30 and 140×40, with and without colour.
- `TestTUIStaysOnTheClientSeam` passes with the contract package as the one
  new allowed import, and the record is filled.
