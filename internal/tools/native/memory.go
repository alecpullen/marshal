package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

// memoryWriteTool returns the memory.write tool wired to the toolSet's db,
// project, and session (AI-15). It lets the agent deliberately persist a
// durable project memory mid-session; batch-2 content-hash dedup in
// SaveMemory collapses repeat saves of the same fact.
func (t *toolSet) memoryWriteTool() registry.Tool {
	return registry.Tool{
		Name:        "memory.write",
		Description: "Save a durable project memory. Use for non-obvious facts, architectural notes, or decisions worth keeping across sessions. Duplicates are deduplicated automatically.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["fact","architecture","decision"]},"content":{"type":"string"}},"required":["kind","content"],"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				Kind    string `json:"kind"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("memory.write: invalid args: %w", err)
			}
			switch args.Kind {
			case "fact", "architecture", "decision":
			default:
				return registry.ToolResult{}, fmt.Errorf("memory.write: kind must be fact, architecture, or decision")
			}
			content := strings.TrimSpace(args.Content)
			if content == "" {
				return registry.ToolResult{}, fmt.Errorf("memory.write: content is required")
			}
			sessionID := ""
			if t.sessionState != nil {
				sessionID = t.sessionState.SessionID()
			}
			if err := t.db.SaveMemory(t.projectID, args.Kind, content, sessionID, time.Now()); err != nil {
				return registry.ToolResult{}, fmt.Errorf("memory.write: %w", err)
			}
			return registry.ToolResult{Content: "Memory saved."}, nil
		},
	}
}
