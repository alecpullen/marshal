package castlist

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func init() {
	theme.Reload(testTheme)
}

func TestNewPanelImplementsDockPanel(t *testing.T) {
	p := New("test", nil, nil)
	if p == nil {
		t.Fatal("New returned nil")
	}
	// Compile-time check: *Panel must satisfy dock.Panel.
	var _ interface {
		Update(msg tea.Msg) tea.Cmd
		View(width, maxHeight int) string
	} = p
}

func TestEnterEmitsStartMsgWhenUnblocked(t *testing.T) {
	p := New("test", []Row{
		{Title: "Alice", Detail: "planner", Badge: "ready"},
		{Title: "Bob", Detail: "implementer", Badge: "ready"},
	}, nil)

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for Enter on unblocked panel")
	}
	msg := cmd()
	if _, ok := msg.(StartMsg); !ok {
		t.Fatalf("expected StartMsg, got %T", msg)
	}
}

func TestEnterBlockedWhenRowHasError(t *testing.T) {
	p := New("test", []Row{
		{Title: "Alice", Detail: "planner", Badge: "ready"},
		{Title: "Bob", Detail: "implementer", Badge: "error", Err: "model unavailable"},
	}, nil)

	if !p.blocked() {
		t.Fatal("expected blocked() to be true when a row has Err")
	}

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("expected nil cmd for Enter when blocked")
	}
}

func TestEscEmitsCancelMsg(t *testing.T) {
	p := New("test", []Row{
		{Title: "Alice", Detail: "planner"},
	}, nil)

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for Esc")
	}
	msg := cmd()
	if _, ok := msg.(CancelMsg); !ok {
		t.Fatalf("expected CancelMsg, got %T", msg)
	}
}

func TestViewRendersRowsAndMetadata(t *testing.T) {
	p := New("Cast", []Row{
		{Title: "Alice", Detail: "planner", Badge: "ready"},
		{Title: "Bob", Detail: "implementer", Badge: "ready", Err: "model unavailable"},
	}, []string{"mode: sdd2", "model: minimax-m3"})

	out := ansi.Strip(p.View(80, 20))
	lines := strings.Split(out, "\n")

	foundTitle := false
	foundMeta := false
	foundAlice := false
	foundBob := false
	foundErr := false
	foundBlocked := false

	for _, l := range lines {
		if strings.Contains(l, "Cast") {
			foundTitle = true
		}
		if strings.Contains(l, "mode: sdd2") {
			foundMeta = true
		}
		if strings.Contains(l, "Alice") {
			foundAlice = true
		}
		if strings.Contains(l, "Bob") {
			foundBob = true
		}
		if strings.Contains(l, "model unavailable") {
			foundErr = true
		}
		if strings.Contains(l, "fix errors") {
			foundBlocked = true
		}
	}

	if !foundTitle {
		t.Error("view missing title")
	}
	if !foundMeta {
		t.Error("view missing metadata")
	}
	if !foundAlice {
		t.Error("view missing Alice row")
	}
	if !foundBob {
		t.Error("view missing Bob row")
	}
	if !foundErr {
		t.Error("view missing error text")
	}
	if !foundBlocked {
		t.Error("view missing blocked indicator")
	}
}

func TestViewHintsChangeWhenBlocked(t *testing.T) {
	// Unblocked: hints should say "↵ start"
	p := New("Cast", []Row{
		{Title: "Alice"},
	}, nil)
	out := ansi.Strip(p.View(80, 20))
	if !strings.Contains(out, "start") {
		t.Errorf("unblocked view should contain 'start' hint, got:\n%s", out)
	}

	// Blocked: hints should say "↵ blocked"
	p2 := New("Cast", []Row{
		{Title: "Alice", Err: "error"},
	}, nil)
	out2 := ansi.Strip(p2.View(80, 20))
	if !strings.Contains(out2, "blocked") {
		t.Errorf("blocked view should contain 'blocked' hint, got:\n%s", out2)
	}
}

func TestViewTruncatesToHeightBudget(t *testing.T) {
	rows := make([]Row, 50)
	for i := range rows {
		rows[i] = Row{Title: "row"}
	}
	p := New("Cast", rows, nil)
	out := ansi.Strip(p.View(80, 5))
	got := len(strings.Split(out, "\n"))
	if got > 5 {
		t.Fatalf("view must not exceed height budget 5, got %d lines:\n%s", got, out)
	}
}

func TestWarnRowRendersButDoesNotBlock(t *testing.T) {
	p := New("Start run?", []Row{
		{Title: "implementer", Detail: "ollama/qwen"},
		{Title: "verify", Warn: "no build or test command configured"},
	}, nil)

	got := ansi.Strip(p.View(80, 20))
	if !strings.Contains(got, "no build or test command configured") {
		t.Errorf("warning text must render:\n%s", got)
	}
	if strings.Contains(got, "fix errors above") {
		t.Errorf("a warning must not render the blocked banner:\n%s", got)
	}
	if p.blocked() {
		t.Error("a warning row must not block the run")
	}
}

func TestErrRowStillBlocks(t *testing.T) {
	p := New("Start run?", []Row{{Title: "implementer", Err: "no model configured"}}, nil)
	if !p.blocked() {
		t.Error("an error row must still block")
	}
}
