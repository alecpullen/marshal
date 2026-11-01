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
	beat, rows := wireOf(beatNode("", 2), "")
	out, _ := renderStep(beat, rows, &stepRenderCtx{}, 100, false)
	plain := ansi.Strip(out)
	for _, bad := range []string{"inferred", "ran ", "working", "thinking"} {
		if strings.Contains(plain, bad) {
			t.Fatalf("headless beat must not invent %q:\n%s", bad, plain)
		}
	}
	if !strings.Contains(plain, "read f0") {
		t.Fatalf("rows must still render:\n%s", plain)
	}
	// Its head counts its calls, so the spine still hangs off something.
	if first := strings.TrimRight(strings.SplitN(plain, "\n", 2)[0], " "); first != " ● 2 calls" {
		t.Fatalf("headless head = %q", first)
	}
}

// Review focus 1: a long headless beat shows the last BeatRowCap rows.
func TestRenderHeadlessBeatCapsRows(t *testing.T) {
	beat, rows := wireOf(beatNode("", viewmodel.BeatRowCap+5), "")
	out, _ := renderStep(beat, rows, &stepRenderCtx{}, 100, false)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "… 5 earlier calls") {
		t.Fatalf("want a collapsed-count line:\n%s", plain)
	}
	if strings.Contains(plain, "read f0") || !strings.Contains(plain, fmt.Sprintf("read f%d", viewmodel.BeatRowCap+4)) {
		t.Fatalf("want only the newest rows:\n%s", plain)
	}
}

// The beat that starts a todo carries its tag at the right edge of its head,
// and no divider: the todo's text lives in the plan.
func TestLabelledBeatCarriesTheTodoTag(t *testing.T) {
	n := beatNode("Parser first.", 1)
	n.Step.Todo = &viewmodel.TodoLabel{ID: "p", Index: 3, Total: 9, Content: "AccessibleModal initialFocus"}
	beat, rows := wireOf(n, "")
	out, _ := renderStep(beat, rows, &stepRenderCtx{}, 80, false)
	plain := ansi.Strip(out)
	first := strings.SplitN(plain, "\n", 2)[0]
	if !strings.HasPrefix(first, " ● Parser first.") || !strings.HasSuffix(first, "▸ 3/9") || len([]rune(first)) != 80 {
		t.Fatalf("head = %q", first)
	}
	if strings.Contains(plain, "AccessibleModal") || strings.Contains(plain, "──") {
		t.Fatalf("no divider:\n%s", plain)
	}
}

// Under width pressure the meta drops the model, the duration and the
// thinking time before the tag; the tag goes last.
func TestTodoTagIsTheLastMetaToDrop(t *testing.T) {
	p := metaParts{owner: "reviewer #1", role: "reviewer", model: "gpt-5", thought: "thought 2s", dur: "1m 4s", tag: "▸ 2/4"}
	full, _ := rightMeta(p, 40, 200)
	if got := stripANSI(full); got != "reviewer #1 · gpt-5 · thought 2s · 1m 4s · ▸ 2/4" {
		t.Fatalf("full meta = %q", got)
	}
	for _, width := range []int{70, 60, 50, 45} {
		text, _ := rightMeta(p, 40, width)
		if got := stripANSI(text); got != "" && !strings.HasSuffix(got, "▸ 2/4") {
			t.Errorf("width %d dropped the tag before the rest: %q", width, got)
		}
	}
	tight, _ := rightMeta(p, 40, gutterWidth+2+24+5)
	if got := stripANSI(tight); got != "▸ 2/4" {
		t.Errorf("the tag alone survives the tightest fit, got %q", got)
	}
}
