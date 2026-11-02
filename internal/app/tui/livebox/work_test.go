package livebox

import (
	"reflect"
	"testing"
	"time"

	"marshal/internal/progress"
	"marshal/internal/viewmodel"
)

// TestWorkPredicatesMatchTheProjection pins the box's own work predicates to
// the projection they copy: for every shape the tray cares about, the answer
// over the wire item equals viewmodel's answer over the projected one.
func TestWorkPredicatesMatchTheProjection(t *testing.T) {
	tests := []struct {
		name string
		item viewmodel.WorkItem
	}{
		{name: "running subagent", item: viewmodel.WorkItem{Kind: "agent", State: "running"}},
		{name: "stopping", item: viewmodel.WorkItem{Kind: "command", State: "stopping"}},
		{name: "watching", item: viewmodel.WorkItem{Kind: "watch", State: "watching"}},
		{name: "blocked", item: viewmodel.WorkItem{Kind: "agent", State: "blocked"}},
		{name: "completed", item: viewmodel.WorkItem{Kind: "command", State: "completed"}},
		{name: "failed", item: viewmodel.WorkItem{Kind: "command", State: "failed"}},
		{name: "stopped", item: viewmodel.WorkItem{Kind: "command", State: "stopped"}},
		{name: "unknown", item: viewmodel.WorkItem{Kind: "command", State: "unknown"}},
		{
			name: "foreground command",
			item: viewmodel.WorkItem{Kind: "command", Attachment: "foreground", State: "running"},
		},
		{
			name: "background command",
			item: viewmodel.WorkItem{Kind: "command", Attachment: "background", State: "running"},
		},
		{
			name: "startup failure",
			item: viewmodel.WorkItem{Kind: "command", State: "completed", Outcome: "startup_failure"},
		},
		{
			name: "lost",
			item: viewmodel.WorkItem{Kind: "agent", State: "stopped", Outcome: "lost"},
		},
		{
			name: "stopped by request",
			item: viewmodel.WorkItem{Kind: "command", State: "stopped", Outcome: "stopped"},
		},
		{
			name: "attention text",
			item: viewmodel.WorkItem{Kind: "agent", State: "completed", Attention: "retry the build"},
		},
		{
			name: "attention text while running",
			item: viewmodel.WorkItem{Kind: "agent", State: "running", Attention: "retry the build"},
		},
		{
			name: "reported error",
			item: viewmodel.WorkItem{
				Kind: "command", State: "completed",
				Progress: &progress.Snapshot{ReportedError: true},
			},
		},
		{
			name: "clean progress observation",
			item: viewmodel.WorkItem{
				Kind: "command", State: "completed",
				Progress: &progress.Snapshot{},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := viewmodel.WorkItemOf(tc.item)
			if got, want := WorkActive(it), viewmodel.WorkActive(tc.item); got != want {
				t.Errorf("WorkActive = %v, want %v", got, want)
			}
			if got, want := WorkNeedsAttention(it), viewmodel.WorkNeedsAttention(tc.item); got != want {
				t.Errorf("WorkNeedsAttention = %v, want %v", got, want)
			}
		})
	}
}

// TestProgressOfRestoresTheProjection: the wire form is lossless, so an
// observation the box converts to measure it is the one the projection made.
func TestProgressOfRestoresTheProjection(t *testing.T) {
	total, rate := 12.0, 2.5
	want := &progress.Snapshot{
		Frame: progress.Frame{
			Version:         1,
			Sequence:        9,
			Phase:           &progress.Phase{ID: "verify", Label: "verify", Index: 2, Total: 3},
			PrimaryMetricID: "checks",
			Metrics: []progress.Metric{
				{ID: "checks", Label: "Checks", Unit: "checks", Current: 8, Total: &total, Rate: &rate},
			},
			Rows:   []progress.Row{{ID: "lint", Label: "lint", State: "done"}},
			Detail: "shard 7 of 12",
		},
		ObservedAt:    time.UnixMilli(1_700_000_000_000),
		MeasuredAt:    time.UnixMilli(1_700_000_001_000),
		Partial:       true,
		Warning:       "counts are partial",
		ReportedError: true,
	}
	if got := progressOf(viewmodel.WorkProgressOf(want)); !reflect.DeepEqual(got, want) {
		t.Errorf("progressOf(WorkProgressOf(p)) = %+v, want %+v", got, want)
	}
	if got := progressOf(nil); got != nil {
		t.Errorf("progressOf(nil) = %+v, want nil", got)
	}
}
