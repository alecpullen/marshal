# P5 plan 8: containers' viewers and their allowlist — implementation plan

Date: 2026-11-16. Status: ready for review.

Base: `origin/main` at `1bc6d64b`, where plans 1 to 6 are merged. Every
anchor below was checked there.

- **The baseline** is CI's suite command at `1bc6d64b`: `scripts/test.sh full`
  passed, 127 packages ok, exit 0. It is recorded in the spec's "Plan 7"
  record entry, which this batch shares.
- **The spec edits are already made,** uncommitted in the main checkout, in
  `docs/daemon/p5-fleet-screen.md`: F6's "Decided by plan 8" bullets,
  Migration order item 8, the Design table, Findings, and the "Plan 8" record
  placeholder.
- **This plan runs beside plan 7.** Every task here needs nothing from it.
  Only the manual run in Task 6 waits for plan 7 on `main`, because a
  container's agent runs only on a server-profile bridge, and only plan 7's
  `--server` reaches one from the TUI.

Approved design: [p5-fleet-screen.md](../p5-fleet-screen.md): decision 6; F6's
containerized agent and the allowlist; Migration order item 8; Tests W9.
Previous plans: [plan 4b, opening an agent](2026-11-14-p5-4b-open-agent-plan.md),
whose relay refusals this plan extends;
[plan 6, needs you](2026-11-15-p5-6-needs-you-plan.md), whose own-child viewer
sockets and `relayEnd` this plan gives to containers.
Sibling plan: [plan 7, server profiles](2026-11-16-p5-7-server-profiles-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **Every agent's session can be opened,** a containerized agent's included.
  Its container serves viewers on a second socket beside its control socket,
  and the bridge's agent attach reaches it.
- **A container's viewer can drive the session screen and nothing else:** its
  turns, controls, answers, reads, notes and browse calls. Every host call,
  every config write and every sign-in is refused, with a sentence saying it
  "is not available for a containerized agent".
- **The refusal holds at both ends.** The relay answers a refused method
  itself and never forwards it, and the agent refuses it again on the viewer
  connection.

## Non-goals

- **Writing user config or credentials from a container's session screen**
  (decision 6). The web UI keeps those.
- **Watching or prompting a container's agent from the local daemon.** The
  local profile runs host processes only (P4 D4), so a container's agent is
  always a server's.
- **Rebuilding images.** An image whose marshal predates this plan starts as
  before and has no viewer socket (decision 3).

## Assumptions

Each was checked at `1bc6d64b`.

- **A container's agent runs `marshal acp --listen`** on
  `/run/marshal/agent.sock` (`web/bridge/container.go:165`), in the socket
  directory mounted from the bridge's `SocketDir`, whose host-side socket path
  is `socketPath` (`container.go:158`). `start` removes a stale socket before
  running the container (`container.go:285`). The run vector's environment is
  explicit, never inherited (`TestContainerEnvIsExplicitNotInherited`).
- **`newRuntime`'s container branch** builds a `Child` over a container
  transport (`web/bridge/fleet.go:493`). `startRuntime` records a viewer socket
  only for a host process (`fleet.go:968`), and `releaseAgent` removes
  whatever socket the runtime recorded (`fleet.go:2128`).
  `TestAContainerChildHasNoViewerSocket` (`web/bridge/ownviewers_test.go:91`)
  pins that a container's runtime has none, and `TestTheAgentAttachRefusals`
  (`web/bridge/attach_test.go:913`) has a row for the container's 409
  (`attach_test.go:995`).
- **What the runtime learns about a container's child comes from `Child`'s
  fields, not from its transport's type.** `Child.Containerized` is a field
  "rather than a type assertion on Transport because a type assertion no test
  transport can satisfy leaves every translating call site untestable"
  (`web/bridge/child.go:93`).
- **The bridge records each agent's version at start** (`checkAgentVersion`),
  and marks the runtime's `versionWarning` when it differs from its own. A
  mismatch is "a warning, never a refusal — refusing would turn a nuisance into
  an outage" (`fleet.go:989`). Custom and derived images are used as they are,
  so their marshal can be older than the bridge.
- **The agent attach refuses a container's agent** with 409 (`web/bridge/attach.go:268`),
  and otherwise dials the runtime's viewer socket with a `relayEnd` of the
  agent's own registry (`attach.go:261`).
- **The relay reads every client frame** (`web/bridge/relay.go:172`). Today it
  decodes only requests (`decodeClientRequest`, `relay.go:247`) and refuses
  two of them, answering with JSON-RPC `-32000` and an uncoded error in `data`
  (`refusal`, `relay.go:271`).
- **`marshal acp` refuses `--viewers` with `--listen`** (`cmd/marshal/main.go:96`).
  `--listen` runs `acp.ListenAndServe` (`internal/acp/listen.go:26`): one
  connection at a time, each with an idle deadline (`listen.go:127`), all
  sharing one agent host. `--viewers` alone runs `RunWithViewers`
  (`internal/acp/viewers.go:60`), whose accept loop serves any number of viewers
  with no idle deadline (`viewers.go:109`).
- **A viewer connection** is served by `serveViewer` (`viewers.go:45`): a
  `MarshalOnly` server (`viewers.go:48`) whose handlers refuse the lifecycle
  calls (`internal/acp/host.go:395`). `MarshalOnly` is checked in `dispatch`
  before the handler is looked up (`internal/acp/server.go:937`), and
  notifications are dispatched through the same function. `$/cancel_request`
  is handled before dispatch, on the read goroutine.
- **The session screen's methods** are the rows of `acpclient.Methods`
  (`internal/acpclient/methods.go:19`), plus four the client sends outside
  that table: `initialize`, `session/sync`, `session/stack_busy` and
  `$/cancel_request`. `TestACPClientMethodTableMatchesTheServer`
  (`internal/acp/acpclient_methods_test.go:20`) checks the table against the
  agent's registered handlers.
- **The session screen survives a refused host read.** A failed `trust/prompt`
  reads as no prompt (`internal/acpclient/rpc/settings.go:78`), and the model
  caches are filled only from lists the host returns (`internal/app/tui/model.go:1277`).
- **The contract package holds data as well as types** (`web/bridge/contract/doc.go`),
  and the TypeScript generator reads only type and constant declarations
  (`web/bridge/contract/tsgen/tsgen.go:153`, `:233`), so a list variable
  does not reach `contract.ts`.

## Decisions this plan makes beyond the spec

1. **The list** is `contract.ContainerViewerMethods`, sorted, in a new
   `web/bridge/contract/viewers.go`. It holds 63 methods:
   - the connection's own: `initialize`, `session/status`, `session/sync`,
     `session/stack_busy` and `$/cancel_request`;
   - turns: `session/prompt`, `session/stop`, `session/steer`,
     `session/steering_pop`, `session/steering_clear`, `session/review`,
     `session/agent_run`, `session/suggest`, `session/verify_proposal`,
     `session/swarm_start`, `session/subagent_cancel`, `session/rollback`, and
     the plan runs: `session/sdd_preflight`, `session/sdd_start`,
     `session/sdd_answer`, `session/sdd_stop`, `session/sdd_author`,
     `session/sdd_clear`, `session/sdd_discard` and `session/sdd_scaffold`;
   - controls: `session/set_mode` and `session/set_effort`;
   - answers: `session/pending`, `session/answer_approval`,
     `session/answer_question`, `session/answer_skill_gate` and
     `session/answer_access`;
   - reads and browse calls: `session/sheet`, `session/stack`,
     `session/stack_node`, `session/callers`, `session/stack_unsubscribe`,
     `session/work_list`, `session/work_inspect`, `session/work_stop`,
     `session/messages`, `session/file_index`, `session/intelligence`,
     `session/plans`, `session/diff`, `session/file`, `session/config`,
     `session/prompt_history`, `session/prompt_history_save`,
     `session/command_list`, `session/command`, `session/command_action`,
     `session/memory_list`, `session/memory_delete`, `session/skills_list`,
     `session/skills_load`, `session/plugins_list`,
     `session/watch_resume_take` and `session/postmortem`;
   - notes: `session/note`, `session/notice_set`, `session/notice_clear` and
     `session/notice_dismiss`.

   Every method not on it is refused. The test of Task 1 names these refusals
   among the session screen's methods, so a new method of the screen fails it
   until someone puts it on one side:
   - every `Host` method, and with them every `config/` and `trust/` method;
   - the lifecycle and the ending: `session/new`, `session/close`,
     `session/load`, `session/resume`, `session/delete`, `project/hold`,
     `project/release`, `session/end_work`, `session/end_accept`,
     `session/end_wait` and `session/exit_analysis`;
   - credentials and sign-ins: `session/answer_sudo`,
     `session/set_sudo_unlock`, `session/mcp_login` and
     `session/mcp_reconnect`. A sudo password is a credential entering the
     container;
   - the settings' writes: `session/apply_config`, `session/reload_config`,
     `session/save_project_config`, `session/save_verify_commands`,
     `session/skills_install`, `session/skills_remove`,
     `session/skills_autoload`, `session/skill_gate_set`,
     `session/skill_gate_clear`, `session/plugins_install_scan`,
     `session/plugins_install_confirm`, `session/plugins_install_discard` and
     `session/plugins_remove`.

   The agent-side slash commands (`session/command`) stay on the list: each of
   them works on the session's own state and workspace, and none writes user
   config.
2. **The refusal's sentence,** at both ends, is "<method> is not available for
   a containerized agent". A refused request is answered with it, in the shape
   the relay's `refusal` already writes. A refused notification has no answer:
   it is dropped, and the relay logs the first one of each method per attach.
3. **The bridge tells the container where to serve viewers through the
   environment, not a flag.** The agent's run vector gains
   `MARSHAL_ACP_VIEWERS=unix:///run/marshal/viewers.sock`.
   - An image whose marshal predates this plan ignores the variable and starts
     as before, with no viewer socket. A flag would make that marshal refuse
     its arguments, so every spawn on an older custom image would fail: the
     outage `checkAgentVersion` was written to avoid.
   - `marshal acp --listen` reads the variable once at startup and removes it
     from its own environment, so nothing the agent runs inherits it.
   - `cmd/marshal` also accepts `--viewers` with `--listen`, as F6 says, for
     running an agent by hand. The flag wins over the variable.
4. **Viewers beside `--listen` always get the allowlist.** There is no
   unrestricted pairing, and no flag that turns it on: only a container runs
   `--listen`, and a separate flag the bridge could leave out would fail open.
   `RunWithViewers`, the project children's mode, is unchanged.
5. **One agent host serves both listeners.** The control socket keeps its
   one-connection-at-a-time loop with the idle deadline, and the viewer socket
   gets `RunWithViewers`' accept loop, shared rather than copied. The viewers
   end when the host does.
6. **The attach reaches a container through its host-side socket,**
   `viewers.sock` in the agent's `SocketDir`, beside `agent.sock`.
   - `newRuntime`'s container branch puts the path on the `Child` it builds,
     as it sets `Containerized` there, and `startRuntime` records it on the
     runtime. There is no stale-file removal of its own: `start` removes both
     sockets before the container runs, and the agent removes its own again
     when it binds.
   - The attach's `relayEnd` for a container carries the list. The 409 for a
     container's agent is gone.
   - A failed dial is 502, "dial the agent's container: <error>". When the
     runtime carries a version warning, the sentence adds "; its image runs
     another marshal version, which may serve no viewer socket: rebuild it".
7. **A restricted relay fails closed.** A frame that does not decode as one
   JSON-RPC message (a batch, or malformed JSON) is dropped and logged rather
   than forwarded. An answer to one of the agent's own requests, which carries
   no method, passes unchanged, so approvals and questions still reach the
   viewer and its answers reach the agent.

## Execution

- In the main checkout, if `git status` shows any of these uncommitted, commit
  them to `main` first, as explicit paths, with the message
  `docs/daemon: record P5 plans 7 and 8`:
  - `docs/daemon/p5-fleet-screen.md`;
  - this plan and `2026-11-16-p5-7-server-profiles-plan.md`.
- Then branch: `git switch -c p5-8-containers` from `main`, or create a
  worktree on that branch.
- **Plan 7 may run at the same time.** This plan edits the `acp` branch of
  `cmd/marshal/main.go`'s `run` (`main.go:83` to `:116`); plan 7 edits
  `runFleet` and the launch calls below it. Whichever merges second re-runs
  `go test ./cmd/marshal/`. Both plans fill their own record entry in the spec
  and edit `AGENTS.md`; whichever merges second keeps both.
- **Task 5 lands the attach and the relay's allowlist in one commit.** The
  attach must never reach a container's viewers without the relay's half of
  the allowlist.
- Run each task's Verify and commit it as `p5-8-containers: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p5-8-suite`.

## Contracts

### The list (`web/bridge/contract/viewers.go`, Task 1)

```go
// ContainerViewerMethods are the methods a containerized agent's viewer may
// call (decision 1), sorted. The relay and the agent both refuse every other.
var ContainerViewerMethods = []string{ /* decision 1's 63 */ }
```

### The agent (`internal/acp`, Task 2)

```go
// server.go: Server gains
	// Allow, when set, refuses every method it answers false for, before the
	// handler is looked up (decision 2). Set it before Serve.
	Allow func(method string) bool

// containerViewerRefusal is decision 2's error for method.
func containerViewerRefusal(method string) error

// viewers.go: the accept loop RunWithViewers and the listener share
// (decision 5); the returned wait ends once every viewer has.
func (h *agentHost) acceptViewers(ctx context.Context, ln net.Listener) (wait func() error)

// runConfig gains
	viewers         net.Listener // a viewer socket beside --listen; nil for none
	restrictViewers bool         // the container allowlist on every viewer

// agentHost gains
	restrictViewers bool

// ListenAndServeWithViewers is ListenAndServe plus a viewer socket at
// viewers, every viewer on the allowlist (decisions 4 and 5).
func ListenAndServeWithViewers(ctx context.Context, network, addr, viewers string, stderr io.Writer) error

// ViewersEnv is the variable the bridge names a container's viewer socket in
// (decision 3).
const ViewersEnv = "MARSHAL_ACP_VIEWERS"
```

### The command (`cmd/marshal`, Task 3)

```go
var acpListenerWithViewers = acp.ListenAndServeWithViewers
```

### The bridge (`web/bridge`, Tasks 4 and 5)

```go
// child.go: Child gains
	// ViewerSocket is a container's host-side viewer socket, set where
	// newRuntime sets a container Transport (decision 6); "" for a host
	// process, whose socket the runtime names itself.
	ViewerSocket string

// container.go
const containerViewersName = "viewers.sock"

// viewerSocketPath is the host-side path of the container's viewer socket.
func (c *containerTransport) viewerSocketPath() string

// relay.go: relayEnd gains
	allow map[string]bool // a container's methods; nil forwards every frame

// clientFrame is what a restricted relay reads of every client frame: its id,
// when it has one, and its method, when it has one.
type clientFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

// restricted answers a frame a container's relay does not forward
// (decisions 2 and 7): answer is nil for a frame that is only dropped.
func (r *relay) restricted(frame []byte) (answer []byte, refused bool)

// containerViewerSet is ContainerViewerMethods as the relay looks them up.
func containerViewerSet() map[string]bool
```

## Task 1 — the list, pinned against the session screen

**Files:** `web/bridge/contract/viewers.go` (new),
`internal/acp/container_viewers_test.go` (new).

1. The list is decision 1's, sorted, with a comment per group saying why it is
   on the list.
2. Tests, in `internal/acp`, which can read both `acpclient.Methods` and the
   agent's registered handlers:
   - `TestTheContainerListIsTheSessionScreens`: every entry is the ACP method
     of a `Session` row of `acpclient.Methods`, or one of the four methods the
     client sends outside the table.
   - `TestTheContainerListHoldsNoHostCallOrWrite`: no entry is a `Host` row's
     method, starts with `config/` or `trust/`, or is one of decision 1's
     named refusals.
   - `TestEverySessionScreenMethodIsClassified`: every `Session` row's method
     is on the list or among the named refusals, so a new one fails here
     until it is placed.
   - `TestTheContainerListNamesRegisteredMethods`: every entry except
     `$/cancel_request` is registered on a viewer connection, as
     `TestACPClientMethodTableMatchesTheServer` reads the handlers.
   - `TestTheContainerListIsSorted`.
3. `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent`
   still passes with no regeneration, since the list is a variable.

**Verify:** `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./internal/acp/ -run 'ContainerList|SessionScreenMethod|MethodTable' -count=1`,
then `go test ./web/bridge/contract/... -count=1`.

**Mutation check:** add `config/set_value` to the list;
`TestTheContainerListHoldsNoHostCallOrWrite` must fail. Restore.

## Task 2 — the agent serves restricted viewers beside `--listen`

**Files:** `internal/acp/server.go`, `internal/acp/viewers.go`,
`internal/acp/listen.go`, `internal/acp/run.go`, their tests.

1. `Server.Allow` is checked in `dispatch` right after the `MarshalOnly` gate,
   so a refused method's handler is never looked up. `initialize` is on the
   list, so the marshal handshake still runs first.
2. `containerViewerRefusal` is an uncoded `*acpwire.Error` with decision 2's
   sentence, so a marshal client shows it as the error's own text, as
   `settingsFailure`'s errors are shown.
3. `serveViewer` sets `Allow` from the list when the host's viewers are
   restricted.
4. `acceptViewers` is `runWithViewersConfig`'s accept loop moved out:
   `runWithViewersConfig` calls it, and `listenAndServeWithConfig` calls it
   when its config carries a viewer listener. The shutdown order stays as
   `runWithViewersConfig` has it: the viewer socket closes first, then the
   viewers are waited for within the host's shutdown time, then the host
   closes.
5. `ListenAndServeWithViewers` binds both sockets with `listenUnix`, the viewer
   socket first so a viewer cannot arrive before the host can serve it, and
   runs `listenAndServeWithConfig` with `restrictViewers` on.
6. Tests:
   - `TestAllowRefusesBeforeTheHandler`: a server with `Allow` and a counting
     handler for `config/set_value`: the call is answered with the sentence,
     and the handler never runs. The same with a notification: no answer, and
     no handler.
   - `TestAllowLetsListedMethodsThrough`.
   - `TestARestrictedViewerIsRefusedAHostCall`: over
     `listenAndServeWithConfig` with a viewer listener and test sockets, a
     marshal client on the viewer socket calling `config/set_value` gets the
     sentence, and the same call on the control connection does not.
   - `TestARestrictedViewerReadsTheSession`: the control connection opens a
     session, and the viewer attaches to it with `session/status` and reads
     its messages.
   - `TestTheControlConnectionStillCutsIdle`:
     `TestListenAndServeCutsIdleConnections`' check with a viewer listener
     present, and a connected viewer kept past the idle time.
   - `TestViewersEndWithTheListener`: cancelling the context ends the viewer
     connections and closes the host.
   - The existing `TestListenAndServe*` tests and `viewers_test.go`'s
     (`TestAViewerCannotStartOrLoadASession` among them) pass unchanged.

**Verify:** `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./internal/acp/ -count=1 -timeout 15m`.

**Mutation check:** leave `Allow` unset in `serveViewer`;
`TestARestrictedViewerIsRefusedAHostCall` must fail. Restore.

## Task 3 — `marshal acp --listen` with viewers

**Files:** `cmd/marshal/main.go`, `cmd/marshal/main_test.go`.

1. The `acp` branch accepts `--viewers` with `--listen`, and reads
   `acp.ViewersEnv` when `--listen` is given without `--viewers` (decision 3).
   It removes the variable from the environment whenever it is set, whichever
   mode runs.
2. The viewer address must be a unix socket, as `--viewers` alone requires
   (`main.go:102`).
3. With a viewer socket, `--listen` runs `acpListenerWithViewers`; without one,
   `acpListener` as today. `--viewers` alone still runs `acpViewersRunner`, and
   ignores the variable.
4. Tests, in the pattern of `TestACPListenFlagRoutesToListener`:
   - `TestACPListenWithViewersRoutesToTheRestrictedListener`: both spellings
     of the flag reach it with both addresses.
   - `TestACPListenReadsTheViewersVariable`, and
     `TestTheViewersVariableIsRemoved`: after dispatch, the process's
     environment no longer has it.
   - `TestTheViewersFlagBeatsTheVariable`.
   - `TestACPViewersAloneIgnoresTheVariable`.
   - `TestACPListenWithATCPViewerIsRefused`.
   - No test pins the old "cannot be combined" refusal today
     (`main.go:96`); its branch is simply gone.

**Verify:** `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./cmd/marshal/ -count=1 -timeout 10m`.

## Task 4 — the container serves viewers

**Files:** `web/bridge/container.go`, `web/bridge/child.go`,
`web/bridge/fleet.go`, `web/bridge/container_test.go`,
`web/bridge/ownviewers_test.go`.

1. `buildRunArgs` adds decision 3's variable to the agent's run vector only,
   before the image. The setup container's vector is unchanged.
2. `start` removes the stale viewer socket beside the control socket's.
3. `newRuntime`'s container branch sets the `Child`'s `ViewerSocket` to the
   transport's `viewerSocketPath`, and `startRuntime` records a containerized
   child's `ViewerSocket` on the runtime (decision 6). The host process's
   branch is unchanged, and `releaseAgent` removes either kind with the
   runtime, as it already does.
4. Tests:
   - `TestTheAgentsRunVectorNamesTheViewerSocket`: the variable is in the
     vector, inside the container's socket directory.
   - `TestTheSetupVectorHasNoViewerSocket`.
   - `TestStartClearsAStaleViewerSocket`, with the injected runner
     `TestContainerUsesInjectedRunner` uses.
   - `TestAContainerChildHasNoViewerSocket` becomes
     `TestAContainerChildNamesItsViewerSocket`: `newRuntime`'s container
     child still has no arguments, and its `ViewerSocket` is `viewers.sock` in
     the agent's socket directory; `startRuntime`, over the fake transport the
     test already uses and a child carrying that path, records it; and no
     directory is made under the state directory's `v/`.
   - `TestContainerRunArgsRefuseHostEscapes` and
     `TestContainerEnvIsExplicitNotInherited` pass unchanged.

**Verify:** `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./web/bridge/ -run 'Container|ViewerSocket|RunVector' -count=1 -timeout 10m`.

## Task 5 — the attach reaches a container, through a restricted relay

**Files:** `web/bridge/attach.go`, `web/bridge/relay.go`, their tests,
`cmd/webbridge/README.md`. One commit (Execution).

1. `attachAgent`: a container's runtime with a viewer socket is dialled like an
   own child's, with a `relayEnd` carrying `containerViewerSet()`. The
   container sentence becomes decision 6's dial failure. An agent with no
   socket at all keeps "has no viewer socket yet".
2. `pumpClient` asks `restricted` before `refusal` when the end carries a list.
   `restricted` decodes each frame as a `clientFrame` and follows decisions 2
   and 7. A listed request then still meets `refusal`, so a viewer of a
   container can no more close the agent's session than any other viewer.
3. Tests, with `relay_test.go`'s `relayPair` and a fake agent socket that
   records every frame it reads:
   - `TestARestrictedRelayRefusesAnUnlistedRequest` (W9, the relay's end): a
     `config/set_value` request is answered with decision 2's sentence, under
     the request's id, and the fake agent reads nothing.
   - `TestARestrictedRelayDropsAnUnlistedNotification`.
   - `TestARestrictedRelayForwardsAListedRequest`, and its answer comes back.
   - `TestARestrictedRelayForwardsAnswersToTheAgent`: an answer to the agent's
     own `session/request_permission` reaches it.
   - `TestARestrictedRelayDropsABatch` and `TestARestrictedRelayDropsMalformedJSON`.
   - `TestARestrictedRelayStillRefusesClose`: a `session/close` naming the
     agent's session gets plan 4b's sentence.
   - `TestAnUnrestrictedRelayForwardsEverything`: a project attach's relay is
     unchanged.
   - `TestAContainersAgentAttachDialsItsViewerSocket`: a staged containerized
     runtime whose viewer socket is a test listener; the attach answers 101
     and the relay restricts.
   - `TestAContainersFailedDialSaysToRebuild`: with a version warning on the
     runtime, the 502 carries decision 6's clause.
   - `TestTheAgentAttachRefusals`: the container's 409 row becomes a staged
     container whose viewer socket is absent, answering decision 6's 502.
4. `cmd/webbridge/README.md`: a containerized agent's session opens too, with
   its viewers kept to the session screen's methods.

**Verify:** `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./web/bridge/ -run 'Relay|Attach' -count=1 -timeout 10m`,
then `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./web/... -count=1 -timeout 20m`.

**Mutation check:** give the container's `relayEnd` no list;
`TestAContainersAgentAttachDialsItsViewerSocket`'s restriction must fail.
Restore.

## Task 6 — W9 end to end, the docs, the record and full verification

**Files:** `test/daemon/container_viewers_test.go` (new),
`docs/daemon/p5-fleet-screen.md`, `AGENTS.md`.

1. `TestAContainerViewerDrivesTheSessionScreen` (W9, the agent's end, through
   the real agent): `acp.ListenAndServeWithViewers` in the test process on two
   short socket paths, with the harness's provider fixture.
   - A marshal client on the control socket opens a session in a project, as
     the bridge's connection would.
   - A second client on the viewer socket attaches to it. A session screen
     model built over it with `tui.WithAgentSession` renders its first frame
     with no refusal sentence in it.
   - A prompt through the viewer runs a turn, and its reply is in the
     transcript.
   - `SaveUserValue` through the viewer fails with decision 2's sentence, and
     the user config file is unchanged.
2. Fill the "Plan 8" record, as plan 6's is written.
3. `AGENTS.md`'s local daemon paragraph: every agent's session can be opened,
   and a containerized agent's viewers are kept to the session screen's
   methods.
4. **The manual run,** after plan 7 is on `main`, on a machine with docker or
   podman: a server-profile `webbridge` with containers, its agent image built
   from this branch (`build/Dockerfile`), and plan 7's `fleet.toml`. From
   `marshal fleet --server NAME`: spawn an agent, open its session, prompt it,
   answer an approval, try `/settings` and record the refusal, read the diff,
   and finish it. If no container runtime is available, record that and why.
5. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p5-8-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

**Verify:** `TMPDIR=/private/tmp/p5-8-suite CGO_ENABLED=1 go test ./test/daemon/ -run ContainerViewer -count=1 -timeout 10m`,
then the full verification above.

## Done when

- A containerized agent's session opens from the fleet screen, on a
  server-profile bridge, through its container's viewer socket.
- A container's viewer drives turns, controls, answers, reads, notes and
  browse calls, and every other method is refused with decision 2's sentence
  by the relay and again by the agent.
- An image whose marshal predates this plan still starts, with no viewer
  socket.
- W9 passes, every other test named above passes, each mutation check fails
  as described, and CI's suite command passes.
