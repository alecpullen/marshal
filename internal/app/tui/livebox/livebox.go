// Package livebox plans and renders the tray: the plan-run row, the running
// and held rows, and the plan, as one unframed stack above the input.
//
// It is pure. Plan reads an Input value and returns a Layout; Render paints a
// Layout. The TUI composes the result into the frame, so every row's tint and
// hit target come from the same plan as the drawing.
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

const (
	// BarCells is a running row's measured bar.
	BarCells = 12
	// PlanWindow is the longest list the plan shows item by item.
	PlanWindow = 6
	// CardRows caps an expanded card, header included.
	CardRows = 5
	// RunningCap is how many running rows survive shed step 3.
	RunningCap = 3
)

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

// PlanItem is one todo.
type PlanItem struct {
	ID, Content, Status string // Status: "completed", "in_progress" or "pending"
	StartedAt           time.Time
	First               viewmodel.NodeID // the first beat bound to it; zero when none
}

// TargetKind is what a tray row acts on when clicked.
type TargetKind uint8

const (
	TargetNone TargetKind = iota
	TargetWork            // a running or held row, or a line of its card; Key is the TaskKey
	TargetMore            // the "+N more · /work" row
	TargetPlan            // a plan item; Key is its ID, or "#<position>" (1-based) when it has none
)

// Target is a tray row's click target: what it opens, and which node or item.
type Target struct {
	Kind TargetKind
	Node viewmodel.NodeID
	Key  string
}

// RunRow is the plan-run row: an active SDD run or swarm, as one row at the
// top of the tray. It is information, not a browse stop.
type RunRow struct {
	Title     string    // the SDD plan's name, or "swarm"
	Summary   string    // the run summary, composed by the TUI
	StartedAt time.Time // the turn's start: the row's clock
}

// AgentMeta is a subagent's route provenance: the model and provider actually
// running it, and whether the route fell back to reach it. The tray shows it on
// the agent's card so a running child is never anonymous, and the same words
// land on its held row once it finishes.
type AgentMeta struct {
	Model    string
	Provider string
	Fallback bool
}

// HeldItem is a finished item that keeps its tray row until the next prompt.
type HeldItem struct {
	Item     viewmodel.WorkItem // the finished item; its TaskKey and StartedAt keep its slot
	Outcome  string             // "done", "exit 1", "stopped", "lost", "failed" or "outcome unknown"
	Failed   bool
	Summary  string        // the completion event's summary line; "" when none
	Duration time.Duration // EndedAt − StartedAt
}

// Input is everything Plan needs to plan a tray: the frame's budget, the
// clock, the plan-run row, the running and held work, and the plan.
type Input struct {
	Width     int // the column the tray spans
	MaxRows   int // the most rows the tray may take
	Now       time.Time
	Spinner   string               // the turn spinner frame; "" draws glyph.Running
	Run       *RunRow              // the plan-run row; nil when no run is active
	Work      []viewmodel.WorkItem // running items only (viewmodel.WorkActive)
	Held      []HeldItem           // finished items held until the next prompt
	Previous  map[string]*progress.Snapshot
	Headlines map[string]string    // agent TaskKey → the child's newest narration headline
	Agents    map[string]AgentMeta // agent TaskKey → the model and provider running it
	Expanded  string               // TaskKey of the one open card
	Plan      []PlanItem
	Stale     time.Duration // > 0 when the plan is stale
	Cursor    Target        // the browse cursor when it is in the tray
}

// RowStop is a second click target drawn on the same row as Row.Target: the
// plan puts two entries on one line, and each keeps its own target so both
// are clickable and both are browse stops.
type RowStop struct {
	Target Target
	Col    int // the entry's 0-based cell offset inside the row's text
}

// Row is one line of the tray.
type Row struct {
	Text   string // styled foreground only
	Tint   bool   // running work: painted with the surface colour
	Cursor bool   // the browse cursor's row: painted with the selection colour
	Target Target
	Also   []RowStop // further targets on this line, in draw order
}

// Layout is a planned tray: its rows, how many shed steps it took to fit, and
// the browse stops in row order.
type Layout struct {
	Rows  []Row
	Shed  int      // shed steps applied, 0–6
	Stops []Target // browse stops in row order: running and held rows, the more row, plan items
}
