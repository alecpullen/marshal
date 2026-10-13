package viewmodel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// A narrated response, then a response that only thinks and calls a tool:
// one beat, headed by the agent's own words.
func TestBeatAbsorbsNarrationlessFollowUp(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Task 7 verifies. Committing."),
			audit("file.write", 1, "w", 2),
			thinking(2, 3),
			audit("shell.run", 2, "g", 4),
			final(9, 10),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5)},
	})
	want := "turn:1\n  msg:1\n  step:1\n    tool:1:w\n    tool:2:g\n  msg:9\n"
	if got := shape(nodes); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	b := nodes[0].Children[1].Step
	if len(b.StepIDs) != 2 || b.StepIDs[0] != 1 || b.StepIDs[1] != 2 {
		t.Fatalf("StepIDs = %v", b.StepIDs)
	}
	if len(b.Thinking) != 1 {
		t.Fatalf("thinking from the merged step must move into the beat: %d", len(b.Thinking))
	}
	if !b.Step.EndedAt.Equal(at(5)) {
		t.Fatalf("beat must end when its last merged step ends: %v", b.Step.EndedAt)
	}
	if head, _ := StepHeadline(b); head != "Task 7 verifies." {
		t.Fatalf("head = %q", head)
	}
}

// New narration starts a new beat.
func TestBeatEndsAtNextNarration(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Reading the parser."),
			audit("file.read", 1, "r", 2),
			narration(3, 2, 3, "Now editing it."),
			audit("file.write", 2, "w", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5)},
	})
	if got := shape(nodes); !strings.Contains(got, "  step:1\n    tool:1:r\n  step:2\n    tool:2:w\n") {
		t.Fatalf("narration must open a new beat:\n%s", got)
	}
}

// A different actor never joins the beat.
func TestBeatEndsAtActorChange(t *testing.T) {
	s2 := stepOf(2, 1, 3, 5)
	s2.Actor = session.Actor{Role: "sdd_reviewer", Label: "reviewer #1"}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Dispatching review."),
			audit("file.read", 1, "r", 2),
			audit("file.read", 2, "r2", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), s2},
	})
	if got := shape(nodes); !strings.Contains(got, "  step:1\n    tool:1:r\n  step:2\n") {
		t.Fatalf("actor change must open a new beat:\n%s", got)
	}
}

// A turn-level block between two steps (a system message) ends the beat, so
// the transcript stays chronological.
func TestBeatEndsAtInterveningMessage(t *testing.T) {
	notice := session.TranscriptItem{Timestamp: at(3), Kind: session.KindMessage,
		Message: &session.Message{ID: 7, Role: session.RoleSystem, Content: "rolled back", CreatedAt: at(3)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Trying the fix."),
			audit("shell.run", 1, "a", 2),
			notice,
			audit("shell.run", 2, "b", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 4, 5)},
	})
	if got := shape(nodes); !strings.Contains(got, "  step:1\n    tool:1:a\n  msg:7\n  step:2\n    tool:2:b\n") {
		t.Fatalf("a message between steps must end the beat:\n%s", got)
	}
	if head, _ := StepHeadline(nodes[0].Children[3].Step); head != "" {
		t.Fatalf("a narrationless beat is headless, got %q", head)
	}
}

// Review focus 1: a model that never narrates gets headless beats and no
// invented text.
func TestBeatNeverNarratingModel(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			audit("file.read", 1, "a", 1),
			audit("file.read", 2, "b", 3),
			audit("shell.run", 3, "c", 5),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 2), stepOf(2, 1, 3, 4), stepOf(3, 1, 5, 6)},
	})
	got := shape(nodes)
	if strings.Count(got, "step:") != 1 {
		t.Fatalf("narrationless steps with nothing between them form one beat:\n%s", got)
	}
	for _, n := range nodes[0].Children {
		if n.Step != nil {
			if head, rest := StepHeadline(n.Step); head != "" || rest != "" {
				t.Fatalf("no invented headline: %q / %q", head, rest)
			}
		}
	}
}

// Legacy items must attach at their own position, not to an earlier loose
// accumulator separated by a stamped narration.
func TestBeatLegacyItemsAttachByPosition(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 0, 1, "Legacy."), audit("file.read", 0, "a", 2),
			narration(3, 1, 4, "Stamped."), audit("file.write", 1, "b", 5),
			audit("shell.run", 0, "c", 6), thinking(0, 7),
		},
		Steps: []session.Step{stepOf(1, 1, 4, 8)},
	})
	beats := nodes[0].Children[1:]
	if len(beats) != 2 || len(beats[0].Children) != 1 || len(beats[1].Children) != 2 || len(beats[1].Step.Thinking) != 1 {
		t.Fatalf("legacy rows must attach to the stamped beat at their position:\n%s", shape(nodes))
	}
	if beats[1].Children[1].Tools[0].ToolCallID != "c" {
		t.Fatalf("legacy tail attached to wrong beat:\n%s", shape(nodes))
	}
}

func TestBeatKeepsFirstTodoIdentityAndUpdatesVersion(t *testing.T) {
	s1, s2 := stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5)
	s1.TodoID = "first"
	a := &Node{ID: NodeID{KindStep, "step:1"}, Kind: KindStep, Step: &StepInfo{Step: s1, StepIDs: []session.StepID{1}, TodoID: "first"}}
	a.Version = versionOfStep(a)
	before := a.Version
	b := &Node{ID: NodeID{KindStep, "step:2"}, Kind: KindStep, Step: &StepInfo{
		Step: s2, StepIDs: []session.StepID{2},
		TodoWrites: []registry.AuditEvent{{ToolName: "todo.write"}},
	}}
	out := mergeBeats([]*Node{a, b})
	if len(out) != 1 || out[0].ID.Key != "step:1" || out[0].Step.TodoID != "first" || len(out[0].Step.TodoWrites) != 1 {
		t.Fatalf("first response identity/todo or merged writes lost: %+v", out)
	}
	if out[0].Version == before {
		t.Fatal("absorbing a response must invalidate the beat version")
	}
}

// A narrationless response bound to a different todo starts its own beat, so
// the new todo gets its divider and its own work time.
func TestBeatEndsAtTodoChange(t *testing.T) {
	s1, s2 := stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5)
	a := &Node{ID: NodeID{KindStep, "step:1"}, Kind: KindStep, Step: &StepInfo{Step: s1, StepIDs: []session.StepID{1}, TodoID: "first"}}
	b := &Node{ID: NodeID{KindStep, "step:2"}, Kind: KindStep, Step: &StepInfo{Step: s2, StepIDs: []session.StepID{2}, TodoID: "second"}}
	out := mergeBeats([]*Node{a, b})
	if len(out) != 2 || out[1].Step.TodoID != "second" {
		t.Fatalf("beats = %+v, want the second todo in its own beat", out)
	}
	labelTransitions(out, []db.TodoItem{{ID: "first", Content: "A"}, {ID: "second", Content: "B"}}, true)
	if out[1].Step.Todo == nil || out[1].Step.Todo.ID != "second" {
		t.Fatalf("second todo has no divider: %+v", out[1].Step.Todo)
	}
}

// A loose (legacy) response has no recorded end; absorbing it must not reopen
// the finished beat.
func TestBeatAbsorbingLooseStepKeepsItsEnd(t *testing.T) {
	a := &Node{ID: NodeID{KindStep, "step:1"}, Kind: KindStep, Step: &StepInfo{Step: stepOf(1, 1, 1, 3), StepIDs: []session.StepID{1}}}
	b := &Node{ID: NodeID{KindStep, "step:loose"}, Kind: KindStep, Step: &StepInfo{}}
	out := mergeBeats([]*Node{a, b})
	if len(out) != 1 || !out[0].Step.Step.EndedAt.Equal(at(3)) {
		t.Fatalf("beat end = %v, want %v", out[0].Step.Step.EndedAt, at(3))
	}
}

// The live step merged into a beat makes the beat live and carries its
// streaming reasoning.
func TestBeatTakesLiveStepAndThinking(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Running the suite."),
			audit("shell.run", 1, "a", 2),
		},
		Steps:      []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 0)},
		Busy:       true,
		InProgress: session.InProgressMessage{Active: true, Reasoning: "checking output"},
	})
	b := nodes[0].Children[1]
	if !b.Live || b.Step.LiveThinking != "checking output" || !b.Step.Step.EndedAt.IsZero() {
		t.Fatalf("beat = live %v thinking %q ended %v", b.Live, b.Step.LiveThinking, b.Step.Step.EndedAt)
	}
}
