package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
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
	n := acpwire.Notice{
		Category: acpwire.NoticeInternal,
		Severity: acpwire.NoticeError,
		Message:  firstLine(err.Error()),
		Source:   source,
	}
	// A multi-line error keeps the rest for the banner's details.
	if full := strings.TrimSpace(err.Error()); full != n.Message {
		n.Detail = full
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

// noticeKey identifies one notice for the expand toggle: the same notice set
// again keeps its state, and any other arrives collapsed.
func noticeKey(n acpwire.Notice) string {
	return fmt.Sprintf("%d|%s|%s|%s", n.SetAt, n.Source, n.Title, n.Message)
}

// noticeIsExpanded reports whether the user expanded n's banner.
func (m *Model) noticeIsExpanded(n acpwire.Notice) bool {
	return m.noticeExpanded != "" && m.noticeExpanded == noticeKey(n)
}

// toggleNotice expands the current banner, or collapses it if expanded.
func (m *Model) toggleNotice() {
	n := m.sess.Status().Notice
	if n == nil {
		m.noticeExpanded = ""
		return
	}
	if m.noticeIsExpanded(*n) {
		m.noticeExpanded = ""
		return
	}
	m.noticeExpanded = noticeKey(*n)
}

// Banner limits: a collapsed banner shows at most noticeMessageLines of the
// message; an expanded one shows the full message and at most
// noticeDetailLines of the detail, so a huge response body cannot push the
// prompt off the screen.
const (
	noticeMessageLines = 3
	noticeDetailLines  = 16
)

// noticeExpandable reports whether n has more than its collapsed banner
// shows: raw detail, or a message longer than the collapsed clamp.
func noticeExpandable(n acpwire.Notice, width int) bool {
	if strings.TrimSpace(n.Detail) != "" {
		return true
	}
	return len(noticeMessage(n, width)) > noticeMessageLines
}

// noticeMessage is the banner's body lines, wrapped to width: the message
// alone under a title, or "category · message" as the head when the notice
// has no title.
func noticeMessage(n acpwire.Notice, width int) []string {
	text := n.Message
	if n.Title == "" {
		text = n.Category + dimSeparator + firstLine(n.Message)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Wrap at spaces only: a provider message often ends in a link (a billing
	// page), and breaking it at a "/" would leave the terminal unable to open
	// it. A word longer than the line is still hard-broken.
	return strings.Split(ansi.Wrap(text, contentWidth(width), ""), "\n")
}

// renderNotice renders the current session notice as a gutter-framed
// banner. A titled notice (a summarised provider failure) leads with its
// title — "ollama-cloud · billing" — and puts the provider's sentence under
// it; an untitled one leads with "category · message". The message is
// clamped to noticeMessageLines unless expanded. A muted last line carries
// the details toggle when there is more to show, the hint, and the dismiss
// keyhint. Glyph and label carry the meaning; colour is decoration (NO_COLOR
// safe).
func renderNotice(n acpwire.Notice, width int, expanded bool) string {
	cw := contentWidth(width)
	g := glyph.Error
	gutterColor := errorColor
	headStyle := errorStyle()
	if n.Severity == acpwire.NoticeWarn {
		g = glyph.Warning
		gutterColor = theme.Current().StatusWarning
		headStyle = warningStyle()
	}
	var b strings.Builder
	line := func(style lipgloss.Style, text string) {
		b.WriteString(continuation())
		b.WriteString(style.Render(text))
		b.WriteString("\n")
	}

	body := noticeMessage(n, width)
	b.WriteString(gutterPrefix(g, gutterColor))
	if n.Title != "" {
		b.WriteString(headStyle.Render(ansi.Truncate(n.Title, cw, "…")))
		b.WriteString("\n")
	} else {
		// The untitled head is the first message line.
		if len(body) == 0 {
			body = []string{n.Category}
		}
		b.WriteString(headStyle.Render(body[0]))
		b.WriteString("\n")
		body = body[1:]
	}
	limit := noticeMessageLines
	if n.Title == "" {
		limit-- // the head line already spent one
	}
	if !expanded && len(body) > limit {
		body = body[:limit]
		body[limit-1] = ansi.Truncate(body[limit-1], max(cw-1, 1), "") + "…"
	}
	for _, l := range body {
		if n.Title != "" {
			line(lipgloss.NewStyle(), l)
		} else {
			line(mutedStyle(), l)
		}
	}

	if expanded {
		if detail := strings.TrimSpace(n.Detail); detail != "" {
			var lines []string
			for _, raw := range strings.Split(detail, "\n") {
				lines = append(lines, strings.Split(ansi.Wrap(raw, max(cw-2, 1), WrapBreakpoints), "\n")...)
			}
			if len(lines) > noticeDetailLines {
				more := len(lines) - noticeDetailLines
				lines = append(lines[:noticeDetailLines], fmt.Sprintf("… %d more lines", more))
			}
			for _, l := range lines {
				line(mutedStyle(), glyph.Rail+" "+l)
			}
		}
	}

	var hint []string
	if noticeExpandable(n, width) {
		if expanded {
			hint = append(hint, glyph.DisclosureExpanded+" hide details")
		} else {
			hint = append(hint, glyph.DisclosureCollapsed+" details (click)")
		}
	}
	if h := strings.TrimSuffix(strings.TrimSpace(n.Hint), "."); h != "" {
		hint = append(hint, h)
	}
	hint = append(hint, "esc to dismiss")
	for _, l := range strings.Split(ansi.Wrap(strings.Join(hint, " · "), cw, WrapBreakpoints), "\n") {
		line(mutedStyle(), l)
	}
	return b.String()
}
