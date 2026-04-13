// Package loop implements the single-task executor-critic round loop.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alec/marshal/internal/backend"
	"github.com/alec/marshal/internal/tools"
)

// runToolUseRound executes a single round using tool-use instead of edit formats.
// It implements multi-turn tool calling within one executor round.
// Returns (content, promptTokens, completionTokens, error) where content is
// a summary of tool calls made for the critic's benefit.
func (e *Engine) runToolUseRound(ctx context.Context, b backend.Backend, msgs []backend.Message) (content string, promptTokens, completionTokens int, err error) {
	toolRegistry := tools.NewRegistry()
	toolDefs := buildToolDefinitions(toolRegistry)

	// Track conversation for this round
	conversation := make([]backend.Message, len(msgs))
	copy(conversation, msgs)

	var toolResults []string
	maxToolCalls := 10 // Prevent infinite loops

	for callCount := 0; callCount < maxToolCalls; callCount++ {
		// Request completion with tools available
		req := backend.Request{
			Messages: conversation,
			Tools:    toolDefs,
		}

		resp, err := b.Complete(ctx, req)
		if err != nil {
			return "", 0, 0, fmt.Errorf("tool use completion: %w", err)
		}

		promptTokens += resp.Usage.PromptTokens
		completionTokens += resp.Usage.CompletionTokens

		// Stream to sink for UI feedback
		e.sink.Token(resp.Content)

		// Check if there are tool calls
		if len(resp.ToolCalls) == 0 {
			// No more tool calls - we're done
			return buildToolSummary(toolResults), promptTokens, completionTokens, nil
		}

		// Process tool calls
		for _, tc := range resp.ToolCalls {
			tool, ok := toolRegistry.Get(tc.Function.Name)
			if !ok {
				toolResults = append(toolResults, fmt.Sprintf("Tool %s not found", tc.Function.Name))
				continue
			}

			// Execute the tool
			result, err := tool.Handler(e.repo.Root(), json.RawMessage(tc.Function.Arguments))
			var resultJSON string
			if err != nil {
				toolResults = append(toolResults, fmt.Sprintf("%s: error: %v", tc.Function.Name, err))
				resultJSON = fmt.Sprintf(`{"error": "%v"}`, err)
			} else {
				resultBytes, _ := json.Marshal(result)
				resultJSON = string(resultBytes)
				toolResults = append(toolResults, fmt.Sprintf("%s: %s", tc.Function.Name, resultJSON))
			}

			// Add tool result to conversation
			conversation = append(conversation, backend.Message{
				Role:       backend.MessageRoleTool,
				Content:    resultJSON,
				ToolCallID: tc.ID,
			})
		}
	}

	return buildToolSummary(toolResults), promptTokens, completionTokens, nil
}

// buildToolDefinitions converts the tool registry to backend tool definitions.
func buildToolDefinitions(registry *tools.Registry) []backend.Tool {
	var defs []backend.Tool
	for _, tool := range registry.All() {
		defs = append(defs, backend.Tool{
			Type: "function",
			Function: backend.ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  json.RawMessage(tool.InputSchema),
			},
		})
	}
	return defs
}

// buildToolSummary creates a summary of tool calls for the critic.
func buildToolSummary(results []string) string {
	if len(results) == 0 {
		return "No file changes were made."
	}

	var sb strings.Builder
	sb.WriteString("Tool calls executed:\n")
	for _, r := range results {
		sb.WriteString("- ")
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	return sb.String()
}
