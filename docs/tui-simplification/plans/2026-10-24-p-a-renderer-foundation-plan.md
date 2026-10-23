# P-A renderer foundation — implementation plan

Date: 2026-10-24. Status: ready for review.
Base: `87282c54` (`main`).
Approved design: [feature-spec.md](../feature-spec.md) §5.2 and §5.3, §6.1 (folding, todo labels, plan updates), §6.3 (`steps.go`, density), §6.5 and §7 (P-A). The spec was updated on 2026-10-24 with the decisions listed below. Frames: [framed-console.md](../framed-console.md).
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** history reads as beats on a spine.
- A beat's head carries its narration, its thinking time and, on the beat that starts a todo, a `▸ N/M` tag.
- Its calls hang off `├ └`, each with a verb, a subject and a right-aligned result.
- A plan line sits where the todo list was written.
- Nothing folds. There is one level of detail: Enter or a click expands a row, and Ctrl+G expands everything.
- `[tui.transcript]` is retired.

```
 ● Planning this in three steps.                                           ▸ 1/3
   └ plan    ▸ · ·  1/3  Start the download

 ✗ Running the harness tests.                                              ▸ 3/3
   ├ shell   go test ./evals/...                                     exit 1 · 3s
   │         ok a
   │         --- FAIL: TestThresholds
   │         FAIL
   ├ read    evals/thresholds.toml, evals/limits.toml                         ×2
   └ edit    evals/thresholds.toml                                          ⠋ 2s
```

(The prototype at 80 columns.)

**Non-goals:** everything the later phases own.
- P-B: completion events, dispatch rows that never settle (`settleJob` and the job row's live state stay), one projection, band gating, `SplitLive`, and the web's rendering of the new kind.
- P-C: the live box, the now bar, the Tasks panel and Ctrl+T, running rows, and the `→ #n` pointer.
- P-D: the `progress-reporting` skill.

Also out:
- Keyboard design: Enter and Ctrl+G keep their keys.
- The web UI. It gains one wire kind it skips and loses `folded`, and otherwise renders as before.
- Deleting the category glyph constants. They leave history here, but the now bar, the in-place subagent cards and the standalone `renderCompletedToolCallIn` still use them.

**Assumptions:**
- Daemon P2 has not started (`docs/daemon/plans/` holds P0 and P1 only), so nothing else is moving the renderer. P2 plan 3 records this renderer as its T6 goldens.
- On macOS, `t.TempDir()` under the default `/var` TMPDIR breaks unrelated tests, so every test command below runs with `TMPDIR=/private/tmp/pa-suite`. Create it first: `mkdir -p /private/tmp/pa-suite`.
- These tests fail at the base commit, on this machine, before any change. They are not caused by this plan, and no task's Verify expects them to pass:
  - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`. This is "the known failure" in each Verify.
  - `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestAgentRunSystemFlagParentWithoutSystem`, `TestSubagentShellOutputDoesNotReachParent`, `TestAccessTUIOptionsResolveWithoutConfigReload`, `TestDefaultPromptBudget` and `TestRuntimeNewAndResumedSessionResetAccess`.
    - All seven fail at `87fa8f0e` (the interaction-modes merge).
    - `TestRuntimeAccessSerializesWithReloadAndQuiesce` is flaky there: 2 of 6 runs fail, at the base and in the prototype alike.
  - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`, and `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity` (environmental).

**How this plan was checked:**
- **Prototype.** A full prototype was built task by task on a worktree at the base commit. After each task its Verify passed and `gofmt -l` was clean. The diffs and files below are that prototype's commits, not sketches.
- **Full suite.** The final tree passes `go test ./...` apart from the base failures above, and `go vet` and `go build ./...` are clean.
- **Mutations.** 27 mutations, each removing one behaviour these tests guard, all made a test fail:
  - Task 1: the length gate.
  - Task 2: the last plan row only; insertion at the write's time; carried-item padding; failed writes skipped.
  - Task 3: the marker opening a capped beat.
  - Task 4: beat inheritance in `isExpanded` and in rendering; Ctrl+G clearing choices; running rows ignoring expand-all; an expanded beat uncapping its rows.
  - Task 5: the diagnostic.
  - Task 6: the summary and the active path.
  - Task 8:
    - the last row's `└`;
    - the fallback subject not repeating the verb;
    - a collapsed edit as one line;
    - the plan row drawn;
    - the tag in the head, and the tag dropping last;
    - reasoning only when expanded;
    - the headless head count;
    - the bounded running tail;
    - blocks for a long plan;
    - the verb skipped on copy, and the detail column skipped on copy;
    - the full subject on copy.

  Two survivors during authoring led to tests: `TestSpineRowsCopyFromTheSubjectColumn` and the headless head assertion. One known survivor is left: the error colour on a failed exit, since tests do not check colours.
- **Usability.** The `test/usability` scenarios assert no tool-row text. They pass on the prototype, and Task 9 runs them again.
- **Replay.** The plan itself was checked. Its code blocks were extracted from this file and applied task by task to a fresh worktree at the base commit, and after every task the tree matched the prototype's commit exactly.

**Decisions this plan makes beyond the spec** (all now recorded in the spec):
1. **Plan payload.** `PlanUpdate` carries every item's status (`Status []string`, with `Done()` and `Total()`) rather than counts, because the stepper draws each item.
   - `todo.write` appends unfinished items the model left out. The plan pads to the length in the write's summary, as waiting items.
   - A failed or denied write is skipped.
2. **The wire kind lands in P-A.** It is `planUpdate` with a `plan` payload, because the projection gains the node here. The web skips unknown kinds. `WireStep.folded` goes with folding.
3. **Thinking.** It is `thought Ns` in the head meta. Expanding a beat shows its reasoning under the head and opens its rows; a row's own choice still wins.
4. **Headless beats.** A beat without narration gets a `● N calls` head, so its calls hang off something.
5. **Row details.**
   - A collapsed edit is one line (`+N −M`); its diff shows when expanded. Today a collapsed edit shows up to 12 diff lines per file.
   - The cap line is a `… N earlier calls` spine row.
   - A failure shows its *last* three output lines, as today (the spec said "first").
6. **Running calls.** A running call names its path or summarised argument. Its sandbox line and its last six output lines stay under it while it runs. The `→ #n` pointer waits for the box in P-C.
7. **Verb fallback.** Tools outside the verb table use their name's first segment, and the rest of the name when the arguments name nothing.
8. **Copy.** Dragging over a call row copies its subject: the connector, verb and result are gutter or meta. Browse-mode `y` still copies the result.
9. **Subagent cards.** In-place cards (no live-work service) render below the spine until P-B removes them.
10. **Config.** `[tui.transcript]` is no longer merged or saved, and the diagnostic sits at `tui.transcript`.
11. **Glyphs.** `●` is `glyph.Beat`, sharing `glyph.Brand`'s rune; Brand never appears in the transcript.
12. **`file.write`.** A running `file.write` names its path (Task 6, in `internal/agent`).

**Execution:**
- Run the tasks in order. Run each task's Verify and commit it before starting the next, as `p-a-renderer-foundation: task N — <title>`.
- Task 6 is parallel-safe with Tasks 1 to 5 and 7. Every other task is sequential.
- Never commit unrelated working-tree changes. The spec and this plan travel with the work (Task 9).

**Encoding:**
- New files are given in full.
- Edits to existing files are unified diffs against the state after the previous tasks, each with a short note. Apply them exactly, by hand or with `git apply`.
- Each task's Steps say what its diffs do and why, so a hunk that no longer applies can be redone by hand.

## Contracts

Declarations that more than one task uses, each created by the task named, verbatim as in that task's code.

**Task 2, `internal/viewmodel/plan.go` and `stack.go`.**

```go
const KindPlanUpdate Kind // appended after KindMarker; wire name "planUpdate"

type Node struct {
	// … existing fields …
	PlanUpdate *PlanUpdate // set for KindPlanUpdate
}

type PlanUpdate struct {
	Status     []string // "completed", "in_progress" or "pending", in list order
	Active     int      // 1-based position of the first in-progress item; 0 when none
	ActiveText string
	Cleared    bool
}

func (p *PlanUpdate) Total() int
func (p *PlanUpdate) Done() int
func PlanID(ev registry.AuditEvent) NodeID // {KindPlanUpdate, "plan:" + ToolKey(ev)}
```

**Task 2, `internal/viewmodel/wire.go`.**

```go
type WireNode struct {
	// … existing fields …
	Plan *WirePlan `json:"plan,omitempty"`
}

type WirePlan struct {
	Status     []string `json:"status,omitempty"`
	Done       int      `json:"done"`
	Total      int      `json:"total"`
	Active     int      `json:"active,omitempty"`
	ActiveText string   `json:"activeText,omitempty"`
	Cleared    bool     `json:"cleared,omitempty"`
}
```

**Task 4, `internal/app/tui/expand.go`, `model.go` and `steps.go`.**

```go
// Model fields, replacing density, nodeDensity and effDensity.
expandAll bool
expanded  map[viewmodel.NodeID]bool

func (m *Model) expandKey(id viewmodel.NodeID) viewmodel.NodeID // moved unchanged
func (m *Model) ownExpanded(id viewmodel.NodeID) (expanded, ok bool)
func (m *Model) expandedUnder(id viewmodel.NodeID, inherited bool) bool
func (m *Model) isExpanded(id viewmodel.NodeID) bool // own, else its beat's (rowBeat), else expandAll
func (m *Model) isToolExpanded(id viewmodel.NodeID, live bool) bool
func (m *Model) setExpanded(id viewmodel.NodeID, e bool)
func (m *Model) toggleExpanded(id viewmodel.NodeID)
func (m *Model) toggleExpandAll()
func (m *Model) resetExpansion()
func (m *Model) isLiveToolNode(id viewmodel.NodeID) bool // moved unchanged

// stepRenderCtx: density and record become
expanded func(id viewmodel.NodeID, inherited bool) bool
func (c *stepRenderCtx) open(id viewmodel.NodeID, inherited bool) bool

func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool) (string, []subRegion)
```

**Task 5, `internal/app/config/layers.go`.**

```go
type Layers struct {
	// … existing fields …
	TranscriptIgnoredBy []LayerID
}
```

**Task 7, `internal/app/tui/transcript.go`.**

```go
func callersDetail(callers []string, w int) []string
func toolBody(event registry.AuditEvent, expanded bool, w int) []string
```

## Task 1: Todo labels for lists of any length

**Goal:** Every todo list labels the beat where work moves to a different todo, whatever its length; the `MinTaskTodos` gate no longer applies to labels.

**Files:**

- `internal/viewmodel/beats_test.go`
- `internal/viewmodel/labels.go`
- `internal/viewmodel/labels_test.go`
- `internal/viewmodel/stack.go`
- `internal/viewmodel/tasks.go`

**Order:** Sequential: Tasks 2 and 3 edit `internal/viewmodel/stack.go` after it.

**Steps:**

1. In `labelTransitions` (`internal/viewmodel/labels.go`), drop the `wroteList` parameter and the early return on `!wroteList && !taskMode(todos)`. A todo no longer in the list still gets no label.
2. In `buildTurn` (`internal/viewmodel/stack.go`), delete `maxWritten` and its update in `finish`, and call `labelTransitions(out, s.Todos)`.
3. Delete `todoWriteLen` from `internal/viewmodel/tasks.go`; it fed only `maxWritten`. `MinTaskTodos` and `taskMode` stay: the band and the receipt still use them until P-B.
4. Tests (`labels_test.go`):
   - `TestNoLabelForDroppedTodoOrShortList` becomes `TestNoLabelForDroppedTodo` (the dropped-todo half only);
   - new `TestOneItemListLabelsItsFirstBeat`: a one-item list labels its first beat `1/1` and not the second beat on the same todo;
   - `TestTaskModeFollowsTheListsATurnWroteNotJustTheCurrentOne` becomes `TestLabelsDoNotDependOnListLength`: one label with a one-item or two-item current list, and one for a turn that only ever saw one item;
   - the comment and case name in `TestDrivenByTodosFollowsTheCodeBlockNotTheProse` stop saying one item is too short for a label.
   `beats_test.go` drops the third argument from its `labelTransitions` call.

**Code:**

**`internal/viewmodel/beats_test.go`.** The new `labelTransitions` signature.

```diff
diff --git a/internal/viewmodel/beats_test.go b/internal/viewmodel/beats_test.go
--- a/internal/viewmodel/beats_test.go
+++ b/internal/viewmodel/beats_test.go
@@ -175,7 +175,7 @@ func TestBeatEndsAtTodoChange(t *testing.T) {
 	if len(out) != 2 || out[1].Step.TodoID != "second" {
 		t.Fatalf("beats = %+v, want the second todo in its own beat", out)
 	}
-	labelTransitions(out, []db.TodoItem{{ID: "first", Content: "A"}, {ID: "second", Content: "B"}}, true)
+	labelTransitions(out, []db.TodoItem{{ID: "first", Content: "A"}, {ID: "second", Content: "B"}})
 	if out[1].Step.Todo == nil || out[1].Step.Todo.ID != "second" {
 		t.Fatalf("second todo has no divider: %+v", out[1].Step.Todo)
 	}
```

**`internal/viewmodel/labels.go`.** Drop the gate.

```diff
diff --git a/internal/viewmodel/labels.go b/internal/viewmodel/labels.go
--- a/internal/viewmodel/labels.go
+++ b/internal/viewmodel/labels.go
@@ -18,12 +18,9 @@ type TodoLabel struct {
 
 // labelTransitions sets Todo on each beat whose bound todo differs from the
 // previous bound beat's in this turn. "Previous" survives interruptions
-// (messages, cards, markers) and resets per turn. Lists shorter than
-// MinTaskTodos and todos no longer in the list get no label.
-func labelTransitions(nodes []*Node, todos []db.TodoItem, wroteList bool) {
-	if !wroteList && !taskMode(todos) {
-		return
-	}
+// (messages, cards, markers) and resets per turn. A list of any length labels
+// its transitions; a todo no longer in the list gets no label.
+func labelTransitions(nodes []*Node, todos []db.TodoItem) {
 	prev := ""
 	for _, n := range nodes {
 		if n.Kind != KindStep || n.Step == nil || n.Step.TodoID == "" {
```

**`internal/viewmodel/labels_test.go`.** The tests above.

```diff
diff --git a/internal/viewmodel/labels_test.go b/internal/viewmodel/labels_test.go
--- a/internal/viewmodel/labels_test.go
+++ b/internal/viewmodel/labels_test.go
@@ -83,22 +83,34 @@ func TestLabelOnEachTransition(t *testing.T) {
 	}
 }
 
-func TestNoLabelForDroppedTodoOrShortList(t *testing.T) {
+func TestNoLabelForDroppedTodo(t *testing.T) {
 	dropped := Build(Snapshot{
 		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Old work."), audit("file.read", 1, "a", 2)},
 		Steps: []session.Step{stepFor(1, "gone", 1, 2)},
 		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "pending"}, db.TodoItem{ID: "y", Content: "Y", Status: "pending"}),
 	})
-	short := Build(Snapshot{
-		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Only item."), audit("file.read", 1, "a", 2)},
-		Steps: []session.Step{stepFor(1, "x", 1, 2)},
-		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "in_progress"}),
-	})
 	if got := labelsOf(dropped); len(got) != 0 {
 		t.Fatalf("dropped todo labelled: %v", got)
 	}
-	if got := labelsOf(short); len(got) != 0 {
-		t.Fatalf("one-item list labelled: %v", got)
+}
+
+// A one-item list is a plan like any other: its beat carries the 1/1 tag,
+// once, however many beats work on it.
+func TestOneItemListLabelsItsFirstBeat(t *testing.T) {
+	nodes := Build(Snapshot{
+		Items: []session.TranscriptItem{
+			userMsg(1, 0),
+			narration(2, 1, 1, "Only item."), audit("file.read", 1, "a", 2),
+			narration(3, 2, 3, "Still on it."), audit("file.read", 2, "b", 4),
+		},
+		Steps: []session.Step{stepFor(1, "x", 1, 2), stepFor(2, "x", 3, 4)},
+		Todos: todos(db.TodoItem{ID: "x", Content: "X", Status: "in_progress"}),
+	})
+	if got := labelsOf(nodes); strings.Join(got, ",") != "step:1=x" {
+		t.Fatalf("labels = %v\n%s", got, shape(nodes))
+	}
+	if l := nodes[0].Children[1].Step.Todo; l.Index != 1 || l.Total != 1 {
+		t.Fatalf("label = %+v", l)
 	}
 }
 
@@ -156,9 +168,9 @@ func TestEarlierTurnsFoldEveryFinishedBeat(t *testing.T) {
 
 // DrivenByTodos is settled by the brief's code block, not its prose:
 // n.Step.Todo != nil || n.Step.TodoID != "", so any beat bound to a non-empty
-// TodoID drives the turn even when the todo is gone from the current list or
-// the list is a one-item one. This test pins that ruling so the divergence
-// from the prose cannot creep back in silently.
+// TodoID drives the turn even when the todo is gone from the current list.
+// This test pins that ruling so the divergence from the prose cannot creep
+// back in silently.
 func TestDrivenByTodosFollowsTheCodeBlockNotTheProse(t *testing.T) {
 	cases := []struct {
 		name string
@@ -176,7 +188,6 @@ func TestDrivenByTodosFollowsTheCodeBlockNotTheProse(t *testing.T) {
 			want: true,
 		},
 		{
-			// One item is too short for a label, but the binding still counts.
 			name: "one-item list",
 			snap: Snapshot{
 				Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Only item."), audit("file.read", 1, "a", 2)},
@@ -280,10 +291,10 @@ func TestNoReceiptWhileRunningOrWithoutSteps(t *testing.T) {
 	}
 }
 
-// taskMode gates the transition labels now that task rows are gone: a turn
-// counts as todo-driven from the longest list it wrote, not from the list as
-// it stands, so a later rewrite to one item must not unlabel finished work.
-func TestTaskModeFollowsTheListsATurnWroteNotJustTheCurrentOne(t *testing.T) {
+// Labels do not depend on how long the list is or was: rewriting the list
+// shorter or longer never unlabels finished work, and a turn that only ever
+// saw one item is labelled too.
+func TestLabelsDoNotDependOnListLength(t *testing.T) {
 	steps := []session.Step{stepFor(1, "t1", 1, 3)}
 	items := []session.TranscriptItem{
 		userMsg(1, 0),
@@ -292,19 +303,13 @@ func TestTaskModeFollowsTheListsATurnWroteNotJustTheCurrentOne(t *testing.T) {
 	}
 	one := todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"})
 	two := todos(db.TodoItem{ID: "t1", Content: "A", Status: "completed"}, db.TodoItem{ID: "t2", Content: "B", Status: "pending"})
-
-	// 2 -> 1: the list shrank after the turn wrote two items. The turn keeps
-	// its label instead of flattening.
-	if got := labelsOf(Build(Snapshot{Items: items, Steps: steps, Todos: one})); len(got) == 0 {
-		t.Fatal("rewriting the list to one item must not flatten a turn that worked from two")
-	}
-	// 1 -> 2: and it is the same with the longer list back.
-	if got := labelsOf(Build(Snapshot{Items: items, Steps: steps, Todos: two})); len(got) == 0 {
-		t.Fatal("two items drive the turn")
+	for name, list := range map[string][]db.TodoItem{"one": one, "two": two} {
+		if got := labelsOf(Build(Snapshot{Items: items, Steps: steps, Todos: list})); len(got) != 1 {
+			t.Fatalf("%s-item list: labels = %v", name, got)
+		}
 	}
-	// A turn that only ever saw one item stays flat whatever the list does later.
 	flat := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "a", 2)}
-	if got := labelsOf(Build(Snapshot{Items: flat, Steps: steps, Todos: one})); len(got) != 0 {
-		t.Fatalf("one item is not a plan: %v", got)
+	if got := labelsOf(Build(Snapshot{Items: flat, Steps: steps, Todos: one})); len(got) != 1 {
+		t.Fatalf("a turn that saw one item is labelled: %v", got)
 	}
 }
```

**`internal/viewmodel/stack.go`.** Drop `maxWritten`.

```diff
diff --git a/internal/viewmodel/stack.go b/internal/viewmodel/stack.go
--- a/internal/viewmodel/stack.go
+++ b/internal/viewmodel/stack.go
@@ -541,7 +541,6 @@ func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]sessi
 		}
 	}
 
-	maxWritten := 0 // the longest list any todo.write in this turn wrote
 	finish := func(id session.StepID, a *stepAcc, isLive bool, active []session.ActiveToolCall) *Node {
 		node := &Node{Kind: KindStep, Step: a.info, Live: isLive}
 		if a.loose {
@@ -555,7 +554,6 @@ func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]sessi
 		for _, ev := range a.audits {
 			if ev.ToolName == todoWriteTool {
 				a.info.TodoWrites = append(a.info.TodoWrites, ev)
-				maxWritten = max(maxWritten, todoWriteLen(ev))
 				continue
 			}
 			audits = append(audits, ev)
@@ -635,10 +633,7 @@ func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]sessi
 	}
 	out = mergeBeats(out)
 	out = coalesceMarkers(out)
-	// Whether the turn is task-driven is settled by the lists it wrote, not
-	// only by the list as it stands now: a later rewrite to one item must not
-	// flatten a finished turn after the fact.
-	labelTransitions(out, s.Todos, maxWritten >= MinTaskTodos)
+	labelTransitions(out, s.Todos)
 	foldBeats(out, lastTurn)
 	// foldBeats and labelTransitions run after finish/absorb computed each
 	// beat's version, so recompute it now that the payload is final.
```

**`internal/viewmodel/tasks.go`.** Delete `todoWriteLen`.

```diff
diff --git a/internal/viewmodel/tasks.go b/internal/viewmodel/tasks.go
--- a/internal/viewmodel/tasks.go
+++ b/internal/viewmodel/tasks.go
@@ -27,17 +27,6 @@ func todoListed(todos []db.TodoItem, id string) bool {
 	return false
 }
 
-// todoWriteLen is how many items a todo.write call wrote.
-func todoWriteLen(ev registry.AuditEvent) int {
-	var args struct {
-		Todos []db.TodoItem `json:"todos"`
-	}
-	if json.Unmarshal(ev.Args, &args) != nil {
-		return 0
-	}
-	return len(args.Todos)
-}
-
 func taskMode(todos []db.TodoItem) bool { return len(todos) >= MinTaskTodos }
 
 // ReceiptInfo is the one-line summary closing a finished turn.
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/viewmodel/   # ok
TMPDIR=/private/tmp/pa-suite go test ./internal/app/tui/ ./internal/acp/   # ok (tui: see the known failure under Assumptions)
```

Commit: `p-a-renderer-foundation: task 1 — Todo labels for lists of any length`.

## Task 2: Plan rows in the projection

**Goal:** A beat that wrote the todo list has one `KindPlanUpdate` child at its last write that changed the list, carrying the list as that write left it, and the wire projection carries it as kind `planUpdate`.

**Files:**

- `internal/viewmodel/plan.go` (new)
- `internal/viewmodel/plan_test.go` (new)
- `internal/viewmodel/stack.go`
- `internal/viewmodel/wire.go`
- `internal/viewmodel/wire_test.go`

**Order:** Sequential.

**Steps:**

1. New file `internal/viewmodel/plan.go` (below):
   - `PlanUpdate` with `Total()` and `Done()`; `PlanID`;
   - `planOf`: reads a `todo.write`'s arguments. A failed or denied write (`EventFailed`) and unparseable arguments give no plan. Statuses other than `completed` and `in_progress` read as `pending`. The tool keeps unfinished items the write left out and reports the whole length in its summary (`tasks updated · N items`), so the plan is padded with `pending` to that length. `Cleared` is an empty result;
   - `planRow`: the last write in a step that changed the list, and its time;
   - `insertPlanRow`: after every settled row whose first call started no later than the write; in-flight rows stay last;
   - `keepLastPlanRow`: a merged beat keeps only its last plan row.
2. `internal/viewmodel/stack.go`:
   - append `KindPlanUpdate` to the `Kind` constants (after `KindMarker`, so existing values do not move) and add `Node.PlanUpdate`;
   - in `finish`, after the job rows are settled and before the cards are appended, insert the step's plan row;
   - after `mergeBeats`, call `keepLastPlanRow` on every beat. The version pass that follows already recomputes each beat's version from its children.
   A narrationless step whose only call is a write is no longer empty, so it joins the beat before it (same actor, same todo) or heads its own.
3. `internal/viewmodel/wire.go`: `WireNode.Plan *WirePlan` (`json:"plan,omitempty"`), the `WirePlan` type, the kind name `planUpdate`, and a `wireNode` case. The web skips kinds it does not render (`TurnNode.svelte` falls through to its children), so it shows nothing for these until P-B.
4. Tests: new `plan_test.go`:
   - `TestPlanRowSitsAtItsWrite`: read, write, shell gives read, plan, shell, with the write's statuses and active item;
   - `TestBeatKeepsOnlyItsLastPlanRow`: two writes across a narrated step and its narrationless follow-up give one plan row, the second write's, and the follow-up joins the beat;
   - `TestPlanOfReadsTheWrite`: list order and first active item, carried items padded from the summary, unknown status, cleared list, carried items preventing a clear, and failed or unparseable writes;
   - `TestPlanRowSkipsAFailedLastWrite`;
   - `TestWireCarriesThePlanRow`: the exact JSON of `plan` and its parent.
   `TestKindStringCoversEveryKind` (`wire_test.go`) runs to `KindPlanUpdate`.

**Code:**

**New file `internal/viewmodel/plan.go`.** 

```go
package viewmodel

import (
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// PlanUpdate is a beat's plan line: the todo list as the beat's last
// todo.write left it. It is read from that write alone, never from the list
// as it stands now, so a plan line in history does not change when the list
// does.
type PlanUpdate struct {
	// Status is each item's status in list order: "completed",
	// "in_progress" or "pending". Unfinished items the write left out are
	// carried by the tool and appended here as "pending".
	Status []string
	// Active is the 1-based position of the first in-progress item, 0 when
	// none is in progress; ActiveText is that item's content.
	Active     int
	ActiveText string
	// Cleared is true when the write left the list empty.
	Cleared bool
}

// Total is how many items the list holds.
func (p *PlanUpdate) Total() int { return len(p.Status) }

// Done is how many items are completed.
func (p *PlanUpdate) Done() int {
	n := 0
	for _, s := range p.Status {
		if s == "completed" {
			n++
		}
	}
	return n
}

// planOf reads the list a successful todo.write left. ok is false for a
// failed or denied write, which changed nothing, and for arguments that do
// not parse.
func planOf(ev registry.AuditEvent) (*PlanUpdate, bool) {
	if EventFailed(ev) {
		return nil, false
	}
	var args struct {
		Todos []db.TodoItem `json:"todos"`
	}
	if json.Unmarshal(ev.Args, &args) != nil {
		return nil, false
	}
	p := &PlanUpdate{}
	for i, it := range args.Todos {
		st := it.Status
		if st != "completed" && st != "in_progress" {
			st = "pending"
		}
		p.Status = append(p.Status, st)
		if st == "in_progress" && p.Active == 0 {
			p.Active, p.ActiveText = i+1, it.Content
		}
	}
	// The tool keeps unfinished items the write left out and reports the
	// whole list's length in its summary ("tasks updated · 5 items"). Those
	// carried items have no status in the arguments; they are unfinished, so
	// they read as waiting.
	var total int
	if _, err := fmt.Sscanf(ev.ResultSummary, "tasks updated · %d items", &total); err == nil {
		for len(p.Status) < total {
			p.Status = append(p.Status, "pending")
		}
	}
	p.Cleared = len(p.Status) == 0
	return p, true
}

// PlanID is the identity of the plan row a todo.write produced.
func PlanID(ev registry.AuditEvent) NodeID {
	return NodeID{KindPlanUpdate, "plan:" + ToolKey(ev)}
}

// planRow is the plan row for a step's todo.writes, from the last one that
// changed the list, and that write's time. It is nil when none did.
func planRow(writes []registry.AuditEvent) (*Node, time.Time) {
	for i := len(writes) - 1; i >= 0; i-- {
		p, ok := planOf(writes[i])
		if !ok {
			continue
		}
		n := &Node{ID: PlanID(writes[i]), Kind: KindPlanUpdate, PlanUpdate: p}
		n.Version = versionOfPlan(p)
		return n, writes[i].Timestamp
	}
	return nil, time.Time{}
}

// insertPlanRow places a plan row among a step's rows at the time of the
// write it came from: after every settled row that started no later.
// In-flight rows always stay last.
func insertPlanRow(rows []*Node, plan *Node, at time.Time) []*Node {
	i := 0
	for i < len(rows) && len(rows[i].Tools) > 0 && !rows[i].Tools[0].Timestamp.After(at) {
		i++
	}
	rows = append(rows, nil)
	copy(rows[i+1:], rows[i:])
	rows[i] = plan
	return rows
}

// keepLastPlanRow drops every plan row of a beat but the last: several writes
// in one beat show only the state the beat ended with.
func keepLastPlanRow(beat *Node) {
	last := -1
	for i, c := range beat.Children {
		if c.Kind == KindPlanUpdate {
			last = i
		}
	}
	if last < 0 {
		return
	}
	kept := beat.Children[:0]
	for i, c := range beat.Children {
		if c.Kind == KindPlanUpdate && i != last {
			continue
		}
		kept = append(kept, c)
	}
	beat.Children = kept
}

func versionOfPlan(p *PlanUpdate) uint64 {
	b := newHash()
	b.f("plan|%v|%d|%s|%v|", p.Status, p.Active, p.ActiveText, p.Cleared)
	return b.sum()
}
```

**New file `internal/viewmodel/plan_test.go`.** 

```go
package viewmodel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func planWrite(step session.StepID, callID string, s int, body, summary string) session.TranscriptItem {
	it := todoWrite(step, callID, s, body)
	it.Audit.ResultSummary = summary
	return it
}

func planRows(beat *Node) []*Node {
	var out []*Node
	for _, c := range beat.Children {
		if c.Kind == KindPlanUpdate {
			out = append(out, c)
		}
	}
	return out
}

// The plan row sits where the write happened, among the beat's other calls,
// and carries the list the write left.
func TestPlanRowSitsAtItsWrite(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."),
			audit("file.read", 1, "r", 2),
			planWrite(1, "w", 3, `{"todos":[{"content":"A","status":"in_progress"},{"content":"B","status":"pending"}]}`, "tasks updated · 2 items"),
			audit("shell.run", 1, "s", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	beat := nodes[0].Children[1]
	want := "step:1\n  tool:1:r\n  plan:tool:1:w\n  tool:1:s\n"
	if got := shape([]*Node{beat}); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	p := beat.Children[1].PlanUpdate
	if !reflect.DeepEqual(p.Status, []string{"in_progress", "pending"}) || p.Active != 1 || p.ActiveText != "A" || p.Done() != 0 || p.Total() != 2 {
		t.Fatalf("plan = %+v", p)
	}
}

// Several writes in one beat show only the last: the state the beat ended
// with, at the position of that write. A narrationless follow-up whose only
// call is a write joins the beat rather than vanishing.
func TestBeatKeepsOnlyItsLastPlanRow(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Starting."),
			planWrite(1, "w1", 2, `{"todos":[{"content":"A","status":"in_progress"},{"content":"B","status":"pending"}]}`, ""),
			audit("file.read", 1, "r", 3),
			planWrite(2, "w2", 5, `{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"}]}`, ""),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 4, 6)},
	})
	beat := nodes[0].Children[1]
	want := "step:1\n  tool:1:r\n  plan:tool:2:w2\n"
	if got := shape([]*Node{beat}); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	if p := planRows(beat)[0].PlanUpdate; p.Done() != 1 || p.Active != 2 || p.ActiveText != "B" {
		t.Fatalf("plan = %+v", p)
	}
}

func TestPlanOfReadsTheWrite(t *testing.T) {
	ev := func(body, summary string) registry.AuditEvent {
		return registry.AuditEvent{ToolName: "todo.write", Args: json.RawMessage(body), ResultSummary: summary}
	}
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want *PlanUpdate
	}{
		{
			name: "statuses in list order, first in-progress item active",
			ev:   ev(`{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"},{"content":"C","status":"in_progress"}]}`, ""),
			want: &PlanUpdate{Status: []string{"completed", "in_progress", "in_progress"}, Active: 2, ActiveText: "B"},
		},
		{
			name: "carried items from the summary read as waiting",
			ev:   ev(`{"todos":[{"content":"A","status":"in_progress"}]}`, "tasks updated · 3 items"),
			want: &PlanUpdate{Status: []string{"in_progress", "pending", "pending"}, Active: 1, ActiveText: "A"},
		},
		{
			name: "an unknown status reads as waiting",
			ev:   ev(`{"todos":[{"content":"A","status":"blocked"}]}`, ""),
			want: &PlanUpdate{Status: []string{"pending"}},
		},
		{
			name: "an empty list is cleared",
			ev:   ev(`{"todos":[],"drop_unfinished":true}`, "tasks updated · 0 items"),
			want: &PlanUpdate{Cleared: true},
		},
		{
			name: "carried items keep a list from clearing",
			ev:   ev(`{"todos":[]}`, "tasks updated · 2 items"),
			want: &PlanUpdate{Status: []string{"pending", "pending"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := planOf(c.ev)
			if !ok || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("planOf = %+v, %v; want %+v", got, ok, c.want)
			}
		})
	}
	failed := ev(`{"todos":[{"content":"A","status":"pending"}]}`, "")
	failed.Error = "invalid todo status"
	if _, ok := planOf(failed); ok {
		t.Fatal("a failed write changed nothing and has no plan row")
	}
	if _, ok := planOf(ev(`not json`, "")); ok {
		t.Fatal("unparseable arguments have no plan row")
	}
}

// A failed last write falls back to the last write that changed the list.
func TestPlanRowSkipsAFailedLastWrite(t *testing.T) {
	bad := planWrite(1, "w2", 4, `{"todos":[{"content":"A","status":"nope"}]}`, "")
	bad.Audit.Error = "invalid todo status"
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."),
			planWrite(1, "w1", 2, `{"todos":[{"content":"A","status":"in_progress"}]}`, ""),
			bad,
		},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	rows := planRows(nodes[0].Children[1])
	if len(rows) != 1 || rows[0].ID.Key != "plan:tool:1:w1" {
		t.Fatalf("plan rows = %s", shape(rows))
	}
}

func TestWireCarriesThePlanRow(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."),
			planWrite(1, "w", 2, `{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"}]}`, ""),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	var got *WireNode
	tree := Project(nodes)
	for i := range tree.Nodes {
		if tree.Nodes[i].Kind == "planUpdate" {
			got = &tree.Nodes[i]
		}
	}
	if got == nil || got.Plan == nil {
		t.Fatalf("no plan node in %+v", tree.Nodes)
	}
	b, _ := json.Marshal(got.Plan)
	if want := `{"status":["completed","in_progress"],"done":1,"total":2,"active":2,"activeText":"B"}`; string(b) != want {
		t.Fatalf("plan = %s, want %s", b, want)
	}
	if !strings.HasPrefix(got.Parent, "step:") {
		t.Fatalf("plan parent = %q", got.Parent)
	}
}
```

**`internal/viewmodel/stack.go`.** The kind, the payload field, insertion and the per-beat pass.

```diff
diff --git a/internal/viewmodel/stack.go b/internal/viewmodel/stack.go
--- a/internal/viewmodel/stack.go
+++ b/internal/viewmodel/stack.go
@@ -36,6 +36,7 @@ const (
 	KindPassthrough
 	KindReceipt
 	KindMarker
+	KindPlanUpdate
 )
 
 // NodeID is a node's identity across rebuilds. The tree is rebuilt from the
@@ -64,13 +65,14 @@ type Node struct {
 	WorkKey string
 
 	// Payload: exactly one is set, by Kind.
-	Item    *session.TranscriptItem
-	Step    *StepInfo
-	Tools   []registry.AuditEvent // len > 1 = merged same-tool run within a step
-	Active  *session.ActiveToolCall
-	Receipt *ReceiptInfo
-	Job     *JobState
-	Marker  *MarkerInfo
+	Item       *session.TranscriptItem
+	Step       *StepInfo
+	Tools      []registry.AuditEvent // len > 1 = merged same-tool run within a step
+	Active     *session.ActiveToolCall
+	Receipt    *ReceiptInfo
+	Job        *JobState
+	Marker     *MarkerInfo
+	PlanUpdate *PlanUpdate
 }
 
 // StepInfo is a beat's payload. A beat is one narrated model response and
@@ -91,7 +93,8 @@ type StepInfo struct {
 	// binding, or for a narrated response whose only call was a todo.write,
 	// the todo that write set in progress. Empty means no task.
 	TodoID string
-	// TodoWrites are the beat's todo.write calls. They are never rows.
+	// TodoWrites are the beat's todo.write calls. They are not tool rows: the
+	// last one that changed the list is the beat's plan row (KindPlanUpdate).
 	TodoWrites []registry.AuditEvent
 
 	// Todo is set on the beat where work moves to a different todo.
@@ -564,6 +567,9 @@ func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]sessi
 		for _, r := range node.Children {
 			ci.settleJob(r)
 		}
+		if plan, at := planRow(a.info.TodoWrites); plan != nil {
+			node.Children = insertPlanRow(node.Children, plan, at)
+		}
 		node.Children = append(node.Children, a.cards...)
 		if id != 0 {
 			a.info.StepIDs = []session.StepID{id}
@@ -632,11 +638,17 @@ func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]sessi
 		out = append(out, b.node)
 	}
 	out = mergeBeats(out)
+	for _, n := range out {
+		if n.Kind == KindStep {
+			keepLastPlanRow(n)
+		}
+	}
 	out = coalesceMarkers(out)
 	labelTransitions(out, s.Todos)
 	foldBeats(out, lastTurn)
-	// foldBeats and labelTransitions run after finish/absorb computed each
-	// beat's version, so recompute it now that the payload is final.
+	// keepLastPlanRow, foldBeats and labelTransitions run after finish/absorb
+	// computed each beat's version, so recompute it now that the payload is
+	// final.
 	for _, n := range out {
 		if n.Kind == KindStep {
 			n.Version = versionOfStep(n)
```

**`internal/viewmodel/wire.go`.** The wire kind and payload.

```diff
diff --git a/internal/viewmodel/wire.go b/internal/viewmodel/wire.go
--- a/internal/viewmodel/wire.go
+++ b/internal/viewmodel/wire.go
@@ -43,6 +43,7 @@ type WireNode struct {
 	Subagent *WireSubagent `json:"subagent,omitempty"`
 	RunEvent *WireRunEvent `json:"runEvent,omitempty"`
 	Marker   *WireMarker   `json:"marker,omitempty"`
+	Plan     *WirePlan     `json:"plan,omitempty"`
 	Receipt  *WireReceipt  `json:"receipt,omitempty"`
 	Band     *WireBand     `json:"band,omitempty"`
 }
@@ -207,6 +208,19 @@ type WireMarker struct {
 	DurationMs int64    `json:"durationMs,omitempty"`
 }
 
+// WirePlan is a beat's plan line: the todo list as the beat's last
+// todo.write left it.
+type WirePlan struct {
+	// Status is each item's status in list order: "completed",
+	// "in_progress" or "pending".
+	Status     []string `json:"status,omitempty"`
+	Done       int      `json:"done"`
+	Total      int      `json:"total"`
+	Active     int      `json:"active,omitempty"`
+	ActiveText string   `json:"activeText,omitempty"`
+	Cleared    bool     `json:"cleared,omitempty"`
+}
+
 // WireCall is one finished call.
 type WireCall struct {
 	CallID     string   `json:"callId,omitempty"`
@@ -309,6 +323,7 @@ var kindNames = [...]string{
 	KindPassthrough: "passthrough",
 	KindReceipt:     "receipt",
 	KindMarker:      "marker",
+	KindPlanUpdate:  "planUpdate",
 }
 
 // String is the kind's wire name.
@@ -360,6 +375,10 @@ func wireNode(n *Node) WireNode {
 			wm.Refs = append(wm.Refs, r.Key)
 		}
 		w.Marker = wm
+	case n.PlanUpdate != nil:
+		p := n.PlanUpdate
+		w.Plan = &WirePlan{Status: p.Status, Done: p.Done(), Total: p.Total(), Active: p.Active,
+			ActiveText: ScrubText(p.ActiveText), Cleared: p.Cleared}
 	case n.Active != nil:
 		w.Tool = wireActive(n.Active)
 	case len(n.Tools) > 0:
```

**`internal/viewmodel/wire_test.go`.** Cover the new kind.

```diff
diff --git a/internal/viewmodel/wire_test.go b/internal/viewmodel/wire_test.go
--- a/internal/viewmodel/wire_test.go
+++ b/internal/viewmodel/wire_test.go
@@ -153,7 +153,7 @@ func TestWireStepCarriesTodoLabelAndFold(t *testing.T) {
 }
 
 func TestKindStringCoversEveryKind(t *testing.T) {
-	for k := KindTurn; k <= KindMarker; k++ {
+	for k := KindTurn; k <= KindPlanUpdate; k++ {
 		if s := k.String(); s == "" || s == "unknown" {
 			t.Fatalf("Kind(%d) has no wire name", k)
 		}
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/viewmodel/ ./internal/acp/   # ok
TMPDIR=/private/tmp/pa-suite go test ./internal/app/tui/   # ok apart from the known failure; the TUI draws nothing for the new kind yet
```

Commit: `p-a-renderer-foundation: task 2 — Plan rows in the projection`.

## Task 3: Remove beat folding

**Goal:** Nothing in history folds: `foldBeats`, `FoldKeep`, `StepInfo.Folded`, `WireStep.folded`, the TUI's fold toggle and its `z` key are gone.

**Files:**

- `internal/app/tui/beats_render_test.go`
- `internal/app/tui/browse.go`
- `internal/app/tui/browse_inspect.go`
- `internal/app/tui/browse_test.go`
- `internal/app/tui/click.go`
- `internal/app/tui/click_test.go`
- `internal/app/tui/density.go`
- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`
- `internal/app/tui/steps.go`
- `internal/app/tui/tasks_test.go`
- `internal/app/tui/viewport_render.go`
- `internal/viewmodel/labels.go`
- `internal/viewmodel/labels_test.go`
- `internal/viewmodel/stack.go`
- `internal/viewmodel/wire.go`
- `internal/viewmodel/wire_test.go`

**Order:** Sequential.

**Steps:**

1. `internal/viewmodel`: delete `FoldKeep` and `foldBeats` (`labels.go`), `StepInfo.Folded`, its call in `buildTurn` and its line in `versionOfStep` (`stack.go`), and `WireStep.Folded` with its assignment (`wire.go`). The web's `step.folded` checks become inert; P-B removes them.
2. `internal/app/tui`:
   - `model.go`: delete the `foldTasks` field and its initialisation, and update the `rowBeat` comment (a beat now hides a card only past its row cap);
   - `density.go`: `applyTranscriptConfig` no longer sets `foldTasks` (the file goes in Task 4);
   - `steps.go`: delete `stepRenderCtx.foldTasks`, `hasOverride` and the folded-beat check in `renderStep`;
   - `viewport_render.go`: delete both from the render context, drop the folded half of the tight-join rule (back-to-back narration still joins), and drop `foldTasks` from both signatures;
   - `browse.go`: delete the `z` case, and drop "a fold" from the vanished-node comment;
   - `browse_inspect.go`: delete the `z` help row;
   - `click.go`: the jump comment names only the row cap.
3. Tests:
   - delete `TestFoldKeepsNewestThreeAndRunningCards`, `TestEarlierTurnsFoldEveryFinishedBeat` (viewmodel), `TestFoldedBeatRendersAtOutline` and `TestZTogglesBeatFolding` (tui);
   - `TestWireStepCarriesTodoLabelAndFold` becomes `TestWireStepCarriesTodoLabel`;
   - `TestFinishedTasksFoldAndTheReceiptSummarisesTheTurn` becomes `TestFinishedTasksStayOpenAndTheReceiptSummarisesTheTurn` (six read rows);
   - `TestRunningTurnFoldsDoneTasksAndKeepsTheLiveOneOpen` becomes `TestRunningTurnKeepsEveryBeatOpen`;
   - `TestMarkerClickOpensTheBeatHidingItsCard` hides the card past `BeatRowCap` (eight edits after the job start; edits never merge) instead of in a folded beat;
   - `TestTranscriptConfigChangeReappliesToTheRunningSession` and the model and click tests lose their `foldTasks` lines.

**Code:**

**`internal/app/tui/beats_render_test.go`.** Delete `TestFoldedBeatRendersAtOutline`.

```diff
diff --git a/internal/app/tui/beats_render_test.go b/internal/app/tui/beats_render_test.go
--- a/internal/app/tui/beats_render_test.go
+++ b/internal/app/tui/beats_render_test.go
@@ -69,18 +69,3 @@ func TestRenderDividerOnLabelledBeat(t *testing.T) {
 		t.Fatalf("divider = %q", first)
 	}
 }
-
-func TestFoldedBeatRendersAtOutline(t *testing.T) {
-	n := beatNode("Read the parser.", 4)
-	n.Step.Folded = true
-	c := &stepRenderCtx{foldTasks: true, hasOverride: func(viewmodel.NodeID) bool { return false }}
-	out, _ := renderStep(n, c, 100, densitySteps)
-	plain := strings.TrimRight(ansi.Strip(out), "\n")
-	if strings.Count(plain, "\n") != 0 || !strings.Contains(plain, "4 tools") {
-		t.Fatalf("folded beat = %q", plain)
-	}
-	c.hasOverride = func(viewmodel.NodeID) bool { return true }
-	if out, _ := renderStep(n, c, 100, densitySteps); strings.Count(ansi.Strip(out), "\n") < 2 {
-		t.Fatalf("an override unfolds the beat")
-	}
-}
```

**`internal/app/tui/browse.go`.** Delete `z`.

```diff
diff --git a/internal/app/tui/browse.go b/internal/app/tui/browse.go
--- a/internal/app/tui/browse.go
+++ b/internal/app/tui/browse.go
@@ -76,7 +76,7 @@ func (m *Model) setBrowseItems(items []browseItem, tree map[viewmodel.NodeID]*vi
 		// out of view while the view moves on.
 		m.cursor = items[len(items)-1].id
 	case m.cursorIndex() < 0:
-		// The node is gone (a fold, a rewind): stay near where it was
+		// The node is gone (a rewind): stay near where it was
 		// rather than jumping to the end of the transcript.
 		m.cursor = items[max(0, min(prev, len(items)-1))].id
 	}
@@ -261,17 +261,6 @@ func (m *Model) handleBrowseKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool)
 			m.leaveBrowse()
 		}
 		return *m, nil, true
-	case "z":
-		m.foldTasks = !m.foldTasks
-		word := "off"
-		if m.foldTasks {
-			word = "on"
-		}
-		cmd := m.setFlash("Fold finished beats: " + word)
-		m.invalidateTranscript()
-		m.refreshViewport()
-		m.scrollToCursor()
-		return *m, cmd, true
 	case "?":
 		return m.openBrowseHelp()
 	default:
```

**`internal/app/tui/browse_inspect.go`.** Delete the `z` help row.

```diff
diff --git a/internal/app/tui/browse_inspect.go b/internal/app/tui/browse_inspect.go
--- a/internal/app/tui/browse_inspect.go
+++ b/internal/app/tui/browse_inspect.go
@@ -328,7 +328,6 @@ func (m *Model) openBrowseHelp() (tea.Model, tea.Cmd, bool) {
 			row("y", "copy its text (OSC 52)"),
 			row("o", "open its file in $EDITOR"),
 			row("f", "drill into the subagent"),
-			row("z", "fold / unfold finished tasks"),
 			{Header: "Leave"},
 			row("Esc", "back to the input"),
 			row("any other key", "back to the input, and type it"),
```

**`internal/app/tui/browse_test.go`.** Delete `TestZTogglesBeatFolding`; the config test loses `foldTasks`.

```diff
diff --git a/internal/app/tui/browse_test.go b/internal/app/tui/browse_test.go
--- a/internal/app/tui/browse_test.go
+++ b/internal/app/tui/browse_test.go
@@ -194,26 +194,6 @@ func TestEscLeavesBrowseAndFollowResumesOnlyAtBottom(t *testing.T) {
 	}
 }
 
-func TestZTogglesBeatFolding(t *testing.T) {
-	m := newTestModel(t)
-	m.resize(120, 60)
-	scriptedTasks(t, &m, 3, 2, true)
-	m.refreshViewport()
-	// One tool row per beat, and every beat folded but the newest
-	// viewmodel.FoldKeep: folding hides a beat's rows, never its narration.
-	if got := strings.Count(viewText(&m), "Read file"); got != viewmodel.FoldKeep {
-		t.Fatalf("Read file rendered %d times, want %d at rest:\n%s", got, viewmodel.FoldKeep, viewText(&m))
-	}
-	m = pressKeys(m, "esc", "z")
-	if got := strings.Count(viewText(&m), "Read file"); got != 6 {
-		t.Fatalf("z must unfold every finished beat, saw %d tool rows:\n%s", got, viewText(&m))
-	}
-	m = pressKeys(m, "z")
-	if got := strings.Count(viewText(&m), "Read file"); got != viewmodel.FoldKeep {
-		t.Fatalf("z again refolds them, saw %d tool rows:\n%s", got, viewText(&m))
-	}
-}
-
 func TestCursorMovesDoNotInvalidateTheRenderCache(t *testing.T) {
 	m := browseFixture(t)
 	m = pressKeys(m, "esc")
@@ -409,14 +389,12 @@ func TestOpenIgnoresDirectoriesAndPathsOutsideTheWorkspace(t *testing.T) {
 
 func TestTranscriptConfigChangeReappliesToTheRunningSession(t *testing.T) {
 	m := browseFixture(t)
-	m.foldTasks = false
 	m.density = densityFull
 	cfg := m.state.Config
 	cfg.TUI.Transcript.Density = "outline"
-	cfg.TUI.Transcript.FoldFinishedTasks = true
 	m.applyNewConfig(cfg)
-	if m.density != densityOutline || !m.foldTasks {
-		t.Fatalf("density %v foldTasks %v after the config changed", m.density, m.foldTasks)
+	if m.density != densityOutline {
+		t.Fatalf("density %v after the config changed", m.density)
 	}
 	// An unrelated reload leaves the user's Ctrl+G choice alone.
 	m.density = densityFull
@@ -492,7 +470,7 @@ func TestCursorStaysNearWhereItWasWhenItsNodeVanishes(t *testing.T) {
 	m := browseFixture(t)
 	m = pressKeys(m, "esc", "g", "j")
 	idx := m.cursorIndex()
-	m.cursor = viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:gone"} // as if it had folded away
+	m.cursor = viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:gone"} // as if it had been rewound away
 	m.invalidateTranscript()
 	m.browseItems = append([]browseItem(nil), m.browseItems...)
 	m.setBrowseItems(m.browseItems, m.browseTree)
```

**`internal/app/tui/click.go`.** Comment only.

```diff
diff --git a/internal/app/tui/click.go b/internal/app/tui/click.go
--- a/internal/app/tui/click.go
+++ b/internal/app/tui/click.go
@@ -212,9 +212,9 @@ func (m *Model) handleTranscriptClick(msg tea.Mouse) (tea.Cmd, bool) {
 	switch {
 	case target.jumpTo.Key != "":
 		// A marker is a pointer, not a row with state of its own: it scrolls
-		// to the card it reports on. A card with no region may sit in a
-		// beat that is folded or capped; opening the beat at full density
-		// shows every row, and the jump retries once.
+		// to the card it reports on. A card with no region may sit past its
+		// beat's row cap; opening the beat at full density shows every row,
+		// and the jump retries once.
 		r, ok := m.regionFor(target.jumpTo)
 		if beat, inBeat := m.rowBeat[target.jumpTo]; !ok && inBeat {
 			m.setOverride(beat, densityFull)
```

**`internal/app/tui/click_test.go`.** Hide the card past the row cap.

```diff
diff --git a/internal/app/tui/click_test.go b/internal/app/tui/click_test.go
--- a/internal/app/tui/click_test.go
+++ b/internal/app/tui/click_test.go
@@ -1,6 +1,7 @@
 package tui
 
 import (
+	"fmt"
 	"strings"
 	"testing"
 	"time"
@@ -16,8 +17,6 @@ import (
 
 func TestClickRegionsCoverThinkingAndAuditBlocks(t *testing.T) {
 	m := newTestModel(t)
-	// The reasoning belongs to the newest turn: an earlier turn's beat folds
-	// to an outline row, which hides the thinking row it contains.
 	m.state.AddMessage(session.RoleUser, "hi", session.ContentTypePlain)
 	ts1 := time.Now()
 	m.state.LogThinking(session.ThinkingEntry{Text: "why I did this", Duration: time.Second, StartedAt: ts1})
@@ -278,17 +277,23 @@ func TestNoNowBarNoBand(t *testing.T) {
 }
 
 // A completion marker in a later turn points at a job card inside an earlier
-// turn's folded beat. The card has no region while folded; clicking the
-// marker opens the beat and lands on the card rather than doing nothing.
+// turn's beat, past the beat's row cap. The card has no region while capped;
+// clicking the marker opens the beat and lands on the card rather than doing
+// nothing.
 func TestMarkerClickOpensTheBeatHidingItsCard(t *testing.T) {
 	m := newTestModel(t)
-	m.resize(100, 40)
-	m.foldTasks = true
+	m.resize(100, 60)
 	m.state.AddMessage(session.RoleUser, "run the gate", session.ContentTypePlain)
 	s1 := m.state.BeginStep(session.Actor{})
 	m.state.AddNarration(s1, "Starting the gate.")
 	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "shell.run", StepID: s1, ToolCallID: "j",
 		Args: []byte(`{"command":"pytest -q","background":true}`), ResultSummary: session.JobStartPrefix + "job-1"})
+	// Edits never merge into a run, so each is its own row and the card is
+	// pushed past the cap.
+	for i := 0; i < viewmodel.BeatRowCap; i++ {
+		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.edit", StepID: s1, ToolCallID: fmt.Sprintf("e%d", i),
+			Args: []byte(fmt.Sprintf(`{"path":"f%d.go"}`, i)), ResultSummary: "edited"})
+	}
 	m.state.EndStep(s1)
 	m.state.AddMessageFinal(session.RoleAssistant, "started", session.ContentTypePlain)
 	m.state.AddMessage(session.RoleUser, "anything else?", session.ContentTypePlain)
@@ -310,7 +315,7 @@ func TestMarkerClickOpensTheBeatHidingItsCard(t *testing.T) {
 		t.Fatal("no completion marker region")
 	}
 	if _, ok := m.regionFor(card); ok {
-		t.Fatal("precondition: the card's beat must be folded, leaving the card no region")
+		t.Fatal("precondition: the card must sit past its beat's row cap, leaving it no region")
 	}
 
 	top := m.scrollHintRows()
```

**`internal/app/tui/density.go`.** Stop reading `fold_finished_tasks`.

```diff
diff --git a/internal/app/tui/density.go b/internal/app/tui/density.go
--- a/internal/app/tui/density.go
+++ b/internal/app/tui/density.go
@@ -170,13 +170,12 @@ func (m *Model) recordDensity(id viewmodel.NodeID, d density) {
 	m.effDensity[m.expandKey(id)] = d
 }
 
-// applyTranscriptConfig resets the global detail level and the task-fold
-// toggle to the configured values, dropping per-node overrides. It runs for a
-// new session and when [tui.transcript] changes.
+// applyTranscriptConfig resets the global detail level to the configured
+// value, dropping per-node overrides. It runs for a new session and when
+// [tui.transcript] changes.
 func (m *Model) applyTranscriptConfig() {
 	tr := m.state.Config.TUI.Transcript
 	m.density = parseDensity(tr.Density)
-	m.foldTasks = tr.FoldFinishedTasks
 	m.nodeDensity = map[viewmodel.NodeID]density{}
 	m.invalidateTranscript()
 }
```

**`internal/app/tui/model.go`.** Delete `foldTasks`.

```diff
diff --git a/internal/app/tui/model.go b/internal/app/tui/model.go
--- a/internal/app/tui/model.go
+++ b/internal/app/tui/model.go
@@ -615,8 +615,6 @@ type Model struct {
 	// effDensity records the level each node was last drawn at, so a click
 	// cycles from what the user sees rather than from the global default.
 	effDensity map[viewmodel.NodeID]density
-	// foldTasks is the session toggle for folding finished tasks (z).
-	foldTasks bool
 	// Browse mode (Esc): a cursor over the rendered nodes. browseItems is
 	// rebuilt on every full refresh; browseTree indexes the nodes for copy,
 	// open and inspect.
@@ -813,8 +811,8 @@ type Model struct {
 	// viewString from the same plan the rows came from.
 	workHits map[string]workHit
 	// rowBeat maps each row of a beat to the beat, from the last build, so
-	// a completion marker can open the beat that hides its card (folded, or
-	// past the row cap) before jumping to it.
+	// a completion marker can open the beat that hides its card past the row
+	// cap before jumping to it.
 	rowBeat map[viewmodel.NodeID]viewmodel.NodeID
 	// sheetPanel is the open Ctrl+B session sheet, kept for the same reason.
 	sheetPanel *sessionsheet.Panel
@@ -1781,7 +1779,6 @@ func New(state *session.State, opts ...Option) Model {
 		now:              time.Now,
 		viewportFollow:   true,
 		density:          parseDensity(state.Config.TUI.Transcript.Density),
-		foldTasks:        state.Config.TUI.Transcript.FoldFinishedTasks,
 		discovered:       map[string][]schema.ModelInfo{},
 	}
 	for _, opt := range opts {
```

**`internal/app/tui/model_test.go`.** No fold toggle to switch off.

```diff
diff --git a/internal/app/tui/model_test.go b/internal/app/tui/model_test.go
--- a/internal/app/tui/model_test.go
+++ b/internal/app/tui/model_test.go
@@ -3187,10 +3187,6 @@ func TestCompletedToolCallsRemainInTranscriptLog(t *testing.T) {
 		CommandExitCode: &exitCode,
 	})
 
-	// A call logged before the first user turn joins the "pre" turn, whose
-	// beats are all finished and therefore fold to one outline row. Unfold
-	// them to read the rows this test is about.
-	m.foldTasks = false
 	m.invalidateTranscript()
 	m.refreshViewport()
 	view := stripANSI(m.View().Content)
```

**`internal/app/tui/steps.go`.** Delete the fold check.

```diff
diff --git a/internal/app/tui/steps.go b/internal/app/tui/steps.go
--- a/internal/app/tui/steps.go
+++ b/internal/app/tui/steps.go
@@ -42,10 +42,6 @@ type stepRenderCtx struct {
 	density func(id viewmodel.NodeID, inherited density) density
 	// record is told the level each node is drawn at.
 	record func(id viewmodel.NodeID, d density)
-	// foldTasks is the session fold toggle; hasOverride says a user override
-	// exists for a node (which unfolds it).
-	foldTasks   bool
-	hasOverride func(viewmodel.NodeID) bool
 	// liveExpanded is expanded for an in-flight call, which stays collapsed
 	// until clicked whatever the global default says.
 	liveExpanded func(viewmodel.NodeID) bool
@@ -100,11 +96,6 @@ func (c *stepRenderCtx) callersFor(id viewmodel.NodeID) []string {
 func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited density) (string, []subRegion) {
 	si := n.Step
 	rows := n.Children
-	// A folded beat draws at outline unless the user opened it; folding
-	// itself is decided in viewmodel, the z toggle only switches it off.
-	if si.Folded && c.foldTasks && (c.hasOverride == nil || !c.hasOverride(n.ID)) {
-		inherited = densityOutline
-	}
 	sd := c.level(n.ID, inherited)
 	var b strings.Builder
 	var subs []subRegion
```

**`internal/app/tui/tasks_test.go`.** History never folds.

```diff
diff --git a/internal/app/tui/tasks_test.go b/internal/app/tui/tasks_test.go
--- a/internal/app/tui/tasks_test.go
+++ b/internal/app/tui/tasks_test.go
@@ -54,7 +54,7 @@ func scriptedTasks(t *testing.T, m *Model, n, stepsPer int, finish bool) {
 
 func viewText(m *Model) string { return strings.Join(transcriptLines(m), "\n") }
 
-func TestFinishedTasksFoldAndTheReceiptSummarisesTheTurn(t *testing.T) {
+func TestFinishedTasksStayOpenAndTheReceiptSummarisesTheTurn(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(120, 60)
 	scriptedTasks(t, &m, 3, 2, true)
@@ -64,31 +64,25 @@ func TestFinishedTasksFoldAndTheReceiptSummarisesTheTurn(t *testing.T) {
 			t.Errorf("missing %q:\n%s", want, text)
 		}
 	}
-	// Six beats, all finished: all but the newest viewmodel.FoldKeep fold to
-	// outline rows, which keep the narration headline and its call count but
-	// hide the tool rows themselves.
-	if got := strings.Count(text, "Read file"); got != viewmodel.FoldKeep {
-		t.Errorf("Read file rendered %d times, want %d: a folded beat hides its tool rows:\n%s", got, viewmodel.FoldKeep, text)
+	// Six beats, all finished: nothing folds, so every beat keeps its row.
+	if got := strings.Count(text, "Read file"); got != 6 {
+		t.Errorf("Read file rendered %d times, want 6: history never folds:\n%s", got, text)
 	}
-	for _, want := range []string{"Working on task 1 part 1. Details follow.", "1 tool"} {
-		if !strings.Contains(text, want) {
-			t.Errorf("a folded beat still renders %q:\n%s", want, text)
-		}
+	if !strings.Contains(text, "Working on task 1 part 1. Details follow.") {
+		t.Errorf("the first beat keeps its narration:\n%s", text)
 	}
 }
 
-func TestRunningTurnFoldsDoneTasksAndKeepsTheLiveOneOpen(t *testing.T) {
+func TestRunningTurnKeepsEveryBeatOpen(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(120, 60)
 	scriptedTasks(t, &m, 3, 2, false)
 	text := viewText(&m)
-	if strings.Contains(text, "Working on task 1 part 1. Details follow.                                    1 tool") {
-		t.Errorf("want a folded outline row, not a full one:\n%s", text)
+	if got := strings.Count(text, "Read file"); got != 6 {
+		t.Errorf("Read file rendered %d times, want 6: done tasks keep their rows:\n%s", got, text)
 	}
-	// The turn's newest beats stay open, and they are the ones carrying the
-	// task-3 narration the model is working on.
 	if !strings.Contains(text, "Working on task 3 part 1") || !strings.Contains(text, "Working on task 3 part 2") {
-		t.Errorf("the newest beats of a running turn stay open:\n%s", text)
+		t.Errorf("the task the model is working on is shown:\n%s", text)
 	}
 	if strings.Contains(text, "done ·") {
 		t.Errorf("no receipt before the turn ends:\n%s", text)
@@ -108,8 +102,6 @@ func TestFailedBeatStaysOpen(t *testing.T) {
 	m.state.EndStep(id)
 	_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Make tests pass", Status: "completed", StartedAt: time.Now(), CompletedAt: time.Now()}})
 	text := viewText(&m)
-	// The failing beat is the newest of its turn, so it stays unfolded: a
-	// beat only folds once the model has moved past it.
 	if !strings.Contains(text, "Running the suite") || !strings.Contains(text, "exit 1") {
 		t.Fatalf("the failed beat must stay open:\n%s", text)
 	}
@@ -411,9 +403,7 @@ func TestTodoDividersOrderTheWorkAndTheBandCarriesProgress(t *testing.T) {
 		t.Fatalf("missing %q:\n%s", sub, text)
 		return -1
 	}
-	// Each todo's work sits under the divider naming it, in list order. Both
-	// beats are finished, but they are the newest of the turn, so they stay
-	// open; a beat only folds once the model has moved past it.
+	// Each todo's work sits under the divider naming it, in list order.
 	read, first, write, second := at("1/4 Read"), at("Reading it."), at("2/4 Write"), at("Writing it.")
 	if !(read < first && first < write && write < second) {
 		t.Fatalf("order must be divider, work, next divider, next work:\n%s", text)
```

**`internal/app/tui/viewport_render.go`.** Delete the fold plumbing and the folded tight join.

```diff
diff --git a/internal/app/tui/viewport_render.go b/internal/app/tui/viewport_render.go
--- a/internal/app/tui/viewport_render.go
+++ b/internal/app/tui/viewport_render.go
@@ -106,8 +106,6 @@ func (m *Model) refreshViewport() {
 	rctx := &stepRenderCtx{
 		density:       m.densityOf,
 		record:        m.recordDensity,
-		foldTasks:     m.foldTasks,
-		hasOverride:   func(id viewmodel.NodeID) bool { _, ok := m.override(id); return ok },
 		liveExpanded:  func(id viewmodel.NodeID) bool { return m.isToolExpanded(id, true) },
 		region:        m.regionView,
 		noteRows:      m.noteRegionRows,
@@ -125,9 +123,8 @@ func (m *Model) refreshViewport() {
 	}
 
 	blocks := make([]string, 0, len(items)+4)
-	// tight[i] joins block i to the one before it with no blank line: the
-	// folded rows of a todo stack read as one list, and back-to-back
-	// narration reads as one text.
+	// tight[i] joins block i to the one before it with no blank line:
+	// back-to-back narration reads as one text.
 	var tight []bool
 	nextTight := false
 	var regions []nodeRegion
@@ -177,20 +174,18 @@ func (m *Model) refreshViewport() {
 			firstTurn = false
 		}
 		turnFirst := true
-		prevFolded, prevNarrationOnly := false, false
+		prevNarrationOnly := false
 		for _, node := range turn.Children {
 			collectSeen(node, seen)
 			indexTree(node, tree)
 			out, subs := m.renderNode(node, rctx, width, themeSig)
 			if out != "" {
-				folded := node.Kind == viewmodel.KindStep && node.Step != nil && node.Step.Folded && rctx.foldTasks && !rctx.hasOverride(node.ID)
-				isTight := (prevFolded && folded) ||
-					(prevNarrationOnly && node.Kind == viewmodel.KindStep && (node.Step == nil || node.Step.Todo == nil))
+				isTight := prevNarrationOnly && node.Kind == viewmodel.KindStep && (node.Step == nil || node.Step.Todo == nil)
 				if isTight && len(blocks) > 0 {
 					lineCursor-- // no blank line before a tight block
 					nextTight = true
 				}
-				prevFolded, prevNarrationOnly = folded, narrationOnly(node)
+				prevNarrationOnly = narrationOnly(node)
 				bitems = collectBrowse(bitems, node, out, subs, lineCursor, turnFirst)
 				turnFirst = false
 			}
@@ -415,7 +410,7 @@ func (m *Model) foldNodeSig(h sigWriter, n *viewmodel.Node, c *stepRenderCtx) {
 	fmt.Fprintf(h, "%s|%v|%d|%d|%d|", n.ID.Key, hasOv, ov, m.regionOffset[n.ID], m.regionRows[n.ID])
 	fmt.Fprintf(h, "d%d|", m.density)
 	if n.Kind == viewmodel.KindStep {
-		fmt.Fprintf(h, "%s|%s|%v|", c.routeModel, c.routeProvider, m.foldTasks)
+		fmt.Fprintf(h, "%s|%s|", c.routeModel, c.routeProvider)
 	}
 	if lines, ok := m.callers[n.ID]; ok {
 		fmt.Fprintf(h, "c%q|", lines)
@@ -437,7 +432,7 @@ func (m *Model) foldNodeSig(h sigWriter, n *viewmodel.Node, c *stepRenderCtx) {
 // not.
 func (m *Model) contentSignature(turns []*viewmodel.Node, width int, themeSig uint64, queued []string, notice session.Notice, noticeUp bool, reconnect string, hasTurns bool) uint64 {
 	h := fnv.New64a()
-	fmt.Fprintf(h, "w%d|t%d|g%d|f%v|", width, themeSig, m.density, m.foldTasks)
+	fmt.Fprintf(h, "w%d|t%d|g%d|", width, themeSig, m.density)
 	fmt.Fprintf(h, "turns%v|", hasTurns)
 	c := &stepRenderCtx{}
 	if st, _ := m.transcriptSource(); st != nil {
```

**`internal/viewmodel/labels.go`.** Delete `FoldKeep` and `foldBeats`.

```diff
diff --git a/internal/viewmodel/labels.go b/internal/viewmodel/labels.go
--- a/internal/viewmodel/labels.go
+++ b/internal/viewmodel/labels.go
@@ -2,9 +2,6 @@ package viewmodel
 
 import "marshal/internal/db"
 
-// FoldKeep is how many of the live turn's newest finished beats stay open.
-const FoldKeep = 3
-
 // TodoLabel marks the beat where work moves to a different todo. It is shown
 // once per transition, never repeated for later beats of the same todo, so a
 // todo the model forgets to advance costs one line rather than one header
@@ -39,28 +36,6 @@ func labelTransitions(nodes []*Node, todos []db.TodoItem) {
 	}
 }
 
-// foldBeats marks finished beats folded. In the live (last) turn the newest
-// FoldKeep finished beats stay open; earlier turns fold every finished beat.
-// A beat that is live or holds anything live (a running subagent card, an
-// in-flight call) never folds.
-func foldBeats(nodes []*Node, lastTurn bool) {
-	keep := 0
-	if lastTurn {
-		keep = FoldKeep
-	}
-	for i := len(nodes) - 1; i >= 0; i-- {
-		n := nodes[i]
-		if n.Kind != KindStep || n.Step == nil || n.AnyLive() {
-			continue
-		}
-		if keep > 0 {
-			keep--
-			continue
-		}
-		n.Step.Folded = true
-	}
-}
-
 // DrivenByTodos reports whether a turn is working from the todo list, which
 // is when the band shows todo progress: some beat in it was labelled or
 // bound to a todo.
```

**`internal/viewmodel/labels_test.go`.** Delete the two fold tests.

```diff
diff --git a/internal/viewmodel/labels_test.go b/internal/viewmodel/labels_test.go
--- a/internal/viewmodel/labels_test.go
+++ b/internal/viewmodel/labels_test.go
@@ -129,43 +129,6 @@ func TestLabelResetsPerTurn(t *testing.T) {
 	}
 }
 
-func TestFoldKeepsNewestThreeAndRunningCards(t *testing.T) {
-	items := []session.TranscriptItem{userMsg(1, 0)}
-	var steps []session.Step
-	for i := 1; i <= 6; i++ {
-		id := session.StepID(i)
-		items = append(items, narration(int64(10+i), id, i*10, "Beat."), audit("file.read", id, "r", i*10+1))
-		steps = append(steps, stepOf(id, 1, i*10, i*10+2))
-	}
-	running := session.TranscriptItem{Timestamp: at(11), Kind: session.KindSubagent,
-		Subagent: &session.SubagentView{ID: 3, Label: "child", Status: session.SubagentRunning, StartedAt: at(11)}}
-	items = append(items, running)
-	nodes := Build(Snapshot{Items: items, Steps: steps, Busy: true, Now: at(100)})
-	var folded []string
-	for _, n := range nodes[0].Children {
-		if n.Step != nil && n.Step.Folded {
-			folded = append(folded, n.ID.Key)
-		}
-	}
-	// step:1 holds the running card; 4..6 are the newest three.
-	if strings.Join(folded, ",") != "step:2,step:3" {
-		t.Fatalf("folded = %v\n%s", folded, shape(nodes))
-	}
-}
-
-func TestEarlierTurnsFoldEveryFinishedBeat(t *testing.T) {
-	nodes := Build(Snapshot{
-		Items: []session.TranscriptItem{
-			userMsg(1, 0), narration(2, 1, 1, "Old."), audit("file.read", 1, "a", 2), final(3, 3),
-			userMsg(5, 10), narration(6, 2, 11, "New."), audit("file.read", 2, "b", 12),
-		},
-		Steps: []session.Step{stepOf(1, 1, 1, 2), stepOf(2, 5, 11, 12)},
-	})
-	if !nodes[0].Children[1].Step.Folded || nodes[1].Children[1].Step.Folded {
-		t.Fatalf("old turn folds, newest-3 of last turn do not:\n%s", shape(nodes))
-	}
-}
-
 // DrivenByTodos is settled by the brief's code block, not its prose:
 // n.Step.Todo != nil || n.Step.TodoID != "", so any beat bound to a non-empty
 // TodoID drives the turn even when the todo is gone from the current list.
```

**`internal/viewmodel/stack.go`.** Delete `Folded`, the call and the hash line.

```diff
diff --git a/internal/viewmodel/stack.go b/internal/viewmodel/stack.go
--- a/internal/viewmodel/stack.go
+++ b/internal/viewmodel/stack.go
@@ -99,9 +99,6 @@ type StepInfo struct {
 
 	// Todo is set on the beat where work moves to a different todo.
 	Todo *TodoLabel
-	// Folded is true for a finished beat the transcript collapses to one
-	// line (see foldBeats).
-	Folded bool
 }
 
 // Snapshot is everything Build reads.
@@ -645,10 +642,8 @@ func buildTurn(items []session.TranscriptItem, stepByID map[session.StepID]sessi
 	}
 	out = coalesceMarkers(out)
 	labelTransitions(out, s.Todos)
-	foldBeats(out, lastTurn)
-	// keepLastPlanRow, foldBeats and labelTransitions run after finish/absorb
-	// computed each beat's version, so recompute it now that the payload is
-	// final.
+	// keepLastPlanRow and labelTransitions run after finish/absorb computed
+	// each beat's version, so recompute it now that the payload is final.
 	for _, n := range out {
 		if n.Kind == KindStep {
 			n.Version = versionOfStep(n)
@@ -882,7 +877,6 @@ func versionOfStep(n *Node) uint64 {
 	if st.Todo != nil {
 		b.f("lbl|%s|%d|%d|%s|", st.Todo.ID, st.Todo.Index, st.Todo.Total, st.Todo.Content)
 	}
-	b.f("fold|%v|", st.Folded)
 	b.f("st|%d|%d|%s|%s|%s|%s|%d|%d|%d|%d|", r.ID, r.TurnMsgID, r.Actor.Role, r.Actor.Label, r.Actor.Model, r.Actor.Provider,
 		r.StartedAt.UnixNano(), r.EndedAt.UnixNano(), len(st.StepIDs), len(st.LiveThinking))
 	for _, id := range st.StepIDs {
```

**`internal/viewmodel/wire.go`.** Delete `WireStep.Folded`.

```diff
diff --git a/internal/viewmodel/wire.go b/internal/viewmodel/wire.go
--- a/internal/viewmodel/wire.go
+++ b/internal/viewmodel/wire.go
@@ -122,7 +122,6 @@ type WireStep struct {
 	Provider     string         `json:"provider,omitempty"`
 	TodoID       string         `json:"todoId,omitempty"`
 	Todo         *WireTodoLabel `json:"todo,omitempty"`
-	Folded       bool           `json:"folded,omitempty"`
 	StartedAt    int64          `json:"startedAt,omitempty"`
 	EndedAt      int64          `json:"endedAt,omitempty"`
 	Thoughts     []WireThought  `json:"thoughts,omitempty"`
@@ -433,7 +432,6 @@ func wireStep(n *Node) *WireStep {
 	for _, t := range si.Thinking {
 		ws.Thoughts = append(ws.Thoughts, WireThought{Text: ScrubBlock(t.Text), DurationMs: t.Duration.Milliseconds()})
 	}
-	ws.Folded = si.Folded
 	if l := si.Todo; l != nil {
 		ws.Todo = &WireTodoLabel{ID: l.ID, Index: l.Index, Total: l.Total, Content: ScrubText(l.Content)}
 	}
```

**`internal/viewmodel/wire_test.go`.** The label test no longer checks the fold.

```diff
diff --git a/internal/viewmodel/wire_test.go b/internal/viewmodel/wire_test.go
--- a/internal/viewmodel/wire_test.go
+++ b/internal/viewmodel/wire_test.go
@@ -132,7 +132,7 @@ func TestProjectCapsLargeText(t *testing.T) {
 	}
 }
 
-func TestWireStepCarriesTodoLabelAndFold(t *testing.T) {
+func TestWireStepCarriesTodoLabel(t *testing.T) {
 	nodes := Build(Snapshot{
 		Items: []session.TranscriptItem{
 			userMsg(1, 0), narration(2, 1, 1, "Old."), audit("file.read", 1, "a", 2), final(3, 3),
@@ -143,7 +143,7 @@ func TestWireStepCarriesTodoLabelAndFold(t *testing.T) {
 	})
 	for _, n := range Project(nodes).Nodes {
 		if n.ID == "step:1" {
-			if n.Step.Todo == nil || n.Step.Todo.Content != "Parser" || !n.Step.Folded {
+			if n.Step.Todo == nil || n.Step.Todo.Content != "Parser" {
 				t.Fatalf("step:1 = %+v", n.Step)
 			}
 			return
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/viewmodel/ ./internal/acp/ ./internal/app/tui/   # ok apart from the known failure
grep -rn "foldTasks\|Folded\|FoldKeep\|foldBeats" --include='*.go' internal   # no output
```

Commit: `p-a-renderer-foundation: task 3 — Remove beat folding`.

## Task 4: One level of detail: per-row expand and Ctrl+G expand-all

**Goal:** The density ladder is gone: a node draws collapsed or expanded, Enter or a click flips one node, expanding a beat opens every row in it without a choice of its own, and Ctrl+G toggles expand-all.

**Files:**

- `internal/app/tui/beats_render_test.go`
- `internal/app/tui/browse_inspect.go`
- `internal/app/tui/browse_test.go`
- `internal/app/tui/cards_render_test.go`
- `internal/app/tui/click.go`
- `internal/app/tui/commands_dispatch.go`
- `internal/app/tui/density.go` (deleted)
- `internal/app/tui/density_test.go` (deleted)
- `internal/app/tui/expand.go` (new)
- `internal/app/tui/expand_test.go`
- `internal/app/tui/keypress.go`
- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`
- `internal/app/tui/steps.go`
- `internal/app/tui/steps_test.go`
- `internal/app/tui/tasks_test.go`
- `internal/app/tui/viewport_render.go`

**Order:** Sequential.

**Steps:**

1. Delete `density.go` and `density_test.go`. New `expand.go` (below) holds the state's methods. `expandKey` and `isLiveToolNode` move into it unchanged.
2. `model.go`: `density`, `nodeDensity` and `effDensity` become `expandAll bool` and `expanded map[viewmodel.NodeID]bool`. Drop the density initialisation, and drop the `[tui.transcript]` reapply in `applyNewConfig` (nothing reads that table any more; Task 5 retires it).
3. `steps.go`:
   - `stepRenderCtx.density` and `record` become `expanded func(id, inherited bool) bool`, and `level` becomes `open`;
   - `renderStep` takes `inherited bool`. The beat's own state uncaps `BeatRowCap` and is what its thinking rows, tool rows, job cards and cards inherit;
   - delete every outline branch (the headless outline summary, the early return that hid rows, the `rd == densityOutline` skips, `headRow` and the outline card head);
   - `renderStepContinuation` loses its unused second parameter.
4. `viewport_render.go`:
   - the context passes `m.expandedUnder`;
   - `drawNode` passes `m.expandAll` to `renderStep`, and `m.expandedUnder(n.ID, m.expandAll)` for items;
   - `foldNodeSig` and `contentSignature` hash the node's own choice and `expandAll` instead of the density;
   - `rowBeats` also maps a beat's thinking rows to it, so a click on one resolves through its beat (Task 8 removes those rows and this mapping).
5. `keypress.go`: Ctrl+G calls `toggleExpandAll` and flashes "Expand all: on" or "off". `commands_dispatch.go`: a session swap calls `resetExpansion`. `click.go`: the jump opens the beat with `setExpanded(beat, true)`. `browse_inspect.go`: Enter's help says "expand / collapse".
6. Tests:
   - `expand_test.go` is rewritten: `TestIsExpandedFollowsExpandAllUntilToggled`, `TestCtrlGClearsPerNodeChoices`, `TestCtrlGClearsActiveToolChoices`, new `TestExpandingABeatOpensItsRows`, and `TestRefreshViewportUsesPerItemExpandForThinking` (unchanged);
   - `TestEnterCyclesTheNodesDensityAndOnlyThatNode` becomes `TestEnterTogglesTheNodeAndOnlyThatNode`;
   - `TestDensityMatrix` becomes `TestExpandMatrix` (collapsed and expanded);
   - the Ctrl+G test in `model_test.go` toggles on and off;
   - delete `TestOutlineShowsOneRowPerStep`, `TestRenderHeadlessBeatOutlineIsOneSummaryRow` and `TestTranscriptConfigChangeReappliesToTheRunningSession`;
   - every `renderStep(…, densitySteps)` passes `false`.

**Code:**

**`internal/app/tui/beats_render_test.go`.** No outline level.

```diff
diff --git a/internal/app/tui/beats_render_test.go b/internal/app/tui/beats_render_test.go
--- a/internal/app/tui/beats_render_test.go
+++ b/internal/app/tui/beats_render_test.go
@@ -28,7 +28,7 @@ func beatNode(narr string, calls int) *viewmodel.Node {
 }
 
 func TestRenderHeadlessBeatHasNoInventedHeader(t *testing.T) {
-	out, _ := renderStep(beatNode("", 2), &stepRenderCtx{}, 100, densitySteps)
+	out, _ := renderStep(beatNode("", 2), &stepRenderCtx{}, 100, false)
 	plain := ansi.Strip(out)
 	for _, bad := range []string{"inferred", "ran ", "working", "thinking"} {
 		if strings.Contains(plain, bad) {
@@ -42,7 +42,7 @@ func TestRenderHeadlessBeatHasNoInventedHeader(t *testing.T) {
 
 // Review focus 1: a long headless beat shows the last BeatRowCap rows.
 func TestRenderHeadlessBeatCapsRows(t *testing.T) {
-	out, _ := renderStep(beatNode("", viewmodel.BeatRowCap+5), &stepRenderCtx{}, 100, densitySteps)
+	out, _ := renderStep(beatNode("", viewmodel.BeatRowCap+5), &stepRenderCtx{}, 100, false)
 	plain := ansi.Strip(out)
 	if !strings.Contains(plain, "+5 earlier calls") {
 		t.Fatalf("want a collapsed-count line:\n%s", plain)
@@ -52,18 +52,10 @@ func TestRenderHeadlessBeatCapsRows(t *testing.T) {
 	}
 }
 
-func TestRenderHeadlessBeatOutlineIsOneSummaryRow(t *testing.T) {
-	out, _ := renderStep(beatNode("", 3), &stepRenderCtx{}, 100, densityOutline)
-	plain := strings.TrimRight(ansi.Strip(out), "\n")
-	if strings.Count(plain, "\n") != 0 || !strings.Contains(plain, "3 tools") {
-		t.Fatalf("outline headless beat = %q", plain)
-	}
-}
-
 func TestRenderDividerOnLabelledBeat(t *testing.T) {
 	n := beatNode("Parser first.", 1)
 	n.Step.Todo = &viewmodel.TodoLabel{ID: "p", Index: 3, Total: 9, Content: "AccessibleModal initialFocus"}
-	out, _ := renderStep(n, &stepRenderCtx{}, 80, densitySteps)
+	out, _ := renderStep(n, &stepRenderCtx{}, 80, false)
 	first := strings.SplitN(ansi.Strip(out), "\n", 2)[0]
 	if !strings.Contains(first, "3/9 AccessibleModal initialFocus") || !strings.HasPrefix(strings.TrimSpace(first), "──") {
 		t.Fatalf("divider = %q", first)
```

**`internal/app/tui/browse_inspect.go`.** Help text.

```diff
diff --git a/internal/app/tui/browse_inspect.go b/internal/app/tui/browse_inspect.go
--- a/internal/app/tui/browse_inspect.go
+++ b/internal/app/tui/browse_inspect.go
@@ -323,7 +323,7 @@ func (m *Model) openBrowseHelp() (tea.Model, tea.Cmd, bool) {
 			row("g   G", "first / last node"),
 			row("PgUp PgDn  ^U ^D", "scroll a page"),
 			{Header: "Act"},
-			row("Enter", "cycle detail (drills into a subagent card)"),
+			row("Enter", "expand / collapse (drills into a subagent card)"),
 			row("i", "inspect the step or tool call"),
 			row("y", "copy its text (OSC 52)"),
 			row("o", "open its file in $EDITOR"),
```

**`internal/app/tui/browse_test.go`.** Enter toggles; the config test goes.

```diff
diff --git a/internal/app/tui/browse_test.go b/internal/app/tui/browse_test.go
--- a/internal/app/tui/browse_test.go
+++ b/internal/app/tui/browse_test.go
@@ -138,17 +138,21 @@ func TestBrowseMovementKeys(t *testing.T) {
 	}
 }
 
-func TestEnterCyclesTheNodesDensityAndOnlyThatNode(t *testing.T) {
+func TestEnterTogglesTheNodeAndOnlyThatNode(t *testing.T) {
 	m := browseFixture(t)
 	m = pressKeys(m, "esc")
 	step := m.cursor
-	before := m.shownDensity(step)
+	before := m.isExpanded(step)
 	m = pressKeys(m, "enter")
-	if got := m.shownDensity(step); got != before.nextOverride() {
-		t.Fatalf("Enter: %v -> %v, want %v", before, got, before.nextOverride())
+	if got := m.isExpanded(step); got == before {
+		t.Fatalf("Enter left the node at expanded=%v", got)
 	}
-	if len(m.nodeDensity) != 1 {
-		t.Fatalf("only the cursor node gets an override, have %v", m.nodeDensity)
+	if len(m.expanded) != 1 {
+		t.Fatalf("only the cursor node gets a choice, have %v", m.expanded)
+	}
+	m = pressKeys(m, "enter")
+	if got := m.isExpanded(step); got != before {
+		t.Fatalf("Enter again restores expanded=%v, got %v", before, got)
 	}
 }
 
@@ -387,23 +391,6 @@ func TestOpenIgnoresDirectoriesAndPathsOutsideTheWorkspace(t *testing.T) {
 	}
 }
 
-func TestTranscriptConfigChangeReappliesToTheRunningSession(t *testing.T) {
-	m := browseFixture(t)
-	m.density = densityFull
-	cfg := m.state.Config
-	cfg.TUI.Transcript.Density = "outline"
-	m.applyNewConfig(cfg)
-	if m.density != densityOutline {
-		t.Fatalf("density %v after the config changed", m.density)
-	}
-	// An unrelated reload leaves the user's Ctrl+G choice alone.
-	m.density = densityFull
-	m.applyNewConfig(cfg)
-	if m.density != densityFull {
-		t.Fatal("a reload that did not touch [tui.transcript] must not reset the density")
-	}
-}
-
 func TestTasksPanelStepCountsMatchTheTaskHeaders(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(120, 60)
```

**`internal/app/tui/cards_render_test.go`.** `renderStep` takes a bool.

```diff
diff --git a/internal/app/tui/cards_render_test.go b/internal/app/tui/cards_render_test.go
--- a/internal/app/tui/cards_render_test.go
+++ b/internal/app/tui/cards_render_test.go
@@ -160,7 +160,7 @@ func TestShellRowElidesTheWorktree(t *testing.T) {
 			Step: &viewmodel.StepInfo{Step: session.Step{ID: 1}}, Children: []*viewmodel.Node{row}}
 	}
 
-	out, _ := renderStep(step(), &stepRenderCtx{workdir: root}, 100, densitySteps)
+	out, _ := renderStep(step(), &stepRenderCtx{workdir: root}, 100, false)
 	plain := ansi.Strip(out)
 	if !strings.Contains(plain, "⌂ pytest -q") {
 		t.Fatalf("row = %q", plain)
@@ -169,7 +169,7 @@ func TestShellRowElidesTheWorktree(t *testing.T) {
 		t.Fatalf("the worktree path should be gone: %q", plain)
 	}
 	// With no workdir known, the command stays exactly as the model wrote it.
-	out, _ = renderStep(step(), &stepRenderCtx{}, 100, densitySteps)
+	out, _ = renderStep(step(), &stepRenderCtx{}, 100, false)
 	if got := ansi.Strip(out); !strings.Contains(got, "cd "+root) {
 		t.Fatalf("row = %q", got)
 	}
```

**`internal/app/tui/click.go`.** Open the beat with `setExpanded`.

```diff
diff --git a/internal/app/tui/click.go b/internal/app/tui/click.go
--- a/internal/app/tui/click.go
+++ b/internal/app/tui/click.go
@@ -213,11 +213,11 @@ func (m *Model) handleTranscriptClick(msg tea.Mouse) (tea.Cmd, bool) {
 	case target.jumpTo.Key != "":
 		// A marker is a pointer, not a row with state of its own: it scrolls
 		// to the card it reports on. A card with no region may sit past its
-		// beat's row cap; opening the beat at full density shows every row,
-		// and the jump retries once.
+		// beat's row cap; expanding the beat shows every row, and the jump
+		// retries once.
 		r, ok := m.regionFor(target.jumpTo)
 		if beat, inBeat := m.rowBeat[target.jumpTo]; !ok && inBeat {
-			m.setOverride(beat, densityFull)
+			m.setExpanded(beat, true)
 			m.invalidateTranscript()
 			m.refreshViewport()
 			r, ok = m.regionFor(target.jumpTo)
```

**`internal/app/tui/commands_dispatch.go`.** Reset on a session swap.

```diff
diff --git a/internal/app/tui/commands_dispatch.go b/internal/app/tui/commands_dispatch.go
--- a/internal/app/tui/commands_dispatch.go
+++ b/internal/app/tui/commands_dispatch.go
@@ -18,7 +18,6 @@ import (
 	"marshal/internal/app/tui/trustpanel"
 	"marshal/internal/interaction"
 	"marshal/internal/pipeline"
-	"marshal/internal/viewmodel"
 	"marshal/internal/worktree"
 )
 
@@ -221,11 +220,8 @@ func startNewSession(m *Model, name string) tea.Cmd {
 	}
 
 	// Drop per-message UI state that belongs to the old session.
-	m.nodeDensity = make(map[viewmodel.NodeID]density)
-	m.effDensity = nil
 	m.viewStack = nil
-	m.invalidateTranscript()
-	m.applyTranscriptConfig()
+	m.resetExpansion()
 	m.nodeRegions = nil
 	// The old session's changed-files list must never render in the new
 	// session while the sheetBaseRefMsg round-trips; re-read it below.
```

**Delete `internal/app/tui/density.go`.** Replaced by `expand.go`.

**Delete `internal/app/tui/density_test.go`.** Its helper is now `Model.isExpanded`.

**New file `internal/app/tui/expand.go`.** 

```go
package tui

import (
	"fmt"

	"marshal/internal/viewmodel"
)

// The transcript has one level of detail. Every row draws collapsed until it
// is expanded: one node at a time with Enter or a click, or every node with
// Ctrl+G. A node without a choice of its own follows its beat's, and a beat
// without one follows Ctrl+G, so expanding a beat opens everything in it.

// expandKey scopes a node ID to the transcript it is shown in. Every session
// numbers its own steps from 1, so without the scope, expanding step 3 of the
// main transcript would also expand step 3 of a subagent's, and toggling it
// there would flip the main one.
func (m *Model) expandKey(id viewmodel.NodeID) viewmodel.NodeID {
	if v, ok := m.drilledInto(); ok {
		return viewmodel.NodeID{Kind: id.Kind, Key: fmt.Sprintf("sub%d/%s", v.ID, id.Key)}
	}
	return id
}

// ownExpanded returns the node's own choice, if the user made one.
func (m *Model) ownExpanded(id viewmodel.NodeID) (expanded, ok bool) {
	expanded, ok = m.expanded[m.expandKey(id)]
	return expanded, ok
}

// expandedUnder resolves whether a node draws expanded: its own choice, else
// what it inherits from its beat or from Ctrl+G.
func (m *Model) expandedUnder(id viewmodel.NodeID, inherited bool) bool {
	if e, ok := m.ownExpanded(id); ok {
		return e
	}
	return inherited
}

// isExpanded reports whether a node draws expanded: its own choice, else its
// beat's, else Ctrl+G's.
func (m *Model) isExpanded(id viewmodel.NodeID) bool {
	inherited := m.expandAll
	if beat, ok := m.rowBeat[id]; ok {
		inherited = m.expandedUnder(beat, inherited)
	}
	return m.expandedUnder(id, inherited)
}

// isToolExpanded reports whether a tool row is shown expanded. A running call
// stays collapsed unless it was opened itself, whatever its beat or Ctrl+G
// says: its output is a live tail, not history. The choice carries over when
// the call settles, because a call's node ID is the same before and after.
func (m *Model) isToolExpanded(id viewmodel.NodeID, live bool) bool {
	if live {
		e, ok := m.ownExpanded(id)
		return ok && e
	}
	return m.isExpanded(id)
}

func (m *Model) setExpanded(id viewmodel.NodeID, e bool) {
	if m.expanded == nil {
		m.expanded = map[viewmodel.NodeID]bool{}
	}
	m.expanded[m.expandKey(id)] = e
}

// toggleExpanded is the click and Enter action: it flips what the node shows
// now.
func (m *Model) toggleExpanded(id viewmodel.NodeID) {
	if m.isLiveToolNode(id) {
		m.setExpanded(id, !m.isToolExpanded(id, true))
		return
	}
	m.setExpanded(id, !m.isExpanded(id))
}

// toggleExpandAll is Ctrl+G. It drops every per-node choice, so the whole
// transcript reads one way.
func (m *Model) toggleExpandAll() {
	m.expandAll = !m.expandAll
	m.expanded = map[viewmodel.NodeID]bool{}
}

// resetExpansion collapses everything and forgets every choice, for a new
// session.
func (m *Model) resetExpansion() {
	m.expandAll = false
	m.expanded = map[viewmodel.NodeID]bool{}
	m.invalidateTranscript()
}

func (m *Model) isLiveToolNode(id viewmodel.NodeID) bool {
	if id.Kind != viewmodel.KindTool {
		return false
	}
	st, _ := m.transcriptSource()
	if st == nil {
		return false
	}
	for _, a := range st.ActiveToolCalls() {
		if viewmodel.ActiveToolID(a) == id {
			return true
		}
	}
	return false
}
```

**`internal/app/tui/expand_test.go`.** Rewritten for the expand model.

```diff
diff --git a/internal/app/tui/expand_test.go b/internal/app/tui/expand_test.go
--- a/internal/app/tui/expand_test.go
+++ b/internal/app/tui/expand_test.go
@@ -8,39 +8,40 @@ import (
 	tea "charm.land/bubbletea/v2"
 
 	"marshal/internal/app/session"
+	"marshal/internal/tools/registry"
 	"marshal/internal/viewmodel"
 )
 
-func TestIsExpandedFollowsGlobalDefaultUntilOverridden(t *testing.T) {
+func TestIsExpandedFollowsExpandAllUntilToggled(t *testing.T) {
 	m := newTestModel(t)
 	key := thinkID(time.Unix(100, 0))
 
 	if m.isExpanded(key) {
-		t.Fatal("expected collapsed by default (density starts at steps)")
+		t.Fatal("expected collapsed by default")
 	}
 
-	m.density = densityFull
+	m.expandAll = true
 	if !m.isExpanded(key) {
-		t.Fatal("expected expanded once the global default flips")
+		t.Fatal("expected expanded once expand-all is on")
 	}
 
 	m.toggleExpanded(key)
 	if m.isExpanded(key) {
-		t.Fatal("expected the per-item override to win over the global default")
+		t.Fatal("expected the node's own choice to win over expand-all")
 	}
 
-	m.density = densitySteps
+	m.expandAll = false
 	if m.isExpanded(key) {
-		t.Fatal("expected the per-item override (still false) to persist")
+		t.Fatal("expected the node's own choice (still collapsed) to persist")
 	}
 }
 
-func TestCtrlGClearsPerItemOverrides(t *testing.T) {
+func TestCtrlGClearsPerNodeChoices(t *testing.T) {
 	m := newTestModel(t)
 	key := thinkID(time.Unix(100, 0))
-	m.toggleExpanded(key) // override to true (default false -> true)
+	m.toggleExpanded(key) // collapsed -> expanded
 	if !m.isExpanded(key) {
-		t.Fatal("precondition: override should read expanded")
+		t.Fatal("precondition: the choice should read expanded")
 	}
 
 	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
@@ -49,24 +50,24 @@ func TestCtrlGClearsPerItemOverrides(t *testing.T) {
 	}
 	mm := asModel(t, updated)
 
-	// the global level moved to full, and the override was cleared, so the
-	// item now simply follows the (new) global default.
-	if !mm.isExpanded(key) {
-		t.Fatal("expected item to follow the flipped global default")
+	// Expand-all turned on and the choice was cleared, so the node simply
+	// follows expand-all.
+	if !mm.expandAll || !mm.isExpanded(key) {
+		t.Fatal("expected the node to follow expand-all")
 	}
-	if len(mm.nodeDensity) != 0 {
-		t.Fatalf("nodeDensity = %v, want cleared", mm.nodeDensity)
+	if len(mm.expanded) != 0 {
+		t.Fatalf("expanded = %v, want cleared", mm.expanded)
 	}
 }
 
-func TestCtrlGClearsActiveToolOverrides(t *testing.T) {
+func TestCtrlGClearsActiveToolChoices(t *testing.T) {
 	m := newTestModel(t)
 	keyA := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:a"}
 	keyB := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:b"}
-	m.toggleExpanded(keyA)
-	m.toggleExpanded(keyB)
+	m.setExpanded(keyA, true)
+	m.setExpanded(keyB, true)
 	if !m.isToolExpanded(keyA, true) || !m.isToolExpanded(keyB, true) {
-		t.Fatal("precondition: both overrides should be set")
+		t.Fatal("precondition: both choices should be set")
 	}
 
 	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
@@ -75,12 +76,44 @@ func TestCtrlGClearsActiveToolOverrides(t *testing.T) {
 	}
 	mm := asModel(t, updated)
 
-	if len(mm.nodeDensity) != 0 {
-		t.Fatalf("nodeDensity = %v, want cleared", mm.nodeDensity)
+	if len(mm.expanded) != 0 {
+		t.Fatalf("expanded = %v, want cleared", mm.expanded)
 	}
-	// A running call ignores the global default: it stays collapsed.
+	// A running call ignores expand-all: it stays collapsed.
 	if mm.isToolExpanded(keyA, true) || mm.isToolExpanded(keyB, true) {
-		t.Fatal("expected all active-tool overrides to be cleared")
+		t.Fatal("expected all active-tool choices to be cleared")
+	}
+}
+
+// Expanding a beat opens every row in it that has no choice of its own; a
+// row's own choice still wins.
+func TestExpandingABeatOpensItsRows(t *testing.T) {
+	m := newTestModel(t)
+	m.resize(120, 60)
+	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
+	id := m.state.BeginStep(session.Actor{})
+	m.state.AddNarration(id, "Reading two files.")
+	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "a",
+		Args: []byte(`{"path":"a.go"}`), ResultSummary: "1 line", ResultContent: "alpha body"})
+	m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "git.status", StepID: id, ToolCallID: "b",
+		ResultSummary: "clean", ResultContent: "beta body"})
+	m.state.EndStep(id)
+	m.refreshViewport()
+	if text := viewText(&m); strings.Contains(text, "alpha body") || strings.Contains(text, "beta body") {
+		t.Fatalf("rows start collapsed:\n%s", text)
+	}
+
+	beat := viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:1"}
+	m.toggleExpanded(beat)
+	m.invalidateTranscript()
+	if text := viewText(&m); !strings.Contains(text, "alpha body") || !strings.Contains(text, "beta body") {
+		t.Fatalf("an expanded beat opens its rows:\n%s", text)
+	}
+
+	m.toggleExpanded(viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "tool:1:a"})
+	m.invalidateTranscript()
+	if text := viewText(&m); strings.Contains(text, "alpha body") || !strings.Contains(text, "beta body") {
+		t.Fatalf("collapsing one row leaves the others open:\n%s", text)
 	}
 }
 
```

**`internal/app/tui/keypress.go`.** Ctrl+G toggles expand-all.

```diff
diff --git a/internal/app/tui/keypress.go b/internal/app/tui/keypress.go
--- a/internal/app/tui/keypress.go
+++ b/internal/app/tui/keypress.go
@@ -12,7 +12,6 @@ import (
 	"marshal/internal/app/tui/doctorpanel"
 	"marshal/internal/app/tui/memory"
 	"marshal/internal/tools/registry"
-	"marshal/internal/viewmodel"
 )
 
 // handleKeypress routes the global hotkeys and the Enter-submit flow.
@@ -213,9 +212,12 @@ func (m *Model) handleKeypress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
 		m.refreshViewport()
 		return *m, nil, true
 	case "ctrl+g":
-		m.density = m.density.nextGlobal()
-		m.nodeDensity = map[viewmodel.NodeID]density{}
-		cmd := m.setFlash("Detail: " + m.density.String())
+		m.toggleExpandAll()
+		word := "off"
+		if m.expandAll {
+			word = "on"
+		}
+		cmd := m.setFlash("Expand all: " + word)
 		m.invalidateTranscript()
 		m.refreshViewport()
 		return *m, cmd, true
```

**`internal/app/tui/model.go`.** The expand state.

```diff
diff --git a/internal/app/tui/model.go b/internal/app/tui/model.go
--- a/internal/app/tui/model.go
+++ b/internal/app/tui/model.go
@@ -607,14 +607,11 @@ type Model struct {
 
 	// Viewport dirty tracking.
 	lastTranscriptHash uint64
-	// density is the global detail level (Ctrl+G); nodeDensity holds per-node
-	// overrides set by Enter or a click, cleared whenever Ctrl+G moves the
-	// global level. A node without one inherits its parent's level.
-	density     density
-	nodeDensity map[viewmodel.NodeID]density
-	// effDensity records the level each node was last drawn at, so a click
-	// cycles from what the user sees rather than from the global default.
-	effDensity map[viewmodel.NodeID]density
+	// expandAll is Ctrl+G: every node draws expanded. expanded holds the
+	// per-node choices made with Enter or a click, cleared whenever Ctrl+G
+	// flips. A row without one follows its beat, and a beat follows expandAll.
+	expandAll bool
+	expanded  map[viewmodel.NodeID]bool
 	// Browse mode (Esc): a cursor over the rendered nodes. browseItems is
 	// rebuilt on every full refresh; browseTree indexes the nodes for copy,
 	// open and inspect.
@@ -1304,16 +1301,11 @@ func relPath(workingDir, path string) string {
 // status line (the policy engine is rebuilt from the same value by the
 // runtime reload).
 func (m *Model) applyNewConfig(cfg config.Config) {
-	prevTranscript := m.state.Config.TUI.Transcript
 	// Rebase rather than assign: the session owns the mode override, so an
 	// unrelated reload must not silently revert a manual selection or an
 	// approved grant. RebaseInteractionConfig installs the new config with the
 	// remaining override applied and preserves the system-access flag.
 	m.state.RebaseInteractionConfig(cfg)
-	if cfg.TUI.Transcript != prevTranscript {
-		// An edit to [tui.transcript] takes effect in the running session.
-		m.applyTranscriptConfig()
-	}
 	// Read the effective mode back from the session: it may differ from the
 	// configured value when an override survived the rebase.
 	m.setReg = nil
@@ -1778,7 +1770,6 @@ func New(state *session.State, opts ...Option) Model {
 		spinner:          NewSpinner(),
 		now:              time.Now,
 		viewportFollow:   true,
-		density:          parseDensity(state.Config.TUI.Transcript.Density),
 		discovered:       map[string][]schema.ModelInfo{},
 	}
 	for _, opt := range opts {
```

**`internal/app/tui/model_test.go`.** Ctrl+G toggles.

```diff
diff --git a/internal/app/tui/model_test.go b/internal/app/tui/model_test.go
--- a/internal/app/tui/model_test.go
+++ b/internal/app/tui/model_test.go
@@ -2606,8 +2606,8 @@ func TestCtrlGExpandsThinkingAndToolRuns(t *testing.T) {
 	m.refreshViewport()
 
 	m = sendKey(m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
-	if m.density != densityFull {
-		t.Fatalf("ctrl+g from steps should reach full, got %v", m.density)
+	if !m.expandAll {
+		t.Fatal("ctrl+g should turn expand-all on")
 	}
 	view := stripANSI(m.View().Content)
 	if !strings.Contains(view, "reasoning about the budget") {
@@ -2620,12 +2620,8 @@ func TestCtrlGExpandsThinkingAndToolRuns(t *testing.T) {
 	}
 
 	m = sendKey(m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
-	if m.density != densityOutline {
-		t.Fatalf("ctrl+g from full should reach outline, got %v", m.density)
-	}
-	m = sendKey(m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
-	if m.density != densitySteps {
-		t.Fatalf("ctrl+g from outline should return to steps, got %v", m.density)
+	if m.expandAll {
+		t.Fatal("ctrl+g again should turn expand-all off")
 	}
 	if !strings.Contains(stripANSI(m.View().Content), "×3") {
 		t.Fatal("collapsing again should restore the ×3 line")
```

**`internal/app/tui/steps.go`.** Collapsed or expanded, never outline.

```diff
diff --git a/internal/app/tui/steps.go b/internal/app/tui/steps.go
--- a/internal/app/tui/steps.go
+++ b/internal/app/tui/steps.go
@@ -38,10 +38,9 @@ type subRegion struct {
 
 // stepRenderCtx carries everything renderStep reads from the Model.
 type stepRenderCtx struct {
-	// density resolves a node's level: its own override, else inherited.
-	density func(id viewmodel.NodeID, inherited density) density
-	// record is told the level each node is drawn at.
-	record func(id viewmodel.NodeID, d density)
+	// expanded resolves whether a node draws expanded: its own choice, else
+	// the state it inherits.
+	expanded func(id viewmodel.NodeID, inherited bool) bool
 	// liveExpanded is expanded for an in-flight call, which stays collapsed
 	// until clicked whatever the global default says.
 	liveExpanded func(viewmodel.NodeID) bool
@@ -63,16 +62,12 @@ type stepRenderCtx struct {
 	workdir string
 }
 
-// level resolves and records a node's density.
-func (c *stepRenderCtx) level(id viewmodel.NodeID, inherited density) density {
-	d := inherited
-	if c.density != nil {
-		d = c.density(id, inherited)
+// open resolves whether a node draws expanded.
+func (c *stepRenderCtx) open(id viewmodel.NodeID, inherited bool) bool {
+	if c.expanded == nil {
+		return inherited
 	}
-	if c.record != nil {
-		c.record(id, d)
-	}
-	return d
+	return c.expanded(id, inherited)
 }
 
 func (c *stepRenderCtx) liveToolExpanded(id viewmodel.NodeID) bool {
@@ -93,10 +88,10 @@ func (c *stepRenderCtx) callersFor(id viewmodel.NodeID) []string {
 // owner meta), the narration continuation, thinking rows, then tool rows and
 // subagent cards at nested indent. Every piece ends in a newline, so the
 // block's line count is strings.Count(out, "\n").
-func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited density) (string, []subRegion) {
+func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool) (string, []subRegion) {
 	si := n.Step
 	rows := n.Children
-	sd := c.level(n.ID, inherited)
+	open := c.open(n.ID, inherited)
 	var b strings.Builder
 	var subs []subRegion
 	lines := 0
@@ -143,18 +138,9 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited densit
 		dur:        stepDuration(n, c.now),
 		ownerColor: actorColor(si.Step.Actor.Role),
 	}
-	if sd == densityOutline {
-		meta.tools = countToolCalls(rows)
-	}
-	switch {
-	case headless && sd == densityOutline:
-		summary, _ := renderMeta("", "", fmt.Sprintf("%d tools", meta.tools), meta.dur, nil)
-		write(gutterPrefix(g, gc) + summary + "\n")
-	case headless:
-		// No header line.
-	default:
+	if !headless {
 		// Render narration as a single Markdown document so formatting and
-		// code fences survive sentence boundaries. Density never hides it.
+		// code fences survive sentence boundaries.
 		var texts []string
 		for _, m := range si.Narration {
 			if text := strings.TrimSpace(m.Content); text != "" {
@@ -185,21 +171,16 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited densit
 		}
 		rest = "" // The full narration has already been rendered once.
 	}
-	if sd == densityOutline {
-		// Outline hides reasoning and tool rows, never narration.
-		return b.String(), subs
-	}
 
 	// Narration continuation.
 	if rest != "" {
-		write(renderStepContinuation(rest, sd == densityFull, width))
+		write(renderStepContinuation(rest, width))
 	}
 
 	// Thinking rows.
 	for _, t := range si.Thinking {
 		id := viewmodel.ThinkingID(t)
-		td := c.level(id, sd)
-		row(id, renderNestedThinking(t, td == densityFull, width), subRegion{})
+		row(id, renderNestedThinking(t, c.open(id, open), width), subRegion{})
 	}
 	if si.LiveThinking != "" {
 		rv := c.region(viewmodel.LiveThinkingID)
@@ -213,8 +194,8 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited densit
 	}
 
 	// A long beat shows its newest rows; the rest collapse into one line.
-	// A user override on the beat (Enter / click to full) shows them all.
-	if over := len(rows) - viewmodel.BeatRowCap; over > 0 && sd != densityFull {
+	// Expanding the beat shows them all.
+	if over := len(rows) - viewmodel.BeatRowCap; over > 0 && !open {
 		write(strings.Repeat(" ", nestedBodyIndent) + mutedStyle().Render(fmt.Sprintf("+%d earlier calls", over)) + "\n")
 		rows = rows[over:]
 	}
@@ -225,28 +206,16 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited densit
 		case ch.Kind == viewmodel.KindTool && ch.Active != nil:
 			row(ch.ID, renderActiveToolRow(*ch.Active, c, c.liveToolExpanded(ch.ID), width), subRegion{})
 		case ch.Kind == viewmodel.KindTool && ch.Job != nil:
-			row(ch.ID, renderJobCard(ch, c.toolSpinner, c.now, c.level(ch.ID, sd) == densityFull, width, c.workdir), subRegion{live: ch.Job.Running})
+			row(ch.ID, renderJobCard(ch, c.toolSpinner, c.now, c.open(ch.ID, open), width, c.workdir), subRegion{live: ch.Job.Running})
 		case ch.Kind == viewmodel.KindTool && len(ch.Tools) > 1:
-			rd := c.level(ch.ID, sd)
-			if rd == densityOutline {
-				continue
-			}
-			row(ch.ID, renderToolGroupRow(ch.Tools, rd == densityFull, width), subRegion{})
+			row(ch.ID, renderToolGroupRow(ch.Tools, c.open(ch.ID, open), width), subRegion{})
 		case ch.Kind == viewmodel.KindTool && len(ch.Tools) == 1:
-			rd := c.level(ch.ID, sd)
-			if rd == densityOutline {
-				continue
-			}
-			row(ch.ID, renderToolRow(ch.Tools[0], rd == densityFull, c.callersFor(ch.ID), width, c.workdir), subRegion{})
+			row(ch.ID, renderToolRow(ch.Tools[0], c.open(ch.ID, open), c.callersFor(ch.ID), width, c.workdir), subRegion{})
 		case ch.Kind == viewmodel.KindSubagent && ch.Item != nil && ch.Item.Subagent != nil:
 			v := *ch.Item.Subagent
 			rv := c.region(ch.ID)
-			cd := c.level(ch.ID, sd)
-			card := renderSubagentCard(v, cd == densityFull, c.toolSpinner, rv, width-stepRowIndent)
-			if cd == densityOutline {
-				card = headRow(card)
-			}
-			sub := subRegion{live: v.Status == session.SubagentRunning && cd != densityOutline}
+			card := renderSubagentCard(v, c.open(ch.ID, open), c.toolSpinner, rv, width-stepRowIndent)
+			sub := subRegion{live: v.Status == session.SubagentRunning}
 			if v.Child != nil {
 				sub.subagent = &v
 			}
@@ -410,14 +379,6 @@ func renderTodoDivider(l *viewmodel.TodoLabel, width int) string {
 	return strings.Repeat(" ", gutterWidth) + mutedStyle().Render("──"+text+strings.Repeat("─", fill)) + "\n"
 }
 
-// headRow keeps only the first line of a rendered block, newline included.
-func headRow(s string) string {
-	if i := strings.IndexByte(s, '\n'); i >= 0 {
-		return s[:i+1]
-	}
-	return s
-}
-
 // countToolCalls is the number of tool calls a step made, counting each call
 // of a merged run.
 func countToolCalls(rows []*viewmodel.Node) int {
@@ -673,9 +634,8 @@ func firstSentence(s string) (head, rest string) { return viewmodel.FirstSentenc
 // which is rendered as plain text.
 func stripEmphasis(s string) string { return viewmodel.StripEmphasis(s) }
 
-// renderStepContinuation always renders the complete narration continuation.
-// Density controls tool and thinking details, not narration visibility.
-func renderStepContinuation(rest string, _ bool, width int) string {
+// renderStepContinuation renders the complete narration continuation.
+func renderStepContinuation(rest string, width int) string {
 	cw := nestedContentWidth(width)
 	pad := strings.Repeat(" ", nestedBodyIndent)
 	body, ok := renderMarkdown(rest, cw)
```

**`internal/app/tui/steps_test.go`.** Expanded instead of density.

````diff
diff --git a/internal/app/tui/steps_test.go b/internal/app/tui/steps_test.go
--- a/internal/app/tui/steps_test.go
+++ b/internal/app/tui/steps_test.go
@@ -185,9 +185,9 @@ func TestStepRowsStayWithinTheFrameAtEveryWidth(t *testing.T) {
 }
 
 func TestNarrationWrapsCompletelyWithoutDuplication(t *testing.T) {
-	for _, d := range []density{densityOutline, densitySteps, densityFull} {
+	for _, d := range []bool{false, true} {
 		for _, width := range []int{40, 80, 120} {
-			t.Run(fmt.Sprintf("density=%d/width=%d", d, width), func(t *testing.T) {
+			t.Run(fmt.Sprintf("expanded=%v/width=%d", d, width), func(t *testing.T) {
 				narration := "The baseline tests are running; I’ll delegate the document import while I prepare the task-by-task execution plan. Second sentence with important details.\n\nThird paragraph with detail."
 				// Exercise the production renderer directly at narrow widths too.
 				si := &viewmodel.StepInfo{Narration: []*session.Message{{Content: narration}}}
@@ -215,7 +215,7 @@ func TestNarrationWrapsCompletelyWithoutDuplication(t *testing.T) {
 func TestNarrationMarkdownRemainsWhole(t *testing.T) {
 	for _, text := range []string{"**Checking the parser. Then checking the lexer.**", "```go\nfunc example() {}\n```"} {
 		si := &viewmodel.StepInfo{Narration: []*session.Message{{Content: text}}}
-		out, _ := renderStep(&viewmodel.Node{Kind: viewmodel.KindStep, Step: si}, &stepRenderCtx{}, 80, densitySteps)
+		out, _ := renderStep(&viewmodel.Node{Kind: viewmodel.KindStep, Step: si}, &stepRenderCtx{}, 80, false)
 		plain := stripANSI(out)
 		if strings.Contains(plain, "**") || strings.Contains(plain, "```") {
 			t.Fatalf("markdown split or unrendered:\n%s", plain)
@@ -625,7 +625,7 @@ func TestRunningToolRowInsideStepExpandsOnClickEvenWithGlobalExpand(t *testing.T
 	m.state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: time.Now(), StepID: id, ToolCallID: "run"})
 	m.state.AppendActiveToolCallOutput("run", "PASS pkg/first\nPASS pkg/b\nPASS pkg/c\nPASS pkg/d\nPASS pkg/e\nPASS pkg/f\nPASS pkg/g\nPASS pkg/h\nPASS pkg/last")
 	m.busy, m.turnStartedAt = true, time.Now()
-	m.density = densityFull // ctrl+g on: settled rows open, running rows stay closed
+	m.expandAll = true // ctrl+g on: settled rows open, running rows stay closed
 
 	row := viewmodel.NodeID{Kind: viewmodel.KindTool, Key: fmt.Sprintf("tool:%d:run", id)}
 	m.invalidateTranscript()
````

**`internal/app/tui/tasks_test.go`.** The expand matrix; no outline.

```diff
diff --git a/internal/app/tui/tasks_test.go b/internal/app/tui/tasks_test.go
--- a/internal/app/tui/tasks_test.go
+++ b/internal/app/tui/tasks_test.go
@@ -142,26 +142,6 @@ func TestDroppedTodoGetsNoDivider(t *testing.T) {
 	}
 }
 
-func TestOutlineShowsOneRowPerStep(t *testing.T) {
-	m := newTestModel(t)
-	m.resize(120, 80)
-	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
-	for i := 0; i < 10; i++ {
-		stepFixture(t, &m, session.Actor{}, fmt.Sprintf("Step %d does a thing. More words here.", i), readEvent("a.go"))
-	}
-	m.density = densityOutline
-	text := viewText(&m)
-	if got := strings.Count(text, "does a thing."); got != 10 {
-		t.Fatalf("outline rows = %d, want 10:\n%s", got, text)
-	}
-	if strings.Contains(text, "a.go") {
-		t.Fatalf("tool rows are hidden at outline:\n%s", text)
-	}
-	if !strings.Contains(text, "1 tool") {
-		t.Fatalf("outline steps carry a tool count:\n%s", text)
-	}
-}
-
 func TestPreTaskSessionsRenderWithoutHeadersOrErrors(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(100, 40)
@@ -295,9 +275,10 @@ func TestSalvagedTurnReceiptSaysSo(t *testing.T) {
 	}
 }
 
-// The density matrix for one step with narration, reasoning and a tool call.
-func TestDensityMatrix(t *testing.T) {
-	build := func(d density) string {
+// One step with narration, reasoning and a tool call, collapsed and with
+// Ctrl+G's expand-all.
+func TestExpandMatrix(t *testing.T) {
+	build := func(all bool) string {
 		m := newTestModel(t)
 		m.resize(120, 80)
 		m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
@@ -307,18 +288,17 @@ func TestDensityMatrix(t *testing.T) {
 		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "r",
 			Args: []byte(`{"path":"cfg.go"}`), ResultSummary: "12 lines", ResultContent: "package cfg\nvar Secret = 1"})
 		m.state.EndStep(id)
-		m.density = d
+		m.expandAll = all
 		return viewText(&m)
 	}
-	outline, steps, full := build(densityOutline), build(densitySteps), build(densityFull)
+	collapsed, expanded := build(false), build(true)
 
 	for _, c := range []struct {
 		name, text string
 		want       map[string]bool // substring -> expected present
 	}{
-		{"outline", outline, map[string]bool{"Reading the config.": true, "Read file": false, "thought for": false, "1 tool": true}},
-		{"steps", steps, map[string]bool{"Reading the config.": true, "Read file": true, "thought for": true, "private reasoning text": false, "var Secret": false}},
-		{"full", full, map[string]bool{"Reading the config.": true, "private reasoning text": true, "var Secret": true}},
+		{"collapsed", collapsed, map[string]bool{"Reading the config.": true, "Read file": true, "thought for": true, "private reasoning text": false, "var Secret": false}},
+		{"expanded", expanded, map[string]bool{"Reading the config.": true, "private reasoning text": true, "var Secret": true}},
 	} {
 		for sub, want := range c.want {
 			if got := strings.Contains(c.text, sub); got != want {
```

**`internal/app/tui/viewport_render.go`.** Pass the expand state and hash it.

```diff
diff --git a/internal/app/tui/viewport_render.go b/internal/app/tui/viewport_render.go
--- a/internal/app/tui/viewport_render.go
+++ b/internal/app/tui/viewport_render.go
@@ -104,8 +104,7 @@ func (m *Model) refreshViewport() {
 
 	toolFrame := m.activeSpinnerFrame(session.ActivityTool)
 	rctx := &stepRenderCtx{
-		density:       m.densityOf,
-		record:        m.recordDensity,
+		expanded:      m.expandedUnder,
 		liveExpanded:  func(id viewmodel.NodeID) bool { return m.isToolExpanded(id, true) },
 		region:        m.regionView,
 		noteRows:      m.noteRegionRows,
@@ -268,7 +267,7 @@ func (m *Model) drawNode(n *viewmodel.Node, c *stepRenderCtx, width int) (string
 	case n.Kind == viewmodel.KindReceipt && n.Receipt != nil:
 		return renderReceipt(n.Receipt, width), nil
 	case n.Kind == viewmodel.KindStep && n.Step != nil:
-		return renderStep(n, c, width, m.density)
+		return renderStep(n, c, width, m.expandAll)
 	case n.Kind == viewmodel.KindThinking && n.Step != nil:
 		// Reasoning before any step has begun: the bounded live box on its own.
 		rv := m.regionView(viewmodel.LiveThinkingID)
@@ -283,14 +282,13 @@ func (m *Model) drawNode(n *viewmodel.Node, c *stepRenderCtx, width int) (string
 		return renderActiveToolCall(*n.Active, c.sandbox, c.allowNetwork, c.toolSpinner, c.now, m.isToolExpanded(n.ID, true), width), nil
 	case n.Item != nil:
 		rv := m.regionView(n.ID)
-		d := m.densityOf(n.ID, m.density)
-		m.recordDensity(n.ID, d)
-		return renderTranscriptItemIn(*n.Item, d == densityFull, m.spinnerFrame, rv, m.callers[n.ID], width, c.workdir), nil
+		return renderTranscriptItemIn(*n.Item, m.expandedUnder(n.ID, m.expandAll), m.spinnerFrame, rv, m.callers[n.ID], width, c.workdir), nil
 	}
 	return "", nil
 }
 
-// rowBeats maps every row inside a beat to that beat.
+// rowBeats maps every row inside a beat, thinking rows included, to that
+// beat.
 func rowBeats(turns []*viewmodel.Node) map[viewmodel.NodeID]viewmodel.NodeID {
 	out := map[viewmodel.NodeID]viewmodel.NodeID{}
 	for _, turn := range turns {
@@ -301,6 +299,9 @@ func rowBeats(turns []*viewmodel.Node) map[viewmodel.NodeID]viewmodel.NodeID {
 			for _, row := range n.Children {
 				out[row.ID] = n.ID
 			}
+			for _, t := range n.Step.Thinking {
+				out[viewmodel.ThinkingID(t)] = n.ID
+			}
 		}
 	}
 	return out
@@ -404,11 +405,11 @@ func (m *Model) nodeSig(n *viewmodel.Node, c *stepRenderCtx) uint64 {
 type sigWriter interface{ Write([]byte) (int, error) }
 
 func (m *Model) foldNodeSig(h sigWriter, n *viewmodel.Node, c *stepRenderCtx) {
-	// The node's own override (if any) is what varies per node; inherited
-	// levels come from ancestors, which this fold also covers.
-	ov, hasOv := m.override(n.ID)
-	fmt.Fprintf(h, "%s|%v|%d|%d|%d|", n.ID.Key, hasOv, ov, m.regionOffset[n.ID], m.regionRows[n.ID])
-	fmt.Fprintf(h, "d%d|", m.density)
+	// The node's own choice (if any) is what varies per node; inherited
+	// states come from ancestors, which this fold also covers.
+	ov, hasOv := m.ownExpanded(n.ID)
+	fmt.Fprintf(h, "%s|%v|%v|%d|%d|", n.ID.Key, hasOv, ov, m.regionOffset[n.ID], m.regionRows[n.ID])
+	fmt.Fprintf(h, "x%v|", m.expandAll)
 	if n.Kind == viewmodel.KindStep {
 		fmt.Fprintf(h, "%s|%s|", c.routeModel, c.routeProvider)
 	}
@@ -417,8 +418,8 @@ func (m *Model) foldNodeSig(h sigWriter, n *viewmodel.Node, c *stepRenderCtx) {
 	}
 	if n.Step != nil {
 		for _, t := range n.Step.Thinking {
-			tov, thas := m.override(viewmodel.ThinkingID(t))
-			fmt.Fprintf(h, "t%v|%d|", thas, tov)
+			tov, thas := m.ownExpanded(viewmodel.ThinkingID(t))
+			fmt.Fprintf(h, "t%v|%v|", thas, tov)
 		}
 	}
 	for _, ch := range n.Children {
@@ -432,7 +433,7 @@ func (m *Model) foldNodeSig(h sigWriter, n *viewmodel.Node, c *stepRenderCtx) {
 // not.
 func (m *Model) contentSignature(turns []*viewmodel.Node, width int, themeSig uint64, queued []string, notice session.Notice, noticeUp bool, reconnect string, hasTurns bool) uint64 {
 	h := fnv.New64a()
-	fmt.Fprintf(h, "w%d|t%d|g%d|", width, themeSig, m.density)
+	fmt.Fprintf(h, "w%d|t%d|x%v|", width, themeSig, m.expandAll)
 	fmt.Fprintf(h, "turns%v|", hasTurns)
 	c := &stepRenderCtx{}
 	if st, _ := m.transcriptSource(); st != nil {
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/app/tui/...   # ok apart from the known failure
grep -rn "density\b\|densityFull\|densityOutline\|nodeDensity\|effDensity" --include='*.go' internal/app/tui   # no output
```

Commit: `p-a-renderer-foundation: task 4 — One level of detail: per-row expand and Ctrl+G expand-all`.

## Task 5: Retire [tui.transcript]

**Goal:** `[tui.transcript] density` and `fold_finished_tasks` still parse but are no longer merged or saved, `/settings` drops them, and one warning at `tui.transcript` says they are ignored.

**Files:**

- `internal/app/config/config_test.go`
- `internal/app/config/defaults.go`
- `internal/app/config/diagnostics.go`
- `internal/app/config/diagnostics_test.go`
- `internal/app/config/file_types.go`
- `internal/app/config/layers.go`
- `internal/app/config/merge.go`
- `internal/app/config/save.go`
- `internal/app/config/types.go`
- `internal/app/tui/settings/frames_tui.go`

**Order:** Sequential after Task 4, which stops the TUI reading them.

**Steps:**

1. `types.go`: delete `TUIConfig.Transcript` and `TranscriptConfig`. `defaults.go`, `merge.go` and `save.go` drop it.
2. `file_types.go`: `fileTranscript` stays so existing files load; its comment says the table is retired.
3. `layers.go`: `Layers.TranscriptIgnoredBy []LayerID`, filled by `transcriptIgnoredBy(userFile, projectFile)` from the raw file mirrors, the way `SidePanelIgnoredBy` is.
4. `diagnostics.go`: diagnostic 11 (the unknown-density warning) becomes one warning at path `tui.transcript` naming every layer that sets either key.
5. `internal/app/tui/settings/frames_tui.go`: delete the "Transcript detail" and "Fold finished tasks" fields.
6. Tests: delete `TestTranscriptConfigDefaultsOverridesAndDiagnostic`. New `TestDiagnoseWarnsOnceForRetiredTranscriptKeys` (`diagnostics_test.go`): keys in both layers give one warning naming both, a save does not write the table, and defaults give none.

**Code:**

**`internal/app/config/config_test.go`.** Delete the old transcript test.

```diff
diff --git a/internal/app/config/config_test.go b/internal/app/config/config_test.go
--- a/internal/app/config/config_test.go
+++ b/internal/app/config/config_test.go
@@ -2134,43 +2134,3 @@ func TestNarrationConfigDefaultsOnAndOverrides(t *testing.T) {
 		t.Fatalf("saved config lost the overrides:\n%s", data)
 	}
 }
-
-func TestTranscriptConfigDefaultsOverridesAndDiagnostic(t *testing.T) {
-	cfg := Default()
-	if cfg.TUI.Transcript.Density != "steps" || !cfg.TUI.Transcript.FoldFinishedTasks {
-		t.Fatalf("defaults = %+v", cfg.TUI.Transcript)
-	}
-	dir := t.TempDir()
-	path := filepath.Join(dir, ".marshal", "config.toml")
-	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
-		t.Fatal(err)
-	}
-	body := "[tui.transcript]\ndensity = \"verbose\"\nfold_finished_tasks = false\n"
-	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
-		t.Fatal(err)
-	}
-	loaded, err := Load(LoadOptions{HomeDir: t.TempDir(), WorkingDir: dir})
-	if err != nil {
-		t.Fatal(err)
-	}
-	if loaded.TUI.Transcript.Density != "verbose" || loaded.TUI.Transcript.FoldFinishedTasks {
-		t.Fatalf("file values must override: %+v", loaded.TUI.Transcript)
-	}
-	var found bool
-	for _, d := range Diagnose(loaded, Layers{}) {
-		if d.Path == "tui.transcript.density" {
-			found = true
-		}
-	}
-	if !found {
-		t.Fatal("an unknown density must produce a diagnostic")
-	}
-	out := filepath.Join(t.TempDir(), ".marshal", "config.toml")
-	if err := SaveProjectConfig(out, loaded, Layers{}); err != nil {
-		t.Fatal(err)
-	}
-	data, _ := os.ReadFile(out)
-	if !strings.Contains(string(data), "fold_finished_tasks = false") {
-		t.Fatalf("saved config lost the override:\n%s", data)
-	}
-}
```

**`internal/app/config/defaults.go`.** No transcript default.

```diff
diff --git a/internal/app/config/defaults.go b/internal/app/config/defaults.go
--- a/internal/app/config/defaults.go
+++ b/internal/app/config/defaults.go
@@ -191,7 +191,6 @@ func Default() Config {
 				MaxCols:  60,
 			},
 			Suggestions: "rules",
-			Transcript:  TranscriptConfig{Density: "steps", FoldFinishedTasks: true},
 		},
 		Hooks: HooksConfig{
 			FailClosed: false,
```

**`internal/app/config/diagnostics.go`.** The retirement warning.

```diff
diff --git a/internal/app/config/diagnostics.go b/internal/app/config/diagnostics.go
--- a/internal/app/config/diagnostics.go
+++ b/internal/app/config/diagnostics.go
@@ -295,15 +295,20 @@ func Diagnose(cfg Config, layers Layers) []Diagnostic {
 		})
 	}
 
-	// 11: [tui.transcript] density. An unknown level is read as "steps".
-	switch cfg.TUI.Transcript.Density {
-	case "", "outline", "steps", "full":
-	default:
+	// 11: [tui.transcript]. The density ladder and beat folding were removed:
+	// the transcript has one level of detail. density and fold_finished_tasks
+	// still parse (existing files must keep loading) but do nothing. One
+	// diagnostic however many layers set them.
+	if len(layers.TranscriptIgnoredBy) > 0 {
+		sources := make([]string, len(layers.TranscriptIgnoredBy))
+		for i, l := range layers.TranscriptIgnoredBy {
+			sources[i] = l.String()
+		}
 		ds = append(ds, Diagnostic{
 			Severity: SeverityWarning,
-			Path:     "tui.transcript.density",
-			Message:  "unknown density " + strconv.Quote(cfg.TUI.Transcript.Density) + " (accepted: \"outline\", \"steps\", \"full\"); using \"steps\"",
-			Source:   layers.ProvenanceOf("tui.transcript.density").SetBy.String(),
+			Path:     "tui.transcript",
+			Message:  "tui.transcript.density and fold_finished_tasks are ignored — the transcript has one level of detail; Enter or a click expands a row, Ctrl+G expands everything",
+			Source:   strings.Join(sources, ", "),
 		})
 	}
 
```

**`internal/app/config/diagnostics_test.go`.** The new test.

```diff
diff --git a/internal/app/config/diagnostics_test.go b/internal/app/config/diagnostics_test.go
--- a/internal/app/config/diagnostics_test.go
+++ b/internal/app/config/diagnostics_test.go
@@ -1,6 +1,7 @@
 package config
 
 import (
+	"path/filepath"
 	"strings"
 	"testing"
 
@@ -417,6 +418,50 @@ func TestDiagnoseWarnsOnceForDeadSidePanelKeys(t *testing.T) {
 	}
 }
 
+// The retired [tui.transcript] keys still load, warn once whichever layers set
+// them, and are gone from the next save.
+func TestDiagnoseWarnsOnceForRetiredTranscriptKeys(t *testing.T) {
+	home, work, userPath, projectPath := hoistPaths(t)
+	writeFile(t, userPath, "[tui.transcript]\ndensity = \"full\"\n")
+	writeFile(t, projectPath, "[tui.transcript]\nfold_finished_tasks = false\n")
+
+	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work, TrustResolver: trust.FixedResolver{Decision: trust.DecisionTrustSession}})
+	if err != nil {
+		t.Fatalf("LoadLayers: %v", err)
+	}
+	var got []Diagnostic
+	for _, d := range Diagnose(l.Merged, l) {
+		if d.Path == "tui.transcript" {
+			got = append(got, d)
+		}
+	}
+	if len(got) != 1 {
+		t.Fatalf("tui.transcript diagnostics = %d, want exactly 1: %+v", len(got), got)
+	}
+	if d := got[0]; d.Severity != SeverityWarning || !strings.Contains(d.Message, "one level of detail") ||
+		!strings.Contains(d.Source, "user config") || !strings.Contains(d.Source, "project config") {
+		t.Fatalf("diagnostic = %+v", d)
+	}
+
+	out := filepath.Join(t.TempDir(), ".marshal", "config.toml")
+	if err := SaveProjectConfig(out, l.Merged, l); err != nil {
+		t.Fatal(err)
+	}
+	if data := readFile(t, out); strings.Contains(data, "transcript") {
+		t.Fatalf("a save must not write the retired table:\n%s", data)
+	}
+
+	quiet, err := LoadLayers(LoadOptions{HomeDir: t.TempDir(), WorkingDir: t.TempDir()})
+	if err != nil {
+		t.Fatalf("LoadLayers: %v", err)
+	}
+	for _, d := range Diagnose(quiet.Merged, quiet) {
+		if d.Path == "tui.transcript" {
+			t.Fatalf("defaults produced a transcript diagnostic: %+v", d)
+		}
+	}
+}
+
 func TestDiagnoseQuietForDefaultsAndHiddenOnly(t *testing.T) {
 	home, work, userPath, _ := hoistPaths(t)
 	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
```

**`internal/app/config/file_types.go`.** Comment.

```diff
diff --git a/internal/app/config/file_types.go b/internal/app/config/file_types.go
--- a/internal/app/config/file_types.go
+++ b/internal/app/config/file_types.go
@@ -257,8 +257,8 @@ type fileTUI struct {
 	Transcript   *fileTranscript   `toml:"transcript"`
 }
 
-// fileTranscript mirrors [tui.transcript]; FoldFinishedTasks folds finished
-// beats to one row (the z toggle).
+// fileTranscript mirrors the retired [tui.transcript] table. Its keys still
+// parse so existing files load; they do nothing, and Diagnose says so.
 type fileTranscript struct {
 	Density           *string `toml:"density"`
 	FoldFinishedTasks *bool   `toml:"fold_finished_tasks"`
```

**`internal/app/config/layers.go`.** Record which layers set the keys.

```diff
diff --git a/internal/app/config/layers.go b/internal/app/config/layers.go
--- a/internal/app/config/layers.go
+++ b/internal/app/config/layers.go
@@ -59,6 +59,10 @@ type Layers struct {
 	// do nothing; presence is read from the raw file mirrors because a
 	// value equal to its default is indistinguishable in the merged config.
 	SidePanelIgnoredBy []LayerID
+	// TranscriptIgnoredBy lists the file layers that set a key of the retired
+	// [tui.transcript] table, read from the raw file mirrors like
+	// SidePanelIgnoredBy.
+	TranscriptIgnoredBy []LayerID
 	// InteractionNotices records source-specific interaction migration
 	// notices (retired approval_mode spellings, ignored legacy fields).
 	// Diagnose appends them to its result.
@@ -217,6 +221,7 @@ func LoadLayers(opts LoadOptions) (Layers, error) {
 		Default:                      def, User: user, Merged: cfg, Migrated: migrated,
 		SubtaskIterationsSet:      subtaskSet,
 		SidePanelIgnoredBy:        sidePanelIgnoredBy(userFile, projectFile),
+		TranscriptIgnoredBy:       transcriptIgnoredBy(userFile, projectFile),
 		RemoteLimitDiscoverySetBy: remoteLimitDiscoverySetBy(userFile, projectFile),
 		HoistedProviders:          hoistedProviders,
 		HoistedPresets:            hoistedPresets,
@@ -247,6 +252,25 @@ func sidePanelIgnoredBy(user, project configFile) []LayerID {
 	return out
 }
 
+// transcriptIgnoredBy reports which file layers set a key of the retired
+// [tui.transcript] table.
+func transcriptIgnoredBy(user, project configFile) []LayerID {
+	set := func(f configFile) bool {
+		if f.TUI == nil || f.TUI.Transcript == nil {
+			return false
+		}
+		return f.TUI.Transcript.Density != nil || f.TUI.Transcript.FoldFinishedTasks != nil
+	}
+	var out []LayerID
+	if set(user) {
+		out = append(out, LayerUser)
+	}
+	if set(project) {
+		out = append(out, LayerProject)
+	}
+	return out
+}
+
 // remoteLimitDiscoverySetBy reports which file layers still set the removed
 // privacy.remote_limit_discovery key.
 func remoteLimitDiscoverySetBy(user, project configFile) []LayerID {
```

**`internal/app/config/merge.go`.** Stop merging.

```diff
diff --git a/internal/app/config/merge.go b/internal/app/config/merge.go
--- a/internal/app/config/merge.go
+++ b/internal/app/config/merge.go
@@ -357,10 +357,6 @@ func merge(cfg *Config, file configFile) error {
 		set(&cfg.TUI.Mode, file.TUI.Mode)
 		set(&cfg.TUI.MouseCapture, file.TUI.MouseCapture)
 		set(&cfg.TUI.Suggestions, file.TUI.Suggestions)
-		if tr := file.TUI.Transcript; tr != nil {
-			set(&cfg.TUI.Transcript.Density, tr.Density)
-			set(&cfg.TUI.Transcript.FoldFinishedTasks, tr.FoldFinishedTasks)
-		}
 		if sp := file.TUI.SidePanel; sp != nil {
 			set(&cfg.TUI.SidePanel.Enabled, sp.Enabled)
 			set(&cfg.TUI.SidePanel.MinWidth, sp.MinWidth)
```

**`internal/app/config/save.go`.** Stop saving.

```diff
diff --git a/internal/app/config/save.go b/internal/app/config/save.go
--- a/internal/app/config/save.go
+++ b/internal/app/config/save.go
@@ -357,10 +357,6 @@ func writeSections(file *configFile, cfg Config, def Config) {
 			Mode:         strutil.Ptr(cfg.TUI.Mode),
 			MouseCapture: strutil.Ptr(cfg.TUI.MouseCapture),
 			Suggestions:  strutil.Ptr(cfg.TUI.Suggestions),
-			Transcript: &fileTranscript{
-				Density:           strutil.Ptr(cfg.TUI.Transcript.Density),
-				FoldFinishedTasks: strutil.Ptr(cfg.TUI.Transcript.FoldFinishedTasks),
-			},
 		}
 		// Only hidden is still meaningful. Writing the deprecated sizing keys
 		// would make every later load warn about a file the app wrote itself.
```

**`internal/app/config/types.go`.** Delete the type and the field.

```diff
diff --git a/internal/app/config/types.go b/internal/app/config/types.go
--- a/internal/app/config/types.go
+++ b/internal/app/config/types.go
@@ -269,19 +269,6 @@ type TUIConfig struct {
 	// additionally falls back to a background model call when the rules
 	// produce no suggestion.
 	Suggestions string `toml:"suggestions"`
-	// Transcript sets how much of the conversation the scrollback shows.
-	Transcript TranscriptConfig `toml:"transcript"`
-}
-
-// TranscriptConfig is the [tui.transcript] table.
-type TranscriptConfig struct {
-	// Density is the starting detail level, cycled with Ctrl+G: "outline"
-	// (one row per step), "steps" (the default), or "full". Any other value
-	// is reported as a diagnostic and treated as "steps".
-	Density string `toml:"density"`
-	// FoldFinishedTasks folds finished beats to one row. It is the session
-	// default for the z toggle in browse mode.
-	FoldFinishedTasks bool `toml:"fold_finished_tasks"`
 }
 
 // SidePanelConfig is what remains of the removed widescreen side rail. Only
```

**`internal/app/tui/settings/frames_tui.go`.** Delete the two fields.

```diff
diff --git a/internal/app/tui/settings/frames_tui.go b/internal/app/tui/settings/frames_tui.go
--- a/internal/app/tui/settings/frames_tui.go
+++ b/internal/app/tui/settings/frames_tui.go
@@ -43,24 +43,6 @@ func interfaceFrame(s *state) *frame {
 				SetFieldWriteGlobal(f, true)
 				return f
 			}(),
-			func() *field {
-				f := enumField("tui.transcript.density", "Transcript detail", []string{"outline", "steps", "full"},
-					func() string { return s.cfg.TUI.Transcript.Density },
-					func(v string) { s.cfg.TUI.Transcript.Density = v })
-				f.TomlPath = "tui.transcript.density"
-				f.Desc = "starting detail level; Ctrl+G cycles it (outline · steps · full)"
-				SetFieldWriteGlobal(f, true)
-				return f
-			}(),
-			func() *field {
-				f := &field{ID: "tui.transcript.fold_finished_tasks", Title: "Fold finished tasks", Kind: kindToggle,
-					TomlPath: "tui.transcript.fold_finished_tasks",
-					Desc:     "collapse a completed task to one row (z toggles in browse mode)",
-					GetBool:  func() bool { return s.cfg.TUI.Transcript.FoldFinishedTasks },
-					SetBool:  func(v bool) { s.cfg.TUI.Transcript.FoldFinishedTasks = v }}
-				SetFieldWriteGlobal(f, true)
-				return f
-			}(),
 		}
 	})
 }
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/app/config/ ./internal/app/tui/settings/   # ok
go build ./... && grep -rn "TUI.Transcript\|TranscriptConfig" --include='*.go' .   # builds; no output
```

Commit: `p-a-renderer-foundation: task 5 — Retire tui.transcript`.

## Task 6: A running file.write names its path

**Goal:** An in-flight `file.write` reports its path as both the active call's argument summary and its `Path`, so the running row names the file rather than whichever string argument the map yields first.

**Files:**

- `internal/agent/toolargs.go`
- `internal/agent/toolargs_test.go`

**Order:** Parallel-safe with Tasks 1 to 5 and 7: it touches only `internal/agent`.

**Steps:**

1. `SummarizeToolArgs` (`internal/agent/toolargs.go`): `file.write` joins `file.edit`'s case and returns the `path` argument. Before, the default branch returned the first string value in map iteration order, which could be the whole file's `content`.
2. `firstPatchPathFromArgs`: `file.write` returns its `path`, as `file.edit` does. A subagent's current-tool label then reads "editing <path>" for a write too (`session/subagents.go`).
3. Test `TestFileWriteNamesItsPath`: twenty calls (map order is random) all return the path, and so does `firstPatchPathFromArgs`.

**Code:**

**`internal/agent/toolargs.go`.** 

```diff
diff --git a/internal/agent/toolargs.go b/internal/agent/toolargs.go
--- a/internal/agent/toolargs.go
+++ b/internal/agent/toolargs.go
@@ -34,7 +34,9 @@ func SummarizeToolArgs(toolName string, args json.RawMessage) string {
 		// The patch body is large and would flood the live tool-call line;
 		// the file path is surfaced separately via ActiveToolCall.Path.
 		return "patch"
-	case "file.edit":
+	case "file.edit", "file.write":
+		// file.write's content is the whole file; the default branch's
+		// "first string value" could pick it over the path.
 		if p, ok := m["path"].(string); ok {
 			return p
 		}
@@ -65,8 +67,9 @@ func SummarizeToolArgs(toolName string, args json.RawMessage) string {
 }
 
 // firstPatchPathFromArgs names the file a write tool is working on: the first
-// "File: <path>" line of a file.write_patch proposal, or file.edit's own path
-// argument. It returns "" for any other tool or when no path is present.
+// "File: <path>" line of a file.write_patch proposal, or the path argument of
+// file.edit and file.write. It returns "" for any other tool or when no path
+// is present.
 func firstPatchPathFromArgs(toolName string, args json.RawMessage) string {
 	if len(args) == 0 {
 		return ""
@@ -75,7 +78,7 @@ func firstPatchPathFromArgs(toolName string, args json.RawMessage) string {
 	if err := json.Unmarshal(args, &m); err != nil {
 		return ""
 	}
-	if toolName == "file.edit" {
+	if toolName == "file.edit" || toolName == "file.write" {
 		p, _ := m["path"].(string)
 		return p
 	}
```

**`internal/agent/toolargs_test.go`.** 

```diff
diff --git a/internal/agent/toolargs_test.go b/internal/agent/toolargs_test.go
--- a/internal/agent/toolargs_test.go
+++ b/internal/agent/toolargs_test.go
@@ -29,6 +29,20 @@ func TestSummarizeToolArgsFileRead(t *testing.T) {
 	}
 }
 
+// file.write carries the whole file as content; the running row must name the
+// path, never the first line of the file.
+func TestFileWriteNamesItsPath(t *testing.T) {
+	args := json.RawMessage(`{"content":"package main\n\nfunc main() {}\n","path":"cmd/tool/main.go"}`)
+	for i := 0; i < 20; i++ { // map order is random; the path must win every time
+		if got := SummarizeToolArgs("file.write", args); got != "cmd/tool/main.go" {
+			t.Fatalf("SummarizeToolArgs(file.write) = %q, want the path", got)
+		}
+	}
+	if got := firstPatchPathFromArgs("file.write", args); got != "cmd/tool/main.go" {
+		t.Fatalf("firstPatchPathFromArgs(file.write) = %q, want the path", got)
+	}
+}
+
 func TestSummarizeToolArgsRepoSearch(t *testing.T) {
 	args, _ := json.Marshal(map[string]string{"query": "func main", "pattern": "*.go"})
 	got := SummarizeToolArgs("repo.search", args)
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/agent/ ./internal/app/session/   # ok
```

Commit: `p-a-renderer-foundation: task 6 — A running file.write names its path`.

## Task 7: A settled call's body as reusable lines

**Goal:** The detail under a settled call (the callers line, the diff, the expanded result and the failure detail) comes from two functions that return styled lines, so the spine rows in Task 8 can hang the same detail at their own indent; `renderCompletedToolCallIn`'s output is unchanged.

**Files:**

- `internal/app/tui/transcript.go`

**Order:** Sequential before Task 8.

**Steps:**

Move the body of `renderCompletedToolCallIn` after its head into:
- `callersDetail(callers []string, w int) []string`: the blast-radius line, wrapped and muted;
- `toolBody(event registry.AuditEvent, expanded bool, w int) []string`: the diff (capped while collapsed), the expanded non-diff result and the failure detail, each line styled and fitted to `w`.

`renderCompletedToolCallIn` prefixes the first with `continuation()` at `contentWidth(width)`, and the second with `nestedRail()` at `nestedContentWidth(width)`, exactly as before. This is a pure refactor: no test changes, and the renderer tests in `transcript_test.go`, `callers_test.go` and `symbolrow_test.go` must pass unchanged.

**Code:**

**`internal/app/tui/transcript.go`.** 

```diff
diff --git a/internal/app/tui/transcript.go b/internal/app/tui/transcript.go
--- a/internal/app/tui/transcript.go
+++ b/internal/app/tui/transcript.go
@@ -1075,28 +1075,51 @@ func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers
 			b.WriteString("\n")
 		}
 	}
-	// Blast radius: a continuation line naming what references the symbol
-	// this row changed. It is a settled one-time addition to a settled row,
-	// so the height change it causes is acceptable and it is not part of any
-	// live-region body. Nothing renders when there is no cached result — a
-	// no-server negative, an empty reference set, or a language without LSP
-	// all leave the row exactly as it would be without this feature.
-	if len(callers) > 0 {
-		shown, extra := callers, 0
-		if len(shown) > maxCallersShown {
-			extra = len(shown) - maxCallersShown
-			shown = shown[:maxCallersShown]
-		}
-		line := fmt.Sprintf("↳ %d callers: %s", len(callers), strings.Join(shown, ", "))
-		if extra > 0 {
-			line += fmt.Sprintf(" +%d", extra)
-		}
-		for _, wl := range strings.Split(ansi.Wrap(line, contentWidth(width), WrapBreakpoints), "\n") {
-			b.WriteString(continuation())
-			b.WriteString(mutedStyle().Render(wl))
-			b.WriteString("\n")
-		}
+	for _, l := range callersDetail(callers, contentWidth(width)) {
+		b.WriteString(continuation())
+		b.WriteString(l)
+		b.WriteString("\n")
+	}
+	for _, l := range toolBody(event, expanded, nestedContentWidth(width)) {
+		b.WriteString(nestedRail())
+		b.WriteString(l)
+		b.WriteString("\n")
+	}
+	return b.String()
+}
+
+// callersDetail is the blast-radius line under a row that changed a symbol,
+// wrapped to w and styled: what references the symbol. It is a settled
+// one-time addition to a settled row, so the height change it causes is
+// acceptable and it is not part of any live-region body. Nothing renders when
+// there is no cached result — a no-server negative, an empty reference set,
+// or a language without LSP all leave the row exactly as it would be without
+// this feature.
+func callersDetail(callers []string, w int) []string {
+	if len(callers) == 0 {
+		return nil
 	}
+	shown, extra := callers, 0
+	if len(shown) > maxCallersShown {
+		extra = len(shown) - maxCallersShown
+		shown = shown[:maxCallersShown]
+	}
+	line := fmt.Sprintf("↳ %d callers: %s", len(callers), strings.Join(shown, ", "))
+	if extra > 0 {
+		line += fmt.Sprintf(" +%d", extra)
+	}
+	var out []string
+	for _, wl := range strings.Split(ansi.Wrap(line, w, WrapBreakpoints), "\n") {
+		out = append(out, mutedStyle().Render(wl))
+	}
+	return out
+}
+
+// toolBody is a settled call's body under its head, styled and fitted to w:
+// an edit's diff (capped while collapsed), and when expanded any other call's
+// result and a failed call's error and arguments.
+func toolBody(event registry.AuditEvent, expanded bool, w int) []string {
+	var out []string
 	if isDiffTool(event.ToolName) && event.ResultContent != "" {
 		files := splitDiffFiles(event.ResultContent)
 		shown := len(files)
@@ -1109,11 +1132,9 @@ func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers
 			if f.path != "" {
 				stat = f.path + " " + stat
 			}
-			b.WriteString(nestedRail())
-			b.WriteString(dimStyle().Render(stat))
-			b.WriteString("\n")
+			out = append(out, dimStyle().Render(stat))
 			rendered := diffview.Render(f.raw, diffview.Options{
-				Width:     nestedContentWidth(width),
+				Width:     w,
 				Mode:      diffview.ModeAuto,
 				Highlight: true,
 			})
@@ -1127,29 +1148,23 @@ func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers
 				if line == "" {
 					continue
 				}
-				b.WriteString(nestedRail())
 				// Clip rather than wrap: diffview does not hard-clamp to the
 				// width it is given, and a wrapped continuation carries no
-				// nested rail, so an over-wide line breaks the chrome.
-				b.WriteString(ansi.Truncate(line, nestedContentWidth(width), "…"))
-				b.WriteString("\n")
+				// rail, so an over-wide line breaks the chrome.
+				out = append(out, ansi.Truncate(line, w, "…"))
 			}
 			if elided > 0 {
-				b.WriteString(nestedRail())
-				b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
-				b.WriteString("\n")
+				out = append(out, dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
 			}
 		}
 		if !expanded && len(files) > shown {
-			b.WriteString(nestedRail())
-			b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more files", len(files)-shown)))
-			b.WriteString("\n")
+			out = append(out, dimStyle().Render(fmt.Sprintf("… %d more files", len(files)-shown)))
 		}
 	}
 	// Non-diff tools with captured output (agent.run is the motivating
-	// case — its ResultContent holds the subagent's report) render it on
-	// ctrl+g, with the same gutter indentation as diffs but using Wrap
-	// instead of Truncate since the content is prose, not code lines.
+	// case — its ResultContent holds the subagent's report) render it when
+	// expanded, wrapped rather than clipped since the content is prose, not
+	// code lines.
 	if expanded && !isDiffTool(event.ToolName) && event.ResultContent != "" {
 		lines := strings.Split(strings.TrimRight(event.ResultContent, "\n"), "\n")
 		elided := 0
@@ -1158,23 +1173,18 @@ func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers
 			lines = lines[:maxExpandedResultLines]
 		}
 		for _, line := range lines {
-			wrapped := ansi.Wrap(line, nestedContentWidth(width), WrapBreakpoints)
-			for _, wl := range strings.Split(wrapped, "\n") {
-				b.WriteString(nestedRail())
-				b.WriteString(dimStyle().Render(wl))
-				b.WriteString("\n")
+			for _, wl := range strings.Split(ansi.Wrap(line, w, WrapBreakpoints), "\n") {
+				out = append(out, dimStyle().Render(wl))
 			}
 		}
 		if elided > 0 {
-			b.WriteString(nestedRail())
-			b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
-			b.WriteString("\n")
+			out = append(out, dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
 		}
 	}
 	// Failed calls carry no ResultContent; expanding one shows the full
 	// error, the exit code when the tool reported one, and the call args.
-	// Each detail line wraps at the nested content width — the raw args JSON
-	// used to run straight off the terminal edge.
+	// Each detail line wraps at w — the raw args JSON used to run straight
+	// off the terminal edge.
 	if expanded && event.Error != "" {
 		detail := "error: " + event.Error
 		if event.CommandExitCode != nil {
@@ -1185,15 +1195,12 @@ func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers
 			lines = append(lines, "args: "+string(event.Args))
 		}
 		for _, line := range lines {
-			wrapped := ansi.Wrap(line, nestedContentWidth(width), WrapBreakpoints)
-			for _, wl := range strings.Split(wrapped, "\n") {
-				b.WriteString(nestedRail())
-				b.WriteString(dimStyle().Render(wl))
-				b.WriteString("\n")
+			for _, wl := range strings.Split(ansi.Wrap(line, w, WrapBreakpoints), "\n") {
+				out = append(out, dimStyle().Render(wl))
 			}
 		}
 	}
-	return b.String()
+	return out
 }
 
 const (
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/app/tui/   # ok apart from the known failure, with no test file changed
```

Commit: `p-a-renderer-foundation: task 7 — A settled call's body as reusable lines`.

## Task 8: Beats on the spine

**Goal:** Each beat draws as its head (`●`, the narration, and the meta with `thought Ns` and the `▸ N/M` tag) with its calls hung off `├ └`: a verb, a subject and a right-aligned result, a plan line where the list was written, detail under the subject when expanded, and copy that takes the subject.

**Files:**

- `internal/app/tui/beats_render_test.go`
- `internal/app/tui/cards_render_test.go`
- `internal/app/tui/click_test.go`
- `internal/app/tui/drill_test.go`
- `internal/app/tui/expand_test.go`
- `internal/app/tui/glyph/glyph.go`
- `internal/app/tui/indent_contract_test.go`
- `internal/app/tui/model_test.go`
- `internal/app/tui/nodes_helpers_test.go`
- `internal/app/tui/scrub_render_test.go`
- `internal/app/tui/selection.go`
- `internal/app/tui/selection_input_test.go`
- `internal/app/tui/selection_test.go`
- `internal/app/tui/selection_text.go`
- `internal/app/tui/spine.go` (new)
- `internal/app/tui/spine_test.go` (new)
- `internal/app/tui/steps.go`
- `internal/app/tui/steps_test.go`
- `internal/app/tui/tasks_test.go`
- `internal/app/tui/tool_head_test.go`
- `internal/app/tui/toolgroup.go` (deleted)
- `internal/app/tui/toolgroup_test.go`
- `internal/app/tui/toolnames.go`
- `internal/app/tui/toolnames_test.go`
- `internal/app/tui/transcript.go`
- `internal/app/tui/view_test.go`
- `internal/app/tui/viewport_render.go`

**Order:** Sequential.

**Steps:**

**Glyphs** (`glyph/glyph.go`): `Beat = Brand` (`●`; `Brand` never appears in the transcript) and the spine glyphs `SpineMid` `├`, `SpineEnd` `└` and `Spine` `│`.

**Rows** (new `spine.go`, below):
- `callVerb`: the verb table, first prefix wins; unknown tools use the first segment of their name;
- `fallbackSubject`: the rest of the name when the arguments name nothing, unless it repeats the verb;
- `callSubject`: a command's first line with the workdir elided, a subagent's `description`, a symbol subject for subject-first tools, a quoted query (prefixed by its qualifier when that is not "search"), else `toolTarget`; scrubbed;
- `callResult`: in order, the error, a command's `exit N · 3s`, an edit's `+N −M`, a started subagent `⧉ #N started` (from the notice's `SubagentWork().AgentID`, else the legacy "started as subagent N" summary), else the summary. A hook's decision leads. Failures are in the error style;
- `spineHead` lays out a row in `w` cells: the verb padded to `verbWidth` (8), the subject truncated to what the result leaves, and the result right-aligned and capped at half the room;
- `spineBlock` prefixes a rendered row: `├`/`└` on its first line and `│` (or a space for the last row) on the rest, at `gutterWidth`;
- `renderCallRow`: one line collapsed, plus the callers line and, for a failed call, `failedTail` (the last `failedTailLines` lines). Expanded, `toolBody` hangs under the subject. A collapsed edit is one line; its diff shows only when expanded;
- `renderGroupRow`: the subjects joined, `×N`; expanded, one `subject · summary` line per call;
- `renderJobRow` and `jobState`: `renderJobCard`'s states moved onto the spine. The result is `┆ job-1` in the info colour, then the state: `⠋ running · 2m`, `stopping`, `ended with session`, `exit 1 · 48s`, `stopped` and so on, with `truncated` appended when the output hit its cap. Expanded, the job's output follows;
- `renderActiveCallRow`: the active call's `Path`, else the first line of its summarised `Args`, never raw arguments; a spinner and the elapsed time as the result; the sandbox line for commands, and the last `liveTailLines` (6) output lines, or all of them when expanded;
- `renderPlanRow`: `plan` then one mark per item (`✓`, `▸`, `·`), or the plan blocks past `maxPlanMarks` (12); the active position and text; `D/M done` when everything is complete, `D/M` when nothing is active, and `plan cleared`.

**Beats** (`steps.go`):
- `renderStep`'s head: `metaParts` gains `thought` (`thoughtMeta`, the beat's summed thinking time) and `tag` (`todoTag`, `▸ N/M` in the accent colour), and loses `tools`;
- `rightMeta` drops the model, then the duration, then the thought, then shortens the owner to its role word and drops it. The tag goes last;
- the divider (`renderTodoDivider`) and the separate thinking row (`renderNestedThinking`) are deleted;
- a headless beat draws `headlessHead`: the glyph, a muted `N calls`, and the meta;
- an expanded beat draws `renderBeatReasoning` (every thinking block behind the nested rail) between its head and its calls;
- spine rows are the plan rows and tool rows. `BeatRowCap` counts only those, and its line becomes a `… N earlier calls` spine row;
- each row hangs at `width-spineWidth`. Rows no longer get blank lines around multi-line rows: the spine carries the structure;
- the plan row records no click region, so a click on it opens the beat;
- in-place subagent cards (the projection without live work) draw after the spine at `cardIndent` (4), as before;
- delete `stepRowIndent`, `renderJobCard`, `renderToolRow`, `renderToolGroupRow`, `renderActiveToolRow`, `stepHeaderLine` (it had no caller), `renderStepContinuation` and the old `failedTail`;
- `stepGlyph` returns `glyph.Beat` for a settled beat.

**Removed with the old rows**: `toolgroup.go` (`toolTarget` moves to `spine.go`; the group head and renderer go) and `toolnames.go`'s pluralizer. `toolCategoryGlyph` stays for the now bar, the subagent card body and the standalone `renderCompletedToolCallIn`.

**Thinking rows are gone from beats**, so `viewport_render.go` drops the thinking override from `foldNodeSig` and the thinking mapping Task 4 added to `rowBeats`.

**Copy** (`selection_text.go`, `selection.go`): the spine glyphs are gutter glyphs.
- After `├` or `└`, `spanOf` also skips the verb and its padding, so a row copies from its subject.
- After `│`, it skips up to `verbWidth` spaces, so detail copies from the same column.
- The right-aligned result stays meta, as before.
- `fullToolHead` returns the untruncated subject (the subjects joined, for a run) whenever the row shows an ellipsis.

**Tests.**
- New `spine_test.go`:
  - `TestCallVerbs`, `TestFallbackSubjectNeverRepeatsTheVerb`, `TestCallRowLaysOutVerbSubjectAndResult`, `TestCallResults` and `TestCallSubjects`;
  - `TestEditRowIsOneLineUntilExpanded` and `TestFailedCallShowsItsTailCollapsed`;
  - `TestGroupRowCollapsedIsOneLineExpandedOnePerCall` and `TestGroupRowFitsItsWidth`;
  - `TestActiveCallRowNamesItsSubject` and `TestPlanRow`;
  - `TestSpineConnectsRowsToTheirBeat`: `├ │ ├ └`, with no spine after the last row;
  - `TestSpineRowsFitTheFrame`: every row kind at 40, 80 and 120 columns, collapsed and expanded;
  - `TestSpineRowsCopyFromTheSubjectColumn`.
- `beats_render_test.go`:
  - the headless head reads ` ● 2 calls`, and the cap line `… 5 earlier calls`;
  - `TestRenderDividerOnLabelledBeat` becomes `TestLabelledBeatCarriesTheTodoTag`;
  - new `TestTodoTagIsTheLastMetaToDrop`.
- `steps_test.go`: `TestThinkingRowLivesInsideTheStep` becomes `TestThinkingTimeSitsInTheBeatHead`, and the narrated-step test checks `●` and `├ read`.
- `tasks_test.go`: `TestTodoDividerGolden` becomes `TestBeatRowsGolden`, the exact head, call and plan rows at 80 and 140 columns. The divider-order, no-todo and tight-join tests check tags and spine rows.
- Thinking-row tests target the beat through the new helper `beatWithThinking` (`nodes_helpers_test.go`):
  - `TestClickRegionsCoverThinkingAndAuditBlocks` becomes `TestClickRegionsCoverABeatThatOnlyReasoned`;
  - `TestMouseClickTogglesThinkingBlock` becomes `TestMouseClickOnABeatShowsItsReasoning`;
  - `TestClickThinkingBlockWhileDrilledIntoSubagent` becomes `TestClickBeatWhileDrilledIntoSubagent`;
  - `TestRefreshViewportUsesPerItemExpandForThinking` becomes `TestRefreshViewportShowsReasoningPerBeat`.
- Selection tests: the clicks use the beat's head line, `thinkKeyOf` becomes `readRowOf`, and `selectionFixtureCopy` is the new copy.
- Removed with the old renderers: the `renderToolGroup` tests, `TestToolGroupHeadIsTheUntruncatedCollapsedRow` and `TestPluralizeToolName`.
- The indent contract checks a beat instead of the old group, and the scrub test checks `renderJobRow`, without the divider.

**Code:**

**`internal/app/tui/beats_render_test.go`.** The head, the tag and the cap row.

```diff
diff --git a/internal/app/tui/beats_render_test.go b/internal/app/tui/beats_render_test.go
--- a/internal/app/tui/beats_render_test.go
+++ b/internal/app/tui/beats_render_test.go
@@ -38,13 +38,17 @@ func TestRenderHeadlessBeatHasNoInventedHeader(t *testing.T) {
 	if !strings.Contains(plain, "read f0") {
 		t.Fatalf("rows must still render:\n%s", plain)
 	}
+	// Its head counts its calls, so the spine still hangs off something.
+	if first := strings.TrimRight(strings.SplitN(plain, "\n", 2)[0], " "); first != " ● 2 calls" {
+		t.Fatalf("headless head = %q", first)
+	}
 }
 
 // Review focus 1: a long headless beat shows the last BeatRowCap rows.
 func TestRenderHeadlessBeatCapsRows(t *testing.T) {
 	out, _ := renderStep(beatNode("", viewmodel.BeatRowCap+5), &stepRenderCtx{}, 100, false)
 	plain := ansi.Strip(out)
-	if !strings.Contains(plain, "+5 earlier calls") {
+	if !strings.Contains(plain, "… 5 earlier calls") {
 		t.Fatalf("want a collapsed-count line:\n%s", plain)
 	}
 	if strings.Contains(plain, "read f0") || !strings.Contains(plain, fmt.Sprintf("read f%d", viewmodel.BeatRowCap+4)) {
@@ -52,12 +56,38 @@ func TestRenderHeadlessBeatCapsRows(t *testing.T) {
 	}
 }
 
-func TestRenderDividerOnLabelledBeat(t *testing.T) {
+// The beat that starts a todo carries its tag at the right edge of its head,
+// and no divider: the todo's text lives in the plan.
+func TestLabelledBeatCarriesTheTodoTag(t *testing.T) {
 	n := beatNode("Parser first.", 1)
 	n.Step.Todo = &viewmodel.TodoLabel{ID: "p", Index: 3, Total: 9, Content: "AccessibleModal initialFocus"}
 	out, _ := renderStep(n, &stepRenderCtx{}, 80, false)
-	first := strings.SplitN(ansi.Strip(out), "\n", 2)[0]
-	if !strings.Contains(first, "3/9 AccessibleModal initialFocus") || !strings.HasPrefix(strings.TrimSpace(first), "──") {
-		t.Fatalf("divider = %q", first)
+	plain := ansi.Strip(out)
+	first := strings.SplitN(plain, "\n", 2)[0]
+	if !strings.HasPrefix(first, " ● Parser first.") || !strings.HasSuffix(first, "▸ 3/9") || len([]rune(first)) != 80 {
+		t.Fatalf("head = %q", first)
+	}
+	if strings.Contains(plain, "AccessibleModal") || strings.Contains(plain, "──") {
+		t.Fatalf("no divider:\n%s", plain)
+	}
+}
+
+// Under width pressure the meta drops the model, the duration and the
+// thinking time before the tag; the tag goes last.
+func TestTodoTagIsTheLastMetaToDrop(t *testing.T) {
+	p := metaParts{owner: "reviewer #1", role: "reviewer", model: "gpt-5", thought: "thought 2s", dur: "1m 4s", tag: "▸ 2/4"}
+	full, _ := rightMeta(p, 40, 200)
+	if got := stripANSI(full); got != "reviewer #1 · gpt-5 · thought 2s · 1m 4s · ▸ 2/4" {
+		t.Fatalf("full meta = %q", got)
+	}
+	for _, width := range []int{70, 60, 50, 45} {
+		text, _ := rightMeta(p, 40, width)
+		if got := stripANSI(text); got != "" && !strings.HasSuffix(got, "▸ 2/4") {
+			t.Errorf("width %d dropped the tag before the rest: %q", width, got)
+		}
+	}
+	tight, _ := rightMeta(p, 40, gutterWidth+2+24+5)
+	if got := stripANSI(tight); got != "▸ 2/4" {
+		t.Errorf("the tag alone survives the tightest fit, got %q", got)
 	}
 }
```

**`internal/app/tui/cards_render_test.go`.** `renderJobRow`.

```diff
diff --git a/internal/app/tui/cards_render_test.go b/internal/app/tui/cards_render_test.go
--- a/internal/app/tui/cards_render_test.go
+++ b/internal/app/tui/cards_render_test.go
@@ -16,14 +16,14 @@ func TestJobCardIsOneLineAndSettles(t *testing.T) {
 	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"cd /w && pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40"}`), ResultSummary: "started background job job-4"}
 	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
 		Job: &viewmodel.JobState{ID: "job-4", Command: "pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40", Running: true}}
-	out := ansi.Strip(renderJobCard(row, "⠋", time.Now(), false, 60, ""))
+	out := ansi.Strip(renderJobRow(row, "⠋", time.Now(), false, 60, ""))
 	if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 || !strings.Contains(out, "job-4") || !strings.Contains(out, "running") {
 		t.Fatalf("running card = %q", out)
 	}
 	row.Job.Running, row.Job.ExitCode, row.Job.Duration = false, 1, 122*time.Second
 	exit := 1
 	row.Job.Exit = &exit
-	out = ansi.Strip(renderJobCard(row, "", time.Now(), false, 60, ""))
+	out = ansi.Strip(renderJobRow(row, "", time.Now(), false, 60, ""))
 	if !strings.Contains(out, "exit 1") || !strings.Contains(out, "2m") {
 		t.Fatalf("settled card = %q", out)
 	}
@@ -96,7 +96,7 @@ func TestJobCardDistinguishesOutcomeStates(t *testing.T) {
 		t.Run(tt.name, func(t *testing.T) {
 			job := tt.job
 			row := &viewmodel.Node{Kind: viewmodel.KindTool, Job: &job}
-			out := ansi.Strip(renderJobCard(row, "⠋", time.Now(), false, 80, ""))
+			out := ansi.Strip(renderJobRow(row, "⠋", time.Now(), false, 80, ""))
 			if strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
 				t.Fatalf("card is not one line:\n%s", out)
 			}
@@ -120,12 +120,12 @@ func TestJobCardFlagsTruncatedOutput(t *testing.T) {
 	zero := 0
 	job := viewmodel.JobState{ID: "job-1", ExitCode: 0, Exit: &zero, Status: "completed", Truncated: true}
 	row := &viewmodel.Node{Kind: viewmodel.KindTool, Job: &job}
-	if out := ansi.Strip(renderJobCard(row, "", time.Now(), false, 80, "")); !strings.Contains(out, "truncated") {
+	if out := ansi.Strip(renderJobRow(row, "", time.Now(), false, 80, "")); !strings.Contains(out, "truncated") {
 		t.Fatalf("card = %q, want the cap called out", out)
 	}
 	job.Truncated = false
 	row.Job = &job
-	if out := ansi.Strip(renderJobCard(row, "", time.Now(), false, 80, "")); strings.Contains(out, "truncated") {
+	if out := ansi.Strip(renderJobRow(row, "", time.Now(), false, 80, "")); strings.Contains(out, "truncated") {
 		t.Fatalf("card = %q, want no cap note without one", out)
 	}
 }
@@ -182,7 +182,7 @@ func TestJobCardElidesTheWorktree(t *testing.T) {
 		ResultSummary: "started background job job-4"}
 	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
 		Job: &viewmodel.JobState{ID: "job-4", Command: "cd " + root + " && pytest -q", Running: true}}
-	out := ansi.Strip(renderJobCard(row, "⠋", time.Now(), false, 100, root))
+	out := ansi.Strip(renderJobRow(row, "⠋", time.Now(), false, 100, root))
 	if !strings.Contains(out, "⌂ pytest -q") || strings.Contains(out, root) {
 		t.Fatalf("card = %q", out)
 	}
```

**`internal/app/tui/click_test.go`.** Beats instead of thinking rows.

```diff
diff --git a/internal/app/tui/click_test.go b/internal/app/tui/click_test.go
--- a/internal/app/tui/click_test.go
+++ b/internal/app/tui/click_test.go
@@ -15,7 +15,7 @@ import (
 	"marshal/internal/viewmodel"
 )
 
-func TestClickRegionsCoverThinkingAndAuditBlocks(t *testing.T) {
+func TestClickRegionsCoverABeatThatOnlyReasoned(t *testing.T) {
 	m := newTestModel(t)
 	m.state.AddMessage(session.RoleUser, "hi", session.ContentTypePlain)
 	ts1 := time.Now()
@@ -23,15 +23,12 @@ func TestClickRegionsCoverThinkingAndAuditBlocks(t *testing.T) {
 	m.invalidateTranscript()
 	m.refreshViewport()
 
-	found := false
-	if r, ok := regionOf(&m, thinkID(ts1)); ok {
-		found = true
-		if r.startLine < 0 || r.endLine <= r.startLine {
-			t.Fatalf("invalid region for thinking block: %+v", r)
-		}
+	r, ok := regionOf(&m, beatWithThinking(&m, ts1))
+	if !ok {
+		t.Fatal("expected a click region for the beat holding the reasoning")
 	}
-	if !found {
-		t.Fatal("expected a click region for the logged thinking item")
+	if r.startLine < 0 || r.endLine <= r.startLine {
+		t.Fatalf("invalid region for the beat: %+v", r)
 	}
 }
 
@@ -96,7 +93,7 @@ func clickAt(t *testing.T, m tea.Model, x, y int) tea.Model {
 	return updated
 }
 
-func TestMouseClickTogglesThinkingBlock(t *testing.T) {
+func TestMouseClickOnABeatShowsItsReasoning(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(80, 24)
 	ts := time.Unix(700, 0)
@@ -104,10 +101,13 @@ func TestMouseClickTogglesThinkingBlock(t *testing.T) {
 	m.invalidateTranscript()
 	m.refreshViewport()
 
-	key := thinkID(ts)
+	key := beatWithThinking(&m, ts)
 	region, found := regionOf(&m, key)
 	if !found {
-		t.Fatal("expected a click region for the thinking block")
+		t.Fatal("expected a click region for the beat")
+	}
+	if strings.Contains(m.viewport.GetContent(), "click me") {
+		t.Fatal("a collapsed beat shows its thinking time, not its reasoning")
 	}
 
 	top := m.scrollHintRows()
@@ -116,7 +116,7 @@ func TestMouseClickTogglesThinkingBlock(t *testing.T) {
 	mm := asModel(t, updated)
 
 	if !mm.isExpanded(key) {
-		t.Fatal("expected the click to expand the thinking block")
+		t.Fatal("expected the click to expand the beat")
 	}
 	if !strings.Contains(mm.viewport.GetContent(), "click me") {
 		t.Fatal("expected the reasoning text to be visible after the click")
```

**`internal/app/tui/drill_test.go`.** Beats instead of thinking rows.

```diff
diff --git a/internal/app/tui/drill_test.go b/internal/app/tui/drill_test.go
--- a/internal/app/tui/drill_test.go
+++ b/internal/app/tui/drill_test.go
@@ -411,12 +411,12 @@ func TestUpArrowRecallsHistoryWhenNotDrilled(t *testing.T) {
 	}
 }
 
-// TestClickThinkingBlockWhileDrilledIntoSubagent verifies the off-by-one
+// TestClickBeatWhileDrilledIntoSubagent verifies the off-by-one
 // fix in contentLineForClick: when drilled into a subagent, a breadcrumb
 // row is prepended above the viewport, so the click offset must include
 // breadcrumbRows() in addition to scrollHintRows(). Without the fix, every
 // click while drilled lands one row too high and fails to hit its region.
-func TestClickThinkingBlockWhileDrilledIntoSubagent(t *testing.T) {
+func TestClickBeatWhileDrilledIntoSubagent(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(80, 24)
 
@@ -436,11 +436,11 @@ func TestClickThinkingBlockWhileDrilledIntoSubagent(t *testing.T) {
 		t.Fatalf("breadcrumbRows = %d, want 1 while drilled in", m.breadcrumbRows())
 	}
 
-	// Find the click region for the child's thinking block.
-	key := thinkID(ts)
+	// Find the click region for the child's beat that reasoned.
+	key := beatWithThinking(&m, ts)
 	region, found := regionOf(&m, key)
 	if !found {
-		t.Fatal("expected a click region for the child's thinking block while drilled in")
+		t.Fatal("expected a click region for the child's beat while drilled in")
 	}
 
 	// Compute the screen Y using the FIXED offset (scrollHintRows + breadcrumbRows).
@@ -451,7 +451,7 @@ func TestClickThinkingBlockWhileDrilledIntoSubagent(t *testing.T) {
 	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 	if !mm.isExpanded(key) {
-		t.Fatal("expected the click to expand the child's thinking block while drilled in (off-by-one fix)")
+		t.Fatal("expected the click to expand the child's beat while drilled in (off-by-one fix)")
 	}
 	if !strings.Contains(stripANSI(mm.viewport.GetContent()), "child reasoning") {
 		t.Fatal("expected the reasoning text to be visible after the click")
```

**`internal/app/tui/expand_test.go`.** Reasoning per beat.

```diff
diff --git a/internal/app/tui/expand_test.go b/internal/app/tui/expand_test.go
--- a/internal/app/tui/expand_test.go
+++ b/internal/app/tui/expand_test.go
@@ -1,6 +1,7 @@
 package tui
 
 import (
+	"fmt"
 	"strings"
 	"testing"
 	"time"
@@ -117,21 +118,27 @@ func TestExpandingABeatOpensItsRows(t *testing.T) {
 	}
 }
 
-func TestRefreshViewportUsesPerItemExpandForThinking(t *testing.T) {
+// A beat's reasoning shows when that beat is expanded, and only that beat's.
+func TestRefreshViewportShowsReasoningPerBeat(t *testing.T) {
 	m := newTestModel(t)
 	ts1 := time.Unix(300, 0)
 	ts2 := time.Unix(301, 0)
-	m.state.LogThinking(session.ThinkingEntry{Text: "reasoning one", Duration: time.Second, StartedAt: ts1})
-	m.state.LogThinking(session.ThinkingEntry{Text: "reasoning two", Duration: time.Second, StartedAt: ts2})
+	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
+	for i, ts := range []time.Time{ts1, ts2} {
+		id := m.state.BeginStep(session.Actor{})
+		m.state.AddNarration(id, fmt.Sprintf("Beat %d.", i+1))
+		m.state.LogThinking(session.ThinkingEntry{Text: []string{"reasoning one", "reasoning two"}[i], Duration: time.Second, StartedAt: ts, StepID: id})
+		m.state.EndStep(id)
+	}
 	m.invalidateTranscript()
 	m.refreshViewport()
 
 	content := m.viewport.GetContent()
 	if strings.Contains(content, "reasoning one") || strings.Contains(content, "reasoning two") {
-		t.Fatalf("expected both thinking blocks collapsed by default, got: %s", content)
+		t.Fatalf("expected both beats' reasoning collapsed by default, got: %s", content)
 	}
 
-	m.toggleExpanded(thinkID(ts1))
+	m.toggleExpanded(beatWithThinking(&m, ts1))
 	m.invalidateTranscript()
 	m.refreshViewport()
 
```

**`internal/app/tui/glyph/glyph.go`.** `Beat` and the spine glyphs.

```diff
diff --git a/internal/app/tui/glyph/glyph.go b/internal/app/tui/glyph/glyph.go
--- a/internal/app/tui/glyph/glyph.go
+++ b/internal/app/tui/glyph/glyph.go
@@ -64,6 +64,17 @@ const (
 	// Brand is the startup banner mark, and marks a bound preset or the
 	// currently-selected item in a picker badge.
 	Brand = "●"
+	// Beat marks a settled beat in the transcript: one narrated response and
+	// the calls under it. It shares Brand's shape, which never appears in the
+	// transcript.
+	Beat = Brand
+
+	// SpineMid, SpineEnd and Spine draw a beat's spine: each call row hangs
+	// off it with SpineMid, the last with SpineEnd, and Spine carries it past
+	// an expanded row's detail. They mean "this belongs to the beat above".
+	SpineMid = "├"
+	SpineEnd = "└"
+	Spine    = "│"
 	// CustomAgent marks a cast entry bound to a user-defined agent.
 	CustomAgent = "◆"
 
```

**`internal/app/tui/indent_contract_test.go`.** A beat instead of the old group.

```diff
diff --git a/internal/app/tui/indent_contract_test.go b/internal/app/tui/indent_contract_test.go
--- a/internal/app/tui/indent_contract_test.go
+++ b/internal/app/tui/indent_contract_test.go
@@ -1,7 +1,6 @@
 package tui
 
 import (
-	"encoding/json"
 	"strings"
 	"testing"
 	"time"
@@ -75,12 +74,10 @@ func TestTranscriptIndentContract(t *testing.T) {
 		}, false, nil, width)},
 		{"thinking summary", renderThinkingSummary("reasoned", time.Second, false, width)},
 		{"agent markdown", renderAgentMarkdown("plain paragraph", width)},
-		{"tool group with wrapped bullets", renderToolGroup([]registry.AuditEvent{
-			{ToolName: "file.read", ResultSummary: "read the entire contents of this very long file to understand the structure",
-				Args: json.RawMessage(`{"path":"internal/app/tui/transcript_render_pipeline_quite_long.go"}`)},
-			{ToolName: "file.read", ResultSummary: "ok",
-				Args: json.RawMessage(`{"path":"internal/app/tui/another_rather_long_runner_implementation_file.go"}`)},
-		}, false, width)},
+		{"beat with calls on its spine", func() string {
+			out, _ := renderStep(beatNode("Reading the parser.", 3), &stepRenderCtx{}, width, false)
+			return out
+		}()},
 	}
 
 	for _, tc := range cases {
```

**`internal/app/tui/model_test.go`.** Verbs.

```diff
diff --git a/internal/app/tui/model_test.go b/internal/app/tui/model_test.go
--- a/internal/app/tui/model_test.go
+++ b/internal/app/tui/model_test.go
@@ -3154,8 +3154,8 @@ func TestRecentToolCallRendersInlineAfterFastToolCompletes(t *testing.T) {
 	m.refreshViewport()
 	view := stripANSI(m.View().Content)
 
-	if !strings.Contains(view, "Read file") {
-		t.Fatalf("View() does not show recent tool name:\n%s", view)
+	if !strings.Contains(view, "read    ") {
+		t.Fatalf("View() does not show recent tool's verb:\n%s", view)
 	}
 	if !strings.Contains(view, "/repo/main.go") {
 		t.Fatalf("View() does not show recent tool summary:\n%s", view)
@@ -3187,7 +3187,7 @@ func TestCompletedToolCallsRemainInTranscriptLog(t *testing.T) {
 	m.refreshViewport()
 	view := stripANSI(m.View().Content)
 
-	for _, want := range []string{"Read file", "/repo/main.go", "go test ./...", "exit 0"} {
+	for _, want := range []string{"read    ", "/repo/main.go", "go test ./...", "exit 0"} {
 		if !strings.Contains(view, want) {
 			t.Fatalf("View() missing persistent tool log item %q:\n%s", want, view)
 		}
@@ -3219,7 +3219,7 @@ func TestCompletedToolCallsRenderInMessageTimeline(t *testing.T) {
 	view := stripANSI(m.View().Content)
 
 	userIdx := strings.Index(view, "USER: inspect auth flow")
-	toolIdx := strings.Index(view, "Read file")
+	toolIdx := strings.Index(view, "read    ")
 	assistantIdx := strings.Index(view, "ASSISTANT: auth flow summary")
 	if userIdx == -1 || toolIdx == -1 || assistantIdx == -1 {
 		t.Fatalf("view missing timeline entries:\n%s", view)
```

**`internal/app/tui/nodes_helpers_test.go`.** `beatWithThinking`.

```diff
diff --git a/internal/app/tui/nodes_helpers_test.go b/internal/app/tui/nodes_helpers_test.go
--- a/internal/app/tui/nodes_helpers_test.go
+++ b/internal/app/tui/nodes_helpers_test.go
@@ -13,6 +13,22 @@ func thinkID(ts time.Time) viewmodel.NodeID {
 	return viewmodel.ThinkingID(&session.ThinkingEntry{StartedAt: ts})
 }
 
+// beatWithThinking is the beat holding the reasoning that started at ts, from
+// the last refresh. Reasoning is part of its beat: expanding the beat shows it.
+func beatWithThinking(m *Model, ts time.Time) viewmodel.NodeID {
+	for id, n := range m.browseTree {
+		if n.Kind != viewmodel.KindStep || n.Step == nil {
+			continue
+		}
+		for _, th := range n.Step.Thinking {
+			if th.StartedAt.Equal(ts) {
+				return id
+			}
+		}
+	}
+	return viewmodel.NodeID{}
+}
+
 // toolIDAt is the node ID of an audit row with no call ID, logged at ts.
 func toolIDAt(ts time.Time) viewmodel.NodeID {
 	return viewmodel.ToolID(registry.AuditEvent{Timestamp: ts})
```

**`internal/app/tui/scrub_render_test.go`.** No divider; `renderJobRow`.

```diff
diff --git a/internal/app/tui/scrub_render_test.go b/internal/app/tui/scrub_render_test.go
--- a/internal/app/tui/scrub_render_test.go
+++ b/internal/app/tui/scrub_render_test.go
@@ -64,19 +64,16 @@ func TestRenderersDropTerminalControlSequences(t *testing.T) {
 	marker := renderMarker(&viewmodel.MarkerInfo{Labels: []string{"#3 " + esc}}, 80)
 	stray("marker", marker)
 
-	divider := renderTodoDivider(&viewmodel.TodoLabel{Index: 3, Total: 9, Content: esc + "AccessibleModal"}, 80)
-	stray("divider", divider)
-
 	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"pytest -q"}`),
 		ResultSummary: "started background job job-4"}
 	row := &viewmodel.Node{Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev},
 		Job: &viewmodel.JobState{ID: "job-4", Command: "echo " + esc, Running: true}}
-	stray("job card", renderJobCard(row, "⠋", time.Now(), false, 80, ""))
+	stray("job card", renderJobRow(row, "⠋", time.Now(), false, 80, ""))
 
 	// The expanded body is the job's own output: escaping it must not cost it
 	// its lines.
 	row.Job.Running, row.Job.Output = false, "line one\n"+esc+"\nline three"
-	expanded := renderJobCard(row, "", time.Now(), true, 80, "")
+	expanded := renderJobRow(row, "", time.Now(), true, 80, "")
 	stray("expanded job output", expanded)
 	if !strings.Contains(expanded, "line three") {
 		t.Fatalf("the output's own lines must survive scrubbing: %q", expanded)
@@ -95,7 +92,7 @@ func TestRenderersDropTerminalControlSequences(t *testing.T) {
 func TestJobCardToleratesAJobWithNoRunAttached(t *testing.T) {
 	row := &viewmodel.Node{Kind: viewmodel.KindTool,
 		Job: &viewmodel.JobState{ID: "job-9", Command: "pytest -q", Running: true}}
-	out := renderJobCard(row, "⠋", time.Now(), false, 80, "")
+	out := renderJobRow(row, "⠋", time.Now(), false, 80, "")
 	if !strings.Contains(out, "job-9") || !strings.Contains(out, "running") {
 		t.Fatalf("card = %q", out)
 	}
```

**`internal/app/tui/selection.go`.** `fullToolHead` returns the subject.

```diff
diff --git a/internal/app/tui/selection.go b/internal/app/tui/selection.go
--- a/internal/app/tui/selection.go
+++ b/internal/app/tui/selection.go
@@ -166,24 +166,27 @@ func (m *Model) selRowAt(l int) selRow {
 	return r
 }
 
-// fullToolHead is the untruncated text of a collapsed tool row whose text
-// ends in the truncation ellipsis, or "".
+// fullToolHead is the untruncated subject of a call row whose subject was
+// truncated, or "": the part of the row a selection copies.
 func (m *Model) fullToolHead(id viewmodel.NodeID, text string) string {
-	if id.Kind != viewmodel.KindTool || !strings.HasSuffix(strings.TrimRight(text, " "), "…") {
+	if id.Kind != viewmodel.KindTool || !strings.Contains(text, "…") {
 		return ""
 	}
 	n := m.browseTree[id]
 	if n == nil {
 		return ""
 	}
-	switch {
-	case len(n.Tools) == 1:
-		state, _ := m.transcriptSource()
-		return oneLine(toolRowHead(n.Tools[0], state.Workspace().ActiveRoot))
-	case len(n.Tools) > 1:
-		return toolGroupHead(n.Tools)
+	if len(n.Tools) == 0 {
+		return ""
+	}
+	state, _ := m.transcriptSource()
+	subjects := make([]string, 0, len(n.Tools))
+	for _, ev := range n.Tools {
+		if s := callSubject(ev, state.Workspace().ActiveRoot); s != "" {
+			subjects = append(subjects, s)
+		}
 	}
-	return ""
+	return strings.Join(subjects, ", ")
 }
 
 // selectedText is the cleaned text between two content positions.
```

**`internal/app/tui/selection_input_test.go`.** Click the beat's head.

```diff
diff --git a/internal/app/tui/selection_input_test.go b/internal/app/tui/selection_input_test.go
--- a/internal/app/tui/selection_input_test.go
+++ b/internal/app/tui/selection_input_test.go
@@ -46,17 +46,17 @@ func TestDragCopiesTheRenderedTranscript(t *testing.T) {
 
 func TestPressAloneDoesNotToggle(t *testing.T) {
 	m, _ := selectionFixture(t)
-	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
+	y := screenY(&m, lineOf(t, &m, "Looking at the failing test."))
 	next, _ := m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
 	mm := asModel(t, next)
 	if strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
-		t.Fatal("a press alone expanded the thinking row; clicks act on release")
+		t.Fatal("a press alone expanded the beat; clicks act on release")
 	}
 }
 
 func TestClickTogglesOnRelease(t *testing.T) {
 	m, _ := selectionFixture(t)
-	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
+	y := screenY(&m, lineOf(t, &m, "Looking at the failing test."))
 	mm := asModel(t, clickAt(t, m, 8, y))
 	if !strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
 		t.Fatal("press and release on one cell did not toggle the row")
@@ -248,7 +248,7 @@ func TestReleaseStopsEdgeAutoScroll(t *testing.T) {
 
 func TestReleaseOutsideTheViewportEndsTheDragWithoutClicking(t *testing.T) {
 	m, clip := selectionFixture(t)
-	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
+	y := screenY(&m, lineOf(t, &m, "Looking at the failing test."))
 	next, _ := m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
 	next, _ = next.Update(tea.MouseMotionMsg{X: 10, Y: y + 1, Button: tea.MouseLeft})
 	mm := asModel(t, next)
@@ -268,8 +268,8 @@ func TestReleaseOutsideTheViewportEndsTheDragWithoutClicking(t *testing.T) {
 	if len(clip.writes) != 1 {
 		t.Fatalf("clipboard writes = %d, want the drag's copy on release", len(clip.writes))
 	}
-	// A click on this row would have expanded the thinking row; a drag's
-	// release must not act as one.
+	// A click on this row would have expanded the beat and shown its
+	// reasoning; a drag's release must not act as one.
 	if strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
 		t.Fatal("the release outside the viewport acted as a click on the press row")
 	}
@@ -281,7 +281,7 @@ func TestClickRoutesThroughScrollTranscriptToo(t *testing.T) {
 	// scrollTranscript. The press and release there share the same drag
 	// state as the direct path, so a click must still act on release.
 	m, _ := selectionFixture(t)
-	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
+	y := screenY(&m, lineOf(t, &m, "Looking at the failing test."))
 	next, cmd, handled := m.scrollTranscript(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
 	if !handled || cmd != nil {
 		t.Fatalf("press through scrollTranscript: handled=%v cmd=%v", handled, cmd)
```

**`internal/app/tui/selection_test.go`.** The read row, and the new copy.

```diff
diff --git a/internal/app/tui/selection_test.go b/internal/app/tui/selection_test.go
--- a/internal/app/tui/selection_test.go
+++ b/internal/app/tui/selection_test.go
@@ -68,15 +68,12 @@ Looking at the failing test. Then:
       return
   }
 
-thought for 2s
-Read file · a.go (5 lines)
-Search texts: ×2 · Timeout, hookTimeout
-
-go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v -timeout 30s · exit 1 · exit status 1
+a.go
+Timeout, hookTimeout
+go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v -timeout 30s
 --- FAIL: TestX (0.01s)
     hooks_test.go:12: timeout
 FAIL
-
 reviewer
 Tracing the config loader.
 
@@ -109,13 +106,13 @@ func selectRows(t *testing.T, m *Model, from, to selPos) {
 	m.sel.copied = m.selectedText(f, l)
 }
 
-// thinkKeyOf returns the node of the fixture's thinking row.
-func thinkKeyOf(t *testing.T, m *Model) viewmodel.NodeID {
+// readRowOf returns the node of the fixture's file.read row.
+func readRowOf(t *testing.T, m *Model) viewmodel.NodeID {
 	t.Helper()
-	l := lineOf(t, m, "thought for 2s")
+	l := lineOf(t, m, "read    a.go")
 	target, ok := m.regionAt(l)
 	if !ok {
-		t.Fatal("no region on the thinking row")
+		t.Fatal("no region on the read row")
 	}
 	return target.node
 }
@@ -147,8 +144,8 @@ func TestSelectionFollowsItsTextWhenRowsChangeAbove(t *testing.T) {
 	l := lineOf(t, &m, "Model switched")
 	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
 	copied := m.sel.copied
-	// Expanding the thinking row adds rows above the selection.
-	m.toggleExpanded(thinkKeyOf(t, &m))
+	// Expanding the read row adds rows above the selection.
+	m.toggleExpanded(readRowOf(t, &m))
 	m.invalidateTranscript()
 	m.refreshViewport()
 	if m.sel == nil {
@@ -162,13 +159,15 @@ func TestSelectionFollowsItsTextWhenRowsChangeAbove(t *testing.T) {
 
 func TestSelectionClearsWhenItsTextChanges(t *testing.T) {
 	m, _ := selectionFixture(t)
-	l := lineOf(t, &m, "thought for 2s")
-	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
-	m.toggleExpanded(thinkKeyOf(t, &m))
+	// The read row and the row after it: expanding the read row puts its
+	// result between them, so the selected text changes.
+	l := lineOf(t, &m, "read    a.go")
+	selectRows(t, &m, selPos{l, 0}, selPos{l + 1, 79})
+	m.toggleExpanded(readRowOf(t, &m))
 	m.invalidateTranscript()
 	m.refreshViewport()
 	if m.sel != nil {
-		t.Fatal("expanding the selected row must clear the selection")
+		t.Fatal("expanding a selected row must clear the selection")
 	}
 }
 
```

**`internal/app/tui/selection_text.go`.** Spine glyphs and verbs are gutter.

```diff
diff --git a/internal/app/tui/selection_text.go b/internal/app/tui/selection_text.go
--- a/internal/app/tui/selection_text.go
+++ b/internal/app/tui/selection_text.go
@@ -64,7 +64,8 @@ func isGutterGlyph(s string) bool {
 	case glyph.Rail, glyph.User, glyph.Running, glyph.Ambient, glyph.OK,
 		glyph.Error, glyph.Warning, glyph.Question, glyph.Thinking, glyph.Edit,
 		glyph.File, glyph.Shell, glyph.Search, glyph.Agent, glyph.Web,
-		glyph.Brand, glyph.CustomAgent, glyph.Job, glyph.Watch:
+		glyph.Brand, glyph.CustomAgent, glyph.Job, glyph.Watch,
+		glyph.SpineMid, glyph.SpineEnd, glyph.Spine:
 		return true
 	}
 	return slices.Contains(spinnerFrames, s)
@@ -85,12 +86,32 @@ func spanOf(r selRow, width int) rowSpan {
 	sp := rowSpan{bare: true}
 	if i < len(runes) && col < maxGutterCols && isGutterGlyph(string(runes[i])) &&
 		(i+1 == len(runes) || runes[i+1] == ' ') {
-		col += ansi.StringWidth(string(runes[i]))
+		g := string(runes[i])
+		col += ansi.StringWidth(g)
 		i++
 		if i < len(runes) {
 			i++ // the space after the glyph
 			col++
 		}
+		// A call row's verb is part of its gutter, and so is the verb column
+		// on the spine's continuation rows: copying a row copies what the
+		// call acted on, and its detail lines up under it.
+		switch g {
+		case glyph.SpineMid, glyph.SpineEnd:
+			for i < len(runes) && runes[i] != ' ' {
+				col += ansi.StringWidth(string(runes[i]))
+				i++
+			}
+			for i < len(runes) && runes[i] == ' ' {
+				col++
+				i++
+			}
+		case glyph.Spine:
+			for n := 0; n < verbWidth && i < len(runes) && runes[i] == ' '; n++ {
+				col++
+				i++
+			}
+		}
 		sp.bare = false
 		sp.start = col
 	}
```

**New file `internal/app/tui/spine.go`.** 

```go
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// The spine ties a beat's calls to the narration above them. Each row hangs
// off the beat's head: ├ for every row but the last, └ for the last, and │
// carrying the spine past an expanded row's detail. A row leads with a verb
// in a fixed column, then its subject, with its result right-aligned:
//
//	● Pointing the eval config at qwen3-coder.             thought 2s · ▸ 2/4
//	  ├ edit    evals/config.toml                                     +3 −3
//	  └ agent   review the config change                       ⧉ #1 started

const (
	// spineWidth is where a row's text starts: the connector sits under the
	// narration's first letter, followed by one space.
	spineWidth = gutterWidth + 2
	// verbWidth is the verb column: the longest verb ("search") and two
	// spaces. A row's detail hangs at the same indent, under its subject.
	verbWidth = 8
	// liveTailLines is how many lines of a running call's output show while
	// it is collapsed.
	liveTailLines = 6
	// maxPlanMarks is the longest list the plan line draws item by item; a
	// longer one draws the plan blocks instead.
	maxPlanMarks = 12
)

// callVerbs maps a tool-name prefix to the verb its row leads with. The first
// matching prefix wins, so write tools come before the broader file prefix.
var callVerbs = []struct{ prefix, verb string }{
	{"file.read", "read"},
	{"file.write_patch", "edit"},
	{"file.edit", "edit"},
	{"patch.", "edit"},
	{"file.write", "write"},
	{"shell.", "shell"},
	{"test.", "shell"},
	{"repo.search", "search"},
	{"codebase.search", "search"},
	{"codebase_search", "search"},
	{"search.", "search"},
	{"symbols", "search"},
	{"lsp.", "read"},
	{"agent.", "agent"},
	{"web.", "web"},
	{"browser.", "web"},
	{"todo.write", "plan"},
	{"mcp.", "mcp"},
}

// callVerb is the verb a call's row leads with. A tool outside the table
// uses the first segment of its name ("git.status" reads "git").
func callVerb(name string) string {
	for _, v := range callVerbs {
		if strings.HasPrefix(name, v.prefix) {
			return v.verb
		}
	}
	first, _, _ := strings.Cut(name, ".")
	return first
}

// fallbackSubject is a row's subject when the call's arguments name none:
// the rest of the tool's name after its first segment ("git.status" reads
// "git status", "agent.await" reads "agent await"), unless that only repeats
// the verb ("file.read" reads "read", not "read read").
func fallbackSubject(name string) string {
	_, rest, ok := strings.Cut(name, ".")
	if !ok || rest == callVerb(name) {
		return ""
	}
	return strings.ReplaceAll(rest, ".", " ")
}

// toolTarget returns the short human-facing subject of a tool call — the
// path it read, the command it ran, the query it searched for.
func toolTarget(event registry.AuditEvent) string { return viewmodel.ToolTarget(event) }

// callSubject is what a settled call acted on: the command it ran, the file
// or symbols it touched, the query it searched for, or the task it handed to
// a subagent. It is model-authored text, so it is scrubbed here.
func callSubject(ev registry.AuditEvent, workdir string) string {
	var s string
	switch {
	case viewmodel.IsShellFamily(ev.ToolName):
		s = viewmodel.ElideWorkdir(toolTarget(ev), workdir)
	case ev.ToolName == "agent.run":
		s = argString(ev.Args, "description")
	default:
		if sym := symbolSubject(ev); sym != "" && subjectFirstTool(ev.ToolName) {
			s = sym
		} else if q, ok := searchQualifier(ev.ToolName); ok {
			if t := searchTarget(ev); t != "" {
				s = strconv.Quote(t)
				// The verb already says "search"; any other qualifier tells
				// two kinds of search apart.
				if q != "search" {
					s = q + " " + s
				}
			}
		} else {
			s = toolTarget(ev)
		}
	}
	if s == "" {
		s = fallbackSubject(ev.ToolName)
	}
	return viewmodel.ScrubText(firstLine(s))
}

// callResult is a settled call's right-aligned result, styled: its error, a
// command's exit and duration, an edit's diff stat, the subagent it started,
// or its summary. A hook's decision leads.
func callResult(ev registry.AuditEvent) string {
	var parts []string
	style := mutedStyle()
	if h := hookIndicatorText(ev.Hooks); h != "" {
		parts = append(parts, h)
	}
	switch {
	case ev.Error != "":
		parts = append(parts, firstLine(ev.Error))
		style = errorStyle()
	case viewmodel.IsShellFamily(ev.ToolName) && ev.CommandExitCode != nil && ev.Sandbox.KilledReason == "":
		parts = append(parts, fmt.Sprintf("exit %d", *ev.CommandExitCode))
		if ev.Duration > 0 {
			parts = append(parts, formatElapsed(ev.Duration))
		}
		if *ev.CommandExitCode != 0 {
			style = errorStyle()
		}
	case isDiffTool(ev.ToolName) && diffStat(ev.ResultContent) != "":
		parts = append(parts, diffStat(ev.ResultContent))
	default:
		if id := startedSubagent(ev); id > 0 {
			started := lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary).Render(fmt.Sprintf("%s #%d", glyph.Agent, id)) +
				mutedStyle().Render(" started")
			if len(parts) > 0 {
				return mutedStyle().Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator))+dimSeparator) + started
			}
			return started
		}
		if s := viewmodel.DisplaySummary(ev); s != "" {
			parts = append(parts, firstLine(s))
		}
	}
	return style.Render(viewmodel.ScrubText(strings.Join(parts, dimSeparator)))
}

// startedSubagent is the subagent an agent.run call started in the
// background, 0 when it started none. The notice's structured reference is
// read first; a record that predates it falls back to the summary the tool
// wrote ("started as subagent 3 — …").
func startedSubagent(ev registry.AuditEvent) int64 {
	if ev.ToolName != "agent.run" || ev.Error != "" {
		return 0
	}
	if ref, ok := ev.Notice.SubagentWork(); ok {
		return ref.AgentID
	}
	var id int64
	if _, err := fmt.Sscanf(ev.ResultSummary, "started as subagent %d", &id); err == nil {
		return id
	}
	return 0
}

// spineHead lays out a row's first line in w cells: the verb in its column,
// the subject truncated to what the result leaves, and the styled result
// right-aligned. A result takes at most half the room after the verb.
func spineHead(verb, subject, result string, w int) string {
	v := verb + strings.Repeat(" ", max(verbWidth-ansi.StringWidth(verb), 1))
	room := max(w-ansi.StringWidth(v), 1)
	result = ansi.Truncate(result, max(room/2, 1), "…")
	rw := ansi.StringWidth(result)
	subRoom := room
	if rw > 0 {
		subRoom = max(room-rw-2, 1)
	}
	subject = ansi.Truncate(oneLine(subject), subRoom, "…")
	line := mutedStyle().Render(v) + lipgloss.NewStyle().Foreground(theme.Current().FGDefault).Render(subject)
	if rw > 0 {
		line += strings.Repeat(" ", max(room-ansi.StringWidth(subject)-rw, 2)) + result
	}
	return line
}

// spineBlock hangs a rendered row off the spine: the connector on its first
// line, the spine's continuation on the rest.
func spineBlock(block string, last bool) string {
	conn, cont := glyph.SpineMid, glyph.Spine
	if last {
		conn, cont = glyph.SpineEnd, " "
	}
	style := lipgloss.NewStyle().Foreground(dimColor)
	pad := strings.Repeat(" ", gutterWidth)
	var b strings.Builder
	for i, l := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		g := cont
		if i == 0 {
			g = conn
		}
		b.WriteString(pad + style.Render(g) + " " + l + "\n")
	}
	return b.String()
}

// detailLines indents a row's detail under its subject, one line each.
func detailLines(b *strings.Builder, lines []string) {
	pad := strings.Repeat(" ", verbWidth)
	for _, l := range lines {
		b.WriteString(pad + l + "\n")
	}
}

// renderCallRow is a settled call's row. Collapsed it is one line, plus the
// callers of a symbol it changed and, when it failed, the last lines of its
// output. Expanded, its diff, result or error follow under the subject.
func renderCallRow(ev registry.AuditEvent, expanded bool, callers []string, w int, workdir string) string {
	var b strings.Builder
	b.WriteString(spineHead(callVerb(ev.ToolName), callSubject(ev, workdir), callResult(ev), w) + "\n")
	dw := max(w-verbWidth, 1)
	detailLines(&b, callersDetail(callers, dw))
	switch {
	case expanded:
		detailLines(&b, toolBody(ev, true, dw))
	case viewmodel.EventFailed(ev):
		detailLines(&b, failedTail(ev, dw))
	}
	return b.String()
}

// failedTail is the last few lines of a failed call's output, shown under its
// collapsed row so the reason is visible without opening it. The tail is the
// useful end: a test run prints its failures last.
func failedTail(ev registry.AuditEvent, w int) []string {
	text := strings.TrimRight(ev.ResultContent, "\n")
	if text == "" {
		text = ev.Error
	}
	if text == "" {
		return nil
	}
	lines := strings.Split(viewmodel.ScrubBlock(text), "\n")
	if len(lines) > failedTailLines {
		lines = lines[len(lines)-failedTailLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, mutedStyle().Render(ansi.Truncate(strings.TrimRight(l, " \t\r"), w, "…")))
	}
	return out
}

// failedTailLines is how many output lines a failed row shows collapsed.
const failedTailLines = 3

// renderGroupRow is a merged run of same-tool calls: their subjects on one
// line and the count as the result. Expanded, one line per call follows with
// its subject and summary.
func renderGroupRow(evs []registry.AuditEvent, expanded bool, w int, workdir string) string {
	subjects := make([]string, 0, len(evs))
	for _, ev := range evs {
		if s := callSubject(ev, workdir); s != "" {
			subjects = append(subjects, s)
		}
	}
	var b strings.Builder
	b.WriteString(spineHead(callVerb(evs[0].ToolName), strings.Join(subjects, ", "), mutedStyle().Render(fmt.Sprintf("×%d", len(evs))), w) + "\n")
	if expanded {
		dw := max(w-verbWidth, 1)
		var lines []string
		for _, ev := range evs {
			line := callSubject(ev, workdir)
			if s := viewmodel.DisplaySummary(ev); s != "" {
				if line != "" {
					line += dimSeparator
				}
				line += viewmodel.ScrubText(firstLine(s))
			}
			lines = append(lines, mutedStyle().Render(ansi.Truncate(line, dw, "…")))
		}
		detailLines(&b, lines)
	}
	return b.String()
}

// renderJobRow is the row of a shell call that started a background job,
// settled in place by the job's exit: the command, then the job and its state
// as the result. Expanded, the job's output follows.
func renderJobRow(row *viewmodel.Node, spinner string, now time.Time, expanded bool, w int, workdir string) string {
	j := row.Job
	// The command is model-authored shell text, scrubbed here as well as at
	// the viewmodel boundary.
	cmd := viewmodel.ScrubText(firstLine(viewmodel.ElideWorkdir(j.Command, workdir)))
	id := lipgloss.NewStyle().Foreground(theme.Current().StatusInfo).Render(glyph.Job + " " + viewmodel.ScrubText(j.ID))
	state, style := jobState(row, spinner, now)
	var b strings.Builder
	b.WriteString(spineHead("shell", cmd, id+" "+style.Render(state), w) + "\n")
	if expanded && j.Output != "" {
		dw := max(w-verbWidth, 1)
		var lines []string
		// Scrubbed at the projection and again here, at this renderer's own
		// boundary; the session caps the output, so the second pass is cheap.
		for _, line := range strings.Split(strings.TrimRight(viewmodel.ScrubBlock(j.Output), "\n"), "\n") {
			for _, wl := range strings.Split(ansi.Wrap(line, dw, WrapBreakpoints), "\n") {
				lines = append(lines, mutedStyle().Render(wl))
			}
		}
		detailLines(&b, lines)
	}
	return b.String()
}

// jobState is a job row's state after its ID: running with its elapsed time,
// stopping, its outcome once settled, or why it has none.
func jobState(row *viewmodel.Node, spinner string, now time.Time) (string, lipgloss.Style) {
	j := row.Job
	var state string
	style := mutedStyle()
	switch {
	case j.Lost:
		// The session ended while the job ran: there is no exit code to
		// report, and the row must not claim one.
		state, style = "ended with session", warningStyle()
	case j.Status == string(native.StatusStopping):
		// The stop was accepted and the process is still going. Checked before
		// running: a stopping execution has no outcome yet.
		state, style = "stopping", warningStyle()
		if j.Duration > 0 {
			state += dimSeparator + formatElapsed(j.Duration)
		}
	case j.Running:
		state = "running"
		// JobStart, not Tools[0]: a caller-built tree can hold a running job
		// whose starting call is not attached, and the row still renders.
		if started := row.JobStart(); !started.IsZero() {
			state += dimSeparator + formatElapsed(max(now.Sub(started), 0))
		}
		if spinner != "" {
			state = spinner + " " + state
		}
	default:
		// Settled. An exit that was never observed is never success: a start-up
		// failure produces no code at all, and a legacy record spells "unknown"
		// as -1, so both render as unknown rather than as a clean exit.
		code := j.Exit
		if code == nil && j.ExitCode >= 0 {
			legacy := j.ExitCode
			code = &legacy
		}
		switch {
		case jobFailedStatus(j.Status) || (code != nil && *code != 0):
			style = errorStyle()
		case code == nil:
			style = warningStyle()
		}
		state = jobOutcomeWord(j, code)
		if j.Duration > 0 {
			state += dimSeparator + formatElapsed(j.Duration)
		}
	}
	if j.Truncated {
		state += dimSeparator + "truncated"
	}
	return state, style
}

// renderActiveCallRow is a call still running: what it acts on, never its
// raw arguments, with a spinner and its elapsed time as the result. A
// command's sandbox and the tail of its output follow; expanded, the whole
// captured output does.
func renderActiveCallRow(atc session.ActiveToolCall, c *stepRenderCtx, expanded bool, w int) string {
	subject := atc.Path
	if subject == "" {
		subject = firstLine(atc.Args)
	}
	if subject == "" {
		subject = fallbackSubject(atc.Name)
	}
	spin := c.toolSpinner
	if spin == "" {
		spin = spinnerFrames[0]
	}
	elapsed := formatElapsed(max(c.now.Sub(atc.StartedAt), 0))
	result := lipgloss.NewStyle().Foreground(accentColor).Render(spin + " " + elapsed)
	var b strings.Builder
	b.WriteString(spineHead(callVerb(atc.Name), viewmodel.ScrubText(subject), result, w) + "\n")
	dw := max(w-verbWidth, 1)
	var lines []string
	if viewmodel.IsShellFamily(atc.Name) {
		if iso := sandboxIsolationText(c.sandbox, c.allowNetwork); iso != "" {
			lines = append(lines, mutedStyle().Render(ansi.Truncate(iso, dw, "…")))
		}
	}
	if atc.Output != "" {
		out := strings.Split(strings.TrimRight(viewmodel.ScrubBlock(atc.Output), "\n"), "\n")
		if !expanded && len(out) > liveTailLines {
			out = out[len(out)-liveTailLines:]
		}
		for _, line := range out {
			for _, wl := range strings.Split(ansi.Wrap(line, dw, WrapBreakpoints), "\n") {
				lines = append(lines, mutedStyle().Render(wl))
			}
		}
	}
	detailLines(&b, lines)
	return b.String()
}

// renderPlanRow is a beat's plan line: one mark per todo (✓ done, ▸ in
// progress, · waiting), the active todo's position and its text. A list
// longer than maxPlanMarks draws the plan blocks instead of marks.
func renderPlanRow(p *viewmodel.PlanUpdate, w int) string {
	v := "plan" + strings.Repeat(" ", verbWidth-len("plan"))
	if p.Cleared {
		return mutedStyle().Render(v+"cleared") + "\n"
	}
	th := theme.Current()
	var marks string
	if p.Total() <= maxPlanMarks {
		parts := make([]string, 0, p.Total())
		for _, st := range p.Status {
			switch st {
			case "completed":
				parts = append(parts, lipgloss.NewStyle().Foreground(th.StatusSuccess).Render(glyph.OK))
			case "in_progress":
				parts = append(parts, lipgloss.NewStyle().Foreground(accentColor).Render(glyph.Running))
			default:
				parts = append(parts, mutedStyle().Render(glyph.Ambient))
			}
		}
		marks = strings.Join(parts, " ")
	} else {
		marks = progressBlocks(p.Done(), p.Total(), 8)
	}
	pos, text := fmt.Sprintf("%d/%d", p.Done(), p.Total()), ""
	switch {
	case p.Active > 0:
		pos, text = fmt.Sprintf("%d/%d", p.Active, p.Total()), viewmodel.ScrubText(oneLine(p.ActiveText))
	case p.Done() == p.Total():
		text = "done"
	}
	line := mutedStyle().Render(v) + marks + "  " + mutedStyle().Render(pos)
	if text != "" {
		room := max(w-ansi.StringWidth(line)-2, 1)
		line += "  " + ansi.Truncate(text, room, "…")
	}
	return ansi.Truncate(line, w, "…") + "\n"
}
```

**New file `internal/app/tui/spine_test.go`.** 

```go
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func groupEvents() []registry.AuditEvent {
	return []registry.AuditEvent{
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"budget.go"}`), ResultSummary: "42 lines"},
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"runner.go"}`), ResultSummary: "88 lines"},
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"execute.go"}`), ResultSummary: "12 lines"},
	}
}

// plainLines strips styling and splits rendered output into lines.
func plainLines(s string) []string {
	return strings.Split(strings.TrimRight(ansi.Strip(s), "\n"), "\n")
}

func TestCallVerbs(t *testing.T) {
	cases := map[string]string{
		"file.read":        "read",
		"file.edit":        "edit",
		"file.write_patch": "edit",
		"patch.apply":      "edit",
		"file.write":       "write",
		"shell.run":        "shell",
		"shell.sudo":       "shell",
		"test.run":         "shell",
		"repo.search":      "search",
		"codebase.search":  "search",
		"symbols.find":     "search",
		"agent.run":        "agent",
		"agent.await":      "agent",
		"web.fetch":        "web",
		"browser.navigate": "web",
		"todo.write":       "plan",
		"mcp.github.issue": "mcp",
		"git.status":       "git",
		"skill.load":       "skill",
	}
	for name, want := range cases {
		if got := callVerb(name); got != want {
			t.Errorf("callVerb(%q) = %q, want %q", name, got, want)
		}
	}
}

// A call whose arguments name nothing falls back to the rest of its tool
// name, but never just repeats the verb.
func TestFallbackSubjectNeverRepeatsTheVerb(t *testing.T) {
	cases := map[string]string{"git.status": "status", "agent.await": "await", "file.read": "", "file.write": "", "todos": ""}
	for name, want := range cases {
		if got := fallbackSubject(name); got != want {
			t.Errorf("fallbackSubject(%q) = %q, want %q", name, got, want)
		}
	}
}

// The verb sits in its column, the subject follows, and the result ends at
// the row's right edge. A long subject gives way to the result.
func TestCallRowLaysOutVerbSubjectAndResult(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"evals/tokenizer.toml"}`), ResultSummary: "18 lines"}
	line := plainLines(renderCallRow(ev, false, nil, 60, ""))[0]
	if !strings.HasPrefix(line, "read    evals/tokenizer.toml") || !strings.HasSuffix(line, "18 lines") || ansi.StringWidth(line) != 60 {
		t.Fatalf("row = %q", line)
	}
	ev.Args = json.RawMessage(`{"path":"` + strings.Repeat("deep/", 20) + `file.go"}`)
	line = plainLines(renderCallRow(ev, false, nil, 60, ""))[0]
	if !strings.Contains(line, "…") || !strings.HasSuffix(line, "18 lines") || ansi.StringWidth(line) > 60 {
		t.Fatalf("a long subject truncates and keeps the result: %q", line)
	}
}

func TestCallResults(t *testing.T) {
	exit0, exit2 := 0, 2
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n-a\n+b\n+c\n"
	notice := &registry.ToolNotice{Data: map[string]any{registry.NoticeSubagentWork: registry.SubagentWorkReference{TaskKey: "k", RuntimeEpoch: "e", AgentID: 3}}}
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want string
	}{
		{"an error", registry.AuditEvent{ToolName: "file.read", Error: "no such file\nmore"}, "no such file"},
		{"a command's exit and time", registry.AuditEvent{ToolName: "shell.run", CommandExitCode: &exit0, Duration: 3 * time.Second, ResultSummary: "ignored"}, "exit 0 · 3s"},
		{"a failing exit", registry.AuditEvent{ToolName: "test.run", CommandExitCode: &exit2}, "exit 2"},
		{"an edit's diff stat", registry.AuditEvent{ToolName: "file.edit", ResultContent: diff}, "+2 −1"},
		{"a started subagent", registry.AuditEvent{ToolName: "agent.run", Notice: notice, ResultSummary: "started as subagent 3 — review"}, "⧉ #3 started"},
		{"a legacy started subagent", registry.AuditEvent{ToolName: "agent.run", ResultSummary: "started as subagent 7 — review"}, "⧉ #7 started"},
		{"a hook leads", registry.AuditEvent{ToolName: "file.read", ResultSummary: "4 lines", Hooks: []registry.HookMetadata{{Rewrote: true}}}, "hook rewrote · 4 lines"},
		{"the summary", registry.AuditEvent{ToolName: "repo.search", ResultSummary: "4 matches"}, "4 matches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ansi.Strip(callResult(c.ev)); got != c.want {
				t.Fatalf("callResult = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCallSubjects(t *testing.T) {
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want string
	}{
		{"a command, first line, workdir elided", registry.AuditEvent{ToolName: "shell.run", Args: json.RawMessage(`{"command":"cd /w && go test ./...\necho done"}`)}, "⌂ go test ./..."},
		{"a search query, quoted", registry.AuditEvent{ToolName: "repo.search", Args: json.RawMessage(`{"query":"auth flow"}`)}, `"auth flow"`},
		{"a semantic search says so", registry.AuditEvent{ToolName: "codebase.search", Args: json.RawMessage(`{"query":"auth flow"}`)}, `semantic "auth flow"`},
		{"a subagent's task", registry.AuditEvent{ToolName: "agent.run", Args: json.RawMessage(`{"description":"review the config change","role":"reviewer"}`)}, "review the config change"},
		{"a path", registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}, "a.go"},
		{"nothing named", registry.AuditEvent{ToolName: "git.status"}, "status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := callSubject(c.ev, "/w"); got != c.want {
				t.Fatalf("callSubject = %q, want %q", got, c.want)
			}
		})
	}
}

// An edit is one line until it is expanded; then its diff hangs under the
// subject.
func TestEditRowIsOneLineUntilExpanded(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "file.edit", Args: json.RawMessage(`{"path":"x.go"}`),
		ResultContent: "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old line\n+new line\n"}
	if got := plainLines(renderCallRow(ev, false, nil, 80, "")); len(got) != 1 {
		t.Fatalf("collapsed edit = %q", got)
	}
	open := plainLines(renderCallRow(ev, true, nil, 80, ""))
	body := strings.Join(open[1:], "\n")
	if !strings.Contains(body, "new line") || !strings.HasPrefix(open[1], strings.Repeat(" ", verbWidth)) {
		t.Fatalf("expanded edit = %q", open)
	}
}

// A failed call shows the last lines of its output collapsed; expanding it
// shows the whole output and the error.
func TestFailedCallShowsItsTailCollapsed(t *testing.T) {
	code := 1
	ev := registry.AuditEvent{ToolName: "shell.run", Args: json.RawMessage(`{"command":"go test ./..."}`), CommandExitCode: &code,
		ResultContent: "line 1\nline 2\nline 3\nline 4\n--- FAIL: TestX"}
	lines := plainLines(renderCallRow(ev, false, nil, 80, ""))
	if len(lines) != 1+failedTailLines || !strings.Contains(lines[len(lines)-1], "--- FAIL: TestX") || strings.Contains(strings.Join(lines, "\n"), "line 1") {
		t.Fatalf("collapsed failure = %q", lines)
	}
	if open := ansi.Strip(renderCallRow(ev, true, nil, 80, "")); !strings.Contains(open, "line 1") {
		t.Fatalf("expanded failure = %q", open)
	}
}

func TestGroupRowCollapsedIsOneLineExpandedOnePerCall(t *testing.T) {
	collapsed := plainLines(renderGroupRow(groupEvents(), false, 80, ""))
	if len(collapsed) != 1 || !strings.Contains(collapsed[0], "budget.go, runner.go, execute.go") || !strings.HasSuffix(collapsed[0], "×3") {
		t.Fatalf("collapsed run = %q", collapsed)
	}
	expanded := plainLines(renderGroupRow(groupEvents(), true, 80, ""))
	if len(expanded) != 4 {
		t.Fatalf("expanded run = %q", expanded)
	}
	for i, want := range []string{"budget.go · 42 lines", "runner.go · 88 lines", "execute.go · 12 lines"} {
		if !strings.Contains(expanded[i+1], want) {
			t.Fatalf("expanded line %d = %q, want %q", i+1, expanded[i+1], want)
		}
	}
}

// An overflowing run truncates its subjects and keeps its count, wide runes
// included.
func TestGroupRowFitsItsWidth(t *testing.T) {
	var evs []registry.AuditEvent
	for _, p := range []string{"内部/非常に長いパス/ファイル一.go", "internal/app/tui/steps.go", "internal/app/tui/viewport_render.go", "internal/app/tui/model.go"} {
		evs = append(evs, registry.AuditEvent{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"` + p + `"}`)})
	}
	for _, w := range []int{30, 60} {
		lines := plainLines(renderGroupRow(evs, false, w, ""))
		if len(lines) != 1 || ansi.StringWidth(lines[0]) > w || !strings.Contains(lines[0], "…") || !strings.HasSuffix(lines[0], "×4") {
			t.Fatalf("w=%d: run = %q", w, lines)
		}
	}
}

// A running call names what it acts on, never its raw arguments, and shows a
// bounded tail of its output until it is opened.
func TestActiveCallRowNamesItsSubject(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &stepRenderCtx{toolSpinner: "⠋", now: now}
	edit := session.ActiveToolCall{Name: "file.edit", Path: "evals/thresholds.toml", Args: "evals/thresholds.toml", StartedAt: now.Add(-2 * time.Second)}
	if line := plainLines(renderActiveCallRow(edit, c, false, 80))[0]; !strings.HasPrefix(line, "edit    evals/thresholds.toml") || !strings.HasSuffix(line, "⠋ 2s") {
		t.Fatalf("running edit = %q", line)
	}
	var out []string
	for i := 1; i <= 10; i++ {
		out = append(out, fmt.Sprintf("PASS pkg/%d", i))
	}
	run := session.ActiveToolCall{Name: "shell.run", Args: "go test ./...", StartedAt: now, Output: strings.Join(out, "\n")}
	collapsed := plainLines(renderActiveCallRow(run, c, false, 80))
	if len(collapsed) != 1+liveTailLines || strings.Contains(strings.Join(collapsed, "\n"), "pkg/1\n") || !strings.Contains(collapsed[len(collapsed)-1], "pkg/10") {
		t.Fatalf("collapsed running command = %q", collapsed)
	}
	if expanded := plainLines(renderActiveCallRow(run, c, true, 80)); len(expanded) != 11 {
		t.Fatalf("expanded running command = %q", expanded)
	}
}

func TestPlanRow(t *testing.T) {
	cases := []struct {
		name string
		p    viewmodel.PlanUpdate
		want string
	}{
		{"active item", viewmodel.PlanUpdate{Status: []string{"completed", "completed", "in_progress", "pending"}, Active: 3, ActiveText: "Run the eval harness unit tests"},
			"plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests"},
		{"all done", viewmodel.PlanUpdate{Status: []string{"completed", "completed"}}, "plan    ✓ ✓  2/2  done"},
		{"nothing in progress", viewmodel.PlanUpdate{Status: []string{"completed", "pending"}}, "plan    ✓ ·  1/2"},
		{"cleared", viewmodel.PlanUpdate{Cleared: true}, "plan    cleared"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := plainLines(renderPlanRow(&c.p, 80))[0]; got != c.want {
				t.Fatalf("plan row = %q, want %q", got, c.want)
			}
		})
	}
	long := viewmodel.PlanUpdate{Status: make([]string, maxPlanMarks+1), Active: 1, ActiveText: "first"}
	for i := range long.Status {
		long.Status[i] = "pending"
	}
	long.Status[0] = "in_progress"
	if got := plainLines(renderPlanRow(&long, 80))[0]; strings.Contains(got, "·") || !strings.Contains(got, "▱") || !strings.Contains(got, "1/13  first") {
		t.Fatalf("a long plan draws blocks: %q", got)
	}
}

// Every row but the last hangs off ├ and the last off └; an expanded row's
// detail carries the spine down to the next row, and nothing does after the
// last.
func TestSpineConnectsRowsToTheirBeat(t *testing.T) {
	n := beatNode("Reading three files.", 3)
	for i := range n.Children {
		n.Children[i].Tools[0].ToolName = "git.status" // distinct rows that do not merge
		n.Children[i].Tools[0].ResultContent = fmt.Sprintf("body %d", i)
	}
	out, _ := renderStep(n, &stepRenderCtx{expanded: func(id viewmodel.NodeID, inherited bool) bool {
		return id == n.Children[0].ID || id == n.Children[2].ID
	}}, 80, false)
	lines := plainLines(out)
	want := []string{"├", "│", "├", "└", " "}
	if len(lines) != 1+len(want) {
		t.Fatalf("beat = %q", lines)
	}
	for i, g := range want {
		if got := string([]rune(lines[i+1])[gutterWidth]); got != g {
			t.Errorf("line %d connector = %q, want %q: %q", i+1, got, g, lines[i+1])
		}
	}
}

// No row on the spine is wider than the frame, at any width.
func TestSpineRowsFitTheFrame(t *testing.T) {
	code := 1
	n := beatNode("A beat with every kind of row and a long narration that wraps across lines.", 0)
	t0 := n.Step.Step.StartedAt
	add := func(ch *viewmodel.Node) { n.Children = append(n.Children, ch) }
	for i, ev := range []registry.AuditEvent{
		{ToolName: "shell.run", Args: json.RawMessage(`{"command":"` + strings.Repeat("go test ./... ", 12) + `"}`), CommandExitCode: &code, ResultContent: strings.Repeat("x", 300)},
		{ToolName: "file.edit", Args: json.RawMessage(`{"path":"` + strings.Repeat("dir/", 30) + `f.go"}`), ResultContent: "--- a\n+++ b\n@@ -1 +1 @@\n-" + strings.Repeat("y", 200) + "\n+z\n"},
	} {
		ev.Timestamp, ev.ToolCallID = t0.Add(time.Duration(i)*time.Second), fmt.Sprintf("c%d", i)
		add(&viewmodel.Node{ID: viewmodel.ToolID(ev), Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev}})
	}
	add(&viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "group"}, Kind: viewmodel.KindTool, Tools: groupEvents()})
	add(&viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindPlanUpdate, Key: "plan"}, Kind: viewmodel.KindPlanUpdate,
		PlanUpdate: &viewmodel.PlanUpdate{Status: []string{"completed", "in_progress"}, Active: 2, ActiveText: strings.Repeat("long todo ", 20)}})
	add(&viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindTool, Key: "live"}, Kind: viewmodel.KindTool, Live: true,
		Active: &session.ActiveToolCall{Name: "shell.run", Args: strings.Repeat("make all ", 20), StartedAt: t0, Output: strings.Repeat("o", 300)}})
	for _, width := range []int{40, 80, 120} {
		for _, open := range []bool{false, true} {
			out, _ := renderStep(n, &stepRenderCtx{now: t0}, width, open)
			for _, l := range plainLines(out) {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("width=%d open=%v: %d-cell line %q", width, open, w, l)
				}
			}
		}
	}
}

// Copying a call row starts at its subject: the connector and the verb are
// gutter, and the detail under a row that is not the last starts at the same
// column, so a copied failure tail lines up with its command.
func TestSpineRowsCopyFromTheSubjectColumn(t *testing.T) {
	subject := spineWidth + verbWidth
	for _, text := range []string{
		"   ├ shell   go test ./...                 exit 1",
		"   └ read    a.go                          5 lines",
		"   │         --- FAIL: TestX (0.01s)",
	} {
		if sp := spanOf(selRow{text: text}, 80); sp.start != subject || sp.bare {
			t.Errorf("%q copies from column %d (bare=%v), want %d", text, sp.start, sp.bare, subject)
		}
	}
}
```

**`internal/app/tui/steps.go`.** The head, the spine loop, and the deletions above.

```diff
diff --git a/internal/app/tui/steps.go b/internal/app/tui/steps.go
--- a/internal/app/tui/steps.go
+++ b/internal/app/tui/steps.go
@@ -4,6 +4,7 @@ import (
 	"fmt"
 	"hash/fnv"
 	"image/color"
+	"slices"
 	"strings"
 	"time"
 
@@ -14,15 +15,9 @@ import (
 	"marshal/internal/app/tui/glyph"
 	"marshal/internal/app/tui/theme"
 	"marshal/internal/tools/native"
-	"marshal/internal/tools/registry"
 	"marshal/internal/viewmodel"
 )
 
-// stepRowIndent is how far a row inside a step sits to the right of the same
-// row at top level. A top-level row's glyph is at column 1 (gutterPrefix is
-// " ⌗ "); inside a step it sits at nestedBodyIndent, column 5.
-const stepRowIndent = nestedBodyIndent - 1
-
 // minHeadlineCols is the room the right-aligned meta must leave the headline.
 const minHeadlineCols = 24
 
@@ -84,10 +79,10 @@ func (c *stepRenderCtx) callersFor(id viewmodel.NodeID) []string {
 	return c.callers(id)
 }
 
-// renderStep renders one step: a header (state glyph, headline, right-aligned
-// owner meta), the narration continuation, thinking rows, then tool rows and
-// subagent cards at nested indent. Every piece ends in a newline, so the
-// block's line count is strings.Count(out, "\n").
+// renderStep renders one beat: a head (state glyph, narration, right-aligned
+// meta), its reasoning when the beat is expanded, then tool rows and subagent
+// cards at nested indent. Every piece ends in a newline, so the block's line
+// count is strings.Count(out, "\n").
 func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool) (string, []subRegion) {
 	si := n.Step
 	rows := n.Children
@@ -99,46 +94,33 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool)
 		b.WriteString(s)
 		lines += strings.Count(s, "\n")
 	}
-	prevMulti := false
 	row := func(id viewmodel.NodeID, s string, sub subRegion) {
-		if s == "" {
-			return
-		}
-		// Single-line rows sit tight under their narration and each other, so
-		// a step and its calls read as one paragraph. A row that spans lines
-		// (expanded, a failure tail, a card) is set off by a blank line on
-		// both sides. The gap stays out of hit regions.
-		multi := strings.Count(s, "\n") > 1
-		if multi || prevMulti {
-			write("\n")
-		}
-		prevMulti = multi
 		sub.id = id
 		sub.start = lines
 		write(s)
 		sub.end = lines
 		subs = append(subs, sub)
 	}
-	if si.Todo != nil {
-		write(renderTodoDivider(si.Todo, width))
-	}
-
-	head, rest := stepHeadline(si)
+	head, _ := stepHeadline(si)
 	headless := head == ""
 
 	failed := stepFailed(rows)
 
-	// Header. A headless beat (no narration) has none: its rows stand on
-	// their own rather than under an invented title.
 	g, gc := stepGlyph(n, failed, c)
 	meta := metaParts{
 		owner:      stepOwner(si.Step),
 		role:       stepRoleWord(si.Step),
 		model:      stepModel(si.Step.Actor, c.routeModel, c.routeProvider),
+		thought:    thoughtMeta(si.Thinking),
 		dur:        stepDuration(n, c.now),
+		tag:        todoTag(si.Todo),
 		ownerColor: actorColor(si.Step.Actor.Role),
 	}
-	if !headless {
+	if headless {
+		// A beat with no narration gets no invented title: its head counts
+		// its calls, and carries the meta like any other.
+		write(headlessHead(g, gc, countToolCalls(rows), meta, width))
+	} else {
 		// Render narration as a single Markdown document so formatting and
 		// code fences survive sentence boundaries.
 		var texts []string
@@ -169,18 +151,12 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool)
 		if metaW > 0 && !metaFits {
 			write(strings.Repeat(" ", max(width-metaW, gutterWidth)) + metaText + "\n")
 		}
-		rest = "" // The full narration has already been rendered once.
-	}
-
-	// Narration continuation.
-	if rest != "" {
-		write(renderStepContinuation(rest, width))
 	}
 
-	// Thinking rows.
-	for _, t := range si.Thinking {
-		id := viewmodel.ThinkingID(t)
-		row(id, renderNestedThinking(t, c.open(id, open), width), subRegion{})
+	// The reasoning shows when the beat is expanded; collapsed, the head's
+	// "thought Ns" stands for it.
+	if open {
+		write(renderBeatReasoning(si.Thinking, width))
 	}
 	if si.LiveThinking != "" {
 		rv := c.region(viewmodel.LiveThinkingID)
@@ -195,132 +171,65 @@ func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool)
 
 	// A long beat shows its newest rows; the rest collapse into one line.
 	// Expanding the beat shows them all.
+	var cards []*viewmodel.Node
+	for _, ch := range rows {
+		if ch.Kind == viewmodel.KindSubagent && ch.Item != nil && ch.Item.Subagent != nil {
+			cards = append(cards, ch)
+		}
+	}
+	rows = slices.DeleteFunc(slices.Clone(rows), func(ch *viewmodel.Node) bool { return !spineRow(ch) })
 	if over := len(rows) - viewmodel.BeatRowCap; over > 0 && !open {
-		write(strings.Repeat(" ", nestedBodyIndent) + mutedStyle().Render(fmt.Sprintf("+%d earlier calls", over)) + "\n")
+		write(spineBlock(mutedStyle().Render(fmt.Sprintf("… %d earlier calls", over))+"\n", false))
 		rows = rows[over:]
 	}
 
-	// Tool rows and subagent cards.
-	for _, ch := range rows {
+	// Calls and the plan line, each hung off the spine.
+	w := max(width-spineWidth, 1)
+	for i, ch := range rows {
+		last := i == len(rows)-1
 		switch {
-		case ch.Kind == viewmodel.KindTool && ch.Active != nil:
-			row(ch.ID, renderActiveToolRow(*ch.Active, c, c.liveToolExpanded(ch.ID), width), subRegion{})
-		case ch.Kind == viewmodel.KindTool && ch.Job != nil:
-			row(ch.ID, renderJobCard(ch, c.toolSpinner, c.now, c.open(ch.ID, open), width, c.workdir), subRegion{live: ch.Job.Running})
-		case ch.Kind == viewmodel.KindTool && len(ch.Tools) > 1:
-			row(ch.ID, renderToolGroupRow(ch.Tools, c.open(ch.ID, open), width), subRegion{})
-		case ch.Kind == viewmodel.KindTool && len(ch.Tools) == 1:
-			row(ch.ID, renderToolRow(ch.Tools[0], c.open(ch.ID, open), c.callersFor(ch.ID), width, c.workdir), subRegion{})
-		case ch.Kind == viewmodel.KindSubagent && ch.Item != nil && ch.Item.Subagent != nil:
-			v := *ch.Item.Subagent
-			rv := c.region(ch.ID)
-			card := renderSubagentCard(v, c.open(ch.ID, open), c.toolSpinner, rv, width-stepRowIndent)
-			sub := subRegion{live: v.Status == session.SubagentRunning}
-			if v.Child != nil {
-				sub.subagent = &v
-			}
-			row(ch.ID, indentLines(card, stepRowIndent), sub)
+		case ch.Kind == viewmodel.KindPlanUpdate:
+			// The plan line is not a target of its own: a click on it opens
+			// the beat.
+			write(spineBlock(renderPlanRow(ch.PlanUpdate, w), last))
+		case ch.Active != nil:
+			row(ch.ID, spineBlock(renderActiveCallRow(*ch.Active, c, c.liveToolExpanded(ch.ID), w), last), subRegion{})
+		case ch.Job != nil:
+			row(ch.ID, spineBlock(renderJobRow(ch, c.toolSpinner, c.now, c.open(ch.ID, open), w, c.workdir), last), subRegion{live: ch.Job.Running})
+		case len(ch.Tools) > 1:
+			row(ch.ID, spineBlock(renderGroupRow(ch.Tools, c.open(ch.ID, open), w, c.workdir), last), subRegion{})
+		default:
+			row(ch.ID, spineBlock(renderCallRow(ch.Tools[0], c.open(ch.ID, open), c.callersFor(ch.ID), w, c.workdir), last), subRegion{})
 		}
 	}
-	return b.String(), subs
-}
 
-// renderJobCard renders a background job's card: the shell.run row that
-// started it, settled in place by the job's exit. One row, with the outcome
-// right-aligned so a long command can never push it off the line.
-func renderJobCard(row *viewmodel.Node, spinner string, now time.Time, expanded bool, width int, workdir string) string {
-	j := row.Job
-	g, style := glyph.Job, statusOkStyle()
-	gutterColor := theme.Current().FGMuted
-	var tail string
-	switch {
-	case j.Lost:
-		// The session ended while the job ran: there is no exit code to
-		// report, and the card must not claim one.
-		g, style = glyph.Warning, warningStyle()
-		gutterColor = theme.Current().StatusWarning
-		tail = "ended with session"
-	case j.Status == string(native.StatusStopping):
-		// The stop was accepted and the process is still going. Checked before
-		// running: a stopping execution has no outcome yet, so it is still
-		// "running" as far as a card is concerned, and its tail must say what is
-		// happening to it rather than repeat that it runs.
-		g, style = glyph.Warning, warningStyle()
-		gutterColor = theme.Current().StatusWarning
-		tail = "stopping"
-		if j.Duration > 0 {
-			tail += dimSeparator + formatElapsed(j.Duration)
-		}
-	case j.Running:
-		g = spinner
-		if g == "" {
-			g = glyph.Running
-		}
-		gutterColor = theme.Current().AccentPrimary
-		tail = "running"
-		// JobStart, not Tools[0]: a caller-built tree can hold a running job
-		// whose starting call is not attached, and the row still renders.
-		if started := row.JobStart(); !started.IsZero() {
-			tail += dimSeparator + formatElapsed(max(now.Sub(started), 0))
-		}
-	default:
-		// Settled. An exit that was never observed is never success: a start-up
-		// failure produces no code at all, and a legacy record spells "unknown"
-		// as -1, so both render as unknown rather than as a clean exit.
-		code := j.Exit
-		if code == nil && j.ExitCode >= 0 {
-			// The legacy field carries a real code; the -1 convention is what
-			// marks it unknown.
-			legacy := j.ExitCode
-			code = &legacy
-		}
-		failed := jobFailedStatus(j.Status) || (code != nil && *code != 0)
-		switch {
-		case failed:
-			g, style = glyph.Error, errorStyle()
-			gutterColor = theme.Current().StatusError
-		case code == nil:
-			g, style = glyph.Warning, warningStyle()
-			gutterColor = theme.Current().StatusWarning
-		}
-		tail = jobOutcomeWord(j, code)
-		if j.Duration > 0 {
-			tail += dimSeparator + formatElapsed(j.Duration)
+	// Subagent cards in the parent view (the projection without live work)
+	// sit below the spine, at the rows' indent.
+	for _, ch := range cards {
+		v := *ch.Item.Subagent
+		card := renderSubagentCard(v, c.open(ch.ID, open), c.toolSpinner, c.region(ch.ID), width-cardIndent)
+		sub := subRegion{live: v.Status == session.SubagentRunning}
+		if v.Child != nil {
+			sub.subagent = &v
 		}
+		row(ch.ID, indentLines(card, cardIndent), sub)
 	}
-	if j.Truncated {
-		tail += dimSeparator + "truncated"
-	}
-	head := viewmodel.ScrubText(j.ID)
-	// The command is model-authored shell text and this renderer prints it
-	// verbatim, so it is scrubbed here as well as at the viewmodel boundary.
-	if cmd := firstLine(viewmodel.ScrubText(viewmodel.ElideWorkdir(j.Command, workdir))); cmd != "" {
-		head += dimSeparator + cmd
-	}
-	if j.Output != "" {
-		if expanded {
-			head += " " + glyph.DisclosureExpanded
-		} else {
-			head += " " + glyph.DisclosureCollapsed
-		}
-	}
-	var b strings.Builder
-	b.WriteString(indentLines(nowBarJustify(gutterPrefix(g, gutterColor)+style.Render(head), tail, width-stepRowIndent), stepRowIndent))
-	b.WriteString("\n")
-	if expanded && j.Output != "" {
-		cw := contentWidth(width)
-		// Scrubbed at the projection and again here, at this renderer's own
-		// boundary; the session caps the output, so the second pass is cheap.
-		out := viewmodel.ScrubBlock(j.Output)
-		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
-			for _, wl := range strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n") {
-				b.WriteString(continuation())
-				b.WriteString(mutedStyle().Render(wl))
-				b.WriteString("\n")
-			}
-		}
+	return b.String(), subs
+}
+
+// cardIndent is where a subagent card inside a beat starts: its glyph lines
+// up with the spine's verbs.
+const cardIndent = gutterWidth + 1
+
+// spineRow reports whether a beat's child draws as a row on its spine.
+func spineRow(ch *viewmodel.Node) bool {
+	switch ch.Kind {
+	case viewmodel.KindPlanUpdate:
+		return ch.PlanUpdate != nil
+	case viewmodel.KindTool:
+		return ch.Active != nil || len(ch.Tools) > 0
 	}
-	return b.String()
+	return false
 }
 
 // jobOutcomeWord is a settled card's right-hand outcome: the actual exit code
@@ -369,16 +278,6 @@ func renderMarker(m *viewmodel.MarkerInfo, width int) string {
 	return gutterPrefix(g, color) + mutedStyle().Render(ansi.Truncate(text, max(width-gutterWidth, 1), "…")) + "\n"
 }
 
-// renderTodoDivider is the dim rule a beat opens with when work moves to a
-// different todo: "── 3/9 AccessibleModal initialFocus ──────".
-func renderTodoDivider(l *viewmodel.TodoLabel, width int) string {
-	text := fmt.Sprintf(" %d/%d %s ", l.Index, l.Total, oneLine(viewmodel.ScrubText(l.Content)))
-	room := max(width-gutterWidth-4, 8)
-	text = ansi.Truncate(text, room, "…")
-	fill := max(width-gutterWidth-2-ansi.StringWidth(text), 2)
-	return strings.Repeat(" ", gutterWidth) + mutedStyle().Render("──"+text+strings.Repeat("─", fill)) + "\n"
-}
-
 // countToolCalls is the number of tool calls a step made, counting each call
 // of a merged run.
 func countToolCalls(rows []*viewmodel.Node) int {
@@ -392,54 +291,6 @@ func countToolCalls(rows []*viewmodel.Node) int {
 	return n
 }
 
-// renderToolRow renders a settled tool call inside a step. It is the
-// top-level row, indented: width is reduced by the indent so wrapping stays
-// inside the frame, and every line (continuations included) shifts with it.
-func renderToolRow(ev registry.AuditEvent, expanded bool, callers []string, width int, workdir string) string {
-	out := indentLines(renderCompletedToolCallIn(ev, expanded, callers, width-stepRowIndent, workdir), stepRowIndent)
-	if !expanded && viewmodel.EventFailed(ev) {
-		out += failedTail(ev, width)
-	}
-	return out
-}
-
-// failedTail shows the last few lines of a failed call's output under its
-// collapsed row, so the reason is visible without opening it. The tail is the
-// useful end: a test run prints its failures last.
-func failedTail(ev registry.AuditEvent, width int) string {
-	text := strings.TrimRight(ev.ResultContent, "\n")
-	if text == "" {
-		text = ev.Error
-	}
-	if text == "" {
-		return ""
-	}
-	lines := strings.Split(text, "\n")
-	if len(lines) > failedTailLines {
-		lines = lines[len(lines)-failedTailLines:]
-	}
-	var b strings.Builder
-	room := max(width-stepRowIndent-continuationIndent-2, 8)
-	for _, l := range lines {
-		b.WriteString(strings.Repeat(" ", stepRowIndent+continuationIndent) +
-			mutedStyle().Render(ansi.Truncate(strings.TrimRight(l, " \t\r"), room, "…")) + "\n")
-	}
-	return b.String()
-}
-
-// failedTailLines is how many output lines a failed row shows collapsed.
-const failedTailLines = 3
-
-// renderToolGroupRow is renderToolRow for a merged same-tool run.
-func renderToolGroupRow(evs []registry.AuditEvent, expanded bool, width int) string {
-	return indentLines(renderToolGroup(evs, expanded, width-stepRowIndent), stepRowIndent)
-}
-
-// renderActiveToolRow renders an in-flight tool call inside a step.
-func renderActiveToolRow(atc session.ActiveToolCall, c *stepRenderCtx, expanded bool, width int) string {
-	return indentLines(renderActiveToolCall(atc, c.sandbox, c.allowNetwork, c.toolSpinner, c.now, expanded, width-stepRowIndent), stepRowIndent)
-}
-
 // indentLines prefixes every non-empty line with n spaces.
 func indentLines(s string, n int) string {
 	if s == "" || n <= 0 {
@@ -458,7 +309,7 @@ func indentLines(s string, n int) string {
 // ---- header ------------------------------------------------------------
 
 // stepGlyph picks the state glyph: ✗ if any row failed, the spinner while
-// live, and a quiet · once settled.
+// live, and a muted ● once settled.
 func stepGlyph(n *viewmodel.Node, failed bool, c *stepRenderCtx) (string, color.Color) {
 	th := theme.Current()
 	switch {
@@ -471,10 +322,10 @@ func stepGlyph(n *viewmodel.Node, failed bool, c *stepRenderCtx) (string, color.
 		}
 		return g, accentColor
 	}
-	// A settled step wears a quiet dot, not a check: a ✓ on every narration
+	// A settled beat wears a muted ●, not a check: a ✓ on every narration
 	// turns the transcript into a column of badges. Only the states that need
 	// attention (running, failed) get a marker with colour.
-	return glyph.Ambient, th.FGMuted
+	return glyph.Beat, th.FGMuted
 }
 
 // stepFailed reports whether any tool row in the step failed: an error, a
@@ -537,39 +388,34 @@ const notableStepDuration = 15 * time.Second
 
 // metaParts are the pieces of a step header's right-aligned meta.
 type metaParts struct {
-	tools      int // outline density: how many calls the collapsed step hides
 	owner      string
 	role       string // owner shortened to the role word
 	model      string
+	thought    string // "thought 2s" when the beat reasoned
 	dur        string
+	tag        string // "▸ 2/4" on the beat that starts a todo
 	ownerColor color.Color
 }
 
-// rightMeta lays out the header's right-aligned meta so the headline keeps at
+// rightMeta lays out the head's right-aligned meta so the headline keeps at
 // least min(headlineWidth, minHeadlineCols) columns. Under pressure it drops
-// the model first, then the duration, then shortens the owner to its role
-// word, and drops the owner only when even that does not fit. It returns the
-// styled text and its visible width.
+// the model first, then the duration, then the thinking time, then shortens
+// the owner to its role word and drops it. The todo tag goes last. It
+// returns the styled text and its visible width.
 func rightMeta(p metaParts, headlineWidth, width int) (string, int) {
 	budget := width - gutterWidth - 2 - min(headlineWidth, minHeadlineCols)
-	type cand struct{ owner, model, tools, dur string }
-	tools := ""
-	switch {
-	case p.tools == 1:
-		tools = "1 tool"
-	case p.tools > 1:
-		tools = fmt.Sprintf("%d tools", p.tools)
-	}
-	cands := []cand{
-		{p.owner, p.model, tools, p.dur},
-		{p.owner, "", tools, p.dur},
-		{p.owner, "", tools, ""},
-		{p.role, "", tools, ""},
-		{"", "", tools, ""},
-		{"", "", "", ""},
+	cands := []metaParts{
+		{owner: p.owner, model: p.model, thought: p.thought, dur: p.dur, tag: p.tag},
+		{owner: p.owner, thought: p.thought, dur: p.dur, tag: p.tag},
+		{owner: p.owner, thought: p.thought, tag: p.tag},
+		{owner: p.owner, tag: p.tag},
+		{owner: p.role, tag: p.tag},
+		{tag: p.tag},
+		{},
 	}
 	for _, cd := range cands {
-		text, w := renderMeta(cd.owner, cd.model, cd.tools, cd.dur, p.ownerColor)
+		cd.ownerColor = p.ownerColor
+		text, w := renderMeta(cd)
 		if w <= budget {
 			return text, w
 		}
@@ -577,7 +423,7 @@ func rightMeta(p metaParts, headlineWidth, width int) (string, int) {
 	return "", 0
 }
 
-func renderMeta(owner, model, tools, dur string, ownerColor color.Color) (string, int) {
+func renderMeta(p metaParts) (string, int) {
 	var plain, styled []string
 	add := func(s string, style lipgloss.Style) {
 		if s == "" {
@@ -587,36 +433,18 @@ func renderMeta(owner, model, tools, dur string, ownerColor color.Color) (string
 		styled = append(styled, style.Render(s))
 	}
 	ownerStyle := mutedStyle()
-	if ownerColor != nil {
-		ownerStyle = lipgloss.NewStyle().Foreground(ownerColor)
-	}
-	add(owner, ownerStyle)
-	add(model, mutedStyle())
-	add(tools, mutedStyle())
-	add(dur, mutedStyle())
+	if p.ownerColor != nil {
+		ownerStyle = lipgloss.NewStyle().Foreground(p.ownerColor)
+	}
+	add(p.owner, ownerStyle)
+	add(p.model, mutedStyle())
+	add(p.thought, mutedStyle())
+	add(p.dur, mutedStyle())
+	add(p.tag, lipgloss.NewStyle().Foreground(accentColor))
 	sep := " · "
 	return strings.Join(styled, mutedStyle().Render(sep)), ansi.StringWidth(strings.Join(plain, sep))
 }
 
-// stepHeaderLine assembles " ✓ headline        owner · 12s" to exactly the
-// frame width at most: the headline is truncated to what the meta leaves.
-func stepHeaderLine(g string, gc color.Color, head string, meta metaParts, width int) string {
-	headStyle := lipgloss.NewStyle().Foreground(theme.Current().FGEmphasis)
-	metaText, metaW := rightMeta(meta, ansi.StringWidth(head), width)
-	avail := max(width-gutterWidth, 1)
-	headRoom := avail
-	if metaW > 0 {
-		headRoom = max(avail-metaW-2, 1)
-	}
-	head = ansi.Truncate(head, headRoom, "…")
-	line := gutterPrefix(g, gc) + headStyle.Render(head)
-	if metaW > 0 {
-		pad := max(avail-ansi.StringWidth(head)-metaW, 1)
-		line += strings.Repeat(" ", pad) + metaText
-	}
-	return line
-}
-
 // stepHeadline picks the header text. A narrated beat uses the first sentence
 // of its first narration; the remainder (and any later narration) becomes the
 // continuation. A beat without narration is headless (head == ""): its rows
@@ -634,43 +462,56 @@ func firstSentence(s string) (head, rest string) { return viewmodel.FirstSentenc
 // which is rendered as plain text.
 func stripEmphasis(s string) string { return viewmodel.StripEmphasis(s) }
 
-// renderStepContinuation renders the complete narration continuation.
-func renderStepContinuation(rest string, width int) string {
-	cw := nestedContentWidth(width)
-	pad := strings.Repeat(" ", nestedBodyIndent)
-	body, ok := renderMarkdown(rest, cw)
-	if !ok {
-		body = renderPlainProse(rest, cw)
+// thoughtMeta is the head's "thought 2s": how long the beat reasoned, or
+// nothing when it did not.
+func thoughtMeta(ts []*session.ThinkingEntry) string {
+	if len(ts) == 0 {
+		return ""
 	}
-	var b strings.Builder
-	for _, l := range strings.Split(strings.Trim(body, "\n"), "\n") {
-		b.WriteString(pad)
-		b.WriteString(strings.TrimLeft(l, " "))
-		b.WriteString("\n")
+	var d time.Duration
+	for _, t := range ts {
+		d += t.Duration
 	}
-	return b.String()
+	return "thought " + formatThinkDuration(d)
 }
 
-// renderNestedThinking renders a step's reasoning as a row at nested indent:
-// "⚙ thought for 2s ▹", and when expanded the reasoning behind the nested
-// rail.
-func renderNestedThinking(t *session.ThinkingEntry, expanded bool, width int) string {
-	disclosure := ""
-	if strings.TrimSpace(t.Text) != "" {
-		disclosure = glyph.DisclosureCollapsed
-		if expanded {
-			disclosure = glyph.DisclosureExpanded
-		}
+// todoTag is "▸ 2/4" on the beat where work moves to the second of four
+// todos, and nothing on every other beat.
+func todoTag(l *viewmodel.TodoLabel) string {
+	if l == nil {
+		return ""
 	}
-	label := fmt.Sprintf("thought for %s", formatThinkDuration(t.Duration))
-	if disclosure != "" {
-		label += " " + disclosure
+	return fmt.Sprintf("%s %d/%d", glyph.Running, l.Index, l.Total)
+}
+
+// headlessHead is the head of a beat with no narration: the state glyph and a
+// muted call count, with the meta right-aligned.
+func headlessHead(g string, gc color.Color, calls int, meta metaParts, width int) string {
+	text := ""
+	if calls > 0 {
+		text = fmt.Sprintf("%d %s", calls, plural(calls, "call", "calls"))
 	}
+	line := gutterPrefix(g, gc) + mutedStyle().Render(text)
+	if metaText, metaW := rightMeta(meta, ansi.StringWidth(text), width); metaW > 0 {
+		line += strings.Repeat(" ", max(width-gutterWidth-ansi.StringWidth(text)-metaW, 1)) + metaText
+	}
+	return line + "\n"
+}
+
+// renderBeatReasoning is an expanded beat's reasoning, behind the nested rail
+// under its head: every thinking block in order, a blank rail between them.
+func renderBeatReasoning(ts []*session.ThinkingEntry, width int) string {
 	var b strings.Builder
-	b.WriteString(indentLines(gutterPrefix(glyph.Thinking, dimColor)+thinkingLineStyle().Render(label)+"\n", stepRowIndent))
-	if expanded && strings.TrimSpace(t.Text) != "" {
-		cw := nestedContentWidth(width)
-		for _, line := range strings.Split(strings.TrimSpace(t.Text), "\n") {
+	cw := nestedContentWidth(width)
+	for i, t := range ts {
+		text := strings.TrimSpace(t.Text)
+		if text == "" {
+			continue
+		}
+		if i > 0 && b.Len() > 0 {
+			b.WriteString(nestedRail() + "\n")
+		}
+		for _, line := range strings.Split(text, "\n") {
 			for _, wl := range strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n") {
 				b.WriteString(nestedRail())
 				b.WriteString(thinkingLineStyle().Render(wl))
```

**`internal/app/tui/steps_test.go`.** The head and the spine.

```diff
diff --git a/internal/app/tui/steps_test.go b/internal/app/tui/steps_test.go
--- a/internal/app/tui/steps_test.go
+++ b/internal/app/tui/steps_test.go
@@ -70,8 +70,8 @@ func TestNarratedStepRendersHeadlineAndNestedToolRows(t *testing.T) {
 	if header == "" {
 		t.Fatalf("headline missing:\n%s", strings.Join(lines, "\n"))
 	}
-	if !strings.HasPrefix(header, " · ") {
-		t.Errorf("a settled step leads with a quiet dot, got %q", header)
+	if !strings.HasPrefix(header, " ● ") {
+		t.Errorf("a settled beat leads with a muted ●, got %q", header)
 	}
 	if strings.Contains(header, "inferred") {
 		t.Errorf("narrated step must not be tagged inferred: %q", header)
@@ -82,9 +82,10 @@ func TestNarratedStepRendersHeadlineAndNestedToolRows(t *testing.T) {
 	if len(toolRows) == 0 {
 		t.Fatalf("no tool row:\n%s", strings.Join(lines, "\n"))
 	}
+	// The read is the first of the beat's two calls.
 	for _, r := range toolRows {
-		if indent := len(r) - len(strings.TrimLeft(r, " ")); indent < stepRowIndent {
-			t.Errorf("tool row not at nested indent (%d): %q", indent, r)
+		if !strings.HasPrefix(r, strings.Repeat(" ", gutterWidth)+"├ read    parser.go") {
+			t.Errorf("a call hangs off the beat's spine: %q", r)
 		}
 	}
 	// The complete narration is visible without expanding the step.
@@ -226,7 +227,9 @@ func TestNarrationMarkdownRemainsWhole(t *testing.T) {
 	}
 }
 
-func TestThinkingRowLivesInsideTheStep(t *testing.T) {
+// A beat's reasoning is its thinking time in the head; expanding the beat
+// shows the reasoning under the head, before its calls.
+func TestThinkingTimeSitsInTheBeatHead(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(100, 30)
 	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
@@ -237,23 +240,33 @@ func TestThinkingRowLivesInsideTheStep(t *testing.T) {
 	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: id, ToolCallID: "c", Timestamp: ts.Add(time.Millisecond), Args: []byte(`{"path":"a.go"}`)})
 	m.state.EndStep(id)
 	lines := transcriptLines(&m)
-	headerAt, thoughtAt := -1, -1
+	head := -1
 	for i, l := range lines {
 		if strings.Contains(l, "Looking at the failing test.") {
-			headerAt = i
-		}
-		if strings.Contains(l, "thought for 2s") {
-			thoughtAt = i
-			if !strings.HasPrefix(l, strings.Repeat(" ", stepRowIndent)) {
-				t.Errorf("thinking row not at nested indent: %q", l)
-			}
+			head = i
 		}
 	}
-	if headerAt < 0 || thoughtAt <= headerAt {
-		t.Fatalf("thinking row should sit under its step header (header %d, thought %d)", headerAt, thoughtAt)
+	if head < 0 || !strings.HasSuffix(strings.TrimRight(lines[head], " "), "thought 2s") {
+		t.Fatalf("the head carries the thinking time:\n%s", strings.Join(lines, "\n"))
 	}
 	if strings.Contains(strings.Join(lines, "\n"), "private reasoning text") {
-		t.Error("collapsed thinking must not show its text")
+		t.Error("a collapsed beat must not show its reasoning")
+	}
+
+	m.toggleExpanded(viewmodel.NodeID{Kind: viewmodel.KindStep, Key: fmt.Sprintf("step:%d", id)})
+	m.invalidateTranscript()
+	lines = transcriptLines(&m)
+	reason, call := -1, -1
+	for i, l := range lines {
+		if strings.Contains(l, "private reasoning text") {
+			reason = i
+		}
+		if strings.Contains(l, "└ read") {
+			call = i
+		}
+	}
+	if !(head < reason && reason < call) {
+		t.Fatalf("an expanded beat shows its reasoning between its head and its calls:\n%s", strings.Join(lines, "\n"))
 	}
 }
 
```

**`internal/app/tui/tasks_test.go`.** Tags, spine rows and the golden.

```diff
diff --git a/internal/app/tui/tasks_test.go b/internal/app/tui/tasks_test.go
--- a/internal/app/tui/tasks_test.go
+++ b/internal/app/tui/tasks_test.go
@@ -7,6 +7,8 @@ import (
 	"testing"
 	"time"
 
+	"github.com/charmbracelet/x/ansi"
+
 	"marshal/internal/app/session"
 	"marshal/internal/app/tui/glyph"
 	"marshal/internal/db"
@@ -59,14 +61,14 @@ func TestFinishedTasksStayOpenAndTheReceiptSummarisesTheTurn(t *testing.T) {
 	m.resize(120, 60)
 	scriptedTasks(t, &m, 3, 2, true)
 	text := viewText(&m)
-	for _, want := range []string{"1/3 Task number 1", "2/3 Task number 2", "3/3 Task number 3", "All done.", "3 tasks", "6 steps", "6 tools"} {
+	for _, want := range []string{"▸ 1/3", "▸ 2/3", "▸ 3/3", "All done.", "3 tasks", "6 steps", "6 tools"} {
 		if !strings.Contains(text, want) {
 			t.Errorf("missing %q:\n%s", want, text)
 		}
 	}
 	// Six beats, all finished: nothing folds, so every beat keeps its row.
-	if got := strings.Count(text, "Read file"); got != 6 {
-		t.Errorf("Read file rendered %d times, want 6: history never folds:\n%s", got, text)
+	if got := strings.Count(text, "read    x.go"); got != 6 {
+		t.Errorf("the read row rendered %d times, want 6: history never folds:\n%s", got, text)
 	}
 	if !strings.Contains(text, "Working on task 1 part 1. Details follow.") {
 		t.Errorf("the first beat keeps its narration:\n%s", text)
@@ -78,8 +80,8 @@ func TestRunningTurnKeepsEveryBeatOpen(t *testing.T) {
 	m.resize(120, 60)
 	scriptedTasks(t, &m, 3, 2, false)
 	text := viewText(&m)
-	if got := strings.Count(text, "Read file"); got != 6 {
-		t.Errorf("Read file rendered %d times, want 6: done tasks keep their rows:\n%s", got, text)
+	if got := strings.Count(text, "read    x.go"); got != 6 {
+		t.Errorf("the read row rendered %d times, want 6: done tasks keep their rows:\n%s", got, text)
 	}
 	if !strings.Contains(text, "Working on task 3 part 1") || !strings.Contains(text, "Working on task 3 part 2") {
 		t.Errorf("the task the model is working on is shown:\n%s", text)
@@ -214,11 +216,15 @@ func TestFailedRowShowsItsLastOutputLinesCollapsed(t *testing.T) {
 	}
 }
 
-// Golden rows for the todo chrome: exact text at 80 and 140 columns. The
-// divider is the only task chrome left; durations are fixed through the todo
-// timestamps so any row that carries one is deterministic.
-func TestTodoDividerGolden(t *testing.T) {
+// Golden rows for a beat: exact text at 80 and 140 columns. The beat that
+// starts a todo carries its tag at the right edge of its head, each call hangs
+// off the spine with its result at the right edge, and the plan line closes
+// the beat where the list was written.
+func TestBeatRowsGolden(t *testing.T) {
 	start := time.Now().Add(-10 * time.Minute)
+	edge := func(left, right string, width int) string {
+		return left + strings.Repeat(" ", width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
+	}
 	for _, width := range []int{80, 140} {
 		m := newTestModel(t)
 		m.resize(width, 60)
@@ -227,26 +233,31 @@ func TestTodoDividerGolden(t *testing.T) {
 		id := m.state.BeginStep(session.Actor{})
 		m.state.AddNarration(id, "Reading the parser. It is small.")
 		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: id, ToolCallID: "a", Args: []byte(`{"path":"p.go"}`), ResultSummary: "ok"})
+		m.state.LogToolCall(registry.AuditEvent{Timestamp: time.Now().Add(time.Millisecond), ToolName: "todo.write", StepID: id, ToolCallID: "w",
+			Args:          []byte(`{"todos":[{"id":"t1","content":"Wire the parser","status":"completed"},{"id":"t2","content":"Test it","status":"in_progress"}]}`),
+			ResultSummary: "tasks updated · 2 items"})
 		m.state.EndStep(id)
-		_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Wire the parser", Status: "completed", StartedAt: start, CompletedAt: start.Add(3 * time.Minute)}, {ID: "t2", Content: "Test it", Status: "pending"}})
+		_ = m.state.SetTodos([]db.TodoItem{{ID: "t1", Content: "Wire the parser", Status: "completed", StartedAt: start, CompletedAt: start.Add(3 * time.Minute)}, {ID: "t2", Content: "Test it", Status: "in_progress", StartedAt: start.Add(3 * time.Minute)}})
 		lines := transcriptLines(&m)
 
-		var divider string
-		for _, l := range lines {
-			if strings.Contains(l, "1/2 Wire the parser") {
-				divider = strings.TrimRight(l, " ")
+		head := -1
+		for i, l := range lines {
+			if strings.Contains(l, "Reading the parser.") {
+				head = i
 			}
 		}
-		if !strings.HasPrefix(divider, "   ── 1/2 Wire the parser ") {
-			t.Errorf("w=%d divider = %q", width, divider)
+		if head < 0 || head+3 > len(lines) {
+			t.Fatalf("w=%d beat missing:\n%s", width, strings.Join(lines, "\n"))
 		}
-		if got := len([]rune(divider)); got != width {
-			t.Errorf("w=%d divider should fill the frame, is %d wide", width, got)
+		want := []string{
+			edge(" ● Reading the parser. It is small.", "▸ 1/2", width),
+			edge("   ├ read    p.go", "ok", width),
+			"   └ plan    ✓ ▸  2/2  Test it",
 		}
-		// The todo is complete and the beat is the turn's newest, so it stays
-		// open: the divider is followed by the beat's own row.
-		if !strings.Contains(strings.Join(lines, "\n"), "Reading the parser.") {
-			t.Errorf("w=%d the beat under the divider still renders:\n%s", width, strings.Join(lines, "\n"))
+		for i, w := range want {
+			if got := strings.TrimRight(lines[head+i], " "); got != w {
+				t.Errorf("w=%d row %d = %q\nwant      %q", width, i, got, w)
+			}
 		}
 	}
 }
@@ -297,7 +308,7 @@ func TestExpandMatrix(t *testing.T) {
 		name, text string
 		want       map[string]bool // substring -> expected present
 	}{
-		{"collapsed", collapsed, map[string]bool{"Reading the config.": true, "Read file": true, "thought for": true, "private reasoning text": false, "var Secret": false}},
+		{"collapsed", collapsed, map[string]bool{"Reading the config.": true, "read    cfg.go": true, "thought 2s": true, "private reasoning text": false, "var Secret": false}},
 		{"expanded", expanded, map[string]bool{"Reading the config.": true, "private reasoning text": true, "var Secret": true}},
 	} {
 		for sub, want := range c.want {
@@ -383,19 +394,20 @@ func TestTodoDividersOrderTheWorkAndTheBandCarriesProgress(t *testing.T) {
 		t.Fatalf("missing %q:\n%s", sub, text)
 		return -1
 	}
-	// Each todo's work sits under the divider naming it, in list order.
-	read, first, write, second := at("1/4 Read"), at("Reading it."), at("2/4 Write"), at("Writing it.")
-	if !(read < first && first < write && write < second) {
-		t.Fatalf("order must be divider, work, next divider, next work:\n%s", text)
+	// The first beat of each todo carries its tag, in list order, on the
+	// head of the work itself.
+	first, second := at("Reading it."), at("Writing it.")
+	if !strings.HasSuffix(strings.TrimRight(lines[first], " "), "▸ 1/4") || !strings.HasSuffix(strings.TrimRight(lines[second], " "), "▸ 2/4") || first > second {
+		t.Fatalf("each todo's first beat carries its tag, in order:\n%s", text)
 	}
-	if strings.Contains(text, "3/4 Test") || strings.Contains(text, "4/4 Ship") {
-		t.Errorf("waiting todos live in the band, not the transcript:\n%s", text)
+	if strings.Contains(text, "3/4") || strings.Contains(text, "4/4") || strings.Contains(text, "──") {
+		t.Errorf("waiting todos live in the band, and history has no dividers:\n%s", text)
 	}
 
 	// The band carries the progress: the head row holds the count and the
 	// active item, the checklist what comes next — on this tall frame, one
 	// row per waiting todo. The numbered per-todo rows are gone — the
-	// transcript's dividers cover those.
+	// transcript's tags cover those.
 	if m.height < nowBarTallHeight {
 		t.Fatalf("frame is %d rows; the test expects a tall one", m.height)
 	}
@@ -447,11 +459,11 @@ func TestTransientNarrationFlowsWithoutHeadersWhenThereAreNoTodos(t *testing.T)
 	lines := transcriptLines(&m)
 	for i, l := range lines {
 		if strings.Contains(l, "First look.") {
-			if !strings.Contains(lines[i+1], "Read file") {
-				t.Errorf("a narration's calls sit directly under it:\n%s", text)
+			if !strings.Contains(lines[i+1], "└ read") {
+				t.Errorf("a narration's calls hang directly under it:\n%s", text)
 			}
-			if !strings.HasPrefix(l, " · ") || regexp.MustCompile(`\d+s$`).MatchString(strings.TrimRight(l, " ")) {
-				t.Errorf("settled steps are a quiet dot with no clock: %q", l)
+			if !strings.HasPrefix(l, " ● ") || regexp.MustCompile(`\d+s$`).MatchString(strings.TrimRight(l, " ")) {
+				t.Errorf("settled beats are a muted ● with no clock: %q", l)
 			}
 		}
 	}
@@ -490,7 +502,7 @@ func TestMergedToolRunStaysOneRowAsItGrows(t *testing.T) {
 	count := func() int {
 		n := 0
 		for _, l := range transcriptLines(&m) {
-			if strings.Contains(l, "Read file") {
+			if strings.Contains(l, "read    f0.go") {
 				n++
 			}
 		}
@@ -531,10 +543,8 @@ func TestTightJoinsKeepClickAndBrowsePositionsOnTheirRows(t *testing.T) {
 	}
 
 	lines := transcriptLines(&m)
-	// A labelled beat's region opens on its divider, so the todo lines count
-	// as that beat's start.
 	wants := map[viewmodel.Kind][]string{
-		viewmodel.KindStep: {"1/3 Read", "2/3 Write", "Writing the first half", "Writing the second half", "One thought", "Another thought"},
+		viewmodel.KindStep: {"Reading it.", "Writing the first half", "Writing the second half", "One thought", "Another thought"},
 	}
 	seen := 0
 	for _, r := range m.nodeRegions {
@@ -554,7 +564,7 @@ func TestTightJoinsKeepClickAndBrowsePositionsOnTheirRows(t *testing.T) {
 		seen++
 	}
 	if seen < 5 {
-		t.Fatalf("expected regions for two labelled beats and three more, saw %d:\n%s", seen, strings.Join(lines, "\n"))
+		t.Fatalf("expected regions for the five beats, saw %d:\n%s", seen, strings.Join(lines, "\n"))
 	}
 	for _, it := range m.browseItems {
 		if it.kind != viewmodel.KindStep {
```

**`internal/app/tui/tool_head_test.go`.** The group head is gone.

```diff
diff --git a/internal/app/tui/tool_head_test.go b/internal/app/tui/tool_head_test.go
--- a/internal/app/tui/tool_head_test.go
+++ b/internal/app/tui/tool_head_test.go
@@ -18,18 +18,3 @@ func TestToolRowHeadIsTheUntruncatedCollapsedRow(t *testing.T) {
 		t.Fatalf("a narrow row %q should be the head truncated", narrow)
 	}
 }
-
-func TestToolGroupHeadIsTheUntruncatedCollapsedRow(t *testing.T) {
-	evs := []registry.AuditEvent{
-		{ToolName: "search.text", Args: []byte(`{"query":"TestPreToolUseBlockDecision"}`)},
-		{ToolName: "search.text", Args: []byte(`{"query":"testHookTimeoutMS"}`)},
-	}
-	head := toolGroupHead(evs)
-	if wide := stripANSI(renderToolGroup(evs, false, 400)); !strings.Contains(wide, head) {
-		t.Fatalf("a wide group row %q does not show the head %q", wide, head)
-	}
-	narrow := strings.TrimRight(stripANSI(renderToolGroup(evs, false, 30)), " \n")
-	if !strings.HasSuffix(narrow, "…") || strings.Contains(narrow, head) {
-		t.Fatalf("a narrow group row %q should be the head truncated", narrow)
-	}
-}
```

**Delete `internal/app/tui/toolgroup.go`.** `toolTarget` moved to `spine.go`; the rest is replaced by `renderGroupRow`.

**`internal/app/tui/toolgroup_test.go`.** The old group tests are gone; `groupEvents` moved to `spine_test.go`.

```diff
diff --git a/internal/app/tui/toolgroup_test.go b/internal/app/tui/toolgroup_test.go
--- a/internal/app/tui/toolgroup_test.go
+++ b/internal/app/tui/toolgroup_test.go
@@ -6,8 +6,6 @@ import (
 	"testing"
 	"time"
 
-	"github.com/charmbracelet/x/ansi"
-
 	"marshal/internal/app/session"
 	"marshal/internal/tools/registry"
 )
@@ -73,63 +71,6 @@ func TestToolTarget(t *testing.T) {
 	}
 }
 
-func groupEvents() []registry.AuditEvent {
-	return []registry.AuditEvent{
-		{ToolName: "file.read", Args: json.RawMessage(`{"path":"budget.go"}`), ResultSummary: "42 lines"},
-		{ToolName: "file.read", Args: json.RawMessage(`{"path":"runner.go"}`), ResultSummary: "88 lines"},
-		{ToolName: "file.read", Args: json.RawMessage(`{"path":"execute.go"}`), ResultSummary: "12 lines"},
-	}
-}
-
-func TestRenderToolGroupCollapsedIsOneLine(t *testing.T) {
-	out := stripANSI(renderToolGroup(groupEvents(), false, 80))
-	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
-	if len(lines) != 1 {
-		t.Fatalf("collapsed run should render one line, got %d:\n%s", len(lines), out)
-	}
-	if !strings.Contains(lines[0], "Read files:") || !strings.Contains(lines[0], "×3") {
-		t.Fatalf("line missing plural name or count:\n%s", lines[0])
-	}
-	for _, want := range []string{"budget.go", "runner.go", "execute.go"} {
-		if !strings.Contains(lines[0], want) {
-			t.Fatalf("line missing target %q:\n%s", want, lines[0])
-		}
-	}
-}
-
-func TestRenderToolGroupExpandedOneLinePerCall(t *testing.T) {
-	out := stripANSI(renderToolGroup(groupEvents(), true, 80))
-	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
-	if len(lines) != 4 {
-		t.Fatalf("expanded run should render header + 3 lines, got %d:\n%s", len(lines), out)
-	}
-	if !strings.Contains(lines[0], "×3") {
-		t.Fatalf("expanded header missing count:\n%s", lines[0])
-	}
-	for i, want := range []string{"budget.go", "runner.go", "execute.go"} {
-		if !strings.Contains(lines[i+1], want) {
-			t.Fatalf("expanded line %d missing %q:\n%s", i+1, want, lines[i+1])
-		}
-	}
-}
-
-// TestRunningToolRendersBelowGroupThenFolds pins the shape spec §4 declares
-// correct: a running call renders on its own row beneath the group it belongs
-// to — it needs the room, since shell.run streams output there — and folds
-// into that group once it completes.
-func TestToolGroupFitsWidthWithWideRunes(t *testing.T) {
-	events := []registry.AuditEvent{
-		{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"内部/非常に長いパス/ファイル一.go"}`)},
-		{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"内部/別のとても長いパス/ファイル二.go"}`)},
-	}
-	out := renderToolGroup(events, false, 40)
-	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
-		if w := ansi.StringWidth(line); w > 40 {
-			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
-		}
-	}
-}
-
 func TestRunningToolRendersBelowGroupThenFolds(t *testing.T) {
 	readEvent := func(path string, ts time.Time) registry.AuditEvent {
 		args, err := json.Marshal(map[string]string{"path": path})
@@ -157,13 +98,13 @@ func TestRunningToolRendersBelowGroupThenFolds(t *testing.T) {
 	m.refreshViewport()
 
 	running := stripANSI(m.viewport.View())
-	if !strings.Contains(running, "Read files: ×2") {
+	if !strings.Contains(running, "budget.go, runner.go") || !strings.Contains(running, "×2") {
 		t.Errorf("while running, want a ×2 group of completed calls:\n%s", running)
 	}
 	if !strings.Contains(running, "chat.go") {
 		t.Errorf("while running, want the in-flight target on its own row:\n%s", running)
 	}
-	if strings.Contains(running, "Read files: ×3") {
+	if strings.Contains(running, "×3") {
 		t.Errorf("in-flight call must not fold into the group while running:\n%s", running)
 	}
 
@@ -172,66 +113,7 @@ func TestRunningToolRendersBelowGroupThenFolds(t *testing.T) {
 	m.refreshViewport()
 
 	done := stripANSI(m.viewport.View())
-	if !strings.Contains(done, "Read files: ×3") {
+	if !strings.Contains(done, "budget.go, runner.go, chat.go") || !strings.Contains(done, "×3") {
 		t.Errorf("after completion, want the call folded into a ×3 group:\n%s", done)
 	}
 }
-
-func TestToolGroupUsesCategoryGlyph(t *testing.T) {
-	events := []registry.AuditEvent{
-		{ToolName: "file.read", ResultSummary: "ok"},
-		{ToolName: "file.read", ResultSummary: "ok"},
-	}
-	out := stripANSI(renderToolGroup(events, false, 80))
-	if !strings.Contains(out, "≡") {
-		t.Fatalf("file.read group should use the ≡ gutter: %q", out)
-	}
-}
-
-func TestRenderToolGroupContinuationLinesIndented(t *testing.T) {
-	events := []registry.AuditEvent{
-		{ToolName: "file.read", ResultSummary: "read a very long file path that forces the bullet to wrap",
-			Args: json.RawMessage(`{"path":"internal/app/tui/transcript_render_pipeline_quite_long_implementation.go"}`)},
-		{ToolName: "file.read", ResultSummary: "ok",
-			Args: json.RawMessage(`{"path":"internal/app/tui/another_rather_long_runner_implementation_file.go"}`)},
-	}
-	out := stripANSI(renderToolGroup(events, true, 40))
-	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
-	if len(lines) < 2 {
-		t.Fatalf("expected a wrapped multi-line group, got %d line(s):\n%s", len(lines), out)
-	}
-	for _, line := range lines {
-		if line == "" {
-			continue
-		}
-		if r := []rune(line); r[0] != ' ' {
-			t.Fatalf("line starts with a non-space rune %q: %q", string(r[0]), line)
-		}
-		if w := ansi.StringWidth(line); w > 40 {
-			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
-		}
-	}
-}
-
-func TestCollapsedToolGroupTruncatesOverflowingTargetsOnOneLine(t *testing.T) {
-	var events []registry.AuditEvent
-	for _, p := range []string{"internal/app/tui/steps.go", "internal/app/tui/tasks_render.go", "internal/app/tui/viewport_render.go", "internal/app/tui/model.go"} {
-		events = append(events, registry.AuditEvent{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"` + p + `"}`)})
-	}
-	for _, width := range []int{30, 60} {
-		out := stripANSI(renderToolGroup(events, false, width))
-		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
-		if len(lines) != 1 {
-			t.Fatalf("w=%d: collapsed run is one line, got %d:\n%s", width, len(lines), out)
-		}
-		if w := ansi.StringWidth(lines[0]); w > width {
-			t.Fatalf("w=%d: line is %d cells wide: %q", width, w, lines[0])
-		}
-		if !strings.HasSuffix(lines[0], "…") {
-			t.Fatalf("w=%d: an overflowing run ends with an ellipsis: %q", width, lines[0])
-		}
-		if !strings.Contains(lines[0], "×4") {
-			t.Fatalf("w=%d: the count survives truncation: %q", width, lines[0])
-		}
-	}
-}
```

**`internal/app/tui/toolnames.go`.** The pluralizer had no other caller.

```diff
diff --git a/internal/app/tui/toolnames.go b/internal/app/tui/toolnames.go
--- a/internal/app/tui/toolnames.go
+++ b/internal/app/tui/toolnames.go
@@ -46,59 +46,3 @@ func toolCategoryGlyph(name string) string {
 // get a title-cased, dot-to-space transformation (only the first segment
 // is capitalized).
 func DisplayToolName(name string) string { return viewmodel.DisplayToolName(name) }
-
-// toolPluralNames overrides the generic pluralizer for tools whose display
-// name does not take a bare "s" — either because the noun is already plural
-// ("Run tests"), because it needs "es" ("Apply patches"), or because the
-// head noun is not the last word ("Query JSON" → "Query JSON documents").
-var toolPluralNames = map[string]string{
-	"file.write_patch": "Edit files",
-	"file.edit":        "Edit files",
-	"patch.apply":      "Apply patches",
-	"test.run":         "Run tests",
-	"repo.search":      "Search repo",
-	"codebase.search":  "Codebase searches",
-	"json.query":       "Query JSON",
-	"csv.inspect":      "Inspect CSVs",
-	"web.search":       "Search web",
-	"git.status":       "Git status",
-	"symbols":          "Symbols",
-	"symbols.find":     "Find symbols",
-	"todos":            "Update todos",
-	"agent.output":     "Peek at subagents",
-	"agent.kill":       "Cancel subagents",
-	"agent.steer":      "Steer subagents",
-	"agent.run":        "Run subagents",
-	// Self-plural: grouped headings must not render "Waiting…s".
-	"agent.await": "Waiting…",
-}
-
-// pluralizeToolName returns the display name for a tool in plural form,
-// suitable for grouped tool call headings (e.g. "Read file" → "Read files",
-// "Run command" → "Run commands"). Known tools come from toolPluralNames;
-// everything else falls back to English's regular rules.
-func pluralizeToolName(name string) string {
-	if plural, ok := toolPluralNames[name]; ok {
-		return plural
-	}
-	singular := DisplayToolName(name)
-	switch {
-	case singular == "":
-		return ""
-	// Already plural (or a non-count noun): leave it alone. Appending to
-	// these is what produced "Run testses" and "Find symbolses".
-	case strings.HasSuffix(singular, "s"):
-		return singular
-	case strings.HasSuffix(singular, "x"), strings.HasSuffix(singular, "z"),
-		strings.HasSuffix(singular, "ch"), strings.HasSuffix(singular, "sh"):
-		return singular + "es"
-	case len(singular) >= 2 && strings.HasSuffix(singular, "y") && !isVowel(singular[len(singular)-2]):
-		return singular[:len(singular)-1] + "ies"
-	default:
-		return singular + "s"
-	}
-}
-
-func isVowel(b byte) bool {
-	return strings.IndexByte("aeiouAEIOU", b) >= 0
-}
```

**`internal/app/tui/toolnames_test.go`.** The pluralizer is gone.

```diff
diff --git a/internal/app/tui/toolnames_test.go b/internal/app/tui/toolnames_test.go
--- a/internal/app/tui/toolnames_test.go
+++ b/internal/app/tui/toolnames_test.go
@@ -46,33 +46,3 @@ func TestDisplayToolName(t *testing.T) {
 		}
 	}
 }
-
-// Grouped tool headings read as English. The old rule ("append s, unless it
-// ends in s/x, then append es") produced "Run testses", "Find symbolses",
-// "Apply patchs" and "Codebase searchs".
-func TestPluralizeToolName(t *testing.T) {
-	cases := map[string]string{
-		// Self-plural: a grouped heading must not render "Waiting…s".
-		"agent.await":      "Waiting…",
-		"file.read":        "Read files",
-		"file.write_patch": "Edit files",
-		"patch.apply":      "Apply patches",
-		"shell.run":        "Run commands",
-		"test.run":         "Run tests",
-		"symbols.find":     "Find symbols",
-		"todos":            "Update todos",
-		"codebase.search":  "Codebase searches",
-		"web.fetch":        "Fetch pages",
-		"git.status":       "Git status",
-		"agent.run":        "Run subagents",
-		// Unknown tools fall through to the regular rules.
-		"custom.box":   "Custom boxes",
-		"custom.entry": "Custom entries",
-		"custom.thing": "Custom things",
-	}
-	for name, want := range cases {
-		if got := pluralizeToolName(name); got != want {
-			t.Errorf("pluralizeToolName(%q) = %q, want %q", name, got, want)
-		}
-	}
-}
```

**`internal/app/tui/transcript.go`.** Comment only.

```diff
diff --git a/internal/app/tui/transcript.go b/internal/app/tui/transcript.go
--- a/internal/app/tui/transcript.go
+++ b/internal/app/tui/transcript.go
@@ -608,7 +608,7 @@ func renderTranscriptItemIn(item session.TranscriptItem, detailExpanded bool, sp
 }
 
 // A finished job is not a row of its own any more: its card is the shell.run
-// that started it, settled in place (steps.go renderJobCard), and the moment
+// that started it, settled in place (spine.go renderJobRow), and the moment
 // it finished is a completion marker. renderJobExit lived here.
 
 // subagentTailBudget is how many logical tail lines to pull from a running
```

**`internal/app/tui/view_test.go`.** The tag instead of the divider.

```diff
diff --git a/internal/app/tui/view_test.go b/internal/app/tui/view_test.go
--- a/internal/app/tui/view_test.go
+++ b/internal/app/tui/view_test.go
@@ -651,11 +651,11 @@ func TestTodoPanelIsPinnedBelowTranscript(t *testing.T) {
 	if inputRow >= 0 && todoRow > inputRow {
 		t.Fatalf("todo progress row must render above the input row (todo=%d input=%d)", todoRow, inputRow)
 	}
-	// The transcript carries the transition divider — the work's own heading —
-	// while the band carries the counts. Neither duplicates the other.
+	// The transcript tags the beat where the work moved to the todo, while
+	// the band names it and carries the counts.
 	transcript := stripANSI(m.viewport.View())
-	if !strings.Contains(transcript, "2/3 implement parser") {
-		t.Fatalf("the transcript should carry the todo divider:\n%s", transcript)
+	if !strings.Contains(transcript, "▸ 2/3") {
+		t.Fatalf("the transcript should tag the todo's first beat:\n%s", transcript)
 	}
 }
 
```

**`internal/app/tui/viewport_render.go`.** No thinking rows inside beats.

```diff
diff --git a/internal/app/tui/viewport_render.go b/internal/app/tui/viewport_render.go
--- a/internal/app/tui/viewport_render.go
+++ b/internal/app/tui/viewport_render.go
@@ -287,8 +287,7 @@ func (m *Model) drawNode(n *viewmodel.Node, c *stepRenderCtx, width int) (string
 	return "", nil
 }
 
-// rowBeats maps every row inside a beat, thinking rows included, to that
-// beat.
+// rowBeats maps every row inside a beat to that beat.
 func rowBeats(turns []*viewmodel.Node) map[viewmodel.NodeID]viewmodel.NodeID {
 	out := map[viewmodel.NodeID]viewmodel.NodeID{}
 	for _, turn := range turns {
@@ -299,9 +298,6 @@ func rowBeats(turns []*viewmodel.Node) map[viewmodel.NodeID]viewmodel.NodeID {
 			for _, row := range n.Children {
 				out[row.ID] = n.ID
 			}
-			for _, t := range n.Step.Thinking {
-				out[viewmodel.ThinkingID(t)] = n.ID
-			}
 		}
 	}
 	return out
@@ -416,12 +412,6 @@ func (m *Model) foldNodeSig(h sigWriter, n *viewmodel.Node, c *stepRenderCtx) {
 	if lines, ok := m.callers[n.ID]; ok {
 		fmt.Fprintf(h, "c%q|", lines)
 	}
-	if n.Step != nil {
-		for _, t := range n.Step.Thinking {
-			tov, thas := m.ownExpanded(viewmodel.ThinkingID(t))
-			fmt.Fprintf(h, "t%v|%v|", thas, tov)
-		}
-	}
 	for _, ch := range n.Children {
 		m.foldNodeSig(h, ch, c)
 	}
```

**Verify:**

```bash
TMPDIR=/private/tmp/pa-suite go test ./internal/app/tui/...   # ok apart from the known failure
gofmt -l internal/app/tui   # no output
```

Commit: `p-a-renderer-foundation: task 8 — Beats on the spine`.

## Task 9: Full verification, docs and changelog

**Goal:** the whole tree is verified, the README and changelog describe the new transcript, and the spec and this plan are committed with the work.

**Files:**

- `README.md`
- `CHANGELOG.md`
- `docs/tui-simplification/` (the spec, the design rounds and this plan; untracked until now)

**Steps:**

1. In `README.md`, under "### The TUI", replace the second and third sentences of the first paragraph ("The transcript is hierarchical — … for any single step.") with:

   ```markdown
   The transcript reads as beats: each tool call hangs off the narration that
   explains it, as one row naming what it did and what came of it, and the beat
   that starts a todo carries its number. Nothing folds: `Enter` or a click opens
   a row, `Ctrl+G` opens everything, and a keyboard browse mode (`Esc`) and a full
   inspector (`i`) give detail on demand.
   ```

2. In `CHANGELOG.md`, under `## [Unreleased]`:
   - under `### Changed`, before "**Tool-usage prompting and tool architecture docs**", add:

     ```markdown
     **Transcript: beats on a spine**

     - Each tool call hangs off the narration that explains it (`├ └`) as one
       row: a verb, what it acted on, and its result on the right. An edit is one
       line until it is expanded.
     - The beat that starts a todo carries `▸ N/M` in its head, and a plan line
       (`✓ ✓ ▸ ·  3/4  <todo>`) sits where the agent updated the list. The todo
       dividers are gone, and a list of any length is tracked.
     - Thinking time moves into the beat's head (`thought 2s`); expanding the beat
       shows the reasoning.
     - One level of detail: nothing folds. `Enter` in browse mode or a click
       expands one row (a beat opens with its rows), and `Ctrl+G` toggles
       expand-all. The outline, steps and full density levels and the `z` fold
       toggle are gone.
     - Dragging over a call row copies what it acted on: the command, path or
       query.
     ```

   - under `### Removed`, add as the first bullet:

     ```markdown
     - `[tui.transcript] density` and `fold_finished_tasks`, with the beat folding
       and density levels they configured. Files that set them still load, with a
       warning that they are ignored.
     ```

3. Run the verification below.
4. Commit `README.md`, `CHANGELOG.md` and `docs/tui-simplification/` together. Name the paths with `git add`; never use `git add -A` in the main checkout, which holds unrelated untracked work.

**Verify:**

```bash
mkdir -p /private/tmp/pa-suite
TMPDIR=/private/tmp/pa-suite CGO_ENABLED=1 go test ./...   # passes apart from the base failures under Assumptions
go vet ./...                                               # clean
gofmt -l .                                                 # no output
CGO_ENABLED=1 go build -o marshal ./cmd/marshal
go test ./test/usability/... -run TestScripted -v -timeout 5m   # passes
```

Then run the binary in a scratch repository with a model configured, and ask for a small change that writes a todo list. Check the frames against [framed-console.md](../framed-console.md):
- the head tag on each todo's first beat;
- the plan line;
- `├ └` rows with verbs;
- Enter, a click and Ctrl+G expanding.

Commit: `p-a-renderer-foundation: task 9 — verification, docs and changelog`.

## Integration notes

- **P-B** builds on this:
  - `renderMarker` becomes `renderCompletion`, and `renderJobRow` loses its live state and reads `┆ job-1 started`.
  - The web renders `planUpdate`, the tag and its own spine in `TurnNode.svelte`. It drops its inert fold code (`step.folded`, `foldTasks`, `unfolded`) and its todo divider.
  - In the projection: the band's `DrivenByTodos` gate, `MinTaskTodos` and `taskMode` go, along with the in-place cards and the `cardIndent` path in `renderStep`.
- **P-C** draws the live beat in the box with `renderStep` and the spine renderers as they are here. `progressBlocks` moves out of `nowbar.go` with the other helpers. `toolCategoryGlyph` and the category glyph constants are deleted with `nowbar.go` and the subagent card body.
- **Daemon P2 plan 3** records this renderer as its T6 goldens. C6's `WireCall.head` was specified as today's `toolRowHead`; a client drawing this renderer needs `callSubject` and `callResult` instead, so plan 3 should carry those two (or the verb, subject and result) on `WireCall`.
- **P-D** adds a skill listing line to the system prompt. `TestDefaultPromptBudget` already fails on main (9,118 bytes against a 9,000 ceiling, since the interaction-modes merge), so P-D must fix the budget first or account for its line.

## Plan self-review

- **Self-contained tasks:** each task has its own Verify and commits alone. Task 7 is a pure refactor whose guard is the unchanged renderer tests.
- **Cold-executor test:** every judgment is made in the Steps, and every change is in the diffs. Prose steps name the functions, their new behaviour and every test added, renamed or removed.
- **Contracts:** the declarations later tasks use (the plan row, the wire kind, the expand state, `TranscriptIgnoredBy`, the body helpers) are in Contracts.
- **Parallel markers:** only Task 6 is marked, and it shares no file with any other task.
- **Anchors:** every path and symbol comes from the prototype at the base commit. The diffs are its commits.
- **Code:** each task compiled, passed its Verify and was `gofmt`-clean in the prototype.
- **Commands:** the commands follow AGENTS.md (CGO, narrow packages first, the usability binary at the repo root).
- **Placeholders:** none.
- **Against the spec:** the spec was updated for every decision above. P-A no longer contradicts it.
