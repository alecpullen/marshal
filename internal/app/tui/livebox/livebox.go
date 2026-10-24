// Package livebox plans and renders the live box: the turn's live beat, the
// running section and the plan section, as one layout of framed rows.
//
// It is pure. Plan reads an Input value and returns a Layout; Render paints a
// Layout. The TUI composes the result into the frame, so every row's frame,
// tint and hit target come from the same plan as the drawing.
//
// It must not import marshal/internal/app/tui: the TUI imports it, and the
// shared vocabulary lives in the glyph and theme sub-packages.
package livebox

import (
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/progress"
	"marshal/internal/viewmodel"
)

// Mode is the plan section's layout.
type Mode string

const (
	Stacked Mode = "stacked"
	Split   Mode = "split"
	Folded  Mode = "folded"
)

const (
	// SplitMinWidth is the narrowest frame that gets the split layout.
	SplitMinWidth = 90
	// RightWidth is the split layout's right pane, inside the outline.
	RightWidth = 34
	// BarCells is a running row's measured bar.
	BarCells = 12
	// PlanWindow is the longest list the plan section shows item by item.
	PlanWindow = 6
	// CardRows caps an expanded card, header included.
	CardRows = 5
	// RunningCap is how many running rows survive shed step 3.
	RunningCap = 3
)

// ParseMode maps a config value to a mode; anything unknown is Stacked.
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case Split:
		return Split
	case Folded:
		return Folded
	default:
		return Stacked
	}
}

// Next is the mode Ctrl+T moves to: stacked → split → folded → stacked,
// skipping split when width < SplitMinWidth.
func Next(m Mode, width int) Mode {
	switch m {
	case Stacked:
		if width >= SplitMinWidth {
			return Split
		}
		return Folded
	case Split:
		return Folded
	default:
		return Stacked
	}
}

// InnerWidth is the content width inside the outline: width − 4.
func InnerWidth(width int) int {
	return max(width-4, 1)
}

// LeftWidth is the live part's width: InnerWidth(width) in stacked and
// folded mode, width − 41 in split mode at width ≥ SplitMinWidth.
func LeftWidth(width int, m Mode) int {
	if m == Split && width >= SplitMinWidth {
		return max(width-41, 1)
	}
	return InnerWidth(width)
}

// Blocks draws done/total as ▰▱ cells, at most maxCells.
func Blocks(done, total, maxCells int) string {
	cells := min(total, maxCells)
	if cells <= 0 {
		return ""
	}
	filled := int(math.Round(float64(done) / float64(total) * float64(cells)))
	filled = min(max(filled, 0), cells)
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Render(strings.Repeat(glyph.ProgressFull, filled)) +
		theme.MutedStyle().Render(strings.Repeat(glyph.ProgressEmpty, cells-filled))
}

// Bar draws a measured bar of cells cells: ━ in c for the filled part, ╸ for
// a half cell, ─ in the muted colour for the track.
func Bar(fraction float64, cells int, c color.Color) string {
	if cells <= 0 {
		return ""
	}
	f := fraction
	if math.IsNaN(f) {
		f = 0
	}
	f = math.Min(math.Max(f, 0), 1)
	// The bar draws half cells, so the fill is quantised to the nearest half:
	// 54% of twelve cells is six and a half cells, not six.
	half := int(math.Round(f * float64(cells) * 2))
	full := half / 2
	hasHalf := half%2 == 1 && full < cells
	track := cells - full
	if hasHalf {
		track--
	}
	filled := strings.Repeat("━", full)
	if hasHalf {
		filled += "╸"
	}
	return lipgloss.NewStyle().Foreground(c).Render(filled) +
		theme.MutedStyle().Render(strings.Repeat("─", track))
}

// Live is the live part, rendered by the TUI at LeftWidth.
type Live struct {
	Full         []string           // one entry per screen line
	Targets      []viewmodel.NodeID // aligned with Full; the zero NodeID for none
	Short        []string           // the beat's first line and its current call
	ShortTargets []viewmodel.NodeID
}

// PlanItem is one todo.
type PlanItem struct {
	ID, Content, Status string // Status: "completed", "in_progress" or "pending"
	StartedAt           time.Time
	First               viewmodel.NodeID // the first beat bound to it; zero when none
}

// TargetKind is what a box row acts on when clicked.
type TargetKind uint8

const (
	TargetNone TargetKind = iota
	TargetLive            // a line of the live part; Node names its row
	TargetWork            // a running row or a line of its card; Key is the TaskKey
	TargetMore            // the "+N more · /work" row
	TargetPlan            // a plan item; Key is its ID, or "#<position>" (1-based) when it has none
)

// Target is a box row's click target: what it opens, and which node or item.
type Target struct {
	Kind TargetKind
	Node viewmodel.NodeID
	Key  string
}

// Input is everything Plan needs to plan a box: the frame's budget, the clock,
// the turn's state, the live part, the running work, and the plan.
type Input struct {
	Width     int // the column the box spans
	MaxRows   int // the most rows the box may take
	Now       time.Time
	Busy      bool
	State     string   // top-border state, already styled; "" when not busy
	Hints     []string // top-border right side, in order of priority
	Spinner   string   // the turn spinner frame; "" draws glyph.Running
	Live      Live
	Work      []viewmodel.WorkItem // live items only (viewmodel.WorkActive)
	Previous  map[string]*progress.Snapshot
	Headlines map[string]string // agent TaskKey → the child's newest narration headline
	Expanded  string            // TaskKey of the one open card
	Plan      []PlanItem
	Stale     time.Duration // > 0 when the plan is stale
	Mode      Mode
	Cursor    Target // the browse cursor when it is in the box
}

// RowFrame is how a row sits in the box's outline.
type RowFrame uint8

const (
	FrameInner  RowFrame = iota // Text sits inside "│ … │"
	FrameBorder                 // Text is a whole border line
	FramePlain                  // Text is a whole row with no outline (idle and compact rows)
)

// RowStop is a second click target drawn on the same row as Row.Target: the
// stacked plan layout puts two entries on one line, and each keeps its own
// target so both are clickable and both are browse stops.
type RowStop struct {
	Target Target
	Col    int // the entry's 0-based cell offset inside the row's text
}

// Row is one line of the box.
type Row struct {
	Text   string // styled foreground only
	Right  string // split mode: the right pane's text for this row
	Frame  RowFrame
	Tint   bool // running work: painted with the surface colour
	Cursor bool // the browse cursor's row: painted with the selection colour
	Target Target
	Also   []RowStop // further targets on this line, in draw order
}

// Layout is a planned box: its rows, the mode finally drawn, how many shed
// steps it took to fit, and the browse stops in row order.
type Layout struct {
	Rows  []Row
	Mode  Mode     // the mode drawn, after shedding
	Shed  int      // shed steps applied, 0–7
	Stops []Target // browse stops in order: running rows, the more row, plan items
}
