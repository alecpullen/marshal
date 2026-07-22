package tui

import (
	"strings"
	"testing"

	"marshal/internal/tools/native"
)

func TestRenderTodosUsesGutter(t *testing.T) {
	todos := []native.TodoItem{
		{Content: "scaffold parser", Status: native.TodoCompleted},
		{Content: "implement parser", Status: native.TodoInProgress},
		{Content: "add tests", Status: native.TodoPending},
	}
	out := stripANSI(renderTodos(todos, 80))
	if strings.Contains(out, "Tasks:") {
		t.Fatalf("todo rendering must not keep old header:\n%s", out)
	}
	if !strings.Contains(out, "✓ scaffold parser") {
		t.Fatalf("completed todo missing ✓ gutter:\n%s", out)
	}
	if !strings.Contains(out, "▶ implement parser") {
		t.Fatalf("in-progress todo missing ▶ gutter:\n%s", out)
	}
	if !strings.Contains(out, "· add tests") {
		t.Fatalf("pending todo missing · gutter:\n%s", out)
	}
}

func TestRenderTodosEmptyReturnsEmpty(t *testing.T) {
	if renderTodos(nil, 80) != "" {
		t.Fatal("empty todo list should render empty")
	}
}
