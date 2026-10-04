package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/db"
)

// todoStack seeds a turn driving a four-item list: the first is done, the
// second in progress, the last two waiting. It returns the model with the
// frame already built.
func todoStack(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.resize(100, 60)
	m.state.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
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

// TestPinnedStripHoldsOnlyTheFinishedAndActiveTodos pins the top strip to
// the collapsed done stack and the todo in progress: the waiting ones are
// the bottom band's job, and must not appear here.
func TestPinnedStripHoldsOnlyTheFinishedAndActiveTodos(t *testing.T) {
	m := todoStack(t)
	strip := stripANSI(m.renderTodoStrip())
	for _, want := range []string{"1/4 Read", "2/4 Write"} {
		if !strings.Contains(strip, want) {
			t.Errorf("strip must keep %q:\n%s", want, strip)
		}
	}
	for _, unwanted := range []string{"3/4 Test", "4/4 Ship"} {
		if strings.Contains(strip, unwanted) {
			t.Errorf("waiting todo %q must not be in the top strip:\n%s", unwanted, strip)
		}
	}

	// The strip is the first thing in the frame, and its two rows are the
	// done todo and the active one.
	frame := strings.Split(stripANSI(m.viewString()), "\n")
	for i, w := range []string{"✓ 1/4 Read", "▸ 2/4 Write"} {
		if i >= len(frame) || !strings.Contains(frame[i], w) {
			t.Fatalf("frame row %d should hold %q:\n%s", i, w, strings.Join(frame[:min(6, len(frame))], "\n"))
		}
	}
	if m.todoStripRows() != 2 {
		t.Errorf("strip rows = %d, want 2", m.todoStripRows())
	}
}

// TestWaitingTodosStackInABandBelowTheTranscript is the headline behaviour:
// pending todos render pinned under the transcript, above the now bar.
func TestWaitingTodosStackInABandBelowTheTranscript(t *testing.T) {
	m := todoStack(t)
	band := stripANSI(m.renderTodoBand())
	for _, want := range []string{"3/4 Test", "4/4 Ship"} {
		if !strings.Contains(band, want) {
			t.Errorf("band must list the waiting todo %q:\n%s", want, band)
		}
	}
	for _, unwanted := range []string{"1/4 Read", "2/4 Write"} {
		if strings.Contains(band, unwanted) {
			t.Errorf("band must not repeat the strip's %q:\n%s", unwanted, band)
		}
	}

	frame := strings.Split(stripANSI(m.viewString()), "\n")
	at := func(sub string) int {
		for i, l := range frame {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("frame is missing %q:\n%s", sub, strings.Join(frame, "\n"))
		return -1
	}
	stripRow, bandRow := at("1/4 Read"), at("3/4 Test")
	if !(stripRow < bandRow) {
		t.Fatalf("the strip must sit above the band: strip row %d, band row %d", stripRow, bandRow)
	}
}

// TestTodoBandIsEmptyOnceTheListIsDone keeps a finished list from pinning a
// band for the rest of the session.
func TestTodoBandIsEmptyOnceTheListIsDone(t *testing.T) {
	m := todoStack(t)
	for _, td := range []db.TodoItem{
		{ID: "t2", Content: "Write", Status: "completed", CompletedAt: time.Now()},
		{ID: "t3", Content: "Test", Status: "completed", CompletedAt: time.Now()},
		{ID: "t4", Content: "Ship", Status: "completed", CompletedAt: time.Now()},
	} {
		todos := m.state.Todos()
		for i := range todos {
			if todos[i].ID == td.ID {
				todos[i].Status, todos[i].CompletedAt = "completed", time.Now()
			}
		}
		_ = m.state.SetTodos(append([]db.TodoItem(nil), todos...))
	}
	m.invalidateTranscript()
	m.refreshViewport()
	if rows := m.todoBandRows(); rows != 0 {
		t.Fatalf("a finished list must not pin a band, rows = %d", rows)
	}
	if band := stripANSI(m.renderTodoBand()); band != "" {
		t.Fatalf("band must render empty once nothing is waiting:\n%s", band)
	}
}

// TestTodoBandIsBudgetedInTheFrameHeight is the regression guard for the
// geometry rule in view.go: an unbudgeted band row makes the left column
// taller than the frame and pushes the input area and the status footer off
// the bottom of the screen.
//
// The row count alone cannot catch that — clipLeftColumn trims the surplus
// from the top and reports it through its hook, so the frame still comes out
// the right height with the strip scrolled away. The trim count is the real
// signal.
func TestTodoBandIsBudgetedInTheFrameHeight(t *testing.T) {
	for _, h := range []int{30, 40, 60} {
		t.Run(fmt.Sprintf("h%d", h), func(t *testing.T) {
			m := todoStack(t)
			m.resize(100, h)
			m.invalidateTranscript()
			m.refreshViewport()
			if m.todoBandRows() == 0 {
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
