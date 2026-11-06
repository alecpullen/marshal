package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/native"
)

// todoStack seeds a busy turn driving a four-item list: the first is done,
// the second in progress, the last two waiting. It returns the model with the
// frame already built.
//
// The turn must be busy: the band only shows progress while work is running,
// so an abandoned list cannot hold a row for the rest of the session.
func todoStack(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.resize(100, 60)
	stateOf(m).AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	m.busy, m.turnStartedAt = true, m.now().Add(-time.Minute)
	todos := []db.TodoItem{
		{ID: "t1", Content: "Read", Status: "in_progress", StartedAt: time.Now()},
		{ID: "t2", Content: "Write", Status: "pending"},
		{ID: "t3", Content: "Test", Status: "pending"},
		{ID: "t4", Content: "Ship", Status: "pending"},
	}
	_ = stateOf(m).SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Reading it. Slowly.", "a")
	todos[0].Status, todos[0].CompletedAt = "completed", time.Now()
	todos[1].Status, todos[1].StartedAt = "in_progress", time.Now()
	_ = stateOf(m).SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Writing it. Quickly.", "b")
	m.invalidateTranscript()
	m.refreshViewport()
	return m
}

// todoDrivenTurn makes the model's latest turn one that is working from the
// list: a user turn, the list, and a beat bound to its in-progress item.
//
// A list on the session now drives the band whatever the turn did, so this
// helper is no longer required for band rows. It still binds a beat, because
// tests use it to get a running turn.
func todoDrivenTurn(t *testing.T, m *Model, todos []native.TodoItem) {
	t.Helper()
	for i := range todos {
		if todos[i].ID == "" {
			todos[i].ID = fmt.Sprintf("t%d", i+1)
		}
	}
	stateOf(m).AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	if err := stateOf(m).SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	// BeginStep binds the step to the in-progress todo by ID, which is what
	// makes the turn driven by the list.
	stepUnder(m, "Working the list. Steadily.", "w")
	m.invalidateTranscript()
	m.refreshViewport()
}

// The todo counters are the band's inputs; the band itself is no longer a
// surface of its own: the box's plan section draws it, and
// TestBoxIsBudgetedInTheFrameHeight (livebox_view_test.go) guards its rows in
// the height budget.

// maxRowWidth is the widest line in a rendered frame, in cells.
func maxRowWidth(frame string) int {
	widest := 0
	for _, l := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(l); w > widest {
			widest = w
		}
	}
	return widest
}
