package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func beatNode(narr string, calls int) *viewmodel.Node {
	t0 := time.Unix(10_000, 0)
	si := &viewmodel.StepInfo{Step: session.Step{ID: 1, StartedAt: t0, EndedAt: t0.Add(12 * time.Second)}, StepIDs: []session.StepID{1}}
	if narr != "" {
		si.Narration = []*session.Message{{ID: 1, Content: narr, ContentType: session.ContentTypeNarration}}
	}
	n := &viewmodel.Node{ID: viewmodel.NodeID{Kind: viewmodel.KindStep, Key: "step:1"}, Kind: viewmodel.KindStep, Step: si}
	for i := 0; i < calls; i++ {
		ev := registry.AuditEvent{Timestamp: t0.Add(time.Duration(i) * time.Second), ToolName: "file.read", ToolCallID: fmt.Sprintf("c%d", i), ResultSummary: fmt.Sprintf("read f%d", i)}
		n.Children = append(n.Children, &viewmodel.Node{ID: viewmodel.ToolID(ev), Kind: viewmodel.KindTool, Tools: []registry.AuditEvent{ev}})
	}
	return n
}

func TestRenderHeadlessBeatHasNoInventedHeader(t *testing.T) {
	out, _ := renderStep(beatNode("", 2), &stepRenderCtx{}, 100, densitySteps)
	plain := ansi.Strip(out)
	for _, bad := range []string{"inferred", "ran ", "working", "thinking"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("headless beat must not invent %q:\n%s", bad, plain)
		}
	}
	if !strings.Contains(plain, "read f0") {
		t.Fatalf("rows must still render:\n%s", plain)
	}
}

// Review focus 1: a long headless beat shows the last BeatRowCap rows.
func TestRenderHeadlessBeatCapsRows(t *testing.T) {
	out, _ := renderStep(beatNode("", viewmodel.BeatRowCap+5), &stepRenderCtx{}, 100, densitySteps)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "+5 earlier calls") {
		t.Fatalf("want a collapsed-count line:\n%s", plain)
	}
	if strings.Contains(plain, "read f0") || !strings.Contains(plain, fmt.Sprintf("read f%d", viewmodel.BeatRowCap+4)) {
		t.Fatalf("want only the newest rows:\n%s", plain)
	}
}

func TestRenderHeadlessBeatOutlineIsOneSummaryRow(t *testing.T) {
	out, _ := renderStep(beatNode("", 3), &stepRenderCtx{}, 100, densityOutline)
	plain := strings.TrimRight(ansi.Strip(out), "\n")
	if strings.Count(plain, "\n") != 0 || !strings.Contains(plain, "3 tools") {
		t.Fatalf("outline headless beat = %q", plain)
	}
}

func TestRenderDividerOnLabelledBeat(t *testing.T) {
	n := beatNode("Parser first.", 1)
	n.Step.Todo = &viewmodel.TodoLabel{ID: "p", Index: 3, Total: 9, Content: "AccessibleModal initialFocus"}
	out, _ := renderStep(n, &stepRenderCtx{}, 80, densitySteps)
	first := strings.SplitN(ansi.Strip(out), "\n", 2)[0]
	if !strings.Contains(first, "3/9 AccessibleModal initialFocus") || !strings.HasPrefix(strings.TrimSpace(first), "──") {
		t.Fatalf("divider = %q", first)
	}
}

func TestFoldedBeatRendersAtOutline(t *testing.T) {
	n := beatNode("Read the parser.", 4)
	n.Step.Folded = true
	c := &stepRenderCtx{foldTasks: true, hasOverride: func(viewmodel.NodeID) bool { return false }}
	out, _ := renderStep(n, c, 100, densitySteps)
	plain := strings.TrimRight(ansi.Strip(out), "\n")
	if strings.Count(plain, "\n") != 0 || !strings.Contains(plain, "4 tools") {
		t.Fatalf("folded beat = %q", plain)
	}
	c.hasOverride = func(viewmodel.NodeID) bool { return true }
	if out, _ := renderStep(n, c, 100, densitySteps); strings.Count(ansi.Strip(out), "\n") < 2 {
		t.Fatalf("an override unfolds the beat")
	}
}
