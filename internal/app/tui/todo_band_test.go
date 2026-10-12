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
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	m.busy, m.turnStartedAt = true, m.now().Add(-time.Minute)
	todos := []db.TodoItem{
		{ID: "t1", Content: "Read", Status: "in_progress", StartedAt: time.Now()},
		{ID: "t2", Content: "Write", Status: "pending"},
		{ID: "t3", Content: "Test", Status: "pending"},
		{ID: "t4", Content: "Ship", Status: "pending"},
	}
	_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Reading it. Slowly.", "a")
	todos[0].Status, todos[0].CompletedAt = "completed", time.Now()
	todos[1].Status, todos[1].StartedAt = "in_progress", time.Now()
	_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	stepUnder(&m, "Writing it. Quickly.", "b")
	m.invalidateTranscript()
	m.refreshViewport()
	return m
}

// todoDrivenTurn makes the model's latest turn one that is working from the
// list: a user turn, the list, and a beat bound to its in-progress item.
//
// The band only shows todo progress for such a turn (viewmodel's
// DrivenByTodos), so a list merely present on the session — left over from an
// earlier turn, or never touched by this one — holds no band rows. Tests that
// want band progress must say so through this helper rather than by setting
// todos alone.
func todoDrivenTurn(t *testing.T, m *Model, todos []native.TodoItem) {
	t.Helper()
	for i := range todos {
		if todos[i].ID == "" {
			todos[i].ID = fmt.Sprintf("t%d", i+1)
		}
	}
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	if err := m.state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	// BeginStep binds the step to the in-progress todo by ID, which is what
	// makes the turn driven by the list.
	stepUnder(m, "Working the list. Steadily.", "w")
	m.invalidateTranscript()
	m.refreshViewport()
}

// todoProgress and the todo counters are the band's inputs; the band itself
// is rendered by the now bar and pinned in nowbar_test.go.

// TestTodoBandIsBudgetedInTheFrameHeight is the regression guard for the
// geometry rule in view.go: an unbudgeted band row makes the left column
// taller than the frame and pushes the input area and the status footer off
// the bottom of the screen.
//
// The band's rows are the now bar's rows now, so the guard covers the same
// ground through nowBarRows.
//
// The row count alone cannot catch an unbudgeted row — clipLeftColumn trims
// the surplus from the top and reports it through its hook, so the frame
// still comes out the right height with the transcript scrolled away. The
// trim count is the real signal.
func TestTodoBandIsBudgetedInTheFrameHeight(t *testing.T) {
	for _, h := range []int{30, 40, 60} {
		t.Run(fmt.Sprintf("h%d", h), func(t *testing.T) {
			m := todoStack(t)
			m.resize(100, h)
			m.invalidateTranscript()
			m.refreshViewport()
			if m.nowBarRows() == 0 {
				t.Fatalf("precondition: the band must be showing")
			}
			trimmed := 0
			clipLeftColumnHook = func(n int) { trimmed += n }
			frame := stripANSI(m.viewString())
			clipLeftColumnHook = nil

			if trimmed != 0 {
				t.Errorf("clipLeftColumn trimmed %d rows: the band is not in the height budget", trimmed)
			}
			if rows := len(strings.Split(frame, "\n")); rows != h {
				t.Errorf("frame rows = %d, want %d", rows, h)
			}
			if widest := maxRowWidth(frame); widest > 100 {
				t.Errorf("widest row = %d, want <= 100", widest)
			}
		})
	}
}

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
