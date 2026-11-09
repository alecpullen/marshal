//go:build !race

package tui

// raceEnabled reports the race detector, whose timing distortion makes the
// key-press budget meaningless. The budget test skips when it is on.
const raceEnabled = false
