package viewmodel

import (
	"testing"

	"marshal/internal/app/session"
)

func diffAudit(step session.StepID, id string, s int, file, diff string) session.TranscriptItem {
	it := audit("file.write_patch", step, id, s)
	it.Audit.FilesChanged = []string{file}
	it.Audit.ResultContent = diff
	return it
}

func TestFindReturnsParent(t *testing.T) {
	items := []session.TranscriptItem{userMsg(1, 0), narration(2, 1, 1, "Reading."), audit("file.read", 1, "r", 2), final(3, 9)}
	nodes := Build(Snapshot{Items: items, Steps: []session.Step{step(1, 1, 3, "")}, Now: at(10)})
	n, parent := Find(nodes, "step:1")
	if n == nil || parent == nil || parent.Kind != KindTurn {
		t.Fatalf("Find step:1 = %v, parent %v", n, parent)
	}
	if turn, p := Find(nodes, nodes[0].ID.Key); turn == nil || p != nil {
		t.Fatalf("turn lookup = %v, parent %v; want turn, nil", turn, p)
	}
	if n, _ := Find(nodes, "nope"); n != nil {
		t.Fatalf("unknown key found %v", n)
	}
}

func TestStepDiffsGroupsByStep(t *testing.T) {
	items := []session.TranscriptItem{
		userMsg(1, 0),
		narration(2, 1, 1, "Edit both."),
		diffAudit(1, "a", 2, "a.go", "diff a"),
		diffAudit(1, "b", 3, "b.go", "diff b"),
		narration(3, 2, 4, "Edit more."),
		diffAudit(2, "c", 5, "c.go", "diff c"),
		audit("file.read", 3, "r", 6),
		final(4, 9),
	}
	steps := []session.Step{step(1, 1, 3, ""), step(2, 4, 5, ""), step(3, 6, 7, "")}
	got := StepDiffs(Build(Snapshot{Items: items, Steps: steps, Now: at(10)}))
	if len(got) != 2 {
		t.Fatalf("got %d step diffs, want 2: %+v", len(got), got)
	}
	if got[0].StepNode != "step:1" || got[0].Diff != "diff a\ndiff b" || len(got[0].Files) != 2 {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].StepNode != "step:2" || got[1].Diff != "diff c" || got[1].TurnNode == "" {
		t.Fatalf("second = %+v", got[1])
	}
}
