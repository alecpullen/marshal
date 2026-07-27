package sidepanel

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/strutil"
)

// contextWarnThreshold is the fraction of the context window past which
// usage renders in StatusWarning.
const contextWarnThreshold = 0.80

// ContextSection shows what is actually in the model's context window:
// the fill bar and the per-kind token breakdown. The status line shows
// only the aggregate; this is the composition behind it.
type ContextSection struct{}

func (ContextSection) ID() string      { return "context" }
func (ContextSection) Title() string   { return "CONTEXT" }
func (ContextSection) Priority() int   { return 1 }
func (ContextSection) Clippable() bool { return false }

func (ContextSection) Relevant(d Data) bool { return !d.Pack.IsEmpty() }

// Bar renders a fill bar of the given width. fraction is clamped to [0,1].
func Bar(fraction float64, width int) string {
	if width <= 0 {
		return ""
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction*float64(width) + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func (ContextSection) Render(d Data, width, maxRows int) []string {
	u := d.Pack.TokenUsage
	rows := make([]string, 0, 8)

	frac := 0.0
	if u.MaxTokens > 0 {
		frac = float64(u.EstimatedTokens) / float64(u.MaxTokens)
	}
	barRow := " " + Bar(frac, max(width-8, 4)) + fmt.Sprintf(" %3d%%", int(frac*100+0.5))
	if u.MaxTokens > 0 && frac >= contextWarnThreshold {
		barRow = styleWarning(barRow)
	}
	rows = append(rows, barRow)

	for _, s := range d.Pack.Sections {
		if s.EstimatedTokens == 0 {
			continue
		}
		pct := 0
		if u.EstimatedTokens > 0 {
			pct = int(float64(s.EstimatedTokens)/float64(u.EstimatedTokens)*100 + 0.5)
		}
		label := ansi.Truncate(s.Title, max(width-14, 4), "…")
		rows = append(rows, fmt.Sprintf(" %-*s %6s %3d%%",
			max(width-14, 4), label, strutil.CompactTokens(s.EstimatedTokens), pct))
	}

	if stats := Telemetry(d.Turns, d.Now); len(stats.Series) > 0 {
		rows = append(rows, fmt.Sprintf(" %s  avg %s/turn",
			Sparkline(stats.Series, max(width-18, 4)),
			strutil.CompactTokens(stats.AvgTokens)))

		detail := fmt.Sprintf(" %s/min", strutil.CompactTokens(stats.BurnPerMin))
		if stats.AvgTokens > 0 && u.MaxTokens > u.EstimatedTokens {
			detail += fmt.Sprintf(" · ~%d turns", (u.MaxTokens-u.EstimatedTokens)/stats.AvgTokens)
		}
		if stats.CacheHitPct > 0 {
			detail += fmt.Sprintf(" · %d%% cache", stats.CacheHitPct)
		}
		rows = append(rows, detail)
	}

	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (ContextSection) OneLine(d Data, width int) string {
	u := d.Pack.TokenUsage
	pct := 0
	if u.MaxTokens > 0 {
		pct = int(float64(u.EstimatedTokens)/float64(u.MaxTokens)*100 + 0.5)
	}
	return ansi.Truncate(fmt.Sprintf("ctx %s/%s · %d%%",
		strutil.CompactTokens(u.EstimatedTokens),
		strutil.CompactTokens(u.MaxTokens), pct), width, "…")
}
