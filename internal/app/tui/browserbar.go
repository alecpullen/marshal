package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
)

func (m Model) renderBrowserBar() string {
	bi := m.state.BrowserInfo()
	if !bi.SessionOpen {
		return ""
	}

	available := max(m.width-4, 1)
	var b strings.Builder
	b.WriteString(browserGlyphStyle().Render("🌐"))
	b.WriteString(" ")
	b.WriteString(urlStyle().Render(truncateURL(bi.URL, available)))

	if bi.Title != "" {
		b.WriteString(dimSep(bi.Title))
	}
	b.WriteString(dimSep(bi.Mode))

	if bi.Active {
		spinner := m.activeSpinnerFrame(session.ActivityTool)
		b.WriteString(dimSep(spinnerLabel(spinner, bi.ToolName)))
	}

	line := b.String()
	return browserBarStyle().
		Width(max(m.width, 1)).
		MaxWidth(max(m.width, 1)).
		Render(ansi.Cut(" "+line+" ", 0, m.width))
}

func dimSep(text string) string {
	return mutedStyle().Render(" · ") + text
}

func truncateURL(raw string, maxWidth int) string {
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if maxWidth <= 0 || ansi.StringWidth(raw) <= maxWidth {
		return raw
	}
	if maxWidth < 8 {
		return ansi.Cut(raw, 0, maxWidth)
	}
	hostEnd := strings.Index(raw, "/")
	if hostEnd < 0 {
		return ansi.Cut(raw, 0, maxWidth-1) + "…"
	}
	lastSlash := strings.LastIndex(raw, "/")
	if lastSlash <= hostEnd {
		return ansi.Cut(raw, 0, maxWidth-1) + "…"
	}
	host := raw[:hostEnd]
	suffix := raw[lastSlash:]
	if ansi.StringWidth(host)+2+ansi.StringWidth(suffix) > maxWidth {
		return ansi.Cut(raw, 0, maxWidth-1) + "…"
	}
	return host + "/…" + suffix
}
