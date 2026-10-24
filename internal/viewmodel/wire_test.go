package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/progress"
	"marshal/internal/tools/registry"
)

func wireByID(t *testing.T, tree WireTree) map[string]WireNode {
	t.Helper()
	out := map[string]WireNode{}
	for _, n := range tree.Nodes {
		if _, dup := out[n.ID]; dup {
			t.Fatalf("duplicate wire id %q", n.ID)
		}
		out[n.ID] = n
	}
	return out
}

func TestProjectLinksParentsAndChildren(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 7, 1, "Reading the config loader. Then the tests."),
			audit("file.read", 7, "c1", 2),
			final(3, 5),
		},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(4)}},
		Now:   at(6),
	}))
	if len(tree.Roots) != 1 || tree.Roots[0] != "turn:1" {
		t.Fatalf("roots = %v, want [turn:1]", tree.Roots)
	}
	byID := wireByID(t, tree)
	seen := map[string]bool{}
	for i, n := range tree.Nodes {
		if n.Parent != "" && !seen[n.Parent] {
			t.Fatalf("node %d (%s) listed before its parent %s", i, n.ID, n.Parent)
		}
		seen[n.ID] = true
		for _, c := range n.Children {
			if byID[c].Parent != n.ID {
				t.Fatalf("child %s of %s has parent %q", c, n.ID, byID[c].Parent)
			}
		}
	}
	st := byID["step:7"]
	if st.Kind != "step" || st.Step == nil {
		t.Fatalf("step:7 = %+v", st)
	}
	if st.Step.Headline != "Reading the config loader." || st.Step.Rest != "Then the tests." {
		t.Fatalf("headline = %q rest = %q", st.Step.Headline, st.Step.Rest)
	}
	tool := byID["tool:7:c1"]
	if tool.Kind != "tool" || tool.Tool == nil || tool.Tool.Display != "Read file" || len(tool.Tool.Calls) != 1 {
		t.Fatalf("tool:7:c1 = %+v", tool)
	}
	if fin := byID["msg:3"]; fin.Kind != "final" || fin.Message == nil || fin.Message.Content != "done" {
		t.Fatalf("msg:3 = %+v", fin)
	}
	if rc, ok := byID["receipt:turn:1"]; !ok || rc.Receipt == nil {
		t.Fatalf("finished turn has no receipt: %+v", rc)
	}
}

func TestProjectLeavesNarrationlessBeatHeadless(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), audit("file.read", 7, "c1", 2), audit("file.read", 7, "c2", 3)},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(4)}},
		Now:   at(6),
	}))
	st := wireByID(t, tree)["step:7"].Step
	if st == nil || st.Headline != "" {
		t.Fatalf("step = %+v, want headless beat", st)
	}
}

func TestWireStepCarriesMergedStepIDs(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Committing."),
			audit("shell.run", 2, "g", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5)},
	})
	tree := Project(nodes)
	for _, n := range tree.Nodes {
		if n.Kind == "step" {
			if len(n.Step.StepIDs) != 2 {
				t.Fatalf("stepIds = %v", n.Step.StepIDs)
			}
			return
		}
	}
	t.Fatal("no step node")
}

func TestProjectRunningCallIsLive(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items:       []session.TranscriptItem{userMsg(1, 0)},
		Steps:       []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1)}},
		ActiveTools: []session.ActiveToolCall{{Name: "shell.run", Args: "$ go test ./...", StartedAt: at(2), StepID: 7, ToolCallID: "c9"}},
		Busy:        true,
		Now:         at(3),
	}))
	n := wireByID(t, tree)["tool:7:c9"]
	if !n.Live || n.Tool == nil || n.Tool.Running == nil || n.Tool.Running.Target != "$ go test ./..." {
		t.Fatalf("running call = %+v", n)
	}
}

func TestProjectCapsLargeText(t *testing.T) {
	big := strings.Repeat("é", WireTextCap) // 2 bytes per rune
	ev := registry.AuditEvent{Timestamp: at(2), ToolName: "shell.run", StepID: 7, ToolCallID: "c1", ResultContent: big}
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), {Timestamp: at(2), Kind: session.KindAudit, Audit: &ev}},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(3)}},
		Now:   at(4),
	}))
	c := wireByID(t, tree)["tool:7:c1"].Tool.Calls[0]
	if !c.Truncated || len(c.Output) > WireTextCap || !strings.HasPrefix(big, c.Output) {
		t.Fatalf("output len %d truncated %v", len(c.Output), c.Truncated)
	}
	if _, err := json.Marshal(tree); err != nil {
		t.Fatal(err)
	}
}

func TestWireStepCarriesTodoLabel(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Old."), audit("file.read", 1, "a", 2), final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "New."), audit("file.read", 2, "b", 12),
		},
		Steps: []session.Step{func() session.Step { s := stepFor(1, "p", 1, 2); s.TurnMsgID = 1; return s }(), stepOf(2, 5, 11, 12)},
		Todos: todos(db.TodoItem{ID: "p", Content: "Parser", Status: "completed"}, db.TodoItem{ID: "q", Content: "Q", Status: "pending"}),
	})
	for _, n := range Project(nodes).Nodes {
		if n.ID == "step:1" {
			if n.Step.Todo == nil || n.Step.Todo.Content != "Parser" {
				t.Fatalf("step:1 = %+v", n.Step)
			}
			return
		}
	}
	t.Fatal("step:1 missing")
}

func TestKindStringCoversEveryKind(t *testing.T) {
	for k := KindTurn; k <= KindPlanUpdate; k++ {
		if s := k.String(); s == "" || s == "unknown" {
			t.Fatalf("Kind(%d) has no wire name", k)
		}
	}
}

// A client outside the process needs the whole event: what finished, how it
// ended, its final measurement, and a bounded tail of its output.
func TestWireCompletionCarriesTheEvent(t *testing.T) {
	long := strings.Repeat("x", WireTextCap+64)
	exit := jobExit(5, "job-4", 2)
	exit.JobExit.Output = long
	exit.JobExit.Progress = &progress.Snapshot{Frame: progress.Frame{Metrics: []progress.Metric{
		{ID: "passed", Label: "Passed", Current: 210},
		{ID: "failed", Label: "Failed", Current: 2},
	}}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 1, 1, "Go."), jobStart(1, "j", 2, "job-4"), exit,
	}, Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	var got *WireCompletion
	for _, n := range Project(nodes).Nodes {
		if n.Completion != nil {
			got = n.Completion
		}
	}
	if got == nil {
		t.Fatalf("no completion on the wire:\n%s", shape(nodes))
	}
	if got.ID != "job-4" || got.Outcome != "exit 2" || got.Measure != "210 passed, 2 failed" {
		t.Fatalf("completion = %+v", got)
	}
	if len(got.Output) > WireTextCap || !got.Truncated {
		t.Fatalf("output = %d bytes, truncated = %v", len(got.Output), got.Truncated)
	}
}

func TestElideWorkdir(t *testing.T) {
	root := "/home/a/proj/.marshal/worktrees/x"
	if got := ElideWorkdir("cd "+root+" && pytest -q", root); got != "⌂ pytest -q" {
		t.Fatalf("got %q", got)
	}
	if got := ElideWorkdir("cd /elsewhere && ls", root); got != "cd /elsewhere && ls" {
		t.Fatalf("other dirs stay: %q", got)
	}
	// The separators a shell command uses, and quoting of the root.
	for _, cmd := range []string{"cd " + root + "; ls", `cd "` + root + `" && ls`, "cd '" + root + "' && ls"} {
		if got := ElideWorkdir(cmd, root); !strings.HasPrefix(got, "⌂ ") {
			t.Errorf("ElideWorkdir(%q) = %q", cmd, got)
		}
	}
	if got := ElideWorkdir("cd "+root, root); got != "cd "+root {
		t.Fatalf("a bare cd is not a prefix we elide: %q", got)
	}
	if got := ElideWorkdir("pytest -q", ""); got != "pytest -q" {
		t.Fatalf("no root, no elision: %q", got)
	}
}

func TestAwaitSummaryIsJustTheWait(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "agent.await", ResultSummary: "subagent 1 completed: Task 4", Duration: 18e9}
	if got := DisplaySummary(ev); got != "waited 18s" {
		t.Fatalf("got %q", got)
	}
}

// A tool row names the subagent it started, so a client can pair the dispatch
// row with the child it launched without parsing display text.
func TestWireToolNamesTheSubagentItStarted(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Dispatch."),
			subagentDispatch(1, "g", 2, "task-1", 3),
			audit("file.read", 1, "r", 3),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4)},
	}))
	byID := wireByID(t, tree)
	dispatch := byID["tool:1:g"]
	if dispatch.Tool == nil || dispatch.Tool.SubagentID != 3 {
		t.Fatalf("dispatch row = %+v, want subagentId 3", dispatch.Tool)
	}
	read := byID["tool:1:r"]
	if read.Tool == nil || read.Tool.SubagentID != 0 {
		t.Fatalf("file.read row = %+v, want no subagentId", read.Tool)
	}
}

// No tree carries a subagent node any more: a subagent item is a completion
// event, never a card.
func TestWireHasNoSubagentNodes(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Dispatch."),
			{Timestamp: at(2), Kind: session.KindSubagent,
				Subagent: &session.SubagentView{ID: 3, Label: "Task 1", StartedAt: at(2), EndedAt: at(3), Status: session.SubagentDone}},
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4)},
	}))
	for _, n := range tree.Nodes {
		if n.Kind == "subagent" {
			t.Fatalf("tree carries a subagent node: %+v", n)
		}
	}
}
