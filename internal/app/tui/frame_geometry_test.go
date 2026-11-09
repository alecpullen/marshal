package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/postmortempanel"
	"marshal/internal/app/tui/sddreview"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/native"
)

// overRenderingPanel is a dock panel that ignores its height budget,
// simulating any panel whose rendered rows exceed what the layout budgeted.
type overRenderingPanel struct{ rows int }

func (p overRenderingPanel) Update(tea.Msg) tea.Cmd { return nil }
func (p overRenderingPanel) View(_, _ int) string {
	return strings.Repeat("panel row\n", p.rows-1) + "panel row"
}
func (p overRenderingPanel) Sizing() dock.Sizing { return dock.FullFrame }

// generatedReviewFixture writes a blocked candidate whose diagnostics are
// wider than the panel at the supported minimum, and returns the result the
// /sdd new flow would dock for it. The markers avoid wrap breakpoints, so only
// a real failure can hide one.
func generatedReviewFixture(t *testing.T) (acpwire.AuthoredPlan, []string) {
	t.Helper()

	path := filepath.Join(t.TempDir(),
		"pathelementalphaqz", "pathelementbravoqz", "pathelementcharlieqz", "generated-plan.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create candidate directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	filler := strings.Repeat("detail that runs well past the panel width ", 2)
	return acpwire.AuthoredPlan{
			PlanPath: path,
			Inspection: &acpwire.PlanInspection{
				Strategy:  acpwire.StrategyAdaptive,
				HasBlocks: true,
				Tasks:     []acpwire.PlanTask{{TaskN: 1, Title: "Add file"}},
				Diagnostics: []acpwire.PlanDiagnostic{
					{TaskN: 0, Severity: acpwire.DiagnosticError, Line: 12, Message: "diagoneqzstart " + filler + "diagoneqzend"},
					{TaskN: 1, Severity: acpwire.DiagnosticWarning, Message: "diagtwoqzstart " + filler + "diagtwoqzend"},
					{TaskN: 2, Severity: acpwire.DiagnosticError, Line: 344, Message: "diagthreeqzstart " + filler + "diagthreeqzend"},
				},
			},
		}, []string{
			"diagoneqzstart", "diagoneqzend",
			"diagtwoqzstart", "diagtwoqzend",
			"diagthreeqzstart", "diagthreeqzend",
			"pathelementalphaqz", "pathelementcharlieqz",
			"plan error", "task 1 warning", "line 344",
		}
}

// generatedReviewFrameModel is the parent model over a populated transcript and
// tray, with the real review docked through its own model, at w×h.
func generatedReviewFrameModel(t *testing.T, w, h int, result acpwire.AuthoredPlan) *Model {
	t.Helper()
	m := newAuthoringModel(t)
	for i := 0; i < 60; i++ {
		stateOf(m).AddMessage(session.RoleAssistant, fmt.Sprintf("transcript line %d", i), session.ContentTypePlain)
	}
	todoDrivenTurn(t, &m, []native.TodoItem{
		{Content: "first task", Status: native.TodoCompleted},
		{Content: "second task", Status: native.TodoInProgress},
	})
	m.dock.Open(sddreview.New(result))
	m.invalidateTranscript()
	m.refreshViewport()

	// Resize through the model's own routing, which is also what disarms the
	// review's discard confirmation on a geometry change.
	next, _ := press(m, tea.WindowSizeMsg{Width: w, Height: h})
	next.refreshViewport()
	return &next
}

// pageReviewThroughModel renders the composed frame and pages the docked review
// to its end through the model, returning every frame it showed. Each frame is
// asserted to fill the terminal exactly and to name the review's actions.
func pageReviewThroughModel(t *testing.T, m *Model, geometry string, w, h int) []string {
	t.Helper()

	var frames []string
	prev := ""
	for page := 0; page < 100; page++ {
		frame := stripANSI(m.viewString())
		// The frame fills the terminal exactly: a layout change that over- or
		// under-fills either axis is a regression, not a cosmetic difference.
		if rows, widest := frameRows(m); rows != h || widest != w {
			t.Fatalf("%s: frame is %d rows (want %d) and %d wide (want %d):\n%s",
				geometry, rows, h, widest, w, frame)
		}
		for _, want := range []string{"Review plan", "esc keep", "d discard"} {
			if !strings.Contains(frame, want) {
				t.Fatalf("%s: composed frame is missing %q:\n%s", geometry, want, frame)
			}
		}
		if page > 0 && frame == prev {
			return frames // the window stopped moving: the end is on screen
		}
		frames = append(frames, frame)
		prev = frame

		next, _ := press(*m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		*m = next
	}
	t.Fatalf("%s: paging the docked review did not terminate", geometry)
	return frames
}

// TestGeneratedPlanReviewFitsComposedFrame drives the real generated-plan
// review through the parent model: over a populated transcript and tray, at
// every supported geometry, the composed frame fills the terminal exactly, the
// review's actions stay on screen, and every diagnostic is reachable by paging
// — including after the terminal is resized under the open review.
func TestGeneratedPlanReviewFitsComposedFrame(t *testing.T) {
	geometries := []struct {
		name string
		w, h int
	}{
		{"80x24", 80, 24},
		{"120x40", 120, 40},
		{"200x60", 200, 60},
	}

	for i, tc := range geometries {
		t.Run(tc.name, func(t *testing.T) {
			result, markers := generatedReviewFixture(t)
			m := generatedReviewFrameModel(t, tc.w, tc.h, result)

			if _, ok := m.dock.Panel().(*sddreview.Panel); !ok {
				t.Fatalf("expected the real review in the dock, got %T", m.dock.Panel())
			}

			// The candidate path and the diagnostics are reachable at this
			// geometry, and nothing about reading it leaves the frame.
			text := strings.Join(pageReviewThroughModel(t, m, tc.name, tc.w, tc.h), "\n")
			for _, want := range markers {
				if !strings.Contains(text, want) {
					t.Errorf("%s: %q is not reachable in the composed frame:\n%s", tc.name, want, text)
				}
			}

			// A resize under the open review must reflow, not lose content: the
			// frame still fills the new terminal and every diagnostic is still
			// reachable from the top.
			other := geometries[(i+1)%len(geometries)]
			next, _ := press(*m, tea.WindowSizeMsg{Width: other.w, Height: other.h})
			next.refreshViewport()
			*m = next
			nextPanel, _ := press(*m, tea.KeyPressMsg{Code: tea.KeyHome})
			*m = nextPanel

			resized := strings.Join(pageReviewThroughModel(t, m, tc.name+" after resize to "+other.name, other.w, other.h), "\n")
			for _, want := range markers {
				if !strings.Contains(resized, want) {
					t.Errorf("%s: %q is not reachable after a resize to %s:\n%s",
						tc.name, want, other.name, resized)
				}
			}
		})
	}
}

func TestClipLeftColumn(t *testing.T) {
	three := "a\nb\nc"
	if got := clipLeftColumn(three, 5); got != three {
		t.Errorf("under-height column changed: %q", got)
	}
	if got := clipLeftColumn(three, 3); got != three {
		t.Errorf("exact-height column changed: %q", got)
	}
	if got := clipLeftColumn(three, 2); got != "b\nc" {
		t.Errorf("clipLeftColumn = %q, want %q (surplus dropped from the top)", got, "b\nc")
	}
	if got := clipLeftColumn(three, 0); got != "c" {
		t.Errorf("clipLeftColumn with 0 budget = %q, want %q (floors to 1 row)", got, "c")
	}
}

// TestOverRenderingPanelCannotHideFooter proves the frame-height invariant
// end to end: even when a panel renders far more rows than the layout
// budgeted, the frame stays exactly terminal-height and the status footer
// remains on the last row.
func TestOverRenderingPanelCannotHideFooter(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	m.dock.Open(overRenderingPanel{rows: 45})

	lines := strings.Split(stripANSI(m.viewString()), "\n")
	if len(lines) != 30 {
		t.Fatalf("frame rows = %d, want 30 (over-rendering panel pushed chrome off screen)", len(lines))
	}
	if strings.TrimSpace(lines[29]) == "" {
		t.Errorf("status footer row is blank; footer pushed off screen:\n%s", strings.Join(lines, "\n"))
	}
}

// frameRows renders the frame and reports its row count and widest line.
func frameRows(m *Model) (rows, widest int) {
	lines := strings.Split(m.viewString(), "\n")
	for _, l := range lines {
		if lw := ansi.StringWidth(l); lw > widest {
			widest = lw
		}
	}
	return len(lines), widest
}

// fillTranscript adds enough messages to overflow the viewport, then scrolls
// off the bottom so the "↑ scrolled" hint row appears.
func scrolledModel(t *testing.T, w, h int) *Model {
	t.Helper()
	m := newTestModel(t)
	stateOf(m).Config.TUI.SidePanel.Enabled = true
	m.resize(w, h)
	for i := 0; i < 200; i++ {
		stateOf(m).AddMessage(session.RoleAssistant, "line", session.ContentTypePlain)
	}
	m.refreshViewport()
	m.viewportFollow = false
	m.refreshViewport()
	return &m
}

// TestScrollHintDoesNotOverflowFrame is the regression gate for the
// disappearing input area. The scroll hint row is rendered above the
// transcript, so it must be part of the row budget — otherwise the left
// column is one row taller than the terminal and the bottom row (the input,
// then the status line) is pushed off screen.
func TestScrollHintDoesNotOverflowFrame(t *testing.T) {
	for _, tc := range []struct {
		name string
		busy bool
	}{
		{"idle", false},
		{"busy", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := scrolledModel(t, 140, 30)
			if tc.busy {
				stateOf(m).SetActivity(session.Activity{Kind: session.ActivityThinking})
				m.refreshViewport()
			}
			// Precondition: the hint must actually be showing.
			if !strings.Contains(stripANSI(m.renderTranscriptFrame()), "scrolled") {
				t.Fatal("scroll hint not rendered; scenario no longer exercises the bug")
			}
			rows, widest := frameRows(m)
			if rows != 30 {
				t.Errorf("frame rows = %d, want 30", rows)
			}
			if widest > 140 {
				t.Errorf("widest line = %d, want <= 140", widest)
			}
		})
	}
}

// TestFollowingFrameFitsTerminal guards the non-scrolled baseline: no hint
// row, and the frame still exactly fills the terminal.
func TestFollowingFrameFitsTerminal(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).Config.TUI.SidePanel.Enabled = true
	m.resize(140, 30)
	for i := 0; i < 200; i++ {
		stateOf(m).AddMessage(session.RoleAssistant, "line", session.ContentTypePlain)
	}
	m.refreshViewport()
	if rows, widest := frameRows(&m); rows != 30 || widest > 140 {
		t.Errorf("rows = %d (want 30), widest = %d (want <= 140)", rows, widest)
	}
}

// TestEndingSurfacesFitTheTerminal pins the ending stack against the frame
// budget: the active-work warning (with a long membership) and the exit
// postmortem question, at every swept size, must render a frame exactly as tall
// as the terminal and no wider — over a full transcript, so the panels are
// competing for the same rows the transcript wants.
func TestEndingSurfacesFitTheTerminal(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}, {200, 60}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			// A long membership: the warning must window rather than overflow.
			m := scrolledModel(t, size.w, size.h)
			stateOf(m).Config.Postmortem.OnExit = "prompt"
			stateOf(m).AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
			*m = withEndService(t, *m, &endTestController{work: longWorkSnapshot(16)})
			if cmd := m.beginEnd(&endIntent{kind: EndIntentExit}); cmd != nil {
				t.Fatalf("warning path returned a cmd (%T), want nil", cmd)
			}
			if rows, widest := frameRows(m); rows != size.h || widest > size.w {
				t.Errorf("warning frame: rows = %d (want %d), widest = %d (want <= %d)",
					rows, size.h, widest, size.w)
			}

			// The postmortem question over the same transcript, at its agent
			// stage (the longest content).
			m.endStage = endPostmortem
			m.openPostmortemPrompt()
			if panel, ok := m.dock.Panel().(*postmortempanel.Panel); ok {
				panel.Update(tea.KeyPressMsg{Code: 'y'})
			}
			if rows, widest := frameRows(m); rows != size.h || widest > size.w {
				t.Errorf("postmortem frame: rows = %d (want %d), widest = %d (want <= %d)",
					rows, size.h, widest, size.w)
			}
		})
	}
}

// postmortemConsentFrameModel opens the real exit-postmortem question over a
// populated transcript and tray, and drives it from its first question to the
// agent disclosure through the model's own routing — so the panel is at the
// stage whose consent matters, at a size it can render.
func postmortemConsentFrameModel(t *testing.T, w, h int) *Model {
	t.Helper()
	m := scrolledModel(t, w, h)
	stateOf(m).Config.Postmortem.OnExit = "prompt"
	stateOf(m).AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	if err := stateOf(m).SetTodos([]native.TodoItem{
		{ID: "t1", Content: "first task", Status: native.TodoInProgress},
		{ID: "t2", Content: "second task", Status: native.TodoPending},
	}); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
	*m = withEndService(t, *m, &endTestController{})
	m.end = &endIntent{kind: acpwire.EndIntentExit}
	m.endStage = endPostmortem
	m.openPostmortemPrompt()
	m.refreshViewport()
	next, cmd := press(*m, tea.KeyPressMsg{Code: 'y'})
	if cmd != nil {
		t.Fatalf("advancing to the agent question returned a cmd (%T), want nil", cmd)
	}
	return &next
}

// TestPostmortemConsentVisibleInComposedFrame pins the consent gate to the
// composed frame rather than to the panel's own idea of its size: over a
// populated transcript and tray, at every supported geometry, the agent
// question's two full disclosure lines and both choices must survive in the
// rendered view, the frame must still fill the terminal exactly, and each
// answer must still reach the result it always produced. The minimum supported
// size is repeated with NO_COLOR, where nothing can fit by luck of a style.
func TestPostmortemConsentVisibleInComposedFrame(t *testing.T) {
	prevTheme := theme.Current()
	t.Cleanup(func() { theme.Reload(prevTheme) })

	disclosure := []string{
		"Include agent analysis?",
		"Agent analysis has system access and editing approval.",
		"It can access and edit files outside this workspace.",
		"▸ Yes",
		"No",
	}
	for _, tc := range []struct {
		name    string
		w, h    int
		noColor bool
	}{
		{"80x24", 80, 24, false},
		{"120x40", 120, 40, false},
		{"200x60", 200, 60, false},
		{"80x24-no-color", 80, 24, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			theme.Reload(theme.LoadFor(tc.noColor, "xterm-256color"))
			m := postmortemConsentFrameModel(t, tc.w, tc.h)

			frame := stripANSI(m.View().Content)
			for _, want := range disclosure {
				if !strings.Contains(frame, want) {
					t.Fatalf("%dx%d frame is missing %q; the disclosure and the choices must be on screen:\n%s",
						tc.w, tc.h, want, frame)
				}
			}
			if rows, widest := frameRows(m); rows != tc.h || widest > tc.w {
				t.Errorf("%dx%d frame: rows = %d (want %d), widest = %d (want <= %d)",
					tc.w, tc.h, rows, tc.h, widest, tc.w)
			}

			// The choices the frame just showed are the ones that answer: No
			// still extracts only, Yes still runs the scoped analysis pass.
			for _, answer := range []struct {
				key  rune
				want postmortempanel.Result
			}{
				{'n', postmortempanel.ResultExtractOnly},
				{'y', postmortempanel.ResultWithAgent},
			} {
				_, cmd := press(*m, tea.KeyPressMsg{Code: answer.key})
				if cmd == nil {
					t.Fatalf("%q produced no answer after the disclosure was rendered", string(answer.key))
				}
				done, ok := cmd().(postmortempanel.DoneMsg)
				if !ok {
					t.Fatalf("%q produced %T, want postmortempanel.DoneMsg", string(answer.key), cmd())
				}
				if done.Result != answer.want {
					t.Errorf("%q = %v, want %v", string(answer.key), done.Result, answer.want)
				}
			}
		})
	}
}

// TestInputAndFooterVisibleWithTodosAndFullTranscript is the regression gate
// for the reported bug: with a full (overflowing) transcript and the todo
// panel visible, the text entry box and the status footer must stay on
// screen — both while following the transcript and while scrolled up.
func TestInputAndFooterVisibleWithTodosAndFullTranscript(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scrolled bool
		busy     bool
	}{
		{"following", false, false},
		{"scrolled", true, false},
		{"scrolled_busy", true, true},
		{"following_busy", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := scrolledModel(t, 140, 30)
			todoDrivenTurn(t, m, []native.TodoItem{
				{Content: "first task", Status: native.TodoCompleted},
				{Content: "second task", Status: native.TodoInProgress},
				{Content: "third task", Status: native.TodoPending},
			})
			if !tc.scrolled {
				m.viewportFollow = true
			}
			if tc.busy {
				m.busy = true
				m.turnStartedAt = m.now()
			}
			m.refreshViewport()

			// Precondition: the plan must actually be showing in the tray.
			frame := stripANSI(m.viewString())
			if tc.busy && !strings.Contains(frame, "second task") {
				t.Fatalf("plan rows missing; scenario no longer exercises the bug:\n%s", frame)
			}

			lines := strings.Split(frame, "\n")
			if len(lines) != 30 {
				t.Fatalf("frame rows = %d, want 30 (overflow pushes input/footer off screen):\n%s", len(lines), frame)
			}

			inputRow := -1
			for i, line := range lines {
				if strings.Contains(line, "❯") {
					inputRow = i
				}
			}
			if inputRow < 0 {
				t.Fatalf("input box missing from the frame:\n%s", frame)
			}
			if inputRow >= 29 {
				t.Errorf("input box at row %d leaves no room for the status footer:\n%s", inputRow, frame)
			}
			if strings.TrimSpace(lines[29]) == "" {
				t.Errorf("status footer row is blank; footer pushed off screen:\n%s", frame)
			}
		})
	}
}
