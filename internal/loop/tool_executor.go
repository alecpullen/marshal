// Package loop implements the single-task executor-critic round loop.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/tools"
)

// runToolUseRound executes a single round using tool-use instead of edit formats.
// It implements multi-turn tool calling within one executor round using streaming
// for better responsiveness and to support larger max_tokens without API limits.
// Returns (content, promptTokens, completionTokens, error) where content is
// a summary of tool calls made for the critic's benefit.
func (e *Engine) runToolUseRound(ctx context.Context, b backend.Backend, msgs []backend.Message) (content string, promptTokens, completionTokens int, err error) {
	toolRegistry := tools.NewRegistry()
	toolDefs := buildToolDefinitions(toolRegistry)

	// Track conversation for this round
	conversation := make([]backend.Message, len(msgs))
	copy(conversation, msgs)

	var toolResults []string
	var allContent strings.Builder // Accumulate all content across tool call iterations
	readCount := 0                 // Count read_file operations
	maxToolCalls := 10             // Prevent infinite loops

	for callCount := 0; callCount < maxToolCalls; callCount++ {
		// Check if model is only reading without outputting findings
		if readCount > 8 && allContent.Len() < 200 {
			// Model has read many files but produced little output - force findings
			conversation = append(conversation, backend.Message{
				Role:    backend.MessageRoleSystem,
				Content: "You have examined many files but not yet output your findings. STOP reading more files and output your security audit findings NOW. List specific flaws with file paths and line numbers.",
			})
		}
		// Request streaming completion with tools available
		req := backend.Request{
			Messages: conversation,
			Tools:    toolDefs,
			// No MaxTokens limit - streaming allows larger responses without API limits
		}

		// Use streaming for better UX and no max_tokens limits
		ch, err := b.Stream(ctx, req)
		if err != nil {
			return "", 0, 0, fmt.Errorf("tool use stream: %w", err)
		}

		// Accumulate streamed response and tool calls
		var contentBuilder strings.Builder
		var accumulatedToolCalls []backend.ToolCall

		for chunk := range ch {
			if chunk.Err != nil {
				return "", promptTokens, completionTokens, fmt.Errorf("tool use stream chunk: %w", chunk.Err)
			}

			// Accumulate content and stream as think blocks for collapsible display
			if chunk.Content != "" {
				contentBuilder.WriteString(chunk.Content)
				allContent.WriteString(chunk.Content) // Accumulate across all iterations
				// Stream as think block for real-time display in collapsible blocks
				e.sink.ThinkBlock(e.cfg.SessionID, chunk.Content)
			}

			// Accumulate tool call deltas
			for _, tc := range chunk.ToolCalls {
				if tc.ID != "" {
					// This is a new tool call - find or create it
					found := false
					for i := range accumulatedToolCalls {
						if accumulatedToolCalls[i].ID == tc.ID {
							// Accumulate arguments
							accumulatedToolCalls[i].Function.Arguments += tc.Function.Arguments
							found = true
							break
						}
					}
					if !found {
						accumulatedToolCalls = append(accumulatedToolCalls, tc)
					}
				}
			}

			completionTokens++
		}

		// Signal that think block streaming is complete for this iteration
		e.sink.ThinkBlockDone(e.cfg.SessionID)

		// Use tool calls from stream (or fall back to non-streaming if none found)
		toolCalls := accumulatedToolCalls

		// If no tool calls from stream, try non-streaming request
		// (some APIs may not support tool calls in streaming mode)
		if len(toolCalls) == 0 {
			reqNonStream := backend.Request{
				Messages:  conversation,
				Tools:     toolDefs,
				MaxTokens: 4096, // Cap for APIs that require streaming for larger
			}
			resp, err := b.Complete(ctx, reqNonStream)
			if err != nil {
				return "", 0, 0, fmt.Errorf("tool use completion: %w", err)
			}
			promptTokens += resp.Usage.PromptTokens
			completionTokens = resp.Usage.CompletionTokens
			toolCalls = resp.ToolCalls

			// Stream the content from non-stream response
			if resp.Content != "" {
				e.sink.Token(resp.Content)
			}
		}

		// Check if there are tool calls
		if len(toolCalls) == 0 {
			// No more tool calls - we're done
			// Return all accumulated content + this iteration's content + tool summary
			finalContent := allContent.String()
			currentContent := contentBuilder.String()
			if currentContent != "" && !strings.Contains(finalContent, currentContent) {
				if finalContent != "" {
					finalContent += "\n\n"
				}
				finalContent += currentContent
			}
			if finalContent == "" {
				finalContent = buildToolSummary(toolResults)
			} else if len(toolResults) > 0 {
				finalContent += "\n\n" + buildToolSummary(toolResults)
			}
			return finalContent, promptTokens, completionTokens, nil
		}

		// Process tool calls
		for _, tc := range toolCalls {
			tool, ok := toolRegistry.Get(tc.Function.Name)
			if !ok {
				toolResults = append(toolResults, fmt.Sprintf("Tool %s not found", tc.Function.Name))
				continue
			}

			// Extract path from arguments for display
			path := extractPathFromArgs(json.RawMessage(tc.Function.Arguments))

			// Handle proposal mode for write_file
			if tc.Function.Name == "write_file" && e.proposalMode {
				result, proposalErr := e.handleWriteFileProposal(json.RawMessage(tc.Function.Arguments))
				var resultJSON string
				if proposalErr != nil {
					resultJSON = fmt.Sprintf(`{"error": "%v"}`, proposalErr)
					e.sink.ToolOperation(e.cfg.SessionID, "write_file", path, "failed", proposalErr.Error())
				} else {
					resultBytes, _ := json.Marshal(result)
					resultJSON = string(resultBytes)
					summary := buildCompactSummary("write_file", result)
					toolResults = append(toolResults, fmt.Sprintf("%s: %s (proposed)", tc.Function.Name, summary))
					e.sink.ToolOperation(e.cfg.SessionID, "write_file", path, "proposed", summary)
				}
				conversation = append(conversation, backend.Message{
					Role:       backend.MessageRoleTool,
					Content:    resultJSON,
					ToolCallID: tc.ID,
				})
				continue
			}

			// Check permission gate for write operations
			if tc.Function.Name == "write_file" && e.cfg.Permission != config.PermissionNever {
				if !e.requestPermission(ctx, "write_file", path, json.RawMessage(tc.Function.Arguments)) {
					// User denied permission
					resultJSON := `{"error": "User denied permission to write file"}`
					toolResults = append(toolResults, fmt.Sprintf("%s: denied by user", tc.Function.Name))
					conversation = append(conversation, backend.Message{
						Role:       backend.MessageRoleTool,
						Content:    resultJSON,
						ToolCallID: tc.ID,
					})
					e.sink.ToolOperation(e.cfg.SessionID, "write_file", path, "failed", "denied by user")
					continue
				}
			}

			// Show compact operation start with descriptive label
			statusLabel := "running"
			if tc.Function.Name == "read_file" {
				statusLabel = "reading"
				readCount++
			} else if tc.Function.Name == "write_file" {
				statusLabel = "writing"
			} else if tc.Function.Name == "run_command" {
				statusLabel = "running"
			}
			e.sink.ToolOperation(e.cfg.SessionID, tc.Function.Name, path, statusLabel, "")

			// Execute the tool
			result, err := tool.Handler(e.repo.Root(), json.RawMessage(tc.Function.Arguments))
			var resultJSON string
			if err != nil {
				toolResults = append(toolResults, fmt.Sprintf("%s: error: %v", tc.Function.Name, err))
				resultJSON = fmt.Sprintf(`{"error": "%v"}`, err)
				e.sink.ToolOperation(e.cfg.SessionID, tc.Function.Name, path, "failed", err.Error())
			} else {
				resultBytes, _ := json.Marshal(result)
				resultJSON = string(resultBytes)
				// Build compact summary
				summary := buildCompactSummary(tc.Function.Name, result)
				toolResults = append(toolResults, fmt.Sprintf("%s: %s", tc.Function.Name, summary))
				e.sink.ToolOperation(e.cfg.SessionID, tc.Function.Name, path, "done", summary)
			}

			// Add tool result to conversation
			conversation = append(conversation, backend.Message{
				Role:       backend.MessageRoleTool,
				Content:    resultJSON,
				ToolCallID: tc.ID,
			})
		}
	}

	// Max tool calls reached - return accumulated content + tool summary
	finalContent := allContent.String()
	if finalContent == "" {
		finalContent = buildToolSummary(toolResults)
	} else if len(toolResults) > 0 {
		finalContent += "\n\n" + buildToolSummary(toolResults)
	}
	return finalContent, promptTokens, completionTokens, nil
}

// runToolUseRoundWithProposals executes a round with pre-apply critic review.
// It captures all file changes as proposals, runs critic on them, and only applies if approved.
func (e *Engine) runToolUseRoundWithProposals(ctx context.Context, b backend.Backend, msgs []backend.Message, taskID string, round int) (content string, promptTokens, completionTokens int, err error) {
	// Enable proposal mode
	e.beginProposalMode()
	defer e.endProposalMode()

	// Run normal tool-use round (writes will be captured as proposals)
	toolRegistry := tools.NewRegistry()
	toolDefs := buildToolDefinitions(toolRegistry)

	conversation := make([]backend.Message, len(msgs))
	copy(conversation, msgs)

	var toolResults []string
	maxToolCalls := 10

	for callCount := 0; callCount < maxToolCalls; callCount++ {
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

		if len(resp.ToolCalls) == 0 {
			// Executor finished, now review proposals
			break
		}

		// Process tool calls (in proposal mode, writes are captured)
		for _, tc := range resp.ToolCalls {
			tool, ok := toolRegistry.Get(tc.Function.Name)
			if !ok {
				toolResults = append(toolResults, fmt.Sprintf("Tool %s not found", tc.Function.Name))
				continue
			}

			path := extractPathFromArgs(json.RawMessage(tc.Function.Arguments))

			// In proposal mode, handle write_file specially
			if tc.Function.Name == "write_file" {
				result, proposalErr := e.handleWriteFileProposal(json.RawMessage(tc.Function.Arguments))
				var resultJSON string
				if proposalErr != nil {
					resultJSON = fmt.Sprintf(`{"error": "%v"}`, proposalErr)
					e.sink.ToolOperation(e.cfg.SessionID, "write_file", path, "failed", proposalErr.Error())
				} else {
					resultBytes, _ := json.Marshal(result)
					resultJSON = string(resultBytes)
					summary := buildCompactSummary("write_file", result)
					toolResults = append(toolResults, fmt.Sprintf("%s: %s (proposed)", tc.Function.Name, summary))
					e.sink.ToolOperation(e.cfg.SessionID, "write_file", path, "proposed", summary)
				}
				conversation = append(conversation, backend.Message{
					Role:       backend.MessageRoleTool,
					Content:    resultJSON,
					ToolCallID: tc.ID,
				})
				continue
			}

			// For other tools (read, run_command), execute normally
			statusLabel := "running"
			if tc.Function.Name == "read_file" {
				statusLabel = "reading"
			}
			e.sink.ToolOperation(e.cfg.SessionID, tc.Function.Name, path, statusLabel, "")

			result, err := tool.Handler(e.repo.Root(), json.RawMessage(tc.Function.Arguments))
			var resultJSON string
			if err != nil {
				toolResults = append(toolResults, fmt.Sprintf("%s: error: %v", tc.Function.Name, err))
				resultJSON = fmt.Sprintf(`{"error": "%v"}`, err)
				e.sink.ToolOperation(e.cfg.SessionID, tc.Function.Name, path, "failed", err.Error())
			} else {
				resultBytes, _ := json.Marshal(result)
				resultJSON = string(resultBytes)
				summary := buildCompactSummary(tc.Function.Name, result)
				toolResults = append(toolResults, fmt.Sprintf("%s: %s", tc.Function.Name, summary))
				e.sink.ToolOperation(e.cfg.SessionID, tc.Function.Name, path, "done", summary)
			}

			conversation = append(conversation, backend.Message{
				Role:       backend.MessageRoleTool,
				Content:    resultJSON,
				ToolCallID: tc.ID,
			})
		}
	}

	// Now we have proposals, run critic review
	if len(e.proposals) == 0 {
		// No changes proposed
		return buildToolSummary(toolResults), promptTokens, completionTokens, nil
	}

	// Build diff from proposals for critic
	proposalDiff := e.buildProposalDiff()

	// Get critic backend
	criticB, cerr := e.reg.For(config.RoleCritic)
	if cerr != nil {
		// No critic available, auto-approve
		e.sink.VerdictBadge(taskID, "PASS", "No critic available; auto-approved proposals")
		_, applyErr := e.applyProposals()
		if applyErr != nil {
			return "", promptTokens, completionTokens, applyErr
		}
		return buildToolSummary(toolResults), promptTokens, completionTokens, nil
	}

	// Call critic on proposals
	criticMsgs := e.buildCriticMessagesForProposals(proposalDiff, buildToolSummary(toolResults))
	criticContent, cPToks, cCToks, cerr := e.callCritic(ctx, criticB, criticMsgs, e.criticSubtype)
	if cerr != nil {
		// Critic failed, auto-approve
		e.sink.VerdictBadge(taskID, "PASS", "Critic error; auto-approved proposals")
		_, applyErr := e.applyProposals()
		if applyErr != nil {
			return "", promptTokens, completionTokens, applyErr
		}
		return buildToolSummary(toolResults) + "\n" + criticContent, promptTokens + cPToks, completionTokens + cCToks, nil
	}

	// Parse verdict
	verdict, _, parseErr := ParseVerdict(criticContent)
	if parseErr != nil {
		verdict = &Verdict{Verdict: "FAIL", Summary: "critic returned unparseable response", Issue: parseErr.Error()}
	}

	// Handle verdict
	e.sink.VerdictBadge(taskID, verdict.Verdict, verdict.Summary)

	if verdict.IsPASS() {
		// Apply proposals
		_, applyErr := e.applyProposals()
		if applyErr != nil {
			return "", promptTokens, completionTokens, applyErr
		}
		// Clear proposals after successful apply
		e.discardProposals()
		return buildToolSummary(toolResults) + "\n" + criticContent, promptTokens + cPToks, completionTokens + cCToks, nil
	}

	// FAIL: discard proposals and return error to trigger retry
	e.discardProposals()
	return "", promptTokens + cPToks, completionTokens + cCToks, fmt.Errorf("proposals rejected: %s - %s", verdict.Summary, verdict.Issue)
}

// buildCriticMessagesForProposals builds critic messages for reviewing proposed changes.
func (e *Engine) buildCriticMessagesForProposals(proposalDiff, execSummary string) []backend.Message {
	var sb strings.Builder
	sb.WriteString("Review the following PROPOSED changes before they are applied:\n\n")
	sb.WriteString("Proposed diff:\n```diff\n")
	sb.WriteString(proposalDiff)
	sb.WriteString("\n```\n\n")
	sb.WriteString("Executor actions:\n")
	sb.WriteString(execSummary)
	sb.WriteString("\n\n")
	sb.WriteString(criticOutputInstructions)

	return []backend.Message{
		{Role: backend.MessageRoleSystem, Content: e.criticSysPrompt},
		{Role: backend.MessageRoleUser, Content: sb.String()},
	}
}

// extractPathFromArgs extracts the path field from tool arguments JSON.
func extractPathFromArgs(args json.RawMessage) string {
	var data struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &data); err == nil {
		if data.Path != "" {
			return data.Path
		}
		if data.Command != "" {
			// Truncate long commands for display
			if len(data.Command) > 50 {
				return data.Command[:50] + "..."
			}
			return data.Command
		}
	}
	return "unknown"
}

// requestPermission asks the user for permission before executing a destructive operation.
// Returns true if permission granted, false otherwise.
func (e *Engine) requestPermission(ctx context.Context, toolName, path string, args json.RawMessage) bool {
	// Build preview of the change
	preview := buildPreview(toolName, args)

	response := make(chan bool, 1)
	e.sink.PermissionRequest(e.cfg.SessionID, toolName, path, preview, response)

	// Wait for response with context cancellation support
	select {
	case granted := <-response:
		return granted
	case <-ctx.Done():
		return false
	}
}

// buildPreview creates a brief preview of the change for the permission prompt.
func buildPreview(toolName string, args json.RawMessage) string {
	switch toolName {
	case "write_file":
		var data struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &data); err == nil {
			lines := strings.Count(data.Content, "\n")
			chars := len(data.Content)
			return fmt.Sprintf("%d lines (%d bytes)", lines, chars)
		}
	}
	return ""
}

// buildCompactSummary creates a brief summary of tool execution result.
func buildCompactSummary(toolName string, result interface{}) string {
	switch toolName {
	case "write_file":
		if r, ok := result.(map[string]interface{}); ok {
			if size, ok := r["size"].(float64); ok {
				return fmt.Sprintf("%d bytes written", int(size))
			}
		}
		return "file written"
	case "read_file":
		if r, ok := result.(map[string]interface{}); ok {
			lines := 0
			if l, ok := r["lines"].(float64); ok {
				lines = int(l)
			}
			size := 0
			if s, ok := r["size"].(float64); ok {
				size = int(s)
			}
			return fmt.Sprintf("%d lines (%d bytes)", lines, size)
		}
		return "file read"
	case "run_command":
		if r, ok := result.(map[string]interface{}); ok {
			exitCode := 0
			if e, ok := r["exit_code"].(float64); ok {
				exitCode = int(e)
			}
			if exitCode == 0 {
				return "success"
			}
			return fmt.Sprintf("exit %d", exitCode)
		}
		return "command executed"
	}
	return "done"
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
