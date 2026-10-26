# P2 plan 3a: the projection — implementation plan

Date: 2026-10-27. Status: ready for review.
Base: `main` at `75ee412d` (plan 2c merged) or later.
- Every anchor below was checked at `75ee412d`. Line numbers are there to find the code fast; the function names are the anchors.
- The JSON literals in Contracts were produced by encoding the declared types, not written by hand.

Approved design: [p2-client-seam.md](../p2-client-seam.md): C1 (packages), C3 (wire rules), C6 (transcript), Migration order item 3a and its revision record entry against `75ee412d`, and tests T2 and T6.
Previous plan: [plan 2c, the session sheet](2026-10-26-p2-2c-sheet-plan.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the wire projection carries everything a collapsed transcript row reads. One shared projector builds it into `acpwire` types, and golden files pin what the TUI draws today. Nothing a user sees changes.
- `session.State` stamps its transcript times from its clock, so a fixture on a fixed clock renders the same text every time.
- T6: golden files of today's transcript and tray for a fixture set, recorded before anything else changes.
- `acpwire` holds the projection types, `WireTextCap`, and the wire names of node kinds, run-event kinds, completion sources and outcomes. `viewmodel` builds them and `acp` serves them, with their JSON unchanged.
- One shared projector: `viewmodel.SnapshotOf` and `viewmodel.ProjectState`, used by `acp`'s stack, flush and node handlers. The TUI builds its snapshot with `SnapshotOf`.
- The row helpers that decide a call row's verb, subject, summary, diff stat, hook hint and failed tail move from the TUI to `viewmodel`. The TUI's functions become one-line wrappers.
- `WireNode` gains `version`, and the payloads gain the collapsed-row fields.

**Non-goals:**
- **The renderer is not ported.** It still reads `viewmodel.Node` payloads. Plan 3c ports it.
- **No `Session` method.** `StackNode` arrives with the browse actions (3b), and `Stack` with the tree (3d).
- **No `NodeDetail` change.** What expanded rows and the inspector need is added by 3b and 3c, next to their readers.
- **No existing JSON changes.** Every existing field keeps its name, type and value. The new fields are optional.
- **No web change.** The web UI ignores the new fields (spec F4).
- **No changelog entry.** Nothing a user sees changes.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/p2-3a-suite`. Create the directory first: `mkdir -p /private/tmp/p2-3a-suite`. Keep the path short: a long one breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **Pre-existing failures** at `75ee412d` are listed in Task 7, from a full run with `-timeout 20m`. A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`. The exceptions are named there.
- **The goldens are written once.** Task 2 writes them with `-update`, and the executor reads each one by eye then. After Task 2 no task runs `-update`. A golden that changes means the task changed what a user sees, and the task is wrong.

**Decisions this plan makes beyond the spec** (decisions 1 to 3, 6 and 7 are also recorded in the spec's revision record):

1. **Plan 3 splits into four plans.** The renderer, browse mode and the drill-in share one tree, so the tree has to move in one plan. The reads of node payloads can move before it, each checked against the goldens.

   | Plan | What moves |
   |---|---|
   | 3a, projection | This plan. |
   | 3b, browse actions | `Session.StackNode`. The inspector, copy, open and the selection's full-subject copy read a node's wire form and detail. Callers lookups move agent side (`Session.Callers`). |
   | 3c, rows | The row renderers take wire payloads. The TUI still builds its own tree and projects it with `ProjectState`. Expanded rows read `StackNode` detail. The settled-row renderer that nothing reaches is deleted. |
   | 3d, tree | `Session.Stack`. The TUI's tree, node identity, expansion, regions, browse items, drill-in, band and completion index come from wire nodes. The text helpers move to an allowed package, and T1b and the allowlist close the group. |

2. **No settled call renders outside a beat.** `Build` files every audit under a beat: an ID'd one, or a loose one for a call with no step (`viewmodel/stack.go:480-494`). It never hands an audit to `passthrough` (`stack.go:730`).
   - So the older settled-row renderer is reached only by tests and by `nodeCopyText`'s item fallback (`browse_actions.go:54`), which no audit reaches. That renderer is `renderCompletedToolCallIn`, `toolRowHead`, and the collapsed diff preview in `toolBody`.
   - The spec's `head` and `diffFiles` are dropped. Plan 3c deletes that renderer.
   - A *running* call can still stand outside a beat (`strayActive`, `stack.go:628`). It renders through `renderActiveToolCall`, and T6 covers it.
3. **The collapsed-row fields.** The spec's list changes, each change because of what a collapsed row reads:
   - added: `WireTool.verb`; `WireCall.summaryLine` and `jobId`; `WireRunning.subject`, `callId` and `args`; `WireThinking.startedAt`; `WireRunEvent.bodyLines`. The spec's revision record gives the reason for each.
   - dropped: `WireCall.stepId` and `WireRunning.stepId`. No collapsed row reads a step ID, and the inspector takes it from `stack_node`'s `CallDetail.StepID`, which exists.
4. **Scrubbing follows the renderer.** Each new display field is scrubbed where the renderer scrubs that value today:
   - `subject`, `summaryLine`, `killedReason` and the running `subject` with `ScrubText`;
   - each `tail` line with `ScrubBlock`, as `failedTail` (`spine.go:242`) does;
   - `narration`, `reasoning` and the running `args` stay raw, because the renderer reads them raw: markdown source, a reasoning panel, and the stray row's "$ command" line (`transcript.go:764`);
   - `hookHint`, `diffStat` and `jobId` are built from fixed words, counts and a job ID.
5. **`version`** is the lower-case hex of `Node.Version` (`strconv.FormatUint(v, 16)`), omitted when `Version` is 0. Turn nodes and running calls have none.
6. **The goldens** are rendered under `NO_COLOR` with the flat theme, as the tray's frames are (`livebox/frames_test.go:350`).
   - Colour comes from the theme, not from the wire. `NO_COLOR` also keeps the files readable and free of terminal detection.
   - They are recorded first, at the base. Every later task in plans 3a to 3d is checked against today's output.
   - The fixtures' state and the model share one fixed clock (decision 7). Subagent IDs come from a process-wide counter (`subagents.go:164`), so the one scene that registers a subagent fixes its ID's width and replaces it in the text (Task 2, step 5).
7. **The state stamps transcript times from its clock.** `WithClock` (`session.go:909`) sets `s.now`, but nothing reads it. Task 1 makes the ten sites that stamp a transcript time read it, through a nil-safe `clockNow`.
   - A `State` built as a literal (`&session.State{}`, as `doctorpanel_test.go:13` does) has no clock, and `clockNow` falls back to `time.Now`.
   - The default stays `time.Now`, so production is unchanged.
   - `TestPromptPrefixStableAcrossSteps` (`internal/agent/cacheprefix_test.go:71`) is the only `WithClock` user today. Task 1's Verify runs it.
8. **The shared projector.**
   - `SnapshotOf(st, busy, now)` is the snapshot both copies build today (`acp/stack.go:107` and `tui/viewport_render.go:66-77`). It is busy when `busy` is true, a call is running, or reasoning is streaming.
   - `Project(turns, workdir)` takes the active root a call's subject elides. Every caller passes the projected state's `Workspace().ActiveRoot`, the value the TUI hands the renderer as `stepRenderCtx.workdir` today (`viewport_render.go:120`).
   - `ProjectState(st, busy, now)` runs `SnapshotOf`, `Build`, `Project` and `BandOf`, then appends the band node after the tree's nodes and leaves it out of `Roots`. That is the sequence at `acp/stack.go:200-204` and `304-310`.
   - In `acp`, `flushProjector` and `stackSnapshot` use `ProjectState`, and `wakeAt` reads `Projection.Band.StaleAt`. `treeFor` builds with `SnapshotOf`. `StackNode` projects its one node with the source state's active root.
   - In the TUI, `refreshViewport` takes its snapshot from `SnapshotOf(transcriptState, m.busy, m.now())`. It keeps calling `Build` and `BandOf` itself, and reads the items and the in-progress message from the snapshot. Plan 3d replaces all of it with `Stack`.
9. **The row helpers move to `viewmodel`** (`rows.go`), and the TUI's functions become one-line wrappers. The TUI already wraps `viewmodel` this way (`toolTarget`, `symbolSubject`, `stepOwner`). The pure-helper tests move with them, and the renderer tests stay where they are. Three helpers are new compositions the TUI then calls too, so there is one source of truth:
   - `RunningSubject`, the subject `renderActiveCallRow` builds inline (`spine.go:313-321`);
   - `SummaryLine`, the summary's first line, scrubbed, which `callResult` and `renderGroupRow` build inline;
   - `CallDiffStat`, the diff stat `callResult` shows only for an edit or a whole-file write (`spine.go:148`).
10. **The wire names move to `acpwire` as constants**, and `viewmodel` defines its names from them, so no caller changes. `kindNames` and `runEventKinds` use the constants. `CompletionJob`, `CompletionSubagent` and the five `Outcome*` names become `= acpwire.…`.

**Execution:**
- Branch first: `git switch -c p2-3a-projection` from `main`.
- If `git status` shows `docs/daemon/p2-client-seam.md` or this plan uncommitted, commit them first as explicit paths, with the message `p2-3a-projection: record the plan 3 split and plan 3a`.
- Run the tasks in order. Run each task's Verify and commit it as `p2-3a-projection: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- Run `gofmt -w` on every Go file a task touches before its Verify.

## Contracts

**`internal/app/session` (Task 1).** No exported change.

```go
// clockNow is the state's clock: the one WithClock set, or time.Now for a
// State that New did not build.
func (s *State) clockNow() time.Time
```

**`internal/acpwire/stack.go` (new), after Task 3.**
- Moved verbatim, doc comments included, from `viewmodel/wire.go`: `WireTextCap` (21), `WireTree` (25), `WireNode` (32), `WireBand` (54), `WireTask` (73), `WireBandEntity` (86), `WireStep` (113), `WireTodoLabel` (130), `WireThought` (138), `WireTool` (145), `WireCompletion` (162), `WirePlan` (189), `WireCall` (201), `WireRunning` (221), `WireMessage` (230), `WireThinking` (243), `WireRunEvent` (249) and `WireReceipt` (261). The comment block at `wire.go:11-15` becomes the file's opening comment.
- Moved verbatim from `acp/stack.go:27-34`: `StackSnapshot`, its `Nodes` typed `[]WireNode`.
- New:

```go
// Node kinds: the values of WireNode.Kind.
const (
	KindTurn        = "turn"
	KindStep        = "step"
	KindTool        = "tool"
	KindMessage     = "message"
	KindFinal       = "final"
	KindSubagent    = "subagent"
	KindRunEvent    = "runEvent"
	KindJobExit     = "jobExit"
	KindThinking    = "thinking"
	KindPassthrough = "passthrough"
	KindReceipt     = "receipt"
	KindCompletion  = "completion"
	KindPlanUpdate  = "planUpdate"
	KindBand        = "band"
)

// BandID is the band node's ID and LiveThinkingID the live reasoning node's.
// Neither changes from one patch to the next.
const (
	BandID         = "band"
	LiveThinkingID = "think:live"
)

// Run-event kinds: the values of WireRunEvent.Kind.
const (
	RunEventVerifyFailed = "verifyFailed"
	RunEventGateSkipped  = "gateSkipped"
	RunEventReview       = "review"
	RunEventCommit       = "commit"
	RunEventRetry        = "retry"
	RunEventConcern      = "concern"
	RunEventTaskDone     = "taskDone"
)

// Completion sources: the values of WireCompletion.Source.
const (
	CompletionJob      = "job"
	CompletionSubagent = "subagent"
)

// Completion outcomes: the values of WireCompletion.Outcome, apart from a
// job's "exit N".
const (
	OutcomeDone    = "done"
	OutcomeStopped = "stopped"
	OutcomeLost    = "lost"
	OutcomeFailed  = "failed"
	OutcomeUnknown = "outcome unknown"
)
```

**The collapsed-row fields, after Task 6.** Each struct gains these fields, in this order. `Version` goes directly after `Live`, and `Verb` directly after `Display`. Every other field goes after the struct's last existing field.

```go
// WireNode
	// Version is the hex of the node's payload hash: a client caches a
	// rendered row while it is unchanged. Empty for a node without one.
	Version string `json:"version,omitempty"`

// WireTool
	Verb string `json:"verb,omitempty"` // the word the row leads with

// WireCall
	Subject      string   `json:"subject,omitempty"`      // what the call acted on, scrubbed, its first line
	SummaryLine  string   `json:"summaryLine,omitempty"`  // the summary's first line, scrubbed
	DiffStat     string   `json:"diffStat,omitempty"`     // "+N −M" over the whole diff, for an edit or a whole-file write
	HookHint     string   `json:"hookHint,omitempty"`     // the hook decision the row shows first
	KilledReason string   `json:"killedReason,omitempty"` // why the sandbox killed the command
	JobID        string   `json:"jobId,omitempty"`        // the background job the call started
	Tail         []string `json:"tail,omitempty"`         // a failed call's last output lines

// WireRunning
	Subject string `json:"subject,omitempty"` // what the call acts on, scrubbed
	CallID  string `json:"callId,omitempty"`
	Args    string `json:"args,omitempty"` // the arguments as the tool shows them, capped

// WireStep
	// Narration is the beat's narration texts in order, each trimmed and an
	// empty one kept, so the count is the beat's narration count.
	Narration []string `json:"narration,omitempty"`

// WireMessage
	Reasoning string `json:"reasoning,omitempty"` // capped at WireTextCap

// WireThinking
	StartedAt int64 `json:"startedAt,omitempty"` // when the live reasoning began

// WireRunEvent
	BodyLines int `json:"bodyLines,omitempty"` // the whole body's line count
```

**`internal/viewmodel`, after Tasks 4 to 6.**

```go
// project.go (Task 4)

// SnapshotOf is what Build reads from st at now. busy says a turn is running.
// A running call or streaming reasoning makes the snapshot busy too.
func SnapshotOf(st *session.State, busy bool, now time.Time) Snapshot

// Projection is one transcript, built and projected for the wire.
type Projection struct {
	Turns []*Node          // the built tree
	Band  BandInfo         // the band, from the same snapshot
	Tree  acpwire.WireTree // Turns projected, then the band node, which is not in Roots
}

// ProjectState builds st's transcript at now and projects it: the sequence
// session/stack serves, and the TUI renders from plan 3c on.
func ProjectState(st *session.State, busy bool, now time.Time) Projection

// wire.go (Task 4 changes the signature)

// Project flattens a built tree for the wire. workdir is the active root a
// call's subject elides.
func Project(turns []*Node, workdir string) acpwire.WireTree

// rows.go (Task 5)

func CallVerb(name string) string                               // spine.go:69, with the callVerbs table (46)
func FallbackSubject(name string) string                        // spine.go:83
func CallSubject(ev registry.AuditEvent, workdir string) string // spine.go:98
func SubjectFirstTool(name string) bool                         // symbolrow.go:20
func SearchQualifier(name string) (string, bool)                // symbolrow.go:70, with the searchQualifiers map (60)
func SearchTarget(ev registry.AuditEvent) string                // symbolrow.go:101
func ArgString(args json.RawMessage, key string) string         // symbolrow.go:117
func HookHint(hooks []registry.HookMetadata) string             // transcript.go:1112
func DiffStat(diff string) string                               // symbolrow.go:147, with diffCounts (156)

// RunningSubject is what a running call acts on: its path, else the first
// line of its arguments, else the rest of its tool name. Scrubbed.
func RunningSubject(atc session.ActiveToolCall) string

// SummaryLine is the first line of the call's display summary, scrubbed: what
// its row shows.
func SummaryLine(ev registry.AuditEvent) string

// CallDiffStat is the diff stat a call's row shows: DiffStat of the result
// for an edit or a whole-file write, and "" for any other call.
func CallDiffStat(ev registry.AuditEvent) string

// FailedTailLines is how many output lines a failed row shows collapsed.
const FailedTailLines = 3

// FailedTail is the last FailedTailLines lines of a call's output, or of its
// error when it printed nothing, scrubbed with ScrubBlock and right-trimmed
// of spaces, tabs and carriage returns. It is nil when there is neither.
func FailedTail(ev registry.AuditEvent) []string

// stack.go (Task 6): StepInfo gains, after LiveThinking:

	// LiveSince is when the live reasoning began; set only with
	// LiveThinking.
	LiveSince time.Time
```

`rows.go` also holds an unexported `firstLine(s string) string` with the TUI's semantics (`notice.go:54`): the text before the first newline, right-trimmed of spaces, tabs and carriage returns. `firstLineOf` (`completion.go:413`) trims both ends and is not a substitute.

**The golden harness, `internal/app/tui/transcript_golden_test.go` (Task 2).**

```go
// updateGoldens rewrites the transcript goldens instead of comparing them.
var updateGoldens = flag.Bool("update", false, "rewrite the transcript goldens")

// goldenT0 is when every scene starts. It lies in the past, so no time a
// scene stamps is later than the wall clock.
var goldenT0 = time.Unix(1_700_000_000, 0)

// goldenClock is the one clock a scene's state and model share. A scene moves
// it on explicitly.
type goldenClock struct{ t time.Time }

func (c *goldenClock) now() time.Time
func (c *goldenClock) advance(d time.Duration)

// goldenScene is one fixture. build fills the state and the model; expand,
// when true, presses Ctrl+G (toggleExpandAll) after build; view renders what
// the golden holds (nil: the transcript); marks are text the golden must show
// once ANSI is stripped.
type goldenScene struct {
	name   string
	build  func(t *testing.T, clk *goldenClock, st *session.State, m *Model)
	expand bool
	view   func(m *Model) string
	marks  []string
}

// goldenState is a scene's state: session.New over root at goldenT0, on
// clk, with the session ID "golden", and a workspace whose project and
// active roots are root.
func goldenState(t *testing.T, clk *goldenClock, root string) *session.State

// renderScene builds sc twice, each time on a fresh clock, state, root and
// model, renders both, and fails unless the two renders match. It returns
// the render.
func renderScene(t *testing.T, sc goldenScene) string

// checkGolden compares got with testdata/transcript/<name>.golden, or
// rewrites the file under -update.
func checkGolden(t *testing.T, name, got string)

// burnSubagentIDs registers throwaway subagents on a scratch state until the
// next subagent ID is at least 100, so the scene's subagent has a three-digit
// ID. It fails the test when the counter is already past 999.
func burnSubagentIDs(t *testing.T)
```

**Strings the tests pin:**

| Where | Text |
|---|---|
| `TestStackSnapshotJSON`, a snapshot with every field set (Task 3) | `{"sessionId":"s1","subagentId":2,"rev":3,"roots":["turn:1"],"nodes":[{"id":"turn:1","kind":"turn","children":["step:1","msg:2"]},{"id":"step:1","kind":"step","parent":"turn:1","children":["tool:1:c1"],"live":true,"step":{"headline":"Reading the config.","rest":"Then editing it.","stepIds":[1],"owner":"reviewer #1","roleWord":"reviewer","role":"reviewer","model":"qwen3","provider":"local","todoId":"t2","todo":{"id":"t2","index":2,"total":4,"content":"Edit the config"},"startedAt":1000,"endedAt":2000,"thoughts":[{"text":"weighing it","durationMs":2000}]}},{"id":"tool:1:c1","kind":"tool","parent":"step:1","tool":{"name":"shell.run","display":"Shell","workKey":"w1","calls":[{"callId":"c1","target":"go test","args":"{\"command\":\"go test\"}","summary":"exit 1","output":"FAIL","error":"boom","exitCode":1,"failed":true,"approval":"approved","risk":"low","files":["a.go"],"role":"reviewer","notice":"note","durationMs":3000,"at":4000,"truncated":true}],"running":{"target":"go vet","output":"ok","startedAt":5000,"truncated":true},"subagentId":3}},{"id":"msg:2","kind":"message","parent":"turn:1","message":{"role":"assistant","contentType":"markdown","content":"Done.","final":true,"usage":"12k tok","salvaged":true,"salvageReason":"cut off","thinkMs":1500,"at":6000}},{"id":"think:live","kind":"thinking","thinking":{"text":"hmm","durationMs":700}},{"id":"run:7","kind":"runEvent","runEvent":{"kind":"verifyFailed","taskN":2,"title":"go test","detail":"exit 1","body":"FAIL","severity":"high","at":7000,"truncated":true}},{"id":"done:job-1","kind":"completion","completion":{"source":"job","id":"job-1","subagentId":3,"label":"go build","outcome":"exit 1","failed":true,"exitCode":1,"stopCause":"user","startedAt":8000,"endedAt":9000,"durationMs":1000,"measure":"3/4","summary":"FAIL x","command":"go build ./...","cwd":"/w","output":"out","report":"rep","error":"err","workKey":"w2","executionId":"e1","reportMessageId":11,"truncated":true}},{"id":"plan:tool:1:c2","kind":"planUpdate","plan":{"status":["completed","in_progress"],"done":1,"total":2,"active":2,"activeText":"Edit the config","cleared":true}},{"id":"receipt:1","kind":"receipt","receipt":{"durationMs":400000,"tasks":4,"steps":11,"tools":19,"files":3,"usage":"212k tok","salvaged":true}},{"id":"band","kind":"band","band":{"todos":true,"done":1,"total":4,"active":"Edit the config","stale":true,"staleAgeMs":60000,"staleSince":10000,"next":["Run the evals"],"tasks":[{"id":"t2","content":"Edit the config","status":"in_progress","startedAt":1000,"completedAt":2000,"steps":3,"workMs":4000,"ref":"step:1"}],"entities":[{"ref":"tool:1:c1","label":"go test","detail":"running","startedAt":5000}]}}]}` |
| the same, the zero snapshot | `{"sessionId":"","rev":0,"roots":null,"nodes":null}` |
| `TestCollapsedRowFieldsJSON`, a step (Task 6) | `{"id":"step:1","kind":"step","version":"9f3a","step":{"headline":"Reading the config.","narration":["Reading the config. Then editing it.",""]}}` |
| the same, a tool row | `{"id":"tool:1:c1","kind":"tool","version":"1c","tool":{"name":"shell.run","display":"Shell","verb":"shell","calls":[{"subject":"⌂ go test ./...","summaryLine":"exit 1","diffStat":"+1 −1","hookHint":"hook rewrote","killedReason":"timeout","jobId":"job-1","tail":["--- FAIL: TestEval","FAIL"]}],"running":{"subject":"evals/config.toml","callId":"c2","args":"go vet ./..."}}}` |
| the same, a message | `{"id":"msg:2","kind":"message","message":{"role":"assistant","content":"Done.","reasoning":"weighing two configs"}}` |
| the same, live reasoning | `{"id":"think:live","kind":"thinking","thinking":{"text":"hmm","startedAt":1000}}` |
| the same, a run event | `{"id":"run:7","kind":"runEvent","runEvent":{"kind":"verifyFailed","body":"a\nb","bodyLines":14}}` |
| the goldens' marks | the marks listed under each of Task 2's scenes (step 6) |

## Task 1: The state's clock

**Goal:** every transcript time the state stamps comes from its clock, so a state on a fixed clock builds the same transcript every time.

**Files:** `internal/app/session/session.go`, `messages.go`, `steps.go`, `streaming.go`, `runevents.go`, `subagents.go`, `jobexit.go`, and `clock_test.go` (new).

**Steps:**

1. **`clockNow`** (Contracts) goes in `session.go` after `WithClock` (909). It returns `s.now()`, or `time.Now()` when `s.now` is nil.
2. **The ten sites** read `s.clockNow()` in place of the wall clock:

   | File:line | Today | After |
   |---|---|---|
   | `messages.go:332` | `time.Since(s.inProgress.StartedAt)` | `s.clockNow().Sub(s.inProgress.StartedAt)` |
   | `messages.go:340` | `createdAt := time.Now()` | `s.clockNow()` |
   | `steps.go:75` | the step's `StartedAt` | `s.clockNow()` |
   | `steps.go:109` | the step's `EndedAt` | `s.clockNow()` |
   | `streaming.go:33` | the in-progress `StartedAt` | `s.clockNow()` |
   | `runevents.go:54` | a zero `ev.At` | `s.clockNow()` |
   | `subagents.go:257` | the view's `StartedAt` | `s.clockNow()` |
   | `subagents.go:492` | the view's `EndedAt` | `s.clockNow()` |
   | `jobexit.go:223` | a zero `e.At` | `s.clockNow()` |
   | `session.go:3761` | a zero audit `Timestamp` | `s.clockNow()` |

   Leave every other `time.Now()` in the package alone. The work gate (`session.go:2268`), notices and the request inspector are not the transcript.
3. **`WithClock`'s comment** says a test passes a frozen clock so the transcript's times are fixed: messages and their thinking time, steps, the in-progress reasoning, tool calls, run events, subagents and job exits.
4. **`clock_test.go`** is in package `session`. `TestStateStampsTranscriptTimesFromItsClock` builds a state with `New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{}, WithClock(now))`, where `now` reads a local `clock` that starts at `time.Unix(1000, 0)`. It moves `clock` on by one second before each call and checks the stamped time equals `clock`:
   - `AddMessage(RoleUser, "go", ContentTypePlain)`: the last `Messages()` entry's `CreatedAt`;
   - `BeginStep(Actor{})`: `Step(id)`'s `StartedAt`; then `EndStep(id)`: its `EndedAt`;
   - `BeginStreaming()`: `InProgress().StartedAt`. Then `AppendThinking("x")`, move the clock on 3 seconds, and `AddMessage(RoleAssistant, "ok", ContentTypePlain)`: that message's `ThinkDuration` is 3 seconds;
   - `LogToolCall` of an event with a zero `Timestamp`: the `Transcript()` item whose `Audit` is set has that `Audit.Timestamp`;
   - `AddRunEvent` with a zero `At`: the last `RunEvents()` entry's `At`;
   - `RegisterSubagent("probe", nil)`: the returned view's `StartedAt`; then `FinishSubagent(view.ID, "done", nil)`: `Subagent(view.ID)`'s `EndedAt`;
   - `AddJobExit(JobExit{ID: "job-1", Command: "true"})`: the `Transcript()` item of kind `KindJobExit` has `JobExit.At`.

   Add `TestStateWithoutAClockStampsTheWallClock`: on `&State{}` built as a literal, `clockNow()` is within a second of `time.Now()`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/app/session/ -run 'Clock' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/app/session/ ./internal/agent/
go vet ./internal/app/session/
```

Expected: both new tests pass, and so do the two packages, `TestPromptPrefixStableAcrossSteps` included.

## Task 2: The transcript goldens (T6)

**Goal:** golden files pin what the TUI draws today for every kind of transcript row, collapsed and expanded, and for the tray.

**Files:** `internal/app/tui/transcript_golden_test.go` (new), and eleven files under `internal/app/tui/testdata/transcript/` (new).

**Steps:**

1. **The harness** is declared in Contracts.
   - The `tui` package has no flag named `update` today, so `updateGoldens` takes that name.
   - `goldenState` builds `session.New(config.Default(), root, goldenT0, session.Persistence{SessionID: "golden"}, session.WithClock(clk.now))`, then calls `SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: root})`.
   - A shell command that should read "⌂ …" in a row is written as `"cd " + root + " && …"`. No scene writes `root` anywhere else, because the root differs between the two builds.
2. **`renderScene`** does this for each of its two builds:
   - set `NO_COLOR=1` and `TERM=xterm-256color` with `t.Setenv`;
   - save `theme.Current()`, restore it in `t.Cleanup`, and load `theme.LoadWithConfig("", "", theme.DepthFlat, nil)` with `theme.Reload`, as `checkFrame` (`livebox/frames_test.go:352`) does;
   - make a fresh clock at `goldenT0`, a fresh `t.TempDir()` root and `goldenState`;
   - `m := testModel(st)`, then `m.now = clk.now`, then `m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})`, keeping the returned model;
   - run `sc.build`, then `m.toggleExpandAll()` when `sc.expand` is set;
   - then `m.invalidateTranscript()` and `m.refreshViewport()`;
   - render `sc.view(&m)`, or, when `view` is nil, `strings.Join(m.contentLines, "\n") + "\n"`.

   It fails when the two renders differ, naming the first differing line and quoting both versions of it with `%q`.
3. **`TestTranscriptGoldens`** runs every scene of the table in step 6 as a subtest named after the scene. For each it:
   - fails on any line wider than `m.viewport.Width()` cells (`ansi.StringWidth`). For the tray scene the limit is `m.leftWidth`;
   - fails on each mark the render does not contain once `ansi.Strip` has run, with `scene <name> no longer shows <mark>`;
   - calls `checkGolden`.

   `checkGolden` writes `testdata/transcript/<name>.golden` under `-update`, creating the directory. Otherwise it reads the file and compares bytes. On a mismatch it names the first differing line, quotes both versions with `%q`, and prints the whole render with ANSI stripped. A missing file fails with "run with -update to write the goldens".
4. **Times.** "+Ns" in the scene table means `clk.advance(N * time.Second)` before the call. A `LogToolCall` event in the table leaves `Timestamp` zero unless the table gives one, so the state stamps it from the clock. `StepID` is the step the row names. The tables abbreviate `registry.AuditEvent` fields:
   - `args` is `Args` as raw JSON;
   - `sum` is `ResultSummary`;
   - `out` is `ResultContent`;
   - `exit` is `CommandExitCode`, a pointer to the value;
   - `dur` is `Duration`;
   - `err` is `Error`;
   - `call` is `ToolCallID`.
5. **Subagent IDs.** The `completions` scenes call `burnSubagentIDs` before they register their subagent. In the render, `renderScene` replaces every `"#" + strconv.FormatInt(id, 10)` with `"#100"`, where `id` is that subagent's ID. The ID has three digits, so the layout does not move.
6. **The scenes.** Each has a collapsed and an expanded golden (`expand: true`), apart from the tray. The expanded scene reuses the collapsed scene's build. Its name ends in `-expanded`, and its marks are the expanded marks below.

   **`beats`** and **`beats-expanded`**: one settled turn of five beats, with a final answer.

   | # | Call |
   |---|---|
   | 1 | `AddMessage(RoleUser, "Point the eval config at qwen3-coder", ContentTypePlain)` |
   | 2 | `SetTodos`: `t1` "Read the config" in_progress; `t2` "Edit the config", `t3` "Run the evals" and `t4` "Report back" pending. Each item sets `ID`, `Content` and `Status`. |
   | 3 | +1s `s1 := BeginStep(Actor{})`, then `AddNarration(s1, "Reading the eval config first. It names the model.")` |
   | 4 | `LogThinking(ThinkingEntry{Text: "The config lives in evals/config.toml.", Duration: 2s, StartedAt: clk.now(), StepID: s1})` |
   | 5 | +1s `file.read` in `s1`, call `c1`, args `{"path":"evals/config.toml"}`, sum "18 lines", out `model = "llama3"` plus a newline, dur 10ms |
   | 6 | +1s `EndStep(s1)`, then `SetTodos` with `t1` completed, `t2` in_progress, and `t3` and `t4` pending |
   | 7 | +1s `s2 := BeginStep(Actor{})`, then `AddNarration(s2, "Pointing the eval config at qwen3-coder.")` |
   | 8 | +1s `file.write_patch` in `s2`, call `c2`, args `{"path":"evals/config.toml"}`, `FilesChanged` `[evals/config.toml]`, sum "patched evals/config.toml". Its out is a one-hunk unified diff of `evals/config.toml` with `--- a/` and `+++ b/` headers, `@@ -1 +1 @@`, removing `model = "llama3"` and adding `model = "qwen3-coder"`. |
   | 9 | +1s `todo.write` in `s2`, call `c3`, args `{"todos":[…]}` holding the four todos as step 6 left them (`id`, `content` and `status` each), sum "tasks updated · 4 items" |
   | 10 | +20s `EndStep(s2)` |
   | 11 | +1s `s3 := BeginStep(Actor{Role: "explorer", Label: "explorer"})`, with no narration: a different actor, so it is a headless beat of its own |
   | 12 | +1s `repo.search` in `s3`, call `c4`, args `{"query":"model ="}`, sum "3 matches"; +1s `repo.search` in `s3`, call `c5`, args `{"query":"provider ="}`, sum "1 match" |
   | 13 | +1s `EndStep(s3)` |
   | 14 | +1s `s4 := BeginStep(Actor{Role: "reviewer", Label: "reviewer #1", Model: "qwen3-coder", Provider: "local"})`, then `AddNarration(s4, "Running the evals and a background build.")` |
   | 15 | +1s `shell.run` in `s4`, call `c6`, args `{"command":"cd <root> && go test ./evals/..."}`, exit 1, dur 3s, sum "exit 1". Its out is these four lines, each ending in a newline: `=== RUN   TestEval`, `--- FAIL: TestEval (0.01s)`, `    eval_test.go:12: want qwen3-coder`, `FAIL`. |
   | 16 | +1s `shell.run` in `s4`, call `c7`, args `{"command":"go vet ./..."}`, exit 0, dur 1s, sum "exit 0", `Hooks` `[{Event: "PreToolUse", Rewrote: true}]` |
   | 17 | +1s `shell.run` in `s4`, call `c8`, args `{"command":"sleep 600"}`, exit 137, dur 30s, sum "command killed: timeout", `Sandbox` `{Enabled: true, Backend: "restricted", KilledReason: "timeout"}` |
   | 18 | +1s `shell.run` in `s4`, call `c9`, args `{"command":"go build ./..."}`, sum "started background job job-1" |
   | 19 | +1s `agent.run` in `s4`, call `c10`, args `{"description":"review the config change","role":"reviewer"}`, sum "started as subagent 3 — review the config change". No subagent is registered: the row reads the ID from the summary (`viewmodel.StartedSubagent`'s fallback). |
   | 20 | +1s `file.read` in `s4`, call `c11`, args `{"path":"evals/old.toml"}`, err "no such file: evals/old.toml" |
   | 21 | +1s `EndStep(s4)` |
   | 22 | +1s `s5 := BeginStep(Actor{})`, then `AddNarration(s5, "Reading every eval file.")` |
   | 23 | Ten calls in `s5`, each +1s, calls `c12` to `c21`, alternating so none merge. An odd one is `file.read` with args `{"path":"evals/eN.toml"}` and sum "4 lines". An even one is `repo.search` with args `{"query":"eN"}` and sum "2 hits". N runs from 1 to 10 in order. |
   | 24 | +1s `EndStep(s5)` |
   | 25 | +1s `AddMessageFinalWithUsage(RoleAssistant, "The eval config now points at **qwen3-coder**.", ContentTypeMarkdown, 21, "12k tok")` |

   `beats` marks: `▸ 2/4`, `thought 2s`, `22s`, `×2`, `Edit the config`, `+1 −1`, `exit 1 · 3s`, `want qwen3-coder`, `hook rewrote`, `command killed: timeout`, `job-1 started`, `#3 started`, `no such file`, `2 earlier calls`, `reviewer #1`, `12k tok`.
   `beats-expanded` marks: `The config lives in evals/config.toml.`, `model = "llama3"`, `error: no such file: evals/old.toml`, `args: {"path":"evals/old.toml"}`, `1 match`, `evals/e1.toml`.

   **`messages`** and **`messages-expanded`**: one turn of standalone messages, ending in a salvaged answer.

   | # | Call |
   |---|---|
   | 1 | `AddMessage(RoleUser, "Tidy the notes", ContentTypePlain)` |
   | 2 | +1s `AddMessage(RoleSystem, "Model switched to qwen3-coder.", ContentTypePlain)` |
   | 3 | +1s `AddMessage(RoleSystem, "tdd", ContentTypeSkill)` |
   | 4 | +1s `AddMessage(RoleSystem, "Compacted 40 earlier messages.", ContentTypeCompaction)` |
   | 5 | +1s `AddMessage(RoleUser, "also check the provider", ContentTypeSteering)` |
   | 6 | +1s `AddMessage(RoleAssistant, "1. Read the notes\n2. Tidy them", ContentTypePlan)` |
   | 7 | +1s `AddMessage(RoleAssistant, "3 files changed", ContentTypeToolResult)` |
   | 8 | +1s `AddMessage(RoleAssistant, "func main() {}", ContentTypeCode)` |
   | 9 | +1s `AddMessage(RoleSystem, "tdd\ngo-style\nreview", ContentTypeSkillAuto)` |
   | 10 | +1s `BeginStreaming()` and `AppendThinking("Weighing which notes to keep.")`; +4s `AddMessage(RoleAssistant, "Keeping the **recent** notes.", ContentTypeMarkdown)` |
   | 11 | +1s `AddMessageSalvaged(RoleAssistant, "Partial answer.", ContentTypeMarkdown, "cut off")` |

   `messages` marks: `Model switched to qwen3-coder.`, `skill.load: tdd`, `Compacted 40 earlier messages.`, `also check the provider`, `Read the notes`, `3 files changed`, `func main`, `skills: tdd, go-style +1`, `thought for 4s`, `Keeping the`, `salvaged`.
   `messages-expanded` marks: `skills auto-loaded`, `review`, `Weighing which notes to keep.`.

   **`run-events`** and **`run-events-expanded`**: one turn of plan-run events.
   - First `AddMessage(RoleUser, "/sdd run plan.md", ContentTypePlain)`.
   - Then, each +1s, `AddRunEvent` with a zero `At`:

   | Kind | Fields |
   |---|---|
   | `RunEventVerifyFailed` | `TaskN` 2, `Title` "go test ./...", `Detail` "exit 1", `Body` the fourteen lines `line 01` to `line 14` joined by newlines |
   | `RunEventGateSkipped` | `TaskN` 3, `Title` "no test command" |
   | `RunEventReview` | `TaskN` 3, `Detail` "clean" |
   | `RunEventReview` | `TaskN` 4, `Severity` "high", `Title` "missing error check", `Detail` "issues" |
   | `RunEventCommit` | `TaskN` 4, `Title` "abc1234", `Detail` "wire the client" |
   | `RunEventRetry` | `TaskN` 5, `Title` "attempt 2", `Detail` "verify failed" |
   | `RunEventConcern` | `TaskN` 5, `Title` "flaky test" |
   | `RunEventTaskDone` | `TaskN` 5, `Detail` "3 commits" |

   `run-events` marks: `go test ./... failed — exit 1`, `line 12`, `2 more lines`, `verification skipped`, `review clean`, `missing error check`, `abc1234`, `attempt 2`, `concern — flaky test`, `done — 3 commits`.
   `run-events-expanded` marks: `line 14`.

   **`completions`** and **`completions-expanded`**: two background jobs and a subagent, finished.

   | # | Call |
   |---|---|
   | 1 | `AddMessage(RoleUser, "Build and review", ContentTypePlain)` |
   | 2 | +1s `s1 := BeginStep(Actor{})`, then `AddNarration(s1, "Starting the build and a review.")` |
   | 3 | +1s `shell.run` in `s1`, call `c1`, args `{"command":"go build ./..."}`, sum "started background job job-1" |
   | 4 | +1s `shell.run` in `s1`, call `c2`, args `{"command":"go test ./..."}`, sum "started background job job-2" |
   | 5 | +1s `burnSubagentIDs(t)`. Then a child state, `session.New(config.Default(), root, clk.now(), session.Persistence{}, session.WithClock(clk.now))`, and `v := RegisterSubagent("review the config change", child)`. Then `agent.run` in `s1`, call `c3`, args `{"description":"review the config change"}`, with sum "started as subagent " + the decimal `v.ID` + " — review the config change". |
   | 6 | +1s `EndStep(s1)` |
   | 7 | +12s `AddJobExit(JobExit{ID: "job-1", Command: "go build ./...", Exit: &zero, Duration: 12s, Output: "ok\n", Cwd: "evals"})` |
   | 8 | +30s `AddJobExit(JobExit{ID: "job-2", Command: "go test ./...", ExitCode: 1, Exit: &one, Duration: 41s, Cwd: "evals"})`. Its `Output` is these five lines, each ending in a newline: `=== RUN   TestA`, `--- FAIL: TestA (0.00s)`, `    a_test.go:9: boom`, `FAIL`, `exit status 1`. |
   | 9 | +5s `FinishSubagent(v.ID, "## Review\n\nThe config change is **correct**.\n\n- the model name matches", nil)` |

   `completions` marks: `job-1`, `job-2`, `exit 1`, `--- FAIL: TestA`, `review the config change`, `#100`.
   `completions-expanded` marks: `outcome`, `a_test.go:9: boom`, `The config change is`.

   **`live`** and **`live-expanded`**: a turn still running. The build sets `m.busy = true` and `m.turnStartedAt = goldenT0`.

   | # | Call |
   |---|---|
   | 1 | `AddMessage(RoleUser, "Run the evals", ContentTypePlain)` |
   | 2 | +1s `s1 := BeginStep(Actor{})`, then `AddNarration(s1, "Running the eval suite now.")`. The step stays open. |
   | 3 | `SetSandboxInfo(SandboxInfo{Backend: "restricted", NetworkIsolation: true})` |
   | 4 | +1s `SetActiveToolCall(ActiveToolCall{Name: "shell.run", Args: "go test ./evals/...", StepID: s1, ToolCallID: "c1", StartedAt: clk.now()})`. Then `AppendActiveToolCallOutput("c1", …)` with the eight lines `ok  evals/aN 0.0Ns`, N from 1 to 8, each ending in a newline. |
   | 5 | +1s `SetActiveToolCall(ActiveToolCall{Name: "agent.run", Args: "review the evals", StepID: s1, ToolCallID: "c2", StartedAt: clk.now()})`. Then set `m.workSnapshot = workSnapOf(item)`. `item` is a `viewmodel.WorkItem` with `TaskKey` "k1", `Kind` "agent", `SourceID` "7", `DisplayID` "#7", `Label` "review the evals", `State` "running", `StartedAt` `clk.now()` and `Origin.ToolCallID` "c2". |
   | 6 | +1s `SetActiveToolCall(ActiveToolCall{Name: "shell.run", Args: "make lint", ToolCallID: "c3", StartedAt: clk.now()})`, with no `StepID`: it stands outside any beat. Then `AppendActiveToolCallOutput("c3", "lint: 2 warnings\n")`. |
   | 7 | `BeginStreaming()` and `AppendThinking("Checking whether the lint warnings matter.")` |
   | 8 | +5s |

   `live-expanded` does not press Ctrl+G. A running call stays collapsed unless it is opened itself, so its build ends with `m.toggleExpanded(viewmodel.ActiveToolID(atc))` for the `ActiveToolCall` values of rows 4 and 6.
   `live` marks: `Running the eval suite now.`, `evals/a8`, `sandbox: restricted`, `→ ⧉ #7`, `$ make lint`, `lint: 2 warnings`, `thinking`, `Checking whether the lint warnings matter.`.
   `live-expanded` marks: `evals/a1`.

   **`tray`**: the tray's plan, held and running rows and its plan-run row. Its `view` renders `livebox.Render(livebox.Plan(m.boxInput()), m.leftWidth)`.

   | # | Call |
   |---|---|
   | 1 | `AddMessage(RoleUser, "Edit and build", ContentTypePlain)`, then `SetTodos`: `t1` "Read the config" completed, `t2` "Edit the config" in_progress, `t3` "Run the evals" and `t4` "Report back" pending |
   | 2 | +1s `s1 := BeginStep(Actor{})`, then `AddNarration(s1, "Building in the background.")`; +1s `shell.run` in `s1`, call `c1`, args `{"command":"go build ./..."}`, sum "started background job job-1"; +1s `EndStep(s1)` |
   | 3 | +10s `AddJobExit(JobExit{ID: "job-1", Command: "go build ./...", ExitCode: 1, Exit: &one, Duration: 11s, ExecutionID: "e1", ConversationID: "conv", SourceEpoch: "ep1"})`, with `Output` `--- FAIL: TestB` and `FAIL` as two newline-terminated lines |
   | 4 | `m.heldSince = goldenT0`. Set `m.workSnapshot` to `workSnapOf` of two items. The first is `workRunningCard("k2", clk.now().Add(-5*time.Second))`. The second is a `viewmodel.WorkItem` with `TaskKey` `viewmodel.WorkKey("conv", "ep1", "command", "e1")`, `Kind` "command", `DisplayID` "job-1", `Label` "go build ./...", `State` "failed", `StartedAt` `clk.now().Add(-11*time.Second)` and `EndedAt` `clk.now()`. |
   | 5 | `m.busy = true`, `m.turnStartedAt = goldenT0`, and `SetSDDProgress(SDDProgress{Active: true, PlanName: "p2-3a", TotalTasks: 7, DoneTasks: 3, CurrentTask: 4, Phase: "verifying"})` |

   `tray` marks: `Edit the config`, `go build ./...`, `exit 1`, `--- FAIL: TestB`, `k2`, `p2-3a`. The held row's `exit 1` and summary come from the completion event, through `m.completions`. Without it the row would read `failed`.
7. **Record.** Run the Verify's `-update` command once, then read each of the eleven files. Every row in the tables is there, and nothing is cut off where it should not be. Then run the test without `-update`.
8. **When a mark is missing**, compare the scene's build with its table first. The marks were derived from the renderer's code at `75ee412d`. If the build matches the table and the mark is still missing, stop and report it rather than editing the mark.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/app/tui/ -run 'TestTranscriptGoldens' -update
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/app/tui/ -run 'TestTranscriptGoldens' -count=3 -v 2>&1 | grep -E '^(    --- |--- |ok|FAIL)'
ls internal/app/tui/testdata/transcript/
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/app/tui/
go vet ./internal/app/tui/
```

Expected:
- the first command writes the eleven files;
- the second passes all eleven subtests three times;
- `ls` lists `beats.golden`, `beats-expanded.golden`, `messages.golden`, `messages-expanded.golden`, `run-events.golden`, `run-events-expanded.golden`, `completions.golden`, `completions-expanded.golden`, `live.golden`, `live-expanded.golden` and `tray.golden`;
- the package fails only on Task 7's baseline list.

Commit the eleven golden files with the test.

## Task 3: The wire types move to `acpwire`

**Goal:** `acpwire` holds the projection types, `StackSnapshot` and the wire names. `viewmodel` and `acp` use them, and their JSON is unchanged.

**Files:**
- `internal/acpwire/stack.go` (new) and `internal/acpwire/stack_test.go` (new);
- `internal/viewmodel/wire.go`, `stack.go`, `completion.go`, and the tests that name the moved types: `wire_test.go` and `plan_test.go`;
- `internal/acp/stack.go`, `stacknode.go`, `stack_test.go`, `stacknode_test.go`.

**Steps:**

1. **`acpwire/stack.go`** holds the moved declarations and the new constants exactly as in Contracts. Every moved type keeps its fields, their order, their tags and their comments.
2. **`viewmodel/wire.go`** loses the moved declarations. Every use names `acpwire.` instead. `Project`, `ProjectBand`, `wireNode`, `wireStep`, `wireTools`, `wireActive` and `wireItem` build `acpwire` values. `capText` and `capTail` cut at `acpwire.WireTextCap`.
   - `kindNames` uses the `acpwire.Kind*` constants. The comments on its `KindSubagent` and `KindJobExit` entries stay.
   - `runEventKinds` uses the `acpwire.RunEvent*` constants.
   - `ProjectBand` uses `acpwire.BandID` and `acpwire.KindBand`.
3. **`viewmodel/completion.go:17-27`.** `CompletionJob`, `CompletionSubagent` and the five `Outcome*` names are defined as the `acpwire` constants of the same name, so nothing that uses them changes.
4. **`viewmodel/stack.go`.** `LiveThinkingID` (`stack.go:173`) is `NodeID{KindThinking, acpwire.LiveThinkingID}`.
5. **`acp/stack.go`.**
   - Delete `StackSnapshot` (27-34). `stackSnapshot` returns an `acpwire.StackSnapshot`.
   - `stackPatch.Upsert` is `[]acpwire.WireNode`, and `diff` takes an `acpwire.WireTree`.
6. **`acp/stacknode.go`** needs no type change: the node it returns is already whatever `Project` returns.
7. **Tests:**
   - `wire_test.go`, `plan_test.go` (191), `acp/stack_test.go` and `acp/stacknode_test.go` name `acpwire.WireNode`, `acpwire.WireTextCap` and `acpwire.StackSnapshot` where they named the `viewmodel` or `acp` ones;
   - **`acpwire/stack_test.go`**, `TestStackSnapshotJSON`. It marshals two values and compares each with its literal from the strings table. The first is the snapshot with every field set, built from the values the literal spells out, field for field. The second is the zero `StackSnapshot`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rnE 'viewmodel\.(Wire[A-Z]|WireTextCap)|acp\.StackSnapshot|^type (Wire[A-Z][A-Za-z]*|StackSnapshot) ' --include='*.go' internal | grep -v '^internal/acpwire/'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/acpwire/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/viewmodel/ ./internal/acp/ ./internal/app/tui/
go vet ./internal/acpwire/ ./internal/viewmodel/ ./internal/acp/
```

Expected:
- the grep prints nothing;
- `TestStackSnapshotJSON` passes, and so do `TestWireFieldsFollowTheRules` and `TestWireImportsOnlyTheStandardLibrary`, now over the moved types;
- `viewmodel` and `acp` pass unchanged;
- `TestTranscriptGoldens` passes, and `tui` fails only on Task 7's baseline list.

## Task 4: The shared projector

**Goal:** one function builds a state's transcript for the wire. `acp`'s stack, flush and node handlers use it, and the TUI builds its snapshot with `SnapshotOf`.

**Files:** `internal/viewmodel/project.go` (new), `project_test.go` (new), `wire.go`, `wire_test.go`, `plan_test.go`; `internal/acp/stack.go`, `stacknode.go`; `internal/app/tui/viewport_render.go`.

**Steps:**

1. **`project.go`** holds `SnapshotOf`, `Projection` and `ProjectState` (Contracts).
   - `SnapshotOf` has the body of `stackSnapshotOf` (`acp/stack.go:107`).
   - `ProjectState` follows decision 8. Its `Band` is the `BandOf` result it appends.
2. **`Project`** gains `workdir` (Contracts). It accepts the parameter and does not use it yet: Task 6 passes it on. Its comment names what `workdir` is for.
3. **`acp/stack.go`:**
   - delete `stackSnapshotOf` (105-116);
   - in `flushProjector` (200-205), `proj := viewmodel.ProjectState(st, busy, now)`. It diffs `proj.Tree`, and `p.wakeAt` is `proj.Band.StaleAt`;
   - in `stackSnapshot` (304-310), the tree is `viewmodel.ProjectState(src, busy, time.Now()).Tree`. The comment on the band node stays.
4. **`acp/stacknode.go`:**
   - `treeFor` (283) builds `viewmodel.Build(viewmodel.SnapshotOf(src, busy, time.Now()))`;
   - `StackNode` (324) projects with `viewmodel.Project([]*viewmodel.Node{n}, state.Workspace().ActiveRoot)`.
5. **`tui/viewport_render.go:66-77`.** The three reads and the `viewmodel.Snapshot` literal become `snap := viewmodel.SnapshotOf(transcriptState, m.busy, m.now())`. `items` and `inProgress` become `snap.Items` and `snap.InProgress` where the function reads them later: `contentSignature`'s `hasConversationTurns` argument, the welcome banner check and `thinkElapsed`.
6. **Tests:**
   - the ten `Project(…)` calls in `wire_test.go` and `plan_test.go` pass `""` as `workdir`;
   - **`project_test.go`**:
     - `TestSnapshotOfIsBusyWhileWorkRuns`: with `busy` false, the snapshot is busy when the state has a running call, and when it has streaming reasoning (`BeginStreaming` and `AppendThinking`), and idle when it has neither. With `busy` true it is busy;
     - `TestSnapshotOfCarriesTheState`: the snapshot's `Items`, `Steps`, `Todos`, `ActiveTools`, `InProgress` and `Now` equal the state's reads and the `now` passed;
     - `TestProjectStateAppendsTheBandAfterTheTree`. Build a state with a todo list and one beat. Then:
       - `Tree.Roots` equals `Project(Build(SnapshotOf(…)), root).Roots`;
       - `Tree.Nodes` is those nodes followed by `ProjectBand(Band)`;
       - the last node's ID is `acpwire.BandID` and is in no `Roots`;
       - `Band` equals `BandOf` of the same snapshot and tree.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -rn 'stackSnapshotOf' --include='*.go' internal
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/viewmodel/ -run 'SnapshotOf|ProjectState' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/viewmodel/ ./internal/acp/ ./internal/app/tui/
go vet ./internal/viewmodel/ ./internal/acp/ ./internal/app/tui/
```

Expected: the grep prints nothing. The three new tests pass, and `viewmodel` and `acp` pass. `TestTranscriptGoldens` passes, and `tui` fails only on Task 7's baseline list.

## Task 5: The row helpers move to `viewmodel`

**Goal:** `viewmodel` decides a call row's verb, subject, summary line, diff stat, hook hint and failed tail. The TUI's functions call it, and nothing the TUI draws changes.

**Files:** `internal/viewmodel/rows.go` (new), `rows_test.go` (new); `internal/app/tui/spine.go`, `symbolrow.go`, `transcript.go`, `spine_test.go`.

**Steps:**

1. **`rows.go`** holds the Contracts functions:
   - the moved ones keep their bodies and comments, each calling the moved form of any helper it used (`CallSubject` calls `SubjectFirstTool`, `SearchQualifier`, `SearchTarget`, `ArgString` and `FallbackSubject`), plus `viewmodel`'s own `IsShellFamily`, `ElideWorkdir`, `ToolTarget`, `SymbolSubject` and `ScrubText`. `firstLine` is the unexported one from Contracts;
   - `RunningSubject` is `renderActiveCallRow`'s subject (`spine.go:313-321`): `Path`, else `firstLine(Args)`, else `FallbackSubject(Name)`, then `ScrubText`;
   - `SummaryLine` is `ScrubText(firstLine(DisplaySummary(ev)))`;
   - `CallDiffStat` is `DiffStat(ev.ResultContent)` when `isDiffTool(ev.ToolName)` or the tool is `file.write`, and "" otherwise. `viewmodel` already has `isDiffTool` (`stack.go:196`);
   - `FailedTail` is the text part of `failedTail` (`spine.go:242-259`) without the width cut and the styling.
2. **The TUI's functions become wrappers** that return the `viewmodel` result:
   - `spine.go`: `callVerb`, `fallbackSubject` and `callSubject`. Delete the `callVerbs` table;
   - `symbolrow.go`: `subjectFirstTool`, `searchQualifier`, `searchTarget`, `argString` and `diffStat`. Delete the `searchQualifiers` map and `diffCounts`;
   - `transcript.go`: `hookIndicatorText` returns `viewmodel.HookHint`.

   Keep each wrapper's comment as a one-line summary that names the `viewmodel` function.
3. **The TUI's inline copies call the new compositions:**
   - `callResult` (`spine.go:130`) computes `stat := viewmodel.CallDiffStat(ev)` before its `switch`. The diff-stat case is `stat != ""`, and appends `stat`. The default case appends `viewmodel.SummaryLine(ev)` when it is not empty;
   - `renderGroupRow` (`spine.go:267`) appends `viewmodel.SummaryLine(ev)` to an expanded line, after the separator, in place of `ScrubText(firstLine(s))`;
   - `renderActiveCallRow` (`spine.go:312`) takes its subject from `viewmodel.RunningSubject(atc)` and passes it to `spineHead` without another `ScrubText`;
   - `failedTail` keeps its signature. It truncates and styles each line of `viewmodel.FailedTail(ev)`. `failedTailLines` is `= viewmodel.FailedTailLines`, because `spine_test.go:158` reads it.
4. **Tests.**
   - Move `TestCallVerbs`, `TestFallbackSubjectNeverRepeatsTheVerb` and `TestCallSubjects` (`spine_test.go:30`, `61`, `114`) to `rows_test.go` as `TestCallVerb`, `TestFallbackSubjectNeverRepeatsTheVerb` and `TestCallSubject`. They call the `viewmodel` functions, with their cases unchanged.
   - New in `rows_test.go`:
     - `TestRunningSubject`: a path wins over arguments; the first line of the arguments when there is no path; `FallbackSubject` of the name when there are neither; an escape sequence in the path is scrubbed;
     - `TestSummaryLineIsTheFirstLineScrubbed`: "4 matches\nmore" gives "4 matches"; an `agent.await` with no error gives its wait (`DisplaySummary`); an ESC byte in the summary is dropped;
     - `TestCallDiffStatIsForEditsAndWrites`: a two-added, one-removed diff gives "+2 −1" for `file.edit`, `file.write_patch`, `patch.apply` and `file.write`, and "" for `shell.run`. A `file.write` whose result is not a diff gives "";
     - `TestFailedTailIsTheLastLines`: five output lines give the last three, each right-trimmed; empty output gives the error's lines; neither gives nil; a carriage return and an ESC are dropped;
     - `TestHookHintPrefersBlockThenRewriteThenFailOpen`: "block" or "halt" gives "hook blocked" even when another hook rewrote; then "hook rewrote"; then "hook failed-open"; otherwise "hooks N"; none gives "".
   - Every other TUI test stays as it is. `TestCallResults` and the row tests in `spine_test.go` and `symbolrow_test.go` now run through the wrappers.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
grep -nE '^var (callVerbs|searchQualifiers)|^func diffCounts' internal/app/tui/*.go
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/viewmodel/ -run 'TestCallVerb|TestFallbackSubject|TestCallSubject|TestRunningSubject|TestSummaryLine|TestCallDiffStat|TestFailedTail|TestHookHint' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/viewmodel/ ./internal/app/tui/
go vet ./internal/viewmodel/ ./internal/app/tui/
```

Expected: the grep prints nothing, and the eight tests pass. `TestTranscriptGoldens`, `TestCallResults` and the spine and symbol-row tests pass, and `tui` fails only on Task 7's baseline list.

## Task 6: `version` and the collapsed-row fields

**Goal:** every wire node carries its version, and each payload carries the collapsed-row fields of Contracts, computed by the Task 5 helpers.

**Files:** `internal/acpwire/stack.go`, `stack_test.go`; `internal/viewmodel/stack.go`, `wire.go`, `wire_test.go`.

**Steps:**

1. **`acpwire/stack.go`** gains the fields exactly as in Contracts.
2. **`StepInfo`** gains `LiveSince` (Contracts). In `buildTurn`, where `reasoning` takes `s.InProgress.Reasoning` (`stack.go:530-533`), keep `s.InProgress.StartedAt` too. The live node (`stack.go:669-672`) sets `LiveSince` from it.
3. **`wire.go`:**
   - `Project` passes `workdir` to `wireNode`, which passes it to `wireTools`.
   - `wireNode` sets `Version` with an unexported `versionHex(v uint64) string` (decision 5). For the live reasoning node it sets `Thinking.StartedAt` to `ms(n.Step.LiveSince)`.
   - `wireStep` sets `Narration` to `strings.TrimSpace` of each narration message's `Content`, in order. An empty one is kept.
   - `wireTools(evs, workdir)` sets `Verb` to `CallVerb(evs[0].ToolName)`. Each call gets:
     - `Subject`: `CallSubject(ev, workdir)`;
     - `SummaryLine`: `SummaryLine(ev)`;
     - `DiffStat`: `CallDiffStat(ev)`;
     - `HookHint`: `HookHint(ev.Hooks)`;
     - `KilledReason`: `ScrubText(ev.Sandbox.KilledReason)`;
     - `JobID`: `JobStartID(ev)`;
     - `Tail`: `FailedTail(ev)`, only when `EventFailed(ev)`.
   - `wireActive` sets `Verb` to `CallVerb(a.Name)`. Its `Running` gets `Subject` (`RunningSubject(*a)`), `CallID` (`a.ToolCallID`) and `Args` (`capText(a.Args)`). `Truncated` is true when either the output or the arguments were cut.
   - `wireItem` sets `Message.Reasoning` to the first return of `capText(m.Reasoning)`. It sets `RunEvent.BodyLines` to 0 for an empty body, and otherwise to the line count of the body with its trailing newlines trimmed: the count `renderRunEvent` (`runevent.go:42`) takes.
4. **Tests** in `wire_test.go`:
   - `TestWireNodesCarryTheirVersion`: in a projected beat with a tool row and a final message, each node's `Version` equals `strconv.FormatUint` (base 16) of its `Node.Version`. The turn node's is "";
   - `TestWireCallsCarryTheirRowFields`: a beat whose calls are the `beats` scene's rows 15 to 20 (Task 2), with `workdir` `/w` and the first command written `cd /w && go test ./evals/...`. Then:
     - the test row's `Subject` is "⌂ go test ./evals/..." and its `Tail` is the three last output lines;
     - the hooked row's `HookHint` is "hook rewrote";
     - the killed row's `KilledReason` is "timeout";
     - the background row's `JobID` is "job-1";
     - the agent row's `Subject` is "review the config change";
     - the failed read's `Tail` is its error's one line, and its `SummaryLine` is "";
     - each row's `Verb` is "shell", "agent" or "read" as its tool says, and no row but the failed ones has a `Tail`;
   - `TestWireEditCarriesItsDiffStat`: a `file.write_patch` call with a one-line change has `DiffStat` "+1 −1", and a `file.read` call has none;
   - `TestWireRunningCarriesItsRowFields`: a running `shell.run` call with `Path` "" and `Args` "make lint" has `Subject` "make lint", its `CallID` and `Args` "make lint". An `Args` longer than `WireTextCap` is cut and sets `Truncated`;
   - `TestWireMessageCarriesItsReasoning`: a message with reasoning carries it, and reasoning longer than `WireTextCap` is cut to it;
   - `TestWireStepCarriesItsNarration`: a beat with the narrations "  First.  " and "   " carries `["First.", ""]`;
   - `TestLiveReasoningCarriesItsStart`: with `InProgress` active since `time.Unix(50, 0)`, the live node's `Thinking.StartedAt` is 50000;
   - `TestWireRunEventCountsItsBodyLines`: a body of fourteen lines and a trailing newline gives 14, and an empty body gives 0.

   And in `acpwire/stack_test.go`, `TestCollapsedRowFieldsJSON`. It marshals five nodes and compares each with its literal from the strings table, building each from the values the literal spells out. `TestStackSnapshotJSON`'s literals are unchanged, because every new field is empty there.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
go build ./...
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/acpwire/ ./internal/viewmodel/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/viewmodel/ -run 'TestWire|TestLiveReasoning' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/p2-3a-suite go test ./internal/acp/ ./internal/app/tui/
go vet ./internal/acpwire/ ./internal/viewmodel/
```

Expected:
- the new tests pass, and so do `acpwire` and `viewmodel`;
- `acp` passes: the patch tests compare node encodings between flushes, and a node's `version` changes only when its payload does;
- `TestTranscriptGoldens` passes, and `tui` fails only on Task 7's baseline list.

## Task 7: Verification and docs

**Goal:** the full suite matches the baseline, and the spec and design record plan 3a.

**Files:** `docs/daemon/p2-client-seam.md`, `docs/daemon/design.md`.

**Steps:**

1. **`p2-client-seam.md`.** Add a plan 3a entry to the Implementation record. It lists:
   - the state's clock;
   - the goldens: eleven files, rendered under `NO_COLOR` at 100 columns;
   - the moved types and the new constants in `acpwire`;
   - `SnapshotOf`, `ProjectState` and `Project`'s new parameter, and who calls them;
   - the helpers moved to `rows.go`, and the three new compositions;
   - `version` and the collapsed-row fields, by struct.

   Note that T1's allowlist and T1b's list did not change: the TUI still imports `viewmodel` in every file it did.
2. **`design.md` §7.** The P2 row's status adds `plan 3a implemented` after `plan 2c implemented`.
3. **Full verification.**

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
gofmt -l ./internal ./cmd
go vet ./...
TMPDIR=/private/tmp/p2-3a-suite CGO_ENABLED=1 go build ./cmd/marshal
TMPDIR=/private/tmp/p2-3a-suite go test ./... -timeout 20m 2>&1 | grep -E '^(--- FAIL|FAIL|ok)' | grep -v '^ok'
git diff --stat <task-2-commit>..HEAD -- internal/app/tui/testdata/transcript
```

`<task-2-commit>` is the Task 2 commit's hash.

Expected:
- `gofmt -l` prints nothing;
- `go vet` is clean;
- the binary builds;
- the failures are exactly the baseline below. A flaky one counts only if it passes alone with `-count=3`;
- the `git diff` prints nothing: no golden changed after Task 2.

**Baseline at `75ee412d`**, from a full run with `-timeout 20m`:
- `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree` and `TestSubagentShellOutputDoesNotReachParent`.
- `internal/app`: `TestDefaultPromptBudget` fails alone too: tool schemas are 15322 bytes, over the 15000 ceiling. It has failed since `0cad39ae`. This plan changes no schema.
- `internal/app`: `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` failed in this run and passed 3 of 3 alone. Both are flaky under load.
- `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`.
- `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`.
- `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`.
- `internal/snapshot`: `TestRunManagedCancellationKillsTree` failed in this run and alone. It counts as pre-existing whether it passes or fails.
- Known to be flaky under load, and passing in this run: `TestIntegrationOwnershipReleaseDrainsWritersBeforeUnlock` (`internal/snapshot`), and `TestACPChildApprovalUsesOwningSessionRegistry` and `TestACPSendNowCancelsPromptRPCWithoutCancelingTurn` (`internal/acp`).
- `internal/acp` passed in 13 s. `web/bridge` passed in 93 s.

Finally, run `./marshal` in this repository and check by eye that nothing has changed:
- a turn that reads, edits and runs a failing command;
- Ctrl+G on and off;
- a background job's completion event.

## Integration notes

- **Plan 3b** adds `Session.StackNode`, and moves `NodeDetail` and its parts into `acpwire`.
  - `CallDetail.Hooks` is `[]registry.HookMetadata` today, whose JSON names include `failed_open`. That name breaks C3's camelCase rule, and T2 forbids an exception. Carry the hooks as a `json.RawMessage` marshalled agent side, which keeps the JSON identical.
  - The inspector needs a call's raw arguments: `CallDetail.Args` is indented.
- **Plan 3c** ports the row renderers to these fields. It deletes the TUI's wrappers from Task 5 and the settled-row renderer (decision 2). What expanded rows need beyond these fields goes into `NodeDetail`:
  - a call's raw arguments and its full result;
  - a completion's full output, its report and its own truncation flag. Today `WireCompletion.truncated` also covers the wire cut;
  - a message's full reasoning;
  - a run event's full body;
  - a running call's whole output.
- **Plan 3d** reads `Session.Stack`. The adapter's `Stack` is `ProjectState` over the session's state or a subagent's child. Its `busy` must match today's `m.busy`, which the TUI owns until plan 5. The state's open parent work (`BeginWork` to `EndWork`) is the candidate, and plan 3d settles it.
- **P3** serves `session/stack` unchanged: `ProjectState` is the shared sequence.

## Self-review

- Every task commits alone.
  - Task 1 is the session alone.
  - Task 2 is a test and its data, recorded at the base.
  - Tasks 3 and 4 move types and calls, and their Verify runs the goldens.
  - Task 5 moves logic behind wrappers, and Task 6 adds fields that nothing renders yet.
- Task 3 spans `acpwire`, `viewmodel` and `acp`, because a moved type breaks the build until its users follow. The change is mechanical: declarations move, and references are renamed.
- Task 5 spans `viewmodel` and the TUI, because the TUI's functions become wrappers in the same commit. Each TUI edit is a body replaced by one call, or an inline expression replaced by a named composition.
- Cold-executor check:
  - every stamping site is in Task 1's table, with its line;
  - every scene's calls, values and marks are in Task 2's tables, and step 8 says what to do when a mark is missing;
  - every moved declaration is named with its line;
  - every caller of `Project` and `stackSnapshotOf` is named;
  - every new field has its source helper and its scrubbing rule (decision 4);
  - every new test names the one behaviour it asserts.
- Contracts hold every cross-task declaration: the clock helper, the moved types and constants, the new fields, the projector, the row helpers, `LiveSince` and the golden harness.
- Anchors were checked at `75ee412d`. The baseline was taken there in a detached worktree.
- Nothing here contradicts the spec. Decisions 1 to 3, 6 and 7 are in its revision record, and C6, the Migration order and T6 were updated to match.
