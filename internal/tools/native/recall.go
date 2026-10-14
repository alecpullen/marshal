package native

import (
	"fmt"
	"strings"

	"marshal/internal/db"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

const (
	// recallMaxChars caps the whole recursive-search result. The search
	// exists to rescue a session that is already under context pressure, so
	// an unbounded result would defeat its own purpose.
	recallMaxChars = 8000
	// recallMaxPerHit caps a single archived turn's excerpt so one giant
	// tool output cannot crowd out every other hit.
	recallMaxPerHit = 1200
	// recallDefaultLimit is the default number of hits.
	recallDefaultLimit = 5
	// recallLimitCeiling is the hard maximum for a caller-supplied limit.
	recallLimitCeiling = 20
)

// searchArchivedTurns searches this project's archived conversation turns
// (across earlier generations and sessions) and returns formatted excerpts.
//
// It is the search half of the retired recall_history tool, which
// transcript_read absorbed in the context-diet Tier 2a cut; keeping it as a
// plain function means the query path has one implementation and one set of
// truncation bounds.
func searchArchivedTurns(database *db.DB, query string, limit int) (registry.ToolResult, error) {
	if limit <= 0 {
		limit = recallDefaultLimit
	}
	if limit > recallLimitCeiling {
		limit = recallLimitCeiling
	}
	if strings.TrimSpace(query) == "" {
		return registry.ToolResult{}, fmt.Errorf("query is required")
	}
	hits, err := database.SearchArchivedTurns("", query, "", limit)
	if err != nil {
		return registry.ToolResult{}, fmt.Errorf("search failed: %w", err)
	}
	if len(hits) == 0 {
		return registry.ToolResult{Content: "No matching archived turns found."}, nil
	}
	var b strings.Builder
	for i, hit := range hits {
		if b.Len() >= recallMaxChars {
			fmt.Fprintf(&b, "\n---\n[%d further match(es) not shown — narrow the query]", len(hits)-i)
			break
		}
		if i > 0 {
			b.WriteString("\n---\n")
		}
		// strutil.Truncate marks with "…"; these excerpts need an explicit
		// word so the model knows the excerpt is partial and can widen its
		// query rather than trusting a fragment.
		content := hit.Turn.Content
		if len([]rune(content)) > recallMaxPerHit {
			content = strutil.Truncate(content, recallMaxPerHit, false) + "\n[truncated]"
		}
		fmt.Fprintf(&b, "Session: %s\nGeneration: %s (seq %d)\nRole: %s\nTurn: %d\nContent:\n%s",
			hit.SessionID, hit.GenerationID, hit.GenerationSeq, hit.Turn.Role, hit.Turn.TurnSeq, content)
	}
	return registry.ToolResult{Content: b.String()}, nil
}
