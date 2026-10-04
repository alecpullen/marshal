package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
)

// swarmStripText renders `swarm 3/5 · implementer — round 1/2`.
func swarmStripText(p session.SwarmProgress) string {
	done, active, detail := 0, "", ""
	for _, r := range p.Roles {
		switch r.Status {
		case session.SwarmRoleDone, session.SwarmRoleFailed:
			done++
		case session.SwarmRoleActive:
			if active == "" {
				active, detail = r.Name, r.Detail
			}
		}
	}
	text := fmt.Sprintf("swarm %d/%d", done, len(p.Roles))
	if active != "" {
		text += " · " + active
		if detail != "" {
			text += " — " + detail
		}
	}
	return text
}

// browserStripText renders `◇ example.com/docs · Title · standalone`
// plus the tool spinner while a browser action is in flight.
func browserStripText(bi session.BrowserInfo, spinner string) string {
	text := browserGlyphStyle().Render(glyph.Web) + " " + urlStyle().Render(truncateURL(bi.URL, 40))
	if bi.Title != "" {
		text += dimSep(bi.Title)
	}
	if bi.Mode != "" {
		text += dimSep(bi.Mode)
	}
	if bi.Active {
		text += dimSep(spinnerLabel(spinner, bi.ToolName))
	}
	return text
}

// dimSep prefixes a dim ` · ` separator, used to join strip fragments.
func dimSep(text string) string {
	return mutedStyle().Render(" · ") + text
}

// truncateURL strips the scheme and shortens long URLs to host/…/last
// segment so the strip and the status segment stay inside their budget.
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
