package tui

import (
	"fmt"
	"strings"

	"marshal/internal/tools/native"
)

func renderTodos(todos []native.TodoItem, width int) string {
	if len(todos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Tasks:\n")
	for _, t := range todos {
		mark := "○"
		switch t.Status {
		case native.TodoCompleted:
			mark = "✓"
		case native.TodoInProgress:
			mark = "▶"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, t.Content))
	}
	return b.String()
}
