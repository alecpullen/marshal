package tui

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/strutil"
	"marshal/internal/tools/native"
)

// jobLaneMaxRows caps the lane: a header row plus up to three job rows, the
// last of which becomes an overflow row when more are running. Every row
// here is subtracted from the transcript viewport, so the lane must not
// grow without bound when a user spawns a dozen jobs.
const jobLaneMaxRows = 4

// renderJobLane renders running background jobs as a persistent lane above
// the input.
//
// Jobs are deliberately not rendered as bounded live regions in the
// transcript: they outlive the turn that spawned them, so a row anchored
// where they were spawned misrepresents their lifetime. The lane carries no
// tint — position already separates it, and a permanently-tinted band
// anchored above the input would be a constant field of colour rather than
// a signal.
func (m Model) renderJobLane() string {
	running := make([]native.JobInfo, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j.Status == native.StatusRunning {
			running = append(running, j)
		}
	}
	if len(running) == 0 {
		return ""
	}
	width := max(m.leftWidth, 1)

	var b strings.Builder
	b.WriteString(dimStyle().Render(fmt.Sprintf("jobs %d", len(running))))
	b.WriteString("\n")

	rows := jobLaneMaxRows - 1
	shown := running
	overflow := 0
	if len(shown) > rows {
		shown = shown[:rows-1]
		overflow = len(running) - len(shown)
	}
	for _, j := range shown {
		line := fmt.Sprintf("%s  %s  %s",
			j.ID,
			strutil.Truncate(j.Command, max(width/2, 12), true),
			formatElapsed(max(time.Since(j.StartedAt), 0)))
		b.WriteString(dimStyle().Render(glyph.Job + " " + line))
		b.WriteString("\n")
	}
	if overflow > 0 {
		b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more", overflow)))
		b.WriteString("\n")
	}

	return laneSeparator(width) + chromeRailWidth(b.String(), dimColor, max(width-1, 1))
}

// jobLaneRows reports the lane's rendered height for the frame's height
// budget. It must agree with renderJobLane exactly; a mismatch pushes the
// input area or the status footer off the bottom of the screen.
func (m Model) jobLaneRows() int {
	out := m.renderJobLane()
	if out == "" {
		return 0
	}
	return strings.Count(out, "\n")
}
