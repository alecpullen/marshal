package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

const (
	// recallMaxChars caps the whole recall result. recall_history exists
	// to rescue a session that is already under context pressure, so an
	// unbounded result would defeat its own purpose.
	recallMaxChars = 8000
	// recallMaxPerHit caps a single archived turn's excerpt so one giant
	// tool output cannot crowd out every other hit.
	recallMaxPerHit = 1200
	// recallDefaultLimit is the default number of hits.
	recallDefaultLimit = 5
)

// recallTool searches archived turns and returns formatted excerpts.
type recallTool struct {
	db     *db.DB
	config config.RolloverConfig
}

// NewRecallTool creates a new recall_history tool.
func NewRecallTool(database *db.DB, cfg config.RolloverConfig) registry.Tool {
	t := &recallTool{db: database, config: cfg}
	return registry.Tool{
		Name: "recall_history",
		Description: "Search this project's archived conversation turns (across " +
			"earlier generations and sessions) and return excerpts. Use after a " +
			"context compaction to recover a specific detail the progress summary " +
			"dropped — an exact command, an error message, a file path, or a past " +
			"decision and its reasoning. Prefer a distinctive phrase as the query; " +
			"results are ranked by relevance and excerpts are truncated.",
		Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Distinctive phrase to search for"},"limit":{"type":"integer","description":"Max archived turns to return (default 5, max 20)"}},"required":["query"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if !recallToolEnabled(t.config) {
				return registry.ToolResult{}, fmt.Errorf("recall_history is disabled by rollover config")
			}
			var args struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("recall_history: invalid args: %w", err)
			}
			limit := args.Limit
			if limit <= 0 {
				limit = recallDefaultLimit
			}
			if limit > 20 {
				limit = 20
			}
			if strings.TrimSpace(args.Query) == "" {
				return registry.ToolResult{}, fmt.Errorf("recall_history: query is required")
			}
			hits, err := t.db.SearchArchivedTurns("", args.Query, "", limit)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("recall_history: search failed: %w", err)
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
				// strutil.Truncate marks with "…"; recall excerpts need an
				// explicit word so the model knows the excerpt is partial
				// and can widen its query rather than trusting a fragment.
				content := hit.Turn.Content
				if len([]rune(content)) > recallMaxPerHit {
					content = strutil.Truncate(content, recallMaxPerHit, false) + "\n[truncated]"
				}
				fmt.Fprintf(&b, "Session: %s\nGeneration: %s (seq %d)\nRole: %s\nTurn: %d\nContent:\n%s",
					hit.SessionID, hit.GenerationID, hit.GenerationSeq, hit.Turn.Role, hit.Turn.TurnSeq, content)
			}
			return registry.ToolResult{Content: b.String()}, nil
		},
	}
}

// recallHistoryTool returns the recall_history tool wired to the toolSet's
// db and config.
func (t *toolSet) recallHistoryTool() registry.Tool {
	return NewRecallTool(t.db, t.config.Session.Rollover)
}

// recallToolEnabled returns true when the recall_history tool should be
// available to the agent.
func recallToolEnabled(config config.RolloverConfig) bool {
	if !config.Enabled {
		return false
	}
	switch config.RecallToolEnabled {
	case "never":
		return false
	case "always":
		return true
	case "auto":
		return config.Policy == "context_percent" || config.Policy == "turn_count"
	}
	return false
}
