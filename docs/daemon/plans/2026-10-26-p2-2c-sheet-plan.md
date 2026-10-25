# P2 plan 2c: the session sheet — implementation plan

Date: 2026-10-26. Status: ready for review.
Base: `main` at `726e1506` (plan 2b merged) or later.
- Every anchor below was checked at `726e1506`. Line numbers are there to find the code fast; the function names are the anchors.
- The JSON literals in Contracts were produced by encoding the declared types, not written by hand.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C2 (reads and calls), C3 (wire rules), C4 (the adapter), C5 (status and the sheet), Migration order item 2c and its revision record entry against `726e1506`, and tests T1, T1b, T2, T4 and T5.
Previous plan: [plan 2b, runs, queues and events](2026-10-25-p2-2b-events-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the session sheet reads everything it renders from `Session.Sheet(subagentID)`, and the TUI no longer queries the database or runs git for it. Nothing a user sees changes.
- `acpwire` holds the sheet types, and `SessionStatus` gains the changed-file count the status line shows.
- `viewmodel` holds the per-tool and per-file aggregation that the sheet's tools and files sections use.
- `Session` gains `Sheet` and `RefreshSheet`. The in-process adapter keeps the sheet's database and git values (HEAD, the changed files, the turn metrics, the usage totals, the repo counts and the agent worktrees) and re-reads them on request.
- The TUI calls `RefreshSheet` where it re-reads those values today. Its base-ref command, message and handler, and its six sheet caches, are deleted.
- `sessionsheet` imports no agent-side package.
- T1b gains seven names.

**Non-goals:**
- **No turn boundaries move.** The TUI still runs turns, so it still decides when the sheet refreshes. Plan 5 moves turns behind `Prompt`, and the adapter can then refresh on its own.
- **No drill-in move.** The view stack still holds `session.SubagentView` values, and `drillIntoSubagent` still checks `Child`. Plan 3 moves the drill-in. The sheet only passes the drilled-in view's ID.
- **No config reads or notes move.** `sheetSections` reads `m.state.Config.TUI.SidePanel.Hidden`, and `openSessionSheet` writes its "Nothing to show" note with `AddMessage`. They stay for plans 7 and 6.
- **No memory store move.** `WithMemoryStore` still feeds the memory panel, the doctor, prompt history and the file index, which move in plan 8. The adapter takes the project ID from `Run` as well.
- **No ACP handler.** `session/sheet` is new in P3.
- **No changelog entry.** Nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-2c-suite`. Create the directory first: `mkdir -p /private/tmp/p2-2c-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- `git` is on `PATH`. The new git-backed tests skip without it, as `TestAgentFinishedAdvancesSheetBaseRef` does today (`exec.LookPath("git")`).
- **Pre-existing failures** at `726e1506` are listed in Task 8, from a full run with `-timeout 20m`. A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`. The exceptions are named there.

**Decisions this plan makes beyond the spec** (decisions 1 to 5 are also recorded in the spec's revision record):

1. **The sheet refreshes on request.** `RefreshSheet(ctx, acpwire.SheetRefresh)` re-reads the values its flags name and then fires `Changed`. The TUI keeps today's trigger points:

   | When | Today | After this plan |
   |---|---|---|
   | `New` | reads HEAD (`model.go:1783`) and the repo counts (1794–1801) on the UI thread | nothing |
   | `Init` | `sheetBaseRefCmd` | `refreshSheetCmd` with `Rebase` |
   | turn end | `sheetBaseRefCmd`, plus the turn metrics and the fleet on the UI thread when the sheet is open (5438–5441) | one `refreshSheetCmd` with `Rebase`, plus `Stats` and `Fleet` when the sheet is open |
   | workspace move (`handleChanged`) | `sheetBaseRefCmd` | `refreshSheetCmd` with `Rebase` |
   | new session | clears the list, then `sheetBaseRefCmd` | `refreshSheetCmd` with `Rebase` on the new session, whose cache starts empty |
   | sheet open | the changed files against the current base, the turn metrics and the fleet, on the UI thread | `RefreshSheet` with `Changed`, `Stats` and `Fleet`, on the UI thread |

   Opening the sheet stays synchronous, so its first frame shows fresh values. Everything else runs off the UI thread, as the base-ref command does today. The one timing change: at turn end, the turn metrics and the fleet of an open sheet are now read off the UI thread with the rebase, so they land a moment after the turn ends rather than in the same frame.
2. **HEAD stays agent side.** The adapter keeps it as the changed-files base and never returns it. `Status` carries `ChangedFiles`, the count the status line shows (`status.go:347`).
3. **The start time is a sheet field.** Only the session section reads it (`section_session.go:38`), and a subagent's sheet needs the child's own.
4. **The sheet carries aggregates, not the tool-call log.**
   - `viewmodel.SheetToolsOf` and `SheetFilesOf` are `sessionsheet.ToolStats` (`section_tools.go:24`) and `FileStats` (`filestats.go:52`), returning wire types.
   - They go to `viewmodel`, not the adapter, for the reason plan 2b's steering conversion did: P3's `session/sheet` handler in `acp` needs them, and the adapter is deleted then.
   - One edge changes. Today the files section is relevant when the log holds a successful `file.read` whose arguments name no path, and it then renders "0 files". From this plan on, it is relevant only when there is a file to list.
5. **The language mix is deleted.** `RepoStats.Languages` is never set outside tests. `LanguageShare`, the repo section's language row and that row's test assertion go with it.
6. **The refresh rules.** These are today's rules, moved into the adapter:
   - An empty active root reads no changed files.
   - `Rebase` reads HEAD of the active root, then the changed files against it. When HEAD cannot be read, the base and the changed files stay as they were. That is `handleSheetBaseRef`'s empty-ref rule (`model.go:6481`).
   - `Changed` reads against the cached base. When there is no base yet, or the base was read for another active root, it rebases first. This replaces the HEAD read in `New`.
   - A changed-files result is stored only when the active root is still the one it read. That is `handleSheetBaseRef`'s stale-dir rule (6478). A `Changed` result is also dropped when the cached base has changed since it was read.
   - `Stats` reads the 24 most recent turn metrics and the session's usage totals. Each is stored only when its query succeeds, as `refreshSheetTurns` (1869) does. `Stats` also reads the repo's file and symbol counts when the project ID is not 0, stored only when both succeed.
     - Today those counts are read once, in `New`, so they never changed during a session. Now they are re-read with the turn metrics, which is what the field's comment (557) already says.
   - A state with no database reads no stats.
   - `Fleet` lists the agent worktrees against the project root's HEAD, as `refreshSheetFleet` (1893) does. Any failure empties the list.
   - The session holds the results in one cache under its mutex. It reads nothing while holding the mutex. It replaces the cached slices and never changes them in place, so `Sheet` returns them without copying.
   - A closed session's `RefreshSheet` reads nothing and returns the closed-session error. A session that a swap closes during the reads stores nothing and returns the same error, as `SendNow` does.
   - `ctx` is not consulted: none of these reads takes one today.
7. **Subagents are found anywhere in the tree.** Subagent IDs are unique within the process (`subagentSeq`, `subagents.go:164`). A drill-in can nest, because `subagentAtCursor` looks the ID up in the transcript's own source (`browse.go:388`). So `Sheet` searches the session's subagents depth first.
   - An ID that names no child state reads as the session's own sheet, with `SubagentID` 0. That is today's fallback in `sheetDataFor` and `drilledSheetState`.
   - The TUI adds the run progress only when the returned `SubagentID` is 0.
8. **A subagent's sheet** holds what `childSheetData` (`model.go:2026`) builds today:
   - from the child: the start time, the activity, the rules, the skills, the tool budget, the loaded tools and the sandbox posture;
   - from the session: the fleet and the repo counts.

   Its context, changed files, turn metrics and totals stay empty, and the TUI adds no swarm or SDD progress.
9. **`Data` stays flat.** `sessionsheet.Data` keeps one field per value, now wire-typed, so the sections and their tests change only where a type does. Its `StartedAt` is a `time.Time`, zero when there is no session, which `sheetDataFor` converts from milliseconds.
10. **The adapter's database is the state's.** `refreshSheetTurns` and `New` use `m.state.DB()`, and so does the adapter. `Deps` gains only `ProjectID`, which `Run` already holds (`app.go:2789`).
11. **Each task trims its own allowlist entries.** T1 fails on an entry the code no longer needs, which plan 2b's tasks 7 to 9 ran into. Tasks 5 and 6 name their entries.
12. **The guard gains seven names.** `AuditLog`, `ContextPack`, `SessionRules` and `ActiveSkills` are read through `m.state` today. `ToolBudget`, `LoadedToolNames` and `StartedAt` are read through `d.State` inside the sheet, and listing them keeps them on the session.
13. **One status read per function**, as plan 2a's decision 2 sets out. `sheetDataFor` takes `st := m.sess.Status()` once.

**Execution:**
- Branch first: `git switch -c p2-2c-sheet` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-2c-sheet: record the plan 2c revisions and plan 2c`.
- Run the tasks in order. Run each task's Verify and commit it as `p2-2c-sheet: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.
- The change pump blocks until the next signal. `runCmdOnce` gives each command a deadline, so the walkers below stay safe. A test reads what a stub recorded only after `batchResults` or `runCmdChain` has returned.

## Contracts

**`internal/acpwire/sheet.go` (new), after Task 1.**

```go
// SessionSheet is what the session sheet's sections read (C5), for the
// session or for one subagent. Its database and git values are the ones the
// last RefreshSheet read.
type SessionSheet struct {
	SubagentID  int64              `json:"subagentId,omitempty"` // whose sheet: 0 for the session's own
	StartedAt   int64              `json:"startedAt,omitempty"`
	Context     SheetContext       `json:"context"`           // the session's only
	Changed     []SheetChangedFile `json:"changed,omitempty"` // the session's only
	Turns       []SheetTurn        `json:"turns,omitempty"`   // the session's only; most recent first
	Totals      SheetTotals        `json:"totals"`            // the session's only
	Fleet       []SheetFleetRow    `json:"fleet,omitempty"`
	Repo        SheetRepo          `json:"repo"`
	Tools       []SheetTool        `json:"tools,omitempty"` // most-called first
	Files       []SheetFile        `json:"files,omitempty"` // most recently touched first
	Rules       []string           `json:"rules,omitempty"`
	Skills      []string           `json:"skills,omitempty"`
	ToolBudget  SheetToolBudget    `json:"toolBudget"`
	LoadedTools int                `json:"loadedTools,omitempty"`
	Sandbox     SheetSandbox       `json:"sandbox"`
}

// SheetContext is the context pack's size and its sections' sizes. Every
// section is listed, an empty one included.
type SheetContext struct {
	MaxTokens       int                   `json:"maxTokens,omitempty"`
	EstimatedTokens int                   `json:"estimatedTokens,omitempty"`
	Sections        []SheetContextSection `json:"sections,omitempty"`
}

// SheetContextSection is one context-pack section's size.
type SheetContextSection struct {
	Title           string `json:"title"`
	EstimatedTokens int    `json:"estimatedTokens,omitempty"`
}

// SheetChangedFile is one file changed against the sheet's base. Status is
// git's status letter ("A", "M", "D", …).
type SheetChangedFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
}

// SheetTurn is one turn's token counts, from the turn metrics.
type SheetTurn struct {
	StartedAt        int64 `json:"startedAt,omitempty"`
	PromptTokens     int   `json:"promptTokens,omitempty"`
	CompletionTokens int   `json:"completionTokens,omitempty"`
	CacheReadTokens  int   `json:"cacheReadTokens,omitempty"`
}

// SheetTotals is the session's usage across all its turns.
type SheetTotals struct {
	Turns            int   `json:"turns,omitempty"`
	PromptTokens     int64 `json:"promptTokens,omitempty"`
	CompletionTokens int64 `json:"completionTokens,omitempty"`
	CacheReadTokens  int64 `json:"cacheReadTokens,omitempty"`
}

// SheetFleetRow is one agent worktree, against the project root's HEAD.
// AgeMs is the age of its branch tip.
type SheetFleetRow struct {
	Branch string `json:"branch"`
	Ahead  int    `json:"ahead,omitempty"`
	Behind int    `json:"behind,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	AgeMs  int64  `json:"ageMs,omitempty"`
}

// SheetRepo is the indexed repository's size.
type SheetRepo struct {
	Files   int `json:"files,omitempty"`
	Symbols int `json:"symbols,omitempty"`
}

// SheetTool is one tool's calls across the tool-call log.
type SheetTool struct {
	Name      string `json:"name"`
	Calls     int    `json:"calls"`
	Errors    int    `json:"errors,omitempty"`
	SlowestMs int64  `json:"slowestMs,omitempty"`
}

// SheetFile is one file's edits and reads across the tool-call log. LastAt
// is its latest touch.
type SheetFile struct {
	Path   string `json:"path"`
	Edits  int    `json:"edits,omitempty"`
	Reads  int    `json:"reads,omitempty"`
	LastAt int64  `json:"lastAt,omitempty"`
}

// SheetToolBudget mirrors the session's ToolBudget. Max 0 means no cap.
type SheetToolBudget struct {
	Used int `json:"used,omitempty"`
	Max  int `json:"max,omitempty"`
}

// SheetSandbox is the sandbox posture: the backend's name, and whether shell
// commands may reach the network.
type SheetSandbox struct {
	Backend      string `json:"backend,omitempty"`
	AllowNetwork bool   `json:"allowNetwork,omitempty"`
}

// SheetRefresh names the sheet values a RefreshSheet call re-reads.
type SheetRefresh struct {
	Rebase  bool `json:"rebase,omitempty"`  // HEAD of the active root as the base, then the changed files
	Changed bool `json:"changed,omitempty"` // the changed files against the current base
	Stats   bool `json:"stats,omitempty"`   // the turn metrics, the usage totals and the repo counts
	Fleet   bool `json:"fleet,omitempty"`   // the agent worktrees
}
```

`SessionStatus` (`status.go`) gains, directly after `Git`:

```go
	ChangedFiles int `json:"changedFiles,omitempty"` // files changed against the sheet's base
```

**`internal/viewmodel/sheet.go` (new), after Task 2.**

```go
// SheetToolsOf aggregates a tool-call log by tool name, most-called first,
// with ties broken by name. It returns nil for an empty log.
func SheetToolsOf(events []registry.AuditEvent) []acpwire.SheetTool

// SheetFilesOf aggregates a tool-call log by file path, most recently touched
// first, with ties broken by path. A failed call touches nothing. An edit is a
// path in FilesChanged, and a read is the path argument of a file.read call.
// It returns nil when no file was touched.
func SheetFilesOf(events []registry.AuditEvent) []acpwire.SheetFile
```

**`internal/acpclient`, after Tasks 3 and 4.** `Session` gains, after `SendNow`:

```go
	// Sheet is what the session sheet's sections read (C5): the session's
	// own sheet when subagentID is 0, or the sheet of the subagent with that
	// ID anywhere in the session's tree. An ID that names no child session
	// reads as 0, and the result's SubagentID says which sheet was read.
	// Sheet does not block: its database and git values are the ones the
	// last RefreshSheet read.
	//
	// ACP: session/sheet (new in P3). P3 serves it from a mirror that
	// RefreshSheet fills.
	Sheet(subagentID int64) acpwire.SessionSheet

	// RefreshSheet re-reads the sheet values req names from the database and
	// git, then fires Changed. It blocks on those reads.
	//
	// ACP: session/sheet (new in P3), sent with the refresh flags.
	RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error
```

and `Methods` gains, after the `SendNow` row:

```go
	{Iface: "Session", Name: "Sheet", ACP: "session/sheet"},
	{Iface: "Session", Name: "RefreshSheet", ACP: "session/sheet"},
```

**`internal/acpclient/inproc`, after Tasks 3 and 4.**

```go
// Deps gains, after GitInfo:
	// ProjectID is the project's database ID, for the sheet's turn metrics
	// and repo counts. The database itself is the state's.
	ProjectID int64

func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet
func (s *Session) RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error

// sheet.go, unexported. Task 3 adds the first three, Task 4 the cache.
func childState(st *session.State, id int64) *session.State
func activityOf(st *session.State) acpwire.SessionSheet
func contextOf(p contextpack.Pack) acpwire.SheetContext

// sheetCache holds what RefreshSheet read. Session.mu guards it.
type sheetCache struct {
	base     string // HEAD of baseRoot at the last rebase; "" before the first
	baseRoot string
	changed  []acpwire.SheetChangedFile
	turns    []acpwire.SheetTurn
	totals   acpwire.SheetTotals
	repo     acpwire.SheetRepo
	fleet    []acpwire.SheetFleetRow
}
```

`Session` gains `projectID int64`, `headSHA func(dir string) string` (`gitinfo.HeadSHA`, replaceable by the package's tests) and `sheet sheetCache`.

**`internal/app/tui`, after Tasks 5 and 6.**

```go
// pump.go (Task 5)
// refreshSheetCmd re-reads the sheet values req names, off the UI thread. sess
// fires Changed when they land, so the command returns no message.
func refreshSheetCmd(ctx context.Context, sess acpclient.Session, req acpwire.SheetRefresh) tea.Cmd

// model.go (Task 6)
func (m Model) sheetDataFor() sessionsheet.Data
// drilledSheetID is the drilled-in subagent's ID, or 0 when the view is not
// drilled in.
func (m Model) drilledSheetID() int64

// sessionsheet/section.go. Task 5 changes Changed, Task 6 the rest.
type Data struct {
	Git         acpwire.Git
	Repo        acpwire.SheetRepo
	Turns       []acpwire.SheetTurn // most recent first
	Totals      acpwire.SheetTotals
	Changed     []acpwire.SheetChangedFile
	Fleet       []acpwire.SheetFleetRow
	Now         time.Time // injected so elapsed-time rendering is deterministic
	Context     acpwire.SheetContext
	Tools       []acpwire.SheetTool
	Files       []acpwire.SheetFile
	Rules       []string
	Skills      []string
	StartedAt   time.Time // the session's start; zero when there is no session
	ToolBudget  acpwire.SheetToolBudget
	LoadedTools int
	Sandbox     acpwire.SheetSandbox
	Swarm       acpwire.SwarmProgress
	SDD         acpwire.SDDProgress
	Spinner     string
}

// sessionsheet/telemetry.go (Task 6)
func Telemetry(turns []acpwire.SheetTurn, now time.Time) TelemetryStats

// sessionsheet/section_session.go (Task 6)
func cachedShare(t acpwire.SheetTotals) int
```

**Test helpers.**

```go
// tui/seam_test.go: stubSession gains two fields and two methods.
	onRefreshSheet func(acpwire.SheetRefresh) // Task 5: sees each RefreshSheet request
	sheetPatch     func(*acpwire.SessionSheet) // Task 6: adjusts every Sheet read

func (s *stubSession) RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error // records, then delegates
func (s *stubSession) Sheet(subagentID int64) acpwire.SessionSheet                      // delegates, then patches

// tui/model_test.go (Task 5), in place of collectSheetBaseRefs:
// runCmdChain runs a command tree of batches and sequences, and returns the
// messages its leaves produce. A leaf that blocks for a second (the change
// pump) yields none.
func runCmdChain(t *testing.T, cmd tea.Cmd) []tea.Msg

// inproc/sheet_test.go (Task 4)
// sheetRepo returns a new git repository with a.txt ("one\n") committed on
// main. It is built the way the TUI's initSheetTestRepo (status_test.go:617)
// builds one: git init -b main, a local user identity, one commit.
func sheetRepo(t *testing.T) string
func runGit(t *testing.T, dir string, args ...string)
```

**Strings the tests pin:**

| Where | Text |
|---|---|
| `TestSessionSheetJSON`, a sheet with every field set (Task 1) | `{"subagentId":7,"startedAt":1000,"context":{"maxTokens":128000,"estimatedTokens":14000,"sections":[{"title":"system","estimatedTokens":2100}]},"changed":[{"path":"a.go","status":"M","added":3,"removed":1}],"turns":[{"startedAt":2000,"promptTokens":100,"completionTokens":20,"cacheReadTokens":50}],"totals":{"turns":4,"promptTokens":400,"completionTokens":80,"cacheReadTokens":200},"fleet":[{"branch":"feat/x","ahead":2,"behind":1,"dirty":true,"ageMs":60000}],"repo":{"files":412,"symbols":3100},"tools":[{"name":"file.read","calls":2,"errors":1,"slowestMs":20}],"files":[{"path":"a.go","edits":1,"reads":2,"lastAt":3000}],"rules":["go test"],"skills":["tdd"],"toolBudget":{"used":7,"max":100},"loadedTools":3,"sandbox":{"backend":"restricted","allowNetwork":true}}` |
| the same, the zero sheet | `{"context":{},"totals":{},"repo":{},"toolBudget":{},"sandbox":{}}` |
| the same, every refresh flag set | `{"rebase":true,"changed":true,"stats":true,"fleet":true}` |
| `RefreshSheet` on a closed session | `&acpwire.Error{Code: acpwire.CodeSessionClosed, Message: "inproc: the session was replaced"}` (the existing `errSessionClosed`) |
| T1b violation (unchanged) | `<file>:<line> reads state.<Name>, which moved behind the seam in plan <plan>: read it from m.sess` |

## Task 1: The wire types

**Goal:** `acpwire` holds the sheet types, `SessionStatus` has `ChangedFiles`, and T2's rules pass over all of them.

**Files:** `internal/acpwire/sheet.go`, `internal/acpwire/status.go`, `internal/acpwire/sheet_test.go`.

**Steps:**

1. **`sheet.go`.** Declare the types exactly as in Contracts, with their doc comments. The file's comment says these are what `Session.Sheet` returns and what `Session.RefreshSheet` takes. It also says that times are Unix milliseconds (`Millis`) and durations are milliseconds.
2. **`status.go`.**
   - Add `ChangedFiles` after `Git`, as in Contracts.
   - `SessionStatus`'s doc drops "Plan 2c adds the session's start time". It then says that plans 3 and 4 add the sandbox info and plan 6 the current notice. It keeps the sentence on the session ID, and adds that the start time is not here either, because only the sheet reads it, so `SessionSheet` carries it.
3. **`sheet_test.go`.** `TestSessionSheetJSON` marshals three values and compares each with its literal from the strings table:
   - the sheet with every field set. It is built from the values that the literal spells out, field for field;
   - the zero `SessionSheet`;
   - a `SheetRefresh` with all four flags set.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/acpwire/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpwire/
```

Expected: `TestSessionSheetJSON` passes. `TestWireFieldsFollowTheRules` and `TestWireImportsOnlyTheStandardLibrary` pass too, now over the new types.

## Task 2: The activity aggregates

**Goal:** `viewmodel` aggregates a tool-call log into the sheet's per-tool and per-file rows, with the tests that cover today's functions.

**Files:** `internal/viewmodel/sheet.go`, `internal/viewmodel/sheet_test.go`.

**Steps:**

1. **`SheetToolsOf`** has the body of `sessionsheet.ToolStats` (`section_tools.go:24`). It aggregates `Slowest` as a `time.Duration` and writes it out as `SlowestMs` (its `Milliseconds()`). It returns nil for an empty log.
2. **`SheetFilesOf`** has the body of `sessionsheet.FileStats` (`filestats.go:52`), with its `readTools` set (`filestats.go:24`) as the unexported `sheetReadTools`. It aggregates `Last` as a `time.Time` and writes it out as `LastAt` through `acpwire.Millis`. It returns nil when the log is empty and when no file was touched, as `FileStats` does.
3. **`sheet_test.go`** copies today's tests. Each test keeps its fixture and its assertions:
   - `Slowest` becomes `SlowestMs`, in milliseconds;
   - `Last` becomes `LastAt`, compared with `acpwire.Millis` of the expected time.

   The copies:
   - from `section_tools_test.go`: `TestToolStatsAggregates` becomes `TestSheetToolsOfAggregates`, `TestToolStatsSortedByCallsDescending` becomes `TestSheetToolsOfSortsByCallsDescending`, and `TestToolStatsEmpty` becomes `TestSheetToolsOfEmptyIsNil`, which asserts nil, not just empty. Each comes with `toolEvents()`;
   - from `filestats_test.go`: the nine `TestFileStats*` tests become `TestSheetFilesOf*`, for example `TestFileStatsMalformedArgsAreSkipped` becomes `TestSheetFilesOfMalformedArgsAreSkipped`. They come with `writeEvent` and `readEvent`, and `at` is renamed `sheetAt`, because `viewmodel`'s `stack_test.go:15` already declares `at`.

   The `sessionsheet` originals stay until Task 6 deletes them.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/viewmodel/ -run 'TestSheet' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/viewmodel/
go vet ./internal/viewmodel/
```

Expected: the twelve `TestSheet*` tests pass, and so does the rest of the package.

## Task 3: The sheet read

**Goal:** `Session.Sheet` returns the session's or a subagent's in-memory sheet values. The database and git values stay empty until Task 4.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acpclient/inproc/sheet.go`, `internal/acpclient/inproc/sheet_test.go`.

**Steps:**

1. **`acpclient.go` and `methods.go`.** Add `Sheet` to `Session` after `SendNow`, with the doc from Contracts, and its row to `Methods`.
2. **`inproc/sheet.go`** (new):
   - `Sheet(subagentID)`:
     - starts from the session's state with an ID of 0;
     - when `subagentID` is not 0 and `childState(s.state, subagentID)` finds a child, uses that child and `subagentID` instead;
     - returns `activityOf` of the chosen state with `SubagentID` set to the chosen ID, and, for ID 0 only, `Context` set to `contextOf(state.ContextPack())`.
   - `childState(st, id)` walks `st.Subagents()` in order. It skips a view whose `Child` is nil, returns the `Child` of the view whose `ID` is `id`, and otherwise searches that child's own subagents before moving on. It returns nil when nothing matches.
   - `activityOf(st)` fills:

     | Field | From |
     |---|---|
     | `StartedAt` | `acpwire.Millis(st.StartedAt)` |
     | `Tools`, `Files` | `viewmodel.SheetToolsOf` and `SheetFilesOf` of one `st.AuditLog()` read |
     | `Rules`, `Skills` | `st.SessionRules()` and `st.ActiveSkills()`, nil when empty |
     | `ToolBudget` | `st.ToolBudget()`, field by field |
     | `LoadedTools` | `len(st.LoadedToolNames())` |
     | `Sandbox` | `st.SandboxInfo().Backend`, and `st.Config.Tools.Shell.AllowNetwork` |

   - `contextOf(p)` copies `p.TokenUsage.MaxTokens` and `EstimatedTokens`, plus one entry per section in order, with its `Title` and `EstimatedTokens`. A section with no tokens is kept, because the context section is relevant whenever the pack has any section (`section_context.go:26`). It gives nil `Sections` for a pack with none.
3. **`sheet_test.go`** (T5):
   - `TestSheetCarriesTheSessionsActivity`. Set up the state with:
     - three tool calls with explicit timestamps: `file.read` of `a.go` (`Args` `{"path":"a.go"}`, 10 ms, at `time.Unix(10, 0)`), `file.write_patch` with `FilesChanged` `[b.go]` (30 ms, at `time.Unix(20, 0)`), and `shell.run` with `Error` "exit 1" (50 ms, at `time.Unix(30, 0)`);
     - `AddSessionRule("go test")`, `ActivateSkill("tdd")`, `SetToolBudget({Used: 3, Max: 100})`, `AddLoadedToolNames([]string{"web.fetch"})` and `SetSandboxInfo({Backend: "restricted"})`;
     - `state.Config.Tools.Shell.AllowNetwork` set to true;
     - a context pack with two sections, one of them with no tokens.

     `Sheet(0)` then equals a literal:
     - `Tools` holds `file.read`, `file.write_patch` and `shell.run`, one call each, the last with one error and 50 ms;
     - `Files` holds `b.go` (one edit) and then `a.go` (one read), with their `LastAt` values;
     - the rules, the skills, the budget, `LoadedTools` 1, the sandbox, `StartedAt` (`100000`, the state's `time.Unix(100, 0)`) and both context sections are set.
   - `TestSheetReadsASubagentsOwnActivity`:
     - the parent logs `parent-probe` and has a context pack. A child logs `child-probe` and has the rule "child rule". Register the child with `RegisterSubagent("scout", child)`;
     - `Sheet(view.ID)` has `SubagentID` equal to the view's ID, only `child-probe` in `Tools`, `Rules` `[child rule]`, the child's `StartedAt`, and an empty `Context`.
   - `TestSheetFindsANestedSubagent`: a grandchild registered on the child, logging `grandchild-probe`. `Sheet(grandchildID)` has that ID and that tool.
   - `TestSheetReadsAnUnknownSubagentAsTheSession`: `Sheet(999999)`, and `Sheet(card.ID)` for a card registered with a nil child, both have `SubagentID` 0 and the parent's tools.
   - `TestSheetOfAReplacedSessionKeepsItsFinalValues`: after `Host.Swap`, the old session's `Sheet(0)` still lists the tool its state logged.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/acpclient/...
```

Expected: the five new tests pass. `TestMethodTableCoversTheInterfaces`, `TestMethodDocsNameTheirACPMethod` and the server's table test pass with the new row.

## Task 4: The sheet refresh

**Goal:** the adapter keeps the sheet's database and git values, re-reads them on `RefreshSheet`, and returns them from `Sheet`. `Status` carries the changed-file count. `Run` hands the adapter the project ID.

**Files:** `internal/acpclient/acpclient.go`, `internal/acpclient/methods.go`, `internal/acpclient/inproc/inproc.go`, `internal/acpclient/inproc/events.go`, `internal/acpclient/inproc/sheet.go`, `internal/acpclient/inproc/sheet_test.go`, `internal/app/app.go`.

**Steps:**

1. **`acpclient.go` and `methods.go`.** Add `RefreshSheet` to `Session` after `Sheet`, with the doc from Contracts, and its row to `Methods`.
2. **`Deps`** gains `ProjectID` (Contracts). **`Session`** gains `projectID`, `headSHA` and `sheet` (Contracts). `newSession` (`events.go:17`) sets `projectID` from `d.ProjectID` and `headSHA` to `gitinfo.HeadSHA`. `Swap` already copies the host's `Deps`, so a swapped-in session keeps the project ID.
3. **`RefreshSheet`** follows decision 6:
   1. A closed session returns `errSessionClosed`.
   2. Read the workspace once. Its active root is the root for the changed files. Its project root, or `s.state.WorkingDir` when that is empty, is the root for the fleet.
   3. The changed files, when `Rebase` or `Changed` is set and the active root is not empty:
      - take the cached `base` and `baseRoot` under the mutex;
      - rebase when `Rebase` is set, `base` is "", or `baseRoot` is not the active root;
      - on a rebase, read `s.headSHA(root)`. An empty result skips the changed files. Otherwise, read `changedfiles.Read(root, head)` and keep the result together with the new base;
      - without a rebase, read `changedfiles.Read(root, base)`.
   4. The stats, when `Stats` is set and `s.state.DB()` is not nil:
      - `RecentTurnMetrics(s.projectID, 24)`;
      - `SessionUsage(s.projectID, s.state.SessionID())`;
      - when `s.projectID` is not 0, `CountFiles` and `CountSymbols`.
   5. The fleet, when `Fleet` is set: `worktree.CLIGitOps{}.RevParse(fleetRoot, "HEAD")`, then `worktree.ListFleet(worktree.CLIGitOps{}, fleetRoot, base)`. Either error gives an empty fleet.
   6. Read the active root again. Then, under the mutex:
      - return `errSessionClosed` if the session closed meanwhile, storing nothing;
      - store the changed files (and, on a rebase, the new base and its root) only when the active root still equals the one read in step 2. A read without a rebase is also dropped when `base` or `baseRoot` no longer match the values taken in step 3;
      - store each stats value whose query succeeded. The repo counts are stored only when both counts succeeded;
      - store the fleet whenever `Fleet` was set.
   7. Call `signal`, and return nil.
4. **Conversions** (in `sheet.go`). Each returns nil for no input:
   - `changedOf([]changedfiles.File) []acpwire.SheetChangedFile`: `Status` is `string(f.Status)`;
   - `turnsOf([]db.TurnMetricsRow) []acpwire.SheetTurn`: `StartedAt` through `acpwire.Millis`;
   - `totalsOf(db.UsageTotals) acpwire.SheetTotals` copies its four fields;
   - `fleetOf([]worktree.FleetRow) []acpwire.SheetFleetRow`: `AgeMs` is `Age.Milliseconds()`. `Path` and `Session` are not carried.
5. **`Sheet`** reads the cache under the mutex:
   - every sheet gets `Fleet` and `Repo`;
   - the session's own sheet (ID 0) also gets `Changed`, `Turns` and `Totals`.
6. **`Status`** sets `ChangedFiles` to the number of cached changed files, read under the mutex.
7. **`app.go:2807`.** `inproc.New` gets `ProjectID: projectID`.
8. **`sheet_test.go`** (T5). The git tests skip when `git` is not on `PATH`. Each git test builds its session with `New(Deps{State: session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: "s1"})})`, whose active root is `repo`. The others use `newState` (`inproc_test.go:23`) unless they say otherwise.
   - `TestRefreshSheetRebasesOntoHead`:
     - write "one\ntwo\n" to `a.txt`. A `Rebase` refresh gives `Changed` `[{a.txt M 1 0}]` and `Status().ChangedFiles` 1;
     - commit it. A second `Rebase` gives no changed files and a count of 0.
   - `TestRefreshSheetChangedKeepsTheBase`:
     - a `Rebase` on the clean repository first;
     - then change and commit `a.txt`. A `Changed` refresh still lists `a.txt`, because it diffs against the earlier base;
     - a `Rebase` then lists nothing.
   - `TestRefreshSheetChangedRebasesWithoutABase`: on a fresh session, change `a.txt`. A `Changed` refresh lists it.
   - `TestRefreshSheetKeepsTheCacheWhenHeadIsUnreadable`:
     - change `a.txt`, and `Rebase` (one file);
     - then `git symbolic-ref HEAD refs/heads/unborn`. A `Rebase` still lists `a.txt`.
   - `TestRefreshSheetDropsAReadForAMovedRoot`:
     - change `a.txt`;
     - replace `sess.headSHA` with a function that calls `state.SetWorkspace` with `ActiveRoot` set to another temporary directory before it returns `gitinfo.HeadSHA(dir)`;
     - a `Rebase` stores nothing: `Sheet(0).Changed` is empty and `ChangedFiles` is 0.
   - `TestRefreshSheetReadsTheActiveRoot` ports the TUI's `TestRefreshSheetChangedUsesActiveRoot` (`status_test.go:644`):
     - a linked worktree `wt` on branch `feat-x`, with `a.txt` changed in it and `main-only.txt` written in the main checkout;
     - `SetWorkspace` to `{ProjectRoot: repo, ActiveRoot: wt, Branch: "feat-x"}`, then `Rebase`;
     - the changed files hold `a.txt` and not `main-only.txt`.
   - `TestRefreshSheetReadsTurnsTotalsAndRepoCounts`:
     - open a database in `t.TempDir()`, `Migrate`, and create the project and session "s1", as `TestBuildCountsParseFailuresFromTurnMetrics` (`postmortem_test.go:92`) does;
     - insert two turn rows with `ProjectID` set and `SessionID` "s1", the older first: `{StartedAt: time.Unix(1000, 0).UTC(), PromptTokens: 100, CompletionTokens: 20, CacheReadTokens: 50}` and `{StartedAt: time.Unix(2000, 0).UTC(), PromptTokens: 200, CompletionTokens: 40}`. `RecentTurnMetrics` orders by row ID, newest first;
     - `SaveFileIndex` two files (`a.go` and `b.go`, each with a language, a hash and a `LastIndexedAt`), and `SaveSymbols` three symbols (each with a file path, a kind and a name);
     - build the state with `Persistence{DB: database, SessionID: "s1"}`, and the adapter with `ProjectID`;
     - after a `Stats` refresh, `Sheet(0)` has `Turns` `[{2000000 200 40 0} {1000000 100 20 50}]`, `Totals` `{2 300 60 50}` and `Repo` `{2 3}`.
   - `TestRefreshSheetReadsTheFleet`: `git worktree add -b agent/x <repo>/.marshal/worktrees/x`. After a `Fleet` refresh, `Sheet(0).Fleet` holds one row with branch `agent/x`.
   - `TestRefreshSheetFiresChanged`: a `Stats` refresh on a state with no database fires `Changed` (`waitChanged`, `events_test.go:17`).
   - `TestRefreshSheetOnAReplacedSessionFails`: after `Host.Swap`, the old session's `RefreshSheet` fails with `acpwire.CodeSessionClosed` (`acpclient.IsCode`), as `sendnow_test.go:93` checks.
   - `TestASubagentSheetHasTheSessionsFleetAndRepo`:
     - register a child with `RegisterSubagent("scout", child)`;
     - seed `sess.sheet` directly with one changed file, one turn, totals, one fleet row and repo counts;
     - `Sheet(view.ID)` has that fleet and those repo counts, and no changed files, turns or totals.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/acpclient/... -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2c-suite go test -race ./internal/acpclient/inproc/
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/acp/ -run 'TestACPClientMethodTableMatchesTheServer'
go vet ./internal/acpclient/... ./internal/app/
```

Expected: the eleven new tests pass, the race run is clean, and `TestStatusMirrorsTheState` still passes: its want has `ChangedFiles` 0.

## Task 5: The changed files and HEAD move to the session

**Goal:** the status line's count and the sheet's changed files come from the session. The TUI's base-ref command, message, handler and two caches are deleted.

**Files:**
- `internal/app/tui/pump.go`, `events.go`, `model.go`, `commands_dispatch.go`, `status.go`;
- `internal/app/tui/sessionsheet/section.go`, `section_changed.go`, `section_changed_test.go`;
- `internal/app/tui/seam_test.go`, `sessionsheet_wiring_test.go`, `workspace_msg_test.go`, `status_test.go`, `model_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`sessionsheet`.**
   - `Data.Changed` becomes `[]acpwire.SheetChangedFile` (Contracts).
   - Delete `ChangedFile` with its comment (`section.go:28–34`).
   - In `ChangedSection.Render`, the marker is `f.Status`, already a string.
   - In `section_changed_test.go`, the fixtures' `Status` values become the strings "M" and "A".
2. **`pump.go`.**
   - Delete `sheetBaseRefCmd` and `sheetChangedFiles` with their comments (45–75).
   - Add `refreshSheetCmd` (Contracts). It calls `sess.RefreshSheet(ctx, req)`, ignores the error and returns nil. A failed refresh leaves the cache as it was, as a failed git read does today.
   - The imports lose `sessionsheet`, `changedfiles` and `gitinfo`, and gain `context`, `acpclient` and `acpwire`.
3. **`events.go`.** Delete `sheetBaseRefMsg` with its comment (60–71). The import of `sessionsheet` goes.
4. **`model.go`:**
   - Delete the fields `sheetBaseRef` and `sheetChanged` with their comments (565–570).
   - In `New`, delete the HEAD read (1783).
   - In `Init` (1824), the base-ref block becomes an unconditional `refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true})`, in the same place in `cmds`, before the change pump. The comment above it says the session reads HEAD and the changed files off the UI thread.
   - Delete `refreshSheetChanged` (1882–1886). In `refreshSheetCaches` (1966), its call becomes `_ = m.sess.RefreshSheet(m.ctx, acpwire.SheetRefresh{Changed: true})`. That call is synchronous, as the read was.
   - In `sheetData` (1984), `Changed` is `m.sess.Sheet(0).Changed`.
   - In `handleAgentFinished`:
     - the comment at 5435 says the changed files are refreshed off-thread by the refresh returned below;
     - at 5479, `sheetBaseRefCmd(…)` becomes `refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true})`;
     - the comment above that line (5476–5478) says the session rebases the changed files onto the active root's HEAD, so committed agent work stops inflating the diff.
   - In `handleChanged`, `sheetBaseRefCmd(st.Workspace.ActiveRoot)` (5623) becomes `refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true})`. Keep the guard on an empty active root.
   - Delete `handleSheetBaseRef` with its comment (6470–6492). Remove `sheetBaseRefMsg` from the runtime list (2505), and delete its case in `dispatchRuntimeMessage` (6597–6598).
   - The imports lose `changedfiles` and `gitinfo`.
5. **`startNewSession`** (`commands_dispatch.go:188`):
   - delete the clearing of `m.sheetChanged` and its comment (233–235). A replaced session's cache starts empty;
   - the comment at 236 says the workspace follow starts from the new session's workspace;
   - `sheetBaseRefCmd(st.Workspace.ActiveRoot)` (250) becomes `refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true})`. It is built after `m.sess` is replaced, so it reads the new session.
6. **`status.go:344–352`.** The count is `st.ChangedFiles`. The comment says the session refreshes it on turn boundaries and workspace moves, never during render.
7. **Tests:**
   - **`seam_test.go`**: `stubSession` gains `onRefreshSheet` and `RefreshSheet` (Contracts).
   - **`model_test.go`**:
     - `collectSheetBaseRefs` (1701) becomes `runCmdChain` (Contracts): the same walk, returning every message that is neither a batch nor a sequence;
     - `TestAgentFinishedAdvancesSheetBaseRef` (1742) and `TestAgentFinishedAdvancesSheetBaseRefWhenCancelled` (2033) keep their names and their busy and cancelling checks. Before `handleAgentFinished`, each:
       - refreshes with `Rebase` on the clean repository;
       - writes and commits a change to `a.txt`;
       - checks that a `Changed` refresh lists `a.txt` against the old base.

       After `runCmdChain` of the returned command, `Sheet(0).Changed` is empty: the turn end rebased onto the new HEAD;
     - `TestNewSessionRereadsGitInfo` (2784):
       - in place of pre-setting a stale list, write an uncommitted change to `a.txt` in `dir` before `/new`;
       - after `/new`, `m.sess.Status().ChangedFiles` is 0, and after `batchResults` of the command it is 1;
       - drop the base-ref assertions and keep the branch check;
     - `TestNewSessionAcceptanceBuildsTheReplacement` (9479) keeps its non-nil command check, whose message now reads "the replacement must return its commands". It drops the search for a base-ref message, because `TestNewSessionRereadsGitInfo` covers the refresh.
     - `runCmdChain`'s comment no longer names the base-ref command.
   - **`sessionsheet_wiring_test.go`**:
     - `TestCtrlBOpensAndClosesSessionSheet` (55) checks `m.sess.Status().ChangedFiles` is 1 where it checked the cache;
     - `TestInitLoadsChangedFilesOffThread` (246) checks the count is 0 after `New`, then runs `runCmdChain(t, m.Init())` in place of collecting and feeding the message. Its status-line check is unchanged;
     - `TestTurnEndDoesNotDiffSynchronously` (271) checks the count is 0 straight after `agentFinishedMsg`, and 1 after `runCmdChain` of the command. Its comment (268–270) says the diff arrives with the session's off-thread refresh;
     - add `TestOpeningTheSheetRefreshesIt`: a `stubSession` records requests, and `ctrlB` records exactly `[{Changed: true}]` (Task 6 extends it);
     - add `TestTurnEndRebasesTheSheet`: with the sheet closed, `handleAgentFinished` records nothing synchronously, and `runCmdChain` of its command records exactly `[{Rebase: true}]`.
   - **`workspace_msg_test.go`**:
     - `TestChangedRebasesTheSheetWhenTheWorkspaceMoves` runs `batchResults` of `handleChanged`'s command, then checks that `m.sess.Sheet(0).Changed` holds the worktree's `a.txt`. Drop its ref and dir checks: the adapter's `TestRefreshSheetReadsTheActiveRoot` covers them;
     - `TestChangedLeavesTheSheetWhenTheWorkspaceStays` builds its model with `testModelStub`. The first change records one `Rebase` request, the second records none. Its state checks stay;
     - delete `TestHandleRailBaseRefStaleDirIgnored`, `TestHandleRailBaseRefEmptyRefKeepsBase` and `TestHandleRailBaseRefRebasesAndRefreshesCache`. They are covered by the adapter's `TestRefreshSheetDropsAReadForAMovedRoot`, `TestRefreshSheetKeepsTheCacheWhenHeadIsUnreadable` and `TestRefreshSheetRebasesOntoHead`;
     - the `sessionsheet` import goes.
   - **`status_test.go`**:
     - delete `TestRefreshSheetChangedUsesActiveRoot` (644), which Task 4 ported;
     - add `newStatusTestModelStub(t *testing.T, stub *stubSession) Model`, which does what `newStatusTestModel` (23) does, but over `testModelStub`;
     - `TestStatusLineShowsChangedFileCount` (771) and `TestStatusLineChangedFileCountDropsBeforeRoute` (787) use it. Their stub's `patch` sets `ChangedFiles` from a local variable, which the first test changes from 0 to 3 to 1 between its checks, and the second sets to 1;
     - `TestChangedFileCountAppearsAfterTurn` (807) runs `runCmdChain(t, cmd)` in place of feeding the collected messages.
8. **The allowlist** (`boundary_allowlist_test.go`): delete `marshal/internal/repo/changedfiles` and `marshal/internal/repo/gitinfo` from both `model.go`'s and `pump.go`'s entries. Never add an entry: a "uses …, which is agent side" error means this task left an agent-side call behind.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'sheetBaseRef|sheetChanged\b|refreshSheetChanged|sheetChangedFiles|collectSheetBaseRefs|handleSheetBaseRef' --include='*.go' internal/app
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/app/tui/ -run 'Sheet|Changed|Rebase|NewSession|AgentFinished|StatusLine|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/tui/...
```

Expected: the grep prints nothing, the named tests pass, and both packages fail only on Task 8's baseline list.

Then run `./marshal` (built with `CGO_ENABLED=1 go build ./cmd/marshal`) in this repository with an uncommitted change, and check by eye: the status line shows `±N files` from the first frame on, and Ctrl+B's CHANGED section lists the files.

## Task 6: The rest of the sheet reads the session

**Goal:** every sheet section renders wire values from `Session.Sheet`, and the TUI's turn, totals, fleet and repo caches are deleted. `sessionsheet` imports no agent-side package.

**Files:**
- `internal/app/tui/sessionsheet/section.go`, `section_session.go`, `section_repo.go`, `section_tools.go`, `section_workingset.go`, `section_context.go`, `section_worktrees.go`, `telemetry.go`, and `filestats.go` (deleted);
- their tests: `section_session_test.go`, `section_repo_test.go`, `section_tools_test.go`, `section_workingset_test.go`, `section_context_test.go`, `section_worktrees_test.go`, `telemetry_test.go`, and `filestats_test.go` (deleted);
- `internal/app/tui/model.go`, `seam_test.go`, `sessionsheet_wiring_test.go`, `boundary_allowlist_test.go`.

**Steps:**

1. **`section.go`.**
   - `Data` is as in Contracts.
   - Delete `RepoStats` and `LanguageShare` with their comments (14–26).
   - `Data`'s doc says the Model assembles it from `Session.Sheet` and `Session.Status` on every frame, that sections never reach outside it, and that the session caches everything expensive off the render path. The per-field comments that described the old sources go.
2. **Sections.** Each keeps its rendering and its strings:
   - `section_session.go`:
     - `Relevant` is `!d.StartedAt.IsZero()`;
     - `sessionElapsed` is 0 for a zero `StartedAt`, and otherwise `d.Now.Sub(d.StartedAt)`;
     - `budgetLabel` reads `d.ToolBudget`, and loses its nil-state check: a zero budget already gives "";
     - `cachedShare` takes `acpwire.SheetTotals`.
   - `section_repo.go`:
     - delete the language row (the `d.Repo.Languages` block);
     - the sandbox and loaded-tools rows render when `!d.StartedAt.IsZero()`, in place of `d.State != nil`. They use `d.Sandbox.Backend`, the OK glyph when `d.Sandbox.AllowNetwork`, and `d.LoadedTools`.
   - `section_tools.go`:
     - delete `ToolStat` and `ToolStats` (14–54);
     - `Relevant` is `len(d.Tools) > 0`;
     - `Render` iterates `d.Tools` in order. It finds the slowest by `SlowestMs` and prints it in seconds with the same `%.1fs`;
     - `OneLine` counts calls as the sum of `Calls`, failures as the sum of `Errors`, and the slowest as the largest `SlowestMs`.
   - `section_workingset.go`: `Relevant` is `len(d.Files) > 0`. `Render` and `OneLine` read `d.Files`, in order.
   - Delete `filestats.go`. `hasFileActivity` goes with it (decision 4).
   - `section_context.go`: `Relevant` is `len(d.Context.Sections) > 0`. `Render` and `OneLine` read `d.Context`'s `MaxTokens`, `EstimatedTokens` and `Sections`.
   - `telemetry.go`: `Telemetry` takes `[]acpwire.SheetTurn` (Contracts). The oldest start is `acpwire.Time(turns[len(turns)-1].StartedAt)`.
   - `section_worktrees.go`: both `strutil.HumanAge` calls take `time.Duration(w.AgeMs) * time.Millisecond`.
   - Drop each file's unused imports. After this step, no file in `sessionsheet` imports `session`, `contextpack`, `db`, `registry` or `worktree`.
3. **`sessionsheet` tests.** Each test keeps its name and its assertions. Its fixture becomes the wire value the old one produced: an audit log becomes the `SheetTool` or `SheetFile` rows `SheetToolsOf` or `SheetFilesOf` would return for it, in that order.
   - `section_tools_test.go`:
     - delete `TestToolStatsAggregates`, `TestToolStatsSortedByCallsDescending` and `TestToolStatsEmpty`, which Task 2 copied;
     - `toolEvents()` becomes `toolStats()`: `file.read` 2 calls with 20 ms, `shell.run` 2 calls with 1 error and 4200 ms, `file.write_patch` 1 call with 5 ms.
   - `section_workingset_test.go`: `wsData` takes `...acpwire.SheetFile` and sets only `Files`. For example, the three `a.go` writes in `TestWorkingSetShowsEditCount` become `{Path: "a.go", Edits: 3}`.
   - Delete `filestats_test.go`, which Task 2 copied.
   - `section_context_test.go`:
     - `ctxData` builds `Context` with the same four sections and sizes;
     - `packWithUsage` becomes `contextWithUsage(used, max int) acpwire.SheetContext`;
     - the turn fixtures are `SheetTurn` values with `StartedAt` through `acpwire.Millis`.
   - `telemetry_test.go`: the same turns as `SheetTurn` values, with `StartedAt` through `acpwire.Millis`.
   - `section_session_test.go`:
     - `sessionData(now)` is `Data{Now: now, StartedAt: now.Add(-90 * time.Second), Totals: acpwire.SheetTotals{Turns: 7, PromptTokens: 42_100, CompletionTokens: 8_300}}`;
     - `TestSessionSectionBudgetRow` sets `d.ToolBudget` to `{Used: 7}`, then to `{Used: 7, Max: 100}`;
     - the other `db.UsageTotals` fixtures become `acpwire.SheetTotals`.
   - `section_repo_test.go`:
     - `repoData`'s `Repo` is `acpwire.SheetRepo{Files: 412, Symbols: 3100}`;
     - `TestRepoSectionRender` no longer expects "go";
     - add `TestRepoSectionShowsTheSandboxForASession`: with `StartedAt` set, `Sandbox{Backend: "restricted", AllowNetwork: true}` and `LoadedTools` 3, the rows contain "sandbox restricted · net ✓" and "3 tools loaded". With a zero `StartedAt`, the rows contain neither.
   - `section_worktrees_test.go`: the rows are `SheetFleetRow` values, without `Path`, and with `AgeMs` from the same durations.
4. **`model.go`:**
   - Delete the fields `sheetRepoStats`, `sheetTurns`, `sheetTotals` and `sheetFleet` with their comments (557–564, 571–574).
   - In `New`, delete the repo-count block (1794–1801).
   - Delete `refreshSheetTurns`, `refreshSheetFleet` and `refreshSheetCaches`. In `openSessionSheet` (1932), the `refreshSheetCaches()` call becomes `_ = m.sess.RefreshSheet(m.ctx, acpwire.SheetRefresh{Changed: true, Stats: true, Fleet: true})`, still before the panel is built. The function's comment says the session re-reads the sheet's cached values once on open.
   - Delete `sheetData`, `drilledSheetState` and `childSheetData`, and add `drilledSheetID` (Contracts). It returns the top view's `ID`, or 0 for an empty view stack. A view on the stack always has a child, because `drillIntoSubagent` refuses the others (4164), and an ID with none reads as the session in any case.
   - `sheetDataFor`:
     - take `st := m.sess.Status()` and `sh := m.sess.Sheet(m.drilledSheetID())`;
     - copy each of `sh`'s values into its `Data` field, with `StartedAt` through `acpwire.Time`;
     - add `Git` from `st`, `Spinner` from `m.turnSpinnerFrame()` and `Now` from `m.now()`;
     - set `Swarm` and `SDD` from `st` only when `sh.SubagentID` is 0.

     Its comment says the adapter picks the subagent's sheet and falls back to the session's.
   - In `handleAgentFinished`:
     - replace the block that refreshed the turns and the fleet (5435–5441) with `sheetOpen := m.sheetOpen()`. The comment says an open sheet's turn and fleet values are refreshed off the UI thread with the rebase below;
     - the refresh at 5479 becomes `acpwire.SheetRefresh{Rebase: true, Stats: sheetOpen, Fleet: sheetOpen}`.
   - The import of `worktree` goes. `db` stays, for `WithMemoryStore`.
5. **TUI tests:**
   - **`seam_test.go`**: `stubSession` gains `sheetPatch` and `Sheet` (Contracts).
   - **`sessionsheet_wiring_test.go`**:
     - `TestSheetDataCarriesTotals` (141) builds its model with `testModelStub`. A `sheetPatch` sets `Totals` to `{Turns: 5, PromptTokens: 1000, CompletionTokens: 200}`, and `sheetDataFor().Totals` carries them;
     - `TestNilChildCardCannotDrillIn` (152) checks that `drilledSheetID()` is 0;
     - `TestSheetDataChildScopedDuringDrillIn` (170) reads the names in `sheetDataFor().Tools`;
     - add `TestChildSheetDataLeavesOutTheRunProgress`: with a swarm goal set on the parent, `sheetDataFor().Swarm.Goal` is the goal before the drill-in and "" during it;
     - `TestOpeningTheSheetRefreshesIt` now expects `[{Changed: true, Stats: true, Fleet: true}]`;
     - `TestTurnEndRebasesTheSheet` gains a case with the sheet open. Open it with `ctrlB`, then clear what the stub recorded. `handleAgentFinished` records nothing synchronously, and `runCmdChain` records exactly `[{Rebase: true, Stats: true, Fleet: true}]`.
6. **The allowlist**:
   - delete the `sessionsheet/filestats.go`, `sessionsheet/section.go`, `sessionsheet/section_session.go`, `sessionsheet/section_tools.go` and `sessionsheet/telemetry.go` keys;
   - delete `marshal/internal/worktree` from `model.go`'s entry.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'sheetRepoStats|sheetTurns|sheetTotals|sheetFleet|refreshSheet(Turns|Fleet|Caches)|childSheetData|drilledSheetState|ToolStats\(|FileStats\(|LanguageShare' --include='*.go' internal/app/tui
grep -rnE 'd\.State\b|"marshal/internal/(app/session|contextpack|db|tools/registry|worktree)"' internal/app/tui/sessionsheet
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/app/tui/sessionsheet/ ./internal/app/tui/ -run 'Sheet|Section|Telemetry|Sparkline|Bar|Context|WorkingSet|Repo|Tools|Worktrees|Changed|TUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/app/tui/... ./internal/app/
go vet ./internal/app/tui/...
```

Expected: both greps print nothing, the named tests pass, and both packages fail only on Task 8's baseline list.

Then run `./marshal` in this repository, run a turn that calls a few tools, and open Ctrl+B. Check by eye:
- CONTEXT shows its bar and sections;
- TOOLS and FILES list the turn's calls;
- the footer shows the turn count;
- REPO shows the file and symbol counts and the sandbox row.

Drill into a subagent and check that the sheet shows the child's tools.

## Task 7: The guard

**Goal:** T1b keeps the sheet's reads moved.

**Files:** `internal/app/tui/moved_reads_test.go`.

**Steps:**

1. **`movedStateReads`** gains these seven names, each mapped to `"2c"`, in a block after the 2b names: `AuditLog`, `ContextPack`, `SessionRules`, `ActiveSkills`, `ToolBudget`, `LoadedToolNames` and `StartedAt`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-2c-suite go test ./internal/app/tui/ -run 'TestMovedStateReadsStayMoved|TestMovedStateReadsAreRealNames|TestMovedReadsGuardFindsSelectors|TestTUIStaysOnTheClientSeam' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go vet ./internal/app/tui/
```

Expected: all four pass.

As a check of the guard (no commit), temporarily add `_ = m.state.AuditLog()` to `sheetDataFor`. `TestMovedStateReadsStayMoved` should fail with the T1b string, naming `model.go` and plan 2c. Then revert it.

## Task 8: Verification and docs

**Goal:** the full suite matches the baseline, and the spec and design record plan 2c.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 2c entry to the Implementation record. It lists:
   - the wire types, `ChangedFiles`, `Sheet` and `RefreshSheet`;
   - the aggregation moved to `viewmodel`;
   - the adapter's cache and refresh rules, in one sentence;
   - the deleted TUI command, message, handler and caches, and the deleted language mix;
   - the seven names added to T1b;
   - the allowlist entries Tasks 5 and 6 removed: 14 entries across 7 files, 5 of them keys dropped entirely.
2. **`design.md` §7.** The P2 row's status adds `plan 2c implemented` after `plan 2b implemented`.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-2c-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-2c-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
```

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the failures are exactly the baseline below. A flaky one counts only if it passes alone with `-count=3`.

**Baseline at `726e1506`**, from a full run with `-timeout 20m`:
- `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree` and `TestSubagentShellOutputDoesNotReachParent`.
- `internal/app`: `TestDefaultPromptBudget` fails alone too: `tool schemas are 15322 bytes, over the 15000 ceiling`. It has failed since `0cad39ae`. This plan changes no schema.
- `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce` is intermittent. It failed in this run, as in earlier ones. It counts as pre-existing whether it passes or fails.
- `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`.
- `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`.
- `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`.
- `internal/snapshot`: `TestRunManagedCancellationKillsTree` failed in this run and 2 of 3 times alone. It counts as pre-existing whether it passes or fails.
- `internal/snapshot`: `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` failed in this run with `Release: probe process group …: operation not permitted`, and passed 3 of 3 alone. It is flaky under load.
- Known to be flaky under load, and passing in this run: `TestRuntimeCloseRunsNoGitGC` (`internal/app`), `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).
- `internal/acp` passed in 13 s. `web/bridge` passed in 108 s.

Finally, run `./marshal` in this repository and check by eye:
- the status line's `±N files` after a turn that edits a file;
- Ctrl+B's sections, as at the end of Task 6;
- `/new`: the new session's status line and sheet show the working tree's changes, not the old session's.

## Integration notes

- **Plan 3** moves the transcript and the drill-in. `drilledSheetID` then reads the wire view stack. `childState` in `inproc/sheet.go` is the lookup `Stack(subagentID)` needs too.
- **Plan 5** moves turns behind `Prompt`. The adapter can then refresh the sheet at turn end itself, and the TUI's turn-end `refreshSheetCmd` goes.
- **Plan 8** moves the memory store. `WithMemoryStore`'s project ID and `Deps.ProjectID` then come from the same place.
- **P3** gives `session/sheet` a handler in `acp`. The conversions in `inproc/sheet.go` must then move to a package `acp` can use, as `SheetToolsOf` and `SheetFilesOf` already have.

## Self-review

- Every task commits alone.
  - Tasks 1 to 4 are agent side, and each builds on the one before.
  - Task 5 moves the changed files while the TUI's other sheet caches still run, so the sheet keeps working.
  - Task 6 moves the rest once the adapter has every value.
  - Task 7 locks the reads in.
- Tasks 3 and 4 each touch `acpclient`, `inproc` and, in Task 4, one wiring line in `app.go`. Adding a method to `Session` breaks the build until its one implementation has it.
- Tasks 5 and 6 each span `tui` and `sessionsheet`, because the sheet's `Data` changes type with its source. Each has its decisions pinned, and in `sessionsheet` the changes are type swaps.
- Cold-executor check:
  - every trigger point is in decision 1's table and has a named line;
  - every refresh rule is in decision 6, with the test that pins it;
  - every deleted test names what covers its behaviour now;
  - every changed test names its new mechanism (`runCmdChain`, `stubSession`'s `onRefreshSheet` and `sheetPatch`, `batchResults`);
  - every allowlist entry to delete is named in the task that stops needing it.
- Contracts hold every cross-task declaration: the wire types, the aggregation functions, the interface methods, the adapter's `Deps` and cache, the TUI's new and changed signatures, and the test helpers.
- Anchors were checked at `726e1506`. The baseline was taken there in a detached worktree.
- Nothing here contradicts the spec. Decisions 1 to 5 are written into its revision record, and C5 and the Migration order were updated to match.
