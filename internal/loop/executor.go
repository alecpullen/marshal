package loop

import (
	"context"
	"fmt"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
)

// Executor sends tasks to the LLM and returns generated code.
type Executor struct {
	backend backend.Backend
	cfg     config.AgentConfig
	skills  []Skill
}

// ExecutorRequest contains the task and any prior feedback.
type ExecutorRequest struct {
	Task          string
	PriorFeedback string
}

// NewExecutor creates a new executor agent.
func NewExecutor(be backend.Backend, cfg config.AgentConfig, skills []Skill) *Executor {
	return &Executor{
		backend: be,
		cfg:     cfg,
		skills:  skills,
	}
}

// ExecuteResult contains the generated code and token usage.
type ExecuteResult struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
}

// Execute sends the task to the LLM and returns the generated code.
func (e *Executor) Execute(ctx context.Context, req ExecutorRequest) (*ExecuteResult, error) {
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

	resp, err := e.backend.Complete(ctx, e.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}

	return &ExecuteResult{
		Content:          resp.Content,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
	}, nil
}

// buildSystemPrompt constructs the full system prompt.
func (e *Executor) buildSystemPrompt() string {
	prompt := SecurityInstructions + "\n\n" + ExecutorBaseInstructions

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
