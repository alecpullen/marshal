package viewmodel

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/progress"
	"marshal/internal/tools/registry"
)

func wireByID(t *testing.T, tree acpwire.WireTree) map[string]acpwire.WireNode {
	t.Helper()
	out := map[string]acpwire.WireNode{}
	for _, n := range tree.Nodes {
		if _, dup := out[n.ID]; dup {
			t.Fatalf("duplicate wire id %q", n.ID)
		}
		out[n.ID] = n
	}
	return out
}

// wireCalls indexes a projected tree's finished calls by call ID, with the verb
// of the row each one sits on.
func wireCalls(t *testing.T, tree acpwire.WireTree) (map[string]acpwire.WireCall, map[string]string) {
	t.Helper()
	calls := map[string]acpwire.WireCall{}
	verbs := map[string]string{}
	for _, n := range tree.Nodes {
		if n.Tool == nil {
			continue
		}
		for _, c := range n.Tool.Calls {
			calls[c.CallID] = c
			verbs[c.CallID] = n.Tool.Verb
		}
	}
	return calls, verbs
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
	}), "")
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
	}), "")
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
	tree := Project(nodes, "")
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
	}), "")
	n := wireByID(t, tree)["tool:7:c9"]
	if !n.Live || n.Tool == nil || n.Tool.Running == nil || n.Tool.Running.Target != "$ go test ./..." {
		t.Fatalf("running call = %+v", n)
	}
}

func TestProjectCapsLargeText(t *testing.T) {
	big := strings.Repeat("é", acpwire.WireTextCap) // 2 bytes per rune
	ev := registry.AuditEvent{Timestamp: at(2), ToolName: "shell.run", StepID: 7, ToolCallID: "c1", ResultContent: big}
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), {Timestamp: at(2), Kind: session.KindAudit, Audit: &ev}},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(3)}},
		Now:   at(4),
	}), "")
	c := wireByID(t, tree)["tool:7:c1"].Tool.Calls[0]
	if !c.Truncated || len(c.Output) > acpwire.WireTextCap || !strings.HasPrefix(big, c.Output) {
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
	for _, n := range Project(nodes, "").Nodes {
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
	long := strings.Repeat("x", acpwire.WireTextCap+64)
	exit := jobExit(5, "job-4", 2)
	exit.JobExit.Output = long
	exit.JobExit.Progress = &progress.Snapshot{Frame: progress.Frame{Metrics: []progress.Metric{
		{ID: "passed", Label: "Passed", Current: 210},
		{ID: "failed", Label: "Failed", Current: 2},
	}}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 1, 1, "Go."), jobStart(1, "j", 2, "job-4"), exit,
	}, Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	var got *acpwire.WireCompletion
	for _, n := range Project(nodes, "").Nodes {
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
	if len(got.Output) > acpwire.WireTextCap || !got.Truncated {
		t.Fatalf("output = %d bytes, truncated = %v", len(got.Output), got.Truncated)
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
	}), "")
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
	}), "")
	for _, n := range tree.Nodes {
		if n.Kind == "subagent" {
			t.Fatalf("tree carries a subagent node: %+v", n)
		}
	}
}

// A client caches a rendered row while its version is unchanged, so every node
// that has a payload hash must carry the hex of it. A turn node has none.
func TestWireNodesCarryTheirVersion(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Reading the config."),
			audit("file.read", 1, "c1", 2), final(3, 3),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4)},
		Now:   at(5),
	})
	versions := map[string]uint64{}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			versions[n.ID.Key] = n.Version
			walk(n.Children)
		}
	}
	walk(nodes)

	tree := Project(nodes, "")
	if len(tree.Nodes) != len(versions) {
		t.Fatalf("projected %d nodes, built %d", len(tree.Nodes), len(versions))
	}
	sawTurn, sawTool, sawFinal := false, false, false
	for _, w := range tree.Nodes {
		v, ok := versions[w.ID]
		if !ok {
			t.Fatalf("wire node %s has no built node", w.ID)
		}
		if w.ID == "turn:1" {
			sawTurn = true
			if w.Version != "" {
				t.Errorf("turn version = %q, want none", w.Version)
			}
			continue
		}
		if want := strconv.FormatUint(v, 16); w.Version != want {
			t.Errorf("%s version = %q, want %q", w.ID, w.Version, want)
		}
		switch w.ID {
		case "tool:1:c1":
			sawTool = w.Version != ""
		case "msg:3":
			sawFinal = w.Version != ""
		}
	}
	if !sawTurn || !sawTool || !sawFinal {
		t.Fatalf("turn %v tool row %v final message %v, want all present with a version",
			sawTurn, sawTool, sawFinal)
	}
}

// beatsRows is the beats scene's reviewer rows (15 to 20) as audit items: the
// failed eval run, the hook-rewritten vet, the killed command, the background
// build, the subagent dispatch and the failed read.
func beatsRows(step session.StepID) []session.TranscriptItem {
	exit := func(n int) *int { return &n }
	row := func(callID string, s int, ev registry.AuditEvent) session.TranscriptItem {
		ev.ToolCallID, ev.StepID, ev.Timestamp = callID, step, at(s)
		return session.TranscriptItem{Timestamp: at(s), Kind: session.KindAudit, Audit: &ev}
	}
	return []session.TranscriptItem{
		row("c6", 2, registry.AuditEvent{
			ToolName: "shell.run", Args: []byte(`{"command":"cd /w && go test ./evals/..."}`),
			CommandExitCode: exit(1), Duration: 3 * time.Second, ResultSummary: "exit 1",
			ResultContent: "=== RUN   TestEval\n--- FAIL: TestEval (0.01s)\n    eval_test.go:12: want qwen3-coder\nFAIL\n",
		}),
		row("c7", 3, registry.AuditEvent{
			ToolName: "shell.run", Args: []byte(`{"command":"go vet ./..."}`),
			CommandExitCode: exit(0), Duration: time.Second, ResultSummary: "exit 0",
			Hooks: []registry.HookMetadata{{Event: "PreToolUse", Rewrote: true}},
		}),
		row("c8", 4, registry.AuditEvent{
			ToolName: "shell.run", Args: []byte(`{"command":"sleep 600"}`),
			CommandExitCode: exit(137), Duration: 30 * time.Second, ResultSummary: "command killed: timeout",
			Sandbox: registry.SandboxMeta{Enabled: true, Backend: "restricted", KilledReason: "timeout"},
		}),
		row("c9", 5, registry.AuditEvent{
			ToolName: "shell.run", Args: []byte(`{"command":"go build ./..."}`),
			ResultSummary: "started background job job-1",
		}),
		row("c10", 6, registry.AuditEvent{
			ToolName:      "agent.run",
			Args:          []byte(`{"description":"review the config change","role":"reviewer"}`),
			ResultSummary: "started as subagent 3 — review the config change",
		}),
		row("c11", 7, registry.AuditEvent{
			ToolName: "file.read", Args: []byte(`{"path":"evals/old.toml"}`),
			Error: "no such file: evals/old.toml",
		}),
	}
}

// A collapsed tool row is one line: what it acted on, how it ended, and — when
// it failed — the tail of its output.
func TestWireCallsCarryTheirRowFields(t *testing.T) {
	const step = session.StepID(1)
	items := append([]session.TranscriptItem{userMsg(1, 0), narration(2, step, 1, "Running the evals and a background build.")},
		beatsRows(step)...)
	tree := Project(Build(Snapshot{
		Items: items,
		Steps: []session.Step{stepOf(step, 1, 1, 9)},
		Now:   at(10),
	}), "/w")

	calls, verbs := wireCalls(t, tree)
	if len(calls) != 6 {
		t.Fatalf("projected %d calls, want 6", len(calls))
	}

	test := calls["c6"]
	if test.Subject != "⌂ go test ./evals/..." {
		t.Errorf("failed run subject = %q", test.Subject)
	}
	wantTail := []string{"--- FAIL: TestEval (0.01s)", "    eval_test.go:12: want qwen3-coder", "FAIL"}
	if len(test.Tail) != len(wantTail) {
		t.Fatalf("failed run tail = %q, want %q", test.Tail, wantTail)
	}
	for i, line := range wantTail {
		if test.Tail[i] != line {
			t.Errorf("failed run tail[%d] = %q, want %q", i, test.Tail[i], line)
		}
	}
	if test.SummaryLine != "exit 1" {
		t.Errorf("failed run summaryLine = %q", test.SummaryLine)
	}

	if got := calls["c7"].HookHint; got != "hook rewrote" {
		t.Errorf("hooked row hookHint = %q", got)
	}
	if got := calls["c8"].KilledReason; got != "timeout" {
		t.Errorf("killed row killedReason = %q", got)
	}
	if got := calls["c9"].JobID; got != "job-1" {
		t.Errorf("background row jobId = %q", got)
	}
	if got := calls["c10"].Subject; got != "review the config change" {
		t.Errorf("agent row subject = %q", got)
	}
	read := calls["c11"]
	if len(read.Tail) != 1 || read.Tail[0] != "no such file: evals/old.toml" {
		t.Errorf("failed read tail = %q", read.Tail)
	}
	if read.SummaryLine != "" {
		t.Errorf("failed read summaryLine = %q, want none", read.SummaryLine)
	}

	wantVerbs := map[string]string{"c6": "shell", "c7": "shell", "c8": "shell", "c9": "shell", "c10": "agent", "c11": "read"}
	for callID, want := range wantVerbs {
		if got := verbs[callID]; got != want {
			t.Errorf("%s verb = %q, want %q", callID, got, want)
		}
		if !calls[callID].Failed && len(calls[callID].Tail) != 0 {
			t.Errorf("%s tail = %q, want none on a call that did not fail", callID, calls[callID].Tail)
		}
	}
}

func TestWireEditCarriesItsDiffStat(t *testing.T) {
	patch := audit("file.write_patch", 1, "e1", 2)
	patch.Audit.FilesChanged = []string{"evals/config.toml"}
	patch.Audit.ResultContent = "--- a/evals/config.toml\n+++ b/evals/config.toml\n@@ -1 +1 @@\n-model = \"llama3\"\n+model = \"qwen3-coder\"\n"
	read := audit("file.read", 1, "r1", 3)
	read.Audit.ResultContent = "-model = \"llama3\"\n"

	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), patch, read},
		Steps: []session.Step{stepOf(1, 1, 1, 4)},
	}), "")
	calls, _ := wireCalls(t, tree)
	if got := calls["e1"].DiffStat; got != "+1 −1" {
		t.Errorf("edit diffStat = %q, want +1 −1", got)
	}
	if got := calls["r1"].DiffStat; got != "" {
		t.Errorf("read diffStat = %q, want none", got)
	}
}

func TestWireRunningCarriesItsRowFields(t *testing.T) {
	run := func(args string) acpwire.WireTool {
		tree := Project(Build(Snapshot{
			Items:       []session.TranscriptItem{userMsg(1, 0)},
			Steps:       []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1)}},
			ActiveTools: []session.ActiveToolCall{{Name: "shell.run", Args: args, StartedAt: at(2), StepID: 7, ToolCallID: "c2"}},
			Busy:        true,
			Now:         at(3),
		}), "")
		n := wireByID(t, tree)["tool:7:c2"]
		if n.Tool == nil || n.Tool.Running == nil {
			t.Fatalf("running call = %+v", n)
		}
		if n.Tool.Verb != "shell" {
			t.Errorf("running verb = %q, want shell", n.Tool.Verb)
		}
		return *n.Tool
	}

	tool := run("make lint")
	r := tool.Running
	if r.Subject != "make lint" || r.CallID != "c2" || r.Args != "make lint" {
		t.Fatalf("running = %+v, want subject, callId and args of the call", r)
	}
	if r.Truncated {
		t.Error("a short call is not truncated")
	}

	long := strings.Repeat("x", acpwire.WireTextCap+64)
	r = run(long).Running
	if len(r.Args) != acpwire.WireTextCap || !r.Truncated {
		t.Errorf("long args = %d bytes, truncated %v; want %d and cut", len(r.Args), r.Truncated, acpwire.WireTextCap)
	}
}

func TestWireMessageCarriesItsReasoning(t *testing.T) {
	msg := func(reasoning string) *acpwire.WireMessage {
		tree := Project(Build(Snapshot{
			Items: []session.TranscriptItem{
				userMsg(1, 0),
				{Timestamp: at(2), Kind: session.KindMessage, Message: &session.Message{
					ID: 2, Role: session.RoleAssistant, Content: "done", Reasoning: reasoning, CreatedAt: at(2)}},
			},
		}), "")
		n := wireByID(t, tree)["msg:2"]
		if n.Message == nil {
			t.Fatalf("msg:2 = %+v", n)
		}
		return n.Message
	}

	short := msg("weighing two configs")
	if short.Reasoning != "weighing two configs" {
		t.Errorf("reasoning = %q", short.Reasoning)
	}
	if short.Truncated {
		t.Error("a short reasoning is not truncated")
	}
	long := strings.Repeat("y", acpwire.WireTextCap+64)
	cut := msg(long)
	if len(cut.Reasoning) != acpwire.WireTextCap {
		t.Errorf("long reasoning = %d bytes, want it cut to %d", len(cut.Reasoning), acpwire.WireTextCap)
	}
	if !cut.Truncated {
		t.Error("a cut reasoning sets the message's truncated flag")
	}
}

func TestWireStepCarriesItsNarration(t *testing.T) {
	const step = session.StepID(1)
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, step, 1, "  First.  "),
			narration(3, step, 2, "   "),
			audit("file.read", step, "c1", 3),
		},
		Steps: []session.Step{stepOf(step, 1, 1, 4)},
	})
	ws := wireByID(t, Project(nodes, ""))["step:1"].Step
	if ws == nil {
		t.Fatal("no step:1 node")
	}
	if len(ws.Narration) != 2 || ws.Narration[0] != "First." || ws.Narration[1] != "" {
		t.Fatalf("narration = %q, want [First. ]", ws.Narration)
	}
}

func TestLiveReasoningCarriesItsStart(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items:      []session.TranscriptItem{userMsg(1, 0)},
		InProgress: session.InProgressMessage{Active: true, Reasoning: "hmm", StartedAt: time.Unix(50, 0)},
		Busy:       true,
	}), "")
	n := wireByID(t, tree)[acpwire.LiveThinkingID]
	if n.Thinking == nil {
		t.Fatalf("%s = %+v", acpwire.LiveThinkingID, n)
	}
	if n.Thinking.StartedAt != 50000 {
		t.Errorf("live reasoning startedAt = %d, want 50000", n.Thinking.StartedAt)
	}
}

func TestWireRunEventCountsItsBodyLines(t *testing.T) {
	body := func(b string) int {
		tree := Project(Build(Snapshot{
			Items: []session.TranscriptItem{
				userMsg(1, 0),
				{Timestamp: at(2), Kind: session.KindRunEvent,
					RunEvent: &session.RunEvent{Kind: session.RunEventVerifyFailed, Body: b, At: at(2)}},
			},
		}), "")
		for _, n := range tree.Nodes {
			if n.RunEvent != nil {
				return n.RunEvent.BodyLines
			}
		}
		t.Fatal("no run-event node")
		return 0
	}

	lines := make([]string, 14)
	for i := range lines {
		lines[i] = "line"
	}
	if got := body(strings.Join(lines, "\n") + "\n"); got != 14 {
		t.Errorf("bodyLines = %d, want 14", got)
	}
	if got := body(""); got != 0 {
		t.Errorf("empty bodyLines = %d, want 0", got)
	}
}

// auditWithSymbols is a finished call that touched symbols.
func auditWithSymbols(tool string, step session.StepID, callID string, s int, syms ...registry.SymbolRef) session.TranscriptItem {
	ev := registry.AuditEvent{Timestamp: at(s), ToolName: tool, StepID: step, ToolCallID: callID,
		ResultSummary: "ok", Symbols: syms}
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindAudit, Audit: &ev}
}

func TestWireToolFlagsCallersLookups(t *testing.T) {
	resolved := registry.SymbolRef{File: "transcript.go", Name: "renderSubagentCard", Line: 584, Col: 5, Resolved: true}
	unresolved := registry.SymbolRef{File: "transcript.go", Name: "renderSubagentCard", Line: 584, Col: 5}
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Editing the card."),
			auditWithSymbols("file.write_patch", 1, "c1", 2, resolved),
			auditWithSymbols("file.write_patch", 1, "c2", 3, unresolved),
			auditWithSymbols("file.read", 1, "c3", 4, resolved),
		},
		Steps: []session.Step{step(1, 1, 5, "")},
	}), "")

	flagged := map[string]bool{}
	for _, n := range tree.Nodes {
		if n.Tool == nil {
			continue
		}
		for _, c := range n.Tool.Calls {
			flagged[c.CallID] = n.Tool.Callers
		}
	}
	if !flagged["c1"] {
		t.Error("a patch with a resolved symbol is flagged, want c1 flagged")
	}
	if flagged["c2"] {
		t.Error("a patch with only an unresolved symbol is not flagged")
	}
	if flagged["c3"] {
		t.Error("a read is never flagged")
	}
}
