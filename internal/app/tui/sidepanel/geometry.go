// Package sidepanel renders the widescreen side rail: a read-only,
// prioritized stack of telemetry sections docked to the right of the
// transcript on terminals wider than a configurable threshold.
package sidepanel

import "marshal/internal/app/config"

// Geometry splits frameWidth into a left column and a rail. A rail of 0
// means the rail is not shown and left == frameWidth. floor is the
// minimum width the left column is allowed to have (the TUI's
// minTerminalWidth); the rail yields to it, and is dropped entirely
// rather than rendered below cfg.MinCols.
//
// left+rail always equals frameWidth exactly.
func Geometry(frameWidth, floor int, cfg config.SidePanelConfig) (left, rail int) {
	if !cfg.Enabled || frameWidth < cfg.MinWidth {
		return frameWidth, 0
	}
	rail = frameWidth * cfg.WidthPct / 100
	if rail < cfg.MinCols {
		rail = cfg.MinCols
	}
	if rail > cfg.MaxCols {
		rail = cfg.MaxCols
	}
	// The left column never starves: the four width settings are
	// independently configurable, so an aggressive width_pct must not
	// push the transcript below the floor the rest of the TUI assumes.
	if avail := frameWidth - floor; rail > avail {
		rail = avail
	}
	if rail < cfg.MinCols {
		// Too narrow to be legible — drop the rail rather than render a sliver.
		return frameWidth, 0
	}
	return frameWidth - rail, rail
}
