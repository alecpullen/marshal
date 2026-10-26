package viewmodel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func planWrite(step session.StepID, callID string, s int, body, summary string) session.TranscriptItem {
	it := todoWrite(step, callID, s, body)
	it.Audit.ResultSummary = summary
	return it
}

func planRows(beat *Node) []*Node {
	var out []*Node
	for _, c := range beat.Children {
		if c.Kind == KindPlanUpdate {
			out = append(out, c)
		}
	}
	return out
}

// The plan row sits where the write happened, among the beat's other calls,
// and carries the list the write left.
func TestPlanRowSitsAtItsWrite(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."),
			audit("file.read", 1, "r", 2),
			planWrite(1, "w", 3, `{"todos":[{"content":"A","status":"in_progress"},{"content":"B","status":"pending"}]}`, "tasks updated · 2 items"),
			audit("shell.run", 1, "s", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	beat := nodes[0].Children[1]
	want := "step:1\n  tool:1:r\n  plan:tool:1:w\n  tool:1:s\n"
	if got := shape([]*Node{beat}); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	p := beat.Children[1].PlanUpdate
	if !reflect.DeepEqual(p.Status, []string{"in_progress", "pending"}) || p.Active != 1 || p.ActiveText != "A" || p.Done() != 0 || p.Total() != 2 {
		t.Fatalf("plan = %+v", p)
	}
}

func TestPlanRowSeparatesReadGroups(t *testing.T) {
	for _, equalTime := range []bool{false, true} {
		name := "distinct timestamps"
		if equalTime {
			name = "equal timestamps"
		}
		t.Run(name, func(t *testing.T) {
			at := func(i int) int {
				if equalTime {
					return 2
				}
				return i
			}
			nodes := Build(Snapshot{
				Items: []session.TranscriptItem{
					userMsg(1, 0), narration(2, 1, 1, "Planning."),
					audit("file.read", 1, "r1", at(2)),
					audit("file.read", 1, "r2", at(3)),
					planWrite(1, "w", at(4), `{"todos":[{"content":"A","status":"in_progress"}]}`, ""),
					audit("file.read", 1, "r3", at(5)),
					audit("file.read", 1, "r4", at(6)),
				},
				Steps: []session.Step{stepOf(1, 1, 1, 7)},
			})
			beat := nodes[0].Children[1]
			want := "step:1\n  tool:1:r1 x2\n  plan:tool:1:w\n  tool:1:r3 x2\n"
			if got := shape([]*Node{beat}); got != want {
				t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// Several writes in one beat show only the last: the state the beat ended
// with, at the position of that write. A narrationless follow-up whose only
// call is a write joins the beat rather than vanishing.
func TestBeatKeepsOnlyItsLastPlanRow(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Starting."),
			planWrite(1, "w1", 2, `{"todos":[{"content":"A","status":"in_progress"},{"content":"B","status":"pending"}]}`, ""),
			audit("file.read", 1, "r", 3),
			planWrite(2, "w2", 5, `{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"}]}`, ""),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 4), stepOf(2, 1, 4, 6)},
	})
	beat := nodes[0].Children[1]
	want := "step:1\n  tool:1:r\n  plan:tool:2:w2\n"
	if got := shape([]*Node{beat}); got != want {
		t.Fatalf("shape:\n%s\nwant:\n%s", got, want)
	}
	if p := planRows(beat)[0].PlanUpdate; p.Done() != 1 || p.Active != 2 || p.ActiveText != "B" {
		t.Fatalf("plan = %+v", p)
	}
}

func TestPlanOfReadsTheWrite(t *testing.T) {
	ev := func(body, summary string) registry.AuditEvent {
		return registry.AuditEvent{ToolName: "todo.write", Args: json.RawMessage(body), ResultSummary: summary}
	}
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want *PlanUpdate
	}{
		{
			name: "statuses in list order, first in-progress item active",
			ev:   ev(`{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"},{"content":"C","status":"in_progress"}]}`, ""),
			want: &PlanUpdate{Status: []string{"completed", "in_progress", "in_progress"}, Active: 2, ActiveText: "B"},
		},
		{
			name: "carried items from the summary read as waiting",
			ev:   ev(`{"todos":[{"content":"A","status":"in_progress"}]}`, "tasks updated · 3 items"),
			want: &PlanUpdate{Status: []string{"in_progress", "pending", "pending"}, Active: 1, ActiveText: "A"},
		},
		{
			name: "an unknown status reads as waiting",
			ev:   ev(`{"todos":[{"content":"A","status":"blocked"}]}`, ""),
			want: &PlanUpdate{Status: []string{"pending"}},
		},
		{
			name: "an empty list is cleared",
			ev:   ev(`{"todos":[],"drop_unfinished":true}`, "tasks updated · 0 items"),
			want: &PlanUpdate{Cleared: true},
		},
		{
			name: "carried items keep a list from clearing",
			ev:   ev(`{"todos":[]}`, "tasks updated · 2 items"),
			want: &PlanUpdate{Status: []string{"pending", "pending"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := planOf(c.ev)
			if !ok || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("planOf = %+v, %v; want %+v", got, ok, c.want)
			}
		})
	}
	failed := ev(`{"todos":[{"content":"A","status":"pending"}]}`, "")
	failed.Error = "invalid todo status"
	if _, ok := planOf(failed); ok {
		t.Fatal("a failed write changed nothing and has no plan row")
	}
	if _, ok := planOf(ev(`not json`, "")); ok {
		t.Fatal("unparseable arguments have no plan row")
	}
}

// A failed last write falls back to the last write that changed the list.
func TestPlanRowSkipsAFailedLastWrite(t *testing.T) {
	bad := planWrite(1, "w2", 4, `{"todos":[{"content":"A","status":"nope"}]}`, "")
	bad.Audit.Error = "invalid todo status"
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."),
			planWrite(1, "w1", 2, `{"todos":[{"content":"A","status":"in_progress"}]}`, ""),
			bad,
		},
		Steps: []session.Step{stepOf(1, 1, 1, 5)},
	})
	rows := planRows(nodes[0].Children[1])
	if len(rows) != 1 || rows[0].ID.Key != "plan:tool:1:w1" {
		t.Fatalf("plan rows = %s", shape(rows))
	}
}

func TestWireCarriesThePlanRow(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Planning."),
			planWrite(1, "w", 2, `{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"}]}`, ""),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3)},
	})
	var got *acpwire.WireNode
	tree := Project(nodes)
	for i := range tree.Nodes {
		if tree.Nodes[i].Kind == "planUpdate" {
			got = &tree.Nodes[i]
		}
	}
	if got == nil || got.Plan == nil {
		t.Fatalf("no plan node in %+v", tree.Nodes)
	}
	b, _ := json.Marshal(got.Plan)
	if want := `{"status":["completed","in_progress"],"done":1,"total":2,"active":2,"activeText":"B"}`; string(b) != want {
		t.Fatalf("plan = %s, want %s", b, want)
	}
	if !strings.HasPrefix(got.Parent, "step:") {
		t.Fatalf("plan parent = %q", got.Parent)
	}
}
