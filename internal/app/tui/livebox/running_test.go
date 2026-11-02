package livebox

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/theme"
	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// The package reads colours through theme.Current(); a zero theme has nil
	// slots, so pin a real palette for the whole package.
	theme.Reload(theme.LoadFor(false, "xterm-256color"))
	os.Exit(testenv.Run(m))
}

const gib = float64(1024 * 1024 * 1024)

var testNow = time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }

// rowText renders one running row at a generous width and returns its text
// with every escape stripped.
func rowText(it acpwire.WorkItem, in Input) string {
	return ansi.Strip(runningRow(it, in, 200).Text)
}

// TestRunningRowPerShape is the spec §5.6 table: every work item shape draws
// the measurement spec calls for, and the shapes that must not draw a bar or a
// rate do not.
func TestRunningRowPerShape(t *testing.T) {
	total := 3.7 * gib
	tests := []struct {
		name    string
		item    acpwire.WorkItem
		in      Input
		want    []string
		notWant []string
	}{
		{
			name: "subagent with headline",
			item: acpwire.WorkItem{
				TaskKey: "k1", Kind: "agent", SourceID: "1", DisplayID: "#1",
				Label: "review the config change", State: "running",
				StartedAt: acpwire.Millis(testNow.Add(-62 * time.Second)),
			},
			in: Input{Now: testNow, Spinner: "⠋",
				Headlines: map[string]string{"k1": "Checking context_window…"}},
			want:    []string{"Checking context_window…", "review the config change"},
			notWant: []string{"━"},
		},
		{
			name: "blocked subagent",
			item: acpwire.WorkItem{
				TaskKey: "k2", Kind: "agent", SourceID: "2", DisplayID: "#2",
				Label: "review the config change", State: "blocked",
				Prompt:    &acpwire.WorkPrompt{Kind: "approval"},
				StartedAt: acpwire.Millis(testNow.Add(-90 * time.Second)),
			},
			in:   Input{Now: testNow},
			want: []string{"⚠ needs approval"},
		},
		{
			name: "generic command with output",
			item: acpwire.WorkItem{
				TaskKey: "k3", Kind: "command", DisplayID: "job-1",
				Label: "Harness tests", State: "running",
				OutputTail: "go: downloading x\n--- FAIL: TestX\nlast useful line\n",
				StartedAt:  acpwire.Millis(testNow.Add(-30 * time.Second)),
			},
			in:   Input{Now: testNow},
			want: []string{"last useful line"},
		},
		{
			name: "go tests 210 passed 2 failed",
			item: acpwire.WorkItem{
				TaskKey: "k4", Kind: "command", DisplayID: "job-2",
				Label: "go test", State: "running",
				ProgressConfig: &acpwire.WorkProgressConfig{Profile: "tests", Source: "go-test-json"},
				Progress: &acpwire.WorkProgress{
					Sequence: 7, Metrics: []acpwire.WorkMetric{
						{ID: "passed", Label: "Passed", Unit: "cases", Current: 210},
						{ID: "failed", Label: "Failed", Unit: "cases", Current: 2},
						{ID: "skipped", Label: "Skipped", Unit: "cases", Current: 0},
					},
					ObservedAt: acpwire.Millis(testNow),
				},
				StartedAt: acpwire.Millis(testNow.Add(-48 * time.Second)),
			},
			in:      Input{Now: testNow},
			want:    []string{"210 passed · 2 failed"},
			notWant: []string{"%", "━", "skipped"},
		},
		{
			name: "download with a total",
			item: acpwire.WorkItem{
				TaskKey: "k5", Kind: "command", DisplayID: "job-3",
				Label: "Download qwen3-coder weights", State: "running",
				ProgressConfig: &acpwire.WorkProgressConfig{Profile: "download", Source: "events-v1"},
				Progress: &acpwire.WorkProgress{
					Sequence: 2, PrimaryMetricID: "bytes",
					Metrics: []acpwire.WorkMetric{
						{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
					},
					ObservedAt: acpwire.Millis(testNow),
				},
				StartedAt: acpwire.Millis(testNow.Add(-2*time.Minute + -3*time.Second)),
			},
			in: Input{Now: testNow, Previous: map[string]*acpwire.WorkProgress{"k5": {
				Sequence: 1, PrimaryMetricID: "bytes",
				Metrics: []acpwire.WorkMetric{
					{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.0 * gib, Total: &total},
				},
				ObservedAt: acpwire.Millis(testNow.Add(-10 * time.Second)),
			}}},
			// The ETA is derived from the previous observation's delta, so it
			// is present but its last second is float-sensitive; ~2m is the
			// stable part.
			want: []string{"━", "57%", "2.1 GiB/3.7 GiB", "~2m ", "MiB/s"},
		},
		{
			name: "download without a total",
			item: acpwire.WorkItem{
				TaskKey: "k6", Kind: "command", DisplayID: "job-4",
				Label: "Fetch shard", State: "running",
				ProgressConfig: &acpwire.WorkProgressConfig{Profile: "download", Source: "events-v1"},
				Progress: &acpwire.WorkProgress{
					Sequence: 3, PrimaryMetricID: "bytes",
					Metrics: []acpwire.WorkMetric{
						{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 512 * 1024 * 1024},
					},
					ObservedAt: acpwire.Millis(testNow),
				},
				StartedAt: acpwire.Millis(testNow.Add(-12 * time.Second)),
			},
			in:      Input{Now: testNow},
			want:    []string{"512.0 MiB", "size unknown"},
			notWant: []string{"━", "%"},
		},
		{
			name: "phased custom job",
			item: acpwire.WorkItem{
				TaskKey: "k7", Kind: "command", DisplayID: "job-5",
				Label: "Eval sweep", State: "running",
				ProgressConfig: &acpwire.WorkProgressConfig{Profile: "custom", Source: "events-v1"},
				Progress: &acpwire.WorkProgress{
					Sequence: 4, PrimaryMetricID: "checks",
					Phase: &acpwire.WorkPhase{ID: "verify", Label: "verify", Index: 2, Total: 3},
					Metrics: []acpwire.WorkMetric{
						{ID: "checks", Label: "Checks", Unit: "checks", Current: 2, Total: f64(3)},
					},
					ObservedAt: acpwire.Millis(testNow),
				},
				StartedAt: acpwire.Millis(testNow.Add(-40 * time.Second)),
			},
			in:   Input{Now: testNow},
			want: []string{"2/3 verify", "67%", "2/3 checks"},
		},
		{
			name: "stale snapshot",
			item: acpwire.WorkItem{
				TaskKey: "k8", Kind: "command", DisplayID: "job-6",
				Label: "Download qwen3-coder weights", State: "running",
				ProgressConfig: &acpwire.WorkProgressConfig{Profile: "download", Source: "events-v1"},
				Progress: &acpwire.WorkProgress{
					Sequence: 5, PrimaryMetricID: "bytes",
					Metrics: []acpwire.WorkMetric{
						{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
					},
					ObservedAt: acpwire.Millis(testNow.Add(-42 * time.Second)),
				},
				StartedAt: acpwire.Millis(testNow.Add(-5 * time.Minute)),
			},
			in:      Input{Now: testNow},
			want:    []string{"no update for 42s", "57%", "━"},
			notWant: []string{"/s"},
		},
		{
			name: "100% while running",
			item: acpwire.WorkItem{
				TaskKey: "k9", Kind: "command", DisplayID: "job-7",
				Label: "Download qwen3-coder weights", State: "running",
				ProgressConfig: &acpwire.WorkProgressConfig{Profile: "download", Source: "events-v1"},
				Progress: &acpwire.WorkProgress{
					Sequence: 6, PrimaryMetricID: "bytes",
					Metrics: []acpwire.WorkMetric{
						{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: total, Total: &total},
					},
					ObservedAt: acpwire.Millis(testNow),
				},
				StartedAt: acpwire.Millis(testNow.Add(-3 * time.Minute)),
			},
			in:   Input{Now: testNow},
			want: []string{"100%", "reported done · process still running"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rowText(tc.item, tc.in)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("row = %q, want it to contain %q", got, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("row = %q, want it not to contain %q", got, notWant)
				}
			}
		})
	}
}

// TestBarDrawsHalfCells pins the measured bar's quantisation: a half cell is
// drawn when the fill reaches the cell's midpoint, and never past the last
// cell.
func TestBarDrawsHalfCells(t *testing.T) {
	c := theme.Current().AccentPrimary
	whole, half, track := "━", "╸", "─"

	got := ansi.Strip(Bar(0.5, 12, c))
	if n := strings.Count(got, whole); n != 6 {
		t.Errorf("Bar(0.5, 12) = %q: %d full cells, want 6", got, n)
	}
	if n := strings.Count(got, track); n != 6 {
		t.Errorf("Bar(0.5, 12) = %q: %d track cells, want 6", got, n)
	}
	if strings.Contains(got, half) {
		t.Errorf("Bar(0.5, 12) = %q, want no half cell", got)
	}

	got = ansi.Strip(Bar(0.54, 12, c))
	if n := strings.Count(got, whole); n != 6 {
		t.Errorf("Bar(0.54, 12) = %q: %d full cells, want 6", got, n)
	}
	if n := strings.Count(got, half); n != 1 {
		t.Errorf("Bar(0.54, 12) = %q: %d half cells, want 1", got, n)
	}
	if n := strings.Count(got, track); n != 5 {
		t.Errorf("Bar(0.54, 12) = %q: %d track cells, want 5", got, n)
	}
	if w := ansi.StringWidth(got); w != 12 {
		t.Errorf("Bar(0.54, 12) width = %d, want 12", w)
	}
}

// TestCardCapsAtFiveRowsAndOffersStopOnlyWhenAllowed pins the expanded card's
// height cap and its conditional stop action.
func TestCardCapsAtFiveRowsAndOffersStopOnlyWhenAllowed(t *testing.T) {
	total := 3.7 * gib
	base := acpwire.WorkItem{
		TaskKey: "k5", Kind: "command", DisplayID: "job-3",
		Label: "Download qwen3-coder weights", State: "running",
		ProgressConfig: &acpwire.WorkProgressConfig{Profile: "download", Source: "events-v1"},
		Progress: &acpwire.WorkProgress{
			Sequence: 2, PrimaryMetricID: "bytes",
			Metrics: []acpwire.WorkMetric{
				{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
			},
			Detail:     "shard 7 of 12",
			ObservedAt: acpwire.Millis(testNow),
		},
		StartedAt: acpwire.Millis(testNow.Add(-2 * time.Minute)),
	}
	in := Input{Now: testNow, Spinner: "⠋"}

	stoppable := base
	stoppable.Capabilities.Stop = true
	rows := card(stoppable, in, 100)
	if len(rows) > CardRows {
		t.Fatalf("card rows = %d, want at most %d", len(rows), CardRows)
	}
	if len(rows) != CardRows {
		t.Errorf("card rows = %d, want all %d rows for a full card", len(rows), CardRows)
	}
	all := ""
	for _, r := range rows {
		all += ansi.Strip(r.Text) + "\n"
		if r.Target != (Target{Kind: TargetWork, Key: "k5"}) {
			t.Errorf("card row target = %+v, want the item's work target", r.Target)
		}
		if !r.Tint {
			t.Error("card row is not tinted; a running card is always tinted")
		}
	}
	if !strings.Contains(all, "s stop") {
		t.Errorf("card = %q, want the stop action for a stoppable item", all)
	}
	for _, want := range []string{"job-3", "57%", "shard 7 of 12", "↵ collapse", "/work list"} {
		if !strings.Contains(all, want) {
			t.Errorf("card = %q, want it to contain %q", all, want)
		}
	}

	fixed := card(base, in, 100)
	all = ""
	for _, r := range fixed {
		all += ansi.Strip(r.Text) + "\n"
	}
	if strings.Contains(all, "s stop") {
		t.Errorf("card = %q, want no stop action when the item is not stoppable", all)
	}
}

// subagentItem is a running subagent's tray item.
func subagentItem() acpwire.WorkItem {
	return acpwire.WorkItem{
		TaskKey: "k1", Kind: "agent", SourceID: "1", DisplayID: "#1",
		Label: "review the config change", State: "running",
		StartedAt: acpwire.Millis(testNow.Add(-62 * time.Second)),
	}
}

// TestSubagentCardIsTwoRowsWithItsRoute pins the card's shape: a subagent is
// two rows — its head, then the model at the provider running it plus what it
// is doing — and every row is the same click target.
func TestSubagentCardIsTwoRowsWithItsRoute(t *testing.T) {
	in := Input{
		Now: testNow, Spinner: "⠋",
		Headlines: map[string]string{"k1": "Checking context_window…"},
		Agents:    map[string]AgentMeta{"k1": {Model: "kimi-k3", Provider: "kimi"}},
	}
	rows := itemRows(subagentItem(), in, 120)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 for a subagent card", len(rows))
	}
	head, detail := ansi.Strip(rows[0].Text), ansi.Strip(rows[1].Text)
	for _, want := range []string{"#1", "review the config change"} {
		if !strings.Contains(head, want) {
			t.Errorf("head row = %q, want it to contain %q", head, want)
		}
	}
	if strings.Contains(head, "Checking context_window") {
		t.Errorf("head row = %q, want the activity on the card's second row", head)
	}
	for _, want := range []string{"kimi-k3 @ kimi", "Checking context_window…"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail row = %q, want it to contain %q", detail, want)
		}
	}
	for i, r := range rows {
		if r.Target != (Target{Kind: TargetWork, Key: "k1"}) {
			t.Errorf("row %d target = %+v, want the item's work target", i, r.Target)
		}
		if !r.Tint {
			t.Errorf("row %d is not tinted; a running card is always tinted", i)
		}
	}
}

// TestSubagentCardMarksAFallbackRoute: a route that fell back says so.
func TestSubagentCardMarksAFallbackRoute(t *testing.T) {
	in := Input{
		Now:    testNow,
		Agents: map[string]AgentMeta{"k1": {Model: "glm-5.3", Provider: "ollama-cloud", Fallback: true}},
	}
	detail := ansi.Strip(itemRows(subagentItem(), in, 120)[1].Text)
	for _, want := range []string{"glm-5.3 @ ollama-cloud", "(fallback)"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail row = %q, want it to contain %q", detail, want)
		}
	}
}

// TestAgentRouteOmitsEmptyFallback: a fallback marker with no route behind it
// would render as dangling provenance, so the row stays blank.
func TestAgentRouteOmitsEmptyFallback(t *testing.T) {
	if got := agentRoute(AgentMeta{Fallback: true}); got != "" {
		t.Errorf("agentRoute(empty fallback) = %q, want empty", got)
	}
}

// TestSubagentCardKeepsTwoRowsWhileQuiet: a child with nothing to report still
// gets its second row, so the rows under it never move as it goes quiet.
func TestSubagentCardKeepsTwoRowsWhileQuiet(t *testing.T) {
	rows := itemRows(subagentItem(), Input{Now: testNow}, 120)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 even with no headline and no route", len(rows))
	}
	if got := strings.TrimSpace(ansi.Strip(rows[1].Text)); got != edge+"         running" {
		t.Errorf("quiet card's second row = %q, want the card's edge and the item's state", got)
	}
}

// TestOnlySubagentsGetTwoRows: every other kind keeps its single row, so the
// card is a subagent affordance rather than a tray-wide change.
func TestOnlySubagentsGetTwoRows(t *testing.T) {
	it := subagentItem()
	it.Kind, it.DisplayID = "command", "job-1"
	if rows := itemRows(it, Input{Now: testNow}, 120); len(rows) != 1 {
		t.Fatalf("command rows = %d, want 1", len(rows))
	}
}

// TestHeldSubagentRowKeepsItsRoute: a finished subagent's held row still says
// which model and provider ran it.
func TestHeldSubagentRowKeepsItsRoute(t *testing.T) {
	item := subagentItem()
	item.State = "completed"
	item.EndedAt = acpwire.Millis(testNow)
	held := HeldItem{
		Item: item, Outcome: "done",
		Summary:  "found 4 call sites",
		Duration: 62 * time.Second,
	}
	got := ansi.Strip(heldRow(held, Input{
		Now:    testNow,
		Agents: map[string]AgentMeta{"k1": {Model: "kimi-k3", Provider: "kimi"}},
	}, 120).Text)
	for _, want := range []string{"review the config change", "kimi-k3 @ kimi", "done", "found 4 call sites"} {
		if !strings.Contains(got, want) {
			t.Errorf("held row = %q, want it to contain %q", got, want)
		}
	}
}
