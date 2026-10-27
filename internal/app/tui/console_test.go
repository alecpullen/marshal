package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
)

// consoleTestModel is an idle, trusted session at the given terminal size: the
// starting point for the console's frame assertions.
func consoleTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := newTestModel(t)
	m.resize(width, height)
	m.state.SetTrusted(true)
	m.refreshViewport()
	return m
}

// consoleRows renders the frame and returns its rows with styling stripped.
func consoleRows(t *testing.T, m Model) []string {
	t.Helper()
	return strings.Split(stripANSI(m.viewString()), "\n")
}

// rowOf returns the index of the first row containing substr, or -1.
func rowOf(rows []string, substr string) int {
	for i, row := range rows {
		if strings.Contains(row, substr) {
			return i
		}
	}
	return -1
}

func TestConsoleOpensWithNothingAbove(t *testing.T) {
	m := consoleTestModel(t, 80, 24)
	rows := consoleRows(t, m)
	composer := rowOf(rows, "│ ❯")
	if composer < 0 {
		t.Fatalf("no composer row in the frame:\n%s", strings.Join(rows, "\n"))
	}
	header := rows[composer-1]
	if !strings.HasPrefix(header, "╭───") {
		t.Fatalf("with nothing framed above, the console opens with a corner: %q", header)
	}
	if !strings.HasSuffix(header, "╮") {
		t.Fatalf("the console's top border does not close: %q", header)
	}
}

func TestConsoleJoinsTheTray(t *testing.T) {
	m := consoleTestModel(t, 80, 24)
	m.workSnapshot = workSnapOf(workRunningCard("work:01", workT0.Add(-1_000_000_000)))
	m.busy = true
	m.refreshViewport()

	rows := consoleRows(t, m)
	composer := rowOf(rows, "│ ❯")
	if composer < 0 {
		t.Fatalf("no composer row in the frame:\n%s", strings.Join(rows, "\n"))
	}
	header := rows[composer-1]
	if !strings.HasPrefix(header, "├─") || !strings.HasSuffix(header, "┤") {
		t.Fatalf("the console must join the tray's outline with tees: %q", header)
	}
	for _, row := range rows[:composer-1] {
		if strings.HasPrefix(row, "╰") {
			t.Fatalf("the tray still closes its outline above the console: %q", row)
		}
	}
	last := rows[len(rows)-1]
	if !strings.HasPrefix(last, "╰─ ") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("the status border must close the frame: %q", last)
	}
}

func TestConsoleContinuesUnderAnOpenTray(t *testing.T) {
	m := consoleTestModel(t, 80, 24)
	// Enough running work that the tray exceeds its row budget and sheds: the
	// first shed step folds the plan into the tray's last border row.
	cards := make([]viewmodel.WorkItem, 0, 6)
	for i := range 6 {
		cards = append(cards, workRunningCard(fmt.Sprintf("work:%02d", i), workT0.Add(-time.Duration(i)*time.Second)))
	}
	m.workSnapshot = workSnapOf(cards...)
	mustSetTodos(t, &m, native.TodoItem{Content: "a task", Status: native.TodoCompleted})
	m.busy = true
	m.refreshViewport()
	if !m.boxLayout().Open {
		t.Fatalf("fixture: the tray should end on the folded plan's border row:\n%s",
			stripANSI(boxOut(m)))
	}

	rows := consoleRows(t, m)
	plan := rowOf(rows, "├─ plan")
	if plan < 0 {
		t.Fatalf("no folded plan row in the frame:\n%s", strings.Join(rows, "\n"))
	}
	composer := rowOf(rows, "│ ❯")
	if composer != plan+1 {
		t.Fatalf("the console must continue directly under the open tray: plan row %d, composer row %d",
			plan, composer)
	}
}

func TestPopupIsAConsoleSection(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 80, 24)
	m.workSnapshot = workSnapOf(workRunningCard("work:01", workT0.Add(-1_000_000_000)))
	m.busy = true
	m.input.SetValue("/")
	m.updateCompletionPopups()
	if m.activeCompletionPopup() == nil {
		t.Fatal("fixture: expected a visible popup for /")
	}
	m.refreshViewport()

	rows := consoleRows(t, m)
	header := rowOf(rows, "├─ Commands")
	if header < 0 {
		t.Fatalf("the popup is not a labelled console section:\n%s", strings.Join(rows, "\n"))
	}
	divider := -1
	for i := header + 1; i < len(rows); i++ {
		if strings.HasPrefix(rows[i], "├───") && strings.HasSuffix(rows[i], "┤") {
			divider = i
			break
		}
	}
	if divider < 0 {
		t.Fatalf("no plain divider between the popup and the composer:\n%s", strings.Join(rows, "\n"))
	}
	if got := rows[divider+1]; !strings.HasPrefix(got, "│ ❯ /") {
		t.Fatalf("the composer does not follow the popup's divider: %q", got)
	}
}

func TestDockedPanelJoinsTheOutline(t *testing.T) {
	m := consoleTestModel(t, 80, 24)
	m.workSnapshot = workSnapOf(workRunningCard("work:01", workT0.Add(-1_000_000_000)))
	m.busy = true
	m.openSessionSheet()
	if !m.dock.IsOpen() {
		t.Fatal("fixture: the session sheet should hold the dock")
	}
	m.refreshViewport()

	rows := consoleRows(t, m)
	sheet := rowOf(rows, "Session")
	if sheet < 0 {
		t.Fatalf("no sheet row in the frame:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[sheet], "├─ ") {
		t.Fatalf("the docked sheet must join the tray's outline: %q", rows[sheet])
	}
	composer := rowOf(rows, "│ ❯")
	if composer < 0 {
		t.Fatalf("no composer row in the frame:\n%s", strings.Join(rows, "\n"))
	}
	if got := rows[composer-1]; !strings.HasPrefix(got, "├───") {
		t.Fatalf("the console must join the panel above it with a plain divider: %q", got)
	}
}

func TestFullFrameKeepsThePlainStatusLine(t *testing.T) {
	m := consoleTestModel(t, 100, 30)
	m.openSettingsBrowser("")
	if !m.dock.FullFrameOpen() {
		t.Fatal("fixture: /settings should own the whole frame")
	}

	rows := consoleRows(t, m)
	last := rows[len(rows)-1]
	if strings.HasPrefix(last, "╰─") {
		t.Fatalf("a full-frame panel closes its own frame; the status row must stay plain: %q", last)
	}
	if !strings.Contains(last, "Act") && !strings.Contains(last, "Ask") {
		t.Fatalf("the plain status row lost the mode cue: %q", last)
	}
}

// TestPromptStatusRowKeepsTheOutline: a pending question turns the console
// violet and the status border still closes the frame.
func TestPromptStatusRowKeepsTheOutline(t *testing.T) {
	m := consoleTestModel(t, 80, 24)
	m.state.SetPendingQuestion(&session.PendingQuestion{
		Questions: []session.Question{{Question: "what?"}},
	})
	m.refreshViewport()

	rows := consoleRows(t, m)
	last := rows[len(rows)-1]
	if !strings.HasPrefix(last, "╰─ ") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("the status border must close the frame under a prompt: %q", last)
	}
}

// TestPromptSectionsCarryLabelsAndKeys: a pending prompt is a labelled,
// coloured section whose header carries the keys the footer used to show.
func TestPromptSectionsCarryLabelsAndKeys(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		m := consoleTestModel(t, 80, 24)
		m.state.SetPendingApproval(&session.PendingToolCall{
			ID:           "call-console",
			Name:         "shell.run",
			Command:      "git push",
			Risk:         "command",
			ResponseChan: make(chan session.UserApprovalDecision, 1),
		})
		out := stripANSI(m.renderInputArea())
		first := strings.Split(out, "\n")[0]
		if !strings.HasPrefix(first, "╭─ ⚠ Approval") {
			t.Fatalf("approval section header = %q", first)
		}
		if !strings.Contains(first, "←→ choose") {
			t.Fatalf("approval header lost its keys: %q", first)
		}
	})
	t.Run("question", func(t *testing.T) {
		m := consoleTestModel(t, 80, 24)
		m.state.SetPendingQuestion(&session.PendingQuestion{
			Questions: []session.Question{{Question: "what?"}},
		})
		out := stripANSI(m.renderInputArea())
		first := strings.Split(out, "\n")[0]
		if !strings.HasPrefix(first, "╭─ ? Question") {
			t.Fatalf("question section header = %q", first)
		}
		if !strings.Contains(first, "Enter answer") {
			t.Fatalf("question header lost its keys: %q", first)
		}
	})
}

// TestStatusLineDropsPromptKeysWhileFramed: the prompt's keys live in its
// header, so the status row never repeats them. (While a prompt is pending the
// status right cluster is the turn state, so the stop key only reaches the row
// through help.Footer's hint cluster — see TestFooterKeysInFrame.)
func TestStatusLineDropsPromptKeysWhileFramed(t *testing.T) {
	m := consoleTestModel(t, 80, 24)
	m.state.SetPendingQuestion(&session.PendingQuestion{
		Questions: []session.Question{{Question: "what?"}},
	})
	m.busy = true
	m.turnStartedAt = m.now().Add(-5 * time.Second)
	m.refreshViewport()

	rows := consoleRows(t, m)
	last := rows[len(rows)-1]
	if strings.Contains(last, "Enter answer") || strings.Contains(last, "Esc skip") {
		t.Fatalf("the status row repeats the prompt keys its header already draws: %q", last)
	}
	if !strings.HasPrefix(last, "╰─ ") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("the status border must still close the frame: %q", last)
	}
}
