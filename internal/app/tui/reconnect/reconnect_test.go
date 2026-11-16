package reconnect

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

// updateGoldens rewrites the screen's goldens instead of comparing them.
var updateGoldens = flag.Bool("update", false, "rewrite the reconnect goldens")

// screenNow is the fixed clock every test runs on. It lies in UTC so the
// seconds left are the same on every machine.
var screenNow = time.Unix(1_700_000_000, 0).UTC()

// screenDeadline is the restart's deadline: thirty seconds from screenNow.
var screenDeadline = screenNow.Add(30 * time.Second)

// fixedClock pins the screen's clock, so no test waits a real second.
func fixedClock() Option { return WithNow(func() time.Time { return screenNow }) }

// attemptsOf returns a channel already holding the given attempts.
func attemptsOf(attempts ...Attempt) chan Attempt {
	ch := make(chan Attempt, len(attempts))
	for _, attempt := range attempts {
		ch <- attempt
	}
	return ch
}

// sized builds the screen at a size, as the program reports it.
func sized(t *testing.T, label, reason string, ch <-chan Attempt, width, height int) Model {
	t.Helper()
	m := New(label, reason, screenDeadline, ch, fixedClock())
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return asModel(t, next)
}

// asModel asserts an Update result is the screen's model.
func asModel(t *testing.T, next tea.Model) Model {
	t.Helper()
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want reconnect.Model", next)
	}
	return got
}

// readAttempt delivers the next attempt off the channel the way the screen
// does, through its own wait command, and applies it. The channel must already
// hold an attempt or be closed.
func readAttempt(t *testing.T, m Model) Model {
	t.Helper()
	cmd := m.waitForAttempt()
	if cmd == nil {
		t.Fatal("the screen has no wait command, want one for its attempts")
	}
	next, _ := m.Update(cmd())
	return asModel(t, next)
}

// press runs one key press.
func press(t *testing.T, m Model, key tea.KeyPressMsg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(key)
	return asModel(t, next), cmd
}

// TestTheWaitingScreenNamesTheSession pins the title: the label the session is
// reconnecting to, with the single ellipsis character after it.
func TestTheWaitingScreenNamesTheSession(t *testing.T) {
	got := sized(t, "local", "", attemptsOf(), 80, 24).View().Content

	if !strings.Contains(got, "Reconnecting to local\u2026") {
		t.Fatalf("the screen does not name the session it is reconnecting to:\n%s", got)
	}
	if strings.Contains(got, "restarting") {
		t.Errorf("the screen gives a reason though none arrived:\n%s", got)
	}
	if n := strings.Count(got, "\u2026"); n != 1 {
		t.Errorf("the screen draws %d ellipses, want one:\n%s", n, got)
	}
}

// TestTheWaitingScreenSaysWhyARestartHappens pins the title when the restart
// gave a reason: it replaces the reconnect line rather than joining it.
func TestTheWaitingScreenSaysWhyARestartHappens(t *testing.T) {
	m := sized(t, "local", "the daemon is restarting", attemptsOf(), 80, 24)
	got := m.View().Content

	if !strings.Contains(got, "local is restarting: the daemon is restarting") {
		t.Fatalf("the screen does not say why the restart happens:\n%s", got)
	}
	if strings.Contains(got, "Reconnecting to") {
		t.Errorf("the screen shows both the restart line and the reconnect line:\n%s", got)
	}
}

// TestTheTitleStaysInsideTheFrame checks the two things a reason can do to the
// title: carry a newline, or be wider than the terminal. The first must not
// push an uncentred row into the block, the second must not run off the frame.
func TestTheTitleStaysInsideTheFrame(t *testing.T) {
	// textRows is the render's rows with visible text, without the blank ones
	// the centring adds above the block.
	textRows := func(render string) []string {
		var rows []string
		for _, row := range strings.Split(render, "\n") {
			if strings.TrimSpace(ansi.Strip(row)) != "" {
				rows = append(rows, ansi.Strip(row))
			}
		}
		return rows
	}

	t.Run("a reason with a newline keeps one title row", func(t *testing.T) {
		const reason = "the daemon is restarting\nand back in a moment"
		got := sized(t, "local", reason, attemptsOf(), 80, 24).View().Content

		if !strings.Contains(got, "local is restarting: the daemon is restarting") {
			t.Errorf("the title does not name the reason's first line:\n%s", got)
		}
		if rows := textRows(got); len(rows) != 2 {
			t.Errorf("the screen draws %d text rows, want the title and the detail line:\n%s", len(rows), got)
		}
	})

	t.Run("a reason wider than the frame wraps", func(t *testing.T) {
		reason := strings.Repeat("the daemon is restarting ", 5) // 125 cells
		got := sized(t, "local", reason, attemptsOf(), 80, 24).View().Content

		for i, row := range strings.Split(got, "\n") {
			if n := ansi.StringWidth(row); n > 80 {
				t.Errorf("row %d is %d cells, wider than the terminal's 80: %q", i+1, n, ansi.Strip(row))
			}
		}
		if rows := textRows(got); len(rows) < 3 {
			t.Errorf("the long reason did not wrap: %d text rows:\n%s", len(rows), got)
		}
	})
}

// TestEachAttemptRepaints checks that an attempt's number and error reach the
// screen, and that the next attempt replaces them.
func TestEachAttemptRepaints(t *testing.T) {
	const refused = "dial tcp 127.0.0.1:7700: connect: connection refused"
	ch := attemptsOf(
		Attempt{N: 1, Err: refused},
		Attempt{N: 2, Err: "i/o timeout"},
	)
	m := sized(t, "local", "", ch, 80, 24)

	m = readAttempt(t, m)
	got := m.View().Content
	if !strings.Contains(got, "attempt 1") {
		t.Errorf("the first attempt's number is missing:\n%s", got)
	}
	if !strings.Contains(got, refused) {
		t.Errorf("the first attempt's error is missing:\n%s", got)
	}
	if !strings.Contains(got, "30s left") {
		t.Errorf("the seconds left are missing:\n%s", got)
	}

	m = readAttempt(t, m)
	got = m.View().Content
	if !strings.Contains(got, "attempt 2") {
		t.Errorf("the second attempt's number is missing:\n%s", got)
	}
	if !strings.Contains(got, "i/o timeout") {
		t.Errorf("the second attempt's error is missing:\n%s", got)
	}
	if strings.Contains(got, "attempt 1") || strings.Contains(got, refused) {
		t.Errorf("the screen still shows the first attempt:\n%s", got)
	}
}

// TestSecondsLeftRoundUpAndStopAtZero pins the countdown: it rounds up, and a
// past deadline reads 0 rather than ending the wait — launch ends the wait.
func TestSecondsLeftRoundUpAndStopAtZero(t *testing.T) {
	ch := attemptsOf()
	for _, tc := range []struct {
		name string
		now  time.Time
		want string
	}{
		{name: "whole seconds", now: screenNow.Add(10 * time.Second), want: "20s left"},
		{name: "a part second rounds up", now: screenNow.Add(10*time.Second + time.Millisecond), want: "20s left"},
		{name: "the deadline is zero", now: screenDeadline, want: "0s left"},
		{name: "a passed deadline stays zero", now: screenDeadline.Add(time.Minute), want: "0s left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := tc.now
			m := New("local", "", screenDeadline, ch, WithNow(func() time.Time { return now }))
			next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			if got := asModel(t, next).View().Content; !strings.Contains(got, tc.want) {
				t.Fatalf("the screen does not show %q:\n%s", tc.want, got)
			}
		})
	}
}

// TestTheScreenEndsWhenTheAttemptsDo checks that closing the attempts channel
// quits, and that the end is launch's rather than the user's.
func TestTheScreenEndsWhenTheAttemptsDo(t *testing.T) {
	ch := attemptsOf(Attempt{N: 1, Err: "i/o timeout"})
	m := sized(t, "local", "", ch, 80, 24)
	m = readAttempt(t, m)

	close(ch)
	cmd := m.waitForAttempt()
	next, quit := m.Update(cmd())
	got := asModel(t, next)

	if got.GaveUp() {
		t.Error("GaveUp is true after the attempts channel closed, want false")
	}
	if quit == nil {
		t.Fatal("the screen did not quit when its attempts ended")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("the screen's command is %T, want tea.QuitMsg", quit())
	}
}

// TestEscGivesUp checks the three keys that end the wait early, and that each
// of them is the user giving up rather than the channel closing.
func TestEscGivesUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "esc", key: tea.KeyPressMsg{Code: tea.KeyEsc}},
		{name: "q", key: tea.KeyPressMsg{Code: 'q', Text: "q"}},
		{name: "ctrl+c", key: tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sized(t, "local", "", attemptsOf(), 80, 24)
			got, cmd := press(t, m, tc.key)
			if !got.GaveUp() {
				t.Fatalf("%s did not give up", tc.name)
			}
			if cmd == nil {
				t.Fatalf("%s did not end the program", tc.name)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("%s's command is %T, want tea.QuitMsg", tc.name, cmd())
			}
		})
	}
}

// reconnectGolden is one golden frame: a name and whether it renders in colour.
type reconnectGolden struct {
	name     string
	coloured bool
}

// reconnectGoldens is the pair the plan asks for: 80×24 with the 256-colour
// theme and 80×24 under NO_COLOR.
func reconnectGoldens() []reconnectGolden {
	return []reconnectGolden{
		{name: "reconnect-80x24", coloured: true},
		{name: "reconnect-80x24-nocolor"},
	}
}

// renderGolden builds the golden scene at 80×24 and returns its render. It is
// built twice, so a golden cannot quietly absorb nondeterminism.
func renderGolden(t *testing.T, g reconnectGolden) string {
	t.Helper()
	if g.coloured {
		t.Setenv("NO_COLOR", "")
	} else {
		t.Setenv("NO_COLOR", "1")
	}
	t.Setenv("TERM", "xterm-256color")
	th := theme.LoadWithConfig("", "", theme.DepthFlat, nil)

	scene := func() string {
		ch := attemptsOf(Attempt{N: 3, Err: "dial tcp 127.0.0.1:7700: connect: connection refused"})
		m := New("local", "", screenDeadline, ch, fixedClock(), WithTheme(th))
		next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = readAttempt(t, asModel(t, next))
		return m.View().Content
	}
	first := scene()
	if second := scene(); second != first {
		t.Fatalf("scene %s is not deterministic:\nfirst:\n%s\nsecond:\n%s", g.name, first, second)
	}
	return first
}

// checkGolden compares got with testdata/<name>.golden, or rewrites the file
// under -update. The goldens hold text only: the escape sequences are stripped,
// because NO_COLOR keeps attributes that the colour frame checks separately.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	text := ansi.Strip(got)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to write the goldens)", path, err)
	}
	if string(want) == text {
		return
	}
	a := strings.Split(string(want), "\n")
	b := strings.Split(text, "\n")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			t.Fatalf("%s: line %d differs:\nwant: %q\n got: %q\n\nfull render:\n%s", path, i+1, a[i], b[i], text)
		}
	}
	t.Fatalf("%s: %d lines, want %d\n\nfull render:\n%s", path, len(b), len(a), text)
}

// TestTheWaitingScreenMatchesItsGoldens pins the 80×24 frames, in colour and
// under NO_COLOR, and checks that neither overflows the terminal.
func TestTheWaitingScreenMatchesItsGoldens(t *testing.T) {
	for _, g := range reconnectGoldens() {
		t.Run(g.name, func(t *testing.T) {
			got := renderGolden(t, g)
			rows := strings.Split(got, "\n")
			for i, line := range rows {
				if n := ansi.StringWidth(line); n > 80 {
					t.Errorf("line %d is %d cells, wider than the terminal's 80: %q", i+1, n, ansi.Strip(line))
				}
			}
			if len(rows) > 24 {
				t.Errorf("render is %d rows, taller than the terminal's 24", len(rows))
			}
			if !g.coloured && strings.Contains(got, "\x1b") {
				t.Errorf("the NO_COLOR frame carries escape sequences:\n%q", got)
			}
			if g.coloured && !strings.Contains(got, "\x1b") {
				t.Errorf("the colour frame carries no escape sequence, so the theme stopped colouring it:\n%q", got)
			}
			checkGolden(t, g.name, got)
		})
	}
}
