// Package executor provides the code-writing agent.
// The executor receives instructions from the Marshal and generates code.
package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/prompts"
	"github.com/alecpullen/marshal/internal/tools"
	"github.com/alecpullen/marshal/internal/types"
)

// Executor is the code-writing agent. It receives tasks from the Marshal
// and generates code implementations.
type Executor struct {
	backend  backend.Backend
	cfg      config.AgentConfig
	skills   []types.Skill
	repoRoot string // sandbox root for tool execution
	maxTools int    // max tool calls per round (0 → default 20)
}

// Request contains the task and any prior feedback.
type Request struct {
	Task          string
	PriorFeedback string
}

// Result contains the generated code and token usage.
type Result struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
}

// New creates a new executor agent.
func New(be backend.Backend, cfg config.AgentConfig, skills []types.Skill, repoRoot string) *Executor {
	maxTools := cfg.MaxToolCalls
	if maxTools == 0 {
		maxTools = 20
	}
	return &Executor{
		backend:  be,
		cfg:      cfg,
		skills:   skills,
		repoRoot: repoRoot,
		maxTools: maxTools,
	}
}

// Execute sends the task to the LLM and returns the generated code.
// This is the primary method the Marshal uses to command the Executor.
func (e *Executor) Execute(ctx context.Context, req Request) (*Result, error) {
	messages := e.buildMessages(req)

	resp, err := e.backend.Complete(ctx, e.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}

	return &Result{
		Content:          resp.Content,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
	}, nil
}

// ExecuteStreaming sends the task to the LLM with streaming callback support.
// If the backend supports streaming (StreamingBackend), it streams chunks via onChunk.
// Otherwise it falls back to non-streaming Complete.
func (e *Executor) ExecuteStreaming(ctx context.Context, req Request, onChunk func(string) error) (*Result, error) {
	messages := e.buildMessages(req)

	if sb := backend.AsStreaming(e.backend); sb != nil {
		var full strings.Builder
		err := sb.CompleteStreaming(ctx, e.cfg.Model, messages, func(chunk backend.StreamResponse) {
			full.WriteString(chunk.Content)
			if onChunk != nil {
				_ = onChunk(chunk.Content)
			}
		})
		if err != nil {
			return nil, fmt.Errorf("streaming complete: %w", err)
		}
		return &Result{
			Content:          full.String(),
			PromptTokens:     0,
			CompletionTokens: 0,
		}, nil
	}

	// Fallback to non-streaming
	resp, err := e.backend.Complete(ctx, e.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}
	if onChunk != nil {
		_ = onChunk(resp.Content)
	}
	return &Result{
		Content:          resp.Content,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
	}, nil
}

// ExecuteWithTools runs the executor with an agentic tool loop.
// The model may call tools repeatedly until it produces a final text response.
// Falls back to Execute when tools are disabled in config or the backend
// does not implement ToolCapableBackend.
func (e *Executor) ExecuteWithTools(ctx context.Context, req Request) (*Result, error) {
	if !e.cfg.EnableTools {
		return e.Execute(ctx, req)
	}

	tb := backend.AsToolCapable(e.backend)
	if tb == nil {
		return e.Execute(ctx, req)
	}

	toolDefs := tools.DefaultTools(e.repoRoot)
	messages := e.buildMessages(req)

	callCount := 0
	for {
		if callCount >= e.maxTools {
			return nil, fmt.Errorf("executor: tool call limit (%d) reached", e.maxTools)
		}

		resp, err := tb.CompleteWithTools(ctx, e.cfg.Model, messages, toolDefs)
		if err != nil {
			return nil, fmt.Errorf("executor: complete with tools: %w", err)
		}

		// No tool calls → model produced its final answer
		if len(resp.ToolCalls) == 0 {
			return &Result{Content: resp.Content}, nil
		}

		// Append the assistant turn that contains the tool calls
		messages = append(messages, backend.Message{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call and append results as tool-role messages
		for _, call := range resp.ToolCalls {
			callCount++
			result := tools.Execute(ctx, call, e.repoRoot)
			messages = append(messages, backend.Message{
				Role:       "tool",
				ToolCallID: result.CallID,
				Content:    result.Content,
			})
		}
		// Continue: model will see results and either call more tools or finish
	}
}

// buildMessages constructs the messages for the executor.
func (e *Executor) buildMessages(req Request) []backend.Message {
	systemPrompt := e.buildSystemPrompt()

	messages := []backend.Message{
		{Role: "system", Content: systemPrompt},
	}

	// Build user message
	userContent := req.Task
	if req.PriorFeedback != "" {
		userContent = fmt.Sprintf("%s\n\nPrevious feedback:\n%s", req.Task, req.PriorFeedback)
	}
	messages = append(messages, backend.Message{Role: "user", Content: userContent})

	return messages
}

// buildSystemPrompt constructs the full system prompt.
func (e *Executor) buildSystemPrompt() string {
	prompt := prompts.SecurityInstructions + "\n\n" + prompts.ExecutorBaseInstructions

	// Append tool instructions when tools are enabled
	if e.cfg.EnableTools {
		prompt += "\n\n" + prompts.ToolInstructions
	}

	// Append skill additions
	if len(e.skills) > 0 {
		prompt += "\n\n<skill_additions>\n"
		for _, skill := range e.skills {
			prompt += fmt.Sprintf("\n[%s]\n%s\n", skill.Name, skill.SystemPromptAdditions)
		}
		prompt += "</skill_additions>"
	}

	return prompt
}
