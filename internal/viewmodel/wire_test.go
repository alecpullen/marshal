package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
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

func TestWireStepCarriesTodoLabelAndFold(t *testing.T) {
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
			if n.Step.Todo == nil || n.Step.Todo.Content != "Parser" || !n.Step.Folded {
				t.Fatalf("step:1 = %+v", n.Step)
			}
			return
		}
	}
	t.Fatal("step:1 missing")
}

func TestKindStringCoversEveryKind(t *testing.T) {
	for k := KindTurn; k <= KindMarker; k++ {
		if s := k.String(); s == "" || s == "unknown" {
			t.Fatalf("Kind(%d) has no wire name", k)
		}
	}
}

func TestWireMarkerAndJobCard(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 1, 1, "Go."), jobStart(1, "j", 2, "job-4"), jobExit(5, "job-4", 2),
	}, Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	var sawJob, sawMarker bool
	for _, n := range Project(nodes).Nodes {
		if n.Tool != nil && n.Tool.Job != nil {
			sawJob = n.Tool.Job.ExitCode == 2 && !n.Tool.Job.Running
		}
		if n.Kind == "marker" {
			sawMarker = n.Marker != nil && n.Marker.Failed && n.Marker.Refs[0] == "tool:1:j"
		}
	}
	if !sawJob || !sawMarker {
		t.Fatalf("job %v marker %v", sawJob, sawMarker)
	}
}

// A client outside the process needs the same execution evidence the card
// renders, plus the two facts only the launch's own record carries: the identity
// the job is paired by across resumes, and the wait it was launched with.
func TestWireJobCarriesWaitAndDetachEvidence(t *testing.T) {
	start := structuredJobStart(1, "j", 2, "job-w", "exec-w")
	start.Audit.Notice = registry.NewCommandExecutionNotice(registry.CommandExecution{
		ExecutionID: "exec-w", DisplayID: "job-w", ConversationID: "conv",
		Attachment: registry.ExecutionAttachmentBackground, Wait: "10s",
		DetachReason: "wait expired", DetachedAt: at(3),
	})
	exit := structuredJobExit(5, "job-w", "exec-w", nil)
	exit.JobExit.State, exit.JobExit.Outcome = "killed", "stopped"
	exit.JobExit.StopCause = "turn stop requested"
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), start, exit},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	var job *WireJob
	for _, n := range Project(nodes).Nodes {
		if n.Tool != nil && n.Tool.Job != nil {
			job = n.Tool.Job
		}
	}
	if job == nil {
		t.Fatal("no job card in the projection")
	}
	if job.ExecutionID != "exec-w" {
		t.Fatalf("ExecutionID = %q, want the execution identity", job.ExecutionID)
	}
	if job.Wait != "10s" || job.DetachReason != "wait expired" || job.DetachedAt == 0 {
		t.Fatalf("job = %+v, want the handoff evidence", job)
	}
	if job.Status != "killed" || job.StopCause != "turn stop requested" {
		t.Fatalf("job = %+v, want the stop evidence", job)
	}
	if job.Exit != nil {
		t.Fatalf("Exit = %v, want it omitted for an unobserved exit", *job.Exit)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"executionId":"exec-w"`) {
		t.Fatalf("the identity is not on the wire: %s", raw)
	}
}

// The new fields are additive: a legacy card with no structured launch record
// projects none of them, and marshals exactly as before.
func TestWireJobOmitsEvidenceForLegacyCard(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 1, 1, "Go."), jobStart(1, "j", 2, "job-legacy"), jobExit(5, "job-legacy", 0),
	}, Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	for _, n := range Project(nodes).Nodes {
		if n.Tool == nil || n.Tool.Job == nil {
			continue
		}
		job := n.Tool.Job
		if job.ExecutionID != "" || job.Wait != "" || job.DetachReason != "" || job.DetachedAt != 0 {
			t.Fatalf("job = %+v, want no structured evidence", job)
		}
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"executionId", "wait", "detachReason", "detachedAt"} {
			if strings.Contains(string(raw), `"`+key+`"`) {
				t.Fatalf("legacy card carries %s: %s", key, raw)
			}
		}
		return
	}
	t.Fatal("no job card in the projection")
}

// Display text in the new fields is scrubbed like every other wire field: a
// detach reason is actor-supplied, and a client renders it.
func TestWireJobScrubsDetachEvidence(t *testing.T) {
	start := structuredJobStart(1, "j", 2, "job-s", "exec-s")
	start.Audit.Notice = registry.NewCommandExecutionNotice(registry.CommandExecution{
		ExecutionID: "exec-s", DisplayID: "job-s", Attachment: registry.ExecutionAttachmentBackground,
		Wait: "10s", DetachReason: "wait\x1b]0;pwned\x07 expired",
	})
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), start},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	for _, n := range Project(nodes).Nodes {
		if n.Tool == nil || n.Tool.Job == nil {
			continue
		}
		if strings.ContainsAny(n.Tool.Job.DetachReason, "\x1b\x07") {
			t.Fatalf("detachReason = %q, still carries a control sequence", n.Tool.Job.DetachReason)
		}
		if !strings.Contains(n.Tool.Job.DetachReason, "wait") {
			t.Fatalf("detachReason = %q, want the text preserved", n.Tool.Job.DetachReason)
		}
		return
	}
	t.Fatal("no job card in the projection")
}

// A client outside the process needs the same execution evidence the card
// renders: the observed exit (distinct from the legacy -1 spelling of unknown),
// the lifecycle state, the attachment, an accepted stop's cause and whether the
// retained output was capped.
func TestWireJobCarriesExecutionEvidence(t *testing.T) {
	code := 3
	start := structuredJobStart(1, "j", 2, "job-5", "exec-5")
	exit := structuredJobExit(5, "job-5", "exec-5", &code)
	exit.JobExit.State, exit.JobExit.StopCause, exit.JobExit.Truncated = "killed", "turn stop", true
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), start, exit},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	var job *WireJob
	for _, n := range Project(nodes).Nodes {
		if n.Tool != nil && n.Tool.Job != nil {
			job = n.Tool.Job
		}
	}
	if job == nil {
		t.Fatal("no job card in the projection")
	}
	if job.Exit == nil || *job.Exit != 3 || job.ExitCode != 3 {
		t.Fatalf("exit = %v / %d, want the observed 3", job.Exit, job.ExitCode)
	}
	if job.Status != "killed" || job.Attachment != "background" || job.StopCause != "turn stop" || !job.Truncated {
		t.Fatalf("job = %+v, want its evidence projected", job)
	}
	if job.Running || job.Lost {
		t.Fatalf("job = %+v, want a settled card", job)
	}
	if _, err := json.Marshal(job); err != nil {
		t.Fatalf("a card with evidence must marshal: %v", err)
	}
}

// An exit that was never observed is projected as absent, not as a zero a client
// would render as success; the legacy -1 field stays for older clients.
func TestWireJobOmitsUnobservedExit(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 1, 1, "Go."),
		structuredJobStart(1, "j", 2, "job-6", "exec-6"),
		session.TranscriptItem{Timestamp: at(5), Kind: session.KindJobExit,
			JobExit: &session.JobExit{ID: "job-6", ExecutionID: "exec-6", ExitCode: -1,
				State: "failed", Outcome: "startup_failure", At: at(5)}},
	}, Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	for _, n := range Project(nodes).Nodes {
		if n.Tool == nil || n.Tool.Job == nil {
			continue
		}
		job := n.Tool.Job
		if job.Exit != nil {
			t.Fatalf("Exit = %v, want it omitted for an unobserved exit", *job.Exit)
		}
		if job.ExitCode != -1 {
			t.Fatalf("ExitCode = %d, want the legacy -1", job.ExitCode)
		}
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"exit"`) {
			t.Fatalf("the wire omitted nothing: %s", raw)
		}
		if !strings.Contains(string(raw), `"status":"failed"`) {
			t.Fatalf("the wire lost the lifecycle status: %s", raw)
		}
		return
	}
	t.Fatal("no job card in the projection")
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

// The wire band keeps the legacy entity listing for a client that has not opted
// into the pinned surface, and the LiveWork projection drops the duplicated live
// cards while the band's own summary data stays whole.
func TestLiveWorkWireOptInPreservesLegacyBand(t *testing.T) {
	items := []session.TranscriptItem{
		userMsg(1, 0), narration(2, 1, 1, "Launch."), jobStart(1, "j", 2, "job-1"),
	}
	steps := []session.Step{stepOf(1, 1, 1, 3)}
	list := todos(db.TodoItem{ID: "p", Content: "Parser", Status: "in_progress"},
		db.TodoItem{ID: "q", Content: "Q", Status: "pending"})

	legacy := Snapshot{Items: items, Steps: steps, Todos: list, Busy: true}
	lb := BandOf(legacy, Build(legacy))
	if len(lb.Entities) != 1 {
		t.Fatalf("legacy band lost its running-card listing: %+v", lb.Entities)
	}
	if wb := ProjectBand(lb); len(wb.Band.Entities) != 1 {
		t.Fatalf("legacy wire band dropped the optional entities: %+v", wb.Band.Entities)
	}

	live := legacy
	live.LiveWork = true
	ab := BandOf(live, Build(live))
	if len(ab.Entities) != 0 {
		t.Fatalf("LiveWork band must omit the duplicated entity listings: %+v", ab.Entities)
	}
	// The summary data the band is the single source for is unchanged.
	if len(ab.Tasks) != len(lb.Tasks) || len(ab.Tasks) != 2 {
		t.Fatalf("LiveWork band lost its todo/task summary: %d vs %d", len(ab.Tasks), len(lb.Tasks))
	}
	if ab.Total != lb.Total || ab.Done != lb.Done {
		t.Fatalf("LiveWork band changed todo progress: %+v vs %+v", ab, lb)
	}
}
