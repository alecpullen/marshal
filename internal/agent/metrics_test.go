package agent

import (
	"strings"
	"testing"
)

func TestTruncateGoal(t *testing.T) {
	cases := []struct {
		name string
		goal string
		want string
	}{
		{name: "short goal unchanged", goal: "fix the bug", want: "fix the bug"},
		{name: "exactly 200 runes unchanged", goal: strings.Repeat("a", 200), want: strings.Repeat("a", 200)},
		{name: "long goal truncated to 200 runes", goal: strings.Repeat("a", 250), want: strings.Repeat("a", 200)},
		{
			name: "multibyte runes not split",
			goal: strings.Repeat("é", 250),
			want: strings.Repeat("é", 200),
		},
		{name: "empty goal", goal: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateGoal(tc.goal, 200)
			if got != tc.want {
				t.Fatalf("truncateGoal length = %d runes, want %d", len([]rune(got)), len([]rune(tc.want)))
			}
		})
	}
}

func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		name string
		task *Task
		want string
	}{
		{
			name: "completed without salvage is answered",
			task: &Task{Status: TaskStatusCompleted},
			want: "answered",
		},
		{
			name: "completed with salvage reason is salvaged",
			task: &Task{Status: TaskStatusCompleted, SalvagedReason: "stalled"},
			want: "salvaged",
		},
		{
			name: "failed status is failed",
			task: &Task{Status: TaskStatusFailed},
			want: "failed",
		},
		{
			name: "executing (interrupted) is failed",
			task: &Task{Status: TaskStatusExecuting},
			want: "failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcomeFor(tc.task); got != tc.want {
				t.Fatalf("outcomeFor = %q, want %q", got, tc.want)
			}
		})
	}
}
