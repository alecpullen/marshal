package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// noticeForError takes the notice the agent classified a failed run's error
// as (C9): a provider or login failure comes back coded, carrying the banner
// it calls for, so provider and login classification lives agent side and its
// Go types never reach the TUI. source says which path failed: "turn" or
// "plan-author". An error the agent did not classify is an internal notice.
func noticeForError(err error, source string) acpwire.Notice {
	if n, ok := acpclient.NoticeOf(err); ok {
		n.Source = source
		return n
	}
	return acpwire.Notice{
		Category: acpwire.NoticeInternal,
		Severity: acpwire.NoticeError,
		Message:  firstLine(err.Error()),
		Source:   source,
	}
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
func renderNotice(n acpwire.Notice, width int) string {
	cw := contentWidth(width)
	g := glyph.Error
	gutterColor := errorColor
	headStyle := errorStyle()
	if n.Severity == acpwire.NoticeWarn {
		g = glyph.Warning
		gutterColor = theme.Current().StatusWarning
		headStyle = warningStyle()
	}
	head := n.Category + dimSeparator + firstLine(n.Message)
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
