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

// pctCol is the width of the trailing percentage column, and tokCol the
// token count beside it. Both the fill bar and every composition row end
// flush with pctCol, so the aggregate percentage sits directly above the
// per-kind percentages instead of one cell off.
const (
	pctCol = 4 // "100%"
	tokCol = 5 // "128k"
)

func (ContextSection) Render(d Data, width, maxRows int) []string {
	u := d.Pack.TokenUsage
	rows := make([]string, 0, 8)

	frac := 0.0
	if u.MaxTokens > 0 {
		frac = float64(u.EstimatedTokens) / float64(u.MaxTokens)
	}
	// The bar fills whatever the reserved percentage column leaves, so its
	// trailing "%" lands in the same cell as the rows beneath it.
	barRow := railRow("", Bar(frac, max(railBudget("", pct(frac), width), 4)),
		pct(frac), width)
	if u.MaxTokens > 0 && frac >= contextWarnThreshold {
		barRow = styleWarning(barRow)
	}
	rows = append(rows, barRow)

	for _, s := range d.Pack.Sections {
		if s.EstimatedTokens == 0 {
			continue
		}
		share := 0.0
		if u.EstimatedTokens > 0 {
			share = float64(s.EstimatedTokens) / float64(u.EstimatedTokens)
		}
		right := fmt.Sprintf("%*s %*s", tokCol,
			strutil.CompactTokens(s.EstimatedTokens), pctCol, pct(share))
		rows = append(rows, railRow("", s.Title, right, width))
	}

	if stats := Telemetry(d.Turns, d.Now); len(stats.Series) > 0 {
		avg := fmt.Sprintf("avg %s/turn", strutil.CompactTokens(stats.AvgTokens))
		rows = append(rows, railRow("",
			Sparkline(stats.Series, max(railBudget("", avg, width), 4)), avg, width))

		detail := fmt.Sprintf("%s/min", strutil.CompactTokens(stats.BurnPerMin))
		if stats.AvgTokens > 0 && u.MaxTokens > u.EstimatedTokens {
			detail += fmt.Sprintf(" · ~%d turns", (u.MaxTokens-u.EstimatedTokens)/stats.AvgTokens)
		}
		if stats.CacheHitPct > 0 {
			detail += fmt.Sprintf(" · %d%% cache", stats.CacheHitPct)
		}
		rows = append(rows, railRow("", detail, "", width))
	}

	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

// pct renders a fraction as a right-aligned whole-percent cell.
func pct(fraction float64) string {
	return fmt.Sprintf("%d%%", int(fraction*100+0.5))
}

func (ContextSection) OneLine(d Data, width int) string {
	u := d.Pack.TokenUsage
	frac := 0.0
	if u.MaxTokens > 0 {
		frac = float64(u.EstimatedTokens) / float64(u.MaxTokens)
	}
	return ansi.Truncate(fmt.Sprintf("ctx %s/%s · %s",
		strutil.CompactTokens(u.EstimatedTokens),
		strutil.CompactTokens(u.MaxTokens), pct(frac)), width, "…")
}
