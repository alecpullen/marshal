package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// rows splits a rendered view into its plain rows.
func rows(view string) []string {
	return strings.Split(ansi.Strip(view), "\n")
}

func TestFrameClosedDrawsTheFooter(t *testing.T) {
	out := Frame{Title: "Files", Footer: "3 items", Closed: true}.Render("a\nb\nc\nd", 40, 4, testTheme)
	lines := rows(out)
	if len(lines) != 4 {
		t.Fatalf("closed frame on h=4 must be 4 rows, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "╰─ 3 items ") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("footer row = %q, want it to start %q and end %q", last, "╰─ 3 items ", "╯")
	}
	content := lines[1 : len(lines)-1]
	if len(content) != 2 {
		t.Fatalf("closed frame keeps h-2 = 2 content rows, got %d: %q", len(content), content)
	}
	for i, l := range content {
		if !strings.HasPrefix(l, "│ ") || !strings.HasSuffix(l, " │") {
			t.Fatalf("content row %d = %q, want a │ … │ body", i, l)
		}
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 40 {
			t.Fatalf("row %d is %d cells wide, want 40: %q", i, got, l)
		}
	}
}

func TestDividerDrawsAcrossTheFrame(t *testing.T) {
	out := Frame{Title: "Session"}.Render("one\n"+Divider("Agent")+"\ntwo", 40, 6, testTheme)
	lines := rows(out)
	if len(lines) != 4 {
		t.Fatalf("want top border + 3 content rows, got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	divider := lines[2]
	if !strings.HasPrefix(divider, "├─ Agent ") || !strings.HasSuffix(divider, "┤") {
		t.Fatalf("divider row = %q, want it to start %q and end %q", divider, "├─ Agent ", "┤")
	}
	for i, l := range lines {
		if got, want := ansi.StringWidth(l), ansi.StringWidth(lines[0]); got != want {
			t.Fatalf("row %d is %d cells wide, want the frame's %d: %q", i, got, want, l)
		}
	}
}

func TestFirstRowDividerJoinsTheTopBorder(t *testing.T) {
	out := Frame{Title: "Session"}.Render(Divider("▸ CONTEXT")+"\nbody", 40, 6, testTheme)
	lines := rows(out)
	if !strings.HasPrefix(lines[0], "╭─ Session · ▸ CONTEXT ") {
		t.Fatalf("top border = %q, want the divider title joined onto the left label", lines[0])
	}
	for i, l := range lines {
		if strings.Contains(l, "├─ ▸ CONTEXT") {
			t.Fatalf("row %d still draws the consumed divider: %q", i, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("want top border + the one remaining content row, got %d: %q", len(lines), lines)
	}
}

func TestBorderLineKeepsTheRightLabel(t *testing.T) {
	left := strings.Repeat("L", 60)
	const right = "Esc close"

	wide := ansi.Strip(BorderLine("╭─ ", left, right, "╮", 40, testTheme.AccentSecondary))
	if got := ansi.StringWidth(wide); got != 40 {
		t.Fatalf("wide row is %d cells, want 40: %q", got, wide)
	}
	if !strings.Contains(wide, "…") {
		t.Fatalf("wide row should truncate the 60-cell left label: %q", wide)
	}
	if !strings.Contains(wide, right) || !strings.HasSuffix(wide, "─╮") {
		t.Fatalf("wide row dropped the right label: %q", wide)
	}

	narrow := ansi.Strip(BorderLine("╭─ ", left, right, "╮", 20, testTheme.AccentSecondary))
	if got := ansi.StringWidth(narrow); got != 20 {
		t.Fatalf("narrow row is %d cells, want 20: %q", got, narrow)
	}
	if strings.Contains(narrow, right) {
		t.Fatalf("narrow row kept the right label it has no room for: %q", narrow)
	}
	if !strings.Contains(narrow, "…") {
		t.Fatalf("narrow row should truncate the left label: %q", narrow)
	}
}

// A frame is asked for absurd heights only by a shrinking terminal. Render
// must stay within h and never panic. A closed frame needs two rows before it
// can show both a top and a bottom border, so at h=1 it shows only the top —
// which is what "at most h rows" means.
func TestFrameNarrowHeights(t *testing.T) {
	for _, h := range []int{0, 1, 2, 3} {
		out := Frame{Title: "T", Footer: "f", Closed: true}.Render("a\nb\nc", 20, h, testTheme)
		lines := rows(out)
		if want := max(h, 1); len(lines) > want {
			t.Fatalf("h=%d: rendered %d rows, want at most %d: %q", h, len(lines), want, lines)
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got != 20 {
				t.Fatalf("h=%d row %d is %d cells, want 20: %q", h, i, got, l)
			}
		}
	}
	if got := len(rows(Frame{Closed: true}.Render("x", 20, 1, testTheme))); got != 1 {
		t.Fatalf("closed frame at h=1 drew %d rows, want the top border only", got)
	}
	if got := len(rows(Frame{Closed: true}.Render("x", 20, 2, testTheme))); got != 2 {
		t.Fatalf("closed frame at h=2 drew %d rows, want top + bottom", got)
	}
}

// BorderLine is exported, so widths the TUI itself never asks for still have
// to stay inside the width requested rather than overflowing it.
func TestBorderLineNeverExceedsItsWidth(t *testing.T) {
	for _, w := range []int{1, 2, 3, 4, 5, 6, 7, 8, 20, 40} {
		for _, right := range []string{"", "Esc close"} {
			out := ansi.Strip(BorderLine("╭─ ", "a long left label", right, "╮", w, testTheme.AccentSecondary))
			if got := ansi.StringWidth(out); got > max(w, 1) {
				t.Fatalf("w=%d right=%q: row is %d cells, want at most %d: %q", w, right, got, max(w, 1), out)
			}
			if w >= 20 {
				if got := ansi.StringWidth(out); got != w {
					t.Fatalf("w=%d right=%q: row is %d cells, want exactly %d: %q", w, right, got, w, out)
				}
			}
		}
	}
}

func TestCloseAddsOneBorderOnce(t *testing.T) {
	open := Frame{Title: "T"}.Render("body", 20, 4, testTheme)
	closed := Close(open, 20, FocusColor(testTheme))
	if got, want := len(rows(closed)), len(rows(open))+1; got != want {
		t.Fatalf("Close grew the view to %d rows, want %d:\n%s", got, want, ansi.Strip(closed))
	}
	if last := rows(closed)[len(rows(closed))-1]; !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
		t.Fatalf("Close appended %q, want a ╰…╯ bottom border", last)
	}
	if again := Close(closed, 20, FocusColor(testTheme)); again != closed {
		t.Fatalf("Close changed an already closed view:\n%q\n%q", closed, again)
	}
}

func TestJoinTopMakesTees(t *testing.T) {
	open := Frame{Title: "T"}.Render("a\nb", 20, 4, testTheme)
	joined := JoinTop(open)

	before, after := rows(open), rows(joined)
	if len(before) != len(after) {
		t.Fatalf("JoinTop changed the row count: %d → %d", len(before), len(after))
	}
	if !strings.HasPrefix(after[0], "├") || !strings.HasSuffix(after[0], "┤") {
		t.Fatalf("joined top row = %q, want it to start %q and end %q", after[0], "├", "┤")
	}
	for i := 1; i < len(before); i++ {
		if before[i] != after[i] {
			t.Fatalf("JoinTop changed row %d: %q → %q", i, before[i], after[i])
		}
	}
}

func TestIsFramed(t *testing.T) {
	framed := Frame{Title: "T"}.Render("body", 20, 4, testTheme)
	if !IsFramed(framed) {
		t.Fatalf("IsFramed(open frame) = false:\n%s", ansi.Strip(framed))
	}
	if !IsFramed(JoinTop(framed)) {
		t.Fatalf("IsFramed(joined frame) = false:\n%s", ansi.Strip(JoinTop(framed)))
	}
	if IsFramed("plain text\nno frame here") {
		t.Fatal("IsFramed(plain text) = true")
	}
	if IsFramed("") {
		t.Fatal("IsFramed(empty) = true")
	}
}

// The design system requires chrome to stay legible with color stripped:
// under NO_COLOR a frame must emit no escape sequences at all.
func TestFrameMonochromeEmitsNoEscapes(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	prev := theme.Current()
	theme.Reload(theme.Load())
	t.Cleanup(func() { theme.Reload(prev) })

	out := Frame{Title: "Session", Hints: "esc close", Footer: "3 items", Closed: true, Focused: true}.
		Render("one\n"+Divider("Agent")+"\ntwo", 40, 6, theme.Current())
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("frame emitted ANSI escapes under NO_COLOR:\n%q", out)
	}
}
