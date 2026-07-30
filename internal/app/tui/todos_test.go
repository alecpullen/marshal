package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/tools/native"
)

func sampleTodos(n, doneCount, inProgressAt int) []native.TodoItem {
	out := make([]native.TodoItem, 0, n)
	for i := 0; i < n; i++ {
		status := native.TodoPending
		switch {
		case i < doneCount:
			status = native.TodoCompleted
		case i == inProgressAt:
			status = native.TodoInProgress
		}
		out = append(out, native.TodoItem{Content: "task " + string(rune('a'+i)), Status: status})
	}
	return out
}

func TestTodoLineFitsWidthWithWideRunes(t *testing.T) {
	out := todoLine(native.TodoItem{Content: "これはとても長い日本語のタスク内容で幅を超えます", Status: native.TodoInProgress}, 20)
	if w := ansi.StringWidth(out); w > 20 {
		t.Fatalf("todo line is %d cells wide, budget 20: %q", w, out)
	}
}

func TestTodoPanelEmptyForNoTodos(t *testing.T) {
	if renderTodoPanelBody(nil, todoPanelExpanded, 40, 80) != "" {
		t.Fatal("empty todo list must render nothing")
	}
}

func TestTodoPanelHiddenMode(t *testing.T) {
	todos := sampleTodos(3, 1, 1)
	if renderTodoPanelBody(todos, todoPanelHidden, 40, 80) != "" {
		t.Fatal("hidden mode must render nothing")
	}
}

func TestTodoPanelExpandedUsesStatusGlyphs(t *testing.T) {
	todos := sampleTodos(3, 1, 1)
	out := stripANSI(renderTodoPanelBody(todos, todoPanelExpanded, 40, 80))
	if strings.Contains(out, "Tasks:") {
		t.Fatalf("panel must not carry a header:\n%s", out)
	}
	if !strings.Contains(out, "✓ task a") {
		t.Fatalf("completed todo missing ✓ gutter:\n%s", out)
	}
	if !strings.Contains(out, "▶ task b") {
		t.Fatalf("in-progress todo missing ▶ gutter:\n%s", out)
	}
	if !strings.Contains(out, "· task c") {
		t.Fatalf("pending todo missing · gutter:\n%s", out)
	}
}

func TestTodoPanelCollapsedIsOneLine(t *testing.T) {
	todos := sampleTodos(7, 3, 3)
	out := stripANSI(renderTodoPanelBody(todos, todoPanelCollapsed, 40, 80))
	if strings.Contains(out, "\n") {
		t.Fatalf("collapsed panel must be one row:\n%s", out)
	}
	if !strings.Contains(out, "tasks 3/7") || !strings.Contains(out, "task d") {
		t.Fatalf("collapsed line missing counts or active task:\n%s", out)
	}
}

func TestTodoPanelAllDoneCollapsesToSummary(t *testing.T) {
	todos := sampleTodos(5, 5, -1)
	out := stripANSI(renderTodoPanelBody(todos, todoPanelExpanded, 40, 80))
	if strings.Contains(out, "\n") {
		t.Fatalf("all-done panel must be one row:\n%s", out)
	}
	if !strings.Contains(out, "✓ 5 tasks done") {
		t.Fatalf("all-done summary missing:\n%s", out)
	}
}

func TestTodoPanelClipsToBudgetAndKeepsInProgressVisible(t *testing.T) {
	todos := sampleTodos(14, 8, 8)
	out := renderTodoPanelBody(todos, todoPanelExpanded, 40, 80)
	rows := strings.Count(out, "\n") + 1
	if rows > todoPanelMaxRows {
		t.Fatalf("panel exceeds row budget: %d > %d", rows, todoPanelMaxRows)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "task i") {
		t.Fatalf("in-progress item must stay visible:\n%s", plain)
	}
	if !strings.Contains(plain, "✓ 8 done") {
		t.Fatalf("leading completed run must collapse into one line:\n%s", plain)
	}
}

func TestTodoPanelRespectsShortFrames(t *testing.T) {
	todos := sampleTodos(9, 2, 2)
	// A 24-row frame gives a 6-row budget (25%).
	out := renderTodoPanelBody(todos, todoPanelExpanded, 24, 80)
	if rows := strings.Count(out, "\n") + 1; rows > 6 {
		t.Fatalf("panel must respect the 25%% frame cap: %d > 6", rows)
	}
	// A tiny frame degrades to the collapsed one-liner.
	out = renderTodoPanelBody(todos, todoPanelExpanded, 6, 80)
	if strings.Contains(out, "\n") {
		t.Fatalf("tiny frames must degrade to one row:\n%s", out)
	}
}

func TestTodoPanelFitsWidth(t *testing.T) {
	todos := []native.TodoItem{{Content: strings.Repeat("long ", 40), Status: native.TodoInProgress}}
	for _, line := range strings.Split(renderTodoPanelBody(todos, todoPanelExpanded, 40, 60), "\n") {
		if n := visibleRunes(line); n > 60 {
			t.Fatalf("todo row exceeds width: %d > 60", n)
		}
	}
}

func TestTodoSignatureChangesWithContentAndStatus(t *testing.T) {
	a := []native.TodoItem{{Content: "x", Status: native.TodoPending}}
	b := []native.TodoItem{{Content: "x", Status: native.TodoCompleted}}
	if todoSignature(a) == todoSignature(b) {
		t.Fatal("signature must change when a status changes")
	}
	if todoSignature(a) != todoSignature([]native.TodoItem{{Content: "x", Status: native.TodoPending}}) {
		t.Fatal("signature must be stable for identical lists")
	}
}
