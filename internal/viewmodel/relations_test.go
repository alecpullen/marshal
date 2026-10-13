package viewmodel

import (
	"reflect"
	"testing"

	"marshal/internal/app/session"
)

func shellCall(cmd string, step session.StepID, callID string, s int, exit int) session.TranscriptItem {
	it := audit("shell.run", step, callID, s)
	it.Audit.Args = []byte(`{"command":"` + cmd + `"}`)
	it.Audit.CommandExitCode = &exit
	return it
}

func editCall(step session.StepID, callID string, s int) session.TranscriptItem {
	it := audit("file.write_patch", step, callID, s)
	it.Audit.FilesChanged = []string{"a.go"}
	return it
}

func TestRelationsFixedBy(t *testing.T) {
	items := []session.TranscriptItem{
		shellCall("go test", 7, "c1", 1, 1),
		editCall(8, "c2", 2),
		shellCall("go test", 9, "c3", 3, 0),
	}
	got := Relations(items)[ToolID(*items[0].Audit)]
	if want := []NodeID{{KindStep, "step:8"}}; !reflect.DeepEqual(got.FixedBy, want) {
		t.Fatalf("FixedBy = %v, want %v", got.FixedBy, want)
	}
}

func TestRelationsCausedBy(t *testing.T) {
	items := []session.TranscriptItem{
		shellCall("go test", 4, "c1", 1, 0),
		editCall(5, "c2", 2),
		shellCall("go test", 6, "c3", 3, 1),
	}
	got := Relations(items)[ToolID(*items[2].Audit)]
	if want := []NodeID{{KindStep, "step:5"}}; !reflect.DeepEqual(got.CausedBy, want) {
		t.Fatalf("CausedBy = %v, want %v", got.CausedBy, want)
	}
}

func TestRelationsNeverRepassed(t *testing.T) {
	items := []session.TranscriptItem{
		shellCall("go test", 7, "c1", 1, 1),
		editCall(8, "c2", 2),
	}
	if got := Relations(items)[ToolID(*items[0].Audit)]; len(got.FixedBy) != 0 {
		t.Fatalf("FixedBy = %v, want empty", got.FixedBy)
	}
}

func TestRelationsStepUnion(t *testing.T) {
	items := []session.TranscriptItem{
		shellCall("go test", 7, "c1", 1, 1),
		shellCall("go test", 7, "c2", 2, 1),
		editCall(8, "c3", 3),
		shellCall("go test", 9, "c4", 4, 0),
	}
	got := Relations(items)[stepNodeID(7)]
	if want := []NodeID{{KindStep, "step:8"}}; !reflect.DeepEqual(got.FixedBy, want) {
		t.Fatalf("step FixedBy = %v, want %v", got.FixedBy, want)
	}
}

func TestResolveBeatsMapsMergedStepToItsBeat(t *testing.T) {
	// Steps 1 and 2 merge into beat step:1; step 3 is its own beat.
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Fixing."),
			audit("file.write", 2, "w", 4),
			narration(3, 3, 6, "Rerunning."),
			audit("shell.run", 3, "r", 7),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5), stepOf(3, 1, 6, 8)},
	})
	idx := RelationIndex{stepNodeID(2): {FixedBy: []NodeID{stepNodeID(3)}}}
	got := ResolveBeats(idx, nodes)
	rel, ok := got[stepNodeID(1)]
	if !ok || len(rel.FixedBy) != 1 || rel.FixedBy[0] != stepNodeID(3) {
		t.Fatalf("resolved = %+v", got)
	}
}

// A failure fixed by a later response that merged into the same beat is not a
// relation between beats: the beat must not cite itself.
func TestResolveBeatsDropsSelfReference(t *testing.T) {
	nodes := Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 1, 1, "Testing."),
			audit("shell.run", 1, "t", 2),
			audit("file.write", 2, "w", 4),
		},
		Steps: []session.Step{stepOf(1, 1, 1, 3), stepOf(2, 1, 3, 5)},
	})
	idx := RelationIndex{
		stepNodeID(1): {FixedBy: []NodeID{stepNodeID(2)}},
		stepNodeID(2): {CausedBy: []NodeID{stepNodeID(1)}},
	}
	if got := ResolveBeats(idx, nodes); len(got) != 0 {
		t.Fatalf("resolved = %+v, want no relations", got)
	}
}
