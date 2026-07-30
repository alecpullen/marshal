package sidepanel

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/strutil"
)

// SwarmSection is the full role roster. The live strip shows one line for
// the whole run; this is the roster panel the hairline-gutter redesign
// removed, restored where there is room for it.
type SwarmSection struct{}

func (SwarmSection) ID() string      { return "swarm" }
func (SwarmSection) Title() string   { return "SWARM" }
func (SwarmSection) Priority() int   { return 0 }
func (SwarmSection) Clippable() bool { return true }

func (SwarmSection) Relevant(d Data) bool { return d.Swarm.Active }

// roleGlyph maps a role status to its one-rune cue.
func roleGlyph(s session.SwarmRoleStatus) string {
	switch s {
	case session.SwarmRoleDone:
		return glyph.OK
	case session.SwarmRoleFailed:
		return "✘"
	case session.SwarmRoleActive:
		return "◐"
	default:
		return glyph.Ambient
	}
}

// elapsed renders m:ss, or "—" when the role has not started.
func elapsed(start, now time.Time) string {
	if start.IsZero() {
		return "—"
	}
	d := now.Sub(start)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func (SwarmSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, len(d.Swarm.Roles)*2)
	for _, r := range d.Swarm.Roles {
		tok := "—"
		if r.Tokens > 0 {
			tok = strutil.CompactTokens(r.Tokens)
		}
		rows = append(rows, fmt.Sprintf(" %s %-12s %6s %5s",
			roleGlyph(r.Status), r.Name, tok, elapsed(r.StartedAt, d.Now)))
		if r.Status == session.SwarmRoleActive && r.Detail != "" {
			rows = append(rows, "   └ "+r.Detail)
		}
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	return rows
}

func (SwarmSection) OneLine(d Data, width int) string {
	done, active := 0, ""
	for _, r := range d.Swarm.Roles {
		switch r.Status {
		case session.SwarmRoleDone, session.SwarmRoleFailed:
			done++
		case session.SwarmRoleActive:
			if active == "" {
				active = r.Name
			}
		}
	}
	parts := []string{fmt.Sprintf("◐ swarm %d/%d", done, len(d.Swarm.Roles))}
	if active != "" {
		parts = append(parts, active)
	}
	if d.Swarm.TokensUsed > 0 {
		parts = append(parts, strutil.CompactTokens(d.Swarm.TokensUsed))
	}
	return ansi.Truncate(strings.Join(parts, " · "), width, "…")
}
