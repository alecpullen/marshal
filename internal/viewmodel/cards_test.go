package viewmodel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestSubagentNoticeIsNotARow(t *testing.T) {
	notice := session.TranscriptItem{Timestamp: at(5), Kind: session.KindMessage,
		Message: &session.Message{ID: 7, Role: session.RoleSystem, ContentType: session.ContentTypeSubagentNotice,
			Content: "✓ subagent 1 completed: Task 4", CreatedAt: at(5)}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), notice}})
	if strings.Contains(shape(nodes), "msg:7") {
		t.Fatalf("the card and its marker replace the notice:\n%s", shape(nodes))
	}
}

func jobStart(step session.StepID, callID string, s int, id string) session.TranscriptItem {
	it := audit("shell.run", step, callID, s)
	it.Audit.ResultSummary = "started background job " + id
	it.Audit.ResultContent = "job_id: " + id
	it.Audit.Args = []byte(`{"command":"pytest -q","background":true}`)
	return it
}

func jobExit(s int, id string, code int) session.TranscriptItem {
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindJobExit,
		JobExit: &session.JobExit{ID: id, Command: "pytest -q", ExitCode: code, Duration: 60e9, At: at(s)}}
}

func findNode(nodes []*Node, key string) *Node {
	var hit *Node
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.ID.Key == key {
				hit = n
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	return hit
}

func TestJobExitBecomesOneCompletion(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Running the backend gate."),
			jobStart(1, "j", 2, "job-4"),
			narration(3, 2, 10, "Gate done."),
			jobExit(9, "job-4", 0),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 10, 11)},
	})
	got := shape(nodes)
	if strings.Contains(got, "job:job-4") {
		t.Fatalf("no separate job-exit row:\n%s", got)
	}
	if !strings.Contains(got, "  completion:tool:1:j\n  step:2") {
		t.Fatalf("the event sits between the beat that launched it and the next:\n%s", got)
	}
	c := findNode(nodes, "completion:tool:1:j")
	if c == nil || c.Completion == nil || c.Completion.Outcome != OutcomeDone || c.Completion.Launch.Key != "tool:1:j" {
		t.Fatalf("completion = %+v", c)
	}
}

// Review focus 5.
func TestOtherWorkCompletingInABeatGoesBeforeIt(t *testing.T) {
	// The subagent ends at 3, between the beat's first response (start 1) and
	// the narrationless response it absorbs (start 4). Nothing in the beat
	// dispatched it, so it comes before the beat: the beat joins history only
	// when it settles.
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "Task 1", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(3)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Dispatching."), sub,
			audit("agent.await", 2, "w", 6),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 4, 7)},
	})
	got := shape(nodes)
	if strings.Count(got, "step:") != 1 || !strings.Contains(got, "  completion:sub:3\n  step:1") {
		t.Fatalf("beat stays whole, the event comes before it:\n%s", got)
	}
}

func TestCompletionFollowsTheBeatThatLaunchedIt(t *testing.T) {
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "review", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(3),
			SubagentWorkFacts: session.SubagentWorkFacts{WorkKey: "task-1"}}}
	for _, busy := range []bool{false, true} {
		nodes := Build(Snapshot{
			Items: []session.TranscriptItem{
				userMsg(1, 0),
				narration(2, 1, 1, "Dispatching."),
				subagentDispatch(1, "g", 2, "task-1", 3),
				sub,
				audit("file.read", 2, "r", 5),
			},
			Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 5, 7)},
			Busy:  busy,
		})
		var keys []string
		for _, n := range nodes[0].Children {
			keys = append(keys, n.ID.Key)
		}
		if got := strings.Join(keys, " "); got != "msg:1 step:1 completion:sub:3" {
			t.Fatalf("busy=%v: children = %q, want one beat then its event", busy, got)
		}
		c := findNode(nodes, "completion:sub:3")
		if c == nil || c.Completion == nil || c.Completion.Launch.Key != "tool:1:g" {
			t.Fatalf("busy=%v: completion = %+v", busy, c)
		}
	}
}

func TestCompletionAfterSettledBeatFollowsIt(t *testing.T) {
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "slow", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(6)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Work."), sub},
		Steps: []session.Step{stepOf(1, 1, 1, 4)},
	})
	if got := shape(nodes); !strings.Contains(got, "  completion:sub:3") ||
		strings.Index(got, "completion:sub:3") < strings.Index(got, "step:1") {
		t.Fatalf("an event after a finished beat's end follows it:\n%s", got)
	}
}

func TestSimultaneousCompletionsStaySeparate(t *testing.T) {
	a := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "A", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(8)}}
	b := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 5, Label: "B", Status: session.SubagentFailed, StartedAt: at(2), EndedAt: at(8)}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), a, b},
		Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	var events []*Node
	for _, n := range nodes[0].Children {
		if n.Kind == KindCompletion {
			events = append(events, n)
		}
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want one per finished item", len(events))
	}
	if events[0].Completion.Outcome != OutcomeDone || events[1].Completion.Outcome != OutcomeFailed {
		t.Fatalf("outcomes = %q, %q", events[0].Completion.Outcome, events[1].Completion.Outcome)
	}
}

// Review focus 2.
func TestJobExitInLaterTurnCompletesThere(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Start."), jobStart(1, "j", 2, "job-7"), final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "Next."), jobExit(12, "job-7", 1),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 5, 11, 13)},
	})
	if findNode(nodes[1:], "completion:tool:1:j") == nil {
		t.Fatalf("the event belongs to the later turn:\n%s", shape(nodes))
	}
}

// Review focus 3.
func TestSubagentCompletionLandsInTurnContainingItsEnd(t *testing.T) {
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "slow", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(20)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Dispatch."), sub, final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "Meanwhile."),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 5, 11, 12)},
	})
	if findNode(nodes[1:], "completion:sub:3") == nil || findNode(nodes[:1], "completion:sub:3") != nil {
		t.Fatalf("the event lands in the turn containing EndedAt:\n%s", shape(nodes))
	}
}

func TestRunningJobHasNoCompletion(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), jobStart(1, "j", 2, "job-1")},
		Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	if strings.Contains(shape(nodes), "completion:") {
		t.Fatal("no completion until it exits")
	}
}

// Job IDs restart at job-1 in every process. After a resume, the new job-1's
// exit pairs only with the new start; the old one is lost, not running and not
// paired with someone else's exit.
func TestReusedJobIDPairsOnlyTheLatestStart(t *testing.T) {
	lost := session.TranscriptItem{Timestamp: at(2), Kind: session.KindJobExit,
		JobExit: &session.JobExit{ID: "job-1", ExitCode: -1, At: at(2), Lost: true}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Start."), jobStart(1, "old", 2, "job-1"), lost, final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "Again."), jobStart(2, "new", 12, "job-1"), jobExit(14, "job-1", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 5, 11, 15)},
	})
	lostEvent := findNode(nodes, "completion:tool:1:old")
	if lostEvent == nil || lostEvent.Completion.Outcome != OutcomeLost {
		t.Fatalf("lost event = %+v", lostEvent)
	}
	newEvent := findNode(nodes, "completion:tool:2:new")
	if newEvent == nil || newEvent.Completion.Outcome != "exit 4" {
		t.Fatalf("new event = %+v", newEvent)
	}
	if got := shape(nodes[:1]); !strings.Contains(got, "  completion:tool:1:old") ||
		strings.Index(got, "completion:tool:1:old") < strings.Index(got, "step:1") {
		t.Fatalf("the lost job's event follows the beat that launched it:\n%s", got)
	}
}

func TestOrphanJobExitsOfReusedIDGetDistinctCompletions(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), jobExit(4, "job-1", 0), userMsg(2, 6), jobExit(9, "job-1", 1),
	}})
	seen := map[string]bool{}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Kind == KindCompletion {
				if seen[n.ID.Key] {
					t.Fatalf("duplicate completion ID %q", n.ID.Key)
				}
				seen[n.ID.Key] = true
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	if len(seen) != 2 {
		t.Fatalf("completions = %v, want 2", seen)
	}
}

// structuredJobStart is the audit a managed background launch records: the
// summary text a legacy reader uses, plus the structured execution record that
// carries the identity a completion is paired by.
func structuredJobStart(step session.StepID, callID string, s int, displayID, execID string) session.TranscriptItem {
	it := audit("shell.run", step, callID, s)
	it.Audit.ResultSummary = session.JobStartPrefix + displayID
	it.Audit.Args = []byte(`{"command":"pytest -q","background":true}`)
	it.Audit.Notice = registry.NewCommandExecutionNotice(registry.CommandExecution{
		ExecutionID: execID, DisplayID: displayID, ConversationID: "conv",
		Actor: "implementer", TurnID: "turn-1", StepID: "1", ToolCallID: callID,
		Cwd: "/repo", Generation: 3, PID: 4242, StartedAt: at(s),
		State: "running", Attachment: registry.ExecutionAttachmentBackground,
	})
	return it
}

// structuredJobExit is a stored completion carrying the same identity. An
// observed exit mirrors into the legacy field the way session.JobExitFromExecution
// writes it, so the fixture is the record the production path actually stores.
func structuredJobExit(s int, displayID, execID string, exit *int) session.TranscriptItem {
	e := &session.JobExit{ID: displayID, ExecutionID: execID, Command: "pytest -q",
		ExitCode: -1, Exit: exit, Duration: 60e9, At: at(s),
		State: "failed", Outcome: "nonzero_exit", Attachment: "background"}
	if exit != nil {
		e.ExitCode = *exit
	}
	return session.TranscriptItem{Timestamp: at(s), Kind: session.KindJobExit, JobExit: e}
}

// A completion can reach the transcript before the audit row of the call that
// started it — the two are written from different goroutines. Pairing by
// execution identity pairs them anyway; ordering by timestamp would leave the
// start unpaired forever.
func TestStructuredExitPairsRegardlessOfOrder(t *testing.T) {
	code := 0
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Launch."),
			structuredJobExit(2, "job-1", "exec-1", &code),
			structuredJobStart(1, "j", 4, "job-1", "exec-1"),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	if got := shape(nodes); !strings.Contains(got, "completion:tool:1:j") {
		t.Fatalf("the completion still marks the card:\n%s", got)
	}
}

// A resume merge can repeat an exit record. Both copies pair with the same
// start, so only one completion event may be emitted: a second would carry the
// same node ID, and the ACP patch diff would silently overwrite the first.
func TestDuplicateExitEmitsOneCompletion(t *testing.T) {
	code := 0
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Launch."),
			structuredJobStart(1, "j", 3, "job-1", "exec-1"),
			structuredJobExit(4, "job-1", "exec-1", &code),
			structuredJobExit(4, "job-1", "exec-1", &code),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	var ids []string
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Kind == KindCompletion {
				ids = append(ids, n.ID.Key)
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	if len(ids) != 1 {
		t.Fatalf("want one completion event for a duplicated exit, got %d: %v", len(ids), ids)
	}
}

// A command the caller is still waiting on has its own tool row. Its audit
// carries the same structured record, and the record's attachment is what keeps
// it from becoming a second — and wrongly background-styled — card.
func TestForegroundExecutionMetadataIsNotABackgroundCard(t *testing.T) {
	zero := 0
	it := audit("shell.run", 1, "j", 2)
	it.Audit.ResultSummary = "ran pytest -q"
	it.Audit.Notice = registry.NewCommandExecutionNotice(registry.CommandExecution{
		ExecutionID: "exec-1", DisplayID: "job-1", ConversationID: "conv",
		Attachment: registry.ExecutionAttachmentForeground, State: "completed", ExitCode: &zero,
	})
	if id := JobStartID(*it.Audit); id != "" {
		t.Fatalf("JobStartID = %q, want no card for a foreground command", id)
	}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Run."), it},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	row := findNode(nodes, "tool:1:j")
	if row == nil {
		t.Fatalf("the call lost its own row:\n%s", shape(nodes))
	}
	if strings.Contains(shape(nodes), "completion:") {
		t.Fatalf("foreground metadata became a completion:\n%s", shape(nodes))
	}
	if row.WorkKey != "" {
		t.Fatalf("foreground row WorkKey = %q, want none", row.WorkKey)
	}
	if row.Live {
		t.Fatal("a settled foreground row must not be live")
	}
}

// detachedJobStart is a structured launch that has ended up background-attached
// — the shape a wait that detaches records on the same execution. It carries a
// SourceEpoch so a viewer can mint the same work key the runtime does.
func detachedJobStart(step session.StepID, callID string, s int, displayID, execID, epoch string) session.TranscriptItem {
	it := audit("shell.run", step, callID, s)
	it.Audit.ResultSummary = session.JobStartPrefix + displayID
	it.Audit.Args = []byte(`{"command":"pytest -q","background":true}`)
	it.Audit.Notice = registry.NewCommandExecutionNotice(registry.CommandExecution{
		ExecutionID: execID, DisplayID: displayID, ConversationID: "conv", SourceEpoch: epoch,
		Attachment: registry.ExecutionAttachmentBackground, State: "running",
	})
	return it
}

// A detach changes the execution's attachment and the card's placement on the
// one dispatch row it already had: it can never add a second dispatch. The row's
// work key is the execution identity the runtime mints, not the summary text.
func TestDetachedCommandKeepsOneDispatch(t *testing.T) {
	code := 0
	start := detachedJobStart(1, "j", 2, "job-1", "exec-1", "epoch-cmd")
	exit := structuredJobExit(5, "job-1", "exec-1", &code)
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Launch."), start, exit},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	want := WorkKey("conv", "epoch-cmd", workKindCommand, "exec-1")
	if n := countWorkKey(nodes, want); n != 1 {
		t.Fatalf("dispatch rows for one execution = %d, want exactly one (%s)", n, shape(nodes))
	}
	row := findNode(nodes, "tool:1:j")
	if row == nil || row.WorkKey != want {
		t.Fatalf("dispatch row = %+v, want the execution-derived work key %q", row, want)
	}
}

// A base-era command record carries an execution identity but no source epoch.
// The dispatch row must key it under the stable legacy epoch the coordinator
// also projects, or the row's key would never match the projected item's and
// would drift with the live runtime epoch on every restart.
func TestBaseEraCommandRowUsesStableWorkKey(t *testing.T) {
	start := detachedJobStart(1, "j", 2, "job-1", "exec-1", "")
	exit := structuredJobExit(5, "job-1", "exec-1", nil)
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Launch."), start, exit},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	want := WorkKey("conv", legacyWorkEpoch, workKindCommand, "exec-1")
	if n := countWorkKey(nodes, want); n != 1 {
		t.Fatalf("dispatch rows for a base-era execution = %d, want exactly one (%s)", n, shape(nodes))
	}
	row := findNode(nodes, "tool:1:j")
	if row == nil || row.WorkKey != want {
		t.Fatalf("dispatch row = %+v, want the stable work key %q", row, want)
	}
}

// A background job's launch row is an ordinary call row: it never takes the
// job's exit. The same start row keeps its identity, version and work key
// before and after the exit arrives; only the tree gains a completion.
func TestJobStartRowNeverSettles(t *testing.T) {
	code := 0
	start := structuredJobStart(1, "j", 2, "job-4", "exec-4")
	before := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Launch."), start},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	after := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Launch."), start,
			structuredJobExit(5, "job-4", "exec-4", &code)},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	first, second := findNode(before, "tool:1:j"), findNode(after, "tool:1:j")
	if first == nil || second == nil {
		t.Fatalf("start row missing: before=%+v after=%+v", first, second)
	}
	if first.ID != second.ID || first.Version != second.Version || first.WorkKey != second.WorkKey {
		t.Fatalf("start row changed when the exit arrived: before=%+v after=%+v", first, second)
	}
	if first.WorkKey == "" {
		t.Fatal("start row has no work key")
	}
	if first.Live || second.Live {
		t.Fatalf("start row is live: before=%v after=%v", first.Live, second.Live)
	}
	if strings.Contains(shape(before), "completion:") {
		t.Fatalf("no completion before the exit:\n%s", shape(before))
	}
	if !strings.Contains(shape(after), "completion:tool:1:j") {
		t.Fatalf("the exit adds a completion:\n%s", shape(after))
	}
}
