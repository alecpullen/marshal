package viewmodel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
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

func TestJobStartBecomesCardAndExitBecomesMarker(t *testing.T) {
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
	card := findNode(nodes, "tool:1:j")
	if card == nil || card.Job == nil || card.Job.Running || card.Job.ExitCode != 0 || card.Job.ID != "job-4" {
		t.Fatalf("card = %+v", card)
	}
	got := shape(nodes)
	if strings.Contains(got, "job:job-4") {
		t.Fatalf("no separate job-exit row:\n%s", got)
	}
	if !strings.Contains(got, "  marker:tool:1:j\n  step:2") {
		t.Fatalf("marker at completion time, before the next beat:\n%s", got)
	}
}

// Review focus 5.
func TestMarkerInsideBeatDoesNotSplitIt(t *testing.T) {
	// The subagent ends at 3, between the beat's first response (start 1) and
	// the narrationless response it absorbs (start 4). Ordering by timestamp
	// alone would put the marker between them and split the beat in two.
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
	if strings.Count(got, "step:") != 1 || !strings.Contains(got, "    tool:2:w\n  marker:sub:3") {
		t.Fatalf("beat stays whole, marker follows it:\n%s", got)
	}
}

func TestSimultaneousCompletionsMergeIntoOneMarker(t *testing.T) {
	a := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "A", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(8)}}
	b := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 5, Label: "B", Status: session.SubagentFailed, StartedAt: at(2), EndedAt: at(8)}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), a, b},
		Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	var markers []*Node
	for _, n := range nodes[0].Children {
		if n.Kind == KindMarker {
			markers = append(markers, n)
		}
	}
	if len(markers) != 1 || len(markers[0].Marker.Refs) != 2 || !markers[0].Marker.Failed {
		t.Fatalf("markers = %+v", markers)
	}
}

// Review focus 2.
func TestJobExitInLaterTurnSettlesCardAndMarksLaterTurn(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Start."), jobStart(1, "j", 2, "job-7"), final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "Next."), jobExit(12, "job-7", 1),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 5, 11, 13)},
	})
	if c := findNode(nodes[:1], "tool:1:j"); c == nil || c.Job.Running || c.Job.ExitCode != 1 {
		t.Fatalf("old card must settle: %+v", c)
	}
	if findNode(nodes[1:], "marker:tool:1:j") == nil {
		t.Fatalf("marker belongs to the later turn:\n%s", shape(nodes))
	}
}

// Review focus 3.
func TestSubagentMarkerLandsInTurnContainingItsEnd(t *testing.T) {
	sub := session.TranscriptItem{Timestamp: at(2), Kind: session.KindSubagent,
		Subagent: &session.SubagentView{ID: 3, Label: "slow", Status: session.SubagentDone, StartedAt: at(2), EndedAt: at(20)}}
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0), narration(2, 1, 1, "Dispatch."), sub, final(3, 3),
			userMsg(5, 10), narration(6, 2, 11, "Meanwhile."),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 5, 11, 12)},
	})
	if findNode(nodes[1:], "marker:sub:3") == nil || findNode(nodes[:1], "marker:sub:3") != nil {
		t.Fatalf("marker in the turn containing EndedAt:\n%s", shape(nodes))
	}
}

func TestRunningJobCardHasNoMarker(t *testing.T) {
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Go."), jobStart(1, "j", 2, "job-1")},
		Steps: []session.Step{stepOf(1, 1, 1, 3)}})
	if c := findNode(nodes, "tool:1:j"); c == nil || !c.Job.Running {
		t.Fatalf("card = %+v", c)
	}
	if strings.Contains(shape(nodes), "marker:") {
		t.Fatal("no marker until it exits")
	}
}
