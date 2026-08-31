package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/tools/native"
)

func TestLaneItemPluralizes(t *testing.T) {
	if got := laneItem(1, "job", "jobs"); got != "1 job" {
		t.Fatalf("laneItem(1) = %q, want %q", got, "1 job")
	}
	if got := laneItem(2, "job", "jobs"); got != "2 jobs" {
		t.Fatalf("laneItem(2) = %q, want %q", got, "2 jobs")
	}
}

func TestRenderLaneEmptyWhenNoRows(t *testing.T) {
	if got := renderLane("1 job", nil, 80); got != "" {
		t.Fatalf("renderLane with no rows must be empty, got %q", got)
	}
}

func TestRenderLaneStructure(t *testing.T) {
	rows := []string{"row one", "row two"}
	// The header is a pre-formatted chrome.Header line (the caller wraps
	// the caption); renderLane renders it verbatim as the caption row.
	header := chrome.Header("2 agents", "", 79)
	out := renderLane(header, rows, 80)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected separator + header + 2 rows = 4 lines, got %d:\n%s", len(lines), out)
	}
	// Row 0 is the separator: starts with the rail and carries the rule.
	sep := ansi.Strip(lines[0])
	if !strings.HasPrefix(sep, glyph.Rail) {
		t.Fatalf("separator must start with the rail, got %q", sep)
	}
	if !strings.Contains(sep, "─") {
		t.Fatalf("separator must carry the rule, got %q", sep)
	}
	// Row 1 is the header: contains the header text and the rule on the
	// same line (via chrome.Header).
	headerLine := ansi.Strip(lines[1])
	if !strings.Contains(headerLine, "2 agents") {
		t.Fatalf("header must contain the caption, got %q", headerLine)
	}
	if !strings.Contains(headerLine, "─") {
		t.Fatalf("header must carry the rule on the same line, got %q", headerLine)
	}
	// Every row carries the rail.
	for i, l := range lines {
		if !strings.Contains(ansi.Strip(l), glyph.Rail) {
			t.Errorf("row %d has no rail: %q", i, ansi.Strip(l))
		}
	}
	// Every line is within the width budget.
	for i, l := range lines {
		if n := ansi.StringWidth(l); n > 80 {
			t.Errorf("row %d exceeds width: %d > 80", i, n)
		}
	}
}

// The lane and the todo panel sit directly on top of each other; their
// rails must land in the same column or the stack looks broken.
func TestRenderLaneBridgesTodoPanelRail(t *testing.T) {
	m := newTestModel(t)
	todos := []native.TodoItem{{Content: "a task", Status: native.TodoInProgress}}
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	width := 80
	lane := renderLane("1 agent", []string{"row one"}, width)
	todo := renderTodoPanelBody(todos, todoPanelExpanded, 40, width)

	laneRows := strings.Split(strings.TrimRight(lane, "\n"), "\n")
	todoRows := strings.Split(strings.TrimRight(todo, "\n"), "\n")
	if len(laneRows) == 0 || len(todoRows) == 0 {
		t.Fatal("expected both panels to render")
	}
	railCol := func(s string) int { return strings.Index(ansi.Strip(s), glyph.Rail) }
	want := railCol(todoRows[len(todoRows)-1])
	for i, l := range laneRows {
		if got := railCol(l); got != want {
			t.Errorf("lane row %d rail at column %d, todo panel rail at column %d", i, got, want)
		}
	}
}
