package session

import (
	"strings"

	"marshal/internal/tools/registry"
)

// estimateTokensForAuditEvent returns a rough size hint for the ledger
// (one line per tool, sized so a stream of large tool calls is
// observable without storing the result content).
func estimateTokensForAuditEvent(ev registry.AuditEvent) int {
	if ev.ResultContent != "" {
		// 4 chars / token heuristic consistent with the rest of the agent.
		return (len(ev.ResultContent) + 3) / 4
	}
	if ev.ResultSummary != "" {
		return (len(ev.ResultSummary) + 3) / 4
	}
	return 0
}

// ledgerFallbackSummary is the summary used by the ledger when a tool
// has not provided one. Kept short so a busy turn's ledger stays one
// line per entry. The ResultSummary winner is selected by the session
// ledger-summary builder; this is the last-resort fallback.
const ledgerFallbackSummary = "(no summary)"

// ledgerSummaryString sanitises a string for use in a ledger line. The
// ledger is rendered into a system message that is read by the model;
// newlines inside a single entry would break the one-line-per-entry
// rule and could confuse downstream parsers. We replace them with
// spaces and trim trailing whitespace.
func ledgerSummaryString(s string) string {
	if s == "" {
		return ledgerFallbackSummary
	}
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}
