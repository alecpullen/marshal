package gatepanel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/theme"
)

func testGate() acpwire.SDDGate {
	return acpwire.SDDGate{
		TaskN:     3,
		TaskTitle: "Add the retry helper",
		Question:  "Should this reuse the existing backoff in internal/retry?",
		Report: "STATUS: NEEDS_CONTEXT\n" +
			"TESTS: none yet\n" +
			"QUESTION: Should this reuse the existing backoff in internal/retry?",
	}
}

// key builds a key press for a single rune, mirroring the picker test helper.
func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// typeInto feeds each rune of s into the panel as a key press.
func typeInto(t *testing.T, p *Panel, s string) {
	t.Helper()
	for _, r := range s {
		p.Update(key(r))
	}
}

// StripANSI removes ANSI escape sequences so tests can assert on plain text.
func StripANSI(s string) string { return ansi.Strip(s) }

func TestPanelShowsQuestionWithContext(t *testing.T) {
	got := StripANSI(New(testGate()).View(80, 20))
	for _, want := range []string{
		"Task 3", "Add the retry helper",
		"Should this reuse the existing backoff",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q — the user cannot answer a question they cannot see:\n%s", want, got)
		}
	}
}

func TestPanelShowsExplicitKeyHints(t *testing.T) {
	got := StripANSI(New(testGate()).View(80, 20))
	if !strings.Contains(got, "answer") {
		t.Errorf("panel must advertise that enter answers:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "esc") || !strings.Contains(got, "stop") {
		t.Errorf("panel must advertise that esc stops the run:\n%s", got)
	}
}

func TestEnterEmitsAnswer(t *testing.T) {
	p := New(testGate())
	typeInto(t, p, "reuse it")

	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter with a non-empty answer must emit a command")
	}
	msg, ok := cmd().(AnswerMsg)
	if !ok {
		t.Fatalf("got %T, want AnswerMsg", cmd())
	}
	if msg.Text != "reuse it" {
		t.Errorf("AnswerMsg.Text = %q, want %q", msg.Text, "reuse it")
	}
}

func TestEnterOnEmptyAnswerDoesNothing(t *testing.T) {
	p := New(testGate())
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		if _, ok := cmd().(AnswerMsg); ok {
			t.Error("an empty answer must not resume the run")
		}
	}
}

func TestEscStopsTheRun(t *testing.T) {
	p := New(testGate())
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc must emit a command")
	}
	if _, ok := cmd().(StopMsg); !ok {
		t.Fatalf("got %T, want StopMsg", cmd())
	}
}

func TestReportIsShownButSecondaryToTheQuestion(t *testing.T) {
	got := StripANSI(New(testGate()).View(80, 24))
	qi := strings.Index(got, "Should this reuse")
	ri := strings.Index(got, "STATUS: NEEDS_CONTEXT")
	if qi < 0 {
		t.Fatalf("question missing:\n%s", got)
	}
	if ri >= 0 && ri < qi {
		t.Errorf("the report must not precede the question:\n%s", got)
	}
}

// longGate returns a gate whose question and report both run far past the
// panel's height, and whose report has lines after the six it used to cap at.
func longGate() acpwire.SDDGate {
	var report strings.Builder
	report.WriteString("STATUS: NEEDS_CONTEXT\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&report, "EVIDENCE-LINE-%02d: %s\n", i, strings.Repeat("根拠 ", 10))
	}
	report.WriteString("END-OF-REPORT")
	return acpwire.SDDGate{
		TaskN:     9,
		TaskTitle: "Add the retry helper",
		Question: "QUESTION-START: 既存のバックオフを再利用すべきですか — " +
			strings.Repeat("とても長い質問です。", 8) + " END-OF-QUESTION",
		Report: report.String(),
	}
}

// physicalRows is the number of rows a rendered frame occupies on screen.
func physicalRows(frame string) int {
	return len(strings.Split(strings.TrimRight(frame, "\n"), "\n"))
}

// positionIndicator matches the body's "1–12/40" position line.
var positionIndicator = regexp.MustCompile(`(\d+)–(\d+)/(\d+)`)

// pressKey feeds one key to the panel, failing if it answers or stops the run.
func pressKey(t *testing.T, p *Panel, code rune) tea.Cmd {
	t.Helper()
	cmd := p.Update(tea.KeyPressMsg{Code: code})
	if cmd != nil {
		if _, ok := cmd().(AnswerMsg); ok {
			t.Fatal("paging must not submit the answer")
		}
	}
	return cmd
}

func TestGatePagesQuestionAndFullReport(t *testing.T) {
	p := New(longGate())
	typeInto(t, p, "KEEP-INPUT")

	const width, height = 64, 12
	var frames []string

	// render draws the panel, enforces the height contract and refuses to lose
	// the answer field: a frame the user cannot type into is not a gate.
	render := func() string {
		t.Helper()
		frame := p.View(width, height)
		if got := physicalRows(frame); got > height {
			t.Fatalf("frame is %d rows, budget is %d:\n%s", got, height, StripANSI(frame))
		}
		plain := StripANSI(frame)
		if !strings.Contains(plain, "KEEP-INPUT") {
			t.Fatalf("the answer field is not visible, so the gate cannot be answered:\n%s", plain)
		}
		frames = append(frames, plain)
		return plain
	}

	first := render()
	if !strings.Contains(first, "QUESTION-START") {
		t.Fatalf("the panel must open on the question's first line:\n%s", first)
	}

	// Page with the real key until the report's final line is reachable.
	var end string
	for i := 0; i < 80 && end == ""; i++ {
		pressKey(t, p, tea.KeyPgDown)
		if plain := render(); strings.Contains(plain, "END-OF-REPORT") {
			end = plain
		}
	}
	if end == "" {
		t.Fatalf("the report's last line is unreachable; the panel is still capping evidence:\n%s", frames[len(frames)-1])
	}

	all := strings.Join(frames, "\n")
	if !strings.Contains(all, "EVIDENCE-LINE-08") {
		t.Errorf("report lines past the old six-line cap never appeared")
	}
	if !strings.Contains(all, "⇞⇟ page") {
		t.Errorf("the panel never advertises paging:\n%s", end)
	}
	m := positionIndicator.FindStringSubmatch(all)
	if m == nil {
		t.Errorf("the panel shows no position indicator for a paged body:\n%s", end)
	} else {
		from, last, total := atoi(t, m[1]), atoi(t, m[2]), atoi(t, m[3])
		if from < 1 || from > last || last > total {
			t.Errorf("position indicator %s is incoherent", m[0])
		}
	}

	pressKey(t, p, tea.KeyHome)
	if plain := render(); !strings.Contains(plain, "QUESTION-START") {
		t.Errorf("home must return to the question's first line:\n%s", plain)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("position indicator is not numeric: %q", s)
	}
	return n
}

func TestGatePagingResizePreservesAnswer(t *testing.T) {
	const answer = "reuse the existing backoff"
	const width = 70

	t.Run("answer", func(t *testing.T) {
		p := New(longGate())
		typeInto(t, p, answer)

		// Shrink to the heights a dock hands out, then grow again: the typed
		// answer and the frame's height budget must both survive.
		for _, h := range []int{20, 12, 7, 5, 4, 3, 2, 1, 6, 18} {
			frame := p.View(width, h)
			if got := physicalRows(frame); got > h {
				t.Fatalf("frame is %d rows, budget is %d:\n%s", got, h, StripANSI(frame))
			}
			if h >= 2 && !strings.Contains(StripANSI(frame), answer) {
				t.Fatalf("the typed answer vanished at height %d:\n%s", h, StripANSI(frame))
			}
			pressKey(t, p, tea.KeyPgDown)
		}

		cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter with a non-empty answer must emit a command after paging")
		}
		msg, ok := cmd().(AnswerMsg)
		if !ok {
			t.Fatalf("got %T, want AnswerMsg", msg)
		}
		if msg.Text != answer {
			t.Errorf("AnswerMsg.Text = %q, want the exact answer %q", msg.Text, answer)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		p := New(longGate())
		typeInto(t, p, answer)
		for _, h := range []int{16, 6, 3, 2, 12} {
			p.View(width, h)
			pressKey(t, p, tea.KeyPgDown)
		}
		pressKey(t, p, tea.KeyHome)

		cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if cmd == nil {
			t.Fatal("esc must still stop the run after paging")
		}
		if got, ok := cmd().(StopMsg); !ok {
			t.Fatalf("got %T, want StopMsg", got)
		}
	})
}

// TestGateOverflowThemes pins that the overflow affordances carry their meaning
// without colour. The same paged scenario runs under monochrome, a dark preset
// and a light preset: after stripping ANSI the frame still says how to answer,
// how to stop, how to page, and where in the report the window sits, and every
// frame still fits the budget it was given.
func TestGateOverflowThemes(t *testing.T) {
	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })

	for _, tc := range []struct {
		name string
		th   theme.Theme
	}{
		{"monochrome", theme.LoadFor(true, "xterm-256color")},
		{"dark", theme.LoadWithConfig("warm-sunset", theme.ModeDark, theme.DepthFlat, nil)},
		{"light", theme.LoadWithConfig("warm-sunset-light", theme.ModeLight, theme.DepthFlat, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			theme.Reload(tc.th)

			p := New(longGate())
			typeInto(t, p, "THE-ANSWER")

			// Wide enough for the border hints, short enough that the report
			// cannot possibly fit: the overflow path is what is under test.
			const width, height = 120, 10
			var frames []string
			for i := 0; i < 100; i++ {
				frame := p.View(width, height)
				if got := physicalRows(frame); got > height {
					t.Fatalf("frame is %d rows, budget is %d under %s:\n%s",
						got, height, tc.name, StripANSI(frame))
				}
				plain := StripANSI(frame)
				if !strings.Contains(plain, "THE-ANSWER") {
					t.Fatalf("%s: the answer field vanished, so the gate cannot be answered:\n%s",
						tc.name, plain)
				}
				frames = append(frames, plain)
				if strings.Contains(plain, "END-OF-REPORT") {
					break
				}
				pressKey(t, p, tea.KeyPgDown)
			}

			joined := strings.Join(frames, "\n")
			if !strings.Contains(joined, "END-OF-REPORT") {
				t.Fatalf("%s: paging never reached the report's end:\n%s", tc.name, joined)
			}
			for _, want := range []string{"answer", "stop", "page"} {
				if !strings.Contains(joined, want) {
					t.Errorf("%s: the frame never advertises %q once colour is gone:\n%s",
						tc.name, want, joined)
				}
			}
			if !positionIndicator.MatchString(joined) {
				t.Errorf("%s: no readable position indicator without colour:\n%s", tc.name, joined)
			}
		})
	}
}

func TestPanelDegradesInASmallHeight(t *testing.T) {
	got := StripANSI(New(testGate()).View(60, 5))
	if got == "" {
		t.Error("panel must render something at height 5")
	}
	if len(strings.Split(strings.TrimRight(got, "\n"), "\n")) > 5 {
		t.Errorf("panel exceeded its height budget:\n%s", got)
	}
}
