package tui

import (
	"regexp"
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

func TestTodoPanelHasChromeRail(t *testing.T) {
	todos := sampleTodos(3, 1, 1)
	out := stripANSI(renderTodoPanelBody(todos, todoPanelExpanded, 40, 80))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("todo panel too short to rail:\n%s", out)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "▍") {
			t.Errorf("todo panel line %d missing the ▍ chrome rail: %q\nfull panel:\n%s", i, line, out)
		}
	}

	// The collapsed one-liner and the all-done summary wear the rail too.
	if one := stripANSI(renderTodoPanelBody(todos, todoPanelCollapsed, 40, 80)); !strings.HasPrefix(one, "▍") {
		t.Fatalf("collapsed one-liner missing the ▍ chrome rail: %q", one)
	}
	done := sampleTodos(3, 3, -1)
	if all := stripANSI(renderTodoPanelBody(done, todoPanelExpanded, 40, 80)); !strings.HasPrefix(all, "▍") {
		t.Fatalf("all-done summary missing the ▍ chrome rail: %q", all)
	}
}

func TestTodoPanelExpandedUsesStatusGlyphs(t *testing.T) {
	// 6 items → budget=6, body=5 rows after header — enough to show all
	// three glyph types (· pending, ▶ in-progress, ✓ completed) within
	// the clipped window.
	todos := []native.TodoItem{
		{Content: "task a", Status: native.TodoCompleted},
		{Content: "task b", Status: native.TodoInProgress},
		{Content: "task c", Status: native.TodoPending},
		{Content: "task d", Status: native.TodoPending},
		{Content: "task e", Status: native.TodoPending},
		{Content: "task f", Status: native.TodoPending},
	}
	out := stripANSI(renderTodoPanelBody(todos, todoPanelExpanded, 40, 80))
	if !strings.Contains(out, "tasks 1/6") {
		t.Fatalf("panel must carry a header with counts:\n%s", out)
	}
	if !strings.Contains(out, "✓ task a") {
		t.Fatalf("completed todo missing ✓ gutter:\n%s", out)
	}
	if !strings.Contains(out, "▸ task b") {
		t.Fatalf("in-progress todo missing ▸ gutter:\n%s", out)
	}
	if !strings.Contains(out, "· task c") {
		t.Fatalf("pending todo missing · gutter:\n%s", out)
	}
}

func TestTodoPanelHeaderTracksCounts(t *testing.T) {
	todos := sampleTodos(5, 2, 2)
	raw := renderTodoPanelBody(todos, todoPanelExpanded, 40, 80)
	if !strings.Contains(raw, "\x1b[") {
		t.Fatal("header must use ANSI styling")
	}
	out := stripANSI(raw)
	if !strings.Contains(out, "tasks 2/5") {
		t.Fatalf("panel header must show task counts:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], "tasks 2/5") {
		t.Fatalf("header must be the first line:\n%s", out)
	}
}

// The header carries the count text and the divider rule on the SAME line
// (no separate rule row), matching the sidepanel Header helper.
func TestTodoPanelHeaderHasRuleOnSameLine(t *testing.T) {
	todos := sampleTodos(5, 2, 2)
	out := stripANSI(renderTodoPanelBody(todos, todoPanelExpanded, 40, 80))
	lines := strings.Split(out, "\n")
	header := lines[0]
	if !strings.Contains(header, "tasks 2/5") {
		t.Fatalf("header must carry the count text, got %q", header)
	}
	if !strings.Contains(header, "──") {
		t.Fatalf("header must carry the divider rule on the same line, got %q", header)
	}
}

// Completed todos are muted, not struck through: SGR 9 support is patchy and
// muted-plus-strikethrough was the least legible pairing in the UI. The ✓
// glyph carries the "done" meaning on its own.
func TestTodoLineDoneIsMutedNotStruckThrough(t *testing.T) {
	item := native.TodoItem{Content: "done task", Status: native.TodoCompleted}
	out := todoLine(item, 80)
	if strings.Contains(out, ";9m") || strings.Contains(out, "[9m") {
		t.Fatalf("completed todo must not use strikethrough:\n%q", out)
	}
	if !strings.Contains(stripANSITodo(out), "✓ done task") {
		t.Fatalf("completed todo missing ✓ glyph:\n%q", out)
	}
}

func TestTodoLineNoStrikethroughWhenPending(t *testing.T) {
	item := native.TodoItem{Content: "pending task", Status: native.TodoPending}
	out := todoLine(item, 80)
	if strings.Contains(out, "\x1b[9m") {
		t.Fatalf("pending todo must not use strikethrough style:\n%q", out)
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

func TestTodoPanelNoBlankFirstRowWhenClipped(t *testing.T) {
	// 14 tasks with 8 done pushes the in-progress item to index 8.
	// The panel collapses the leading completed run to one summary line,
	// then clips the remaining rows. The first clipped row must be real
	// content, not a blank placeholder from chrome.ClipLines.
	todos := sampleTodos(14, 8, 8)
	out := stripANSI(renderTodoPanelBody(todos, todoPanelExpanded, 40, 80))
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(rows) < 2 {
		t.Fatalf("panel too short to inspect:\n%s", out)
	}
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("row %d is blank:\n%s", i, out)
		}
	}
}

func TestTodoPanelRespectsShortFrames(t *testing.T) {
	todos := sampleTodos(9, 2, 2)
	// A 24-row frame gives an 8-row budget (33%).
	out := renderTodoPanelBody(todos, todoPanelExpanded, 24, 80)
	if rows := strings.Count(out, "\n") + 1; rows > 8 {
		t.Fatalf("panel must respect the 33%% frame cap: %d > 8", rows)
	}
	// A tiny frame degrades to the collapsed one-liner.
	out = renderTodoPanelBody(todos, todoPanelExpanded, 3, 80)
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

// stripANSITodo removes SGR sequences for glyph assertions.
func stripANSITodo(s string) string { return ansiTodoRe.ReplaceAllString(s, "") }

var ansiTodoRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
