# P-C live box — implementation plan

Date: 2026-10-24, re-checked 2026-10-25. Status: ready for review.
Base: `main` at `d7d9b39c`, after P-B merged (`24792d61`).
- Every anchor was re-checked at `d7d9b39c`, including P-B's `SplitLive`, `KindCompletion`, `CompletionInfo`, `renderCompletion`, `WireTool.SubagentID`, and the streaming-reasoning node as the newest turn's last child.
- Task 6's test list was measured, not guessed. The viewport was made to render only `SplitLive`'s history, in a throwaway copy, and the TUI suite was run. Every test that failed is named in Task 6.

Approved design: [feature-spec.md](../feature-spec.md) §5.1, §5.5 to §5.8, §6.3, §6.5, §7 (P-C) and §8. The spec was updated on 2026-10-24 with the decisions below.
Previous phase: [P-B plan](2026-10-24-p-b-lifecycle-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations. Every step is an instruction against checked anchors. Executing it also tests how well Marshal follows a plan, so each step states its decision. "As `X` does" means `X` exists and is the model to copy.

**Goal:** everything that can still change lives in one bordered box above the input. History only grows above it.
- The box holds the live beat (with the events of the work it launched and the streaming reasoning), the running work, and the plan. Running work rows carry a measured bar.
- The plan has three layouts that Ctrl+T cycles (stacked, split, folded). `[tui.live_box] layout` sets the starting one.
- The box takes at most 40% of the frame and sheds in a fixed order.
- The now bar, the Tasks panel's Plan tab and the Work list's acknowledgement go.
- A skill loaded by the agent shows once: as its call row.

```
╭─ ⠋ working 4m12s ───────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│ ⠋ Checking the eval thresholds still make sense for a coder model.        │
│   ├ read    evals/thresholds.toml                                22 lines │
│   └ edit    evals/thresholds.toml                                    ⠋ 2s │
├─ running 2 ───────────────────────────────────────────────────────────────┤
│ ▌⧉ #1     review the config change    Checking context_window…  1m02s  ⠋ │
│ ▌┆ job-1  Download qwen3-coder        ━━━━━━━╸──── 57%  2.1/3.7 GiB  ⠋   │
├─ plan  ▰▰▱▱ 2/4 ──────────────────────────────────────────────────────────┤
│ ✓ Start the weights download      ▸ Run the eval harness unit tests 0m31s │
│ ✓ Point evals/config.toml at…     · Run the eval suite on media-box       │
╰───────────────────────────────────────────────────────────────────────────╯
```

(The spec's frame. It is drawn by hand; Task 5's frame tests record the real one.)

**Non-goals:**
- The web UI: it keeps its now bar, which reads the band.
- P-D's skill.
- Keyboard design: every key stays provisional.
- The status line, apart from the browser URL, which it now always carries (decision 6).

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/pc-suite`. Create it first: `mkdir -p /private/tmp/pc-suite`.
- **Pre-existing failures,** measured at `d7d9b39c` with `TMPDIR=/private/tmp/...`:
  - `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestSubagentShellOutputDoesNotReachParent`, `TestAccessTUIOptionsResolveWithoutConfigReload`, `TestDefaultPromptBudget` and `TestRuntimeNewAndResumedSessionResetAccess`;
  - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`, called "the known TUI failure" below;
  - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`;
  - `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`;
  - flaky under full-suite load, passing when run alone: `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`), and `TestACPChildApprovalUsesOwningSessionRegistry` (`internal/acp`).

  A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`.
- The app always attaches the work service (`tui.WithWork(rt)` in `internal/app/app.go`), so the live-work snapshot is the only source of running work the box needs.

**Decisions this plan makes beyond the spec** (all now recorded in the spec):
1. **The skill duplicate.**
   - An explicit `skill.load` writes a hidden body message and a one-line tag message beside its call row. Neither message carries a step ID, so each becomes a turn-level block. The tag draws a second row under the beat, and both end the beat early, so later calls start a headless beat.
   - The projection drops the hidden body always. It drops a tag when the same turn holds a successful `skill.load` of that skill.
   - Tags from a load with no call row (the skills panel, an ACP client) stay.
   - Old transcripts are fixed too, because nothing in the session changes.
2. **Rate and ETA were never measured.** `Model.workPrevious` is declared and read, but nothing writes it, so no card ever had a measured rate or ETA. Task 2 records the previous observation on each snapshot.
3. **Running work comes only from the work snapshot.** The legacy lane (subagents, jobs and watches read from the session when no work service is attached) goes. Tests feed snapshots.
4. **The tint is painted at every depth.**
   - Running rows are tinted with `BGSurface` even at depth `flat`, as the one exception to "flat paints nothing". The spec makes the tint the mark of running work, and `flat` is the default.
   - Under NO_COLOR the `▌` edge, the glyph and the id carry it.
5. **The box's top border** holds the turn state and the hints.
   - The state is the spinner and `working <elapsed>`, plus the activity label when there is one. During an SDD run it is the run's summary instead, and during a swarm the swarm's.
   - The hints are `Ctrl+T plan` when a plan exists and `Ctrl+C ×2 stop` while busy.
   - When not busy the border carries no state.
   - Correction to the spec: the status line never held a busy timer. The now bar's turn row did, and that row is what moves into the border.
6. **The browser session** shows in the status line only. The box never draws it, so the status line always carries the URL while a session is open.
7. **Clicks and keys in the box.**
   - A click on a running row expands or collapses its card. A click on a plan item scrolls history to the todo's first beat. A click on `+N more · /work` opens the Work list. A click on a live call row expands it.
   - In browse mode, `j` past the last history row enters the box (running rows, then the more row, then plan items).
   - On a box stop, Enter expands a card, `s` requests a stop when the item allows it, and `f` drills into an agent.
   - The live part's rows are click targets but not browse stops. `G` always resumes following.
8. **A finished plan is one row:** the plan section's header alone while busy, or the idle row `✓ Plan 4/4 done`.
9. **Go test rows show `N passed · N failed`**, plus `N skipped` when nonzero. The snapshot has no package count, so the spec's `N packages` is dropped.
10. **Ctrl+T only cycles the plan layout.**
    - `/work` opens the Work list, which is the Tasks panel without its Plan tab.
    - Ctrl+B still switches from the Work list to the session sheet.
    - The Work list loses "Mark reviewed" and "Dismiss", so `WorkService.AcknowledgeWork` has no TUI caller left.
11. **The budget.** The box's maximum height is `min(height×2/5, height − scroll hint − breadcrumb − status line − dock − input chrome − 1 − minTranscriptRows)`, never below 1. This is `maxInputHeight`'s formula with the box in place of the input. When the box is still too tall after the spec's four shed steps:
    - the live part goes;
    - running rows cap at 1 plus the more row;
    - finally the box becomes one plain summary row.
12. **Split geometry.** The right pane is 34 cells inside the outline and is skipped below 90 columns. The live part renders at the left pane's width.
13. **Frame tests.** The spec asks for golden frames. They are text files under `internal/app/tui/livebox/testdata/` with an `-update` flag (Task 5). The executor reviews each frame against the spec's frames before committing.

**Execution:**
- Branch first: `git switch -c p-c-live-box` from `main` after P-B merged.
- Run the tasks in order. Run each task's Verify and commit it as `p-c-live-box: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Task 1 touches only `internal/viewmodel`. It can be cherry-picked to `main` on its own if the skill duplicate should ship before the box.
- Tasks 3 to 5 are the pure `livebox` package and run in order. Tasks 6 to 10 change `internal/app/tui` and run in order after them.
- "Delete test X" means delete the whole function. "Rewrite test X as Y" means replace it with one named Y that asserts what the step says.

## Contracts

**Task 3, 4 and 5, `internal/app/tui/livebox` (new package).**

```go
package livebox

// Mode is the plan section's layout.
type Mode string

const (
	Stacked Mode = "stacked"
	Split   Mode = "split"
	Folded  Mode = "folded"
)

const (
	// SplitMinWidth is the narrowest frame that gets the split layout.
	SplitMinWidth = 90
	// RightWidth is the split layout's right pane, inside the outline.
	RightWidth = 34
	// BarCells is a running row's measured bar.
	BarCells = 12
	// PlanWindow is the longest list the plan section shows item by item.
	PlanWindow = 6
	// CardRows caps an expanded card, header included.
	CardRows = 5
	// RunningCap is how many running rows survive shed step 3.
	RunningCap = 3
)

// ParseMode maps a config value to a mode; anything unknown is Stacked.
func ParseMode(s string) Mode

// Next is the mode Ctrl+T moves to: stacked → split → folded → stacked,
// skipping split when width < SplitMinWidth.
func Next(m Mode, width int) Mode

// InnerWidth is the content width inside the outline: width − 4.
func InnerWidth(width int) int

// LeftWidth is the live part's width: InnerWidth(width) in stacked and
// folded mode, width − 41 in split mode at width ≥ SplitMinWidth.
func LeftWidth(width int, m Mode) int

// Blocks draws done/total as ▰▱ cells, at most maxCells. It is
// progressBlocks moved unchanged from internal/app/tui/nowbar.go.
func Blocks(done, total, maxCells int) string

// Bar draws a measured bar of cells cells: ━ in c for the filled part, ╸
// for a half cell, ─ in the muted colour for the track.
func Bar(fraction float64, cells int, c color.Color) string

// Live is the live part, rendered by the TUI at LeftWidth.
type Live struct {
	Full         []string           // one entry per screen line
	Targets      []viewmodel.NodeID // aligned with Full; the zero NodeID for none
	Short        []string           // the beat's first line and its current call
	ShortTargets []viewmodel.NodeID
}

// PlanItem is one todo.
type PlanItem struct {
	ID, Content, Status string // Status: "completed", "in_progress" or "pending"
	StartedAt           time.Time
	First               viewmodel.NodeID // the first beat bound to it; zero when none
}

type TargetKind uint8

const (
	TargetNone TargetKind = iota
	TargetLive             // a line of the live part; Node names its row
	TargetWork             // a running row or a line of its card; Key is the TaskKey
	TargetMore             // the "+N more · /work" row
	TargetPlan             // a plan item; Key is its ID, or "#<index>" when it has none
)

type Target struct {
	Kind TargetKind
	Node viewmodel.NodeID
	Key  string
}

type Input struct {
	Width     int // the column the box spans
	MaxRows   int // the most rows the box may take
	Now       time.Time
	Busy      bool
	State     string   // top-border state, already styled; "" when not busy
	Hints     []string // top-border right side, in order of priority
	Spinner   string   // the turn spinner frame; "" draws glyph.Running
	Live      Live
	Work      []viewmodel.WorkItem // live items only (viewmodel.WorkActive)
	Previous  map[string]*progress.Snapshot
	Headlines map[string]string // agent TaskKey → the child's newest narration headline
	Expanded  string            // TaskKey of the one open card
	Plan      []PlanItem
	Stale     time.Duration // > 0 when the plan is stale
	Mode      Mode
	Cursor    Target // the browse cursor when it is in the box
}

type RowFrame uint8

const (
	FrameInner  RowFrame = iota // Text sits inside "│ … │"
	FrameBorder                 // Text is a whole border line
	FramePlain                  // Text is a whole row with no outline (idle and compact rows)
)

type Row struct {
	Text   string // styled foreground only
	Right  string // split mode: the right pane's text for this row
	Frame  RowFrame
	Tint   bool // running work: painted with the surface colour
	Cursor bool // the browse cursor's row: painted with the selection colour
	Target Target
}

type Layout struct {
	Rows  []Row
	Mode  Mode     // the mode drawn, after shedding
	Shed  int      // shed steps applied, 0–7 (Task 5)
	Stops []Target // browse stops in order: running rows, the more row, plan items
}

func Plan(in Input) Layout
func Render(l Layout, width int) string
```

**Task 6, `internal/app/tui`.**

```go
// Model fields.
liveBoxMode livebox.Mode    // the plan layout; Task 9 seeds it from config
live        livebox.Live    // the live part, rendered by refreshViewport
boxMemo     *livebox.Layout // this frame's layout, planned once in viewString

// livebox_view.go (new).
func (m Model) boxInput() livebox.Input
func (m Model) boxLayout() livebox.Layout // boxMemo when set, else livebox.Plan(m.boxInput())
func (m Model) boxRows() int
func (m Model) boxMaxRows() int
func (m *Model) renderLive(live []*viewmodel.Node, c *stepRenderCtx, width int, themeSig uint64) livebox.Live
func (m *Model) boxTop() int // the screen row of the box's first line
func (m *Model) handleBoxClick(msg tea.Mouse) (tea.Cmd, bool)
func (m *Model) scrollToNode(id viewmodel.NodeID) bool
```

**Task 7, `internal/app/tui/steps.go`.**

```go
// stepRenderCtx gains:
pointer func(toolCallID string) string // the live work row a running call drives, "" for none
```

**Task 9, `internal/app/config/types.go`.**

```go
type TUIConfig struct {
	// … existing fields …
	LiveBox LiveBoxConfig `toml:"live_box"`
}

type LiveBoxConfig struct {
	// Layout is the plan's starting layout: "stacked", "split" or "folded".
	Layout string `toml:"layout"`
}
```

## Task 1: A skill load shows once

**Goal:** an explicit `skill.load` leaves one row in the transcript, its call row in the beat. The beat is not split by the load's messages.

**Files:** `internal/viewmodel/stack.go`, `internal/viewmodel/stack_test.go`.

**Steps:**

1. **`buildTurn`, before its item loop.** Build `loadedHere map[string]bool`: the skills this turn's items loaded with a call.
   - An item counts when it is a `KindAudit` with `Audit.ToolName == "skill.load"` and `!EventFailed(*Audit)`.
   - Its key is the `name` field of `Audit.Args`, decoded with `json.Unmarshal` into `struct{ Name string \`json:"name"\` }` and trimmed.
   - Add the `encoding/json` import if `stack.go` lacks it.
2. **The `case session.KindMessage:` branch.** Next to the existing `ContentTypeSubagentNotice` skip, add two:
   - `m.ContentType == session.ContentTypeSkillBody`: `continue`. The body is model context and never rendered.
   - `m.ContentType == session.ContentTypeSkill && loadedHere[strings.TrimSpace(m.Content)]`: `continue`. The call row is the trace.

   Comment both: the messages carry no step ID, so as turn-level blocks they would draw a second row and end the beat.
3. **Tests (`stack_test.go`):**
   - Add `TestSkillLoadLeavesOneRow`. Items:
     - `userMsg(1, 0)`;
     - `narration(2, 1, 1, "Loading the debug skill.")`;
     - a `ContentTypeSkillBody` system message at 2 and a `ContentTypeSkill` system message `"debug"` at 2, both with no step;
     - a `skill.load` audit in step 1 at 3 with `Args: {"name":"debug"}`;
     - `audit("file.read", 2, "r", 5)` in narrationless step 2.

     Steps are `stepOf(1, 1, 1, 4)` and `stepOf(2, 1, 5, 6)`. The shape is one `step:1` beat holding both call rows, and there is no `msg:` node besides `msg:1`.
   - Add `TestSkillTagWithoutACallStays`: the same tag message with no `skill.load` audit in the turn is still a `msg:` node.
   - Add `TestFailedSkillLoadKeepsItsTag`: a `skill.load` audit with `Error` set does not hide the tag.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pc-suite go test ./internal/viewmodel/... ./internal/acp/...
TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/...
go build ./... && gofmt -l internal/
```

Expected: viewmodel and ACP pass; the TUI passes except the known failure; the build is clean and `gofmt -l` prints nothing.

## Task 2: Measured rate and ETA

**Goal:** each running item's previous progress observation is kept, so `progress.EstimateFor` can measure a rate and an ETA.

**Files:** `internal/app/tui/model.go`, `internal/app/tui/pump_test.go`.

**Steps:**

1. **`handleWorkMsg`.** Before `m.workSnapshot = msg.snapshot`, call `m.notePreviousProgress(msg.snapshot)`.
2. **Add `notePreviousProgress(next viewmodel.WorkSnapshot)`** beside it.
   - Build a new map. For each item in `next` with a non-nil `Progress`, find the item with the same `TaskKey` in the current `m.workSnapshot`:
     - when that old item's `Progress` is non-nil and its `ObservedAt` is before the new one's, store `progress.Clone(old.Progress)` under the key;
     - otherwise keep the existing `m.workPrevious[key]`, if any.
   - Assign the new map to `m.workPrevious`, so keys absent from `next` are dropped.
3. **`startWorkSubscription`.** Reset `m.workPrevious = nil` where it seeds the snapshot.
4. **Tests (`pump_test.go`):**
   - Add `TestWorkProgressKeepsThePreviousObservation`. Two snapshots of one item, with progress observed at t and t+2s. After the second, `m.workPrevious[key].ObservedAt` is t.
   - Add `TestWorkProgressPrunesSettledItems`: an item missing from the next snapshot loses its entry.
   - Add `TestWorkProgressIgnoresARepeatedObservation`: the same `ObservedAt` twice keeps the earlier previous.

**Verify:** run `TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/...`. Expected: it passes except the known failure.

## Task 3: `livebox` running rows

**Goal:** the new package draws a running row and an expanded card for every work item shape in spec §5.6, with the measured bar.

**Files:** `internal/app/tui/livebox/running.go`, `progress.go`, `livebox.go` (the Contracts' constants and types, `ParseMode`, `Next`, `InnerWidth`, `LeftWidth`, `Blocks`, `Bar`), and `running_test.go`. All are new.

**Steps:**

1. **`livebox.go`.** Declare the Contracts' types and constants, then the small functions:
   - `ParseMode` and `Next` follow their doc comments.
   - `InnerWidth` is `max(width−4, 1)`.
   - `LeftWidth` is `max(width−41, 1)` for `Split` when `width >= SplitMinWidth`, else `InnerWidth(width)`.
   - `Blocks` is `progressBlocks` copied unchanged from `internal/app/tui/nowbar.go`. Task 10 deletes the original.
   - **`Bar`:** quantise the filled fraction to the nearest half cell — a half cell appears once the fraction reaches the midpoint between two whole cells:
     - `filled := fraction × cells`, clamped to [0, cells];
     - round `filled × 2` to the nearest integer and call it `half`; draw `⌊half/2⌋` `━` cells, then one `╸` when `half` is odd and a cell is left;
     - fill the rest with `─`;
     - the filled part is in `c`, the track in `theme.Current().FGMuted`.

     The package imports `marshal/internal/app/tui/glyph`, `marshal/internal/app/tui/theme`, `marshal/internal/viewmodel` and `marshal/internal/progress`. It must not import `internal/app/tui`.
2. **`progress.go`.** Move the measurement helpers from `nowbar.go` into this package, unexported and unchanged except that `dimStyle()` and `mutedStyle()` become `theme.MutedStyle()`:
   - `workPrimaryMetric`, `workMetricCount`, `workNumber` and `workRateText`;
   - `workBytesIEC`, `workClean` and `lastWorkLine`.

   Drop the `work` prefix from each name.
3. **`running.go`, kind styling.** `kindStyle(kind string) (g string, c color.Color)`:
   - `"agent"`: `glyph.Agent`, `AccentSecondary`;
   - `"command"`: `glyph.Job`, `StatusInfo`;
   - `"watch"`: `glyph.Watch`, `FGMuted`;
   - anything else: `glyph.Running`, `AccentPrimary`.

   `itemID(it)` is `"#" + it.SourceID` for an agent, else `it.DisplayID`, else `it.SourceID`, cut to 6 cells.
4. **`running.go`, the row.** `runningRow(it viewmodel.WorkItem, in Input, w int, split bool) Row`, inner width `w`. The text:
   - `▌` in the kind colour, the glyph in the kind colour, a space, then the id in the kind colour, padded to column 10;
   - the title: `it.Label`, else the id, through `clean(…, 120)`, cut to `min(width(title), max((w−22)×2/5, 8))` cells;
   - two spaces, then the measurement (step 5), cut to the room left;
   - right-aligned: the elapsed time (`now − StartedAt`, in `formatElapsed` form), a space, and `in.Spinner` (else `glyph.Running`) in the kind colour.

   `Tint` is true, and `Target` is `{TargetWork, Key: it.TaskKey}`. Copy `formatElapsed` into the package unchanged from `internal/app/tui/transcript.go`.
5. **`running.go`, the measurement.** `measure(it, prev *progress.Snapshot, now time.Time, room int, split bool) string`. First match wins:
   1. `it.State == "blocked"`, in warning colour: `⚠ needs ` followed by a word for `it.Prompt.Kind`. The kinds are the ones `internal/app/runtime.go` sets:
      - `approval` → `approval`
      - `question` → `an answer`
      - `access` → `access`
      - `skill` → `a skill decision`
      - `sudo` → `a password`
      - anything else, or a nil `Prompt` → `input`
   2. `it.Attention != ""`: `⚠ ` plus the cleaned attention, in warning colour.
   3. `it.Kind == "agent"`: `in.Headlines[it.TaskKey]`, else `it.Activity`.
   4. **`it.Progress != nil`.** Let `m, ok := primaryMetric(p)` and `est := progress.EstimateFor(prev, p, m.ID, now, it.State == "stopping")`.
      - **Go test JSON** (`ProgressConfig.Source == "go-test-json"`): `N passed · N failed`, plus ` · N skipped` when nonzero. N comes from the metrics labelled Passed, Failed and Skipped. The failed count is in error colour when nonzero. There is no bar.
      - **A total and a fraction.** Optionally a phase prefix (`<Index>/<Total> <label> `, when `Phase.Total > 0`), then `Bar(*est.Fraction, BarCells, kind colour)`, a space and the percent (`%.0f%%`). Then two spaces and `metricCount(m)`. Then:
        - **stale** (`est.Stale && p.Sequence > 0`): `⚠ no update for <now − ObservedAt>` in warning colour, with no rate or ETA;
        - **done but still running** (`*est.Fraction >= 1` and the state is running): `reported done · process still running`;
        - **otherwise:** ` · ~<ETA>` when there is an ETA, and ` · <rate>/s` when there is a rate and at least 20 cells remain.

        In split mode, keep only the bar and the percent.
      - **A metric without a total:** `metricCount(m)`, then ` · <rate>/s` when measured, then ` · size unknown` for a `bytes` unit. There is no bar.
      - **No metric but a phase:** the phase label (`Label`, else `ID`).
   5. Otherwise `lastWorkLine(it.OutputTail)`, else the cleaned `it.Activity`, else "".
6. **`running.go`, the card.** `card(it, in, w) []Row` is the expanded card: at most `CardRows` rows, the header row first. The rest, each only when it has text, all with `Tint` and the same target:
   - the full-width bar and percent: `Bar(fraction, w−10, kind colour)` when a fraction is known;
   - the measurement line: as step 5 with the full room, without the bar;
   - the detail: `p.Detail`; else up to four checklist rows on one line (`✓` done, the spinner for running, `·` otherwise, each with its label, joined by two spaces); else `lastWorkLine(it.OutputTail)`;
   - the actions: `↵ collapse`, plus ` · s stop` when `it.Capabilities.Stop`, ` · f open` for an agent, and ` · /work list`, all muted.
7. **Tests (`running_test.go`).** Every string is checked with `ansi.Strip`.
   - `TestRunningRowPerShape`, a table over spec §5.6 that asserts the measurement text:
     - a subagent with a headline;
     - a blocked subagent;
     - a generic command with output;
     - Go tests with 210 passed and 2 failed;
     - a download with a total (bar, `57%`, `2.1 GiB/3.7 GiB`, ETA given a previous observation);
     - a download without a total (`size unknown`, no `━`);
     - a phased custom job (`2/3 verify`);
     - a stale snapshot (`no update for 42s`, no `/s`);
     - 100% while running.
   - `TestBarDrawsHalfCells`: `Bar(0.5, 12, c)` has 6 `━` and 6 `─`, and `Bar(0.54, 12, c)` has 6 `━`, one `╸` and 5 `─`.
   - `TestSplitRowKeepsOnlyBarAndPercent`.
   - `TestCardCapsAtFiveRowsAndOffersStopOnlyWhenAllowed`.
   - `TestNextSkipsSplitWhenNarrow`: `Next(Stacked, 89) == Folded` and `Next(Stacked, 90) == Split`.
   - `TestParseModeDefaultsToStacked`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/livebox/...
go vet ./internal/app/tui/livebox/... && gofmt -l internal/app/tui/livebox
```

Expected: the tests pass, vet is clean, and `gofmt -l` prints nothing.

## Task 4: `livebox` plan section

**Goal:** the plan renders in the three layouts and as the idle row, with the long-list window.

**Files:** `internal/app/tui/livebox/plan.go` and `plan_test.go` (both new).

**Steps:**

1. **`planWindow(items []PlanItem) (shown []PlanItem, doneBefore, moreAfter int)`.**
   - With `len(items) <= PlanWindow`, return all.
   - Otherwise the anchor is the first `in_progress` item, else the first item not completed. `shown` is up to four items from the anchor. `doneBefore` counts the completed items before the anchor, and `moreAfter` counts the items after `shown`.
2. **Marks.** `glyph.OK` in `StatusSuccess` for completed, the spinner in `AccentPrimary` for in progress, and `glyph.Ambient` in `FGMuted` for pending. Completed text is muted.
3. **The stacked section,** `planStacked(in Input, w int) (header string, rows []Row)`.
   - **Header:** `plan  ` + `Blocks(done, total, 10)` + ` <done>/<total>`. Add `  stale <age>` in warning colour when `in.Stale > 0`, and ` done` when every item is completed.
   - **Rows:**
     - none when every item is completed (decision 8);
     - otherwise the window's entries in two columns, column-major. The left column takes `ceil(n/2)` entries. Each cell is `(w−2)/2` cells wide, with a two-space gap.
     - An entry is a mark, a space and the cleaned content, truncated. The in-progress item's cell ends with its elapsed time when `StartedAt` is set.
     - `doneBefore > 0` adds a first entry `✓ <n> done`, and `moreAfter > 0` a last entry `+<n> more`.
     - Each item row's target is `{TargetPlan, Key: ID or "#<index>"}`.
4. **The split pane,** `planSplit(in, rows int) []string`. The window's entries one per line at `RightWidth`. Content longer than the pane wraps onto one continuation line indented by two cells, then truncates. The header goes in the top border's right segment (Task 5).
5. **The folded border,** `planFolded(in, width int) string`:
   - `╰─ plan ` then the marks separated by spaces for up to 12 items, else `Blocks(done, total, 8)`;
   - then `<active>/<total>` (or `<done>/<total>`) and the active item's text, truncated;
   - then ` ─` and `─` fill to `╯`, all in `BorderMuted` except the marks.
6. **The idle row,** `idleRow(in, width) Row` with `FramePlain`. `✓ Plan 4/4 done` when all are completed. Otherwise `▸ Plan <done>/<total> · <text>`, where the text is the in-progress item's, else the first pending item's.
7. **Tests (`plan_test.go`):**
   - `TestPlanWindowAroundTheActiveItem`: 10 items, active 6, give `✓ 5 done`, items 6–9 and `+1 more`.
   - `TestShortPlanShowsEveryItem`.
   - `TestFinishedPlanHasNoItemRows`.
   - `TestStackedPlanUsesTwoColumns`: item 1's text sits left of item 4's on the same row for a 6-item list.
   - `TestFoldedBorderUsesBlocksPastTwelve`.
   - `TestIdleRowSaysDoneOrActive`.
   - `TestSplitPaneWrapsALongTodo`.

**Verify:** the same commands as Task 3.

## Task 5: `livebox` assembly, budget and frames

**Goal:** `Plan` assembles the bordered box from its sections, sheds to fit `MaxRows`, and records a target for every row. `Render` paints it.

**Files:** `internal/app/tui/livebox/box.go`, `box_test.go`, `frames_test.go` and `testdata/*.txt`. All are new.

**Steps:**

1. **When the box exists.**
   - `in.Busy || len(in.Work) > 0 || len(in.Live.Full) > 0`: the bordered box.
     - The live part counts on its own because `SplitLive`'s busy flag (the snapshot's `Busy`) is also true for an active call or streaming reasoning while the turn clock (`in.Busy`) is off.
     - A live part that drew no box would be on screen nowhere.
   - Otherwise, `len(in.Plan) > 0`: the idle row alone.
   - Otherwise: no rows.
2. **Running order.** `Plan` sorts a copy of `in.Work`: blocked items first, then by `StartedAt`, then by `TaskKey`.
3. **The bordered box, top to bottom.** All outline glyphs are in `BorderMuted`.
   - **Top border:** `╭─ ` + `in.State` + ` ` + a `─` fill + ` ` + the hints joined with ` · ` + ` ─╮`.
     - When `State` is empty, the border starts `╭─` with no gap.
     - When it doesn't fit, drop hints from the end, then truncate the state.
     - In split mode, the plan header sits at the split column, as `┬─ <plan header> ─╮`, replacing the end of the fill.
   - **Live section**, only when `len(Live.Full) > 0`: one `FrameInner` row per line, targeted `{TargetLive, Node: Targets[i]}`. It has no header.
   - **Running section**, only when there is running work:
     - the header `├─ running <n> ─…─┤` (`FrameBorder`);
     - a row per item, or `card` rows for `in.Expanded`;
     - then `+<n> more · /work` (muted, `{TargetMore}`) when capped.
   - **Plan section**, only when a plan exists and the mode is `Stacked`: the header `├─ <plan header> ─…─┤` and `planStacked`'s rows.
   - **Bottom border:** `planFolded` in `Folded` mode with a plan, else `╰─…─╯`. In split mode, `┴` sits at the split column.
4. **Split rows.** In `Split` mode at `width >= SplitMinWidth`:
   - the left content is the live and running sections at `LeftWidth`;
   - each row's `Right` is the next line of `planSplit`;
   - the box is as tall as the longer side;
   - section headers on the left end with `┤` at the split column.
5. **Shedding.** While `len(rows) > in.MaxRows`, apply the next step and rebuild. `Layout.Shed` is the number of steps applied.
   1. The plan folds into the bottom border (`Mode = Folded`, which ends any split).
   2. The live part becomes `Live.Short`.
   3. Running rows cap at `RunningCap`, plus the more row.
   4. The expanded card collapses to its row.
   5. The live part is dropped.
   6. Running rows cap at 1, plus the more row.
   7. **The compact row:** one `FramePlain` row with the state, `⧉<n> ┆<n> ○<n>` counts for nonzero kinds, and `plan <done>/<total>` when a plan exists, truncated to `width`.
6. **Cursor and stops.**
   - A row whose target equals `in.Cursor` (same kind and key; for `TargetLive`, same node) gets `Cursor = true`.
   - `Stops` lists one target per running item, then the more row, then one per plan item drawn, in row order.
7. **`Render(l, width)`.** Join the rows with `\n`.
   - `FrameBorder` and `FramePlain` rows are written as they are, cut to `width`.
   - A `FrameInner` row is `│ ` + its text padded to `LeftWidth` (`InnerWidth` when not split), then ` │ ` + `Right` padded to `RightWidth` in split mode, then ` │`.
   - A `Tint` row's content area is painted with `chrome.PaintBand(text, cellWidth, theme.Current().BGSurface)`, and a `Cursor` row's with `BGSelection`.
   - Under NO_COLOR, `BGSurface` is a no-colour value and paints nothing.
8. **Tests (`box_test.go`):**
   - `TestBoxEmptyWhenNothingLiveAndNoPlan`, and `TestIdlePlanIsOnePlainRow`.
   - `TestLivePartDrawsTheBoxWithoutTheTurnClock`: `Busy` false with one `Live.Full` line gives a bordered box with an empty state.
   - `TestShedOrder`. Step `MaxRows` down from the full height and assert `Shed` and the shape at each threshold: the plan folds first, then the live part shortens, then running rows cap at 3, then the card collapses.
   - `TestBoxNeverExceedsMaxRows` for `MaxRows` from 1 to 30 over a busy input with every section.
   - `TestSplitSkippedBelowNinety`: `Mode: Split` at 89 columns draws stacked.
   - `TestOneExpandedCard`: the other items stay one row each.
   - `TestTargetsMatchRows`: every `TargetWork` row's key is an item drawn, every running item has its first row targeted, and `Stops` follow row order.
   - `TestEveryRowIsExactlyTheWidth`: `ansi.StringWidth` of every rendered line equals `width`, in all three modes.
   - `TestHintsDropBeforeTheState`.
9. **Frame tests (`frames_test.go`).**
   - Add `var update = flag.Bool("update", false, "rewrite testdata frames")`.
   - For each case, render `Render(Plan(in), width)` with NO_COLOR set (`t.Setenv("NO_COLOR", "1")`, then `theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))`, restoring the theme in `t.Cleanup` as `depth_golden_test.go` does).
   - Compare with `testdata/<name>.txt`, or write it under `-update`.
   - **Cases**, at 80×24, 96×34 and 120×40 (`MaxRows` = `height×2/5`):
     - `stacked`, `split` (96 and 120 only), `folded`, `expanded`, `idle`, `compact` (`MaxRows` 1);
     - `rows-<shape>` for every shape in Task 3's `TestRunningRowPerShape`, one frame at 96 columns.

     They are built from fixed inputs with the clock at `time.Unix(10_000, 0)`.
   - Generate the files with `go test ./internal/app/tui/livebox/... -run TestFrames -update`. Then read every file and check it against spec §5.5 and §5.6 before committing:
     - the outline closes;
     - the sections sit in order;
     - running rows have `▌`;
     - the split column lines up;
     - no line is wider than its frame.

**Verify:** the same commands as Task 3, plus `git status --short internal/app/tui/livebox/testdata`, which lists the new frames.

## Task 6: The box replaces the now bar

**Goal:**
- History renders in the viewport, and the live part (`SplitLive`'s `live`) renders in the box.
- The box takes the now bar's place in the frame, its height budget and its clicks.
- The live mirror row goes.

**Files:**
- `internal/app/tui/livebox_view.go` (new), `model.go`, `view.go`, `viewport_render.go`, `click.go`, `selection_input.go`, `livebox_view_test.go` (new);
- the tests listed in step 8.

**Steps:**

1. **Model fields.** Add the Contracts' `liveBoxMode`, `live` and `boxMemo` to `Model`. Set `liveBoxMode` to `livebox.Stacked` in `New`; Task 9 reads the config.
2. **`refreshViewport` (`viewport_render.go`).**
   - After `turns := viewmodel.Build(snap)` and the band, add `history, live := viewmodel.SplitLive(turns, snap.Busy)`.
   - The render loop (`for _, turn := range turns`, the one that calls `addBlock`) iterates `history` instead.
   - After that loop, call `collectSeen` and `indexTree` on every node in `live`. Otherwise `pruneRenderState` drops their expansion and scroll state.
   - Set `m.live = m.renderLive(live, rctx, livebox.LeftWidth(width, m.liveBoxMode), themeSig)`.
   - `rowBeats`, `TaskStats` and `contentSignature` keep reading `turns`.
3. **`livebox_view.go`.**
   - **`renderLive`.** Render each node of `live`, in order, with `m.renderNode(n, c, w, themeSig)`.
     - This is the dispatch history uses (`drawNode`): a beat goes through `renderStep`, a completion through `renderCompletion`, and the reasoning node through `renderThinkingBox`, which also records the region's rows.
     - Going through `renderNode` keeps `nodeRenderHook` firing for the live beat, which `TestSettledStepsAreNotReRenderedOnSpinnerTicks` relies on.

     Split each output into lines and append them to `Full`. Each line's target is the node's ID, except lines inside one of the returned `subRegion`s, which take that region's ID.

     `Short` is, with the same targets:
     - the beat's first line, plus the lines of its last `KindTool` sub-region;
     - then the reasoning node's first line, when there is one. With no beat, that line is all of `Short`.
   - **`boxInput`.**
     - `Width` is `m.leftWidth`, `MaxRows` is `m.boxMaxRows()`, `Now` is `m.now()`, and `Busy` is `m.busy && !m.turnStartedAt.IsZero()`.
     - **State,** while busy, with elapsed as `formatElapsed(now − m.turnStartedAt)`:
       - during an active SDD run: `spinnerLabel(spinner, runPanelSummaryText(sdd, now, 60)) + " " + elapsed`;
       - during an active swarm: `spinnerLabel(spinner, swarmStripText(swarm)) + " " + elapsed`;
       - otherwise: `spinnerLabel(spinner, "working " + elapsed)`, plus ` · <activity>` when `m.state.Activity()` passes `spinnerShowsLabel` and has a label. Use `m.state.PinnedSpinnerLabel(act)` as `nowBarInput` does.
     - **Hints:** `Ctrl+T plan` when `len(m.band.Tasks) > 0`, then `Ctrl+C ×2 stop` while busy.
     - **The rest:**
       - `Spinner` is `m.turnSpinnerFrame()`, and `Live` is `m.live`;
       - `Work` holds the items of `m.workSnapshot.Items` for which `viewmodel.WorkActive` holds;
       - `Previous` is `m.workPrevious`, and `Expanded` is `m.workExpanded`;
       - `Headlines` maps each agent item's `TaskKey` to `subagentHeadline(v.Child)`, where `v, ok := m.state.Subagent(id)`, `id` is its `SourceID` parsed with `strconv.ParseInt`, and `v.Child != nil`;
       - `Plan` comes from `m.band.Tasks` (ID, content, status, `StartedAt`, `First`);
       - `Stale` is `m.band.StaleAge` when `m.band.Stale`;
       - `Mode` is `m.liveBoxMode`, and `Cursor` is the box target under the browse cursor (zero until Task 8).
   - **`boxMaxRows`:** `max(min(m.height*2/5, m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-statusLineRows-m.dockRows()-m.inputChromeRows()-1-minTranscriptRows), 1)`. The second term is `maxInputHeight`'s, with the box in place of the input.
   - **`boxLayout` and `boxRows`** follow the Contracts. `boxRows` is 0 when `m.dock.FullFrameOpen()`.
   - **`boxTop`:** `m.scrollHintRows() + m.breadcrumbRows() + m.viewport.Height()`, as `nowBarBand` computes its top.
   - **`handleBoxClick`.** A left click inside `[boxTop, boxTop+len(rows))` and `[0, m.leftWidth)` is consumed, and acts on the row's target:
     - `TargetLive`: `toggleExpanded(Node)` and refresh, unless the node is `LiveThinkingID`;
     - `TargetWork`: toggle `m.workExpanded` and refresh;
     - `TargetMore`: open the Work list with `m.openTasks(tasksTabWork)` (Task 10 renames it);
     - `TargetPlan`: `scrollToNode(item.First)` when it is set.
   - **`scrollToNode`:** find the node in `m.nodeRegions`, as P-B's deleted `regionFor` did. Set `m.viewportFollow = false` and `m.viewport.SetYOffset(r.startLine)`, and report whether it was found.
4. **`view.go`, `viewString`.**
   - Replace `plan := planNowBar(m.nowBarInput())`, `m.nowBarMemo = &plan` and `m.recordWorkPlan(plan)` with `layout := livebox.Plan(m.boxInput())` and `m.boxMemo = &layout`.
   - Replace the `renderNowBar(plan, m.leftWidth)` block with `livebox.Render(layout, m.leftWidth)`, appended in the same place when non-empty.
5. **The height budget (`model.go`).** Replace `m.nowBarRows()` with `m.boxRows()` in `updateViewportHeight`, `maxInputHeight` and the resize line that sets the viewport height. `ShouldShowStatusURL` returns `true` (decision 6); Task 10 deletes it.
6. **Clicks and the wheel.**
   - In `selection_input.go`, the release path's `return m.handleNowBarClick(press)` becomes `return m.handleBoxClick(press)`.
   - In `scrollLiveRegionAt` (`click.go`), when the wheel is over a box row targeted `{TargetLive, Node: LiveThinkingID}`, change `m.regionOffset[viewmodel.LiveThinkingID]` exactly as the function already does for a viewport region. Then refresh and return true.
7. **The mirror row.** Delete `liveStepSummary` (`steps.go`) and the `LiveHeadline`/`LiveToolGlyph` assignment in `nowBarInput`. The now bar is dead code from this task on, and Task 10 deletes it.
8. **Tests.**
   - **`livebox_view_test.go`:**
     - `TestLiveBeatRendersInTheBoxNotTheViewport`. While busy, the live beat's narration is in `livebox.Render(m.boxLayout(), …)` and not in `m.viewport.GetContent()`. After `m.busy = false` and a refresh, it is in the viewport.
     - `TestBoxIsBudgetedInTheFrameHeight`: `clipLeftColumnHook` never fires for a busy model with 8 running items, a 10-item plan and a live beat, at 80×24 and 120×40.
     - `TestBoxClickTogglesAWorkCard`, `TestBoxClickOnPlanItemJumpsToItsBeat` and `TestBoxClickOnALiveCallExpandsIt`.
     - `TestWheelOverTheBoxReasoningScrollsIt`.
     - `TestBoxStateShowsTheSDDRun`.
   - **Rewrite these against the box.** Read the box's text through a test helper, `boxOut(m Model) string`, defined in `livebox_view_test.go`, which returns `livebox.Render(m.boxLayout(), m.leftWidth)`. Keep each test's name:
     - `TestTurnSpinnerShowsActivityLabel`, `TestTurnSpinnerShowsElapsedDirectlyAboveInput` and `TestTurnSpinnerSpansPhaseGaps` (`view_test.go`);
     - `TestTurnSpinnerDropsStreamingThinkingLabels`, `TestTurnSpinnerKeepsStableActivityLabels`, `TestTurnSpinnerLabelDwellHoldsOldLabel` and `TestTurnSpinnerLabelDwellResetsOnKindChange` (`model_test.go`).

     Each asserts the box's top border where it asserted the now bar's turn row.
   - **Rewrite with new names:**

     | Old test (file) | New test |
     |---|---|
     | `TestTurnSpinnerReservedWhenIdle`, `TestTurnSpinnerBlankWhenIdleButStillReserved` (`view_test.go`) | one test, `TestIdleBoxIsEmptyWithoutAPlan` (`boxRows() == 0`) |
     | `TestNoBlankRowAboveNowBar` | `TestNoBlankRowAboveTheBox` |
     | `TestResizeComputesSingleColumnGeometry` | same name, using `boxRows()` |
     | `TestNoNowBarNoBand` | `TestNoBoxWhenIdle` |
     | `TestNowBarBandSitsBelowViewport` | `TestBoxSitsBelowTheViewport` |
     | `TestAllDoneTodosLeaveTheNowBar` | `TestFinishedPlanIsOneIdleRow` |
     | `TestNowBarFollowsDrilledSubagentTodos` | `TestBoxFollowsDrilledSubagentTodos` |
     | `TestSDDRunOccupiesOneNowBarRow` (`run_panel_test.go`) | `TestSDDRunShowsInTheBoxBorder` |
     | `TestTodoBandIsBudgetedInTheFrameHeight` | `TestBoxIsBudgetedInTheFrameHeight` (above) |

   - **Delete:**
     - `TestTurnRowFoldsIntoSDDProgressRow`, `TestNowBarClickDrillsIntoAgent` and `TestNowBarClickOnProgressRowDoesNothing`;
     - `TestMirrorRowComesFromTheLiveStepWhenScrolledAway`, `TestNowBarLiveMirrorRow` and `TestNowBarAgentRowsShowHeadlineAndModelOnlyWhenDifferent` (`steps_test.go`);
     - `TestNowBarClockIsTheInProgressTasksElapsedTime` and `TestNowBarIgnoresATaskStartedBeforeThisTurn` (`tasks_test.go`). The plan item's cell carries its own clock, which Task 4 tests.
   - **`TestTodoDividersOrderTheWorkAndTheBandCarriesProgress` (`tasks_test.go`):** read the live beat's narration (`Writing it.`) through `boxOut`, not the viewport, and replace its band assertions with the box's plan header (`2/3`).
   - **`TestBandNotShownWhenTheTurnNeverTouchedTheList` (`tasks_test.go`):** rewrite it as `TestLeftoverPlanIsOneIdleRow`. The same idle, unrelated turn with a leftover two-item list keeps `m.band.Todos` false, and `stripANSI(boxOut(m))` is exactly the idle row `▸ Plan 0/2 · A`. The plan now always shows.
   - **Tests that read live rows from the viewport.** Moving the live part out of the viewport fails these at `d7d9b39c` (the probe in the header).
     - **Unchanged.** These read `View()`, which now includes the box, so they pass once the box draws the live part. If one still fails, the live part is missing from the box, and that is a defect in this task.
       - `TestPolishedTranscriptShowsRolesThinkingAndInput`, `TestThinkingBoxRendersWhileStreaming`, `TestBusyTickRefreshesViewportOnReasoningGrowthAlone` and `TestRecentToolCallRendersInlineAfterFastToolCompletes` (`model_test.go`);
       - `TestRunningToolRendersBelowGroupThenFolds` (`toolgroup_test.go`);
       - `TestSettledStepsAreNotReRenderedOnSpinnerTicks` (`steps_test.go`), through `renderLive`'s use of `renderNode`.
     - **Same name, reading the box.**
       - `TestPolishedTranscriptReflowsAfterResize` (`model_test.go`): the viewport holds the user message alone, and `strings.Join(m.live.Full, "\n")` equals the thinking box rendered at `livebox.LeftWidth(m.leftWidth, m.liveBoxMode)`.
       - `TestRunningToolRowInsideStepExpandsOnClickEvenWithGlobalExpand` (`steps_test.go`): both checks read `boxOut(m)`.
       - `TestLiveThinkingDrawsBelowTheSpine` (`spine_test.go`): assert on `m.live.Full`. The line holding `go test` comes before the line holding `thinking`, and no line after it contains `├` or `└`. The box's own section borders use `├`, so the rendered box is the wrong thing to search.
     - **The live thinking region.**
       - Rewrite `TestRegionScrollRepaintsViewport` (`region_scroll_test.go`) as `TestRegionScrollRepaintsTheBox`: `boxOut(m)` changes when `m.regionOffset[viewmodel.LiveThinkingID]` does.
       - Delete `TestWheelOverLiveRegionScrollsRegionAndRepaints`: it looks for the thinking box in `m.nodeRegions`, where it no longer is. `TestWheelOverTheBoxReasoningScrollsIt` (above) replaces it.
   - **The frame invariants.** Add a variant to `TestFrameInvariants` (`frame_invariants_test.go`): a busy model with a 12-item `m.workSnapshot` (built with `workRunningCard`), a 10-item list and a live beat.
   - **Any other failure in the TUI suite is a defect in this task.** The pure now-bar tests in `nowbar_test.go`, `jobs_test.go` and `tasks_test.go` still pass, because `planNowBar` is untouched until Task 10.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/...
go build ./... && go vet ./internal/app/tui/... && gofmt -l internal/
CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m; rm -f marshal
```

Expected: the TUI passes except the known failure, the build and vet are clean, and the usability scenarios pass.

## Task 7: A running call points at its row

**Goal:** a running call whose work has a row in the box shows `→ ⧉ #1` (or `→ ┆ job-2`) as its result, instead of a spinner and its own output.

**Files:** `internal/app/tui/steps.go`, `spine.go`, `viewport_render.go`, `spine_test.go`.

**Steps:**

1. **`stepRenderCtx`.** Add `pointer func(toolCallID string) string`. In `refreshViewport`'s `rctx`, set it to a function over `m.workSnapshot.Items`.
   - It returns the kind glyph and id (`⧉ #<SourceID>`, `┆ <DisplayID>` or `○ <DisplayID>`) of the first item where `viewmodel.WorkActive` holds and `Origin.ToolCallID` equals the argument.
   - For anything else, it returns "".
2. **`renderActiveCallRow` (`spine.go`).** When `c.pointer != nil` and `p := c.pointer(atc.ToolCallID)` is non-empty:
   - the result is `→ ` plus `p`, both in the kind colour: `AccentSecondary` for `⧉`, `StatusInfo` for `┆`, `FGMuted` for `○`;
   - skip the output tail, because the box shows the work.

   The sandbox line stays.
3. **Tests (`spine_test.go`):**
   - `TestRunningCallPointsAtItsBoxRow`: an active `agent.run` with call ID `g`, and a work item with `Origin.ToolCallID == "g"` that is running. The row shows `→ ⧉ #3` and no spinner.
   - `TestRunningCallWithoutWorkKeepsItsSpinner`.

**Verify:** run `TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/...`. Expected: it passes except the known failure.

## Task 8: Browse into the box

**Goal:** browse mode continues from the last history row into the box's stops. Enter, `s` and `f` act on them.

**Files:** `internal/app/tui/browse.go`, `livebox_view.go`, `browse_test.go`.

**Steps:**

1. **Box stops as browse items.**
   - A box stop becomes a `browseItem` whose `id` is a synthetic `viewmodel.NodeID{Kind: viewmodel.KindPassthrough, Key: "box:" + kind + ":" + key}`, where kind is `work`, `more` or `plan`. Its `kind` is `viewmodel.KindPassthrough`, its `start` and `end` are -1, and `jump` is false.
   - Add `boxStopID(t livebox.Target) viewmodel.NodeID` and `boxTargetOf(id viewmodel.NodeID) (livebox.Target, bool)` to `livebox_view.go`.
   - In `setBrowseItems`, append one item per `m.boxLayout().Stops` after the history items.
   - `scrollToCursor` does nothing for an item with `start < 0`. `paintCursor` skips such items, because the box paints its own cursor.
2. **The cursor inside the box.** `boxInput` sets `Cursor` to `boxTargetOf(m.cursor)` while browsing.
3. **`handleBrowseKey`.** When the cursor is a box stop:
   - **Enter:** for `TargetWork`, toggle `m.workExpanded`; for `TargetMore`, open the Work list; for `TargetPlan`, `scrollToNode(First)`.
   - **`s`,** on `TargetWork` when the item's `Capabilities.Stop` is set: return `m.boxStopCmd(key)`.
     - Add `boxStopCmd` next to `workStopCmd` in `model.go`. It calls `m.workService.StopWork(context.Background(), viewmodel.WorkTarget{ConversationID: m.workSnapshot.ConversationID, TaskKey: key}, "requested from the live box")` off the Update thread.
     - It returns a `boxStopMsg{taskKey string; stop viewmodel.WorkStopResult; err error}`. Its handler sets a flash: the error when there is one, else `stop.Summary` when `!stop.Accepted`, else `Stop requested`.
     - It does not reuse `workStopCmd`: that command's reply (`workActionMsg`) is dropped unless the Tasks panel that asked is open.
   - **`f`,** on an agent item: drill in with `drillIntoSubagent` when `m.state.Subagent(id)` has a child. `id` is the item's `SourceID`, parsed with `strconv.ParseInt`. This runs before the existing `subagentAtCursor` lookup, which finds no node for a box stop.
   - **`G` and End:** replace the `last.AnyLive()` check with an unconditional `m.viewportFollow = true` and `m.viewport.GotoBottom()` after moving. The live part is no longer a history node that could re-arm following.
   - **Every other key** acts as it does now. `i`, `y` and `o` find no node at a box stop (`currentNode` is nil) and flash as they already do for an empty cursor.
4. **Tests (`browse_test.go`):**
   - `TestBrowseJPastHistoryEntersTheBox`.
   - `TestBrowseEnterExpandsARunningCard`.
   - `TestBrowseSRequestsAStopOnlyWhenAllowed`. Use `fakeWorkService` from `pump_test.go`, which records `StopWork` calls.
   - `TestBrowseFOpensAnAgentFromTheBox`.
   - `TestBrowseEnterOnAPlanItemScrollsToItsBeat`.
   - `TestBrowseGFollowsTheLiveBox`.

**Verify:** run `TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/...`. Expected: it passes except the known failure.

## Task 9: Ctrl+T, `[tui.live_box]` and settings

**Goal:** Ctrl+T cycles the plan layout, `[tui.live_box] layout` sets the starting one, and `/settings` edits it.

**Files:**
- `internal/app/config/types.go`, `file_types.go`, `merge.go`, `save.go`, `defaults.go`, `config_test.go`;
- `internal/app/tui/settings/frames_tui.go`, `frames_tui_test.go`;
- `internal/app/tui/keypress.go`, `model.go`, `help/help.go`, `help/help_test.go`, `keypress_test.go` (new);
- the `ctrlT` callers: `model_test.go`, `todos_test.go`, `pump_test.go`, `commands_dispatch_test.go`, `sessionsheet_wiring_test.go` and `nowbar_test.go`.

**Steps:**

1. **Config.**
   - Add the Contracts' `LiveBox LiveBoxConfig` to `TUIConfig`. Its default is `Layout: "stacked"` in `defaults.go`.
   - Add `LiveBox *fileLiveBox` with `toml:"live_box"` to `fileTUI`, where `type fileLiveBox struct { Layout *string \`toml:"layout"\` }`.
   - In `merge.go`'s TUI block: `if lb := file.TUI.LiveBox; lb != nil { set(&cfg.TUI.LiveBox.Layout, lb.Layout) }`.
   - In `save.go`'s TUI block: `LiveBox: &fileLiveBox{Layout: strutil.Ptr(cfg.TUI.LiveBox.Layout)}`.
   - An unknown value is accepted and resolves to stacked at use (`livebox.ParseMode`), as `tui.depth` does.
2. **Settings (`frames_tui.go`).** Add an enum field after Depth:

   ```
   enumField("tui.live_box.layout", "Plan layout", []string{"stacked", "split", "folded"}, …)
   ```

   - `TomlPath` is `tui.live_box.layout`, with the description `where the plan sits in the live box (Ctrl+T cycles it)`.
   - Make it write-global, as the other fields are.
3. **The model.** In `New`, after the config is known, set `m.liveBoxMode = livebox.ParseMode(cfg.TUI.LiveBox.Layout)`.
4. **Ctrl+T (`keypress.go`).**
   - The `case "ctrl+t":` branch keeps its `readlineShortcutAvailable()` guard. In place of `m.toggleTasksPanel()`, it sets `m.liveBoxMode = livebox.Next(m.liveBoxMode, m.leftWidth)`.
   - It flashes `Plan: <mode>` with `m.setFlash`, re-renders the live part with `m.invalidateTranscript()` and `m.refreshViewport()`, and returns the flash command.
   - In `model.go`, delete the `case "ctrl+t":` arm of the global panel-switch block that calls `m.toggleTasksPanel()`. The `ctrl+b` arm stays.
5. **Help (`help/help.go`).** The `Ctrl+T` footer pair reads `plan`. Update `help_test.go`'s assertion from `tasks` to `plan`.
6. **Tests:**
   - `config_test.go`: `TestTUILiveBoxLayoutDefaultsToStacked` and `TestTUILiveBoxLayoutRoundTrips`.
   - `frames_tui_test.go`: `TestInterfaceFrameHasPlanLayoutEnum`.
   - `keypress_test.go`: `TestCtrlTCyclesThePlanLayout`. At 100 columns it goes stacked → split → folded → stacked. At 80 it goes stacked → folded.
   - **The `ctrlT` test helper** (`model_test.go`) pressed Ctrl+T to toggle the Tasks panel, and 20 calls in six test files use it. Ctrl+T no longer does that, so:
     - replace it with `toggleTasks(m Model) Model`, which calls `m.toggleTasksPanel()` and returns `m`;
     - rename every call;
     - Task 10 changes its body when `toggleTasksPanel` goes.
   - `TestCtrlTAndCtrlBSwitchBetweenPanels` (`sessionsheet_wiring_test.go`): rewrite it as `TestCtrlBSwitchesFromWorkToTheSheet`, which opens the Work list with `/work`.
   - Delete `TestCtrlTOpensTasksPanelListingEveryTodo` and `TestCtrlTWithoutTodosOnlyAddsNotice` (`model_test.go`).
   - `TestEscClosesTasksPanel` opens the panel with `/work` instead of `ctrlT`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pc-suite go test ./internal/app/config/... ./internal/app/tui/...
go build ./... && gofmt -l internal/
```

Expected: config passes, the TUI passes except the known failure, and the build is clean.

## Task 10: Delete the now bar, the Plan tab and acknowledgement

**Goal:** `nowbar.go` is gone. The Tasks panel is the Work list alone, opened by `/work`, and the TUI never acknowledges work.

**Files:**
- `internal/app/tui/nowbar.go` (deleted), `nowbar_test.go` (deleted), `box_helpers_test.go` (new);
- `jobs.go`, `jobs_test.go`, `todos.go`, `todos_test.go`, `model.go`, `click.go`, `view.go`, `spine.go`, `status.go`, `status_test.go`;
- `commands_dispatch.go`, `commands_dispatch_test.go`, `pump_test.go`, `tasks_test.go`, `model_test.go`.

**Steps:**

1. **Before deleting `nowbar_test.go`,** move its helpers that other test files still use into `box_helpers_test.go`, unchanged:
   - `registerRunningSubagent` (`frame_invariants_test.go`, `spinner_test.go`);
   - `workRunningCard` (Task 6's frame-invariant variant);
   - `workSnapOf` (`todos_test.go`).

   Its other helpers have no callers left once Task 6's rewrites and this task's deletions are done. `nowBarOut`, `nowBarBase` and `nowBarAgents` in particular are gone: a remaining caller is a test that Task 6 missed, and it is rewritten against `boxOut`. Then delete `nowbar_test.go` and `nowbar.go`.
2. **What `nowbar.go` provided.**
   - Move `oneLine` and `nowBarFlatten` into `spine.go` unchanged.
   - `spine.go`'s `renderPlanRow` calls `livebox.Blocks` instead of `progressBlocks`.
   - `todos.go` calls `progress.Clean(s, limit, false)` where it called `workClean`.
3. **Dead model state.**
   - Delete `nowBarMemo`, `workHits`, `recordWorkPlan`, `nowBarBand` and `handleNowBarClick`.
   - Delete `ShouldShowStatusURL`. The status line always shows the browser segment while `BrowserInfo().SessionOpen`.
   - Delete `runningJobs` and `runningWatches` (`jobs.go`). `workLaneActive` stays: the Work list still uses it.
4. **The Tasks panel becomes the Work list (`todos.go`).**
   - Delete `tasksTabPlan`, `tasksDoc`, `toggleTasksPanel`, and the tab switching in `activeTab` handling (the `if p.activeTab == tasksTabPlan` branches). Also delete `viewString`'s `m.tasksPanel.SetSource(…)` line.
   - With one tab left, delete the tab state too: the `workTab` type and its constants, the panel's `activeTab` and `previousTab`, and the model's `tasksPanelTab` and `tasksPanelUsed`.
   - The panel's title is `Work`.
   - Rename `openTasks(tab workTab)` to `openWork()`, and update its callers: `/work` in `commands_dispatch.go`, and the box's more row.
     - When `!m.workLaneActive()`, it adds the system message `No work in this session.` instead of opening. The list of todos no longer matters to it.
     - With the panel already open, it does nothing but refresh.
   - The test helper `toggleTasks` (Task 9) keeps its toggle. When `m.tasksOpen()`, it closes the panel with `m.dock.CloseNow()` and sets `m.tasksPanel = nil`; otherwise it calls `m.openWork()`.
5. **Acknowledgement.**
   - In the Work list's detail actions, delete the `it.Capabilities.Acknowledge` block ("Mark reviewed" and "Dismiss").
   - Delete `workReqAck`, `workAckCmd` and their case in `handleWorkRequest`.
6. **Tests.**
   - Delete from `jobs_test.go`: `TestRunningJobsKeepsDetachedRunningAndStoppingOnly`, `TestWorkCardsGenericTestsDownloadTransferCustom` and `TestWorkCardsUnknownStalePartialAndStopping`.
   - Delete from `todos_test.go`: `TestWorkTasksInitialAndRememberedTab` and `TestWorkTasksPlanKeepsChildStats`. Also delete `TestTasksDocShowsStepCountsAndWorkTime` (`tasks_test.go`).
   - Rename `TestWorkStopAndAckStayAsyncAndExact` (`commands_dispatch_test.go`) to `TestWorkStopStaysAsyncAndExact`, without its acknowledgement half.
   - `TestWorkTasksInspectNeverAcknowledges` stays.
   - Tests open the panel through `toggleTasks` or `/work`. A test that switched to the Work tab after opening drops the switch, because the panel has one view.
   - In `status_test.go`:
     - delete `TestShouldShowStatusURLReturnsFalseWhenStripShowsBrowser` and `TestShouldShowStatusURLReturnsTrueWhenNoBrowserSession`;
     - rewrite `TestStatusLineBrowserSegmentFollowsNowBar` as `TestStatusLineShowsTheBrowserWhileASessionIsOpen`.
   - Any other failure is a defect in this task.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pc-suite go test ./internal/app/tui/...
go build ./... && go vet ./internal/app/tui/... && gofmt -l internal/
grep -rn "nowBar\|planNowBar\|tasksTabPlan\|workAckCmd\|AcknowledgeWork" internal/app/tui --include='*.go'
```

Expected:
- the TUI passes except the known failure, and the build and vet are clean;
- the grep prints nothing, apart from comments that explain the removal. Rewrite those comments so they no longer name the removed symbols.

## Task 11: Verification, docs and changelog

**Goal:** the phase is verified, and the spec, the folder README, the user README and the changelog describe what shipped.

**Files:** `docs/tui-simplification/feature-spec.md`, `docs/tui-simplification/README.md`, `README.md`, `CHANGELOG.md`.

**Steps:**

1. **Full verification.**

   ```bash
   cd /Users/alecpullen/projects/marshal
   TMPDIR=/private/tmp/pc-suite CGO_ENABLED=1 go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
   go vet ./... && gofmt -l .
   CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m; rm -f marshal
   cd web/ui && npm test
   ```

   Expected: every failure is in the baseline list, vet is clean, `gofmt -l .` prints nothing, and the usability scenarios and vitest pass.
2. **Run the TUI by hand** (`go run ./cmd/marshal`) on a scratch repository at 80×24 and at 120×40. Ask for a task that writes a todo list, starts a background `shell.run` with `progress`, and loads a skill. Confirm:
   - the box shows the live beat, the running row with its bar, and the plan;
   - Ctrl+T cycles the layouts;
   - the beat moves into history when it settles;
   - the skill load shows once.

   Record what was seen in the commit message.
3. **Spec.** Set the status line to "P-A, P-B and P-C landed" with this plan's commit range.
4. **README.** Find the sentence that describes the now bar or Ctrl+T's Tasks panel (search for "now bar" and "Ctrl+T") and replace it with:

   > While the agent works, a box above the input shows the step in progress, everything running in the background (subagents, jobs, downloads and tests with their progress bars) and the plan. Ctrl+T moves the plan between a stacked list, a column beside the work, and a one-line summary; `[tui.live_box] layout` picks the starting one. `/work` lists all work.

   If neither phrase appears, add that paragraph to the TUI section.
5. **CHANGELOG**, under the unreleased section:
   - **Changed:**

     > **The live box.** Everything that can still change sits in one bordered box above the input: the step in progress, running subagents, jobs and progress cards (now with measured bars, rates and ETAs), and the plan. History above it only grows. Ctrl+T cycles the plan between stacked, split and folded; `[tui.live_box] layout` sets the start.

   - **Removed:**

     > The now bar and its live-mirror row; the Tasks panel's Plan tab (the plan is always in the box; `/work` opens the Work list); "Mark reviewed" and "Dismiss" in the Work list.

   - **Fixed:**

     > A skill the agent loads shows once, as its call row, instead of a second row that also split the step. Running work rows now show a measured rate and ETA.
6. **Commit** `README.md`, `CHANGELOG.md` and `docs/tui-simplification/` as explicit paths. Merge `p-c-live-box` only when the user asks.

**Verify:** step 1's commands. `git status --short` shows only the unrelated untracked docs that were there before.

## Integration notes

- **Daemon P2 plan 3** moves the renderer onto `WireNode`. The box reads the band and the work snapshot, which P2 plans 2 and 3 carry. `livebox` takes no `viewmodel.Node`, only pre-rendered live lines and `WorkItem`s, so it ports without change.
- **P-D** is unaffected. Its skill teaches the `progress` record that Task 3's rows draw.
- **The web** still reads `band.entities` and `band.todos`, which this phase leaves unchanged.

## Self-review

- **Cold-executor test.** Every row shape, border, shed step and key is stated with its exact text and order. The judgment steps (Tasks 6, 8 and 10) name each function they change and each test they rewrite or delete.
- **Contracts.**
  - The `livebox` API is used by Tasks 3 to 10, and the model fields and `livebox_view.go` functions by Tasks 6 to 10.
  - `stepRenderCtx.pointer` is used by Task 7, and `LiveBoxConfig` by Task 9.
- **Anchors.**
  - Every file, symbol and test named was checked at `2325ca73`, then again at `d7d9b39c` after P-B merged. P-B landed its Contracts' names unchanged.
  - The test lists come from searching the TUI tests for now-bar, Tasks-panel, `ctrlT` and acknowledgement uses, and from the history-only probe described in the header.
- **Scope.** Tasks 6 and 10 are large but stay inside `internal/app/tui`. Task 9 touches config and settings with one wiring line in the TUI.
- **Parallel safety.** No task is marked parallel-safe.
- **Placeholders.** None. The two by-hand checks are Task 5's frame review and Task 11's manual run, and each has a stated checklist.
- **Agreement with the spec.** The spec is updated for decisions 5, 9 and 10, which correct or extend it: the status line's timer, the Go test package count, and Ctrl+T and acknowledgement.
