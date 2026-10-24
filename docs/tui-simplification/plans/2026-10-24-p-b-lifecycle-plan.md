# P-B lifecycle in the projection — implementation plan

Date: 2026-10-24. Status: ready for review.
Base: `2325ca73` (`main`, P-A landed).
Approved design: [feature-spec.md](../feature-spec.md) §5.4, §6.1 (completion events, one projection, band, the live beat, the property to test), §6.2, §7 (P-B) and §8. The spec was updated on 2026-10-24 with the decisions listed below.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations. Every step is an instruction against anchors (files, symbols, test names) checked at the base commit. Executing this plan also tests how well Marshal follows one, so each step states its decision instead of leaving it to the executor. "As `X` does" means `X` exists at the base commit and is the model to copy.

**Goal:** finished background work leaves one completion event in history, and history only grows.
- A background job's or subagent's end appends one completion event: kind and id, label, outcome, final measurement and duration, and a summary line. Expanded, the event is an issue report.
- The call that launched the work is an ordinary call row (`┆ job-3 started`) and never changes again.
- One projection serves the TUI, ACP and the web. In-place subagent cards, settled job cards, `Snapshot.LiveWork` and marker merging are gone.
- The band shows the plan whenever a list exists, without the gating rules.
- History rows read only the transcript, so a rebuilt tree keeps every settled row's ID and version. `SplitLive` separates the live beat, and a property test pins the rule.
- Streaming reasoning is drawn once, at the bottom of the transcript, never between a beat's head and its calls.
- The web renders completion events and plan lines, and loses folding and job cards.

```
 ● Running the harness tests.                                              ▸ 3/3
   └ shell   go test -json ./evals/...                            ┆ job-3 started

 ┆ job-3 Harness tests · exit 1 · 210 passed, 2 failed · 48s                  ▹
     --- FAIL: TestThresholdsForCoderModels/context_window
```

Expanded, the event becomes the issue report:

```
 ┆ job-3 Harness tests · exit 1 · 210 passed, 2 failed · 48s                  ▿
     outcome   exit 1
     command   go test -json ./evals/...
     cwd       evals
     duration  48s
     progress  210 passed, 2 failed
     output    --- FAIL: TestThresholdsForCoderModels/context_window
               FAIL marshal/evals 2.1s
```

(Drawn by hand to show the layout. No prototype rendered it.)

**Non-goals:** everything the later phases own.
- P-C: the live box. The TUI starts using `SplitLive` there. Also P-C: running rows; deleting the now bar; the attention row and acknowledgement (`WorkService.AcknowledgeWork` keeps its TUI callers until the now bar and the Tasks panel's Plan tab go); and the idle plan display (`BandInfo.Todos` keeps its busy gate).
- P-D: the `progress-reporting` skill.
- The web layout. The web keeps its density ladder and its todo divider. It renders the new kinds and loses folding and the job card, nothing more.
- Keyboard design. Every key stays provisional.

**Assumptions:**
- On macOS, `t.TempDir()` under the default `/var` TMPDIR breaks unrelated tests, so every Go test command below runs with `TMPDIR=/private/tmp/pb-suite`. Create the directory first: `mkdir -p /private/tmp/pb-suite`.
- These tests fail at the base commit on this machine (measured 2026-10-24). They are not caused by this plan, and no task's Verify expects them to pass:
  - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`. This is "the known TUI failure" in each Verify.
  - `internal/app`:
    - `TestAccessTUIOptionsResolveWithoutConfigReload`, `TestDefaultPromptBudget` and `TestPipelineRegistryCommandsCaptureDispatchWorktree`;
    - `TestRuntimeNewAndResumedSessionResetAccess`, `TestSubagentChildRegistryBorrowsConversationService` and `TestSubagentShellOutputDoesNotReachParent`;
    - `TestRuntimeAccessSerializesWithReloadAndQuiesce`, which is flaky.
  - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`, and `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity` (environmental).
  - `internal/viewmodel` and `internal/acp` pass at the base.
- Node and npm are installed for `web/ui`. Its tests run with `npm test` (vitest).
- Daemon P2 plan 3 has not started, so nothing else is moving the projection.

**Decisions this plan makes beyond the spec** (all now recorded in the spec):
1. **Where an event lands.**
   - An event always follows the beat whose row launched its work.
   - Any other event that ends while a beat is still open goes *before* that beat, and one that ends after the beat settled goes after it.
   - A beat settles when the next block that does not merge into it starts, or when its turn is no longer running.
   - Today every completion that lands inside a beat is held until after the beat. For work the beat did not launch, that order changes when the beat settles, so it breaks the append-only rule.
   - Putting every such event before the beat would show a job that finished quickly above its own launch row.
2. **Lost jobs get an event** with outcome `lost`. A lost exit is synthesised on resume and stamped with the job's start, so the event follows the beat that launched the job.
3. **No jump to the launch row.**
   - Enter or a click expands the event.
   - The marker's jump (`clickTarget.jumpTo`) is deleted, and so is the web's `onSelect` jump from a marker.
4. **Outcome words.** They are `done`, `exit N`, `stopped`, `lost` and `failed`, plus `outcome unknown` (in warning colour) for a record with no observed exit and no stop or failure state. A missing exit never reads as success, as today's job card already rules.
5. **History reads only the transcript.** Three things today read the session's *current* todo list, so a later rewrite changes rows that already settled:
   - **The todo tag** takes its `N/M` and text from the plan in effect: the plan left by the write that re-bound the beat (next bullet), else the newest plan line before the beat. The current list is the fallback only when no plan with an active item is known.
   - **A narrated beat whose only call is a `todo.write`** is re-bound to the stored todo of the same actor's next step in the same turn, instead of the list item the write named.
   - **The receipt's task count** is the number of items the turn's plan lines moved to completed.
6. **One projection everywhere.**
   - ACP never had a `session/stack` parameter that selects live work: it always served the legacy projection, so it switches in Task 3. Spec §6.1 said otherwise, and is corrected.
   - `Snapshot.Drilled` goes with `LiveWork` and `RunningSubagent`.
   - No tree carries subagent nodes any more, so `WireSubagent` and the web's `SubagentCard` go too.
7. **Band entities from the transcript.** The web's now bar lists running work from `band.entities`, which today walks the in-place cards. The entities now come from the snapshot's items: running subagent views, and background starts without an exit.
8. **Band gating.**
   - `DrivenByTodos`, `MinTaskTodos` and `taskMode` go, so progress shows whenever a list exists.
   - `BandInfo.Todos` keeps its "busy and unfinished" gate. The idle plan state belongs to P-C's box.
9. **Drill-in from rows.**
   - Completion events carry the subagent's ID, and `WireTool` gains `subagentId` from the `agent.run` row that started a subagent.
   - The TUI's `f` and the web's `f` drill in from either.
   - The `StartedSubagent` reader moves from the TUI into `viewmodel`.
10. **The issue report renders from the projection** (`CompletionInfo`), not from a `WorkService.InspectWork` call in the render path.
    - Resume renders the same report.
    - `i` opens the inspector with the full retained output.
    - `o` is not bound on an event.
11. **`SplitLive(turns, busy)` returns a slice.**
    - While busy, the live part is the newest turn's last beat, plus the events after it. Those events are only ever events of work that beat launched (decision 1).
    - The streaming reasoning node, when there is one, is always the last element of the live part. With no live beat, it is the whole live part.
    - The beat is live whether or not its current step has ended, because a narrationless step can still merge into it.
    - The spec's `live *Node` becomes `live []*Node`, so the events stay with their beat until it settles.
12. **Summary and measurement rules** are pinned in Task 1, so the TUI and the web print the same words.
13. **Streaming reasoning sits at the bottom** (added after testing found it splitting a beat's tree).
    - No beat carries in-progress reasoning. It is one node, the newest turn's last child, below every beat, call row and completion event, so a reader can glance at it or ignore it.
    - A step that has opened but holds nothing but reasoning has no node until its first narration or call.
    - In P-C the same node is the last row of the box.

**Execution:**
- Branch first: `git switch -c p-b-lifecycle`.
  - From Task 1 until Task 9 the web does not render completion events, and from Task 3 it has no subagent cards.
  - Merge to `main` only after Task 10.
- Run the tasks in order. Run each task's Verify and commit it before starting the next, as `p-b-lifecycle: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path: the checkout holds unrelated untracked docs.
- No task is parallel-safe:
  - Tasks 1, 2, 3, 6 and 7 all edit `internal/viewmodel/stack.go`, and Task 8 tests their combined behaviour.
  - Tasks 1 to 5 edit `internal/app/tui`.
  - Task 9 consumes the wire from Tasks 1 to 3 and 7.
- "Delete test X" means delete the whole function. "Rewrite test X as Y" means replace the function with one named Y that asserts what the step says.

## Contracts

Declarations that more than one task uses, each created by the task named.

**Task 1, `internal/viewmodel/stack.go`.** `KindMarker` is renamed in place, so every later constant keeps its value.

```go
const (
	KindTurn Kind = iota
	KindStep
	KindTool
	KindMessage
	KindFinal
	KindSubagent
	KindRunEvent
	KindJobExit
	KindThinking
	KindPassthrough
	KindReceipt
	KindCompletion // was KindMarker; wire name "completion"
	KindPlanUpdate
)

type Node struct {
	// … existing fields; Marker is removed (Task 1), Job is removed (Task 2) …
	Completion *CompletionInfo // set for KindCompletion
}

// mergeBeats gains open: true when the turn is the newest one and the
// session is busy, so its last beat is still open.
func mergeBeats(nodes []*Node, open bool) []*Node
```

**Task 1, `internal/viewmodel/completion.go` (new).**

```go
// The two sources of a completion event.
const (
	CompletionJob      = "job"
	CompletionSubagent = "subagent"
)

// Outcome words that are not an exit code.
const (
	OutcomeDone    = "done"
	OutcomeStopped = "stopped"
	OutcomeLost    = "lost"
	OutcomeFailed  = "failed"
	OutcomeUnknown = "outcome unknown"
)

// ReportTailLines bounds the output an issue report shows.
const ReportTailLines = 20

// CompletionInfo is one finished piece of background work at the moment it
// ended: a background job's exit or a subagent's end.
type CompletionInfo struct {
	Source     string // CompletionJob or CompletionSubagent
	ID         string // "job-3", or "#3" for a subagent
	SubagentID int64  // the subagent's ID; 0 for a job
	Label      string // job: its progress title, else its command's first line; subagent: its label
	Outcome    string // OutcomeDone, "exit N", OutcomeStopped, OutcomeLost, OutcomeFailed or OutcomeUnknown
	Failed     bool   // true for every outcome except OutcomeDone
	ExitCode   *int   // the observed exit code; nil when none was observed
	StopCause  string
	StartedAt  time.Time
	EndedAt    time.Time     // zero for a lost job
	Duration   time.Duration // a job's recorded run time, or a subagent's EndedAt − StartedAt
	Measure    string        // the final measurement (MeasureSummary); "" when none
	Summary    string        // the one line under the head; "" when none

	// The issue report's fields.
	Command         string // job: the whole command
	Cwd             string // job: the working directory, when recorded
	Output          string // job: the retained output tail
	Truncated       bool   // job: the retained output hit its cap
	Report          string // subagent: the final report
	Error           string // subagent: the failure text
	WorkKey         string
	ExecutionID     string // job
	ReportMessageID int64  // subagent: the persisted report message; 0 when unavailable

	// Launch is the row that launched the work: a job's start row, or the
	// agent.run row that started the subagent. Zero when none is found.
	Launch NodeID
}

func MeasureSummary(p *progress.Snapshot) string
func ReportHeadline(summary string) string
func ReportTail(output string) []string
```

**Task 1, `internal/viewmodel/cards.go`.**

```go
// StartedSubagent is the subagent an agent.run call started, 0 when it
// started none. Moved unchanged from internal/app/tui/spine.go.
func StartedSubagent(ev registry.AuditEvent) int64
```

**Task 1, `internal/viewmodel/wire.go`.**

```go
type WireNode struct {
	// … Marker is removed (Task 1), Subagent is removed (Task 3) …
	Completion *WireCompletion `json:"completion,omitempty"`
}

type WireCompletion struct {
	Source          string `json:"source"`
	ID              string `json:"id"`
	SubagentID      int64  `json:"subagentId,omitempty"`
	Label           string `json:"label"`
	Outcome         string `json:"outcome"`
	Failed          bool   `json:"failed,omitempty"`
	ExitCode        *int   `json:"exitCode,omitempty"`
	StopCause       string `json:"stopCause,omitempty"`
	StartedAt       int64  `json:"startedAt,omitempty"`
	EndedAt         int64  `json:"endedAt,omitempty"`
	DurationMs      int64  `json:"durationMs,omitempty"`
	Measure         string `json:"measure,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Command         string `json:"command,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	Output          string `json:"output,omitempty"`
	Report          string `json:"report,omitempty"`
	Error           string `json:"error,omitempty"`
	WorkKey         string `json:"workKey,omitempty"`
	ExecutionID     string `json:"executionId,omitempty"`
	ReportMessageID int64  `json:"reportMessageId,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
}
```

**Task 1, `internal/app/tui/steps.go`.** Task 1 draws the head and summary. Task 5 adds the expanded report.

```go
func renderCompletion(c *viewmodel.CompletionInfo, expanded bool, width int, workdir string) string
```

**Task 3, `internal/viewmodel/stack.go` and `wire.go`.**

```go
type Snapshot struct {
	Items       []session.TranscriptItem
	Steps       []session.Step
	ActiveTools []session.ActiveToolCall
	InProgress  session.InProgressMessage
	Busy        bool
	Todos       []db.TodoItem
	Now         time.Time
	// Drilled, LiveWork and RunningSubagent are removed.
}

type WireTool struct {
	// … Job is removed (Task 2) …
	SubagentID int64 `json:"subagentId,omitempty"`
}
```

**Task 8, `internal/viewmodel/live.go` (new).**

```go
// SplitLive separates the live part (the open beat, the events of the work
// it launched, and the streaming reasoning, always last) from history. It
// never mutates turns.
func SplitLive(turns []*Node, busy bool) (history []*Node, live []*Node)
```

## Task 1: Completion events replace markers

**Goal:** every finished job and subagent becomes one `KindCompletion` node with a `CompletionInfo`. It is placed by the new rule, projected as `WireCompletion`, and drawn by the TUI as a head plus a summary line.

**Files:**
- `internal/viewmodel/completion.go` (new), `internal/viewmodel/completion_test.go` (new);
- `internal/viewmodel/cards.go`, `stack.go`, `beats.go`, `wire.go`;
- `internal/viewmodel/cards_test.go`, `hardening_test.go`, `wire_test.go`;
- `internal/app/tui/steps.go`, `spine.go`, `viewport_render.go`, `browse.go`, `click.go`;
- `internal/app/tui/cards_render_test.go`, `scrub_render_test.go`, `click_test.go`.

**Steps:**

1. **`completion.go`.** Create it in package `viewmodel` with the Contracts' constants and `CompletionInfo`, plus these functions.
   - **`ReportHeadline(summary string) string`.** The first sentence of the trimmed summary (`FirstSentence`), with emphasis stripped (`StripEmphasis`) and one leading ATX heading marker removed: one to six `#` followed by a space, and its padding.
     - The logic is `subagentSummaryHeadline` and `stripMarkdownHeading` in `internal/app/tui/steps.go`. Copy both; the TUI copies are deleted in Task 4.
     - Name the unexported copy `stripHeading`.
   - **`ReportTail(output string) []string`.**
     - Split the output with its trailing newlines trimmed. Empty output returns nil.
     - Failure lines are the lines that contain `--- FAIL`, in order, at most 10.
     - With no failure lines, return the last `ReportTailLines` lines.
     - Otherwise return the failure lines, then the last `ReportTailLines − len(failures)` lines that are not failure lines, in their original order.
   - **`MeasureSummary(p *progress.Snapshot) string`.** A nil snapshot returns "". Otherwise build up to two parts and join them with `" · "`:
     - **Phase.** When `p.Phase != nil && p.Phase.Total > 0`, the part is `"<Index>/<Total> <label>"`. The label is `Phase.Label`, else `Phase.ID`.
     - **Metric.** The primary metric is the one whose ID equals `PrimaryMetricID`, else the first metric. With no metrics there is no metric part.
       - When the primary has a `Total`: for unit `"bytes"`, `"<cur>/<total>"` in IEC form; otherwise `"<cur>/<total> <unit>"`.
       - When there are two or more metrics and none has a total: each metric whose `Current > 0` as `"<n> <label>"`, joined with `", "`. The label is the metric's label lower-cased, else its ID. This gives `"210 passed, 2 failed"` for the Go test adapter.
       - Otherwise: for `"bytes"`, the current value in IEC form; else `"<cur> <unit>"`.
     - **Numbers.** Print an integer without a decimal; otherwise print one decimal (`strconv.FormatFloat(v, 'f', 1, 64)`).
     - **IEC form.** The same output as `workBytesIEC` in `internal/app/tui/nowbar.go`: below 1024, `"%.0f B"`; otherwise divide by 1024 into KiB, MiB, GiB, TiB or PiB and print `"%.1f <unit>"`. Name it `bytesIEC`.
     - Pass every label through `progress.Clean(s, 80, false)`.
   - **`jobCompletion(e *session.JobExit, start *registry.AuditEvent) CompletionInfo`**, unexported. `start` is the paired start audit, or nil.
     - `Source` is `CompletionJob` and `ID` is `e.ID`.
     - **Command.** `ToolTarget(*start)` when `start != nil`, else `e.Command`.
     - **Label.** `e.ProgressConfig.Title` when the config is non-nil and the title is non-empty. Otherwise the command's first line.
     - **Code.** Use `e.Exit`. When it is nil, `!e.Lost` and `e.ExitCode >= 0`, use a copy of `e.ExitCode`.
     - **Outcome**, first match wins:
       - `e.Lost`: `OutcomeLost`.
       - Code 0 and `e.State` is not `"failed"`, `"killed"` or `"timed_out"`: `OutcomeDone`.
       - Any code: `fmt.Sprintf("exit %d", code)`.
       - `e.StopCause != ""`: `OutcomeStopped`.
       - `e.State` is `"failed"`, `"killed"` or `"timed_out"`: `OutcomeFailed`.
       - Otherwise: `OutcomeUnknown`.
     - `Failed` is `Outcome != OutcomeDone`, and `ExitCode` is the code.
     - **Times.** `StartedAt` is `e.StartedAt`, else `start.Timestamp` when `start != nil`. `EndedAt` is `e.EndedAt`, else `e.At`, and zero when lost. `Duration` is `e.Duration`.
     - `Measure` is `MeasureSummary(e.Progress)`.
     - **Summary**, first match wins:
       - the first output line containing `--- FAIL`, trimmed;
       - `e.Progress.Detail`, when the snapshot is non-nil and the detail is neither empty nor `"go test"`;
       - the last non-blank output line, trimmed, that is not `[output truncated]`;
       - "".
     - The report fields: `Cwd` is `e.Cwd`, `Output` is `e.Output`, `Truncated` is `e.Truncated` and `ExecutionID` is `e.ExecutionID`.
     - `Launch` is `ToolID(*start)` when `start != nil`.
     - **WorkKey.** When `e.ExecutionID != ""`: `WorkKey(e.ConversationID, epoch, workKindCommand, e.ExecutionID)`, where `epoch` is `e.SourceEpoch`, else `legacyWorkEpoch`.
   - **`subagentCompletion(v *session.SubagentView, launch NodeID) CompletionInfo`**, unexported. `Launch` is `launch`.
     - `Source` is `CompletionSubagent`, `ID` is `fmt.Sprintf("#%d", v.ID)`, `SubagentID` is `v.ID` and `Label` is `v.Label`.
     - **Outcome.** `SubagentFailed` gives `OutcomeFailed`. `SubagentDone` gives `OutcomeStopped` when `v.Outcome == "stopped"` or `v.StopRequested`, else `OutcomeDone`.
     - `Failed` is `Outcome != OutcomeDone`.
     - **Times.** `StartedAt` and `EndedAt` come from the view, and `Duration` is `EndedAt − StartedAt` when both are set.
     - **Summary.** `ReportHeadline(v.Summary)`. When that is empty and the subagent failed, the first line of `v.Error`.
     - The rest: `Report` is `v.Summary`, `Error` is `v.Error`, `WorkKey` is `v.WorkKey` and `ReportMessageID` is `v.Report.MessageID`.
   - **`completionNode(c CompletionInfo, refKey string) *Node`**, unexported. It replaces `markerNode`.
     - Scrub the model-authored text in one place, as `markerNode` does. `ScrubText` takes `Label`, `Summary`, `StopCause` and `Cwd`. `ScrubBlock` takes `Command`, `Output`, `Report` and `Error`.
     - The node is `&Node{ID: NodeID{KindCompletion, "completion:" + refKey}, Kind: KindCompletion, Completion: &c}`.
     - Its `Version` hashes every field of `c`, with `newHash` as `versionOfMarker` does.
2. **`cards.go`.**
   - Delete `MarkerInfo`, `markerNode`, `markerKey`, `versionOfMarker` and `coalesceMarkers`.
   - Add `StartedSubagent`, moved unchanged from `startedSubagent` in `internal/app/tui/spine.go`. It reads the notice's structured `SubagentWork` reference first, then falls back to `"started as subagent %d"` in the summary.
   - In `cardIndex`, replace `markers []block` with `done []doneEvent`, where `type doneEvent struct{ at time.Time; node *Node }`.
   - Add `starts map[NodeID]registry.AuditEvent`: the start row's ID mapped to its audit. Fill it wherever `indexCards` records a start, in the identity pass and in the legacy pass.
   - **Find each subagent's dispatch row.** Before the last loop, collect every audit with `StartedSubagent(ev) > 0`. For a view `v`, the dispatch row is:
     - the audit whose `dispatchWorkKey(&ev)` equals `v.WorkKey`, when `v.WorkKey != ""`;
     - else the last audit whose `StartedSubagent(ev) == v.ID` and whose timestamp is not after `v.EndedAt`;
     - else none (the zero `NodeID`).
   - In `indexCards`'s last loop, replace the marker construction:
     - **A finished subagent** (status not running, `EndedAt` set) gives `doneEvent{at: v.EndedAt, node: completionNode(subagentCompletion(v, launch), fmt.Sprintf("sub:%d", v.ID))}`. `launch` is the `ToolID` of its dispatch row.
     - **Every job exit, a lost one included.** With `ref, ok := ci.exitRef[j]`:
       - `refKey` is `ref.Key` when paired, else `fmt.Sprintf("job:%s@%d", j.ID, j.At.UnixNano())`.
       - `start` points at a copy of `ci.starts[ref]` when paired (`ev := ci.starts[ref]; start = &ev`; a map element has no address), else nil.
       - The event is `doneEvent{at: j.At, node: completionNode(jobCompletion(j, start), refKey)}`.
     - Sort `ci.done` by `at`, stably.
   - Rename `markersIn` to `completionsIn(from, to time.Time) []block`, with the same window. Each block's `ts` is the event's `at`.
3. **`stack.go`.**
   - Rename `KindMarker` to `KindCompletion` in the `Kind` block.
   - Replace the `Marker *MarkerInfo` field with `Completion *CompletionInfo`.
   - In `Build`, call `ci.completionsIn(from, to)` where it calls `ci.markersIn`, and update the comment above it.
   - In `buildTurn`:
     - rename the `markers` parameter to `completions`, and update the "Completion markers join the turn" comment;
     - call `mergeBeats(out, s.Busy && lastTurn)`;
     - delete the `out = coalesceMarkers(out)` line.
4. **`beats.go`. Rewrite `mergeBeats(nodes []*Node, open bool)` with the placement rule.** Keep the open beat *pending*, not yet in `out`, and hold the completions that arrive while it is pending.
   - A `KindCompletion` node with a pending beat is appended to `held`. With no pending beat, it is appended to `out`.
   - A step that merges (the existing narration, actor and todo test) is absorbed as today.
   - Any other node settles the pending beat: append the beat's `before` group, then the beat, then its `after` group, then clear `held`.
     - The node is then handled: a step becomes the new pending beat; anything else is appended.
   - At the end, a pending beat settles the same way.
   - **The groups.** Split `held`, in order, into `after` and `before`. A completion goes in `after` when either holds:
     - its `Completion.Launch` is the ID of one of the pending beat's children;
     - this is the end of the nodes, `open` is false, the beat's `Step.Step.EndedAt` is set, and the completion's time is later than that end.

     Every other completion goes in `before`.
   - A completion's time is `Completion.EndedAt`, or `StartedAt` when that is zero.
   - Rewrite the doc comment to state the rule:
     - an event follows the beat that launched its work;
     - any other event that ends while the beat is still open comes before it, because the beat joins history only when it settles;
     - an event after a finished turn's last beat ended follows it.
5. **`wire.go`.**
   - Replace `WireMarker` with the Contracts' `WireCompletion`, and the `Marker` field with `Completion`.
   - In `kindNames`, replace `KindMarker: "marker"` with `KindCompletion: "completion"`.
   - In `wireNode`, replace the `n.Marker != nil` case with a `n.Completion != nil` case that copies every field:
     - times through `ms`, and `DurationMs` from `Duration.Milliseconds()`;
     - `Output` through `capTail`, setting `Truncated` when it cut, or when `c.Truncated`;
     - `Report` through `capText`, setting `Truncated` when it cut.
6. **TUI wiring.**
   - **`steps.go`.** Replace `renderMarker` with `renderCompletion(c, expanded, width, workdir)`.
     - **Glyph.** `glyph.Job` in `theme.Current().StatusInfo` for a job; `glyph.Agent` in `theme.Current().AccentSecondary` for a subagent.
     - **The head.** `gutterPrefix(glyph, colour)`, then the ID in the same colour, a space, and the label: `viewmodel.ScrubText(viewmodel.ElideWorkdir(c.Label, workdir))`.
     - **After the label**, each joined with `dimSeparator`:
       - the outcome, in `statusOkStyle()` for `OutcomeDone`, `warningStyle()` for `OutcomeUnknown` and `errorStyle()` otherwise;
       - `c.Measure`, muted, when set;
       - `formatElapsed(c.Duration)`, muted, when the duration is positive.
     - **Width.** Truncate the head with `ansi.Truncate` to `width − gutterWidth − 2` cells. Pad with spaces so that `glyph.DisclosureCollapsed` (or `glyph.DisclosureExpanded` when expanded), muted, sits in the last column.
     - **Summary line.** When `c.Summary != ""`, add a second line: `nestedBodyIndent` spaces, then the muted summary truncated to `width − nestedBodyIndent`.
     - In this task, `expanded` changes only the disclosure glyph.
   - **`viewport_render.go`.**
     - In `drawNode`, replace the `KindMarker` case with `n.Kind == viewmodel.KindCompletion && n.Completion != nil`, which returns `renderCompletion(n.Completion, m.expandedUnder(n.ID, m.expandAll), width, c.workdir), nil`.
     - In `blockTarget`, replace the marker case with `n.Kind == viewmodel.KindCompletion` returning `&clickTarget{node: n.ID}`.
   - **`browse.go`.** In `navigable`, replace `viewmodel.KindMarker` with `viewmodel.KindCompletion`.
   - **`spine.go`.** Delete `startedSubagent`, and call `viewmodel.StartedSubagent` in `callResult`.
   - **`click.go`.** Delete the `jumpTo` field of `clickTarget`, the `case target.jumpTo.Key != ""` branch in the click handler, and `regionFor`, whose only callers are that branch and the test deleted below.
7. **Tests.**
   - **`completion_test.go` (new):**
     - `TestJobCompletionOutcomes`, a table:
       - exit 0 with state `completed` is `done`, not failed;
       - exit 0 with state `killed` is `exit 0`, failed;
       - exit 3 is `exit 3`;
       - no exit with `StopCause: "turn stop"` is `stopped`;
       - no exit with state `failed` is `failed`;
       - a legacy `ExitCode: -1` record with no state is `outcome unknown`, failed;
       - `Lost` is `lost` with a zero `EndedAt`.
     - `TestSubagentCompletionOutcomes`: done is `done`; failed is `failed` and its summary is the error's first line; done with `Outcome: "stopped"` is `stopped`.
     - `TestMeasureSummary`, a table:
       - Go test metrics (Passed 210, Failed 2, Skipped 0, Running 0) give `210 passed, 2 failed`;
       - a bytes metric with a total, current 1536 and total 4096, gives `1.5 KiB/4.0 KiB`;
       - a count metric with a total gives `3/7 files`;
       - a bytes metric without a total gives `512 B`;
       - a phase (index 2, total 3, label "verify") with a count metric gives `2/3 verify · 3/7 files`;
       - nil gives "".
     - `TestJobCompletionSummary`: a `--- FAIL` line wins over the progress detail; the detail wins over the last line; `"go test"` is skipped as a detail; a trailing `[output truncated]` is skipped.
     - `TestReportHeadlineStripsAHeading`: `"## Task 2 complete. Details follow."` gives `"Task 2 complete."`, and `"#3 done"` keeps its `#`.
     - `TestReportTailPutsFailuresFirst`: 30 lines with two `--- FAIL` lines give 20 lines, starting with the two failures, then the last 18 lines that are not failures.
   - **`cards_test.go`:**
     - Rewrite `TestJobStartBecomesCardAndExitBecomesMarker` as `TestJobExitBecomesOneCompletion`. Keep its card assertions (Task 2 removes them).
       - `completion:tool:1:j` sits between `step:1` and `step:2`: the job was launched in beat 1.
       - Its `Completion.Outcome` is `done`, and its `Completion.Launch.Key` is `tool:1:j`.
     - Rewrite `TestMarkerInsideBeatDoesNotSplitIt` as `TestOtherWorkCompletingInABeatGoesBeforeIt`. Use the same fixture: it has no dispatch row for subagent 3, so the beat did not launch it. Assert one `step:` node, with `completion:sub:3` *before* it: the subagent ended at 3, before the beat settled.
     - Add `TestCompletionFollowsTheBeatThatLaunchedIt`:
       - The beat has two steps: step 1 (1–4) with `subagentDispatch(1, "g", 2, "task-1", 3)`, and narrationless step 2 (5–7).
       - A `SubagentView` `{ID: 3, WorkKey: "task-1", Status: SubagentDone, StartedAt: at(2), EndedAt: at(3)}` is in the items.
       - The tree is one `step:1` beat followed by `completion:sub:3`, whose `Launch.Key` is `tool:1:g`. It holds when busy and when not busy.
       - Set `LiveWork: true` on both snapshots, so the `agent.run` row is kept and no card is placed. Task 3 deletes the field and these lines.
     - Add `TestCompletionAfterSettledBeatFollowsIt`: a finished turn (not busy) with no final message, whose only beat ends at 4, and a subagent that ends at 6 and was not dispatched in it. The event follows the beat.
     - Rewrite `TestSimultaneousCompletionsMergeIntoOneMarker` as `TestSimultaneousCompletionsStaySeparate`: two events, one done and one failed.
     - In `TestJobExitInLaterTurnSettlesCardAndMarksLaterTurn`, change the `marker:tool:1:j` key to `completion:tool:1:j`. Rename the test `TestJobExitInLaterTurnCompletesThere`.
     - Rename `TestSubagentMarkerLandsInTurnContainingItsEnd` to `TestSubagentCompletionLandsInTurnContainingItsEnd`, with keys `completion:sub:3`.
     - Rename `TestRunningJobCardHasNoMarker` to `TestRunningJobHasNoCompletion`, asserting no `completion:` key.
     - In `TestReusedJobIDSettlesOnlyTheLatestStart`, expect two completion keys: `completion:tool:2:new` (outcome `exit 4`) and the lost one, `completion:tool:1:old` (outcome `lost`). The lost one is in the first turn, directly after `step:1`.
     - In `TestOrphanJobExitsOfReusedIDGetDistinctMarkers`, count `KindCompletion` nodes. Rename it `TestOrphanJobExitsOfReusedIDGetDistinctCompletions`.
     - In `TestStructuredExitSettlesItsCardRegardlessOfOrder`, change the key to `completion:tool:1:j`.
   - **`hardening_test.go`:** delete `TestMarkerNodeSurvivesAMarkerWithNoRefs` and `TestMarkerNodeToleratesLabelsShorterThanRefs`. Add `TestCompletionNodeScrubsModelText`: an escape sequence in `Label`, `Summary`, `Output` and `Report` is gone from the node.
   - **`wire_test.go`:** remove the marker assertions from `TestWireMarkerAndJobCard` and rename it `TestWireJobCard`. Add `TestWireCompletionCarriesTheEvent`, which builds a job exit with output longer than `WireTextCap` and asserts the wire event's id, outcome, measure, a capped tail and `truncated`.
   - **`internal/app/tui/cards_render_test.go`:** rewrite `TestMarkerLine` as `TestCompletionLine`.
     - A done job shows its id, label, `done` and its duration.
     - A failed subagent shows `#3`, `failed` and its summary on a second line.
     - The collapsed head ends with `▹` and the expanded one with `▿`.
   - **`scrub_render_test.go`:** in `TestRenderersDropTerminalControlSequences`, replace the `renderMarker` call with `renderCompletion` on a `CompletionInfo` whose label carries the escape sequence.
   - **`click_test.go`:** delete `TestMarkerClickOpensTheBeatHidingItsCard`. Add `TestCompletionClickTogglesItsDisclosure`: a turn with a completion event; a click on its line turns `▹` into `▿`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pb-suite go test ./internal/viewmodel/... ./internal/acp/...
TMPDIR=/private/tmp/pb-suite go test ./internal/app/tui/...
go build ./... && go vet ./internal/viewmodel/... ./internal/app/tui/... && gofmt -l internal/
```

Expected:
- viewmodel and ACP pass;
- the TUI passes except the known failure;
- the build and vet are clean, and `gofmt -l` prints nothing.

## Task 2: Dispatch rows never settle

**Goal:** a background job's launch row is an ordinary call row whose result is `┆ job-N started`. It never takes the job's exit, and `JobState`, `settleJob` and `WireJob` are gone.

**Files:**
- `internal/viewmodel/cards.go`, `stack.go`, `band.go`, `wire.go`;
- `internal/viewmodel/cards_test.go`, `band_test.go`, `hardening_test.go`, `wire_test.go`;
- `internal/acp/stack_test.go`;
- `internal/app/tui/steps.go`, `spine.go`;
- `internal/app/tui/cards_render_test.go`, `scrub_render_test.go`.

**Steps:**

1. **`cards.go`.**
   - Delete `JobState`, `settleJob`, `versionOfJob`, `exitCodeOrUnknown` and the `(*Node).JobStart` method. `exitCodeOrUnknown` and `JobStart` have no other callers once the band and the wire stop reading jobs (steps 3 and 4).
   - Add `commandWorkKey(ev registry.AuditEvent) string`: the work key `settleJob` computed. With `js, ok := JobStartOf(ev)`, it applies when `ok && js.Structured && js.Execution.ExecutionID != ""`. The epoch is `js.Execution.SourceEpoch`, else `legacyWorkEpoch`. The result is `WorkKey(js.Execution.ConversationID, epoch, workKindCommand, js.Execution.ExecutionID)`. Otherwise it returns "".
   - Update the comments on `cardIndex` and `indexCards`: exits now pair with starts only to build completion events.
2. **`stack.go`.**
   - Delete the `Job *JobState` field.
   - In `toolRows`, after `row.WorkKey = dispatchWorkKey(&ev)`, add: when `row.WorkKey == ""`, set it to `commandWorkKey(ev)`.
   - In `buildTurn`'s `finish`, delete the `ci.settleJob(r)` call and its comment.
   - `buildTurn` no longer reads `ci`: remove that parameter, and drop the argument in `Build`.
3. **`band.go`.**
   - In `BandOf`, replace the tree walk inside `if !s.LiveWork { … }` with `b.Entities = runningEntities(s.Items)`. Keep the `LiveWork` gate itself; Task 3 deletes it.
   - Add `runningEntities(items []session.TranscriptItem) []BandEntity`, in transcript order:
     - **A running subagent:** each item whose `Subagent` status is running gives `BandEntity{Ref: NodeID{KindSubagent, fmt.Sprintf("sub:%d", v.ID)}, Label: fmt.Sprintf("#%d %s", v.ID, ScrubText(v.Label)), Detail: ScrubText(v.CurrentTool), Started: v.StartedAt}`.
     - **A running job:** with `ci := indexCards(items)`, each audit whose `JobStartOf` is ok and whose `ToolID` has no entry in `ci.exits` gives `BandEntity{Ref: ToolID(ev), Label: js.DisplayID, Detail: ScrubText(ToolTarget(ev)), Started: ev.Timestamp}`.
4. **`wire.go`.** Delete `WireJob`, the `Job` field of `WireTool` and the `if j := n.Job; j != nil { … }` block in `wireNode`.
5. **TUI.**
   - In `steps.go`'s `renderStep`, delete the `case ch.Job != nil` branch. Delete `jobOutcomeWord` and `jobFailedStatus`.
   - In `spine.go`, delete `renderJobRow` and `jobState`.
   - In `callResult`, add a case before `default`: `case viewmodel.JobStartID(ev) != ""`. It returns the job ID in the same form as the subagent case below it: `lipgloss.NewStyle().Foreground(theme.Current().StatusInfo).Render(glyph.Job + " " + id)` plus `mutedStyle().Render(" started")`, after any hook indicator.
   - Remove the `native` import from any file that no longer uses it.
6. **Tests.**
   - **`cards_test.go`:**
     - Delete `TestCardCarriesExecutionEvidenceAndUnknownExit` and `TestJobCardVersionFollowsStatusAndTruncation`.
     - Remove the card (`.Job`) assertions from `TestJobExitBecomesOneCompletion`, `TestJobExitInLaterTurnCompletesThere`, `TestRunningJobHasNoCompletion`, `TestReusedJobIDSettlesOnlyTheLatestStart` and `TestStructuredExitSettlesItsCardRegardlessOfOrder`. Rename the last two `TestReusedJobIDPairsOnlyTheLatestStart` and `TestStructuredExitPairsRegardlessOfOrder`.
     - In `TestForegroundExecutionMetadataIsNotABackgroundCard`, replace the `row.Job` assertion with "the tree has no `completion:` node and the row's `WorkKey` is empty".
     - Add `TestJobStartRowNeverSettles`. Build the same session twice, before and after a `structuredJobExit` for its start. The start row has the same ID, `Version` and `WorkKey` (non-empty) both times, and `Live` is false both times. The second tree gains the completion.
   - **`band_test.go`:** `TestBandEntitiesAreRunningCards` passes unchanged. Add `TestBandEntitiesDropAFinishedJob`: a start with its exit gives no entity, and a lost exit counts as finished.
   - **`hardening_test.go`:** delete `TestBandOfSurvivesAJobCardWithNoRun`.
   - **`wire_test.go`:** delete `TestWireJobCard`, `TestWireJobCarriesWaitAndDetachEvidence`, `TestWireJobOmitsEvidenceForLegacyCard`, `TestWireJobScrubsDetachEvidence`, `TestWireJobCarriesExecutionEvidence` and `TestWireJobOmitsUnobservedExit`.
   - **`internal/acp/stack_test.go`:** in `TestStackPatchAndDetailAgreeOnExecutionEvidence`, find the upserted node whose `Tool.Name` is `shell.run` (not `Tool.Job`). Assert its `Tool.WorkKey` is non-empty, and keep the detail assertions on that node.
   - **`internal/app/tui/cards_render_test.go`:**
     - Delete `TestJobCardIsOneLineAndSettles`, `TestJobCardDistinguishesOutcomeStates`, `TestJobCardFlagsTruncatedOutput` and `TestJobCardElidesTheWorktree`.
     - Add `TestJobLaunchRowNamesTheJob`: `renderCallRow` on a `structuredJobStart`-shaped audit (build it inline) shows `shell`, the command and `┆ job-4 started` on one line.
   - **`scrub_render_test.go`:** delete `TestJobCardToleratesAJobWithNoRunAttached`. In `TestRenderersDropTerminalControlSequences`, delete the job-card case.

**Verify:** the same commands as Task 1, with the same expected results. Also run `grep -rn "JobState\|settleJob\|WireJob" internal`, which must print nothing.

## Task 3: One projection

**Goal:** `Build` has one projection.
- It never filters `agent.run` audits and never places subagent cards.
- `Snapshot.LiveWork`, `RunningSubagent` and `Drilled` are gone, and ACP serves that projection.
- `WireTool.subagentId` names the subagent a row started, and `WireSubagent` is gone.

**Files:**
- `internal/viewmodel/stack.go`, `band.go`, `wire.go`;
- `internal/viewmodel/stack_test.go`, `cards_test.go`, `wire_test.go`, `band_test.go`;
- `internal/acp/stack.go`, `stacknode.go`, `stack_test.go`;
- `internal/app/tui/viewport_render.go`, `spine_test.go`.

**Steps:**

1. **`stack.go`.**
   - Delete the `Drilled`, `LiveWork` and `RunningSubagent` fields and their comments.
   - In `Build`, delete the loop that drops `agent.run` audits: `items` is `s.Items`.
   - In `buildTurn`:
     - the `case session.KindSubagent:` branch becomes `curLoose = nil; continue`;
     - delete the `cards` slice and the whole `if !s.LiveWork { … }` placement block;
     - delete the `stepAcc.cards` field and the `node.Children = append(node.Children, a.cards...)` line;
     - in the active-call loop, delete the `atc.Name == "agent.run" && …` skip.
2. **`band.go`.** Remove the `if !s.LiveWork` gate: `b.Entities = runningEntities(s.Items)` always runs.
3. **`wire.go`.**
   - Add `SubagentID` to `WireTool`, set in `wireNode`'s tools case from `StartedSubagent(n.Tools[0])` when the row has one call.
   - Delete `WireSubagent`, the `Subagent` field of `WireNode`, the `it.Subagent != nil` case in `wireItem` and `subagentStatus`. No tree carries a `KindSubagent` node any more.
4. **ACP.**
   - In `internal/acp/stack.go`, remove the `drilled` parameter from `stackSnapshotOf`. Drop `Drilled` and `RunningSubagent` from the literal, and replace its doc comment with: "stackSnapshotOf builds the projection a stack request reads: the same one the TUI renders."
   - Update its three callers: `stack.go` twice and `stacknode.go`'s `treeFor`. `treeFor` keeps its `drilled` variable for the busy check.
5. **TUI.** In `viewport_render.go`, drop `Drilled`, `RunningSubagent` and `LiveWork` from the `viewmodel.Snapshot` literal. `drilling` is still used further down, so leave it.
6. **Tests.**
   - **`stack_test.go`:**
     - Delete `TestBuildSubagentCardPlacement`, `TestBuildKeepsAgentRunAuditsWhenDrilled`, `TestBuildSubagentCardNotPlacedInPipelineRoleStep` and `TestBuildSuppressesActiveAgentRunWhileCardRenders`.
     - Rewrite `TestLiveWorkDispatchShownOnceWithoutChildBox` as `TestDispatchShownOnceWithoutChildBox` with no `LiveWork` field. Its assertions stand.
     - Rewrite `TestLiveWorkReportAndDrilledTranscriptRemainReachable` as `TestReportStaysReachable`, dropping the `Drilled` loop.
     - Add `TestSubagentItemIsNeverANode`: a running and a finished `SubagentView` item. The tree has no `KindSubagent` node and has one completion for the finished one.
   - **`cards_test.go`:**
     - In `TestLiveWorkDetachedCommandKeepsOneDispatch`, drop `LiveWork: true` and rename it `TestDetachedCommandKeepsOneDispatch`.
     - In `TestBaseEraCommandRowUsesStableWorkKey` and `TestCompletionFollowsTheBeatThatLaunchedIt`, drop `LiveWork: true`.
   - **`wire_test.go`:**
     - Delete `TestLiveWorkWireOptInPreservesLegacyBand`.
     - Add `TestWireToolNamesTheSubagentItStarted`: a `subagentDispatch(1, "g", 2, "task-1", 3)` row projects `SubagentID == 3`, and a `file.read` row projects 0.
     - Add `TestWireHasNoSubagentNodes`: a tree with a subagent item projects no node of kind `subagent`.
   - **`internal/acp/stack_test.go`:** `TestWorkACPLegacyStackAndWatchRemainCompatible` keeps passing, because a running job is still an entity. Rename it `TestWorkACPStackAndWatchRemainCompatible` and drop "legacy" from its doc comment.
   - **`internal/app/tui`:** add `TestStepRowNamesItsSubagentInCallResult`, which renders a `subagentDispatch`-shaped audit and asserts `⧉ #3 started`. Put it in `spine_test.go`, which exists.

**Verify:** the same commands as Task 1. Also run `grep -rnE '\.LiveWork\b|\.RunningSubagent\b|Drilled:' internal --include='*.go'`, which must print nothing. (The field references are what must be gone; bare `RunningSubagent` also matches live helpers such as `HasRunningSubagent` and `drillIntoLatestRunningSubagent`, so anchor on the selector.)

## Task 4: TUI without in-place cards, and drill-in from rows

**Goal:** the TUI has no in-place subagent card code. `f` in browse mode drills into a subagent from its completion event or from the `agent.run` row that started it.

**Files:**
- `internal/app/tui/steps.go`, `transcript.go`, `browse.go`, `viewport_render.go`;
- `internal/app/tui/drill_test.go`, `steps_test.go`, `transcript_test.go`, `cards_render_test.go`, `browse_test.go`.

**Steps:**

1. **`steps.go`.**
   - In `renderStep`, delete the `cards` slice, its collection loop and the "Subagent cards in the parent view" loop.
   - Delete `cardIndent`, `subagentBodyLines`, `subagentSummaryHeadline`, `stripMarkdownHeading` and `liveSubagentRows`.
   - Keep `subagentHeadline`: the now bar uses it until P-C.
2. **`transcript.go`.**
   - Delete `renderSubagentCard`, `settledSubagentRow` and `subagentTailBudget`, plus the comment block about `renderJobExit` above them.
   - In `renderTranscriptItemIn`, delete the `session.KindSubagent` case.
3. **`viewport_render.go`.** In `blockTarget`, delete the `KindSubagent` case. In the sub-region click loop (the line that builds `clickTarget{node: sr.id, subagent: sr.subagent, …}`), leave the `subagent` field: it is nil now for every row.
4. **`browse.go`.**
   - Rewrite `subagentAtCursor(onlyCard bool)` so that `onlyCard == true` always returns false: no card exists, so Enter expands.
   - With `onlyCard == false`, resolve a subagent ID from the cursor node:
     - a `KindCompletion` node with `Completion.SubagentID > 0`;
     - a `KindTool` node with one call whose `viewmodel.StartedSubagent` is positive;
     - a `KindStep` node: the first child that matches either rule.
   - Look the ID up with `src, _ := m.transcriptSource(); v, ok := src.Subagent(id)`, and return `v, ok && v.Child != nil`.
   - Delete the `KindSubagent` branches.
   - In `navigable`, keep `viewmodel.KindSubagent`: it is harmless, and P-C revisits browse.
5. **Tests.**
   - Delete `TestRenderSubagentCardContent` (`drill_test.go`) and `TestLiveSubagentCardShowsLatestActivity` (`steps_test.go`).
   - In `transcript_test.go`, delete:
     - `TestDoneSubagentCardOmitsTail`, `TestFinishedSubagentCardIsOneFlatRow`, `TestFinishedSubagentCardOmitsCurrentTool` and `TestFinishedSubagentExpandedShowsSummary`;
     - `TestRenderSubagentCardDoneHasOneGlyph` and `TestRenderSubagentCardRunningHasOneGlyph`;
     - `TestRunningSubagentCardIsTinted`, `TestRunningSubagentCardRendersLatestAuditWhenNoReasoning`, `TestRunningSubagentCardRendersLatestReasoningLine` and `TestRunningSubagentCardShowsCurrentTool`;
     - `TestSubagentCardHeightIsStableWhileRunning`, `TestSubagentCardRendersTokenCount` and `TestSubagentCardShowsProviderModel`.
   - In `cards_render_test.go`, delete `TestRunningSubagentCardHasNoPaddingOrFooter`, `TestSettledSubagentSummaryHasNoMarkdownHeading` and `TestStripMarkdownHeadingKeepsHashText`. `viewmodel`'s `TestReportHeadlineStripsAHeading` covers the heading rule.
   - The symbol-row tests that use the string `"renderSubagentCard"` as fixture text stay as they are.
   - In `browse_test.go`:
     - Add `TestBrowseFDrillsFromACompletionEvent`: register a subagent with a child state, finish it, put the cursor on its completion event and press `f`. The view stack's top is that child.
     - Add `TestBrowseFDrillsFromTheDispatchRow`: the same, from the `agent.run` row while the subagent runs.
     - Add `TestBrowseEnterExpandsACompletionEvent`: Enter toggles the event's disclosure and does not drill.
     - Follow `TestCtrlXStopsRunningSubagentWhileDrilled` in `drill_test.go` for registering a child.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pb-suite go test ./internal/app/tui/...
go build ./... && go vet ./internal/app/tui/... && gofmt -l internal/
grep -n "renderSubagentCard\|settledSubagentRow\|cardIndent" internal/app/tui/*.go | grep -v _test
```

Expected: the TUI passes except the known failure; the build and vet are clean; and the grep prints nothing.

## Task 5: The issue report

**Goal:** an expanded completion event shows the issue report. The inspector (`i`) shows the event with its full output, and `y` copies the event.

**Files:** `internal/app/tui/steps.go`, `browse_inspect.go`, `browse_actions.go`, `cards_render_test.go` and `browse_inspect_test.go` (new).

**Steps:**

1. **`renderCompletion` (`steps.go`).** When `expanded`, draw the report under the head instead of the summary line.
   - **Layout.** One line per field, each starting at `nestedBodyIndent`. The key is muted and padded to 10 cells; the value is in the default foreground. Each line is truncated to `width − nestedBodyIndent`. Fields with an empty value are skipped.
   - **`outcome`:** the outcome word, plus `dimSeparator + "stop: " + StopCause` when a stop cause is set.
   - **`command`:** a job's `Command` first line, through `viewmodel.ElideWorkdir`. For a subagent the key is `task` and the value its `Label`.
   - **`cwd`:** `viewmodel.ElideWorkdir(Cwd, workdir)`.
   - **`duration`:** `formatElapsed(Duration)`. For a lost job the value is `ended with session`.
   - **`progress`:** `Measure`.
   - **`output`:** the lines of `viewmodel.ReportTail(Output)`. The first sits on the key's line and the rest are indented under the value column. Every line is scrubbed with `viewmodel.ScrubText` and muted. When `Truncated` is set, a last muted line reads `earlier output dropped`.
   - **`report`:** a subagent's `Report`, rendered with `renderMarkdown` at `width − nestedBodyIndent − 10`, or `renderPlainProse` when that fails, under the value column.
   - **`error`:** a subagent's `Error` first line, in error style.
2. **`browse_inspect.go`.** In `inspectorDetail`, add a case for `n.Kind == viewmodel.KindCompletion && n.Completion != nil` that returns `m.completionDetail(n.Completion), true`. Write `completionDetail`:
   - `Title` is the event's ID, and `Subject` its label.
   - `Fields`, each only when set: `outcome`, `exit`, `stop cause`, `started` and `ended` (local time, `15:04:05`), `duration`, `cwd`, `execution` (`ExecutionID`), `work key` and `report message` (the message ID).
   - `Sections`: `output` with the whole scrubbed `Output`, `report` with `Report`, `error` with `Error`, and `command` with `Command`, each only when non-empty.
3. **`browse_actions.go`.** In `nodeCopyText`, add a case for `n.Kind == viewmodel.KindCompletion && n.Completion != nil`.
   - The first line is the event's plain head text: id, label, outcome, measure and duration, joined with `" · "`.
   - Then `Output` for a job, or `Report` for a subagent, after a blank line.
4. **Tests.**
   - In `cards_render_test.go`:
     - Add `TestCompletionReportShowsTheFailure`. An expanded failed job with a 30-line output that has one `--- FAIL` line shows `outcome`, `command`, `cwd`, `duration`, `progress` and `output`. The `--- FAIL` line is the first output line, and at most 20 output lines show.
     - Add `TestCompletionReportShowsASubagentsReport`: an expanded subagent shows `task` and the report's text.
     - Add `TestLostJobReportSaysItEndedWithTheSession`.
   - In `browse_inspect_test.go` (new, package `tui`):
     - add `TestInspectorShowsACompletionsFullOutput`: `completionDetail` for a job holds an `output` section equal to the scrubbed output, and an `exit` field;
     - add `TestCopyTakesTheCompletionHeadAndOutput`.

**Verify:** the same commands as Task 4, without the grep.

## Task 6: History reads only the transcript

**Goal:**
- A settled beat's todo tag, its todo binding and the receipt's task count no longer change when the todo list is rewritten.
- The band shows todo progress whenever a list exists.
- `DrivenByTodos`, `MinTaskTodos` and `taskMode` are gone.

**Files:**
- `internal/viewmodel/tasks.go`, `labels.go`, `stack.go`, `band.go`;
- `internal/viewmodel/labels_test.go`, `band_test.go`;
- `internal/app/tui/todo_band_test.go`, and any TUI test that the note under Verify covers.

**Steps:**

1. **The binding (`tasks.go`).**
   - Change `effectiveTodoID` to `effectiveTodoID(info *StepInfo, noOtherCalls bool, turnSteps []session.Step) (id string, rebound bool)`. Keep the condition: the step has todo writes, no other calls, and narration.
   - When the condition holds and `info.Step.ID != 0`, find the first step in `turnSteps` that:
     - has the same `Actor.Role` and `Actor.Label`;
     - has `StartedAt` after `info.Step.StartedAt`;
     - has `ID != info.Step.ID`.
   - When that step's `TodoID` is non-empty, return it with `rebound = true`. In every other case, return `info.Step.TodoID, false`.
   - Delete `todoSetInProgress`.
   - Rewrite the doc comment: a narrated step that only writes the list sits under the todo its actor's next step was bound to, and that step's binding is stored, so the answer never changes once the next step exists. The beat is still live until then.
2. **`stack.go`.**
   - Add an unexported `labelPlan *PlanUpdate` field to `StepInfo`, documented as "the plan left by the write that re-bound this beat (effectiveTodoID); nil otherwise". `absorb` does not touch it, so a beat keeps its first step's.
   - In `buildTurn`, build `turnSteps`: the steps in `s.Steps` whose `TurnMsgID == turnMsgID`, sorted by `StartedAt`.
   - In `finish`, set `id, rebound := effectiveTodoID(a.info, len(audits) == 0 && len(active) == 0, turnSteps)`, then `a.info.TodoID = id`.
   - When `rebound` and `finish`'s `plan` row is non-nil, also set `a.info.labelPlan = plan.PlanUpdate`. That row comes from this step's own writes only, because `absorb` runs later.
   - Give `buildTurn` a `before *PlanUpdate` parameter (the newest plan of earlier turns) and a second result, the newest plan once this turn is included.
   - In `Build`, keep `var plan *PlanUpdate` across the turn loop: pass it in, and store the result back.
3. **The tag (`labels.go`).**
   - Change `labelTransitions` to `labelTransitions(nodes []*Node, todos []db.TodoItem, plan *PlanUpdate) *PlanUpdate`.
   - Walk the nodes in order, keeping `cur` (starting at `plan`). For each beat (`KindStep` with a `Step`):
     - **Its own plan** is the beat's `KindPlanUpdate` child, if any (`keepLastPlanRow` leaves at most one).
     - **On a transition** (the existing `id != prev` test), the label's source plan is `n.Step.labelPlan` when it is set, else `cur`.
       - When that plan is non-nil and its `Active > 0`, the label is `TodoLabel{ID: id, Index: p.Active, Total: p.Total(), Content: ScrubText(p.ActiveText)}`.
       - Otherwise fall back to today's lookup of `id` in `todos`.
     - After the beat, set `cur` to its own plan when it has one.
   - Return `cur`.
   - Delete `DrivenByTodos`.
   - Rewrite the doc comment to say the tag reads the plan in effect, so a list rewritten later does not change it.
   - In `buildTurn`, call `after := labelTransitions(out, s.Todos, before)`.
4. **The receipt (`tasks.go`).**
   - Change `receipt` to take `before, after *PlanUpdate`.
   - `Tasks` is `max(0, after.Done() − beforeDone)` when the turn wrote a plan (`after != before`), where `beforeDone` is `before.Done()` or 0 when `before` is nil. Otherwise `Tasks` is 0.
   - Delete the loop over `s.Todos`, `taskMode` and `MinTaskTodos`. `todoListed` stays only if something still calls it; otherwise delete it too.
   - In `buildTurn`, pass `before, after`.
5. **The band (`band.go`).**
   - Replace `len(turns) > 0 && taskMode(s.Todos) && DrivenByTodos(turns[len(turns)-1])` with `len(s.Todos) > 0`.
   - Update `BandInfo.Todos`'s field comment: "a list exists and the turn is running with work left".
6. **Tests.**
   - **`labels_test.go`:**
     - Delete `TestDrivenByTodosFollowsTheCodeBlockNotTheProse`.
     - In `TestReceiptArithmetic`, replace the `{"todos":[]}` write with `{"todos":[{"id":"t1","content":"A","status":"completed"},{"id":"t2","content":"B","status":"completed"}]}`. `rc.Tasks` stays 2.
     - Add `TestTagReadsThePlanNotTheCurrentList`.
       - Step 1 (no todo) is narrated, and has a `file.read` and the write `{"todos":[{"id":"a","content":"A","status":"in_progress"},{"id":"b","content":"B","status":"pending"},{"id":"c","content":"C","status":"pending"}]}`.
       - Step 2 (todo `a`) is narrated and has a `file.read`.
       - `Todos` holds four items: a, b, c and d.
       - `step:2`'s label is `{ID: "a", Index: 1, Total: 3, Content: "A"}`.
     - Add `TestTagSurvivesDroppingItsTodo`: the same steps, with `Todos` holding only b and c. `step:2`'s label is the same.
     - Add `TestWriteOnlyBeatTakesTheNextStepsTodo`. A narrated step whose only call is a write, followed by a same-actor narrationless step bound to `b`: the first beat's `TodoID` is `b`, it carries the label, and the second step merges into it.
     - Add `TestWriteOnlyBeatKeepsItsBindingWithoutANextStep`.
     - Add `TestReceiptCountsThePlansCompletions`: turn 1 completes one item; turn 2's write drops the completed item from the current list; turn 1's receipt still says one task.
     - `TestNoLabelForDroppedTodo` and `TestLabelsDoNotDependOnListLength` pass unchanged: they write no plan with an active item, so they take the fallback.
   - **`band_test.go`:** add `TestBandShowsAListTheTurnDidNotTouch`: a busy turn with a list but no bound beat has `b.Todos == true` and the right `Done` and `Total`.
   - **`internal/app/tui/todo_band_test.go`:** rewrite `todoDrivenTurn`'s doc comment. A list on the session now drives the band whatever the turn did. The helper still binds a beat, because tests use it to get a running turn.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pb-suite go test ./internal/viewmodel/... ./internal/acp/... ./internal/app/tui/...
go build ./... && go vet ./internal/viewmodel/... && gofmt -l internal/
grep -rn "DrivenByTodos\|MinTaskTodos\|taskMode\|todoSetInProgress" internal --include='*.go'
```

Expected:
- viewmodel and ACP pass, and the TUI passes except the known failure;
- the build and vet are clean;
- the grep prints nothing.

Band rows now show for a busy turn whose list it never touched, and for a one-item list. A TUI test that fails only because it expected no band rows in that case is asserting the removed gate. The cases most likely affected are the `frame_invariants_test.go` variants with a running turn and a todo.
- Change such a test to expect the rows, and name it in the commit message.
- Any other TUI failure is a defect in this task.

## Task 7: Streaming reasoning at the bottom

**Goal:** the reasoning a model is streaming is one node at the end of the newest turn, below every beat, call row and completion event. No beat carries it, so it never lands between a beat's head and its calls. The TUI draws it last.

This fixes a bug seen in testing. `renderStep` draws the live thinking box after the beat's head and before its spine rows, which pushes the calls down and breaks the tree while the model thinks. The web does the same in `StepHeader.svelte`; Task 9 removes that.

**Files:**
- `internal/viewmodel/stack.go`, `beats.go`, `wire.go`;
- `internal/viewmodel/stack_test.go`, `beats_test.go`;
- `internal/app/tui/steps.go`, `viewport_render.go`, `spine_test.go`.

**Steps:**

1. **`stack.go`, in `buildTurn`.**
   - Delete both places that attach the reasoning to a step:
     - `live.info.LiveThinking = reasoning` under `if live != nil && reasoning != ""`;
     - `a.info.LiveThinking = reasoning` in the loose-live-step loop.
   - Delete `liveThinkingPlaced`, and the "Anything still unplaced" block that adds the `LiveThinkingID` node to `blocks`. The `strayActive` loop after it stays.
   - Keep `reasoning`'s computation and the `live` step: `live` still marks the open step's `Live` flag.
   - Make the reasoning node the turn's last child. At the very end of `buildTurn`, after the receipt, when `reasoning != ""`, append `&Node{ID: LiveThinkingID, Kind: KindThinking, Live: true, Step: &StepInfo{LiveThinking: reasoning}}` to `out`.
     - It joins `out` after `mergeBeats`, so it never settles the open beat or splits it.
     - Comment the line: "Streaming reasoning is drawn last, below every row, so it never lands inside a beat's spine."
   - In `empty`, delete the `n.Step.LiveThinking == ""` clause: no step carries reasoning any more.
     - A step that has opened but holds only streaming reasoning is now omitted as empty, and its reasoning shows at the bottom.
     - The step joins the tree with its first narration or call.
   - Change `StepInfo.LiveThinking`'s field comment to: "the in-progress reasoning; set only on the live reasoning node (LiveThinkingID), never on a beat".
   - In `versionOfStep`, remove `len(st.LiveThinking)` and its `%d|` verb from the `st|…` format.
2. **`beats.go`.** In `absorb`, delete the `if ni.LiveThinking != ""` block.
3. **`wire.go`.**
   - Delete `WireStep.LiveThinking` and its assignment in `wireStep`.
   - The `n.Kind == KindThinking && n.Step != nil` case in `wireNode` stays: it projects the reasoning node as `WireThinking`.
4. **TUI.**
   - **`steps.go`.** In `renderStep`, delete the `if si.LiveThinking != "" { … }` block, which draws the box between the head and the spine rows. Delete the `subRegion{live: true}` row it recorded with it.
   - **`viewport_render.go`.**
     - In `narrationOnly`, delete the `n.Step.LiveThinking == ""` clause.
     - `drawNode`'s `KindThinking && n.Step != nil` case, `blockTarget`'s `LiveThinkingID` target and `collectSeen` stay as they are. They already handle the turn-level node, which is now the only one.
5. **Tests.**
   - **`stack_test.go`:**
     - Rename `TestBuildLiveStepAndThinking` to `TestBuildLiveStepWithThinkingBelowIt`.
       - Its expected shape gains a last line, `  think:live live`, after `    tool:2:s live`.
       - Replace the "attached to the live step" assertion. The turn's last child is `LiveThinkingID`, with `Step.LiveThinking == "thinking hard"`, and `step:2`'s `Step.LiveThinking` is empty.
     - `TestBuildLiveThinkingBeforeAnyStepIsPassthrough` and `TestBuildLiveThinkingOnlyInLatestTurn` pass unchanged.
     - Add `TestLiveThinkingIsTheTurnsLastChild`. A busy turn has:
       - a live beat with an active call;
       - a completion event of work the beat launched;
       - an earlier settled beat;
       - streaming reasoning.

       The turn's children end with the live beat, the completion event, then `think:live`. No beat has `LiveThinking` set.
     - Add `TestStepWithOnlyReasoningIsOmitted`: a busy turn whose newest step is open and has no narration or calls, with streaming reasoning. That step has no node, and `think:live` is the last child.
   - **`beats_test.go`:** rewrite `TestBeatTakesLiveStepAndThinking` as `TestBeatTakesLiveStepNotItsThinking`. The beat is live and its end is zero, its `Step.LiveThinking` is empty, and the turn's last child is `LiveThinkingID`.
   - **`internal/app/tui/spine_test.go`:** add `TestLiveThinkingDrawsBelowTheSpine`.
     - Set up a busy model (`newTestModel`, `resize(100, 40)`, `m.busy = true`).
     - Open a step with `m.state.BeginStep(session.Actor{})`, add narration with `m.state.AddNarration`, and log a finished `file.read` with `m.state.LogToolCall`.
     - Start a running `shell.run` with `m.state.SetActiveToolCall`, using the step's ID and the args `go test ./...`.
     - Stream reasoning with `m.state.BeginStreaming()` and `m.state.AppendThinking("weighing the next step\n")`, then `m.refreshViewport()`.
     - In `stripANSI(m.viewport.GetContent())`, the line holding `go test` comes before the line holding `thinking`. No line after the `thinking` line contains `├` or `└`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pb-suite go test ./internal/viewmodel/... ./internal/acp/...
TMPDIR=/private/tmp/pb-suite go test ./internal/app/tui/...
go build ./... && go vet ./internal/viewmodel/... ./internal/app/tui/... && gofmt -l internal/
grep -rn "LiveThinking" internal/viewmodel internal/app/tui --include='*.go' | grep -v _test
```

Expected:
- viewmodel and ACP pass, and the TUI passes except the known failure.
- `TestRegionScrollRepaintsViewport` and `TestWheelOverLiveRegionScrollsRegionAndRepaints` pass unchanged: the region keeps the `LiveThinkingID` key.
- The build and vet are clean, and `gofmt -l` prints nothing.
- The grep lists only:
  - the `StepInfo` field;
  - `LiveThinkingID`;
  - the node built at the end of `buildTurn`;
  - `wireNode`'s thinking case;
  - `drawNode`'s and `collectSeen`'s turn-level uses.

## Task 8: SplitLive and the append-only property

**Goal:** `SplitLive` separates the live part from history. A scripted session proves that successive trees only append to history and that events land where decision 1 says.

**Files:** `internal/viewmodel/live.go` (new), `internal/viewmodel/history_test.go` (new).

**Steps:**

1. **`live.go`.** Write `SplitLive(turns []*Node, busy bool) (history []*Node, live []*Node)`.
   - When `!busy`, when there are no turns, or when the last turn has no children, return `turns, nil`.
   - Let `children` be the last turn's children. When its last element has the ID `LiveThinkingID` (Task 7 guarantees it is always last), set it aside as `thinking` and drop it from `children`.
   - Let `i` be the index of the last `KindStep` in `children`, provided every element after it is a `KindCompletion`. When there is no such element, let `i = len(children)`.
   - When `i == len(children)` and there is no `thinking`, return `turns, nil`.
   - Otherwise:
     - `live` is a new slice holding `children[i:]`, followed by `thinking` when there is one;
     - `history` is a new slice holding the same turn pointers, except that the last turn is replaced by a shallow copy whose `Children` is a new slice holding `children[:i]`.
   - The doc comment states the rule from decision 11, and that the input is never mutated.
2. **`history_test.go`.**
   - **Helpers.**
     - `historyRows(turns []*Node, busy bool) []*Node` returns the children of every turn from `SplitLive`'s history, in order.
     - `assertAppends(t, step string, before, after []*Node)` fails unless `len(after) >= len(before)` and, for each `i < len(before)`, `after[i].ID == before[i].ID && after[i].Version == before[i].Version`. On failure it prints both ID lists.
   - **`TestHistoryOnlyAppends`.** Build ten snapshots of one session, each extending the last, and call `assertAppends` on each consecutive pair.
     - Use the package's helpers: `userMsg`, `narration`, `audit`, `final`, `stepOf`, `planWrite`, `jobStart`, `jobExit` and `subagentDispatch`.
     - Steps are `stepOf(id, turnMsgID, start, end)` with `TodoID` set on the struct. An end of 0 means open.
     - `Busy` is true in every snapshot except 9.
     - The `Todos` column is the snapshot's current list. Give each completed item the `CompletedAt` shown.
     - The plan lists in the table are `planWrite` bodies with `id`, `content` and `status` per item. For example, `<a in_progress, b, c>` is `{"todos":[{"id":"a","content":"A","status":"in_progress"},{"id":"b","content":"B","status":"pending"},{"id":"c","content":"C","status":"pending"}]}`.

     | # | Adds | Todos |
     |---|---|---|
     | 1 | `userMsg(1, 0)`; step 1 (turn 1, start 1, open, no todo) with `narration(2, 1, 1, "Planning.")` and `planWrite(1, "w1", 2, <a in_progress, b, c>, "tasks updated · 3 items")` | a in progress, b, c |
     | 2 | step 1 ends at 3; step 2 (start 4, open, todo `a`), no narration; an `ActiveTools` entry `{Name: "shell.run", StepID: 2, ToolCallID: "s", StartedAt: at(4)}`; `InProgress: {Active: true, Reasoning: "Weighing the next step.", StartedAt: at(4)}` | same |
     | 3 | `jobStart(2, "s", 5, "job-1")` and `jobStart(2, "t", 5, "job-2")`; step 2 ends at 6; no active calls; `InProgress` cleared | same |
     | 4 | `jobExit(7, "job-1", 0)` | same |
     | 5 | step 3 (start 8, open, todo `a`), no narration, with `audit("file.read", 3, "r", 9)` and `planWrite(3, "w2", 10, <a completed, b in_progress, c, d>, "tasks updated · 4 items")` | a done at 10, b in progress, c, d |
     | 6 | step 3 ends at 10; step 4 (start 11, open, todo `b`) with `narration(3, 4, 11, "Checking B.")` and `audit("file.read", 4, "r2", 12)` | same |
     | 7 | `subagentDispatch(4, "g", 13, "task-1", 1)`; a `SubagentView` item at 13, `{ID: 1, WorkKey: "task-1", Label: "review", Status: SubagentRunning, StartedAt: at(13)}`; `jobExit(14, "job-2", 1)` | same |
     | 8 | that view becomes `SubagentDone`, with `EndedAt: at(15)` and `Summary: "Looks right."`; `InProgress: {Active: true, Reasoning: "Checking the review.", StartedAt: at(15)}` | same |
     | 9 | step 4 ends at 16; `final(4, 17)`; `InProgress` cleared; `Busy: false` | same |
     | 10 | `userMsg(5, 20)`; step 5 (turn 5, start 21, end 23, todo `b`) with `narration(6, 5, 21, "Finishing.")` and `planWrite(5, "w3", 22, <b completed, c in_progress>, "tasks updated · 3 items")`; `Busy: true` | b done at 22, c in progress, d |

   - **After the pair checks, assert turn 1's history in snapshot 10.** The keys are exactly `msg:1`, `step:1`, `completion:tool:2:s`, `completion:tool:2:t`, `step:4`, `completion:sub:1`, `msg:4`, `receipt:turn:1`.
     - Each job event follows the beat whose rows launched it (`step:1`).
     - `job-2`'s event ended while `step:4` was open, and that beat did not launch it, so it comes before `step:4`.
     - The subagent's event follows the beat that dispatched it.
   - **Also assert:**
     - in snapshots 2 and 8, `live`'s last element has the ID `LiveThinkingID`, and no history row does. In snapshot 8, `live` is `step:4`, `completion:sub:1`, then the reasoning;
     - `step:1`'s label is `1/3 A`, from the write that moved it to `a`;
     - `step:4`'s label is `2/4 B`;
     - the receipt's `Tasks` is 1.
   - **`TestSplitLiveTakesTheNewestBeatWhileBusy`:**
     - busy, last child a beat whose steps have all ended: the beat is live;
     - busy, a beat followed by two completion events: all three are live;
     - busy, a beat, a completion event and the reasoning node: all three are live, the reasoning last;
     - busy, a user message then the reasoning node: only the reasoning node is live, and the message is history;
     - not busy: nothing is live;
     - busy with a user message last: nothing is live;
     - the input turn's `Children` is unchanged after each call.
3. **Mutation checks.** Make each by hand, run the test, and revert. Record each failure in the commit message.
   - (a) In `mergeBeats`, put every held completion in `after`. The turn-1 order assertion fails: `completion:tool:2:t` follows `step:4`.
   - (b) In `mergeBeats`, put every held completion in `before`. The order assertion fails: `completion:tool:2:s` precedes `step:1`.
   - (c) In `labelTransitions`, always use the lookup in `todos`. `assertAppends` fails at 9→10, because `a` leaves the list and `step:1` loses its label.
   - (d) Compute the receipt's `Tasks` from `s.Todos` as before Task 6. `assertAppends` fails at 9→10.
   - (e) In `SplitLive`, treat the beat as live only when its `Live` flag is set. `assertAppends` fails at 4→5, because `step:1` was history while its steps had ended, then step 3 merged into it.
   - (f) In `buildTurn`, attach the reasoning to the live step again, as before Task 7. The snapshot-2 assertion fails: `live` no longer ends with `LiveThinkingID`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pb-suite go test ./internal/viewmodel/... -run 'TestHistoryOnlyAppends|TestSplitLive' -v
TMPDIR=/private/tmp/pb-suite go test ./internal/viewmodel/...
gofmt -l internal/
```

Expected:
- both tests pass, and so does the package;
- each of the six mutations failed as stated before it was reverted;
- `gofmt -l` prints nothing.

## Task 9: The web adopts the projection

**Goal:**
- The web renders completion events, with a click expanding the issue report, and plan lines as call rows.
- It drills into subagents from events and dispatch rows.
- It has no fold handling, no job card and no subagent card.
- The built assets are refreshed.

**Files:**
- `web/ui/src/lib/stack.ts`;
- `web/ui/src/lib/transcript/`: `TurnNode.svelte`, `ToolRow.svelte`, `StepHeader.svelte`, `ctx.ts`, `Transcript.svelte`, `browse.ts`;
- `CompletionRow.svelte` (new), `PlanRow.svelte` (new); `MarkerRow.svelte` and `SubagentCard.svelte` (deleted);
- `web/ui/src/lib/dock/nodes.ts`;
- `web/ui/src/views/Chat.svelte`, `Run.svelte`;
- tests: `Transcript.test.ts`, `browse.test.ts`, `dock/nodes.test.ts`, `views/Chat.test.ts`;
- `web/bridge/static/` (rebuilt).

**Steps:**

1. **`stack.ts`.**
   - Remove `folded` and `liveThinking` from `WireStep`.
   - Remove `WireJob` and `WireTool.job`, and add `subagentId?: number` to `WireTool`.
   - Replace `WireMarker` with a `WireCompletion` interface whose fields mirror the Go JSON names in Contracts (optional where the Go tag has `omitempty`).
   - Add `WirePlan` (`status?: string[]`, `done`, `total`, `active?`, `activeText?`, `cleared?`).
   - Remove `WireSubagent`.
   - On `WireNode`, replace `marker?` and `subagent?` with `completion?: WireCompletion` and `plan?: WirePlan`.
   - Update the `WireTodoLabel` comment, which no longer mentions folding.
2. **`PlanRow.svelte` (new).** Props: `plan: WirePlan`. One `text-xs` line (`data-testid="plan-row"`):
   - the muted word `plan`;
   - one mark per status when `total <= 12`: `glyph.OK` in `text-ok` for `completed`, `glyph.Running` in `text-accent` for `in_progress`, else `glyph.Ambient` in `text-muted`;
   - for a longer list, eight cells of `glyph.ProgressFull` and `glyph.ProgressEmpty` in proportion to `done/total`;
   - then `${active || done}/${total}`;
   - then `activeText`, or `done` when every item is done.
   - A cleared plan reads `plan cleared`.
3. **`CompletionRow.svelte` (new).** It replaces `MarkerRow.svelte`. Props: `completion: WireCompletion`. It holds a local `expanded` state.
   - **The head** is a button (`data-testid="completion-row"`, `aria-expanded`) that toggles `expanded`. It shows:
     - `glyph.Job` in `text-info` for a job, or `glyph.Agent` in `text-violet` for a subagent;
     - the id, then the label;
     - the outcome: `text-ok` for `done`, `text-warn` for `outcome unknown`, `text-err` otherwise;
     - the measure and `compactDuration(durationMs)` when set;
     - `glyph.DisclosureCollapsed` or `glyph.DisclosureExpanded` at the right.
   - **Collapsed,** the summary (when set) follows as one muted, truncated line.
   - **Expanded,** a key/value list shows, each field only when set:
     - outcome, with the stop cause;
     - `command` for a job, or `task` (the label) for a subagent;
     - cwd, duration and progress (the measure);
     - output, in a `<pre>` styled as `ToolRow`'s output block;
     - report, with `whitespace-pre-wrap`;
     - error, in `text-err`.
4. **`TurnNode.svelte`.**
   - Delete `folded`, `headless` and `hiddenTools`, the folded-summary button, and the `!folded` conditions. The beat always renders its header and its capped children.
   - Delete the `subagent` branch and the `SubagentCard` import.
   - Replace the `marker` branch with `node.kind === 'completion' && node.completion` rendering `<CompletionRow completion={node.completion} />`, keeping the cursor class.
   - Add `node.kind === 'planUpdate' && node.plan` rendering `<PlanRow plan={node.plan} />` inside the same `ml-5` wrapper as a tool row.
5. **`ToolRow.svelte`.** Delete `job`, `jobFailed` and `jobTail`, the job branches of `lead` and `leadTone`, and the `{#if job}` block.
   **`StepHeader.svelte`.** Delete the `{#if step.liveThinking}` block. Streaming reasoning now arrives as its own `thinking` node, the turn's last child (Task 7), and `TurnNode`'s existing `thinking` branch draws it below every row.
6. **Folding.**
   - **`ctx.ts`:** delete `foldTasks`, `unfolded`, `onToggleFold` and `onSelect`.
   - **`Transcript.svelte`:** delete the same props and their `ctx` entries.
   - **`browse.ts`:**
     - remove `'z'` from `BOUND`, the `'z'` case and `{ toggleFold }` from `BrowseEffect`;
     - delete the folded check in `flattenVisible`;
     - in `nodeText`, delete the job branch, and replace the `subagent` and `marker` branches with `n.completion`: `[label, outcome, summary, output, report]` joined by newlines, and `n.plan`: `` `${done}/${total} ${activeText ?? ''}` ``.
   - **`views/Run.svelte`:** remove `foldTasks={false}`.
   - **`views/Chat.svelte`:**
     - delete `unfolded`, `toggleFold` and its effect branch, and the `foldTasks` and `unfolded` entries of `tctx`;
     - delete the `{unfolded}`, `onToggleFold` and `onSelect` props on `<Transcript>`;
     - delete `scrollToNode` when nothing else references it after that (search the file).
7. **Drill-in (`Chat.svelte`'s `drillInto`).** Resolve `subId` and its label from the cursor node, else its first matching child:
   - `completion.subagentId` (label: `completion.label`);
   - else `tool.subagentId` (label: `tool.calls?.[0]?.target`);
   - else `subagent ${id}`.

   Replace the `subNode` logic with that. The "no separate transcript" fallback selects the cursor node. Delete `subagentIdOf` from `dock/nodes.ts` and its assertions in `nodes.test.ts`. In `nodeLabel`, replace the `subagent` and `marker` branches with `n.completion`: `` `${id} ${label} ${outcome}` ``, and `n.plan`: `` `plan ${done}/${total}` ``.
8. **Tests.**
   - **`Transcript.test.ts`:**
     - remove `folded` from the fixtures;
     - delete the tests that fold a finished beat, unfold a beat the reader opened, and fold a headless beat to a tool count;
     - delete the job tests: the running job card's elapsed time, the one-line job card, and the job card settling in place;
     - rewrite "renders a completion marker as one line…" as "renders a completion event as one line and expands its report on click": click the row, then assert the output text and `aria-expanded="true"`;
     - add "renders a plan line as a call row";
     - add "draws streaming reasoning after the beat's rows". Use a turn whose children are a step with one tool row, then a live `thinking` node (`id: 'think:live'`). The thinking row's element follows the tool row's in document order (`compareDocumentPosition`);
     - change every `foldTasks: false` render argument to nothing.
   - **`browse.test.ts`:** drop `z` from the "Enter toggles density…" test and rename it "Enter toggles density, y copies, on the cursor row". Delete "does not list a folded beat's children…", and remove `foldTasks` and `unfolded` from `base` and from "skips containers…".
   - **`views/Chat.test.ts`:**
     - delete "opens a folded beat when its completion marker is clicked…";
     - change `withSub` to an `agent.run` tool node: `{ id: 'tool:1:g', kind: 'tool', parent: 'step:1', tool: { name: 'agent.run', display: 'Run agent', subagentId: 3, calls: [{ target: 'reviewer' }] } }`, with root children `['tool:1:g']`;
     - the two drill tests then pass as written.
9. **Build the assets:** run `npm run build` in `web/ui` and `go build ./cmd/webbridge` at the root.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal/web/ui && npm test && npm run build
cd /Users/alecpullen/projects/marshal && go build ./cmd/webbridge && TMPDIR=/private/tmp/pb-suite go test ./web/...
grep -rn "folded\|foldTasks\|unfolded\|\.job\b\|WireMarker\|SubagentCard\|marker\|liveThinking" web/ui/src --include='*.ts' --include='*.svelte'
```

Expected:
- vitest passes and the build writes `web/bridge/static/`;
- `go test ./web/...` passes, including `TestWebIsStdlibOnly`;
- the grep prints only the line in `stack.ts` that says a burst of requests "is folded into a single follow-up", which is unrelated.

Commit with `web/ui` and `web/bridge/static` as explicit paths: `git add web/ui web/bridge/static` stages the removed hashed bundle too.

## Task 10: Verification, docs and changelog

**Goal:** the whole phase is verified, and the spec, folder README and changelog describe what shipped.

**Files:** `docs/tui-simplification/feature-spec.md`, `docs/tui-simplification/README.md`, `CHANGELOG.md` and `README.md` (only if step 3 finds text to change).

**Steps:**

1. **Full verification.**

   ```bash
   cd /Users/alecpullen/projects/marshal
   TMPDIR=/private/tmp/pb-suite CGO_ENABLED=1 go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
   go vet ./... && gofmt -l .
   CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m
   cd web/ui && npm test
   ```

   Expected: every failure is in the Assumptions' base list, `go vet` is clean, `gofmt -l .` prints nothing, and the usability scenarios and vitest pass. Delete the `./marshal` binary afterwards.
2. **Spec.** The 2026-10-24 decisions above are already recorded. Set the status line to "P-A and P-B landed" with this plan's commit range, and add the P-B row's plan link in §7.
3. **README.** Search `README.md` for "job card", "marker", "fold" and "settle". Where a sentence describes completion markers or job cards settling in place, replace it with: "When background work finishes, a completion event is added to the transcript at that moment; expand it (Enter or click) for the issue report: outcome, command, duration, final progress and the end of the output." If nothing matches, leave the file alone.
4. **CHANGELOG.** Under the unreleased section's `Changed` heading, add:

   > **Transcript: completion events.** Finished background jobs and subagents add one completion event to the transcript when they end, with the outcome, final measurement and duration, and an issue report when expanded. The call that started the work no longer changes after it starts. The web shows the same events, and the plan line, as rows.

   Under `Removed`, add:

   > In-place subagent cards, job cards that settled in place, and merged completion markers. The web transcript no longer folds finished beats.

   Under `Fixed`, add:

   > The todo tag and turn receipts in finished turns no longer change when the todo list is rewritten later.

   and:

   > Streaming reasoning no longer appears between a beat's narration and its tool calls, where it pushed the calls apart. It shows at the bottom of the transcript, in the TUI and the web.
5. Commit `CHANGELOG.md`, `docs/tui-simplification/` and, if changed, `README.md` as explicit paths. Then merge `p-b-lifecycle` into `main` only when the user asks.

**Verify:** step 1's commands. `git status --short` shows only the unrelated untracked docs that were there before.

## Integration notes

- **P-C (live box)** renders `SplitLive`'s `live` in the box and its `history` in the viewport. The placement rule (decision 1) already treats an open beat as the box would: events that end during it are already above it.
  - P-C also deletes the now bar, its attention row and acknowledgement, and the Tasks panel's Plan tab.
  - It can port `MeasureSummary`'s number formatting instead of `workNumber` and `workBytesIEC`.
- **Daemon P2 C6** (`docs/daemon/p2-client-seam.md`) must drop three things:
  - the planned `StackQuery.LiveWork` and the matching ACP `session/stack` parameter, because there is one projection now;
  - the `WireSubagent` additions (`id`, `fallback`, `tail`), because subagent nodes no longer exist;
  - the "density, folding" port, because P-A removed both.

  C6 should treat `WireCompletion`, `WirePlan` and `WireTool.subagentId` as the settled names.
- **P-D (skill)** is unaffected. `TestDefaultPromptBudget` still fails at the base (the system prompt is over its ceiling), so P-D must fix the budget before adding a listing line.

## Self-review

- **Cold-executor test.** Every step names the function, the change and the test. Each judgment step states its rule, including the placement rule, the outcome order, the summary order, the measure format and the label source.
- **Contracts.**
  - `CompletionInfo`, `WireCompletion`, `KindCompletion`, `mergeBeats(nodes, open)` and `renderCompletion` are each used by more than one task.
  - So are the new `Snapshot`, `StartedSubagent`, `WireTool.SubagentID` and `SplitLive`.
  - The internal signatures that change within one task (`buildTurn`, `effectiveTodoID`, `labelTransitions`, `receipt`) are described in their task.
- **Anchors.** Every existing file, symbol and test named here was checked at `2325ca73`. The TUI test names come from the test files, and the web test names are quoted from the `it(...)` strings.
- **Scope.** Tasks 1 to 3 cross packages, because deleting a viewmodel type breaks the TUI build until its renderer goes. Each such step pins the replacement exactly.
- **Parallel safety.** No task is marked parallel-safe (see Execution).
- **Placeholders.** None. The only search left to the executor is `README.md` in Task 10, step 3, and it has a stated fallback. The palette tokens Task 9 uses (`--color-info`, `--color-violet`) exist in `app.css`.
- **Agreement with the spec.** The two spec statements this plan contradicts are corrected in the spec:
  - the ACP live-work parameter, which never existed;
  - the claim that markers stay after the beat they landed in, which the spec implied but never stated.
