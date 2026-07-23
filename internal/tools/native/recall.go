package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
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
		Name:        "recall_history",
		Description: "Search archived conversation turns and return formatted excerpts.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if !recallToolEnabled(t.config) {
				return registry.ToolResult{}, fmt.Errorf("recall_history is disabled by rollover config")
			}
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("recall_history: invalid args: %w", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return registry.ToolResult{}, fmt.Errorf("recall_history: query is required")
			}
			hits, err := t.db.SearchArchivedTurns("", args.Query, "", 0)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("recall_history: search failed: %w", err)
			}
			if len(hits) == 0 {
				return registry.ToolResult{Content: "No matching archived turns found."}, nil
			}
			var b strings.Builder
			for i, hit := range hits {
				if i > 0 {
					b.WriteString("\n---\n")
				}
				fmt.Fprintf(&b, "Session: %s\nGeneration: %s (seq %d)\nRole: %s\nTurn: %d\nContent:\n%s",
					hit.SessionID, hit.GenerationID, hit.GenerationSeq, hit.Turn.Role, hit.Turn.TurnSeq, hit.Turn.Content)
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
