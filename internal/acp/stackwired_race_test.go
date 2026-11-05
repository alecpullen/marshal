//go:build race

package acp

// raceEnabled reports the race detector, whose timing distortion makes the
// stack budgets meaningless. The budget tests skip when it is on.
const raceEnabled = true
