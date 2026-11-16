# P6 plan 6: a wake with no screen, and P4's two warnings — implementation plan

Date: 2026-11-16. Status: ready for review.

Base: `origin/main` at `87c41ad9` with P5 plan 8's branch (`d57f178f`, PR
#100) merged in. Every anchor below was checked there. Plan 8 changes
`internal/acp/host.go`, `web/bridge/attach.go` and `web/bridge/fleet.go`,
which this plan also edits; if plan 8 merges with changes, re-check this
plan's anchors there.

- **The baseline** is recorded in the spec's "Implementation record".
- **The spec is already written,** uncommitted in the main checkout (see
  Execution).
- **Tasks 1 to 4 run beside plans 1, 2 and 3.** Task 5 adds a note through
  plan 2's `addNoteOnce`, so it waits for plan 2 on `main`.
- **Plan 3 also edits the service builder** in `internal/acp/session.go:883`.
  Whichever merges second keeps both changes.

Approved design: [p6-daemon-lifecycle.md](../p6-daemon-lifecycle.md):
decision 9 and decision 6's last bullet; B7, B8; Migration order item 6;
Tests L7.
Previous plans: [P5 plan 4c, a viewer's turn](2026-11-15-p5-4c-viewer-turns-plan.md),
whose followed runs a detached wake's run shows as;
[P4 plan 6, the environment](2026-11-11-p4-6-environment-plan.md), whose
warning this plan brings to the screen.
Sibling plans: [plan 1](2026-11-16-p6-1-versions-plan.md),
[plan 2](2026-11-16-p6-2-reconnect-plan.md),
[plan 3](2026-11-16-p6-3-staleness-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A watch that fires while no screen is attached acts on it** (P4 H8): its
  session starts the resume turn itself, and the next screen to attach sees
  it running.
- **The fleet shows an agent's version warning** in its Info tab (P4 H13).
- **The session screen shows its project's environment warning** as a note,
  once (P4 H9).

## Non-goals

- **A wake on a fleet agent's own child, or in embedded mode.** Embedded mode
  always has its screen; an agent's own child is the bridge's, which binds the
  hook itself.
- **Repeating a wake that was dropped** because its session was busy. A busy
  session's running turn takes the report, as today.

## Assumptions

Each was checked at the base.

- **The watch-resume hook** is the runtime's `WatchResumeHook`, set and
  replaced with `Set` (`internal/app/runtime.go:1127`). A non-marshal
  connection binds it to its own turn manager on its first turn lookup
  (`internal/acp/host.go:272`, `bindWatchResume` at `:323`); a marshal
  connection binds nothing, because its screen resumes the watch itself
  (`TestAMarshalConnectionBindsNoWatchResume`,
  `internal/acp/controls_test.go:741`).
- **The bridge's connection never looks up a TUI's session for a turn,** so a
  TUI session's hook is bound to nothing while no screen is attached.
- **The agent-side resume** is `ResumeIfIdle` (`internal/acp/turn.go:1348`): it
  checks the watch-resume config gate and a pending SDD gate, and skips a
  session with a turn. The screen's resume is a `Prompt` whose params name
  the watch (`internal/app/tui/model.go:4195`), which the service serves
  (`internal/acp/service/turns.go:71`).
- **A marshal connection subscribes to a session's status** on its first
  `session/status` for it (`ensureForwarder`, `internal/acp/status.go:85`,
  `:195`). The status manager is per connection.
- **A run the session started itself** shows on every viewer's status, and a
  screen that did not start it follows it (`syncFollowedRun`,
  `internal/app/tui/model.go:5291`), as P5 plan 4c's followed runs are.
- **An agent's version warning** is the runtime's `versionWarning`
  (`web/bridge/fleet.go:103`), set for an agent on a project child
  (`fleet.go:934`) and on its own (`fleet.go:1039`). The agent's version
  string itself is not kept. `contract.AgentStatus` (`web/bridge/contract/agents.go:41`)
  has no field for it, and `Snapshot` builds it at `fleet.go:2025`.
- **The fleet's Info tab** is `infoLines` (`internal/app/tui/fleet/view.go:549`).
- **A project child's environment warning** is `projectChild.envWarning`
  (`web/bridge/projectchild.go:45`, set at `:222`), reported only on
  `GET /api/daemon` (`web/bridge/daemoninfo.go:94`).
- **Both attaches write their `101` answer by hand:** the project attach at
  `web/bridge/attach.go:180`, the agent attach at `attach.go:333`. The daemon
  client reads the answer's headers (`internal/daemon/client.go:421`) into an
  `Attachment` (`client.go:248`).
- **The generated TypeScript** is regenerated with
  `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -update`.

## Decisions this plan makes beyond the spec

1. **The host counts marshal viewers per session** in one map of its own. A
   connection's status manager adds one the first time it subscribes to a
   session, and the connection's cleanup takes each of its additions back.
2. **The detached binding is made when the session's service is built**, on a
   host that serves viewers: `RunWithViewers` sets a flag in the run config.
   The hook's function checks, at each fire, the config gate, a pending SDD
   gate, a run in progress, and the session's marshal viewers; any of them
   makes it do nothing.
3. **Its resume is the screen's own call:** the service's `Prompt` with the
   watch's name and whether it repeats, on a goroutine, so the watch's
   goroutine never waits.
4. **The agent's version is kept on the runtime** beside `versionWarning`,
   from the same handshake, so the warning can name it.
5. **`versionWarning` on the wire is a sentence,** "agent runs marshal
   <theirs>; the daemon is <ours>", empty when there is no warning. The fleet
   screen shows it as it is.
6. **The environment header carries one line:** the warning's first line,
   with control characters removed and cut at 300 bytes, because a header
   cannot hold a newline.

## Execution

- In the main checkout, if `git status` shows the spec or this batch's plans
  uncommitted, commit them to `main` first, as explicit paths, with the message
  `docs/daemon: record P6 and its plans 1, 2, 3 and 6`. The paths are listed
  in [plan 1](2026-11-16-p6-1-versions-plan.md)'s Execution.
- Then branch: `git switch -c p6-6-wake-warnings` from `main`, or create a
  worktree on that branch.
- Run Tasks 1 to 4 first. Before Task 5, merge `main` once plan 2 is on it,
  and check `addNoteOnce` against plan 2's code.
- Run each task's Verify and commit it as `p6-6-wake-warnings: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p6-6-suite`.

## Contracts

### The agent (`internal/acp`, Task 1)

```go
// run.go: runConfig gains
	resumeDetached bool // bind each session's watch resume to its service (decision 2)

// host.go: agentHost gains
	viewers *marshalViewers

// marshalViewers counts each session's marshal viewers (decision 1).
type marshalViewers struct{ /* the executor's */ }

// add counts one more viewer of sessionID and returns its release.
func (v *marshalViewers) add(sessionID string) (release func())

// any reports whether sessionID has a marshal viewer.
func (v *marshalViewers) any(sessionID string) bool

// status.go: StatusManagerConfig gains
	Viewers *marshalViewers // nil counts nothing

// bindDetachedResume binds rt's hook to a resume through svc (decisions 2, 3).
func bindDetachedResume(rt *app.Runtime, svc *service.Session, sessionID string, viewers *marshalViewers)
```

### The bridge and the contract (Tasks 2 and 3)

```go
// web/bridge/contract/agents.go: AgentStatus gains
	VersionWarning string `json:"versionWarning,omitempty"` // decision 5

// web/bridge/fleet.go: agentRuntime gains
	agentVersion string // the version the agent's handshake reported (decision 4)

// web/bridge/attach.go
const envWarningHeader = "Marshal-Env-Warning"

// headerLine is decision 6's one line.
func headerLine(s string) string

// internal/daemon/client.go: Attachment gains
	EnvWarning string // the project child's environment warning; "" when none
```

## Task 1 — a wake with no screen

**Files:** `internal/acp/host.go`, `internal/acp/status.go`,
`internal/acp/session.go`, `internal/acp/run.go`, `internal/acp/viewers.go`,
`internal/acp/watch_detached.go` (new), their tests.

1. `marshalViewers` follows decision 1. The status manager's
   `ensureForwarder` adds the subscribing session, once per connection, and
   the connection's cleanup releases each addition.
2. `RunWithViewers` sets `resumeDetached`. The service builder calls
   `bindDetachedResume` for each session it builds when the host has it set.
3. `bindDetachedResume` sets the hook to decision 2's checks and decision 3's
   resume. A non-marshal connection's later binding replaces it, as
   `bindWatchResume` already does.
4. Tests, in the pattern of `TestAConnectionWithoutTheCapabilityBindsWatchResume`:
   - `TestAWakeWithNoScreenStartsARun` (L7, the agent's half): a session on a
     viewers host, with resume on and no marshal viewer; firing the hook with a
     resumable report starts a run whose prompt names the watch, and the
     session's status shows it.
   - `TestAWakeWithAScreenIsTheScreens`: with a marshal viewer subscribed, the
     fire starts nothing.
   - `TestTheViewerCountFollowsTheConnection`: a marshal connection's
     `session/status` counts one; closing the connection counts none.
   - `TestAWakeDuringARunStartsNothing`, and `TestAWakeWithResumeOffStartsNothing`.
   - `TestABridgeBindingReplacesTheDetachedOne`: a non-marshal connection's
     turn lookup rebinds the hook to its own turn manager.
   - `TestAHostWithoutViewersBindsNothing`: a stdio host leaves the hook as
     today.

**Verify:** `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./internal/acp/ -run 'Wake|ViewerCount|Binding|BindsWatchResume' -count=1 -timeout 10m`.

**Mutation check:** let the hook resume even with a marshal viewer;
`TestAWakeWithAScreenIsTheScreens` must fail. Restore.

## Task 2 — an agent's version warning on the wire

**Files:** `web/bridge/contract/agents.go`, `web/bridge/fleet.go`,
`web/bridge/fleet_test.go`, `web/ui/src/lib/contract.ts` (regenerated).

1. Both places that set `versionWarning` keep the agent's version on the
   runtime (decision 4).
2. `Snapshot` fills `VersionWarning` with decision 5's sentence when the
   runtime has the warning.
3. Regenerate `contract.ts` with the command in Assumptions.
4. Tests:
   - `TestAnAgentsVersionWarningIsOnItsStatus`, for an agent on a project
     child and for one on its own child, each with a skewed version.
   - `TestNoWarningLeavesTheFieldEmpty`.
   - `TestTypeScriptContractIsCurrent` passes.

**Verify:** `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./web/bridge/ ./web/bridge/contract/... -run 'VersionWarning|NoWarning|TypeScript' -count=1 -timeout 10m`.

## Task 3 — the environment warning on both attaches

**Files:** `web/bridge/attach.go`, `web/bridge/attach_test.go`,
`internal/daemon/client.go`, `internal/daemon/client_test.go`.

1. Both `101` answers add `Marshal-Env-Warning` with `headerLine` of the
   project child's warning, when it has one. An agent on a child of its own
   has no project child, and gets none.
2. The daemon client reads the header into `Attachment.EnvWarning`.
3. Tests:
   - `TestTheProjectAttachCarriesTheEnvWarning` and
     `TestAnAgentAttachOnTheProjectChildCarriesIt`, over a project child whose
     environment resolution warned (P4 plan 6's test seam).
   - `TestNoWarningSendsNoHeader`.
   - `TestHeaderLineIsOneLine`: a warning with a newline, a tab and 400 bytes
     gives its first line, no control characters, at most 300 bytes.
   - `TestTheAttachmentReadsTheEnvWarning`, in `internal/daemon`.

**Verify:** `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./web/bridge/ -run 'Attach|HeaderLine' -count=1 -timeout 10m`,
then `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./internal/daemon/ -count=1`.

## Task 4 — the fleet's Info tab shows the version warning

**Files:** `internal/app/tui/fleet/view.go`, its tests and goldens.

1. `infoLines` adds a `version` line in the warning colour when the agent's
   status carries a warning, after `branch`.
2. Tests: `TestTheInfoTabShowsAVersionWarning` and
   `TestTheInfoTabHasNoVersionLineWithoutOne`; the Info tab's goldens without
   a warning are unchanged.

**Verify:** `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./internal/app/tui/fleet/ -count=1 -timeout 10m`.

## Task 5 — the session screen's environment note (after plan 2)

**Files:** `internal/launch/daemon.go`, `internal/launch/daemon_test.go`.

1. After a dial whose attachment carries an environment warning, the loop
   adds it as a note through plan 2's `addNoteOnce`: "This project's
   environment could not be resolved fully: <warning>".
2. Tests:
   - `TestTheEnvWarningIsANote`: the fake daemon's attachment carries a
     warning, and the session gets the note.
   - `TestTheEnvWarningIsAddedOnce`: a second attach adds no second note.
   - `TestNoEnvWarningAddsNoNote`.

**Verify:** `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./internal/launch/ -count=1 -timeout 15m`.

## Task 6 — a real daemon, the docs, the record and full verification

**Files:** `test/daemon/detached_wake_test.go` (new),
`docs/daemon/p6-daemon-lifecycle.md`, `AGENTS.md`.

1. `TestADetachedSessionsWakeRuns` (L7): against the real bridge harness, a
   session with a repeating watch on a command the test controls, and watch
   resume on in its config. The screen detaches; the test makes the watch
   fire; the session's turn runs on the provider fixture. A client that
   attaches afterwards finds the run in the transcript or still running.
2. Fill the "Plan 6" record.
3. `AGENTS.md`'s local daemon paragraph: a watch's wake resumes even with no
   screen attached.
4. **The manual run.** With a daemon running this branch's build: start a
   watch with resume on, detach, make it fire, and reattach; record what the
   transcript shows. Open the fleet's Info tab on an agent of another build if
   one is at hand, and record the version line.
5. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p6-6-suite`
   passes; `cd web/ui && npm test`; `CGO_ENABLED=1 go build -o marshal
   ./cmd/marshal`; the usability suite passes.

**Verify:** `TMPDIR=/private/tmp/p6-6-suite CGO_ENABLED=1 go test ./test/daemon/ -run DetachedSessionsWake -count=1 -timeout 15m`,
then the full verification above.

## Done when

- A watch that fires while no screen is attached starts its session's resume
  turn, and the next screen sees it.
- The fleet's Info tab names an agent's version warning.
- The session screen gets its project's environment warning as one note.
- L7 passes, every other test named above passes, the mutation check fails as
  described, and CI's suite command passes.
