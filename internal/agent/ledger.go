package agent

import (
	"fmt"
	"strings"

	"marshal/internal/db"
)

// ledgerLineCap is the maximum length of a single ledger line. A long
// turn can have many entries; cap keeps one line of the message small
// enough that 5-10 turns' ledgers still fit comfortably in the history
// budget. The format trims from the tail with an explicit ", ..." marker
// so the model can see when it lost entries.
const ledgerLineCap = 380

// LedgerLine builds the compact tool-activity line for one turn's audit.
// Format: "tools: a, b, c". Each entry is "<tool> <summary>" plus an
// optional " (err)" tag for failed calls. The trailing cap trims the
// last entry, replacing it with ", ..." to mark the cut.
//
// Empty input is the legitimate "no tool calls" case; LedgerLine
// returns the empty string (caller falls back to the legacy
// placeholder).
//
// The intent is the same shape as the pre-existing "(N tool calls were
// executed)" note emitted by buildHistoryMessages, but with actual
// operational context. A long file.read produced "(412t)" hints a big
// result, even after compaction has dropped the result body. A failed
// shell.run produced "(err)" so the model knows that turn surfaced an
// error.
func LedgerLine(entries []db.ToolAuditEntry) string {
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		line := formatLedgerEntry(e)
		if line == "" {
			continue
		}
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		return ""
	}
	body := "tools: " + strings.Join(parts, ", ")
	if len(body) <= ledgerLineCap {
		return body
	}
	// Trim until the line fits. Drop trailing entries first so the most
	// recent tool calls — which the model is likeliest to need to
	// reference — survive.
	prefix := "tools: "
	cutMarker := ", ..."
	for len(parts) > 1 && len(prefix+strings.Join(parts, ", ")+cutMarker) > ledgerLineCap {
		parts = parts[:len(parts)-1]
	}
	return prefix + strings.Join(parts, ", ") + cutMarker
}

// formatLedgerEntry produces one "<tool> <summary>" entry with a
// trailing "(err)" marker for failed calls. Whitespace inside the
// summary is normalised so two consecutive spaces never appear in the
// line.
func formatLedgerEntry(e db.ToolAuditEntry) string {
	tool := strings.TrimSpace(e.Tool)
	summary := collapseWhitespace(e.Summary)
	if tool == "" {
		return ""
	}
	out := tool
	if summary != "" && summary != tool {
		out = tool + " " + summary
	}
	if !e.Ok {
		out += " (err)"
	}
	return out
}

// collapseWhitespace collapses runs of whitespace to single spaces and
// trims. The ledger is rendered in a single system message; newlines
// inside an entry would break the one-line-per-entry rule.
func collapseWhitespace(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// LedgerSystemMessage returns a one-line system message that packs the
// turn's tool calls for cross-turn replay, or nil when no entries
// exist (caller falls back to the legacy "(N tool calls were executed)"
// placeholder).
func LedgerSystemMessage(entries []db.ToolAuditEntry) (rolePrefix string, content string, ok bool) {
	line := LedgerLine(entries)
	if line == "" {
		return "", "", false
	}
	return "system", fmt.Sprintf("Previous turn tool activity — %s", line), true
}
