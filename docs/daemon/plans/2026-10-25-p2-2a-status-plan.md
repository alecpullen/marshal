# P2 plan 2a: status — implementation plan

Date: 2026-10-25. Status: ready for review.
Base: `main` at `4a81ef11` (plan 1 merged) or later.
- Every anchor below was checked at `4a81ef11`. Line numbers are there to find the code fast; the function names are the anchors.
- The read sites were found with a type-checked scan of the TUI tree: every selection of a `session.State` method or field, with its receiver. Each task lists all of its sites.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C2 (reads), C3 (wire rules), C4 (the adapter), C5 (status), Migration order item 2a, and tests T1b, T2, T4 and T5.
Previous plan: [plan 1, foundation](2026-10-25-p2-1-foundation-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the TUI reads its session's identity, routes, access, activity, workspace roots and browser state through `Session.Status()`, and its session ID through `Session.ID()`. Nothing a user sees changes.
- `acpwire.SessionStatus` and its parts exist, with JSON names fixed.
- `Session.Status()` is on the interface, and the in-process adapter implements it.
- 89 reads in 47 functions move off `m.state`.
- A guard test (T1b) keeps them moved.

**Non-goals:**
- **No events.** The pumps and `Changed` are plan 2b. The TUI keeps re-reading on its tick and its events, as today.
- **No runs or queues.** The steering queue, jobs, watches, subagent views, swarm and SDD progress and the SDD gate are plan 2b.
- **No git info.** `gitinfo.Read` and `HeadSHA` stay in the TUI until plan 2b. This plan moves only the reads of the root they are given.
- **No sheet.** Plan 2c covers it, including the session's start time and the `d.State` reads in `sessionsheet`.
- **No sandbox and no notice** (spec C5). Sandbox info moves with plans 3 and 4, and the notice with plan 6.
- **No writes.** Every setter the TUI calls stays on `m.state`.
- **No reads of other states.** A drilled-in child's state (`transcriptState`, `child`, `st`, `src` and `v.Child`) and a function's own `state` parameter (`writePostmortemFor`, `fullToolHead`) keep their reads, because plans 2b, 2c and 3 move those.
- **No allowlist change expected.** No file loses its last agent-side import here. If T1 reports a stale entry, delete it: that is progress.
- **No changelog entry.** Nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-2a-suite`. Create the directory first: `mkdir -p /private/tmp/p2-2a-suite`.
- **Pre-existing failures** at `4a81ef11` are listed in Task 8, from a full run with `-timeout 20m`. A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`. The one exception, `TestRuntimeAccessSerializesWithReloadAndQuiesce`, is named there. This plan touches no file in `internal/app` outside the TUI.

**Decisions this plan makes beyond the spec** (decision 1 is recorded in the spec's Migration order and revision record):
1. **Plan 2 is three plans.** The handlers the broker pumps feed do more than re-read: the watch handler starts resume turns, the steering handler keeps Send-now receipts, and the workspace handler re-reads git. Replacing the pumps is plan 2b, and the plain reads come first.
2. **`Status()` is a fresh read, taken once per function.** The adapter builds the value from the state on every call, so a read sees every write before it, as the direct reads do today. In each function or function literal, take `st := m.sess.Status()` once, in the smallest block that encloses all of that function's moved reads, before the first of them. If an earlier task of this plan already added an `st` to the function, reuse it. If the function already has a local named `st`, name the new one `status`.
   - In `update` (`model.go:2361`), the block is each `case`.
   - In `commands_dispatch.go`'s `init` (line 252), it is each command's closure.
   - A function with one moved read may write `m.sess.Status().X` inline.
   - `startNewSession` (`commands_dispatch.go:188`) reads only after it has replaced `m.sess`.

   Every field is an in-memory read. The one with a cost is the branch list, a scan of the session's messages. The status line already does that scan once per frame, and the read-once rule keeps it to about four per frame.
3. **The wire status carries only what the TUI reads.** It follows C3:
   - durations are milliseconds, so the quota's reset times and windows convert from seconds and minutes;
   - times are Unix milliseconds, through the new `acpwire.Millis` and `acpwire.Time`, which map the zero time to 0 and back;
   - the route's role is a plain string, so no `routing` type enters `acpwire`, and T3 still waits.
4. **Every read of a moved field moves.** That includes `WorkingDir`'s reads in config, plan-file and history code that plans 5, 7 and 8 later replace. A single rule is one a guard can check.
5. **The session ID comes from `Session.ID()`.** It never enters `Status`.
6. **Two helpers change their parameter type.** `quotaSegment` and the agents roster's `SetQuota` and `quotaLine` take `*acpwire.Quota`, and `browserStatusText` takes `acpwire.Browser`. `livestrip.go`'s `browserStripText` has no production caller, so it is left alone.
7. **T1b is a list, not an allowlist.** It names the `State` methods and fields that have moved, and fails on any `<x>.state.<Name>` selector for them. No exceptions are allowed. Later plans add names, and plan 9 deletes the test along with `WithLegacyState`.

**Execution:**
- Branch first: `git switch -c p2-2a-status` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-2a-status: record the plan 2 split and plan 2a`.
- Run the tasks in order. Run each task's Verify and commit it as `p2-2a-status: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/acpwire`, after Task 1** (`status.go` and `time.go`).

```go
// SessionStatus is what the status line, the footer and input gating read
// about a session (C5).
type SessionStatus struct {
	WorkingDir      string      `json:"workingDir"`
	GenerationSeq   int         `json:"generationSeq,omitempty"`
	LeafID          int64       `json:"leafId,omitempty"`
	Branches        []int64     `json:"branches,omitempty"`
	ActiveRoute     Route       `json:"activeRoute"`
	TurnRoute       *Route      `json:"turnRoute,omitempty"` // nil between turns
	TurnUsed        int         `json:"turnUsed,omitempty"`
	TurnWindow      int         `json:"turnWindow,omitempty"`
	Quota           *Quota      `json:"quota,omitempty"`
	Effort          Effort      `json:"effort"`
	Interaction     Interaction `json:"interaction"`
	SudoUnlock      bool        `json:"sudoUnlock,omitempty"`
	Trusted         bool        `json:"trusted,omitempty"`
	HasBackup       bool        `json:"hasBackup,omitempty"`
	Activity        Activity    `json:"activity"`
	SubagentRunning bool        `json:"subagentRunning,omitempty"`
	Workspace       Workspace   `json:"workspace"`
	Browser         Browser     `json:"browser"`
}

type Route struct {
	Role             string `json:"role,omitempty"`
	Profile          string `json:"profile,omitempty"`
	Preset           string `json:"preset,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	LocalOnly        bool   `json:"localOnly,omitempty"`
	Thinking         string `json:"thinking,omitempty"`
	ReasoningCapable bool   `json:"reasoningCapable,omitempty"`
	Active           bool   `json:"active,omitempty"`
}

type Quota struct {
	PlanType              string `json:"planType,omitempty"`
	PrimaryUsedPercent    int    `json:"primaryUsedPercent"`
	PrimaryResetAfterMs   int64  `json:"primaryResetAfterMs,omitempty"`
	PrimaryWindowMs       int64  `json:"primaryWindowMs,omitempty"`
	SecondaryUsedPercent  int    `json:"secondaryUsedPercent"`
	SecondaryResetAfterMs int64  `json:"secondaryResetAfterMs,omitempty"`
	SecondaryWindowMs     int64  `json:"secondaryWindowMs,omitempty"`
}

type Effort struct {
	Level    string `json:"level,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type Interaction struct {
	Mode     string `json:"mode"`
	Auto     bool   `json:"auto,omitempty"`
	System   bool   `json:"system,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
}

type Activity struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
}

type Workspace struct {
	ProjectRoot  string `json:"projectRoot,omitempty"`
	ActiveRoot   string `json:"activeRoot,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BaseSha      string `json:"baseSha,omitempty"`
	TargetBranch string `json:"targetBranch,omitempty"`
}

type Browser struct {
	Active      bool   `json:"active,omitempty"`
	ToolName    string `json:"toolName,omitempty"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	Mode        string `json:"mode,omitempty"`
	SessionOpen bool   `json:"sessionOpen,omitempty"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
}

// Millis is t as Unix milliseconds, and 0 for the zero time.
func Millis(t time.Time) int64

// Time is the time ms names, and the zero time for 0.
func Time(ms int64) time.Time
```

**`internal/acpclient`, after Task 2.** `Session` gains:

```go
// Status is the session's current status (C5). It is read without
// blocking: the in-process adapter builds it from the state on each call,
// and P3 serves it from a mirror.
//
// ACP: session/status (new in P3). A session_status update refreshes it.
Status() acpwire.SessionStatus
```

and `Methods` gains `{Iface: "Session", Name: "Status", ACP: "session/status"}`.

**`internal/acpclient/inproc`, after Task 2.**

```go
func (s *Session) Status() acpwire.SessionStatus
```

**`internal/app/tui`, after Tasks 4 and 6.**

```go
func quotaSegment(q *acpwire.Quota) string          // status.go
func browserStatusText(bi acpwire.Browser) string   // status.go
func (p *Panel) SetQuota(q *acpwire.Quota)          // agents/panel.go
func quotaLine(q *acpwire.Quota) string             // agents/panel.go
```

**The moved-reads guard (package `tui`, `moved_reads_test.go`, Task 7).**

```go
// movedStateReads names each session.State method or field the TUI no
// longer reads from m.state, with the plan that moved it.
var movedStateReads = map[string]string{ /* Task 7 */ }

func movedReadSites(fset *token.FileSet, f *ast.File, moved map[string]string) []string
```

**Strings the tests pin:**

| Where | Text |
|---|---|
| T1b violation | `<file>:<line> reads state.<Name>, which moved behind the seam in plan <plan>: read it from m.sess` |

## Task 1: The wire status types

**Goal:** `acpwire` holds `SessionStatus` and its parts, plus the time helpers, and T2's rules pass over them.

**Files:** `internal/acpwire/status.go`, `internal/acpwire/time.go`, `internal/acpwire/time_test.go`.

**Steps:**

1. **`status.go`.** Declare the types exactly as in Contracts.
   - `SessionStatus`'s doc says plan 2b adds the runs, queues and git info, plan 2c the start time, plans 3 and 4 the sandbox, and plan 6 the notice (spec C5).
   - Each type gets a one-line doc naming the `session` value it mirrors: `RouteInfo`, `schema.QuotaInfo`, `EffortOverride`, `interaction.State`, `Activity`, `Workspace` and `BrowserInfo`. The doc names them in prose only, because `acpwire` imports nothing but the standard library.
   - `Quota`'s doc says its durations are milliseconds, converted from the seconds and minutes the provider reports.
2. **`time.go`.** `Millis` returns `t.UnixMilli()`, or 0 when `t.IsZero()`. `Time` returns `time.UnixMilli(ms)`, or `time.Time{}` when `ms` is 0.
3. **`time_test.go`.**
   - `TestMillisOfTheZeroTimeIsZero`: `Millis(time.Time{})` is 0, and `Time(0).IsZero()` is true.
   - `TestMillisRoundTrips`: for `time.Unix(100, 5_000_000)`, `Time(Millis(t)).Equal(t)` holds, and `Millis` of it is `100005`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/acpwire/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpwire/
```

Expected: the two new tests pass, and so do `TestWireFieldsFollowTheRules` and `TestWireImportsOnlyTheStandardLibrary`, now over the new types.

## Task 2: `Session.Status` on the agent side

**Goal:** the interface has `Status`, its ACP row is in the table, and the in-process adapter builds it from the state.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/inproc_test.go`.

**Steps:**

1. **`acpclient.go`.** Add `Status` to `Session` after `ID`, with the doc comment from Contracts.
2. **`methods.go`.** Add the `Status` row from Contracts after the `ID` row.
3. **`inproc.go`, `Status`.** It reads `s.state` once per field and fills every `SessionStatus` field:

   | Field | From the state |
   |---|---|
   | `WorkingDir` | the `WorkingDir` field |
   | `GenerationSeq` | `Generation().Seq` |
   | `LeafID`, `Branches` | `LeafID()`, `Branches()` |
   | `ActiveRoute` | `ActiveRoute()`, through `routeOf` |
   | `TurnRoute` | `TurnRoute()`: a pointer to its `routeOf` when ok, else nil |
   | `TurnUsed`, `TurnWindow` | `TurnUsage()` |
   | `Quota` | `TurnQuota()`, through `quotaOf` |
   | `Effort` | `EffortOverride()`, field by field |
   | `Interaction` | `InteractionState()`, with `Mode` as `string(…)` |
   | `SudoUnlock`, `Trusted`, `HasBackup` | `SudoUnlockEnabled()`, `Trusted()`, `HasBackup()` |
   | `Activity` | `Activity()`, with `Kind` as `string(…)` and `StartedAt` through `acpwire.Millis` |
   | `SubagentRunning` | `HasRunningSubagent()` |
   | `Workspace` | `Workspace()`, field by field |
   | `Browser` | `BrowserInfo()`, with `UpdatedAt` through `acpwire.Millis` |

   The doc says the fields are read one at a time, as the TUI read them before, so the value is not one atomic snapshot.
   - `routeOf(r session.RouteInfo) acpwire.Route` copies every field, with `Role` as `string(r.Role)`.
   - `quotaOf(q *schema.QuotaInfo) *acpwire.Quota` returns nil for nil. Otherwise it copies `PlanType` and both percents, multiplies the two `…ResetAfterSecs` by 1000 into `…ResetAfterMs`, and multiplies the two `…WindowMinutes` by 60000 into `…WindowMs`.
4. **Tests (`inproc_test.go`, T5).**
   - `TestStatusOfAFreshState`: a state built as `TestSessionIDIsTheStateID` builds it has `WorkingDir` equal to the state's, `TurnRoute` and `Quota` nil, `Interaction.Mode` equal to `string(state.InteractionState().Mode)`, and `Activity.StartedAt` 0 when the state's activity has no start.
   - `TestStatusMirrorsTheState`: set every field through its setter:
     - `SetActiveRoute` and `SetTurnRoute`, with two different routes whose nine fields are all non-zero;
     - `SetTurnUsage(1200)` and `SetTurnContextWindow(8000)`;
     - `SetTurnQuota` with all seven fields non-zero;
     - `SetEffortOverride`;
     - `ApplyInteraction` with `Mode` Act and `Auto` true;
     - `SetSudoUnlockEnabled(true)` and `SetTrusted(true)`;
     - `SetActivity` with kind `session.ActivityTool`, a label and a start of `time.Unix(100, 5_000_000)`;
     - `SetWorkspace` with all five fields;
     - `SetBrowserInfo` with all seven fields;
     - `BeginGeneration("g", 2, "d")`.

     Compare `Status()` with an expected literal, field by field:
     - `Quota.PrimaryResetAfterMs` is the seconds times 1000, and `PrimaryWindowMs` the minutes times 60000;
     - `Activity.StartedAt` is `100005`;
     - `GenerationSeq` is 2;
     - `Interaction.Revision` is whatever the state reports.
   - `TestStatusOfAClosedSessionStillReads`: after `Swap`, the old session's `Status().WorkingDir` is still its own state's.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/...
```

Expected:
- the build succeeds, so every `Session` implementation (only `inproc`) has `Status`;
- the three new tests pass, along with `TestMethodTableCoversTheInterfaces` and `TestMethodDocsNameTheirACPMethod`;
- the ACP table test passes, because `session/status` is not registered.

## Task 3: Identity reads

**Goal:** the TUI reads its working directory, generation, leaf and branches from `Status`, and its session ID from `ID`.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/status.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/sdd_scaffold.go`, `internal/app/tui/todos.go`.

**Steps:** apply decision 2's rule at each site.

1. **`m.state.WorkingDir` → `st.WorkingDir`** (29 sites):
   - `commands_dispatch.go:442`, inside `init`'s `/runs` closure;
   - in `model.go`:
     - `persistAndReload` (1387, 1395);
     - `reportSetResult` (1596 and 1598, two each);
     - `openSettingsBrowser` (1630);
     - `openAgentsRoster` (1663);
     - `refreshSheetFleet` (2033);
     - `update`'s cases: 2476, 2479 and 2480; 2506; 2529 (two); 2839 and 2845 (two); and 2930;
     - `openRunPreflight` (4373, 4427);
     - `startSDDAuthoring` (4650, 4659, 4686);
     - `openSessionPicker` (7244);
     - `openSDDPlanPicker` (7441);
   - `sdd_scaffold.go:69`, in `scaffoldSDDPlan`;
   - `status.go:377`, in `statusLeftSegments`.
2. **`statusLeftSegments` (`status.go:247`).**
   - `m.state.Generation()` (358) becomes `st.GenerationSeq`, and its condition reads `st.GenerationSeq > 0`.
   - `m.state.Branches()` (365) becomes `st.Branches`, and `m.state.LeafID()` (366) becomes `st.LeafID`.
3. **`m.state.SessionID()` → `m.sess.ID()`** (12 sites):
   - `commands_dispatch.go:117` (`sendNowCommand`) and `:142` (`sendNowFlushOrOpen`);
   - in `model.go`: `refreshSheetTurns` (2014), `reconcileAccess` (3269), `beginSendNow` (6034), `openSteeringComposer` (6064, 6067), `handleComposerSubmit` (6171), `resetTasksPanelWork` (6361), `handleWorkMsg` (6385) and `openSessionPicker` (7257);
   - `todos.go:162` (`newTasksPanel`).

   `writePostmortemFor`'s `state.SessionID()` and `state.WorkingDir` (5452, 5453) read its parameter, and `v.Child.SessionID()` (6665) reads a child, so all three stay.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'm\.state\.(WorkingDir|Generation|LeafID|Branches|SessionID)\b' --include='*.go' internal/app/tui | grep -v '_test.go:'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/... ./internal/app/
```

Expected: the grep prints nothing, and both packages fail only on Task 8's baseline list.

## Task 4: Route and effort reads

**Goal:** the routes, turn usage, quota and effort override come from `Status`, and the quota helpers take the wire type.

**Files:** `internal/app/tui/status.go`, `internal/app/tui/effort.go`, `internal/app/tui/model_options.go`, `internal/app/tui/model.go`, `internal/app/tui/agents/panel.go`; tests `internal/app/tui/status_test.go` and `internal/app/tui/agents/panel_test.go`.

**Steps:**

1. **`statusLeftSegments`.**
   - `m.state.ActiveRoute()` (261) becomes `st.ActiveRoute`.
   - `m.state.TurnRoute()` (263) becomes `st.TurnRoute`, with `running := st.TurnRoute != nil`, and `turn` is `*st.TurnRoute` when running and the zero `acpwire.Route` otherwise.
   - `m.state.EffortOverride()` (271) becomes `st.Effort`.
   - `m.state.TurnUsage()` (326) becomes `st.TurnUsed` and `st.TurnWindow`.
   - `m.state.TurnQuota()` (348) becomes `st.Quota`.

   The rest of the function reads the same field names, because `Route` and `Effort` keep them.
2. **`quotaSegment` (`status.go:528`)** takes `*acpwire.Quota`. Its countdown is `time.Duration(q.PrimaryResetAfterMs) * time.Millisecond`, shown when `PrimaryResetAfterMs > 0`. Drop the `schema` import if nothing else in the file uses it.
3. **`agents/panel.go`.**
   - The `quota` field (72) becomes `*acpwire.Quota`, and so does `SetQuota`'s parameter (78). The field's comment stays.
   - `quotaLine` (789) takes `*acpwire.Quota`, and its two countdowns use `…ResetAfterMs` times `time.Millisecond`.
   - Drop the `schema` import if nothing else uses it.
4. **`openAgentsRoster` (`model.go:1646`).** `roster.SetQuota(m.state.TurnQuota())` (1669) passes `st.Quota`, from the same `st` Task 3 gave this function for `WorkingDir`.
5. **`ActiveRoute`, elsewhere.** `m.state.ActiveRoute()` becomes `st.ActiveRoute`, inline where it is the function's only read:
   - `effortLevels` (`effort.go:15`);
   - `openModelOptions` (`model_options.go:37`);
   - `cycleModel` (`model.go:7422`);
   - `modelPickerItems` (`model.go:7765`).
6. **`EffortOverride`, elsewhere.** `m.state.EffortOverride().Level` becomes `m.sess.Status().Effort.Level` in `cycleEffort` (`effort.go:61`) and `openEffortPicker` (`effort.go:75`).
7. **Tests.**
   - `TestQuotaSegmentOmitsZeroReset` (`status_test.go:96`) passes `&acpwire.Quota{PrimaryUsedPercent: 12}`.
   - `TestRosterShowsQuotaWhenSet` (`agents/panel_test.go:41`) passes an `acpwire.Quota` with `PrimaryResetAfterMs: 14845000` and `SecondaryResetAfterMs: 601645000`. Those are its seconds times 1000, so the view still shows `4h7m` and `6d23h`.
   - `TestRosterQuotaClearedByNil` passes `&acpwire.Quota{PrimaryUsedPercent: 50}`.
   - The tests that set a quota through `m.state.SetTurnQuota` keep `schema.QuotaInfo`, because that is the state's type.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'm\.state\.(ActiveRoute|TurnRoute|TurnUsage|TurnQuota|EffortOverride)\(' --include='*.go' internal/app/tui | grep -v '_test.go:'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/ -run 'Quota|Roster|StatusLine|Effort|Think|Route|ModelPicker' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/... ./internal/app/
```

Expected: the grep prints nothing, the named tests pass, and both packages fail only on Task 8's baseline list.

## Task 5: Access reads

**Goal:** the interaction state, sudo unlock, trust and backup flags come from `Status`.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/status.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/keypress.go`.

**Steps:**

1. **`InteractionState`.** `Interaction.Mode` is a string, so a comparison with `interaction.ModeAct` compares against `string(interaction.ModeAct)`, and a value passed on as a mode is `interaction.Mode(st.Interaction.Mode)`.
   - `New` (`model.go:1815`): `m.syncedAccessMode` is `st.Interaction.Mode`. This `st` is taken after `state := m.state`, and Task 6 reuses it.
   - `reconcileAccess` (3251): `effectiveMode` is `st.Interaction.Mode`, and the unused `effective` local goes. Keep the `m.state == nil` guard at its top.
   - `setAuto` (7347): `on = !m.sess.Status().Interaction.Auto`.
   - `setSystem` (7378): `on = !m.sess.Status().Interaction.System`.
   - `cycleMode` (7395): `current := interaction.Mode(m.sess.Status().Interaction.Mode)`.
   - `modePickerItems` (7815): `current := m.sess.Status().Interaction.Mode`.
   - `modeSegment` (`status.go:200`): `access := m.sess.Status().Interaction`, and the mode check compares strings.
2. **`SudoUnlockEnabled`.** `commands_dispatch.go:366`, in the `/system unlock` closure: `on := !m.sess.Status().SudoUnlock`.
3. **`Trusted`.**
   - `commands_dispatch.go:291` and `:296` (the `/skills` and `/plugins` closures) pass `m.sess.Status().Trusted`.
   - `commands_dispatch.go:304` reads `m.sess.Status().Trusted`.
   - `statusLeftSegments` (`status.go:252`): `!st.Trusted`.
4. **`HasBackup`.**
   - `New` (`model.go:1894`): `st.HasBackup`.
   - `handleApproval` (`model.go:3447` and `3534`): one `st`.
   - `handleKeypress` (`keypress.go:253`): `m.sess.Status().HasBackup`.
   - `footerHints` (`status.go:550`): `m.sess.Status().HasBackup`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'm\.state\.(InteractionState|SudoUnlockEnabled|Trusted|HasBackup)\(' --include='*.go' internal/app/tui | grep -v '_test.go:'
grep -nE '\bstate\.(InteractionState|HasBackup)\(' internal/app/tui/model.go
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/ -run 'Mode|Auto|System|Access|Approval|Rollback|Untrusted|Skills|Plugins' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/... ./internal/app/
```

Expected:
- both greps print nothing;
- no named test fails apart from the baseline's `TestStatusLineUntrustedIsWarningColored`, if Task 8 lists it;
- both packages fail only on Task 8's baseline list.

## Task 6: Activity, workspace and browser reads

**Goal:** the activity, the running-subagent flag, the workspace roots and the browser state come from `Status`.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/status.go`, `internal/app/tui/commands_dispatch.go`, `internal/app/tui/browse_actions.go`.

**Steps:**

1. **`Activity`.** In `activeSpinnerFrame` (`model.go:4840`), `act := m.state.Activity()` (4847) becomes `act := m.sess.Status().Activity`, and the 200 ms check reads `acpwire.Time(act.StartedAt)`.
2. **`HasRunningSubagent`.** In `handleSpinnerTick` (`model.go:6851`), the check reads `!m.sess.Status().SubagentRunning`.
3. **`Workspace` → `st.Workspace`** (13 sites). Each keeps the field it reads (`ActiveRoot` or `ProjectRoot`):
   - `New` (1906, 1908), reusing Task 5's `st`;
   - `Init` (1953);
   - `refreshSheetChanged` (2022);
   - `refreshSheetFleet` (2031), with the same `st` Task 3 gave it for 2033;
   - `update`'s case at 2429;
   - `handleAgentFinished` (5623);
   - `handleSheetBaseRef` (6784);
   - `handleAgentTick` (6803 and 6804, one `st`);
   - `startNewSession` (`commands_dispatch.go:203` and `:245`), read after `m.sess` is replaced;
   - `nodeFile` (`browse_actions.go:182`).

   `fullToolHead` (`selection.go:185`) reads its parameter, and `refreshViewport` (`viewport_render.go:120`) reads `transcriptState`, so both stay.
4. **`BrowserInfo`.**
   - In `statusLeftSegments` (`status.go:438`), `bi := st.Browser`.
   - `browserStatusText` (455) takes `acpwire.Browser`. Its body is unchanged, because the field names are the same.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'm\.state\.(Activity|HasRunningSubagent|Workspace|BrowserInfo)\(' --include='*.go' internal/app/tui | grep -v '_test.go:'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/ -run 'Spinner|Workspace|Git|Browser|Sheet|NewSession|Worktree' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/... ./internal/app/
```

Expected: the grep prints nothing, and both packages fail only on Task 8's baseline list.

## Task 7: The moved-reads guard

**Goal:** T1b fails on any read of a moved `State` method or field through a model's `state`.

**Files:** `internal/app/tui/moved_reads_test.go`.

**Steps:**

1. **`movedStateReads`** maps these 18 names to `"2a"`: `WorkingDir`, `Generation`, `LeafID`, `Branches`, `SessionID`, `ActiveRoute`, `TurnRoute`, `TurnUsage`, `TurnQuota`, `EffortOverride`, `InteractionState`, `SudoUnlockEnabled`, `Trusted`, `HasBackup`, `Activity`, `HasRunningSubagent`, `Workspace` and `BrowserInfo`. Its doc says:
   - each later P2 plan adds the names it moves, with its own plan number;
   - plan 9 deletes the test along with `WithLegacyState`;
   - a name is never removed to make the test pass.
2. **`movedReadSites`** returns, for one file, one entry `<line>\t<Name>` per selector `X.Sel` where:
   - `X` is itself a selector whose `Sel` is `state` and whose `X` is an identifier, as in `m.state`;
   - the outer `Sel` is in the list.

   This finds `m.state.Trusted()` and `m.state.WorkingDir`. It does not find `child.Trusted()`, `state.Workspace()` or `m.state.Config`.
3. **`TestMovedStateReadsStayMoved`** walks the non-test `.go` files under `.`, skipping `testdata`, as `seamViolations` (`boundary_test.go`) walks them. It parses each file and reports every site with `t.Errorf`, using the T1b string from Contracts.
4. **`TestMovedReadsGuardFindsSelectors`** guards the guard. It parses a source string with one function containing `m.state.Trusted()`, `m.state.WorkingDir`, `child.Trusted()` and `m.state.Config`. `movedReadSites` returns exactly two entries, naming `Trusted` and `WorkingDir`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2a-suite go test ./internal/app/tui/ -run 'TestMovedStateReadsStayMoved|TestMovedReadsGuardFindsSelectors|TestTUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/tui/
```

Expected: all three pass.

As a check of the test (no commit), temporarily add `_ = m.state.Trusted()` to `statusLeftSegments` and see the test fail with the T1b string naming `status.go`. Then revert.

## Task 8: Verification and docs

**Goal:** the full suite matches the baseline, and the spec records plan 2a.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 2a entry to the Implementation record. It lists:
   - the wire types and `Session.Status`;
   - the 18 moved names in T1b;
   - whether T1's allowlist changed.
2. **`design.md` §7.** The P2 row's status adds `plan 2a implemented`.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-2a-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-2a-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the failures are exactly the baseline below. A flaky one counts only if it passes alone with `-count=3`.

**Baseline at `4a81ef11`**, from a full run with `-timeout 20m`:
- `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree` and `TestSubagentShellOutputDoesNotReachParent`.
- `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce` is intermittent and fails on its own too, in 7 of 10 runs with `-count=10`: `tuple after a concurrent reload = {Mode:act Auto:false System:false Revision:2}, want act with system access on`. It counts as pre-existing whether it passes or fails.
- `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`.
- `internal/snapshot`: `TestRunManagedCancellationKillsTree` fails under full-suite load and passes alone with `-count=3`.
- `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`.
- `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`.
- Known to be flaky under load, and passing in this run: `TestRuntimeCloseRunsNoGitGC` (`internal/app`) and `TestACPChildApprovalUsesOwningSessionRegistry` (`internal/acp`).
- `web/bridge` took 95 s.

Finally, run `./marshal` in this repository and check the status line by eye:
- the mode cue, model and `ctx` segment are as before;
- Tab still toggles Ask and Act;
- `/system` and `/auto` still report and flip their state.

## Integration notes

- **Plan 2b** starts from this branch's merge. It adds `Session.Changed`, removes the pumps, and adds the runs, queues and git info to `SessionStatus`. It extends T1b's list with `SteeringQueue`, `RunningJobsCount`, `Subagents`, `Subagent`, `SwarmProgress`, `SDDProgress`, `SDDGate` and the git reads.
- **Plan 2c** adds `Session.Sheet`. It takes the start time, the tool-call log, the context pack, the skills, the rules, the tool budget and the loaded tools out of `sessionsheet`'s `d.State`.

## Self-review

- Every task commits alone. Tasks 1 and 2 are agent side. Tasks 3 to 6 each move one family of reads and verify that none remains. Task 7 locks them in.
- Task 2 spans `acpclient` and `inproc` because adding a method to `Session` breaks the build until its one implementation has it.
- Cold-executor check: every site is listed with its function and line. The one rule for where `st` goes is decision 2, and its exceptions (`update`, `init`, `startNewSession`) are named.
- Contracts hold every cross-task declaration: the wire types, the helpers, the interface method, the changed helper signatures and the guard.
- Anchors were checked at `4a81ef11`. The 89 sites in 47 functions came from the type-checked scan, without the child and parameter receivers it also found.
- Nothing here contradicts the spec. Decision 1 and the C5 placement changes are already written into it.
