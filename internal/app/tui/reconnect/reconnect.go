// Package reconnect renders the waiting screen a restart leaves behind: the
// session being reconnected to, the attempt count, the last error's first line
// and the seconds left before launch gives up (decision 1).
//
// It is its own package so launch can run it through the program runner like
// every other screen and read GaveUp off the final model. Under the TUI's
// boundary rule it renders only: the label, the reason when there is one, the
// deadline and a channel of attempts, all of which launch gives it. It ends
// the wait only when that channel closes or the user gives up.
package reconnect

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

const (
	// defaultWidth and defaultHeight are the frame the screen centres in
	// before the program reports a size.
	defaultWidth  = 80
	defaultHeight = 24

	// ellipsis is the single character that trails "Reconnecting to <label>".
	ellipsis = "\u2026"

	// detailSeparator joins the detail line's items: the attempt count, the
	// last error's first line and the seconds left.
	detailSeparator = " \u00b7 "
)

// Attempt is one dial that failed.
type Attempt struct {
	N   int
	Err string
}

// Model is the waiting screen (decision 1).
type Model struct {
	label    string
	reason   string
	deadline time.Time
	attempts <-chan Attempt

	// now is the clock the seconds left are read on; tests pin it so no test
	// waits a real second.
	now func() time.Time
	th  theme.Theme

	// last is the most recent attempt, and seen reports whether one arrived:
	// before the first there is no attempt count to show.
	last Attempt
	seen bool

	// gaveUp reports that the user ended the wait; quit is the program's own
	// end, whichever reason.
	gaveUp bool

	width, height int
}

// Option configures the screen; WithTheme and WithNow are its two.
type Option func(*Model)

// WithTheme sets the theme the screen paints with.
func WithTheme(th theme.Theme) Option {
	return func(m *Model) {
		if th.FGDefault != nil {
			m.th = th
		}
	}
}

// WithNow sets the clock the seconds left are read on.
func WithNow(now func() time.Time) Option {
	return func(m *Model) {
		if now != nil {
			m.now = now
		}
	}
}

// New waits for attempts until the channel closes or the user gives up.
// reason is the restart's, "" when none arrived.
func New(label, reason string, deadline time.Time, attempts <-chan Attempt, opts ...Option) Model {
	m := &Model{
		label:    label,
		reason:   reason,
		deadline: deadline,
		attempts: attempts,
		now:      time.Now,
		th:       activeTheme(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return *m
}

// activeTheme is the theme the screen paints with when WithTheme is not given:
// the active one when something has loaded one, and otherwise the environment's
// — which is where NO_COLOR is honoured. The decision is theme's; the screen
// only asks for it.
func activeTheme() theme.Theme {
	if th := theme.Current(); th.FGDefault != nil {
		return th
	}
	return theme.Load()
}

// GaveUp reports whether the user ended the wait.
func (m Model) GaveUp() bool { return m.gaveUp }

// attemptMsg is one dial that failed, read off the attempts channel.
type attemptMsg Attempt

// attemptsClosedMsg ends the wait: launch is done trying.
type attemptsClosedMsg struct{}

// tickMsg repaints the seconds left.
type tickMsg struct{}

// Init reads the first attempt and starts the one-second repaint.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.waitForAttempt(), tickCmd())
}

// waitForAttempt waits on the attempts channel for the next dial. The closed
// channel is the end of the wait, and it is the only thing a successful read
// does not turn into an attempt.
func (m Model) waitForAttempt() tea.Cmd {
	ch := m.attempts
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		attempt, ok := <-ch
		if !ok {
			return attemptsClosedMsg{}
		}
		return attemptMsg(attempt)
	}
}

// tickCmd is the one-second repaint the seconds left need.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles the terminal size, the attempts, the tick and the keys.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case attemptMsg:
		m.last = Attempt(msg)
		m.seen = true
		return m, m.waitForAttempt()
	case attemptsClosedMsg:
		// Launch has stopped trying. The user did not give up, so GaveUp
		// stays false and the loop can go on to its next step.
		return m, tea.Quit
	case tickMsg:
		return m, tickCmd()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey quits on esc, q and ctrl+c. The three all mean the user gave up.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.gaveUp = true
		return m, tea.Quit
	}
	return m, nil
}

// View draws the screen. It takes the alternate screen, as the other
// standalone screens do: the screen owns the terminal while the wait lasts.
func (m Model) View() tea.View {
	v := tea.NewView(m.screen())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeNone
	return v
}

// screen centres the title and, under it, the detail line inside the frame.
func (m Model) screen() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = defaultWidth
	}
	if h <= 0 {
		h = defaultHeight
	}

	title := m.title()
	detail := m.detail()

	// The detail line wraps: an error's first line is as long as the dial
	// made it, and the frame is narrow.
	details := strings.Split(ansi.Hardwrap(detail, w, false), "\n")

	top := (h - (1 + len(details))) / 2
	if top < 0 {
		top = 0
	}

	titleStyle := lipgloss.NewStyle().Foreground(m.th.AccentPrimary)
	detailStyle := lipgloss.NewStyle().Foreground(m.th.FGMuted)

	lines := make([]string, 0, top+1+len(details))
	for i := 0; i < top; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, centre(title, w, titleStyle))
	for _, line := range details {
		lines = append(lines, centre(line, w, detailStyle))
	}
	return strings.Join(lines, "\n")
}

// centre indents text so it sits in the middle of width cells.
func centre(text string, width int, style lipgloss.Style) string {
	indent := (width - ansi.StringWidth(text)) / 2
	if indent < 0 {
		indent = 0
	}
	return strings.Repeat(" ", indent) + style.Render(text)
}

// title is the screen's first line: the reason when the restart gave one, and
// the reconnect otherwise.
func (m Model) title() string {
	if m.reason != "" {
		return m.label + " is restarting: " + m.reason
	}
	return "Reconnecting to " + m.label + ellipsis
}

// detail is the line under the title: the attempt count, the last error's
// first line and the seconds left.
func (m Model) detail() string {
	parts := make([]string, 0, 3)
	if m.seen {
		parts = append(parts, fmt.Sprintf("attempt %d", m.last.N))
	}
	if err := firstLine(m.last.Err); err != "" {
		parts = append(parts, err)
	}
	parts = append(parts, fmt.Sprintf("%ds left", m.secondsLeft()))
	return strings.Join(parts, detailSeparator)
}

// secondsLeft is the time to the deadline in whole seconds, rounded up. A past
// deadline reads 0: the screen keeps waiting, because launch ends the wait.
func (m Model) secondsLeft() int {
	left := m.deadline.Sub(m.now())
	if left <= 0 {
		return 0
	}
	return int((left + time.Second - 1) / time.Second)
}

// firstLine is the first line of s, so a multi-line dial error keeps the
// detail line one line tall.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], "\r")
	}
	return s
}
