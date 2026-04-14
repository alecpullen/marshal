// Package planner implements the high-level task decomposition that converts a
// user prompt into a DAG of concrete tasks for the pipeline scheduler.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/prompts"
)

// Task represents a single unit of work in the pipeline task graph.
type Task struct {
	ID                   string   `json:"id"`
	Description          string   `json:"description"`
	FilesLikelyAffected  []string `json:"files_likely_affected"`
	DependsOn            []string `json:"depends_on"`
}

// TaskGraph is the output of the planner: a decomposed set of tasks with dependencies.
type TaskGraph struct {
	Prompt string `json:"prompt"`
	Tasks  []Task `json:"tasks"`
}

// plannerPrompt instructs the marshal model to decompose a high-level feature
// into concrete tasks with dependency information.
const plannerPrompt = `You are a software architect decomposing high-level feature requests into concrete implementation tasks.

Given a user request, output a JSON task graph that breaks the work into 2–6 concrete, independently implementable tasks. Each task should:
- Have a clear, specific description (1–2 sentences)
- List files likely affected (best-guess paths relative to repo root)
- Declare dependencies on other task IDs (or empty for independent tasks)

Output format (JSON only, no markdown):
{
  "prompt": "original user prompt",
  "tasks": [
    {
      "id": "task-1",
      "description": "Create database schema for X",
      "files_likely_affected": ["db/schema.sql", "models/x.go"],
      "depends_on": []
    },
    {
      "id": "task-2",
      "description": "Implement API endpoint for X",
      "files_likely_affected": ["api/x.go", "routes.go"],
      "depends_on": ["task-1"]
    }
  ]
}

Rules:
- Task IDs must be unique and follow pattern "task-N"
- Dependencies must reference existing task IDs
- No circular dependencies
- Tasks with overlapping files should usually have a dependency ordering to prevent conflicts
- Err on the side of more granular tasks rather than monolithic ones

Respond with ONLY valid JSON (no markdown, no prose).`

// criticOutputInstructions reused from loop package for consistency.
const criticOutputInstructions = `Respond with ONLY valid JSON (no markdown, no prose).`

// Generate calls the marshal model to decompose the user prompt into a task graph.
func Generate(ctx context.Context, marshalB backend.Backend, userPrompt string) (*TaskGraph, error) {
	msgs := []backend.Message{
		{Role: backend.MessageRoleSystem, Content: plannerPrompt},
		{Role: backend.MessageRoleUser, Content: userPrompt},
	}

	resp, err := marshalB.Complete(ctx, backend.Request{Messages: msgs})
	if err != nil {
		return nil, fmt.Errorf("planner backend call: %w", err)
	}

	content := stripThinkBlocks(resp.Content)
	content = strings.TrimSpace(content)

	// Handle markdown fences if present.
	if strings.HasPrefix(content, "```") {
		lines := strings.Split(content, "\n")
		var inside []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```json") || strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock || (!strings.HasPrefix(line, "```") && len(inside) > 0) {
				inside = append(inside, line)
			}
		}
		content = strings.Join(inside, "\n")
	}

	var graph TaskGraph
	if err := json.Unmarshal([]byte(content), &graph); err != nil {
		return nil, fmt.Errorf("parse task graph JSON: %w\nraw: %s", err, resp.Content)
	}

	// Validate the graph.
	if err := Validate(&graph); err != nil {
		return nil, fmt.Errorf("invalid task graph: %w", err)
	}

	return &graph, nil
}

// Validate checks the task graph for structural errors.
func Validate(g *TaskGraph) error {
	ids := make(map[string]bool)
	for _, t := range g.Tasks {
		if t.ID == "" {
			return fmt.Errorf("task missing id")
		}
		if ids[t.ID] {
			return fmt.Errorf("duplicate task id: %s", t.ID)
		}
		ids[t.ID] = true
	}

	// Check dependencies exist and build dependency graph for cycle detection.
	deps := make(map[string]map[string]bool)
	for _, t := range g.Tasks {
		deps[t.ID] = make(map[string]bool)
		for _, d := range t.DependsOn {
			if !ids[d] {
				return fmt.Errorf("task %s depends on unknown task %s", t.ID, d)
			}
			deps[t.ID][d] = true
		}
	}

	// Cycle detection via DFS.
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	var dfs func(string) bool
	dfs = func(id string) bool {
		visited[id] = true
		recStack[id] = true
		for dep := range deps[id] {
			if !visited[dep] {
				if dfs(dep) {
					return true
				}
			} else if recStack[dep] {
				return true
			}
		}
		recStack[id] = false
		return false
	}
	for id := range deps {
		if !visited[id] {
			if dfs(id) {
				return fmt.Errorf("circular dependency detected involving %s", id)
			}
		}
	}

	return nil
}

// stripThinkBlocks removes <thinking> content before JSON parsing.
func stripThinkBlocks(s string) string {
	for {
		start := strings.Index(s, "<thinking>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</thinking>")
		if end < 0 {
			s = s[:start]
			break
		}
		s = s[:start] + s[end+len("</thinking>"):]
	}
	return s
}

// AssembleSystemPrompt returns the full planner system prompt for debugging/tests.
func AssembleSystemPrompt() string {
	return prompts.Assemble(plannerPrompt, "")
}
