# P7 plan 1: the bridge addresses, follows and indexes every session — implementation plan

Date: 2026-11-18. Status: ready for review.

Base: `main` at `bdd20ddc`. Every anchor below was checked there.

- **The baseline** is recorded in the spec's "Implementation record".
- **The spec and this plan** land on `main` with PR #157 before execution
  starts.
- **Plan 2 runs after this one.** It reads the contract types Task 1
  generates and the routes Tasks 7 and 8 add.

Approved design: [p7-studio-sessions.md](../p7-studio-sessions.md): D1–D11,
D14's bridge half; Error handling; Testing's bridge table.
Previous plans: [P4 plan 3, viewers](2026-11-10-p4-3-viewers-plan.md),
[P4 plan 4, the relay](2026-11-10-p4-4-relay-plan.md) and
[P5 plan 4c, viewer turns](2026-11-15-p5-4c-viewer-turns-plan.md), whose
project children, attach handshake and `session/follow` this plan extends.
Sibling plan: [plan 2](2026-11-18-p7-2-studio-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **One rule resolves a session id** for every per-session route, the event
  stream and image uploads, and it finds a terminal's live session on its
  project child.
- **A closed session reopens on its project's child,** never on the control
  child, and the bridge's own sessions are refused.
- **The bridge follows every client session on a project child,** so a
  terminal's turn sets `Busy` and streams to the Studio.
- **`GET /api/sessions/all`** lists every project's sessions, open and closed.
  A `sessions` delta on the fleet stream says when to read it again.
- **An ask from the Studio's own turn** is in its session's row and answerable
  through the existing answer routes.

## Non-goals

- **Any app change** beyond the regenerated `contract.g.dart` and its fixture
  test. Plan 2.
- **Paging closed sessions** past the listing's first page.
- **The requests of a turn the terminal started** (spec D11).
- **Changing `GET /api/sessions`' answer.**

## Assumptions

Each was checked at the base.

- **Resolution today.**
  - `registryForSession` (`web/bridge/http.go:846`) wraps
    `RuntimeForSession` (`fleet.go:1099`), whose steps are
    `liveRuntimeForSession` (`fleet.go:1085`), `runtimeForAgent` and a
    persisted agent started on demand.
  - `LogForSession` (`fleet.go:1132`) wraps the same.
  - `loadSession` (`http.go:955`) resolves inline at `:971`.
  - `imageTarget` (`images.go:115`) falls back to a project child that already
    tracks the id.
- **Project children.**
  - Each project child is a `projectChild` (`projectchild.go:31`) with `reg`,
    `log` and `root`, built in `startProjectChildLocked`
    (`projectchild.go:232`). There, `attachSessions(pc.log, child, reg, …)`
    keeps every session except a client's (`:269`).
  - `projectChildFor` (`projectchild.go:144`) starts one on demand.
  - `projectChildSnapshot` (`idle.go:125`) lists the live ones.
- **The attach handshake** (`attach.go:99`):
  - it opens a session with `pc.reg.New` (`:155`) or resumes one with
    `pc.reg.Resume` (`:185`);
  - it refuses an agent's session from three sources (`:177-183`);
  - it never calls `Follow`.
- **Following.**
  - `Registry.Follow` (`registry.go:639`) records `followAsked`, and
    `resumeAll` (`:1157`) follows again after a restart (`:1189`).
  - `observeFollowedRun` (`:689`) sets and clears `Busy` from `followed_run`
    updates.
  - `followAgentSession` (`fleet.go:2227`) is the model for logging a failed
    follow.
- **Registry state changes** happen in:
  - `trackAs` (`registry.go:277`) and `dropSessionLocked` (`:363`, reached
    from `closeTracked` and `forget`);
  - `attachViewer` and `detachViewer` (`:432`, `:444`);
  - `beginTurn` and `endTurn` (`:1053`, `:1063`);
  - `observeFollowedRun`'s two branches;
  - `resumeAll`'s busy reset (`:1171`);
  - `awaitPermission` and `awaitQuestion`'s registration and deferred cleanup
    (`:897-909`, and the same in `awaitQuestion`).
- **The control child.**
  - `control()` (`control.go:122`) starts it.
  - Its `OnRestart` clears `projectSessions` (`:146-151`).
  - `controlRegistryForRoot` (`:342`) is called only by `listSessions`
    (`http.go:891`).
  - Its own sessions are `controlRuntime.sessionID` and `projectSessions`
    (`control.go:35-37`).
- **The child's `session/list`** answers `updatedAt` as RFC3339 and
  `messageCount` as `_meta.messageCount`, a number, with `nextCursor` when
  more remain (`internal/acp/session.go:766-786`). Its page is 50.
- **Answers.** `Fleet.ResolvePermission` and `ResolveQuestion`
  (`fleet.go:1139`, `:1154`) search the agent runtimes only.
- **Planners.** `PlannerService.records()` (`planner_service.go:626`) holds
  each planner's `SessionID`. `plannerRoutes` (`:1174`) builds the service and
  keeps no reference to it.
- **The fleet stream.**
  - Every delta goes through `Fleet.emit` (`notify.go:172`).
  - `ProjectRemovedDelta`'s emit (`http.go:595`) is the model.
- **The idle sweep.**
  - `sweepIdleSessions` (`idle.go:45`) closes `idleCandidates` through
    `closeIfIdle`.
  - `EventLog` counts subscribers per session (`events.go:78`) and calls
    `OnUnsubscribe` when the last one leaves (`:220`). Nothing sets it on a
    project child's log.
- **Errors and tests.**
  - `writeErr` (`http.go:399`) maps `ErrUnknownSession` to 404.
  - Test fleets with fake project children are `projectFleet` and
    `projectFleetWith` (`projectchild_test.go:38`, `:45`). A fake control
    child is installed through `f.newControl`, as
    `TestListSessionsWorksForAProjectWithNoLiveAgent` (`agentpath_test.go:384`)
    does.
  - `TestAProjectChildLogsTheFleetsSessionsOnly`
    (`fleetchild_test.go:45`) pins today's log filter.

## Decisions this plan makes beyond the spec

1. **`OnChange` carries what changed.** The fleet emits a `sessions` delta on
   every change. It drops the root's cached listing only on a new track and on
   a turn's end, since those are the only changes that move a title or a
   message count.
2. **The index's open rows come from a new `Registry.clientSessions`,** not
   from `summaries`, because the index needs `Busy` and `summaries` is the
   daemon document's shape.
3. **Unsubscribing the last stream subscriber touches the session.** Setting
   `pc.log.OnUnsubscribe` to `pc.reg.touch` makes the idle timeout count from
   when the Studio stopped watching, not from the session's last turn.
4. **The Fleet keeps the planner service** (`f.planners`), set by
   `plannerRoutes`, so one ownership map covers agents, the control agent and
   planners.
5. **`load` keeps today's body for an agent's session.** It runs
   `reg.Load` with the agent's path translation. A project session is answered
   from `OpenSession` without a `session/load`: it is already open and
   followed. Its answer is the log's tail, as today.
6. **Listings are fetched in parallel** across roots, each with its own 5 s
   timeout, so one stalled root bounds the index at 5 s.

## Execution

- After PR #157 merges: `git switch -c p7-1-bridge` from `main`, or create a
  worktree on that branch.
- Run each task's Verify and commit it as `p7-1-bridge: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p7-1`.
- Each test this plan adds is proved by mutation: revert the production line
  it covers and watch it fail, then restore with `git checkout`.

## Contracts

### Wire types (`web/bridge/contract/session.go`, `stream.go`; Task 1)

```go
// SessionIndex is GET /api/sessions/all's answer: every project's sessions,
// sorted by root. Projects is never null.
type SessionIndex struct {
	Projects []ProjectSessions `json:"projects"`
}

// ProjectSessions is one project root's sessions. Sessions is never null.
// Error is why the root's listing failed; its open sessions still list.
type ProjectSessions struct {
	Root     string       `json:"root"`
	Sessions []SessionRow `json:"sessions"`
	Error    string       `json:"error,omitempty"`
}

// SessionRow is one session the index lists (P7 D7).
type SessionRow struct {
	SessionID    string          `json:"sessionId"`
	Title        string          `json:"title,omitempty"`
	UpdatedAt    int64           `json:"updatedAt,omitempty"` // Unix milliseconds
	MessageCount int             `json:"messageCount,omitempty"`
	Open         bool            `json:"open"`    // tracked on its project's live child
	Viewers      int             `json:"viewers"` // terminals attached through the relay
	Busy         bool            `json:"busy"`
	Pending      *PendingRequest `json:"pending,omitempty"` // a turn the bridge started is parked on it
}

// DeltaSessions announces that a project's sessions changed. Its payload is
// SessionsDelta.
const DeltaSessions DeltaKind = "sessions"

// SessionsDelta is a "sessions" fleet delta: read the index for Project again.
type SessionsDelta struct {
	Kind    DeltaKind `json:"kind"`
	Project string    `json:"project"`
}
```

### The registry (`web/bridge/registry.go`, Task 3)

```go
// registryChange is what changed in a tracked session (decision 1).
type registryChange int

const (
	changeTracked     registryChange = iota // a new session is tracked
	changeDropped                           // a session is no longer tracked
	changeViewers                           // a viewer attached or detached
	changeTurnStarted                       // the bridge's own turn or a followed run began
	changeTurnEnded                         // either ended, or a restart interrupted it
	changePending                           // a request was parked or cleared
)

// Registry gains:
	// OnChange, if set, is called after a tracked session changes, outside
	// the registry's locks.
	OnChange func(sessionID string, change registryChange)

// clientSession is one tracked client session, as the index reads it.
type clientSession struct {
	ID         string
	Viewers    int
	Busy       bool
	LastActive time.Time
}

// clientSessions lists the tracked sessions that are not the fleet's.
func (r *Registry) clientSessions() []clientSession

// PendingRequest is the request id's session is parked on, with its payload;
// an approval wins over a question, as Pending's does.
func (r *Registry) PendingRequest(sessionID string) (contract.PendingRequest, bool)
```

### The event log (`web/bridge/events.go`, Task 4)

```go
// Subscribers is how many live subscribers sessionID has.
func (l *EventLog) Subscribers(sessionID string) int
```

### Sessions (`web/bridge/sessions.go`, new; Task 5)

```go
// ResolvedSession is where one session id lives (P7 D2).
type ResolvedSession struct {
	Reg       *Registry
	Log       *EventLog
	SessionID string        // the ACP session id to address Reg with
	Root      string        // the project root
	Agent     *agentRuntime // the agent whose session it is; nil for a project session
}

// ResolveSession finds id among the live: an agent runtime by session id, an
// agent id or persisted agent, then a live project child that tracks it as a
// client's session. It never starts a project child.
func (f *Fleet) ResolveSession(id string) (ResolvedSession, error)

// OpenSession is ResolveSession, and for a closed session the reopening on its
// project's child (P7 D3).
func (f *Fleet) OpenSession(ctx context.Context, id string) (ResolvedSession, error)

// ErrSessionOwned refuses a session the bridge owns (P7 D5).
type ErrSessionOwned struct{ Owner string } // "agent <name>", "the control agent", "planner <id>"

func (e ErrSessionOwned) Error() string // "bridge: session belongs to " + Owner

// ownedSessions maps each session the bridge owns to its owner phrase.
func (f *Fleet) ownedSessions() map[string]string

// knownRoots is the sorted union of the workspace's projects and the live
// project children's roots.
func (f *Fleet) knownRoots() []string

// listRoot is root's first page of stored sessions, normalised (P7 D8),
// through the listing cache (P7 D10).
func (f *Fleet) listRoot(ctx context.Context, root string) ([]contract.SessionSummary, error)

// Fleet gains:
	listNow     func() time.Time // the listing cache's clock; nil is time.Now
	listTimeout time.Duration    // each session/list's bound; zero is 5 s; tests shorten it

// dropListing forgets root's cached listing; dropListings forgets every one,
// failures included.
func (f *Fleet) dropListing(root string)
func (f *Fleet) dropListings()

// rootOfSession is the root whose listing names id.
func (f *Fleet) rootOfSession(ctx context.Context, id string) (string, error)

// followClientSession follows a client session on a project child, logging a
// failure as followAgentSession does.
func (f *Fleet) followClientSession(ctx context.Context, reg *Registry, sessionID string)
```

### The control child (`web/bridge/control.go`, Task 2)

```go
// controlChild is the control agent's child, started if needed.
func (f *Fleet) controlChild(ctx context.Context) (*Child, error)
```

### The planner service (`web/bridge/planner_service.go`, Task 5)

```go
// Fleet gains:
	planners *PlannerService // set by plannerRoutes; nil without one

// sessionIDs maps each planner session the store knows to its planner's id.
func (s *PlannerService) sessionIDs() map[string]string
```

### The index (`web/bridge/sessions.go`, Task 8)

```go
// SessionIndex builds GET /api/sessions/all's answer; root narrows it.
func (f *Fleet) SessionIndex(ctx context.Context, root string) contract.SessionIndex

// serveSessionIndex serves GET /api/sessions/all.
func (s *Server) serveSessionIndex(w http.ResponseWriter, r *http.Request)
```

## Task 1 — the wire types

**Files:** `web/bridge/contract/session.go`, `web/bridge/contract/stream.go`,
`web/bridge/contract/fixture_test.go`, `test/contract/session.json`,
`test/contract/stream.json`, `app/lib/src/client/contract.g.dart`
(generated), `app/test/client/contract_fixture_test.dart`.

1. Declare the types as Contracts gives them, with the comments.
2. Add a fixture value for each new type: `SessionIndex` with one root
   carrying an open row with a pending approval and one carrying `error`;
   `SessionRow` closed; `ProjectSessions`; `SessionsDelta`. Name each in
   `fixtureTypes`, as `SessionSummary` and `ProjectRemovedDelta` are.
3. Regenerate the Dart contract with
   `go test ./web/bridge/contract/dartgen -run TestDartContractIsCurrent -update`.
4. Add the four types to the Dart fixture test's decoder map, as
   `SessionSummary` (`contract_fixture_test.dart:261`) and
   `ProjectRemovedDelta` (`:322`) are.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/contract/... -count=1`,
then `cd app && flutter test test/client/contract_fixture_test.dart`.

## Task 2 — listing on the control child, without a registry

**Files:** `web/bridge/control.go`, `web/bridge/http.go`,
`web/bridge/sessions_test.go` (new).

1. `controlChild` calls `control(ctx)` and returns its child, or
   `bridge: control agent is not running` when it has none. It is what
   `controlRegistryForRoot` did, without the `NewRegistry`.
2. Delete `controlRegistryForRoot`.
3. In `listSessions`, the no-live-agent fallback calls `controlChild` and
   sends `session/list` on that child, with the same params as today.
4. Tests:
   - `TestListingLeavesTheControlChildsHooksAlone`: with a fake control child
     through `f.newControl`, set the child's `OnRequest` to a sentinel after
     its first use, then run `GET /api/sessions?cwd=<root>` three times. The
     child's `OnRequest` is still the sentinel. Compare with
     `reflect.ValueOf(fn).Pointer()`.
   - `TestListSessionsWorksForAProjectWithNoLiveAgent` passes unchanged.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -run 'ListSessions|ListingLeaves' -count=1`.

**Mutation check:** build a `NewRegistry` on the child inside
`controlChild`; `TestListingLeavesTheControlChildsHooksAlone` must fail.
Restore.

## Task 3 — the registry reports changes and keeps parked payloads

**Files:** `web/bridge/registry.go`, `web/bridge/registry_change_test.go`
(new).

1. A private `changed(id, change)` calls `OnChange` when it is set. It is
   always called after the registry's locks are released.
2. Call it at each site in Assumptions' list:
   - `trackAs`: `changeTracked` only when it creates the record, not when it
     updates one.
   - `closeTracked` and `forget`: `changeDropped`.
   - `attachViewer` on success, and `detachViewer` when a count moved:
     `changeViewers`.
   - `beginTurn`, and `observeFollowedRun`'s started branch:
     `changeTurnStarted`.
   - `endTurn`, `observeFollowedRun`'s ended branch, and each session
     `resumeAll` resets: `changeTurnEnded`.
   - `awaitPermission` and `awaitQuestion` after registering and in their
     deferred cleanup: `changePending`.
3. Beside `permSession` and `quesSession`, keep each parked request's params
   under its id. Set them where the session is recorded, and delete them in
   the same deferred cleanup. `PendingRequest` returns the approval if the
   session has one, else the question, with `Kind` `approval` or `question`,
   the id, and the params as received.
4. `clientSessions` returns the non-fleet records, in `summaries`' order.
5. Tests:
   - `TestTheRegistryReportsEachChange`: one table over `New`, a second
     `track` of the same id (no call), `attachViewer`, `detachViewer`,
     `beginTurn`, `endTurn`, a `followed_run` started and ended through the
     child's notification, and `Close`. Each row asserts the exact calls.
   - `TestOnChangeRunsWithoutTheLock`: an `OnChange` that calls `lookup`
     does not deadlock.
   - `TestAParkedRequestKeepsItsPayload`: a `session/request_permission` from
     the fake child is answered by `PendingRequest` with its params. After
     `ResolvePermission` it is gone. The same for a question.
   - `TestClientSessionsLeaveTheFleetsOut`: a `TrackFleet` session is absent.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -run 'RegistryReports|OnChangeRuns|ParkedRequest|ClientSessions' -count=1`.

## Task 4 — a watched session is not idle

**Files:** `web/bridge/events.go`, `web/bridge/idle.go`,
`web/bridge/projectchild.go`, `web/bridge/idle_test.go`.

1. `Subscribers` reads `subCount` under the log's lock.
2. In `sweepIdleSessions`, read `pc.log` with `child` and `reg` under
   `pc.mu`, and skip a candidate whose log has a subscriber before calling
   `closeIfIdle`.
3. In `startProjectChildLocked`, set `pc.log.OnUnsubscribe` to touch the
   session on `pc.reg` (decision 3).
4. Tests:
   - `TestAWatchedSessionIsNotSwept`: a client session idle past the timeout
     with a subscriber on the child's log stays open through a sweep.
   - `TestAnUnwatchedSessionIsSweptAfterItsTimeout`: after the subscriber
     unsubscribes, a sweep at the same instant keeps it, because the session
     was just touched. A sweep a timeout later closes it.
   - `TestAnIdleSessionIsClosed` passes unchanged.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -run 'Swept|Idle|Sweep' -count=1`.

## Task 5 — resolution, listings, ownership and reopening

**Files:** `web/bridge/sessions.go` (new), `web/bridge/fleet.go` (the
`planners` field and the listing cache fields), `web/bridge/control.go` (its
restart hook), `web/bridge/planner_service.go`,
`web/bridge/sessions_test.go`.

1. **`ResolveSession`.** Steps 1 and 2 are a call to `RuntimeForSession`,
   unchanged. Its runtime fills `Agent`, `Root` from `rt.root`, and `Reg`,
   `Log` and `SessionID` from the runtime. For
   step 3, walk `projectChildSnapshot` in root order. The first child whose
   `reg.tracksClient(id)` is true answers, with `pc.reg`, `pc.log`, the id
   and `pc.root`. A miss is `ErrUnknownSession`. `RuntimeForSession` keeps
   its signature and its callers.
2. **The listing cache.** These are new fields on `Fleet` under a mutex of
   their own.
   - Per root: the normalised listing, its error, and when it was read.
     There is also an `id → root` map.
   - `listRoot`:
     - serves a success younger than 10 s and a failure younger than 30 s;
     - otherwise calls `controlChild`, then `session/list` with `cwd` root
       under `listTimeout` (5 s when zero);
     - parses each item's `updatedAt` from RFC3339 to Unix milliseconds,
       leaving 0 when absent or unreadable, and reads `messageCount` from
       `_meta.messageCount`;
     - records every id it names in the map.
   - In `control()`, the child's `OnRestart` also calls `dropListings`.
3. **`knownRoots`.** `ws.Projects()` and the keys of `f.projects`, cleaned,
   de-duplicated and sorted.
4. **`rootOfSession`.**
   - It answers from the map when the map names `id`.
   - Otherwise it lists every known root (decision 6) and answers from the
     refreshed map.
   - If no root names `id`, it returns `ErrUnknownSession`.
5. **`ownedSessions`.**
   - Every agent record's `SessionID` (`ws.Agents()`), plus the classifier's
     map (`sessionAgent`), each as `agent <display name>`
     (`agentDisplayName`).
   - The control runtime's `sessionID` and `projectSessions` values, as
     `the control agent`.
   - `f.planners.sessionIDs()` when `f.planners` is set, as
     `planner <id>`.
   - In `plannerRoutes`, set `s.fleet.planners = svc`. `sessionIDs` reads
     `records()` and keeps the records with a session id.
6. **`followClientSession`.** It calls `reg.Follow` and logs any error other
   than `ErrUnsupported{Feature: "follow"}`, as `followAgentSession` does.
7. **`OpenSession`.**
   - `ResolveSession` first; anything it resolves is returned as is.
   - On `ErrUnknownSession`: `ownedSessions` names `id` → `ErrSessionOwned`.
     Otherwise:
     1. `rootOfSession`;
     2. `projectChildFor(root)`;
     3. if `pc.reg` does not track `id`: `pc.reg.Resume(ctx, AgentPath(root), id)`,
        then `followClientSession`;
     4. on a failed resume, `rearmLinger(pc)` and return the resume's error,
        as the attach handshake does at `attach.go:185-191`;
     5. return the project result.
8. Tests, on `projectFleetWith` with a fake control child:
   - `TestResolveSessionOrder`: with a live agent session and a project child
     tracking a client session, each id resolves to its own registry, and
     `Agent` is set only for the agent's.
   - `TestResolveSessionSkipsFleetSessions`: a `TrackFleet` session on a
     project child with no agent mapping is `ErrUnknownSession`.
   - `TestResolveSessionNeverStartsAChild`: an id only a listing names is
     `ErrUnknownSession`, and no project child was built.
   - `TestOpenSessionResumesOnTheProjectChild`: a closed session the control
     child's listing names is resumed on that root's child. `session/resume`
     and `session/follow` are its calls there, and the control child sees no
     `session/load`, `session/resume` or `session/new` for it.
   - `TestOpenSessionRefusesTheBridgesOwn`: a paused agent's session, the
     control agent's project session and a planner record's session are each
     `ErrSessionOwned`, naming the owner, and no child is started.
   - `TestOpenSessionUnknownIs404`: an id no listing names is
     `ErrUnknownSession`.
   - `TestListRootNormalises`: an RFC3339 `updatedAt` becomes milliseconds,
     and `_meta.messageCount` becomes `messageCount`.
   - `TestListRootCachesAFailedRoot`: a root whose list fails is asked once
     across calls within 30 s on the `listNow` clock, and again after.
   - `TestAControlRestartDropsTheListings`.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -run 'ResolveSession|OpenSession|ListRoot|ControlRestartDrops' -count=1`.

**Mutation check:** in `OpenSession`, resume on the control child's registry
instead of `pc.reg`; `TestOpenSessionResumesOnTheProjectChild` must fail.
Restore.

## Task 6 — project children follow, log and announce their sessions

**Files:** `web/bridge/attach.go`, `web/bridge/projectchild.go`,
`web/bridge/attach_test.go`, `web/bridge/fleetchild_test.go`.

1. In `attachProject`, call `followClientSession` after a successful
   `pc.reg.New` and after a successful `pc.reg.Resume`. A session already
   tracked was followed when it was tracked.
2. In `startProjectChildLocked`:
   - pass a nil `keep` to `attachSessions`, so the child's log keeps every
     session;
   - set `pc.reg.OnChange` to emit `contract.SessionsDelta{Kind:
     contract.DeltaSessions, Project: pc.root}` through `f.emit` on every
     change;
   - call `dropListing(pc.root)` on `changeTracked` and `changeTurnEnded`
     (decision 1).
3. Replace `TestAProjectChildLogsTheFleetsSessionsOnly` with
   `TestAProjectChildLogsEverySession`: a fleet session, a client session from
   `New` and an untracked session each get one notification from the fake,
   and the child's log holds all three.
4. Tests:
   - `TestAnAttachFollowsItsSession`: a new and a resumed attach each send one
     `session/follow` for the session on the child's stdio connection.
   - `TestATerminalTurnMarksTheSessionBusy`: the fake sends a `followed_run`
     started update for a session only once it has received `session/follow`
     for it, as the real child does. After an attach and that update,
     `pc.reg.lookup(id)` reports `Busy`. After the ended update it does not.
   - `TestEachChangeAnnouncesItsProject`: an attach, a viewer leaving, a
     followed run starting and ending, and a close each append one `sessions`
     delta naming the root to the fleet log.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -run 'Attach|LogsEverySession|TerminalTurn|AnnouncesItsProject' -count=1`.

**Mutation check:** remove the `followClientSession` call after `pc.reg.New`;
`TestAnAttachFollowsItsSession` and `TestATerminalTurnMarksTheSessionBusy`
must fail. Restore.

## Task 7 — the routes resolve through one rule

**Files:** `web/bridge/http.go`, `web/bridge/fleet.go`,
`web/bridge/images.go`, `web/bridge/sessions_test.go`,
`web/bridge/fleetchild_test.go`.

1. `registryForSession` returns `ResolveSession`'s `Reg`, `Log` and
   `SessionID` on the fleet path. Its non-fleet path is unchanged.
2. `LogForSession` returns `ResolveSession`'s `Log`.
3. `imageTarget` resolves through `registryForSession` alone, with its
   project-child fallback removed. Its `root` parameter stays for its
   callers, unused, with a comment saying why.
4. `loadSession` on the fleet path calls `OpenSession`:
   - **An agent's result.** Keep today's body exactly: `agentPath(cwd)`, then
     `reg.Load`, then the tail (decision 5).
   - **A project result.** Answer `{"sessionId", "events"}` from
     `Log.Tail(SessionID)` with no `session/load`.
5. In `writeErr`, map `ErrSessionOwned` (`errors.As`) to 409 with its
   message, beside `ErrUnknownSession`'s case.
6. Tests:
   - `TestATerminalTurnStreams`: after an attach, a `session/update` for the
     session from the fake reaches a subscriber of
     `/api/events?sessionId=<id>`.
   - `TestAPromptDuringATerminalTurnIs409`: with the fake of
     `TestATerminalTurnMarksTheSessionBusy`, `POST /api/sessions/<id>/prompt`
     answers 409 during the followed run and 202 after it.
   - `TestEveryPerSessionRouteReachesAProjectSession`: for a client session
     on a project child, `GET …/stack`, `GET …/access`, `POST …/prompt`,
     `POST …/cancel` and `GET /api/events?sessionId=` each reach the child.
     Assert the child's recorded call or the stream's subscription.
   - `TestLoadOpensAClosedSession`: `POST …/load` for a session only the
     listing names answers 200, and the child then tracks it.
   - `TestLoadOfAnOwnedSessionIs409`.
   - `TestLoadOfAnAgentSessionIsUnchanged`:
     `TestLoadSessionSendsTheAgentsViewOfCwd` passes unchanged, and an agent
     session's `load` still sends `session/load`.
   - `TestAnImageReachesAProjectSession`, through the route's existing test
     helper.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -count=1 -timeout 15m`.

**Mutation check:** have `LogForSession` keep `RuntimeForSession`;
`TestATerminalTurnStreams` must fail. Restore.

## Task 8 — the index, and answers through project children

**Files:** `web/bridge/sessions.go`, `web/bridge/http.go`,
`web/bridge/fleet.go`, `web/bridge/sessions_test.go`,
`docs/studio-flutter/route-parity.md`.

1. `SessionIndex(ctx, root)`:
   - **Roots.** `knownRoots()`, or just `root` when it is set; an unknown
     `root` gives an index with no projects.
   - **Listings.** List every root in parallel through `listRoot`
     (decision 6). Build `ownedSessions` once.
   - **One group per root,** in root order.
     - Its open rows come first: the live child's `reg.clientSessions()`,
       minus owned ids. Each takes its listing entry's title, `updatedAt`
       and `messageCount` when the listing names it, else `lastActive` as
       `updatedAt`. Add `Open: true`, `Viewers`, `Busy`, and `Pending` from
       `reg.PendingRequest`.
     - Then the closed rows: every listed id that is neither open nor owned.
     - Each part is newest first.
     - A listing error sets the group's `Error`.
   - **Never null.** `Projects` and every `Sessions` are non-nil.
2. `serveSessionIndex`:
   - A server with no fleet answers 501, as `attachProject` does.
   - It reads `root` from the query.
   - It writes the index with 200.
   - Register `GET /api/sessions/all` beside `GET /api/sessions`. The new
     pattern is more specific than `GET /api/sessions/{id}/…`, so it cannot
     shadow a session id.
3. `Fleet.ResolvePermission` and `ResolveQuestion` try each project child's
   registry (`projectChildSnapshot`) after the runtimes, with the same
   `ErrGone` rule.
4. In `docs/studio-flutter/route-parity.md`, add the row
   `| GET | /api/sessions/all | — | dropped: the Studio reads it from P7 plan 2 |`
   after the `POST /api/sessions` row.
5. Tests:
   - `TestTheIndexGroupsOpenAndClosedSessions`: two roots, one with a live
     child tracking a client session that the listing also names. Its row is
     open, with the listing's title and the registry's viewers and busy flag,
     and it is not repeated as closed.
   - `TestTheIndexLeavesTheBridgesOwnOut`: an agent's, the control agent's
     and a planner's sessions in a listing are absent.
   - `TestTheIndexShowsADeadRootBesideItsSiblings`: one root's list fails.
     That group carries `error`, the other lists, and the status is 200.
   - `TestTheIndexIsBoundedByASlowRoot`: a fake control child that never
     answers one root's list, with `listTimeout` at 100 ms. The request
     returns within 1 s with that root's `error`, and the other root lists.
   - `TestTheIndexNarrowsToARoot`.
   - `TestTheIndexIsNeverNull`: no projects gives `{"projects":[]}`, and an
     empty root gives `"sessions":[]`.
   - `TestTheIndexCarriesAParkedRequest`: a prompt from the bridge on a
     client session, then a permission request from the fake. The row's
     `pending` has its id and params.
   - `TestAnAnswerReachesAProjectSession`:
     `POST /api/permissions/<toolCallId>` resolves that request, and the
     fake receives the decision.

**Verify:** `TMPDIR=/private/tmp/p7-1 go test ./web/bridge/ -run 'TheIndex|AnswerReaches' -count=1`,
then `cd app && flutter test test/route_parity_test.dart`.

**Mutation check:** drop the owned-session filter from the closed rows;
`TestTheIndexLeavesTheBridgesOwnOut` must fail. Restore.

## Task 9 — the record, the docs and full verification

**Files:** `docs/daemon/p7-studio-sessions.md`, `AGENTS.md`.

1. Fill the spec's "Plan 1" record:
   - what landed;
   - any decision the executor had to make;
   - the mutation checks and their results.
2. In `AGENTS.md`'s local-daemon paragraph, after the sentence about a watch
   resuming its session, add: "The Studio lists every session a daemon's
   projects hold, the TUI's included, and opens any of them on its project's
   child; the bridge follows each one, so a turn typed in the terminal shows
   in the Studio (P7)."
3. Full verification:
   - `gofmt -l web cmd` prints nothing;
   - `go vet ./web/...` is clean;
   - `TMPDIR=/private/tmp/p7-1 scripts/test.sh` passes, with no failure
     beyond the spec's recorded baseline;
   - `TestWebIsStdlibOnly` passes;
   - `cd app && flutter test` passes.

**Verify:** the full verification above.

## Done when

- A terminal's open session resolves on every per-session route, the event
  stream and image uploads.
- `load` reopens a closed session on its project's child, refuses the
  bridge's own, and leaves an agent's unchanged.
- A terminal's turn sets `Busy` and streams to `/api/events?sessionId=`.
- `GET /api/sessions/all` answers the spec's shape, and each change of a
  project child's sessions emits one `sessions` delta for its root.
- A parked request of the Studio's own turn is in its row and answerable.
- Every test named above passes, each mutation check fails as described, and
  the full verification passes.

## What plan 2 relies on

- `contract.SessionIndex`, `ProjectSessions`, `SessionRow`, `SessionsDelta`
  and `DeltaSessions` in `contract.g.dart`.
- `GET /api/sessions/all[?root=]`, `POST /api/sessions/{id}/load` answering
  409 for an owned session, and the `sessions` delta.
- `404` from `/stack` for a session the idle sweep closed.
