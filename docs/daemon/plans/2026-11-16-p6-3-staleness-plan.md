# P6 plan 3: a project child knows when it is stale — implementation plan

Date: 2026-11-16. Status: ready for review.

Base: `origin/main` at `87c41ad9` with P5 plan 8's branch (`d57f178f`, PR
#100) merged in. Every anchor below was checked there. Plan 8 changes
`internal/acp/host.go`, `viewers.go` and `run.go`, all of which this plan
also edits; if plan 8 merges with changes, re-check this plan's anchors in
those three files.

- **The baseline** is recorded in the spec's "Implementation record".
- **The spec is already written,** uncommitted in the main checkout (see
  Execution).
- **Plans 1, 2 and 6 run beside this one.** Plan 6 also edits
  `internal/acp/session.go`'s service builder (`session.go:883`), to bind the
  watch-resume hook; this plan sets two dependencies there. Whichever merges
  second keeps both.

Approved design: [p6-daemon-lifecycle.md](../p6-daemon-lifecycle.md):
decision 1, decision 6's first bullet; B3; Migration order item 3; Tests L4.
Previous plans: [P4 plan 9, pins](2026-11-12-p4-9-pins-plan.md), whose
`project/hold` this plan's two requests sit beside.
Sibling plans: [plan 1](2026-11-16-p6-1-versions-plan.md),
[plan 2](2026-11-16-p6-2-reconnect-plan.md),
[plan 6](2026-11-16-p6-6-wake-warnings-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no
implementation code. The Contracts section holds declarations only. Every other
step states its decision in prose. Executing the plan also tests how well
Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

## Goal

- **A project child knows what it started from:** its executable, the user
  config and the project config.
- **It knows its own config writes apart from anyone else's,** so a settings
  change made in one of its sessions never makes it stale.
- **The bridge can ask it** whether it is stale and what keeps it busy
  (`project/state`), and can tell every session something (`project/note`).
  Plan 4 does both.
- **Its sessions show a pending restart** on the status line and in the
  sessions sheet.

## Non-goals

- **Restarting anything.** Plan 4.
- **A fleet agent's own child, `--listen` and embedded mode.** Only the
  bridge's project children keep a record (B3).
- **Watching files.** The check runs when the bridge asks.

## Assumptions

Each was checked at the base.

- **A project child is `marshal acp --viewers`,** served by `RunWithViewers`
  (`internal/acp/viewers.go:89`), whose run config carries the pin's hold
  (`viewers.go:114`). The bridge starts it with the project's root as its
  working directory (`web/bridge/projectchild.go:103`).
- **`project/hold` is served on the bridge's connection only,** and refused on
  a viewer's (`internal/acp/host.go:419`, `:429`), and only when the run
  config has a hold (`host.go:136`).
- **The host builds each session's service** in the session manager, from the
  runtime's `ServiceDeps` (`internal/acp/session.go:883`), and its config
  (`SessionManagerConfig`, `session.go:58`) is filled in `newAgentHost`
  (`host.go:121`).
- **The service writes the user config or the project config in eight
  places:**
  - `SaveUserValue` (`internal/acp/service/config.go:87`);
  - `SaveUserProviders` (`config.go:150`);
  - `SaveUserPresets` (`config.go:164`);
  - `SaveProviderKey` (`config.go:183`);
  - `SaveProjectConfig` (`config.go:375`);
  - `SaveVerifyCommands` (`config.go:391`);
  - `SetSkillAutoload` (`internal/acp/service/skills.go:279`);
  - an approval's "always" rule (`internal/acp/service/answers.go:91`).

  `SaveProvenance`, `SaveModels` and the plugin and skill installs write
  elsewhere: the data directory, the model cache and the plugin and skill
  directories.
- **The connection's config manager** writes the user config for the `config/`
  methods (`internal/acp/config.go:305`, `:334`, `:382`, `:448`, `:482`), and is
  built per connection (`host.go:557`).
- **The status** is the service's `Status`, sent by the status forwarder only
  when its encoding changes (`internal/acp/status.go:248`). It reports a run,
  subagents, running jobs, a swarm and an SDD run (`internal/acpwire/status.go:33`,
  `:45` to `:48`; `SwarmProgress.Active` and `SDDProgress.Active` in
  `internal/acpwire/runs.go:45`, `:71`). A session's watches are its runtime's
  watch manager's (`internal/watch/watch.go:709`), watching while their state
  is `watching` (`watch.go:65`).
- **The manager lists its live runtimes** (`SessionManager.All`,
  `session.go:902`) and serves each one's service (`Service`, `session.go:796`).
- **The config paths** are `config.UserConfigPath(home)` and
  `config.ProjectConfigPath(dir)` (`internal/app/config/locations.go:23`,
  `:28`).
- **The status line's right cluster** is built in `statusClusters`
  (`internal/app/tui/status.go:119`), with P5 plan 6's `needs you N` composed
  outside the hint shedding. The sessions sheet's session section renders in
  `SessionSection.Render` (`internal/app/tui/sessionsheet/section_session.go:80`).

## Decisions this plan makes beyond the spec

1. **The record is its own type in `internal/acp`,** built by `RunWithViewers`
   and handed to the host through the run config. With no record, nothing in
   this plan changes how a connection behaves.
2. **A record that cannot read its executable** keeps no executable entry, and
   logs why once. Its config entries still work. An absent config file is
   recorded as absent, so creating one later is a change.
3. **`ConfigWritten` is a dependency, not a hook.** `service.Deps` and the
   config manager each carry the function, set by the host from its record, so
   two hosts in one test process never see each other's writes.
4. **The record remembers the last answer it gave,** reasons and busy lines
   both, and the status reads that. A status read never hashes a file.
5. **Busy is computed from each session's own status and watches,** in the
   order the sessions were created, so the first busy line is stable.
6. **The two new requests are refused on a viewer's connection,** as
   `project/hold` is, with the same sentence.
7. **`project/note` adds a plain note through each session's service,** as
   `AddNote` adds one, so a screen attached to the session sees it at once.

## Execution

- In the main checkout, if `git status` shows the spec or this batch's plans
  uncommitted, commit them to `main` first, as explicit paths, with the message
  `docs/daemon: record P6 and its plans 1, 2, 3 and 6`. The paths are listed
  in [plan 1](2026-11-16-p6-1-versions-plan.md)'s Execution.
- Then branch: `git switch -c p6-3-staleness` from `main`, or create a
  worktree on that branch.
- Run each task's Verify and commit it as `p6-3-staleness: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- Keep `TMPDIR` short for every test run: `TMPDIR=/private/tmp/p6-3-suite`.

## Contracts

### The wire (`internal/acpwire`, Task 1)

```go
// status.go: SessionStatus gains
	Restart *PendingRestart `json:"restart,omitempty"` // nil while the child is current

// PendingRestart is a stale child's restart as its sessions show it (B3).
type PendingRestart struct {
	Reason     string `json:"reason"`
	WaitingFor string `json:"waitingFor"`
}

// project.go (new)
const (
	MethodProjectState = "project/state"
	MethodProjectNote  = "project/note"
)

// ProjectState is project/state's answer (B3).
type ProjectState struct {
	Stale   []string `json:"stale,omitempty"`
	Busy    []string `json:"busy,omitempty"`
	Version string   `json:"version"`
}

// ProjectNoteParams is project/note's.
type ProjectNoteParams struct {
	Text string `json:"text"`
}
```

### The record (`internal/acp/record.go`, Task 2)

```go
// startRecord is what a project child started from (decision 1).
type startRecord struct{ /* the executor's */ }

// newStartRecord records executable, userConfig and projectConfig now.
func newStartRecord(executable, userConfig, projectConfig string, log *slog.Logger) *startRecord

// wrote re-records path when it is one the record keeps: the child's own
// write (decision 3).
func (r *startRecord) wrote(path string)

// stale compares the record with the files as they are now.
func (r *startRecord) stale() []string

// remember keeps an answer for the status to read (decision 4).
func (r *startRecord) remember(stale, busy []string)

// pending is the remembered answer as a session shows it; nil when current.
func (r *startRecord) pending() *acpwire.PendingRestart
```

### The host (`internal/acp`, Task 3)

```go
// run.go: runConfig gains
	record *startRecord // a project child's; nil elsewhere

// host.go: agentHost gains
	record *startRecord

// projectState answers project/state; projectNote serves project/note.
func (h *agentHost) projectState(ctx context.Context, params json.RawMessage) (any, error)
func (h *agentHost) projectNote(ctx context.Context, params json.RawMessage) (any, error)

// busy lists what holds the child up (decision 5).
func (h *agentHost) busy() []string

// session.go: SessionManagerConfig gains
	ConfigWritten  func(path string)
	PendingRestart func() *acpwire.PendingRestart

// config.go: ConfigManager gains
	wrote func(path string) // nil when the host keeps no record
```

### The service (`internal/acp/service`, Task 3)

```go
// Deps gains:
	// ConfigWritten is told the path of every user or project config write
	// this session makes, after it succeeds (decision 3). Nil tells no one.
	ConfigWritten func(path string)
	// PendingRestart is the child's pending restart; nil, or a nil answer,
	// means none.
	PendingRestart func() *acpwire.PendingRestart
```

## Task 1 — the wire

**Files:** `internal/acpwire/status.go`, `internal/acpwire/project.go` (new),
their tests.

1. The field and the types, as Contracts declares them.
2. Tests: `TestAStatusWithoutARestartOmitsIt`, `TestAPendingRestartRoundTrips`
   and `TestProjectStateRoundTrips`.

**Verify:** `TMPDIR=/private/tmp/p6-3-suite go test ./internal/acpwire/ -count=1`.

## Task 2 — the record

**Files:** `internal/acp/record.go`, `internal/acp/record_test.go` (both new).

1. The executable's entry is its path and that path's device, inode, size and
   modification time, from the system's stat. Each config entry is the
   SHA-256 of the file's content, or absent.
2. `stale` gives B3's reasons in a fixed order: the build, then
   `config.toml`, then `.marshal/config.toml`. It compares against the record
   every time, so a file put back as it was gives no reason.
3. `wrote` re-records only a path the record keeps, compared after cleaning
   both paths.
4. `pending` gives the first remembered reason and the first busy line, or
   "a quiet moment" when nothing is busy.
5. Tests, over files in a temporary directory standing in for the executable
   and both configs:
   - `TestARecordIsCurrentAtStart`.
   - `TestAReplacedExecutableIsStale`: removing the file and writing a new one
     at its path gives "a new marshal build".
   - `TestAnEditedConfigIsStale`, for each config file, and
     `TestACreatedConfigIsStale` for one that was absent.
   - `TestAFilePutBackIsCurrent`.
   - `TestAnOwnWriteIsNotStale`: a write followed by `wrote` gives no reason;
     the same write without it does.
   - `TestWroteIgnoresOtherPaths`.
   - `TestAnUnreadableExecutableStillWatchesConfig`.
   - `TestPendingNamesWhatItWaitsFor`, with and without busy lines.

**Verify:** `TMPDIR=/private/tmp/p6-3-suite CGO_ENABLED=1 go test ./internal/acp/ -run 'Record|Stale|OwnWrite|Wrote|Pending' -count=1`.

**Mutation check:** make `wrote` do nothing; `TestAnOwnWriteIsNotStale` must
fail. Restore.

## Task 3 — the host's requests, its own writes, and the status

**Files:** `internal/acp/viewers.go`, `internal/acp/run.go`,
`internal/acp/host.go`, `internal/acp/session.go`, `internal/acp/config.go`,
`internal/acp/service/service.go`, `internal/acp/service/config.go`,
`internal/acp/service/skills.go`, `internal/acp/service/answers.go`, their
tests. `Status` is built in `internal/acp/service/service.go:371`.

1. `RunWithViewers` builds the record from `os.Executable`,
   `config.UserConfigPath(home)` and `config.ProjectConfigPath` of its
   working directory, and passes it in the run config.
2. `newAgentHost` keeps it, and sets the session manager's `ConfigWritten` to
   `record.wrote` and its `PendingRestart` to `record.pending`. The service
   builder copies both into each session's `Deps`.
3. Each of the eight service writes in Assumptions calls `ConfigWritten` with
   the path it wrote, after the write succeeds. The connection's config
   manager does the same for its five, through `wrote`, which the host sets
   when it builds the manager.
4. The service's `Status` sets `Restart` from `PendingRestart`.
5. `registerHandlers` serves `project/state` and `project/note` on the bridge's
   connection when the host has a record, and refuses both on a viewer's
   connection (decision 6).
   - `projectState` runs `stale`, computes `busy` (decision 5) from each live
     session's status and watches, remembers both, and answers them with the
     child's version.
   - `projectNote` adds the text as a plain note to every live session, and
     answers the number of sessions it reached.
6. Tests:
   - `TestEveryConfigWriteReportsItself`, in `internal/acp/service`: a table
     over the eight writes, each run against a temporary home, recording the
     paths `ConfigWritten` is told; each tells the path it wrote, and a write
     that fails tells nothing.
   - `TestTheConfigManagerReportsItsWrites`, over the five `config/` methods.
   - `TestASettingsWriteLeavesItsChildCurrent` (L4, the child's half): over
     `runWithViewersConfig` with a record on temporary files, a marshal
     viewer's `SaveUserValue` leaves `project/state` with no stale reason; the
     same file changed directly afterwards gives "config.toml changed".
   - `TestProjectStateNamesWhatIsBusy`: a session in a run, held by a scripted
     provider's gate, gives "a run in <session>"; a session with a watching
     watch gives "watch <name> in <session>".
   - `TestProjectStateIsTheBridgesOnly`: a viewer's call is refused with the
     sentence `project/hold`'s refusal uses.
   - `TestOnlyAProjectChildServesProjectState`: a host with no record answers
     method-not-found.
   - `TestProjectNoteReachesEverySession`: two sessions, one note each.
   - `TestTheStatusCarriesAPendingRestart`: after a stale `project/state`, a
     viewer's status has `restart` with the reason and what it waits for, and
     the status forwarder sends it without being asked.

**Verify:** `TMPDIR=/private/tmp/p6-3-suite CGO_ENABLED=1 go test ./internal/acp/... -count=1 -timeout 15m -skip '^Test(NoticeCallBudget|ControlCallBudget|AnswerCallBudget|ResponseBarrierBudget|SettingsCallBudget|NoteCallBudget|BrowserCallBudget|EndCallBudget|StackBusyFlipBudget|StackPatchApplyBudget|StackRoundTripBudget|AKeyPressOverThePipeBudget)$'`.

**Mutation check:** drop the `ConfigWritten` call from `SaveUserValue`;
`TestEveryConfigWriteReportsItself` and
`TestASettingsWriteLeavesItsChildCurrent` must fail. Restore.

## Task 4 — the session screen shows a pending restart

**Files:** `internal/app/tui/status.go`,
`internal/app/tui/sessionsheet/section_session.go`, their tests and goldens.

1. While the session's status has `restart`, the status line's right cluster
   reads `restart pending` in the warning colour, composed beside P5 plan 6's
   needs-you count, outside the hint shedding, so it survives a narrow
   terminal as the count does.
2. The sessions sheet's session section adds a row: "Restart: <reason>,
   waiting for <what>".
3. Tests:
   - `TestAPendingRestartShowsOnTheStatusLine` and
     `TestNoPendingRestartShowsNothing`.
   - `TestThePendingRestartSurvivesANarrowTerminal`, at 80 columns with the
     count present.
   - `TestTheSheetSaysWhatTheRestartWaitsFor`.
   - A status-line golden at 100 columns with the segment, beside P5 plan 6's.

**Verify:** `TMPDIR=/private/tmp/p6-3-suite CGO_ENABLED=1 go test ./internal/app/tui/... -count=1 -timeout 15m`.

## Task 5 — the docs, the record and full verification

**Files:** `docs/daemon/p6-daemon-lifecycle.md`.

1. Fill the "Plan 3" record.
2. **No manual run.** Nothing asks `project/state` until plan 4's sweep, and
   the request is served only on the bridge's own connection, so there is
   nothing a person can see yet. Record that, and that Task 3's tests drive
   the request over a real host.
3. Full verification: `gofmt -l internal cmd test web` prints nothing; `go vet
   ./...` is clean; CI's suite command with `TMPDIR=/private/tmp/p6-3-suite`
   passes; `CGO_ENABLED=1 go build -o marshal ./cmd/marshal`; the usability
   suite passes.

## Done when

- A project child answers `project/state` with its stale reasons, what keeps
  it busy, and its version, and serves `project/note`.
- A config write the child makes itself never makes it stale; anyone else's
  does.
- Its sessions show `restart pending` and what it waits for once a stale
  answer has been given.
- Every test named above passes, each mutation check fails as described, and
  CI's suite command passes.
