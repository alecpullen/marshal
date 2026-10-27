package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// findKind returns the first node of kind k and its parent.
func findKind(turns []*Node, k Kind) (n, parent *Node) {
	var walk func(ns []*Node, p *Node) (*Node, *Node)
	walk = func(ns []*Node, p *Node) (*Node, *Node) {
		for _, c := range ns {
			if c.Kind == k {
				return c, p
			}
			if f, fp := walk(c.Children, c); f != nil {
				return f, fp
			}
		}
		return nil, nil
	}
	return walk(turns, nil)
}

func TestStackNodeOfFindsANodeAndItsParent(t *testing.T) {
	long := strings.Repeat("x", acpwire.WireTextCap*3)
	st := projTestState(t)
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := st.BeginStep(session.Actor{Model: "qwen3"})
	st.AddNarration(id, "Running the tests.")
	st.LogToolCall(registry.AuditEvent{
		Timestamp: at(2), ToolName: "shell.run", StepID: id, ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"go test"}`), ResultContent: long,
	})
	tree := Build(SnapshotOf(st, false, at(5)))
	toolNode, parent := findKind(tree, KindTool)
	if toolNode == nil || parent == nil {
		t.Fatalf("the built tree has no tool row under a beat")
	}

	res, found := StackNodeOf(st, tree, toolNode.ID.Key)
	if !found {
		t.Fatalf("StackNodeOf(%q) not found", toolNode.ID.Key)
	}
	if res.Node.Parent != parent.ID.Key {
		t.Fatalf("node parent = %q, want the beat %q", res.Node.Parent, parent.ID.Key)
	}
	if len(res.Detail.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(res.Detail.Calls))
	}
	if got := res.Detail.Calls[0].Output; got != long {
		t.Fatalf("output = %d bytes, want the whole %d-byte output", len(got), len(long))
	}
	if want := "{\n  \"command\": \"go test\"\n}"; res.Detail.Calls[0].Args != want {
		t.Fatalf("args = %q, want %q", res.Detail.Calls[0].Args, want)
	}
}

func TestStackNodeOfMissesAnUnknownNode(t *testing.T) {
	st := projTestState(t)
	tree := Build(SnapshotOf(st, false, at(5)))
	if _, found := StackNodeOf(st, tree, "nope"); found {
		t.Fatal(`StackNodeOf("nope") was found, want not found`)
	}
}

func TestCallDetailCarriesItsHooksAsTheirJSON(t *testing.T) {
	hooks := []registry.HookMetadata{
		{Event: "PreToolUse", Decision: "allow"},
		{Event: "PostToolUse", FailedOpen: true},
	}
	want, err := json.Marshal(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if got := callDetail(registry.AuditEvent{ToolName: "shell.run", Hooks: hooks}).Hooks; string(got) != string(want) {
		t.Fatalf("hooks = %s, want %s", got, want)
	}
	if got := callDetail(registry.AuditEvent{ToolName: "shell.run"}).Hooks; len(got) != 0 {
		t.Fatalf("a call with no hooks carries %q, want none", got)
	}
}

// toolNodeForCall finds the tool row holding callID.
func toolNodeForCall(t *testing.T, turns []*Node, callID string) *Node {
	t.Helper()
	var found *Node
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Kind == KindTool {
				for _, ev := range n.Tools {
					if ev.ToolCallID == callID {
						found = n
					}
				}
			}
			walk(n.Children)
		}
	}
	walk(turns)
	if found == nil {
		t.Fatalf("no tool row for call %q", callID)
	}
	return found
}

func TestStackNodeOfCarriesACallsOwnStep(t *testing.T) {
	st := projTestState(t)
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	first := st.BeginStep(session.Actor{Label: "reviewer #1", Model: "qwen3", Provider: "local"})
	st.AddNarration(first, "Checking the **config**. Then more.")
	st.LogToolCall(registry.AuditEvent{Timestamp: at(2), ToolName: "file.read",
		StepID: first, ToolCallID: "c1", ResultSummary: "ok"})
	second := st.BeginStep(session.Actor{Model: "qwen3-mini"})
	st.LogToolCall(registry.AuditEvent{Timestamp: at(3), ToolName: "shell.run",
		StepID: second, ToolCallID: "c2", ResultSummary: "ok"})
	tree := Build(SnapshotOf(st, false, at(5)))

	read, found := StackNodeOf(st, tree, toolNodeForCall(t, tree, "c1").ID.Key)
	if !found {
		t.Fatal("the read row's node was not found")
	}
	if read.Detail.Step == nil {
		t.Fatal("the read row carries no step")
	}
	if read.Detail.Step.ID != int64(first) {
		t.Fatalf("read step id = %d, want %d", read.Detail.Step.ID, first)
	}
	if read.Detail.Step.Owner != "reviewer #1" || read.Detail.Step.Model != "qwen3" || read.Detail.Step.Provider != "local" {
		t.Fatalf("read step = %+v, want the first response's actor", *read.Detail.Step)
	}
	if read.Detail.Step.Why != "Checking the config." {
		t.Fatalf("read step why = %q, want %q", read.Detail.Step.Why, "Checking the config.")
	}

	shell, found := StackNodeOf(st, tree, toolNodeForCall(t, tree, "c2").ID.Key)
	if !found || shell.Detail.Step == nil {
		t.Fatalf("the shell row's step is missing (found = %v)", found)
	}
	if shell.Detail.Step.ID != int64(second) {
		t.Fatalf("shell step id = %d, want %d", shell.Detail.Step.ID, second)
	}
	if shell.Detail.Step.Model != "qwen3-mini" {
		t.Fatalf("shell step model = %q, want qwen3-mini", shell.Detail.Step.Model)
	}
	if shell.Detail.Step.Why != "" {
		t.Fatalf("a narrationless response's why = %q, want empty", shell.Detail.Step.Why)
	}
}

func TestStackNodeOfCarriesABeatsStep(t *testing.T) {
	st := projTestState(t)
	if err := st.SetTodos([]db.TodoItem{{ID: "t1", Content: "Run the tests", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := st.BeginStep(session.Actor{Model: "qwen3"})
	st.AddNarration(id, "Running the tests. Then more.")
	st.LogToolCall(registry.AuditEvent{Timestamp: at(2), ToolName: "shell.run",
		StepID: id, ToolCallID: "c1", ResultSummary: "ok"})
	tree := Build(SnapshotOf(st, false, at(5)))
	beat, _ := findKind(tree, KindStep)
	if beat == nil {
		t.Fatal("the built tree has no beat")
	}
	res, found := StackNodeOf(st, tree, beat.ID.Key)
	if !found || res.Detail.Step == nil {
		t.Fatalf("the beat's step is missing (found = %v)", found)
	}
	if res.Detail.Step.ID != int64(id) {
		t.Fatalf("beat step id = %d, want %d", res.Detail.Step.ID, id)
	}
	if res.Detail.Step.Why != "Running the tests." {
		t.Fatalf("beat step why = %q, want %q", res.Detail.Step.Why, "Running the tests.")
	}
	if res.Detail.Step.TodoID != "t1" {
		t.Fatalf("beat step todo = %q, want t1", res.Detail.Step.TodoID)
	}

	// A loose beat: narration with no step ID behind it.
	loose := projTestState(t)
	loose.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	loose.AddNarration(0, "Reading the tree.")
	looseTree := Build(SnapshotOf(loose, false, at(5)))
	looseBeat, _ := findKind(looseTree, KindStep)
	if looseBeat == nil {
		t.Fatal("the built tree has no loose beat")
	}
	got, found := StackNodeOf(loose, looseTree, looseBeat.ID.Key)
	if !found || got.Detail.Step == nil {
		t.Fatalf("the loose beat's step is missing (found = %v)", found)
	}
	if got.Detail.Step.ID != 0 {
		t.Fatalf("loose beat step id = %d, want 0", got.Detail.Step.ID)
	}
	if got.Detail.Step.Why != "" {
		t.Fatalf("loose beat step why = %q, want empty", got.Detail.Step.Why)
	}
}

func TestStackNodeOfCarriesACompletionsOutputAndReport(t *testing.T) {
	long := strings.Repeat("y", acpwire.WireTextCap*3)
	job := completionNode(CompletionInfo{Source: CompletionJob, ID: "job-1", Label: "test",
		Outcome: OutcomeDone, Output: long}, "job-1")
	sub := completionNode(CompletionInfo{Source: CompletionSubagent, ID: "#3", SubagentID: 3,
		Label: "review", Outcome: OutcomeDone, Report: "## Review\nwhole report"}, "sub:3")
	tree := []*Node{job, sub}
	st := projTestState(t)

	got, found := StackNodeOf(st, tree, job.ID.Key)
	if !found || got.Detail.Completion == nil {
		t.Fatalf("the job completion's detail is missing (found = %v)", found)
	}
	if got.Detail.Completion.Output != long {
		t.Fatalf("completion output = %d bytes, want the whole %d-byte output",
			len(got.Detail.Completion.Output), len(long))
	}

	gotSub, found := StackNodeOf(st, tree, sub.ID.Key)
	if !found || gotSub.Detail.Completion == nil {
		t.Fatalf("the subagent completion's detail is missing (found = %v)", found)
	}
	if want := "## Review\nwhole report"; gotSub.Detail.Completion.Report != want {
		t.Fatalf("report = %q, want %q", gotSub.Detail.Completion.Report, want)
	}
}

func TestCallDetailCarriesItsSummary(t *testing.T) {
	c := callDetail(registry.AuditEvent{ToolName: "file.edit", Error: "boom", ResultSummary: "edit failed"})
	if c.Summary != "edit failed" {
		t.Fatalf("summary = %q, want %q", c.Summary, "edit failed")
	}
	if c.Output != "" || c.Diff != "" {
		t.Fatalf("an empty edit carries output %q and diff %q, want neither", c.Output, c.Diff)
	}
}
