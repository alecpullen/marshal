// internal/tui/styles.go
// All Forge palette colours and shared styles live here.
// No raw lipgloss.Color() calls anywhere else in the tui package.

package tui

import "github.com/charmbracelet/lipgloss"

// ── Palette ───────────────────────────────────────────────────────────────────

var (
	colBg  = lipgloss.Color("#0D1117")
	colBg2 = lipgloss.Color("#161B22")
	colBg3 = lipgloss.Color("#21262D")
	colBr  = lipgloss.Color("#30363D")
	colBr2 = lipgloss.Color("#21262D")
	colBr3 = lipgloss.Color("#484F58")
	colTx  = lipgloss.Color("#E6EDF3")
	colTx2 = lipgloss.Color("#8B949E")
	colTx3 = lipgloss.Color("#484F58")
	colBl  = lipgloss.Color("#388BFD")
	colGr  = lipgloss.Color("#3FB950")
	colRd  = lipgloss.Color("#F85149")
	colAm  = lipgloss.Color("#D29922")
	colPu  = lipgloss.Color("#A78BFA")
)

// ── Layout constants ──────────────────────────────────────────────────────────

const (
	sidebarWidth = 28 // chars, not px
	prefixWidth  = 6  // "exec  " / "critic" / "      "
)

// ── Header styles ─────────────────────────────────────────────────────────────

var (
	styleHeader = lipgloss.NewStyle().
			Background(colBg2).
			Foreground(colTx3)

	styleHeaderTitle = lipgloss.NewStyle().
				Foreground(colBl).
				Background(colBg2).
				Bold(true)

	styleHeaderHint = lipgloss.NewStyle().
			Foreground(colTx3).
			Background(colBg2)

	styleHeaderRule = lipgloss.NewStyle().
			Foreground(colBr3).
			Background(colBg2)
)

// ── Sidebar styles ────────────────────────────────────────────────────────────

var (
	styleSidebar = lipgloss.NewStyle().
			Width(sidebarWidth).
			Background(colBg).
			BorderRight(true).
			BorderStyle(lipgloss.ThickBorder()).
			BorderForeground(colBl)

	styleSidebarItemName = lipgloss.NewStyle().
				Foreground(colTx2).
				Width(sidebarWidth - 2)

	styleSidebarItemNameActive = lipgloss.NewStyle().
					Foreground(colTx).
					Width(sidebarWidth - 4). // -4 for left border
					BorderLeft(true).
					BorderStyle(lipgloss.ThickBorder()).
					BorderForeground(colBl).
					Background(colBg2)

	styleSidebarMeta = lipgloss.NewStyle().
				Foreground(colTx3).
				PaddingLeft(2).
				Width(sidebarWidth - 2)

	styleSidebarRule = lipgloss.NewStyle().
				Foreground(colBr3).
				Width(sidebarWidth)

	styleSidebarDotPass    = lipgloss.NewStyle().Foreground(colGr)
	styleSidebarDotFail    = lipgloss.NewStyle().Foreground(colRd)
	styleSidebarDotRunning = lipgloss.NewStyle().Foreground(colAm)
	styleSidebarDotQueued  = lipgloss.NewStyle().Foreground(colTx3)
)

// ── Task block styles ─────────────────────────────────────────────────────────

var (
	// Task block uses a double-line top border and single bottom.
	// We draw this manually with box-drawing chars for control.
	styleBlockHeader = lipgloss.NewStyle().
				Background(colBg2).
				Foreground(colTx).
				PaddingLeft(1)

	styleBlockBody = lipgloss.NewStyle().
			Background(colBg).
			Foreground(colTx2)

	styleBlockFooterPass = lipgloss.NewStyle().
				Background(colBg2).
				Foreground(colGr)

	styleBlockFooterFail = lipgloss.NewStyle().
				Background(colBg2).
				Foreground(colRd)

	styleBlockFooterRunning = lipgloss.NewStyle().
				Background(colBg2).
				Foreground(colAm)
)

// ── Log line styles ───────────────────────────────────────────────────────────

var (
	stylePrefixExec = lipgloss.NewStyle().
			Foreground(colBl).
			Width(prefixWidth)

	stylePrefixCritic = lipgloss.NewStyle().
				Foreground(colPu).
				Width(prefixWidth)

	stylePrefixBlank = lipgloss.NewStyle().
				Width(prefixWidth)

	styleLogDefault = lipgloss.NewStyle().Foreground(colTx3)
	styleLogSuccess = lipgloss.NewStyle().Foreground(colGr)
	styleLogError   = lipgloss.NewStyle().Foreground(colRd)
	styleLogWarning = lipgloss.NewStyle().Foreground(colAm)

	styleRoundSep = lipgloss.NewStyle().Foreground(colBr3)
	styleRoundLabel = lipgloss.NewStyle().Foreground(colTx3)

	styleThink = lipgloss.NewStyle().
			Foreground(colPu).
			Background(colBg3).
			Italic(true)

	styleQueuedLine = lipgloss.NewStyle().Foreground(colTx3).Italic(true)
	styleLogSystem  = lipgloss.NewStyle().Foreground(colTx3).Italic(true)

	styleCursor = lipgloss.NewStyle().Foreground(colBl)
)

// ── Prompt bar styles ─────────────────────────────────────────────────────────

var (
	stylePromptBar = lipgloss.NewStyle().
			Background(colBg).
			PaddingLeft(1)

	stylePromptPrefix = lipgloss.NewStyle().
				Foreground(colBl).
				SetString("› ")

	stylePromptCmd = lipgloss.NewStyle().
			Foreground(colAm).
			SetString(":")

	stylePromptGhost = lipgloss.NewStyle().Foreground(colBr3)

	stylePromptInput = lipgloss.NewStyle().
				Foreground(colTx)

	stylePromptBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colBr3)

	stylePromptHint = lipgloss.NewStyle().
			Foreground(colTx3)

	stylePromptSep = lipgloss.NewStyle().
			Foreground(colBl).
			Background(colBg)
)

// ── Sidebar title style ───────────────────────────────────────────────────────

var styleSidebarTitle = lipgloss.NewStyle().
	Foreground(colTx3).
	Background(colBg).
	Width(sidebarWidth - 2). // -2 for border
	PaddingLeft(1)

// ── Status bar styles ─────────────────────────────────────────────────────────

var (
	styleStatusbar = lipgloss.NewStyle().
			Background(colBg2).
			Foreground(colTx3).
			PaddingLeft(1).
			PaddingRight(1)

	styleStatusSep     = lipgloss.NewStyle().Foreground(colBr).SetString(" · ")
	styleStatusExec    = lipgloss.NewStyle().Foreground(colBl)
	styleStatusCritic  = lipgloss.NewStyle().Foreground(colPu)
	styleStatusAlert   = lipgloss.NewStyle().Foreground(colAm)
)

// ── Badge styles ──────────────────────────────────────────────────────────────

var (
	styleBadgePass    = lipgloss.NewStyle().Background(colGr).Foreground(colBg).Padding(0, 1).Bold(true)
	styleBadgeFail    = lipgloss.NewStyle().Background(colRd).Foreground(colBg).Padding(0, 1).Bold(true)
	styleBadgeRunning = lipgloss.NewStyle().Background(colAm).Foreground(colBg).Padding(0, 1).Bold(true)
)

// ── Progress bar ──────────────────────────────────────────────────────────────

var (
	styleProgressFill  = lipgloss.NewStyle().Background(colBl)
	styleProgressEmpty = lipgloss.NewStyle().Background(colBg3)
)

func progressBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := int(float64(width) * pct)
	if filled > width {
		filled = width
	}
	empty := width - filled
	return styleProgressFill.Render(repeatStr("█", filled)) +
		styleProgressEmpty.Render(repeatStr("░", empty))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func repeatStr(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
