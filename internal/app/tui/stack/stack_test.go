package stack

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
			extra := ""
			if n.Live {
				extra = " live"
			}
			if n.Step != nil && n.Step.Heuristic {
				extra += " heuristic"
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
	if st.Kind != KindStep || len(st.Step.Narration) != 1 || st.Step.Step.ID != 1 || st.Step.Heuristic {
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
    tools:tool:1:a x3
  step:2
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
		audit("file.read", 0, "", 1), // before any narration: unnarrated step
		narration(2, 0, 2, "Checking the guard."),
		audit("search.text", 0, "", 3),
		thinking(0, 4),
		narration(3, 0, 5, "Now the tests."),
		audit("test.run", 0, "", 6),
		final(4, 7),
	}})
	var steps []*Node
	for _, c := range nodes[0].Children {
		if c.Kind == KindStep {
			steps = append(steps, c)
		}
	}
	if len(steps) != 3 {
		t.Fatalf("heuristic steps = %d, want 3 (unnarrated, guard, tests):\n%s", len(steps), shape(nodes))
	}
	for _, s := range steps {
		if !s.Step.Heuristic || !strings.HasPrefix(s.ID.Key, "hstep:") {
			t.Errorf("step %+v is not a heuristic step", s.ID)
		}
	}
	if len(steps[0].Step.Narration) != 0 || len(steps[0].Children) != 1 {
		t.Errorf("first step should be the unnarrated read: %+v", steps[0])
	}
	if len(steps[1].Children) != 1 || len(steps[1].Step.Thinking) != 1 {
		t.Errorf("guard step should own the search and the thought: %s", shape(nodes))
	}
}

func TestBuildHeuristicStepEndsAtAnotherMessage(t *testing.T) {
	sys := session.TranscriptItem{Timestamp: at(3), Kind: session.KindMessage,
		Message: &session.Message{ID: 9, Role: session.RoleSystem, Content: "notice"}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{
		userMsg(1, 0), narration(2, 0, 1, "A."), audit("file.read", 0, "", 2), sys, audit("file.read", 0, "", 4),
	}})
	var n int
	for _, c := range nodes[0].Children {
		if c.Kind == KindStep {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("a system message must close the heuristic step; steps = %d\n%s", n, shape(nodes))
	}
}

func TestBuildMixedStepIDsAndLegacy(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 0, 1, "Legacy."), audit("file.read", 0, "", 2),
			narration(3, 1, 4, "Stamped."), audit("file.read", 1, "c", 5),
		},
		Steps: []session.Step{step(1, 4, 6, "")},
	})
	want := `turn:1
  msg:1
  hstep:` // legacy first by time
	if got := shape(nodes); !strings.HasPrefix(got, want) || !strings.Contains(got, "step:1") {
		t.Fatalf("shape:\n%s", got)
	}
}

func TestBuildStepWithoutRecordGetsZeroActor(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 7, 1, "Orphan."), audit("file.read", 7, "c", 2)}})
	st := nodes[0].Children[1]
	if st.Kind != KindStep || st.Step.Step.ID != 0 || st.Step.Step.Actor.Role != "" {
		t.Fatalf("orphan step = %+v", st.Step)
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

func TestBuildSubagentCardPlacement(t *testing.T) {
	card := func(id int64, s int) session.TranscriptItem {
		return session.TranscriptItem{Timestamp: at(s), Kind: session.KindSubagent,
			Subagent: &session.SubagentView{ID: id, Label: "x", StartedAt: at(s), Status: session.SubagentRunning}}
	}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Dispatching."), audit("agent.run", 1, "r", 2),
			card(10, 2),  // inside orchestrator step 1 [1,10]
			card(11, 20), // after every step: turn level
		},
		Steps: []session.Step{step(1, 1, 10, "")},
	})
	got := shape(nodes)
	if !strings.Contains(got, "step:1\n    sub:10") {
		t.Fatalf("card 10 should sit inside step 1:\n%s", got)
	}
	if !strings.Contains(got, "\n  sub:11") || strings.Contains(got, "step:1\n    sub:10\n    sub:11") {
		t.Fatalf("card 11 should be pass-through at turn level:\n%s", got)
	}
	if strings.Contains(got, "tool:1:r") {
		t.Fatalf("the agent.run audit duplicates the card and must be dropped:\n%s", got)
	}
}

func TestBuildKeepsAgentRunAuditsWhenDrilled(t *testing.T) {
	nodes := Build(Snapshot{
		Items:   []session.TranscriptItem{userMsg(1, 0), audit("agent.run", 1, "r", 2)},
		Steps:   []session.Step{step(1, 1, 3, "")},
		Drilled: true,
	})
	if !strings.Contains(shape(nodes), "tool:1:r") {
		t.Fatalf("drilled view must show the child's own agent.run rows:\n%s", shape(nodes))
	}
}

func TestBuildSubagentCardNotPlacedInPipelineRoleStep(t *testing.T) {
	card := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 1, StartedAt: at(2)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reviewing."), card},
		Steps: []session.Step{step(1, 1, 10, "sdd_reviewer")},
	})
	if strings.Contains(shape(nodes), "step:1\n    sub:") {
		t.Fatalf("cards belong in orchestrator steps only:\n%s", shape(nodes))
	}
}

func TestBuildLiveStepAndThinking(t *testing.T) {
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
`
	if got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	if nodes[0].Children[2].Step.LiveThinking != "thinking hard" {
		t.Errorf("live thinking not attached to the live step")
	}
	snap.Busy = false
	snap.InProgress.Active = false
	for _, n := range Build(snap)[0].Children {
		if n.Live {
			t.Errorf("nothing is live when idle: %s", n.ID.Key)
		}
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

func TestBuildSuppressesActiveAgentRunWhileCardRenders(t *testing.T) {
	nodes := Build(Snapshot{
		Items:           []session.TranscriptItem{userMsg(1, 0)},
		Busy:            true,
		RunningSubagent: true,
		ActiveTools:     []session.ActiveToolCall{{Name: "agent.run", ToolCallID: "r", StartedAt: at(1)}},
	})
	if strings.Contains(shape(nodes), "tool:1:r") {
		t.Fatalf("in-flight agent.run duplicates the running card")
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
	a := nodes[0].Children[1].Children[0].ID
	b := nodes[0].Children[2].Children[0].ID
	if a == b {
		t.Fatalf("two steps' call_0 rows share identity %v", a)
	}
}
