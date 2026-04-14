package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/prompts"
)

// Planner decomposes feature descriptions into dependency-ordered task graphs.
type Planner struct {
	backend backend.Backend
	cfg     config.AgentConfig
}

// Result contains the task graph and token usage from a planning call.
type Result struct {
	Graph            *TaskGraph
	PromptTokens     int
	CompletionTokens int
	RawJSON          string
}

// New creates a new Planner agent.
func New(be backend.Backend, cfg config.AgentConfig) *Planner {
	return &Planner{
		backend: be,
		cfg:     cfg,
	}
}

// Plan sends the feature description to the LLM and returns a validated task graph.
func (p *Planner) Plan(ctx context.Context, feature string) (*Result, error) {
	messages := []backend.Message{
		{Role: "system", Content: p.buildSystemPrompt()},
		{Role: "user", Content: feature},
	}

	resp, err := p.backend.Complete(ctx, p.cfg.Model, messages)
	if err != nil {
		return nil, fmt.Errorf("backend complete: %w", err)
	}

	graph, err := parseTaskGraph(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("parse task graph: %w", err)
	}

	if err := ValidateGraph(graph); err != nil {
		return nil, fmt.Errorf("invalid task graph: %w", err)
	}

	return &Result{
		Graph:            graph,
		PromptTokens:     resp.PromptTokens,
		CompletionTokens: resp.CompletionTokens,
		RawJSON:          resp.Content,
	}, nil
}

// buildSystemPrompt constructs the full system prompt for the planner LLM.
func (p *Planner) buildSystemPrompt() string {
	return prompts.PlannerBaseInstructions
}

// parseTaskGraph extracts and unmarshals a TaskGraph from raw LLM output.
// Defensively finds the outermost JSON object in case of surrounding whitespace or text.
func parseTaskGraph(content string) (*TaskGraph, error) {
	// Find outermost JSON object boundaries
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")

	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}

	jsonStr := content[start : end+1]

	var graph TaskGraph
	if err := json.Unmarshal([]byte(jsonStr), &graph); err != nil {
		return nil, fmt.Errorf("unmarshal task graph: %w", err)
	}

	if graph.Feature == "" {
		return nil, fmt.Errorf("task graph missing required field: feature")
	}
	if len(graph.Tasks) == 0 {
		return nil, fmt.Errorf("task graph has no tasks")
	}

	return &graph, nil
}
