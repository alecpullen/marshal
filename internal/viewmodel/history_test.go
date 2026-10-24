package viewmodel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
)

// historyStep is a step with its stored todo binding.
func historyStep(id session.StepID, turn int64, start, end int, todo string) session.Step {
	st := stepOf(id, turn, start, end)
	st.TodoID = todo
	return st
}

// historySubagent is the subagent item the plan's table names.
func historySubagent(status session.SubagentStatus, endedAt time.Time, summary string) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(13), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 1, Label: "review", Status: status, StartedAt: at(13), EndedAt: endedAt,
			Summary: summary, SubagentWorkFacts: session.SubagentWorkFacts{WorkKey: "task-1"}}}
}

func historySnap(items []session.TranscriptItem, steps []session.Step, active []session.ActiveToolCall, inProgress session.InProgressMessage, busy bool, todos []db.TodoItem) Snapshot {
	return Snapshot{
		Items:       append([]session.TranscriptItem(nil), items...),
		Steps:       append([]session.Step(nil), steps...),
		ActiveTools: append([]session.ActiveToolCall(nil), active...),
		InProgress:  inProgress,
		Busy:        busy,
		Todos:       append([]db.TodoItem(nil), todos...),
		Now:         at(30),
	}
}

// historySnapshots is one session in ten successive states, each extending the
// last: a plan, a dispatch, two jobs, a subagent, and a second turn.
func historySnapshots() []Snapshot {
	todos1 := []db.TodoItem{
		{ID: "a", Content: "A", Status: "in_progress"},
		{ID: "b", Content: "B", Status: "pending"},
		{ID: "c", Content: "C", Status: "pending"},
	}
	todos5 := []db.TodoItem{
		{ID: "a", Content: "A", Status: "completed", CompletedAt: at(10)},
		{ID: "b", Content: "B", Status: "in_progress"},
		{ID: "c", Content: "C", Status: "pending"},
		{ID: "d", Content: "D", Status: "pending"},
	}
	todos10 := []db.TodoItem{
		{ID: "b", Content: "B", Status: "completed", CompletedAt: at(22)},
		{ID: "c", Content: "C", Status: "in_progress"},
		{ID: "d", Content: "D", Status: "pending"},
	}

	items1 := []session.TranscriptItem{
		userMsg(1, 0),
		narration(2, 1, 1, "Planning."),
		planWrite(1, "w1", 2, `{"todos":[{"id":"a","content":"A","status":"in_progress"},{"id":"b","content":"B","status":"pending"},{"id":"c","content":"C","status":"pending"}]}`, "tasks updated · 3 items"),
	}
	steps1 := []session.Step{historyStep(1, 1, 1, 0, "")}

	steps2 := []session.Step{historyStep(1, 1, 1, 3, ""), historyStep(2, 1, 4, 0, "a")}
	active2 := []session.ActiveToolCall{{Name: "shell.run", StepID: 2, ToolCallID: "s", StartedAt: at(4)}}
	inProgress2 := session.InProgressMessage{Active: true, Reasoning: "Weighing the next step.", StartedAt: at(4)}

	items3 := append(append([]session.TranscriptItem(nil), items1...),
		jobStart(2, "s", 5, "job-1"), jobStart(2, "t", 5, "job-2"))
	steps3 := []session.Step{historyStep(1, 1, 1, 3, ""), historyStep(2, 1, 4, 6, "a")}

	items4 := append(append([]session.TranscriptItem(nil), items3...), jobExit(7, "job-1", 0))

	items5 := append(append([]session.TranscriptItem(nil), items4...),
		audit("file.read", 3, "r", 9),
		planWrite(3, "w2", 10, `{"todos":[{"id":"a","content":"A","status":"completed"},{"id":"b","content":"B","status":"in_progress"},{"id":"c","content":"C","status":"pending"},{"id":"d","content":"D","status":"pending"}]}`, "tasks updated · 4 items"))
	steps5 := []session.Step{historyStep(1, 1, 1, 3, ""), historyStep(2, 1, 4, 6, "a"), historyStep(3, 1, 8, 0, "a")}

	items6 := append(append([]session.TranscriptItem(nil), items5...),
		narration(3, 4, 11, "Checking B."),
		audit("file.read", 4, "r2", 12))
	steps6 := []session.Step{historyStep(1, 1, 1, 3, ""), historyStep(2, 1, 4, 6, "a"),
		historyStep(3, 1, 8, 10, "a"), historyStep(4, 1, 11, 0, "b")}

	items7 := append(append([]session.TranscriptItem(nil), items6...),
		subagentDispatch(4, "g", 13, "task-1", 1),
		historySubagent(session.SubagentRunning, time.Time{}, ""),
		jobExit(14, "job-2", 1))

	items8 := append(append([]session.TranscriptItem(nil), items6...),
		subagentDispatch(4, "g", 13, "task-1", 1),
		historySubagent(session.SubagentDone, at(15), "Looks right."),
		jobExit(14, "job-2", 1))
	inProgress8 := session.InProgressMessage{Active: true, Reasoning: "Checking the review.", StartedAt: at(15)}

	items9 := append(append([]session.TranscriptItem(nil), items8...), final(4, 17))
	steps9 := []session.Step{historyStep(1, 1, 1, 3, ""), historyStep(2, 1, 4, 6, "a"),
		historyStep(3, 1, 8, 10, "a"), historyStep(4, 1, 11, 16, "b")}

	items10 := append(append([]session.TranscriptItem(nil), items9...),
		userMsg(5, 20),
		narration(6, 5, 21, "Finishing."),
		planWrite(5, "w3", 22, `{"todos":[{"id":"b","content":"B","status":"completed"},{"id":"c","content":"C","status":"in_progress"}]}`, "tasks updated · 3 items"))
	steps10 := append(append([]session.Step(nil), steps9...), historyStep(5, 5, 21, 23, "b"))

	return []Snapshot{
		historySnap(items1, steps1, nil, session.InProgressMessage{}, true, todos1),
		historySnap(items1, steps2, active2, inProgress2, true, todos1),
		historySnap(items3, steps3, nil, session.InProgressMessage{}, true, todos1),
		historySnap(items4, steps3, nil, session.InProgressMessage{}, true, todos1),
		historySnap(items5, steps5, nil, session.InProgressMessage{}, true, todos5),
		historySnap(items6, steps6, nil, session.InProgressMessage{}, true, todos5),
		historySnap(items7, steps6, nil, session.InProgressMessage{}, true, todos5),
		historySnap(items8, steps6, nil, inProgress8, true, todos5),
		historySnap(items9, steps9, nil, session.InProgressMessage{}, false, todos5),
		historySnap(items10, steps10, nil, session.InProgressMessage{}, true, todos10),
	}
}

// historyRows is every history row of a tree, in order.
func historyRows(turns []*Node, busy bool) []*Node {
	history, _ := SplitLive(turns, busy)
	var out []*Node
	for _, t := range history {
		out = append(out, t.Children...)
	}
	return out
}

func keysOf(nodes []*Node) string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID.Key
	}
	return strings.Join(out, " ")
}

// assertAppends fails unless after only appends to before: every earlier row
// keeps its ID and version.
func assertAppends(t *testing.T, step string, before, after []*Node) {
	t.Helper()
	if len(after) < len(before) {
		t.Fatalf("%s: history shrank from %d to %d rows\nbefore: %s\nafter:  %s",
			step, len(before), len(after), keysOf(before), keysOf(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Version != before[i].Version {
			t.Fatalf("%s: row %d changed: %s v%d -> %s v%d\nbefore: %s\nafter:  %s",
				step, i, before[i].ID.Key, before[i].Version, after[i].ID.Key, after[i].Version,
				keysOf(before), keysOf(after))
		}
	}
}

func TestHistoryOnlyAppends(t *testing.T) {
	snaps := historySnapshots()
	var prev []*Node
	for i, s := range snaps {
		rows := historyRows(Build(s), s.Busy)
		if i > 0 {
			assertAppends(t, fmt.Sprintf("%d->%d", i, i+1), prev, rows)
		}
		prev = rows
	}

	last := snaps[len(snaps)-1]
	turns := Build(last)
	history, _ := SplitLive(turns, last.Busy)

	// Turn 1's history is exactly the settled rows, in the order decision 1
	// gives them: each job event follows the beat whose rows launched it, and
	// job-2's event — which ended while step:4 was open, a beat that did not
	// launch it — comes before step:4.
	want := "msg:1 step:1 completion:tool:2:s completion:tool:2:t step:4 completion:sub:1 msg:4 receipt:turn:1"
	if got := keysOf(history[0].Children); got != want {
		t.Fatalf("turn 1 history = %q\nwant %q", got, want)
	}

	// The tags read the plan in effect, not the list as it stands now.
	if l := findNode(history[:1], "step:1").Step.Todo; l == nil || l.Index != 1 || l.Total != 3 || l.Content != "A" {
		t.Fatalf("step:1 label = %+v, want 1/3 A", l)
	}
	if l := findNode(history[:1], "step:4").Step.Todo; l == nil || l.Index != 2 || l.Total != 4 || l.Content != "B" {
		t.Fatalf("step:4 label = %+v, want 2/4 B", l)
	}

	// The receipt counts the plan's completions.
	var rc *ReceiptInfo
	for _, n := range history[0].Children {
		if n.Kind == KindReceipt {
			rc = n.Receipt
		}
	}
	if rc == nil || rc.Tasks != 1 {
		t.Fatalf("receipt = %+v, want one task", rc)
	}

	// Snapshots 2 and 8 are mid-turn with reasoning streaming: the live part
	// ends with it, and no history row holds it.
	for _, idx := range []int{1, 7} {
		s := snaps[idx]
		st := Build(s)
		_, l := SplitLive(st, s.Busy)
		if len(l) == 0 || l[len(l)-1].ID != LiveThinkingID {
			t.Fatalf("snapshot %d: live = %q, want it to end with the reasoning node", idx+1, keysOf(l))
		}
		for _, n := range historyRows(st, s.Busy) {
			if n.ID == LiveThinkingID {
				t.Fatalf("snapshot %d: history holds the reasoning node", idx+1)
			}
		}
	}
	// Snapshot 8's live part is the open beat, its event, then the reasoning.
	s8 := snaps[7]
	_, live8 := SplitLive(Build(s8), s8.Busy)
	if got := keysOf(live8); got != "step:4 completion:sub:1 think:live" {
		t.Fatalf("snapshot 8 live = %q", got)
	}
}

func TestSplitLiveTakesTheNewestBeatWhileBusy(t *testing.T) {
	beat := func(key string) *Node {
		return &Node{ID: NodeID{KindStep, key}, Kind: KindStep, Step: &StepInfo{}}
	}
	completion := func(key string) *Node {
		return &Node{ID: NodeID{KindCompletion, key}, Kind: KindCompletion, Completion: &CompletionInfo{}}
	}
	msg := func() *Node { return &Node{ID: NodeID{KindMessage, "msg:1"}, Kind: KindMessage} }
	think := &Node{ID: LiveThinkingID, Kind: KindThinking, Step: &StepInfo{LiveThinking: "hm"}}
	turn := func(children ...*Node) []*Node {
		return []*Node{{ID: NodeID{KindTurn, "turn:1"}, Kind: KindTurn, Children: children}}
	}

	cases := []struct {
		name     string
		turns    []*Node
		busy     bool
		wantLive string
		wantHist string
	}{
		{"settled beat is still live", turn(msg(), beat("step:1")), true, "step:1", "msg:1"},
		{"beat and two events", turn(msg(), beat("step:1"), completion("completion:a"), completion("completion:b")), true,
			"step:1 completion:a completion:b", "msg:1"},
		{"beat, event and reasoning", turn(msg(), beat("step:1"), completion("completion:a"), think), true,
			"step:1 completion:a think:live", "msg:1"},
		{"message then reasoning", turn(msg(), think), true, "think:live", "msg:1"},
		{"not busy", turn(msg(), beat("step:1")), false, "", "msg:1 step:1"},
		{"message last", turn(msg(), beat("step:1"), msg()), true, "", "msg:1 step:1 msg:1"},
	}
	for _, c := range cases {
		before := keysOf(c.turns[0].Children)
		history, live := SplitLive(c.turns, c.busy)
		if got := keysOf(live); got != c.wantLive {
			t.Errorf("%s: live = %q, want %q", c.name, got, c.wantLive)
		}
		var hist []*Node
		for _, tn := range history {
			hist = append(hist, tn.Children...)
		}
		if got := keysOf(hist); got != c.wantHist {
			t.Errorf("%s: history = %q, want %q", c.name, got, c.wantHist)
		}
		if after := keysOf(c.turns[0].Children); after != before {
			t.Errorf("%s: SplitLive mutated the input: %q -> %q", c.name, before, after)
		}
	}
}
