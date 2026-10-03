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

// stripANSITodo removes SGR sequences for glyph assertions.
func stripANSITodo(s string) string { return ansiTodoRe.ReplaceAllString(s, "") }

var ansiTodoRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
