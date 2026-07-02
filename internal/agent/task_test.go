package agent

import (
	"testing"
	"time"
)

func TestNewTaskDefaultsToPendingStatus(t *testing.T) {
	startedAt := time.Unix(100, 0)
	task := NewTask("fix the parser", startedAt)

	if task.Goal != "fix the parser" {
		t.Fatalf("Goal = %q, want %q", task.Goal, "fix the parser")
	}
	if task.Status != TaskStatusPending {
		t.Fatalf("Status = %q, want %q", task.Status, TaskStatusPending)
	}
	if !task.StartedAt.Equal(startedAt) {
		t.Fatalf("StartedAt = %v, want %v", task.StartedAt, startedAt)
	}
	if task.Plan != nil {
		t.Fatalf("Plan = %#v, want nil", task.Plan)
	}
}

func TestSplitPlanLinesTrimsAndDropsBlankLines(t *testing.T) {
	text := "1. Read the file\n\n  2. Apply the fix  \n3. Run tests\n"
	got := splitPlanLines(text)
	want := []string{"1. Read the file", "2. Apply the fix", "3. Run tests"}

	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
