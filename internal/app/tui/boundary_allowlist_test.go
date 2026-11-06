package tui

// boundaryAllowlist lists every TUI file that still crosses the seam, by
// import and by config function. Each P2 plan deletes its group's entries, and
// plan 9 empties this map.
//
// An entry is never added to make TestTUIStaysOnTheClientSeam pass. A new use
// belongs behind acpclient.
var boundaryAllowlist = map[string][]string{}
