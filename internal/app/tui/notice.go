package tui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/llm/provider"
)

// noticeForError classifies an agent-turn error into a session notice.
// Provider-originated failures (typed by the provider package) become
// provider notices with a /connect hint; anything else is internal.
// Classification is by error identity (errors.As), never string matching.
func noticeForError(err error, source string) session.Notice {
	n := session.Notice{
		Severity: session.SeverityError,
		Message:  firstLine(err.Error()),
		Source:   source,
	}
	var httpErr *provider.ProviderError
	var reqErr *provider.RequestError
	if errors.As(err, &httpErr) || errors.As(err, &reqErr) {
		n.Category = session.NoticeProvider
		n.Hint = "Run /connect to review the provider, or /models to pick another model."
	} else {
		n.Category = session.NoticeInternal
	}
	return n
}

// firstLine returns the first line of s, right-trimmed. Multi-line error
// text would break the one-row banner shape and the compact transcript
// row.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t\r")
}

// renderNotice renders the current session notice as a gutter-framed
// banner: severity glyph + category label + message (clamped to two
// lines), then a muted hint line carrying the dismiss keyhint. Glyph and
// label carry the meaning; colour is decoration (NO_COLOR safe).
func renderNotice(n session.Notice, width int) string {
	cw := contentWidth(width)
	g := glyph.Error
	gutterColor := errorColor
	headStyle := errorStyle()
	if n.Severity == session.SeverityWarn {
		g = glyph.Warning
		gutterColor = theme.Current().StatusWarning
		headStyle = warningStyle()
	}
	head := n.Category.String() + dimSeparator + firstLine(n.Message)
	lines := strings.Split(ansi.Wrap(head, cw, WrapBreakpoints), "\n")
	if len(lines) > 2 {
		lines = lines[:2]
	}
	var b strings.Builder
	b.WriteString(gutterPrefix(g, gutterColor))
	b.WriteString(headStyle.Render(lines[0]))
	b.WriteString("\n")
	for _, line := range lines[1:] {
		b.WriteString(continuation())
		b.WriteString(mutedStyle().Render(line))
		b.WriteString("\n")
	}
	hint := n.Hint
	if hint != "" {
		hint += " — "
	}
	hint += "esc to dismiss"
	hintLine := strings.Split(ansi.Wrap(hint, cw, WrapBreakpoints), "\n")[0]
	b.WriteString(continuation())
	b.WriteString(mutedStyle().Render(hintLine))
	b.WriteString("\n")
	return b.String()
}
