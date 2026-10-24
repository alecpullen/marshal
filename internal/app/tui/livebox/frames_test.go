package livebox

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
	"marshal/internal/progress"
	"marshal/internal/viewmodel"
)

// update rewrites the golden frames instead of comparing them.
var update = flag.Bool("update", false, "rewrite testdata frames")

// frameNow is the clock every frame is built at, so an elapsed time is stable.
var frameNow = time.Unix(10_000, 0)

// frameLive is the live part of the standard frame.
func frameLive() Live {
	return Live{
		Full: []string{
			"⠋ Checking the eval thresholds still make sense for a coder model.",
		},
		Targets:      []viewmodel.NodeID{node("live-1")},
		Short:        []string{"⠋ Checking the eval thresholds…"},
		ShortTargets: []viewmodel.NodeID{node("live-1")},
	}
}

// frameWork is the standard frame's running section: a subagent and a command,
// both running.
func frameWork() []viewmodel.WorkItem {
	return []viewmodel.WorkItem{
		{
			TaskKey: "k1", Kind: "agent", SourceID: "1", DisplayID: "#1",
			Label: "review the config change", State: "running",
			StartedAt: frameNow.Add(-62 * time.Second),
		},
		{
			TaskKey: "k2", Kind: "command", DisplayID: "job-2",
			Label: "Harness tests", State: "running",
			OutputTail: "--- FAIL: TestX\nlast useful line\n",
			StartedAt:  frameNow.Add(-48 * time.Second),
		},
	}
}

// frameInput is the standard frame: the turn's live beat, two running items, a
// four-item plan, and the mode under test.
func frameInput(w, h int, mode Mode) Input {
	return Input{
		Width:   w,
		MaxRows: h * 2 / 5,
		Now:     frameNow,
		Busy:    true,
		State:   "⠋ working 4m12s",
		Hints:   []string{"Ctrl+T plan", "Ctrl+C ×2 stop"},
		Spinner: "⠋",
		Live:    frameLive(),
		Work:    frameWork(),
		Headlines: map[string]string{
			"k1": "Checking context_window…",
		},
		Plan: planOf(4, 2, 3),
		Mode: mode,
	}
}

// frameDownload is the expanded frame's item: a download with every card row.
func frameDownload() viewmodel.WorkItem {
	total := 3.7 * gib
	return viewmodel.WorkItem{
		TaskKey: "k1", Kind: "command", DisplayID: "job-3",
		Label: "Download qwen3-coder weights", State: "running",
		ProgressConfig: &progress.Config{Profile: "download", Source: "events-v1"},
		Progress: &progress.Snapshot{
			Frame: progress.Frame{Sequence: 2, PrimaryMetricID: "bytes",
				Metrics: []progress.Metric{
					{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
				},
				Detail: "shard 7 of 12"},
			ObservedAt: frameNow,
		},
		StartedAt: frameNow.Add(-2 * time.Minute),
	}
}

// expandedInput is the box with one item's card open and the plan out of the
// way, so the card is the frame's subject.
func expandedInput(w, h int, mode Mode) Input {
	in := frameInput(w, h, mode)
	in.Plan = nil
	in.Expanded = "k1"
	in.Work = []viewmodel.WorkItem{frameDownload()}
	in.Hints = []string{"Ctrl+C ×2 stop"}
	return in
}

// shapeFrame is one running-row shape from the spec §5.6 table.
type shapeFrame struct {
	name      string
	item      viewmodel.WorkItem
	previous  map[string]*progress.Snapshot
	headlines map[string]string
}

// shapeFrames is the §5.6 table as fixed inputs: one running item per shape.
func shapeFrames() []shapeFrame {
	total := 3.7 * gib
	return []shapeFrame{
		{
			name: "subagent-with-headline",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "agent", SourceID: "1", DisplayID: "#1",
				Label: "review the config change", State: "running",
				StartedAt: frameNow.Add(-62 * time.Second),
			},
			headlines: map[string]string{"k1": "Checking context_window…"},
		},
		{
			name: "blocked-subagent",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "agent", SourceID: "2", DisplayID: "#2",
				Label: "review the config change", State: "blocked",
				Prompt:    &viewmodel.WorkPrompt{Kind: "approval"},
				StartedAt: frameNow.Add(-90 * time.Second),
			},
		},
		{
			name: "generic-command-with-output",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-1",
				Label: "Harness tests", State: "running",
				OutputTail: "go: downloading x\n--- FAIL: TestX\nlast useful line\n",
				StartedAt:  frameNow.Add(-30 * time.Second),
			},
		},
		{
			name: "go-tests-210-passed-2-failed",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-2",
				Label: "go test", State: "running",
				ProgressConfig: &progress.Config{Profile: "tests", Source: "go-test-json"},
				Progress: &progress.Snapshot{
					Frame: progress.Frame{Sequence: 7, Metrics: []progress.Metric{
						{ID: "passed", Label: "Passed", Unit: "cases", Current: 210},
						{ID: "failed", Label: "Failed", Unit: "cases", Current: 2},
						{ID: "skipped", Label: "Skipped", Unit: "cases", Current: 0},
					}},
					ObservedAt: frameNow,
				},
				StartedAt: frameNow.Add(-48 * time.Second),
			},
		},
		{
			name: "download-with-a-total",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-3",
				Label: "Download qwen3-coder weights", State: "running",
				ProgressConfig: &progress.Config{Profile: "download", Source: "events-v1"},
				Progress: &progress.Snapshot{
					Frame: progress.Frame{Sequence: 2, PrimaryMetricID: "bytes",
						Metrics: []progress.Metric{
							{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
						}},
					ObservedAt: frameNow,
				},
				StartedAt: frameNow.Add(-2*time.Minute - 3*time.Second),
			},
			previous: map[string]*progress.Snapshot{"k1": {
				Frame: progress.Frame{Sequence: 1, PrimaryMetricID: "bytes",
					Metrics: []progress.Metric{
						{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.0 * gib, Total: &total},
					}},
				ObservedAt: frameNow.Add(-10 * time.Second),
			}},
		},
		{
			name: "download-without-a-total",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-4",
				Label: "Fetch shard", State: "running",
				ProgressConfig: &progress.Config{Profile: "download", Source: "events-v1"},
				Progress: &progress.Snapshot{
					Frame: progress.Frame{Sequence: 3, PrimaryMetricID: "bytes",
						Metrics: []progress.Metric{
							{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 512 * 1024 * 1024},
						}},
					ObservedAt: frameNow,
				},
				StartedAt: frameNow.Add(-12 * time.Second),
			},
		},
		{
			name: "phased-custom-job",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-5",
				Label: "Eval sweep", State: "running",
				ProgressConfig: &progress.Config{Profile: "custom", Source: "events-v1"},
				Progress: &progress.Snapshot{
					Frame: progress.Frame{Sequence: 4, PrimaryMetricID: "checks",
						Phase: &progress.Phase{ID: "verify", Label: "verify", Index: 2, Total: 3},
						Metrics: []progress.Metric{
							{ID: "checks", Label: "Checks", Unit: "checks", Current: 2, Total: f64(3)},
						}},
					ObservedAt: frameNow,
				},
				StartedAt: frameNow.Add(-40 * time.Second),
			},
		},
		{
			name: "stale-snapshot",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-6",
				Label: "Download qwen3-coder weights", State: "running",
				ProgressConfig: &progress.Config{Profile: "download", Source: "events-v1"},
				Progress: &progress.Snapshot{
					Frame: progress.Frame{Sequence: 5, PrimaryMetricID: "bytes",
						Metrics: []progress.Metric{
							{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
						}},
					ObservedAt: frameNow.Add(-42 * time.Second),
				},
				StartedAt: frameNow.Add(-5 * time.Minute),
			},
		},
		{
			name: "100-while-running",
			item: viewmodel.WorkItem{
				TaskKey: "k1", Kind: "command", DisplayID: "job-7",
				Label: "Download qwen3-coder weights", State: "running",
				ProgressConfig: &progress.Config{Profile: "download", Source: "events-v1"},
				Progress: &progress.Snapshot{
					Frame: progress.Frame{Sequence: 6, PrimaryMetricID: "bytes",
						Metrics: []progress.Metric{
							{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: total, Total: &total},
						}},
					ObservedAt: frameNow,
				},
				StartedAt: frameNow.Add(-3 * time.Minute),
			},
		},
	}
}

// TestFrames renders the box's shapes as golden frames, one file per shape and
// frame size, so a change to the drawing is visible in review.
func TestFrames(t *testing.T) {
	dims := []struct{ w, h int }{{80, 24}, {96, 34}, {120, 40}}
	for _, d := range dims {
		for _, name := range []string{"stacked", "folded", "expanded", "idle", "compact"} {
			var in Input
			switch name {
			case "stacked":
				in = frameInput(d.w, d.h, Stacked)
			case "folded":
				in = frameInput(d.w, d.h, Folded)
			case "expanded":
				in = expandedInput(d.w, d.h, Stacked)
			case "idle":
				in = Input{Width: d.w, MaxRows: d.h * 2 / 5, Now: frameNow, Plan: planOf(4, 2, 3)}
			case "compact":
				in = frameInput(d.w, d.h, Stacked)
				in.MaxRows = 1
			}
			checkFrame(t, fmt.Sprintf("%s-%dx%d", name, d.w, d.h), in, d.w)
		}
		if d.w >= SplitMinWidth {
			checkFrame(t, fmt.Sprintf("split-%dx%d", d.w, d.h), frameInput(d.w, d.h, Split), d.w)
		}
	}

	for _, shape := range shapeFrames() {
		in := Input{
			Width:     96,
			MaxRows:   13,
			Now:       frameNow,
			Busy:      true,
			State:     "⠋ working 0m12s",
			Hints:     []string{"Ctrl+C ×2 stop"},
			Spinner:   "⠋",
			Work:      []viewmodel.WorkItem{shape.item},
			Previous:  shape.previous,
			Headlines: shape.headlines,
		}
		checkFrame(t, "rows-"+shape.name, in, 96)
	}
}

// checkFrame renders one frame with NO_COLOR and flat depth, then compares it
// with its golden file (or rewrites it under -update).
func checkFrame(t *testing.T, name string, in Input, width int) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		prev := theme.Current()
		t.Cleanup(func() { theme.Reload(prev) })
		theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

		got := Render(Plan(in), width) + "\n"
		// The review rule made executable: no line is wider than its frame.
		for i, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
			if n := ansi.StringWidth(line); n > width {
				t.Errorf("line %d is %d cells, wider than the %d-cell frame: %q", i, n, width, line)
			}
		}
		path := filepath.Join("testdata", name+".txt")
		if *update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (run with -update to write the frames)", path, err)
		}
		if string(want) != got {
			t.Errorf("frame differs from %s\n--- got ---\n%s\n--- want ---\n%s",
				path, got, strings.TrimSuffix(string(want), "\n"))
		}
	})
}
