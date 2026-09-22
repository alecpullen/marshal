package postmortem

import (
	"fmt"
	"strings"
)

// Turn-outcome labels as stored in turn_metrics. They are named here because
// Summarize counts two of them by name; the canonical values live with the
// writer in internal/agent/metrics.go.
const (
	outcomeSalvaged = "salvaged"
	outcomeFailed   = "failed"
)

// Summarize renders the one-line headline the /postmortem command shows in the
// transcript: how much harness friction the report recorded, as bare counts.
//
// It reports the deduplicated tool-failure entry count alongside the raw call
// total behind it whenever they differ, because one repeating error and three
// distinct errors are different stories. It never interprets or ranks the
// numbers — that is the agent pass's job, and only when asked for.
func Summarize(report Report) string {
	failureEntries, failureCalls := len(report.ToolFailures), 0
	for _, f := range report.ToolFailures {
		failureCalls += f.Count
	}

	parseTotal := 0
	for _, p := range report.ParseIssues {
		parseTotal += p.Count
	}

	salvaged, failed := 0, 0
	for _, o := range report.TurnOutcomes {
		switch o.Outcome {
		case outcomeSalvaged:
			salvaged += o.Count
		case outcomeFailed:
			failed += o.Count
		}
	}

	parts := make([]string, 0, 4)
	if failureCalls > 0 {
		part := plural(failureCalls, "tool failure")
		if failureEntries > 0 && failureEntries != failureCalls {
			part += fmt.Sprintf(" (%d distinct)", failureEntries)
		}
		parts = append(parts, part)
	}
	if parseTotal > 0 {
		parts = append(parts, plural(parseTotal, "parse issue"))
	}
	if salvaged > 0 {
		parts = append(parts, plural(salvaged, "salvaged turn"))
	}
	if failed > 0 {
		parts = append(parts, plural(failed, "failed turn"))
	}
	if len(parts) == 0 {
		return "Postmortem written: no harness friction recorded"
	}
	return "Postmortem written: " + strings.Join(parts, ", ")
}

// plural renders n with the singular or plural form of noun.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
