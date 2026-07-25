package sdd

import (
	"strconv"
	"strings"
)

// DetectLoops scans the last windowSize progress lines for a repeated
// dispatch signature (the same DISPATCH_* line with the same task + kv
// appearing 3+ times without an intervening MERGED). Returns a LOOP
// HealthAlert if detected; nil if healthy.
func DetectLoops(progress *Progress, ws *Workspace, windowSize int) (*HealthAlert, error) {
	lines, err := progress.Tail(ws, windowSize)
	if err != nil {
		return nil, err
	}
	signatures := map[string]int{}
	for _, line := range lines {
		// Only count dispatch events.
		if !strings.Contains(line, " DISPATCH_") {
			if strings.Contains(line, " MERGED ") {
				// A merge resets the loop counter for any in-flight dispatches.
				signatures = map[string]int{}
			}
			continue
		}
		// Signature = the whole line (task + event + kv). Identical dispatches loop.
		signatures[line]++
		if signatures[line] >= 3 {
			return &HealthAlert{Kind: "LOOP", Detail: "dispatch signature repeated 3x without MERGED", Evidence: []string{line}}, nil
		}
	}
	return nil, nil
}

// DetectStagnation scans the last windowSize progress lines; if NONE of them
// are MERGED/INTEGRATED/FINALIZE events, returns a STAGNATION HealthAlert.
func DetectStagnation(progress *Progress, ws *Workspace, windowSize int) (*HealthAlert, error) {
	lines, err := progress.Tail(ws, windowSize)
	if err != nil {
		return nil, err
	}
	if len(lines) < windowSize {
		return nil, nil // not enough history to judge
	}
	for _, line := range lines {
		if strings.Contains(line, " MERGED ") || strings.Contains(line, " INTEGRATED ") || strings.Contains(line, " FINALIZE") {
			return nil, nil
		}
	}
	return &HealthAlert{Kind: "STAGNATION", Detail: "no MERGED/INTEGRATED/FINALIZE in last " + strconv.Itoa(windowSize) + " events", Evidence: lines}, nil
}
