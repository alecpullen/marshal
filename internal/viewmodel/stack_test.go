package viewmodel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

var t0 = time.Unix(10_000, 0)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

func userMsg(id int64, s int) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindMessage,
		Message: &session.Message{ID: id, Role: session.RoleUser, Content: "go", CreatedAt: at(s)}}
}

func narration(id int64, step session.StepID, s int, text string) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindMessage,
		Message: &session.Message{ID: id, Role: session.RoleAssistant, ContentType: session.ContentTypeNarration, Content: text, StepID: step, CreatedAt: at(s)}}
}

func final(id int64, s int) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindMessage,
		Message: &session.Message{ID: id, Role: session.RoleAssistant, Content: "done", Final: true, CreatedAt: at(s)}}
}

func audit(tool string, step session.StepID, callID string, s int) session.TranscriptItem {
	ev := registry.AuditEvent{Timestamp: at(s), ToolName: tool, StepID: step, ToolCallID: callID, ResultSummary: "ok"}
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindAudit, Audit: &ev}
}

func thinking(step session.StepID, s int) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindThinking,
		Thinking: &session.ThinkingEntry{Text: "hm", StartedAt: at(s), StepID: step}}
}

func step(id session.StepID, start, end int, role string) session.Step {
	st := session.Step{ID: id, StartedAt: at(start), Actor: session.Actor{Role: role}}
	if end > 0 {
		st.EndedAt = at(end)
	}
	return st
}

// shape renders a tree as an indented outline for assertions.
func shape(nodes []*Node) string {
	var b strings.Builder
	var walk func(ns []*Node, depth int)
	walk = func(ns []*Node, depth int) {
		for _, n := range ns {
			if n.Kind == KindReceipt {
				continue // asserted on directly; keeps older outlines readable
			}
			extra := ""
			if n.Live {
				extra = " live"
			}
			if len(n.Tools) > 1 {
				extra += fmt.Sprintf(" x%d", len(n.Tools))
			}
			fmt.Fprintf(&b, "%s%s%s\n", strings.Repeat("  ", depth), n.ID.Key, extra)
			walk(n.Children, depth+1)
		}
	}
	walk(nodes, 0)
	return b.String()
}

func TestBuildGroupsByStepID(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Reading the parser."),
			audit("file.read", 1, "c1", 2),
			audit("search.text", 1, "c2", 3),
			final(3, 5),
		},
		Steps: []session.Step{step(1, 1, 4, "")},
	})
	want := `turn:1
  msg:1
  step:1
    tool:1:c1
    tool:1:c2
  msg:3
`
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	st := nodes[0].Children[1]
	if st.Kind != KindStep || len(st.Step.Narration) != 1 || st.Step.Step.ID != 1 || len(st.Step.StepIDs) != 1 || st.Step.StepIDs[0] != 1 {
		t.Fatalf("step node = %+v", st.Step)
	}
	if nodes[0].Children[2].Kind != KindFinal {
		t.Errorf("assistant final should be KindFinal, got %v", nodes[0].Children[2].Kind)
	}
}

func TestBuildMergesSameToolRunsWithinAStepOnly(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			audit("file.read", 1, "a", 1), audit("file.read", 1, "b", 2), audit("file.read", 1, "c", 3),
			audit("file.read", 2, "d", 5),
		},
		Steps: []session.Step{step(1, 1, 4, ""), step(2, 5, 6, "")},
	})
	want := `turn:1
  msg:1
  step:1
    tool:1:a x3
    tool:2:d
`
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildDoesNotMergeFailuresOrEdits(t *testing.T) {
	failed := audit("file.read", 1, "b", 2)
	failed.Audit.Error = "boom"
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), audit("file.read", 1, "a", 1), failed, audit("file.write_patch", 1, "e1", 3), audit("file.write_patch", 1, "e2", 4)},
		Steps: []session.Step{step(1, 1, 5, "")},
	})
	if got := len(nodes[0].Children[1].Children); got != 4 {
		t.Fatalf("rows = %d, want 4 unmerged (failure and edits keep their own rows)", got)
	}
}

func TestBuildHeuristicStepsForLegacySessions(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0),
		audit("file.read", 0, "", 1), // before any narration: a loose beat
		narration(2, 0, 2, "Checking the guard."),
		audit("search.text", 0, "", 3),
		thinking(0, 4),
		narration(3, 0, 5, "Now the tests."),
		audit("test.run", 0, "", 6),
		final(4, 7),
	}})
	want := fmt.Sprintf("turn:1\n  msg:1\n  hstep:%d\n    tool:%d\n  hstep:%d\n    tool:%d\n  hstep:%d\n    tool:%d\n  msg:4\n", at(1).UnixNano(), at(1).UnixNano(), at(2).UnixNano(), at(3).UnixNano(), at(5).UnixNano(), at(6).UnixNano())
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	var steps []*Node
	for _, c := range nodes[0].Children {
		if c.Kind == KindStep {
			steps = append(steps, c)
		}
	}
	if len(steps) != 3 {
		t.Fatalf("loose beats = %d, want 3 (unnarrated, guard, tests):\n%s", len(steps), shape(nodes))
	}
	for _, s := range steps {
		if !strings.HasPrefix(s.ID.Key, "hstep:") {
			t.Errorf("step %+v is not a loose beat", s.ID)
		}
	}
	if len(steps[0].Step.Narration) != 0 || len(steps[0].Children) != 1 || len(steps[0].Step.StepIDs) != 0 {
		t.Errorf("first beat should be the step-less read: %+v", steps[0])
	}
	if len(steps[1].Children) != 1 || len(steps[1].Step.Thinking) != 1 {
		t.Errorf("guard beat should own the search and the thought: %s", shape(nodes))
	}
}

func TestBuildHeuristicStepEndsAtAnotherMessage(t *testing.T) {
	sys := session.TranscriptItem{Timestamp: at(3), Kind: session.KindMessage,
		Message: &session.Message{ID: 9, Role: session.RoleSystem, Content: "notice"}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 0, 1, "A."), audit("file.read", 0, "", 2), sys, audit("file.read", 0, "", 4),
	}})
	want := fmt.Sprintf("turn:1\n  msg:1\n  hstep:%d\n    tool:%d\n  msg:9\n  hstep:%d\n    tool:%d\n", at(1).UnixNano(), at(2).UnixNano(), at(4).UnixNano(), at(4).UnixNano())
	if got := shape(nodes); got != want {
		t.Fatalf("a system message must close the loose beat:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildMixedStepIDsAndLegacy(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 0, 1, "Legacy."), audit("file.read", 0, "", 2),
			narration(3, 1, 4, "Stamped."), audit("file.read", 1, "c", 5),
			audit("shell.run", 0, "tail", 6),
		},
		Steps: []session.Step{step(1, 4, 6, "")},
	})
	want := fmt.Sprintf("turn:1\n  msg:1\n  hstep:%d\n    tool:%d\n  step:1\n    tool:1:c\n    tool:tail\n", at(1).UnixNano(), at(2).UnixNano())
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildStepWithoutRecordGetsZeroActor(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 7, 1, "Orphan."), audit("file.read", 7, "c", 2)}})
	st := nodes[0].Children[1]
	if st.Kind != KindStep || st.Step.Step.ID != 0 || st.Step.Step.Actor.Role != "" {
		t.Fatalf("orphan step = %+v", st.Step)
	}
	if len(st.Step.StepIDs) != 1 || st.Step.StepIDs[0] != 7 {
		t.Fatalf("orphan step IDs = %v, want [7]", st.Step.StepIDs)
	}
}

func TestBuildOmitsEmptySteps(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), final(2, 3)},
		Steps: []session.Step{step(1, 1, 2, "")}, // a nudged retry: no narration, tools or thinking
	})
	if got := shape(nodes); strings.Contains(got, "step:") {
		t.Fatalf("empty step rendered:\n%s", got)
	}
}

func TestBuildPreambleTurn(t *testing.T) {
	sys := session.TranscriptItem{Timestamp: at(0), Kind: session.KindMessage,
		Message: &session.Message{ID: 1, Role: session.RoleSystem, Content: "boot"}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{sys, userMsg(2, 1)}})
	if len(nodes) != 2 || nodes[0].ID.Key != "turn:pre" || nodes[1].ID.Key != "turn:2" {
		t.Fatalf("turns = %s", shape(nodes))
	}
}

func TestBuildSteeringMessageDoesNotOpenATurn(t *testing.T) {
	steer := session.TranscriptItem{Timestamp: at(2), Kind: session.KindMessage,
		Message: &session.Message{ID: 5, Role: session.RoleUser, ContentType: session.ContentTypeSteering, Content: "aside"}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), steer}})
	if len(nodes) != 1 {
		t.Fatalf("steering opened a turn: %s", shape(nodes))
	}
}

func TestBuildLiveStepWithThinkingBelowIt(t *testing.T) {
	snap := Snapshot{
		Items:      []session.TranscriptItem{userMsg(1, 0), narration(2, 2, 3, "Now this."), audit("file.read", 1, "a", 2)},
		Steps:      []session.Step{step(1, 1, 2, ""), step(2, 3, 0, "")},
		Busy:       true,
		InProgress: session.InProgressMessage{Active: true, Reasoning: "thinking hard", StartedAt: at(4)},
		ActiveTools: []session.ActiveToolCall{
			{Name: "shell.run", ToolCallID: "s", StepID: 2, StartedAt: at(4)},
			{Name: "file.read", ToolCallID: "a", StepID: 1, StartedAt: at(1)}, // already audited
		},
	}
	nodes := Build(snap)
	got := shape(nodes)
	want := `turn:1
  msg:1
  step:1
    tool:1:a
  step:2 live
    tool:2:s live
  think:live live
`
	if got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	children := nodes[0].Children
	last := children[len(children)-1]
	if last.ID != LiveThinkingID || last.Step.LiveThinking != "thinking hard" {
		t.Errorf("the turn's last child must be the live reasoning node, got %+v", last.ID)
	}
	if children[2].Step.LiveThinking != "" {
		t.Errorf("no beat carries the streaming reasoning, got %q", children[2].Step.LiveThinking)
	}
	snap.Busy = false
	snap.InProgress.Active = false
	for _, n := range Build(snap)[0].Children {
		if n.Live {
			t.Errorf("nothing is live when idle: %s", n.ID.Key)
		}
	}
}

// The streaming reasoning is the turn's last child: below the live beat, below
// the completion event of the work that beat launched, and below every earlier
// beat. No beat carries it.
func TestLiveThinkingIsTheTurnsLastChild(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "First beat."),
			audit("file.read", 1, "a", 2),
			narration(3, 2, 3, "Second beat."),
			jobStart(2, "j", 4, "job-4"),
			jobExit(5, "job-4", 0),
		},
		Steps:      []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 0)},
		Busy:       true,
		InProgress: session.InProgressMessage{Active: true, Reasoning: "weighing", StartedAt: at(6)},
		ActiveTools: []session.ActiveToolCall{
			{Name: "shell.run", ToolCallID: "s", StepID: 2, StartedAt: at(6)},
		},
	})
	children := nodes[0].Children
	if len(children) < 3 {
		t.Fatalf("children = %s", shape(nodes))
	}
	tail := children[len(children)-3:]
	if tail[0].ID.Key != "step:2" || tail[1].ID.Key != "completion:tool:2:j" || tail[2].ID != LiveThinkingID {
		t.Fatalf("turn must end with the live beat, its completion, then the reasoning:\n%s", shape(nodes))
	}
	for _, n := range children {
		if n.ID != LiveThinkingID && n.Step != nil && n.Step.LiveThinking != "" {
			t.Errorf("beat %s carries streaming reasoning", n.ID.Key)
		}
	}
}

// A step that has opened but holds only streaming reasoning is empty: it gets
// no node, and the reasoning shows at the bottom of the turn.
func TestStepWithOnlyReasoningIsOmitted(t *testing.T) {
	nodes := Build(Snapshot{
		Items:      []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "First beat."), audit("file.read", 1, "a", 2)},
		Steps:      []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 4, 0)},
		Busy:       true,
		InProgress: session.InProgressMessage{Active: true, Reasoning: "weighing", StartedAt: at(5)},
	})
	got := shape(nodes)
	if strings.Contains(got, "step:2") {
		t.Fatalf("a step holding only streaming reasoning is omitted:\n%s", got)
	}
	children := nodes[0].Children
	if last := children[len(children)-1]; last.ID != LiveThinkingID {
		t.Fatalf("the reasoning must be the turn's last child:\n%s", got)
	}
}

func TestBuildLiveThinkingBeforeAnyStepIsPassthrough(t *testing.T) {
	nodes := Build(Snapshot{
		Items:      []session.TranscriptItem{userMsg(1, 0)},
		Busy:       true,
		InProgress: session.InProgressMessage{Active: true, Reasoning: "hm", StartedAt: at(1)},
	})
	last := nodes[0].Children[len(nodes[0].Children)-1]
	if last.ID != LiveThinkingID || !last.Live {
		t.Fatalf("expected live-thinking pass-through, got %+v", last.ID)
	}
}

func TestBuildLiveThinkingOnlyInLatestTurn(t *testing.T) {
	for _, withStep := range []bool{false, true} {
		t.Run(fmt.Sprintf("withStep=%t", withStep), func(t *testing.T) {
			snap := Snapshot{
				Items:      []session.TranscriptItem{userMsg(1, 0), final(2, 1), userMsg(3, 2)},
				Busy:       true,
				InProgress: session.InProgressMessage{Active: true, Reasoning: "current reasoning", StartedAt: at(3)},
			}
			if withStep {
				snap.Steps = []session.Step{step(1, 3, 0, "")}
				snap.Steps[0].TurnMsgID = 3
			}
			turns := Build(snap)
			count := 0
			var visit func(*Node, bool)
			visit = func(n *Node, latest bool) {
				if n.Step != nil && n.Step.LiveThinking != "" {
					count++
					if !latest {
						t.Fatal("current reasoning appeared in a previous turn")
					}
				}
				for _, ch := range n.Children {
					visit(ch, latest)
				}
			}
			for i, turn := range turns {
				visit(turn, i == len(turns)-1)
			}
			if count != 1 {
				t.Fatalf("got %d live reasoning blocks, want 1", count)
			}
		})
	}
}

func TestBuildUnstampedActiveCallStandsAlone(t *testing.T) {
	nodes := Build(Snapshot{
		Items:       []session.TranscriptItem{userMsg(1, 0)},
		Busy:        true,
		ActiveTools: []session.ActiveToolCall{{Name: "shell.run", StartedAt: at(1)}},
	})
	last := nodes[0].Children[len(nodes[0].Children)-1]
	if last.Kind != KindTool || last.Active == nil || !last.Live {
		t.Fatalf("unstamped active call should render as a live pass-through row: %+v", last)
	}
}

func TestNodeKeysAreStableAcrossRebuilds(t *testing.T) {
	snap := Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "x"), audit("file.read", 1, "a", 2), audit("file.read", 1, "b", 3),
			narration(3, 0, 5, "legacy"), audit("search.text", 0, "", 6), final(4, 7),
		},
		Steps: []session.Step{step(1, 1, 4, "")},
	}
	first := shape(Build(snap))
	// Append a later item: earlier keys must not change.
	snap.Items = append(snap.Items, audit("shell.run", 0, "", 8))
	second := shape(Build(snap))
	for _, line := range strings.Split(strings.TrimSpace(first), "\n") {
		if !strings.Contains(second, strings.TrimSpace(line)) {
			t.Errorf("key %q vanished after an unrelated append:\n%s", strings.TrimSpace(line), second)
		}
	}
	if first != shape(Build(Snapshot{Items: snap.Items[:len(snap.Items)-1], Steps: snap.Steps})) {
		t.Error("rebuilding the same snapshot must give identical keys")
	}
}

func TestVersionChangesWithPayload(t *testing.T) {
	mk := func(summary string) uint64 {
		it := audit("file.read", 1, "a", 2)
		it.Audit.ResultSummary = summary
		n := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), it}, Steps: []session.Step{step(1, 1, 3, "")}})
		return n[0].Children[1].Children[0].Version
	}
	if mk("one") == mk("two") {
		t.Fatal("tool node version must change when its summary does")
	}
	if mk("same") != mk("same") {
		t.Fatal("version must be deterministic")
	}
}

// Providers reuse call IDs like call_0 across responses; the rows must still
// get distinct identities or they share expanded state and callers.
func TestToolKeysAreScopedToTheirStep(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			audit("file.write_patch", 1, "call_0", 1),
			audit("file.write_patch", 2, "call_0", 5),
		},
		Steps: []session.Step{step(1, 1, 4, ""), step(2, 5, 6, "")},
	})
	beat := nodes[0].Children[1]
	if len(beat.Children) != 2 {
		t.Fatalf("merged beat children = %d, want 2", len(beat.Children))
	}
	a := beat.Children[0].ID
	b := beat.Children[1].ID
	if a == b {
		t.Fatalf("two merged responses' call_0 rows share identity %v", a)
	}
}

func stepOf(id session.StepID, turn int64, start, end int) session.Step {
	st := step(id, start, end, "")
	st.TurnMsgID = turn
	return st
}

// A step the runner has just opened has no narration yet. A nearby legacy
// step-less item remains the loose beat and absorbs the stamped live call.
func TestBuildPlacesCallInFreshStepDespiteStepLessItem(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), audit("git.rollback", 0, "", 1)},
		Steps: []session.Step{stepOf(1, 1, 2, 0)},
		Busy:  true,
		ActiveTools: []session.ActiveToolCall{
			{Name: "shell.run", ToolCallID: "r", StepID: 1, StartedAt: at(3)},
		},
	})
	got := shape(nodes)
	if !strings.Contains(got, "hstep:") || !strings.Contains(got, "tool:1:r live") {
		t.Fatalf("the live call must sit in the merged loose beat:\n%s", got)
	}
	if !nodes[0].Children[1].Live {
		t.Fatalf("the merged loose beat must be live:\n%s", got)
	}
}

// Saved timestamps lose sub-second precision, so on resume a step's last tool
// row can sort after the next user message. It still belongs to its own turn.
func TestBuildKeepsStepTogetherWhenAuditSortsIntoNextTurn(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			audit("file.read", 1, "a", 1),
			userMsg(2, 3),
			audit("file.read", 1, "b", 4), // belongs to step 1 / turn 1
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4)},
	})
	if len(nodes) != 2 {
		t.Fatalf("turns = %d", len(nodes))
	}
	if !strings.Contains(shape(nodes[:1]), "tool:1:a x2") {
		t.Fatalf("turn 1 lost the step's last row:\n%s", shape(nodes))
	}
	if strings.Contains(shape(nodes[1:]), "tool:1:b") {
		t.Fatalf("turn 2 must not own step 1's row:\n%s", shape(nodes))
	}
}

func TestMergedRunKeepsFirstCallsID(t *testing.T) {
	one := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), audit("file.read", 1, "a", 1)}, Steps: []session.Step{stepOf(1, 1, 1, 2)}})
	two := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), audit("file.read", 1, "a", 1), audit("file.read", 1, "b", 2)}, Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	a, b := one[0].Children[1].Children[0], two[0].Children[1].Children[0]
	if len(b.Tools) != 2 || a.ID != b.ID {
		t.Fatalf("merging changed the row identity: %v -> %v (tools %d)", a.ID, b.ID, len(b.Tools))
	}
}

// subagentDispatch is the audit a subagent launch records: the legacy summary
// text, plus the structured notice that names the child's stable work.
func subagentDispatch(step session.StepID, callID string, s int, taskKey string, agentID int64) session.TranscriptItem {
	it := audit("agent.run", step, callID, s)
	it.Audit.Notice = &registry.ToolNotice{
		Kind: registry.NoticeSubagentWork,
		Data: map[string]any{registry.NoticeSubagentWork: registry.SubagentWorkReference{
			TaskKey: taskKey, RuntimeEpoch: "epoch-1", AgentID: agentID, ChildSessionID: "child",
		}},
	}
	return it
}

func countWorkKey(nodes []*Node, key string) int {
	n := 0
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, x := range ns {
			if x.WorkKey == key {
				n++
			}
			walk(x.Children)
		}
	}
	walk(nodes)
	return n
}

// The transcript keeps the actual dispatch once — as its own agent.run row
// carrying the notice's work key — and never places a subagent child box.
func TestDispatchShownOnceWithoutChildBox(t *testing.T) {
	dispatch := subagentDispatch(1, "r", 2, "work-abc", 3)
	card := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "Task 1", StartedAt: at(2), Status: session.SubagentRunning}}
	items := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Dispatch."), dispatch, card}
	steps := []session.Step{stepOf(1, 1, 1, 3)}

	nodes := Build(Snapshot{Items: items, Steps: steps})
	if findNode(nodes, "sub:3") != nil {
		t.Fatalf("the transcript must omit the parent child box:\n%s", shape(nodes))
	}
	row := findNode(nodes, "tool:1:r")
	if row == nil || row.WorkKey != "work-abc" {
		t.Fatalf("dispatch row = %+v, want exactly one row carrying the notice's work key", row)
	}
	if n := countWorkKey(nodes, "work-abc"); n != 1 {
		t.Fatalf("dispatch rows with the work key = %d, want exactly one", n)
	}
}

// The retained report message stays reachable.
func TestReportStaysReachable(t *testing.T) {
	dispatch := subagentDispatch(1, "r", 2, "work-abc", 3)
	report := session.TranscriptItem{Timestamp: at(4), Kind: session.KindMessage,
		Message: &session.Message{ID: 9, Role: session.RoleUser, ContentType: session.ContentTypeSubagentReport,
			Content: "[subagent 3 finished] done", CreatedAt: at(4)}}
	items := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Dispatch."), dispatch, report}
	steps := []session.Step{stepOf(1, 1, 1, 5)}

	nodes := Build(Snapshot{Items: items, Steps: steps})
	if findNode(nodes, "msg:9") == nil {
		t.Fatalf("the persisted report message must remain a reachable row:\n%s", shape(nodes))
	}
}

// A subagent item is never a node of its own: a running one is only the band's
// entity, and a finished one is a completion event.
func TestSubagentItemIsNeverANode(t *testing.T) {
	running := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "Task 1", StartedAt: at(2), Status: session.SubagentRunning}}
	finished := session.TranscriptItem{Timestamp: at(4), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 4, Label: "Task 2", StartedAt: at(3), EndedAt: at(4), Status: session.SubagentDone}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Dispatch."), running, finished},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	completions := 0
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Kind == KindSubagent {
				t.Fatalf("a subagent item must never become a node:\n%s", shape(nodes))
			}
			if n.Kind == KindCompletion {
				completions++
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	if completions != 1 {
		t.Fatalf("completions = %d, want 1 for the finished subagent:\n%s", completions, shape(nodes))
	}
}

// systemMsg is a step-less system message of the given content type, the shape
// a skill load writes beside its call row.
func systemMsg(id int64, s int, ct session.ContentType, text string) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindMessage,
		Message: &session.Message{ID: id, Role: session.RoleSystem, ContentType: ct, Content: text, CreatedAt: at(s)}}
}

// A skill load writes a hidden body and a one-line tag beside its call row.
// Neither carries a step ID, so both would draw a turn-level row and end the
// beat; the call row is the trace, so both are dropped.
func TestSkillLoadLeavesOneRow(t *testing.T) {
	load := audit("skill.load", 1, "s", 3)
	load.Audit.Args = []byte(`{"name":"debug"}`)
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Loading the debug skill."),
			systemMsg(3, 2, session.ContentTypeSkillBody, "the whole skill body"),
			systemMsg(4, 2, session.ContentTypeSkill, "debug"),
			load,
			audit("file.read", 2, "r", 5),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 5, 6)},
	})
	want := `turn:1
  msg:1
  step:1
    tool:1:s
    tool:2:r
`
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
}

// Two different skills loaded in one turn: both call rows survive, and each
// tag is dropped because its own name has a matching successful load here.
func TestTwoSkillLoadsLeaveBothCallRows(t *testing.T) {
	loadA := audit("skill.load", 1, "a", 3)
	loadA.Audit.Args = []byte(`{"name":"alpha"}`)
	loadB := audit("skill.load", 2, "b", 6)
	loadB.Audit.Args = []byte(`{"name":"beta"}`)
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Loading alpha."),
			systemMsg(3, 2, session.ContentTypeSkillBody, "the alpha body"),
			systemMsg(4, 2, session.ContentTypeSkill, "alpha"),
			loadA,
			narration(5, 2, 5, "Loading beta."),
			systemMsg(6, 2, session.ContentTypeSkillBody, "the beta body"),
			systemMsg(7, 2, session.ContentTypeSkill, "beta"),
			loadB,
			audit("file.read", 2, "r", 7),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 5, 8)},
	})
	want := `turn:1
  msg:1
  step:1
    tool:1:a
  step:2
    tool:2:b
    tool:2:r
`
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
}

// A tag for a skill this turn loaded twice still drops once: each load keeps
// its own call row, and the tag never draws a duplicate row.
func TestRepeatedSkillLoadKeepsOneRowPerLoad(t *testing.T) {
	first := audit("skill.load", 1, "s", 3)
	first.Audit.Args = []byte(`{"name":"debug"}`)
	second := audit("skill.load", 2, "s2", 6)
	second.Audit.Args = []byte(`{"name":"debug"}`)
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Loading the debug skill."),
			systemMsg(3, 2, session.ContentTypeSkillBody, "the whole skill body"),
			systemMsg(4, 2, session.ContentTypeSkill, "debug"),
			first,
			narration(5, 2, 5, "Loading the debug skill again."),
			second,
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 5, 7)},
	})
	want := `turn:1
  msg:1
  step:1
    tool:1:s
  step:2
    tool:2:s2
`
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
}

// A tag with no call row in the turn (the skills panel, an ACP client) is the
// only trace of the load, so it stays.
func TestSkillTagWithoutACallStays(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			systemMsg(2, 1, session.ContentTypeSkill, "debug"),
		},
	})
	if got := shape(nodes); !strings.Contains(got, "msg:2") {
		t.Fatalf("a tag with no call row must stay a message:\n%s", got)
	}
}

// A failed load has no successful call row to stand in for the tag.
func TestFailedSkillLoadKeepsItsTag(t *testing.T) {
	load := audit("skill.load", 1, "s", 2)
	load.Audit.Args = []byte(`{"name":"debug"}`)
	load.Audit.Error = "boom"
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			systemMsg(2, 1, session.ContentTypeSkill, "debug"),
			load,
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	if got := shape(nodes); !strings.Contains(got, "msg:2") {
		t.Fatalf("a failed load must keep its tag:\n%s", got)
	}
}
