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
