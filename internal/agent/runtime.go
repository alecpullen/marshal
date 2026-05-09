package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	agtctx "github.com/alecpullen/marshal/internal/agent/context"
	agttools "github.com/alecpullen/marshal/internal/agent/tools"
	"github.com/alecpullen/marshal/internal/gateway"
)

// Runtime executes the agent's tool-call loop.
type Runtime struct {
	agent *Agent
}

// NewRuntime creates a runtime for an agent.
func NewRuntime(agent *Agent) *Runtime {
	return &Runtime{agent: agent}
}

// Run executes the agent until completion, failure, or iteration limit.
func (r *Runtime) Run(ctx context.Context) (*Result, error) {
	a := r.agent

	// Initialize context with deadline
	timeout := a.Manifest.Timeout
	if a.Task.Deadline.IsZero() {
		ctx, a.cancelFunc = context.WithTimeout(ctx, timeout)
	} else {
		// Use the shorter of the two deadlines
		deadline := time.Until(a.Task.Deadline)
		if deadline < timeout {
			ctx, a.cancelFunc = context.WithTimeout(ctx, deadline)
		} else {
			ctx, a.cancelFunc = context.WithTimeout(ctx, timeout)
		}
	}
	defer a.cancelFunc()

	a.Ctx = ctx

	// Set status
	a.Status = AgentStatusRunning
	a.Events.AgentStarted(a.ID, a.Role, a.Task.Goal)

	// Assemble initial context
	assembler := agtctx.NewAssembler(a.Store)
	contextEntries, truncated, err := assembler.AssembleWithTruncation(
		ctx,
		agtctx.ContextPolicy(a.Manifest.ContextPolicy),
		a.Task.Context,
	)
	if err != nil {
		a.Status = AgentStatusFailed
		return nil, fmt.Errorf("assemble context: %w", err)
	}

	// Build initial messages
	messages := r.buildInitialMessages(contextEntries, truncated)

	// Main loop
	for round := 1; round <= a.Manifest.MaxIterations; round++ {
		select {
		case <-ctx.Done():
			return r.handleTimeout(ctx.Err())
		default:
		}

		roundStart := time.Now()
		a.Events.RoundStart(a.ID, round, a.Manifest.MaxIterations)

		// Execute one round
		roundResult, err := r.executeRound(ctx, round, messages)
		if err != nil {
			// Check if this is a critical error
			if IsCritical(err) {
				return r.handleCriticalError(err)
			}
			// For retryable errors, add to tool results and continue
			if IsRetryable(err) {
				roundResult.ToolResults = append(roundResult.ToolResults, ToolResult{
					Content: fmt.Sprintf("Error: %v. %s", err, getRecoveryHint(err)),
					IsError: true,
				})
			} else {
				return r.handleError(err)
			}
		}

		// Record round
		roundResult.StartTime = roundStart
		endTime := time.Now()
		roundResult.EndTime = &endTime
		a.History = append(a.History, *roundResult)

		// Calculate usage for this round
		usage := roundResult.Usage
		a.Events.RoundEnd(a.ID, round, usage)

		// Check for completion
		if roundResult.Complete {
			// Validate output if schema is defined
			if len(a.Manifest.OutputSchema) > 0 {
				result := &Result{
					Status:  ResultStatusSuccess,
					Output:  roundResult.Output,
					Rounds:  round,
					ReadSet: a.ReadSet.AllPaths(),
					Usage:   calculateTotalUsage(a.History),
				}
				if err := result.ValidateOutput(a.Manifest.OutputSchema); err != nil {
					// Invalid output - let agent retry with error feedback
					roundResult.Complete = false
					roundResult.ToolResults = append(roundResult.ToolResults, ToolResult{
						Content: fmt.Sprintf("Output validation failed: %v. Please produce output matching the required schema.", err),
						IsError: true,
					})
				} else {
					// Valid output - complete
					return r.handleCompletion(roundResult)
				}
			} else {
				// No schema required - complete
				return r.handleCompletion(roundResult)
			}
		}

		// Prepare messages for next round
		messages = r.buildNextMessages(roundResult)
	}

	// Iteration limit reached
	return r.handleMaxIterations()
}

// executeRound runs one iteration of the tool-call loop.
func (r *Runtime) executeRound(
	ctx context.Context,
	roundNum int,
	messages []gateway.Message,
) (*Round, error) {
	a := r.agent

	// Convert manifest tools to gateway ToolDef
	toolDefs := r.convertTools(a.Manifest.Tools)

	// Prepare request
	req := gateway.ChatRequest{
		Messages:       messages,
		Tools:          toolDefs,
		MaxTokens:      4096,
		Temperature:    0.2,
		System:         a.Manifest.SystemPrompt,
		ToolChoice:     gateway.ToolChoiceAuto,
	}

	// Stream completion
	stream, err := a.Gateway.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gateway completion: %w", err)
	}

	// Process stream events
	var (
		content   strings.Builder
		toolCalls []ToolCall
		usage     gateway.Usage
		thinking  strings.Builder
	)

	for event := range stream {
		if event.Err != nil {
			return nil, fmt.Errorf("stream error: %w", event.Err)
		}

		switch event.Kind {
		case gateway.StreamEventDelta:
			content.WriteString(event.Text)
			a.Events.Token(a.ID, event.Text)

		case gateway.StreamEventThinking:
			thinking.WriteString(event.Thinking.Thinking)
			a.Events.ThinkBlock(a.ID, event.Thinking.Thinking)

		case gateway.StreamEventToolCall:
			tc := ToolCall{
				ID:        event.ToolCall.ID,
				Name:      event.ToolCall.Name,
				Arguments: event.ToolCall.Input,
			}
			toolCalls = append(toolCalls, tc)
			a.Events.ToolCall(a.ID, tc.Name, tc.Arguments)

		case gateway.StreamEventDone:
			if event.Usage != nil {
				usage = *event.Usage
			}

		case gateway.StreamEventError:
			return nil, fmt.Errorf("gateway error: %s", event.Text)
		}
	}

	// Execute tools
	var toolResults []ToolResult
	complete := false

	for _, tc := range toolCalls {
		result, err := r.executeTool(ctx, tc)
		if err != nil {
			// Check if this is a critical error
			if IsCritical(err) {
				return nil, err
			}
			// Record the error as a tool result
			result = &ToolResult{
				ToolCallID: tc.ID,
				Content:    fmt.Sprintf("Error: %v", err),
				IsError:    true,
			}
		}
		toolResults = append(toolResults, *result)
		a.Events.ToolResult(a.ID, tc.Name, result.Content, result.IsError)

		// Check for termination signals
		if tc.Name == "finish" || tc.Name == "complete" {
			complete = true
		}
	}

	return &Round{
		Number:      roundNum,
		ToolCalls:   toolCalls,
		ToolResults: toolResults,
		Output:      content.String(),
		Complete:    complete,
		Usage:       usage,
	}, nil
}

// executeTool runs a tool with proper error handling.
func (r *Runtime) executeTool(ctx context.Context, tc ToolCall) (*ToolResult, error) {
	a := r.agent

	// Get tool from registry
	tool, ok := a.Tools.Get(tc.Name)
	if !ok {
		return nil, &agttools.ToolError{
			Code:    "tool_not_found",
			Message: fmt.Sprintf("Tool %s not found", tc.Name),
			Hint:    fmt.Sprintf("Available tools: %v", a.Tools.List()),
		}
	}

	// Execute tool
	result, err := tool.Invoke(ctx, tc.Arguments)
	if err != nil {
		return nil, err
	}

	return &ToolResult{
		ToolCallID: tc.ID,
		Content:    result.Content,
		IsError:    result.Error != nil,
	}, nil
}

// buildInitialMessages creates the first message set.
func (r *Runtime) buildInitialMessages(contextEntries []agtctx.ContextEntry, truncated bool) []gateway.Message {
	a := r.agent

	var contextBuilder strings.Builder

	// Add context header
	if truncated {
		contextBuilder.WriteString("[Context truncated to fit token limit]\n\n")
	}

	// Add context entries
	for _, entry := range contextEntries {
		contextBuilder.WriteString(fmt.Sprintf("## %s\n\n", entry.Key))
		contextBuilder.WriteString(entry.Content)
		contextBuilder.WriteString("\n\n")
	}

	// Add task goal
	contextBuilder.WriteString(fmt.Sprintf("Task: %s\n\n", a.Task.Goal))
	contextBuilder.WriteString("Use the available tools to complete this task. Read files before modifying them.")

	return []gateway.Message{
		{
			Role:    gateway.RoleSystem,
			Content: []gateway.ContentBlock{{Type: gateway.ContentBlockTypeText, Text: a.Manifest.SystemPrompt}},
		},
		{
			Role:    gateway.RoleUser,
			Content: []gateway.ContentBlock{{Type: gateway.ContentBlockTypeText, Text: contextBuilder.String()}},
		},
	}
}

// buildNextMessages prepares messages for the next round.
func (r *Runtime) buildNextMessages(prevRound *Round) []gateway.Message {
	a := r.agent
	var messages []gateway.Message

	// Include system prompt
	messages = append(messages, gateway.Message{
		Role:    gateway.RoleSystem,
		Content: []gateway.ContentBlock{{Type: gateway.ContentBlockTypeText, Text: a.Manifest.SystemPrompt}},
	})

	// Add conversation history
	for _, round := range a.History {
		// Assistant message with tool calls
		var content []gateway.ContentBlock

		if round.Output != "" {
			content = append(content, gateway.ContentBlock{
				Type: gateway.ContentBlockTypeText,
				Text: round.Output,
			})
		}

		for _, tc := range round.ToolCalls {
			content = append(content, gateway.ContentBlock{
				Type: gateway.ContentBlockTypeToolUse,
				ToolUse: &gateway.ToolUseContent{
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Arguments,
				},
			})
		}

		messages = append(messages, gateway.Message{
			Role:    gateway.RoleAssistant,
			Content: content,
		})

		// Tool results
		for _, tr := range round.ToolResults {
			var resultContent string
			if tr.IsError {
				resultContent = fmt.Sprintf("Error: %s", tr.Content)
			} else {
				resultContent = tr.Content
			}

			messages = append(messages, gateway.Message{
				Role: gateway.RoleTool,
				Content: []gateway.ContentBlock{{
					Type: gateway.ContentBlockTypeToolResult,
					ToolResult: &gateway.ToolResultContent{
						ToolUseID: tr.ToolCallID,
						Content:   resultContent,
						IsError:   tr.IsError,
					},
				}},
			})
		}
	}

	return messages
}

// convertTools converts manifest tool names to gateway ToolDef.
func (r *Runtime) convertTools(names []string) []gateway.ToolDef {
	a := r.agent
	var defs []gateway.ToolDef

	for _, name := range names {
		if tool, ok := a.Tools.Get(name); ok {
			defs = append(defs, gateway.ToolDef{
				Name:        tool.Name(),
				Description: tool.Description(),
				InputSchema: tool.Schema(),
			})
		}
	}

	return defs
}

// Handlers for different completion scenarios
func (r *Runtime) handleCompletion(round *Round) (*Result, error) {
	a := r.agent
	a.Status = AgentStatusCompleted
	a.Events.AgentCompleted(a.ID, round.Output)

	// Collect sub-agent results
	subResults := make(map[string]*SubAgentResult)
	for id := range a.SubAgents {
		if result, err := a.GetSubAgentResult(id); err == nil {
			subResults[id] = result
		}
	}

	return &Result{
		Status:          ResultStatusSuccess,
		Output:          round.Output,
		Rounds:          len(a.History),
		ReadSet:         a.ReadSet.AllPaths(),
		Usage:           calculateTotalUsage(a.History),
		SubAgentResults: subResults,
	}, nil
}

func (r *Runtime) handleTimeout(err error) (*Result, error) {
	a := r.agent
	a.Status = AgentStatusFailed
	a.Events.AgentFailed(a.ID, fmt.Sprintf("timeout: %v", err))

	return &Result{
		Status:  ResultStatusTimeout,
		Error:   fmt.Sprintf("execution timeout: %v", err),
		Rounds:  len(a.History),
		ReadSet: a.ReadSet.AllPaths(),
		Usage:   calculateTotalUsage(a.History),
	}, nil
}

func (r *Runtime) handleCriticalError(err error) (*Result, error) {
	a := r.agent
	a.Status = AgentStatusFailed
	a.Events.AgentFailed(a.ID, fmt.Sprintf("critical error: %v", err))

	return &Result{
		Status:  ResultStatusError,
		Error:   fmt.Sprintf("critical error: %v", err),
		Rounds:  len(a.History),
		ReadSet: a.ReadSet.AllPaths(),
		Usage:   calculateTotalUsage(a.History),
	}, nil
}

func (r *Runtime) handleError(err error) (*Result, error) {
	a := r.agent
	a.Status = AgentStatusFailed
	a.Events.AgentFailed(a.ID, err.Error())

	return &Result{
		Status:  ResultStatusError,
		Error:   err.Error(),
		Rounds:  len(a.History),
		ReadSet: a.ReadSet.AllPaths(),
		Usage:   calculateTotalUsage(a.History),
	}, nil
}

func (r *Runtime) handleMaxIterations() (*Result, error) {
	a := r.agent
	a.Status = AgentStatusFailed
	a.Events.AgentFailed(a.ID, "max iterations reached")

	return &Result{
		Status:  ResultStatusMaxIterations,
		Error:   "max iterations reached without completion",
		Rounds:  len(a.History),
		ReadSet: a.ReadSet.AllPaths(),
		Usage:   calculateTotalUsage(a.History),
	}, nil
}

// getRecoveryHint extracts recovery hint from error.
func getRecoveryHint(err error) string {
	if toolErr, ok := err.(*agttools.ToolError); ok {
		return toolErr.Hint
	}
	return "Please try again with a different approach."
}

// GetAgent returns the underlying agent.
func (r *Runtime) GetAgent() *Agent {
	return r.agent
}
